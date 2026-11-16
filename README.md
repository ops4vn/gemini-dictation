# Gemini Dictation

macOS speech-to-text replacement powered by the **Gemini API**. Hold **⌥
Option**, speak (Vietnamese + English mixed, code-switching friendly), release
— the text is appended at your cursor with punctuation and correct Vietnamese
diacritics.

- **Push-to-talk**: hold ⌥ ≥1.5s → a minimal dark pill with a **live waveform**
  appears next to your cursor → release → text pasted ~2s later
- Mic is **only online while the key is held** — accidental Option taps do
  nothing
- Local **quota dashboard** — requests, tokens, daily usage vs free-tier
  limits, 14-day history
- **Config CLI** — interactive or one-liners for API key and model
- Single Python file, runs headless (menu-bar-less), autostarts at login

![states](https://img.shields.io/badge/pill-recording%20%7C%20spinner%20%7C%20done-7aa2ff)

> ⚠️ **Not affiliated with or endorsed by Google.** "Gemini" refers to the
> Google Gemini API that this tool calls with your own key.

## Requirements

- macOS 13+ (Apple Silicon or Intel), Python 3.10+
- A free Gemini API key — [aistudio.google.com/apikey](https://aistudio.google.com/apikey)
- macOS permissions: **Microphone**, **Input Monitoring**, **Accessibility**
  for the app (setup.sh + first launch trigger the prompts)

## Quick start

```bash
git clone https://github.com/ops4vn/gemini-dictation.git
cd gemini-dictation
./setup.sh                                        # venv + .app bundle + autostart
.venv/bin/python dictation.py --set-key YOUR_KEY  # your Gemini API key
./start.sh                                        # launch (see below)
```

`setup.sh` also walks you through the one-time permission grants: it opens
System Settings at **Input Monitoring** and **Accessibility** and asks you to
drag the `Gemini Dictation.app` into each list. Grants belong to the **app
bundle**, never to your terminal — so you do this once, regardless of which
terminal you ran setup from.

Start the app later with `./start.sh` — it launches the **app instance** via
`open -a` and never runs Python inside your terminal (that's what keeps
permissions terminal-independent).

Hold **⌥ Option ≥1.5s → speak → release**. Text is pasted at the cursor
(Cmd+V round-trip, your clipboard is restored afterwards).

Useful commands:

```bash
.venv/bin/python dictation.py --config              # interactive menu (key, model)
.venv/bin/python dictation.py --set-model gemini-3.5-flash-lite   # faster, lighter
.venv/bin/python dictation.py --check               # verify config / API / mic
launchctl kickstart -k "gui/$(id -u)/vn.ops4vn.gemini-dictation"  # restart app
tail -f ~/Library/Logs/gemini-dictation.log         # logs
```

## Interaction model

**Menu bar:** a small **mic icon** lives in the macOS menu bar while the app
runs — click it for *Mở Dashboard* and **Quit** (⌘Q). That's the way to stop
the app; `pkill -f dictation.py` works too.

| Action | Result |
|---|---|
| press & hold **⌥ Option** | nothing happens yet — safe for normal Option use |
| keep holding **≥ 1.5s** | mic goes live + waveform pill appears at cursor + **Pop** |
| release | spinner → ~2s → text pasted at caret + pill fades + **Tink** |
| release **< 1.5s** | nothing at all (mic never opened) |
| hold ≥1.5s without speaking | silently discarded |
| error (rate limit / API) | **Basso** sound; count visible on the dashboard |
| `Ctrl+Shift+Space` | backup toggle: start/stop recording |
| speak > 120s | auto-stops and transcribes |

Start speaking **after the Pop** — audio before the threshold isn't captured.

## Models

| Model | Notes |
|---|---|
| `gemini-3.5-flash` *(default)* | best accuracy of the fast group (~2s with thinking disabled) |
| `gemini-3.5-flash-lite` | fastest/lightest, weaker on noisy real speech |
| `gemini-3.5-transcribe` | dedicated STT, very fast, weaker at code-switching |

Thinking is disabled by default (`disable_thinking`) — that's what keeps the
flash-tier models around ~2s. Switch models via `--config` / `--set-model`.

## Quota dashboard

**http://127.0.0.1:7788** while the app runs:

- today's requests vs RPD estimate, RPM burst, audio seconds, tokens in/out
- errors and 429 counts (a 429 = your real daily quota was hit)
- 14-day history + last 15 requests with latency

The Gemini API exposes no remaining-quota data, so the dashboard computes
"remaining" from `free_tier_limits` in `config.json` — **estimates you can
edit**; your authoritative numbers live in
[AI Studio → usage](https://aistudio.google.com/usage).

## Privacy

- Your audio is sent to **Google's Gemini API** for transcription — there is
  no offline mode. On the **free tier, Google may use submitted data to
  improve its products**; see the [Gemini API terms](https://ai.google.dev/gemini-api/terms).
- Audio is held only in a temp file (`/tmp/gemini_dictation_segment.wav`,
  overwritten each session). No transcripts or audio are stored by this app.
- Usage stats (`usage.json`, `usage_log.jsonl`) stay on your machine and are
  never uploaded anywhere by this project.

## Configuration

`config.json` (created from `config.example.json` on first setup):

| Key | Default | Notes |
|---|---|---|
| `api_key` | — | your Gemini API key |
| `model` | `gemini-3.5-flash` | any text-capable model from `--config` |
| `disable_thinking` | `true` | keeps flash models at ~2s |
| `hold_seconds` | `1.5` | ⌥ hold before mic + pill start |
| `hotkey` | `<ctrl>+<shift>+<space>` | pynput syntax; named keys need `<>` |
| `web_port` | `7788` | dashboard port |
| `free_tier_limits` | estimates | used by the dashboard; edit to match AI Studio |
| `sounds` | `true` | Pop / Tink / Basso |
| `trailing_space` | `true` | space appended after pasted text |
| `restore_clipboard` | `true` | old clipboard restored ~1.5s after paste |

## Troubleshooting

| Symptom | Fix |
|---|---|
| Pill shows but transcripts silent (rms=0) | `sudo killall coreaudiod` (restarts macOS audio daemon) |
| Hold ⌥ does nothing | Input Monitoring missing → re-grant to the app, then restart via launchctl command above |
| Paste doesn't land | Accessibility missing, or target app blocks synthetic Cmd+V |
| "Mic không có dữ liệu" | wrong input device or Microphone permission |
| macOS dictation also triggers | System Settings → Keyboard → Dictation → shortcut Off |

## License

[MIT](LICENSE) — uses pynput (MIT), numpy (BSD-3), pyobjc (MIT). Not
affiliated with Google.
