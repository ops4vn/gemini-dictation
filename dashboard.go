package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

//go:embed web/index.html
var dashboardFS embed.FS

// StartDashboard serves the quota page + a small JSON API + a quit endpoint
// (the app is headless besides the pill; the dashboard is the control surface).
func StartDashboard(tracker *UsageTracker, cfg *atomicConfig, port int, quit chan struct{}) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/index.html" {
			http.NotFound(w, r)
			return
		}
		b, err := dashboardFS.ReadFile("web/index.html")
		if err != nil {
			http.Error(w, "missing web/index.html", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})

	mux.HandleFunc("/api/usage", func(w http.ResponseWriter, r *http.Request) {
		c := cfg.Load()
		daily, recent := tracker.Snapshot()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"today":   time.Now().Format("2006-01-02"),
			"limits":  c.FreeTierLimits,
			"updated": time.Now().Format("15:04:05"),
			"daily":   daily,
			"recent":  recent,
		})
	})

	mux.HandleFunc("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		w.Write([]byte(`{"bye":true}`))
		select {
		case quit <- struct{}{}:
		default:
		}
	})

	// permission status for the onboarding checklist (macOS TCC services).
	mux.HandleFunc("/api/perm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(permStatus())
	})

	// mark onboarding done (hides the auto-opened checklist until reinstall)
	mux.HandleFunc("/api/onboarded", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		c := cfg.Load()
		c.Onboarded = true
		cfg.Store(c)
		if err := SaveConfig(c); err != nil {
			logf("save onboarding flag: %v", err)
		}
		w.Write([]byte(`{"ok":true}`))
	})

	// open a System Settings privacy pane: mic | input | accessibility
	mux.HandleFunc("/api/open", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		panes := map[string]string{
			"mic":          "x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone",
			"input":        "x-apple.systempreferences:com.apple.preference.security?Privacy_ListenEvent",
			"accessibility": "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
		}
		url := panes[r.URL.Query().Get("pane")]
		if url == "" {
			http.Error(w, "unknown pane", 400)
			return
		}
		openURL(url)
		w.Write([]byte(`{"ok":true}`))
	})

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})

	return http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", port), mux)
}

var lastIMPrompt, lastAXPrompt time.Time

// promptOnce fires the (blocking) permission request on a goroutine, at
// most once per minute per service.
func promptOnce(last *time.Time, missing bool, ask func() bool) {
	if !missing || time.Since(*last) < time.Minute {
		return
	}
	*last = time.Now()
	go ask()
}

// permStatus checks the three macOS permissions the app needs. The paste
// probe runs the same System Events call PasteAtCursor uses — with a short
// timeout so a pending prompt can't hang the dashboard.
func permStatus() map[string]any {
	st := map[string]any{"tap": tapPermission(), "mic": micState.Load(), "paste": axTrusted()}
	// permission prompts BLOCK until answered — run them async and at most
	// once a minute or the 5s dashboard poll stacks dialogs
	promptOnce(&lastIMPrompt, !st["tap"].(bool), tapRequest)
	promptOnce(&lastAXPrompt, !st["paste"].(bool), promptAccessibility)
	return st
}
