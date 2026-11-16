// Package audio lists playback devices and sets the Windows default one, so a session can
// move sound to the stream device on connect and back to a speaker or TV afterwards.
package audio

import "errors"

// Device is one playback (render) endpoint.
type Device struct {
	ID      string `json:"id"`      // endpoint id, stable across renames and driver updates
	Name    string `json:"name"`    // "Speakers (Realtek(R) Audio)"
	Active  bool   `json:"active"`  // present and usable right now
	State   string `json:"state"`   // active, disabled, unplugged, not present
	Default bool   `json:"default"` // the current default playback device
}

// Backend is the OS layer.
type Backend interface {
	// List returns every playback endpoint Windows knows, including ones that are
	// unplugged or not present right now (a TV's HDMI audio while its output is off).
	List() ([]Device, error)
	// Default returns the id of the current default playback device ("" if none).
	Default() (string, error)
	// SetDefault makes the device the default for every role (console, multimedia,
	// communications). It returns ErrNotActive if the device is not usable right now.
	SetDefault(id string) error
}

// ErrNotActive: the device exists but is not present/enabled at the moment.
var ErrNotActive = errors.New("audio device is not active right now")

// New returns the backend for this OS.
func New() Backend { return newBackend() }
