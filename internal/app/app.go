// Package app wires everything together: the Fyne application, the tray,
// the source manager, the config repository, and the two windows. The UI
// thread only ever calls non-blocking manager methods; every Apply runs
// either at startup (before the event loop) or on the Settings save
// goroutine.
package app

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/driver/desktop"

	"github.com/skobkin/llm-monitor/internal/config"
	"github.com/skobkin/llm-monitor/internal/credentials"
	"github.com/skobkin/llm-monitor/internal/domain"
	"github.com/skobkin/llm-monitor/internal/resources"
	"github.com/skobkin/llm-monitor/internal/source"
	"github.com/skobkin/llm-monitor/internal/source/amdgpuweb"
	"github.com/skobkin/llm-monitor/internal/source/llamaswap"
	"github.com/skobkin/llm-monitor/internal/source/minimax"
	"github.com/skobkin/llm-monitor/internal/source/zai"
	"github.com/skobkin/llm-monitor/internal/ui/monitor"
	"github.com/skobkin/llm-monitor/internal/ui/settings"
)

// appID is the single application identity, identical to FyneApp.toml
// (preferences path, tray, packaging all key off it).
const appID = "com.skobkin.llm-monitor"

// appMetadata mirrors FyneApp.toml for runs without packaged metadata
// (see New). Keep the values in sync with the toml; a test guards the
// duplication.
func appMetadata() fyne.AppMetadata {
	return fyne.AppMetadata{
		ID:         appID,
		Name:       "LLM Monitor",
		Version:    "0.1.0",
		Build:      1,
		Migrations: map[string]bool{"fyneDo": true},
	}
}

// App is the running application.
type App struct {
	fyneApp  fyne.App
	log      *slog.Logger
	client   *http.Client
	creds    credentials.Store
	repo     *config.Repository
	mgr      *source.Manager
	monitor  *monitor.Monitor
	settings *settings.Settings

	done    chan struct{}
	running atomic.Bool
	unsub   func()

	mu  sync.RWMutex // guards cfg and the adapters
	cfg config.Config

	amdAdapter  *amdgpuweb.Adapter
	swapAdapter *llamaswap.Adapter
	mmAdapter   *minimax.Adapter
	zaiAdapter  *zai.Adapter
}

// New creates the application (windows built, sources not yet started).
func New() (*App, error) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	fa := fyneapp.NewWithID(appID)
	// Packaged builds get their metadata injected by the fyne tool; every
	// other run (go run, an installed binary) finds no FyneApp.toml next
	// to the executable, so the fyne.Do migration flag and the version
	// would be missing and every start would log the not-migrated warning.
	fyneapp.SetMetadata(appMetadata())
	fa.SetIcon(resources.Icon())

	repo := config.NewRepository(fa.Preferences())
	cfg := repo.Load()

	creds := credentials.NewKeyringStore()
	client := &http.Client{}
	mgr := source.NewManager(logger)

	a := &App{
		fyneApp: fa,
		log:     logger,
		client:  client,
		creds:   creds,
		repo:    repo,
		mgr:     mgr,
		cfg:     cfg,
		done:    make(chan struct{}),
	}
	a.running.Store(true)

	// The MiniMax adapter is config-independent (fixed endpoint, key read
	// at refresh time): built once, never rebuilt.
	a.mmAdapter = minimax.New(creds, client, logger)

	a.monitor = monitor.NewMonitor(fa, a)
	a.settings = settings.NewSettings(fa, settings.Deps{
		LoadConfig: a.Config,
		OnSave:     a.onSaveConfig,
		Creds:      creds,
		TestAMDGPU: func(ctx context.Context, baseURL string) ([]domain.GPUInfo, error) {
			return amdgpuweb.New(baseURL, "", client, logger).Discover(ctx)
		},
		TestLlamaSwap: func(ctx context.Context, baseURL, override string) error {
			return llamaswap.New(baseURL, a.testerStore(override, credentials.AccountLlamaSwap), client, logger).Test(ctx)
		},
		TestMiniMax: func(ctx context.Context, override string) error {
			_, err := minimax.New(a.testerStore(override, credentials.AccountMiniMax), client, logger).Refresh(ctx)
			return err
		},
		TestZAI: func(ctx context.Context, baseURL, override string) error {
			_, err := zai.New(baseURL, a.testerStore(override, credentials.AccountZAI), client, logger).Refresh(ctx)
			return err
		},
		Done: a.done,
	})

	a.setupTray()
	a.unsub = a.mgr.Subscribe(func(domain.SourceID) {
		if !a.running.Load() {
			return
		}
		fyne.Do(a.monitor.OnStatesChanged)
	})
	a.monitor.StartTicker(a.done)
	return a, nil
}

// Run starts the configured sources and the event loop, then joins the
// source goroutines after the loop returns.
func (a *App) Run() {
	a.log.Info("starting", "app", appID, "version", a.fyneApp.Metadata().Version)

	// Keychain state changes are Info-worthy (they gate credential
	// features); the probe never touches a real account.
	if st := a.creds.Status(context.Background()); st.State != credentials.StateAvailable {
		a.log.Warn("keychain not available", "state", string(st.State), "backend", st.Backend, "message", st.Message)
	}

	// Apply before the event loop exists: no contention with the UI.
	a.applyConfig(a.Config())
	a.fyneApp.Run()
	a.mgr.Wait()
	a.log.Info("stopped")
}

// setupTray installs the tray icon and menu. Without a desktop driver
// (off-desktop run) the monitor window shows at startup instead.
func (a *App) setupTray() {
	desk, ok := a.fyneApp.(desktop.App)
	if !ok {
		a.monitor.Show()
		return
	}
	desk.SetSystemTrayWindow(a.monitor.Window())
	desk.SetSystemTrayMenu(fyne.NewMenu("LLM Monitor",
		fyne.NewMenuItem("Show Monitor", a.monitor.Show),
		fyne.NewMenuItem("Refresh", a.mgr.RefreshAll),
		fyne.NewMenuItem("Settings", a.settings.Show),
		fyne.NewMenuItemSeparator(),
		fyne.NewMenuItem("Quit", a.quit),
	))
}

// Config implements monitor.MonitorController.
func (a *App) Config() config.Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

// States implements monitor.MonitorController.
func (a *App) States() map[domain.SourceID]domain.Runtime { return a.mgr.States() }

// RefreshAll implements monitor.MonitorController.
func (a *App) RefreshAll() { a.mgr.RefreshAll() }

// OpenSettings implements monitor.MonitorController.
func (a *App) OpenSettings() { a.settings.Show() }

// onSaveConfig persists the config and reconfigures the manager. It runs
// on the Settings save goroutine, never on the UI thread (Apply may wait
// out a request timeout).
func (a *App) onSaveConfig(cfg config.Config) error {
	a.repo.Save(cfg)
	a.applyConfig(cfg)
	return nil
}

// applyConfig diffs the new config against the current one and hands the
// manager a registration set. Adapters are rebuilt only when their
// sub-config changed (the manager also diffs by pointer identity).
func (a *App) applyConfig(cfg config.Config) {
	a.mu.Lock()
	defer a.mu.Unlock()

	regs := map[domain.SourceID]source.Registration{}
	if cfg.AMDGPUWeb.Enabled {
		if a.amdAdapter == nil || a.cfg.AMDGPUWeb != cfg.AMDGPUWeb {
			a.amdAdapter = amdgpuweb.New(cfg.AMDGPUWeb.BaseURL, cfg.AMDGPUWeb.SelectedGPUID, a.client, a.log)
		}
		regs[domain.SourceAMDGPUWeb] = source.Registration{Source: a.amdAdapter, Enabled: true, Interval: cfg.AMDGPUWeb.PollInterval}
	}
	if cfg.LlamaSwap.Enabled {
		if a.swapAdapter == nil || a.cfg.LlamaSwap != cfg.LlamaSwap {
			a.swapAdapter = llamaswap.New(cfg.LlamaSwap.BaseURL, a.creds, a.client, a.log)
		}
		regs[domain.SourceLlamaSwap] = source.Registration{Source: a.swapAdapter, Enabled: true, Interval: cfg.LlamaSwap.PollInterval}
	}
	if cfg.MiniMax.Enabled {
		regs[domain.SourceMiniMax] = source.Registration{Source: a.mmAdapter, Enabled: true, Interval: cfg.MiniMax.PollInterval}
	}
	if cfg.ZAI.Enabled {
		if a.zaiAdapter == nil || a.cfg.ZAI != cfg.ZAI {
			a.zaiAdapter = zai.New(cfg.ZAI.BaseURL, a.creds, a.client, a.log)
		}
		regs[domain.SourceZAI] = source.Registration{Source: a.zaiAdapter, Enabled: true, Interval: cfg.ZAI.PollInterval}
	}

	a.mgr.Apply(regs)
	a.cfg = cfg
}

// quit is the single shutdown path (tray Quit). It is idempotent: the
// CAS gate makes re-entry a no-op. Ordering matters: unsubscribe manager
// notifications, stop timers, cancel source work, then quit the loop;
// Run joins the source goroutines after the loop returns.
func (a *App) quit() {
	if !a.running.CompareAndSwap(true, false) {
		return
	}
	a.log.Info("quitting")
	a.unsub()
	close(a.done)
	a.mgr.Cancel()
	a.fyneApp.Quit()
}

// testerStore returns a credential store that answers Get for one account
// with the typed override (when non-empty) and delegates everything else
// to the real keychain, so Settings tests reuse the real adapters.
func (a *App) testerStore(override string, account credentials.AccountID) credentials.Store {
	return &overrideStore{inner: a.creds, account: account, override: override}
}

// overrideStore layers a typed-in credential over a backing store.
type overrideStore struct {
	inner    credentials.Store
	account  credentials.AccountID
	override string
}

func (o *overrideStore) Status(ctx context.Context) credentials.Status { return o.inner.Status(ctx) }

func (o *overrideStore) Get(ctx context.Context, id credentials.AccountID) (string, error) {
	if id == o.account && o.override != "" {
		return o.override, nil
	}
	return o.inner.Get(ctx, id)
}

func (o *overrideStore) Set(ctx context.Context, id credentials.AccountID, secret string) error {
	return o.inner.Set(ctx, id, secret)
}

func (o *overrideStore) Delete(ctx context.Context, id credentials.AccountID) error {
	return o.inner.Delete(ctx, id)
}
