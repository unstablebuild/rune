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

package text

import (
	"bufio"
	"context"
	"maps"
	"math"
	"sort"
	"strings"
	"unicode"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
)

// SelectMode represents a select mode.
type SelectMode uint8

const (
	// NoSelection represents the mode where there's no selection.
	NoSelection SelectMode = iota
	// StandardSelection represents a select mode. See Select for more details.
	StandardSelection
	// LineSelection represents a select mode. See SelectLine for more details.
	LineSelection
	// BlockSelection represents a select mode. See SelectBlock for more details.
	BlockSelection
)

func (s SelectMode) String() string {
	switch s {
	case NoSelection:
		return "nop"
	case StandardSelection:
		return "standard"
	case LineSelection:
		return "line"
	case BlockSelection:
		return "block"
	}
	panic("unknown selection mode")
}

const (
	searchLocationListID         = "search"
	selectionLocationListID      = "selection"
	internalLocationListPriority = textapi.LocationPriorityCritical
)

// used to subscribe to buffer updates
type curSubscriber struct {
	mark CursorMark
	c    *Cursor
}

type message struct {
	listID   string
	location textapi.Location
}

// Cursor is a helper structure which manages a cursor over a Scroll.
type Cursor struct {
	// RightInclusiveSemantics changes the cursor selection behaviour to
	// add the current cursor position to the selection.
	RightInclusiveSemantics bool

	ctx        context.Context
	scroll     *component.Scroll
	search     string
	cursor     term.Coordinates
	shouldSeek bool
	searchAttr term.Attributes

	scheduleNextTick func(func()) bool

	locationStore LocationStore
	commentSpec   CommentSpec

	selection struct {
		mode       SelectMode
		scrollFrom term.Coordinates
		scrollTo   term.Coordinates
		explicit   bool
		cells      [][]term.Cell
	}
	selectionHistory selectionHistory
	subscriber       curSubscriber
}

// selectionSnapshot captures the state needed to restore a prior selection,
// including the caret position so undo/redo returns the caret where it was.
type selectionSnapshot struct {
	mode       SelectMode
	scrollFrom term.Coordinates
	scrollTo   term.Coordinates
	explicit   bool
	cursor     term.Coordinates
}

const maxSelectionHistory = 128

type selectionHistory struct {
	undo []selectionSnapshot
	redo []selectionSnapshot
	// suppress prevents recording while restoring a snapshot, so undo/redo
	// navigation does not itself generate new history entries.
	suppress bool
}

// NewCursor allocates storage for a new cursor,
// initializes it with an empty Scroll, and returns it.
func NewCursor(scroll *component.Scroll, scheduleNextTick func(func()) bool) *Cursor {
	c := new(Cursor)
	c.Init(scroll, scheduleNextTick)
	return c
}

// Center centers the cursor at the scroll such that the cursor
// occupies the line at the center of the view.
func (c *Cursor) Center() (handled bool) {
	return c.repositionLine(
		c.scroll.SizeHeight()/2, c.scroll.RepositionLineCenter)
}

// RepositionTop repositions the cursor at the scroll such that the cursor
// occupies the line at the top of the view.
func (c *Cursor) RepositionTop() (handled bool) {
	return c.repositionLine(0, c.scroll.RepositionLineTop)
}

// RepositionBottom repositions the cursor at the scroll such that the cursor
// occupies the line at the bottom of the view.
func (c *Cursor) RepositionBottom() (handled bool) {
	return c.repositionLine(
		c.scroll.SizeHeight()-1, c.scroll.RepositionLineBottom)
}

func (c *Cursor) repositionLine(target int, reposition func(int) int) bool {
	if c.scroll.SizeHeight() == 0 || c.scroll.Width() == 0 {
		return false
	}
	seeked := reposition(c.cursorAtScroll().Y)
	c.cursor.Y -= seeked
	pastEnd := c.cursor.Y - target
	if pastEnd <= 0 || c.scroll.InvertOffset {
		return seeked != 0
	}
	offset := c.scroll.Offset()
	offset.Y += pastEnd
	c.scroll.SetOffset(offset)
	c.cursor.Y = target
	return true
}

// MoveToWindow moves the cursor to the given visible window coordinates in the
// current viewport. The target is clamped to the current viewport bounds. If the
// requested position falls below the last visible content row, the cursor moves
// to the last visible content row instead.
func (c *Cursor) MoveToWindow(pos term.Coordinates) (ok bool) {
	height := c.scroll.SizeHeight()
	width := c.scroll.Width()
	if height == 0 || width == 0 {
		return false
	}
	pos.Y = max(0, min(pos.Y, height-1))
	pos.X = max(0, min(pos.X, width-1))
	atScroll := c.scroll.WindowToScrollCoordinates(pos)
	for atScroll.Y >= c.rows() && pos.Y > 0 {
		pos.Y--
		atScroll = c.scroll.WindowToScrollCoordinates(pos)
	}
	if atScroll.Y >= c.rows() {
		return false
	}
	_, ok = c.MoveToScroll(atScroll)
	return ok
}

// MoveToWindowTop moves the cursor to the first visible window row.
func (c *Cursor) MoveToWindowTop() bool {
	pos := c.cursor
	pos.Y = 0
	return c.MoveToWindow(pos)
}

// MoveToWindowMiddle moves the cursor to the middle visible window row.
func (c *Cursor) MoveToWindowMiddle() bool {
	pos := c.cursor
	pos.Y = c.scroll.SizeHeight() / 2
	return c.MoveToWindow(pos)
}

// MoveToWindowBottom moves the cursor to the last visible window row.
func (c *Cursor) MoveToWindowBottom() bool {
	pos := c.cursor
	pos.Y = c.scroll.SizeHeight() - 1
	return c.MoveToWindow(pos)
}

// Init initializes this cursor with the given scroll and subscribes
// to changes to the scroll's buffer. If buffer is swapped
// via Scroll.SetBuffer, consider re-initializing this cursor with the
// updated scroll, unless it's a temporary swap.
// Also, once initialized this cursor MUST NOT be copied.
func (c *Cursor) Init(scroll *component.Scroll, scheduleNextTick func(func()) bool) {
	c.InitPerformance(scroll)
	c.scroll.Buffer().Subscribe(&c.subscriber)
	c.scroll.Subscribe(&c.subscriber)
	c.shouldSeek = true
	c.scheduleNextTick = scheduleNextTick
}

// InitPerformance initializes this Cursor with a Scroll
// that was initialized with InitPerformance. It also
// disables automatic scrolling of content for the client.
// It also disables all fold-related methods.
func (c *Cursor) InitPerformance(scroll *component.Scroll) {
	c.ctx = context.Background()
	c.cursor = term.Coordinates{}
	c.scroll = scroll
	c.selection.mode = NoSelection
	c.subscriber.c = c
	c.locationStore.Init()

	// zero-out scroll attributes so we can better control
	// what gets highlighted upon search/search word, etc.
	c.searchAttr = scroll.ResultsAttr
	scroll.ResultsAttr = term.Attributes{}
}

func (c *curSubscriber) OnWillEdit(
	ctx context.Context, start, end term.Coordinates, str string,
) {
	c.c.Unselect()
}

func (c *curSubscriber) OnDidEdit(
	ctx context.Context, from, to term.Coordinates, old string,
) {
	c.c.setSearchLocationList(c.c.search, false)
}

func (c *curSubscriber) OnWillSeek(from term.Coordinates) {
}

func (c *curSubscriber) OnDidSeek(from, to term.Coordinates) {
}

func (c *curSubscriber) OnWillHide(start, end int) {
	c.mark = c.c.Mark()
}

func (c *curSubscriber) OnDidHide(start, end int) {
	c.c.MoveToMark(c.mark)
}

func (c *curSubscriber) OnWillVisible(start int) {
	c.mark = c.c.Mark()
}

func (c *curSubscriber) OnDidVisible(start int) {
	c.c.MoveToMark(c.mark)
}

// Coordinates returns the current position of the cursor.
func (c *Cursor) Coordinates() term.Coordinates {
	return c.cursor
}

// View returns the underlying cell.Buffer view.
func (c *Cursor) View() cell.View {
	return c.view()
}

func (c *Cursor) view() cell.View {
	return c.buffer().View()
}

func (c *Cursor) buffer() *cell.Buffer {
	return c.scroll.Buffer()
}

// CursorMark is used with Mark and MoveToMark to
// move the cursor to previously marked positions.
type CursorMark struct {
	// keep it private so cursor position semantics are hidden from clients
	scroll term.Coordinates
	window term.Coordinates
}

// Before returns true if other is before CursorMark.
func (c CursorMark) Before(other term.Coordinates) bool {
	res := term.CoordinatesDiff(c.window, other)
	return res.Y < 0 || res.Y == 0 && res.X < 0
}

// Mark returns the current cursor position as a CursorMark
// to later be used in calls to MoveToMark.
func (c *Cursor) Mark() CursorMark {
	return CursorMark{
		scroll: c.cursorAtScroll(),
		window: c.cursor,
	}
}

// MoveToMark moves the cursor to the position represented by mark.
// It attempts to keep cursor in the same window position as it was
// when the given mark was created.
func (c *Cursor) MoveToMark(mark CursorMark) (CursorMark, bool) {
	ret := CursorMark{window: c.cursor, scroll: c.cursorAtScroll()}
	pos := mark.scroll
	pos.Y = max(0, min(pos.Y, c.rows()-1))
	pos.X = max(0, min(pos.X, c.view().Columns(pos.Y)))
	win, _ := c.scroll.ScrollToWindowCoordinates(pos)
	// if inside hidden block, we still want to make the best out of it
	c.setCursor(win, false)
	// try to keep cursor at the same window position, if possible
	if c.cursor.Y > mark.window.Y {
		diff := c.cursor.Y - mark.window.Y
		for diff > 0 && c.cursor.Y > 0 && c.scroll.SeekDown() {
			c.cursor.Y--
			diff--
		}
	} else if c.cursor.Y < mark.window.Y {
		diff := mark.window.Y - c.cursor.Y
		for diff > 0 && c.cursor.Y < c.scroll.SizeHeight() && c.scroll.SeekUp() {
			c.cursor.Y++
			diff--
		}
	}
	return ret, c.cursorAtScroll() != ret.scroll
}

func (c *Cursor) rows() int {
	return c.view().Rows()
}

// CursorAtScroll returns the current position of the cursor relative
// to the scroll coorindates.
func (c *Cursor) CursorAtScroll() term.Coordinates {
	return c.cursorAtScroll()
}

// SetCursorAtScroll moves the cursor to the given scroll position.
func (c *Cursor) SetCursorAtScroll(pos term.Coordinates) term.Coordinates {
	prev := c.scroll.WindowToScrollCoordinates(c.cursor)
	win, _ := c.scroll.ScrollToWindowCoordinates(pos)
	c.setCursor(win, false)
	return prev
}

// SelectionFrom returns the position of the current selection,
// if cursor is in select mode.
func (c *Cursor) SelectionFrom() (pos term.Coordinates, ok bool) {
	if c.selection.mode == NoSelection {
		return
	}
	ok = true
	pos = c.selection.scrollFrom
	return
}

// SwapSelectionEnd swaps the current cursor position with the selection anchor.
// It only applies to non-explicit selections.
func (c *Cursor) SwapSelectionEnd() bool {
	if c.selection.mode == NoSelection || c.selection.explicit {
		return false
	}

	enable := c.disablePublishing()
	defer enable()

	anchor, ok := c.clampSelectionCoordinates(c.selection.scrollFrom)
	if !ok {
		return false
	}
	current, ok := c.cursorAtScrollBounds()
	if !ok {
		return false
	}

	if anchor == current {
		return false
	}

	c.selection.scrollFrom = current
	c.moveToScroll(anchor)
	c.setSelection()
	return true
}

// SwapSelectionCorner swaps the horizontal block corner on the current row.
// It only applies to non-explicit block selections.
func (c *Cursor) SwapSelectionCorner() bool {
	if c.selection.mode != BlockSelection || c.selection.explicit {
		return false
	}

	enable := c.disablePublishing()
	defer enable()

	anchor, ok := c.clampSelectionCoordinates(c.selection.scrollFrom)
	if !ok {
		return false
	}
	current, ok := c.cursorAtScrollBounds()
	if !ok {
		return false
	}

	target, ok := c.clampSelectionCoordinates(term.Coordinates{X: anchor.X, Y: current.Y})
	if !ok {
		return false
	}
	newAnchor, ok := c.clampSelectionCoordinates(term.Coordinates{X: current.X, Y: anchor.Y})
	if !ok {
		return false
	}

	if target == current && newAnchor == anchor {
		return false
	}

	c.selection.scrollFrom = newAnchor
	c.moveToScroll(target)
	c.setSelection()
	return true
}

// MoveToScroll moves the cursor to pos in scroll.
func (c *Cursor) MoveToScroll(pos term.Coordinates) (
	ret term.Coordinates, ok bool,
) {
	enable := c.disablePublishing()
	defer enable()

	pos.Y = max(0, pos.Y)
	pos.X = max(0, pos.X)
	ret = c.cursorAtScroll()
	c.moveToScroll(pos)
	ok = ret != c.cursorAtScroll()
	return
}

// the bounds of the current view, then underlying scroll is used
// to seek to pos.
func (c *Cursor) moveToScroll(pos term.Coordinates) {
	var windowPos term.Coordinates
	if c.scroll.Width() == 0 && c.scroll.Wrap {
		// best effort, assume no wrap when width is 0
		windowPos = term.CoordinatesDiff(pos, c.scroll.Offset())
	} else {
		windowPos, _ = c.scroll.ScrollToWindowCoordinates(pos)
	}
	c.setCursor(windowPos, c.shouldSeek)
}

func (c *Cursor) seekToScrollCoordinates() {
	if c.scroll.Width() == 0 || c.scroll.SizeHeight() == 0 {
		return
	}
	pos := c.cursor
	// scroll can return some coordinates that are be outside
	// of the bounds of the current window.
	// For instance, if DeleteCell deletes a tab, it could be that
	// pos.X at scroll yields a negative coordinate
	// (i.c. -3 if tabspaces is 4)
	for !c.scroll.Wrap && pos.X < 0 && c.scroll.SeekLeft() {
		pos.X++
	}
	for pos.Y < 0 && c.scroll.SeekUp() {
		pos.Y++
	}
	for !c.scroll.Wrap && pos.X > 0 && pos.X >= c.scroll.Width() && c.scroll.SeekRight() {
		pos.X--
	}

	for pos.Y > 0 && pos.Y >= c.scroll.SizeHeight() && c.scroll.SeekDown() {
		pos.Y--
	}
	c.cursor = pos
}

// note that pos is window coordinates, not scroll coordinates
func (c *Cursor) setCursor(pos term.Coordinates, seek bool) {
	if c.cursor == pos {
		// even if pos is the same, content could have scrolled
		if c.selection.mode != NoSelection {
			c.setSelection()
		}
		return
	}

	c.cursor = pos

	// this is an optimization to disable expensive calculations
	// during composite moves that call setCursor multiple times
	if seek {
		c.seekToScrollCoordinates()
	}

	if c.selection.mode != NoSelection {
		c.setSelection()
	}
}

// Search searches text string in the underlying cell buffer. It returns
// the number of occurrences found.
func (c *Cursor) Search(text string) int {
	n := c.setSearchLocationList(text, false /* word */)
	return n
}

// SearchWord searches the given word in the underlying cell buffer. It returns
// the number of occurrences found.
func (c *Cursor) SearchWord(text string) int {
	n := c.setSearchLocationList(text, true /* word */)
	return n
}

func isBeforeCursorOrInsideHiddenBlock(c *Cursor, cursor, pos term.Coordinates) bool {
	return pos.Y < cursor.Y || (pos.Y == cursor.Y && pos.X <= cursor.X) ||
		isInsideHiddenBlock(c, cursor, pos)
}

func isInsideHiddenBlock(c *Cursor, cursor, pos term.Coordinates) bool {
	at, ok := c.scroll.ScrollToWindowCoordinates(pos)
	return !ok && at.Y == cursor.Y
}

func isPastCursor(c *Cursor, cursor, pos term.Coordinates) bool {
	return pos.Y > cursor.Y || (pos.Y == cursor.Y && pos.X >= cursor.X)
}

// MoveToNextMatch moves the cursor to the next search result if any.
func (c *Cursor) MoveToNextMatch() (ok bool) {
	return c.MoveToNextLocation(searchLocationListID)
}

// MoveToPrevMatch moves the cursor to the next search result if any.
func (c *Cursor) MoveToPrevMatch() (ok bool) {
	return c.MoveToPrevLocation(searchLocationListID)
}

// MoveStartLine moves the cursor at the start of the current line, scrolling
// to the start of the line if required.
func (c *Cursor) MoveStartLine() (ok bool) {
	_, ok = c.MoveToScroll(term.Coordinates{Y: c.cursorAtScroll().Y})
	return
}

// MoveStartLineNonBlank moves the cursor at the first character that's not
// blank (e.g. space, tab, etc.) in the current line.
func (c *Cursor) MoveStartLineNonBlank() bool {
	enable := c.disablePublishing()
	defer enable()

	initialScrollPos := c.cursorAtScroll()
	cells := c.view().RawCells()
	if initialScrollPos.Y < 0 || initialScrollPos.Y >= len(cells) {
		return false
	}
	line := cells[initialScrollPos.Y]

	for x := 0; ; x++ {
		if x >= len(line) {
			return false
		}

		pos, _ := c.scroll.ScrollToWindowCoordinates(
			term.Coordinates{X: x, Y: initialScrollPos.Y})
		c.setCursor(pos, false)

		cell, ok := c.cellAtCursor()
		if !ok {
			return false
		}
		if !isOneOf(cell, blankCharacters) {
			return true
		}
	}
}

// MoveEndLine moves the cursor at the end of the current line, scrolling
// to the end of the line if required.
func (c *Cursor) MoveEndLine() (ok bool) {
	curr := c.cursorAtScroll()
	if curr.Y >= c.rows() {
		return
	}
	x := c.view().Columns(curr.Y)
	_, ok = c.MoveToScroll(term.Coordinates{Y: curr.Y, X: x})
	return
}

// MoveEndLineNonBlank moves the cursor to the last non-blank character
// of the current line. If the line is empty or all blanks, the cursor
// stays at position 0.
func (c *Cursor) MoveEndLineNonBlank() bool {
	enable := c.disablePublishing()
	defer enable()

	initialScrollPos := c.cursorAtScroll()
	cells := c.view().RawCells()
	if initialScrollPos.Y < 0 || initialScrollPos.Y >= len(cells) {
		return false
	}
	line := cells[initialScrollPos.Y]

	lastNonBlank := -1
	for x := range len(line) {
		pos, _ := c.scroll.ScrollToWindowCoordinates(
			term.Coordinates{X: x, Y: initialScrollPos.Y})
		c.setCursor(pos, false)

		cell, ok := c.cellAtCursor()
		if !ok {
			break
		}
		if !isOneOf(cell, blankCharacters) {
			lastNonBlank = x
		}
	}

	if lastNonBlank < 0 {
		// All blank or empty line — move to start of line
		pos, _ := c.scroll.ScrollToWindowCoordinates(
			term.Coordinates{X: 0, Y: initialScrollPos.Y})
		c.setCursor(pos, false)
		return false
	}

	pos, _ := c.scroll.ScrollToWindowCoordinates(
		term.Coordinates{X: lastNonBlank, Y: initialScrollPos.Y})
	c.setCursor(pos, false)
	return true
}

// MoveFirstLine moves the cursor to the first line, scrolling the content
// if appplicable.
func (c *Cursor) MoveFirstLine() (ok bool) {
	_, ok = c.MoveToScroll(term.Coordinates{X: c.cursorAtScroll().X})
	return
}

// MoveLastLine moves the cursor to the last line, scrolling the content
// if required.
func (c *Cursor) MoveLastLine() (ok bool) {
	max := c.rows()
	if max == 0 {
		return
	}
	_, ok = c.MoveToScroll(term.Coordinates{X: c.cursorAtScroll().X, Y: max - 1})
	return ok
}

// MoveDown moves the cursor one line below the current line, scrolling
// the content if required. It returns false and does nothing when the end
// of the content is reached.
func (c *Cursor) MoveDown() (ok bool) {
	atScroll := c.cursorAtScroll()
	if atScroll.Y >= c.rows()-1 {
		if c.scroll.Wrap && c.scroll.SeekDown() {
			// last line is longer than the entire height x width
			// allow user to scroll down by moving up and down the window axis
			return c.tryMoveDownWindowRow()
		}
		return
	}
	atScroll.Y++
	_, ok = c.MoveToScroll(atScroll)
	if !ok {
		return c.tryMoveDownWindowRow()
	}
	return ok
}

// MoveDownLines moves the cursor "n" lines below the current line, scrolling
// the content if required. It returns false and does nothing when the end
// of the content is reached.
func (c *Cursor) MoveDownLines(n int) (ok bool) {
	return c.multiplyMove(n, c.MoveDown)
}

// MoveUp moves the cursor one line above the current line, scrolling
// the content if required. It returns false and does nothing when the end
// of the content is reached.
func (c *Cursor) MoveUp() (ok bool) {
	atScroll := c.cursorAtScroll()
	if atScroll.Y <= 0 {
		// first line is longer than the entire height x width
		// allow user to scroll up by moving up and down the window axis
		if c.scroll.Wrap && c.scroll.SeekUp() {
			var win term.Coordinates
			win, ok = c.scroll.ScrollToWindowCoordinates(atScroll)
			if !ok {
				return ok
			}
			win.Y--
			_, ok = c.MoveToScroll(c.scroll.WindowToScrollCoordinates(win))
		}
		return
	}
	atScroll.Y--
	_, ok = c.MoveToScroll(atScroll)
	return
}

// MoveUpLines moves the cursor "n" lines above the current line, scrolling
// the content if required. It returns false and does nothing when the end
// of the content is reached.
func (c *Cursor) MoveUpLines(n int) (ok bool) {
	return c.multiplyMove(n, c.MoveUp)
}

// MoveLeft moves the cursor one column before the current cell, scrolling
// the content if required. It returns false and does nothing when the end
// of the content is reached.
func (c *Cursor) MoveLeft() (ok bool) {
	atScroll := c.cursorAtScroll()
	if atScroll.X <= 0 {
		return
	}
	atScroll.X--
	_, ok = c.MoveToScroll(atScroll)
	return
}

// MoveLeftColumns moves the cursor "n" columns before the current cell, scrolling
// the content if required. It returns false and does nothing when the end
// of the content is reached.
func (c *Cursor) MoveLeftColumns(n int) (ok bool) {
	return c.multiplyMove(n, c.MoveLeft)
}

// MoveRight moves the cursor one column after the current cell, scrolling
// the content if required. It returns false and does nothing when the end
// of the content is reached.
func (c *Cursor) MoveRight() (ok bool) {
	atScroll := c.cursorAtScroll()
	if atScroll.Y >= c.rows() || atScroll.X >= c.view().Columns(atScroll.Y) {
		return
	}
	atScroll.X++
	_, ok = c.MoveToScroll(atScroll)
	return
}

// MoveRightColumns moves the cursor "n" columns after the current cell, scrolling
// the content if required. It returns false and does nothing when the end
// of the content is reached.
func (c *Cursor) MoveRightColumns(n int) (ok bool) {
	return c.multiplyMove(n, c.MoveRight)
}

// MoveLeftWrap will move the cursor to the left or wrap to end of
// previous line if cursor is at X=0
func (c *Cursor) MoveLeftWrap() bool {
	ok := c.MoveLeft()
	if ok {
		return true
	}
	ok = c.MoveUp()
	if !ok {
		return false
	}
	c.MoveEndLine()
	return true
}

// MoveRightWrap will move the cursor to the right or wrap to beginning
// of next line if cursor is at X=EOL
func (c *Cursor) MoveRightWrap() bool {
	ok := c.MoveRight()
	if ok {
		return true
	}
	ok = c.MoveDown()
	if !ok {
		return false
	}
	c.MoveStartLine()
	return true
}

func (c *Cursor) multiplyMove(n int, move func() bool) (ok bool) {
	enable := c.disablePublishing()
	defer enable()
	for range n {
		mOk := move()
		// If it was moving ok and now it stopped, we're done.
		if ok && !mOk {
			return
		}
		ok = mOk || ok
	}
	return
}

func (c *Cursor) cursorAtScroll() term.Coordinates {
	return c.scroll.WindowToScrollCoordinates(c.cursor)
}

func (c *Cursor) cellAtCursor() (cell term.Cell, ok bool) {
	cursorAtScroll := c.cursorAtScroll()
	cells := c.view().RawCells()
	if cursorAtScroll.Y >= len(cells) ||
		cursorAtScroll.X >= len(cells[cursorAtScroll.Y]) ||
		cursorAtScroll.Y < 0 || cursorAtScroll.X < 0 {
		return
	}
	ok = true
	cell = cells[cursorAtScroll.Y][cursorAtScroll.X]
	return
}

func isOneOf(cell term.Cell, special map[rune]struct{}) bool {
	_, ok := special[cell.Ch]
	return ok
}

func isNoneOf(cell term.Cell, skip map[rune]struct{}) (none bool) {
	_, ok := skip[cell.Ch]
	return !ok
}

func (c *Cursor) moveAfterRune(budget int, skip, special map[rune]struct{}, move func() bool) (ok bool) {
	enable := c.disablePublishing()
	defer enable()

	const (
		init = iota
		foundRune
	)

	initialScrollPos := c.cursorAtScroll()
	mark := c.Mark()
	state := init

	cell, cOk := c.cellAtCursor()
	if !cOk {
		return
	}

	if isOneOf(cell, special) {
		state = foundRune
	}

	for i := 0; i < budget && move(); i++ {
		cell, cOk := c.cellAtCursor()
		if !cOk {
			continue
		}
		ok = true
		mark = c.Mark()

		if initialScrollPos.Y != c.cursorAtScroll().Y {
			state = foundRune
		}

		if isOneOf(cell, special) {
			state = foundRune
		}

		switch state {
		case init:
			if isOneOf(cell, skip) {
				state = foundRune
			}
		case foundRune:
			if isNoneOf(cell, skip) {
				return
			}
		}
	}

	c.MoveToMark(mark)
	return
}

func (c *Cursor) moveBeforeRune(budget int, skip, all map[rune]struct{}, move func() bool) (ok bool) {
	enable := c.disablePublishing()
	defer enable()

	const (
		skipRune = iota
		findRune
		done
	)

	state := skipRune
	initialScrollPos := c.cursorAtScroll()
	mark := c.Mark()

	var moved bool
	for i := 0; i < budget && move(); i++ {
		moved = true
		cell, cOk := c.cellAtCursor()
		if !cOk {
			continue
		}

		switch state {
		case skipRune:
			if isOneOf(cell, skip) {
				continue
			}
			if isOneOf(cell, all) {
				state = done
				mark = c.Mark()
			} else {
				state = findRune
			}
		case findRune:
			if isOneOf(cell, all) {
				state = done
			}
			if initialScrollPos.Y != c.cursorAtScroll().Y {
				state = done
			}
		}
		if state == done {
			break
		}
		mark = c.Mark()
	}

	if !moved {
		return
	}
	if state == done {
		ok = true
		c.MoveToMark(mark)
	}
	return
}

var allSpecialCharacters = map[rune]struct{}{
	'.': {}, ',': {}, ':': {}, ';': {}, ' ': {}, ')': {}, '"': {},
	'\'': {}, '(': {}, '{': {}, '}': {}, '[': {}, ']': {}, '\t': {},
	'\x00': {}, '\\': {}, '/': {}, '+': {}, '`': {}, '_': {}, '@': {},
	'=': {}, '<': {}, '>': {}, '!': {}, '?': {}, '|': {}, '&': {},
	'*': {}, '%': {}, '#': {}, '^': {}, '-': {}}

var skipCharacters = map[rune]struct{}{' ': {}, '\t': {}, '\x00': {}, '_': {}}

var blankCharacters = map[rune]struct{}{'\x00': {}, ' ': {}, '\t': {}}

const budgetFindWord = 100

// MoveRightStartWordGroup moves the cursor right to the start of the next word.
func (c *Cursor) MoveRightStartWordGroup() bool {
	return c.moveAfterRune(budgetFindWord, skipCharacters, skipCharacters, c.MoveRightWrap)
}

// MoveLeftStartWordGroup moves the cursor left to the start of the previous word.
func (c *Cursor) MoveLeftStartWordGroup() bool {
	return c.moveBeforeRune(budgetFindWord, skipCharacters,
		skipCharacters, c.MoveLeftWrap)
}

// MoveRightEndWordGroup moves the cursor right to the end of the next or current word.
func (c *Cursor) MoveRightEndWordGroup() bool {
	ok := c.moveBeforeRune(budgetFindWord, skipCharacters,
		skipCharacters, c.MoveRightWrap)
	if !ok || c.RightInclusiveSemantics {
		return false
	}
	c.MoveRight()
	return true
}

// MoveLeftEndWordGroup moves the cursor left to the end of the previous word.
func (c *Cursor) MoveLeftEndWordGroup() bool {
	ok := c.moveAfterRune(budgetFindWord, skipCharacters,
		skipCharacters, c.MoveLeftWrap)
	if !ok || c.RightInclusiveSemantics {
		return false
	}
	c.MoveRight()
	return true
}

// MoveRightStartWord moves the cursor right to the start of the next word.
func (c *Cursor) MoveRightStartWord() bool {
	return c.moveAfterRune(budgetFindWord, skipCharacters,
		allSpecialCharacters, c.MoveRightWrap)
}

// MoveLeftStartWord moves the cursor left to the start of the previous word.
func (c *Cursor) MoveLeftStartWord() bool {
	return c.moveBeforeRune(budgetFindWord, skipCharacters,
		allSpecialCharacters, c.MoveLeftWrap)
}

// MoveLeftStartWordNoWrap moves the cursor left to the start of the previous word,
// but as opposed to MoveLeftStartWord, it doesn't continue on the previous line after exhausting
// results on the current line.
func (c *Cursor) MoveLeftStartWordNoWrap() bool {
	return c.moveBeforeRune(budgetFindWord, skipCharacters,
		allSpecialCharacters, c.MoveLeft)
}

// MoveRightEndWord moves the cursor right to the end of the next or current word.
func (c *Cursor) MoveRightEndWord() bool {
	ok := c.moveBeforeRune(budgetFindWord, skipCharacters,
		allSpecialCharacters, c.MoveRightWrap)
	if !ok || c.RightInclusiveSemantics {
		return ok
	}
	c.MoveRight()
	return true
}

// MoveRightEndWordNoWrap moves the cursor right to the end of the next or current word,
// but as opposed to MoveRightEndWord, it doesn't continue on the next line after exhausting
// results on the current line.
func (c *Cursor) MoveRightEndWordNoWrap() bool {
	ok := c.moveBeforeRune(budgetFindWord, skipCharacters,
		allSpecialCharacters, c.MoveRight)
	if !ok || c.RightInclusiveSemantics {
		return ok
	}
	c.MoveRight()
	return true
}

// MoveLeftEndWord moves the cursor left to the end of the previous word.
func (c *Cursor) MoveLeftEndWord() bool {
	ok := c.moveAfterRune(budgetFindWord, skipCharacters,
		allSpecialCharacters, c.MoveLeftWrap)
	if !ok || c.RightInclusiveSemantics {
		return ok
	}
	c.MoveRight()
	return true
}

// IsStartWord returns true if cursor is at the start of a word.
func (c *Cursor) IsStartWord() bool {
	pos := c.cursorAtScroll()
	start, _, word := c.scroll.WordAt(pos)
	return word != "" && start == pos
}

// IsEndWord returns true if cursor is at the start of a word.
func (c *Cursor) IsEndWord() bool {
	pos := c.cursorAtScroll()
	if pos.X > 0 && !c.RightInclusiveSemantics {
		pos.X--
	}
	_, end, word := c.scroll.WordAt(pos)
	if end.X > 0 && c.RightInclusiveSemantics {
		end.X--
	} else {
		pos.X++
	}
	return word != "" && end == pos
}

func isWordObjectBlank(r rune) bool {
	return r == '\x00' || r == ' ' || r == '\t'
}

func isWordObjectRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func isSentenceRune(r rune) bool {
	switch r {
	case '.', '!', '?':
		return true
	default:
		return false
	}
}

func (c *Cursor) cellAtScrollCoordinates(pos term.Coordinates) (term.Cell, bool) {
	cells := c.view().RawCells()
	if pos.Y < 0 || pos.Y >= len(cells) {
		return term.Cell{}, false
	}
	if pos.X < 0 || pos.X >= len(cells[pos.Y]) {
		return term.Cell{}, false
	}
	return cells[pos.Y][pos.X], true
}

func (c *Cursor) firstCellInOrAfter(pos term.Coordinates) (term.Coordinates, bool) {
	rows := c.rows()
	if rows == 0 {
		return term.Coordinates{}, false
	}
	if pos.Y < 0 {
		pos = term.Coordinates{}
	}
	for y := pos.Y; y < rows; y++ {
		startX := 0
		if y == pos.Y {
			startX = max(0, pos.X)
		}
		if startX < c.view().Columns(y) {
			return term.Coordinates{Y: y, X: startX}, true
		}
	}
	return term.Coordinates{}, false
}

func (c *Cursor) lastCellInOrBefore(pos term.Coordinates) (term.Coordinates, bool) {
	rows := c.rows()
	if rows == 0 {
		return term.Coordinates{}, false
	}
	if pos.Y >= rows {
		pos.Y = rows - 1
		pos.X = c.view().Columns(pos.Y)
	}
	for y := pos.Y; y >= 0; y-- {
		endX := c.view().Columns(y) - 1
		if y == pos.Y {
			endX = min(endX, pos.X)
		}
		if endX >= 0 {
			return term.Coordinates{Y: y, X: endX}, true
		}
	}
	return term.Coordinates{}, false
}

func (c *Cursor) nextCellPosition(pos term.Coordinates) (term.Coordinates, bool) {
	return c.firstCellInOrAfter(term.Coordinates{Y: pos.Y, X: pos.X + 1})
}

func (c *Cursor) previousCellPosition(pos term.Coordinates) (term.Coordinates, bool) {
	if pos.X > 0 {
		return c.lastCellInOrBefore(term.Coordinates{Y: pos.Y, X: pos.X - 1})
	}
	return c.lastCellInOrBefore(term.Coordinates{Y: pos.Y - 1, X: math.MaxInt})
}

func (c *Cursor) currentTextObjectCell() (term.Coordinates, bool) {
	if cell, ok := c.cellAtCursor(); ok && cell.Ch != '\x00' {
		return c.cursorAtScroll(), true
	}
	pos := c.cursorAtScroll()
	if pos.Y >= 0 && pos.Y < c.rows() {
		cols := c.view().Columns(pos.Y)
		if cols > 0 && pos.X >= cols {
			return term.Coordinates{Y: pos.Y, X: cols - 1}, true
		}
	}
	if next, ok := c.firstCellInOrAfter(pos); ok && next.Y == pos.Y {
		return next, true
	}
	if prev, ok := c.lastCellInOrBefore(pos); ok && prev.Y == pos.Y {
		return prev, true
	}
	return term.Coordinates{}, false
}

func (c *Cursor) selectRange(start, end term.Coordinates) bool {
	return c.setExplicitSelection(StandardSelection, start, end, start)
}

// SelectRange selects the explicit right-exclusive range [start, end).
func (c *Cursor) SelectRange(start, end term.Coordinates) bool {
	c.pushSelectionHistory()
	return c.selectRange(start, end)
}

// ExpandSelection expands the current caret or explicit selection to the next
// larger syntactic range.
func (c *Cursor) ExpandSelection(ctx context.Context) bool {
	svc := c.getSelectionService()
	if svc == nil {
		return false
	}

	current, cursorPos, ok := c.currentSelectionRange()
	if !ok {
		return false
	}
	next, ok := svc.SelectionExpand(current)
	if !ok || !validSelectionRange(next) || next == current {
		return false
	}

	return c.setExplicitSelection(StandardSelection, next.Start, next.End, cursorPos)
}

// ShrinkSelection contracts an expanded syntactic selection through prior steps.
func (c *Cursor) ShrinkSelection() bool {
	svc := c.getSelectionService()
	if svc == nil {
		return false
	}
	current, caret, ok := c.currentSelectionRange()
	if !ok || current.Start == current.End {
		return false
	}
	prev, ok := svc.SelectionShrink(current, caret)
	if !ok || (!validSelectionRange(prev) && prev.Start != caret) {
		return false
	}

	if prev.Start == prev.End {
		c.Unselect()
		c.moveToScroll(prev.Start)
		return true
	}
	return c.setExplicitSelection(StandardSelection, prev.Start, prev.End, prev.Start)
}

// ExpandRange expands rng to the next larger syntactic range without selecting it.
func (c *Cursor) ExpandRange(ctx context.Context, rng term.Range) (term.Range, bool) {
	_ = ctx
	svc := c.getSelectionService()
	if svc == nil {
		return term.Range{}, false
	}
	next, ok := svc.SelectionExpand(rng)
	return next, ok && validSelectionRange(next) && next != rng
}

// ShrinkRange shrinks rng toward caret without selecting it.
func (c *Cursor) ShrinkRange(ctx context.Context, rng term.Range, caret term.Coordinates) (term.Range, bool) {
	_ = ctx
	svc := c.getSelectionService()
	if svc == nil {
		return term.Range{}, false
	}
	next, ok := svc.SelectionShrink(rng, caret)
	return next, ok && validSelectionRange(next) && next != rng
}

func validSelectionRange(rng term.Range) bool {
	return rng.Start != rng.End
}

func (c *Cursor) wordClass(r rune, group bool) int {
	if isWordObjectBlank(r) {
		return 0
	}
	if group {
		return 1
	}
	if isWordObjectRune(r) {
		return 1
	}
	return 2
}

func (c *Cursor) wordObjectStart(pos term.Coordinates, allowPrevFallback bool) (term.Coordinates, bool) {
	if cell, ok := c.cellAtScrollCoordinates(pos); ok && !isWordObjectBlank(cell.Ch) {
		return pos, true
	}

	if allowPrevFallback {
		for next, ok := c.firstCellInOrAfter(pos); ok && next.Y == pos.Y; next, ok = c.nextCellPosition(next) {
			cell, ok := c.cellAtScrollCoordinates(next)
			if ok && !isWordObjectBlank(cell.Ch) {
				return next, true
			}
		}
		for prev, ok := c.lastCellInOrBefore(pos); ok && prev.Y == pos.Y; prev, ok = c.previousCellPosition(prev) {
			cell, ok := c.cellAtScrollCoordinates(prev)
			if ok && !isWordObjectBlank(cell.Ch) {
				return prev, true
			}
		}
		return term.Coordinates{}, false
	}

	for next, ok := c.firstCellInOrAfter(pos); ok; next, ok = c.nextCellPosition(next) {
		cell, ok := c.cellAtScrollCoordinates(next)
		if ok && !isWordObjectBlank(cell.Ch) {
			return next, true
		}
	}
	return term.Coordinates{}, false
}

func (c *Cursor) wordObjectBounds(
	pos term.Coordinates, group, around, allowPrevFallback bool,
) (startCoord, endCoord term.Coordinates, ok bool) {
	pos, ok = c.wordObjectStart(pos, allowPrevFallback)
	if !ok {
		return term.Coordinates{}, term.Coordinates{}, false
	}

	cell, ok := c.cellAtScrollCoordinates(pos)
	if !ok || isWordObjectBlank(cell.Ch) {
		return term.Coordinates{}, term.Coordinates{}, false
	}

	class := c.wordClass(cell.Ch, group)
	start, end := pos, pos
	for prev, ok := c.previousCellPosition(start); ok && prev.Y == pos.Y; prev, ok = c.previousCellPosition(prev) {
		prevCell, ok := c.cellAtScrollCoordinates(prev)
		if !ok || c.wordClass(prevCell.Ch, group) != class {
			break
		}
		start = prev
	}
	for next, ok := c.nextCellPosition(end); ok && next.Y == pos.Y; next, ok = c.nextCellPosition(next) {
		nextCell, ok := c.cellAtScrollCoordinates(next)
		if !ok || c.wordClass(nextCell.Ch, group) != class {
			break
		}
		end = next
	}

	startCoord = start
	endCoord = term.Coordinates{Y: end.Y, X: end.X + 1}
	if around {
		right := endCoord
		for right.X < c.view().Columns(pos.Y) {
			rightCell, ok := c.cellAtScrollCoordinates(right)
			if !ok || !isWordObjectBlank(rightCell.Ch) {
				break
			}
			right.X++
		}
		if right != endCoord {
			endCoord = right
		} else {
			for startCoord.X > 0 {
				left := term.Coordinates{Y: startCoord.Y, X: startCoord.X - 1}
				leftCell, ok := c.cellAtScrollCoordinates(left)
				if !ok || !isWordObjectBlank(leftCell.Ch) {
					break
				}
				startCoord = left
			}
		}
	}
	return startCoord, endCoord, true
}

func (c *Cursor) selectWordObjectCount(group, around bool, count int) bool {
	count = max(1, count)
	start, end, ok := c.wordObjectBounds(c.cursorAtScroll(), group, around, true)
	if !ok {
		return false
	}
	for i := 1; i < count; i++ {
		_, nextEnd, nextOK := c.wordObjectBounds(end, group, around, false)
		if !nextOK {
			break
		}
		end = nextEnd
	}
	return c.selectRange(start, end)
}

func (c *Cursor) selectWordObject(group, around bool) bool {
	return c.selectWordObjectCount(group, around, 1)
}

// SelectInnerWord selects the inner word at cursor.
func (c *Cursor) SelectInnerWord() bool {
	return c.selectWordObject(false, false)
}

// SelectInnerWords selects count inner words starting at cursor.
func (c *Cursor) SelectInnerWords(count int) bool {
	return c.selectWordObjectCount(false, false, count)
}

// SelectAWord selects the word at cursor, plus surrounding whitespace when present.
func (c *Cursor) SelectAWord() bool {
	return c.selectWordObject(false, true)
}

// SelectAWords selects count a-word objects starting at cursor.
func (c *Cursor) SelectAWords(count int) bool {
	return c.selectWordObjectCount(false, true, count)
}

// SelectInnerWordGroup selects the inner WORD at cursor.
func (c *Cursor) SelectInnerWordGroup() bool {
	return c.selectWordObject(true, false)
}

// SelectInnerWordGroups selects count inner WORD objects starting at cursor.
func (c *Cursor) SelectInnerWordGroups(count int) bool {
	return c.selectWordObjectCount(true, false, count)
}

// SelectAWordGroup selects the WORD at cursor, plus surrounding whitespace when present.
func (c *Cursor) SelectAWordGroup() bool {
	return c.selectWordObject(true, true)
}

// SelectAWordGroups selects count a-WORD objects starting at cursor.
func (c *Cursor) SelectAWordGroups(count int) bool {
	return c.selectWordObjectCount(true, true, count)
}

func (c *Cursor) quoteEscapedAt(y, x int) bool {
	escaped := false
	for bx := x - 1; bx >= 0; bx-- {
		prev, ok := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: bx})
		if !ok || prev.Ch != '\\' {
			break
		}
		escaped = !escaped
	}
	return escaped
}

func (c *Cursor) quoteIndexRight(y, from int, quote rune) int {
	for x := from; x < c.view().Columns(y); x++ {
		cell, ok := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: x})
		if !ok || cell.Ch != quote {
			continue
		}
		if !c.quoteEscapedAt(y, x) {
			return x
		}
	}
	return -1
}

func (c *Cursor) quoteBounds(quote rune) (start, end term.Coordinates, ok bool) {
	pos, ok := c.currentTextObjectCell()
	if !ok {
		return term.Coordinates{}, term.Coordinates{}, false
	}
	y := pos.Y
	for openX := min(pos.X, c.view().Columns(y)-1); openX >= 0; openX-- {
		cell, ok := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: openX})
		if !ok || cell.Ch != quote || c.quoteEscapedAt(y, openX) {
			continue
		}
		closeX := c.quoteIndexRight(y, openX+1, quote)
		if closeX >= 0 && pos.X <= closeX {
			return term.Coordinates{Y: y, X: openX}, term.Coordinates{Y: y, X: closeX}, true
		}
	}
	return term.Coordinates{}, term.Coordinates{}, false
}

// SelectInnerQuote selects the contents of the surrounding quote pair.
func (c *Cursor) SelectInnerQuote(quote rune) bool {
	start, end, ok := c.quoteBounds(quote)
	if !ok {
		return false
	}
	start.X++
	return c.selectRange(start, end)
}

// SelectAQuote selects the surrounding quote pair including delimiters.
func (c *Cursor) SelectAQuote(quote rune) bool {
	start, end, ok := c.quoteBounds(quote)
	if !ok {
		return false
	}
	end.X++
	return c.selectRange(start, end)
}

func coordinatesGTE(a, b term.Coordinates) bool {
	return a.Y > b.Y || (a.Y == b.Y && a.X >= b.X)
}

func (c *Cursor) findMatchingRuneFrom(start term.Coordinates, open, close rune) (term.Coordinates, bool) {
	depth := 1
	for pos, ok := c.nextCellPosition(start); ok; pos, ok = c.nextCellPosition(pos) {
		cell, ok := c.cellAtScrollCoordinates(pos)
		if !ok {
			continue
		}
		switch cell.Ch {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return pos, true
			}
		}
	}
	return term.Coordinates{}, false
}

func (c *Cursor) blockBounds(open, close rune) (start, end term.Coordinates, ok bool) {
	pos, ok := c.currentTextObjectCell()
	if !ok {
		return term.Coordinates{}, term.Coordinates{}, false
	}
	if cell, ok := c.cellAtScrollCoordinates(pos); ok && cell.Ch == close {
		if prev, ok := c.previousCellPosition(pos); ok {
			pos = prev
		}
	}

	depth := 0
	for scan, ok := c.lastCellInOrBefore(pos); ok; scan, ok = c.previousCellPosition(scan) {
		cell, ok := c.cellAtScrollCoordinates(scan)
		if !ok {
			continue
		}
		switch cell.Ch {
		case close:
			depth++
		case open:
			if depth == 0 {
				match, ok := c.findMatchingRuneFrom(scan, open, close)
				if ok && coordinatesGTE(match, pos) {
					return scan, match, true
				}
			} else {
				depth--
			}
		}
	}
	return term.Coordinates{}, term.Coordinates{}, false
}

// SelectInnerBlock selects the contents of the surrounding paired block.
func (c *Cursor) SelectInnerBlock(open, close rune) bool {
	start, end, ok := c.blockBounds(open, close)
	if !ok {
		return false
	}
	start.X++
	return c.selectRange(start, end)
}

// SelectABlock selects the surrounding paired block including delimiters.
func (c *Cursor) SelectABlock(open, close rune) bool {
	start, end, ok := c.blockBounds(open, close)
	if !ok {
		return false
	}
	end.X++
	return c.selectRange(start, end)
}

// SelectABlockClose selects the surrounding paired block including delimiters,
// leaving the cursor on the closing delimiter.
func (c *Cursor) SelectABlockClose(open, close rune) bool {
	start, end, ok := c.blockBounds(open, close)
	if !ok {
		return false
	}
	selectionEnd := end
	selectionEnd.X++
	return c.setExplicitSelection(StandardSelection, start, selectionEnd, end)
}

func (c *Cursor) isBlankLine(y int) bool {
	if y < 0 || y >= c.rows() {
		return true
	}
	cols := c.view().Columns(y)
	if cols == 0 {
		return true
	}
	for x := range cols {
		cell, ok := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: x})
		if ok && !isWrapParagraphBlank(cell.Ch) {
			return false
		}
	}
	return true
}

func (c *Cursor) lineEndCoordinate(y int) term.Coordinates {
	if y+1 < c.rows() {
		return term.Coordinates{Y: y + 1, X: 0}
	}
	return term.Coordinates{Y: y, X: c.view().Columns(y)}
}

func (c *Cursor) paragraphLines(around bool) (startLine, endLine int, ok bool) {
	if c.rows() == 0 {
		return 0, 0, false
	}
	line := min(c.cursorAtScroll().Y, c.rows()-1)
	if c.isBlankLine(line) {
		for next := line; next < c.rows(); next++ {
			if !c.isBlankLine(next) {
				line = next
				goto found
			}
		}
		for prev := line - 1; prev >= 0; prev-- {
			if !c.isBlankLine(prev) {
				line = prev
				goto found
			}
		}
		return 0, 0, false
	}

found:
	startLine, endLine = line, line
	for startLine > 0 && !c.isBlankLine(startLine-1) {
		startLine--
	}
	for endLine+1 < c.rows() && !c.isBlankLine(endLine+1) {
		endLine++
	}
	if around {
		for startLine > 0 && c.isBlankLine(startLine-1) {
			startLine--
		}
		for endLine+1 < c.rows() && c.isBlankLine(endLine+1) {
			endLine++
		}
	}
	return startLine, endLine, true
}

// WrapParagraph reflows the current paragraph to fit within the given ruler.
// It preserves indentation and re-applies a common line comment leader when
// the paragraph is a block of line comments.
func (c *Cursor) WrapParagraph(ruler int) bool {
	if ruler <= 0 {
		return false
	}
	startLine, endLine, ok := c.paragraphLines(false)
	if !ok {
		return false
	}
	indent, leader, bodyLines, ok := c.wrapParagraphParts(startLine, endLine)
	if !ok {
		return false
	}
	return c.wrapParagraphRange(startLine, endLine, indent, leader, bodyLines, ruler, nil)
}

// WrapSelectedParagraph reflows the current selected line range. It is intended
// for visual-line gq style formatting where the selected comment block should be
// reformatted without absorbing surrounding code. Block selections are
// treated as if every covered line were fully selected (Vim behavior:
// gq on <C-v> ignores per-column block bounds).
func (c *Cursor) WrapSelectedParagraph(ruler int) bool {
	return c.wrapSelectedParagraph(ruler, nil)
}

// WrapSelectedParagraphPreservePosition reflows the current selected line range
// and restores the cursor to the same text position after reflow. This matches
// Vim's gw operator: the saved position is tracked through the formatted text
// rather than restored as a raw line/column coordinate.
func (c *Cursor) WrapSelectedParagraphPreservePosition(ruler int, pos term.Coordinates) bool {
	state := &wrapParagraphPreservedPosition{pos: pos}
	changed := c.wrapSelectedParagraph(ruler, state)
	c.SetCursorAtScroll(state.pos)
	return changed
}

type wrapParagraphPreservedPosition struct {
	pos term.Coordinates
}

func (c *Cursor) wrapSelectedParagraph(ruler int, preserved *wrapParagraphPreservedPosition) bool {
	if ruler <= 0 {
		return false
	}
	if _, ok := c.SelectionMode(); !ok {
		return false
	}
	chunks := c.wrapSelectedParagraphChunks()
	if len(chunks) == 0 {
		return false
	}
	var changed bool
	for i := len(chunks) - 1; i >= 0; i-- {
		chunk := chunks[i]
		indent, leader, bodyLines, ok := c.wrapParagraphParts(chunk.startLine, chunk.endLine)
		if !ok {
			continue
		}
		if c.wrapParagraphRange(chunk.startLine, chunk.endLine, indent, leader, bodyLines, ruler, preserved) {
			changed = true
		}
	}
	return changed
}

type wrapParagraphChunk struct {
	startLine int
	endLine   int
}

func (c *Cursor) wrapSelectedParagraphChunks() []wrapParagraphChunk {
	startLine, endLine := c.commentLineBounds()
	var chunks []wrapParagraphChunk
	for y := startLine; y <= endLine; {
		for y <= endLine && strings.TrimSpace(c.lineString(y)) == "" {
			y++
		}
		if y > endLine {
			break
		}

		isComment := c.lineHasAnyCommentPrefix(y)
		chunkStart := y
		for y <= endLine {
			line := c.lineString(y)
			if strings.TrimSpace(line) == "" {
				break
			}
			if c.lineHasAnyCommentPrefix(y) != isComment {
				break
			}
			y++
		}
		chunks = append(chunks, wrapParagraphChunk{startLine: chunkStart, endLine: y - 1})
	}
	return chunks
}

func (c *Cursor) lineHasAnyCommentPrefix(y int) bool {
	if !c.commentSpec.HasLine() {
		return false
	}
	trimmed := strings.TrimLeft(c.lineString(y), " \t")
	for _, prefix := range c.commentSpec.Line {
		if strings.HasPrefix(trimmed, prefix+" ") || strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func (c *Cursor) wrapParagraphRange(
	startLine, endLine int,
	indent, leader string,
	bodyLines []string,
	ruler int,
	preserved *wrapParagraphPreservedPosition,
) bool {
	body := strings.Join(strings.Fields(strings.Join(bodyLines, " ")), " ")
	if body == "" {
		return false
	}
	tabspaces := c.resolveTabspaces(0)
	available := ruler - displayWidthWithTabs(indent, tabspaces, 0) -
		displayWidthWithTabs(leader, tabspaces, displayWidthWithTabs(indent, tabspaces, 0))
	if available <= 0 {
		available = 1
	}
	wrappedBody := wrapTextWords(body, available, tabspaces)
	if len(wrappedBody) == 0 {
		return false
	}
	wrapped := make([]string, len(wrappedBody))
	for i := range wrapped {
		wrapped[i] = indent + leader + wrappedBody[i]
	}
	replacement := strings.Join(wrapped, "\n")
	from := term.Coordinates{Y: startLine}
	to := term.Coordinates{Y: endLine, X: c.buffer().Columns(endLine)}
	if c.rangeString(from, to) == replacement {
		return false
	}
	if preserved != nil {
		if preserved.pos.Y >= startLine && preserved.pos.Y <= endLine {
			offset := c.wrapParagraphTextOffset(startLine, endLine, indent, leader, bodyLines, preserved.pos)
			preserved.pos = wrapParagraphPositionForTextOffset(startLine, indent, leader, wrappedBody, offset)
		} else if preserved.pos.Y > endLine {
			preserved.pos.Y += len(wrapped) - (endLine - startLine + 1)
		}
	}
	cur := c.CursorAtScroll()
	_, after, _ := c.buffer().Edit(c.ctx, from, to, replacement)
	if cur.Y >= startLine && cur.Y <= endLine {
		cur.Y = min(cur.Y, startLine+len(wrapped)-1)
		cur.X = min(cur.X, c.buffer().Columns(cur.Y))
		c.setCursorAfterUpdate(cur)
		return true
	}
	c.setCursorAfterUpdate(after)
	return true
}

func (c *Cursor) wrapParagraphTextOffset(
	startLine, endLine int,
	indent, leader string,
	bodyLines []string,
	pos term.Coordinates,
) int {
	if pos.Y < startLine {
		return 0
	}

	offset := 0
	bodyIndex := 0
	for y := startLine; y <= endLine && bodyIndex < len(bodyLines); y++ {
		line := c.lineString(y)
		bodyStart, body := wrapParagraphLineBody(line, indent, leader)
		if body == "" {
			continue
		}
		if y < pos.Y {
			offset += len([]rune(body))
			if bodyIndex < len(bodyLines)-1 {
				offset++
			}
			bodyIndex++
			continue
		}
		return offset + max(0, pos.X-bodyStart)
	}

	return offset
}

func wrapParagraphLineBody(line, indent, leader string) (start int, body string) {
	trimmed := strings.TrimPrefix(line, indent)
	start = len([]rune(line)) - len([]rune(trimmed))
	if leader != "" {
		withoutLeader := strings.TrimPrefix(trimmed, leader)
		start += len([]rune(trimmed)) - len([]rune(withoutLeader))
		trimmed = withoutLeader
	}
	leading := len([]rune(trimmed)) - len([]rune(strings.TrimLeft(trimmed, " \t")))
	start += leading
	body = strings.TrimSpace(trimmed)
	return start, body
}

func wrapParagraphPositionForTextOffset(
	startLine int,
	indent, leader string,
	wrappedBody []string,
	offset int,
) term.Coordinates {
	if len(wrappedBody) == 0 {
		return term.Coordinates{Y: startLine}
	}
	prefixWidth := len([]rune(indent + leader))
	if offset <= 0 {
		return term.Coordinates{Y: startLine, X: prefixWidth}
	}

	remaining := offset
	for i, line := range wrappedBody {
		lineLen := len([]rune(line))
		if remaining <= lineLen {
			return term.Coordinates{Y: startLine + i, X: prefixWidth + remaining}
		}
		remaining -= lineLen
		if i < len(wrappedBody)-1 {
			if remaining == 0 {
				return term.Coordinates{Y: startLine + i, X: prefixWidth + lineLen}
			}
			remaining--
		}
	}
	lastLine := wrappedBody[len(wrappedBody)-1]
	return term.Coordinates{Y: startLine + len(wrappedBody) - 1, X: prefixWidth + len([]rune(lastLine))}
}

func (c *Cursor) wrapParagraphParts(startLine, endLine int) (indent, leader string, bodyLines []string, ok bool) {
	lines := make([]string, 0, endLine-startLine+1)
	for y := startLine; y <= endLine; y++ {
		lines = append(lines, c.lineString(y))
	}
	if len(lines) == 0 {
		return "", "", nil, false
	}
	indent = leadingWhitespace(lines[0])
	leader = c.commonCommentLeader(lines)
	for _, line := range lines {
		trimmed := strings.TrimPrefix(line, indent)
		if leader != "" {
			trimmed = strings.TrimPrefix(trimmed, leader)
		}
		trimmed = strings.TrimSpace(trimmed)
		if trimmed != "" {
			bodyLines = append(bodyLines, trimmed)
		}
	}
	if len(bodyLines) == 0 {
		return "", "", nil, false
	}
	return indent, leader, bodyLines, true
}

func (c *Cursor) commonCommentLeader(lines []string) string {
	if !c.commentSpec.HasLine() {
		return ""
	}
	linePrefix := c.commentSpec.Line[0]
	leader := linePrefix + " "
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if !strings.HasPrefix(trimmed, linePrefix) {
			return ""
		}
		if !strings.HasPrefix(trimmed, leader) {
			leader = linePrefix
		}
	}
	return leader
}

func leadingWhitespace(s string) string {
	for i, r := range s {
		if r != ' ' && r != '\t' {
			return s[:i]
		}
	}
	return s
}

// displayWidthWithTabs returns the display width of s assuming it begins
// at column startCol. Tabs advance to the next multiple of tabspaces.
func displayWidthWithTabs(s string, tabspaces, startCol int) int {
	if tabspaces <= 0 {
		tabspaces = 1
	}
	col := startCol
	for _, r := range s {
		if r == '\t' {
			col += tabspaces - (col % tabspaces)
			continue
		}
		col += graphemecluster.StringWidth(string(r))
	}
	return col - startCol
}

func wrapTextWords(text string, width, tabspaces int) []string {
	if width <= 0 {
		width = 1
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	lines := []string{words[0]}
	for _, word := range words[1:] {
		current := lines[len(lines)-1]
		currentWidth := displayWidthWithTabs(current, tabspaces, 0)
		wordWidth := displayWidthWithTabs(word, tabspaces, currentWidth+1)
		if currentWidth+1+wordWidth <= width {
			lines[len(lines)-1] = current + " " + word
			continue
		}
		lines = append(lines, word)
	}
	return lines
}

func isWrapParagraphBlank(r rune) bool {
	return r == '\x00' || r == ' ' || r == '\t' || r == '\r'
}

// SelectInnerParagraph selects the current paragraph without surrounding blank lines.
func (c *Cursor) SelectInnerParagraph() bool {
	startLine, endLine, ok := c.paragraphLines(false)
	if !ok {
		return false
	}
	return c.selectRange(term.Coordinates{Y: startLine}, c.lineEndCoordinate(endLine))
}

// SelectAParagraph selects the current paragraph including surrounding blank lines.
func (c *Cursor) SelectAParagraph() bool {
	startLine, endLine, ok := c.paragraphLines(true)
	if !ok {
		return false
	}
	return c.selectRange(term.Coordinates{Y: startLine}, c.lineEndCoordinate(endLine))
}

// lineVisualIndent returns the number of visual columns of leading
// whitespace on line y, expanding tab characters using the given tabstop.
// Lines without leading whitespace return 0.
func (c *Cursor) lineVisualIndent(y int, tabspaces int) int {
	if y < 0 || y >= c.rows() {
		return 0
	}
	cells := c.buffer().RawCells()
	if y >= len(cells) {
		return 0
	}
	ts := max(1, tabspaces)
	indent := 0
	for _, cell := range cells[y] {
		switch cell.Ch {
		case '\t':
			indent += ts
		case ' ':
			indent++
		default:
			return indent
		}
	}
	return indent
}

// SelectIndentationLevel selects the contiguous block of lines whose
// visual indentation is greater than or equal to the indentation of the
// current line, including blank lines that lie between same-indent lines
// and trimming any leading or trailing blank lines that would extend the
// selection beyond the indented block.
func (c *Cursor) SelectIndentationLevel(tabspaces int) bool {
	pos, ok := c.cursorAtScrollBounds()
	if !ok {
		return false
	}
	rows := c.rows()
	if rows == 0 {
		return false
	}

	line := pos.Y
	if line >= rows {
		line = rows - 1
	}
	if c.isBlankLine(line) {
		found := false
		for next := line + 1; next < rows; next++ {
			if !c.isBlankLine(next) {
				line = next
				found = true
				break
			}
		}
		if !found {
			for prev := line - 1; prev >= 0; prev-- {
				if !c.isBlankLine(prev) {
					line = prev
					found = true
					break
				}
			}
		}
		if !found {
			return false
		}
	}

	target := c.lineVisualIndent(line, tabspaces)
	startLine, endLine := line, line
	for startLine > 0 {
		prev := startLine - 1
		if !c.isBlankLine(prev) && c.lineVisualIndent(prev, tabspaces) < target {
			break
		}
		startLine = prev
	}
	for endLine+1 < rows {
		next := endLine + 1
		if !c.isBlankLine(next) && c.lineVisualIndent(next, tabspaces) < target {
			break
		}
		endLine = next
	}
	for startLine < line && c.isBlankLine(startLine) {
		startLine++
	}
	for endLine > line && c.isBlankLine(endLine) {
		endLine--
	}

	from := term.Coordinates{Y: startLine}
	to := term.Coordinates{Y: endLine, X: c.view().Columns(endLine)}
	return c.setExplicitSelection(LineSelection, from, to, from)
}

// MoveNextParagraph moves the cursor forward to the next blank line
// after a non-blank line (i.e. the next paragraph boundary). Returns
// true when the cursor actually moved.
func (c *Cursor) MoveNextParagraph() bool {
	rows := c.rows()
	if rows == 0 {
		return false
	}
	y := c.cursorAtScroll().Y
	// Skip blank lines at the current position.
	for y < rows && c.isBlankLine(y) {
		y++
	}
	// Skip non-blank lines.
	for y < rows && !c.isBlankLine(y) {
		y++
	}
	if y >= rows {
		y = rows - 1
	}
	_, ok := c.MoveToScroll(term.Coordinates{Y: y})
	return ok
}

// MoveNextParagraphs repeats MoveNextParagraph n times.
func (c *Cursor) MoveNextParagraphs(n int) bool {
	return c.multiplyMove(n, c.MoveNextParagraph)
}

// MovePrevParagraph moves the cursor backward to the blank line before
// the previous paragraph (i.e. the previous paragraph boundary). Returns
// true when the cursor actually moved.
func (c *Cursor) MovePrevParagraph() bool {
	rows := c.rows()
	if rows == 0 {
		return false
	}
	y := c.cursorAtScroll().Y
	// Skip blank lines at the current position.
	for y > 0 && c.isBlankLine(y) {
		y--
	}
	// Skip non-blank lines.
	for y > 0 && !c.isBlankLine(y) {
		y--
	}
	_, ok := c.MoveToScroll(term.Coordinates{Y: y})
	return ok
}

// MovePrevParagraphs repeats MovePrevParagraph n times.
func (c *Cursor) MovePrevParagraphs(n int) bool {
	return c.multiplyMove(n, c.MovePrevParagraph)
}

func (c *Cursor) sentenceBounds(around bool) (start, end term.Coordinates, ok bool) {
	pos, ok := c.currentTextObjectCell()
	if !ok {
		return term.Coordinates{}, term.Coordinates{}, false
	}
	y := pos.Y
	cols := c.view().Columns(y)
	if cols == 0 {
		return term.Coordinates{}, term.Coordinates{}, false
	}
	x := min(pos.X, cols-1)
	for x < cols {
		cell, ok := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: x})
		if ok && !unicode.IsSpace(cell.Ch) {
			break
		}
		x++
	}
	if x >= cols {
		for x = min(pos.X, cols-1); x >= 0; x-- {
			cell, ok := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: x})
			if ok && !unicode.IsSpace(cell.Ch) {
				break
			}
		}
		if x < 0 {
			return term.Coordinates{}, term.Coordinates{}, false
		}
	}

	startX := 0
	for i := x - 1; i >= 0; i-- {
		cell, _ := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: i})
		if isSentenceRune(cell.Ch) {
			if i+1 >= cols {
				startX = cols
				break
			}
			next, _ := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: i + 1})
			if unicode.IsSpace(next.Ch) {
				startX = i + 1
				break
			}
		}
	}
	for startX < cols {
		cell, _ := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: startX})
		if !unicode.IsSpace(cell.Ch) {
			break
		}
		startX++
	}

	endX := cols
	for i := x; i < cols; i++ {
		cell, _ := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: i})
		if !isSentenceRune(cell.Ch) {
			continue
		}
		if i+1 >= cols {
			endX = i + 1
			break
		}
		next, _ := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: i + 1})
		if unicode.IsSpace(next.Ch) {
			endX = i + 1
			break
		}
	}

	if around {
		trail := endX
		for trail < cols {
			cell, _ := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: trail})
			if !unicode.IsSpace(cell.Ch) {
				break
			}
			trail++
		}
		if trail > endX {
			endX = trail
		} else {
			for startX > 0 {
				cell, _ := c.cellAtScrollCoordinates(term.Coordinates{Y: y, X: startX - 1})
				if !unicode.IsSpace(cell.Ch) {
					break
				}
				startX--
			}
		}
	}

	return term.Coordinates{Y: y, X: startX}, term.Coordinates{Y: y, X: endX}, true
}

// SelectInnerSentence selects the current sentence without surrounding whitespace.
func (c *Cursor) SelectInnerSentence() bool {
	start, end, ok := c.sentenceBounds(false)
	if !ok {
		return false
	}
	return c.selectRange(start, end)
}

// SelectASentence selects the current sentence including adjacent separating whitespace.
func (c *Cursor) SelectASentence() bool {
	start, end, ok := c.sentenceBounds(true)
	if !ok {
		return false
	}
	return c.selectRange(start, end)
}

// MoveToMatchingRune moves the cursor to the balanced matching rune of the rune at
// the current cursor's cell.
func (c *Cursor) MoveToMatchingRune() bool {
	cell, ok := c.cellAtCursor()
	if !ok {
		return ok
	}

	ok = false
	switch cell.Ch {
	case '[':
		ok = c.moveMatchRuneForward('[', ']')
	case '{':
		ok = c.moveMatchRuneForward('{', '}')
	case '(':
		ok = c.moveMatchRuneForward('(', ')')

	case ']':
		ok = c.moveMatchRuneBackward(']', '[')
	case '}':
		ok = c.moveMatchRuneBackward('}', '{')
	case ')':
		ok = c.moveMatchRuneBackward(')', '(')
	}

	return ok
}

// InsertLineAbove inserts a row above the current row and moves the cursor up.
func (c *Cursor) InsertLineAbove(indentRune rune, tabspaces int) {
	mode := c.selection.mode
	c.selection.mode = NoSelection
	c.setSelection()

	cursorAtScroll := c.cursorAtScroll()
	pos := cursorAtScroll
	pos.X = 0
	c.buffer().Edit(c.ctx, pos, pos, "\n")
	pos, _ = c.tryIndent(c.ctx, pos, indentRune, tabspaces)
	c.selection.mode = mode
	c.setSelection()
	c.setCursorAfterUpdate(pos)
}

// InsertLineBelow inserts a row below the current row and moves the cursor down.
func (c *Cursor) InsertLineBelow(indentRune rune, tabspaces int) {
	if _, ok := c.scroll.HiddenBlockAt(c.cursorAtScroll().Y); ok {
		c.MoveDown()
		c.MoveStartLine()
		c.InsertWithIndentRune('\n', indentRune, tabspaces)
		c.MoveUp()
		c.MoveEndLine()
		return
	}
	mode := c.selection.mode
	c.selection.mode = NoSelection
	c.setSelection()

	buf := c.buffer()
	pos := c.cursorAtScroll()
	pos.X = buf.Columns(pos.Y)
	_, to, _ := buf.Edit(c.ctx, pos, pos, "\n")
	to, _ = c.tryIndent(c.ctx, to, indentRune, tabspaces)

	c.selection.mode = mode
	c.setSelection()
	c.setCursorAfterUpdate(to)
}

// Insert is equivalent to InsertContext with context.Background.
func (c *Cursor) Insert(r rune) {
	c.InsertWithIndentRune(r, IndentRuneTab, 0)
}

// InsertWithIndentRune inserts rune at the current cursor position using the
// given indent material and indent width for indentation-aware follow-up edits.
// tabspaces is only consulted when indentRune is IndentRuneSpace.
func (c *Cursor) InsertWithIndentRune(r rune, indentRune rune, tabspaces int) {
	c.InsertContext(c.ctx, r, indentRune, tabspaces)
}

// InsertWithAutoPair inserts r at the current cursor position with basic
// delimiter auto-pair behavior. Opening delimiters insert their matching closing
// delimiter and leave the cursor between the pair. Closing delimiters overtype a
// matching delimiter already under the cursor. Other runes insert normally.
func (c *Cursor) InsertWithAutoPair(r rune, indentRune rune, tabspaces int) bool {
	if _, ok := autoPairOpenForClose(r); ok {
		if cell, cellOK := c.cellAtScrollCoordinates(c.cursorAtScroll()); cellOK && cell.Ch == r {
			return c.MoveRight()
		}
	}

	if close, ok := autoPairCloseForOpen(r); ok {
		c.insertAutoPairRunes(r, close)
		return true
	}

	c.InsertWithIndentRune(r, indentRune, tabspaces)
	return true
}

// BackspaceAutoPair deletes both delimiters when the cursor is between a known
// matching pair. Otherwise, it behaves like Backspace.
func (c *Cursor) BackspaceAutoPair() bool {
	prevPos, nextPos, _, _, ok := c.autoPairAroundCursor()
	if !ok {
		return c.Backspace()
	}

	_, old := c.buffer().DeleteContext(c.ctx, prevPos, term.Coordinates{Y: nextPos.Y, X: nextPos.X + 1})
	if old == "" {
		return false
	}
	c.setCursorAfterUpdate(prevPos)
	return true
}

// InsertAutoPairNewline inserts a blank line between paired braces when the
// cursor is between "{" and "}". In all other cases it inserts a normal newline.
func (c *Cursor) InsertAutoPairNewline(indentRune rune, tabspaces int) bool {
	_, nextPos, prev, next, ok := c.autoPairAroundCursor()
	if !ok || !autoPairIsBracePair(prev, next) {
		c.InsertWithIndentRune('\n', indentRune, tabspaces)
		return true
	}

	insertAt := c.cursorAtScroll()
	if nextPos.X+1 < c.view().Columns(nextPos.Y) {
		_, _, _ = c.buffer().Edit(c.ctx, nextPos, term.Coordinates{Y: nextPos.Y, X: nextPos.X + 1}, "\n\n}\n")
	} else {
		_, _, _ = c.buffer().Edit(c.ctx, insertAt, insertAt, "\n\n")
	}
	c.setCursorAfterUpdate(term.Coordinates{Y: insertAt.Y + 1})
	return true
}

// InsertContext inserts rune at the current cursor's position.
func (c *Cursor) InsertContext(ctx context.Context, r rune, indentRune rune, tabspaces int) {
	mode := c.selection.mode
	c.selection.mode = NoSelection
	c.setSelection()

	insertAt := c.cursorAtScroll()
	pos := c.buffer().InsertContext(ctx, insertAt, r)
	switch r {
	case '\n':
		pos, _ = c.tryIndent(ctx, pos, indentRune, tabspaces)
	case '}', ']', ')':
		var ok bool
		pos, ok = c.tryDedent(ctx, pos, indentRune, tabspaces)
		if ok {
			pos.X++
		}
	}
	c.selection.mode = mode
	c.setSelection()
	c.setCursorAfterUpdate(pos)
}

// InsertWithAttr inserts the given rune with the given attributes,
// at the current cursor's position .
func (c *Cursor) InsertWithAttr(r rune, attr term.Attributes) {
	mode := c.selection.mode
	c.selection.mode = NoSelection
	c.setSelection()

	insertAt := c.cursorAtScroll()
	pos := c.buffer().InsertWithAttr(insertAt, r, attr)
	switch r {
	case '\n':
		pos, _ = c.tryIndent(c.ctx, pos, IndentRuneTab, 0)
	case '}', ']', ')':
		var ok bool
		pos, ok = c.tryDedent(c.ctx, pos, IndentRuneTab, 0)
		if ok {
			pos.X++
		}
	}
	c.selection.mode = mode
	c.setSelection()
	c.setCursorAfterUpdate(pos)
}

// InsertString inserts str at the current cursor's position.
func (c *Cursor) InsertString(str string) {
	mode := c.selection.mode
	c.selection.mode = NoSelection
	c.setSelection()

	_, until := c.buffer().InsertString(c.cursorAtScroll(), str)
	c.selection.mode = mode

	c.setSelection()
	c.setCursorAfterUpdate(until)
}

// SetCommentSpec sets the active language-specific comment delimiters.
func (c *Cursor) SetCommentSpec(spec CommentSpec) {
	c.commentSpec = spec
}

// CommentSpec returns the active language-specific comment delimiters,
// the zero value when none were set.
func (c *Cursor) CommentSpec() CommentSpec {
	return c.commentSpec
}

// InsertBlock inserts a string in a block-wise fashion meaning it
// will insert each of the lines at corresponding relative x and y positions
// shifting content to the right accordingly.
func (c *Cursor) InsertBlock(str string) {
	reader := bufio.NewReader(strings.NewReader(str))
	for {
		str, err := reader.ReadString('\n')
		if err == nil && len(str) > 0 {
			str = str[:len(str)-1]
		}
		cur := c.CursorAtScroll()
		c.InsertString(str)
		if err != nil {
			break
		}
		cur.Y++
		c.MoveToScroll(cur)
	}
}

// Paste pastes the given string on the underlying scroll at the current
// cursor position.
func (c *Cursor) Paste(str string, mode SelectMode, after bool) {
	// disable seeking while performing combined move
	prev := c.shouldSeek
	c.shouldSeek = false

	if mode == NoSelection {
		// this is how vim behaves when using a system clipboard
		if strings.HasSuffix(str, "\n") {
			mode = LineSelection
		} else {
			mode = StandardSelection
		}
	}

	if ok := c.DeleteSelection(); ok {
		// vim keeps the new line when pasting on a fully selected line
		if mode == LineSelection {
			c.InsertString("\n")
			c.MoveUp()
		}
	}

	switch mode {
	case StandardSelection:
		if after {
			c.MoveRight()
		}
		c.InsertString(str)
	case LineSelection:
		if after {
			movedDown := c.MoveLineDown()
			var cur term.Coordinates
			if !movedDown {
				// force set cursor past last line
				cur = c.CursorAtScroll()
				c.setCursor(term.Coordinates{X: 0, Y: c.cursor.Y + 1}, false)
			} else {
				c.MoveStartLine()
				cur = c.CursorAtScroll()
			}
			c.InsertString(str)
			c.MoveToScroll(cur)
		} else {
			cur := c.CursorAtScroll()
			c.MoveStartLine()
			c.InsertString(str)
			c.MoveToScroll(cur)
			c.MoveStartLine()
		}
	case BlockSelection:
		if after {
			c.MoveRight()
			cur := c.CursorAtScroll()
			c.InsertBlock(str)
			c.MoveToScroll(cur)
		} else {
			cur := c.CursorAtScroll()
			c.InsertBlock(str)
			c.MoveToScroll(cur)
		}
	}
	c.shouldSeek = prev
}

// Replace is equivalent to ReplaceContext with context.Background. It returns the
// position next to the replaced character for the caller to decide whether to move.
func (c *Cursor) Replace(r rune) (next term.Coordinates) {
	return c.ReplaceContext(c.ctx, r)
}

// ReplaceContext replaces the cell under the cursor with r. It returns the position
// next to the replaced character for the caller to decide whether to move.
func (c *Cursor) ReplaceContext(ctx context.Context, r rune) (next term.Coordinates) {
	mode := c.selection.mode
	c.selection.mode = NoSelection
	c.setSelection()

	from := c.cursorAtScroll()
	to := term.Coordinates{X: from.X + 1, Y: from.Y}
	_, next, _ = c.buffer().Edit(ctx, from, to, string(r))

	c.selection.mode = mode
	c.setSelection()
	return
}

// TransposeChars swaps the two characters around the caret and advances the
// caret past them, matching Emacs/Zed transpose-chars. When the caret is at
// the end of a non-empty line it transposes the two trailing characters
// instead. It returns false and does nothing when there are not two adjacent
// characters on the current line to swap.
func (c *Cursor) TransposeChars() bool {
	pos := c.cursorAtScroll()
	if pos.Y >= c.rows() {
		return false
	}

	cols := c.view().Columns(pos.Y)
	// leftX is the first of the two cells to swap; rightX is the second.
	leftX := pos.X - 1
	rightX := pos.X
	if rightX >= cols {
		// At end of line: transpose the two trailing characters.
		leftX = cols - 2
		rightX = cols - 1
	}
	if leftX < 0 || rightX >= cols {
		return false
	}

	left, ok := c.cellAtScrollCoordinates(term.Coordinates{Y: pos.Y, X: leftX})
	if !ok {
		return false
	}
	right, ok := c.cellAtScrollCoordinates(term.Coordinates{Y: pos.Y, X: rightX})
	if !ok {
		return false
	}

	from := term.Coordinates{Y: pos.Y, X: leftX}
	to := term.Coordinates{Y: pos.Y, X: rightX + 1}
	swapped := string([]rune{right.Ch, left.Ch})
	if _, _, old := c.buffer().Edit(c.ctx, from, to, swapped); old == "" {
		return false
	}

	c.setCursorAfterUpdate(term.Coordinates{Y: pos.Y, X: min(rightX+1, c.view().Columns(pos.Y))})
	return true
}

// Delete is equivalent to DeleteContext with context.Background.
func (c *Cursor) Delete() (ok bool) {
	ok = c.DeleteContext(c.ctx)
	return
}

// DeleteContext deletes the cell at the current cursor position.
func (c *Cursor) DeleteContext(ctx context.Context) (ok bool) {
	var pos term.Coordinates
	pos, _, ok = c.buffer().DeleteCellContext(ctx, c.cursorAtScroll())
	if ok {
		c.setCursorAfterUpdate(pos)
	}
	return
}

// Backspace is a special form of Delete, named after the keyboard key backspace.
func (c *Cursor) Backspace() (ok bool) {
	if c.cursorAtScroll().X > 0 {
		if c.MoveLeft() {
			ok = c.Delete()
		}
		return
	}

	if ok = c.MoveLineUp(); ok {
		ok = c.Conflate()
	}
	return
}

// BackspaceWord deletes from the current cursor position back to the start of
// the previous word, using the cursor's existing word-motion semantics.
func (c *Cursor) BackspaceWord() (ok bool) {
	end := c.CursorAtScroll()
	start, ok := c.previousCellPosition(end)
	if !ok {
		return false
	}

	cell, ok := c.cellAtScrollCoordinates(start)
	if !ok {
		return false
	}
	class := c.wordClass(cell.Ch, false)

	if class == 0 {
		for {
			prev, ok := c.previousCellPosition(start)
			if !ok {
				break
			}
			prevCell, ok := c.cellAtScrollCoordinates(prev)
			if !ok || c.wordClass(prevCell.Ch, false) != 0 {
				break
			}
			start = prev
		}

		prev, ok := c.previousCellPosition(start)
		if !ok {
			if !c.SelectRange(start, end) {
				return false
			}
			return c.DeleteSelection()
		}
		prevCell, ok := c.cellAtScrollCoordinates(prev)
		if !ok {
			if !c.SelectRange(start, end) {
				return false
			}
			return c.DeleteSelection()
		}
		start = prev
		class = c.wordClass(prevCell.Ch, false)
	}

	for {
		prev, ok := c.previousCellPosition(start)
		if !ok {
			break
		}
		prevCell, ok := c.cellAtScrollCoordinates(prev)
		if !ok || c.wordClass(prevCell.Ch, false) != class {
			break
		}
		start = prev
	}

	if !c.SelectRange(start, end) {
		return false
	}
	return c.DeleteSelection()
}

// Conflate is equivalent to calling ConflateContext with context.Background.
func (c *Cursor) Conflate() (ok bool) {
	return c.ConflateContext(c.ctx)
}

// ConflateContext removes the new line character at the end of the current line.
func (c *Cursor) ConflateContext(ctx context.Context) (ok bool) {
	enable := c.disablePublishing()
	defer enable()

	pos := c.cursorAtScroll()
	if pos.Y >= c.rows() {
		ok = false
		return
	}

	if c.view().Columns(pos.Y) == 0 {
		ok = c.buffer().DeleteRowContext(ctx, pos.Y)
		return
	}
	x, ok := c.buffer().ConflateRowContext(ctx, pos.Y)
	if ok {
		c.setCursorAfterUpdate(term.Coordinates{Y: pos.Y, X: x})
	}
	return
}

// Join is equivalent to calling JoinContext with the cursor context.
func (c *Cursor) Join() (ok bool) {
	return c.JoinContext(c.ctx)
}

// JoinContext joins the current line with the one below it, separated by
// a single space, and leaves the cursor at the join point. It follows
// Vim's J: the next line's leading blanks are dropped, and no space is
// inserted when either line is blank, when the current line already ends
// in a blank, or when the next line starts with a closing parenthesis.
// A line comment leader shared by both lines is dropped as well, matching
// Vim's "j" format option.
func (c *Cursor) JoinContext(ctx context.Context) (ok bool) {
	enable := c.disablePublishing()
	defer enable()

	pos := c.cursorAtScroll()
	cells := c.view().RawCells()
	if pos.Y < 0 || pos.Y+1 >= len(cells) {
		return false
	}
	curr, next := cells[pos.Y], cells[pos.Y+1]

	start := skipBlankCells(next, 0)
	if leader, found := c.sharedCommentLeader(curr, next, start); found {
		start = skipBlankCells(next, start+leader)
	}

	var separator string
	if len(curr) > 0 && start < len(next) &&
		next[start].Ch != ')' &&
		isNoneOf(curr[len(curr)-1], blankCharacters) {
		separator = " "
	}

	from, _, _ := c.buffer().Edit(ctx,
		term.Coordinates{Y: pos.Y, X: len(curr)},
		term.Coordinates{Y: pos.Y + 1, X: start},
		separator)
	c.setCursorAfterUpdate(from)
	return true
}

// sharedCommentLeader reports the cell length of the line comment leader
// that both lines start with. Vim only strips the leader from the joined
// line when the line above is a comment too.
func (c *Cursor) sharedCommentLeader(curr, next []term.Cell, nextStart int) (int, bool) {
	if !c.commentSpec.HasLine() {
		return 0, false
	}
	currStart := skipBlankCells(curr, 0)
	for _, prefix := range c.commentSpec.Line {
		leader := []rune(prefix)
		if cellsHavePrefix(curr, currStart, leader) &&
			cellsHavePrefix(next, nextStart, leader) {
			return len(leader), true
		}
	}
	return 0, false
}

func skipBlankCells(cells []term.Cell, at int) int {
	for at < len(cells) && isOneOf(cells[at], blankCharacters) {
		at++
	}
	return at
}

func cellsHavePrefix(cells []term.Cell, at int, prefix []rune) bool {
	if at+len(prefix) > len(cells) {
		return false
	}
	for i, r := range prefix {
		if cells[at+i].Ch != r {
			return false
		}
	}
	return true
}

// DeleteHorizontalSpace deletes the blank characters (spaces and tabs)
// surrounding the cursor on the current line, leaving the cursor where the
// whitespace run began. It mirrors Emacs delete-horizontal-space (M-\).
func (c *Cursor) DeleteHorizontalSpace() (ok bool) {
	return c.DeleteHorizontalSpaceContext(c.ctx)
}

// DeleteHorizontalSpaceContext is DeleteHorizontalSpace with an explicit context.
func (c *Cursor) DeleteHorizontalSpaceContext(ctx context.Context) (ok bool) {
	enable := c.disablePublishing()
	defer enable()

	pos := c.cursorAtScroll()
	cells := c.view().RawCells()
	if pos.Y < 0 || pos.Y >= len(cells) {
		return false
	}
	line := cells[pos.Y]

	blank := func(x int) bool {
		return x >= 0 && x < len(line) && isOneOf(line[x], blankCharacters)
	}

	from := pos.X
	for blank(from - 1) {
		from--
	}
	to := pos.X
	for blank(to) {
		to++
	}
	if from == to {
		return false
	}

	c.buffer().Edit(ctx,
		term.Coordinates{Y: pos.Y, X: from},
		term.Coordinates{Y: pos.Y, X: to}, "")
	c.setCursorAfterUpdate(term.Coordinates{Y: pos.Y, X: from})
	return true
}

func (c *Cursor) setSelection() (ok bool) {
	from, to, ok := c.SelectionRange()
	if !ok {
		c.selection.cells = nil
		c.SetLocationList(internalLocationListPriority, selectionLocationListID, nil)
		return false
	}

	var sels []cell.Selection
	switch c.selection.mode {
	case StandardSelection:
		c.selection.cells, sels, ok = c.buffer().Select(from, to)
	case LineSelection:
		c.selection.cells, sels, ok = c.buffer().SelectLine(from, to)
	case BlockSelection:
		c.selection.cells, sels, ok = c.buffer().SelectBlock(from, to)
	case NoSelection:
		c.selection.cells = nil
		ok = true
	}

	var locs []textapi.Location
	for _, sel := range sels {
		locs = append(locs, textapi.Location{
			From: sel.From,
			To:   sel.To,
			Attr: term.Attributes{Attrs: term.AttrReverse},
		})
	}

	c.SetLocationList(internalLocationListPriority, selectionLocationListID, LocationSlice(locs))

	return
}

// SelectionBounds returns the unsorted (anchor, cursor) coordinates of
// the current selection, if any. The returned coordinates are clamped
// into the buffer; callers that need a sorted (from <= to) range can
// use term.CoordinatesSort. ok is false when there is no active
// selection.
func (c *Cursor) SelectionBounds() (from, to term.Coordinates, ok bool) {
	return c.selectionBounds()
}

// SelectionRange returns the sorted, half-open [from, to) range the
// selection highlight covers, with RightInclusiveSemantics and line
// expansion folded in. Unlike SelectionBounds, the raw (anchor,
// cursor) pair, it matches exactly what setSelection highlights.
func (c *Cursor) SelectionRange() (from, to term.Coordinates, ok bool) {
	from, to, ok = c.selectionBounds()
	if !ok {
		return
	}
	if c.selection.mode == BlockSelection {
		from, to = term.CoordinatesBlockSort(from, to)
	} else {
		from, to = term.CoordinatesSort(from, to)
	}
	if !c.selection.explicit && c.RightInclusiveSemantics {
		to.X++
	}
	if c.selection.mode == LineSelection {
		from.X = 0
		to.X = 0
		if to.Y < c.rows() {
			to.X = c.view().Columns(to.Y)
		}
	}
	return
}

func (c *Cursor) selectionBounds() (from, to term.Coordinates, ok bool) {
	if c.selection.mode == NoSelection {
		return
	}

	from, ok = c.clampSelectionCoordinates(c.selection.scrollFrom)
	if !ok {
		return
	}

	if c.selection.explicit {
		to, ok = c.clampSelectionCoordinates(c.selection.scrollTo)
		return
	}

	to, ok = c.cursorAtScrollBounds()
	return
}

func (c *Cursor) clampSelectionCoordinates(pos term.Coordinates) (term.Coordinates, bool) {
	rows := c.rows()
	if rows == 0 || pos.Y < 0 || pos.Y >= rows {
		return term.Coordinates{}, false
	}
	pos.X = max(0, min(pos.X, c.view().Columns(pos.Y)))
	return pos, true
}

func (c *Cursor) setExplicitSelection(
	mode SelectMode,
	from, to, cursorPos term.Coordinates,
) (ok bool) {
	from, ok = c.clampSelectionCoordinates(from)
	if !ok {
		return false
	}
	to, ok = c.clampSelectionCoordinates(to)
	if !ok {
		return false
	}
	cursorPos, ok = c.clampSelectionCoordinates(cursorPos)
	if !ok {
		return false
	}

	prevMode := c.selection.mode
	c.selection.mode = NoSelection
	c.setSelection()
	c.moveToScroll(cursorPos)

	c.selection.mode = mode
	c.selection.scrollFrom = from
	c.selection.scrollTo = to
	c.selection.explicit = true
	ok = c.setSelection()
	if !ok {
		c.selection.mode = prevMode
		c.selection.explicit = false
	}
	return ok
}

// returns ok=false if there's no content to select in buffer
func (c *Cursor) cursorAtScrollBounds() (pos term.Coordinates, ok bool) {
	rows := c.rows()
	if rows == 0 {
		pos = term.Coordinates{}
		return
	}

	pos = c.cursorAtScroll()
	ok = true

	if pos.Y >= rows {
		pos.Y = rows
		pos.X = 0
		return
	}

	pos.X = min(pos.X, c.view().Columns(pos.Y))
	return
}

// SelectionMode returns the current SelectMode if any.
func (c *Cursor) SelectionMode() (mode SelectMode, ok bool) {
	mode = c.selection.mode
	ok = mode != NoSelection
	return
}

// Select anchors the current cursor position as the start of a text selection.
// In order to unset anchor, use Unselect(). It returns true if cursor is in bounds or
// false if selection failed. If cursor has already been called one of the Select methods,
// then this method switches to the new mode and maintains original cursor position.
func (c *Cursor) Select() (ok bool) {
	c.pushSelectionHistory()
	mode := c.selection.mode
	c.selection.mode = StandardSelection
	c.selection.explicit = false
	if mode != NoSelection {
		ok = c.setSelection()
		return
	}
	c.selection.scrollFrom, ok = c.cursorAtScrollBounds()
	if !ok {
		return
	}
	ok = c.setSelection()
	return
}

// SelectLine anchors the current cursor position as the start of a line selection.
// In order to unset anchor, use Unselect(). It returns true if cursor is in bounds or
// false if selection failed. If cursor has already been called one of the Select methods,
// then this method switches to the new mode and maintains original cursor position.
func (c *Cursor) SelectLine() (ok bool) {
	c.pushSelectionHistory()
	mode := c.selection.mode
	c.selection.mode = LineSelection
	c.selection.explicit = false
	if mode != NoSelection {
		ok = c.setSelection()
		return
	}
	c.selection.scrollFrom, ok = c.cursorAtScrollBounds()
	if !ok {
		return
	}
	ok = c.setSelection()
	return
}

// SelectBlock anchors the current cursor position as the start of a block selection.
// In order to unset anchor, use Unselect(). It returns true if cursor is in bounds or
// false if selection failed. If cursor has already been called one of the Select methods,
// then this method switches to the new mode and maintains original cursor position.
func (c *Cursor) SelectBlock() (ok bool) {
	c.pushSelectionHistory()
	mode := c.selection.mode
	c.selection.mode = BlockSelection
	c.selection.explicit = false
	if mode != NoSelection {
		ok = c.setSelection()
		return
	}
	c.selection.scrollFrom, ok = c.cursorAtScrollBounds()
	if !ok {
		return
	}
	ok = c.setSelection()
	return
}

// Unselect resets the current selection anchor.
func (c *Cursor) Unselect() bool {
	if c.selection.mode == NoSelection {
		return false
	}
	c.pushSelectionHistory()
	c.selection.mode = NoSelection
	c.selection.explicit = false
	c.selection.cells = nil
	c.SetLocationList(internalLocationListPriority, selectionLocationListID, nil)
	return true
}

// currentSelectionSnapshot captures the current selection and caret state so it
// can be restored later by UndoSelection/RedoSelection.
func (c *Cursor) currentSelectionSnapshot() selectionSnapshot {
	return selectionSnapshot{
		mode:       c.selection.mode,
		scrollFrom: c.selection.scrollFrom,
		scrollTo:   c.selection.scrollTo,
		explicit:   c.selection.explicit,
		cursor:     c.cursorAtScroll(),
	}
}

// pushSelectionHistory records the current selection state onto the undo stack
// before it is replaced, discarding the redo stack. It is a no-op when the new
// state would be identical to the last recorded one, so repeated selections of
// the same range do not clutter the history.
func (c *Cursor) pushSelectionHistory() {
	if c.selectionHistory.suppress {
		return
	}
	snap := c.currentSelectionSnapshot()
	h := &c.selectionHistory
	if n := len(h.undo); n > 0 && h.undo[n-1] == snap {
		return
	}
	h.undo = append(h.undo, snap)
	if len(h.undo) > maxSelectionHistory {
		h.undo = h.undo[len(h.undo)-maxSelectionHistory:]
	}
	h.redo = h.redo[:0]
}

// restoreSelectionSnapshot restores the selection and caret to a snapshot.
func (c *Cursor) restoreSelectionSnapshot(snap selectionSnapshot) {
	c.selectionHistory.suppress = true
	defer func() { c.selectionHistory.suppress = false }()
	if snap.mode == NoSelection {
		c.Unselect()
		c.moveToScroll(snap.cursor)
		return
	}
	c.setExplicitSelection(snap.mode, snap.scrollFrom, snap.scrollTo, snap.cursor)
}

// UndoSelection restores the selection and caret to the state prior to the most
// recent selection change. It returns false when there is no earlier state.
func (c *Cursor) UndoSelection() bool {
	h := &c.selectionHistory
	if len(h.undo) == 0 {
		return false
	}
	h.redo = append(h.redo, c.currentSelectionSnapshot())
	snap := h.undo[len(h.undo)-1]
	h.undo = h.undo[:len(h.undo)-1]
	c.restoreSelectionSnapshot(snap)
	return true
}

// RedoSelection re-applies a selection change previously reverted by
// UndoSelection. It returns false when there is nothing to redo.
func (c *Cursor) RedoSelection() bool {
	h := &c.selectionHistory
	if len(h.redo) == 0 {
		return false
	}
	h.undo = append(h.undo, c.currentSelectionSnapshot())
	snap := h.redo[len(h.redo)-1]
	h.redo = h.redo[:len(h.redo)-1]
	c.restoreSelectionSnapshot(snap)
	return true
}

// Selection returns the current text under either text, line or block selection.
func (c *Cursor) Selection() string {
	s := cell.RowsToString(c.selection.cells)
	if c.selection.mode == LineSelection && len(s) > 0 {
		s += "\n"
	}
	return s
}

// Redo reverses the previously reversed update to the underlying buffer.
func (c *Cursor) Redo() bool {
	enable := c.disablePublishing()
	defer enable()

	ok, at := c.buffer().Redo()
	if !ok {
		return false
	}
	c.setCursorAfterUpdate(at)
	return true
}

// Undo reverses the last update to the underlying buffer.
func (c *Cursor) Undo() bool {
	enable := c.disablePublishing()
	defer enable()

	ok, at := c.buffer().Undo()
	if !ok {
		return false
	}
	c.setCursorAfterUpdate(at)
	return true
}

// Line returns the row number of the row where the cursor is positioned.
func (c *Cursor) Line() int {
	return c.cursorAtScroll().Y
}

// Column returns the column number of the column where the cursor is positioned.
func (c *Cursor) Column() int {
	return c.cursorAtScroll().X
}

// Cell returns the cell where the cursor is positioned or false if there's no cell
// at the current cursor position.
func (c *Cursor) Cell() (term.Cell, bool) {
	return c.cellAtCursor()
}

// DeleteSelection deletes the current text under selection and returns true
// or does nothing and returns false.
func (c *Cursor) DeleteSelection() (ok bool) {
	if c.selection.mode == NoSelection {
		return
	}

	mode := c.selection.mode
	explicit := c.selection.explicit
	from, to, ok := c.selectionBounds()
	c.Unselect()

	// allow for subscribers of buffer to intercept via OnWillDelete
	// the current selection mode via SelectionMode.
	c.selection.mode = mode

	// this means that content was modified after Select started
	// and now there's no content to select, so we are done.
	if !ok {
		return
	}

	// buffer delete uses right exclusive semantics
	if mode == BlockSelection {
		from, to = term.CoordinatesBlockSort(from, to)
	} else {
		from, to = term.CoordinatesSort(from, to)
	}
	if !explicit && c.RightInclusiveSemantics {
		to.X++
	}

	var start term.Coordinates
	var str string
	switch mode {
	case StandardSelection:
		start, str = c.buffer().Delete(from, to)
	case LineSelection:
		start, str = c.buffer().DeleteLine(from, to)
	case BlockSelection:
		start, str = c.buffer().DeleteBlock(from, to)
	}
	c.selection.mode = NoSelection
	c.setCursorAfterUpdate(start)
	ok = str != ""
	return
}

// UppercaseSelection updates the current text under selection to upper case
// or does nothing and returns false.
func (c *Cursor) UppercaseSelection() (ok bool) {
	return c.selectionOp(func(cells string) string {
		return strings.ToUpper(cells)
	})
}

// LowercaseSelection updates the current text under selection to upper case
// or does nothing and returns false.
func (c *Cursor) LowercaseSelection() (ok bool) {
	return c.selectionOp(func(cells string) string {
		return strings.ToLower(cells)
	})
}

// ToggleCaseSelection toggles the case of each character in the current
// selection (upper → lower, lower → upper) and returns true, or does nothing
// and returns false when there is no selection.
func (c *Cursor) ToggleCaseSelection() (ok bool) {
	return c.selectionOp(func(cells string) string {
		return toggleCaseString(cells)
	})
}

// ToggleCase toggles the case of the character under the cursor and advances
// the cursor one position to the right, mimicking vim's ~ in normal mode.
func (c *Cursor) ToggleCase() (ok bool) {
	cell, has := c.cellAtCursor()
	if !has {
		return
	}
	ch := cell.Ch
	var toggled rune
	if unicode.IsUpper(ch) {
		toggled = unicode.ToLower(ch)
	} else {
		toggled = unicode.ToUpper(ch)
	}
	if toggled != ch {
		c.Replace(toggled)
	}
	c.MoveRight()
	ok = true
	return
}

// ToggleLineComment toggles the configured line-comment prefix on the current
// line or selected lines.
func (c *Cursor) ToggleLineComment() bool {
	if !c.commentSpec.HasLine() {
		return false
	}
	prefix := c.commentSpec.Line[0]
	fromY, toY := c.commentLineBounds()
	nonBlank := 0
	for y := fromY; y <= toY; y++ {
		if strings.TrimLeft(c.lineString(y), " \t") != "" {
			nonBlank++
		}
	}
	if nonBlank == 0 {
		return false
	}
	commented := c.lineCommentedRanges(prefix, fromY, toY)
	if len(commented) == toY-fromY+1 {
		for i := len(commented) - 1; i >= 0; i-- {
			if commented[i].Start == commented[i].End {
				continue
			}
			c.buffer().Delete(commented[i].Start, commented[i].End)
		}
		c.setCursorAfterLineCommentToggle(prefix, false)
		return true
	}
	for y := toY; y >= fromY; y-- {
		line := c.lineString(y)
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		indent := len(line) - len(trimmed)
		c.buffer().InsertString(term.Coordinates{Y: y, X: indent}, prefix+" ")
	}
	c.setCursorAfterLineCommentToggle(prefix, true)
	return true
}

// ToggleBlockComment toggles the configured block comment delimiters around the
// current selection.
func (c *Cursor) ToggleBlockComment() bool {
	if !c.commentSpec.HasBlock() {
		return false
	}
	open, close := c.commentSpec.Block[0].Start, c.commentSpec.Block[0].End
	from, to, ok := c.selectionBounds()
	if ok {
		from, to = term.CoordinatesSort(from, to)
		if !c.selection.explicit && c.RightInclusiveSemantics {
			to.X++
		}
	}
	if !ok {
		cur := c.cursorAtScroll()
		from, to = cur, cur
	}
	if rngs, covered := c.commentCoverage(term.Range{Start: from, End: to}); covered && len(rngs) == 1 {
		comment := c.rangeString(rngs[0].Start, rngs[0].End)
		if strings.HasPrefix(comment, open) && strings.HasSuffix(comment, close) {
			c.buffer().Delete(term.Coordinates{Y: rngs[0].End.Y, X: rngs[0].End.X - len(close)}, rngs[0].End)
			c.buffer().Delete(rngs[0].Start, term.Coordinates{Y: rngs[0].Start.Y, X: rngs[0].Start.X + len(open)})
			c.Unselect()
			c.setCursorAfterUpdate(rngs[0].Start)
			return true
		}
	}
	if !ok {
		return false
	}
	c.buffer().InsertString(to, close)
	c.buffer().InsertString(from, open)
	c.setCursorAfterUpdate(term.Coordinates{Y: from.Y, X: from.X + len(open)})
	return true
}

func toggleCaseString(s string) string {
	runes := []rune(s)
	for i, r := range runes {
		if unicode.IsUpper(r) {
			runes[i] = unicode.ToLower(r)
		} else if unicode.IsLower(r) {
			runes[i] = unicode.ToUpper(r)
		}
	}
	return string(runes)
}

// TryIndent attempts to indent the cursor if an indent service is available.
// When the line is below the syntax target indent, TryIndent snaps the line
// up to the target. Otherwise it always inserts exactly one full indent level
// at the start of the line (a tab for IndentRuneTab, or strings.Repeat(" ",
// tabspaces) for IndentRuneSpace) and advances the cursor by that width.
// TryIndent never dedents; explicit snap-to-target dedent behaviour is
// available through Reindent / ReindentSelection.
// Returns false when no indent service is available so the caller can fall
// back to inserting the indent rune literally.
func (c *Cursor) TryIndent(indentRune rune, tabspaces int) bool {
	pos := c.cursorAtScroll()
	svc := c.getIndentService()
	target, ok := svc.IndentationAt(pos.Y)
	if !ok {
		return false
	}
	current, _ := c.getIndentation(pos, indentRune, tabspaces)
	targetCells := target
	if indentRune == IndentRuneSpace {
		targetCells *= c.resolveTabspaces(tabspaces)
	}
	var after term.Coordinates
	if current < targetCells {
		after, _ = c.doTryIndent(c.ctx, pos, target, indentRune, tabspaces)
	} else {
		indent, width := c.indentMaterial(indentRune, tabspaces)
		c.buffer().InsertString(pos, indent)
		after = pos
		after.X += width
	}
	c.setCursorAfterUpdate(after)
	return true
}

// ToggleHide either unhides the hidden block at cursor,
// or hides the current selection.
func (c *Cursor) ToggleHide() (ok bool) {
	if c.Unhide() {
		return true
	}
	return c.HideSelection()
}

// HideSelection hides the current text under selection and returns true
// or does nothing and returns false.
func (c *Cursor) HideSelection() (ok bool) {
	if c.selection.mode == NoSelection {
		return
	}

	explicit := c.selection.explicit
	from, to, ok := c.selectionBounds()
	c.Unselect()
	// this means that content was modified after Select started
	// and now there's no content to select, so we are done.
	if !ok {
		return
	}

	from, to = term.CoordinatesSort(from, to)
	if !explicit && c.RightInclusiveSemantics {
		to.X++
	}
	ok = c.scroll.MarkHidden(from.Y, to.Y)
	return
}

// Unhide un-hides the block of hidden lines starting at the cursor position.
func (c *Cursor) Unhide() (ok bool) {
	at, ok := c.cursorAtScrollBounds()
	if !ok {
		return
	}
	ok = c.scroll.MarkVisible(at.Y)
	return
}

// CopySelection copies the current text under selection and returns true
// or does nothing and returns false.
func (c *Cursor) CopySelection(registerID string, clip clipboard.Register) (ok bool, err error) {
	if c.selection.mode == NoSelection ||
		(c.scroll.Width() == 0 && c.scroll.Wrap) {
		return
	}

	enable := c.disablePublishing()
	defer enable()

	selection := c.Selection()
	mode := c.selection.mode
	c.Unselect()
	c.moveToScroll(c.selection.scrollFrom)

	ok = true
	err = clip.Copy(registerID, clipboard.Data{Text: selection, Metadata: mode})
	return
}

// CopySelectionNoUnselect copies the current text under selection and returns true
// or does nothing and returns false, but as opposed to CopySelection,
// it keeps selection selected.
func (c *Cursor) CopySelectionNoUnselect(
	registerID string, clip clipboard.Register,
) (ok bool, err error) {
	if c.selection.mode == NoSelection {
		return
	}

	enable := c.disablePublishing()
	defer enable()

	selection := c.Selection()
	mode := c.selection.mode

	ok = true
	err = clip.Copy(registerID, clipboard.Data{Text: selection, Metadata: mode})
	return
}

// MoveToBounds moves the cursor up and to the left until it is in a row
// with content and it is 'padding' cells away from the last column in the row.
// If cursor is already in a row and/or in a column with content, then this method
// does nothing.
func (c *Cursor) MoveToBounds(padding int) {
	enable := c.disablePublishing()
	defer enable()

	for c.Line() < 0 && c.MoveLineDown() {
	}

	for c.Line() >= c.rows() && c.MoveLineUp() {
	}

	for c.Column() < 0 && c.MoveRight() {
	}

	for c.Line() < c.rows() && c.Column() >= c.view().Columns(c.Line())+padding && c.MoveLeft() {
	}
}

func (c *Cursor) moveToChar(
	ch rune, findResult func(int, cell.Searcher) (term.Coordinates, bool),
) bool {
	enable := c.disablePublishing()
	defer enable()

	cursor := c.cursorAtScroll()
	if cursor.Y >= c.rows() {
		return false
	}
	lastPos := c.view().Columns(cursor.Y)
	start := term.Coordinates{Y: cursor.Y}
	end := term.Coordinates{Y: cursor.Y, X: lastPos}

	cells, _, ok := c.buffer().Select(start, end)
	if !ok || len(cells) == 0 {
		return false
	}

	view := cell.NewView(cells)

	searcher := cell.NewSimpleSearcher(view)
	n := searcher.Search(string(ch))
	if n == 0 {
		return false
	}

	result, ok := findResult(n, searcher)
	if !ok { // results not aligned with direction
		return false
	}

	c.moveToScroll(term.Coordinates{Y: cursor.Y, X: result.X})

	return true
}

// MoveToNextChar moves the cursor to the next occurence of ch in the current line,
// from the cursor's current position.
func (c *Cursor) MoveToNextChar(ch rune) bool {
	cursor := c.cursorAtScroll()
	return c.moveToChar(ch, func(n int, searcher cell.Searcher) (term.Coordinates, bool) {
		for range n {
			result, _ := searcher.NextResult()
			if result.X > cursor.X {
				return result, true
			}
		}
		return term.Coordinates{}, false
	})
}

// MoveToPrevChar moves the cursor to the previous occurence of ch in the current line,
// from the cursor's current position.
func (c *Cursor) MoveToPrevChar(ch rune) bool {
	cursor := c.cursorAtScroll()
	return c.moveToChar(ch, func(n int, searcher cell.Searcher) (term.Coordinates, bool) {
		for range n {
			result, _ := searcher.PrevResult()
			if result.X < cursor.X {
				return result, true
			}
		}
		return term.Coordinates{}, false
	})
}

// ShiftLineRight shifts the current cursor's line one indent level to the right.
// The indent material used is determined by indentRune (tab or space); when
// inserting spaces, tabspaces controls the number of spaces per indent level.
func (c *Cursor) ShiftLineRight(indentRune rune, tabspaces int) {
	cursor := c.cursorAtScroll()
	indent, width := c.indentMaterial(indentRune, tabspaces)
	at := term.Coordinates{Y: cursor.Y}
	c.buffer().InsertString(at, indent)
	cursor.X += width
	c.setCursorAfterUpdate(cursor)
}

// ShiftLineLeft shifts the current cursor's line one indent level to the left.
// The indent material used is determined by indentRune (tab or space); when
// dedenting spaces, tabspaces controls the number of spaces per indent level.
// It returns false if the line's start of content is already at the start of
// the line.
func (c *Cursor) ShiftLineLeft(indentRune rune, tabspaces int) bool {
	cursor := c.cursorAtScroll()
	removed, ok := c.shiftRowLeft(cursor.Y, indentRune, tabspaces)
	if !ok {
		return false
	}
	cursor.X -= removed
	if cursor.X < 0 {
		cursor.X = 0
	}
	c.setCursorAfterUpdate(cursor)
	return true
}

func (c *Cursor) getShiftSelection() (from, to term.Coordinates) {
	from, to, _ = c.selectionBounds()
	switch c.selection.mode {
	case BlockSelection:
		from, to = term.CoordinatesBlockSort(from, to)
	default:
		from, to = term.CoordinatesSort(from, to)
	}
	return
}

// ShiftSelectionRight shifts the current selection one indent level to the
// right. The indent material is controlled by indentRune.
func (c *Cursor) ShiftSelectionRight(indentRune rune, tabspaces int) {
	from, to := c.getShiftSelection()

	c.Unselect()

	indent, _ := c.indentMaterial(indentRune, tabspaces)
	for y := from.Y; y <= to.Y; y++ {
		c.buffer().InsertString(term.Coordinates{Y: y}, indent)
	}
}

// ShiftSelectionLeft shifts the current selection one indent level to the
// left. The indent material is controlled by indentRune. It returns false if
// selection could not be shifted.
func (c *Cursor) ShiftSelectionLeft(indentRune rune, tabspaces int) (ok bool) {
	from, to := c.getShiftSelection()

	c.Unselect()

	for y := from.Y; y <= to.Y; y++ {
		if _, sok := c.shiftRowLeft(y, indentRune, tabspaces); sok {
			ok = true
		}
	}
	return
}

// indentMaterial returns the indent string to insert for one indent level and
// the number of cells that indent occupies. tabspaces is only consulted when
// indentRune is IndentRuneSpace; a non-positive value falls back to the
// scroll's configured tabspaces.
func (c *Cursor) indentMaterial(indentRune rune, tabspaces int) (string, int) {
	if indentRune == IndentRuneSpace {
		width := c.resolveTabspaces(tabspaces)
		return strings.Repeat(" ", width), width
	}
	return "\t", 1
}

// resolveTabspaces returns a positive indent width, preferring the
// explicitly-passed tabspaces when it is positive and falling back to
// the scroll's configured value otherwise.
func (c *Cursor) resolveTabspaces(tabspaces int) int {
	if tabspaces <= 0 {
		tabspaces = c.scroll.Tabspaces()
	}
	return max(1, tabspaces)
}

// shiftRowLeft removes one indent level from the start of the row and returns
// the number of cells removed and whether anything was removed.
func (c *Cursor) shiftRowLeft(row int, indentRune rune, tabspaces int) (int, bool) {
	buf := c.buffer()
	if indentRune != IndentRuneSpace {
		if buf.ShiftRowLeft(row) {
			return 1, true
		}
		return 0, false
	}
	ts := c.resolveTabspaces(tabspaces)
	from := term.Coordinates{Y: row}
	// Remove up to tabspaces leading ' ' or a single leading '\t'.
	removed := 0
	for removed < ts {
		cell, ok := buf.Cell(term.Coordinates{Y: row, X: removed})
		if !ok {
			break
		}
		if cell.Ch == '\t' && removed == 0 {
			buf.Delete(from, term.Coordinates{Y: row, X: 1})
			return ts, true
		}
		if cell.Ch != ' ' {
			break
		}
		removed++
	}
	if removed == 0 {
		return 0, false
	}
	buf.Delete(from, term.Coordinates{Y: row, X: removed})
	return removed, true
}

// ReindentSelection reindents all lines in the current selection using the indent service.
// It unselects after the operation.
func (c *Cursor) ReindentSelection(indentRune rune, tabspaces int) {
	from, to := c.getShiftSelection()
	c.Unselect()

	for y := from.Y; y <= to.Y; y++ {
		c.reindentAt(term.Coordinates{Y: y}, indentRune, tabspaces)
	}
}

// Reindent reindents the current cursor line if an indent service is available.
func (c *Cursor) Reindent(indentRune rune, tabspaces int) bool {
	pos := c.cursorAtScroll()
	after, ok := c.reindentAt(pos, indentRune, tabspaces)
	if !ok {
		return false
	}
	if after != pos {
		c.setCursorAfterUpdate(after)
	}
	return true
}

// LocationsAtCursor returns the set of locations by location list ID set by SetLocationList,
// at the current cursor position, if there's any. The returned slice is only valid
// until this method is called again.
func (c *Cursor) LocationsAtCursor() ([]textapi.Location, bool) {
	return c.locationStore.LocationsAtCoordinates(c.cursorAtScroll())
}

// SortedLocations returns all the locations sorted by priority level. If two
// location lists have the same priority level, then the location list ID is used
// to disambiguate order.
func (c *Cursor) SortedLocations() []textapi.Location {
	return c.locationStore.SortedLocations()
}

// SetLocationList sets a location list on this cursor. It substitutes and returns
// the previous location list with the same ID, if there was any.
// Any calls to Insert on the underlying Writer will reset all location lists.
func (c *Cursor) SetLocationList(
	pri textapi.LocationPriority, ID string, l LocationList,
) LocationList {
	return c.locationStore.SetLocationList(pri, ID, l)
}

// LocationLists returns a map of location list IDs to their
// respective locations.
func (c *Cursor) LocationLists() []LocationSet {
	return c.locationStore.LocationLists()
}

func (c *Cursor) endOfLocationList(
	l LocationList, op func(LocationList) (textapi.Location, bool),
) (term.Coordinates, bool) {
	prev, ok := l.Current()
	if !ok {
		return term.Coordinates{}, false
	}
	for {
		pos, ok := op(l)
		if !ok {
			return prev.From, true
		}
		prev = pos
	}
}

func (c *Cursor) movePastCursor(
	l LocationList, op, reverse func(LocationList) (textapi.Location, bool),
	continueIf func(*Cursor, term.Coordinates, term.Coordinates) bool,
) bool {
	enable := c.disablePublishing()
	defer enable()

	_, gotLocations := c.endOfLocationList(l, reverse)
	if !gotLocations {
		return false
	}

	cursor := c.cursorAtScroll()
	for {
		pos, _ := l.Current()
		if !continueIf(c, cursor, pos.From) {
			c.moveToScroll(pos.From)
			break
		}

		_, ok := op(l)
		if ok {
			continue
		}

		pos.From, _ = c.endOfLocationList(l, reverse)
		c.moveToScroll(pos.From)
		break
	}

	return cursor != c.cursorAtScroll()
}

// MoveToNextLocation moves the cursor to the next position returned by the location
// list set by SetLocationList. If there isn't a location list set, this method returns
// false.
func (c *Cursor) MoveToNextLocation(ID string) bool {
	l, ok := c.locationStore.LocationList(ID)
	if !ok {
		return false
	}

	return c.movePastCursor(l, (LocationList).Next,
		(LocationList).Prev, isBeforeCursorOrInsideHiddenBlock)
}

// MoveToPrevLocation moves the cursor to the previous position returned by the
// location list set by SetLocationList. If there isn't a location list set,
// this method returns false.
func (c *Cursor) MoveToPrevLocation(ID string) bool {
	l, ok := c.locationStore.LocationList(ID)
	if !ok {
		return false
	}

	return c.movePastCursor(l, (LocationList).Prev,
		(LocationList).Next, isPastCursor)
}

// LocationList returns the location list identified by ID, set previously via SetLocationList,
// or nil and false, if no location list is currently set with the given ID.
func (c *Cursor) LocationList(ID string) (LocationList, bool) {
	return c.locationStore.LocationList(ID)
}

// Word returns the word under the cursor or an empty string if
// the token under cursor is not a word.
func (c *Cursor) Word() string {
	_, _, word := c.scroll.WordAt(c.cursorAtScroll())
	return word
}

// SubscribeScroll subscribes subs to scroll events. This should be
// prefered over subscribing directly to scroll because some
// cursor movements are composite movements that would trigger
// multiple OnSeek dispatches rather than a single one.
func (c *Cursor) SubscribeScroll(subs component.ScrollSubscriber) {
	c.scroll.Subscribe(subs)
}

// ScrollCoordinates translates window coordinates to the scroll coordinates system.
func (c *Cursor) ScrollCoordinates(pos term.Coordinates) term.Coordinates {
	return c.scroll.WindowToScrollCoordinates(pos)
}

// WindowCoordinates translates scroll coordinates to the window coordinates system.
func (c *Cursor) WindowCoordinates(pos term.Coordinates) (term.Coordinates, bool) {
	if c.scroll.Width() == 0 && c.scroll.Wrap {
		// best effort conversion, if scroll width is 0 assume no wrap
		return term.CoordinatesDiff(pos, c.scroll.Offset()), true
	}
	return c.scroll.ScrollToWindowCoordinates(pos)
}

// MoveLineDown is equivalent to MoveDown in non wrap mode. In wrap mode,
// rather than going down one row, the cursor goes down to the line above.
func (c *Cursor) MoveLineDown() bool {
	pos := c.cursorAtScroll()
	pos.Y++
	if pos.Y >= c.rows() {
		return false
	}
	_, ok := c.MoveToScroll(pos)
	return ok
}

// MoveLineUp is equivalent to MoveUp in non wrap mode. In wrap mode,
// rather than going up one row, the cursor goes up to the line below.
func (c *Cursor) MoveLineUp() bool {
	pos := c.cursorAtScroll()
	pos.Y--
	if pos.Y < 0 {
		return false
	}
	_, ok := c.MoveToScroll(pos)
	return ok
}

// FoldAt runs the given callback if a fold is found at or around the
// current cursor position or returns false if folds are not enabled.
func (c *Cursor) FoldAt(ctx context.Context, cb func(term.Range, bool)) bool {
	if c.scheduleNextTick == nil {
		return false
	}

	if block, ok := c.scroll.HiddenBlockAt(c.cursorAtScroll().Y); ok {
		cb(block, ok)
		return true
	}

	return c.opFoldsFrom(ctx, c.scroll.Offset(), func(folds []term.Range) {
		pos := c.cursorAtScroll()

		var ok bool
		var found term.Range
		for _, fold := range folds {
			if pos.Y < fold.Start.Y || pos.Y > fold.End.Y {
				continue
			}
			if !ok || (fold.Start.Y > found.Start.Y || fold.End.Y < found.End.Y) {
				found = fold
				ok = true
			}
		}
		cb(found, ok)
	})
}

// CollapseFold collapses the fold at the current cursor position,
// or returns false if folds are not enabled.
func (c *Cursor) CollapseFold(ctx context.Context) bool {
	if c.scheduleNextTick == nil {
		return false
	}
	return c.FoldAt(ctx, func(fold term.Range, ok bool) {
		if !ok {
			return
		}
		c.scroll.MarkHidden(fold.Start.Y, fold.End.Y)
	})
}

// ExpandFold expands the fold at the current cursor position,
// or returns false if folds are not enabled.
func (c *Cursor) ExpandFold(ctx context.Context) bool {
	if c.scheduleNextTick == nil {
		return false
	}
	return c.FoldAt(ctx, func(fold term.Range, ok bool) {
		if !ok {
			return
		}
		mark := c.Mark()
		if c.scroll.MarkVisible(fold.Start.Y) {
			c.MoveToMark(mark)
		}
	})
}

// SelectFold selects the fold at the current cursor position,
// or returns false if folds are not enabled.
func (c *Cursor) SelectFold(ctx context.Context) bool {
	if c.scheduleNextTick == nil {
		return false
	}
	return c.FoldAt(ctx, func(fold term.Range, ok bool) {
		if !ok {
			return
		}
		c.MoveToScroll(fold.End)
		c.selection.scrollFrom = fold.Start
		c.selection.mode = StandardSelection
		c.setSelection()
	})
}

// ToggleFold toggles the fold at the current cursor position,
// or returns false if folds are not enabled.
func (c *Cursor) ToggleFold(ctx context.Context) bool {
	if c.scheduleNextTick == nil {
		return false
	}
	return c.FoldAt(ctx, func(fold term.Range, ok bool) {
		if !ok {
			return
		}
		if hidden, ok := c.isFoldHidden(fold.Start, fold.End); hidden || !ok {
			c.scroll.MarkVisible(fold.Start.Y)
		} else if ok {
			c.scroll.MarkHidden(fold.Start.Y, fold.End.Y)
		}
	})
}

// CollapseAllFolds collapses all the folds available in the file,
// or returns false if folds are not enabled.
func (c *Cursor) CollapseAllFolds(ctx context.Context) bool {
	if c.scheduleNextTick == nil {
		return false
	}
	return c.opFolds(ctx, func(folds []term.Range) {
		for _, fold := range folds {
			c.scroll.MarkHidden(fold.Start.Y, fold.End.Y)
		}
	})
}

// ExpandAllFolds expands all the folds available in the file,
// or returns false if folds are not enabled.
func (c *Cursor) ExpandAllFolds(ctx context.Context) bool {
	if c.scheduleNextTick == nil {
		return false
	}
	return c.opFolds(ctx, func(folds []term.Range) {
		mark := c.Mark()
		var handled bool
		for _, fold := range folds {
			handled = c.scroll.MarkVisible(fold.Start.Y) || handled
		}
		if handled {
			c.MoveToMark(mark)
		}
	})
}

// ToggleAllFolds toggles all the folds available in the file,
// or returns false if folds are not enabled.
func (c *Cursor) ToggleAllFolds(ctx context.Context) bool {
	if c.scheduleNextTick == nil {
		return false
	}
	return c.opFolds(ctx, func(folds []term.Range) {
		// first determine if they're currently hidden or visible:
		// if we start toggling as we're iterating, nested folds
		// will be incorrectly categorized.
		var visible, hidden []term.Range
		for _, fold := range folds {
			if isHidden, ok := c.isFoldHidden(fold.Start, fold.End); isHidden || !ok {
				visible = append(visible, fold)
			} else if ok {
				hidden = append(hidden, fold)
			}
		}
		for _, fold := range visible {
			c.scroll.MarkVisible(fold.Start.Y)
		}
		for _, fold := range hidden {
			c.scroll.MarkHidden(fold.Start.Y, fold.End.Y)
		}
	})
}

// FindMatchingRuneForward tries to find the target's matching rune by scrolling
// through cells forward, until either a match is found or until the end of the file,
// in which case false is returned.
func (c *Cursor) FindMatchingRuneForward(target, match rune) (term.Coordinates, bool) {
	return c.findMatchRune(target, match, c.matchRuneForward)
}

// FindMatchingRuneBackward tries to find the target's matching rune by scrolling
// through cells backward, until either a match is found or until the end of the file,
// in which case false is returned.
func (c *Cursor) FindMatchingRuneBackward(target, match rune) (term.Coordinates, bool) {
	return c.findMatchRune(target, match, c.matchRuneBackward)
}

func (c *Cursor) disablePublishing() func() {
	if !c.scroll.PublishingEnabled() {
		return func() {}
	}
	enable := c.scroll.DisablePublishing()
	return func() {
		c.seekToScrollCoordinates()
		enable()
	}
}

func (c *Cursor) matchRuneForward(pos *term.Coordinates) bool {
	lastRow := c.rows() - 1
	pos.X++
	for pos.Y <= lastRow && pos.X >= c.view().Columns(pos.Y) {
		pos.Y++
		pos.X = 0
	}
	return pos.Y <= lastRow || (pos.Y == lastRow && pos.X < c.view().Columns(pos.Y))
}

func (c *Cursor) matchRuneBackward(pos *term.Coordinates) bool {
	pos.X--
	for pos.Y > 0 && pos.X < 0 {
		pos.Y--
		pos.X = c.view().Columns(pos.Y) - 1
	}
	return pos.Y >= 0 && pos.X >= 0
}

func (c *Cursor) moveMatchRuneForward(target, match rune) bool {
	pos, ok := c.findMatchRune(target, match, c.matchRuneForward)
	if !ok {
		return false
	}
	_, ok = c.MoveToScroll(pos)
	return ok
}

func (c *Cursor) findMatchRune(
	target, match rune,
	advance func(*term.Coordinates) bool,
) (term.Coordinates, bool) {
	cells := c.view().RawCells()
	pos := c.cursorAtScroll()
	pending := 1
	for pending != 0 && advance(&pos) {
		switch cells[pos.Y][pos.X].Ch {
		case target:
			pending++
		case match:
			pending--
		}
	}

	if pending == 0 {
		return pos, true
	}
	return term.Coordinates{}, false
}

func (c *Cursor) moveMatchRuneBackward(target, match rune) bool {
	pos, ok := c.findMatchRune(target, match, c.matchRuneBackward)
	if !ok {
		return false
	}
	_, ok = c.MoveToScroll(pos)
	return ok
}

func (c *Cursor) setCursorAfterUpdate(atScroll term.Coordinates) {
	if c.scroll.Width() == 0 && c.scroll.Wrap {
		// nothing should be updating if width of the scroll is 0!
		return
	}
	c.scroll.RecalculateWraps()
	res, _ := c.scroll.ScrollToWindowCoordinates(atScroll)
	c.setCursor(res, c.shouldSeek)
}

func autoPairCloseForOpen(open rune) (rune, bool) {
	switch open {
	case '(':
		return ')', true
	case '[':
		return ']', true
	case '{':
		return '}', true
	case '"':
		return '"', true
	case '\'':
		return '\'', true
	default:
		return 0, false
	}
}

func autoPairOpenForClose(close rune) (rune, bool) {
	switch close {
	case ')':
		return '(', true
	case ']':
		return '[', true
	case '}':
		return '{', true
	case '"':
		return '"', true
	case '\'':
		return '\'', true
	default:
		return 0, false
	}
}

func (c *Cursor) insertAutoPairRunes(open, close rune) {
	mode := c.selection.mode
	c.selection.mode = NoSelection
	c.setSelection()

	insertAt := c.cursorAtScroll()
	_, _, _ = c.buffer().Edit(c.ctx, insertAt, insertAt, string([]rune{open, close}))
	c.selection.mode = mode
	c.setSelection()
	c.setCursorAfterUpdate(term.Coordinates{Y: insertAt.Y, X: insertAt.X + 1})
}

func (c *Cursor) autoPairAroundCursor() (
	prevPos, nextPos term.Coordinates, prev, next term.Cell, ok bool,
) {
	pos := c.cursorAtScroll()
	if pos.X <= 0 {
		return term.Coordinates{}, term.Coordinates{}, term.Cell{}, term.Cell{}, false
	}

	prevPos = term.Coordinates{Y: pos.Y, X: pos.X - 1}
	nextPos = pos
	prev, ok = c.cellAtScrollCoordinates(prevPos)
	if !ok {
		return term.Coordinates{}, term.Coordinates{}, term.Cell{}, term.Cell{}, false
	}
	next, ok = c.cellAtScrollCoordinates(pos)
	if !ok {
		return term.Coordinates{}, term.Coordinates{}, term.Cell{}, term.Cell{}, false
	}
	close, ok := autoPairCloseForOpen(prev.Ch)
	return prevPos, nextPos, prev, next, ok && close == next.Ch
}

func autoPairIsBracePair(prev, next term.Cell) bool {
	return prev.Ch == '{' && next.Ch == '}'
}

func (c *Cursor) getIndentation(pos term.Coordinates, indentRune rune, tabspaces int) (ret int, ok bool) {
	cells := c.buffer().RawCells()
	if pos.Y >= len(cells) {
		return
	}
	ts := c.resolveTabspaces(tabspaces)
	for x, cell := range cells[pos.Y] {
		switch cell.Ch {
		case '\t':
			if indentRune == IndentRuneSpace {
				ret += ts
			} else {
				ret++
			}
		case ' ':
			if indentRune == IndentRuneSpace {
				ret++
			} else {
				ok = x == pos.X
				return
			}
		default:
			ok = x == pos.X
			return
		}
	}
	return
}

func (c *Cursor) tryIndent(ctx context.Context, to term.Coordinates, indentRune rune, tabspaces int) (term.Coordinates, bool) {
	svc := c.getIndentService()
	indentation, ok := svc.IndentationAt(to.Y)
	if !ok {
		return to, false
	}
	return c.doTryIndent(ctx, to, indentation, indentRune, tabspaces)
}

func (c *Cursor) reindentAt(pos term.Coordinates, indentRune rune, tabspaces int) (term.Coordinates, bool) {
	svc := c.getIndentService()
	target, ok := svc.IndentationAt(pos.Y)
	if !ok {
		return pos, false
	}
	after, changed := c.doTryIndent(c.ctx, pos, target, indentRune, tabspaces)
	if changed {
		return after, true
	}
	after, changed = c.doTryDedent(c.ctx, pos, target, indentRune, tabspaces)
	if changed {
		return after, true
	}
	return pos, true
}

func (c *Cursor) doTryIndent(ctx context.Context, to term.Coordinates, target int, indentRune rune, tabspaces int) (term.Coordinates, bool) {
	current, _ := c.getIndentation(to, indentRune, tabspaces)
	if indentRune == IndentRuneSpace {
		target *= c.resolveTabspaces(tabspaces)
	}
	diff := target - current
	if diff <= 0 {
		c.log(log.DebugLevel, "try indent: already equal or more than correct indentation: %d", target)
		return to, false
	}

	var builder strings.Builder
	if indentRune == IndentRuneSpace {
		for range diff {
			builder.WriteByte(' ')
		}
	} else {
		for range diff {
			builder.WriteByte('\t')
		}
	}
	buf := c.buffer()
	indent := builder.String()
	// even if given position to indent is not at the start of the line
	// to "indent" we must resolve to start of line
	at := term.Coordinates{Y: to.Y}
	_, _, _ = buf.Edit(ctx, at, at, indent)
	to.X += diff
	return to, true
}

func (c *Cursor) tryDedent(ctx context.Context, pos term.Coordinates, indentRune rune, tabspaces int) (
	term.Coordinates, bool,
) {
	svc := c.getIndentService()
	indentation, ok := svc.IndentationAt(pos.Y)
	if !ok {
		return pos, false
	}
	return c.doTryDedent(ctx, pos, indentation, indentRune, tabspaces)
}

func (c *Cursor) doTryDedent(ctx context.Context, pos term.Coordinates, target int, indentRune rune, tabspaces int) (
	term.Coordinates, bool,
) {
	buf := c.buffer()
	current, _ := c.getIndentation(pos, indentRune, tabspaces)
	if indentRune == IndentRuneSpace {
		target *= c.resolveTabspaces(tabspaces)
	}
	diff := current - target
	if diff <= 0 {
		c.log(log.TraceLevel, "try dedent: already equal or less than correct indentation: %d", target)
		return pos, false
	}
	// start of edit should be at the end of starting tabs block
	from := term.Coordinates{X: (current - diff), Y: pos.Y}
	to := term.Coordinates{X: current, Y: pos.Y}
	if diff <= 0 || from.X < 0 || to.X < 0 {
		c.log(log.TraceLevel, "try dedent: could not dedent at (%v), current: %d, "+
			"should be: %d, from: %v, to: %v", pos, current, target, from, to)
		return pos, false
	}
	cells, _, ok := buf.Select(from, to)
	empty := isEmpty(cells[0])
	if ok && len(cells) != 0 && empty {
		buf.Edit(ctx, from, to, "")
		pos.X -= diff
		return pos, true
	}
	c.log(log.TraceLevel, "try dedent: could not dedent at (%v), cells: %#v, from: %#v, to: %#v ",
		pos, empty, from, to)
	return pos, false
}

func isEmpty(cells []term.Cell) bool {
	for _, cell := range cells {
		switch cell.Ch {
		case ' ', '\t':
			continue
		}
		return false
	}
	return true
}

func (c *Cursor) getIndentService() indentService {
	svc, ok := c.buffer().View().(indentService)
	if ok {
		return svc
	}
	return nopIndentService{}
}

func (c *Cursor) getSelectionService() selectionService {
	svc, ok := c.buffer().View().(selectionService)
	if ok {
		return svc
	}
	return nil
}

func (c *Cursor) getCommentService() commentService {
	svc, ok := c.buffer().View().(commentService)
	if ok {
		return svc
	}
	return nil
}

func (c *Cursor) commentCoverage(rng term.Range) ([]term.Range, bool) {
	svc := c.getCommentService()
	if svc == nil {
		return nil, false
	}
	return svc.CommentCoverage(rng)
}

func (c *Cursor) commentLineBounds() (int, int) {
	if _, ok := c.SelectionMode(); ok {
		if from, to, ok := c.selectionBounds(); ok {
			from, to = term.CoordinatesSort(from, to)
			return from.Y, to.Y
		}
	}
	pos := c.cursorAtScroll()
	return pos.Y, pos.Y
}

func (c *Cursor) lineCommentedRanges(_ string, fromY, toY int) []term.Range {
	ret := make([]term.Range, 0, toY-fromY+1)
	for y := fromY; y <= toY; y++ {
		line := c.lineString(y)
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			ret = append(ret, term.Range{})
			continue
		}
		indent := len(line) - len(trimmed)
		for _, candidate := range c.commentSpec.Line {
			comment := candidate
			if strings.HasPrefix(trimmed, candidate+" ") {
				comment = candidate + " "
			}
			if !strings.HasPrefix(trimmed, comment) {
				continue
			}
			start := term.Coordinates{Y: y, X: indent}
			end := term.Coordinates{Y: y, X: indent + len(comment)}
			coverage, ok := c.commentCoverage(term.Range{Start: start, End: end})
			if ok && len(coverage) != 0 {
				ret = append(ret, term.Range{Start: start, End: end})
				goto nextLine
			}
		}
		return nil
	nextLine:
	}
	return ret
}

func (c *Cursor) setCursorAfterLineCommentToggle(prefix string, inserted bool) {
	cur := c.cursorAtScroll()
	adjust := len(prefix)
	if inserted {
		adjust++
	}
	line := c.lineString(cur.Y)
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" {
		return
	}
	indent := len(line) - len(trimmed)
	if inserted {
		if cur.X >= indent {
			cur.X += adjust
		}
	} else if cur.X >= indent+adjust {
		cur.X -= adjust
	} else {
		cur.X = max(indent, 0)
	}
	c.setCursorAfterUpdate(cur)
}

func (c *Cursor) lineString(y int) string {
	from := term.Coordinates{Y: y}
	to := term.Coordinates{Y: y, X: c.buffer().Columns(y)}
	line, _, _ := c.buffer().Select(from, to)
	return term.CellsToString(line)
}

func (c *Cursor) rangeString(from, to term.Coordinates) string {
	cells, _, _ := c.buffer().Select(from, to)
	return term.CellsToString(cells)
}

func (c *Cursor) currentSelectionRange() (term.Range, term.Coordinates, bool) {
	if c.selection.explicit {
		from, to, ok := c.selectionBounds()
		if !ok {
			return term.Range{}, term.Coordinates{}, false
		}
		from, to = term.CoordinatesSort(from, to)
		return term.Range{Start: from, End: to}, from, true
	}
	pos, ok := c.cursorAtScrollBounds()
	if !ok {
		return term.Range{}, term.Coordinates{}, false
	}
	return term.Range{Start: pos, End: pos}, pos, true
}

// either op is invoked or this function returns false
func (c *Cursor) opFolds(ctx context.Context, op func([]term.Range)) bool {
	svc, ok := c.buffer().View().(foldsService)
	if !ok {
		return false
	}

	folds, ok := svc.Folds()
	if !ok {
		return false
	}
	c.opFoldsIter(ctx, op, folds)
	return true
}

func (c *Cursor) opFoldsFrom(
	ctx context.Context, from term.Coordinates, op func([]term.Range),
) bool {
	svc, ok := c.buffer().View().(foldsService)
	if !ok {
		return false
	}

	folds, ok := svc.FoldsFrom(from)
	if !ok {
		return false
	}
	c.opFoldsIter(ctx, op, folds)
	return true
}

func (c *Cursor) opFoldsIter(
	ctx context.Context, op func([]term.Range), folds iterator.Iterator[term.Range],
) {
	go debug.CapturePanicReport(func() {
		folds, isEmpty := iterator.IsEmpty(ctx, folds)
		if isEmpty {
			c.scheduleNextTick(func() {
				op(nil)
			})
			return
		}
		c.scheduleNextTick(func() {
			defer folds.Close()
			// this needs to roughly follow the same algorithm used by aux_bar
			m := make(map[term.Coordinates]term.Coordinates)
			for {
				fold, ok := folds.Next(ctx)
				if !ok {
					break
				}
				if fold.Start.Y >= fold.End.Y {
					continue
				}
				fold.Start.X = 0 // avoid ambiguity
				if end, exists := m[fold.Start]; exists && end.Y > fold.End.Y {
					continue
				}
				m[fold.Start] = fold.End
			}
			if err := folds.Err(); err != nil {
				c.log(log.ErrorLevel, "error getting folds: %v", err)
				op(nil)
				return
			}
			seq := maps.All(m)
			slice := make([]term.Range, 0, len(m))
			for a, b := range seq {
				slice = append(slice, term.Range{Start: a, End: b})
			}
			sort.Slice(slice, func(i, j int) bool {
				return slice[i].Start.Y < slice[j].Start.Y
			})
			op(slice)
		})
	})
}

func (c *Cursor) isFoldHidden(start, end term.Coordinates) (bool, bool) {
	// convert folds which are scroll coordinates to window coordinates
	foldStart, startOk := c.scroll.ScrollToWindowCoordinates(start)
	foldEnd, endOk := c.scroll.ScrollToWindowCoordinates(end)
	if foldStart.Y != foldEnd.Y && (!startOk || !endOk) {
		// inside hidden block
		return false, false
	}
	return foldStart.Y == foldEnd.Y, true
}

func (c *Cursor) selectionOp(fn func(string) string) (ok bool) {
	if c.selection.mode == NoSelection {
		return
	}

	mode := c.selection.mode
	explicit := c.selection.explicit
	from, to, ok := c.selectionBounds()
	c.Unselect()
	c.selection.mode = mode
	if !ok {
		return
	}

	// buffer delete uses right exclusive semantics
	if mode == BlockSelection {
		from, to = term.CoordinatesBlockSort(from, to)
	} else {
		from, to = term.CoordinatesSort(from, to)
	}
	if !explicit && c.RightInclusiveSemantics {
		to.X++
	}

	var cells [][]term.Cell
	switch mode {
	case StandardSelection:
		cells, _, ok = c.buffer().Select(from, to)
		if ok {
			str := term.CellsToString(cells)
			c.log(log.TraceLevel, "selectionOp: replace with cells: %#v, from: %v, to: %v", cells, from, to)
			c.buffer().Edit(c.ctx, from, to, fn(str))
		}
	case LineSelection:
		cells, sels, ok := c.buffer().SelectLine(from, to)
		if ok {
			lineFrom := sels[0].From
			lineTo := sels[len(sels)-1].To
			c.buffer().Edit(c.ctx, lineFrom, lineTo, fn(term.CellsToString(cells)))
		}
	case BlockSelection:
		ok = false
		return
		/* not supported at the moment
		cells, _, ok = c.buffer().SelectBlock(from, to)
		if ok {
			_, str := c.buffer().DeleteBlock(from, to)
			c.InsertBlock(fn(str))
		}
		*/
	}
	if !ok {
		return
	}
	c.selection.mode = NoSelection
	return
}

func (c *Cursor) setSearchLocationList(text string, word bool) int {
	c.search = text
	n := c.scroll.Search(text)
	searchLength := len([]rune(text))

	searchLoc := make([]textapi.Location, 0, n)
	for range n {
		res, ok := c.scroll.NextResult()
		if !ok {
			panic("invalid scroll search results")
		}
		if word {
			_, _, wordAtPos := c.scroll.WordAt(res)
			// in word mode, if word doesn't match exactly
			// continue with the next result
			if text != wordAtPos {
				continue
			}
		}
		searchLoc = append(searchLoc, textapi.Location{
			From: res,
			To:   term.Coordinates{Y: res.Y, X: res.X + searchLength},
			Attr: c.searchAttr,
		})
	}

	c.SetLocationList(internalLocationListPriority, searchLocationListID, LocationSlice(searchLoc))
	return len(searchLoc)
}

func (c *Cursor) tryMoveDownWindowRow() bool {
	atScroll := c.cursorAtScroll()
	win, _ := c.scroll.ScrollToWindowCoordinates(atScroll)
	win.Y++
	atScroll = c.scroll.WindowToScrollCoordinates(win)
	if atScroll.Y >= c.rows() {
		return false
	}
	_, ok := c.MoveToScroll(atScroll)
	return ok
}

// MoveDisplayDown moves the cursor one display (window) row down.
// When wrap is enabled, this moves within the same buffer line if it
// spans multiple display rows, rather than jumping to the next buffer line.
// Without wrap, this behaves the same as MoveDown.
func (c *Cursor) MoveDisplayDown() bool {
	if !c.scroll.Wrap {
		return c.MoveDown()
	}
	atScroll := c.cursorAtScroll()
	win, _ := c.scroll.ScrollToWindowCoordinates(atScroll)
	win.Y++
	atScroll = c.scroll.WindowToScrollCoordinates(win)
	if atScroll.Y >= c.rows() {
		if c.scroll.SeekDown() {
			return c.MoveDisplayDown()
		}
		return false
	}
	_, ok := c.MoveToScroll(atScroll)
	return ok
}

// MoveDisplayUp moves the cursor one display (window) row up.
// When wrap is enabled, this moves within the same buffer line if it
// spans multiple display rows, rather than jumping to the previous buffer line.
// Without wrap, this behaves the same as MoveUp.
func (c *Cursor) MoveDisplayUp() bool {
	if !c.scroll.Wrap {
		return c.MoveUp()
	}
	atScroll := c.cursorAtScroll()
	win, _ := c.scroll.ScrollToWindowCoordinates(atScroll)
	if win.Y <= 0 {
		if c.scroll.SeekUp() {
			return c.MoveDisplayUp()
		}
		return false
	}
	win.Y--
	atScroll = c.scroll.WindowToScrollCoordinates(win)
	_, ok := c.MoveToScroll(atScroll)
	return ok
}

// MoveDisplayDownLines moves the cursor "n" display rows down.
func (c *Cursor) MoveDisplayDownLines(n int) bool {
	return c.multiplyMove(n, c.MoveDisplayDown)
}

// MoveDisplayUpLines moves the cursor "n" display rows up.
func (c *Cursor) MoveDisplayUpLines(n int) bool {
	return c.multiplyMove(n, c.MoveDisplayUp)
}

func (c *Cursor) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "text.Cursor").Logf(level, msg, args...)
}
