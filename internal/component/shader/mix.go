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

package shader

import "github.com/unstablebuild/rune-go-sdk/term"

// Mix combines multiple shaders into one. They're run
// in the same order they're passed to this constructor,
// and the images each returns, as [ShadeImages] does,
// are passed on to the next.
func Mix(shaders ...Shader) Shader {
	if len(shaders) == 0 {
		panic("shaders must not be 0")
	}
	return mixShader{shaders: shaders}
}

type mixShader struct {
	shaders []Shader
}

func (m mixShader) Shade(frame, total int, in [][]term.Cell) {
	for _, shader := range m.shaders {
		shader.Shade(frame, total, in)
	}
}

func (m mixShader) ShadeImages(frame, total int, in [][]term.Cell, images []term.Image) []term.Image {
	for _, shader := range m.shaders {
		images = ShadeImages(shader, frame, total, in, images)
	}
	return images
}
