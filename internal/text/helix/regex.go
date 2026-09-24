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
// position, and a command over a range runs on that range's text.

// docIndex is the document as one string with the byte offset every
// row starts at, for mapping regex matches back onto cells. Every
// cell holds one rune, so a column is a rune count into its row.
type docIndex struct {
	text     string
	rowStart []int
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
				b.WriteRune(c.Ch)
			}
		}
	}
	// The position past the final line ending, one beyond the text.
	starts = append(starts, b.Len()+1)
	return docIndex{text: b.String(), rowStart: starts}
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
	row := d.text[off:min(d.rowStart[pos.Y+1]-1, len(d.text))]
	for i := 0; i < pos.X && len(row) > 0; i++ {
		_, n := utf8.DecodeRuneInString(row)
		off += n
		row = row[n:]
	}
	return off
}

// pos is the document position of a byte offset.
func (d docIndex) pos(off int) term.Coordinates {
	y := sort.Search(len(d.rowStart), func(i int) bool { return d.rowStart[i] > off }) - 1
	y = max(0, min(y, len(d.rowStart)-2))
	return term.Coordinates{Y: y, X: utf8.RuneCountInString(d.text[d.rowStart[y]:off])}
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

// matchesIn is a regex's matches over the text of one range, as
// document ranges.
func matchesIn(buf *cell.Buffer, r rng, re *regexp.Regexp) []rng {
	from := r.from()
	text := rangeText(buf, r)
	var out []rng
	for _, m := range re.FindAllStringIndex(text, -1) {
		out = append(out, rng{
			anchor: advance(from, text[:m[0]]),
			head:   advance(from, text[:m[1]]),
		})
	}
	return out
}

// selectOnMatches is selection::select_on_matches: every match inside
// every range becomes a range, the first of them primary. A point
// right at a range's end is dropped: it is an anchor such as $ matching
// just outside the range. Nothing found reports false.
func selectOnMatches(buf *cell.Buffer, s selection, re *regexp.Regexp) (selection, bool) {
	var out []rng
	for _, r := range s.ranges {
		end := point(r.to())
		for _, m := range matchesIn(buf, r, re) {
			if !m.same(end) {
				out = append(out, m)
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
func splitOnMatches(buf *cell.Buffer, s selection, re *regexp.Regexp) selection {
	var out []rng
	for _, r := range s.ranges {
		if r.isPoint() {
			out = append(out, r)
			continue
		}
		start, end := r.from(), r.to()
		for _, m := range matchesIn(buf, r, re) {
			out = append(out, rng{anchor: start, head: m.from()})
			start = m.to()
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
func keepOrRemoveMatches(buf *cell.Buffer, s selection, re *regexp.Regexp, remove bool) (selection, bool) {
	var out []rng
	for _, r := range s.ranges {
		if re.MatchString(rangeText(buf, r)) != remove {
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
		d := indexDoc(h.buf())
		attr := h.less.Scroll().ResultsAttr
		for _, m := range re.FindAllStringIndex(d.text, -1) {
			if m[1] == m[0] {
				continue
			}
			locs = append(locs, rowLocations(h.buf(), rng{anchor: d.pos(m[0]), head: d.pos(m[1])}, attr)...)
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
	d := indexDoc(h.buf())
	primary := h.sel.primaryRange()
	all := re.FindAllStringIndex(d.text, -1)
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
	hit := rng{anchor: d.pos(mat[0]), head: d.pos(mat[1])}.withDirection(primary.backward())
	if extend {
		h.setSelection(h.sel.with(hit))
	} else {
		h.setSelection(h.sel.replace(h.sel.primary, hit))
	}
	h.cursor.Center()
	return true
}

func firstMatchFrom(all [][]int, start int) []int {
	for _, m := range all {
		if m[0] >= start {
			return m
		}
	}
	return nil
}

func lastMatchBefore(all [][]int, start int) []int {
	for i := len(all) - 1; i >= 0; i-- {
		if all[i][1] <= start {
			return all[i]
		}
	}
	return nil
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

func isWordRune(ch rune) bool { return categorizeChar(ch) == catWord }

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
		return
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
	buf := h.buf()
	switch p.kind {
	case promptSearch:
		h.searchImpl(re, p.extend, p.backward, false)
	case promptSelect:
		if s, ok := selectOnMatches(buf, h.sel, re); ok {
			h.setSelection(s)
		} else if validate {
			h.less.SetMessage("nothing selected")
		}
	case promptSplit:
		h.setSelection(splitOnMatches(buf, h.sel, re))
	case promptKeep, promptRemove:
		if s, ok := keepOrRemoveMatches(buf, h.sel, re, p.kind == promptRemove); ok {
			h.setSelection(s)
		} else if validate {
			h.less.SetMessage("no selections remaining")
		}
	}
}

// --- content commands ---

// alignSelections is align_selections: on every row, the n-th range
// is pushed right with spaces until it starts on the same column as
// the n-th range of every other row. A range spanning rows is an
// error.
func (h *helixHandlerImpl) alignSelections() bool {
	h.carryEdits()
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
		width := r.head.X - running
		if col < len(widths) {
			widths[col] = max(widths[col], width)
		} else {
			widths = append(widths, width)
		}
		cols[i] = r.head.X
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
