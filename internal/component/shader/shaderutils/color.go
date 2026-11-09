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

import (
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// InterpolateColor calculates a color that is a linear blend between the
// provided color and target color.
//
// In case color is the term.ColorDefault then it is resolved with
// resolveColorDefault.
//
// If after resolution either endpoint is still unresolvable (e.g. a default
// color whose fallback is also default), the blend is undefined and the
// function returns color unchanged so callers preserve the terminal's
// default rendering rather than emitting garbage RGB.
func InterpolateColor(
	factor float64, color, target, resolveColorDefault term.Color,
) term.Color {
	// Identity short-circuit: when color and target are the same value,
	// return color unchanged so named/palette colors (and ColorDefault) are
	// preserved instead of being converted to literal TrueColor RGB. This
	// keeps cells that no shader actually changed visually identical to the
	// underlying component when used via blending wrappers.
	if color == target {
		return color
	}
	resolved := color
	if resolved == term.ColorDefault {
		resolved = resolveColorDefault
	}
	// If either endpoint can't be expressed as RGB after resolution, the
	// lerp would operate over (-1,-1,-1) and NewRGBColor's & 0xff masking
	// would synthesize spurious dark/bright colors at intermediate
	// factors. Treat it as a no-op and keep the original color so the
	// terminal keeps rendering it natively.
	if resolved.Hex() < 0 || target.Hex() < 0 {
		return color
	}
	r, g, b := resolved.RGB()
	tr, tg, tb := target.RGB()
	return term.NewRGBColor(
		int32(float64(r)+((float64(tr)-float64(r))*factor)),
		int32(float64(g)+((float64(tg)-float64(g))*factor)),
		int32(float64(b)+((float64(tb)-float64(b))*factor)),
	)
}

// ColorBrightness returns a normalized value 0..1 indicating the intensity of the color.
//
// In case color is the term.ColorDefault then it is resolved with
// resolveColorDefault.
func ColorBrightness(color, resolveColorDefault term.Color) float64 {
	if color == term.ColorDefault {
		color = resolveColorDefault
	}
	r, g, b := color.RGB()
	return float64(r+g+b) / float64(255*3)
}

// SampleGradient interpolates a color from a gradient at a given position
// where factor=0 is the first color and factor=1 the last. A NaN factor
// samples the first color: shader knobs can produce one through a
// division the caller cannot rule out, and converting NaN to an index is
// implementation-defined (amd64 yields MinInt64 and panics on the index).
func SampleGradient(factor float64, gradient []term.Color) term.Color {
	if len(gradient) == 0 {
		return term.ColorDefault
	}
	if math.IsNaN(factor) {
		factor = 0
	}
	t := math.Min(1, math.Max(0, factor))
	stops := float64(len(gradient) - 1)
	tt := t * stops

	currIdx := int(math.Floor(tt))
	nextIdx := currIdx + 1
	if nextIdx >= len(gradient) {
		nextIdx = len(gradient) - 1
	}

	tDec := tt - math.Floor(tt)

	col1 := gradient[currIdx]
	col2 := gradient[nextIdx]

	return InterpolateColor(tDec, col1, col2, 0)
}

// DesaturateColor returns a color whose saturation has been reduced by
// amount in [0, 1]. amount=0 returns color unchanged; amount=1 returns
// the per-channel gray with the same luminance (the ITU-R BT.601 weighted
// average of the RGB components). Intermediate values are a linear blend
// between color and that gray.
//
// Unlike interpolating toward a single fixed gray, this preserves each
// cell's relative brightness so the result reads as the same color with
// its hue removed rather than every cell collapsing to one shade.
//
// If color is [term.ColorDefault] it is resolved with resolveDefault.
// If after resolution color cannot be expressed as RGB, color is
// returned unchanged so the terminal keeps rendering it natively.
func DesaturateColor(
	color term.Color, amount float64, resolveDefault term.Color,
) term.Color {
	if amount <= 0 {
		return color
	}
	if amount > 1 {
		amount = 1
	}
	resolved := color
	if resolved == term.ColorDefault {
		resolved = resolveDefault
	}
	if resolved.Hex() < 0 {
		return color
	}
	r, g, b := resolved.RGB()
	lum := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
	if amount >= 1 {
		l := int32(math.Round(lum))
		return term.NewRGBColor(l, l, l)
	}
	return term.NewRGBColor(
		int32(math.Round(float64(r)+(lum-float64(r))*amount)),
		int32(math.Round(float64(g)+(lum-float64(g))*amount)),
		int32(math.Round(float64(b)+(lum-float64(b))*amount)),
	)
}

// AdjustBrightness scales the HSL lightness of color by factor, so
// factor < 1 darkens and factor > 1 brightens while keeping the hue,
// the way terminaltexteffects dims and flashes its characters. The
// lightness is clamped to [0, 1].
//
// If color is [term.ColorDefault] it is resolved with resolveDefault.
// If after resolution color cannot be expressed as RGB, color is
// returned unchanged so the terminal keeps rendering it natively.
func AdjustBrightness(
	color term.Color, factor float64, resolveDefault term.Color,
) term.Color {
	resolved := color
	if resolved == term.ColorDefault {
		resolved = resolveDefault
	}
	if resolved.Hex() < 0 {
		return color
	}
	ri, gi, bi := resolved.RGB()
	r, g, b := float64(ri)/255, float64(gi)/255, float64(bi)/255
	h, s, l := rgbToHSL(r, g, b)
	l = math.Max(0, math.Min(1, l*factor))
	r, g, b = hslToRGB(h, s, l)
	return term.NewRGBColor(
		int32(math.Round(r*255)),
		int32(math.Round(g*255)),
		int32(math.Round(b*255)),
	)
}

func rgbToHSL(r, g, b float64) (h, s, l float64) {
	maxC := math.Max(r, math.Max(g, b))
	minC := math.Min(r, math.Min(g, b))
	l = (maxC + minC) / 2
	if maxC == minC {
		return 0, 0, l
	}
	d := maxC - minC
	if l > 0.5 {
		s = d / (2 - maxC - minC)
	} else {
		s = d / (maxC + minC)
	}
	switch maxC {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return h / 6, s, l
}

func hslToRGB(h, s, l float64) (r, g, b float64) {
	if s == 0 {
		return l, l, l
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	return hueToRGB(p, q, h+1.0/3), hueToRGB(p, q, h), hueToRGB(p, q, h-1.0/3)
}

func hueToRGB(p, q, t float64) float64 {
	if t < 0 {
		t++
	}
	if t > 1 {
		t--
	}
	switch {
	case t < 1.0/6:
		return p + (q-p)*6*t
	case t < 0.5:
		return q
	case t < 2.0/3:
		return p + (q-p)*(2.0/3-t)*6
	default:
		return p
	}
}
