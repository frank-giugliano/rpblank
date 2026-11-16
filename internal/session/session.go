// Package session turns Remote Play start/stop events into actions.
//
// Start: switch audio → connect hooks → switch the ticked outputs off once Steam's virtual
//
//	display exists → darken commands → start hooks.
//
// Stop:  wait the grace period (a start during it cancels) → restore outputs → restore
//
//	commands → switch audio back → wait the stop delay → stop hooks.
//
// A small state file records that outputs are off so a crash or restart can restore them.
package session

import (
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"rpblank/internal/audio"
	"rpblank/internal/config"
	"rpblank/internal/display"
	"rpblank/internal/hooks"
)

// Status is what the UI shows.
type Status struct {
	Active     bool      `json:"active"`
	Client     string    `json:"client"`
	Blanked    bool      `json:"blanked"`
	Restoring  bool      `json:"restoring"` // in the grace period
	LastStart  time.Time `json:"lastStart"`
	LastStop   time.Time `json:"lastStop"`
	LastAction string    `json:"lastAction"`
}

type Controller struct {
	cfg   *config.Store
	disp  display.Backend
	aud   audio.Backend
	hooks *hooks.Runner
	state string // path of the blanked-marker file; state+".audio" holds the previous device

	mu        sync.Mutex
	st        Status
	timer     *time.Timer
	prevAudio string // default playback device from before the session
}

func New(cfg *config.Store, disp display.Backend, aud audio.Backend, hk *hooks.Runner, statePath string) *Controller {
	c := &Controller{cfg: cfg, disp: disp, aud: aud, hooks: hk, state: statePath}
	if b, err := os.ReadFile(statePath + ".audio"); err == nil {
		c.prevAudio = strings.TrimSpace(string(b))
	}
	if _, err := os.Stat(statePath); err == nil {
		log.Println("screens were left off by a previous run; restoring")
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
		log.Printf("session resumed by %q before restore; keeping screens off", client)
	}
	alreadyBlanked := c.st.Blanked
	c.st.Active, c.st.Client, c.st.Restoring, c.st.LastStart = true, client, false, time.Now()
	c.mu.Unlock()

	if !c.allowed(client) {
		log.Printf("session from %q ignored (not in client list)", client)
		return
	}
	// Audio and the connect commands go first and synchronously: the default device has
	// to be in place before Steam opens its capture a second or two after this log line,
	// and before any display change makes a TV's HDMI audio disappear.
	cfg := c.cfg.Get()
	c.audioConnect(cfg)
	if len(cfg.OnConnect) > 0 {
		c.hooks.Run("connect", client, cfg.OnConnect, c.timeout())
	}
	if alreadyBlanked {
		// Windows brings the outputs back by itself when Steam removes its virtual display
		// between sessions, so a resumed session has to take them down again.
		if len(outputIDs(cfg)) > 0 {
			go c.disableOutputs(cfg)
		}
		return
	}
	go c.blank(client)
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
	log.Printf("session ended; restoring in %s unless it resumes", grace)
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

// outputIDs lists the ticked displays.
func outputIDs(cfg config.Config) []string {
	var ids []string
	for id, d := range cfg.Displays {
		if d.Blank {
			ids = append(ids, id)
		}
	}
	return ids
}

func (c *Controller) timeout() time.Duration {
	return time.Duration(c.cfg.Get().TimeoutSec) * time.Second
}

// outputWait is how long a session start waits for Steam's virtual display to appear;
// outputRechecks are the delays after which the outputs are taken down again in case Steam
// re-applied its own layout while it finished setting the virtual display up. audioWait is
// how long a disconnect waits for its target device to reappear.
var (
	outputWait     = 20 * time.Second
	outputPoll     = 500 * time.Millisecond
	outputRechecks = []time.Duration{2 * time.Second, 3 * time.Second}
	audioWait      = 30 * time.Second
	audioPoll      = 500 * time.Millisecond
)

// disableOutputs switches the ticked displays off once another display exists.
func (c *Controller) disableOutputs(cfg config.Config) {
	ids := outputIDs(cfg)
	if len(ids) == 0 {
		return
	}
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = shortID(id)
	}
	label := strings.Join(names, ", ")
	deadline := time.Now().Add(outputWait)
	for {
		err := c.disp.DisableOutputs(ids)
		if err == nil {
			break
		}
		if !errors.Is(err, display.ErrNoOtherDisplay) {
			log.Printf("switch off %s: %v", label, err)
			return
		}
		if time.Now().After(deadline) {
			log.Printf("switch off %s: no virtual display appeared within %s; leaving it on (is Steam's virtual display enabled under Settings → Remote Play?)", label, outputWait)
			return
		}
		time.Sleep(outputPoll)
	}
	log.Printf("screens off: %s", label)
	for _, d := range outputRechecks {
		time.Sleep(d)
		c.mu.Lock()
		still := c.st.Blanked || c.st.Active
		c.mu.Unlock()
		if !still {
			break
		}
		if err := c.disp.DisableOutputs(ids); err != nil && !errors.Is(err, display.ErrNoOtherDisplay) {
			log.Printf("switch off %s (recheck): %v", label, err)
		}
	}
}

// runLines runs extra command lines and returns how many succeeded.
func (c *Controller) runLines(lines []string) int {
	n := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := hooks.RunOne(line, c.timeout(), c.hooks.LogPath); err == nil {
			n++
		}
	}
	return n
}

// blank is the session path: outputs off (in the background: waiting for Steam's display
// must not hold up the commands), darken commands, start hooks.
func (c *Controller) blank(client string) {
	cfg := c.cfg.Get()
	_ = os.WriteFile(c.state, []byte(time.Now().Format(time.RFC3339)), 0o644)
	c.mu.Lock()
	c.st.Blanked, c.st.LastAction = true, "screens off"
	c.mu.Unlock()
	go c.disableOutputs(cfg)
	c.runLines(cfg.OffCommands)
	log.Printf("session active for %q", client)
	c.hooks.Run("start", client, cfg.OnStart, c.timeout())
}

// restore is the session path back: outputs on, restore commands, audio, stop hooks.
func (c *Controller) restore(client string) {
	cfg := c.cfg.Get()
	if len(outputIDs(cfg)) > 0 {
		if err := c.disp.RestoreOutputs(); err != nil {
			log.Printf("restore screens: %v", err)
		} else {
			log.Println("screens restored")
		}
	}
	c.runLines(cfg.OnCommands)
	_ = os.Remove(c.state)
	c.mu.Lock()
	c.st.Blanked, c.st.Restoring, c.st.LastAction = false, false, "restored"
	c.mu.Unlock()
	c.audioDisconnect(cfg)
	if d := time.Duration(cfg.StopDelaySec) * time.Second; d > 0 && len(cfg.OnStop) > 0 {
		time.Sleep(d)
	}
	c.hooks.Run("stop", client, cfg.OnStop, c.timeout())
}

// audioName is a readable label for a device id in the log.
func (c *Controller) audioName(id string) string {
	if devs, err := c.aud.List(); err == nil {
		for _, d := range devs {
			if strings.EqualFold(d.ID, id) {
				return d.Name
			}
		}
	}
	return id
}

// audioConnect remembers the current default device and switches to the connect device.
func (c *Controller) audioConnect(cfg config.Config) {
	if cfg.AudioConnect == "" && cfg.AudioDisconnect != config.AudioPrevious {
		return
	}
	// Snapshot once per session (a resume keeps the first one). Skip it when the default
	// is already the connect device or Steam's own stream device: Steam may have switched
	// before this ran, and that is not a device anyone wants to go back to.
	c.mu.Lock()
	have := c.prevAudio != ""
	c.mu.Unlock()
	if !have {
		if cur, err := c.aud.Default(); err != nil {
			log.Printf("audio: read default: %v", err)
		} else if cur != "" && !strings.EqualFold(cur, cfg.AudioConnect) && !strings.Contains(c.audioName(cur), "Steam Streaming") {
			c.mu.Lock()
			c.prevAudio = cur
			c.mu.Unlock()
			_ = os.WriteFile(c.state+".audio", []byte(cur), 0o644)
		}
	}
	if cfg.AudioConnect == "" {
		return
	}
	if err := c.aud.SetDefault(cfg.AudioConnect); err != nil {
		log.Printf("audio: switch to %s: %v", c.audioName(cfg.AudioConnect), err)
		return
	}
	log.Printf("audio: default is now %s", c.audioName(cfg.AudioConnect))
}

// audioDisconnect switches the default device back after a session, waiting for the target
// to reappear (a TV's HDMI audio returns a few seconds after its output does).
func (c *Controller) audioDisconnect(cfg config.Config) {
	c.mu.Lock()
	prev := c.prevAudio
	c.prevAudio = ""
	c.mu.Unlock()
	_ = os.Remove(c.state + ".audio")

	target := cfg.AudioDisconnect
	if target == "" {
		return
	}
	previous := target == config.AudioPrevious
	if previous {
		if target = prev; target == "" {
			log.Println("audio: no previous device recorded for this session; leaving the default alone")
			return
		}
	}
	name := c.audioName(target)
	// "Previous" only undoes rpblank's own switch: if the default is no longer the connect
	// device, someone (or Steam) changed it during the session and that choice stands. A
	// named device is always applied.
	if previous && cfg.AudioConnect != "" {
		if cur, err := c.aud.Default(); err == nil && cur != "" && !strings.EqualFold(cur, cfg.AudioConnect) {
			if !strings.EqualFold(cur, target) {
				log.Printf("audio: default changed during the session (now %s); leaving it", c.audioName(cur))
			}
			return
		}
	}
	deadline := time.Now().Add(audioWait)
	waiting := false
	for {
		err := c.aud.SetDefault(target)
		if err == nil {
			log.Printf("audio: default is now %s", name)
			return
		}
		if !errors.Is(err, audio.ErrNotActive) {
			log.Printf("audio: switch to %s: %v", name, err)
			return
		}
		if time.Now().After(deadline) {
			log.Printf("audio: %s did not come back within %s; default left as it is", name, audioWait)
			return
		}
		c.mu.Lock()
		active := c.st.Active
		c.mu.Unlock()
		if active {
			// A new session started; its connect step owns the default now. Hand the
			// remembered device back so that session can still return to it.
			c.mu.Lock()
			if c.prevAudio == "" {
				c.prevAudio = prev
			}
			c.mu.Unlock()
			return
		}
		if !waiting {
			waiting = true
			log.Printf("audio: waiting for %s to come back", name)
		}
		time.Sleep(audioPoll)
	}
}
