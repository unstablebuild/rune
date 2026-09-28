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
	"regexp"
	"regexp/syntax"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

// Regex commands, from helix-core/src/selection.rs and the search
// commands in helix-term/src/commands.rs. Helix runs its regexes over
// the rope with the search bounded to a range while anchors still see
// the surrounding text; Go's regexp has no bounded search, so a search
// over the document runs on the whole text and picks the match by
// position, and a command over a range runs on that range's text with
// one character of context on either side when the pattern has an
// anchor to look at it.

// docIndex is the document as one string with the byte offset every
// row starts at, for mapping regex matches back onto cells. A cell
// holds a whole grapheme, so a column is found by walking the row's
// cells and their byte lengths rather than counting runes; the rows
// are read from the buffer when needed rather than kept, so an index
// held across an edit never aliases storage the buffer has let go of.
type docIndex struct {
	text     string
	rowStart []int
	buf      *cell.Buffer
}

func indexDoc(buf *cell.Buffer) docIndex {
	cells := buf.View().RawCells()
	rows := buf.Rows()
	var b strings.Builder
	starts := make([]int, 0, rows+1)
	for y := range rows {
		if y > 0 {
			b.WriteByte('\n')
		}
		starts = append(starts, b.Len())
		if y < len(cells) {
			for _, c := range cells[y] {
				writeCell(&b, c)
			}
		}
	}
	// The position past the final line ending, one beyond the text.
	starts = append(starts, b.Len()+1)
	return docIndex{text: b.String(), rowStart: starts, buf: buf}
}

func writeCell(b *strings.Builder, c term.Cell) {
	b.WriteRune(c.Ch)
	for _, comb := range c.CombiningRunes() {
		b.WriteRune(comb)
	}
}

// cellBytes is the length of what writeCell writes for c.
func cellBytes(c term.Cell) int {
	n := runeBytes(c.Ch)
	for _, comb := range c.CombiningRunes() {
		n += runeBytes(comb)
	}
	return n
}

// runeBytes is the encoded length of ch, which for a rune that is not
// valid UTF-8 is the replacement character strings.Builder writes.
func runeBytes(ch rune) int {
	if n := utf8.RuneLen(ch); n > 0 {
		return n
	}
	return utf8.RuneLen(utf8.RuneError)
}

func (d docIndex) row(y int) []term.Cell {
	cells := d.buf.View().RawCells()
	if y < 0 || y >= len(cells) {
		return nil
	}
	return cells[y]
}

// offset is the byte offset of a document position, clamped onto the
// text.
func (d docIndex) offset(pos term.Coordinates) int {
	if pos.Y < 0 {
		return 0
	}
	if pos.Y >= len(d.rowStart)-1 {
		return len(d.text)
	}
	off := d.rowStart[pos.Y]
	row := d.row(pos.Y)
	for _, c := range row[:max(0, min(pos.X, len(row)))] {
		off += cellBytes(c)
	}
	return off
}

// pos is the document position of a byte offset: the cell the offset
// falls in, so an offset inside a grapheme names the cell holding it.
func (d docIndex) pos(off int) term.Coordinates {
	off = max(0, min(off, len(d.text)))
	y := sort.Search(len(d.rowStart), func(i int) bool { return d.rowStart[i] > off }) - 1
	y = max(0, min(y, len(d.rowStart)-2))
	at, x := d.rowStart[y], 0
	for _, c := range d.row(y) {
		n := cellBytes(c)
		if at+n > off {
			break
		}
		at += n
		x++
	}
	return term.Coordinates{Y: y, X: x}
}

// endPos is pos for the end of a match: an offset inside a grapheme
// names the cell after it, so the match covers the cell it ends in.
func (d docIndex) endPos(off int) term.Coordinates {
	p := d.pos(off)
	if d.offset(p) < off && p.X < len(d.row(p.Y)) {
		p.X++
	}
	return p
}

// docCache is the index and the matches of the last pattern, valid
// until the buffer reports an edit: n, * and the prompt's live preview
// all scan the whole document, and a keystroke that changes nothing
// must not pay for that again. The recorder's edit count is what
// tells the entry apart from the text, so an edit made outside the
// handler drops it as well; the row count is a second guard for a
// reload that bypasses the buffer's subscribers.
type docCache struct {
	edits   uint64
	rows    int
	index   docIndex
	matched string
	matches [][]int
}

// doc is the document index, rebuilt only once the buffer has changed.
func (h *helixHandlerImpl) doc() docIndex {
	buf := h.buf()
	c := &h.docs
	if c.index.buf != buf || c.edits != h.rec.edits || c.rows != buf.Rows() {
		*c = docCache{edits: h.rec.edits, rows: buf.Rows(), index: indexDoc(buf)}
	}
	return c.index
}

// matches is every match of re over the document, in order, computed
// once per pattern between edits. The spans are shared and must not
// be written to.
func (h *helixHandlerImpl) matches(re *regexp.Regexp) [][]int {
	d := h.doc()
	c := &h.docs
	if pattern := re.String(); c.matched != pattern {
		c.matched = pattern
		c.matches = re.FindAllStringIndex(d.text, -1)
	}
	return c.matches
}

// matcher runs one pattern over ranges of one document. Whether the
// pattern looks at the text around a position is settled once, not
// per range.
type matcher struct {
	d       docIndex
	re      *regexp.Regexp
	context bool
}

func newMatcher(d docIndex, re *regexp.Regexp) matcher {
	return matcher{d: d, re: re, context: hasContextAssertion(re)}
}

// spans is regex.find_iter over the text of one range, as byte spans
// into the document. A pattern with an anchor or a word boundary runs
// over the range with the character before and after it in view, so
// ^ at the start of a range in the middle of a line does not match,
// and only the matches inside the range count; a match that runs past
// the end of the range is dropped rather than cut short, which is
// where this differs from a bounded search.
func (m matcher) spans(r rng) [][]int {
	d := m.d
	from, to := d.offset(r.from()), d.offset(r.to())
	to = max(from, to)
	if !m.context {
		return shiftSpans(m.re.FindAllStringIndex(d.text[from:to], -1), from, from, to)
	}
	start := from
	if from > 0 {
		_, n := utf8.DecodeLastRuneInString(d.text[:from])
		start -= n
	}
	end := to
	if to < len(d.text) {
		_, n := utf8.DecodeRuneInString(d.text[to:])
		end += n
	}
	return shiftSpans(m.re.FindAllStringIndex(d.text[start:end], -1), start, from, to)
}

// ranges is spans as document ranges.
func (m matcher) ranges(r rng) []rng {
	var out []rng
	for _, span := range m.spans(r) {
		out = append(out, rng{anchor: m.d.pos(span[0]), head: m.d.endPos(span[1])})
	}
	return out
}

// shiftSpans moves spans found at base into document offsets, keeping
// those inside [from, to].
func shiftSpans(spans [][]int, base, from, to int) [][]int {
	var out [][]int
	for _, span := range spans {
		span[0] += base
		span[1] += base
		if span[0] >= from && span[1] <= to {
			out = append(out, span)
		}
	}
	return out
}

// hasContextAssertion reports whether the pattern looks at the text
// around a position: a line or text anchor, or a word boundary.
func hasContextAssertion(re *regexp.Regexp) bool {
	tree, err := syntax.Parse(re.String(), syntax.Perl)
	if err != nil {
		return true
	}
	var walk func(*syntax.Regexp) bool
	walk = func(n *syntax.Regexp) bool {
		switch n.Op {
		case syntax.OpBeginLine, syntax.OpEndLine, syntax.OpBeginText, syntax.OpEndText,
			syntax.OpWordBoundary, syntax.OpNoWordBoundary:
			return true
		}
		return slices.ContainsFunc(n.Sub, walk)
	}
	return walk(tree)
}

// compilePattern builds a pattern the way Helix's regex prompt does:
// multi-line anchors, and case-insensitive unless the pattern has an
// upper-case letter (smart case).
func compilePattern(pattern string) (*regexp.Regexp, error) {
	flags := "(?m)"
	if !strings.ContainsFunc(pattern, unicode.IsUpper) {
		flags = "(?mi)"
	}
	return regexp.Compile(flags + pattern)
}

// selectOnMatches is selection::select_on_matches: every match inside
// every range becomes a range, the first of them primary. A point
// right at a range's end is dropped: it is an anchor such as $ matching
// just outside the range. Nothing found reports false.
func selectOnMatches(m matcher, s selection) (selection, bool) {
	var out []rng
	for _, r := range s.ranges {
		end := point(r.to())
		for _, hit := range m.ranges(r) {
			if !hit.same(end) {
				out = append(out, hit)
			}
		}
	}
	if len(out) == 0 {
		return s, false
	}
	return selection{ranges: out}.normalized(), true
}

// splitOnMatches is selection::split_on_matches: every range becomes
// the pieces between its matches, a point staying as it is.
func splitOnMatches(m matcher, s selection) selection {
	var out []rng
	for _, r := range s.ranges {
		if r.isPoint() {
			out = append(out, r)
			continue
		}
		start, end := r.from(), r.to()
		for _, hit := range m.ranges(r) {
			out = append(out, rng{anchor: start, head: hit.from()})
			start = hit.to()
		}
		if coordinatesBefore(start, end) {
			out = append(out, rng{anchor: start, head: end})
		}
	}
	return selection{ranges: out}.normalized()
}

// keepOrRemoveMatches is selection::keep_or_remove_matches: the ranges
// whose text the regex matches survive, or the others when remove is
// set. Nothing left reports false.
func keepOrRemoveMatches(m matcher, s selection, remove bool) (selection, bool) {
	var out []rng
	for _, r := range s.ranges {
		if (len(m.spans(r)) > 0) != remove {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return s, false
	}
	return selection{ranges: out}, true
}

// --- search ---

// searchPattern is the pattern n and N follow: the last one written to
// the / register by a prompt, * or Search.
func (h *helixHandlerImpl) searchPattern() (string, bool) {
	data, err := h.readRegister('/')
	if err != nil || data.Text == "" {
		return "", false
	}
	return data.Text, true
}

func (h *helixHandlerImpl) setSearchPattern(pattern string) {
	if err := h.writeRegister('/', clipboard.Data{Text: pattern}); err != nil {
		h.logError(err)
	}
	h.highlightMatches(pattern)
}

// highlightMatches lights every occurrence of the search pattern up
// the way the less search does for a literal. Helix has no such
// highlight; it is the one the rest of Rune's editors show.
func (h *helixHandlerImpl) highlightMatches(pattern string) {
	var locs []textapi.Location
	if re, err := compilePattern(pattern); err == nil && pattern != "" {
		all := h.matches(re)
		d := h.doc()
		attr := h.less.Scroll().ResultsAttr
		for _, m := range all {
			if m[1] == m[0] {
				continue
			}
			locs = append(locs, rowLocations(h.buf(), rng{anchor: d.pos(m[0]), head: d.endPos(m[1])}, attr)...)
		}
	}
	h.cursor.SetLocationList(textapi.LocationPriorityInfo, searchLocID, locationsOrNil(locs))
}

// searchNext is search_next and search_prev, and their extend variants
// in select mode: the / register is the pattern, count matches along.
func (h *helixHandlerImpl) searchNext(backward bool) bool {
	pattern, ok := h.searchPattern()
	if !ok {
		return false
	}
	re, err := compilePattern(pattern)
	if err != nil {
		h.less.SetMessage("Invalid regex: %s", pattern)
		return false
	}
	return h.jumping(func() bool {
		moved := false
		for range h.motionCount() {
			if !h.searchImpl(re, h.extend, backward, true) {
				break
			}
			moved = true
		}
		return moved
	})
}

// searchImpl is search_impl: the next match after the primary's end,
// or the last one before its start, wrapping around the document,
// replaces the primary or joins the set as the new primary when
// extending. warn puts the wrap or the miss in the message bar.
func (h *helixHandlerImpl) searchImpl(re *regexp.Regexp, extend, backward, warn bool) bool {
	h.carryEdits()
	all := h.matches(re)
	d := h.doc()
	primary := h.sel.primaryRange()
	var mat []int
	if backward {
		start := d.offset(primary.from())
		mat = lastMatchBefore(all, start)
	} else {
		start := d.offset(primary.to())
		mat = firstMatchFrom(all, start)
	}
	if mat == nil {
		if n := len(all); n > 0 {
			if backward {
				mat = all[n-1]
			} else {
				mat = all[0]
			}
		}
		if warn {
			if mat != nil {
				h.less.SetMessage("Wrapped around document")
			} else {
				h.less.SetMessage("No more matches")
			}
		}
	}
	if mat == nil || mat[1] == 0 {
		return false
	}
	hit := rng{anchor: d.pos(mat[0]), head: d.endPos(mat[1])}.withDirection(primary.backward())
	if extend {
		h.setSelection(h.sel.with(hit))
	} else {
		h.setSelection(h.sel.replace(h.sel.primary, hit))
	}
	h.cursor.Center()
	return true
}

// firstMatchFrom is the first match starting at or after start; the
// matches do not overlap, so their starts are sorted.
func firstMatchFrom(all [][]int, start int) []int {
	i := sort.Search(len(all), func(i int) bool { return all[i][0] >= start })
	if i == len(all) {
		return nil
	}
	return all[i]
}

// lastMatchBefore is the last match ending at or before start; the
// ends are sorted for the same reason the starts are.
func lastMatchBefore(all [][]int, start int) []int {
	i := sort.Search(len(all), func(i int) bool { return all[i][1] > start })
	if i == 0 {
		return nil
	}
	return all[i-1]
}

// searchSelection is search_selection and its A-* variant: the text
// under every range, escaped, joined with | and wrapped in \b where a
// range sits on a word boundary, becomes the / register. Nothing
// moves; n finds the next occurrence.
func (h *helixHandlerImpl) searchSelection(detectWordBoundaries bool) bool {
	if h.config.disableSearch {
		return false
	}
	h.carryEdits()
	buf := h.buf()
	var parts []string
	for _, r := range h.sel.ranges {
		word := regexp.QuoteMeta(rangeText(buf, r))
		if detectWordBoundaries {
			if atWordStart(buf, r.from()) {
				word = `\b` + word
			}
			if atWordEnd(buf, r.to()) {
				word += `\b`
			}
		}
		if !slices.Contains(parts, word) {
			parts = append(parts, word)
		}
	}
	pattern := strings.Join(parts, "|")
	if pattern == "" {
		return false
	}
	h.setSearchPattern(pattern)
	h.less.SetMessage("register '/' set to '%s'", pattern)
	return true
}

// isWordRune is what \b in Go's regexp counts as a word character,
// which is ASCII only. Helix checks the boundary with Unicode word
// characters, but a \b put next to a rune the engine does not count
// as one would never match, so the check has to agree with the
// engine rather than with Helix.
func isWordRune(ch rune) bool {
	return ch == '_' || (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

func atWordStart(buf *cell.Buffer, pos term.Coordinates) bool {
	ch, ok := charAt(buf, pos)
	if !ok {
		return false
	}
	prev, ok := prevPos(buf, pos)
	if !ok {
		return isWordRune(ch)
	}
	pch, _ := charAt(buf, prev)
	return !isWordRune(pch) && isWordRune(ch)
}

func atWordEnd(buf *cell.Buffer, pos term.Coordinates) bool {
	prev, ok := prevPos(buf, pos)
	if !ok {
		return false
	}
	ch, ok := charAt(buf, pos)
	if !ok {
		return false
	}
	pch, _ := charAt(buf, prev)
	return isWordRune(pch) && !isWordRune(ch)
}

// --- regex prompt ---

// promptKind is what the regex prompt does with its pattern.
type promptKind uint8

const (
	promptNone promptKind = iota
	promptSearch
	promptSelect
	promptSplit
	promptKeep
	promptRemove
)

// regexPrompt is ui::regex_prompt's state: the selection to start
// every application of the pattern from, and how the search was
// asked for.
type regexPrompt struct {
	kind     promptKind
	snapshot selection
	backward bool
	extend   bool
}

// openPrompt is ui::regex_prompt: the less prompt collects the
// pattern, the selection is applied to it on every keystroke, and
// <esc> restores what was there.
func (h *helixHandlerImpl) openPrompt(kind promptKind, backward bool) bool {
	if h.config.disableSearch {
		return false
	}
	h.carryEdits()
	var label string
	switch kind {
	case promptSearch:
		label = "search:"
	case promptSelect:
		label = "select:"
	case promptSplit:
		label = "split:"
	case promptKeep:
		label = "keep:"
	case promptRemove:
		label = "remove:"
	}
	h.prompt = regexPrompt{kind: kind, snapshot: h.sel.clone(), backward: backward, extend: h.extend}
	h.less.SetPromptMode(label)
	return true
}

func (h *helixHandlerImpl) handleSearch(ev term.Event) (bool, bool) {
	if ev.Type != term.EventKey || h.prompt.kind == promptNone {
		return h.less.Handle(ev)
	}
	if ev.Mod == 0 {
		switch ev.Key {
		case term.KeyEnter:
			input := h.less.SearchText()
			h.less.SetNormalMode()
			h.promptValidate(input)
			return false, true
		case term.KeyEsc:
			h.less.SetNormalMode()
			h.promptAbort()
			return false, true
		}
	}
	_, handled := h.less.Handle(ev)
	h.promptUpdate(h.less.SearchText())
	return false, handled
}

func (h *helixHandlerImpl) promptAbort() {
	p := h.prompt
	h.prompt = regexPrompt{}
	h.setSelection(p.snapshot)
}

// promptUpdate is PromptEvent::Update: an empty input is skipped and a
// pattern that does not compile puts the selection back.
func (h *helixHandlerImpl) promptUpdate(input string) {
	if input == "" {
		return
	}
	p := h.prompt
	re, err := compilePattern(input)
	if err != nil {
		h.setSelection(p.snapshot)
		return
	}
	h.setSelection(p.snapshot)
	h.applyPrompt(p, re, false)
}

// promptValidate is PromptEvent::Validate: the pattern goes to the /
// register as the prompt's history, the selection it started from to
// the jumplist, and a pattern that does not compile is reported.
func (h *helixHandlerImpl) promptValidate(input string) {
	p := h.prompt
	h.prompt = regexPrompt{}
	if input == "" {
		// An empty line runs the last pattern in the history, as
		// the prompt does.
		last, ok := h.searchPattern()
		if !ok {
			return
		}
		input = last
	}
	h.setSearchPattern(input)
	re, err := compilePattern(input)
	if err != nil {
		h.setSelection(p.snapshot)
		h.less.SetMessage("%s", err.Error())
		return
	}
	h.setSelection(p.snapshot)
	h.pushJumpEntry(p.snapshot.clone())
	h.applyPrompt(p, re, true)
}

func (h *helixHandlerImpl) applyPrompt(p regexPrompt, re *regexp.Regexp, validate bool) {
	switch p.kind {
	case promptSearch:
		h.searchImpl(re, p.extend, p.backward, false)
		return
	}
	m := newMatcher(h.doc(), re)
	switch p.kind {
	case promptSelect:
		if s, ok := selectOnMatches(m, h.sel); ok {
			h.setSelection(s)
		} else if validate {
			h.less.SetMessage("nothing selected")
		}
	case promptSplit:
		h.setSelection(splitOnMatches(m, h.sel))
	case promptKeep, promptRemove:
		if s, ok := keepOrRemoveMatches(m, h.sel, p.kind == promptRemove); ok {
			h.setSelection(s)
		} else if validate {
			h.less.SetMessage("no selections remaining")
		}
	}
}

// --- content commands ---

// alignSelections is align_selections: on every row, the n-th range
// is pushed right with spaces until its head sits on the same visual
// column as the n-th range of every other row, a tab and a wide
// glyph counting for the columns the scroll draws them on. A range
// spanning rows is an error.
func (h *helixHandlerImpl) alignSelections() bool {
	h.carryEdits()
	buf := h.buf()
	tabspaces := h.less.Scroll().Tabspaces()
	var widths []int
	prevRow, col, running := -1, 0, 0
	cols := make([]int, h.sel.len())
	for i, r := range h.sel.ranges {
		if r.head.Y != r.anchor.Y {
			h.less.SetMessage("align cannot work with multi line selections")
			return false
		}
		if r.head.Y != prevRow {
			col, running, prevRow = 0, 0, r.head.Y
		}
		cols[i] = visualColumn(buf, r.head, tabspaces)
		width := cols[i] - running
		if col < len(widths) {
			widths[col] = max(widths[col], width)
		} else {
			widths = append(widths, width)
		}
		running += width
		col++
	}
	positions := make([]int, len(widths))
	sum := 0
	for i, w := range widths {
		sum += w
		positions[i] = sum
	}
	prevRow, col, running = -1, 0, 0
	edits := make([]edit, 0, h.sel.len())
	for i, r := range h.sel.ranges {
		if r.head.Y != prevRow {
			col, running, prevRow = 0, 0, r.head.Y
		}
		n := positions[col] - cols[i] - running
		at := r.from()
		edits = append(edits, edit{from: at, to: at, text: strings.Repeat(" ", n)})
		col++
		running += n
	}
	_, cs := h.applyEdits(edits)
	h.setSelection(h.sel.mapThrough(cs))
	h.exitSelectMode()
	return true
}

// visualColumn is the column the scroll draws pos on, as
// visual_coords_at_pos has it, with a tab taking the scroll's fixed
// width rather than running to a tab stop.
func visualColumn(buf *cell.Buffer, pos term.Coordinates, tabspaces int) int {
	cells := buf.View().RawCells()
	if pos.Y < 0 || pos.Y >= len(cells) {
		return 0
	}
	row := cells[pos.Y]
	col := 0
	for _, c := range row[:max(0, min(pos.X, len(row)))] {
		if c.Ch == '\t' {
			col += max(1, tabspaces)
		} else {
			col += max(1, int(c.Width))
		}
	}
	return col + max(0, pos.X-len(row))
}

// rotateSelectionContents is rotate_selection_contents_forward and
// _backward: the text under the ranges moves count ranges along, and
// the primary index follows it.
func (h *helixHandlerImpl) rotateSelectionContents(forward bool) bool {
	h.carryEdits()
	n := h.sel.len()
	if n < 2 {
		return false
	}
	by := min(h.motionCount(), n)
	buf := h.buf()
	frags := h.sel.fragments(buf)
	primary := h.sel.primary
	if forward {
		frags = append(frags[n-by:], frags[:n-by]...)
		primary = (primary + by) % n
	} else {
		frags = append(frags[by:], frags[:by]...)
		primary = (primary + n - by) % n
	}
	edits := make([]edit, n)
	for i, r := range h.sel.ranges {
		edits[i] = edit{from: r.from(), to: r.to(), text: frags[i]}
	}
	spans, _ := h.applyEdits(edits)
	for i := range spans {
		spans[i] = spans[i].withDirection(h.sel.ranges[i].backward())
	}
	h.setSelection(selection{ranges: spans, primary: primary})
	return true
}
