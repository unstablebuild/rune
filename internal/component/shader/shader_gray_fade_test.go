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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
)

func TestGrayFade_FirstFrameLeavesCellsIntact(t *testing.T) {
	sh := shader.GrayFade(shader.DefaultGrayFadeParams(), term.Attributes{})
	in := makeCharCells(4, 2)
	in[0][0].Fg = term.NewRGBColor(255, 0, 0)
	want := cloneCells(in)

	sh.Shade(0, 10, in)

	assert.Equal(t, want, in, "frame 0 must not alter any cell")
	assert.Equal(t, 0.0, sh.Progress())
}

func TestGrayFade_HoldsAtFullDesaturationAfterFadeFrames(t *testing.T) {
	// The loading shader must NOT regain color if addWorkspace takes
	// longer than the configured fade window. After FadeFrames it must
	// keep Progress() pinned at 1.0 so the open shader can chain from
	// the same fully-desaturated state.
	params := shader.DefaultGrayFadeParams()
	params.FadeFrames = 5
	sh := shader.GrayFade(params, term.Attributes{})

	// total is intentionally much larger than FadeFrames; the shader
	// runner uses an oversized total so the Component never expires.
	const total = 10_000

	// At the end of the fade window: full desaturation.
	in := makeCharCells(2, 1)
	in[0][0].Fg = term.NewRGBColor(255, 0, 0)
	sh.Shade(params.FadeFrames-1, total, in)
	assert.Equal(t, 1.0, sh.Progress())
	r1, g1, b1 := in[0][0].Fg.RGB()
	assert.Equal(t, r1, g1, "Fg must be fully desaturated at end of fade")
	assert.Equal(t, r1, b1)

	// Many frames later: still fully desaturated, no recolor.
	in = makeCharCells(2, 1)
	in[0][0].Fg = term.NewRGBColor(255, 0, 0)
	sh.Shade(500, total, in)
	assert.Equal(t, 1.0, sh.Progress(),
		"Progress must stay at 1.0 past FadeFrames")
	r2, g2, b2 := in[0][0].Fg.RGB()
	assert.Equal(t, r1, r2, "color must not drift after the fade completes")
	assert.Equal(t, r2, g2)
	assert.Equal(t, r2, b2)
}

func TestGrayFade_LastFrameFullyDesaturatesFgAndBg(t *testing.T) {
	// The loading shader must finish at Progress()==1 so the open shader
	// can pick up exactly where it left off.
	params := shader.DefaultGrayFadeParams()
	params.FadeFrames = 10
	sh := shader.GrayFade(params, term.Attributes{})
	in := makeCharCells(2, 1)
	in[0][0].Fg = term.NewRGBColor(255, 0, 0)
	in[0][0].Bg = term.NewRGBColor(10, 20, 30)

	sh.Shade(params.FadeFrames-1, 10_000, in)

	assert.Equal(t, 1.0, sh.Progress())
	assert.Equal(t, term.NewRGBColor(76, 76, 76), in[0][0].Fg)
	assert.Equal(t, term.NewRGBColor(18, 18, 18), in[0][0].Bg)
}

func TestGrayFade_DesaturatesBackgrounds(t *testing.T) {
	themed := term.Attributes{Fg: term.NewRGBColor(200, 200, 200), Bg: term.NewRGBColor(40, 20, 80)}
	canvas := func(c term.Color) []term.Image {
		return []term.Image{{
			Fill: true, FillColor: c, Width: 2, Height: 1, Layer: term.ImageLayerBelowBackground,
		}}
	}
	tests := []struct {
		name        string
		defaultAttr term.Attributes
		frame       int
		bg          term.Color
		attrs       term.AttrMask
		wantBg      term.Color
		wantImages  []term.Image
	}{
		{
			name: "explicit", defaultAttr: themed, frame: 2,
			bg:         term.NewRGBColor(10, 20, 30),
			wantBg:     term.NewRGBColor(18, 18, 18),
			wantImages: canvas(term.NewRGBColor(33, 33, 33)),
		},
		{
			name: "explicit half way", defaultAttr: themed, frame: 1,
			bg:         term.NewRGBColor(10, 20, 30),
			wantBg:     term.NewRGBColor(14, 19, 24),
			wantImages: canvas(term.NewRGBColor(36, 26, 56)),
		},
		{
			// Pictures drawn under the backgrounds of cells only show
			// through the default one.
			name: "default shows the canvas", defaultAttr: themed, frame: 2,
			bg:         term.ColorDefault,
			wantBg:     term.ColorDefault,
			wantImages: canvas(term.NewRGBColor(33, 33, 33)),
		},
		{
			name: "default of a reversed cell", defaultAttr: themed, frame: 2,
			bg: term.ColorDefault, attrs: term.AttrReverse,
			wantBg:     term.NewRGBColor(33, 33, 33),
			wantImages: canvas(term.NewRGBColor(33, 33, 33)),
		},
		{
			name: "default of a writer that resolves it", frame: 2,
			bg:     term.ColorDefault,
			wantBg: term.ColorDefault,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params := shader.DefaultGrayFadeParams()
			params.FadeFrames = 3
			sh := shader.GrayFade(params, tc.defaultAttr)
			in := makeCharCells(2, 1)
			in[0][0].Bg, in[0][0].Attrs = tc.bg, tc.attrs

			images := sh.ShadeImages(tc.frame, 100, in, nil)

			assert.Equal(t, tc.wantBg, in[0][0].Bg)
			assert.Equal(t, tc.wantImages, images)
		})
	}
}

func TestGrayFade_DesaturatesFills(t *testing.T) {
	params := shader.DefaultGrayFadeParams()
	params.FadeFrames = 2
	sh := shader.GrayFade(params, term.Attributes{Bg: term.NewRGBColor(40, 20, 80)})
	solid := term.Image{
		Fill: true, FillColor: term.NewRGBColor(0, 90, 200),
		Pos: term.Coordinates{X: 1}, Width: 1, Height: 1, Layer: term.ImageLayerBelowText,
	}
	defaultFill := solid
	defaultFill.FillColor = term.ColorDefault
	mask := image.NewRGBA(image.Rect(0, 0, 1, 1))
	mask.SetRGBA(0, 0, color.RGBA{G: 255, A: 255})
	masked := solid
	masked.Src, masked.ID, masked.FillColor = mask, term.NewImageID(), term.NewRGBColor(255, 0, 0)

	got := sh.ShadeImages(1, 100, makeCharCells(2, 1), []term.Image{solid, defaultFill, masked})

	require.Len(t, got, 4)
	assert.Equal(t, term.ImageLayerBelowBackground, got[0].Layer, "the canvas goes under every picture")
	solid.FillColor = term.NewRGBColor(76, 76, 76)
	defaultFill.FillColor = term.NewRGBColor(33, 33, 33)
	// The colours of a mask's pixels are not the fill's.
	masked.FillColor = term.NewRGBColor(76, 76, 76)
	assert.Equal(t, []term.Image{solid, defaultFill, masked}, got[1:])
}

func TestGrayFade_DesaturationPreservesPerCellBrightness(t *testing.T) {
	// The whole point of using per-cell desaturation instead of blending
	// toward a single shared gray is that distinct source colors stay
	// distinct (and visible) after the fade.
	params := shader.DefaultGrayFadeParams()
	params.FadeFrames = 10
	sh := shader.GrayFade(params, term.Attributes{})
	in := makeCharCells(2, 1)
	in[0][0].Fg = term.NewRGBColor(255, 0, 0)
	in[0][1].Fg = term.NewRGBColor(255, 255, 255)

	sh.Shade(params.FadeFrames-1, 10_000, in)

	left, _, _ := in[0][0].Fg.RGB()
	right, _, _ := in[0][1].Fg.RGB()
	assert.NotEqual(t, left, right,
		"different source colors must not collapse to the same gray")
}

func TestGrayFade_DefaultParamsRampOverTotal(t *testing.T) {
	// When FadeFrames is left at its zero value, GrayFade falls back to
	// the standard Shader convention: progress ramps linearly with
	// frame/(total-1) and reaches 1 at frame==total-1. This lets the
	// shader be used standalone (e.g. via :shaderrun grayFade) where the
	// caller-supplied duration is the only knob.
	sh := shader.GrayFade(shader.GrayFadeParams{}, term.Attributes{})

	const total = 10
	in := makeCharCells(2, 1)
	in[0][0].Fg = term.NewRGBColor(255, 0, 0)
	sh.Shade(total-1, total, in)
	assert.Equal(t, 1.0, sh.Progress(),
		"default params should reach full progress at frame==total-1")
	r, g, b := in[0][0].Fg.RGB()
	assert.Equal(t, r, g)
	assert.Equal(t, r, b)

	// Mid-way through total, progress should be partial.
	sh = shader.GrayFade(shader.GrayFadeParams{}, term.Attributes{})
	mid := makeCharCells(2, 1)
	mid[0][0].Fg = term.NewRGBColor(255, 0, 0)
	sh.Shade(total/2, total, mid)
	assert.InDelta(t, float64(total/2)/float64(total-1), sh.Progress(), 1e-9)
}

func TestGrayFade_DesaturatesPictures(t *testing.T) {
	params := shader.DefaultGrayFadeParams()
	params.FadeFrames = 3
	sh := shader.GrayFade(params, term.Attributes{})
	orange := color.RGBA{R: 200, G: 100, B: 50, A: 255}
	picture := solidPicture(orange, image.Rect(0, 0, 4, 2))
	left, right := picture, picture
	left.Clip, right.Clip = image.Rect(0, 0, 2, 2), image.Rect(2, 0, 4, 2)
	shade := func(frame int, images ...term.Image) []term.Image {
		return sh.ShadeImages(frame, 100, makeCharCells(4, 2), images)
	}
	colorOf := func(img term.Image) color.RGBA { return img.Src.(*image.RGBA).RGBAAt(0, 0) }

	assert.Equal(t, []term.Image{left, right}, shade(0, left, right), "frame 0 leaves pictures as drawn")

	half := shade(1, left, right)
	require.Len(t, half, 2)
	for i, want := range []term.Image{left, right} {
		assert.Equal(t, want.Bounds(), half[i].Bounds())
		assert.Equal(t, want.Clip, half[i].Clip)
		assert.NotEqual(t, picture.ID, half[i].ID)
		assert.Equal(t, color.RGBA{R: 162, G: 112, B: 87, A: 255}, colorOf(half[i]))
	}
	assert.Same(t, half[0].Src, half[1].Src, "the parts of a picture share their gray copy")
	assert.Equal(t, half[0].ID, half[1].ID)

	full := shade(2, left, right)
	gray := color.RGBA{R: 124, G: 124, B: 124, A: 255}
	assert.Equal(t, gray, colorOf(full[0]))
	assert.Equal(t, half[0].ID, full[0].ID, "the copy keeps its ID as the fade goes on")
	assert.Greater(t, full[0].Version, half[0].Version)

	held := shade(3, left)
	assert.Equal(t, full[0], held[0], "the copy is reused while nothing changes")

	picture.Src.(*image.RGBA).SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	left.Version++
	changed := shade(4, left)
	assert.Equal(t, color.RGBA{R: 76, G: 76, B: 76, A: 255}, colorOf(changed[0]), "follows the picture")
	assert.Greater(t, changed[0].Version, held[0].Version)
	assert.Equal(t, color.RGBA{R: 255, A: 255}, colorOf(picture), "the picture drawn is left as is")
}

func TestGrayFade_DesaturatesEachCropOfAPicture(t *testing.T) {
	params := shader.DefaultGrayFadeParams()
	params.FadeFrames = 2
	sh := shader.GrayFade(params, term.Attributes{})
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	src.SetRGBA(1, 0, color.RGBA{G: 255, A: 255})
	left := term.Image{Src: src, ID: term.NewImageID(), Crop: image.Rect(0, 0, 1, 1), Width: 1, Height: 1}
	right := left
	right.Crop, right.Pos = image.Rect(1, 0, 2, 1), term.Coordinates{X: 1}

	got := sh.ShadeImages(1, 100, makeCharCells(2, 1), []term.Image{left, right})
	require.Len(t, got, 2)
	assert.Equal(t, color.RGBA{R: 76, G: 76, B: 76, A: 255}, got[0].Src.(*image.RGBA).RGBAAt(0, 0))
	assert.Equal(t, color.RGBA{R: 150, G: 150, B: 150, A: 255}, got[1].Src.(*image.RGBA).RGBAAt(0, 0))
}
