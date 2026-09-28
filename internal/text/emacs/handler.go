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

package emacs

import (
	"context"
	"strconv"
	"strings"
	"unicode"

	"github.com/ernestrc/logd-go/logging"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/registerhistory"
	"unstable.build/rune/internal/text/registerset"
)

var _ component.Scrollable = (*emacsHandler)(nil)

const emacsMarkLocationListID = "mark"

type emacsHandler struct {
	cfg              emacsConfig
	buf              *cell.Buffer
	less             handler.Less
	statusBar        statusBar
	pasteBuf         strings.Builder
	pasteStarted     bool
	resource         workspaceapi.URI
	cursor           text.Cursor
	anchor           term.Coordinates
	lastCursor       term.Coordinates
	correctionChecks uint64
	height           int
	mouse            *mouse.Mouse
	clipboard        clipboard.Register
	macroRecorder    MacroRecorder
	macroPlayer      MacroPlayer
	pendingSetCursor *term.Coordinates
	lastIterateWord  term.Coordinates
	setLocations     bool
	lastPaste        bool
	historyIdx       int
	// lastKill tracks whether the previous handled event was a kill
	// command; killedNow records kills within the current event so
	// consecutive kills accumulate into one kill-ring entry, GNU-style.
	lastKill  bool
	killedNow bool
	// undoRun/undoRunLen track the open GNU amalgamation run: consecutive
	// self-inserts (or same-direction deletes) share one undo group of at
	// most undoRunMax characters.
	undoRun      undoRunKind
	undoRunLen   int
	undoSequence undoSequenceState
	undoPrefix   *undoSequenceState
	minibuffer   minibuffer
	pendingGoto  bool
	pendingZap   bool
	zapCount     int
	prefix       prefixState
	isearch      isearchState
	queryReplace queryReplaceState
	// pendingQuotedInsert holds C-q open until the next key supplies the
	// literal character to insert.
	pendingQuotedInsert bool
	quotedInsertCount   int
}

// NewHandler returns a emacs, simple-to-use text.Handler. indentTabspaces
// is the number of spaces per indent level when indentRune is IndentRuneSpace;
// pass 0 to fall back to the editor's configured tabspaces.
func NewHandler(
	buf *cell.Buffer, resource workspaceapi.URI,
	indentRune rune, indentTabspaces int, opts ...Option,
) text.Handler {
	ret := new(emacsHandler)
	ret.Init(buf, resource, indentRune, indentTabspaces, opts...)
	return ret
}

func (h *emacsHandler) Init(
	buf *cell.Buffer, resource workspaceapi.URI,
	indentRune rune, indentTabspaces int, opts ...Option,
) {
	h.cfg = defaultConfig()
	h.cfg.indentRune = indentRune
	h.cfg.indentTabspaces = indentTabspaces
	for _, o := range opts {
		o(&h.cfg)
	}
	h.buf = buf
	h.resource = resource
	h.less.InitWithBuffer(buf, handler.LessConfig{
		Wrap:               h.cfg.wrap,
		NoBar:              !h.cfg.commandBar,
		SuperimposeMessage: true,
		ResAttr:            h.cfg.resAttr,
		BarAttr:            h.cfg.barAttr,
		MessageLayout:      h.cfg.messageBarLayout,
		Attributes:         h.cfg.attr,
	})
	h.less.Scroll().SetTabspaces(h.cfg.tabspaces)
	h.cursor.Init(h.less.Scroll(), h.cfg.scheduleNextTick)
	h.anchor = h.cursor.CursorAtScroll()
	h.lastCursor = h.anchor
	if spec, ok := text.CommentSpecForURI(resource, h.cfg.comments); ok {
		h.cursor.SetCommentSpec(spec)
	}
	h.mouse = mouse.New(text.CursorMouseDelegate(&h.cursor))
	h.clipboard = h.cfg.clipboard
	h.macroRecorder = h.cfg.macroRecorder
	h.macroPlayer = h.cfg.macroPlayer
	h.statusBar = nopBar{}
	h.lastIterateWord.X = -1
	if h.cfg.enableInitialFolds {
		h.hideInitialFolds()
	}
}

// Resize satisfies tui.Component
func (h *emacsHandler) Resize(width, height int) {
	h.height = height
	h.less.Resize(width, height)
	if h.pendingSetCursor != nil {
		h.SetCursorAtScroll(*h.pendingSetCursor)
		h.pendingSetCursor = nil
	}
}

func (h *emacsHandler) setActiveLocationListMessage(locs []textapi.Location) {
	// NOTE: if there are multiple location lists with a message
	// in current cursor position, then there's no guarantee of which one
	// is going to be rendered.
	for _, loc := range locs {
		if loc.Message != "" {
			h.less.SetMessage("%s", loc.Message)
			return
		}
	}
	h.less.SetMessage("")
}

func (h *emacsHandler) drawLocationMessage() {
	locs, ok := h.cursor.LocationsAtCursor()
	if ok {
		h.setActiveLocationListMessage(locs)
		h.setLocations = true
	} else if h.setLocations {
		h.less.SetMessage("")
		h.setLocations = false
	}
}

// Draw satisfies tui.Component
func (h *emacsHandler) Draw(w term.Writer) {
	h.drawLocationMessage()
	h.less.Draw(w)
	text.DrawLocations(h.cursor.SortedLocations(), h.less.Scroll(), w)
}

func (h *emacsHandler) setMarkLocation() bool {
	locs := h.markLocations()
	pos := h.cursor.CursorAtScroll()
	locs = append(locs, h.newMarkLocation(pos))
	h.cursor.SetLocationList(textapi.LocationPriorityInfo, emacsMarkLocationListID,
		text.LocationSlice(locs))
	return true
}

func (h *emacsHandler) newMarkLocation(pos term.Coordinates) textapi.Location {
	return textapi.Location{
		From: pos,
		To:   term.Coordinates{Y: pos.Y, X: pos.X + 1},
		Attr: term.Attributes{Bg: term.ColorGray},
	}
}

func (h *emacsHandler) markLocations() []textapi.Location {
	for _, list := range h.cursor.LocationLists() {
		if list.ID != emacsMarkLocationListID {
			continue
		}
		locs := make([]textapi.Location, len(list.Locations))
		copy(locs, list.Locations)
		return locs
	}
	return nil
}

func (h *emacsHandler) markLocation() (textapi.Location, bool) {
	locs := h.markLocations()
	if len(locs) == 0 {
		return textapi.Location{}, false
	}
	return locs[len(locs)-1], true
}

func (h *emacsHandler) clearMarkLocation() bool {
	h.cursor.SetLocationList(textapi.LocationPriorityInfo, emacsMarkLocationListID, nil)
	return true
}

func (h *emacsHandler) popMarkLocation() bool {
	locs := h.markLocations()
	if len(locs) == 0 {
		return false
	}
	locs = locs[:len(locs)-1]
	if len(locs) == 0 {
		return h.clearMarkLocation()
	}
	h.cursor.SetLocationList(textapi.LocationPriorityInfo, emacsMarkLocationListID,
		text.LocationSlice(locs))
	return true
}

func (h *emacsHandler) playMacro() bool {
	if h.macroPlayer == nil {
		return false
	}
	if err := h.macroPlayer.Play(registerset.UnnamedRegisterID, 1); err != nil {
		h.log(log.ErrorLevel, "macro playback: %v", err)
	}
	return true
}

// asciiControlKeys maps the Control chords that ASCII defines as ordinary
// keys. A terminal already delivers these as the key itself, so folding them
// here keeps the GUI path identical: C-m is RET and C-i is TAB, exactly as
// in GNU.
var asciiControlKeys = map[rune]term.Key{
	'm': term.KeyEnter,
	'i': term.KeyTab,
}

// normalizeCtrlShift folds a Control chord carrying an upper-case glyph into
// ModCtrlShift. The GUI reports C-S-<letter> as ModCtrlShift while other
// input paths report ModCtrl with the shifted glyph, and without this the
// two paths would need every binding duplicated.
func normalizeCtrlShift(mod term.Modifier, ch rune) term.Modifier {
	if mod == term.ModCtrl && ch >= 'A' && ch <= 'Z' {
		return term.ModCtrlShift
	}
	return mod
}

// startMacroRecording implements <f3> (kmacro-start-macro). Recording into
// the unnamed register mirrors GNU's single "last keyboard macro" slot.
func (h *emacsHandler) startMacroRecording() bool {
	if h.macroRecorder == nil || h.macroRecorder.IsRecording() {
		return false
	}
	h.macroRecorder.Start(registerset.UnnamedRegisterID)
	h.setTransientMode("MACRO")
	return true
}

// endOrCallMacro implements <f4> (kmacro-end-or-call-macro): it closes an
// open recording, or replays the last macro when nothing is being recorded.
func (h *emacsHandler) endOrCallMacro() bool {
	if h.macroRecorder != nil && h.macroRecorder.IsRecording() {
		h.macroRecorder.Stop()
		h.setTransientMode("")
		return true
	}
	return h.playMacro()
}

// copyRegion copies the text between the mark and point to the clipboard,
// leaving point and the buffer unchanged (M-w). A shift-selection stands in
// for an explicit mark, matching GNU shift-select-mode where shifted motion
// activates the region.
func (h *emacsHandler) copyRegion() bool {
	point := h.cursor.CursorAtScroll()
	if _, _, ok := h.cursor.SelectionBounds(); ok {
		if _, err := h.cursor.CopySelection(
			clipboard.DefaultRegisterID, h.clipboard); err != nil {
			h.log(log.ErrorLevel, "cursor copy selection: %v", err)
			return false
		}
		h.cursor.Unselect()
		h.cursor.MoveToScroll(point)
		return true
	}
	loc, ok := h.markLocation()
	if !ok {
		return false
	}
	if !h.cursor.SelectRange(loc.From, point) {
		return false
	}
	if _, err := h.cursor.CopySelection(clipboard.DefaultRegisterID, h.clipboard); err != nil {
		h.log(log.ErrorLevel, "cursor copy selection: %v", err)
		return false
	}
	h.cursor.MoveToScroll(point)
	return true
}

var sexpOpeners = map[rune]struct{}{'(': {}, '[': {}, '{': {}}
var sexpClosers = map[rune]struct{}{')': {}, ']': {}, '}': {}}

// moveForwardSexp implements a bracket-based forward-sexp (C-M-f): it scans
// forward for the next opening bracket, jumps to its balanced close and leaves
// point just past it. It is intentionally bracket-only and does not treat bare
// atoms as sexps. Point is restored when no bracket is found.
func (h *emacsHandler) moveForwardSexp() bool {
	mark := h.cursor.Mark()
	for {
		cell, ok := h.cursor.Cell()
		if ok {
			if _, isOpen := sexpOpeners[cell.Ch]; isOpen {
				if h.cursor.MoveToMatchingRune() {
					h.cursor.MoveRight()
					return true
				}
				break
			}
		}
		if !h.cursor.MoveRight() {
			break
		}
	}
	h.cursor.MoveToMark(mark)
	return false
}

// moveBackwardSexp implements a bracket-based backward-sexp (C-M-b): it scans
// backward for the closing bracket that ends the previous sexp, jumps to its
// balanced open and leaves point on it. Point is restored when no bracket is
// found.
func (h *emacsHandler) moveBackwardSexp() bool {
	mark := h.cursor.Mark()
	for h.cursor.MoveLeft() {
		cell, ok := h.cursor.Cell()
		if !ok {
			continue
		}
		if _, isClose := sexpClosers[cell.Ch]; isClose {
			if h.cursor.MoveToMatchingRune() {
				return true
			}
			break
		}
	}
	h.cursor.MoveToMark(mark)
	return false
}

// bracketPairs lists the delimiter pairs the structural motions understand,
// mirroring sexpOpeners/sexpClosers.
var bracketPairs = [][2]rune{{'(', ')'}, {'{', '}'}, {'[', ']'}}

// enclosingBlock returns the tightest bracket pair enclosing point across the
// supported delimiter kinds. It probes each pair with SelectABlock and keeps
// the innermost match (largest start, smallest end). The selection is cleared
// before returning; ok is false when point is not inside any bracket pair.
func (h *emacsHandler) enclosingBlock() (start, end term.Coordinates, ok bool) {
	origin := h.cursor.CursorAtScroll()
	for _, pair := range bracketPairs {
		if !h.cursor.SelectABlock(pair[0], pair[1]) {
			continue
		}
		s, e, sok := h.cursor.SelectionBounds()
		h.cursor.Unselect()
		h.cursor.MoveToScroll(origin)
		if !sok {
			continue
		}
		if coordLess(e, s) {
			s, e = e, s
		}
		if !ok || coordLess(start, s) {
			start, end, ok = s, e, true
		}
	}
	return
}

// coordLess reports whether a precedes b in document order.
func coordLess(a, b term.Coordinates) bool {
	if a.Y != b.Y {
		return a.Y < b.Y
	}
	return a.X < b.X
}

// killForwardSexp deletes the balanced bracket expression following point
// (C-M-k). It scans forward for the next opening bracket, selects through its
// balanced close and kills the selection to the kill ring. Point and the
// buffer are left untouched when no bracket expression follows.
func (h *emacsHandler) killForwardSexp() bool {
	origin := h.cursor.CursorAtScroll()
	start := origin
	for {
		cell, ok := h.cursor.Cell()
		if ok {
			if _, isOpen := sexpOpeners[cell.Ch]; isOpen {
				break
			}
		}
		if !h.cursor.MoveRight() {
			h.cursor.MoveToScroll(origin)
			return false
		}
		start = h.cursor.CursorAtScroll()
	}
	if !h.cursor.MoveToMatchingRune() {
		h.cursor.MoveToScroll(origin)
		return false
	}
	end := h.cursor.CursorAtScroll()
	// Include the closing bracket in the deleted range.
	end.X++
	if !h.cursor.SelectRange(start, end) {
		h.cursor.MoveToScroll(origin)
		return false
	}
	if !h.killSelection(false) {
		h.cursor.MoveToScroll(origin)
		return false
	}
	return true
}

// moveToDefunStart moves point to the beginning of the enclosing bracketed
// block (C-M-a). There is no true defun-boundary API, so this is a
// bracket-based approximation: it uses the innermost enclosing bracket pair.
func (h *emacsHandler) moveToDefunStart(context.Context) bool {
	start, _, ok := h.enclosingBlock()
	if !ok {
		return false
	}
	_, ok = h.cursor.MoveToScroll(start)
	return ok
}

// moveToDefunEnd moves point to the end of the enclosing bracketed block
// (C-M-e), landing on the closing delimiter. See moveToDefunStart for the
// bracket-based approximation caveat.
func (h *emacsHandler) moveToDefunEnd(context.Context) bool {
	_, end, ok := h.enclosingBlock()
	if !ok {
		return false
	}
	_, ok = h.cursor.MoveToScroll(end)
	return ok
}

// moveUpList moves point to the opening delimiter of the bracket pair
// enclosing it (C-M-u, backward-up-list).
func (h *emacsHandler) moveUpList() bool {
	start, _, ok := h.enclosingBlock()
	if !ok {
		return false
	}
	_, ok = h.cursor.MoveToScroll(start)
	return ok
}

// markDefun implements C-M-h: put the mark at the start of the enclosing
// defun and point at its end, leaving the whole defun as the region. The
// defun boundary is the same bracket-based approximation moveToDefunStart
// uses.
func (h *emacsHandler) markDefun() bool {
	start, end, ok := h.enclosingBlock()
	if !ok {
		return false
	}
	if _, ok = h.cursor.MoveToScroll(start); !ok {
		return false
	}
	h.setMarkLocation()
	if !h.cursor.SelectRange(start, end) {
		return false
	}
	h.cursor.MoveToScroll(end)
	return true
}

// moveDownList moves point just inside the next opening delimiter after it
// (C-M-d, down-list). It scans forward to the next opener and steps one cell
// past it. Point is restored when no opener follows on reachable lines.
func (h *emacsHandler) moveDownList() bool {
	origin := h.cursor.CursorAtScroll()
	for {
		cell, ok := h.cursor.Cell()
		if ok {
			if _, isOpen := sexpOpeners[cell.Ch]; isOpen {
				if h.cursor.MoveRight() {
					return true
				}
				break
			}
		}
		if !h.cursor.MoveRight() {
			break
		}
	}
	h.cursor.MoveToScroll(origin)
	return false
}

func (h *emacsHandler) upcaseWord() bool {
	if !h.selectWordForward() {
		return false
	}
	handled := h.cursor.UppercaseSelection()
	h.cursor.Unselect()
	return handled
}

func (h *emacsHandler) downcaseWord() bool {
	if !h.selectWordForward() {
		return false
	}
	handled := h.cursor.LowercaseSelection()
	h.cursor.Unselect()
	return handled
}

func (h *emacsHandler) capitalizeWord() bool {
	if !h.selectWordForward() {
		return false
	}
	word := h.cursor.Selection()
	capitalized := capitalize(word)
	if capitalized == word {
		h.cursor.Unselect()
		return true
	}
	if !h.cursor.DeleteSelection() {
		h.cursor.Unselect()
		return false
	}
	h.cursor.InsertString(capitalized)
	return true
}

// capitalize upper-cases the first word-constituent letter of s and
// lower-cases the rest, matching Emacs capitalize-word on a region that may
// start with whitespace or punctuation.
func capitalize(s string) string {
	upped := false
	return strings.Map(func(r rune) rune {
		if !upped {
			if !wordRune(r) {
				return r
			}
			upped = true
			return unicode.ToUpper(r)
		}
		return unicode.ToLower(r)
	}, s)
}

// capitalizeWords capitalizes every word in s, for the case commands that
// operate on several words at once.
func capitalizeWords(s string) string {
	inWord := false
	return strings.Map(func(r rune) rune {
		if !wordRune(r) {
			inWord = false
			return r
		}
		if inWord {
			return unicode.ToLower(r)
		}
		inWord = true
		return unicode.ToUpper(r)
	}, s)
}

// yank pastes the newest kill at point and primes the yank-pop cycle so a
// following M-y replaces it with an older kill-ring entry.
func (h *emacsHandler) yank() bool {
	paste, err := h.clipboard.Paste(clipboard.DefaultRegisterID)
	if err != nil {
		h.log(log.ErrorLevel, "clipboard paste: %v", err)
		return false
	}
	mode, _ := paste.Metadata.(text.SelectMode)
	h.cursor.Paste(paste.Text, mode, false)
	h.lastPaste = true
	h.historyIdx = 0
	return true
}

// renderPrompt mirrors the active minibuffer prompt into the echo area. It is
// called after every keystroke the minibuffer consumes.
func (h *emacsHandler) renderPrompt() {
	h.less.SetMessage("%s", h.minibuffer.prompt())
}

func (h *emacsHandler) setTransientMode(mode string) {
	// Zero attributes let the status bar layout own the slot styling; the
	// echo-area attributes must not leak into the status bar.
	h.statusBar.SetStatus(mode, term.Attributes{})
}

// startGotoLine opens the go-to-line prompt (M-g g). On submit it moves point
// to the given one-based line, clamped to the buffer; a blank or malformed
// entry is ignored. The echo area is cleared when the prompt closes.
func (h *emacsHandler) startGotoLine() {
	h.minibuffer.start("Goto line: ", func(text string) {
		defer func() {
			h.less.SetMessage("")
			h.setTransientMode("")
		}()
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		line, err := strconv.Atoi(text)
		if err != nil || line < 1 {
			return
		}
		// SetCursorAtScroll clamps the target to the buffer bounds, so an
		// out-of-range line lands on the last line rather than off the end.
		h.SetCursorAtScroll(term.Coordinates{Y: line - 1, X: 0})
	}, func() {
		h.less.SetMessage("")
		h.setTransientMode("")
	})
	h.setTransientMode("GOTO")
	h.renderPrompt()
}

func (h *emacsHandler) Handle(ev term.Event) (exit, handled bool) {
	ctx := context.Background()

	// Track whether the current event is a paste-related action.
	// Reset lastPaste at the end unless the handler explicitly sets it.
	pastedThisTurn := false
	h.killedNow = false
	defer func() {
		if !pastedThisTurn {
			h.lastPaste = false
			h.historyIdx = 0
		}
		// Any event that is not itself a kill breaks the kill-accumulation
		// chain, mirroring GNU last-command tracking.
		h.lastKill = h.killedNow
	}()

	// only a user event clears a pending set cursor
	h.pendingSetCursor = nil

	switch ev.Type {
	case term.EventMouse:
		// Any mouse action breaks an amalgamation run, like any other
		// intervening command in GNU Emacs.
		h.closeUndoRun()
		h.breakUndoSequence()
		return h.mouse.Handle(ev)
	case term.EventPasteStart:
		h.closeUndoRun()
		h.breakUndoSequence()
		h.pasteBuf.Reset()
		h.pasteStarted = true
		handled = true
		return
	case term.EventPasteEnd:
		if !h.pasteStarted {
			return
		}
		str := h.pasteBuf.String()
		h.pasteStarted = false
		from, to, selected := h.cursor.SelectionBounds()
		if str == "" && (!selected || from == to) {
			if selected {
				h.cursor.Unselect()
			}
			handled = true
			return
		}
		h.buf.MarkStartUndo()
		if selected {
			// Pasting over a selection replaces it with the pasted text.
			h.cursor.DeleteSelection()
			h.cursor.Unselect()
		}
		h.cursor.InsertString(str)
		h.buf.GroupUndo()
		handled = true
		return
	case term.EventKey:
	default:
		return
	}

	if h.pasteStarted {
		if ev.Ch != 0 {
			h.pasteBuf.WriteRune(ev.Ch)
			handled = true
		}
		return
	}
	if current := h.cursor.CursorAtScroll(); current != h.lastCursor {
		h.anchor = current
	}
	correctionChecks := h.correctionChecks
	defer func() {
		if h.correctionChecks == correctionChecks {
			_ = h.doMoveToBounds()
		}
	}()

	// An active echo-area prompt (go-to-line, query-replace) owns every
	// keystroke until it is submitted or cancelled.
	if h.minibuffer.active {
		h.breakUndoSequence()
		h.minibuffer.handle(ev)
		// A submit callback may chain another prompt or start the
		// query-replace loop, each of which sets its own message; only
		// refresh the prompt when this minibuffer is still the active one.
		if h.minibuffer.active {
			h.renderPrompt()
		}
		return false, true
	}

	// An active incremental search owns keystrokes until it exits. A key that
	// both ends the search and carries its own meaning is reprocessed by the
	// normal keymap below.
	if h.isearch.active {
		h.breakUndoSequence()
		if !h.handleIsearchKey(ev) {
			return false, true
		}
	}

	// The query-replace decision loop owns every keystroke until it ends.
	if h.queryReplace.active {
		h.breakUndoSequence()
		h.handleQueryReplaceKey(ev)
		return false, true
	}

	// M-g is a prefix (goto-map). The following key selects the goto
	// command; today only go-to-line is bound, reachable as both M-g g
	// and M-g M-g like in GNU Emacs.
	if h.pendingGoto {
		h.breakUndoSequence()
		h.pendingGoto = false
		if ev.Type == term.EventKey && ev.Ch == 'g' &&
			(ev.Mod == 0 || ev.Mod == term.ModAlt) {
			h.startGotoLine()
			return false, true
		}
		// Any other key aborts the prefix; C-g is the explicit abort.
		h.setTransientMode("")
		return false, true
	}

	// M-z reads the next character as the zap target.
	if h.pendingZap {
		h.breakUndoSequence()
		h.handleZapKey(ev)
		return false, true
	}

	// C-q reads the next key as a literal character.
	if h.pendingQuotedInsert {
		h.breakUndoSequence()
		h.handleQuotedInsertKey(ev)
		return false, true
	}

	// A pending numeric argument owns digit and sign keys; any other key
	// consumes it. Prefix keystrokes are not commands in GNU terms, so
	// they preserve the kill-accumulation and yank-pop chains.
	if h.prefix.active {
		if h.collectPrefixKey(ev) {
			if h.prefix.active {
				h.killedNow = h.lastKill
				pastedThisTurn = h.lastPaste
			}
			return false, true
		}
		count, raw := h.prefix.value(), h.prefix.rawOnly()
		h.prefix = prefixState{}
		h.less.SetMessage("")
		h.setTransientMode("")
		h.closeUndoRun()
		if dispatchesUndo(ev) {
			handled = h.dispatchCounted(ctx, ev, count, raw, &pastedThisTurn)
			if !isUndoEvent(ev) {
				h.breakUndoSequence()
			}
		} else {
			// The whole counted command is one undo group.
			h.buf.MarkStartUndo()
			handled = h.dispatchCounted(ctx, ev, count, raw, &pastedThisTurn)
			h.buf.GroupUndo()
			h.breakUndoSequence()
		}
		return false, handled
	}
	if h.startPrefixArg(ev) {
		h.killedNow = h.lastKill
		pastedThisTurn = h.lastPaste
		return false, true
	}
	if ev.Mod == term.ModCtrl && ev.Ch == 'x' {
		state := h.undoSequence
		h.undoPrefix = &state
		h.breakUndoSequence()
	} else {
		h.undoPrefix = nil
	}
	undoEvent := isUndoEvent(ev)
	defer func() {
		if !undoEvent {
			h.breakUndoSequence()
		}
	}()

	// One command, one undo: everything a dispatched key edits merges into
	// a single undo group, except amalgamating runs which keep their group
	// open across events.
	if h.beginEventUndo(ev) {
		defer h.buf.GroupUndo()
	}

	if ev.Mod == term.ModShift {
		switch ev.Key {
		case term.KeyTab:
			if _, ok := h.cursor.SelectionMode(); ok {
				if handled = h.cursor.ShiftSelectionLeft(h.cfg.indentRune, h.cfg.indentTabspaces); handled {
					h.cursor.Unselect()
				}
			} else {
				handled = h.cursor.ShiftLineLeft(h.cfg.indentRune, h.cfg.indentTabspaces)
			}
			return
		}
	}

	var shift bool
	if ev.Mod&term.ModShift != 0 && ev.Mod == term.ModShift {
		if _, ok := h.cursor.SelectionMode(); !ok {
			h.cursor.Select()
		}
		ev.Mod = ev.Mod &^ term.ModShift
		shift = true
	}

	handled = h.dispatchKey(ctx, ev, shift, &pastedThisTurn)
	return
}

// dispatchKey executes one normal-keymap dispatch for ev. It is the unit a
// numeric argument repeats: motions and edits land here after the modal
// states (minibuffer, isearch, query-replace, pending prefixes) have had
// their turn. shift reports that a shift-extended selection is in progress
// so the move-clears-selection tail leaves it alone; pasted is set when the
// dispatched command was a yank so Handle keeps the yank-pop cycle alive.
func (h *emacsHandler) dispatchKey(
	ctx context.Context, ev term.Event, shift bool, pasted *bool,
) (handled bool) {
	cursorAt := h.cursor.CursorAtScroll()
	if ev.Mod == term.ModCtrl {
		if key, ok := asciiControlKeys[ev.Ch]; ok {
			ev = term.Event{Type: ev.Type, Key: key}
		}
	}
	ev.Mod = normalizeCtrlShift(ev.Mod, ev.Ch)
	switch ev.Mod {
	case term.ModAltShift:
		switch ev.Key {
		case term.KeyArrowDown:
			handled = h.duplicateLine(false /* down */)
		case term.KeyArrowUp:
			handled = h.duplicateLine(true /* up */)
		}
	// <alt> is authentic Emacs Meta (Option on macOS reaches the GUI as
	// ModAlt). The editor owns the real M- editing chords here.
	case term.ModAlt:
		switch ev.Key {
		case term.KeyArrowDown:
			handled = h.moveLine(false /* down */)
		case term.KeyArrowUp:
			handled = h.moveLine(true /* up */)
		case term.KeyArrowLeft:
			handled = h.moveBackwardWord()
		case term.KeyArrowRight:
			handled = h.moveForwardWord()
		case term.KeyBackspace:
			// M-DEL: backward-kill-word.
			handled = h.killBackwardWord()
		case term.KeyDelete:
			// M-Delete: kill-word (forward).
			handled = h.killForwardWord()
		case term.KeySpace:
			// M-SPC: just-one-space.
			handled = h.justOneSpace()
		case 0:
			switch ev.Ch {
			case 'f':
				handled = h.moveForwardWord()
			case 'b':
				handled = h.moveBackwardWord()
			case 'd':
				// M-d: kill-word (forward).
				handled = h.killForwardWord()
			case 'w':
				// M-w: kill-ring-save (copy the region).
				handled = h.copyRegion()
			case '<':
				// M-<: beginning-of-buffer. Alt+Shift+comma reaches the
				// handler as ModAlt with the shifted glyph '<' on both the
				// GUI and terminal paths, so the shift bit never survives as
				// ModAltShift here.
				h.restoreDesiredColumn()
				handled = h.cursor.MoveFirstLine()
			case '>':
				// M->: end-of-buffer.
				h.restoreDesiredColumn()
				handled = h.cursor.MoveLastLine()
			case 'v':
				// M-v: scroll-up (page up), symmetric with C-v.
				handled = h.less.Scroll().SeekUpPage()
			case 't':
				// M-t: transpose-words.
				handled = h.transposeWords()
			case 'm':
				// M-m: back-to-indentation.
				handled = h.backToIndentation()
			case 'a':
				// M-a: backward-sentence.
				handled = h.backwardSentence()
			case 'e':
				// M-e: forward-sentence.
				handled = h.forwardSentence()
			case 'k':
				// M-k: kill-sentence.
				handled = h.killSentence()
			case 'z':
				// M-z: zap-to-char; the next key picks the target.
				handled = h.startZap(1)
			case '^':
				// M-^: delete-indentation (join onto the previous line).
				handled = h.deleteIndentation()
			case '\\':
				// M-\: delete-horizontal-space.
				handled = h.cursor.DeleteHorizontalSpace()
			case 'u':
				handled = h.upcaseWord()
				return
			case 'l':
				handled = h.downcaseWord()
				return
			case 'c':
				handled = h.capitalizeWord()
				return
			case ';':
				// M-;: comment-dwim (toggle line comment).
				handled = h.cursor.ToggleLineComment()
			case 'y':
				// M-y: yank-pop (replace the last yank with an older kill).
				if handled = h.pasteFromHistory(); handled {
					*pasted = true
				}
			case 'q':
				// M-q: fill-paragraph.
				handled = h.cursor.WrapParagraph(h.cfg.ruler)
			case 'g':
				// M-g: prefix for the goto-map. Await the next key
				// (currently only M-g g / go-to-line).
				h.pendingGoto = true
				h.setTransientMode("GOTO")
				handled = true
			case '%':
				// M-%: query-replace.
				handled = h.startQueryReplace()
				return
			case '{':
				// M-{: backward-paragraph.
				h.restoreDesiredColumn()
				handled = h.cursor.MovePrevParagraph()
			case '}':
				// M-}: forward-paragraph.
				h.restoreDesiredColumn()
				handled = h.cursor.MoveNextParagraph()
			}
		}
	// no modifier
	case 0:
		switch ev.Key {
		case term.KeyF3:
			// <f3>: kmacro-start-macro.
			handled = h.startMacroRecording()
			return
		case term.KeyF4:
			// <f4>: kmacro-end-or-call-macro.
			handled = h.endOrCallMacro()
			return
		case term.KeyEsc:
			handled = h.cursor.Unselect()
			h.cursor.Search("")
		case term.KeyEnd:
			handled = h.cursor.MoveEndLine()
		case term.KeyHome:
			handled = h.cursor.MoveStartLine()
		case term.KeyPgup:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveUpLines(h.less.Scroll().SizeHeight())
			if handled {
				h.cursor.RepositionBottom()
			}
		case term.KeyPgdn:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveDownLines(h.less.Scroll().SizeHeight())
			if handled {
				h.cursor.RepositionTop()
			}
		case term.KeyArrowLeft:
			handled = h.cursor.MoveLeft()
		case term.KeyArrowRight:
			handled = h.cursor.MoveRight()
		case term.KeyArrowUp:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveUp()
		case term.KeyArrowDown:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveDown()
		case term.KeyEnter:
			if _, ok := h.cursor.SelectionMode(); ok {
				h.cursor.DeleteSelection()
				h.cursor.Unselect()
			}
			if h.cfg.autoPair {
				h.cursor.InsertAutoPairNewline(h.cfg.indentRune, h.cfg.indentTabspaces)
			} else {
				h.cursor.InsertWithIndentRune('\n', h.cfg.indentRune, h.cfg.indentTabspaces)
			}
			handled = true
		case term.KeySpace:
			if _, ok := h.cursor.SelectionMode(); ok {
				h.cursor.DeleteSelection()
				h.cursor.Unselect()
			}
			h.cursor.InsertWithIndentRune(' ', h.cfg.indentRune, h.cfg.indentTabspaces)
			handled = true
		case term.KeyTab:
			if _, ok := h.cursor.SelectionMode(); ok {
				h.cursor.ShiftSelectionRight(h.cfg.indentRune, h.cfg.indentTabspaces)
				h.cursor.Unselect()
			} else {
				if !h.cursor.TryIndent(h.cfg.indentRune, h.cfg.indentTabspaces) {
					h.cursor.InsertWithIndentRune(
						h.cfg.indentRune, h.cfg.indentRune, h.cfg.indentTabspaces)
				}
			}
			handled = true
		case term.KeyBackspace:
			if _, ok := h.cursor.SelectionMode(); ok {
				handled = h.cursor.DeleteSelection()
				h.cursor.Unselect()
			} else if h.cfg.autoPair {
				handled = h.cursor.BackspaceAutoPair()
			} else {
				handled = h.cursor.Backspace()
			}
		case term.KeyDelete:
			if _, ok := h.cursor.SelectionMode(); ok {
				handled = h.cursor.DeleteSelection()
				h.cursor.Unselect()
			} else {
				handled = h.cursor.Delete()
			}
		default:
			if ev.Ch != 0 {
				if _, ok := h.cursor.SelectionMode(); ok {
					// Like RET, SPC and paste, a typed character replaces
					// the selection rather than just deleting it.
					h.cursor.DeleteSelection()
					h.cursor.Unselect()
				}
				if h.cfg.autoPair {
					handled = h.cursor.InsertWithAutoPair(ev.Ch, h.cfg.indentRune, h.cfg.indentTabspaces)
				} else {
					h.cursor.InsertWithIndentRune(ev.Ch, h.cfg.indentRune, h.cfg.indentTabspaces)
					handled = true
				}
			}
		}
	case term.ModCtrl:
		switch ev.Key {
		case term.KeyEnter:
			h.cursor.InsertLineBelow(h.cfg.indentRune, h.cfg.indentTabspaces)
			handled = true
			return
		case term.KeySpace:
			// C-SPC: set-mark-command.
			handled = h.setMarkLocation()
			return
		}
		switch ev.Ch {
		case 'y':
			// C-y: yank (paste from the clipboard).
			if handled = h.yank(); handled {
				*pasted = true
			}
		case 'l':
			handled = h.cursor.Center()
		case 'v':
			handled = h.less.Scroll().SeekDownPage()
		case 'g':
			// C-g: keyboard-quit. Clear selection, search highlight and any
			// pending mark.
			handled = h.cursor.Unselect()
			h.cursor.Search("")
			h.clearMarkLocation()
		case 'o':
			// C-o: open-line. Insert a newline but keep point before it.
			mark := h.cursor.Mark()
			h.cursor.InsertWithIndentRune('\n', h.cfg.indentRune, h.cfg.indentTabspaces)
			h.cursor.MoveToMark(mark)
			handled = true
		case 'j':
			// C-j: newline-and-indent.
			h.cursor.InsertWithIndentRune('\n', h.cfg.indentRune, h.cfg.indentTabspaces)
			handled = true
		case 'd':
			handled = h.cursor.Delete()
		case 'h':
			handled = h.cursor.Backspace()
		case 'a':
			handled = h.cursor.MoveStartLine()
		case 'p':
			h.restoreDesiredColumn()
			handled = h.cursor.MoveUp()
		case 'n':
			h.restoreDesiredColumn()
			handled = h.cursor.MoveDown()
		case 'q':
			// C-q: quoted-insert; the next key is inserted literally.
			handled = h.startQuotedInsert(1)
			return
		case 'e':
			handled = h.cursor.MoveEndLine()
		case 'f':
			handled = h.cursor.MoveRight()
		case 'b':
			handled = h.cursor.MoveLeft()
		case 'k':
			// C-k: kill-line.
			handled = h.killLine()
		case '/', '_':
			// C-/ and C-_: undo. A terminal folds C-/, C-_ and C-7 into
			// one control code that arrives as C-/; the GUI path delivers
			// the distinct glyphs.
			handled = h.undo()
		case '?':
			// C-?: undo-redo (GUI path only; a terminal C-? is DEL).
			handled = h.undoRedo()
		case 's':
			// C-s: isearch-forward.
			handled = h.startIsearch(true)
			return
		case 'r':
			// C-r: isearch-backward.
			handled = h.startIsearch(false)
			return
		case 't':
			// C-t: transpose-chars.
			handled = h.transposeChars()
		case 'w':
			// C-w: kill-region (mark to point).
			handled = h.killRegion()
		case '=':
			// C-= : expand-region (grow the syntactic selection).
			handled = h.cursor.ExpandSelection(ctx)
			return
		}
	case term.ModCtrlShift:
		switch ev.Key {
		case term.KeyEnter:
			h.cursor.InsertLineAbove(h.cfg.indentRune, h.cfg.indentTabspaces)
			handled = true
			return
		case term.KeyBackspace:
			// C-S-DEL: kill-whole-line.
			handled = h.killWholeLine()
			return
		}
		switch ev.Ch {
		case 'M':
			// Block selection must survive the move-clears-selection tail.
			handled = h.cursor.SelectABlockClose('(', ')') ||
				h.cursor.SelectABlockClose('{', '}') ||
				h.cursor.SelectABlockClose('[', ']')
			return
		case 'W':
			handled = h.cursor.ShrinkSelection()
			return
		case 'Z':
			handled = h.cursor.ToggleFold(ctx)
		case 'A':
			handled = h.cursor.ToggleAllFolds(ctx)
		case 'H':
			handled = h.cursor.HideSelection()
			h.cursor.Unselect()
		case 'V':
			handled = h.cursor.Unhide()
			h.cursor.Unselect()
		}
	case term.ModCtrlAlt:
		switch ev.Key {
		case term.KeyArrowUp:
			pos := h.cursor.CursorAtScroll()
			if handled = h.less.Scroll().SeekUp(); handled {
				win, ok := h.cursor.WindowCoordinates(pos)
				if ok && win.Y >= 0 {
					h.cursor.MoveToScroll(pos)
				}
			}
		case term.KeyArrowDown:
			pos := h.cursor.CursorAtScroll()
			if handled = h.less.Scroll().SeekDown(); handled {
				win, ok := h.cursor.WindowCoordinates(pos)
				if ok && win.Y >= 0 {
					h.cursor.MoveToScroll(pos)
				}
			}
		case 0:
			switch ev.Ch {
			case 'h':
				// C-M-h: mark-defun.
				handled = h.markDefun()
				return
			case 'f':
				// C-M-f: forward-sexp (bracket-based).
				handled = h.moveForwardSexp()
			case 'b':
				// C-M-b: backward-sexp (bracket-based).
				handled = h.moveBackwardSexp()
			case 'k':
				// C-M-k: kill-sexp (bracket-based, forward).
				handled = h.killForwardSexp()
			case 'a':
				// C-M-a: beginning-of-defun. Approximated with the innermost
				// enclosing fold; there is no true defun boundary API.
				handled = h.moveToDefunStart(ctx)
			case 'e':
				// C-M-e: end-of-defun. Fold-based, see moveToDefunStart.
				handled = h.moveToDefunEnd(ctx)
			case 'u':
				// C-M-u: backward-up-list. Move to the opener of the
				// bracket pair enclosing point.
				handled = h.moveUpList()
			case 'd':
				// C-M-d: down-list. Move just inside the next opening
				// bracket after point.
				handled = h.moveDownList()
			case '/', '_':
				// C-M-/ and C-M-_: undo-redo (Emacs 28). The terminal
				// folds both onto the same control code, like C-/.
				handled = h.undoRedo()
			}
		}
	}

	cursorCorrected := h.doMoveToBounds()

	// if moved and shift is not pressed
	if (cursorAt != h.cursor.CursorAtScroll() || cursorCorrected) && !shift {
		h.cursor.Unselect()
		h.cursor.Search("")
	}
	return
}

func (h *emacsHandler) restoreDesiredColumn() {
	if h.cfg.cursorCorrections {
		h.cursor.MoveToScroll(h.anchor)
	}
}

func (h *emacsHandler) doMoveToBounds() bool {
	h.correctionChecks++
	if !h.cfg.cursorCorrections {
		return false
	}
	current := h.cursor.CursorAtScroll()
	if current.Y < h.less.Buffer().Rows() {
		h.anchor = current
	}
	prevCoords := h.cursor.Coordinates()
	h.cursor.MoveToBounds(1)
	if h.cursor.Coordinates().Y != prevCoords.Y {
		h.anchor = h.cursor.CursorAtScroll()
	}
	h.lastCursor = h.cursor.CursorAtScroll()
	return current != h.lastCursor
}

// Cursor satisfies tui.Handler
func (h *emacsHandler) Cursor() (
	pos term.Coordinates, style term.CursorStyle, show bool,
) {
	return h.cursor.Coordinates(), term.CursorStyleSteadyBar, true
}

// Selection satisfies tui.Handler.
func (h *emacsHandler) Selection() (string, bool) {
	text := h.cursor.Selection()
	if text != "" {
		return text, true
	}
	return h.regionText()
}

// regionText returns the text between an explicit mark and point. GNU keeps
// that region live without an active selection, so hosts that read Selection
// (e.g. the clipboardcopy command) would otherwise see nothing to copy after
// C-SPC even though C-w and M-w operate on it.
func (h *emacsHandler) regionText() (string, bool) {
	if _, active := h.cursor.SelectionMode(); active {
		return "", false
	}
	loc, ok := h.markLocation()
	if !ok {
		return "", false
	}
	from, to := term.CoordinatesSort(loc.From, h.cursor.CursorAtScroll())
	cells, _, ok := h.buf.Select(from, to)
	if !ok {
		return "", false
	}
	text := term.CellsToString(cells)
	return text, text != ""
}

// SelectionBounds returns the sorted, half-open [from, to) buffer
// range covered by the active selection highlight, if any.
func (h *emacsHandler) SelectionBounds() (from, to term.Coordinates, ok bool) {
	return h.cursor.SelectionRange()
}

// Close satisfies editor.Handler.
func (h *emacsHandler) Close() error {
	return nil
}

// Resource satisfies editor.Handler.
func (h *emacsHandler) Resource() workspaceapi.URI {
	return h.resource
}

// SetWrap satisfies editor.Handler.
func (h *emacsHandler) SetWrap(wrap bool) {
	h.less.Scroll().Wrap = wrap
}

func (h *emacsHandler) IsSearchMode() bool {
	// An active echo-area prompt (isearch, query-replace, go-to-line) also
	// consumes Enter and other keys, so outer wrappers must delegate to the
	// editor while one is open.
	return h.less.Mode() == handler.LessSearchMode ||
		h.minibuffer.active || h.isearch.active || h.queryReplace.active
}

func (h *emacsHandler) IsNormalMode() bool { return false }

func (t *emacsHandler) ShowCommandBar(show bool) {
	t.less.ShowCommandBar(show)
}

func (h *emacsHandler) SetCursorAtScroll(pos term.Coordinates) bool {
	// Clamp to buffer bounds so a stale or overshooting position (for
	// example after the buffer shrank underneath the cursor) cannot leave
	// the cursor pointing past the last row or column. This mirrors the
	// modal handler and prevents out-of-range panics in downstream edits.
	pos.Y = max(0, min(pos.Y, h.less.Buffer().Rows()-1))
	pos.X = max(0, min(pos.X, h.less.Buffer().Columns(pos.Y)))
	// setCursor should be robust against resizes, etc.
	// only the first client interaction should clear this position
	if h.less.Scroll().Width() == 0 || h.less.Scroll().SizeHeight() == 0 {
		h.pendingSetCursor = new(term.Coordinates)
		*h.pendingSetCursor = pos
		return false
	}

	_, ok := h.cursor.MoveToScroll(pos)
	h.anchor = h.cursor.CursorAtScroll()
	h.lastCursor = h.anchor
	if !ok || !h.cfg.autoCenter {
		return ok
	}
	h.cursor.Center()
	return true
}

func (h *emacsHandler) SeekUp() bool {
	return h.less.Scroll().SeekUp()
}

func (h *emacsHandler) SeekDown() bool {
	return h.less.Scroll().SeekDown()
}

func (h *emacsHandler) SeekOffset() int {
	return h.less.Scroll().SeekOffset()
}

func (h *emacsHandler) MaxSeekOffset() int {
	return h.less.Scroll().MaxSeekOffset()
}

func (h emacsHandler) SetLocationList(
	pri textapi.LocationPriority, ID string, loc text.LocationList,
) {
	h.cursor.SetLocationList(pri, ID, loc)
}

func (h *emacsHandler) MoveToNextLocation(ID string) bool {
	return h.cursor.MoveToNextLocation(ID)
}

func (h *emacsHandler) MoveToPrevLocation(ID string) bool {
	return h.cursor.MoveToPrevLocation(ID)
}

func (h *emacsHandler) CellView() cell.View {
	return h.buf.View()
}

func (h *emacsHandler) CellEditor() cell.Editor {
	return text.ExternalEditor(&h.cursor, h.buf.Editor())
}

// Dimensions satisfies text.Handler (and component.Floating).
// emacsHandler is a leaf handler with no sidebar chrome, so the
// ideal size is the widest row in the underlying buffer by the
// buffer row count. Bar-wrapping handlers add their own
// contribution on top. See text.ViewDimensions for why this must
// sum each cell's Width rather than use View.Columns.
func (h *emacsHandler) Dimensions() (int, int) {
	return text.ViewDimensions(h.buf.View())
}

func (e *emacsHandler) SetDefaultAttributes(attr term.Attributes) {
	e.less.Scroll().Attributes = attr
}

func (h *emacsHandler) CursorAtScroll() term.Coordinates {
	return h.cursor.CursorAtScroll()
}

func (h *emacsHandler) LocationLists() []text.LocationSet {
	return h.cursor.LocationLists()
}

func (h *emacsHandler) moveLine(up bool) (handled bool) {
	// Moving past the buffer edge would scramble line order rather than
	// swap; the boundary is a no-op.
	lo := h.cursor.CursorAtScroll()
	hi := lo
	if from, to, ok := h.cursor.SelectionBounds(); ok {
		lo, hi = sortBounds(from, to)
	}
	if up && lo.Y == 0 {
		return false
	}
	if !up && hi.Y >= h.cursor.View().Rows()-1 {
		return false
	}
	if _, ok := h.cursor.SelectionMode(); !ok {
		if !h.cursor.SelectLine() {
			return
		}
	}
	handled, _ = h.cursor.CopySelectionNoUnselect(
		clipboard.DefaultRegisterID, h.clipboard)
	if !handled {
		return
	}
	h.cursor.DeleteSelection()
	if up {
		h.cursor.MoveUp()
	} else {
		h.cursor.MoveDown()
	}
	h.cursor.MoveStartLine()
	paste, err := h.clipboard.Paste(clipboard.DefaultRegisterID)
	if err != nil {
		h.log(log.ErrorLevel, "clipboard paste: %v", err)
	} else {
		str := paste.Text
		curr := h.cursor.CursorAtScroll()
		h.cursor.Paste(str, text.StandardSelection, false)
		if up {
			curr.Y--
		}
		h.cursor.MoveToScroll(curr)
	}
	return
}

func (h *emacsHandler) duplicateLine(up bool) (handled bool) {
	if _, ok := h.cursor.SelectionMode(); !ok {
		if !h.cursor.SelectLine() {
			return
		}
	}
	handled, _ = h.cursor.CopySelection(
		clipboard.DefaultRegisterID, h.clipboard)
	if !handled {
		return
	}
	if !up {
		h.cursor.MoveDown()
	}
	h.cursor.MoveStartLine()
	paste, err := h.clipboard.Paste(clipboard.DefaultRegisterID)
	if err != nil {
		h.log(log.ErrorLevel, "clipboard paste: %v", err)
	} else {
		str := paste.Text
		curr := h.cursor.CursorAtScroll()
		h.cursor.Paste(str, text.StandardSelection, false)
		h.cursor.MoveToScroll(curr)
	}
	return
}

// pasteFromHistory implements yank-pop (M-y): it replaces the text of the
// yank that immediately preceded it with the next older kill-ring entry,
// wrapping around to the newest once the oldest has been shown. Like GNU
// yank-pop it refuses to run when the previous command was not a yank.
func (h *emacsHandler) pasteFromHistory() (handled bool) {
	if !h.lastPaste {
		return false
	}
	history, ok := registerhistory.AsHistory(h.clipboard)
	if !ok {
		// Without history there is no older kill to rotate in; the yank
		// simply stays, matching a one-entry kill ring.
		return true
	}
	if history.HistoryLen() == 0 {
		return true
	}
	next := (h.historyIdx + 1) % history.HistoryLen()
	data, ok := history.HistoryAt(next)
	if !ok {
		return true
	}
	// Undo the previous paste before replacing it with an older entry.
	h.cursor.Undo()
	// That undo shrank the timeline below the merge mark beginEventUndo
	// opened for this event; re-anchor it so every edit of the replacement
	// paste (a block paste makes several) still merges into one group.
	h.buf.MarkStartUndo()
	mode, _ := data.Metadata.(text.SelectMode)
	h.cursor.Paste(data.Text, mode, false)
	h.historyIdx = next
	h.lastPaste = true
	handled = true
	return
}

func (h *emacsHandler) setStatusBar(bar statusBar) {
	h.statusBar = bar
}

func (h *emacsHandler) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "emacs.handler").Logf(level, msg, args...)
}

var _ foldsService = (*syntax.Tree)(nil)

type foldsService interface {
	FoldsFrom(pos term.Coordinates) (iterator.Iterator[term.Range], bool)
	Folds() (iterator.Iterator[term.Range], bool)
	InitialFolds() (iterator.Iterator[term.Range], bool)
}

func (h *emacsHandler) hideInitialFolds() {
	svc, ok := h.less.Buffer().View().(foldsService)
	if !ok {
		h.log(log.DebugLevel, "folds service not available for resource: %s", h.resource)
		return
	}
	folds, ok := svc.InitialFolds()
	if !ok {
		h.log(log.DebugLevel, "initial folds returned false")
		return
	}

	scroll := h.less.Scroll()

	go debug.CapturePanicReport(func() {
		folds, isEmpty := iterator.IsEmpty(context.Background(), folds)
		if isEmpty {
			folds.Close()
			return
		}
		h.cfg.scheduleNextTick(func() {
			defer folds.Close()
			cursor := h.CursorAtScroll()
			for {
				fold, ok := folds.Next(context.Background())
				if !ok {
					break
				}
				scroll.MarkHidden(fold.Start.Y, fold.End.Y)
			}
			if err := folds.Err(); err != nil {
				h.log(log.ErrorLevel, "error hiding initial folds: %v", err)
			}
			h.SetCursorAtScroll(cursor)
		})
	})
}
