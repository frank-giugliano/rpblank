//go:build !windows

package audio

import "sync"

// fakeBackend stands in on non-Windows builds so the UI and session logic can be exercised.
type fakeBackend struct {
	mu   sync.Mutex
	def  string
	devs []Device
}

func newBackend() Backend {
	return &fakeBackend{def: "{0.0.0}.{tv}", devs: []Device{
		{ID: "{0.0.0}.{steam}", Name: "Speakers (Steam Streaming Speakers)", Active: true, State: "active"},
		{ID: "{0.0.0}.{tv}", Name: "LG TV (NVIDIA High Definition Audio)", Active: true, State: "active"},
		{ID: "{0.0.0}.{desk}", Name: "Speakers (Realtek(R) Audio)", Active: true, State: "active"},
		{ID: "{0.0.0}.{headset}", Name: "Headphones (CORSAIR HS55)", Active: false, State: "unplugged"},
	}}
}

func (f *fakeBackend) List() ([]Device, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]Device(nil), f.devs...)
	for i := range out {
		out[i].Default = out[i].ID == f.def
	}
	return out, nil
}

func (f *fakeBackend) Default() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.def, nil
}

func (f *fakeBackend) SetDefault(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.devs {
		if d.ID == id {
			if !d.Active {
				return ErrNotActive
			}
			f.def = id
			return nil
		}
	}
	return ErrNotActive
}
