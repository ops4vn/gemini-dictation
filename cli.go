package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"
)

// RunCLI handles every non-GUI invocation. Returns true when the argument
// was consumed (caller exits instead of starting the GUI).
func RunCLI(args []string) bool {
	if len(args) == 0 {
		return false
	}
	cfg, _ := LoadConfig()
	switch args[0] {
	case "--version", "-v":
		fmt.Println("gemini-dictation", AppVersion)

	case "--check":
		runCheck(cfg)

	case "--transcribe":
		if len(args) < 2 {
			fmt.Println("usage: gemini-dictation --transcribe file.wav")
			return true
		}
		wav, err := os.ReadFile(args[1])
		if err != nil {
			fmt.Println("read failed:", err)
			return true
		}
		res, err := Transcribe(wav, cfg)
		if err != nil {
			fmt.Println("transcribe failed:", err)
			return true
		}
		fmt.Println(res.Text)
		fmt.Printf("(%.1fs via %s)\n", res.Latency.Seconds(), cfg.Model)

	case "--set-key":
		if len(args) < 2 {
			fmt.Println("usage: gemini-dictation --set-key YOUR_KEY")
			return true
		}
		if _, err := apiListModels(args[1]); err != nil {
			fmt.Println("❌ key không dùng được:", err)
			os.Exit(1)
		}
		cfg.APIKey = args[1]
		cfg.APIKeys = []string{args[1]}
		cfg.KeyIndex = 0
		if err := SaveConfig(cfg); err != nil {
			fmt.Println("save failed:", err)
			os.Exit(1)
		}
		fmt.Println("✅ đã lưu key")

	case "--set-model":
		if len(args) < 2 {
			fmt.Println("usage: gemini-dictation --set-model gemini-3.5-flash")
			return true
		}
		models, listErr := apiListModels(cfg.APIKey)
		if listErr != nil {
			fmt.Println("❌ không lấy được danh sách model:", listErr)
			return true
		}
		valid := map[string]bool{}
		for _, m := range audioModels(models) {
			valid[m] = true
		}
		if !valid[args[1]] {
			fmt.Printf("❌ '%s' không có trong danh sách model của key này.\n", args[1])
			return true
		}
		cfg.Model = args[1]
		if err := SaveConfig(cfg); err != nil {
			fmt.Println("save failed:", err)
			os.Exit(1)
		}
		fmt.Println("✅ đã lưu model:", args[1])

	case "--config":
		cliConfig(cfg)

	case "--add-key":
		if len(args) < 2 {
			fmt.Println("usage: gemini-dictation --add-key YOUR_KEY")
			return true
		}
		if _, err := apiListModels(args[1]); err != nil {
			fmt.Println("❌ key không dùng được:", err)
			os.Exit(1)
		}
		for _, k := range cfg.APIKeys {
			if k == args[1] {
				fmt.Println("key đã có trong danh sách")
				return true
			}
		}
		cfg.APIKeys = append(cfg.APIKeys, args[1])
		cfg.APIKey = cfg.APIKeys[0]
		if err := SaveConfig(cfg); err != nil {
			fmt.Println("save failed:", err)
			os.Exit(1)
		}
		fmt.Printf("✅ đã thêm key (%d key trong danh sách — tự rotate khi 429)\n", len(cfg.APIKeys))

	case "--list-keys":
		if len(cfg.APIKeys) == 0 {
			fmt.Println("chưa có key nào (--set-key)")
			return true
		}
		for i, k := range cfg.APIKeys {
			mark := "  "
			if i == cfg.KeyIndex {
				mark = "→ "
			}
			fmt.Printf("%s#%d %s...%s\n", mark, i+1, k[:10], k[len(k)-4:])
		}
		fmt.Println("→ = key đang dùng; 429 sẽ tự nhảy key kế tiếp")

	default:
		fmt.Printf("unknown flag %q — try --help, --config, --set-key, --set-model, --check, --transcribe\n", args[0])
	}
	return true
}

func apiListModels(key string) ([]string, error) {
	return geminiListModels(key)
}

func audioModels(models []string) []string {
	exclude := []string{"tts", "image", "banana", "lyria", "video", "native-audio", "embed", "clip", "grounding"}
	notes := map[string]string{
		"gemini-3.5-flash":       "KHUYẾN NGHỊ — accuracy tốt trong nhóm nhanh",
		"gemini-3.5-flash-lite":  "nhanh/nhẹ nhất, accuracy kém hơn với giọng thật",
		"gemini-3.5-transcribe":  "STT chuyên dụng, hay sai từ kỹ thuật khi code-switch",
		"gemini-3.6-flash":       "phụ — hơi kém hơn 3.5-flash",
	}
	var known, rest []string
	for _, m := range models {
		skip := false
		for _, e := range exclude {
			if strings.Contains(strings.ToLower(m), e) {
				skip = true
				break
			}
		}
		if skip || !(strings.HasPrefix(m, "gemini") || strings.HasPrefix(m, "gemma")) {
			continue
		}
		if _, ok := notes[m]; ok {
			known = append(known, m)
		} else {
			rest = append(rest, m)
		}
	}
	return append(known, rest...)
}

func runCheck(cfg Config) {
	fmt.Printf("1) config: model=%s hold=%.1fs hotkey=%s port=%d\n",
		cfg.Model, cfg.HoldSeconds, cfg.Hotkey, cfg.WebPort)
	models, err := apiListModels(cfg.APIKey)
	if err != nil {
		fmt.Println("2) API key: FAILED —", err)
	} else {
		found := false
		for _, m := range models {
			if m == cfg.Model {
				found = true
			}
		}
		if found {
			fmt.Println("2) API key: OK — model available")
		} else {
			fmt.Printf("2) API key: OK — model %q NOT accessible!\n", cfg.Model)
		}
	}
	if !micStart() {
		fmt.Println("3) microphone: FAILED to open")
		return
	}
	time.Sleep(700 * time.Millisecond)
	dur := micDuration()
	micStop()
	if dur > 0.4 {
		fmt.Printf("3) microphone: OK (%.2fs captured)\n", dur)
	} else {
		fmt.Println("3) microphone: SILENT or denied")
	}
	fmt.Println("4) event tap: needs Input Monitoring granted to the app")
}

func cliConfig(cfg Config) {
	r := bufio.NewReader(os.Stdin)
	for {
		fmt.Println("\n=== Gemini Dictation — cấu hình ===")
		key := cfg.APIKey
		if len(key) > 14 {
			key = key[:8] + "…" + key[len(key)-4:]
		}
		fmt.Printf("  API key : %s\n", key)
		fmt.Printf("  Model   : %s (thinking off=%v)\n", cfg.Model, cfg.DisableThinking)
		fmt.Printf("  Hold    : %.1fs | Dashboard: 127.0.0.1:%d\n", cfg.HoldSeconds, cfg.WebPort)
		fmt.Println("  1) Đổi API key")
		fmt.Println("  2) Chọn model")
		fmt.Println("  3) Thoát")
		fmt.Print("Chọn: ")
		c, _ := r.ReadString('\n')
		switch strings.TrimSpace(c) {
		case "1":
			fmt.Print("API key mới: ")
			k, _ := r.ReadString('\n')
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			if _, err := apiListModels(k); err != nil {
				fmt.Println("❌ key không dùng được:", err)
				continue
			}
			cfg.APIKey = k
			SaveConfig(cfg)
			fmt.Println("✅ đã lưu — restart app để áp dụng")
		case "2":
			models, err := apiListModels(cfg.APIKey)
			if err != nil {
				fmt.Println("❌", err)
				continue
			}
			opts := audioModels(models)
			fmt.Printf("\nModel hiện tại: %s\n", cfg.Model)
			for i, m := range opts {
				note := ""
				if n, ok := modelNote(m); ok {
					note = "  — " + n
				}
				cur := ""
				if m == cfg.Model {
					cur = "  ← đang dùng"
				}
				fmt.Printf("  %2d. %s%s%s\n", i+1, m, note, cur)
			}
			fmt.Print("\nChọn số (Enter = giữ nguyên): ")
			p, _ := r.ReadString('\n')
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			idx := 0
			fmt.Sscanf(p, "%d", &idx)
			if idx >= 1 && idx <= len(opts) {
				cfg.Model = opts[idx-1]
			} else {
				cfg.Model = p
			}
			SaveConfig(cfg)
			fmt.Println("✅ đã lưu model:", cfg.Model, "— restart app để áp dụng")
		case "3":
			return
		}
	}
}

func modelNote(m string) (string, bool) {
	notes := map[string]string{
		"gemini-3.5-flash":      "KHUYẾN NGHỊ — accuracy tốt trong nhóm nhanh",
		"gemini-3.5-flash-lite": "nhanh/nhẹ nhất, accuracy kém hơn với giọng thật",
		"gemini-3.5-transcribe": "STT chuyên dụng, hay sai từ kỹ thuật khi code-switch",
		"gemini-3.6-flash":      "phụ — hơi kém hơn 3.5-flash",
	}
	n, ok := notes[m]
	return n, ok
}
