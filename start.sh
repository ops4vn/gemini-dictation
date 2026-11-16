#!/bin/bash
# Start Gemini Dictation as the APP instance — never run python directly in a
# terminal: TCC permissions would attach to the terminal app (VS Code, Warp…)
# instead of the bundle, forcing you to re-grant for every terminal you use.
cd "$(dirname "$0")"
APP="$(pwd)/Gemini Dictation.app"
if [ ! -d "$APP" ]; then
    echo "Bundle chưa tồn tại — chạy ./setup.sh trước."
    exit 1
fi
open -a "$APP"
