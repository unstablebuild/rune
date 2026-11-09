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
	"image/color"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/braille"
	"unstable.build/rune/internal/component/shader"
)

var bloomRed = color.RGBA{R: 255, A: 255}

func newBloom(fallback shader.Shader, arts ...image.Rectangle) shader.Shader {
	return shader.BrailleBloom(shader.DefaultBrailleBloomParams(),
		term.Attributes{Fg: term.ColorWhite, Bg: term.NewRGBColor(0, 0, 0)},
		blockWallpaperDots(solidifyRed, nil, arts...), fallback)
}

func shadeBloom(
	sh shader.Shader, frame int, live [][]term.Cell, images ...term.Image,
) ([][]term.Cell, []term.Image) {
	cells := cloneCells(live)
	return cells, sh.(shader.ImageShader).ShadeImages(frame, brailleTotal, cells, slices.Clone(images))
}

// dotAlpha returns the opacity of picture next to the center of dot dx,
// dy of the cell at x, y.
func dotAlpha(picture term.Image, x, y, dx, dy int) uint8 {
	px := (x-picture.Pos.X)*pictureCellW + (2*dx+1)*pictureCellW/braille.CellDotsX/2
	py := (y-picture.Pos.Y)*pictureCellH + (2*dy+1)*pictureCellH/braille.CellDotsY/2
	return picture.Src.(*image.RGBA).RGBAAt(px, py).A
}

func opaquePixels(picture term.Image) int {
	n := 0
	pix := picture.Src.(*image.RGBA).Pix
	for i := 3; i < len(pix); i += 4 {
		if pix[i] > 0 {
			n++
		}
	}
	return n
}

func TestBrailleBloom_StartsOnTheBrailleArtAndEndsOnThePicture(t *testing.T) {
	arts := []image.Rectangle{image.Rect(10, 2, 14, 6), image.Rect(4, 7, 9, 10)}
	live, picture := pictureWallpaper(24, 12, bloomRed, arts...)
	swapped := shadeBraille(shader.BrailleSwap(blockWallpaperDots(solidifyRed, nil, arts...)), 0, live)
	sh := newBloom(nil, arts...)

	cells, images := shadeBloom(sh, 0, live, picture)
	assert.Equal(t, swapped, cells)
	require.Len(t, images, 1)
	assert.Equal(t, picture.Bounds(), images[0].Bounds())
	assert.Zero(t, opaquePixels(images[0]), "no dot bloomed")

	bloomed := 0
	for frame := 1; frame < brailleTotal-1; frame++ {
		_, images := shadeBloom(sh, frame, live, picture)
		require.Len(t, images, 1, "frame %d", frame)
		require.NotEqual(t, picture.ID, images[0].ID, "frame %d: the picture is blooming", frame)
		require.GreaterOrEqual(t, opaquePixels(images[0]), bloomed, "frame %d: the picture fades", frame)
		bloomed = opaquePixels(images[0])
	}
	assert.Equal(t, opaquePixels(picture), bloomed, "the picture fully bloomed")

	cells, images = shadeBloom(sh, brailleTotal-1, live, picture)
	assert.Equal(t, live, cells)
	assert.Equal(t, []term.Image{picture}, images)
}

func TestBrailleBloom_ShowsEveryDotAsBrailleOrAsPicture(t *testing.T) {
	arts := []image.Rectangle{image.Rect(10, 2, 14, 6), image.Rect(4, 7, 9, 10)}
	live, picture := pictureWallpaper(24, 12, bloomRed, arts...)
	art := drawCells(blockWallpaperDots(solidifyRed, nil, arts...), 24, 11)
	sh := newBloom(nil, arts...)

	for frame := range brailleTotal - 1 {
		cells, images := shadeBloom(sh, frame, live, picture)
		for y := 1; y < 12; y++ {
			for x := range 24 {
				for dy := range braille.CellDotsY {
					for dx := range braille.CellDotsX {
						bit := braille.Bit(dx, dy)
						if art[y-1][x].Ch&bit == 0 {
							continue
						}
						braille := cells[y][x].Ch&bit != 0
						pictured := dotAlpha(images[0], x, y, dx, dy) > 0
						require.NotEqual(t, braille, pictured,
							"frame %d: dot %d,%d of cell %d,%d is braille %t and picture %t",
							frame, dx, dy, x, y, braille, pictured)
					}
				}
			}
		}
	}
}

func TestBrailleBloom_SpreadsDownTheArtFromItsTop(t *testing.T) {
	art := image.Rect(10, 1, 13, 11)
	live, picture := pictureWallpaper(24, 12, bloomRed, art)
	sh := newBloom(nil, art)
	top := image.Rect(art.Min.X, art.Min.Y, art.Max.X, art.Min.Y+1)
	bottom := image.Rect(art.Min.X, art.Max.Y-1, art.Max.X, art.Max.Y)

	topFirst := false
	for frame := range brailleTotal {
		cells, _ := shadeBloom(sh, frame, live, picture)
		require.False(t, settled(cells, live, bottom, false) && !settled(cells, live, top, false),
			"frame %d: the bottom bloomed before the top", frame)
		topFirst = topFirst || settled(cells, live, top, false) && settled(cells, live, bottom, true)
	}
	assert.True(t, topFirst, "the top never bloomed while the bottom was still braille")
}

func TestBrailleBloom_JumpsGapsOnlyAfterCrossingTheArt(t *testing.T) {
	// Crossing the gap is far shorter than following the upper art to
	// its end.
	upper := image.Rect(2, 1, 14, 2)
	lower := image.Rect(2, 3, 5, 5)
	live, picture := pictureWallpaper(24, 12, bloomRed, upper, lower)
	sh := newBloom(nil, upper, lower)

	inOrder := false
	for frame := range brailleTotal {
		cells, _ := shadeBloom(sh, frame, live, picture)
		inOrder = inOrder || settled(cells, live, upper, false) && settled(cells, live, lower, true)
	}
	assert.True(t, inOrder, "the upper art never bloomed before the lower one started")
}

func TestBrailleBloom_GlowsAsTheFrontPasses(t *testing.T) {
	art := image.Rect(10, 2, 14, 9)
	live, picture := pictureWallpaper(24, 12, bloomRed, art)
	sh := newBloom(nil, art)

	warmed, flashed := false, false
	for frame := range brailleTotal {
		cells, images := shadeBloom(sh, frame, live, picture)
		for _, row := range cells {
			for _, c := range row {
				if _, g, _ := c.Fg.RGB(); g > 0 && braille.IsPattern(c.Ch) && c.Ch != braille.Blank {
					warmed = true
				}
			}
		}
		for _, img := range images {
			pix := img.Src.(*image.RGBA).Pix
			for i := 1; i < len(pix); i += 4 {
				flashed = flashed || pix[i] > 128
			}
		}
	}
	assert.True(t, warmed, "no dot warmed up before blooming")
	assert.True(t, flashed, "no dot flashed as it bloomed")
}

func TestBrailleBloom_OnlyChangesTheWallpaper(t *testing.T) {
	art := image.Rect(10, 2, 14, 9)
	live, picture := pictureWallpaper(24, 12, bloomRed, art)
	upper, lower := picture, picture
	upper.Clip = image.Rect(art.Min.X, art.Min.Y, art.Max.X+1, 5)
	lower.Clip = image.Rect(art.Min.X, 5, art.Max.X+1, art.Max.Y)
	other := term.Image{
		Src: image.NewRGBA(image.Rect(0, 0, 1, 1)), ID: term.NewImageID(),
		Pos: term.Coordinates{X: 1}, Width: 3, Height: 2,
	}
	sh := newBloom(nil, art)

	for frame := range brailleTotal - 1 {
		cells, images := shadeBloom(sh, frame, live, upper, other, lower)
		for y, row := range cells {
			for x, c := range row {
				want := live[y][x]
				if want.Ch != braille.Blank {
					require.Equal(t, want, c, "frame %d cell %d,%d", frame, x, y)
					continue
				}
				require.Equal(t, want.Bg, c.Bg, "frame %d cell %d,%d", frame, x, y)
			}
		}
		require.Len(t, images, 3, "frame %d", frame)
		assert.Equal(t, other, images[1], "frame %d", frame)
		for i, piece := range []term.Image{upper, lower} {
			got := images[2*i]
			assert.Equal(t, piece.Visible(), got.Visible(), "frame %d", frame)
			assert.Equal(t, images[0].Src, got.Src, "frame %d", frame)
			assert.Equal(t, images[0].ID, got.ID, "frame %d", frame)
		}
	}
}

func TestBrailleBloom_FallsBackWithoutAPicture(t *testing.T) {
	art := image.Rect(10, 2, 14, 9)
	live := blockWallpaper(24, 12, solidifyRed, art)
	sh := newBloom(newSolidify(art), art)
	want := newSolidify(art)

	for frame := range brailleTotal {
		cells, images := shadeBloom(sh, frame, live)
		require.Equal(t, shadeBraille(want, frame, live), cells, "frame %d", frame)
		require.Empty(t, images, "frame %d", frame)
	}
	assert.Equal(t, live, shadeBraille(newBloom(nil, art), brailleTotal/2, live),
		"without a fallback, the screen is left as drawn")
}

func TestBrailleBloom_LeavesScreensWithoutBlankBrailleUntouched(t *testing.T) {
	live := makeCharCells(20, 8)
	_, picture := pictureWallpaper(20, 8, bloomRed, image.Rect(2, 2, 5, 5))
	sh := newBloom(newSolidify(image.Rect(2, 2, 5, 5)), image.Rect(2, 2, 5, 5))
	for frame := range brailleTotal {
		cells, images := shadeBloom(sh, frame, live, picture)
		require.Equal(t, live, cells, "frame %d", frame)
		require.Equal(t, []term.Image{picture}, images, "frame %d", frame)
	}
}

func TestBrailleBloom_FollowsThePictureAsItChanges(t *testing.T) {
	art := image.Rect(10, 2, 14, 9)
	live, picture := pictureWallpaper(24, 12, bloomRed, art)
	_, blue := pictureWallpaper(24, 12, color.RGBA{B: 255, A: 255}, art)
	blue.ID, blue.Version = picture.ID, picture.Version+1
	sh := newBloom(nil, art)

	shadeBloom(sh, brailleTotal-2, live, picture)
	_, images := shadeBloom(sh, brailleTotal-2, live, blue)
	require.Len(t, images, 1)
	assert.Equal(t, opaquePixels(blue), opaquePixels(images[0]))
	pix := images[0].Src.(*image.RGBA).Pix
	for i := 0; i < len(pix); i += 4 {
		require.Zero(t, pix[i], "pixel %d is still red", i/4)
	}
}

func TestBrailleBloom_ReplaysTheSameSeed(t *testing.T) {
	art := image.Rect(4, 2, 18, 10)
	live, picture := pictureWallpaper(24, 12, bloomRed, art)
	params := shader.DefaultBrailleBloomParams()
	dots := blockWallpaperDots(solidifyRed, nil, art)
	a := shader.BrailleBloom(params, term.Attributes{}, dots, nil)
	b := shader.BrailleBloom(params, term.Attributes{}, dots, nil)
	params.Seed++
	other := shader.BrailleBloom(params, term.Attributes{}, dots, nil)
	pixels := func(images []term.Image) []uint8 {
		return slices.Clone(images[0].Src.(*image.RGBA).Pix)
	}

	differs := false
	for frame := range brailleTotal - 1 {
		wantCells, wantImages := shadeBloom(a, frame, live, picture)
		gotCells, gotImages := shadeBloom(b, frame, live, picture)
		require.Equal(t, wantCells, gotCells, "frame %d", frame)
		require.Equal(t, pixels(wantImages), pixels(gotImages), "frame %d", frame)
		otherCells, otherImages := shadeBloom(other, frame, live, picture)
		differs = differs || !assert.ObjectsAreEqual(wantCells, otherCells) ||
			!assert.ObjectsAreEqual(pixels(wantImages), pixels(otherImages))
	}
	assert.True(t, differs, "another seed replays the same animation")
}
