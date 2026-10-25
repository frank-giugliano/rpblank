package hooks

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunInOrderWithEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	out := filepath.Join(t.TempDir(), "out.txt")
	r := &Runner{}
	n := r.Run("stop", "deck", []string{
		`/bin/sh -c "echo one $RPBLANK_EVENT $RPBLANK_CLIENT >> ` + out + `"`,
		"",
		`/bin/sh -c "echo two >> ` + out + `"`,
	}, 0)
	if n != 2 {
		t.Fatalf("ran %d, want 2", n)
	}
	data, _ := os.ReadFile(out)
	if got := strings.TrimSpace(string(data)); got != "one stop deck\ntwo" {
		t.Fatalf("got %q", got)
	}
}

func TestRunTimesOutAndIgnoresOrphans(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	logPath := filepath.Join(t.TempDir(), "hooks.log")
	r := &Runner{LogPath: logPath}
	start := time.Now()
	r.Run("stop", "", []string{`/bin/sh -c "sleep 5"`}, time.Second)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("timeout did not apply: %s", d)
	}
	start = time.Now()
	r.Run("stop", "", []string{`/bin/sh -c "echo parent; sleep 4 & exit 0"`}, 10*time.Second)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waited %s for an orphaned child", d)
	}
	data, _ := os.ReadFile(logPath)
	if !strings.Contains(string(data), "parent") {
		t.Fatalf("output not logged: %q", data)
	}
}

func TestRunOne(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	if err := RunOne(`/bin/sh -c "exit 3"`, time.Second, ""); err == nil {
		t.Fatal("expected failure")
	}
	if err := RunOne(`/bin/true`, time.Second, ""); err != nil {
		t.Fatal(err)
	}
	if err := RunOne(`""`, time.Second, ""); err == nil {
		t.Fatal("empty should fail")
	}
}

func TestSplitAndValidate(t *testing.T) {
	cases := map[string][]string{
		``:                              nil,
		`C:\a.exe`:                      {`C:\a.exe`},
		`"C:\P F\x.exe" /S "LG TV" all`: {`C:\P F\x.exe`, "/S", "LG TV", "all"},
		"a\tb\r\n":                      {"a", "b"},
	}
	for in, want := range cases {
		got := Split(in)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%q: got %q want %q", in, got, want)
		}
	}
	if err := Validate([]string{"a", "", `"" `}); err == nil {
		t.Fatal("empty quoted command should fail")
	}
	if err := Validate([]string{"a b c", " "}); err != nil {
		t.Fatal(err)
	}
	if got := scriptArgv([]string{`C:\h\a.PS1`, "x"}); got[0] != "powershell.exe" || got[len(got)-1] != "x" {
		t.Fatalf("ps1: %v", got)
	}
	if got := scriptArgv([]string{`C:\h\a.cmd`}); got[0] != "cmd.exe" {
		t.Fatalf("cmd: %v", got)
	}
}
