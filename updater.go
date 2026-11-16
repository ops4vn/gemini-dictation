package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	runtime "runtime"
	"strings"
	"time"
)

// Updater: polls a manifest published to Cloudflare R2 / GitHub Releases
// (any static host):
//   { "version": "1.1.0",
//     "macos":   {"sha256": "…", "url": "…-macos-universal.zip"},
//     "windows": {"sha256": "…", "url": "…-windows-x64.zip"} }
// Legacy flat manifests ({"version","sha256","url"}) still work.
// On macOS: download → verify sha256 → swap .app bundle → relaunch.
// Windows: update check only (auto-swap not supported — download manually).
// Disabled when update.url is empty.

type PlatformAsset struct {
	SHA256 string `json:"sha256"`
	URL    string `json:"url"`
}

type Manifest struct {
	Version string         `json:"version"`
	SHA256  string         `json:"sha256,omitempty"` // legacy flat layout
	URL     string         `json:"url,omitempty"`
	MacOS   *PlatformAsset `json:"macos,omitempty"`
	Windows *PlatformAsset `json:"windows,omitempty"`
	Notes   string         `json:"notes,omitempty"`
}

// Asset returns the entry for the running OS (falls back to flat layout).
func (m *Manifest) Asset() *PlatformAsset {
	switch runtime.GOOS {
	case "darwin":
		if m.MacOS != nil {
			return m.MacOS
		}
	case "windows":
		if m.Windows != nil {
			return m.Windows
		}
	}
	return &PlatformAsset{SHA256: m.SHA256, URL: m.URL}
}

var AppVersion = "dev"

func CheckUpdate(cfg Config) (*Manifest, error) {
	if cfg.Update.URL == "" {
		return nil, errors.New("updater disabled (update.url empty)")
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(strings.TrimRight(cfg.Update.URL, "/") + "/latest.json")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	if m.Version == "" || m.Asset().URL == "" {
		return nil, errors.New("manifest missing version/url")
	}
	return &m, nil
}

func VersionNewer(latest, current string) bool {
	if latest == "" || current == "" || current == "dev" {
		return false
	}
	parts := func(v string) []int {
		var out []int
		for _, p := range strings.Split(strings.TrimPrefix(v, "v"), ".") {
			n := 0
			fmt.Sscanf(p, "%d", &n)
			out = append(out, n)
		}
		return out
	}
	a, b := parts(latest), parts(current)
	for i := 0; i < 3; i++ {
		av, bv := 0, 0
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			return av > bv
		}
	}
	return false
}

// ApplyUpdate downloads + verifies + swaps the .app bundle, returns relaunch cmd.
func ApplyUpdate(m *Manifest, cfg Config) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", fmt.Errorf("auto-update on %s not supported — download %s manually",
			runtime.GOOS, m.Asset().URL)
	}
	tmpZip := "/tmp/gemini-dictation-update.zip"
	a := m.Asset()
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(a.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.Create(tmpZip)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	f.Close()
	if err != nil {
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		os.Remove(tmpZip)
		return "", fmt.Errorf("sha256 mismatch: got %s want %s", got, a.SHA256)
	}

	// zip is expected to contain "Gemini Dictation.app/" at its root
	zr, err := zip.OpenReader(tmpZip)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	extractDir := "/tmp/gemini-dictation-update"
	os.RemoveAll(extractDir)
	for _, zf := range zr.File {
		if err := extractFile(zf, extractDir); err != nil {
			return "", err
		}
	}
	newApp := filepath.Join(extractDir, "Gemini Dictation.app")
	if !fileExists(filepath.Join(newApp, "Contents", "MacOS")) {
		return "", errors.New("zip does not contain 'Gemini Dictation.app'")
	}

	appPath, err := bundlePath()
	if err != nil {
		return "", err
	}
	old := appPath + ".old"
	os.RemoveAll(old)
	if err := os.Rename(appPath, old); err != nil {
		return "", err
	}
	if err := os.Rename(newApp, appPath); err != nil {
		_ = os.Rename(old, appPath) // roll back
		return "", err
	}
	os.RemoveAll(old)
	os.Remove(tmpZip)
	os.RemoveAll(extractDir)
	return fmt.Sprintf("open -a %q", appPath), nil
}

func extractFile(zf *zip.File, dest string) error {
	target := filepath.Join(dest, zf.Name) // zip.Slip risk guarded below
	if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(dest)+string(filepath.Separator)) &&
		filepath.Clean(target) != filepath.Clean(dest) {
		return fmt.Errorf("zip entry escapes dir: %s", zf.Name)
	}
	if zf.FileInfo().IsDir() {
		return os.MkdirAll(target, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := zf.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, zf.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, rc)
	return err
}

// bundlePath resolves the running .app bundle root (binary → Contents/MacOS → …).
func bundlePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		real = exe
	}
	if i := strings.Index(real, ".app/"); i >= 0 {
		return real[:i+4], nil
	}
	return "", fmt.Errorf("not running from a .app bundle: %s", real)
}

func Relaunch(appPath string) error {
	// Retries: `open` racing the old process exit / singleton lock can leave
	// the new instance dead — observed live on the 1.0.0→1.0.1 update.
	var lastErr error
	for i := 0; i < 3; i++ {
		time.Sleep(time.Duration(1+i) * time.Second)
		if lastErr = exec.Command("open", "-a", appPath).Start(); lastErr == nil {
			return nil
		}
	}
	return lastErr
}
