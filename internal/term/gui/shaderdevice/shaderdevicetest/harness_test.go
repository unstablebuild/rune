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

package shaderdevicetest_test

import (
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/term/gui/shaderdevice/shaderdevicetest"
)

const positionSrc = `//kage:unit pixels

package main

var Resolution vec2

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	return vec4(dstPos.x/Resolution.x, dstPos.y/Resolution.y, 0, 1)
}
`

var position = gshader.MustProgram("position.kage", []byte(positionSrc))

// solid is a shader of a single colour.
type solid term.Color

func (s solid) Shade(int, int, gshader.Node) gshader.Node { return gshader.Solid(term.Color(s)) }

func TestHarnessRenders(t *testing.T) {
	h := shaderdevicetest.New(t)
	pixels, err := h.Render(gshader.FromProgram(position, nil), 0, 1, image.Pt(8, 4))
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, 32, 32), pixels.Rect, "cells of 4x8 pixels")
}

func TestHarnessReportsErrors(t *testing.T) {
	broken := gshader.MustProgram("broken.kage", []byte("//kage:unit pixels\n\npackage main\n\n"+
		"func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {\n\treturn undefined\n}\n"))
	h := shaderdevicetest.New(t)
	_, err := h.Render(gshader.FromProgram(broken, nil), 0, 1, image.Pt(8, 4))
	assert.ErrorContains(t, err, "undefined")
}

func TestHarnessDrawsPositionsFromTheRenderingsCorner(t *testing.T) {
	shaderdevicetest.RequireGPU(t)
	h := shaderdevicetest.New(t)
	// Renderings sharing a texture with others would see positions on
	// that texture.
	for _, size := range []image.Point{{8, 4}, {6, 2}, {8, 4}} {
		pixels, err := h.Render(gshader.FromProgram(position, nil), 0, 1, size)
		require.NoError(t, err)
		w, h := pixels.Rect.Dx(), pixels.Rect.Dy()
		assertNear(t, color.RGBA{R: channel(0.5 / float64(w)), G: channel(0.5 / float64(h)), A: 0xff},
			pixels.RGBAAt(0, 0), "top left of %v", size)
		assertNear(t, color.RGBA{R: channel(1 - 0.5/float64(w)), G: channel(1 - 0.5/float64(h)), A: 0xff},
			pixels.RGBAAt(w-1, h-1), "bottom right of %v", size)
	}
}

func TestHarnessDrawsNodesOnTheGPU(t *testing.T) {
	shaderdevicetest.RequireGPU(t)
	red := color.RGBA{R: 0xff, A: 0xff}
	blue := color.RGBA{B: 0xff, A: 0xff}
	tests := []struct {
		name string
		sh   gshader.Shader
		at   image.Point
		want color.RGBA
	}{
		{name: "the background", sh: solid(term.ColorDefault), at: image.Pt(5, 5), want: color.RGBA{A: 0xff}},
		{name: "a colour", sh: solid(term.ColorRed), at: image.Pt(5, 5), want: red},
		{
			name: "inside a virtual rectangle",
			sh:   gshader.Mix(solid(term.ColorBlue), gshader.Virtual(solid(term.ColorRed), image.Rect(2, 1, 4, 3))),
			at:   image.Pt(9, 9), want: red,
		},
		{
			name: "outside a virtual rectangle",
			sh:   gshader.Mix(solid(term.ColorBlue), gshader.Virtual(solid(term.ColorRed), image.Rect(2, 1, 4, 3))),
			at:   image.Pt(7, 9), want: blue,
		},
		{
			name: "half way through a cross fade",
			sh:   gshader.Remap(gshader.CrossFade(gshader.DefaultCrossFadeParams(), solid(term.ColorRed), solid(term.ColorBlue)), func(int, int) int { return 50 }),
			at:   image.Pt(5, 5), want: color.RGBA{R: 0x80, B: 0x80, A: 0xff},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := shaderdevicetest.New(t)
			h.Theme.Resolve = func(c term.Color) color.RGBA {
				switch c {
				case term.ColorRed:
					return red
				case term.ColorBlue:
					return blue
				default:
					return h.Theme.Background
				}
			}
			pixels, err := h.Render(tt.sh, 0, 100, image.Pt(8, 4))
			require.NoError(t, err)
			assertNear(t, tt.want, pixels.RGBAAt(tt.at.X, tt.at.Y))
		})
	}
}

func channel(v float64) uint8 { return uint8(v*255 + 0.5) }

func assertNear(t *testing.T, want, got color.RGBA, msgAndArgs ...any) {
	t.Helper()
	for _, d := range []int{
		int(want.R) - int(got.R), int(want.G) - int(got.G),
		int(want.B) - int(got.B), int(want.A) - int(got.A),
	} {
		if d > shaderdevicetest.Tolerance || d < -shaderdevicetest.Tolerance {
			assert.Equal(t, want, got, msgAndArgs...)
			return
		}
	}
}
