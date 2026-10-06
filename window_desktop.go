//go:build !server

package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"runtime"
	"strconv"
	"time"

	"github.com/vietlubu/agents-dashboard/internal/config"
	"github.com/vietlubu/agents-dashboard/internal/service"
	"github.com/vietlubu/agents-dashboard/internal/sleep"
	"github.com/vietlubu/agents-dashboard/internal/store"
	appupdate "github.com/vietlubu/agents-dashboard/internal/update"
	"github.com/vietlubu/agents-dashboard/internal/version"
	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"
)

// mainWindow is the single desktop window, kept so the menu bar can reveal it again after
// the user has closed or hidden it.
var mainWindow *application.WebviewWindow

// openMainWindow creates the desktop window. In server mode this file is not compiled:
// server builds create browser windows internally, and asking for a native window would
// only log a warning.
func openMainWindow(app *application.App) {
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Agents Dashboard",
		Width:            1440,
		Height:           900,
		MinWidth:         1024,
		MinHeight:        700,
		BackgroundColour: application.NewRGB(20, 22, 26),
		URL:              "/",
	})
	mainWindow = window
	// On macOS, closing the window hides it and drops the Dock icon; the tray brings it
	// back. Elsewhere this is a no-op and closing keeps the platform's own behaviour.
	installWindowCloseBehavior(window)
	window.Center()
	window.Show()
}

// trayRefreshTick updates today's totals and provides a fallback status snapshot.
// Sleep and settings changes request an immediate refresh through the same loop.
const trayRefreshTick = 30 * time.Second

// trayLabels are the menu-bar strings for one locale. The tray lives in Go, outside the
// Vue i18n bundle, so it carries its own small table and follows settings.locale.
type trayLabels struct {
	title, mode, modeOff, modeAgent, modeAlways string
	system, display, lid, restore               string
	sleepNow, dispNow, screensaver              string
	usage, total, input, cache, output          string
	status, show, quit                          string
	confirmTitle, confirmMsg                    string
	confirmSleep, confirmCancel                 string

	stUnsupported, stDisabled, stActive  string
	stGrace, stWaitingUser, stSleeping   string
	stIdle, stAlways, stError, noTargets string
}

func trayLabelsFor(locale string) trayLabels {
	if locale == "vi" {
		return trayLabels{
			title:         "Agents Dashboard",
			mode:          "Chế độ chặn sleep",
			modeOff:       "Tắt",
			modeAgent:     "Khi agent hoạt động",
			modeAlways:    "Luôn chặn sleep",
			system:        "Chặn sleep hệ thống",
			display:       "Chặn sleep màn hình",
			lid:           "Gi\u1eef m\u00e1y ch\u1ea1y khi g\u1eadp n\u1eafp",
			restore:       "Khôi phục sleep hệ thống",
			sleepNow:      "Ng\u1ee7 ngay",
			dispNow:       "T\u1eaft m\u00e0n h\u00ecnh",
			screensaver:   "B\u1eadt screensaver",
			usage:         "Token h\u00f4m nay",
			total:         "T\u1ed5ng: ",
			input:         "Input: ",
			cache:         "Input cache: ",
			output:        "Output: ",
			status:        "Tr\u1ea1ng th\u00e1i: ",
			show:          "M\u1edf Dashboard",
			quit:          "Tho\u00e1t",
			confirmTitle:  "Agent v\u1eabn \u0111ang l\u00e0m vi\u1ec7c",
			confirmMsg:    "C\u00f3 phi\u00ean agent \u0111ang ho\u1ea1t \u0111\u1ed9ng. V\u1eabn sleep?",
			confirmSleep:  "Sleep",
			confirmCancel: "Hu\u1ef7",

			stUnsupported: "Không hỗ trợ",
			stDisabled:    "Đang tắt",
			stActive:      "Đang giữ máy thức",
			stGrace:       "Đang đếm ngược tới lúc ngủ",
			stWaitingUser: "Chờ người dùng rời máy",
			stSleeping:    "Đang ngủ",
			stIdle:        "Rảnh",
			stAlways:      "Đang chặn sleep liên tục",
			stError:       "Lỗi điều khiển sleep",
			noTargets:     "Chưa chọn mục nào cần chặn sleep.",
		}
	}
	return trayLabels{
		title:         "Agents Dashboard",
		mode:          "Sleep prevention mode",
		modeOff:       "Off",
		modeAgent:     "While an agent is active",
		modeAlways:    "Always prevent sleep",
		system:        "Prevent system sleep",
		display:       "Prevent display sleep",
		lid:           "Keep running with the lid closed",
		restore:       "Restore system sleep",
		sleepNow:      "Sleep now",
		dispNow:       "Turn off display",
		screensaver:   "Start screensaver",
		usage:         "Session usage (today)",
		total:         "Total (all tokens): ",
		input:         "Input: ",
		cache:         "Input cache: ",
		output:        "Output: ",
		status:        "Status: ",
		show:          "Open Dashboard",
		quit:          "Quit",
		confirmTitle:  "An agent is still working",
		confirmMsg:    "An agent session is active. Sleep anyway?",
		confirmSleep:  "Sleep",
		confirmCancel: "Cancel",

		stUnsupported: "Unsupported",
		stDisabled:    "Off",
		stActive:      "Holding the machine awake",
		stGrace:       "Countdown to sleep",
		stWaitingUser: "Waiting for the user to step away",
		stSleeping:    "Sleeping",
		stIdle:        "Idle",
		stAlways:      "Preventing sleep continuously",
		stError:       "Sleep control error",
		noTargets:     "No sleep prevention targets are selected.",
	}
}

// tray owns the menu-bar status item: the sleep toggles and today's usage. macOS shows it
// in the menu bar; Windows in the notification area; Linux in the app indicator when a
// tray host is present.
type tray struct {
	app      *application.App
	sleepSvc *service.SleepService
	setSvc   *service.SettingsService

	item  *application.SystemTray
	title *application.MenuItem
	usage *application.Menu
	show  *application.MenuItem
	quit  *application.MenuItem

	modeHeading *application.MenuItem
	modeOff     *application.MenuItem
	modeAgent   *application.MenuItem
	modeAlways  *application.MenuItem
	sysSleep    *application.MenuItem
	dispSleep   *application.MenuItem
	lidSleep    *application.MenuItem
	restore     *application.MenuItem
	sleepNow    *application.MenuItem
	dispNow     *application.MenuItem
	screensaver *application.MenuItem
	status      *application.MenuItem
	total       *application.MenuItem
	input       *application.MenuItem
	cache       *application.MenuItem
	output      *application.MenuItem

	locale      string
	labels      trayLabels
	refreshKick chan struct{}
	stop        chan struct{}
}

// setupTray creates the menu bar (desktop builds only). A duplicate registration would
// error, so it is created exactly once from main.
func setupTray(app *application.App, setSvc *service.SettingsService, sleepSvc *service.SleepService) {
	t := &tray{app: app, sleepSvc: sleepSvc, setSvc: setSvc,
		refreshKick: make(chan struct{}, 1), stop: make(chan struct{})}
	t.locale = t.localeOf()
	t.labels = trayLabelsFor(t.locale)
	l := t.labels

	menu := application.NewMenu()
	t.title = menu.Add(l.title)
	t.title.SetEnabled(false)
	menu.AddSeparator()

	t.modeHeading = menu.Add(l.mode)
	t.modeHeading.SetEnabled(false)
	t.modeOff = menu.AddRadio(l.modeOff, true)
	t.modeAgent = menu.AddRadio(l.modeAgent, false)
	t.modeAlways = menu.AddRadio(l.modeAlways, false)
	t.modeOff.OnClick(func(*application.Context) { t.setSleepMode(config.SleepModeOff) })
	t.modeAgent.OnClick(func(*application.Context) { t.setSleepMode(config.SleepModeAgent) })
	t.modeAlways.OnClick(func(*application.Context) { t.setSleepMode(config.SleepModeAlways) })
	menu.AddSeparator()
	t.sysSleep = menu.AddCheckbox(l.system, true)
	t.sysSleep.OnClick(func(*application.Context) { t.toggleSystem() })
	t.dispSleep = menu.AddCheckbox(l.display, true)
	t.lidSleep = menu.AddCheckbox(l.lid, false)
	t.dispSleep.OnClick(func(*application.Context) { t.toggleDisplay() })
	t.lidSleep.OnClick(func(*application.Context) { t.toggleLid() })
	t.restore = menu.Add(l.restore)
	t.restore.OnClick(func(*application.Context) { t.restoreClamshell() })

	menu.AddSeparator()
	t.sleepNow = menu.Add(l.sleepNow)
	t.dispNow = menu.Add(l.dispNow)
	t.screensaver = menu.Add(l.screensaver)
	t.sleepNow.OnClick(func(*application.Context) { t.requestSleepNow() })
	t.dispNow.OnClick(func(*application.Context) { t.displaySleepNow() })
	t.screensaver.OnClick(func(*application.Context) { t.startScreensaver() })

	menu.AddSeparator()
	t.usage = menu.AddSubmenu(l.usage)
	t.total = t.usage.Add(l.total + "-")
	t.input = t.usage.Add(l.input + "-")
	t.cache = t.usage.Add(l.cache + "-")
	t.output = t.usage.Add(l.output + "-")
	for _, item := range []*application.MenuItem{t.total, t.input, t.cache, t.output} {
		item.SetEnabled(false)
	}

	menu.AddSeparator()
	t.status = menu.Add(l.status + "-")
	t.status.SetEnabled(false)
	t.show = menu.Add(l.show)
	t.show.OnClick(func(*application.Context) {
		if mainWindow != nil {
			// Reopen from the menu bar: restore the Dock icon, then bring the window back.
			setDockIconVisible(true)
			mainWindow.Show().Focus()
		}
	})
	menu.AddSeparator()
	t.quit = menu.Add(l.quit)
	t.quit.OnClick(func(*application.Context) { t.app.Quit() })

	t.item = t.app.SystemTray.New()
	if icon := trayIcon(); len(icon) > 0 {
		t.item.SetTemplateIcon(icon)
	}
	t.item.SetMenu(menu)

	settingsOff := app.Event.On(service.EventSettingsSaved, func(*application.CustomEvent) { t.requestRefresh() })
	sleepOff := app.Event.On(service.EventSleepStatus, func(*application.CustomEvent) { t.requestRefresh() })
	app.OnShutdown(func() {
		settingsOff()
		sleepOff()
		close(t.stop)
	})

	t.refresh()
	go t.loop()
}

func (t *tray) loop() {
	ticker := time.NewTicker(trayRefreshTick)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			t.refresh()
		case <-t.refreshKick:
			t.refresh()
		case <-t.stop:
			return
		}
	}
}

func (t *tray) requestRefresh() {
	select {
	case t.refreshKick <- struct{}{}:
	default:
	}
}

// localeOf reads the configured locale, defaulting to English.
func (t *tray) localeOf() string {
	s, err := t.setSvc.Get()
	if err != nil || s.Locale == "" {
		return "en"
	}
	return s.Locale
}

// current reads the effective settings, returning nil when they cannot be read.
func (t *tray) current() *store.Settings {
	s, err := t.setSvc.Get()
	if err != nil {
		return nil
	}
	return &s
}

func (t *tray) setSleepMode(mode string) {
	t.save(store.SettingsPatch{SleepMode: mode})
}

func (t *tray) toggleSystem() {
	s := t.current()
	if s == nil {
		return
	}
	s.PreventSystemSleep = !s.PreventSystemSleep
	t.save(store.SettingsPatch{PreventSystemSleep: &s.PreventSystemSleep})
}

func (t *tray) toggleDisplay() {
	s := t.current()
	if s == nil {
		return
	}
	s.PreventDisplaySleep = !s.PreventDisplaySleep
	t.save(store.SettingsPatch{PreventDisplaySleep: &s.PreventDisplaySleep})
}

func (t *tray) toggleLid() {
	s := t.current()
	if s == nil {
		return
	}
	s.PreventLidClosedSleep = !s.PreventLidClosedSleep
	t.save(store.SettingsPatch{PreventLidClosedSleep: &s.PreventLidClosedSleep})
}

// requestSleepNow puts the machine to sleep on demand. When an agent is still working it
// asks first, so a stray menu click cannot cut a running session short. The activity check
// lists processes, so it runs off the UI thread; the dialog marshals itself back.
func (t *tray) requestSleepNow() {
	go func() {
		if t.sleepSvc.AgentActive() {
			t.confirmSleepNow()
			return
		}
		_ = t.sleepSvc.SleepNow()
	}()
}

// confirmSleepNow asks whether to sleep while an agent is working, then sleeps only on the
// affirmative button.
func (t *tray) confirmSleepNow() {
	l := trayLabelsFor(t.localeOf())
	dlg := t.app.Dialog.Question()
	dlg.SetTitle(l.confirmTitle)
	dlg.SetMessage(l.confirmMsg)
	dlg.AddButton(l.confirmSleep).OnClick(func() { _ = t.sleepSvc.SleepNow() })
	dlg.AddButton(l.confirmCancel).SetAsCancel()
	dlg.Show()
}

// displaySleepNow turns the display off without suspending the machine.
func (t *tray) displaySleepNow() { _ = t.sleepSvc.DisplaySleepNow() }

// startScreensaver switches the session to the screensaver.
func (t *tray) startScreensaver() { _ = t.sleepSvc.ScreensaverNow() }

// save persists only the fields changed by a menu action.
func (t *tray) save(patch store.SettingsPatch) {
	_, err := t.setSvc.Update(patch)
	defer t.requestRefresh()
	if err != nil {
		t.showSleepError(err)
		return
	}
	requestLid := (patch.PreventLidClosedSleep != nil && *patch.PreventLidClosedSleep) ||
		patch.SleepMode == config.SleepModeAgent || patch.SleepMode == config.SleepModeAlways
	effective := t.current()
	if requestLid && effective != nil && effective.SleepMode != config.SleepModeOff &&
		effective.PreventLidClosedSleep && t.sleepSvc.ClamshellSupported() && !t.sleepSvc.Status().Clamshell {
		if err := t.sleepSvc.RequestClamshell(); err != nil {
			t.showSleepError(err)
		}
	}
}

func (t *tray) showSleepError(err error) {
	l := trayLabelsFor(t.localeOf())
	dlg := t.app.Dialog.Error()
	dlg.SetTitle(l.stError)
	dlg.SetMessage(err.Error())
	dlg.Show()
}

func (t *tray) restoreClamshell() {
	if err := t.sleepSvc.RestoreClamshell(); err != nil {
		t.showSleepError(err)
	}
	t.requestRefresh()
}

// refresh repaints the menu bar from the persisted settings, the controller status and
// today's totals. A locale change rewrites the static labels too.
func (t *tray) refresh() {
	s := t.current()
	if s != nil {
		t.applyLabels(s.Locale)
		t.modeOff.SetChecked(s.SleepMode == config.SleepModeOff)
		t.modeAgent.SetChecked(s.SleepMode == config.SleepModeAgent)
		t.modeAlways.SetChecked(s.SleepMode == config.SleepModeAlways)
		t.dispSleep.SetChecked(s.PreventDisplaySleep)
		t.sysSleep.SetChecked(s.PreventSystemSleep)
		t.lidSleep.SetChecked(s.PreventLidClosedSleep)
	}

	l := t.labels
	st := t.sleepSvc.Status()
	t.modeAgent.SetEnabled(st.Supported)
	t.modeAlways.SetEnabled(st.Supported)
	scopesEnabled := s != nil && s.SleepMode != config.SleepModeOff && st.Supported
	t.sysSleep.SetEnabled(scopesEnabled)
	t.dispSleep.SetEnabled(scopesEnabled)
	t.lidSleep.SetEnabled(scopesEnabled)
	t.restore.SetEnabled(st.Clamshell)
	t.status.SetLabel(l.status + t.statusText(st))

	tot, err := t.sleepSvc.Today()
	if err != nil {
		return
	}
	t.total.SetLabel(l.total + compactTokens(tot.Total))
	t.input.SetLabel(l.input + compactTokens(tot.Input))
	t.cache.SetLabel(fmt.Sprintf("%s%s (hit %.1f%%)", l.cache, compactTokens(tot.CacheRead), cacheHitRate(tot)))
	t.output.SetLabel(l.output + compactTokens(tot.Output))
	// Show the all-in day total in the menu bar itself; nothing to show means the icon
	// alone.
	if tokens := tot.Total; tokens > 0 {
		t.item.SetLabel(compactTokens(tokens))
	} else {
		t.item.SetLabel("")
	}
}

// applyLabels rewrites the static menu labels when the configured locale changes.
func (t *tray) applyLabels(locale string) {
	if locale == "" {
		locale = "en"
	}
	if locale == t.locale {
		return
	}
	t.locale = locale
	t.labels = trayLabelsFor(locale)
	l := t.labels
	t.title.SetLabel(l.title)
	t.modeHeading.SetLabel(l.mode)
	t.modeOff.SetLabel(l.modeOff)
	t.modeAgent.SetLabel(l.modeAgent)
	t.modeAlways.SetLabel(l.modeAlways)
	t.sysSleep.SetLabel(l.system)
	t.dispSleep.SetLabel(l.display)
	t.lidSleep.SetLabel(l.lid)
	t.restore.SetLabel(l.restore)
	t.sleepNow.SetLabel(l.sleepNow)
	t.dispNow.SetLabel(l.dispNow)
	t.screensaver.SetLabel(l.screensaver)
	t.usage.SetLabel(l.usage)
	t.show.SetLabel(l.show)
	t.quit.SetLabel(l.quit)
}

func (t *tray) statusText(st sleep.Status) string {
	l := t.labels
	if st.Error != "" {
		return l.stError + ": " + st.Error
	}
	if !st.Supported {
		return l.stUnsupported
	}
	if st.Mode == config.SleepModeOff {
		return l.stDisabled
	}
	switch st.Detail {
	case "always":
		if !st.KeepingAwake {
			return l.noTargets
		}
		return l.stAlways
	case "active":
		if !st.KeepingAwake {
			return l.noTargets
		}
		return l.stActive
	case "grace":
		if st.SleepAtMs > 0 {
			return l.stGrace + " " + time.UnixMilli(st.SleepAtMs).Format("15:04")
		}
		return l.stGrace
	case "waiting-user":
		return l.stWaitingUser
	case "sleeping":
		return l.stSleeping
	default:
		return l.stIdle
	}
}

// cacheHitRate is the share of prompt tokens served from cache: cache reads over all
// prompt tokens (fresh input plus cache reads and writes). Zero when nothing was asked.
func cacheHitRate(t store.Totals) float64 {
	denom := t.Input + t.CacheRead + t.CacheWrite
	if denom <= 0 {
		return 0
	}
	return float64(t.CacheRead) / float64(denom) * 100
}

// compactTokens renders a token count as a short menu-bar label.
func compactTokens(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return strconv.FormatFloat(float64(n)/1e9, 'f', 1, 64) + "B"
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
	case n >= 1_000:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64) + "k"
	default:
		return strconv.FormatInt(n, 10)
	}
}

// trayIcon draws a small monochrome template icon (a filled ring) so the status item is
// visible next to the text label. macOS recolors a template icon for light and dark menu
// bars; the other platforms simply show it as-is.
func trayIcon() []byte {
	const size = 22
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	cx, cy := 11.0, 11.0
	outer, inner := 9.0, 5.0
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)+0.5-cx, float64(y)+0.5-cy
			dist := dx*dx + dy*dy
			if dist <= outer*outer && dist >= inner*inner {
				img.SetRGBA(x, y, color.RGBA{R: 0, G: 0, B: 0, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// configureUpdater leaves development and unsupported builds offline. Release
// checks use the CalVer adapter; downloads are only initiated by AppService consent.
func configureUpdater(app *application.App, deps *service.Deps) error {
	deps.Updater = nil
	if version.Version == "dev" {
		deps.UpdaterDisabledReason = "development"
		return nil
	}
	if _, err := version.Compare(version.Version, version.Version); err != nil {
		return fmt.Errorf("configure updater version: %w", err)
	}
	if _, err := appupdate.DesktopAsset(runtime.GOOS, runtime.GOARCH); err != nil {
		deps.UpdaterDisabledReason = "unsupported-platform"
		return nil
	}
	provider, err := appupdate.NewGitHubProvider(&http.Client{Timeout: 5 * time.Minute}, "")
	if err != nil {
		return fmt.Errorf("configure update provider: %w", err)
	}
	if err := app.Updater.Init(updater.Config{
		CurrentVersion: version.Version,
		Providers:      []updater.Provider{provider},
		Window:         updater.WindowNone,
	}); err != nil {
		return fmt.Errorf("initialize updater: %w", err)
	}
	deps.Updater = app.Updater
	deps.UpdaterDisabledReason = ""
	return nil
}
