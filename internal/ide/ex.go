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
	"errors"
	"fmt"
	"os"
	"runtime"
	rtdebug "runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"mvdan.cc/sh/v3/syntax"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	fileexplorercomp "unstable.build/rune/internal/component/fileexplorer"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/component/notifications"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/debug"
	thandler "unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/handler/command"
	hmarkdown "unstable.build/rune/internal/handler/markdown"
	"unstable.build/rune/internal/ide/idecmd"
	"unstable.build/rune/internal/ide/ideshell"
	"unstable.build/rune/internal/ide/ideshell/workspaceshell"
	"unstable.build/rune/internal/ide/idetask"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/ide/vctrl"
	tterm "unstable.build/rune/internal/term"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtereservoir"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/cmdenv"
	"unstable.build/rune/internal/text/exoeditor"
	"unstable.build/rune/internal/text/textrpc"
	"unstable.build/rune/internal/workspace"
)

const (
	commandHistoryDocumentID = "command-history:ex-command-history"
	shellHistoryDocumentID   = "shell-history:ex-shell-history"
	reissuePadding           = 10 * time.Millisecond
	fileExplorerURI          = "memory:///fexplorer"
)

var (
	errInvalidSetCursor    = errors.New("cannot set cursor on this buffer")
	errEventStreamNotReady = errors.New("event stream not ready to publish")
	errInvalidTab          = errors.New("expected exactly one argument with the position")
)

// ErrFlushPendingQuit is returned by :q and :wq when one or more
// buffers have an outstanding async save in flight. The user can
// wait for the save to complete or force the quit via :q! / :wq.
var ErrFlushPendingQuit = errors.New(
	"save in progress; wait or use :q! / :wq")

type pluginHandler interface {
	browserapi.Floating
	OnFocusChange(bool)
	Title() string
}

type macroRecorder interface {
	IsRecording() bool
	RegisterID() string
}

// ex implements a tui.Handler by wrapping an editor.Component and
// providing an ex editor type of interface.
type ex struct {
	config         text.Config
	comp           text.Component
	clip           clipboard.Register
	ed             text.Editor
	editorObserver *commandRegisterObserver
	parser         syntaxapi.Parser
	svc            vctrl.Service
	// gitshowSeq keeps a second :gitshow popup for the same file from
	// colliding with one the user has not closed yet.
	gitshowSeq    int
	wsExecutor    *workspaceshell.Executor
	aliasExpander *idecmd.Expander
	// executor is a forwarding proxy: long-lived consumers (the
	// CommandSubstResolver, plugin.New, the VTE) capture this value
	// once and continue to route through whatever underlying
	// schemeapi.Executor setExecutor last installed.
	executor           *currentExecutor
	extensionsExecutor *workspaceshell.Executor
	storage            storageapi.Service
	// terminalStorage holds saved terminal sessions, whose snapshots
	// are too large to share a partition with anything that is listed.
	// It is opened on first use and released by Close: a firstmover
	// partition caches backend handles once resolved.
	terminalStorage          storageapi.Service
	workspaceURI             workspaceapi.URI
	closed                   bool
	home                     bool
	reservoir                *vtereservoir.Facility
	initialReservoirCapacity int
	container                *notifications.Container
	notifications            browserapi.Notifications
	emulatorConfig           vte.Config
	newEmulatorHandler       func([]string) (vtereservoir.VTE, error)
	tm                       browser.TabManager
	tabAliases               *tabNameAliaser
	newPluginHandler         func(int, ...string) (pluginHandler, error)
	workspace                workspace.Workspace
	terminal                 *asyncTerminal
	tasks                    *idetask.Manager
	dispatchOnPreview        map[string]previewFunc
	filepathCompleter        command.Completer
	sequencer                thandler.Sequencer
	publishEvent             func(term.Event) bool
	cancelPartialReissue     func()
	ctxPartialReissue        context.Context
	reissueEvent             term.Event
	cmd                      *command.Prompt
	syncCommandPrompt        bool
	// promptEditor backs both the command prompt's modal edit mode
	// and the companion shell's input line. It is a required
	// dependency (see newEx) so neither consumer has to guard nil.
	promptEditor      command.Editor
	pluginWaitTimeout time.Duration
	// stepWaitTimeout bounds how long one alias step may hold the
	// dispatch slot after claiming its waiter. Must exceed
	// pluginWaitTimeout, the budget of the only step kind that bounds
	// itself; the rest can claim and then never report at all.
	stepWaitTimeout time.Duration
	// use floating windows functionality without having to work around focus commands
	// and how to se cmd.Window correctly.
	cmdV   handler.Virtual[*browser.Component]
	cmdWin browser.Window
	// promptMouseDown latches while a mouse button is held over the
	// prompt handling path so a drag that leaves the prompt rect and
	// releases outside is not mistaken for an outside click.
	promptMouseDown  bool
	promptShader     *shader.Component
	commandPromptCfg commandPromptConfig
	// editorModeModal records whether the editor backing this ex runs in
	// modal mode. The cheatsheet uses it to gate modal-only key tips.
	editorModeModal bool
	// editorMode is the resolved editor mode (modal, standard, emacs, or
	// exo). The cheatsheet uses it to describe the active editor; unlike
	// editorModeModal it preserves the exo distinction.
	editorMode string
	// editorAutoSave records whether the editor flushes buffers
	// automatically. The cheatsheet uses it to gate the manual write row.
	editorAutoSave   bool
	fullscreenID     uint64
	exit             bool
	forceExit        bool
	height           int
	width            int
	isPromptDispatch bool
	macro            macroRecorder

	companionTerminal    vtereservoir.VTE
	companionTerminalWin browser.Window
	companionConsole     *ideshell.Handler
	companionConsoleURI  workspaceapi.URI

	fileExplorerWin     browser.Window
	fileExplorerTarget  browser.Window
	fileExplorerHandler *fileExplorerHandler
	sched               func(func()) bool
	flusher             *flusher
	debugCommands       bool
	commandObserver     commandObserver
	consoleCfg          consoleConfig
	extReady            map[string]chan extReadyJob
	bgCtx               context.Context
	bgCancel            context.CancelFunc
	runInFlight         *aliasRun
	runQueue            []queuedDispatch
	// asyncVTELoads tracks in-flight asyncVTE and asyncPlugin factory
	// goroutines. Test-only synchronization point.
	asyncVTELoads sync.WaitGroup
	// defAttr holds the live theme default attributes so the grayscale
	// command-prompt dim writer resolves a ColorDefault foreground.
	defAttr term.Attributes
}

type commandObserver interface {
	observeCommand(typed, resolved string, args []string, err error)
}

// previewFunc is a function used to preview commands.
// The first argument returns a component to render alongside the command
// prompt and the function is used to cancel any mutable effects.
type previewFunc = func(string, ...string) (component.Responsive, func(), bool)

func newEx(
	edFactory func(exoeditor.Reloader, schemeapi.Terminal) (text.Editor, error),
	m workspace.Workspace,
	storage storageapi.Service,
	notifications *notisManager,
	uri workspaceapi.URI,
	emulatorConfig vte.Config,
	pluginBarConfig plugin.BarConfig,
	publishEvent func(term.Event) bool,
	initialVTECapacity int,
	clip clipboard.Register,
	macro macroRecorder,
	dispatchOnPreview map[string]previewFunc,
	tm browser.TabManager,
	parser syntaxapi.Parser,
	svc vctrl.Service,
	promptEditor command.Editor,
	commandObserver commandObserver,
	debugCommands bool,
	commandPromptCfg commandPromptConfig,
	editorModeModal bool,
	editorMode string,
	editorAutoSave bool,
	consoleCfg consoleConfig,
	opts ...text.Option,
) (e *ex, err error) {
	e = new(ex)
	e.svc = svc
	err = e.init(edFactory, m, storage, notifications, uri,
		emulatorConfig, pluginBarConfig, publishEvent, initialVTECapacity, clip, macro,
		dispatchOnPreview, tm, parser, promptEditor, opts...)
	if err != nil {
		// The async terminal's worker goroutine outlives a failed
		// construction otherwise: nothing will ever call Close.
		e.stopTerminal()
		return
	}
	e.commandObserver = commandObserver
	e.debugCommands = debugCommands
	e.commandPromptCfg = commandPromptCfg
	e.editorModeModal = editorModeModal
	e.editorMode = editorMode
	e.editorAutoSave = editorAutoSave
	e.consoleCfg = consoleCfg
	return
}

// init initializes this ex with the given editor and Options.
// It returns an error if an initial filepath was given through WithFilePath option
// and the file failed to be opened.
func (e *ex) init(
	edFactory func(exoeditor.Reloader, schemeapi.Terminal) (text.Editor, error),
	m workspace.Workspace,
	storage storageapi.Service,
	notifications *notisManager,
	uri workspaceapi.URI,
	emulatorConfig vte.Config,
	pluginBarConfig plugin.BarConfig,
	publishEvent func(term.Event) bool,
	initialVTECapacity int,
	clip clipboard.Register,
	macro macroRecorder,
	dispatchOnPreview map[string]previewFunc,
	tm browser.TabManager,
	parser syntaxapi.Parser,
	promptEditor command.Editor,
	opts ...text.Option,
) (err error) {
	if promptEditor == nil {
		panic("ide.ex requires a prompt editor")
	}
	e.promptEditor = promptEditor
	e.extReady = make(map[string]chan extReadyJob)
	e.bgCtx, e.bgCancel = context.WithCancel(context.Background())
	err = e.doInit(m, storage, notifications, uri,
		emulatorConfig, publishEvent, clip, opts...)
	if err != nil {
		return
	}
	e.parser = parser
	if e.svc == nil {
		e.svc = vctrl.NopService()
	}
	if emulatorConfig.ScheduleNextTick == nil {
		panic("ide.ex: emulatorConfig.ScheduleNextTick must not be nil")
	}
	e.sched = emulatorConfig.ScheduleNextTick
	e.flusher = newFlusher(&e.comp, e.notifications, e.sched)
	ed, err := edFactory(e.flusher, e.terminal)
	if err != nil {
		return err
	}
	e.ed = ed
	if e.config.CommandFallbacks == nil {
		e.config.CommandFallbacks = map[string]text.FallbackPrompter{}
	}
	e.config.CommandFallbacks["agent"] = e
	e.config.CommandFallbacks["?"] = e
	e.config.CommandFallbacks["searchfile"] = e
	e.config.CommandFallbacks["searchtext"] = e
	e.config.CommandFallbacks["searchast"] = e
	e.config.OnTabIconClick = e.closeTabFromIcon
	err = e.comp.Init(ed, m, e.config)
	if err != nil {
		return
	}
	e.editorObserver = newCommandRegisterObserver(&e.comp)
	if tm == nil {
		tm = e.Browser()
	}
	e.tabAliases = newTabNameAliaser(tm)
	e.tm = e.tabAliases
	e.comp.SubscribeWindow((*windowSubscriber)(e))
	if initialVTECapacity != 0 {
		e.initialReservoirCapacity = initialVTECapacity
		e.reservoir = vtereservoir.New(e.Browser(), e.Browser(),
			e.terminal, e.executor, e.tm, e.emulatorConfig, initialVTECapacity)
	}
	newEmulator := func(cmdAndArgs []string) (
		vtereservoir.VTE, error,
	) {
		if e.reservoir != nil && argsMatchEmulatorShell(cmdAndArgs, e.emulatorConfig.CommandAndArgs) {
			e.log(log.TraceLevel, "getting vte instance from reservoir")
			return e.reservoir.Get()
		}
		cfg := e.emulatorConfig
		if len(cmdAndArgs) != 0 {
			cfg.CommandAndArgs = cmdAndArgs
		}
		v, err := vte.NewHandler(e.Browser(), e.Browser(),
			e.terminal, e.executor, e.tm, cfg)
		if err != nil {
			return nil, err
		}
		return vteAdapter{v}, nil
	}
	e.newEmulatorHandler = func(cmdAndArgs []string) (
		vtereservoir.VTE, error,
	) {
		// Spawns can block on transport RPCs (reservoir warm-up
		// waits, NewPty/StartCommand); run them off the event loop
		// behind a placeholder so the UI stays responsive even when
		// the workspace transport is slow or wedged.
		return newAsyncVTE(e, func() (vtereservoir.VTE, error) {
			return newEmulator(cmdAndArgs)
		}), nil
	}
	pluginOpts := []plugin.Option{
		plugin.WithVTEConfig(e.emulatorConfig),
		plugin.WithFrame(e.config.Frame),
		plugin.WithFrameCharSet(e.config.FocusFrameCharSet),
		plugin.WithFrameAttr(e.config.FocusFrameAttr),
		plugin.WithBarConfig(pluginBarConfig),
		plugin.WithCommandExpander(cmdenv.NewCommandSubstResolver(
			e.executor, e.config.EnvSource)),
	}
	// "!" plugins float with a window bar that shows the command as
	// its title, so drop the command from the plugin's own bar. Task
	// windows keep it: they can be tiled, where there is no bar.
	floatPluginOpts := pluginOpts
	if e.config.WindowManagerConfig.Frame &&
		e.config.WindowManagerConfig.WindowBar {
		floatPluginOpts = append(slices.Clip(pluginOpts),
			plugin.WithoutBarCommand())
	}
	e.newPluginHandler = func(width int, args ...string) (pluginHandler, error) {
		h, err := plugin.New(e.Browser(), e.Browser(), e.executor, e.terminal,
			e.tm, args, width, floatPluginOpts...)
		if err != nil {
			// An explicit nil keeps the interface nil-comparable for
			// the caller (a typed nil *plugin.Handler would not be).
			return nil, err
		}
		return h, nil
	}
	e.dispatchOnPreview = dispatchOnPreview
	e.macro = macro
	e.filepathCompleter = command.FilePathCompleter(e.workspace)
	e.tasks = idetask.NewManager(&e.comp, tm, m, e.terminal,
		emulatorConfig.ScheduleNextTick, pluginOpts...)
	e.tasks.SetFrameAttr(e.config.FrameAttr)
	e.tasks.SetFocusFrameAttr(e.config.FocusFrameAttr)
	e.comp.SubscribeWindow(e.tasks)
	return
}

func (e *ex) subscribeCommands() error {
	var ret error
	subscribe := func(name string, man commandAll) {
		man.man.Name = name
		err := e.editorObserver.SubscribeCommand(man.man, text.FuncCommandHandler(
			func(ctx context.Context, cmd textapi.Command) error {
				return man.handler(e, ctx, cmd.Args...)
			}, func(ctx context.Context, cmd textapi.Command) (
				iterator.Iterator[string], string, error,
			) {
				if man.completer == nil {
					return iterator.FromSlice[string](nil), "", nil
				}
				return man.completer(e, ctx, cmd)
			}))
		if err != nil {
			ret = multierror.Append(ret, fmt.Errorf("subscribe command: %w", err))
		}
	}
	for name, man := range exCommands {
		subscribe(name, man)
	}
	if e.debugCommands {
		for name, man := range exDebugCommands {
			subscribe(name, man)
		}
	}
	return ret
}

func (e *ex) setExecutor(
	exe schemeapi.Executor,
	wsExec *workspaceshell.Executor,
	extExec *workspaceshell.Executor,
) {
	e.executor.set(exe)
	e.wsExecutor = wsExec
	e.extensionsExecutor = extExec
	if e.reservoir != nil {
		_ = e.reservoir.Close()
		e.reservoir = vtereservoir.New(e.Browser(), e.Browser(),
			e.terminal, e.executor, e.tm, e.emulatorConfig,
			e.initialReservoirCapacity)
	}
}

func (e *ex) completeReadFile(
	ctx context.Context, args []string,
) (iterator.Iterator[string], string, error) {
	// `:readfile` auto-completion works the same as the `:edit` command.
	return e.filepathCompleter.Complete(ctx, args)
}

func (e *ex) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "ide.ex").Logf(level, msg, args...)
}

func (e *ex) Interrupt(ctx context.Context) error {
	payload, _ := term.PayloadFromContext(ctx)
	ev := term.Event{Type: term.EventInterrupt, Raw: payload, Context: ctx}
	if !e.publishEvent(ev) {
		return errEventStreamNotReady
	}
	return nil
}

func (e *ex) doInit(
	m workspace.Workspace,
	storage storageapi.Service,
	n *notisManager,
	uri workspaceapi.URI,
	emulatorConfig vte.Config,
	publishEvent func(term.Event) bool,
	clip clipboard.Register,
	opts ...text.Option,
) (err error) {
	e.executor = &currentExecutor{}
	e.executor.set(m)
	e.pluginWaitTimeout = 60 * time.Second
	e.stepWaitTimeout = e.pluginWaitTimeout + 30*time.Second
	e.clip = clip
	e.workspace = m
	e.container = notifications.New(&e.comp, n.cfg)
	e.notifications = n.new(uri, e.container)
	e.terminal = newAsyncTerminal(m, e.notifications)
	e.publishEvent = publishEvent
	e.storage = storage
	e.workspaceURI = uri
	// A t=f, t=t or t=s graphics transmission names a file on the
	// machine the terminal's command runs on, which is this workspace.
	emulatorConfig.FileSystem = m
	if uri.Scheme() == workspace.FileScheme {
		// The command inherits this process's environment, TMPDIR
		// included; a remote machine's is unknown.
		emulatorConfig.TempDir = os.TempDir()
	}
	e.emulatorConfig = emulatorConfig

	e.config = text.DefaultConfig()

	opts = append(opts, text.WithNotifications(e.notifications))
	for _, o := range opts {
		o(&e.config)
	}
	if e.config.ScheduleNextTick == nil {
		e.config.ScheduleNextTick = emulatorConfig.ScheduleNextTick
	}

	seqInterests := make([]thandler.Sequence, 0,
		len(e.config.CommandSequenceBindings))
	for seq := range e.config.CommandSequenceBindings {
		seqInterests = append(seqInterests, seq)
	}
	e.sequencer.Init(seqInterests, e.config.SequencerTimeout)

	e.cleanPartialReissueState()
	cmdBrowserCfg := e.config.Config
	if cmdBrowserCfg.WindowManagerConfig.Frame {
		wmOriginY := 2
		if cmdBrowserCfg.TabBarHeight != 0 {
			wmOriginY = cmdBrowserCfg.TabBarHeight - 1
		}
		cmdBrowserCfg.WindowManagerConfig.Frame = false
		cmdBrowserCfg.TabBarHeight = wmOriginY
	}
	e.cmdV.C = browser.NewComponent(cmdBrowserCfg)
	e.aliasExpander = idecmd.NewExpander(
		e.config.CommandAliases, e.comp.DispatchEnv(),
	)
	return
}

func (e *ex) Wait() {
	if e.cmd != nil {
		e.cmd.Wait()
	}
}

// Complete satisfies command.Completer for command.Handler.
func (e *ex) Complete(ctx context.Context, args []string) (
	iterator.Iterator[string], string, error,
) {
	if len(args) == 0 {
		return nil, "", errors.New("missing command")
	}
	uri, h, ok := e.handlerInFocus()
	scmd := textapi.Command{
		Name:     args[0],
		Args:     args[1:],
		Resource: h,
		URI:      uri,
		Window:   e.invokeWindow(),
	}
	if ok {
		scmd.Cursor.Content = h.CursorAtScroll()
		scmd.Cursor.Window, _, _ = h.Cursor()
	}
	it, newArg, err := e.completeCommand(ctx, scmd)
	if err != nil {
		e.setError(fmt.Errorf("complete command: %v", err))
	}
	return it, newArg, err
}

// completeCommand resolves alias completers before falling through to
// the text component's command-subscriber completion. Aliases used to
// live inside text.Component; now they're owned by ide.
func (e *ex) completeCommand(
	ctx context.Context, cmd textapi.Command,
) (iterator.Iterator[string], string, error) {
	if alias, ok := e.aliasExpander.ResolveAlias(cmd.Name); ok {
		if len(alias.Completers) == 0 {
			return iterator.FromSlice[string](nil), "", nil
		}
		argv := append([]string{cmd.Name}, cmd.Args...)
		if len(alias.Completers) == 1 {
			factory := alias.Completers[0]
			if factory == nil {
				return iterator.FromSlice[string](nil), "", nil
			}
			return factory(&e.comp).Complete(ctx, argv)
		}
		completers := make([]command.Completer, 0, len(alias.Completers))
		for _, factory := range alias.Completers {
			if factory == nil {
				continue
			}
			completers = append(completers, factory(&e.comp))
		}
		return command.MultiCompleter(completers...).Complete(ctx, argv)
	}
	return e.comp.CompleteCommand(ctx, cmd)
}

// Dispatch satisfies command.Dispatcher for command.Handler.
func (e *ex) Dispatch(command string, args ...string) bool {
	e.isPromptDispatch = true
	quit, err := e.runCommand(command, args)
	if err != nil {
		e.setError(err)
	}
	e.isPromptDispatch = false
	return quit
}

// Preview satisfies command.Dispatcher for command.Handler.
func (e *ex) Preview(command string, args ...string) (component.Responsive, func(), bool) {
	if e.dispatchOnPreview == nil {
		return nil, nil, false
	}
	do, ok := e.dispatchOnPreview[command]
	if !ok {
		return nil, nil, false
	}
	return do(command, args...)
}

func (e *ex) handlerInFocus() (workspaceapi.URI, text.Handler, bool) {
	content, _ := e.invokeWindow().Content()
	t, ok := content.(*browser.Tab)
	if !ok {
		return workspaceapi.URI{}, nil, false
	}
	ret, ok := t.Handler().(text.Handler)
	if !ok {
		return t.URI(), nil, false
	}
	return t.URI(), ret, true
}

// focusTab returns the focused tab (and its URI) for use with the
// async FlushTab/ReloadTab/OverwriteTab APIs. Unlike handlerInFocus,
// it does not unwrap the tab's inner text.Handler — those APIs need
// the *browser.Tab so they can locate its FlusherCloser.
func (e *ex) focusTab() (workspaceapi.URI, browserapi.Handler, bool) {
	content, _ := e.invokeWindow().Content()
	t, ok := content.(*browser.Tab)
	if !ok {
		return workspaceapi.URI{}, nil, false
	}
	return t.URI(), t, true
}

func (e *ex) moveFocusCursor(line int) error {
	_, h, ok := e.handlerInFocus()
	if !ok {
		return errInvalidSetCursor
	}
	// silently correct invalid line numbers
	// NOTE: this won't work if we implement +/- relative
	// line go to i.e. :+1, :-20
	if line < 0 {
		line = 0
	}
	ok = h.SetCursorAtScroll(term.Coordinates{Y: line})
	if !ok {
		return errors.New("could not set cursor to position")
	}
	return nil
}

func (e *ex) tabrename(_ context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("expected at least one argument with new tab name")
	}

	win := e.invokeWindow()
	if win == e.companionTerminalWin {
		return errors.New("cannot rename companion terminal")
	}
	content, _ := win.Content()
	t, ok := content.(*browser.Tab)
	if !ok {
		return errors.New("cannot rename non tab")
	}
	var attrs term.Attributes
	if len(args) > 1 {
		attrs.Fg = term.GetColor(args[1])
		if len(args) > 2 {
			attrs.Bg = term.GetColor(args[2])
		}
	}
	return e.Browser().SetTabName(t.URI(), args[0], attrs)
}

func (e *ex) tabprevious(_ context.Context, args ...string) error {
	b := e.comp.Browser()
	if e.invokeWindow() == e.companionTerminalWin {
		return e.toggleCompanionTerminal()
	}
	if e.fileExplorerWin != nil && e.invokeWindow() == e.fileExplorerWin {
		return nil
	}
	b.PreviousTab(e.invokeWindow())
	return nil
}

func (e *ex) tabnext(_ context.Context, args ...string) error {
	b := e.comp.Browser()
	if e.invokeWindow() == e.companionTerminalWin {
		return e.toggleCompanionTerminal()
	}
	if e.fileExplorerWin != nil && e.invokeWindow() == e.fileExplorerWin {
		return nil
	}
	b.NextTab(e.invokeWindow())
	return nil
}

func (e *ex) tabfocus(_ context.Context, args ...string) error {
	if len(args) < 1 {
		return errInvalidTab
	}
	if e.fileExplorerWin != nil && e.invokeWindow() == e.fileExplorerWin {
		return nil
	}
	idxStr := args[0]
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return errInvalidTab
	}
	if idx == 0 {
		return errors.New("the first tab is 1")
	}
	b := e.comp.Browser()
	tabs := b.Tabs()
	if idx-1 >= 0 && idx-1 < len(tabs) {
		if win, ok := tabs[idx-1].Window(); ok {
			if win != e.invokeWindow() {
				b.SetFocus(win)
			}
			return nil
		}
	}
	b.SetContentToTab(e.invokeWindow(), idx-1)
	return nil
}

func (e *ex) tabclose(_ context.Context, args ...string) error {
	b := e.comp.Browser()
	win := e.invokeWindow()
	if win == e.companionTerminalWin {
		return e.toggleCompanionTerminal()
	}
	if e.fileExplorerWin != nil && win == e.fileExplorerWin {
		e.closeFileExplorerWindow()
		return nil
	}
	content, err := win.Content()
	if err != nil {
		return fmt.Errorf("get window content: %w", err)
	}
	if tab, ok := content.(*browser.Tab); ok && e.tabIsDirty(tab) {
		e.openCloseDirtyTabsPrompt(
			fmt.Sprintf("File '%s' has changes pending to be written. "+
				"Close and discard changes?", tab.URI().Name()),
			func() error {
				b.RemoveWindowContent(win)
				return nil
			},
		)
		return nil
	}
	// on focus dispatch to vte.Handler via Close
	b.RemoveWindowContent(win)
	return nil
}

// closeTabFromIcon closes tab like tabclose closes the one in focus,
// asking first when it has changes pending to be written.
func (e *ex) closeTabFromIcon(tab *browser.Tab) {
	b := e.comp.Browser()
	if e.tabIsDirty(tab) {
		e.openCloseDirtyTabsPrompt(
			fmt.Sprintf("File '%s' has changes pending to be written. "+
				"Close and discard changes?", tab.URI().Name()),
			func() error {
				b.RemoveTab(tab)
				return nil
			},
		)
		return
	}
	b.RemoveTab(tab)
}

func (e *ex) tabcloseall(_ context.Context, args ...string) error {
	b := e.comp.Browser()
	dirty := e.dirtyTabCount(e.comp.Tabs())
	if dirty > 0 {
		e.openCloseDirtyTabsPrompt(
			fmt.Sprintf("There are %d tabs with changes pending to be written. "+
				"Close and discard changes?", dirty),
			func() error {
				b.RemoveAllTabs()
				return nil
			},
		)
		return nil
	}
	// on focus dispatch to vte.Handler via Close
	b.RemoveAllTabs()
	return nil
}

func (e *ex) tabcloseinactive(_ context.Context, args ...string) error {
	b := e.comp.Browser()
	inactive := e.inactiveTabs()
	dirty := e.dirtyTabCount(inactive)
	if dirty > 0 {
		e.openCloseDirtyTabsPrompt(
			fmt.Sprintf("There are %d inactive tabs with changes pending to be written. "+
				"Close and discard changes?", dirty),
			func() error {
				if removed := b.RemoveInactiveTabs(); !removed {
					return errors.New("no inactive tabs left")
				}
				return nil
			},
		)
		return nil
	}
	if removed := b.RemoveInactiveTabs(); !removed {
		return errors.New("no inactive tabs left")

	}
	return nil
}

func (e *ex) tabIsDirty(tab *browser.Tab) bool {
	dirty, ok := e.comp.IsDirty(tab.URI())
	return ok && dirty
}

func (e *ex) dirtyTabCount(tabs []*browser.Tab) int {
	var dirty int
	for _, tab := range tabs {
		if e.tabIsDirty(tab) {
			dirty++
		}
	}
	return dirty
}

func (e *ex) inactiveTabs() []*browser.Tab {
	var inactive []*browser.Tab
	for _, tab := range e.comp.Tabs() {
		if _, ok := tab.Window(); ok {
			continue
		}
		inactive = append(inactive, tab)
	}
	return inactive
}

func (e *ex) closeFocusWindow(_ context.Context, args ...string) error {
	win := e.invokeWindow()
	if win == e.companionTerminalWin {
		return e.toggleCompanionTerminal()
	}
	// on focus dispatch to vte.Handler via Close
	// closing the last tiled window is a no-op so the command is idempotent.
	if err := win.Close(); err != nil && !isNothingToCloseErr(err) {
		return err
	}
	return nil
}

func (e *ex) windowcloseall(_ context.Context, args ...string) error {
	// closing when there are no other windows is a no-op so the command is
	// idempotent.
	if err := e.comp.Browser().CloseOtherWindows(e.invokeWindow()); err != nil &&
		!isNothingToCloseErr(err) {
		return err
	}
	return nil
}

func isNothingToCloseErr(err error) bool {
	switch err.Error() {
	case "cannot close last tiled window", "no windows to close":
		return true
	default:
		return false
	}
}

func (e *ex) flushCloseIgnoreNonFlushed(_ context.Context, args ...string) error {
	if h, ok := e.fileExplorerHandlerInFocus(); ok {
		return h.forceFlush()
	}
	// :writeforcequit! — exit immediately and let any in-flight or
	// newly-started saves complete in the background.
	e.forceExit = true
	e.exit = true
	uri, t, ok := e.focusTab()
	if !ok {
		return nil
	}
	_ = e.flusher.forceFlush(uri, t)
	return nil
}

func (e *ex) flushClose(_ context.Context, args ...string) error {
	if h, ok := e.fileExplorerHandlerInFocus(); ok {
		return h.flush()
	}
	uri, t, ok := e.focusTab()
	if !ok {
		// nothing to flush; behave like :q
		e.forceExit = false
		e.exit = true
		return nil
	}
	// :wq — start an async flush; on success, request exit.
	return e.flusher.flushAndThen(uri, t, false /* force */, func() {
		e.forceExit = false
		e.exit = true
	})
}

func (e *ex) flush(ctx context.Context, args ...string) error {
	if h, ok := e.fileExplorerHandlerInFocus(); ok {
		return h.flush()
	}
	if h, ok := e.terminalInFocus(); ok {
		var name string
		var err error
		if len(args) == 0 {
			name, err = e.nextTerminalSessionName(ctx, h)
		} else {
			name, err = normalizeTerminalSessionName(args)
		}
		if err != nil {
			return err
		}
		return e.saveTerminalSession(ctx, name, h)
	}
	uri, t, ok := e.focusTab()
	if !ok {
		return textapi.ErrInvalidSave
	}
	return e.flusher.flush(uri, t)
}

func (e *ex) forceFlush(ctx context.Context, args ...string) error {
	if h, ok := e.fileExplorerHandlerInFocus(); ok {
		return h.forceFlush()
	}
	if h, ok := e.terminalInFocus(); ok {
		var name string
		var err error
		if len(args) == 0 {
			name, err = e.nextTerminalSessionName(ctx, h)
		} else {
			name, err = normalizeTerminalSessionName(args)
		}
		if err != nil {
			return err
		}
		return e.saveTerminalSession(ctx, name, h)
	}
	uri, t, ok := e.focusTab()
	if !ok {
		return textapi.ErrInvalidSave
	}
	return e.flusher.forceFlush(uri, t)
}

func (e *ex) flushAll(_ context.Context, args ...string) (ret error) {
	for _, t := range e.comp.Tabs() {
		if err := e.flusher.flush(t.URI(), t); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	return
}

func (e *ex) forceFlushAll(_ context.Context, args ...string) (ret error) {
	for _, t := range e.comp.Tabs() {
		if err := e.flusher.forceFlush(t.URI(), t); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	return
}

func (e *ex) forcequit(_ context.Context, args ...string) error {
	e.forceExit = true
	e.exit = true
	return nil
}
func (e *ex) quit(_ context.Context, args ...string) error {
	if e.flusher.inFlightCount() > 0 {
		return ErrFlushPendingQuit
	}
	e.forceExit = false
	e.exit = true
	return nil
}

func (e *ex) waitInflight() {
	e.waitAsyncVTELoads()
	e.comp.WaitStreamingLoads()
	e.flusher.wait()
	if e.tasks != nil {
		e.tasks.WaitInflight()
	}
}

// waitAsyncVTELoads blocks until every in-flight asyncVTE factory
// goroutine has delivered its result to the scheduler. Test-only
// synchronization point, mirroring text.Component.WaitStreamingLoads.
func (e *ex) waitAsyncVTELoads() {
	e.asyncVTELoads.Wait()
}

func (e *ex) dispatchCommand(cmd string, args ...string) (err error) {
	return e.dispatchCommandCtx(context.Background(), cmd, args...)
}

// dispatchCommandCtx is dispatchCommand with a caller-supplied base
// context, letting the caller thread values (e.g. a textrpc.Waiter) down
// to the leaf command handler so it can observe asynchronous completion.
//
// Dispatches are serialised: while an earlier one is still in flight the
// command queues and the returned error is nil. A Waiter on ctx is
// claimed in that case and receives the result instead.
func (e *ex) dispatchCommandCtx(
	ctx context.Context, cmd string, args ...string,
) (err error) {
	if e.runInFlight != nil {
		var w *textrpc.Waiter
		if cw, ok := textrpc.WaiterFromContext(ctx); ok {
			cw.Claimed = true
			w = cw
		}
		e.runQueue = append(e.runQueue, queuedDispatch{
			ctx: ctx, cmd: cmd, args: args, waiter: w,
		})
		return nil
	}
	return e.dispatchResolved(ctx, cmd, args...)
}

// drainRunQueue dispatches queued commands in arrival order until the
// queue empties or one of them takes the dispatch slot in turn.
func (e *ex) drainRunQueue() {
	for e.runInFlight == nil && len(e.runQueue) > 0 {
		q := e.runQueue[0]
		e.runQueue = e.runQueue[1:]
		e.dispatchQueued(q)
	}
}

// dispatchQueued runs a deferred command and delivers its result to the
// waiter the caller was promised, as a notification when there is none.
func (e *ex) dispatchQueued(q queuedDispatch) {
	ctx := q.ctx
	inner := &textrpc.Waiter{Ch: make(chan error, 1)}
	if q.waiter != nil {
		// Shadowed so the handler cannot report on q.waiter directly:
		// the result must be delivered exactly once, from here.
		ctx = textrpc.ContextWithWaiter(ctx, inner)
	}
	err := e.dispatchResolved(ctx, q.cmd, q.args...)
	if q.waiter == nil {
		if err != nil {
			e.setError(err)
		}
		return
	}
	if err != nil || !inner.Claimed {
		q.waiter.Ch <- err
		return
	}
	go debug.CapturePanicReport(func() {
		select {
		case res := <-inner.Ch:
			q.waiter.Ch <- res
		case <-e.bgCtx.Done():
		}
	})
}

// dispatchResolved expands cmd and dispatches it, bypassing the
// serialisation dispatchCommandCtx applies.
func (e *ex) dispatchResolved(
	ctx context.Context, cmd string, args ...string,
) (err error) {
	uri, h, ok := e.handlerInFocus()
	scmd := textapi.Command{
		Name:     cmd,
		Args:     args,
		Resource: h,
		URI:      uri,
		Window:   e.invokeWindow(),
	}
	if ok {
		scmd.Cursor.Content = h.CursorAtScroll()
		scmd.Cursor.Window, _, _ = h.Cursor()
	}
	target, isAlias := e.aliasExpander.ResolveAlias(cmd)
	if cmd == "!" || cmd == "!!" {
		ctx = cmdenv.WithCommandSubstitution(ctx)
	}
	if isAlias && idecmd.ChainFromContext(ctx) == nil {
		ctx = idecmd.WithChain(ctx, cmd, idecmd.NewChain())
	}
	var handled bool
	if isAlias {
		var parent *textrpc.Waiter
		if w, wok := textrpc.WaiterFromContext(ctx); wok {
			parent = w
		}
		r, rerr := newAliasRun(e, ctx, scmd, nil, parent, false)
		if rerr != nil {
			return rerr
		}
		r.step()
		if r.detached {
			// The run owns its result from here on.
			return nil
		}
		handled, err = r.handled, r.err
	} else {
		handled, err = e.dispatchLeaf(ctx, cmd, scmd)
	}
	if err != nil {
		return err
	}
	if handled {
		return nil
	}
	if !ok {
		return fmt.Errorf("unknown command or command alias %q", cmd)
	}
	return fmt.Errorf("%s is aliased to an unknown command %v", cmd, target.Commands)
}

// dispatchLeaf expands a non-alias command's args and dispatches it.
func (e *ex) dispatchLeaf(
	ctx context.Context, cmd string, scmd textapi.Command,
) (handled bool, err error) {
	it, err := e.aliasExpander.Expand(ctx, scmd)
	if err != nil {
		return false, err
	}
	defer func() { _ = it.Close() }()
	for {
		next, ok := it.Next(ctx)
		if !ok {
			break
		}
		h, derr := e.comp.DispatchCommand(ctx, next)
		if e.commandObserver != nil {
			e.commandObserver.observeCommand(cmd, next.Name, next.Args, derr)
		}
		if derr != nil {
			return false, derr
		}
		handled = handled || h
	}
	if iterErr := it.Err(); iterErr != nil {
		return false, iterErr
	}
	return handled, nil
}

func formatStep(cmd textapi.Command) string {
	if len(cmd.Args) == 0 {
		return cmd.Name
	}
	return cmd.Name + " " + strings.Join(cmd.Args, " ")
}

func (e *ex) editFileURI(uri workspaceapi.URI, win browser.Window, readOnly bool) (
	*browser.Tab, error,
) {
	return e.editFileURILocal(uri, win, readOnly)
}

func (e *ex) editFileURILocal(uri workspaceapi.URI, win browser.Window, readOnly bool) (
	*browser.Tab, error,
) {
	var h browserapi.Handler
	var err error
	if readOnly {
		h, err = e.comp.OpenReadOnly(uri)
	} else {
		h, err = e.comp.Open(uri)
	}
	if err != nil {
		return nil, err
	}
	if win.Closed() {
		win, _ = e.comp.Focus()
	}
	err = win.SetContent(h)
	if err == browserapi.ErrTabNotFree {
		// The tab is already rendered in a window. If that window
		// is ours (the file was already open in this workspace),
		// focus it so <enter> brings it into view. Otherwise the
		// multi-workspace OpenRouter already switched to and
		// focused the owning workspace, so there is nothing left
		// to do here.
		err = nil
		if owner, ok := e.localTabWindow(h.(*browser.Tab)); ok {
			_, _ = e.comp.SetFocus(owner)
		}
	}
	return h.(*browser.Tab), err
}

// localTabWindow returns the window of this ex's browser that
// currently renders tab, if any. The lookup is scoped to this ex's
// own windows by ID: a tab routed in from another workspace belongs
// to a different window manager, and SetFocus panics on a foreign
// window.
func (e *ex) localTabWindow(tab *browser.Tab) (browser.Window, bool) {
	owner, ok := tab.Window()
	if !ok || owner.Closed() {
		return nil, false
	}
	return e.comp.Browser().Window(owner.WindowID())
}

func (e *ex) parseURIOrWorkspaceURI(path string) (workspaceapi.URI, error) {
	uri, err := workspaceapi.ParseURI(path)
	if err != nil {
		uri, err = e.workspace.URI(path)
		e.log(log.TraceLevel, "URI (%s): %s, %v", path, uri.String(), err)
	}
	return uri, err
}

func (e *ex) editFiles(_ context.Context, args ...string) error {
	return e.editFilesReadOnly(false, args...)
}

func (e *ex) viewFiles(_ context.Context, args ...string) error {
	return e.editFilesReadOnly(true, args...)
}

func (e *ex) editFilesReadOnly(readOnly bool, args ...string) error {
	if len(args) == 0 {
		return errors.New("expected at least one file name")
	}

	if e.invokeWindow() == e.companionTerminalWin {
		if err := e.toggleCompanionTerminal(); err != nil {
			return fmt.Errorf("toggle companion terminal: %v", err)
		}
	}

	for _, arg := range args {
		// attempt to parse URI otherwise expect local file path
		uri, err := e.parseURIOrWorkspaceURI(arg)
		if err != nil {
			return err
		}
		_, err = e.editFileURI(uri, e.invokeWindow(), readOnly)
		if err != nil {
			return err
		}
	}

	return nil
}

func (e *ex) tabcopypath(_ context.Context, args ...string) error {
	absolute := len(args) > 0 && args[0] == "absolute"
	t, ok := e.comp.FocusTab()
	if !ok {
		return errors.New("not a tab")
	}
	if _, ok := t.Handler().(text.Handler); !ok {
		return errors.New("not a file")
	}

	uri := t.URI()
	path := e.copyPath(uri, absolute)

	err := e.clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: path})
	if err != nil {
		return fmt.Errorf("clipboard copy: %v", err)
	}

	_, _ = e.notifications.Notify(browserapi.LevelSuccess,
		"file path copied to clipboard")

	return nil
}

func (e *ex) tabcopylocation(_ context.Context, args ...string) error {
	absolute := len(args) > 0 && args[0] == "absolute"
	uri, h, ok := e.handlerInFocus()
	if !ok {
		return errors.New("not a file")
	}

	location := fmt.Sprintf("%s:%d", e.copyPath(uri, absolute), h.CursorAtScroll().Y+1)
	err := e.clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: location})
	if err != nil {
		return fmt.Errorf("clipboard copy: %v", err)
	}

	_, _ = e.notifications.Notify(browserapi.LevelSuccess,
		"file location copied to clipboard")

	return nil
}

func (e *ex) copyPath(uri workspaceapi.URI, absolute bool) string {
	if absolute {
		return uri.Path()
	}
	path := workspaceapi.RelPath(e.workspaceURI, uri)
	if path == "" || path == "." || path == uri.String() {
		path = uri.Path()
	}
	return path
}

func (e *ex) reloadfile(_ context.Context, args ...string) error {
	focus := e.invokeWindow()
	content, err := focus.Content()
	if err != nil {
		return fmt.Errorf("get window content: %w", err)
	}
	t, ok := content.(*browser.Tab)
	if !ok {
		return textapi.ErrInvalidReload
	}
	// :reloadfile is fire-and-forget: the reload runs on a
	// background goroutine and a reparse is scheduled back onto
	// the host event loop via syntax.Tree.wrapReparse. Awaiting
	// the result synchronously here would deadlock that reparse
	// against the host mutex.
	return e.flusher.reloadAsync(t.URI(), t, nil)
}

func (e *ex) splitDirectionChange(_ context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("command expects argument 'horizontal', 'h', 'vertical', 'v'")
	}

	b := e.comp.Browser()
	switch args[0] {
	case "horizontal", "h":
		b.SetDefaultSplit(browserapi.OrientationBottom)
		_, _ = e.notifications.Notify(browserapi.LevelInfo, "changed split direction to horizontal")
	case "vertical", "v":
		b.SetDefaultSplit(browserapi.OrientationRight)
		_, _ = e.notifications.Notify(browserapi.LevelInfo, "changed split direction to vertical")
	}
	return nil
}

func (e *ex) windownewHandler(h browserapi.Handler, orientation browserapi.Orientation) {
	eb := e.comp.Browser()
	win := e.invokeWindow()
	eb.Split(orientation, win, h)
}

func (e *ex) invokeWindow() browser.Window {
	ret, _ := e.comp.Focus()
	return ret
}

func (e *ex) windowfocus(_ context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("command expects at least one argument")
	}

	if e.invokeWindow() == e.companionTerminalWin {
		return e.toggleCompanionTerminal()
	}

	switch args[0] {
	case "right":
		e.comp.Browser().FocusRight()
	case "down":
		e.comp.Browser().FocusDown()
	case "left":
		e.comp.Browser().FocusLeft()
	case "up":
		e.comp.Browser().FocusUp()
	case "other":
		e.comp.Browser().ShiftFocus()
	default:
		return fmt.Errorf("invalid argument %q", args[0])
	}
	return nil
}

func (e *ex) moveWindow(_ context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("command expects at least one argument")
	}

	if e.invokeWindow() == e.companionTerminalWin {
		return e.toggleCompanionTerminal()
	}

	var ok bool
	switch args[0] {
	case "right":
		ok = e.comp.Browser().SwapContentRight()
		if ok {
			e.comp.Browser().FocusRight()
		}
	case "down":
		ok = e.comp.Browser().SwapContentDown()
		if ok {
			e.comp.Browser().FocusDown()
		}
	case "left":
		ok = e.comp.Browser().SwapContentLeft()
		if ok {
			e.comp.Browser().FocusLeft()
		}
	case "up":
		ok = e.comp.Browser().SwapContentUp()
		if ok {
			e.comp.Browser().FocusUp()
		}
	default:
		return fmt.Errorf("invalid argument %q", args[0])
	}
	if !ok {
		return errors.New("cannot move window in this direction")
	}
	return nil
}

func (e *ex) moveTab(_ context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("command expects at least one argument")
	}

	var err error
	switch args[0] {
	case "right":
		err = e.comp.Browser().MoveTabRight(e.invokeWindow())
	case "left":
		err = e.comp.Browser().MoveTabLeft(e.invokeWindow())
	default:
		var idx int
		idx, err = strconv.Atoi(args[0])
		if err != nil {
			return errInvalidTab
		}
		if idx == 0 {
			return errors.New("the first tab is 1")
		}
		err = e.comp.Browser().MoveTabTo(e.invokeWindow(), idx-1)
	}
	return err
}

var defaultConvertTabIcon = ''

func (e *ex) convertTab(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("command expects at least one argument with tab name")
	}

	var icon rune
	name := args[0]
	if len(args) > 1 {
		runeArgs := []rune(args[1])
		if len(runeArgs) > 0 {
			icon = runeArgs[0]
		}
	}
	if icon == 0 {
		icon = defaultConvertTabIcon
	}
	win := e.invokeWindow()
	tab, ok := e.comp.Browser().NewTabFromContent(icon, name, win)
	if !ok {
		return errors.New("window content is already a tab")
	}

	if win == e.companionTerminalWin {
		_ = e.toggleCompanionTerminal()
		e.companionTerminal = nil // force re-open next time
		other, _ := e.comp.Focus()
		return other.SetContent(tab)
	}

	if win.IsFloating() {
		err := win.Close()
		if err == nil {
			other, _ := e.comp.Focus()
			return other.SetContent(tab)
		}
		return err
	}
	return nil
}

func (e *ex) windowresize(_ context.Context, args ...string) error {
	if (len(args) == 1 && args[0] != "reset") || len(args) == 0 {
		return errors.New("invalid arguments")
	}

	var ok bool
	switch args[0] {
	case "reset":
		ok = e.comp.Browser().ResetWindowSize()
	case "min":
		switch args[1] {
		case "height":
			ok = e.comp.Browser().SetMinWindowHeight()
		case "width":
			ok = e.comp.Browser().SetMinWindowWidth()
		default:
			return fmt.Errorf("invalid second argument %q", args[1])
		}
	case "max":
		switch args[1] {
		case "height":
			ok = e.comp.Browser().SetMaxWindowHeight()
		case "width":
			ok = e.comp.Browser().SetMaxWindowWidth()
		default:
			return fmt.Errorf("invalid second argument %q", args[1])
		}
	case "increase":
		switch args[1] {
		case "height":
			ok = e.comp.Browser().IncreaseWindowHeight()
		case "width":
			ok = e.comp.Browser().IncreaseWindowWidth()
		default:
			return fmt.Errorf("invalid second argument %q", args[1])
		}
	case "decrease":
		switch args[1] {
		case "height":
			ok = e.comp.Browser().DecreaseWindowHeight()
		case "width":
			ok = e.comp.Browser().DecreaseWindowWidth()
		default:
			return fmt.Errorf("invalid second argument %q", args[1])
		}
	default:
		return fmt.Errorf("invalid first argument %q", args[0])
	}
	if !ok {
		return errors.New("could not resize this window in this way")
	}
	return nil
}

func (e *ex) sendNotificationInfo(_ context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("command expects at least one argument")
	}
	_, err := e.notifications.Notify(browserapi.LevelInfo, strings.Join(args, " "))
	return err
}

func (e *ex) sendNotificationSuccess(_ context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("command expects at least one argument")
	}
	_, err := e.notifications.Notify(browserapi.LevelSuccess, strings.Join(args, " "))
	return err
}

func (e *ex) sendNotificationWarning(_ context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("command expects at least one argument")
	}
	_, err := e.notifications.Notify(browserapi.LevelWarn, strings.Join(args, " "))
	return err
}

func (e *ex) sendNotificationError(_ context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("command expects at least one argument")
	}
	_, err := e.notifications.Notify(browserapi.LevelError, strings.Join(args, " "))
	return err
}

func (e *ex) windowtogglemaximize(_ context.Context, args ...string) error {
	if e.fullscreenID != 0 {
		e.comp.Browser().ResetWindowSize()
		e.fullscreenID = 0
		return nil
	}
	win := e.invokeWindow()
	e.comp.Browser().SetMaxWindowHeight()
	e.comp.Browser().SetMaxWindowWidth()
	e.fullscreenID = win.WindowID()
	return nil
}

func (e *ex) closeNotifications(_ context.Context, args ...string) error {
	e.container.CloseAll()
	return nil
}

func (e *ex) pauseNotifications(_ context.Context, args ...string) error {
	e.container.PauseAll()
	return nil
}

func (e *ex) resumeNotifications(_ context.Context, args ...string) error {
	e.container.ResumeAll()
	return nil
}

func (e *ex) pasteFromClipboard(_ context.Context, args ...string) error {
	handler := e.focusHandler()
	data, err := e.clip.Paste(clipboard.DefaultRegisterID)
	if err != nil {
		return fmt.Errorf("clipboard paste: %w", err)
	}
	if len(data.Text) == 0 {
		_, _ = e.Browser().Notify(browserapi.LevelInfo, "nothing to paste")
		return nil
	}

	handler.Handle(term.Event{
		Type: term.EventPasteStart,
	})
	for _, ch := range data.Text {
		raw := []byte(string(ch))
		handler.Handle(term.Event{
			Type: term.EventKey,
			Ch:   ch,
			Raw:  raw,
		})
	}
	handler.Handle(term.Event{
		Type: term.EventPasteEnd,
	})
	return nil
}

func (e *ex) copyToClipboard(_ context.Context, args ...string) error {
	handler := e.focusHandler()
	data, ok := handler.Selection()
	if !ok {
		_, _ = e.Browser().Notify(browserapi.LevelInfo, "nothing to copy")
		return nil
	}
	err := e.clip.Copy(clipboard.DefaultRegisterID, clipboard.Data{Text: data})
	if err != nil {
		_, _ = e.Browser().Notify(browserapi.LevelError,
			"failed to copy to clipboard: %v", err)
		err = fmt.Errorf("clipboard copy: %w", err)
		return err
	}
	_, _ = e.Browser().Notify(browserapi.LevelSuccess, "copied to clipboard")
	return nil
}

func (e *ex) defaultcolors(_ context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("command expects at least one argument 'background'")
	}

	var attrs term.Attributes
	attrs.Bg = term.GetColor(args[0])

	if len(args) == 2 {
		attrs.Fg = term.GetColor(args[1])
	}

	content, _ := e.invokeWindow().Content()
	emh, ok := content.(vtereservoir.VTE)
	if ok {
		emh.SetDefaultAttributes(attrs)
		return nil
	}
	t, ok := content.(*browser.Tab)
	if !ok {
		return errors.New("cannot change colors of this window")
	}
	th, ok := t.Handler().(text.Handler)
	if ok {
		th.SetDefaultAttributes(attrs)
		return nil
	}
	emh, ok = t.Handler().(vtereservoir.VTE)
	if ok {
		emh.SetDefaultAttributes(attrs)
		return nil
	}
	return errors.New("cannot change colors of this window")
}

func (e *ex) readfile(_ context.Context, args ...string) error {
	if len(args) != 1 {
		return errors.New("expected one file name")
	}

	// get desired uri to read
	filename := args[0]
	uri, err := e.parseURIOrWorkspaceURI(filename)
	if err != nil {
		return err
	}

	// get current editor handler to be used for content insertion
	_, handler, ok := e.handlerInFocus()
	if !ok {
		return errors.New("read file cannot get handler in focus")
	}

	// insert the file contents into the current cursor position
	err = e.comp.ReadFile(uri, handler)
	if err != nil {
		return err
	}

	return err
}

func (e *ex) executePlugin(_ context.Context, args ...string) error {
	if len(args) == 0 {
		return e.toggleCompanionTerminal()
	}
	pluginArgs := cmdenv.BuildPluginArgv(args)
	// The plugin build blocks on transport RPCs (NewPty/StartCommand),
	// so run it behind an async placeholder and open the floating
	// window immediately. Snapshot the width here: the factory runs
	// off the event loop and must not read loop-owned state.
	width := e.width
	h := newAsyncPlugin(e, strings.Join(pluginArgs, " "), width,
		func() (pluginHandler, error) {
			return e.newPluginHandler(width, pluginArgs...)
		})
	cfg := browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
		Title:     h.Title(),
	}
	// there can be multiple floating windows open
	// so instead of matching windows on tabclose,
	// we set a handler that closes the window if the handler
	// is closed.
	ph := &pluginAdapter{pluginHandler: h}
	// mimic same behavior as tab
	ph.OnFocusChange(false)
	win, err := e.comp.Floating(ph, cfg)
	if err != nil {
		_ = h.Close()
		return err
	}
	ph.win = win
	return nil
}

func (e *ex) executePluginWait(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("expected at least one argument")
	}
	start := time.Now()
	e.log(log.DebugLevel, "starting command %v", args)

	var line string
	if len(args) == 1 {
		line = args[0]
	} else {
		line = strings.Join(args, " ")
	}
	parsed, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil {
		return fmt.Errorf("parse shell line %q: %w", line, err)
	}

	notifyName := firstWord(line)
	waiter, hasWaiter := textrpc.WaiterFromContext(ctx)
	if hasWaiter {
		waiter.Claimed = true
	}

	notifID, nerr := e.notifications.Notify(browserapi.LevelInfo,
		"%s: running...", notifyName)
	if nerr == nil {
		_ = e.notifications.UpdateNotificationProgress(notifID, "", 0, 1)
	}

	go debug.CapturePanicReport(func() {
		runCtx, cancel := context.WithTimeout(ctx, e.pluginWaitTimeout)
		defer cancel()
		var stderrBuf strings.Builder
		runner := cmdenv.Runner{
			Executor:  e.executor,
			EnvSource: chainOverlayEnvSource(ctx, e.config.EnvSource),
			Dir:       e.workspaceURI.Path(),
			Stderr:    &stderrBuf,
		}
		captured, runErr := runner.Run(runCtx, line, parsed)
		if runErr == nil {
			// Must land before the waiter is released: whatever the
			// caller does next has to see the captures.
			idecmd.UpdateChainVars(ctx, captured)
		}
		e.config.ScheduleNextTick(func() {
			if nerr == nil {
				_ = e.notifications.UpdateNotificationProgress(notifID, "", 1, 1)
			}
			if runErr != nil {
				if !hasWaiter {
					// A claimed waiter takes delivery of runErr and
					// reports it, so this would be a duplicate.
					_, _ = e.notifications.Notify(browserapi.LevelError,
						fmt.Sprintf("%s: %s", notifyName, runErr))
				}
				return
			}
			_, _ = e.notifications.Notify(browserapi.LevelSuccess,
				fmt.Sprintf("%s: done in %s", notifyName,
					time.Since(start).Truncate(time.Millisecond)))
		})
		if hasWaiter {
			waiter.Ch <- runErr
		}
	})
	return nil
}

// firstWord returns the leading whitespace-delimited word of line,
// used only for surfacing a readable command name in success /
// failure notifications.
func firstWord(line string) string {
	line = strings.TrimLeft(line, " \t")
	if idx := strings.IndexAny(line, " \t"); idx >= 0 {
		return line[:idx]
	}
	return line
}

func chainOverlayEnvSource(ctx context.Context, base cmdenv.Source) cmdenv.Source {
	chain := idecmd.ChainFromContext(ctx)
	if chain == nil {
		return base
	}
	return func(name string) (string, bool) {
		if v, ok := chain.Get(name); ok {
			return v, true
		}
		if base != nil {
			return base(name)
		}
		return "", false
	}
}

func (e *ex) keydump(_ context.Context, _ ...string) error {
	h := browser.Keydump(e.clip, &e.comp)
	cfg := browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	}
	_, err := e.comp.Floating(h, cfg)
	return err
}

func (e *ex) newTask(_ context.Context, args ...string) error {
	const errExpect = "command expects at least four arguments: " +
		"name, alignment, a separator '--' and the command to run"
	if len(args) < 4 {
		return errors.New(errExpect)
	}
	sysArgs, cmdAndArgs, found := strings.Cut(strings.Join(args, " "), "--")
	if !found {
		return errors.New(errExpect)
	}

	sysArgv := strings.Split(sysArgs, " ")
	cmdAndArgv := strings.Split(cmdAndArgs, " ")

	// cleanup splitting via --
	cmdAndArgv = cmdAndArgv[1:]
	sysArgv = sysArgv[:len(sysArgv)-1]

	if len(sysArgv) < 2 || len(cmdAndArgv) == 0 {
		return errors.New(errExpect)
	}

	e.log(log.DebugLevel, "newtask called with args %+v, sysArgs: %+v, cmdAndArgv: %+v",
		args, sysArgv, cmdAndArgv)

	t := idetask.Task{
		Name: sysArgv[0],
		Cmd:  cmdAndArgv[0],
		Args: cmdAndArgv[1:],
	}
	if len(sysArgv) > 2 {
		t.Filter = sysArgv[2]
	}
	switch sysArgv[1] {
	case "right":
		t.MinimizeAlignment = component.AlignmentRight
	case "left":
		t.MinimizeAlignment = component.AlignmentLeft
	default:
		return fmt.Errorf("invalid orientation argument %q", sysArgv[1])
	}

	err := e.tasks.RunTask(t)
	if errors.Is(err, idetask.ErrTaskExists) {
		return e.openReplaceTaskPrompt(t)
	}
	return err
}

func (e *ex) newTaskTab(ctx context.Context, args ...string) error {
	const errExpect = "command expects at least three arguments: " +
		"name, alignment, a separator '--' and the command to run"
	if len(args) < 3 {
		return errors.New(errExpect)
	}
	sysArgs, cmdAndArgs, found := strings.Cut(strings.Join(args, " "), "--")
	if !found {
		return errors.New(errExpect)
	}

	sysArgv := strings.Split(sysArgs, " ")
	cmdAndArgv := strings.Split(cmdAndArgs, " ")

	// cleanup splitting via --
	cmdAndArgv = cmdAndArgv[1:]
	sysArgv = sysArgv[:len(sysArgv)-1]

	if len(sysArgv) < 1 || len(cmdAndArgv) == 0 {
		return errors.New(errExpect)
	}

	e.log(log.DebugLevel, "newtasktab called with args %+v, sysArgs: %+v, cmdAndArgv: %+v",
		args, sysArgv, cmdAndArgv)

	t := idetask.Task{
		Name: sysArgv[0],
		Cmd:  cmdAndArgv[0],
		Args: cmdAndArgv[1:],
	}
	if len(sysArgv) > 1 {
		t.Filter = sysArgv[1]
	}

	err := e.tasks.RunTask(t)
	if errors.Is(err, idetask.ErrTaskExists) {
		return e.openReplaceTaskPrompt(t)
	}
	if err != nil {
		return err
	}

	ok := e.tasks.FocusTask(args[0])
	if !ok {
		return errors.New("could not convert task to tab")
	}
	return e.convertTab(ctx, args[0])
}

func (e *ex) stopTask(_ context.Context, args ...string) error {
	if len(args) != 1 {
		return errors.New("expected one argument with the name of the task to stop")
	}

	return e.tasks.StopTask(args[0])
}

func (e *ex) focusTask(_ context.Context, args ...string) error {
	if len(args) != 1 {
		return errors.New("expected one argument with the name of the task to focus")
	}
	if !e.tasks.FocusTask(args[0]) {
		return fmt.Errorf("task %q does not exist", args[0])
	}
	return nil
}

func (e *ex) completeTasks(
	ctx context.Context, cmd textapi.Command,
) (iterator.Iterator[string], string, error) {
	if len(cmd.Args) > 1 {
		return iterator.FromSlice[string](nil), "", nil
	}
	tasks := e.tasks.ListTasks()
	names := make([]string, len(tasks))
	for i, task := range tasks {
		names[i] = task.Name
	}
	// ensure that order is deterministic
	sort.Strings(names)

	return iterator.FromSlice(names), "", nil
}

func (e *ex) toggleCompanionTerminal() error {
	// if window is open and shell didn't exit, then close. If shell exited
	// then most likely what the user really wants is to open a new one
	// and the reason why the companion terminal is not nil is because
	// it wan't cleaned up properly.
	if e.companionTerminalWin != nil && !e.companionTerminal.IsComplete() {
		e.companionTerminalWin.Close()
		e.companionTerminalWin = nil
		return nil
	}

	width := int(float64(e.width) * 0.8)
	height := int(float64(e.height) * 0.8)

	if e.companionTerminal == nil || e.companionTerminal.IsComplete() {
		// if user exits via 'exit' command, then we must close the previous
		// terminal emulator and open a new one
		if e.companionTerminal != nil && e.companionTerminal.IsComplete() {
			_ = e.companionTerminal.Close()
		}
		var err error
		e.companionTerminal, err = e.newEmulatorHandler(nil)
		if err != nil {
			return err
		}
		e.companionTerminal.OnFocusChange(false)
	}
	cfg := browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	}
	cth := companionTerminalHandler{
		vth:      e.companionTerminal,
		Floating: browser.StaticFloating(e.companionTerminal, width, height),
	}
	win, err := e.comp.Floating(cth, cfg)
	if err != nil {
		return err
	}
	e.companionTerminalWin = win
	return nil
}

func (e *ex) initFileExplorer() error {
	prev, _ := e.comp.Focus()
	if !prev.Closed() {
		e.fileExplorerTarget = prev
	}

	if e.fileExplorerHandler == nil {
		rootURI, err := e.workspace.URI(".")
		if err != nil {
			return fmt.Errorf("file explorer: resolve root: %w", err)
		}
		buf := cell.NewBuffer()
		ignore, err := vctrl.LoadGitignore(e.workspace)
		if err != nil {
			e.log(log.WarnLevel,
				"file explorer: load gitignore: %v", err)
			ignore = vctrl.NopMatcher(false)
		}
		comp, err := fileexplorercomp.New(buf, e.workspace, rootURI, fileexplorercomp.Config{
			Icons:       e.config.Icons,
			IndentWidth: e.config.Tabspaces,
			IndentAttr:  e.config.FileExplorer.IndentAttr,
			IconAttr:    e.config.FileExplorer.IconAttr,
			Ignore:      ignore,
		})
		if err != nil {
			return fmt.Errorf("file explorer: create component: %w", err)
		}
		uri, err := workspaceapi.ParseURI(fileExplorerURI)
		if err != nil {
			return fmt.Errorf("file explorer: parse uri: %w", err)
		}
		if existing, ok := e.comp.Resource(uri); ok {
			if err := e.comp.RemoveTab(existing); err != nil {
				return fmt.Errorf("file explorer: remove stale tab: %w", err)
			}
		}
		ed, err := e.ed.Edit(context.Background(), uri, buf, false, false)
		if err != nil {
			return fmt.Errorf("file explorer: open editor: %w", err)
		}
		wrapped, err := newFileExplorerHandler(
			exFileExplorerHost{ex: e}, comp, buf, ed, uri, e.fileExplorerTarget,
			fileExplorerConfig{
				FileExplorerConfig: e.config.FileExplorer,
				SaveKey:            e.fileExplorerSaveKey(),
			},
		)
		if err != nil {
			return fmt.Errorf("file explorer: wrap component: %w", err)
		}
		e.fileExplorerHandler = wrapped
		if err := e.comp.SubscribeEvents(
			fileExplorerFSEvents, wrapped.fsEventHandler(),
		); err != nil {
			return fmt.Errorf(
				"file explorer: subscribe fs events: %w", err)
		}
	} else {
		// Keep the target in sync with the latest focus.
		e.fileExplorerHandler.SetTargetWindow(e.fileExplorerTarget)
		e.fileExplorerHandler.refreshTree()
	}

	win, err := e.comp.SplitRoot(component.AlignmentLeft, e.fileExplorerHandler)
	if err != nil {
		return fmt.Errorf("file explorer: create root split window: %w", err)
	}
	e.fileExplorerWin = win
	e.fileExplorerHandler.SetWindow(win)
	e.fileExplorerHandler.syncWidth()
	_, _ = e.comp.SetFocus(prev)
	return nil
}

// fileExplorerSaveKey resolves the key label bound to `write` so the
// explorer hint names the key the user actually has. Presets bind it
// differently per editor mode; modal binds no chord at all, and an
// empty result drops the hint rather than naming the command prompt.
func (e *ex) fileExplorerSaveKey() string {
	if e.commandPromptCfg.keyBindingHint == nil {
		return ""
	}
	return e.commandPromptCfg.keyBindingHint("write")
}

func (e *ex) fexplorer(_ context.Context, args ...string) error {
	if e.fileExplorerWin == nil || e.fileExplorerWin.Closed() {
		e.fileExplorerWin = nil
		if err := e.initFileExplorer(); err != nil {
			return err
		}
		if e.fileExplorerWin == nil || e.fileExplorerWin.Closed() {
			return errors.New("file explorer is not available")
		}
		_, _ = e.comp.SetFocus(e.fileExplorerWin)
		return nil
	}
	focus, _ := e.comp.Focus()
	if focus != e.fileExplorerWin {
		if !focus.Closed() {
			e.fileExplorerTarget = focus
		}
		_, _ = e.comp.SetFocus(e.fileExplorerWin)
	} else {
		// Closing the explorer with unflushed buffer edits
		// implicitly discards them. If an FS event arrived
		// while the user was editing, replay it now so the
		// next open shows the up-to-date tree.
		e.closeFileExplorerWindow()
	}
	return nil
}

func (e *ex) closeFileExplorerWindow() {
	prev := e.fileExplorerTarget
	if e.fileExplorerWin != nil {
		_ = e.fileExplorerWin.Close()
		e.fileExplorerWin = nil
	}
	if e.fileExplorerHandler != nil {
		e.fileExplorerHandler.onWindowClosed()
	}
	if prev != nil && !prev.Closed() {
		_, _ = e.comp.SetFocus(prev)
	}
}

func (e *ex) terminalnewtab(_ context.Context, args ...string) error {
	h, err := e.newEmulatorHandler(args)
	if err != nil {
		return err
	}

	uri := h.URI()
	t, err := e.comp.Tab(uri, e.config.Icons.Terminal, h.Title(), h)
	if err != nil {
		_ = h.Close()
		return fmt.Errorf("wm.Tab: %s", err)
	}

	tab := t.(*browser.Tab)
	tab.Subscribe((*tabSubscriber)(e))

	win := e.invokeWindow()
	if err := win.SetContent(tab); err != nil {
		_ = tab.Close()
		return err
	}
	return nil
}

func (e *ex) consolenewtab(_ context.Context, args ...string) error {
	if e.companionConsole == nil {
		workspaceURI, err := e.workspace.URI(".")
		if err != nil {
			return fmt.Errorf("workspace uri: %w", err)
		}
		consoleCfg := ideshell.Config{
			Storage:           e.storage,
			HistoryDocumentID: shellHistoryDocumentID,
			MaxHistory:        e.config.ShellMaxHistory,
			Workspace:         workspaceURI,
			Executor:          e.executor,
			Modal:             e.consoleCfg.modal,
			ModalStartInsert:  e.consoleCfg.modalStartInsert,
			Prompt:            e.consoleCfg.prompt,
		}
		h, registry := ideshell.New(
			e.emulatorConfig.ScheduleNextTick, e, e.promptEditor,
			consoleCfg,
		)
		if e.wsExecutor != nil {
			e.wsExecutor.RegisterProcessCommand(registry)
		}

		router := text.NewREPLHandler(&e.comp)
		for _, cmd := range e.comp.REPLCommands() {
			handler := textapi.REPLHandler(router)
			if cmd.Name == extensionsREPLCommandName && e.extensionsExecutor != nil {
				cmd.Commands = append(cmd.Commands, extensionsProcessManual())
				handler = extensionsREPLWithProcess{
					underlying: router,
					proc:       e.extensionsExecutor,
				}
			}
			if e.commandObserver != nil {
				handler = observingREPLHandler{
					underlying: handler,
					observer:   e.commandObserver,
					name:       cmd.Name,
					schedule:   e.sched,
				}
			}
			if err := registry.RegisterREPLCommand(cmd, handler); err != nil {
				_ = h.Close()
				return fmt.Errorf("register repl command %q: %w", cmd.Name, err)
			}
		}

		uri, err := workspaceapi.ParseURI("console:///")
		if err == nil {
			uri, err = workspaceapi.WithPath(uri, workspaceURI.Path())
			if workspaceURI.Host() != "" {
				uri, err = workspaceapi.ParseURI(
					fmt.Sprintf("console://%s%s", workspaceURI.Host(), workspaceURI.Path()),
				)
			}
		}
		if err != nil {
			_ = h.Close()
			return fmt.Errorf("parse console uri: %w", err)
		}
		e.companionConsole = h
		e.companionConsoleURI = uri
	}

	t, err := e.comp.Tab(e.companionConsoleURI, e.config.Icons.Shell, "console", e.companionConsole)
	if err != nil {
		return fmt.Errorf("wm.Tab: %s", err)
	}

	tab := t.(*browser.Tab)

	win := e.invokeWindow()
	if err := win.SetContent(tab); err != nil {
		content, cerr := win.Content()
		if cerr == nil {
			if curr, ok := content.(*browser.Tab); ok && curr == tab {
				if line := strings.TrimSpace(strings.Join(args, " ")); line != "" {
					e.companionConsole.Submit(line)
				}
				return nil
			}
		}
		return err
	}
	tab.Subscribe((*tabSubscriber)(e))
	if line := strings.TrimSpace(strings.Join(args, " ")); line != "" {
		e.companionConsole.Submit(line)
	}
	return nil
}

func (e *ex) completeConsole(ctx context.Context, cmd textapi.Command) (
	iterator.Iterator[string], string, error,
) {
	if len(cmd.Args) <= 1 {
		var prefix string
		if len(cmd.Args) == 1 {
			prefix = cmd.Args[0]
		}
		var names []string
		for _, c := range e.comp.REPLCommands() {
			if strings.HasPrefix(c.Name, prefix) {
				names = append(names, c.Name)
			}
		}
		sort.Strings(names)
		return iterator.FromSlice(names), "", nil
	}

	router := text.NewREPLHandler(&e.comp)
	it, err := router.Complete(ctx, cmd.Args[0], cmd.Args[1:])
	if err != nil {
		return iterator.FromSlice[string](nil), "", err
	}
	return it, "", nil
}

func (e *ex) terminalnew(_ context.Context, args ...string) error {
	h, err := e.newEmulatorHandler(args)
	if err != nil {
		return err
	}

	win := e.invokeWindow()
	if err := win.SetContent(h); err != nil {
		_ = h.Close()
		return err
	}
	return nil
}

func (e *ex) terminalneworsplit(_ context.Context, args ...string) error {
	t, err := e.newEmulatorHandler(args)
	if err != nil {
		return err
	}
	win := e.invokeWindow()
	content, _ := win.Content()
	_, vok := content.(vtereservoir.VTE)
	_, tok := content.(*browser.Tab)
	if !vok && !tok {
		err = win.SetContent(t)
	} else {
		_, err = e.comp.Split(browserapi.OrientationDefault, win, t)
	}
	if err != nil {
		_ = t.Close()
		return err
	}
	return nil
}

func (e *ex) panic(_ context.Context, args ...string) error {
	panic("this could be a panic")
}

func (e *ex) crash(_ context.Context, args ...string) error {
	var recurse func(int) int
	recurse = func(n int) int { return recurse(n+1) + 1 }
	_ = recurse(0)
	return nil
}

func (e *ex) datarace(_ context.Context, _ ...string) error {
	var shared int
	done := make(chan struct{}, 2)
	go func() {
		for i := range 1_000_000 {
			shared = i
		}
		done <- struct{}{}
	}()
	go func() {
		for range 1_000_000 {
			_ = shared
		}
		done <- struct{}{}
	}()
	<-done
	<-done
	return nil
}

func (e *ex) heapdump(_ context.Context, args ...string) error {
	var (
		f   *os.File
		err error
	)
	if len(args) > 0 && args[0] != "" {
		f, err = os.Create(args[0])
	} else {
		f, err = os.CreateTemp("", "rune-heap-*.dump")
	}
	if err != nil {
		return fmt.Errorf("heapdump: create file: %w", err)
	}
	rtdebug.WriteHeapDump(f.Fd())
	if err := f.Close(); err != nil {
		return fmt.Errorf("heapdump: close %q: %w", f.Name(), err)
	}
	_, _ = e.notifications.Notify(browserapi.LevelInfo,
		"heap dump written to %s", f.Name())
	return nil
}

func (e *ex) pprof(_ context.Context, args ...string) error {
	addr := "127.0.0.1:0"
	if len(args) > 0 && args[0] != "" {
		addr = args[0]
	}
	bound, err := debug.StartPProfHTTP(addr)
	if err != nil {
		return fmt.Errorf("pprof: %w", err)
	}
	_, _ = e.notifications.Notify(browserapi.LevelInfo,
		"pprof server listening on http://%s/debug/pprof/", bound)
	return nil
}

func (e *ex) windownew(_ context.Context, args ...string) error {
	orientation := browserapi.OrientationDefault
	if len(args) != 0 {
		switch args[0] {
		case "right":
			orientation = browserapi.OrientationRight
		case "down":
			orientation = browserapi.OrientationBottom
		case "left":
			orientation = browserapi.OrientationLeft
		case "up":
			orientation = browserapi.OrientationTop
		default:
			return fmt.Errorf("invalid orientation argument %q", args[0])
		}
	}
	e.windownewHandler(nil, orientation)
	return nil
}

func (e *ex) echo(_ context.Context, args ...string) error {
	if len(args) == 0 {
		return errors.New("expected one argument with the sequence of keys")
	}
	var keys []echoKey
	for _, arg := range args {
		sub, err := parseEchoKeys(arg)
		if err != nil {
			return fmt.Errorf("invalid syntax: %v", err)
		}
		keys = append(keys, sub...)
	}
	var err error
	keys, err = e.expandEchoRegisters(keys, nil)
	if err != nil {
		return err
	}
	ok := true
	for i := 0; i < len(keys); i++ {
		keyComb := keys[i]
		if keyComb.instructWait {
			ok = ok && e.publishEvent(term.Event{
				Type: term.EventInterrupt,
				UserFunc: func() {
					e.Wait()
				},
			})
			continue
		}
		if keyComb.instructPrompt {
			if i == len(keys)-1 && e.cmd != nil {
				if err := e.closeCommandPrompt(); err != nil {
					return err
				}
				continue
			}
			e.openCommandPrompt()
			continue
		}
		ok = ok && e.publishEvent(term.Event{
			Type: term.EventKey,
			Ch:   keyComb.Ch,
			Mod:  keyComb.Mod,
			Key:  keyComb.Key,
		})
	}
	if !ok {
		return errors.New("could not publish all events to the event loop")
	}
	return nil
}

func (e *ex) expandEchoRegisters(keys []echoKey, stack []string) ([]echoKey, error) {
	ret := make([]echoKey, 0, len(keys))
	for _, key := range keys {
		if key.instructReg == "" {
			ret = append(ret, key)
			continue
		}
		if e.macro != nil && e.macro.IsRecording() && e.macro.RegisterID() == key.instructReg {
			return nil, fmt.Errorf("cannot echo register %q while it is actively being recorded", key.instructReg)
		}
		for _, seen := range stack {
			if seen == key.instructReg {
				cycle := append(append([]string{}, stack...), key.instructReg)
				return nil, fmt.Errorf("recursive register expansion detected: %s", strings.Join(cycle, " -> "))
			}
		}
		data, err := e.clip.Paste(key.instructReg)
		if err != nil {
			return nil, fmt.Errorf("expand register %q: paste register %q: %w", key.instructReg, key.instructReg, err)
		}
		regKeys, err := parseEchoKeys(data.Text)
		if err != nil {
			return nil, fmt.Errorf("expand register %q: parse register %q: %w", key.instructReg, key.instructReg, err)
		}
		expanded, err := e.expandEchoRegisters(regKeys, append(stack, key.instructReg))
		if err != nil {
			return nil, fmt.Errorf("expand register %q: %w", key.instructReg, err)
		}
		ret = append(ret, expanded...)
	}
	return ret, nil
}

func (e *ex) runCommand(cmd string, args []string) (quit bool, err error) {
	if len(args) == 0 {
		line, cerr := strconv.Atoi(cmd)
		if cerr == nil {
			err = e.moveFocusCursor(line - 1)
			return
		}
		return e.exit, e.dispatchCommand(cmd)
	}

	return e.exit, e.dispatchCommand(cmd, args...)
}

func (e *ex) setError(err error) {
	_, _ = e.notifications.Notify(browserapi.LevelError, fmt.Sprintf("%s", err))
}

func (e *ex) handleCommandEvent(ev term.Event) bool {
	if ev.KeyComb() == e.config.CommandEvent {
		e.openCommandPrompt()
		return true
	}
	return false
}

func (e *ex) onFocusChange(inFocus bool) {
	handler, _ := e.comp.Browser().Focus().Content()
	onFocusChangeHandler(handler, inFocus)
}

func (e *ex) handleEvent(ev term.Event) (
	exit, handled bool,
) {
	if ev.Type == term.EventMouse {
		_, handled = e.comp.Browser().Handle(ev)
		return
	}

	if inFocus := ev.Type == term.EventFocus; inFocus || ev.Type == term.EventUnfocus {
		e.log(log.DebugLevel, "focus event: inFocus: %t", inFocus)
		handler, _ := e.invokeWindow().Content()
		onFocusChangeHandler(handler, inFocus)
		handled = true
		return
	}

	if ev.Type != term.EventKey {
		e.log(log.DebugLevel, "delegating non-key event: %+v", ev)
		_, handled = e.comp.Browser().Handle(ev)
		return
	}

	// If ex is configured with non character
	// command mode trigger event, then this takes
	// precedence over any other event
	if e.config.CommandEvent.Key != 0 || (ev.Ch != 0 && ev.Mod != 0) {
		handled = e.handleCommandEvent(ev)
		if handled {
			return
		}
	}

	keyComb := ev.KeyComb()

	if e.cancelPartialReissue == nil {
		// allow handler to take precedence over key bindings (i.e. vi is in
		// insert mode and some key bindings shouldn't apply)
		// but only do it when the previous event didn't match (i.e. vi `ma`
		// should bind to creating bookmark 'a', so 'a' shouldn't be
		// delegated to handler, otherwise it'll handle it and switch to insert mode.
		_, handled = e.comp.Browser().Handle(ev)
		if handled {
			return
		}
	}

	var seq thandler.Sequence
	var match thandler.SequenceMatchResult
	// err nil indicates that match is still valid as timer hasn't expired
	// and it was not canceled yet or simply it hasn't even started and
	// this is first event in sequence.
	if e.ctxPartialReissue.Err() == nil {
		seq, match = e.sequencer.Sequence(keyComb)
	}

	var cmdsAndArgs [][]string
	var ok bool
	switch match {
	case thandler.SequenceMatch:
		cmdsAndArgs, ok = e.config.CommandSequenceBindings[seq]
		if !ok {
			panic("key sequencer matched but no command configured")
		}
		if e.cancelPartialReissue != nil {
			e.cancelPartialReissue()
			e.cleanPartialReissueState()
		}
	case thandler.SequencePartialMatch:
		if e.cancelPartialReissue == nil {
			// Record the pending prefix so the next key bypasses the
			// editor-first Handle above (see the cancelPartialReissue guard)
			// and reaches the sequencer to complete the sequence.
			e.reissueEvent = ev
			if keyComb.Mod != 0 {
				// A modifier-bearing prefix (e.g. <ctrl-x>) is not ambiguous
				// with plain typed input, so there is nothing to re-issue on
				// a timeout: wait indefinitely for the second key. Use a
				// plain cancelable context with no deadline so
				// ctxPartialReissue.Err() stays nil and never re-issues.
				e.ctxPartialReissue, e.cancelPartialReissue =
					context.WithCancel(context.Background())
				return
			}
			// A bare-character prefix (e.g. vi's `d`) also has a standalone
			// meaning, so set a timer to re-issue it if the user does not
			// complete the sequence before the timer expires.
			e.ctxPartialReissue, e.cancelPartialReissue = context.WithTimeout(
				context.Background(), e.config.SequencerTimeout+reissuePadding)
			waitCtx := e.ctxPartialReissue
			reissueEvent := e.reissueEvent
			go debug.CapturePanicReport(func() {
				<-waitCtx.Done()
				if waitCtx.Err() == context.DeadlineExceeded {
					// timer expired, reissue event because
					// user didn't send a matching key combination.
					e.publishEvent(reissueEvent)
				}
			})
			return
		}
		// this is a re-issue so process normally
		_, handled = e.comp.Browser().Handle(ev)
		if handled {
			return
		}
	default:
		if e.cancelPartialReissue != nil {
			err := e.ctxPartialReissue.Err()
			e.cancelPartialReissue()
			e.cleanPartialReissueState()
			// A bare-character prefix (e.g. vi's `d`) also has a standalone
			// meaning, so when the second key does not complete a sequence
			// re-dispatch the prefix right before this event. A
			// modifier-bearing prefix (e.g. <ctrl-x>) has no standalone
			// meaning and is simply dropped.
			if err == nil && e.reissueEvent.KeyComb().Mod == 0 {
				e.log(log.TraceLevel, "no sequence match: re-dispatching previous event %q",
					e.reissueEvent.KeyComb())
				_, _ = e.comp.Browser().Handle(e.reissueEvent)
			}
			_, handled = e.comp.Browser().Handle(ev)
			if handled {
				return
			}
		}
		cmdsAndArgs, _ = e.comp.CommandKeyBinding(keyComb)
	}

	// if match is a single "" command, then this is effectively unsetting
	// a key binding.
	if len(cmdsAndArgs) == 1 && len(cmdsAndArgs[0]) == 1 && cmdsAndArgs[0][0] == "" {
		cmdsAndArgs = nil
	}

	// dispatch command or sequence of commands
	for _, cmdAndArgs := range cmdsAndArgs {
		quit, err := e.runCommand(cmdAndArgs[0], cmdAndArgs[1:])
		if err != nil {
			e.setError(err)
		}
		handled = true
		if quit {
			return quit, handled
		}
	}

	// If ex is configured with character
	// command mode trigger event (i.e. ':')
	// then we assume that the underlying editor is
	// a modal editor, and so does not handle
	// the command trigger event in its "initial" mode
	// (in vi terms, this would be normal mode).
	handled = e.handleCommandEvent(ev)
	if handled {
		return
	}
	return
}

func (e *ex) resetCommandList(cmd *command.Prompt) {
	// commands can be registered dynamicall via Editor.Register:
	// compile a new list every time we switch to command mode
	commands := e.comp.Commands()
	commands = append(commands, e.aliasExpander.Aliases()...)
	sort.Slice(commands, func(i, j int) bool {
		return commands[i].Name < commands[j].Name
	})
	cmd.Reset(commands)
}

func (e *ex) openCommandPrompt() {
	e.newCommandPrompt(func(cmd *command.Prompt) {
		e.resetCommandList(cmd)
	})
}

func (e *ex) openCommandHistoryPrompt(_ context.Context, _ ...string) error {
	e.newCommandPrompt(func(cmd *command.Prompt) {
		cmd.ResetHistory()
	})
	return nil
}

func (e *ex) newCommandPrompt(reset func(*command.Prompt)) {
	// Retire any prompt still installed before building the replacement.
	// Closing the previous floating window runs its onClose callback while
	// e.cmd still refers to the old prompt, so that callback clears the old
	// prompt state and stops the old shader. Detaching here prevents the old
	// window and its cell/undo buffers from leaking and keeps a stale close
	// callback from clobbering the prompt we are about to install.
	if e.cmdWin != nil && !e.cmdWin.Closed() {
		_ = e.cmdWin.Close()
	}
	e.cmdWin = nil
	// A latch left set by the previous prompt must not suppress the
	// first outside click delivered to the new one.
	e.promptMouseDown = false

	commandCfg := command.DefaultConfig()
	commandCfg.NoMarkdown = false
	commandCfg.MaxHistory = e.config.CommandMaxHistory
	commandCfg.HistoryCycleKey = e.config.CommandEvent
	commandCfg.HistoryToggleKey = e.config.CommandHistoryKey
	commandCfg.MatchedTextAttr = e.config.CommandOverlay.MatchedTextAttr
	commandCfg.FocusElementAttr = e.config.CommandOverlay.FocusElementAttr
	commandCfg.ElementAttr = e.config.CommandOverlay.ElementAttr
	commandCfg.ManualAttr = e.config.CommandOverlay.ManualAttr
	commandCfg.DocumentID = commandHistoryDocumentID
	commandCfg.FrameCharSet = e.config.FrameCharSet
	commandCfg.FrameAttr = e.config.FrameAttr
	commandCfg.ShowManual = e.config.CommandOverlay.ShowManual
	commandCfg.ShowProgressHint = e.config.CommandOverlay.ShowProgressHint
	commandCfg.Sync = e.syncCommandPrompt
	commandCfg.Editor = e.promptEditor
	commandCfg.KeyBindingHint = e.commandPromptCfg.keyBindingHint
	commandCfg.KeyBindingHintAttr = e.commandPromptCfg.keyBindingHintAttr
	commandCfg.KeyBindingHintFocusAttr = e.commandPromptCfg.keyBindingHintFocusAttr
	cmd := command.NewPrompt(e.storage, e, e, e, []command.Manual{}, commandCfg)

	commandHandler := newCommandPromptHandler(cmd, e, func() error {
		err := cmd.Close()
		if cmd == e.cmd {
			e.cmd = nil
			e.stopPromptShader()
		}
		return err
	})
	reset(cmd)

	e.cmdWin = e.cmdV.C.Floating(commandHandler,
		browserapi.FloatingConfig{
			Offset:    term.Coordinates{Y: int(float64(e.height) * 0.2)},
			Alignment: component.AlignmentHorizontallyCentered,
		})
	e.cmd = cmd
	if e.commandPromptCfg.shader.enabled {
		e.startPromptShader()
	}
}

func (e *ex) handlePrompt(ev term.Event) (exit, handled bool) {
	if ev.Type == term.EventMouse {
		switch ev.Key {
		case term.MouseLeft, term.MouseMiddle, term.MouseRight:
			press := !e.promptMouseDown
			e.promptMouseDown = true
			if press && e.promptClickOutside(ev) {
				if err := e.closeCommandPrompt(); err != nil {
					e.setError(err)
				}
				return false, true
			}
		case term.MouseRelease:
			e.promptMouseDown = false
		}
		_, handled = e.cmdV.Handle(ev)
		return
	}

	// special handling of paste on prompt via key binding
	// which is the only keybinding that we want to enable while
	// prompt is active.
	if ev.Type != term.EventKey {
		_, handled = e.cmdV.Handle(ev)
		return
	}

	cmdsAndArgs, ok := e.comp.CommandKeyBinding(ev.KeyComb())
	if ok && len(cmdsAndArgs[0]) == 1 && cmdsAndArgs[0][0] == cmdClipboardPaste {
		err := e.pasteFromClipboard(context.Background())
		if err != nil {
			_, _ = e.Browser().Notify(browserapi.LevelError, "%v", err)
		}
		return
	}

	_, handled = e.cmdV.Handle(ev)
	return
}

// Handle satisfies tui.Handler.
func (e *ex) Handle(ev term.Event) (exit, handled bool) {
	exit, handled = e.container.Handle(ev)
	if handled {
		return
	}
	if e.cmd != nil {
		_, handled = e.handlePrompt(ev)
	} else {
		_, handled = e.handleEvent(ev)
		currFocus, _ := e.comp.Focus()
		if e.fullscreenID != 0 && currFocus.WindowID() != e.fullscreenID {
			_ = e.windowtogglemaximize(context.Background())
		}
	}
	return e.exit, handled
}

// Cursor satisfies tui.Handler.
func (e *ex) Cursor() (
	pos term.Coordinates, style term.CursorStyle, show bool,
) {
	return e.focusHandler().Cursor()
}

// Selection satisfies tui.Handler.
func (e *ex) Selection() (string, bool) {
	return e.focusHandler().Selection()
}

// windowRows satisfies windowRows: the rows this workspace's windows
// occupy, which exclude the bars laid out around them.
func (e *ex) windowRows() (top, rows int) {
	b := e.comp.Browser()
	_, height := b.WindowManagerSize()
	return b.WindowManagerPosition().Y, height
}

// setRightInset resizes the column reserved along the right edge, both
// in the editor layout and for the notifications floating over it.
func (e *ex) setRightInset(cells int) {
	e.comp.SetRightInset(cells)
	e.container.SetRightInset(cells)
	e.Resize(e.width, e.height)
}

// Resize satisfies tui.Component
func (e *ex) Resize(width, height int) {
	e.height = height
	e.width = width
	e.tasks.SetMaxWidthHeight(width, height)
	if e.reservoir != nil {
		e.reservoir.Resize(width, height)
	}
	e.container.Resize(width, height)
	// if a top bar is added we don't reposition
	// command window until the next resize, but that's
	// acceptable because bars are added once
	offset := e.comp.WindowManagerPosition()
	e.cmdV.Move(offset)

	// set correct width and height for dynamically resized
	// components
	width = max(0, width-offset.X)
	height = max(0, height-offset.Y)
	e.cmdV.Resize(width, height)
	if e.promptShader != nil {
		e.promptShader.Resize(e.width, e.height)
	}
}

// Draw satisfies tui.Component
func (e *ex) Draw(w term.Writer) {
	switch {
	case e.cmd == nil:
		e.container.Draw(w)
	case e.promptShader != nil:
		e.promptShader.Draw(w)
	default:
		e.drawCmdPrompt(w)
	}
}

// setDefaultAttr stores the live theme default attributes and forwards
// them to the browser window manager so grayscale dimming (both the
// unfocused-window path and the command prompt) resolves a ColorDefault
// foreground against the current theme.
func (e *ex) setDefaultAttr(attr term.Attributes) {
	e.defAttr = attr
	e.comp.Browser().SetDefaultAttr(attr)
}

func (e *ex) drawCmdPrompt(w term.Writer) {
	// temporarily disable auto-dimming based on focus so we
	// can pass a DimWriter below and dim everything.
	prev := e.comp.SetDim(false)
	defer e.comp.SetDim(prev)

	if e.config.Config.Dim {
		if e.config.Config.BW {
			e.container.Draw(tterm.BWWriter(w, e.defAttr))
		} else {
			e.container.Draw(tterm.DimWriter(w))
		}
	} else {
		e.container.Draw(w)
	}
	e.cmdV.C.DrawWindow(e.cmdWin, &component.VirtualWriter{
		Writer: w,
		Offset: e.cmdV.Position(),
		Height: e.cmdV.Height(),
		Width:  e.cmdV.Width(),
	})
}

func (e *ex) startPromptShader() {
	if e.promptShader != nil {
		_ = e.promptShader.Close()
		e.promptShader = nil
	}
	// cmdWin.Position is relative to the inner browser's window
	// manager, so reaching screen coordinates requires stacking
	// the editor's outer window-manager origin (cmdV.Position)
	// and the inner browser's wm origin.
	wmOff := e.cmdV.C.WindowManagerPosition()
	offset := e.cmdV.Position()
	offset.X += wmOff.X
	offset.Y += wmOff.Y
	e.promptShader = newPromptShader(
		e.config.FocusFrameCharSet, e.config.FrameAttr,
		e.cmdWin, offset, drawFunc(e.drawCmdPrompt), e,
		e.commandPromptCfg.shader,
	)
	e.promptShader.Resize(e.width, e.height)
}

// stopPromptShader tears down any live promptShader. Safe to call
// when none is set.
func (e *ex) stopPromptShader() {
	if e.promptShader == nil {
		return
	}
	_ = e.promptShader.Close()
	e.promptShader = nil
}

// promptClickOutside reports whether ev lands outside the command
// prompt's screen rect. cmdWin.Position() is relative to the inner
// browser's window manager, so reaching screen coordinates requires
// stacking the editor's outer window-manager origin (cmdV.Position)
// and the inner browser's wm origin, mirroring startPromptShader.
func (e *ex) promptClickOutside(ev term.Event) bool {
	if e.cmdWin == nil || e.cmdWin.Closed() {
		return false
	}
	width, height := e.cmdWin.Width(), e.cmdWin.Height()
	if width <= 0 || height <= 0 {
		return false
	}
	pos := e.cmdWin.Position()
	wmOff := e.cmdV.C.WindowManagerPosition()
	off := e.cmdV.Position()
	pos.X += off.X + wmOff.X
	pos.Y += off.Y + wmOff.Y
	return ev.MouseX < pos.X || ev.MouseY < pos.Y ||
		ev.MouseX >= pos.X+width || ev.MouseY >= pos.Y+height
}

// closeCommandPrompt dismisses the command prompt if one is open. It is
// a no-op otherwise. Closing the floating window runs the prompt's
// onClose callback, which clears e.cmd and stops the shader; the direct
// e.cmd.Close() fallback covers the rare case where the window is
// already gone. Callers dispatching a command that opens its own prompt
// use this first so the new surface is not hidden behind the
// always-on-top command prompt overlay.
func (e *ex) closeCommandPrompt() error {
	if e.cmd == nil {
		return nil
	}
	if e.cmdWin != nil && !e.cmdWin.Closed() {
		return e.cmdWin.Close()
	}
	return e.cmd.Close()
}

// Editor returns the underlying Editor implementation.
func (e *ex) Editor() text.Editor {
	return e.editorObserver
}

// Browser returns the underlying browser.Browser implementaiton.
func (e *ex) Browser() browser.Browser {
	return &e.comp
}

// Close closes the resources associated with this browser.
func (e *ex) Close() (ret error) {
	if e.closed {
		return nil
	}
	e.closed = true
	e.bgCancel()
	e.sequencer.Reset()
	e.stopTerminal()
	if err := e.comp.Close(); err != nil {
		ret = multierror.Append(ret, err)
	}
	if e.reservoir != nil {
		if err := e.reservoir.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if e.cancelPartialReissue != nil {
		e.cancelPartialReissue()
		e.cleanPartialReissueState()
	}
	// we do not call Close on terminal when window is closed
	if e.companionTerminal != nil {
		_ = e.companionTerminal.Close()
		e.companionTerminal = nil
	}
	if e.companionConsole != nil {
		_ = e.companionConsole.Close()
		e.companionConsole = nil
		e.companionConsoleURI = workspaceapi.URI{}
	}
	if e.fileExplorerHandler != nil {
		if err := e.fileExplorerHandler.closeEditor(); err != nil {
			ret = multierror.Append(ret, err)
		}
		e.fileExplorerHandler = nil
	}
	if err := e.closeCommandPrompt(); err != nil {
		ret = multierror.Append(ret, err)
	}
	e.stopPromptShader()
	if err := e.container.Close(); err != nil {
		ret = multierror.Append(ret, err)
	}
	if e.terminalStorage != nil {
		if err := e.terminalStorage.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
		e.terminalStorage = nil
	}
	return ret
}

func (e *ex) cleanPartialReissueState() {
	e.cancelPartialReissue = nil
	e.ctxPartialReissue = context.Background()
}

// stopTerminal releases the pty-resize worker. It is safe to call on a
// partially constructed ex and more than once.
func (e *ex) stopTerminal() {
	if e.terminal != nil {
		e.terminal.Close()
	}
}

func (e *ex) focusHandler() tui.Handler {
	if e.cmd != nil && !e.isPromptDispatch {
		return &e.cmdV
	}
	return e.comp.Browser()
}

type pluginAdapter struct {
	pluginHandler
	win browser.Window
}

type workspaceExecutorAdapter struct {
	e schemeapi.Executor
}

func (a workspaceExecutorAdapter) Start(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	return a.e.StartCommand(ctx, cmd)
}

func (a workspaceExecutorAdapter) Signal(
	pid workspaceapi.Pid, sig syscall.Signal,
) error {
	return a.e.Signal(pid, sig)
}

func (a workspaceExecutorAdapter) Close() error {
	return a.e.Close()
}

func (h *pluginAdapter) Close() error {
	if !h.win.Closed() {
		_ = h.win.Close()
	}

	// ephemeral handlers are closed when focus changes
	// in component. So adding the following line here
	// should handle all cases
	defer h.pluginHandler.OnFocusChange(false)

	return h.pluginHandler.Close()
}

type tabSubscriber ex

func (e *tabSubscriber) OnFocus(t *browser.Tab) {
	onFocusChangeTab(t, true)
}

func (e *tabSubscriber) OnFree(t *browser.Tab) {
	if e.companionConsole != nil && t.URI().String() == e.companionConsoleURI.String() {
		e.companionConsole = nil
		e.companionConsoleURI = workspaceapi.URI{}
	}
	onFocusChangeTab(t, false)
}

type windowSubscriber ex

func (e *windowSubscriber) OnFocus(prev, curr thandler.Window) {
	onFocusChange(prev, false)
	onFocusChange(curr, true)
}

func onFocusChange(win thandler.Window, isInFocus bool) {
	if win == (thandler.Window{}) {
		return
	}

	onFocusChangeHandler(win.Content(), isInFocus)
}

func onFocusChangeHandler(handler tui.Handler, isInFocus bool) {
	if t, ok := handler.(*browser.Tab); ok {
		onFocusChangeTab(t, isInFocus)
		return
	}

	// if it's not a tab, it can be either an internal browser type
	// (through handler.Window, which doesn't unwrap) or directly through browser
	// which doesn unwrap the internal browser handler.
	attempts := make([]tui.Handler, 1, 2)
	attempts[0] = handler

	internal, ok := handler.(interface{ Content() browserapi.Handler })
	if ok {
		attempts = append(attempts, internal.Content())
	}
	for _, content := range attempts {
		if t, ok := content.(*pluginAdapter); ok {
			t.pluginHandler.OnFocusChange(isInFocus)
			return
		}
		if t, ok := content.(companionTerminalHandler); ok {
			t.vth.OnFocusChange(isInFocus)
			return
		}
	}
}

func onFocusChangeTab(t *browser.Tab, isInFocus bool) {
	emulator, ok := t.Handler().(vtereservoir.VTE)
	if !ok {
		return
	}
	emulator.OnFocusChange(isInFocus)
}

var _ component.Scrollable = vteAdapter{}

// argsMatchEmulatorShell reports whether the requested
// terminal command is satisfied by the configured shell, in which
// case the warm reservoir can serve it. An empty argument list
// always falls back to the default shell so it matches.
func argsMatchEmulatorShell(cmdAndArgs, configured []string) bool {
	if len(cmdAndArgs) == 0 {
		return true
	}
	return slices.Equal(cmdAndArgs, configured)
}

// adapts vte.Handler to vtereservoir.VTE
type vteAdapter struct {
	*vte.Handler
}

func (v vteAdapter) SetDefaultAttributes(attr term.Attributes) {
	v.Handler.SetDefaultAttributes(attr)
}

func (v vteAdapter) IsComplete() bool {
	return v.Component().IsComplete()
}

func (v vteAdapter) URI() workspaceapi.URI {
	return v.Component().URI()
}

func (v vteAdapter) Title() string {
	return v.Component().Title()
}

func (v vteAdapter) UsedAlternateBuffer() bool {
	return v.Component().UsedAlternateBuffer()
}

func (v vteAdapter) ClearPrimaryBuffer() bool {
	return v.Component().ClearPrimaryBuffer()
}

var _ component.Scrollable = companionTerminalHandler{}
var _ vtereservoir.VTE = companionTerminalHandler{}

func (e *ex) fileExplorerHandlerInFocus() (*fileExplorerHandler, bool) {
	if e.fileExplorerWin == nil || e.fileExplorerWin.Closed() {
		return nil, false
	}
	focus, _ := e.comp.Focus()
	if focus != e.fileExplorerWin {
		return nil, false
	}
	content, err := focus.Content()
	if err != nil {
		return nil, false
	}
	h, ok := content.(*fileExplorerHandler)
	return h, ok
}

// Aids in ensure that Close is not called when window is closed:
// session should remain open as long as this workspace is not closed.
// Also ensures that we can identify companionTerminal on window focus
// change to deliver on focus calls to underlying vte.Handler
type companionTerminalHandler struct {
	browser.Floating
	vth vtereservoir.VTE
}

// SeekUp satisfies component.Scrollable.
func (c companionTerminalHandler) SeekUp() bool {
	return c.vth.SeekUp()
}

// SeekDown satisfies component.Scrollable.
func (c companionTerminalHandler) SeekDown() bool {
	return c.vth.SeekDown()
}

// SeekOffset satisfies component.Scrollable.
func (c companionTerminalHandler) SeekOffset() int {
	return c.vth.SeekOffset()
}

// MaxSeekOffset satisfies component.Scrollable.
func (c companionTerminalHandler) MaxSeekOffset() int {
	return c.vth.MaxSeekOffset()
}

func (c companionTerminalHandler) OnFocusChange(inFocus bool) {
	c.vth.OnFocusChange(inFocus)
}

func (c companionTerminalHandler) SetDefaultAttributes(attr term.Attributes) {
	c.vth.SetDefaultAttributes(attr)
}

func (c companionTerminalHandler) Snapshot() (vte.Snapshot, error) {
	return c.vth.Snapshot()
}

func (c companionTerminalHandler) RestoreFromSnapshot(snapshot vte.Snapshot) error {
	return c.vth.RestoreFromSnapshot(snapshot)
}

func (c companionTerminalHandler) IsComplete() bool {
	return c.vth.IsComplete()
}

func (c companionTerminalHandler) URI() workspaceapi.URI {
	return c.vth.URI()
}

func (c companionTerminalHandler) Title() string {
	return c.vth.Title()
}

func (c companionTerminalHandler) UsedAlternateBuffer() bool {
	return c.vth.UsedAlternateBuffer()
}

func (c companionTerminalHandler) ClearPrimaryBuffer() bool {
	return c.vth.ClearPrimaryBuffer()
}

func (c companionTerminalHandler) Close() error {
	return nil
}

// openMarkdownFloating renders md as a read-only markdown view in a
// centered floating window pinned to a fixed width. It is shared by the
// cheatsheet and keybindings commands so both views line up.
func (e *ex) openMarkdownFloating(md, title string, width int) error {
	mdCfg := e.config.Markdown
	mdCfg.HeaderPrefix = false
	mdComp, err := markdown.NewWithConfig(md, mdCfg)
	if err != nil {
		return fmt.Errorf("new markdown component: %w", err)
	}
	mdh := hmarkdown.New(mdComp, hmarkdown.WithOnLinkClick(openCheatsheetLink))
	span := handler.NewSpan(mdh, component.SpanConfig{
		PadHorizontal:    2,
		ContentAlignment: component.AlignmentCentered,
	})

	var win browser.Window
	floating := browser.FuncFloatingHandler(span, func() error {
		defer win.Close() //nolint:errcheck
		return mdh.Close()
	})
	staticWidth := browser.FuncFloating(floating, func() (int, int) {
		_, h := floating.Dimensions()
		return width, h
	})
	win, err = e.comp.Floating(staticWidth, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
		Title:     title,
	})
	return err
}

// cheatsheet renders a keys-first cheatsheet from the user's resolved key
// bindings and editor mode and opens it as a read-only markdown view in a
// centered floating window.
func (e *ex) cheatsheet(_ context.Context, _ ...string) error {
	md, err := renderCheatsheet(e.config, e.editorModeModal, e.editorMode, e.editorAutoSave)
	if err != nil {
		return err
	}
	return e.openMarkdownFloating(md, "Cheatsheet", cheatsheetWidth)
}

// keybindings renders the user's currently configured key bindings as a
// Markdown table and opens it in a centered floating window, mirroring
// the cheatsheet view.
func (e *ex) keybindings(_ context.Context, _ ...string) error {
	md, err := renderKeyBindings(e.config, e.comp.Commands(), e.editorMode, runtime.GOOS)
	if err != nil {
		return err
	}
	return e.openMarkdownFloating(md, "Key bindings", keybindingsWidth)
}

func (e *ex) gitshow(ctx context.Context, _ ...string) error {
	_, err := e.openGitshow(ctx)
	return err
}

func (e *ex) openGitshow(ctx context.Context) (text.Handler, error) {
	uri, h, ok := e.handlerInFocus()
	if !ok {
		return nil, errors.New("gitshow: no file in focus")
	}
	rel := workspaceapi.RelPath(e.workspaceURI, uri)
	// The diff is computed against disk, so a dirty buffer would make
	// the popup and the gutter marks describe different bytes.
	if dirty, _ := e.comp.IsDirty(uri); dirty {
		return nil, fmt.Errorf(
			"gitshow: %s has unsaved changes; write it first", rel)
	}
	diff, err := e.svc.Diff(ctx, uri)
	if err != nil {
		return nil, fmt.Errorf("gitshow: diff %s: %w", rel, err)
	}
	content, err := readFile(e.workspace, uri)
	if err != nil {
		return nil, fmt.Errorf("gitshow: read %s: %w", rel, err)
	}
	hunks := vctrl.UnifiedHunks(diff, content, gitshowContextLines)
	if len(hunks) == 0 {
		// A service with nothing to report returns an empty FileDiff
		// and no error, so this is the only place it surfaces.
		return nil, fmt.Errorf("gitshow: no changes in %s", rel)
	}

	buf, anchors := vctrl.UnifiedBuffer(hunks, rel, rel)
	// The real file URI, so snippets parse as the file's language
	// rather than as a diff.
	vctrl.HighlightSnippets(ctx, buf.RawCells(),
		vctrl.UnifiedSnippets(hunks, anchors, uri), e.parser, gitshowGutter)

	diffURI, err := e.gitshowURI(rel)
	if err != nil {
		return nil, fmt.Errorf("gitshow: %w", err)
	}
	edh, err := e.ed.Edit(text.WithBars(ctx, text.BarOptions{
		DisableAuxBar:   true,
		DisableIconsBar: true,
		StatusBar: &text.StatusBarOverride{
			Workspace:  gitshowBaseURI(),
			GitService: gitshowService{Service: e.svc, file: uri, diff: diff},
		},
	}), diffURI, buf, true /* readOnly */, false)
	if err != nil {
		return nil, fmt.Errorf("gitshow: open editor: %w", err)
	}

	edh.SetLocationList(textapi.LocationPriorityInfo, gitshowLocationList,
		textapi.LocationSlice(gitshowLocations(
			hunks, anchors, gitshowLabel(ctx, e.svc, uri))))
	edh.SetCursorAtScroll(term.Coordinates{
		Y: anchors[gitshowSeek(hunks, h.CursorAtScroll().Y+1)].Row,
	})

	var win browser.Window
	bhandler := browser.FuncHandler(
		gitshowKeyHandler(edh, e.editorModeModal), func() error {
			defer win.Close() //nolint:errcheck
			return edh.Close()
		})
	win, err = e.comp.Floating(
		browser.FuncFloating(bhandler, func() (int, int) {
			w, height := edh.Dimensions()
			// dimensions does not account for the row the editors
			// draw the cursor's location message over.
			return min(w, gitshowMaxWidth), height + 1
		}),
		browserapi.FloatingConfig{
			Alignment: component.AlignmentCentered,
			Title:     "git diff · " + rel,
		})
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("gitshow: open window: %w", err), edh.Close())
	}
	return edh, nil
}

func (e *ex) gitshowURI(rel string) (workspaceapi.URI, error) {
	e.gitshowSeq++
	joined := workspaceapi.Join(gitshowBaseURI(), rel+".diff")
	return workspaceapi.ParseURI(
		fmt.Sprintf("%s?n=%d", joined.String(), e.gitshowSeq))
}
