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

// Period returns a Shader that plays a run of inner every frames frames,
// over and over, whatever the total it is given: it sets the pace of inner
// rather than stretching it over the total. frames must be positive.
func Period(inner Shader, frames int) Shader {
	return periodShader{inner: inner, frames: frames}
}

type periodShader struct {
	inner  Shader
	frames int
}

func (s periodShader) Shade(frame, _ int, src Node) Node {
	return s.inner.Shade(frame%s.frames, s.frames, src)
}
