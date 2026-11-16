# rpblank

**Screens off and sound in the right place while you Steam Remote Play.**

rpblank is a small background tool (Windows, no tray icon, no installer) for a
couch-gaming PC. When a Steam Remote Play client connects, it switches the physical
screens off so nobody in the room sees what you're streaming, moves the default audio
device to the stream, and runs your scripts. When the session ends it puts everything
back.

It is one static binary with a local web UI and no driver of its own.

```
Steam logs "Streaming started"  ─►  audio → stream device ─► connect scripts
                                    ─► screens off (once Steam's virtual display is up) ─► start scripts
Steam logs "Encoding complete"  ─►  grace period ─► screens restored ─► audio → speakers/TV ─► stop scripts
```

## Requirements

- Windows 10/11 and Steam, with any Remote Play client on the other end.
- **Steam's virtual display** (the SudoVDA driver; Steam → Settings → Remote Play). The
  desktop has to live somewhere while your screens are off, and Windows will not switch
  off its only display. This is also what makes the stream render at the client's
  resolution instead of your monitor's.

## Features

- **Detects sessions from Steam itself.** Tails `Steam\logs\streaming_log.txt`; no Steam
  integration, pairing or client-side plugin. Works with any Remote Play client (Steam
  Deck, Steam Link, phones, Fire TV).
- **Switches screens off for the session.** Ticked displays are taken out of the active
  Windows layout once Steam's virtual display exists, so they show "no signal", the same
  way Sunshine/Vibeshine with a virtual display behaves. Works on monitors and TVs alike.
  The change is applied without being saved to Windows' display database, so a reboot or
  `rpblank.exe -restore` always returns to your own layout.
- **Audio switching built in.** Pick the playback device for "session connects" (normally
  Steam Streaming Speakers) and for "session ends" (a specific device, or whatever was
  default before). After a session rpblank waits for the device to come back, since a
  TV's HDMI audio reappears a few seconds after its picture, and then switches all three
  Windows roles. Devices are stored by endpoint id, so renames and driver updates don't
  break it.
- **Script hooks.** `On connect`, `On start`, `On stop`, plus extra darken/restore
  commands (HDMI-CEC, a smart plug). Paths to `.exe`, `.ps1`, `.cmd`/`.bat` with
  arguments; no console windows flash; output goes to `hooks.log`. Each has **Run now**.
- **Grace period.** Closing one game and starting another, or a Wi-Fi reconnect, inside
  the grace window is treated as one session: no restore, no stop scripts.
- **Recovers.** If rpblank is killed mid-session it restores screens and audio on its next
  start.
- **Web UI** at `http://127.0.0.1:47996/`: Overview, Displays, Audio, Scripts, Timing,
  Log. Loopback only by default.

## Install

1. Grab `rpblank.exe` from [Releases](../../releases) (or build, below) into a folder of
   its own, e.g. `C:\tools\rpblank\`. Config, state and logs live there.
2. Run `rpblank.exe` once. It writes `rpblank.json` with defaults and opens the web UI.
   (Unsigned: SmartScreen → More info → Run anyway.)
3. **Displays** tab: tick the screens to switch off during sessions.
4. **Audio** tab: set the connect device to *Steam Streaming Speakers* and choose what to
   return to afterwards. Save.
5. Start it at logon (next section).

Only one instance runs; launching the exe again just opens the web page.

### Start at logon, without opening the browser

Run by hand, rpblank opens its web page. At startup you don't want that, so start it with
`-open=false`:

```
C:\tools\rpblank\rpblank.exe -open=false
```

1. Right-click `rpblank.exe` → **Create shortcut**.
2. Right-click the shortcut → **Properties** → in **Target**, add a space and `-open=false`
   after the path, as above. (If the path has spaces, keep the quotes around the path and
   put the argument after the closing quote.)
3. Press `Win+R`, enter `shell:startup`, and move the shortcut into that folder.

The UI is always available at <http://127.0.0.1:47996/>.

Idle screen-off is left to Windows: keep your normal "Turn my screen off after" setting.
With Steam's virtual display on, a session that starts while the screen is asleep still
comes up with picture.

## Timing settings

| Setting | Default | What it does |
|---|---|---|
| Grace before restore | 20 s | A new session inside this window is a resume. Raise it (60–120) if you browse the library between games. |
| Delay before stop scripts | 3 s | Time between the restore and your On stop commands. |
| Script timeout | 60 s | Per command. |
| Clients | (all) | Only react to these client names (as Steam logs them). |

Screens come back when the grace period ends, though Windows usually re-enables them by
itself the moment Steam removes its virtual display, so they may light up earlier.

## Scripts

Scripts get `RPBLANK_EVENT` (`connect`/`start`/`stop`/`off`/`on`) and `RPBLANK_CLIENT`
(the client name Steam logged) in their environment. Commands run in order, each with the
configured timeout; a hung one is killed and the next runs. Example, a TV in standby over
HDMI-CEC with a Pulse-Eight adapter (Screens darken):

```
"C:\Program Files (x86)\Pulse-Eight\USB-CEC Adapter\cec-client.exe" -s -d 1 "standby 0"
```

## Command line

```
rpblank.exe              start (opens the UI if run by hand)
rpblank.exe -open=false  start without opening the browser
rpblank.exe -restore     restore screens and audio now and exit
rpblank.exe -blank       switch the ticked screens off now (needs a virtual display)
rpblank.exe -list        list displays and playback devices
rpblank.exe -dir PATH    use PATH for config/state/logs (default: next to the exe)
```

`rpblank-console.exe` is the same program built with a console, for seeing `-list` output.

## HTTP API

All JSON, loopback only unless `listen` in `rpblank.json` is changed (there is no auth).

| Method | Path | |
|---|---|---|
| GET | `/api/status` | version, uptime, session state |
| GET / PUT | `/api/config` | the config file |
| GET | `/api/displays` | active outputs |
| POST | `/api/displays/power` | `{"id": "...", "on": false}` switch one output off / restore |
| GET | `/api/audio` | playback devices, all states |
| POST | `/api/audio/default` | `{"id": "..."}` set the default playback device |
| POST | `/api/blank` · `/api/restore` | manual screens off / restore |
| POST | `/api/hooks/run` | `{"event": "connect|start|stop|off|on"}` |
| GET | `/api/log` | last 80 lines of `rpblank.log` |

## Build

Go 1.23+, no cgo, no Windows SDK needed; cross-compiles from anywhere.

```sh
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=0.4.0" \
  -o rpblank.exe ./cmd/rpblank

# console variant (drop -H=windowsgui)
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w -X main.version=0.4.0" \
  -o rpblank-console.exe ./cmd/rpblank

go test ./...
```

On non-Windows hosts it runs with fake display and audio backends so the UI and session
logic can be exercised.

## How it works

- **Detection** — `internal/steamlog` tails Steam's `streaming_log.txt` from the end,
  tolerating rotation, and emits start/stop events for `Streaming started to <client>` and
  `Encoding complete`.
- **Displays** — `internal/display` reads the active paths with `QueryDisplayConfig`,
  waits until there is another active display besides the ticked ones, then applies the
  same layout minus those paths with `SetDisplayConfig`, without `SDC_SAVE_TO_DATABASE`.
  It re-checks twice over the next few seconds in case Steam re-applies its own layout.
  Restore re-applies the captured layout, or `SDC_USE_DATABASE_CURRENT` if the virtual
  display is already gone.
- **Audio** — `internal/audio` enumerates render endpoints through the MMDevice API and
  sets the default with `IPolicyConfig::SetDefaultEndpoint` (undocumented, but stable since
  Windows 7 and used by every audio switcher). COM is called directly through vtables, so
  there is no cgo and no extra dependency.
- **Session** — `internal/session` is the state machine, with a `blanked.state` file (and
  the remembered audio device beside it) for crash recovery.
- **Hooks** — `internal/hooks` splits command lines shell-style, picks the interpreter by
  extension, hides windows, and writes output to `hooks.log` so a hook that spawns a
  detached child isn't counted as hung.
- **Server/UI** — `internal/server` is `net/http` with one embedded HTML page.

## Notes and limits

- If no virtual display appears within 20 s of a session starting, rpblank logs that and
  leaves the screens on.
- Don't run it alongside another tool that rewrites the display layout for the same
  session. Sunshine/Vibeshine are fine as long as a Moonlight session and a Steam session
  aren't both active at once.
- Windows only.

## License

MIT — see [LICENSE](LICENSE).
