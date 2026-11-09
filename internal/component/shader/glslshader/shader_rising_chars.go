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
	"math/rand"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
)

// RisingChars produce ascending particles that displacing the original
// characters. The traveling character ends up landing on the original position
// producing a kind of alien abduction effect.
//
// Which characters travel and from where only depends on the size of the
// canvas, so every frame of a run picks the same ones.
func RisingChars(
	params RisingCharsParams, defaultAttr term.Attributes,
) shader.Shader {
	params.DurationPercTravelling = clamp(params.DurationPercTravelling, 0.0, 1.0)
	params.DurationPercStillness = clamp(params.DurationPercStillness, 0.0, 1.0)

	// Parameters that represent percentage of an animation between 0% (0.0)
	// and 100% (1.0) do not make sense to exceed 1.0 when added.
	if (params.DurationPercTravelling + params.DurationPercStillness) > 1.0 {
		panic("DurationPercTravelling and DurationPercStillness mustn't exceed 1.0 " +
			"(100% of the animation)",
		)
	}

	params.TravelDistance *= 70.0   // from 0..1 to 0..70 frames of risingCharsTunedFrames
	params.CharFgFlickerSpeed *= 20 // from 0..1 to 0..20

	return &risingChars{
		RisingCharsParams: params,
		defaultAttr:       defaultAttr,
	}
}

// RisingCharsParams allows you to customize the RisingChars effect.
//
// Most parameters are in the normalized range 0-1. Exceeding the range is
// allowed in most of the cases (it's specified in the docstring).
type RisingCharsParams struct {
	// DurationPercTravelling (in percentage) sets how long the characters are
	// in motion and (DurationPercStillness) is the time they stay after
	// landing to their destination.
	// (range: 0..1 clamped (0% to 100%) and shouldn't exceed 1.0 when added)
	DurationPercTravelling, DurationPercStillness float
	// How far apart in time the characters set off, so they don't all
	// walk the same fraction of their path at once: at 1, up to 23% of
	// the run earlier or later.
	// (range: 0..1 unrestricted)
	TravelDistance float
	// The speed at which the travelling character foreground color is
	// animated.
	// (range: 0..1 unrestricted)
	CharFgFlickerSpeed float
}

// DefaultRisingCharsParams gives you a set of params that exhibits the
// RisingChars effect properly. It's a good starting point to customizing on
// top of with parameter overrides.
func DefaultRisingCharsParams() RisingCharsParams {
	return RisingCharsParams{
		DurationPercTravelling: 0.57,
		DurationPercStillness:  0.43,
		TravelDistance:         0.71,
		CharFgFlickerSpeed:     0.8,
	}
}

type risingChars struct {
	defaultAttr term.Attributes
	RisingCharsParams
	// candidates are the character translations of the last canvas size
	// shaded, which only depend on that size. A frame drops those whose
	// destination carries no character.
	candidates []charTx
	w, h       int
}

// risingCharsTunedFrames is the length of the run, in frames, whose
// frames TravelDistance was tuned in: sshshop's.
const risingCharsTunedFrames = 300.0

func (s *risingChars) Shade(frame, total int, cells [][]term.Cell) {
	if total <= 0 || frame < 0 || frame >= total || len(cells) == 0 {
		return
	}
	progress := float(frame) / float(total)
	txs := s.translations(progress, cells)
	for y := range cells {
		for x := 0; x < len(cells[0]); x++ {
			s.processCell(x, y, progress, txs, cells)
		}
	}
}

// translations returns where the characters of cells travelling to
// their destination are at progress, in [0, 1), into the run.
func (s *risingChars) translations(progress float, cells [][]term.Cell) []charTx {
	w := len(cells[0])
	h := len(cells)
	if s.candidates == nil || s.w != w || s.h != h {
		s.candidates = s.makeCandidates(w, h)
		s.w, s.h = w, h
	}

	txs := make([]charTx, 0, len(s.candidates))
	for _, tx := range s.candidates {
		if tx.dest.X >= len(cells[tx.dest.Y]) {
			continue
		}
		// do not bother transforming empty cells
		dest := cells[tx.dest.Y][tx.dest.X]
		if dest.Ch == ' ' || dest.Width == 0 {
			continue
		}
		// The characters travel for DurationPercTravelling of the run,
		// and then rest for DurationPercStillness till it ends.
		left := 1.0 - progress - s.DurationPercStillness - tx.stagger
		t := 1.0 - left/s.DurationPercTravelling
		t = clamp(t, 0.0, 1.0)
		t = 1.0 - (1.0-t)*(1.0-t)

		tx.curr = term.Coordinates{
			X: int(math.Round(t*float(tx.dest.X) + (1.0-t)*float(tx.orig.X))),
			Y: int(math.Round(t*float(tx.dest.Y) + (1.0-t)*float(tx.orig.Y))),
		}
		tx.destCh = dest.Ch
		txs = append(txs, tx)
	}
	return txs
}

// makeCandidates picks the translations of a canvas w by h at random,
// though the same ones every time for the same size.
func (s *risingChars) makeCandidates(w, h int) []charTx {
	rng := rand.New(rand.NewSource(0))
	candidates := make([]charTx, 0, w*h/7)
	for range w * h / 7 {
		ox := rng.Intn(w)
		oy := rng.Intn(h)
		staggerFrames := int(s.TravelDistance * noiseSimplex(
			vec2(float(ox), float(oy)).div(vec2(float(w), float(h))).multSc(10.0)),
		)
		candidates = append(candidates, charTx{
			dest:    term.Coordinates{X: ox, Y: oy},
			orig:    term.Coordinates{X: ox, Y: oy + 5 + rng.Intn(h)},
			stagger: float(staggerFrames) / risingCharsTunedFrames,
		})
	}
	return candidates
}

// processCell shades the cell at x, y with the characters travelling in
// txs, flickering them as they are progress into the run.
func (s *risingChars) processCell(
	x, y int, progress float, txs []charTx, cells [][]term.Cell,
) {
	if y >= len(cells) || x >= len(cells[y]) {
		return
	}

	fg := cells[y][x].Fg
	bg := cells[y][x].Bg
	char := cells[y][x].Ch

	w := len(cells[0])
	h := len(cells)

	// this is run by all cells and they check if they are either the source or
	// the current value that is being interpolated
	const kNoiseScale = 3.0
	for _, charTx := range txs {
		cellIsTravelling := x == charTx.curr.X && y == charTx.curr.Y
		cellIsDestination := x == charTx.dest.X && y == charTx.dest.Y
		if cellIsTravelling {
			char = charTx.destCh
			n := noiseSimplex(vec2(
				kNoiseScale*float(charTx.dest.X)/float(w),
				kNoiseScale*float(charTx.dest.Y)/float(h),
			))
			fg = vecToCol(
				mix3D(
					colToVec(fg, s.defaultAttr.Fg),
					vec3FromScalar(255.0*(0.5+0.5*n)),
					0.5+0.5*sin(s.CharFgFlickerSpeed*math.Pi*progress+
						float(charTx.dest.X+charTx.dest.Y))),
			)
		} else if cellIsDestination {
			char = ' '
		}
	}

	cells[y][x].Fg = fg
	cells[y][x].Bg = bg
	cells[y][x].Ch = char

	// A cell with a character to be visible must have width set.
	if char != rune(0) {
		cells[y][x].Width = 1
	}
}

// charTx represents a character moving around screen from one point to another
// (translation, viz. Tx)
type charTx struct {
	// The destination of this character to land on.
	dest term.Coordinates
	// The actual character at the destination at the very moment, that way if
	// it changes it reflects into the moving char too.
	destCh rune
	// The point the traveling character is currently at.
	curr term.Coordinates
	// The original position it departed from.
	orig term.Coordinates
	// What fraction of the run to offset this travel from orig to dest.
	// This breaks up the progress of the concurrent travelers so that all
	// characters don't walk the same percentage along the path at once.
	stagger float
}
