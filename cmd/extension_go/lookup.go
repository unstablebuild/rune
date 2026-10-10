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
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/extension/langext"
)

const goplsResolutionTimeout = 5 * time.Second

var wellKnownGoplsPaths = []string{
	"~/go/bin/gopls",
	"/usr/local/go/bin/gopls",
	"/opt/homebrew/bin/gopls",
	"/usr/local/bin/gopls",
}

func hasGoProjectFiles(_ context.Context, fs workspaceapi.FileSystem) bool {
	for _, name := range []string{"go.mod", "go.sum", "go.work"} {
		if _, err := fs.Stat(name); err == nil {
			return true
		}
	}
	return false
}

// resolveGoplsForRoot locates the gopls binary for a Go project root. The
// caller only invokes it once a module has been discovered, so it no
// longer gates on hasGoProjectFiles: an lsp_path override wins, otherwise
// the packaged gopls, otherwise the host is probed and a failure warns.
// gopls probes are host-global, so the result is correct for every root
// on the same host.
func resolveGoplsForRoot(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	tools *langext.Tools,
	cfg config.Config,
	notify browserapi.Notifications,
	scheme string,
) string {
	if lspPath, ok := readGoplsLspPath(cfg, notify); ok {
		return lspPath
	}
	packaged, err := tools.Find(ctx, "gopls")
	if err != nil && !errors.Is(err, pkgapi.ErrNotInstalled) && notify != nil {
		_, _ = notify.NotifyOnce(browserapi.LevelWarn,
			"Could not find gopls in the go package: %v. Looking for one on the host instead.", err)
	}
	bin, err := resolveGoplsBinary(ctx, fs, exec, packaged)
	if err == nil {
		return bin
	}
	msg := "We could not locate the gopls executable, please set the " +
		"extensions.go.config.lsp_path property in your config and " +
		"reload the workspace"
	if scheme == "file" {
		msg = "We could not locate the gopls executable, please " +
			"reinstall the go extension"
	}
	if notify != nil {
		_, _ = notify.Notify(browserapi.LevelWarn, msg)
	}
	return ""
}

// resolveGoplsBinary prefers packaged, the gopls the go package ships,
// and otherwise looks for one already on the host.
func resolveGoplsBinary(
	ctx context.Context,
	fs workspaceapi.FileSystem,
	exec workspaceapi.Executor,
	packaged string,
) (string, error) {
	if packaged != "" {
		return packaged, nil
	}

	if bin, ok := probeWellKnown(fs); ok {
		return bin, nil
	}

	ctx, cancel := context.WithTimeout(ctx, goplsResolutionTimeout)
	defer cancel()
	if bin, err := probeShellLookup(ctx, exec); err == nil {
		return bin, nil
	}

	return "", errors.New("gopls binary not found on workspace host")
}

func probeShellLookup(
	ctx context.Context, exec workspaceapi.Executor,
) (string, error) {
	var stdout, stderr bytes.Buffer
	ch := make(chan error, 1)
	cmd := workspaceapi.Cmd{
		Path:    "sh",
		Args:    []string{"-lc", "command -v gopls"},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	}
	if _, err := exec.Start(ctx, cmd); err != nil {
		return "", fmt.Errorf("start sh: %w", err)
	}
	select {
	case err := <-ch:
		if err != nil {
			return "", err
		}
	case <-ctx.Done():
		return "", ctx.Err()
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" || !strings.HasPrefix(out, "/") {
		return "", fmt.Errorf("shell probe produced no absolute path: %q", out)
	}
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

func probeWellKnown(fs workspaceapi.FileSystem) (string, bool) {
	for _, candidate := range wellKnownGoplsPaths {
		uri, err := fs.URI(candidate)
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
