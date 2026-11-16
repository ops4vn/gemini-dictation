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

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})

	return http.ListenAndServe(fmt.Sprintf("127.0.0.1:%d", port), mux)
}
