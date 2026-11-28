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

// Sine creates a full sine wave cycle (start .. end .. start .. -end .. start)
// or the corresponding portion of it (or multiple of them) specified by the
// "revolutions" parameter.
func Sine(baseShader shader.Shader, revolutions float64) shader.Shader {
	return sine(baseShader, revolutions)
}

// Boomerang creates a half sine wave cycle (start .. end .. start) creating a
// curved time animation playing twice the speed til the last frame and coming
// back to initial frame.
func Boomerang(baseShader shader.Shader) shader.Shader {
	return sine(baseShader, 0.5)
}

func sine(baseShader shader.Shader, revolutions float64) *timeShader {
	return &timeShader{baseShader, funcTimeRemapper(SineTime(revolutions))}
}

// SineTime returns the frame Sine plays at frame of total, for shaders
// other than shader.Shader to remap their time with.
func SineTime(revolutions float64) func(frame, total int) int {
	return func(frame, total int) int {
		return int(math.Round(float64(total-1) *
			math.Sin(revolutions*2*math.Pi*float64(frame)/float64(total-1))),
		)
	}
}

// BoomerangTime returns the frame Boomerang plays at frame of total, for
// shaders other than shader.Shader to remap their time with.
func BoomerangTime() func(frame, total int) int {
	return SineTime(0.5)
}
