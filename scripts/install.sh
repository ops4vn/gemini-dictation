#!/bin/bash
# Install the built .app locally: copy to /Applications (or run in place),
# create the .app-local config dir, and register the LaunchAgent autostart.
set -euo pipefail
cd "$(dirname "$0")/.."
LABEL="vn.ops4vn.gemini-dictation"
APP="$(pwd)/build/bin/Gemini Dictation.app"

[ -d "$APP" ] || { echo "❌ build first: make build"; exit 1; }

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
        <string>$APP</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><false/>
</dict>
</plist>
AGENT
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/$LABEL.plist" 2>/dev/null \
  || launchctl kickstart -k "gui/$(id -u)/$LABEL"
echo "✅ installed: $APP"
echo "   autostart: $LABEL (RunAtLoad at next login)"
echo "   quit: menu bar 🎙 → Quit — dashboard: http://127.0.0.1:7788"
