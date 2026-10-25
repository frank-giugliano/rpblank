//go:build windows

package display

import (
	"fmt"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32 = windows.NewLazySystemDLL("user32.dll")
	dxva2  = windows.NewLazySystemDLL("dxva2.dll")

	procEnumDisplayMonitors = user32.NewProc("EnumDisplayMonitors")
	procGetMonitorInfoW     = user32.NewProc("GetMonitorInfoW")
	procEnumDisplayDevices  = user32.NewProc("EnumDisplayDevicesW")
	procSendInput           = user32.NewProc("SendInput")

	procGetNumberOfPhysicalMonitors = dxva2.NewProc("GetNumberOfPhysicalMonitorsFromHMONITOR")
	procGetPhysicalMonitors         = dxva2.NewProc("GetPhysicalMonitorsFromHMONITOR")
	procDestroyPhysicalMonitors     = dxva2.NewProc("DestroyPhysicalMonitors")
	procGetVCPFeature               = dxva2.NewProc("GetVCPFeatureAndVCPFeatureReply")
	procSetVCPFeature               = dxva2.NewProc("SetVCPFeature")
	procGetCapabilitiesStringLength = dxva2.NewProc("GetCapabilitiesStringLength")
)

type rect struct{ Left, Top, Right, Bottom int32 }

type monitorInfoEx struct {
	Size    uint32
	Monitor rect
	Work    rect
	Flags   uint32
	Device  [32]uint16
}

type physicalMonitor struct {
	Handle      windows.Handle
	Description [128]uint16
}

type displayDevice struct {
	Size         uint32
	DeviceName   [32]uint16
	DeviceString [128]uint16
	StateFlags   uint32
	DeviceID     [128]uint16
	DeviceKey    [128]uint16
}

const eddGetDeviceInterfaceName = 0x1

type winBackend struct{}

func newBackend() Backend { return winBackend{} }

// enumCallback is created once: syscall.NewCallback has a small process-wide limit, so a
// callback per call would eventually exhaust it. Results are passed through enumSink under
// enumMu rather than captured by the closure.
var (
	enumMu       sync.Mutex
	enumSink     map[string]windows.Handle
	enumCallback = syscall.NewCallback(func(h windows.Handle, _ windows.Handle, _ *rect, _ uintptr) uintptr {
		var mi monitorInfoEx
		mi.Size = uint32(unsafe.Sizeof(mi))
		if r, _, _ := procGetMonitorInfoW.Call(uintptr(h), uintptr(unsafe.Pointer(&mi))); r != 0 {
			enumSink[windows.UTF16ToString(mi.Device[:])] = h
		}
		return 1
	})
)

// hmonitors lists every HMONITOR with its GDI device name (\\.\DISPLAYn).
func hmonitors() (map[string]windows.Handle, error) {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumSink = map[string]windows.Handle{}
	if r, _, err := procEnumDisplayMonitors.Call(0, 0, enumCallback, 0); r == 0 {
		return nil, fmt.Errorf("EnumDisplayMonitors: %w", err)
	}
	out := enumSink
	enumSink = nil
	return out, nil
}

// physical opens the physical monitors behind an HMONITOR; the caller destroys them.
func physical(h windows.Handle) ([]physicalMonitor, error) {
	var n uint32
	if r, _, err := procGetNumberOfPhysicalMonitors.Call(uintptr(h), uintptr(unsafe.Pointer(&n))); r == 0 {
		return nil, fmt.Errorf("GetNumberOfPhysicalMonitorsFromHMONITOR: %w", err)
	}
	if n == 0 {
		return nil, nil
	}
	pm := make([]physicalMonitor, n)
	if r, _, err := procGetPhysicalMonitors.Call(uintptr(h), uintptr(n), uintptr(unsafe.Pointer(&pm[0]))); r == 0 {
		return nil, fmt.Errorf("GetPhysicalMonitorsFromHMONITOR: %w", err)
	}
	return pm, nil
}

func destroy(pm []physicalMonitor) {
	if len(pm) > 0 {
		procDestroyPhysicalMonitors.Call(uintptr(len(pm)), uintptr(unsafe.Pointer(&pm[0])))
	}
}

// monitorDevice returns the i-th monitor attached to a GDI adapter: its stable interface
// path and its EDID model string.
func monitorDevice(adapter string, i int) (id, name string) {
	var dd displayDevice
	dd.Size = uint32(unsafe.Sizeof(dd))
	a, _ := windows.UTF16PtrFromString(adapter)
	if r, _, _ := procEnumDisplayDevices.Call(uintptr(unsafe.Pointer(a)), uintptr(i), uintptr(unsafe.Pointer(&dd)), eddGetDeviceInterfaceName); r == 0 {
		return "", ""
	}
	return windows.UTF16ToString(dd.DeviceID[:]), windows.UTF16ToString(dd.DeviceString[:])
}

func (winBackend) List() ([]Info, error) {
	hs, err := hmonitors()
	if err != nil {
		return nil, err
	}
	var out []Info
	for dev, h := range hs {
		pm, err := physical(h)
		if err != nil {
			out = append(out, Info{ID: dev, Name: dev, Device: dev, Err: err.Error()})
			continue
		}
		for i := range pm {
			id, name := monitorDevice(dev, i)
			if id == "" {
				id = fmt.Sprintf("%s#%d", dev, i)
			}
			desc := strings.TrimSpace(windows.UTF16ToString(pm[i].Description[:]))
			if name == "" || strings.EqualFold(name, "Generic PnP Monitor") {
				if desc != "" {
					name = desc
				}
			}
			info := Info{ID: id, Name: fmt.Sprintf("%s #%d", name, i+1), Device: dev}
			// Probe: D6 read first (gives current power), then brightness (0x10), then the
			// capabilities string. Many monitors accept a D6 write but refuse to report it,
			// so a failed probe only marks DDC as unverified; the Off/On buttons stay usable.
			var cur, max uint32
			if r, _, _ := procGetVCPFeature.Call(uintptr(pm[i].Handle), VCPPower, 0, uintptr(unsafe.Pointer(&cur)), uintptr(unsafe.Pointer(&max))); r != 0 {
				info.DDC = true
				info.Power = int(cur)
			} else if r, _, _ := procGetVCPFeature.Call(uintptr(pm[i].Handle), 0x10, 0, uintptr(unsafe.Pointer(&cur)), uintptr(unsafe.Pointer(&max))); r != 0 {
				info.DDC = true
				info.Err = "power state not readable; brightness is, so writes should work"
			} else {
				var capLen uint32
				if r, _, _ := procGetCapabilitiesStringLength.Call(uintptr(pm[i].Handle), uintptr(unsafe.Pointer(&capLen))); r != 0 && capLen > 0 {
					info.DDC = true
					info.Err = "answers DDC capabilities but not reads; try Off/On"
				} else {
					info.Err = "no DDC/CI reply (check the monitor's DDC/CI menu setting); Off/On may still work"
				}
			}
			out = append(out, info)
		}
		destroy(pm)
	}
	return out, nil
}

func (winBackend) ReadPower(id string) (int, bool) {
	hs, err := hmonitors()
	if err != nil {
		return 0, false
	}
	for dev, h := range hs {
		pm, err := physical(h)
		if err != nil {
			continue
		}
		for i := range pm {
			mid, _ := monitorDevice(dev, i)
			if mid == "" {
				mid = fmt.Sprintf("%s#%d", dev, i)
			}
			if mid != id {
				continue
			}
			var cur, max uint32
			r, _, _ := procGetVCPFeature.Call(uintptr(pm[i].Handle), VCPPower, 0, uintptr(unsafe.Pointer(&cur)), uintptr(unsafe.Pointer(&max)))
			destroy(pm)
			return int(cur), r != 0
		}
		destroy(pm)
	}
	return 0, false
}

func (winBackend) SetPower(id string, value uint32) error {
	hs, err := hmonitors()
	if err != nil {
		return err
	}
	for dev, h := range hs {
		pm, err := physical(h)
		if err != nil {
			continue
		}
		for i := range pm {
			mid, _ := monitorDevice(dev, i)
			if mid == "" {
				mid = fmt.Sprintf("%s#%d", dev, i)
			}
			if mid != id {
				continue
			}
			r, _, callErr := procSetVCPFeature.Call(uintptr(pm[i].Handle), VCPPower, uintptr(value))
			destroy(pm)
			if r == 0 {
				return fmt.Errorf("SetVCPFeature(D6=%d) on %s: %w", value, id, callErr)
			}
			return nil
		}
		destroy(pm)
	}
	return fmt.Errorf("display %s not found (unplugged or asleep?)", id)
}

// INPUT with the MOUSEINPUT arm (the largest union member): 40 bytes on x64, union at +8.
type mouseInput struct {
	Type  uint32
	_     uint32
	Dx    int32
	Dy    int32
	Data  uint32
	Flags uint32
	Time  uint32
	_     uint32
	Extra uintptr
}

var (
	kernel32                    = windows.NewLazySystemDLL("kernel32.dll")
	procSetThreadExecutionState = kernel32.NewProc("SetThreadExecutionState")
	procSendMessageTimeoutW     = user32.NewProc("SendMessageTimeoutW")
	procGetLastInputInfo        = user32.NewProc("GetLastInputInfo")
	procGetTickCount            = kernel32.NewProc("GetTickCount")
	powrprof                    = windows.NewLazySystemDLL("powrprof.dll")
	procCallNtPowerInformation  = powrprof.NewProc("CallNtPowerInformation")
)

// DisplayRequired reads the system-wide execution state: the OR of every process's
// SetThreadExecutionState requests. Games and Steam set ES_DISPLAY_REQUIRED while playing,
// which is what keeps Windows' own display timeout from firing mid-game.
func (winBackend) DisplayRequired() bool {
	const (
		systemExecutionState = 16
		esDisplayRequired    = 0x00000002
	)
	var state uint32
	if r, _, _ := procCallNtPowerInformation.Call(systemExecutionState, 0, 0, uintptr(unsafe.Pointer(&state)), 4); r != 0 {
		return false // NTSTATUS failure: assume not required rather than never blanking
	}
	return state&esDisplayRequired != 0
}

type lastInputInfo struct {
	Size uint32
	Time uint32
}

// IdleSeconds reads the time since the last keyboard/mouse input (real or injected).
func (winBackend) IdleSeconds() (float64, bool) {
	var li lastInputInfo
	li.Size = uint32(unsafe.Sizeof(li))
	if r, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&li))); r == 0 {
		return 0, false
	}
	now, _, _ := procGetTickCount.Call()
	// 32-bit tick arithmetic wraps correctly on uint32 subtraction.
	return float64(uint32(now)-li.Time) / 1000, true
}

// Wake turns a DPMS-sleeping display back on. Three mechanisms, because no single one is
// reliable everywhere: a one-pixel mouse jiggle (resets the idle timer and wakes the output
// like a real user would), the SC_MONITORPOWER "on" broadcast (what nircmd sends), and
// ES_DISPLAY_REQUIRED (tells the power manager the display is needed right now).
func (winBackend) Wake() error {
	const (
		inputMouse        = 0
		mouseeventfMove   = 0x0001
		esDisplayRequired = 0x00000002
		hwndBroadcast     = 0xffff
		wmSyscommand      = 0x0112
		scMonitorpower    = 0xF170
		smtoAbortIfHung   = 0x0002
	)
	var firstErr error
	in := []mouseInput{
		{Type: inputMouse, Dx: 1, Dy: 0, Flags: mouseeventfMove},
		{Type: inputMouse, Dx: -1, Dy: 0, Flags: mouseeventfMove},
	}
	if r, _, err := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0])); r == 0 {
		firstErr = fmt.Errorf("SendInput: %w", err)
	}
	procSetThreadExecutionState.Call(esDisplayRequired)
	// -1 = on. SendMessageTimeout so a hung window can't block us.
	procSendMessageTimeoutW.Call(hwndBroadcast, wmSyscommand, scMonitorpower, ^uintptr(0), smtoAbortIfHung, 1000, 0)
	return firstErr
}
