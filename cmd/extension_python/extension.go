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
		w.DataDir(ctx),
		w.Storage(ctx),
		w.WindowManager(ctx),
		w.RegisterREPLCommand,
	)
}

// extendWorkspaceWith wires Python project discovery to per-root language
// server bring-up. It registers the REPL command once for the workspace,
// subscribes for opened .py files so a server is initialized rooted at
// each file's nearest project, and eagerly initializes the workspace-root
// project when one is present. The dependencies are passed positionally
// so the compiler flags a missing one at every call site.
func (e *pyExtension) extendWorkspaceWith(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	lsp semanticapi.LSP,
	editor textapi.Editor,
	inst installer,
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
	syncEnv := func(ctx context.Context, root langext.Root) error {
		return setupManagedEnvironment(ctx, fs, exec, notify, inst, cfg, dataDir, root)
	}
	manual, handler := newPyHandler(pyHandlerConfig{
		exec: exec, notify: notify, fs: fs,
		setting: setting, syncEnv: syncEnv, wsRoot: cwd,
	})
	if err := registerREPL(manual, handler); err != nil {
		return fmt.Errorf("register python command: %w", err)
	}

	init := langext.NewInitializer(ctx, fs, editor, langext.ProjectConfig{
		LanguageID:  "python",
		Markers:     pyMarkers,
		FileMatch:   isPythonFile,
		WatchEvents: pyWatchEvents(cfg, notify),
		InitRoot: func(ctx context.Context, root langext.Root) error {
			return initializeProjectRoot(
				ctx, fs, exec, notify, lsp, inst, cfg, dataDir, setting, wm, root)
		},
	})
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

// initializeProjectRoot performs the language-specific bring-up for a
// discovered project root. The environment work (uv bootstrap, venv-aware
// shims, debugpy prewarm) only runs once the user has agreed to let Rune
// manage the root; ty and ruff are bundled tooling and come up either
// way. The decision must be made before Initialize, which rejects a
// second init for the same root.
//
// A loose-scripts root is never auto-managed: there is nothing to sync,
// so guessing would cost a prompt and an interpreter download for no
// gain. `python enable` still reaches it explicitly.
func initializeProjectRoot(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	lsp semanticapi.LSP,
	inst installer,
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
				ctx, fs, exec, notify, inst, cfg, dataDir, root); err != nil {
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

	tyBin := resolvePyTool(ctx, fs, exec, inst, "ty")
	ruffBin := resolvePyTool(ctx, fs, exec, inst, "ruff")
	logLevel := pyLogLevel(cfg, notify)
	command := pyCommand(tyBin, "ty", "server")
	alternates := map[string]string{
		"textDocument/formatting":      pyRuffCommand(ruffBin, logLevel),
		"textDocument/rangeFormatting": pyRuffCommand(ruffBin, logLevel),
	}
	command, alternates = applyPyConfig(cfg, notify, command, alternates)

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

// managedEnvironmentAllowed resolves the stored policy for root, asking
// the user when there is no stored answer. A dismissed prompt is not
// persisted, so the question is asked again next session.
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

// setupManagedEnvironment bootstraps the uv environment rooted at root,
// installs the venv-aware python shims and prewarms the debugpy adapter
// env. It is shared by bring-up and `python enable`.
func setupManagedEnvironment(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	inst installer,
	cfg config.Config,
	dataDir string,
	root langext.Root,
) error {
	kind := detectProjectAt(ctx, fs, root.Dir)
	uvBin := resolvePyTool(ctx, fs, exec, inst, "uv")
	envErr := ensureEnvironment(ctx, uvBin, exec, notify, kind, fs, root.Dir, dataDir)

	if dataDir != "" {
		if err := pyshim.Write(fs, dataDir); err != nil {
			slog.Warn("python shim install failed", "dataDir", dataDir, "error", err)
		}
	}

	if pin := debugpyPin(cfg, notify); pin != "" {
		uvxBin := resolvePyTool(ctx, fs, exec, inst, "uvx")
		go debug.CapturePanicReport(func() {
			prewarmDebugpy(ctx, uvxBin, exec, root.Dir, pin)
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

// debugpyPin reads the optional `debugpy` config key: the version pin
// used to prewarm the debug adapter's uvx environment right after the
// project env syncs, so the first debug launch works offline. Unset
// skips the prewarm; the pin must match the one in the package's
// debugger.python.command so the prewarmed env is the one the adapter
// resolves.
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

// prewarmDebugpy resolves the pinned debugpy into uvx's cached env by
// running a no-op python through it. Best-effort: a failure only means
// the first debug launch pays the resolution cost (or fails offline).
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

// applyPyConfig overrides the ty/ruff defaults with the optional
// top-level `command` and `alternate_commands` config keys, each applied
// independently. Overriding `command` without supplying
// `alternate_commands` drops the default ruff alternates, since they
// assume the ty+ruff split; supply `alternate_commands` to keep a
// multi-server setup. Invalid values warn and leave the default in place.
func applyPyConfig(
	cfg config.Config, notify browserapi.Notifications,
	command string, alternates map[string]string,
) (string, map[string]string) {
	if cfg == nil {
		return command, alternates
	}

	override, err := cfg.GetString("command")
	switch {
	case err == nil && override != "":
		command = override
		alternates = nil
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

// pyWatchEvents returns the editor events that drive project discovery,
// defaulting to opens plus out-of-band changes and creates. Setting the
// optional `watch_events` config key to false restores open-only
// behavior, an escape hatch for pathological monorepos.
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

// pyDiagnosticMode reads the optional `diagnostic_mode` config key that
// controls ty's diagnostic scope. It is opt-in: when unset, ty keeps
// its default "openFilesOnly" scope. Only ty's documented values are
// accepted ("off", "openFilesOnly", "workspace"); an unknown value
// warns and is ignored. "workspace" makes ty type-check the whole
// project and answer workspace/diagnostic pulls for unopened files, at
// the cost of a full-project scan per pull, so it is left to the user
// to enable per project.
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
