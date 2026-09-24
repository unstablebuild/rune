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
	"unicode"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
)

// goto_word, from helix-term/src/commands.rs jump_to_word and
// jump_to_label. Every visible word of at least two word characters is
// given a two-character label drawn over its first two cells; typing a
// label selects that word.

// jumpAlphabet is editor.jump-label-alphabet's default, so a label
// addresses up to len(jumpAlphabet)^2 candidates.
var jumpAlphabet = []rune("abcdefghijklmnopqrstuvwxyz")

// jumpLabelAttr stands in for the ui.virtual.jump-label theme key.
// Labels replace buffer content, so they have to be unmistakably chrome.
var jumpLabelAttr = term.Attributes{
	Fg:    term.ColorBlack,
	Bg:    term.ColorFuchsia,
	Attrs: term.AttrBold,
}

// jumpRange is one candidate's (anchor, head) pair in document space.
type jumpRange struct {
	anchor term.Coordinates
	head   term.Coordinates
}

func (r jumpRange) backward() bool { return coordinatesBefore(r.head, r.anchor) }

func (r jumpRange) from() term.Coordinates {
	if r.backward() {
		return r.head
	}
	return r.anchor
}

// forward is Range::with_direction(Direction::Forward).
func (r jumpRange) forward() jumpRange {
	if r.backward() {
		return jumpRange{anchor: r.head, head: r.anchor}
	}
	return r
}

func jumpAlphabetIndex(ch rune) int {
	for i, a := range jumpAlphabet {
		if a == ch {
			return i
		}
	}
	return -1
}

// jumpCandidates walks outward from the caret, alternating forwards and
// backwards, so the labels closest to the caret come first. The word the
// caret is already on is skipped: the walk starts from its own edges.
func jumpCandidates(buf *cell.Buffer, caret, start, end term.Coordinates) []jumpRange {
	limit := len(jumpAlphabet) * len(jumpAlphabet)
	fwd := jumpRange{anchor: caret, head: caret}
	rev := fwd
	if ch, ok := charAt(buf, caret); ok && !unicode.IsSpace(ch) {
		if a, hd := wordMove(buf, caret, caret, 1, nextWordEnd); a == caret {
			fwd = jumpRange{anchor: a, head: hd}
		}
		after, ok := nextPos(buf, caret)
		if a, hd := wordMove(buf, caret, caret, 1, prevWordStart); ok && a == after {
			rev = jumpRange{anchor: a, head: hd}
		}
	}

	words := make([]jumpRange, 0, limit)
	for len(words) < limit {
		changed := false
		for coordinatesBefore(fwd.head, end) {
			next := jumpStep(buf, fwd, nextWordEnd)
			if next == fwd {
				break
			}
			fwd = next
			if !labelWorthy(buf, fwd.head, true) {
				continue
			}
			changed = true
			fwd.anchor = skipToWord(buf, fwd.anchor, true)
			words = append(words, fwd)
			break
		}
		if len(words) == limit {
			break
		}
		for coordinatesBefore(start, rev.head) {
			next := jumpStep(buf, rev, prevWordStart)
			if next == rev {
				break
			}
			rev = next
			if !labelWorthy(buf, rev.head, false) {
				continue
			}
			changed = true
			rev.anchor = skipToWord(buf, rev.anchor, false)
			words = append(words, rev)
			break
		}
		if !changed {
			break
		}
	}
	return words
}

func jumpStep(buf *cell.Buffer, r jumpRange, target wordMotionTarget) jumpRange {
	anchor, head := wordMove(buf, r.anchor, r.head, 1, target)
	return jumpRange{anchor: anchor, head: head}
}

// visibleBounds is the half-open document range labels are offered over:
// the start of the first visible row up to the start of the row after
// the last visible one.
func visibleBounds(scroll *component.Scroll) (start, end term.Coordinates) {
	top := scroll.WindowToScrollCoordinates(term.Coordinates{})
	bottom := scroll.WindowToScrollCoordinates(term.Coordinates{Y: scroll.SizeHeight()})
	start = term.Coordinates{Y: max(0, top.Y)}
	end = term.Coordinates{Y: min(scroll.Buffer().Rows(), max(start.Y, bottom.Y))}
	return start, end
}

// labelWorthy is jump_to_word's add_label test. A word motion stops on
// any run of same-category cells, so without it operator soup like "=<"
// would be offered a label too.
func labelWorthy(buf *cell.Buffer, head term.Coordinates, forward bool) bool {
	// A forward range ends one past the word; a backward one starts on
	// its first cell.
	if !forward {
		next, ok := nextPos(buf, head)
		return ok && isWordCell(buf, head) && isWordCell(buf, next)
	}
	last, ok := prevPos(buf, head)
	if !ok {
		return false
	}
	prior, ok := prevPos(buf, last)
	return ok && isWordCell(buf, last) && isWordCell(buf, prior)
}

func isWordCell(buf *cell.Buffer, pos term.Coordinates) bool {
	ch, ok := charAt(buf, pos)
	return ok && categorizeChar(ch) == catWord
}

// skipToWord trims the whitespace a word motion swept up so the label
// lands on the word itself.
func skipToWord(buf *cell.Buffer, pos term.Coordinates, forward bool) term.Coordinates {
	for {
		probe, ok := pos, true
		if !forward {
			probe, ok = prevPos(buf, pos)
		}
		if !ok || isWordCell(buf, probe) {
			return pos
		}
		next := probe
		if forward {
			if next, ok = nextPos(buf, pos); !ok {
				return pos
			}
		}
		pos = next
	}
}

// extendedJumpAnchor keeps whichever end of the live selection [from, to)
// is further from the label, so extend_to_word grows the range instead
// of replacing it.
func extendedJumpAnchor(buf *cell.Buffer, from, to term.Coordinates, target jumpRange) term.Coordinates {
	if target.backward() {
		to = docPos(buf, to)
		if coordinatesBefore(to, target.anchor) {
			return target.anchor
		}
		return to
	}
	if coordinatesBefore(target.anchor, from) {
		return target.anchor
	}
	return from
}

func drawJumpLabels(w term.Writer, scroll *component.Scroll, labels []jumpRange) {
	for i, label := range labels {
		first := label.from()
		second, ok := nextPos(scroll.Buffer(), first)
		if !ok {
			continue
		}
		drawJumpLabel(w, scroll, first, jumpAlphabet[i/len(jumpAlphabet)])
		drawJumpLabel(w, scroll, second, jumpAlphabet[i%len(jumpAlphabet)])
	}
}

func drawJumpLabel(w term.Writer, scroll *component.Scroll, pos term.Coordinates, ch rune) {
	at, ok := scroll.ScrollToWindowCoordinates(pos)
	if !ok || at.X < 0 || at.X >= scroll.Width() {
		return
	}
	w.SetCell(at, term.NewCell(ch, 1, jumpLabelAttr))
}
