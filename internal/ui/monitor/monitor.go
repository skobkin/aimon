package monitor

import (
	"net/url"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/resources"
	"git.skobk.in/skobkin/aimonitor/internal/ui"
	"git.skobk.in/skobkin/aimonitor/internal/ui/components"
)

// MonitorController is everything the monitor window needs from the app.
// All methods are safe to call on the UI thread.
type MonitorController interface {
	States() map[domain.SourceID]domain.Runtime
	Config() config.Config
	RefreshAll()
	OpenSettings()
}

// monitorSize is the splash window size (UI_UX ~500–560 wide; content
// scrolls, so the height is a comfortable default, not a limit).
var monitorSize = fyne.NewSize(540, 660)

// Monitor is the borderless tray monitor window: chrome, the three tabs,
// and a 1-second ticker re-rendering relative freshness labels.
type Monitor struct {
	ctrl MonitorController
	win  fyne.Window

	overviewBox *fyne.Container
	localBox    *fyne.Container
	remoteBox   *fyne.Container

	refreshBtn *widget.Button

	mu      sync.Mutex
	visible bool
}

// NewMonitor builds the monitor window (hidden; tray-first startup shows
// it on demand).
func NewMonitor(a fyne.App, ctrl MonitorController) *Monitor {
	m := &Monitor{ctrl: ctrl}

	var win fyne.Window
	if d, ok := a.Driver().(desktop.Driver); ok {
		win = d.CreateSplashWindow()
	} else {
		// No desktop driver (off-desktop run): a plain window still works.
		win = a.NewWindow("AI Monitor")
	}
	// Splash windows are created with an empty title (Fyne would show
	// "Fyne Application"); give the window its name and icon explicitly.
	win.SetTitle("AI Monitor")
	win.SetIcon(resources.Icon())
	m.win = win

	m.overviewBox = container.NewVBox()
	m.localBox = container.NewVBox()
	m.remoteBox = container.NewVBox()

	m.refreshBtn = widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), m.onRefresh)
	settingsBtn := widget.NewButtonWithIcon("", theme.SettingsIcon(), ctrl.OpenSettings)

	// Chrome keeps only the actions; the window title (and the tray
	// tooltip) already name the app, so a label would repeat it.
	chrome := container.NewHBox(
		layout.NewSpacer(),
		m.refreshBtn,
		settingsBtn,
	)

	tabs := container.NewAppTabs(
		container.NewTabItem("Overview", container.NewVScroll(m.overviewBox)),
		container.NewTabItem("Local", container.NewVScroll(m.localBox)),
		container.NewTabItem("Remote", container.NewVScroll(m.remoteBox)),
	)
	tabs.SelectIndex(0)

	win.SetContent(container.NewBorder(chrome, nil, nil, nil, tabs))
	win.Resize(monitorSize)

	// Closing hides to tray; the app keeps running.
	win.SetCloseIntercept(func() { m.hide() })
	// Esc hides the window (public shortcut API only).
	win.Canvas().AddShortcut(
		&desktop.CustomShortcut{KeyName: fyne.KeyEscape},
		func(fyne.Shortcut) { m.hide() },
	)
	return m
}

// Window returns the underlying window (the tray icon needs it).
func (m *Monitor) Window() fyne.Window { return m.win }

// Show reveals the window and re-renders immediately.
func (m *Monitor) Show() {
	m.setVisible(true)
	m.rebuild()
	m.win.Show()
	m.win.RequestFocus()
}

func (m *Monitor) hide() {
	m.setVisible(false)
	m.win.Hide()
}

func (m *Monitor) setVisible(v bool) {
	m.mu.Lock()
	m.visible = v
	m.mu.Unlock()
}

func (m *Monitor) isVisible() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.visible
}

// OnStatesChanged re-renders every tab. It must run on the UI thread;
// the app subscriber wraps the call in fyne.Do.
func (m *Monitor) OnStatesChanged() { m.rebuild() }

// StartTicker re-renders relative ages ("5s ago") once a second while the
// window is visible. It exits when done closes.
func (m *Monitor) StartTicker(done <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if !m.isVisible() {
					continue
				}
				fyne.Do(m.rebuild)
			}
		}
	}()
}

// onRefresh triggers a manual refresh of every enabled source and gives
// lightweight feedback by disabling the button briefly; existing data is
// never blanked.
func (m *Monitor) onRefresh() {
	m.ctrl.RefreshAll()
	m.refreshBtn.Disable()
	time.AfterFunc(time.Second, func() {
		fyne.Do(m.refreshBtn.Enable)
	})
}

// rebuild regenerates all three tab bodies from the current runtimes.
func (m *Monitor) rebuild() {
	states := m.ctrl.States()
	now := time.Now()
	cfg := m.ctrl.Config()

	m.rebuildOverview(ui.BuildOverview(states, now), cfg)
	replaceObjects(m.localBox, m.localTabObjects(ui.BuildLocalTab(states, now), cfg)...)
	replaceObjects(m.remoteBox, m.remoteTabObjects(ui.BuildRemoteTab(states, now), cfg)...)
}

func (m *Monitor) rebuildOverview(vm ui.OverviewVM, cfg config.Config) {
	var objs []fyne.CanvasObject
	if vm.NothingEnabled {
		objs = append(objs, components.NewEmptyState("No sources enabled.\nConfigure sources in Settings.", m.ctrl.OpenSettings))
		replaceObjects(m.overviewBox, objs...)
		return
	}

	if len(vm.LocalCards) > 0 {
		objs = append(objs, components.NewSectionLabel("Local"))
		for _, c := range vm.LocalCards {
			objs = append(objs, components.NewSourceCard(c, cardBody(c, cfg)))
		}
	}
	if len(vm.RemoteCards) > 0 {
		objs = append(objs, components.NewSectionLabel("Remote"))
		for _, c := range vm.RemoteCards {
			objs = append(objs, components.NewSourceCard(c, cardBody(c, cfg)))
		}
	}
	replaceObjects(m.overviewBox, objs...)
}

func (m *Monitor) localTabObjects(cards []ui.CardVM, cfg config.Config) []fyne.CanvasObject {
	if len(cards) == 0 {
		return []fyne.CanvasObject{components.NewEmptyState("No local sources enabled.", m.ctrl.OpenSettings)}
	}
	objs := make([]fyne.CanvasObject, 0, len(cards))
	for _, c := range cards {
		body := cardBody(c, cfg)
		if link, ok := ui.SourceWebLink(c.ID, cfg); ok {
			body = withWebUILink(body, "Open "+link.Label, link.URL)
		}
		objs = append(objs, components.NewSourceCard(c, body))
	}
	return objs
}

func (m *Monitor) remoteTabObjects(cards []ui.CardVM, cfg config.Config) []fyne.CanvasObject {
	if len(cards) == 0 {
		return []fyne.CanvasObject{components.NewEmptyState("No remote sources enabled.", m.ctrl.OpenSettings)}
	}
	objs := make([]fyne.CanvasObject, 0, len(cards))
	for _, c := range cards {
		body := cardBody(c, cfg)
		if link, ok := ui.SourceWebLink(c.ID, cfg); ok {
			body = withWebUILink(body, "Open "+link.Label, link.URL)
		}
		objs = append(objs, components.NewSourceCard(c, body))
	}
	return objs
}

// cardBody renders the card's data body from its view model. Which rows
// appear depends on the VM the builder produced (overview vs detail).
func cardBody(c ui.CardVM, _ config.Config) fyne.CanvasObject {
	switch c.Kind {
	case ui.KindGPU:
		rows := c.GPU.Overview
		if len(rows) == 0 && len(c.GPU.Detail) > 0 {
			rows = c.GPU.Detail
		}
		objs := make([]fyne.CanvasObject, 0, len(rows))
		for _, r := range rows {
			objs = append(objs, components.NewMetricRow(r))
		}
		if len(objs) == 0 {
			return noDataYet()
		}
		return container.NewVBox(objs...)
	case ui.KindSwap:
		box := container.NewVBox()
		for _, line := range c.Swap.ModelLines {
			box.Add(widget.NewLabel(line))
		}
		if c.Swap.MoreCount > 0 {
			box.Add(widget.NewLabelWithStyle(
				"+"+ui.FormatCount(float64(c.Swap.MoreCount))+" more",
				fyne.TextAlignLeading, fyne.TextStyle{Italic: true}))
		}
		if len(c.Swap.ModelLines) == 0 {
			box.Add(widget.NewLabel("No models running"))
		}
		return box
	case ui.KindQuota:
		box := container.NewVBox()
		for _, w := range c.Quota.Windows {
			box.Add(components.NewQuotaRow(w))
		}
		if len(c.Quota.Windows) == 0 {
			return noDataYet()
		}
		return box
	default:
		return noDataYet()
	}
}

// withWebUILink appends a navigation button to a card body (navigation
// only — S1 never calls control APIs).
func withWebUILink(body fyne.CanvasObject, label, target string) fyne.CanvasObject {
	btn := widget.NewButtonWithIcon(label, theme.LoginIcon(), func() {
		if u, err := url.Parse(target); err == nil && u.Scheme != "" {
			fyne.CurrentApp().OpenURL(u)
		}
	})
	return container.NewVBox(body, btn)
}

func noDataYet() fyne.CanvasObject {
	return widget.NewLabelWithStyle("No data yet", fyne.TextAlignLeading, fyne.TextStyle{Italic: true})
}

func replaceObjects(box *fyne.Container, objs ...fyne.CanvasObject) {
	box.Objects = objs
	box.Refresh()
}
