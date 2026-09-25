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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"syscall"

	multierr "github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/searchbox"
)

var _ tui.Handler = (*Handler)(nil)

// isNormalPtyExit reports whether the pty read loop ended because the
// child process exited rather than because something went wrong. Linux
// fails the master read with EIO once the last slave descriptor closes,
// where macOS reports EOF. For a remote workspace the error crosses the
// RPC boundary untyped, so the message is matched as well.
func isNormalPtyExit(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) ||
		errors.Is(err, syscall.EIO) {
		return true
	}
	return strings.Contains(err.Error(), syscall.EIO.Error())
}

// Handler is a terminal emulator that satisfies tui.Handler.
type Handler struct {
	comp          *Component
	publisher     browser.EventPublisher
	notifications browser.Notifications
	vi            viHandler
	ctx           context.Context
	cancelCtx     func()

	modalEnabled bool
	viMode       bool
	mouse        *mouse.Mouse
	mouseDriver  *mouseDriver

	// viEnabled tracks whether the vi handler was initialized: both modal
	// mode and the scrollback search render through it.
	viEnabled     bool
	searchBox     *searchbox.Box
	searchCtl     *searchController
	searchOverlay handler.Virtual[browserapi.Floating]

	bracketedPaste    bool
	bracketedPasteBuf bytes.Buffer

	closed        atomic.Bool // whether Close has been called
	exit          atomic.Bool // whether running shell/program has exited
	width, height int
}

// NewHandler allocates storage for a new Handler and initializes it.
func NewHandler(
	publisher browser.EventPublisher, n browser.Notifications,
	terminal schemeapi.Terminal, executor schemeapi.Executor,
	tm browser.TabManager, config Config,
) (*Handler, error) {
	ret := new(Handler)
	err := ret.Init(publisher, n, terminal, executor, tm, config)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// Init initializes this handler.
func (e *Handler) Init(
	publisher browser.EventPublisher, n browser.Notifications,
	termapi schemeapi.Terminal, executor schemeapi.Executor,
	tm browser.TabManager, config Config,
) error {
	// wrap clipboard to provide stitch newlines on paste
	// depending on the copy mode.
	config.Clipboard = stitchingClipboard{root: config.Clipboard}
	e.publisher = publisher
	e.notifications = n

	comp, err := NewComponent(termapi, executor, tm, config)
	if err != nil {
		return err
	}
	e.comp = comp

	e.modalEnabled = config.Modal
	if config.Modal || config.Search.Editor != nil {
		e.viEnabled = true
		e.vi.init(e.comp, config)
	}
	if config.Search.Editor != nil {
		e.searchCtl = &searchController{
			vi: &e.vi, cfg: config.Search, viModeActive: func() bool { return e.viMode },
		}
		e.searchBox = searchbox.New(e.searchCtl, config.Search.Config)
	}

	// set size hint before running firsrt program so output is correctly captured
	if config.WidthHint != 0 || config.HeightHint != 0 {
		err := e.comp.Resize(config.WidthHint, config.HeightHint)
		if err != nil {
			return fmt.Errorf("set initial pty size: %v", err)
		}
	}

	e.mouseDriver = &mouseDriver{t: e.comp, clipboard: config.Clipboard}
	e.mouse = mouse.New(e.mouseDriver)
	e.ctx, e.cancelCtx = context.WithCancel(context.Background())

	go debug.CapturePanicReport(func() {
		logErr := e.comp.Run(e.publisher)
		e.exit.Store(true)
		if err := e.publisher.PublishEvent(term.Event{Type: term.EventNone}); err != nil {
			e.log(log.ErrorLevel, "pty exit publish: %s", err)
		}
		if e.closed.Load() {
			e.log(log.DebugLevel, "terminal run: ok")
			return
		}
		if logErr != nil && !isNormalPtyExit(logErr) {
			e.log(log.ErrorLevel, "terminal run: %v", logErr)
			_, _ = e.notifications.Notify(browserapi.LevelError,
				"terminal run: %v", logErr)
		}
	})

	return nil
}

// SystemCanDispatchBell tests whether the underlying vte is able
// to dispatch a bell by calling the given callback with an error,
// if there was one. There's a fixed timeout of 3 seconds.
func (e *Handler) SystemCanDispatchBell(callback func(error)) {
	e.comp.systemCanDispatchBell(callback)
}

// Component returns the underlying vte.Component.
func (e *Handler) Component() *Component {
	return e.comp
}

// Snapshot returns a durable snapshot of the terminal's rendered
// buffers. It captures output/history, not the live pty process.
func (e *Handler) Snapshot() (Snapshot, error) {
	return e.comp.Snapshot()
}

// SnapshotInto behaves like Snapshot but copies the active buffer's
// cells into dst, reusing dst's capacity. See Component.SnapshotInto for
// the dst ownership contract.
func (e *Handler) SnapshotInto(dst [][]term.Cell) (Snapshot, error) {
	return e.comp.SnapshotInto(dst)
}

// Version returns the underlying Component's grid revision. See
// Component.Version.
func (e *Handler) Version() uint64 {
	return e.comp.Version()
}

// DrawSnapshot paints the active terminal grid to w and returns a
// Snapshot of the same grid taken under one lock. See
// Component.DrawSnapshot. Unlike Draw it does not render the modal (vi)
// overlay; it is intended for non-modal embeddings that overlay on the
// terminal grid itself.
func (e *Handler) DrawSnapshot(w term.Writer, dst [][]term.Cell) (Snapshot, error) {
	return e.comp.DrawSnapshot(w, dst)
}

// RestoreFromSnapshot restores a saved terminal snapshot into this
// live terminal emulator.
//
// Component.RestoreFromSnapshot mutates the primary buffer cells in
// place — the *cell.Buffer identity (and therefore every editor/scroll
// reference that viHandler captured at init) is preserved. The only
// state that becomes stale on the vi side is the cursor and the scroll
// offset, so we just refresh those rather than re-initialising vi.
func (e *Handler) RestoreFromSnapshot(snapshot Snapshot) error {
	cursor, err := e.comp.RestoreFromSnapshot(snapshot)
	if err != nil {
		return err
	}
	if e.viEnabled {
		e.vi.setCursorAtScroll(cursor)
	}
	return nil
}

// ClearPrimaryBuffer resets the primary buffer.
func (e *Handler) ClearPrimaryBuffer() (ok bool) {
	if e.comp.IsAltBuffer() {
		return
	}
	e.comp.Unselect()
	ok = e.comp.ClearPrimaryBuffer()
	if !ok {
		return
	}
	if e.viMode {
		// re-entering vi mode will reset cursor/offset for vi handler
		e.exitViMode()
	}
	return
}

// SetDefaultAttributes sets the background and foreground attributes
// of the underlying buffer.
func (e *Handler) SetDefaultAttributes(attr term.Attributes) {
	if e.viMode || e.searchViewing() {
		e.vi.setDefaultAttributes(attr)
	}
	e.comp.SetDefaultAttributes(attr)
}

// Resize satisfies tui.Component.
func (e *Handler) Resize(width, height int) {
	// avoid divisions by 0 in terminal impl
	if width == 0 || height == 0 {
		return
	}

	e.width, e.height = width, height
	// primary buffer resets the offset to max offset after every resize
	// so we need to, reset the cursor position
	var modalCursorPos term.Coordinates
	if e.viEnabled {
		if e.viMode || e.searchViewing() {
			modalCursorPos = e.vi.cursorAtScroll()
		}
		e.vi.Resize(width, height)
	}

	err := e.comp.Resize(width, height)
	if err != nil {
		e.log(log.ErrorLevel, "terminal set size: %s", err)
		// do not notify if already closed
		if !e.exit.Load() {
			_, _ = e.notifications.Notify(browserapi.LevelError,
				"terminal set size: %v", err)
		}
		return
	}

	if e.viMode || e.searchViewing() {
		e.vi.setCursorAtScroll(modalCursorPos)
	}
}

// Draw satisfies tui.Component.
func (e *Handler) Draw(w term.Writer) {
	if e.viMode || e.searchViewing() {
		e.vi.Draw(w)
		e.drawSearch(w)
		return
	}
	e.comp.Draw(w)
}

// Handle satisfies tui.Handler.
func (e *Handler) Handle(ev term.Event) (exit, handled bool) {
	exit = e.exit.Load()
	if exit {
		e.log(log.DebugLevel, "Handle: exit")
		return
	}
	if e.handleSearch(ev) {
		return false, true
	}
	if !e.viMode && e.searchViewing() && ev.Type == term.EventMouse {
		// selection and scrolling must follow what is drawn; while
		// actually in vi mode this is already covered below, since
		// every event reaches e.vi.Handle regardless of the box
		_, handled = e.vi.Handle(ev)
		return false, handled
	}
	if e.modalEnabled {
		if e.viMode {
			exit, handled := e.vi.Handle(ev)
			if exit {
				e.exitViMode()
			}
			return false, handled
		}

		if ev.Key == term.KeyEsc && ev.Mod == 0 && !e.comp.IsAltBuffer() {
			handled = true
			cursor := e.comp.CursorAtScroll()
			e.comp.systemCanDispatchBell(func(err error) {
				if err == nil {
					e.enterViMode(cursor)
					return
				}
				msg := "You pressed <esc>, which would enable modal (vi) mode, " +
					"but it cannot be enabled because the shell's " +
					"audible bell is currently unavailable. " +
					"Ensure that the shell's audible bell is configured and " +
					"working correctly. You can test it in your terminal with `printf '\\a'`."
				e.log(log.WarnLevel, "%s: %v", msg, err)
				if _, err := e.notifications.NotifyOnce(browserapi.LevelWarn, "%s", msg); err != nil {
					e.log(log.ErrorLevel, "notify: %v", err)
				}
			})
			return
		}
	}

	if ev.Mod&^term.ModCtrlShift != 0 {
		return
	}

	var raw []byte
	enc := e.comp.keyboard.encoding()
	if !e.bracketedPaste && ev.Type == term.EventKey && ev.Mod == 0 && ev.Ch != 0 &&
		!enc.reportsAllKeys() {
		raw = ev.Raw
	} else {
		handled, raw = e.handleInput(ev, enc)
	}
	if handled || len(raw) == 0 {
		return
	}

	if e.ctx.Err() != nil {
		exit = true
		return
	}

	_, err := e.comp.pty.Master.Write(raw)
	if err != nil {
		e.log(log.ErrorLevel, "write to pty: %s", err)
		e.notify(browserapi.LevelError, "write to pty: %v", err)
		return
	}
	handled = true
	if e.searchViewing() {
		// the shell is about to echo at the bottom of the scrollback, so
		// the reader is done with the results
		e.searchCtl.exitView()
	}

	// do not scroll to bottom in all cases or it could
	// interfere with interactive program that uses primary buffer
	if ev.Type == term.EventKey && ev.Ch == 'c' && ev.Mod == term.ModCtrl {
		e.comp.ScrollBottom()
		e.log(log.TraceLevel, "written cltr-c to pty: %q", raw)
	}
	return
}

// OnFocusChange allows clients to report whether this vte.Handler is on focus or not.
func (e *Handler) OnFocusChange(inFocus bool) {
	err := e.comp.OnFocusChange(inFocus)
	if err != nil {
		e.notify(browserapi.LevelError, "failed to report focus changed: %v", err)
	}
}

// Cursor satisfies tui.Handler.
func (e *Handler) Cursor() (pos term.Coordinates, style term.CursorStyle, show bool) {
	if e.exit.Load() {
		return
	}
	if e.searchOpen() {
		return e.searchOverlay.Cursor()
	}
	if e.viMode || e.searchViewing() {
		return e.vi.Cursor()
	}
	if !e.comp.CursorVisible() {
		return
	}

	show = true
	pos = e.comp.CursorAtScreen()
	style = e.comp.CursorStyle()
	if !e.comp.IsAltBuffer() && style == term.CursorStyleDefault {
		style = term.CursorStyleSteadyBar
	}

	return
}

// Selection satisfies tui.Handler.
func (e *Handler) Selection() (data string, ok bool) {
	if e.exit.Load() {
		return
	}
	if e.searchOpen() {
		return e.searchOverlay.Selection()
	}
	if e.searchViewing() {
		return e.searchCtl.Selection()
	}
	if e.viMode {
		return e.vi.Selection()
	}
	return e.comp.Selection()
}

// SeekUp satisfies component.Scrollable.
func (e *Handler) SeekUp() bool {
	if e.viMode || e.searchViewing() {
		return e.vi.SeekUp()
	}
	return e.comp.ScrollUp(1)
}

// SeekDown satisfies component.Scrollable.
func (e *Handler) SeekDown() bool {
	if e.viMode || e.searchViewing() {
		return e.vi.SeekDown()
	}
	return e.comp.ScrollDown(1)
}

// SeekOffset satisfies component.Scrollable.
func (e *Handler) SeekOffset() int {
	if e.viMode || e.searchViewing() {
		return e.vi.SeekOffset()
	}
	return e.comp.ScrollOffset()
}

// MaxSeekOffset satisfies component.Scrollable.
func (e *Handler) MaxSeekOffset() int {
	if e.viMode || e.searchViewing() {
		return e.vi.MaxSeekOffset()
	}
	return e.comp.MaxScrollOffset()
}

// Close closes this terminal emulator and all the resources
// associated with it.
func (e *Handler) Close() error {
	e.log(log.TraceLevel, "close called")

	if !e.closed.CompareAndSwap(false, true) {
		return nil
	}

	// undo circular dependency
	e.mouseDriver.clipboard = nil
	e.mouseDriver = nil

	e.cancelCtx()

	var ret error
	if e.searchBox != nil {
		if err := e.searchBox.Close(); err != nil {
			ret = multierr.Append(ret, err)
			e.log(log.ErrorLevel, "close search: %s", err)
		}
	}
	if err := e.comp.Close(); err != nil {
		ret = multierr.Append(ret, err)
		e.log(log.ErrorLevel, "terminal close: %s", err)
	}
	// we can't remove /dev/pts files so leave it up to the system
	return ret
}

func (e *Handler) handleInput(ev term.Event, enc keyEncoding) (handled bool, raw []byte) {
	if (ev.Type == term.EventKey || ev.Type == term.EventRaw) && e.bracketedPaste {
		e.bracketedPasteBuf.Write(ev.Raw)
		handled = true
		return
	}
	if isStart := ev.Type == term.EventPasteStart; isStart || ev.Type == term.EventPasteEnd {
		e.bracketedPaste = isStart
		programBracketedMode := e.comp.ModeBracketedPaste()
		e.log(log.DebugLevel, "handled bracketed paste start=%t,"+
			" programBracketedMode : %v", isStart, programBracketedMode)
		if isStart {
			if programBracketedMode {
				raw = append(raw, ev.Raw...)
			} else {
				// handle bracketed paste when we receive EventPasteEnd
				handled = true
			}
			return
		}

		defer e.bracketedPasteBuf.Reset()
		if programBracketedMode {
			raw = e.bracketedPasteBuf.Bytes()
			// remove `\x1b` (escape sequence) and `\x03` (ctrl-c) to ensure it's
			// impossible for the pasted text to control the shell's behavior in any way
			raw = bytes.ReplaceAll(raw, []byte("\x1b"), nil)
			raw = bytes.ReplaceAll(raw, []byte("\x03"), nil)
			// start of paste sequence was written upon term.EventPasteStart
			// so append term.EventPasteEnd or end of paste sequence.
			raw = append(raw, ev.Raw...)
		} else {
			raw = e.bracketedPasteBuf.Bytes()
			// replace line breaks with a single carriage, to reproduce
			// the enter key as much as possible.
			raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\r"))
			raw = bytes.ReplaceAll(raw, []byte("\n"), []byte("\r"))
		}
		if !e.comp.IsAltBuffer() {
			e.comp.ScrollBottom()
		}
		return
	}

	if ev.Type == term.EventMouse {
		if raw, tracking := e.mouseDriver.report(ev); tracking {
			e.log(log.TraceLevel, "input: mouse report: raw=%q", raw)
			return len(raw) == 0, raw
		}
		if raw := e.mouseDriver.alternateScroll(ev); raw != nil {
			return false, raw
		}
		e.mouseDriver.hookRawBytes = nil
		_, handled = e.mouse.Handle(ev)
		raw = e.mouseDriver.hookRawBytes
		// raw bytes should be sent directly only
		// if we didn't handle mouse event
		if len(raw) != 0 {
			handled = false
		}
		e.log(log.TraceLevel, "input: mouse: handled=%t, raw=%q", handled, raw)
		return
	}

	if ev.Ch == 'l' && ev.Mod == term.ModCtrl {
		e.mouseDriver.ClearSelection()
	} else if ev.Mod == 0 && ev.Key == term.KeyEsc {
		e.comp.Unselect()
	}

	if ev.Type == term.EventKey {
		if raw, ok := enc.encode(ev); ok {
			return false, raw
		}
	}

	// we cannot simply send raw bytes coming from termbox.
	// The running program sends escape sequences to e.terminal via stdout which
	// configure the program's I/O mode and so this might not might not necessarily
	// match termbox's configuration.
	raw, ok := mapKeyToEscapeSequence(e.comp, ev)
	if !ok {
		raw = ev.Raw
	}
	return
}

func (e *Handler) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithFields(log.Fields{
		logging.KeyClass: "vte.Handler",
	}).Logf(level, msg, args...)
}

func (e *Handler) notify(level browserapi.NotificationLevel, msg string, args ...any) {
	if _, err := e.notifications.Notify(level, msg, args...); err != nil {
		e.log(log.ErrorLevel, "notify: %v", err)
	}
}

func (e *Handler) enterViMode(cursor term.Coordinates) {
	e.viMode = true
	e.comp.Unselect()
	e.vi.enterViMode(cursor)
}

func (e *Handler) exitViMode() {
	e.viMode = false
}

// searchViewing reports whether the scrollback search results are on
// screen instead of the live terminal view.
func (e *Handler) searchViewing() bool {
	return e.searchCtl != nil && e.searchCtl.viewing
}

// searchOpen reports whether the search box is taking input.
func (e *Handler) searchOpen() bool {
	return e.searchBox != nil && e.searchBox.Active()
}

// searchOverlayMargin keeps the box clear of the top-right corner, on
// top of the box's own one cell of built-in side padding (its
// PaddingTop is 0 here, see terminalSearchConfig, so the frame sits
// flush with the top edge).
const (
	searchOverlayMarginTop   = 0
	searchOverlayMarginRight = 1
)

// layoutSearch parks the box on the top-right corner of the terminal,
// resizing it to the size it asks for as the query grows.
func (e *Handler) layoutSearch() {
	content := e.searchBox.Content()
	if content == nil {
		return
	}
	e.searchOverlay.C = content
	width, height := e.searchOverlay.C.Dimensions()
	width, height = min(width, e.width), min(height, e.height)
	e.searchOverlay.Resize(width, height)
	e.searchOverlay.Move(term.Coordinates{
		X: max(0, e.width-width-searchOverlayMarginRight),
		Y: searchOverlayMarginTop,
	})
}

func (e *Handler) drawSearch(w term.Writer) {
	if !e.searchOpen() {
		return
	}
	e.layoutSearch()
	// the box only tints the cells it draws on, so the results view
	// underneath has to be cleared for it to read as an overlay
	var blank term.Cell
	blank.SetAttributes(e.searchCtl.cfg.Attr)
	pos := e.searchOverlay.Position()
	for y := range e.searchOverlay.Height() {
		for x := range e.searchOverlay.Width() {
			w.SetCell(term.Coordinates{X: pos.X + x, Y: pos.Y + y}, blank)
		}
	}
	e.searchOverlay.Draw(w)
}

// handleSearch routes the search key to the find box and puts the live
// view back on <esc>. Keys that reach the shell dismiss the results too,
// but only once they are actually written to the pty: shortcuts the
// terminal drops, such as copying the selection, must leave the results
// and their selection alone.
func (e *Handler) handleSearch(ev term.Event) (handled bool) {
	if e.searchBox == nil {
		return false
	}
	if e.searchOpen() {
		return e.handleSearchInput(ev)
	}
	// full-screen programs paint their own view over a buffer that has no
	// scrollback to search
	if !e.comp.IsAltBuffer() && e.searchBox.HandleKey(ev) {
		// the box must be laid out before anything asks it for a cursor
		// or a selection, which can happen before the first draw
		e.layoutSearch()
		return true
	}
	if !e.searchViewing() || ev.Type != term.EventKey ||
		ev.Key != term.KeyEsc || ev.Mod != 0 {
		return false
	}
	e.searchCtl.exitView()
	return true
}

// handleSearchInput gives the open box the keyboard, while mouse events
// outside of it keep reaching the results so they can be selected. A key
// the box does not understand only stays claimed when it could otherwise
// reach the shell (a plain key or a ctrl/shift combo); alt/meta keys
// never reach the shell regardless (see the modifier gate below in
// Handle), so letting them fall through is what lets IDE shortcuts such
// as <meta-c> for clipboardcopy work while the box is open.
func (e *Handler) handleSearchInput(ev term.Event) (handled bool) {
	e.layoutSearch()
	if ev.Type == term.EventMouse && !e.searchOverlayContains(ev.MouseX, ev.MouseY) {
		return false
	}
	exit, handled := e.searchOverlay.Handle(ev)
	if exit {
		_ = e.searchBox.Close()
	}
	if handled || ev.Type != term.EventKey {
		return handled
	}
	return ev.Mod&^term.ModCtrlShift == 0
}

func (e *Handler) searchOverlayContains(x, y int) bool {
	pos := e.searchOverlay.Position()
	x, y = x-pos.X, y-pos.Y
	return x >= 0 && y >= 0 && x < e.searchOverlay.Width() && y < e.searchOverlay.Height()
}
