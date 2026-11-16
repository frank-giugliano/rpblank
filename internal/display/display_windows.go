//go:build windows

package display

import (
	"encoding/binary"
	"fmt"
	"log"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Everything here goes through QueryDisplayConfig / SetDisplayConfig and is applied WITHOUT
// SDC_SAVE_TO_DATABASE, so Windows' remembered layouts are never rewritten: a reboot, or
// SDC_USE_DATABASE_CURRENT, always returns to the user's own layout.

var (
	user32 = windows.NewLazySystemDLL("user32.dll")

	procGetDisplayConfigBufferSizes = user32.NewProc("GetDisplayConfigBufferSizes")
	procQueryDisplayConfig          = user32.NewProc("QueryDisplayConfig")
	procSetDisplayConfig            = user32.NewProc("SetDisplayConfig")
	procDisplayConfigGetDeviceInfo  = user32.NewProc("DisplayConfigGetDeviceInfo")
)

const (
	qdcOnlyActivePaths = 0x00000002

	sdcUseDatabaseCurrent       = 0x0000000F
	sdcUseSuppliedDisplayConfig = 0x00000020
	sdcApply                    = 0x00000080
	sdcAllowChanges             = 0x00000400

	dcModeIdxInvalid       = 0xffffffff
	dcModeInfoTypeSource   = 1
	dcDeviceInfoTargetName = 2

	// DISPLAYCONFIG_OUTPUT_TECHNOLOGY values for indirect (IddCx) displays.
	dcTechIndirectWired   = 16
	dcTechIndirectVirtual = 17

	errorInsufficientBuffer = 122
)

type luid struct {
	Low  uint32
	High int32
}

// DISPLAYCONFIG_PATH_INFO: 20 + 48 + 4 = 72 bytes.
type dcPathSource struct {
	Adapter luid
	ID      uint32
	ModeIdx uint32
	Status  uint32
}

type dcPathTarget struct {
	Adapter    luid
	ID         uint32
	ModeIdx    uint32
	Tech       uint32
	Rotation   uint32
	Scaling    uint32
	RefreshNum uint32
	RefreshDen uint32
	ScanLine   uint32
	Available  int32
	Status     uint32
}

type dcPath struct {
	Source dcPathSource
	Target dcPathTarget
	Flags  uint32
}

// DISPLAYCONFIG_MODE_INFO: 16-byte header + 48-byte union = 64 bytes. For a source mode
// the union holds width, height, pixelFormat, position.x, position.y (5 × 4 bytes).
type dcMode struct {
	InfoType uint32
	ID       uint32
	Adapter  luid
	Data     [48]byte
}

// DISPLAYCONFIG_TARGET_DEVICE_NAME: 420 bytes.
type dcTargetName struct {
	Type      uint32
	Size      uint32
	Adapter   luid
	ID        uint32
	Flags     uint32
	Tech      uint32
	EdidMfg   uint16
	EdidProd  uint16
	Connector uint32
	Friendly  [64]uint16
	Path      [128]uint16
}

func init() {
	// These layouts are passed straight to user32; a wrong size would corrupt the call.
	if unsafe.Sizeof(dcPath{}) != 72 || unsafe.Sizeof(dcMode{}) != 64 || unsafe.Sizeof(dcTargetName{}) != 420 {
		panic("display: DISPLAYCONFIG struct layout mismatch")
	}
}

type winBackend struct{}

func newBackend() Backend { return winBackend{} }

// queryActive returns the active paths and their modes.
func queryActive() ([]dcPath, []dcMode, error) {
	for try := 0; try < 4; try++ {
		var np, nm uint32
		if r, _, _ := procGetDisplayConfigBufferSizes.Call(qdcOnlyActivePaths, uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&nm))); r != 0 {
			return nil, nil, fmt.Errorf("GetDisplayConfigBufferSizes: error %d", r)
		}
		if np == 0 || nm == 0 {
			return nil, nil, nil
		}
		paths := make([]dcPath, np)
		modes := make([]dcMode, nm)
		r, _, _ := procQueryDisplayConfig.Call(qdcOnlyActivePaths,
			uintptr(unsafe.Pointer(&np)), uintptr(unsafe.Pointer(&paths[0])),
			uintptr(unsafe.Pointer(&nm)), uintptr(unsafe.Pointer(&modes[0])), 0)
		if r == errorInsufficientBuffer {
			continue // the topology changed between the two calls
		}
		if r != 0 {
			return nil, nil, fmt.Errorf("QueryDisplayConfig: error %d", r)
		}
		return paths[:np], modes[:nm], nil
	}
	return nil, nil, fmt.Errorf("QueryDisplayConfig: topology kept changing")
}

// targetName returns the monitor's device interface path and friendly name for a path.
func targetName(p *dcPath) (devPath, friendly string) {
	var n dcTargetName
	n.Type = dcDeviceInfoTargetName
	n.Size = uint32(unsafe.Sizeof(n))
	n.Adapter = p.Target.Adapter
	n.ID = p.Target.ID
	if r, _, _ := procDisplayConfigGetDeviceInfo.Call(uintptr(unsafe.Pointer(&n))); r != 0 {
		return "", ""
	}
	return windows.UTF16ToString(n.Path[:]), windows.UTF16ToString(n.Friendly[:])
}

// monitorCode pulls the EDID id (e.g. GSM0001) out of a device interface path.
func monitorCode(devPath string) string {
	parts := strings.Split(devPath, "#")
	if len(parts) > 1 {
		return parts[1]
	}
	return devPath
}

func (winBackend) List() ([]Info, error) {
	paths, _, err := queryActive()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Info
	for i := range paths {
		dev, name := targetName(&paths[i])
		if dev == "" || seen[strings.ToLower(dev)] {
			continue
		}
		seen[strings.ToLower(dev)] = true
		if name == "" {
			name = monitorCode(dev)
		}
		tech := paths[i].Target.Tech
		out = append(out, Info{ID: dev, Name: name, Virtual: tech == dcTechIndirectWired || tech == dcTechIndirectVirtual})
	}
	return out, nil
}

func setConfig(paths []dcPath, modes []dcMode, flags uintptr) error {
	var pp, mp unsafe.Pointer
	if len(paths) > 0 {
		pp = unsafe.Pointer(&paths[0])
	}
	if len(modes) > 0 {
		mp = unsafe.Pointer(&modes[0])
	}
	if r, _, _ := procSetDisplayConfig.Call(uintptr(len(paths)), uintptr(pp), uintptr(len(modes)), uintptr(mp), flags); r != 0 {
		return fmt.Errorf("SetDisplayConfig: error %d", r)
	}
	return nil
}

// snapshot is the layout captured just before the last DisableOutputs that changed anything.
var (
	topoMu    sync.Mutex
	snapPaths []dcPath
	snapModes []dcMode
	// noMatchLogged keeps the "nothing to switch off" line from repeating on rechecks.
	noMatchLogged bool
)

func (winBackend) DisableOutputs(ids []string) error {
	topoMu.Lock()
	defer topoMu.Unlock()
	paths, modes, err := queryActive()
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[strings.ToLower(id)] = true
	}
	var keep []dcPath
	var active []string
	dropped := 0
	for i := range paths {
		dev, _ := targetName(&paths[i])
		active = append(active, dev)
		if want[strings.ToLower(dev)] {
			dropped++
			continue
		}
		keep = append(keep, paths[i])
	}
	if dropped == 0 {
		// Already inactive (Steam or another host disabled it), or the id did not match
		// any active output; the list makes the second case visible in the log.
		if !noMatchLogged {
			noMatchLogged = true
			log.Printf("disable output: %v not among the active outputs %q; nothing changed", ids, active)
		}
		return nil
	}
	if len(keep) == 0 {
		return ErrNoOtherDisplay
	}
	noMatchLogged = false
	log.Printf("disable output: %d active output(s), switching off %d, keeping %d", len(paths), dropped, len(keep))

	// Rebuild the mode table with only the modes the kept paths reference.
	remap := map[uint32]uint32{}
	var newModes []dcMode
	use := func(idx uint32) uint32 {
		if idx == dcModeIdxInvalid || int(idx) >= len(modes) {
			return dcModeIdxInvalid
		}
		if n, ok := remap[idx]; ok {
			return n
		}
		remap[idx] = uint32(len(newModes))
		newModes = append(newModes, modes[idx])
		return remap[idx]
	}
	for i := range keep {
		keep[i].Source.ModeIdx = use(keep[i].Source.ModeIdx)
		keep[i].Target.ModeIdx = use(keep[i].Target.ModeIdx)
	}
	// Windows wants a primary at (0,0). If the display that held the origin was dropped,
	// slide the remaining desktop so the first kept source sits there.
	origin := false
	var first *dcMode
	for i := range newModes {
		if newModes[i].InfoType != dcModeInfoTypeSource {
			continue
		}
		if first == nil {
			first = &newModes[i]
		}
		if binary.LittleEndian.Uint32(newModes[i].Data[12:]) == 0 && binary.LittleEndian.Uint32(newModes[i].Data[16:]) == 0 {
			origin = true
		}
	}
	if !origin && first != nil {
		dx := int32(binary.LittleEndian.Uint32(first.Data[12:]))
		dy := int32(binary.LittleEndian.Uint32(first.Data[16:]))
		for i := range newModes {
			if newModes[i].InfoType != dcModeInfoTypeSource {
				continue
			}
			x := int32(binary.LittleEndian.Uint32(newModes[i].Data[12:])) - dx
			y := int32(binary.LittleEndian.Uint32(newModes[i].Data[16:])) - dy
			binary.LittleEndian.PutUint32(newModes[i].Data[12:], uint32(x))
			binary.LittleEndian.PutUint32(newModes[i].Data[16:], uint32(y))
		}
	}

	if err := setConfig(keep, newModes, sdcApply|sdcUseSuppliedDisplayConfig|sdcAllowChanges); err != nil {
		return err
	}
	snapPaths = append([]dcPath(nil), paths...)
	snapModes = append([]dcMode(nil), modes...)
	return nil
}

func (winBackend) RestoreOutputs() error {
	topoMu.Lock()
	defer topoMu.Unlock()
	if len(snapPaths) > 0 {
		p, m := snapPaths, snapModes
		snapPaths, snapModes = nil, nil
		// Exactly what was there before, valid as long as the same displays still exist
		// (i.e. the virtual display has not been torn down yet).
		if err := setConfig(p, m, sdcApply|sdcUseSuppliedDisplayConfig|sdcAllowChanges); err == nil {
			return nil
		}
	}
	// The virtual display is gone, or rpblank restarted and has no snapshot: ask Windows
	// for its saved layout for whatever is connected now. rpblank never wrote to that
	// database, so this is the user's own layout.
	return setConfig(nil, nil, sdcApply|sdcUseDatabaseCurrent)
}
