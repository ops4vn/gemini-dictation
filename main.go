package main

import (
	"context"
	"embed"
	"io"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ops4vn/gemini-dictation/native"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

const lockPath = "/tmp/gemini_dictation.lock"

func acquireSingleton() *os.File {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Println("singleton lock open failed:", err)
		return nil
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		log.Println("another instance is already running — exiting")
		os.Exit(0)
	}
	return f
}

func main() {
	log.SetFlags(log.Ltime)
	// file log (launched via `open` there is no stdout/stderr capture)
	if home, err := os.UserHomeDir(); err == nil {
		if f, ferr := os.OpenFile(filepath.Join(home, "Library/Logs/gemini-dictation-go.log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); ferr == nil {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
		}
	}

	// CLI dispatch (config tooling runs headless, no wails)
	if len(os.Args) > 1 && RunCLI(os.Args[1:]) {
		return
	}

	lock := acquireSingleton()
	defer func() {
		if lock != nil {
			syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
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

	// event tap
	ch, ok, err := native.StartTap()
	if err != nil || !ok {
		log.Println("⚠️ event tap NOT created — grant Input Monitoring to the app, then restart")
	} else {
		go func() {
			for ev := range ch {
				a.OnTap(ev.HasOption)
			}
		}()
		log.Println("event tap active — hold ⌥ to dictate")
	}

	// click-through for the pill window (after wails window exists)
	time.AfterFunc(2*time.Second, func() {
		nativeSetClickThrough(pillTitle, true)
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
