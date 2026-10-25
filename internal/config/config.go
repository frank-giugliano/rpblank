// Package config holds rpblank's settings, persisted as JSON next to the executable.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"rpblank/internal/hooks"
)

// Display is a physical monitor rpblank may blank over DDC/CI.
type Display struct {
	// ID is the stable device instance path (Windows) or a fake id on other platforms.
	ID string `json:"id"`
	// Blank marks the display for blanking during a session.
	Blank bool `json:"blank"`
	// OffValue is the VCP D6 value that powers the panel down (4 = off, 5 = hard off on some).
	OffValue int `json:"offValue"`
}

// Config is the on-disk layout.
type Config struct {
	// Displays maps a display id to its settings; unknown displays default to not blanked.
	Displays map[string]Display `json:"displays"`
	// OffCommands / OnCommands are extra command lines run at blank and restore time, for
	// screens DDC cannot reach (a TV over CEC, a smart plug).
	OffCommands []string `json:"offCommands"`
	OnCommands  []string `json:"onCommands"`
	// OnConnect runs the instant a session is detected, before the wake and blank — the
	// place to pin the default audio device before Steam opens its capture.
	OnConnect []string `json:"onConnect"`
	// OnStart / OnStop run after blanking and after restoring.
	OnStart []string `json:"onStart"`
	OnStop  []string `json:"onStop"`
	// Clients, when non-empty, limits action to these client names (case-insensitive).
	Clients []string `json:"clients"`
	// WakeDisplay sends a synthetic key press at session start so a sleeping monitor wakes
	// before Steam creates its capture.
	WakeDisplay bool `json:"wakeDisplay"`
	// WakeSettleSec waits after the wake before any DDC command: a panel told to go to
	// standby while still powering up can stop listening to DDC entirely.
	WakeSettleSec int `json:"wakeSettleSec"`
	// IdleBlankMin blanks the displays after this many minutes without input, outside of
	// sessions, so Windows' own display sleep can be set to Never. 0 disables it. With
	// Windows never sleeping the display, Steam's capture always comes up live.
	IdleBlankMin int `json:"idleBlankMin"`
	// GraceSec is how long a session must be gone before restoring (absorbs reconnects).
	GraceSec int `json:"graceSec"`
	// StopDelaySec waits after restore before OnStop runs (HDMI audio endpoints re-enumerate).
	StopDelaySec int `json:"stopDelaySec"`
	// TimeoutSec bounds each command.
	TimeoutSec int `json:"timeoutSec"`
	// Listen is the web UI address.
	Listen string `json:"listen"`
}

func Default() Config {
	return Config{
		Displays:     map[string]Display{},
		OffCommands:  []string{},
		OnCommands:   []string{},
		OnStart:      []string{},
		OnStop:       []string{},
		Clients:      []string{},
		WakeDisplay:  true,
		GraceSec:     20,
		StopDelaySec: 3,
		TimeoutSec:   60,
		Listen:       "127.0.0.1:47996",
	}
}

func (c Config) Validate() error {
	for name, lines := range map[string][]string{"offCommands": c.OffCommands, "onCommands": c.OnCommands, "onConnect": c.OnConnect, "onStart": c.OnStart, "onStop": c.OnStop} {
		if err := hooks.Validate(lines); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if c.IdleBlankMin < 0 || c.IdleBlankMin > 1440 {
		return errors.New("idleBlankMin must be 0–1440")
	}
	if c.GraceSec < 0 || c.GraceSec > 3600 {
		return errors.New("graceSec must be 0–3600")
	}
	if c.WakeSettleSec < 0 || c.WakeSettleSec > 60 {
		return errors.New("wakeSettleSec must be 0–60")
	}
	if c.StopDelaySec < 0 || c.StopDelaySec > 600 {
		return errors.New("stopDelaySec must be 0–600")
	}
	if c.TimeoutSec < 0 || c.TimeoutSec > hooks.MaxTimeoutSec {
		return errors.New("timeoutSec must be 0–3600")
	}
	if len(c.Clients) > 64 || len(c.Displays) > 64 {
		return errors.New("too many entries")
	}
	for id, d := range c.Displays {
		if d.OffValue != 0 && (d.OffValue < 2 || d.OffValue > 5) {
			return fmt.Errorf("display %s: offValue must be 2–5", id)
		}
	}
	return nil
}

// Store persists a Config.
type Store struct {
	mu   sync.Mutex
	path string
	cur  Config
}

func Open(path string) (*Store, error) {
	s := &Store{path: path, cur: Default()}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	c := Default()
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	normalize(&c)
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", path, err)
	}
	s.cur = c
	return s, nil
}

func normalize(c *Config) {
	if c.Displays == nil {
		c.Displays = map[string]Display{}
	}
	for _, p := range []*[]string{&c.OffCommands, &c.OnCommands, &c.OnConnect, &c.OnStart, &c.OnStop, &c.Clients} {
		if *p == nil {
			*p = []string{}
		}
	}
	if c.Listen == "" {
		c.Listen = Default().Listen
	}
}

func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Set validates and writes a whole config; memory changes only on success.
func (s *Store) Set(c Config) error {
	normalize(&c)
	if err := c.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.cur = c
	return nil
}

// Path returns the config file location.
func (s *Store) Path() string { return s.path }
