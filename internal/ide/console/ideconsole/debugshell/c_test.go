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

	"github.com/google/go-dap"
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

// ccppAdapterConfig reuses the lldb-dap transport contract documented on
// rustAdapterConfig: C and C++ binaries built with -g carry DWARF and are
// debugged by the same adapter with the same launch template.
func ccppAdapterConfig(lldbDapBin string) idedebug.AdapterConfig {
	return rustAdapterConfig(lldbDapBin)
}

// ccppPkgManager satisfies idedebug.PkgManager and syntax.PkgManager for
// the C and C++ e2e harnesses: it returns the lldb-dap binary plus, for
// the language id it was built for, the tree-sitter grammar files so the
// parser-driven breakpoint/variables features run as they do for Go.
type ccppPkgManager struct {
	bin     string
	langID  string
	grammar string
}

func (p *ccppPkgManager) LibDir(
	_ context.Context, langID string,
) (iterator.Iterator[string], error) {
	files := []string{p.bin}
	if p.grammar != "" && langID == p.langID {
		entries, err := os.ReadDir(p.grammar)
		if err == nil {
			for _, e := range entries {
				files = append(files, filepath.Join(p.grammar, e.Name()))
			}
		}
	}
	return iterator.FromSlice(files), nil
}

// findCompiler returns the first of the named compilers on PATH, or skips
// the test when none is installed. Unlike lldb-dap there is no bundled
// copy to prefer: the system toolchain builds the fixture binary the
// adapter launches.
func findCompiler(t *testing.T, names ...string) string {
	t.Helper()
	for _, name := range names {
		if bin, err := exec.LookPath(name); err == nil {
			return bin
		}
	}
	t.Skipf("none of %v found, skipping debugger e2e test", names)
	return ""
}

// ccppGrammarDir returns the committed tree-sitter grammar fixture for
// langID shared with the syntax test suites, so the parser-driven
// breakpoint and variables features never self-skip.
func ccppGrammarDir(t *testing.T, langID string) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	dir := filepath.Join(wd, "..", "..", "..", "syntax", "syntaxtest", langID)
	if _, err := os.Stat(filepath.Join(dir, hostParserRel())); err != nil {
		t.Fatalf("missing %s tree-sitter fixture in %s: %v", langID, dir, err)
	}
	return dir
}

// setupBuggyCcpp copies testdata/<fixture> to a fresh temp dir, resolves
// symlinks (so the adapter's reported source path matches breakpoints),
// and builds the debug binary lldb-dap will launch with the given
// compiler at -O0 so no local is optimized away. It returns the
// workspace dir and the built binary path.
func setupBuggyCcpp(t *testing.T, fixture, source, compiler string) (dir, binPath string) {
	t.Helper()
	tmp, err := os.MkdirTemp("", "debugshell-"+fixture+"-e2e-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}
	src := filepath.Join("testdata", fixture)
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(tmp, e.Name()), data, 0o644))
	}

	build := exec.Command(compiler, "-g", "-O0", "-o", fixture, source)
	build.Dir = tmp
	out, err := build.CombinedOutput()
	require.NoError(t, err, "%s failed: %s", compiler, out)

	binPath = filepath.Join(tmp, fixture)
	if _, err := os.Stat(binPath); err != nil {
		t.Fatalf("built binary not found at %s: %v", binPath, err)
	}
	return tmp, binPath
}

// newCcppE2EHarness mirrors newZigE2EHarness but registers the adapter
// under langID ("c" or "cpp") and wires the matching committed
// tree-sitter grammar so the parser-driven breakpoint and
// variables-location features behave as they do for Go and Rust.
func newCcppE2EHarness(t *testing.T, lldbDapBin, dir, langID, source string) *e2eHarness {
	t.Helper()
	scheme := newLocalScheme()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	pkg := &ccppPkgManager{bin: lldbDapBin, langID: langID, grammar: ccppGrammarDir(t, langID)}
	dapCfg := idedebug.Config{
		MaxRetries:        1,
		InitializeTimeout: 30 * time.Second,
		Adapters:          map[string]idedebug.AdapterConfig{langID: ccppAdapterConfig(lldbDapBin)},
	}
	mgr := idedebug.New(uri, scheme, pkg, dapCfg)

	br := newFakeBrowser()
	tile := &fakeWindow{id: 1, content: &texttest.TestEditorHandler{}}
	shellW := &fakeWindow{id: 2, content: newFakeShellHandler()}
	br.windows = []*fakeWindow{tile, shellW}
	ed := newFakeTextapiEditor()
	mainBytes, err := os.ReadFile(filepath.Join(dir, source))
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

// ccppStmtLine is the 1-based line whose trimmed text equals stmt,
// computed at runtime so edits to the fixture do not break the
// breakpoint target. The `other` siblings initialize their shadowed
// locals to 1, so `int total = 0;` is unique to sum_to in both fixtures.
func ccppStmtLine(t *testing.T, path, stmt string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == stmt {
			return i + 1
		}
	}
	t.Fatalf("could not find %q in %s", stmt, path)
	return 0
}

// ccppFunctionLineRange returns the 0-based [start, end] line range of
// the free function whose signature line starts with prefix (for
// example "static int other("), used to assert that variable locations
// in a sibling function are filtered out.
func ccppFunctionLineRange(t *testing.T, path, prefix string) (int, int) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	start := -1
	depth := 0
	for i, line := range lines {
		if start < 0 {
			if strings.HasPrefix(line, prefix) {
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
		"could not locate function %q in %s", prefix, path)
	return 0, 0
}

// assertNoVariableIn fails when any variables-location highlight lands
// inside the 0-based line range of the sibling function, which would
// mean the grammar's locals scopes were not honored.
func assertNoVariableIn(t *testing.T, h *e2eHarness, start, end int) {
	t.Helper()
	require.Greater(t, end, start, "sibling range")
	for _, loc := range h.allLocations(t, variablesLocationID) {
		assert.False(t, loc.From.Y >= start && loc.From.Y <= end,
			"variable highlight must not fall in the sibling (line %d) — "+
				"got %s in scope of sibling at lines %d..%d",
			loc.From.Y+1, loc.Message, start+1, end+1)
	}
}

func TestE2E_C_Launch(t *testing.T) {
	t.Parallel()
	lldbDapBin := findLldbDap(t)
	cc := findCompiler(t, "cc", "clang", "gcc")
	tmpDir, binPath := setupBuggyCcpp(t, "buggy_c", "main.c", cc)
	mainPath := filepath.Join(tmpDir, "main.c")

	h := newCcppE2EHarness(t, lldbDapBin, tmpDir, "c", "main.c")
	defer h.close()
	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "c")
	require.NoError(t, err)
	go h.drainIterator(it)

	// lldb-dap launches the compiled binary, not the source dir.
	_, err = h.run(ctx, subLaunch, binPath)
	require.NoError(t, err)

	h.waitMilestone(t, "initialized", 30*time.Second)

	bpLine := ccppStmtLine(t, mainPath, "int total = 0;")
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
	// highlight landing there means the C grammar scopes were not
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
	otherStart, otherEnd := ccppFunctionLineRange(t, mainPath, "static int other(")
	assertNoVariableIn(t, h, otherStart, otherEnd)

	// Evaluate an arbitrary expression in the stopped (sum_to) frame:
	// the parameter n is 6 at the first statement.
	ev, err := h.mgr.Evaluate(ctx, h.sessionID(t), &dap.EvaluateArguments{
		Expression: "n + 1",
		FrameId:    stack[0].Id,
		Context:    "repl",
	})
	require.NoError(t, err)
	assert.Contains(t, ev.Result, "7", "expected n + 1 to evaluate to 7, got %q", ev.Result)

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

func TestE2E_C_BreakpointOnEmptyLine(t *testing.T) {
	t.Parallel()
	lldbDapBin := findLldbDap(t)
	cc := findCompiler(t, "cc", "clang", "gcc")
	tmpDir, binPath := setupBuggyCcpp(t, "buggy_c", "main.c", cc)
	mainPath := filepath.Join(tmpDir, "main.c")

	h := newCcppE2EHarness(t, lldbDapBin, tmpDir, "c", "main.c")
	defer h.close()
	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "c")
	require.NoError(t, err)
	go h.drainIterator(it)

	_, err = h.run(ctx, subLaunch, binPath)
	require.NoError(t, err)
	h.waitMilestone(t, "initialized", 30*time.Second)

	firstStmt := ccppStmtLine(t, mainPath, "int total = 0;")
	emptyLine := rsBlankLineNear(t, mainPath, firstStmt+1)
	hndl, err := h.runPromptOnHandler(ctx, mainPath,
		term.Coordinates{Y: emptyLine - 1}, subSetBreakpoint)
	require.NoError(t, err)
	require.NotNil(t, hndl.LocationList)
	_, ok := hndl.LocationList.Current()
	require.True(t, ok, "no breakpoint location installed")
}
