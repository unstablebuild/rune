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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-dap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/extension_python/pyshim"
	"unstable.build/rune/internal/ide/idedebug"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/text/vi"
	"unstable.build/rune/internal/workspace"

	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// pyDebugpyPin mirrors the debugpy version the python language package
// pins in config.yaml (debugger.python.command and
// extensions.python.config.debugpy).
const pyDebugpyPin = "1.8.17"

// pythonAdapterConfig mirrors the python language package's
// config.yaml: the adapter runs the pinned debugpy through uvx
// (isolated from the project env, never mutating it) and the launch
// template routes the debuggee through the venv-aware python shim, so
// it executes in the nearest project venv enclosing the program,
// resolved per launch. The {host}/{port} placeholders are substituted
// with the address the manager binds.
func pythonAdapterConfig(uvxBin, shim string) idedebug.Config {
	return idedebug.Config{
		MaxRetries:        1,
		InitializeTimeout: 15 * time.Second,
		Adapters: map[string]idedebug.AdapterConfig{
			"python": {
				Command: []string{uvxBin, "--from", "debugpy==" + pyDebugpyPin,
					"python", "-m", "debugpy.adapter",
					"--host", "{host}", "--port", "{port}"},
				AdapterID: "debugpy",
				LaunchArgs: map[string]string{
					"request": "launch",
					"type":    "python",
					"console": "internalConsole",
					"python":  shim,
				},
				AttachArgs: map[string]string{
					"request": "attach",
					"type":    "python",
				},
			},
		},
	}
}

// pyPkgManager satisfies idedebug.PkgManager and syntax.PkgManager
// for the Python e2e harness: for the "python" language id it returns
// the tree-sitter grammar files so the parser-driven
// breakpoint/variables features run exactly as they do for Go. The
// adapter binary needs no package resolution — the uvx command is
// absolute, matching the packaged config.
type pyPkgManager struct {
	grammar string
}

func (p *pyPkgManager) LibDir(
	_ context.Context, langID string,
) (iterator.Iterator[string], error) {
	var files []string
	if p.grammar != "" && langID == "python" {
		entries, err := os.ReadDir(p.grammar)
		if err == nil {
			for _, e := range entries {
				files = append(files, filepath.Join(p.grammar, e.Name()))
			}
		}
	}
	return iterator.FromSlice(files), nil
}

// pyGrammarDir locates the installed Python tree-sitter grammar
// directory (the lib dir holding tree-sitter.so + *.scm) shipped by
// the python language package under ~/.rune/pkg/python/<ver>/lib. It
// skips the test when no such grammar is installed so CI without the
// Python toolchain stays green.
func pyGrammarDir(t *testing.T) string {
	t.Helper()
	base := filepath.Join(os.Getenv("HOME"), ".rune", "pkg", "python")
	versions, err := os.ReadDir(base)
	if err != nil {
		t.Skipf("python grammar package not found at %s: %v", base, err)
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
	t.Skipf("no python tree-sitter.so under %s", base)
	return ""
}

// newPyE2EHarness mirrors newE2EHarness but targets the Python
// debugpy adapter, wiring the real Python tree-sitter grammar so the
// parser-driven breakpoint and variables-location features behave as
// they do for Go.
func newPyE2EHarness(t *testing.T, dir string) *e2eHarness {
	t.Helper()
	uvxBin, shim := pySetupNewWay(t, dir)
	return newPyE2EHarnessDAP(t, dir, pythonAdapterConfig(uvxBin, shim))
}

// newPyE2EHarnessDAP is newPyE2EHarness with an explicit DAP adapter
// registry, so tests can exercise alternative adapter topologies.
func newPyE2EHarnessDAP(
	t *testing.T, dir string, dapCfg idedebug.Config,
) *e2eHarness {
	t.Helper()
	scheme := newLocalScheme()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	pkg := &pyPkgManager{grammar: pyGrammarDir(t)}
	mgr := idedebug.New(uri, scheme, pkg, dapCfg)

	br := newFakeBrowser()
	tile := &fakeWindow{id: 1, content: &texttest.TestEditorHandler{}}
	shellW := &fakeWindow{id: 2, content: newFakeShellHandler()}
	br.windows = []*fakeWindow{tile, shellW}
	ed := newFakeTextapiEditor()
	mainBytes, err := os.ReadFile(filepath.Join(dir, "main.py"))
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

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

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

// findUVTool resolves uv or uvx from PATH or the Rune install dir
// (where the python language package stages them), skipping the test
// when absent — production always has them on the Rune PATH.
func findUVTool(t *testing.T, name string) string {
	t.Helper()
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	p := filepath.Join(os.Getenv("HOME"), ".rune", "bin", name)
	if info, err := os.Stat(p); err == nil && !info.IsDir() {
		return p
	}
	t.Skipf("%s not found, skipping python debugger e2e test", name)
	return ""
}

// pySetupNewWay provisions the production Python debug topology for the
// workspace dir the way the python package and extension set it up: a
// project .venv (what `uv sync` leaves behind), the venv-aware python
// shims written exactly as extension_python writes them, and the pinned
// debugpy resolved into uvx's cache (the extension's prewarm). The shim
// dir gets no managed fallback, so a launch only reaches a stopped
// state if the launch template's `python` shim actually resolved the
// project venv. Skips when uv/uvx or the pinned debugpy is unavailable
// (e.g. offline with a cold cache).
func pySetupNewWay(t *testing.T, dir string) (uvxBin, shim string) {
	t.Helper()
	uvBin := findUVTool(t, "uv")
	uvxBin = findUVTool(t, "uvx")

	venv := exec.Command(uvBin, "venv")
	venv.Dir = dir
	if out, err := venv.CombinedOutput(); err != nil {
		t.Skipf("uv venv failed: %v\n%s", err, out)
	}

	warm := exec.Command(uvxBin, "--from", "debugpy=="+pyDebugpyPin,
		"python", "-c", "import debugpy")
	if out, err := warm.CombinedOutput(); err != nil {
		t.Skipf("debugpy==%s not resolvable via uvx: %v\n%s",
			pyDebugpyPin, err, out)
	}

	dataDir := t.TempDir()
	uri, err := workspaceapi.ParseURI("file://" + dataDir)
	require.NoError(t, err)
	fs, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	defer func() { _ = fs.Close() }()
	require.NoError(t, pyshim.Write(fs, dataDir))
	return uvxBin, filepath.Join(pyshim.Dir(dataDir), "python3")
}

// setupBuggyPy copies testdata/buggy_py to a fresh temp directory
// and returns its real path. As with setupBuggy, symlinks are
// resolved so the debug adapter matches breakpoints against the
// path it reports for the source file.
func setupBuggyPy(t *testing.T) string {
	t.Helper()
	tmp, err := os.MkdirTemp("", "debugshell-py-e2e-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}
	src := "testdata/buggy_py"
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(src, e.Name()))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(
			filepath.Join(tmp, e.Name()), data, 0o644))
	}
	return tmp
}

// pyFirstStmtLine is the 1-based line of `total = 0` inside
// sum_to in testdata/buggy_py/main.py; it is computed at runtime so
// edits to the surrounding file do not break the breakpoint target.
func pyFirstStmtLine(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "total = 0" {
			return i + 1
		}
	}
	t.Fatalf("could not find 'total = 0' in %s", path)
	return 0
}

func TestE2E_Python_Launch(t *testing.T) {
	t.Parallel()
	tmpDir := setupBuggyPy(t)
	mainPath := filepath.Join(tmpDir, "main.py")

	h := newPyE2EHarness(t, tmpDir)
	defer h.close()
	ctx := h.ctx

	// 1. initialize the session.
	it, err := h.run(ctx, subInitialize, "python")
	require.NoError(t, err)
	go h.drainIterator(it)

	// 2. launch the buggy program (fire-and-forget until configured).
	_, err = h.run(ctx, subLaunch, mainPath)
	require.NoError(t, err)

	// 3. wait for the "initialized" milestone before configurationDone.
	h.waitMilestone(t, "initialized", 15*time.Second)

	// 4. set a breakpoint at the first statement of sum_to.
	bpLine := pyFirstStmtLine(t, mainPath)
	h.setBreakpoint(t, mainPath, bpLine)

	// 5. mark configuration done, then wait for the hit.
	_, err = h.run(ctx, subConfigured)
	require.NoError(t, err)
	h.waitMilestone(t, "stopped", 15*time.Second)

	// 6. the top frame must be exactly sum_to at the breakpoint line
	// in the program source we launched.
	threadID := h.firstThreadID(t)
	stack := h.stackTrace(t, threadID)
	require.NotEmpty(t, stack)
	assert.Equal(t, "sum_to", stack[0].Name)
	assert.Equal(t, bpLine, stack[0].Line)
	require.NotNil(t, stack[0].Source)
	assert.Equal(t, mainPath, stack[0].Source.Path)

	// The launch template's `python` shim must have routed the debuggee
	// into the project venv resolved from the program's directory —
	// the monorepo-correct per-launch contract.
	ev, err := h.mgr.Evaluate(ctx, h.sessionID(t), &dap.EvaluateArguments{
		Expression: "__import__('sys').executable",
		FrameId:    stack[0].Id,
		Context:    "repl",
	})
	require.NoError(t, err)
	assert.Contains(t, ev.Result, filepath.Join(tmpDir, ".venv", "bin", "python"),
		"debuggee must run on the project venv interpreter")

	// 6a. the stopped location list must span the line, use black on
	// yellow, and carry the stack-trace text as its Message.
	h.waitLocation(t, stoppedLocationID, 5*time.Second)
	stopLoc := h.findLocation(t, stoppedLocationID)
	require.True(t, stopLoc.To.X > 1,
		"expected stopped location to span the line, got To.X=%d", stopLoc.To.X)
	assert.Equal(t, term.ColorBlack, stopLoc.Attr.Fg)
	assert.Equal(t, term.ColorYellow, stopLoc.Attr.Bg)
	assert.Equal(t, bpLine-1, stopLoc.From.Y,
		"stopped highlight must sit on the breakpoint line")
	assert.Contains(t, stopLoc.Message, "sum_to",
		"expected stack-trace message, got %q", stopLoc.Message)

	// 6b. the variables location list must be installed at critical
	// priority with a gray background, a range covering the variable
	// name, and a non-empty value Message — and must never highlight
	// the identically-named locals inside the sibling `other`.
	h.waitLocation(t, variablesLocationID, 5*time.Second)
	varLoc := h.findLocation(t, variablesLocationID)
	assert.Equal(t, term.ColorGray, varLoc.Attr.Bg)
	assert.True(t, varLoc.To.X > varLoc.From.X,
		"expected variable name range, got %v..%v", varLoc.From, varLoc.To)
	assert.NotEmpty(t, varLoc.Message, "expected variable value as Message")
	assert.Equal(t, textapi.LocationPriorityCritical,
		h.locationPrio(t, variablesLocationID),
		"variables list must be critical priority to sit on top of stopped")
	otherStart, otherEnd := pyFunctionLineRange(t, mainPath, "other")
	require.Greater(t, otherEnd, otherStart, "other range")
	for _, loc := range h.allLocations(t, variablesLocationID) {
		assert.False(t, loc.From.Y >= otherStart && loc.From.Y <= otherEnd,
			"variable highlight must not fall in other (line %d)",
			loc.From.Y+1)
	}

	// 6c. evaluate an expression in the stopped (sum_to) frame. `n`
	// is the function parameter and must resolve to a value.
	evIt, err := h.run(ctx, subEvaluate, "n + 1")
	require.NoError(t, err)
	require.NotNil(t, evIt)
	defer evIt.Close()

	// 6d. `debugger jump backward` must move the cursor up into the
	// caller frame without stepping.
	require.GreaterOrEqual(t, len(stack), 2,
		"need at least two stack frames for jump test")
	beforeJump := h.cursor(t)
	require.NoError(t, h.runPrompt(ctx, mainPath, beforeJump,
		subJump, jumpBackward))
	afterJump := h.cursor(t)
	assert.NotEqual(t, beforeJump, afterJump,
		"expected cursor to move on jump backward")

	// 7. capture the output path before the session ends, then run
	// the program to completion.
	outPath := h.outputPath(t)
	require.NotEmpty(t, outPath, "output capture file path")
	h.continueUntilExit(t, threadID, 20*time.Second)
	_, _ = h.run(ctx, subTerminate)

	// 8. the sink writes one "[<category>] <output>" record per DAP
	// OutputEvent. debugpy emits the debuggee's single
	// `print("Sum:", result)` as two stdout events ("Sum:" then
	// " 21\n"); reassembling just the stdout records must yield
	// exactly the program's output.
	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Equal(t, "Sum: 21\n", stdoutFromCapture(string(data)))
}

// stdoutFromCapture extracts and concatenates the payloads of the
// "[stdout] " records the debug output sink writes. Records have the
// form "[<category>] <payload>" with no separator between them, so a
// record's payload runs until the next "[<category>] " marker.
func stdoutFromCapture(capture string) string {
	const prefix = "[stdout] "
	var b strings.Builder
	for {
		i := strings.Index(capture, prefix)
		if i < 0 {
			break
		}
		rest := capture[i+len(prefix):]
		// A payload runs until the next "[<category>] " record marker.
		next := nextRecordStart(rest)
		if next < 0 {
			b.WriteString(rest)
			break
		}
		b.WriteString(rest[:next])
		capture = rest[next:]
	}
	return b.String()
}

// nextRecordStart returns the index of the next "[<category>] "
// record marker in s, or -1 if none.
func nextRecordStart(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '[' {
			continue
		}
		close := strings.IndexByte(s[i:], ']')
		if close < 0 {
			continue
		}
		// A marker is "[word] " — the char after ']' must be a space.
		if i+close+1 < len(s) && s[i+close+1] == ' ' {
			return i
		}
	}
	return -1
}

// pyLineContaining returns the 1-based line number of the first line
// in path whose trimmed content equals literal. Fails the test when
// none match.
func pyLineContaining(t *testing.T, path, literal string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for i, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == literal {
			return i + 1
		}
	}
	require.Failf(t, "literal not found", "path=%s literal=%q", path, literal)
	return 0
}

// pyBlankLineNear returns a 1-based blank-line number at or after
// start (1-based) in path. Fails the test if none.
func pyBlankLineNear(t *testing.T, path string, start int) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	for i := start - 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			return i + 1
		}
	}
	require.Fail(t, "no blank line after start",
		"path=%s start=%d", path, start)
	return 0
}

// pyFunctionLineRange returns the 0-based [start, end] line range of
// the named top-level `def` in path, where end is the last line of
// its (indentation-delimited) body. Used to assert that locations in
// a sibling function are filtered out of the variables list.
func pyFunctionLineRange(t *testing.T, path, name string) (int, int) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	start := -1
	for i, line := range lines {
		if start < 0 {
			if strings.HasPrefix(line, "def "+name+"(") {
				start = i
			}
			continue
		}
		// The body ends at the next top-level (column-0, non-blank)
		// line — the start of the following def or statement.
		if strings.TrimSpace(line) != "" &&
			!strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			return start, i - 1
		}
	}
	if start >= 0 {
		return start, len(lines) - 1
	}
	require.Fail(t, "function not found",
		"could not locate def %q in %s", name, path)
	return 0, 0
}

func TestE2E_Python_BreakpointOnEmptyLine(t *testing.T) {
	t.Parallel()
	tmpDir := setupBuggyPy(t)
	mainPath := filepath.Join(tmpDir, "main.py")

	h := newPyE2EHarness(t, tmpDir)
	defer h.close()
	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "python")
	require.NoError(t, err)
	go h.drainIterator(it)

	_, err = h.run(ctx, subLaunch, mainPath)
	require.NoError(t, err)
	h.waitMilestone(t, "initialized", 15*time.Second)

	// The blank line between the sum_to docstring boundary and the
	// for loop is non-executable. Use the blank line that sits right
	// after `total = 0` so the next executable line is inside sum_to.
	firstStmt := pyFirstStmtLine(t, mainPath)
	emptyLine := pyBlankLineNear(t, mainPath, firstStmt+1)
	hndl, err := h.runPromptOnHandler(ctx, mainPath,
		term.Coordinates{Y: emptyLine - 1}, subSetBreakpoint)
	require.NoError(t, err)
	require.NotNil(t, hndl.LocationList)
	loc, ok := hndl.LocationList.Current()
	require.True(t, ok, "no breakpoint location installed")
	assert.NotEqual(t, emptyLine-1, loc.From.Y,
		"breakpoint marker should be moved off the empty line")
	assert.Equal(t, emptyLine, loc.From.Y,
		"breakpoint marker should sit on the next executable line")

	_, err = h.run(ctx, subConfigured)
	require.NoError(t, err)

	// The breakpoint must hit; without the client-side adjustment the
	// program would run to completion and only "terminated" would
	// arrive.
	h.waitMilestone(t, "stopped", 15*time.Second)
	threadID := h.firstThreadID(t)
	stack := h.stackTrace(t, threadID)
	require.NotEmpty(t, stack)
	assert.Equal(t, "sum_to", stack[0].Name)

	h.continueUntilExit(t, threadID, 20*time.Second)
	_, _ = h.run(ctx, subTerminate)
}

func TestE2E_Python_BreakpointOnTrailingComment(t *testing.T) {
	t.Parallel()
	tmpDir := setupBuggyPy(t)
	mainPath := filepath.Join(tmpDir, "main.py")

	h := newPyE2EHarness(t, tmpDir)
	defer h.close()
	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "python")
	require.NoError(t, err)
	go h.drainIterator(it)

	// The trailing comment-only line sits after the last statement
	// (`main()`), so there is no statement at or after it. Comment
	// nodes are skipped by the parser-driven adjustment.
	line := pyLineContaining(t, mainPath,
		"# trailing comment: a breakpoint target past the last statement")
	cursor := term.Coordinates{Y: line - 1}
	err = h.runPrompt(ctx, mainPath, cursor, subSetBreakpoint)
	require.Error(t, err, "set-breakpoint past last statement must fail")
	assert.Contains(t, err.Error(), "no statement",
		"expected parser-driven failure, got %v", err)
}

func TestE2E_Python_BreakpointOnLiteralOnlyReturn(t *testing.T) {
	t.Parallel()
	tmpDir := setupBuggyPy(t)
	mainPath := filepath.Join(tmpDir, "main.py")

	h := newPyE2EHarness(t, tmpDir)
	defer h.close()
	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "python")
	require.NoError(t, err)
	go h.drainIterator(it)

	line := pyLineContaining(t, mainPath, "return False")
	cursor := term.Coordinates{Y: line - 1}
	hndl, err := h.runPromptOnHandler(ctx, mainPath, cursor, subSetBreakpoint)
	require.NoError(t, err,
		"set-breakpoint on a literal-only return must succeed")
	require.NotNil(t, hndl.LocationList)
	loc, ok := hndl.LocationList.Current()
	require.True(t, ok, "no breakpoint location installed")
	assert.Equal(t, line-1, loc.From.Y,
		"breakpoint should sit on the literal-only return line")
}

func TestE2E_Python_AttachConnect(t *testing.T) {
	t.Parallel()
	uvBin := findUVTool(t, "uv")
	tmpDir := setupBuggyPy(t)
	mainPath := filepath.Join(tmpDir, "main.py")

	// The debuggee-side debugpy must resolve before starting the
	// listener, or the listen port never opens (offline, cold cache).
	probe := exec.Command(uvBin, "run", "--with", "debugpy=="+pyDebugpyPin,
		"python", "-c", "import debugpy")
	probe.Dir = tmpDir
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("debugpy==%s not resolvable via uv run: %v\n%s",
			pyDebugpyPin, err, out)
	}

	// Reserve a port for the debuggee-side adapter. There is a small
	// TOCTOU window before debugpy binds it; the initialize dial
	// retries until the adapter is listening either way.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())

	// Start the debuggee in "wait" mode under debugpy --listen so it
	// loops calling sum_to until we attach — via `uv run --with
	// debugpy`, the attach recipe the Python docs give users, which
	// overlays debugpy on the project env without mutating it.
	cmd := exec.Command(uvBin, "run", "--with", "debugpy=="+pyDebugpyPin,
		"python", "-m", "debugpy", "--listen", addr, mainPath, "wait")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})

	// The harness keeps the packaged adapter config (which spawns
	// uvx debugpy); the endpoint on the attach command overrides the
	// transport for this session only.
	h := newPyE2EHarness(t, tmpDir)
	defer h.close()
	ctx := h.ctx

	// 1. one command dials the debuggee-spawned adapter and attaches.
	it, err := h.run(ctx, subAttach, "python", "connect://"+addr, mainPath)
	require.NoError(t, err)
	go h.drainIterator(it)
	h.waitMilestone(t, "initialized", 30*time.Second)

	// 2. breakpoint inside sum_to, then configurationDone resumes.
	bpLine := pyFirstStmtLine(t, mainPath)
	h.setBreakpoint(t, mainPath, bpLine)
	_, err = h.run(ctx, subConfigured)
	require.NoError(t, err)
	h.waitMilestone(t, "stopped", 30*time.Second)

	// 3. the top frame must be sum_to at the breakpoint line.
	threadID := h.firstThreadID(t)
	stack := h.stackTrace(t, threadID)
	require.NotEmpty(t, stack)
	assert.Equal(t, "sum_to", stack[0].Name)
	assert.Equal(t, bpLine, stack[0].Line)
	require.NotNil(t, stack[0].Source)
	assert.Equal(t, mainPath, stack[0].Source.Path)

	// 4. terminate must clear the local session even though the
	// adapter lifecycle is owned by the debuggee.
	_, _ = h.run(ctx, subTerminate)
}

func TestE2E_Python_CtrlCDoesNotStopEventStream(t *testing.T) {
	t.Parallel()
	tmpDir := setupBuggyPy(t)
	mainPath := filepath.Join(tmpDir, "main.py")

	h := newPyE2EHarness(t, tmpDir)
	defer h.close()

	initCtx, initCancel := context.WithCancel(h.ctx)
	defer initCancel()

	it, err := h.run(initCtx, subInitialize, "python")
	require.NoError(t, err)

	var (
		drainMu  sync.Mutex
		received int
	)
	doneDrain := make(chan struct{})
	go func() {
		defer close(doneDrain)
		defer it.Close()
		for {
			_, ok := it.Next(initCtx)
			if !ok {
				return
			}
			drainMu.Lock()
			received++
			drainMu.Unlock()
		}
	}()

	_, err = h.run(h.ctx, subLaunch, mainPath)
	require.NoError(t, err)

	h.waitMilestone(t, "initialized", 30*time.Second)
	h.setBreakpoint(t, mainPath, pyFirstStmtLine(t, mainPath))

	// >>> Ctrl-C <<<
	initCancel()
	time.Sleep(100 * time.Millisecond)
	drainMu.Lock()
	beforeConfigured := received
	drainMu.Unlock()

	_, err = h.run(h.ctx, subConfigured)
	require.NoError(t, err)
	h.waitMilestone(t, "stopped", 15*time.Second)

	// A StoppedEvent pushes the auto stack-trace plus the event
	// itself; the drain loop must observe new items despite the
	// cancelled per-command context.
	require.Eventually(t, func() bool {
		drainMu.Lock()
		defer drainMu.Unlock()
		return received > beforeConfigured+1
	}, 15*time.Second, 50*time.Millisecond,
		"session iterator delivered no new items after Ctrl-C and "+
			"StoppedEvent — the cancelled per-command context killed "+
			"event flow")

	threadID := h.firstThreadID(t)
	h.continueUntilExit(t, threadID, 20*time.Second)
	_, _ = h.run(h.ctx, subTerminate)

	// The drain goroutine is torn down by the deferred h.close()
	// (manager Close), which closes the session and ends the
	// iterator. Unlike dlv, debugpy does not close its adapter
	// connection on terminate after the debuggee has already exited,
	// so the iterator does not end on terminate alone — asserting
	// that here would be debugpy-version-specific.
	_ = doneDrain
}

func TestE2E_Python_OutputAppearsInIDEEditorBuffer(t *testing.T) {
	t.Parallel()
	tmpDir := setupBuggyPy(t)
	mainPath := filepath.Join(tmpDir, "main.py")

	h := newPyIDEHarness(t, tmpDir)
	defer h.close()
	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "python")
	require.NoError(t, err)
	go h.drainIterator(it)

	_, err = h.run(ctx, subLaunch, mainPath)
	require.NoError(t, err)
	h.waitMilestone(t, "initialized", 15*time.Second)

	_, err = h.run(ctx, subConfigured)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return h.outputPath(t) != ""
	}, 5*time.Second, 20*time.Millisecond, "sink path never set")
	outPath := h.outputPath(t)

	outURI, err := workspaceapi.ParseURI("file://" + outPath)
	require.NoError(t, err)
	h.uiMu.Lock()
	_, err = h.comp.Open(outURI)
	h.uiMu.Unlock()
	require.NoError(t, err)

	h.waitMilestone(t, "terminated", 20*time.Second)

	require.NotEmpty(t, outPath, "output capture path")
	require.True(t, strings.HasPrefix(outPath, tmpDir),
		"sink must live in workspace dir for FS watcher; got %s", outPath)

	// The on-disk sink must carry the debuggee's stdout. debugpy
	// splits the print across stdout records, so match on the
	// reassembled stdout payload.
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(outPath)
		return err == nil &&
			strings.Contains(stdoutFromCapture(string(data)), "Sum: 21")
	}, 15*time.Second, 50*time.Millisecond,
		"on-disk sink never received Sum: 21")

	// And the IDE editor buffer for the same file must reflect it,
	// proving the watcher → ReloadTab pipeline fired.
	require.Eventually(t, func() bool {
		return strings.Contains(h.bufferContent(outPath), "Sum:")
	}, 15*time.Second, 100*time.Millisecond,
		"editor buffer never reflected output; got %q",
		h.bufferContent(outPath))

	_, _ = h.run(ctx, subTerminate)
}

// newPyIDEHarness mirrors newIDEHarness but targets the Python
// debugpy adapter, so the full FS-watch → ReloadTab path is exercised
// for a Python debug session.
func newPyIDEHarness(t *testing.T, dir string) *ideHarness {
	t.Helper()
	uvxBin, shim := pySetupNewWay(t, dir)
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	hh := &ideHarness{t: t}
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	// See newIDEHarness: reload buffer mutations must hold uiMu.
	ws := workspace.NewSchemeWorkspace(uri, scheme, hh.uiSchedule)

	ed := vi.Editor(vi.WithStatusBarConfig(false, text.StatusBarConfig{
		Publisher:        texttest.NopEditor(),
		ScheduleNextTick: func(fn func()) bool { fn(); return true },
	}))
	tcfg := text.DefaultConfig()
	tcfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
	comp, err := text.NewComponent(ed, ws, tcfg)
	require.NoError(t, err)
	comp.Browser().Resize(120, 40)

	procExec := newLocalScheme()
	pkg := &pyPkgManager{}
	dapCfg := pythonAdapterConfig(uvxBin, shim)
	mgr := idedebug.New(uri, procExec, pkg, dapCfg)

	hh.scheme = scheme
	hh.ws = ws
	hh.comp = comp
	hh.mgr = mgr
	hh.procExec = procExec
	hh.cond = sync.NewCond(&hh.mu)

	apiEd := newCompEditorAdapter(comp)
	h := New(mgr, comp, apiEd, passThroughParser{}, passThroughFS{}, Config{
		WorkspaceURI:     uri,
		Debugger:         dapCfg,
		ScheduleNextTick: hh.uiSchedule,
	}).WithNotify(hh.notify)
	hh.h = h

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	hh.ctx = ctx
	hh.cancel = cancel

	go hh.pollAndReload(ctx, dir)
	return hh
}
