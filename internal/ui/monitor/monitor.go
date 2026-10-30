package monitor

import (
	"net/url"
	"slices"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
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
	// PaceStates carries the derived usage-pace estimates (S7); a plain
	// read-only map, recomputed per rebuild.
	PaceStates() map[domain.InstanceID]pace.InstancePaces
	RefreshAll()
	// RefreshOne triggers an immediate refresh of one instance, with the
	// same coalescing as RefreshAll (no duplicate in-flight polls).
	RefreshOne(id domain.InstanceID)
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

	// tabs and the two detail scrolls are the drill-down targets: an
	// Overview tap selects the matching tab and scrolls the card into view.
	// localIDs/remoteIDs mirror the card order of the last rebuild (UI
	// thread only, like tickFns — written by rebuild, read by taps).
	tabs         *container.AppTabs
	localScroll  *detailScroll
	remoteScroll *detailScroll
	localIDs     []domain.InstanceID
	remoteIDs    []domain.InstanceID

	refreshBtn *widget.Button

	// tickFns refresh only the time-dependent labels (freshness ages,
	// reset countdowns) of the tree built by the last rebuild. Written by
	// rebuild and read by the ticker — both on the UI thread (the ticker
	// hops through fyne.Do), so no locking is needed.
	tickFns []func(now time.Time)

	// revealKey carries which sensitive money row is currently hovered,
	// keyed "instance\x00label". Fyne recomputes hover only when the
	// pointer moves, so after a rebuild swaps in a fresh SensitiveValue
	// under a stationary pointer the new widget restores the reveal from
	// this key; the hover-out of the replaced widget clears it (also on
	// the UI thread, with the same no-locking reasoning as tickFns).
	// Deliberately not reset by rebuild — resetting is the defect.
	revealKey string

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

	m.localScroll = newDetailScroll(m.localBox)
	m.remoteScroll = newDetailScroll(m.remoteBox)
	tabs := container.NewAppTabs(
		container.NewTabItem("Overview", container.NewVScroll(m.overviewBox)),
		container.NewTabItem("Local", m.localScroll),
		container.NewTabItem("Remote", m.remoteScroll),
	)
	tabs.SelectIndex(0)
	m.tabs = tabs

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
	// One snapshot feeds all three tabs: three reads would cost three
	// estimate passes under the tracker mutex and could disagree across
	// tabs.
	paces := m.ctrl.PaceStates()
	now := time.Now()

	var tick []func(now time.Time)

	m.rebuildOverview(ui.BuildOverview(instances, states, paces, now), states, &tick)
	localObjs, localIDs := m.tabObjects(ui.BuildLocalTab(instances, states, paces, now), instances, states, "No local sources enabled.", &tick)
	remoteObjs, remoteIDs := m.tabObjects(ui.BuildRemoteTab(instances, states, paces, now), instances, states, "No remote sources enabled.", &tick)
	m.localIDs = localIDs
	m.remoteIDs = remoteIDs
	replaceObjects(m.localBox, localObjs...)
	replaceObjects(m.remoteBox, remoteObjs...)

	m.tickFns = tick
}

// updateTimeTexts re-renders the time-dependent labels of the current tree
// (freshness ages, quota reset countdowns) for the given instant. It must
// run on the UI thread. Data-dependent content is intentionally untouched:
// it changes only with a new snapshot, which triggers a rebuild instead.
func (m *Monitor) updateTimeTexts(now time.Time) {
	// A drill-down offset queued while the target tab was still unsized is
	// applied here, on the first tick after its layout — the retry path of
	// detailScroll.applyPending (normally it applies immediately).
	m.localScroll.applyPending()
	m.remoteScroll.applyPending()
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
				m.overviewTile(cards[i], states, tick),
				m.overviewTile(cards[i+1], states, tick),
			))

			continue
		}
		objs = append(objs, m.overviewTile(cards[i], states, tick))
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

// overviewTile builds one Overview card as a tappable tile: the standard
// card renders identically, the wrapper adds drill-down navigation and the
// hover highlight that advertises it.
func (m *Monitor) overviewTile(c ui.OverviewCardVM, states map[domain.InstanceID]domain.Runtime, tick *[]func(now time.Time)) fyne.CanvasObject {
	card := overviewSourceCard(c, states[c.Instance], m.overviewCardBody(c, tick), tick)

	return newTapCard(card, func() { m.showDetail(c.Instance) })
}

// tapCard wraps one Overview card in a minimal widget that is tappable and
// hoverable without changing the card's look. It must be a real widget:
// Fyne's hit-test walk descends only into containers and widgets, so a
// plain wrapper struct would be invisible to it and starve any interactive
// child; as a widget, the walk matches this wrapper first and any Tappable
// descendant last — the child wins, which is exactly the precedence an
// Overview card must keep for future interactive children.
type tapCard struct {
	widget.BaseWidget

	card      fyne.CanvasObject
	highlight *canvas.Rectangle
	onTap     func()
}

func newTapCard(card fyne.CanvasObject, onTap func()) *tapCard {
	t := &tapCard{
		card:      card,
		highlight: canvas.NewRectangle(theme.Color(theme.ColorNameHover)),
		onTap:     onTap,
	}
	t.highlight.Hide()
	t.ExtendBaseWidget(t)

	return t
}

func (t *tapCard) Tapped(*fyne.PointEvent) {
	if t.onTap != nil {
		t.onTap()
	}
}

// MouseIn/MouseMoved/MouseOut implement desktop.Hoverable: the highlight
// advertises the tile as clickable the way Fyne buttons advertise
// themselves. The rectangle is stretched by the renderer and never
// measured, so the card's geometry is untouched.
func (t *tapCard) MouseIn(*desktop.MouseEvent) {
	t.highlight.Show()
}

func (t *tapCard) MouseMoved(*desktop.MouseEvent) {}

func (t *tapCard) MouseOut() {
	t.highlight.Hide()
}

func (t *tapCard) CreateRenderer() fyne.WidgetRenderer {
	return &tapCardRenderer{t: t}
}

type tapCardRenderer struct {
	t *tapCard
}

func (r *tapCardRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.t.highlight, r.t.card}
}

func (r *tapCardRenderer) Layout(size fyne.Size) {
	r.t.highlight.Move(fyne.NewPos(0, 0))
	r.t.highlight.Resize(size)
	r.t.card.Move(fyne.NewPos(0, 0))
	r.t.card.Resize(size)
}

// MinSize is the card's own: the highlight is never measured.
func (r *tapCardRenderer) MinSize() fyne.Size {
	return r.t.card.MinSize()
}

func (r *tapCardRenderer) Refresh() {
	r.t.highlight.FillColor = theme.Color(theme.ColorNameHover)
	r.t.highlight.Refresh()
	r.t.card.Refresh()
}

func (r *tapCardRenderer) Destroy() {}

// refreshOneButton builds the detail header's per-source refresh action:
// icon-only, the same idiom as the Settings row actions and the monitor's
// floating buttons, with the global refresh button's disable-for-1s
// feedback. Re-enabling a button that a rebuild has already detached is
// harmless.
func (m *Monitor) refreshOneButton(id domain.InstanceID) fyne.CanvasObject {
	btn := widget.NewButtonWithIcon("", theme.ViewRefreshIcon(), nil)
	btn.OnTapped = func() {
		m.ctrl.RefreshOne(id)
		btn.Disable()
		time.AfterFunc(time.Second, func() {
			select {
			case <-m.done:
				return
			default:
			}
			fyne.Do(btn.Enable)
		})
	}

	return btn
}

// showDetail navigates from an Overview tap to the card's detail tab and
// scrolls the card to the top of the tab. UI thread only.
func (m *Monitor) showDetail(id domain.InstanceID) {
	switch m.categoryOf(id) {
	case domain.CategoryLocal:
		m.tabs.SelectIndex(1)
		m.scrollToCard(m.localBox, m.localScroll, slices.Index(m.localIDs, id))
	case domain.CategoryRemote:
		m.tabs.SelectIndex(2)
		m.scrollToCard(m.remoteBox, m.remoteScroll, slices.Index(m.remoteIDs, id))
	default:
		// Unknown instance: it was removed between render and tap; the
		// next rebuild fixes the tree, so there is nothing to navigate to.
	}
}

// categoryOf resolves an instance's category from the configured list.
func (m *Monitor) categoryOf(id domain.InstanceID) domain.Category {
	for _, inst := range m.ctrl.Instances() {
		if inst.ID == id {
			return inst.Kind.Category()
		}
	}

	return ""
}

// scrollToCard queues the scroll offset placing child i of the (VBox) box
// at the top of the viewport: the sum of the preceding children's minimum
// heights plus the theme padding between each pair (VBox pads only between
// children). Minimum sizes are intrinsic, so this works while the target
// tab is still unselected — AppTabs lays out only the active tab. The
// scroller applies the offset (clamped to the content) at its next layout.
func (m *Monitor) scrollToCard(box *fyne.Container, scroll *detailScroll, i int) {
	if scroll == nil || i < 0 || i >= len(box.Objects) {
		return
	}
	offset := float32(0)
	for k := 0; k < i; k++ {
		offset += box.Objects[k].MinSize().Height
	}
	offset += float32(i) * theme.Padding()
	scroll.scrollToY(offset)
}

// detailScroll wraps the detail tab scroller to carry one queued offset: an
// offset set directly after a tab switch is unreliable — the scroller
// zeroes it while neither it nor its content has been laid out yet
// (updateOffset clamps everything to the current, still-zero sizes). The
// queued offset is applied in the wrapper's layout pass, once the real
// viewport size is known, and clamped so the tail cannot overscroll.
type detailScroll struct {
	widget.BaseWidget

	scroll     *container.Scroll
	pending    float32
	hasPending bool
}

func newDetailScroll(box *fyne.Container) *detailScroll {
	d := &detailScroll{scroll: container.NewVScroll(box)}
	d.ExtendBaseWidget(d)

	return d
}

func (d *detailScroll) scrollToY(y float32) {
	d.pending = y
	d.hasPending = true
	d.applyPending()
	d.Refresh()
}

// applyPending consumes the queued offset when the scroller knows its
// viewport size; it is retried from the monitor ticker (updateTimeTexts)
// because a tab switched inside a tap handler is laid out at frame time,
// and the scroller zeroes offsets set while its content is unsized.
func (d *detailScroll) applyPending() {
	if !d.hasPending || d.scroll.Size().Height <= 0 {
		return
	}
	offset := d.pending
	if max := d.scroll.Content.MinSize().Height - d.scroll.Size().Height; offset > max {
		offset = max
	}
	if offset < 0 {
		offset = 0
	}
	d.hasPending = false
	if d.scroll.Offset.Y == offset {
		return
	}
	d.scroll.Offset = fyne.NewPos(0, offset)
	d.scroll.Refresh()
}

func (d *detailScroll) CreateRenderer() fyne.WidgetRenderer {
	return &detailScrollRenderer{d: d}
}

type detailScrollRenderer struct {
	d *detailScroll
}

func (r *detailScrollRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.d.scroll}
}

func (r *detailScrollRenderer) Layout(size fyne.Size) {
	r.d.scroll.Resize(size)
}

func (r *detailScrollRenderer) MinSize() fyne.Size {
	return r.d.scroll.MinSize()
}

func (r *detailScrollRenderer) Refresh() {
	r.d.scroll.Refresh()
	r.d.applyPending()
}

func (r *detailScrollRenderer) Destroy() {}

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
		return components.NewSpendOverviewBody(c.Spend, m.sensitiveRows(c.Instance))
	case ui.OverviewService:
		return components.NewServiceOverviewBody(c.Service)
	default:
		return noDataYet()
	}
}

// tabObjects renders one detail tab: the cards with per-instance web UI
// buttons, or the empty state when the category has no enabled instances.
// It also returns the card order, which is the drill-down scroll index.
func (m *Monitor) tabObjects(cards []ui.CardVM, instances []config.SourceInstance, states map[domain.InstanceID]domain.Runtime, emptyText string, tick *[]func(now time.Time)) ([]fyne.CanvasObject, []domain.InstanceID) {
	if len(cards) == 0 {
		return []fyne.CanvasObject{components.NewEmptyState(emptyText, m.ctrl.OpenSettings)}, nil
	}
	objs := make([]fyne.CanvasObject, 0, len(cards))
	ids := make([]domain.InstanceID, 0, len(cards))
	for _, c := range cards {
		body := m.cardBody(c, tick)
		if label, target, ok := webLink(c, instances); ok {
			body = withWebUILink(body, "Open "+label, target)
		}
		card, setStatus := components.NewSourceCardWithHeaderAction(c, body, m.refreshOneButton(c.Instance))
		if rt, ok := states[c.Instance]; ok {
			*tick = append(*tick, func(now time.Time) {
				setStatus(ui.FreshnessText(rt, now))
			})
		}
		objs = append(objs, card)
		ids = append(ids, c.Instance)
	}

	return objs, ids
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
		return components.NewSpendDetailBody(c.Spend, m.sensitiveRows(c.Instance))
	case ui.KindService:
		body, ticks := components.NewServiceDetailBody(c.Service, time.Now())
		*tick = append(*tick, ticks...)

		return body
	default:
		return noDataYet()
	}
}

// revealKeyFor is the revealKey format for one sensitive row.
func revealKeyFor(inst domain.InstanceID, label string) string {
	return string(inst) + "\x00" + label
}

// sensitiveRows builds the per-card SensitiveRows: predicates read the
// monitor's reveal key so the row under a stationary pointer stays revealed
// across rebuilds, and hover reports maintain that key.
func (m *Monitor) sensitiveRows(inst domain.InstanceID) components.SensitiveRows {
	return perCardRows{m: m, inst: inst}
}

type perCardRows struct {
	m    *Monitor
	inst domain.InstanceID
}

func (p perCardRows) Hidden(label string) func() bool {
	key := revealKeyFor(p.inst, label)

	return func() bool {
		return p.m.ctrl.HideSensitiveInfo() && p.m.revealKey != key
	}
}

// Hover records which row the pointer entered. On leave, the key is only
// cleared when it still names this row (a fast move into another row may
// have replaced it) and the tree is refreshed so the live widget — a
// different SensitiveValue after a rebuild — re-masks.
func (p perCardRows) Hover(label string, revealed bool) {
	key := revealKeyFor(p.inst, label)
	if revealed {
		p.m.revealKey = key

		return
	}
	if p.m.revealKey != key {
		return
	}
	p.m.revealKey = ""
	if c := p.m.win.Content(); c != nil {
		c.Refresh()
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
