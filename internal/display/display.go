// Package display enumerates physical monitors and powers them off/on over DDC/CI (VCP D6).
// Nothing here changes the Windows display topology.
package display

import (
	"errors"
	"time"
)

// VCP code D6: power mode. 1 = on, 4 = off (standby/suspend), 5 = power off (some panels).
const (
	VCPPower        = 0xD6
	PowerOn  uint32 = 1
	PowerOff uint32 = 4
)

// Info describes one physical monitor.
type Info struct {
	ID     string `json:"id"`     // stable device path
	Name   string `json:"name"`   // monitor model from EDID, or the GDI name
	Device string `json:"device"` // \\.\DISPLAYn
	DDC    bool   `json:"ddc"`    // answered a VCP capabilities/D6 read
	Power  int    `json:"power"`  // last read D6 value, 0 if unknown
	Err    string `json:"err,omitempty"`
}

var ErrNotSupported = errors.New("DDC/CI not supported on this platform")

// Backend is the OS layer.
type Backend interface {
	// List enumerates monitors and probes DDC on each.
	List() ([]Info, error)
	// SetPower sends VCP D6 = value to the monitor with this id.
	SetPower(id string, value uint32) error
	// Wake nudges the OS so a sleeping display turns on (synthetic input).
	Wake() error
	// ReadPower returns the current D6 value and whether the panel answered.
	ReadPower(id string) (int, bool)
	// IdleSeconds is the time since the last user input, and whether it could be read.
	IdleSeconds() (float64, bool)
	// DisplayRequired reports whether any process is holding the display awake
	// (ES_DISPLAY_REQUIRED): a running game, Big Picture, a video player, a stream host.
	DisplayRequired() bool
}

// New returns the backend for this OS.
func New() Backend { return newBackend() }

// WakeRetries / WakeInterval: a panel coming out of hard-off may ignore the first On
// commands while its DDC listener restarts, so PowerOn is retried.
const (
	WakeRetries  = 5
	WakeInterval = 2 * time.Second
)

// SetPowerRetry sends D6=value repeatedly until a send succeeds, retrying I2C failures
// (no signal on the link yet, typically right after a wake) and, for PowerOn, until a
// follow-up read (where the panel supports reads) no longer reports it off.
func SetPowerRetry(b Backend, id string, value uint32) error {
	var last error
	for i := 0; i < WakeRetries; i++ {
		if i > 0 {
			time.Sleep(WakeInterval)
		}
		if err := b.SetPower(id, value); err != nil {
			last = err
			continue
		}
		last = nil
		if value != PowerOn {
			return nil
		}
		if p, ok := b.ReadPower(id); !ok || p == int(PowerOn) {
			return nil
		}
	}
	return last
}

// SetPowerOn is SetPowerRetry with PowerOn.
func SetPowerOn(b Backend, id string) error { return SetPowerRetry(b, id, PowerOn) }
