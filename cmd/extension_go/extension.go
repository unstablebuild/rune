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
	"os"
	"path/filepath"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/extension/langext"
)

// goMarkers are the project-root markers that drive nested discovery: an
// opened .go file initializes a server rooted at its nearest module.
var goMarkers = []string{"go.mod", "go.sum", "go.work"}

// NewExtension returns the Go extension and its metadata.
func NewExtension() (extensionapi.WorkspaceExtension, extensionapi.Metadata) {
	ext := &goExtension{}
	meta := extensionapi.Metadata{
		DeveloperID:    "Unstable Build",
		DeveloperEmail: "it@unstable.build",
		DeveloperKey:   "064D4ABCFA6D9338",
		ExtensionID:    "go",
		ExtensionName:  "Go Language Extension",
		Permissions: extensionapi.NewPermissions(
			extensionapi.PermissionLSP,
			extensionapi.PermissionEditor,
			extensionapi.PermissionCommands,
			extensionapi.PermissionConfig,
			extensionapi.PermissionBrowserWindowManager,
			extensionapi.PermissionNotifications,
			extensionapi.PermissionInterrupt,
			extensionapi.PermissionExecute,
			extensionapi.PermissionFileSystem,
			extensionapi.PermissionBrowserResourceOpener,
			extensionapi.PermissionSyntaxTree,
			extensionapi.PermissionStorage,
			extensionapi.PermissionPackages,
		),
	}
	return ext, meta
}

type goExtension struct{}

func (e *goExtension) ExtendWorkspace(
	ctx context.Context, w *extensionapi.Workspace, cfg config.Config,
) error {
	return e.extendWorkspaceWith(ctx,
		w.FileSystem(ctx),
		w.Executor(ctx),
		w.Notifications(ctx),
		w.LSP(ctx),
		w.Editor(ctx),
		w.WindowManager(ctx),
		w.Parser(ctx),
		w.Interrupter(ctx),
		w.Storage(ctx),
		w,
		cfg,
		w.RegisterCommand,
	)
}

// extendWorkspaceWith wires Go project discovery to per-root gopls
// bring-up. It registers the `go` command once for the workspace
// (independent of any project root), subscribes for opened .go files so
// a server is initialized rooted at each file's nearest module, and
// eagerly initializes the workspace-root module when one is present. The
// dependencies are passed positionally so the compiler flags a missing
// one at every call site.
func (e *goExtension) extendWorkspaceWith(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	lsp semanticapi.LSP,
	editor textapi.Editor,
	wm browserapi.WindowManager,
	parser syntaxapi.Parser,
	interrupter term.Interrupter,
	storage storageapi.Service,
	inst langext.Installer,
	cfg config.Config,
	registerCommand func(textapi.CommandManual, textapi.CommandHandler) error,
) error {
	cwd, err := fs.URI(".")
	if err != nil {
		return fmt.Errorf("resolve cwd uri: %w", err)
	}
	scheme := cwd.Scheme()

	// The `go` command's selection/cursor subscriptions and cwd are
	// workspace-scoped, not project-scoped, so register it once up front
	// regardless of whether a module is ever discovered.
	manual, handler, err := newGoHandler(
		lsp, editor, wm, notify, parser, exec, interrupter, fs, cfg, storage)
	if err != nil {
		return fmt.Errorf("create handler: %w", err)
	}
	if err := registerCommand(manual, handler); err != nil {
		return fmt.Errorf("register command: %w", err)
	}

	init := langext.NewInitializer(ctx, fs, editor, inst, langext.ProjectConfig{
		LanguageID: "go",
		Markers:    goMarkers,
		FileMatch:  isGoFile,
		Tools:      []string{"gopls"},
		InitRoot: func(ctx context.Context, root langext.Root, tools *langext.Tools) error {
			return initializeGoRoot(ctx, fs, exec, notify, lsp, tools, scheme, cfg, root)
		},
	})
	if err := init.Start(); err != nil {
		return fmt.Errorf("subscribe go open events: %w", err)
	}

	// Preserve today's startup behavior: if the workspace root is itself
	// a Go module, bring it up immediately rather than waiting for the
	// first open. Discovery still drives nested modules.
	if hasGoProjectFiles(ctx, fs) {
		root := langext.Root{Dir: cwd.Path(), URI: fmt.Sprintf("file://%s", cwd.Path())}
		if err := init.InitializeAt(ctx, root); err != nil {
			return err
		}
	}
	return nil
}

// initializeGoRoot performs the gopls bring-up for a discovered module
// root: it resolves the gopls binary for the host and initializes the
// language server with the nested root URI.
func initializeGoRoot(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	lsp semanticapi.LSP,
	tools *langext.Tools,
	scheme string,
	cfg config.Config,
	root langext.Root,
) error {
	dbg := readGoplsDebugOptions(cfg)
	goplsBin := resolveGoplsForRoot(ctx, fs, exec, tools, cfg, notify, scheme)
	params, err := goplsInitializeParams(root.URI, dbg, goplsBin)
	if err != nil {
		return fmt.Errorf("build init params: %w", err)
	}
	if _, err := lsp.Initialize(ctx, params); err != nil {
		return fmt.Errorf("initialize gopls: %w", err)
	}
	slog.Info("gopls initialized",
		"root", root.Dir,
		"rpc_trace", dbg.RPCTrace,
		"logfile", dbg.LogFile,
		"debug_addr", dbg.DebugAddr,
		"trace", string(dbg.Trace),
	)
	return nil
}

// isGoFile reports whether uri names a Go source file.
func isGoFile(uri workspaceapi.URI) bool {
	return strings.HasSuffix(uri.Path(), ".go")
}

func readGoplsDebugOptions(cfg config.Config) goplsDebugOptions {
	var opts goplsDebugOptions
	if cfg == nil {
		return opts
	}
	dbg, err := cfg.GetConfig("debug")
	if err != nil || dbg == nil {
		return opts
	}
	if v, err := dbg.GetBool("rpc_trace"); err == nil {
		opts.RPCTrace = v
	}
	if v, err := dbg.GetString("logfile"); err == nil {
		resolved, rerr := resolveLogFile(v)
		if rerr != nil {
			slog.Warn("gopls debug logfile disabled",
				"logfile", v, "error", rerr)
		} else {
			opts.LogFile = resolved
		}
	}
	if v, err := dbg.GetString("addr"); err == nil {
		opts.DebugAddr = v
	}
	if v, err := dbg.GetString("trace"); err == nil {
		switch semanticapi.TraceValue(v) {
		case semanticapi.TraceValueOff,
			semanticapi.TraceValueMessages,
			semanticapi.TraceValueVerbose:
			opts.Trace = semanticapi.TraceValue(v)
		}
	}
	if v, err := dbg.GetString("log_level"); err == nil {
		switch v {
		case "info", "debug", "trace":
			opts.LogLevel = v
		}
	}
	return opts
}

func resolveLogFile(path string) (string, error) {
	if path == "" || path == "auto" {
		return path, nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create logfile dir: %w", err)
	}
	return path, nil
}

func readGoplsLspPath(cfg config.Config, notify browserapi.Notifications) (string, bool) {
	if cfg == nil {
		return "", false
	}
	v, err := cfg.GetString("lsp_path")
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return "", false
		}
		if notify != nil {
			_, _ = notify.Notify(browserapi.LevelWarn,
				"extensions.go.config.lsp_path must be a string: %v", err)
		}
		return "", false
	}
	return v, v != ""
}
