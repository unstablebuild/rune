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

// Period plays a run of the base shader every frames frames, over and
// over, whatever the total it is given: it sets the pace of the base
// shader rather than stretching it over the total. A total shorter than
// frames plays part of a run, and a longer one starts the base shader
// over each time its run ends, which only the shaders whose last frame
// leads back into their first play seamlessly. frames must be positive.
func Period(baseShader shader.Shader, frames int) shader.Shader {
	return period{baseShader: baseShader, frames: frames}
}

type period struct {
	baseShader shader.Shader
	frames     int
}

func (p period) Shade(frame, _ int, in [][]term.Cell) {
	p.baseShader.Shade(frame%p.frames, p.frames, in)
}

func (p period) ShadeImages(frame, _ int, in [][]term.Cell, images []term.Image) []term.Image {
	return shader.ShadeImages(p.baseShader, frame%p.frames, p.frames, in, images)
}
