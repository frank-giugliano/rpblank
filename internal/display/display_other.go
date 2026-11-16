//go:build !windows

package display

import "sync"

// fakeBackend stands in on non-Windows builds so the session logic and UI can be exercised.
type fakeBackend struct {
	mu       sync.Mutex
	disabled map[string]bool
}

// FakeVirtual mimics Steam's virtual display being present.
var FakeVirtual = false

func newBackend() Backend { return &fakeBackend{disabled: map[string]bool{}} }

func (f *fakeBackend) List() ([]Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Info
	for _, d := range []Info{{ID: `\\?\DISPLAY#FAKE0001#1&abc&0&UID1#{e6f07b5f}`, Name: "Fake Monitor 27"}, {ID: `\\?\DISPLAY#FAKE0002#1&abc&0&UID2#{e6f07b5f}`, Name: "Fake TV"}} {
		if !f.disabled[d.ID] {
			out = append(out, d)
		}
	}
	if FakeVirtual {
		out = append(out, Info{ID: `\\?\DISPLAY#SMK0001#1&abc&0&UID9#{e6f07b5f}`, Name: "steamdeck", Virtual: true})
	}
	return out, nil
}

func (f *fakeBackend) DisableOutputs(ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !FakeVirtual {
		return ErrNoOtherDisplay
	}
	for _, id := range ids {
		f.disabled[id] = true
	}
	return nil
}

func (f *fakeBackend) RestoreOutputs() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disabled = map[string]bool{}
	return nil
}
