# rpblank

**Dark screens while you Steam Remote Play. Nothing else touched.**

rpblank is a small background tool (Windows, no tray icon, no installer) for a couch-gaming PC. When a
Steam Remote Play client connects, it blanks the physical monitor(s) over DDC/CI so
nobody in the room sees what you're streaming, runs your scripts (audio switching,
HDMI-CEC, whatever), and puts everything back when the session ends.

It is one static binary with a local web UI. No driver, no virtual display, no
`SetDisplayConfig`, no saved display layouts. That last part is the point: tools that
rearrange the display topology to hide a screen can leave Windows with a broken
layout and break other streaming hosts (Sunshine/Vibeshine). rpblank never changes
what Windows thinks is plugged in; it only tells the panel to go dark.

```
Steam logs "Streaming started"  ─►  connect scripts ─► wake ─► DDC off ─► start scripts
Steam logs "Encoding complete"  ─►  grace period ─► DDC on ─► stop scripts
```

## Features

- **Detects sessions from Steam itself.** Tails `Steam\logs\streaming_log.txt`; no Steam
  integration, pairing or client-side plugin needed. Works with any Remote Play client
  (Steam Deck, Steam Link, phones, Fire TV).
- **Blanks over DDC/CI (VCP 0xD6).** Per-display on/off with a selectable off value
  (standby or power-off). Displays without DDC can be blanked by command instead.
- **Script hooks.** `On connect`, `On start`, `On stop`, plus extra blank/restore commands.
  Paths to `.exe`, `.ps1`, `.cmd`/`.bat` with arguments; no console windows flash; output
  goes to `hooks.log`. Each hook has a **Run now** button.
- **Idle blanking.** Replaces Windows' "turn off display after N minutes". Steam creates its
  capture the instant a client connects; if Windows has the display asleep right then, the
  stream is black until you reconnect. With rpblank owning display sleep the desktop never
  actually sleeps, so a cold-start session always captures a live screen. Idle blanking is
  skipped while a game or player holds the display awake (`ES_DISPLAY_REQUIRED`).
- **Safe by default.** Grace period so switching games doesn't flash the monitor; wake +
  settle before any DDC command; retries while the link is down; restores on next start if
  it was killed while blanked; `-restore` escape hatch from a shell.
- **Coexists with Vibeshine / Sunshine / Moonlight.** It never fights over the displays, and
  if a Vibeshine session takes the physical outputs away mid-restore it just retries until
  they're back.
- **Web UI** at `http://127.0.0.1:47996/` — Overview, Displays, Scripts, Timing, Log.
  Loopback only by default.

## Install

1. Grab `rpblank.exe` and `install.ps1` from [Releases](../../releases) (or build, below)
   into a folder of their own, e.g. `C:\tools\rpblank\`. Config, state and logs live there.
2. Run `rpblank.exe` once. It writes `rpblank.json` with defaults and opens the web UI.
   (Unsigned: SmartScreen → More info → Run anyway.)
3. **Displays** tab: click **Off** then **On** on each display to confirm the panel obeys
   DDC, then tick the ones to blank. If "off = 4" (standby) doesn't blank it, try 5; if 5
   won't come back on, use 4.
4. **Scripts** tab: add what you need (examples below). Save.
5. **Timing** tab: set **Blank after idle** to the minutes you had in Windows, then set
   Windows → Settings → System → Power → *Turn off display* to **Never**.
6. Start at logon: `powershell -ExecutionPolicy Bypass -File install.ps1`. This registers
   a hidden scheduled task running as you. `install.ps1 -Uninstall` removes it.

Only one instance runs; launching the exe again just opens the web page.

## Example scripts

Pin Steam's audio before it captures (On connect), using
[SoundVolumeView](https://www.nirsoft.net/utils/sound_volume_view.html):

```
"C:\tools\SoundVolumeView.exe" /SetDefault "Steam Streaming Speakers" all
```

Switch back to the TV after the stream (On stop):

```
"C:\tools\SoundVolumeView.exe" /SetDefault "NVIDIA High Definition Audio\Device\LG TV" all
```

Put a TV in standby over HDMI-CEC with a Pulse-Eight adapter (Extra blank commands):

```
"C:\Program Files (x86)\Pulse-Eight\USB-CEC Adapter\cec-client.exe" -s -d 1 "standby 0"
```

Scripts get `RPBLANK_EVENT` (`connect`/`start`/`stop`/`off`/`on`) and `RPBLANK_CLIENT`
(the client name Steam logged) in their environment. Commands run in order, each with the
configured timeout; a hung one is killed and the next runs.

## Timing settings

| Setting | Default | What it does |
|---|---|---|
| Blank after idle | 0 (off) | Minutes without keyboard/mouse input before blanking. Any input restores. |
| Wake display | on | Jiggle the mouse / send monitor-on before DDC commands so the link is up. |
| Wake settle | 6 s | Wait after waking before talking DDC. Raise if your panel is slow to come back. |
| Grace before restore | 20 s | A new session inside this window is a resume: no restore, no stop scripts. Raise (60–120) if you browse the library between games. |
| Delay before stop scripts | 3 s | Time between displays coming back and On stop running. |
| Script timeout | 60 s | Per command. |
| Clients | (all) | Only react to these client names (as Steam logs them). |

## Command line

```
rpblank.exe              start (opens the UI if run by hand)
rpblank.exe -open=false  start without opening the browser
rpblank.exe -blank       blank configured displays now and exit
rpblank.exe -restore     restore them now and exit
rpblank.exe -list        list displays and DDC support
rpblank.exe -dir PATH    use PATH for config/state/logs (default: next to the exe)
```

`rpblank-console.exe` is the same program built with a console, for seeing `-list` output.

## HTTP API

All JSON, loopback only unless `listen` in `rpblank.json` is changed (there is no auth).

| Method | Path | |
|---|---|---|
| GET | `/api/status` | version, uptime, session state |
| GET / PUT | `/api/config` | the config file |
| GET | `/api/displays` | enumerated displays with DDC status |
| POST | `/api/displays/power` | `{"id": "...", "value": 1-5}` |
| POST | `/api/blank` · `/api/restore` | manual blank / restore |
| POST | `/api/idle-blank` | trigger an idle blank now (for testing) |
| POST | `/api/hooks/run` | `{"event": "connect|start|stop|off|on"}` |
| GET | `/api/log` | last 80 lines of `rpblank.log` |

## Build

Go 1.23+, no cgo, no Windows SDK needed; cross-compiles from anywhere.

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=0.2.1" \
  -o rpblank.exe ./cmd/rpblank

# console variant (drop -H=windowsgui)
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w -X main.version=0.2.1" \
  -o rpblank-console.exe ./cmd/rpblank

go test ./...
```

On non-Windows hosts it runs with a fake display backend so the UI and session logic can
be exercised; the Windows-only parts are the DDC/CI calls (`dxva2.dll`), input/idle
(`user32.dll`) and the power-state query (`powrprof.dll`).

## How it works

- **Detection** — `internal/steamlog` tails Steam's `streaming_log.txt` from the end,
  tolerating rotation, and emits start/stop events for `Streaming started to <client>` and
  `Encoding complete`.
- **Displays** — `internal/display` enumerates monitors with `EnumDisplayMonitors` /
  `GetPhysicalMonitorsFromHMONITOR`, gives each a stable device-interface ID, and sends
  `SetVCPFeature(0xD6, value)`. Wake is a 1‑px `SendInput` mouse move plus
  `SC_MONITORPOWER -1`. Idle is `GetLastInputInfo`; `CallNtPowerInformation
  (SystemExecutionState)` tells it whether something is holding the display awake.
- **Session** — `internal/session` is the state machine: connect hooks → wake → settle →
  blank → start hooks; stop → grace → wake → restore (with retry) → stop hooks; plus the
  idle loop and a `blanked.state` file for crash recovery.
- **Hooks** — `internal/hooks` splits command lines shell-style, picks the interpreter by
  extension, hides windows, and writes output to `hooks.log` so a hook that spawns a
  detached child isn't counted as hung.
- **Server/UI** — `internal/server` is `net/http` with one embedded HTML page.

## Notes and limits

- DDC/CI must be enabled in the monitor's OSD (it usually is). A panel that is "no signal"
  or DPMS-asleep won't answer on the I²C bus; that's why rpblank wakes first and retries.
- Some monitors drop off the DisplayPort bus when put in DDC power-off, which Windows sees
  as an unplug. HDMI generally stays connected. If your layout shuffles when blanking, use
  standby (4) or a CEC/plug route for that screen.
- A session starting while already idle-blanked keeps the panel dark and skips the wake.
- Windows only. Steam on Linux doesn't use the same log format and DDC there is `ddcutil`
  territory; patches welcome.

## License

MIT — see [LICENSE](LICENSE).
