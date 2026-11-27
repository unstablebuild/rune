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

package gshader

import "image"

// Regions returns a Shader that runs inner over each rectangle regions
// returns, as Virtual does for one. regions is called on every frame, so
// the shaded parts follow layout changes. Each rectangle is shaded from
// what Regions shades, so where rectangles overlap the last one shows.
func Regions(inner Shader, regions func() []image.Rectangle) Shader {
	return regionsShader{inner: inner, regions: regions}
}

type regionsShader struct {
	inner   Shader
	regions func() []image.Rectangle
}

func (s regionsShader) Shade(frame, total int, src Node) Node {
	n := src
	for _, r := range s.regions() {
		// Cropping src rather than n keeps the tree growing linearly with
		// the rectangles: n is already in the tree once, as Under.
		n = virtual(s.inner, r, frame, total, src, n)
	}
	return n
}
