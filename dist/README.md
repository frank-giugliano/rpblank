# rpblank 0.2.1

Blanks your physical screens over DDC/CI while a Steam Remote Play session runs, then
restores them and runs your scripts. It never calls SetDisplayConfig, never saves display
layouts, and never creates a virtual display — so it cannot collide with Vibeshine.

## Install

1. Make a folder, e.g. `C:\tools\rpblank\`, and put `rpblank.exe` and `install.ps1` in it.
   Config, state and logs live in that folder.
2. Double-click `rpblank.exe` once. It starts, writes `rpblank.json` with defaults, and opens
   the web UI at <http://127.0.0.1:47996/>. (SmartScreen: More info → Run anyway; unsigned.)
3. In the UI → **Displays**: your monitor and TV should be listed. For each one that says
   "DDC ok", click **Off** then **On** to confirm the panel obeys. Tick the ones to blank.
   If a display says "no DDC/CI reply", blank it with a command instead (see below).
4. **Scripts → On stop**: `C:\scripts\delayed_audio_switch.exe`. Save.
5. When happy, make it start at logon: right-click `install.ps1` → Run with PowerShell
   (or `powershell -ExecutionPolicy Bypass -File install.ps1`). It registers a hidden
   logon task running as you and starts it. `install.ps1 -Uninstall` removes the task.

Only one instance runs; launching the exe again just opens the web page.

## What happens

- Session start (Steam logs `Streaming started to <client>`): **connect** scripts, wake the
  display (mouse jiggle + monitor-on, optional), settle, DDC-off each ticked display, run the
  **off** commands, run **start** scripts.
- Session end (`Encoding complete`): wait the grace period (a reconnect cancels it), DDC-on
  each display, run the **on** commands, wait the stop delay, run **stop** scripts.
- If rpblank is killed while displays are blanked, it restores them on next start.
  `rpblank.exe -restore` from a shell also restores immediately; `-blank` blanks; `-list`
  prints displays and DDC support (use `rpblank-console.exe` for those so output is visible).

## Idle blanking (recommended) — fixes Steam's black screen on a sleeping PC

Steam Remote Play creates its capture the instant a client connects. If Windows has the
display asleep at that moment, the stream is black until you reconnect. rpblank can't wake
the display fast enough to beat that, so instead let rpblank own display sleep:

1. Windows → Settings → System → Power → **Turn off display: Never**.
2. rpblank → Timing → **Blank after idle** = the minutes you used in Windows (e.g. 10).

After that long without input, rpblank DDC-blanks the ticked displays (and runs the
off-commands); any keyboard/mouse input restores them. The desktop itself never sleeps, so a
Remote Play session always starts against a live capture. A session starting while
idle-blanked simply keeps the panel dark.

## Scripts

- **On connect** runs the instant a session is detected, before anything else. Put the audio
  pin here so Steam captures a stable device:
  `"C:\scripts\SoundVolumeView.exe" /SetDefault "Steam Streaming Speakers" all`
- **On start** runs once displays are blanked; **On stop** after they're restored (+ delay).

## TV over HDMI-CEC (Pulse-Eight)

Put your CEC commands in **Extra blank / restore commands**, e.g.
```
"C:\Program Files (x86)\Pulse-Eight\USB-CEC Adapter\cec-client.exe" -s -d 1 "standby 0"
```
and the matching `"on 0"` for restore. Anything that works from a command prompt works here.

## Notes

- DDC "off = 4" is standby; some panels need "off = 5". Pick per display in the dropdown.
- A monitor on DisplayPort can drop off the bus when powered down over DDC, which Windows
  treats as unplugged. Over HDMI it stays "connected". If your layout shuffles when blanking,
  that's why — use the CEC/plug route for that screen instead.
- The web UI binds to 127.0.0.1 only. To reach it over your VPN, set `"listen":
  "0.0.0.0:47996"` in `rpblank.json` and restart (there is no auth; private networks only).
- Logs: `rpblank.log` (host) and `hooks.log` (your scripts' output) in the data folder.
- Env for scripts: `RPBLANK_EVENT` (start/stop/off/on), `RPBLANK_CLIENT` (Steam's client name).

sha256 rpblank.exe: cc0311a8ad7e6c084c4ccded1b25530207bde8ed370baee3467562f861257b53
sha256 rpblank-console.exe: 298dc22fa7e3769ad274825f6bd5c69b9a179569d1e6f860d3929354e5e543da
