package main

import (
	"context"
	"embed"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

// lockPath lives in the per-user temp dir (TMPDIR on darwin, %TEMP% on windows).
var lockPath = filepath.Join(os.TempDir(), "gemini_dictation.lock")

var logPathNote string

func acquireSingleton() *os.File {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Println("singleton lock open failed:", err)
		return nil
	}
	if !tryLockFile(f.Fd()) {
		f.Close()
		log.Println("another instance is already running — exiting")
		os.Exit(0)
	}
	return f
}

func main() {
	log.SetFlags(log.Ltime)
	// file log (launched via `open`/Explorer there is no stdout capture)
	if cache, err := os.UserCacheDir(); err == nil {
		dir := filepath.Join(cache, "gemini-dictation")
		_ = os.MkdirAll(dir, 0o755)
		if f, ferr := os.OpenFile(filepath.Join(dir, "app.log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); ferr == nil {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
			logPathNote = filepath.Join(dir, "app.log")
		}
	}

	// CLI dispatch (config tooling runs headless, no wails)
	if len(os.Args) > 1 && RunCLI(os.Args[1:]) {
		return
	}

	lock := acquireSingleton()
	defer func() {
		if lock != nil {
			unlockFile(lock.Fd())
			lock.Close()
		}
	}()

	cfg, err := LoadConfig()
	if err != nil {
		log.Println("config:", err)
		cfg = Defaults()
	}
	configDir := filepath.Dir(ConfigPath())

	tracker := NewUsageTracker(configDir)
	quit := make(chan struct{})
	app := NewApp(tracker, quit)
	app.SetCfg(cfg)

	err = wails.Run(&options.App{
		Title:     pillTitle,
		Width:     380,
		Height:    96,
		Frameless: true,
		BackgroundColour: &options.RGBA{
			R: 20, G: 22, B: 28, A: 0, // transparent — pill paints its own dark bg
		},
		StartHidden:       true,
		HideWindowOnClose: false,
		DisableResize:     true,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: func(ctx context.Context) {
			pillCtx = ctx
			app.startupServices(cfg, quit)
		},
		OnBeforeClose: func(ctx context.Context) bool {
			return false // close only via Quit actions
		},
		Bind: []interface{}{app},
		Mac: &mac.Options{
			WindowIsTranslucent:  true,
			WebviewIsTransparent: true,
		},
	})
	if err != nil {
		log.Println("wails:", err)
	}
}

// startupServices wires everything that needs the wails context.
func (a *App) startupServices(cfg Config, quit chan struct{}) {
	a.SetCfg(cfg)
	logf("gemini-dictation %s — hold ⌥ ≥ %.1fs → speak → release → paste", AppVersion, cfg.HoldSeconds)

	if cfg.ProbeMicOnStart {
		go micProbe()
	}

	// event tap (platform: macOS CGEventTap / Windows LL keyboard hook)
	ok, err := startTap(func(down bool) { a.OnTap(down) })
	if err != nil || !ok {
		log.Println("⚠️ key listener NOT installed — grant Input Monitoring (macOS) / check hook (Windows), then restart")
	} else {
		log.Println("key listener active — hold the trigger key to dictate")
	}

	// click-through for the pill window (after wails window exists)
	time.AfterFunc(2*time.Second, func() {
		setClickThrough(pillTitle, true)
	})

	// dashboard (control surface + quota)
	go func() {
		if derr := StartDashboard(a.tracker, &a.cfg, cfg.WebPort, quit); derr != nil {
			logf("dashboard: port %d unavailable — %v", cfg.WebPort, derr)
		}
	}()

	// dashboard quit → app exit
	go func() {
		<-quit
		logf("shutting down")
		if pillCtx != nil {
			runtime.Quit(pillCtx)
		}
		time.Sleep(300 * time.Millisecond)
		os.Exit(0)
	}()

	// updater
	if cfg.Update.URL != "" {
		go updaterLoop(cfg)
	}
}

func updaterLoop(cfg Config) {
	minutes := cfg.Update.CheckMinutes
	if minutes < 5 {
		minutes = 5
	}
	check := func() {
		m, err := CheckUpdate(cfg)
		if err != nil {
			return
		}
		if VersionNewer(m.Version, AppVersion) {
			logf("update available: %s → %s", AppVersion, m.Version)
			if cfg.Update.AutoApply {
				if cmd, err := ApplyUpdate(m, cfg); err == nil {
					logf("updated to %s — relaunching", m.Version)
					_ = Relaunch(cmd)
					os.Exit(0)
				} else {
					logf("update failed: %v", err)
				}
			}
		}
	}
	check()
	t := time.NewTicker(time.Duration(minutes) * time.Minute)
	for range t.C {
		check()
	}
}
