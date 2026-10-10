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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-dap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/ide/console/ideconsole"
	"unstable.build/rune/internal/ide/idedebug"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/text/vi"
	"unstable.build/rune/internal/workspace"

	"github.com/unstablebuild/rune-go-sdk/api/config"
)

// TestE2E_Launch and TestE2E_Attach drive a real delve dap
// server through the debugshell.Handler command surface, end
// to end. They verify that the basic session lifecycle
// (initialize → launch/attach → set-breakpoint → configured →
// stopped → continue → terminate) works against the real
// adapter and that our DAP client implementation matches what
// delve expects on the wire.
//
// The two tests share the testing harness defined in this
// file: a real idedebug.Manager (debugapi.Debugger), a real
// localScheme (schemeapi.Executor) using the file scheme, a
// real debugshell.Handler with a stub Browser/Editor, and a
// stub notification sink that turns key DAP events
// (initialized, stopped, terminated, exited) into deterministic
// signals tests can wait on.

// sumFirstStmtLine is the 1-based line number of the first
// executable statement of the Sum function in
// testdata/buggy/main.go (`total := 0`). The tests use this
// instead of a literal so the breakpoint target survives
// edits to the surrounding file (license header, package
// comment, blank lines, etc.).
//
// sumBlankLineStart is a 1-based line at-or-just-before the
// blank line inside Sum's body (between `total := 0` and the
// `for` loop). It is used as the starting point for
// blankLineNear so the test reliably picks the blank line
// whose immediately-following line is executable.
const (
	sumFirstStmtLine  = 37
	sumBlankLineStart = 37
)

func TestE2E_Launch(t *testing.T) {
	t.Parallel()
	dlvBin := findDlv(t)
	tmpDir := setupBuggy(t)
	mainPath := filepath.Join(tmpDir, "main.go")

	h := newE2EHarness(t, dlvBin, tmpDir)
	defer h.close()

	ctx := h.ctx

	// 1. initialize the session.
	it, err := h.run(ctx, subInitialize, "go")
	require.NoError(t, err)
	go h.drainIterator(it)

	// 2. launch the buggy program. Launch is fire-and-forget;
	// the server will respond after configurationDone.
	_, err = h.run(ctx, subLaunch, tmpDir)
	require.NoError(t, err)

	// 3. wait for the "initialized" milestone before sending
	// configurationDone (DAP spec requirement).
	h.waitMilestone(t, "initialized", 10*time.Second)

	// 4. set a breakpoint at the first executable line of Sum.
	h.setBreakpoint(t, mainPath, sumFirstStmtLine)

	// 5. mark configuration done.
	_, err = h.run(ctx, subConfigured)
	require.NoError(t, err)

	// 6. wait for the breakpoint to hit.
	h.waitMilestone(t, "stopped", 10*time.Second)

	// 7. inspect the stack — top frame should be in Sum.
	threadID := h.firstThreadID(t)
	stack := h.stackTrace(t, threadID)
	require.NotEmpty(t, stack)
	assert.Contains(t, stack[0].Name, "Sum",
		"expected to be stopped inside Sum, got %s", stack[0].Name)

	// 7a. The stopped location list must cover the whole line
	// (X 0..line-length), use ColorBlack as foreground, and
	// use the StackTrace as Message.
	h.waitLocation(t, stoppedLocationID, 5*time.Second)
	stopLoc := h.findLocation(t, stoppedLocationID)
	require.True(t, stopLoc.To.X > 1,
		"expected stopped location to span the line, got To.X=%d", stopLoc.To.X)
	assert.Equal(t, term.ColorBlack, stopLoc.Attr.Fg)
	assert.Equal(t, term.ColorYellow, stopLoc.Attr.Bg)
	assert.Contains(t, stopLoc.Message, "main.Sum",
		"expected stack-trace message, got %q", stopLoc.Message)

	// 7b. The variables location list must be installed at
	// LocationPriorityCritical with at least one entry that
	// has a non-empty Message (the variable's value), gray bg
	// and a range covering the variable name (To.X > From.X).
	h.waitLocation(t, variablesLocationID, 5*time.Second)
	varLoc := h.findLocation(t, variablesLocationID)
	assert.Equal(t, term.ColorGray, varLoc.Attr.Bg)
	assert.True(t, varLoc.To.X > varLoc.From.X,
		"expected variable name range, got %v..%v", varLoc.From, varLoc.To)
	assert.NotEmpty(t, varLoc.Message,
		"expected variable value as Message")
	assert.Equal(t, textapi.LocationPriorityCritical,
		h.locationPrio(t, variablesLocationID),
		"variables list must be critical priority to sit on top of stopped")

	// Variables list must:
	//   * format Message as "type = value" (no name prefix)
	//   * cover identifiers only inside the *current* scope
	//     (Sum), not the sibling Other function which reuses
	//     the same local names (n, total).
	otherStart, otherEnd := h.functionLineRange(t, mainPath, "Other")
	require.Greater(t, otherEnd, otherStart, "Other range")
	for _, loc := range h.allLocations(t, variablesLocationID) {
		assert.NotContains(t, loc.Message, "=  ",
			"variable Message should be 'type = value', got %q",
			loc.Message)
		assert.False(t, loc.From.Y >= otherStart && loc.From.Y <= otherEnd,
			"variable highlight must not fall in Other (line %d) — "+
				"got %s in scope of helper at lines %d..%d",
			loc.From.Y+1, loc.Message, otherStart+1, otherEnd+1)
	}

	// 7c. evaluate an arbitrary expression in the current
	// (deepest, Sum) frame and verify the response surfaces a
	// value. Must run *before* `debugger jump backward` because
	// post-jump evaluate correctly targets the caller frame
	// where `n` is out of scope (see RUNE-173).
	evIt, err := h.run(ctx, subEvaluate, "n+1")
	require.NoError(t, err)
	require.NotNil(t, evIt)
	defer evIt.Close()

	// 7d. The prompt 'debugger jump backward' subcommand must
	// move the cursor up into the calling frame (one level
	// shallower) without invoking step-in/out.
	require.GreaterOrEqual(t, len(stack), 2,
		"need at least two stack frames for jump test")
	beforeJump := h.cursor(t)
	require.NoError(t, h.runPrompt(ctx, mainPath, beforeJump,
		subJump, jumpBackward))
	afterJump := h.cursor(t)
	assert.NotEqual(t, beforeJump, afterJump,
		"expected cursor to move on jump backward")

	// 8. continue and let the program run to completion. The
	// breakpoint may hit again across remaining loop iterations
	// — drain those by continuing until the debuggee exits.
	// 8a. Capture the output path *before* the session ends —
	// stopOutputCapture nils the handler's reference once the
	// debuggee is closed, but the file itself is left on disk.
	outPath := h.outputPath(t)
	require.NotEmpty(t, outPath, "output capture file path")

	h.continueUntilExit(t, threadID, 15*time.Second)

	// 9. terminate the session for good measure.
	_, _ = h.run(ctx, subTerminate)

	// 10. The captured file must contain the program's
	// stdout (the debuggee prints "Sum: 21"). The file is
	// left on disk after the session ends.
	data, err := os.ReadFile(outPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "Sum: 21",
		"expected debuggee stdout in capture file, got %q", string(data))
}

func TestE2E_BreakpointOnEmptyLine(t *testing.T) {
	t.Parallel()
	dlvBin := findDlv(t)
	tmpDir := setupBuggy(t)
	mainPath := filepath.Join(tmpDir, "main.go")

	h := newE2EHarness(t, dlvBin, tmpDir)
	defer h.close()

	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "go")
	require.NoError(t, err)
	go h.drainIterator(it)

	_, err = h.run(ctx, subLaunch, tmpDir)
	require.NoError(t, err)
	h.waitMilestone(t, "initialized", 10*time.Second)

	// The blank line inside Sum (between `total := 0` and the
	// for loop) used to cause the program to run to completion
	// without binding when used as a breakpoint target. Drive
	// the prompt-side toggle so the parser-driven adjustment
	// runs end-to-end.
	emptyLine := blankLineNear(t, mainPath, sumBlankLineStart)
	hndl, err := h.runPromptOnHandler(ctx, mainPath,
		term.Coordinates{Y: emptyLine - 1}, subSetBreakpoint)
	require.NoError(t, err)
	// The visual breakpoint marker installed on the prompt's
	// editor handler must reflect the parser-adjusted line —
	// not the empty line the user clicked on.
	require.NotNil(t, hndl.LocationList)
	loc, ok := hndl.LocationList.Current()
	require.True(t, ok, "no breakpoint location installed")
	assert.NotEqual(t, emptyLine-1, loc.From.Y,
		"breakpoint marker should be moved off the empty line")
	assert.Equal(t, emptyLine, loc.From.Y,
		"breakpoint marker should sit on the next executable line")

	_, err = h.run(ctx, subConfigured)
	require.NoError(t, err)

	// The breakpoint must hit. Without the client-side line
	// adjustment, this used to time out and the test would
	// see only "terminated" before any "stopped" event.
	h.waitMilestone(t, "stopped", 10*time.Second)

	threadID := h.firstThreadID(t)
	stack := h.stackTrace(t, threadID)
	require.NotEmpty(t, stack)

	_, _ = h.run(ctx, subTerminate)
}

// blankLineNear returns a 1-based blank-line number at or
// after start (1-based) in path. Fails the test if none.
func blankLineNear(t *testing.T, path string, start int) int {
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

func TestE2E_BreakpointOnClosingBrace(t *testing.T) {
	t.Parallel()
	dlvBin := findDlv(t)
	tmpDir := setupBuggy(t)
	mainPath := filepath.Join(tmpDir, "main.go")

	h := newE2EHarness(t, dlvBin, tmpDir)
	defer h.close()

	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "go")
	require.NoError(t, err)
	go h.drainIterator(it)

	// Find the closing-brace-only line of the last function
	// in main.go (`}` for `func main`). There are no
	// statements at or after that line.
	closingLine := lastClosingBraceLine(t, mainPath)
	cursor := term.Coordinates{Y: closingLine - 1}
	err = h.runPrompt(ctx, mainPath, cursor, subSetBreakpoint)
	require.Error(t, err, "set-breakpoint on closing brace must fail")
	assert.Contains(t, err.Error(), "no statement",
		"expected parser-driven failure, got %v", err)
}

// lastClosingBraceLine returns the 1-based line number of the
// last `}` that sits alone on its line in path.
func lastClosingBraceLine(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == "}" {
			return i + 1
		}
	}
	require.Fail(t, "no closing-brace-only line found", path)
	return 0
}

func TestE2E_BreakpointOnLiteralOnlyReturn(t *testing.T) {
	t.Parallel()
	dlvBin := findDlv(t)
	tmpDir := setupBuggy(t)
	mainPath := filepath.Join(tmpDir, "main.go")

	h := newE2EHarness(t, dlvBin, tmpDir)
	defer h.close()

	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "go")
	require.NoError(t, err)
	go h.drainIterator(it)

	// Locate the `return false` line inside AlwaysFalse.
	// Using a content match instead of a hard-coded number
	// keeps the test resilient to edits to the fixture's
	// license header or surrounding helpers.
	line := lineContaining(t, mainPath, "return false")
	cursor := term.Coordinates{Y: line - 1}
	hndl, err := h.runPromptOnHandler(ctx, mainPath, cursor, subSetBreakpoint)
	require.NoError(t, err,
		"set-breakpoint on a literal-only return must succeed")
	require.NotNil(t, hndl.LocationList)
	loc, ok := hndl.LocationList.Current()
	require.True(t, ok, "no breakpoint location installed")
	// The breakpoint must bind on the literal-only line
	// itself, not the next/previous line.
	assert.Equal(t, line-1, loc.From.Y,
		"breakpoint should sit on the literal-only return line")
}

// lineContaining returns the 1-based line number of the first
// line in path whose trimmed content equals literal. Fails
// the test if none match.
func lineContaining(t *testing.T, path, literal string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == literal {
			return i + 1
		}
	}
	require.Failf(t, "literal not found",
		"path=%s literal=%q", path, literal)
	return 0
}

func TestE2E_Attach(t *testing.T) {
	t.Parallel()
	dlvBin := findDlv(t)
	tmpDir := setupBuggy(t)
	mainPath := filepath.Join(tmpDir, "main.go")

	// Build the buggy program with debug info and start it in
	// "wait" mode so it loops calling Sum until we attach.
	binPath := filepath.Join(tmpDir, "buggy")
	build := exec.Command("go", "build", "-gcflags=all=-N -l",
		"-o", binPath, ".")
	build.Dir = tmpDir
	build.Env = append(os.Environ(), "GOFLAGS=")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "go build: %s", out)

	cmd := exec.Command(binPath, "wait")
	cmd.Dir = tmpDir
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
	})
	pid := cmd.Process.Pid

	h := newE2EHarness(t, dlvBin, tmpDir)
	defer h.close()

	ctx := h.ctx

	// 1. initialize.
	it, err := h.run(ctx, subInitialize, "go")
	require.NoError(t, err)
	go h.drainIterator(it)

	// 2. attach to the running pid.
	_, err = h.run(ctx, subAttach, strconv.Itoa(pid))
	require.NoError(t, err)

	// 3. wait for initialized event.
	h.waitMilestone(t, "initialized", 15*time.Second)

	// 4. set a breakpoint at the first executable line of Sum.
	h.setBreakpoint(t, mainPath, sumFirstStmtLine)

	// 5. configurationDone resumes the attached process.
	_, err = h.run(ctx, subConfigured)
	require.NoError(t, err)

	// 6. wait for the breakpoint to hit.
	h.waitMilestone(t, "stopped", 15*time.Second)

	// 7. inspect the stack.
	threadID := h.firstThreadID(t)
	stack := h.stackTrace(t, threadID)
	require.NotEmpty(t, stack)
	var foundSum bool
	for _, f := range stack {
		if f.Name != "" && (f.Name == "main.Sum" ||
			f.Source != nil && filepath.Base(f.Source.Path) == "main.go") {
			foundSum = true
			break
		}
	}
	assert.True(t, foundSum,
		"expected stack frame from main.go in stack: %#v", stack)

	// 7a/7b. Same location-list invariants as the launch test:
	// stopped covers the line, variables list installed at
	// critical priority with gray bg, jump moves the cursor.
	h.waitLocation(t, stoppedLocationID, 5*time.Second)
	stopLoc := h.findLocation(t, stoppedLocationID)
	require.True(t, stopLoc.To.X > 1,
		"expected stopped location to span the line, got To.X=%d",
		stopLoc.To.X)
	assert.Equal(t, term.ColorBlack, stopLoc.Attr.Fg)
	assert.Equal(t, term.ColorYellow, stopLoc.Attr.Bg)
	assert.Contains(t, stopLoc.Message, "main.Sum",
		"expected stack-trace message, got %q", stopLoc.Message)

	h.waitLocation(t, variablesLocationID, 5*time.Second)
	varLoc := h.findLocation(t, variablesLocationID)
	assert.Equal(t, term.ColorGray, varLoc.Attr.Bg)
	assert.True(t, varLoc.To.X > varLoc.From.X)
	assert.NotEmpty(t, varLoc.Message)
	assert.Equal(t, textapi.LocationPriorityCritical,
		h.locationPrio(t, variablesLocationID))

	otherStartA, otherEndA := h.functionLineRange(t, mainPath, "Other")
	require.Greater(t, otherEndA, otherStartA)
	for _, loc := range h.allLocations(t, variablesLocationID) {
		assert.False(t,
			loc.From.Y >= otherStartA && loc.From.Y <= otherEndA,
			"variable highlight must not fall in Other helper")
	}

	require.GreaterOrEqual(t, len(stack), 2)
	beforeJump := h.cursor(t)
	require.NoError(t, h.runPrompt(ctx, mainPath, beforeJump,
		subJump, jumpBackward))
	afterJump := h.cursor(t)
	assert.NotEqual(t, beforeJump, afterJump)

	// 8. terminate (delve translates this to a disconnect
	// without killing for attach mode in some versions; either
	// way the local session must be cleared).
	_, _ = h.run(ctx, subTerminate)
}

// e2eHarness wires up a real idedebug.Manager + debugshell.Handler
// against the local file scheme, with a stub browser, editor and
// notification sink that records key DAP milestones.
type e2eHarness struct {
	t      *testing.T
	ctx    context.Context
	cancel context.CancelFunc

	scheme *localScheme
	mgr    *idedebug.Manager
	h      *Handler
	br     *fakeBrowser
	ed     *fakeTextapiEditor

	mu         sync.Mutex
	milestones []milestone
	cond       *sync.Cond
	closed     bool
}

type milestone struct {
	level browserapi.NotificationLevel
	msg   string
}

func newE2EHarness(t *testing.T, dlvBin, dir string) *e2eHarness {
	t.Helper()
	scheme := newLocalScheme()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	pkg := &stubPkgManager{bin: dlvBin, grammar: grammarDir(t)}
	dapCfg := idedebug.Config{
		MaxRetries:        1,
		InitializeTimeout: 10 * time.Second,
		Adapters:          map[string]idedebug.AdapterConfig{"go": goAdapterConfig(dlvBin)},
	}
	mgr := idedebug.New(uri, scheme, pkg, dapCfg)

	br := newFakeBrowser()
	tile := &fakeWindow{id: 1, content: &texttest.TestEditorHandler{}}
	shellW := &fakeWindow{id: 2, content: newFakeShellHandler()}
	br.windows = []*fakeWindow{tile, shellW}
	ed := newFakeTextapiEditor()
	// Load main.go contents into the editor's CellView so the
	// stopped-line marker can compute the line length.
	mainBytes, err := os.ReadFile(filepath.Join(dir, "main.go"))
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

func (h *e2eHarness) close() {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	_ = h.mgr.Close()
	_ = h.scheme.Close()
	h.cancel()
}

func (h *e2eHarness) notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) {
	formatted := msg
	if len(args) > 0 {
		formatted = sprintf(msg, args...)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.milestones = append(h.milestones, milestone{level, formatted})
	h.cond.Broadcast()
	h.t.Logf("milestone: %s", formatted)
}

// waitMilestone blocks until a milestone whose message contains
// substr is recorded, or fails the test on timeout.
func (h *e2eHarness) waitMilestone(
	t *testing.T, substr string, timeout time.Duration,
) {
	h.waitMilestoneAfter(t, 0, substr, timeout)
}

func (h *e2eHarness) waitMilestoneAfter(
	t *testing.T, start int, substr string, timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := start
	for {
		for i := seen; i < len(h.milestones); i++ {
			if containsString(h.milestones[i].msg, substr) {
				return
			}
		}
		seen = len(h.milestones)
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for milestone %q; got %v",
				substr, h.milestones)
		}
		// Use a timer goroutine to wake the cond on timeout.
		done := make(chan struct{})
		go func() {
			select {
			case <-time.After(time.Until(deadline) + 50*time.Millisecond):
				h.cond.Broadcast()
			case <-done:
			}
		}()
		h.cond.Wait()
		close(done)
	}
}

func (h *e2eHarness) run(
	ctx context.Context, sub string, args ...string,
) (iterator.Iterator[component.Responsive], error) {
	cmdArgs := append([]string{sub}, args...)
	it, err := h.h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: cmdArgs,
	}, nil)
	return it, err
}

// drainIterator consumes the event iterator returned by
// `debugger initialize` so the session's internal channel does
// not back up. Items are discarded.
func (h *e2eHarness) drainIterator(it iterator.Iterator[component.Responsive]) {
	if it == nil {
		return
	}
	defer it.Close()
	for {
		_, ok := it.Next(h.ctx)
		if !ok {
			return
		}
	}
}

func (h *e2eHarness) setBreakpoint(t *testing.T, path string, line int) {
	t.Helper()
	h.h.mu.Lock()
	h.h.breakpoints[path] = []int{line}
	h.h.mu.Unlock()
}

func (h *e2eHarness) firstThreadID(t *testing.T) int {
	t.Helper()
	sid := h.sessionID(t)
	threads, err := h.mgr.Threads(h.ctx, sid)
	require.NoError(t, err)
	require.NotEmpty(t, threads)
	return threads[0].Id
}

func (h *e2eHarness) sessionID(t *testing.T) string {
	t.Helper()
	h.h.mu.Lock()
	sid := h.h.sessionID
	h.h.mu.Unlock()
	require.NotEmpty(t, sid, "no active session")
	return sid
}

func (h *e2eHarness) stackTrace(t *testing.T, threadID int) []dap.StackFrame {
	t.Helper()
	sid := h.sessionID(t)
	resp, err := h.mgr.StackTrace(h.ctx, sid, &dap.StackTraceArguments{
		ThreadId: threadID, Levels: 5,
	})
	require.NoError(t, err)
	return resp.StackFrames
}

func TestWaitStoppedOrCompleted(t *testing.T) {
	tests := []struct {
		name          string
		milestones    []milestone
		start         int
		wantCompleted bool
		wantOK        bool
	}{
		{
			name:       "stopped",
			milestones: []milestone{{msg: "debugger: stopped (breakpoint) on thread 1"}},
			wantOK:     true,
		},
		{
			name:          "debuggee exited",
			milestones:    []milestone{{msg: "debugger: debuggee exited (code 0)"}},
			wantCompleted: true,
			wantOK:        true,
		},
		{
			name:          "terminated fallback",
			milestones:    []milestone{{msg: "debugger: terminated"}},
			wantCompleted: true,
			wantOK:        true,
		},
		{
			name:       "prior exit is ignored",
			milestones: []milestone{{msg: "debugger: debuggee exited (code 0)"}},
			start:      1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &e2eHarness{milestones: tt.milestones}
			h.cond = sync.NewCond(&h.mu)

			completed, ok := h.waitStoppedOrCompleted(tt.start, 0)
			assert.Equal(t, tt.wantCompleted, completed)
			assert.Equal(t, tt.wantOK, ok)
		})
	}
}

func (h *e2eHarness) continueUntilExit(
	t *testing.T, threadID int, timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		milestoneStart := h.milestoneCount()
		_, err := h.run(h.ctx, subContinue, strconv.Itoa(threadID))
		require.NoError(t, err)

		completed, ok := h.waitStoppedOrCompleted(milestoneStart, time.Until(deadline))
		if !ok {
			break
		}
		if completed {
			return
		}
	}
	t.Fatalf("debuggee did not exit within %s", timeout)
}

func (h *e2eHarness) milestoneCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.milestones)
}

func (h *e2eHarness) waitStoppedOrCompleted(
	start int, d time.Duration,
) (completed, ok bool) {
	deadline := time.Now().Add(d)
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := start
	for {
		for i := seen; i < len(h.milestones); i++ {
			m := h.milestones[i]
			if containsString(m.msg, "debuggee exited") ||
				containsString(m.msg, "terminated") {
				return true, true
			}
			if containsString(m.msg, "stopped") {
				return false, true
			}
		}
		seen = len(h.milestones)
		if !time.Now().Before(deadline) {
			return false, false
		}
		done := make(chan struct{})
		go func() {
			select {
			case <-time.After(time.Until(deadline) + 10*time.Millisecond):
				h.cond.Broadcast()
			case <-done:
			}
		}()
		h.cond.Wait()
		close(done)
	}
}

// localScheme implements schemeapi.Executor using the local OS
// process table. It mirrors ide/idelsp/go_test.go's localScheme
// but only the executor surface (not the file-system surface)
// is exercised here, since the debugger only spawns processes.
type localScheme struct {
	mu      sync.Mutex
	procs   map[workspaceapi.Pid]*os.Process
	nextPid workspaceapi.Pid
}

func newLocalScheme() *localScheme {
	return &localScheme{
		procs:   make(map[workspaceapi.Pid]*os.Process),
		nextPid: 1,
	}
}

func (s *localScheme) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	if cmd.Dir != "" {
		c.Dir = cmd.Dir
	}
	if cmd.Env != nil {
		c.Env = cmd.Env
	}
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	if cmd.SysProcAttr != nil {
		c.SysProcAttr = cmd.SysProcAttr
	}
	if err := c.Start(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	pid := s.nextPid
	s.nextPid++
	s.procs[pid] = c.Process
	s.mu.Unlock()
	if cmd.Watcher != nil {
		ch := cmd.Watcher.WatchProcess()
		go func() {
			err := c.Wait()
			if ch != nil {
				ch <- err
			}
		}()
	}
	return pid, nil
}

func (s *localScheme) Signal(
	pid workspaceapi.Pid, sig syscall.Signal,
) error {
	s.mu.Lock()
	proc, ok := s.procs[pid]
	s.mu.Unlock()
	if !ok {
		return errors.New("process not found")
	}
	return proc.Signal(sig)
}

func (s *localScheme) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.procs {
		_ = p.Signal(syscall.SIGKILL)
	}
	return nil
}

// findDlv returns the path to the dlv binary or skips the test
// when none is available on the system.
func findDlv(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("dlv")
	if err == nil {
		return bin
	}
	for _, p := range []string{
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "dlv"),
		filepath.Join(os.Getenv("HOME"), "go", "bin", "dlv"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("dlv not found, skipping debugger e2e test")
	return ""
}

// setupBuggy copies testdata/buggy to a fresh temp directory and
// returns its path. The directory is cleaned up automatically.
func setupBuggy(t *testing.T) string {
	t.Helper()
	warmBuggyBuildCache(t)
	tmp, err := os.MkdirTemp("", "debugshell-e2e-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmp) })
	// Resolve the temp dir's real path. On macOS,
	// os.MkdirTemp returns /var/folders/... which is a symlink
	// to /private/var/folders/... . The Go toolchain compiles
	// debug info with the resolved path, so dlv only matches
	// breakpoints against the resolved variant.
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = real
	}
	src := "testdata/buggy"
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

var (
	warmBuggyOnce sync.Once
	warmBuggyOut  []byte
	warmBuggyErr  error
)

// warmBuggyBuildCache compiles the fixture once with the flags dlv
// uses for launch mode "debug". Every parallel test otherwise makes
// its own dlv rebuild the whole standard library with -N -l from a
// cold cache at the same time, and dlv only reports "initialized"
// after that build; on a small CI runner the fan-out overran the
// initialize timeout. The dependency objects are cached by content,
// so the per-test build in a temp copy is then a cache hit.
func warmBuggyBuildCache(t *testing.T) {
	t.Helper()
	warmBuggyOnce.Do(func() {
		build := exec.Command("go", "build", "-gcflags=all=-N -l", "-o", os.DevNull, ".")
		build.Dir = "testdata/buggy"
		build.Env = append(os.Environ(), "GOFLAGS=")
		warmBuggyOut, warmBuggyErr = build.CombinedOutput()
	})
	require.NoError(t, warmBuggyErr, "warm build cache: %s", warmBuggyOut)
}

func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}

// goAdapterConfig mirrors the Delve adapter entry shipped in the Go
// language package's config.yaml so the e2e harness drives the same
// launch/attach templates as production rather than relying on
// host-side defaults.
func goAdapterConfig(dlvBin string) idedebug.AdapterConfig {
	return idedebug.AdapterConfig{
		Command:    []string{dlvBin, "dap", "--listen={addr}"},
		AdapterID:  "dlv-dap",
		LaunchArgs: map[string]string{"mode": "debug", "outputMode": "remote"},
		AttachArgs: map[string]string{"mode": "local"},
	}
}

func containsString(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

// findLocation returns the first location installed under id.
func (h *e2eHarness) findLocation(t *testing.T, id string) textapi.Location {
	t.Helper()
	h.ed.mu.Lock()
	defer h.ed.mu.Unlock()
	locs := h.ed.setLocationLists[id]
	require.NotEmpty(t, locs, "location list %q not installed", id)
	return locs[0]
}

func (h *e2eHarness) waitLocation(
	t *testing.T, id string, timeout time.Duration,
) {
	t.Helper()
	require.Eventually(t, func() bool {
		h.ed.mu.Lock()
		defer h.ed.mu.Unlock()
		return len(h.ed.setLocationLists[id]) > 0
	}, timeout, 20*time.Millisecond,
		"timed out waiting for location list %q", id)
}

func (h *e2eHarness) locationPrio(
	t *testing.T, id string,
) textapi.LocationPriority {
	t.Helper()
	h.ed.mu.Lock()
	defer h.ed.mu.Unlock()
	prio, ok := h.ed.setLocationByID[id]
	require.True(t, ok, "location list %q not installed", id)
	return prio
}

func (h *e2eHarness) cursor(t *testing.T) term.Coordinates {
	t.Helper()
	h.ed.mu.Lock()
	defer h.ed.mu.Unlock()
	return h.ed.cursor
}

// allLocations returns every location installed under id, in
// insertion order.
func (h *e2eHarness) allLocations(
	t *testing.T, id string,
) []textapi.Location {
	t.Helper()
	h.ed.mu.Lock()
	defer h.ed.mu.Unlock()
	return append([]textapi.Location(nil), h.ed.setLocationLists[id]...)
}

// functionLineRange returns the 0-based [start, end] line
// range of the named top-level function in path. Used to
// assert that locations in a sibling function are filtered out.
func (h *e2eHarness) functionLineRange(
	t *testing.T, path, name string,
) (int, int) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	start := -1
	depth := 0
	for i, line := range lines {
		if start < 0 {
			if strings.HasPrefix(line, "func "+name+"(") ||
				strings.HasPrefix(line, "func "+name+" (") {
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
		"could not locate function %q in %s", name, path)
	return 0, 0
}

func (h *e2eHarness) runPrompt(
	ctx context.Context, path string, cursor term.Coordinates,
	args ...string,
) error {
	_, err := h.runPromptOnHandler(ctx, path, cursor, args...)
	return err
}

// runPromptOnHandler is like runPrompt but returns the
// text.Handler used as the command's Resource so callers can
// inspect side effects (e.g. installed location lists) on it.
func (h *e2eHarness) runPromptOnHandler(
	ctx context.Context, path string, cursor term.Coordinates,
	args ...string,
) (*texttest.TestEditorHandler, error) {
	uri, err := workspaceapi.ParseURI("file://" + path)
	if err != nil {
		return nil, err
	}
	hndl := &texttest.TestEditorHandler{}
	cmd := textapi.Command{
		Name:     CommandName,
		Args:     args,
		URI:      uri,
		Resource: hndl,
		Cursor: struct {
			Content term.Coordinates
			Window  term.Coordinates
		}{Content: cursor},
	}
	return hndl, NewPromptHandler(h.h).HandleCommand(ctx, cmd)
}

type fakeCellView struct{ cells [][]term.Cell }

func (v *fakeCellView) RawCells() ([][]term.Cell, error) {
	return v.cells, nil
}

func cellsFromString(text string) [][]term.Cell {
	lines := strings.Split(text, "\n")
	out := make([][]term.Cell, 0, len(lines))
	for _, line := range lines {
		row := make([]term.Cell, 0, len(line))
		for _, r := range line {
			row = append(row, term.Cell{Ch: r})
		}
		out = append(out, row)
	}
	return out
}

// grammarDir returns the absolute path of the directory holding
// the Go tree-sitter grammar (parser.so + *.scm) used by the
// debugshell's variables location list. Files live in
// ide/syntax/syntaxtest/go and are reused here to avoid a
// build-time dependency on a packaged grammar.
func grammarDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	abs, err := filepath.Abs(filepath.Join(
		wd, "..", "..", "syntax", "syntaxtest", "go"))
	require.NoError(t, err)
	if _, err := os.Stat(filepath.Join(abs, hostParserRel())); err != nil {
		t.Skipf("tree-sitter grammar not found at %s: %v", abs, err)
	}
	return abs
}

// newFakeShellHandler returns an *ideconsole.Handler purely to
// use as a content-type marker for fakeWindow.Content(). The
// handler is never started; debugshell.findShellWindow only
// checks the dynamic type via type-assert.
func newFakeShellHandler() *ideconsole.Handler {
	h, _ := ideconsole.New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		nopShellEditor{},
		ideconsole.Config{},
	)
	return h
}

// nopShellEditor is a minimal command.Editor for tests that need an
// *ideconsole.Handler purely as a content-type marker. The spawned
// handler is never driven.
type nopShellEditor struct{}

func (nopShellEditor) Edit(*cell.Buffer) command.EditHandler {
	return nopShellEditHandler{}
}

type nopShellEditHandler struct{}

func (nopShellEditHandler) Resize(int, int)  {}
func (nopShellEditHandler) Draw(term.Writer) {}
func (nopShellEditHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleSteadyBar, false
}
func (nopShellEditHandler) CursorAtScroll() term.Coordinates        { return term.Coordinates{} }
func (nopShellEditHandler) SetCursorAtScroll(term.Coordinates) bool { return true }
func (nopShellEditHandler) Selection() (string, bool)               { return "", false }
func (nopShellEditHandler) Handle(term.Event) (bool, bool)          { return false, false }

// outputPath returns the absolute path of the active output
// capture file, or "" when no session has been launched.
func (h *e2eHarness) outputPath(t *testing.T) string {
	t.Helper()
	h.h.mu.Lock()
	defer h.h.mu.Unlock()
	if h.h.output == nil {
		return ""
	}
	return h.h.output.Path()
}

func TestE2E_OutputAppearsInIDEEditorBuffer(t *testing.T) {
	t.Parallel()
	dlvBin := findDlv(t)
	tmpDir := setupBuggy(t)

	h := newIDEHarness(t, dlvBin, tmpDir)
	defer h.close()

	ctx := h.ctx

	it, err := h.run(ctx, subInitialize, "go")
	require.NoError(t, err)
	go h.drainIterator(it)

	_, err = h.run(ctx, subLaunch, tmpDir)
	require.NoError(t, err)
	h.waitMilestone(t, "initialized", 10*time.Second)

	_, err = h.run(ctx, subConfigured)
	require.NoError(t, err)

	// Capture the sink path *before* termination — the
	// session's OnClose hook calls stopOutputCapture which
	// nils handler.output and would make outputPath() return
	// "" if asked after terminate. The file itself is left on
	// disk for inspection.
	require.Eventually(t, func() bool {
		return h.outputPath(t) != ""
	}, 5*time.Second, 20*time.Millisecond,
		"sink path never set")
	outPath := h.outputPath(t)

	// Simulate the user opening the per-session log file in
	// the IDE — debugshell intentionally does not auto-open
	// or split the shell window for it (the path is reported
	// to the user instead). Once the file is open in a tab,
	// the harness's pollAndReload picks up Write events and
	// the buffer reflects on-disk content.
	outURI, err := workspaceapi.ParseURI("file://" + outPath)
	require.NoError(t, err)
	h.uiMu.Lock()
	_, err = h.comp.Open(outURI)
	h.uiMu.Unlock()
	require.NoError(t, err)

	// Wait for the debuggee to print "Sum: 21" and exit.
	h.waitMilestone(t, "terminated", 15*time.Second)

	require.NotEmpty(t, outPath, "output capture path")
	require.True(t, strings.HasPrefix(outPath, tmpDir),
		"sink must live in workspace dir for FS watcher; got %s", outPath)

	// The on-disk file must contain Sum: 21 — proves the
	// adapter actually piped stdout through to our sink.
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(outPath)
		return err == nil && strings.Contains(string(data), "Sum: 21")
	}, 10*time.Second, 50*time.Millisecond,
		"on-disk sink never received Sum: 21")

	// And — the bug reproducer — the IDE's editor buffer for
	// the same file must also reflect that content. Without
	// the workspace-dir + fsync fix, the watcher never fires,
	// ReloadTab is never called, and this buffer stays empty.
	require.Eventually(t, func() bool {
		return strings.Contains(h.bufferContent(outPath), "Sum: 21")
	}, 10*time.Second, 100*time.Millisecond,
		"editor buffer never reflected Sum: 21; got %q",
		h.bufferContent(outPath))

	_, _ = h.run(ctx, subTerminate)
}

func TestE2E_StoppedLocationWithCursorAlreadyOnLine(t *testing.T) {
	t.Parallel()
	dlvBin := findDlv(t)
	tmpDir := setupBuggy(t)
	mainPath := filepath.Join(tmpDir, "main.go")

	h := newIDEHarness(t, dlvBin, tmpDir)
	defer h.close()

	ctx := h.ctx

	mainURI, err := workspaceapi.ParseURI("file://" + mainPath)
	require.NoError(t, err)

	// Park the cursor exactly where the debuggee will stop.
	stopPos := term.Coordinates{X: 0, Y: sumFirstStmtLine - 1}
	h.uiMu.Lock()
	_, err = h.comp.Open(mainURI)
	require.NoError(t, err)
	ed, err := h.comp.Editor(mainURI)
	require.NoError(t, err)
	ed.Resize(120, 40)
	ed.SetCursorAtScroll(stopPos)
	cur := ed.CursorAtScroll()
	h.uiMu.Unlock()
	require.Equal(t, stopPos, cur, "precondition: cursor parked on breakpoint line")

	it, err := h.run(ctx, subInitialize, "go")
	require.NoError(t, err)
	go h.drainIterator(it)

	_, err = h.run(ctx, subLaunch, tmpDir)
	require.NoError(t, err)
	h.waitMilestone(t, "initialized", 10*time.Second)

	h.setBreakpoint(t, mainPath, sumFirstStmtLine)

	_, err = h.run(ctx, subConfigured)
	require.NoError(t, err)
	h.waitMilestone(t, "stopped", 15*time.Second)

	var stopLoc textapi.Location
	require.Eventually(t, func() bool {
		set, ok := h.locationSet(mainURI, stoppedLocationID)
		if !ok || len(set.Locations) == 0 {
			return false
		}
		stopLoc = set.Locations[0]
		return true
	}, 10*time.Second, 50*time.Millisecond,
		"stopped location list never installed with cursor already on the line")

	assert.Equal(t, sumFirstStmtLine-1, stopLoc.From.Y)
	assert.Equal(t, term.ColorYellow, stopLoc.Attr.Bg)
	assert.Equal(t, term.ColorBlack, stopLoc.Attr.Fg)

	_, _ = h.run(ctx, subTerminate)
}

// ideHarness is an e2e harness that uses a real text.Component
// as both browser.Browser and textapi.Editor — the production
// wiring — plus a file-scheme watcher that reloads open tabs on
// disk Write events, exactly as ide.dispatchFilesystemEvent
// does. The point is to exercise the full path from "DAP
// adapter writes to a file" through to "the IDE editor buffer
// the user is looking at reflects the new content".
type ideHarness struct {
	t      *testing.T
	ctx    context.Context
	cancel context.CancelFunc

	scheme   schemeapi.Scheme
	ws       workspace.Workspace
	comp     *text.Component
	mgr      *idedebug.Manager
	h        *Handler
	procExec *localScheme

	// uiMu serializes IDE-resource mutations the way the
	// production ide.workspace_handler does: every call into
	// the text.Component (open/reload/split/setFocus) is held
	// behind this mutex. The harness's ScheduleNextTick and
	// FS-event dispatcher both grab it.
	uiMu sync.Mutex

	mu         sync.Mutex
	milestones []milestone
	cond       *sync.Cond
}

// uiSchedule is the harness's stand-in for the host event loop's
// ScheduleNextTick: callbacks run inline but serialized behind uiMu
// so buffer mutations from async workers cannot race the test's
// IDE-resource reads.
func (h *ideHarness) uiSchedule(fn func()) bool {
	h.uiMu.Lock()
	defer h.uiMu.Unlock()
	fn()
	return true
}

func newIDEHarness(t *testing.T, dlvBin, dir string) *ideHarness {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	hh := &ideHarness{t: t}
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	// The reload worker mutates the editor buffer through this
	// scheduler; it must hold uiMu like every other IDE-resource
	// access or bufferContent races with in-flight reloads.
	ws := workspace.NewSchemeWorkspace(uri, scheme, hh.uiSchedule)

	ed := vi.Editor(vi.WithStatusBarConfig(false, text.StatusBarConfig{
		Publisher:        texttest.NopEditor(),
		ScheduleNextTick: hh.uiSchedule,
	}))
	tcfg := text.DefaultConfig()
	// Flush and reload completions land on the browser tabs through
	// this scheduler from worker goroutines; like every other UI
	// mutation in the harness they must be serialized behind uiMu.
	tcfg.ScheduleNextTick = hh.uiSchedule
	comp, err := text.NewComponent(ed, ws, tcfg)
	require.NoError(t, err)
	comp.Browser().Resize(120, 40)

	procExec := newLocalScheme()
	pkg := &stubPkgManager{bin: dlvBin}
	dapCfg := idedebug.Config{
		MaxRetries:        1,
		InitializeTimeout: 10 * time.Second,
		Adapters:          map[string]idedebug.AdapterConfig{"go": goAdapterConfig(dlvBin)},
	}
	mgr := idedebug.New(uri, procExec, pkg, dapCfg)

	hh.scheme = scheme
	hh.ws = ws
	hh.comp = comp
	hh.mgr = mgr
	hh.procExec = procExec
	hh.cond = sync.NewCond(&hh.mu)

	hh.h = nil
	apiEd := newCompEditorAdapter(comp)
	h := New(mgr, comp, apiEd, passThroughParser{}, passThroughFS{}, Config{
		WorkspaceURI:     uri,
		Debugger:         dapCfg,
		ScheduleNextTick: hh.uiSchedule,
	}).WithNotify(hh.notify)
	hh.h = h

	// In production the IDE wires a kqueue/FSEvents watcher
	// via ide/events.go's dispatchFilesystemEvent and reloads
	// open tabs on Write events. Replicating that in tests is
	// flaky on macOS because FSEvents coalesces Create+Write
	// for short-lived sessions, so the harness uses a
	// polling stand-in with the same observable effect: the
	// editor buffer reflects the on-disk content. See
	// pollAndReload below.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	hh.ctx = ctx
	hh.cancel = cancel

	go hh.pollAndReload(ctx, dir)

	return hh
}

func (h *ideHarness) close() {
	_ = h.mgr.Close()
	_ = h.scheme.Close()
	h.cancel()
}

func (h *ideHarness) notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) {
	formatted := msg
	if len(args) > 0 {
		formatted = fmt.Sprintf(msg, args...)
	}
	h.mu.Lock()
	h.milestones = append(h.milestones, milestone{level, formatted})
	h.cond.Broadcast()
	h.mu.Unlock()
	h.t.Logf("milestone: %s", formatted)
}

// pollAndReload is a deterministic stand-in for the
// production IDE's FS-watcher → ReloadTab pipeline. Every
// 50ms it walks the workspace dir and, for any open tab whose
// on-disk size has grown since the previous tick, calls
// ReloadTab. Avoids relying on FSEvents (which on macOS
// coalesces Create+Write events for short-lived sessions and
// therefore makes the watcher-based variant flaky in tests).
func (h *ideHarness) pollAndReload(ctx context.Context, dir string) {
	sizes := make(map[string]int64)
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			h.pollOnce(dir, sizes)
		}
	}
}

func (h *ideHarness) pollOnce(dir string, sizes map[string]int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	h.uiMu.Lock()
	defer h.uiMu.Unlock()
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		size := info.Size()
		if sizes[path] == size {
			continue
		}
		sizes[path] = size
		uri, err := workspaceapi.ParseURI("file://" + path)
		if err != nil {
			continue
		}
		t, open := h.comp.Resource(uri)
		if !open {
			continue
		}
		_, _ = h.comp.ReloadTab(context.Background(), t)
	}
}

func (h *ideHarness) run(
	ctx context.Context, sub string, args ...string,
) (iterator.Iterator[component.Responsive], error) {
	cmdArgs := append([]string{sub}, args...)
	return h.h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: cmdArgs,
	}, nil)
}

func (h *ideHarness) drainIterator(it iterator.Iterator[component.Responsive]) {
	if it == nil {
		return
	}
	defer it.Close()
	for {
		_, ok := it.Next(h.ctx)
		if !ok {
			return
		}
	}
}

func (h *ideHarness) waitMilestone(
	t *testing.T, substr string, timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	h.mu.Lock()
	defer h.mu.Unlock()
	seen := 0
	for {
		for i := seen; i < len(h.milestones); i++ {
			if strings.Contains(h.milestones[i].msg, substr) {
				return
			}
		}
		seen = len(h.milestones)
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for milestone %q; got %v",
				substr, h.milestones)
		}
		done := make(chan struct{})
		go func() {
			select {
			case <-time.After(time.Until(deadline) + 50*time.Millisecond):
				h.cond.Broadcast()
			case <-done:
			}
		}()
		h.cond.Wait()
		close(done)
	}
}

func (h *ideHarness) outputPath(t *testing.T) string {
	t.Helper()
	h.h.mu.Lock()
	defer h.h.mu.Unlock()
	if h.h.output == nil {
		return ""
	}
	return h.h.output.Path()
}

func (h *ideHarness) setBreakpoint(t *testing.T, path string, line int) {
	t.Helper()
	h.h.mu.Lock()
	h.h.breakpoints[path] = []int{line}
	h.h.mu.Unlock()
}

// locationSet reads the location list installed under id
// directly from the real editor handler for uri.
func (h *ideHarness) locationSet(
	uri workspaceapi.URI, id string,
) (text.LocationSet, bool) {
	h.uiMu.Lock()
	defer h.uiMu.Unlock()
	hd, err := h.comp.Editor(uri)
	if err != nil {
		return text.LocationSet{}, false
	}
	for _, ls := range hd.LocationLists() {
		if ls.ID == id {
			return ls, true
		}
	}
	return text.LocationSet{}, false
}

// bufferContent returns the IDE editor buffer's current
// contents for the file at path, joining all rows. Returns ""
// when the file is not open or RawCells fails.
func (h *ideHarness) bufferContent(path string) string {
	h.uiMu.Lock()
	defer h.uiMu.Unlock()
	uri, err := workspaceapi.ParseURI("file://" + path)
	if err != nil {
		return ""
	}
	hd, err := h.comp.Editor(uri)
	if err != nil {
		return ""
	}
	view := hd.CellView()
	if view == nil {
		return ""
	}
	rows := view.RawCells()
	var b strings.Builder
	for _, row := range rows {
		for _, c := range row {
			b.WriteRune(c.Ch)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// newCompEditorAdapter exposes a *text.Component as a
// textapi.Editor for the debugshell.Handler. It mirrors the
// production ide.editorAdapter (which is unexported) and is
// only used by the IDE-level e2e harness — handlers that the
// debugshell installs (cursor, location lists) flow through
// the real editor; CellEditor is not exercised here so it
// returns nil.
type compEditorAdapter struct {
	c *text.Component
}

func newCompEditorAdapter(c *text.Component) textapi.Editor {
	return &compEditorAdapter{c: c}
}

var _ textapi.Editor = (*compEditorAdapter)(nil)

func (a *compEditorAdapter) SubscribeEvents(
	types []textapi.EventType, h textapi.EventHandler,
) error {
	return a.c.SubscribeEvents(types, h)
}

func (a *compEditorAdapter) Editor(
	uri workspaceapi.URI,
) (textapi.Handler, error) {
	return a.c.Editor(uri)
}

func (a *compEditorAdapter) SetLocationList(
	h textapi.Handler, p textapi.LocationPriority,
	id string, l textapi.LocationList,
) error {
	eh, ok := h.(text.Handler)
	if !ok {
		return errors.New("not a text.Handler")
	}
	eh.SetLocationList(p, id, l)
	return nil
}

func (a *compEditorAdapter) MoveToNextLocation(
	h textapi.Handler, id string,
) error {
	eh, ok := h.(text.Handler)
	if !ok {
		return errors.New("not a text.Handler")
	}
	if !eh.MoveToNextLocation(id) {
		return fmt.Errorf("no next location for %s", id)
	}
	return nil
}

func (a *compEditorAdapter) MoveToPrevLocation(
	h textapi.Handler, id string,
) error {
	eh, ok := h.(text.Handler)
	if !ok {
		return errors.New("not a text.Handler")
	}
	if !eh.MoveToPrevLocation(id) {
		return fmt.Errorf("no prev location for %s", id)
	}
	return nil
}

func (a *compEditorAdapter) Cursor(
	h textapi.Handler,
) (term.Coordinates, error) {
	eh, ok := h.(text.Handler)
	if !ok {
		return term.Coordinates{}, errors.New("not a text.Handler")
	}
	return eh.CursorAtScroll(), nil
}

func (a *compEditorAdapter) SetCursor(
	h textapi.Handler, pos term.Coordinates,
) error {
	eh, ok := h.(text.Handler)
	if !ok {
		return errors.New("not a text.Handler")
	}
	if !eh.SetCursorAtScroll(pos) {
		return fmt.Errorf("could not set cursor (%d, %d)", pos.X, pos.Y)
	}
	return nil
}

func (a *compEditorAdapter) CellView(
	h textapi.Handler,
) textapi.CellView {
	eh, ok := h.(text.Handler)
	if !ok {
		return nil
	}
	return compCellView{eh.CellView()}
}

func (a *compEditorAdapter) CellEditor(
	h textapi.Handler,
) textapi.CellEditor {
	return nil
}

func (a *compEditorAdapter) SetDefaultAttributes(
	h textapi.Handler, attr term.Attributes,
) error {
	eh, ok := h.(text.Handler)
	if !ok {
		return errors.New("not a text.Handler")
	}
	eh.SetDefaultAttributes(attr)
	return nil
}

type compCellView struct{ v cell.View }

func (v compCellView) RawCells() ([][]term.Cell, error) {
	return v.v.RawCells(), nil
}

func TestE2E_CtrlCDoesNotStopEventStream(t *testing.T) {
	t.Parallel()
	dlvBin := findDlv(t)
	tmpDir := setupBuggy(t)
	mainPath := filepath.Join(tmpDir, "main.go")

	h := newE2EHarness(t, dlvBin, tmpDir)
	defer h.close()

	// initCtx mirrors the REPL's per-command ctx for
	// `debugger initialize`. We cancel it midway through the
	// session to simulate the user pressing Ctrl-C.
	initCtx, initCancel := context.WithCancel(h.ctx)
	defer initCancel()

	// 1. Initialize. Drain the returned iterator with the
	// SAME initCtx the REPL would use. This is the goroutine
	// that renders the session transcript.
	it, err := h.run(initCtx, subInitialize, "go")
	require.NoError(t, err)

	// Count items the iterator delivers, with the SAME
	// initCtx the REPL's drain goroutine would use. The
	// counter is the bug-reproducer: after Ctrl-C cancels
	// initCtx, this loop must keep growing as the adapter
	// emits more events; before the fix it stopped the moment
	// initCtx was cancelled.
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

	// 2. Launch the buggy program (own short ctx — this one
	// is supposed to be cancellable per command, that's fine).
	_, err = h.run(h.ctx, subLaunch, tmpDir)
	require.NoError(t, err)

	// 3. Wait for the adapter's "initialized" milestone, set
	// a breakpoint, then simulate Ctrl-C BEFORE configured.
	// The cancel must not affect subsequent event flow.
	h.waitMilestone(t, "initialized", 10*time.Second)
	h.setBreakpoint(t, mainPath, sumFirstStmtLine)

	// >>> Ctrl-C <<<
	initCancel()

	// Give the drain goroutine a moment to observe the
	// cancellation, in case it (incorrectly) reacts to it.
	time.Sleep(100 * time.Millisecond)
	drainMu.Lock()
	beforeConfigured := received
	drainMu.Unlock()

	// 4. Mark configuration done — this triggers the
	// breakpoint hit and a StoppedEvent.
	_, err = h.run(h.ctx, subConfigured)
	require.NoError(t, err)

	// 5. Wait for the stopped milestone (the notify hook
	// always fires regardless of the iterator).
	h.waitMilestone(t, "stopped", 10*time.Second)

	// 6. The bug reproducer: the iterator's drain loop must
	// have observed *new* items after Ctrl-C. A StoppedEvent
	// pushes at least two items (the auto stack-trace plus
	// the StoppedEvent itself). Before the fix the count
	// stayed at beforeConfigured because Next returned
	// ok=false on ctx cancel.
	require.Eventually(t, func() bool {
		drainMu.Lock()
		defer drainMu.Unlock()
		return received > beforeConfigured+1
	}, 10*time.Second, 50*time.Millisecond,
		"session iterator delivered no new items after Ctrl-C "+
			"and StoppedEvent — the cancelled per-command "+
			"context killed event flow")

	// Cleanup. continueUntilExit also drains any further
	// breakpoint hits.
	threadID := h.firstThreadID(t)
	h.continueUntilExit(t, threadID, 15*time.Second)
	_, _ = h.run(h.ctx, subTerminate)

	// The drain goroutine must terminate cleanly once the
	// session closes — independent of initCtx already being
	// cancelled.
	select {
	case <-doneDrain:
	case <-time.After(5 * time.Second):
		t.Fatal("drain goroutine did not exit after terminate")
	}
}
