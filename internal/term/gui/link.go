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
	"slices"
	"unicode"
	"unicode/utf8"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

// Link is a target rendered as text in the frame. URL is set for an
// address that carries a scheme, and Path for a bare file path as
// compilers and grep print it. Line and Col are 1-based and zero when the
// text named no position; a file URL's position is reported here rather
// than left in the URL.
type Link struct {
	URL       *url.URL
	Path      string
	Line, Col int
}

// linkSpan locates a link rendered as plain text in the composited frame.
// x1 is exclusive. A span carries either a url or a bare path.
type linkSpan struct {
	y, x0, x1 int
	url       string
	path      string
	line, col int
}

// linkSchemes are the schemes recognized in rendered text.
var (
	linkSchemes     = []string{"https://", "http://", "file://"}
	schemeSeparator = []byte("://")
	fileScheme      = []byte("file://")
)

// maxPositionDigits bounds a line or column so that it always fits an int.
const maxPositionDigits = 9

// rowScanner matches URLs row by row. It keeps its scratch buffer
// between scans because a scan runs on every frame that redraws while
// the meta modifier is held.
type rowScanner struct {
	text []byte
	// wrapped accumulates a link across the rows the terminal broke it
	// onto.
	wrapped []byte
	// heads holds the rows a path wrapped onto before the row that
	// carries its position.
	heads []linkSpan
}

// scan appends the spans found in cells to dst and returns it.
func (s *rowScanner) scan(cells [][]term.Cell, dst []linkSpan) []linkSpan {
	for y := 0; y < len(cells); y++ {
		before := len(dst)
		dst = s.scanRow(y, cells[y], dst)
		if len(dst) == before {
			continue
		}
		if y > 0 {
			dst = s.joinHeads(cells, y, before, dst)
		}
		last := dst[len(dst)-1]
		if last.url == "" || last.y != y || !wrapMarked(cells[y], last.x1) {
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
	left := paneStart(cells[y], dst[first].x1-1)
	s.wrapped = append(s.wrapped[:0], dst[first].url...)
	// Brackets are ASCII, which never occurs inside a multi-byte rune.
	var open nesting
	for _, b := range s.wrapped {
		open.admit(rune(b))
	}
	last := first
	for y+1 < len(cells) {
		row := cells[y+1]
		if left >= len(row) {
			break
		}
		end := lineEnd(row, left, &open)
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
	address, line, col := splitPosition(trimmed)
	if !hasHost(address) {
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
	joined := string(address)
	for i := first; i <= last; i++ {
		dst[i].url, dst[i].line, dst[i].col = joined, line, col
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
	// floor is where the last match ended, which a path cannot reach back
	// past.
	floor := 0
	for x := 0; x < len(row); {
		found := linkStart(row[x:])
		if found < 0 {
			break
		}
		x += found

		if row[x].Ch == ':' {
			span, ok := s.pathAt(y, row, floor, x)
			if !ok {
				x++
				continue
			}
			dst = s.settle(row, dst, pending)
			pending = -1
			dst = append(dst, span)
			x, floor = span.x1, span.x1
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
			dst = append(dst, linkSpan{y: y, x0: x, x1: end, url: string(s.text)})
			pending = len(dst) - 1
			x, floor = end, end
			continue
		}
		trimmed := trimURL(s.text)
		address, line, col := splitPosition(trimmed)
		if hasHost(address) {
			dst = s.settle(row, dst, pending)
			pending = -1
			// Trimming only ever drops ASCII punctuation, which never
			// continues a cluster and so always had a cell to itself.
			dst = append(dst, linkSpan{
				y:    y,
				x0:   x,
				x1:   end - (len(s.text) - len(trimmed)),
				url:  string(address),
				line: line,
				col:  col,
			})
		}
		x, floor = end, end
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
	address, line, col := splitPosition(trimmed)
	if !hasHost(address) {
		return dst[:i]
	}
	dst[i].x1 -= len(s.wrapped) - len(trimmed)
	dst[i].url, dst[i].line, dst[i].col = string(address), line, col
	return dst
}

// pathAt matches a bare path whose line, and optionally column, follow the
// colon at x, as in "internal/a.go:12:3". Only a colon followed by a digit
// gets this far, which is what keeps prose cheap to reject.
func (s *rowScanner) pathAt(y int, row []term.Cell, floor, x int) (linkSpan, bool) {
	line, col, end, ok := parsePosition(row, x+1)
	if !ok {
		return linkSpan{}, false
	}
	start := pathStart(row, floor, x)
	if isAuthority(row, floor, start, x) {
		return linkSpan{}, false
	}
	s.text = s.text[:0]
	for i := start; i < x; i++ {
		s.text = appendCluster(s.text, &row[i])
	}
	if !isFilePath(s.text) {
		return linkSpan{}, false
	}
	return linkSpan{
		y: y, x0: start, x1: end, path: string(s.text), line: line, col: col,
	}, true
}

// isAuthority reports whether the path in row[start:end] is what follows
// the "scheme:" of a URL, as in the "//h/x.go" of "ftp://h/x.go:12". A
// scheme that linkSchemes does not name is not a link, and its host is
// not a directory either.
func isAuthority(row []term.Cell, floor, start, end int) bool {
	return start > floor && start+1 < end && row[start-1].Ch == ':' &&
		row[start].Ch == '/' && row[start+1].Ch == '/'
}

// joinHeads prepends to the bare path at dst[at:] the rows it wrapped
// across before reaching its position, when it begins at the left margin of
// its pane. Each head is inserted after the last span of its own row, so
// dst stays in row order: overlay copies a row afresh whenever the row of
// the span changes, which would drop the underline of every span already
// drawn on a row it returns to. A head that would overlap a span already on
// its row is not a path continuing but two matches that touch, so the path
// is left without it.
func (s *rowScanner) joinHeads(
	cells [][]term.Cell, y, at int, dst []linkSpan,
) []linkSpan {
	for i := at; i < len(dst); i++ {
		tail := dst[i]
		if tail.path == "" || paneStart(cells[y], tail.x0) != tail.x0 {
			continue
		}
		left := tail.x0
		heads := s.heads[:0]
		for yy := y - 1; yy >= 0; yy-- {
			prev := cells[yy]
			end := left
			for end < len(prev) && prev[end].Ch != 0 && !isFrameRune(prev[end].Ch) {
				end++
			}
			if !wrapMarked(prev, end) {
				break
			}
			start := pathStart(prev, left, end)
			if start == end {
				break
			}
			heads = append(heads, linkSpan{y: yy, x0: start, x1: end})
			if start > left {
				break
			}
		}
		s.heads = heads
		if len(heads) == 0 {
			continue
		}
		slices.Reverse(heads)
		if overlapsAny(dst[:at], heads) {
			continue
		}
		s.text = s.text[:0]
		for _, head := range heads {
			for x := head.x0; x < head.x1; x++ {
				s.text = appendCluster(s.text, &cells[head.y][x])
			}
		}
		s.text = append(s.text, tail.path...)
		if !isFilePath(s.text) {
			continue
		}
		path := string(s.text)
		dst[i].path = path
		for k := range heads {
			heads[k].path, heads[k].line, heads[k].col = path, tail.line, tail.col
		}
		for _, head := range heads {
			// Spans before at are in row order, and every head is on an
			// earlier row than the span at dst[i].
			idx := at
			for idx > 0 && dst[idx-1].y > head.y {
				idx--
			}
			dst = slices.Insert(dst, idx, head)
			at++
			i++
		}
	}
	return dst
}

// overlapsAny reports whether any of heads, in row order, covers a cell a
// span in spans already covers. spans is in row order too.
func overlapsAny(spans, heads []linkSpan) bool {
	for _, head := range heads {
		for i := len(spans) - 1; i >= 0 && spans[i].y >= head.y; i-- {
			if spans[i].y == head.y && spans[i].x0 < head.x1 && head.x0 < spans[i].x1 {
				return true
			}
		}
	}
	return false
}

// parsePosition reads "LINE" or "LINE:COL" from row[i:], returning the
// index one past it. The position must not run into more of a word, so
// "a.go:12x" is not one. A column is dropped, not the whole position,
// when it cannot be read, since "a.go:12:" is just a line followed by the
// colon that ends the message.
func parsePosition(row []term.Cell, i int) (line, col, end int, ok bool) {
	line, end = digits(row, i)
	if line < 0 {
		return 0, 0, 0, false
	}
	if end < len(row) && row[end].Ch == ':' {
		if n, next := digits(row, end+1); n >= 0 {
			col, end = n, next
		}
	}
	if end < len(row) && isWordRune(row[end].Ch) {
		return 0, 0, 0, false
	}
	return line, col, end, true
}

// digits reads a run of ASCII digits from row[i:], returning its value, or
// -1 when there is none or it is too long to be a position, and the index
// one past it.
func digits(row []term.Cell, i int) (n, end int) {
	end = i
	for end < len(row) && row[end].Ch >= '0' && row[end].Ch <= '9' {
		n = n*10 + int(row[end].Ch-'0')
		end++
	}
	if end == i || end-i > maxPositionDigits {
		return -1, end
	}
	return n, end
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// pathStart walks left from i to the first cell of the path that ends
// there, stopping before an opening bracket the path did not close, so
// that "(a/b.go:3)" starts at the "a" while "app/[id]/page.tsx" is whole.
// It also stops at a colon, which joins a path to a prefix as in
// "FAIL:pkg/a.go:3", unless the colon ends a drive letter.
func pathStart(row []term.Cell, floor, i int) int {
	closers := 0
	for i > floor {
		r := row[i-1].Ch
		if r == ':' {
			if isDrive(row, floor, i-1) {
				return i - 2
			}
			break
		}
		if !isPathRune(r) {
			break
		}
		switch r {
		case ')', ']', '}':
			closers++
		case '(', '[', '{':
			if closers == 0 {
				return i
			}
			closers--
		}
		i--
	}
	return i
}

// isDrive reports whether the colon at row[colon] ends a Windows drive
// letter: a single letter that starts a word, or follows another colon as
// in "error:C:\x\a.go", and is followed by a directory separator, as in the
// "C:" of "C:\x\a.go".
func isDrive(row []term.Cell, floor, colon int) bool {
	if colon-1 < floor || colon+1 >= len(row) {
		return false
	}
	letter := row[colon-1].Ch
	if letter > unicode.MaxASCII || !unicode.IsLetter(letter) {
		return false
	}
	if prev := colon - 2; prev >= floor && row[prev].Ch != ':' && isPathRune(row[prev].Ch) {
		return false
	}
	sep := row[colon+1].Ch
	return sep == '\\' || sep == '/'
}

// isPathRune reports whether r can be part of a file path rendered in
// prose. A backslash is allowed for Windows paths. Separators that join
// a path to its surroundings, as in "path=a/b.go:3", are not. A colon is
// not decided here: pathStart accepts one only after a drive letter.
func isPathRune(r rune) bool {
	switch r {
	case '\\':
		return true
	case ',', ';', '=':
		return false
	}
	return isURLRune(r)
}

// isFilePath reports whether p looks like a file path rather than prose
// such as "note" in "note:1" or "12" in "12:30". It is the second line of
// defense after the colon and digit that precede it. A path with a
// directory separator is accepted as it is. A bare name must also end in
// an extension of a source or text file, because "name.ext:digits" is
// otherwise indistinguishable from "host.tld:port", as in
// "listening on example.com:8080". The scanner sees only the rendered
// frame and runs on every frame while the meta modifier is held, so the
// file system is not an option, and an allowlist is the one closed set:
// top-level domains are open ended and many double as extensions.
func isFilePath(p []byte) bool {
	if len(p) == 0 || bytes.Contains(p, schemeSeparator) {
		return false
	}
	return bytes.ContainsAny(p, "/\\") || hasSourceExtension(p)
}

// hasSourceExtension reports whether p ends in a dot and the extension
// of a file that compilers, linters and grep report positions in. The
// comparison ignores case. An ambiguous extension such as "py" or "rs"
// is accepted, so "example.rs:8080" is still taken for a file.
func hasSourceExtension(p []byte) bool {
	dot := bytes.LastIndexByte(p, '.')
	if dot <= 0 || len(p)-dot-1 > maxExtension || dot == len(p)-1 {
		return false
	}
	var ext [maxExtension]byte
	n := copy(ext[:], p[dot+1:])
	for i := range n {
		if ext[i] >= 'A' && ext[i] <= 'Z' {
			ext[i] += 'a' - 'A'
		}
	}
	switch string(ext[:n]) {
	case "go", "mod", "sum", "rs", "toml", "py", "pyi", "js", "mjs", "cjs",
		"jsx", "ts", "mts", "cts", "tsx", "c", "h", "cc", "cpp", "cxx", "hpp",
		"hh", "hxx", "m", "mm", "java", "kt", "kts", "scala", "groovy",
		"gradle", "rb", "erb", "php", "cs", "fs", "swift", "dart", "lua", "pl",
		"pm", "r", "jl", "ex", "exs", "erl", "hrl", "hs", "ml", "mli", "clj",
		"cljs", "zig", "nim", "v", "sv", "sh", "bash", "zsh", "fish", "ps1",
		"bat", "sql", "graphql", "proto", "thrift", "html", "htm", "css",
		"scss", "sass", "less", "vue", "svelte", "astro", "xml", "xsl", "json",
		"jsonc", "yaml", "yml", "ini", "cfg", "conf", "env", "md", "mdx", "rst",
		"txt", "tex", "csv", "tsv", "lock", "tf", "hcl", "nix", "cmake", "s",
		"asm":
		return true
	}
	return false
}

// maxExtension is the length of the longest extension hasSourceExtension
// knows.
const maxExtension = 7

// splitPosition separates the ":LINE" or ":LINE:COL" that ends a file URL
// from the address. Only file URLs are split, since any other scheme may
// carry a port in that place.
func splitPosition(u []byte) (address []byte, line, col int) {
	if !bytes.HasPrefix(u, fileScheme) {
		return u, 0, 0
	}
	rest, last, ok := cutNumber(u)
	if !ok {
		return u, 0, 0
	}
	if rest2, first, ok := cutNumber(rest); ok {
		return rest2, first, last
	}
	return rest, last, 0
}

// cutNumber removes a trailing ":N" from u, never reaching into the
// scheme, and returns what is left and N.
func cutNumber(u []byte) (rest []byte, n int, ok bool) {
	colon := bytes.LastIndexByte(u, ':')
	if colon < len(fileScheme) {
		return u, 0, false
	}
	digits := u[colon+1:]
	if len(digits) == 0 || len(digits) > maxPositionDigits {
		return u, 0, false
	}
	for _, b := range digits {
		if b < '0' || b > '9' {
			return u, 0, false
		}
		n = n*10 + int(b-'0')
	}
	return u[:colon], n, true
}

// linkStart returns the offset of the first cell that could begin a
// scheme or is the colon before a path's position, or -1. Prose is the
// overwhelming majority of a frame, so rejecting it is the loop worth
// tuning. A cell is 24 bytes, so an indexed load has to rescale the index
// on every step. Taking a fixed-length window amortizes that into one
// address the compiler reaches the rest of the window from by immediate
// offset, and its constant bounds are what let the checks fall away.
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

// linkLead reports whether a cell can begin one of linkSchemes, or is the
// colon a path's position follows. The subtraction maps 'f' and 'h', and
// nothing else, onto the two values the mask clears, so the scheme test is
// one branch. The colon is a second branch that every cell of a frame now
// pays; a lookup table and a shift against a bit mask were both measured
// and came out slower than this.
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

// linkScanner gives every URL and file path rendered as plain text a link
// affordance: it underlines the spans while the meta modifier is held and
// turns a meta-click on one into an observer call. It works off the
// composited frame, so it is agnostic to which handler drew the text.
type linkScanner struct {
	observer func(Link)
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
		observer: func(Link) {},
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
	if span.url == "" {
		l.observer(Link{Path: span.path, Line: span.line, Col: span.col})
		return true
	}
	u, err := url.Parse(span.url)
	if err != nil {
		log.Warnf("gui: link %q: %v", span.url, err)
		return true
	}
	l.observer(Link{URL: u, Line: span.line, Col: span.col})
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
