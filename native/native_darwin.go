//go:build darwin

// Package native wraps the CGO/ObjC pieces: global Option-key event tap,
// AVFoundation mic capture, cursor position. Everything else is plain Go.
package native

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit -framework CoreFoundation -framework CoreGraphics -framework AVFoundation -framework Foundation -framework WebKit -framework ApplicationServices
#include <stdlib.h>

extern void DictationTapCallback(int eventType, int hasOption);
int  DictationTapStart(void (*cb)(int, int));
void DictationTapStop(void);
int  DictationTapIsEnabled(void);
int  DictationPromptAccessibility(void);
int  DictationTapPermission(void);
int  DictationTapRequest(void);
void DictationTapReenable(void);
int  DictationTapRestart(void);
int  DictationCursorPos(double *x, double *y);
void DictationTraySetup(void);

int    DictationAudioStart(void);
double DictationAudioDuration(void);
double DictationAudioLevel(void);
void   DictationAudioStop(void);

int DictationSetClickThrough(const char *title, int enable);
int  DictationMakeOverlay(const char *title);
int  DictationShowWindow(const char *title);
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

// TapPermission reports real Input Monitoring state (preflight).
func TapPermission() bool {
	return int(C.DictationTapPermission()) == 1
}

// TapRequest asks macOS to prompt for Input Monitoring.
func TapRequest() bool {
	return int(C.DictationTapRequest()) == 1
}

// PromptAccessibility shows the macOS trust dialog for Accessibility and
// returns whether the app is currently trusted.
func PromptAccessibility() bool {
	return int(C.DictationPromptAccessibility()) == 1
}

// TapRestart tears down and recreates the tap (last-resort self-heal).
func TapRestart() bool {
	return int(C.DictationTapRestart()) == 1
}

// Tray hooks, invoked from the native menu-bar item.
var TrayOpenHook func()
var TrayQuitHook func()

//export DictationTrayOpen
func DictationTrayOpen() {
	if TrayOpenHook != nil {
		TrayOpenHook()
	}
}

//export DictationTrayQuit
func DictationTrayQuit() {
	if TrayQuitHook != nil {
		TrayQuitHook()
	}
}

// TraySetup installs the menu-bar item (dashboard + quit).
func TraySetup() {
	C.DictationTraySetup()
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

// MakeOverlay restyles the pill window as a floating overlay and hides the
// app from the Dock / app switcher. Returns false if the window wasn't found.
func MakeOverlay(title string) bool {
	cstr := C.CString(title)
	defer C.free(unsafe.Pointer(cstr))
	return int(C.DictationMakeOverlay(cstr)) == 1
}

// ShowWindow orderFrontRegardless — visible without taking focus.
func ShowWindow(title string) bool {
	cstr := C.CString(title)
	defer C.free(unsafe.Pointer(cstr))
	return int(C.DictationShowWindow(cstr)) == 1
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
