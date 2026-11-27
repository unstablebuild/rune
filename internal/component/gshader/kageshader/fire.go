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

package kageshader

import (
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/gshader"
)

// FireParams are the parameters of the fires, Blaze and Inferno.
type FireParams struct {
	// Speed is how many times the fire's noise changes over a run,
	// rounded to a whole number; 0 keeps it still.
	Speed float64
	// Color, unless term.ColorDefault, turns the fire's hue so its red
	// burns in the hue of Color, keeping the flames' light and shade. Only
	// the hue counts, so a grey leaves the fire red. It applies before
	// SwapRedBlue.
	Color term.Color
	// SwapRedBlue swaps the fire's red and blue.
	SwapRedBlue bool
	// Strength is how far the fire stands out from the background, from 0
	// to 1.
	Strength float64
	// Seed picks the fire's noise: each whole number burns differently.
	Seed int
}

func (p FireParams) uniforms() gshader.Uniforms {
	return gshader.Uniforms{
		"Speed":             float32(p.Speed),
		"Hue":               float32(hue(p.Color)),
		"SwapRedBlue":       flag(p.SwapRedBlue),
		"Strength":          float32(p.Strength),
		gshader.UniformSeed: float32(p.Seed),
	}
}

// hue returns the hue of c as a fraction of a turn, 0 for a grey or a
// colour with no RGB of its own.
func hue(c term.Color) float64 {
	ri, gi, bi := c.RGB()
	if ri < 0 {
		return 0
	}
	r, g, b := float64(ri), float64(gi), float64(bi)
	hi := max(r, g, b)
	d := hi - min(r, g, b)
	if d == 0 {
		return 0
	}
	var h float64
	switch hi {
	case r:
		h = (g - b) / d
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h /= 6
	return h - math.Floor(h)
}
