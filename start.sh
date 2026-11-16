#!/bin/bash
# Gemini Dictation — foreground run (shows logs, Ctrl+C to quit)
cd "$(dirname "$0")"
exec .venv/bin/python dictation.py
