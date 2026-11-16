// Package display lists the active Windows outputs and takes chosen ones out of the active
// layout for the length of a Remote Play session, the way a streaming host with a virtual
// display does. The change is never saved to Windows' display database.
package display

import "errors"

// Info describes one active output.
type Info struct {
	ID      string `json:"id"`      // monitor device interface path (stable)
	Name    string `json:"name"`    // monitor name from EDID, where there is one
	Virtual bool   `json:"virtual"` // an indirect (virtual) display such as Steam's
}

// Backend is the OS layer.
type Backend interface {
	// List returns the outputs that are active right now.
	List() ([]Info, error)
	// DisableOutputs deactivates the outputs driving these displays: the panels lose
	// signal. It needs some other active display (Steam's virtual one) to keep the desktop
	// on; without one it returns ErrNoOtherDisplay and changes nothing. Displays that are
	// already inactive are skipped.
	DisableOutputs(ids []string) error
	// RestoreOutputs undoes DisableOutputs: the layout captured before the change if it
	// still applies, otherwise Windows' saved layout for the displays now connected.
	RestoreOutputs() error
}

// ErrNoOtherDisplay: disabling the requested outputs would leave Windows with no display.
var ErrNoOtherDisplay = errors.New("no other active display to keep the desktop on")

// New returns the backend for this OS.
func New() Backend { return newBackend() }
