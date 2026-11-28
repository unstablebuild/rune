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

	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/component/gshader/gshadertest"
)

func TestRegions(t *testing.T) {
	inner := timed("inner")
	src := gshader.Source()
	r1, r2 := image.Rect(0, 0, 4, 2), image.Rect(4, 2, 8, 4)
	shade := func(r image.Rectangle, under gshader.Node) gshader.Node {
		return gshader.Place(inner.Shade(3, 10, gshader.Crop(src, r)), under, r)
	}
	tests := []struct {
		name    string
		regions []image.Rectangle
		want    gshader.Node
	}{
		{name: "none", want: src},
		{name: "one", regions: []image.Rectangle{r1}, want: shade(r1, src)},
		{
			name:    "each from what it shades, over the ones before",
			regions: []image.Rectangle{r1, r2},
			want:    shade(r2, shade(r1, src)),
		},
		{name: "empty ones are skipped", regions: []image.Rectangle{{}, r1, {}}, want: shade(r1, src)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sh := gshader.Regions(inner, func() []image.Rectangle { return tt.regions })
			gshadertest.AssertEqual(t, tt.want, sh.Shade(3, 10, src))
		})
	}
}

func TestRegionsFollowTheLayout(t *testing.T) {
	inner := timed("inner")
	src := gshader.Source()
	r := image.Rect(0, 0, 4, 2)
	sh := gshader.Regions(inner, func() []image.Rectangle { return []image.Rectangle{r} })
	first := sh.Shade(3, 10, src)
	r = r.Add(image.Pt(1, 1))
	gshadertest.AssertEqual(t, gshader.Place(inner.Shade(3, 10, gshader.Crop(src, r)), src, r),
		sh.Shade(3, 10, src))
	gshadertest.AssertEqual(t, gshader.Virtual(inner, image.Rect(0, 0, 4, 2)).Shade(3, 10, src), first)
}
