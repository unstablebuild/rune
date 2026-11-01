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

package vi

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	sdkcomp "github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/text"
)

type viMode uint8
type moveMode uint8

const (
	normalMode viMode = iota
	insertMode
	deleteMode
	gMode
	zMode
	yankMode
	visualMode
	visualLineMode
	visualBlockMode
	replaceMode
	replaceOneMode
	searchMode // this is never set in currMode, but returned by mode()
	caseChangeMode
	shiftMode
	commentMode
)

const (
	moveNone moveMode = iota
	moveToNext
	moveToPrev
	moveTillNext
	moveTillPrev
)

var _ viHandler = (*viHandlerImpl)(nil)

type viHandler interface {
	tui.Handler

	mode() viMode
	moveToNextLocation(ID string) bool
	moveToPrevLocation(ID string) bool
	setLocationList(pri textapi.LocationPriority, ID string, l text.LocationList)
	setCursorAtScroll(pos term.Coordinates) bool
	setNormalMode() bool
	cursorAtScroll() term.Coordinates
	search(string)
	moveToBounds()
	unselect() bool
	copySuppressed() bool
	setStatusBar(bar statusBar)
}

// viHandlerImpl implements a basic vi-like text editor which satisfies tui.Handler
// and tui.Component.
type viHandlerImpl struct {
	config             viConfig
	less               handler.Less // used for search capabilities
	statusBar          statusBar
	anchor             term.Coordinates
	cursor             text.Cursor
	repeater           text.Repeater // used for block repeat only
	currMode           viMode
	moveMode           moveMode
	lastMoveMode       moveMode // stores the mode of the last f/F/t/T for ;/, repeat
	searchMode         moveMode
	moveChar           rune
	pendingRegister    bool
	pendingMacro       bool
	pendingPlayback    bool
	selectedRegister   rune
	lastPlayedRegister rune
	pendingGoMotion    bool
	textObjectPending  bool
	textObjectAround   bool
	deleteInsert       bool
	caseChangeFn       func() bool
	caseChangeRepeat   rune
	commentFn          func() bool
	commentRepeat      rune
	pendingSearchOp    *searchOpState
	pendingMarkOp      *markOpState
	shiftFn            func()
	shiftRepeat        rune
	blockRepeat        struct {
		From term.Coordinates
		To   term.Coordinates
	}
	pendingSetCursor      *term.Coordinates
	setLocations          bool
	countDigits           string
	count                 int
	operatorCount         int
	insertRegister        strings.Builder
	zRange                term.Range
	zRangeOK              bool
	pasteBuf              strings.Builder
	pasteStarted          bool
	pendingInsertRegister bool
	pendingInsertNormal   bool
	insertCompletion      insertCompletionState
	suppressCopyDelete    bool
	// paragraphAtEnd is whether the last } ran into the end of the buffer,
	// which makes it inclusive for an operator.
	paragraphAtEnd bool
	// changeLines holds the lines a linewise change replaces: the register
	// takes them whole while the delete keeps the first line's indent.
	changeLines *clipboard.Data
}

type statusBar interface {
	SetStatus(string, term.Attributes)
}

func (vi *viHandlerImpl) init(buf *cell.Buffer, cfg viConfig) {
	vi.config = cfg
	vi.statusBar = nopBar{}
	vi.less.InitWithBuffer(buf, handler.LessConfig{
		Wrap:               vi.config.wrap,
		ResAttr:            vi.config.resAttr,
		SuperimposeMessage: true,
		BarAttr:            vi.config.barAttr,
		MessageLayout:      vi.config.messageBarLayout,
		Attributes:         vi.config.attr,
	})
	vi.less.Scroll().SetTabspaces(vi.config.tabspaces)
	scroll := vi.less.Scroll()
	scroll.Attributes = vi.config.attr
	scroll.ResultsAttr = vi.config.resAttr
	scroll.Subscribe(vi)
	vi.cursor.Init(vi.less.Scroll(), vi.config.scheduleNextTick)
	vi.cursor.RightInclusiveSemantics = true
	vi.cursor.VimWords = true
	vi.repeater.Init(&vi.cursor, buf)

	vi.anchor = vi.cursorAtScroll()

	vi.setMode(normalMode)
	vi.resetCount()
}

func (vi *viHandlerImpl) initWithScroll(scroll *component.Scroll, opts ...Option) {
	vi.config = defaultviHandlerImplConfig()
	for _, o := range opts {
		o(&vi.config)
	}
	vi.statusBar = nopBar{}
	vi.less.InitWithScroll(scroll, handler.LessConfig{
		Wrap:               vi.config.wrap,
		ResAttr:            vi.config.resAttr,
		SuperimposeMessage: true,
		BarAttr:            vi.config.barAttr,
		MessageLayout:      vi.config.messageBarLayout,
		Attributes:         vi.config.attr,
	})
	vi.less.Scroll().SetTabspaces(vi.config.tabspaces)
	// do not initialize repeater, as we don't know if scroll
	// was initialized with subscription functionality.
	// vi.repeater.Init(&vi.cursor, scroll.Buffer())
	vi.cursor.InitPerformance(vi.less.Scroll())
	vi.cursor.RightInclusiveSemantics = true
	vi.cursor.VimWords = true
	vi.anchor = vi.cursorAtScroll()
	vi.setMode(normalMode)
	vi.resetCount()
}

func (vi *viHandlerImpl) setStatusBar(msg statusBar) {
	vi.statusBar = msg
	vi.setMode(vi.mode())
}

// Resize satisfies tui.Component
func (vi *viHandlerImpl) Resize(width, height int) {
	vi.less.Resize(width, height)
	if vi.pendingSetCursor != nil {
		pos := *vi.pendingSetCursor
		vi.setCursorAtScroll(pos)
		vi.pendingSetCursor = nil
	}
}

func (vi *viHandlerImpl) setActiveLocationListMessage(locs []textapi.Location) {
	// NOTE: if therea re multiple location lists with a message
	// in current cursor position, then there's no guarantee of which one
	// is going to be rendered.
	for _, loc := range locs {
		if loc.Message != "" {
			vi.less.SetMessage("%s", loc.Message)
			return
		}
	}
	vi.less.SetMessage("")
}

func (vi *viHandlerImpl) drawLocationMessage() {
	locs, ok := vi.cursor.LocationsAtCursor()
	if ok {
		vi.setActiveLocationListMessage(locs)
		vi.setLocations = true
	} else if vi.setLocations {
		vi.less.SetMessage("")
		vi.setLocations = false
	}
}

// Draw satisfies tui.Component
func (vi *viHandlerImpl) Draw(w term.Writer) {
	vi.drawLocationMessage()
	vi.less.Draw(w)
	locations := vi.cursor.SortedLocations()
	text.DrawLocations(locations, vi.less.Scroll(), w)
}

// Cursor satisfies tui.Handler
func (vi *viHandlerImpl) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	var style term.CursorStyle
	switch vi.mode() {
	case searchMode:
		return vi.less.Cursor()
	case insertMode:
		style = term.CursorStyleSteadyBar
	case zMode, gMode, yankMode, deleteMode, replaceMode, replaceOneMode:
	case caseChangeMode, shiftMode, commentMode:
		style = term.CursorStyleSteadyUnderline
	case normalMode, visualMode, visualLineMode, visualBlockMode:
		style = term.CursorStyleDefault
	default:
		panic(fmt.Sprintf("unknown mode: %d", vi.currMode))
	}
	return vi.cursor.Coordinates(), style, true
}

func (vi *viHandlerImpl) setMode(mode viMode) {
	var text string
	var attrs term.Attributes
	switch mode {
	case normalMode, gMode, replaceOneMode, zMode:
		text = " NORMAL"
	case insertMode:
		text = " INSERT"
		attrs = term.Attributes{Bg: term.ColorGreen, Fg: term.ColorBlack}
	case deleteMode:
		text = " DELETE"
		attrs = term.Attributes{Bg: term.ColorRed, Fg: term.ColorBlack}
	case yankMode:
		text = "  YANK "
		attrs = term.Attributes{Bg: term.ColorFuchsia, Fg: term.ColorBlack}
	case caseChangeMode:
		text = "  CASE "
		attrs = term.Attributes{Bg: term.ColorFuchsia, Fg: term.ColorBlack}
	case shiftMode:
		text = " SHIFT "
		attrs = term.Attributes{Bg: term.GetColor("orange"), Fg: term.ColorBlack}
	case commentMode:
		text = "COMMENT"
		attrs = term.Attributes{Bg: term.ColorLime, Fg: term.ColorBlack}
	case visualMode:
		text = " VISUAL"
		attrs = term.Attributes{Bg: term.ColorBlue, Fg: term.ColorBlack}
	case visualLineMode:
		text = " V-LINE"
		attrs = term.Attributes{Bg: term.ColorTeal, Fg: term.ColorBlack}
	case visualBlockMode:
		text = "V-BLOCK"
		attrs = term.Attributes{Bg: term.ColorAqua, Fg: term.ColorBlack}
	case replaceMode:
		text = "REPLACE"
		attrs = term.Attributes{Bg: term.ColorYellow, Fg: term.ColorBlack}
	case searchMode:
		text = " SEARCH"
		attrs = term.Attributes{Bg: term.ColorSilver, Fg: term.ColorBlack}
	default:
		panic(fmt.Sprintf("unknown mode: %v", mode))
	}
	vi.statusBar.SetStatus(text, attrs)
	vi.currMode = mode
}

func (vi *viHandlerImpl) setNormalMode() bool {
	if vi.less.Mode() == handler.LessSearchMode {
		vi.less.SetNormalMode()
	}

	vi.resetCount()
	vi.setMode(normalMode)
	vi.moveMode = moveNone
	vi.pendingRegister = false
	vi.pendingPlayback = false
	vi.pendingGoMotion = false
	vi.textObjectPending = false
	vi.pendingSearchOp = nil
	vi.pendingMarkOp = nil
	return true
}

// recordVisualMarksFromSnapshot persists snapshotted selection bounds
// as the `<` / `>` visual marks. It is a no-op when no snapshot was
// captured. Used by handleVisual to record bounds before operators
// that internally clear the selection (yank, delete, etc.).
func (vi *viHandlerImpl) recordVisualMarksFromSnapshot(
	had bool, from, to term.Coordinates,
) {
	if !had {
		return
	}
	vi.cursor.SetLocationList(textapi.LocationPriorityInfo,
		visualSelectionStartMarkID,
		textapi.LocationSlice([]textapi.Location{{
			From: from,
			To:   term.Coordinates{Y: from.Y, X: from.X + 1},
		}}))
	vi.cursor.SetLocationList(textapi.LocationPriorityInfo,
		visualSelectionEndMarkID,
		textapi.LocationSlice([]textapi.Location{{
			From: to,
			To:   term.Coordinates{Y: to.Y, X: to.X + 1},
		}}))
}

func (vi *viHandlerImpl) beginOperatorPending() {
	vi.operatorCount = max(1, vi.count)
	vi.count = 1
	vi.countDigits = ""
}

func (vi *viHandlerImpl) setInsertMode() {
	vi.repeater.Clear()
	vi.insertRegister.Reset()
	vi.blockRepeat.From = term.Coordinates{}
	vi.blockRepeat.To = term.Coordinates{}
	vi.textObjectPending = false
	vi.pendingInsertRegister = false
	vi.pendingInsertNormal = false
	vi.resetInsertCompletion()
	vi.setMode(insertMode)
	vi.less.SetMessage("")
	vi.resetCount()
}

func (vi *viHandlerImpl) setDeleteMode(thenInsert bool) {
	vi.beginOperatorPending()
	vi.setMode(deleteMode)
	vi.deleteInsert = thenInsert
	vi.pendingGoMotion = false
	vi.textObjectPending = false
	vi.less.SetMessage("")
}

func (vi *viHandlerImpl) setGMode() {
	vi.setMode(gMode)
}

const foldHighlightLocationListID = "_foldHighlightID"

// visualSelectionStartMarkID and visualSelectionEndMarkID hold the
// start (`<`) and end (`>`) of the last visual selection. They are
// populated whenever vi exits a visual mode and are consumed by the
// `'<`/“ `< “/`'>`/“ `> “ keybindings via the standard
// location-jump command. Vim records these for any visual operation
// (operator, <esc>, motion-driven exit), so we update them on every
// transition out of a visual mode.
const (
	visualSelectionStartMarkID = "<"
	visualSelectionEndMarkID   = ">"
)

func (vi *viHandlerImpl) setZMode() {
	vi.setMode(zMode)
	pos := vi.cursor.CursorAtScroll()
	vi.zRange = term.Range{Start: pos, End: pos}
	vi.zRangeOK = true
	vi.highlightZRange()
	vi.cursor.FoldAt(context.Background(), func(fold term.Range, ok bool) {
		if !ok {
			return
		}
		vi.zRange = fold
		vi.zRangeOK = true
		vi.highlightZRange()
	})
}

func (vi *viHandlerImpl) highlightZRange() {
	if !vi.zRangeOK || vi.zRange.Start == vi.zRange.End {
		vi.clearZRangeHighlight()
		return
	}
	foldHighlightAttr := term.Attributes{Bg: term.ColorGray}
	vi.cursor.SetLocationList(
		textapi.LocationPriorityInfo, foldHighlightLocationListID,
		text.LocationSlice([]textapi.Location{
			{From: vi.zRange.Start, To: vi.zRange.End, Attr: foldHighlightAttr},
		}))
}

func (vi *viHandlerImpl) clearZRangeHighlight() {
	vi.cursor.SetLocationList(textapi.LocationPriorityInfo, foldHighlightLocationListID, nil)
}

func (vi *viHandlerImpl) selectZRange() bool {
	if !vi.zRangeOK || vi.zRange.Start == vi.zRange.End {
		return false
	}
	if !vi.cursor.SelectRange(vi.zRange.Start, vi.zRange.End) {
		return false
	}
	vi.setMode(visualMode)
	return true
}

func (vi *viHandlerImpl) setYankMode() {
	vi.beginOperatorPending()
	vi.pendingGoMotion = false
	vi.textObjectPending = false
	vi.setMode(yankMode)
}

func (vi *viHandlerImpl) setCaseChangeMode(fn func() bool, repeat rune) {
	vi.beginOperatorPending()
	vi.pendingGoMotion = false
	vi.textObjectPending = false
	vi.caseChangeFn = fn
	vi.caseChangeRepeat = repeat
	vi.setMode(caseChangeMode)
}

func (vi *viHandlerImpl) setShiftMode(fn func(), repeat rune) {
	vi.beginOperatorPending()
	vi.pendingGoMotion = false
	vi.textObjectPending = false
	vi.shiftFn = func() { vi.withoutRegisters(fn) }
	vi.shiftRepeat = repeat
	vi.setMode(shiftMode)
}

func (vi *viHandlerImpl) setCommentMode(fn func() bool, repeat rune) {
	vi.beginOperatorPending()
	vi.pendingGoMotion = false
	vi.textObjectPending = false
	vi.commentFn = fn
	vi.commentRepeat = repeat
	vi.setMode(commentMode)
}

func (vi *viHandlerImpl) setVisualMode() {
	if vi.cursor.Select() {
		vi.textObjectPending = false
		vi.setMode(visualMode)
	}
}

func (vi *viHandlerImpl) setVisualLineMode() {
	if vi.cursor.SelectLine() {
		vi.textObjectPending = false
		vi.setMode(visualLineMode)
	}
}

func (vi *viHandlerImpl) setVisualBlockMode() {
	if vi.cursor.SelectBlock() {
		vi.textObjectPending = false
		vi.setMode(visualBlockMode)
	}
}

func (vi *viHandlerImpl) setTextObjectPending(around bool) {
	vi.textObjectPending = true
	vi.textObjectAround = around
}

func (vi *viHandlerImpl) clearTextObjectPending() {
	vi.textObjectPending = false
	vi.textObjectAround = false
}

func (vi *viHandlerImpl) selectTextObject(ch rune) bool {
	around := vi.textObjectAround
	vi.clearTextObjectPending()

	switch ch {
	case 'w':
		if around {
			return vi.cursor.SelectAWords(vi.motionCount())
		}
		return vi.cursor.SelectInnerWords(vi.motionCount())
	case 'W':
		if around {
			return vi.cursor.SelectAWordGroups(vi.motionCount())
		}
		return vi.cursor.SelectInnerWordGroups(vi.motionCount())
	case 's':
		if around {
			return vi.cursor.SelectASentence()
		}
		return vi.cursor.SelectInnerSentence()
	case 'p':
		if around {
			return vi.cursor.SelectAParagraph()
		}
		return vi.cursor.SelectInnerParagraph()
	case '"', '\'', '`':
		if around {
			return vi.cursor.SelectAQuote(ch)
		}
		return vi.cursor.SelectInnerQuote(ch)
	case '(', ')', 'b':
		if around {
			return vi.cursor.SelectABlock('(', ')')
		}
		return vi.cursor.SelectInnerBlock('(', ')')
	case '{', '}', 'B':
		if around {
			return vi.cursor.SelectABlock('{', '}')
		}
		return vi.cursor.SelectInnerBlock('{', '}')
	case '[', ']':
		if around {
			return vi.cursor.SelectABlock('[', ']')
		}
		return vi.cursor.SelectInnerBlock('[', ']')
	case '<', '>', 't':
		if around {
			return vi.cursor.SelectABlock('<', '>')
		}
		return vi.cursor.SelectInnerBlock('<', '>')
	default:
		return false
	}
}

func (vi *viHandlerImpl) setMoveToCharacterMode(mode moveMode) {
	vi.moveMode = mode
	vi.lastMoveMode = mode
}

// nudgeCursorForTillRepeat moves the cursor one position in the search
// direction before repeating a till motion with ; or ,. This prevents
// the repeat from finding the same character the cursor is sitting next
// to and making no progress.
func (vi *viHandlerImpl) nudgeCursorForTillRepeat(mode moveMode) {
	switch mode {
	case moveTillNext:
		vi.cursor.MoveRight()
	case moveTillPrev:
		vi.cursor.MoveLeft()
	}
}

// reverseDirection returns the opposite direction for a moveMode,
// preserving whether it is a find or till motion.
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

func (vi *viHandlerImpl) setReplaceMode() {
	vi.setMode(replaceMode)
}

func (vi *viHandlerImpl) setReplaceOneMode() {
	vi.setMode(replaceOneMode)
}

func (vi *viHandlerImpl) handleSearch(ev term.Event) (bool, bool) {
	switch ev.Mod {
	case 0:
		switch ev.Key {
		case term.KeyEnter:
			text := vi.less.SearchText()
			vi.less.SetNormalMode()
			if text == "" {
				vi.less.SetMessage("")
			} else {
				vi.less.SetMessage("searching '%s'", text)
			}
			vi.search(text)
			if err := vi.writeRegister('/', clipboard.Data{Text: text}); err != nil {
				vi.logError(err)
			}
			return false, true
		}
	}
	return vi.less.Handle(ev)
}

func (vi *viHandlerImpl) logError(err error) {
	log.WithField(logging.KeyClass, "vi.handler").Error(err)
}

func (vi *viHandlerImpl) macroPlaybackActive() bool {
	return vi.config.macroPlayer != nil && vi.config.macroPlayer.IsPlaying()
}

func (vi *viHandlerImpl) activeRegister() rune {
	if vi.selectedRegister != 0 {
		return vi.selectedRegister
	}
	return unnamedRegister
}

func (vi *viHandlerImpl) consumeActiveRegister() rune {
	name := vi.activeRegister()
	vi.selectedRegister = 0
	return name
}

func (vi *viHandlerImpl) readRegister(name rune) (clipboard.Data, error) {
	return vi.config.clipboard.Paste(registerNameToID(name))
}

func (vi *viHandlerImpl) writeRegister(name rune, data clipboard.Data) error {
	return vi.config.clipboard.Copy(registerNameToID(name), data)
}

func (vi *viHandlerImpl) pasteClipboard(after bool) bool {
	paste, err := vi.readRegister(vi.consumeActiveRegister())
	if err != nil {
		vi.logError(fmt.Errorf("clipboard.Get: %s", err))
		return false
	}
	str := paste.Text

	// if not ok, zero value of mode is accepted
	// and interpreted by cursor.
	mode, _ := paste.Metadata.(text.SelectMode)

	// Pasting on visual selection will first delete it before inserting the pasted
	// text. cursor.DeleteSelection sets the cursor mode to NoSelection so the state
	// that called replacing from (text.LineSelection or others) is lost.
	//
	// This does not happen if no deletion happens between the yanking and the pasting,
	// in other words: if the user is not replacing but simply yank-pasting.
	if vi.currMode == visualLineMode {
		mode = text.LineSelection
	}
	charwise := mode == text.StandardSelection || (mode == text.NoSelection && !strings.HasSuffix(str, "\n"))

	if count := max(1, vi.count); count > 1 && mode != text.BlockSelection {
		if len(str) > maxPutLen/count {
			vi.logError(fmt.Errorf("put: %d copies of %d bytes is too long", count, len(str)))
			return false
		}
		str = strings.Repeat(str, count)
	}

	// A multi-line charwise put leaves the cursor at the start of the text.
	start := vi.cursor.CursorAtScroll()
	if from, to, ok := vi.cursor.SelectionBounds(); ok {
		start, _ = term.CoordinatesSort(from, to)
	} else if after && start.Y < vi.less.Buffer().Rows() && vi.less.Buffer().Columns(start.Y) > 0 {
		start.X++
	}

	vi.cursor.Paste(str, mode, after)
	switch {
	case !charwise:
		if mode != text.BlockSelection {
			// A linewise put leaves it on the first non-blank it put.
			vi.cursor.MoveStartLineNonBlank()
		}
	case str == "":
	case strings.Contains(str, "\n"):
		vi.cursor.MoveToScroll(start)
	default:
		// One that fits on a line leaves it on the last character.
		vi.cursor.MoveLeft()
	}
	return true
}

// maxPutLen bounds the text a counted put inserts.
const maxPutLen = 1 << 26

func (vi *viHandlerImpl) pasteClipboardLeaveCursorAfter(after bool) bool {
	paste, err := vi.readRegister(vi.consumeActiveRegister())
	if err != nil {
		vi.logError(fmt.Errorf("clipboard.Get: %s", err))
		return false
	}

	str := paste.Text
	mode, _ := paste.Metadata.(text.SelectMode)
	if mode == text.NoSelection {
		if strings.HasSuffix(str, "\n") {
			mode = text.LineSelection
		} else {
			mode = text.StandardSelection
		}
	}

	count := max(1, vi.count)
	orig := vi.cursorAtScroll()
	origLastRow := vi.less.Buffer().Rows() - 1

	for range count {
		vi.cursor.Paste(str, mode, after)
	}

	pos := vi.cursorAtScroll()
	if mode == text.LineSelection {
		insertedRows := strings.Count(str, "\n")
		if insertedRows == 0 {
			insertedRows = 1
		}
		targetY := orig.Y + insertedRows*count
		if after && orig.Y < origLastRow {
			targetY++
		}
		pos = term.Coordinates{Y: targetY}
	}

	if mode == text.BlockSelection {
		pos = vi.cursorAtScroll()
	}

	vi.setNormalMode()
	vi.setCursorAtScroll(pos)
	return true
}

const matchingLocID = "_matchingMark"

func (vi *viHandlerImpl) markMatchingBrace() {
	vi.cursor.SetLocationList(textapi.LocationPriorityInfo, matchingLocID, nil)
	c, ok := vi.cursor.Cell()
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
	case '<':
		matching = '>'
	case '}':
		matching = '{'
		end = true
	case ')':
		matching = '('
		end = true
	case ']':
		matching = '['
		end = true
	case '>':
		matching = '<'
		end = true
	default:
		return
	}

	vi.markMatchingBraceViaCursor(c.Ch, matching, end)
}

func (vi *viHandlerImpl) markMatchingBraceViaCursor(target, match rune, end bool) {
	var pos term.Coordinates
	var ok bool
	if end {
		pos, ok = vi.cursor.FindMatchingRuneBackward(target, match)
	} else {
		pos, ok = vi.cursor.FindMatchingRuneForward(target, match)
	}
	if !ok {
		return
	}
	selectEnd := pos
	selectEnd.X++
	vi.cursor.SetLocationList(textapi.LocationPriorityInfo,
		matchingLocID, textapi.LocationSlice([]textapi.Location{
			{From: pos, To: selectEnd, Attr: term.Attributes{
				Attrs: term.AttrReverse,
			}},
		}))
}

const pagePadding = 2 // similar to neovim

func (vi *viHandlerImpl) handleNormal(ev term.Event) (quit, handled bool) {
	doResetCount := true
	defer func() {
		if doResetCount {
			vi.resetCount()
		}
	}()

	quit, handled = vi.handleMoveToCharacter(vi.moveMode, ev)
	if handled {
		return
	}

	if vi.pendingRegister {
		if ev.Ch == 0 && ev.Key == 0 {
			return false, true
		}
		if ev.Mod == 0 && validRegisterName(ev.Ch) {
			vi.selectedRegister = normalizedRegisterName(ev.Ch)
			vi.pendingRegister = false
			doResetCount = false
			return false, true
		}

		vi.selectedRegister = 0
		vi.pendingRegister = false
		return false, true
	}

	if vi.pendingMacro {
		if ev.Ch == 0 && ev.Key == 0 {
			return false, true
		}
		vi.pendingMacro = false
		if ev.Mod == 0 && validRegisterName(ev.Ch) && vi.config.macroRecorder != nil {
			vi.config.macroRecorder.Start(registerNameToID(normalizedRegisterName(ev.Ch)))
			return false, true
		}
		return false, true
	}

	if vi.pendingPlayback {
		if ev.Ch == 0 && ev.Key == 0 {
			return false, true
		}
		vi.pendingPlayback = false
		reg := ev.Ch
		if reg == '@' {
			// @@ replays the last-used register.
			reg = vi.lastPlayedRegister
		}
		if vi.config.macroPlayer != nil && ev.Mod == 0 && reg != 0 && validRegisterName(reg) {
			reg = normalizedRegisterName(reg)
			vi.lastPlayedRegister = reg
			count := max(1, vi.count)
			if err := vi.config.macroPlayer.Play(registerNameToID(reg), count); err != nil {
				vi.logError(fmt.Errorf("macro playback: %s", err))
			}
			return false, true
		}
		return false, true
	}

	switch ev.Mod {
	case term.ModCtrl:
		switch ev.Ch {
		case 'e':
			pos := vi.cursor.CursorAtScroll()
			if handled = vi.less.Scroll().SeekDown(); handled {
				win, _ := vi.cursor.WindowCoordinates(pos)
				if win.Y >= 0 {
					vi.cursor.SetCursorAtScroll(pos)
				}
			}
		case 'y':
			pos := vi.cursor.CursorAtScroll()
			if handled = vi.less.Scroll().SeekUp(); handled {
				win, _ := vi.cursor.WindowCoordinates(pos)
				if win.Y < vi.less.Scroll().SizeHeight() {
					vi.cursor.SetCursorAtScroll(pos)
				}
			}
		case 'f':
			scroll := vi.less.Scroll()
			if scroll.Width() == 0 || scroll.SizeHeight() == 0 {
				break
			}
			by := max(1, scroll.SizeHeight()-pagePadding)
			offset := scroll.Offset()
			if scroll.InvertOffset {
				offset.Y = max(0, offset.Y-by)
			} else {
				last, _ := scroll.ScrollToWindowCoordinates(term.Coordinates{Y: max(0, scroll.Buffer().Rows()-1)})
				if last.Y < scroll.SizeHeight() {
					by = max(0, last.Y)
				}
				offset.Y += by
			}
			if handled = scroll.SetOffset(offset); handled {
				vi.cursor.MoveToWindowTop()
			}
		case 'b':
			scroll := vi.less.Scroll()
			if scroll.Width() == 0 || scroll.SizeHeight() == 0 {
				break
			}
			by := max(1, scroll.SizeHeight()-pagePadding)
			offset := scroll.Offset()
			if scroll.InvertOffset {
				offset.Y = min(scroll.MaxSeekOffset(), offset.Y+by)
			} else {
				offset.Y = max(0, offset.Y-by)
			}
			if handled = scroll.SetOffset(offset); handled {
				vi.cursor.MoveToWindowBottom()
			}
		case 'd':
			handled = vi.cursor.MoveDownLines(vi.less.Scroll().SizeHeight() / 2)
			if handled {
				vi.cursor.RepositionTop()
			}
		case 'u':
			handled = vi.cursor.MoveUpLines(vi.less.Scroll().SizeHeight() / 2)
			if handled {
				vi.cursor.RepositionBottom()
			}
		case 'v':
			vi.setVisualBlockMode()
			handled = true
		}
	case 0:
		handled = true
		switch ev.Ch {
		case '"':
			vi.pendingRegister = true
			doResetCount = false
		case 'q':
			if vi.config.macroRecorder == nil {
				return false, false
			}
			if vi.macroPlaybackActive() {
				return false, true
			}
			if vi.config.macroRecorder.IsRecording() {
				vi.config.macroRecorder.Stop()
				return false, true
			}
			vi.pendingMacro = true
			doResetCount = false
		case '@':
			if vi.config.macroPlayer == nil {
				handled = false
				break
			}
			vi.pendingPlayback = true
			doResetCount = false
		case 'R':
			vi.setReplaceMode()
		case 'r':
			vi.setReplaceOneMode()
		case '>':
			vi.setShiftMode(
				func() { vi.cursor.ShiftSelectionRight(vi.config.indentRune, vi.config.indentTabspaces) }, '>')
			doResetCount = false
		case '<':
			vi.setShiftMode(
				func() { vi.cursor.ShiftSelectionLeft(vi.config.indentRune, vi.config.indentTabspaces) }, '<')
			doResetCount = false
		case '=':
			vi.setShiftMode(
				func() { vi.cursor.ReindentSelection(vi.config.indentRune, vi.config.indentTabspaces) }, '=')
			doResetCount = false
		case ',':
			event := term.Event{Type: term.EventKey, Ch: vi.moveChar}
			mode := reverseDirection(vi.lastMoveMode)
			start := vi.cursor.CursorAtScroll()
			vi.nudgeCursorForTillRepeat(mode)
			if _, _, found := vi.moveToCharacter(mode, event); !found {
				vi.cursor.MoveToScroll(start)
			}
		case ';':
			event := term.Event{Type: term.EventKey, Ch: vi.moveChar}
			start := vi.cursor.CursorAtScroll()
			vi.nudgeCursorForTillRepeat(vi.lastMoveMode)
			if _, _, found := vi.moveToCharacter(vi.lastMoveMode, event); !found {
				vi.cursor.MoveToScroll(start)
			}
		case 'f':
			vi.setMoveToCharacterMode(moveToNext)
			doResetCount = false
		case 'F':
			vi.setMoveToCharacterMode(moveToPrev)
			doResetCount = false
		case 't':
			vi.setMoveToCharacterMode(moveTillNext)
			doResetCount = false
		case 'T':
			vi.setMoveToCharacterMode(moveTillPrev)
			doResetCount = false
		case 'g':
			vi.setGMode()
			doResetCount = false
		case 'z':
			vi.setZMode()
			doResetCount = false
		case 'd':
			vi.setDeleteMode(false)
			doResetCount = false
		case 'c':
			vi.setDeleteMode(true)
			doResetCount = false
		case 'y':
			vi.setYankMode()
			doResetCount = false
		case 'N':
			switch vi.searchMode {
			case moveToNext:
				vi.cursor.MoveToPrevMatch()
			case moveToPrev:
				vi.cursor.MoveToNextMatch()
			}
		case 'n':
			switch vi.searchMode {
			case moveToNext:
				vi.cursor.MoveToNextMatch()
			case moveToPrev:
				vi.cursor.MoveToPrevMatch()
			}
		case 'p':
			// In Vim when pasting on a visual selection it doesn't make any difference
			// if you press "p" or "P" it will always paste before the cursor.
			pasteAfter := true
			switch vi.currMode {
			case visualMode, visualLineMode, visualBlockMode:
				pasteAfter = false
			}
			vi.pasteClipboard(pasteAfter)
		case 'P':
			vi.pasteClipboard(false)
		case '^':
			vi.cursor.MoveStartLineNonBlank()
		case '+':
			vi.moveLineNonBlank(vi.motionCount())
		case '-':
			vi.moveLineNonBlank(-vi.motionCount())
		case '_':
			vi.moveLineNonBlank(vi.motionCount() - 1)
		case '$':
			vi.cursor.MoveEndLine()
		case 'H':
			count := vi.motionCount()
			vi.cursor.MoveToWindow(term.Coordinates{X: vi.cursor.Coordinates().X, Y: count - 1})
		case 'M':
			vi.cursor.MoveToWindowMiddle()
		case 'L':
			count := vi.motionCount()
			vi.cursor.MoveToWindow(term.Coordinates{X: vi.cursor.Coordinates().X, Y: vi.less.Scroll().SizeHeight() - count})
		case 'G':
			vi.cursor.MoveToScroll(vi.anchor)
			if vi.countDigits == "" {
				vi.cursor.MoveLastLine()
			} else {
				target := max(0, min(vi.count-1, vi.less.Buffer().Rows()-1))
				vi.setCursorAtScroll(term.Coordinates{Y: target})
			}
		case 'j':
			count := vi.motionCount()
			vi.cursor.MoveToScroll(vi.anchor)
			if count == 1 {
				vi.cursor.MoveDown()
			} else {
				pos := vi.cursor.CursorAtScroll()
				maxY := vi.less.Buffer().Rows() - 1
				if count > maxY-pos.Y {
					pos.Y = maxY
				} else {
					pos.Y += count
				}
				vi.setCursorAtScroll(pos)
			}
		case 'k':
			count := vi.motionCount()
			vi.cursor.MoveToScroll(vi.anchor)
			if count == 1 {
				vi.cursor.MoveUp()
			} else {
				pos := vi.cursor.CursorAtScroll()
				pos.Y = max(0, pos.Y-count)
				vi.setCursorAtScroll(pos)
			}
		case 'h':
			count := vi.motionCount()
			if count == 1 {
				vi.cursor.MoveLeft()
			} else {
				pos := vi.cursor.CursorAtScroll()
				pos.X = max(0, pos.X-count)
				vi.cursor.MoveToScroll(pos)
			}
		case 'l':
			count := vi.motionCount()
			if count == 1 {
				vi.cursor.MoveRight()
			} else {
				pos := vi.cursor.CursorAtScroll()
				if pos.Y < vi.less.Buffer().Rows() {
					maxX := vi.less.Buffer().Columns(pos.Y)
					if count > maxX-pos.X {
						pos.X = maxX
					} else {
						pos.X += count
					}
				}
				vi.cursor.MoveToScroll(pos)
			}
		case 'O':
			vi.setInsertMode()
			vi.cursor.InsertLineAbove(vi.config.indentRune, vi.config.indentTabspaces)
		case 'o':
			vi.setInsertMode()
			vi.cursor.InsertLineBelow(vi.config.indentRune, vi.config.indentTabspaces)
		case 'i':
			vi.setInsertMode()
		case 'I':
			vi.cursor.MoveStartLineNonBlank()
			vi.setInsertMode()
		case 'J':
			vi.repeatJoin(vi.cursor.Join)
		case 'a':
			vi.cursor.MoveRight()
			vi.setInsertMode()
		case 'A':
			vi.setInsertMode()
			vi.cursor.MoveEndLine()
			vi.cursor.MoveRight()
		case 'C':
			if vi.cursor.Select() {
				vi.cursor.MoveEndLine()
				vi.deleteSelection(true)
			}
			vi.setInsertMode()
		case 'D':
			if vi.cursor.Select() {
				vi.cursor.MoveEndLine()
				vi.deleteSelection(false)
			}
		case 'X':
			if !vi.deleteChars(vi.count, true) {
				for range vi.count {
					if !vi.cursor.Backspace() {
						break
					}
				}
			}
		case 'x':
			vi.deleteChars(vi.count, false)
		case '~':
			vi.cursor.ToggleCase()
		case 's':
			vi.deleteChars(vi.count, false)
			vi.setInsertMode()
		case 'S':
			vi.changeLine()
		case 'v':
			vi.setVisualMode()
			if vi.count > 1 {
				vi.cursor.MoveRightColumns(vi.count - 1)
			}
		case 'V':
			vi.setVisualLineMode()
			if vi.count > 1 {
				vi.cursor.MoveDownLines(vi.count - 1)
			}
		case 'w':
			vi.repeatMotion(vi.cursor.MoveRightStartWord)
		case 'W':
			vi.repeatMotion(vi.cursor.MoveRightStartWordGroup)
		case 'e':
			vi.repeatMotion(vi.cursor.MoveRightEndWord)
		case 'E':
			vi.repeatMotion(vi.cursor.MoveRightEndWordGroup)
		case 'b':
			vi.repeatMotion(vi.cursor.MoveLeftStartWord)
		case 'B':
			vi.repeatMotion(vi.cursor.MoveLeftStartWordGroup)
		case '{':
			vi.cursor.MovePrevParagraphs(vi.count)
		case '}':
			vi.paragraphAtEnd = vi.moveNextParagraphs(vi.motionCount())
		case '(':
			vi.repeatMotion(vi.cursor.MovePrevSentence)
		case ')':
			vi.repeatMotion(vi.cursor.MoveNextSentence)
		case '|':
			vi.moveToScreenColumn(vi.motionCount())
		case '?':
			vi.searchMode = moveToPrev
			if !vi.config.disableSearch {
				vi.less.Handle(ev)
			}
		case '/':
			vi.searchMode = moveToNext
			if !vi.config.disableSearch {
				vi.less.Handle(ev)
			}
		case '%':
			cell, _ := vi.cursor.Cell()
			switch cell.Ch {
			case '[', '{', '(':
				handled = vi.cursor.MoveToNextLocation(matchingLocID)
			case ']', '}', ')':
				handled = vi.cursor.MoveToPrevLocation(matchingLocID)
			}
		case '#':
			vi.searchMode = moveToPrev
			vi.searchWord(vi.cursor.Word())
		case '*':
			vi.searchMode = moveToNext
			vi.searchWord(vi.cursor.Word())
		default:
			switch ev.Key {
			case term.KeyEsc:
				handled = vi.setNormalMode()
				vi.cursor.Unselect()
			case term.KeyArrowUp:
				vi.cursor.MoveToScroll(vi.anchor)
				handled = vi.cursor.MoveUp()
			case term.KeyArrowRight:
				vi.cursor.MoveToScroll(vi.anchor)
				handled = vi.cursor.MoveRight()
			case term.KeyArrowDown:
				vi.cursor.MoveToScroll(vi.anchor)
				handled = vi.cursor.MoveDown()
			case term.KeyArrowLeft:
				vi.cursor.MoveToScroll(vi.anchor)
				handled = vi.cursor.MoveLeft()
			default:
				if ev.Ch == '0' && vi.countDigits == "" {
					vi.cursor.MoveStartLine()
				} else if vi.parseCountDigit(ev.Ch) {
					doResetCount = false
					return
				}
				handled = false
			}
		}
	}

	return
}

// --- Insert completion ---

type insertCompletionState struct {
	active     bool
	prefix     string
	start      term.Coordinates
	candidates []string
	index      int
	win        browserapi.Window
}

func (vi *viHandlerImpl) resetInsertCompletion() {
	if vi.insertCompletion.active {
		vi.closeInsertCompletionWindow()
	}
	vi.insertCompletion = insertCompletionState{}
}

func (vi *viHandlerImpl) closeInsertCompletionWindow() {
	wm := vi.config.windowManager
	if wm == nil || vi.insertCompletion.win == nil {
		return
	}
	if err := wm.CloseWindow(vi.insertCompletion.win); err != nil {
		vi.logError(err)
	}
	vi.insertCompletion.win = nil
}

func (vi *viHandlerImpl) openInsertCompletionWindow(candidates []string, focusIdx int) {
	wm := vi.config.windowManager
	if wm == nil {
		return
	}

	floating := newInsertCompletionFloating(candidates, focusIdx, func(candidate string) {
		vi.applyInsertCompletion(candidate)
	})

	pos := vi.cursorAtScroll()
	winCoords, _ := vi.cursor.WindowCoordinates(pos)

	win, err := wm.Floating(floating, browserapi.FloatingConfig{
		Alignment:   sdkcomp.AlignmentLeft | sdkcomp.AlignmentTop,
		Offset:      term.Coordinates{X: winCoords.X + 1, Y: winCoords.Y + 2},
		NoWindowBar: true,
	})
	if err != nil {
		vi.logError(err)
		return
	}
	vi.insertCompletion.win = win
}

func isInsertCompletionRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func (vi *viHandlerImpl) bufferLine(y int) string {
	buf := vi.less.Buffer()
	if y < 0 || y >= buf.Rows() {
		return ""
	}

	var line strings.Builder
	for x := range buf.Columns(y) {
		c, ok := buf.Cell(term.Coordinates{X: x, Y: y})
		if !ok {
			continue
		}
		line.WriteRune(c.Ch)
	}
	return line.String()
}

func (vi *viHandlerImpl) insertCompletionPrefix() (string, term.Coordinates, bool) {
	pos := vi.cursorAtScroll()
	line := []rune(vi.bufferLine(pos.Y))
	x := min(max(pos.X, 0), len(line))
	startX := x
	for startX > 0 && isInsertCompletionRune(line[startX-1]) {
		startX--
	}
	if startX == x {
		return "", term.Coordinates{}, false
	}
	return string(line[startX:x]), term.Coordinates{X: startX, Y: pos.Y}, true
}

func (vi *viHandlerImpl) insertCompletionCandidates(prefix string) []string {
	if prefix == "" {
		return nil
	}

	var candidates []string
	seen := make(map[string]bool)
	buf := vi.less.Buffer()
	for y := range buf.Rows() {
		line := []rune(vi.bufferLine(y))
		for x := 0; x < len(line); {
			if !isInsertCompletionRune(line[x]) {
				x++
				continue
			}

			start := x
			for x < len(line) && isInsertCompletionRune(line[x]) {
				x++
			}
			word := string(line[start:x])
			if word == prefix || !strings.HasPrefix(word, prefix) || seen[word] {
				continue
			}
			seen[word] = true
			candidates = append(candidates, word)
		}
	}
	return candidates
}

func (vi *viHandlerImpl) applyInsertCompletion(candidate string) bool {
	state := vi.insertCompletion
	end := vi.cursorAtScroll()
	if !vi.cursor.SelectRange(state.start, end) {
		return false
	}
	if !vi.cursor.DeleteSelection() {
		return false
	}
	vi.cursor.InsertString(candidate)
	return true
}

func (vi *viHandlerImpl) completeInsert(direction int) bool {
	if direction == 0 {
		return false
	}

	if !vi.insertCompletion.active {
		prefix, start, ok := vi.insertCompletionPrefix()
		if !ok {
			vi.less.SetMessage("Pattern Not Found")
			return false
		}
		candidates := vi.insertCompletionCandidates(prefix)
		if len(candidates) == 0 {
			vi.less.SetMessage("Pattern Not Found")
			return false
		}

		index := 0
		if direction < 0 {
			index = len(candidates) - 1
		}
		vi.insertCompletion = insertCompletionState{
			active:     true,
			prefix:     prefix,
			start:      start,
			candidates: candidates,
			index:      index,
		}
		ok = vi.applyInsertCompletion(candidates[index])
		if ok && len(candidates) > 1 {
			vi.openInsertCompletionWindow(candidates, index)
		}
		if ok && len(candidates) == 1 {
			vi.insertCompletion = insertCompletionState{}
		}
		return ok
	}

	state := &vi.insertCompletion
	state.index = (state.index + direction + len(state.candidates)) % len(state.candidates)
	vi.closeInsertCompletionWindow()
	ok := vi.applyInsertCompletion(state.candidates[state.index])
	if ok {
		vi.openInsertCompletionWindow(state.candidates, state.index)
	}
	return ok
}

func (vi *viHandlerImpl) resetCount() {
	vi.count = 1
	vi.countDigits = ""
	vi.operatorCount = 0
}

func (vi *viHandlerImpl) parseOperatorCountDigit(ev term.Event) bool {
	// After f/t/F/T a digit is the character to find, not a count.
	if ev.Type != term.EventKey || ev.Mod != 0 || vi.moveMode != moveNone {
		return false
	}
	if ev.Ch == '0' && vi.countDigits == "" {
		return false
	}
	return vi.parseCountDigit(ev.Ch)
}

func (vi *viHandlerImpl) parseCountDigit(ch rune) bool {
	if !unicode.IsDigit(ch) {
		return false
	}
	vi.countDigits += string(ch)
	parsedCount, err := strconv.Atoi(vi.countDigits)
	if err == nil {
		vi.count = parsedCount
		return true
	}
	if errors.Is(err, strconv.ErrRange) {
		vi.count = math.MaxInt
		return true
	}
	vi.logError(fmt.Errorf("count digits parse: %s %v", err, parsedCount))
	return false
}

func (vi *viHandlerImpl) motionCount() int {
	operatorCount := max(1, vi.operatorCount)
	motionCount := max(1, vi.count)
	if operatorCount > math.MaxInt/motionCount {
		return math.MaxInt
	}
	return operatorCount * motionCount
}

func (vi *viHandlerImpl) repeatMotion(fn func() bool) bool {
	var ok bool
	for range vi.motionCount() {
		before := vi.cursor.CursorAtScroll()
		if !fn() {
			if vi.cursor.CursorAtScroll() == before {
				break
			}
		}
		ok = true
	}
	return ok
}

// moveLineNonBlank is Vim's +, - and _: it moves dy lines down, or up when
// negative, to the first non-blank character. A count past the first or last
// line stops there, but the motion fails without moving when there is no line
// that way at all.
func (vi *viHandlerImpl) moveLineNonBlank(dy int) bool {
	rows := vi.less.Buffer().Rows()
	if rows == 0 {
		return false
	}
	y := min(vi.cursor.CursorAtScroll().Y, rows-1)
	dy = max(-rows, min(dy, rows))
	target := max(0, min(y+dy, rows-1))
	if dy != 0 && target == y {
		return false
	}
	vi.cursor.MoveToScroll(term.Coordinates{Y: target})
	vi.cursor.MoveStartLineNonBlank()
	return true
}

// moveNextParagraphs moves as Vim's } does: to the count-th blank line
// after a non-blank one or, when the buffer ends first, onto its last
// character, which it reports so operators include that character. It
// stays put when the buffer ends with paragraphs to go.
func (vi *viHandlerImpl) moveNextParagraphs(count int) (atEnd bool) {
	start := vi.cursor.CursorAtScroll()
	last := vi.less.Buffer().Rows() - 1
	for i := range count {
		vi.cursor.MoveNextParagraph()
		pos := vi.cursor.CursorAtScroll()
		if pos.Y < last || vi.blankLine(last) {
			continue
		}
		if i < count-1 {
			vi.cursor.MoveToScroll(start)
			return false
		}
		pos.X = max(0, vi.less.Buffer().Columns(last)-1)
		vi.cursor.MoveToScroll(pos)
		return true
	}
	return false
}

func (vi *viHandlerImpl) blankLine(y int) bool {
	return vi.inIndent(term.Coordinates{Y: y, X: vi.less.Buffer().Columns(y)})
}

// moveToScreenColumn moves to the cell drawn over the 1-based screen
// column col of the cursor line, or to the line's last cell when it is
// narrower, as Vim's | does.
func (vi *viHandlerImpl) moveToScreenColumn(col int) {
	pos := vi.cursor.CursorAtScroll()
	buf := vi.less.Buffer()
	if pos.Y >= buf.Rows() {
		return
	}
	row := buf.Row(pos.Y)
	tabspaces := vi.less.Scroll().Tabspaces()
	pos.X = max(0, len(row)-1)
	width := 0
	for x, cell := range row {
		if cell.Ch == '\t' {
			width += max(1, tabspaces)
		} else {
			width += max(1, int(cell.Width))
		}
		if width >= col {
			pos.X = x
			break
		}
	}
	vi.cursor.MoveToScroll(pos)
}

// repeatJoin applies a join command count-1 times, with a minimum of
// one: Vim's J counts lines to join, not joins to perform.
func (vi *viHandlerImpl) repeatJoin(fn func() bool) bool {
	var ok bool
	for range max(1, vi.motionCount()-1) {
		if !fn() {
			break
		}
		ok = true
	}
	return ok
}

// joinSelection collapses the lines spanned by the visual selection,
// with a minimum of two lines. Vim ignores any count typed in visual
// mode here: the selection alone decides how many lines are joined.
func (vi *viHandlerImpl) joinSelection(fn func() bool) bool {
	from, to, found := vi.cursor.SelectionBounds()
	if !found {
		return false
	}
	from, to = term.CoordinatesSort(from, to)
	vi.cursor.Unselect()
	vi.setCursorAtScroll(term.Coordinates{Y: from.Y})

	var ok bool
	for range max(1, to.Y-from.Y) {
		if !fn() {
			break
		}
		ok = true
	}
	return ok
}

func (vi *viHandlerImpl) selectLineCount() bool {
	count := vi.motionCount()
	// As in Vim, a count reaching past the last line selects up to it,
	// but there is nothing below the last line to count from.
	if count > 1 && vi.cursor.CursorAtScroll().Y >= vi.less.Buffer().Rows()-1 {
		return false
	}
	if !vi.cursor.SelectLine() {
		return false
	}
	for i := 1; i < count && vi.cursor.MoveLineDown(); i++ {
	}
	return true
}

func (vi *viHandlerImpl) search(text string) {
	vi.cursor.Search(text)
	switch vi.searchMode {
	case moveToNext:
		vi.cursor.MoveToNextMatch()
	case moveToPrev:
		vi.cursor.MoveToPrevMatch()
	default:
	}
}

func (vi *viHandlerImpl) searchWord(text string) {
	vi.cursor.SearchWord(text)
	switch vi.searchMode {
	case moveToNext:
		vi.cursor.MoveToNextMatch()
	case moveToPrev:
		vi.cursor.MoveToPrevMatch()
	default:
	}
}

func (vi *viHandlerImpl) insertRegisterContents(name rune) bool {
	data, err := vi.readRegister(normalizedRegisterName(name))
	if err != nil {
		vi.logError(fmt.Errorf("clipboard.Get: %s", err))
		return false
	}
	vi.cursor.InsertString(data.Text)
	vi.insertRegister.WriteString(data.Text)
	return true
}

func (vi *viHandlerImpl) startInsertNormalCommand() {
	vi.pendingInsertNormal = true
	vi.resetInsertCompletion()
	vi.setMode(normalMode)
}

func (vi *viHandlerImpl) insertNormalCommandComplete() bool {
	return vi.mode() == normalMode &&
		vi.countDigits == "" &&
		!vi.pendingRegister &&
		!vi.pendingMacro &&
		!vi.pendingPlayback &&
		vi.moveMode == moveNone &&
		!vi.pendingGoMotion &&
		!vi.textObjectPending
}

func (vi *viHandlerImpl) returnToInsertAfterNormalCommand() {
	vi.pendingInsertNormal = false
	vi.resetInsertCompletion()
	vi.setMode(insertMode)
	vi.less.SetMessage("")
	vi.resetCount()
}

func (vi *viHandlerImpl) exitInsert() {
	vi.writeDotRegister()
	vi.cursor.MoveLeft()
	vi.repeatInsertStart()
	vi.setNormalMode()
}

// insertTabIndent handles a <tab> keypress in insert mode. It either snaps
// the line up to the syntax target indent or inserts a full indent level via
// Cursor.TryIndent. When no indent service is available, it falls back to
// inserting the configured indent rune literally. The inserted material is
// recorded to vi.insertRegister so that `.`-repeat replays the keystroke.
func (vi *viHandlerImpl) insertTabIndent() {
	if vi.cursor.TryIndent(vi.config.indentRune, vi.config.indentTabspaces) {
		vi.insertRegister.WriteRune(vi.config.indentRune)
		return
	}
	vi.cursor.InsertWithIndentRune(
		vi.config.indentRune, vi.config.indentRune, vi.config.indentTabspaces)
	vi.insertRegister.WriteRune(vi.config.indentRune)
}

func (vi *viHandlerImpl) writeDotRegister() {
	str := vi.insertRegister.String()
	if str == "" {
		return
	}
	if err := vi.writeRegister('.', clipboard.Data{Text: str, Metadata: text.NoSelection}); err != nil {
		vi.logError(err)
	}
}

func (vi *viHandlerImpl) handleInsert(ev term.Event) (quit, handled bool) {
	if vi.pendingInsertRegister {
		vi.pendingInsertRegister = false
		if ev.Ch == 0 && ev.Key == 0 {
			return false, true
		}
		if ev.Mod == 0 && validRegisterName(ev.Ch) {
			return false, vi.insertRegisterContents(ev.Ch)
		}
		return false, true
	}

	keepCompletion := false
	defer func() {
		if !keepCompletion {
			vi.resetInsertCompletion()
		}
	}()

	switch ev.Mod {
	case 0:
		switch ev.Key {
		case term.KeyEnter:
			if vi.config.autoPair {
				vi.cursor.InsertAutoPairNewline(vi.config.indentRune, vi.config.indentTabspaces)
			} else {
				vi.cursor.InsertWithIndentRune('\n', vi.config.indentRune, vi.config.indentTabspaces)
			}
			vi.insertRegister.WriteRune('\n')
			handled = true
		case term.KeySpace:
			vi.cursor.InsertWithIndentRune(' ', vi.config.indentRune, vi.config.indentTabspaces)
			vi.insertRegister.WriteRune(' ')
			handled = true
		case term.KeyTab:
			vi.insertTabIndent()
			handled = true
		case term.KeyBackspace:
			if vi.config.autoPair {
				vi.cursor.BackspaceAutoPair()
			} else {
				vi.cursor.Backspace()
			}
			handled = true
		case term.KeyEsc:
			vi.exitInsert()
			handled = true
		case term.KeyArrowUp:
			vi.cursor.MoveToScroll(vi.anchor)
			vi.cursor.MoveUp()
			handled = true
		case term.KeyArrowRight:
			vi.cursor.MoveToScroll(vi.anchor)
			vi.cursor.MoveRight()
			handled = true
		case term.KeyArrowDown:
			vi.cursor.MoveToScroll(vi.anchor)
			vi.cursor.MoveDown()
			handled = true
		case term.KeyArrowLeft:
			vi.cursor.MoveToScroll(vi.anchor)
			vi.cursor.MoveLeft()
			handled = true
		default:
			if ev.Ch != 0 {
				if vi.config.autoPair {
					vi.cursor.InsertWithAutoPair(ev.Ch, vi.config.indentRune, vi.config.indentTabspaces)
				} else {
					vi.cursor.InsertWithIndentRune(ev.Ch, vi.config.indentRune, vi.config.indentTabspaces)
				}
				vi.insertRegister.WriteRune(ev.Ch)
				handled = true
			}
		}
	case term.ModCtrl:
		switch ev.Ch {
		case 'c':
			vi.exitInsert()
			handled = true
		case 'h':
			vi.cursor.Backspace()
			handled = true
		case 'w':
			vi.cursor.BackspaceWord()
			handled = true
		case 'j':
			vi.cursor.InsertWithIndentRune('\n', vi.config.indentRune, vi.config.indentTabspaces)
			vi.insertRegister.WriteRune('\n')
			handled = true
		case 't':
			vi.cursor.ShiftLineRight(vi.config.indentRune, vi.config.indentTabspaces)
			handled = true
		case 'd':
			vi.cursor.ShiftLineLeft(vi.config.indentRune, vi.config.indentTabspaces)
			handled = true
		case 'n':
			keepCompletion = true
			vi.completeInsert(1)
			handled = true
		case 'p':
			keepCompletion = true
			vi.completeInsert(-1)
			handled = true
		case 'r':
			vi.pendingInsertRegister = true
			handled = true
		case 'o':
			vi.startInsertNormalCommand()
			handled = true
		}
	}
	return
}

func (vi *viHandlerImpl) copySelection() {
	data := vi.selectionRegisterData()
	reg := vi.consumeActiveRegister()
	_, err := vi.cursor.CopySelection(registerNameToID(reg), fixedCopy{vi.config.clipboard, data})
	if err != nil {
		vi.logError(err)
	}
	if reg != unnamedRegister {
		if err := vi.writeRegister(unnamedRegister, data); err != nil {
			vi.logError(err)
		}
	}
	if reg != lastYankRegister {
		if err := vi.writeRegister(lastYankRegister, data); err != nil {
			vi.logError(err)
		}
	}
}

// yankRange yanks the range an operator motion selected and, as in Vim,
// leaves the cursor at its start, which is where the motion ended when it
// moved backward.
func (vi *viHandlerImpl) yankRange() {
	from, to, ok := vi.cursor.SelectionBounds()
	vi.copySelection()
	if ok {
		start, _ := term.CoordinatesSort(from, to)
		vi.cursor.MoveToScroll(start)
	}
}

func (vi *viHandlerImpl) copySuppressed() bool {
	return vi.suppressCopyDelete
}

// withoutRegisters runs an edit that, as in Vim, leaves the registers alone
// even though it removes text, such as shifting lines left.
func (vi *viHandlerImpl) withoutRegisters(edit func()) {
	vi.suppressCopyDelete = true
	defer func() { vi.suppressCopyDelete = false }()
	edit()
}

// deleteSelection writes the selection to the active register and deletes
// it. The copy-on-delete subscriber is held off so it cannot overwrite the
// registers just written or fill the unnamed one on a black hole delete.
// A linewise delete removes the lines as Vim does, except for a change,
// which keeps a line to insert into.
func (vi *viHandlerImpl) deleteSelection(change bool) {
	mode, _ := vi.cursor.SelectionMode()
	vi.copySelectionForDelete()
	linewise := mode == text.LineSelection && !change
	if linewise {
		vi.selectLineBreakBeforeEnd()
	}
	vi.suppressCopyDelete = true
	vi.cursor.DeleteSelection()
	vi.suppressCopyDelete = false
	if linewise {
		// Vim leaves a linewise delete on the first non-blank of the line
		// that takes the place of the deleted ones.
		vi.cursor.MoveStartLineNonBlank()
	}
}

// selectLineBreakBeforeEnd widens a selection of lines that run to the end
// of the buffer over the line break before them, which Vim deletes along
// with the lines so that the line above becomes the last one. The buffer's
// own line delete keeps that line break unless the lines are only the last.
func (vi *viHandlerImpl) selectLineBreakBeforeEnd() {
	from, to, ok := vi.cursor.SelectionBounds()
	if !ok {
		return
	}
	from, to = term.CoordinatesSort(from, to)
	buf := vi.less.Buffer()
	last := buf.Rows() - 1
	if from.Y == 0 || from.Y > last || to.Y < last {
		return
	}
	vi.cursor.SelectRange(
		term.Coordinates{Y: from.Y - 1, X: buf.Columns(from.Y - 1)},
		term.Coordinates{Y: last, X: buf.Columns(last)})
}

// deleteChars deletes up to n characters of the cursor line into the
// active register: those from the cursor on as x does, or those before it
// as X does. It reports whether there was any to delete.
func (vi *viHandlerImpl) deleteChars(n int, before bool) bool {
	pos := vi.cursor.CursorAtScroll()
	if pos.Y >= vi.less.Buffer().Rows() {
		vi.consumeActiveRegister()
		return false
	}
	from, to := pos, pos
	if before {
		from.X -= min(n, pos.X)
	} else {
		to.X += min(n, vi.less.Buffer().Columns(pos.Y)-pos.X)
	}
	if from.X >= to.X {
		vi.consumeActiveRegister()
		return false
	}
	vi.cursor.SelectRange(from, to)
	vi.deleteSelection(false)
	return true
}

// changeLine empties the cursor line for an insert, as cc and S do: the
// register takes the whole line and the line is indented afresh.
func (vi *viHandlerImpl) changeLine() {
	y := vi.cursor.CursorAtScroll().Y
	vi.captureChangeLines(y, y)
	vi.cursor.MoveStartLine()
	if vi.cursor.Select() {
		vi.cursor.MoveEndLine()
		vi.deleteSelection(true)
		vi.cursor.TryIndent(vi.config.indentRune, vi.config.indentTabspaces)
	}
	vi.changeLines = nil
	vi.setInsertMode()
}

// captureChangeLines keeps lines fromY through toY for the register of the
// linewise change about to delete their text.
func (vi *viHandlerImpl) captureChangeLines(fromY, toY int) {
	pos := vi.cursor.CursorAtScroll()
	vi.cursor.Unselect()
	vi.cursor.MoveToScroll(term.Coordinates{Y: fromY})
	if vi.cursor.SelectLine() {
		vi.cursor.MoveToScroll(term.Coordinates{Y: toY})
		data := vi.selectionRegisterData()
		vi.changeLines = &data
	}
	vi.cursor.Unselect()
	vi.cursor.MoveToScroll(pos)
}

// selectionRegisterData returns the selection as register contents.
func (vi *viHandlerImpl) selectionRegisterData() clipboard.Data {
	mode, _ := vi.cursor.SelectionMode()
	data := clipboard.Data{Text: vi.cursor.Selection(), Metadata: mode}
	if _, _, ok := vi.cursor.SelectionRange(); ok && mode == text.LineSelection && data.Text == "" {
		// A single empty line still holds its line break.
		data.Text = "\n"
	}
	return data
}

// fixedCopy is a clipboard.Register that copies data whatever it is asked
// to copy, so Cursor.CopySelection can store selectionRegisterData.
type fixedCopy struct {
	clipboard.Register
	data clipboard.Data
}

func (f fixedCopy) Copy(registerID string, _ clipboard.Data) error {
	return f.Register.Copy(registerID, f.data)
}

// changeCase applies a case operator to the selection. As in Vim, it leaves
// the registers alone and the cursor at the start of the range.
func (vi *viHandlerImpl) changeCase(fn func() bool) {
	from, to, ok := vi.cursor.SelectionBounds()
	vi.suppressCopyDelete = true
	fn()
	vi.suppressCopyDelete = false
	if ok {
		start, _ := term.CoordinatesSort(from, to)
		vi.cursor.MoveToScroll(start)
	}
}

func (vi *viHandlerImpl) copySelectionForDelete() {
	data := vi.selectionRegisterData()
	if vi.changeLines != nil {
		data = *vi.changeLines
		vi.changeLines = nil
	}
	reg := vi.consumeActiveRegister()
	if reg == blackHoleRegister {
		return
	}

	if reg != unnamedRegister {
		if err := vi.writeRegister(reg, data); err != nil {
			vi.logError(err)
		}
	}
	if err := vi.writeRegister(unnamedRegister, data); err != nil {
		vi.logError(err)
	}
	if data.Metadata != text.LineSelection {
		if err := vi.writeRegister('-', data); err != nil {
			vi.logError(err)
		}
	}
}

func (vi *viHandlerImpl) repeatInsertStart() {
	from, to := term.CoordinatesSort(vi.blockRepeat.From, vi.blockRepeat.To)
	n := to.Y - from.Y
	for range n {
		vi.blockRepeat.From.Y++
		vi.cursor.MoveToScroll(vi.blockRepeat.From)
		vi.repeater.Repeat()
	}
}

func (vi *viHandlerImpl) handleVisualBlockInsertStart() {
	anchor, _ := vi.cursor.SelectionFrom()
	cursor := vi.cursor.CursorAtScroll()
	blockFrom, blockTo := term.CoordinatesBlockSort(anchor, cursor)
	vi.setInsertMode()
	vi.blockRepeat.From = blockFrom
	vi.blockRepeat.To = blockTo
	vi.setCursorAtScroll(blockFrom)
}

func (vi *viHandlerImpl) handleVisualBlockAppendStart() {
	anchor, _ := vi.cursor.SelectionFrom()
	cursor := vi.cursor.CursorAtScroll()
	blockFrom, blockTo := term.CoordinatesBlockSort(anchor, cursor)
	// append position is one column past the right edge of the block
	appendCol := blockTo.X + 1
	vi.setInsertMode()
	vi.blockRepeat.From = term.Coordinates{X: appendCol, Y: blockFrom.Y}
	vi.blockRepeat.To = term.Coordinates{X: appendCol, Y: blockTo.Y}
	vi.setCursorAtScroll(vi.blockRepeat.From)
}

func (vi *viHandlerImpl) handleVisualBlockChangeStart() {
	anchor, _ := vi.cursor.SelectionFrom()
	cursor := vi.cursor.CursorAtScroll()
	blockFrom, blockTo := term.CoordinatesBlockSort(anchor, cursor)
	// delete the block first, then enter insert at the left edge
	vi.suppressCopyDelete = true
	vi.cursor.DeleteSelection()
	vi.suppressCopyDelete = false
	vi.setInsertMode()
	vi.blockRepeat.From = term.Coordinates{X: blockFrom.X, Y: blockFrom.Y}
	vi.blockRepeat.To = term.Coordinates{X: blockFrom.X, Y: blockTo.Y}
	vi.setCursorAtScroll(blockFrom)
}

func (vi *viHandlerImpl) replaceVisualBlockSelection(ch rune) bool {
	mode, ok := vi.cursor.SelectionMode()
	if !ok || mode != text.BlockSelection {
		return false
	}
	anchor, ok := vi.cursor.SelectionFrom()
	if !ok {
		return false
	}
	cursor := vi.cursor.CursorAtScroll()
	blockFrom, blockTo := term.CoordinatesBlockSort(anchor, cursor)

	vi.cursor.Unselect()
	buf := vi.less.Buffer()
	replacement := string(ch)
	for y := blockFrom.Y; y <= blockTo.Y && y < buf.Rows(); y++ {
		columns := buf.Columns(y)
		fromX := min(blockFrom.X, columns)
		toX := min(blockTo.X+1, columns)
		for x := fromX; x < toX; x++ {
			from := term.Coordinates{X: x, Y: y}
			to := term.Coordinates{X: x + 1, Y: y}
			buf.Edit(context.Background(), from, to, replacement)
		}
	}
	vi.setCursorAtScroll(blockFrom)
	return true
}

func (vi *viHandlerImpl) handleVisual(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return
	}
	// Snapshot the current selection bounds before dispatching the
	// event. Many visual-mode operators (yank, delete, change,
	// shift, gq, gu, gU, g~, ...) clear the selection internally,
	// so we cannot read the bounds back after dispatch. If this
	// event causes vi to leave visual mode, persist the snapshot as
	// the `<` / `>` visual marks below.
	hadSelection := false
	var snapFrom, snapTo term.Coordinates
	if from, to, ok := vi.cursor.SelectionBounds(); ok {
		hadSelection = true
		snapFrom, snapTo = term.CoordinatesSort(from, to)
		if mode, modeOk := vi.cursor.SelectionMode(); modeOk {
			// For line selections, Vim's `'>` mark sits at the
			// last column of the last selected line. Adjust the
			// snapshot accordingly so jumps land in the right
			// place.
			if mode == text.LineSelection {
				snapFrom.X = 0
				snapTo.X = vi.less.Buffer().Columns(snapTo.Y)
			}
		}
	}
	// Persist visual marks whenever this dispatch leaves any of
	// the visual modes, regardless of which path inside this
	// function returned.
	defer func() {
		switch vi.mode() {
		case visualMode, visualLineMode, visualBlockMode:
			// staying in visual; don't overwrite marks yet.
		default:
			vi.recordVisualMarksFromSnapshot(hadSelection, snapFrom, snapTo)
		}
	}()

	if ev.Key == term.KeyEsc || (ev.Ch == 'c' && ev.Mod == term.ModCtrl) {
		vi.setNormalMode()
		vi.recordVisualMarksFromSnapshot(hadSelection, snapFrom, snapTo)
		vi.cursor.Unselect()
		handled = true
		return
	}
	if vi.pendingRegister {
		if ev.Mod == 0 && validRegisterName(ev.Ch) {
			vi.selectedRegister = normalizedRegisterName(ev.Ch)
		}
		vi.pendingRegister = false
		handled = true
		return
	}

	quit, handled = vi.handleMoveToCharacter(vi.moveMode, ev)
	if handled {
		return
	}

	if ev.Mod == 0 {
		hadPending := vi.textObjectPending
		if vi.textObjectPending {
			handled = vi.selectTextObject(ev.Ch)
			if handled {
				return
			}
		}
		switch ev.Ch {
		case 'i':
			vi.setTextObjectPending(false)
			return false, true
		case 'a':
			vi.setTextObjectPending(true)
			return false, true
		default:
			if hadPending {
				return false, true
			}
		}
	}

	handled = true
	switch ev.Mod {
	case 0:
		switch ev.Ch {
		case '"':
			vi.pendingRegister = true
		case 'g':
			if mode, ok := vi.cursor.SelectionMode(); ok && mode == text.LineSelection {
				if vi.pendingGoMotion {
					vi.pendingGoMotion = false
				}
				vi.setGMode()
				return
			}
			vi.setGMode()
			return
		case 'z':
			vi.cursor.HideSelection()
			vi.setNormalMode()
		case 'o':
			vi.cursor.SwapSelectionEnd()
		case 'O':
			if vi.mode() == visualBlockMode {
				if !vi.cursor.SwapSelectionCorner() {
					vi.cursor.SwapSelectionEnd()
				}
			} else {
				vi.cursor.SwapSelectionEnd()
			}
		case '>':
			vi.withoutRegisters(func() {
				vi.cursor.ShiftSelectionRight(vi.config.indentRune, vi.config.indentTabspaces)
			})
			vi.setNormalMode()
		case '<':
			vi.withoutRegisters(func() {
				vi.cursor.ShiftSelectionLeft(vi.config.indentRune, vi.config.indentTabspaces)
			})
			vi.setNormalMode()
		case '=':
			vi.withoutRegisters(func() {
				vi.cursor.ReindentSelection(vi.config.indentRune, vi.config.indentTabspaces)
			})
			vi.setNormalMode()
		case 'y':
			vi.copySelection()
			vi.setNormalMode()
		case 'J':
			vi.joinSelection(vi.cursor.Join)
			vi.setNormalMode()
		case 'd', 'x':
			vi.deleteSelection(false)
			vi.setNormalMode()
		case 's', 'c':
			switch vi.mode() {
			case visualBlockMode:
				vi.handleVisualBlockChangeStart()
			default:
				vi.deleteSelection(true)
				vi.setInsertMode()
			}
		case 'u':
			vi.changeCase(vi.cursor.LowercaseSelection)
			vi.setNormalMode()
		case 'U':
			vi.changeCase(vi.cursor.UppercaseSelection)
			vi.setNormalMode()
		case '~':
			vi.changeCase(vi.cursor.ToggleCaseSelection)
			vi.setNormalMode()
		case 'v', 'V':
			// The key of the current visual mode leaves it; the other
			// key switches modes in handleNormal.
			current := visualMode
			if ev.Ch == 'V' {
				current = visualLineMode
			}
			if vi.mode() == current {
				vi.setNormalMode()
			} else {
				handled = false
			}
		case 'I':
			switch vi.mode() {
			case visualBlockMode:
				vi.handleVisualBlockInsertStart()
			default:
				handled = false
			}
		case 'A':
			switch vi.mode() {
			case visualBlockMode:
				vi.handleVisualBlockAppendStart()
			default:
				handled = false
			}
		case 'r':
			switch vi.mode() {
			case visualBlockMode:
				vi.setReplaceOneMode()
				return false, true
			default:
				handled = false
			}
		default:
			handled = false
		}
	case term.ModCtrl:
		if ev.Ch == 'v' && vi.mode() == visualBlockMode {
			vi.setNormalMode()
		} else {
			handled = false
		}
	default:
		handled = false
	}

	if !handled {
		quit, handled = vi.handleNormal(ev)
	}

	switch vi.mode() {
	case visualMode, visualLineMode, visualBlockMode:
		if ev.Mod == 0 && ev.Ch == 'p' {
			vi.setNormalMode()
		}
	default:
		if vi.mode() != gMode {
			vi.cursor.Unselect()
		}
	}

	return
}

func (vi *viHandlerImpl) handleMoveToCharacter(mode moveMode, ev term.Event) (exit, handled bool) {
	exit, handled, _ = vi.moveToCharacter(mode, ev)
	return
}

// moveToCharacter completes an f/t/F/T motion with the character in ev
// and reports whether it found count of them. As in Vim, the cursor stays
// put when it finds fewer.
func (vi *viHandlerImpl) moveToCharacter(mode moveMode, ev term.Event) (exit, handled, found bool) {
	prevMode := vi.currMode
	switch ev.Mod {
	case 0:
		switch ev.Type {
		case term.EventKey:
			ev.Ch = findTarget(ev)
			if mode == moveNone {
				return
			}
			if found = vi.findChar(mode, ev.Ch); found {
				vi.moveChar = ev.Ch
			}
			vi.setNormalMode()
			switch prevMode {
			case visualMode, visualLineMode, visualBlockMode:
				vi.setMode(prevMode)
			}
			handled = true
		default:
			vi.setMode(vi.mode())
			handled = true
		}
	}
	return
}

func (vi *viHandlerImpl) findChar(mode moveMode, ch rune) bool {
	start := vi.cursor.CursorAtScroll()
	move := vi.cursor.MoveToNextChar
	if mode == moveToPrev || mode == moveTillPrev {
		move = vi.cursor.MoveToPrevChar
	}
	for range vi.motionCount() {
		if !move(ch) {
			vi.cursor.MoveToScroll(start)
			return false
		}
	}
	switch mode {
	case moveTillNext:
		vi.cursor.MoveLeft()
	case moveTillPrev:
		vi.cursor.MoveRight()
	}
	return true
}

// findTarget returns the character an f/t/F/T key event searches for.
func findTarget(ev term.Event) rune {
	switch ev.Key {
	case term.KeySpace:
		return ' '
	case term.KeyTab:
		return '\t'
	case term.KeyEnter:
		return '\n'
	default:
		return ev.Ch
	}
}

func (vi *viHandlerImpl) handleReplace(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventKey || ev.Mod != 0 {
		return
	}

	handled = true

	switch ev.Key {
	case term.KeyEnter:
		ev.Ch = '\n'
	case term.KeySpace:
		ev.Ch = ' '
	case term.KeyTab:
		ev.Ch = '\t'
	case term.KeyBackspace:
		vi.cursor.MoveLeft()
	case term.KeyEsc:
		vi.cursor.MoveLeft()
		vi.setNormalMode()
	}
	if ev.Ch == 0 && ev.Key == 0 {
		return false, false
	}

	if ev.Ch != 0 {
		if vi.replaceVisualBlockSelection(ev.Ch) {
			return
		}
		// do not delete column == len(row); it contains a newline
		// and that would conflate the current row with the next
		if vi.cursor.Column() < vi.less.Buffer().Columns(vi.cursor.Line()) {
			next := vi.cursor.Replace(ev.Ch)
			if vi.mode() == replaceMode { // replaceOneMode therefore stays in same char
				vi.cursor.MoveToScroll(next)
			}
		} else {
			vi.cursor.Insert(ev.Ch)
		}
	}
	return
}

func (vi *viHandlerImpl) handleMetaNormal(ev term.Event) (quit, handled, done bool) {
	before := vi.cursor.CursorAtScroll()
	vi.cursor.Select()

	prevMode := vi.moveMode
	count := vi.motionCount()
	// Read before the motion runs: a completed f/t/F/T resets the mode.
	change := vi.currMode == deleteMode && vi.deleteInsert
	kind := vi.operatorMotionKind(ev, prevMode)
	plain := prevMode == moveNone && ev.Mod == 0
	vi.paragraphAtEnd = false
	quit, handled = vi.handleNormal(ev)
	isMoveSwitch := prevMode == moveNone && vi.moveMode != moveNone
	after := vi.cursor.CursorAtScroll()
	// As in Vim, } running into the end of the buffer includes its last
	// character, and so does w with no word left to move to.
	atEnd := plain && ev.Ch == '}' && vi.paragraphAtEnd
	if plain && (ev.Ch == 'w' || ev.Ch == 'W') && before == after {
		_, atEnd = vi.cursor.Cell()
	}
	if atEnd {
		kind = motionInclusive
	}
	// Linewise jumps such as G act on the cursor line when they land on it.
	inPlace := plain && (strings.ContainsRune("GHML", ev.Ch) || (ev.Ch == '_' && count == 1))

	if before == after && !atEnd && !inPlace && !vi.tillTargetAdjacent(prevMode, ev, before) {
		vi.cursor.Unselect()
		if !isMoveSwitch {
			handled = false
			vi.setNormalMode()
		}
		return
	}

	done = true
	switch {
	case atEnd:
	case plain && (ev.Ch == 'w' || ev.Ch == 'W'):
		vi.selectWordOperatorRange(before, after, count, ev.Ch == 'W', change)
	case kind == motionExclusive:
		start, end := term.CoordinatesSort(before, after)
		vi.selectExclusiveRange(start, end, change)
	case kind == motionLinewise && change:
		start, end := term.CoordinatesSort(before, after)
		vi.selectLinewiseRange(start, end.Y, true)
	case kind == motionLinewise:
		vi.cursor.SelectLine()
	}
	return
}

// tillTargetAdjacent reports whether ev completes a t motion whose target
// sits right after pos: the motion succeeds without moving the cursor.
func (vi *viHandlerImpl) tillTargetAdjacent(prevMode moveMode, ev term.Event, pos term.Coordinates) bool {
	if prevMode != moveTillNext || pos.Y >= vi.less.Buffer().Rows() {
		return false
	}
	row := vi.less.Buffer().Row(pos.Y)
	return pos.X+1 < len(row) && row[pos.X+1].Ch == findTarget(ev)
}

// motionKind is how an operator treats the text a motion moves over.
type motionKind uint8

const (
	motionInclusive motionKind = iota
	motionExclusive
	motionLinewise
)

// findMotionKind returns the kind of an f/t/F/T motion: forward finds
// include the character they stop on, backward finds do not.
func findMotionKind(mode moveMode) motionKind {
	switch mode {
	case moveToPrev, moveTillPrev:
		return motionExclusive
	default:
		return motionInclusive
	}
}

// operatorMotionKind classifies the motion ev completes for an operator as
// in Vim's :h exclusive and :h linewise. prevMode is the f/t/F/T mode
// pending when ev arrived, which makes ev the character to find.
func (vi *viHandlerImpl) operatorMotionKind(ev term.Event, prevMode moveMode) motionKind {
	if prevMode != moveNone {
		return findMotionKind(prevMode)
	}
	switch ev.Mod {
	case 0:
	case term.ModCtrl:
		return motionLinewise
	default:
		return motionInclusive
	}
	switch ev.Key {
	case term.KeyArrowLeft, term.KeyArrowRight:
		return motionExclusive
	case term.KeyArrowUp, term.KeyArrowDown:
		return motionLinewise
	}
	switch ev.Ch {
	case 'h', 'l', '0', '^', '|', 'b', 'B', 'w', 'W', '(', ')', '{', '}', 'n', 'N', '*', '#':
		return motionExclusive
	case 'j', 'k', 'G', 'H', 'M', 'L', '+', '-', '_':
		return motionLinewise
	case ';':
		return findMotionKind(vi.lastMoveMode)
	case ',':
		return findMotionKind(reverseDirection(vi.lastMoveMode))
	default:
		return motionInclusive
	}
}

// selectExclusiveRange selects [start, end) for an exclusive motion. As in
// Vim's :h exclusive, an end in column 0 of a later line moves to the end of
// the previous line, and the range turns linewise if start is also within
// its line's indent.
func (vi *viHandlerImpl) selectExclusiveRange(start, end term.Coordinates, change bool) bool {
	if end.X == 0 && end.Y > start.Y {
		end.Y--
		if vi.inIndent(start) {
			return vi.selectLinewiseRange(start, end.Y, change)
		}
		end.X = vi.less.Buffer().Columns(end.Y)
	}
	return vi.cursor.SelectRange(start, end)
}

// selectLinewiseRange selects lines start.Y through endY, anchored at start,
// so start stays the first position of the selection.
// A change keeps one line to insert into, with its indent, so it selects
// the text of those lines instead and keeps the lines for the register.
func (vi *viHandlerImpl) selectLinewiseRange(start term.Coordinates, endY int, change bool) bool {
	if change {
		vi.captureChangeLines(start.Y, endY)
		buf := vi.less.Buffer()
		indent := 0
		for _, cell := range buf.Row(start.Y) {
			if cell.Ch != ' ' && cell.Ch != '\t' {
				break
			}
			indent++
		}
		return vi.cursor.SelectRange(term.Coordinates{Y: start.Y, X: indent},
			term.Coordinates{Y: endY, X: buf.Columns(endY)})
	}
	vi.cursor.Unselect()
	vi.cursor.MoveToScroll(start)
	if !vi.cursor.SelectLine() {
		return false
	}
	if endY != start.Y {
		vi.cursor.MoveToScroll(term.Coordinates{Y: endY})
	}
	return true
}

// inIndent reports whether only blanks precede pos on its line.
func (vi *viHandlerImpl) inIndent(pos term.Coordinates) bool {
	row := vi.less.Buffer().Row(pos.Y)
	for x := 0; x < pos.X && x < len(row); x++ {
		if row[x].Ch != ' ' && row[x].Ch != '\t' {
			return false
		}
	}
	return true
}

// selectWordOperatorRange selects what an operator acts on for count w/W
// motions from before to after. As in Vim, the last word stops at the end of
// the line it starts on instead of the next line's first word, and an end in
// column 0 is pulled back onto the previous line, turning linewise when the
// operator starts in the indent. When no word follows at the end of the
// buffer, the range includes the last character.
func (vi *viHandlerImpl) selectWordOperatorRange(
	before, after term.Coordinates, count int, bigWord, change bool,
) {
	move := vi.cursor.MoveRightStartWord
	if bigWord {
		move = vi.cursor.MoveRightStartWordGroup
	}
	vi.cursor.MoveToScroll(before)
	for range count - 1 {
		prev := vi.cursor.CursorAtScroll()
		if !move() && vi.cursor.CursorAtScroll() == prev {
			break
		}
	}
	last := vi.cursor.CursorAtScroll()

	buf := vi.less.Buffer()
	if after.Y == last.Y {
		vi.cursor.MoveToScroll(after)
		atBufferEnd := after.Y == buf.Rows()-1 && after.X >= buf.Columns(after.Y)-1
		if !atBufferEnd || vi.cursor.AtStartWord(bigWord) {
			vi.cursor.MoveLeft()
		}
		return
	}
	if cols := buf.Columns(last.Y); cols > 0 {
		vi.cursor.MoveToScroll(term.Coordinates{Y: last.Y, X: cols - 1})
		return
	}

	// The last word is the empty line last.Y, so the motion ends at column 0
	// of the following line.
	vi.selectExclusiveRange(before, term.Coordinates{Y: last.Y + 1}, change)
}

func (vi *viHandlerImpl) handleMetaGo(ev term.Event) (quit, handled, done bool) {
	if ev.Mod == 0 && (ev.Ch == 'j' || ev.Ch == 'k') && !vi.less.Scroll().Wrap {
		// Without wrapping, display lines are buffer lines, and Vim's gj
		// and gk are j and k, linewise.
		return vi.handleMetaNormal(ev)
	}
	before := vi.cursor.CursorAtScroll()
	vi.cursor.Select()
	change := vi.currMode == deleteMode && vi.deleteInsert
	kind := motionInclusive
	failed := false

	switch ev.Mod {
	case 0:
		switch ev.Ch {
		case 'g':
			count := vi.motionCount()
			vi.cursor.MoveToScroll(vi.anchor)
			if count == 1 {
				vi.cursor.MoveFirstLine()
			} else {
				target := max(0, min(count-1, vi.less.Buffer().Rows()-1))
				vi.setCursorAtScroll(term.Coordinates{Y: target})
			}
			vi.resetCount()
			kind = motionLinewise
			handled = true
		case 'e':
			vi.repeatMotion(vi.cursor.MoveLeftEndWord)
			handled = true
		case 'E':
			vi.repeatMotion(vi.cursor.MoveLeftEndWordGroup)
			handled = true
		case 'j', 'k':
			move := vi.cursor.MoveDisplayDown
			if ev.Ch == 'k' {
				move = vi.cursor.MoveDisplayUp
			}
			// As in Vim, an operator is cancelled when the count runs past
			// the buffer.
			for range vi.motionCount() {
				if !move() {
					failed = true
					break
				}
			}
			// Vim's cursor cannot rest past a line's last character.
			vi.cursor.MoveToBounds(0)
			kind = motionExclusive
			handled = true
		case '_':
			vi.cursor.MoveEndLineNonBlank()
			handled = true
		}
	}

	after := vi.cursor.CursorAtScroll()
	// gg acts on the cursor line when it is already on the target line.
	if failed || (before == after && kind != motionLinewise) {
		vi.cursor.Unselect()
		handled = false
		vi.setNormalMode()
		return
	}

	done = true
	start, end := term.CoordinatesSort(before, after)
	switch {
	case kind == motionExclusive:
		vi.selectExclusiveRange(start, end, change)
	case kind == motionLinewise && change:
		vi.selectLinewiseRange(start, end.Y, true)
	case kind == motionLinewise:
		vi.cursor.SelectLine()
	}
	return
}

// handleOperatorGoMotion completes the g-prefixed motion an operator waits
// on (gg, ge, gE, gj, gk or g_) and runs apply on the range it selects.
func (vi *viHandlerImpl) handleOperatorGoMotion(ev term.Event, apply func()) (quit, handled bool) {
	vi.pendingGoMotion = false
	switch ev.Ch {
	case 'g', 'e', 'E', 'j', 'k', '_':
	default:
		vi.setNormalMode()
		return false, true
	}
	quit, handled, done := vi.handleMetaGo(ev)
	if !done {
		return quit, handled
	}
	apply()
	return quit, true
}

func (vi *viHandlerImpl) handleYank(ev term.Event) (quit, handled bool) {
	if ev.Ch == 'y' && ev.Mod == 0 {
		if vi.selectLineCount() {
			vi.copySelection()
		}
		handled = true
		vi.setNormalMode()
		return
	}
	if vi.parseOperatorCountDigit(ev) {
		return false, true
	}

	if vi.moveMode == moveNone && ev.Mod == 0 {
		if vi.pendingGoMotion {
			return vi.handleOperatorGoMotion(ev, func() {
				vi.yankRange()
				vi.setNormalMode()
			})
		}

		if vi.textObjectPending {
			if ev.Ch == 'i' {
				vi.setTextObjectPending(false)
				return false, true
			}
			if ev.Ch == 'a' {
				vi.setTextObjectPending(true)
				return false, true
			}
			if !vi.selectTextObject(ev.Ch) {
				vi.setNormalMode()
				return false, true
			}
			vi.copySelection()
			vi.setNormalMode()
			return false, true
		}
		switch ev.Ch {
		case 'g':
			vi.pendingGoMotion = true
			return false, true
		case 'i':
			vi.setTextObjectPending(false)
			return false, true
		case 'a':
			vi.setTextObjectPending(true)
			return false, true
		case '/':
			vi.beginSearchMotion(ev, moveToNext, vi.beginYankSearchOp)
			return false, true
		case '?':
			vi.beginSearchMotion(ev, moveToPrev, vi.beginYankSearchOp)
			return false, true
		case '\'':
			vi.beginYankMarkOp(true)
			return false, true
		case '`':
			vi.beginYankMarkOp(false)
			return false, true
		}
	}

	var done bool
	quit, handled, done = vi.handleMetaNormal(ev)
	if !done {
		return
	}

	vi.yankRange()
	vi.setNormalMode()
	return
}

func (vi *viHandlerImpl) handleShift(ev term.Event) (quit, handled bool) {
	// double-key: >>, <<, == = apply to whole line(s)
	if ev.Ch == vi.shiftRepeat && ev.Mod == 0 && vi.moveMode == moveNone {
		if !vi.cursor.SelectLine() {
			vi.setNormalMode()
			return
		}
		count := vi.motionCount()
		if count > 1 {
			for i := 1; i < count && vi.cursor.MoveLineDown(); i++ {
			}
		}
		vi.shiftFn()
		vi.setNormalMode()
		handled = true
		return
	}
	if vi.parseOperatorCountDigit(ev) {
		return false, true
	}

	if vi.moveMode == moveNone && ev.Mod == 0 {
		if vi.pendingGoMotion {
			return vi.handleOperatorGoMotion(ev, func() {
				vi.shiftFn()
				vi.setNormalMode()
			})
		}

		if vi.textObjectPending {
			if ev.Ch == 'i' {
				vi.setTextObjectPending(false)
				return false, true
			}
			if ev.Ch == 'a' {
				vi.setTextObjectPending(true)
				return false, true
			}
			if !vi.selectTextObject(ev.Ch) {
				vi.setNormalMode()
				return false, true
			}
			vi.shiftFn()
			vi.setNormalMode()
			return false, true
		}
		switch ev.Ch {
		case 'g':
			vi.pendingGoMotion = true
			return false, true
		case 'i':
			vi.setTextObjectPending(false)
			return false, true
		case 'a':
			vi.setTextObjectPending(true)
			return false, true
		case '/':
			vi.beginSearchMotion(ev, moveToNext, vi.beginShiftSearchOp)
			return false, true
		case '?':
			vi.beginSearchMotion(ev, moveToPrev, vi.beginShiftSearchOp)
			return false, true
		case '\'':
			vi.beginShiftMarkOp(true)
			return false, true
		case '`':
			vi.beginShiftMarkOp(false)
			return false, true
		}
	}

	var done bool
	quit, handled, done = vi.handleMetaNormal(ev)
	if !done {
		return
	}

	vi.shiftFn()
	vi.setNormalMode()
	return
}

func (vi *viHandlerImpl) handleCaseChange(ev term.Event) (quit, handled bool) {
	// double-key: guu, gUU, g~~ = apply to whole line
	if ev.Ch == vi.caseChangeRepeat && ev.Mod == 0 && vi.moveMode == moveNone {
		origin := vi.cursor.CursorAtScroll()
		if vi.selectLineCount() {
			// Vim's motion ends on the first non-blank of the last line, and
			// the cursor lands on whichever of that and the origin comes first.
			vi.cursor.MoveStartLineNonBlank()
			start, _ := term.CoordinatesSort(origin, vi.cursor.CursorAtScroll())
			vi.changeCase(vi.caseChangeFn)
			vi.cursor.MoveToScroll(start)
			handled = true
		}
		vi.setNormalMode()
		return
	}
	if vi.parseOperatorCountDigit(ev) {
		return false, true
	}

	if vi.moveMode == moveNone && ev.Mod == 0 {
		if vi.pendingGoMotion {
			return vi.handleOperatorGoMotion(ev, func() {
				vi.changeCase(vi.caseChangeFn)
				vi.setNormalMode()
			})
		}

		if vi.textObjectPending {
			if ev.Ch == 'i' {
				vi.setTextObjectPending(false)
				return false, true
			}
			if ev.Ch == 'a' {
				vi.setTextObjectPending(true)
				return false, true
			}
			if !vi.selectTextObject(ev.Ch) {
				vi.setNormalMode()
				return false, true
			}
			vi.changeCase(vi.caseChangeFn)
			vi.setNormalMode()
			return false, true
		}
		switch ev.Ch {
		case 'g':
			vi.pendingGoMotion = true
			return false, true
		case 'i':
			vi.setTextObjectPending(false)
			return false, true
		case 'a':
			vi.setTextObjectPending(true)
			return false, true
		case '/':
			vi.beginSearchMotion(ev, moveToNext, vi.beginCaseChangeSearchOp)
			return false, true
		case '?':
			vi.beginSearchMotion(ev, moveToPrev, vi.beginCaseChangeSearchOp)
			return false, true
		case '\'':
			vi.beginCaseChangeMarkOp(true)
			return false, true
		case '`':
			vi.beginCaseChangeMarkOp(false)
			return false, true
		}
	}

	var done bool
	quit, handled, done = vi.handleMetaNormal(ev)
	if !done {
		return
	}

	vi.changeCase(vi.caseChangeFn)
	vi.setNormalMode()
	return
}

func (vi *viHandlerImpl) handleComment(ev term.Event) (quit, handled bool) {
	if ev.Ch == vi.commentRepeat && ev.Mod == 0 && vi.moveMode == moveNone {
		if vi.selectLineCount() {
			vi.commentFn()
			handled = true
		}
		vi.cursor.Unselect()
		vi.setNormalMode()
		return
	}
	if vi.parseOperatorCountDigit(ev) {
		return false, true
	}

	if vi.moveMode == moveNone && ev.Mod == 0 {
		if vi.pendingGoMotion {
			return vi.handleOperatorGoMotion(ev, func() {
				vi.commentFn()
				vi.cursor.Unselect()
				vi.setNormalMode()
			})
		}

		if vi.textObjectPending {
			if ev.Ch == 'i' {
				vi.setTextObjectPending(false)
				return false, true
			}
			if ev.Ch == 'a' {
				vi.setTextObjectPending(true)
				return false, true
			}
			if !vi.selectTextObject(ev.Ch) {
				vi.setNormalMode()
				return false, true
			}
			selectionMode, ok := vi.cursor.SelectionMode()
			if vi.commentRepeat == 'c' && ok && selectionMode == text.StandardSelection &&
				!strings.Contains(vi.cursor.Selection(), "\n") &&
				vi.cursor.ToggleBlockComment() {
				vi.cursor.Unselect()
				vi.setNormalMode()
				return false, true
			}
			vi.commentFn()
			vi.cursor.Unselect()
			vi.setNormalMode()
			return false, true
		}
		switch ev.Ch {
		case 'g':
			vi.pendingGoMotion = true
			return false, true
		case '_':
			if vi.selectLineCount() {
				vi.commentFn()
				vi.cursor.Unselect()
				vi.setNormalMode()
				return false, true
			}
		case 'i':
			vi.setTextObjectPending(false)
			return false, true
		case 'a':
			vi.setTextObjectPending(true)
			return false, true
		case '/':
			vi.beginSearchMotion(ev, moveToNext, vi.beginCommentSearchOp)
			return false, true
		case '?':
			vi.beginSearchMotion(ev, moveToPrev, vi.beginCommentSearchOp)
			return false, true
		case '\'':
			vi.beginCommentMarkOp(true)
			return false, true
		case '`':
			vi.beginCommentMarkOp(false)
			return false, true
		}
	}

	var done bool
	quit, handled, done = vi.handleMetaNormal(ev)
	if !done {
		return
	}

	vi.commentFn()
	vi.cursor.Unselect()
	vi.setNormalMode()
	return
}

// searchOpState holds the operator-pending state while a `/` or `?`
// motion is being entered. It is constructed by beginSearchOp and
// consumed by handleSearchOp once the user submits or cancels the
// search. Each operator (gq, d, y, c, >, <, gu, gU, g~) builds its
// own apply closure for what to do with the resolved range.
type searchOpState struct {
	from      term.Coordinates // cursor position when `/` or `?` was pressed
	direction moveMode         // moveToNext or moveToPrev
	linewise  bool             // when true the range is extended to whole lines
	apply     func()           // run on the materialized selection
	finish    func()           // mode transition after apply (e.g. setNormalMode)
}

// beginSearchOp captures the current cursor and arms the search-pending
// state for an operator. The caller must follow up by forwarding the
// triggering event (`/` or `?`) to vi.less so the search bar is shown.
func (vi *viHandlerImpl) beginSearchOp(direction moveMode, linewise bool, apply, finish func()) {
	vi.pendingSearchOp = &searchOpState{
		from:      vi.cursorAtScroll(),
		direction: direction,
		linewise:  linewise,
		apply:     apply,
		finish:    finish,
	}
	vi.searchMode = direction
}

// beginCommentSearchOp arms the search-pending state for the gq/gw
// operators. Formatting is linewise: the resolved range is extended to
// whole lines before the formatter runs.

// beginSearchMotion arms an operator's search-pending state and forwards the
// triggering `/`/`?` event to vi.less so the search bar is shown. When search
// is disabled it cancels the operator instead, treating the key as an
// unsupported motion.
func (vi *viHandlerImpl) beginSearchMotion(ev term.Event, direction moveMode, begin func(moveMode)) {
	if vi.config.disableSearch {
		vi.setNormalMode()
		return
	}
	begin(direction)
	vi.less.Handle(ev)
}

func (vi *viHandlerImpl) beginCommentSearchOp(direction moveMode) {
	commentFn := vi.commentFn
	vi.beginSearchOp(direction, true,
		func() { commentFn() },
		func() { vi.cursor.Unselect(); vi.setNormalMode() },
	)
}

// beginYankSearchOp arms the search-pending state for the y operator.
// Yank with `/` or `?` is charwise.
func (vi *viHandlerImpl) beginYankSearchOp(direction moveMode) {
	vi.beginSearchOp(direction, false,
		vi.yankRange,
		func() { vi.setNormalMode() },
	)
}

// beginDeleteSearchOp arms the search-pending state for the d/c
// operators. The motion is charwise. When the operator was invoked as
// `c` (deleteInsert), the cursor enters insert mode after the deletion.
func (vi *viHandlerImpl) beginDeleteSearchOp(direction moveMode) {
	deleteInsert := vi.deleteInsert
	vi.beginSearchOp(direction, false,
		func() { vi.deleteSelection(deleteInsert) },
		func() {
			if deleteInsert {
				vi.setInsertMode()
			} else {
				vi.setNormalMode()
			}
		},
	)
}

// beginShiftSearchOp arms the search-pending state for the >/< shift
// operators. Shift is linewise.
func (vi *viHandlerImpl) beginShiftSearchOp(direction moveMode) {
	shiftFn := vi.shiftFn
	vi.beginSearchOp(direction, true,
		func() { shiftFn() },
		func() { vi.setNormalMode() },
	)
}

// beginCaseChangeSearchOp arms the search-pending state for the gu/gU/g~
// operators. The motion is charwise.
func (vi *viHandlerImpl) beginCaseChangeSearchOp(direction moveMode) {
	caseChangeFn := vi.caseChangeFn
	vi.beginSearchOp(direction, false,
		func() { vi.changeCase(caseChangeFn) },
		func() { vi.setNormalMode() },
	)
}

// handleSearchOp routes events while an operator is waiting for the
// user to finish typing a `/` or `?` pattern. Typing and editing keys
// are forwarded to vi.less, Enter resolves the search and runs the
// operator, Esc cancels.
func (vi *viHandlerImpl) handleSearchOp(ev term.Event) (quit, handled bool) {
	if ev.Mod == 0 {
		switch ev.Key {
		case term.KeyEnter:
			vi.completeSearchOp()
			return false, true
		case term.KeyEsc:
			vi.cancelSearchOp()
			return false, true
		}
	}
	return vi.less.Handle(ev)
}

// completeSearchOp resolves the typed pattern, materializes the motion
// range as a selection, runs the operator's apply/finish closures, and
// clears the pending state. On empty pattern, no match, or zero-width
// range it cancels cleanly without mutating the buffer.
func (vi *viHandlerImpl) completeSearchOp() {
	op := vi.pendingSearchOp
	text := vi.less.SearchText()
	vi.less.SetNormalMode()
	if text == "" {
		vi.less.SetMessage("")
		vi.cancelSearchOp()
		return
	}
	vi.less.SetMessage("searching '%s'", text)
	before := op.from
	vi.search(text)
	if err := vi.writeRegister('/', clipboard.Data{Text: text}); err != nil {
		vi.logError(err)
	}
	after := vi.cursorAtScroll()
	if before == after {
		vi.cancelSearchOp()
		return
	}

	from, to := before, after
	if to.Y < from.Y || (to.Y == from.Y && to.X < from.X) {
		from, to = to, from
	}
	if op.linewise {
		// Linewise operators (gq, >, <) extend the range to whole lines.
		// Search motions are exclusive: a match landing at column 0
		// excludes the match line from the linewise range.
		if after.X == 0 && after != before {
			if after.Y > before.Y {
				to.Y--
			} else {
				from.Y++
			}
			if to.Y < from.Y {
				vi.cancelSearchOp()
				return
			}
		}
		from = term.Coordinates{Y: from.Y}
		to = term.Coordinates{Y: to.Y, X: vi.less.Buffer().Columns(to.Y)}
		if !vi.cursor.SelectRange(from, to) {
			vi.cancelSearchOp()
			return
		}
	} else if !vi.selectExclusiveRange(from, to, vi.currMode == deleteMode && vi.deleteInsert) {
		vi.cancelSearchOp()
		return
	}
	op.apply()
	vi.pendingSearchOp = nil
	op.finish()
}

// cancelSearchOp clears the search-operator state and returns to
// normal mode without mutating the buffer.
func (vi *viHandlerImpl) cancelSearchOp() {
	vi.less.SetNormalMode()
	vi.pendingSearchOp = nil
	vi.cursor.Unselect()
	vi.setNormalMode()
}

// markOpState holds the operator-pending state while a mark motion
// (`'{a}` or “ `{a} “) is being entered. It is constructed by
// beginMarkOp and consumed by handleMarkOp once the mark name is
// received. linewise is forced for `'`; charwise (“ ` “) operators
// fall back to their natural granularity. Operators that are
// intrinsically linewise (gq, >, <) set forceLinewise=true regardless
// of which mark form was used.
type markOpState struct {
	linewise      bool   // true when the mark form was `'`
	forceLinewise bool   // true for intrinsically linewise operators
	apply         func() // run on the materialized selection
	finish        func() // mode transition after apply
}

// beginMarkOp arms the mark-pending state for an operator. The next
// event consumed by handleMarkOp must be the mark name.
func (vi *viHandlerImpl) beginMarkOp(linewise, forceLinewise bool, apply, finish func()) {
	vi.pendingMarkOp = &markOpState{
		linewise:      linewise,
		forceLinewise: forceLinewise,
		apply:         apply,
		finish:        finish,
	}
}

// handleMarkOp consumes the mark-name event and runs the operator
// over the materialized range. Unknown or unset marks are no-ops.
func (vi *viHandlerImpl) handleMarkOp(ev term.Event) (quit, handled bool) {
	op := vi.pendingMarkOp
	if ev.Mod != 0 || !validMarkName(ev.Ch) {
		vi.cancelMarkOp()
		return false, true
	}
	before := vi.cursorAtScroll()
	if !vi.moveToNextLocation(string(ev.Ch)) {
		vi.cancelMarkOp()
		return false, true
	}
	linewise := op.linewise || op.forceLinewise
	if linewise {
		vi.cursor.MoveStartLineNonBlank()
	}
	after := vi.cursorAtScroll()
	if before == after {
		vi.cancelMarkOp()
		return false, true
	}
	change := vi.currMode == deleteMode && vi.deleteInsert
	if linewise && change {
		from, to := term.CoordinatesSort(before, after)
		if !vi.selectLinewiseRange(from, to.Y, true) {
			vi.cancelMarkOp()
			return false, true
		}
	} else if linewise {
		// Linewise selection: anchor at the starting line, then extend to
		// the mark line's first non-blank, where a yank leaves the cursor
		// when the mark is above. SelectLine produces a proper line
		// selection (including trailing newline) that operators like d, c,
		// y can act on as whole-line ranges.
		vi.cursor.MoveToScroll(before)
		if !vi.cursor.SelectLine() {
			vi.cancelMarkOp()
			return false, true
		}
		vi.cursor.MoveToScroll(after)
	} else {
		from, to := before, after
		if to.Y < from.Y || (to.Y == from.Y && to.X < from.X) {
			from, to = to, from
		}
		if !vi.selectExclusiveRange(from, to, change) {
			vi.cancelMarkOp()
			return false, true
		}
	}
	op.apply()
	vi.pendingMarkOp = nil
	op.finish()
	return false, true
}

// cancelMarkOp clears the mark-operator state and returns to normal
// mode without mutating the buffer.
func (vi *viHandlerImpl) cancelMarkOp() {
	vi.pendingMarkOp = nil
	vi.cursor.Unselect()
	vi.setNormalMode()
}

// beginCommentMarkOp arms the mark-pending state for the gq/gw operators.
// Formatting is linewise regardless of `'` vs “ ` “.
func (vi *viHandlerImpl) beginCommentMarkOp(linewise bool) {
	commentFn := vi.commentFn
	vi.beginMarkOp(linewise, true,
		func() { commentFn() },
		func() { vi.cursor.Unselect(); vi.setNormalMode() },
	)
}

// beginYankMarkOp arms the mark-pending state for the y operator.
func (vi *viHandlerImpl) beginYankMarkOp(linewise bool) {
	vi.beginMarkOp(linewise, false,
		vi.yankRange,
		func() { vi.setNormalMode() },
	)
}

// beginDeleteMarkOp arms the mark-pending state for d/c.
func (vi *viHandlerImpl) beginDeleteMarkOp(linewise bool) {
	deleteInsert := vi.deleteInsert
	vi.beginMarkOp(linewise, false,
		func() { vi.deleteSelection(deleteInsert) },
		func() {
			if deleteInsert {
				vi.setInsertMode()
			} else {
				vi.setNormalMode()
			}
		},
	)
}

// beginShiftMarkOp arms the mark-pending state for >/<. Shift is
// always linewise.
func (vi *viHandlerImpl) beginShiftMarkOp(linewise bool) {
	shiftFn := vi.shiftFn
	vi.beginMarkOp(linewise, true,
		func() { shiftFn() },
		func() { vi.setNormalMode() },
	)
}

// beginCaseChangeMarkOp arms the mark-pending state for gu/gU/g~.
func (vi *viHandlerImpl) beginCaseChangeMarkOp(linewise bool) {
	caseChangeFn := vi.caseChangeFn
	vi.beginMarkOp(linewise, false,
		func() { vi.changeCase(caseChangeFn) },
		func() { vi.setNormalMode() },
	)
}

func (vi *viHandlerImpl) handleDelete(ev term.Event) (quit, handled bool) {
	if !vi.deleteInsert && vi.moveMode == moveNone && ev.Ch == 'd' && ev.Mod == 0 {
		if vi.selectLineCount() {
			vi.deleteSelection(false)
		}
		vi.setNormalMode()
		handled = true
		return
	}

	if vi.deleteInsert && vi.moveMode == moveNone && ev.Ch == 'c' && ev.Mod == 0 {
		vi.changeLine()
		handled = true
		return
	}
	if vi.parseOperatorCountDigit(ev) {
		return false, true
	}

	if vi.moveMode == moveNone && ev.Mod == 0 {
		if vi.pendingGoMotion {
			return vi.handleOperatorGoMotion(ev, func() {
				vi.deleteSelection(vi.deleteInsert)
				if vi.deleteInsert {
					vi.setInsertMode()
				} else {
					vi.setNormalMode()
				}
			})
		}

		if vi.textObjectPending {
			if ev.Ch == 'i' {
				vi.setTextObjectPending(false)
				return false, true
			}
			if ev.Ch == 'a' {
				vi.setTextObjectPending(true)
				return false, true
			}
			if !vi.selectTextObject(ev.Ch) {
				vi.setNormalMode()
				return false, true
			}
			vi.deleteSelection(vi.deleteInsert)
			if vi.deleteInsert {
				vi.setInsertMode()
			} else {
				vi.setNormalMode()
			}
			return false, true
		}
		switch ev.Ch {
		case 'g':
			vi.pendingGoMotion = true
			return false, true
		case 'i':
			vi.setTextObjectPending(false)
			return false, true
		case 'a':
			vi.setTextObjectPending(true)
			return false, true
		case '/':
			vi.beginSearchMotion(ev, moveToNext, vi.beginDeleteSearchOp)
			return false, true
		case '?':
			vi.beginSearchMotion(ev, moveToPrev, vi.beginDeleteSearchOp)
			return false, true
		case '\'':
			vi.beginDeleteMarkOp(true)
			return false, true
		case '`':
			vi.beginDeleteMarkOp(false)
			return false, true
		}
	}

	var done bool
	if vi.selectChangeWord(ev) {
		handled = true
		done = true
	} else {
		quit, handled, done = vi.handleMetaNormal(ev)
	}
	if !done {
		return
	}

	vi.deleteSelection(vi.deleteInsert)
	if vi.deleteInsert {
		vi.setInsertMode()
	} else {
		vi.setNormalMode()
	}
	return
}

// selectChangeWord selects the range a `cw`/`cW` operator acts on and reports
// whether the event is such a motion. The range runs from the cursor to the
// end of the count-th Vim word end: a word end under the cursor counts as the
// first end, so `cw` there covers just the cell under it. Blank and missing
// cells return false so the caller falls back to the plain `w` motion.
func (vi *viHandlerImpl) selectChangeWord(ev term.Event) bool {
	if !vi.deleteInsert || vi.moveMode != moveNone || ev.Mod != 0 ||
		(ev.Ch != 'w' && ev.Ch != 'W') {
		return false
	}
	cell, ok := vi.cursor.Cell()
	if !ok || text.IsWordObjectBlank(cell.Ch) {
		return false
	}
	bigWord := ev.Ch == 'W'
	before := vi.cursorAtScroll()
	vi.cursor.Select()
	count := vi.motionCount()
	if vi.cursor.AtWordObjectEnd(bigWord) {
		count--
	}
	for i := 0; i < count; i++ {
		prev := vi.cursor.CursorAtScroll()
		if !vi.cursor.MoveRightWordObjectEnd(bigWord) &&
			vi.cursor.CursorAtScroll() == prev {
			break
		}
	}
	if vi.cursor.CursorAtScroll() == before {
		// The cursor never moved, so the anchor selection is empty while
		// Vim still changes the word-end cell under it.
		vi.cursor.SelectRange(before, term.Coordinates{Y: before.Y, X: before.X + 1})
	}
	return true
}

func (vi *viHandlerImpl) handleGo(ev term.Event) (quit, handled bool) {
	switch ev.Mod {
	case 0:
		switch ev.Ch {
		case 'u':
			vi.setCaseChangeMode(vi.cursor.LowercaseSelection, 'u')
			handled = true
			return
		case 'U':
			vi.setCaseChangeMode(vi.cursor.UppercaseSelection, 'U')
			handled = true
			return
		case '~':
			vi.setCaseChangeMode(vi.cursor.ToggleCaseSelection, '~')
			handled = true
			return
		case 'c':
			if _, ok := vi.cursor.SelectionMode(); ok {
				vi.cursor.ToggleLineComment()
				vi.cursor.Unselect()
				vi.setNormalMode()
				handled = true
				return
			}
			vi.setCommentMode(vi.cursor.ToggleLineComment, 'c')
			handled = true
			return
		case 'q':
			if _, ok := vi.cursor.SelectionMode(); ok {
				// Vim treats gq on a blank-only selection as handled
				// (no buffer change, but the operator consumed the
				// keys and the mode transition runs). Always report
				// handled so the visual operator pipeline matches
				// Vim semantics.
				vi.cursor.WrapSelectedParagraph(vi.config.ruler)
				handled = true
				vi.cursor.Unselect()
				vi.setNormalMode()
				return
			}
			vi.setCommentMode(func() bool {
				return vi.cursor.WrapSelectedParagraph(vi.config.ruler)
			}, 'q')
			handled = true
			return
		case 'w':
			saved := vi.cursor.CursorAtScroll()
			if _, ok := vi.cursor.SelectionMode(); ok {
				vi.cursor.WrapSelectedParagraphPreservePosition(vi.config.ruler, saved)
				handled = true
				vi.cursor.Unselect()
				vi.setNormalMode()
				return
			}
			vi.setCommentMode(func() bool {
				return vi.cursor.WrapSelectedParagraphPreservePosition(vi.config.ruler, saved)
			}, 'w')
			handled = true
			return
		case 'e':
			vi.repeatMotion(vi.cursor.MoveLeftEndWord)
			handled = true
		case 'E':
			vi.repeatMotion(vi.cursor.MoveLeftEndWordGroup)
			handled = true
		case 'J':
			if _, selected := vi.cursor.SelectionMode(); selected {
				vi.joinSelection(vi.cursor.Conflate)
			} else {
				vi.repeatJoin(vi.cursor.Conflate)
			}
			handled = true
		case 'g':
			vi.cursor.MoveToScroll(vi.anchor)
			if vi.count == 1 {
				vi.cursor.MoveFirstLine()
			} else {
				target := max(0, min(vi.count-1, vi.less.Buffer().Rows()-1))
				vi.setCursorAtScroll(term.Coordinates{Y: target})
			}
			vi.resetCount()
			handled = true
		case 'j':
			vi.cursor.MoveToScroll(vi.anchor)
			if vi.count == 1 {
				vi.cursor.MoveDisplayDown()
			} else {
				vi.cursor.MoveDisplayDownLines(vi.count)
			}
			vi.resetCount()
			handled = true
		case 'k':
			vi.cursor.MoveToScroll(vi.anchor)
			if vi.count == 1 {
				vi.cursor.MoveDisplayUp()
			} else {
				vi.cursor.MoveDisplayUpLines(vi.count)
			}
			vi.resetCount()
			handled = true
		case 'p':
			handled = vi.pasteClipboardLeaveCursorAfter(true)
		case 'P':
			handled = vi.pasteClipboardLeaveCursorAfter(false)
		case '_':
			vi.cursor.MoveEndLineNonBlank()
			handled = true
		default:
		}
	}
	vi.setNormalMode()
	return
}

// Selection satisfies tui.Handler
func (vi *viHandlerImpl) Selection() (string, bool) {
	text := vi.cursor.Selection()
	return text, text != ""
}

// Handle satisfies tui.Handler
func (vi *viHandlerImpl) Handle(ev term.Event) (quit, handled bool) {
	// only a user event clears a pending set cursor
	vi.pendingSetCursor = nil

	switch ev.Type {
	case term.EventPasteStart:
		vi.pasteBuf.Reset()
		vi.pasteStarted = true
		handled = true
		return
	case term.EventPasteEnd:
		str := vi.pasteBuf.String()
		vi.pasteStarted = false
		if vi.mode() == insertMode && len(str) > 0 {
			vi.cursor.InsertString(str)
		}
		handled = true
		return
	}

	if vi.pasteStarted {
		if ev.Ch != 0 {
			vi.pasteBuf.WriteRune(ev.Ch)
		}
		handled = true
		return
	}

	// Edits made elsewhere can leave the cursor past the end of its line
	// or of the buffer; commands start from the nearest character, as the
	// cursor Vim keeps on the text would.
	vi.clampCursor()

	if vi.pendingSearchOp != nil {
		mode := vi.mode()
		defer vi.doneHandle(mode)
		quit, handled = vi.handleSearchOp(ev)
		return
	}

	if vi.pendingMarkOp != nil {
		mode := vi.mode()
		defer vi.doneHandle(mode)
		quit, handled = vi.handleMarkOp(ev)
		return
	}

	mode := vi.mode()
	defer vi.doneHandle(mode)

	switch mode {
	case searchMode:
		quit, handled = vi.handleSearch(ev)
	case normalMode:
		quit, handled = vi.handleNormal(ev)
	case insertMode:
		quit, handled = vi.handleInsert(ev)
	case gMode:
		quit, handled = vi.handleGo(ev)
	case zMode:
		quit, handled = vi.handleZ(ev)
	case yankMode:
		quit, handled = vi.handleYank(ev)
	case deleteMode:
		quit, handled = vi.handleDelete(ev)
	case caseChangeMode:
		quit, handled = vi.handleCaseChange(ev)
	case shiftMode:
		quit, handled = vi.handleShift(ev)
	case commentMode:
		quit, handled = vi.handleComment(ev)
	case visualMode, visualLineMode, visualBlockMode:
		quit, handled = vi.handleVisual(ev)
	case replaceMode:
		quit, handled = vi.handleReplace(ev)
	case replaceOneMode:
		quit, handled = vi.handleReplace(ev)
		if handled {
			vi.setNormalMode()
		}
	default:
		panic(fmt.Sprintf("unknown mode: %d", vi.currMode))
	}

	if vi.pendingInsertNormal && handled && mode != insertMode {
		if vi.insertNormalCommandComplete() {
			vi.returnToInsertAfterNormalCommand()
		} else if vi.mode() == insertMode {
			vi.pendingInsertNormal = false
		}
	}

	return
}

func (vi *viHandlerImpl) doneHandle(mode viMode) {
	vi.doMoveToBounds()
	if mode == normalMode || mode == visualMode /* clicks */ {
		vi.markMatchingBrace()
	}
}

func (vi *viHandlerImpl) moveToBounds() {
	vi.doMoveToBounds()
	vi.markMatchingBrace()
}

func (vi *viHandlerImpl) doMoveToBounds() {
	if vi.cursor.CursorAtScroll().Y < vi.less.Buffer().Rows() {
		vi.anchor = vi.cursorAtScroll()
	}
	vi.clampCursor()
}

func (vi *viHandlerImpl) clampCursor() {
	switch vi.mode() {
	case normalMode, yankMode, searchMode, zMode, gMode, deleteMode, caseChangeMode, shiftMode, commentMode:
		if vi.config.cursorCorrections {
			prevCoords := vi.cursor.Coordinates()
			vi.cursor.MoveToBounds(0)
			// Only vertical marking. Not horizontal because otherwise when scrolling
			// down with `j` or `k` from middle columns the cursor will start snapping
			// to shorter column indices as it comes across shorter text lines.
			if vi.cursor.Coordinates().Y != prevCoords.Y {
				vi.anchor = vi.cursorAtScroll()
			}
		}
	case insertMode, replaceMode, replaceOneMode,
		visualMode, visualLineMode, visualBlockMode:
		if vi.config.cursorCorrections {
			vi.cursor.MoveToBounds(1)
		}
	default:
		panic(fmt.Sprintf("unknown mode: %d", vi.currMode))
	}
}

func (vi *viHandlerImpl) OnWillSeek(from term.Coordinates) {}

func (vi *viHandlerImpl) OnDidSeek(from, to term.Coordinates) {}

func (vi *viHandlerImpl) OnWillHide(start, end int) {
}

func (vi *viHandlerImpl) OnWillVisible(start int) {
}

func (vi *viHandlerImpl) OnDidHide(start, end int) {
	// next tick because this callback is called before cursor calls MoveToScroll
	vi.config.scheduleNextTick(func() {
		vi.anchor = vi.cursorAtScroll()
	})
}

func (vi *viHandlerImpl) OnDidVisible(start int) {
	vi.config.scheduleNextTick(func() {
		vi.anchor = vi.cursorAtScroll()
	})
}

// moveToNextLocation moves the cursor to the next location
// in the location list identified by ID.
func (vi *viHandlerImpl) moveToNextLocation(ID string) bool {
	ok := vi.cursor.MoveToNextLocation(ID)
	vi.anchor = vi.cursorAtScroll()
	vi.markMatchingBrace()
	return ok
}

// moveToPrevLocation moves the cursor to the previous location
// in the location list identified by ID.
func (vi *viHandlerImpl) moveToPrevLocation(ID string) bool {
	ok := vi.cursor.MoveToPrevLocation(ID)
	vi.anchor = vi.cursorAtScroll()
	vi.markMatchingBrace()
	return ok
}

// setLocationList sets a location list of this handler. See Cursor.SetLocationList
func (vi *viHandlerImpl) setLocationList(
	pri textapi.LocationPriority, ID string, l text.LocationList,
) {
	_ = vi.cursor.SetLocationList(pri, ID, l)
}

// setCursorAtScroll sets the cursor of this viHandlerImpl handler at content pos.
func (vi *viHandlerImpl) setCursorAtScroll(pos term.Coordinates) bool {
	pos.Y = max(0, min(pos.Y, vi.less.Buffer().Rows()-1))
	pos.X = max(0, min(pos.X, vi.less.Buffer().Columns(pos.Y)))
	// setCursorAtScroll should be robust against resizes, etc.
	// only the first client interaction should clear this position
	if vi.less.Scroll().Width() == 0 || vi.less.Scroll().SizeHeight() == 0 {
		vi.pendingSetCursor = new(term.Coordinates)
		*vi.pendingSetCursor = pos
		return false
	}

	_, ok := vi.cursor.MoveToScroll(pos)
	vi.anchor = vi.cursorAtScroll()
	vi.markMatchingBrace()
	return ok
}

// CursorAtScroll sets the cursor of this viHandlerImpl handler at content pos.
func (vi *viHandlerImpl) cursorAtScroll() term.Coordinates {
	return vi.cursor.CursorAtScroll()
}

func (vi *viHandlerImpl) mode() viMode {
	if vi.currMode == normalMode && vi.less.Mode() != handler.LessNormalMode {
		return searchMode
	}
	return vi.currMode
}

func (vi *viHandlerImpl) unselect() bool {
	return vi.cursor.Unselect()
}

func (vi *viHandlerImpl) handleZ(ev term.Event) (quit, handled bool) {
	ctx := context.Background()
	var visual, stayZMode bool
	switch ev.Mod {
	case 0:
		switch ev.Key {
		case term.KeyEnter:
			repositioned := vi.cursor.RepositionTop()
			handled = vi.cursor.MoveStartLineNonBlank() || repositioned
		}
		switch ev.Ch {
		case '.':
			repositioned := vi.cursor.Center()
			handled = vi.cursor.MoveStartLineNonBlank() || repositioned
		case '-':
			repositioned := vi.cursor.RepositionBottom()
			handled = vi.cursor.MoveStartLineNonBlank() || repositioned
		case 'z':
			handled = vi.cursor.Center()
		case 'v', 'V':
			handled = vi.selectZRange()
			visual = handled
		case 'h', 'k':
			if vi.zRangeOK {
				next, ok := vi.cursor.ExpandRange(ctx, vi.zRange)
				handled = ok
				if handled {
					vi.zRange = next
					vi.highlightZRange()
				}
			}
			stayZMode = true
		case 'l', 'j':
			if vi.zRangeOK {
				next, ok := vi.cursor.ShrinkRange(ctx, vi.zRange, vi.cursor.CursorAtScroll())
				handled = ok
				if handled {
					vi.zRange = next
					vi.highlightZRange()
				}
			}
			stayZMode = true
		case 'H':
			pos := vi.cursor.CursorAtScroll()
			for range vi.less.Scroll().Width() / 2 {
				if !vi.less.Scroll().SeekLeft() {
					break
				}
			}
			win, _ := vi.cursor.WindowCoordinates(pos)
			if win.X >= 0 {
				vi.cursor.SetCursorAtScroll(pos)
			}
		case 'L':
			pos := vi.cursor.CursorAtScroll()
			width := vi.less.Scroll().Width()
			for range vi.less.Scroll().Width() / 2 {
				if !vi.less.Scroll().SeekRight() {
					break
				}
			}
			win, _ := vi.cursor.WindowCoordinates(pos)
			if win.X < width {
				vi.cursor.SetCursorAtScroll(pos)
			}
		case 't':
			handled = vi.cursor.RepositionTop()
		case 'b':
			handled = vi.cursor.RepositionBottom()
		case 'c':
			handled = vi.cursor.CollapseFold(ctx)
		case 'o':
			handled = vi.cursor.ExpandFold(ctx)
		case 'a':
			handled = vi.cursor.ToggleFold(ctx)
		case 'M', 'C': // neovim uses zM, but zC follows lower/upper case convention
			handled = vi.cursor.CollapseAllFolds(ctx)
		case 'R', 'O': // neovim has a recursive vs non-recursive option
			handled = vi.cursor.ExpandAllFolds(ctx)
		case 'A':
			handled = vi.cursor.ToggleAllFolds(ctx)
		default:
		}
	}
	if stayZMode {
		vi.setMode(zMode)
	} else if !visual {
		vi.clearZRangeHighlight()
		vi.setNormalMode()
	} else {
		vi.clearZRangeHighlight()
	}
	return
}
