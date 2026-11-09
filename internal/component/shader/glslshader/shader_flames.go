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
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// Flames cross the screen from bottom to top.
//
// Every timing is a fraction of the run, so a longer run plays the same
// flames slower.
func Flames(params FlamesParams, defaultAttr term.Attributes) shader.Shader {
	// Change normalized parameters to suitable ranges.
	params.Fuel *= 7.0       // from 0..1 to 0..7
	params.JitteryBirth *= 3 // from 0..1 to 0..3
	params.NoiseAmount *= 10 // from 0..1 to 0..10
	params.NoiseSize *= 8    // from 0..1 to 0..8
	params.ClumpHeight *= 10 // from 0..1 to 0..10

	return &flames{
		defaultAttr:  defaultAttr,
		FlamesParams: params,
	}
}

// FlamesParams allows you to customize the Flames effect.
//
// Most parameters are in the normalized range 0-1. Exceeding the range is
// allowed in most of the cases (it's specified in the docstring).
type FlamesParams struct {
	// Characters to use for painting the flames (first coolest; last hottest).
	Chars []rune
	// Colors to use for painting the flames (first coolest; last hottest).
	ColorGradient []term.Color
	// Describe how the background opacity of the flame characters evolves
	// throughout the course of the animation.
	BgContour FlamesBgOpacityContour
	// Flames are kickstarted if the given cell has been marked to do allow it.
	// This marking is done progressively on time at the pace specified with
	// this parameter: if too low it might happen the flames struggles to run,
	// if too high the flames will run too fast not looking organic exposing the
	// base shape (defined with the belly and clump parameters).
	// (range 0..1 unrestricted)
	Fuel float // each 1/90 of the run, Fuel rows' worth of cells are activated
	// Amplifies the Fuel results by spacing out the markings that kickstart
	// the course of the flames.
	// (range 0..1 unrestricted)
	JitteryBirth float
	// A half-sine shape is drawn first, before adding the teeth or spikes that
	// give the fire look. This curve can be flatter or more pronounced based
	// on the magnitude of the value you pass.
	//
	// This value is the size of the belly (or dome) in pixels, positive values
	// give concave results and negative convex.
	Belly float
	// This noise also produces variation of how the flames advances in a
	// similar way to JitteryBirth but more stable and smoother, since it uses
	// a continuous noise (spatial-aware) instead of a random one.
	// (range 0..1 unrestricted)
	NoiseAmount float
	// Space resolution of the NoiseAmount: the smaller the larger the blobs.
	// (range 0..1 unrestricted)
	NoiseSize float
	// Number of base flamess (imagine shape of spikes or teeth) along the
	// horizontal axis that raises up "burning" the screen.
	Clumps float
	// How tall  those flamess (spikes or teeth) are.
	// (range 0..1 unrestricted)
	ClumpHeight float
}

// DefaultFlamesParams gives you a set of params that exhibits the Flames
// effect properly. It's a good starting point to customizing on top of with
// parameter overrides.
func DefaultFlamesParams() FlamesParams {
	return FlamesPresetBlobby()
}

// FlamesPresetBlobby makes big blobby blast on the flamess in convex shape.
func FlamesPresetBlobby() FlamesParams {
	return FlamesParams{
		Chars:         defaultFlamesChars(),
		ColorGradient: FlamesColorGradientRed(),
		BgContour: FlamesBgOpacityContour{
			inEdge0: 0.2, inEdge1: 0.4,
			outEdge0: 0.7, outEdge1: 0.9,
		},
		Fuel:         0.7,
		JitteryBirth: 1.0,
		Belly:        -25.0,
		NoiseAmount:  0.8,
		NoiseSize:    0.5,
		Clumps:       3.0,
		ClumpHeight:  0.7,
	}
}

// FlamesColorGradientRed provides natural fire-looking colors to grade the
// flames with.
//
// Aimed to be passed to FlamesParams.ColorGradient.
func FlamesColorGradientRed() []term.Color {
	return []term.Color{
		term.NewRGBColor(255, 255, 255),
		term.NewRGBColor(255, 247, 143),
		term.NewRGBColor(255, 247, 93),
		term.NewRGBColor(254, 101, 13),
		term.NewRGBColor(138, 0, 60),
		term.NewRGBColor(81, 1, 0),
		term.NewRGBColor(255, 20, 0),
	}
}

// FlamesBgOpacityContour defines a smooth soft opacity rise-fall through time:
// >             ____________
// >           -              -
// >          /                \   <- not linear but curvy! (see GLSL smoothstep)
// >         /                  \
// > _____ _-                    - _ _______
// >        A   B           C    D
// >
// > A: inEdge0, B: inEdge1, C: outEdge0, D: outEdge1
type FlamesBgOpacityContour struct {
	// Expressed in percentage 0.0-1.0 of the flame animation:
	// inEdge0 will be transparent up until inEdge1 which will be opaque.
	inEdge0, inEdge1 float
	// Expressed in percentage 0.0-1.0 of the flame animation:
	// outEdge0 will be opaque up until outEdge1 which will be transparent.
	outEdge0, outEdge1 float
}

type flames struct {
	FlamesParams
	defaultAttr term.Attributes
	// table is the activations and offsets of the last canvas size
	// shaded, which only depend on that size.
	table *flamesTable
}

// flamesTable is when each cell of a canvas may catch fire and when its
// flame starts, in steps of 1/tunedSteps of the run.
type flamesTable struct {
	w, h int
	// cells is indexed by screen row then column.
	cells [][]flamesCell
	// lastActivation and lastOffset are the largest activation and
	// offset of a cell that ever activates.
	lastActivation, lastOffset float
}

type flamesCell struct {
	// activation is the step after which the cell may burn, or +Inf if
	// it never does. Activations are handed out from the bottom of each
	// column up, the natural ascending nature of fire.
	activation float
	// offset is the step the cell's flame starts at. On top of the
	// activation it pulls and pushes the start to achieve the belly (or
	// dome) shape, the saw teeth for the fire look, and more organic
	// noises, ... It is not rounded to a whole step, so that over a long
	// run cells catch fire on every frame rather than in batches.
	offset float
}

func (s *flames) Shade(frame, total int, cells [][]term.Cell) {
	if total <= 0 || frame < 0 || frame >= total ||
		len(cells) == 0 || len(cells[0]) == 0 {
		return
	}

	speed := flamesSpeed(1)
	table := s.tableFor(len(cells[0]), len(cells))
	step := float(frame) / float(total) * s.runSteps(table, speed)
	if s.wouldRenderNothing(table, step, speed) {
		return
	}

	for y := range cells {
		for x := 0; x < len(cells[0]); x++ {
			s.processCell(table, x, y, step, speed, cells)
		}
	}
}

// flamesDoneBy is how far into a run the flames are over at the latest,
// so that its last frames show the canvas untouched, as its first does,
// and the run loops without a seam.
const flamesDoneBy = 0.95

// runSteps is how many steps a run spans: the ones the flames were tuned
// to, or more on a canvas too tall for those to see them over by
// flamesDoneBy.
func (s *flames) runSteps(table *flamesTable, speed float) float {
	end := math.Max(table.lastActivation, table.lastOffset+float(len(s.Chars)-1)/speed)
	return math.Max(tunedSteps, end/flamesDoneBy)
}

// flamesSpeed is how many characters of its animation a flame goes
// through per step, slowed down stretch times.
func flamesSpeed(stretch float) float {
	const charsPerStep = 1 / 0.43
	return charsPerStep / stretch
}

// phase tells whether the flame has not reached, is passing or has
// passed the cell at x, y at step, and when passing, how far along the
// characters of its animation it is.
func (s *flames) phase(table *flamesTable, x, y int, step, speed float) (
	status flamesCellStatus, at float,
) {
	c := table.cells[y][x]
	if c.activation >= step || step < c.offset {
		return flamesNotPassed, 0
	}
	at = speed * (step - c.offset)
	if math.Round(at) >= float(len(s.Chars)-1) {
		return flamesPassed, 0
	}
	return flamesPassing, at
}

func (s *flames) processCell(
	table *flamesTable, x, y int, step, speed float, cells [][]term.Cell,
) {
	if y >= len(cells) || x >= len(cells[y]) {
		return
	}

	status, at := s.phase(table, x, y, step, speed)

	var char rune
	var fg, bg term.Color

	// Only draw while the flame passes, within the bounds of the
	// animation characters.
	if status == flamesPassing {
		char = s.Chars[int(math.Round(at))]
		factor := at / float(len(s.Chars)-1)

		// Starts at 1.0 and after edge0 it starts decreasing up to edge1,
		// after which the result is 0.0.
		fadingTail := smoothstep(s.BgContour.outEdge1, s.BgContour.outEdge0, factor)

		// If dealing with a character-less cell the flame character should
		// fade into the same color of the background of that character-less
		// cell, but if there's a character we fade into that character's color
		// to prevent color jumps, otherwise you would have your flame
		// characters radically show the original character in another color
		// suddenly.
		if cells[y][x].Ch == rune(0) {
			fg = shaderutils.InterpolateColor(
				fadingTail,
				cells[y][x].Bg,
				s.sampleFlamesGradient(factor),
				s.defaultAttr.Bg,
			)
		} else {
			fg = shaderutils.InterpolateColor(
				fadingTail,
				cells[y][x].Bg,
				s.sampleFlamesGradient(factor),
				s.defaultAttr.Bg,
			)
		}

		// When drawing the flame chars, we paint the background with a slight
		// color offset, so characters that are not filling the entire cell
		// like quadrants ▙, ▚, ... become visually more interesting, that's
		// why we scale the color gradient from [0,1] to [0.2-0.8] (in the case
		// of kBgSampleStart=0.2 and kBgSampleScale=0.6).
		const (
			kBgSampleStart = 0.2
			kBgSampleScale = 0.6
		)
		bg = shaderutils.InterpolateColor(
			// At the beginning of the flame (0%) it will have the original
			// cell background (0.0) and progressively go opaque (1.0) towards
			// the middle (30%-40%) and then stay opaque and fade down again
			// towards the end, something like this:
			//
			//   0% (0) 20% (0) 30% (0.5) 40% (1.0) 50% (1.0)
			//   60% (1.0) 70% (1.0) 80% (0.5) 90% (0.0) 100% (0.0)
			//
			// Multiplying 0-1 ranges gives us logical intersection (an AND &&
			// boolean operation).
			fadingTail*smoothstep(s.BgContour.inEdge0, s.BgContour.inEdge1, factor),
			cells[y][x].Bg,
			// Mid part of the flame will paint some offset colors to make the
			// chars stand out as mentioned above (see docstring above consts).
			s.sampleFlamesGradient(kBgSampleStart+kBgSampleScale*factor),
			s.defaultAttr.Bg,
		)

		// If the flame animation char is not the fullblock (is fading) we can do something
		// more interesting than rendering a full cell. We replace it with the
		// original character it is covering and that character we give it a
		// color from the same gradient but mirroring across the middle, that
		// is, sampling from the end instead of from the start to make it stand
		// out more. And we darken it afterwards to avoid the ending of the
		// animation showing super bright foregrounds, since it should be
		// something ashy and burnt, that's why we dress it with a black fade.
		if char != blockChar {
			char = cells[y][x].Ch
			bg = fg
			const (
				kDarkenStart = 0.35
				kDarkenDone  = 0.6
			)
			const (
				kClearStart = 0.21
				kClearEnd   = 0.38
			)
			fg = shaderutils.InterpolateColor(
				smoothstep(kClearStart, kClearEnd, factor),
				fg,
				vecToCol(colToVec(
					s.sampleFlamesGradient(1.0-factor), s.defaultAttr.Fg).multSc(
					smoothstep(kDarkenDone, kDarkenStart, factor),
				)),
				s.defaultAttr.Fg,
			)
		}
		cells[y][x].Bg = bg
		cells[y][x].Fg = fg
		cells[y][x].Ch = char
		if char != rune(0) {
			cells[y][x].Width = 1
		}
	}
}

// sampleFlamesGradient returns the blended color at point t (0...1)
// interpolating any color steps, 0 being the start of the gradient and 1 the
// end.
func (s *flames) sampleFlamesGradient(t float) term.Color {
	t = math.Min(1, math.Max(0, t))
	stops := float(len(s.ColorGradient) - 1)
	tt := t * stops

	currIdx := int(math.Floor(tt))
	nextIdx := currIdx + 1
	if nextIdx >= len(s.ColorGradient) {
		nextIdx = len(s.ColorGradient) - 1
	}

	tDec := tt - math.Floor(tt)

	return shaderutils.InterpolateColor(
		tDec,
		s.ColorGradient[currIdx],
		s.ColorGradient[nextIdx],
		s.defaultAttr.Bg,
	)
}

// tableFor returns the table of a canvas w columns wide and h rows tall,
// reusing the last one built when it is of that size.
func (s *flames) tableFor(w, h int) *flamesTable {
	if s.table != nil && s.table.w == w && s.table.h == h {
		return s.table
	}
	activations := s.jitterActivations(w, h)
	table := &flamesTable{
		w: w, h: h,
		cells:          make([][]flamesCell, h),
		lastActivation: math.Inf(-1),
		lastOffset:     math.Inf(-1),
	}
	for y := range table.cells {
		table.cells[y] = make([]flamesCell, w)
		yy := h - y - 1 // complementary to flip vertical screen coords
		for x := range table.cells[y] {
			if len(activations[x]) <= yy {
				table.cells[y][x] = flamesCell{activation: math.Inf(1), offset: math.Inf(1)}
				continue
			}
			activation := activations[x][yy]
			offset := s.offsets(x, yy, w, h, activation)
			table.cells[y][x] = flamesCell{activation: activation, offset: offset}
			table.lastActivation = math.Max(table.lastActivation, activation)
			table.lastOffset = math.Max(table.lastOffset, offset)
		}
	}
	s.table = table
	return table
}

// jitterActivations hands out activations to random columns, Fuel rows'
// worth each step, until every cell of a w by h canvas has one, and
// returns the steps each column got its activations at. A step's
// activations are spread evenly across it, so that over a long run the
// fire does not advance in batches.
func (s *flames) jitterActivations(w, h int) [][]float {
	// One past the last column also draws activations, which go unused.
	activations := make([][]float, w+1)
	picks := int(s.Fuel * float(w))
	if picks <= 0 {
		return activations
	}
	rng := rand.New(rand.NewSource(0))
	for f, full := 0, 0; full < w; f++ {
		for i := range picks {
			col := rng.Intn(w + 1)
			if len(activations[col]) < h {
				activations[col] = append(activations[col], float(f)+float(i)/float(picks))
				if col < w && len(activations[col]) == h {
					full++
				}
			}
		}
	}
	return activations
}

// offsets is when the flame of the cell at x, yy from the bottom starts,
// in steps, for a canvas w by h and the cell's activation. The
// flame animation is simply showing the chars in FlamesParams.Chars one
// after the other. To give the illusion of fire we offset the chars to
// break the animation into different flames. The offset not only is when
// the flame starts but also conditions the location within the flame
// defining the general shape of the effect, that's why we speak in plural.
func (s *flames) offsets(x, yy, w, h int, activation float) float {
	// Offset: Non-spatial random.
	of1 := s.JitteryBirth * activation
	if s.JitteryBirth < 1.0 {
		of1 += (1.0 - s.JitteryBirth) * float(yy)
	}

	// Offset: Sine belly, lateral shows flames first and middle of scren last.
	of2 := s.Belly * sin(
		(math.Pi/2.0)*(float(x)-float(w)/2.0)/(float(w)/2.0)+ // freq
			(math.Pi/2.0), // offset
	)

	// Offset: [triangle waves]: https://thndl.com/triangle-waves.html.
	teethNum := s.Clumps
	teethHeight := s.ClumpHeight
	teeth := abs(-1.0 + 2.0*fract(((teethNum*float(x))/float(w))))
	teeth = 1.0 - (teeth-1.0)*(teeth-1.0) // sharp pointy ([timeshader.FastToSlow] algorithm)
	teeth *= teethHeight                  // from unit-scale to desired length
	of3 := teeth

	// Offset: break down for organic look.
	of4 := s.NoiseAmount * noiseSimplex(
		vec2(float(x)/float(w), float(yy)/float(h)).multSc(s.NoiseSize),
	)

	// Ofset: Combine them all.
	return of1 + of2 + of3 + of4
}

// wouldRenderNothing tells whether the flames have passed every cell
// of table by step, so shading would change nothing.
func (s *flames) wouldRenderNothing(table *flamesTable, step, speed float) bool {
	return step > table.lastActivation &&
		step >= table.lastOffset+float(len(s.Chars)-1)/speed
}

const blockChar = '█'

func defaultFlamesChars() []rune {
	return []rune{
		'░',
		'░',
		'▒',
		'▒',
		'▒',
		'▓',
		'▓',
		'▓',
		'▓',
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		blockChar,
		'▓',
		'▓',
		'▓',
		'▓',
		'▒',
		'▒',
		'▒',
		'░',
		'░',
	}
}

type flamesCellStatus int

const (
	flamesNotPassed flamesCellStatus = iota
	flamesPassing
	flamesPassed
)
