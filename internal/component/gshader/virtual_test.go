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

package gshader_test

import (
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/component/gshader/gshadertest"
)

func TestVirtual(t *testing.T) {
	inner := timed("inner")
	src := gshader.Source()
	r := image.Rect(2, 1, 6, 3)
	tests := []struct {
		name string
		r    image.Rectangle
		want gshader.Node
	}{
		{
			name: "shades the part inside r as if it were all there was",
			r:    r,
			want: gshader.Place(inner.Shade(3, 10, gshader.Crop(src, r)), src, r),
		},
		{name: "an empty rectangle shades nothing", r: image.Rect(2, 1, 2, 3), want: src},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := gshader.Virtual(inner, tt.r).Shade(3, 10, src)
			gshadertest.AssertEqual(t, tt.want, got)
			area := image.Pt(8, 4)
			assert.Equal(t, area, gshader.Size(got, area), "the size of what it shades")
		})
	}
}
