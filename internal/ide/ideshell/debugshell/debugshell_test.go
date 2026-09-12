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

package debugshell

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-dap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/debugapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/ide/idedebug"
	"unstable.build/rune/internal/text/texttest"
)

// fakeDebugger is a session-based debugapi.Debugger stub. It
// records calls for each subcommand and supports a configurable
// CreateSession result so tests can exercise the session
// lifecycle end-to-end.
type fakeDebugger struct {
	mu sync.Mutex

	// CreateSession behaviour.
	nextSessionID string
	createCalls   []string // langID per call
	createClient  []debugapi.ClientCapabilities
	createErr     error
	subscriber    debugapi.EventSubscriber
	// connectCalls records the (langID, addr) pairs passed to
	// CreateSessionConnect.
	connectCalls [][2]string

	launchCalls     []debugapi.LaunchRequestArguments
	attachCalls     []debugapi.AttachRequestArguments
	configDoneCalls int
	terminateCalls  int
	terminateErr    error
	disconnectCalls []*dap.DisconnectArguments
	disconnectErr   error
	restartCalls    int
	continueCalls   []*dap.ContinueArguments
	nextCalls       []*dap.NextArguments
	threadsResponse []dap.Thread
	stackResponse   *dap.StackTraceResponseBody
	scopesResponse  []dap.Scope
	variablesResp   []dap.Variable
	setBpCalls      []*dap.SetBreakpointsArguments
	setVarResp      *dap.SetVariableResponseBody
	evaluateCalls   []*dap.EvaluateArguments
	threadsCalls    int
	stackTraceArgs  []*dap.StackTraceArguments
}

func newFakeDebugger() *fakeDebugger {
	return &fakeDebugger{
		nextSessionID:   "s-test",
		threadsResponse: []dap.Thread{{Id: 1, Name: "main"}},
		stackResponse: &dap.StackTraceResponseBody{
			StackFrames: []dap.StackFrame{{
				Id: 42, Name: "main.main", Line: 10,
				Source: &dap.Source{Path: "/tmp/file.go"},
			}},
		},
		scopesResponse: []dap.Scope{{Name: "Local", VariablesReference: 7}},
		variablesResp:  []dap.Variable{{Name: "x", Value: "1", Type: "int"}},
		setVarResp:     &dap.SetVariableResponseBody{Value: "42"},
	}
}

func (f *fakeDebugger) CreateSession(
	_ context.Context, langID string,
	client debugapi.ClientCapabilities, sub debugapi.EventSubscriber,
) (string, *dap.Capabilities, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.createCalls = append(f.createCalls, langID)
	f.createClient = append(f.createClient, client)
	if f.createErr != nil {
		return "", nil, f.createErr
	}
	f.subscriber = sub
	return f.nextSessionID, &dap.Capabilities{}, nil
}

func (f *fakeDebugger) CreateSessionConnect(
	_ context.Context, langID, addr string,
	client debugapi.ClientCapabilities, sub debugapi.EventSubscriber,
) (string, *dap.Capabilities, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connectCalls = append(f.connectCalls, [2]string{langID, addr})
	f.createClient = append(f.createClient, client)
	if f.createErr != nil {
		return "", nil, f.createErr
	}
	f.subscriber = sub
	return f.nextSessionID, &dap.Capabilities{}, nil
}

func (f *fakeDebugger) Launch(
	_ context.Context, _ string, a debugapi.LaunchRequestArguments,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.launchCalls = append(f.launchCalls, a)
	return nil
}

func (f *fakeDebugger) Attach(
	_ context.Context, _ string, a debugapi.AttachRequestArguments,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attachCalls = append(f.attachCalls, a)
	return nil
}

func (f *fakeDebugger) ConfigurationDone(_ context.Context, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configDoneCalls++
	return nil
}

func (f *fakeDebugger) Disconnect(
	_ context.Context, _ string, a *dap.DisconnectArguments,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnectCalls = append(f.disconnectCalls, a)
	return f.disconnectErr
}

func (f *fakeDebugger) Terminate(
	_ context.Context, _ string, _ *dap.TerminateArguments,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminateCalls++
	if f.terminateErr != nil {
		return f.terminateErr
	}
	return nil
}

func (f *fakeDebugger) Restart(_ context.Context, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restartCalls++
	return nil
}

func (f *fakeDebugger) SetBreakpoints(
	_ context.Context, _ string, a *dap.SetBreakpointsArguments,
) ([]dap.Breakpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setBpCalls = append(f.setBpCalls, a)
	return nil, nil
}

func (f *fakeDebugger) SetFunctionBreakpoints(
	_ context.Context, _ string, _ *dap.SetFunctionBreakpointsArguments,
) ([]dap.Breakpoint, error) {
	return nil, nil
}

func (f *fakeDebugger) SetExceptionBreakpoints(
	_ context.Context, _ string, _ *dap.SetExceptionBreakpointsArguments,
) ([]dap.Breakpoint, error) {
	return nil, nil
}

func (f *fakeDebugger) Continue(
	_ context.Context, _ string, a *dap.ContinueArguments,
) (*dap.ContinueResponseBody, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.continueCalls = append(f.continueCalls, a)
	return &dap.ContinueResponseBody{AllThreadsContinued: true}, nil
}

func (f *fakeDebugger) Next(_ context.Context, _ string, a *dap.NextArguments) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextCalls = append(f.nextCalls, a)
	return nil
}

func (f *fakeDebugger) StepIn(_ context.Context, _ string, _ *dap.StepInArguments) error {
	return nil
}
func (f *fakeDebugger) StepOut(_ context.Context, _ string, _ *dap.StepOutArguments) error {
	return nil
}
func (f *fakeDebugger) StepBack(_ context.Context, _ string, _ *dap.StepBackArguments) error {
	return nil
}
func (f *fakeDebugger) ReverseContinue(
	_ context.Context, _ string, _ *dap.ReverseContinueArguments,
) error {
	return nil
}
func (f *fakeDebugger) Pause(_ context.Context, _ string, _ *dap.PauseArguments) error {
	return nil
}

func (f *fakeDebugger) Threads(_ context.Context, _ string) ([]dap.Thread, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threadsCalls++
	return f.threadsResponse, nil
}

func (f *fakeDebugger) StackTrace(
	_ context.Context, _ string, a *dap.StackTraceArguments,
) (*dap.StackTraceResponseBody, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stackTraceArgs = append(f.stackTraceArgs, a)
	return f.stackResponse, nil
}

func (f *fakeDebugger) Scopes(
	_ context.Context, _ string, _ *dap.ScopesArguments,
) ([]dap.Scope, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scopesResponse, nil
}

func (f *fakeDebugger) Variables(
	_ context.Context, _ string, _ *dap.VariablesArguments,
) ([]dap.Variable, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.variablesResp, nil
}

func (f *fakeDebugger) SetVariable(
	_ context.Context, _ string, _ *dap.SetVariableArguments,
) (*dap.SetVariableResponseBody, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.setVarResp, nil
}

func (f *fakeDebugger) Source(
	_ context.Context, _ string, _ *dap.SourceArguments,
) (*dap.SourceResponseBody, error) {
	return &dap.SourceResponseBody{}, nil
}

func (f *fakeDebugger) Evaluate(
	_ context.Context, _ string, a *dap.EvaluateArguments,
) (*dap.EvaluateResponseBody, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evaluateCalls = append(f.evaluateCalls, a)
	return &dap.EvaluateResponseBody{Result: "evaluated:" + a.Expression}, nil
}

func (f *fakeDebugger) SetExpression(
	_ context.Context, _ string, _ *dap.SetExpressionArguments,
) (*dap.SetExpressionResponseBody, error) {
	return &dap.SetExpressionResponseBody{Value: "ok"}, nil
}

func (f *fakeDebugger) Completions(
	_ context.Context, _ string, _ *dap.CompletionsArguments,
) ([]dap.CompletionItem, error) {
	return nil, nil
}

func (f *fakeDebugger) ExceptionInfo(
	_ context.Context, _ string, _ *dap.ExceptionInfoArguments,
) (*dap.ExceptionInfoResponseBody, error) {
	return &dap.ExceptionInfoResponseBody{}, nil
}

func (f *fakeDebugger) Modules(
	_ context.Context, _ string, _ *dap.ModulesArguments,
) (*dap.ModulesResponseBody, error) {
	return &dap.ModulesResponseBody{}, nil
}

func (f *fakeDebugger) LoadedSources(_ context.Context, _ string) ([]dap.Source, error) {
	return nil, nil
}

func (f *fakeDebugger) ReadMemory(
	_ context.Context, _ string, _ *dap.ReadMemoryArguments,
) (*dap.ReadMemoryResponseBody, error) {
	return &dap.ReadMemoryResponseBody{}, nil
}

func (f *fakeDebugger) WriteMemory(
	_ context.Context, _ string, _ *dap.WriteMemoryArguments,
) (*dap.WriteMemoryResponseBody, error) {
	return &dap.WriteMemoryResponseBody{}, nil
}

func (f *fakeDebugger) Disassemble(
	_ context.Context, _ string, _ *dap.DisassembleArguments,
) ([]dap.DisassembledInstruction, error) {
	return nil, nil
}

func (f *fakeDebugger) GotoTargets(
	_ context.Context, _ string, _ *dap.GotoTargetsArguments,
) ([]dap.GotoTarget, error) {
	return nil, nil
}

func (f *fakeDebugger) Goto(_ context.Context, _ string, _ *dap.GotoArguments) error {
	return nil
}

var _ debugapi.Debugger = (*fakeDebugger)(nil)

// fakeTextapiEditor is a minimal textapi.Editor stub recording
// the calls made by the debug-shell stopped-line handler.
type fakeTextapiEditor struct {
	mu               sync.Mutex
	editorCalls      []workspaceapi.URI
	setCursorCalls   []term.Coordinates
	setLocationID    []string
	setLocationPrio  []textapi.LocationPriority
	setLocationCalls []textapi.Location
	// setLocationLists records the full list of locations
	// installed under each ID (last write wins).
	setLocationLists map[string][]textapi.Location
	setLocationByID  map[string]textapi.LocationPriority
	cursor           term.Coordinates
	cellView         textapi.CellView
	editorErr        error
	setCursorErr     error
	setLocationErr   error
	handler          textapi.Handler
}

func newFakeTextapiEditor() *fakeTextapiEditor {
	return &fakeTextapiEditor{
		handler:          &texttest.TestEditorHandler{},
		setLocationLists: make(map[string][]textapi.Location),
		setLocationByID:  make(map[string]textapi.LocationPriority),
	}
}

func (e *fakeTextapiEditor) SubscribeEvents(
	_ []textapi.EventType, _ textapi.EventHandler,
) error {
	return nil
}

func (e *fakeTextapiEditor) Editor(uri workspaceapi.URI) (textapi.Handler, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.editorCalls = append(e.editorCalls, uri)
	return e.handler, e.editorErr
}

func (e *fakeTextapiEditor) SetLocationList(
	_ textapi.Handler, prio textapi.LocationPriority,
	id string, list textapi.LocationList,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.setLocationID = append(e.setLocationID, id)
	e.setLocationPrio = append(e.setLocationPrio, prio)
	all := drainLocations(list)
	e.setLocationLists[id] = all
	e.setLocationByID[id] = prio
	if loc, ok := list.Current(); ok {
		e.setLocationCalls = append(e.setLocationCalls, loc)
	}
	return e.setLocationErr
}

func (e *fakeTextapiEditor) MoveToNextLocation(_ textapi.Handler, _ string) error {
	return nil
}

func (e *fakeTextapiEditor) MoveToPrevLocation(_ textapi.Handler, _ string) error {
	return nil
}

func (e *fakeTextapiEditor) Cursor(_ textapi.Handler) (term.Coordinates, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cursor, nil
}

func (e *fakeTextapiEditor) SetCursor(
	_ textapi.Handler, pos term.Coordinates,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.setCursorCalls = append(e.setCursorCalls, pos)
	e.cursor = pos
	return e.setCursorErr
}

func (e *fakeTextapiEditor) CellView(_ textapi.Handler) textapi.CellView {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cellView
}

func (e *fakeTextapiEditor) CellEditor(_ textapi.Handler) textapi.CellEditor {
	return nil
}

func (e *fakeTextapiEditor) SetDefaultAttributes(
	_ textapi.Handler, _ term.Attributes,
) error {
	return nil
}

var _ textapi.Editor = (*fakeTextapiEditor)(nil)

// drainLocations walks list from its current position forward
// (and from the previous one backward) to collect every entry
// without depending on its underlying iteration order.
func drainLocations(list textapi.LocationList) []textapi.Location {
	var out []textapi.Location
	if list == nil {
		return nil
	}
	cur, ok := list.Current()
	if !ok {
		return nil
	}
	out = append(out, cur)
	for {
		v, ok := list.Next()
		if !ok {
			break
		}
		out = append(out, v)
	}
	return out
}

// fakeWindow is a browser.Window stub used to model the various
// window states (floating, minimized, hosting the shell, etc.)
// that the stopped-breakpoint navigator must distinguish.
type fakeWindow struct {
	id        uint64
	floating  bool
	minimized bool
	content   browserapi.Handler
	setCalls  []browserapi.Handler
}

func (w *fakeWindow) WindowID() uint64 { return w.id }
func (w *fakeWindow) IsFloating() bool { return w.floating }
func (w *fakeWindow) IsMinimized() (component.Alignment, bool) {
	return component.Alignment(0), w.minimized
}
func (w *fakeWindow) Content() (browserapi.Handler, error) { return w.content, nil }
func (w *fakeWindow) SetContent(h browserapi.Handler) error {
	w.setCalls = append(w.setCalls, h)
	w.content = h
	return nil
}
func (w *fakeWindow) Focus() (bool, error)   { return false, nil }
func (w *fakeWindow) Close() error           { return nil }
func (w *fakeWindow) Closed() bool           { return false }
func (w *fakeWindow) MinimizeUp(int) bool    { return false }
func (w *fakeWindow) MinimizeDown(int) bool  { return false }
func (w *fakeWindow) MinimizeLeft(int) bool  { return false }
func (w *fakeWindow) MinimizeRight(int) bool { return false }
func (w *fakeWindow) Unminimize() bool       { return false }
func (w *fakeWindow) SetFrameAttr(a term.Attributes) (term.Attributes, bool) {
	return a, false
}
func (w *fakeWindow) Position() term.Coordinates { return term.Coordinates{} }
func (w *fakeWindow) Width() int                 { return 0 }
func (w *fakeWindow) Height() int                { return 0 }

var _ browser.Window = (*fakeWindow)(nil)

// fakeBrowser is a minimal browser.Browser stub that lets tests
// drive the stopped-breakpoint navigator: it records Open/Split/
// SetFocus calls and exposes a controllable list of windows for
// IterateWindows.
type fakeBrowser struct {
	mu sync.Mutex

	openCalls   []workspaceapi.URI
	openHandler browserapi.Handler
	openErr     error

	splitCalls  []splitCall
	splitWindow *fakeWindow
	splitErr    error

	setFocusCalls []browser.Window
	windows       []*fakeWindow

	notifyCalls []notifyCall
}

type splitCall struct {
	orientation browserapi.Orientation
	target      browser.Window
	handler     browserapi.Handler
}

type notifyCall struct {
	level browserapi.NotificationLevel
	msg   string
	args  []any
}

func newFakeBrowser() *fakeBrowser {
	return &fakeBrowser{
		openHandler: &texttest.TestEditorHandler{},
	}
}

func (b *fakeBrowser) Open(uri workspaceapi.URI) (browserapi.Handler, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.openCalls = append(b.openCalls, uri)
	return b.openHandler, b.openErr
}

func (b *fakeBrowser) Resource(workspaceapi.URI) (browserapi.Handler, bool) {
	return nil, false
}

func (b *fakeBrowser) IterateWindows(fn func(browser.Window)) {
	b.mu.Lock()
	wins := append([]*fakeWindow(nil), b.windows...)
	b.mu.Unlock()
	for _, w := range wins {
		fn(w)
	}
}

func (b *fakeBrowser) Split(
	o browserapi.Orientation, w browser.Window, h browserapi.Handler,
) (browser.Window, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.splitCalls = append(b.splitCalls, splitCall{o, w, h})
	if b.splitErr != nil {
		return nil, b.splitErr
	}
	if b.splitWindow == nil {
		b.splitWindow = &fakeWindow{id: 999, content: h}
	} else {
		b.splitWindow.content = h
	}
	return b.splitWindow, nil
}

func (b *fakeBrowser) SetFocus(w browser.Window) (browser.Window, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.setFocusCalls = append(b.setFocusCalls, w)
	return w, nil
}

func (b *fakeBrowser) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.notifyCalls = append(b.notifyCalls, notifyCall{level, msg, args})
	return "", nil
}

func (b *fakeBrowser) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return b.Notify(level, msg, args...)
}

func (b *fakeBrowser) UpdateNotificationProgress(
	_ string, _ string, _, _ int64,
) error {
	return nil
}

func (b *fakeBrowser) Focus() (browser.Window, error)              { return nil, nil }
func (b *fakeBrowser) Bar(browserapi.BarConfig, tui.Handler) error { return nil }
func (b *fakeBrowser) Floating(
	browser.Floating, browserapi.FloatingConfig,
) (browser.Window, error) {
	return nil, nil
}
func (b *fakeBrowser) Window(uint64) (browser.Window, bool) { return nil, false }
func (b *fakeBrowser) Tab(
	workspaceapi.URI, rune, string, browserapi.Handler,
) (browserapi.Handler, error) {
	return nil, nil
}
func (b *fakeBrowser) SetTabName(
	workspaceapi.URI, string, term.Attributes,
) error {
	return nil
}

func (b *fakeBrowser) OnTabExit(workspaceapi.URI) bool {
	return false
}
func (b *fakeBrowser) PublishEvent(term.Event) error { return nil }
func (b *fakeBrowser) Close() error                  { return nil }

func (b *fakeBrowser) DragHover(term.Coordinates) bool          { return false }
func (b *fakeBrowser) DragCancel()                              {}
func (b *fakeBrowser) DragDrop(term.Coordinates, []string) bool { return false }

var _ browser.Browser = (*fakeBrowser)(nil)

func mustParseURI(t *testing.T, s string) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI(s)
	require.NoError(t, err)
	return uri
}

func newTestHandler(t *testing.T) (*Handler, *fakeDebugger, *texttest.TestEditor) {
	t.Helper()
	ed := texttest.NopEditor()
	dbg := newFakeDebugger()
	h := New(dbg, nil, nil, passThroughParser{}, passThroughFS{},
		Config{ScheduleNextTick: syncScheduleNextTick})
	return h, dbg, ed
}

// syncScheduleNextTick runs fn synchronously on the calling
// goroutine. Used by tests to short-circuit the GUI event-loop
// hop performed by Handler.runOnUI in production.
func syncScheduleNextTick(fn func()) bool {
	fn()
	return true
}

// initSession runs `debugger initialize <langID>` against h.
// It discards the returned event iterator (tests that need to
// observe events should call cmdInitialize directly).
func initSession(t *testing.T, h *Handler, dbg *fakeDebugger, langID string) {
	t.Helper()
	it, err := h.HandleCommand(context.Background(), repl.Command{
		Name: CommandName, Args: []string{subInitialize, langID},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, it)
	require.NotEmpty(t, dbg.createCalls)
}

func collectStrings[T any](ctx context.Context, it iterator.Iterator[T]) []T {
	defer it.Close()
	var out []T
	for {
		v, ok := it.Next(ctx)
		if !ok {
			return out
		}
		out = append(out, v)
	}
}

func TestHandler_Help(t *testing.T) {
	h, _, _ := newTestHandler(t)
	it, err := h.Help(context.Background(), nil)
	require.NoError(t, err)
	require.NotNil(t, it)
	defer it.Close()
	_, ok := it.Next(context.Background())
	require.True(t, ok)
}

func TestHandler_Initialize(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()

	it, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subInitialize, "go"},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, it)
	defer it.Close()
	assert.Equal(t, []string{"go"}, dbg.createCalls)
	require.Len(t, dbg.createClient, 1)
	assert.Equal(t, "rune", dbg.createClient[0].ClientID)
	assert.Equal(t, "Rune IDE", dbg.createClient[0].ClientName)
	assert.Equal(t, "path", dbg.createClient[0].PathFormat)
	assert.True(t, dbg.createClient[0].LinesStartAt1)
	assert.True(t, dbg.createClient[0].ColumnsStartAt1)

	// Re-initializing while active is rejected.
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subInitialize, "go"},
	}, nil)
	require.ErrorIs(t, err, errSessionActive)
}

func TestHandler_Initialize_BubblesError(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	dbg.createErr = errors.New("boom")
	_, err := h.HandleCommand(context.Background(), repl.Command{
		Name: CommandName, Args: []string{subInitialize, "go"},
	}, nil)
	require.Error(t, err)
	// The channel should be cleared so a subsequent initialize
	// can succeed.
	dbg.createErr = nil
	_, err = h.HandleCommand(context.Background(), repl.Command{
		Name: CommandName, Args: []string{subInitialize, "go"},
	}, nil)
	require.NoError(t, err)
}

func TestHandler_RequiresSession(t *testing.T) {
	h, _, _ := newTestHandler(t)
	ctx := context.Background()

	for _, sub := range []string{
		subLaunch, subAttach, subConfigured, subTerminate, subRestart,
		subContinue, subNext, subThreads,
	} {
		t.Run(sub, func(t *testing.T) {
			args := []string{sub}
			if sub == subLaunch || sub == subAttach {
				args = append(args, "x")
			}
			_, err := h.HandleCommand(ctx, repl.Command{
				Name: CommandName, Args: args,
			}, nil)
			require.ErrorIs(t, err, errNoSession)
		})
	}
}

func TestHandler_Launch(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName,
		Args: []string{subLaunch, "/bin/prog", "--flag", "arg"},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 0, dbg.configDoneCalls)
	require.Len(t, dbg.launchCalls, 1)
	assert.Equal(t, "/bin/prog", dbg.launchCalls[0].Program)
	assert.Equal(t, []string{"--flag", "arg"}, dbg.launchCalls[0].Args)
}

// rootJoinFS resolves relative paths against a workspace root the
// way a real workspace FileSystem does, so launch tests can observe
// program-path resolution.
type rootJoinFS struct {
	passThroughFS
	root string
}

func (f rootJoinFS) URI(path string) (workspaceapi.URI, error) {
	if !strings.HasPrefix(path, "/") {
		path = f.root + "/" + path
	}
	return workspaceapi.ParseURI("file://" + path)
}

// TestHandler_LaunchResolvesRelativeProgram asserts that a relative
// program path is resolved to an absolute path against the workspace
// root before being sent to the adapter. debugpy derives the
// debuggee cwd from the program's directory and then re-resolves a
// still-relative program against it, producing a doubled path such
// as `<root>/src/src/init.py`. Sending an absolute program prevents
// that second resolution.
func TestHandler_LaunchResolvesRelativeProgram(t *testing.T) {
	root := t.TempDir()
	uri, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)

	dbg := newFakeDebugger()
	h := New(dbg, nil, nil, passThroughParser{}, rootJoinFS{root: root},
		Config{WorkspaceURI: uri, ScheduleNextTick: syncScheduleNextTick})
	ctx := context.Background()
	initSession(t, h, dbg, "python")

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subLaunch, "src/init.py"},
	}, nil)
	require.NoError(t, err)
	require.Len(t, dbg.launchCalls, 1)
	assert.Equal(t, root+"/src/init.py", dbg.launchCalls[0].Program)
}

func TestHandler_LaunchWithEnvFlags(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		program  string
		progArgs []string
		env      map[string]string
	}{
		{
			name:     "env before program no separator",
			args:     []string{subLaunch, "-e", "FOO=bar", "/bin/prog", "arg1"},
			program:  "/bin/prog",
			progArgs: []string{"arg1"},
			env:      map[string]string{"FOO": "bar"},
		},
		{
			name:     "env before program with separator",
			args:     []string{subLaunch, "-e", "FOO=bar", "--", "/bin/prog", "arg1"},
			program:  "/bin/prog",
			progArgs: []string{"arg1"},
			env:      map[string]string{"FOO": "bar"},
		},
		{
			name:    "multiple env vars",
			args:    []string{subLaunch, "-e", "FOO=bar", "-e", "BAZ=qux", "--", "/bin/prog"},
			program: "/bin/prog",
			env:     map[string]string{"FOO": "bar", "BAZ": "qux"},
		},
		{
			name:    "env value contains equals",
			args:    []string{subLaunch, "-e", "KEY=a=b=c", "--", "/bin/prog"},
			program: "/bin/prog",
			env:     map[string]string{"KEY": "a=b=c"},
		},
		{
			name:     "separator preserves program args starting with dash",
			args:     []string{subLaunch, "--", "/bin/prog", "-e", "not-a-flag"},
			program:  "/bin/prog",
			progArgs: []string{"-e", "not-a-flag"},
		},
		{
			name:     "no flags still works",
			args:     []string{subLaunch, "/bin/prog", "--flag", "arg"},
			program:  "/bin/prog",
			progArgs: []string{"--flag", "arg"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, dbg, _ := newTestHandler(t)
			ctx := context.Background()
			initSession(t, h, dbg, "go")

			_, err := h.HandleCommand(ctx, repl.Command{
				Name: CommandName,
				Args: tc.args,
			}, nil)
			require.NoError(t, err)
			require.Len(t, dbg.launchCalls, 1)
			assert.Equal(t, tc.program, dbg.launchCalls[0].Program)
			if tc.progArgs == nil {
				assert.Empty(t, dbg.launchCalls[0].Args)
			} else {
				assert.Equal(t, tc.progArgs, dbg.launchCalls[0].Args)
			}
			assert.Equal(t, tc.env, dbg.launchCalls[0].Env)
		})
	}
}

func TestHandler_LaunchFlagErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			name: "missing -e value",
			args: []string{subLaunch, "-e"},
		},
		{
			name: "malformed -e value no equals",
			args: []string{subLaunch, "-e", "FOO", "/bin/prog"},
		},
		{
			name: "malformed -e value empty key",
			args: []string{subLaunch, "-e", "=bar", "/bin/prog"},
		},
		{
			name: "unknown flag",
			args: []string{subLaunch, "-x", "FOO=bar", "/bin/prog"},
		},
		{
			name: "only flags no program",
			args: []string{subLaunch, "-e", "FOO=bar"},
		},
		{
			name: "separator with no program",
			args: []string{subLaunch, "-e", "FOO=bar", "--"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, dbg, _ := newTestHandler(t)
			ctx := context.Background()
			initSession(t, h, dbg, "go")

			_, err := h.HandleCommand(ctx, repl.Command{
				Name: CommandName,
				Args: tc.args,
			}, nil)
			require.Error(t, err)
			assert.Empty(t, dbg.launchCalls)
		})
	}
}

func TestHandler_LaunchThenConfigured(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName,
		Args: []string{subLaunch, "/bin/prog"},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 0, dbg.configDoneCalls)

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName,
		Args: []string{subConfigured},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, dbg.configDoneCalls)
}

// TestHandler_LaunchBlocksUntilInitializedEvent verifies that
// the iterator returned by `debugger launch` stays open after
// emitting its static lines so the prompt remains visibly
// busy while the adapter is bringing the debuggee up. The
// iterator must complete only once a *dap.InitializedEvent
// has been delivered (the same event that prints "Debuggee
// initialized." through the session iterator).
func TestHandler_LaunchBlocksUntilInitializedEvent(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	it, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName,
		Args: []string{subLaunch, "/bin/prog"},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, it)
	t.Cleanup(func() { _ = it.Close() })

	// Drain in a goroutine so the test can observe when the
	// iterator transitions from blocked to finished.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, ok := it.Next(ctx); !ok {
				return
			}
		}
	}()

	// Iterator must still be running after the static
	// lines have been drained.
	select {
	case <-done:
		t.Fatal("iterator completed before InitializedEvent")
	case <-time.After(50 * time.Millisecond):
	}

	require.NotNil(t, dbg.subscriber)
	dbg.subscriber.OnEvent(&dap.InitializedEvent{
		Event: dap.Event{Event: "initialized"},
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("iterator did not complete after InitializedEvent")
	}
}

// TestHandler_LaunchIteratorReturnsOnContextCancel verifies
// that cancelling the caller's context releases the launch
// iterator promptly when the adapter is slow, so Ctrl-C
// returns control to the prompt.
func TestHandler_LaunchIteratorReturnsOnContextCancel(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	initSession(t, h, dbg, "go")

	it, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName,
		Args: []string{subLaunch, "/bin/prog"},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, it)
	t.Cleanup(func() { _ = it.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, ok := it.Next(ctx); !ok {
				return
			}
		}
	}()

	// Give the iterator a moment to reach the blocking
	// section, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("iterator did not return after context cancel")
	}
}

// TestHandler_AttachBlocksUntilInitializedEvent mirrors the
// launch test for the attach path so both subcommands keep
// the prompt busy until the adapter signals initialization.
func TestHandler_AttachBlocksUntilInitializedEvent(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	it, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName,
		Args: []string{subAttach, "1234"},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, it)
	t.Cleanup(func() { _ = it.Close() })

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, ok := it.Next(ctx); !ok {
				return
			}
		}
	}()

	// Iterator must still be running after the static
	// lines have been drained.
	select {
	case <-done:
		t.Fatal("iterator completed before InitializedEvent")
	case <-time.After(50 * time.Millisecond):
	}

	require.NotNil(t, dbg.subscriber)
	dbg.subscriber.OnEvent(&dap.InitializedEvent{
		Event: dap.Event{Event: "initialized"},
	})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("attach iterator did not complete after InitializedEvent")
	}
}

func TestHandler_Attach(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subAttach, "1234"},
	}, nil)
	require.NoError(t, err)
	require.Len(t, dbg.attachCalls, 1)
	assert.Equal(t, 1234, dbg.attachCalls[0].PID)
	require.Equal(t, 0, dbg.configDoneCalls)

	// attach by program name when not a valid int
	h, dbg, _ = newTestHandler(t)
	initSession(t, h, dbg, "go")
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subAttach, "myprog"},
	}, nil)
	require.NoError(t, err)
	require.Len(t, dbg.attachCalls, 1)
	assert.Equal(t, "myprog", dbg.attachCalls[0].Program)
}

// debuggerOnly hides fakeDebugger's CreateSessionConnect so the
// endpoint form can be exercised against a debugger that lacks the
// capability.
type debuggerOnly struct{ debugapi.Debugger }

// TestHandler_AttachConnectEndpoint covers the one-shot
// `debugger attach <langID> connect://host:port [program]` form,
// which creates the session and sends attach in a single gesture so
// the endpoint never has to be written into the workspace config.
func TestHandler_AttachConnectEndpoint(t *testing.T) {
	ctx := context.Background()

	t.Run("creates session and attaches", func(t *testing.T) {
		h, dbg, _ := newTestHandler(t)
		it, err := h.HandleCommand(ctx, repl.Command{
			Name: CommandName,
			Args: []string{
				subAttach, "python", "connect://127.0.0.1:5678", "main.py",
			},
		}, nil)
		require.NoError(t, err)
		require.NotNil(t, it)
		t.Cleanup(func() { _ = it.Close() })

		assert.Empty(t, dbg.createCalls,
			"endpoint form must not go through CreateSession")
		assert.Equal(t, [][2]string{{"python", "127.0.0.1:5678"}},
			dbg.connectCalls)
		require.Len(t, dbg.attachCalls, 1)
		assert.Equal(t, "main.py", dbg.attachCalls[0].Program)

		h.mu.Lock()
		phase, sid := h.phase, h.sessionID
		h.mu.Unlock()
		assert.Equal(t, phaseStarted, phase)
		assert.Equal(t, "s-test", sid)

		// The returned iterator is the session event stream: it
		// announces the session and stays open until it closes.
		v, ok := it.Next(ctx)
		require.True(t, ok)
		require.NotNil(t, v)
		require.NotNil(t, dbg.subscriber)
		dbg.subscriber.OnClose("terminated")
		v, ok = it.Next(ctx)
		require.True(t, ok)
		require.NotNil(t, v)
		_, ok = it.Next(ctx)
		require.False(t, ok)
	})

	t.Run("program is optional", func(t *testing.T) {
		h, dbg, _ := newTestHandler(t)
		it, err := h.HandleCommand(ctx, repl.Command{
			Name: CommandName,
			Args: []string{subAttach, "python", "connect://127.0.0.1:5678"},
		}, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _ = it.Close() })
		require.Len(t, dbg.attachCalls, 1)
		assert.Empty(t, dbg.attachCalls[0].Program)
		assert.Zero(t, dbg.attachCalls[0].PID)
	})

	t.Run("rejected while a session is active", func(t *testing.T) {
		h, dbg, _ := newTestHandler(t)
		initSession(t, h, dbg, "python")
		_, err := h.HandleCommand(ctx, repl.Command{
			Name: CommandName,
			Args: []string{subAttach, "python", "connect://127.0.0.1:5678"},
		}, nil)
		require.ErrorIs(t, err, errSessionActive)
		assert.Empty(t, dbg.connectCalls)
	})

	t.Run("debugger without the capability errors", func(t *testing.T) {
		dbg := newFakeDebugger()
		h := New(debuggerOnly{dbg}, nil, nil, passThroughParser{}, passThroughFS{},
			Config{ScheduleNextTick: syncScheduleNextTick})
		_, err := h.HandleCommand(ctx, repl.Command{
			Name: CommandName,
			Args: []string{subAttach, "python", "connect://127.0.0.1:5678"},
		}, nil)
		require.ErrorContains(t, err, "not supported")
		assert.Empty(t, dbg.attachCalls)
	})

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing langID", []string{"connect://127.0.0.1:5678"}},
		{"missing host:port", []string{"python", "connect://5678"}},
		{"endpoint not second", []string{"connect://127.0.0.1:5678", "python"}},
		{"too many arguments", []string{
			"python", "connect://127.0.0.1:5678", "main.py", "extra",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, dbg, _ := newTestHandler(t)
			_, err := h.HandleCommand(ctx, repl.Command{
				Name: CommandName,
				Args: append([]string{subAttach}, tc.args...),
			}, nil)
			require.ErrorContains(t, err,
				"debugger attach <langID> connect://host:port")
			assert.Empty(t, dbg.connectCalls)
		})
	}
}

func TestHandler_LifecycleSequenceErrors(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subConfigured},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "launch")
	assert.Contains(t, err.Error(), "attach")

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subContinue},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "launch")
	assert.Contains(t, err.Error(), "attach")

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subLaunch, "/bin/prog"},
	}, nil)
	require.NoError(t, err)

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subLaunch, "/bin/again"},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configured")

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subContinue},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configured")

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subConfigured},
	}, nil)
	require.NoError(t, err)

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subConfigured},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already configured")
}

func TestHandler_ReplaysBreakpointsOnConfigured(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	uri := mustParseURI(t, "file:///tmp/hello.go")
	hndl := &texttest.TestEditorHandler{}
	cmd := textapi.Command{
		Name:     CommandName,
		Args:     []string{subSetBreakpoint},
		URI:      uri,
		Resource: hndl,
		Cursor: struct {
			Content term.Coordinates
			Window  term.Coordinates
		}{
			Content: term.Coordinates{Y: 9},
		},
	}
	ph := NewPromptHandler(h)
	require.NoError(t, ph.HandleCommand(ctx, cmd))
	// Breakpoints are stored in memory after initialize, but are
	// not sent until configured.
	require.Len(t, dbg.setBpCalls, 0)

	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subLaunch, "/bin/prog"},
	}, nil)
	require.NoError(t, err)
	require.Len(t, dbg.setBpCalls, 0)

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subConfigured},
	}, nil)
	require.NoError(t, err)
	// The in-memory breakpoints are replayed on configured.
	require.Len(t, dbg.setBpCalls, 1)
	require.Len(t, dbg.setBpCalls[0].Breakpoints, 1)
	assert.Equal(t, 10, dbg.setBpCalls[0].Breakpoints[0].Line)
	require.Equal(t, 1, dbg.configDoneCalls)
}

func TestHandler_StoppedBreakpointHighlightsLine(t *testing.T) {
	dbg := newFakeDebugger()
	br := newFakeBrowser()
	tile := &fakeWindow{id: 1, content: &texttest.TestEditorHandler{}}
	br.windows = []*fakeWindow{tile}
	ed := newFakeTextapiEditor()
	h := New(dbg, br, ed, passThroughParser{}, passThroughFS{}, Config{
		Icons:            Icons{Stopped: "0a8b"},
		ScheduleNextTick: syncScheduleNextTick,
	})

	ctx := context.Background()
	it, err := h.cmdInitialize(ctx, []string{"go"}, nil)
	require.NoError(t, err)
	defer it.Close()
	_, _ = it.Next(ctx) // consume announcement

	require.NotNil(t, dbg.subscriber)
	dbg.subscriber.OnEvent(&dap.StoppedEvent{
		Event: dap.Event{Event: "stopped"},
		Body: dap.StoppedEventBody{
			Reason:   "breakpoint",
			ThreadId: 1,
		},
	})

	// handleStoppedBreakpoint runs asynchronously off the
	// adapter read goroutine — wait for its last side effect.
	// The browser open happens first, so waiting on it alone lets
	// the editor assertions below run before the cursor move and
	// the stopped location list have landed.
	require.Eventually(t, func() bool {
		ed.mu.Lock()
		defer ed.mu.Unlock()
		return len(ed.setLocationLists[stoppedLocationID]) >= 1
	}, 2*time.Second, 10*time.Millisecond)
	br.mu.Lock()
	// Find the stopped-frame source-file open in the
	// recorded calls; with auto-open of the log file
	// removed it is the only entry, but assert by path
	// rather than position to keep the test robust.
	var stoppedOpen string
	for _, u := range br.openCalls {
		if u.Path() == "/tmp/file.go" {
			stoppedOpen = u.Path()
			break
		}
	}
	assert.Equal(t, "/tmp/file.go", stoppedOpen)
	require.Len(t, br.setFocusCalls, 1)
	assert.Equal(t, browser.Window(tile), br.setFocusCalls[0])
	br.mu.Unlock()

	// The non-shell tile window's SetContent path was used
	// for the stopped frame. The output file's window may
	// have been split off the shell — but in this test there
	// is no shell window configured, so no split happens.
	assert.Empty(t, br.splitCalls)
	require.Len(t, tile.setCalls, 1)

	ed.mu.Lock()
	defer ed.mu.Unlock()
	require.Len(t, ed.setCursorCalls, 1)
	assert.Equal(t, term.Coordinates{X: 0, Y: 9}, ed.setCursorCalls[0])
	stop := ed.setLocationLists[stoppedLocationID]
	require.Len(t, stop, 1)
	loc := stop[0]
	assert.Equal(t, term.Coordinates{X: 0, Y: 9}, loc.From)
	assert.Equal(t, 9, loc.To.Y)
	assert.Contains(t, loc.Message, "main.main")
	assert.Equal(t, term.ColorYellow, loc.Attr.Bg)
	assert.Equal(t, term.ColorBlack, loc.Attr.Fg)
	assert.Equal(t, textapi.LocationPriorityWarning,
		ed.setLocationByID[stoppedLocationID])
}

func TestHandler_Lifecycle(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subLaunch, "/bin/prog"},
	}, nil)
	require.NoError(t, err)

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subConfigured},
	}, nil)
	require.NoError(t, err)

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subContinue},
	}, nil)
	require.NoError(t, err)
	require.Len(t, dbg.continueCalls, 1)
	assert.Equal(t, 1, dbg.continueCalls[0].ThreadId)

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subNext, "2"},
	}, nil)
	require.NoError(t, err)
	require.Len(t, dbg.nextCalls, 1)
	assert.Equal(t, 2, dbg.nextCalls[0].ThreadId)

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subRestart},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, dbg.restartCalls)

	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subTerminate},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, dbg.terminateCalls)

	// Simulate adapter closing the session.
	require.NotNil(t, dbg.subscriber)
	dbg.subscriber.OnClose("terminated")

	// After OnClose, a new initialize should work.
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subInitialize, "go"},
	}, nil)
	require.NoError(t, err)
}

func TestHandler_Introspection(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")
	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subLaunch, "/bin/prog"},
	}, nil)
	require.NoError(t, err)
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subConfigured},
	}, nil)
	require.NoError(t, err)

	cases := []struct {
		name string
		sub  string
	}{
		{"threads", subThreads},
		{"stack-trace", subStackTrace},
		{"scopes", subScopes},
		{"variables", subVariables},
		{"modules", subModules},
		{"loaded-sources", subLoadedSources},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			it, err := h.HandleCommand(ctx, repl.Command{
				Name: CommandName, Args: []string{tc.sub},
			}, nil)
			require.NoError(t, err)
			require.NotNil(t, it)
		})
	}
}

func TestHandler_SetBreakpointREPLHint(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	it, err := h.HandleCommand(context.Background(), repl.Command{
		Name: CommandName, Args: []string{subSetBreakpoint},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, it)
	// No SetBreakpoints call should be made via REPL.
	assert.Len(t, dbg.setBpCalls, 0)
}

func TestHandler_Complete(t *testing.T) {
	h, _, _ := newTestHandler(t)
	ctx := context.Background()
	it, err := h.Complete(ctx, CommandName, nil)
	require.NoError(t, err)
	all := collectStrings(ctx, it)
	assert.Contains(t, all, subInitialize)
	assert.Contains(t, all, subConfigured)
	assert.Contains(t, all, subLaunch)
	assert.Contains(t, all, subContinue)
	assert.Contains(t, all, subSetBreakpoint)

	// Prefix matching.
	it, err = h.Complete(ctx, CommandName, []string{"ste"})
	require.NoError(t, err)
	matches := collectStrings(ctx, it)
	for _, m := range matches {
		assert.True(t, strings.HasPrefix(m, "ste"), "%q should start with ste", m)
	}
	assert.Contains(t, matches, subStepIn)
	assert.Contains(t, matches, subStepOut)
	assert.Contains(t, matches, subStepBack)

	// Unknown command returns empty.
	it, err = h.Complete(ctx, "not-debugger", nil)
	require.NoError(t, err)
	assert.Empty(t, collectStrings(ctx, it))
}

func TestHandler_HandleCommand_UnknownSub(t *testing.T) {
	h, _, _ := newTestHandler(t)
	_, err := h.HandleCommand(context.Background(), repl.Command{
		Name: CommandName, Args: []string{"nope"},
	}, nil)
	require.ErrorIs(t, err, ErrUnknownSubcommand)
}

// TestHandler_CompleteInitializeAdapters verifies that
// `debugger initialize <prefix>` is completed against the
// adapter language IDs configured on the debugshell.
func TestHandler_CompleteInitializeAdapters(t *testing.T) {
	h := New(newFakeDebugger(), nil, nil, passThroughParser{}, passThroughFS{}, Config{
		Debugger: idedebug.Config{
			Adapters: map[string]idedebug.AdapterConfig{
				"go":     {},
				"python": {},
				"rust":   {},
			},
		},
		ScheduleNextTick: syncScheduleNextTick,
	})
	ctx := context.Background()

	it, err := h.Complete(ctx, CommandName, []string{subInitialize, ""})
	require.NoError(t, err)
	all := collectStrings(ctx, it)
	assert.Equal(t, []string{"go", "python", "rust"}, all)

	it, err = h.Complete(ctx, CommandName, []string{subInitialize, "p"})
	require.NoError(t, err)
	assert.Equal(t, []string{"python"}, collectStrings(ctx, it))

	it, err = h.Complete(ctx, CommandName, []string{subInitialize, "z"})
	require.NoError(t, err)
	assert.Empty(t, collectStrings(ctx, it))
}

// TestHandler_CompleteInitializeFallback exercises the path
// where no adapters are configured: no adapter completion is
// provided.
func TestHandler_CompleteInitializeFallback(t *testing.T) {
	h, _, _ := newTestHandler(t)
	ctx := context.Background()
	it, err := h.Complete(ctx, CommandName, []string{subInitialize, ""})
	require.NoError(t, err)
	assert.Empty(t, collectStrings(ctx, it))
}

// TestHandler_Evaluate verifies that `debugger evaluate <expr>`
// forwards the expression to debugapi.Debugger.Evaluate using
// the top frame and renders the result.
func TestHandler_Evaluate(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")
	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subLaunch, "/bin/prog"},
	}, nil)
	require.NoError(t, err)
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subConfigured},
	}, nil)
	require.NoError(t, err)

	it, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subEvaluate, "x", "+", "1"},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, it)
	defer it.Close()

	require.Len(t, dbg.evaluateCalls, 1)
	assert.Equal(t, "x + 1", dbg.evaluateCalls[0].Expression)
	assert.Equal(t, "repl", dbg.evaluateCalls[0].Context)
	assert.Equal(t, 42, dbg.evaluateCalls[0].FrameId,
		"FrameId should match top frame from fakeDebugger")
}

// TestHandler_Evaluate_RequiresArgs ensures an empty expression
// is rejected with a usage error.
func TestHandler_Evaluate_RequiresArgs(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")
	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subEvaluate},
	}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}

// TestHandler_VariablesEvaluateUseStoppedFrame is the
// regression test for RUNE-173. With many runtime/GC goroutines
// in the DAP `Threads` response, picking `threads[0]` for
// `variables`/`evaluate` returns runtime locals instead of the
// user's locals. The handler must instead target the goroutine
// from the most recent StoppedEvent and the frame index
// currently selected by `debugger jump`.
func TestHandler_VariablesEvaluateUseStoppedFrame(t *testing.T) {
	dbg := newFakeDebugger()
	br := newFakeBrowser()
	tile := &fakeWindow{id: 1, content: &texttest.TestEditorHandler{}}
	br.windows = []*fakeWindow{tile}
	ed := newFakeTextapiEditor()
	h := New(dbg, br, ed, passThroughParser{}, passThroughFS{}, Config{ScheduleNextTick: syncScheduleNextTick})

	// Threads ranks a runtime goroutine ahead of the user's
	// goroutine — the old code would target Id=1 (runtime) and
	// surface runtime/internal locals.
	dbg.threadsResponse = []dap.Thread{
		{Id: 1, Name: "runtime"},
		{Id: 1130, Name: "main"},
	}
	// Two frames: deepest (index 0) is the user's frame, and
	// the caller is what `debugger jump backward` should pick.
	dbg.stackResponse = &dap.StackTraceResponseBody{
		StackFrames: []dap.StackFrame{
			{
				Id: 1000, Name: "user.frame", Line: 10,
				Source: &dap.Source{Path: "/tmp/user.go"},
			},
			{
				Id: 1001, Name: "user.caller", Line: 20,
				Source: &dap.Source{Path: "/tmp/user.go"},
			},
		},
	}

	ctx := context.Background()
	it, err := h.cmdInitialize(ctx, []string{"go"}, nil)
	require.NoError(t, err)
	defer it.Close()
	_, _ = it.Next(ctx) // consume announcement
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subLaunch, "/bin/prog"},
	}, nil)
	require.NoError(t, err)
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subConfigured},
	}, nil)
	require.NoError(t, err)

	// Drive a StoppedEvent on the user's goroutine.
	require.NotNil(t, dbg.subscriber)
	dbg.subscriber.OnEvent(&dap.StoppedEvent{
		Event: dap.Event{Event: "stopped"},
		Body:  dap.StoppedEventBody{Reason: "breakpoint", ThreadId: 1130},
	})
	// handleStoppedBreakpoint runs in its own goroutine — wait
	// for the stop state to be installed.
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.stoppedThreadID == 1130 && len(h.stoppedFrames) == 2
	}, time.Second, 10*time.Millisecond)

	// `debugger evaluate c` must target the deepest user
	// frame (id 1000), NOT the top frame of threads[0].
	evIt, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subEvaluate, "c"},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, evIt)
	evIt.Close()
	require.Len(t, dbg.evaluateCalls, 1)
	assert.Equal(t, 1000, dbg.evaluateCalls[0].FrameId,
		"evaluate must use the stopped frame, not threads[0]")

	// `debugger variables` (no args) must request scopes for
	// the same stopped frame, not the runtime goroutine.
	varsIt, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subVariables},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, varsIt)
	varsIt.Close()
	// No StackTrace request should have been issued by the
	// `variables`/`evaluate` path itself — handleStoppedBreakpoint
	// already populated h.stoppedFrames, so topFrameID must read
	// straight from that cache instead of calling StackTrace again.
	// The one StackTrace call we expect is the one issued by
	// handleStoppedBreakpoint when the StoppedEvent arrived.
	require.Len(t, dbg.stackTraceArgs, 1,
		"only handleStoppedBreakpoint should call StackTrace")
	assert.Equal(t, 1130, dbg.stackTraceArgs[0].ThreadId)

	// `debugger jump backward` selects the caller frame.
	// jump is a prompt subcommand, not a repl subcommand.
	ph := NewPromptHandler(h)
	require.NoError(t, ph.HandleCommand(ctx, textapi.Command{
		Name: CommandName,
		Args: []string{subJump, jumpBackward},
		URI:  mustParseURI(t, "file:///tmp/user.go"),
	}))

	// After jump, `debugger evaluate` must target the newly
	// selected frame's id (1001).
	evIt2, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subEvaluate, "c"},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, evIt2)
	evIt2.Close()
	require.Len(t, dbg.evaluateCalls, 2)
	assert.Equal(t, 1001, dbg.evaluateCalls[1].FrameId,
		"evaluate must follow `debugger jump` to the new frame")
}

// TestHandler_ClearLocationsOnTerminate verifies that every
// location list installed during the active session is reset
// (SetLocationList with an empty slice) when the session
// terminates via OnClose. Otherwise stale stopped/variables/
// breakpoint markers would persist in the editor across
// independent debug sessions.
func TestHandler_ClearLocationsOnTerminate(t *testing.T) {
	dbg := newFakeDebugger()
	br := newFakeBrowser()
	tile := &fakeWindow{id: 1, content: &texttest.TestEditorHandler{}}
	br.windows = []*fakeWindow{tile}
	ed := newFakeTextapiEditor()
	h := New(dbg, br, ed, passThroughParser{}, passThroughFS{}, Config{ScheduleNextTick: syncScheduleNextTick})

	ctx := context.Background()
	it, err := h.cmdInitialize(ctx, []string{"go"}, nil)
	require.NoError(t, err)
	defer it.Close()
	_, _ = it.Next(ctx)

	require.NotNil(t, dbg.subscriber)
	dbg.subscriber.OnEvent(&dap.StoppedEvent{
		Event: dap.Event{Event: "stopped"},
		Body:  dap.StoppedEventBody{Reason: "breakpoint", ThreadId: 1},
	})
	require.Eventually(t, func() bool {
		ed.mu.Lock()
		defer ed.mu.Unlock()
		return len(ed.setLocationLists[stoppedLocationID]) > 0
	}, time.Second, 10*time.Millisecond)

	// Trigger the close path (adapter closing the session).
	dbg.subscriber.OnClose("terminated")

	// After OnClose, every previously-installed list must
	// have been cleared via an empty-slice SetLocationList
	// call. The drain helper used by the fake records the
	// final state per id.
	require.Eventually(t, func() bool {
		ed.mu.Lock()
		defer ed.mu.Unlock()
		// the stopped list must have been reset to empty
		return len(ed.setLocationLists[stoppedLocationID]) == 0
	}, time.Second, 10*time.Millisecond,
		"stopped location was not cleared on terminate")
}

// TestHandler_LaunchPersistsOutputEventsToSink reproduces the
// reported bug where DAP OutputEvents fired by the debuggee
// after launch never reached the per-session log file. The
// sink must be installed before Launch returns and every
// subsequent OutputEvent must land on disk so the user can
// inspect the captured output afterwards.
func TestHandler_LaunchPersistsOutputEventsToSink(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName,
		Args: []string{subLaunch, "/bin/prog"},
	}, nil)
	require.NoError(t, err)

	require.NotNil(t, dbg.subscriber)
	dbg.subscriber.OnEvent(&dap.OutputEvent{
		Event: dap.Event{Event: "output"},
		Body:  dap.OutputEventBody{Category: "stdout", Output: "hello\n"},
	})
	dbg.subscriber.OnEvent(&dap.OutputEvent{
		Event: dap.Event{Event: "output"},
		Body:  dap.OutputEventBody{Category: "stderr", Output: "boom\n"},
	})

	h.mu.Lock()
	sink := h.output
	h.mu.Unlock()
	require.NotNil(t, sink, "sink must be installed after launch")
	require.NotEmpty(t, sink.Path())
	t.Cleanup(func() { _ = os.Remove(sink.Path()) })

	data, err := os.ReadFile(sink.Path())
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "hello",
		"stdout OutputEvent must land on disk, got %q", got)
	assert.Contains(t, got, "boom",
		"stderr OutputEvent must land on disk, got %q", got)
}

// TestHandler_OutputAfterReinitializePersistsToCurrentSink
// reproduces the reported bug where, after a previous session
// has ended (OnClose fired), a fresh `initialize → launch`
// cycle does not produce a new sink and OutputEvents either
// land in a stale file or are lost. Each launch must capture
// to a sink whose file path is reported in the launch
// response, and OutputEvents fired during that session must
// land on that file.
// TestHandler_OutputEventBeforeLaunchIsLost reproduces the
// case where the adapter sends OutputEvents (e.g. delve's
// "[console] Type 'dlv help' ...") before cmdLaunch has had
// a chance to install the sink. Today these events are
// silently dropped because h.output is nil when OnEvent
// fires. After the fix, every OutputEvent received during a
// session must end up on the sink — including those that
// arrive between CreateSession and Launch.
func TestHandler_OutputEventBeforeLaunchIsLost(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	// Adapter sends an OutputEvent BEFORE the user runs
	// `debugger launch ...`. There is no sink yet.
	require.NotNil(t, dbg.subscriber)
	dbg.subscriber.OnEvent(&dap.OutputEvent{
		Event: dap.Event{Event: "output"},
		Body:  dap.OutputEventBody{Category: "console", Output: "early\n"},
	})

	// Then launch installs the sink.
	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName,
		Args: []string{subLaunch, "/bin/prog"},
	}, nil)
	require.NoError(t, err)
	dbg.subscriber.OnEvent(&dap.OutputEvent{
		Event: dap.Event{Event: "output"},
		Body:  dap.OutputEventBody{Category: "stdout", Output: "after\n"},
	})

	h.mu.Lock()
	sink := h.output
	h.mu.Unlock()
	require.NotNil(t, sink)
	t.Cleanup(func() { _ = os.Remove(sink.Path()) })

	data, err := os.ReadFile(sink.Path())
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "early",
		"pre-launch OutputEvent must be retained, got %q", got)
	assert.Contains(t, got, "after",
		"post-launch OutputEvent must be retained, got %q", got)
}

func TestHandler_OutputAfterReinitializePersistsToCurrentSink(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()

	// Cycle 1: initialize, launch, OnClose (adapter terminated).
	initSession(t, h, dbg, "go")
	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName,
		Args: []string{subLaunch, "/bin/prog"},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, dbg.subscriber)
	dbg.subscriber.OnClose("terminated")

	// Cycle 2: re-initialize, launch, fire an OutputEvent.
	dbg.subscriber = nil
	initSession(t, h, dbg, "go")
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName,
		Args: []string{subLaunch, "/bin/prog2"},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, dbg.subscriber)

	// Capture the path the second launch reported as the
	// active sink. After step 2, h.output must be the second
	// session's sink, with a fresh path.
	h.mu.Lock()
	sink := h.output
	h.mu.Unlock()
	require.NotNil(t, sink)
	require.NotEmpty(t, sink.Path())
	t.Cleanup(func() { _ = os.Remove(sink.Path()) })

	dbg.subscriber.OnEvent(&dap.OutputEvent{
		Event: dap.Event{Event: "output"},
		Body:  dap.OutputEventBody{Category: "stdout", Output: "second\n"},
	})

	data, err := os.ReadFile(sink.Path())
	require.NoError(t, err)
	got := string(data)
	assert.Contains(t, got, "second",
		"OutputEvent in cycle 2 must land on the active sink, got %q", got)
}

func TestHandler_HandleCommand_NotFound(t *testing.T) {
	h, _, _ := newTestHandler(t)
	_, err := h.HandleCommand(context.Background(), repl.Command{
		Name: "other",
	}, nil)
	require.ErrorIs(t, err, repl.ErrNotFound)
}

// TestHandler_ClearBreakpointsOnTerminate verifies that the
// in-memory breakpoint and stop-frame state held by Handler
// is dropped when a session terminates. Otherwise the next
// `debugger initialize` + `configured` cycle silently
// re-submits breakpoints from the previous run, causing the
// debuggee to stop at locations the user thought they had
// cleared.
func TestHandler_ClearBreakpointsOnTerminate(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	initSession(t, h, dbg, "go")

	// Seed breakpoint and stop-frame state, the same way a
	// real session would after `:debugger set-breakpoint` and
	// a stopped event.
	h.mu.Lock()
	h.breakpoints["/tmp/main.go"] = []int{10, 20}
	h.stoppedFrames = []dap.StackFrame{{Id: 1, Line: 10}}
	h.stoppedFrame = 0
	h.mu.Unlock()

	// Adapter closes the session (terminate path).
	require.NotNil(t, dbg.subscriber)
	dbg.subscriber.OnClose("terminated")

	h.mu.Lock()
	bps := h.breakpoints
	frames := h.stoppedFrames
	frameIdx := h.stoppedFrame
	h.mu.Unlock()
	assert.Empty(t, bps,
		"breakpoints must be cleared after terminate")
	assert.Empty(t, frames,
		"stopped frames must be cleared after terminate")
	assert.Zero(t, frameIdx,
		"stopped-frame index must reset to 0 after terminate")
}

func TestHandler_SetBreakpointPrompt(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	initSession(t, h, dbg, "go")
	uri := mustParseURI(t, "file:///tmp/hello.go")
	hndl := &texttest.TestEditorHandler{}
	ctx := context.Background()

	cmd := textapi.Command{
		Name:     CommandName,
		Args:     []string{subSetBreakpoint},
		URI:      uri,
		Resource: hndl,
		Cursor: struct {
			Content term.Coordinates
			Window  term.Coordinates
		}{
			Content: term.Coordinates{Y: 9},
		},
	}

	ph := NewPromptHandler(h)
	require.NoError(t, ph.HandleCommand(ctx, cmd))
	// Breakpoints are only tracked in memory until launch/attach.
	require.Len(t, dbg.setBpCalls, 0)
	// The location list was installed.
	require.NotNil(t, hndl.LocationList)
	loc, ok := hndl.LocationList.Current()
	require.True(t, ok)
	assert.Equal(t, 9, loc.From.Y)
	assert.Equal(t, "breakpoint", loc.Message)

	// Toggling the same line removes it.
	require.NoError(t, ph.HandleCommand(ctx, cmd))
	require.Len(t, dbg.setBpCalls, 0)
}

func TestHandler_SetBreakpointPrompt_NoSession(t *testing.T) {
	h, _, _ := newTestHandler(t)
	ph := NewPromptHandler(h)
	hndl := &texttest.TestEditorHandler{}
	uri := mustParseURI(t, "file:///tmp/hello.go")
	err := ph.HandleCommand(context.Background(), textapi.Command{
		Name: CommandName, Args: []string{subSetBreakpoint},
		URI: uri, Resource: hndl,
	})
	require.ErrorIs(t, err, errNoSession)
}

func TestHandler_SetBreakpointPrompt_NoResource(t *testing.T) {
	h, _, _ := newTestHandler(t)
	ph := NewPromptHandler(h)
	err := ph.HandleCommand(context.Background(), textapi.Command{
		Name: CommandName, Args: []string{subSetBreakpoint},
	})
	require.Error(t, err)
}

func TestPromptHandler_CompleteOnlyPromptSubcommands(t *testing.T) {
	h, _, _ := newTestHandler(t)
	ph := NewPromptHandler(h)
	ctx := context.Background()

	it, _, err := ph.Complete(ctx, textapi.Command{
		Name: CommandName,
		Args: nil,
	})
	require.NoError(t, err)
	got := collectStrings(ctx, it)
	assert.Equal(t, []string{subSetBreakpoint, subJump}, got)

	it, _, err = ph.Complete(ctx, textapi.Command{
		Name: CommandName,
		Args: []string{"set-"},
	})
	require.NoError(t, err)
	got = collectStrings(ctx, it)
	assert.Equal(t, []string{subSetBreakpoint}, got)

	it, _, err = ph.Complete(ctx, textapi.Command{
		Name: CommandName,
		Args: []string{"jum"},
	})
	require.NoError(t, err)
	got = collectStrings(ctx, it)
	assert.Equal(t, []string{subJump}, got)

	it, _, err = ph.Complete(ctx, textapi.Command{
		Name: CommandName,
		Args: []string{"lau"},
	})
	require.NoError(t, err)
	got = collectStrings(ctx, it)
	assert.Empty(t, got)
}

func TestHandler_New_NilDebugger(t *testing.T) {
	require.Panics(t, func() { New(nil, nil, nil, passThroughParser{}, passThroughFS{}, Config{}) })
}

func TestHandler_New_NilScheduleNextTick(t *testing.T) {
	require.Panics(t, func() {
		New(newFakeDebugger(), nil, nil, passThroughParser{}, passThroughFS{}, Config{})
	})
}

// TestHandler_TerminateUnsupportedFallsBackToDisconnect verifies
// that when the DAP server does not implement the Terminate RPC,
// the shell falls back to Disconnect, clears local session state,
// and allows a subsequent `debugger initialize` to succeed.
func TestHandler_TerminateUnsupportedFallsBackToDisconnect(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	dbg.terminateErr = errors.New(
		"terminate: unsupported request 'terminate'")

	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subTerminate},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, dbg.terminateCalls)
	require.Len(t, dbg.disconnectCalls, 1)
	assert.True(t, dbg.disconnectCalls[0].TerminateDebuggee)

	// Local session state must be cleared so a new initialize
	// can proceed.
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subInitialize, "go"},
	}, nil)
	require.NoError(t, err)
}

// TestHandler_TerminateAndDisconnectFailStillResets ensures that
// even if both Terminate and Disconnect fail, the user can still
// recover by initializing a new session.
func TestHandler_TerminateAndDisconnectFailStillResets(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	dbg.terminateErr = errors.New("boom-terminate")
	dbg.disconnectErr = errors.New("boom-disconnect")

	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subTerminate},
	}, nil)
	require.Error(t, err)

	// Recovery must still be possible.
	dbg.terminateErr = nil
	dbg.disconnectErr = nil
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subInitialize, "go"},
	}, nil)
	require.NoError(t, err)
}

// TestHandler_TerminateSuccessClearsSession ensures that a
// successful DAP terminate clears local session state so a
// subsequent `debugger initialize` is not rejected with
// errSessionActive. The adapter may not fire OnClose (e.g. the
// debuggee already exited), so terminate itself must reset.
func TestHandler_TerminateSuccessClearsSession(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	initSession(t, h, dbg, "go")

	_, err := h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subTerminate},
	}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, dbg.terminateCalls)
	require.Empty(t, dbg.disconnectCalls)

	// The session must be cleared even though the adapter never
	// fired OnClose, so a new initialize can proceed.
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: CommandName, Args: []string{subInitialize, "go"},
	}, nil)
	require.NoError(t, err)
}

// TestSessionIterator_EventsAndClose exercises the event
// streaming iterator returned from cmdInitialize.
func TestSessionIterator_EventsAndClose(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	it, err := h.cmdInitialize(ctx, []string{"go"}, nil)
	require.NoError(t, err)
	defer it.Close()

	// First element is the announcement.
	v, ok := it.Next(ctx)
	require.True(t, ok)
	require.NotNil(t, v)

	// Push an OutputEvent and observe it.
	require.NotNil(t, dbg.subscriber)
	dbg.subscriber.OnEvent(&dap.OutputEvent{
		Event: dap.Event{Event: "output"},
		Body:  dap.OutputEventBody{Category: "stdout", Output: "hi\n"},
	})
	v, ok = it.Next(ctx)
	require.True(t, ok)
	require.NotNil(t, v)

	// Close the session; the iterator should surface the close
	// message and then return false.
	dbg.subscriber.OnClose("terminated")
	v, ok = it.Next(ctx)
	require.True(t, ok)
	require.NotNil(t, v)
	_, ok = it.Next(ctx)
	require.False(t, ok)
}

// TestSessionIterator_SuppressesConsoleOutputEvents asserts
// that OutputEvents in the `console` category (used by Delve
// for adapter chatter such as the "Type 'dlv help' ..." banner)
// are not surfaced as transcript entries. Stdout/stderr output
// must still come through unchanged.
func TestSessionIterator_SuppressesConsoleOutputEvents(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx := context.Background()
	it, err := h.cmdInitialize(ctx, []string{"go"}, nil)
	require.NoError(t, err)
	defer it.Close()
	_, _ = it.Next(ctx) // announcement

	require.NotNil(t, dbg.subscriber)
	// A console-category event followed by a stdout event.
	// The iterator should skip the console one and surface
	// the stdout one as the next item.
	dbg.subscriber.OnEvent(&dap.OutputEvent{
		Event: dap.Event{Event: "output"},
		Body: dap.OutputEventBody{
			Category: "console",
			Output:   "Type 'dlv help' for list of commands.\n",
		},
	})
	dbg.subscriber.OnEvent(&dap.OutputEvent{
		Event: dap.Event{Event: "output"},
		Body: dap.OutputEventBody{
			Category: "stdout",
			Output:   "hi\n",
		},
	})

	// Run Next in a goroutine with a short deadline so the
	// test fails loudly if the iterator returns the console
	// banner instead of skipping it.
	type result struct {
		v  component.Responsive
		ok bool
	}
	done := make(chan result, 1)
	go func() {
		v, ok := it.Next(ctx)
		done <- result{v, ok}
	}()
	select {
	case r := <-done:
		require.True(t, r.ok)
		require.NotNil(t, r.v)
	case <-time.After(time.Second):
		t.Fatal("Next blocked: console event was not skipped")
	}
}

// TestHandler_StoppedEventEmitsStackTrace asserts that hitting
// a breakpoint pushes a markdown stack-trace entry into the
// session iterator automatically — saving the user from having
// to type `debugger stack-trace` after every stop.
func TestHandler_StoppedEventEmitsStackTrace(t *testing.T) {
	dbg := newFakeDebugger()
	br := newFakeBrowser()
	tile := &fakeWindow{id: 1, content: &texttest.TestEditorHandler{}}
	br.windows = []*fakeWindow{tile}
	ed := newFakeTextapiEditor()
	h := New(dbg, br, ed, passThroughParser{}, passThroughFS{}, Config{ScheduleNextTick: syncScheduleNextTick})

	ctx := context.Background()
	it, err := h.cmdInitialize(ctx, []string{"go"}, nil)
	require.NoError(t, err)
	defer it.Close()
	_, _ = it.Next(ctx) // announcement

	require.NotNil(t, dbg.subscriber)
	dbg.subscriber.OnEvent(&dap.StoppedEvent{
		Event: dap.Event{Event: "stopped"},
		Body:  dap.StoppedEventBody{Reason: "breakpoint", ThreadId: 1},
	})

	// The iterator should yield exactly one rendered entry
	// (the auto stack-trace) before the StoppedEvent itself
	// is rendered. We pull entries until we see the auto
	// stack-trace, then stop.
	deadline := time.After(2 * time.Second)
	got := false
	for !got {
		type result struct {
			v  component.Responsive
			ok bool
		}
		ch := make(chan result, 1)
		go func() {
			v, ok := it.Next(ctx)
			ch <- result{v, ok}
		}()
		select {
		case r := <-ch:
			require.True(t, r.ok)
			require.NotNil(t, r.v)
			got = true
		case <-deadline:
			t.Fatal("auto stack-trace was not emitted after StoppedEvent")
		}
	}

	// Also assert that the formatted markdown matches the
	// shape of `debugger stack-trace`.
	body := formatStackTraceMarkdown(dbg.stackResponse.StackFrames)
	assert.Contains(t, body, "Stack trace:")
	assert.Contains(t, body, "main.main")
}

// TestSessionIterator_SurvivesContextCancel asserts that the
// long-lived session iterator returned from `debugger
// initialize` keeps streaming events after the caller's context
// is cancelled. The REPL cancels the per-command context on
// Ctrl-C; if the iterator honored that cancellation, no DAP
// event would ever be rendered after the user typed Ctrl-C
// once, even though the debug session is still alive. Only an
// explicit `debugger terminate` (or a session-side close) must
// end the iterator.
func TestSessionIterator_SurvivesContextCancel(t *testing.T) {
	h, dbg, _ := newTestHandler(t)
	ctx, cancel := context.WithCancel(context.Background())
	it, err := h.cmdInitialize(ctx, []string{"go"}, nil)
	require.NoError(t, err)
	defer it.Close()

	// Consume the announcement.
	v, ok := it.Next(ctx)
	require.True(t, ok)
	require.NotNil(t, v)

	// Simulate Ctrl-C in the REPL: cancel the context that
	// was passed to HandleCommand / cmdInitialize.
	cancel()

	// A subsequent DAP event should still be observed by the
	// iterator. We exercise this by asking the iterator for
	// the next item in a goroutine, then pushing an event.
	require.NotNil(t, dbg.subscriber)
	type result struct {
		v  component.Responsive
		ok bool
	}
	done := make(chan result, 1)
	go func() {
		v, ok := it.Next(ctx)
		done <- result{v, ok}
	}()

	// Give Next a moment to enter the channel-receive state,
	// then deliver an event.
	time.Sleep(50 * time.Millisecond)
	dbg.subscriber.OnEvent(&dap.OutputEvent{
		Event: dap.Event{Event: "output"},
		Body:  dap.OutputEventBody{Category: "stdout", Output: "hi\n"},
	})

	select {
	case r := <-done:
		require.True(t, r.ok,
			"iterator returned ok=false after ctx cancel; "+
				"events stopped flowing")
		require.NotNil(t, r.v)
	case <-time.After(2 * time.Second):
		t.Fatal("iterator never produced an item after ctx cancel")
	}

	// Closing the session via OnClose still terminates the
	// iterator cleanly.
	dbg.subscriber.OnClose("terminated")
	v, ok = it.Next(ctx)
	require.True(t, ok)
	require.NotNil(t, v)
	_, ok = it.Next(ctx)
	require.False(t, ok)
}

// TestFormatStackTraceIncludesInstructionPointer asserts that
// `stack-trace` rendering surfaces each frame's
// InstructionPointerReference. The IP is the canonical
// `memref` argument users feed to `debugger disassemble`, so
// the rendered transcript must show it explicitly. The
// disassemble help text refers users to the `ip:` line.
func TestFormatStackTraceIncludesInstructionPointer(t *testing.T) {
	frames := []dap.StackFrame{
		{
			Id:                          1,
			Name:                        "main.main",
			Line:                        42,
			InstructionPointerReference: "0x10b3a40",
		},
		{
			Id:   2,
			Name: "runtime.main",
			Line: 7,
			// No IP set: must still render cleanly without
			// an empty `ip:` line.
		},
	}
	body := formatStackTraceMarkdown(frames)
	assert.Contains(t, body, "ip: `0x10b3a40`",
		"first frame's IP must be rendered for disassemble use")
	assert.NotContains(t, body, "ip: ``",
		"frames without IP must not render an empty ip line")
}

// TestPromptHandler_NoArgs_OpensShell verifies that invoking
// ":debugger" with no arguments calls the WithOpenShell callback
// with "debugger" as a single arg, so the editor command opens
// (or focuses) the companion shell tab and submits "debugger" on
// its prompt.
func TestPromptHandler_NoArgs_OpensShell(t *testing.T) {
	h, _, _ := newTestHandler(t)
	var gotArgs []string
	var called int
	ph := NewPromptHandler(h).WithOpenShell(
		func(_ context.Context, args ...string) error {
			called++
			gotArgs = append([]string(nil), args...)
			return nil
		},
	)
	err := ph.HandleCommand(context.Background(), textapi.Command{
		Name: CommandName,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, called)
	assert.Equal(t, []string{CommandName}, gotArgs)
}

// TestPromptHandler_NoArgs_NoCallback ensures that when no
// WithOpenShell callback is wired, the legacy usage error is
// preserved so callers that do not opt in keep their behaviour.
func TestPromptHandler_NoArgs_NoCallback(t *testing.T) {
	h, _, _ := newTestHandler(t)
	ph := NewPromptHandler(h)
	err := ph.HandleCommand(context.Background(), textapi.Command{
		Name: CommandName,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage: debugger")
}
