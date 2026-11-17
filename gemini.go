package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const APIBase = "https://generativelanguage.googleapis.com/v1beta/models"

const DictationPrompt = "Transcribe this audio exactly as spoken. The speaker mixes " +
	"Vietnamese and English (code-switching) and uses technical/IT terms " +
	"(e.g. pull request, merge, ArgoCD, UAT, deploy). Keep English words in English, " +
	"Vietnamese in Vietnamese with correct diacritics. Add natural punctuation and " +
	"capitalization. Output ONLY the transcript text, nothing else."

type TranscribeResult struct {
	Text     string
	TokensIn  int
	TokensOut int
	Latency  time.Duration
	KeyIndex int // which api key served the request (-1 = single key)
}

// Transcribe rotates through cfg.APIKeys on 429 — more keys = more quota.
func Transcribe(wav []byte, cfg Config) (TranscribeResult, error) {
	keys := cfg.APIKeys
	if len(keys) == 0 && cfg.APIKey != "" {
		keys = []string{cfg.APIKey}
	}
	if len(keys) == 0 {
		return TranscribeResult{}, fmt.Errorf("no API key configured (gemini-dictation --set-key)")
	}
	start := cfg.KeyIndex
	if start < 0 || start >= len(keys) {
		start = 0
	}
	var lastErr error
	for i := 0; i < len(keys); i++ {
		idx := (start + i) % len(keys)
		res, err := transcribeOnce(wav, cfg, keys[idx])
		if err == nil {
			res.KeyIndex = idx
			if len(keys) > 1 && idx != start {
				logf("key #%d served the request (rotated from #%d)", idx+1, start+1)
			}
			return res, nil
		}
		if rl, ok := err.(*RateLimitError); ok {
			lastErr = rl // this key is throttled — try the next immediately
			continue
		}
		return res, err
	}
	return TranscribeResult{}, lastErr
}

func transcribeOnce(wav []byte, cfg Config, key string) (TranscribeResult, error) {
	res := TranscribeResult{}
	b64 := base64.StdEncoding.EncodeToString(wav)
	body := map[string]any{
		"contents": []any{map[string]any{
			"parts": []any{
				map[string]string{"text": DictationPrompt},
				map[string]any{
					"inline_data": map[string]string{
						"mime_type": "audio/wav",
						"data":      b64,
					},
				},
			},
		}},
	}
	if cfg.SystemPrompt != "" {
		body["systemInstruction"] = map[string]any{
			"parts": []any{map[string]string{"text": cfg.SystemPrompt}},
		}
	}
	// flash-lite rejects thinkingConfig with HTTP 400 INVALID_ARGUMENT —
	// only the thinking-capable models accept a zero budget
	if cfg.DisableThinking && !strings.Contains(cfg.Model, "lite") {
		body["generationConfig"] = map[string]any{
			"thinkingConfig": map[string]int{"thinkingBudget": 0},
		}
	}
	jbody, _ := json.Marshal(body)
	req, err := http.NewRequest("POST",
		fmt.Sprintf("%s/%s:generateContent?key=%s", APIBase, cfg.Model, key),
		bytes.NewReader(jbody))
	if err != nil {
		return res, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 60 * time.Second}
	t0 := time.Now()
	resp, err := client.Do(req)
	res.Latency = time.Since(t0)
	if err != nil {
		return res, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		msg := string(raw)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if resp.StatusCode == 429 {
			// the details section (cut from the old log) says WHICH quota
			// (per-minute vs per-day) and how long to wait — keep it
			var q struct {
				Error struct {
					Details []struct {
						Violations []struct {
							QuotaID string `json:"quotaId"`
						} `json:"violations"`
						RetryDelay string `json:"retryDelay"`
					} `json:"details"`
				} `json:"error"`
			}
			_ = json.Unmarshal(raw, &q)
			rl := &RateLimitError{Detail: msg}
			for _, d := range q.Error.Details {
				for _, v := range d.Violations {
					if v.QuotaID != "" {
						rl.QuotaID = v.QuotaID
					}
				}
				if d.RetryDelay != "" {
					rl.RetryAfter, _ = time.ParseDuration(d.RetryDelay)
				}
			}
			return res, rl
		}
		return res, fmt.Errorf("gemini HTTP %d: %s", resp.StatusCode, msg)
	}
	var d struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text               string `json:"text"`
					AudioTranscription *struct {
						Text string `json:"text"`
					} `json:"audioTranscription"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			PromptTokens     int `json:"promptTokenCount"`
			CandidatesTokens int `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return res, err
	}
	if len(d.Candidates) > 0 && len(d.Candidates[0].Content.Parts) > 0 {
		p := d.Candidates[0].Content.Parts[0]
		if p.Text != "" {
			res.Text = p.Text
		} else if p.AudioTranscription != nil {
			res.Text = p.AudioTranscription.Text
		}
	}
	res.TokensIn = d.UsageMetadata.PromptTokens
	res.TokensOut = d.UsageMetadata.CandidatesTokens
	return res, nil
}

type RateLimitError struct {
	Detail    string
	QuotaID   string        // PerMinute* = transient, PerDay* = wait for reset
	RetryAfter time.Duration // server-suggested wait
}

func (e *RateLimitError) Error() string {
	if e.QuotaID != "" {
		return "rate limit (429, " + e.QuotaID + "): " + e.Detail
	}
	return "rate limit (429): " + e.Detail
}

// geminiListModels returns model names accessible with the given key.
func geminiListModels(key string) ([]string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(APIBase + "?key=" + key + "&pageSize=100")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, string(raw))
	}
	var d struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(d.Models))
	for _, m := range d.Models {
		parts := strings.Split(m.Name, "/")
		out = append(out, parts[len(parts)-1])
	}
	return out, nil
}
