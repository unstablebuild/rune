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
	"unstable.build/rune/internal/cell"
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

// SelectionWriter is a term.Writer that captures cells and images for
// text selection: it can extract text and word/line bounds under a
// coordinate and blit the captured
// frame back out with a reverse-video selection overlay on its cells.
//
// It is a sibling of term.StringWriter (string rendering) and
// cell.BufferWriter (cell.Buffer bridge); unlike those, its purpose is
// to back mouse-driven selection over already-rendered content.
type SelectionWriter struct {
	frame cell.BufferWriter
	// cells aliases the frame so selection reads do not finalize placements
	// before later cell writes have had a chance to cover them.
	cells         [][]term.Cell
	width, height int
}

// NewSelectionWriter allocates a SelectionWriter sized to width x height.
func NewSelectionWriter(width, height int) *SelectionWriter {
	w := new(SelectionWriter)
	w.Resize(width, height)
	return w
}

// Resize discards the captured frame, retaining storage when the dimensions match.
func (g *SelectionWriter) Resize(width, height int) {
	if g.width == width && g.height == height {
		g.Clear()
		return
	}
	g.frame.Init(g.Context(), width, height)
	g.cells = g.frame.RawCells()
	g.width, g.height = width, height
}

// Clear discards all cells and images while preserving the current dimensions.
func (g *SelectionWriter) Clear() {
	_ = g.frame.Clear(term.Attributes{})
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
	g.frame.SetCell(pos, cell)
}

// UnionAttributes satisfies term.Writer. Out-of-bounds writes are ignored.
func (g *SelectionWriter) UnionAttributes(pos term.Coordinates, attr term.Attributes) {
	g.frame.UnionAttributes(pos, attr)
}

// DrawImage retains the placement for Dump, clipped to the grid and any cells
// written over it later. Images do not contribute selectable text.
func (g *SelectionWriter) DrawImage(img term.Image) bool {
	return g.frame.DrawImage(img)
}

// Context satisfies term.Writer, returning the context set via
// SetContext or context.Background when none was set.
func (g *SelectionWriter) Context() context.Context {
	if ctx := g.frame.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// SetContext sets the context returned by Context. Capturing handlers
// set this to the writer context of the in-flight Draw so downstream
// components observe the same context.
func (g *SelectionWriter) SetContext(ctx context.Context) {
	g.frame.SetContext(ctx)
}

// Dump copies the captured frame to w. When sel is non-nil and active, cells
// within the selection have AttrReverse applied via UnionAttributes.
// Null cells are emitted as a single space so the output is opaque.
func (g *SelectionWriter) Dump(w term.Writer, sel *SelRange) {
	images := g.frame.Images()
	reverseAttr := term.Attributes{Attrs: term.AttrReverse}
	for y := range g.height {
		for x := range g.width {
			pos := term.Coordinates{X: x, Y: y}
			cell := g.cells[y][x]
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
	// Blitting the opaque cell grid after the images would cover their pixels.
	for _, img := range images {
		w.DrawImage(img)
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
			cell := g.cells[y][x]
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
	cell := g.cells[pos.Y][pos.X]
	if cell.Ch == 0 || unicode.IsSpace(cell.Ch) {
		return
	}
	isWord := isWordChar(cell.Ch)

	startX := pos.X
	for startX > 0 {
		prev := g.cells[pos.Y][startX-1]
		if prev.Ch == 0 || unicode.IsSpace(prev.Ch) || isWordChar(prev.Ch) != isWord {
			break
		}
		startX--
	}
	endX := pos.X
	for endX < g.width-1 {
		next := g.cells[pos.Y][endX+1]
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
		if g.cells[y][x].Ch != 0 {
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
