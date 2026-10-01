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

package ide

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"time"

	multierr "github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/ide/idenag"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacessh"
)

// IDE encapsulates the ability to run an IDE within a TUI session.
type IDE struct {
	ideConfig
	options
	locker           sync.Locker
	workspaceManager *workspace.Manager
	workspaceHandler *workspaceManagerHandler
	root             shaderRunner
	tutorial         tutorialRunner
	tutorialsConfig  tutorialsConfig
	nag              *idenag.Scheduler
	publishEventFn   EventPublisher
	storage          storageapi.Service
}

// provisionManifest builds the encoded package-provisioning manifest passed
// to a remote `rune -x` server so it mirrors the local toolchain. It returns
// an empty string (skip provisioning) when the package manager is not yet
// initialized or has no in-use packages.
func (i *IDE) provisionManifest() string {
	if i.workspaceHandler == nil {
		return ""
	}
	pm := i.workspaceHandler.pkgmanager
	if pm == nil || pm.pkg == nil {
		return ""
	}
	entries, err := idepkg.BuildProvisionManifest(context.Background(), pm.pkg)
	if err != nil {
		log.Warnf("build remote provision manifest: %v", err)
		return ""
	}
	return idepkg.EncodeProvisionManifest(entries)
}

// EventPublisher is a function that publishes the given event back
// into the event loop. It should be safe for concurrent use.
type EventPublisher func(term.Event) bool

// New allocates storage for a new IDE and initializes it with config
// at cfgfilename and filename. Note that if filename is empty, a default inmutable
// buffer will be loaded.
//
// storage is borrowed, not owned: the caller retains ownership and must
// close it. IDE.Close does not close storage, so a single storage handle
// may be shared across IDEs (as cmd/rune's bootstrap swap does) without one
// IDE's shutdown tearing it down underneath another.
func New(
	cwd, cfgfilename, dataDir string, trust *pkgtrust.Store,
	storage storageapi.Service, opts ...Option,
) (i *IDE, err error) {
	i = new(IDE)
	err = i.init(cwd, cfgfilename, dataDir, trust, storage, opts...)
	return
}

// Config loads and returns the IDE configuration without starting workspaces.
//
// def is required: it is the baseline the user config is overlaid onto and
// must match the default the running IDE uses, so subscript overrides in the
// user config (config["terminal"][...] = ...) resolve against the full tree.
func Config(cfgfilename string, def DefaultConfig, opts ...Option) (config.Config, error) {
	op := newOptions(append([]Option{def.option()}, opts...)...)
	cfg, err := loadIDEConfig(cfgfilename, op)
	return config.MapConfig(cfg.cfg), err
}

// ConfigWithOverlays loads the IDE config from cfgfilename (like Config) and
// then overlays each file in overlayFiles, in order, reading them through cwd
// (so a remote workspace resolves them over its RPC file scheme). Missing or
// permission-denied overlay files are silently skipped; a malformed overlay
// returns an error. It is the exported entry point for callers outside this
// package (the remote `rune -x` server) that must layer a workspace-root or
// remote-home config over the base config before applying gui.env.
func ConfigWithOverlays(
	cfgfilename string, def DefaultConfig, cwd workspace.Workspace,
	overlayFiles []string, opts ...Option,
) (config.Config, error) {
	op := newOptions(append([]Option{def.option()}, opts...)...)
	cfg, err := loadIDEConfig(cfgfilename, op)
	if err != nil {
		return config.MapConfig(cfg.cfg), err
	}
	if cwd != nil {
		for _, filename := range overlayFiles {
			if _, oErr := loadWorkspaceConfig(
				filename, cwd, workspaceapi.URI{}, &cfg,
			); oErr != nil {
				return config.MapConfig(cfg.cfg),
					fmt.Errorf("overlay config %q: %w", filename, oErr)
			}
		}
	}
	return config.MapConfig(cfg.cfg), nil
}

// DefaultConfigTree decodes def into the raw config map the editor uses as the
// predeclared `config` base when reading a package's .star config during a
// merge. Headless callers (the remote provisioning manager) pass the result to
// idepkg.NewProvisioningManager so a .star-based gui.env merge resolves against
// the same tree the editor would.
func DefaultConfigTree(def DefaultConfig) (map[string]any, error) {
	return decodeDefaultConfig(def)
}

// PkgEditorMode resolves the editor mode exposed to package config.star scripts
// (the RUNE_EDITOR_MODE predeclared global) from cfg, applying the same
// normalization the running editor uses: exo resolves to its configured
// fallback, the deprecated "modal" and "modeless" map to "vim" and
// "standard", and a missing or unreadable editor.mode defaults to "vim". A nil
// cfg yields "vim".
//
// The remote `rune -x` provisioning server has no editor UI, so it calls this
// to thread the user's mode into idepkg.NewProvisioningManager; without it, a
// package's config.star that reads RUNE_EDITOR_MODE fails to decode with
// "undefined: RUNE_EDITOR_MODE".
func PkgEditorMode(cfg config.Config) string {
	var raw map[string]any
	if cfg != nil {
		if editor, err := cfg.GetMap("editor"); err == nil {
			raw = map[string]any{"editor": editor}
		}
	}
	c := ideConfig{cfg: raw, errors: map[string]error{}}
	return c.pkgEditorMode()
}

// Interrupt satisfies term.Interrupter
func (i *IDE) Interrupt(ctx context.Context) error {
	return i.workspaceHandler.Interrupt(ctx)
}

// SubscribeCommand subscribes the given handler in calls to the given cmd,
// or returns an error if there's already a CommandHandler
// installed for this command.
//
// The command will be automatically installed to all active
// and future workspaces.
func (c *IDE) SubscribeCommand(
	cmd textapi.CommandManual, handler text.CommandHandler,
) error {
	return c.workspaceHandler.subscribeCommand(cmd, handler)
}

// RegisterREPLCommand registers the given handler as a top-level REPL
// command in the IDE shell, or returns an error if a REPL command with
// the same name is already registered.
//
// The command will be automatically installed to all active
// and future workspaces.
func (c *IDE) RegisterREPLCommand(
	cmd textapi.CommandManual, handler textapi.REPLHandler,
) error {
	return c.workspaceHandler.registerREPLCommand(cmd, handler)
}

// SubscribeEvents subscribes the given handler to all the given events,
// or returns an error if there's an error while subscribing it.
//
// The command will be automatically installed to all active
// and future workspaces.
func (c *IDE) SubscribeEvents(
	events []textapi.EventType, handler text.EventHandler,
) error {
	c.workspaceHandler.mu.Lock()
	defer c.workspaceHandler.mu.Unlock()
	return c.workspaceHandler.SubscribeEvents(events, handler)
}

// UnsubscribeEvents unsubscribes the given handler to all the events.
func (c *IDE) UnsubscribeEvents(handler text.EventHandler) (bool, error) {
	c.workspaceHandler.mu.Lock()
	defer c.workspaceHandler.mu.Unlock()
	return c.workspaceHandler.UnsubscribeEvents(handler)
}

// DefaultAttributes return the default attributes to be used to fill the screen.
func (i *IDE) DefaultAttributes() term.Attributes {
	return i.root.defAttr
}

// InputMode return the configured tui input mode.
func (i *IDE) InputMode() term.InputMode {
	return i.ideConfig.inputMode()
}

// SetDefaultAttributes sets the default attributes to be used to fill the screen.
//
// It mutates state the event loop and the async workspace builds read,
// so callers must hold the IDE locker (see WithLocker). Command
// handlers already run under it; setup code running outside the loop
// has to take it explicitly.
func (i *IDE) SetDefaultAttributes(defAttr term.Attributes) {
	i.root.defAttr = defAttr
	// Propagate to every workspace ex so grayscale dimming of unfocused
	// windows and the command prompt resolves a ColorDefault foreground
	// against the current theme rather than keeping its native hue.
	i.workspaceHandler.setDefaultAttr(defAttr)
}

// Config returns the configuration loaded by this IDE.
func (i *IDE) Config() config.Config {
	return config.MapConfig(i.ideConfig.cfg)
}

// Ready returns the root Handler of this IDE,
// and marks this IDE as ready to run.
// Close must be called when this IDE is no longer in use.
func (i *IDE) Ready() tui.Handler {
	i.initRunning()
	i.maybeStartTutorial()
	i.maybeReopenLastSession()
	return &i.root
}

// tutorialInitShaderBuffer is an extra delay added on top of the init
// shader duration before the tutorial is dispatched, so the prompt
// mounts only after the shader has fully settled.
const tutorialInitShaderBuffer = 1000 * time.Millisecond

// reopenPromptInitShaderBuffer is the extra delay added on top of the
// init shader duration before the last-session reopen offer runs. It is
// shorter than tutorialInitShaderBuffer because the offer should appear
// promptly once the shader settles.
const reopenPromptInitShaderBuffer = 250 * time.Millisecond

func (i *IDE) maybeStartTutorial() {
	name := i.options.startingTutorial
	if name == "" {
		return
	}
	if _, ok := i.tutorial.tutorials[name]; !ok {
		return
	}
	dispatch := func() {
		i.options.scheduleFn(func() {
			i.workspaceHandler.focusEx().Dispatch("tutorial", "start", name)
		})
	}
	// Defer the tutorial until the init shader finishes so the shader
	// does not animate on top of the freshly-mounted tutorial prompt.
	if i.options.initShaderFn != nil && i.options.initShaderDuration > 0 {
		i.options.afterFunc(i.options.initShaderDuration+tutorialInitShaderBuffer, func() {
			debug.CapturePanicReport(dispatch)
		})
		return
	}
	dispatch()
}

// maybeReopenLastSession offers to reopen the workspaces left open by
// the previous session. A workspace requested at launch consumes the
// snapshot itself once its async install completes, so this only covers
// the bare launch.
func (i *IDE) maybeReopenLastSession() {
	if i.workspaceHandler.startupWorkspace {
		return
	}
	if len(i.workspaceHandler.lastSessionReopenTargets()) == 0 {
		return
	}
	dispatch := func() {
		i.options.scheduleFn(func() {
			i.workspaceHandler.maybeReopenLastSession()
		})
	}
	// Defer behind the init shader the same way maybeStartTutorial does
	// so the shader does not animate on top of the reopen prompt.
	if i.options.initShaderFn != nil && i.options.initShaderDuration > 0 {
		i.options.afterFunc(i.options.initShaderDuration+reopenPromptInitShaderBuffer, func() {
			debug.CapturePanicReport(dispatch)
		})
		return
	}
	dispatch()
}

// Browser returns the current browser in focus.
func (i *IDE) Browser() browser.Browser {
	return i.workspaceHandler.focusBrowser()
}

// WindowManager returns a browserapi.WindowManager that routes calls
// to the workspace currently in focus. Use this when registering UI
// elements (e.g. floating prompts) that need to remain attached to
// the focused workspace as the user switches between them.
func (i *IDE) WindowManager() browserapi.WindowManager {
	return currentWorkspaceWindowManager{root: i.workspaceHandler}
}

// Storage returns persistent storage acrosss IDE instances, given
// the same data dir passed in ide.New, or ide.NewRecovery.
func (i *IDE) Storage() storageapi.Service {
	return i.storage
}

// Size returns the current width and height in cells.
func (i *IDE) Size() (width, height int) {
	return i.root.width, i.root.height
}

// SetRightInset resizes the column reserved along the right edge for a
// UI element floating over it, relaying out whatever sits at that
// edge: the workspaces, or the tutorial tile while a lesson runs. Zero
// gives the column back. It must run on the event loop.
func (i *IDE) SetRightInset(cells int) {
	i.tutorial.setRightInset(cells)
}

// RightInset returns the width of the column currently reserved along
// the right edge.
func (i *IDE) RightInset() int {
	return i.tutorial.inset
}

// Open opens the given file, in the currently active workspace.
func (i *IDE) Open(file workspaceapi.URI) error {
	return i.workspaceHandler.openURI(file, true /* focus */)
}

// DispatchCommand runs cmd in the focused workspace, as if it had been
// typed in the command prompt. Commands that exit the IDE cannot be run
// this way: the exit signal is only observable on the handler event
// path, so those must be triggered through their key binding.
func (i *IDE) DispatchCommand(cmd string, args ...string) error {
	return i.workspaceHandler.focusEx().dispatchCommand(cmd, args...)
}

// Models returns the models available through the IDE's host-side LLM
// service. The service belongs to the IDE rather than any workspace, so it is
// available while the home workspace is focused.
func (i *IDE) Models() iterator.Iterator[llmapi.ModelEntry] {
	return i.workspaceHandler.llmRouter.Models()
}

// TutorialNames returns the registered tutorial names in display order.
func (i *IDE) TutorialNames() []string {
	names := make([]string, 0, len(i.tutorial.tutorials))
	for name := range i.tutorial.tutorials {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// CloseCommandPrompt dismisses the command prompt in the focused
// workspace if one is open, and is a no-op otherwise. Callers that
// dispatch a command opening its own prompt or picker (for example the
// native menu bar) use this first so the new surface is not hidden
// behind the always-on-top command prompt overlay.
func (i *IDE) CloseCommandPrompt() error {
	return i.workspaceHandler.focusEx().closeCommandPrompt()
}

// RegisterScheme registers a workspace scheme after construction, for
// schemes whose SchemeFunc needs the IDE itself (for example to
// prompt) and therefore cannot exist before [New] returns. Schemes
// known up front use [WithScheme] instead.
func (i *IDE) RegisterScheme(scheme string, fn schemeapi.SchemeFunc) error {
	return i.workspaceManager.RegisterScheme(scheme, fn)
}

// RecentWorkspaceOpens returns the paths previously passed to the
// workspaceopen command from the command prompt, most-recent-first and
// de-duplicated. It reflects only prompt-driven opens; menu-driven
// opens are tracked separately by the caller. Returns nil when the
// history is empty or unavailable.
func (i *IDE) RecentWorkspaceOpens() []string {
	acc := i.workspaceHandler.commandHistory
	if acc == nil {
		return nil
	}
	// The trailing empty token makes HistoryCompleter keep entries
	// beginning with "workspaceopen " and strip that prefix, yielding
	// just the path arguments.
	it, _, err := command.HistoryCompleter(acc).
		Complete(context.Background(), []string{cmdAddWorkspace, ""})
	if err != nil {
		return nil
	}
	paths, err := iterator.ToSlice(context.Background(), it)
	if err != nil {
		return nil
	}
	for idx, p := range paths {
		paths[idx] = command.UnquoteToken(p)
	}
	return paths
}

// WaitWorkspaces blocks until every async addWorkspace launched by
// the IDE has either installed its workspace or had its pending
// reservation cleaned up, and every background workspace teardown
// launched by closeWorkspace has finished. Use this after
// constructing the IDE — and
// before driving keyboard input or calling Open — when you want to
// be sure the cwd workspace is fully wired in. Production code does
// not need it: the event loop pumps install callbacks naturally as
// part of its tick. Callers must NOT hold the IDE locker (set via
// WithLocker) when invoking this — installs need to acquire it.
func (i *IDE) WaitWorkspaces() {
	i.workspaceHandler.closeWG.Wait()
	i.workspaceHandler.pendingWG.Wait()
}

// WaitInflight blocks until every active workspace's in-flight async
// save / reload awaiter goroutines have completed and their completion
// callbacks have been dispatched to the event-loop scheduler. Intended
// for tests that assert post-:write state in the same input turn (or
// during shutdown when callers need a strict drain).
//
// Callers must not hold the IDE locker when invoking this.
func (i *IDE) WaitInflight() {
	i.workspaceHandler.waitInflight()
}

// SetReleaseManager sets the release.Manager of the IDE.
// This should be called before Run or Handler are called for the first time.
func (i *IDE) SetReleaseManager(m release.Manager) {
	i.workspaceHandler.setReleaseManager(m)
}

// Notifications returns an cross-workspace, goroutine-safe implementation
// of browserapi.Notifications.
func (i *IDE) Notifications() browserapi.Notifications {
	return i.workspaceHandler.notifications.current()
}

// Prompt opens a yes/no floating prompt in the currently focused workspace.
// It returns the prompt window that can be closed by the caller.
func (i *IDE) Prompt(
	message string,
	options []string,
	bindings []term.KeyComb,
	h handler.PromptHandler,
) browser.Window {
	return i.workspaceHandler.focusEx().comp.Prompt(
		message, options, bindings, h,
	)
}

// Close satisfies io.Closer by closing this all ide's resources, including
// the terminal state.
func (i *IDE) Close() error {
	return i.closeResources()
}

func (i *IDE) closeResources() (ret error) {
	i.workspaceHandler.mu.Lock()
	defer i.workspaceHandler.mu.Unlock()

	if i.nag != nil {
		i.nag.Stop()
	}

	if err := i.workspaceHandler.Close(); err != nil {
		ret = multierr.Append(ret, err)
	}

	if err := i.workspaceManager.Close(); err != nil {
		ret = multierr.Append(ret, err)
	}

	if err := i.root.Close(); err != nil {
		ret = multierr.Append(ret, err)
	}

	if err := i.tutorial.Close(); err != nil {
		ret = multierr.Append(ret, err)
	}

	return
}

func (i *IDE) init(
	cwd, cfgfilename, dataDir string, trust *pkgtrust.Store,
	storage storageapi.Service, opts ...Option,
) error {
	if trust == nil {
		panic("ide.init: nil trust store")
	}
	op := newOptions(opts...)
	i.options = op

	defaultCfg := newDefaultConfig(op)
	var configErr error
	i.ideConfig, configErr = loadIDEConfig(cfgfilename, op)

	i.storage = storage
	i.ideConfig.storage = storageapi.WithPartition(i.storage, "ide")

	var logger *slog.Logger
	if logPath := i.ideConfig.logOutputPath(); logPath != "" {
		f, err := workspace.OpenFile(logPath,
			os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
		if err != nil {
			expanded, expandErr := workspaceapi.CurrentUserHostURI(logPath)
			if expandErr != nil {
				return multierr.Append(
					fmt.Errorf("open log file %q: %w", logPath, err),
					fmt.Errorf("make uri %q: %w", logPath, expandErr),
				)
			}
			logDir := path.Dir(expanded.Path())
			dirErr := os.MkdirAll(logDir, 0755)
			if dirErr != nil {
				return multierr.Append(
					fmt.Errorf("open log file %q: %w", logPath, err),
					fmt.Errorf("make dir %q: %w", logDir, dirErr),
				)
			}
			f, err = workspace.OpenFile(logPath,
				os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
			if err != nil {
				return fmt.Errorf("open log file %q: %w", logPath, err)
			}
		}

		level, slogLevel := i.ideConfig.logLevel()

		log.SetOutput(f)
		log.SetLevel(level)
		log.SetFormatter(logging.LogrusLogdFormatter{})
		logger = slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{
			Level: slogLevel,
		}))

	} else {
		log.SetOutput(io.Discard)
		log.SetLevel(log.PanicLevel)
		logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{}))
	}
	slog.SetDefault(logger)

	log.Tracef("logging configured and ready")
	slog.Debug("structured logging configured and ready")

	i.publishEventFn = op.publishEvent
	i.locker = op.locker

	// register default schemes
	workspaceManager := workspace.NewManagerWithWorkspaceFunc(
		i.ideConfig.workspace(), op.scheduleFn, workspace.NewSchemeWorkspace)
	err := workspaceManager.RegisterScheme(
		workspacessh.Scheme,
		workspacessh.New(newWorkspaceWindowManagerUI(i),
			workspacessh.WithProvisionManifest(i.provisionManifest),
			workspacessh.WithRemoteDataDir(filepath.Base(dataDir))),
	)
	if err != nil {
		return fmt.Errorf("register ssh scheme: %w", err)
	}
	err = workspaceManager.RegisterScheme(workspace.FileScheme,
		fileSchemeFunc(op.zdotDir))
	if err != nil {
		return fmt.Errorf("register file scheme: %w", err)
	}
	err = workspaceManager.RegisterScheme(workspace.MemoryScheme, workspace.NewMemoryScheme)
	if err != nil {
		return fmt.Errorf("register memory scheme: %w", err)
	}

	// register user-provided schemes via ide.WithScheme
	for scheme, fn := range op.schemes {
		if err := workspaceManager.RegisterScheme(scheme, fn); err != nil {
			return fmt.Errorf("register %q scheme: %w", scheme, err)
		}
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("user home dir: %v", err)
	}

	homePath, err := workspaceapi.ExpandPath(i.ideConfig.workspaceHome(),
		func() (*user.User, error) {
			return &user.User{HomeDir: homeDir}, nil
		}, os.Getwd)
	if err != nil {
		return fmt.Errorf("expand home workspace path: %v", err)
	}

	homeDirURI, err := workspaceapi.CurrentUserHostURI(homePath)
	if err != nil {
		return fmt.Errorf("make home dir uri: %v", err)
	}

	var cwdURI *workspaceapi.URI
	if cwd != "" {
		uri, parseErr := workspaceapi.ParseURI(cwd)
		if parseErr != nil {
			parseErr = fmt.Errorf("could not parse workspace uri: %w", parseErr)
			var pathErr error
			uri, pathErr = workspaceapi.CurrentUserHostURI(cwd)
			if pathErr != nil {
				return multierr.Append(
					parseErr, fmt.Errorf("make current host URI: %w", pathErr))
			}
		}
		cwdURI = &uri
	}

	i.workspaceHandler = new(workspaceManagerHandler)
	commandObserver := newCommandObserverRegistry()
	i.workspaceHandler.packageConfigMergeHook = op.packageConfigMergeHook
	i.workspaceHandler.watchedFilesChangeHook = op.watchedFilesChangeHook
	i.workspaceHandler.workspaceOpenCompleters = op.workspaceOpenCompleters
	i.workspaceHandler.tutorialsInstalled = i.onTutorialsInstalled
	i.workspaceHandler.sessionReopenDisabled = op.disableSessionReopen
	i.workspaceHandler.onboardingActive = func() bool {
		return op.startingTutorial != "" || i.tutorial.running()
	}
	err = i.workspaceHandler.init(cwdURI, homeDirURI, workspaceManager,
		i.ideConfig.notificationsConfig(), i.ideConfig, i.storage, dataDir,
		i.publishEvent,
		op.extensionRunner, trust, i.locker, op.extensions, func() (ideConfig, error) {
			cfg, err := reloadConfig(cfgfilename,
				op.defaultWallpaper, defaultCfg, op.bell, op.scheduleFn,
				op.zdotDir)
			cfg.storage = i.ideConfig.storage
			cfg.cellPixelSize = op.cellPixelSize
			return cfg, err
		}, op.workspaceConfig, op.tabBarOffset,
		op.rightInset, op.tabBarHeight, op.workspacesIcon, op.workspacesBarHeight,
		op.workspacesBarOffset, op.workspacesBarFrame, op.tabsClickCallback,
		op.releaseManager, &i.root, i.ideConfig.initialTerminalCapacity(),
		op.dispatchOnPreview, op.debugCommands, op.streamingOpen,
		commandObserver)
	if err != nil {
		return fmt.Errorf("new workspace manager: %w", err)
	}
	i.workspaceManager = workspaceManager
	// Build tutorials before logNonFatalErrs so any per-tutorial
	// parse errors recorded into ideConfig.errors are surfaced
	// alongside other config decode errors.
	tutorials := buildTutorials(i)
	i.workspaceHandler.logNonFatalErrs(i.workspaceHandler.focusBrowser(),
		configErr, i.ideConfig.errors)

	shutdownShaderCfg := nopShutdownShaderConfig()
	if op.shutdownShaderFn != nil {
		shutdownShaderCfg = shutdownShaderConfig{
			shader:   op.shutdownShaderFn,
			fps:      op.shutdownShaderFPS,
			duration: op.shutdownShaderDuration,
		}
	}
	loadingShaderCfg := loadingShaderConfig{
		shader:   op.loadingShaderFn,
		fps:      op.loadingShaderFPS,
		duration: op.loadingShaderDuration,
	}
	openShaderCfg := openShaderConfig{
		shader:   op.openShaderFn,
		fps:      op.openShaderFPS,
		duration: op.openShaderDuration,
	}
	// Suppress the loading/open workspace animations when the
	// rune.star config explicitly disables them. Defaults are
	// enabled. The open shader has no effect without a loading
	// shader, so disabling loading effectively disables both.
	if !i.ideConfig.animationsLoadingWorkspace() {
		loadingShaderCfg.shader = nil
	}
	if !i.ideConfig.animationsOpenWorkspace() {
		openShaderCfg.shader = nil
	}
	// Apply rune.star overrides for the open-workspace shader, if
	// any. The shader override uses the showroom defaults exposed by
	// :shaderrun; duration replaces the runtime-provided lifetime.
	if name, ok := i.ideConfig.animationsOpenWorkspaceShader(); ok {
		fps := openShaderCfg.fps
		fc := i.ideConfig.windowFrameCharset()
		dur := openShaderCfg.duration
		openShaderCfg.shader = func(defAttr term.Attributes) shader.Shader {
			s, _ := buildNamedShader(name, defAttr, fps, dur, fc)
			return s
		}
	}
	if dur, ok := i.ideConfig.animationsOpenWorkspaceDuration(); ok {
		openShaderCfg.duration = dur
	}
	i.tutorial.init(i.workspaceHandler, tutorials, newTutorialTileStyle(i),
		i.workspaceHandler.rightInset, i.workspaceHandler.setRightInset,
		i.onTutorialCompleted)
	commandObserver.subscribe(&i.tutorial)
	if op.commandDispatchHook != nil {
		commandObserver.subscribe(funcCommandObserver(op.commandDispatchHook))
	}
	_ = i.workspaceHandler.SubscribeEvents(
		textapi.AllEvents(),
		tutorialEventObserver(i.options.scheduleFn, i.tutorial.observeEvent),
	)
	// The boot workspace build launched by workspaceHandler.init above
	// reaches the shader runner from its own goroutine (e.g.
	// abortPendingBuild -> stopLoading) under the locker; take it here
	// too so this init does not race that access.
	i.locker.Lock()
	i.root.init(&i.tutorial, i, i.ideConfig.defaultAttr(), shutdownShaderCfg,
		loadingShaderCfg, openShaderCfg, i.ideConfig.windowFrameCharset())
	i.locker.Unlock()
	if op.nagPrompt.State != nil {
		i.nag = idenag.New(idenag.Config{
			Storage: storageapi.WithPartition(i.storage, idenag.Partition),
			State:   op.nagPrompt.State,
			Show:    i.nagPromptOpener(op.nagPrompt),
		})
		if err := i.nag.Start(context.Background()); err != nil {
			log.WithError(err).Warn("idenag: start nag scheduler")
		}
	}
	err = i.workspaceHandler.subscribeCommand(runShaderCmdManual, &i.root)
	if err != nil {
		return err
	}
	return i.workspaceHandler.subscribeCommand(tutorialCmdManual, &i.tutorial)
}

func newOptions(opts ...Option) options {
	op := defaultOptions()
	for _, o := range opts {
		o(&op)
	}
	return op
}

func newDefaultConfig(op options) DefaultConfig {
	return DefaultConfig{
		src:   op.defaultConfig,
		modal: op.defaultConfigModeModal,
		tui:   op.defaultConfigTUI,
	}
}

func loadIDEConfig(cfgfilename string, op options) (ideConfig, error) {
	var cfg ideConfig
	err := loadConfig(&cfg, cfgfilename,
		op.defaultWallpaper, newDefaultConfig(op), op.bell,
		op.scheduleFn, op.zdotDir)
	cfg.cellPixelSize = op.cellPixelSize
	return cfg, err
}

func (i *IDE) initRunning() {
	if i.options.initShaderFn != nil {
		// protect access to root, simulating a std loop iteration
		i.locker.Lock()
		defer i.locker.Unlock()
		i.root.runShader(i.options.initShaderFn(i.root.defAttr, i.ideConfig.windowFrameCharset()),
			i.options.initShaderFPS, i.options.initShaderDuration)
	}
}

func fileSchemeFunc(zdotDir string) schemeapi.SchemeFunc {
	return func(
		ctx context.Context, cfg config.Config, uri workspaceapi.URI,
	) (schemeapi.Scheme, error) {
		if zdotDir != "" {
			cfg = configWithZdotDir(cfg, zdotDir)
		}
		return workspace.NewFileScheme(ctx, cfg, uri)
	}
}

func configWithZdotDir(base config.Config, zdotDir string) config.Config {
	if base != nil {
		if existing, err := base.GetString("zdotdir"); err == nil && existing != "" {
			return base
		}
	}
	merged := map[string]any{"zdotdir": zdotDir}
	if base != nil {
		base.Iterate(func(k string, v any) {
			if _, ok := merged[k]; !ok {
				merged[k] = v
			}
		})
	}
	return config.MapConfig(merged)
}
