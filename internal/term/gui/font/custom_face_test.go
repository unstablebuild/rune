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
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/image/math/fixed"
)

var (
	handled = []rune{
		'░', '▒', '▓', '█',
		'─', '━', '╴', '╶', '╸', '╺',
		'│', '┃', '╵', '╷', '╹', '╻',
		'┌', '┍', '┎', '┏', '┐', '┑', '┒', '┓',
		'└', '┕', '┖', '┗', '┘', '┙', '┚', '┛',
		'├', '┝', '┞', '┟', '┠', '┡', '┢', '┣',
		'┤', '┥', '┦', '┧', '┨', '┩', '┪', '┫', '┬', '┭',
		'┮', '┯', '┰', '┱', '┲', '┳', '┴', '┵', '┶', '┷',
		'┸', '┹', '┺', '┻', '┼', '┽', '┾', '┿',
		'╀', '╁', '╂', '╃', '╄', '╅', '╆', '╇', '╈', '╉', '╊', '╋',
		'╼', '╽', '╾', '╿',
		'╯', '╰', '╮', '╭', '┄', '┅', '┈', '┉', '╌', '╍', '┆', '┇', '┊',
		'┋', '╎', '╏', '▉', '▊', '▋', '▌', '▍', '▎', '▏', '▔', '▀', '▁',
		'▂', '▃', '▄', '▅', '▆', '▇', '▐', '▕', '▖', '▗', '▘', '▙', '▚',
		'▛', '▜', '▝', '▞', '▟',
		'\U0001fb00', '\U0001fb01', '\U0001fb02', '\U0001fb03', '\U0001fb04',
		'\U0001fb05', '\U0001fb06', '\U0001fb07', '\U0001fb08', '\U0001fb09',
		'\U0001fb0A', '\U0001fb0B', '\U0001fb0C', '\U0001fb0D', '\U0001fb0E',
		'\U0001fb0F', '\U0001fb10', '\U0001fb11', '\U0001fb12', '\U0001fb13',
		'\U0001fb14', '\U0001fb15', '\U0001fb16', '\U0001fb17', '\U0001fb18',
		'\U0001fb19', '\U0001fb1A', '\U0001fb1B', '\U0001fb1C', '\U0001fb1D',
		'\U0001fb1E', '\U0001fb1F', '\U0001fb20', '\U0001fb21', '\U0001fb22',
		'\U0001fb23', '\U0001fb24', '\U0001fb25', '\U0001fb26', '\U0001fb27',
		'\U0001fb28', '\U0001fb29', '\U0001fb2A', '\U0001fb2B', '\U0001fb2C',
		'\U0001fb2D', '\U0001fb2E', '\U0001fb2F', '\U0001fb30', '\U0001fb31',
		'\U0001fb32', '\U0001fb33', '\U0001fb34', '\U0001fb35', '\U0001fb36',
		'\U0001fb37', '\U0001fb38', '\U0001fb39', '\U0001fb3A', '\U0001fb3B',
		'\U0001fb7C', '\U0001fb7D', '\U0001fb7E', '\U0001fb7F', '\U0001fb80',
		'\U00100008', '\U00100009', '\U0010000A', '\U0010000B',
		'\U0010000C', '\U0010000D', '\U0010000E', '\U0010000F',
		'\U00100010', '\U00100011',
	}
	unhandled       = []rune{'a', 'b', 'A'}
	allRunes        = append(append([]rune{}, handled...), unhandled...)
	standardWidths  = []float64{0, 10}
	standardHeights = []float64{0, 10}
	standardDots    = []fixed.Point26_6{
		{X: fixed.I(-1)},
		{X: fixed.I(0)},
		{X: fixed.I(1)},
		{Y: fixed.I(-1)},
		{Y: fixed.I(0)},
		{Y: fixed.I(1)},
		{X: fixed.I(1), Y: fixed.I(-1)},
		{X: fixed.I(-1), Y: fixed.I(0)},
		{X: fixed.I(-1), Y: fixed.I(1)},
	}
	standardOffsets = []float64{-25, 0, 25}
)

func TestCustomFace(t *testing.T) {
	t.Run("none of the special characters cause a panic", func(t *testing.T) {
		for _, width := range standardWidths {
			for _, height := range standardWidths {
				for _, offsetY := range standardOffsets {
					f := newCustomFace(width, height, offsetY, &mockFace{}, false, 0, 0)
					for _, r := range handled {
						assert.NotPanics(t, func() {
							f.GlyphBounds(r)
						})
						assert.NotPanics(t, func() {
							f.GlyphAdvance(r)
						})
						assert.NotPanics(t, func() {
							f.Kern(r, 'a')
						})
						assert.NotPanics(t, func() {
							f.Metrics()
						})
						for _, dot := range standardDots {
							assert.NotPanics(t, func() {
								f.Glyph(dot, r)
							})
						}
					}
				}
			}
		}
	})
}
