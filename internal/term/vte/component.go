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

package vte

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"go.uber.org/multierr"
	"mvdan.cc/sh/v3/shell"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte/vteparser"
	"unstable.build/rune/internal/text"
)

// Component implements a vte terminal emulator tui.Component.
type Component struct {
	mu        sync.Mutex
	terminal  schemeapi.Terminal
	executor  schemeapi.Executor
	clipboard clipboard.Register
	cfg       Config
	pty       workspaceapi.Pty
	watcher   workspaceapi.ProcessWatcher
	tm        browser.TabManager
	scroll    component.Scroll
	ctx       context.Context
	cancelCtx func()
	uri       workspaceapi.URI
	remote    remote
	// closed is set by Close so the async expandAndStart goroutine
	// can bail before handing the pty FDs to os/exec. It is atomic
	// because Close runs on the host event loop while the
	// expandAndStart goroutine reads it before acquiring mu and
	// invoking executor.StartCommand.
	closed atomic.Bool
	// spawnWG tracks the async expandAndStart goroutine that
	// Init spawns when CommandExpander is set. Tests can call
	// WaitSpawn to block until the spawn (and any
	// reportSpawnError watcher hand-off) has completed.
	spawnWG sync.WaitGroup
	// spawnErrored reports whether the async expandAndStart goroutine
	// reported a spawn failure via reportSpawnError. Tests use this
	// to know whether to also wait for the watcher receiver to
	// process the resulting error; long-running successful spawns
	// would otherwise block waiting for a process exit that may be
	// minutes or hours away.
	spawnErrored atomic.Bool
	pid          atomic.Int64
	// slaveClosed makes the parent-side slave close idempotent:
	// startCommand closes it after a successful spawn and Close
	// closes it on teardown paths where no spawn succeeded.
	slaveClosed atomic.Bool

	width, height     int
	parserHandler     *parserHandler
	waitParserHandler *waitParserHandler
	parser            vteparser.Parser
	complete          bool
	selectionAttr     term.Attributes
	defAttr           term.Attributes

	// version increments after each batch of pty output the parser
	// applies, so it changes whenever the rendered grid may have. It
	// lets consumers cheaply detect a changed grid without diffing
	// cells; it is not a lock — Snapshot/Draw still take mu. It starts
	// at 1 so the zero value reads as "never observed".
	version atomic.Uint64
}

// NOTE: this is an integrator implementation, it shouldn't really do much other
// than creating a pty and initializing the vte parser and the parser handler.

// NewComponent allocates storage for a new Component and initializes it.
func NewComponent(
	t schemeapi.Terminal, e schemeapi.Executor,
	tm browser.TabManager, cfg Config,
) (*Component, error) {
	ret := new(Component)
	err := ret.Init(t, e, tm, cfg)
	return ret, err
}

// Init initializes it with the given dependencies and options.
func (t *Component) Init(
	term schemeapi.Terminal, e schemeapi.Executor,
	tm browser.TabManager, cfg Config,
) error {
	t.clipboard = cfg.Clipboard
	t.watcher = cfg.Watcher
	t.tm = tm
	t.defAttr = cfg.Attributes
	t.selectionAttr = cfg.SelectionAttributes
	t.terminal = term
	t.executor = e
	t.cfg = cfg
	// Start at 1 so the zero value (0) means "never observed" for
	// consumers comparing Version across calls.
	t.version.Store(1)

	t.ctx, t.cancelCtx = context.WithCancel(context.Background())
	err := t.createPty(cfg.CommandAndArgs)
	if err != nil {
		return err
	}

	if cfg.ScheduleNextTick == nil || cfg.RingBell == nil {
		panic("nil schedule/bell function(s)")
	}

	t.parserHandler = newParserHandler(
		&t.mu, t.pty, tm, t.clipboard, cfg.scheduleBell, t.uri,
		cfg.NeedsAttentionAttributes, cfg.DynamicTabName, cfg.MaxLines, cfg.MinWidth)

	// start with pty slave file name as title
	var h vteparser.Handler = t.parserHandler
	if log.IsLevelEnabled(log.TraceLevel) {
		h = vteparser.HandlerWithLogging("vte.parserHandler", h)
	}
	t.scroll.InitPerformance(&t.parserHandler.sync.primBuf.Cells)
	t.scroll.InvertOffset = true
	t.scroll.SetTabspaces(1)

	t.remote = ptyWriterRemote(t, cfg.ScheduleNextTick)
	t.waitParserHandler = newWaitParserHandler(t.ctx, h)
	h = t.waitParserHandler
	t.waitParserHandler.useTrigger(t.triggerBell)

	t.parser.Init(h, new(vteparser.StdTimeout))
	t.SetDefaultAttributes(t.defAttr)
	// StartCommand holds mu during the descriptor hand-off, so launch only
	// after synchronous initialization no longer needs that lock.
	if cfg.CommandExpander != nil {
		t.spawnWG.Add(1)
		go debug.CapturePanicReport(func() {
			defer t.spawnWG.Done()
			t.expandAndStart(cfg.CommandAndArgs)
		})
	}
	return err
}

func (t *Component) triggerBell() {
	err := t.remote.triggerBell()
	if err != nil {
		t.log(log.WarnLevel, "trigger bell: %v", err)
	}
}

type TabExiter interface {
	OnTabExit(uri workspaceapi.URI) bool
}

func (t *Component) Run(publisher browser.EventPublisher) error {
	err := t.run(publisher)
	if ex, ok := t.tm.(TabExiter); ok {
		uri := t.uri
		if !t.cfg.ScheduleNextTick(func() { ex.OnTabExit(uri) }) {
			ex.OnTabExit(uri)
		}
	}
	return err
}

// Title returns the Title of this Component.
func (t *Component) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.parserHandler.title
}

// URI returns the raw URI of this terminal emulator.
func (t *Component) URI() workspaceapi.URI {
	return t.uri
}

// WriteToPty writes the given data to the underlying pty master.
func (t *Component) WriteToPty(data []byte) error {
	_, err := t.pty.Master.Write(data)
	if err != nil {
		return err
	}
	t.log(log.TraceLevel, "written %d byte(s) to the pty", len(data))
	return nil
}

// Resize resizes this component and returns an error if
// the call to resize the underlying pty failed.
func (t *Component) Resize(width, height int) error {
	if t.pty.Master == nil {
		return fmt.Errorf("terminal is not running")
	}
	// Close released the master, but the window manager keeps
	// resizing a closed handler until its tab is removed. The
	// descriptor number is reusable by then, so the ioctl either
	// fails with EBADF or lands on an unrelated file.
	if t.closed.Load() {
		return nil
	}

	t.mu.Lock()
	sameSize := t.width == width && t.height == height
	t.mu.Unlock()
	if sameSize {
		// some programs will not re-print if width and height
		// are the same, but resizing buffers does clear all the content
		// so we would be left with an empty screen buffer.
		return nil
	}

	err := t.terminal.SetPtySize(t.pty, width, height)
	if err != nil {
		return err
	}

	t.mu.Lock()
	t.width = width
	t.height = height
	t.parserHandler.resizeLocked(width, height)
	t.scroll.Resize(width, height)
	// Widening unwraps history into fewer rows, which can leave a
	// scrolled-up offset past the new maximum and blank the view; snap it
	// back to the top of history instead.
	t.scroll.ClampOffset()
	t.mu.Unlock()

	return nil
}

// ModeBracketedPaste returns whether bracketed paste mode is set.
func (t *Component) ModeBracketedPaste() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parserHandler.modeBracketedPaste
}

// MouseModeReportMouseClicks returns whether PrivateMode 1000 (MouseModeVT200) is set.
func (t *Component) MouseModeReportMouseClicks() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parserHandler.modeReportMouseClicks
}

// MouseModeReportCellMouseMotion returns whether PrivateMode 1002 (MouseModeButtonEvent) is set.
func (t *Component) MouseModeReportCellMouseMotion() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parserHandler.modeReportCellMouseMotion
}

// MouseModeReportAllMouseMotion returns whether PrivateMode 1003 (MouseModeAnyEvent) is set.
func (t *Component) MouseModeReportAllMouseMotion() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parserHandler.modeReportAllMouseMotion
}

// MouseModeUtf8Mouse returns whether PrivateMode 1005 (MouseExtUTF) is set.
func (t *Component) MouseModeUtf8Mouse() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parserHandler.modeUtf8Mouse
}

// MouseModeSgrMouse returns whether PrivateMode 1006 (MouseExtSGR) is set.
func (t *Component) MouseModeSgrMouse() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parserHandler.modeSgrMouse
}

// CursorVisible returns whether the cursor should be rendered or not.
func (t *Component) CursorVisible() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return !t.parserHandler.cursorHidden &&
		t.scroll.Offset().Y == 0 &&
		t.parserHandler.modeShowCursor &&
		t.parserHandler.sync.buf.CursorAtScreen().Y < t.height
}

// CursorAtScreen returns the current coordinates of the cursor,
// relative to the screen.
func (t *Component) CursorAtScreen() term.Coordinates {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.scroll.Offset().Y != 0 {
		return term.Coordinates{}
	}
	return t.parserHandler.sync.buf.CursorAtScreen()
}

// CursorAtScroll returns the current coordinates of the cursor,
// relative to the underlying scroll.
func (t *Component) CursorAtScroll() term.Coordinates {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.cursorAtScroll()
}

// CursorStyle returns the term.CursorStyle that should be rendered
// with this Component, if IsCursorVisible returns true.
func (t *Component) CursorStyle() term.CursorStyle {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.parserHandler.modeBlinkingCursor {
		return t.parserHandler.cursorStyle
	}

	switch t.parserHandler.cursorStyle {
	case term.CursorStyleSteadyUnderline:
		return term.CursorStyleBlinkingUnderline
	case term.CursorStyleSteadyBar:
		return term.CursorStyleBlinkingBar
	default:
		return term.CursorStyleBlinkingBlock
	}
}

// Height returns the height of the underlying terminal buffer in lines.
func (t *Component) Height() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.parserHandler.sync.buf.Rows()
}

// MaxWidth returns the maximum width of the underlying terminal buffer in columns.
func (t *Component) MaxWidth() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	// NOTE: using a cell.Buffer's MaxColumns is not exact
	// when there are multi-width characters. As long as
	// MaxWidth is used for hinting, it should be ok.
	if t.parserHandler.useAlt {
		return t.parserHandler.sync.altBuf.MaxColumns()
	}
	return t.parserHandler.sync.primBuf.MaxColumns()
}

// ScrollDown scrolls down content of this terminal emulator.
func (t *Component) ScrollDown(count int) (ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.parserHandler.useAlt {
		return
	}
	offset := t.scroll.Offset().Y
	// do not rely on primary buffer max offset, as it uses cursor
	// or scroll max offset, as it doesn't allow for rows-1 full scroll,
	// only rows-height-1 scroll.
	maxOffset := t.scroll.MaxOffset().Y + t.height - 1
	target := min(maxOffset, offset+count)

	for i := 0; i < target-offset && t.scroll.SeekDown(); i++ {
		ok = true
	}
	return
}

// ScrollUp scrolls up the content of this terminal emulator.
func (t *Component) ScrollUp(count int) (ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.parserHandler.useAlt {
		return
	}
	for i := 0; i < count && t.scroll.SeekUp(); i++ {
		ok = true
	}
	return
}

// ScrollOffset returns the current vertical scroll offset.
func (t *Component) ScrollOffset() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.parserHandler.useAlt {
		return 0
	}

	return t.scroll.MaxOffset().Y - t.scroll.Offset().Y
}

// scrollY returns the raw vertical scroll offset that Select, SelectEnd,
// SelectWordAt and SelectLine subtract when translating window
// coordinates into buffer coordinates. The alt buffer has no
// scrollback, so it reports 0 to match those methods.
func (t *Component) scrollY() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.parserHandler.useAlt {
		return 0
	}

	return t.scroll.Offset().Y
}

// MaxScrollOffset returns the current vertical scroll offset.
func (t *Component) MaxScrollOffset() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.parserHandler.useAlt {
		return 0
	}

	return t.scroll.MaxOffset().Y
}

// ScrollBottom scrolls down the content of this terminal emulator to the bottom.
func (t *Component) ScrollBottom() (ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.parserHandler.useAlt {
		return
	}

	return t.scroll.SeekEndFile()
}

// SetDefaultAttributes updates the default attributes of this terminal emulator.
func (t *Component) SetDefaultAttributes(attrs term.Attributes) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.parserHandler.sync.primBuf.SetDefaultAttributes(attrs)
	t.parserHandler.sync.altBuf.SetDefaultAttributes(attrs)
	t.scroll.Attributes = attrs
}

// IsApplicationCursorKeysMode returns whether cursor keys mode is enabled.
// https://vt100.net/docs/vt510-rm/chapter2.html#S2.8.11
func (t *Component) IsApplicationCursorKeysMode() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.parserHandler.modeCursorKeys
}

// IsAltBuffer returns true if underlying buffer utilizes is the alternate buffer.
func (t *Component) IsAltBuffer() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.parserHandler.useAlt
}

// IsNewLineMode returns whether new line mode is enabled.
// https://vt100.net/docs/vt510-rm/chapter2.html#S2.5.13
func (t *Component) IsNewLineMode() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.parserHandler.modeLineFeedNewLine
}

// Draw satisfies tui.Component.
func (t *Component) Draw(w term.Writer) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.drawLocked(w)
}

// drawLocked paints the active buffer and selection to w. The caller
// must hold t.mu.
func (t *Component) drawLocked(w term.Writer) {
	if t.parserHandler.useAlt {
		t.parserHandler.sync.altBuf.Draw(w)
	} else if t.scroll.Offset().Y == 0 {
		t.parserHandler.sync.primBuf.Draw(w)
	} else {
		t.scroll.Draw(w)
	}

	t.drawSelection(w)
}

// IsComplete returnes whether this terminal has stopped processing
// data from the pty file.
func (t *Component) IsComplete() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.complete
}

// Unselect clears this Component's selection.
func (t *Component) Unselect() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.parserHandler.useAlt {
		t.parserHandler.sync.altBuf.Unselect()
	} else {
		t.parserHandler.sync.primBuf.Unselect()
	}
}

// Select anchors the current cursor position as the start and end of a text selection.
func (t *Component) Select(pos term.Coordinates) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.parserHandler.useAlt {
		t.parserHandler.sync.altBuf.Select(pos)
	} else if t.scroll.Offset().Y == 0 {
		t.parserHandler.sync.primBuf.Select(pos)
	} else {
		pos.Y -= t.scroll.Offset().Y
		t.parserHandler.sync.primBuf.Select(pos)
	}
}

// SelectEnd anchors the current cursor position as the end of a text selection.
func (t *Component) SelectEnd(pos term.Coordinates) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.parserHandler.useAlt {
		t.parserHandler.sync.altBuf.SelectEnd(pos)
	} else if t.scroll.Offset().Y == 0 {
		t.parserHandler.sync.primBuf.SelectEnd(pos)
	} else {
		pos.Y -= t.scroll.Offset().Y
		t.parserHandler.sync.primBuf.SelectEnd(pos)
	}
}

// SelectWordAt selects the word under the current cursor position.
func (t *Component) SelectWordAt(pos term.Coordinates) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.parserHandler.useAlt {
		t.parserHandler.sync.altBuf.SelectWordAt(pos)
	} else if t.scroll.Offset().Y == 0 {
		t.parserHandler.sync.primBuf.SelectWordAt(pos)
	} else {
		pos.Y -= t.scroll.Offset().Y
		t.parserHandler.sync.primBuf.SelectWordAt(pos)
	}
}

// SelectLine anchors the current cursor position as the start and end line of
// the text selection.
func (t *Component) SelectLine(pos term.Coordinates) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.parserHandler.useAlt {
		t.parserHandler.sync.altBuf.SelectLine(pos)
	} else if t.scroll.Offset().Y == 0 {
		t.parserHandler.sync.primBuf.SelectLine(pos)
	} else {
		pos.Y -= t.scroll.Offset().Y
		t.parserHandler.sync.primBuf.SelectLine(pos)
	}
}

// Selection returns the current selection or false if no
// text is currently selected.
func (t *Component) Selection() (data string, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var cells [][]term.Cell
	cells, ok = t.selection()
	if !ok {
		return
	}
	data = cell.RowsToString(cells)
	data = strings.ReplaceAll(data, "\x00", "")
	return data, ok
}

// OnFocusChange allows clients to report whether this vte.Component is on focus or not.
func (t *Component) OnFocusChange(inFocus bool) error {
	t.mu.Lock()
	cmd, ok := t.parserHandler.onFocusChange(inFocus)
	t.mu.Unlock()
	if !ok {
		return nil
	}
	err := t.WriteToPty(fmt.Appendf(nil, "\x1b[%s", cmd))
	if err != nil {
		return fmt.Errorf("write to pty: %w", err)
	}
	return nil
}

// PrimaryScroll returns the primary buffer's underlying
// component.Scroll along with the sync.Locker that guards mutations to
// it. Callers must hold the returned Locker for the entire duration
// they read from or otherwise depend on the Scroll's underlying
// cell.Buffer; the VTE parser goroutine mutates that buffer under the
// same Locker. For one-shot reads of the rendered grid, prefer
// Component.Snapshot which locks and clones for you.
func (t *Component) PrimaryScroll() (*component.Scroll, sync.Locker) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parserHandler.sync.primBuf.Scroll(), &t.mu
}

// AlternateScroll mirrors PrimaryScroll for the alternate buffer. The
// same locking contract applies: hold the returned Locker for as long
// as the Scroll (or its underlying cell.Buffer) is in use. For
// one-shot reads of the rendered grid, prefer Component.Snapshot which
// locks and clones for you.
func (t *Component) AlternateScroll() (*component.Scroll, sync.Locker) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parserHandler.sync.altBuf.Scroll(), &t.mu
}

// Snapshot returns a durable snapshot of the terminal's currently
// active rendered buffer together with the cursor position at the
// same instant. The clone is detached from the live VTE state, so
// callers can iterate it freely without holding any lock.
//
// Cells and cursor are read under one mutex acquire so the parser
// goroutine cannot advance the grid between the two reads. The parser
// applies pty bytes in many short critical sections, so a caller that
// reads cells and cursor through separate accessors can observe a
// cursor from a later parser state than the cells it just snapshotted
// — feeding that mismatched pair to anything that interprets the grid
// (e.g. vteprobe.Cursor.Infer) produces confidently-wrong results.
// Use Snapshot whenever both the grid and the cursor are needed.
//
// Exactly one of Snapshot.Primary or Snapshot.Alternate is populated,
// matching the buffer that was active at snapshot time. Snapshot does
// not attempt to persist the live pty process or the inactive buffer.
func (t *Component) Snapshot() (Snapshot, error) {
	return t.SnapshotInto(nil)
}

// Version returns the current grid revision. It increments whenever the
// parser applies pty output, so callers can compare it across calls to
// detect a changed grid without diffing cells.
func (t *Component) Version() uint64 {
	return t.version.Load()
}

// SnapshotInto behaves like Snapshot but copies the active buffer's
// cells into dst, reusing dst's row and per-row capacity instead of
// allocating a fresh grid on every call. Passing nil allocates a new
// grid, matching Snapshot exactly.
//
// The returned cells are backed by dst. The caller must not retain them
// across the next SnapshotInto(dst) call, and dst must not be shared
// across goroutines.
func (t *Component) SnapshotInto(dst [][]term.Cell) (Snapshot, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked(dst), nil
}

// snapshotLocked builds a Snapshot from the active buffer, reusing dst
// for the cell grid. The caller must hold t.mu.
func (t *Component) snapshotLocked(dst [][]term.Cell) Snapshot {
	snap := Snapshot{
		Schema:       terminalSnapshotVersion,
		Version:      t.version.Load(),
		Title:        t.parserHandler.title,
		Width:        t.width,
		Height:       t.height,
		ScrollOffset: t.scroll.Offset(),
	}
	cursor := t.parserHandler.sync.buf.CursorAtScreen()
	if t.parserHandler.useAlt {
		snap.Alternate = ScreenSnapshot{
			Cells:  t.parserHandler.sync.altBuf.Cells.CopyRows(dst),
			Cursor: cursor,
		}
	} else {
		snap.Primary = ScreenSnapshot{
			Cells:  t.parserHandler.sync.primBuf.Cells.CopyRows(dst),
			Cursor: cursor,
		}
	}
	return snap
}

// DrawSnapshot paints the active grid to w and, under the same single
// t.mu acquire, returns a Snapshot of the very cells and cursor it
// painted. Holding the lock across both the draw and the snapshot is
// what guarantees the returned grid is exactly what was written to w:
// the parser goroutine cannot advance the grid in between. Callers that
// overlay on top of the painted grid (e.g. exo location highlights,
// which translate file coordinates to screen rows via a snapshot) rely
// on this so the overlay can never land on a row the grid has since
// scrolled away from.
//
// dst is reused for the snapshot's cell grid exactly as in SnapshotInto;
// the returned cells are backed by dst and must not be retained across
// the next call or shared across goroutines.
func (t *Component) DrawSnapshot(w term.Writer, dst [][]term.Cell) (Snapshot, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.drawLocked(w)
	return t.snapshotLocked(dst), nil
}

// RestoreFromSnapshot restores a previously captured terminal snapshot
// into this live terminal emulator's primary buffer. It restores rendered
// buffer contents, cursor position, title and scroll offset while leaving the
// currently running pty process intact.
func (t *Component) RestoreFromSnapshot(snapshot Snapshot) (cursor term.Coordinates, err error) {
	width, height := snapshot.Width, snapshot.Height
	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		height = 1
	}

	t.mu.Lock()
	t.parserHandler.sync.primBuf.Restore(
		snapshot.Primary.Cells, snapshot.Primary.Cursor, width, height)
	t.parserHandler.useAlt = false
	t.parserHandler.sync.buf = t.parserHandler.sync.primBuf
	if snapshot.Title != "" {
		t.parserHandler.title = snapshot.Title
	}
	t.scroll.InitPerformance(&t.parserHandler.sync.primBuf.Cells)
	t.scroll.InvertOffset = true
	t.scroll.SetTabspaces(1)
	t.width = 0
	t.height = 0
	t.mu.Unlock()

	// Drive the snapshot dimensions through the regular Resize path so
	// SetPtySize, parserHandler resize and scroll.Resize all run via
	// the same code as a normal window-manager Resize. Resize takes
	// t.mu, so it must run with the lock released.
	if err = t.Resize(width, height); err != nil {
		return cursor, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	offset := snapshot.ScrollOffset
	maxOffset := t.scroll.MaxOffset()
	offset.X = max(0, min(offset.X, maxOffset.X))
	offset.Y = max(0, min(offset.Y, maxOffset.Y))
	t.scroll.SetOffset(offset)
	cursor = t.parserHandler.sync.primBuf.CursorAtScroll()
	return cursor, nil
}

// UsedAlternateBuffer returns whether the alternate buffer was used
// at some point by the underlying program driving the vte.
func (t *Component) UsedAlternateBuffer() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parserHandler.usedAlternate()
}

// ClearPrimaryBuffer clears the primary buffer of this Component.
// If this Component is using the alternate buffer,
// this method returns false.
func (t *Component) ClearPrimaryBuffer() (ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.parserHandler.useAlt {
		return
	}

	t.parserHandler.sync.primBuf.Reset()
	_ = t.WriteToPty([]byte("\n"))
	return true
}

// Close tears down the pty and cancels the Component's context.
// Safe to call concurrently with the async expandAndStart goroutine:
// startCommand re-checks t.closed under mu before handing the pty
// FDs to executor.StartCommand, and the teardown below takes the same
// lock so it cannot release a descriptor the executor is still
// reading to build the child. The context cancellation above unwinds
// a spawn RPC parked on a dead transport, so the wait stays bounded.
func (t *Component) Close() (ret error) {
	t.closed.Store(true)
	t.cancelCtx()

	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.closeSlave(); err != nil {
		ret = multierr.Append(ret, err)
	}
	if err := t.pty.Master.Close(); err != nil {
		ret = multierr.Append(ret, err)
	}
	return ret
}

// closeSlave closes the parent's copy of the pty slave exactly once.
// After a successful spawn the child owns its own dup of the slave,
// and the parent must not hold another one: Linux only fails the
// master read with EIO once the last slave descriptor closes, so a
// retained parent copy would keep the run loop — and therefore the
// tab auto-close chain — alive forever after the child exits. macOS
// revokes the tty when the session leader exits, which is why the
// leak was invisible there.
func (t *Component) closeSlave() error {
	if !t.slaveClosed.CompareAndSwap(false, true) {
		return nil
	}
	return t.pty.Slave.Close()
}

// Pid returns the pid of the process started for this Component, or 0
// if no process has been started. A started process always has a
// nonzero pid, so 0 doubles as the "not started" sentinel.
func (t *Component) Pid() workspaceapi.Pid {
	return workspaceapi.Pid(t.pid.Load())
}

// WaitSpawn blocks until the async expandAndStart goroutine
// (created by Init when Config.CommandExpander is set) has
// finished, including any reportSpawnError watcher hand-off.
// Tests use this to settle integration timing where the pty/process
// outcome influences subsequent rendering.
func (t *Component) WaitSpawn() {
	t.spawnWG.Wait()
}

// SpawnErrored reports whether the async expandAndStart goroutine
// produced a spawn failure that was already pushed through the
// configured watcher. Tests use this in combination with WaitSpawn
// to decide whether to also wait for downstream watcher receivers
// to process the resulting error.
func (t *Component) SpawnErrored() bool {
	return t.spawnErrored.Load()
}

func (t *Component) log(level log.Level, line string, params ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "vte.Component").
		Logf(level, line, params...)
}

func (t *Component) createPty(cmdAndArgs []string) error {
	spawnCtx := t.ctx
	if t.cfg.SpawnTimeout > 0 {
		// Bound only the spawn RPCs. Deriving WithTimeout directly
		// would cancel the command's stream SpawnTimeout after a
		// successful start, so arm a watchdog that is disarmed once
		// the spawn has completed.
		var cancelSpawn context.CancelFunc
		spawnCtx, cancelSpawn = context.WithCancel(t.ctx)
		watchdog := time.AfterFunc(t.cfg.SpawnTimeout, cancelSpawn)
		defer watchdog.Stop()
	}

	pty, err := t.terminal.NewPty(spawnCtx)
	if err != nil {
		return fmt.Errorf("new pty: %w", t.spawnError(spawnCtx, err))
	}

	t.uri, err = workspaceapi.CurrentUserHostURI(pty.Slave.Name())
	if err != nil {
		return fmt.Errorf("pty URI: %v", err)
	}

	t.pty = pty
	if t.cfg.CommandExpander != nil {
		return nil
	}
	cmdAndArgsStr := strings.Join(cmdAndArgs, " ")
	if err := t.startCommand(spawnCtx, cmdAndArgsStr); err != nil {
		err = t.spawnError(spawnCtx, err)
		if closeErr := pty.Master.Close(); closeErr != nil {
			closeErr = fmt.Errorf("close pty: %w", closeErr)
			err = multierr.Append(err, closeErr)
		}
		return err
	}
	return nil
}

// spawnError distinguishes a spawn-watchdog timeout from an ordinary
// RPC failure or component shutdown, so the surfaced error points at
// the stalled workspace transport rather than a bare "context
// canceled".
func (t *Component) spawnError(spawnCtx context.Context, err error) error {
	if spawnCtx.Err() != nil && t.ctx.Err() == nil {
		return fmt.Errorf("%w: spawn timed out after %v", err, t.cfg.SpawnTimeout)
	}
	return err
}

// expandAndStart runs the configured CommandExpander and then
// starts the resolved command. Failures are written to the pty
// slave (so the user sees them in the floating window) and reported
// to the watcher.
func (t *Component) expandAndStart(cmdAndArgs []string) {
	line := strings.Join(cmdAndArgs, " ")
	resolved, err := t.cfg.CommandExpander.ExpandCommand(t.ctx, line)
	if err != nil {
		t.reportSpawnError(err)
		return
	}
	if err := t.startCommand(t.ctx, resolved); err != nil {
		t.reportSpawnError(err)
	}
}

// reportSpawnError writes err to the pty slave (so the floating
// window surfaces it) and notifies the configured watcher. The
// watcher send is synchronous so callers (always the
// expandAndStart goroutine) know the receiver has observed the
// terminal error before they exit; tests that wait for the
// goroutine via WaitGroup or context observe a fully-settled state.
func (t *Component) reportSpawnError(err error) {
	_, _ = t.pty.Slave.Write([]byte(err.Error()))
	t.spawnErrored.Store(true)
	if t.watcher != nil {
		select {
		case t.watcher.WatchProcess() <- err:
		case <-t.ctx.Done():
		}
	}
}

// startCommand performs the shell field-splitting on t.cmdAndArgs
// and dispatches the resolved command via the executor. It assumes
// t.pty has already been populated by createPty.
func (t *Component) startCommand(ctx context.Context, cmdAndArgsStr string) error {
	// NOTE: this uses os.Getenv, but it should use the workspace's
	// Getenv mechanism, which should be implemented at some point.
	cmdAndArgs, err := shell.Fields(cmdAndArgsStr, os.Getenv)
	if err != nil {
		return fmt.Errorf("expand shell arguments: %w", err)
	}
	t.log(log.DebugLevel, "creating pty with cmdAndArgs: %#v", cmdAndArgs)
	cmd := workspaceapi.Cmd{
		SysProcAttr: &syscall.SysProcAttr{
			Setsid:  true,
			Setctty: true,
		},
		Watcher: t.watcher,
	}
	cmd.Env = appendDefaultTerminalEnv(cmd.Env)
	// When the user hasn't configured a CommandAndArgs, leave
	// cmd.Path empty: that's the protocol contract for "use the
	// user's login shell on the executor's host",
	if len(cmdAndArgs) > 0 {
		cmd.Path = cmdAndArgs[0]
		if len(cmdAndArgs) > 1 {
			cmd.Args = cmdAndArgs[1:]
		}
	}

	cmd.Stdout = t.pty.Slave
	cmd.Stderr = t.pty.Slave
	cmd.Stdin = t.pty.Slave

	t.mu.Lock()
	if t.closed.Load() {
		t.mu.Unlock()
		return nil
	}
	pid, err := t.executor.StartCommand(ctx, cmd)
	if err == nil {
		t.pid.Store(int64(pid))
		if closeErr := t.closeSlave(); closeErr != nil {
			t.log(log.WarnLevel, "close parent pty slave: %v", closeErr)
		}
	}
	t.mu.Unlock()
	if err != nil {
		return fmt.Errorf("start command: %w", err)
	}
	return nil
}

// appendDefaultTerminalEnv pins TERM=xterm-256color and unsets
// COLORTERM for the program spawned in the vte. Existing TERM /
// COLORTERM entries in env are preserved so a caller can override.
//
// The COLORTERM clear is intentional: we want programs running in
// the vte to render with the 256-color ANSI palette so the editor
// theme remains the single source of truth for colors.
func appendDefaultTerminalEnv(env []string) []string {
	const defaultTerm = "xterm"
	hasTerm, hasColorTerm := false, false
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "TERM="):
			hasTerm = true
		case strings.HasPrefix(e, "COLORTERM="):
			hasColorTerm = true
		}
	}
	if !hasTerm {
		env = append(env, "TERM="+defaultTerm)
	}
	if !hasColorTerm {
		env = append(env, "COLORTERM=")
	}
	return env
}

func (t *Component) selection() (cells [][]term.Cell, ok bool) {
	if t.parserHandler.useAlt {
		cells, ok = t.parserHandler.sync.altBuf.Selection()
	} else {
		cells, ok = t.parserHandler.sync.primBuf.Selection()
	}
	return
}

func (t *Component) drawSelection(w term.Writer) {
	var from, to, offset term.Coordinates
	var mode text.SelectMode
	var ok bool
	if t.parserHandler.useAlt {
		mode, from, to, ok = t.parserHandler.sync.altBuf.SelectionCoordinatesAtScroll()
	} else {
		mode, from, to, ok = t.parserHandler.sync.primBuf.SelectionCoordinatesAtScroll()
		offset = term.Coordinates{Y: t.scroll.Buffer().Rows() - t.height - t.scroll.Offset().Y}
	}
	if !ok {
		return
	}
	for y := from.Y; y <= to.Y; y++ {
		xStart, xEnd := 0, t.parserHandler.sync.buf.Columns(y)
		if y == from.Y && mode == text.StandardSelection {
			xStart = from.X
		}
		if y == to.Y && mode == text.StandardSelection {
			xEnd = to.X
		}
		for x := xStart; x < xEnd; x++ {
			pos := term.Coordinates{X: x, Y: y}
			c := t.parserHandler.sync.buf.CellAt(pos)
			if c == nil {
				continue
			}
			posAtScreen := term.CoordinatesDiff(pos, offset)
			if posAtScreen.Y < 0 || posAtScreen.Y >= t.height ||
				posAtScreen.X < 0 || posAtScreen.X >= t.width {
				continue
			}
			cell := *c
			cell.SetAttributes(t.selectionAttr)
			w.SetCell(posAtScreen, cell)
		}
	}
}
func (t *Component) cursorAtScroll() term.Coordinates {
	return t.parserHandler.sync.buf.CursorAtScroll()
}

func (t *Component) scheduleBellCallback(callback func()) (ok bool) {
	return t.waitParserHandler.scheduleBellCallback(callback)
}

func (t *Component) systemCanDispatchBell(callback func(error)) {
	const systemCanDispatchBellTimeout = 3 * time.Second
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, systemCanDispatchBellTimeout)

	var called atomic.Bool
	t.waitParserHandler.scheduleBellCallback(func() {
		if called.CompareAndSwap(false, true) {
			cancel()
			t.cfg.ScheduleNextTick(func() {
				callback(nil)
			})
		}
	})

	go debug.CapturePanicReport(func() {
		defer cancel()
		<-ctx.Done()
		if called.CompareAndSwap(false, true) {
			t.cfg.ScheduleNextTick(func() {
				callback(fmt.Errorf("timeout waiting for bell: %w", ctx.Err()))
			})
		}
	})
}

func (t *Component) pendingCallbacks() int {
	return t.waitParserHandler.pendingCallbacks()
}

func (t *Component) run(publisher browser.EventPublisher) error {
	t.mu.Lock()
	complete := t.complete
	t.mu.Unlock()
	if complete {
		panic("called Run twice on vte.Component")
	}

	defer func() {
		t.mu.Lock()
		t.complete = true
		t.mu.Unlock()
	}()

	if g, ok := newPtyGather(t.ctx, t.pty.Master); ok {
		return t.runGather(g, publisher)
	}

	buf := make([]byte, os.Getpagesize())
	for {
		n, err := t.pty.Master.Read(buf[:])
		if err != nil {
			return err
		}
		t.parser.AdvanceBytes(buf[:n])
		t.version.Add(1)
		_ = publisher.PublishEvent(term.Event{Type: term.EventInterrupt})
	}
}

// runGather is the parse stage of the local-pty pipeline: it consumes
// gathered batches so the gather thread, not this loop, owns draining
// the kernel pty queue.
func (t *Component) runGather(g *ptyGather, publisher browser.EventPublisher) error {
	for batch := range g.ready {
		t.parser.AdvanceBytes(batch)
		g.release(batch)
		t.version.Add(1)
		_ = publisher.PublishEvent(term.Event{Type: term.EventInterrupt})
	}
	return g.err
}
