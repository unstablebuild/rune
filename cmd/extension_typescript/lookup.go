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
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/extension/langext"
)

const tsResolutionTimeout = 5 * time.Second

// minServerMajor is the first TypeScript release whose compiler is
// native and serves LSP.
const minServerMajor = 7

// npmScopes are where npm and bun's hoisted linker
// (node_modules/@typescript), pnpm's hidden hoist
// (node_modules/.pnpm/node_modules/@typescript) and bun's isolated
// linker (node_modules/.bun/node_modules/@typescript) install the
// platform package holding typescript@7's native compiler.
var npmScopes = []string{
	filepath.Join("node_modules", "@typescript"),
	filepath.Join("node_modules", ".pnpm", "node_modules", "@typescript"),
	filepath.Join("node_modules", ".bun", "node_modules", "@typescript"),
}

// readLspPath reads the optional extensions.typescript.config.lsp_path
// override. A missing key is not an error; a non-string value warns and
// is ignored. The returned bool reports whether a non-empty override was
// supplied.
func readLspPath(cfg config.Config, notify browserapi.Notifications) (string, bool) {
	if cfg == nil {
		return "", false
	}
	v, err := cfg.GetString("lsp_path")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) && notify != nil {
			_, _ = notify.Notify(browserapi.LevelWarn,
				"extensions.typescript.config.lsp_path must be a string: %v", err)
		}
		return "", false
	}
	return v, v != ""
}

// readDeveloper reads extensions.typescript.config.developer, which
// registers the profiling commands. A missing key or non-bool value
// resolves to false.
func readDeveloper(cfg config.Config, notify browserapi.Notifications) bool {
	if cfg == nil {
		return false
	}
	v, err := cfg.GetBool("developer")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) && notify != nil {
			_, _ = notify.Notify(browserapi.LevelWarn,
				"extensions.typescript.config.developer must be a bool: %v", err)
		}
		return false
	}
	return v
}

// resolveServer returns the TypeScript 7 native compiler that serves
// LSP for the project at rootDir, in order of preference: the lsp_path
// override; the compiler the project installed, so the server checks
// with the version the project builds with; the tsgo the typescript
// package ships; and a tsgo or tsc on the host that reports version 7
// or later. A miss warns and returns "".
func resolveServer(
	ctx context.Context,
	cfg config.Config,
	notify browserapi.Notifications,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	tools *langext.Tools,
	wsDir, rootDir string,
) string {
	if p, ok := readLspPath(cfg, notify); ok {
		return p
	}
	if p, ok := projectServer(fs, wsDir, rootDir); ok {
		return p
	}
	if p := findPackaged(ctx, tools, notify); p != "" {
		return p
	}
	ctx, cancel := context.WithTimeout(ctx, tsResolutionTimeout)
	defer cancel()
	for _, name := range []string{"tsgo", "tsc"} {
		if p, ok := hostServer(ctx, exec, name); ok {
			return p
		}
	}
	if notify != nil {
		_, _ = notify.Notify(browserapi.LevelWarn,
			"We could not locate a TypeScript 7 language server. Add typescript@7 "+
				"to the project, install the typescript package, or set the "+
				"extensions.typescript.config.lsp_path property in your config "+
				"and reload the workspace")
	}
	return ""
}

// projectServer finds the native compiler of an installed typescript@7,
// walking from rootDir up to wsDir so a monorepo's hoisted install is
// found too. It runs the platform binary directly rather than the
// package's bin/tsc, which is a node launcher for it.
func projectServer(fs workspaceapi.FileSystem, wsDir, rootDir string) (string, bool) {
	for dir := rootDir; ; dir = filepath.Dir(dir) {
		for _, scope := range npmScopes {
			scopeDir := filepath.Join(dir, scope)
			entries, err := fs.ReadDir(scopeDir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !strings.HasPrefix(e.Name(), "typescript-") {
					continue
				}
				bin := filepath.Join(scopeDir, e.Name(), "lib", "tsc")
				if info, err := fs.Stat(bin); err == nil && !info.IsDir() {
					return bin, true
				}
			}
		}
		if dir == wsDir || dir == filepath.Dir(dir) {
			return "", false
		}
	}
}

// findPackaged returns the tsgo the typescript package ships, or "" so
// the caller probes the host. A package that ended up not installed has
// already been explained to the user; any other miss is worth a warning.
func findPackaged(
	ctx context.Context, tools *langext.Tools, notify browserapi.Notifications,
) string {
	packaged, err := tools.Find(ctx, "tsgo")
	if err != nil && !errors.Is(err, pkgapi.ErrNotInstalled) && notify != nil {
		_, _ = notify.NotifyOnce(browserapi.LevelWarn,
			"Could not find tsgo in the typescript package: %v. "+
				"Looking for one on the host instead.", err)
	}
	return packaged
}

// hostServer looks name up on the workspace host and accepts it only if
// it is TypeScript 7 or later: an older tsc does not serve LSP.
func hostServer(ctx context.Context, exec workspaceapi.Executor, name string) (string, bool) {
	bin, err := probeShellLookup(ctx, exec, name)
	if err != nil {
		return "", false
	}
	out, err := commandOutput(ctx, exec, bin, "--version")
	if err != nil || !isNativeVersion(out) {
		return "", false
	}
	return bin, true
}

// isNativeVersion reports whether `tsc --version` output, such as
// "Version 7.0.2", names TypeScript 7 or later.
func isNativeVersion(out string) bool {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return false
	}
	major, _, _ := strings.Cut(fields[len(fields)-1], ".")
	n, err := strconv.Atoi(major)
	return err == nil && n >= minServerMajor
}

func probeShellLookup(
	ctx context.Context, exec workspaceapi.Executor, name string,
) (string, error) {
	out, err := commandOutput(ctx, exec, "sh", "-lc", "command -v "+name)
	if err != nil {
		return "", err
	}
	// command -v may print multiple lines for aliases/functions; take
	// the first absolute path it printed.
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
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
