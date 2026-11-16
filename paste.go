package main

import (
	"os"
	"os/exec"
	"time"
)

const (
	pbcopyPath    = "/usr/bin/pbcopy"
	pbpastePath   = "/usr/bin/pbpaste"
	osascriptPath = "/usr/bin/osascript"
)

// PasteAtCursor inserts text at the focused caret: clipboard round-trip +
// synthetic Cmd+V, then restores the previous clipboard after a delay.
func PasteAtCursor(text string, cfg Config) {
	var old []byte
	if cfg.RestoreClipboard {
		if out, err := exec.Command(pbpastePath).Output(); err == nil {
			old = out
		}
	}
	p := exec.Command(pbcopyPath)
	stdin, err := p.StdinPipe()
	if err == nil {
		p.Start()
		stdin.Write([]byte(text))
		stdin.Close()
		p.Wait()
	}
	time.Sleep(50 * time.Millisecond)
	_ = exec.Command(osascriptPath, "-e",
		`tell application "System Events" to keystroke "v" using command down`).Run()
	if cfg.RestoreClipboard && len(old) >= 0 {
		go func() {
			time.Sleep(1500 * time.Millisecond)
			p2 := exec.Command(pbcopyPath)
			s2, err2 := p2.StdinPipe()
			if err2 == nil {
				p2.Start()
				s2.Write(old)
				s2.Close()
				p2.Wait()
			}
		}()
	}
}

func PlaySound(name string) {
	go func() {
		_ = exec.Command("/usr/bin/afplay", "/System/Library/Sounds/"+name+".aiff").Run()
	}()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
