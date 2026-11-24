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
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// ccppLanguageID is the server id one clangd serves both C and C++
// under, and the name of the Rune package that ships clangd.
const ccppLanguageID = "ccpp"

// ccppExtensions are the file extensions the extension serves. Headers
// are in since clangd serves them like sources.
var ccppExtensions = []string{
	".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".hh", ".hxx", ".h++", ".inl", ".ipp", ".tpp",
}

// ccppToolNames are the executables the ccpp package ships: clangd for
// the server and Bear, which turns a Make or Autotools build into a
// compilation database for `ccpp compdb generate`.
var ccppToolNames = []string{"clangd", "bear"}

func newExtension() (extensionapi.WorkspaceExtension, extensionapi.Metadata) {
	ext := &ccppExtension{}
	meta := extensionapi.Metadata{
		DeveloperID:    "Unstable Build",
		DeveloperEmail: "it@unstable.build",
		DeveloperKey:   "064D4ABCFA6D9338",
		ExtensionID:    ccppLanguageID,
		ExtensionName:  "C and C++ Language Extension",
		// debug.Tag is stamped by the rune-language-ccpp build with the package's release tag.
		ExtensionVersion: debug.Tag,
		Permissions: extensionapi.NewPermissions(
			extensionapi.PermissionLSP,
			extensionapi.PermissionEditor,
			extensionapi.PermissionCommands,
			extensionapi.PermissionConfig,
			extensionapi.PermissionBrowserWindowManager,
			extensionapi.PermissionBrowserResourceOpener,
			extensionapi.PermissionNotifications,
			extensionapi.PermissionExecute,
			extensionapi.PermissionFileSystem,
			extensionapi.PermissionStorage,
			extensionapi.PermissionPackages,
		),
	}
	return ext, meta
}

type ccppExtension struct{}

func (e *ccppExtension) ExtendWorkspace(
	ctx context.Context, w *extensionapi.Workspace, cfg config.Config,
) error {
	return e.extendWorkspaceWith(ctx,
		w.FileSystem(ctx),
		w.Executor(ctx),
		w.Notifications(ctx),
		w.LSP(ctx),
		w.Editor(ctx),
		w.WindowManager(ctx),
		w.Prompter(ctx),
		w.ResourceOpener(ctx),
		w.Storage(ctx),
		w,
		cfg,
		w.RegisterREPLCommand,
		w.RegisterCommand,
	)
}

func (e *ccppExtension) extendWorkspaceWith(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	lsp semanticapi.LSP,
	editor textapi.Editor,
	wm browserapi.WindowManager,
	prompter browserapi.Prompter,
	opener browserapi.ResourceOpener,
	storage storageapi.Service,
	inst langext.Installer,
	cfg config.Config,
	registerREPL func(textapi.CommandManual, textapi.REPLHandler) error,
	registerCommand func(textapi.CommandManual, textapi.CommandHandler) error,
) error {
	cwd, err := fs.URI(".")
	if err != nil {
		return fmt.Errorf("resolve cwd uri: %w", err)
	}
	wsRoot := langext.Root{Dir: cwd.Path(), URI: fmt.Sprintf("file://%s", cwd.Path())}
	compdb := newCompdbSetting(storage, cwd)
	state := &serverState{}

	// The initializer dedupes the bring-up and rebuilds the server on a
	// restart; discovery itself is the rootBringUp below, since no marker
	// walk is wanted: one clangd at the workspace root serves every file.
	init := langext.NewInitializer(ctx, fs, editor, inst, langext.ProjectConfig{
		LanguageID: ccppLanguageID,
		FileMatch:  isCcppFile,
		Tools:      ccppToolNames,
		InitRoot: func(ctx context.Context, root langext.Root, tools *langext.Tools) error {
			state.starting()
			err := initializeClangd(ctx, fs, exec, notify, lsp, tools, cfg, compdb, state, root)
			if err != nil {
				state.failed(err)
			}
			return err
		},
	})
	bringUp := &rootBringUp{baseCtx: ctx, init: init, wsRoot: wsRoot}
	if err := editor.SubscribeEvents([]textapi.EventType{textapi.EventTypeOpen}, bringUp); err != nil {
		return fmt.Errorf("subscribe ccpp open events: %w", err)
	}

	gen := &compdbGenerator{
		fs:      fs,
		exec:    exec,
		notify:  notify,
		state:   state,
		restart: init.Reinitialize,
		bear: func(ctx context.Context) string {
			return resolveBear(ctx, notify, exec, init.Tools())
		},
	}
	offer := &compdbOffer{
		baseCtx:    ctx,
		fs:         fs,
		cfg:        cfg,
		notify:     notify,
		prompter:   prompter,
		compdb:     compdb,
		gen:        gen,
		wsDir:      wsRoot.Dir,
		considered: map[string]bool{},
	}
	if err := editor.SubscribeEvents([]textapi.EventType{textapi.EventTypeOpen}, offer); err != nil {
		return fmt.Errorf("subscribe ccpp compile_commands.json offer: %w", err)
	}

	focus := &focusTracker{wsDir: wsRoot.Dir}
	if err := editor.SubscribeEvents([]textapi.EventType{textapi.EventTypeFocus}, focus); err != nil {
		return fmt.Errorf("subscribe ccpp focus events: %w", err)
	}
	manual, handler := newCcppHandler(ccppHandlerConfig{
		exec:   exec,
		fs:     fs,
		cfg:    cfg,
		notify: notify,
		wsRoot: wsRoot,
		focus:  focus,
		compdb: compdb,
		state:  state,
		gen:    gen,
		clangd: func(ctx context.Context) string {
			return resolveClangd(ctx, cfg, notify, fs, exec, init.Tools())
		},
		restart: init.Reinitialize,
	})
	if err := registerREPL(manual, handler); err != nil {
		return fmt.Errorf("register ccpp command: %w", err)
	}

	// The `c` and `cpp` command-prompt handlers expose clangd's code
	// actions and custom requests. Their selection subscription and
	// lifetime are workspace-scoped, so they register once up front.
	sel := lspcmd.NewSelectionTracker()
	evs := []textapi.EventType{textapi.EventTypeSelection, textapi.EventTypeCursor}
	if err := editor.SubscribeEvents(evs, sel); err != nil {
		return fmt.Errorf("subscribe selection events: %w", err)
	}
	deps := actionDeps{lsp: lsp, editor: editor, wm: wm, notify: notify, opener: opener, sel: sel}
	for _, name := range []string{cActionCmdName, cppActionCmdName} {
		actionManual, actionHandler := newActionHandler(name, deps)
		if err := registerCommand(actionManual, actionHandler); err != nil {
			return fmt.Errorf("register %s action command: %w", name, err)
		}
	}
	return nil
}

func initializeClangd(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	lsp semanticapi.LSP,
	tools *langext.Tools,
	cfg config.Config,
	compdb *compdbSetting,
	state *serverState,
	root langext.Root,
) error {
	bin := resolveClangd(ctx, cfg, notify, fs, exec, tools)
	if bin == "" {
		return errors.New("no clangd found")
	}
	dir, err := compdb.effectiveDir(ctx, cfg, notify)
	if err != nil {
		slog.Warn("ccpp: read compilation database override", "error", err)
	}
	opts := clangdOptions{
		bin:                bin,
		args:               readArgs(cfg, notify),
		compileCommandsDir: dir,
		fallbackFlags:      readFallbackFlags(cfg, notify),
		logLevel:           clangdLogLevel(cfg, notify),
	}
	params, err := clangdInitializeParams(root.URI, filepath.Base(root.Dir), opts)
	if err != nil {
		return fmt.Errorf("build init params: %w", err)
	}
	if _, err := lsp.Initialize(ctx, params); err != nil {
		return fmt.Errorf("initialize ccpp lsp: %w", err)
	}
	state.running(opts)
	slog.Info("ccpp lsp initialized", "root", root.Dir, "command", clangdCommand(opts),
		"compile-commands-dir", dir)
	return nil
}

func isCcppFile(uri workspaceapi.URI) bool {
	return slices.Contains(ccppExtensions, strings.ToLower(filepath.Ext(uri.Path())))
}

// rootBringUp starts clangd at the workspace root when the first C or
// C++ file of the workspace opens. clangd finds each file's compilation
// database itself and serves every project under its root, so there is
// no project discovery to do, and a workspace without C or C++ never
// starts a server.
type rootBringUp struct {
	// baseCtx outlives the per-event context, which the editor cancels
	// as soon as Handle returns.
	baseCtx context.Context
	init    *langext.Initializer
	wsRoot  langext.Root
}

var _ textapi.EventHandler = (*rootBringUp)(nil)

func (b *rootBringUp) Handle(_ context.Context, ev textapi.Event) bool {
	if ev.Type != textapi.EventTypeOpen || !isCcppFile(ev.URI) || !insideDir(b.wsRoot.Dir, ev.URI.Path()) {
		return false
	}
	go debug.CapturePanicReport(func() {
		if err := b.init.InitializeAt(b.baseCtx, b.wsRoot); err != nil {
			slog.Warn("ccpp workspace root bring-up failed", "root", b.wsRoot.Dir, "error", err)
		}
	})
	return false
}

// focusTracker remembers the workspace file focused last, which `ccpp
// status` and `ccpp compdb` report on when no path is named. Moving the
// focus to the console does not clear it.
type focusTracker struct {
	wsDir string

	mu   sync.Mutex
	file string
}

var _ textapi.EventHandler = (*focusTracker)(nil)

func (f *focusTracker) Handle(_ context.Context, ev textapi.Event) bool {
	if ev.Type != textapi.EventTypeFocus {
		return false
	}
	path := ev.URI.Path()
	if !insideDir(f.wsDir, path) {
		return false
	}
	f.mu.Lock()
	f.file = path
	f.mu.Unlock()
	return false
}

// lastFile returns the path of the workspace file focused last, or ""
// when none has been.
func (f *focusTracker) lastFile() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.file
}

// serverState is what the extension knows of its clangd, for `ccpp
// status`: the host owns the process, so this is the bring-up's view.
type serverState struct {
	mu    sync.Mutex
	phase string
	opts  clangdOptions
	err   error
}

func (s *serverState) starting() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase, s.err = "starting", nil
}

func (s *serverState) running(opts clangdOptions) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase, s.opts, s.err = "running", opts, nil
}

func (s *serverState) failed(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase, s.err = "failed", err
}

// snapshot returns the phase ("" before any bring-up), the options of
// the last successful bring-up and the error of the last failed one.
func (s *serverState) snapshot() (string, clangdOptions, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase, s.opts, s.err
}

// insideDir reports whether path is dir or below it.
func insideDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
