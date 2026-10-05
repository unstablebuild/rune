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

//go:build e2e

package debugshell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idedebug"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

// rustAdapterConfig builds the DAP adapter registry for lldb-dap. The
// transport is the verified contract: lldb-dap's --connection accepts
// `listen://[host]:port` or `accept:///path` (per lldb-dap's Options.td)
// — there is no `connect://` scheme. Because idedebug binds a TCP
// listener and dials the adapter (server.go dialWithRetry), lldb-dap
// must LISTEN on the bound {addr}, so the URI is `listen://{addr}`.
// The launch template holds only static keys: Manager.Launch injects
// program/args/env/cwd/stopOnEntry with their proper JSON types, and
// dangling {placeholder} strings would make lldb-dap reject the
// launch without ever emitting the initialized event.
func rustAdapterConfig(lldbDapBin string) idedebug.AdapterConfig {
	return idedebug.AdapterConfig{
		Command:   []string{lldbDapBin, "--connection", "listen://{addr}"},
		AdapterID: "lldb-dap",
		LaunchArgs: map[string]string{
			"request": "launch",
			"type":    "lldb-dap",
		},
		AttachArgs: map[string]string{
			"request": "attach",
			"type":    "lldb-dap",
			"program": "{program}",
			"pid":     "{pid}",
		},
	}
}

// rustPkgManager satisfies idedebug.PkgManager and syntax.PkgManager for
// the Rust e2e harness: it returns the lldb-dap binary plus, for the
// "rust" language id, the tree-sitter grammar files so the parser-driven
// breakpoint/variables features run exactly as they do for Go.
type rustPkgManager struct {
	bin     string
	grammar string
}

func (p *rustPkgManager) LibDir(
	_ context.Context, langID string,
) (iterator.Iterator[string], error) {
	files := []string{p.bin}
	if p.grammar != "" && langID == "rust" {
		entries, err := os.ReadDir(p.grammar)
		if err == nil {
			for _, e := range entries {
				files = append(files, filepath.Join(p.grammar, e.Name()))
			}
		}
	}
	return iterator.FromSlice(files), nil
}

// findLldbDap returns the path to the lldb-dap binary, probing the
// rust language package first and then the common names on PATH, or
// skips the test when none is installed. The bundled adapter is
// preferred because production resolves it by package path and its
// @rpath dylibs live next to it; stale copies on PATH may not have
// them. lldb-dap is the M3 deferred dependency, so without it the
// suite self-skips and CI stays green while the config above still
// documents the contract.
func findLldbDap(t *testing.T) string {
	t.Helper()
	base := filepath.Join(os.Getenv("HOME"), ".rune", "pkg", "rust")
	if versions, err := os.ReadDir(base); err == nil {
		for _, v := range versions {
			if !v.IsDir() {
				continue
			}
			bin := filepath.Join(base, v.Name(), "bin", "lldb-dap")
			if _, err := os.Stat(bin); err == nil {
				return bin
			}
		}
	}
	for _, name := range []string{"lldb-dap", "lldb-vscode"} {
		if bin, err := exec.LookPath(name); err == nil {
			return bin
		}
	}
	t.Skip("lldb-dap not found, skipping rust debugger e2e test")
	return ""
}

// rustGrammarDir locates the installed Rust tree-sitter grammar dir
// shipped by the rust language package under ~/.rune/pkg/rust/<ver>/lib.
// It skips the test when no such grammar is installed.
func rustGrammarDir(t *testing.T) string {
	t.Helper()
	base := filepath.Join(os.Getenv("HOME"), ".rune", "pkg", "rust")
	versions, err := os.ReadDir(base)
	if err != nil {
		t.Skipf("rust grammar package not found at %s: %v", base, err)
	}
	for _, v := range versions {
		if !v.IsDir() {
			continue
		}
		lib := filepath.Join(base, v.Name(), "lib")
		if _, err := os.Stat(filepath.Join(lib, hostParserRel())); err == nil {
			return lib
		}
	}
	t.Skipf("no rust tree-sitter.so under %s", base)
	return ""
}

// setupBuggyRs copies testdata/buggy_rs to a fresh temp dir, resolves
// symlinks (so the adapter's reported source path matches breakpoints),
// and cargo-builds the debug binary lldb-dap will launch. It returns the
// workspace dir and the built binary path. cargo build is required
// because lldb-dap debugs a compiled executable, unlike dlv (which
// compiles) or debugpy (which runs source).
func setupBuggyRs(t *testing.T) (dir, binPath string) {
	t.Helper()
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not found, skipping rust debugger e2e test")
	}
	tmp, err := os.MkdirTemp("", "debugshell-rs-e2e-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}
	src := "testdata/buggy_rs"
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(tmp, e.Name()), data, 0o644))
	}

	build := exec.Command("cargo", "build")
	build.Dir = tmp
	out, err := build.CombinedOutput()
	require.NoError(t, err, "cargo build failed: %s", out)

	binPath = filepath.Join(tmp, "target", "debug", "buggy-rs")
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("built binary not found at %s: %v", binPath, err)
	}
	return tmp, binPath
}

// newRustE2EHarness mirrors newPyE2EHarness but targets the lldb-dap
// adapter, wiring the real Rust tree-sitter grammar so the parser-driven
// breakpoint and variables-location features behave as they do for Go.
func newRustE2EHarness(t *testing.T, lldbDapBin, dir string) *e2eHarness {
	t.Helper()
	scheme := newLocalScheme()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	pkg := &rustPkgManager{bin: lldbDapBin, grammar: rustGrammarDir(t)}
	dapCfg := idedebug.Config{
		MaxRetries:        1,
		InitializeTimeout: 30 * time.Second,
		Adapters:          map[string]idedebug.AdapterConfig{"rust": rustAdapterConfig(lldbDapBin)},
	}
	mgr := idedebug.New(uri, scheme, pkg, dapCfg)

	br := newFakeBrowser()
	tile := &fakeWindow{id: 1, content: &texttest.TestEditorHandler{}}
	shellW := &fakeWindow{id: 2, content: newFakeShellHandler()}
	br.windows = []*fakeWindow{tile, shellW}
	ed := newFakeTextapiEditor()
	mainBytes, err := os.ReadFile(filepath.Join(dir, "main.rs"))
	require.NoError(t, err)
	ed.cellView = &fakeCellView{cells: cellsFromString(string(mainBytes))}

	fs, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fs.Close() })
	parser := syntax.NewParser(fs, pkg, uri)
	h := New(mgr, br, ed, parser, fs, Config{
		WorkspaceURI: uri,
		Debugger:     dapCfg,
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)

	hh := &e2eHarness{
		t:      t,
		ctx:    ctx,
		cancel: cancel,
		scheme: scheme,
		mgr:    mgr,
		h:      h,
		br:     br,
		ed:     ed,
	}
	hh.cond = sync.NewCond(&hh.mu)
	h.WithNotify(hh.notify)
	return hh
}

// rsFirstStmtLine is the 1-based line of `let mut total = 0;` inside
// sum_to in testdata/buggy_rs/main.rs, computed at runtime so edits to
// the fixture do not break the breakpoint target.
func rsFirstStmtLine(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "let mut total = 0;" {
			return i + 1
		}
	}
	t.Fatalf("could not find 'let mut total = 0;' in %s", path)
	return 0
}

func TestE2E_Rust_Launch(t *testing.T) {
	t.Parallel()
	lldbDapBin := findLldbDap(t)
	tmpDir, binPath := setupBuggyRs(t)
	mainPath := filepath.Join(tmpDir, "main.rs")

	h := newRustE2EHarness(t, lldbDapBin, tmpDir)
	defer h.close()
	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "rust")
	require.NoError(t, err)
	go h.drainIterator(it)

	// lldb-dap launches the compiled binary, not the source dir.
	_, err = h.run(ctx, subLaunch, binPath)
	require.NoError(t, err)

	h.waitMilestone(t, "initialized", 30*time.Second)

	bpLine := rsFirstStmtLine(t, mainPath)
	h.setBreakpoint(t, mainPath, bpLine)

	_, err = h.run(ctx, subConfigured)
	require.NoError(t, err)
	h.waitMilestone(t, "stopped", 30*time.Second)

	threadID := h.firstThreadID(t)
	stack := h.stackTrace(t, threadID)
	require.NotEmpty(t, stack)
	assert.Contains(t, stack[0].Name, "sum_to",
		"expected to be stopped inside sum_to, got %s", stack[0].Name)
	assert.Equal(t, bpLine, stack[0].Line)
	require.NotNil(t, stack[0].Source)
	assert.Equal(t, mainPath, stack[0].Source.Path)

	outPath := h.outputPath(t)
	require.NotEmpty(t, outPath, "output capture file path")
	milestoneStart := h.milestoneCount()
	h.continueUntilExit(t, threadID, 30*time.Second)
	h.waitMilestoneAfter(t, milestoneStart, "debuggee exited", 30*time.Second)
	_, _ = h.run(ctx, subTerminate)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Sum: 21",
		"expected debuggee stdout in capture file, got %q", string(data))
}

func TestE2E_Rust_BreakpointOnEmptyLine(t *testing.T) {
	t.Parallel()
	lldbDapBin := findLldbDap(t)
	tmpDir, binPath := setupBuggyRs(t)
	mainPath := filepath.Join(tmpDir, "main.rs")

	h := newRustE2EHarness(t, lldbDapBin, tmpDir)
	defer h.close()
	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "rust")
	require.NoError(t, err)
	go h.drainIterator(it)

	_, err = h.run(ctx, subLaunch, binPath)
	require.NoError(t, err)
	h.waitMilestone(t, "initialized", 30*time.Second)

	firstStmt := rsFirstStmtLine(t, mainPath)
	emptyLine := rsBlankLineNear(t, mainPath, firstStmt+1)
	hndl, err := h.runPromptOnHandler(ctx, mainPath,
		term.Coordinates{Y: emptyLine - 1}, subSetBreakpoint)
	require.NoError(t, err)
	require.NotNil(t, hndl.LocationList)
	_, ok := hndl.LocationList.Current()
	require.True(t, ok, "no breakpoint location installed")
}

// rsBlankLineNear returns a 1-based blank-line number at or after start
// (1-based) in path. Fails the test if none.
func rsBlankLineNear(t *testing.T, path string, start int) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	for i := start - 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			return i + 1
		}
	}
	require.Fail(t, "no blank line after start", "path=%s start=%d", path, start)
	return 0
}
