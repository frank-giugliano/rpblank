// Package session turns Remote Play start/stop events into blank/restore actions.
//
// Start: optional wake → blank each selected display (DDC D6) → off-commands → on-start hooks.
// Stop:  wait the grace period (a start during it cancels) → restore displays → on-commands
//
//	→ wait the stop delay → on-stop hooks.
//
// A small state file records that displays are blanked so a crash or restart can restore them.
package session

import (
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"rpblank/internal/config"
	"rpblank/internal/display"
	"rpblank/internal/hooks"
)

// Status is what the UI shows.
type Status struct {
	Active    bool `json:"active"`
	IdleBlank bool `json:"idleBlank"` // blanked by the idle timer rather than a session
	// RestorePending: an "on" failed for lack of a display link; the idle loop keeps retrying.
	RestorePending bool      `json:"restorePending"`
	Client         string    `json:"client"`
	Blanked        bool      `json:"blanked"`
	Restoring      bool      `json:"restoring"` // in the grace period
	LastStart      time.Time `json:"lastStart"`
	LastStop       time.Time `json:"lastStop"`
	LastAction     string    `json:"lastAction"`
}

type Controller struct {
	cfg   *config.Store
	disp  display.Backend
	hooks *hooks.Runner
	state string // path of the blanked-marker file

	mu        sync.Mutex
	st        Status
	timer     *time.Timer
	blankedAt time.Time // when the idle blank happened; input within 3 s of it is ignored
	ignored   string    // client name skipped by the filter, for the log
}

func New(cfg *config.Store, disp display.Backend, hk *hooks.Runner, statePath string) *Controller {
	c := &Controller{cfg: cfg, disp: disp, hooks: hk, state: statePath}
	if _, err := os.Stat(statePath); err == nil {
		log.Println("displays were left blanked by a previous run; restoring")
		c.restore("recovery")
	}
	return c
}

func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.st
}

func (c *Controller) allowed(client string) bool {
	cfg := c.cfg.Get()
	if len(cfg.Clients) == 0 {
		return true
	}
	for _, name := range cfg.Clients {
		if strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(client)) {
			return true
		}
	}
	return false
}

// Start handles a session beginning for client.
func (c *Controller) Start(client string) {
	c.mu.Lock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
		log.Printf("session resumed by %q before restore; keeping displays blanked", client)
	}
	alreadyBlanked := c.st.Blanked
	c.st.Active, c.st.Client, c.st.Restoring, c.st.LastStart = true, client, false, time.Now()
	c.mu.Unlock()

	if !c.allowed(client) {
		log.Printf("session from %q ignored (not in client list)", client)
		return
	}
	// Connect commands fire first and synchronously: pinning the audio device has to beat
	// Steam opening its capture, which happens within a second or two of this log line.
	if cfg := c.cfg.Get(); len(cfg.OnConnect) > 0 {
		c.hooks.Run("connect", client, cfg.OnConnect, c.timeout())
	}
	if alreadyBlanked {
		// Idle-blanked (or resumed): the panel is already dark and the desktop is live.
		c.mu.Lock()
		c.st.IdleBlank = false
		c.mu.Unlock()
		return
	}
	go c.blank(client)
}

// IdleBlankNow blanks as the idle timer would (no wake, no session hooks) so the input-wake
// path can be tested without waiting. Returns false if a session or restore is in progress.
func (c *Controller) IdleBlankNow() bool {
	cfg := c.cfg.Get()
	c.mu.Lock()
	if c.st.Active || c.st.Restoring || c.timer != nil || c.st.Blanked {
		c.mu.Unlock()
		return false
	}
	c.mu.Unlock()
	log.Println("idle blank requested from the UI")
	n := c.applyBlank(cfg)
	c.mu.Lock()
	c.st.Blanked, c.st.IdleBlank, c.st.LastAction = true, true, "idle-blanked (test)"
	c.blankedAt = time.Now()
	c.mu.Unlock()
	log.Printf("idle-blanked: %d action(s)", n)
	return true
}

// RunIdle blanks the displays after IdleBlankMin minutes without input (outside sessions)
// and restores them on the next input. It replaces Windows' own display sleep, which would
// leave Steam's capture black when a session starts against a sleeping desktop.
func (c *Controller) RunIdle(stop <-chan struct{}) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		cfg := c.cfg.Get()
		if cfg.IdleBlankMin <= 0 || !anyBlank(cfg) {
			continue
		}
		c.mu.Lock()
		st := c.st
		busy := c.timer != nil
		c.mu.Unlock()
		if st.Active || st.Restoring || busy {
			continue
		}
		idle, ok := c.disp.IdleSeconds()
		if !ok {
			continue
		}
		switch {
		case !st.Blanked && idle >= float64(cfg.IdleBlankMin)*60:
			if c.disp.DisplayRequired() {
				continue // a game, Big Picture, a video or a stream host is keeping the display awake
			}
			log.Printf("idle for %dm: blanking", cfg.IdleBlankMin)
			n := c.applyBlank(cfg)
			c.mu.Lock()
			c.st.Blanked, c.st.IdleBlank, c.st.LastAction = true, true, "idle-blanked"
			c.blankedAt = time.Now()
			c.mu.Unlock()
			log.Printf("idle-blanked: %d action(s)", n)
		case st.Blanked && st.IdleBlank && st.RestorePending:
			// An earlier "on" could not reach the panel (typically Vibeshine had the physical
			// display disabled for a Moonlight session). Keep trying until the link is back.
			if c.restoreDisplaysOnce(cfg) {
				c.mu.Lock()
				c.st.Blanked, c.st.IdleBlank, c.st.RestorePending, c.st.LastAction = false, false, false, "restored (retry)"
				c.mu.Unlock()
				log.Println("restored: display reachable again")
			}
		case st.Blanked && st.IdleBlank && idle < 3 && time.Since(c.idleBlankedAt()) > 3*time.Second:
			log.Println("input after idle: restoring")
			n, ok := c.applyRestoreChecked(cfg)
			c.mu.Lock()
			if ok {
				c.st.Blanked, c.st.IdleBlank, c.st.LastAction = false, false, "restored (input)"
			} else {
				c.st.RestorePending, c.st.LastAction = true, "restore pending (no display link)"
			}
			c.mu.Unlock()
			if ok {
				log.Printf("restored: %d action(s)", n)
			} else {
				log.Println("restore failed: no DDC link; will retry until the display is back")
			}
		}
	}
}

// Stop handles the session ending.
func (c *Controller) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.st.Active {
		return
	}
	c.st.Active, c.st.LastStop = false, time.Now()
	if !c.st.Blanked {
		return
	}
	grace := time.Duration(c.cfg.Get().GraceSec) * time.Second
	c.st.Restoring = true
	client := c.st.Client
	if grace <= 0 {
		go c.restore(client)
		return
	}
	log.Printf("session ended; restoring displays in %s unless it resumes", grace)
	c.timer = time.AfterFunc(grace, func() {
		c.mu.Lock()
		if c.timer == nil {
			c.mu.Unlock()
			return
		}
		c.timer = nil
		c.mu.Unlock()
		c.restore(client)
	})
}

// Blank / Restore are the manual (UI) entry points.
func (c *Controller) Blank() { c.blank("manual") }
func (c *Controller) Restore() {
	c.mu.Lock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	c.mu.Unlock()
	c.restore("manual")
}

// shortID trims a Windows device path to its monitor id for readable logs.
func shortID(id string) string {
	if i := strings.Index(id, "#"); i >= 0 {
		rest := id[i+1:]
		if j := strings.Index(rest, "#"); j >= 0 {
			return rest[:j]
		}
	}
	return id
}

func anyBlank(cfg config.Config) bool {
	for _, d := range cfg.Displays {
		if d.Blank {
			return true
		}
	}
	return false
}

func (c *Controller) idleBlankedAt() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.blankedAt
}

func (c *Controller) timeout() time.Duration {
	return time.Duration(c.cfg.Get().TimeoutSec) * time.Second
}

// applyBlank powers the selected displays down and runs the off-commands.
func (c *Controller) applyBlank(cfg config.Config) int {
	_ = os.WriteFile(c.state, []byte(time.Now().Format(time.RFC3339)), 0o644)
	n := 0
	for id, d := range cfg.Displays {
		if !d.Blank {
			continue
		}
		v := uint32(d.OffValue)
		if v == 0 {
			v = display.PowerOff
		}
		if err := display.SetPowerRetry(c.disp, id, v); err != nil {
			log.Printf("blank %s: %v (no DDC link: display still asleep, or unplugged?)", shortID(id), err)
		} else {
			n++
		}
	}
	for _, line := range cfg.OffCommands {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := hooks.RunOne(line, c.timeout(), c.hooks.LogPath); err == nil {
			n++
		}
	}
	return n
}

// blank is the session path: wake, settle, blank, start-hooks.
func (c *Controller) blank(client string) {
	cfg := c.cfg.Get()
	if cfg.WakeDisplay {
		if err := c.disp.Wake(); err != nil {
			log.Println("wake:", err)
		}
		if d := time.Duration(cfg.WakeSettleSec) * time.Second; d > 0 && anyBlank(cfg) {
			log.Printf("waiting %s for the display to settle after wake", d)
			time.Sleep(d)
		}
	}
	n := c.applyBlank(cfg)
	log.Printf("blanked: %d action(s) for %q", n, client)
	c.mu.Lock()
	c.st.Blanked, c.st.IdleBlank, c.st.LastAction = true, false, "blanked"
	c.mu.Unlock()
	c.hooks.Run("start", client, cfg.OnStart, c.timeout())
}

// applyRestore powers the selected displays back on and runs the on-commands.
func (c *Controller) applyRestore(cfg config.Config) int {
	n, _ := c.applyRestoreChecked(cfg)
	return n
}

// restoreDisplaysOnce sends a single "on" to each ticked display; true if all succeeded.
func (c *Controller) restoreDisplaysOnce(cfg config.Config) bool {
	ok := true
	for id, d := range cfg.Displays {
		if !d.Blank {
			continue
		}
		if err := c.disp.SetPower(id, display.PowerOn); err != nil {
			ok = false
		}
	}
	if ok {
		_ = os.Remove(c.state)
	}
	return ok
}

// applyRestoreChecked is applyRestore that also reports whether every ticked display
// accepted the "on" (false means at least one had no DDC link).
func (c *Controller) applyRestoreChecked(cfg config.Config) (int, bool) {
	n, ok := 0, true
	for id, d := range cfg.Displays {
		if !d.Blank {
			continue
		}
		if err := display.SetPowerOn(c.disp, id); err != nil {
			log.Printf("restore %s: %v", shortID(id), err)
			ok = false
		} else {
			n++
		}
	}
	for _, line := range cfg.OnCommands {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := hooks.RunOne(line, c.timeout(), c.hooks.LogPath); err == nil {
			n++
		}
	}
	if ok {
		_ = os.Remove(c.state)
	}
	return n, ok
}

// restore is the session path: wake, settle, restore, stop-hooks.
func (c *Controller) restore(client string) {
	cfg := c.cfg.Get()
	if cfg.WakeDisplay && anyBlank(cfg) {
		// Windows may have DPMS-slept the output while the panel was blanked; DDC needs the
		// link up before "on" can reach the panel, so wake first and let it settle.
		if err := c.disp.Wake(); err != nil {
			log.Println("wake before restore:", err)
		}
		if d := time.Duration(cfg.WakeSettleSec) * time.Second; d > 0 {
			time.Sleep(d)
		}
	}
	n := c.applyRestore(cfg)
	log.Printf("restored: %d action(s)", n)
	c.mu.Lock()
	c.st.Blanked, c.st.IdleBlank, c.st.Restoring, c.st.LastAction = false, false, false, "restored"
	c.mu.Unlock()
	if d := time.Duration(cfg.StopDelaySec) * time.Second; d > 0 && len(cfg.OnStop) > 0 {
		time.Sleep(d)
	}
	c.hooks.Run("stop", client, cfg.OnStop, c.timeout())
}
