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

package term

import (
	"context"
	"strings"
	"unicode"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// SelRange is an inclusive, sorted selection coordinate pair over a
// SelectionWriter's cell grid (Start <= End in reading order).
type SelRange struct {
	Start, End term.Coordinates
	Active     bool
}

// Contains reports whether pos falls within the selection. An inactive
// range contains nothing.
func (s SelRange) Contains(pos term.Coordinates) bool {
	if !s.Active {
		return false
	}
	if pos.Y < s.Start.Y || pos.Y > s.End.Y {
		return false
	}
	if s.Start.Y == s.End.Y {
		return pos.X >= s.Start.X && pos.X <= s.End.X
	}
	if pos.Y == s.Start.Y {
		return pos.X >= s.Start.X
	}
	if pos.Y == s.End.Y {
		return pos.X <= s.End.X
	}
	return true
}

var _ term.Writer = (*SelectionWriter)(nil)

// SelectionWriter is a term.Writer that captures Draw output into a
// flat cell grid specialized for text selection: it can extract the
// text and word/line bounds under a coordinate and blit the captured
// cells back out with a reverse-video selection overlay.
//
// It is a sibling of term.StringWriter (string rendering) and
// cell.BufferWriter (cell.Buffer bridge); unlike those, its purpose is
// to back mouse-driven selection over already-rendered content.
type SelectionWriter struct {
	cells         []term.Cell
	width, height int
	ctx           context.Context
}

// NewSelectionWriter allocates a SelectionWriter sized to width x height.
func NewSelectionWriter(width, height int) *SelectionWriter {
	w := new(SelectionWriter)
	w.Resize(width, height)
	return w
}

// Resize reallocates the grid to width x height and clears it, reusing
// the backing array when it is large enough.
func (g *SelectionWriter) Resize(width, height int) {
	needed := width * height
	if cap(g.cells) >= needed {
		g.cells = g.cells[:needed]
	} else {
		g.cells = make([]term.Cell, needed)
	}
	g.width, g.height = width, height
	g.Clear()
}

// Clear zeroes every cell while preserving the current dimensions.
func (g *SelectionWriter) Clear() {
	clear(g.cells)
}

// Width returns the grid width in cells.
func (g *SelectionWriter) Width() int { return g.width }

// Height returns the grid height in cells.
func (g *SelectionWriter) Height() int { return g.height }

func (g *SelectionWriter) outOfBounds(pos term.Coordinates) bool {
	return pos.X < 0 || pos.Y < 0 || pos.X >= g.width || pos.Y >= g.height
}

// SetCell satisfies term.Writer. Out-of-bounds writes are ignored.
func (g *SelectionWriter) SetCell(pos term.Coordinates, cell term.Cell) {
	if g.outOfBounds(pos) {
		return
	}
	g.cells[pos.Y*g.width+pos.X] = cell
}

// UnionAttributes satisfies term.Writer. Out-of-bounds writes are ignored.
func (g *SelectionWriter) UnionAttributes(pos term.Coordinates, attr term.Attributes) {
	if g.outOfBounds(pos) {
		return
	}
	idx := pos.Y*g.width + pos.X
	g.cells[idx].SetAttributes(term.AttributesUnion(g.cells[idx].Attributes(), attr))
}

// DrawImage satisfies term.Writer. The selection grid carries cells
// only, so callers draw a cell-based fallback instead.
func (g *SelectionWriter) DrawImage(term.Image) bool {
	return false
}

// Context satisfies term.Writer, returning the context set via
// SetContext or context.Background when none was set.
func (g *SelectionWriter) Context() context.Context {
	if g.ctx != nil {
		return g.ctx
	}
	return context.Background()
}

// SetContext sets the context returned by Context. Capturing handlers
// set this to the writer context of the in-flight Draw so downstream
// components observe the same context.
func (g *SelectionWriter) SetContext(ctx context.Context) {
	g.ctx = ctx
}

// Dump copies all cells to w. When sel is non-nil and active, cells
// within the selection have AttrReverse applied via UnionAttributes.
// Null cells are emitted as a single space so the output is opaque.
func (g *SelectionWriter) Dump(w term.Writer, sel *SelRange) {
	reverseAttr := term.Attributes{Attrs: term.AttrReverse}
	for y := range g.height {
		for x := range g.width {
			pos := term.Coordinates{X: x, Y: y}
			cell := g.cells[y*g.width+x]
			if cell.Ch == 0 {
				cell.Ch = ' '
				cell.Width = 1
			}
			w.SetCell(pos, cell)
			if sel != nil && sel.Contains(pos) {
				w.UnionAttributes(pos, reverseAttr)
			}
		}
	}
}

// TextBetween extracts text from cells between start and end
// (inclusive). Trailing whitespace on each row and trailing empty rows
// are trimmed; null cells are skipped.
func (g *SelectionWriter) TextBetween(start, end term.Coordinates) string {
	start, end = term.CoordinatesSort(start, end)
	var lines []string
	for y := start.Y; y <= end.Y; y++ {
		if y < 0 || y >= g.height {
			continue
		}
		startX := 0
		endX := g.width - 1
		if y == start.Y {
			startX = start.X
		}
		if y == end.Y {
			endX = end.X
		}
		startX = max(startX, 0)
		if endX >= g.width {
			endX = g.width - 1
		}
		var line strings.Builder
		for x := startX; x <= endX; x++ {
			cell := g.cells[y*g.width+x]
			if cell.Ch == 0 {
				continue
			}
			line.WriteRune(cell.Ch)
			for _, r := range cell.CombiningRunes() {
				line.WriteRune(r)
			}
		}
		lines = append(lines, strings.TrimRight(line.String(), " \t"))
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// WordBoundsAt returns the inclusive start and end of the word at pos.
// A word is a maximal run of same-class characters (word vs symbol),
// where word characters are letters, digits and underscore. ok is
// false when pos is out of bounds, empty, or whitespace.
func (g *SelectionWriter) WordBoundsAt(pos term.Coordinates) (start, end term.Coordinates, ok bool) {
	if g.outOfBounds(pos) {
		return
	}
	cell := g.cells[pos.Y*g.width+pos.X]
	if cell.Ch == 0 || unicode.IsSpace(cell.Ch) {
		return
	}
	isWord := isWordChar(cell.Ch)

	startX := pos.X
	for startX > 0 {
		prev := g.cells[pos.Y*g.width+startX-1]
		if prev.Ch == 0 || unicode.IsSpace(prev.Ch) || isWordChar(prev.Ch) != isWord {
			break
		}
		startX--
	}
	endX := pos.X
	for endX < g.width-1 {
		next := g.cells[pos.Y*g.width+endX+1]
		if next.Ch == 0 || unicode.IsSpace(next.Ch) || isWordChar(next.Ch) != isWord {
			break
		}
		endX++
	}
	return term.Coordinates{X: startX, Y: pos.Y}, term.Coordinates{X: endX, Y: pos.Y}, true
}

// LineBounds returns the inclusive start and end of the non-empty
// content on row y. ok is false when the row is empty or out of bounds.
func (g *SelectionWriter) LineBounds(y int) (start, end term.Coordinates, ok bool) {
	if y < 0 || y >= g.height {
		return
	}
	firstX := -1
	lastX := -1
	for x := range g.width {
		if g.cells[y*g.width+x].Ch != 0 {
			if firstX == -1 {
				firstX = x
			}
			lastX = x
		}
	}
	if firstX == -1 {
		return
	}
	return term.Coordinates{X: firstX, Y: y}, term.Coordinates{X: lastX, Y: y}, true
}

func isWordChar(ch rune) bool {
	return unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_'
}
