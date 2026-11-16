//go:build darwin

// Package native wraps the CGO/ObjC pieces: global Option-key event tap,
// AVFoundation mic capture, cursor position. Everything else is plain Go.
package native

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit -framework CoreFoundation -framework CoreGraphics -framework AVFoundation -framework Foundation
#include <stdlib.h>

extern void DictationTapCallback(int eventType, int hasOption);
int  DictationTapStart(void (*cb)(int, int));
void DictationTapStop(void);
int  DictationTapIsEnabled(void);
void DictationTapReenable(void);
int  DictationCursorPos(double *x, double *y);

int    DictationAudioStart(void);
double DictationAudioDuration(void);
double DictationAudioLevel(void);
void   DictationAudioStop(void);

int DictationSetClickThrough(const char *title, int enable);
*/
import "C"

import (
	"sync"
	"unsafe"
)

// TapEvent is what the C callback forwards: CGEventType + whether the Option
// flag was on. The Go side derives press/release from the Option flag.
type TapEvent struct {
	Type       int
	HasOption  bool
}

var tapMu sync.Mutex
var tapEvents chan TapEvent

//export DictationTapCallback
func DictationTapCallback(eventType C.int, hasOption C.int) {
	tapMu.Lock()
	ch := tapEvents
	tapMu.Unlock()
	if ch != nil {
		// non-blocking: drop if the consumer is slow (dictation UX tolerates it)
		select {
		case ch <- TapEvent{Type: int(eventType), HasOption: hasOption != 0}:
		default:
		}
	}
}

// StartTap begins listening for Option-key events. Returns the event channel
// and true if the tap was created (false = Input Monitoring not granted).
func StartTap() (<-chan TapEvent, bool, error) {
	tapMu.Lock()
	defer tapMu.Unlock()
	if tapEvents != nil {
		return nil, false, nil
	}
	ch := make(chan TapEvent, 64)
	tapEvents = ch
	ok := C.DictationTapStart((*[0]byte)(C.DictationTapCallback))
	if int(ok) == 0 {
		tapEvents = nil
		return nil, false, nil
	}
	return ch, true, nil
}

// StopTap tears the tap down.
func StopTap() {
	tapMu.Lock()
	defer tapMu.Unlock()
	C.DictationTapStop()
	tapEvents = nil
}

// TapIsEnabled reports whether macOS still has the tap enabled (telemetry
// for diagnosing silent tap death).
func TapIsEnabled() bool {
	return int(C.DictationTapIsEnabled()) == 1
}

// TapReenable forces the tap back on (Go-side watchdog path).
func TapReenable() {
	C.DictationTapReenable()
}

// MicStart opens a fresh capture session. False = mic denied/broken.
func MicStart() bool {
	return int(C.DictationAudioStart()) == 1
}

// MicDuration returns recorded seconds so far (0 = not armed).
func MicDuration() float64 {
	return float64(C.DictationAudioDuration())
}

// MicLevel returns the normalized 0..1 metering level (waveform UI).
func MicLevel() float64 {
	return float64(C.DictationAudioLevel())
}

// MicStop ends the session; segment file is the fixed temp WAV.
func MicStop() {
	C.DictationAudioStop()
}

const SegmentPath = "/tmp/gemini_dictation_segment.wav"

// CursorPos returns the global mouse position (top-left origin).
func CursorPos() (float64, float64, bool) {
	var x, y C.double
	if int(C.DictationCursorPos(&x, &y)) == 0 {
		return 0, 0, false
	}
	return float64(x), float64(y), true
}

// SetClickThrough makes the Wails pill window transparent to clicks.
// Returns false if the window wasn't found (not created yet).
func SetClickThrough(title string, enable bool) bool {
	cstr := C.CString(title)
	defer C.free(unsafe.Pointer(cstr))
	enableInt := 0
	if enable {
		enableInt = 1
	}
	return int(C.DictationSetClickThrough(cstr, C.int(enableInt))) == 1
}
