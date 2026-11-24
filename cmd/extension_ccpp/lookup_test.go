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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/extension/langext/langexttest"
)

func TestReadLspPath(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		p, ok := readLspPath(nil, newFakeNotifications())
		assert.False(t, ok)
		assert.Empty(t, p)
	})
	t.Run("override set", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"lsp_path": "/opt/llvm/bin/clangd"})
		p, ok := readLspPath(cfg, newFakeNotifications())
		assert.True(t, ok)
		assert.Equal(t, "/opt/llvm/bin/clangd", p)
	})
	t.Run("empty override ignored", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"lsp_path": ""})
		_, ok := readLspPath(cfg, newFakeNotifications())
		assert.False(t, ok)
	})
	t.Run("compile_commands_dir", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"compile_commands_dir": "build"})
		p, ok := readCompileCommandsDir(cfg, newFakeNotifications())
		assert.True(t, ok)
		assert.Equal(t, "build", p)
	})
}

func TestReadStringList(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		assert.Nil(t, readArgs(newStubConfig(nil), newFakeNotifications()))
		assert.Nil(t, readFallbackFlags(nil, newFakeNotifications()))
	})
	t.Run("forwarded", func(t *testing.T) {
		cfg := stubConfig{slices: map[string][]any{
			"args":           {"--query-driver=/usr/bin/g++"},
			"fallback_flags": {"-std=c17", "-Wall"},
		}}
		assert.Equal(t, []string{"--query-driver=/usr/bin/g++"}, readArgs(cfg, newFakeNotifications()))
		assert.Equal(t, []string{"-std=c17", "-Wall"}, readFallbackFlags(cfg, newFakeNotifications()))
	})
	t.Run("non-string element warns and drops the list", func(t *testing.T) {
		cfg := stubConfig{slices: map[string][]any{"args": {"--log=verbose", 42}}}
		notify := newFakeNotifications()
		assert.Nil(t, readArgs(cfg, notify))
		assert.True(t, notify.hasMessage("extensions.ccpp.config.args"))
	})
}

func TestClangdLogLevel(t *testing.T) {
	assert.Equal(t, "error", clangdLogLevel(nil, newFakeNotifications()))

	cfg := config.JSONFromMap(map[string]any{
		"debug": map[string]any{"log_level": "verbose"},
	})
	assert.Equal(t, "verbose", clangdLogLevel(cfg, newFakeNotifications()))

	invalid := config.JSONFromMap(map[string]any{
		"debug": map[string]any{"log_level": "debug"},
	})
	notify := newFakeNotifications()
	assert.Equal(t, "error", clangdLogLevel(invalid, notify))
	assert.True(t, notify.hasMessage("log_level"))
}

// ccppTools is the lookup bring-up gets for a ccpp package shipping files.
func ccppTools(t *testing.T, inst *langexttest.Installer) *langext.Tools {
	return langext.NewInitializer(t.Context(), nil, nil, inst, langext.ProjectConfig{
		LanguageID: ccppLanguageID, Tools: ccppToolNames,
	}).Tools()
}

func TestResolveClangd(t *testing.T) {
	ctx := context.Background()
	packaged := []string{"/data/bin/clangd"}
	notInstalled := &langexttest.Installer{Err: fmt.Errorf("ccpp: %w", pkgapi.ErrNotInstalled)}

	t.Run("lsp_path override wins without a lookup", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"lsp_path": "/opt/llvm/bin/clangd"})
		inst := &langexttest.Installer{Files: packaged}
		got := resolveClangd(ctx, cfg, newFakeNotifications(), newFakeFS(), newFakeExecutor(), ccppTools(t, inst))
		assert.Equal(t, "/opt/llvm/bin/clangd", got)
		assert.Zero(t, inst.Lookups())
	})

	t.Run("defaults to packaged binary", func(t *testing.T) {
		fs := newFakeFS().addFile("/usr/bin/clangd")
		got := resolveClangd(ctx, nil, newFakeNotifications(), fs, newFakeExecutor(),
			ccppTools(t, &langexttest.Installer{Files: packaged}))
		assert.Equal(t, "/data/bin/clangd", got)
	})

	t.Run("falls back to well-known path, LLVM before the system toolchain", func(t *testing.T) {
		fs := newFakeFS().addFile("/usr/bin/clangd").addFile("/opt/homebrew/opt/llvm/bin/clangd")
		notify := newFakeNotifications()
		got := resolveClangd(ctx, nil, notify, fs, newFakeExecutor(), ccppTools(t, notInstalled))
		assert.Equal(t, "/opt/homebrew/opt/llvm/bin/clangd", got)
		assert.Empty(t, notify.notifMessages(), "Rune has already explained the missing package")
	})

	t.Run("package without clangd warns and falls back", func(t *testing.T) {
		fs := newFakeFS().addFile("/usr/bin/clangd")
		notify := newFakeNotifications()
		inst := &langexttest.Installer{Files: []string{"/data/bin/lldb-dap"}}
		got := resolveClangd(ctx, nil, notify, fs, newFakeExecutor(), ccppTools(t, inst))
		assert.Equal(t, "/usr/bin/clangd", got)
		assert.Equal(t, []string{"Could not find clangd in the ccpp package: " +
			"ccpp/bin/clangd: langext: tool not shipped by package"}, notify.notifMessages())
	})

	t.Run("failed lookup warns and falls back", func(t *testing.T) {
		fs := newFakeFS().addFile("/usr/bin/clangd")
		notify := newFakeNotifications()
		inst := &langexttest.Installer{Err: errors.New("rpc error: code = PermissionDenied")}
		got := resolveClangd(ctx, nil, notify, fs, newFakeExecutor(), ccppTools(t, inst))
		assert.Equal(t, "/usr/bin/clangd", got)
		assert.True(t, notify.hasMessage("rpc error: code = PermissionDenied"))
	})

	t.Run("falls back to PATH", func(t *testing.T) {
		ex := newFakeExecutor().onPath("clangd", "/nix/store/abc/bin/clangd")
		got := resolveClangd(ctx, nil, newFakeNotifications(), newFakeFS(), ex, ccppTools(t, notInstalled))
		assert.Equal(t, "/nix/store/abc/bin/clangd", got)
		assert.Empty(t, ex.callsSnapshot(),
			"looking clangd up must not run a command the user is asked to approve")
	})

	t.Run("missing everywhere warns and resolves empty", func(t *testing.T) {
		notify := newFakeNotifications()
		got := resolveClangd(ctx, nil, notify, newFakeFS(), newFakeExecutor(), ccppTools(t, notInstalled))
		assert.Empty(t, got)
		assert.True(t, notify.hasMessage("could not locate the clangd executable"))
	})
}

func TestResolveBear(t *testing.T) {
	ctx := context.Background()
	notInstalled := &langexttest.Installer{Err: fmt.Errorf("ccpp: %w", pkgapi.ErrNotInstalled)}

	t.Run("the packaged one wins over PATH", func(t *testing.T) {
		inst := &langexttest.Installer{Files: []string{"/data/bin/clangd", "/data/bin/bear"}}
		ex := newFakeExecutor().onPath("bear", "/opt/homebrew/bin/bear")
		assert.Equal(t, "/data/bin/bear", resolveBear(ctx, newFakeNotifications(), ex, ccppTools(t, inst)))
		assert.Empty(t, ex.callsSnapshot(), "looking bear up must not run a command the user is asked to approve")
	})
	t.Run("a package without bear warns and falls back to PATH", func(t *testing.T) {
		notify := newFakeNotifications()
		inst := &langexttest.Installer{Files: []string{"/data/bin/clangd"}}
		ex := newFakeExecutor().onPath("bear", "/opt/homebrew/bin/bear")
		assert.Equal(t, "/opt/homebrew/bin/bear", resolveBear(ctx, notify, ex, ccppTools(t, inst)))
		assert.Equal(t, []string{"Could not find bear in the ccpp package: " +
			"ccpp/bin/bear: langext: tool not shipped by package"}, notify.notifMessages())
	})
	t.Run("without the package PATH is used quietly", func(t *testing.T) {
		notify := newFakeNotifications()
		ex := newFakeExecutor().onPath("bear", "/usr/bin/bear")
		assert.Equal(t, "/usr/bin/bear", resolveBear(ctx, notify, ex, ccppTools(t, notInstalled)))
		assert.Empty(t, notify.notifMessages(), "Rune has already explained the missing package")
	})
	t.Run("no package and no PATH entry resolves empty quietly", func(t *testing.T) {
		notify := newFakeNotifications()
		assert.Empty(t, resolveBear(ctx, notify, newFakeExecutor(), ccppTools(t, notInstalled)))
		assert.Empty(t, notify.notifMessages(), "Rune has already explained the missing package")
	})
}
