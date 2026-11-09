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
	"context"
	"image"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/braille"
)

// BrailleSwap is a Shader that draws art, the braille rendering of the
// wallpaper, in its place, such as for [BrailleAssemble] to animate. It
// is an [ImageShader] that hides the wallpaper's pictures.
func BrailleSwap(art tui.Component) Shader {
	return &brailleSwap{art: brailleArtCache{art: art}}
}

type brailleSwap struct {
	art brailleArtCache
}

func (s *brailleSwap) Shade(frame, total int, cells [][]term.Cell) {
	s.ShadeImages(frame, total, cells, nil)
}

func (s *brailleSwap) ShadeImages(
	frame, total int, cells [][]term.Cell, images []term.Image,
) []term.Image {
	if frame < 0 || frame >= total {
		return images
	}
	region, art, ok := s.art.find(cells)
	if !ok {
		return images
	}
	for y := region.Min.Y; y < region.Max.Y && y < len(cells); y++ {
		for x := region.Min.X; x < region.Max.X && x < len(cells[y]); x++ {
			c := &cells[y][x]
			a, ok := brailleArtCell(art, x-region.Min.X, y-region.Min.Y)
			if !ok || !isBrailleWallpaper(*c) {
				continue
			}
			c.Ch, c.Fg = a.Ch, a.Fg
		}
	}
	kept := images[:0]
	for _, img := range images {
		if !isWallpaperPicture(img, region) {
			kept = append(kept, img)
		}
	}
	return kept
}

// brailleArtCache finds the wallpaper on screen and draws its braille
// rendering, redrawing it only when the wallpaper's area changes.
type brailleArtCache struct {
	art    tui.Component
	region image.Rectangle
	cells  [][]term.Cell
}

// find returns the area of the wallpaper on screen and its braille
// rendering, or false when no wallpaper is on screen.
func (a *brailleArtCache) find(cells [][]term.Cell) (image.Rectangle, [][]term.Cell, bool) {
	region, ok := blankBrailleBounds(cells)
	if !ok || a.art == nil {
		return image.Rectangle{}, nil, false
	}
	if region != a.region || a.cells == nil {
		a.region = region
		a.art.Resize(region.Dx(), region.Dy())
		w := cell.NewBufferWriter(context.Background(), region.Dx(), region.Dy())
		a.art.Draw(w)
		a.cells = w.RawCells()
	}
	return region, a.cells, true
}

// brailleArtCell returns the cell of art at x, y, reporting false when
// art draws no braille there.
func brailleArtCell(art [][]term.Cell, x, y int) (term.Cell, bool) {
	if y < 0 || y >= len(art) || x < 0 || x >= len(art[y]) || !braille.IsPattern(art[y][x].Ch) {
		return term.Cell{}, false
	}
	return art[y][x], true
}

func isBrailleWallpaper(c term.Cell) bool {
	return c.Ch == braille.Blank || c.Ch == braille.Block
}

// isWallpaperPicture reports whether img is a picture of the wallpaper
// whose area is region.
func isWallpaperPicture(img term.Image, region image.Rectangle) bool {
	return img.Visible().In(region)
}
