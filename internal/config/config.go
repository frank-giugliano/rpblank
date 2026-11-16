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

// Display is a screen rpblank may switch off during a session.
type Display struct {
	// ID is the monitor's device interface path (Windows) or a fake id on other platforms.
	ID string `json:"id"`
	// Blank marks the display to be switched off while a session runs.
	Blank bool `json:"blank"`
}

// AudioPrevious, as Config.AudioDisconnect, means "whatever was the default before the
// session".
const AudioPrevious = "previous"

// Config is the on-disk layout.
type Config struct {
	// Displays maps a display id to its settings; unknown displays default to left on.
	Displays map[string]Display `json:"displays"`
	// AudioConnect is the playback device id made default when a session is detected
	// ("" = leave audio alone). AudioConnectName is its name, kept for display when the
	// device is not present.
	AudioConnect     string `json:"audioConnect"`
	AudioConnectName string `json:"audioConnectName"`
	// AudioDisconnect is the device made default after a session: "" = leave alone,
	// "previous" = the default from before the session, otherwise a device id.
	AudioDisconnect     string `json:"audioDisconnect"`
	AudioDisconnectName string `json:"audioDisconnectName"`
	// OffCommands / OnCommands are extra command lines run when screens go dark and when
	// they come back (a TV over CEC, a smart plug).
	OffCommands []string `json:"offCommands"`
	OnCommands  []string `json:"onCommands"`
	// OnConnect runs the instant a session is detected, before anything else.
	OnConnect []string `json:"onConnect"`
	// OnStart / OnStop run after blanking and after restoring.
	OnStart []string `json:"onStart"`
	OnStop  []string `json:"onStop"`
	// Clients, when non-empty, limits action to these client names (case-insensitive).
	Clients []string `json:"clients"`
	// GraceSec is how long a session must be gone before restoring (absorbs reconnects
	// and switching games).
	GraceSec int `json:"graceSec"`
	// StopDelaySec waits after restore before OnStop runs.
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
	if c.GraceSec < 0 || c.GraceSec > 3600 {
		return errors.New("graceSec must be 0–3600")
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
	if len(c.AudioConnect) > 512 || len(c.AudioDisconnect) > 512 || len(c.AudioConnectName) > 512 || len(c.AudioDisconnectName) > 512 {
		return errors.New("audio device id too long")
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
