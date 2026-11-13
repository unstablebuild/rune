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

package component

import (
	"context"
	"fmt"
	"image"
	"sort"
	"unicode"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

const (
	// DefaultTabspaces is the default number of tabspaces used by this package.
	DefaultTabspaces int = 4
)

// Scroll adds Draw to a Buffer along with
// scrolling, searching and wrap-around capabilities.
type Scroll struct {
	tabspaces     int
	buf           *cell.Buffer
	searcher      cell.Searcher
	matches       []term.Coordinates
	wrapsLen      int
	wraps         []int
	width, height int
	searchText    []rune
	searchTextStr string
	offset        term.Coordinates
	subs          []ScrollSubscriber
	hidden        int
	hiddenblocks  map[int]int
	hiddenlines   map[int]int
	hiddenmeta    map[int]string
	hiddensorted  []startEndBlock

	onWillEditFrom term.Coordinates
	onWillEditTo   term.Coordinates

	disablePublishing   bool
	lastPublishedOffset term.Coordinates

	// ResultsAttr determines the search result attributes upon matching.
	ResultsAttr term.Attributes

	// Attributes determines the attributes for regular cells.
	Attributes term.Attributes

	// rows longer than the width of the scroll wrap around and
	// are rendered in the next row if Wrap is set to true.
	// If Wrap is set to true, then InvertOffset is ignored (default is false).
	Wrap bool

	// HideAttr determines the attributes used to mark hidden rows.
	HideAttr term.Attributes

	// InvertOffset inverts the semantics of all the Seek* methods,
	// thus the default offset is the maximum offset. This is incompatible
	// with hidden lines, so calls to MarkHidden will panic.
	InvertOffset bool

	// Hatch, when set, draws its stripes over the rows below the content.
	Hatch *Hatch
}

var _ component.Scrollable = (*Scroll)(nil)

// NewScroll allocates storage for a Scroll and initializes it.
func NewScroll(buf *cell.Buffer) (s *Scroll) {
	s = new(Scroll)
	s.Init(buf)
	return
}

func (s *Scroll) initBuffer(buf *cell.Buffer) {
	s.buf = buf
	s.searcher = cell.NewSimpleSearcher(s.buf)
	s.searcher.Reset()
}

// Init initializes this scroll with buf.
func (s *Scroll) Init(buf *cell.Buffer) {
	s.InitPerformance(buf)
	s.SetTabspaces(DefaultTabspaces)
	s.buf.Subscribe((*scrollSubscriber)(s))
	s.hiddenblocks = make(map[int]int)
	s.hiddenlines = make(map[int]int)
	s.hiddenmeta = make(map[int]string)
	if s.HideAttr == (term.Attributes{}) {
		s.HideAttr = s.Attributes
		s.HideAttr.Fg = term.ColorGray
	}
}

// InitPerformance initializes this scroll with limited search functionality:
// Updates to buffer do not update search results. Clearing of search results
// should be done manually by clients by calling Search again after updates,
// if desired.
//
// Note that this initialization method should be used instead of Init
// if the given cell.Buffer has been also initialized with InitPerformance.
//
// MarkHidden is disabled.
func (s *Scroll) InitPerformance(buf *cell.Buffer) {
	if s.ResultsAttr == (term.Attributes{}) {
		s.ResultsAttr.Attrs = term.AttrReverse
	}
	s.initBuffer(buf)
}

// SetTabspaces updates the initial tabspaces value.
func (s *Scroll) SetTabspaces(tabspaces int) {
	if tabspaces <= 0 {
		tabspaces = 1 // 0, disabled, effectively is equivalent to 1
	}
	s.tabspaces = tabspaces
}

// Tabspaces returns the tabspaces value.
func (s *Scroll) Tabspaces() int {
	return s.tabspaces
}

// RepositionLineCenter centers the scroll's view around the given position,
// scrolling up or down as needed. It returns how many lines
// it seeked. A negative seek value indicates it seeked up,
// whereas a positive seek value indicates it seeked down.
func (s *Scroll) RepositionLineCenter(y int) (seek int) {
	return s.repositionLine(s.height/2, y)
}

// RepositionLineTop repositions the scroll's view such that
// the given position is at the top of the view,
// scrolling up or down as needed. It returns how many lines
// it seeked. A negative seek value indicates it seeked up,
// whereas a positive seek value indicates it seeked down.
func (s *Scroll) RepositionLineTop(y int) (seek int) {
	return s.repositionLine(0, y)
}

// RepositionLineBottom repositions the scroll's view such that
// the given position is at the bottom of the view,
// scrolling up or down as needed. It returns how many lines
// it seeked. A negative seek value indicates it seeked up,
// whereas a positive seek value indicates it seeked down.
func (s *Scroll) RepositionLineBottom(y int) (seek int) {
	return s.repositionLine(s.height-1, y)
}

// CanSeekUp returns true if SeekUp would seek one row up.
func (s *Scroll) CanSeekUp() bool {
	if s.InvertOffset {
		return s.offset.Y < s.getMaxYOffset()
	}
	return s.offset.Y > 0
}

// CanSeekDown returns true if SeekDown would seek one row down.
func (s *Scroll) CanSeekDown() bool {
	if s.InvertOffset {
		return s.offset.Y > 0
	}
	return s.offset.Y < s.getMaxYOffset()
}

// MaxSeekOffset returns the max seek offset.
func (s *Scroll) MaxSeekOffset() int {
	return s.getMaxYOffset()
}

// SeekOffset returns the current seek offset.
func (s *Scroll) SeekOffset() int {
	return s.offset.Y
}

// CanSeekLeft returns true if SeekLeft would seek one column left.
func (s *Scroll) CanSeekLeft() bool {
	return !s.Wrap && s.offset.X > 0
}

// CanSeekRight returns true if SeekRight would seek one column right.
func (s *Scroll) CanSeekRight() bool {
	return !s.Wrap && s.offset.X < s.getMaxXOffset()
}

// SeekUp shifts the contents of this scroll one row up.
func (s *Scroll) SeekUp() (ok bool) {
	if ok = s.CanSeekUp(); ok {
		dispatch := s.dispatchSubscribers()
		defer dispatch()
		if !s.InvertOffset {
			s.offset.Y--
		} else {
			s.offset.Y++
		}
	}
	return ok
}

// SeekDown shifts the contents of this scroll one row down.
func (s *Scroll) SeekDown() (ok bool) {
	if ok = s.CanSeekDown(); ok {
		dispatch := s.dispatchSubscribers()
		defer dispatch()
		if !s.InvertOffset {
			s.offset.Y++
		} else {
			s.offset.Y--
		}
	}
	return
}

// SeekLeft shifts the contents of this scroll one column left.
func (s *Scroll) SeekLeft() (ok bool) {
	if ok = s.CanSeekLeft(); ok {
		dispatch := s.dispatchSubscribers()
		defer dispatch()
		s.offset.X--
	}
	return
}

// SeekRight shifts the contents of this scroll one column right.
func (s *Scroll) SeekRight() (ok bool) {
	if ok = s.CanSeekRight(); ok {
		dispatch := s.dispatchSubscribers()
		defer dispatch()
		s.offset.X++
	}
	return
}

// seekVertical shifts the contents of this scroll such that
// the vertical offset is y.
func (s *Scroll) seekVertical(y int) (ok bool) {
	if y < 0 {
		y = 0
	}

	ok = s.offset.Y != y
	if ok {
		dispatch := s.dispatchSubscribers()
		defer dispatch()
		s.offset.Y = y
	}
	return
}

// seekHorizontal shifts the contents of this scroll such that
// the horizontal offset is x.
func (s *Scroll) seekHorizontal(x int) (ok bool) {
	if x < 0 {
		x = 0
	}

	ok = s.offset.X != x
	if ok {
		dispatch := s.dispatchSubscribers()
		defer dispatch()
		s.offset.X = x
	}
	return
}

// SeekEndLine shifts the contents of this scroll to the maximum x offset.
func (s *Scroll) SeekEndLine() bool {
	return s.seekHorizontal(s.getMaxXOffset())
}

// SeekStartLine shifts the contents of this scroll to the minimum x offset.
func (s *Scroll) SeekStartLine() bool {
	return s.seekHorizontal(0)
}

// SeekEndFile shifts the contents of this scroll to the maximum y offset.
func (s *Scroll) SeekEndFile() bool {
	if s.InvertOffset {
		return s.seekVertical(0)
	}
	return s.seekVertical(s.getMaxYOffset())
}

// SeekStartFile shifts the contents of this scroll to the minimum y offset.
func (s *Scroll) SeekStartFile() bool {
	if s.InvertOffset {
		return s.seekVertical(s.getMaxYOffset())
	}
	return s.seekVertical(0)
}

// SeekDownPage seeks down a full page worth of lines.
func (s *Scroll) SeekDownPage() bool {
	if s.InvertOffset {
		return s.seekVertical(max(s.offset.Y-s.height, 0))
	}
	return s.seekVertical(min(s.offset.Y+s.height, s.getMaxYOffset()))
}

// SeekUpPage seeks up a full page worth of lines.
func (s *Scroll) SeekUpPage() bool {
	if s.InvertOffset {
		return s.seekVertical(min(s.offset.Y+s.height, s.getMaxYOffset()))
	}
	return s.seekVertical(max(s.offset.Y-s.height, 0))
}

func (s *Scroll) seekTo(pos term.Coordinates, xpadding, ypadding int) bool {
	pos = s.expandCoordinatesWidth(pos)
	var yok, xok bool
	if max := s.getMaxXOffset(); xpadding > max {
		xpadding = max
	}
	if max := s.getMaxYOffset(); ypadding > max {
		ypadding = max
	}

	// ypadding < 0 is used to signal seek on the y axis with no padding
	// setting the exact position to pos.Y, rather than ensuring that pos.Y
	// is within view.
	if ypadding == -1 {
		yok = s.seekVertical(pos.Y)
	} else if pos.Y >= s.offset.Y+s.height-ypadding {
		yok = s.seekVertical(pos.Y - s.height + ypadding)
	} else if pos.Y < s.offset.Y-ypadding {
		yok = s.seekVertical(pos.Y - ypadding)
	}

	if pos.X >= s.offset.X+s.width-xpadding {
		xok = s.seekHorizontal(pos.X - s.width + xpadding)
	} else if pos.X < s.offset.X-xpadding {
		xok = s.seekHorizontal(pos.X - xpadding)
	}

	ok := yok || xok
	return ok
}

// SetOffset force-sets the underlying offset of this scroll.
// It is up to the caller to ensure that pos is within the max
// offset. Negative coordinates will trigger a panic.
func (s *Scroll) SetOffset(pos term.Coordinates) bool {
	if pos.X < 0 || pos.Y < 0 {
		panic("invalid coordinates: negative")
	}
	ok := pos != s.offset
	if ok {
		dispatch := s.dispatchSubscribers()
		defer dispatch()
		s.offset = pos
	}
	return ok
}

// ClampOffset clamps the current offset into the valid range so an offset
// left past the maximum (for example after content shrinks on resize)
// snaps back to the last valid position instead of scrolling off the end.
// It reports whether the offset changed. Unlike a Seek it does not notify
// subscribers, so callers may use it from a Resize path.
func (s *Scroll) ClampOffset() bool {
	maxOff := s.MaxOffset()
	clamped := term.Coordinates{
		X: min(s.offset.X, maxOff.X),
		Y: min(s.offset.Y, maxOff.Y),
	}
	if clamped == s.offset {
		return false
	}
	s.offset = clamped
	return true
}

// SeekNextResult shifts the contents of this scroll to visualize
// the next result in the result list.
func (s *Scroll) SeekNextResult() bool {
	pos, ok := s.NextResult()
	if !ok {
		return false
	}

	return s.seekTo(pos, len(s.searchText), -1)
}

// SeekPrevResult shifts the contents of this scroll to visualize
// the previous result in the result list.
func (s *Scroll) SeekPrevResult() bool {
	pos, ok := s.PrevResult()
	if !ok {
		return false
	}

	return s.seekTo(pos, len(s.searchText), -1)
}

// HiddenLineCount returns the total number of hidden lines.
func (s *Scroll) HiddenLineCount() int {
	return len(s.hiddenlines)
}

// MarkHidden marks the inner rows of the given row range as hidden,
// so next call to Draw will not display them, and instead display an icon to indicate
// that there are hidden rows.
func (s *Scroll) MarkHidden(start, end int) bool {
	if s.InvertOffset {
		panic("called MarkHidden but InvertOffset is enabled")
	}
	if start > end {
		tmp := end
		end = start
		start = tmp
	}
	if start == end {
		return false
	}
	if start < 0 || end >= s.buf.Rows() || s.hiddenblocks == nil {
		return false
	}

	// do not process if new fold is inside a current fold
	for bstart, bend := range s.hiddenblocks {
		if start >= bstart && start <= bend && end >= bstart && end <= bend {
			return false
		}
	}

	// remove any folds that intersect with the new fold:
	// this avoids problems calculating coordinates
	for bstart, bend := range s.hiddenblocks {
		if (bstart >= start && bstart <= end) || (bend <= end && bend >= start) {
			dispatchDid := s.dispatchVisibleToSubscribers(bstart)
			defer dispatchDid()
			delete(s.hiddenblocks, bstart)
		}
	}

	// update offset subscribers when there's a change in hidden lines
	dispatchDid := s.dispatchHiddenToSubscribers(start, end)
	defer dispatchDid()
	s.hiddenblocks[start] = end
	s.rebuildHiddenLines()

	return true
}

// HiddenBlockAt returns the hidden block starting at line, or false
// if there's no hidden block.
func (s *Scroll) HiddenBlockAt(line int) (ret term.Range, ok bool) {
	if s.hiddenblocks == nil {
		return
	}
	end, ok := s.hiddenblocks[line]
	if !ok {
		return
	}
	ret = term.Range{
		Start: term.Coordinates{Y: line},
		End:   term.Coordinates{Y: end},
	}
	return ret, ok
}

// MarkVisible reverses MarkHidden for the given row block start.
func (s *Scroll) MarkVisible(start int) bool {
	if s.hiddenblocks == nil {
		return false
	}
	_, ok := s.hiddenblocks[start]
	if ok {
		dispatchDid := s.dispatchVisibleToSubscribers(start)
		delete(s.hiddenblocks, start)
		s.rebuildHiddenLines()
		dispatchDid()
	}
	return ok
}

// Resize resizes this scroll to fit inside given width and height.
func (s *Scroll) Resize(width, height int) {
	s.width = width
	s.height = height
	// do not re-calculate offsets here as it should trigger
	// dispatching subscribers OnWillSeek/OnDidSeek, but we
	// shouldn't do that on calls to Resize.
}

// RowsWithWraps returns the number of rows in this scroll,
// including the chunks of rows that were wrapped around.
func (s *Scroll) RowsWithWraps() int {
	return s.buf.Rows() + s.wrapsLen
}

// MaxOffset returns the max seek offset.
func (s *Scroll) MaxOffset() term.Coordinates {
	return term.Coordinates{
		Y: s.getMaxYOffset(),
		X: s.getMaxXOffset(),
	}
}

func (s *Scroll) getMaxXOffset() (x int) {
	if s.Wrap {
		return
	}

	var columns int
	for i := 0; i < s.buf.Rows(); i++ {
		if c := s.viewColumns(i); c > columns {
			columns = c
		}
	}
	x = max(0, columns-s.width)
	return
}

func (s *Scroll) getMaxYOffset() (y int) {
	rows := s.RowsWithWraps()
	if s.InvertOffset {
		// hidden lines are not enabled with InvertOffset
		y = max(0, rows-s.height)
	} else {
		y = max(0, rows-s.height-s.hidden)
	}
	return
}

func (s *Scroll) hiddenOffset() (ret int) {
	if s.InvertOffset {
		return 0
	}
	for _, t := range s.hiddensorted {
		if t.start < s.offset.Y+ret {
			ret += t.end - t.start
		}
	}
	return
}

type startEndBlock struct {
	start int
	end   int
}

func (s *Scroll) drawNoAttr(writer term.Writer) {
	xwindow := s.offset.X + s.width
	rowOffset := s.convertedOffset().Y
	yoffset := -min(0, rowOffset)
	start := max(0, min(rowOffset, s.buf.Rows()))
	ywindow := max(0, min(s.height-yoffset, s.buf.Rows()-start))
	for y := range ywindow {
		r := s.buf.Row(start + y)
		var xoffset int
		for x, c := range r {
			if c.Ch == '\t' {
				xoffset += s.tabspaces - 1
			}
			x += xoffset
			if c.Width > 1 {
				xoffset += int(c.Width) - 1
			}
			if x >= xwindow {
				break
			}
			if x < s.offset.X {
				continue
			}
			xi := x - s.offset.X
			writer.SetCell(term.Coordinates{X: xi, Y: y + yoffset}, c)
		}
	}
}

func (s *Scroll) draw(writer term.Writer) {
	xwindow := s.offset.X + s.width
	rowOffset := s.convertedOffset().Y
	yoffset := -min(0, rowOffset)
	start := max(0, min(rowOffset, s.buf.Rows()))
	ywindow := max(0, min(s.height-yoffset, s.buf.Rows()-start))
	for y := range ywindow {
		r := s.buf.Row(start + y)
		var xoffset int
		for x, c := range r {
			if c.Ch == '\t' {
				xoffset += s.tabspaces - 1
			}
			x += xoffset
			if c.Width > 1 {
				xoffset += int(c.Width) - 1
			}
			if x >= xwindow {
				break
			}
			if x < s.offset.X {
				continue
			}
			xi := x - s.offset.X
			if c.Bg == 0 {
				c.Bg = s.Attributes.Bg
			}
			if c.Fg == 0 {
				c.Fg = s.Attributes.Fg
			}
			writer.SetCell(term.Coordinates{X: xi, Y: y + yoffset}, c)
		}
	}
}

func (s *Scroll) wrapdrawNoAttr(writer term.Writer) {
	s.wraps, s.wrapsLen = doWrapdrawNoAttr(s.tabspaces, writer, s.buf, s.width, s.height, s.offset)
}

func doWrapdrawNoAttr(
	tabspaces int, writer term.Writer, buf *cell.Buffer,
	width, height int, offset term.Coordinates,
) (wraps []int, totalWrapCount int) {
	xwindow := width
	ywindow := height
	wraps = make([]int, 0)
	totalWrapCount = 0
	for y, r := range buf.RawCells() {
		var xoffset int
		var lineWrapCount int
		// cannot skip any row until all wraps are accounted for
		for x, c := range r {
			if c.Ch == '\t' {
				xoffset += tabspaces - 1
			}
			xi := x + xoffset
			if c.Width > 1 {
				xoffset += int(c.Width) - 1
			}
			if xi >= xwindow {
				charWrapCount := xi / xwindow
				xi = xi % xwindow
				if charWrapCount > lineWrapCount {
					lineWrapCount++
					totalWrapCount++
				}
			}
			yi := y + totalWrapCount - offset.Y
			if yi >= 0 && yi < ywindow {
				writer.SetCell(term.Coordinates{X: xi, Y: yi}, c)
			}
		}
		wraps = append(wraps, lineWrapCount)
	}
	return
}

func (s *Scroll) wrapdraw(writer term.Writer) {
	xwindow := s.width
	ywindow := s.height
	s.wraps = make([]int, 0)
	s.wrapsLen = 0
	for y, r := range s.buf.RawCells() {
		var lineWrapCount int
		var xoffset int
		// cannot skip any row until wraps are accounted for
		for x, c := range r {
			if c.Ch == '\t' {
				xoffset += s.tabspaces - 1
			}
			xi := x + xoffset
			if c.Width > 1 {
				xoffset += int(c.Width) - 1
			}
			if xi >= xwindow {
				charWrapCount := xi / xwindow
				xi = xi % xwindow
				if charWrapCount > lineWrapCount {
					lineWrapCount++
					s.wrapsLen++
				}
			}
			yi := y + s.wrapsLen - s.offset.Y
			if yi >= 0 && yi < ywindow {
				if c.Bg == 0 {
					c.Bg = s.Attributes.Bg
				}
				if c.Fg == 0 {
					c.Fg = s.Attributes.Fg
				}
				writer.SetCell(term.Coordinates{X: xi, Y: yi}, c)
			}
		}
		s.wraps = append(s.wraps, lineWrapCount)
	}
}

// RecalculateWraps can be used to signal Scroll that buffer has been updated
// and so calculated wrap properties might be incorrect.
func (s *Scroll) RecalculateWraps() {
	if s.Wrap {
		s.Draw(term.NoopWriter{})
	}
}

// Draw draws the contents of this scroll to the given writer. If Wrap is set,
// rows that are too long wrap around and thus are rendered in the next row.
func (s *Scroll) Draw(writer term.Writer) {
	if s.width <= 0 || s.height <= 0 {
		return
	}
	s.drawContent(writer)
	if s.Hatch != nil {
		s.Hatch.Draw(writer, image.Rect(0, s.EndRow(), s.width, s.height))
	}
	s.drawSearchResults(writer)
}

func (s *Scroll) drawContent(writer term.Writer) {
	if s.Attributes == (term.Attributes{}) {
		if s.Wrap {
			s.wrapdrawNoAttr(writer)
			return
		}

		if len(s.hiddenblocks) != 0 {
			s.drawWithHidden(writer)
			return
		}

		s.drawNoAttr(writer)
		return
	}

	// setcell background
	for y := 0; y < s.height; y++ {
		for x := 0; x < s.width; x++ {
			writer.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell(0, 0, s.Attributes))
		}
	}

	if s.Wrap {
		s.wrapdraw(writer)
		return
	}

	if len(s.hiddenblocks) != 0 {
		s.drawWithHidden(writer)
		return
	}

	s.draw(writer)
}

// EndRow returns the row below the last one the content is drawn on, from
// 0 when the content is scrolled out of view to the height of the scroll
// when it reaches the bottom. With Wrap set, it counts the rows that the
// last Draw wrapped.
func (s *Scroll) EndRow() int {
	var end int
	if s.Wrap {
		end = s.RowsWithWraps() - s.offset.Y
	} else {
		// A hidden block is drawn on a single row.
		end = s.buf.Rows() - s.hidden - s.convertedOffset().Y
	}
	return max(0, min(end, s.height))
}

// WordAt returns the word at the given position or an empty string if token
// at the given position is not a word. See TokenAt for more details.
// The returned start and end coordinates follow left inclusive, right exclusive semantics,
// so they're ready to be used for selection.
func (s *Scroll) WordAt(pos term.Coordinates) (
	term.Coordinates, term.Coordinates, string,
) {
	if pos.Y < 0 || pos.Y >= s.buf.Rows() {
		return pos, pos, ""
	}
	return s.buf.TokenAt(pos, wordMatcher)
}

// Search performs a text search of text in the internal cell buffer. It populates
// a search list so SeekNextResult and SeekPreviousResult can be used to visualize results.
// It returns the number of matches found. Note that it also sets the attributes of
// the matching terms as defined by ResultsAttr.
func (s *Scroll) Search(text string) (n int) {
	s.searchText = []rune(text)
	s.searchTextStr = text
	return s.search()
}

// PrevResult returns the coordinates of the previous result in the Search list.
func (s *Scroll) PrevResult() (pos term.Coordinates, ok bool) {
	return s.searcher.PrevResult()
}

// NextResult returns the coordinates of the next result in the Search list.
func (s *Scroll) NextResult() (pos term.Coordinates, ok bool) {
	return s.searcher.NextResult()
}

// Result returns the current search result's coordinates.
func (s *Scroll) Result() (pos term.Coordinates, ok bool) {
	return s.searcher.Result()
}

// Offset returns the scroll offset from the start of the content.
func (s *Scroll) Offset() term.Coordinates {
	return s.offset
}

// Width returns this scroll's width.
func (s *Scroll) Width() int {
	return s.width
}

// SizeHeight returns this scroll's height as prescribed
// by the last call to Resize. It's named
// SizeHeight to differentiate from Height, which is used
// to satisfy component.Responsive.
func (s *Scroll) SizeHeight() int {
	return s.height
}

// Height satisfies component.Responsive.
func (s *Scroll) Height(width int) int {
	rows := s.buf.Rows()
	if !s.Wrap {
		return rows
	}
	if width == 0 {
		return 0
	}
	// NOTE: Height shouldn't rely on any internal mutable state
	// except for the buffer.
	_, wrapsLen := doWrapdrawNoAttr(s.tabspaces, term.NoopWriter{}, s.buf,
		width, 0, term.Coordinates{})
	return rows + wrapsLen
}

// Wraps returns all the wraps detected in the last call to Draw.
//
// If Draw has not been called yet, then this method returns an
// empty map.
func (s *Scroll) Wraps() []int {
	return s.wraps
}

// Buffer returns s internal Buffer.
func (s *Scroll) Buffer() *cell.Buffer {
	return s.buf
}

// ScrollSubscriber is a subscriber of seek operations in a scroll.
type ScrollSubscriber interface {
	// OnWillSeek is dispatched before a scroll is about to change its offset.
	OnWillSeek(from term.Coordinates)
	// OnDidSeek is dispatched after a scroll has changed its offset.
	OnDidSeek(from, to term.Coordinates)

	// OnWillHide is dispatched before a scroll hides a block of lines.
	OnWillHide(start, end int)
	// OnWillVisible is dispatched before a scroll makes visible a block of lines,
	// that was previously hidden.
	OnWillVisible(start int)

	// OnDidHide is dispatched when a scroll has hidden a block of lines.
	OnDidHide(start, end int)
	// OnVisible is dispatched when a scroll has made visible a block of lines,
	// that was previously hidden.
	OnDidVisible(start int)
}

type fnSubscriber func(term.Coordinates, term.Coordinates)

func (s fnSubscriber) OnWillSeek(from term.Coordinates) {
	/* no-op */
}

func (s fnSubscriber) OnDidHide(start, end int) {
	/* no-op */
}

func (s fnSubscriber) OnDidVisible(start int) {
	/* no-op */
}

func (s fnSubscriber) OnWillHide(start, end int) {
	/* no-op */
}

func (s fnSubscriber) OnWillVisible(start int) {
	/* no-op */
}

func (s fnSubscriber) OnDidSeek(from, to term.Coordinates) {
	s(from, to)
}

func (s *Scroll) dispatchSubscribers() func() {
	if s.disablePublishing {
		return func() {}
	}
	from := s.offset
	for _, sub := range s.subs {
		sub.OnWillSeek(from)
	}
	return func() {
		if s.disablePublishing {
			return
		}

		enable := s.DisablePublishing()
		defer enable()

		to := s.offset
		for _, sub := range s.subs {
			sub.OnDidSeek(from, to)
		}
		s.lastPublishedOffset = to
	}
}

func (s *Scroll) dispatchVisibleToSubscribers(start int) func() {
	if s.disablePublishing {
		return func() {}
	}
	for _, sub := range s.subs {
		sub.OnWillVisible(start)
	}

	return func() {
		for _, sub := range s.subs {
			sub.OnDidVisible(start)
		}
	}
}

func (s *Scroll) dispatchHiddenToSubscribers(start, end int) func() {
	if s.disablePublishing {
		return func() {}
	}
	for _, sub := range s.subs {
		sub.OnWillHide(start, end)
	}

	return func() {
		for _, sub := range s.subs {
			sub.OnDidHide(start, end)
		}
	}
}

// Subscribe subscribes sub to seek operations.
func (s *Scroll) Subscribe(sub ScrollSubscriber) {
	s.subs = append(s.subs, sub)
}

// DisablePublishing disables dispatching OnDidSeek/OnWillSeek calls to subscribers.
// This is useful when clients of Scroll perform composite moves that
// would otherwise dispatch multiple calls rather than one.
// The returned function can be called to re-enable publishing.
//
// This method can be called multiple times and only the first time
// will disable, and only the first returned enable will re-enable
// publishing.
//
// The returned function, dispatches an OnWillSeek/OnDidSeek call pair to
// each subscriber if the offset has changed since last time publishing was disabled.
// Note that OnWillSeek in this case will be dispatched after the offset is changed
// so callers must ensure that any state that needs capturing is captured
// before using DisablePublishing/EnablePublishing.
func (s *Scroll) DisablePublishing() (enable func()) {
	if s.disablePublishing {
		return func() {}
	}
	s.disablePublishing = true
	return s.enablePublishing
}

// enablePublishing enables dispatching OnWillSeek/OnDidSeek calls
// after a call to DisablePublishing. It dispatches an OnWillSeek/OnDidSeek call pair to
// each subscriber if the offset has changed since last time publishing was disabled.
// Note that OnWillSeek in this case will be dispatched after the offset is changed
// so callers must ensure that any state that needs capturing is captured
// before using DisablePublishing/EnablePublishing.
func (s *Scroll) enablePublishing() {
	s.disablePublishing = false
	if s.lastPublishedOffset != s.Offset() {
		dispatch := s.dispatchSubscribers()
		dispatch()
	}
}

// PublishingEnabled returns whether publishing has been
// enabled with EnablePublishing, or disabled with DisablePublishing. By default
// it is enabled when Scroll is initialized.
func (s *Scroll) PublishingEnabled() bool {
	return !s.disablePublishing
}

// FuncScrollSubscriber wraps fn to satisfy ScrollSubscriber, which is called
// every time the offset changes. Changes to line visibility will be ignored.
func FuncScrollSubscriber(fn func(from, to term.Coordinates)) ScrollSubscriber {
	return fnSubscriber(fn)
}

// ScrollToWindowCoordinates translates scroll content Coordinates to window Coordinates,
// taking into consideration scroll offsets and wrapped rows. The second boolean return
// value is used to indicate that the given position is inside a hidden block (false), or
// not hidden (true). A valid set of coordinates is returned in either case, but when the
// coordinates would fall inside a hidden block, the start of the hidden block is returned.
func (s *Scroll) ScrollToWindowCoordinates(pos term.Coordinates) (term.Coordinates, bool) {
	offset := s.convertedOffset()
	pos = s.expandCoordinatesWidth(pos)
	ret := term.CoordinatesDiff(pos, offset)
	if !s.Wrap {
		if len(s.hiddensorted) != 0 {
			for _, block := range s.hiddensorted {
				if block.start > pos.Y {
					break
				}
				if pos.Y > block.start {
					// min in case pos is inside block
					ret.Y -= min(block.end, pos.Y) - block.start
					if pos.Y <= block.end {
						ret.X = 0
						return ret, false
					}
				}
			}
		}
		return ret, true
	}
	if s.Width() == 0 {
		return term.Coordinates{}, false
	}
	wraps := s.Wraps()
	for y, count := range wraps {
		if y >= pos.Y {
			break
		}
		ret.Y += count
	}
	// resolve amgiuity of converting positions past the last column by
	// assuming that the intent is to reference the same line, with an offset
	// over the last cell in the row, rather than the next row.
	var maxx int
	if pos.Y < s.buf.Rows() {
		maxx = max(0, min(pos.X, s.buf.Columns(pos.Y)-1))
	}
	var pastmax int
	if pos.X > maxx {
		pastmax = pos.X - maxx
		pos.X = maxx
	}
	diff := pos.X / s.Width()
	ret.X = pos.X%s.Width() - offset.X + pastmax
	ret.Y += diff
	return ret, true
}

// WindowToScrollCoordinates translates window Coordinates to scroll content Coordinates,
// taking into consideration scroll offsets and wrapped rows.
func (s *Scroll) WindowToScrollCoordinates(pos term.Coordinates) term.Coordinates {
	offset := s.convertedOffset()
	ret := term.CoordinatesSum(pos, offset)
	if !s.Wrap {
		if len(s.hiddensorted) != 0 {
			for _, block := range s.hiddensorted {
				if block.start > ret.Y {
					break
				}
				if ret.Y > block.start {
					ret.Y += block.end - block.start
				}
			}
		}
		return s.contractCoordinatesWidth(ret)
	}
	wraps := s.Wraps()
	var sum, count int
	for _, count = range wraps {
		if pos.Y+offset.Y >= sum+count+1 {
			ret.Y -= count
			sum += count + 1
			continue
		}
		diff := pos.Y + offset.Y - sum
		ret.X = pos.X + diff*s.Width() + offset.X
		ret.Y -= diff
		break
	}
	return s.contractCoordinatesWidth(ret)
}

func wordMatcher(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func (s *Scroll) drawSearchResults(w term.Writer) {
	slen := len(s.searchText)
	for _, posAtScroll := range s.matches {
		posAtScreen, ok := s.ScrollToWindowCoordinates(posAtScroll)
		if !ok || posAtScreen.Y >= s.height || posAtScreen.Y < 0 {
			continue
		}

		toX := posAtScroll.X + slen
		for x := posAtScroll.X; x < toX; x++ {
			posAtScreen, ok := s.ScrollToWindowCoordinates(term.Coordinates{Y: posAtScroll.Y, X: x})
			if !ok || posAtScreen.X >= s.width || posAtScreen.X < 0 {
				continue
			}
			w.UnionAttributes(posAtScreen, s.ResultsAttr)
		}
	}
}

func (s *Scroll) search() (n int) {
	s.matches = s.matches[:0]

	n = s.searcher.Search(s.searchTextStr)
	for i := 0; i < n; i++ {
		pos, ok := s.searcher.NextResult()
		if !ok {
			panic("searcher return n results but no enough results available")
		}
		s.matches = append(s.matches, pos)
	}
	return
}

type scrollSubscriber Scroll

func (s *scrollSubscriber) OnWillEdit(
	ctx context.Context, from, to term.Coordinates, str string,
) {
	s.onWillEditFrom = from
	s.onWillEditTo = to
}

func (s *scrollSubscriber) OnDidEdit(
	ctx context.Context, start, end term.Coordinates, str string,
) {
	// if any match doesn't match, then re-issue search
	for _, match := range s.matches {
		for i := 0; i < len(s.searchText); i++ {
			cc, ok := s.buf.Cell(match)
			if !ok || cc.Ch != s.searchText[i] {
				(*Scroll)(s).search()
				return
			}
			match.X++
		}
	}

	// if lines above hidden lines were either added or removed
	// (or both) then update hidden locations
	removed := s.onWillEditTo.Y - s.onWillEditFrom.Y
	added := end.Y - start.Y
	clear(s.hiddenblocks)
	for _, hidden := range s.hiddensorted {
		// clear blocks that are partially deleted or fully deleted
		clear := (s.onWillEditFrom.Y <= hidden.end && s.onWillEditFrom.Y >= hidden.start) ||
			(s.onWillEditTo.Y >= hidden.start && s.onWillEditTo.Y <= hidden.end) ||
			(s.onWillEditFrom.Y < hidden.start && s.onWillEditTo.Y > hidden.end)
		if clear {
			// NOTE: this might break subscribers if multiple blocks
			// are cleared in one delete, and we call multiple OnWillVisible
			// sequentially rathern than alternating with OnDidVisible.
			dispatchDid := (*Scroll)(s).dispatchVisibleToSubscribers(hidden.start)
			defer dispatchDid()
			continue
		}
		var delta int
		if s.onWillEditFrom.Y < hidden.start && s.onWillEditTo.Y < hidden.start {
			delta -= removed
		}
		if start.Y < hidden.start {
			delta += added
		}
		s.hiddenblocks[hidden.start+delta] = hidden.end + delta
	}

	(*Scroll)(s).rebuildHiddenLines()
}

func (s *Scroll) viewColumns(y int) (ret int) {
	for _, c := range s.Buffer().Row(y) {
		if c.Ch == '\t' {
			ret += s.tabspaces
		} else {
			ret += int(c.Width)
		}
	}
	return
}

func (s *Scroll) drawWithHidden(writer term.Writer) {
	xwindow := s.offset.X + s.width
	ywindow := s.height
	var targety, hideLineIconOffset int
	endblock := -1
	hiddenOffset := s.hiddenOffset()
	yoffset := s.convertedOffset().Y + hiddenOffset
	targety = -min(0, yoffset)
	yoffset = max(0, yoffset)
	for y := 0; y+yoffset < s.buf.Rows(); y++ {
		if targety >= ywindow {
			break
		}
		r := s.buf.Row(y + yoffset)
		scrollY := y + yoffset
		if scrollY < endblock {
			continue
		}
		if scrollY == endblock {
			trim := true
			targetx := hideLineIconOffset
			for _, c := range r {
				if targetx >= s.width {
					break
				}
				if targetx < 0 || (trim && (c.Ch == ' ' || c.Ch == '\t')) {
					continue
				}
				if c.Bg == 0 {
					c.Bg = s.Attributes.Bg
				}
				if c.Fg == 0 {
					c.Fg = s.Attributes.Fg
				}
				trim = false
				writer.SetCell(term.Coordinates{X: targetx, Y: targety}, c)
				targetx++
			}
			targety++
			continue
		}
		// the first hidden line in hiddenlines
		end, ok := s.hiddenlines[scrollY]
		_, isBlockStart := s.hiddenblocks[scrollY]
		// we're partially scrolled over a block
		if ok && !isBlockStart {
			y = end + s.offset.Y + 1 + hiddenOffset
			continue
		}
		var xoffset int
		for x, c := range r {
			if c.Ch == '\t' {
				xoffset += s.tabspaces - 1
			}
			x += xoffset
			if c.Width > 1 {
				xoffset += int(c.Width) - 1
			}
			if x >= xwindow {
				break
			}
			if x < s.offset.X {
				continue
			}
			xi := x - s.offset.X
			if c.Bg == 0 {
				c.Bg = s.Attributes.Bg
			}
			if c.Fg == 0 {
				c.Fg = s.Attributes.Fg
			}
			writer.SetCell(term.Coordinates{X: xi, Y: targety}, c)
		}
		if ok {
			endblock = end
			// save allocations during Draw by pre-computing this, which doesn't change
			// unless hidden blocks are altered.
			hideLineStr := s.hiddenmeta[scrollY]
			hideLineIconOffset = s.viewColumns(scrollY) - s.offset.X - 1
			if hideLineIconOffset >= s.width {
				continue
			}
			hideLineIconOffset++
			for _, r := range hideLineStr {
				if hideLineIconOffset < 0 {
					hideLineIconOffset++
					continue
				}
				if hideLineIconOffset == s.width {
					break
				}
				cell := term.NewCell(r, 1, s.HideAttr)
				writer.SetCell(term.Coordinates{X: hideLineIconOffset, Y: targety}, cell)
				hideLineIconOffset++
			}
			continue
		}
		targety++
	}
}

func (s *Scroll) rebuildHiddenLines() {
	clear(s.hiddenlines)
	clear(s.hiddenmeta)
	s.hidden = 0
	s.hiddensorted = s.hiddensorted[:0]
	for start, end := range s.hiddenblocks {
		for i := start; i <= end; i++ {
			s.hiddenlines[i] = end
		}
		s.hiddensorted = append(s.hiddensorted, startEndBlock{start, end})
		s.hiddenmeta[start] = fmt.Sprintf(" [%d lines] ", end-start+1)
		s.hidden += end - start
	}
	sort.Slice(s.hiddensorted, func(i, j int) bool {
		return s.hiddensorted[i].start < s.hiddensorted[j].start
	})
}

func (s *Scroll) repositionLine(target, y int) (seek int) {
	if s.height == 0 || s.width == 0 {
		return
	}
	// ignore bool return value; we don't care if it's inside a hidden block
	pos, _ := s.ScrollToWindowCoordinates(term.Coordinates{Y: y})
	y = pos.Y
	for y > target && s.SeekDown() {
		y--
		seek++
	}
	for y < target && s.SeekUp() {
		y++
		seek--
	}
	return
}

func (s *Scroll) expandCoordinatesWidth(scrollPos term.Coordinates) (ret term.Coordinates) {
	ret = scrollPos
	if scrollPos.Y < 0 || scrollPos.Y >= s.buf.Rows() {
		return
	}
	row := s.buf.Row(scrollPos.Y)
	posx := max(0, min(scrollPos.X, len(row)))
	for _, c := range row[:posx] {
		if c.Ch == '\t' {
			ret.X += s.tabspaces - 1
		} else if c.Width > 1 {
			ret.X += int(c.Width) - 1
		}
	}
	if scrollPos.X < len(row) && row[scrollPos.X].Ch == '\t' {
		ret.X += s.tabspaces - 1
	}

	return
}

func (s *Scroll) contractCoordinatesWidth(pos term.Coordinates) (ret term.Coordinates) {
	ret = pos
	if pos.Y < 0 || pos.Y >= s.buf.Rows() {
		return
	}
	// Indexed rather than ranged: this runs once per cursor conversion,
	// i.e. several times per parsed codepoint, and copying each 24-byte
	// cell dominated the loop.
	row := s.buf.Row(pos.Y)
	for x := 0; x < len(row) && x < ret.X; x++ {
		if row[x].Ch == '\t' {
			ret.X -= s.tabspaces - 1
		} else if row[x].Width > 1 {
			ret.X -= int(row[x].Width) - 1
		}
	}
	if ret.X < 0 {
		ret.X = 0
	}
	return
}

// always returns a from the top offset. Some conversions
// might yield negative results, and that's ok, it simply
// means that the current scroll couldn't be drawn with
// InvertOffset turned off, but it still can be used to translate
// coordinates.
func (s *Scroll) convertedOffset() term.Coordinates {
	if !s.InvertOffset {
		return s.offset
	}
	return term.Coordinates{
		Y: s.buf.Rows() - s.height - s.offset.Y,
		X: s.offset.X,
	}
}
