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
	"strings"
	"time"
)

// Updater: polls a manifest published to Cloudflare R2 (or any static host):
//   { "version": "1.1.0", "sha256": "<zip hash>", "url": "https://…/gemini-dictation-1.1.0-macos.zip" }
// On a newer version: download → verify sha256 → swap the .app bundle → relaunch.
// Disabled when update.url is empty.

type Manifest struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	URL     string `json:"url"`
	Notes   string `json:"notes,omitempty"`
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
	if m.Version == "" || m.URL == "" {
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
	tmpZip := "/tmp/gemini-dictation-update.zip"
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(m.URL)
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
	if got := hex.EncodeToString(h.Sum(nil)); got != m.SHA256 {
		os.Remove(tmpZip)
		return "", fmt.Errorf("sha256 mismatch: got %s want %s", got, m.SHA256)
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
	time.Sleep(300 * time.Millisecond)
	return exec.Command("open", "-a", appPath).Start()
}
