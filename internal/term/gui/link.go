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
	"sort"
	"unicode"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/workspace/linecol"
)

// Link describes a clickable link rendered in the composited frame.
//
// A link is either a URL or a file position:
//
//   - URL is set when the text carried a scheme (https://, http:// or
//     file://). It is nil for a bare path reference.
//   - Path is the file path of a file link: the path of a file:// URL,
//     or the path of a bare reference as it was written. It is empty
//     for a link that does not point at a file.
//   - Line and Col are the 1-based position of a file link. Line zero
//     means the text carried no position, and column zero means the
//     text named a line but no column. linecol.Coord turns either into
//     the zero-based coordinates the editor cursor uses.
type Link struct {
	URL       *url.URL
	Path      string
	Line, Col int
}

// linkKind tells a scheme link from a bare path reference. The scanner
// knows which of the two it matched, so the click handler does not have
// to guess from the text.
type linkKind uint8

const (
	linkScheme linkKind = iota
	linkPath
)

// linkSpan locates a link rendered as plain text in the composited
// frame. x1 is exclusive.
type linkSpan struct {
	y, x0, x1 int
	link      Link
	kind      linkKind
	// text is the address of a scheme link, as it was rendered. The URL
	// is parsed when the link is clicked, so that a frame with many
	// links does not pay for a parse per link per frame.
	text string
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
	// offsets holds the offset in wrapped of every cell it holds, so
	// that a position in the joined text maps back to a cell.
	offsets []int
	// segs holds the rows the joined text was built from.
	segs []joinSegment
	// pos holds the position of a bare path reference.
	pos []byte
}

// joinSegment records the part of a joined link that one row carries.
type joinSegment struct {
	row   int
	x0    int
	count int
}

// scan appends the spans found in cells to dst and returns it.
func (s *rowScanner) scan(cells [][]term.Cell, dst []linkSpan) []linkSpan {
	for y := 0; y < len(cells); y++ {
		before := len(dst)
		dst = s.scanRow(y, cells[y], dst)
		if len(dst) == before {
			continue
		}
		// A path that begins on the first cell of a row the terminal
		// broke may be the second half of a path the row above started.
		// Reading it on its own would name a different file, so it is
		// not offered as a link.
		dst = dropContinuedPaths(cells, y, before, dst)
		if len(dst) == before {
			continue
		}
		// A link that ends on the wrap marker continues on the next row.
		for i := len(dst) - 1; i >= before; i-- {
			if !wrapMarked(cells[y], dst[i].x1) {
				continue
			}
			dst, y = s.joinWrapped(cells, y, i, dst)
			break
		}
	}
	return dst
}

// dropContinuedPaths removes the path spans of a row that starts in the
// middle of a reference the row above broke. The row above carries the
// wrap marker on the last cell the pane wrote, and a path that ends on a
// path rune there says the reference ran to the margin rather than
// ending in ordinary prose.
func dropContinuedPaths(
	cells [][]term.Cell, y, before int, dst []linkSpan,
) []linkSpan {
	if y == 0 || len(dst) == before {
		return dst
	}
	above := cells[y-1]
	end := len(above)
	for end > 0 && above[end-1].Ch == 0 {
		end--
	}
	if end < 2 || !wrapMarked(above, end) || !isPathRune(above[end-2].Ch) {
		return dst
	}
	// A name that reached the margin without a position is not half a
	// reference, and neither is ordinary prose that happens to end in a
	// path rune. Only a name that looks like a path is worth dropping.
	if !rowEndsInPath(above[:end-1]) {
		return dst
	}
	kept := dst[:before]
	for _, span := range dst[before:] {
		if span.kind == linkPath && span.x0 == 0 {
			continue
		}
		kept = append(kept, span)
	}
	return kept
}

// rowEndsInPath reports whether the cells end in something shaped like
// a path, by checking the last run of path runes without building a
// string. A row that ends mid-word is prose; one that ends in a
// directory or an extension is a path.
func rowEndsInPath(row []term.Cell) bool {
	// Walk back over the path runes.
	end := len(row)
	for end > 0 {
		c := row[end-1].Ch
		if c == 0 {
			end--
			continue
		}
		if !isPathRune(c) {
			break
		}
		end--
	}
	if end == len(row) {
		return false
	}
	// The run is row[end:]. It is a path when it holds a separator, or
	// an extension that starts with a letter.
	hasDot := false
	for i := end; i < len(row); i++ {
		switch row[i].Ch {
		case '/':
			return true
		case '.':
			hasDot = true
		}
	}
	if !hasDot {
		return false
	}
	last := rune(0)
	for i := len(row) - 1; i >= end; i-- {
		if row[i].Ch != 0 {
			last = row[i].Ch
			break
		}
	}
	return last >= 'a' && last <= 'z' || last >= 'A' && last <= 'Z'
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
	for x > 0 {
		prev := row[x-1]
		if prev.Ch == 0 {
			// A wide rune owns two cells and the second holds no rune,
			// so the pane's content starts at the cell in front of it.
			// A blank is also zero, and ends the run like any other
			// cell the pane did not write.
			if x >= 2 && row[x-2].Width > 1 {
				x -= 2
				continue
			}
			break
		}
		if isFrameRune(prev.Ch) {
			break
		}
		x--
	}
	return x
}

// lineEnd returns the index one past the last cell a line can reach from
// x. Any rune beyond ASCII counts, so that an internationalized host
// survives, which also means the run would otherwise swallow the border
// or scroll bar the window drew hard against the pane. The wrap marker
// is the one thing that names the pane's right margin, so a line that
// reaches it stops there. open carries the brackets the address has left
// open, so a line continuing a wrapped address can still close them.
func lineEnd(row []term.Cell, x int, open *nesting) int {
	// Ch == 0 is both a blank cell and the continuation half of a wide
	// character, so it ends the line either way.
	for x < len(row) && row[x].Ch != 0 && isURLRune(row[x].Ch) && open.admit(row[x].Ch) {
		if row[x].Bytes == cell.WrapMarker {
			return x + 1
		}
		x++
	}
	return x
}

// nesting counts the brackets an address has opened and not yet closed,
// one count per kind of bracket.
type nesting [3]int

// admit reports whether r continues the address, counting it if it is a
// bracket. A closing bracket the address never opened belongs to the
// prose around it, as the ")" and "](" closing a markdown link do, so it
// ends the address rather than being swallowed with whatever follows.
func (n *nesting) admit(r rune) bool {
	switch r {
	case '(', '[', '{':
		n[bracketKind(r)]++
	case ')', ']', '}':
		kind := bracketKind(r)
		if n[kind] == 0 {
			return false
		}
		n[kind]--
	}
	return true
}

// bracketKind indexes the count a bracket belongs to. The opener and
// closer of each ASCII pair share their top three bits, which tell the
// three pairs apart, so the shift stays cheap enough for admit to inline
// into the cell walk.
func bracketKind(r rune) int {
	return int(r>>5) - 1
}

// joinWrapped continues the link at dst[first] onto the rows it wrapped
// onto and rewrites every span it covers with the whole address.
func (s *rowScanner) joinWrapped(
	cells [][]term.Cell, y, first int, dst []linkSpan,
) ([]linkSpan, int) {
	span := dst[first]
	left := paneStart(cells[y], span.x1-1)
	s.wrapped = s.wrapped[:0]
	s.offsets = s.offsets[:0]
	s.segs = s.segs[:0]
	s.appendCells(cells[y], span.x0, span.x1)
	s.segs = append(s.segs, joinSegment{row: y, x0: span.x0, count: span.x1 - span.x0})
	// Brackets are ASCII, which never occurs inside a multi-byte rune.
	var open nesting
	for _, b := range s.wrapped {
		open.admit(rune(b))
	}
	// A wrapped link need not be the last one on its row: the row may
	// carry spans from a neighbouring pane. Those are not this link's,
	// so the join works on its own indices and leaves the rest alone.
	joined := []int{first}
	for y+1 < len(cells) {
		row := cells[y+1]
		if left >= len(row) {
			break
		}
		end := lineEnd(row, left, &open)
		if end == left {
			break
		}
		s.appendCells(row, left, end)
		s.segs = append(s.segs, joinSegment{row: y + 1, x0: left, count: end - left})
		y++
		dst = append(dst, linkSpan{y: y, x0: left, x1: end, kind: span.kind})
		joined = append(joined, len(dst)-1)
		if !wrapMarked(row, end) {
			break
		}
	}
	if span.kind == linkPath {
		return s.joinPath(cells, y, dst, joined)
	}
	trimmed := trimURL(s.wrapped)
	if !hasHost(trimmed) {
		// The run reached the margin on nothing but its scheme and the
		// rows below it did not carry a host after all.
		return dropSpans(dst, joined), y
	}
	// Trimming drops nothing but ASCII punctuation, which never
	// continues a cluster and so always had a cell to itself.
	last := joined[len(joined)-1]
	dst[last].x1 -= len(s.wrapped) - len(trimmed)
	for len(joined) > 1 && dst[last].x1 <= dst[last].x0 {
		dst = dropSpans(dst, joined[len(joined)-1:])
		joined = joined[:len(joined)-1]
		last = joined[len(joined)-1]
	}
	text := string(trimmed)
	for _, i := range joined {
		dst[i].text = text
	}
	return dst, y
}

// dropSpans removes the spans at the given indices, leaving the order
// of the rest alone.
func dropSpans(dst []linkSpan, drop []int) []linkSpan {
	if len(drop) == 0 {
		return dst
	}
	skip := make(map[int]bool, len(drop))
	for _, i := range drop {
		skip[i] = true
	}
	out := dst[:0]
	for i, span := range dst {
		if skip[i] {
			continue
		}
		out = append(out, span)
	}
	return out
}

// joinPath finishes a bare path reference that wrapped onto the rows
// below it. The reference is read from the joined text, so that a
// position the terminal broke across the margin is read whole.
func (s *rowScanner) joinPath(
	cells [][]term.Cell, y int, dst []linkSpan, joined []int,
) ([]linkSpan, int) {
	path, line, col, start, end, ok := parsePathRef(string(s.wrapped))
	if !ok {
		// The rows below the margin do not continue the reference, so
		// the first row keeps the reference it found on its own.
		return dropSpans(dst, joined[1:]), y
	}
	first := joined[0]
	// The reference ends in the cell the joined text ends in.
	if count := s.cellsBefore(end); count > 0 {
		row, x := s.locate(count - 1)
		for n, i := range joined {
			if dst[i].y != row {
				continue
			}
			dst[i].x1 = x + 1
			// A reference that ends on an earlier row ends the join
			// there: the rows below it belong to something else.
			dst = dropSpans(dst, joined[n+1:])
			joined = joined[:n+1]
			break
		}
	}
	// The path may start inside the first row, after a delimiter the
	// prose put in front of it.
	if start > 0 {
		if row, x := s.locate(s.cellsBefore(start)); row == dst[first].y {
			dst[first].x0 = x
		}
	}
	link := Link{Path: path, Line: line, Col: col}
	for _, i := range joined {
		dst[i].link = link
	}
	return dst, y
}

// appendCells appends the cells of a row to the joined text, recording
// the offset of every cell so that a position in the text maps back to
// the cell it fell in.
func (s *rowScanner) appendCells(row []term.Cell, x0, x1 int) {
	for x := x0; x < x1; x++ {
		s.offsets = append(s.offsets, len(s.wrapped))
		if row[x].Ch == 0 {
			// The second cell of a wide rune holds no text of its own.
			continue
		}
		s.wrapped = appendCluster(s.wrapped, &row[x])
	}
}

// cellsBefore returns the number of cells of the joined text that end
// before the given offset.
func (s *rowScanner) cellsBefore(offset int) int {
	return sort.Search(len(s.offsets), func(i int) bool {
		return s.offsets[i] >= offset
	})
}

// locate returns the row and the cell in it that the given cell of the
// joined text fell in.
func (s *rowScanner) locate(count int) (row, x int) {
	for _, seg := range s.segs {
		if count < seg.count {
			return seg.row, seg.x0 + count
		}
		count -= seg.count
	}
	return 0, 0
}

// scanRow matches against the cells themselves rather than a flattened
// copy of the row, so prose never reaches the scratch buffer: it is only
// touched once a scheme is found. The walk looks for a scheme lead and
// a colon at once, so the cells of the row are read once.
func (s *rowScanner) scanRow(y int, row []term.Cell, dst []linkSpan) []linkSpan {
	// A run that ends on the wrap marker is half an address, so it is
	// appended raw and left for joinWrapped to finish. Only the last
	// span of a row can still be waiting: anything matched after it
	// proves the join will never reach it.
	pending := -1
	for x := 0; x < len(row); {
		found := linkStart(row[x:])
		if found < 0 {
			break
		}
		x += found

		if row[x].Ch == ':' {
			// A colon can start a path reference.
			dst, x = s.scanPath(y, row, x, dst)
			continue
		}

		width, ok := schemeAt(row, x)
		if !ok {
			x++
			continue
		}

		var open nesting
		end := lineEnd(row, x+width, &open)

		s.text = s.text[:0]
		for i := x; i < end; i++ {
			s.text = appendCluster(s.text, &row[i])
		}

		if wrapMarked(row, end) {
			dst = s.settle(row, dst, pending)
			dst = append(dst, linkSpan{y: y, x0: x, x1: end, kind: linkScheme})
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
				y:    y,
				x0:   x,
				x1:   end - (len(s.text) - len(trimmed)),
				kind: linkScheme,
				text: string(trimmed),
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
	dst[i].text = string(trimmed)
	return dst
}

// scanPath matches the path reference whose colon is at the given cell
// and returns the cell the scan continues from. The reference is read
// backwards over the path characters, so ordinary prose is rejected
// without touching the filesystem.
func (s *rowScanner) scanPath(
	y int, row []term.Cell, x int, dst []linkSpan,
) ([]linkSpan, int) {
	// A colon only starts a position when a digit follows it. Log output
	// is full of colons that do not, so this is settled before the run
	// in front of the colon is walked.
	if x+1 >= len(row) || !isDigit(row[x+1].Ch) {
		return dst, x + 1
	}
	start := x
	// isFilePathBytes can only accept a run that holds a slash or a dot,
	// so a run with neither is rejected without building the text. A log
	// line puts a colon after every digit, and the walk over the
	// timestamp in front of one is the costliest thing this does.
	slash, dot := false, false
	for start > 0 {
		cell := start - 1
		ch := row[cell].Ch
		if ch == 0 {
			if cell == 0 || row[cell-1].Width <= 1 {
				// A blank the pane did not write, so the path ends.
				break
			}
			// The second cell of a wide rune: the rune in front of it
			// decides whether the path continues past it.
			cell--
			ch = row[cell].Ch
		}
		if ch == '/' {
			slash = true
		} else if ch == '.' {
			dot = true
		}
		if !isPathRune(ch) {
			break
		}
		start = cell
	}
	if start == x {
		// Nothing in front of the colon, so there is no path.
		return dst, x + 1
	}
	if !slash && !dot {
		return dst, x + 1
	}
	if coveredBySpan(dst, y, start, x) {
		// The text belongs to a URL, which the scheme pass owns.
		return dst, x + 1
	}

	s.text = s.text[:0]
	for i := start; i < x; i++ {
		// The second cell of a wide rune holds no text of its own.
		if row[i].Ch == 0 {
			continue
		}
		s.text = appendCluster(s.text, &row[i])
	}
	if !isFilePathBytes(s.text) {
		return dst, x + 1
	}

	line, col, end, ok := s.parsePosition(row, x+1)
	if !ok {
		return dst, x + 1
	}

	dst = append(dst, linkSpan{
		y:    y,
		x0:   start,
		x1:   end,
		link: Link{Path: string(s.text), Line: line, Col: col},
		kind: linkPath,
	})
	return dst, end
}

// parsePosition reads the position that starts at the given cell of the
// row and returns the 1-based line and column with the cell one past
// the position. The rules live in linecol: the cells are copied into a
// byte slice, Parse decides what is a position, and its byte count is
// the number of cells the position covers, since every cell in the run
// is one ASCII byte.
func (s *rowScanner) parsePosition(row []term.Cell, x int) (line, col, end int, ok bool) {
	s.pos = s.pos[:0]
	for i := x; i < len(row); i++ {
		c := row[i].Ch
		if c != ':' && !isDigit(c) {
			break
		}
		s.pos = append(s.pos, byte(c))
	}
	line, col, n, ok := linecol.Parse(string(s.pos))
	if !ok {
		return 0, 0, 0, false
	}
	return line, col, x + n, true
}

// coveredBySpan reports whether the cells of the row between x0 and x1
// are covered by a link that was already found.
func coveredBySpan(dst []linkSpan, y, x0, x1 int) bool {
	for _, span := range dst {
		if span.y != y {
			continue
		}
		if x0 < span.x1 && span.x0 < x1 {
			return true
		}
	}
	return false
}

// isDigit reports whether r is an ASCII digit.
func isDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

// pathRuneTable holds the ASCII runes that can appear in a path, so the
// backward walk can classify a cell with one array read instead of two
// switches. Index it with a byte; anything at or above 0x80 is not ASCII.
var pathRuneTable = func() (t [utf8.RuneSelf]bool) {
	for c := 'a'; c <= 'z'; c++ {
		t[c] = true
	}
	for c := 'A'; c <= 'Z'; c++ {
		t[c] = true
	}
	for c := '0'; c <= '9'; c++ {
		t[c] = true
	}
	for _, c := range []byte("/._-~+@") {
		t[c] = true
	}
	return t
}()

// isPathRune reports whether r can appear in a file path written in
// prose. The glyphs the window draws its own chrome with are excluded,
// so a border drawn against the text does not become part of the path.
func isPathRune(r rune) bool {
	if uint32(r) < utf8.RuneSelf {
		return pathRuneTable[byte(r)]
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// isFilePath reports whether the text names a file rather than prose. A
// path with a directory separator is a path whatever it ends in, and a
// bare name must carry an extension that starts with a letter, so that
// "127.0.0.1:8080" and "1.5:1" are not paths.
func isFilePath(path string) bool {
	return isFilePathBytes([]byte(path))
}

// isFilePathBytes reports whether the bytes name a file rather than
// prose. It takes bytes so the scanner can decide before it builds a
// string for every colon it passes.
func isFilePathBytes(path []byte) bool {
	for _, b := range path {
		if b == '/' {
			return true
		}
	}
	dot := bytes.LastIndexByte(path, '.')
	if dot <= 0 || dot == len(path)-1 {
		return false
	}
	c := path[dot+1]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// parsePathRef reads a bare path reference from text and returns the
// path it names, the 1-based position it carries and the bounds of the
// reference within the text.
func parsePathRef(text string) (path string, line, col, start, end int, ok bool) {
	pos := -1
	for i := 0; i+1 < len(text); i++ {
		if text[i] == ':' && isDigit(rune(text[i+1])) {
			pos = i
			break
		}
	}
	if pos < 0 {
		return "", 0, 0, 0, 0, false
	}
	start = pos
	for start > 0 {
		r, size := utf8.DecodeLastRuneInString(text[:start])
		if !isPathRune(r) {
			break
		}
		start -= size
	}
	if start == pos {
		return "", 0, 0, 0, 0, false
	}
	path = text[start:pos]
	if !isFilePath(path) {
		return "", 0, 0, 0, 0, false
	}
	line, col, n, ok := linecol.Parse(text[pos+1:])
	if !ok {
		return "", 0, 0, 0, 0, false
	}
	return path, line, col, start, pos + 1 + n, true
}

// linkStart returns the offset of the first cell that could begin a
// link, or -1. A cell could begin one when it is the lead of a scheme
// or the colon of a path reference. Prose is the overwhelming majority
// of a frame, so rejecting it is the loop worth tuning. A cell is 24
// bytes, so an indexed load has to rescale the index on every step.
// Taking a fixed-length window amortizes that into one address the
// compiler reaches the rest of the window from by immediate offset, and
// its constant bounds are what let the checks fall away.
func linkStart(row []term.Cell) int {
	i := 0
	for ; i+8 <= len(row); i += 8 {
		win := row[i : i+8 : i+8]
		if linkLead(win[0].Ch) || linkLead(win[1].Ch) ||
			linkLead(win[2].Ch) || linkLead(win[3].Ch) ||
			linkLead(win[4].Ch) || linkLead(win[5].Ch) ||
			linkLead(win[6].Ch) || linkLead(win[7].Ch) {
			break
		}
	}
	for ; i < len(row); i++ {
		if linkLead(row[i].Ch) {
			return i
		}
	}
	return -1
}

// linkLead reports whether a cell can begin a link. The subtraction
// maps 'f' and 'h' — and nothing else — onto the two values the mask
// clears, so the test costs one branch instead of two, and the colon
// of a path reference is checked alongside them.
func linkLead(ch rune) bool {
	return (uint32(ch)-'f')&^2 == 0 || ch == ':'
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

// appendWideCluster is split out from appendCluster so that the ASCII cell
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
	return r >= '─' && r <= '▟'
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

// trimURL strips trailing sentence punctuation, which belongs to the
// surrounding prose rather than the URL.
func trimURL(raw []byte) []byte {
	for len(raw) > 0 {
		switch raw[len(raw)-1] {
		case '.', ',', ';', ':', '!', '?':
		default:
			return raw
		}
		raw = raw[:len(raw)-1]
	}
	return raw
}

// hasHost reports whether the URL carries anything after its scheme.
func hasHost(u []byte) bool {
	i := bytes.Index(u, schemeSeparator)
	return i >= 0 && len(u) > i+3
}

// schemeLink builds the Link of a link whose text carried a scheme. The
// text must carry a host to be a link at all. A URL that does not parse
// is not openable.
func schemeLink(text string) (Link, bool) {
	u, err := url.Parse(text)
	if err != nil {
		return Link{}, false
	}
	if u.Scheme != "file" {
		return Link{URL: u}, true
	}
	path, line, col := u.Path, 0, 0
	fragment := u.Fragment != ""
	if frag := u.Fragment; frag != "" {
		if l, c, _, ok := linecol.Parse(frag); ok {
			line, col = l, c
		}
	} else if p, pos, rest, ok := linecol.Cut(path); ok && rest == "" {
		// A file path is only a reference when the position ends it: an
		// ambiguous suffix such as y.go:7:2:4 stays in the path.
		path, line, col = p, pos.Line, pos.Col
	}
	// A position that came from either the fragment or the path is not
	// part of the file's address. Leaving it in the URL would open a
	// second tab for the same file, which the workspace refuses.
	if path != u.Path || fragment {
		clean := *u
		clean.Path = path
		clean.RawPath = ""
		clean.Fragment = ""
		clean.RawFragment = ""
		u = &clean
	}
	return Link{URL: u, Path: path, Line: line, Col: col}, true
}

type linkScanner struct {
	observer func(Link)
	pointer  *pointer
	scanner  rowScanner
	spans    []linkSpan
	valid    bool
	meta     bool
	// clicking records that a meta-click already opened a link, so the
	// rest of that gesture is swallowed too.
	clicking bool
	// rows is the underlined view of the frame; only rows carrying a
	// span are copied into buf, the rest alias the source rows.
	rows [][]term.Cell
	buf  [][]term.Cell
}

func newLinkScanner(p *pointer) linkScanner {
	return linkScanner{
		observer: func(Link) {},
		pointer:  p,
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
		l.pointer.setLink(false)
	}
	return true
}

// pointAt offers the link cursor while the mouse rests on a link,
// overriding whatever shape the handler under it asked for.
func (l *linkScanner) pointAt(pos term.Coordinates, cells [][]term.Cell) {
	l.scan(cells)
	_, over := l.at(pos.X, pos.Y)
	l.pointer.setLink(over)
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
	link, ok := span.buildLink()
	if !ok {
		// A URL that does not parse is not openable.
		return true
	}
	l.observer(link)
	return true
}

// buildLink builds the Link of a span. The URL of a scheme link is
// parsed here, so that a scan does not parse a URL for every link of
// every frame.
func (s linkSpan) buildLink() (Link, bool) {
	if s.kind == linkPath {
		return s.link, true
	}
	return schemeLink(s.text)
}

func (l *linkScanner) at(x, y int) (linkSpan, bool) {
	for _, span := range l.spans {
		if span.y == y && x >= span.x0 && x < span.x1 {
			return span, true
		}
	}
	return linkSpan{}, false
}
