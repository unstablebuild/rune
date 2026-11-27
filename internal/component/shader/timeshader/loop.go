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
	"unstable.build/rune/internal/component/shader"
)

// Loop repeats the input range "cycles" times.
//
//	····································x····································x
//	···································x····································x·
//	·································xx···································xx··
//	··········································································
//	································x····································x····
//	······························xx···································xx·····
//	··········································································
//	····························xx···································xx·······
//	··········································································
//	··························xx···································xx·········
//	·························x····································x···········
//	··········································································
//	·······················xx···································xx············
//	······················x····································x··············
//	·····················x····································x···············
//	····················x····································x················
//	···················x····································x·················
//	··················x····································x··················
//	·················x····································x···················
//	················x····································x····················
//	···············x····································x·····················
//	·············xx···································xx······················
//	··········································································
//	···········xx···································xx························
//	··········x····································x··························
//	··········································································
//	········xx···································xx···························
//	··········································································
//	······xx···································xx·····························
//	·····x····································x·······························
//	····x····································x································
//	···x····································x·································
//	··x····································x··································
//	·x····································x···································
//	x++++++++++++++++++++++++++++++++++++x++++++++++++++++++++++++++++++++++++
func Loop(baseShader shader.Shader, cycles int) shader.Shader {
	return loop(baseShader, cycles)
}

func loop(baseShader shader.Shader, cycles int) *timeShader {
	return &timeShader{baseShader, funcTimeRemapper(LoopTime(cycles))}
}

// LoopTime returns the frame Loop plays at frame of total, for shaders
// other than shader.Shader to remap their time with.
func LoopTime(cycles int) func(frame, total int) int {
	return func(frame, total int) int {
		framesPerLoop := total / cycles
		loopedFrame := frame
		if cycles > 1 {
			loopedFrame = cycles * (frame % (framesPerLoop))

			// prevent the very last frame of the looped animation to be
			// the first input frame.
			if frame == total-1 && total%2 == 1 {
				loopedFrame = total - 1
			}
		}
		return loopedFrame
	}
}
