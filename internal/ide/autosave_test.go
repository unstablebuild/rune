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

package ide

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/workspace"
)

func TestAutoSaver(t *testing.T) {
	uriA := mustURI(t, "memory:///tmp/a")
	uriB := mustURI(t, "memory:///tmp/b")
	uriFex := mustURI(t, fileExplorerURI)

	cases := []autoSaverCase{
		{
			name:        "flushes after idle delay",
			edits:       []autoSaverInput{{evt: textapi.EventTypeEdit, uri: uriA}},
			drainOnce:   true,
			wantCalls:   []workspaceapi.URI{uriA},
			wantNotifLn: 0,
		},
		{
			name: "debounces repeated edits to a single flush",
			edits: []autoSaverInput{
				{evt: textapi.EventTypeEdit, uri: uriA, sleep: 5 * time.Millisecond},
				{evt: textapi.EventTypeEdit, uri: uriA, sleep: 5 * time.Millisecond},
				{evt: textapi.EventTypeEdit, uri: uriA, sleep: 5 * time.Millisecond},
				{evt: textapi.EventTypeEdit, uri: uriA, sleep: 5 * time.Millisecond},
				{evt: textapi.EventTypeEdit, uri: uriA, sleep: 5 * time.Millisecond},
			},
			delay:       50 * time.Millisecond,
			drainOnce:   true,
			settle:      80 * time.Millisecond,
			wantCalls:   []workspaceapi.URI{uriA},
			wantNotifLn: 0,
		},
		{
			name: "manual flush event cancels pending auto-save",
			edits: []autoSaverInput{
				{evt: textapi.EventTypeEdit, uri: uriA},
				{evt: textapi.EventTypeFlush, uri: uriA},
			},
			delay:       50 * time.Millisecond,
			settle:      80 * time.Millisecond,
			wantCalls:   nil,
			wantNotifLn: 0,
		},
		{
			name: "close event cancels pending auto-save",
			edits: []autoSaverInput{
				{evt: textapi.EventTypeEdit, uri: uriA},
				{evt: textapi.EventTypeClose, uri: uriA},
			},
			delay:       50 * time.Millisecond,
			settle:      80 * time.Millisecond,
			wantCalls:   nil,
			wantNotifLn: 0,
		},
		{
			name: "per-uri isolation: cancelling A leaves B's timer",
			edits: []autoSaverInput{
				{evt: textapi.EventTypeEdit, uri: uriA},
				{evt: textapi.EventTypeEdit, uri: uriB},
				{evt: textapi.EventTypeFlush, uri: uriA},
			},
			delay:       30 * time.Millisecond,
			drainOnce:   true,
			settle:      60 * time.Millisecond,
			wantCalls:   []workspaceapi.URI{uriB},
			wantNotifLn: 0,
		},
		{
			name:        "skips closed tabs silently",
			edits:       []autoSaverInput{{evt: textapi.EventTypeEdit, uri: uriA}},
			missing:     []string{uriA.String()},
			drainOnce:   true,
			wantCalls:   nil,
			wantNotifLn: 0,
		},
		{
			name:           "stale data on disk surfaces a warning",
			edits:          []autoSaverInput{{evt: textapi.EventTypeEdit, uri: uriA}},
			preflightErr:   workspaceapi.ErrStaleData,
			drainOnce:      true,
			wantCalls:      []workspaceapi.URI{uriA},
			wantNotifLn:    1,
			wantNotifLevel: browserapi.LevelWarn,
		},
		{
			name:           "read-only file surfaces a warning",
			edits:          []autoSaverInput{{evt: textapi.EventTypeEdit, uri: uriA}},
			preflightErr:   workspaceapi.ErrFileIsNotWritable,
			drainOnce:      true,
			wantCalls:      []workspaceapi.URI{uriA},
			wantNotifLn:    1,
			wantNotifLevel: browserapi.LevelWarn,
		},
		{
			name:         "invalid-save tab kinds (e.g. terminal) are silently skipped",
			edits:        []autoSaverInput{{evt: textapi.EventTypeEdit, uri: uriA}},
			preflightErr: textapi.ErrInvalidSave,
			drainOnce:    true,
			wantCalls:    []workspaceapi.URI{uriA},
			wantNotifLn:  0,
		},
		{
			name:         "in-flight save is silently skipped (debounce will retry)",
			edits:        []autoSaverInput{{evt: textapi.EventTypeEdit, uri: uriA}},
			preflightErr: workspace.ErrFlushInProgress,
			drainOnce:    true,
			wantCalls:    []workspaceapi.URI{uriA},
			wantNotifLn:  0,
		},
		{
			name:        "fexplorer edits do not arm a flush timer",
			edits:       []autoSaverInput{{evt: textapi.EventTypeEdit, uri: uriFex}},
			delay:       30 * time.Millisecond,
			settle:      60 * time.Millisecond,
			wantCalls:   nil,
			wantNotifLn: 0,
			wantNoTimer: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runAutoSaverCase(t, tc)
		})
	}
}

// FIXTURES — kept below the test functions per project convention.

// autoSaverInput is a single event sent into autoSaver.Handle, with an
// optional inter-event sleep used by the debounce case.
type autoSaverInput struct {
	evt   textapi.EventType
	uri   workspaceapi.URI
	sleep time.Duration
}

// autoSaverCase is one row in the TestAutoSaver table.
type autoSaverCase struct {
	name string

	// inputs
	edits        []autoSaverInput
	missing      []string
	preflightErr error
	delay        time.Duration // defaults to 10ms
	drainOnce    bool          // pull a single scheduled callback
	settle       time.Duration // additional time to let stray timers fire

	// expectations
	wantCalls      []workspaceapi.URI
	wantNotifLn    int
	wantNotifLevel browserapi.NotificationLevel
	wantNoTimer    bool
}

func runAutoSaverCase(t *testing.T, tc autoSaverCase) {
	t.Helper()
	missing := map[string]bool{}
	for _, m := range tc.missing {
		missing[m] = true
	}
	flusher := &recordingFlusher{err: tc.preflightErr, missing: missing}
	notif := &fakeNotifications{}
	sched := newQueueSched()
	delay := tc.delay
	if delay == 0 {
		delay = 10 * time.Millisecond
	}
	saver := newAutoSaver(flusher, notif, sched.sched, delay)

	for _, in := range tc.edits {
		saver.Handle(context.Background(),
			textapi.Event{Type: in.evt, URI: in.uri})
		if in.sleep > 0 {
			time.Sleep(in.sleep)
		}
	}

	if tc.drainOnce {
		sched.drain(t)
	}
	if tc.settle > 0 {
		time.Sleep(tc.settle)
		sched.drainAll()
	}

	if tc.wantCalls == nil {
		assert.Empty(t, flusher.calls)
	} else {
		assert.Equal(t, tc.wantCalls, flusher.calls)
	}
	if tc.wantNoTimer {
		assert.Empty(t, saver.timers,
			"expected no debounce timers to be armed")
	}
	if tc.wantNotifLn == 0 {
		assert.Empty(t, notif.notes)
	} else {
		require.Len(t, notif.notes, tc.wantNotifLn)
		assert.Equal(t, tc.wantNotifLevel, notif.notes[0].level)
	}
}

// recordingFlusher implements autoSaverFlusher and records every URI it is
// asked to flush. All calls happen on the test goroutine because tests
// drain the scheduler queue inline (matching the editor's single-threaded
// event dispatch).
type recordingFlusher struct {
	calls   []workspaceapi.URI
	err     error
	missing map[string]bool
}

func (r *recordingFlusher) Resource(uri workspaceapi.URI) (browserapi.Handler, bool) {
	if r.missing[uri.String()] {
		return nil, false
	}
	return recordingHandler{uri: uri}, true
}

func (r *recordingFlusher) FlushTab(
	_ context.Context, h browserapi.Handler,
) (<-chan error, error) {
	rh := h.(recordingHandler)
	r.calls = append(r.calls, rh.uri)
	if r.err != nil {
		// pre-flight error (e.g. ErrInvalidSave / ErrFlushInProgress).
		// Return nil channel like the production interface.
		return nil, r.err
	}
	// async completion: no error.
	ch := make(chan error, 1)
	ch <- nil
	close(ch)
	return ch, nil
}

// recordingHandler is a minimal browserapi.Handler used to round-trip a
// URI from Resource into FlushTab.
type recordingHandler struct{ uri workspaceapi.URI }

func (recordingHandler) Resize(_, _ int)                          {}
func (recordingHandler) Draw(_ term.Writer)                       {}
func (recordingHandler) Handle(_ term.Event) (exit, handled bool) { return false, false }
func (recordingHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}
func (recordingHandler) Selection() (string, bool) { return "", false }
func (recordingHandler) Close() error              { return nil }

var _ tui.Handler = recordingHandler{}

// fakeNotifications captures calls into browserapi.Notifications.
type fakeNotifications struct {
	notes []notifRecord
}

type notifRecord struct {
	level browserapi.NotificationLevel
	msg   string
}

func (f *fakeNotifications) Notify(level browserapi.NotificationLevel,
	msg string, _ ...any) (string, error) {
	f.notes = append(f.notes, notifRecord{level: level, msg: msg})
	return "", nil
}

func (f *fakeNotifications) NotifyOnce(level browserapi.NotificationLevel,
	msg string, args ...any) (string, error) {
	return f.Notify(level, msg, args...)
}

func (f *fakeNotifications) UpdateNotificationProgress(_, _ string, _, _ int64) error {
	return nil
}

// queueSched mimics scheduleNextTick by enqueuing scheduled callbacks onto
// a buffered channel. Tests drain the queue inline so all flushURI calls
// run on the test goroutine, matching the editor's single-threaded event
// dispatch.
type queueSched struct{ q chan func() }

func newQueueSched() *queueSched { return &queueSched{q: make(chan func(), 16)} }

func (s *queueSched) sched(fn func()) bool {
	s.q <- fn
	return true
}

// drain pulls the next scheduled callback and runs it. It fails the test
// if no callback shows up within the timeout.
func (s *queueSched) drain(t *testing.T) {
	t.Helper()
	select {
	case fn := <-s.q:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for scheduled flush")
	}
}

// drainAll runs everything currently queued.
func (s *queueSched) drainAll() {
	for {
		select {
		case fn := <-s.q:
			fn()
		default:
			return
		}
	}
}

func mustURI(t *testing.T, raw string) workspaceapi.URI {
	t.Helper()
	u, err := workspaceapi.ParseURI(raw)
	require.NoError(t, err)
	return u
}
