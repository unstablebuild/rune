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
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

func TestDetectZigProject(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeFS)
		want  bool
	}{
		{"empty", func(*fakeFS) {}, false},
		{"build manifest", func(f *fakeFS) { f.addFile("build.zig") }, true},
		{"zon manifest only", func(f *fakeFS) { f.addFile("build.zig.zon") }, true},
		{"zig source only", func(f *fakeFS) { f.addEntry("main.zig", false) }, true},
		{"non-zig files only", func(f *fakeFS) { f.addEntry("README.md", false) }, false},
		{"build.zig dir is not a manifest", func(f *fakeFS) { f.addDir("build.zig") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeFS()
			tc.setup(fs)
			assert.Equal(t, tc.want, detectZigProject(context.Background(), fs))
		})
	}
}

func TestReadLspPath(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		p, ok := readLspPath(nil, newFakeNotifications())
		assert.False(t, ok)
		assert.Empty(t, p)
	})
	t.Run("override set", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"lsp_path": "/opt/zls/zls"})
		p, ok := readLspPath(cfg, newFakeNotifications())
		assert.True(t, ok)
		assert.Equal(t, "/opt/zls/zls", p)
	})
	t.Run("empty override ignored", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"lsp_path": ""})
		p, ok := readLspPath(cfg, newFakeNotifications())
		assert.False(t, ok)
		assert.Empty(t, p)
	})
	t.Run("zig_path override", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"zig_path": "/opt/zig/zig"})
		p, ok := readZigPath(cfg, newFakeNotifications())
		assert.True(t, ok)
		assert.Equal(t, "/opt/zig/zig", p)
	})
}

func TestResolveZls(t *testing.T) {
	ctx := context.Background()
	t.Run("lsp_path override wins", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"lsp_path": "/opt/zls/zls"})
		fs := newFakeFS().addFile("/data/bin/zls")
		got := resolveZls(ctx, cfg, newFakeNotifications(), fs,
			newFakeExecutor(), fakeInstaller{fs: fs, root: "/data"})
		assert.Equal(t, "/opt/zls/zls", got)
	})

	t.Run("defaults to provisioned binary", func(t *testing.T) {
		fs := newFakeFS().addFile("/data/bin/zls")
		got := resolveZls(ctx, nil, newFakeNotifications(), fs,
			newFakeExecutor(), fakeInstaller{fs: fs, root: "/data"})
		assert.Equal(t, "/data/bin/zls", got)
	})

	t.Run("falls back to well-known path", func(t *testing.T) {
		fs := newFakeFS().addFile("/opt/homebrew/bin/zls")
		got := resolveZls(ctx, nil, newFakeNotifications(), fs,
			newFakeExecutor(), fakeInstaller{fs: fs, root: "/data"})
		assert.Equal(t, "/opt/homebrew/bin/zls", got)
	})

	t.Run("falls back to shell lookup", func(t *testing.T) {
		fs := newFakeFS()
		ex := newFakeExecutor().respond(
			"sh -lc command -v zls", scriptedCmd{stdout: "/usr/bin/zls\n"})
		got := resolveZls(ctx, nil, newFakeNotifications(), fs,
			ex, fakeInstaller{fs: fs, root: "/data"})
		assert.Equal(t, "/usr/bin/zls", got)
	})

	t.Run("missing everywhere warns and resolves empty", func(t *testing.T) {
		fs := newFakeFS()
		notify := newFakeNotifications()
		got := resolveZls(ctx, nil, notify, fs,
			newFakeExecutor(), fakeInstaller{fs: fs, root: "/data"})
		assert.Empty(t, got)
		assert.True(t, notify.hasMessage("could not locate the zls executable"))
	})
}

func TestResolveZig(t *testing.T) {
	ctx := context.Background()
	t.Run("zig_path override wins", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"zig_path": "/opt/zig/zig"})
		fs := newFakeFS().addFile("/data/bin/zig")
		got := resolveZig(ctx, cfg, newFakeNotifications(), fs,
			newFakeExecutor(), fakeInstaller{fs: fs, root: "/data"})
		assert.Equal(t, "/opt/zig/zig", got)
	})

	t.Run("missing everywhere resolves empty without warning", func(t *testing.T) {
		fs := newFakeFS()
		notify := newFakeNotifications()
		got := resolveZig(ctx, nil, notify, fs,
			newFakeExecutor(), fakeInstaller{fs: fs, root: "/data"})
		assert.Empty(t, got)
		assert.Empty(t, notify.notifMessages())
	})
}

func TestReadBuildOnSave(t *testing.T) {
	t.Run("absent leaves enable nil", func(t *testing.T) {
		bos := readBuildOnSave(newStubConfig(nil), newFakeNotifications())
		assert.Nil(t, bos.Enable)
		assert.Empty(t, bos.Args)
	})
	t.Run("nil config", func(t *testing.T) {
		bos := readBuildOnSave(nil, newFakeNotifications())
		assert.Nil(t, bos.Enable)
	})
	t.Run("explicit enable", func(t *testing.T) {
		cfg := stubConfig{bools: map[string]bool{"build_on_save": true}}
		bos := readBuildOnSave(cfg, newFakeNotifications())
		require.NotNil(t, bos.Enable)
		assert.True(t, *bos.Enable)
	})
	t.Run("explicit disable", func(t *testing.T) {
		cfg := stubConfig{bools: map[string]bool{"build_on_save": false}}
		bos := readBuildOnSave(cfg, newFakeNotifications())
		require.NotNil(t, bos.Enable)
		assert.False(t, *bos.Enable)
	})
	t.Run("args forwarded", func(t *testing.T) {
		cfg := stubConfig{slices: map[string][]any{
			"build_on_save_args": {"check", "-fincremental"},
		}}
		bos := readBuildOnSave(cfg, newFakeNotifications())
		assert.Equal(t, []string{"check", "-fincremental"}, bos.Args)
	})
	t.Run("non-string args warn and are dropped", func(t *testing.T) {
		cfg := stubConfig{slices: map[string][]any{
			"build_on_save_args": {"check", 42},
		}}
		notify := newFakeNotifications()
		bos := readBuildOnSave(cfg, notify)
		assert.Empty(t, bos.Args)
		assert.True(t, notify.hasMessage("build_on_save_args"))
	})
}

func TestHasCheckStep(t *testing.T) {
	assert.True(t, hasCheckStep([]byte(`const check = b.step("check", "typecheck");`)))
	assert.False(t, hasCheckStep([]byte(`const run = b.step("run", "run the app");`)))
}

func TestWarnMissingCheckStep(t *testing.T) {
	root := langext.Root{Dir: "/ws", URI: "file:///ws"}
	withCheck := []byte(`const check = b.step("check", "typecheck only");`)
	withoutCheck := []byte(`const run = b.step("run", "run");`)

	t.Run("no check step notifies", func(t *testing.T) {
		fs := newFakeFS().addContent("/ws/build.zig", withoutCheck)
		notify := newFakeNotifications()
		warnMissingCheckStep(fs, notify, root, buildOnSaveOptions{})
		assert.True(t, notify.hasMessage("check"))
	})
	t.Run("check step present stays quiet", func(t *testing.T) {
		fs := newFakeFS().addContent("/ws/build.zig", withCheck)
		notify := newFakeNotifications()
		warnMissingCheckStep(fs, notify, root, buildOnSaveOptions{})
		assert.Empty(t, notify.notifMessages())
	})
	t.Run("explicit setting silences the hint", func(t *testing.T) {
		enabled := true
		fs := newFakeFS().addContent("/ws/build.zig", withoutCheck)
		notify := newFakeNotifications()
		warnMissingCheckStep(fs, notify, root, buildOnSaveOptions{Enable: &enabled})
		assert.Empty(t, notify.notifMessages())
	})
	t.Run("missing build.zig stays quiet", func(t *testing.T) {
		notify := newFakeNotifications()
		warnMissingCheckStep(newFakeFS(), notify, root, buildOnSaveOptions{})
		assert.Empty(t, notify.notifMessages())
	})
}

func TestZlsInitializeParams(t *testing.T) {
	enabled := true
	params, err := zlsInitializeParams("file:///ws", "ws", "/data/bin/zls", "/data/bin/zig",
		"debug", buildOnSaveOptions{Enable: &enabled, Args: []string{"check"}})
	require.NoError(t, err)
	assert.Equal(t, "file:///ws", params.RootURI)

	// zls only registers workspaces from workspaceFolders; rootUri alone
	// is ignored, so the folder must always be present.
	require.Len(t, params.WorkspaceFolders, 1)
	assert.Equal(t, "file:///ws", params.WorkspaceFolders[0].URI)
	assert.Equal(t, "ws", params.WorkspaceFolders[0].Name)

	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	assert.Equal(t, "zig", opts["langID"])
	assert.Equal(t,
		"/data/bin/zls --enable-stderr-logs --log-level debug",
		opts["command"])
	assert.Equal(t, "/data/bin/zig", opts["zig_exe_path"])
	assert.Equal(t, true, opts["enable_build_on_save"])
	assert.Equal(t, []any{"check"}, opts["build_on_save_args"])
	assert.Equal(t, false, opts["enable_snippets"])

	// With no explicit build-on-save setting, the key must stay absent so
	// zls keeps its check-step auto-detection default; an absent zig path
	// lets zls run its own PATH discovery.
	defaults, err := zlsInitializeParams(
		"file:///ws", "ws", "zls", "", "", buildOnSaveOptions{})
	require.NoError(t, err)
	var opts2 map[string]any
	require.NoError(t, json.Unmarshal(defaults.InitializeOptions, &opts2))
	_, hasBos := opts2["enable_build_on_save"]
	assert.False(t, hasBos)
	_, hasArgs := opts2["build_on_save_args"]
	assert.False(t, hasArgs)
	_, hasZig := opts2["zig_exe_path"]
	assert.False(t, hasZig)
}

func TestZlsLogLevel(t *testing.T) {
	assert.Equal(t, "info", zlsLogLevel(nil, newFakeNotifications()))

	cfg := config.JSONFromMap(map[string]any{
		"debug": map[string]any{"log_level": "debug"},
	})
	assert.Equal(t, "debug", zlsLogLevel(cfg, newFakeNotifications()))

	invalid := config.JSONFromMap(map[string]any{
		"debug": map[string]any{"log_level": "trace"},
	})
	notify := newFakeNotifications()
	assert.Equal(t, "info", zlsLogLevel(invalid, notify))
	assert.NotEmpty(t, notify.notifs)
}

func TestZlsInitializeCapabilities(t *testing.T) {
	params, err := zlsInitializeParams(
		"file:///ws", "ws", "zls", "", "", buildOnSaveOptions{})
	require.NoError(t, err)
	var caps map[string]any
	require.NoError(t, json.Unmarshal(params.Capabilities, &caps))

	textDocument, _ := caps["textDocument"].(map[string]any)
	require.NotNil(t, textDocument)
	_, hasPublish := textDocument["publishDiagnostics"]
	assert.True(t, hasPublish,
		"zls only pushes diagnostics when publishDiagnostics is advertised")

	for _, unsupported := range []string{
		"implementation", "rangeFormatting", "codeLens", "callHierarchy",
	} {
		_, has := textDocument[unsupported]
		assert.False(t, has, "%s is not implemented by zls and must not be advertised", unsupported)
	}

	hover, _ := textDocument["hover"].(map[string]any)
	assert.Contains(t, hover["contentFormat"], "markdown")
}

func TestZigInitializeCommandLogsToStderr(t *testing.T) {
	params, err := zlsInitializeParams(
		"file:///ws", "ws", "/data/bin/zls", "", "", buildOnSaveOptions{})
	require.NoError(t, err)
	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	assert.Equal(t,
		"/data/bin/zls --enable-stderr-logs --log-level info",
		opts["command"])
}

func TestExtendWorkspaceNonZigRegistersButSkipsInit(t *testing.T) {
	fs := newFakeFS()
	lsp := &captureLSP{}
	ext := &zigExtension{}
	registered := false
	err := ext.extendWorkspaceWith(context.Background(),
		fs, newFakeExecutor(), newFakeNotifications(), lsp, &fakeEditor{},
		&fakeWM{}, fakeInstaller{fs: fs, root: "/data"}, nil,
		func(textapi.CommandManual, textapi.REPLHandler) error {
			registered = true
			return nil
		},
		func(textapi.CommandManual, textapi.CommandHandler) error { return nil })
	require.NoError(t, err)
	assert.True(t, registered)
	_, count := lsp.captured()
	assert.Zero(t, count)
}

func TestExtendWorkspaceRegistersAndInitializes(t *testing.T) {
	fs := newFakeFS().
		addFile("build.zig").
		addFile("/data/bin/zls").
		addFile("/data/bin/zig")
	lsp := &captureLSP{}

	var manuals []textapi.CommandManual
	var cmds []textapi.CommandManual
	ext := &zigExtension{}
	err := ext.extendWorkspaceWith(context.Background(),
		fs, newFakeExecutor(), newFakeNotifications(), lsp, &fakeEditor{},
		&fakeWM{}, fakeInstaller{fs: fs, root: "/data"}, nil,
		func(m textapi.CommandManual, _ textapi.REPLHandler) error {
			manuals = append(manuals, m)
			return nil
		},
		func(m textapi.CommandManual, _ textapi.CommandHandler) error {
			cmds = append(cmds, m)
			return nil
		})
	require.NoError(t, err)
	require.Len(t, manuals, 1)
	assert.Equal(t, zigCommandName, manuals[0].Name)

	// The code-action command shares the `zig` word with the REPL
	// command but lives in the command-prompt registry.
	require.Len(t, cmds, 1)
	assert.Equal(t, zigActionCmdName, cmds[0].Name)
	assert.NotEmpty(t, cmds[0].Commands)

	params, count := lsp.captured()
	require.Equal(t, 1, count)
	require.Len(t, params.WorkspaceFolders, 1)
	assert.Equal(t, params.RootURI, params.WorkspaceFolders[0].URI)
	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	assert.Equal(t,
		"/data/bin/zls --enable-stderr-logs --log-level info",
		opts["command"])
	assert.Equal(t, "/data/bin/zig", opts["zig_exe_path"])
}

func TestExtendWorkspaceNestedDiscovery(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "tools", "cli")
	require.NoError(t, os.MkdirAll(filepath.Join(proj, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(proj, "build.zig"),
		[]byte("pub fn build(b: anytype) void { _ = b; }\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(proj, "src", "main.zig"),
		[]byte("pub fn main() void {}\n"), 0o644))

	stray := filepath.Join(root, "stray")
	require.NoError(t, os.MkdirAll(stray, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stray, "loose.zig"),
		[]byte("fn x() void {}\n"), 0o644))

	fs := realFS{root: root}
	lsp := &captureLSP{}
	editor := &fakeEditor{}
	ext := &zigExtension{}

	err := ext.extendWorkspaceWith(context.Background(),
		fs, newFakeExecutor(), newFakeNotifications(), lsp, editor,
		&fakeWM{}, fakeInstaller{fs: fs, root: "/data"}, nil,
		func(textapi.CommandManual, textapi.REPLHandler) error { return nil },
		func(textapi.CommandManual, textapi.CommandHandler) error { return nil })
	require.NoError(t, err)

	// No root manifest, so nothing is initialized on startup.
	_, count := lsp.captured()
	require.Zero(t, count)

	// A .zig with no enclosing build.zig must not spawn a server.
	editor.open(t, filepath.Join(stray, "loose.zig"))

	// Opening the nested project's source initializes a server rooted there.
	editor.open(t, filepath.Join(proj, "src", "main.zig"))
	lsp.waitForInit(t, 5*time.Second)

	params, count := lsp.captured()
	require.Equal(t, 1, count, "only the marked nested project must initialize")
	assert.Equal(t, "file://"+proj, params.RootURI)
	require.Len(t, params.WorkspaceFolders, 1)
	assert.Equal(t, "file://"+proj, params.WorkspaceFolders[0].URI)
}

func TestExtendWorkspaceWarnsMissingCheckStep(t *testing.T) {
	buildZig := `const std = @import("std");
pub fn build(b: *std.Build) void {
    _ = b;
}
`
	t.Run("without check step", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "build.zig"), []byte(buildZig), 0o644))
		env := runZigExtensionOnDir(t, dir, t.TempDir(), nil)
		assert.True(t, env.notify.hasMessage("build-on-save"),
			"expected the check-step hint; got %v", env.notify.notifMessages())
	})
	t.Run("with check step", func(t *testing.T) {
		dir := t.TempDir()
		withCheck := buildZig + `// const check = b.step("check", "typecheck");` + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "build.zig"), []byte(withCheck), 0o644))
		env := runZigExtensionOnDir(t, dir, t.TempDir(), nil)
		assert.False(t, env.notify.hasMessage("build-on-save"))
	})
}

func TestZigHandlerUnknownCommand(t *testing.T) {
	_, h := newZigHandler(newFakeExecutor(), newFakeNotifications(), "/ws",
		func(context.Context) string { return "/opt/zig/zig" }, nil)
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "zig", Args: []string{"frobnicate"}}, repl.NopProgressWriter())
	require.Error(t, err)
}

func TestZigHandlerForwardsToZig(t *testing.T) {
	bin := "/opt/zig/zig"
	ex := newFakeExecutor().respond(bin+" version", scriptedCmd{stdout: "0.16.0"})
	_, h := newZigHandler(ex, newFakeNotifications(), "/ws",
		func(context.Context) string { return bin }, nil)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "zig", Args: []string{"version"}}, repl.NopProgressWriter())
	require.NoError(t, err)
	out, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Contains(t, ex.callsSnapshot(), bin+" version")
}

func TestZigHandlerMissingBinaryErrors(t *testing.T) {
	_, h := newZigHandler(newFakeExecutor(), newFakeNotifications(), "/ws",
		func(context.Context) string { return "" }, nil)
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "zig", Args: []string{"build"}}, repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zig_path")
}

func TestZigHandlerReloadInvokesCallback(t *testing.T) {
	reloaded := false
	_, h := newZigHandler(newFakeExecutor(), newFakeNotifications(), "/ws",
		func(context.Context) string { return "zig" },
		func(context.Context) error { reloaded = true; return nil })
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "zig", Args: []string{"reload"}}, repl.NopProgressWriter())
	require.NoError(t, err)
	assert.True(t, reloaded)
}

func TestZigHandlerComplete(t *testing.T) {
	_, h := newZigHandler(newFakeExecutor(), newFakeNotifications(), "/ws",
		func(context.Context) string { return "zig" }, nil)
	ctx := context.Background()

	it, err := h.Complete(ctx, "", []string{"bu"})
	require.NoError(t, err)
	got, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Equal(t, []string{"build"}, got)

	it, err = h.Complete(ctx, "", nil)
	require.NoError(t, err)
	got, err = iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Equal(t, zigSubcommandNames, got)
}

func TestZigHandlerFailureIncludesOutput(t *testing.T) {
	bin := "/opt/zig/zig"
	ex := newFakeExecutor().respond(bin+" build", scriptedCmd{
		stderr: "error: expected type 'i32'", err: assertErr})
	_, h := newZigHandler(ex, newFakeNotifications(), "/ws",
		func(context.Context) string { return bin }, nil)
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "zig", Args: []string{"build"}}, repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected type 'i32'")
}

func TestNewExtensionMetadata(t *testing.T) {
	_, meta := NewExtension()
	assert.Equal(t, "zig", meta.ExtensionID)
	assert.NotEmpty(t, meta.ExtensionName)
	// The code-action picker floats a window.
	assert.Contains(t, meta.Permissions, extensionapi.PermissionBrowserWindowManager)
}

func TestZigActionRouterUnknownCommand(t *testing.T) {
	_, h := newTestZigActionRouter()
	err := h.HandleCommand(context.Background(),
		textapi.Command{Name: zigActionCmdName, Args: []string{"frobnicate"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown zig subcommand")
}

func TestZigActionRouterRejectsForeignCommand(t *testing.T) {
	_, h := newTestZigActionRouter()
	err := h.HandleCommand(context.Background(), textapi.Command{Name: "rust"})
	require.Error(t, err)

	err = h.HandleCommand(context.Background(), textapi.Command{Name: zigActionCmdName})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing subcommand")
}

func TestZigActionRouterComplete(t *testing.T) {
	manual, h := newTestZigActionRouter()
	ctx := context.Background()

	it, err := h.Complete(ctx, zigActionCmdName, []string{""})
	require.NoError(t, err)
	got, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"fix-all", "list", "organize-imports", "quickfix", "refactor",
	}, got)

	documented := make([]string, 0, len(manual.Commands))
	for _, c := range manual.Commands {
		documented = append(documented, c.Name)
	}
	sort.Strings(documented)
	assert.Equal(t, got, documented, "the manual must document every route")

	_, err = h.Complete(ctx, "rust", []string{""})
	require.Error(t, err)
}

func newTestZigActionRouter() (textapi.CommandManual, textapi.CommandHandler) {
	return newZigActionHandler(&captureLSP{}, &fakeEditor{}, &fakeWM{},
		newFakeNotifications(), lspcmd.NewSelectionTracker())
}
