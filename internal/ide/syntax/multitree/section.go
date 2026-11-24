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

package multitree

import (
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/syntax"
)

// section is a region of the host buffer parsed by its own tree, which runs
// on a private copy of the region's text.
type section struct {
	lang string
	// rng is the region in host coordinates, End exclusive.
	rng  term.Range
	buf  *cell.Buffer
	tree syntax.Tree
	// hl is the tree's last highlight list, in section coordinates.
	hl []textapi.Location
	// dirty is set once an edit crossed one of the region's boundaries,
	// leaving rng and buf out of step with the host until reconciled.
	dirty bool
}

// snapshot copies what callers need to address the section's tree once
// Tree.mu is released.
func (s *section) snapshot() sectionSnapshot {
	return sectionSnapshot{tree: s.tree, rng: s.rng}
}

// toLocal maps a host position within the section to the section buffer.
func (s *section) toLocal(p term.Coordinates) term.Coordinates {
	return toLocal(s.rng.Start, p)
}

// toHost maps a section buffer position to the host buffer.
func (s *section) toHost(p term.Coordinates) term.Coordinates {
	return toHost(s.rng.Start, p)
}

// contains reports whether r lies within the section, boundaries included.
func (s *section) contains(r term.Range) bool {
	return rangeContains(s.rng, r)
}

// sectionSnapshot is a section's tree and the region it covered when the
// snapshot was taken.
type sectionSnapshot struct {
	tree syntax.Tree
	rng  term.Range
}

func (s *sectionSnapshot) toLocal(p term.Coordinates) term.Coordinates {
	return toLocal(s.rng.Start, p)
}

func (s *sectionSnapshot) toHost(p term.Coordinates) term.Coordinates {
	return toHost(s.rng.Start, p)
}

func (s *sectionSnapshot) rangeToLocal(r term.Range) term.Range {
	return term.Range{Start: s.toLocal(r.Start), End: s.toLocal(r.End)}
}

func (s *sectionSnapshot) rangeToHost(r term.Range) term.Range {
	return term.Range{Start: s.toHost(r.Start), End: s.toHost(r.End)}
}

func (s *sectionSnapshot) contains(r term.Range) bool {
	return rangeContains(s.rng, r)
}

// toLocal maps p, a host position at or after start, to the coordinates of
// a buffer holding the text from start.
func toLocal(start, p term.Coordinates) term.Coordinates {
	if p.Y == start.Y {
		p.X -= start.X
	}
	p.Y -= start.Y
	return p
}

// toHost is the inverse of toLocal.
func toHost(start, p term.Coordinates) term.Coordinates {
	if p.Y == 0 {
		p.X += start.X
	}
	p.Y += start.Y
	return p
}

// shift moves p, a position at or after oldEnd, the way an edit whose
// replaced text ended at oldEnd and whose inserted text ends at newEnd
// moves it.
func shift(p, oldEnd, newEnd term.Coordinates) term.Coordinates {
	if p.Y == oldEnd.Y {
		p.X += newEnd.X - oldEnd.X
		p.Y = newEnd.Y
		return p
	}
	p.Y += newEnd.Y - oldEnd.Y
	return p
}

func coordinatesLess(a, b term.Coordinates) bool {
	if a.Y != b.Y {
		return a.Y < b.Y
	}
	return a.X < b.X
}

func coordinatesLessOrEqual(a, b term.Coordinates) bool {
	return !coordinatesLess(b, a)
}

func coordinatesMax(a, b term.Coordinates) term.Coordinates {
	if coordinatesLess(a, b) {
		return b
	}
	return a
}

func rangeContains(outer, inner term.Range) bool {
	return coordinatesLessOrEqual(outer.Start, inner.Start) &&
		coordinatesLessOrEqual(inner.End, outer.End)
}

// rangesOverlap reports whether a and b share at least one position.
func rangesOverlap(a, b term.Range) bool {
	return coordinatesLess(a.Start, b.End) && coordinatesLess(b.Start, a.End)
}

func sortRange(r term.Range) term.Range {
	r.Start, r.End = term.CoordinatesSort(r.Start, r.End)
	return r
}
