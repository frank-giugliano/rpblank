// rpblank switches your physical screens off while a Steam Remote Play session runs, moves
// the default audio device, and runs your scripts around it. Screens are taken out of the
// active Windows layout for the session only; the change is never saved.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"rpblank/internal/audio"
	"rpblank/internal/config"
	"rpblank/internal/display"
	"rpblank/internal/hooks"
	"rpblank/internal/server"
	"rpblank/internal/session"
	"rpblank/internal/steamlog"
)

var version = "dev"

func main() {
	dir := flag.String("dir", "", "data directory for config, state and logs (default: next to the exe)")
	blank := flag.Bool("blank", false, "switch the ticked screens off now and exit (needs a virtual display)")
	restore := flag.Bool("restore", false, "restore the screens now and exit")
	list := flag.Bool("list", false, "list displays and audio devices, then exit")
	open := flag.Bool("open", true, "open the web UI in the browser when started by hand (Windows)")
	flag.Parse()

	if *dir == "" {
		exe, err := os.Executable()
		if err != nil {
			fatal(err)
		}
		*dir = filepath.Dir(exe)
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fatal(err)
	}
	cfgPath := filepath.Join(*dir, "rpblank.json")
	logPath := filepath.Join(*dir, "rpblank.log")
	hooksLog := filepath.Join(*dir, "hooks.log")
	statePath := filepath.Join(*dir, "blanked.state")

	store, err := config.Open(cfgPath)
	if err != nil {
		fatal(err)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		_ = store.Set(store.Get()) // write defaults so the file exists for editing
	}
	disp := display.New()
	aud := audio.New()

	if *list {
		infos, err := disp.List()
		if err != nil {
			fatal(err)
		}
		fmt.Println("Displays (active outputs):")
		for _, d := range infos {
			kind := ""
			if d.Virtual {
				kind = "  [virtual]"
			}
			fmt.Printf("  %s%s\n     id: %s\n", d.Name, kind, d.ID)
		}
		devs, err := aud.List()
		if err != nil {
			fatal(err)
		}
		fmt.Println("Playback devices:")
		for _, d := range devs {
			def := ""
			if d.Default {
				def = "  [default]"
			}
			fmt.Printf("  %s (%s)%s\n     id: %s\n", d.Name, d.State, def, d.ID)
		}
		return
	}

	rotate(logPath)
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fatal(err)
	}
	defer lf.Close()
	log.SetOutput(io.MultiWriter(lf, os.Stderr))
	log.SetFlags(log.LstdFlags)

	hk := &hooks.Runner{LogPath: hooksLog}
	sess := session.New(store, disp, aud, hk, statePath)

	if *blank {
		sess.Blank()
		return
	}
	if *restore {
		sess.Restore()
		return
	}

	addr := store.Get().Listen
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Already running: just open its page.
		if *open {
			openURL("http://" + addr + "/")
		}
		fatal(fmt.Errorf("listen %s: %w (is rpblank already running?)", addr, err))
	}
	log.Printf("rpblank %s starting (listen %s, data %s)", version, addr, *dir)

	stop := make(chan struct{})
	defer close(stop)
	go steamlog.Watch(steamlog.Candidates(), time.Second, stop, func(ev steamlog.Event) {
		if ev.Start {
			log.Printf("Remote Play session started (client %q)", ev.Client)
			sess.Start(ev.Client)
		} else {
			log.Printf("Remote Play session ended")
			sess.Stop()
		}
	})

	h := server.New(store, disp, aud, sess, hk, logPath, version)
	if *open && runtime.GOOS == "windows" && !startedByTask() {
		openURL("http://" + addr + "/")
	}
	if err := http.Serve(ln, h); err != nil {
		fatal(err)
	}
}

// rotate keeps the log from growing forever: over 2 MB it is moved aside once.
func rotate(path string) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > 2<<20 {
		_ = os.Rename(path, path+".old")
	}
}

// startedByTask is true when launched without an interactive parent (Task Scheduler),
// so the browser is not opened at logon.
func startedByTask() bool {
	return os.Getenv("RPBLANK_NO_OPEN") != ""
}

func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func fatal(err error) {
	log.Println(err)
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
