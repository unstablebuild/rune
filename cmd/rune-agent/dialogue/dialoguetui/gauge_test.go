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

package dialoguetui

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// cellRecorder is a term.Writer that keeps the last cell written at
// each coordinate so tests can assert runes and attributes together.
type cellRecorder struct {
	cells map[term.Coordinates]term.Cell
}

func newCellRecorder() *cellRecorder {
	return &cellRecorder{cells: make(map[term.Coordinates]term.Cell)}
}

func (r *cellRecorder) Context() context.Context  { return context.Background() }
func (r *cellRecorder) DrawImage(term.Image) bool { return false }

func (r *cellRecorder) SetCell(pos term.Coordinates, c term.Cell) {
	r.cells[pos] = c
}

func (r *cellRecorder) UnionAttributes(pos term.Coordinates, attrs term.Attributes) {
	c := r.cells[pos]
	c.SetAttributes(term.AttributesUnion(c.Attributes(), attrs))
	r.cells[pos] = c
}

// row returns the runes written on row 0 up to width.
func (r *cellRecorder) row(width int) string {
	out := make([]rune, 0, width)
	for x := range width {
		c, ok := r.cells[term.Coordinates{X: x}]
		if !ok {
			out = append(out, '\x00')
			continue
		}
		out = append(out, c.Ch)
	}
	return string(out)
}

var (
	testGaugeFill  = term.Attributes{Fg: term.ColorBlack, Bg: term.ColorAqua}
	testGaugeEmpty = term.Attributes{Fg: term.ColorGray}
)

func TestGaugeDraw(t *testing.T) {
	tests := []struct {
		name     string
		label    string
		ratio    float64
		width    int
		wantRow  string
		boundary int
	}{
		{
			name: "empty", label: "0%", ratio: 0, width: 8,
			wantRow: "   0%   ", boundary: 0,
		},
		{
			name: "half", label: "50%", ratio: 0.5, width: 8,
			wantRow: "  50%   ", boundary: 4,
		},
		{
			name: "full", label: "100%", ratio: 1, width: 8,
			wantRow: "  100%  ", boundary: 8,
		},
		{
			name: "ratio below range clamps to empty", label: "x", ratio: -3, width: 4,
			wantRow: " x  ", boundary: 0,
		},
		{
			name: "ratio above range clamps to full", label: "x", ratio: 7, width: 4,
			wantRow: " x  ", boundary: 4,
		},
		{
			name: "odd width centres left of middle", label: "ab", ratio: 0.5, width: 5,
			wantRow: " ab  ", boundary: 3,
		},
		{
			name: "label wider than field is clipped", label: "abcdef", ratio: 0.5, width: 3,
			wantRow: "abc", boundary: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &gauge{
				label: tt.label, ratio: tt.ratio, width: tt.width,
				fill: []term.Attributes{testGaugeFill}, empty: testGaugeEmpty,
			}
			w, h := g.Dimensions()
			assert.Equal(t, tt.width, w)
			assert.Equal(t, 1, h)

			rec := newCellRecorder()
			g.Draw(rec)
			require.Equal(t, tt.wantRow, rec.row(tt.width))

			for x := range tt.width {
				want := testGaugeEmpty
				if x < tt.boundary {
					want = testGaugeFill
				}
				assert.Equal(t, want, rec.cells[term.Coordinates{X: x}].Attributes(),
					"cell %d", x)
			}
		})
	}
}

func TestGaugeZeroWidth(t *testing.T) {
	g := &gauge{
		label: "x", ratio: 0.5,
		fill: []term.Attributes{testGaugeFill}, empty: testGaugeEmpty,
	}
	w, h := g.Dimensions()
	assert.Zero(t, w)
	assert.Zero(t, h)

	rec := newCellRecorder()
	g.Draw(rec)
	assert.Empty(t, rec.cells)
}

// The ramp is laid across the whole field, not across the part the
// gauge reaches, so a cell keeps its colour as the gauge grows. Cells
// between two stops blend them, so a wide gauge reads as a ramp rather
// than as a few blocks.
func TestGaugeFillRampsBetweenItsStops(t *testing.T) {
	const width = 6
	stops := []term.Attributes{
		{Bg: term.ColorGreen}, {Bg: term.ColorOlive}, {Bg: term.ColorRed},
	}
	newGauge := func(ratio float64) *gauge {
		return &gauge{
			ratio: ratio, width: width, empty: testGaugeEmpty, fill: stops,
		}
	}

	rec := newCellRecorder()
	newGauge(1).Draw(rec)
	bgs := make([]term.Color, width)
	for x := range width {
		bgs[x] = rec.cells[term.Coordinates{X: x}].Bg
	}

	assert.Equal(t, term.ColorGreen, bgs[0], "the ramp starts on its first stop")
	assert.Equal(t, term.ColorRed, bgs[width-1], "the ramp ends on its last")
	for x := 1; x < width; x++ {
		assert.NotEqual(t, bgs[x-1], bgs[x], "cell %d repeats its neighbour", x)
	}

	part := newCellRecorder()
	newGauge(0.5).Draw(part)
	for x := range 3 {
		assert.Equal(t, bgs[x], part.cells[term.Coordinates{X: x}].Bg,
			"cell %d changed colour as the gauge grew", x)
	}
}

// The brackets come out of the field the layout budgeted rather than
// widening it, so a capped gauge still measures its configured width.
func TestGaugeCapsTakeCellsFromTheField(t *testing.T) {
	capAttr := term.Attributes{Fg: term.ColorYellow, Bg: term.ColorGray}
	g := &gauge{
		label: "50%", ratio: 0.5, width: 8, empty: testGaugeEmpty,
		fill:      []term.Attributes{testGaugeFill},
		startRune: '╟', endRune: '╢', capAttr: capAttr,
	}
	w, _ := g.Dimensions()
	assert.Equal(t, 8, w)

	rec := newCellRecorder()
	g.Draw(rec)
	assert.Equal(t, "╟ 50%  ╢", rec.row(8))
	assert.Equal(t, capAttr, rec.cells[term.Coordinates{X: 0}].Attributes())
	assert.Equal(t, capAttr, rec.cells[term.Coordinates{X: 7}].Attributes())

	// The ratio measures the track between the brackets, not the field.
	for x := 1; x <= 3; x++ {
		assert.Equal(t, testGaugeFill, rec.cells[term.Coordinates{X: x}].Attributes(),
			"cell %d", x)
	}
	for x := 4; x <= 6; x++ {
		assert.Equal(t, testGaugeEmpty, rec.cells[term.Coordinates{X: x}].Attributes(),
			"cell %d", x)
	}
}
