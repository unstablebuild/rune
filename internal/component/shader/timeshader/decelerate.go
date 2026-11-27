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
	"math"

	"unstable.build/rune/internal/component/shader"
)

// Decelerate the reverse of the accelerate quadratic t².
//
// It starts fast and slows down.
//
//	f(t) = 1 -(1 - t)²
//
// This causes the animation to start slowly and then speed up.
//
//	····························································xxxxxxxxxxxxxx
//	·······················································xxxxx··············
//	···················································xxxx···················
//	·················································xx·······················
//	··············································xxx·························
//	···········································xxx····························
//	········································xxx·······························
//	······································xx··································
//	····································xx····································
//	·································xxx······································
//	··········································································
//	······························xxx·········································
//	·····························x············································
//	···························xx·············································
//	·························xx···············································
//	························x·················································
//	······················xx··················································
//	·····················x····················································
//	···················xx·····················································
//	··················x·······················································
//	················xx························································
//	···············x··························································
//	·············xx···························································
//	··········································································
//	···········xx·····························································
//	··········x·······························································
//	·········x································································
//	········x·································································
//	·······x··································································
//	·····xx···································································
//	····x·····································································
//	···x······································································
//	··x·······································································
//	·x········································································
//	x+++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++++
func Decelerate(baseShader shader.Shader) shader.Shader {
	return decelerate(baseShader)
}

func decelerate(baseShader shader.Shader) *timeShader {
	return &timeShader{baseShader, funcTimeRemapper(DecelerateTime())}
}

// DecelerateTime returns the frame Decelerate plays at frame of total, for
// shaders other than shader.Shader to remap their time with.
func DecelerateTime() func(frame, total int) int {
	return func(frame, total int) int {
		dur := float64(total - 1)
		t := float64(frame) / dur
		tt := 1.0 - (t-1.0)*(t-1.0)
		return int(math.Round(dur * tt))
	}
}
