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
	vi.shiftFn = fn
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

	vi.cursor.Paste(str, mode, after)
	return true
}

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
			by := vi.less.Scroll().SizeHeight() - pagePadding
			handled = vi.cursor.MoveDownLines(by)
			if handled {
				vi.cursor.RepositionTop()
			}
		case 'b':
			by := vi.less.Scroll().SizeHeight() - pagePadding
			handled = vi.cursor.MoveUpLines(by)
			if handled {
				vi.cursor.RepositionBottom()
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
			vi.nudgeCursorForTillRepeat(mode)
			vi.handleMoveToCharacter(mode, event)
		case ';':
			event := term.Event{Type: term.EventKey, Ch: vi.moveChar}
			vi.nudgeCursorForTillRepeat(vi.lastMoveMode)
			vi.handleMoveToCharacter(vi.lastMoveMode, event)
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
				vi.cursor.DeleteSelection()
			}
			vi.setInsertMode()
		case 'D':
			if vi.cursor.Select() {
				vi.cursor.MoveEndLine()
				vi.cursor.DeleteSelection()
			}
		case 'X':
			for range vi.count {
				if !vi.cursor.Backspace() {
					break
				}
			}
		case 'x':
			vi.cursor.Delete()
		case '~':
			vi.cursor.ToggleCase()
		case 's':
			vi.cursor.Delete()
			vi.setInsertMode()
		case 'S':
			vi.cursor.MoveStartLine()
			if vi.cursor.Select() {
				vi.cursor.MoveEndLine()
				vi.cursor.DeleteSelection()
				vi.cursor.TryIndent(vi.config.indentRune, vi.config.indentTabspaces)
			}
			vi.setInsertMode()
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
			vi.cursor.MoveNextParagraphs(vi.count)
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
	if ev.Type != term.EventKey || ev.Mod != 0 {
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
	if !vi.cursor.SelectLine() {
		return false
	}
	count := vi.motionCount()
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
	mode, _ := vi.cursor.SelectionMode()
	data := clipboard.Data{Text: vi.cursor.Selection(), Metadata: mode}
	reg := vi.consumeActiveRegister()
	_, err := vi.cursor.CopySelection(registerNameToID(reg), vi.config.clipboard)
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

func (vi *viHandlerImpl) copySuppressed() bool {
	return vi.suppressCopyDelete
}

func (vi *viHandlerImpl) copySelectionForDelete() {
	reg := vi.consumeActiveRegister()
	if reg == blackHoleRegister {
		return
	}

	mode, _ := vi.cursor.SelectionMode()
	data := clipboard.Data{Text: vi.cursor.Selection(), Metadata: mode}
	if reg != unnamedRegister {
		if err := vi.writeRegister(reg, data); err != nil {
			vi.logError(err)
		}
	}
	if err := vi.writeRegister(unnamedRegister, data); err != nil {
		vi.logError(err)
	}
	if mode != text.LineSelection {
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
			vi.cursor.ShiftSelectionRight(vi.config.indentRune, vi.config.indentTabspaces)
			vi.setNormalMode()
		case '<':
			vi.cursor.ShiftSelectionLeft(vi.config.indentRune, vi.config.indentTabspaces)
			vi.setNormalMode()
		case '=':
			vi.cursor.ReindentSelection(vi.config.indentRune, vi.config.indentTabspaces)
			vi.setNormalMode()
		case 'y':
			vi.copySelection()
			vi.setNormalMode()
		case 'J':
			vi.joinSelection(vi.cursor.Join)
			vi.setNormalMode()
		case 'd', 'x':
			mode, modeOk := vi.cursor.SelectionMode()
			vi.copySelectionForDelete()
			// A block delete runs one edit per row; hold the
			// copy-on-delete writes so only the copy above lands.
			vi.suppressCopyDelete = modeOk && mode == text.BlockSelection
			vi.cursor.DeleteSelection()
			vi.suppressCopyDelete = false
			vi.setNormalMode()
		case 's', 'c':
			switch vi.mode() {
			case visualBlockMode:
				vi.handleVisualBlockChangeStart()
			default:
				vi.copySelectionForDelete()
				vi.cursor.DeleteSelection()
				vi.setInsertMode()
			}
		case 'u':
			vi.cursor.LowercaseSelection()
			vi.setNormalMode()
		case 'U':
			vi.cursor.UppercaseSelection()
			vi.setNormalMode()
		case '~':
			vi.cursor.ToggleCaseSelection()
			vi.setNormalMode()
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
	switch ev.Mod {
	case 0:
		switch ev.Type {
		case term.EventKey:
			switch ev.Key {
			case term.KeySpace:
				ev.Ch = ' '
			case term.KeyTab:
				ev.Ch = '\t'
			case term.KeyEnter:
				ev.Ch = '\n'
			}
			var ok bool
			switch mode {
			case moveToNext:
				ok = vi.repeatMotion(func() bool { return vi.cursor.MoveToNextChar(ev.Ch) })
			case moveToPrev:
				ok = vi.repeatMotion(func() bool { return vi.cursor.MoveToPrevChar(ev.Ch) })
			case moveTillNext:
				if ok = vi.repeatMotion(func() bool { return vi.cursor.MoveToNextChar(ev.Ch) }); ok {
					vi.cursor.MoveLeft()
				}
			case moveTillPrev:
				if ok = vi.repeatMotion(func() bool { return vi.cursor.MoveToPrevChar(ev.Ch) }); ok {
					vi.cursor.MoveRight()
				}
			case moveNone:
				return
			}
			if ok {
				vi.moveChar = ev.Ch
			}
			vi.setNormalMode()
			handled = true
		default:
			vi.setMode(vi.mode())
			handled = true
		}
	}
	return
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
	quit, handled = vi.handleNormal(ev)
	isMoveSwitch := prevMode == moveNone && vi.moveMode != moveNone
	after := vi.cursor.CursorAtScroll()

	if before == after {
		vi.cursor.Unselect()
		if !isMoveSwitch {
			handled = false
			vi.setNormalMode()
		}
		return
	}

	done = true

	// handle <op>wWeEbB idiosyncrasies
	after = vi.cursor.CursorAtScroll()
	switch ev.Mod {
	case 0:
		switch ev.Ch {
		case 'e', 'E':
		case 'w', 'W':
			if before.Y < after.Y && vi.less.Buffer().Columns(before.Y) == 0 {
				vi.cursor.MoveToScroll(before)
				vi.cursor.SelectLine()
				break
			}
			vi.cursor.MoveLeft()
			if before.Y < after.Y && !vi.cursor.MoveLeftEndWord() {
				vi.cursor.MoveLeftWrap()
			}
		case 'b', 'B':
			if before.Y > after.Y {
				vi.cursor.MoveLeftStartWord()
			}
		default:
			if before.Y != after.Y {
				vi.cursor.SelectLine()
			}
		}
	}
	return
}

func (vi *viHandlerImpl) handleMetaGo(ev term.Event) (quit, handled, done bool) {
	before := vi.cursor.CursorAtScroll()
	vi.cursor.Select()

	switch ev.Mod {
	case 0:
		switch ev.Ch {
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
		case 'e':
			vi.repeatMotion(vi.cursor.MoveLeftEndWord)
			handled = true
		case 'E':
			vi.repeatMotion(vi.cursor.MoveLeftEndWordGroup)
			handled = true
		case 'j':
			vi.repeatMotion(vi.cursor.MoveDisplayDown)
			handled = true
		case 'k':
			vi.repeatMotion(vi.cursor.MoveDisplayUp)
			handled = true
		case '_':
			vi.cursor.MoveEndLineNonBlank()
			handled = true
		}
	}

	after := vi.cursor.CursorAtScroll()
	if before == after {
		vi.cursor.Unselect()
		handled = false
		vi.setNormalMode()
		return
	}

	done = true
	return
}

func (vi *viHandlerImpl) handleYank(ev term.Event) (quit, handled bool) {
	if ev.Ch == 'y' && ev.Mod == 0 {
		if vi.selectLineCount() {
			vi.copySelection()
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
			vi.pendingGoMotion = false
			switch ev.Ch {
			case 'g':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'g'})
				if !done {
					return quit, handled
				}
				vi.commentFn()
				vi.cursor.Unselect()
				vi.setNormalMode()
				return quit, true
			case 'e':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'e'})
				if !done {
					return quit, handled
				}
				vi.copySelection()
				vi.setNormalMode()
				return quit, true
			case 'E':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'E'})
				if !done {
					return quit, handled
				}
				vi.copySelection()
				vi.setNormalMode()
				return quit, true
			case 'j':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'j'})
				if !done {
					return quit, handled
				}
				vi.copySelection()
				vi.setNormalMode()
				return quit, true
			case 'k':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'k'})
				if !done {
					return quit, handled
				}
				vi.copySelection()
				vi.setNormalMode()
				return quit, true
			case '_':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: '_'})
				if !done {
					return quit, handled
				}
				vi.copySelection()
				vi.setNormalMode()
				return quit, true
			default:
				vi.setNormalMode()
				return false, true
			}
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

	vi.copySelection()
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
			vi.pendingGoMotion = false
			switch ev.Ch {
			case 'g':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'g'})
				if !done {
					return quit, handled
				}
				vi.commentFn()
				vi.cursor.Unselect()
				vi.setNormalMode()
				return quit, true
			case 'e':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'e'})
				if !done {
					return quit, handled
				}
				vi.shiftFn()
				vi.setNormalMode()
				return quit, true
			case 'E':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'E'})
				if !done {
					return quit, handled
				}
				vi.shiftFn()
				vi.setNormalMode()
				return quit, true
			case 'j':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'j'})
				if !done {
					return quit, handled
				}
				vi.shiftFn()
				vi.setNormalMode()
				return quit, true
			case 'k':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'k'})
				if !done {
					return quit, handled
				}
				vi.shiftFn()
				vi.setNormalMode()
				return quit, true
			case '_':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: '_'})
				if !done {
					return quit, handled
				}
				vi.shiftFn()
				vi.setNormalMode()
				return quit, true
			default:
				vi.setNormalMode()
				return false, true
			}
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
		vi.cursor.MoveStartLine()
		if vi.cursor.Select() {
			vi.cursor.MoveEndLine()
			vi.caseChangeFn()
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
			vi.pendingGoMotion = false
			switch ev.Ch {
			case 'e':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'e'})
				if !done {
					return quit, handled
				}
				vi.caseChangeFn()
				vi.setNormalMode()
				return quit, true
			case 'E':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'E'})
				if !done {
					return quit, handled
				}
				vi.caseChangeFn()
				vi.setNormalMode()
				return quit, true
			case 'j':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'j'})
				if !done {
					return quit, handled
				}
				vi.caseChangeFn()
				vi.setNormalMode()
				return quit, true
			case 'k':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'k'})
				if !done {
					return quit, handled
				}
				vi.caseChangeFn()
				vi.setNormalMode()
				return quit, true
			case '_':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: '_'})
				if !done {
					return quit, handled
				}
				vi.caseChangeFn()
				vi.setNormalMode()
				return quit, true
			default:
				vi.setNormalMode()
				return false, true
			}
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
			vi.caseChangeFn()
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

	vi.caseChangeFn()
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
			vi.pendingGoMotion = false
			switch ev.Ch {
			case 'g':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'g'})
				if !done {
					return quit, handled
				}
				vi.commentFn()
				vi.cursor.Unselect()
				vi.setNormalMode()
				return quit, true
			case 'e':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'e'})
				if !done {
					return quit, handled
				}
				vi.commentFn()
				vi.cursor.Unselect()
				vi.setNormalMode()
				return quit, true
			case 'E':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'E'})
				if !done {
					return quit, handled
				}
				vi.commentFn()
				vi.cursor.Unselect()
				vi.setNormalMode()
				return quit, true
			case 'j':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'j'})
				if !done {
					return quit, handled
				}
				vi.commentFn()
				vi.cursor.Unselect()
				vi.setNormalMode()
				return quit, true
			case 'k':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'k'})
				if !done {
					return quit, handled
				}
				vi.commentFn()
				vi.cursor.Unselect()
				vi.setNormalMode()
				return quit, true
			case '_':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: '_'})
				if !done {
					return quit, handled
				}
				vi.commentFn()
				vi.cursor.Unselect()
				vi.setNormalMode()
				return quit, true
			default:
				vi.setNormalMode()
				return false, true
			}
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
		vi.copySelection,
		func() { vi.setNormalMode() },
	)
}

// beginDeleteSearchOp arms the search-pending state for the d/c
// operators. The motion is charwise. When the operator was invoked as
// `c` (deleteInsert), the cursor enters insert mode after the deletion.
func (vi *viHandlerImpl) beginDeleteSearchOp(direction moveMode) {
	deleteInsert := vi.deleteInsert
	vi.beginSearchOp(direction, false,
		func() {
			vi.copySelectionForDelete()
			vi.cursor.DeleteSelection()
		},
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
		func() { caseChangeFn() },
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
	}

	if !vi.cursor.SelectRange(from, to) {
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
	if linewise {
		// Linewise selection: anchor at the starting line, then
		// extend down or up to the mark line. SelectLine produces a
		// proper line selection (including trailing newline) that
		// operators like d, c, y can act on as whole-line ranges.
		vi.cursor.MoveToScroll(before)
		if !vi.cursor.SelectLine() {
			vi.cancelMarkOp()
			return false, true
		}
		if after.Y > before.Y {
			for i := before.Y; i < after.Y; i++ {
				if !vi.cursor.MoveLineDown() {
					break
				}
			}
		} else if after.Y < before.Y {
			for i := before.Y; i > after.Y; i-- {
				if !vi.cursor.MoveLineUp() {
					break
				}
			}
		}
	} else {
		from, to := before, after
		if to.Y < from.Y || (to.Y == from.Y && to.X < from.X) {
			from, to = to, from
		}
		if !vi.cursor.SelectRange(from, to) {
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
		vi.copySelection,
		func() { vi.setNormalMode() },
	)
}

// beginDeleteMarkOp arms the mark-pending state for d/c.
func (vi *viHandlerImpl) beginDeleteMarkOp(linewise bool) {
	deleteInsert := vi.deleteInsert
	vi.beginMarkOp(linewise, false,
		func() {
			vi.copySelectionForDelete()
			vi.cursor.DeleteSelection()
		},
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
		func() { caseChangeFn() },
		func() { vi.setNormalMode() },
	)
}

func (vi *viHandlerImpl) handleDelete(ev term.Event) (quit, handled bool) {
	if !vi.deleteInsert && vi.moveMode == moveNone && ev.Ch == 'd' && ev.Mod == 0 {
		if !vi.cursor.SelectLine() {
			return
		}
		count := vi.motionCount()
		if count > 1 {
			for i := 0; i < count && vi.cursor.MoveLineDown(); i++ {
			}
		}
		vi.copySelectionForDelete()
		vi.cursor.DeleteSelection()
		vi.setNormalMode()
		handled = true
		return
	}

	if vi.deleteInsert && vi.moveMode == moveNone && ev.Ch == 'c' && ev.Mod == 0 {
		vi.cursor.MoveStartLine()
		if vi.cursor.Select() {
			vi.cursor.MoveEndLine()
			vi.copySelectionForDelete()
			vi.cursor.DeleteSelection()
			vi.cursor.TryIndent(vi.config.indentRune, vi.config.indentTabspaces)
		}
		vi.setInsertMode()
		handled = true
		return
	}
	if vi.parseOperatorCountDigit(ev) {
		return false, true
	}

	if vi.moveMode == moveNone && ev.Mod == 0 {
		if vi.pendingGoMotion {
			vi.pendingGoMotion = false
			switch ev.Ch {
			case 'e':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'e'})
				if !done {
					return quit, handled
				}
				vi.copySelectionForDelete()
				vi.cursor.DeleteSelection()
				if vi.deleteInsert {
					vi.setInsertMode()
				} else {
					vi.setNormalMode()
				}
				return quit, true
			case 'E':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'E'})
				if !done {
					return quit, handled
				}
				vi.copySelectionForDelete()
				vi.cursor.DeleteSelection()
				if vi.deleteInsert {
					vi.setInsertMode()
				} else {
					vi.setNormalMode()
				}
				return quit, true
			case 'j':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'j'})
				if !done {
					return quit, handled
				}
				vi.copySelectionForDelete()
				vi.cursor.DeleteSelection()
				if vi.deleteInsert {
					vi.setInsertMode()
				} else {
					vi.setNormalMode()
				}
				return quit, true
			case 'k':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: 'k'})
				if !done {
					return quit, handled
				}
				vi.copySelectionForDelete()
				vi.cursor.DeleteSelection()
				if vi.deleteInsert {
					vi.setInsertMode()
				} else {
					vi.setNormalMode()
				}
				return quit, true
			case '_':
				quit, handled, done := vi.handleMetaGo(term.Event{Type: ev.Type, Mod: ev.Mod, Ch: '_'})
				if !done {
					return quit, handled
				}
				vi.copySelectionForDelete()
				vi.cursor.DeleteSelection()
				if vi.deleteInsert {
					vi.setInsertMode()
				} else {
					vi.setNormalMode()
				}
				return quit, true
			default:
				vi.setNormalMode()
				return false, true
			}
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
			vi.copySelectionForDelete()
			vi.cursor.DeleteSelection()
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
	quit, handled, done = vi.handleMetaNormal(ev)
	if !done {
		return
	}

	vi.copySelectionForDelete()
	vi.cursor.DeleteSelection()
	if vi.deleteInsert {
		vi.setInsertMode()
	} else {
		vi.setNormalMode()
	}
	return
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
