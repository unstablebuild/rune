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
	"fmt"
	"log/slog"
	"os"
	"path"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// rustMarkers are the project-root markers that drive nested discovery.
// A stray .rs file with no enclosing Cargo.toml does not spawn a server;
// the bare-.rs fallback stays a workspace-root concern (see
// detectRustProject).
var rustMarkers = []string{"Cargo.toml"}

// NewExtension returns the Rust extension and its metadata.
func NewExtension() (extensionapi.WorkspaceExtension, extensionapi.Metadata) {
	ext := &rustExtension{}
	meta := extensionapi.Metadata{
		DeveloperID:    "Unstable Build",
		DeveloperEmail: "it@unstable.build",
		DeveloperKey:   "064D4ABCFA6D9338",
		ExtensionID:    "rust",
		ExtensionName:  "Rust Language Extension",
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
			extensionapi.PermissionSyntaxTree,
			extensionapi.PermissionDebugger,
			extensionapi.PermissionPackages,
		),
	}
	return ext, meta
}

type rustExtension struct{}

func (e *rustExtension) ExtendWorkspace(
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
		w.Parser(ctx),
		w.Interrupter(ctx),
		w,
		os.Getenv("RUSTUP_HOME"),
		os.Getenv("CARGO_HOME"),
		cfg,
		w.RegisterREPLCommand,
		w.RegisterCommand,
	)
}

func (e *rustExtension) extendWorkspaceWith(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	lsp semanticapi.LSP,
	editor textapi.Editor,
	wm browserapi.WindowManager,
	opener browserapi.ResourceOpener,
	parser syntaxapi.Parser,
	interrupt term.Interrupter,
	inst langext.Installer,
	rustupHome, cargoHome string,
	cfg config.Config,
	registerREPL func(textapi.CommandManual, textapi.REPLHandler) error,
	registerCommand func(textapi.CommandManual, textapi.CommandHandler) error,
) error {
	cwd, err := fs.URI(".")
	if err != nil {
		return fmt.Errorf("resolve cwd uri: %w", err)
	}

	experimental := readExperimental(cfg, notify)
	memoryUsage := readMemoryUsage(cfg, notify)

	init := langext.NewInitializer(ctx, fs, editor, inst, langext.ProjectConfig{
		LanguageID: "rust",
		Markers:    rustMarkers,
		FileMatch:  isRustFile,
		Tools:      []string{"rust-analyzer", "rustup-init"},
		InitRoot: func(ctx context.Context, root langext.Root, tools *langext.Tools) error {
			return initializeRustRoot(ctx,
				fs, exec, notify, lsp, tools, rustupHome, cargoHome,
				cfg, experimental, root)
		},
	})
	if err := init.Start(); err != nil {
		return fmt.Errorf("subscribe rust open events: %w", err)
	}

	// The REPL command's cwd is always the workspace root, independent of
	// any nested project, so register it once up front. A reload (or a
	// toolchain-mutating subcommand) must rebuild every server brought up
	// so far, which Reinitialize does across all discovered roots.
	reload := func(ctx context.Context) error {
		return init.Reinitialize(ctx)
	}
	manual, handler := newRustHandler(
		exec, notify, cwd.Path(), resolveRustupProxy(cargoHome), reload)
	if err := registerREPL(manual, handler); err != nil {
		return fmt.Errorf("register rust command: %w", err)
	}

	// The `rust` command-prompt handler exposes rust-analyzer's assists.
	// Its selection/cursor subscriptions and lifetime are workspace-scoped,
	// not project-scoped, so register it once up front regardless of
	// whether a Cargo project is ever discovered.
	sel := lspcmd.NewSelectionTracker()
	evs := []textapi.EventType{textapi.EventTypeSelection, textapi.EventTypeCursor}
	if err := editor.SubscribeEvents(evs, sel); err != nil {
		return fmt.Errorf("subscribe selection events: %w", err)
	}
	actionManual, actionHandler := newRustActionHandler(
		lsp, editor, wm, notify, opener, sel, exec, fs, parser, interrupt,
		cwd.Path(), experimental, memoryUsage)
	if err := registerCommand(actionManual, actionHandler); err != nil {
		return fmt.Errorf("register rust action command: %w", err)
	}

	// Preserve the eager workspace-root behavior: if the workspace root
	// is itself a Rust project (including the bare-.rs fallback), bring
	// it up immediately rather than waiting for the first open. Nested
	// discovery still requires a Cargo.toml.
	if detectRustProject(ctx, fs) {
		root := langext.Root{Dir: cwd.Path(), URI: fmt.Sprintf("file://%s", cwd.Path())}
		if err := init.InitializeAt(ctx, root); err != nil {
			return err
		}
	}
	return nil
}

func initializeRustRoot(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	lsp semanticapi.LSP,
	tools *langext.Tools,
	rustupHome, cargoHome string,
	cfg config.Config,
	experimental bool,
	root langext.Root,
) error {
	// Toolchain setup is best-effort: rust-analyzer is still brought up on
	// any failure. Without CARGO_HOME we cannot place the toolchain or
	// resolve its proxies, so skip the install entirely rather than let
	// rustup-init land it in the wrong place, and skip the sysroot probe.
	var sysroot string
	if cargoHome == "" {
		_, _ = notify.Notify(browserapi.LevelWarn,
			"CARGO_HOME is not set; continuing without a managed Rust toolchain")
		slog.Warn("rust toolchain setup skipped: CARGO_HOME not set", "root", root.Dir)
	} else {
		if err := bootstrapRustup(
			ctx, tools, exec, notify, fs, rustupHome, root.Dir,
		); err != nil {
			_, _ = notify.Notify(browserapi.LevelWarn,
				"Rust toolchain setup failed, continuing without a managed toolchain: %v", err)
			slog.Warn("rust toolchain setup failed", "root", root.Dir, "error", err)
		}
		sysroot = resolveSysroot(ctx, exec, resolveRustcProxy(cargoHome))
	}

	command, err := resolveRustAnalyzer(ctx, cfg, notify, tools)
	if err != nil {
		return fmt.Errorf("find rust-analyzer: %w", err)
	}
	params, err := rustInitializeParams(
		root.URI, command, sysroot, rustLogFilter(cfg, notify), experimental)
	if err != nil {
		return fmt.Errorf("build init params: %w", err)
	}
	if _, err := lsp.Initialize(ctx, params); err != nil {
		return fmt.Errorf("initialize rust lsp: %w", err)
	}
	slog.Info("rust lsp initialized", "root", root.Dir, "command", command)
	return nil
}

func rustLogFilter(cfg config.Config, notify browserapi.Notifications) string {
	const defaultFilter = "info"
	if cfg == nil {
		return defaultFilter
	}
	debug, err := cfg.GetConfig("debug")
	if err != nil || debug == nil {
		return defaultFilter
	}
	filter, err := debug.GetString("log_level")
	if err != nil || filter == "" {
		return defaultFilter
	}
	if strings.ContainsAny(filter, "\x00\r\n") {
		if notify != nil {
			_, _ = notify.Notify(browserapi.LevelWarn,
				"extensions.rust.config.debug.log_level contains invalid characters")
		}
		return defaultFilter
	}
	return filter
}

func isRustFile(uri workspaceapi.URI) bool {
	return strings.HasSuffix(uri.Path(), ".rs")
}

func resolveRustupProxy(cargoHome string) string {
	if cargoHome == "" {
		return ""
	}
	return path.Join(cargoHome, "bin", "rustup")
}

func resolveRustcProxy(cargoHome string) string {
	return path.Join(cargoHome, "bin", "rustc")
}
