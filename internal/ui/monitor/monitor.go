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

	"git.skobk.in/skobkin/aimonitor/internal/buildinfo"
	"git.skobk.in/skobkin/aimonitor/internal/config"
	"git.skobk.in/skobkin/aimonitor/internal/domain"
	"git.skobk.in/skobkin/aimonitor/internal/pace"
	"git.skobk.in/skobkin/aimonitor/internal/registry"
	"git.skobk.in/skobkin/aimonitor/internal/resources"
	"git.skobk.in/skobkin/aimonitor/internal/ui"
	"git.skobk.in/skobkin/aimonitor/internal/ui/components"
)

// MonitorController is everything the monitor window needs from the app.
// All methods are safe to call on the UI thread.
type MonitorController interface {
	Instances() []config.SourceInstance
	States() map[domain.InstanceID]domain.Runtime
	RefreshAll()
	OpenSettings()
	// HideSensitiveInfo reports the global privacy preference: account-level
	// monetary values render masked until hovered while it is set.
	HideSensitiveInfo() bool
}

// monitorSize is the splash window size (UI_UX ~500–560 wide; content
// scrolls, so the height is a comfortable default, not a limit).
var monitorSize = fyne.NewSize(540, 660)

// Monitor is the borderless tray monitor window: chrome, the three tabs,
// and a 1-second ticker re-rendering relative freshness labels.
type Monitor struct {
	ctrl MonitorController
	win  fyne.Window

	// done closes when the app begins quitting; background work scheduled
	// by the monitor bails out instead of touching the dying UI.
	done <-chan struct{}

	overviewBox *fyne.Container
	localBox    *fyne.Container
	remoteBox   *fyne.Container

	refreshBtn *widget.Button

	// tickFns refresh only the time-dependent labels (freshness ages,
	// reset countdowns) of the tree built by the last rebuild. Written by
	// rebuild and read by the ticker — both on the UI thread (the ticker
	// hops through fyne.Do), so no locking is needed.
	tickFns []func(now time.Time)

	mu      sync.Mutex
	visible bool
}

// NewMonitor builds the monitor window (hidden; tray-first startup shows
// it on demand). done closes when the app begins quitting.
func NewMonitor(a fyne.App, ctrl MonitorController, done <-chan struct{}) *Monitor {
	m := &Monitor{ctrl: ctrl, done: done}

	// The window title leads with the build version so release builds are
	// identifiable at a glance ("AI Monitor 0.7.0"; development builds
	// read "AI Monitor dev").
	windowTitle := buildinfo.AppName + " " + buildinfo.Version

	var win fyne.Window
	if d, ok := a.Driver().(desktop.Driver); ok {
		win = d.CreateSplashWindow()
	} else {
		// No desktop driver (off-desktop run): a plain window still works.
		win = a.NewWindow(windowTitle)
	}
	// Splash windows are created with an empty title (Fyne would show
	// "Fyne Application"); give the window its name and icon explicitly.
	win.SetTitle(windowTitle)
	win.SetIcon(resources.Icon())
	m.win = win

	m.overviewBox = container.NewVBox()
	m.localBox = container.NewVBox()
	m.remoteBox = container.NewVBox()

	m.refreshBtn = widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), m.onRefresh)
	settingsBtn := widget.NewButtonWithIcon("", theme.SettingsIcon(), ctrl.OpenSettings)

	tabs := container.NewAppTabs(
		container.NewTabItem("Overview", container.NewVScroll(m.overviewBox)),
		container.NewTabItem("Local", container.NewVScroll(m.localBox)),
		container.NewTabItem("Remote", container.NewVScroll(m.remoteBox)),
	)
	tabs.SelectIndex(0)

	// No chrome row: the actions float over the tab content, top-right
	// (the tab strip only reaches mid-width, so they sit in its empty
	// right end). The row must sit in a VBox: a Stack stretches its
	// children to the full size and an HBox then grows its children to
	// the row height, so a bare row stretched the buttons window-tall.
	// Empty overlay space is not interactive, so the tabs underneath
	// still take the clicks.
	actions := container.NewVBox(
		container.NewHBox(layout.NewSpacer(), m.refreshBtn, settingsBtn),
	)
	win.SetContent(container.NewStack(tabs, actions))
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

// ToggleVisible shows the window if hidden, or hides it if shown.
// This is the standard left-click behaviour for a tray-controlled splash window.
func (m *Monitor) ToggleVisible() {
	if m.isVisible() {
		m.hide()
	} else {
		m.Show()
	}
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
//
// Rebuilds are skipped while the window is hidden. Fyne creates widget
// renderers during layout even when nothing is painted, and a hidden
// window never repaints, so Fyne's render expiry (destroyExpiredRenderers)
// never runs — every hidden rebuild would retain its entire widget tree in
// Fyne's global renderer cache until quit, which is precisely the leak that
// grew an idle tray instance to gigabytes. Show() rebuilds from the current
// states before revealing the window, so nothing is lost while hidden.
func (m *Monitor) OnStatesChanged() {
	if !m.isVisible() {
		return
	}
	m.rebuild()
}

// StartTicker re-renders the relative time labels ("5s ago", countdowns)
// once a second while the window is visible. It exits when the app begins
// quitting. Labels are updated in place: rebuilding the whole tree every
// second would churn Fyne's caches for no visible difference.
func (m *Monitor) StartTicker() {
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.done:
				return
			case <-ticker.C:
				if !m.isVisible() {
					continue
				}
				fyne.Do(func() { m.updateTimeTexts(time.Now()) })
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
		// Once the app is quitting the event loop is gone: fyne.Do would
		// either run the callback inline on this timer goroutine or race
		// the loop's queue teardown. Leave the dead UI alone.
		select {
		case <-m.done:
			return
		default:
		}
		fyne.Do(m.refreshBtn.Enable)
	})
}

// rebuild regenerates all three tab bodies from the configured instances
// and their runtimes, collecting the per-second label refreshers of the
// fresh tree for the ticker.
func (m *Monitor) rebuild() {
	instances := m.ctrl.Instances()
	states := m.ctrl.States()
	now := time.Now()

	var tick []func(now time.Time)

	// Pace data reaches the builders as plain read-only maps; the wired
	// tracker supply lands with the app integration (S7).
	var paces map[domain.InstanceID]pace.InstancePaces
	m.rebuildOverview(ui.BuildOverview(instances, states, paces, now), states, &tick)
	replaceObjects(m.localBox, m.tabObjects(ui.BuildLocalTab(instances, states, paces, now), instances, states, "No local sources enabled.", &tick)...)
	replaceObjects(m.remoteBox, m.tabObjects(ui.BuildRemoteTab(instances, states, paces, now), instances, states, "No remote sources enabled.", &tick)...)

	m.tickFns = tick
}

// updateTimeTexts re-renders the time-dependent labels of the current tree
// (freshness ages, quota reset countdowns) for the given instant. It must
// run on the UI thread. Data-dependent content is intentionally untouched:
// it changes only with a new snapshot, which triggers a rebuild instead.
func (m *Monitor) updateTimeTexts(now time.Time) {
	for _, tick := range m.tickFns {
		tick(now)
	}
}

func (m *Monitor) rebuildOverview(vm ui.OverviewVM, states map[domain.InstanceID]domain.Runtime, tick *[]func(now time.Time)) {
	var objs []fyne.CanvasObject
	if vm.NothingEnabled {
		objs = append(objs, components.NewEmptyState("No sources enabled.\nConfigure sources in Settings.", m.ctrl.OpenSettings))
		replaceObjects(m.overviewBox, objs...)

		return
	}

	if len(vm.LocalCards) > 0 {
		objs = append(objs, components.NewSectionLabel("Local"))
		objs = append(objs, m.overviewRow(vm.LocalCards, states, tick)...)
	}
	if len(vm.RemoteCards) > 0 {
		objs = append(objs, components.NewSectionLabel("Remote"))
		objs = append(objs, m.overviewRow(vm.RemoteCards, states, tick)...)
	}
	replaceObjects(m.overviewBox, objs...)
}

// overviewRow lays out overview cards in pairs of equal columns
// (GridWithColumns); with an odd count the final card takes the full width.
func (m *Monitor) overviewRow(cards []ui.OverviewCardVM, states map[domain.InstanceID]domain.Runtime, tick *[]func(now time.Time)) []fyne.CanvasObject {
	objs := make([]fyne.CanvasObject, 0, (len(cards)+1)/2)
	for i := 0; i < len(cards); i += 2 {
		if i+1 < len(cards) {
			objs = append(objs, container.NewGridWithColumns(2,
				overviewSourceCard(cards[i], states[cards[i].Instance], m.overviewCardBody(cards[i], tick), tick),
				overviewSourceCard(cards[i+1], states[cards[i+1].Instance], m.overviewCardBody(cards[i+1], tick), tick),
			))

			continue
		}
		objs = append(objs, overviewSourceCard(cards[i], states[cards[i].Instance], m.overviewCardBody(cards[i], tick), tick))
	}

	return objs
}

// overviewSourceCard wraps an overview card body in a title/status header.
// No footer: Overview omits failure detail per S2_UI_UX. As in the detail
// cards, the title wraps in a Border centre and the status keeps its
// natural width on the right: a stale status ("5h32m · stale") in a
// minimum-width HBox once widened the paired overview columns past the
// fixed window width. The status label ages with time, so its per-second
// refresher is collected for the ticker.
func overviewSourceCard(vm ui.OverviewCardVM, rt domain.Runtime, body fyne.CanvasObject, tick *[]func(now time.Time)) fyne.CanvasObject {
	status := widget.NewLabel(vm.Status)
	header := container.NewBorder(nil, nil, nil, status,
		components.NewWrappedLabel(vm.Title, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
	)
	*tick = append(*tick, func(now time.Time) {
		setLabelStatus(status, ui.FreshnessText(rt, now))
	})

	return widget.NewCard("", "", container.NewVBox(header, body))
}

// setLabelStatus writes a freshness label, skipping the refresh when the
// rendered text is unchanged.
func setLabelStatus(l *widget.Label, s string) {
	if l.Text != s {
		l.SetText(s)
	}
}

// overviewCardBody dispatches to the compact overview body builder. Spend
// bodies receive the privacy predicate; all builders run on the UI thread.
func (m *Monitor) overviewCardBody(c ui.OverviewCardVM, tick *[]func(now time.Time)) fyne.CanvasObject {
	switch c.Kind {
	case ui.OverviewMetrics:
		return components.NewMetricsOverviewBody(c.Metrics)
	case ui.OverviewRuntime:
		return components.NewRuntimeOverviewBody(c.Runtime)
	case ui.OverviewQuota:
		body, countdown := components.NewQuotaOverviewBody(c.Quota)
		if countdown != nil {
			*tick = append(*tick, countdown)
		}

		return body
	case ui.OverviewSpend:
		return components.NewSpendOverviewBody(c.Spend, m.ctrl.HideSensitiveInfo)
	case ui.OverviewService:
		return components.NewServiceOverviewBody(c.Service)
	default:
		return noDataYet()
	}
}

// tabObjects renders one detail tab: the cards with per-instance web UI
// buttons, or the empty state when the category has no enabled instances.
func (m *Monitor) tabObjects(cards []ui.CardVM, instances []config.SourceInstance, states map[domain.InstanceID]domain.Runtime, emptyText string, tick *[]func(now time.Time)) []fyne.CanvasObject {
	if len(cards) == 0 {
		return []fyne.CanvasObject{components.NewEmptyState(emptyText, m.ctrl.OpenSettings)}
	}
	objs := make([]fyne.CanvasObject, 0, len(cards))
	for _, c := range cards {
		body := m.cardBody(c, tick)
		if label, target, ok := webLink(c, instances); ok {
			body = withWebUILink(body, "Open "+label, target)
		}
		card, setStatus := components.NewSourceCard(c, body)
		if rt, ok := states[c.Instance]; ok {
			*tick = append(*tick, func(now time.Time) {
				setStatus(ui.FreshnessText(rt, now))
			})
		}
		objs = append(objs, card)
	}

	return objs
}

// webLink resolves a card's provider web UI from its configured instance;
// ok is false for unknown instances or kinds without a web UI.
func webLink(c ui.CardVM, instances []config.SourceInstance) (label, target string, ok bool) {
	for _, inst := range instances {
		if inst.ID != c.Instance {
			continue
		}
		def, found := registry.Get(inst.Kind)
		if !found {
			return "", "", false
		}
		url, has := def.WebURL(inst)

		return def.WebLabel, url, has
	}

	return "", "", false
}

// cardBody renders the card's data body from its view model. Which rows
// appear depends on the VM the builder produced (overview vs detail). Any
// per-second refresher the rows carry is collected for the ticker.
func (m *Monitor) cardBody(c ui.CardVM, tick *[]func(now time.Time)) fyne.CanvasObject {
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
			// Model names are provider free text and can be long
			// ("hf.co/org/repo:quant"): wrap instead of widening the tab.
			box.Add(components.NewWrappedLabel(line, fyne.TextAlignLeading, fyne.TextStyle{}))
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
		// The optional Account section belongs to the subscription kinds
		// (ChatGPT Codex); the API-quota kinds never set it, so their
		// rendering is unchanged.
		if len(c.Quota.Account) > 0 {
			box.Add(components.NewSectionLabel("Account"))
			for _, r := range c.Quota.Account {
				box.Add(components.NewMetricRow(r))
			}
			box.Add(components.NewSectionLabel("Limits"))
		}
		for _, w := range c.Quota.Windows {
			row, countdown := components.NewQuotaRow(w)
			if countdown != nil {
				*tick = append(*tick, countdown)
			}
			box.Add(row)
		}
		if len(c.Quota.Windows) == 0 {
			if len(c.Quota.Account) > 0 {
				return box
			}

			return noDataYet()
		}

		return box
	case ui.KindSpend:
		return components.NewSpendDetailBody(c.Spend, m.ctrl.HideSensitiveInfo)
	case ui.KindService:
		body, ticks := components.NewServiceDetailBody(c.Service, time.Now())
		*tick = append(*tick, ticks...)

		return body
	default:
		return noDataYet()
	}
}

// withWebUILink appends a navigation button to a card body (navigation
// only — S1 never calls control APIs).
func withWebUILink(body fyne.CanvasObject, label, target string) fyne.CanvasObject {
	btn := widget.NewButtonWithIcon(label, theme.LoginIcon(), func() {
		if u, err := url.Parse(target); err == nil && u.Scheme != "" {
			// Nothing useful to do when the browser cannot be opened.
			_ = fyne.CurrentApp().OpenURL(u)
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
