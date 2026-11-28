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

// Remap returns a Shader that runs inner at the frame fn returns for the
// frame and total it is given, such as those of timeshader.LoopTime and
// its siblings. fn must return the same frame for the same arguments.
func Remap(inner Shader, fn func(frame, total int) int) Shader {
	return remapShader{inner: inner, fn: fn}
}

type remapShader struct {
	inner Shader
	fn    func(frame, total int) int
}

func (s remapShader) Shade(frame, total int, src Node) Node {
	return s.inner.Shade(s.fn(frame, total), total, src)
}
