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
	"context"
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

func hatchWriter(cellHeight float64) *cell.BufferWriter {
	ctx := cell.ContextWithPixelSize(context.Background(), cell.PixelSize{Width: 10, Height: cellHeight})
	return cell.NewBufferWriter(ctx, 20, 10)
}

func TestHatchDraw(t *testing.T) {
	tests := []struct {
		name  string
		style HatchStyle
		w     *cell.BufferWriter
		area  image.Rectangle
		// want are the cells the stripes are placed over, none when empty.
		want       image.Rectangle
		wantPeriod int
		wantColor  term.Color
	}{
		{
			name: "tiles the area below its glyphs",
			w:    hatchWriter(40), area: image.Rect(2, 3, 12, 10),
			want: image.Rect(2, 3, 12, 10), wantPeriod: 20, wantColor: term.ColorSilver,
		},
		{
			name: "spaces the stripes by the cell height",
			w:    hatchWriter(24), area: image.Rect(0, 0, 20, 10),
			want: image.Rect(0, 0, 20, 10), wantPeriod: 12, wantColor: term.ColorSilver,
		},
		{
			name: "keeps the stripes apart on tiny cells",
			w:    hatchWriter(5), area: image.Rect(0, 0, 20, 10),
			want: image.Rect(0, 0, 20, 10), wantPeriod: minHatchPeriod, wantColor: term.ColorSilver,
		},
		{
			name:  "draws the style's color and period",
			style: HatchStyle{Color: term.ColorBlue, Period: 0.25},
			w:     hatchWriter(40), area: image.Rect(0, 0, 20, 10),
			want: image.Rect(0, 0, 20, 10), wantPeriod: 10, wantColor: term.ColorBlue,
		},
		{
			name:  "draws the default period for one that is not positive",
			style: HatchStyle{Period: -1},
			w:     hatchWriter(40), area: image.Rect(0, 0, 20, 10),
			want: image.Rect(0, 0, 20, 10), wantPeriod: 20, wantColor: term.ColorSilver,
		},
		{name: "draws nothing over an empty area", w: hatchWriter(40), area: image.Rect(0, 10, 20, 10)},
		{
			name: "draws nothing where cells have no pixel size",
			w:    cell.NewBufferWriter(context.Background(), 20, 10), area: image.Rect(0, 0, 20, 10),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Hatch{Style: tt.style}
			h.Draw(tt.w, tt.area)
			images := tt.w.Images()
			if tt.want.Empty() {
				assert.Empty(t, images)
				return
			}
			require.Len(t, images, 1)
			img := images[0]
			assert.Equal(t, tt.want, img.Bounds())
			assert.Equal(t, term.ImageFitTile, img.Fit)
			assert.Equal(t, term.ImageLayerBelowText, img.Layer)
			assert.Equal(t, hatchTile(tt.wantPeriod, tt.wantColor), img.Src)
		})
	}
}

func TestHatchKeepsItsTileWhileTheCellHeightAndStyleHold(t *testing.T) {
	var h Hatch
	draw := func(cellHeight float64) term.Image {
		w := hatchWriter(cellHeight)
		h.Draw(w, image.Rect(0, 0, 20, 10))
		require.Len(t, w.Images(), 1)
		return w.Images()[0]
	}
	first := draw(40)
	again := draw(40)
	assert.Same(t, first.Src, again.Src, "the tile is reused")
	assert.Equal(t, first.Version, again.Version, "the tile is not uploaded again")

	h.Style.Color = term.ColorBlue
	recolored := draw(40)
	assert.Equal(t, first.ID, recolored.ID, "a new color keeps the picture")
	assert.NotEqual(t, again.Version, recolored.Version, "the recolored tile is uploaded")
	assert.Equal(t, hatchTile(20, term.ColorBlue), recolored.Src)

	resized := draw(20)
	assert.Equal(t, first.ID, resized.ID, "a new font keeps the picture")
	assert.NotEqual(t, recolored.Version, resized.Version, "the new tile is uploaded")
	assert.Equal(t, image.Rect(0, 0, 10, 10), resized.Src.Bounds())
}

func TestHatchTile(t *testing.T) {
	shade := uint8(27) // silver, 192, at hatchAlpha, premultiplied
	for _, period := range []int{minHatchPeriod, 7, 16} {
		tile := hatchTile(period, term.ColorSilver)
		at := func(x, y int) color.RGBA {
			return tile.RGBAAt((x+period)%period, (y+period)%period)
		}
		for y := range period {
			for x := range period {
				assert.Equal(t, at(x, y), at(x+1, y-1),
					"period %d: a stripe runs up and right across the tile's edges from %d,%d", period, x, y)
				c := at(x, y)
				assert.True(t, c.R == c.G && c.G == c.B && c.A <= hatchAlpha,
					"period %d: %d,%d is a faint gray, got %v", period, x, y, c)
			}
		}
		if period == 16 {
			assert.Equal(t, color.RGBA{R: shade, G: shade, B: shade, A: hatchAlpha}, at(1, 0), "inside a stripe")
			assert.Equal(t, color.RGBA{}, at(8, 0), "between stripes")
		}
	}
	red := hatchTile(16, term.NewRGBColor(255, 0, 0)).RGBAAt(1, 0)
	assert.Equal(t, color.RGBA{R: hatchAlpha, A: hatchAlpha}, red, "the stripes take the color given")
}
