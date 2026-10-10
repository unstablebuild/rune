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
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idedebug"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

// zigAdapterConfig reuses the lldb-dap transport contract documented on
// rustAdapterConfig: zig binaries carry DWARF like rust ones and are
// debugged by the same adapter with the same launch template.
func zigAdapterConfig(lldbDapBin string) idedebug.AdapterConfig {
	return rustAdapterConfig(lldbDapBin)
}

// zigPkgManager satisfies idedebug.PkgManager and syntax.PkgManager for
// the Zig e2e harness: it returns the lldb-dap binary plus, for the
// "zig" language id, the tree-sitter grammar files so the parser-driven
// breakpoint/variables features run exactly as they do for Go and Rust.
type zigPkgManager struct {
	bin     string
	grammar string
}

func (p *zigPkgManager) LibDir(
	_ context.Context, langID string,
) (iterator.Iterator[string], error) {
	files := []string{p.bin}
	if p.grammar != "" && langID == "zig" {
		entries, err := os.ReadDir(p.grammar)
		if err == nil {
			for _, e := range entries {
				files = append(files, filepath.Join(p.grammar, e.Name()))
			}
		}
	}
	return iterator.FromSlice(files), nil
}

// findZig returns the zig compiler, or skips the test when it is not
// installed. Unlike lldb-dap there is no bundled copy to prefer: the
// system toolchain builds the fixture binary the adapter launches.
func findZig(t *testing.T) string {
	t.Helper()
	if bin, err := exec.LookPath("zig"); err == nil {
		return bin
	}
	for _, p := range []string{
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "zig"),
		"/opt/homebrew/bin/zig",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("zig not found, skipping zig debugger e2e test")
	return ""
}

// zigGrammarDir returns the committed Zig tree-sitter grammar fixture
// shared with the syntax test suites, so the parser-driven breakpoint
// and variables features never self-skip.
func zigGrammarDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	dir := filepath.Join(wd, "..", "..", "..", "syntax", "syntaxtest", "zig")
	if _, err := os.Stat(filepath.Join(dir, hostParserRel())); err != nil {
		t.Fatalf("missing zig tree-sitter fixture in %s: %v", dir, err)
	}
	return dir
}

// setupBuggyZig copies testdata/buggy_zig to a fresh temp dir, resolves
// symlinks (so the adapter's reported source path matches breakpoints),
// and builds the debug binary lldb-dap will launch with `zig build-exe`
// (Debug mode by default, so DWARF is emitted). It returns the
// workspace dir and the built binary path.
func setupBuggyZig(t *testing.T) (dir, binPath string) {
	t.Helper()
	zigBin := findZig(t)
	tmp, err := os.MkdirTemp("", "debugshell-zig-e2e-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}
	src := "testdata/buggy_zig"
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(tmp, e.Name()), data, 0o644))
	}

	build := exec.Command(zigBin, "build-exe", "-femit-bin=buggy_zig", "main.zig")
	build.Dir = tmp
	out, err := build.CombinedOutput()
	require.NoError(t, err, "zig build-exe failed: %s", out)

	binPath = filepath.Join(tmp, "buggy_zig")
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("built binary not found at %s: %v", binPath, err)
	}
	return tmp, binPath
}

// newZigE2EHarness mirrors newRustE2EHarness but registers the adapter
// under the "zig" language id and wires the committed Zig tree-sitter
// grammar so the parser-driven breakpoint and variables-location
// features behave as they do for Go and Rust.
func newZigE2EHarness(t *testing.T, lldbDapBin, dir string) *e2eHarness {
	t.Helper()
	scheme := newLocalScheme()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	pkg := &zigPkgManager{bin: lldbDapBin, grammar: zigGrammarDir(t)}
	dapCfg := idedebug.Config{
		MaxRetries:        1,
		InitializeTimeout: 30 * time.Second,
		Adapters:          map[string]idedebug.AdapterConfig{"zig": zigAdapterConfig(lldbDapBin)},
	}
	mgr := idedebug.New(uri, scheme, pkg, dapCfg)

	br := newFakeBrowser()
	tile := &fakeWindow{id: 1, content: &texttest.TestEditorHandler{}}
	shellW := &fakeWindow{id: 2, content: newFakeShellHandler()}
	br.windows = []*fakeWindow{tile, shellW}
	ed := newFakeTextapiEditor()
	mainBytes, err := os.ReadFile(filepath.Join(dir, "main.zig"))
	require.NoError(t, err)
	ed.cellView = &fakeCellView{cells: cellsFromString(string(mainBytes))}

	fs, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fs.Close() })
	parser := treesitter.NewParser(fs, pkg, uri)
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

// zigFirstStmtLine is the 1-based line of `var total: i32 = 0;` inside
// sum_to in testdata/buggy_zig/main.zig, computed at runtime so edits
// to the fixture do not break the breakpoint target. The `other`
// sibling initializes its shadowed local to 1, so the match is unique
// to sum_to.
func zigFirstStmtLine(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "var total: i32 = 0;" {
			return i + 1
		}
	}
	t.Fatalf("could not find 'var total: i32 = 0;' in %s", path)
	return 0
}

// zigFunctionLineRange returns the 0-based [start, end] line range of
// the named top-level `fn` in path, used to assert that variable
// locations in a sibling function are filtered out.
func zigFunctionLineRange(t *testing.T, path, name string) (int, int) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	start := -1
	depth := 0
	for i, line := range lines {
		if start < 0 {
			if strings.HasPrefix(line, "fn "+name+"(") ||
				strings.HasPrefix(line, "pub fn "+name+"(") {
				start = i
			} else {
				continue
			}
		}
		depth += strings.Count(line, "{")
		depth -= strings.Count(line, "}")
		if start >= 0 && depth == 0 && i > start {
			return start, i
		}
	}
	require.Fail(t, "function not found",
		"could not locate fn %q in %s", name, path)
	return 0, 0
}

func TestE2E_Zig_Launch(t *testing.T) {
	t.Parallel()
	lldbDapBin := findLldbDap(t)
	tmpDir, binPath := setupBuggyZig(t)
	mainPath := filepath.Join(tmpDir, "main.zig")

	h := newZigE2EHarness(t, lldbDapBin, tmpDir)
	defer h.close()
	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "zig")
	require.NoError(t, err)
	go h.drainIterator(it)

	// lldb-dap launches the compiled binary, not the source dir.
	_, err = h.run(ctx, subLaunch, binPath)
	require.NoError(t, err)

	h.waitMilestone(t, "initialized", 30*time.Second)

	bpLine := zigFirstStmtLine(t, mainPath)
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

	// The stopped location list must be installed with the stack trace
	// as Message.
	h.waitLocation(t, stoppedLocationID, 5*time.Second)
	stopLoc := h.findLocation(t, stoppedLocationID)
	require.True(t, stopLoc.To.X > 1,
		"expected stopped location to span the line, got To.X=%d", stopLoc.To.X)
	assert.Contains(t, stopLoc.Message, "sum_to",
		"expected stack-trace message, got %q", stopLoc.Message)

	// The variables location list must be installed with values for the
	// stopped scope only: `other` reuses the same local names, so any
	// highlight landing there means the zig grammar scopes were not
	// honored.
	h.waitLocation(t, variablesLocationID, 5*time.Second)
	varLoc := h.findLocation(t, variablesLocationID)
	assert.True(t, varLoc.To.X > varLoc.From.X,
		"expected variable name range, got %v..%v", varLoc.From, varLoc.To)
	assert.NotEmpty(t, varLoc.Message,
		"expected variable value as Message")
	assert.Equal(t, textapi.LocationPriorityCritical,
		h.locationPrio(t, variablesLocationID),
		"variables list must be critical priority to sit on top of stopped")
	otherStart, otherEnd := zigFunctionLineRange(t, mainPath, "other")
	require.Greater(t, otherEnd, otherStart, "other range")
	for _, loc := range h.allLocations(t, variablesLocationID) {
		assert.False(t, loc.From.Y >= otherStart && loc.From.Y <= otherEnd,
			"variable highlight must not fall in other (line %d) — "+
				"got %s in scope of sibling at lines %d..%d",
			loc.From.Y+1, loc.Message, otherStart+1, otherEnd+1)
	}

	// Evaluate an arbitrary expression in the stopped (sum_to) frame.
	evIt, err := h.run(ctx, subEvaluate, "n+1")
	require.NoError(t, err)
	require.NotNil(t, evIt)
	defer evIt.Close()

	outPath := h.outputPath(t)
	require.NotEmpty(t, outPath, "output capture file path")
	milestoneStart := h.milestoneCount()
	h.continueUntilExit(t, threadID, 30*time.Second)
	h.waitMilestoneAfter(t, milestoneStart, "debuggee exited", 30*time.Second)
	_, _ = h.run(ctx, subTerminate)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Sum: 21",
		"expected debuggee output in capture file, got %q", string(data))
}

func TestE2E_Zig_BreakpointOnEmptyLine(t *testing.T) {
	t.Parallel()
	lldbDapBin := findLldbDap(t)
	tmpDir, binPath := setupBuggyZig(t)
	mainPath := filepath.Join(tmpDir, "main.zig")

	h := newZigE2EHarness(t, lldbDapBin, tmpDir)
	defer h.close()
	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "zig")
	require.NoError(t, err)
	go h.drainIterator(it)

	_, err = h.run(ctx, subLaunch, binPath)
	require.NoError(t, err)
	h.waitMilestone(t, "initialized", 30*time.Second)

	firstStmt := zigFirstStmtLine(t, mainPath)
	emptyLine := rsBlankLineNear(t, mainPath, firstStmt+1)
	hndl, err := h.runPromptOnHandler(ctx, mainPath,
		term.Coordinates{Y: emptyLine - 1}, subSetBreakpoint)
	require.NoError(t, err)
	require.NotNil(t, hndl.LocationList)
	_, ok := hndl.LocationList.Current()
	require.True(t, ok, "no breakpoint location installed")
}
