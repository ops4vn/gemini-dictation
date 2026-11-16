package main

import (
	"context"
	"encoding/binary"
	"log"
	"math"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/ops4vn/gemini-dictation/native"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// contextCtx alias + helpers shared by app.go/main.go. pillCtx itself is
// declared in app.go and set by main.go once the wails context is ready.
type contextCtx = context.Context

// atomicConfig is a tiny mutex-protected config holder.
type atomicConfig struct {
	mu sync.Mutex
	c  Config
}

func (a *atomicConfig) Load() Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.c
}

func (a *atomicConfig) Store(c Config) {
	a.mu.Lock()
	a.c = c
	a.mu.Unlock()
}

// ---------------- native wrappers ----------------

func nativeCursor() (float64, float64, bool) { return native.CursorPos() }

func nativeSetClickThrough(title string, enable bool) bool {
	return native.SetClickThrough(title, enable)
}

func micStart() bool    { return native.MicStart() }
func micStop()          { native.MicStop() }
func micLevel() float64 { return native.MicLevel() }
func micDuration() float64 { return native.MicDuration() }

// readSegment reads the fixed temp WAV produced by the mic session.
// Returns (bytes, durationSeconds, rms, err).
func readSegment() ([]byte, float64, float64, error) {
	raw, err := os.ReadFile(native.SegmentPath)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(raw) < 44 {
		return nil, 0, 0, nil
	}
	data := raw[44:] // standard 44-byte PCM WAV header
	n := len(data) / 2
	if n == 0 {
		return raw, 0, 0, nil
	}
	dur := float64(n) / 16000.0 // int16 mono @16k
	var sum float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(data[i*2:])))
		sum += v * v
	}
	return raw, dur, math.Sqrt(sum/float64(n)), nil
}

func playSoundFile(name string) {
	_ = exec.Command("/usr/bin/afplay", "/System/Library/Sounds/"+name+".aiff").Run()
}

// primaryScreenSize returns the current screen in logical points.
func primaryScreenSize() (w, h float64) {
	if pillCtx != nil {
		screens, err := runtime.ScreenGetAll(pillCtx)
		if err == nil {
			for _, s := range screens {
				if s.IsCurrent {
					return float64(s.Size.Width), float64(s.Size.Height)
				}
			}
		}
	}
	return 1440, 900
}

// micProbe opens/closes the mic briefly at launch so the macOS permission
// prompt appears early (attributed to the app bundle, not a terminal).
func micProbe() {
	if !micStart() {
		log.Println("mic probe failed — grant Microphone permission, then restart")
		return
	}
	time.Sleep(500 * time.Millisecond)
	micStop()
	log.Println("mic probe ok")
}
