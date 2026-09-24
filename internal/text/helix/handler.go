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
	"strconv"
	"strings"

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

	// secondariesDrawn remembers that the location lists for the
	// non-primary ranges are populated, so an empty set does not
	// rebuild them on every event.
	secondariesDrawn bool

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

	// insertKeepsCaret marks the one insert entry (i) that leaves a
	// non-empty range behind with its head at the start. Escaping such
	// a range leaves the caret on the cell it was already on instead of
	// pulling it back onto the text just typed.
	insertKeepsCaret bool

	jumps   []rng
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
	h.setMode(normalMode)
	return true
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
	h.insertKeepsCaret = false
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

func (h *helixHandlerImpl) unselect() bool { return h.cursor.Unselect() }

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
			h.cursor.InsertString(str)
			h.insertRegister.WriteString(str)
		}
		return false, true
	}

	if h.pasteStarted {
		if ev.Ch != 0 {
			h.pasteBuf.WriteRune(ev.Ch)
		}
		return false, true
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
	if !h.config.cursorCorrections {
		return
	}
	if h.mode() == insertMode {
		h.cursor.MoveToBounds(1)
		return
	}
	prev := h.cursor.Coordinates()
	h.cursor.MoveToBounds(0)
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
				h.anchorHere()
				h.exitSelectMode()
			case 'c':
				h.changeSelection(false)
			case ';':
				handled = h.cursor.SwapSelectionEnd()
			case ':':
				handled = h.ensureSelectionForward()
			case 'x':
				handled = h.shrinkToLineBounds()
			case 'o':
				handled = h.expandSelection()
			case 'i':
				handled = h.shrinkSelection()
			case '`':
				handled = h.keepSelection(h.cursor.UppercaseSelection)
				h.exitSelectMode()
			case '.':
				handled = h.repeatLastMotion()
			case 'J':
				handled = h.joinSelection(true)
			case '*':
				handled = h.searchSelection(false)
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
			h.anchorHere()
		case 'x':
			handled = h.extendLineBelow()
		case 'X':
			handled = h.extendToLineBounds()
		case '%':
			handled = h.selectAll()
		case '_':
			handled = h.trimSelection()
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
			h.anchorHere()
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
			handled = h.keepSelection(h.cursor.ToggleCaseSelection)
			h.exitSelectMode()
		case '`':
			handled = h.keepSelection(h.cursor.LowercaseSelection)
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
	h.pinSelection()
	return h.cursor.ExpandSelection(context.Background())
}

func (h *helixHandlerImpl) shrinkSelection() bool {
	h.pinSelection()
	return h.cursor.ShrinkSelection()
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

// ensureSelectionForward implements A-: so the head always trails the
// anchor, which makes the following operator direction-independent.
func (h *helixHandlerImpl) ensureSelectionForward() bool {
	if !h.selectionBackward() {
		return false
	}
	return h.cursor.SwapSelectionEnd()
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
		return false, h.moveCaret(h.cursor.MoveFirstLine)
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

func (h *helixHandlerImpl) exitInsert() {
	if inserted := h.insertRegister.String(); inserted != "" {
		if err := h.writeRegister('.', clipboard.Data{
			Text: inserted, Metadata: text.NoSelection,
		}); err != nil {
			h.logError(err)
		}
	}
	h.setNormalMode()
	if !h.insertKeepsCaret {
		h.cursor.MoveLeft()
	}
	h.insertKeepsCaret = false
	h.anchorHere()
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

func (h *helixHandlerImpl) insertRegisterContents(name rune) bool {
	data, err := h.readRegister(name)
	if err != nil {
		h.logError(err)
		return false
	}
	if data.Text == "" {
		return false
	}
	h.cursor.InsertString(data.Text)
	h.insertRegister.WriteString(data.Text)
	return true
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
			if h.config.autoPair {
				h.cursor.InsertAutoPairNewline(h.config.indentRune, h.config.indentTabspaces)
			} else {
				h.cursor.InsertWithIndentRune('\n', h.config.indentRune, h.config.indentTabspaces)
			}
			h.insertRegister.WriteRune('\n')
			handled = true
		case term.KeySpace:
			h.cursor.InsertWithIndentRune(' ', h.config.indentRune, h.config.indentTabspaces)
			h.insertRegister.WriteRune(' ')
			handled = true
		case term.KeyTab:
			h.insertTabIndent()
			handled = true
		case term.KeyBackspace:
			h.backspace()
			handled = true
		case term.KeyDelete:
			h.cursor.Delete()
			handled = true
		case term.KeyEsc:
			h.exitInsert()
			handled = true
		case term.KeyArrowUp:
			h.cursor.MoveToScroll(h.anchor)
			handled = h.cursor.MoveUp()
		case term.KeyArrowRight:
			h.cursor.MoveToScroll(h.anchor)
			handled = h.cursor.MoveRight()
		case term.KeyArrowDown:
			h.cursor.MoveToScroll(h.anchor)
			handled = h.cursor.MoveDown()
		case term.KeyArrowLeft:
			h.cursor.MoveToScroll(h.anchor)
			handled = h.cursor.MoveLeft()
		case term.KeyHome:
			handled = h.cursor.MoveStartLine()
		case term.KeyEnd:
			handled = h.cursor.MoveEndLine()
		case term.KeyPgup:
			handled = h.cursor.MoveUpLines(max(1, h.less.Scroll().SizeHeight()-pagePadding))
		case term.KeyPgdn:
			handled = h.cursor.MoveDownLines(max(1, h.less.Scroll().SizeHeight()-pagePadding))
		default:
			if ev.Ch != 0 {
				if h.config.autoPair {
					h.cursor.InsertWithAutoPair(ev.Ch, h.config.indentRune, h.config.indentTabspaces)
				} else {
					h.cursor.InsertWithIndentRune(ev.Ch, h.config.indentRune, h.config.indentTabspaces)
				}
				h.insertRegister.WriteRune(ev.Ch)
				handled = true
			}
		}
	case term.ModShift:
		switch ev.Key {
		case term.KeyTab:
			h.insertIndentLiteral()
			handled = true
		case term.KeyBackspace:
			handled = h.backspace()
		}
	case term.ModAlt:
		switch ev.Key {
		case term.KeyBackspace:
			handled = h.cursor.BackspaceWord()
		case term.KeyDelete:
			handled = h.deleteWordForward()
		default:
			if ev.Ch == 'd' {
				handled = h.deleteWordForward()
			}
		}
	case term.ModCtrl:
		switch ev.Ch {
		case 'c':
			h.exitInsert()
			handled = true
		case 'h':
			handled = h.backspace()
		case 'w':
			handled = h.cursor.BackspaceWord()
		case 'u':
			handled = h.deleteToLineStart()
		case 'k':
			handled = h.deleteToLineEnd()
		case 'd':
			handled = h.cursor.Delete()
		case 'j':
			h.cursor.InsertWithIndentRune('\n', h.config.indentRune, h.config.indentTabspaces)
			h.insertRegister.WriteRune('\n')
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

// --- selection grammar ---

// Helix's selection grammar, mirroring helix-core/src/movement.rs and
// Range::put_cursor in helix-core/src/selection.rs.
//
// Every range is an (anchor, head) pair and is never empty: Helix stores
// selections through Selection::ensure_invariants, which widens a
// collapsed range to one grapheme. text.Cursor with
// RightInclusiveSemantics has the same shape, so a motion is a matter of
// deciding where the anchor lands before moving the head.
//
// In normal mode put_cursor drops a fresh anchor on the landing cell; in
// select mode the existing anchor survives. Word motions additionally
// re-anchor one cell along when the caret already sits on the boundary
// the motion looks for, which is what makes repeated presses walk
// forward instead of standing still (word_move's `head == head_start`
// branch).

// anchorHere drops a fresh anchor on the caret, collapsing the selection
// to the single cell under it.
func (h *helixHandlerImpl) anchorHere() {
	h.cursor.Unselect()
	h.cursor.Select()
}

// setSelectionRange rebuilds the selection as the (anchor, head) pair,
// leaving the caret on head. The anchor is placed without seeking so
// the viewport only ever follows the caret.
func (h *helixHandlerImpl) setSelectionRange(anchor, head term.Coordinates) {
	h.cursor.Unselect()
	h.cursor.SetCursorAtScroll(anchor)
	h.cursor.Select()
	h.cursor.MoveToScroll(head)
}

// selectionAnchor returns the cell the selection is anchored on.
func (h *helixHandlerImpl) selectionAnchor() (term.Coordinates, bool) {
	anchor, _, ok := h.cursor.SelectionBounds()
	return anchor, ok
}

func (h *helixHandlerImpl) selectionBackward() bool {
	anchor, head, ok := h.cursor.SelectionBounds()
	return ok && coordinatesBefore(head, anchor)
}

// runMotion applies step count times. Select mode restores the original
// anchor afterwards, matching extend_word_impl: the head is computed as
// if the motion were unextended, then put back on the old anchor.
func (h *helixHandlerImpl) runMotion(step func() bool) bool {
	return h.applyMotion(step, h.motionCount())
}

// runMotionOnce is for motions that consume the count themselves.
func (h *helixHandlerImpl) runMotionOnce(step func() bool) bool {
	return h.applyMotion(step, 1)
}

func (h *helixHandlerImpl) applyMotion(step func() bool, times int) bool {
	run := func() bool {
		moved := false
		for range max(1, times) {
			if !step() {
				break
			}
			moved = true
		}
		return moved
	}
	if !h.extend {
		return run()
	}
	if _, ok := h.cursor.SelectionMode(); !ok {
		h.cursor.Select()
	}
	anchor, ok := h.selectionAnchor()
	if !ok {
		return run()
	}
	moved := run()
	h.setSelectionRange(anchor, h.cursor.CursorAtScroll())
	return moved
}

// moveCaret runs a motion that leaves a single-cell selection behind in
// normal mode: put_cursor(extend=false) collapses to the landing cell.
func (h *helixHandlerImpl) moveCaret(fn func() bool) bool {
	return h.runMotion(h.caretStep(fn))
}

// moveCaretOnce is moveCaret for motions that apply the count inside fn.
func (h *helixHandlerImpl) moveCaretOnce(fn func() bool) bool {
	return h.runMotionOnce(h.caretStep(fn))
}

func (h *helixHandlerImpl) caretStep(fn func() bool) func() bool {
	return func() bool {
		h.cursor.Unselect()
		ok := h.moved(fn)
		h.anchorHere()
		return ok
	}
}

// moved runs a cursor primitive and reports whether the caret actually
// changed position. The primitives disagree on what their boolean
// means: Cursor.MoveRightEndWord returns false under
// RightInclusiveSemantics even when it moved, so position is the only
// reliable signal.
func (h *helixHandlerImpl) moved(fn func() bool) bool {
	before := h.cursor.CursorAtScroll()
	fn()
	return h.cursor.CursorAtScroll() != before
}

// --- selection set ---

// The handler keeps the selection set in document space and text.Cursor
// mirrors only the primary. readRange and installRange are the two
// bridges between them; everything else goes through setSelection so
// the set is always normalized and the primary always installed.
//
// A linewise cursor selection is the Helix range from the start of its
// first line to the start of the line after its last, which is what x
// and extend_line_below build there, and any range of that shape is
// installed back as a linewise selection so d, y and p keep their
// whole-line semantics.

// adoptCursor makes whatever selection the cursor holds the whole
// selection set. It runs after every event, which is what turns a
// pinned selection (a text object, select all, a syntax expansion) back
// into an anchor/head pair the next motion can extend.
func (h *helixHandlerImpl) adoptCursor() {
	h.setSelection(single(h.readRange()))
}

// setSelection replaces the selection set and installs its primary.
func (h *helixHandlerImpl) setSelection(s selection) {
	h.sel = h.ensureInvariants(s.normalized())
	h.syncPrimary()
}

// ensureInvariants is Selection::ensure_invariants: outside insert mode
// every range covers at least one cell. A caret on a line ending stays
// a point: the cell model has no cell there to widen onto, so the
// one-cell selection the cursor shows on an empty row covers no text.
func (h *helixHandlerImpl) ensureInvariants(s selection) selection {
	if h.currMode == insertMode {
		return s
	}
	buf := h.buf()
	return s.transform(func(r rng) rng {
		if !r.isPoint() {
			return r
		}
		if next, ok := nextPos(buf, r.head); ok && next.Y == r.head.Y {
			r.head = next
		}
		return r
	})
}

// syncPrimary installs the primary range into the cursor and refreshes
// the highlight of the other ranges.
func (h *helixHandlerImpl) syncPrimary() {
	before := h.cursor.CursorAtScroll()
	h.installRange(h.sel.primaryRange())
	if h.cursor.CursorAtScroll() != before {
		h.anchor = h.cursorAtScroll()
	}
	h.markSecondaries()
}

// markSecondaries draws every non-primary range through two location
// lists: the covered cells in reverse and, on top, the cell the range's
// cursor sits on dimmed so it reads as a ghost caret.
func (h *helixHandlerImpl) markSecondaries() {
	if h.sel.len() < 2 {
		if h.secondariesDrawn {
			h.cursor.SetLocationList(textapi.LocationPriorityCritical, selectionsLocID, nil)
			h.cursor.SetLocationList(textapi.LocationPriorityCritical, cursorsLocID, nil)
			h.secondariesDrawn = false
		}
		return
	}
	buf := h.buf()
	var sels, carets []textapi.Location
	for i, r := range h.sel.ranges {
		if i == h.sel.primary {
			continue
		}
		sels = append(sels, rowLocations(buf, r, term.Attributes{Attrs: term.AttrReverse})...)
		c := clampCell(buf, r.cursor(buf))
		carets = append(carets, textapi.Location{
			From: c, To: term.Coordinates{X: c.X + 1, Y: c.Y},
			Attr: term.Attributes{Attrs: term.AttrReverse | term.AttrDim},
		})
	}
	h.cursor.SetLocationList(textapi.LocationPriorityCritical, selectionsLocID,
		textapi.LocationSlice(sels))
	h.cursor.SetLocationList(textapi.LocationPriorityCritical, cursorsLocID,
		textapi.LocationSlice(carets))
	h.secondariesDrawn = true
}

// rowLocations splits a range into one location per row it covers.
func rowLocations(buf *cell.Buffer, r rng, attr term.Attributes) []textapi.Location {
	from, to := r.from(), r.to()
	var locs []textapi.Location
	for y := from.Y; y <= to.Y && y < buf.Rows(); y++ {
		start := term.Coordinates{Y: y}
		if y == from.Y {
			start.X = from.X
		}
		end := term.Coordinates{Y: y, X: buf.Columns(y)}
		if y == to.Y {
			end.X = min(to.X, end.X)
		}
		if end.X <= start.X {
			continue
		}
		locs = append(locs, textapi.Location{From: start, To: end, Attr: attr})
	}
	return locs
}

// readRange reads the cursor's selection as a Helix range in document
// space, where head is the exclusive edge of the range. Without a
// selection the caret is a point.
func (h *helixHandlerImpl) readRange() rng {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		caret := h.cursor.CursorAtScroll()
		return rng{anchor: caret, head: caret}
	}
	buf := h.buf()
	if mode, _ := h.cursor.SelectionMode(); mode == text.LineSelection {
		from = term.Coordinates{Y: from.Y}
		to = term.Coordinates{Y: min(to.Y+1, buf.Rows())}
	} else {
		to = docTo(buf, to)
	}
	if h.selectionBackward() {
		return rng{anchor: to, head: from}
	}
	return rng{anchor: from, head: to}
}

// docTo maps the far edge of a cell selection into document space. The
// cursor reports it one column past the caret, which for a caret on the
// line-ending slot lands past the last position the row has; that edge
// is the line ending itself, so the range covers no more than the row.
func docTo(buf *cell.Buffer, to term.Coordinates) term.Coordinates {
	if to.Y < 0 || to.Y >= buf.Rows() || to.X <= buf.Columns(to.Y) {
		return to
	}
	return term.Coordinates{X: buf.Columns(to.Y), Y: to.Y}
}

// lineShaped reports whether r runs from a line start to a line start,
// which is the shape a linewise selection reads back as.
func lineShaped(r rng) bool {
	from, to := r.from(), r.to()
	return from.X == 0 && to.X == 0 && to.Y > from.Y
}

// installRange makes r the cursor's selection. It is a no-op when the
// cursor already holds exactly that, so re-installing after every event
// costs nothing on the common path. Outside insert mode a point is the
// one-cell selection anchored where the caret is, line-ending slot
// included: the next event's bounds correction clamps it, as it does
// for a caret the IDE parks there.
func (h *helixHandlerImpl) installRange(r rng) {
	buf := h.buf()
	if buf.Rows() == 0 {
		return
	}
	anchor, head := r.anchor, r.head
	var anchorCell, caret term.Coordinates
	mode := text.StandardSelection
	switch {
	case r.isPoint() && h.currMode == insertMode:
		caret = clampInsert(buf, head)
		if _, ok := h.cursor.SelectionMode(); !ok && h.cursor.CursorAtScroll() == caret {
			return
		}
		h.cursor.Unselect()
		h.cursor.MoveToScroll(caret)
		return
	case r.isPoint():
		anchorCell = clampInsert(buf, head)
		caret = anchorCell
	case lineShaped(r):
		mode = text.LineSelection
		first, last := r.from().Y, r.to().Y-1
		lastCell := clampCell(buf, term.Coordinates{X: buf.Columns(last), Y: last})
		if r.backward() {
			anchorCell, caret = lastCell, term.Coordinates{Y: first}
		} else {
			anchorCell, caret = term.Coordinates{Y: first}, lastCell
		}
	case coordinatesBefore(anchor, head):
		last, ok := prevPos(buf, head)
		if !ok {
			last = anchor
		}
		anchorCell, caret = clampCell(buf, anchor), clampCell(buf, last)
	default:
		first, ok := prevPos(buf, anchor)
		if !ok {
			first = head
		}
		anchorCell, caret = clampCell(buf, first), clampCell(buf, head)
	}
	if h.cursorHolds(mode, anchorCell, caret) {
		return
	}
	if mode == text.LineSelection {
		h.cursor.Unselect()
		h.cursor.SetCursorAtScroll(anchorCell)
		h.cursor.SelectLine()
		h.cursor.MoveToScroll(caret)
		return
	}
	h.setSelectionRange(anchorCell, caret)
}

// cursorHolds reports whether the cursor's selection is exactly the
// anchor/caret pair in the given mode. A pinned selection never
// matches: its far edge is exclusive where the caret cell is not.
func (h *helixHandlerImpl) cursorHolds(mode text.SelectMode, anchor, caret term.Coordinates) bool {
	m, ok := h.cursor.SelectionMode()
	if !ok || m != mode {
		return false
	}
	a, c, ok := h.cursor.SelectionBounds()
	return ok && a == anchor && c == caret && h.cursor.CursorAtScroll() == caret
}

// clampInsert clamps a document position onto the cells the caret can
// occupy in insert mode, which includes the slot past the last cell.
func clampInsert(buf *cell.Buffer, pos term.Coordinates) term.Coordinates {
	rows := buf.Rows()
	if rows == 0 {
		return term.Coordinates{}
	}
	if pos.Y >= rows {
		return term.Coordinates{X: buf.Columns(rows - 1), Y: rows - 1}
	}
	pos.Y = max(0, pos.Y)
	pos.X = max(0, min(pos.X, buf.Columns(pos.Y)))
	return pos
}

// wordMotion implements w/W, e/E and b/B. Normal mode installs the
// range word_move produced; select mode keeps the existing anchor and
// only adopts the new cursor, which is what extend_word_impl does.
func (h *helixHandlerImpl) wordMotion(target wordMotionTarget) bool {
	r := h.readRange()
	anchor, head := wordMove(h.buf(), r.anchor, r.head, h.motionCount(), target)
	next := rng{anchor: anchor, head: head}
	if next == r {
		return false
	}
	if h.extend {
		next = r.putCursor(h.buf(), next.cursor(h.buf()), true)
	}
	h.installRange(next)
	return true
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

// moveToMatch implements n/N: the caret jumps to the match and the
// selection covers it, as search_impl does. The scan starts from the
// far edge of the current selection so a match already under the caret
// is not found again.
func (h *helixHandlerImpl) moveToMatch(dir moveMode) bool {
	return h.jumping(func() bool {
		return h.runMotion(func() bool {
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
				h.anchorHere()
				return false
			}
			h.selectMatchAtCaret()
			return true
		})
	})
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

func (h *helixHandlerImpl) pageMotion(dir int) bool {
	return h.scrollBy(dir, max(1, h.less.Scroll().SizeHeight()-pagePadding))
}

func (h *helixHandlerImpl) halfPageMotion(dir int) bool {
	return h.scrollBy(dir, max(1, h.less.Scroll().SizeHeight()/2))
}

func (h *helixHandlerImpl) scrollBy(dir, by int) bool {
	return h.moveCaret(func() bool {
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
	})
}

// --- jumplist ---

// jumpIdx mirrors helix-view's JumpList::current: it points one past the
// newest entry after a push, so the first C-o records where the caret is
// now and then steps back onto the last pushed jump.
func (h *helixHandlerImpl) currentJump() rng { return h.readRange() }

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
func (h *helixHandlerImpl) pushJumpEntry(entry rng) int {
	if h.jumpIdx >= 0 && h.jumpIdx < len(h.jumps) {
		h.jumps = h.jumps[:h.jumpIdx]
	}
	if n := len(h.jumps); n > 0 && h.jumps[n-1] == entry {
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
	if h.jumps[next] == h.currentJump() {
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

func (h *helixHandlerImpl) restoreJump(entry rng) {
	h.installRange(entry)
}

// --- operators ---

// reselectRange restores an anchor/head selection spanning the
// half-open range [from, to). Helix operators keep their selection, but
// the cursor primitives clear it, so it has to be rebuilt afterwards.
func (h *helixHandlerImpl) reselectRange(from, to term.Coordinates) {
	last := to
	if last.X > 0 {
		last.X--
	}
	h.setSelectionRange(from, last)
}

func (h *helixHandlerImpl) reselectLines(startY, endY int) {
	h.cursor.Unselect()
	h.cursor.MoveToScroll(term.Coordinates{Y: startY})
	h.cursor.SelectLine()
	if endY > startY {
		h.cursor.MoveDownLines(endY - startY)
	}
}

// keepSelection runs an in-place operator that clears the selection as
// a side effect and puts an equivalent selection back.
func (h *helixHandlerImpl) keepSelection(fn func() bool) bool {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		return fn()
	}
	mode, _ := h.cursor.SelectionMode()
	changed := fn()
	if mode == text.LineSelection {
		h.reselectLines(from.Y, to.Y)
	} else {
		h.reselectRange(from, to)
	}
	return changed
}

// keepLineSelection runs an operator that rewrites the indentation of
// the selected lines, so only the line span can be restored.
func (h *helixHandlerImpl) keepLineSelection(fn func()) bool {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		fn()
		return true
	}
	fn()
	h.reselectLines(from.Y, to.Y)
	return true
}

func (h *helixHandlerImpl) shiftSelection(right bool) bool {
	return h.keepLineSelection(func() {
		for range h.motionCount() {
			if right {
				h.cursor.ShiftSelectionRight(h.config.indentRune, h.config.indentTabspaces)
			} else {
				h.cursor.ShiftSelectionLeft(h.config.indentRune, h.config.indentTabspaces)
			}
		}
	})
}

func (h *helixHandlerImpl) formatSelection() bool {
	return h.keepLineSelection(func() {
		h.cursor.ReindentSelection(h.config.indentRune, h.config.indentTabspaces)
	})
}

func (h *helixHandlerImpl) toggleComments() bool {
	return h.keepLineSelection(func() { h.cursor.ToggleLineComment() })
}

func (h *helixHandlerImpl) yankSelection() bool {
	mode, ok := h.cursor.SelectionMode()
	if !ok {
		return false
	}
	data := clipboard.Data{Text: h.selectionText(), Metadata: mode}
	reg := h.consumeActiveRegister()
	if _, err := h.cursor.CopySelectionNoUnselect(
		registerNameToID(reg), h.config.clipboard); err != nil {
		h.logError(err)
		return false
	}
	if reg != unnamedRegister {
		if err := h.writeRegister(unnamedRegister, data); err != nil {
			h.logError(err)
		}
	}
	if reg != lastYankRegister {
		if err := h.writeRegister(lastYankRegister, data); err != nil {
			h.logError(err)
		}
	}
	return true
}

// copySelectionForDelete fills the registers Helix writes on a delete
// and reports whether the cursor's own copy-on-delete must be
// suppressed, which is what the black hole register asks for.
func (h *helixHandlerImpl) copySelectionForDelete() bool {
	reg := h.consumeActiveRegister()
	if reg == blackHoleRegister {
		return true
	}
	mode, _ := h.cursor.SelectionMode()
	data := clipboard.Data{Text: h.selectionText(), Metadata: mode}
	if reg != unnamedRegister {
		if err := h.writeRegister(reg, data); err != nil {
			h.logError(err)
		}
	}
	if err := h.writeRegister(unnamedRegister, data); err != nil {
		h.logError(err)
	}
	if mode != text.LineSelection {
		if err := h.writeRegister('-', data); err != nil {
			h.logError(err)
		}
	}
	return false
}

// deleteSelection removes the selection. Helix's Alt-d/Alt-c variants
// pass yank=false so the registers and the system clipboard are left
// untouched.
func (h *helixHandlerImpl) deleteSelection(yank bool) bool {
	if _, ok := h.cursor.SelectionMode(); !ok {
		return false
	}
	if yank {
		h.suppressCopyDelete = h.copySelectionForDelete()
	} else {
		h.selectedRegister = 0
		h.suppressCopyDelete = true
	}
	ok := h.cursor.DeleteSelection()
	h.suppressCopyDelete = false
	return ok
}

// changeSelection implements c and Alt-c. A selection that covers whole
// lines is replaced by a fresh blank line rather than collapsing the
// surrounding ones, which is delete_selection_impl's only_whole_lines
// branch.
func (h *helixHandlerImpl) changeSelection(yank bool) bool {
	mode, ok := h.cursor.SelectionMode()
	linewise := ok && mode == text.LineSelection
	deleted := h.deleteSelection(yank)
	if linewise {
		h.openLine(true)
		return deleted
	}
	h.setInsertMode()
	return deleted
}

// pasteClipboard inserts the register contents next to the selection.
// Unlike vi's visual-mode paste, Helix never replaces the selection:
// that is what R does. paste_impl anchors a characterwise paste at
// range.from()/range.to() and a linewise paste at the surrounding line
// boundaries.
func (h *helixHandlerImpl) pasteClipboard(after bool) bool {
	data, err := h.readRegister(h.consumeActiveRegister())
	if err != nil {
		h.logError(err)
		return false
	}
	if data.Text == "" {
		return false
	}
	mode, _ := data.Metadata.(text.SelectMode)

	from, to, hasSelection := h.cursor.SelectionRange()
	h.cursor.Unselect()
	if hasSelection {
		target := from
		if after {
			target = to
			if target.X > 0 {
				target.X--
			}
		}
		h.cursor.MoveToScroll(target)
	}
	// paste_impl repeats the register contents, not the insertion, so a
	// counted paste lands as one contiguous run.
	h.cursor.Paste(strings.Repeat(data.Text, h.motionCount()), mode, after)
	h.anchorHere()
	return true
}

// replaceWithYanked implements Helix's R: swap the selection for the
// register contents.
func (h *helixHandlerImpl) replaceWithYanked() bool {
	data, err := h.readRegister(h.consumeActiveRegister())
	if err != nil {
		h.logError(err)
		return false
	}
	if _, ok := h.cursor.SelectionMode(); !ok {
		return false
	}
	mode, _ := data.Metadata.(text.SelectMode)
	h.suppressCopyDelete = true
	h.cursor.Paste(data.Text, mode, false)
	h.suppressCopyDelete = false
	h.anchorHere()
	return true
}

// replaceSelection implements Helix's r<char>: every character in the
// selection becomes ch and the selection survives.
func (h *helixHandlerImpl) replaceSelection(ch rune) bool {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		return false
	}
	buf := h.less.Buffer()
	replacement := string(ch)
	replaced := false
	ctx := context.Background()
	for y := from.Y; y <= to.Y && y < buf.Rows(); y++ {
		columns := buf.Columns(y)
		startX := 0
		if y == from.Y {
			startX = from.X
		}
		endX := columns
		if y == to.Y {
			endX = min(to.X, columns)
		}
		for x := startX; x < endX; x++ {
			buf.Edit(ctx, term.Coordinates{X: x, Y: y},
				term.Coordinates{X: x + 1, Y: y}, replacement)
			replaced = true
		}
	}
	if !replaced {
		return false
	}
	if ch == '\n' {
		// Newlines change the shape of the buffer, so the original
		// range no longer describes anything meaningful.
		h.anchorHere()
		return true
	}
	h.reselectRange(from, to)
	return true
}

// joinSelection collapses every line the selection touches onto the
// first of them. selectSpace implements A-J, which leaves the inserted
// separator selected.
func (h *helixHandlerImpl) joinSelection(selectSpace bool) bool {
	from, to, ok := h.cursor.SelectionRange()
	lines := 1
	if ok {
		lines = max(1, to.Y-from.Y)
	}
	h.cursor.Unselect()
	if ok {
		h.cursor.MoveToScroll(from)
	}
	joined := false
	var lastJoin term.Coordinates
	for range lines {
		if !h.cursor.Join() {
			break
		}
		lastJoin = h.cursor.CursorAtScroll()
		joined = true
	}
	if joined && selectSpace {
		h.setSelectionRange(lastJoin, lastJoin)
		return true
	}
	h.anchorHere()
	return joined
}

// extendLineBelow implements x: the first press snaps the selection to
// whole lines, repeats grow it downwards by count lines.
func (h *helixHandlerImpl) extendLineBelow() bool {
	count := h.motionCount()
	mode, ok := h.cursor.SelectionMode()
	if ok && mode == text.LineSelection {
		return h.cursor.MoveDownLines(count)
	}
	h.cursor.Unselect()
	selected := h.cursor.SelectLine()
	if count > 1 {
		h.cursor.MoveDownLines(count - 1)
	}
	return selected
}

// extendToLineBounds implements X: snap whatever is selected out to
// whole lines without moving further.
func (h *helixHandlerImpl) extendToLineBounds() bool {
	if _, ok := h.cursor.SelectionMode(); !ok {
		return false
	}
	return h.cursor.SelectLine()
}

// shrinkToLineBounds implements A-x: drop the partially covered first
// and last lines. Selections inside a single line are left alone, which
// is what shrink_to_line_bounds does.
func (h *helixHandlerImpl) shrinkToLineBounds() bool {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		return false
	}
	mode, _ := h.cursor.SelectionMode()
	lastY := to.Y
	if to.X == 0 && lastY > from.Y {
		lastY--
	}
	if from.Y == lastY {
		return false
	}
	startY, endY := from.Y, lastY
	if from.X > 0 {
		startY++
	}
	if mode != text.LineSelection && to.X < h.less.Buffer().Columns(lastY) {
		endY--
	}
	if startY > endY {
		return false
	}
	h.reselectLines(startY, endY)
	return true
}

// trimSelection implements _: shrink the selection past the whitespace
// at either end. An all-whitespace selection collapses onto the caret,
// which is what trim_selections falls back to.
func (h *helixHandlerImpl) trimSelection() bool {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		return false
	}
	buf := h.buf()
	last, ok := prevPos(buf, to)
	if !ok {
		return false
	}
	// A linewise selection owns the separator of its last line, which
	// the cell range reports as the position just past it.
	if mode, _ := h.cursor.SelectionMode(); mode == text.LineSelection {
		last = to
	}
	start, startOK := skipSpace(buf, from, last, nextPos)
	end, endOK := skipSpace(buf, last, from, prevPos)
	if !startOK || !endOK || coordinatesBefore(end, start) {
		h.anchorHere()
		return true
	}
	// A line selection carries its separator implicitly, so there is
	// always a newline to shave off even when the range itself starts
	// and ends on non-blank cells.
	mode, _ := h.cursor.SelectionMode()
	if start == from && end == last && mode != text.LineSelection {
		return false
	}
	if h.selectionBackward() {
		h.setSelectionRange(end, start)
	} else {
		h.setSelectionRange(start, end)
	}
	return true
}

// selectAll implements %.
func (h *helixHandlerImpl) selectAll() bool {
	buf := h.less.Buffer()
	rows := buf.Rows()
	if rows == 0 {
		return false
	}
	lastY := rows - 1
	to := term.Coordinates{X: buf.Columns(lastY), Y: lastY}
	return h.cursor.SelectRange(term.Coordinates{}, to)
}

// insertBeforeSelection implements i and is also the entry point the
// Helix wrapper uses to replay the last insert for `.`.
func (h *helixHandlerImpl) insertBeforeSelection() {
	if from, _, ok := h.cursor.SelectionRange(); ok {
		h.cursor.Unselect()
		h.cursor.MoveToScroll(from)
	}
	h.setInsertMode()
	h.insertKeepsCaret = true
}

// insertAfterSelection implements a: the caret lands one cell past the
// selection, which is where Helix appends.
func (h *helixHandlerImpl) insertAfterSelection() {
	if _, to, ok := h.cursor.SelectionRange(); ok {
		h.cursor.Unselect()
		h.cursor.MoveToScroll(to)
	}
	h.setInsertMode()
}

func (h *helixHandlerImpl) insertAtLineStart() {
	h.cursor.Unselect()
	h.cursor.MoveStartLineNonBlank()
	h.setInsertMode()
}

func (h *helixHandlerImpl) insertAtLineEnd() {
	h.cursor.Unselect()
	h.cursor.MoveEndLine()
	h.setInsertMode()
}

func (h *helixHandlerImpl) openLine(above bool) {
	count := h.motionCount()
	h.cursor.Unselect()
	h.setInsertMode()
	for i := range count {
		if above && i == 0 {
			h.cursor.InsertLineAbove(h.config.indentRune, h.config.indentTabspaces)
			continue
		}
		h.cursor.InsertLineBelow(h.config.indentRune, h.config.indentTabspaces)
	}
}

// addNewline implements [<space> and ]<space>: a blank line is added
// without leaving normal mode or moving the caret.
func (h *helixHandlerImpl) addNewline(below bool) bool {
	origin := h.cursor.CursorAtScroll()
	buf := h.less.Buffer()
	at := term.Coordinates{Y: origin.Y}
	if below {
		at = term.Coordinates{Y: origin.Y, X: buf.Columns(origin.Y)}
	}
	ctx := context.Background()
	for range h.motionCount() {
		buf.Edit(ctx, at, at, "\n")
	}
	target := origin
	if !below {
		target.Y += h.motionCount()
	}
	h.cursor.MoveToScroll(target)
	h.anchorHere()
	return true
}

// selectTextObject implements the mi/ma pairs.
func (h *helixHandlerImpl) selectTextObject(ch rune) bool {
	around := h.matchAround
	h.matchPending = false
	h.matchAround = false

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

// surroundAdd implements ms<char>: wrap the selection in the pair and
// keep the delimiters selected.
func (h *helixHandlerImpl) surroundAdd(ch rune) bool {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		return false
	}
	open, closing := surroundPair(ch)
	buf := h.buf()
	ctx := context.Background()
	// Insert the closing delimiter first so the opening insert cannot
	// shift the position it was computed from.
	buf.Edit(ctx, to, to, string(closing))
	buf.Edit(ctx, from, from, string(open))

	end := to
	if end.Y == from.Y {
		end.X += 2
	} else {
		end.X++
	}
	h.reselectRange(from, end)
	h.exitSelectMode()
	return true
}

// surroundDelete implements md<char>.
func (h *helixHandlerImpl) surroundDelete(ch rune) bool {
	from, to, ok := h.surroundBounds(surroundPair(ch))
	if !ok {
		return false
	}
	last := to
	if last.X == 0 {
		return false
	}
	last.X--
	buf := h.buf()
	buf.Delete(last, to)
	buf.Delete(from, term.Coordinates{Y: from.Y, X: from.X + 1})

	inner := from
	innerEnd := last
	if innerEnd.Y == from.Y {
		innerEnd.X -= 2
	} else {
		innerEnd.X--
	}
	if coordinatesBefore(innerEnd, inner) {
		h.cursor.MoveToScroll(inner)
		h.anchorHere()
		return true
	}
	h.setSelectionRange(inner, innerEnd)
	h.exitSelectMode()
	return true
}

// surroundReplace implements mr<from><to>.
func (h *helixHandlerImpl) surroundReplace(from, to rune) bool {
	start, end, ok := h.surroundBounds(surroundPair(from))
	if !ok {
		return false
	}
	openTo, closeTo := surroundPair(to)
	last := end
	if last.X == 0 {
		return false
	}
	last.X--
	ctx := context.Background()
	buf := h.buf()
	buf.Edit(ctx, last, end, string(closeTo))
	buf.Edit(ctx, start, term.Coordinates{Y: start.Y, X: start.X + 1}, string(openTo))
	h.reselectRange(start, end)
	h.exitSelectMode()
	return true
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

// increment applies C-a / C-x and drops select mode when the number
// actually changed, matching increment_impl's guarded exit.
func (h *helixHandlerImpl) increment(by int) bool {
	if !h.incrementSelection(by) {
		return false
	}
	h.exitSelectMode()
	return true
}

// incrementSelection implements C-a / C-x. Helix looks for a number
// inside the selection, or the first one starting at the caret, and
// rewrites it in place keeping the selection over the new digits.
func (h *helixHandlerImpl) incrementSelection(by int) bool {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		return false
	}
	row := from.Y
	buf := h.buf()
	if row >= buf.Rows() || row != to.Y {
		return false
	}
	line := []rune(rowString(buf, row))
	start, end, ok := numberAround(line, from.X, min(to.X, len(line)))
	if !ok {
		return false
	}
	value, err := strconv.ParseInt(string(line[start:end]), 10, 64)
	if err != nil {
		return false
	}
	next := strconv.FormatInt(value+int64(by), 10)
	// Fixed-width numbers such as 007 keep their padding.
	if padded := zeroPadded(string(line[start:end])); padded > 0 && value+int64(by) >= 0 {
		next = padLeft(next, padded)
	}
	buf.Edit(context.Background(),
		term.Coordinates{X: start, Y: row},
		term.Coordinates{X: end, Y: row}, next)
	h.reselectRange(
		term.Coordinates{X: start, Y: row},
		term.Coordinates{X: start + len([]rune(next)), Y: row})
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
	h.installRange(r)
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
	h.adoptCursor()
	h.markMatchingBrace()
	return ok
}

func (h *helixHandlerImpl) moveToNextLocation(ID string) bool {
	ok := h.cursor.MoveToNextLocation(ID)
	h.anchor = h.cursorAtScroll()
	h.adoptCursor()
	h.markMatchingBrace()
	return ok
}

func (h *helixHandlerImpl) moveToPrevLocation(ID string) bool {
	ok := h.cursor.MoveToPrevLocation(ID)
	h.anchor = h.cursorAtScroll()
	h.adoptCursor()
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
