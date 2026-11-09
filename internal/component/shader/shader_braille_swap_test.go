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

package shader_test

import (
	"image"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/braille"
	"unstable.build/rune/internal/component/shader"
)

func TestBrailleSwap_DrawsTheBrailleArtOverTheWallpaper(t *testing.T) {
	red := term.NewRGBColor(255, 0, 0)
	art := image.Rect(10, 5, 14, 8)
	live := blockWallpaper(24, 12, red, art)
	sh := shader.BrailleSwap(blockWallpaperDots(red, nil, art))

	for _, frame := range []int{0, 50, brailleTotal - 1} {
		cells := shadeBraille(sh, frame, live)
		for y, row := range cells {
			for x, c := range row {
				want := live[y][x]
				p := image.Pt(x, y)
				switch {
				case !p.In(image.Rect(0, 1, 24, 12)) || p == image.Pt(2, 10):
				case p.In(art):
					want.Ch, want.Fg = '⣿', red
				case p.X == art.Max.X && p.Y >= art.Min.Y && p.Y < art.Max.Y:
					want.Ch, want.Fg = '⡇', red
				default:
					want.Ch, want.Fg = braille.Blank, term.ColorDefault
				}
				require.Equal(t, want, c, "frame %d cell %d,%d", frame, x, y)
			}
		}
	}
}

func TestBrailleSwap_LeavesTheCellsArtDrawsNoBrailleInAsDrawn(t *testing.T) {
	live := blockWallpaper(24, 12, term.NewRGBColor(255, 0, 0), image.Rect(10, 5, 14, 8))
	want := cloneCells(live)
	// The wallpaper starts below the text row.
	want[1][0].Ch = '⣿'
	assert.Equal(t, want, shadeBraille(shader.BrailleSwap(component.NewString("⣿")), 0, live))
}

func TestBrailleSwap_LeavesScreensWithoutBlankBrailleUntouched(t *testing.T) {
	live := makeCharCells(20, 8)
	live[3][3].Ch = braille.Block
	drawn := [][2]int{}
	sh := shader.BrailleSwap(blockWallpaperDots(term.ColorRed, &drawn, image.Rect(2, 2, 5, 5)))
	assert.Equal(t, live, shadeBraille(sh, 0, live))
	assert.Empty(t, drawn)

	images := []term.Image{{ID: 1, Pos: term.Coordinates{X: 2, Y: 2}, Width: 3, Height: 3}}
	got := sh.(shader.ImageShader).ShadeImages(0, brailleTotal, cloneCells(live), slices.Clone(images))
	assert.Equal(t, images, got)
}

func TestBrailleSwap_HidesTheWallpapersPictures(t *testing.T) {
	red := term.NewRGBColor(255, 0, 0)
	art := image.Rect(10, 5, 14, 8)
	live := blockWallpaper(24, 12, red, art)
	sh := shader.BrailleSwap(blockWallpaperDots(red, nil, art)).(shader.ImageShader)

	picture := term.Image{ID: 1, Pos: term.Coordinates{X: 9, Y: 4}, Width: 6, Height: 5}
	pictureCut := picture
	pictureCut.Clip = image.Rect(9, 4, 12, 9)
	overTheText := term.Image{ID: 2, Pos: term.Coordinates{X: 3}, Width: 4, Height: 3}
	onTheText := term.Image{ID: 3, Width: 4, Height: 1}

	got := sh.ShadeImages(0, brailleTotal, cloneCells(live),
		[]term.Image{picture, overTheText, pictureCut, onTheText})
	assert.Equal(t, []term.Image{overTheText, onTheText}, got)
}

func TestBrailleSwap_RedrawsTheArtOnlyWhenTheWallpaperResizes(t *testing.T) {
	red := term.NewRGBColor(255, 0, 0)
	drawn := [][2]int{}
	sh := shader.BrailleSwap(blockWallpaperDots(red, &drawn, image.Rect(3, 3, 6, 6)))
	small := blockWallpaper(24, 12, red, image.Rect(3, 3, 6, 6))
	large := blockWallpaper(40, 20, red, image.Rect(3, 3, 6, 6))

	for frame := range 3 {
		shadeBraille(sh, frame, small)
	}
	shadeBraille(sh, 3, large)
	shadeBraille(sh, 4, large)
	assert.Equal(t, [][2]int{{24, 11}, {40, 19}}, drawn)
}
