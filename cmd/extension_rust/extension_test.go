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
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

func TestDetectRustProject(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*fakeFS)
		want  bool
	}{
		{"empty", func(*fakeFS) {}, false},
		{"cargo manifest", func(f *fakeFS) { f.addFile("Cargo.toml") }, true},
		{"rs source only", func(f *fakeFS) { f.addEntry("main.rs", false) }, true},
		{"non-rust files only", func(f *fakeFS) { f.addEntry("README.md", false) }, false},
		{"cargo dir is not a manifest", func(f *fakeFS) { f.addDir("Cargo.toml") }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeFS()
			tc.setup(fs)
			assert.Equal(t, tc.want, detectRustProject(context.Background(), fs))
		})
	}
}

func TestToolchainInstalled(t *testing.T) {
	t.Run("empty home is not installed", func(t *testing.T) {
		assert.False(t, toolchainInstalled(newFakeFS(), ""))
	})
	t.Run("no toolchains dir is not installed", func(t *testing.T) {
		fs := newFakeFS()
		assert.False(t, toolchainInstalled(fs, "/rustup"))
	})
	t.Run("empty toolchains dir is not installed", func(t *testing.T) {
		fs := newFakeFS().addReadDir("/rustup/toolchains")
		assert.False(t, toolchainInstalled(fs, "/rustup"))
	})
	t.Run("toolchain present is installed", func(t *testing.T) {
		fs := newFakeFS().addReadDir("/rustup/toolchains",
			fakeDirEntry{name: "stable-aarch64-apple-darwin", dir: true})
		assert.True(t, toolchainInstalled(fs, "/rustup"))
	})
}

func TestReadLspPath(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		p, ok := readLspPath(nil, newFakeNotifications())
		assert.False(t, ok)
		assert.Empty(t, p)
	})
	t.Run("override set", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"lsp_path": "/opt/ra/rust-analyzer"})
		p, ok := readLspPath(cfg, newFakeNotifications())
		assert.True(t, ok)
		assert.Equal(t, "/opt/ra/rust-analyzer", p)
	})
	t.Run("empty override ignored", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"lsp_path": ""})
		p, ok := readLspPath(cfg, newFakeNotifications())
		assert.False(t, ok)
		assert.Empty(t, p)
	})
}

func TestResolveRustAnalyzer(t *testing.T) {
	t.Run("lsp_path override wins", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"lsp_path": "/opt/ra"})
		fs := newFakeFS().addFile("/data/bin/rust-analyzer")
		got := resolveRustAnalyzer(context.Background(), cfg,
			newFakeNotifications(), fakeInstaller{fs: fs, root: "/data"})
		assert.Equal(t, "/opt/ra", got)
	})

	t.Run("defaults to provisioned binary", func(t *testing.T) {
		fs := newFakeFS().addFile("/data/bin/rust-analyzer")
		got := resolveRustAnalyzer(context.Background(), nil,
			newFakeNotifications(), fakeInstaller{fs: fs, root: "/data"})
		assert.Equal(t, "/data/bin/rust-analyzer", got)
	})

	t.Run("empty lsp_path uses provisioned binary", func(t *testing.T) {
		cfg := newStubConfig(map[string]string{"lsp_path": ""})
		fs := newFakeFS().addFile("/data/bin/rust-analyzer")
		got := resolveRustAnalyzer(context.Background(), cfg,
			newFakeNotifications(), fakeInstaller{fs: fs, root: "/data"})
		assert.Equal(t, "/data/bin/rust-analyzer", got)
	})

	t.Run("missing provisioned binary resolves empty", func(t *testing.T) {
		fs := newFakeFS()
		got := resolveRustAnalyzer(context.Background(), nil,
			newFakeNotifications(), fakeInstaller{fs: fs, root: "/data"})
		assert.Empty(t, got)
	})
}

func TestResolveSysroot(t *testing.T) {
	ctx := context.Background()
	rustc := "/cargo/bin/rustc"
	ex := newFakeExecutor().respond(
		rustc+" --print sysroot",
		scriptedCmd{stdout: "/home/u/.rustup/toolchains/stable\n"})
	assert.Equal(t, "/home/u/.rustup/toolchains/stable", resolveSysroot(ctx, ex, rustc))

	exFail := newFakeExecutor().respond(rustc+" --print sysroot", scriptedCmd{err: assertErr})
	assert.Empty(t, resolveSysroot(ctx, exFail, rustc))
}

func TestRustInitializeParams(t *testing.T) {
	params, err := rustInitializeParams(
		"file:///ws", "/data/bin/rust-analyzer", "/sysroot", "info", false)
	require.NoError(t, err)
	assert.Equal(t, "file:///ws", params.RootURI)

	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	assert.Equal(t, "rust", opts["langID"])
	assert.Equal(t, "/data/bin/rust-analyzer", opts["command"])
	assert.Equal(t, map[string]any{"RA_LOG": "info"}, opts["env"])
	assert.Equal(t, "/sysroot", opts["sysroot"])
	assert.Equal(t, true, opts["checkOnSave"])
	check, _ := opts["check"].(map[string]any)
	assert.Equal(t, "clippy", check["command"])

	noSysroot, err := rustInitializeParams("file:///ws", "ra", "", "info", false)
	require.NoError(t, err)
	var opts2 map[string]any
	require.NoError(t, json.Unmarshal(noSysroot.InitializeOptions, &opts2))
	_, hasSysroot := opts2["sysroot"]
	assert.False(t, hasSysroot)
}

// Cache priming defaults to one worker per physical core, which is the
// burst that saturates the machine on a cold project and leaves the
// editor's render loop without a core to run on.
func TestRustInitializeParamsCapsWorkerThreads(t *testing.T) {
	params, err := rustInitializeParams("file:///ws", "ra", "", "info", false)
	require.NoError(t, err)

	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))

	want := float64(max(1, runtime.NumCPU()/2))
	assert.Equal(t, want, opts["numThreads"])
	priming, _ := opts["cachePriming"].(map[string]any)
	assert.Equal(t, want, priming["numThreads"],
		"0 would mean 'pick automatically' and restore the default pool")
	assert.Positive(t, want, "a zero cap would restore the default pool size")
}

func TestRustLogFilter(t *testing.T) {
	assert.Equal(t, "info", rustLogFilter(nil, newFakeNotifications()))

	cfg := config.JSONFromMap(map[string]any{
		"debug": map[string]any{"log_level": "rust_analyzer=debug"},
	})
	assert.Equal(t, "rust_analyzer=debug",
		rustLogFilter(cfg, newFakeNotifications()))

	invalid := config.JSONFromMap(map[string]any{
		"debug": map[string]any{"log_level": "info\nEVIL=1"},
	})
	notify := newFakeNotifications()
	assert.Equal(t, "info", rustLogFilter(invalid, notify))
	assert.NotEmpty(t, notify.notifs)
}

func TestReadMemoryUsage(t *testing.T) {
	assert.False(t, readMemoryUsage(nil, newFakeNotifications()))
	assert.False(t, readMemoryUsage(
		config.JSONFromMap(map[string]any{}), newFakeNotifications()))

	enabled := config.JSONFromMap(map[string]any{
		"debug": map[string]any{"memory_usage": true},
	})
	assert.True(t, readMemoryUsage(enabled, newFakeNotifications()))

	invalid := config.JSONFromMap(map[string]any{
		"debug": map[string]any{"memory_usage": "yes"},
	})
	notify := newFakeNotifications()
	assert.False(t, readMemoryUsage(invalid, notify))
	assert.NotEmpty(t, notify.notifs)
}

// TestRustInitializeOptionsExtras guards the initialization options we
// forward to rust-analyzer beyond the baseline: import shaping so
// organize-imports and auto-import assists produce idiomatic use trees,
// assist.emitMustUse, autoimport completion, and lens suppression.
func TestRustInitializeOptionsExtras(t *testing.T) {
	params, err := rustInitializeParams("file:///ws", "ra", "", "info", false)
	require.NoError(t, err)
	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))

	assist, _ := opts["assist"].(map[string]any)
	assert.Equal(t, true, assist["emitMustUse"])

	imports, _ := opts["imports"].(map[string]any)
	granularity, _ := imports["granularity"].(map[string]any)
	assert.Equal(t, "module", granularity["group"])
	assert.Equal(t, "crate", imports["prefix"])

	completion, _ := opts["completion"].(map[string]any)
	autoimport, _ := completion["autoimport"].(map[string]any)
	assert.Equal(t, true, autoimport["enable"])

	lens, _ := opts["lens"].(map[string]any)
	assert.Equal(t, false, lens["enable"])
}

// TestRustInitializeCapabilities verifies the experimental capabilities we
// advertise. snippetTextEdit must NOT be advertised: lspcmd.ApplyWorkspaceEdit
// writes edits verbatim, so a snippet edit would leak literal $0/${1:_} tab
// stops into the buffer. codeAction.resolveSupport must stay absent so
// rust-analyzer resolves each assist's edit eagerly in the codeAction response.
func TestRustInitializeCapabilities(t *testing.T) {
	params, err := rustInitializeParams("file:///ws", "ra", "", "info", false)
	require.NoError(t, err)
	var caps map[string]any
	require.NoError(t, json.Unmarshal(params.Capabilities, &caps))

	experimental, _ := caps["experimental"].(map[string]any)
	_, hasSnippet := experimental["snippetTextEdit"]
	assert.False(t, hasSnippet, "snippetTextEdit must not be advertised; snippets leak into the buffer")
	assert.Equal(t, true, experimental["codeActionGroup"])
	_, hasLocalDocs := experimental["localDocs"]
	assert.False(t, hasLocalDocs, "localDocs is gated on the experimental config flag")
	_, hasHoverActions := experimental["hoverActions"]
	assert.False(t, hasHoverActions, "hoverActions is gated on the experimental config flag")
	_, hasCommands := experimental["commands"]
	assert.False(t, hasCommands, "commands is gated on the experimental config flag")

	textDocument, _ := caps["textDocument"].(map[string]any)
	codeAction, _ := textDocument["codeAction"].(map[string]any)
	_, hasResolve := codeAction["resolveSupport"]
	assert.False(t, hasResolve, "resolveSupport must stay absent to keep edits eager")

	hover, _ := textDocument["hover"].(map[string]any)
	assert.Contains(t, hover["contentFormat"], "markdown",
		"hover must request markdown so `rust hover` can render it")

	// rust-analyzer gates native semantic diagnostics on the client
	// advertising pull support once build scripts and proc macros are
	// enabled; without this Rune only ever sees clippy flycheck output,
	// which is a different diagnostic set and only runs on save
	// (RUNE-332).
	_, hasDiagnostic := textDocument["diagnostic"]
	assert.True(t, hasDiagnostic,
		"textDocument.diagnostic must be advertised so rust-analyzer "+
			"computes native semantic diagnostics")
}

// With the experimental flag set, localDocs is advertised so external-docs
// receives a {web, local} response; the always-on flags stay set.
func TestRustInitializeCapabilitiesExperimental(t *testing.T) {
	params, err := rustInitializeParams("file:///ws", "ra", "", "info", true)
	require.NoError(t, err)
	var caps map[string]any
	require.NoError(t, json.Unmarshal(params.Capabilities, &caps))

	experimental, _ := caps["experimental"].(map[string]any)
	assert.Equal(t, true, experimental["localDocs"])
	assert.Equal(t, true, experimental["codeActionGroup"])
	assert.Equal(t, true, experimental["serverStatusNotification"])
	_, hasSnippet := experimental["snippetTextEdit"]
	assert.False(t, hasSnippet)

	// hoverActions + the client command list are what unlock hover actions.
	assert.Equal(t, true, experimental["hoverActions"])
	commands, _ := experimental["commands"].(map[string]any)
	require.NotNil(t, commands, "experimental.commands must be advertised with the flag")
	names, _ := commands["commands"].([]any)
	for _, want := range []string{
		"rust-analyzer.runSingle", "rust-analyzer.debugSingle",
		"rust-analyzer.showReferences", "rust-analyzer.gotoLocation",
	} {
		assert.Contains(t, names, want)
	}
	// rename and triggerParameterHints are deliberately not advertised: this
	// client cannot service them locally, and advertising rename makes
	// rust-analyzer attach it to assists like "Extract into variable", which
	// codeaction.go would forward to the server as an unknown request (see
	// TestE2E/ExtractVariable).
	assert.NotContains(t, names, "rust-analyzer.rename")
	assert.NotContains(t, names, "rust-analyzer.triggerParameterHints")
	assert.Len(t, names, 4)
}

// rustInitializeCommandHasNoSpaces guards the idelsp command tokenizer,
// which splits InitializeOptions.command on spaces. A bundled path with
// no subcommand keeps the command a single argv element.
func TestRustInitializeCommandHasNoSpaces(t *testing.T) {
	params, err := rustInitializeParams(
		"file:///ws", "/data/bin/rust-analyzer", "", "info", false)
	require.NoError(t, err)
	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	assert.NotContains(t, opts["command"], " ")
}

func TestBootstrapRustupInstallsWhenAbsent(t *testing.T) {
	ctx := context.Background()
	fs := newFakeFS()
	ex := newFakeExecutor()
	notify := newFakeNotifications()

	require.NoError(t, bootstrapRustup(
		ctx, "rustup-init", ex, notify, fs, "/rustup", "/ws"))

	calls := ex.callsSnapshot()
	assert.Contains(t, calls, "rustup-init -y --no-modify-path "+
		"--default-toolchain stable --profile minimal -c rust-src,clippy,rustfmt")

	msgs := notify.progressMessages()
	require.NotEmpty(t, msgs)
	assert.Equal(t, "Rust toolchain ready", msgs[len(msgs)-1])
	assertMonotonicProgress(t, notify)
}

func TestBootstrapRustupSkipsWhenInstalled(t *testing.T) {
	ctx := context.Background()
	fs := newFakeFS().addReadDir("/rustup/toolchains",
		fakeDirEntry{name: "stable-x86_64", dir: true})
	ex := newFakeExecutor()
	notify := newFakeNotifications()

	require.NoError(t, bootstrapRustup(
		ctx, "rustup-init", ex, notify, fs, "/rustup", "/ws"))

	calls := ex.callsSnapshot()
	for _, c := range calls {
		assert.NotContains(t, c, "rustup-init")
	}
	assert.Empty(t, notify.progressMessages())
}

// TestExtendWorkspaceNonRustRegistersButSkipsInit verifies the REPL
// command is always registered (its cwd is the workspace root and is
// independent of any project), while a workspace with no Rust project is
// not eagerly initialized.
func TestExtendWorkspaceNonRustRegistersButSkipsInit(t *testing.T) {
	fs := newFakeFS()
	lsp := &captureLSP{}
	ext := &rustExtension{}
	registered := false
	err := ext.extendWorkspaceWith(context.Background(),
		fs, newFakeExecutor(), newFakeNotifications(), lsp, &fakeEditor{},
		&fakeWM{}, nil, nil, nil, fakeInstaller{fs: fs, root: "/data"},
		"/rustup", "/cargo", nil,
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

// TestExtendWorkspaceWithoutCargoHome verifies that when CARGO_HOME is
// unset the extension does not fail: it skips the toolchain install (so
// nothing lands in the wrong place), warns the user, and still brings up
// rust-analyzer with no sysroot.
func TestExtendWorkspaceWithoutCargoHome(t *testing.T) {
	fs := newFakeFS().
		addFile("Cargo.toml").
		addFile("/data/bin/rust-analyzer")
	ex := newFakeExecutor()
	notify := newFakeNotifications()
	lsp := &captureLSP{}

	ext := &rustExtension{}
	err := ext.extendWorkspaceWith(context.Background(),
		fs, ex, notify, lsp, &fakeEditor{}, &fakeWM{}, nil, nil, nil,
		fakeInstaller{fs: fs, root: "/data"}, "/rustup", "", nil,
		func(textapi.CommandManual, textapi.REPLHandler) error { return nil },
		func(textapi.CommandManual, textapi.CommandHandler) error { return nil })
	require.NoError(t, err)

	// No rustup-init/rustc is executed, so nothing is installed anywhere.
	assert.Empty(t, ex.callsSnapshot())
	assert.Contains(t, notify.notifMessages(),
		"CARGO_HOME is not set; continuing without a managed Rust toolchain")

	params, count := lsp.captured()
	require.Equal(t, 1, count)
	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	_, hasSysroot := opts["sysroot"]
	assert.False(t, hasSysroot, "no sysroot without a managed toolchain")
}

// TestExtendWorkspaceNestedDiscovery verifies that a workspace with no
// root Cargo.toml is not initialized on startup, but opening a .rs file
// under a nested crate brings up a server rooted at that crate. A
// marker-less .rs open is ignored.
func TestExtendWorkspaceNestedDiscovery(t *testing.T) {
	root := t.TempDir()
	crate := filepath.Join(root, "crates", "foo")
	require.NoError(t, os.MkdirAll(crate, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(crate, "Cargo.toml"), []byte("[package]\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(crate, "lib.rs"), []byte("fn main() {}\n"), 0o644))

	stray := filepath.Join(root, "stray")
	require.NoError(t, os.MkdirAll(stray, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stray, "loose.rs"), []byte("fn x() {}\n"), 0o644))

	fs := realFS{root: root}
	ex := newFakeExecutor().respond(
		"/cargo/bin/rustc --print sysroot", scriptedCmd{stdout: "/sysroot\n"})
	lsp := &captureLSP{}
	editor := &fakeEditor{}
	ext := &rustExtension{}

	err := ext.extendWorkspaceWith(context.Background(),
		fs, ex, newFakeNotifications(), lsp, editor,
		&fakeWM{}, nil, nil, nil, fakeInstaller{fs: fs, root: "/data"},
		"/rustup", "/cargo", nil,
		func(textapi.CommandManual, textapi.REPLHandler) error { return nil },
		func(textapi.CommandManual, textapi.CommandHandler) error { return nil })
	require.NoError(t, err)

	// No root Cargo.toml, so nothing is initialized on startup.
	_, count := lsp.captured()
	require.Zero(t, count)

	// A .rs with no enclosing Cargo.toml must not spawn a server.
	editor.open(t, filepath.Join(stray, "loose.rs"))

	// Opening the nested crate's source initializes a server rooted there.
	editor.open(t, filepath.Join(crate, "lib.rs"))
	lsp.waitForInit(t, 5*time.Second)

	params, count := lsp.captured()
	require.Equal(t, 1, count, "only the marked nested crate must initialize")
	assert.Equal(t, "file://"+crate, params.RootURI)
}

func TestExtendWorkspaceRegistersAndInitializes(t *testing.T) {
	fs := newFakeFS().
		addFile("Cargo.toml").
		addFile("/data/bin/rust-analyzer").
		addReadDir("/rustup/toolchains",
			fakeDirEntry{name: "stable-x86_64", dir: true})
	ex := newFakeExecutor().respond(
		"/cargo/bin/rustc --print sysroot", scriptedCmd{stdout: "/sysroot\n"})
	lsp := &captureLSP{}
	notify := newFakeNotifications()

	var manuals []textapi.CommandManual
	var cmds []textapi.CommandManual
	ext := &rustExtension{}
	err := ext.extendWorkspaceWith(context.Background(),
		fs, ex, notify, lsp, &fakeEditor{}, &fakeWM{},
		nil, nil, nil, fakeInstaller{fs: fs, root: "/data"}, "/rustup", "/cargo", nil,
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
	assert.Equal(t, rustCommandName, manuals[0].Name)
	require.Len(t, cmds, 1)
	assert.Equal(t, actionCmdName, cmds[0].Name)

	params, count := lsp.captured()
	require.Equal(t, 1, count)
	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	assert.Equal(t, "/data/bin/rust-analyzer", opts["command"])
	assert.Equal(t, "/sysroot", opts["sysroot"])
}

func TestRustHandlerUnknownCommand(t *testing.T) {
	_, h := newRustHandler(newFakeExecutor(), newFakeNotifications(), "/ws", "rustup", nil)
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "rust", Args: []string{"frobnicate"}}, repl.NopProgressWriter())
	require.Error(t, err)
}

func TestRustHandlerForwardsToRustup(t *testing.T) {
	// The handler must drive the installation-owned proxy by absolute
	// path, not a bare `rustup` that could resolve to a system install.
	proxy := "/cargo/bin/rustup"
	ex := newFakeExecutor().respond(proxy+" show", scriptedCmd{stdout: "stable (default)"})
	_, h := newRustHandler(ex, newFakeNotifications(), "/ws", proxy, nil)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "rust", Args: []string{"show"}}, repl.NopProgressWriter())
	require.NoError(t, err)
	out, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Len(t, out, 1)
	assert.Contains(t, ex.callsSnapshot(), proxy+" show")
}

func TestRustHandlerReloadInvokesCallback(t *testing.T) {
	reloaded := false
	_, h := newRustHandler(newFakeExecutor(), newFakeNotifications(), "/ws", "rustup",
		func(context.Context) error { reloaded = true; return nil })
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "rust", Args: []string{"reload"}}, repl.NopProgressWriter())
	require.NoError(t, err)
	assert.True(t, reloaded)
}

func TestRustHandlerDefaultTriggersReload(t *testing.T) {
	reloaded := false
	ex := newFakeExecutor().respond("rustup default nightly", scriptedCmd{stdout: "ok"})
	_, h := newRustHandler(ex, newFakeNotifications(), "/ws", "rustup",
		func(context.Context) error { reloaded = true; return nil })
	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "rust", Args: []string{"default", "nightly"}}, repl.NopProgressWriter())
	require.NoError(t, err)
	assert.True(t, reloaded)
}

func TestRustHandlerComplete(t *testing.T) {
	_, h := newRustHandler(newFakeExecutor(), newFakeNotifications(), "/ws", "rustup", nil)
	ctx := context.Background()

	it, err := h.Complete(ctx, "", []string{"to"})
	require.NoError(t, err)
	got, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Equal(t, []string{"toolchain"}, got)

	it, err = h.Complete(ctx, "", []string{"component", "a"})
	require.NoError(t, err)
	got, err = iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Equal(t, []string{"add"}, got)
}
