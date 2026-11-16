//go:build windows

package audio

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Enumeration uses the documented MMDevice API. Setting the default uses IPolicyConfig,
// which Microsoft has never documented but which has been stable since Windows 7 and is
// what every audio switcher (SoundVolumeView, EarTrumpet, Sunshine) calls.

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")
	procPropVariantClear = ole32.NewProc("PropVariantClear")

	clsidMMDeviceEnumerator = windows.GUID{Data1: 0xBCDE0395, Data2: 0xE52F, Data3: 0x467C, Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E}}
	iidIMMDeviceEnumerator  = windows.GUID{Data1: 0xA95664D2, Data2: 0x9614, Data3: 0x4F35, Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6}}
	clsidPolicyConfigClient = windows.GUID{Data1: 0x870AF99C, Data2: 0x171D, Data3: 0x4F9E, Data4: [8]byte{0xAF, 0x0D, 0xE6, 0x3D, 0xF4, 0x0C, 0x2B, 0xC9}}
	iidIPolicyConfig        = windows.GUID{Data1: 0xF8679F50, Data2: 0x850A, Data3: 0x41CF, Data4: [8]byte{0x9C, 0x72, 0x43, 0x0F, 0x29, 0x02, 0x90, 0xC8}}
)

const (
	clsctxAll = 0x17
	eRender   = 0
	eConsole  = 0
	stgmRead  = 0
	vtLPWSTR  = 31

	stateActive     = 0x1
	stateDisabled   = 0x2
	stateNotPresent = 0x4
	stateUnplugged  = 0x8
	stateAll        = 0xF

	// vtable slots (after the three IUnknown methods).
	unkRelease = 2

	enumEnumAudioEndpoints      = 3
	enumGetDefaultAudioEndpoint = 4
	enumGetDevice               = 5

	collGetCount = 3
	collItem     = 4

	devOpenPropertyStore = 4
	devGetId             = 5
	devGetState          = 6

	propsGetValue = 5

	policySetDefaultEndpoint = 13
)

// PKEY_Device_FriendlyName.
type propertyKey struct {
	Fmtid windows.GUID
	Pid   uint32
}

var pkeyFriendlyName = propertyKey{windows.GUID{Data1: 0xA45C254E, Data2: 0xDF1C, Data3: 0x4EFD, Data4: [8]byte{0x80, 0x20, 0x67, 0xD1, 0x46, 0xA8, 0x50, 0xE0}}, 14}

// PROPVARIANT on x64: 8-byte header, 16-byte union.
type propVariant struct {
	VT  uint16
	_   [6]byte
	Ptr *uint16
	_   uintptr
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

// vcall invokes method slot idx on a COM object.
func vcall(obj unsafe.Pointer, idx int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func release(obj unsafe.Pointer) {
	if obj != nil {
		vcall(obj, unkRelease)
	}
}

// COM calls run on one dedicated OS thread that initialised COM once.
var (
	comOnce sync.Once
	comCh   = make(chan func())
)

func comDo(f func() error) error {
	comOnce.Do(func() {
		go func() {
			runtime.LockOSThread()
			procCoInitializeEx.Call(0, 0) // COINIT_MULTITHREADED
			for fn := range comCh {
				fn()
			}
		}()
	})
	done := make(chan error, 1)
	comCh <- func() { done <- f() }
	return <-done
}

func create(clsid, iid *windows.GUID) (unsafe.Pointer, error) {
	var obj unsafe.Pointer
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(clsid)), 0, clsctxAll, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&obj)))
	if failed(hr) || obj == nil {
		return nil, fmt.Errorf("CoCreateInstance: 0x%08x", uint32(hr))
	}
	return obj, nil
}

// deviceID reads an IMMDevice's endpoint id.
func deviceID(dev unsafe.Pointer) string {
	var p *uint16
	if hr := vcall(dev, devGetId, uintptr(unsafe.Pointer(&p))); failed(hr) || p == nil {
		return ""
	}
	s := windows.UTF16PtrToString(p)
	procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
	return s
}

func deviceName(dev unsafe.Pointer) string {
	var store unsafe.Pointer
	if hr := vcall(dev, devOpenPropertyStore, stgmRead, uintptr(unsafe.Pointer(&store))); failed(hr) || store == nil {
		return ""
	}
	defer release(store)
	var pv propVariant
	if hr := vcall(store, propsGetValue, uintptr(unsafe.Pointer(&pkeyFriendlyName)), uintptr(unsafe.Pointer(&pv))); failed(hr) {
		return ""
	}
	defer procPropVariantClear.Call(uintptr(unsafe.Pointer(&pv)))
	if pv.VT != vtLPWSTR || pv.Ptr == nil {
		return ""
	}
	return windows.UTF16PtrToString(pv.Ptr)
}

func stateName(s uint32) string {
	switch {
	case s&stateActive != 0:
		return "active"
	case s&stateDisabled != 0:
		return "disabled"
	case s&stateUnplugged != 0:
		return "unplugged"
	default:
		return "not present"
	}
}

func defaultID(enum unsafe.Pointer) string {
	var dev unsafe.Pointer
	if hr := vcall(enum, enumGetDefaultAudioEndpoint, eRender, eConsole, uintptr(unsafe.Pointer(&dev))); failed(hr) || dev == nil {
		return "" // no playback device at all
	}
	defer release(dev)
	return deviceID(dev)
}

type winBackend struct{}

func newBackend() Backend { return winBackend{} }

func (winBackend) List() (out []Device, err error) {
	err = comDo(func() error {
		enum, err := create(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator)
		if err != nil {
			return err
		}
		defer release(enum)
		def := defaultID(enum)
		var coll unsafe.Pointer
		if hr := vcall(enum, enumEnumAudioEndpoints, eRender, stateAll, uintptr(unsafe.Pointer(&coll))); failed(hr) || coll == nil {
			return fmt.Errorf("EnumAudioEndpoints: 0x%08x", uint32(hr))
		}
		defer release(coll)
		var n uint32
		if hr := vcall(coll, collGetCount, uintptr(unsafe.Pointer(&n))); failed(hr) {
			return fmt.Errorf("GetCount: 0x%08x", uint32(hr))
		}
		for i := uint32(0); i < n; i++ {
			var dev unsafe.Pointer
			if hr := vcall(coll, collItem, uintptr(i), uintptr(unsafe.Pointer(&dev))); failed(hr) || dev == nil {
				continue
			}
			id := deviceID(dev)
			var st uint32
			vcall(dev, devGetState, uintptr(unsafe.Pointer(&st)))
			name := deviceName(dev)
			release(dev)
			if id == "" {
				continue
			}
			if name == "" {
				name = id
			}
			out = append(out, Device{ID: id, Name: name, Active: st&stateActive != 0, State: stateName(st), Default: strings.EqualFold(id, def)})
		}
		return nil
	})
	return out, err
}

func (winBackend) Default() (id string, err error) {
	err = comDo(func() error {
		enum, err := create(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator)
		if err != nil {
			return err
		}
		defer release(enum)
		id = defaultID(enum)
		return nil
	})
	return id, err
}

func (winBackend) SetDefault(id string) error {
	return comDo(func() error {
		wid, err := windows.UTF16PtrFromString(id)
		if err != nil {
			return err
		}
		enum, err := create(&clsidMMDeviceEnumerator, &iidIMMDeviceEnumerator)
		if err != nil {
			return err
		}
		defer release(enum)
		var dev unsafe.Pointer
		if hr := vcall(enum, enumGetDevice, uintptr(unsafe.Pointer(wid)), uintptr(unsafe.Pointer(&dev))); failed(hr) || dev == nil {
			return ErrNotActive // unknown to Windows right now
		}
		var st uint32
		vcall(dev, devGetState, uintptr(unsafe.Pointer(&st)))
		release(dev)
		if st&stateActive == 0 {
			return ErrNotActive
		}
		policy, err := create(&clsidPolicyConfigClient, &iidIPolicyConfig)
		if err != nil {
			return fmt.Errorf("IPolicyConfig: %w", err)
		}
		defer release(policy)
		for role := uintptr(0); role < 3; role++ { // eConsole, eMultimedia, eCommunications
			if hr := vcall(policy, policySetDefaultEndpoint, uintptr(unsafe.Pointer(wid)), role); failed(hr) {
				return fmt.Errorf("SetDefaultEndpoint(role %d): 0x%08x", role, uint32(hr))
			}
		}
		return nil
	})
}
