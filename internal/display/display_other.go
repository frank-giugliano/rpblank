//go:build !windows

package display

import (
	"fmt"
	"sync"
)

// fakeBackend stands in on non-Windows builds so the session logic and UI can be exercised.
type fakeBackend struct {
	mu    sync.Mutex
	power map[string]uint32
}

// FakeIdle is the idle time the non-Windows stub reports (tests and the Linux smoke run).
var FakeIdle = 0.0

func newBackend() Backend {
	return &fakeBackend{power: map[string]uint32{"fake-monitor": 1, "fake-tv": 1}}
}

func (f *fakeBackend) List() ([]Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []Info{
		{ID: "fake-monitor", Name: "Fake Monitor 27", Device: `\\.\DISPLAY1`, DDC: true, Power: int(f.power["fake-monitor"])},
		{ID: "fake-tv", Name: "Fake TV", Device: `\\.\DISPLAY1`, DDC: true, Power: int(f.power["fake-tv"])},
		{ID: "fake-noddc", Name: "Old VGA", Device: `\\.\DISPLAY2`, DDC: false, Err: "no DDC/CI reply for power (D6)"},
	}, nil
}

func (f *fakeBackend) SetPower(id string, value uint32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.power[id]; !ok {
		return fmt.Errorf("display %s not found", id)
	}
	f.power[id] = value
	return nil
}

func (f *fakeBackend) Wake() error { FakeIdle = 0; return nil }

func (f *fakeBackend) IdleSeconds() (float64, bool) { return FakeIdle, true }

// FakeDisplayRequired mimics a game holding the display awake.
var FakeDisplayRequired = false

func (f *fakeBackend) DisplayRequired() bool { return FakeDisplayRequired }

func (f *fakeBackend) ReadPower(id string) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.power[id]
	return int(v), ok
}
