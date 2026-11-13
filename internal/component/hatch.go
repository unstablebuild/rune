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

package component

import (
	"image"
	"image/color"
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

const (
	// DefaultHatchColor is the color a HatchStyle without one draws with.
	DefaultHatchColor = term.ColorSilver
	// DefaultHatchPeriod is the period a HatchStyle without one draws with.
	DefaultHatchPeriod = 0.5

	// hatchAlpha keeps the stripes faint on dark and light backgrounds
	// alike, as they only mark an area that holds nothing.
	hatchAlpha = 36
	// minHatchPeriod keeps stripes from merging on tiny cells.
	minHatchPeriod = 4
	// hatchStripe is the stripes' horizontal width in periods.
	hatchStripe = 0.2
	// hatchSamples is how many samples a side of a tile pixel takes to
	// antialias the stripes' edges.
	hatchSamples = 4
)

// HatchStyle selects the stripes a Hatch draws. The zero value selects
// the default ones.
type HatchStyle struct {
	// Color is drawn faint, so the stripes stay in the background.
	// term.ColorDefault selects DefaultHatchColor.
	Color term.Color
	// Period is the distance between stripes in cell heights, so the
	// stripes keep their density as the font is resized. Zero or less
	// selects DefaultHatchPeriod. Stripes are at least 4 pixels apart.
	Period float64
}

// Hatch draws faint diagonal stripes over an area, such as the rows a
// Scroll leaves empty below its content, on writers whose context carries
// the size of a cell in pixels (see [cell.PixelSizeFromContext]), and
// nothing on other writers. A Hatch keeps the tile it draws the stripes
// with, so it is not safe for concurrent use. The zero value is ready to
// use.
type Hatch struct {
	Style HatchStyle

	id        term.ImageID
	version   uint64
	tile      *image.RGBA
	tileColor term.Color
}

// Draw draws the stripes over area, a cell rectangle of w, under any
// glyphs w holds there. The stripes start at area's top-left corner, so
// they move along with it.
func (h *Hatch) Draw(w term.Writer, area image.Rectangle) {
	size, ok := cell.PixelSizeFromContext(w.Context())
	if !ok || area.Empty() {
		return
	}
	c, period := h.Style.Color, h.Style.Period
	if c == term.ColorDefault {
		c = DefaultHatchColor
	}
	if period <= 0 {
		period = DefaultHatchPeriod
	}
	pixels := max(minHatchPeriod, int(math.Round(size.Height*period)))
	if h.tile == nil || h.tile.Rect.Dx() != pixels || h.tileColor != c {
		h.tile, h.tileColor = hatchTile(pixels, c), c
		h.version++
	}
	if h.id == 0 {
		h.id = term.NewImageID()
	}
	w.DrawImage(term.Image{
		Src:     h.tile,
		ID:      h.id,
		Version: h.version,
		Pos:     term.Coordinates{X: area.Min.X, Y: area.Min.Y},
		Width:   area.Dx(),
		Height:  area.Dy(),
		Fit:     term.ImageFitTile,
		Layer:   term.ImageLayerBelowText,
	})
}

// hatchTile returns a period by period tile of faint c "/" stripes that
// continue across its edges, so that its repeats draw unbroken lines.
func hatchTile(period int, c term.Color) *image.RGBA {
	tile := image.NewRGBA(image.Rect(0, 0, period, period))
	r, g, b := c.TrueColor().RGB()
	stripe := float64(period) * hatchStripe
	for y := range period {
		for x := range period {
			var covered int
			for j := range hatchSamples {
				for i := range hatchSamples {
					// Points on a "/" stripe share x+y, so the stripes are
					// the bands where it wraps around the period.
					d := float64(x+y) + float64(i+j+1)/hatchSamples
					if math.Mod(d, float64(period)) < stripe {
						covered++
					}
				}
			}
			a := hatchAlpha * float64(covered) / (hatchSamples * hatchSamples)
			premultiply := func(c int32) uint8 { return uint8(math.Round(float64(c) * a / 255)) }
			tile.SetRGBA(x, y, color.RGBA{
				R: premultiply(r), G: premultiply(g), B: premultiply(b), A: uint8(math.Round(a)),
			})
		}
	}
	return tile
}
