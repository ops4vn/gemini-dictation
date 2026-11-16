//go:build windows

package main

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/gen2brain/malgo"
	"golang.org/x/sys/windows"
)

// ---------------- singleton lock ----------------

// tryLockFile/unlockFile implement the singleton lock (flock on darwin;
// LockFileEx on windows — see desktop_darwin.go).
func tryLockFile(fd uintptr) bool {
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(windows.Handle(fd),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol)
	return err == nil
}

func unlockFile(fd uintptr) {
	ol := new(windows.Overlapped)
	_ = windows.UnlockFileEx(windows.Handle(fd), 0, 1, 0, ol)
}

// ---------------- global Alt-key hook (push-to-talk trigger) ----------------

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	pSetWindowsHookEx    = user32.NewProc("SetWindowsHookExW")
	pGetMessage          = user32.NewProc("GetMessageW")
	pCallNextHookEx      = user32.NewProc("CallNextHookEx")
	pUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	pGetCursorPos        = user32.NewProc("GetCursorPos")
)

const (
	whKeyboardLL = 13
	wmKeyDown    = 0x0100
	wmKeyUp      = 0x0101
	wmSysKeyDown = 0x0104
	wmSysKeyUp   = 0x0105
	vkLMenu      = 0xA4 // left Alt
	vkRMenu      = 0xA5 // right Alt
)

type kbdLLHookStruct struct {
	VkCode      uint32
	ScanCode    uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

type pointStruct struct{ X, Y int32 }

type msgStruct struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      pointStruct
}

var (
	hookMu   sync.Mutex
	hookCb   func(down bool)
	hookPump bool // pump goroutine started
)

// keyboardProc must return fast and always chain to the next hook, or
// Windows silently removes the hook (LowLevelHooksTimeout).
func keyboardProc(nCode int32, wParam uintptr, lParam *kbdLLHookStruct) uintptr {
	if nCode >= 0 && hookCb != nil {
		if lParam.VkCode == vkLMenu || lParam.VkCode == vkRMenu {
			switch wParam {
			case wmKeyDown, wmSysKeyDown:
				hookCb(true)
			case wmKeyUp, wmSysKeyUp:
				hookCb(false)
			}
		}
	}
	r, _, _ := pCallNextHookEx.Call(0, uintptr(nCode), wParam, uintptr(unsafe.Pointer(lParam)))
	return r
}

// startTap installs a low-level keyboard hook watching Alt down/up (the
// Windows analog of holding ⌥). LL-hook callbacks are delivered to the
// installing thread's message queue, so the hook and its GetMessage pump
// are pinned to one locked OS thread. Events are handed to a dispatcher
// goroutine (ordered, non-blocking) so a slow handler can never trip the
// hook timeout.
func startTap(cb func(down bool)) (bool, error) {
	hookMu.Lock()
	defer hookMu.Unlock()
	if hookPump {
		return true, nil
	}
	events := make(chan bool, 64)
	hookCb = func(down bool) {
		select {
		case events <- down:
		default: // dispatcher behind — drop rather than block the hook thread
		}
	}
	installed := make(chan error, 1)
	go func() {
		for down := range events {
			cb(down)
		}
	}()
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		proc := syscall.NewCallback(keyboardProc)
		h, _, err := pSetWindowsHookEx.Call(whKeyboardLL, proc, 0, 0)
		if h == 0 {
			installed <- fmt.Errorf("SetWindowsHookEx failed: %v", err)
			return
		}
		installed <- nil
		var msg msgStruct
		for {
			ret, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
			if int32(ret) <= 0 { // WM_QUIT or error
				break
			}
		}
		pUnhookWindowsHookEx.Call(h)
	}()
	hookPump = true
	if err := <-installed; err != nil {
		hookPump = false
		hookCb = nil
		return false, err
	}
	return true, nil
}

// ---------------- mic capture (miniaudio / WASAPI) ----------------

var (
	micMu        sync.Mutex // guards ctx/device lifecycle
	bufMu        sync.Mutex // guards micBuf (audio callback vs readers)
	micCtx       *malgo.AllocatedContext
	micDevice    *malgo.Device
	micBuf       []byte // PCM int16 mono 16k
	micRunning   atomic.Bool
	micLastLevel atomic.Uint64 // math.Float64bits of 0..1, for the waveform
)

const micRate = 16000

// micLevelFromRMS maps RMS to 0..1 on the same dBFS scale the macOS meter
// uses ((db+60)/60), so the pill waveform looks identical on both platforms.
func micLevelFromRMS(sum float64, n int) float64 {
	if n == 0 {
		return 0
	}
	lin := math.Sqrt(sum/float64(n)) / 32768
	if lin <= 0 {
		return 0
	}
	db := 20 * math.Log10(lin)
	if db <= -60 {
		return 0
	}
	if db >= 0 {
		return 1
	}
	return (db + 60) / 60
}

// tap health watchdog is macOS-only (CGEventTap); no-op on Windows.
func tapEnabled() bool { return true }
func tapReenable()    {}
func tapRestart() bool { return true }

func micStart() bool {
	micMu.Lock()
	defer micMu.Unlock()
	if micDevice != nil {
		return true // already armed
	}
	os.Remove(segmentPath()) // never reuse a stale segment
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return false
	}
	cfg := malgo.DefaultDeviceConfig(malgo.Capture)
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = 1
	cfg.SampleRate = micRate
	cfg.PeriodSizeInMilliseconds = 20

	bufMu.Lock()
	micBuf = nil
	bufMu.Unlock()
	onRecv := func(_, pInput []byte, frameCount uint32) {
		if !micRunning.Load() {
			return
		}
		bufMu.Lock()
		micBuf = append(micBuf, pInput...)
		bufMu.Unlock()
		n := len(pInput) / 2
		var sum float64
		for i := 0; i < n; i++ {
			v := float64(int16(binary.LittleEndian.Uint16(pInput[i*2:])))
			sum += v * v
		}
		micLastLevel.Store(math.Float64bits(micLevelFromRMS(sum, n)))
	}
	onStop := func() { micRunning.Store(false) }
	dev, err := malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{Data: onRecv, Stop: onStop})
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return false
	}
	if err := dev.Start(); err != nil {
		dev.Uninit()
		_ = ctx.Uninit()
		ctx.Free()
		return false
	}
	micCtx = ctx
	micDevice = dev
	micRunning.Store(true)
	return true
}

func micStop() {
	micMu.Lock()
	defer micMu.Unlock()
	micRunning.Store(false)
	if micDevice != nil {
		_ = micDevice.Stop()
		micDevice.Uninit()
		micDevice = nil
	}
	if micCtx != nil {
		_ = micCtx.Uninit()
		micCtx.Free()
		micCtx = nil
	}
	bufMu.Lock()
	pcm := micBuf
	micBuf = nil
	bufMu.Unlock()
	micLastLevel.Store(0)                 // Float64bits(0) == 0
	writeWAV(segmentPath(), pcm, micRate) // always rewrite — no stale segments
}

func micLevel() float64 { return math.Float64frombits(micLastLevel.Load()) }

func micDuration() float64 {
	bufMu.Lock()
	n := len(micBuf) / 2
	bufMu.Unlock()
	return float64(n) / micRate
}

func segmentPath() string {
	return filepath.Join(os.TempDir(), "gemini_dictation_segment.wav")
}

// writeWAV writes a canonical 44-byte-header PCM WAV (int16 mono).
func writeWAV(path string, pcm []byte, rate int) {
	hdr := make([]byte, 44)
	copy(hdr[0:], "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(36+len(pcm)))
	copy(hdr[8:], "WAVE")
	copy(hdr[12:], "fmt ")
	binary.LittleEndian.PutUint32(hdr[16:], 16)
	binary.LittleEndian.PutUint16(hdr[20:], 1) // PCM
	binary.LittleEndian.PutUint16(hdr[22:], 1) // mono
	binary.LittleEndian.PutUint32(hdr[24:], uint32(rate))
	binary.LittleEndian.PutUint32(hdr[28:], uint32(rate*2)) // byte rate
	binary.LittleEndian.PutUint16(hdr[32:], 2)              // block align
	binary.LittleEndian.PutUint16(hdr[34:], 16)             // bits per sample
	copy(hdr[36:], "data")
	binary.LittleEndian.PutUint32(hdr[40:], uint32(len(pcm)))
	_ = os.WriteFile(path, append(hdr, pcm...), 0o644)
}

// ---------------- cursor / window / paste ----------------

func nativeCursor() (float64, float64, bool) {
	var pt pointStruct
	r, _, _ := pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	if r == 0 {
		return 0, 0, false
	}
	return float64(pt.X), float64(pt.Y), true
}

// setClickThrough is a macOS-only nicety; the pill on Windows gets no clicks.
func setClickThrough(title string, enable bool) bool { return true }

// PowerShell snippets. Text travels over stdin and is decoded as UTF-8
// in-script (console code pages never touch it), so Vietnamese diacritics
// survive; the saved clipboard round-trips as base64 for the same reason.
const psSetClipboard = `$ms = New-Object System.IO.MemoryStream; ` +
	`[Console]::OpenStandardInput().CopyTo($ms); ` +
	`Set-Clipboard -Value ([Text.Encoding]::UTF8.GetString($ms.ToArray()))`

const psPaste = psSetClipboard + `; ` +
	`Add-Type -AssemblyName System.Windows.Forms; ` +
	`Start-Sleep -Milliseconds 60; ` +
	`[System.Windows.Forms.SendKeys]::SendWait('^v')`

const psGetClipboardB64 = `[Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([string](Get-Clipboard -Raw)))`

// PasteAtCursor: clipboard round-trip via PowerShell + synthetic Ctrl+V,
// restoring the previous clipboard shortly after.
func PasteAtCursor(text string, cfg Config) {
	var old []byte
	if cfg.RestoreClipboard {
		if out, err := exec.Command("powershell", "-NoProfile", "-Command",
			psGetClipboardB64).Output(); err == nil {
			old, _ = base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
		}
	}
	pipePS(psPaste, text)
	if cfg.RestoreClipboard {
		go func() {
			time.Sleep(1500 * time.Millisecond)
			pipePS(psSetClipboard, string(old))
		}()
	}
}

func playSoundFile(name string) {
	wav := map[string]string{
		"Pop":   `C:\Windows\Media\Speech On.wav`,
		"Tink":  `C:\Windows\Media\chimes.wav`,
		"Basso": `C:\Windows\Media\Windows Critical Stop.wav`,
	}[name]
	if wav == "" {
		return
	}
	_ = exec.Command("powershell", "-NoProfile", "-Command",
		fmt.Sprintf(`(New-Object Media.SoundPlayer '%s').PlaySync()`, wav)).Run()
}

func pipePS(script, stdinText string) {
	p := exec.Command("powershell", "-NoProfile", "-Command", script)
	in, err := p.StdinPipe()
	if err != nil {
		return
	}
	p.Start()
	in.Write([]byte(stdinText))
	in.Close()
	p.Wait()
}
