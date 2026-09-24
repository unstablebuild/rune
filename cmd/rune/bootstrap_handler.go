// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	sdkhandler "github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/cmd/rune/ide/apiclient"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/ideupgrade"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/term/gui"
	"unstable.build/rune/internal/term/gui/appmenu"
	"unstable.build/rune/internal/term/gui/glassbar"
	"unstable.build/rune/internal/term/gui/openpanel"
)

type bootstrapPrompter interface {
	Prompt(message string, options []string, bindings []term.KeyComb, h sdkhandler.PromptHandler) browser.Window
}

type bootstrapHandler struct {
	inner             tui.Handler
	dataDir           string
	storage           storageapi.Service
	configPath        string
	workspace         string
	zdotDir           string
	filenames         []string
	launchCmd         []string
	runner            ide.ExtensionsRunner
	trust             *pkgtrust.Store
	mu                *sync.Mutex
	publishEvent      func(term.Event) bool
	openBrowser       func(*url.URL) error
	clip              clipboard.Register
	installBackupDir  string
	preIDE            *ide.IDE
	realIDE           *ide.IDE
	g                 *gui.GUI
	transparentWindow bool
	initialThemeAttr  term.Attributes
	client            *apiclient.Client
	upgradeMgr        *ideupgrade.Manager
	upgradeCancel     context.CancelFunc
	network           *network
	rootCfg           config.Config
	lastTabsClick     time.Time
	clickCount        int
	lastResizeW       int
	lastResizeH       int
	chosenEditor      string
	telemetryEnabled  bool
	prompter          bootstrapPrompter
	closingPreIDE     bool
	recent            *recentWorkspaces
	// quickMenu is the configured native quick menu. It is parsed once
	// per config load because the reserved grid column it implies is
	// fixed at ide.New time.
	quickMenu []ide.QuickMenuButton
	// quickMenuOff is set while the user has toggled the bar off with
	// the quickmenu command.
	quickMenuOff bool
}

func newBootstrapHandler(
	dataDir, configPath, workspace, zdotDir string,
	filenames, launchCmd []string,
	runner ide.ExtensionsRunner,
	mu *sync.Mutex,
	publishEvent func(term.Event) bool,
	openBrowser func(*url.URL) error,
	clip clipboard.Register,
	installBackupDir string,
	rootCfg config.Config,
	trust *pkgtrust.Store,
) (*bootstrapHandler, error) {
	bh := &bootstrapHandler{
		dataDir:          dataDir,
		storage:          newRuneStorage(dataDir),
		configPath:       configPath,
		workspace:        workspace,
		zdotDir:          zdotDir,
		filenames:        filenames,
		launchCmd:        launchCmd,
		runner:           runner,
		trust:            trust,
		mu:               mu,
		publishEvent:     publishEvent,
		openBrowser:      openBrowser,
		clip:             clip,
		installBackupDir: installBackupDir,
	}
	bh.recent = newRecentWorkspaces(bh.storage)
	bh.loadQuickMenu()
	bh.rootCfg = rootCfg

	if isBootstrapped(dataDir) {
		client, releaseManager := newAPIClient(bh.storage, installBackupDir, rootCfg)
		bh.network = newNetwork(rootCfg, dataDir, newNetworkGate(client))
		bh.network.startAutoJoin()
		realIDE, err := bh.buildConfiguredIDE(client, releaseManager, false)
		if err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("build configured ide: %w", err)
		}
		bh.client = client
		bh.realIDE = realIDE
		bh.inner = realIDE.Ready()
		return bh, nil
	}

	preIDE, err := bh.buildPreIDE()
	if err != nil {
		return nil, fmt.Errorf("new pre-config ide: %w", err)
	}
	bh.preIDE = preIDE
	bh.prompter = preIDE
	bh.inner = preIDE.Ready()
	bh.openBootstrapFlow()
	return bh, nil
}

func isBootstrapped(dataDir string) bool {
	if _, err := os.Stat(filepath.Join(dataDir, configFilename)); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dataDir, configStarFilename)); err == nil {
		return true
	}
	return false
}

func (b *bootstrapHandler) buildPreIDE() (*ide.IDE, error) {
	opts := []ide.Option{
		ide.WithExtensionsRunner(b.runner),
		ide.WithLocker(b.mu),
		ide.WithDefaultConfigStarlark(defaultStarlarkConfig, true, false),
		ide.WithTabBarOffset(13),
		ide.WithRightInset(b.quickMenuCells()),
		ide.WithTabBarHeight(2),
		ide.WithWorkspacesBarHeight(2),
		ide.WithWorkspacesBarOffset(2),
		ide.WithWorkspacesIcon('1'),
		ide.WithWorkspacesBarFrame(false),
		ide.WithStreamingOpen(true),
		ide.WithBell(func() {}),
		ide.WithPublishEvent(b.publishEvent),
		ide.WithScheduleNextTick(b.scheduleNextTick),
		ide.WithZdotDir(b.zdotDir),
		ide.WithTabsClickCallback(b.handleTabsClick),
	}
	preIDE, err := ide.New("", b.configPath, b.dataDir, b.trust, b.storage, opts...)
	if err != nil {
		return nil, err
	}
	b.applyInitialThemeAttr(preIDE)
	return preIDE, nil
}

func (b *bootstrapHandler) buildConfiguredIDE(
	client *apiclient.Client, releaseManager release.Manager,
	startingTutorial bool,
) (*ide.IDE, error) {
	opts := []ide.Option{
		ide.WithExtensionsRunner(b.runner),
		ide.WithInitShader(initShader, initShaderFPS, initShaderDuration),
		ide.WithShutdownShader(shutdownShader, shutdownShaderFPS, shutdownShaderDuration),
		ide.WithLoadingShader(loadingShader, loadingShaderFPS, loadingShaderDuration),
		ide.WithOpenShader(openShader, openShaderFPS, openShaderDuration),
		ide.WithStreamingOpen(true),
		ide.WithLocker(b.mu),
		ide.WithConfigFilename(workspaceConfigFilename),
		ide.WithDefaultWallpaper(makeThemedWallpaper(b.wallpaperTheme)),
		ide.WithTabBarOffset(13),
		ide.WithRightInset(b.quickMenuCells()),
		ide.WithTabBarHeight(2),
		ide.WithWorkspacesBarHeight(2),
		ide.WithWorkspacesBarOffset(2),
		ide.WithWorkspacesIcon('1'),
		ide.WithWorkspacesBarFrame(false),
		ide.WithDefaultConfigStarlark(defaultStarlarkConfig, true, false),
		ide.WithBell(func() {}),
		ide.WithPublishEvent(b.publishEvent),
		ide.WithScheduleNextTick(b.scheduleNextTick),
		ide.WithZdotDir(b.zdotDir),
		ide.WithScheme(docsScheme, newDocsSchemeFunc(b.configPath)),
		ide.WithTabsClickCallback(b.handleTabsClick),
		ide.WithPackageConfigMergeHook(b.guiEnvLiveApplyHook),
		ide.WithDispatchOnPreview(cmdSetTheme,
			func(cmd string, args ...string) (component.Responsive, func(), bool) {
				if cmd != cmdSetTheme || b.g == nil {
					return nil, nil, false
				}
				if len(args) == 0 {
					return nil, nil, false
				}
				theme := b.g.Theme()
				_, err := b.g.SetTheme(args[0])
				if err != nil {
					return nil, nil, false
				}
				return nil, func() {
					theme, err := b.g.SetTheme(theme)
					if err == nil && b.realIDE != nil {
						b.realIDE.SetDefaultAttributes(term.Attributes{
							Fg: term.FromTcellColor(theme.Foreground),
							Bg: term.FromTcellColor(theme.Background),
						})
					}
				}, true
			}),
	}
	opts = append(opts, embeddedTutorialOptions()...)
	if startingTutorial {
		opts = append(opts, ide.WithStartingTutorial("basics"))
	}
	if debug.DebugBuild == "true" {
		opts = append(opts, ide.WithDebugCommands(true))
	}
	if *flagNoSessionReopen {
		opts = append(opts, ide.WithoutSessionReopen())
	}
	opts = append(opts,
		ide.WithReleaseManager(releaseManager),
		nagPromptOption(client),
		ide.WithWatchedFilesChangeHook(client.RecordWatchedFilesChange),
		ide.WithCommandDispatchHook(client.RecordCommand),
		b.network.completerOption(),
	)
	realIDE, err := ide.New(b.workspace, b.configPath, b.dataDir,
		b.trust, b.storage, opts...)
	if err != nil {
		return nil, err
	}
	b.applyInitialThemeAttr(realIDE)
	return realIDE, nil
}

func (b *bootstrapHandler) attachGUI(g *gui.GUI, transparentWindow bool) {
	b.g = g
	b.transparentWindow = transparentWindow
	b.publishQuickMenuInstall()
}

// loadQuickMenu re-reads the configured quick menu. It loads the config
// standalone because the reserved grid column must be known before
// ide.New, and again after the bootstrap wizard writes the user config.
// A validation error still yields a usable tree with the offending
// entries neutralised, and the IDE reports it to the user, so the valid
// buttons are kept rather than dropping the whole bar.
func (b *bootstrapHandler) loadQuickMenu() {
	cfg, err := ide.Config(b.configPath, runeDefaultConfig())
	if err != nil {
		log.Warnf("load config for quick menu: %v", err)
	}
	b.quickMenu = ide.QuickMenuButtons(cfg)
}

// setupPreIDE registers the GUI command family on the pre-config IDE
// that is active during first-run bootstrap, so that commands such as
// guifontsize resolve instead of failing with "unknown command". The
// configured IDE gets them separately via setupConfiguredIDE after the
// swap. Must run after attachGUI so b.g is set.
func (b *bootstrapHandler) setupPreIDE() error {
	return errors.Join(
		subscribeGUICommands(b.g, b.preIDE, b.transparentWindow, b.launchCmd),
		b.subscribeQuickMenuCommand(b.preIDE),
	)
}

// wallpaperTheme reports the live GUI theme name for the themed wallpaper.
// It returns "" before the GUI is attached, which selects the default logo.
func (b *bootstrapHandler) wallpaperTheme() string {
	if b.g == nil {
		return ""
	}
	return b.g.Theme()
}

// applyInitialThemeAttr must run before Ready(): Ready() captures
// defAttr to materialize the init shader, so a later
// SetDefaultAttributes would not propagate into the running shader
// (RUNE-203). Seeding both the pre-bootstrap and configured IDEs
// with the same attrs also avoids a color jump across performSwap.
//
// The IDE is already running async workspace builds by the time it is
// returned, and those read the state the setter writes, so the seed
// takes the event loop lock like a loop iteration would.
func (b *bootstrapHandler) applyInitialThemeAttr(i *ide.IDE) {
	b.initialThemeAttr = resolveInitialThemeAttr(i.Browser(), i.Config())
	b.mu.Lock()
	defer b.mu.Unlock()
	i.SetDefaultAttributes(b.initialThemeAttr)
}

func (b *bootstrapHandler) browser() browser.Browser {
	if b.realIDE != nil {
		return b.realIDE.Browser()
	}
	return b.preIDE.Browser()
}

// currentIDE is the IDE the bootstrap handler is currently delegating
// to, which is the pre-config one until the wizard swaps it out.
func (b *bootstrapHandler) currentIDE() *ide.IDE {
	if b.realIDE != nil {
		return b.realIDE
	}
	return b.preIDE
}

// dragObserver routes host file drags to the focused browser, so the
// window under the cursor previews the drop and receives the dropped
// paths regardless of which window holds the focus.
func (b *bootstrapHandler) dragObserver(ev gui.DragEvent) {
	target := b.browser()
	switch ev.Kind {
	case gui.DragHover:
		target.DragHover(ev.Pos)
	case gui.DragLeave:
		target.DragCancel()
	case gui.DragDrop:
		target.DragDrop(ev.Pos, ev.Paths)
	}
}

// linkObserver opens a URL the user meta-clicked in the rendered frame.
// Workspace files open in the editor; anything else goes to the system
// browser.
func (b *bootstrapHandler) linkObserver(u *url.URL) {
	if u.Scheme == "file" {
		uri, err := workspaceapi.ParseURI(u.String())
		if err != nil {
			b.notifyError("open link", err)
			return
		}
		if err := b.currentIDE().Open(uri); err != nil {
			b.notifyError("open link", err)
		}
		return
	}
	if err := b.openBrowser(u); err != nil {
		b.notifyError("open link", err)
	}
}

func (b *bootstrapHandler) notifications() browserapi.Notifications {
	return bootstrapNotifications{b: b}
}

// notifyError surfaces a bootstrap-flow failure to the user. The
// bootstrap UI has no log pane, so log-only reporting is invisible.
func (b *bootstrapHandler) notifyError(context string, err error) {
	log.Errorf("bootstrap %s: %v", context, err)
	_, nerr := b.notifications().Notify(browserapi.LevelError, "%s: %v", context, err)
	if nerr != nil {
		log.Warnf("bootstrap %s: notify: %v", context, nerr)
	}
}

func (b *bootstrapHandler) alreadyBootstrapped() bool {
	return b.realIDE != nil
}

func (b *bootstrapHandler) config() config.Config {
	if b.realIDE != nil {
		return b.realIDE.Config()
	}
	return b.preIDE.Config()
}

func (b *bootstrapHandler) guiEnvLiveApplyHook(
	event idepkg.ConfigMergeEvent,
) (idepkg.ConfigMergeResult, error) {
	if !event.TouchesPath("gui", "env") {
		return idepkg.ConfigMergeResult{}, nil
	}
	rootCfg, err := ide.Config(b.configPath, runeDefaultConfig())
	if err != nil {
		return idepkg.ConfigMergeResult{}, fmt.Errorf("reload config for gui.env: %w", err)
	}
	guiCfg, ok, err := getGUIConfig(rootCfg)
	if err != nil {
		return idepkg.ConfigMergeResult{}, fmt.Errorf("load gui config: %w", err)
	}
	if !ok {
		return idepkg.ConfigMergeResult{}, nil
	}
	if env, err := getGUIEnvVars(guiCfg); err != nil {
		return idepkg.ConfigMergeResult{}, fmt.Errorf("decode gui.env: %w", err)
	} else if err := applyGUIEnvVars(env); err != nil {
		return idepkg.ConfigMergeResult{}, fmt.Errorf("apply gui.env: %w", err)
	}
	return idepkg.ConfigMergeResult{LiveApplied: true}, nil
}

func (b *bootstrapHandler) setupConfiguredIDE(
	i *ide.IDE, client *apiclient.Client,
) error {
	b.mu.Lock()
	i.SetDefaultAttributes(b.initialThemeAttr)
	b.mu.Unlock()

	b.client = client
	scheduleCrashReportCheck(i, client, b.dataDir, b.scheduleNextTick)

	var errs []error
	if err := subscribeCommands(b.g, client, i,
		b.transparentWindow, b.configPath, b.launchCmd); err != nil {
		errs = append(errs, fmt.Errorf("subscribe to GUI commands: %w", err))
	}
	if err := b.subscribeQuickMenuCommand(i); err != nil {
		errs = append(errs, fmt.Errorf("subscribe quick menu command: %w", err))
	}

	if client.TelemetryEnabled() {
		err := i.SubscribeEvents(apiclient.TelemetryEvents(), client)
		if err != nil {
			errs = append(errs, fmt.Errorf("subscribe telemetry events: %w", err))
		}
	}

	openFiles(i, b.filenames)

	upgradeCtx, upgradeCancel := context.WithCancel(context.Background())
	b.upgradeCancel = upgradeCancel
	b.upgradeMgr = scheduleUpgradeCheck(upgradeCtx, i,
		apiclient.DefaultDownloadsHost, b.scheduleNextTick)
	if err := registerUpgradeCommand(i, b.upgradeMgr); err != nil {
		errs = append(errs, fmt.Errorf("register upgrade command: %w", err))
	}
	if err := b.network.register(i, b.scheduleNextTick); err != nil {
		errs = append(errs, fmt.Errorf("register network: %w", err))
	}
	return errors.Join(errs...)
}

func (b *bootstrapHandler) scheduleNextTick(fn func()) bool {
	return b.publishEvent(term.Event{Type: term.EventInterrupt, UserFunc: fn})
}

// handleTabsClick maximizes the GUI window on a double-click of the
// tabs bar. Installed on both buildPreIDE and buildConfiguredIDE so
// the affordance is available during the bootstrap UI as well as the
// configured IDE.
func (b *bootstrapHandler) handleTabsClick(_ int) bool {
	if b.clickCount == 0 || time.Since(b.lastTabsClick) < doubleClickTimeout {
		b.clickCount++
	} else {
		b.clickCount = 1
	}
	b.lastTabsClick = time.Now()
	if b.clickCount == 2 && b.g != nil {
		b.g.MaximizeWindow()
		return true
	}
	return false
}

func (b *bootstrapHandler) Resize(w, h int) {
	b.lastResizeW = w
	b.lastResizeH = h
	b.inner.Resize(w, h)
	b.positionQuickMenu(w)
}

// positionQuickMenu keeps the native quick menu aligned with the grid
// column browser.Config.RightInset reserves for it. GUI resizes run on
// the main thread, which is where AppKit requires the frame change.
func (b *bootstrapHandler) positionQuickMenu(width int) {
	cells := b.quickMenuCells()
	if cells == 0 || width <= cells || b.g == nil {
		return
	}
	x, y, w, _ := b.g.CellRect(width-cells, quickMenuTopCell, cells, 1)
	glassbar.SetFrame(x, y, w)
}

func (b *bootstrapHandler) Draw(w term.Writer) { b.inner.Draw(w) }

func (b *bootstrapHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return b.inner.Cursor()
}

func (b *bootstrapHandler) Selection() (string, bool) {
	return b.inner.Selection()
}

func (b *bootstrapHandler) Handle(ev term.Event) (exit, handled bool) {
	if b.realIDE == nil {
		if b.adjustBootstrapFontSize(ev) {
			return false, true
		}
		if isBootstrapQuitEvent(ev) {
			return true, true
		}
		if shouldSwallowBootstrapEvent(ev) {
			return false, true
		}
	}
	return b.inner.Handle(ev)
}

func (b *bootstrapHandler) performSwap() error {
	if err := b.writePresetConfig(); err != nil {
		return fmt.Errorf("write bootstrap preset: %w", err)
	}

	b.configPath = filepath.Join(b.dataDir, configFilename)
	b.loadQuickMenu()

	b.mu.Unlock()
	defer b.mu.Lock()

	rootCfg := config.MapConfig(map[string]any{
		"editor":    map[string]any{"mode": b.chosenEditor},
		"telemetry": map[string]any{"enabled": b.telemetryEnabled},
	})
	client, releaseManager := newAPIClient(b.storage, b.installBackupDir, rootCfg)
	// The network gates on the account, so it exists only from the
	// moment the API client that vouches for it does.
	b.network = newNetwork(b.rootCfg, b.dataDir, newNetworkGate(client))
	b.network.startAutoJoin()
	realIDE, err := b.buildConfiguredIDE(client, releaseManager, true)
	if err != nil {
		_ = client.Close()
		return fmt.Errorf("build configured ide: %w", err)
	}
	b.realIDE = realIDE
	setupErr := b.setupConfiguredIDE(realIDE, client)
	b.inner = realIDE.Ready()
	if b.lastResizeW > 0 && b.lastResizeH > 0 {
		b.inner.Resize(b.lastResizeW, b.lastResizeH)
	}

	var closeErr error
	if b.preIDE != nil {
		b.closingPreIDE = true
		if cerr := b.preIDE.Close(); cerr != nil {
			closeErr = fmt.Errorf("close pre-config ide: %w", cerr)
		}
		b.preIDE = nil
	}
	b.publishAppMenuInstall()
	b.publishQuickMenuInstall()
	return errors.Join(setupErr, closeErr)
}

// installAppMenu replaces the application menu bar. It must run on the
// main thread, so callers publish it as an EventInterrupt rather than
// calling it directly.
func (b *bootstrapHandler) installAppMenu() {
	appmenu.Install(
		appMenus(appMenuKeyBindings(b.config()), b.mergedRecentWorkspaces(),
			b.appMenuModels(), b.appMenuTutorials(), appMenuQuickMenu{
				available: b.quickMenuAvailable(),
				visible:   b.quickMenuVisible(),
			}),
		b.activateAppMenuCommand)
}

func (b *bootstrapHandler) appMenuModels() []string {
	if b.realIDE == nil {
		return nil
	}
	it := b.realIDE.Models()
	defer func() { _ = it.Close() }()
	var models []string
	for {
		model, ok := it.Next(context.Background())
		if !ok {
			break
		}
		models = append(models, model.Provider+"/"+model.Name)
	}
	slices.Sort(models)
	return slices.Compact(models)
}

func (b *bootstrapHandler) appMenuTutorials() []string {
	if b.realIDE == nil {
		return nil
	}
	return b.realIDE.TutorialNames()
}

// mergedRecentWorkspaces combines the projects opened through the app
// menu with those opened from the command prompt into one recency-
// ordered, de-duplicated, disambiguated list for the Open Recent menu.
// Menu opens lead because they are the most recent user action taken
// through the menu itself.
func (b *bootstrapHandler) mergedRecentWorkspaces() []recentEntry {
	paths := b.recent.paths()
	if b.realIDE != nil {
		paths = append(paths, b.realIDE.RecentWorkspaceOpens()...)
	}
	entries := recentLabels(paths)
	if len(entries) > recentMenuLimit {
		entries = entries[:recentMenuLimit]
	}
	return entries
}

// publishAppMenuInstall schedules a menu (re)install on the main
// thread. Bindings change when the bootstrap wizard writes the chosen
// editor preset, so the menu is rebuilt after the IDE swap.
func (b *bootstrapHandler) publishAppMenuInstall() {
	b.publishEvent(term.Event{Type: term.EventInterrupt, UserFunc: b.installAppMenu})
}

// publishQuickMenuInstall installs the native quick menu on the main
// thread, once the application window exists. Installing also replays
// the last frame, and repositioning here covers the install landing
// before the first Resize, which would otherwise leave the bar
// invisible until the window was resized.
func (b *bootstrapHandler) publishQuickMenuInstall() {
	b.publishEvent(term.Event{Type: term.EventInterrupt, UserFunc: func() {
		glassbar.Install(quickMenuGlassButtons(b.quickMenuButtons()),
			b.activateQuickMenuButton)
		b.positionQuickMenu(b.lastResizeW)
	}})
}

func (b *bootstrapHandler) activateQuickMenuButton(id string) {
	for _, button := range b.quickMenu {
		if button.ID() != id {
			continue
		}
		b.activateAppMenuCommand(appmenu.Command{
			Title:   button.Title,
			Command: button.Command[0],
			Args:    button.Command[1:],
			Key:     appMenuKeyBindings(b.config())[id],
		})
		return
	}
}

func (b *bootstrapHandler) activateAppMenuCommand(cmd appmenu.Command) {
	// An Open Recent item is a panel-table command carrying an explicit
	// path, so it must dispatch straight to the IDE instead of opening
	// the panel to ask for one.
	if _, ok := appMenuPanelCommands[cmd.Command]; ok && len(cmd.Args) > 0 {
		b.activateRecentCommand(cmd)
		return
	}
	// The panel table otherwise takes precedence over chord replay: a
	// menu click must open the native panel even when the command is
	// bound.
	if opts, ok := appMenuPanelCommands[cmd.Command]; ok {
		b.activateOpenPanel(cmd, opts)
		return
	}
	// Replaying the chord runs the command through the full handler
	// chain, so authorization, macros, tutorials and the confirm-exit
	// prompt behave exactly as they do for a real keypress.
	if cmd.Key != (term.KeyComb{}) {
		b.publishEvent(term.Event{
			Type: term.EventKey,
			Mod:  cmd.Key.Mod,
			Key:  cmd.Key.Key,
			Ch:   cmd.Key.Ch,
		})
		return
	}
	b.publishEvent(term.Event{Type: term.EventInterrupt, UserFunc: func() {
		if b.realIDE == nil {
			// The wizard is still writing the configuration this
			// command would act on.
			_, _ = b.notifications().Notify(browserapi.LevelWarn,
				"%s is not available during setup", cmd.Title)
			return
		}
		_ = b.dispatchAppMenuCommand(cmd, cmd.Args...)
	}})
}

// activateRecentCommand dispatches an Open Recent item — a workspaceopen
// with a fixed path — on the main thread, recording the open so the
// menu stays ordered by recency.
func (b *bootstrapHandler) activateRecentCommand(cmd appmenu.Command) {
	b.publishEvent(term.Event{Type: term.EventInterrupt, UserFunc: func() {
		if b.realIDE == nil {
			_, _ = b.notifications().Notify(browserapi.LevelWarn,
				"%s is not available during setup", cmd.Title)
			return
		}
		if err := b.dispatchAppMenuCommand(cmd, cmd.Args...); err != nil {
			return
		}
		b.recordRecentOpen(cmd.Command, cmd.Args...)
	}})
}

// activateOpenPanel collects cmd's path argument through the native
// open panel and dispatches the command once per selected path. It
// runs on the main thread — the menu callback — which is also where
// the panel must be shown.
func (b *bootstrapHandler) activateOpenPanel(cmd appmenu.Command, opts openpanel.Options) {
	if b.realIDE == nil {
		// The wizard is still writing the configuration this
		// command would act on.
		b.publishEvent(term.Event{Type: term.EventInterrupt, UserFunc: func() {
			_, _ = b.notifications().Notify(browserapi.LevelWarn,
				"%s is not available during setup", cmd.Title)
		}})
		return
	}
	showOpenPanel(opts, func(paths []string) {
		if len(paths) == 0 {
			return
		}
		b.publishEvent(term.Event{Type: term.EventInterrupt, UserFunc: func() {
			for _, path := range paths {
				if err := b.dispatchAppMenuCommand(cmd, path); err != nil {
					continue
				}
				b.recordRecentOpen(cmd.Command, path)
			}
		}})
	})
}

// dispatchAppMenuCommand closes the command prompt, then runs cmd with
// the given args against the IDE, reporting any error as a notification.
// Menu-driven commands often open their own prompt or picker, so the
// always-on-top command prompt must be dismissed first or the new
// surface renders behind it. Runs on the main thread.
func (b *bootstrapHandler) dispatchAppMenuCommand(cmd appmenu.Command, args ...string) error {
	_ = b.realIDE.CloseCommandPrompt()
	if err := b.realIDE.DispatchCommand(cmd.Command, args...); err != nil {
		_, _ = b.notifications().Notify(browserapi.LevelError,
			"%s: %v", cmd.Title, err)
		return err
	}
	return nil
}

// recordRecentOpen remembers a successful project open so the Open
// Recent menu reflects it, and republishes the menu. Only workspaceopen
// feeds the recent list; other panel commands (edit, view) open files,
// not projects.
func (b *bootstrapHandler) recordRecentOpen(command string, args ...string) {
	if command != cmdWorkspaceOpen || len(args) == 0 {
		return
	}
	b.recent.record(args[0])
	b.publishAppMenuInstall()
}

func (b *bootstrapHandler) writePresetConfig() error {
	body, err := renderPreset(b.chosenEditor, b.telemetryEnabled)
	if err != nil {
		return fmt.Errorf("render preset: %w", err)
	}
	path := filepath.Join(b.dataDir, configFilename)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write preset config %q: %w", path, err)
	}
	return nil
}

func (b *bootstrapHandler) Close() error {
	b.closingPreIDE = true
	var errs []error
	if b.network != nil {
		if err := b.network.Close(); err != nil {
			errs = append(errs, err)
		}
		b.network = nil
	}
	if b.upgradeMgr != nil {
		if err := b.upgradeMgr.Close(); err != nil {
			errs = append(errs, err)
		}
		b.upgradeMgr = nil
	}
	if b.upgradeCancel != nil {
		b.upgradeCancel()
		b.upgradeCancel = nil
	}
	if b.client != nil {
		if err := b.client.Close(); err != nil {
			errs = append(errs, err)
		}
		b.client = nil
	}
	if b.realIDE != nil {
		if err := b.realIDE.Close(); err != nil {
			errs = append(errs, err)
		}
		b.realIDE = nil
	}
	if b.preIDE != nil {
		if err := b.preIDE.Close(); err != nil {
			errs = append(errs, err)
		}
		b.preIDE = nil
	}
	if b.storage != nil {
		if err := b.storage.Close(); err != nil {
			errs = append(errs, err)
		}
		b.storage = nil
	}
	return errors.Join(errs...)
}

const (
	editorModal    = "modal"
	editorHelix    = "helix"
	editorStandard = "standard"
	editorEmacs    = "emacs"
	// editorModeless is the deprecated alias for editorStandard, kept so
	// existing configs and callers keep working.
	editorModeless = "modeless"
)

// Editor option labels double as map keys in optionToChoice; they must
// stay byte-identical between the prompt and the callback.
const (
	optVimYes   = "    vim    "
	optHelix    = "   helix   "
	optStandard = " standard "
	optEmacs    = "   emacs   "
)

var (
	bootstrapVimKeys = []term.KeyComb{
		{Ch: 's'}, {Ch: 'e'}, {Ch: 'v'}, {Ch: 'h'},
	}

	bootstrapWelcomeKeys = []term.KeyComb{
		{Ch: 'g'},
	}
)

const (
	optWelcomeGo = " Let's go "
)

const (
	optTelemetryYes = " yes "
	optTelemetryNo  = " no thanks "
)

var (
	bootstrapTelemetryKeys = []term.KeyComb{
		{Ch: 'y'}, {Ch: 'n'},
	}
)

func (b *bootstrapHandler) openBootstrapFlow() {
	b.openWelcomePrompt()
}

// metaKeySymbol names the physical key that produces <meta> chords on
// the user's platform, matching ide.metaKeyName. On macOS <meta> is the
// Command key (⌘); elsewhere it is the Windows or Super key.
func metaKeySymbol() string {
	if runtime.GOOS == "darwin" {
		return "\u2318"
	}
	return "the Windows or Super key"
}

func (b *bootstrapHandler) prompt(
	message string,
	options []string,
	bindings []term.KeyComb,
	h sdkhandler.PromptHandler,
) browser.Window {
	return b.prompter.Prompt(message, options, bindings, h)
}

func (b *bootstrapHandler) openWelcomePrompt() {
	msg := "## Welcome to Rune\n\n" +
		"Glad you're here. Let's get everything set up.\n\n" +
		"Learning a new editor is hard, and it can feel daunting at first. We've all been there. " +
		"These first steps are designed to make that process easier, and we promise that once Rune starts to click, " +
		"the payoff will be huge.\n\n" +
		"If this text is too small, press " + metaKeySymbol() + " and `=` to make the font bigger; " +
		"if it's too big, press " + metaKeySymbol() + " and `-` to make it smaller."
	guard := b.promptGuard()
	b.prompt(
		msg,
		[]string{optWelcomeGo},
		bootstrapWelcomeKeys,
		sdkhandler.FuncPromptHandler(
			guard.onSelect(func(_ int, _ string) {
				b.openVimPrompt()
			}),
			guard.onClose(b.openWelcomePrompt),
		),
	)
}

func (b *bootstrapHandler) openVimPrompt() {
	msg := "## Choose your key bindings\n" +
		"Rune ships with four built-in editors, so pick the one that feels like home.\n\n" +
		"Know vim? Pick **vim** and you get it **everywhere**, not just in editor " +
		"buffers: the terminal, input boxes, and the file explorer.\n\n" +
		"Used to VS Code, Cursor, Sublime or a plain text editor? Pick **standard** and Rune uses " +
		"those familiar, standard key bindings everywhere instead.\n\n" +
		"Prefer Emacs? Pick **emacs** for an Emacs-style keymap everywhere.\n\n" +
		"Coming from Helix? Pick **helix** for its selection-first grammar, " +
		"where a motion picks the target and the operator acts on it.\n\n" +
		"**Which key bindings do you want?**"
	guard := b.promptGuard()
	b.prompt(
		msg,
		[]string{optStandard, optEmacs, optVimYes, optHelix},
		bootstrapVimKeys,
		sdkhandler.FuncPromptHandler(
			guard.onSelect(func(_ int, option string) {
				b.chosenEditor = optionToChoice(option)
				b.openTelemetryPrompt()
			}),
			guard.onClose(b.openVimPrompt),
		),
	)
}

func (b *bootstrapHandler) openTelemetryPrompt() {
	msg := "## Help us pick what to build next\n\n" +
		"Rune reports a small amount of anonymous usage data. The most useful " +
		"signal is which languages people actually edit, and it is how we decide " +
		"which language support to build next.\n\n" +
		"We never send file names, paths, file contents, terminal output, or " +
		"anything you type.\n\n" +
		"You can change this any time in your config. A complete report " +
		"and sample payloads are on the Telemetry page in the docs."
	guard := b.promptGuard()
	b.prompt(
		msg,
		[]string{optTelemetryYes, optTelemetryNo},
		bootstrapTelemetryKeys,
		sdkhandler.FuncPromptHandler(
			guard.onSelect(func(_ int, option string) {
				b.telemetryEnabled = telemetryOptionToChoice(option)
				b.scheduleNextTick(func() {
					if err := b.performSwap(); err != nil {
						b.notifyError("finish bootstrap", err)
					}
				})
			}),
			guard.onClose(b.openTelemetryPrompt),
		),
	)
}

// guardedPromptChain re-opens a bootstrap prompt that was closed
// without a user selection (Esc, mouse dismissal, …). The SDK
// invokes OnSelect synchronously before OnClose, so "advanced"
// reliably distinguishes the two paths.
type guardedPromptChain struct {
	advanced bool
	closing  func() bool
}

func (g *guardedPromptChain) onSelect(next func(int, string)) func(int, string) {
	return func(idx int, option string) {
		g.advanced = true
		next(idx, option)
	}
}

func (g *guardedPromptChain) onClose(reopen func()) func() error {
	return func() error {
		if !g.advanced && !g.closing() {
			reopen()
		}
		return nil
	}
}

// promptGuard builds the chain guard for a bootstrap prompt. The
// closing check stops the reopen-on-dismiss chain while the preIDE
// is being torn down (shutdown or swap to the configured IDE), where
// every window close fires OnClose and reopening would resurrect
// prompt windows mid-Close forever.
func (b *bootstrapHandler) promptGuard() *guardedPromptChain {
	return &guardedPromptChain{closing: func() bool { return b.closingPreIDE }}
}

// bootstrapFontSizeDelta reports the font-size adjustment ev requests,
// mirroring the editor presets' <m-=> / <m--> guifontsize bindings.
// Those presets are only written at the end of bootstrap, so the
// bindings the welcome prompt tells the user to press do not exist yet
// and the chords have to be recognized here. The shifted forms count
// too because the prompt copy says "+" and "-".
func bootstrapFontSizeDelta(ev term.Event) int {
	if ev.Type != term.EventKey || ev.Mod&term.ModMeta == 0 {
		return 0
	}
	switch ev.Ch {
	case '=', '+':
		return 1
	case '-', '_':
		return -1
	}
	return 0
}

func (b *bootstrapHandler) adjustBootstrapFontSize(ev term.Event) bool {
	delta := bootstrapFontSizeDelta(ev)
	if delta == 0 || b.g == nil {
		return false
	}
	var err error
	if delta > 0 {
		err = b.g.IncreaseFontSize()
	} else {
		err = b.g.DecreaseFontSize()
	}
	if err != nil {
		b.notifyError("adjust font size", err)
	}
	return true
}

// isBootstrapQuitEvent reports whether ev is the quit chord bound by
// every shipped editor preset. During bootstrap nothing is open and
// nothing is unsaved, so it exits without a confirmation prompt.
func isBootstrapQuitEvent(ev term.Event) bool {
	return ev.Type == term.EventKey && ev.Ch == 'q' && ev.Mod&term.ModMeta != 0
}

// shouldSwallowBootstrapEvent must NOT swallow Esc: the SDK prompt
// relies on Esc to exit, and guardedPromptChain.onClose re-opens
// the prompt right after.
func shouldSwallowBootstrapEvent(ev term.Event) bool {
	if ev.Type != term.EventKey {
		return false
	}
	// preIDE only loads embedded defaults — no user config exists
	// yet — so ':' is hardcoded as the command-prompt activation key.
	if ev.Mod == 0 && ev.Ch == ':' {
		return true
	}
	// Close keybindings from the editor presets. Closing the
	// pre-config IDE's only window would break the wizard:
	//   <m-w> windowclose, <a-w> tabclose, <c-w> tabclose
	//   <m-s-w> / <s-m-w> windowclose (standard overrides)
	if ev.Ch == 'w' && ev.Mod != 0 {
		switch {
		case ev.Mod&term.ModMeta != 0,
			ev.Mod&term.ModAlt != 0,
			ev.Mod&term.ModCtrl != 0:
			return true
		}
	}
	return false
}

func optionToChoice(option string) string {
	switch option {
	case optStandard:
		return editorStandard
	case optEmacs:
		return editorEmacs
	case optHelix:
		return editorHelix
	}
	return editorModal
}

func telemetryOptionToChoice(option string) bool {
	return option == optTelemetryYes
}
