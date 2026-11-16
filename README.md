# Gemini Dictation (Go / Wails)

macOS push-to-talk dictation powered by the **Gemini API**, written in **Go
with [Wails](https://wails.io)**. Hold **⌥ Option ≥1.5s**, speak (Vietnamese +
English mixed, code-switching friendly), release — the text is appended at
your cursor with punctuation and correct Vietnamese diacritics.

- **Live waveform pill** floats next to your cursor while recording
  (mic is only online during the hold — accidental Option taps do nothing)
- **Quota dashboard** at `http://127.0.0.1:7788` — requests, tokens, daily
  usage vs free-tier limits, 14-day history, error/429 counts
- **Config CLI** — interactive or one-liners for API key + model
- **Signed builds + auto-updater** (Cloudflare R2 manifest based)
- Quit via the dashboard button / Dock icon

> ⚠️ **Not affiliated with or endorsed by Google.** "Gemini" refers to the
> Google Gemini API that this tool calls with your own key.

## Requirements

- macOS 13+ · Go 1.23+ · [Wails CLI](https://wails.io/docs/gettingstarted/installation)
  (`go install github.com/wailsapp/wails/v2/cmd/wails@latest`)
- A free Gemini API key — [aistudio.google.com/apikey](https://aistudio.google.com/apikey)
- macOS permissions (first launch prompts): **Microphone**, **Input
  Monitoring**, **Accessibility** — granted to the app bundle, once

## Build & install

```bash
git clone https://github.com/ops4vn/gemini-dictation.git
cd gemini-dictation
make build                       # adhoc-signed build/bin/Gemini Dictation.app
./scripts/install.sh             # LaunchAgent autostart
.venv nothing here — pure Go.
gemini-dictation --set-key YOUR_KEY   # or edit ~/.gemini-dictation/config.json
```

Start/quit: the app runs in the background (pill + dashboard). Quit via the
dashboard **Quit** button or the Dock icon. `--check` verifies config/API/mic:

```bash
"Gemini Dictation.app/Contents/MacOS/Gemini Dictation" --check
```

## Usage

| Action | Result |
|---|---|
| hold **⌥ ≥ 1.5s** | mic goes live + waveform pill at cursor + **Pop** |
| release | spinner → ~2s → text pasted at caret + **Tink** |
| release **< 1.5s** | nothing (mic never opened) |
| hold without speaking | silently discarded |
| speak > 120s | auto-stops and transcribes |

Start speaking **after the Pop** — audio before the threshold isn't captured.
The transcript is pasted via a clipboard round-trip (your clipboard is
restored ~1.5s later).

## Configuration

`~/.gemini-dictation/config.json` (created on first run; `--set-key`,
`--set-model`, `--config` edit it for you):

| Key | Default | Notes |
|---|---|---|
| `model` | `gemini-3.5-flash` | best accuracy of the fast group |
| `disable_thinking` | `true` | keeps flash models at ~2s (17–45s without!) |
| `hold_seconds` | `1.5` | ⌥ hold before mic + pill |
| `web_port` | `7788` | dashboard port |
| `update.url` | *(empty)* | enable updater (see below) |

## Releases, signing, updates

```bash
make build           # adhoc-signed .app — fine for your own machine
make sign            # re-sign with Developer ID Application cert
make notarize        # submit to Apple notary (needs APPLE_ID/AC_* env)
make release UPDATE_BASE_URL=https://dl.example.com/gemini-dictation
make publish R2_REMOTE=r2:my-bucket    # uploads zip + latest.json to Cloudflare R2
```

Enable the updater in `~/.gemini-dictation/config.json`:

```json
"update": { "url": "https://dl.example.com/gemini-dictation", "auto_apply": true }
```

The app fetches `latest.json` (`{version, sha256, url}`), verifies the zip
hash, swaps the .app bundle and relaunches. **Note:** distributing to other
Macs without Gatekeeper warnings requires a paid Apple Developer account
(Developer ID + notarization); adhoc builds only run cleanly on the machine
that built them.

## Privacy

- Your audio is sent to **Google's Gemini API** — no offline mode. On the
  **free tier, Google may use submitted data to improve its products**;
  see the [Gemini API terms](https://ai.google.dev/gemini-api/terms).
- Audio lives only in a temp file, overwritten every session. Usage stats
  stay on your machine.
- Your API key lives in `~/.gemini-dictation/config.json` (chmod 600, outside
  the repo).

## License

[MIT](LICENSE) — Wails (MIT), pynput-era prototype retired; current deps are
Go standard library + Wails + pyobjc-free. Not affiliated with Google.

## Legacy

The original Python prototype lives in the repo history under the
`python-prototype` tag.
