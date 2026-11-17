package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Config mirrors config.json. Defaults applied over the file (see LoadConfig).
type Config struct {
	APIKey           string                       `json:"api_key"` // legacy single key (mirrors APIKeys[0])
	APIKeys          []string                     `json:"api_keys"`
	KeyIndex         int                          `json:"key_index"`
	Model            string                       `json:"model"`
	DisableThinking  bool                         `json:"disable_thinking"`
	Hotkey           string                       `json:"hotkey"`
	HoldSeconds      float64                      `json:"hold_seconds"`
	SampleRate       int                          `json:"sample_rate"`
	MaxSeconds       float64                      `json:"max_seconds"`
	MinSeconds       float64                      `json:"min_seconds"`
	SilenceRMS       float64                      `json:"silence_rms"`
	Sounds           bool                         `json:"sounds"`
	ProbeMicOnStart  bool                         `json:"probe_mic_on_start"`
	TrailingSpace    bool                         `json:"trailing_space"`
	RestoreClipboard bool                         `json:"restore_clipboard"`
	WebPort          int                          `json:"web_port"`
	FreeTierLimits   map[string]map[string]float64 `json:"free_tier_limits"`
	Update           UpdateConfig                 `json:"update"`
	DebugKeys        bool                         `json:"debug_keys"`
	Onboarded        bool                         `json:"onboarded"`
	SystemPrompt     string                       `json:"system_prompt"`
}

type UpdateConfig struct {
	URL              string `json:"url"`  // empty = updater disabled
	CheckMinutes     int    `json:"check_minutes"`
	AutoApply        bool   `json:"auto_apply"`
}

func Defaults() Config {
	return Config{
		Model:           "gemini-3.5-flash",
		DisableThinking: true,
		Hotkey:          "<ctrl>+<shift>+<space>",
		HoldSeconds:     1.5,
		SampleRate:      16000,
		MaxSeconds:      120,
		MinSeconds:      0.35,
		SilenceRMS:      25,
		Sounds:          true,
		ProbeMicOnStart: true,
		SystemPrompt: "You are a transcription engine. Transcribe the audio VERBATIM and output ONLY the transcript. The speaker often code-switches between Vietnamese and English: keep English technical terms exactly as spoken (e.g. pull request, merge, deploy, Kubernetes, ArgoCD), write Vietnamese with full diacritics, and preserve numbers, file paths and code identifiers literally. Never add, complete or translate anything.",
		TrailingSpace:   true,
		RestoreClipboard: true,
		WebPort:         7788,
		FreeTierLimits: map[string]map[string]float64{
			"gemini-3.5-flash":      {"rpm": 10, "rpd": 250},
			"gemini-3.5-flash-lite": {"rpm": 15, "rpd": 1000},
		},
		Update: UpdateConfig{CheckMinutes: 60},
	}
}

func ConfigPath() string {
	exe, err := os.Executable()
	if err == nil {
		// config lives next to the binary inside the .app bundle's dir? No —
		// keep it user-editable: ~/.gemini-dictation/config.json
		_ = exe
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".gemini-dictation", "config.json")
}

func LoadConfig() (Config, error) {
	cfg := Defaults()
	p := ConfigPath()
	raw, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		_ = os.WriteFile(p, marshalPretty(cfg), 0o600)
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Defaults(), fmt.Errorf("config %s: %w", p, err)
	}
	if len(cfg.APIKeys) == 0 && cfg.APIKey != "" {
		cfg.APIKeys = []string{cfg.APIKey} // migrate legacy single-key config
	}
	if cfg.APIKey == "" {
		return cfg, errors.New("config: api_key is empty — run with --set-key")
	}
	return cfg, nil
}

func SaveConfig(cfg Config) error {
	_ = os.MkdirAll(filepath.Dir(ConfigPath()), 0o700)
	return os.WriteFile(ConfigPath(), marshalPretty(cfg), 0o600)
}

func marshalPretty(v any) []byte {
	b, _ := json.MarshalIndent(v, "", "  ")
	return b
}
