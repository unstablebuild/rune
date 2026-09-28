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
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// gauge is a fixed-width field that reads as a label with a colour
// boundary marking how full it is. The fill is carried by the cell
// attributes rather than by a texture, so the label stays legible on
// both sides of the boundary and the track reads as one flat surface.
//
// fill is a ramp of stops laid across the whole field rather than
// across the part the gauge reaches, so a cell keeps its colour as the
// gauge grows and how far along the ramp the boundary sits is itself
// the reading. Cells between two stops blend them, so no two cells of
// a wide gauge land on the same colour.
type gauge struct {
	label string
	ratio float64
	width int
	fill  []term.Attributes
	empty term.Attributes
	// startRune and endRune bracket the track, taking a cell each out
	// of width so the field keeps the size the layout budgeted. A zero
	// rune leaves that end of the track flush with the bar.
	startRune rune
	endRune   rune
	capAttr   term.Attributes
}

// Dimensions satisfies component.Floating.
func (g *gauge) Dimensions() (width, height int) {
	if g.width <= 0 {
		return 0, 0
	}
	return g.width, 1
}

// Resize satisfies tui.Component. The gauge always occupies its
// configured width.
func (g *gauge) Resize(width, height int) {}

// Draw satisfies tui.Component.
func (g *gauge) Draw(w term.Writer) {
	if g.width <= 0 {
		return
	}
	left, right := 0, g.width
	if g.startRune != 0 {
		w.SetCell(term.Coordinates{X: left},
			term.NewCell(g.startRune, 1, g.capAttr))
		left++
	}
	if g.endRune != 0 {
		right--
		w.SetCell(term.Coordinates{X: right},
			term.NewCell(g.endRune, 1, g.capAttr))
	}
	track := right - left
	if track <= 0 {
		return
	}

	label := []rune(g.label)
	if len(label) > track {
		label = label[:track]
	}
	start := (track - len(label)) / 2

	ratio := math.Max(0, math.Min(1, g.ratio))
	boundary := int(math.Round(ratio * float64(track)))

	for x := range track {
		attrs := g.empty
		if x < boundary {
			attrs = g.fillAt(x, track)
		}
		r := ' '
		if x >= start && x-start < len(label) {
			r = label[x-start]
		}
		w.SetCell(term.Coordinates{X: left + x}, term.NewCell(r, 1, attrs))
	}
}

// fillAt returns the ramp colour for cell x of an n-cell track.
func (g *gauge) fillAt(x, n int) term.Attributes {
	switch {
	case len(g.fill) == 0:
		return g.empty
	case len(g.fill) == 1 || n <= 1:
		return g.fill[0]
	}
	pos := float64(x) / float64(n-1) * float64(len(g.fill)-1)
	i := int(pos)
	if i >= len(g.fill)-1 {
		return g.fill[len(g.fill)-1]
	}
	from, to := g.fill[i], g.fill[i+1]
	return term.Attributes{
		Fg:    lerpColor(from.Fg, to.Fg, pos-float64(i)),
		Bg:    lerpColor(from.Bg, to.Bg, pos-float64(i)),
		Attrs: from.Attrs,
	}
}

// lerpColor mixes two colours in RGB. A colour the terminal resolves
// itself has no channels to mix, so such a pair keeps the start colour
// instead of flipping halfway between two stops.
func lerpColor(from, to term.Color, t float64) term.Color {
	if t == 0 || from == to || !from.Valid() || !to.Valid() {
		return from
	}
	fr, fg, fb := from.RGB()
	tr, tg, tb := to.RGB()
	return term.NewRGBColor(
		lerpChannel(fr, tr, t),
		lerpChannel(fg, tg, t),
		lerpChannel(fb, tb, t),
	)
}

func lerpChannel(from, to int32, t float64) int32 {
	return int32(math.Round(float64(from) + (float64(to)-float64(from))*t))
}
