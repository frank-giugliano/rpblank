package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"rpblank/internal/config"
	"rpblank/internal/display"
	"rpblank/internal/hooks"
)

type rec struct {
	mu    sync.Mutex
	calls []string
	wakes int
	idle  float64
	req   bool
	fail  bool // SetPower returns an error (no link) while set
}

func (r *rec) DisplayRequired() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.req }

func (r *rec) setIdle(v float64) { r.mu.Lock(); r.idle = v; r.mu.Unlock() }

func (r *rec) List() ([]display.Info, error) { return nil, nil }
func (r *rec) SetPower(id string, v uint32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("I2C: no link")
	}
	r.calls = append(r.calls, id+"="+map[uint32]string{1: "on", 4: "off", 5: "hard"}[v])
	return nil
}
func (r *rec) Wake() error                  { r.mu.Lock(); r.wakes++; r.mu.Unlock(); return nil }
func (r *rec) ReadPower(string) (int, bool) { return 1, true }
func (r *rec) IdleSeconds() (float64, bool) { r.mu.Lock(); defer r.mu.Unlock(); return r.idle, true }
func (r *rec) seq() string                  { r.mu.Lock(); defer r.mu.Unlock(); return strings.Join(r.calls, ",") }

func setup(t *testing.T, mutate func(*config.Config)) (*Controller, *rec, string) {
	dir := t.TempDir()
	st, err := config.Open(filepath.Join(dir, "c.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := config.Default()
	c.Displays["mon"] = config.Display{ID: "mon", Blank: true}
	c.Displays["tv"] = config.Display{ID: "tv", Blank: false}
	c.GraceSec, c.StopDelaySec, c.WakeSettleSec = 0, 0, 0
	if mutate != nil {
		mutate(&c)
	}
	if err := st.Set(c); err != nil {
		t.Fatal(err)
	}
	r := &rec{}
	state := filepath.Join(dir, "blanked")
	return New(st, r, &hooks.Runner{}, state), r, state
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

func TestStartBlanksOnlySelectedThenStopRestores(t *testing.T) {
	c, r, state := setup(t, nil)
	c.Start("deck")
	wait(t, func() bool { return c.Status().Blanked })
	if r.seq() != "mon=off" || r.wakes != 1 {
		t.Fatalf("seq %q wakes %d", r.seq(), r.wakes)
	}
	if _, err := os.Stat(state); err != nil {
		t.Fatal("state file missing while blanked")
	}
	c.Stop()
	wait(t, func() bool { return !c.Status().Blanked })
	if r.seq() != "mon=off,mon=on" {
		t.Fatalf("seq %q", r.seq())
	}
	if _, err := os.Stat(state); err == nil {
		t.Fatal("state file should be gone after restore")
	}
}

func TestGraceCancelledByResume(t *testing.T) {
	c, r, _ := setup(t, func(c *config.Config) { c.GraceSec = 1 })
	c.Start("deck")
	wait(t, func() bool { return c.Status().Blanked })
	c.Stop()
	if !c.Status().Restoring {
		t.Fatal("should be in grace")
	}
	c.Start("deck")
	time.Sleep(1300 * time.Millisecond)
	if r.seq() != "mon=off" || !c.Status().Blanked {
		t.Fatalf("resume should cancel restore: %q", r.seq())
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
	wait(t, func() bool { return c.Status().Blanked })
}

func TestRecoveryRestoresOnStartup(t *testing.T) {
	dir := t.TempDir()
	st, _ := config.Open(filepath.Join(dir, "c.json"))
	cfg := config.Default()
	cfg.Displays["mon"] = config.Display{ID: "mon", Blank: true}
	cfg.StopDelaySec, cfg.WakeSettleSec = 0, 0
	_ = st.Set(cfg)
	state := filepath.Join(dir, "blanked")
	_ = os.WriteFile(state, []byte("x"), 0o644)
	r := &rec{}
	New(st, r, &hooks.Runner{}, state)
	wait(t, func() bool { return r.seq() == "mon=on" })
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
		c.OffCommands = []string{line("off")}
		c.OnStart = []string{line("start")}
		c.OnCommands = []string{line("on")}
		c.OnStop = []string{line("stop")}
	})
	c.Start("deck")
	wait(t, func() bool { return c.Status().Blanked })
	c.Stop()
	wait(t, func() bool {
		d, _ := os.ReadFile(out)
		return strings.Count(string(d), "\n") == 4
	})
	d, _ := os.ReadFile(out)
	if got := strings.TrimSpace(string(d)); got != "off\nstart\non\nstop" {
		t.Fatalf("order: %q", got)
	}
}

func TestIdleBlankAndRestoreOnInput(t *testing.T) {
	c, r, _ := setup(t, func(c *config.Config) { c.IdleBlankMin = 1 })
	stop := make(chan struct{})
	defer close(stop)
	go c.RunIdle(stop)
	r.setIdle(30)
	time.Sleep(2500 * time.Millisecond)
	if c.Status().Blanked {
		t.Fatal("blanked before the idle threshold")
	}
	r.setIdle(61)
	wait(t, func() bool { return c.Status().Blanked && c.Status().IdleBlank })
	if r.seq() != "mon=off" || r.wakes != 0 {
		t.Fatalf("idle blank should not wake: seq %q wakes %d", r.seq(), r.wakes)
	}
	// A session starting while idle-blanked keeps the panel dark and does not wake.
	c.Start("deck")
	time.Sleep(200 * time.Millisecond)
	if r.wakes != 0 || r.seq() != "mon=off" || c.Status().IdleBlank {
		t.Fatalf("session over idle-blank: seq %q wakes %d idle %v", r.seq(), r.wakes, c.Status().IdleBlank)
	}
	c.Stop()
	wait(t, func() bool { return !c.Status().Blanked })
	// Idle-blank again, then input restores without hooks.
	r.setIdle(61)
	wait(t, func() bool { return c.Status().IdleBlank })
	r.setIdle(0)
	wait(t, func() bool { return !c.Status().Blanked })
	if r.seq() != "mon=off,mon=on,mon=off,mon=on" {
		t.Fatalf("seq %q", r.seq())
	}
}

func TestIdleBlankSkippedWhileDisplayRequired(t *testing.T) {
	c, r, _ := setup(t, func(c *config.Config) { c.IdleBlankMin = 1 })
	stop := make(chan struct{})
	defer close(stop)
	go c.RunIdle(stop)
	r.mu.Lock()
	r.req, r.idle = true, 600
	r.mu.Unlock()
	time.Sleep(2500 * time.Millisecond)
	if c.Status().Blanked {
		t.Fatal("blanked while a game held the display")
	}
	r.mu.Lock()
	r.req = false
	r.mu.Unlock()
	wait(t, func() bool { return c.Status().IdleBlank })
}

// Idle-blanked, then the physical display goes away (Vibeshine session): the "on" fails,
// rpblank keeps the blanked state and retries until the display is back.
func TestIdleRestoreRetriesUntilLinkReturns(t *testing.T) {
	c, r, _ := setup(t, func(c *config.Config) { c.IdleBlankMin = 1 })
	stop := make(chan struct{})
	defer close(stop)
	go c.RunIdle(stop)
	r.setIdle(61)
	wait(t, func() bool { return c.Status().IdleBlank })
	time.Sleep(3200 * time.Millisecond) // past the post-blank input guard
	r.mu.Lock()
	r.fail, r.idle = true, 0 // input arrives, but no link
	r.mu.Unlock()
	wait(t, func() bool { return c.Status().RestorePending })
	if !c.Status().Blanked {
		t.Fatal("blanked state dropped despite failed restore")
	}
	r.mu.Lock()
	r.fail = false
	r.mu.Unlock()
	wait(t, func() bool { return !c.Status().Blanked })
	if r.seq() != "mon=off,mon=on" {
		t.Fatalf("seq %q", r.seq())
	}
}
