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

package gshader_test

import (
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/gshader"
)

func TestUniformsEqual(t *testing.T) {
	red := term.NewRGBColor(255, 0, 0)
	tests := []struct {
		name string
		a, b gshader.Uniforms
		want bool
	}{
		{name: "nil and empty", a: nil, b: gshader.Uniforms{}, want: true},
		{name: "same scalars", a: gshader.Uniforms{"A": float32(1), "B": 2}, b: gshader.Uniforms{"A": float32(1), "B": 2}, want: true},
		{name: "different values", a: gshader.Uniforms{"A": float32(1)}, b: gshader.Uniforms{"A": float32(2)}},
		{name: "different types", a: gshader.Uniforms{"A": float32(1)}, b: gshader.Uniforms{"A": float64(1)}},
		{name: "different keys", a: gshader.Uniforms{"A": 1}, b: gshader.Uniforms{"B": 1}},
		{name: "slices by value", a: gshader.Uniforms{"A": []float32{1, 2}}, b: gshader.Uniforms{"A": []float32{1, 2}}, want: true},
		{name: "different slices", a: gshader.Uniforms{"A": []float32{1, 2}}, b: gshader.Uniforms{"A": []float32{1}}},
		{name: "arrays", a: gshader.Uniforms{"A": [2]float32{1, 2}}, b: gshader.Uniforms{"A": [2]float32{1, 2}}, want: true},
		{name: "colours", a: gshader.Uniforms{"A": red}, b: gshader.Uniforms{"A": red}, want: true},
		{name: "different colours", a: gshader.Uniforms{"A": red}, b: gshader.Uniforms{"A": term.ColorBlue}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.a.Equal(tt.b))
			assert.Equal(t, tt.want, tt.b.Equal(tt.a))
		})
	}
}

func TestUniformsValues(t *testing.T) {
	p := gshader.MustProgram("t.kage", []byte(kage(
		"var Resolution vec2\nvar CellSize vec2\nvar Background vec4\nvar Foreground vec4\n"+
			"var Progress float\nvar Tint vec4\nvar Count int\nvar Stops [2]vec2\nvar Mat mat2")))
	defaults := gshader.Defaults{
		Resolution: [2]float32{200, 100}, CellSize: [2]float32{10, 20},
		Background: color.RGBA{A: 0xff}, Foreground: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff},
	}
	resolve := func(c term.Color) color.RGBA {
		if c == term.ColorRed {
			return color.RGBA{R: 0xff, A: 0xff}
		}
		return color.RGBA{}
	}
	t.Run("converts values and fills built-ins", func(t *testing.T) {
		got, err := gshader.Uniforms{
			"Progress": float64(0.5), "Tint": term.ColorRed, "Count": 3,
			"Stops": []float32{1, 2, 3, 4}, "Mat": [4]float32{1, 0, 0, 1},
			"Undeclared": "ignored", "CellSize": [2]float32{1, 2},
		}.Values(p, defaults, resolve)
		require.NoError(t, err)
		assert.Equal(t, map[string]any{
			"Resolution": []float32{200, 100},
			"CellSize":   []float32{1, 2},
			"Background": []float32{0, 0, 0, 1},
			"Foreground": []float32{1, 1, 1, 1},
			"Progress":   []float32{0.5},
			"Tint":       []float32{1, 0, 0, 1},
			"Count":      3,
			"Stops":      []float32{1, 2, 3, 4},
			"Mat":        []float32{1, 0, 0, 1},
		}, got)
	})
	for _, tt := range []struct {
		name    string
		u       gshader.Uniforms
		wantErr string
	}{
		{name: "too few values", u: gshader.Uniforms{"Tint": [3]float32{}}, wantErr: "t.kage: uniform Tint: a vec4 takes 4 values, got 3"},
		{name: "too many values", u: gshader.Uniforms{"Progress": [2]float32{}}, wantErr: "a float takes 1 values, got 2"},
		{name: "colour for a vec2", u: gshader.Uniforms{"Resolution": term.ColorRed}, wantErr: "a vec2 cannot be set to a colour"},
		{name: "float for an int", u: gshader.Uniforms{"Count": float32(1)}, wantErr: "a int cannot be set to a float32"},
		{name: "unsupported value", u: gshader.Uniforms{"Tint": "red"}, wantErr: "a vec4 cannot be set to a string"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.u.Values(p, defaults, resolve)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
	t.Run("unsupported declared type", func(t *testing.T) {
		p := gshader.MustProgram("t.kage", []byte(kage("var Flags ivec2")))
		_, err := gshader.Uniforms{"Flags": [2]float32{}}.Values(p, defaults, resolve)
		require.ErrorContains(t, err, "unsupported type ivec2")
	})
}
