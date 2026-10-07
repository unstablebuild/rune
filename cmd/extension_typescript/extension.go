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

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// tsLanguageID keys the one tsgo server per root that serves every
// TypeScript and JavaScript dialect, and names the Rune package.
const tsLanguageID = "typescript"

var tsMarkers = []string{"tsconfig.json", "jsconfig.json", "package.json"}

var tsExtensions = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

// NewExtension returns the TypeScript extension and its metadata.
func NewExtension() (extensionapi.WorkspaceExtension, extensionapi.Metadata) {
	ext := &tsExtension{}
	meta := extensionapi.Metadata{
		DeveloperID:    "Unstable Build",
		DeveloperEmail: "it@unstable.build",
		DeveloperKey:   "064D4ABCFA6D9338",
		ExtensionID:    "typescript",
		ExtensionName:  "TypeScript Language Extension",
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
			extensionapi.PermissionPackages,
		),
	}
	return ext, meta
}

type tsExtension struct{}

func (e *tsExtension) ExtendWorkspace(
	ctx context.Context, w *extensionapi.Workspace, cfg config.Config,
) error {
	return e.extendWorkspaceWith(ctx,
		w.FileSystem(ctx),
		w.Executor(ctx),
		w.Notifications(ctx),
		w.LSP(ctx),
		w.Editor(ctx),
		w.WindowManager(ctx),
		w.ResourceOpener(ctx),
		w,
		cfg,
		w.RegisterREPLCommand,
		w.RegisterCommand,
	)
}

func (e *tsExtension) extendWorkspaceWith(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	lsp semanticapi.LSP,
	editor textapi.Editor,
	wm browserapi.WindowManager,
	opener browserapi.ResourceOpener,
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

	init := langext.NewInitializer(ctx, fs, editor, inst, langext.ProjectConfig{
		LanguageID: tsLanguageID,
		Markers:    tsMarkers,
		FileMatch:  isTSFile,
		Tools:      []string{"tsgo"},
		// One tsgo serves every tsconfig under its root, and only finds
		// references and renames across the packages it has loaded.
		Outermost: true,
		InitRoot: func(ctx context.Context, root langext.Root, tools *langext.Tools) error {
			return initializeTSRoot(ctx, fs, exec, notify, lsp, tools, cfg, wsRoot.Dir, root)
		},
	})
	if err := init.Start(); err != nil {
		return fmt.Errorf("subscribe typescript open events: %w", err)
	}
	fallback := &rootFallback{baseCtx: ctx, fs: fs, init: init, wsURI: cwd, wsRoot: wsRoot}
	if err := editor.SubscribeEvents([]textapi.EventType{textapi.EventTypeOpen}, fallback); err != nil {
		return fmt.Errorf("subscribe typescript fallback events: %w", err)
	}
	hint := newInferredProjectHint(ctx, lsp, notify, fs, cwd)
	if err := editor.SubscribeEvents([]textapi.EventType{textapi.EventTypeOpen}, hint); err != nil {
		return fmt.Errorf("subscribe typescript project hint events: %w", err)
	}

	// Both commands are workspace-scoped rather than project-scoped, so
	// they register once whether or not a project is ever discovered.
	focus := &focusTracker{wsDir: wsRoot.Dir}
	if err := editor.SubscribeEvents([]textapi.EventType{textapi.EventTypeFocus}, focus); err != nil {
		return fmt.Errorf("subscribe typescript focus events: %w", err)
	}
	manual, handler := newTSHandler(tsHandlerConfig{
		exec:  exec,
		fs:    fs,
		wsURI: cwd,
		focus: focus,
		server: func(ctx context.Context, rootDir string) string {
			return resolveServer(ctx, cfg, notify, fs, exec, init.Tools(), wsRoot.Dir, rootDir)
		},
		restart: init.Reinitialize,
	})
	if err := registerREPL(manual, handler); err != nil {
		return fmt.Errorf("register ts command: %w", err)
	}
	sel := lspcmd.NewSelectionTracker()
	evs := []textapi.EventType{textapi.EventTypeSelection, textapi.EventTypeCursor}
	if err := editor.SubscribeEvents(evs, sel); err != nil {
		return fmt.Errorf("subscribe selection events: %w", err)
	}
	actionManual, actionHandler := newTSActionHandler(lsp, editor, wm, notify, opener, sel,
		wsRoot.Dir, readDeveloper(cfg, notify))
	if err := registerCommand(actionManual, actionHandler); err != nil {
		return fmt.Errorf("register ts action command: %w", err)
	}

	if detectTSProject(fs) {
		return init.InitializeAt(ctx, wsRoot)
	}
	return nil
}

func initializeTSRoot(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	lsp semanticapi.LSP,
	tools *langext.Tools,
	cfg config.Config,
	wsDir string,
	root langext.Root,
) error {
	server := resolveServer(ctx, cfg, notify, fs, exec, tools, wsDir, root.Dir)
	if server == "" {
		return errors.New("no TypeScript 7 language server found")
	}
	params, err := tsInitializeParams(root.URI, server)
	if err != nil {
		return fmt.Errorf("build init params: %w", err)
	}
	if _, err := lsp.Initialize(ctx, params); err != nil {
		return fmt.Errorf("initialize typescript lsp: %w", err)
	}
	slog.Info("typescript lsp initialized", "root", root.Dir, "server", server)
	hintMissingDependencies(fs, notify, wsDir, root.Dir)
	hintOlderTypeScript(fs, notify, root.Dir)
	return nil
}

// hintOlderTypeScript warns, once, a project that installed a TypeScript
// older than 7: that compiler does not serve LSP, so tsgo checks the
// project with TypeScript 7, which removed options older releases accept.
func hintOlderTypeScript(fs workspaceapi.FileSystem, notify browserapi.Notifications, rootDir string) {
	version, _, ok := projectTypeScript(fs, rootDir)
	if !ok || isNativeVersion(version) {
		return
	}
	_, _ = notify.NotifyOnce(browserapi.LevelWarn,
		"%s installs TypeScript %s, but Rune's language server is TypeScript 7, which removed "+
			"tsconfig options older releases accept, such as baseUrl, target ES5 and "+
			"moduleResolution node. `%s check` names the ones to change.",
		rootDir, version, tsCommandName)
}

// hintMissingDependencies explains, once, why imports of a project's
// packages stay unresolved: the project declares dependencies that are
// not installed where node would resolve them from. The extension never
// installs them itself, since install scripts run arbitrary code.
func hintMissingDependencies(
	fs workspaceapi.FileSystem, notify browserapi.Notifications, wsDir, rootDir string,
) {
	pkg, err := readPackageJSON(fs, rootDir)
	if err != nil || !pkg.hasDependencies() {
		return
	}
	// Node resolves packages from every ancestor's node_modules, even
	// ones above the workspace, such as a monorepo's hoisted install.
	for dir := rootDir; ; dir = filepath.Dir(dir) {
		if info, err := fs.Stat(filepath.Join(dir, "node_modules")); err == nil && info.IsDir() {
			return
		}
		if dir == filepath.Dir(dir) {
			break
		}
	}
	install := fmt.Sprintf("`%s install`", tsCommandName)
	if rootDir != wsDir {
		install = fmt.Sprintf("`%s install` in %s", detectPackageManager(fs, rootDir, pkg), rootDir)
	}
	_, _ = notify.NotifyOnce(browserapi.LevelInfo,
		"The dependencies of %s are not installed, so imports from them are unresolved. Run %s.",
		rootDir, install)
}

func isTSFile(uri workspaceapi.URI) bool {
	return slices.Contains(tsExtensions, filepath.Ext(uri.Path()))
}

// detectTSProject reports whether the workspace root looks like a
// TypeScript or JavaScript project: a root marker or any top-level
// source file.
func detectTSProject(fs workspaceapi.FileSystem) bool {
	for _, marker := range tsMarkers {
		if info, err := fs.Stat(marker); err == nil && info != nil && !info.IsDir() {
			return true
		}
	}
	entries, err := fs.ReadDir(".")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && slices.Contains(tsExtensions, filepath.Ext(e.Name())) {
			return true
		}
	}
	return false
}

// rootFallback brings up the workspace root for a workspace file with
// no enclosing marker. tsgo infers a project for such a file, unlike
// servers that need a manifest, so a stray script still gets a server.
type rootFallback struct {
	// baseCtx outlives the per-event context, which the editor cancels
	// as soon as Handle returns.
	baseCtx context.Context
	fs      workspaceapi.FileSystem
	init    *langext.Initializer
	wsURI   workspaceapi.URI
	wsRoot  langext.Root
}

func (f *rootFallback) Handle(_ context.Context, ev textapi.Event) bool {
	if ev.Type != textapi.EventTypeOpen || !isTSFile(ev.URI) {
		return false
	}
	if !insideDir(f.wsRoot.Dir, ev.URI.Path()) {
		return false
	}
	if _, found := langext.FindProjectRoot(f.fs, f.wsURI, ev.URI, tsMarkers); found {
		return false
	}
	go debug.CapturePanicReport(func() {
		if err := f.init.InitializeAt(f.baseCtx, f.wsRoot); err != nil {
			slog.Warn("typescript workspace root bring-up failed",
				"root", f.wsRoot.Dir, "error", err)
		}
	})
	return false
}

// insideDir reports whether path is dir or below it.
func insideDir(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
