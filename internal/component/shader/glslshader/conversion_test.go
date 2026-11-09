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
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestConversion(t *testing.T) {
	t.Run("convert color to vector", func(t *testing.T) {
		col := term.NewRGBColor(255, 245, 235)
		vec := colToVec(col, 0)
		assert.Equal(t, vec, vec3(255, 245, 235))
	})

	t.Run("convert color to vector out of range wraps", func(t *testing.T) {
		col := term.NewRGBColor(300, 301, 302)
		vec := colToVec(col, 0)
		assert.Equal(t, vec, vec3(300%256, 301%256, 302%256))
	})

	t.Run("convert default color to vector", func(t *testing.T) {
		col := term.ColorDefault
		vec := colToVec(col, term.NewRGBColor(255, 0, 0))
		assert.Equal(t, vec, vec3(255, 0, 0))
	})

	t.Run("convert default color to vector no default attribute", func(t *testing.T) {
		col := term.ColorDefault

		// term.Attribute{} makes field Bg be the zero-value (0) which is
		// itself ColorDefault, so it will leave the color unresolved.
		vec := colToVec(col, 0)

		assert.Equal(t, vec, vec3(-1, -1, -1))
	})
}

func TestColorHue(t *testing.T) {
	for _, tc := range []struct {
		name string
		col  term.Color
		want float
	}{
		{"red", term.NewRGBColor(255, 0, 0), 0},
		{"yellow", term.NewRGBColor(255, 255, 0), 1.0 / 6},
		{"green", term.NewRGBColor(0, 255, 0), 1.0 / 3},
		{"blue", term.NewRGBColor(0, 0, 255), 2.0 / 3},
		{"magenta", term.NewRGBColor(255, 0, 255), 5.0 / 6},
		{"rose wraps below red", term.NewRGBColor(255, 0, 128), 0.9163},
		{"dark green has green's hue", term.NewRGBColor(0, 100, 0), 1.0 / 3},
		{"grey has none", term.NewRGBColor(128, 128, 128), 0},
		{"default has none", term.ColorDefault, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.want, colorHue(tc.col), 1e-4)
		})
	}
}
