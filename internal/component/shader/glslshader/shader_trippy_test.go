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
	"testing"

	"github.com/stretchr/testify/assert"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestTrippy(t *testing.T) {
	shadertest.TestShader(t, Noise(DefaultNoiseParams(), 30))

	sh := Trippy(DefaultTrippyParams(), 30)

	t.Run("rand produces noise between 0 and 1", func(t *testing.T) {
		res := sh.(*trippy).rand(vec2(123.0, 456.0))
		assert.True(t, res > -1.0 && res < 1.0)
	})

	t.Run("noise produces noise between 0 and 1", func(t *testing.T) {
		res := sh.(*trippy).noise(vec2(789.0, 123.0))
		assert.True(t, res > -1.0 && res < 1.0)
	})
}

func TestTrippyPaintForeground(t *testing.T) {
	params := DefaultTrippyParams()
	params.PaintForeground = true

	t.Run("suite", func(t *testing.T) {
		shadertest.TestShader(t, Trippy(params, 30))
	})

	t.Run("paints the characters and keeps them", func(t *testing.T) {
		in := paintTestCells()
		Trippy(params, 30).Shade(3, 20, in)
		want := paintTestCells()
		var fgChanged bool
		for y, row := range in {
			for x, cell := range row {
				assert.Equal(t, want[y][x].Ch, cell.Ch, "text must survive")
				assert.Equal(t, want[y][x].Bg, cell.Bg, "background must stay bare")
				fgChanged = fgChanged || cell.Fg != want[y][x].Fg
			}
		}
		assert.True(t, fgChanged, "foreground painted")
	})

	t.Run("leaves background glyphs bare", func(t *testing.T) {
		assertPaintsOnlyText(t, Trippy(params, 30))
	})
}
