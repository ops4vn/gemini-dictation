#!/usr/bin/env python3
"""
Gemini Dictation — local macOS speech-to-text replacement.

Interaction:
    HOLD Option (⌥)            -> pill overlay + live waveform at cursor (mic live)
    ... keep holding >= 2s     -> recording confirmed (Pop)
    RELEASE                    -> spinner -> Gemini -> ✅ transcript in the pill
                                  + text appended at the focused caret (Tink)
    (release < 2s              -> pill fades out, nothing transcribed)

    Ctrl+Shift+Space           -> backup toggle: start/stop recording

UI: NSVisualEffectView HUD pill (native vibrancy), waveform bars driven by real
mic levels, NSProgressIndicator spinner, fade animations. Overlay never takes
focus or clicks. Mic is only online while ⌥ is held (or toggle session runs).

Run as an app:  open "Gemini Dictation.app"
Self-check:     .venv/bin/python dictation.py --check
Test a file:    .venv/bin/python dictation.py --transcribe file.wav
"""
import argparse
import base64
import fcntl
import io
import json
import os
import queue
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
import wave
from pathlib import Path

import objc
from AppKit import NSView
from Foundation import NSObject, NSMakeRect

from pynput import keyboard

SCRIPT_DIR = Path(__file__).resolve().parent
CONFIG_PATH = SCRIPT_DIR / "config.json"
API_BASE = "https://generativelanguage.googleapis.com/v1beta/models"

AFPLAY = "/usr/bin/afplay"
PBCOPY = "/usr/bin/pbcopy"
PBPASTE = "/usr/bin/pbpaste"
OSASCRIPT = "/usr/bin/osascript"

PROMPT = (
    "Transcribe this audio exactly as spoken. The speaker mixes Vietnamese and English "
    "(code-switching) and uses technical/IT terms (e.g. pull request, merge, ArgoCD, UAT, deploy). "
    "Keep English words in English, Vietnamese in Vietnamese with correct diacritics. "
    "Add natural punctuation and capitalization. Output ONLY the transcript text, nothing else."
)

DEFAULTS = {
    "api_key": "",
    "model": "gemini-3.5-flash",
    "hotkey": "<ctrl>+<shift>+<space>",
    "hold_seconds": 1.5,
    "show_on_press": False,
    "disable_thinking": True,
    "sample_rate": 16000,
    "max_seconds": 120,
    "min_seconds": 0.35,
    "silence_rms": 25,
    "sounds": True,
    "notifications": False,
    "probe_mic_on_start": True,
    "trailing_space": True,
    "restore_clipboard": True,
    "show_on_press": False,
    "web_port": 7788,
    "free_tier_limits": {
        # ESTIMATES — Google no longer publishes static free-tier tables;
        # check your real limits in AI Studio (Usage & billing) and edit here.
        "gemini-3.5-flash-lite": {"rpm": 15, "rpd": 1000},
        "gemini-3.5-flash": {"rpm": 10, "rpd": 250}
    },
    "debug_keys": False,
}


def log(msg: str) -> None:
    print(f"[{time.strftime('%H:%M:%S')}] {msg}", flush=True)


def load_config() -> dict:
    cfg = dict(DEFAULTS)
    if CONFIG_PATH.exists():
        cfg.update(json.loads(CONFIG_PATH.read_text()))
    else:
        CONFIG_PATH.write_text(json.dumps(DEFAULTS, indent=2))
    if not cfg["api_key"]:
        sys.exit(f"No api_key in {CONFIG_PATH}")
    return cfg


def play(name: str) -> None:
    """Pop (record confirmed) / Tink (pasted) / Basso (error)."""
    try:
        subprocess.run([AFPLAY, f"/System/Library/Sounds/{name}.aiff"], check=False)
    except OSError:
        pass


# ---------------------------------------------------------------- recording

class Recorder:
    """Lazy mic via AVFoundation (AVAudioRecorder → temp WAV per segment).

    PortAudio/sounddevice is NOT used: after the coreaudiod jams on this
    machine its HAL clients deliver all-zero PCM even though callbacks run,
    while AVFoundation capture works. Mic opens only while a segment is armed
    (⌥ held / toggle on); capture starts immediately at key-down so speech in
    the first `hold_seconds` is not lost. Metering feeds the waveform UI.
    """

    RATE = 16000

    def __init__(self):
        import numpy as np
        self._np = np
        self._path = "/tmp/gemini_dictation_segment.wav"
        self._rec = None
        self._armed = False

    def open_and_arm(self) -> bool:
        """Start recording to the temp file. False = mic failed to open."""
        self.close()
        LPCM = int.from_bytes(b'lpcm', 'big')
        settings = {
            "AVFormatIDKey": LPCM,
            "AVSampleRateKey": float(self.RATE),
            "AVNumberOfChannelsKey": 1,
            "AVLinearPCMBitDepthKey": 16,
            "AVLinearPCMIsFloatKey": False,
            "AVLinearPCMIsBigEndianKey": False,
            "AVLinearPCMIsNonInterleaved": False,
        }
        from AVFoundation import AVAudioRecorder
        from Foundation import NSURL
        url = NSURL.fileURLWithPath_(self._path)
        rec, err = AVAudioRecorder.alloc().initWithURL_settings_error_(url, settings, None)
        if rec is None:
            log(f"MIC ERROR: {err}")
            return False
        rec.setMeteringEnabled_(True)
        if not rec.record():
            log("MIC ERROR: record() refused")
            return False
        self._rec = rec
        self._armed = True
        return True

    def has_data(self) -> bool:
        """Recorded duration is advancing → input really flowing."""
        return bool(self._armed and self._rec is not None
                    and self._rec.currentTime() > 0.15)

    def level_norm(self) -> float:
        """Current mic level normalized 0..1 (for the waveform UI)."""
        if not self._armed or self._rec is None:
            return 0.0
        self._rec.updateMeters()
        db = self._rec.averagePowerForChannel_(0)
        if db is None or db < -60:
            return 0.0
        return min(1.0, (db + 60.0) / 60.0)

    def stop_segment(self) -> tuple[bytes, float, float]:
        """Stop; return (wav_bytes, duration_s, rms) of the segment."""
        self._armed = False
        if self._rec is None:
            return b"", 0.0, 0.0
        self._rec.stop()
        self._rec = None
        try:
            with wave.open(self._path, "rb") as w:
                frames = w.getnframes()
                audio = self._np.frombuffer(w.readframes(frames), dtype=self._np.int16)
        except Exception as e:
            log(f"segment read failed: {e}")
            return b"", 0.0, 0.0
        if frames == 0:
            return b"", 0.0, 0.0
        dur = frames / self.RATE
        rms = float(self._np.sqrt(self._np.mean(audio.astype(self._np.float32)**2)))
        bio = io.BytesIO()
        with wave.open(bio, "wb") as out:
            out.setnchannels(1)
            out.setsampwidth(2)
            out.setframerate(self.RATE)
            out.writeframes(audio.tobytes())
        return bio.getvalue(), dur, rms

    def close(self) -> None:
        self._armed = False
        if self._rec is not None:
            try:
                self._rec.stop()
            except Exception:
                pass
            self._rec = None


# ------------------------------------------------------------- transcription

def transcribe(wav_bytes: bytes, cfg: dict) -> dict:
    """Returns {"text", "tin", "tout"} (token counts from usageMetadata)."""
    b64 = base64.b64encode(wav_bytes).decode()
    body = {
        "contents": [{"parts": [
            {"text": cfg.get("prompt", PROMPT)},
            {"inline_data": {"mime_type": "audio/wav", "data": b64}},
        ]}],
    }
    if cfg.get("disable_thinking", True):
        body["generationConfig"] = {"thinkingConfig": {"thinkingBudget": 0}}
    req = urllib.request.Request(
        f"{API_BASE}/{cfg['model']}:generateContent?key={cfg['api_key']}",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
        method="POST")
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            d = json.loads(r.read())
    except urllib.error.HTTPError as e:
        detail = ""
        try:
            detail = json.loads(e.read()).get("error", {}).get("message", "")[:200]
        except Exception:
            pass
        if e.code == 429:
            raise RuntimeError("Rate limit Gemini free tier — đợi vài giây. " + detail)
        raise RuntimeError(f"Gemini API HTTP {e.code}: {detail}")
    part = d["candidates"][0]["content"]["parts"][0]
    text = part.get("text") or part.get("audioTranscription", {}).get("text") or ""
    um = d.get("usageMetadata", {})
    return {"text": text.strip(),
            "tin": int(um.get("promptTokenCount", 0) or 0),
            "tout": int(um.get("candidatesTokenCount", 0) or 0)}


# ------------------------------------------------------------- usage tracker

class UsageTracker:
    """Local per-day/per-model usage counters feeding the web dashboard.

    The API exposes no remaining-quota headers, so "remaining" is computed
    against `free_tier_limits` estimates in config.json.
    """

    def __init__(self, model_default: str):
        self._lock = threading.Lock()
        self.model_default = model_default
        self.path_json = SCRIPT_DIR / "usage.json"
        self.path_log = SCRIPT_DIR / "usage_log.jsonl"
        self.daily: dict = {}
        self.recent: list = []
        self._load()

    def _load(self) -> None:
        try:
            self.daily = json.loads(self.path_json.read_text()).get("daily", {})
        except Exception:
            self.daily = {}

    def record(self, model: str, ok: bool, error: str | None = None,
               audio_s: float = 0.0, tin: int = 0, tout: int = 0,
               latency: float = 0.0) -> None:
        date = time.strftime("%Y-%m-%d")
        entry = {"date": date, "ts": time.strftime("%H:%M:%S"), "model": model,
                 "ok": ok, "error": error, "audio_s": round(audio_s, 2),
                 "tin": tin, "tout": tout, "latency": round(latency, 2)}
        with self._lock:
            d = self.daily.setdefault(date, {}).setdefault(
                model, {"requests": 0, "errors": 0, "e429": 0,
                        "audio_s": 0.0, "tin": 0, "tout": 0})
            d["requests"] += 1
            if not ok:
                d["errors"] += 1
                if error == "429":
                    d["e429"] += 1
            d["audio_s"] = round(d["audio_s"] + audio_s, 2)
            d["tin"] += tin
            d["tout"] += tout
            self.recent.append(entry)
            self.recent = self.recent[-50:]
            try:
                with open(self.path_log, "a") as f:
                    f.write(json.dumps(entry) + "\n")
                tmp = self.path_json.with_suffix(".tmp")
                tmp.write_text(json.dumps({"daily": self.daily}, indent=2))
                tmp.replace(self.path_json)
            except Exception as e:
                log(f"usage write failed: {e}")

    def snapshot(self) -> dict:
        with self._lock:
            return {"daily": {k: v for k, v in self.daily.items()},
                    "recent": list(self.recent)}


def start_web(tracker: UsageTracker, cfg: dict) -> None:
    """Dashboard at http://127.0.0.1:<web_port> (daemon thread)."""
    from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
    port = int(cfg.get("web_port", 7788))
    index_path = SCRIPT_DIR / "web" / "index.html"

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def _send(self, code: int, body: bytes, ctype: str) -> None:
            self.send_response(code)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            if self.path in ("/", "/index.html"):
                try:
                    body = index_path.read_bytes()
                except Exception:
                    body = b"<h1>web/index.html missing</h1>"
                self._send(200, body, "text/html; charset=utf-8")
            elif self.path == "/api/usage":
                payload = {"today": time.strftime("%Y-%m-%d"),
                           "limits": cfg.get("free_tier_limits", {}),
                           "updated": time.strftime("%H:%M:%S")}
                payload.update(tracker.snapshot())
                self._send(200, json.dumps(payload).encode(), "application/json")
            else:
                self._send(404, b"{}", "application/json")

    try:
        srv = ThreadingHTTPServer(("127.0.0.1", port), Handler)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        log(f"dashboard: http://127.0.0.1:{port}")
    except OSError as e:
        log(f"dashboard: port {port} unavailable — {e}")


# ------------------------------------------------------------------ pasting

def paste_at_cursor(text: str, cfg: dict) -> None:
    """Append text at the focused caret via Cmd+V (clipboard round-trip)."""
    old_clip = b""
    if cfg["restore_clipboard"]:
        old_clip = subprocess.run([PBPASTE], capture_output=True).stdout
    p = subprocess.Popen([PBCOPY], stdin=subprocess.PIPE)
    p.communicate(text.encode("utf-8"))
    time.sleep(0.05)
    subprocess.run(
        [OSASCRIPT, "-e", 'tell application "System Events" to keystroke "v" using command down'],
        check=False, capture_output=True)
    if cfg["restore_clipboard"]:
        def restore():
            time.sleep(1.5)
            p2 = subprocess.Popen([PBCOPY], stdin=subprocess.PIPE)
            p2.communicate(old_clip)
        threading.Thread(target=restore, daemon=True).start()


# ------------------------------------------------------------------- popup

class WaveformView(NSView):
    """9 rounded bars driven by a level history (real mic RMS/dB)."""

    N_BARS = 9
    BAR_W = 4.0
    GAP = 4.0

    def initWithFrame_(self, frame):
        self = objc.super(WaveformView, self).initWithFrame_(frame)
        if self is None:
            return None
        self._levels = [0.14] * self.N_BARS
        return self

    @objc.python_method
    def push_level(self, lvl: float) -> None:
        lvl = max(0.10, min(1.0, lvl))
        self._levels.pop(0)
        self._levels.append(lvl)
        self.setNeedsDisplay_(True)

    def drawRect_(self, rect):
        try:
            from AppKit import NSBezierPath, NSColor
            NSColor.colorWithCalibratedWhite_alpha_(1.0, 0.95).set()
            w = self.frame().size.width
            h = self.frame().size.height
            total = self.N_BARS * self.BAR_W + (self.N_BARS - 1) * self.GAP
            x0 = (w - total) / 2
            for i, lvl in enumerate(self._levels):
                bh = max(4.0, lvl * (h - 12.0))
                r = NSMakeRect(x0 + i * (self.BAR_W + self.GAP), (h - bh) / 2,
                               self.BAR_W, bh)
                NSBezierPath.bezierPathWithRoundedRect_xRadius_yRadius_(r, 2.0, 2.0).fill()
        except Exception as e:   # never let drawing kill the app
            if not getattr(self, "_draw_err_logged", False):
                log(f"waveform draw error: {e}")
                self._draw_err_logged = True


class Popup(NSObject):
    """Cursor-anchored dictation pill (HUD vibrancy, waveform, spinner).

    Main thread runs NSApplication + a 20fps timer that drains a queue of
    events from other threads and drives the waveform/auto-hide:
        ("armed", t0)  mic live, below threshold      (subtle pill)
        ("rec", t0)    hold confirmed                  (timer + Pop already played)
        ("busy", None) transcribing
        ("result", t)  transcript                       (auto fade)
        ("error", m)   error                            (auto fade)
        ("hide", None) early release / force hide
    """

    def initWithQueue_level_(self, ev_q, level_fn):
        self = objc.super(Popup, self).init()
        if self is None:
            return None
        import AppKit as ak
        from Foundation import NSMakeRect
        self.q = ev_q
        self.level_fn = level_fn
        self.state = "idle"
        self.t0 = 0.0
        self.hide_at = 0.0
        self.order_at = 0.0

        app = ak.NSApplication.sharedApplication()
        app.setActivationPolicy_(ak.NSApplicationActivationPolicyAccessory)

        w = ak.NSWindow.alloc().initWithContentRect_styleMask_backing_defer_(
            NSMakeRect(0, 0, 220, 56),
            ak.NSWindowStyleMaskBorderless, ak.NSBackingStoreBuffered, False)
        w.setLevel_(ak.NSStatusWindowLevel)
        w.setOpaque_(False)
        w.setBackgroundColor_(ak.NSColor.clearColor())
        w.setHasShadow_(True)
        w.setReleasedWhenClosed_(False)
        w.setIgnoresMouseEvents_(True)
        w.setCollectionBehavior_(
            ak.NSWindowCollectionBehaviorCanJoinAllSpaces
            | ak.NSWindowCollectionBehaviorFullScreenAuxiliary)
        w.setAlphaValue_(0.0)

        fx = ak.NSView.alloc().initWithFrame_(NSMakeRect(0, 0, 220, 56))
        fx.setWantsLayer_(True)
        # solid near-black — vibrancy/blur leaked the (often white) content behind
        fx.layer().setBackgroundColor_(
            ak.NSColor.colorWithCalibratedRed_green_blue_alpha_(0.09, 0.10, 0.13, 1.0).CGColor())
        fx.layer().setCornerRadius_(18.0)
        fx.layer().setMasksToBounds_(True)

        self.wave = WaveformView.alloc().initWithFrame_(NSMakeRect(14, 11, 84, 34))
        fx.addSubview_(self.wave)

        self.spinner = ak.NSProgressIndicator.alloc().initWithFrame_(NSMakeRect(18, 19, 18, 18))
        self.spinner.setStyle_(ak.NSProgressIndicatorStyleSpinning)
        self.spinner.setDisplayedWhenStopped_(False)
        self.spinner.startAnimation_(None)
        self.spinner.setHidden_(True)
        fx.addSubview_(self.spinner)

        self.label = ak.NSTextField.alloc().initWithFrame_(NSMakeRect(106, 8, 100, 40))
        self.label.setEditable_(False)
        self.label.setBordered_(False)
        self.label.setDrawsBackground_(False)
        self.label.cell().setWraps_(True)
        self.label.cell().setLineBreakMode_(4)  # by word
        self.label.setFont_(ak.NSFont.systemFontOfSize_(13))
        self.label.setTextColor_(ak.NSColor.whiteColor())
        fx.addSubview_(self.label)

        w.setContentView_(fx)
        self.win = w
        self.fx = fx
        self._ak = ak
        self._mouse = None
        return self

    # -- layout ----------------------------------------------------------

    @objc.python_method
    def _place(self, w: float, h: float) -> None:
        from pynput.mouse import Controller as MouseController
        if self._mouse is None:
            self._mouse = MouseController()
        mx, my = self._mouse.position          # global, origin TOP-left
        screens = self._ak.NSScreen.screens()
        primary_h = screens[0].frame().size.height
        ax, ay = mx, primary_h - my            # global, origin BOTTOM-left
        target = None
        for s in screens:
            f = s.frame()
            if f.origin.x <= ax <= f.origin.x + f.size.width and \
               f.origin.y <= ay <= f.origin.y + f.size.height:
                target = s
                break
        if target is None:
            target = self._ak.NSScreen.mainScreen()
        sf = target.frame()
        x = mx + 18
        if x + w > sf.origin.x + sf.size.width:
            x = mx - w - 18
        x = max(sf.origin.x + 4, min(x, sf.origin.x + sf.size.width - w - 4))
        y = ay - h - 14
        if y < sf.origin.y + 4:
            y = ay + 26
        y = max(sf.origin.y + 4, min(y, sf.origin.y + sf.size.height - h - 4))
        self.win.setFrame_display_(NSMakeRect(x, y, w, h), True)
        self.fx.setFrame_(NSMakeRect(0, 0, w, h))
        # per-state inner layout: uniform 48px pill, 14px padding, 84px wave
        if self.state in ("armed", "rec"):
            self.wave.setHidden_(False)
            self.wave.setFrame_(NSMakeRect(14, 10, 84, 28))
            self.spinner.setHidden_(True)
            if self.state == "armed":
                self.label.setHidden_(True)          # waveform only
            else:
                self.label.setHidden_(False)
                self.label.setFrame_(NSMakeRect(104, 13, w - 118, 22))
                self.label.setAlignment_(1)          # right: mm:ss
        elif self.state == "busy":
            self.wave.setHidden_(True)
            self.spinner.setHidden_(False)
            self.spinner.setFrame_(NSMakeRect(18, 15, 18, 18))
            self.label.setHidden_(False)
            self.label.setFrame_(NSMakeRect(46, 13, w - 60, 22))
            self.label.setAlignment_(0)
        else:                                        # idle / fading out
            self.wave.setHidden_(True)
            self.spinner.setHidden_(True)
            self.label.setHidden_(True)

    @objc.python_method
    def _show_state(self, state: str, text: str, width: float, height: float,
                    autohide: float | None) -> None:
        self.state = state
        self.label.setStringValue_(text)
        self._place(width, height)
        if self.win.alphaValue() < 0.05:
            self.win.setAlphaValue_(0.0)
            self._fade_to(1.0, 0.16)
        self.win.orderFrontRegardless()
        self.hide_at = (time.time() + autohide) if autohide else 0.0

    @objc.python_method
    def _fade_to(self, alpha: float, dur: float) -> None:
        ak = self._ak
        ak.NSAnimationContext.beginGrouping()
        ctx = ak.NSAnimationContext.currentContext()
        ctx.setDuration_(dur)
        self.win.animator().setAlphaValue_(alpha)
        ak.NSAnimationContext.endGrouping()

    @objc.python_method
    def _hide(self) -> None:
        self.state = "idle"
        self.hide_at = 0.0
        self._fade_to(0.0, 0.18)
        self.order_at = time.time() + 0.2

    # -- event loop (main thread, ~20fps) ---------------------------------

    @objc.python_method
    def run(self) -> None:
        from Foundation import NSTimer, NSRunLoop
        t = NSTimer.timerWithTimeInterval_target_selector_userInfo_repeats_(
            0.05, self, "drain:", None, True)
        NSRunLoop.currentRunLoop().addTimer_forMode_(t, "kCFRunLoopDefaultMode")
        self._ak.NSApplication.sharedApplication().run()

    def drain_(self, timer) -> None:
        now = time.time()
        try:
            while True:
                ev, payload = self.q.get_nowait()
                if ev == "armed":
                    self._show_state("armed", "", 116, 48, None)
                elif ev == "rec":
                    self.state, self.t0 = "rec", payload
                    self._show_state("rec", self._fmt_time(now - payload), 190, 48, None)
                elif ev == "busy":
                    self._show_state("busy", "Đang chuyển giọng nói…", 240, 48, None)
                elif ev == "hide":
                    self._hide()
        except queue.Empty:
            pass

        if self.state in ("armed", "rec"):
            self.wave.push_level(self.level_fn())
        if self.state == "rec":
            self.label.setStringValue_(self._fmt_time(now - self.t0))
        if self.state == "idle" and self.win.isVisible():
            if self.order_at and now >= self.order_at:
                self.win.orderOut_(None)
                self.order_at = 0.0
        if self.hide_at and now >= self.hide_at and self.state != "idle":
            self._hide()

    @objc.python_method
    def _fmt_time(self, s: float) -> str:
        s = max(0, int(s))
        return f"{s // 60}:{s % 60:02d}"


# --------------------------------------------------------------- controller

def _key_set(*names: str) -> set:
    keys = set()
    for n in names:
        k = getattr(keyboard.Key, n, None)
        if k is not None:
            keys.add(k)
    return keys


class Dictation:
    """Hold-⌥ push-to-talk + ctrl+shift+space toggle, one keyboard listener."""

    def __init__(self, cfg: dict, ui_q: "queue.Queue"):
        self.cfg = cfg
        self.ui_q = ui_q
        self.recorder = Recorder()
        self.mode: str | None = None        # None | "hold" | "toggle"
        self.busy = False
        self.hold_timer: threading.Timer | None = None
        self.t_press = 0.0
        self._max_timer: threading.Timer | None = None
        self._alt_keys = _key_set("alt", "alt_l", "alt_r", "alt_gr")
        self._ctrl_keys = _key_set("ctrl", "ctrl_l", "ctrl_r")
        self._shift_keys = _key_set("shift", "shift_l", "shift_r")
        self._mods: set[str] = set()
        self._space_down = False

    def ui(self, ev: str, payload=None) -> None:
        self.ui_q.put((ev, payload))

    # -- hold-⌥ machinery -------------------------------------------------

    def _hold_fired(self) -> None:
        self.hold_timer = None
        if self.mode is not None or self.busy:
            return
        if self.cfg.get("show_on_press"):
            # mic was opened at key-down; just confirm
            if not self.recorder.has_data():
                self.recorder.close()
                self.ui("hide")
                if self.cfg["sounds"]:
                    play("Basso")
                log("mic armed but no data — mic dead?")
                return
        else:
            # mic + pill start only now (> hold_seconds of holding)
            if not self.recorder.open_and_arm():
                self.ui("hide")
                if self.cfg["sounds"]:
                    play("Basso")
                log("mic failed to open")
                return
            self.t_press = time.time()
        self.mode = "hold"
        self.ui("rec", self.t_press)
        if self.cfg["sounds"]:
            threading.Thread(target=play, args=("Pop",), daemon=True).start()
        log(f"RECORDING (hold) — limit {self.cfg['max_seconds']}s")
        self._arm_max_timer()
        if not self.cfg.get("show_on_press"):
            # watchdog: verify the freshly-opened mic actually delivers
            def check():
                time.sleep(0.8)
                if self.mode == "hold" and not self.recorder.has_data():
                    self.mode = None
                    if self._max_timer:
                        self._max_timer.cancel()
                    self.recorder.close()
                    self.ui("hide")
                    if self.cfg["sounds"]:
                        play("Basso")
                    log("mic opened but no data — mic dead?")
            threading.Thread(target=check, daemon=True).start()

    def _arm_max_timer(self) -> None:
        self._max_timer = threading.Timer(self.cfg["max_seconds"], self._auto_stop)
        self._max_timer.daemon = True
        self._max_timer.start()

    def _auto_stop(self) -> None:
        if self.mode is not None:
            log("max duration reached — auto stop")
            self.finish()

    # -- shared finish path ------------------------------------------------

    def finish(self) -> None:
        if self.mode is None:
            return
        self.mode = None
        if self._max_timer:
            self._max_timer.cancel()
        wav_bytes, dur, rms = self.recorder.stop_segment()
        log(f"stopped — {dur:.1f}s, rms={rms:.0f}")
        if dur < self.cfg["min_seconds"] or rms < self.cfg["silence_rms"]:
            # silent discard — no popup for accidental Option holds
            self.ui("hide")
            return
        self.busy = True
        self.ui("busy")
        threading.Thread(target=self._process, args=(wav_bytes, dur), daemon=True).start()

    def _process(self, wav_bytes: bytes, audio_s: float) -> None:
        t0 = time.time()
        try:
            r = transcribe(wav_bytes, self.cfg)
            latency = time.time() - t0
            text = r["text"]
            log(f"transcribed in {latency:.1f}s: {text[:120]}")
            if not text:
                self.tracker.record(self.cfg["model"], False, "empty", audio_s,
                                    r["tin"], r["tout"], latency)
                if self.cfg["sounds"]:
                    play("Basso")
                return
            paste_text = text + (" " if self.cfg["trailing_space"] else "")
            paste_at_cursor(paste_text, self.cfg)
            if self.cfg["sounds"]:
                play("Tink")
            self.tracker.record(self.cfg["model"], True, None, audio_s,
                                r["tin"], r["tout"], latency)
        except Exception as e:
            err = str(e)
            log(f"ERROR: {err}")
            kind = "429" if "429" in err or "Rate limit" in err else "http"
            self.tracker.record(self.cfg["model"], False, kind, audio_s, 0, 0,
                                time.time() - t0)
            if self.cfg["sounds"]:
                play("Basso")
        finally:
            self.busy = False
            self.ui("hide")

    # -- keyboard listener callbacks --------------------------------------

    def on_press(self, key) -> None:
        try:
            if self.cfg.get("debug_keys"):
                log(f"key press: {key}")
            if key in self._alt_keys:
                if self.mode is None and not self.busy and self.hold_timer is None:
                    self.t_press = time.time()
                    if self.cfg.get("show_on_press"):
                        # legacy mode: mic + pill at key-down
                        if self.recorder.open_and_arm():
                            self.ui("armed", self.t_press)
                            self.hold_timer = threading.Timer(
                                self.cfg["hold_seconds"], self._hold_fired)
                            self.hold_timer.daemon = True
                            self.hold_timer.start()
                    else:
                        # default: timer only — mic/popup start at hold-fire
                        self.hold_timer = threading.Timer(
                            self.cfg["hold_seconds"], self._hold_fired)
                        self.hold_timer.daemon = True
                        self.hold_timer.start()
                return
            if key in self._ctrl_keys:
                self._mods.add("ctrl")
            elif key in self._shift_keys:
                self._mods.add("shift")
            elif key == keyboard.Key.space:
                if not self._space_down:                  # ignore auto-repeat
                    self._space_down = True
                    if {"ctrl", "shift"} <= self._mods:
                        self.toggle()
        except Exception:
            pass

    def on_release(self, key) -> None:
        try:
            if self.cfg.get("debug_keys"):
                log(f"key release: {key}")
            if key in self._alt_keys:
                if self.hold_timer is not None:           # short press → discard
                    self.hold_timer.cancel()
                    self.hold_timer = None
                    if self.cfg.get("show_on_press"):
                        self.recorder.close()             # only opened in that mode
                        self.ui("hide")
                elif self.mode == "hold":
                    self.finish()
                return
            if key in self._ctrl_keys:
                self._mods.discard("ctrl")
            elif key in self._shift_keys:
                self._mods.discard("shift")
            elif key == keyboard.Key.space:
                self._space_down = False
        except Exception:
            pass

    def toggle(self) -> None:
        if self.busy:
            return
        if self.mode == "toggle":
            self.finish()
        elif self.mode is None:
            if not self.recorder.open_and_arm():
                if self.cfg["sounds"]:
                    play("Basso")
                log("mic failed to open (toggle)")
                return
            self.mode = "toggle"
            self.t_press = time.time()
            self.ui("rec", self.t_press)
            if self.cfg["sounds"]:
                threading.Thread(target=play, args=("Pop",), daemon=True).start()
            log(f"RECORDING (toggle) — limit {self.cfg['max_seconds']}s")
            self._arm_max_timer()


# -------------------------------------------------------------------- modes

def run_check(cfg: dict) -> None:
    log(f"1) config: OK (model={cfg['model']}, hold={cfg['hold_seconds']}s, "
        f"hotkey={cfg['hotkey']}, rate={cfg['sample_rate']}Hz)")
    req = urllib.request.Request(f"{API_BASE}?key={cfg['api_key']}&pageSize=100")
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            models = [m["name"].split("/")[-1] for m in json.loads(r.read()).get("models", [])]
        ok = cfg["model"] in models
        log(f"2) API key: OK — model '{cfg['model']}' "
            + ("available" if ok else "NOT in accessible list!"))
    except Exception as e:
        log(f"2) API key: FAILED — {e}")
    try:
        rec_probe = Recorder()
        if rec_probe.open_and_arm():
            time.sleep(1.0)
            _, dur, rms = rec_probe.stop_segment()
            ok = dur > 0.5 and rms > 0
            log(f"3) microphone (AVFoundation): {'OK' if ok else 'SILENT'} "
                f"(dur={dur:.2f}s, rms={rms:.0f})")
        else:
            log("3) microphone: FAILED to open AVAudioRecorder")
    except Exception as e:
        log(f"3) microphone: FAILED — {e}")
    try:
        import pynput  # noqa: F401
        log("4) pynput: OK (global key capture needs Input Monitoring permission)")
    except Exception as e:
        log(f"4) pynput: FAILED — {e}")


def run_repl(cfg: dict) -> None:
    import AppKit  # noqa: F401  (validate availability early)
    # singleton: a second instance would double-record and double-paste
    lock_file = open("/tmp/gemini_dictation.lock", "w")
    try:
        fcntl.flock(lock_file, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        log("another instance already runs — exiting")
        sys.exit(0)
    ui_q: "queue.Queue" = queue.Queue()
    app = Dictation(cfg, ui_q)
    app.tracker = UsageTracker(cfg["model"])
    start_web(app.tracker, cfg)
    popup = Popup.alloc().initWithQueue_level_(ui_q, app.recorder.level_norm)
    listener = keyboard.Listener(on_press=app.on_press, on_release=app.on_release)
    listener.start()
    if cfg.get("probe_mic_on_start"):
        # trigger the macOS microphone permission prompt at launch (attributed
        # to the app bundle) instead of mid-recording on first use
        def mic_probe():
            probe = Recorder()
            if probe.open_and_arm():
                time.sleep(0.5)
                probe.close()
            else:
                log("mic probe failed — grant Microphone permission, then restart")
        threading.Thread(target=mic_probe, daemon=True).start()
    log(f"ready — HOLD ⌥ ≥ {cfg['hold_seconds']}s → speak → release → paste at cursor")
    log(f"backup toggle hotkey: {cfg['hotkey']}")
    popup.run()


# ---------------------------------------------------------------- cli config

AGENT_LABEL = "vn.ops4vn.gemini-dictation"
EXCLUDE_MODEL_SUBS = ("tts", "image", "banana", "lyria", "video", "native-audio",
                      "embed", "clip", "grounding")
MODEL_NOTES = {
    "gemini-3.5-flash": "KHUYẾN NGHỊ — model đầy đủ, accuracy tốt trong nhóm nhanh",
    "gemini-3.5-flash-lite": "nhanh/nhẹ nhất nhưng accuracy kém hơn với giọng thật",
    "gemini-3.5-transcribe": "STT chuyên dụng, rất nhanh nhưng hay sai từ kỹ thuật khi code-switch",
    "gemini-3.6-flash": "phụ — accuracy hơi kém hơn 3.5-flash",
}


def api_list_models(key: str) -> list:
    req = urllib.request.Request(f"{API_BASE}?key={key}&pageSize=100")
    with urllib.request.urlopen(req, timeout=20) as r:
        return [m["name"].split("/")[-1] for m in json.loads(r.read()).get("models", [])]


def _audio_models(models: list) -> list:
    out = []
    for m in models:
        if any(s in m.lower() for s in EXCLUDE_MODEL_SUBS):
            continue
        if not (m.startswith("gemini") or m.startswith("gemma")):
            continue
        out.append(m)
    known = [m for m in MODEL_NOTES if m in out]
    rest = sorted(m for m in out if m not in MODEL_NOTES)
    return known + rest


def _model_note(m: str) -> str:
    if m in MODEL_NOTES:
        return MODEL_NOTES[m]
    if m.startswith("gemini-omni"):
        return "audio-native, free tier hay 429"
    return ""


def _mask(key: str) -> str:
    return (key[:8] + "…" + key[-4:]) if len(key) > 14 else "***"


def _save_config_field(field: str, value) -> None:
    data = json.loads(CONFIG_PATH.read_text()) if CONFIG_PATH.exists() else {}
    data[field] = value
    CONFIG_PATH.write_text(json.dumps(data, indent=2))
    CONFIG_PATH.chmod(0o600)


def _restart_app() -> None:
    r = subprocess.run(["launchctl", "kickstart", "-k",
                        f"gui/{os.getuid()}/{AGENT_LABEL}"], capture_output=True)
    if r.returncode == 0:
        print("✅ app đã restart (LaunchAgent)")
        return
    subprocess.run(["pkill", "-f", "dictation.py"], capture_output=True)
    time.sleep(1)
    subprocess.run(["open", str(SCRIPT_DIR / "Gemini Dictation.app")], capture_output=True)
    print("✅ app đã restart")


def _maybe_restart() -> None:
    a = input("Restart app để nhận cấu hình mới? [Y/n] ").strip().lower()
    if a in ("", "y", "yes"):
        _restart_app()


def cli_set_key(cfg: dict) -> None:
    key = input("API key mới: ").strip()
    if not key:
        print("bỏ trống — giữ nguyên key cũ")
        return
    try:
        n = len(api_list_models(key))
        print(f"✅ key hợp lệ — truy cập được {n} models")
    except Exception as e:
        print(f"❌ key không dùng được: {e}")
        return
    _save_config_field("api_key", key)
    print("đã lưu key")
    _maybe_restart()


def cli_set_model(cfg: dict) -> None:
    try:
        opts = _audio_models(api_list_models(cfg["api_key"]))
    except Exception as e:
        print(f"❌ không lấy được danh sách model: {e}")
        return
    if not opts:
        print("danh sách trống?!"); return
    print(f"\nModel hiện tại: {cfg['model']}\n")
    for i, m in enumerate(opts, 1):
        note = _model_note(m)
        cur = "  ← đang dùng" if m == cfg["model"] else ""
        print(f"  {i:2d}. {m}{('  — ' + note) if note else ''}{cur}")
    pick = input("\nChọn số (hoặc gõ tên model, Enter = giữ nguyên): ").strip()
    if not pick:
        return
    if pick.isdigit() and 1 <= int(pick) <= len(opts):
        model = opts[int(pick) - 1]
    else:
        model = pick
    if model not in models_list_all(cfg["api_key"]) and model not in opts:
        a = input(f"⚠️ '{model}' không có trong key này — vẫn lưu? [y/N] ").strip().lower()
        if a != "y":
            return
    _save_config_field("model", model)
    print(f"✅ đã lưu model: {model}")
    _maybe_restart()


def models_list_all(key: str) -> list:
    try:
        return api_list_models(key)
    except Exception:
        return []


def cli_config(cfg: dict) -> None:
    while True:
        print(f"\n=== Gemini Dictation — cấu hình ===")
        print(f"  API key : {_mask(cfg['api_key'])}")
        print(f"  Model   : {cfg['model']}  (thinking {'off' if cfg.get('disable_thinking') else 'on'})")
        print(f"  Hold    : {cfg['hold_seconds']}s | Hotkey: {cfg['hotkey']} | Dashboard: 127.0.0.1:{cfg['web_port']}")
        print("  1) Đổi API key")
        print("  2) Chọn model")
        print("  3) Thoát")
        c = input("Chọn: ").strip()
        if c == "1":
            cli_set_key(cfg)
        elif c == "2":
            cli_set_model(cfg)
        elif c == "3":
            return
        cfg = load_config()


def main() -> None:
    ap = argparse.ArgumentParser(description="Gemini Dictation")
    ap.add_argument("--check", action="store_true", help="verify config/API/mic/deps")
    ap.add_argument("--transcribe", metavar="WAV", help="transcribe a wav file and print text")
    ap.add_argument("--config", action="store_true", help="menu cấu hình (API key, model)")
    ap.add_argument("--set-key", metavar="KEY", help="đặt API key (non-interactive, auto-restart)")
    ap.add_argument("--set-model", metavar="MODEL", help="đặt model (non-interactive, auto-restart)")
    args = ap.parse_args()
    cfg = load_config()

    if args.check:
        run_check(cfg)
    elif args.transcribe:
        t0 = time.time()
        r = transcribe(Path(args.transcribe).read_bytes(), cfg)
        print(r["text"])
        log(f"({time.time() - t0:.1f}s via {cfg['model']})")
    elif args.set_key:
        try:
            n = len(api_list_models(args.set_key))
            print(f"✅ key hợp lệ — {n} models")
        except Exception as e:
            sys.exit(f"❌ key không dùng được: {e}")
        _save_config_field("api_key", args.set_key)
        print("đã lưu")
        _restart_app()
    elif args.set_model:
        opts = _audio_models(models_list_all(cfg["api_key"]))
        if args.set_model not in opts and args.set_model not in models_list_all(cfg["api_key"]):
            sys.exit(f"❌ '{args.set_model}' không có trong danh sách model của key này. "
                     f"Gợi ý: {', '.join(opts[:8])}")
        _save_config_field("model", args.set_model)
        print(f"✅ đã lưu model: {args.set_model}")
        _restart_app()
    elif args.config:
        cli_config(cfg)
    else:
        if cfg["sounds"]:
            log("sounds on: Pop=record confirmed, Tink=pasted, Basso=error")
        run_repl(cfg)


if __name__ == "__main__":
    main()
