#!/bin/bash
# Gemini Dictation — one-shot setup:
#   1) python venv + dependencies
#   2) build "Gemini Dictation.app" with paths bound to this folder
#   3) install LaunchAgent (autostart at login)
# Run from any clone location — no hardcoded paths.
set -euo pipefail
cd "$(dirname "$0")"
LABEL="vn.ops4vn.gemini-dictation"

echo "1/3  venv + dependencies"
[ -d .venv ] || python3 -m venv .venv
.venv/bin/pip install -q -r requirements.txt

[ -f config.json ] || cp config.example.json config.json
chmod 600 config.json

echo "2/3  build 'Gemini Dictation.app'"
APP="Gemini Dictation.app"
mkdir -p "$APP/Contents/MacOS"
BINDIR="$(pwd)"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleName</key><string>Gemini Dictation</string>
    <key>CFBundleDisplayName</key><string>Gemini Dictation</string>
    <key>CFBundleIdentifier</key><string>$LABEL</string>
    <key>CFBundleVersion</key><string>1.0</string>
    <key>CFBundleShortVersionString</key><string>1.0</string>
    <key>CFBundlePackageType</key><string>APPL</string>
    <key>CFBundleExecutable</key><string>gemini-dictation</string>
    <key>LSUIElement</key><true/>
    <key>NSAppSleepDisabled</key><true/>
    <key>NSMicrophoneUsageDescription</key>
    <string>Gemini Dictation records your voice while you hold the Option key, and transcribes it to text with the Gemini API.</string>
    <key>NSAppleEventsUsageDescription</key>
    <string>Gemini Dictation sends the Cmd+V keystroke to paste the transcribed text at your cursor.</string>
</dict>
</plist>
PLIST
echo "APPL????" > "$APP/Contents/PkgInfo"

cat > "$APP/Contents/MacOS/gemini-dictation" <<LAUNCH
#!/bin/bash
LOG="\$HOME/Library/Logs/gemini-dictation.log"
echo "=== \$(date '+%Y-%m-%d %H:%M:%S') launched ===" >> "\$LOG"
exec "$BINDIR/.venv/bin/python" "$BINDIR/dictation.py" >> "\$LOG" 2>&1
LAUNCH
chmod +x "$APP/Contents/MacOS/gemini-dictation"

echo "3/3  LaunchAgent (autostart at login)"
mkdir -p "$HOME/Library/LaunchAgents"
cat > "$HOME/Library/LaunchAgents/$LABEL.plist" <<AGENT
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>$LABEL</string>
    <key>ProgramArguments</key>
    <array>
        <string>/usr/bin/open</string>
        <string>-a</string>
        <string>$BINDIR/$APP</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><false/>
</dict>
</plist>
AGENT
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/$LABEL.plist" 2>/dev/null \
  || launchctl kickstart -k "gui/$(id -u)/$LABEL" 2>/dev/null \
  || open -a "$BINDIR/$APP"

echo ""
echo "✅ Setup complete. Next:"
echo "   1. Get a free API key: https://aistudio.google.com/apikey"
echo "   2. .venv/bin/python dictation.py --set-key YOUR_KEY"
echo "   3. If macOS prompts: allow Microphone + Input Monitoring + Accessibility"
echo "      for 'Gemini Dictation' (System Settings → Privacy & Security), then"
echo "      launchctl kickstart -k \"gui/\$(id -u)/$LABEL\""
echo ""
echo "Usage: hold ⌥ Option ≥1.5s → speak → release → text pasted at cursor."
echo "Dashboard: http://127.0.0.1:7788"
