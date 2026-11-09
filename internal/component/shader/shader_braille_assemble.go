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

package shader

import (
	"image"
	"math"
	"math/rand/v2"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/braille"
)

// BrailleAssembleParams configures [BrailleAssemble]. Times are fractions
// of the animation's length.
type BrailleAssembleParams struct {
	// Seed fixes where dots start and when they depart, so equal params
	// replay the same animation.
	Seed uint64
	// Hold is how long the dots idle on the starting line before the
	// first one departs.
	Hold float64
	// Launch spreads the departures: the last dot departs at Hold+Launch.
	Launch float64
	// Travel is how long each dot flies. Every dot has landed by
	// Hold+Launch+Travel; from then on cells are left as drawn.
	Travel float64
	// Swirl bends flight paths sideways by this fraction of the distance
	// flown, so dots spiral in rather than fly straight.
	Swirl float64
	// Lines are where the dots start, spread along them by length and in
	// order, so the dots on the first line depart first. A line of zero
	// length is a point the dots burst out of. Without lines, the dots
	// start where they land and only twinkle and brighten in place.
	Lines []BrailleLine
}

// BrailleLine is a segment from X0, Y0 to X1, Y1, in fractions of the
// area the dots fly in: 0, 0 is its top left corner and 1, 1 its bottom
// right. Points outside 0..1 are off that area, so the dots starting there
// show once they fly into it. The area follows the window's shape, and so
// does the angle of a line.
type BrailleLine struct {
	X0, Y0, X1, Y1 float64
}

// DefaultBrailleAssembleParams returns the params of the Rune intro.
func DefaultBrailleAssembleParams() BrailleAssembleParams {
	return BrailleAssembleParams{
		Seed:   1,
		Hold:   0.12,
		Launch: 0.33,
		Travel: 0.38,
		Swirl:  0.35,
		// A line across the area that wraps around its right edge once.
		Lines: []BrailleLine{
			{X0: 0.62, Y0: 0.04, X1: 1, Y1: 0.3896},
			{X0: 0, Y0: 0.3896, X1: 0.62, Y1: 0.96},
		},
	}
}

const (
	// brailleLineDrift is how far along the lines idle dots slide over
	// the whole animation.
	brailleLineDrift = 0.04
	// brailleIdleBrightness is how bright idle dots are relative to their
	// landed color.
	brailleIdleBrightness = 0.35
	// brailleFadeIn is how long the starting line takes to appear.
	brailleFadeIn = 0.04
	// brailleTwinkleFrames is how many frames an idle dot stays shown or
	// hidden before it twinkles again.
	brailleTwinkleFrames = 3
)

// BrailleAssemble is a Shader that assembles the braille art on screen,
// such as a wallpaper drawn by the braille package, out of flying dots.
//
// The art's dots start out twinkling along the lines of params across the
// area spanned by [braille.Blank] cells, then fly along curved paths to
// their places, brightening as they land. A dot keeps the color of the
// cell it lands in.
//
// Only braille cells change, so whatever is drawn over the art, such as a
// prompt, hides the dots passing under it. Once every dot has landed the
// cells are left untouched: the animation ends on the screen as drawn.
// Screens without blank braille cells are left untouched throughout.
func BrailleAssemble(params BrailleAssembleParams, defaultAttr term.Attributes) Shader {
	return &brailleAssemble{BrailleAssembleParams: params, defaultAttr: defaultAttr}
}

type brailleAssemble struct {
	BrailleAssembleParams
	defaultAttr term.Attributes

	// field is the area, in cells, that the dots fly in. The dots below
	// were collected from it.
	field image.Rectangle
	dots  []brailleDot
	// start is where the dots start in field.
	start brailleStart
	// twinkle is the chance that an idle dot shows in a frame, tuned so
	// about half the starting lines are lit however many dots share them.
	twinkle float64
	// bg is the default background idle dots are dimmed toward.
	bg    [3]float64
	bgRGB bool

	// Per frame scratch, one entry per field cell.
	bits []rune
	sums []brailleCellSum
}

type brailleDot struct {
	// line is the dot's starting position along the lines, in 0..1.
	line float64
	// tx, ty is where the dot lands, in dots from the field's origin.
	tx, ty float64
	// cell is the index of the field cell the dot lands in.
	cell   int
	color  term.Color
	rgb    [3]float64
	isRGB  bool
	depart float64
	swirl  float64
	seed   uint32
}

type brailleCellSum struct {
	rgb      [3]float64
	n        int
	fallback term.Color
	// blend is set when a dot in flight or bound to another cell is in
	// the cell, so its color must be mixed rather than kept.
	blend bool
}

func (s *brailleAssemble) Shade(frame, total int, cells [][]term.Cell) {
	if frame < 0 || frame >= total {
		return
	}
	field, ok := blankBrailleBounds(cells)
	if !ok {
		return
	}
	if field != s.field {
		s.collect(field, cells)
	}
	progress := float64(frame) / float64(total)
	if progress >= s.Hold+s.Launch+s.Travel {
		return
	}
	s.draw(progress, frame, cells)
}

// blankBrailleBounds returns the smallest rectangle holding every
// [braille.Blank] cell.
func blankBrailleBounds(cells [][]term.Cell) (image.Rectangle, bool) {
	var r image.Rectangle
	for y, row := range cells {
		for x, c := range row {
			if c.Ch == braille.Blank {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r, !r.Empty()
}

// collect turns every raised dot of the braille cells in field into a
// flying dot.
func (s *brailleAssemble) collect(field image.Rectangle, cells [][]term.Cell) {
	s.field = field
	s.dots = s.dots[:0]
	rng := rand.New(rand.NewPCG(s.Seed, 0x9e3779b97f4a7c15))
	s.bg, s.bgRGB = brailleRGB(s.defaultAttr.Bg)
	fg, fgRGB := brailleRGB(s.defaultAttr.Fg)

	for y := field.Min.Y; y < field.Max.Y && y < len(cells); y++ {
		for x := field.Min.X; x < field.Max.X && x < len(cells[y]); x++ {
			c := cells[y][x]
			if !braille.IsPattern(c.Ch) || c.Ch == braille.Blank {
				continue
			}
			rgb, isRGB := brailleRGB(c.Fg)
			if c.Fg == term.ColorDefault {
				rgb, isRGB = fg, fgRGB
			}
			for dy := range braille.CellDotsY {
				for dx := range braille.CellDotsX {
					if c.Ch&braille.Bit(dx, dy) == 0 {
						continue
					}
					line := rng.Float64()
					s.dots = append(s.dots, brailleDot{
						line:   line,
						tx:     float64((x-field.Min.X)*braille.CellDotsX + dx),
						ty:     float64((y-field.Min.Y)*braille.CellDotsY + dy),
						cell:   (y-field.Min.Y)*field.Dx() + x - field.Min.X,
						color:  c.Fg,
						rgb:    rgb,
						isRGB:  isRGB,
						depart: s.Hold + s.Launch*(0.6*line+0.4*rng.Float64()),
						swirl:  s.Swirl * (0.5 + rng.Float64()),
						seed:   rng.Uint32(),
					})
				}
			}
		}
	}

	w := float64(field.Dx() * braille.CellDotsX)
	h := float64(field.Dy() * braille.CellDotsY)
	s.start = newBrailleStart(s.Lines, w, h)
	perPosition := float64(len(s.dots)) / math.Max(1, s.start.length)
	s.twinkle = 1
	if perPosition > 1 {
		s.twinkle = 1 - math.Pow(0.5, 1/perPosition)
	}

	n := field.Dx() * field.Dy()
	if cap(s.bits) < n {
		s.bits = make([]rune, n)
		s.sums = make([]brailleCellSum, n)
	}
	s.bits = s.bits[:n]
	s.sums = s.sums[:n]
}

func (s *brailleAssemble) draw(progress float64, frame int, cells [][]term.Cell) {
	clear(s.bits)
	clear(s.sums)
	cols := s.field.Dx()
	w, h := cols*braille.CellDotsX, s.field.Dy()*braille.CellDotsY
	fadeIn := math.Min(1, progress/brailleFadeIn)

	for i := range s.dots {
		d := &s.dots[i]
		x, y, flight := d.position(progress, s.Travel, s.start)
		if flight == 0 && d.twinkle(frame) >= s.twinkle {
			continue
		}
		ix, iy := int(math.Floor(x+0.5)), int(math.Floor(y+0.5))
		if ix < 0 || iy < 0 || ix >= w || iy >= h {
			continue
		}
		cell := iy/braille.CellDotsY*cols + ix/braille.CellDotsX
		s.bits[cell] |= braille.Bit(ix%braille.CellDotsX, iy%braille.CellDotsY)
		sum := &s.sums[cell]
		if flight < 1 || d.cell != cell {
			sum.blend = true
		}
		if !d.isRGB {
			sum.fallback = d.color
			continue
		}
		bright := fadeIn * (brailleIdleBrightness + (1-brailleIdleBrightness)*flight)
		for c := range sum.rgb {
			v := d.rgb[c]
			if s.bgRGB {
				v = s.bg[c] + (v-s.bg[c])*bright
			}
			sum.rgb[c] += v
		}
		sum.n++
	}

	for y := s.field.Min.Y; y < s.field.Max.Y && y < len(cells); y++ {
		for x := s.field.Min.X; x < s.field.Max.X && x < len(cells[y]); x++ {
			c := &cells[y][x]
			if !braille.IsPattern(c.Ch) {
				continue
			}
			i := (y-s.field.Min.Y)*cols + x - s.field.Min.X
			c.Ch = braille.Blank | s.bits[i]
			sum := s.sums[i]
			switch {
			case !sum.blend:
			case sum.n > 0:
				n := float64(sum.n)
				c.Fg = term.NewRGBColor(
					int32(math.Round(sum.rgb[0]/n)),
					int32(math.Round(sum.rgb[1]/n)),
					int32(math.Round(sum.rgb[2]/n)))
			default:
				c.Fg = sum.fallback
			}
		}
	}
}

// position returns where the dot is, in dots from the field's origin, and
// how far along its flight it is, from 0 before departure to 1 once landed.
func (d *brailleDot) position(progress, travel float64, start brailleStart) (x, y, flight float64) {
	line := d.line + brailleLineDrift*math.Min(progress, d.depart)
	line -= math.Floor(line)
	sx, sy, ok := start.at(line)
	if !ok {
		sx, sy = d.tx, d.ty
	}

	flight = 1
	if travel > 0 {
		flight = math.Max(0, math.Min(1, (progress-d.depart)/travel))
	}
	e := easeInOutCubic(flight)
	// Quadratic Bézier whose control point sits off the straight path,
	// perpendicular to it.
	dx, dy := d.tx-sx, d.ty-sy
	cx := sx + dx/2 - dy*d.swirl
	cy := sy + dy/2 + dx*d.swirl
	a, b, c := (1-e)*(1-e), 2*(1-e)*e, e*e
	return a*sx + b*cx + c*d.tx, a*sy + b*cy + c*d.ty, flight
}

// brailleStart is the starting lines laid end to end, in dots.
type brailleStart struct {
	lines []BrailleLine
	// ends are where each line ends along the whole, in 0..1.
	ends   []float64
	length float64
}

// newBrailleStart lays lines out in an area of w by h dots.
func newBrailleStart(lines []BrailleLine, w, h float64) brailleStart {
	s := brailleStart{
		lines: make([]BrailleLine, len(lines)),
		ends:  make([]float64, len(lines)),
	}
	for i, l := range lines {
		s.lines[i] = BrailleLine{X0: l.X0 * w, Y0: l.Y0 * h, X1: l.X1 * w, Y1: l.Y1 * h}
		s.length += math.Hypot(s.lines[i].X1-s.lines[i].X0, s.lines[i].Y1-s.lines[i].Y0)
		s.ends[i] = s.length
	}
	for i := range s.ends {
		if s.length > 0 {
			s.ends[i] /= s.length
		} else {
			// Points share the dots evenly.
			s.ends[i] = float64(i+1) / float64(len(s.ends))
		}
	}
	return s
}

// at returns the point t, in 0..1, along the lines, reporting false when
// there are none.
func (s brailleStart) at(t float64) (x, y float64, ok bool) {
	begin := 0.0
	for i, end := range s.ends {
		if t < end || i == len(s.ends)-1 {
			u := 0.0
			if end > begin {
				u = (t - begin) / (end - begin)
			}
			l := s.lines[i]
			return l.X0 + (l.X1-l.X0)*u, l.Y0 + (l.Y1-l.Y0)*u, true
		}
		begin = end
	}
	return 0, 0, false
}

// twinkle returns a pseudo-random value in [0, 1) that changes every
// brailleTwinkleFrames frames.
func (d *brailleDot) twinkle(frame int) float64 {
	return brailleHash(d.seed ^ uint32(frame/brailleTwinkleFrames)*0x9e3779b1)
}

// brailleHash maps h to a pseudo-random value in [0, 1).
func brailleHash(h uint32) float64 {
	h ^= h >> 16
	h *= 0x7feb352d
	h ^= h >> 15
	h *= 0x846ca68b
	h ^= h >> 16
	return float64(h) / (1 << 32)
}

func easeInOutCubic(t float64) float64 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	u := -2*t + 2
	return 1 - u*u*u/2
}

func brailleRGB(c term.Color) ([3]float64, bool) {
	if c.Hex() < 0 {
		return [3]float64{}, false
	}
	r, g, b := c.RGB()
	return [3]float64{float64(r), float64(g), float64(b)}, true
}
