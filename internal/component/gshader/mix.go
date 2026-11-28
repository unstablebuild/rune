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

// Mix returns a Shader that runs shaders one after the other, each over
// what the one before it returned, the first over what Mix shades. It
// panics without shaders.
func Mix(shaders ...Shader) Shader {
	if len(shaders) == 0 {
		panic("gshader: Mix needs shaders")
	}
	return mixShader{shaders: shaders}
}

type mixShader struct {
	shaders []Shader
}

func (m mixShader) Shade(frame, total int, src Node) Node {
	for _, s := range m.shaders {
		src = s.Shade(frame, total, src)
	}
	return src
}
