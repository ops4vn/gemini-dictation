package main

import (
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// App orchestrates: Option-key hold → mic segment → Gemini → paste.
// States: idle → (hold ≥ hold_seconds) rec → (release) busy → idle.

var pillCtx contextCtx // wails runtime context, set in main.go
const pillTitle = "gemini-pill"

// PillState is the event payload pushed to the frontend pill.
type PillState struct {
	State string  `json:"state"` // rec | busy | hide
	T0    float64 `json:"t0,omitempty"`
}

type App struct {
	cfg atomicConfig

	tracker *UsageTracker
	quit    chan struct{}

	mu        sync.Mutex
	mode      string // "", "hold", "toggle"
	busy      bool
	t0        time.Time
	maxTimer  *time.Timer
	holdTimer *time.Timer
}

func NewApp(tracker *UsageTracker, quit chan struct{}) *App {
	return &App{tracker: tracker, quit: quit}
}

func (a *App) Cfg() Config      { return a.cfg.Load() }
func (a *App) SetCfg(c Config)  { a.cfg.Store(c) }

func logf(format string, args ...any) {
	log.Printf(format, args...)
}

// ---------------- pill / wails glue ----------------

func (a *App) emitPill(s PillState) {
	if pillCtx != nil {
		runtime.EventsEmit(pillCtx, "pill", s)
	}
}

func (a *App) placePill() {
	x, y, ok := nativeCursor()
	if !ok || pillCtx == nil {
		return
	}
	const w, h = 380.0, 96.0
	sw, sh := primaryScreenSize()
	px := x + 18
	if px+w > sw-4 {
		px = x - w - 18
	}
	py := y + 16
	if py+h > sh-40 {
		py = y - h - 46
	}
	if px < 4 {
		px = 4
	}
	if py < 4 {
		py = 4
	}
	runtime.WindowSetSize(pillCtx, int(w), int(h))
	runtime.WindowSetPosition(pillCtx, int(px), int(py))
}

func (a *App) showPill() {
	if pillCtx == nil {
		return
	}
	a.placePill()
	runtime.WindowShow(pillCtx)
	// re-assert click-through shortly after show (window may be recreated)
	time.AfterFunc(1200*time.Millisecond, func() {
		setClickThrough(pillTitle, true)
	})
}

func (a *App) hidePill() {
	if pillCtx != nil {
		runtime.WindowHide(pillCtx)
	}
}

func (a *App) uiError(msg string) {
	logf("UI: %s", msg)
}

// ---------------- tap event entry ----------------

type tapEvt struct {
	OptionDown bool
}

func (a *App) OnTap(down bool) {
	a.mu.Lock()
	if a.cfg.Load().DebugKeys {
		logf("tap: optionDown=%v mode=%q busy=%v", down, a.mode, a.busy)
	}
	if down {
		a.onOptionDownLocked()
	} else {
		a.onOptionUpLocked()
	}
	a.mu.Unlock()
}

func (a *App) onOptionDownLocked() {
	c := a.cfg.Load()
	if a.mode != "" || a.busy || a.holdTimer != nil {
		return
	}
	a.t0 = time.Now()
	h := c.HoldSeconds
	if h < 0.2 {
		h = 1.5
	}
	a.holdTimer = time.AfterFunc(time.Duration(h*float64(time.Second)), a.holdFired)
}

func (a *App) onOptionUpLocked() {
	if a.holdTimer != nil { // released before threshold — mic never opened
		a.holdTimer.Stop()
		a.holdTimer = nil
		return
	}
	if a.mode == "hold" || a.mode == "toggle" {
		a.finishLocked()
	}
}

func (a *App) holdFired() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.holdTimer = nil // threshold reached — release must finish, not discard
	if a.mode != "" || a.busy {
		return
	}
	c := a.cfg.Load()
	if !micStart() {
		playSound(c.Sounds, "Basso")
		logf("mic failed to open (permission/device)")
		return
	}
	a.mode = "hold"
	a.t0 = time.Now()
	sounds := c.Sounds
	holdT0 := a.t0
	maxS := c.MaxSeconds

	if sounds {
		go playSoundFile("Pop")
	}
	a.emitPill(PillState{State: "rec", T0: float64(holdT0.UnixMilli()) / 1000})
	a.showPill()
	logf("RECORDING (hold) — limit %.0fs", maxS)
	a.maxTimer = time.AfterFunc(time.Duration(maxS*float64(time.Second)), func() {
		a.mu.Lock()
		should := a.mode == "hold"
		a.mu.Unlock()
		if should {
			logf("max duration reached — auto stop")
			a.finish()
		}
	})
}

// Toggle is the backup Ctrl+Shift+Space binding (wired from the tap handler
// when all three modifiers are detected — see tap.go).
func (a *App) Toggle() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return
	}
	if a.mode == "toggle" {
		a.finishLocked()
		return
	}
	if a.mode != "" {
		return
	}
	c := a.cfg.Load()
	if !micStart() {
		playSound(c.Sounds, "Basso")
		logf("mic failed to open (toggle)")
		return
	}
	a.mode = "toggle"
	a.t0 = time.Now()
	sounds := c.Sounds
	if sounds {
		go playSoundFile("Pop")
	}
	a.emitPill(PillState{State: "rec", T0: float64(a.t0.UnixMilli()) / 1000})
	a.showPill()
	logf("RECORDING (toggle)")
	a.maxTimer = time.AfterFunc(time.Duration(c.MaxSeconds*float64(time.Second)), func() {
		a.mu.Lock()
		should := a.mode == "toggle"
		a.mu.Unlock()
		if should {
			a.finish()
		}
	})
}

// finish must be called with a.mu held or via the public wrapper.
func (a *App) finishLocked() {
	if a.mode == "" {
		return
	}
	mode := a.mode
	a.mode = ""
	if a.maxTimer != nil {
		a.maxTimer.Stop()
	}
	micStop()
	wav, dur, rms, err := readSegment()
	logf("stopped (%s) — %.1fs, rms=%.0f", mode, dur, rms)
	if err != nil || dur < a.cfg.Load().MinSeconds || rms < a.cfg.Load().SilenceRMS {
		// silent discard — no popup for accidental Option holds
		a.emitPill(PillState{State: "hide"})
		a.hidePill()
		return
	}
	a.busy = true
	a.emitPill(PillState{State: "busy"})
	go a.process(wav, dur)
}

func (a *App) finish() {
	a.mu.Lock()
	a.finishLocked()
	a.mu.Unlock()
}

func (a *App) process(wav []byte, audioS float64) {
	c := a.cfg.Load()
	var errKind string
	var res TranscribeResult
	var err error
	res, err = Transcribe(wav, c)
	if err != nil {
		errKind = "http"
		var rl *RateLimitError
		if errors.As(err, &rl) {
			errKind = "429"
		}
		logf("ERROR: %v", err)
		a.tracker.Record(c.Model, false, errKind, audioS, 0, 0, res.Latency.Seconds())
		playSound(c.Sounds, "Basso")
		a.done()
		return
	}
	text := strings.TrimSpace(res.Text)
	logf("transcribed in %.1fs: %.120s", res.Latency.Seconds(), text)
	if text == "" {
		a.tracker.Record(c.Model, false, "empty", audioS, res.TokensIn, res.TokensOut, res.Latency.Seconds())
		playSound(c.Sounds, "Basso")
		a.done()
		return
	}
	if c.TrailingSpace {
		text += " "
	}
	PasteAtCursor(text, c)
	playSound(c.Sounds, "Tink")
	a.tracker.Record(c.Model, true, "", audioS, res.TokensIn, res.TokensOut, res.Latency.Seconds())
	a.done()
}

func (a *App) done() {
	a.mu.Lock()
	a.busy = false
	a.mu.Unlock()
	a.emitPill(PillState{State: "hide"})
	a.hidePill()
}

// ---------------- wails bindings (frontend JS) ----------------

// GetLevel returns the normalized mic level for the waveform animation.
func (a *App) GetLevel() float64 { return micLevel() }

// GetInfo returns a small state summary for the frontend.
func (a *App) GetInfo() map[string]any {
	return map[string]any{"version": AppVersion, "model": a.cfg.Load().Model}
}

// QuitApp is bound to the dashboard Quit button.
func (a *App) QuitApp() {
	logf("quit requested from dashboard")
	select {
	case a.quit <- struct{}{}:
	default:
	}
}

// ---------------- helpers ----------------

func playSound(sounds bool, name string) {
	if sounds {
		go playSoundFile(name)
	}
}
