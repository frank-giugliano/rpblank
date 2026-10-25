// Package server is the local web UI and JSON API.
package server

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"rpblank/internal/config"
	"rpblank/internal/display"
	"rpblank/internal/hooks"
	"rpblank/internal/session"
)

//go:embed web/index.html
var webFS embed.FS

type Server struct {
	cfg     *config.Store
	disp    display.Backend
	sess    *session.Controller
	hooks   *hooks.Runner
	logPath string
	version string
	logFile string
	started time.Time
}

func New(cfg *config.Store, disp display.Backend, sess *session.Controller, hk *hooks.Runner, logFile, version string) http.Handler {
	s := &Server{cfg: cfg, disp: disp, sess: sess, hooks: hk, logFile: logFile, version: version, started: time.Now()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/config", s.getConfig)
	mux.HandleFunc("PUT /api/config", s.putConfig)
	mux.HandleFunc("GET /api/displays", s.displays)
	mux.HandleFunc("POST /api/displays/power", s.power)
	mux.HandleFunc("POST /api/blank", func(w http.ResponseWriter, _ *http.Request) {
		go s.sess.Blank()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/restore", func(w http.ResponseWriter, _ *http.Request) {
		go s.sess.Restore()
		writeJSON(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /api/idle-blank", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]bool{"ok": s.sess.IdleBlankNow()})
	})
	mux.HandleFunc("POST /api/hooks/run", s.runHooks)
	mux.HandleFunc("GET /api/log", s.log)
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return localOnly(mux)
}

// localOnly refuses requests that did not arrive on a loopback address, unless the
// listener itself was bound elsewhere on purpose (then the operator chose exposure).
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil {
			if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() && !ip.IsPrivate() {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	st := s.sess.Status()
	writeJSON(w, 200, map[string]any{
		"version": s.version,
		"uptime":  time.Since(s.started).Round(time.Second).String(),
		"session": st,
	})
}

func (s *Server) getConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.cfg.Get())
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var c config.Config
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<17)).Decode(&c); err != nil {
		writeErr(w, 400, err)
		return
	}
	cur := s.cfg.Get()
	c.Listen = cur.Listen // bind address only changes via the file + restart
	if err := s.cfg.Set(c); err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 200, s.cfg.Get())
}

func (s *Server) displays(w http.ResponseWriter, _ *http.Request) {
	list, err := s.disp.List()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	if list == nil {
		list = []display.Info{}
	}
	writeJSON(w, 200, list)
}

func (s *Server) power(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string `json:"id"`
		Value uint32 `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if body.Value < 1 || body.Value > 5 {
		writeErr(w, 400, errors.New("value must be 1–5"))
		return
	}
	if body.Value == display.PowerOn {
		_ = s.disp.Wake() // the link must be up for DDC to reach the panel
	}
	err := display.SetPowerRetry(s.disp, body.ID, body.Value)
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *Server) runHooks(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Event string `json:"event"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		writeErr(w, 400, err)
		return
	}
	cfg := s.cfg.Get()
	var lines []string
	switch body.Event {
	case "connect":
		lines = cfg.OnConnect
	case "start":
		lines = cfg.OnStart
	case "stop":
		lines = cfg.OnStop
	case "off":
		lines = cfg.OffCommands
	case "on":
		lines = cfg.OnCommands
	default:
		writeErr(w, 400, errors.New("event must be connect, start, stop, off or on"))
		return
	}
	n := s.hooks.Run(body.Event, s.sess.Status().Client, lines, time.Duration(cfg.TimeoutSec)*time.Second)
	writeJSON(w, 200, map[string]int{"ran": n})
}

// log returns the tail of the host log.
func (s *Server) log(w http.ResponseWriter, _ *http.Request) {
	data, err := os.ReadFile(s.logFile)
	if err != nil {
		writeJSON(w, 200, map[string]string{"text": ""})
		return
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 80 {
		lines = lines[len(lines)-80:]
	}
	writeJSON(w, 200, map[string]string{"text": strings.Join(lines, "\n")})
}
