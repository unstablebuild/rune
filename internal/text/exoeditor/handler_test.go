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

package exoeditor

import (
	"context"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vteprobe"
	"unstable.build/rune/internal/text"
)

func TestPickLocation(t *testing.T) {
	t.Parallel()

	locs := []textapi.Location{
		{From: term.Coordinates{X: 0, Y: 1}},
		{From: term.Coordinates{X: 4, Y: 3}},
		{From: term.Coordinates{X: 2, Y: 5}},
	}

	cases := []struct {
		name    string
		cursor  term.Coordinates
		forward bool
		want    term.Coordinates
	}{
		{
			name:    "next from start jumps to first",
			cursor:  term.Coordinates{X: 0, Y: 0},
			forward: true,
			want:    locs[0].From,
		},
		{
			name:    "next from middle jumps to next",
			cursor:  term.Coordinates{X: 0, Y: 3},
			forward: true,
			want:    locs[1].From,
		},
		{
			name:    "next past last wraps to first",
			cursor:  term.Coordinates{X: 9, Y: 9},
			forward: true,
			want:    locs[0].From,
		},
		{
			name:    "prev from end jumps to last",
			cursor:  term.Coordinates{X: 9, Y: 9},
			forward: false,
			want:    locs[2].From,
		},
		{
			name:    "prev from middle jumps to prev",
			cursor:  term.Coordinates{X: 9, Y: 3},
			forward: false,
			want:    locs[1].From,
		},
		{
			name:    "prev before first wraps to last",
			cursor:  term.Coordinates{X: 0, Y: 0},
			forward: false,
			want:    locs[2].From,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := pickLocation(
				text.LocationSlice(locs), tc.cursor, tc.forward)
			assert.True(t, ok)
			assert.Equal(t, tc.want, got.From)
		})
	}
}

func TestPickLocationEmptyListReturnsFalse(t *testing.T) {
	t.Parallel()
	_, ok := pickLocation(
		text.LocationSlice(nil), term.Coordinates{}, true)
	assert.False(t, ok)
}

func TestMoveToLocationReturnsFalseForUnknownList(t *testing.T) {
	t.Parallel()
	h := &editorHandler{locations: text.NewLocationStore()}
	assert.False(t, h.MoveToNextLocation("missing"))
	assert.False(t, h.MoveToPrevLocation("missing"))
}

// stubVTEHandler records the events the readiness gate injects so tests
// can assert on the keystrokes forwarded to the embedded editor without
// a live vte. Every other vteHandler method is unused by these tests and
// panics if exercised, surfacing accidental dependencies.
type stubVTEHandler struct {
	events      []term.Event
	closeCalled chan struct{}
	closeOnce   sync.Once
}

func (s *stubVTEHandler) Handle(ev term.Event) (bool, bool) {
	s.events = append(s.events, ev)
	return false, true
}

func (s *stubVTEHandler) Resize(int, int)                                    { panic("unused") }
func (s *stubVTEHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) { panic("unused") }
func (s *stubVTEHandler) Selection() (string, bool)                          { panic("unused") }
func (s *stubVTEHandler) Draw(term.Writer)                                   { panic("unused") }
func (s *stubVTEHandler) MaxSeekOffset() int                                 { panic("unused") }
func (s *stubVTEHandler) SeekOffset() int                                    { panic("unused") }
func (s *stubVTEHandler) SeekUp() bool                                       { panic("unused") }
func (s *stubVTEHandler) SeekDown() bool                                     { panic("unused") }

// closeCalled is closed the first time Close is invoked so tests that
// drive the Close goroutine can wait for the (now-clean) pty teardown.
func (s *stubVTEHandler) Close() error {
	if s.closeCalled != nil {
		s.closeOnce.Do(func() { close(s.closeCalled) })
	}
	return nil
}

// gotoReadinessHarness builds a minimal editorHandler with a real goto
// template, a recording vteHandler stub, and a recording scheduleNextTick
// so the readiness gate can be exercised without a live vte.
type gotoReadinessHarness struct {
	h         *editorHandler
	vte       *stubVTEHandler
	component *fakeComponent
	executor  *recordingSignalExecutor
	scheduled []func()
}

func newGotoReadinessHarness(t *testing.T, tpl string) *gotoReadinessHarness {
	t.Helper()
	parsed, err := parseGotoTemplate(tpl)
	require.NoError(t, err)

	hr := &gotoReadinessHarness{}
	hr.vte = &stubVTEHandler{}
	hr.component = &fakeComponent{version: 1, pid: 4242}
	hr.executor = &recordingSignalExecutor{}
	h := &editorHandler{
		gotoTemplate: parsed,
		vteHandler:   hr.vte,
		component:    hr.component,
		executor:     hr.executor,
	}
	h.scheduleNextTick = func(fn func()) bool {
		hr.scheduled = append(hr.scheduled, fn)
		return true
	}
	hr.h = h
	return hr
}

// wantEvents renders tpl for the 1-based coords of pos and converts the
// keys into the events injectGoto forwards, for exact assertion.
func wantEvents(g gotoTemplate, pos term.Coordinates) []term.Event {
	keys := g.Render(pos.Y+1, pos.X+1)
	out := make([]term.Event, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyCombToEvent(k))
	}
	return out
}

// markReady simulates refreshProbe's first successful probe: it stores a
// probe result and flushes any pending goto via scheduleNextTick.
func (hr *gotoReadinessHarness) markReady() {
	h := hr.h
	h.probeStateMu.Lock()
	firstProbe := h.lastProbe.Load() == nil
	h.lastProbe.Store(&vteprobe.Result{})
	var flush *term.Coordinates
	var flushQuit bool
	if firstProbe {
		flush = h.pendingGoto
		h.pendingGoto = nil
		flushQuit = h.pendingQuit
		h.pendingQuit = false
	}
	h.probeStateMu.Unlock()

	if flushQuit {
		h.scheduleNextTick(h.dispatchQuit)
	}
	if flush != nil {
		pos := *flush
		h.scheduleNextTick(func() { h.injectGoto(pos) })
	}
}

func (hr *gotoReadinessHarness) runScheduled() {
	for _, fn := range hr.scheduled {
		fn()
	}
}

const readinessTpl = "<esc>:{line}<enter>"

func TestSetCursorAtScrollGatedUntilReady(t *testing.T) {
	hr := newGotoReadinessHarness(t, readinessTpl)
	pos := term.Coordinates{X: 4, Y: 9}

	require.True(t, hr.h.SetCursorAtScroll(pos))
	assert.Empty(t, hr.vte.events, "keys must not be injected before the editor is ready")
	assert.Empty(t, hr.scheduled, "nothing scheduled before first probe")
	require.NotNil(t, hr.h.pendingGoto)
	assert.Equal(t, pos, *hr.h.pendingGoto)

	hr.markReady()
	require.Len(t, hr.scheduled, 1, "first probe flushes the queued goto exactly once")
	assert.Empty(t, hr.vte.events, "flush is deferred to the scheduled tick")
	assert.Nil(t, hr.h.pendingGoto)

	hr.runScheduled()
	assert.Equal(t, wantEvents(hr.h.gotoTemplate, pos), hr.vte.events)
}

func TestSetCursorAtScrollReadyInjectsImmediately(t *testing.T) {
	hr := newGotoReadinessHarness(t, readinessTpl)
	hr.markReady()
	require.Empty(t, hr.scheduled, "no pending goto means nothing is scheduled")

	pos := term.Coordinates{X: 2, Y: 5}
	require.True(t, hr.h.SetCursorAtScroll(pos))
	assert.Empty(t, hr.scheduled, "ready path injects synchronously, no tick")
	assert.Equal(t, wantEvents(hr.h.gotoTemplate, pos), hr.vte.events)
}

func TestSecondProbeDoesNotReflush(t *testing.T) {
	hr := newGotoReadinessHarness(t, readinessTpl)
	require.True(t, hr.h.SetCursorAtScroll(term.Coordinates{X: 1, Y: 1}))

	hr.markReady()
	require.Len(t, hr.scheduled, 1)

	hr.markReady()
	assert.Len(t, hr.scheduled, 1, "a subsequent probe must not re-schedule the goto")
}

func TestSetCursorAtScrollEmptyTemplate(t *testing.T) {
	hr := newGotoReadinessHarness(t, "")
	assert.False(t, hr.h.SetCursorAtScroll(term.Coordinates{X: 1, Y: 1}))
	assert.Empty(t, hr.vte.events)
	assert.Empty(t, hr.scheduled)
	assert.Nil(t, hr.h.pendingGoto)
}

type recordingNotifications struct {
	browserapi.Notifications
	mu     sync.Mutex
	levels []browserapi.NotificationLevel
}

func (n *recordingNotifications) Notify(
	level browserapi.NotificationLevel, _ string, _ ...any,
) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.levels = append(n.levels, level)
	return "", nil
}

func (n *recordingNotifications) warned() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Contains(n.levels, browserapi.LevelWarn)
}

type recordingSignalExecutor struct {
	mu      sync.Mutex
	pid     workspaceapi.Pid
	signals []syscall.Signal
}

func (e *recordingSignalExecutor) StartCommand(
	context.Context, workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	return 0, nil
}

func (e *recordingSignalExecutor) Signal(
	pid workspaceapi.Pid, sig syscall.Signal,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pid = pid
	e.signals = append(e.signals, sig)
	return nil
}

func (e *recordingSignalExecutor) Close() error { return nil }

func (e *recordingSignalExecutor) sentSignals() []syscall.Signal {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]syscall.Signal(nil), e.signals...)
}

func TestCloseDefersQuitUntilReady(t *testing.T) {
	quit, err := term.ParseKeys("<esc>:qa!<enter>")
	require.NoError(t, err)

	hr := newGotoReadinessHarness(t, readinessTpl)
	hr.h.quitKeys = quit
	// procDone never delivers and the timeout is long, so the teardown
	// goroutine stays parked past the end of this test and never races
	// the harness slices; the test only asserts the readiness gate.
	hr.h.procDone = make(chan error)
	hr.h.gracefulQuitTimeout = time.Hour
	hr.h.notifications = &recordingNotifications{}
	_, hr.h.cancelCtx = context.WithCancel(context.Background())

	require.NoError(t, hr.h.Close())
	require.True(t, hr.h.pendingQuit,
		"closing before the first probe must stash the quit sequence")
	assert.Empty(t, hr.vte.events,
		"quit keys must not be dispatched before the editor is ready")

	hr.markReady()
	require.NotEmpty(t, hr.scheduled,
		"first probe must schedule the deferred quit")
	assert.False(t, hr.h.pendingQuit, "pendingQuit cleared after flush")
	hr.runScheduled()

	want := make([]term.Event, 0, len(quit))
	for _, k := range quit {
		want = append(want, keyCombToEvent(k))
	}
	assert.Equal(t, want, hr.vte.events,
		"the deferred quit sequence must reach the editor once it is ready")
}

func TestCloseReadyDispatchesQuitImmediately(t *testing.T) {
	quit, err := term.ParseKeys("<esc>:qa!<enter>")
	require.NoError(t, err)

	hr := newGotoReadinessHarness(t, readinessTpl)
	hr.h.quitKeys = quit
	hr.h.procDone = make(chan error)
	hr.h.gracefulQuitTimeout = time.Hour
	hr.h.notifications = &recordingNotifications{}
	_, hr.h.cancelCtx = context.WithCancel(context.Background())
	hr.markReady()

	require.NoError(t, hr.h.Close())
	assert.False(t, hr.h.pendingQuit)

	want := make([]term.Event, 0, len(quit))
	for _, k := range quit {
		want = append(want, keyCombToEvent(k))
	}
	assert.Equal(t, want, hr.vte.events,
		"a ready editor receives the quit synchronously from Close")
}

func TestCloseTimeoutWarnsAndCloses(t *testing.T) {
	hr := newGotoReadinessHarness(t, readinessTpl)
	hr.h.procDone = make(chan error) // never delivers, forcing the timeout
	hr.h.gracefulQuitTimeout = 10 * time.Millisecond
	hr.h.hangUpGraceTimeout = 10 * time.Millisecond
	uri, err := workspaceapi.ParseURI("file:///x.txt")
	require.NoError(t, err)
	hr.h.resource = uri
	hr.vte.closeCalled = make(chan struct{})
	notes := &recordingNotifications{}
	hr.h.notifications = notes
	_, hr.h.cancelCtx = context.WithCancel(context.Background())
	hr.h.scheduleNextTick = func(fn func()) bool { fn(); return true }

	require.NoError(t, hr.h.Close())

	select {
	case <-hr.vte.closeCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("close goroutine never tore down the pty after timeout")
	}
	assert.True(t, notes.warned(),
		"hitting the graceful-quit timeout must surface a Warn so an "+
			"editor that ignored :qa! is visible to the user")
	assert.Equal(t, []syscall.Signal{syscall.SIGHUP}, hr.executor.sentSignals(),
		"the timeout path must SIGHUP the editor for a clean hangup "+
			"before the pty is torn down")
}
