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

package helix

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/text"
)

type (
	helixMode  uint8
	moveMode   uint8
	surroundOp uint8
)

const (
	normalMode helixMode = iota
	insertMode
	gotoMode
	matchMode
	viewMode
	replaceMode
	bracketMode
	jumpMode
	searchMode // never stored in currMode, only reported by mode()
)

const (
	moveNone moveMode = iota
	moveToNext
	moveToPrev
	moveTillNext
	moveTillPrev
)

const (
	surroundNone surroundOp = iota
	surroundAdd
	surroundReplace
	surroundDelete
)

const (
	lastChangeLocationListID = "changes"
	matchingLocID            = "_matchingMark"
	selectionsLocID          = "helix.selections"
	cursorsLocID             = "helix.cursors"
)

const (
	pagePadding = 2
	maxJumps    = 64
)

var _ helixHandler = (*helixHandlerImpl)(nil)

type helixHandler interface {
	tui.Handler

	mode() helixMode
	extending() bool
	moveToNextLocation(ID string) bool
	moveToPrevLocation(ID string) bool
	setLocationList(pri textapi.LocationPriority, ID string, l text.LocationList)
	setCursorAtScroll(pos term.Coordinates) bool
	setNormalMode() bool
	cursorAtScroll() term.Coordinates
	search(string)
	moveToBounds()
	adoptCursor()
	resetSelection()
	selectionBefore() selection
	selectionAfter() selection
	restoreSelection(selection)
	unselect() bool
	copySuppressed() bool
	setStatusBar(bar statusBar)
	insertBeforeSelection()
	takeCount() int
	takeUndoCheckpoint() bool
}

// helixHandlerImpl implements Helix's inverted modal grammar: a motion
// creates or extends a selection and an operator then acts on it. The
// handler owns the selection set in document space; text.Cursor holds
// exactly one selection, the primary, which is re-installed after every
// event so the caret, viewport, mouse and every other consumer of
// text.Handler keep working unchanged.
type helixHandlerImpl struct {
	config    helixConfig
	less      handler.Less
	statusBar statusBar
	anchor    term.Coordinates
	cursor    text.Cursor
	currMode  helixMode

	// sel is the selection set; rec captures the edits an operation
	// makes on one range so the others can be carried through them.
	sel selection
	rec changeRecorder

	// mirror is what the cursor read back as when the primary was last
	// installed: the same range, or the nearest the cursor can show.
	mirror rng

	// secondariesDrawn remembers that the location lists for the
	// ranges the cursor does not hold are populated, so an empty set
	// does not rebuild them on every event; countShown does the same
	// for the selection count in the message bar.
	secondariesDrawn bool
	countShown       bool

	// extend is Helix's sticky select mode (v): motions grow the
	// selection instead of replacing it.
	extend bool

	// stickyView keeps Z's view mode active until <esc>.
	stickyView bool

	moveMode     moveMode
	lastMoveMode moveMode
	moveChar     rune
	searchDir    moveMode
	searchText   string

	matchPending   bool
	matchAround    bool
	surroundMode   surroundOp
	surroundFrom   rune
	bracketForward bool

	pendingRegister  bool
	selectedRegister rune

	countDigits string
	count       int

	insertRegister        strings.Builder
	pendingInsertRegister bool
	undoCheckpoint        bool

	// restoreCursor is Document::restore_cursor: an append (a) widens
	// every range one cell past its end so the insertion lands after
	// it, and leaving insert mode pulls that cell back off.
	restoreCursor bool

	jumps   []selection
	jumpIdx int

	// jumpLabels holds goto_word's live candidates while the two label
	// keys are pending; jumpOuter is -1 until the first one lands.
	jumpLabels []rng
	jumpOuter  int
	jumpExtend bool

	pendingSetCursor *term.Coordinates
	setLocations     bool

	pasteBuf     strings.Builder
	pasteStarted bool

	suppressCopyDelete bool
}

type statusBar interface {
	SetStatus(string, term.Attributes)
}

func (h *helixHandlerImpl) init(buf *cell.Buffer, cfg helixConfig) {
	h.config = cfg
	h.less.InitWithBuffer(buf, h.lessConfig())
	scroll := h.less.Scroll()
	scroll.Attributes = h.config.attr
	scroll.ResultsAttr = h.config.resAttr
	scroll.Subscribe(h)
	h.cursor.Init(scroll, h.config.scheduleNextTick)
	h.initState()
}

func (h *helixHandlerImpl) initWithScroll(scroll *component.Scroll, cfg helixConfig) {
	h.config = cfg
	h.less.InitWithScroll(scroll, h.lessConfig())
	h.cursor.InitPerformance(h.less.Scroll())
	h.initState()
}

func (h *helixHandlerImpl) lessConfig() handler.LessConfig {
	return handler.LessConfig{
		Wrap:               h.config.wrap,
		ResAttr:            h.config.resAttr,
		SuperimposeMessage: true,
		BarAttr:            h.config.barAttr,
		MessageLayout:      h.config.messageBarLayout,
		Attributes:         h.config.attr,
	}
}

func (h *helixHandlerImpl) initState() {
	h.statusBar = nopBar{}
	h.less.Scroll().SetTabspaces(h.config.tabspaces)
	h.cursor.RightInclusiveSemantics = true
	h.buf().Subscribe(&h.rec)
	h.jumpIdx = -1
	h.jumpOuter = -1
	h.anchor = h.cursorAtScroll()
	h.setMode(normalMode)
	h.resetCount()
	// Helix always owns a selection, at minimum the cell under the
	// caret, so every operator has something to act on.
	h.adoptCursor()
}

func (h *helixHandlerImpl) buf() *cell.Buffer { return h.less.Buffer() }

func (h *helixHandlerImpl) setStatusBar(bar statusBar) {
	h.statusBar = bar
	h.setMode(h.currMode)
}

// Resize satisfies tui.Component.
func (h *helixHandlerImpl) Resize(width, height int) {
	h.less.Resize(width, height)
	if h.pendingSetCursor != nil {
		pos := *h.pendingSetCursor
		h.setCursorAtScroll(pos)
		h.pendingSetCursor = nil
	}
	// Selecting before the scroll has a size fails, so the invariant
	// that normal mode always owns a selection is restored here.
	if _, ok := h.cursor.SelectionMode(); !ok && h.currMode == normalMode {
		h.syncPrimary()
	}
}

func (h *helixHandlerImpl) setActiveLocationListMessage(locs []textapi.Location) {
	for _, loc := range locs {
		if loc.Message != "" {
			h.less.SetMessage("%s", loc.Message)
			return
		}
	}
	h.less.SetMessage("")
}

func (h *helixHandlerImpl) drawLocationMessage() {
	locs, ok := h.cursor.LocationsAtCursor()
	if ok {
		h.setActiveLocationListMessage(locs)
		h.setLocations = true
	} else if h.setLocations {
		h.less.SetMessage("")
		h.setLocations = false
	}
}

// Draw satisfies tui.Component.
func (h *helixHandlerImpl) Draw(w term.Writer) {
	h.drawLocationMessage()
	h.less.Draw(w)
	text.DrawLocations(h.cursor.SortedLocations(), h.less.Scroll(), w)
	drawJumpLabels(w, h.less.Scroll(), h.jumpLabels)
}

// Cursor satisfies tui.Handler.
func (h *helixHandlerImpl) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	var style term.CursorStyle
	switch h.mode() {
	case searchMode:
		return h.less.Cursor()
	case insertMode:
		style = term.CursorStyleSteadyBar
	case gotoMode, matchMode, viewMode, replaceMode, bracketMode, jumpMode:
		style = term.CursorStyleSteadyUnderline
	case normalMode:
		style = term.CursorStyleDefault
	default:
		panic(fmt.Sprintf("unknown mode: %d", h.currMode))
	}
	return h.cursor.Coordinates(), style, true
}

func (h *helixHandlerImpl) setMode(mode helixMode) {
	var label string
	var attrs term.Attributes
	switch mode {
	case normalMode:
		if h.extend {
			label = " SELECT"
			attrs = term.Attributes{Bg: term.ColorBlue, Fg: term.ColorBlack}
		} else {
			label = " NORMAL"
		}
	case insertMode:
		label = " INSERT"
		attrs = term.Attributes{Bg: term.ColorGreen, Fg: term.ColorBlack}
	case gotoMode:
		label = "  GOTO "
		attrs = term.Attributes{Bg: term.ColorFuchsia, Fg: term.ColorBlack}
	case matchMode:
		label = " MATCH "
		attrs = term.Attributes{Bg: term.ColorLime, Fg: term.ColorBlack}
	case viewMode:
		label = "  VIEW "
		attrs = term.Attributes{Bg: term.GetColor("orange"), Fg: term.ColorBlack}
	case replaceMode:
		label = "REPLACE"
		attrs = term.Attributes{Bg: term.ColorYellow, Fg: term.ColorBlack}
	case bracketMode:
		label = "BRACKET"
		attrs = term.Attributes{Bg: term.ColorAqua, Fg: term.ColorBlack}
	case jumpMode:
		label = "  JUMP "
		attrs = term.Attributes{Bg: term.ColorFuchsia, Fg: term.ColorBlack}
	case searchMode:
		label = " SEARCH"
		attrs = term.Attributes{Bg: term.ColorSilver, Fg: term.ColorBlack}
	default:
		panic(fmt.Sprintf("unknown mode: %v", mode))
	}
	h.statusBar.SetStatus(label, attrs)
	h.currMode = mode
}

func (h *helixHandlerImpl) setNormalMode() bool {
	if h.less.Mode() == handler.LessSearchMode {
		h.less.SetNormalMode()
	}
	h.resetCount()
	h.extend = false
	h.stickyView = false
	h.moveMode = moveNone
	h.matchPending = false
	h.surroundMode = surroundNone
	h.pendingRegister = false
	h.pendingInsertRegister = false
	h.clearJumpLabels()
	leavingInsert := h.currMode == insertMode
	h.setMode(normalMode)
	if leavingInsert {
		h.leaveInsert()
	}
	return true
}

// leaveInsert is the selection half of Editor::enter_normal_mode. The
// ranges survive insert mode as they are; only an append pulls every
// range back off the cell it was widened onto so the cursor rests on
// the last cell typed. Normal mode's one-cell invariant then widens
// whatever is left as a point.
func (h *helixHandlerImpl) leaveInsert() {
	h.carryEdits()
	restore := h.restoreCursor
	h.restoreCursor = false
	sel := h.sel
	if restore {
		buf := h.buf()
		sel = sel.transform(func(r rng) rng {
			head := r.to()
			if coordinatesBefore(r.anchor, r.head) {
				if prev, ok := prevPos(buf, head); ok {
					head = prev
				}
			}
			return rng{anchor: r.from(), head: head}
		})
	}
	h.setSelection(sel)
}

// exitSelectMode mirrors Helix's exit_select_mode: operators drop the
// sticky extend flag but leave everything else alone.
func (h *helixHandlerImpl) exitSelectMode() {
	if !h.extend {
		return
	}
	h.extend = false
	h.setMode(normalMode)
}

func (h *helixHandlerImpl) setInsertMode() {
	h.insertRegister.Reset()
	h.moveMode = moveNone
	h.matchPending = false
	h.surroundMode = surroundNone
	h.extend = false
	h.restoreCursor = false
	h.setMode(insertMode)
	h.less.SetMessage("")
	h.resetCount()
}

func (h *helixHandlerImpl) mode() helixMode {
	if h.currMode == normalMode && h.less.Mode() != handler.LessNormalMode {
		return searchMode
	}
	return h.currMode
}

func (h *helixHandlerImpl) extending() bool { return h.extend }

// unselect is the text.Handler hook: the set collapses onto the cell
// under the caret, the least Helix ever selects.
func (h *helixHandlerImpl) unselect() bool {
	ok := h.cursor.Unselect()
	h.resetSelection()
	return ok
}

func (h *helixHandlerImpl) copySuppressed() bool { return h.suppressCopyDelete }

func (h *helixHandlerImpl) takeUndoCheckpoint() bool {
	taken := h.undoCheckpoint
	h.undoCheckpoint = false
	return taken
}

func (h *helixHandlerImpl) logError(err error) {
	log.WithField(logging.KeyClass, "helix.handler").Error(err)
}

func (h *helixHandlerImpl) selectionText() string { return h.cursor.Selection() }

// --- counts ---

func (h *helixHandlerImpl) resetCount() {
	h.countDigits = ""
	h.count = 1
}

func (h *helixHandlerImpl) parseCountDigit(ch rune) bool {
	if ch < '0' || ch > '9' {
		return false
	}
	if ch == '0' && h.countDigits == "" {
		return false
	}
	h.countDigits += string(ch)
	count, err := strconv.Atoi(h.countDigits)
	if err != nil {
		h.resetCount()
		return false
	}
	h.count = count
	return true
}

func (h *helixHandlerImpl) motionCount() int { return max(1, h.count) }

func (h *helixHandlerImpl) hasCount() bool { return h.countDigits != "" }

// takeCount hands the pending count to the wrapper, which owns the
// buffer-level undo commands.
func (h *helixHandlerImpl) takeCount() int {
	count := h.motionCount()
	h.resetCount()
	return count
}

// --- registers ---

func (h *helixHandlerImpl) activeRegister() rune {
	if h.selectedRegister != 0 {
		return h.selectedRegister
	}
	return unnamedRegister
}

func (h *helixHandlerImpl) consumeActiveRegister() rune {
	name := h.activeRegister()
	h.selectedRegister = 0
	return name
}

func (h *helixHandlerImpl) readRegister(name rune) (clipboard.Data, error) {
	return h.config.clipboard.Paste(registerNameToID(name))
}

func (h *helixHandlerImpl) writeRegister(name rune, data clipboard.Data) error {
	return h.config.clipboard.Copy(registerNameToID(name), data)
}

func (h *helixHandlerImpl) macroPlaybackActive() bool {
	return h.config.macroPlayer != nil && h.config.macroPlayer.IsPlaying()
}

// --- search ---

func (h *helixHandlerImpl) search(target string) {
	if h.config.disableSearch {
		return
	}
	if h.searchDir == moveNone {
		h.searchDir = moveToNext
	}
	h.searchText = target
	h.cursor.Search(target)
}

// searchSelection implements * and A-*: the selected text becomes the
// search pattern, optionally anchored at word boundaries.
func (h *helixHandlerImpl) searchSelection(detectWordBoundaries bool) bool {
	target := h.selectionText()
	if target == "" || h.config.disableSearch {
		return false
	}
	h.searchDir = moveToNext
	h.searchText = target
	if detectWordBoundaries && h.cursor.Word() == target {
		h.cursor.SearchWord(target)
	} else {
		h.cursor.Search(target)
	}
	if err := h.writeRegister('/', clipboard.Data{Text: target}); err != nil {
		h.logError(err)
	}
	return true
}

func (h *helixHandlerImpl) handleSearch(ev term.Event) (bool, bool) {
	if ev.Mod == 0 && ev.Key == term.KeyEnter {
		target := h.less.SearchText()
		h.less.SetNormalMode()
		if target == "" {
			h.less.SetMessage("")
		} else {
			h.less.SetMessage("searching '%s'", target)
		}
		h.search(target)
		if err := h.writeRegister('/', clipboard.Data{Text: target}); err != nil {
			h.logError(err)
		}
		// Confirming the prompt jumps to and selects the first match,
		// which is what search_impl does on Enter.
		h.moveToMatch(h.searchDir)
		return false, true
	}
	return h.less.Handle(ev)
}

func (h *helixHandlerImpl) beginSearch(ev term.Event, dir moveMode) bool {
	h.searchDir = dir
	if h.config.disableSearch {
		return false
	}
	_, handled := h.less.Handle(ev)
	return handled
}

func reverseDirection(mode moveMode) moveMode {
	switch mode {
	case moveToNext:
		return moveToPrev
	case moveToPrev:
		return moveToNext
	case moveTillNext:
		return moveTillPrev
	case moveTillPrev:
		return moveTillNext
	default:
		return moveNone
	}
}

// --- matching brace highlight ---

func (h *helixHandlerImpl) markMatchingBrace() {
	h.cursor.SetLocationList(textapi.LocationPriorityInfo, matchingLocID, nil)
	c, ok := h.cursor.Cell()
	if !ok {
		return
	}
	var matching rune
	var end bool
	switch c.Ch {
	case '{':
		matching = '}'
	case '(':
		matching = ')'
	case '[':
		matching = ']'
	case '}':
		matching, end = '{', true
	case ')':
		matching, end = '(', true
	case ']':
		matching, end = '[', true
	default:
		return
	}

	var pos term.Coordinates
	if end {
		pos, ok = h.cursor.FindMatchingRuneBackward(c.Ch, matching)
	} else {
		pos, ok = h.cursor.FindMatchingRuneForward(c.Ch, matching)
	}
	if !ok {
		return
	}
	to := pos
	to.X++
	h.cursor.SetLocationList(textapi.LocationPriorityInfo, matchingLocID,
		textapi.LocationSlice([]textapi.Location{
			{From: pos, To: to, Attr: term.Attributes{Attrs: term.AttrReverse}},
		}))
}

// --- dispatch ---

// Handle satisfies tui.Handler.
func (h *helixHandlerImpl) Handle(ev term.Event) (quit, handled bool) {
	// Only a user event clears a pending set cursor.
	h.pendingSetCursor = nil

	switch ev.Type {
	case term.EventPasteStart:
		h.pasteBuf.Reset()
		h.pasteStarted = true
		return false, true
	case term.EventPasteEnd:
		str := h.pasteBuf.String()
		h.pasteStarted = false
		if h.mode() == insertMode && str != "" {
			// A paste goes in at every cursor and closes an undo step
			// of its own, as paste_impl does in insert mode.
			h.insertAtCursors(func() bool { h.cursor.InsertString(str); return true })
			h.insertRegister.WriteString(str)
			h.undoCheckpoint = true
		}
		return false, true
	}

	if h.pasteStarted {
		if ev.Ch != 0 {
			h.pasteBuf.WriteRune(ev.Ch)
		}
		return false, true
	}

	// An edit made between events leaves the cursor where it was but
	// the set carried through the change, so the set is what the
	// command must start from.
	if h.carryEdits() {
		h.syncPrimary()
	}

	mode := h.mode()
	defer h.doneHandle(mode)

	switch mode {
	case searchMode:
		return h.handleSearch(ev)
	case normalMode:
		return h.handleNormal(ev)
	case insertMode:
		return h.handleInsert(ev)
	case gotoMode:
		return h.handleGoto(ev)
	case matchMode:
		return h.handleMatch(ev)
	case viewMode:
		return h.handleView(ev)
	case replaceMode:
		return h.handleReplace(ev)
	case bracketMode:
		return h.handleBracket(ev)
	case jumpMode:
		return h.handleJump(ev)
	default:
		panic(fmt.Sprintf("unknown mode: %d", h.currMode))
	}
}

func (h *helixHandlerImpl) doneHandle(mode helixMode) {
	h.doMoveToBounds()
	h.adoptCursor()
	if mode == normalMode {
		h.markMatchingBrace()
	}
}

func (h *helixHandlerImpl) moveToBounds() {
	h.doMoveToBounds()
	h.adoptCursor()
	h.markMatchingBrace()
}

func (h *helixHandlerImpl) doMoveToBounds() {
	if h.cursor.CursorAtScroll().Y < h.less.Buffer().Rows() {
		h.anchor = h.cursorAtScroll()
	}
	prev := h.cursor.Coordinates()
	h.clampCursor()
	if h.cursor.Coordinates().Y != prev.Y {
		h.anchor = h.cursorAtScroll()
	}
}

// handleNormal dispatches both normal and select mode; the difference
// between them is the sticky extend flag, not the key table.
func (h *helixHandlerImpl) handleNormal(ev term.Event) (quit, handled bool) {
	doResetCount := true
	defer func() {
		if doResetCount {
			h.resetCount()
		}
	}()

	if h.moveMode != moveNone {
		return h.handleFindChar(ev)
	}

	if h.pendingRegister {
		h.pendingRegister = false
		if ev.Ch == 0 && ev.Key == 0 {
			return false, true
		}
		if ev.Mod == 0 && validRegisterName(ev.Ch) {
			h.selectedRegister = normalizedRegisterName(ev.Ch)
			doResetCount = false
			return false, true
		}
		h.selectedRegister = 0
		return false, true
	}

	switch ev.Mod {
	case term.ModCtrl:
		handled = true
		switch ev.Ch {
		case 'b':
			handled = h.pageMotion(-1)
		case 'f':
			handled = h.pageMotion(1)
		case 'u':
			handled = h.halfPageMotion(-1)
		case 'd':
			handled = h.halfPageMotion(1)
		case 'e':
			handled = h.scrollLine(1)
		case 'y':
			handled = h.scrollLine(-1)
		case 'c':
			handled = h.toggleComments()
			h.exitSelectMode()
		case 'a':
			handled = h.increment(h.motionCount())
		case 'x':
			handled = h.increment(-h.motionCount())
		case 's':
			handled = h.pushJump()
		case 'o':
			handled = h.jumpBackward()
		case 'i':
			handled = h.jumpForward()
		default:
			handled = false
		}
	case term.ModAlt:
		handled = true
		switch ev.Key {
		case term.KeyArrowUp:
			handled = h.expandSelection()
		case term.KeyArrowDown:
			handled = h.shrinkSelection()
		default:
			switch ev.Ch {
			case 'd':
				handled = h.deleteSelection(false)
				h.exitSelectMode()
			case 'c':
				h.changeSelection(false)
			case ';':
				handled = h.flipSelections()
			case ':':
				handled = h.ensureSelectionsForward()
			case 'x':
				handled = h.shrinkToLineBounds()
			case 'o':
				handled = h.expandSelection()
			case 'i':
				handled = h.shrinkSelection()
			case '`':
				handled = h.switchCase(strings.ToUpper)
				h.exitSelectMode()
			case '.':
				handled = h.repeatLastMotion()
			case 'J':
				handled = h.joinSelection(true)
			case '*':
				handled = h.searchSelection(false)
			case 'C':
				handled = h.copySelectionOnLine(false)
			case ',':
				handled = h.removePrimarySelection()
			case '-':
				handled = h.mergeSelections()
			case '_':
				handled = h.mergeConsecutiveSelections()
			case 's':
				handled = h.splitSelectionOnNewline()
			default:
				handled = false
			}
		}
	case 0:
		handled = true
		switch ev.Ch {
		case '"':
			h.pendingRegister = true
			doResetCount = false
		case 'h':
			handled = h.moveCaret(h.cursor.MoveLeft)
		case 'l':
			handled = h.moveCaret(h.cursor.MoveRight)
		case 'j':
			handled = h.moveVertically(1)
		case 'k':
			handled = h.moveVertically(-1)
		case 'w':
			handled = h.moveNextWordStart(false)
		case 'W':
			handled = h.moveNextWordStart(true)
		case 'e':
			handled = h.moveNextWordEnd(false)
		case 'E':
			handled = h.moveNextWordEnd(true)
		case 'b':
			handled = h.movePrevWordStart(false)
		case 'B':
			handled = h.movePrevWordStart(true)
		case 'f':
			h.beginFindChar(moveToNext)
			doResetCount = false
		case 'F':
			h.beginFindChar(moveToPrev)
			doResetCount = false
		case 't':
			h.beginFindChar(moveTillNext)
			doResetCount = false
		case 'T':
			h.beginFindChar(moveTillPrev)
			doResetCount = false
		case 'G':
			// Bare G is a no-op in Helix: goto_line only acts on a count.
			handled = h.hasCount() && h.jumping(func() bool {
				return h.moveCaret(h.gotoLine)
			})
		case 'g':
			h.setMode(gotoMode)
			doResetCount = false
		case 'm':
			h.setMode(matchMode)
			doResetCount = false
		case 'z':
			h.setMode(viewMode)
			doResetCount = false
		case 'Z':
			h.stickyView = true
			h.setMode(viewMode)
			doResetCount = false
		case '[':
			h.bracketForward = false
			h.setMode(bracketMode)
			doResetCount = false
		case ']':
			h.bracketForward = true
			h.setMode(bracketMode)
			doResetCount = false
		case 'v':
			h.toggleExtend()
		case ';':
			h.collapseSelection()
		case 'x':
			handled = h.extendLine(false)
		case 'X':
			handled = h.extendToLineBounds()
		case '%':
			handled = h.selectAll()
		case '_':
			handled = h.trimSelections()
		case 'C':
			handled = h.copySelectionOnLine(true)
		case ',':
			handled = h.keepPrimarySelection()
		case '(':
			handled = h.rotateSelections(false)
		case ')':
			handled = h.rotateSelections(true)
		case 'i':
			h.insertBeforeSelection()
		case 'a':
			h.insertAfterSelection()
		case 'I':
			h.insertAtLineStart()
		case 'A':
			h.insertAtLineEnd()
		case 'o':
			h.openLine(false)
		case 'O':
			h.openLine(true)
		case 'd':
			handled = h.deleteSelection(true)
			h.exitSelectMode()
		case 'c':
			h.changeSelection(true)
		case 'y':
			handled = h.yankSelection()
			h.exitSelectMode()
		case 'p':
			handled = h.pasteClipboard(true)
			h.exitSelectMode()
		case 'P':
			handled = h.pasteClipboard(false)
			h.exitSelectMode()
		case 'R':
			handled = h.replaceWithYanked()
			h.exitSelectMode()
		case 'r':
			h.setMode(replaceMode)
			doResetCount = false
		case '~':
			handled = h.switchCase(toggleCase)
			h.exitSelectMode()
		case '`':
			handled = h.switchCase(strings.ToLower)
			h.exitSelectMode()
		case 'J':
			handled = h.joinSelection(false)
		case '>':
			handled = h.shiftSelection(true)
			h.exitSelectMode()
		case '<':
			handled = h.shiftSelection(false)
			h.exitSelectMode()
		case '=':
			handled = h.formatSelection()
		case 'n':
			handled = h.moveToMatch(h.searchDir)
		case 'N':
			handled = h.moveToMatch(reverseDirection(h.searchDir))
		case '/':
			handled = h.beginSearch(ev, moveToNext)
		case '?':
			handled = h.beginSearch(ev, moveToPrev)
		case '*':
			handled = h.searchSelection(true)
		case 'Q':
			handled = h.toggleMacroRecording()
		case 'q':
			handled = h.playMacro()
		default:
			switch ev.Key {
			case term.KeyEsc:
				handled = h.setNormalMode()
			case term.KeyArrowLeft:
				handled = h.moveCaret(h.cursor.MoveLeft)
			case term.KeyArrowRight:
				handled = h.moveCaret(h.cursor.MoveRight)
			case term.KeyArrowDown:
				handled = h.moveVertically(1)
			case term.KeyArrowUp:
				handled = h.moveVertically(-1)
			case term.KeyHome:
				handled = h.moveCaret(h.cursor.MoveStartLine)
			case term.KeyEnd:
				handled = h.moveCaret(h.gotoLineEnd)
			case term.KeyPgup:
				handled = h.pageMotion(-1)
			case term.KeyPgdn:
				handled = h.pageMotion(1)
			case term.KeyTab:
				handled = h.jumpForward()
			default:
				if h.parseCountDigit(ev.Ch) {
					doResetCount = false
					return false, true
				}
				handled = false
			}
		}
	}
	return quit, handled
}

func (h *helixHandlerImpl) toggleExtend() {
	h.extend = !h.extend
	if h.extend {
		if _, ok := h.cursor.SelectionMode(); !ok {
			h.cursor.Select()
		}
	}
	h.setMode(normalMode)
}

// moveVertically keeps the desired column across short lines, which is
// what Helix's old_visual_position does.
func (h *helixHandlerImpl) moveVertically(dir int) bool {
	count := h.motionCount()
	return h.moveCaretOnce(func() bool {
		h.cursor.MoveToScroll(h.anchor)
		if dir > 0 {
			return h.cursor.MoveDownLines(count)
		}
		return h.cursor.MoveUpLines(count)
	})
}

func (h *helixHandlerImpl) expandSelection() bool {
	return h.forEachRange(func() bool {
		h.pinSelection()
		return h.cursor.ExpandSelection(context.Background())
	})
}

func (h *helixHandlerImpl) shrinkSelection() bool {
	return h.forEachRange(func() bool {
		h.pinSelection()
		return h.cursor.ShrinkSelection()
	})
}

// pinSelection hands the highlighted range to the cursor as an explicit
// selection, so the syntax service grows or shrinks that range rather
// than the caret. A one-cell range is left as the caret it is: the
// service treats an unexpanded caret as a zero-width range.
func (h *helixHandlerImpl) pinSelection() {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		return
	}
	if next, ok := nextPos(h.buf(), from); ok && next == to {
		return
	}
	h.cursor.SelectRange(from, to)
}

func (h *helixHandlerImpl) toggleMacroRecording() bool {
	if h.config.macroRecorder == nil || h.macroPlaybackActive() {
		return false
	}
	if h.config.macroRecorder.IsRecording() {
		h.config.macroRecorder.Stop()
		return true
	}
	h.config.macroRecorder.Start(registerNameToID(h.consumeActiveRegister()))
	return true
}

func (h *helixHandlerImpl) playMacro() bool {
	if h.config.macroPlayer == nil {
		return false
	}
	reg := h.consumeActiveRegister()
	if err := h.config.macroPlayer.Play(registerNameToID(reg), h.motionCount()); err != nil {
		h.logError(fmt.Errorf("macro playback: %s", err))
	}
	return true
}

// --- find char ---

func (h *helixHandlerImpl) beginFindChar(mode moveMode) {
	h.moveMode = mode
	h.setMode(normalMode)
}

func (h *helixHandlerImpl) handleFindChar(ev term.Event) (quit, handled bool) {
	mode := h.moveMode
	h.moveMode = moveNone
	defer h.resetCount()
	if ev.Type != term.EventKey || ev.Mod != 0 || ev.Key == term.KeyEsc {
		return false, true
	}
	ch := ev.Ch
	switch ev.Key {
	case term.KeySpace:
		ch = ' '
	case term.KeyTab:
		ch = '\t'
	}
	if ch == 0 {
		return false, true
	}
	h.moveChar = ch
	h.lastMoveMode = mode
	h.findChar(mode, ch)
	return false, true
}

// repeatLastMotion implements A-. for the f/t/F/T family.
func (h *helixHandlerImpl) repeatLastMotion() bool {
	if h.lastMoveMode == moveNone || h.moveChar == 0 {
		return false
	}
	return h.findChar(h.lastMoveMode, h.moveChar)
}

// --- minor modes ---

func (h *helixHandlerImpl) handleGoto(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return false, true
	}
	defer func() {
		// goto_word takes over the mode to collect its label keys, so
		// only an ordinary goto command falls back to normal mode.
		if h.currMode == gotoMode {
			h.setMode(normalMode)
		}
		h.resetCount()
	}()

	if ev.Mod != 0 {
		return false, ev.Key == term.KeyEsc
	}
	if ev.Key == term.KeyEsc {
		return false, true
	}

	switch ev.Ch {
	case 'g':
		if h.hasCount() {
			return false, h.jumping(func() bool { return h.moveCaret(h.gotoLine) })
		}
		h.pushJump()
		return false, h.moveCaret(h.gotoFirstLine)
	case 'e':
		h.pushJump()
		return false, h.moveCaret(h.gotoLastLine)
	case 'h':
		return false, h.moveCaret(h.cursor.MoveStartLine)
	case 'l':
		return false, h.moveCaret(h.gotoLineEnd)
	case 's':
		return false, h.moveCaret(h.cursor.MoveStartLineNonBlank)
	case '|':
		return false, h.jumping(func() bool { return h.moveCaret(h.gotoColumn) })
	case 't':
		return false, h.moveCaret(h.cursor.MoveToWindowTop)
	case 'c':
		return false, h.moveCaret(h.cursor.MoveToWindowMiddle)
	case 'b':
		return false, h.moveCaret(h.cursor.MoveToWindowBottom)
	case 'k':
		return false, h.moveCaret(h.cursor.MoveLineUp)
	case 'j':
		return false, h.moveCaret(h.cursor.MoveLineDown)
	case '.':
		return false, h.jumping(func() bool {
			return h.moveCaret(func() bool {
				return h.cursor.MoveToNextLocation(lastChangeLocationListID)
			})
		})
	case 'w':
		return false, h.jumpToWord(h.extend)
	}
	return false, false
}

func (h *helixHandlerImpl) handleMatch(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return false, true
	}
	if ev.Key == term.KeyEsc || (ev.Mod != 0 && ev.Mod != term.ModShift) {
		h.matchPending = false
		h.surroundMode = surroundNone
		h.setMode(normalMode)
		h.resetCount()
		return false, ev.Key == term.KeyEsc
	}

	if h.surroundMode != surroundNone {
		return h.handleSurroundKey(ev.Ch)
	}

	if h.matchPending {
		// The key is consumed either way: an unresolved object just
		// leaves the selection alone.
		h.selectTextObject(ev.Ch)
		h.matchPending = false
		h.setMode(normalMode)
		h.resetCount()
		return false, true
	}

	switch ev.Ch {
	case 'i':
		h.matchPending = true
		h.matchAround = false
		return false, true
	case 'a':
		h.matchPending = true
		h.matchAround = true
		return false, true
	case 's':
		h.surroundMode = surroundAdd
		return false, true
	case 'r':
		h.surroundMode = surroundReplace
		h.surroundFrom = 0
		return false, true
	case 'd':
		h.surroundMode = surroundDelete
		return false, true
	case 'm':
		handled = h.moveCaret(h.cursor.MoveToMatchingRune)
	default:
		handled = false
	}
	h.setMode(normalMode)
	h.resetCount()
	return quit, handled
}

func (h *helixHandlerImpl) handleView(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return false, true
	}
	stay := h.stickyView
	defer func() {
		if !stay {
			h.stickyView = false
			h.setMode(normalMode)
		}
		h.resetCount()
	}()

	switch ev.Mod {
	case term.ModCtrl:
		switch ev.Ch {
		case 'f':
			return false, h.pageMotion(1)
		case 'b':
			return false, h.pageMotion(-1)
		case 'd':
			return false, h.halfPageMotion(1)
		case 'u':
			return false, h.halfPageMotion(-1)
		}
		return false, false
	case 0:
		switch ev.Key {
		case term.KeyEsc:
			stay = false
			return false, true
		case term.KeyArrowDown:
			return false, h.scrollLine(1)
		case term.KeyArrowUp:
			return false, h.scrollLine(-1)
		case term.KeyPgdn:
			return false, h.pageMotion(1)
		case term.KeyPgup:
			return false, h.pageMotion(-1)
		case term.KeySpace:
			return false, h.halfPageMotion(1)
		case term.KeyBackspace:
			return false, h.halfPageMotion(-1)
		}
		switch ev.Ch {
		case 'z', 'c':
			return false, h.cursor.Center()
		case 't':
			return false, h.cursor.RepositionTop()
		case 'b':
			return false, h.cursor.RepositionBottom()
		case 'm':
			return false, h.cursor.Center()
		case 'j':
			return false, h.scrollLine(1)
		case 'k':
			return false, h.scrollLine(-1)
		case 'n':
			return false, h.moveToMatch(h.searchDir)
		case 'N':
			return false, h.moveToMatch(reverseDirection(h.searchDir))
		case '/':
			stay = false
			return false, h.beginSearch(ev, moveToNext)
		case '?':
			stay = false
			return false, h.beginSearch(ev, moveToPrev)
		}
	}
	return false, false
}

// handleBracket dispatches the [ and ] minor modes. Helix fills these
// with diagnostic, change and syntax-object navigation; only the entries
// text.Cursor can serve are bound, the rest stay free for the command
// layer.
func (h *helixHandlerImpl) handleBracket(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return false, true
	}
	forward := h.bracketForward
	defer func() {
		h.setMode(normalMode)
		h.resetCount()
	}()
	if ev.Mod != 0 {
		return false, false
	}
	if ev.Key == term.KeyEsc {
		return false, true
	}
	if ev.Key == term.KeySpace {
		return false, h.addNewline(forward)
	}
	switch ev.Ch {
	case 'p':
		if forward {
			return false, h.moveCaret(func() bool {
				return h.cursor.MoveNextParagraphs(h.motionCount())
			})
		}
		return false, h.moveCaret(func() bool {
			return h.cursor.MovePrevParagraphs(h.motionCount())
		})
	}
	return false, false
}

func (h *helixHandlerImpl) handleReplace(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return false, true
	}
	defer func() {
		h.setMode(normalMode)
		h.resetCount()
	}()
	if ev.Mod != 0 || ev.Key == term.KeyEsc {
		return false, ev.Key == term.KeyEsc
	}
	ch := ev.Ch
	switch ev.Key {
	case term.KeySpace:
		ch = ' '
	case term.KeyTab:
		ch = '\t'
	case term.KeyEnter:
		ch = '\n'
	}
	if ch == 0 {
		return false, true
	}
	ok := h.replaceSelection(ch)
	h.exitSelectMode()
	return false, ok
}

// --- insert ---

// exitInsert is normal_mode on <esc>: the session's keystrokes go to
// the . register and the ranges are left as leaveInsert has them.
func (h *helixHandlerImpl) exitInsert() {
	if inserted := h.insertRegister.String(); inserted != "" {
		if err := h.writeRegister('.', clipboard.Data{
			Text: inserted, Metadata: text.NoSelection,
		}); err != nil {
			h.logError(err)
		}
	}
	h.setNormalMode()
}

func (h *helixHandlerImpl) insertTabIndent() {
	if h.cursor.TryIndent(h.config.indentRune, h.config.indentTabspaces) {
		h.insertRegister.WriteRune(h.config.indentRune)
		return
	}
	h.insertIndentLiteral()
}

func (h *helixHandlerImpl) insertIndentLiteral() {
	h.cursor.InsertWithIndentRune(
		h.config.indentRune, h.config.indentRune, h.config.indentTabspaces)
	h.insertRegister.WriteRune(h.config.indentRune)
}

// deleteToLineStart implements C-u: Helix kills back to the first
// non-blank, or to the line start when the caret is already there, or
// joins with the previous line at column zero.
func (h *helixHandlerImpl) deleteToLineStart() bool {
	pos := h.cursor.CursorAtScroll()
	if pos.X == 0 {
		if pos.Y == 0 {
			return false
		}
		return h.cursor.Backspace()
	}
	h.cursor.Unselect()
	target := term.Coordinates{Y: pos.Y}
	if firstNonBlank, ok := h.firstNonBlank(pos.Y); ok && firstNonBlank.X < pos.X {
		target = firstNonBlank
	}
	// An explicit range keeps the delete right-exclusive, so the cell
	// the caret sits on survives.
	if !h.cursor.SelectRange(target, pos) {
		return false
	}
	ok := h.cursor.DeleteSelection()
	h.cursor.Unselect()
	return ok
}

// deleteToLineEnd implements C-k, deleting the line ending itself when
// the caret already sits past the last cell.
func (h *helixHandlerImpl) deleteToLineEnd() bool {
	pos := h.cursor.CursorAtScroll()
	buf := h.less.Buffer()
	if pos.Y >= buf.Rows() {
		return false
	}
	end := term.Coordinates{Y: pos.Y, X: buf.Columns(pos.Y)}
	if pos.X >= end.X {
		// The caret already sits on the line separator, so kill_to_line_end
		// swallows it and pulls the next line up.
		return h.cursor.Conflate()
	}
	h.cursor.Unselect()
	if !h.cursor.SelectRange(pos, end) {
		return false
	}
	ok := h.cursor.DeleteSelection()
	h.cursor.Unselect()
	h.cursor.MoveToScroll(pos)
	return ok
}

// backspace implements delete_char_backward. Helix tries a dedent
// first: when only indentation precedes the caret, backspace removes a
// whole indent level instead of a single space.
func (h *helixHandlerImpl) backspace() bool {
	if h.dedent() {
		return true
	}
	if h.config.autoPair {
		return h.cursor.BackspaceAutoPair()
	}
	return h.cursor.Backspace()
}

// dedent removes up to one indent level of spaces before the caret,
// mirroring helix-term's dedent helper. A tab takes the fast path of a
// plain backspace, and a line that is not purely indented so far is
// left to the caller.
func (h *helixHandlerImpl) dedent() bool {
	pos := h.cursor.CursorAtScroll()
	if pos.X == 0 || h.config.indentRune != text.IndentRuneSpace {
		return false
	}
	width := h.config.indentTabspaces
	if width <= 0 {
		width = h.config.tabspaces
	}
	if width <= 1 {
		return false
	}
	line := []rune(rowString(h.buf(), pos.Y))
	if pos.X > len(line) {
		return false
	}
	for _, ch := range line[:pos.X] {
		if ch != ' ' && ch != '\t' {
			return false
		}
	}
	drop := pos.X % width
	if drop == 0 {
		drop = width
	}
	start := pos.X
	for range drop {
		if start == 0 || line[start-1] != ' ' {
			break
		}
		start--
	}
	if start == pos.X {
		return false
	}
	h.cursor.Unselect()
	if !h.cursor.SelectRange(term.Coordinates{X: start, Y: pos.Y}, pos) {
		return false
	}
	ok := h.cursor.DeleteSelection()
	h.cursor.Unselect()
	return ok
}

// deleteWordForward implements A-d in insert mode.
func (h *helixHandlerImpl) deleteWordForward() bool {
	origin := h.cursor.CursorAtScroll()
	h.cursor.Unselect()
	if !h.cursor.MoveRightEndWord() {
		return false
	}
	end := h.cursor.CursorAtScroll()
	if next, ok := nextCoord(h.buf(), end); ok {
		end = next
	}
	h.cursor.MoveToScroll(origin)
	if !h.cursor.SelectRange(origin, end) {
		return false
	}
	ok := h.cursor.DeleteSelection()
	h.cursor.Unselect()
	return ok
}

func (h *helixHandlerImpl) firstNonBlank(row int) (term.Coordinates, bool) {
	buf := h.less.Buffer()
	if row < 0 || row >= buf.Rows() {
		return term.Coordinates{}, false
	}
	cells := buf.View().RawCells()
	if row >= len(cells) {
		return term.Coordinates{}, false
	}
	for x, c := range cells[row] {
		if c.Ch != ' ' && c.Ch != '\t' {
			return term.Coordinates{X: x, Y: row}, true
		}
	}
	return term.Coordinates{}, false
}

// insertRegisterContents is insert_register, C-r: paste_impl at every
// cursor, so a register holding one fragment per range puts each
// fragment at its own range, and the paste closes an undo step of its
// own as append_changes_to_history does there.
func (h *helixHandlerImpl) insertRegisterContents(name rune) bool {
	data, err := h.readRegister(name)
	if err != nil {
		h.logError(err)
		return false
	}
	if data.Text == "" {
		return false
	}
	h.carryEdits()
	values, _ := h.registerValues(data, 1)
	h.fanOut(func(i int) bool {
		h.cursor.InsertString(values[i])
		return true
	}, h.edited)
	h.insertRegister.WriteString(data.Text)
	h.undoCheckpoint = true
	return true
}

// insertAtCursors runs an insert-mode edit at every range's cursor,
// reporting whether it did anything at any of them.
func (h *helixHandlerImpl) insertAtCursors(op func() bool) bool {
	return h.fanOut(func(int) bool { return op() }, h.edited)
}

// insertRune is insert_char: the auto-pair hook when it is on, a plain
// insert otherwise.
func (h *helixHandlerImpl) insertRune(ch rune) {
	if h.config.autoPair {
		h.fanOut(func(int) bool {
			return h.cursor.InsertWithAutoPair(ch, h.config.indentRune, h.config.indentTabspaces)
		}, h.paired)
	} else {
		h.insertAtCursors(func() bool {
			h.cursor.InsertWithIndentRune(ch, h.config.indentRune, h.config.indentTabspaces)
			return true
		})
	}
	h.insertRegister.WriteRune(ch)
}

// insertNewline is insert_newline.
func (h *helixHandlerImpl) insertNewline() {
	h.insertAtCursors(func() bool {
		if h.config.autoPair {
			return h.cursor.InsertAutoPairNewline(h.config.indentRune, h.config.indentTabspaces)
		}
		h.cursor.InsertWithIndentRune('\n', h.config.indentRune, h.config.indentTabspaces)
		return true
	})
	h.insertRegister.WriteRune('\n')
}

// moveCursors runs an insert-mode movement at every range, each of
// which becomes a point where its caret lands.
func (h *helixHandlerImpl) moveCursors(op func() bool) bool {
	return h.forEachRange(func() bool {
		h.cursor.MoveToScroll(h.anchor)
		return op()
	})
}

func (h *helixHandlerImpl) handleInsert(ev term.Event) (quit, handled bool) {
	if h.pendingInsertRegister {
		h.pendingInsertRegister = false
		if ev.Ch == 0 && ev.Key == 0 {
			return false, true
		}
		if ev.Mod == 0 && validRegisterName(ev.Ch) {
			return false, h.insertRegisterContents(normalizedRegisterName(ev.Ch))
		}
		return false, true
	}

	switch ev.Mod {
	case 0:
		switch ev.Key {
		case term.KeyEnter:
			h.insertNewline()
			handled = true
		case term.KeySpace:
			h.insertRune(' ')
			handled = true
		case term.KeyTab:
			h.insertAtCursors(func() bool { h.insertTabIndent(); return true })
			handled = true
		case term.KeyBackspace:
			h.insertAtCursors(h.backspace)
			handled = true
		case term.KeyDelete:
			h.insertAtCursors(h.cursor.Delete)
			handled = true
		case term.KeyEsc:
			h.exitInsert()
			handled = true
		case term.KeyArrowUp:
			handled = h.moveCursors(h.cursor.MoveUp)
		case term.KeyArrowRight:
			handled = h.moveCursors(h.cursor.MoveRight)
		case term.KeyArrowDown:
			handled = h.moveCursors(h.cursor.MoveDown)
		case term.KeyArrowLeft:
			handled = h.moveCursors(h.cursor.MoveLeft)
		case term.KeyHome:
			handled = h.moveCursors(h.cursor.MoveStartLine)
		case term.KeyEnd:
			handled = h.moveCursors(h.cursor.MoveEndLine)
		case term.KeyPgup:
			handled = h.moveCursors(func() bool {
				return h.cursor.MoveUpLines(max(1, h.less.Scroll().SizeHeight()-pagePadding))
			})
		case term.KeyPgdn:
			handled = h.moveCursors(func() bool {
				return h.cursor.MoveDownLines(max(1, h.less.Scroll().SizeHeight()-pagePadding))
			})
		default:
			if ev.Ch != 0 {
				h.insertRune(ev.Ch)
				handled = true
			}
		}
	case term.ModShift:
		switch ev.Key {
		case term.KeyTab:
			h.insertAtCursors(func() bool { h.insertIndentLiteral(); return true })
			handled = true
		case term.KeyBackspace:
			handled = h.insertAtCursors(h.backspace)
		}
	case term.ModAlt:
		switch ev.Key {
		case term.KeyBackspace:
			handled = h.insertAtCursors(h.cursor.BackspaceWord)
		case term.KeyDelete:
			handled = h.insertAtCursors(h.deleteWordForward)
		default:
			if ev.Ch == 'd' {
				handled = h.insertAtCursors(h.deleteWordForward)
			}
		}
	case term.ModCtrl:
		switch ev.Ch {
		case 'c':
			h.exitInsert()
			handled = true
		case 'h':
			handled = h.insertAtCursors(h.backspace)
		case 'w':
			handled = h.insertAtCursors(h.cursor.BackspaceWord)
		case 'u':
			handled = h.insertAtCursors(h.deleteToLineStart)
		case 'k':
			handled = h.insertAtCursors(h.deleteToLineEnd)
		case 'd':
			handled = h.insertAtCursors(h.cursor.Delete)
		case 'j':
			h.insertNewline()
			handled = true
		case 'r':
			h.pendingInsertRegister = true
			handled = true
		case 's':
			h.undoCheckpoint = true
			handled = true
		}
	}
	return quit, handled
}

// wordMotion implements w/W, e/E and b/B over every range. Normal mode
// keeps the range word_move produced; select mode keeps the existing
// anchor and only adopts the new cursor, which is what
// extend_word_impl does.
func (h *helixHandlerImpl) wordMotion(target wordMotionTarget) bool {
	h.carryEdits()
	buf := h.buf()
	count := h.motionCount()
	return h.transformSelection(func(r rng) rng {
		anchor, head := wordMove(buf, r.anchor, r.head, count, target)
		next := rng{anchor: anchor, head: head}
		if h.extend {
			return r.putCursor(buf, next.cursor(buf), true)
		}
		return next
	})
}

func (h *helixHandlerImpl) moveNextWordStart(group bool) bool {
	if group {
		return h.wordMotion(nextLongWordStart)
	}
	return h.wordMotion(nextWordStart)
}

func (h *helixHandlerImpl) moveNextWordEnd(group bool) bool {
	if group {
		return h.wordMotion(nextLongWordEnd)
	}
	return h.wordMotion(nextWordEnd)
}

func (h *helixHandlerImpl) movePrevWordStart(group bool) bool {
	if group {
		return h.wordMotion(prevLongWordStart)
	}
	return h.wordMotion(prevWordStart)
}

// findChar implements f/t/F/T. Helix offsets the search start so a
// till motion can be repeated without standing still, then selects from
// the original caret through the landing cell. A miss leaves the range
// untouched.
func (h *helixHandlerImpl) findChar(mode moveMode, ch rune) bool {
	count := h.motionCount()
	return h.runMotionOnce(func() bool {
		origin := h.cursor.CursorAtScroll()
		prevAnchor, hadAnchor := h.selectionAnchor()
		h.cursor.Unselect()
		findNext := func() bool {
			return h.moved(func() bool { return h.cursor.MoveToNextChar(ch) })
		}
		findPrev := func() bool {
			return h.moved(func() bool { return h.cursor.MoveToPrevChar(ch) })
		}
		nth := func(step func() bool) bool {
			for range count {
				if !step() {
					return false
				}
			}
			return true
		}
		var ok bool
		switch mode {
		case moveToNext:
			ok = nth(findNext)
		case moveToPrev:
			ok = nth(findPrev)
		case moveTillNext:
			// The search starts one cell further on so a repeat cannot
			// land back on the character the caret already sits next to.
			if h.cursor.MoveRight() {
				if ok = nth(findNext); ok {
					h.cursor.MoveLeft()
				}
			}
		case moveTillPrev:
			if h.cursor.MoveLeft() {
				if ok = nth(findPrev); ok {
					h.cursor.MoveRight()
				}
			}
		}
		if !ok || h.cursor.CursorAtScroll() == origin {
			h.cursor.MoveToScroll(origin)
			if hadAnchor {
				h.setSelectionRange(prevAnchor, origin)
			} else {
				h.setSelectionRange(origin, origin)
			}
			return false
		}
		h.setSelectionRange(origin, h.cursor.CursorAtScroll())
		return true
	})
}

// gotoLineEnd puts the caret on the last cell of the line rather than
// past it, which is where Helix's goto_line_end lands.
func (h *helixHandlerImpl) gotoLineEnd() bool {
	if !h.cursor.MoveEndLine() {
		return false
	}
	pos := h.cursor.CursorAtScroll()
	if pos.X > 0 {
		h.cursor.MoveLeft()
	}
	return true
}

// gotoColumn implements g| : the count-th column of the current line,
// clamped to the line end.
func (h *helixHandlerImpl) gotoColumn() bool {
	pos := h.cursor.CursorAtScroll()
	if pos.Y >= h.less.Buffer().Rows() {
		return false
	}
	pos.X = max(0, min(h.motionCount()-1, h.less.Buffer().Columns(pos.Y)-1))
	_, ok := h.cursor.MoveToScroll(pos)
	return ok
}

// gotoLine implements G and a counted gg: jump to the count-th line,
// skipping a trailing blank last line the way Helix does.
func (h *helixHandlerImpl) gotoLine() bool {
	buf := h.less.Buffer()
	rows := buf.Rows()
	if rows == 0 {
		return false
	}
	maxLine := rows - 1
	if buf.Columns(maxLine) == 0 && rows > 1 {
		maxLine--
	}
	target := max(0, min(h.motionCount()-1, maxLine))
	_, ok := h.cursor.MoveToScroll(term.Coordinates{Y: target})
	return ok
}

// gotoFirstLine is goto_file_start: the very first position, not the
// current column on the first line.
func (h *helixHandlerImpl) gotoFirstLine() bool {
	_, ok := h.cursor.MoveToScroll(term.Coordinates{})
	return ok
}

// gotoLastLine skips a trailing blank last line, matching
// goto_last_line_impl.
func (h *helixHandlerImpl) gotoLastLine() bool {
	buf := h.less.Buffer()
	rows := buf.Rows()
	if rows == 0 {
		return false
	}
	target := rows - 1
	if buf.Columns(target) == 0 && rows > 1 {
		target--
	}
	_, ok := h.cursor.MoveToScroll(term.Coordinates{Y: target})
	return ok
}

// moveToMatch implements n/N as search_impl does: the scan starts from
// the far edge of the primary so a match already under the caret is
// not found again, and the hit replaces the primary or, in select
// mode, joins the set as a new primary, which is how a search grows a
// selection per match.
func (h *helixHandlerImpl) moveToMatch(dir moveMode) bool {
	return h.jumping(func() bool {
		h.carryEdits()
		moved := false
		for range h.motionCount() {
			if !h.stepToMatch(dir) {
				break
			}
			moved = true
		}
		return moved
	})
}

func (h *helixHandlerImpl) stepToMatch(dir moveMode) bool {
	primary := h.sel.primaryRange()
	from, to, hadSelection := h.cursor.SelectionRange()
	h.cursor.Unselect()
	if hadSelection {
		if dir == moveToPrev {
			h.cursor.MoveToScroll(from)
		} else {
			last := to
			if last.X > 0 {
				last.X--
			}
			h.cursor.MoveToScroll(last)
		}
	}
	var ok bool
	if dir == moveToPrev {
		ok = h.cursor.MoveToPrevMatch()
	} else {
		ok = h.cursor.MoveToNextMatch()
	}
	if !ok {
		h.installRange(primary)
		return false
	}
	h.selectMatchAtCaret()
	hit := h.readRange().withDirection(primary.backward())
	if h.extend {
		h.setSelection(h.sel.with(hit))
	} else {
		h.setSelection(h.sel.replace(h.sel.primary, hit))
	}
	return true
}

// selectMatchAtCaret covers the search hit the caret was just moved to.
func (h *helixHandlerImpl) selectMatchAtCaret() {
	width := len([]rune(h.searchText))
	h.cursor.Select()
	for range max(0, width-1) {
		if !h.cursor.MoveRightWrap() {
			break
		}
	}
}

// scrollLine scrolls the viewport and only drags the caret along when it
// would otherwise leave the window.
func (h *helixHandlerImpl) scrollLine(dir int) bool {
	pos := h.cursor.CursorAtScroll()
	scroll := h.less.Scroll()
	moved := false
	for range h.motionCount() {
		if dir > 0 {
			moved = scroll.SeekDown() || moved
		} else {
			moved = scroll.SeekUp() || moved
		}
	}
	if !moved {
		return false
	}
	win, _ := h.cursor.WindowCoordinates(pos)
	if win.Y >= 0 && win.Y < scroll.SizeHeight() {
		h.cursor.SetCursorAtScroll(pos)
	}
	return true
}

// pageMotion is page_up/page_down, which drag only the primary along
// with the view; halfPageMotion is page_cursor_half_up/down, which
// move every range.
func (h *helixHandlerImpl) pageMotion(dir int) bool {
	return h.primaryMotion(h.scrollStep(dir, max(1, h.less.Scroll().SizeHeight()-pagePadding)))
}

func (h *helixHandlerImpl) halfPageMotion(dir int) bool {
	return h.moveCaret(h.scrollStep(dir, max(1, h.less.Scroll().SizeHeight()/2)))
}

func (h *helixHandlerImpl) scrollStep(dir, by int) func() bool {
	return func() bool {
		if dir > 0 {
			if !h.cursor.MoveDownLines(by) {
				return false
			}
			h.cursor.RepositionTop()
			return true
		}
		if !h.cursor.MoveUpLines(by) {
			return false
		}
		h.cursor.RepositionBottom()
		return true
	}
}

// --- jumplist ---

// jumpIdx mirrors helix-view's JumpList::current: it points one past the
// newest entry after a push, so the first C-o records where the caret is
// now and then steps back onto the last pushed jump. An entry is the
// whole selection set, as JumpList::push stores it.
func (h *helixHandlerImpl) currentJump() selection {
	h.carryEdits()
	return h.sel.clone()
}

func (h *helixHandlerImpl) pushJump() bool {
	h.pushJumpEntry(h.currentJump())
	return true
}

// jumping records where the caret was before an explicit jump so C-o
// can walk back to it, which is what Helix's push_jump does for G, g|,
// g., a confirmed search and n/N.
func (h *helixHandlerImpl) jumping(fn func() bool) bool {
	origin := h.currentJump()
	if !fn() {
		return false
	}
	h.pushJumpEntry(origin)
	return true
}

// pushJumpEntry reports how many entries were dropped off the front so a
// caller holding an index into the list can rebase it.
func (h *helixHandlerImpl) pushJumpEntry(entry selection) int {
	if h.jumpIdx >= 0 && h.jumpIdx < len(h.jumps) {
		h.jumps = h.jumps[:h.jumpIdx]
	}
	if n := len(h.jumps); n > 0 && h.jumps[n-1].equal(entry) {
		return 0
	}
	removed := 0
	h.jumps = append(h.jumps, entry)
	if over := len(h.jumps) - maxJumps; over > 0 {
		h.jumps = h.jumps[over:]
		removed = over
	}
	h.jumpIdx = len(h.jumps)
	return removed
}

func (h *helixHandlerImpl) jumpBackward() bool {
	count := h.motionCount()
	if h.jumpIdx < count || len(h.jumps) == 0 {
		return false
	}
	next := h.jumpIdx - count
	if h.jumpIdx == len(h.jumps) {
		next -= h.pushJumpEntry(h.currentJump())
	}
	if next < 0 || next >= len(h.jumps) {
		return false
	}
	// Helix skips an entry that matches where the caret already is.
	if h.jumps[next].equal(h.currentJump()) {
		if next == 0 {
			return false
		}
		next--
	}
	h.jumpIdx = next
	h.restoreJump(h.jumps[h.jumpIdx])
	return true
}

func (h *helixHandlerImpl) jumpForward() bool {
	next := h.jumpIdx + h.motionCount()
	if next >= len(h.jumps) {
		return false
	}
	h.jumpIdx = next
	h.restoreJump(h.jumps[h.jumpIdx])
	return true
}

func (h *helixHandlerImpl) restoreJump(entry selection) {
	h.setSelection(entry.clone())
}

// --- operators ---

// forEachLineBlock runs op once per run of consecutive lines the
// ranges touch, with that run installed as a linewise selection, and
// carries the ranges through the edits. It is what Helix's get_lines
// gives the line operators: a line shared by two ranges is handled
// once, and the ranges keep their shape through Range::map rather than
// snapping to the lines.
func (h *helixHandlerImpl) forEachLineBlock(op func()) bool {
	h.carryEdits()
	buf := h.buf()
	rows := buf.Rows()
	if rows == 0 {
		return false
	}
	type block struct{ first, last int }
	var blocks []block
	for _, r := range h.sel.ranges {
		first, last := r.lineRange(buf)
		if n := len(blocks); n > 0 && first <= blocks[n-1].last+1 {
			blocks[n-1].last = max(blocks[n-1].last, last)
			continue
		}
		blocks = append(blocks, block{first, last})
	}
	src := h.sel
	var all changeSet
	for i := len(blocks) - 1; i >= 0; i-- {
		b := blocks[i]
		h.installRange(rng{
			anchor: term.Coordinates{Y: b.first},
			head:   term.Coordinates{Y: min(b.last+1, rows)},
		})
		op()
		all = append(all, h.rec.take()...)
	}
	h.setSelection(src.mapThrough(all))
	return len(all) > 0
}

// shiftSelection is indent and unindent: every line any range touches
// is shifted once, count levels deep, and the ranges are carried
// through the whitespace that came or went. Indenting skips blank
// lines; unindenting takes up to count levels of the whitespace a
// line starts with, a tab counting for the columns it reaches.
func (h *helixHandlerImpl) shiftSelection(right bool) bool {
	h.carryEdits()
	count := h.motionCount()
	buf := h.buf()
	indent := strings.Repeat(h.indentUnit(), count)
	width := count * h.config.indentTabspaces
	var edits []edit
	for _, line := range h.selectedLines() {
		row := []rune(rowString(buf, line))
		if right {
			if strings.TrimSpace(string(row)) == "" {
				continue
			}
			at := term.Coordinates{Y: line}
			edits = append(edits, edit{from: at, to: at, text: indent})
			continue
		}
		tab := max(1, h.config.indentTabspaces)
		cols, pos := 0, 0
	blanks:
		for pos < len(row) && cols < width {
			switch row[pos] {
			case ' ':
				cols++
			case '\t':
				cols = (cols/tab + 1) * tab
			default:
				break blanks
			}
			pos++
		}
		if pos > 0 {
			edits = append(edits, edit{from: term.Coordinates{Y: line}, to: term.Coordinates{X: pos, Y: line}})
		}
	}
	if len(edits) == 0 {
		return false
	}
	_, cs := h.applyEdits(edits)
	h.setSelection(h.sel.mapThrough(cs))
	return true
}

// indentUnit is one level of indentation as the buffer writes it.
func (h *helixHandlerImpl) indentUnit() string {
	if h.config.indentRune == text.IndentRuneTab {
		return "\t"
	}
	return strings.Repeat(" ", max(1, h.config.indentTabspaces))
}

// selectedLines is get_lines: every row some range touches, once, in
// order.
func (h *helixHandlerImpl) selectedLines() []int {
	buf := h.buf()
	var lines []int
	for _, r := range h.sel.ranges {
		first, last := r.lineRange(buf)
		for y := first; y <= last; y++ {
			if n := len(lines); n == 0 || lines[n-1] < y {
				lines = append(lines, y)
			}
		}
	}
	return lines
}

func (h *helixHandlerImpl) formatSelection() bool {
	return h.forEachLineBlock(func() {
		h.cursor.ReindentSelection(h.config.indentRune, h.config.indentTabspaces)
	})
}

func (h *helixHandlerImpl) toggleComments() bool {
	return h.forEachLineBlock(func() { h.cursor.ToggleLineComment() })
}

// yankSelection is yank_impl: the register receives one fragment per
// range.
func (h *helixHandlerImpl) yankSelection() bool {
	h.carryEdits()
	data := h.fragmentsData()
	reg := h.consumeActiveRegister()
	for _, name := range []rune{reg, unnamedRegister, lastYankRegister} {
		if err := h.writeRegister(name, data); err != nil {
			h.logError(err)
			return false
		}
	}
	return true
}

// copySelectionForDelete fills the registers Helix writes on a delete,
// unless the black hole register asks for the text to be dropped.
func (h *helixHandlerImpl) copySelectionForDelete() {
	reg := h.consumeActiveRegister()
	if reg == blackHoleRegister {
		return
	}
	data := h.fragmentsData()
	names := []rune{reg, unnamedRegister}
	if !h.selectionLinewise() {
		names = append(names, '-')
	}
	for _, name := range names {
		if err := h.writeRegister(name, data); err != nil {
			h.logError(err)
		}
	}
}

// selectionLinewise is selection_is_linewise: every range covers whole
// lines.
func (h *helixHandlerImpl) selectionLinewise() bool {
	for _, r := range h.sel.ranges {
		if !lineShaped(r) {
			return false
		}
	}
	return true
}

// deleteSelection is delete_selection_impl: the registers are filled
// once for the whole set, then every range is removed and each
// collapses onto where its text was. Helix's Alt-d/Alt-c variants pass
// yank=false so the registers are left untouched. Nothing to remove,
// which is a set of points, reports false.
func (h *helixHandlerImpl) deleteSelection(yank bool) bool {
	h.carryEdits()
	edits := h.deleteEdits()
	if len(edits) == 0 {
		h.selectedRegister = 0
		return false
	}
	if yank {
		h.copySelectionForDelete()
	} else {
		h.selectedRegister = 0
	}
	_, cs := h.applyEdits(edits)
	h.setSelection(h.sel.mapThrough(cs))
	return true
}

// changeSelection implements c and Alt-c. A selection that covers whole
// lines is replaced by a fresh blank line rather than collapsing the
// surrounding ones, which is delete_selection_impl's only_whole_lines
// branch.
func (h *helixHandlerImpl) changeSelection(yank bool) bool {
	h.carryEdits()
	if h.selectionLinewise() {
		deleted := h.deleteSelection(yank)
		h.openLine(true)
		return deleted
	}
	deleted := h.deleteSelection(yank)
	h.setInsertMode()
	// The delete left a point at the start of every range, which the
	// normal-mode invariant widened onto a cell; insert mode has them
	// collapsed again.
	h.setSelection(h.sel.transform(func(r rng) rng { return point(r.from()) }))
	return deleted
}

// pasteClipboard is paste_impl: the i-th register fragment lands next
// to the i-th range, the last fragment serving every range beyond
// that, and the pasted text becomes the selection. Unlike vi's
// visual-mode paste, Helix never replaces the selection: that is what
// R does. A characterwise paste goes at range.from()/range.to(), a
// linewise one at the surrounding line boundaries.
func (h *helixHandlerImpl) pasteClipboard(after bool) bool {
	data, err := h.readRegister(h.consumeActiveRegister())
	if err != nil {
		h.logError(err)
		return false
	}
	if data.Text == "" {
		return false
	}
	h.carryEdits()
	// paste_impl repeats the register contents, not the insertion, so a
	// counted paste lands as one contiguous run.
	values, mode := h.registerValues(data, h.motionCount())
	linewise := mode == text.LineSelection
	for _, v := range values {
		linewise = linewise || strings.HasSuffix(v, "\n")
	}
	buf := h.buf()
	rows := buf.Rows()
	edits := make([]edit, len(values))
	opened := make([]bool, len(values))
	for i, r := range h.sel.ranges {
		value := values[i]
		var pos term.Coordinates
		switch {
		case linewise && !after:
			pos = term.Coordinates{Y: r.from().Y}
		case linewise:
			_, last := r.lineRange(buf)
			pos = term.Coordinates{Y: min(last+1, rows)}
		case !after:
			pos = r.from()
		default:
			pos = r.to()
		}
		if linewise && !strings.HasSuffix(value, "\n") {
			value += "\n"
		}
		// The buffer has no position past its last row, so a line
		// pasted after it opens the row it needs first.
		if pos.Y >= rows && rows > 0 {
			pos = term.Coordinates{X: buf.Columns(rows - 1), Y: rows - 1}
			value = "\n" + strings.TrimSuffix(value, "\n")
			opened[i] = true
		}
		edits[i] = edit{from: pos, to: pos, text: value}
	}
	spans, _ := h.applyEdits(edits)
	for i := range spans {
		if opened[i] {
			// The span starts on the line-break it opened with; the
			// pasted lines are the rows after it, down to the document
			// end, which is where its trailing line ending went.
			spans[i] = rng{
				anchor: term.Coordinates{Y: spans[i].anchor.Y + 1},
				head:   term.Coordinates{Y: spans[i].head.Y + 1},
			}
		}
		spans[i] = spans[i].withDirection(h.sel.ranges[i].backward())
	}
	h.setSelection(selection{ranges: spans, primary: h.sel.primary})
	return true
}

// replaceWithYanked is replace_selections_with_register: every range
// is swapped for its register fragment.
func (h *helixHandlerImpl) replaceWithYanked() bool {
	data, err := h.readRegister(h.consumeActiveRegister())
	if err != nil {
		h.logError(err)
		return false
	}
	if data.Text == "" {
		return false
	}
	h.carryEdits()
	values, _ := h.registerValues(data, h.motionCount())
	edits := make([]edit, 0, len(values))
	for i, r := range h.sel.ranges {
		if r.isPoint() {
			continue
		}
		edits = append(edits, edit{from: r.from(), to: r.to(), text: values[i]})
	}
	_, cs := h.applyEdits(edits)
	h.setSelection(h.sel.mapThrough(cs))
	return true
}

// replaceSelection is replace: every character under every range
// becomes ch, line endings excepted, and the ranges survive.
func (h *helixHandlerImpl) replaceSelection(ch rune) bool {
	h.carryEdits()
	buf := h.buf()
	edits := make([]edit, 0, h.sel.len())
	for _, r := range h.sel.ranges {
		if r.isPoint() {
			continue
		}
		var b strings.Builder
		for _, c := range rangeText(buf, r) {
			if c == '\n' {
				b.WriteRune(c)
			} else {
				b.WriteRune(ch)
			}
		}
		edits = append(edits, edit{from: r.from(), to: r.to(), text: b.String()})
	}
	if len(edits) == 0 {
		return false
	}
	_, cs := h.applyEdits(edits)
	h.setSelection(h.sel.mapThrough(cs))
	return true
}

// switchCase is switch_case_impl over every range: the text under each
// range is rewritten in place and the ranges survive.
func (h *helixHandlerImpl) switchCase(fn func(string) string) bool {
	h.carryEdits()
	buf := h.buf()
	edits := make([]edit, 0, h.sel.len())
	for _, r := range h.sel.ranges {
		if r.isPoint() {
			continue
		}
		text := rangeText(buf, r)
		if next := fn(text); next != text {
			edits = append(edits, edit{from: r.from(), to: r.to(), text: next})
		}
	}
	if len(edits) == 0 {
		return false
	}
	_, cs := h.applyEdits(edits)
	h.setSelection(h.sel.mapThrough(cs))
	return true
}

// toggleCase is switch_case: every letter flips its case.
func toggleCase(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLower(r):
			return unicode.ToUpper(r)
		case unicode.IsUpper(r):
			return unicode.ToLower(r)
		}
		return r
	}, s)
}

// joinSelection is join_selections: every line a range touches but
// the last is joined with the line after it, a range on one line
// taking the next line. The ranges are carried through the joins.
// selectSpace implements A-J, which instead selects every separator
// the joins inserted, or keeps the carried ranges when none was.
func (h *helixHandlerImpl) joinSelection(selectSpace bool) bool {
	h.carryEdits()
	edits := h.joinEdits()
	if len(edits) == 0 {
		return false
	}
	spans, cs := h.applyEdits(edits)
	if selectSpace {
		var spaces []rng
		for i, e := range edits {
			if e.text != "" {
				spaces = append(spaces, point(spans[i].from()))
			}
		}
		if len(spaces) > 0 {
			h.setSelection(selection{ranges: spaces})
			return true
		}
	}
	h.setSelection(h.sel.mapThrough(cs))
	return true
}

// joinEdits is the change list join_selections_impl builds: each join
// replaces a line ending and the indentation of the line after it
// with one space, or with nothing when that line is blank. A line
// comment leader the block's first line carries is stripped from the
// joined lines that carry the same one, so joining a comment keeps
// one leader; a different leader is kept and becomes the one to
// strip from then on. Two ranges asking for the same join produce it
// once.
func (h *helixHandlerImpl) joinEdits() []edit {
	buf := h.buf()
	rows := buf.Rows()
	tokens := slices.Clone(h.cursor.CommentSpec().Line)
	slices.SortStableFunc(tokens, func(a, b string) int { return len(b) - len(a) })
	leader := func(line []rune) string {
		for _, tok := range tokens {
			if strings.HasPrefix(string(line), tok) {
				return tok
			}
		}
		return ""
	}
	skipBlank := func(line []rune, at int) int {
		for at < len(line) && (line[at] == ' ' || line[at] == '\t') {
			at++
		}
		return at
	}
	var edits []edit
	for _, r := range h.sel.ranges {
		start, end := r.lineRange(buf)
		if start == end {
			end = min(end+1, rows-1)
		}
		first := []rune(rowString(buf, start))
		current := leader(first[skipBlank(first, 0):])
		for line := start; line < end; line++ {
			next := []rune(rowString(buf, line+1))
			at := skipBlank(next, 0)
			if tok := leader(next[at:]); tok != "" {
				if tok == current {
					at = skipBlank(next, at+len([]rune(tok)))
				} else {
					current = tok
				}
			}
			separator := " "
			if at == len(next) {
				separator = ""
			}
			edits = append(edits, edit{
				from: term.Coordinates{X: buf.Columns(line), Y: line},
				to:   term.Coordinates{X: at, Y: line + 1},
				text: separator,
			})
		}
	}
	sortEdits(edits)
	return slices.CompactFunc(edits, func(a, b edit) bool { return a.from == b.from })
}

// selectAll implements %: one range over the whole document.
func (h *helixHandlerImpl) selectAll() bool {
	buf := h.less.Buffer()
	rows := buf.Rows()
	if rows == 0 {
		return false
	}
	lastY := rows - 1
	to := term.Coordinates{X: buf.Columns(lastY), Y: lastY}
	if !h.cursor.SelectRange(term.Coordinates{}, to) {
		return false
	}
	h.resetSelection()
	return true
}

// insertBeforeSelection is insert_mode, i, and also the entry point
// the Helix wrapper uses to replay the last insert for `.`: every
// range flips so its cursor sits on its first cell.
func (h *helixHandlerImpl) insertBeforeSelection() {
	h.carryEdits()
	h.setInsertMode()
	h.setSelection(h.sel.transform(func(r rng) rng {
		return rng{anchor: r.to(), head: r.from()}
	}))
}

// insertAfterSelection is append_mode, a: every range widens one cell
// past its end so its cursor, and so the insertion, lands right after
// it. A range ending at the end of the document gets a line ending
// put there first so there is a cell to widen onto.
func (h *helixHandlerImpl) insertAfterSelection() {
	h.carryEdits()
	h.setInsertMode()
	h.restoreCursor = true
	buf := h.buf()
	if rows, n := buf.Rows(), h.sel.len(); rows > 0 && n > 0 {
		end := term.Coordinates{X: buf.Columns(rows - 1), Y: rows - 1}
		last := h.sel.ranges[n-1]
		if !last.isPoint() && last.to() == end {
			buf.Edit(context.Background(), end, end, "\n")
			h.carryEdits()
		}
	}
	h.setSelection(h.sel.transform(func(r rng) rng {
		head := r.to()
		if next, ok := nextPos(buf, head); ok {
			head = next
		}
		return rng{anchor: r.from(), head: head}
	}))
}

// insertAtLineStart and insertAtLineEnd are insert_with_indent, I and
// A: every range's cursor goes to the first non-blank or the end of
// its line, as a point, or extending the range when select mode was
// on.
func (h *helixHandlerImpl) insertAtLineStart() {
	h.insertWithIndent(false)
}

func (h *helixHandlerImpl) insertAtLineEnd() {
	h.insertWithIndent(true)
}

func (h *helixHandlerImpl) insertWithIndent(lineEnd bool) {
	wasSelect := h.extend
	h.carryEdits()
	h.setInsertMode()
	buf := h.buf()
	h.setSelection(h.sel.transform(func(r rng) rng {
		line := clampInsert(buf, r.cursor(buf)).Y
		pos := term.Coordinates{Y: line}
		if lineEnd {
			pos.X = buf.Columns(line)
		} else if first, ok := h.firstNonBlank(line); ok {
			pos = first
		}
		return r.putCursor(buf, pos, wasSelect)
	}))
}

// openLine is open: a line goes in next to every range, count times,
// and a cursor lands on each new line, which is what makes 3o three
// cursors. o opens under the last row a range covers and O above its
// first, whichever end the head is on.
func (h *helixHandlerImpl) openLine(above bool) {
	count := h.motionCount()
	h.carryEdits()
	h.setInsertMode()
	primary := h.sel.primary
	buf := h.buf()
	h.fanOut(func(i int) bool {
		r := h.sel.ranges[i]
		h.cursor.Unselect()
		row := r.from().Y
		if !above {
			_, row = r.lineRange(buf)
		}
		h.cursor.MoveToScroll(term.Coordinates{Y: row})
		for i := range count {
			if above && i == 0 {
				h.cursor.InsertLineAbove(h.config.indentRune, h.config.indentTabspaces)
				continue
			}
			h.cursor.InsertLineBelow(h.config.indentRune, h.config.indentTabspaces)
		}
		return true
	}, nil)
	if count < 2 {
		return
	}
	// The cursor ends on the last line it opened; the others sit
	// directly above it with the same indentation.
	ranges := make([]rng, 0, h.sel.len()*count)
	for _, r := range h.sel.ranges {
		for i := count - 1; i >= 0; i-- {
			ranges = append(ranges, point(term.Coordinates{X: r.head.X, Y: r.head.Y - i}))
		}
	}
	h.setSelection(selection{ranges: ranges, primary: primary})
}

// addNewline is add_newline_impl: a blank line goes above or below the
// lines every range touches, without leaving normal mode, and the
// ranges follow the text. The line below goes in at the start of the
// following row, so a range ending there stays short of it; on the
// last row it is appended instead.
func (h *helixHandlerImpl) addNewline(below bool) bool {
	h.carryEdits()
	buf := h.buf()
	rows := buf.Rows()
	if rows == 0 {
		return false
	}
	newlines := strings.Repeat("\n", h.motionCount())
	edits := make([]edit, 0, h.sel.len())
	for _, r := range h.sel.ranges {
		first, last := r.lineRange(buf)
		at := term.Coordinates{Y: first}
		switch {
		case below && last+1 < rows:
			at = term.Coordinates{Y: last + 1}
		case below:
			at = term.Coordinates{Y: last, X: buf.Columns(last)}
		}
		edits = append(edits, edit{from: at, to: at, text: newlines})
	}
	_, cs := h.applyEdits(edits)
	h.setSelection(h.sel.mapThrough(cs))
	return true
}

// selectTextObject implements the mi/ma pairs over every range.
func (h *helixHandlerImpl) selectTextObject(ch rune) bool {
	around := h.matchAround
	h.matchPending = false
	h.matchAround = false
	return h.forEachRange(func() bool { return h.selectTextObjectAt(ch, around) })
}

func (h *helixHandlerImpl) selectTextObjectAt(ch rune, around bool) bool {
	var ok bool
	switch ch {
	case 'm':
		pos, _, hasSelection := h.cursor.SelectionRange()
		if !hasSelection {
			pos = h.cursor.CursorAtScroll()
		}
		open, closing, found := closestPair(h.buf(), pos)
		if !found {
			return false
		}
		if around {
			ok = h.cursor.SelectABlock(open, closing)
		} else {
			ok = h.cursor.SelectInnerBlock(open, closing)
		}
	case 'w':
		if around {
			ok = h.cursor.SelectAWords(h.motionCount())
		} else {
			ok = h.cursor.SelectInnerWords(h.motionCount())
		}
	case 'W':
		if around {
			ok = h.cursor.SelectAWordGroups(h.motionCount())
		} else {
			ok = h.cursor.SelectInnerWordGroups(h.motionCount())
		}
	case 's':
		if around {
			ok = h.cursor.SelectASentence()
		} else {
			ok = h.cursor.SelectInnerSentence()
		}
	case 'p':
		if around {
			ok = h.cursor.SelectAParagraph()
		} else {
			ok = h.cursor.SelectInnerParagraph()
		}
	case '"', '\'', '`':
		if around {
			ok = h.cursor.SelectAQuote(ch)
		} else {
			ok = h.cursor.SelectInnerQuote(ch)
		}
	case '(', ')', 'b':
		if around {
			ok = h.cursor.SelectABlock('(', ')')
		} else {
			ok = h.cursor.SelectInnerBlock('(', ')')
		}
	case '{', '}', 'B':
		if around {
			ok = h.cursor.SelectABlock('{', '}')
		} else {
			ok = h.cursor.SelectInnerBlock('{', '}')
		}
	case '[', ']':
		if around {
			ok = h.cursor.SelectABlock('[', ']')
		} else {
			ok = h.cursor.SelectInnerBlock('[', ']')
		}
	case '<', '>', 't':
		if around {
			ok = h.cursor.SelectABlock('<', '>')
		} else {
			ok = h.cursor.SelectInnerBlock('<', '>')
		}
	default:
		return false
	}
	return ok
}

// --- surround ---

// handleSurroundKey consumes the delimiter keys of ms / mr / md.
func (h *helixHandlerImpl) handleSurroundKey(ch rune) (quit, handled bool) {
	op := h.surroundMode
	if ch == 0 {
		return false, true
	}
	if op == surroundReplace && h.surroundFrom == 0 {
		h.surroundFrom = ch
		return false, true
	}

	h.surroundMode = surroundNone
	from := h.surroundFrom
	h.surroundFrom = 0
	defer func() {
		h.setMode(normalMode)
		h.resetCount()
	}()

	switch op {
	case surroundAdd:
		return false, h.surroundAdd(ch)
	case surroundDelete:
		return false, h.surroundDelete(ch)
	case surroundReplace:
		return false, h.surroundReplace(from, ch)
	}
	return false, true
}

// surroundAdd is surround_add: every range is wrapped in the pair and
// the delimiters join the range.
func (h *helixHandlerImpl) surroundAdd(ch rune) bool {
	if _, ok := h.cursor.SelectionMode(); !ok {
		return false
	}
	h.carryEdits()
	open, closing := surroundPair(ch)
	src := h.sel
	edits := make([]edit, 0, src.len()*2)
	for _, r := range src.ranges {
		edits = append(edits,
			edit{from: r.from(), to: r.from(), text: string(open)},
			edit{from: r.to(), to: r.to(), text: string(closing)})
	}
	spans, _ := h.applyEdits(edits)
	ranges := make([]rng, src.len())
	for i, r := range src.ranges {
		ranges[i] = rng{anchor: spans[2*i].from(), head: spans[2*i+1].to()}.withDirection(r.backward())
	}
	h.setSelection(selection{ranges: ranges, primary: src.primary})
	h.exitSelectMode()
	return true
}

// surroundPositions is surround::get_surround_pos: the opening and
// closing delimiter of the pair enclosing every range, as consecutive
// pairs in range order. A range without one, or two ranges sharing
// one, fail the whole command with the message Helix shows.
func (h *helixHandlerImpl) surroundPositions(open, closing rune) ([]term.Coordinates, bool) {
	h.carryEdits()
	var pairs []term.Coordinates
	var err string
	for _, r := range h.sel.ranges {
		h.installRange(r)
		from, to, found := h.surroundBounds(open, closing)
		if !found || to.X == 0 {
			err = "pair not found"
			break
		}
		last := term.Coordinates{X: to.X - 1, Y: to.Y}
		if slices.Contains(pairs, from) || slices.Contains(pairs, last) {
			err = "cursors overlap for a single surround pair"
			break
		}
		pairs = append(pairs, from, last)
	}
	h.syncPrimary()
	if err != "" {
		h.less.SetMessage("%s", err)
		return nil, false
	}
	return pairs, true
}

// surroundDelete is surround_delete: the pair around every range goes
// away and the ranges are carried through the deletions.
func (h *helixHandlerImpl) surroundDelete(ch rune) bool {
	pairs, ok := h.surroundPositions(surroundPair(ch))
	if !ok {
		return false
	}
	edits := make([]edit, 0, len(pairs))
	for _, at := range pairs {
		edits = append(edits, edit{from: at, to: term.Coordinates{X: at.X + 1, Y: at.Y}})
	}
	sortEdits(edits)
	_, cs := h.applyEdits(edits)
	h.setSelection(h.sel.mapThrough(cs))
	h.exitSelectMode()
	return true
}

// surroundReplace is surround_replace: each delimiter of the pair
// around every range becomes the matching half of the new pair,
// nested pairs included, since every delimiter keeps the half it was
// paired with before the edits are put in document order.
func (h *helixHandlerImpl) surroundReplace(from, to rune) bool {
	pairs, ok := h.surroundPositions(surroundPair(from))
	if !ok {
		return false
	}
	openTo, closeTo := surroundPair(to)
	edits := make([]edit, 0, len(pairs))
	for i, at := range pairs {
		replacement := openTo
		if i%2 == 1 {
			replacement = closeTo
		}
		edits = append(edits, edit{
			from: at, to: term.Coordinates{X: at.X + 1, Y: at.Y}, text: string(replacement),
		})
	}
	sortEdits(edits)
	_, cs := h.applyEdits(edits)
	h.setSelection(h.sel.mapThrough(cs))
	h.exitSelectMode()
	return true
}

// sortEdits puts edits in document order, which applyEdits needs.
func sortEdits(edits []edit) {
	slices.SortStableFunc(edits, func(a, b edit) int {
		switch {
		case coordinatesBefore(a.from, b.from):
			return -1
		case coordinatesBefore(b.from, a.from):
			return 1
		}
		return 0
	})
}

// surroundBounds locates the delimiter pair enclosing the caret without
// disturbing the visible selection.
func (h *helixHandlerImpl) surroundBounds(open, closing rune) (from, to term.Coordinates, ok bool) {
	origin := h.cursor.CursorAtScroll()
	anchor, hadAnchor := h.selectionAnchor()
	h.cursor.Unselect()
	if open == closing {
		ok = h.cursor.SelectAQuote(open)
	} else {
		ok = h.cursor.SelectABlock(open, closing)
	}
	if ok {
		from, to, ok = h.cursor.SelectionRange()
	}
	h.cursor.Unselect()
	if hadAnchor {
		h.setSelectionRange(anchor, origin)
	} else {
		h.cursor.MoveToScroll(origin)
		h.anchorHere()
	}
	return from, to, ok
}

// --- increment ---

// increment applies C-a / C-x and drops select mode when a number
// actually changed, matching increment_impl's guarded exit. With the #
// register the amount grows by one per range, so a column of equal
// numbers becomes a sequence.
func (h *helixHandlerImpl) increment(by int) bool {
	step := 0
	if h.selectedRegister == sequenceRegister {
		step = by / max(1, h.motionCount())
	}
	h.selectedRegister = 0
	if !h.incrementSelection(by, step) {
		return false
	}
	h.exitSelectMode()
	return true
}

// incrementSelection is increment_impl over every range: the text a
// range covers is handed to the incrementors as it is, so a cursor on
// one digit of a number changes that digit alone, as it does in
// Helix, and a range that holds no integer or date is left as it is.
// The amount grows by step from one range to the next, which is what
// the # register asks for, whether or not the range before changed.
func (h *helixHandlerImpl) incrementSelection(by, step int) bool {
	h.carryEdits()
	buf := h.buf()
	src := h.sel
	edits := make([]edit, 0, src.len())
	ranges := slices.Clone(src.ranges)
	changed := make([]int, 0, src.len())
	amount := int64(by)
	for i, r := range src.ranges {
		next, ok := incrementText(rangeText(buf, r), amount)
		amount += int64(step)
		if !ok {
			continue
		}
		edits = append(edits, edit{from: r.from(), to: r.to(), text: next})
		changed = append(changed, i)
	}
	if len(edits) == 0 {
		return false
	}
	spans, cs := h.applyEdits(edits)
	for i := range ranges {
		ranges[i] = ranges[i].mapThrough(cs)
	}
	for j, i := range changed {
		ranges[i] = spans[j].withDirection(src.ranges[i].backward())
	}
	h.setSelection(selection{ranges: ranges, primary: src.primary})
	return true
}

// --- goto_word ---

// jumpToWord labels the visible words and waits for the two label keys.
func (h *helixHandlerImpl) jumpToWord(extend bool) bool {
	start, end := visibleBounds(h.less.Scroll())
	labels := jumpCandidates(h.buf(), h.cursor.CursorAtScroll(), start, end)
	if len(labels) == 0 {
		return false
	}
	h.jumpLabels = labels
	h.jumpExtend = extend
	h.jumpOuter = -1
	h.setMode(jumpMode)
	return true
}

// handleJump consumes the two label keys. Anything else cancels, which
// is jump_to_label's on_next_key contract.
func (h *helixHandlerImpl) handleJump(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return false, true
	}
	idx := -1
	if ev.Mod == 0 {
		idx = jumpAlphabetIndex(ev.Ch)
	}
	if idx < 0 {
		h.cancelJump()
		return false, true
	}
	if h.jumpOuter < 0 {
		outer := idx * len(jumpAlphabet)
		if outer > len(h.jumpLabels) {
			h.cancelJump()
			return false, true
		}
		h.jumpOuter = outer
		return false, true
	}

	labels, at, extend := h.jumpLabels, h.jumpOuter+idx, h.jumpExtend
	h.cancelJump()
	if at < len(labels) {
		h.applyJump(labels[at], extend)
	}
	return false, true
}

func (h *helixHandlerImpl) clearJumpLabels() {
	h.jumpLabels = nil
	h.jumpOuter = -1
	h.jumpExtend = false
}

func (h *helixHandlerImpl) cancelJump() {
	h.clearJumpLabels()
	h.setMode(normalMode)
}

func (h *helixHandlerImpl) applyJump(target rng, extend bool) {
	r := target.forward()
	if extend {
		r = target
		if from, to, ok := h.cursor.SelectionRange(); ok {
			r.anchor = extendedJumpAnchor(h.buf(), from, to, target)
		}
	}
	h.pushJump()
	h.setSelection(single(r))
}

// --- plumbing ---

// Selection satisfies tui.Handler.
func (h *helixHandlerImpl) Selection() (string, bool) {
	sel := h.cursor.Selection()
	return sel, sel != ""
}

func (h *helixHandlerImpl) cursorAtScroll() term.Coordinates {
	return h.cursor.CursorAtScroll()
}

func (h *helixHandlerImpl) setCursorAtScroll(pos term.Coordinates) bool {
	pos.Y = max(0, min(pos.Y, h.less.Buffer().Rows()-1))
	pos.X = max(0, min(pos.X, h.less.Buffer().Columns(pos.Y)))
	// Robust against resizes: only the first client interaction clears
	// this position.
	if h.less.Scroll().Width() == 0 || h.less.Scroll().SizeHeight() == 0 {
		h.pendingSetCursor = new(term.Coordinates)
		*h.pendingSetCursor = pos
		return false
	}
	_, ok := h.cursor.MoveToScroll(pos)
	h.anchor = h.cursorAtScroll()
	if h.mode() == normalMode && !h.extend {
		h.anchorHere()
	}
	h.resetSelection()
	h.markMatchingBrace()
	return ok
}

func (h *helixHandlerImpl) moveToNextLocation(ID string) bool {
	ok := h.cursor.MoveToNextLocation(ID)
	h.anchor = h.cursorAtScroll()
	h.resetSelection()
	h.markMatchingBrace()
	return ok
}

func (h *helixHandlerImpl) moveToPrevLocation(ID string) bool {
	ok := h.cursor.MoveToPrevLocation(ID)
	h.anchor = h.cursorAtScroll()
	h.resetSelection()
	h.markMatchingBrace()
	return ok
}

func (h *helixHandlerImpl) setLocationList(
	pri textapi.LocationPriority, ID string, l text.LocationList,
) {
	_ = h.cursor.SetLocationList(pri, ID, l)
}

func (h *helixHandlerImpl) OnWillSeek(from term.Coordinates) {}

func (h *helixHandlerImpl) OnDidSeek(from, to term.Coordinates) {}

func (h *helixHandlerImpl) OnWillHide(start, end int) {}

func (h *helixHandlerImpl) OnWillVisible(start int) {}

func (h *helixHandlerImpl) OnDidHide(start, end int) {
	h.config.scheduleNextTick(func() {
		h.anchor = h.cursorAtScroll()
	})
}

func (h *helixHandlerImpl) OnDidVisible(start int) {
	h.config.scheduleNextTick(func() {
		h.anchor = h.cursorAtScroll()
	})
}
