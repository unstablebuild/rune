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
	"context"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// Position mapping, from ChangeSet::map_pos in
// helix-core/src/transaction.rs. An operation over one range edits the
// buffer through text.Cursor, which knows nothing about the other
// ranges, so every edit is recorded and the untouched ranges are
// carried through the recording afterwards.

// assoc is Helix's Assoc: which side of an edit at exactly its position
// a mapped position sticks to.
type assoc uint8

const (
	assocBefore assoc = iota
	assocAfter
)

// change is one Edit call: the span [from, to) was replaced by text
// that now ends at end. A pure delete has end == from, a pure insert
// has to == from.
type change struct {
	from, to term.Coordinates
	end      term.Coordinates
}

// replacing describes an edit of [from, to) by text, with end derived
// by walking text from from. Every cell holds one rune here, which is
// true of the ASCII the callers build changes from.
func replacing(from, to term.Coordinates, text string) change {
	return change{from: from, to: to, end: advance(from, text)}
}

// advance returns where s ends when written at pos.
func advance(pos term.Coordinates, s string) term.Coordinates {
	for _, ch := range s {
		if ch == '\n' {
			pos = term.Coordinates{Y: pos.Y + 1}
			continue
		}
		pos.X++
	}
	return pos
}

// mapPos moves pos through the change. Inside the replaced span a
// position collapses to the start (before) or to the end of the new
// text (after); the start of a replaced span maps to itself for both,
// which is what keeps a range's from on the text that replaced it.
func (c change) mapPos(pos term.Coordinates, a assoc) term.Coordinates {
	if coordinatesBefore(pos, c.from) {
		return pos
	}
	inserted := c.end != c.from
	deleted := c.to != c.from
	switch {
	case pos == c.from && !deleted:
		if a == assocBefore {
			return c.from
		}
		return c.end
	case pos == c.from && deleted && inserted:
		return c.from
	case coordinatesBefore(pos, c.to):
		if a == assocBefore {
			return c.from
		}
		return c.end
	case pos.Y == c.to.Y:
		return term.Coordinates{Y: c.end.Y, X: c.end.X + pos.X - c.to.X}
	default:
		return term.Coordinates{Y: pos.Y + c.end.Y - c.to.Y, X: pos.X}
	}
}

// changeSet is the sequence of edits one operation made, in the order
// they were applied. Each later change is expressed in the coordinates
// left behind by the earlier ones, so mapping folds them in order.
type changeSet []change

func (cs changeSet) mapPos(pos term.Coordinates, a assoc) term.Coordinates {
	for _, c := range cs {
		pos = c.mapPos(pos, a)
	}
	return pos
}

// changeRecorder is a cell.Subscriber that captures every edit made
// while it is armed. It lives on the buffer rather than the scroll so
// both handler constructors see the same edits.
type changeRecorder struct {
	armed   bool
	changes changeSet
}

func (r *changeRecorder) arm() {
	r.armed = true
	r.changes = r.changes[:0]
}

// disarm stops recording and hands back what was captured. The slice
// is owned by the caller: the next arm starts a fresh one.
func (r *changeRecorder) disarm() changeSet {
	r.armed = false
	cs := r.changes
	r.changes = nil
	return cs
}

// OnWillEdit satisfies cell.Subscriber.
func (r *changeRecorder) OnWillEdit(_ context.Context, start, end term.Coordinates, _ string) {
	if !r.armed {
		return
	}
	from, to := term.CoordinatesSort(start, end)
	r.changes = append(r.changes, change{from: from, to: to, end: from})
}

// OnDidEdit satisfies cell.Subscriber. The buffer reports where the
// inserted text really ended, which is the only reliable measure when
// a cell can hold a whole grapheme cluster.
func (r *changeRecorder) OnDidEdit(_ context.Context, _, to term.Coordinates, _ string) {
	if !r.armed || len(r.changes) == 0 {
		return
	}
	r.changes[len(r.changes)-1].end = to
}
