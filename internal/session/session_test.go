package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"rpblank/internal/audio"
	"rpblank/internal/config"
	"rpblank/internal/display"
	"rpblank/internal/hooks"
)

// rec is a fake display + audio backend that records what was asked of it.
type rec struct {
	mu      sync.Mutex
	calls   []string
	virtual bool            // Steam's virtual display exists
	def     string          // default audio device
	absent  map[string]bool // audio devices that are not active right now
}

func (r *rec) List() ([]display.Info, error) { return nil, nil }
func (r *rec) DisableOutputs(ids []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.virtual {
		return display.ErrNoOtherDisplay
	}
	for _, id := range ids {
		r.calls = append(r.calls, id+"=off")
	}
	return nil
}
func (r *rec) RestoreOutputs() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, "restore")
	return nil
}
func (r *rec) seq() string  { r.mu.Lock(); defer r.mu.Unlock(); return strings.Join(r.calls, ",") }
func (r *rec) set(f func()) { r.mu.Lock(); f(); r.mu.Unlock() }

// recAudio adapts rec to audio.Backend (List would clash with the display method).
type recAudio struct{ r *rec }

func (a recAudio) List() ([]audio.Device, error) {
	return []audio.Device{{ID: "steam", Name: "Speakers (Steam Streaming Speakers)"}, {ID: "tv", Name: "LG TV"}, {ID: "desk", Name: "Desk speakers"}}, nil
}
func (a recAudio) Default() (string, error) {
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	return a.r.def, nil
}
func (a recAudio) SetDefault(id string) error {
	a.r.mu.Lock()
	defer a.r.mu.Unlock()
	if a.r.absent[id] {
		return audio.ErrNotActive
	}
	a.r.def = id
	a.r.calls = append(a.r.calls, "audio="+id)
	return nil
}

func setup(t *testing.T, mutate func(*config.Config)) (*Controller, *rec, string) {
	outputWait, outputPoll, outputRechecks = 2*time.Second, 20*time.Millisecond, nil
	audioWait, audioPoll = 2*time.Second, 20*time.Millisecond
	dir := t.TempDir()
	st, err := config.Open(filepath.Join(dir, "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := config.Default()
	c.Displays["mon"] = config.Display{ID: "mon", Blank: true}
	c.Displays["tv"] = config.Display{ID: "tv", Blank: false}
	c.GraceSec, c.StopDelaySec = 0, 0
	if mutate != nil {
		mutate(&c)
	}
	if err := st.Set(c); err != nil {
		t.Fatal(err)
	}
	r := &rec{virtual: true, def: "tv", absent: map[string]bool{}}
	state := filepath.Join(dir, "blanked")
	return New(st, r, recAudio{r}, &hooks.Runner{}, state), r, state
}

func wait(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 1500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestStartSwitchesOffOnlyTickedThenStopRestores(t *testing.T) {
	c, r, state := setup(t, nil)
	c.Start("deck")
	wait(t, func() bool { return r.seq() == "mon=off" })
	if _, err := os.Stat(state); err != nil {
		t.Fatal("state file missing while screens are off")
	}
	c.Stop()
	wait(t, func() bool { return !c.Status().Blanked })
	if r.seq() != "mon=off,restore" {
		t.Fatalf("seq %q", r.seq())
	}
	if _, err := os.Stat(state); err == nil {
		t.Fatal("state file should be gone after restore")
	}
}

// The output is only switched off once Steam's virtual display exists.
func TestWaitsForVirtualDisplay(t *testing.T) {
	c, r, _ := setup(t, nil)
	r.set(func() { r.virtual = false })
	c.Start("deck")
	wait(t, func() bool { return c.Status().Blanked })
	time.Sleep(100 * time.Millisecond)
	if r.seq() != "" {
		t.Fatalf("acted before a virtual display existed: %q", r.seq())
	}
	r.set(func() { r.virtual = true })
	wait(t, func() bool { return r.seq() == "mon=off" })
}

// No virtual display ever appears: nothing is changed and the session carries on.
func TestGivesUpWithoutVirtualDisplay(t *testing.T) {
	c, r, _ := setup(t, nil)
	outputWait = 100 * time.Millisecond
	r.set(func() { r.virtual = false })
	c.Start("deck")
	time.Sleep(400 * time.Millisecond)
	if r.seq() != "" {
		t.Fatalf("seq %q", r.seq())
	}
}

// A session resumed inside the grace period cancels the restore and takes the output down
// again: Windows brings it back by itself when Steam removes the virtual display.
func TestResumeInGraceReapplies(t *testing.T) {
	c, r, _ := setup(t, func(c *config.Config) { c.GraceSec = 1 })
	c.Start("deck")
	wait(t, func() bool { return r.seq() == "mon=off" })
	c.Stop()
	if !c.Status().Restoring {
		t.Fatal("should be in grace")
	}
	c.Start("deck")
	wait(t, func() bool { return r.seq() == "mon=off,mon=off" })
	time.Sleep(1300 * time.Millisecond)
	if r.seq() != "mon=off,mon=off" || !c.Status().Blanked {
		t.Fatalf("restore ran despite resume: %q", r.seq())
	}
}

func TestClientFilter(t *testing.T) {
	c, r, _ := setup(t, func(c *config.Config) { c.Clients = []string{"Deck"} })
	c.Start("iphone")
	time.Sleep(100 * time.Millisecond)
	if r.seq() != "" {
		t.Fatalf("filtered client acted: %q", r.seq())
	}
	c.Stop()
	c.Start("deck")
	wait(t, func() bool { return r.seq() == "mon=off" })
}

func TestRecoveryRestoresOnStartup(t *testing.T) {
	dir := t.TempDir()
	st, _ := config.Open(filepath.Join(dir, "c.json"))
	cfg := config.Default()
	cfg.Displays["mon"] = config.Display{ID: "mon", Blank: true}
	cfg.StopDelaySec = 0
	cfg.AudioDisconnect = config.AudioPrevious
	_ = st.Set(cfg)
	state := filepath.Join(dir, "blanked")
	_ = os.WriteFile(state, []byte("x"), 0o644)
	_ = os.WriteFile(state+".audio", []byte("tv"), 0o644)
	r := &rec{def: "steam", absent: map[string]bool{}}
	New(st, r, recAudio{r}, &hooks.Runner{}, state)
	wait(t, func() bool { return r.seq() == "restore,audio=tv" })
	if _, err := os.Stat(state); err == nil {
		t.Fatal("state file should be removed")
	}
}

func TestHooksAndCommandsRunInOrder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	out := filepath.Join(t.TempDir(), "o.txt")
	line := func(s string) string { return `/bin/sh -c "echo ` + s + ` >> ` + out + `"` }
	c, _, _ := setup(t, func(c *config.Config) {
		c.OnConnect = []string{line("connect")}
		c.OffCommands = []string{line("off")}
		c.OnStart = []string{line("start")}
		c.OnCommands = []string{line("on")}
		c.OnStop = []string{line("stop")}
	})
	c.Start("deck")
	wait(t, func() bool {
		d, _ := os.ReadFile(out)
		return strings.Count(string(d), "\n") == 3
	})
	c.Stop()
	wait(t, func() bool {
		d, _ := os.ReadFile(out)
		return strings.Count(string(d), "\n") == 5
	})
	d, _ := os.ReadFile(out)
	if got := strings.TrimSpace(string(d)); got != "connect\noff\nstart\non\nstop" {
		t.Fatalf("order: %q", got)
	}
}

// Named devices: switch on connect, switch to the named one afterwards.
func TestAudioNamedDevices(t *testing.T) {
	c, r, _ := setup(t, func(c *config.Config) {
		c.Displays = map[string]config.Display{}
		c.AudioConnect, c.AudioDisconnect = "steam", "desk"
	})
	c.Start("deck")
	wait(t, func() bool { return r.seq() == "audio=steam" && c.Status().Blanked })
	c.Stop()
	wait(t, func() bool { return r.seq() == "audio=steam,audio=desk" })
}

// "Previous": go back to what was default before, waiting for it to reappear.
func TestAudioPreviousWaitsForDevice(t *testing.T) {
	c, r, _ := setup(t, func(c *config.Config) {
		c.Displays = map[string]config.Display{}
		c.AudioConnect, c.AudioDisconnect = "steam", config.AudioPrevious
	})
	c.Start("deck")
	wait(t, func() bool { return r.seq() == "audio=steam" && c.Status().Blanked })
	r.set(func() { r.absent["tv"] = true }) // HDMI audio gone while the output is off
	c.Stop()
	time.Sleep(200 * time.Millisecond)
	if r.seq() != "audio=steam" {
		t.Fatalf("switched before the device came back: %q", r.seq())
	}
	r.set(func() { delete(r.absent, "tv") })
	wait(t, func() bool { return r.seq() == "audio=steam,audio=tv" })
}

// "Previous" leaves the default alone if it was changed during the session.
func TestAudioPreviousRespectsManualChange(t *testing.T) {
	c, r, _ := setup(t, func(c *config.Config) {
		c.Displays = map[string]config.Display{}
		c.AudioConnect, c.AudioDisconnect = "steam", config.AudioPrevious
	})
	c.Start("deck")
	wait(t, func() bool { return r.seq() == "audio=steam" && c.Status().Blanked })
	r.set(func() { r.def = "desk" })
	c.Stop()
	wait(t, func() bool { return !c.Status().Blanked })
	time.Sleep(150 * time.Millisecond)
	if r.seq() != "audio=steam" || r.def != "desk" {
		t.Fatalf("overrode a manual change: %q def %q", r.seq(), r.def)
	}
}

// A resume keeps the first snapshot, so "previous" is not the stream device.
func TestAudioPreviousSurvivesResume(t *testing.T) {
	c, r, _ := setup(t, func(c *config.Config) {
		c.Displays = map[string]config.Display{}
		c.GraceSec = 1
		c.AudioConnect, c.AudioDisconnect = "steam", config.AudioPrevious
	})
	c.Start("deck")
	wait(t, func() bool { return r.seq() == "audio=steam" && c.Status().Blanked })
	c.Stop()
	c.Start("deck")
	wait(t, func() bool { return r.seq() == "audio=steam,audio=steam" })
	c.Stop()
	wait(t, func() bool { return r.seq() == "audio=steam,audio=steam,audio=tv" })
}

// Audio left alone entirely when nothing is configured.
func TestAudioLeftAlone(t *testing.T) {
	c, r, _ := setup(t, nil)
	c.Start("deck")
	wait(t, func() bool { return r.seq() == "mon=off" })
	c.Stop()
	wait(t, func() bool { return r.seq() == "mon=off,restore" })
	time.Sleep(100 * time.Millisecond)
	if r.seq() != "mon=off,restore" {
		t.Fatalf("seq %q", r.seq())
	}
}
