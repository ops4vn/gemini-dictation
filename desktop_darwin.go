//go:build darwin

package main

import (
	"context"
	"os/exec"
	"syscall"
	"time"

	"github.com/ops4vn/gemini-dictation/native"
)

// tryLockFile/unlockFile implement the singleton lock (flock on darwin;
// LockFileEx on windows — see desktop_windows.go).
func tryLockFile(fd uintptr) bool {
	return syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}

func unlockFile(fd uintptr) { syscall.Flock(int(fd), syscall.LOCK_UN) }

// startTap begins listening for Option-key hold/release; cb receives
// key-down=true / key-up=false. ok=false means Input Monitoring missing.
func startTap(cb func(down bool)) (bool, error) {
	ch, ok, err := native.StartTap()
	if err != nil || !ok {
		return ok, err
	}
	go func() {
		for ev := range ch {
			cb(ev.HasOption)
		}
	}()
	return true, nil
}

func micStart() bool       { return native.MicStart() }
func tapEnabled() bool     { return native.TapIsEnabled() }
func tapReenable()         { native.TapReenable() }
func tapRestart() bool     { return native.TapRestart() }

// makeOverlay hides the app from Dock/⌘-Tab and floats the pill above all
// windows (a true overlay, not an application window).
func makeOverlay(title string) bool { return native.MakeOverlay(title) }

// openURL opens a URL / settings pane (dashboard onboarding buttons).
func openURL(u string) { exec.Command("open", u).Start() }

// showWinNoActivate shows the pill without stealing keyboard focus.
func showWinNoActivate() bool { return native.ShowWindow(pillTitle) }

// micProbeQuick arms + immediately stops the mic to test the TCC grant.
func micProbeQuick() bool {
	if micStart() {
		micStop()
		return true
	}
	return false
}

// pasteProbe reads a UI element via System Events — UI scripting needs the
// same Accessibility grant the paste keystroke does (a plain process-name
// query only needs Automation and returns a false positive).
func pasteProbe() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/osascript", "-e",
		`tell application "System Events" to get position of window 1 of process "Finder"`).Run() == nil
}
func micStop()             { native.MicStop() }
func micLevel() float64    { return native.MicLevel() }
func micDuration() float64 { return native.MicDuration() }

func segmentPath() string { return native.SegmentPath }

// nativeCursor returns the global mouse position (top-left origin).
func nativeCursor() (float64, float64, bool) { return native.CursorPos() }

// setClickThrough makes the pill window transparent to mouse events.
func setClickThrough(title string, enable bool) bool {
	return native.SetClickThrough(title, enable)
}

// PasteAtCursor inserts text at the focused caret: clipboard round-trip +
// synthetic Cmd+V, restoring the previous clipboard shortly after.
func PasteAtCursor(text string, cfg Config) {
	// A pending Accessibility/Automation prompt blocks osascript forever;
	// the timeout skips the paste instead of wedging `busy` so no later
	// hold is ever accepted (observed live: tap goes deaf while the dialog
	// waits for the user).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var old []byte
	if cfg.RestoreClipboard {
		if out, err := exec.CommandContext(ctx, "/usr/bin/pbpaste").Output(); err == nil {
			old = out
		}
	}
	pipeTo("/usr/bin/pbcopy", text)
	time.Sleep(50 * time.Millisecond)
	if err := exec.CommandContext(ctx, "/usr/bin/osascript", "-e",
		`tell application "System Events" to keystroke "v" using command down`).Run(); err != nil {
		logf("paste failed (check Accessibility/Automation permission): %v", err)
	}
	if cfg.RestoreClipboard {
		go func() {
			time.Sleep(1500 * time.Millisecond)
			pipeTo("/usr/bin/pbcopy", string(old))
		}()
	}
}

func playSoundFile(name string) {
	_ = exec.Command("/usr/bin/afplay", "/System/Library/Sounds/"+name+".aiff").Run()
}

func pipeTo(cmdPath, text string) {
	p := exec.Command(cmdPath)
	stdin, err := p.StdinPipe()
	if err != nil {
		return
	}
	p.Start()
	stdin.Write([]byte(text))
	stdin.Close()
	p.Wait()
}
