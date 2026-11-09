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

package shaderutils

import "math"

// Easing maps a progress ratio in [0, 1] to an eased ratio. An easing
// must map 0 to 0 and 1 to 1 and stay finite within [-1, 2] in between:
// it may overshoot outside [0, 1] (see [EaseInOutBack]), so callers
// positioning cells must clamp the result to the canvas.
type Easing func(t float64) float64

// EaseLinear returns t unchanged.
func EaseLinear(t float64) float64 { return t }

// EaseInQuad accelerates quadratically from rest.
func EaseInQuad(t float64) float64 { return t * t }

// EaseOutQuad decelerates quadratically to rest.
func EaseOutQuad(t float64) float64 { return 1 - (1-t)*(1-t) }

// EaseInOutQuad accelerates until halfway, then decelerates.
func EaseInOutQuad(t float64) float64 {
	if t < 0.5 {
		return 2 * t * t
	}
	u := -2*t + 2
	return 1 - u*u/2
}

// EaseInCubic accelerates cubically from rest.
func EaseInCubic(t float64) float64 { return t * t * t }

// EaseOutCubic decelerates cubically to rest.
func EaseOutCubic(t float64) float64 { return 1 - (1-t)*(1-t)*(1-t) }

// EaseInOutSine follows half a cosine wave from rest to rest.
func EaseInOutSine(t float64) float64 { return -(math.Cos(math.Pi*t) - 1) / 2 }

// EaseOutSine decelerates along a quarter sine wave.
func EaseOutSine(t float64) float64 { return math.Sin(t * math.Pi / 2) }

// EaseInQuart accelerates quartically from rest.
func EaseInQuart(t float64) float64 { return t * t * t * t }

// EaseInOutQuart accelerates quartically until halfway, then
// decelerates.
func EaseInOutQuart(t float64) float64 {
	if t < 0.5 {
		return 8 * t * t * t * t
	}
	return 1 - math.Pow(-2*t+2, 4)/2
}

// EaseInExpo accelerates exponentially from rest.
func EaseInExpo(t float64) float64 {
	if t <= 0 {
		return 0
	}
	return math.Pow(2, 10*t-10)
}

// EaseOutExpo decelerates exponentially to rest.
func EaseOutExpo(t float64) float64 {
	if t >= 1 {
		return 1
	}
	return 1 - math.Pow(2, -10*t)
}

// EaseOutQuint decelerates quintically to rest.
func EaseOutQuint(t float64) float64 { return 1 - math.Pow(1-t, 5) }

// EaseOutBounce bounces a few times before coming to rest.
func EaseOutBounce(t float64) float64 {
	const n1, d1 = 7.5625, 2.75
	switch {
	case t < 1/d1:
		return n1 * t * t
	case t < 2/d1:
		t -= 1.5 / d1
		return n1*t*t + 0.75
	case t < 2.5/d1:
		t -= 2.25 / d1
		return n1*t*t + 0.9375
	default:
		t -= 2.625 / d1
		return n1*t*t + 0.984375
	}
}

// EaseInOutBack pulls back before accelerating, and overshoots before
// settling, so it returns values outside [0, 1] near both ends.
func EaseInOutBack(t float64) float64 {
	const c1 = 1.70158
	const c2 = c1 * 1.525
	if t < 0.5 {
		u := 2 * t
		return (u * u * ((c2+1)*2*t - c2)) / 2
	}
	u := 2*t - 2
	return (u*u*((c2+1)*(t*2-2)+c2) + 2) / 2
}

// EaseInCirc accelerates along a quarter circle from rest.
func EaseInCirc(t float64) float64 {
	return 1 - math.Sqrt(1-math.Min(1, t*t))
}
