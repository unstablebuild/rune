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
	"path"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/extension/langext"
)

const resolutionTimeout = 5 * time.Second

// defaultClangdLogLevel keeps clangd's stderr to errors: at "info"
// clangd logs every request and reply.
const defaultClangdLogLevel = "error"

// wellKnownClangdPaths are probed after the package and before PATH, so
// an LLVM installed beside the system toolchain wins over the
// toolchain's own clangd, which is often older.
var wellKnownClangdPaths = []string{
	"~/.rune/bin/clangd",
	"/opt/homebrew/opt/llvm/bin/clangd",
	"/usr/local/opt/llvm/bin/clangd",
	"/usr/lib/llvm-21/bin/clangd",
	"/usr/lib/llvm-20/bin/clangd",
	"/usr/lib/llvm-19/bin/clangd",
	"/usr/lib/llvm-18/bin/clangd",
	"/usr/local/bin/clangd",
	"/usr/bin/clangd",
	"/Library/Developer/CommandLineTools/usr/bin/clangd",
}

// readLspPath reads the optional extensions.ccpp.config.lsp_path
// override. A missing key is not an error; a non-string value warns and
// is ignored. The returned bool reports whether a non-empty override was
// supplied.
func readLspPath(cfg config.Config, notify browserapi.Notifications) (string, bool) {
	return readStringKey(cfg, notify, "lsp_path")
}

// readCompileCommandsDir reads the declarative
// extensions.ccpp.config.compile_commands_dir, which `ccpp compdb use`
// overrides.
func readCompileCommandsDir(cfg config.Config, notify browserapi.Notifications) (string, bool) {
	return readStringKey(cfg, notify, "compile_commands_dir")
}

func readStringKey(
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
				"extensions.ccpp.config.%s must be a string: %v", key, err)
		}
		return "", false
	}
	return v, v != ""
}

// readArgs reads the optional extensions.ccpp.config.args, extra clangd
// flags such as --query-driver.
func readArgs(cfg config.Config, notify browserapi.Notifications) []string {
	return readStringList(cfg, notify, "args")
}

// readFallbackFlags reads the optional extensions.ccpp.config.fallback_flags,
// the compiler flags clangd uses for files no compilation database covers.
func readFallbackFlags(cfg config.Config, notify browserapi.Notifications) []string {
	return readStringList(cfg, notify, "fallback_flags")
}

// readStringList reads a list-of-strings key. A list with a non-string
// element warns and is dropped whole, so clangd never sees half of it.
func readStringList(cfg config.Config, notify browserapi.Notifications, key string) []string {
	if cfg == nil {
		return nil
	}
	vs, err := cfg.GetSlice(key)
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) && notify != nil {
			_, _ = notify.Notify(browserapi.LevelWarn,
				"extensions.ccpp.config.%s must be a list of strings: %v", key, err)
		}
		return nil
	}
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		s, ok := v.(string)
		if !ok {
			if notify != nil {
				_, _ = notify.Notify(browserapi.LevelWarn,
					"extensions.ccpp.config.%s must be a list of strings", key)
			}
			return nil
		}
		out = append(out, s)
	}
	return out
}

// clangdLogLevel reads extensions.ccpp.config.debug.log_level, one of
// clangd's --log values.
func clangdLogLevel(cfg config.Config, notify browserapi.Notifications) string {
	if cfg == nil {
		return defaultClangdLogLevel
	}
	debug, err := cfg.GetConfig("debug")
	if err != nil || debug == nil {
		return defaultClangdLogLevel
	}
	level, err := debug.GetString("log_level")
	if err != nil || level == "" {
		return defaultClangdLogLevel
	}
	switch level {
	case "error", "info", "verbose":
		return level
	default:
		if notify != nil {
			_, _ = notify.Notify(browserapi.LevelWarn,
				"extensions.ccpp.config.debug.log_level must be one of "+
					"\"error\", \"info\", \"verbose\"; got %q", level)
		}
		return defaultClangdLogLevel
	}
}

// resolveClangd returns the clangd path. A configured lsp_path overrides
// discovery; otherwise the clangd the ccpp package ships is used, and
// failing that the host is probed: well-known install locations, then
// the workspace host's PATH. A miss warns and returns "" so the caller
// surfaces the initialization error.
func resolveClangd(
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
	if packaged := findPackaged(ctx, tools, notify, "clangd"); packaged != "" {
		return packaged
	}
	if bin, ok := probeWellKnown(fs, wellKnownClangdPaths); ok {
		return bin
	}
	ctx, cancel := context.WithTimeout(ctx, resolutionTimeout)
	defer cancel()
	if bin, err := lookPath(ctx, exec, "clangd"); err == nil {
		return bin
	}
	if notify != nil {
		_, _ = notify.Notify(browserapi.LevelWarn,
			"We could not locate the clangd executable, please set the "+
				"extensions.ccpp.config.lsp_path property in your config and "+
				"reload the workspace")
	}
	return ""
}

// resolveBear returns the Bear `ccpp compdb generate` runs: the one the
// ccpp package ships, whose parse-sh command it relies on, else the one
// on the workspace host's PATH, "" when there is neither. The package
// wins since a Bear older than 4 has no parse-sh.
func resolveBear(
	ctx context.Context,
	notify browserapi.Notifications,
	exec workspaceapi.Executor,
	tools *langext.Tools,
) string {
	if packaged := findPackaged(ctx, tools, notify, "bear"); packaged != "" {
		return packaged
	}
	lookCtx, cancel := context.WithTimeout(ctx, resolutionTimeout)
	defer cancel()
	bin, err := lookPath(lookCtx, exec, "bear")
	if err != nil {
		return ""
	}
	return bin
}

// findPackaged returns the path of name in the ccpp package, or "" so
// the caller falls back to the host. A package that ended up not
// installed has already been explained to the user; any other miss is
// worth a warning.
func findPackaged(ctx context.Context, tools *langext.Tools, notify browserapi.Notifications, name string) string {
	packaged, err := tools.Find(ctx, name)
	if err != nil && !errors.Is(err, pkgapi.ErrNotInstalled) && notify != nil {
		_, _ = notify.NotifyOnce(browserapi.LevelWarn, "Could not find %s in the ccpp package: %v", name, err)
	}
	return packaged
}

func probeWellKnown(fs workspaceapi.FileSystem, candidates []string) (string, bool) {
	for _, candidate := range candidates {
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

// lookPath returns the absolute path name resolves to on the workspace
// host's PATH. It runs no command, so the user is not asked to approve
// one.
func lookPath(ctx context.Context, exec workspaceapi.Executor, name string) (string, error) {
	bin, err := exec.LookPath(ctx, name, "")
	if err != nil {
		return "", err
	}
	if !path.IsAbs(bin) {
		return "", fmt.Errorf("%s resolved to %q, not an absolute path", name, bin)
	}
	return bin, nil
}
