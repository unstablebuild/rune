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

package font

import (
	"image"
	"math"

	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

var _ font.Face = (*custom)(nil)

// clearRGBA zeroes an RGBA glyph scratch buffer for reuse across glyphs.
func clearRGBA(img *image.RGBA) {
	clear(img.Pix)
}

// handles special runes used to build (T)UIs for
// pixel perfect rendering.
type custom struct {
	width, height fixed.Int26_6
	face          font.Face
	offsetY       fixed.Int26_6
	boldFont      bool
	overlapX      int
	overlapY      int

	// re-use allocs
	mask    *image.RGBA
	altMask *image.RGBA
}

func newCustomFace(
	width, height, offsetY float64,
	standard font.Face, bold bool,
	overlapX, overlapY int,
) *custom {
	w := int(math.Max(width, 1))
	h := int(math.Max(height, 1))
	mask := image.NewRGBA(image.Rect(0, 0, w, h))
	altMask := image.NewRGBA(image.Rect(0, 0, w, h))
	return &custom{
		boldFont: bold,
		mask:     mask,
		altMask:  altMask,
		offsetY:  float64ToFixed(offsetY),
		face:     standard,
		width:    float64ToFixed(width),
		height:   float64ToFixed(height),
		// we want certain characters to take into consideration cell overlap pixels
		overlapX: overlapX,
		overlapY: overlapY,
	}
}

func (m *custom) Glyph(dot fixed.Point26_6, r rune) (
	dr image.Rectangle, mask image.Image,
	maskp image.Point, advance fixed.Int26_6, ok bool,
) {
	clearRGBA(m.mask)
	clearRGBA(m.altMask)

	dr = image.Rect(
		dot.X.Floor(),
		(dot.Y - m.offsetY).Floor(),
		(dot.X + m.width).Floor(),
		(dot.Y + m.height - m.offsetY).Floor(),
	)

	switch r {
	case '', '':
		dr, mask, maskp, advance, ok = m.face.Glyph(dot, r)
		if ok {
			dr = dr.Sub(image.Point{X: 4, Y: 0})
		}
		return
	case '', '', '', '', '', '':
		dr, mask, maskp, advance, ok = m.face.Glyph(dot, r)
		if ok {
			dr = dr.Sub(image.Point{X: 2, Y: 0})
		}
		return
	case '', '', '', '', '', '', '', '':
		dr, mask, maskp, advance, ok = m.face.Glyph(dot, r)
		if ok {
			dr = dr.Sub(image.Point{X: -1, Y: 0})
		}
		return
	case '':
		dr, mask, maskp, advance, ok = m.face.Glyph(dot, '')
		if ok {
			dr = dr.Sub(image.Point{X: 1, Y: 0})
		}
		return
	case '':
		dr, mask, maskp, advance, ok = m.face.Glyph(dot, '')
		if ok {
			dr = dr.Sub(image.Point{X: 4, Y: 0})
		}
		return
	case '\t':
		dr, mask, maskp, advance, ok = m.face.Glyph(dot, '')
		if ok {
			dr = dr.Sub(image.Point{X: 3, Y: 0})
		}
		return
	case '░':
		ok = m.shadeGlyph(dr, colorFillAlphaStep3)
	case '▒':
		ok = m.shadeGlyph(dr, colorFillAlphaStep2)
	case '▓':
		ok = m.shadeGlyph(dr, colorFillAlphaStep1)
	case '█':
		ok = m.shadeGlyph(dr, colorFill)
	case '─':
		ok = m.plusGlyph(dr, styleSingle, styleSingle, 0, 0)
	case '━':
		ok = m.plusGlyph(dr, styleBold, styleBold, 0, 0)
	case '╴':
		ok = m.plusGlyph(dr, styleSingle, 0, 0, 0)
	case '╶':
		ok = m.plusGlyph(dr, 0, styleSingle, 0, 0)
	case '╸':
		ok = m.plusGlyph(dr, styleBold, 0, 0, 0)
	case '╺':
		ok = m.plusGlyph(dr, 0, styleBold, 0, 0)
	case '│':
		ok = m.plusGlyph(dr, 0, 0, styleSingle, styleSingle)
	case '┃':
		ok = m.plusGlyph(dr, 0, 0, styleBold, styleBold)
	case '╵':
		ok = m.plusGlyph(dr, 0, 0, styleSingle, 0)
	case '╷':
		ok = m.plusGlyph(dr, 0, 0, 0, styleSingle)
	case '╹':
		ok = m.plusGlyph(dr, 0, 0, styleBold, 0)
	case '╻':
		ok = m.plusGlyph(dr, 0, 0, 0, styleBold)
	case '┌':
		ok = m.plusGlyph(dr, 0, styleSingle, 0, styleSingle)
	case '┍':
		ok = m.plusGlyph(dr, 0, styleBold, 0, styleSingle)
	case '┎':
		ok = m.plusGlyph(dr, 0, styleSingle, 0, styleBold)
	case '┏':
		ok = m.plusGlyph(dr, 0, styleBold, 0, styleBold)
	case '┐':
		ok = m.plusGlyph(dr, styleSingle, 0, 0, styleSingle)
	case '┑':
		ok = m.plusGlyph(dr, styleBold, 0, 0, styleSingle)
	case '┒':
		ok = m.plusGlyph(dr, styleSingle, 0, 0, styleBold)
	case '┓':
		ok = m.plusGlyph(dr, styleBold, 0, 0, styleBold)
	case '└':
		ok = m.plusGlyph(dr, 0, styleSingle, styleSingle, 0)
	case '┕':
		ok = m.plusGlyph(dr, 0, styleBold, styleSingle, 0)
	case '┖':
		ok = m.plusGlyph(dr, 0, styleSingle, styleBold, 0)
	case '┗':
		ok = m.plusGlyph(dr, 0, styleBold, styleBold, 0)
	case '┘':
		ok = m.plusGlyph(dr, styleSingle, 0, styleSingle, 0)
	case '┙':
		ok = m.plusGlyph(dr, styleBold, 0, styleSingle, 0)
	case '┚':
		ok = m.plusGlyph(dr, styleSingle, 0, styleBold, 0)
	case '┛':
		ok = m.plusGlyph(dr, styleBold, 0, styleBold, 0)
	case '├':
		ok = m.plusGlyph(dr, 0, styleSingle, styleSingle, styleSingle)
	case '┝':
		ok = m.plusGlyph(dr, 0, styleSingle, styleSingle, styleBold)
	case '┞':
		ok = m.plusGlyph(dr, 0, styleSingle, styleBold, styleSingle)
	case '┟':
		ok = m.plusGlyph(dr, 0, styleSingle, styleSingle, styleBold)
	case '┠':
		ok = m.plusGlyph(dr, 0, styleSingle, styleBold, styleBold)
	case '┡':
		ok = m.plusGlyph(dr, 0, styleBold, styleBold, styleSingle)
	case '┢':
		ok = m.plusGlyph(dr, 0, styleBold, styleSingle, styleBold)
	case '┣':
		ok = m.plusGlyph(dr, 0, styleBold, styleBold, styleBold)
	case '┤':
		ok = m.plusGlyph(dr, styleSingle, 0, styleSingle, styleSingle)
	case '┥':
		ok = m.plusGlyph(dr, styleBold, 0, styleSingle, styleSingle)
	case '┦':
		ok = m.plusGlyph(dr, styleSingle, 0, styleBold, styleSingle)
	case '┧':
		ok = m.plusGlyph(dr, styleSingle, 0, styleSingle, styleBold)
	case '┨':
		ok = m.plusGlyph(dr, styleSingle, 0, styleBold, styleBold)
	case '┩':
		ok = m.plusGlyph(dr, styleBold, 0, styleBold, styleSingle)
	case '┪':
		ok = m.plusGlyph(dr, styleBold, 0, styleSingle, styleBold)
	case '┫':
		ok = m.plusGlyph(dr, styleBold, 0, styleBold, styleBold)
	case '┬':
		ok = m.plusGlyph(dr, styleSingle, styleSingle, 0, styleSingle)
	case '┭':
		ok = m.plusGlyph(dr, styleBold, styleSingle, 0, styleSingle)
	case '┮':
		ok = m.plusGlyph(dr, styleSingle, styleBold, 0, styleSingle)
	case '┯':
		ok = m.plusGlyph(dr, styleBold, styleBold, 0, styleSingle)
	case '┰':
		ok = m.plusGlyph(dr, styleSingle, styleSingle, 0, styleBold)
	case '┱':
		ok = m.plusGlyph(dr, styleBold, styleSingle, 0, styleBold)
	case '┲':
		ok = m.plusGlyph(dr, styleSingle, styleBold, 0, styleBold)
	case '┳':
		ok = m.plusGlyph(dr, styleBold, styleBold, 0, styleBold)
	case '┴':
		ok = m.plusGlyph(dr, styleSingle, styleSingle, styleSingle, 0)
	case '┵':
		ok = m.plusGlyph(dr, styleBold, styleSingle, styleSingle, 0)
	case '┶':
		ok = m.plusGlyph(dr, styleSingle, styleBold, styleSingle, 0)
	case '┷':
		ok = m.plusGlyph(dr, styleBold, styleBold, styleSingle, 0)
	case '┸':
		ok = m.plusGlyph(dr, styleSingle, styleSingle, styleBold, 0)
	case '┹':
		ok = m.plusGlyph(dr, styleBold, styleSingle, styleBold, 0)
	case '┺':
		ok = m.plusGlyph(dr, styleSingle, styleBold, styleBold, 0)
	case '┻':
		ok = m.plusGlyph(dr, styleBold, styleBold, styleBold, 0)
	case '┼':
		ok = m.plusGlyph(dr, styleSingle, styleSingle, styleSingle, styleSingle)
	case '┽':
		ok = m.plusGlyph(dr, styleBold, styleSingle, styleSingle, styleSingle)
	case '┾':
		ok = m.plusGlyph(dr, styleSingle, styleBold, styleSingle, styleSingle)
	case '┿':
		ok = m.plusGlyph(dr, styleBold, styleBold, styleSingle, styleSingle)
	case '╀':
		ok = m.plusGlyph(dr, styleSingle, styleSingle, styleBold, styleSingle)
	case '╁':
		ok = m.plusGlyph(dr, styleSingle, styleSingle, styleSingle, styleBold)
	case '╂':
		ok = m.plusGlyph(dr, styleSingle, styleSingle, styleBold, styleBold)
	case '╃':
		ok = m.plusGlyph(dr, styleBold, styleSingle, styleBold, styleSingle)
	case '╄':
		ok = m.plusGlyph(dr, styleSingle, styleBold, styleBold, styleSingle)
	case '╅':
		ok = m.plusGlyph(dr, styleBold, styleSingle, styleSingle, styleBold)
	case '╆':
		ok = m.plusGlyph(dr, styleSingle, styleBold, styleSingle, styleBold)
	case '╇':
		ok = m.plusGlyph(dr, styleBold, styleBold, styleBold, styleSingle)
	case '╈':
		ok = m.plusGlyph(dr, styleBold, styleBold, styleSingle, styleBold)
	case '╉':
		ok = m.plusGlyph(dr, styleBold, styleSingle, styleBold, styleBold)
	case '╊':
		ok = m.plusGlyph(dr, styleSingle, styleBold, styleBold, styleBold)
	case '╋':
		ok = m.plusGlyph(dr, styleBold, styleBold, styleBold, styleBold)
	case '╼':
		ok = m.plusGlyph(dr, styleSingle, styleBold, 0, 0)
	case '╾':
		ok = m.plusGlyph(dr, styleBold, styleSingle, 0, 0)
	case '╽':
		ok = m.plusGlyph(dr, 0, 0, styleSingle, styleBold)
	case '╿':
		ok = m.plusGlyph(dr, 0, 0, styleBold, styleSingle)
	case '╯':
		ok = m.arcPlusGlyph(dr, styleSingle, 0, 0, 0)
	case '╰':
		ok = m.arcPlusGlyph(dr, 0, styleSingle, 0, 0)
	case '╮':
		ok = m.arcPlusGlyph(dr, 0, 0, styleSingle, 0)
	case '╭':
		ok = m.arcPlusGlyph(dr, 0, 0, 0, styleSingle)
	case '┄':
		ok = m.dottedHorizontalGlyph(dr, styleSingle, 2)
	case '┅':
		ok = m.dottedHorizontalGlyph(dr, styleBold, 2)
	case '┈':
		ok = m.dottedHorizontalGlyph(dr, styleSingle, 3)
	case '┉':
		ok = m.dottedHorizontalGlyph(dr, styleBold, 3)
	case '╌':
		ok = m.dottedHorizontalGlyph(dr, styleSingle, 1)
	case '╍':
		ok = m.dottedHorizontalGlyph(dr, styleBold, 1)
	case '┆':
		ok = m.dottedVerticalGlyph(dr, styleSingle, 2)
	case '┇':
		ok = m.dottedVerticalGlyph(dr, styleBold, 2)
	case '┊':
		ok = m.dottedVerticalGlyph(dr, styleSingle, 3)
	case '┋':
		ok = m.dottedVerticalGlyph(dr, styleBold, 3)
	case '╎':
		ok = m.dottedVerticalGlyph(dr, styleSingle, 1)
	case '╏':
		ok = m.dottedVerticalGlyph(dr, styleBold, 1)
	case '▉':
		ok = m.blockGlyph(dr, [][]bool{{true, true, true, true, true, true, true, false}})
	case '▊':
		ok = m.blockGlyph(dr, [][]bool{{true, true, true, false}})
	case '▋':
		ok = m.blockGlyph(dr, [][]bool{{true, true, true, true, true, false, false, false}})
	case '▌':
		ok = m.blockGlyph(dr, [][]bool{{true, false}})
	case '▍':
		ok = m.blockGlyph(dr, [][]bool{{true, true, true, false, false, false, false, false}})
	case '▎':
		ok = m.blockGlyph(dr, [][]bool{{true, false, false, false}})
	case '▏':
		ok = m.blockGlyphOuterSquare(dr, 0, 0, styleSingle, 0)
	case '▔':
		ok = m.blockGlyphOuterSquare(dr, styleSingle, 0, 0, 0)
	case '▀':
		ok = m.blockGlyph(dr, [][]bool{{true}, {false}})
	case '▁':
		ok = m.blockGlyphOuterSquare(dr, 0, styleSingle, 0, 0)
	case '▂':
		ok = m.blockGlyph(dr, [][]bool{
			{false}, {false}, {false}, {true},
		})
	case '▃':
		ok = m.blockGlyph(dr, [][]bool{
			{false}, {false}, {false}, {false}, {false}, {true}, {true}, {true},
		})
	case '▄':
		ok = m.blockGlyph(dr, [][]bool{{false}, {true}})
	case '▅':
		ok = m.blockGlyph(dr, [][]bool{
			{false}, {false}, {false}, {true}, {true}, {true}, {true}, {true},
		})
	case '▆':
		ok = m.blockGlyph(dr, [][]bool{
			{false}, {true}, {true}, {true},
		})
	case '▇':
		ok = m.blockGlyph(dr, [][]bool{
			{false}, {true}, {true}, {true}, {true}, {true}, {true}, {true},
		})
	case '▐':
		ok = m.blockGlyph(dr, [][]bool{{false, false, false, true, true, true, true, true, true, true}})
	case '▕':
		ok = m.blockGlyphOuterSquare(dr, 0, 0, 0, styleSingle)
	case '▖':
		ok = m.blockGlyph(dr, [][]bool{{false, false}, {true, false}})
	case '▗':
		ok = m.blockGlyph(dr, [][]bool{{false, false}, {false, true}})
	case '▘':
		ok = m.blockGlyph(dr, [][]bool{{true, false}, {false, false}})
	case '▙':
		ok = m.blockGlyph(dr, [][]bool{{true, false}, {true, true}})
	case '▚':
		ok = m.blockGlyph(dr, [][]bool{{true, false}, {false, true}})
	case '▛':
		ok = m.blockGlyph(dr, [][]bool{{true, true}, {true, false}})
	case '▜':
		ok = m.blockGlyph(dr, [][]bool{{true, true}, {false, true}})
	case '▝':
		ok = m.blockGlyph(dr, [][]bool{{false, true}, {false, false}})
	case '▞':
		ok = m.blockGlyph(dr, [][]bool{{false, true}, {true, false}})
	case '▟':
		ok = m.blockGlyph(dr, [][]bool{{false, true}, {true, true}})

	// Some of Unicode 13 Symbols for Legacy Computing block
	// https://en.wikipedia.org/wiki/Symbols_for_Legacy_Computing
	case '🬀':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {false, false}, {false, false},
		})
	case '🬁':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {false, false}, {false, false},
		})
	case '🬂':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {false, false}, {false, false},
		})
	case '🬃':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {true, false}, {false, false},
		})
	case '🬄':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {true, false}, {false, false},
		})
	case '🬅':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {true, false}, {false, false},
		})
	case '🬆':
		ok = m.blockGlyph(dr, [][]bool{
			{true, true}, {true, false}, {false, false},
		})
	case '🬇':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {false, true}, {false, false},
		})
	case '🬈':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {false, true}, {false, false},
		})
	case '🬉':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {false, true}, {false, false},
		})
	case '🬊':
		ok = m.blockGlyph(dr, [][]bool{
			{true, true}, {false, true}, {false, false},
		})
	case '🬋':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {true, true}, {false, false},
		})
	case '🬌':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {true, true}, {false, false},
		})
	case '🬍':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {true, true}, {false, false},
		})
	case '🬎':
		ok = m.blockGlyph(dr, [][]bool{
			{true, true}, {true, true}, {false, false},
		})
	case '🬏':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {false, false}, {true, false},
		})
	case '🬐':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {false, false}, {true, false},
		})
	case '🬑':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {false, false}, {true, false},
		})
	case '🬒':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {false, false}, {true, false},
		})
	case '🬓':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {true, false}, {true, false},
		})
	case '🬔':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {true, false}, {true, false},
		})
	case '🬕':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {true, false}, {true, false},
		})
	case '🬖':
		ok = m.blockGlyph(dr, [][]bool{
			{true, true}, {true, false}, {true, false},
		})
	case '🬗':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {false, true}, {true, false},
		})
	case '🬘':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {false, true}, {true, false},
		})
	case '🬙':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {false, true}, {true, false},
		})
	case '🬚':
		ok = m.blockGlyph(dr, [][]bool{
			{true, true}, {false, true}, {true, false},
		})
	case '🬛':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {true, true}, {true, false},
		})
	case '🬜':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {true, true}, {true, false},
		})
	case '🬝':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {true, true}, {true, false},
		})
	case '🬞':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {false, false}, {false, true},
		})
	case '🬟':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {false, false}, {false, false},
		})
	case '🬠':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {false, false}, {false, true},
		})
	case '🬡':
		ok = m.blockGlyph(dr, [][]bool{
			{true, true}, {false, false}, {false, true},
		})
	case '🬢':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {true, false}, {false, true},
		})
	case '🬣':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {true, false}, {false, true},
		})
	case '🬤':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {true, false}, {false, true},
		})
	case '🬥':
		ok = m.blockGlyph(dr, [][]bool{
			{true, true}, {true, false}, {false, true},
		})
	case '🬦':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {false, true}, {false, true},
		})
	case '🬧':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {false, true}, {false, true},
		})
	case '🬨':
		ok = m.blockGlyph(dr, [][]bool{
			{true, true}, {false, true}, {false, true},
		})
	case '🬩':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {true, true}, {false, true},
		})
	case '🬪':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {true, true}, {false, true},
		})
	case '🬫':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {true, true}, {false, true},
		})
	case '🬬':
		ok = m.blockGlyph(dr, [][]bool{
			{true, true}, {true, true}, {false, true},
		})
	case '🬭':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {false, false}, {true, true},
		})
	case '🬮':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {false, false}, {true, true},
		})
	case '🬯':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {false, false}, {true, true},
		})
	case '🬰':
		ok = m.blockGlyph(dr, [][]bool{
			{true, true}, {false, false}, {true, true},
		})
	case '🬱':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {true, false}, {true, true},
		})
	case '🬲':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {true, false}, {true, true},
		})
	case '🬳':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {true, false}, {true, true},
		})
	case '🬴':
		ok = m.blockGlyph(dr, [][]bool{
			{true, true}, {true, false}, {true, true},
		})
	case '🬵':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {false, true}, {true, true},
		})
	case '🬶':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {false, true}, {true, true},
		})
	case '🬷':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {false, true}, {true, true},
		})
	case '🬸':
		ok = m.blockGlyph(dr, [][]bool{
			{true, true}, {false, true}, {true, true},
		})
	case '🬹':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false}, {true, true}, {true, true},
		})
	case '🬺':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false}, {true, true}, {true, true},
		})
	case '🬻':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true}, {true, true}, {true, true},
		})
	case '🭼':
		ok = m.blockGlyphOuterSquare(dr, 0, styleSingle, styleSingle, 0)
	case '🭽':
		ok = m.blockGlyphOuterSquare(dr, styleSingle, 0, styleSingle, 0)
	case '🭾':
		ok = m.blockGlyphOuterSquare(dr, styleSingle, 0, 0, styleSingle)
	case '🭿':
		ok = m.blockGlyphOuterSquare(dr, 0, styleSingle, 0, styleSingle)
	case '🮀':
		ok = m.blockGlyphOuterSquare(dr, styleSingle, styleSingle, 0, 0)

	/* private unicode series for custom UI elements */

	// progress bar start
	case '􀀀':
		ok = m.blockGlyph(dr, [][]bool{
			{true, false, false, false, false, false, false, false},
			{true, false, false, false, false, false, false, false},
			{true, false, false, false, false, false, false, false},
			{true, false, false, false, false, false, false, false},
			{true, false, false, false, false, false, false, false},
			{true, false, false, false, false, false, false, false},
			{true, true, true, true, true, true, true, true},
		})

	// progress bar current
	case '􀀁':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{true, true, true, true, true, true, true, true},
		})

	// progress bar current tip
	case '􀀂':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{true, true, true, true, true, true, true, true},
		})

	// progress bar remain
	case '􀀃':
		ok = m.blockGlyphFill(dr, [][]bool{
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{false, false, false, false, false, false, false, false},
			{true, true, true, true, true, true, true, true},
		}, colorFillAlphaStep3)

	// progress bar end
	case '􀀄':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false, false, false, false, false, false, false, false, false, false, false, false, false, false, true},
			{false, false, false, false, false, false, false, false, false, false, false, false, false, false, false, true},
			{false, false, false, false, false, false, false, false, false, false, false, false, false, false, false, true},
			{false, false, false, false, false, false, false, false, false, false, false, false, false, false, false, true},
			{false, false, false, false, false, false, false, false, false, false, false, false, false, false, false, true},
			{false, false, false, false, false, false, false, false, false, false, false, false, false, false, false, true},
			{false, false, false, false, false, false, false, false, false, false, false, false, false, false, false, true},
		})
		if ok {
			ok = m.blockGlyphFill(dr, [][]bool{
				{false, false, false, false, false, false, false, false},
				{false, false, false, false, false, false, false, false},
				{false, false, false, false, false, false, false, false},
				{false, false, false, false, false, false, false, false},
				{false, false, false, false, false, false, false, false},
				{false, false, false, false, false, false, false, false},
				{true, true, true, true, true, true, true, true},
			}, colorFillAlphaStep3)
		}

	// character at the end of so transition is smoother ▇▆▅▄▃▂
	case '􀀅':
		ok = m.blockGlyph(dr, [][]bool{
			{false}, {false}, {false}, {false}, {false}, {false}, {false}, {true},
		})

	// tab top highlight
	case '􀀆':
		ok = m.blockGlyph(dr, [][]bool{
			{true},
			{false},
			{false},
			{false},
			{false},
			{false},
			{false},
			{false},
		})

	// tab bottom highlight
	case '􀀇':
		ok = m.blockGlyph(dr, [][]bool{
			{false},
			{false},
			{false},
			{false},
			{false},
			{false},
			{false},
			{true},
		})

	//   􀀈􀀌􀀉
	//   􀀎 􀀏
	//   􀀊􀀍􀀋
	case '􀀈':
		ok = m.blockGlyph(dr, [][]bool{
			{false},
			{false, true, true, true, true, true, true, true},
			{false, true, true, true, true, true, true, true},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
		})
	case '􀀉':
		ok = m.blockGlyph(dr, [][]bool{
			{false},
			{true, true, true, true, true, true, true, false},
			{true, true, true, true, true, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
		})
	case '􀀊':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, true, true, true, true, true},
			{false, true, true, true, true, true, true, true},
			{false},
		})
	case '􀀋':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{true, true, true, true, true, true, true, false},
			{true, true, true, true, true, true, true, false},
			{false},
		})

	// frame channel: top, bottom, left, right
	case '􀀌':
		ok = m.blockGlyph(dr, [][]bool{
			{false}, {true}, {true}, {false}, {false}, {false}, {false}, {false},
			{false}, {false}, {false}, {false}, {false}, {false}, {false}, {false},
		})
	case '􀀍':
		ok = m.blockGlyph(dr, [][]bool{
			{false}, {false}, {false}, {false}, {false}, {false}, {false}, {false},
			{false}, {false}, {false}, {false}, {false}, {true}, {true}, {false},
		})
	case '􀀎':
		ok = m.blockGlyph(dr, [][]bool{{false, true, true, false, false, false, false, false}})
	case '􀀏':
		ok = m.blockGlyph(dr, [][]bool{{false, false, false, false, false, true, true, false}})

	// frame channel: left and right, branching inward into a bottom channel
	case '􀀐':
		ok = m.blockGlyph(dr, [][]bool{
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, false, false, false, false, false},
			{false, true, true, true, true, true, true, true},
			{false, true, true, true, true, true, true, true},
			{false, true, true, false, false, false, false, false},
		})
	case '􀀑':
		ok = m.blockGlyph(dr, [][]bool{
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{false, false, false, false, false, true, true, false},
			{true, true, true, true, true, true, true, false},
			{true, true, true, true, true, true, true, false},
			{false, false, false, false, false, true, true, false},
		})
	}

	if ok {
		mask = m.mask
		maskp = dr.Min
		advance = fixed.I(dr.Max.X - dr.Min.X)
	}
	return
}

func (m *custom) GlyphBounds(r rune) (
	bounds fixed.Rectangle26_6, advance fixed.Int26_6, ok bool,
) {
	var dot fixed.Point26_6
	boundsRect, _, _, advance, ok := m.Glyph(dot, r)
	if !ok {
		return
	}

	bounds = fixedRectangleFromImageRectangle(boundsRect)
	return
}
func (m *custom) GlyphAdvance(r rune) (
	advance fixed.Int26_6, ok bool,
) {
	var dot fixed.Point26_6
	_, _, _, advance, ok = m.Glyph(dot, r)
	return
}

func (m *custom) Close() (ret error) {
	// it's assumed that m.face is closed elsewhere
	return
}

func (m *custom) Kern(r0, r1 rune) fixed.Int26_6 {
	return m.face.Kern(r0, r1)
}

func (m *custom) Metrics() font.Metrics {
	return m.face.Metrics()
}

func fixedRectangleFromImageRectangle(r image.Rectangle) fixed.Rectangle26_6 {
	return fixed.Rectangle26_6{
		Min: fixed.Point26_6{
			X: fixed.I(r.Min.X),
			Y: fixed.I(r.Min.Y),
		},
		Max: fixed.Point26_6{
			X: fixed.I(r.Max.X),
			Y: fixed.I(r.Max.Y),
		},
	}
}

func float64ToFixed(x float64) fixed.Int26_6 {
	return fixed.Int26_6(x * (1 << 6))
}

func fixedToFloat64(x fixed.Int26_6) float64 {
	return float64(x>>6) + float64(x&((1<<6)-1))/float64(1<<6)
}
