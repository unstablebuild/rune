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
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/extension_python/pyshim"
	"unstable.build/rune/internal/debug"
)

// projectKind classifies the Python project layout of the workspace
// root, which determines how the uv environment is bootstrapped.
type projectKind int

const (
	kindNone projectKind = iota
	kindProject
	kindRequirements
	kindVenvOnly
	kindScript
)

// requirementsFiles lists the requirements manifests probed in priority
// order; the first that exists drives `uv pip install`.
var requirementsFiles = []string{
	"requirements.txt",
	"requirements.lock",
	"requirements.in",
}

// pyMarkers are the project-root marker files and directories used to
// discover the nearest Python project enclosing an opened file. A stray
// `.py` with no enclosing marker does not spin up a server, so the
// `.py`-only kindScript fallback stays a root-level concern handled by
// the eager workspace-root path rather than nested discovery.
var pyMarkers = append([]string{"pyproject.toml"}, append(requirementsFiles, ".venv")...)

// isPythonFile reports whether uri names a Python source file.
func isPythonFile(uri workspaceapi.URI) bool {
	return strings.HasSuffix(uri.Path(), ".py")
}

// detectProjectAt classifies the Python project layout rooted at dir
// (relative to the workspace root) so a nested project's environment is
// bootstrapped the same way the workspace-root project is.
func detectProjectAt(_ context.Context, fs workspaceapi.FileSystem, dir string) projectKind {
	if info, err := fs.Stat(filepath.Join(dir, "pyproject.toml")); err == nil && info != nil && !info.IsDir() {
		return kindProject
	}
	for _, name := range requirementsFiles {
		if info, err := fs.Stat(filepath.Join(dir, name)); err == nil && info != nil && !info.IsDir() {
			return kindRequirements
		}
	}
	if info, err := fs.Stat(filepath.Join(dir, ".venv")); err == nil && info != nil && info.IsDir() {
		return kindVenvOnly
	}
	if entries, err := fs.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".py") {
				return kindScript
			}
		}
	}
	return kindNone
}

// firstRequirementsFile returns the highest-priority requirements
// manifest present in dir, falling back to the default name when none
// exists. The returned path is relative to dir so `uv pip install -r`
// resolves it inside the project root the command runs in.
func firstRequirementsFile(fs workspaceapi.FileSystem, dir string) string {
	for _, name := range requirementsFiles {
		if info, err := fs.Stat(filepath.Join(dir, name)); err == nil && info != nil && !info.IsDir() {
			return name
		}
	}
	return "requirements.txt"
}

// ensureEnvironment bootstraps the uv-managed environment for the
// detected project kind, reporting step-based progress through a single
// notification. It is best-effort: the caller continues to LSP bring-up
// even on error, since the language server is useful without a synced
// env.
func ensureEnvironment(
	ctx context.Context,
	uvBin string,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	kind projectKind,
	fs workspaceapi.FileSystem,
	dir string,
	dataDir string,
) error {
	notifID, _ := notify.Notify(browserapi.LevelInfo, "Preparing Python environment")

	total, step := int64(4), int64(3)
	installed, err := ensureInterpreter(ctx, uvBin, exec, notify, notifID, fs, dir, dataDir)
	if err != nil {
		return err
	}
	if !installed {
		total--
		step--
	}

	err = runSyncStep(ctx, uvBin, exec, notify, notifID, kind, fs, dir, step, total)
	if err != nil {
		return err
	}
	return notify.UpdateNotificationProgress(notifID, "Python environment ready", total, total)
}

// ensureInterpreter resolves a Python interpreter, installing one when
// `uv python find` fails so a fresh machine bootstraps on first run, or
// when the managed-fallback link the python shims exec is missing so an
// install migrated from the old layout (uv links in python/bin) relinks
// into uvbin without re-downloading. It reports the find/install step
// and returns whether an install ran.
//
// The install passes `--default` so uv links the bare `python` and
// `python3` executables (not just the versioned `python3.X`) into
// UV_PYTHON_BIN_DIR (<dataDir>/python/uvbin per config.yaml gui.env),
// which the Rune-owned shims in <dataDir>/python/bin fall back to when
// neither a project venv nor a PATH interpreter applies.
func ensureInterpreter(
	ctx context.Context,
	uvBin string,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	notifID string,
	fs workspaceapi.FileSystem,
	dir string,
	dataDir string,
) (bool, error) {
	_ = notify.UpdateNotificationProgress(notifID, "Finding Python interpreter", 1, 4)
	if err := runUV(ctx, uvBin, exec, dir, "python", "find"); err == nil &&
		!managedRelinkNeeded(fs, dataDir) {
		return false, nil
	}
	_ = notify.UpdateNotificationProgress(notifID, "Installing Python interpreter", 2, 4)
	if err := runUV(ctx, uvBin, exec, dir, "python", "install", "--default"); err != nil {
		return true, err
	}
	return true, nil
}

// managedRelinkNeeded reports whether uv already has a managed
// interpreter installed but the link the shims fall back to is missing,
// which is the state of an install migrated from the old layout (uv
// links in python/bin). Relinking that costs no download. With no
// managed interpreter there is nothing to relink, and forcing an
// install would fetch a whole CPython on a machine that already has one
// on PATH, which the shim resolves anyway.
func managedRelinkNeeded(fs workspaceapi.FileSystem, dataDir string) bool {
	if dataDir == "" {
		return false
	}
	if _, err := fs.Stat(pyshim.FallbackPath(dataDir)); err == nil {
		return false
	}
	// UV_PYTHON_INSTALL_DIR per config.yaml gui.env.
	info, err := fs.Stat(filepath.Join(dataDir, "python", "python"))
	return err == nil && info != nil && info.IsDir()
}

// runSyncStep installs or verifies the project dependencies for the
// detected kind, reporting a kind-specific message at step/total.
func runSyncStep(
	ctx context.Context,
	uvBin string,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	notifID string,
	kind projectKind,
	fs workspaceapi.FileSystem,
	dir string,
	step, total int64,
) error {
	switch kind {
	case kindProject:
		_ = notify.UpdateNotificationProgress(notifID, "Syncing project dependencies", step, total)
		return runUV(ctx, uvBin, exec, dir, "sync")
	case kindRequirements:
		_ = notify.UpdateNotificationProgress(notifID, "Installing requirements", step, total)
		// `--allow-existing` keeps the venv creation idempotent: on a re-run
		// it preserves the existing .venv instead of erroring on the present
		// target directory.
		if err := runUV(ctx, uvBin, exec, dir, "venv", "--allow-existing"); err != nil {
			return err
		}
		return runUV(ctx, uvBin, exec, dir, "pip", "install", "-r", firstRequirementsFile(fs, dir))
	case kindVenvOnly:
		_ = notify.UpdateNotificationProgress(notifID, "Verifying virtual environment", step, total)
		return runUV(ctx, uvBin, exec, dir, "python", "find")
	case kindScript:
		_ = notify.UpdateNotificationProgress(notifID, "Creating virtual environment", step, total)
		return runUV(ctx, uvBin, exec, dir, "venv", "--allow-existing")
	default:
		return nil
	}
}

// runUV runs `uv <args>` through the workspace executor in workdir,
// draining stderr so the process never blocks on a full pipe. A workdir
// of "" or "." leaves the executor's default (the workspace root) in
// place; a nested project root is passed so uv operates on that
// project's environment.
func runUV(
	ctx context.Context,
	uvBin string,
	exec workspaceapi.Executor,
	workdir string,
	args ...string,
) error {
	bin := uvBin
	if bin == "" {
		bin = "uv"
	}

	stderrR, stderrW := io.Pipe()
	ch := make(chan error, 1)
	cmd := workspaceapi.Cmd{
		Path:    bin,
		Dir:     uvWorkdir(workdir),
		Args:    args,
		Stderr:  stderrW,
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go debug.CapturePanicReport(func() {
		defer wg.Done()
		_, _ = io.Copy(io.Discard, stderrR)
	})

	if _, err := exec.Start(ctx, cmd); err != nil {
		_ = stderrW.Close()
		wg.Wait()
		return fmt.Errorf("start uv %s: %w", strings.Join(args, " "), err)
	}

	var runErr error
	select {
	case runErr = <-ch:
	case <-ctx.Done():
		runErr = ctx.Err()
	}
	_ = stderrW.Close()
	wg.Wait()

	if runErr != nil {
		return fmt.Errorf("uv %s: %w", strings.Join(args, " "), runErr)
	}
	return nil
}

// uvWorkdir normalizes a project-root directory into a Cmd.Dir value,
// treating "" and "." as "use the executor's default working directory".
func uvWorkdir(dir string) string {
	if dir == "" || dir == "." {
		return ""
	}
	return dir
}
