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
	"slices"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

// Helix's selection model, from helix-core/src/selection.rs: a sorted
// set of non-overlapping (anchor, head) ranges in document space with
// one of them primary. head is the exclusive edge, so a one-cell range
// under a block cursor is (c, c+1).

// rng is one Helix Range.
type rng struct {
	anchor term.Coordinates
	head   term.Coordinates

	// col is Range::old_visual_position: the column a vertical motion
	// set out from, kept so a run of j/k presses across short lines
	// comes back to it. Zero means unset; a desired column of zero
	// never needs remembering because clamping only ever moves left.
	col int
}

func point(pos term.Coordinates) rng { return rng{anchor: pos, head: pos} }

func (r rng) backward() bool { return coordinatesBefore(r.head, r.anchor) }

func (r rng) isPoint() bool { return r.anchor == r.head }

// same reports whether two ranges cover the same span in the same
// direction; the remembered column is not part of it.
func (r rng) same(o rng) bool { return r.anchor == o.anchor && r.head == o.head }

func (r rng) from() term.Coordinates {
	if r.backward() {
		return r.head
	}
	return r.anchor
}

func (r rng) to() term.Coordinates {
	if r.backward() {
		return r.anchor
	}
	return r.head
}

// forward is Range::with_direction(Direction::Forward).
func (r rng) forward() rng {
	if r.backward() {
		return r.flipped()
	}
	return r
}

// withDirection is Range::with_direction.
func (r rng) withDirection(backward bool) rng {
	if r.backward() != backward && !r.isPoint() {
		return r.flipped()
	}
	return r
}

// flipped is Range::flip.
func (r rng) flipped() rng { return rng{anchor: r.head, head: r.anchor, col: r.col} }

// lineRange is Range::line_range: the inclusive rows the range
// touches, where a range ending at a row start does not touch that
// row.
func (r rng) lineRange(buf *cell.Buffer) (first, last int) {
	from, to := r.from(), r.to()
	if !r.isPoint() {
		if prev, ok := prevPos(buf, to); ok && !coordinatesBefore(prev, from) {
			to = prev
		}
	}
	rows := buf.Rows()
	return min(from.Y, max(rows-1, 0)), min(to.Y, max(rows-1, 0))
}

// cursor is Range::cursor: the block cursor sits on the last cell a
// forward range covers and on the head otherwise.
func (r rng) cursor(buf *cell.Buffer) term.Coordinates {
	if !coordinatesBefore(r.anchor, r.head) {
		return r.head
	}
	prev, ok := prevPos(buf, r.head)
	if !ok {
		return r.head
	}
	return prev
}

// putCursor is Range::put_cursor. With extend set the anchor survives,
// shifting by one cell when the range flips direction; otherwise the
// result is a point on target, which ensure_invariants widens to one
// cell.
func (r rng) putCursor(buf *cell.Buffer, target term.Coordinates, extend bool) rng {
	if !extend {
		return point(target)
	}
	anchor := r.anchor
	switch {
	case !r.backward() && coordinatesBefore(target, anchor):
		if next, ok := nextPos(buf, anchor); ok {
			anchor = next
		}
	case r.backward() && !coordinatesBefore(target, anchor):
		if prev, ok := prevPos(buf, anchor); ok {
			anchor = prev
		}
	}
	if !coordinatesBefore(target, anchor) {
		next, ok := nextPos(buf, target)
		if !ok {
			next = target
		}
		return rng{anchor: anchor, head: next}
	}
	return rng{anchor: anchor, head: target}
}

// overlaps is Range::overlaps. Two points at the same position overlap,
// which is what keeps collapsed cursors from piling up.
func (r rng) overlaps(o rng) bool {
	return r.from() == o.from() ||
		(coordinatesBefore(o.from(), r.to()) && coordinatesBefore(r.from(), o.to()))
}

// merged is Range::merge: the union keeps the direction only when both
// ranges face backwards.
func (r rng) merged(o rng) rng {
	if r.backward() && o.backward() {
		return rng{
			anchor: maxCoordinates(r.anchor, o.anchor),
			head:   minCoordinates(r.head, o.head),
		}
	}
	return rng{
		anchor: minCoordinates(r.from(), o.from()),
		head:   maxCoordinates(r.to(), o.to()),
	}
}

// mapThrough is Range::map: the from end tracks the text after an
// edit at its position, the to end tracks the text before it, and a
// point follows the text on both ends.
func (r rng) mapThrough(cs changeSet) rng {
	if len(cs) == 0 {
		return r
	}
	r.col = 0
	switch {
	case r.isPoint():
		r.anchor = cs.mapPos(r.anchor, assocAfter)
		r.head = cs.mapPos(r.head, assocAfter)
	case r.backward():
		r.head = cs.mapPos(r.head, assocAfter)
		r.anchor = cs.mapPos(r.anchor, assocBefore)
	default:
		r.anchor = cs.mapPos(r.anchor, assocAfter)
		r.head = cs.mapPos(r.head, assocBefore)
	}
	return r
}

func minCoordinates(a, b term.Coordinates) term.Coordinates {
	if coordinatesBefore(b, a) {
		return b
	}
	return a
}

func maxCoordinates(a, b term.Coordinates) term.Coordinates {
	if coordinatesBefore(a, b) {
		return b
	}
	return a
}

// selection is Helix's Selection. Every method returns a new value;
// the ranges slice is never shared with the receiver after a mutation.
type selection struct {
	ranges  []rng
	primary int
}

func single(r rng) selection {
	return selection{ranges: []rng{r}}
}

func (s selection) primaryRange() rng { return s.ranges[s.primary] }

func (s selection) len() int { return len(s.ranges) }

// equal is selection equality up to the remembered columns.
func (s selection) equal(o selection) bool {
	if s.primary != o.primary || len(s.ranges) != len(o.ranges) {
		return false
	}
	for i := range s.ranges {
		if !s.ranges[i].same(o.ranges[i]) {
			return false
		}
	}
	return true
}

func (s selection) clone() selection {
	return selection{ranges: slices.Clone(s.ranges), primary: s.primary}
}

// normalized is Selection::normalize: sort by from, merge overlapping
// neighbours, and keep the primary pointing at whichever merged range
// swallowed it.
func (s selection) normalized() selection {
	if len(s.ranges) < 2 {
		return s
	}
	order := make([]int, len(s.ranges))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		fa, fb := s.ranges[a].from(), s.ranges[b].from()
		switch {
		case coordinatesBefore(fa, fb):
			return -1
		case coordinatesBefore(fb, fa):
			return 1
		}
		return 0
	})

	out := selection{ranges: make([]rng, 0, len(s.ranges))}
	for _, i := range order {
		r := s.ranges[i]
		if n := len(out.ranges); n > 0 && out.ranges[n-1].overlaps(r) {
			out.ranges[n-1] = out.ranges[n-1].merged(r)
			if i == s.primary {
				out.primary = n - 1
			}
			continue
		}
		if i == s.primary {
			out.primary = len(out.ranges)
		}
		out.ranges = append(out.ranges, r)
	}
	return out
}

// transform is Selection::transform.
func (s selection) transform(fn func(rng) rng) selection {
	out := selection{ranges: make([]rng, len(s.ranges)), primary: s.primary}
	for i, r := range s.ranges {
		out.ranges[i] = fn(r)
	}
	return out.normalized()
}

// transformEach is Selection::transform_iter: a range may become any
// number of ranges, or none. The primary index is not preserved, which
// is what Helix does for every command built on it.
func (s selection) transformEach(fn func(rng) []rng) (selection, bool) {
	out := selection{ranges: make([]rng, 0, len(s.ranges))}
	for _, r := range s.ranges {
		out.ranges = append(out.ranges, fn(r)...)
	}
	if len(out.ranges) == 0 {
		return s, false
	}
	return out.normalized(), true
}

// mapThrough is Selection::map.
func (s selection) mapThrough(cs changeSet) selection {
	if len(cs) == 0 {
		return s
	}
	return s.transform(func(r rng) rng { return r.mapThrough(cs) })
}

// with is Selection::push: the new range becomes primary.
func (s selection) with(r rng) selection {
	out := selection{ranges: make([]rng, 0, len(s.ranges)+1)}
	out.ranges = append(out.ranges, s.ranges...)
	out.ranges = append(out.ranges, r)
	out.primary = len(out.ranges) - 1
	return out.normalized()
}

// without is Selection::remove. The last range cannot be removed.
func (s selection) without(i int) selection {
	if len(s.ranges) < 2 {
		return s
	}
	out := selection{ranges: make([]rng, 0, len(s.ranges)-1), primary: s.primary}
	out.ranges = append(out.ranges, s.ranges[:i]...)
	out.ranges = append(out.ranges, s.ranges[i+1:]...)
	if i < out.primary || out.primary == len(out.ranges) {
		out.primary--
	}
	return out
}

// replace is Selection::replace.
func (s selection) replace(i int, r rng) selection {
	out := s.clone()
	out.ranges[i] = r
	return out.normalized()
}

// rotated moves the primary n ranges along, wrapping at either end.
func (s selection) rotated(n int) selection {
	if len(s.ranges) < 2 {
		return s
	}
	l := len(s.ranges)
	s.primary = ((s.primary+n)%l + l) % l
	return s
}

// merged is Selection::merge_ranges: one range from the first to the
// last.
func (s selection) merged() selection {
	return single(s.ranges[0].merged(s.ranges[len(s.ranges)-1]))
}

// mergedConsecutive is Selection::merge_consecutive_ranges: ranges
// that touch are joined, and the primary follows whichever join
// swallowed it.
func (s selection) mergedConsecutive() selection {
	if len(s.ranges) < 2 {
		return s
	}
	out := selection{ranges: make([]rng, 0, len(s.ranges))}
	for i, r := range s.ranges {
		if n := len(out.ranges); n > 0 && out.ranges[n-1].to() == r.from() {
			out.ranges[n-1] = r.merged(out.ranges[n-1])
			if i == s.primary {
				out.primary = n - 1
			}
			continue
		}
		if i == s.primary {
			out.primary = len(out.ranges)
		}
		out.ranges = append(out.ranges, r)
	}
	return out
}

// splitOnNewline is selection::split_on_newline: every range becomes
// one range per line it covers, without the line endings.
func (s selection) splitOnNewline(buf *cell.Buffer) selection {
	out, _ := s.transformEach(func(r rng) []rng {
		if r.isPoint() {
			return []rng{r}
		}
		from, to := r.from(), r.to()
		var parts []rng
		start := from
		for y := from.Y; y < to.Y && y < buf.Rows(); y++ {
			parts = append(parts, rng{anchor: start, head: term.Coordinates{X: buf.Columns(y), Y: y}})
			start = term.Coordinates{Y: y + 1}
		}
		if coordinatesBefore(start, to) {
			parts = append(parts, rng{anchor: start, head: to})
		}
		return parts
	})
	return out
}

// fragments returns the text under every range in document order.
func (s selection) fragments(buf *cell.Buffer) []string {
	out := make([]string, len(s.ranges))
	for i, r := range s.ranges {
		out[i] = rangeText(buf, r)
	}
	return out
}

// rangeText is the document text of [r.from(), r.to()), with a
// newline for every line ending the range crosses. The position past
// the last row is the end of the document, not a line ending, except
// for a linewise range, which owns the line ending of its last line
// whether or not the buffer stores one, as the cursor's own linewise
// selection does.
func rangeText(buf *cell.Buffer, r rng) string {
	from, to := r.from(), r.to()
	cells := buf.View().RawCells()
	linewise := lineShaped(r)
	var b strings.Builder
	for y := from.Y; y <= to.Y && y < len(cells); y++ {
		row := cells[y]
		start, end := 0, len(row)
		if y == from.Y {
			start = min(from.X, len(row))
		}
		if y == to.Y {
			end = min(to.X, len(row))
		}
		for _, c := range row[start:end] {
			b.WriteRune(c.Ch)
			for _, comb := range c.CombiningRunes() {
				b.WriteRune(comb)
			}
		}
		if y < to.Y && (y+1 < len(cells) || linewise) {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
