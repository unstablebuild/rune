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

package gui

import (
	"image/color"
	"strconv"
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/benchdraw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/gui/drawtext"
	"unstable.build/rune/internal/term/gui/font"
)

func TestRendererDeallocateReleasesGraphicsStorage(t *testing.T) {
	r := &renderer{
		frame:  ebiten.NewImage(32, 32),
		drawer: drawtext.New(),
	}

	r.deallocate()

	assert.Nil(t, r.frame)
	assert.Nil(t, r.drawer)
	assert.NotPanics(t, r.deallocate)
}

func TestRendererCursorColorsOnMatchingBackground(t *testing.T) {
	red := color.RGBA{R: 202, G: 124, B: 94, A: 255}
	black := color.RGBA{A: 255}
	redCell := term.NewColor(202, 124, 94)
	creamCell := term.NewColor(255, 255, 234)
	cursorAttr := term.Attributes{Bg: redCell}
	defaultAttr := term.Attributes{Fg: term.NewColor(0, 0, 0), Bg: creamCell}
	manager, err := font.NewManager(1, 1)
	require.NoError(t, err)

	for _, opacity := range []float64{1, 0.9, 0} {
		t.Run(strconv.FormatFloat(opacity, 'f', 1, 64), func(t *testing.T) {
			textBlack := color.RGBA{A: uint8(255 * opacity)}
			for _, tt := range []struct {
				name           string
				cell           term.Cell
				defaults       term.Attributes
				wantFG, wantBG color.RGBA
			}{
				{"Pike error background", term.Cell{Bg: redCell}, defaultAttr, red, black},
				{"ordinary background", term.Cell{}, defaultAttr, textBlack, red},
				{"reversed matching background",
					term.Cell{Fg: redCell, Attrs: term.AttrReverse}, defaultAttr, red, black},
				{"reversed different background",
					term.Cell{Fg: creamCell, Bg: redCell, Attrs: term.AttrReverse},
					defaultAttr, textBlack, red},
				{"matching default background", term.Cell{}, term.Attributes{Bg: redCell}, red, black},
				{"reversed default foreground", term.Cell{Attrs: term.AttrReverse},
					term.Attributes{Fg: redCell}, red, black},
			} {
				t.Run(tt.name, func(t *testing.T) {
					r := newRenderer(20, 40, 1, manager, opacity, opacity, false,
						cursorAttr, tt.defaults)
					t.Cleanup(r.deallocate)
					fg, bg := r.cursorColors(tt.cell)
					assert.Equal(t, tt.wantFG, fg, "text inside the block cursor")
					assert.Equal(t, tt.wantBG, bg, "cursor fill")
				})
			}
		})
	}
}

func BenchmarkRenderLigaturesHD(b *testing.B) {
	benchmarkRenderLigatures(b, 1920, 1080)
}

func BenchmarkRenderLigaturesHD2(b *testing.B) {
	benchmarkRenderLigatures(b, 2560, 1440)
}

func BenchmarkRenderLigatures4k(b *testing.B) {
	benchmarkRenderLigatures(b, 3840, 2160)
}

func BenchmarkRendererDrawOpaqueHD(b *testing.B) {
	benchmarkRendererContent(b, 1920, 1080, 1)
}

func BenchmarkRendererDrawOpaqueHD2(b *testing.B) {
	benchmarkRendererContent(b, 2560, 1440, 1)
}

func BenchmarkRendererDrawOpaque4k(b *testing.B) {
	benchmarkRendererContent(b, 3840, 2160, 1)
}

func BenchmarkRendererDrawTransparentHD(b *testing.B) {
	benchmarkRendererContent(b, 1920, 1080, 0.5)
}

func BenchmarkRendererDrawTransparentHD2(b *testing.B) {
	benchmarkRendererContent(b, 2560, 1440, 0.5)
}

func BenchmarkRendererDrawTransparent4k(b *testing.B) {
	benchmarkRendererContent(b, 3840, 2160, 0.5)
}

func benchmarkRendererContent(
	b *testing.B, pixelsWidth, pixelsHeight int,
	opacity float64,
) {

	manager, err := font.NewManager(1, 1)
	if err != nil {
		b.Logf("new manager: %v", err)
		b.FailNow()
	}
	width := manager.CellsWidth(pixelsWidth)
	height := manager.CellsHeight(pixelsHeight)

	cells := make([][]term.Cell, height)
	for i := 0; i < height; i++ {
		cells[i] = make([]term.Cell, width)
		for j := 0; j < width; j++ {
			cells[i][j].Ch = []rune(strconv.Itoa(i))[0]
			cells[i][j].Fg = term.NewColor(255, 0, 255)
			cells[i][j].Bg = term.NewColor(0, 0, 255)
		}
	}
	var (
		doLigatures = false
		defAttr     = term.Attributes{}
		deviceScale = 1.0
	)
	image := ebiten.NewImage(pixelsWidth, pixelsHeight)
	r := newRenderer(pixelsWidth, pixelsHeight, deviceScale,
		manager, opacity, opacity, doLigatures, defAttr, defAttr)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchdraw.BeginFrame(b)
		r.Draw(image, cells, nil, true, term.Coordinates{X: 1, Y: 5},
			term.CursorStyleDefault)
		benchdraw.EndFrame(b)
	}
}

func benchmarkRenderLigatures(b *testing.B, pixelsWidth, pixelsHeight int) {
	manager, err := font.NewManager(1, 1)
	if err != nil {
		b.Logf("new manager: %v", err)
		b.FailNow()
	}
	width := manager.CellsWidth(pixelsWidth)
	height := manager.CellsHeight(pixelsHeight)

	cells := make([][]term.Cell, height)
	for i := 0; i < height; i++ {
		cells[i] = make([]term.Cell, width)
		for j := 0; j < width; j++ {
			if j%2 == 0 {
				cells[i][j].Ch = '='
			} else {
				cells[i][j].Ch = '>'
			}
		}
	}
	image := ebiten.NewImage(pixelsWidth, pixelsHeight)
	font := newFontFace(manager)
	colorBlack := color.RGBA{A: 255}
	drawer := drawtext.New()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			handleLigatures(drawer, cells, 4, 10, font.Regular, colorBlack, font, image)
		} else {
			handleLigatures(drawer, cells, 5, 10, font.Regular, colorBlack, font, image)
		}
	}
}
