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

package shader

import (
	"image"
	"slices"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// layer is what shows on a cell of a frame.
type layer uint8

const (
	// layerLive is a cell of the input, in place or moved.
	layerLive layer = iota
	// layerSnapshot is the cell of the snapshot a reveal starts from.
	layerSnapshot
	// layerEffect is what the shader draws of its own.
	layerEffect
)

// move is how far the cell of the input that a cell of a frame shows
// has moved to it, if it shows one.
type move struct {
	d  image.Point
	ok bool
}

// backdrop is the screen a reveal shader starts from. It shows on the
// cells the shader has not uncovered yet wherever the shader draws
// nothing of its own there, so that the effect plays in front of it.
// The pictures over the cells shaded go along with the cells as the
// shader moves them.
type backdrop struct {
	Snapshot
	bg term.Color
	// live is the input of the frame being shaded.
	live [][]term.Cell
	// tracking tells whether the frame being shaded keeps moves.
	tracking bool
	cols     int
	// moves and layers are where each cell of the frame being shaded
	// comes from and what shows on it, row by row.
	moves  []move
	layers []layer
	// given, regions, open and next are scratch space for cutting the
	// pictures.
	given      []term.Image
	regions    []region
	open, next map[run]int
}

// region is a rectangle of cells of a frame showing the same layer, the
// cells of the input it shows moved by d.
type region struct {
	r image.Rectangle
	l layer
	d image.Point
}

// run is a run of cells of a row of a frame showing the same layer, the
// cells of the input it shows moved by d.
type run struct {
	x0, x1 int
	l      layer
	d      image.Point
}

// newBackdrop returns the backdrop of from for a shader whose default
// attributes are defaultAttr.
func newBackdrop(from Snapshot, defaultAttr term.Attributes) backdrop {
	return backdrop{Snapshot: from.copied(defaultAttr.Fg), bg: defaultAttr.Bg}
}

// uncover tells when, as a progress through the animation, a reveal
// stops showing its backdrop on the cell at x, y.
type uncover func(x, y int) float64

// reveal shades cells, over which images are drawn, at frame of total
// with shade, and lays the backdrop behind the cells that shade tells
// are still covered. It returns the placements to draw over the frame:
// the snapshot's over the cells showing it, and images over the cells
// showing the input, moved along with them. shade is given the progress
// through the animation, and returns nil when it leaves the cells as
// they are. It moves glyphs with [backdrop.place], so that their
// pictures go along with them.
func (b *backdrop) reveal(
	frame, total int, cells [][]term.Cell, images []term.Image,
	shade func(t float64, cells [][]term.Cell) uncover,
) []term.Image {
	t, ok := progress(frame, total)
	if !ok {
		return images
	}
	rows, cols := canvasSize(cells)
	// The last frame shows the input.
	b.tracking = t < 1 && rows*cols > 0 && (len(b.Cells) > 0 || len(images) > 0)
	if !b.tracking {
		shade(t, cells)
		return images
	}
	defer func() { b.tracking = false }()
	b.live = copyCellsInto(b.live, cells)
	b.cols = cols
	b.moves = slices.Grow(b.moves[:0], rows*cols)[:rows*cols]
	for i := range b.moves {
		b.moves[i] = move{ok: true}
	}
	at := shade(t, cells)
	if at == nil {
		return images
	}
	b.layers = slices.Grow(b.layers[:0], rows*cols)[:rows*cols]
	shown, moved := [3]bool{}, false
	for y, row := range cells {
		layers := b.layers[y*cols : y*cols+len(row)]
		moves := b.moves[y*cols : y*cols+len(row)]
		for x, c := range row {
			m := moves[x]
			sx, sy := x-m.d.X, y-m.d.Y
			shows := m.ok && inCanvas(b.live, sx, sy) && sameContent(c, b.live[sy][sx], b.bg)
			inSnap := inCanvas(b.Cells, x, y)
			switch {
			// A wide character's continuation goes along with it.
			case c.Width == 0 && x > 0:
				layers[x], moves[x] = layers[x-1], moves[x-1]
				if layers[x] == layerSnapshot && !inSnap {
					layers[x] = layerEffect
				}
			// Moved text passes in front of the backdrop.
			case shows && m.d != (image.Point{}):
				layers[x] = layerLive
			case inSnap && (c == b.live[y][x] || !hasContent(c, b.bg)) && t <= at(x, y):
				layers[x] = layerSnapshot
			case shows:
				layers[x] = layerLive
			default:
				layers[x] = layerEffect
			}
			// The effect plays in front of the backdrop, which shows
			// through what it draws on the canvas background.
			if layers[x] == layerEffect && (c.Bg == b.bg || c.Bg == term.ColorDefault) &&
				inSnap && t <= at(x, y) {
				row[x].Bg = b.Cells[y][x].Bg
			}
			shown[layers[x]] = true
			moved = moved || layers[x] == layerLive && moves[x].d != (image.Point{})
		}
		if y < len(b.Cells) {
			paintSnapshot(row, b.Cells[y], layers)
		}
	}
	switch {
	case !shown[layerSnapshot] && !shown[layerEffect] && !moved:
		return images
	case !shown[layerLive] && !shown[layerEffect]:
		return append(images[:0], b.Images...)
	case len(images) == 0 && len(b.Images) == 0:
		return images
	}
	return b.cut(cells, images)
}

// place draws glyphs at places as [place] does, on the canvas of the
// backdrop, and has the pictures over the glyphs go along with them.
func (b *backdrop) place(cells [][]term.Cell, glyphs []glyph, places []placement) {
	place(cells, glyphs, places, b.bg)
	if !b.tracking {
		return
	}
	// Moved glyphs are drawn over the others in order, so the last one
	// drawn on a cell is the one it shows.
	for i, g := range glyphs {
		p := places[i]
		if p.hidden || p.ch != 0 || (p.x == g.x && p.y == g.y) {
			continue
		}
		for k := range max(1, int(g.cell.Width)) {
			if inCanvas(cells, p.x+k, p.y) {
				b.moves[p.y*b.cols+p.x+k] = move{d: image.Pt(p.x-g.x, p.y-g.y), ok: true}
			}
		}
	}
}

// shift has every cell of the frame being shaded show the cell of the
// input dy rows above it, and none when there is none, for a shader that
// moves the whole canvas down by dy rows. Glyphs placed after it with
// [backdrop.place] show where they come from.
func (b *backdrop) shift(dy int) {
	if !b.tracking {
		return
	}
	for i := range b.moves {
		b.moves[i] = move{d: image.Pt(0, dy), ok: i/b.cols >= dy}
	}
}

// cut returns the snapshot's placements cut to the cells of the frame
// showing it, and images cut to the cells of the frame showing the cells
// of the input under them, moved along with them. It reads all of images
// before it appends, so the two may share their backing array.
func (b *backdrop) cut(cells [][]term.Cell, images []term.Image) []term.Image {
	b.given = append(b.given[:0], images...)
	defer clear(b.given)
	regions := b.buildRegions(cells)
	out := images[:0]
	for _, img := range b.given {
		for _, r := range regions {
			if r.l != layerLive {
				continue
			}
			moved := img.Translated(term.Coordinates{X: r.d.X, Y: r.d.Y})
			if p, ok := moved.Clipped(r.r); ok {
				out = append(out, p)
			}
		}
	}
	for _, img := range b.Images {
		for _, r := range regions {
			if r.l != layerSnapshot {
				continue
			}
			if p, ok := img.Clipped(r.r); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

// buildRegions returns the regions of cells showing the input or the
// snapshot, merging the runs of a row with the run of the same columns,
// layer and move in the row above.
func (b *backdrop) buildRegions(cells [][]term.Cell) []region {
	if b.open == nil {
		b.open, b.next = map[run]int{}, map[run]int{}
	}
	clear(b.open)
	b.regions = b.regions[:0]
	key := func(i int) (layer, image.Point) {
		if b.layers[i] != layerLive {
			return b.layers[i], image.Point{}
		}
		return layerLive, b.moves[i].d
	}
	for y, row := range cells {
		clear(b.next)
		base := y * b.cols
		for x := 0; x < len(row); {
			l, d := key(base + x)
			end := x + 1
			for end < len(row) {
				if nl, nd := key(base + end); nl != l || nd != d {
					break
				}
				end++
			}
			if l != layerEffect {
				k := run{x0: x, x1: end, l: l, d: d}
				if j, ok := b.open[k]; ok {
					b.regions[j].r.Max.Y = y + 1
					b.next[k] = j
				} else {
					b.next[k] = len(b.regions)
					b.regions = append(b.regions, region{r: image.Rect(x, y, end, y+1), l: l, d: d})
				}
			}
			x = end
		}
		b.open, b.next = b.next, b.open
	}
	return b.regions
}

// sameContent tells whether a and b show the same thing on a canvas
// whose background is bg, whatever the color of their characters.
func sameContent(a, b term.Cell, bg term.Color) bool {
	ca, cb := hasContent(a, bg), hasContent(b, bg)
	if !ca || !cb {
		return ca == cb
	}
	return a.Ch == b.Ch && a.Width == b.Width && a.Extra == b.Extra && a.Bg == b.Bg
}

// paintSnapshot draws the cells of the snapshot's row snap over the
// cells of row that layers tells show it. A wide character of the
// snapshot only shows whole, and leaves a blank of its background where
// it cannot.
func paintSnapshot(row, snap []term.Cell, layers []layer) {
	for x := 0; x < len(row); x++ {
		if layers[x] != layerSnapshot {
			continue
		}
		s := snap[x]
		width := max(1, int(s.Width))
		fits := s.Width != 0 && x+width <= min(len(row), len(snap))
		for i := 1; fits && i < width; i++ {
			fits = layers[x+i] == layerSnapshot && snap[x+i].Width == 0
		}
		cells := [][]term.Cell{row}
		claim(cells, x, 0)
		if !fits {
			row[x] = term.Cell{Ch: ' ', Width: 1, Fg: s.Fg, Bg: s.Bg}
			continue
		}
		for i := range width {
			claim(cells, x+i, 0)
			row[x+i] = snap[x+i]
		}
		x += width - 1
	}
}

// copyCellsInto returns a copy of src, reusing the rows of dst.
func copyCellsInto(dst, src [][]term.Cell) [][]term.Cell {
	dst = slices.Grow(dst[:0], len(src))[:len(src)]
	for y, row := range src {
		dst[y] = append(dst[y][:0], row...)
	}
	return dst
}
