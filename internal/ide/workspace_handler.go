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
	"io"
	"maps"
	"net/url"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	handlerapi "github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/component/notifications"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/extension/extensionv2"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/handler/command"
	handlermarkdown "unstable.build/rune/internal/handler/markdown"
	"unstable.build/rune/internal/handler/search"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/idecursor"
	"unstable.build/rune/internal/ide/idedebug"
	"unstable.build/rune/internal/ide/idehistory"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
	"unstable.build/rune/internal/ide/idemacro"
	"unstable.build/rune/internal/ide/idenotice"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idescavenger"
	"unstable.build/rune/internal/ide/ideshell/debugshell"
	"unstable.build/rune/internal/ide/ideshell/workspaceshell"
	"unstable.build/rune/internal/ide/llmshell"
	"unstable.build/rune/internal/ide/pkgshell"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/symboldb"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/ide/vctrl/gogit"
	"unstable.build/rune/internal/llm/llamaserver"
	"unstable.build/rune/internal/llm/llmrouter"
	"unstable.build/rune/internal/localstorage"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/emacs"
	"unstable.build/rune/internal/text/exoeditor"
	"unstable.build/rune/internal/text/exofallback"
	"unstable.build/rune/internal/text/helix"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/text/textrpc"
	"unstable.build/rune/internal/text/vi"
	"unstable.build/rune/internal/workspace"
)

const (
	cmdSwitchToWorkspace = "workspacefocus"
	cmdMoveWorkspace     = "workspacemove"
	cmdCloseWorkspace    = "workspaceclose"
	cmdReloadWorkspace   = "workspacereload"
	cmdAddWorkspace      = "workspaceopen"
	cmdRenameWorkspace   = "workspacerename"
	cmdWorkspaceReady    = "workspaceready"
	cmdExtensionReady    = "extensionready"
	cmdMacroRecord       = "record"
	workspaceSlots       = 9
)

var (
	defaultModalCommandKey    = term.KeyComb{Ch: ':'}
	defaultModelessCommandKey = term.KeyComb{Key: term.KeySpace, Mod: term.ModCtrl}
)

var _ text.EventPublisher = (*workspaceManagerHandler)(nil)

type workspaceManagerHandler struct {
	mu         sync.Locker
	pkgmanager *pkgManager
	// gitRemoteURL, when non-nil, overrides how git package IDs map to
	// git remote URLs. Production leaves it nil (clone from the host in
	// the id); tests point it at a local fixture git server.
	gitRemoteURL func(pkgID string) string
	// localExecutor is a stable, IDE-scoped executor proxy handed to the
	// llama-server backend at router construction. Its inner executor is
	// swapped to the focused workspace's executor at each setExecutor site,
	// mirroring the per-ex currentExecutor pattern. Before any workspace
	// installs an executor it returns a clean "no executor installed" error
	// rather than nil-derefing.
	localExecutor           *currentExecutor
	exitPromptOpen          bool
	scheduleNextTick        func(func()) bool
	confirmedForceExit      bool
	notifications           *notisManager
	storage                 storageapi.Service
	ideStorage              storageapi.Service
	workspace               workspace.WorkspaceManager
	clip                    clipboard.Register
	macro                   *idemacro.Recorder
	macroPlayer             *idemacro.Player
	events                  *eventRouter
	tabsClickCallback       func(int) bool
	extensionRunner         ExtensionsRunner
	trust                   *pkgtrust.Store
	sixDir                  string
	extensionsStorage       storageapi.Service
	configPath              string
	llmRouter               *llmrouter.Router
	frameCharSet            component.FrameCharSet
	tabBarOffset            int
	rightInset              int
	tabBarHeight            int
	builtinExtensions       map[string]Extension
	workspaceConfigFilename string
	tabAttentionNameSuffix  string
	workspaceBarKind        workspaceBarKind
	userHome                string
	state                   *idehistory.Store
	scavenger               *idescavenger.Cleaner
	workspacesBarHeight     int
	workspacesIcon          rune
	externalCommands        map[string]externalCommand
	externalREPLCommands    map[string]externalREPLCommand
	externalEvents          []externalEvents
	initialVTECapacity      int
	dispatchOnPreview       map[string]previewFunc
	debugCommands           bool
	frame                   bool
	streamingOpen           bool
	reloadConfig            func() (ideConfig, error)

	// startupWorkspace records whether a workspace was requested at
	// launch (non-empty cwd). It lets the IDE decide on Ready whether
	// the user is intentionally landing on the home workspace, before
	// the async cwd workspace install has completed.
	startupWorkspace bool

	// lastSession is the set of workspaces that were open when the
	// previous session ended. It is loaded once, before the current
	// session starts overwriting the persisted document, and consumed
	// by the one-shot maybeReopenLastSession.
	lastSession   idehistory.Session
	reopenPending bool

	// sessionReopenDisabled suppresses the reopen prompt entirely. It
	// is set for instances spawned as secondary OS windows, which share
	// the storage of the instance that spawned them and must not reopen
	// its workspaces.
	sessionReopenDisabled bool

	packageConfigMergeHook func(idepkg.ConfigMergeEvent) (idepkg.ConfigMergeResult, error)

	watchedFilesChangeHook func(int)

	// tutorialsInstalled is a required dependency wired by the IDE at
	// construction; afterPackageConfigMerge calls it unconditionally and a nil
	// value is a construction bug that must panic, not be guarded.
	tutorialsInstalled func(names []string) (bool, error)

	// onboardingActive is wired by the IDE at construction and reports
	// whether the bootstrap-started first-run tutorial flow is currently
	// active; extension authorizers use it to grant verified-publisher
	// commands without prompting while the tutorial overlay would bury
	// the prompt. Nil (in tests) means never active.
	onboardingActive func() bool

	commandObserver *commandObserverRegistry

	// commandHistory exposes the command prompt's persisted history so
	// the built-in workspaceopen completer can surface previously opened
	// workspaces alongside directory completion.
	commandHistory command.HistoryAccessor

	// workspaceOpenCompleters are scheme-provided completers appended to
	// the built-in workspaceopen completion. See WithWorkspaceOpenCompleter.
	workspaceOpenCompleters []command.Completer

	union               handler.FrameUnion
	bar                 handler.Tabs
	shadedBar           *handler.ShadedTabs
	barIdxToSlot        []int
	focusProxy          handler.Proxy
	width, height       int
	workspaces          [workspaceSlots]*workspaceHandler
	workspaceCount      int
	focus               int
	defAttr             term.Attributes
	homeURI             workspaceapi.URI
	homeWorkspace       workspace.Workspace
	empty               *ex
	homeRunner          extension.Runner
	homeLSPManager      *idelsp.Manager
	homeDAPManager      *idedebug.Manager
	openPrevFiles       []idehistory.File
	openPrevFilesEx     *ex
	openPrevWindows     map[uint64]browser.Window
	shaderRunner        *shaderRunner
	pending             map[string]*pendingWorkspace
	lastReservedPending *pendingWorkspace
	pendingWG           sync.WaitGroup
	closing             map[string]chan struct{}
	closeWG             sync.WaitGroup

	// Fields, not constants, so tests can shorten the extensionready
	// readiness and command-registration timeouts.
	extReadyWait   time.Duration
	extCommandWait time.Duration
	extHandleWait  time.Duration
}

type openFileTarget struct {
	workspaceIdx int
	workspace    *workspaceHandler
	tab          *browser.Tab
}

const (
	pendingBuildActive uint32 = iota
	pendingBuildReturned
	pendingBuildCancelClosing
)

type pendingWorkspace struct {
	uri               workspaceapi.URI
	slot              int
	cancelCtx         func()
	canceled          atomic.Bool
	buildPhase        atomic.Uint32
	onReady           [][]string
	cwd               workspace.Workspace
	teardownBegin     sync.Once
	teardownStart     sync.Once
	teardownFinish    sync.Once
	detachedWorkspace workspace.Workspace
	buildReleased     chan struct{}
	closeGate         chan struct{}
	ownsCloseGate     bool
}

type visibleWorkspaceManager struct {
	parent  *workspaceManagerHandler
	manager workspace.WorkspaceManager
}

func (m visibleWorkspaceManager) AddWorkspace(
	ctx context.Context, uri workspaceapi.URI,
) (workspace.Workspace, error) {
	return m.manager.AddWorkspace(ctx, uri)
}

func (m visibleWorkspaceManager) Workspace(
	file workspaceapi.URI,
) (workspace.Workspace, bool, error) {
	target, ok := m.parent.workspaceForFile(file)
	if !ok || target == nil || target.workspace == nil || target.workspace.ex == nil {
		return nil, false, nil
	}
	return target.workspace.ex.workspace, true, nil
}

func (m visibleWorkspaceManager) RegisterScheme(
	scheme string, fn schemeapi.SchemeFunc,
) error {
	return m.manager.RegisterScheme(scheme, fn)
}

func (m visibleWorkspaceManager) UnregisterScheme(scheme string) error {
	return m.manager.UnregisterScheme(scheme)
}

func (m visibleWorkspaceManager) IncrementReference(uri workspaceapi.URI) {
	m.manager.IncrementReference(uri)
}

func (m visibleWorkspaceManager) DecrementReference(uri workspaceapi.URI) error {
	return m.manager.DecrementReference(uri)
}

func (m visibleWorkspaceManager) RemoveWorkspace(
	uri workspaceapi.URI,
) (workspace.Workspace, bool) {
	return m.manager.RemoveWorkspace(uri)
}

func (h *workspaceManagerHandler) newEditor(
	reloader exoeditor.Reloader,
	cwd workspaceapi.URI, ws workspace.Workspace, tm browser.TabManager,
	terminal schemeapi.Terminal, cfg ideConfig, svc vctrl.Service,
) (text.Editor, error) {
	switch cfg.editorMode() {
	case editorModeModal:
		return h.newBuiltinModalEditor(cwd, cfg, svc), nil
	case editorModeHelix:
		return h.newBuiltinHelixEditor(cwd, cfg, svc), nil
	case editorModeStandard:
		return h.newBuiltinStandardEditor(cwd, cfg, svc), nil
	case editorModeEmacs:
		return h.newBuiltinEmacsEditor(cwd, cfg, svc), nil
	case editorModeExo:
		return h.newExoFallbackEditor(
			reloader, cwd, ws, tm, terminal, cfg, svc), nil
	default:
		panic("invalid editor mode")
	}
}

func (h *workspaceManagerHandler) newBuiltinModalEditor(
	cwd workspaceapi.URI, cfg ideConfig, svc vctrl.Service,
) text.Editor {
	auxBarConfig := cfg.auxiliaryBarConfig(h, svc)
	iconsBarConfig := cfg.iconsBarConfig(h)
	statusBarConfig := cfg.statusBarConfig(cwd, h, svc)
	viOpts := append([]vi.Option{},
		vi.WithResAttr(cfg.modalResultAttr()),
		vi.WithBarAttr(cfg.modalMessageBarAttr()),
		vi.WithMessageBarLayout(cfg.modalMessageBarLayout()),
		vi.WithTabspaces(cfg.editorTabspaces()),
		vi.WithIndents(cfg.editorIndents()),
		vi.WithRuler(cfg.editorRuler()),
		vi.WithAutoPair(cfg.editorAutoPair()),
		vi.WithComments(cfg.editorComments()),
		vi.WithScheduleNextTick(cfg.scheduleNextTick),
		vi.WithAttr(cfg.modalAttr()),
		vi.WithAuxiliaryBar(cfg.auxiliaryBarEnabled(), auxBarConfig),
		vi.WithIconsBar(cfg.iconsBarEnabled(), iconsBarConfig),
		vi.WithGitIcons(cfg.gitIconsEnabled()),
		vi.WithStatusBarConfig(cfg.statusBarEnabled(), statusBarConfig),
		vi.WithHideInitialFolds(cfg.initialFolds()),
		vi.WithClipboard(h.clip),
		vi.WithMacroRecorder(h.macro),
		vi.WithMacroPlayer(h.macroPlayer),
		vi.WithWorkspaceCommandRegistry(cwd, h),
		vi.WithAutoCenter(true),
		vi.WithWindowManager(currentWorkspaceWindowManager{root: h}),
		vi.WithNotifications(h.notifications.current()),
	)
	return vi.Editor(viOpts...)
}

func (h *workspaceManagerHandler) newBuiltinHelixEditor(
	cwd workspaceapi.URI, cfg ideConfig, svc vctrl.Service,
) text.Editor {
	auxBarConfig := cfg.auxiliaryBarConfig(h, svc)
	iconsBarConfig := cfg.iconsBarConfig(h)
	statusBarConfig := cfg.statusBarConfig(cwd, h, svc)
	return helix.Editor(
		helix.WithResAttr(cfg.helixResultAttr()),
		helix.WithBarAttr(cfg.helixMessageBarAttr()),
		helix.WithMessageBarLayout(cfg.helixMessageBarLayout()),
		helix.WithTabspaces(cfg.editorTabspaces()),
		helix.WithIndents(cfg.editorIndents()),
		helix.WithRuler(cfg.editorRuler()),
		helix.WithAutoPair(cfg.editorAutoPair()),
		helix.WithComments(cfg.editorComments()),
		helix.WithScheduleNextTick(cfg.scheduleNextTick),
		helix.WithAttr(cfg.helixAttr()),
		helix.WithAuxiliaryBar(cfg.auxiliaryBarEnabled(), auxBarConfig),
		helix.WithIconsBar(cfg.iconsBarEnabled(), iconsBarConfig),
		helix.WithGitIcons(cfg.gitIconsEnabled()),
		helix.WithStatusBarConfig(cfg.statusBarEnabled(), statusBarConfig),
		helix.WithHideInitialFolds(cfg.initialFolds()),
		helix.WithClipboard(h.clip),
		helix.WithMacroRecorder(h.macro),
		helix.WithMacroPlayer(h.macroPlayer),
		helix.WithWorkspaceCommandRegistry(cwd, h),
		helix.WithAutoCenter(true),
		// See newBuiltinModalEditor for why we route notifications.
		helix.WithNotifications(h.notifications.current()),
	)
}

func (h *workspaceManagerHandler) newBuiltinStandardEditor(
	cwd workspaceapi.URI, cfg ideConfig, svc vctrl.Service,
) text.Editor {
	auxBarConfig := cfg.auxiliaryBarConfig(h, svc)
	iconsBarConfig := cfg.iconsBarConfig(h)
	statusBarConfig := cfg.statusBarConfig(cwd, h, svc)
	return standard.Editor(
		standard.WithCommandBar(true),
		standard.WithSearchConfig(cfg.standardSearchConfig(
			currentWorkspaceWindowManager{root: h})),
		standard.WithTabspaces(cfg.editorTabspaces()),
		standard.WithIndents(cfg.editorIndents()),
		standard.WithRuler(cfg.editorRuler()),
		standard.WithAutoPair(cfg.editorAutoPair()),
		standard.WithComments(cfg.editorComments()),
		standard.WithScheduleNextTick(cfg.scheduleNextTick),
		standard.WithAttr(cfg.standardAttr()),
		standard.WithAuxiliaryBar(cfg.auxiliaryBarEnabled(), auxBarConfig),
		standard.WithIconsBar(cfg.iconsBarEnabled(), iconsBarConfig),
		standard.WithGitIcons(cfg.gitIconsEnabled()),
		standard.WithHideInitialFolds(cfg.initialFolds()),
		standard.WithClipboard(h.clip),
		standard.WithMacroRecorder(h.macro),
		standard.WithMacroPlayer(h.macroPlayer),
		standard.WithStatusBarConfig(cfg.statusBarEnabled(), statusBarConfig),
		standard.WithWorkspaceCommandRegistry(cwd, h),
		standard.WithAutoCenter(true),
		// See newBuiltinModalEditor for why we route notifications.
		standard.WithNotifications(h.notifications.current()),
	)
}

func (h *workspaceManagerHandler) newBuiltinEmacsEditor(
	cwd workspaceapi.URI, cfg ideConfig, svc vctrl.Service,
) text.Editor {
	auxBarConfig := cfg.auxiliaryBarConfig(h, svc)
	iconsBarConfig := cfg.iconsBarConfig(h)
	statusBarConfig := cfg.statusBarConfig(cwd, h, svc)
	return emacs.Editor(
		emacs.WithCommandBar(true),
		emacs.WithResAttr(cfg.emacsResultAttr()),
		emacs.WithBarAttr(cfg.emacsMessageBarAttr()),
		emacs.WithMessageBarLayout(cfg.emacsMessageBarLayout()),
		emacs.WithTabspaces(cfg.editorTabspaces()),
		emacs.WithIndents(cfg.editorIndents()),
		emacs.WithRuler(cfg.editorRuler()),
		emacs.WithAutoPair(cfg.editorAutoPair()),
		emacs.WithComments(cfg.editorComments()),
		emacs.WithScheduleNextTick(cfg.scheduleNextTick),
		emacs.WithAttr(cfg.emacsAttr()),
		emacs.WithAuxiliaryBar(cfg.auxiliaryBarEnabled(), auxBarConfig),
		emacs.WithIconsBar(cfg.iconsBarEnabled(), iconsBarConfig),
		emacs.WithGitIcons(cfg.gitIconsEnabled()),
		emacs.WithHideInitialFolds(cfg.initialFolds()),
		emacs.WithClipboard(h.clip),
		emacs.WithMacroRecorder(h.macro),
		emacs.WithMacroPlayer(h.macroPlayer),
		emacs.WithStatusBarConfig(cfg.statusBarEnabled(), statusBarConfig),
		emacs.WithWorkspaceCommandRegistry(cwd, h),
		emacs.WithAutoCenter(true),
		// See newBuiltinModalEditor for why we route notifications.
		emacs.WithNotifications(h.notifications.current()),
	)
}

func (h *workspaceManagerHandler) newExoFallbackEditor(
	reloader exoeditor.Reloader,
	cwd workspaceapi.URI, ws workspace.Workspace,
	tm browser.TabManager, terminal schemeapi.Terminal,
	cfg ideConfig, svc vctrl.Service,
) text.Editor {
	var fallback text.Editor
	switch cfg.exoFallback() {
	case editorFallbackHelix:
		fallback = h.newBuiltinHelixEditor(cwd, cfg, svc)
	case editorFallbackStandard:
		fallback = h.newBuiltinStandardEditor(cwd, cfg, svc)
	case editorFallbackEmacs:
		fallback = h.newBuiltinEmacsEditor(cwd, cfg, svc)
	default:
		fallback = h.newBuiltinModalEditor(cwd, cfg, svc)
	}
	return exofallback.New(
		cfg.exoCommand(),
		cfg.exoGoto(),
		cfg.exoQuit(),
		cfg.scheduleNextTick,
		ws,
		cwd,
		h.notifications.current(),
		exoeditor.PublisherFunc(h.events.newPublisher(cwd)),
		terminal,
		ws, // executor
		tm,
		cfg.terminalConfig(),
		reloader,
		fallback,
		h.envSource,
		cfg.exoExperimentalHighlights(),
		h,
		svc,
		h.clip,
	)
}

func (h *workspaceManagerHandler) newPromptEditor(
	cfg ideConfig,
) command.Editor {
	switch cfg.pkgEditorMode() {
	case editorModeStandard:
		return standardPromptEditor{
			tabspaces:        cfg.editorTabspaces(),
			indents:          cfg.editorIndents(),
			scheduleNextTick: cfg.scheduleNextTick,
			clipboard:        h.clip,
			autoPair:         cfg.editorAutoPair(),
		}
	case editorModeEmacs:
		return emacsPromptEditor{
			tabspaces:        cfg.editorTabspaces(),
			indents:          cfg.editorIndents(),
			scheduleNextTick: cfg.scheduleNextTick,
			clipboard:        h.clip,
			autoPair:         cfg.editorAutoPair(),
		}
	case editorModeModal:
		return viPromptEditor{
			tabspaces:        cfg.editorTabspaces(),
			indents:          cfg.editorIndents(),
			scheduleNextTick: cfg.scheduleNextTick,
			clipboard:        h.clip,
			autoPair:         cfg.editorAutoPair(),
		}
	case editorModeHelix:
		return helixPromptEditor{
			tabspaces:        cfg.editorTabspaces(),
			indents:          cfg.editorIndents(),
			scheduleNextTick: cfg.scheduleNextTick,
			clipboard:        h.clip,
			autoPair:         cfg.editorAutoPair(),
		}
	default:
		panic("invalid editor mode")
	}
}

type standardPromptEditor struct {
	tabspaces        int
	indents          text.IndentConfig
	scheduleNextTick func(func()) bool
	clipboard        clipboard.Register
	autoPair         bool
}

func (m standardPromptEditor) Edit(buf *cell.Buffer) command.EditHandler {
	uri := workspaceapi.RandomURI("memory")
	return standard.NewHandler(buf, uri, text.IndentRuneTab, m.tabspaces,
		standard.WithCommandBar(false),
		standard.WithTabspaces(m.tabspaces),
		standard.WithIndents(m.indents),
		standard.WithScheduleNextTick(m.scheduleNextTick),
		standard.WithClipboard(m.clipboard),
		standard.WithAutoPair(m.autoPair),
		standard.WithWrap(false),
	)
}

type emacsPromptEditor struct {
	tabspaces        int
	indents          text.IndentConfig
	scheduleNextTick func(func()) bool
	clipboard        clipboard.Register
	autoPair         bool
}

func (m emacsPromptEditor) Edit(buf *cell.Buffer) command.EditHandler {
	uri := workspaceapi.RandomURI("memory")
	return emacs.NewHandler(buf, uri, text.IndentRuneTab, m.tabspaces,
		emacs.WithCommandBar(false),
		emacs.WithTabspaces(m.tabspaces),
		emacs.WithIndents(m.indents),
		emacs.WithScheduleNextTick(m.scheduleNextTick),
		emacs.WithClipboard(m.clipboard),
		emacs.WithAutoPair(m.autoPair),
		emacs.WithWrap(false),
	)
}

type viPromptEditor struct {
	tabspaces        int
	indents          text.IndentConfig
	scheduleNextTick func(func()) bool
	clipboard        clipboard.Register
	autoPair         bool
}

func (v viPromptEditor) Edit(buf *cell.Buffer) command.EditHandler {
	uri := workspaceapi.RandomURI("memory")
	return vi.NewWithIndent(buf, uri, text.IndentRuneTab, v.tabspaces,
		vi.WithTabspaces(v.tabspaces),
		vi.WithIndents(v.indents),
		vi.WithScheduleNextTick(v.scheduleNextTick),
		vi.WithClipboard(v.clipboard),
		vi.WithAutoPair(v.autoPair),
		vi.WithWrap(false),
	)
}

type helixPromptEditor struct {
	tabspaces        int
	indents          text.IndentConfig
	scheduleNextTick func(func()) bool
	clipboard        clipboard.Register
	autoPair         bool
}

func (p helixPromptEditor) Edit(buf *cell.Buffer) command.EditHandler {
	uri := workspaceapi.RandomURI("memory")
	return helix.NewWithIndent(buf, uri, text.IndentRuneTab, p.tabspaces,
		helix.WithTabspaces(p.tabspaces),
		helix.WithIndents(p.indents),
		helix.WithScheduleNextTick(p.scheduleNextTick),
		helix.WithClipboard(p.clipboard),
		helix.WithAutoPair(p.autoPair),
		helix.WithWrap(false),
	)
}

func (h *workspaceManagerHandler) init(
	cwd *workspaceapi.URI, homeDirUri workspaceapi.URI,
	manager workspace.WorkspaceManager,
	notiConfig notifications.Config,
	cfg ideConfig, storage storageapi.Service, sixDir string,
	publishEvent func(term.Event) bool,
	extensionRunner ExtensionsRunner, trust *pkgtrust.Store, locker sync.Locker,
	builtinExtensions map[string]Extension,
	reloadConfig func() (ideConfig, error), workspaceConfigFilename string,
	tabBarOffset, rightInset, tabBarHeight int, workspacesIcon rune,
	workspacesBarHeight, workspacesBarOffset int, workspacesBarFrame bool,
	tabsClickCallback func(int) bool,
	releaseManager release.Manager,
	shaderRunner *shaderRunner,
	initialVTECapacity int,
	dispatchOnPreview map[string]previewFunc,
	debugCommands bool,
	streamingOpen bool,
	commandObserver *commandObserverRegistry,
) (err error) {
	ctx := context.Background()

	if h.tutorialsInstalled == nil {
		panic("workspaceManagerHandler.init: tutorialsInstalled is required")
	}
	h.storage = storage
	h.ideStorage = storageapi.WithPartition(h.storage, "ide")
	h.commandHistory = search.NewHistory(
		h.ideStorage, commandHistoryDocumentID, cfg.commandMaxHistory())
	cfg.storage = h.ideStorage
	// localExecutor is the stable, IDE-scoped executor proxy passed to the
	// llama-server backend at router construction. Created non-nil here; its
	// inner executor is installed at the setExecutor sites below.
	h.localExecutor = &currentExecutor{}
	h.events = newEventRouter(publishEvent)
	h.frameCharSet = cfg.windowFrameCharset()
	notiConfig.Interrupter = h.events.globalInterrupter()
	notiConfig.RightInset = rightInset
	h.notifications = newWorkspaceNotifications(h.ideStorage, notiConfig, h)
	h.shaderRunner = shaderRunner
	h.mu = locker
	h.externalCommands = make(map[string]externalCommand)
	h.externalREPLCommands = make(map[string]externalREPLCommand)
	h.frame = cfg.frame()
	h.scheduleNextTick = cfg.scheduleNextTick
	h.configPath = cfg.configPath
	h.reloadConfig = reloadConfig
	h.workspaceConfigFilename = workspaceConfigFilename
	h.tabsClickCallback = tabsClickCallback
	h.workspace = manager
	h.clip = cfg.clipboard()
	h.macro = idemacro.New(h.clip, h.notifications.current(), cfg.commandKey())
	h.macroPlayer = idemacro.NewPlayer(h.clip, h.macro, h.events.globalPublisher())
	h.sixDir = sixDir
	// One extensions storage per process: every workspace's extension
	// runner serves this same service. See extension.StorageResources.
	h.extensionsStorage = localstorage.New(
		ctx, filepath.Join(sixDir, "extensions"), docbson.Marshaler())
	h.extensionRunner = extensionRunner
	h.trust = trust
	h.builtinExtensions = builtinExtensions
	h.initialVTECapacity = initialVTECapacity
	h.dispatchOnPreview = dispatchOnPreview
	if h.dispatchOnPreview == nil {
		h.dispatchOnPreview = make(map[string]previewFunc)
	}

	h.workspacesIcon = workspacesIcon
	h.tabBarOffset = tabBarOffset
	h.rightInset = rightInset
	h.tabBarHeight = tabBarHeight
	h.debugCommands = debugCommands
	h.streamingOpen = streamingOpen
	h.commandObserver = commandObserver
	h.pending = make(map[string]*pendingWorkspace)
	h.closing = make(map[string]chan struct{})
	h.extReadyWait = extensionReadyWait
	h.extCommandWait = extensionCommandWait
	h.extHandleWait = extensionHandleWait

	homeWorkspace, err := h.workspace.AddWorkspace(ctx, homeDirUri)
	if err != nil {
		return fmt.Errorf("add home workspace: %v", err)
	}

	h.homeURI = homeDirUri
	h.homeWorkspace = homeWorkspace
	h.setReleaseManager(releaseManager)
	// Construct the llama-server backend and router here, after
	// setReleaseManager has installed h.pkgmanager and h.notifications is
	// set. The backend takes three stable, non-nil IDE-scoped handles; the
	// "server not installed" state lives inside the locator, never in a nil
	// dependency.
	llmCfg := cfg.llmConfig()
	localBackend := llamaserver.New(
		llmCfg.Local.Service,
		h.localExecutor,
		llamaserver.NewPkgLocator(h.pkgmanager),
		h.notifications.current(),
	)
	router, err := llmrouter.New(llmCfg, sixDir, h.storage, localBackend)
	if err != nil {
		return fmt.Errorf("init llm router: %w", err)
	}
	h.llmRouter = router
	h.events.setFocus(h.homeURI)

	// don't install a fs watcher for the home workspace,
	// to prevent unecessary resource consumption
	homeParser := syntax.NewParser(h.homeWorkspace, h.pkgmanager, h.homeURI)
	globalOpts := h.textOpts(cfg, homeParser, h.homeURI, h.homeWorkspace)
	tm := new(workspaceTabManager)
	tm.parent = h
	h.empty, err = newEx(
		func(
			reloader exoeditor.Reloader, terminal schemeapi.Terminal,
		) (text.Editor, error) {
			return h.newEditor(reloader, homeDirUri, h.homeWorkspace,
				tm, terminal, cfg, vctrl.NopService())
		},
		homeWorkspace, h.ideStorage, h.notifications, h.homeURI,
		cfg.terminalConfig(), cfg.pluginBarConfig(),
		h.events.newPublisher(h.homeURI), 0 /* vte capacity */, h.clip, h.macro,
		h.dispatchOnPreview, tm, homeParser,
		vctrl.NopService(),
		h.newPromptEditor(cfg), h.commandObserver, h.debugCommands,
		cfg.commandPromptCfg(),
		modalEditorMode(cfg.pkgEditorMode()),
		cfg.editorMode(),
		cfg.editorAutoSave(),
		cfg.consoleCfg(),
		globalOpts...)
	if err != nil {
		return fmt.Errorf("new ex: %w", err)
	}
	tm.tm = h.empty.Browser()
	if err = h.subscribeAllCommands(h.empty); err != nil {
		return err
	}
	if err = h.subscribeAllEvents(cfg, h.empty); err != nil {
		return err
	}
	h.empty.markHome()

	wsExec := workspaceshell.NewExecutor(
		workspaceExecutorAdapter{e: h.homeWorkspace})
	trackedCwd := &trackedWorkspace{Workspace: h.homeWorkspace, exec: wsExec}
	extExec, err := newExtensionsExecutor()
	if err != nil {
		_, _ = h.notifications.current().Notify(browserapi.LevelError,
			"Error building extensions executor: %v", err)
		log.Errorf("build home workspace extensions executor: %v", err)
		return nil
	}
	runner, lspManager, dapManager, _, err := h.buildExtensions(
		cfg, homeDirUri, trackedCwd, h.empty, extExec, homeParser, false)
	if err != nil {
		_, _ = h.notifications.current().Notify(browserapi.LevelError,
			"Error building channel for extensions and plugins: %v", err)
		log.Errorf("build home workspace extensions: %v", err)
	} else {
		exec, isExecutor := runner.(schemeapi.Executor)
		if isExecutor {
			h.empty.setExecutor(exec, wsExec, extExec.shell)
			h.localExecutor.set(exec)
		}
		h.homeRunner = runner
		h.homeLSPManager = lspManager
		h.homeDAPManager = dapManager
		// Deliberately do not start extensions on the home/empty workspace:
		// rune-agent (and other user extensions) recursively walk the entire
		// workspace root for .gitignore files at startup, which is ruinously
		// expensive when the root is the user's home directory. Extensions
		// start when a real workspace is opened instead.
	}

	h.bar.Init()
	h.bar.OnClick = h.onBarTabClick
	frameAttr := cfg.windowFrameAttr()
	frameAttr.Attrs |= term.AttrVerticalRenderOffset
	backgroundAttr := term.Attributes{Bg: frameAttr.Bg}
	focusTabAttr := cfg.focusTabAttr()
	focusTabAttr.Attrs |= term.AttrVerticalRenderOffset
	nonFocusTabAttr := cfg.nonFocusTabAttr()
	nonFocusTabAttr.Attrs |= term.AttrVerticalRenderOffset
	focusTabIconAttr := cfg.focusTabIconAttr()
	focusTabIconAttr.Attrs |= term.AttrVerticalRenderOffset
	nonFocusTabIconAttr := cfg.nonFocusTabIconAttr()
	nonFocusTabIconAttr.Attrs |= term.AttrVerticalRenderOffset
	highlightAttr := cfg.highlightTabAttr()
	h.bar.SetAttr(focusTabAttr, nonFocusTabAttr,
		focusTabIconAttr, nonFocusTabIconAttr,
		highlightAttr, frameAttr, backgroundAttr)
	h.bar.SetFrameCharSet(cfg.windowFrameCharset())
	h.bar.SetBorder(cfg.frame())
	h.bar.SetBottomHighlight(true)
	h.bar.SetFocusFrameChar(cfg.workspaceHighlightTabChar())

	h.union.Init(&h.focusProxy)
	h.union.Attributes = cfg.windowFrameAttr()
	h.union.Frame = cfg.frame()

	charset := cfg.frameUnionCharset()
	h.union.Right = charset.Right
	h.union.Left = charset.Left
	h.union.Top = charset.Top
	h.union.Bottom = charset.Bottom
	h.workspaceBarKind = cfg.workspaceBarKind()
	h.state = idehistory.New(h.ideStorage)
	h.initScavenger(ctx)

	// Read the previous session before any workspace install can
	// overwrite the document with the current one.
	if !h.sessionReopenDisabled {
		lastSession, loadErr := h.state.LoadLastSession(ctx)
		if loadErr != nil {
			log.Warnf("load last session: %v", loadErr)
		}
		h.lastSession = lastSession
		h.reopenPending = len(h.lastSession.Workspaces) > 0
	}

	// best effort
	user, err := user.Current()
	if err == nil {
		h.userHome = user.HomeDir
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if cwd == nil {
		h.focusProxy.Target = h.focusHandler()
		h.initTabs(cfg, workspacesBarHeight,
			workspacesBarOffset, workspacesBarFrame)
		return nil
	}

	h.startupWorkspace = true
	err = h.addWorkspace(*cwd, true, !cfg.autoRestore(), -1)
	if err != nil {
		return fmt.Errorf("add default workspace: %w", err)
	}

	h.focusProxy.Target = h.focusHandler()
	h.initTabs(cfg, workspacesBarHeight,
		workspacesBarOffset, workspacesBarFrame)
	return nil
}

func (h *workspaceManagerHandler) focusHandler() tui.Handler {
	if handler := h.workspaces[h.focus]; handler != nil {
		return handler
	}

	return h.empty
}

// initScavenger sets up the reclamation of storage left behind by
// workspaces that have since been removed from disk, and kicks off one
// pass. Failures are not fatal: they only leave storage unreclaimed.
func (h *workspaceManagerHandler) initScavenger(ctx context.Context) {
	cleaner, err := idescavenger.New(idescavenger.Config{
		Storage:        h.ideStorage,
		OpenWorkspaces: h.openWorkspaceURIs,
		// workspaces opened before the scavenger existed are only known
		// to the session history
		Seed: h.state.ListWorkspaceURIs,
	})
	if err != nil {
		log.Errorf("new workspace scavenger: %v", err)
		return
	}
	cleaner.AddWorkspaceHook(symboldb.CleanupWorkspaceHook(h.ideStorage))
	cleaner.AddWorkspaceHook(h.state.ClearWorkspaceState)
	h.scavenger = cleaner
	cleaner.Start(ctx)
}

// openWorkspaceURIs answers from the event loop, which owns the
// installed and in-flight workspace tables.
func (h *workspaceManagerHandler) openWorkspaceURIs(
	ctx context.Context,
) ([]workspaceapi.URI, error) {
	result := make(chan []workspaceapi.URI, 1)
	if !h.scheduleNextTick(func() {
		uris := make([]workspaceapi.URI, 0,
			h.workspaceCount+len(h.pending)+1)
		for _, w := range h.workspaces {
			if w == nil {
				continue
			}
			uris = append(uris, w.uri)
		}
		for _, p := range h.pending {
			uris = append(uris, p.uri)
		}
		result <- append(uris, h.homeURI)
	}) {
		return nil, errors.New("event loop is not accepting work")
	}
	select {
	case uris := <-result:
		return uris, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *workspaceManagerHandler) focusURI() workspaceapi.URI {
	if handler := h.workspaces[h.focus]; handler != nil {
		return handler.uri
	}
	return h.homeURI
}

// notificationsForURIHash returns the pinned notifications of the
// installed workspace whose URI hashes to hash, or nil when none match.
// Callers run on the event loop (which already holds the host lock), so
// this reads h.workspaces/h.empty without re-acquiring h.mu, mirroring
// focusHandler/focusURI.
func (h *workspaceManagerHandler) notificationsForURIHash(
	hash string,
) browserapi.Notifications {
	for _, wh := range h.workspaces {
		if wh == nil || wh.ex == nil || wh.ex.notifications == nil {
			continue
		}
		if uriHash(wh.uri) == hash {
			return wh.ex.notifications
		}
	}
	if h.empty != nil && h.empty.notifications != nil &&
		uriHash(h.homeURI) == hash {
		return h.empty.notifications
	}
	return nil
}

func (h *workspaceManagerHandler) focusRunner() extension.Runner {
	if handler := h.workspaces[h.focus]; handler != nil {
		runner, _ := handler.Extensions.Load().(extension.Runner)
		return runner
	}
	return h.homeRunner
}

func (h *workspaceManagerHandler) envSource(name string) (string, bool) {
	uri := h.focusURI()
	if uri == (workspaceapi.URI{}) {
		return "", false
	}
	switch name {
	case "WORKSPACE":
		return workspaceBasename(uri), true
	case "WORKSPACE_URI":
		return uri.String(), true
	case "WORKSPACE_PATH":
		return uri.Path(), true
	}
	return "", false
}

func (h *workspaceManagerHandler) setWorkspaceRequiresAttention(
	uri workspaceapi.URI, attr term.Attributes,
) {
	// Notifications arrive on background goroutines (e.g. VTE run
	// workers), so the layout rebuild in Resize must be marshaled
	// onto the event loop to avoid racing Draw/Resize.
	h.scheduleNextTick(func() {
		for _, w := range h.workspaces {
			if w == nil || w.uri.String() != uri.String() {
				continue
			}
			w.attentionAttr = attr
			h.Resize(h.width, h.height)
			break
		}
	})
}

func (h *workspaceManagerHandler) focusBrowser() browser.Browser {
	if handler := h.workspaces[h.focus]; handler != nil {
		return handler.Browser()
	}
	return h.empty.Browser()
}

func (h *workspaceManagerHandler) setDefaultAttr(attr term.Attributes) {
	h.defAttr = attr
	for _, w := range h.workspaces {
		if w == nil || w.ex == nil {
			continue
		}
		w.ex.setDefaultAttr(attr)
	}
	if h.empty != nil {
		h.empty.setDefaultAttr(attr)
	}
}

func (h *workspaceManagerHandler) workspaceForFile(file workspaceapi.URI) (*openFileTarget, bool) {
	for i, wh := range h.workspaces {
		if wh == nil {
			continue
		}
		is, err := workspace.IsWorkspaceURI(wh.workspace, file)
		if err != nil || !is {
			continue
		}
		return &openFileTarget{workspaceIdx: i, workspace: wh}, true
	}
	return nil, false
}

func (h *workspaceManagerHandler) openFileTarget(file workspaceapi.URI) (*openFileTarget, bool) {
	for i, wh := range h.workspaces {
		if wh == nil {
			continue
		}
		for _, tab := range wh.ex.comp.Tabs() {
			if tab.URI().Equal(file) {
				return &openFileTarget{workspaceIdx: i, workspace: wh, tab: tab}, true
			}
		}
	}
	return h.workspaceForFile(file)
}

func (h *workspaceManagerHandler) focusOpenFileTarget(
	target *openFileTarget, file workspaceapi.URI, readOnly bool,
) (*browser.Tab, error) {
	if target == nil || target.workspace == nil {
		return nil, errors.New("workspace target not found")
	}
	h.switchToWorkspace(target.workspaceIdx)
	if target.tab != nil {
		if win, ok := target.tab.Window(); ok {
			_, err := target.workspace.ex.comp.SetFocus(win)
			return target.tab, err
		}
		win := target.workspace.ex.invokeWindow()
		if win.Closed() {
			win, _ = target.workspace.ex.comp.Focus()
		}
		if err := win.SetContent(target.tab); err != nil && err != browserapi.ErrTabNotFree {
			return nil, err
		}
		return target.tab, nil
	}
	return target.workspace.ex.editFileURILocal(file, target.workspace.ex.invokeWindow(), readOnly)
}

func (h *workspaceManagerHandler) RouteOpen(
	file workspaceapi.URI, readOnly bool,
) (browserapi.Handler, bool, error) {
	target, ok := h.openFileTarget(file)
	if !ok || target == nil || target.workspace == nil {
		return nil, false, nil
	}
	if target.tab == nil {
		focus := h.focusHandler()
		if focus == target.workspace {
			return nil, false, nil
		}
	}
	tab, err := h.focusOpenFileTarget(target, file, readOnly)
	return tab, true, err
}

func (h *workspaceManagerHandler) focusEx() *ex {
	if handler := h.workspaces[h.focus]; handler != nil {
		return handler.ex
	}
	return h.empty
}

// focusLSPManager returns the LSP manager of the focused workspace, or
// the home slot's manager when no workspace is focused. It may return
// nil (e.g. the empty slot); callers must nil-check.
func (h *workspaceManagerHandler) focusLSPManager() *idelsp.Manager {
	if handler := h.workspaces[h.focus]; handler != nil {
		return handler.lspManager
	}
	return h.homeLSPManager
}

func (h *workspaceManagerHandler) drawBar() bool {
	return (h.workspaceCount > 1 || h.focusHandler() == h.empty) &&
		h.workspaceBarKind != workspaceBarKindDisabled
}

func (h *workspaceManagerHandler) barSize() int {
	if h.workspacesBarHeight != 0 {
		return h.workspacesBarHeight
	}
	ret := 1
	if h.frame {
		ret += 2
	}
	return ret
}

func (h *workspaceManagerHandler) makeWorkspaceTabName(
	i int, w *workspaceHandler,
) string {
	if w != nil && w.tabname != "" {
		return w.tabname
	}
	switch h.workspaceBarKind {
	case workspaceBarKindDisabled:
		return ""
	case workspaceBarKindNumbers:
		return strconv.Itoa(i + 1)
	case workspaceBarKindPaths:
	}
	if w == nil {
		return strconv.Itoa(i + 1)
	}
	return h.workspaceDisplayPath(w.uri)
}

// workspaceDisplayPath renders uri for humans, shortening the user's
// home directory to ~.
func (h *workspaceManagerHandler) workspaceDisplayPath(
	uri workspaceapi.URI,
) string {
	if uri.Scheme() == workspace.FileScheme && h.userHome != "" {
		return strings.ReplaceAll(uri.Path(), h.userHome, "~")
	}
	return uri.String()
}

func (h *workspaceManagerHandler) Resize(width, height int) {
	h.width, h.height = width, height
	h.bar.RemoveAll()
	h.barIdxToSlot = h.barIdxToSlot[:0]

	drawBar := h.drawBar()
	if drawBar {
		height = max(0, height-h.barSize())
	}
	var barFocusIdx int
	for i, w := range h.workspaces {
		if w != nil {
			name := h.makeWorkspaceTabName(i, w)
			idx := h.bar.Add(rune(int(h.workspacesIcon)+i), name)
			h.barIdxToSlot = append(h.barIdxToSlot, i)
			if w.attentionAttr != (term.Attributes{}) {
				h.bar.SetTabAttr(idx, w.attentionAttr)
				h.bar.SetTabName(idx, name+h.tabAttentionNameSuffix)
			}
			if i == h.focus {
				barFocusIdx = idx
				if !drawBar {
					w.Resize(width, height)
				}
			}
		} else if i == h.focus {
			idx := h.bar.Add(0, h.makeWorkspaceTabName(i, w))
			h.barIdxToSlot = append(h.barIdxToSlot, i)
			barFocusIdx = idx
		}
	}
	h.bar.SetFocus(barFocusIdx)
	h.bar.SetHighlight(barFocusIdx)
	h.empty.Resize(width, height)
	// bar needs to be drawn last so frame union characters
	// are drawn last
	if drawBar {
		h.union.Resize(h.width, h.height)
	}
	h.refreshWorkspaceActivity()
	if h.openPrevFiles != nil && h.width != 0 && h.height != 0 {
		err := h.openPrevSessionFiles(h.openPrevFilesEx, h.openPrevFiles, h.openPrevWindows)
		if err != nil {
			// do not notify during a call to Resize
			log.Errorf("restore prev session: %v", err)
		}
		h.openPrevFiles = nil
		h.openPrevFilesEx = nil
		h.openPrevWindows = nil
	}

}

// setRightInset resizes the column reserved along the right edge across
// every live workspace, and keeps it as the default for workspaces
// opened later.
func (h *workspaceManagerHandler) setRightInset(cells int) {
	if cells < 0 {
		cells = 0
	}
	if cells == h.rightInset {
		return
	}
	h.rightInset = cells
	for _, w := range h.workspaces {
		if w == nil || w.ex == nil {
			continue
		}
		w.setRightInset(cells)
	}
	h.Resize(h.width, h.height)
}

func (h *workspaceManagerHandler) Draw(w term.Writer) {
	target := h.focusHandler()
	h.focusProxy.Target = target
	if h.drawBar() {
		h.union.Draw(w)
	} else {
		target.Draw(w)
	}
}

func (h *workspaceManagerHandler) switchToWorkspace(i int) bool {
	if i < 0 || i >= workspaceSlots {
		return false
	}
	// clear attention attributes and propagate focus status
	if i != h.focus {
		if w := h.workspaces[h.focus]; w != nil {
			w.ex.container.PauseAll()
			w.ex.onFocusChange(false)
		} else {
			h.empty.container.PauseAll()
		}
		if w := h.workspaces[i]; w != nil {
			w.attentionAttr = term.Attributes{}
			w.ex.onFocusChange(true)
			w.ex.container.ResumeAll()
		} else {
			h.empty.container.ResumeAll()
		}
	}
	h.focus = i
	h.events.setFocus(h.focusURI())
	h.focusProxy.Target = h.focusHandler()
	// resize for bottom workspace bar to disappear
	h.Resize(h.width, h.height)
	return true
}

func (h *workspaceManagerHandler) onBarTabClick(barIdx int) bool {
	if barIdx < 0 || barIdx >= len(h.barIdxToSlot) {
		return false
	}
	return h.switchToWorkspace(h.barIdxToSlot[barIdx])
}

func (h *workspaceManagerHandler) Handle(ev term.Event) (exit, handled bool) {
	h.macro.BeginEvent(ev)
	defer h.macro.EndEvent()
	if ev.Type == term.EventMouse && h.drawBar() && ev.MouseY >= h.height-h.barSize() {
		_, handled = h.union.Handle(ev)
		return
	}
	focus := h.focusHandler()
	exit, handled = focus.Handle(ev)
	if !exit {
		return h.confirmedForceExit, handled || h.confirmedForceExit
	}

	exHandler := h.exHandler(focus)
	if exHandler.forceExit || h.confirmedForceExit || (exHandler.exit && h.exitPromptOpen) {
		return true, true
	}

	if !h.exitPromptOpen {
		hasDirtyFilesOpen := h.state.DirtyFilesOpen()
		h.exitPromptOpen = true
		h.openConfirmExitPrompt(exHandler, hasDirtyFilesOpen)
		h.shaderRunner.runShutdownShader()
	}

	exHandler.forceExit = false
	exHandler.exit = false

	return false, true

}

func (h *workspaceManagerHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return h.focusHandler().Cursor()
}

// isExitCommand reports whether name is a command that exits the IDE.
func isExitCommand(name string) bool {
	switch name {
	case "quit", "forcequit!", "writequit", "writeforcequit!":
		return true
	}
	return false
}

// exitRequested reports whether ev is bound to a command that exits
// the IDE under the focused workspace's key bindings. Two-key
// sequences (e.g. the emacs <c-x><c-c>) are resolved by the sequencer
// inside ex and are not visible here.
func (h *workspaceManagerHandler) exitRequested(ev term.Event) bool {
	if ev.Type != term.EventKey {
		return false
	}
	cmdsAndArgs, ok := h.focusEx().comp.CommandKeyBinding(ev.KeyComb())
	if !ok {
		return false
	}
	for _, cmd := range cmdsAndArgs {
		if len(cmd) > 0 && isExitCommand(cmd[0]) {
			return true
		}
	}
	return false
}

func (h *workspaceManagerHandler) Selection() (string, bool) {
	return h.focusHandler().Selection()
}

func (h *workspaceManagerHandler) initExtensions(manager extension.Runner, cfg ideConfig) {
	var wg sync.WaitGroup
	userExtensions := cfg.extensions()

	wg.Add(len(userExtensions) + len(h.builtinExtensions))

	for id, p := range h.builtinExtensions {
		pconfig := p.Config
		if pconfig == nil {
			pconfig = config.MapConfig(make(map[string]any))
		}
		cmdAndArgs := p.CmdAndArgs
		id := id
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			err := manager.Run(id, cmdAndArgs, pconfig)
			if err != nil {
				log.Errorf("failed to run built-in extension with id %q: %v", id, err)
			}
		})
	}

	for id, p := range userExtensions {
		path, pconfig := extensionRunArgs(p)
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			if err := startUserExtension(manager, id, path, pconfig); err != nil {
				log.Errorf("failed to run extension with id %q: %v", id, err)
			}
		})
	}

	wg.Wait()
}

func extensionRunArgs(p extensionConfig) (string, config.Config) {
	path, _ := p.path()
	pconfig, ok := p.config()
	if !ok {
		pconfig = config.MapConfig(make(map[string]any))
	}
	return path, pconfig
}

func startUserExtension(
	manager extension.Runner, id, path string, pconfig config.Config,
) error {
	if err := manager.Run(id, path, pconfig); err != nil &&
		!errors.Is(err, extensionv2.ErrExtensionAlreadyRunning) {
		return err
	}
	return nil
}

func (h *workspaceManagerHandler) startInstalledExtensions(ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	cfg, err := h.reloadConfig()
	if err != nil {
		log.Errorf("failed to reload config to start installed extensions: %v", err)
		return false
	}
	userExtensions := cfg.extensions()

	type startArgs struct {
		id      string
		path    string
		pconfig config.Config
	}
	var toStart []startArgs
	for _, id := range ids {
		if p, ok := userExtensions[id]; ok {
			path, pconfig := extensionRunArgs(p)
			toStart = append(toStart, startArgs{id: id, path: path, pconfig: pconfig})
		}
	}
	if len(toStart) == 0 {
		return false
	}

	h.mu.Lock()
	// The home/empty workspace intentionally runs no extensions (see the
	// home-runner setup in init), so a package install must not start them
	// there either.
	runners := make([]extension.Runner, 0, h.workspaceCount)
	for _, hm := range h.workspaces {
		if hm == nil {
			continue
		}
		if runner, ok := hm.Extensions.Load().(extension.Runner); ok && runner != nil {
			runners = append(runners, runner)
		}
	}
	h.mu.Unlock()

	var wg sync.WaitGroup
	for _, runner := range runners {
		for _, a := range toStart {
			runner, a := runner, a
			wg.Add(1)
			go debug.CapturePanicReport(func() {
				defer wg.Done()
				if err := startUserExtension(runner, a.id, a.path, a.pconfig); err != nil {
					log.Errorf("failed to start installed extension with id %q: %v",
						a.id, err)
				}
			})
		}
	}
	wg.Wait()
	return true
}

// afterPackageConfigMerge is the post-merge hook wired into the package
// manager. It starts any extension added under the `extensions:` config key so
// the package's tools work without a restart, and composes the externally
// supplied gui.env hook so both live-apply paths run. The gui.env hook runs
// first because it applies the package's environment via os.Setenv, and the
// extension processes spawned by startInstalledExtensions inherit os.Environ()
// at fork time; starting them first would deny them those variables. Extension
// start failures are logged (as in initExtensions), so the returned error is
// the stored hook's, and LiveApplied is OR'd across both paths. Any tutorial
// added under the `tutorials:` config key is live-registered (and the user
// prompted) through tutorialsInstalled so a freshly-installed tutorial is
// runnable without a restart; its error is joined onto the returned error so
// idepkg can notify in one place.
func (h *workspaceManagerHandler) afterPackageConfigMerge(
	event idepkg.ConfigMergeEvent,
) (idepkg.ConfigMergeResult, error) {
	var result idepkg.ConfigMergeResult
	var err error
	if h.packageConfigMergeHook != nil {
		result, err = h.packageConfigMergeHook(event)
	}

	startedExtension := h.startInstalledExtensions(event.AddedExtensionIDs())
	result.LiveApplied = result.LiveApplied || startedExtension

	if names := event.AddedTutorialNames(); len(names) > 0 {
		registered, tutErr := h.tutorialsInstalled(names)
		result.LiveApplied = result.LiveApplied || registered
		err = errors.Join(err, tutErr)
	}
	return result, err
}

func (h *workspaceManagerHandler) textOpts(
	cfg ideConfig, parser syntaxapi.Parser, uri workspaceapi.URI,
	ws workspace.Workspace,
) []text.Option {
	markdownConfig := markdown.DefaultConfig()
	markdownConfig.Parser = parser
	markdownConfig.ScheduleNextTick = cfg.scheduleNextTick
	ret := []text.Option{
		text.WithTabspaces(cfg.editorTabspaces()),
		text.WithComments(cfg.editorComments()),
		text.WithWindowManagerConfig(cfg.windowManagerConfig()),
		text.WithFrameUnionCharSet(cfg.frameUnionCharset()),
		text.WithFrameUnion(cfg.frameUnion()),
		text.WithTabsClickCallback(h.tabsClickCallback),
		text.WithCommandKey(cfg.commandKey()),
		text.WithCommandMaxHistory(cfg.commandMaxHistory()),
		text.WithShellMaxHistory(cfg.consoleMaxHistory()),
		text.WithCommandHistoryKey(cfg.commandHistoryKey()),
		text.WithFocusTabAttr(cfg.focusTabAttr(), cfg.focusTabIconAttr()),
		text.WithNonFocusTabAttr(cfg.nonFocusTabAttr(), cfg.nonFocusTabIconAttr()),
		text.WithFocusTabHighlightAttr(cfg.highlightTabAttr()),
		text.WithFocusTabHighlightChar(cfg.highlightTabChar()),
		text.WithWallpaper(cfg.wallpaper()),
		text.WithDropTarget(
			term.Attributes{Bg: term.ColorGray, Fg: term.ColorBlack},
			term.Attributes{Fg: term.ColorBlue},
			map[string]string{
				"":           "Drop files here",
				"rune-agent": "Drop files here to add to chat",
			}),
		text.WithDirtyTabAttr(cfg.dirtyTabAttr()),
		text.WithActiveTabShader(
			cfg.animationsActiveTabShader(animActiveContentTab),
			cfg.animationsActiveTabFPS(animActiveContentTab),
			cfg.animationsActiveTabLoop(animActiveContentTab)),
		text.WithOnTabActivity(h.refreshWorkspaceActivity),
		text.WithIconSet(cfg.icons()),
		text.WithTabOverrideIcon(cfg.tabOverrideIcon()),
		text.WithCommandOverlayConfig(cfg.commandOverlayConfig()),
		text.WithCommandAliases(cfg.commandAliases()),
		text.WithPromptConfig(cfg.promptConfig()),
		text.WithEventPublisher(h.events.newPublisher(uri)),
		text.WithTabBarOffset(h.tabBarOffset),
		text.WithRightInset(h.rightInset),
		text.WithTabBarHeight(h.tabBarHeight),
		text.WithTabNameSeparator(cfg.tabNameSeparator()),
		text.WithPackageManager(h.pkgmanager),
		text.WithSyntaxConfig(cfg.syntaxConfig()),
		text.WithMaxSyntaxParseSize(cfg.editorMaxSizeForSyntax()),
		text.WithSwapDirectory(h.swapDirectory(cfg, ws, uri)),
		text.WithMarkdownConfig(markdownConfig),
		text.WithClipboard(h.clip),
		text.WithOpenRouter(h),
		text.WithFileExplorer(text.FileExplorerConfig{
			IndentAttr: cfg.fileExplorerIndentAttr(),
			IconAttr:   cfg.fileExplorerIconAttr(),
			ReadOnly:   cfg.fileExplorerReadOnly(),
			EditKey:    cfg.fileExplorerEditKey(),
			MinWidth:   cfg.fileExplorerMinWidth(),
			Hint:       cfg.fileExplorerHint(),
			HintAttr:   cfg.fileExplorerHintAttr(),
		}),
		text.WithEnvSource(h.envSource),
		text.WithStreamingOpen(h.streamingOpen),
	}

	for seq, cmd := range cfg.commandKeyMappings() {
		if seq.Last != (term.KeyComb{}) {
			ret = append(ret, text.WithCommandSequenceBinding(seq, cmd))
		} else {
			ret = append(ret, text.WithCommandKeyBinding(seq.First, cmd))
		}
	}

	if cfg.editorMode() == editorModeModal {
		for seq, cmd := range vi.KeyBindings() {
			if seq.Last != (term.KeyComb{}) {
				ret = append(ret, text.WithCommandSequenceBinding(seq, cmd))
			} else {
				ret = append(ret, text.WithCommandKeyBinding(seq.First, cmd))
			}
		}
	}

	return ret
}

func cleanedExtensionConfig(cfg map[string]any) map[string]any {
	m := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if k != "extensions" {
			m[k] = v
		}
	}
	return m
}

func (h *workspaceManagerHandler) addWorkspace(
	uri workspaceapi.URI, shouldRestore, promptRecommended bool, slot int,
) error {
	if i, ok := h.findInstalledSlot(uri); ok {
		h.switchToWorkspace(i)
		return nil
	}
	if h.isPending(uri) {
		_, _ = h.notifications.current().Notify(browserapi.LevelWarn,
			"workspace %q is already loading", uri.String())
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done, ok := h.closing[uri.String()]
	if !ok {
		pending, err := h.reservePendingSlot(uri, slot, cancel)
		if err != nil {
			cancel()
			return err
		}
		cwd, err := h.createWorkspaceScheme(uri)
		if err != nil {
			delete(h.pending, uri.String())
			if h.lastReservedPending == pending {
				h.lastReservedPending = nil
			}
			cancel()
			return err
		}
		pending.cwd = cwd
		h.shaderRunner.startLoading()
		h.pendingWG.Add(1)
		h.launchBuild(pending, uri, cwd, ctx, cancel, shouldRestore, promptRecommended)
		return nil
	}
	pending, err := h.reservePendingSlot(uri, slot, cancel)
	if err != nil {
		cancel()
		return err
	}
	h.shaderRunner.startLoading()
	h.pendingWG.Add(1)
	go debug.CapturePanicReport(func() {
		<-done
		if pending.canceled.Load() {
			h.abortPendingBuild(pending, nil, cancel)
			return
		}
		scheduled := h.scheduleNextTick(func() {
			if pending.canceled.Load() || h.pending[uri.String()] != pending {
				h.shaderRunner.stopLoading()
				cancel()
				h.pendingWG.Done()
				return
			}
			cwd, err := h.createWorkspaceScheme(uri)
			if err != nil {
				delete(h.pending, uri.String())
				if h.lastReservedPending == pending {
					h.lastReservedPending = nil
				}
				h.shaderRunner.stopLoading()
				cancel()
				log.Errorf("load workspace %s: %v", uri.String(), err)
				_, _ = h.notifications.current().Notify(browserapi.LevelError,
					"Failed to load workspace %s: %v", uri.String(), err)
				h.pendingWG.Done()
				return
			}
			pending.cwd = cwd
			h.launchBuild(pending, uri, cwd, ctx, cancel,
				shouldRestore, promptRecommended)
		})
		if !scheduled {
			h.abortPendingBuild(pending, nil, cancel)
		}
	})
	return nil
}

// launchBuild runs the async workspace build for an already-reserved
// pending slot. Callers must have incremented pendingWG; every exit
// path below (install, abort) decrements it.
func (h *workspaceManagerHandler) launchBuild(
	pending *pendingWorkspace, uri workspaceapi.URI,
	cwd workspace.Workspace, ctx context.Context, cancel context.CancelFunc,
	shouldRestore, promptRecommended bool,
) {
	go debug.CapturePanicReport(func() {
		built, buildErr := h.buildWorkspaceAsync(uri, cwd, pending)
		pending.buildPhase.CompareAndSwap(pendingBuildActive, pendingBuildReturned)
		if pending.canceled.Load() {
			h.abortPendingBuild(pending, built, cancel)
			return
		}
		scheduled := h.scheduleNextTick(func() {
			defer h.pendingWG.Done()
			h.installPendingWorkspace(pending, uri, ctx, cancel,
				cwd, built, buildErr, shouldRestore, promptRecommended)
		})
		if !scheduled {
			h.abortPendingBuild(pending, built, cancel)
		}
	})
}

func (h *workspaceManagerHandler) abortPendingBuild(
	pending *pendingWorkspace, built *builtWorkspace, cancel context.CancelFunc,
) {
	h.mu.Lock()
	h.beginPendingWorkspaceTeardown(pending)
	h.shaderRunner.stopLoading()
	h.mu.Unlock()
	if built != nil {
		h.discardBuiltWorkspace(built)
	}
	h.finishPendingWorkspaceTeardown(pending)
	cancel()
	h.pendingWG.Done()
}

// beginPendingWorkspaceTeardown transfers manager and close-gate ownership
// while the event-loop lock prevents a same-URI successor from being created.
func (h *workspaceManagerHandler) beginPendingWorkspaceTeardown(pending *pendingWorkspace) {
	pending.teardownBegin.Do(func() {
		key := pending.uri.String()
		if h.pending[key] != pending {
			return
		}
		delete(h.pending, key)
		if h.lastReservedPending == pending {
			h.lastReservedPending = nil
		}
		if pending.cwd == nil {
			return
		}

		detached, ok := h.workspace.RemoveWorkspace(pending.uri)
		if !ok {
			return
		}
		pending.detachedWorkspace = detached
		pending.closeGate = h.closing[key]
		if pending.closeGate == nil {
			pending.closeGate = make(chan struct{})
			pending.ownsCloseGate = true
			h.closing[key] = pending.closeGate
		}
		pending.buildReleased = make(chan struct{})
		h.closeWG.Add(1)
	})
}

func (h *workspaceManagerHandler) abortPendingWorkspaceBuild(pending *pendingWorkspace) {
	// Only an active Phase B needs Close as an interrupt. Once Phase B
	// returns, normal cleanup keeps built-resource disposal ahead of Close.
	if pending.buildPhase.CompareAndSwap(
		pendingBuildActive, pendingBuildCancelClosing,
	) {
		h.startPendingWorkspaceTeardown(pending)
	}
}

func (h *workspaceManagerHandler) startPendingWorkspaceTeardown(pending *pendingWorkspace) {
	if pending.detachedWorkspace == nil {
		return
	}
	pending.teardownStart.Do(func() {
		go debug.CapturePanicReport(func() {
			defer h.closeWG.Done()
			if err := pending.detachedWorkspace.Close(); err != nil {
				log.Errorf("close failed pending workspace %s: %v", pending.uri.String(), err)
			}
			// Same-URI recreation must wait for both the aborting close and
			// Phase B cleanup, regardless of which side finishes first.
			<-pending.buildReleased
			if !pending.ownsCloseGate {
				return
			}
			h.mu.Lock()
			if h.closing[pending.uri.String()] == pending.closeGate {
				delete(h.closing, pending.uri.String())
			}
			h.mu.Unlock()
			close(pending.closeGate)
		})
	})
}

func (h *workspaceManagerHandler) finishPendingWorkspaceTeardown(pending *pendingWorkspace) {
	pending.teardownFinish.Do(func() {
		if pending.detachedWorkspace == nil {
			return
		}
		h.startPendingWorkspaceTeardown(pending)
		close(pending.buildReleased)
	})
}

func (h *workspaceManagerHandler) findInstalledSlot(uri workspaceapi.URI) (int, bool) {
	for i, w := range h.workspaces {
		if w == nil {
			continue
		}
		if w.uri.Equal(uri) {
			return i, true
		}
	}
	return -1, false
}

func (h *workspaceManagerHandler) isPending(uri workspaceapi.URI) bool {
	_, ok := h.pending[uri.String()]
	return ok
}

func (h *workspaceManagerHandler) createWorkspaceScheme(uri workspaceapi.URI) (
	workspace.Workspace, error,
) {
	cwd, err := h.workspace.AddWorkspace(context.Background(), uri)
	if err != nil {
		return nil, fmt.Errorf("create new workspace for %q: %w", uri, err)
	}
	return cwd, nil
}

func (h *workspaceManagerHandler) reservePendingSlot(
	uri workspaceapi.URI, slot int, cancel context.CancelFunc,
) (*pendingWorkspace, error) {
	if slot == -1 {
		var ok bool
		slot, ok = h.nextAvailableWorkspace()
		if !ok {
			return nil, fmt.Errorf("no available workspaces")
		}
	}
	if slot < 0 || slot >= len(h.workspaces) {
		return nil, fmt.Errorf("invalid workspace slot %d", slot)
	}
	if h.workspaces[slot] != nil {
		return nil, fmt.Errorf("workspace slot %d is occupied", slot)
	}
	pending := &pendingWorkspace{
		uri:       uri,
		slot:      slot,
		cancelCtx: cancel,
	}
	h.pending[uri.String()] = pending
	h.lastReservedPending = pending
	return pending, nil
}

type builtWorkspace struct {
	cfg                 ideConfig
	configErr           error
	wh                  *workspaceHandler
	ex                  *ex
	runner              extension.Runner
	cursorHistoryCloser io.Closer
	symbolDBCloser      io.Closer
	notice              *idenotice.Crier
	lspManager          *idelsp.Manager
	dapManager          *idedebug.Manager
	promptStorage       storageapi.Service
}

func (h *workspaceManagerHandler) buildWorkspaceAsync(
	uri workspaceapi.URI, cwd workspace.Workspace,
	pending *pendingWorkspace,
) (*builtWorkspace, error) {
	cfg, configErr := h.reloadConfig()
	cfg.storage = h.ideStorage
	cfg.scheduleNextTick = h.scheduleNextTick
	if _, wConfigErr := loadWorkspaceConfig(
		h.workspaceConfigFilename, cwd, uri, &cfg,
	); wConfigErr != nil {
		configErr = multierror.Append(configErr,
			fmt.Errorf("workspace config: %w", wConfigErr))
	}

	// tracked from the build phase rather than the event loop: this is
	// storage I/O, and the workspace owns storage from here on
	if h.scavenger != nil {
		if err := h.scavenger.RegisterNewWorkspace(
			context.Background(), uri,
		); err != nil {
			log.Errorf("track workspace %q for scavenging: %v",
				uri.String(), err)
		}
	}

	parser := syntax.NewParser(cwd, h.pkgmanager, uri)
	var wsParser syntaxapi.Parser = parser
	var symbolDB *symboldb.Parser
	var symbolDBCloser io.Closer
	if cfg.workspaceSymbolDB() {
		sdb, sdbErr := symboldb.New(parser, cwd, uri, storageapi.WithPartition(
			storageapi.WithPartition(h.ideStorage, symboldb.PartitionName),
			uri.String()),
			h.notifications.current(), h.scheduleNextTick)
		if sdbErr != nil {
			h.empty.log(log.ErrorLevel, "symbol database for workspace %q: %v",
				uri.Path(), sdbErr)
		} else {
			symbolDB = sdb
			wsParser = sdb
			symbolDBCloser = sdb
		}
	}
	textOpts := h.textOpts(cfg, wsParser, uri, cwd)
	vctrlService, err := gogit.NewService(uri, cwd)
	if err != nil {
		h.empty.log(log.ErrorLevel, "new git service for workspace %q: %v",
			uri.Path(), err)
		vctrlService = vctrl.NopService()
	} else {
		vctrlService = vctrl.SyncService(vctrlService, new(sync.Mutex))
	}

	visibleManager := visibleWorkspaceManager{parent: h, manager: h.workspace}
	multicwd := workspace.Multi(context.Background(), visibleManager, cwd, uri)
	tm := new(workspaceTabManager)
	tm.parent = h
	ex, err := newEx(
		func(
			reloader exoeditor.Reloader, terminal schemeapi.Terminal,
		) (text.Editor, error) {
			return h.newEditor(reloader, uri, multicwd, tm, terminal,
				cfg, vctrlService)
		},
		multicwd, h.ideStorage, h.notifications, uri,
		cfg.terminalConfig(), cfg.pluginBarConfig(), h.events.newPublisher(uri),
		h.initialVTECapacity, h.clip, h.macro, h.dispatchOnPreview,
		tm, wsParser,
		vctrlService,
		h.newPromptEditor(cfg), h.commandObserver, h.debugCommands,
		cfg.commandPromptCfg(),
		modalEditorMode(cfg.pkgEditorMode()),
		cfg.editorMode(),
		cfg.editorAutoSave(),
		cfg.consoleCfg(),
		textOpts...)
	if err != nil {
		if symbolDBCloser != nil {
			_ = symbolDBCloser.Close()
		}
		return nil, fmt.Errorf("new ex: %w", err)
	}
	if symbolDB != nil {
		if serr := ex.comp.SubscribeEvents(
			symboldb.EditorEvents(), symbolDB,
		); serr != nil {
			log.Errorf("subscribe symbol database events for %s: %v",
				uri.String(), serr)
		}
	}
	apibrowser := newBrowserAdapter(ex.Browser())
	fexURI, _ := workspaceapi.ParseURI(fileExplorerURI)
	cursorHistoryCloser, err := idecursor.WithHistory(
		ex.Editor(), h.ideStorage, apibrowser, apibrowser, ex.workspace,
		wsParser, visibleManager, uri,
		h.scheduleNextTick, fexURI, gitshowBaseURI(),
	)
	if err != nil {
		if symbolDBCloser != nil {
			_ = symbolDBCloser.Close()
		}
		return nil, fmt.Errorf("install cursor history: %w", err)
	}
	tm.tm = ex.Browser()

	wh := &workspaceHandler{
		vctrlService:        vctrlService,
		cursorHistoryCloser: cursorHistoryCloser,
		symbolDBCloser:      symbolDBCloser,
		cancelCtx:           pending.cancelCtx,
		uri:                 uri,
		ex:                  ex,
		cwd:                 cwd,
	}
	tm.workspace = wh

	wsExec := workspaceshell.NewExecutor(
		workspaceExecutorAdapter{e: cwd})
	trackedCwd := &trackedWorkspace{Workspace: cwd, exec: wsExec}
	extExec, err := newExtensionsExecutor()
	if err != nil {
		_ = cursorHistoryCloser.Close()
		if symbolDBCloser != nil {
			_ = symbolDBCloser.Close()
		}
		_, _ = h.notifications.current().Notify(browserapi.LevelError,
			"Error building extensions executor: %v", err)
		log.Errorf("build extensions executor for workspace %s: %v",
			uri.String(), err)
		return nil, fmt.Errorf("new extensions executor: %w", err)
	}
	runner, lspManager, dapManager, promptStorage, err := h.buildExtensions(
		cfg, uri, trackedCwd, ex, extExec, wsParser, symbolDB != nil)
	if err != nil {
		_, _ = h.notifications.current().Notify(browserapi.LevelError,
			"Error building channel for extensions and plugins: %v", err)
		log.Errorf("build extensions for workspace %s: %v",
			uri.String(), err)
	} else {
		exec, isExecutor := runner.(schemeapi.Executor)
		if isExecutor {
			ex.setExecutor(exec, wsExec, extExec.shell)
			h.localExecutor.set(exec)
		}
		wh.Extensions.Store(runner)
		wh.lspManager = lspManager
		wh.dapManager = dapManager
		wh.promptStorage = promptStorage
	}

	built := &builtWorkspace{
		cfg:                 cfg,
		configErr:           configErr,
		wh:                  wh,
		ex:                  ex,
		runner:              runner,
		cursorHistoryCloser: cursorHistoryCloser,
		symbolDBCloser:      symbolDBCloser,
		lspManager:          lspManager,
		dapManager:          dapManager,
		promptStorage:       promptStorage,
	}
	if noticeCfg, ok := newNoticeConfig(cfg, h.ideStorage, uri); ok {
		built.notice = idenotice.New(
			cwd, apibrowser, wsParser, h.scheduleNextTick,
			noticeLinkCopier(h.clip, apibrowser), noticeCfg)
	}
	return built, nil
}

func (h *workspaceManagerHandler) installPendingWorkspace(
	pending *pendingWorkspace,
	uri workspaceapi.URI,
	ctx context.Context,
	cancel context.CancelFunc,
	cwd workspace.Workspace,
	built *builtWorkspace,
	buildErr error,
	shouldRestore, promptRecommended bool,
) {
	defer h.shaderRunner.stopLoading()
	// One-shot: only the first install of the session consumes the
	// previous session's snapshot.
	defer h.maybeReopenLastSession()

	if pending.canceled.Load() {
		h.beginPendingWorkspaceTeardown(pending)
		h.discardBuiltWorkspace(built)
		h.finishPendingWorkspaceTeardown(pending)
		cancel()
		return
	}

	if buildErr != nil {
		h.beginPendingWorkspaceTeardown(pending)
		log.Errorf("load workspace %s: %v", uri.String(), buildErr)
		_, _ = h.notifications.current().Notify(browserapi.LevelError,
			"Failed to load workspace %s: %v", uri.String(), buildErr)
		h.discardBuiltWorkspace(built)
		h.finishPendingWorkspaceTeardown(pending)
		cancel()
		return
	}

	if err := h.subscribeAllCommands(built.ex); err != nil {
		h.beginPendingWorkspaceTeardown(pending)
		h.discardBuiltWorkspace(built)
		h.finishPendingWorkspaceTeardown(pending)
		cancel()
		_, _ = h.notifications.current().Notify(browserapi.LevelError,
			"subscribe workspace commands: %v", err)
		return
	}
	if err := h.subscribeAllEvents(built.cfg, built.ex); err != nil {
		h.beginPendingWorkspaceTeardown(pending)
		h.discardBuiltWorkspace(built)
		h.finishPendingWorkspaceTeardown(pending)
		cancel()
		_, _ = h.notifications.current().Notify(browserapi.LevelError,
			"subscribe workspace events: %v", err)
		return
	}
	if h.pending[uri.String()] == pending {
		delete(h.pending, uri.String())
	}
	if h.lastReservedPending == pending {
		h.lastReservedPending = nil
	}

	ex := built.ex
	wh := built.wh
	ex.setDefaultAttr(h.defAttr)
	go debug.CapturePanicReport(func() {
		start := time.Now()
		// to preserve the order of events we don't want to spawn
		// multiple workers so make the buffer sufficiently large
		// so we don't block the fs subsystem, even in large
		// monorepos with large git operations
		ch := make(chan schemeapi.EventInfo, 8192)
		watchPath := filepath.Join(uri.Path(), "...")
		watchID, err := cwd.Watch(watchPath, ch,
			schemeapi.Create, schemeapi.Write,
			schemeapi.Remove, schemeapi.Rename)
		if err != nil {
			ex.log(log.WarnLevel, "oob file monitoring: create FS event watcher: %v", err)
			return
		}
		defer cwd.StopWatch(watchID) //nolint:errcheck

		ex.log(log.InfoLevel, "created FS event watcher in %s", time.Since(start))

		ignores, err := vctrl.LoadGitignore(cwd)
		if err != nil {
			ex.log(log.ErrorLevel, "load excludes for filesystem event matching: %v", err)
			ignores = vctrl.NopMatcher(false)
		}

		dispatchFilesystemEvents(ctx, ex, h.mu, ch, ignores)
	})

	if built.runner != nil {
		// load async to speed up workspace initialization
		go debug.CapturePanicReport(func() {
			h.initExtensions(built.runner, built.cfg)
		})
	}

	// Capture the current root before switching slots so the open shader can
	// burn away the previous screen. Capturing inside the shader would be too
	// late: by its first Draw, the newly opened workspace is already focused.
	h.shaderRunner.captureOpenShaderCells()

	h.workspaces[pending.slot] = wh
	h.workspaceCount++
	h.switchToWorkspace(pending.slot)
	h.persistLastSession()

	if built.notice != nil {
		notice := built.notice
		h.scheduleNextTick(func() {
			if err := notice.Show(ctx); err != nil {
				ex.log(log.WarnLevel, "show workspace notice: %v", err)
			}
		})
	}

	// Drain any commands enqueued via `workspaceready` while this
	// pending build was in flight. They run against the freshly
	// focused ex, in the same event-loop turn, so semantics match
	// "the new workspace just opened and then ran these commands".
	for _, cmd := range pending.onReady {
		if len(cmd) == 0 {
			continue
		}
		if err := ex.dispatchCommand(cmd[0], cmd[1:]...); err != nil {
			_, _ = h.notifications.current().Notify(browserapi.LevelError,
				"workspaceready %s: %v", cmd[0], err)
		}
	}
	pending.onReady = nil

	h.logNonFatalErrs(wh.Browser(), built.configErr, built.cfg.errors)

	state, err := h.state.LoadWorkspaceState(ctx, uri)
	if err != nil {
		_, _ = h.notifications.current().Notify(browserapi.LevelError,
			"load workspace state for %s: %v", uri.String(), err)
		return
	}
	fexplorerURI, _ := workspaceapi.ParseURI(fileExplorerURI)
	wh.historyCloser = h.state.SubscribeEvents(
		ctx, uri, &ex.comp, exSnapshotter{ex: ex, wh: wh},
		fexplorerURI, gitshowBaseURI())
	// The name identifies the workspace rather than its contents, so it
	// comes back whether or not the session itself is restored.
	if state.Name != "" {
		wh.tabname = state.Name
		h.Resize(h.width, h.height)
	}
	if !shouldRestore {
		if err := h.state.ClearWorkspaceState(ctx, uri); err != nil {
			_, _ = h.notifications.current().Notify(browserapi.LevelError,
				"clear workspace state for %s: %v", uri.String(), err)
		}
		return
	}
	if state.IsEmpty() {
		return
	}
	if promptRecommended && !built.cfg.autoRestore() {
		h.openRestorePrompt(ex, uri, state)
		return
	}
	if err := h.restorePreviousSession(ex, state); err != nil {
		_, _ = h.notifications.current().Notify(browserapi.LevelError,
			"restore previous session: %v", err)
	}
}

// discardBuiltWorkspace tears down a Phase B build whose install was
// canceled (via closeWorkspace before Phase C ran).
func (h *workspaceManagerHandler) discardBuiltWorkspace(built *builtWorkspace) {
	if built == nil {
		return
	}
	if built.cursorHistoryCloser != nil {
		_ = built.cursorHistoryCloser.Close()
	}
	if built.symbolDBCloser != nil {
		_ = built.symbolDBCloser.Close()
	}
	if built.runner != nil {
		if c, ok := built.runner.(io.Closer); ok {
			_ = c.Close()
		}
	}
	if built.lspManager != nil {
		_ = built.lspManager.Close()
	}
	if built.dapManager != nil {
		_ = built.dapManager.Close()
	}
	if built.promptStorage != nil {
		_ = built.promptStorage.Close()
	}
	if built.ex != nil {
		_ = built.ex.Close()
	}
}

func lspConfig(cfg ideConfig) config.Config {
	ret := make(map[string]any)
	lspAny, ok := cfg.cfg["lsp"]
	if !ok {
		return config.MapConfig(ret)
	}
	lsp, ok := lspAny.(map[string]any)
	if !ok {
		return config.MapConfig(ret)
	}
	lspCfg := make(map[string]any)
	maps.Copy(lspCfg, lsp)
	ret["lsp"] = lspCfg
	return config.MapConfig(ret)
}

func (h *workspaceManagerHandler) buildExtensions(
	cfg ideConfig, uri workspaceapi.URI,
	cwd workspace.Workspace, ex *ex, extExecutor *extensionsExecutor,
	parser syntaxapi.Parser, indexedSymbols bool,
) (
	_ extension.Runner, _ *idelsp.Manager, _ *idedebug.Manager,
	_ storageapi.Service, retErr error,
) {
	notifications := h.notifications.new(uri, ex.container)
	ed := ex.Editor()
	promptOpener := &ex.comp
	promptStorage := storageapi.WithPartition(h.storage, "extension-permissions")
	defer func() {
		if retErr != nil {
			_ = promptStorage.Close()
		}
	}()
	cmdAuthorizer, err := ideauthorizer.NewAuthorizer(
		ed, promptOpener, promptStorage, cfg.scheduleNextTick, notifications,
		h.trust, ideauthorizer.Config{
			AutoAuthorizeExtensions:        cfg.authorizerAutoAuthorizeExtensions(),
			AutoAuthorizeCommands:          cfg.authorizerAutoAuthorizeCommands(),
			AutoAuthorizeVerifiedPublisher: cfg.authorizerAutoAuthorizeVerified(),
			OnboardingActive:               h.onboardingActive,
		})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("new command authorizer: %w", err)
	}
	res := extension.BrowserResources(ex.Browser(), h.events.newPublisher(uri))
	res = extension.MergeResourceMap(res,
		extension.EditorResources(ex.Browser(), ed, h.events.newPublisher(uri)))
	res = extension.MergeResourceMap(res,
		extension.WorkspaceResources(cwd, cmdAuthorizer))
	res = extension.MergeResourceMap(res,
		extension.StorageResources(h.extensionsStorage))
	res = extension.MergeResourceMap(res,
		extension.ConfigResources(config.MapConfig(cleanedExtensionConfig(cfg.cfg))))
	res = extension.MergeResourceMap(res,
		extension.SyntaxResources(parser))
	apibrowser := newBrowserAdapter(ex.Browser())
	apieditor := newEditorAdapter(ed)

	lspCallbackCfg := idelsp.CallbackHandlerConfig{
		Config:           lspConfig(cfg),
		ScheduleNextTick: cfg.scheduleNextTick,
		Icons:            cfg.lspIcons(),
	}
	// Resolve the workspace root once, expanding ~ on the workspace host so
	// the LSP/DAP managers agree with the RootURI the language extensions
	// report (see workspaceRootURI).
	rootURI, err := workspaceRootURI(cwd, uri)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	callbacks := idelsp.NewCallbackHandler(notifications, apibrowser, ex.Browser(),
		apieditor, cwd, rootURI.String(), lspCallbackCfg)
	lspConfig := idelsp.Config{
		NoInitializeServer: true,
		Callback:           callbacks,
		MaxRetries:         5,
		WorkDoneProgress:   true,
		ScheduleNextTick:   cfg.scheduleNextTick,
		Parser:             parser,
		IndexedSymbols:     indexedSymbols,
	}
	lsp := idelsp.New(rootURI, cwd,
		cwd, h.pkgmanager, notifications,
		ex.Browser(), lspConfig)
	var lspifc semanticapi.LSP = lsp
	if h.watchedFilesChangeHook != nil {
		lspifc = watchedFilesTelemetryLSP{
			LSP:      lsp,
			onChange: h.watchedFilesChangeHook,
		}
	}
	dapCfg := idedebug.Config{
		MaxRetries: 5,
		Adapters:   cfg.debuggerConfigs(),
	}
	dap := idedebug.New(rootURI, cwd, h.pkgmanager, dapCfg)
	defer func() {
		if retErr == nil {
			return
		}
		if err := lsp.Close(); err != nil {
			log.Warnf("close lsp manager after build error: %v", err)
		}
		if err := dap.Close(); err != nil {
			log.Warnf("close dap manager after build error: %v", err)
		}
	}()
	err = ex.comp.SubscribeEvents(idelsp.EditorEvents(), lsp)
	if err != nil {
		log.Errorf("subscribe LSP manager: %v", err)
	}
	// Register the top-level "debugger" REPL command and its
	// command-prompt handler. debugshell.Handler owns the active
	// debug session lifecycle (initialize, launch, attach,
	// terminate) and forwards DAP events back to the REPL.
	dbgHandler := debugshell.New(dap, &ex.comp, apieditor, parser, ex.workspace, debugshell.Config{
		WorkspaceURI: uri,
		Icons: debugshell.Icons{
			Breakpoint: "",
			Stopped:    "",
		},
		Debugger:         dapCfg,
		ScheduleNextTick: cfg.scheduleNextTick,
	}).WithNotify(func(level browserapi.NotificationLevel, msg string, args ...any) {
		// DAP message-reader goroutines invoke this off the event
		// loop; hop through scheduleNextTick so notis.inFocus reads
		// workspaceManagerHandler.focus on the goroutine that mutates
		// it.
		cfg.scheduleNextTick(func() {
			_, _ = notifications.Notify(level, msg, args...)
		})
	})
	dbgMan := debugshell.Manual()
	if err := ex.editorObserver.RegisterREPLCommand(dbgMan, dbgHandler); err != nil {
		log.Errorf("register debugger repl command: %v", err)
	}
	if err := ex.editorObserver.SubscribeCommand(dbgMan,
		debugshell.NewPromptHandler(dbgHandler).
			WithOpenShell(ex.consolenewtab)); err != nil {
		log.Errorf("subscribe debugger command prompt: %v", err)
	}
	cmdcfg := lspCommandsConfig(uri, cfg, notifications,
		h.events.newInterrupter(uri), parser, callbacks)
	apiHandler, err := lspcmd.AllHandler(
		lspifc, apieditor, apibrowser, apibrowser, apibrowser,
		ex.workspace, parser, cmdcfg)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("new lsp command handler: %v", err)
	}
	handler := text.FuncCommandHandler(apiHandler.HandleCommand,
		func(ctx context.Context, cmd textapi.Command) (iterator.Iterator[string], string, error) {
			ret, err := apiHandler.Complete(ctx, cmd.Name, cmd.Args)
			return ret, "", err
		})
	err = ex.editorObserver.SubscribeCommand(lspcmd.Manual(), handler)
	if err != nil {
		log.Errorf("subscribe LSP manager: %v", err)
	}
	res = extension.MergeResourceMap(res, extension.SemanticResources(lspifc))
	res = extension.MergeResourceMap(res, extension.DebugResources(dap))
	res = extension.MergeResourceMap(res, extension.LLMResources(h.llmRouter))

	// Register the top-level `models` REPL command. The llmshell
	// reads the local llama.cpp registry directly off the router.
	llmHandler := llmshell.New(llmshell.Config{
		Service:          h.llmRouter,
		LocalRegistry:    h.llmRouter.LocalRegistry(),
		Storage:          h.storage,
		Router:           h.llmRouter,
		WindowManager:    apibrowser,
		Notifications:    notifications,
		ScheduleNextTick: cfg.scheduleNextTick,
		PromptOpener:     &ex.comp,
	})
	if err := ex.editorObserver.RegisterREPLCommand(llmshell.Manual(), llmHandler); err != nil {
		log.Errorf("register llm repl command: %v", err)
	}

	// Register the top-level `pkg` REPL command for package management.
	pkgHandler := pkgshell.New(pkgshell.Config{
		Manager:       h.pkgmanager.pkg,
		UpdateChecker: h.pkgmanager.uc,
	})
	if err := ex.editorObserver.RegisterREPLCommand(pkgshell.Manual(), pkgHandler); err != nil {
		log.Errorf("register pkg repl command: %v", err)
	}

	dataDir := h.sixDir
	if err := os.MkdirAll(dataDir, 0777); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("mkdir %s: %v", dataDir, err)
	}
	// installDir is where the IDE provisions per-extension toolchains on the
	// workspace host. Extensions resolve provisioned binaries under it via
	// FindInstalledExecutable.
	installDir := installDataDir(cwd, uri, dataDir)
	browser := ex.Browser()
	grantor := newExtensionPromptGrantor(promptOpener, promptStorage,
		cfg.scheduleNextTick, h.trust)
	runner, err := h.extensionRunner.WorkspaceExtensionsRunner(uri, res, cmdAuthorizer,
		h.pkgmanager.pkg, dataDir, installDir, browser, cwd, extExecutor, grantor,
		ed, promptOpener, promptStorage, cfg.scheduleNextTick)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("new workspace extensions runner: %v", err)
	}
	return runner, lsp, dap, promptStorage, nil
}

// swapDirectory resolves where a file the editor for ws opens keeps
// its swap, per file rather than once per editor: a directory path
// only names a directory on the host it was resolved against, and an
// editor rooted in a remote workspace still opens local files.
//
// A file on any other host would need a data directory this editor
// never resolved, so it keeps its swap next to itself. The resolver
// stays a pure function of the file URI because the recovery prompts
// have to derive the same swap entry the open did.
func (h *workspaceManagerHandler) swapDirectory(
	cfg ideConfig, ws workspace.Workspace, uri workspaceapi.URI,
) func(workspaceapi.URI) string {
	if !cfg.editorSwapDir() {
		return nil
	}
	local := filepath.Join(h.sixDir, workspace.SwapDirName)
	host := path.Join(installDataDir(ws, uri, h.sixDir), workspace.SwapDirName)
	return func(file workspaceapi.URI) string {
		switch {
		case file.Scheme() == workspace.FileScheme:
			return local
		case file.Scheme() == uri.Scheme() &&
			file.User() == uri.User() && file.Host() == uri.Host():
			return host
		default:
			return ""
		}
	}
}

func installDataDir(ws workspace.Workspace, uri workspaceapi.URI, localDataDir string) string {
	if uri.Scheme() == workspace.FileScheme {
		return localDataDir
	}
	root, err := ws.InstallDataDir(context.Background())
	switch {
	case err == nil && root != "":
		return root
	case err != nil && !errors.Is(err, errors.ErrUnsupported):
		log.Warnf("resolve install root advertised by workspace host %s: %v; "+
			"falling back to ~/%s", uri.Host(), err, filepath.Base(localDataDir))
	}
	base := filepath.Base(localDataDir)
	remote, err := ws.URI("~/" + base)
	if err != nil {
		log.Warnf("resolve install root ~/%s on workspace host: %v; "+
			"falling back to local data dir %s", base, err, localDataDir)
		return localDataDir
	}
	return remote.Path()
}

func workspaceRootURI(cwd workspace.Workspace, raw workspaceapi.URI) (workspaceapi.URI, error) {
	if cwd == nil {
		panic("workspaceRootURI: cwd workspace is required")
	}
	resolved, err := cwd.URI(".")
	if err != nil {
		return workspaceapi.URI{}, fmt.Errorf("resolve workspace root on host for %s: %w", raw, err)
	}
	return resolved, nil
}

func (h *workspaceManagerHandler) addOrCreateWorkspace(
	uri workspaceapi.URI,
) error {
	err := h.addWorkspace(uri, true, true, -1)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		h.openCreateWorkspacePrompt(h.exHandler(h.focusHandler()), uri)
		return nil
	}
	return err
}

func (h *workspaceManagerHandler) openPrevSessionFiles(
	ex *ex, files []idehistory.File, windows map[uint64]browser.Window,
) (err error) {
	invokeWindow := ex.invokeWindow()
	for _, f := range files {
		if f.URI.String() == fileExplorerURI {
			continue
		}
		uri := f.URI
		win := invokeWindow
		if f.WindowID != 0 {
			if mappedWin, ok := windows[f.WindowID]; ok {
				win = mappedWin
			}
		}
		t, ferr := ex.editFileURI(uri, win, false)
		if ferr != nil {
			err = multierror.Append(err, ferr)
			continue
		}
		ed, ok := t.Handler().(text.Handler)
		if !ok {
			continue
		}
		ed.SetCursorAtScroll(f.Cursor)
	}
	return err
}

func (h *workspaceManagerHandler) restorePreviousSession(
	ex *ex,
	state idehistory.State,
) error {
	ret := new(multierror.Error)
	layout := state.Layout
	layout.Floating = nil
	restoreTerminals := len(state.Terminals) > 0
	windows := h.restoreWorkspaceWindows(ex, state.Files, restoreTerminals,
		layout, state.HasLayout)
	if restoreTerminals {
		ret = multierror.Append(ret,
			restoreOpenTerminalSessions(ex, state.Terminals, windows))
	}
	if len(state.Tasks) > 0 {
		ret = multierror.Append(ret,
			restoreOpenTaskSessions(ex, state.Tasks))
	}
	if len(state.Files) == 0 {
		return ret.ErrorOrNil()
	}
	if h.width == 0 || h.height == 0 {
		// if restoreSession is called on an size 0,0 handler
		// then cursor is not properly set.
		h.openPrevFiles = state.Files
		h.openPrevWindows = windows
		h.openPrevFilesEx = ex
		return ret.ErrorOrNil()
	}
	ret = multierror.Append(ret, h.openPrevSessionFiles(ex, state.Files, windows))
	return ret.ErrorOrNil()
}

func (h *workspaceManagerHandler) restoreWorkspaceWindows(
	ex *ex,
	files []idehistory.File,
	restoreTerminals bool,
	layout tcomponent.TileLayout,
	hasLayout bool,
) map[uint64]browser.Window {
	if !hasLayout {
		return nil
	}
	if len(files) == 0 && !restoreTerminals {
		return nil
	}
	return ex.comp.Browser().RestoreTileLayout(layout, func(windowID uint64) browserapi.Handler {
		return nil
	})
}
func (h *workspaceManagerHandler) nextAvailableWorkspace() (idx int, ok bool) {
	for i := h.focus; i >= 0 && i < len(h.workspaces); i++ {
		if h.slotIsFree(i) {
			return i, true
		}
	}
	for i := 0; i < h.focus && i < len(h.workspaces); i++ {
		if h.slotIsFree(i) {
			return i, true
		}
	}
	return 0, false
}

// slotIsFree reports whether slot i has neither an installed
// workspaceHandler nor an in-flight pending build reserving it.
func (h *workspaceManagerHandler) slotIsFree(i int) bool {
	if h.workspaces[i] != nil {
		return false
	}
	for _, p := range h.pending {
		if p.slot == i {
			return false
		}
	}
	return true
}

// pendingForFocus returns the pending build (if any) reserving the
// currently focused slot.
func (h *workspaceManagerHandler) pendingForFocus() (*pendingWorkspace, bool) {
	for _, p := range h.pending {
		if p.slot == h.focus {
			return p, true
		}
	}
	return nil, false
}

func (h *workspaceManagerHandler) logNonFatalErrs(
	browser browser.Browser,
	configErr error,
	configErrs map[string]error,
) {
	all := configErr
	for key, err := range configErrs {
		err = fmt.Errorf("load %q: %v", key, err)
		all = multierror.Append(all, err)
	}
	if all != nil {
		log.Warn(all)
		_, _ = browser.Notify(browserapi.LevelError, "Config decode error: %v", all)
	}
}

func (h *workspaceManagerHandler) commandReloadWorkspace(args ...string) error {
	i := h.focus
	workspaceURI, _, err := h.closeWorkspace()
	if err != nil {
		return err
	}
	return h.addWorkspace(workspaceURI, true, false, i)
}

func (h *workspaceManagerHandler) commandAddWorkspace(args ...string) error {
	if len(args) == 0 {
		return errors.New("expected at least one argument with the workspace path")
	}
	// Directory completion candidates carry a trailing separator so that
	// accepting one descends instead of terminating the argument; the
	// user can dispatch straight from that state.
	path := command.TrimPartialCandidateSuffix(args[0])

	if uri, err := workspaceapi.ParseURI(path); err == nil {
		return h.addOrCreateWorkspace(uri)
	}

	uri, err := h.homeWorkspace.URI(path)
	if err != nil {
		return err
	}
	return h.addOrCreateWorkspace(uri)
}

func (h *workspaceManagerHandler) commandRenameWorkspace(args ...string) error {
	if h.focusHandler() == h.empty {
		return fmt.Errorf("there's no workspace to rename. " +
			"First you must open one via `workspaceopen`")
	}
	if len(args) == 0 {
		return fmt.Errorf("expected one argument with the new name")
	}
	name := args[0]
	handler := h.workspaces[h.focus]
	handler.tabname = name
	h.Resize(h.width, h.height)
	if err := h.state.PersistWorkspaceState(
		context.Background(), handler.uri); err != nil {
		log.Warnf("persist workspace name %s: %v", handler.uri.String(), err)
	}
	return nil
}

func (h *workspaceManagerHandler) commandWorkspaceReady(args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("expected at least one argument with the command to run")
	}
	if h.lastReservedPending != nil {
		cmd := append([]string(nil), args...)
		h.lastReservedPending.onReady = append(h.lastReservedPending.onReady, cmd)
		return nil
	}
	ex := h.exHandler(h.focusHandler())
	return ex.dispatchCommand(args[0], args[1:]...)
}

func (h *workspaceManagerHandler) commandExtensionReady(args ...string) error {
	if len(args) < 2 {
		return fmt.Errorf("expected extension id and command")
	}
	if h.lastReservedPending != nil {
		return h.commandWorkspaceReady(append([]string{cmdExtensionReady}, args...)...)
	}
	runner := h.focusRunner()
	if runner == nil {
		return fmt.Errorf("no extension runner on the focused workspace")
	}
	id := args[0]
	job := extReadyJob{cmd: args[1], args: append([]string(nil), args[2:]...)}
	ex := h.exHandler(h.focusHandler())
	ch, ok := ex.extReady[id]
	if !ok {
		ch = make(chan extReadyJob, extReadyQueueLimit)
		ex.extReady[id] = ch
		ch <- job
		h.startExtReadyWorker(ex.bgCtx, ex, id, runner, ch)
		return nil
	}
	if len(ch) == extReadyQueueLimit {
		return fmt.Errorf(
			"too many extensionready commands queued for %q (max %d)",
			id, extReadyQueueLimit)
	}
	ch <- job
	return nil
}

// extReadyQueueLimit bounds the per-id extensionready follow-up queue.
const extReadyQueueLimit = 20

// extReadyJob is a queued extensionready follow-up command; the id and
// runner are fixed per worker, so only the command and args vary.
type extReadyJob struct {
	cmd  string
	args []string
}

// startExtReadyWorker drains ch for one extension id, dispatching queued
// commands in submission order once the extension is ready. ctx cancel
// (ex.Close) stops the worker mid-backlog.
func (h *workspaceManagerHandler) startExtReadyWorker(
	ctx context.Context, ex *ex, id string,
	runner extension.Runner, ch chan extReadyJob,
) {
	readyErr := make(chan error, 1)

	go debug.CapturePanicReport(func() {
		readyCtx, cancel := context.WithTimeout(ctx, h.extReadyWait)
		defer cancel()
		err := runner.WaitReady(readyCtx, id)
		// A cancelled parent ctx is ex.Close, handled by the worker; only
		// a genuine deadline becomes a user-facing error.
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			err = fmt.Errorf("extension %q not ready within %s", id, h.extReadyWait)
		}
		readyErr <- err
	})

	go debug.CapturePanicReport(func() {
		var err error
		select {
		case err = <-readyErr:
		case <-ctx.Done():
			return
		}
		for {
			var job extReadyJob
			select {
			case job = <-ch:
			case <-ctx.Done():
				return
			}
			jobErr := err
			if jobErr == nil {
				jobErr = h.waitCommandRegistered(ctx, ex, job.cmd)
			}
			if ctx.Err() != nil {
				return
			}
			if dispErr := h.runExtReadyJob(ctx, ex, id, job, jobErr); dispErr != nil {
				_, _ = h.notifications.current().Notify(
					browserapi.LevelError,
					"extensionready %s: %v", id, dispErr)
			}
		}
	})
}

// runExtReadyJob dispatches a single follow-up command and blocks until
// it completes, so the next job cannot overtake it. The dispatch carries
// a textrpc.Waiter: an out-of-process extension command claims it and
// reports completion on the channel after HandleCommand returns, so the
// wait (bounded by extensionHandleWait) preserves ordering across the RPC
// boundary. An in-process command leaves the waiter unclaimed and is
// already done when dispatch returns. A non-nil jobErr from the
// readiness/registration wait short-circuits dispatch.
func (h *workspaceManagerHandler) runExtReadyJob(
	ctx context.Context, ex *ex, _ string, job extReadyJob, jobErr error,
) error {
	if jobErr != nil {
		return jobErr
	}

	type dispatched struct {
		err     error
		claimed bool
	}
	waiterCh := make(chan error, 1)
	doneCh := make(chan dispatched, 1)
	scheduled := h.scheduleNextTick(func() {
		w := &textrpc.Waiter{Ch: waiterCh}
		err := ex.dispatchCommandCtx(
			textrpc.ContextWithWaiter(ctx, w), job.cmd, job.args...)
		doneCh <- dispatched{err: err, claimed: w.Claimed}
	})
	if !scheduled {
		return fmt.Errorf("could not schedule")
	}

	var d dispatched
	select {
	case d = <-doneCh:
	case <-ctx.Done():
		return nil
	}
	// An unclaimed waiter (in-process command) or a dispatch error (the
	// extension command never reached the wire) means no completion will
	// arrive on the channel; the dispatch result is final.
	if !d.claimed || d.err != nil {
		return d.err
	}

	waitCtx, cancel := context.WithTimeout(ctx, h.extHandleWait)
	defer cancel()
	select {
	case err := <-waiterCh:
		return err
	case <-waitCtx.Done():
		return waitCtx.Err()
	}
}

// extensionCommandWait bounds how long extensionready blocks for the
// follow-up command to be registered after the extension's protocol
// handshake completes. Registration is published asynchronously over
// the extension's editor RPC, so the command may not exist the instant
// WaitReady returns.
const extensionCommandWait = 10 * time.Second

// extensionReadyWait bounds the wait for an extension to become ready,
// so a never-ready extension surfaces an error instead of parking its
// follow-up commands until the workspace is torn down.
const extensionReadyWait = 30 * time.Second

// extensionHandleWait bounds how long an extensionready follow-up waits
// for an out-of-process extension command to finish handling, so a wedged
// extension surfaces an error and the queue keeps draining instead of
// blocking the next follow-up forever.
const extensionHandleWait = 30 * time.Second

// waitCommandRegistered blocks until cmd is registered on ex, the
// timeout elapses, or ctx is cancelled. Registrations are observed
// directly through ex's editor (see commandRegisterObserver), so the
// wait is signalled the moment the command is registered rather than
// polling the registry.
func (h *workspaceManagerHandler) waitCommandRegistered(
	ctx context.Context, ex *ex, cmd string,
) error {
	waitCtx, cancel := context.WithTimeout(ctx, h.extCommandWait)
	defer cancel()
	if err := ex.editorObserver.Wait(waitCtx, cmd); err != nil {
		if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("command %q was not registered within %s",
				cmd, h.extCommandWait)
		}
		return err
	}
	return nil
}

func (h *workspaceManagerHandler) closeWorkspace() (
	workspaceapi.URI, []workspaceapi.URI, error,
) {
	if pending, ok := h.pendingForFocus(); ok {
		uri := pending.uri
		pending.canceled.Store(true)
		pending.cancelCtx()
		h.beginPendingWorkspaceTeardown(pending)
		h.abortPendingWorkspaceBuild(pending)
		if err := h.state.ClearWorkspaceState(
			context.Background(), uri); err != nil {
			log.Warnf("clear workspace state %s: %v", uri.String(), err)
		}
		return uri, nil, nil
	}
	if h.focusHandler() == h.empty {
		return workspaceapi.URI{}, nil, errors.New("workspace tab is empty")
	}

	focus := h.focus
	hm := h.workspaces[focus]
	uri := hm.uri
	tabs := hm.ex.comp.Tabs()
	var files []workspaceapi.URI
	for _, tab := range tabs {
		// only reload with tabs that were created by workspace
		_, ok := tab.Closer().(workspace.FlusherCloser)
		uri := tab.URI()
		if ok && uri != (workspaceapi.URI{}) && uri.Scheme() != "" {
			files = append(files, uri)
		}
	}

	h.persistWorkspaceStateOnClose(hm)
	// ex.Close is UI-owned and idempotent: closing it here establishes
	// e.closed before the background goroutine re-enters it via
	// workspaceHandler.Close, keeping all UI teardown on the loop.
	if err := hm.ex.Close(); err != nil {
		log.Error(err)
	}
	// Detach the raw workspace so the background managerWorkspace.Close
	// cannot write the manager maps off-loop.
	if raw, ok := h.workspace.RemoveWorkspace(hm.uri); ok {
		hm.cwd = raw
	}
	done := make(chan struct{})
	h.closing[uri.String()] = done
	h.closeWG.Add(1)
	go debug.CapturePanicReport(func() {
		defer h.closeWG.Done()
		if err := hm.closeAndRemove(); err != nil {
			log.Error(err)
		} else {
			log.Debugf("Closed all workspace resources successfully")
		}
		h.mu.Lock()
		delete(h.closing, uri.String())
		h.mu.Unlock()
		close(done)
	})

	h.workspaces[focus] = nil
	h.workspaceCount--
	h.persistLastSession()

	for i := h.focus; i >= 0; i-- {
		if h.workspaces[i] != nil {
			h.switchToWorkspace(i)
			return uri, files, nil
		}
	}

	// for resize of current workspace with empty
	h.switchToWorkspace(h.focus)

	return uri, files, nil
}

func (h *workspaceManagerHandler) commandCloseWorkspace(args ...string) error {
	_, _, err := h.closeWorkspace()
	return err
}

func (h *workspaceManagerHandler) commandSwitchToWorkspace(args ...string) error {
	if len(args) == 0 {
		return errors.New("invalid arguments. " +
			"Expecting 1 argument with workspace number")
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("invalid workspace: %s", err)
	}
	n-- // UI does not use 0-based indexing
	if n < 0 || n >= len(h.workspaces) {
		return fmt.Errorf("invalid workspace: there's only %d workspaces",
			len(h.workspaces))
	}
	h.switchToWorkspace(n)
	return nil
}

func (h *workspaceManagerHandler) moveWorkspace(args ...string) error {
	if len(args) == 0 {
		return errors.New("command expects at least one argument")
	}

	var err error
	var next int
	curr := h.focus
	switch args[0] {
	case "right":
		if curr+1 == workspaceSlots {
			return errors.New("workspace is already at the last slot")
		}
		next = curr + 1
	case "left":
		if curr == 0 {
			return errors.New("workspace is already at the first slot")
		}
		next = curr - 1
	default:
		next, err = strconv.Atoi(args[0])
		if err != nil {
			return errInvalidTab
		}
		// next is 1-indexed
		if next == 0 {
			return errors.New("the first worskpace slot is 1")
		}
		if next > len(h.workspaces) {
			return fmt.Errorf("the last worskpace slot is %d", len(h.workspaces))
		}
		next--
	}
	temp := h.workspaces[curr]
	h.workspaces[curr] = h.workspaces[next]
	h.workspaces[next] = temp
	h.focus = next
	h.events.setFocus(h.focusURI())
	h.persistLastSession()
	h.Resize(h.width, h.height)
	return err
}

func (h *workspaceManagerHandler) Close() (ret error) {
	for _, p := range h.pending {
		p.canceled.Store(true)
		p.cancelCtx()
		h.beginPendingWorkspaceTeardown(p)
		h.abortPendingWorkspaceBuild(p)
	}
	h.mu.Unlock()
	h.pendingWG.Wait()
	// Background workspace closes must finish before the shared
	// resources below (llmRouter, home LSP/DAP) are torn down. Pending
	// cancellation above guarantees gated reopen waiters exit via their
	// canceled path once these complete, so this cannot deadlock.
	h.closeWG.Wait()
	h.mu.Lock()
	for _, hm := range h.workspaces {
		if hm == nil {
			continue
		}
		h.persistWorkspaceStateOnClose(hm)
		if err := hm.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if err := h.empty.Close(); err != nil {
		ret = multierror.Append(ret, err)
	}
	if h.homeRunner != nil {
		if err := h.homeRunner.(io.Closer).Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if h.homeLSPManager != nil {
		if err := h.homeLSPManager.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if h.homeDAPManager != nil {
		if err := h.homeDAPManager.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if h.pkgmanager != nil {
		if err := h.pkgmanager.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if h.scavenger != nil {
		if err := h.scavenger.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if h.llmRouter != nil {
		if err := h.llmRouter.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	// Closed last: every workspace extension runner served this shared
	// service and must release its partition handles first.
	if h.extensionsStorage != nil {
		if err := h.extensionsStorage.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if merr, ok := ret.(*multierror.Error); ok {
		return merr.ErrorOrNil()
	}
	return ret
}

type workspaceHandler struct {
	*ex
	tabname             string
	attentionAttr       term.Attributes
	vctrlService        vctrl.Service
	cursorHistoryCloser io.Closer
	symbolDBCloser      io.Closer
	cancelCtx           func()
	uri                 workspaceapi.URI
	Extensions          atomic.Value
	cwd                 workspace.Workspace
	closeOnce           sync.Once
	closeErr            error
	historyCloser       io.Closer
	lspManager          *idelsp.Manager
	dapManager          *idedebug.Manager
	promptStorage       storageapi.Service
}

func (hm *workspaceHandler) Close() error {
	hm.closeOnce.Do(func() {
		var ret error
		if err := hm.ex.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
		if runner := hm.Extensions.Load(); runner != nil {
			if err := runner.(io.Closer).Close(); err != nil {
				ret = multierror.Append(ret, err)
			}
		}
		if closer, ok := hm.vctrlService.(io.Closer); ok {
			if err := closer.Close(); err != nil {
				ret = multierror.Append(ret, err)
			}
		}
		if hm.cursorHistoryCloser != nil {
			if err := hm.cursorHistoryCloser.Close(); err != nil {
				ret = multierror.Append(ret, err)
			}
		}
		if hm.symbolDBCloser != nil {
			if err := hm.symbolDBCloser.Close(); err != nil {
				ret = multierror.Append(ret, err)
			}
		}
		if hm.lspManager != nil {
			if err := hm.lspManager.Close(); err != nil {
				ret = multierror.Append(ret, err)
			}
		}
		if hm.dapManager != nil {
			if err := hm.dapManager.Close(); err != nil {
				ret = multierror.Append(ret, err)
			}
		}
		if hm.promptStorage != nil {
			if err := hm.promptStorage.Close(); err != nil {
				ret = multierror.Append(ret, err)
			}
		}
		// cancel at the end, so fs event processing is not
		// vacated before everything else is still potentially
		// sending events (i.e. mem scheme)
		hm.cancelCtx()
		hm.closeErr = ret
	})
	return hm.closeErr
}

func (hm *workspaceHandler) closeAndRemove() (ret error) {
	if err := hm.Close(); err != nil {
		ret = multierror.Append(ret, err)
	}
	if hm.cwd != nil {
		if err := hm.cwd.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
		hm.cwd = nil
	}
	return ret
}

func (h *workspaceManagerHandler) initTabs(
	cfg ideConfig, workspacesBarHeight, workspacesBarOffset int,
	workspacesBarFrame bool,
) {
	h.workspacesBarHeight = workspacesBarHeight
	h.bar.SetBorder(workspacesBarFrame)
	h.bar.SetNameSeparator(cfg.tabNameSeparator())
	h.shadedBar = handler.NewShadedTabs(&h.bar, handler.ShadedTabsConfig{
		Shader:      cfg.animationsActiveTabShader(animActiveWorkspaceTab),
		FPS:         cfg.animationsActiveTabFPS(animActiveWorkspaceTab),
		Loop:        cfg.animationsActiveTabLoop(animActiveWorkspaceTab),
		DefAttr:     cfg.nonFocusTabAttr(),
		Interrupter: h.events.globalInterrupter(),
		Active:      h.activeWorkspaceBarIndices,
	})
	var bar tui.Handler = h.shadedBar
	if workspacesBarOffset != 0 {
		v := new(handlerapi.Virtual[*handler.ShadedTabs])
		v.C = h.shadedBar
		v.Move(term.Coordinates{X: workspacesBarOffset})
		bar = v
	}
	h.union.UnionBottomFrame(bar, h.barSize(), workspacesBarFrame)
	h.refreshWorkspaceActivity()
}

// workspaceActive reports whether any tab of the workspace in slot is
// marked active by an extension.
func (h *workspaceManagerHandler) workspaceActive(slot int) bool {
	w := h.workspaces[slot]
	return w != nil && w.ex != nil && w.ex.comp.HasActiveTabs()
}

// activeWorkspaceBarIndices returns the workspace bar indices of the
// workspaces with at least one active tab, whether or not they are in
// focus.
func (h *workspaceManagerHandler) activeWorkspaceBarIndices() []int {
	var ret []int
	for idx, slot := range h.barIdxToSlot {
		if h.workspaceActive(slot) {
			ret = append(ret, idx)
		}
	}
	return ret
}

// refreshWorkspaceActivity runs the workspace bar's active-tab effect
// while the bar is shown and any workspace has an active tab. It must
// be called on the host event loop whenever either can change.
func (h *workspaceManagerHandler) refreshWorkspaceActivity() {
	if h.shadedBar == nil {
		// initTabs has not run yet; it refreshes once the bar exists.
		return
	}
	anyActive := false
	for slot := range h.workspaces {
		if h.workspaceActive(slot) {
			anyActive = true
			break
		}
	}
	h.shadedBar.SetRunning(anyActive && h.drawBar())
}

func (h *workspaceManagerHandler) subscribeAllCommands(ex *ex) error {
	err := ex.subscribeCommands()
	if err != nil {
		return fmt.Errorf("subscribe ex commands: %w", err)
	}
	err = h.subscribeActiveWorkspaceCommands(ex)
	if err != nil {
		return fmt.Errorf("subscribe workspace commands: %w", err)
	}
	err = h.subscribeAllExternalCommands(ex)
	if err != nil {
		return fmt.Errorf("subscribe external commands: %w", err)
	}
	err = h.subscribeAllExternalREPLCommands(ex)
	if err != nil {
		return fmt.Errorf("subscribe external repl commands: %w", err)
	}
	err = ex.editorObserver.SubscribeCommand(textapi.CommandManual{
		Name: cmdMacroRecord,
		Summary: "Toggle recording all user key events into the given clipboard register. " +
			"Run `record a` to start capturing keys into register `a`, then run `record a` " +
			"again to stop recording and save the key sequence. Recorded macros share the " +
			"same register namespace as editor copy/paste, so different register IDs can hold " +
			"different macros (`record a`, `record b`, `record +`, etc.). Replay a recorded " +
			"macro by pairing this command with echo's register instruction: " +
			"`echo {register}a` reads register `a`, parses the recorded keys, and sends them " +
			"back through the IDE event loop. Echo sequences can also combine literal keys, " +
			"instructions, and registers, for example `echo i{register}a<esc>{register}b`.",
		Synopsis: "[<register>]",
	}, h.macro)
	if err != nil {
		return fmt.Errorf("subscribe macro commands: %w", err)
	}
	return nil
}

type commandAllWorkspace struct {
	man     textapi.CommandManual
	handler func(*workspaceManagerHandler, ...string) error
}

func (h *workspaceManagerHandler) subscribeActiveWorkspaceCommands(ex *ex) (ret error) {
	workspaceActiveCommands := map[string]commandAllWorkspace{
		cmdAddWorkspace: {
			handler: (*workspaceManagerHandler).commandAddWorkspace,
			man: textapi.CommandManual{
				Summary: "Opens the workspace at the given URI in the current " +
					"workspace slot if it's empty, or in the next available slot if it's not. " +
					"If no scheme is present in the URI, file:// is assumed.",
				Synopsis: "[<scheme>:][//[<userinfo>@]<host>][/]<workspacepath>",
			},
		},
		cmdRenameWorkspace: {
			handler: (*workspaceManagerHandler).commandRenameWorkspace,
			man: textapi.CommandManual{
				Summary: "Renames the workspace tab. The tab is displayed at the " +
					"bottom of the screen when multiple workspaces are open.",
				Synopsis: "<name>",
			},
		},
		cmdCloseWorkspace: {
			handler: (*workspaceManagerHandler).commandCloseWorkspace,
			man: textapi.CommandManual{
				Summary: "Closes the current active workspace and switches focus " +
					"to the previous workspace.",
			},
		},
		cmdReloadWorkspace: {
			handler: (*workspaceManagerHandler).commandReloadWorkspace,
			man: textapi.CommandManual{
				Summary: "Reloads the current active workspace, along with all extensions.",
			},
		},
		cmdSwitchToWorkspace: {
			handler: (*workspaceManagerHandler).commandSwitchToWorkspace,
			man: textapi.CommandManual{
				Summary:  "Switches the current active workspace to the workspace at the given position.",
				Synopsis: "(1|2|3|4|5|6|7|8|9)",
			},
		},
		cmdMoveWorkspace: {
			man: textapi.CommandManual{
				Summary: "Moves the workspace tab in focus in the given direction " +
					"within the tabs list, or to an absolute position if a number is passed.",
				Synopsis: "(right|left|1|2|3|4|5|6|7|8|9)",
			},
			handler: (*workspaceManagerHandler).moveWorkspace,
		},
		cmdWorkspaceReady: {
			handler: (*workspaceManagerHandler).commandWorkspaceReady,
			man: textapi.CommandManual{
				Summary: "Runs another workspace command once the most recently " +
					"issued `workspaceopen` has finished loading. If no workspace is " +
					"currently being loaded, the command is dispatched immediately " +
					"against the focused workspace.",
				Synopsis: "<command> [<args>...]",
			},
		},
		cmdExtensionReady: {
			handler: (*workspaceManagerHandler).commandExtensionReady,
			man: textapi.CommandManual{
				Summary: "Runs another command once the extension with the given " +
					"id has finished initializing on the workspace. If a " +
					"workspaceopen is currently pending, the wait starts after " +
					"that workspace finishes installing.",
				Synopsis: "<extension-id> <command> [<args>...]",
			},
		},
	}
	return h.subscribeInternalCommands(ex, workspaceActiveCommands)
}

func (h *workspaceManagerHandler) subscribeInternalCommands(
	ex *ex,
	commands map[string]commandAllWorkspace,
) (ret error) {
	for cmd, man := range commands {
		man.man.Name = cmd
		err := ex.editorObserver.SubscribeCommand(man.man, text.FuncCommandHandler(
			func(ctx context.Context, cmd textapi.Command) error {
				return man.handler(h, cmd.Args...)
			}, func(ctx context.Context, cmd textapi.Command) (
				iterator.Iterator[string], string, error,
			) {
				return h.completeCommand(ctx, cmd)
			}))
		if err != nil {
			ret = multierror.Append(ret, fmt.Errorf("subscribe command '%s': %w", cmd, err))
		}
	}
	return ret
}

func (h *workspaceManagerHandler) completeCommand(
	ctx context.Context, cmd textapi.Command,
) (iterator.Iterator[string], string, error) {
	switch cmd.Name {
	case cmdAddWorkspace:
		// History first so re-opening a previously opened workspace is a
		// single pick; directory completion follows. DirsCompleter only
		// inspects the trailing token, so passing the command name as
		// args[0] (required by HistoryCompleter to strip the prefix) is
		// safe for both.
		//
		// History stores workspace paths canonically, without the partial
		// marker, so its entries are marked here: every workspaceopen
		// argument is a directory, and picking one must not stop the user
		// from descending further.
		argv := append([]string{cmd.Name}, cmd.Args...)
		completers := append([]command.Completer{
			command.PartialCompleter(command.HistoryCompleter(h.commandHistory)),
			command.NonRecursiveDirsCompleter(h.empty.workspace),
		}, h.workspaceOpenCompleters...)
		return command.MultiCompleter(completers...).Complete(ctx, argv)
	case cmdMoveWorkspace:
		if len(cmd.Args) <= 1 {
			options := []string{"left", "right", "1", "2",
				"3", "4", "5", "6", "7", "8", "9"}
			return iterator.FromSlice(options), "", nil
		}
		return iterator.FromSlice[string](nil), "", nil
	case cmdSwitchToWorkspace:
		var tabNames []string
		if len(cmd.Args) <= 1 {
			for i, h := range h.workspaces {
				if h == nil {
					tabNames = append(tabNames, strconv.Itoa(i+1))
					continue
				}
				pretty := strconv.Itoa(i + 1)
				name := h.uri.String()
				pretty += " " + name
				tabNames = append(tabNames, pretty)
			}
		}
		return iterator.FromSlice(tabNames), "", nil
	default:
		return iterator.FromSlice[string](nil), "", nil
	}
}

func (h *workspaceManagerHandler) subscribeAllExternalCommands(ex *ex) (ret error) {
	var cmds []externalCommand
	for _, cmd := range h.externalCommands {
		cmds = append(cmds, cmd)
	}
	return h.subscribeExternalCommands(ex, cmds...)
}

func (h *workspaceManagerHandler) subscribeExternalCommands(
	ex *ex, commands ...externalCommand,
) (ret error) {
	for _, cmd := range commands {
		err := ex.Editor().SubscribeCommand(cmd.cmd, cmd.handler)
		if err != nil {
			ret = multierror.Append(ret,
				fmt.Errorf("subscribe command '%s': %v", cmd.cmd.Name, err))
		}
	}
	return ret
}

type externalCommand struct {
	cmd     textapi.CommandManual
	handler text.CommandHandler
}

func (h *workspaceManagerHandler) subscribeAllExternalREPLCommands(ex *ex) (ret error) {
	var cmds []externalREPLCommand
	for _, cmd := range h.externalREPLCommands {
		cmds = append(cmds, cmd)
	}
	return h.subscribeExternalREPLCommands(ex, cmds...)
}

func (h *workspaceManagerHandler) subscribeExternalREPLCommands(
	ex *ex, commands ...externalREPLCommand,
) (ret error) {
	for _, cmd := range commands {
		err := ex.Editor().RegisterREPLCommand(cmd.cmd, cmd.handler)
		if err != nil {
			ret = multierror.Append(ret,
				fmt.Errorf("register repl command '%s': %v", cmd.cmd.Name, err))
		}
	}
	return ret
}

type externalREPLCommand struct {
	cmd     textapi.CommandManual
	handler textapi.REPLHandler
}

func (h *workspaceManagerHandler) registerREPLCommand(
	cmd textapi.CommandManual, handler textapi.REPLHandler,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.externalREPLCommands[cmd.Name]; ok {
		return fmt.Errorf("repl command '%s' already registered", cmd.Name)
	}

	extCmd := externalREPLCommand{cmd: cmd, handler: handler}

	ret := h.subscribeExternalREPLCommands(h.empty, extCmd)
	for _, w := range h.workspaces {
		if w == nil {
			continue
		}
		if err := h.subscribeExternalREPLCommands(w.ex, extCmd); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if ret != nil {
		return ret
	}

	h.externalREPLCommands[cmd.Name] = extCmd
	return nil
}

func (h *workspaceManagerHandler) SubscribeCommandForWorkspace(
	uri workspaceapi.URI, cmd textapi.CommandManual, handler text.CommandHandler,
) error {
	extCmd := externalCommand{cmd: cmd, handler: handler}
	if uri.String() == h.homeURI.String() {
		return h.subscribeExternalCommands(h.empty, extCmd)
	}
	for _, w := range h.workspaces {
		if w == nil || w.uri.String() != uri.String() {
			continue
		}
		return h.subscribeExternalCommands(w.ex, extCmd)
	}
	return errors.New("workspace with given uri not found")
}

func (h *workspaceManagerHandler) UnsubscribeCommandForWorkspace(
	uri workspaceapi.URI, name string,
) error {
	if uri.String() == h.homeURI.String() {
		return h.empty.comp.UnsubscribeCommand(name)
	}
	for _, w := range h.workspaces {
		if w == nil || w.uri.String() != uri.String() {
			continue
		}
		return w.ex.comp.UnsubscribeCommand(name)
	}
	return errors.New("command is not registered")
}

func (h *workspaceManagerHandler) subscribeCommand(
	cmd textapi.CommandManual, handler text.CommandHandler,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := h.externalCommands[cmd.Name]; ok {
		return fmt.Errorf("command '%s' already registered", cmd.Name)
	}

	extCmd := externalCommand{cmd: cmd, handler: handler}

	// subscribe in current workspaces
	ret := h.subscribeExternalCommands(h.empty, extCmd)
	for _, w := range h.workspaces {
		if w == nil {
			continue
		}
		if err := h.subscribeExternalCommands(w.ex, extCmd); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if ret != nil {
		return ret
	}

	// store for future workspaces
	h.externalCommands[cmd.Name] = extCmd
	return nil
}

type externalEvents struct {
	events  []textapi.EventType
	handler text.EventHandler
}

func (h *workspaceManagerHandler) subscribeExternalEvents(
	ex *ex, evs ...externalEvents,
) (ret error) {
	for _, cmd := range evs {
		err := ex.comp.SubscribeEvents(cmd.events, cmd.handler)
		if err != nil {
			ret = multierror.Append(ret, fmt.Errorf("subscribe events: %w", err))
		}
	}
	return ret
}

func (h *workspaceManagerHandler) subscribeAllEvents(
	cfg ideConfig, ex *ex,
) error {
	if err := h.subscribeExternalEvents(ex, h.externalEvents...); err != nil {
		return err
	}
	if cfg.editorAutoSave() && !ex.ed.IsExternal() {
		saver := autoSaverFactory(&ex.comp, ex.notifications,
			cfg.scheduleNextTick, defaultAutoSaveDelay)
		if err := ex.comp.SubscribeEvents(autoSaveEvents, saver); err != nil {
			return fmt.Errorf("subscribe autoSaver: %w", err)
		}
	}
	return nil
}

func (h *workspaceManagerHandler) SubscribeEvents(
	events []textapi.EventType, handler text.EventHandler,
) error {
	extEvt := externalEvents{events: events, handler: handler}

	// subscribe in current workspaces
	ret := h.subscribeExternalEvents(h.empty, extEvt)
	for _, w := range h.workspaces {
		if w == nil {
			continue
		}
		if err := h.subscribeExternalEvents(w.ex, extEvt); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if ret != nil {
		return ret
	}

	// store for future workspaces
	h.externalEvents = append(h.externalEvents, extEvt)
	return nil
}

func (h *workspaceManagerHandler) UnsubscribeEvents(
	handler text.EventHandler,
) (ok bool, ret error) {
	ok, err := h.empty.comp.UnsubscribeEvents(handler)
	if err != nil {
		ret = multierror.Append(ret, err)
	}
	for _, w := range h.workspaces {
		if w == nil {
			continue
		}
		if exOk, err := w.ex.comp.UnsubscribeEvents(handler); err != nil {
			ret = multierror.Append(ret, err)
		} else if !exOk {
			ok = false
		}
	}

	// remove for future workspaces
	for i, extEvt := range h.externalEvents {
		if extEvt.handler == handler {
			h.externalEvents[i] = h.externalEvents[len(h.externalEvents)-1]
			h.externalEvents = h.externalEvents[:len(h.externalEvents)-1]
			return
		}
	}
	ok = false
	return
}

func (h *workspaceManagerHandler) exHandler(focus tui.Handler) *ex {
	if ex, ok := focus.(*ex); ok {
		return ex
	}
	if wh, ok := focus.(*workspaceHandler); ok {
		return wh.ex
	}

	panic("unknown focus handler")

}

func (h *workspaceManagerHandler) persistWorkspaceStateOnClose(hm *workspaceHandler) {
	if err := h.state.StoreWorkspaceStateForClose(
		context.Background(), hm.uri,
		exSnapshotter{ex: hm.ex, wh: hm}); err != nil {
		log.Warnf("persist workspace state %s: %v", hm.uri.String(), err)
	}
	if hm.historyCloser != nil {
		_ = hm.historyCloser.Close()
		hm.historyCloser = nil
	}
}

func (h *workspaceManagerHandler) Interrupt(ctx context.Context) error {
	return h.events.globalInterrupter().Interrupt(ctx)
}

// persistLastSession records the currently installed workspaces so the
// next run can offer to reopen them. It runs synchronously on the event
// loop: the document is small and the write must not race with the
// installs and closes that mutate h.workspaces.
func (h *workspaceManagerHandler) persistLastSession() {
	session := idehistory.Session{FocusSlot: h.focus, SavedAt: time.Now()}
	for i, w := range h.workspaces {
		if w == nil {
			continue
		}
		session.Workspaces = append(session.Workspaces,
			idehistory.SessionWorkspace{URI: w.uri, Slot: i})
	}
	if err := h.state.StoreLastSession(context.Background(), session); err != nil {
		log.Warnf("persist last session: %v", err)
	}
}

// lastSessionReopenTargets returns the previous session's workspaces
// that are neither installed nor already loading.
func (h *workspaceManagerHandler) lastSessionReopenTargets() []idehistory.SessionWorkspace {
	var targets []idehistory.SessionWorkspace
	for _, w := range h.lastSession.Workspaces {
		if _, ok := h.findInstalledSlot(w.URI); ok {
			continue
		}
		if h.isPending(w.URI) {
			continue
		}
		targets = append(targets, w)
	}
	return targets
}

// maybeReopenLastSession offers to reopen the workspaces left open by
// the previous session. It is a one-shot startup action. Reopening
// other workspaces is always an explicit choice: unlike per-workspace
// state restore, it is deliberately not covered by
// workspace.auto_restore.
func (h *workspaceManagerHandler) maybeReopenLastSession() {
	if !h.reopenPending {
		return
	}
	h.reopenPending = false
	targets := h.lastSessionReopenTargets()
	h.lastSession = idehistory.Session{}
	if len(targets) == 0 {
		return
	}
	h.openReopenSessionPrompt(h.focusEx(), targets)
}

func (h *workspaceManagerHandler) reopenSessionWorkspaces(
	targets []idehistory.SessionWorkspace,
) {
	for _, w := range targets {
		slot := w.Slot
		if slot < 0 || slot >= len(h.workspaces) || !h.slotIsFree(slot) {
			slot = -1
		}
		if err := h.addWorkspace(w.URI, true, false, slot); err != nil {
			_, _ = h.notifications.current().Notify(browserapi.LevelError,
				"Failed to reopen workspace %s: %v", w.URI.String(), err)
		}
	}
}

func (h *workspaceManagerHandler) waitInflight() {
	h.mu.Lock()
	exes := make([]*ex, 0, len(h.workspaces))
	for _, w := range h.workspaces {
		if w == nil || w.ex == nil {
			continue
		}
		exes = append(exes, w.ex)
	}
	if h.empty != nil {
		exes = append(exes, h.empty)
	}
	h.mu.Unlock()
	for _, e := range exes {
		e.waitInflight()
	}
	h.waitAliasRuns(exes)
}

// waitAliasRuns is TEST ONLY and blocks until no ex in exes has a
// command dispatch in flight or queued, or a bound elapses. Callers must
// not hold h.mu.
func (h *workspaceManagerHandler) waitAliasRuns(exes []*ex) {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		busy := false
		for _, e := range exes {
			if e.runInFlight != nil || len(e.runQueue) > 0 {
				busy = true
				break
			}
		}
		h.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

// waitClosing is TEST ONLY and blocks until every background workspace teardown spawned
// by closeWorkspace has completed
func (h *workspaceManagerHandler) waitClosing() {
	h.closeWG.Wait()
}

func (h *workspaceManagerHandler) setReleaseManager(releaseManager release.Manager) {
	notifications := h.notifications.current()
	if h.pkgmanager == nil {
		h.pkgmanager = new(pkgManager)
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := h.pkgmanager.pkg.Reconcile(ctx); err != nil {
				_, _ = notifications.Notify(browserapi.LevelError,
					"package manager: clean up: %v", err)
			}
			err := h.pkgmanager.pkg.ProcessInstalledSettings(ctx)
			if err != nil {
				_, _ = notifications.Notify(browserapi.LevelError,
					"package manager: process installed settings: %v", err)
			} else {
				log.Debugf("processed all installed settings")
			}
		}()
	} else {
		h.pkgmanager.Close()
	}
	wm := currentWorkspaceWindowManager{root: h}
	parser := &lazyParser{root: h}
	editorMode := ""
	autoInstall := false
	if cfg, err := h.reloadConfig(); err == nil {
		editorMode = cfg.pkgEditorMode()
		autoInstall = cfg.updatesAutoInstall()
	}
	h.pkgmanager.init(notifications, releaseManager, wm,
		h.ideStorage, h.homeWorkspace, h.sixDir, h.configPath, h.frameCharSet,
		h, h, h.scheduleNextTick, parser,
		editorMode, autoInstall, h.afterPackageConfigMerge, h.gitRemoteURL,
		h.trust)
}

func (h *workspaceManagerHandler) openURI(file workspaceapi.URI, focus bool) error {
	ex := h.exHandler(h.focusHandler())
	var err error
	if focus {
		_, err = ex.editFileURI(file, ex.invokeWindow(), false)
	} else {
		_, err = ex.comp.Open(file)
	}
	if err != nil {
		return err
	}
	return err
}

func (h *workspaceManagerHandler) openFile(file string, focus bool) error {
	ex := h.exHandler(h.focusHandler())
	uri, err := ex.workspace.URI(file)
	if err != nil {
		return err
	}
	return h.openURI(uri, focus)
}

type workspaceTabManager struct {
	workspace *workspaceHandler
	parent    *workspaceManagerHandler
	tm        browser.TabManager
}

func (f *workspaceTabManager) Tab(uri workspaceapi.URI, icon rune, name string, h browserapi.Handler) (
	browserapi.Handler, error,
) {
	if f.tm == nil {
		return nil, errors.New("tab manager is not initialized")
	}
	if uri == (workspaceapi.URI{}) {
		panic("nil uri")
	}
	return f.tm.Tab(uri, icon, name, h)
}

func (f *workspaceTabManager) SetTabName(
	uri workspaceapi.URI, name string, attr term.Attributes,
) error {
	if f.tm == nil {
		return errors.New("tab manager is not initialized")
	}
	f.parent.scheduleNextTick(func() {
		_ = f.tm.SetTabName(uri, name, attr)
		if attr == (term.Attributes{}) {
			log.Debugf("SetTabName called on workspace tab manager %p: "+
				"empty attributes", f)
			return
		}
		// home workspace: can't set workspace tab attributes
		if f.workspace == nil {
			log.Debugf("SetTabName called on workspace tab manager %p: "+
				"home workspace", f)
			return
		}
		// set workspace tab attr as well if workspace not in focus
		if f.parent.focusHandler() != f.workspace {
			log.Debugf("SetTabName called on workspace tab manager %p: "+
				"setting attention attrs", f)
			f.workspace.attentionAttr = attr
			f.parent.Resize(f.parent.width, f.parent.height)
			return
		}
		log.Debugf("SetTabName called on workspace tab manager %p: "+
			"workspace in focus", f)
	})
	return nil
}

func (f *workspaceTabManager) OnTabExit(uri workspaceapi.URI) bool {
	return f.tm != nil && f.tm.OnTabExit(uri)
}

// SetTabActivity satisfies browser.TabManager. Callers may be off the
// event loop, so the mark is applied on the next tick; the browser then
// reports the change back through refreshWorkspaceActivity.
func (f *workspaceTabManager) SetTabActivity(
	uri workspaceapi.URI, active bool,
) error {
	if f.tm == nil {
		return errors.New("tab manager is not initialized")
	}
	f.parent.scheduleNextTick(func() {
		if err := f.tm.SetTabActivity(uri, active); err != nil {
			log.Debugf("SetTabActivity on workspace tab manager %p: %v", f, err)
		}
	})
	return nil
}

func lspCommandsConfig(
	uri workspaceapi.URI, cfg ideConfig, notifications browserapi.Notifications,
	interrupter term.Interrupter, parser syntaxapi.Parser,
	diagnosticsSource lspcmd.DiagnosticsSource,
) lspcmd.Config {
	cmdcfg := lspcmd.DefaultConfig()
	cmdcfg.RootURI = uri
	cmdcfg.Parser = parser
	cmdcfg.ScheduleNextTick = cfg.scheduleNextTick
	cmdcfg.DiagnosticsSource = diagnosticsSource
	cmdcfg.Highlight.Delay = 50 * time.Millisecond
	cmdcfg.Highlight.WriteAttr = term.Attributes{Attrs: term.AttrBold | term.AttrUnderline}
	cmdcfg.Highlight.ReadAttr = term.Attributes{Attrs: term.AttrUnderline}
	cmdcfg.SignatureHelp.TriggerCharacters = []string{"(", ","}
	cmdcfg.SignatureHelp.AutoTrigger = true
	cmdcfg.Interrupter = interrupter
	cmdcfg.Hover.MarkdownConfig = markdown.DefaultConfig()
	cmdcfg.Hover.MarkdownConfig.Parser = parser
	cmdcfg.Hover.MarkdownConfig.ScheduleNextTick = cfg.scheduleNextTick
	cmdcfg.Hover.MarkdownHandlerOptions = []handlermarkdown.Option{
		handlermarkdown.WithOnLinkClick(func(link *url.URL) bool {
			if link.Scheme != "http" && link.Scheme != "https" {
				return false
			}
			linkstr := link.String()
			meta := clipboard.Data{Text: linkstr}
			err := cfg.clipboard().Copy(clipboard.DefaultRegisterID, meta)
			if err != nil {
				_, _ = notifications.Notify(browserapi.LevelError,
					"copy URL to clipboard: %v", err)
			} else {
				_, _ = notifications.Notify(browserapi.LevelSuccess,
					"copied URL %s to clipboard", linkstr)
			}
			return true
		}),
	}
	return cmdcfg
}
