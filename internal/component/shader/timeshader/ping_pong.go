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

// PingPong plays normal and then plays the same content in reverse "bounces"
// times, linearly.
//
//	····································xxx···································
//	···································x···x··································
//	·································xx·····x·································
//	·········································x································
//	································x·········x·······························
//	······························xx···········x······························
//	············································x·····························
//	····························xx···············x····························
//	··············································x···························
//	··························xx···················x··························
//	·························x······················x·························
//	·················································x························
//	·······················xx·························x·······················
//	······················x····························xx·····················
//	·····················x····················································
//	····················x································xx···················
//	···················x···································x··················
//	··················x·······················································
//	·················x······································xx················
//	················x·········································x···············
//	···············x···········································x··············
//	·············xx·············································x·············
//	·····························································x············
//	···········xx·················································x···········
//	··········x····················································x··········
//	································································x·········
//	········xx·······················································x········
//	··································································x·······
//	······xx···························································x······
//	·····x······························································xx····
//	····x·····································································
//	···x··································································x···
//	··x····································································xx·
//	·x········································································
//	x++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++x
func PingPong(baseShader shader.Shader) shader.Shader {
	return pingPong(baseShader)
}

func pingPong(baseShader shader.Shader) *timeShader {
	return &timeShader{baseShader, funcTimeRemapper(PingPongTime())}
}

// PingPongTime returns the frame PingPong plays at frame of total, for
// shaders other than shader.Shader to remap their time with.
func PingPongTime() func(frame, total int) int {
	return func(frame, total int) int {
		halfTotal := total / 2
		bouncedFrame := frame % (halfTotal)

		// on the second half reverse the frame
		whichHalf := frame / (halfTotal)
		if whichHalf == 1 {
			return total - 2*bouncedFrame - 1
		}
		return 2 * bouncedFrame
	}
}
