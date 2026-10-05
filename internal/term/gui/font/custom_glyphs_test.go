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
	"testing"

	"github.com/stretchr/testify/assert"
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
