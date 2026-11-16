package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// UsageTracker mirrors the Python version: per-day/per-model counters + a
// tail of recent requests, persisted next to the config.
type UsageTracker struct {
	mu       sync.Mutex
	daily    map[string]map[string]*UsageDay
	recent   []UsageEntry
	dir      string
}

type UsageDay struct {
	Requests int     `json:"requests"`
	Errors   int     `json:"errors"`
	E429     int     `json:"e429"`
	AudioS   float64 `json:"audio_s"`
	TokensIn int     `json:"tin"`
	TokensOut int    `json:"tout"`
}

type UsageEntry struct {
	Date    string  `json:"date"`
	TS      string  `json:"ts"`
	Model   string  `json:"model"`
	OK      bool    `json:"ok"`
	Error   string  `json:"error,omitempty"`
	AudioS  float64 `json:"audio_s"`
	TokensIn int    `json:"tin"`
	TokensOut int   `json:"tout"`
	Latency float64 `json:"latency"`
}

func NewUsageTracker(dir string) *UsageTracker {
	t := &UsageTracker{daily: map[string]map[string]*UsageDay{}, dir: dir}
	b, err := os.ReadFile(filepath.Join(dir, "usage.json"))
	if err == nil {
		var stored struct {
			Daily map[string]map[string]*UsageDay `json:"daily"`
		}
		if json.Unmarshal(b, &stored) == nil && stored.Daily != nil {
			t.daily = stored.Daily
		}
	}
	return t
}

func (t *UsageTracker) Record(model string, ok bool, errKind string, audioS float64, tin, tout int, latencyS float64) {
	date := time.Now().Format("2006-01-02")
	ts := time.Now().Format("15:04:05")
	t.mu.Lock()
	defer t.mu.Unlock()
	day := t.daily[date]
	if day == nil {
		day = map[string]*UsageDay{}
		t.daily[date] = day
	}
	u := day[model]
	if u == nil {
		u = &UsageDay{}
		day[model] = u
	}
	u.Requests++
	if !ok {
		u.Errors++
		if errKind == "429" {
			u.E429++
		}
	}
	u.AudioS = round1(u.AudioS + audioS)
	u.TokensIn += tin
	u.TokensOut += tout

	entry := UsageEntry{date, ts, model, ok, errKind, round2(audioS), tin, tout, round2(latencyS)}
	t.recent = append(t.recent, entry)
	if len(t.recent) > 50 {
		t.recent = t.recent[len(t.recent)-50:]
	}
	_ = os.WriteFile(filepath.Join(t.dir, "usage.json"), mustJSON(map[string]any{"daily": t.daily}), 0o600)
	f, err := os.OpenFile(filepath.Join(t.dir, "usage_log.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		f.Write(mustJSON(entry))
		f.WriteString("\n")
		f.Close()
	}
}

func (t *UsageTracker) Snapshot() (map[string]map[string]*UsageDay, []UsageEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.daily, append([]UsageEntry(nil), t.recent...)
}

func round1(v float64) float64 { return float64(int(v*10+0.5)) / 10 }
func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
