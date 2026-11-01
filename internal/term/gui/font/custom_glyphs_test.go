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

package font

import (
	"fmt"
	"image"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/math/fixed"
)

func TestCustomGlyphRendersToCPUMask(t *testing.T) {
	m, err := NewManager(0, 0)
	assert.NoError(t, err)
	face := m.RegularFontFace()

	// '─' (U+2500) is a full-width horizontal line: it must fill exactly
	// the stroke row across the whole cell.
	dot := fixed.Point26_6{X: 0, Y: fixed.I(20)}
	_, mask, _, _, ok := face.Glyph(dot, '─')
	assert.True(t, ok)

	_, isRGBA := mask.(*image.RGBA)
	assert.Truef(t, isRGBA, "custom glyph mask type = %T, want *image.RGBA", mask)

	coverage := 0
	b := mask.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := mask.At(x, y).RGBA(); a != 0 {
				coverage++
			}
		}
	}
	assert.Positivef(t, coverage, "box-drawing glyph rendered no pixels")
}

func TestFrameChannelGlyphs(t *testing.T) {
	suite := []struct {
		description   string
		r             rune
		width, height float64
		want          []image.Rectangle
	}{
		{
			description: "top left turns from the left groove into the top one",
			r:           '\U00100008',
			width:       16, height: 32,
			want: []image.Rectangle{image.Rect(2, 2, 6, 32), image.Rect(2, 2, 16, 6)},
		},
		{
			description: "top right turns from the top groove into the right one",
			r:           '\U00100009',
			width:       16, height: 32,
			want: []image.Rectangle{image.Rect(10, 2, 14, 32), image.Rect(0, 2, 14, 6)},
		},
		{
			description: "bottom left turns from the left groove into the bottom one",
			r:           '\U0010000A',
			width:       16, height: 32,
			want: []image.Rectangle{image.Rect(2, 0, 6, 30), image.Rect(2, 26, 16, 30)},
		},
		{
			description: "bottom right turns from the bottom groove into the right one",
			r:           '\U0010000B',
			width:       16, height: 32,
			want: []image.Rectangle{image.Rect(10, 0, 14, 30), image.Rect(0, 26, 14, 30)},
		},
		{
			description: "top spans the width along its groove",
			r:           '\U0010000C',
			width:       16, height: 32,
			want: []image.Rectangle{image.Rect(0, 2, 16, 6)},
		},
		{
			description: "bottom spans the width along its groove",
			r:           '\U0010000D',
			width:       16, height: 32,
			want: []image.Rectangle{image.Rect(0, 26, 16, 30)},
		},
		{
			description: "left spans the height along its groove",
			r:           '\U0010000E',
			width:       16, height: 32,
			want: []image.Rectangle{image.Rect(2, 0, 6, 32)},
		},
		{
			description: "right spans the height along its groove",
			r:           '\U0010000F',
			width:       16, height: 32,
			want: []image.Rectangle{image.Rect(10, 0, 14, 32)},
		},
		{
			description: "left tee branches from the left groove into a bottom one",
			r:           '\U00100010',
			width:       16, height: 32,
			want: []image.Rectangle{image.Rect(2, 0, 6, 32), image.Rect(2, 26, 16, 30)},
		},
		{
			description: "right tee branches from the right groove into a bottom one",
			r:           '\U00100011',
			width:       16, height: 32,
			want: []image.Rectangle{image.Rect(10, 0, 14, 32), image.Rect(0, 26, 14, 30)},
		},
	}

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			f := newCustomFace(test.width, test.height, 0, &mockFace{}, false, 0, 0)
			dr, mask, maskp, advance, ok := f.Glyph(fixed.Point26_6{}, test.r)
			require.True(t, ok)
			require.Equal(t, image.Rect(0, 0, int(test.width), int(test.height)), dr)
			assert.Equal(t, fixed.I(int(test.width)), advance)

			got := make([]string, dr.Dy())
			want := make([]string, dr.Dy())
			for y := range dr.Dy() {
				var gotRow, wantRow strings.Builder
				for x := range dr.Dx() {
					_, _, _, a := mask.At(maskp.X+x, maskp.Y+y).RGBA()
					gotRow.WriteByte(".#"[min(a, 1)])
					covered := byte('.')
					for _, r := range test.want {
						if image.Pt(x, y).In(r) {
							covered = '#'
						}
					}
					wantRow.WriteByte(covered)
				}
				got[y], want[y] = gotRow.String(), wantRow.String()
			}
			assert.Equal(t, want, got)
		})
	}
}

func TestPlusGlyphBounds(t *testing.T) {
	suite := []struct {
		description string
		// in
		bounds   image.Rectangle
		lhstroke int
		rhstroke int
		tvstroke int
		bvstroke int

		// expected
		xv     float64
		yh     float64
		xh     float64
		yv     float64
		lhsize float64
		rhsize float64
		tvsize float64
		bvsize float64
	}{
		{
			description: "full plus sign, bounds on 0, 0 coordinates origin, even size",
			bounds:      image.Rectangle{Max: image.Point{X: 6, Y: 8}},
			lhstroke:    1,
			rhstroke:    1,
			tvstroke:    1,
			bvstroke:    1,

			xv:     3,
			yh:     4,
			xh:     2,
			yv:     3,
			lhsize: 3,
			rhsize: 4,
			tvsize: 4,
			bvsize: 5,
		},
		{
			description: "bar sign, bounds on 0, 0 coordinates origin",
			bounds:      image.Rectangle{Max: image.Point{X: 6, Y: 8}},
			lhstroke:    0,
			rhstroke:    0,
			tvstroke:    1,
			bvstroke:    1,

			xv:     3,
			yh:     4,
			xh:     2,
			yv:     4,
			lhsize: 3,
			rhsize: 4,
			tvsize: 4,
			bvsize: 4,
		},
		{
			description: "top bar only sign, bounds on 0, 0 coordinates origin",
			bounds:      image.Rectangle{Max: image.Point{X: 6, Y: 8}},
			lhstroke:    0,
			rhstroke:    0,
			tvstroke:    1,
			bvstroke:    0,

			xv:     3,
			yh:     4,
			xh:     2,
			yv:     4,
			lhsize: 3,
			rhsize: 4,
			tvsize: 4,
			bvsize: 4,
		},
		{
			description: "horizontal bar sign, bounds on 0, 0 coordinates origin",
			bounds:      image.Rectangle{Max: image.Point{X: 6, Y: 8}},
			lhstroke:    0,
			rhstroke:    1,
			tvstroke:    1,
			bvstroke:    0,

			xv:     3,
			yh:     4,
			xh:     2,
			yv:     3,
			lhsize: 3,
			rhsize: 4,
			tvsize: 4,
			bvsize: 5,
		},
		{
			description: "full plus sign, bounds on 0, 0 coordinates origin, odd size",
			bounds:      image.Rectangle{Max: image.Point{X: 7, Y: 9}},
			lhstroke:    1,
			rhstroke:    1,
			tvstroke:    1,
			bvstroke:    1,

			xv:     3.5,
			yh:     4.5,
			xh:     3,
			yv:     4,
			lhsize: 4,
			rhsize: 4,
			tvsize: 5,
			bvsize: 5,
		},
		{
			description: "full plus sign, negative y bounds, odd size",
			bounds: image.Rectangle{
				Min: image.Point{X: 0, Y: -23},
				Max: image.Point{X: 11, Y: 0},
			},
			lhstroke: 1,
			rhstroke: 1,
			tvstroke: 1,
			bvstroke: 1,

			xv:     5.5,
			yh:     -11.5,
			xh:     5,
			yv:     -12,
			lhsize: 6,
			rhsize: 6,
			tvsize: 12,
			bvsize: 12,
		},
	}

	for i, test := range suite {
		t.Run(fmt.Sprintf("test number %d", i), func(t *testing.T) {
			xv, yh, xh, yv, lhsize, rhsize, tvsize, bvsize := plusGlyphBounds(
				test.bounds, test.lhstroke, test.rhstroke, test.tvstroke, test.bvstroke)

			assert.Equal(t, test.xv, xv, "xv")
			assert.Equal(t, test.yh, yh, "yh")
			assert.Equal(t, test.xh, xh, "xh")
			assert.Equal(t, test.yv, yv, "yv")

			assert.Equal(t, test.lhsize, lhsize, "lhsize")
			assert.Equal(t, test.rhsize, rhsize, "rhsize")
			assert.Equal(t, test.tvsize, tvsize, "tvsize")
			assert.Equal(t, test.bvsize, bvsize, "bvsize")
		})
	}
}
