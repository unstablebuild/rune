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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/extension/langext"
)

const zigResolutionTimeout = 5 * time.Second

// $RUNE_INSTALLDIR is the data dir the workspace host provisions
// package binaries under; the fs resolves it on the workspace host.
var wellKnownZlsPaths = []string{
	"$RUNE_INSTALLDIR/bin/zls",
	"/opt/homebrew/bin/zls",
	"/usr/local/bin/zls",
}

var wellKnownZigPaths = []string{
	"$RUNE_INSTALLDIR/bin/zig",
	"/opt/homebrew/bin/zig",
	"/usr/local/bin/zig",
}

// readLspPath reads the optional extensions.zig.config.lsp_path
// override. A missing key is not an error; a non-string value warns and
// is ignored. The returned bool reports whether a non-empty override was
// supplied.
func readLspPath(cfg config.Config, notify browserapi.Notifications) (string, bool) {
	return readPathKey(cfg, notify, "lsp_path")
}

// readZigPath reads the optional extensions.zig.config.zig_path
// override for the zig compiler binary.
func readZigPath(cfg config.Config, notify browserapi.Notifications) (string, bool) {
	return readPathKey(cfg, notify, "zig_path")
}

func readPathKey(
	cfg config.Config, notify browserapi.Notifications, key string,
) (string, bool) {
	if cfg == nil {
		return "", false
	}
	v, err := cfg.GetString(key)
	if err != nil {
		if errors.Is(err, config.ErrNotFound) {
			return "", false
		}
		if notify != nil {
			_, _ = notify.Notify(browserapi.LevelWarn,
				"extensions.zig.config.%s must be a string: %v", key, err)
		}
		return "", false
	}
	return v, v != ""
}

// resolveZls returns the zls language server path. A configured lsp_path
// overrides discovery; otherwise the zls the zig package ships is used,
// and failing that the host is probed: well-known install locations, then
// a login-shell `command -v` lookup. A miss warns and returns "" so the
// caller surfaces the initialization error.
func resolveZls(
	ctx context.Context,
	cfg config.Config,
	notify browserapi.Notifications,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	tools *langext.Tools,
) string {
	if p, ok := readLspPath(cfg, notify); ok {
		return p
	}
	packaged := findPackaged(ctx, tools, notify, "zls")
	bin, err := resolveBinary(ctx, fs, exec, packaged, "zls", wellKnownZlsPaths)
	if err == nil {
		return bin
	}
	if notify != nil {
		_, _ = notify.Notify(browserapi.LevelWarn,
			"We could not locate the zls executable, please set the "+
				"extensions.zig.config.lsp_path property in your config and "+
				"reload the workspace")
	}
	return ""
}

// resolveZig returns the zig compiler path forwarded to zls as
// zig_exe_path. A configured zig_path overrides the zig the package
// ships, which overrides discovery. An empty result is acceptable: zls
// then falls back to its own PATH lookup.
func resolveZig(
	ctx context.Context,
	cfg config.Config,
	notify browserapi.Notifications,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	tools *langext.Tools,
) string {
	if p, ok := readZigPath(cfg, notify); ok {
		return p
	}
	packaged := findPackaged(ctx, tools, notify, "zig")
	bin, err := resolveBinary(ctx, fs, exec, packaged, "zig", wellKnownZigPaths)
	if err != nil {
		return ""
	}
	return bin
}

// findPackaged returns the name the zig package ships, or "" so the
// caller probes the host. A package that ended up not installed has
// already been explained to the user; any other miss is worth a warning.
func findPackaged(
	ctx context.Context, tools *langext.Tools, notify browserapi.Notifications, name string,
) string {
	packaged, err := tools.Find(ctx, name)
	if err != nil && !errors.Is(err, pkgapi.ErrNotInstalled) && notify != nil {
		_, _ = notify.NotifyOnce(browserapi.LevelWarn,
			"Could not find %s in the zig package: %v. Looking for one on the host instead.",
			name, err)
	}
	return packaged
}

// resolveBinary prefers packaged and otherwise probes for name on the
// workspace host: the well-known locations, then a shell lookup.
func resolveBinary(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	packaged string,
	name string,
	wellKnown []string,
) (string, error) {
	if packaged != "" {
		return packaged, nil
	}

	if bin, ok := probeWellKnown(fs, wellKnown); ok {
		return bin, nil
	}

	ctx, cancel := context.WithTimeout(ctx, zigResolutionTimeout)
	defer cancel()
	if bin, err := probeShellLookup(ctx, exec, name); err == nil {
		return bin, nil
	}

	return "", fmt.Errorf("%s binary not found on workspace host", name)
}

func probeWellKnown(fs workspaceapi.FileSystem, candidates []string) (string, bool) {
	for _, candidate := range candidates {
		// Candidates may reference the workspace host's data dir through
		// the environment the runner exports; fs.URI expands only ~.
		uri, err := fs.URI(os.ExpandEnv(candidate))
		if err != nil {
			continue
		}
		full := uri.Path()
		info, err := fs.Stat(full)
		if err != nil || info == nil || info.IsDir() {
			continue
		}
		return full, true
	}
	return "", false
}

func probeShellLookup(
	ctx context.Context, exec workspaceapi.Executor, name string,
) (string, error) {
	out, err := commandOutput(ctx, exec, "sh", "-lc", "command -v "+name)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	// command -v may print multiple lines for aliases/functions; take
	// the first absolute path it printed.
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/") {
			return line, nil
		}
	}
	return "", fmt.Errorf("shell probe produced no absolute path: %q", out)
}

// commandOutput runs bin with args and returns its stdout, draining
// stderr so the process never blocks on a full pipe.
func commandOutput(
	ctx context.Context,
	exec workspaceapi.Executor,
	bin string,
	args ...string,
) (string, error) {
	var stdout, stderr bytes.Buffer
	ch := make(chan error, 1)
	cmd := workspaceapi.Cmd{
		Path:    bin,
		Args:    args,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	}
	if _, err := exec.Start(ctx, cmd); err != nil {
		return "", fmt.Errorf("start %s: %w", bin, err)
	}
	var runErr error
	select {
	case runErr = <-ch:
	case <-ctx.Done():
		runErr = ctx.Err()
	}
	if runErr != nil {
		return "", fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), runErr)
	}
	return stdout.String(), nil
}
