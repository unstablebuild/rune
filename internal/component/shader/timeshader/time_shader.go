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

package timeshader

import (
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
)

var _ shader.Shader = (*timeShader)(nil)

type timeShader struct {
	baseShader shader.Shader
	timeRemapper
}

func (ts timeShader) Shade(frame, total int, in [][]term.Cell) {
	mappedFrame := ts.remapTime(frame, total)
	ts.baseShader.Shade(mappedFrame, total, in)

}

func (ts timeShader) ShadeImages(frame, total int, in [][]term.Cell, images []term.Image) []term.Image {
	return shader.ShadeImages(ts.baseShader, ts.remapTime(frame, total), total, in, images)
}

type timeRemapper interface {
	remapTime(frame, total int) int
}

func funcTimeRemapper(fn func(frame, total int) int) timeRemapper {
	return fnTimeRemapper{fn: fn}
}

type fnTimeRemapper struct {
	fn func(frame, total int) int
}

func (i fnTimeRemapper) remapTime(frame, total int) int {
	return i.fn(frame, total)
}
