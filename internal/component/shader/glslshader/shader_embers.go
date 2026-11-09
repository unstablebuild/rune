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

package glslshader

import (
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/shader"
)

// Embers are shows burning in ring-like blobs that turn into ashes as sparks
// glitter on top. As this happens ascending rising particles displace the
// original characters that end up landing on the original position.
//
// Every timing is a fraction of the run, so a longer run plays the same
// embers slower.
func Embers(params EmbersParams, defaultAttr term.Attributes) shader.Shader {
	// Change normalized parameters to suitable ranges.
	params.DurationPercCoolDown = clamp(params.DurationPercCoolDown, 0.0, 1.0)
	params.DurationPercDisappear = clamp(params.DurationPercDisappear, 0.0, 1.0)
	params.DurationPercFgEmberToAsh = clamp(params.DurationPercFgEmberToAsh, 0.0, 1.0)

	// Parameters that represent percentage of an animation between 0% (0.0)
	// and 100% (1.0) do not make sense to exceed 1.0 when added.
	if (params.DurationPercCoolDown + params.DurationPercDisappear) > 1.0 {
		panic("DurationCoolDown and DurationDisappear mustn't exceed 1.0 " +
			"(100% of the animation)",
		)
	}

	params.Scale *= 20.0        // from 0..1 to 0..20
	params.Fierce *= 2.0        // from 0..1 to 0..2
	params.GlowSpeed *= 60.0    // from 0..1 to 0..60 radians over the run
	params.SparksSpeed *= 180.0 // from 0..1 to 0..180 radians over the run

	return &embers{
		EmbersParams: params,
		defaultAttr:  defaultAttr,
	}
}

// EmbersParams allows you to customize the Embers effect.
//
// Most parameters are in the normalized range 0-1. Exceeding the range is
// allowed in most of the cases (it's specified in the docstring).
type EmbersParams struct {
	// Draw these specific characters based on the heat.
	Chars []rune
	// DurationCoolDown (in percentage) of the embers to cool down and
	// (DurationDisappear) to vanish afterwards.
	// (range: 0..1 clamped (0% to 100%) and shouldn't exceed 1.0 when added)
	DurationPercCoolDown, DurationPercDisappear float
	// Controls how long does it take for the foreground characters to
	// transition between the spark colors to the ash pale colors.
	//
	// It's similar to DurationPercCoolDown and DurationPercDisappear but only
	// affects the foreground characters.
	//
	// (range: 0..1 clamped (0% to 100%) and shouldn't exceed 1.0 when added)
	DurationPercFgEmberToAsh float
	// The smaller the bigger the ember blobs.
	// (range: 0..1 unrestricted)
	Scale float
	// The higher the more fierce and larger the remaining ember rings.
	// (range: 0..1 unrestricted)
	Fierce float
	// How fast the heat rings pulsate.
	// (range: 0..1 unrestricted)
	GlowSpeed float
	// Sparks are warm-flickering star-like bright points that affect
	// foreground only.
	// (range: 0..1 unrestricted)
	SparksAmount float
	// How fast the sparks pulsate.
	// (range: 0..1 unrestricted)
	SparksSpeed float
	// Colors to paint the effect with.
	Colors EmbersColors
}

// DefaultEmbersParams gives you a set of params that exhibits the Embers
// effect properly. It's a good starting point to customizing on top of with
// parameter overrides.
func DefaultEmbersParams() EmbersParams {
	return EmbersParams{
		Chars:                    []rune{' ', '.', ':', '∴', 'o'},
		DurationPercCoolDown:     0.4,
		DurationPercDisappear:    0.4,
		DurationPercFgEmberToAsh: 0.3,
		Scale:                    0.25,
		Fierce:                   0.55,
		GlowSpeed:                0.5,
		SparksAmount:             0.45,
		SparksSpeed:              0.5,
		Colors:                   EmbersColorsRed(),
	}
}

// EmbersColorsRed provides natural fire-looking colors to grade the embers
// with.
//
// Aimed to be passed to EmbersParams.Colors.
func EmbersColorsRed() EmbersColors {
	return EmbersColors{
		Fg1:     term.NewRGBColor(255, 22, 0),
		Fg2:     term.NewRGBColor(255, 198, 28),
		Bg1:     term.NewRGBColor(255, 22, 0),
		Bg2:     term.NewRGBColor(255, 255, 0),
		Sparks1: term.NewRGBColor(241, 24, 0),
		Sparks2: term.NewRGBColor(243, 255, 24),
		Ashes:   term.NewRGBColor(47, 30, 32),
	}
}

// EmbersColors defines color-related parameters for EmbersParams.
type EmbersColors struct {
	// Colors to taint the foreground with: Fg1 on cooler ember regions
	// presence Fg2 otherwise.
	Fg1, Fg2 term.Color
	// Colors to taint the background with: Fg1 on cooler ember regions
	// presence Fg2 otherwise.
	Bg1, Bg2 term.Color
	// Colors to taint the foreground with: Fg1 on cooler ember parts Fg2
	// otherwise.
	Sparks1, Sparks2 term.Color
	// Base color for the ashes embers blend into.
	Ashes term.Color
}

type embers struct {
	EmbersParams
	defaultAttr term.Attributes
}

// ashStagger staggers when neighbouring characters turn to ash, as a
// fraction of the run per step of a four-step pattern.
const ashStagger = 30 / tunedSteps

func (s *embers) Shade(frame, total int, cells [][]term.Cell) {
	if total <= 0 || frame < 0 || frame >= total {
		return
	}

	progress := float(frame) / float(total)
	for y := range cells {
		for x := 0; x < len(cells[0]); x++ {
			s.processCell(x, y, progress, cells)
		}
	}
}

// processCell shades the cell at x, y as it is progress, in [0, 1),
// into the run. A negative progress stands for a cell the embers are
// yet to reach.
func (s *embers) processCell(x, y int, progress float, cells [][]term.Cell) (
	embersAmount, hotRings float, // useful outputs for creative shader combination
) {
	if y >= len(cells) || x >= len(cells[y]) {
		return
	}

	// Prepare position vector to feed to embers generator.

	rows := len(cells)
	cols := len(cells[0])

	cellX := x
	cellY := y

	fragCoordX := x
	fragCoordY := int(math.Round(float(rows-y-1) *
		asciiart.HeightToWidthCellAspectRatio))
	resolutionX := cols
	resolutionY := int(math.Round(float(rows) *
		asciiart.HeightToWidthCellAspectRatio))

	aspectRatio := float(resolutionX) / float(resolutionY)

	p := vec2(float(fragCoordX), float(fragCoordY)).
		div(vec2(float(resolutionX), aspectRatio*float(resolutionY))).
		multSc(s.Scale)

	// Produce the embers.

	embersProgress := clamp(
		progress/s.DurationPercCoolDown,
		0.0, 1.0,
	)
	embersAmount, hotRings, embersChar, embersFg, embersBg := s.embers(
		p, cellX, cellY, embersProgress, progress, cells,
	)

	char := embersChar

	// Blend embers with ashes.

	ashesFg, ashesBg := s.ashes(p)

	fgEmbersAshesFade := clamp(
		(1.0)-(progress-
			ashStagger*float((fragCoordX+fragCoordY)%4))/
			s.DurationPercFgEmberToAsh,
		0.0, 1.0,
	)
	hotRingsFg := mix3D(
		colToVec(s.Colors.Sparks2, s.defaultAttr.Fg),
		colToVec(s.Colors.Sparks1, s.defaultAttr.Fg),
		noiseSimplex01(p.multSc(75.0)),
	)
	fg := vecToCol(mix3D(
		mix3D(ashesFg, embersFg, fgEmbersAshesFade), hotRingsFg, embersProgress*hotRings),
	)

	cells[y][x].Fg = fg
	cells[y][x].Bg = vecToCol(mix3D(ashesBg, embersBg, embersAmount))
	cells[y][x].Ch = char

	// A cell with a character to be visible must have width set.
	if char != rune(0) {
		cells[cellY][cellX].Width = 1
	}

	return
}

func (s *embers) embers(
	p vec2D,
	cellX, cellY int,
	emberProgress float,
	progress float,
	cells [][]term.Cell,
) (emberAmount float, hotRings float, char rune, fg, bg vec3D) {
	if cellY >= len(cells) || cellX >= len(cells[cellY]) {
		return
	}

	cell := cells[cellY][cellX]

	noise := noiseSimplex(p)
	strength := s.Fierce * smoothstep(
		s.DurationPercCoolDown+s.DurationPercDisappear,
		s.DurationPercCoolDown,
		progress,
	)
	loc := clamp(strength+noise-emberProgress, 0.0, 1.0)
	glow := 0.5 + 0.5*sin((s.GlowSpeed*progress)+loc*10.0)

	// General amount of ember.
	emberAmount = clamp(loc*glow, 0.0, 1.0)

	// Influence on fg and bg.
	bgAmount := emberAmount
	fgAmount := emberAmount

	// Chars glitch depending on ember location.
	char = s.Chars[int((loc)*(float(len(s.Chars))-1))]
	if char == ' ' {
		char = cell.Ch
	}

	// Cut some parts which we mark as sparks and draw glittery stars on them.
	sparksLoc := (1.0 - (0.5 + 0.5*noise)) * noiseSimplex(p.multSc(9.0))

	if sparksLoc >= (-1.0 + 2.0*float(1.0-s.SparksAmount)) {
		// Spark locations outside the ember blobs, scattered in ashes.
		char = cell.Ch
		if cell.Width == 0 { // show ash characters on empty cells
			// A different one every step, so they flicker. Each cell
			// flips at its own point of the step so that, over a long
			// run, they do not all flip on the same frame.
			phase := float(hashCell(cellX, cellY, -1)) / (1 << 32)
			step := int(math.Floor(progress*tunedSteps + phase))
			ashes := uint32(len(s.Chars) - 1)
			if ashes == 0 {
				ashes = 1
			}
			char = s.Chars[hashCell(cellX, cellY, step)%ashes]
			cells[cellY][cellX].Width = 1
		}
		const kBreakPatternX = 389
		const kBreakPatternY = 123
		sp := 0.5*sin(
			(s.SparksSpeed*progress)/(3.0+sparksLoc*4.0)+
				float(cellX*kBreakPatternX+cellY*kBreakPatternY)) + 0.5
		fg = mix3D(
			colToVec(s.Colors.Sparks2, s.defaultAttr.Fg),
			colToVec(s.Colors.Sparks1, s.defaultAttr.Fg),
			sp,
		)

	} else {
		// Non-spark characters living inside the ember blobs.
		fg = mix3D(
			colToVec(s.Colors.Fg1, s.defaultAttr.Fg),
			colToVec(s.Colors.Fg2, s.defaultAttr.Fg),
			fgAmount,
		)
		hotRings = fgAmount
		const kNonSparkHotRingThreshold = 0.33
		const kNonSparkHotRingMult = 3
		if hotRings > kNonSparkHotRingThreshold {
			hotRings = 1.0
		} else {
			hotRings *= kNonSparkHotRingMult
			if hotRings > 1.0 {
				hotRings = 1.0
			}
		}
	}

	bg = mix3D(
		colToVec(s.Colors.Bg2, s.defaultAttr.Bg),
		colToVec(s.Colors.Bg1, s.defaultAttr.Bg),
		0.5+0.5*sin(3.0*bgAmount),
	)

	return
}

// hashCell scrambles a cell's coordinates and a step of time into
// bits that look random but are always the same for the same input.
func hashCell(x, y, step int) uint32 {
	h := uint32(x)*0x8da6b343 ^ uint32(y)*0xd8163841 ^ uint32(step)*0xcb1ab31f
	h ^= h >> 13
	h *= 0x5bd1e995
	h ^= h >> 15
	return h
}

func (s *embers) ashes(p vec2D) (fg vec3D, bg vec3D) {
	const (
		kScale1           = 0.9
		kScale2           = 1.7
		kScale3           = 8.0
		kAmplitude3       = 0.7
		kFgBase           = 80.0
		kFgNoiseAmplitude = 60.0
		kFgNoiseScale     = 12.0
	)

	// Stack some noises.
	af := clamp(noiseSimplex01(p.multSc(kScale1)), 0.0, 1.0)
	af += -0.5 + clamp(noiseSimplex01(p.multSc(kScale2)), 0.0, 1.0)
	af += kAmplitude3 * (-0.5 + clamp(noiseSimplex01(p.multSc(kScale3)), 0.0, 1.0))
	af = clamp(af, 0.0, 1.0)

	bg = mix3D(colToVec(s.Colors.Ashes, s.defaultAttr.Bg), vec3FromScalar(0.0), af)
	fg = bg.addSc(kFgBase + kFgNoiseAmplitude*noiseSimplex(p.multSc(kFgNoiseScale)))

	return fg, bg
}
