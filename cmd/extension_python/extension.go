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

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/extension_python/pyshim"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/langext"
)

// NewExtension returns the Python extension and its metadata.
func NewExtension() (extensionapi.WorkspaceExtension, extensionapi.Metadata) {
	ext := &pyExtension{}
	meta := extensionapi.Metadata{
		DeveloperID:    "Unstable Build",
		DeveloperEmail: "it@unstable.build",
		DeveloperKey:   "064D4ABCFA6D9338",
		ExtensionID:    "python",
		ExtensionName:  "Python Language Extension",
		Permissions: extensionapi.NewPermissions(
			extensionapi.PermissionLSP,
			extensionapi.PermissionEditor,
			extensionapi.PermissionCommands,
			extensionapi.PermissionConfig,
			extensionapi.PermissionNotifications,
			extensionapi.PermissionExecute,
			extensionapi.PermissionFileSystem,
			extensionapi.PermissionSyntaxTree,
			extensionapi.PermissionStorage,
			extensionapi.PermissionBrowserWindowManager,
			extensionapi.PermissionPackages,
		),
	}
	return ext, meta
}

type pyExtension struct{}

func (e *pyExtension) ExtendWorkspace(
	ctx context.Context, w *extensionapi.Workspace, cfg config.Config,
) error {
	return e.extendWorkspaceWith(ctx,
		w.FileSystem(ctx),
		w.Executor(ctx),
		w.Notifications(ctx),
		w.LSP(ctx),
		w.Editor(ctx),
		w,
		cfg,
		hostDataDir(ctx, w),
		w.Storage(ctx),
		w.WindowManager(ctx),
		w.RegisterREPLCommand,
	)
}

type installRoot interface {
	FindInstalledResource(ctx context.Context, relpath string) (string, error)
}

func hostDataDir(ctx context.Context, root installRoot) string {
	dir, err := root.FindInstalledResource(ctx, ".")
	if err != nil {
		slog.Warn("resolve data directory on the workspace host; python shims disabled",
			"error", err)
		return ""
	}
	return dir
}

func (e *pyExtension) extendWorkspaceWith(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	lsp semanticapi.LSP,
	editor textapi.Editor,
	inst langext.Installer,
	cfg config.Config,
	dataDir string,
	storage storageapi.Service,
	wm browserapi.WindowManager,
	registerREPL func(textapi.CommandManual, textapi.REPLHandler) error,
) error {
	// The REPL command's cwd is always the workspace root, independent of
	// any nested project, so register it once up front regardless of
	// whether a project root is ever discovered.
	cwd, err := fs.URI(".")
	if err != nil {
		return fmt.Errorf("resolve cwd uri: %w", err)
	}
	setting := newEnvSetting(storage)
	init := langext.NewInitializer(ctx, fs, editor, inst, langext.ProjectConfig{
		LanguageID:  "python",
		Markers:     pyMarkers,
		FileMatch:   isPythonFile,
		WatchEvents: pyWatchEvents(cfg, notify),
		Tools:       []string{"uv", "uvx", "ty", "ruff"},
		InitRoot: func(ctx context.Context, root langext.Root, tools *langext.Tools) error {
			return initializeProjectRoot(
				ctx, fs, exec, notify, lsp, tools, cfg, dataDir, setting, wm, root)
		},
	})
	syncEnv := func(ctx context.Context, root langext.Root) error {
		return setupManagedEnvironment(ctx, fs, exec, notify, init.Tools(), cfg, dataDir, root)
	}
	manual, handler := newPyHandler(pyHandlerConfig{
		exec: exec, notify: notify, fs: fs,
		setting: setting, syncEnv: syncEnv, wsRoot: cwd,
	})
	if err := registerREPL(manual, handler); err != nil {
		return fmt.Errorf("register python command: %w", err)
	}

	if err := init.Start(); err != nil {
		return fmt.Errorf("subscribe python open events: %w", err)
	}

	// Preserve the eager workspace-root behavior: if the workspace root
	// is itself a Python project, bring it up immediately rather than
	// waiting for the first open. Discovery still drives nested projects.
	if detectProjectAt(ctx, fs, ".") != kindNone {
		root := langext.Root{Dir: cwd.Path(), URI: fmt.Sprintf("file://%s", cwd.Path())}
		// An undecided root has to ask the user first, and
		// ExtendWorkspace must not block on that answer.
		if _, known, err := setting.get(ctx, root); err == nil && !known {
			go debug.CapturePanicReport(func() {
				slog.Info("initializing language project at known root",
					"dir", root.Dir, "uri", root.URI)
				if err := init.InitializeAt(ctx, root); err != nil {
					slog.Warn("python workspace root bring-up failed",
						"root", root.Dir, "error", err)
				}
			})
			return nil
		} else {
			slog.Info("initializing language project at root",
				"dir", root.Dir, "uri", root.URI)
		}
		if err := init.InitializeAt(ctx, root); err != nil {
			return err
		}
	} else {
		slog.Info("no python projects found at root")
	}
	return nil
}

func initializeProjectRoot(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	lsp semanticapi.LSP,
	tools *langext.Tools,
	cfg config.Config,
	dataDir string,
	setting *envSetting,
	wm browserapi.WindowManager,
	root langext.Root,
) error {
	if detectProjectAt(ctx, fs, root.Dir) != kindScript {
		managed := managedEnvironmentAllowed(ctx, notify, setting, wm, root)
		slog.Warn("initializing project root", "managed",
			managed, "root", root.Dir, "uri", root.URI)
		if managed {
			if err := setupManagedEnvironment(
				ctx, fs, exec, notify, tools, cfg, dataDir, root); err != nil {
				_, _ = notify.Notify(browserapi.LevelWarn,
					"Python environment setup failed, continuing without a synced env: %v", err)
				slog.Warn("python env setup failed", "root", root.Dir, "error", err)
			}
		} else {
			_, _ = notify.NotifyOnce(browserapi.LevelInfo,
				"Rune is not managing the Python environment in %s. "+
					"Run `python enable` to let it.", root.Dir)
		}
	}

	logLevel := pyLogLevel(cfg, notify)
	command, alternates := readPyOverrides(cfg, notify)
	if command == "" {
		command = pyCommand(findTool(ctx, tools, notify, "ty"), "ty", "server")
		if alternates == nil {
			ruff := pyRuffCommand(findTool(ctx, tools, notify, "ruff"), logLevel)
			alternates = map[string]string{
				"textDocument/formatting":      ruff,
				"textDocument/rangeFormatting": ruff,
			}
		}
	}

	diagnosticMode := pyDiagnosticMode(cfg, notify)
	params, err := pyInitializeParams(
		root.URI, command, alternates, diagnosticMode, logLevel)
	if err != nil {
		return fmt.Errorf("build init params: %w", err)
	}
	if _, err := lsp.Initialize(ctx, params); err != nil {
		return fmt.Errorf("initialize python lsp: %w", err)
	}
	slog.Info("python lsp initialized", "root", root.Dir, "command", command)
	return nil
}

func findTool(
	ctx context.Context, tools *langext.Tools, notify browserapi.Notifications, name string,
) string {
	bin, err := tools.Find(ctx, name)
	if err != nil && !errors.Is(err, pkgapi.ErrNotInstalled) {
		_, _ = notify.NotifyOnce(browserapi.LevelWarn,
			"Could not find %s in the python package: %v. Using the %s on PATH instead.",
			name, err, name)
	}
	return bin
}

func managedEnvironmentAllowed(
	ctx context.Context,
	notify browserapi.Notifications,
	setting *envSetting,
	wm browserapi.WindowManager,
	root langext.Root,
) bool {
	managed, known, err := setting.get(ctx, root)
	if err != nil {
		slog.Warn("python env policy read failed", "root", root.Dir, "error", err)
		return false
	}
	if known {
		return managed
	}
	answer, answered := askManageEnvironment(ctx, wm, root)
	if !answered {
		return false
	}
	if err := setting.set(ctx, root, answer); err != nil {
		_, _ = notify.Notify(browserapi.LevelWarn,
			"Could not remember the Python environment choice for %s: %v", root.Dir, err)
		slog.Warn("python env policy write failed", "root", root.Dir, "error", err)
	}
	return answer
}

func setupManagedEnvironment(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	tools *langext.Tools,
	cfg config.Config,
	dataDir string,
	root langext.Root,
) error {
	kind := detectProjectAt(ctx, fs, root.Dir)
	uv := findTool(ctx, tools, notify, "uv")
	envErr := ensureEnvironment(ctx, uv, exec, notify, kind, fs, root.Dir, dataDir)

	if dataDir != "" {
		if err := pyshim.Write(fs, dataDir); err != nil {
			slog.Warn("python shim install failed", "dataDir", dataDir, "error", err)
		}
	}

	if pin := debugpyPin(cfg, notify); pin != "" {
		uvx := findTool(ctx, tools, notify, "uvx")
		go debug.CapturePanicReport(func() {
			prewarmDebugpy(ctx, uvx, exec, root.Dir, pin)
		})
	}
	return envErr
}

func pyLogLevel(cfg config.Config, notify browserapi.Notifications) string {
	if cfg == nil {
		return ""
	}
	debug, err := cfg.GetConfig("debug")
	if err != nil || debug == nil {
		return ""
	}
	level, err := debug.GetString("log_level")
	if err != nil {
		return ""
	}
	switch level {
	case "trace", "debug", "info", "warn", "error":
		return level
	default:
		if notify != nil {
			_, _ = notify.Notify(browserapi.LevelWarn,
				"extensions.python.config.debug.log_level must be one of "+
					"\"trace\", \"debug\", \"info\", \"warn\", \"error\"; got %q", level)
		}
		return ""
	}
}

func debugpyPin(cfg config.Config, notify browserapi.Notifications) string {
	if cfg == nil {
		return ""
	}
	pin, err := cfg.GetString("debugpy")
	switch {
	case errors.Is(err, config.ErrNotFound):
		return ""
	case err != nil:
		_, _ = notify.Notify(browserapi.LevelWarn,
			"extensions.python.config.debugpy must be a string: %v", err)
		return ""
	}
	return pin
}

func prewarmDebugpy(
	ctx context.Context,
	uvxBin string,
	exec workspaceapi.Executor,
	dir, pin string,
) {
	if uvxBin == "" {
		uvxBin = "uvx"
	}
	if err := runUV(ctx, uvxBin, exec, dir, "--from", "debugpy=="+pin, "python", "-c", ""); err != nil {
		slog.Warn("debugpy prewarm failed", "pin", pin, "error", err)
	}
}

func readPyOverrides(
	cfg config.Config, notify browserapi.Notifications,
) (command string, alternates map[string]string) {
	if cfg == nil {
		return "", nil
	}

	override, err := cfg.GetString("command")
	switch {
	case err == nil && override != "":
		command = override
	case err != nil && !errors.Is(err, config.ErrNotFound):
		_, _ = notify.Notify(browserapi.LevelWarn,
			"extensions.python.config.command must be a string: %v", err)
	}

	raw, err := cfg.GetMap("alternate_commands")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			_, _ = notify.Notify(browserapi.LevelWarn,
				"extensions.python.config.alternate_commands must be a map: %v", err)
		}
		return command, alternates
	}
	parsed := make(map[string]string, len(raw))
	for k, v := range raw {
		s, ok := v.(string)
		if !ok {
			_, _ = notify.Notify(browserapi.LevelWarn,
				"extensions.python.config.alternate_commands.%s must be a string", k)
			continue
		}
		parsed[k] = s
	}
	if len(parsed) > 0 {
		alternates = parsed
	}
	return command, alternates
}

func pyWatchEvents(cfg config.Config, notify browserapi.Notifications) []textapi.EventType {
	openOnly := []textapi.EventType{textapi.EventTypeOpen}
	onChange := []textapi.EventType{
		textapi.EventTypeOpen, textapi.EventTypeChange, textapi.EventTypeCreate,
	}
	enabled, err := cfg.GetBool("watch_events")
	switch {
	case errors.Is(err, config.ErrNotFound):
		return onChange
	case err != nil:
		_, _ = notify.Notify(browserapi.LevelWarn,
			"extensions.python.config.watch_events must be a bool: %v", err)
		return onChange
	case !enabled:
		return openOnly
	default:
		return onChange
	}
}

func pyDiagnosticMode(cfg config.Config, notify browserapi.Notifications) string {
	if cfg == nil {
		return ""
	}
	mode, err := cfg.GetString("diagnostic_mode")
	switch {
	case errors.Is(err, config.ErrNotFound):
		return ""
	case err != nil:
		_, _ = notify.Notify(browserapi.LevelWarn,
			"extensions.python.config.diagnostic_mode must be a string: %v", err)
		return ""
	}
	switch mode {
	case "", "off", "openFilesOnly", "workspace":
		return mode
	default:
		_, _ = notify.Notify(browserapi.LevelWarn,
			"extensions.python.config.diagnostic_mode must be one of "+
				"\"off\", \"openFilesOnly\", \"workspace\"; got %q", mode)
		return ""
	}
}
