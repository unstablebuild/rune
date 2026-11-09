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
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
)

// Incendium creates flames that turn screen into embers and ashes while sparks
// glitter rising while dragging some characters along the way.
//
// This shader is not a new creation but a combination of the following ones:
// - glslshader.Flames
// - glslshader.Embers
// - glslshader.RisingChars
//
// Every timing is a fraction of the run, so a longer run plays the same
// incendium slower.
func Incendium(
	params IncendiumParams, defaultAttr term.Attributes,
) shader.Shader {
	shFlames := Flames(params.Flames, defaultAttr).(*flames)
	shEmbers := Embers(params.Embers, defaultAttr).(*embers)
	shRisingChars := RisingChars(params.RisingChars, defaultAttr).(*risingChars)
	return &incendium{
		IncendiumParams:   params,
		shaderFlames:      shFlames,
		shaderEmbers:      shEmbers,
		shaderRisingChars: shRisingChars,
	}
}

// IncendiumParams allows you to customize the Incendium effect.
//
// Most parameters are in the normalized range 0-1. Exceeding the range is
// allowed in most of the cases (it's specified in the docstring).
type IncendiumParams struct {
	ExtraFlames float // 0..1 brings out more of the flames visual component
	Flames      FlamesParams
	Embers      EmbersParams
	RisingChars RisingCharsParams
}

// DefaultIncendiumParams gives you a set of params that exhibits the Incendium
// effect properly. It's a good starting point to customizing on top of with
// parameter overrides.
func DefaultIncendiumParams() IncendiumParams {
	flamesParams := DefaultFlamesParams()
	embersParams := DefaultEmbersParams()
	risingChars := DefaultRisingCharsParams()

	return IncendiumParams{
		Flames:      flamesParams,
		Embers:      embersParams,
		RisingChars: risingChars,
	}
}

type incendium struct {
	IncendiumParams
	shaderFlames      *flames
	shaderEmbers      *embers
	shaderRisingChars *risingChars
}

func (s *incendium) Shade(frame, total int, cells [][]term.Cell) {
	if total <= 0 || frame < 0 || frame >= total ||
		len(cells) == 0 || len(cells[0]) == 0 {
		return
	}

	// NOTE: Why not simply running the shade of each inner shader? Here's why:
	//
	// Instead of running shaders sequentially respecting each range:
	//
	//	s.shaderFlames.Shade(frame, total, cells)
	//	s.shaderEmbers.Shade(frame, total, cells)
	//	s.shaderRisingChars.Shade(frame, total, cells)
	//
	//	... or instead of using a Transition shader like this:
	//
	//	s.timeline := shader.TransitionCrossFade(s.shaderFlames, s.shaderEmbers, ...)
	//
	// ... the design decision for the Incendium shader is to be aware of what
	// shader to render on a per-cell basis. This allows starting the second
	// animation only if the first has finished. It produces nicer-looking
	// result (since it blends nicer) and it's more performant (since you don't
	// throw renders away by painting on top of existing paint).

	progress := float(frame) / float(total)
	step := progress * tunedSteps
	// The flames burn slower so more of them show before the embers take
	// over.
	speed := flamesSpeed(1 + 2*s.ExtraFlames)
	table := s.shaderFlames.tableFor(len(cells[0]), len(cells))

	// Forget about Flames shader if all are done with the Flames animation.
	wouldRenderNoFlames := s.shaderFlames.wouldRenderNothing(table, step, speed)

	txs := s.shaderRisingChars.translations(progress, cells)

	for y := range cells {
		for x := 0; x < len(cells[0]); x++ {
			if x >= len(cells[y]) {
				break
			}

			// Don't bother rendering Flames shader if no cells would be changed.
			if !wouldRenderNoFlames {
				s.shaderFlames.processCell(table, x, y, step, speed, cells)
			}

			// Do not render embers if unless the incendium passed over the cells.
			if status, _ := s.shaderFlames.phase(table, x, y, step, speed); status != flamesPassed {
				continue
			}

			// The embers of a cell start a flame's worth of characters,
			// in steps, after its flame did.
			flamesDuration := float(len(s.Flames.Chars))
			delayed := (step - table.cells[y][x].offset - flamesDuration) / tunedSteps
			s.shaderEmbers.processCell(x, y, delayed, cells)

			// Have some characters rising up from the screen as it were light
			// ashes following the flames heat.
			s.shaderRisingChars.processCell(x, y, delayed, txs, cells)
		}
	}
}
