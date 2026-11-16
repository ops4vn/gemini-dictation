package main

import (
	"context"
	"encoding/binary"
	"log"
	"math"
	"os"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// contextCtx alias shared by app.go/main.go; pillCtx lives in app.go.
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

// liveLevel tails the segment WAV while recording and returns the RMS of the
// last flushed bytes as a 0..1 meter (native metering is dead in-process —
// see audio_darwin.m). The file flushes in ~10KB chunks with ~0.3s lag.
func liveLevel() float64 {
	f, err := os.Open(segmentPath())
	if err != nil {
		return 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() < 44+400 {
		return 0
	}
	n := int64(3200)
	if st.Size()-44 < n {
		n = st.Size() - 44
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, st.Size()-n); err != nil {
		return 0
	}
	var sum float64
	for i := 0; i < len(buf)/2; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(buf[i*2:])))
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(len(buf)/2))
	if rms == 0 {
		return 0
	}
	db := 20 * math.Log10(rms / 32768)
	if db < -60 {
		return 0
	}
	if db > 0 {
		return 1
	}
	return (db + 60) / 60
}

// readSegment reads the segment WAV produced by the platform mic layer.
// Returns (bytes, durationSeconds, rms, err).
func readSegment() ([]byte, float64, float64, error) {
	raw, err := os.ReadFile(segmentPath())
	if err != nil {
		return nil, 0, 0, err
	}
	if len(raw) < 44 {
		return nil, 0, 0, nil
	}
	data := raw[44:] // canonical 44-byte PCM WAV header (both platforms write it)
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

// micProbe opens/closes the mic briefly at launch so the OS permission
// prompt appears early (attributed to the app, not a terminal).
func micProbe() {
	if !micStart() {
		log.Println("mic probe failed — grant Microphone permission, then restart")
		return
	}
	time.Sleep(500 * time.Millisecond)
	micStop()
	log.Println("mic probe ok")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
