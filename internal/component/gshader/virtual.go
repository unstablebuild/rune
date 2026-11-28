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

// Virtual returns a Shader that runs inner over the part of what it shades
// inside r, as if that part were all there was, and leaves the rest as it
// is. An empty r leaves everything as it is.
func Virtual(inner Shader, r image.Rectangle) Shader {
	return virtualShader{inner: inner, r: r}
}

type virtualShader struct {
	inner Shader
	r     image.Rectangle
}

func (s virtualShader) Shade(frame, total int, src Node) Node {
	return virtual(s.inner, s.r, frame, total, src, src)
}

// virtual returns under with inner's shading of the part of src inside r
// placed over r.
func virtual(inner Shader, r image.Rectangle, frame, total int, src, under Node) Node {
	if r.Empty() {
		return under
	}
	return Place(inner.Shade(frame, total, Crop(src, r)), under, r)
}
