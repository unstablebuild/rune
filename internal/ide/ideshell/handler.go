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

package ideshell

import (
	"context"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/handler/search"
	tterm "unstable.build/rune/internal/term"
	"unstable.build/rune/internal/term/sh"
)

// Handler is the IDE companion shell handler. It wraps the SDK
// repl.Handler and overlays a fuzzy reverse-history search list at the
// bottom of the prompt area, triggered by <c-r>.
//
// The wrapper is needed because repl.Handler's input box and history
// slice are unexported in rune-go-sdk; this type owns both halves of
// the shell tab so it can read the persisted history and drive the
// inner inputbox by re-issuing key events.
type Handler struct {
	inner      *repl.Handler
	storage    storageapi.Service
	historyKey string
	maxHistory int

	list      *search.List
	width     int
	height    int
	searching bool
	// lastSearchH is the search overlay height most recently
	// applied via Resize. Tracking it lets Draw notice when the
	// match count changed and re-Resize the inner inputbox so it
	// re-anchors against the new vertical split.
	lastSearchH int
	// mode distinguishes the two overlay flavors so that
	// acceptSearch/cancelSearch know how to mutate (or not)
	// the underlying inputbox.
	mode searchMode
	// query mirrors what the user has typed since entering search mode.
	// It is what is forwarded to list.Buffer() to drive the fuzzy match.
	query []rune
	// compTyped tracks how many runes the user has typed (or
	// deleted) since opening the completion overlay. It is used
	// to decide when backspace has consumed the partial word and
	// the overlay should close.
	compTyped int
	// compMoved is set once the user manually navigates the
	// completion overlay (focus up/down). It suppresses the
	// auto-focus-top that finishCompletionStream applies when a
	// multi-candidate stream settles, so streaming candidates do not
	// yank focus away from the user's selection.
	compMoved bool
	// compCancel cancels the goroutine streaming completion
	// candidates into the overlay. It is nil when no completion
	// stream is in flight. Ending the overlay (accept/cancel/close)
	// must call it so the feeder goroutine exits.
	compCancel context.CancelFunc
	// compDone is closed by the feeder goroutine when it stops, so
	// the overlay can wait for it to exit before tearing down.
	compDone chan struct{}

	// shim is consulted on tab completion: it captures the head/
	// candidates returned from the underlying repl.CommandHandler
	// so we can render them in a search.List instead of cycling
	// inline through inputbox completion.
	shim *completionShim
	// prompt mirrors what the inner repl was configured with so we
	// can paint the same prefix in front of the search overlay's
	// input bar — this is what makes the overlay's bar look like a
	// continuation of the shell prompt.
	prompt string

	// The following retain the inputs originally passed to repl.New so
	// the inner repl can be rebuilt from scratch on <c-l>.
	scheduleNextTick func(func()) bool
	interrupter      term.Interrupter
	replOpts         []repl.Option
	// clearHook, when set, runs after the screen is cleared (<c-l>) so a
	// host can reset state it mirrors on the screen. See Config.ClearHook.
	clearHook func()

	// editor spawns the EditHandler that owns the shell input
	// line. It is the only input mode: there is no inputbox
	// fallback and no modal toggle.
	editor command.Editor
	// editHandler is the live editor bound to editBuf. It receives
	// every key first; events it leaves unhandled fall through to
	// the history-search / completion overlays.
	editHandler command.EditHandler
	// editBuf holds the current input line. On submit its contents
	// are dispatched to the inner repl and then cleared so the next
	// prompt starts empty.
	editBuf *cell.Buffer
	// cycling is set while the user is walking through command history
	// with up/down (or <c-j>/<c-k>). The inner repl's inputbox owns the
	// history cursor, so during a cycle the editor buffer is treated as
	// a mirror of the inner inputbox and is not re-seeded between
	// presses (which would reset the cursor to the newest entry). Any
	// other key the editor handles ends the cycle.
	cycling bool

	// grid captures the inner repl's drawn output so the mouse
	// delegate can extract selected text and overlay reverse-video
	// highlight. mouseDelegate holds the active selection and
	// mouse drives the press/drag/click state machine over it.
	grid          tterm.SelectionWriter
	mouseDelegate *mouseDelegate
	mouse         *mouse.Mouse
	// mouseDragInOutput is set while a left-button drag that began in
	// the output band is in flight. All drag and release events are
	// routed to the output mouse handler until MouseRelease, even if
	// the pointer crosses into the input band, so the selection does
	// not see a missed release.
	mouseDragInOutput bool
	// mouseDragInInput is the same latch for a drag that began in the
	// input band, which the editor owns.
	mouseDragInInput bool

	// sigHint is the signature-help label currently shown as a
	// transient hint above the input band; empty when no hint is
	// active. sigActive guards rendering so a stale empty label does
	// not reserve a row.
	sigHint   string
	sigActive bool
}

// New creates an IDE shell Handler wired with a CommandRegistry, sh
// layer, and the built-in help command. The returned Handler wraps an
// SDK repl.Handler and adds an interactive reverse-history search
// overlay. The shell input line is edited with editor, which is the
// only input mode: every key first goes to the spawned EditHandler
// and only events the editor leaves unhandled fall through to the
// history-search / completion overlays. editor is required. The
// returned registry can be used to register additional commands.
func New(
	scheduleNextTick func(func()) bool,
	interrupter term.Interrupter,
	editor command.Editor,
	cfg Config,
	opts ...repl.Option,
) (*Handler, *CommandRegistry) {
	if editor == nil {
		panic("ideshell.New requires an Editor")
	}
	r := NewRegistry()
	registerBaseCommands(r)
	prompt := cfg.Prompt
	if prompt == "" {
		prompt = defaultPrompt
	}
	if cfg.Storage != nil && cfg.HistoryDocumentID != "" {
		opts = append(opts,
			repl.WithStorage(cfg.HistoryDocumentID, cfg.Storage),
		)
	}
	if cfg.MaxHistory > 0 {
		opts = append(opts, repl.WithMaxHistory(cfg.MaxHistory))
	}
	opts = append(opts, repl.WithPrompt(prompt))
	var underlying repl.CommandHandler
	switch {
	case cfg.DisableShellInterpreter != nil:
		underlying = &registryFallback{registry: r, fallback: cfg.DisableShellInterpreter}
		// A pure language REPL owns the whole prompt: surface its own
		// command reference for top-level `help` instead of the
		// synthetic `go`/`help` registry list.
		if hp, ok := cfg.DisableShellInterpreter.(helpProvider); ok {
			r.SetHelpFallback(hp)
		}
	default:
		var shOpts []sh.Option
		if cfg.Executor != nil {
			shOpts = append(shOpts, sh.WithExecutor(cfg.Executor))
		}
		underlying = sh.New(r, cfg.Workspace, shOpts...)
	}
	shim := &completionShim{underlying: underlying}
	inner := repl.New(shim, scheduleNextTick, interrupter, opts...)
	list := search.NewList(search.ListConfig{
		Algo:            search.FuzzyMatch,
		Interrupter:     interrupter,
		SyncSearch:      true,
		BottomSearchBar: true,
	})
	h := &Handler{
		inner:      inner,
		storage:    cfg.Storage,
		historyKey: cfg.HistoryDocumentID,
		maxHistory: cfg.MaxHistory,
		list:       list,
		shim:       shim,
		prompt:     prompt,
		editor:     editor,

		scheduleNextTick: scheduleNextTick,
		interrupter:      interrupter,
		replOpts:         opts,
		clearHook:        cfg.ClearHook,
	}
	h.mouseDelegate = newMouseDelegate(&h.grid, func(ev term.Event) {
		_, _ = h.inner.Handle(ev)
	})
	h.mouse = mouse.New(h.mouseDelegate)
	h.editBuf = cell.NewBuffer()
	h.editHandler = editor.Edit(h.editBuf)
	// Seed a non-zero size so the editor's cursor math works for
	// input that arrives before the first Resize (e.g. headless
	// command submission in tests). Resize overrides this with the
	// real input-band geometry before the editor is ever drawn.
	h.editHandler.Resize(1, 1)
	if cfg.Modal && cfg.ModalStartInsert {
		h.editHandler.Handle(term.Event{Type: term.EventKey, Ch: 'i'})
	}
	return h, r
}

// Wait forwards to the underlying repl.Handler so callers (including
// ex_test) can drain in-flight commands.
func (h *Handler) Wait() {
	h.inner.Wait()
}

// WaitCompletion blocks until any in-flight completion feeder goroutine
// has finished pushing candidates and the list has settled. It is used
// by tests and by callers that need a deterministic view of the overlay
// after a tab press; production redraws are driven by the list's
// interrupter as candidates stream in.
func (h *Handler) WaitCompletion() {
	if h.compDone != nil {
		<-h.compDone
	}
	h.list.Wait()
}

// Submit aborts any in-flight command on the inner repl, clears the
// current input, types line into the inputbox and dispatches it by
// re-issuing <enter>. The leading <ctrl-c> mirrors what a real user
// would do to interrupt whatever the shell is currently running so a
// follow-up command can be pasted on top of a fresh prompt.
func (h *Handler) Submit(line string) {
	if h.searching {
		h.cancelSearch()
	}
	abort := term.Event{Type: term.EventKey, Ch: 'c', Mod: term.ModCtrl}
	_, _ = h.inner.Handle(abort)
	h.replaceInputText(line)
	enter := term.Event{Type: term.EventKey, Key: term.KeyEnter}
	_, _ = h.inner.Handle(enter)
}

// Close releases both the inner repl handler and the search list.
func (h *Handler) Close() error {
	h.stopCompletionStream()
	err := h.inner.Close()
	if h.list != nil {
		if cerr := h.list.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	return err
}

// clearScreen discards all accumulated command output by replacing the
// inner repl with a fresh one. repl.New reloads the persisted history,
// and the current input line lives in editBuf, so neither is lost.
func (h *Handler) clearScreen() {
	old := h.inner
	h.inner = repl.New(h.shim, h.scheduleNextTick, h.interrupter, h.replOpts...)
	if h.width > 0 && h.height > 0 {
		h.inner.Resize(h.width, h.height)
	}
	_ = old.Close()
	h.mouseDelegate.ClearSelection()
	if h.clearHook != nil {
		h.clearHook()
	}
}

// Resize satisfies tui.Component. While the search overlay is open
// we shrink the inner repl so its prior output gets pushed up
// rather than overdrawn by the candidate band. When closed we
// delegate fully.
func (h *Handler) Resize(width, height int) {
	h.width = width
	h.height = height
	h.grid.Resize(width, height)
	if !h.searching {
		h.inner.Resize(width, height)
		// Keep the editor at a usable width even when the host has
		// not been given real dimensions yet; a zero width breaks
		// the editor's cursor math (input would reverse).
		h.editHandler.Resize(max(1, width), max(1, h.editEditorH()))
		h.lastSearchH = 0
		return
	}
	searchH := h.searchHeight(height)
	listW := max(1, width-len(h.prompt))
	h.list.Resize(listW, searchH)
	innerSlice := searchH
	if h.mode == modeCompletion {
		// Completion mode keeps the inner's rl band visible
		// at the bottom of the screen, so the inner only
		// needs to give up the listRows that the candidate
		// band steals.
		innerSlice = max(0, searchH-h.list.InputHeight())
	}
	h.inner.Resize(width, max(0, height-innerSlice))
	h.editHandler.Resize(max(1, width), max(1, h.editEditorH()))
	h.lastSearchH = searchH
}

// Draw satisfies tui.Component.
func (h *Handler) Draw(w term.Writer) {
	if !h.searching {
		h.drawShell(w)
		return
	}
	h.drawSearch(w)
}

// drawShell renders the inner repl's output band into the capture
// grid so a mouse text selection can be overlaid with reverse-video
// highlight, then paints the editor over the bottom input band. The
// output band starts at row 0, so the mouse delegate's selection
// coordinates are already screen-relative.
func (h *Handler) drawShell(w term.Writer) {
	h.grid.Clear()
	h.grid.SetContext(w.Context())
	h.inner.Draw(&h.grid)
	_, outH := h.inner.LayoutHeights()
	h.mouseDelegate.outH = outH
	var sel *tterm.SelRange
	if h.mouseDelegate.sel.Active {
		s := h.mouseDelegate.sel
		sel = &s
	}
	h.grid.Dump(w, sel)
	h.drawSignatureHint(w)
	h.drawEdit(w)
}

// drawInnerOutput renders the inner repl's previous-command output
// into the top limit rows of w, clipping out the inner's own input
// band (the editor owns the input line). It renders the inner to a
// scratch buffer sized to the full screen so its output wrapping is
// unaffected, then copies only the leading output rows.
func (h *Handler) drawInnerOutput(w term.Writer, limit int) {
	if limit <= 0 || h.width <= 0 {
		return
	}
	buf := term.NewStringWriter(h.width, h.height)
	h.inner.Draw(buf)
	_, outH := h.inner.LayoutHeights()
	copyH := min(outH, limit)
	cells := buf.Cells()
	for y := range copyH {
		for x := range h.width {
			w.SetCell(term.Coordinates{X: x, Y: y}, cells[y*h.width+x])
		}
	}
}

// Cursor satisfies tui.Handler. Outside the history overlay the
// editor owns the cursor in the bottom input band. In history mode
// the cursor belongs to the search bar that we render on the bottom
// row.
func (h *Handler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	if h.searching && h.mode == modeHistory {
		return term.Coordinates{
			X: len(h.prompt) + len(h.query),
			Y: max(0, h.height-1),
		}, term.CursorStyleSteadyBar, true
	}
	if _, style, ok := h.editHandler.Cursor(); ok {
		// The host renders the prompt-prefixed buffer itself (see
		// drawEdit), so translate the editor's buffer-relative
		// cursor through the same hanging-indent wrap geometry and
		// offset it into the bottom input band.
		pos := h.editCursorVisual()
		pos.Y += h.editInnerH()
		return pos, style, true
	}
	return h.inner.Cursor()
}

// editCursorVisual converts the editor's buffer-relative cursor into
// the wrapped on-screen position within the editor band.
func (h *Handler) editCursorVisual() term.Coordinates {
	return h.editVisualPos(h.editHandler.CursorAtScroll())
}

// editVisualPos converts a buffer position into the wrapped on-screen
// position within the editor band. Only logical line 0 carries the
// prompt prefix (the hanging indent of editContent).
func (h *Handler) editVisualPos(pos term.Coordinates) term.Coordinates {
	width := max(1, h.width)
	lastLine := min(pos.Y, h.editBuf.Rows())
	row := 0
	for line := range lastLine {
		cols := h.editBuf.Columns(line)
		if line == 0 {
			cols += len(h.prompt)
		}
		row += cols/width + 1
	}
	col := pos.X
	if pos.Y == 0 {
		col += len(h.prompt)
	}
	return term.Coordinates{X: col % width, Y: row + col/width}
}

// editBufferPosAtVisual is the inverse of editVisualPos, clamping
// out-of-range positions to the nearest valid buffer cell.
func (h *Handler) editBufferPosAtVisual(pos term.Coordinates) term.Coordinates {
	rows := h.editBuf.Rows()
	if rows == 0 {
		return term.Coordinates{}
	}
	width := max(1, h.width)
	row := max(0, pos.Y-h.editInnerH())
	for line := range rows {
		cols := h.editBuf.Columns(line)
		lineRows := cols/width + 1
		if line == 0 {
			lineRows = (cols+len(h.prompt))/width + 1
		}
		if row < lineRows {
			col := row*width + pos.X
			if line == 0 {
				col -= len(h.prompt)
			}
			return term.Coordinates{X: min(max(col, 0), cols), Y: line}
		}
		row -= lineRows
	}
	last := rows - 1
	return term.Coordinates{X: h.editBuf.Columns(last), Y: last}
}

// Selection satisfies tui.Handler.
func (h *Handler) Selection() (string, bool) {
	if sel, ok := h.mouseDelegate.Selection(); ok {
		return sel, true
	}
	if sel, ok := h.editHandler.Selection(); ok && sel != "" {
		return sel, true
	}
	return h.inner.Selection()
}

// Handle satisfies tui.Handler. The editor owns the input line: every
// key first goes to the spawned EditHandler. <enter> commits the
// buffer to the inner repl for dispatch and then clears it. Events
// the editor leaves unhandled fall through to the host overlays:
// <c-r> opens the reverse-history search overlay and <tab> opens the
// completion overlay. While an overlay is open it consumes keys for
// navigation / acceptance / dismissal and otherwise mirrors typed
// runes into the editor buffer so the prompt and overlay stay in sync.
func (h *Handler) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type == term.EventMouse && !h.searching {
		return h.handleMouse(ev, h.editInnerH())
	}
	if ev.Type != term.EventKey {
		return h.editHandler.Handle(ev)
	}

	if h.searching {
		return h.handleSearch(ev)
	}

	// <c-c> is the shell interrupt key, so it must reach the inner
	// repl before modal editors can consume it as an edit command.
	if ev.Mod == term.ModCtrl && ev.Ch == 'c' {
		exit, handled = h.inner.Handle(ev)
		h.clearEdit()
		return exit, handled
	}

	// Plain <enter> submits the current input line.
	if ev.Mod == 0 && ev.Key == term.KeyEnter {
		h.submitEdit()
		return false, true
	}

	// <shift-enter> inserts a newline into the input line. The shell
	// owns it so it never ambiguously falls through to the editor.
	if ev.Mod == term.ModShift && ev.Key == term.KeyEnter {
		ev.Mod &^= term.ModShift
		h.editHandler.Handle(ev)
		return false, true
	}

	// <tab> always triggers completion and is never delegated to the
	// editor. A modal editor would otherwise consume it to insert
	// indentation, which is why completion previously stopped working.
	// This mirrors how a VTE owns <tab> and never forwards it to the
	// program it hosts.
	if ev.Key == term.KeyTab && ev.Mod == 0 {
		return h.handleTab(ev)
	}

	// <c-r> always opens reverse-history search and is never delegated
	// to the editor. vi's insert mode would otherwise consume it to
	// insert a register, which is why search stopped working there.
	// This mirrors how a VTE owns <c-r> for the shell it hosts.
	if ev.Mod == term.ModCtrl && ev.Ch == 'r' {
		h.openSearch()
		return false, true
	}

	// <c-l> clears the output band and re-anchors the prompt at the
	// bottom, mirroring how a terminal owns <c-l> for the shell it
	// hosts. The current input line and history are preserved.
	//
	// While a command is running we ignore it: clearing recreates the
	// inner repl, which would abort the in-flight command. A terminal
	// keeps the running program alive on <c-l>, so doing nothing is the
	// closer match until the command finishes.
	if ev.Mod == term.ModCtrl && ev.Ch == 'l' {
		if h.shim.running() {
			return false, true
		}
		h.clearScreen()
		return false, true
	}

	// up/down and the control-key aliases cycle through
	// command history, but only at the vertical edges of the input. A
	// recalled multi-line command must let the editor move the cursor
	// between its rows first; history is reached only when the cursor
	// is already on the top row (up) or bottom row (down). This mirrors
	// how shells navigate a multi-line buffer before stepping history.
	if up, ok := historyDir(ev); ok {
		if h.editAtHistoryEdge(up) {
			h.cycleHistory(up)
			return false, true
		}
		if _, handled := h.editHandler.Handle(ev); handled {
			h.cycling = false
			return false, true
		}
		return false, false
	}

	// Give the editor a crack at the remaining events. Only events it
	// leaves unhandled fall through to the host overlays.
	if _, handled := h.editHandler.Handle(ev); handled {
		// Any edit ends an in-progress history cycle; the next up
		// press should recall relative to the freshly edited line.
		h.cycling = false
		h.maybeSignatureHelp(ev)
		return false, true
	}
	return false, false
}

// historyDir maps a key event to a history-navigation direction. The
// second result is false for events that are not history-cycle keys.
// Up moves toward older entries; down moves toward newer ones.
func historyDir(ev term.Event) (up, ok bool) {
	switch {
	case ev.Mod == 0 && ev.Key == term.KeyArrowUp:
		return true, true
	case ev.Mod == 0 && ev.Key == term.KeyArrowDown:
		return false, true
	case ev.Mod == term.ModCtrl && (ev.Ch == 'k' || ev.Ch == 'p'):
		return true, true
	case ev.Mod == term.ModCtrl && (ev.Ch == 'j' || ev.Ch == 'n'):
		return false, true
	}
	return false, false
}

// editAtHistoryEdge reports whether the editor cursor sits at the
// vertical edge of the input buffer in the direction history would be
// stepped: the top row for an upward step, the bottom row for a
// downward one. Only then should up/down cycle history instead of
// moving the cursor within a (possibly multi-line) recalled command.
func (h *Handler) editAtHistoryEdge(up bool) bool {
	y := h.editHandler.CursorAtScroll().Y
	if up {
		return y <= 0
	}
	return y >= h.editBuf.Rows()-1
}

// cycleHistory walks the inner repl's command history in the given
// direction and mirrors the resulting line into the editor buffer. The
// inner inputbox owns the history cursor, so on the first press of a
// cycle the current editor line is seeded into it; subsequent presses
// feed the arrow key directly so the cursor keeps advancing.
func (h *Handler) cycleHistory(up bool) {
	if !h.cycling {
		h.replaceInputText(h.editBuf.String())
		h.cycling = true
	}
	key := term.KeyArrowDown
	if up {
		key = term.KeyArrowUp
	}
	_, _ = h.inner.Handle(term.Event{Type: term.EventKey, Key: key})
	h.setEditText(h.inner.Text())
}

// handleMouse routes a mouse event between the output band (text
// selection) and the input band (inputbox). outH is the height of the
// output band; rows [0, outH) belong to the output, [outH, height) to
// the inputbox.
//
// Drags are latched to the band they started in until MouseRelease,
// so a selection never misses its release when the pointer crosses
// bands.
func (h *Handler) handleMouse(ev term.Event, outH int) (exit, handled bool) {
	if h.mouseDragInOutput {
		if ev.Key == term.MouseRelease {
			h.mouseDragInOutput = false
		}
		return h.mouse.Handle(ev)
	}
	if h.mouseDragInInput {
		if ev.Key == term.MouseRelease {
			h.mouseDragInInput = false
		}
		return h.handleEditMouse(ev)
	}
	if ev.MouseY >= outH {
		h.mouseDelegate.ClearSelection()
		if ev.Key == term.MouseLeft {
			h.mouseDragInInput = true
		}
		return h.handleEditMouse(ev)
	}
	if ev.Key == term.MouseLeft {
		h.mouseDragInOutput = true
	}
	return h.mouse.Handle(ev)
}

// handleEditMouse translates a band screen position into the editor's
// window coordinates: invert the band wrap geometry to a buffer
// position, then subtract the editor's scroll offset
// (CursorAtScroll − Cursor).
func (h *Handler) handleEditMouse(ev term.Event) (exit, handled bool) {
	pos := h.editBufferPosAtVisual(
		term.Coordinates{X: ev.MouseX, Y: ev.MouseY})
	if win, _, ok := h.editHandler.Cursor(); ok {
		scroll := h.editHandler.CursorAtScroll()
		pos = term.CoordinatesDiff(pos, term.CoordinatesDiff(scroll, win))
	}
	ev.MouseX, ev.MouseY = pos.X, pos.Y
	return h.editHandler.Handle(ev)
}

// historyDoc mirrors the on-disk shape that repl.Handler persists via
// repl.WithStorage. It is duplicated here because the field is
// unexported in the SDK; we must read the same document to seed the
// reverse-history search overlay.
type historyDoc struct {
	Items   []string
	Version int64
}

// searchOverlayMaxRows bounds how many candidate rows the search list
// renders at once. The full overlay height is this plus a fixed
// allowance for the search bar and the match-count bar
// (searchOverlayChromeRows). This ceiling matters only when there is
// more vertical room than candidates; small windows use whatever is
// available.
const (
	searchOverlayMaxRows    = 10
	searchOverlayChromeRows = 2
)

// searchMode is the overlay flavor.
type searchMode int

const (
	// modeHistory is the reverse-history search overlay opened
	// with <c-r>. Accepting replaces the entire prompt.
	modeHistory searchMode = iota
	// modeCompletion is the tab-completion overlay opened
	// with <tab> when the underlying handler returns more than
	// one completion candidate. Accepting replaces only the
	// currently-completed word.
	modeCompletion
)

// drawSearch renders the search overlay shared by both reverse-
// history and tab-completion modes. Both modes shrink the inner
// repl so prior output is pushed up (instead of being overdrawn
// by the candidate band), draw the search list immediately below
// the inner repl, and put a prompt-style row at the very bottom
// of the screen. They differ only in what owns the bottom row:
//
//   - history mode: the inner repl's bottom row (which would only
//     hold an empty prompt) is blanked out and the search list's
//     own BottomSearchBar lands on the screen's bottom row instead,
//     with the shell prompt prefix painted in front of it. The
//     cursor lives on that row and the user types into the search
//     query.
//   - completion mode: the inner repl's rl band stays visible at
//     the bottom of the screen so the user continues editing the
//     partial word in the inputbox. To keep the inputbox anchored
//     to row height-1 (rather than row height-listRows-1, where
//     the inner would naturally place it after our shrink), we
//     render the inner to an offscreen buffer at height-listRows
//     and shift its rl band down by listRows when copying to the
//     real writer. The search list is then drawn just above the
//     relocated rl band, clipped to its candidate rows.
func (h *Handler) drawSearch(w term.Writer) {
	if h.mode == modeCompletion {
		h.drawCompletion(w)
		return
	}
	h.drawHistory(w)
}

// drawHistory implements the reverse-history overlay. See drawSearch.
func (h *Handler) drawHistory(w term.Writer) {
	searchH := h.searchHeight(h.height)
	listW := max(1, h.width-len(h.prompt))
	innerH := max(0, h.height-searchH)
	if searchH != h.lastSearchH {
		h.inner.Resize(h.width, innerH)
		h.list.Resize(listW, searchH)
		h.lastSearchH = searchH
	}
	innerW := &component.VirtualWriter{
		Writer: w,
		Width:  h.width,
		Height: innerH,
	}
	h.inner.Draw(innerW)
	if innerH > 0 {
		// Blank out the inner's bottom row so its inputbox prompt
		// (mirroring the input line while searching) doesn't
		// appear in addition to the search bar at the bottom of
		// the screen. Multi-row wrapped input above this row
		// stays visible.
		clearY := innerH - 1
		for x := range h.width {
			w.SetCell(term.Coordinates{X: x, Y: clearY},
				term.Cell{Ch: ' '})
		}
	}
	barY := h.height - 1
	for i, r := range h.prompt {
		w.SetCell(term.Coordinates{X: i, Y: barY},
			term.Cell{Ch: r})
	}
	overlayW := &component.VirtualWriter{
		Writer: w,
		Offset: term.Coordinates{X: len(h.prompt), Y: innerH},
		Width:  listW,
		Height: searchH,
	}
	h.list.Draw(overlayW)
}

// drawCompletion implements the tab-completion overlay. The editor
// input line stays anchored to the bottom band (showing the partial
// word being completed) and the candidate list is drawn just above
// it, with the inner repl's prior output above that.
func (h *Handler) drawCompletion(w term.Writer) {
	searchH := h.searchHeight(h.height)
	listW := max(1, h.width-len(h.prompt))
	listRows := max(0, searchH-h.list.InputHeight())
	editorH := h.editEditorH()
	innerH := max(0, h.height-listRows-editorH)
	if searchH != h.lastSearchH {
		h.inner.Resize(h.width, h.height)
		h.list.Resize(listW, searchH)
		h.lastSearchH = searchH
	}
	h.drawInnerOutput(w, innerH)
	overlayW := &component.VirtualWriter{
		Writer: w,
		Offset: term.Coordinates{X: len(h.prompt), Y: innerH},
		Width:  listW,
		Height: listRows,
	}
	h.list.Draw(overlayW)
	h.drawEdit(w)
}

// handleTab forwards <tab> to the inner repl.Handler so its inputbox
// calls our completion shim. If the shim captured more than one
// candidate, we open the completion overlay seeded with them.
// Otherwise we let the inner handler's response stand (zero or one
// candidate is best handled inline, exactly like the SDK default).
//
// The editor owns the visible input line, so the inner inputbox is
// seeded from the editor buffer before the tab is forwarded (so the
// completion prefix is computed against the real line) and cleared
// afterwards. When the inner resolves the completion inline (0 or 1
// candidate), the resulting text is synced back into the editor.
func (h *Handler) handleTab(ev term.Event) (exit, handled bool) {
	// A <tab> with the cursor immediately after an unclosed "(" asks
	// for signature help rather than completion: there is no partial
	// identifier to complete, so the user wants to see the call's
	// parameters.
	if h.cursorAfterOpenParen() {
		h.triggerSignatureHelp()
		return false, true
	}
	h.shim.reset()
	h.replaceInputText(h.editBuf.String())
	exit, handled = h.inner.Handle(ev)
	if exit {
		return exit, handled
	}
	captured, ok := h.shim.consume()
	if !ok {
		h.setEditText(h.inner.Text())
		h.clearInner()
		return false, true
	}
	h.clearInner()
	h.openCompletion(captured)
	return false, true
}

// maybeSignatureHelp reacts to a printable rune the editor just
// consumed: "(" requests a fresh hint for the call being opened, ")"
// dismisses any active hint, and any other key leaves the current hint
// in place so it can update as arguments are typed.
func (h *Handler) maybeSignatureHelp(ev term.Event) {
	if h.sigActive && h.editBuf.String() == "" {
		h.clearSignatureHint()
		return
	}
	if ev.Key != 0 || ev.Mod != 0 {
		return
	}
	switch ev.Ch {
	case '(':
		h.triggerSignatureHelp()
	case ')':
		h.clearSignatureHint()
	}
}

// cursorAfterOpenParen reports whether the rune immediately to the left
// of the editor cursor is "(", marking the cursor as sitting just inside
// an opening call where signature help applies.
func (h *Handler) cursorAfterOpenParen() bool {
	cur := h.editHandler.CursorAtScroll()
	if cur.X <= 0 {
		return false
	}
	c, ok := h.editBuf.Cell(term.Coordinates{X: cur.X - 1, Y: cur.Y})
	return ok && c.Ch == '('
}

// signatureLine returns the text of the editor row the cursor sits on up
// to the cursor, plus the cursor's rune column within it. Signature help
// only needs the prefix of the line up to the cursor; a call rarely
// spans editor rows, so the current row is enough.
func (h *Handler) signatureLine() (string, int) {
	cur := h.editHandler.CursorAtScroll()
	line := h.editBuf.String()
	rows := strings.Split(line, "\n")
	if cur.Y < 0 || cur.Y >= len(rows) {
		return "", 0
	}
	row := []rune(rows[cur.Y])
	col := min(cur.X, len(row))
	return string(row[:col]), col
}

// triggerSignatureHelp asks the underlying handler for the signature of
// the call being typed and shows the result as a transient hint. It runs
// synchronously on the event loop, mirroring tab completion (which also
// queries gopls inline): both share the session's single program-file
// overlay, so serializing them on the loop avoids racing that state.
func (h *Handler) triggerSignatureHelp() {
	line, col := h.signatureLine()
	label, ok := h.shim.signatureHelp(context.Background(), line, col)
	h.sigHint = label
	h.sigActive = ok && label != ""
}

// clearSignatureHint hides any active hint so a later draw treats the
// band as empty.
func (h *Handler) clearSignatureHint() {
	h.sigHint = ""
	h.sigActive = false
}

func (h *Handler) handleSearch(ev term.Event) (exit, handled bool) {
	if h.mode == modeCompletion {
		return h.handleCompletion(ev)
	}
	return h.handleHistory(ev)
}

func (h *Handler) handleHistory(ev term.Event) (exit, handled bool) {
	switch ev.Mod {
	case 0:
		switch ev.Key {
		case term.KeyEnter, term.KeyTab:
			h.acceptSearch()
			return false, true
		case term.KeyEsc:
			h.cancelSearch()
			return false, true
		case term.KeyArrowUp:
			h.list.FocusUp()
			h.compMoved = true
			return false, true
		case term.KeyArrowDown:
			h.list.FocusDown()
			h.compMoved = true
			return false, true
		case term.KeyBackspace:
			h.shrinkQuery()
			return false, true
		case term.KeySpace:
			h.appendQuery(' ')
			return false, true
		}
		if ev.Ch != 0 {
			h.appendQuery(ev.Ch)
			return false, true
		}
	case term.ModCtrl:
		switch ev.Ch {
		case 'r':
			h.list.FocusDown()
			return false, true
		case 'j', 'n':
			h.list.FocusDown()
			return false, true
		case 'k', 'p':
			h.list.FocusUp()
			return false, true
		case 'c', 'g':
			h.cancelSearch()
			return false, true
		}
	}
	// Swallow unhandled events while searching so they don't bypass the
	// overlay and reach the underlying inputbox.
	return false, true
}

// handleCompletion handles events while the tab-completion overlay
// is open. The inputbox stays visible and is the source of truth for
// the partial word being completed; we mirror it into the search
// list's filter buffer.
func (h *Handler) handleCompletion(ev term.Event) (exit, handled bool) {
	switch ev.Mod {
	case 0:
		switch ev.Key {
		case term.KeyEnter, term.KeyTab:
			h.acceptSearch()
			return false, true
		case term.KeyEsc:
			h.cancelSearch()
			return false, true
		case term.KeyArrowUp:
			h.list.FocusUp()
			h.compMoved = true
			return false, true
		case term.KeyArrowDown:
			h.list.FocusDown()
			h.compMoved = true
			return false, true
		case term.KeyBackspace:
			return h.completionBackspace()
		case term.KeySpace:
			// space ends the current argument: close the
			// overlay and append the space to the input line.
			h.cancelSearch()
			h.editBuf.WriteString(" ")
			h.editHandler.SetCursorAtScroll(
				term.Coordinates{X: h.editBuf.Columns(0)})
			return false, true
		}
		if ev.Ch != 0 {
			h.completionAppend(ev.Ch)
			return false, true
		}
	case term.ModCtrl:
		switch ev.Ch {
		case 'j', 'n':
			h.list.FocusDown()
			h.compMoved = true
			return false, true
		case 'k', 'p':
			h.list.FocusUp()
			h.compMoved = true
			return false, true
		case 'c', 'g':
			h.cancelSearch()
			return false, true
		}
	}
	return false, false
}

// completionAppend appends a printable rune to the editor buffer so
// the prompt visually grows, then mirrors the same character into the
// list's filter buffer so the candidate set narrows.
func (h *Handler) completionAppend(r rune) {
	h.editBuf.WriteString(string(r))
	h.editHandler.SetCursorAtScroll(term.Coordinates{X: h.editBuf.Columns(0)})
	h.compTyped++
	h.appendQuery(r)
}

// completionBackspace either shrinks the typed-after-tab portion of
// the partial word (removed from the editor buffer AND mirrored into
// the filter buffer), or closes the overlay when the user has back-
// spaced past everything they typed since opening it. The character
// is removed from the editor buffer in either case so the prompt
// keeps shrinking.
func (h *Handler) completionBackspace() (exit, handled bool) {
	h.editDeleteFromEnd(1)
	if h.compTyped <= 0 {
		// Backspaces have caught up to the original partial
		// word boundary; further deletions should affect the
		// underlying line, not the overlay.
		h.cancelSearch()
		return false, true
	}
	h.compTyped--
	h.shrinkQuery()
	return false, true
}

func (h *Handler) openSearch() {
	h.cycling = false
	h.list.DataReset()
	for _, item := range h.loadHistory() {
		h.list.PushSync([]byte(item))
	}
	seed := h.editBuf.String()
	// While the history overlay is open the inner inputbox mirrors
	// the input line so its wrapping renders the (possibly
	// multi-row) prompt above the candidate band; the editor band
	// itself is not drawn in history mode.
	h.replaceInputText(seed)
	h.query = append(h.query[:0], []rune(seed)...)
	h.list.Buffer().Replace(seed)
	h.list.FocusStart()
	h.searching = true
	h.mode = modeHistory
	h.Resize(h.width, h.height)
}

// openCompletion opens the tab-completion overlay and streams the
// captured candidate iterator into it off the event loop. The overlay
// shares the search.List rendering and navigation behavior with
// reverse-history search. Streaming keeps the prompt responsive for
// completers that start background I/O instead of buffering the whole
// candidate set first.
func (h *Handler) openCompletion(c capturedCompletion) {
	h.stopCompletionStream()
	h.list.DataReset()
	h.query = h.query[:0]
	h.list.Buffer().Replace("")
	h.list.FocusStart()
	h.searching = true
	h.mode = modeCompletion
	h.shim.lastPrefix = c.prefix
	h.compMoved = false
	h.Resize(h.width, h.height)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	h.compCancel = cancel
	h.compDone = done
	ch := h.list.Push(ctx)
	iter := c.iter
	go debug.CapturePanicReport(func() {
		count := h.streamCandidates(ctx, iter, ch)
		// Signal completion before scheduling resolution so a
		// synchronous scheduler (tests) that runs the callback inline
		// cannot deadlock stopCompletionStream waiting on done.
		close(done)
		// Schedule single/zero-match resolution on the event loop so
		// it serializes with key handling and overlay teardown.
		h.scheduleNextTick(func() {
			h.finishCompletionStream(done, count)
		})
	})
}

// streamCandidates pumps the completion iterator into the list's async
// push channel until the iterator is exhausted or the context is
// cancelled, returning the number of candidates sent. It owns closing
// both the channel and the iterator.
func (h *Handler) streamCandidates(
	ctx context.Context, iter iterator.Iterator[string], ch chan<- []byte,
) int {
	defer close(ch)
	defer func() { _ = iter.Close() }()
	count := 0
	for {
		v, ok := iter.Next(ctx)
		if !ok {
			return count
		}
		select {
		case ch <- []byte(v):
			count++
		case <-ctx.Done():
			return count
		}
	}
}

// finishCompletionStream runs on the event loop after the feeder
// goroutine exits. With exactly one candidate and no user query it
// auto-accepts (restoring the inline single-completion UX); with none
// it closes the overlay. The done guard ensures a stale stream (one
// the user already replaced or cancelled) cannot mutate the current
// overlay.
func (h *Handler) finishCompletionStream(done chan struct{}, count int) {
	if h.compDone != done || !h.searching || h.mode != modeCompletion {
		return
	}
	if len(h.query) != 0 {
		return
	}
	switch count {
	case 0:
		h.cancelSearch()
	case 1:
		h.acceptSearch()
	default:
		// Anchor focus on the best (first) candidate once the stream
		// settles, unless the user already navigated. Pushes append to
		// the back, so without this focus could rest on the last item.
		if !h.compMoved {
			h.list.FocusStart()
		}
	}
}

// stopCompletionStream cancels any in-flight completion feeder
// goroutine and waits for it to exit so it cannot push into a torn-down
// overlay or leak.
func (h *Handler) stopCompletionStream() {
	if h.compCancel == nil {
		return
	}
	h.compCancel()
	<-h.compDone
	h.compCancel = nil
	h.compDone = nil
}

func (h *Handler) cancelSearch() {
	h.stopCompletionStream()
	h.list.Cancel()
	h.list.Wait()
	h.searching = false
	h.query = h.query[:0]
	h.compTyped = 0
	h.list.Buffer().Replace("")
	h.clearInner()
	h.Resize(h.width, h.height)
}

func (h *Handler) acceptSearch() {
	h.stopCompletionStream()
	h.list.Cancel()
	h.list.Wait()
	if h.mode == modeCompletion && len(h.query) == 0 && !h.compMoved {
		h.list.FocusStart()
	}
	match, ok := h.list.Focus()
	mode := h.mode
	prefix := h.shim.lastPrefix
	typed := h.compTyped
	h.searching = false
	h.query = h.query[:0]
	h.compTyped = 0
	h.Resize(h.width, h.height)
	if mode == modeHistory {
		// The inner inputbox was only mirroring the input line for
		// the history overlay's wrapped rendering; the editor owns
		// the line again now.
		h.clearInner()
	}
	if !ok {
		return
	}
	text := string(match.Data())
	switch mode {
	case modeHistory:
		h.setEditText(text)
	case modeCompletion:
		// The editor buffer currently ends with the original
		// prefix plus everything the user typed after <tab>;
		// delete both, then append the chosen candidate.
		h.editDeleteFromEnd(len([]rune(prefix)) + typed)
		h.editBuf.WriteString(text)
		h.editHandler.SetCursorAtScroll(
			term.Coordinates{X: h.editBuf.Columns(0)})
	}
}

func (h *Handler) appendQuery(r rune) {
	h.query = append(h.query, r)
	h.list.Buffer().Replace(string(h.query))
}

func (h *Handler) shrinkQuery() {
	if len(h.query) == 0 {
		return
	}
	h.query = h.query[:len(h.query)-1]
	h.list.Buffer().Replace(string(h.query))
}

// replaceInputText drives the inner inputbox to contain text by first
// clearing the line (<c-u>) and then re-issuing each rune. The inner
// repl.Handler's inputbox state is private, so re-issuing key events
// is the supported way to mutate it from outside.
func (h *Handler) replaceInputText(text string) {
	clear := term.Event{Type: term.EventKey, Ch: 'u', Mod: term.ModCtrl}
	_, _ = h.inner.Handle(clear)
	for _, r := range text {
		ev := term.Event{Type: term.EventKey, Ch: r}
		_, _ = h.inner.Handle(ev)
	}
}

// clearInner empties the inner inputbox. The inner repl is only used
// transiently to compute completion prefixes; the editor owns the
// visible input line.
func (h *Handler) clearInner() {
	_, _ = h.inner.Handle(
		term.Event{Type: term.EventKey, Ch: 'u', Mod: term.ModCtrl})
}

// editDeleteFromEnd removes n cells from the end of the editor buffer
// and re-anchors the editor cursor to the new end of line.
func (h *Handler) editDeleteFromEnd(n int) {
	for range n {
		cols := h.editBuf.Columns(0)
		if cols <= 0 {
			break
		}
		h.editBuf.DeleteCell(term.Coordinates{X: cols - 1})
	}
	h.editHandler.SetCursorAtScroll(term.Coordinates{X: h.editBuf.Columns(0)})
}

// loadHistory reads the persisted shell history from storage. It
// returns the entries newest-first so that the search list focuses on
// the most recent commands when opened.
func (h *Handler) loadHistory() []string {
	if h.storage == nil || h.historyKey == "" {
		return nil
	}
	var doc historyDoc
	if err := h.storage.Get(context.Background(), h.historyKey, &doc); err != nil {
		return nil
	}
	items := doc.Items
	if h.maxHistory > 0 && len(items) > h.maxHistory {
		items = items[len(items)-h.maxHistory:]
	}
	out := make([]string, len(items))
	for i, it := range items {
		out[len(items)-1-i] = it
	}
	return out
}

func (h *Handler) searchHeight(total int) int {
	if total <= 1 {
		return 0
	}
	// Reserve at least one row for the inner inputbox so the
	// caller still sees the prompt and any partial input above the
	// overlay.
	maxOverlay := total - 1
	rows := h.list.MatchCount()
	if rows < 1 {
		// Always render at least one candidate row, even if the
		// list is empty: this keeps the overlay's "0/0" affordance
		// stable instead of collapsing the bottom block.
		rows = 1
	}
	if rows > searchOverlayMaxRows {
		rows = searchOverlayMaxRows
	}
	want := rows + searchOverlayChromeRows
	if want > maxOverlay {
		want = maxOverlay
	}
	return want
}

// submitEdit dispatches the editor's current buffer to the inner repl
// and then clears the buffer so the next prompt starts empty. The
// inner repl still owns command dispatch and history persistence, so
// the buffer text is seeded into its inputbox and submitted with a
// synthesized <enter>.
func (h *Handler) submitEdit() {
	line := h.editBuf.String()
	h.replaceInputText(line)
	enter := term.Event{Type: term.EventKey, Key: term.KeyEnter}
	_, _ = h.inner.Handle(enter)
	h.clearEdit()
}

// setEditText replaces the editor buffer with text and moves the
// editor cursor to the end of the last row. It is used by the overlay
// accept paths (history match / completion candidate) and history
// cycling, which mutate the input line directly rather than re-issuing
// key events. Multi-line history entries span several rows, so the
// cursor must land on the final row (not row 0) for the editor to
// navigate the recalled block correctly.
func (h *Handler) setEditText(text string) {
	h.editBuf.Replace(text)
	lastRow := h.editBuf.Rows() - 1
	h.editHandler.SetCursorAtScroll(
		term.Coordinates{X: h.editBuf.Columns(lastRow), Y: lastRow})
}

// clearEdit empties the editor buffer and resets the cursor to the
// start of the line.
func (h *Handler) clearEdit() {
	h.editBuf.Replace("")
	h.editHandler.SetCursorAtScroll(term.Coordinates{})
	h.cycling = false
	h.clearSignatureHint()
}

// editEditorH returns the number of rows reserved for the editor
// input line. The editor lives in the same vertical band the inner
// inputbox would otherwise own, so the prompt visually stays anchored
// to the bottom of the screen. The band grows with the wrapped height
// of the prompt-prefixed buffer (long lines wrap with a hanging
// indent so continuation rows use the full width), capped so the
// inner repl's output keeps at least one row.
func (h *Handler) editEditorH() int {
	if h.height <= 0 || h.width <= 0 {
		return 0
	}
	rows := h.editContent().Height(h.width)
	// When the line exactly fills the width the cursor wraps onto a
	// fresh row that the content itself does not occupy; reserve it
	// so the caret stays visible (mirrors the inputbox behavior).
	if cursorRows := h.editCursorVisual().Y + 1; cursorRows > rows {
		rows = cursorRows
	}
	return min(max(rows, 1), h.height)
}

// editInnerH returns the number of rows allocated to the inner repl
// (its previous output). The editor owns the remaining bottom rows.
func (h *Handler) editInnerH() int {
	return max(0, h.height-h.editEditorH())
}

// drawEdit paints the shell prompt prefix and the input line onto the
// bottom band, where the inner inputbox would normally be drawn. The
// real text editors render a bare buffer (no shell prompt and a
// wrap geometry tied to their own width), so — like the command
// Prompt — the host renders the buffer itself through a responsive
// view with the prompt folded into the content. This yields a hanging
// indent: the first row is "prompt + text" and continuation rows use
// the full width. The caller draws the inner repl's output band above.
func (h *Handler) drawEdit(w term.Writer) {
	innerH := h.editInnerH()
	editorH := h.editEditorH()
	view := h.editContent()
	view.Resize(h.width, editorH)
	editorW := &component.VirtualWriter{
		Writer: w,
		Offset: term.Coordinates{Y: innerH},
		Width:  h.width,
		Height: editorH,
	}
	view.Draw(editorW)
	h.drawEditSelection(editorW)
}

// drawEditSelection overlays the editor's selection highlight, which
// the host bypasses by rendering editContent's attribute-less buffer.
// The bounds are half-open (see command.SelectionBoundsHandler).
func (h *Handler) drawEditSelection(w term.Writer) {
	sb, ok := h.editHandler.(command.SelectionBoundsHandler)
	if !ok {
		return
	}
	from, to, ok := sb.SelectionBounds()
	if !ok {
		return
	}
	from, to = term.CoordinatesSort(from, to)
	attr := term.Attributes{Attrs: term.AttrReverse}
	for y := from.Y; y <= to.Y && y < h.editBuf.Rows(); y++ {
		startX := 0
		endX := h.editBuf.Columns(y)
		if y == from.Y {
			startX = from.X
		}
		if y == to.Y {
			endX = min(to.X, endX)
		}
		for x := startX; x < endX; x++ {
			w.UnionAttributes(h.editVisualPos(term.Coordinates{X: x, Y: y}), attr)
		}
	}
}

// editContent returns a responsive view over a transient buffer
// holding the shell prompt followed by the editor's current text.
// Folding the prompt into the rendered content (rather than offsetting
// the editor) reproduces the inputbox's hanging-indent wrapping.
func (h *Handler) editContent() component.Responsive {
	buf := cell.NewBuffer()
	buf.WriteString(h.prompt + h.editBuf.String())
	return tcomponent.Buffer(buf, component.StringResponsiveConfig{})
}

// drawSignatureHint paints the transient signature-help label on the row
// directly above the editor input band, dimmed so it reads as
// decoration. It is a passive hint: it never moves the cursor and is
// dismissed by editing past the call (see maybeSignatureHelp). The row
// is blanked first so any stale output beneath it does not bleed
// through.
func (h *Handler) drawSignatureHint(w term.Writer) {
	if !h.sigActive || h.sigHint == "" {
		return
	}
	y := h.editInnerH() - 1
	if y < 0 || h.width <= 0 {
		return
	}
	for x := range h.width {
		w.SetCell(term.Coordinates{X: x, Y: y}, term.Cell{Ch: ' '})
	}
	attr := term.Attributes{Attrs: term.AttrDim}
	x := 0
	for _, r := range h.sigHint {
		if x >= h.width {
			break
		}
		w.SetCell(term.Coordinates{X: x, Y: y},
			term.NewCell(r, 0, attr))
		x++
	}
}
