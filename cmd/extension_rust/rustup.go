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
	"path"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/langext"
)

// detectRustProject reports whether the workspace root looks like a Rust
// project: a Cargo.toml manifest or any top-level .rs source file.
func detectRustProject(_ context.Context, fs workspaceapi.FileSystem) bool {
	if info, err := fs.Stat("Cargo.toml"); err == nil && info != nil && !info.IsDir() {
		return true
	}
	if entries, err := fs.ReadDir("."); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".rs") {
				return true
			}
		}
	}
	return false
}

// toolchainInstalled reports whether a rustup toolchain already exists
// under $RUSTUP_HOME, so first-run bootstrap is skipped on subsequent
// launches. rustup stores toolchains in $RUSTUP_HOME/toolchains.
func toolchainInstalled(fs workspaceapi.FileSystem, rustupHome string) bool {
	if rustupHome == "" {
		return false
	}
	dir := path.Join(rustupHome, "toolchains")
	entries, err := fs.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			return true
		}
	}
	return false
}

// bootstrapRustup installs the stable toolchain and required components
// when none is present. It runs the rustup-init the rust package ships
// once, which also populates $CARGO_HOME/bin with the rustup/cargo
// proxies later commands resolve. The shipped binary is the installer,
// not the manager, so it cannot be invoked as `rustup toolchain install`.
// Best-effort: on failure the caller still brings up rust-analyzer
// against any existing toolchain.
func bootstrapRustup(
	ctx context.Context,
	tools *langext.Tools,
	exec workspaceapi.Executor,
	notify browserapi.Notifications,
	fs workspaceapi.FileSystem,
	rustupHome, dir string,
) error {
	if toolchainInstalled(fs, rustupHome) {
		return nil
	}
	rustupInit, err := tools.Find(ctx, "rustup-init")
	if err != nil {
		return fmt.Errorf("find rustup-init: %w", err)
	}

	notifID, _ := notify.Notify(browserapi.LevelInfo, "Preparing Rust toolchain")

	total := int64(2)
	_ = notify.UpdateNotificationProgress(notifID, "Installing Rust toolchain", 1, total)
	if err := runRustupInit(ctx, rustupInit, exec, dir,
		"-y", "--no-modify-path",
		"--default-toolchain", "stable",
		"--profile", "minimal",
		"-c", "rust-src,clippy,rustfmt"); err != nil {
		return fmt.Errorf("install toolchain: %w", err)
	}

	return notify.UpdateNotificationProgress(notifID, "Rust toolchain ready", total, total)
}

// runRustupInit runs the bundled rustup-init installer at absolute path
// rustupInitBin. An empty path means the package is missing its shipped
// binary, which is an error rather than a reason to fall through to a
// PATH lookup.
func runRustupInit(
	ctx context.Context,
	rustupInitBin string,
	exec workspaceapi.Executor,
	dir string,
	args ...string,
) error {
	if rustupInitBin == "" {
		return fmt.Errorf("bundled rustup-init not found")
	}
	return runCommand(ctx, exec, dir, rustupInitBin, args...)
}

// runCommand starts bin with args through the executor and waits for it
// to exit, draining stderr in a panic-captured goroutine.
func runCommand(
	ctx context.Context,
	exec workspaceapi.Executor,
	dir string,
	bin string,
	args ...string,
) error {
	stderrR, stderrW := io.Pipe()
	ch := make(chan error, 1)
	cmd := workspaceapi.Cmd{
		Path:    bin,
		Dir:     dir,
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
		return fmt.Errorf("start %s %s: %w", bin, strings.Join(args, " "), err)
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
		return fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), runErr)
	}
	return nil
}
