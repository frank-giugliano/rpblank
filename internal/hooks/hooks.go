// Package hooks runs user command lines around a Remote Play session.
//
// A command line is split like a shell would (double quotes group words). A .ps1 runs through
// PowerShell, .cmd/.bat through cmd, anything else is executed directly. Every command gets
// RPBLANK_EVENT and RPBLANK_CLIENT in its environment. Commands run one after another, each
// with a timeout; output goes to a log file rather than a pipe so a command that exits but
// leaves a background child behind still counts as finished.
package hooks

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout = 60 * time.Second
	maxCommands    = 32
	MaxTimeoutSec  = 3600
)

// Validate checks that every non-blank command line parses to a program.
func Validate(lines []string) error {
	if len(lines) > maxCommands {
		return errors.New("too many commands")
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if argv := Split(line); len(argv) == 0 || argv[0] == "" {
			return errors.New("empty command: " + line)
		}
	}
	return nil
}

// Runner executes command lists. Zero value is usable.
type Runner struct {
	// LogPath receives each command's stdout and stderr (appended); empty discards them.
	LogPath string
	mu      sync.Mutex // serialises runs so start and stop commands never overlap
}

// Run executes lines in order with event/client in the environment and returns how many ran.
// It blocks until all finish or time out; callers that must not wait run it in a goroutine.
func (r *Runner) Run(event, client string, lines []string, timeout time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	var cmds [][]string
	for _, line := range lines {
		if argv := Split(line); len(argv) > 0 && argv[0] != "" {
			cmds = append(cmds, scriptArgv(argv))
		}
	}
	if len(cmds) == 0 {
		return 0
	}
	env := append(os.Environ(), "RPBLANK_EVENT="+event, "RPBLANK_CLIENT="+client)
	var out *os.File
	if r.LogPath != "" {
		if f, err := os.OpenFile(r.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			out = f
			defer f.Close()
		} else {
			log.Printf("hook %s: cannot open %s: %v", event, r.LogPath, err)
		}
	}
	for _, argv := range cmds {
		run(argv, env, timeout, event, out)
	}
	return len(cmds)
}

// RunOne executes a single command line (used for display actions) and returns its error.
func RunOne(line string, timeout time.Duration, logPath string) error {
	argv := Split(line)
	if len(argv) == 0 || argv[0] == "" {
		return errors.New("empty command")
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	var out *os.File
	if logPath != "" {
		if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			out = f
			defer f.Close()
		}
	}
	return run(scriptArgv(argv), os.Environ(), timeout, "action", out)
}

// Split breaks a command line into words. Double quotes group words and are removed;
// there is no escaping, so a path with spaces is simply quoted.
func Split(line string) []string {
	var out []string
	var cur strings.Builder
	inQuote, have := false, false
	for _, c := range line {
		switch {
		case c == '"':
			inQuote = !inQuote
			have = true
		case !inQuote && (c == ' ' || c == '\t' || c == '\r' || c == '\n'):
			if have {
				out = append(out, cur.String())
				cur.Reset()
				have = false
			}
		default:
			cur.WriteRune(c)
			have = true
		}
	}
	if have {
		out = append(out, cur.String())
	}
	return out
}

// scriptArgv wraps argv in the interpreter its program needs, by extension.
func scriptArgv(argv []string) []string {
	switch strings.ToLower(filepath.Ext(argv[0])) {
	case ".ps1":
		return append([]string{"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File"}, argv...)
	case ".cmd", ".bat":
		return append([]string{"cmd.exe", "/c"}, argv...)
	case ".sh":
		if runtime.GOOS != "windows" {
			return append([]string{"/bin/sh"}, argv...)
		}
	}
	return argv
}

func run(argv []string, env []string, timeout time.Duration, event string, out *os.File) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = env
	if out != nil {
		fmt.Fprintf(out, "[%s] %s: %s\n", time.Now().Format("2006-01-02 15:04:05"), event, strings.Join(argv, " "))
		cmd.Stdout, cmd.Stderr = out, out
	}
	hideWindow(cmd)
	started := time.Now()
	err := cmd.Run()
	took := time.Since(started).Round(time.Millisecond)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		log.Printf("hook %s: %s timed out after %s", event, argv[0], timeout)
		return fmt.Errorf("%s timed out after %s", argv[0], timeout)
	case err != nil:
		log.Printf("hook %s: %s failed after %s: %v", event, strings.Join(argv, " "), took, err)
		return err
	default:
		log.Printf("hook %s: ran %s (%s)", event, strings.Join(argv, " "), took)
		return nil
	}
}
