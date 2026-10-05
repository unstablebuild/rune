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

package texttest_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

// inlineSchedule is a synchronous ScheduleNextTick stub local to
// this test file. It mirrors the runner used elsewhere in texttest.
func inlineSchedule(fn func()) bool { fn(); return true }

func writeNLines(t *testing.T, path string, n int) {
	t.Helper()
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o644))
}

// newStreamingComponent wires text.Component against a real local
// file scheme so the streaming open path's OpenFile pre-read and
// background workspace.Load can both observe real files. Returns
// the component and the temp workspace URI used for files.
func newStreamingComponent(
	t *testing.T,
) (*text.Component, workspaceapi.URI) {
	t.Helper()
	dir := t.TempDir()
	wsURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), wsURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	ws := workspace.NewSchemeWorkspace(wsURI, scheme, inlineSchedule)

	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = inlineSchedule
	cfg.StreamingOpen = true
	c, err := text.NewComponent(texttest.NopEditor(), ws, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c, wsURI
}

func TestStreamingOpenReplacesHandlerAfterLoad(t *testing.T) {
	c, wsURI := newStreamingComponent(t)

	fpath := filepath.Join(wsURI.Path(), "a.txt")
	writeNLines(t, fpath, 5)
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	h, err := c.OpenFileTab(fileURI, false)
	require.NoError(t, err)
	require.NotNil(t, h)

	c.WaitStreamingLoads()

	// After the swap, the tab's handler must satisfy text.Handler so
	// downstream IDE code (Editor lookup, cursor, command dispatch)
	// works against the real editor and not the streaming placeholder.
	ed, err := c.Editor(fileURI)
	require.NoError(t, err, "Editor() must find a text.Handler post-swap")
	assert.NotNil(t, ed)
}

func TestStreamingOpenReadOnlyMissingFileErrors(t *testing.T) {
	c, wsURI, sched, noti := newStreamingComponentWithScheduler(t)

	// readOnly=true so the missing file must error instead of
	// silently creating an empty buffer.
	fileURI, err := workspaceapi.ParseURI(
		"file://" + filepath.Join(wsURI.Path(), "nope.txt"))
	require.NoError(t, err)

	h, err := c.OpenFileTab(fileURI, true)
	require.NoError(t, err,
		"OpenFileTab must not block on nor surface the async open error")
	require.NotNil(t, h)

	c.WaitStreamingLoads()
	sched.drain()

	_, err = c.Editor(fileURI)
	require.Error(t, err,
		"placeholder tab must be removed after the failed open")
	msgs := noti.snapshot()
	require.NotEmpty(t, msgs, "the open error must surface as a notification")
	assert.Contains(t, msgs[len(msgs)-1], "nope.txt")
}

func TestStreamingOpenCreatesEmptyBufferForMissingFile(t *testing.T) {
	c, wsURI, sched, _ := newStreamingComponentWithScheduler(t)

	fpath := filepath.Join(wsURI.Path(), "new.txt")
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	h, err := c.OpenFileTab(fileURI, false)
	require.NoError(t, err)
	require.NotNil(t, h)

	c.WaitStreamingLoads()
	sched.drain()

	ed, err := c.Editor(fileURI)
	require.NoError(t, err,
		"Editor() must find a text.Handler for a freshly opened missing file")
	require.NotNil(t, ed)

	// The file is only materialized on first flush, mirroring the
	// sync path. Confirm that contract still holds by writing through
	// the editor and flushing the tab.
	_, statErr := os.Stat(fpath)
	require.True(t, os.IsNotExist(statErr),
		"file should not exist yet before flush, got: %v", statErr)
}

func TestStreamingOpenSurfacesPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permission checks")
	}
	c, wsURI, sched, noti := newStreamingComponentWithScheduler(t)

	fpath := filepath.Join(wsURI.Path(), "denied.txt")
	writeNLines(t, fpath, 3)
	require.NoError(t, os.Chmod(fpath, 0o000))
	t.Cleanup(func() { _ = os.Chmod(fpath, 0o644) })

	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	h, err := c.OpenFileTab(fileURI, false)
	require.NoError(t, err)
	require.NotNil(t, h)

	c.WaitStreamingLoads()
	sched.drain()

	_, err = c.Editor(fileURI)
	require.Error(t, err,
		"placeholder tab must be removed after the failed open")
	var found bool
	for _, msg := range noti.snapshot() {
		if strings.Contains(msg, "permission denied") {
			found = true
		}
	}
	assert.True(t, found,
		"expected a permission-denied notification, got: %v", noti.snapshot())
}

func TestSyncOpenCreatesEmptyBufferForMissingFile(t *testing.T) {
	dir := t.TempDir()
	wsURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), wsURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	ws := workspace.NewSchemeWorkspace(wsURI, scheme, inlineSchedule)

	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = inlineSchedule
	c, err := text.NewComponent(texttest.NopEditor(), ws, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	fpath := filepath.Join(dir, "new.txt")
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	h, err := c.OpenFileTab(fileURI, false)
	require.NoError(t, err)
	require.NotNil(t, h)

	ed, err := c.Editor(fileURI)
	require.NoError(t, err)
	require.NotNil(t, ed)

	_, statErr := os.Stat(fpath)
	require.True(t, os.IsNotExist(statErr),
		"file should not exist yet before flush, got: %v", statErr)
}

func TestStreamingOpenDisabledFallsBackToSync(t *testing.T) {
	dir := t.TempDir()
	wsURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), wsURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	ws := workspace.NewSchemeWorkspace(wsURI, scheme, inlineSchedule)

	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = inlineSchedule
	// StreamingOpen left false (the default).
	c, err := text.NewComponent(texttest.NopEditor(), ws, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	fpath := filepath.Join(dir, "a.txt")
	writeNLines(t, fpath, 3)
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	_, err = c.OpenFileTab(fileURI, false)
	require.NoError(t, err)

	// No goroutines spawned, no wait needed.
	ed, err := c.Editor(fileURI)
	require.NoError(t, err)
	assert.NotNil(t, ed)
}

// queuedScheduler is a manual ScheduleNextTick implementation: it
// captures every callback into a FIFO so the test can drive the
// event loop one step at a time. Used by the streaming-open
// regression tests below to interleave a user-initiated tab close
// between the load goroutine queueing the swap callback and the
// swap callback running.
type queuedScheduler struct {
	mu      sync.Mutex
	pending []func()
}

func (s *queuedScheduler) Schedule(fn func()) bool {
	s.mu.Lock()
	s.pending = append(s.pending, fn)
	s.mu.Unlock()
	return true
}

// drain runs every pending callback (including any queued by those
// callbacks) until the queue is empty.
func (s *queuedScheduler) drain() {
	for {
		s.mu.Lock()
		if len(s.pending) == 0 {
			s.mu.Unlock()
			return
		}
		fn := s.pending[0]
		s.pending = s.pending[1:]
		s.mu.Unlock()
		fn()
	}
}

// recordingNotifications records every Notify message so a test can
// assert which notifications fired during a flow.
type recordingNotifications struct {
	mu       sync.Mutex
	messages []string
}

func (n *recordingNotifications) Notify(
	_ browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	n.mu.Lock()
	n.messages = append(n.messages, fmt.Sprintf(msg, args...))
	n.mu.Unlock()
	return "", nil
}

func (n *recordingNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *recordingNotifications) UpdateNotificationProgress(
	string, string, int64, int64,
) error {
	return nil
}

func (n *recordingNotifications) snapshot() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.messages))
	copy(out, n.messages)
	return out
}

// newStreamingComponentWithScheduler is like newStreamingComponent
// but installs a manual queuedScheduler and a recordingNotifications
// so tests can drive the swap callback explicitly and observe any
// surfaced notifications.
func newStreamingComponentWithScheduler(
	t *testing.T,
) (
	*text.Component, workspaceapi.URI,
	*queuedScheduler, *recordingNotifications,
) {
	t.Helper()
	dir := t.TempDir()
	wsURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), wsURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	// The workspace itself uses inlineSchedule so its own internal
	// async machinery doesn't sit behind our test's queue.
	ws := workspace.NewSchemeWorkspace(wsURI, scheme, inlineSchedule)

	sched := &queuedScheduler{}
	noti := &recordingNotifications{}

	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = sched.Schedule
	cfg.Notifications = noti
	cfg.StreamingOpen = true
	c, err := text.NewComponent(texttest.NopEditor(), ws, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c, wsURI, sched, noti
}

func TestStreamingOpenSilentWhenTabClosedDuringLoad(t *testing.T) {
	c, wsURI, sched, noti := newStreamingComponentWithScheduler(t)

	fpath := filepath.Join(wsURI.Path(), "huge.log")
	writeNLines(t, fpath, 10)
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	// Open via the streaming path. With the queued scheduler the
	// background load goroutine populates its buffer and queues the
	// swap callback, but the callback has not yet been drained.
	h, err := c.OpenFileTab(fileURI, false)
	require.NoError(t, err)
	require.NotNil(t, h)

	// Block until the background goroutine finishes loading and has
	// queued its swap callback on the scheduler. The callback is
	// still pending in sched.pending at this point.
	c.WaitStreamingLoads()

	// Simulate the user closing the streaming tab while the swap
	// callback is still pending on the event loop.
	require.True(t, c.Browser().RemoveTab(h))

	// Now drain the scheduler so the swap callback runs.
	sched.drain()

	// The user already closed the tab so they do not want to see a
	// notification — neither success nor failure.
	assert.Empty(t, noti.snapshot(),
		"no notifications should fire after the streaming tab was closed")
}

func TestStreamingOpenIgnoresErrorAfterTabClose(t *testing.T) {
	dir := t.TempDir()
	wsURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), wsURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	realWS := workspace.NewSchemeWorkspace(wsURI, scheme, inlineSchedule)
	ws := &failingLoadWorkspace{
		Workspace: realWS,
		loadErr:   workspaceapi.ErrStaleData,
	}

	sched := &queuedScheduler{}
	noti := &recordingNotifications{}

	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = sched.Schedule
	cfg.Notifications = noti
	cfg.StreamingOpen = true
	c, err := text.NewComponent(texttest.NopEditor(), ws, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	fpath := filepath.Join(dir, "huge.log")
	writeNLines(t, fpath, 10)
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	h, err := c.OpenFileTab(fileURI, false)
	require.NoError(t, err)
	require.NotNil(t, h)

	// The load goroutine returns the injected ErrStaleData and
	// queues the failure callback on sched. The user now closes the
	// streaming tab before the swap callback runs.
	c.WaitStreamingLoads()
	require.True(t, c.Browser().RemoveTab(h))

	sched.drain()

	// No "open ...:" notification should fire even though the swap
	// callback ran after the tab was removed.
	for _, msg := range noti.snapshot() {
		assert.NotContains(t, msg, "open ",
			"no open-error notification expected after tab close: %q", msg)
	}
}

// failingLoadWorkspace wraps a real text.Workspace and forces
// Load/Recover to return a configured error. Used to reproduce
// the close-during-load notification bug.
type failingLoadWorkspace struct {
	text.Workspace
	mu      sync.Mutex
	loadErr error
}

func (w *failingLoadWorkspace) err() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.loadErr
}

func (w *failingLoadWorkspace) setErr(e error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.loadErr = e
}

func (w *failingLoadWorkspace) Load(
	file workspaceapi.URI, _ *cell.Buffer,
	_ workspaceapi.URI, _ bool,
) (workspace.FlusherCloser, error) {
	return nil, w.err()
}

func (w *failingLoadWorkspace) Recover(
	_ workspaceapi.URI, _ workspaceapi.URI,
	_ *cell.Buffer, _ bool,
) (workspace.FlusherCloser, error) {
	return nil, w.err()
}

func TestStreamingOpenStaleRecoverShowsAreYouSurePrompt(t *testing.T) {
	dir := t.TempDir()
	wsURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), wsURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	realWS := workspace.NewSchemeWorkspace(wsURI, scheme, inlineSchedule)
	ws := &failingLoadWorkspace{
		Workspace: realWS,
		loadErr:   workspaceapi.ErrFileAlreadyOpen,
	}

	sched := &queuedScheduler{}
	noti := &recordingNotifications{}
	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = sched.Schedule
	cfg.Notifications = noti
	cfg.StreamingOpen = true
	c, err := text.NewComponent(texttest.NopEditor(), ws, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	c.Resize(60, 20)

	fpath := filepath.Join(dir, "stale.txt")
	writeNLines(t, fpath, 3)
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	// Step 1: open the file. The background Load returns
	// ErrFileAlreadyOpen. Wait for the load goroutine to finish
	// and queue its swap callback on the scheduler, then drain
	// the queue on the main goroutine so the callback's UI work
	// happens here. That sequence routes through the recovery
	// prompt and serializes the prompt-installation with the
	// browser state changes on a single goroutine.
	_, err = c.OpenFileTab(fileURI, false)
	require.NoError(t, err)
	c.WaitStreamingLoads()
	sched.drain()

	// The recovery prompt is now floating in focus.
	focus, err := c.Focus()
	require.NoError(t, err)
	require.True(t, focus.IsFloating(),
		"recovery prompt should be floating after streaming load failure")

	// Step 2: flip the injected error to ErrStaleData, then press
	// 'R' (Recover). The handler in openRecoveryPrompt invokes
	// recoverOpenFileTab, which re-enters the streaming open path
	// with a non-empty recoveryFilename. The background goroutine
	// hits failingLoadWorkspace.Recover and surfaces ErrStaleData.
	ws.setErr(workspaceapi.ErrStaleData)
	_, handled := c.Handle(term.Event{Type: term.EventKey, Ch: 'R'})
	require.True(t, handled, "recovery prompt should handle 'R'")
	c.WaitStreamingLoads()
	sched.drain()

	// The streaming-recovery failure must open the are-you-sure
	// prompt — not surface a generic error notification.
	focus, err = c.Focus()
	require.NoError(t, err)
	assert.True(t, focus.IsFloating(),
		"are-you-sure prompt should be floating after stale recover")
	for _, msg := range noti.snapshot() {
		assert.NotContains(t, msg, "stale data",
			"no stale-data error notification expected: %q", msg)
		assert.NotContains(t, msg, "open file://",
			"no generic open-error notification expected: %q", msg)
	}
}

func TestStreamingOpenPreservesCursorSetDuringLoad(t *testing.T) {
	c, wsURI, sched, _ := newStreamingComponentWithScheduler(t)

	fpath := filepath.Join(wsURI.Path(), "src.txt")
	writeNLines(t, fpath, 20)
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	h, err := c.OpenFileTab(fileURI, false)
	require.NoError(t, err)
	require.NotNil(t, h)

	// Pre-swap: the tab's handler must already satisfy text.Handler
	// so call sites like ide.openPrevSessionFiles, idecursor.navigate,
	// and ex.moveFocusCursor (which all do tab.Handler().(text.Handler))
	// succeed.
	ed, err := c.Editor(fileURI)
	require.NoError(t, err,
		"Editor() must return a text.Handler during the async-load window")

	// Apply a cursor — the wrapper queues it because the real editor
	// handler is not installed yet. The call must report success so
	// callers do not fall back to alternative paths or surface an
	// "errInvalidSetCursor"-style error.
	target := term.Coordinates{Y: 5}
	require.True(t, ed.SetCursorAtScroll(target),
		"pre-swap SetCursorAtScroll must report success")

	// Wait for the load goroutine to queue the swap callback on the
	// scheduler, then drain so the swap runs. The deferred wrapper
	// applies the queued cursor to the real handler at swap time.
	c.WaitStreamingLoads()
	sched.drain()

	// Look up the post-swap handler (deferred wrapper hides the swap
	// from upstream lookups) and confirm the cursor landed where the
	// caller requested.
	ed, err = c.Editor(fileURI)
	require.NoError(t, err)
	assert.Equal(t, target, ed.CursorAtScroll(),
		"cursor set during the streaming-load window must land on the real handler after swap")
}

func TestStreamingOpenAppliesMutationsDuringLoad(t *testing.T) {
	c, wsURI, sched, _ := newStreamingComponentWithScheduler(t)

	fpath := filepath.Join(wsURI.Path(), "mut.txt")
	writeNLines(t, fpath, 12)
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	_, err = c.OpenFileTab(fileURI, false)
	require.NoError(t, err)

	ed, err := c.Editor(fileURI)
	require.NoError(t, err)

	// All of these are no-throw, no-fallback calls. They are queued
	// and replayed at swap time. We only assert they do not panic
	// and that LocationLists round-trips post-swap.
	ed.SetWrap(true)
	ed.ShowCommandBar(false)
	ed.SetDefaultAttributes(term.Attributes{})

	c.WaitStreamingLoads()
	sched.drain()

	ed, err = c.Editor(fileURI)
	require.NoError(t, err)
	// The real editor handler is now in place; querying location
	// lists must not panic and the post-swap handler is responsive
	// to fresh mutations.
	assert.NotNil(t, ed.CellView())
}

// goroutineRecordingEditor wraps a TestEditor and records the
// goroutine that called Edit. Used by the regression test below to
// prove buildEditorHandler runs on the scheduler goroutine rather
// than the streaming-load worker.
type goroutineRecordingEditor struct {
	*texttest.TestEditor
	mu        sync.Mutex
	editGoID  uint64
	editCalls int
}

func (e *goroutineRecordingEditor) Edit(
	ctx context.Context,
	resource workspaceapi.URI, buf *cell.Buffer, readOnly, recovered bool,
) (text.Handler, error) {
	e.mu.Lock()
	e.editGoID = currentGoID()
	e.editCalls++
	e.mu.Unlock()
	return e.TestEditor.Edit(ctx, resource, buf, readOnly, recovered)
}

// goroutineTrackingScheduler is a queuedScheduler that also records
// the goroutine running each scheduled callback. It serves both as
// the test's ScheduleNextTick and as the "event loop" goroutine
// identity reference.
type goroutineTrackingScheduler struct {
	queuedScheduler
	mu       sync.Mutex
	lastRun  uint64
	runCount int
}

func (s *goroutineTrackingScheduler) drainOnce() bool {
	s.queuedScheduler.mu.Lock()
	if len(s.queuedScheduler.pending) == 0 {
		s.queuedScheduler.mu.Unlock()
		return false
	}
	fn := s.queuedScheduler.pending[0]
	s.queuedScheduler.pending = s.queuedScheduler.pending[1:]
	s.queuedScheduler.mu.Unlock()

	s.mu.Lock()
	s.lastRun = currentGoID()
	s.runCount++
	s.mu.Unlock()
	fn()
	return true
}

func (s *goroutineTrackingScheduler) drainAll() {
	for s.drainOnce() {
	}
}

func TestStreamingOpenBuildsEditorOnScheduler(t *testing.T) {
	dir := t.TempDir()
	wsURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), wsURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	ws := workspace.NewSchemeWorkspace(wsURI, scheme, inlineSchedule)

	sched := &goroutineTrackingScheduler{}
	ed := &goroutineRecordingEditor{TestEditor: texttest.NopEditor()}

	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = sched.Schedule
	cfg.StreamingOpen = true
	c, err := text.NewComponent(ed, ws, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	fpath := filepath.Join(wsURI.Path(), "probe.txt")
	writeNLines(t, fpath, 8)
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	_, err = c.OpenFileTab(fileURI, false)
	require.NoError(t, err)

	// Wait for the load worker to finish I/O and queue the swap
	// callback. Edit must NOT have been called yet — that's the
	// whole point.
	c.WaitStreamingLoads()

	ed.mu.Lock()
	require.Zero(t, ed.editCalls,
		"Edit must not run on the load worker goroutine")
	ed.mu.Unlock()

	sched.drainAll()

	ed.mu.Lock()
	editGoID := ed.editGoID
	editCalls := ed.editCalls
	ed.mu.Unlock()
	require.Equal(t, 1, editCalls, "Edit must run exactly once")

	sched.mu.Lock()
	lastRun := sched.lastRun
	sched.mu.Unlock()
	require.Equal(t, lastRun, editGoID,
		"buildEditorHandler.Edit must run on the scheduler goroutine, "+
			"not the load worker")
}

// blockingOpenWorkspace wraps a real text.Workspace and blocks the
// first OpenFile call until release is closed. It simulates an
// unresponsive remote (ssh) workspace whose OpenFile RPC hangs.
type blockingOpenWorkspace struct {
	text.Workspace
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingOpenWorkspace) OpenFile(
	p string, flag int, perm os.FileMode,
) (workspaceapi.File, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.Workspace.OpenFile(p, flag, perm)
}

func TestStreamingOpenDoesNotBlockOnFileOpen(t *testing.T) {
	dir := t.TempDir()
	wsURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), wsURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	realWS := workspace.NewSchemeWorkspace(wsURI, scheme, inlineSchedule)
	ws := &blockingOpenWorkspace{
		Workspace: realWS,
		entered:   make(chan struct{}),
		release:   make(chan struct{}),
	}

	sched := &queuedScheduler{}
	cfg := text.DefaultConfig()
	cfg.ScheduleNextTick = sched.Schedule
	cfg.StreamingOpen = true
	c, err := text.NewComponent(texttest.NopEditor(), ws, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	fpath := filepath.Join(dir, "slow.txt")
	writeNLines(t, fpath, 5)
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	h, err := c.OpenFileTab(fileURI, false)
	require.NoError(t, err)
	require.NotNil(t, h)

	// The load worker is (or will shortly be) parked inside the
	// blocked OpenFile; the event loop (this goroutine) is free.
	<-ws.entered
	ed, err := c.Editor(fileURI)
	require.NoError(t, err,
		"the placeholder tab must expose a text.Handler while the open is blocked")
	require.NotNil(t, ed)

	close(ws.release)
	c.WaitStreamingLoads()
	sched.drain()

	ed, err = c.Editor(fileURI)
	require.NoError(t, err)
	assert.Positive(t, ed.CellView().Rows(),
		"the swapped-in editor must expose the loaded file content")
}

// currentGoID returns a unique-per-goroutine integer derived from
// runtime.Stack. Tests use it only for equality comparisons against
// other identifiers captured from the same process.
func currentGoID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	s := string(buf[:n])
	const prefix = "goroutine "
	s = strings.TrimPrefix(s, prefix)
	i := strings.IndexByte(s, ' ')
	if i < 0 {
		return 0
	}
	var id uint64
	for _, ch := range s[:i] {
		if ch < '0' || ch > '9' {
			return 0
		}
		id = id*10 + uint64(ch-'0')
	}
	return id
}
