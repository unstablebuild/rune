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

package main

import (
	"context"
	"image"
	"math"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/braille"
	"unstable.build/rune/internal/component/shader"
)

func TestInitShaderAssemblesTheLogoThenSolidifiesIt(t *testing.T) {
	const w, h = 120, 40
	live := drawLogoSpan(logoImageBase, nil, braille.Blocks, w, h)
	dots := drawCells(logoDots(nil, nil), w, h)

	params := defaultIntroParams()
	sh := makeInitShader(params, logoDots(nil, nil))(term.Attributes{
		Fg: term.NewRGBColor(220, 220, 220),
		Bg: term.NewRGBColor(12, 12, 20),
	}, component.FrameCharSet{})
	total := int(initShaderDuration / (time.Second / initShaderFPS))
	shade := func(frame int) [][]term.Cell {
		cells := term.CloneCells(live)
		sh.Shade(frame, total, cells)
		return cells
	}
	solidifying := int(math.Round(float64(total) * params.Assembly))

	assert.NotEqual(t, dots, shade(0), "the intro starts with the logo scattered")
	assert.Equal(t, dots, shade(solidifying-1), "the dots assemble the logo")
	assert.Equal(t, dots, shade(solidifying), "the logo starts to solidify from its dots")
	mid := shade((solidifying + total) / 2)
	assert.NotEqual(t, dots, mid, "the logo is solidifying")
	assert.NotEqual(t, live, mid, "the logo is solidifying")
	require.Equal(t, live, shade(total-1), "the intro ends on the wallpaper as drawn")
}

func TestInitShaderAssemblesTheLogoThenBloomsIntoItsPicture(t *testing.T) {
	const w, h = 120, 40
	cellSize := func() (int, int) { return 9, 20 }
	wallpaper := makeThemedWallpaper(nil, cellSize).NewComponent()
	wallpaper.Resize(w, h)
	buf := cell.NewBufferWriter(
		cell.ContextWithPixelSize(context.Background(), cell.PixelSize{Width: 9, Height: 20}), w, h)
	wallpaper.Draw(buf)
	live, liveImages := buf.RawCells(), buf.Images()
	require.Len(t, liveImages, 1)
	dots := drawCells(logoDots(nil, cellSize), w, h)

	params := defaultIntroParams()
	sh := makeInitShader(params, logoDots(nil, cellSize))(term.Attributes{
		Fg: term.NewRGBColor(220, 220, 220),
		Bg: term.NewRGBColor(12, 12, 20),
	}, component.FrameCharSet{}).(shader.ImageShader)
	total := int(initShaderDuration / (time.Second / initShaderFPS))
	shade := func(frame int) ([][]term.Cell, []term.Image) {
		cells := term.CloneCells(live)
		images := sh.ShadeImages(frame, total, cells, slices.Clone(liveImages))
		return cells, images
	}
	opaque := func(img term.Image) int {
		n := 0
		pix := img.Src.(*image.RGBA).Pix
		for i := 3; i < len(pix); i += 4 {
			if pix[i] > 0 {
				n++
			}
		}
		return n
	}
	blooming := int(math.Round(float64(total) * params.Assembly))

	cells, images := shade(0)
	assert.NotEqual(t, dots, cells, "the intro starts with the logo scattered")
	assert.Empty(t, images, "the picture is hidden while the dots assemble")
	cells, images = shade(blooming - 1)
	assert.Equal(t, dots, cells, "the dots assemble the logo")
	assert.Empty(t, images, "the picture is hidden while the dots assemble")

	cells, images = shade(blooming)
	assert.Equal(t, dots, cells, "the logo starts to bloom from its dots")
	require.Len(t, images, 1)
	assert.Equal(t, liveImages[0].Bounds(), images[0].Bounds())
	assert.Zero(t, opaque(images[0]), "no dot bloomed yet")

	cells, images = shade((blooming + total) / 2)
	assert.NotEqual(t, dots, cells, "the dots are blooming")
	assert.NotEqual(t, live, cells, "the dots are blooming")
	require.Len(t, images, 1)
	assert.Positive(t, opaque(images[0]), "the picture is blooming")
	assert.Less(t, opaque(images[0]), opaque(liveImages[0]), "the picture is blooming")

	cells, images = shade(total - 1)
	assert.Equal(t, live, cells, "the intro ends on the wallpaper as drawn")
	assert.Equal(t, liveImages, images, "the intro ends on the wallpaper's picture")
}

func TestInitShaderDrawsTheDotsInNonSampleColorUnlessItSamplesThem(t *testing.T) {
	const w, h = 120, 40
	live := drawLogoSpan(logoImageBase, nil, braille.Blocks, w, h)
	total := int(initShaderDuration / (time.Second / initShaderFPS))
	nonSample := term.NewRGBColor(200, 30, 40)
	for _, sample := range []bool{true, false} {
		params := defaultIntroParams()
		params.SampleColor, params.NonSampleColor = sample, nonSample
		sh := makeInitShader(params, logoDots(nil, nil))(term.Attributes{
			Fg: term.NewRGBColor(220, 220, 220),
			Bg: term.NewRGBColor(12, 12, 20),
		}, component.FrameCharSet{})
		shade := func(frame int) [][]term.Cell {
			cells := term.CloneCells(live)
			sh.Shade(frame, total, cells)
			return cells
		}

		assembled := shade(int(math.Round(float64(total)*params.Assembly)) - 1)
		colors := map[term.Color]int{}
		for _, row := range assembled {
			for _, c := range row {
				if braille.IsPattern(c.Ch) && c.Ch != braille.Blank {
					colors[c.Fg]++
				}
			}
		}
		require.NotEmpty(t, colors, "sample %t: the dots assemble the logo", sample)
		if sample {
			assert.NotContains(t, colors, nonSample, "the dots take the logo's colors")
		} else {
			assert.Len(t, colors, 1, "every dot is NonSampleColor")
			assert.Contains(t, colors, nonSample)
		}
		assert.Equal(t, live, shade(total-1),
			"sample %t: the intro ends on the wallpaper as drawn", sample)
	}
}
