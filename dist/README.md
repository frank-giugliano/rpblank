# rpblank 0.4.0

Switches your screens off while a Steam Remote Play session runs, moves the default audio
device to the stream and back, and runs your scripts.

## What changed in 0.4.0

- **Audio tab.** Choose the playback device for "session connects" and "session ends" from
  dropdowns. This replaces SoundVolumeView lines and delayed-switch scripts: after a
  session rpblank waits for the device to come back and then switches.
- **One way to darken screens.** Ticked displays are switched off in Windows for the
  session (needs Steam's virtual display). DDC/CI blanking, its off values, wake/settle and
  rpblank's own idle blanking are gone; use Windows' "Turn my screen off after" for idle.

## Upgrading

1. Stop rpblank (Task Manager → end `rpblank.exe`), replace the exe, start it again.
2. Displays tab: your ticked screens carry over.
3. Audio tab: set the connect device to **Steam Streaming Speakers** and pick the device
   to return to. Save, then use **Switch now** on each to confirm it works.
4. Scripts tab: once Audio is confirmed, remove your audio lines from "Session detected"
   and "Stream over" so the two don't fight. Until then they still work.
5. Windows → Settings → System → Power: if you had "Turn my screen off" set to Never for
   rpblank's idle blanking, set it back to your minutes.

`rpblank.exe -restore` or a reboot always brings screens back. Web UI:
<http://127.0.0.1:47996/>. Logs: `rpblank.log` and `hooks.log` next to the exe.

sha256 78618329f2b1280d184398b1c61977d5334b1257e24b422e6c06c1ecc951582e  rpblank.exe
sha256 abda8bc07f191b14e7670267ed997889d2b6257afcc2a5f95810a9953952f6ca  rpblank-console.exe
