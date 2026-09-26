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

package gui

import (
	"bytes"
	"net/url"
	"unicode"
	"unicode/utf8"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

// linkSpan locates a URL rendered as plain text in the composited frame.
// x1 is exclusive.
type linkSpan struct {
	y, x0, x1 int
	url       string
}

// linkSchemes are the schemes recognized in rendered text.
var (
	linkSchemes     = []string{"https://", "http://", "file://"}
	schemeSeparator = []byte("://")
)

// rowScanner matches URLs row by row. It keeps its scratch buffer
// between scans because a scan runs on every frame that redraws while
// the meta modifier is held.
type rowScanner struct {
	text []byte
	// wrapped accumulates a link across the rows the terminal broke it
	// onto.
	wrapped []byte
}

// scan appends the spans found in cells to dst and returns it.
func (s *rowScanner) scan(cells [][]term.Cell, dst []linkSpan) []linkSpan {
	for y := 0; y < len(cells); y++ {
		before := len(dst)
		dst = s.scanRow(y, cells[y], dst)
		if len(dst) == before {
			continue
		}
		if !wrapMarked(cells[y], dst[len(dst)-1].x1) {
			continue
		}
		dst, y = s.joinWrapped(cells, y, len(dst)-1, dst)
	}
	return dst
}

// wrapMarked reports whether the cell a link ends on records that its
// line overflowed and continues on the next row. The frame covers the
// whole window, so a wrapped line ends at its own pane's right margin
// rather than the row's, and only the marker says where that margin is.
func wrapMarked(row []term.Cell, x1 int) bool {
	return x1 > 0 && x1 <= len(row) && row[x1-1].Bytes == cell.WrapMarker
}

// paneStart walks left to the first cell the pane did not write.
// Whatever drew the row filled its own pane and nothing beyond it, so
// the written run bounds the pane the continuation resumes in.
func paneStart(row []term.Cell, x int) int {
	for x > 0 && row[x-1].Ch != 0 && !isFrameRune(row[x-1].Ch) {
		x--
	}
	return x
}

// lineEnd returns the index one past the last cell a line can reach from
// x. Any rune beyond ASCII counts, so that an internationalized host
// survives, which also means the run would otherwise swallow the border
// or scroll bar the window drew hard against the pane. The wrap marker
// is the one thing that names the pane's right margin, so a line that
// reaches it stops there.
func lineEnd(row []term.Cell, x int) int {
	// Ch == 0 is both a blank cell and the continuation half of a wide
	// character, so it ends the line either way.
	for x < len(row) && row[x].Ch != 0 && isURLRune(row[x].Ch) {
		if row[x].Bytes == cell.WrapMarker {
			return x + 1
		}
		x++
	}
	return x
}

// joinWrapped continues the link at dst[first] onto the rows it wrapped
// onto and rewrites every span it covers with the whole address.
func (s *rowScanner) joinWrapped(
	cells [][]term.Cell, y, first int, dst []linkSpan,
) ([]linkSpan, int) {
	left := paneStart(cells[y], dst[first].x1-1)
	s.wrapped = append(s.wrapped[:0], dst[first].url...)
	last := first
	for y+1 < len(cells) {
		row := cells[y+1]
		if left >= len(row) {
			break
		}
		end := lineEnd(row, left)
		if end == left {
			break
		}
		for i := left; i < end; i++ {
			s.wrapped = appendCluster(s.wrapped, &row[i])
		}
		y++
		dst = append(dst, linkSpan{y: y, x0: left, x1: end})
		last = len(dst) - 1
		if !wrapMarked(row, end) {
			break
		}
	}
	trimmed := trimURL(s.wrapped)
	if !hasHost(trimmed) {
		// The run reached the margin on nothing but its scheme and the
		// rows below it did not carry a host after all.
		return dst[:first], y
	}
	// Trimming drops nothing but ASCII punctuation, which never
	// continues a grapheme cluster and so always had a cell to itself.
	dst[last].x1 -= len(s.wrapped) - len(trimmed)
	for last > first && dst[last].x1 <= dst[last].x0 {
		dst = dst[:last]
		last--
	}
	joined := string(trimmed)
	for i := first; i <= last; i++ {
		dst[i].url = joined
	}
	return dst, y
}

// scanRow matches against the cells themselves rather than a flattened
// copy of the row, so prose never reaches the scratch buffer: it is only
// touched once a scheme is found.
func (s *rowScanner) scanRow(y int, row []term.Cell, dst []linkSpan) []linkSpan {
	// A run that ends on the wrap marker is half an address, so it is
	// appended raw and left for joinWrapped to finish. Only the last
	// span of a row can still be waiting: anything matched after it
	// proves the join will never reach it.
	pending := -1
	for x := 0; x < len(row); {
		found := schemeStart(row[x:])
		if found < 0 {
			break
		}
		x += found

		width, ok := schemeAt(row, x)
		if !ok {
			x++
			continue
		}

		end := lineEnd(row, x+width)

		s.text = s.text[:0]
		for i := x; i < end; i++ {
			s.text = appendCluster(s.text, &row[i])
		}

		if wrapMarked(row, end) {
			dst = s.settle(row, dst, pending)
			dst = append(dst, linkSpan{y: y, x0: x, x1: end, url: string(s.text)})
			pending = len(dst) - 1
			x = end
			continue
		}
		trimmed := trimURL(s.text)
		if hasHost(trimmed) {
			dst = s.settle(row, dst, pending)
			pending = -1
			// Trimming only ever drops ASCII punctuation, which never
			// continues a cluster and so always had a cell to itself.
			dst = append(dst, linkSpan{
				y:   y,
				x0:  x,
				x1:  end - (len(s.text) - len(trimmed)),
				url: string(trimmed),
			})
		}
		x = end
	}
	return dst
}

// settle finishes a span left raw for a join that the rest of the row
// has just ruled out, reading its address back from the cells because
// the scratch buffer has moved on to the next match.
func (s *rowScanner) settle(row []term.Cell, dst []linkSpan, i int) []linkSpan {
	if i < 0 {
		return dst
	}
	s.wrapped = s.wrapped[:0]
	for x := dst[i].x0; x < dst[i].x1; x++ {
		s.wrapped = appendCluster(s.wrapped, &row[x])
	}
	trimmed := trimURL(s.wrapped)
	if !hasHost(trimmed) {
		return dst[:i]
	}
	dst[i].x1 -= len(s.wrapped) - len(trimmed)
	dst[i].url = string(trimmed)
	return dst
}

// schemeStart returns the offset of the first cell that could begin a
// scheme, or -1. Prose is the overwhelming majority of a frame, so
// rejecting it is the loop worth tuning. A cell is 24 bytes, so an
// indexed load has to rescale the index on every step. Taking a
// fixed-length window amortizes that into one address the compiler
// reaches the rest of the window from by immediate offset, and its
// constant bounds are what let the checks fall away.
func schemeStart(row []term.Cell) int {
	i := 0
	for ; i+8 <= len(row); i += 8 {
		win := row[i : i+8 : i+8]
		if schemeLead(win[0].Ch) || schemeLead(win[1].Ch) ||
			schemeLead(win[2].Ch) || schemeLead(win[3].Ch) ||
			schemeLead(win[4].Ch) || schemeLead(win[5].Ch) ||
			schemeLead(win[6].Ch) || schemeLead(win[7].Ch) {
			break
		}
	}
	for ; i < len(row); i++ {
		if schemeLead(row[i].Ch) {
			return i
		}
	}
	return -1
}

// schemeLead reports whether a cell can begin one of linkSchemes. The
// subtraction maps 'f' and 'h' — and nothing else — onto the two values
// the mask clears, so the test costs one branch instead of two.
func schemeLead(ch rune) bool {
	return (uint32(ch)-'f')&^2 == 0
}

// appendCluster writes the whole grapheme cluster a cell holds. A cell
// keeps the leading code point in Ch and the rest of the cluster in
// Combining, so writing Ch alone would not merely shorten the address
// but change it: a host rendered café would open as cafe.
func appendCluster(dst []byte, c *term.Cell) []byte {
	// Unsigned, so a negative rune takes the slow path that replaces it
	// rather than being truncated to a byte.
	if uint32(c.Ch) < utf8.RuneSelf && c.Extra == nil {
		return append(dst, byte(c.Ch))
	}
	return appendWideCluster(dst, c)
}

// appendWideCluster is split out of appendCluster so that the ASCII cell
// a URL is almost entirely made of inlines at the call site.
func appendWideCluster(dst []byte, c *term.Cell) []byte {
	dst = utf8.AppendRune(dst, c.Ch)
	for _, r := range c.CombiningRunes() {
		dst = utf8.AppendRune(dst, r)
	}
	return dst
}

// schemeAt reports how many cells the scheme starting at x occupies.
func schemeAt(row []term.Cell, x int) (int, bool) {
	for _, scheme := range linkSchemes {
		if len(row)-x < len(scheme) {
			continue
		}
		if matchesScheme(row[x:x+len(scheme)], scheme) {
			return len(scheme), true
		}
	}
	return 0, false
}

func matchesScheme(cells []term.Cell, scheme string) bool {
	for i := range cells {
		if cells[i].Ch != rune(scheme[i]) {
			return false
		}
	}
	return true
}

// isURLRune reports whether r can appear in a URL rendered in prose.
// Anything beyond ASCII is kept, so an internationalized host survives,
// bar the glyphs the window draws its own chrome with.
func isURLRune(r rune) bool {
	if r > unicode.MaxASCII {
		return !isFrameRune(r)
	}
	return isURLByte(byte(r))
}

// isFrameRune reports whether r is one of the box drawing or block
// element glyphs that frame a pane, split two of them, or draw a scroll
// bar. The frame covers the whole window, so this chrome is rendered
// hard against the text it surrounds, and no address contains it.
func isFrameRune(r rune) bool {
	return r >= '\u2500' && r <= '\u259f'
}

// isURLByte reports whether b can appear in a URL rendered in prose.
// Delimiters that terminate a URL in running text are excluded.
func isURLByte(b byte) bool {
	if b <= ' ' || b == 0x7f {
		return false
	}
	switch b {
	case '"', '\'', '`', '<', '>', '\\', '|', '^':
		return false
	}
	return true
}

// trimURL strips trailing characters that belong to the surrounding
// prose rather than the URL: sentence punctuation and closing brackets
// with no opener inside the URL.
func trimURL(raw []byte) []byte {
	for len(raw) > 0 {
		last := raw[len(raw)-1]
		switch last {
		case '.', ',', ';', ':', '!', '?':
		case ')', ']', '}':
			if balanced(raw, openerFor(last), last) {
				return raw
			}
		default:
			return raw
		}
		raw = raw[:len(raw)-1]
	}
	return raw
}

func openerFor(closer byte) byte {
	switch closer {
	case ')':
		return '('
	case ']':
		return '['
	default:
		return '{'
	}
}

func balanced(s []byte, opener, closer byte) bool {
	return bytes.Count(s, []byte{opener}) >= bytes.Count(s, []byte{closer})
}

// hasHost reports whether the URL carries anything after its scheme.
func hasHost(u []byte) bool {
	i := bytes.Index(u, schemeSeparator)
	return i >= 0 && len(u) > i+3
}

// linkScanner gives every URL rendered as plain text a link affordance:
// it underlines the spans while the link modifier (Cmd on macOS, Ctrl
// elsewhere) is held and turns a modified click on one into an observer
// call. It works off the composited frame, so it is agnostic to which
// handler drew the text.
type linkScanner struct {
	observer func(*url.URL)
	// setShape is the window's cursor-shape setter, replaced in tests.
	setShape func(ebiten.CursorShapeType)
	scanner  rowScanner
	spans    []linkSpan
	valid    bool
	meta     bool
	// clicking records that a meta-click already opened a link, so the
	// rest of that gesture is swallowed too.
	clicking bool
	// pointing records that the window is currently showing the link
	// cursor, so the shape is only pushed on a transition.
	pointing bool
	// rows is the underlined view of the frame; only rows carrying a
	// span are copied into buf, the rest alias the source rows.
	rows [][]term.Cell
	buf  [][]term.Cell
}

func newLinkScanner() linkScanner {
	return linkScanner{
		observer: func(*url.URL) {},
		setShape: ebiten.SetCursorShape,
	}
}

// invalidate drops the cached spans, which the next scan recomputes.
func (l *linkScanner) invalidate() {
	l.valid = false
}

// setMeta records the modifier state and reports whether it changed, so
// the caller can repaint on the modifier alone.
func (l *linkScanner) setMeta(held bool) bool {
	if held == l.meta {
		return false
	}
	l.meta = held
	l.valid = false
	if !held {
		l.point(false)
	}
	return true
}

// pointAt offers the link cursor while the mouse rests on a link. The
// GUI is the only owner of the window's cursor shape, so nothing else
// has to arbitrate with it.
func (l *linkScanner) pointAt(pos term.Coordinates, cells [][]term.Cell) {
	l.scan(cells)
	_, over := l.at(pos.X, pos.Y)
	l.point(over)
}

func (l *linkScanner) point(over bool) {
	if over == l.pointing {
		return
	}
	l.pointing = over
	if over {
		l.setShape(ebiten.CursorShapePointer)
		return
	}
	l.setShape(ebiten.CursorShapeDefault)
}

// armed reports whether links are being offered, which is only while the
// meta modifier is held.
func (l *linkScanner) armed() bool {
	return l.meta
}

// scan rescans cells when the cached spans are stale.
func (l *linkScanner) scan(cells [][]term.Cell) {
	if l.valid {
		return
	}
	l.spans = l.scanner.scan(cells, l.spans[:0])
	l.valid = true
}

// overlay returns a view of cells with every link underlined, or cells
// itself when the frame holds no link. The source rows are never
// mutated: handlers only rewrite damaged cells, so an in-place underline
// would survive the meta release.
func (l *linkScanner) overlay(cells [][]term.Cell) [][]term.Cell {
	l.scan(cells)
	if len(l.spans) == 0 {
		return cells
	}

	if cap(l.rows) < len(cells) {
		l.rows = make([][]term.Cell, len(cells))
	}
	l.rows = l.rows[:len(cells)]
	copy(l.rows, cells)

	if cap(l.buf) < len(cells) {
		grown := make([][]term.Cell, len(cells))
		copy(grown, l.buf)
		l.buf = grown
	}
	l.buf = l.buf[:len(cells)]

	copied := -1
	for _, span := range l.spans {
		if span.y >= len(cells) {
			continue
		}
		if span.y != copied {
			src := cells[span.y]
			row := l.buf[span.y]
			if cap(row) < len(src) {
				row = make([]term.Cell, len(src))
			}
			row = row[:len(src)]
			copy(row, src)
			l.buf[span.y] = row
			l.rows[span.y] = row
			copied = span.y
		}
		row := l.rows[span.y]
		for x := span.x0; x < span.x1 && x < len(row); x++ {
			row[x].Attrs |= term.AttrUnderline
		}
	}
	return l.rows
}

// handleMouse consumes a meta-click on a rendered link and reports
// whether the event was consumed.
func (l *linkScanner) handleMouse(ev term.Event, cells [][]term.Cell) bool {
	if l.clicking {
		if ev.Key == term.MouseRelease {
			l.clicking = false
		}
		return true
	}
	if !l.meta || ev.Key != term.MouseLeft {
		return false
	}
	l.scan(cells)
	span, ok := l.at(ev.MouseX, ev.MouseY)
	if !ok {
		return false
	}
	l.clicking = true
	u, err := url.Parse(span.url)
	if err != nil {
		log.Warnf("gui: link %q: %v", span.url, err)
		return true
	}
	l.observer(u)
	return true
}

func (l *linkScanner) at(x, y int) (linkSpan, bool) {
	for _, span := range l.spans {
		if span.y == y && x >= span.x0 && x < span.x1 {
			return span, true
		}
	}
	return linkSpan{}, false
}
