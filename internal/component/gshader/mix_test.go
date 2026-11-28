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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/component/gshader/gshadertest"
)

func TestMix(t *testing.T) {
	a, b := timed("a"), timed("b")
	src := gshader.Solid(term.ColorRed)
	tests := []struct {
		name    string
		shaders []gshader.Shader
		want    gshader.Node
	}{
		{name: "one", shaders: []gshader.Shader{a}, want: a.Shade(3, 10, src)},
		{name: "each over the one before", shaders: []gshader.Shader{a, b}, want: b.Shade(3, 10, a.Shade(3, 10, src))},
		{name: "in order", shaders: []gshader.Shader{b, a}, want: a.Shade(3, 10, b.Shade(3, 10, src))},
		{name: "nops", shaders: []gshader.Shader{gshader.Nop(), gshader.Nop()}, want: src},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gshadertest.AssertEqual(t, tt.want, gshader.Mix(tt.shaders...).Shade(3, 10, src))
		})
	}
}

func TestMixPanicsWithoutShaders(t *testing.T) {
	assert.PanicsWithValue(t, "gshader: Mix needs shaders", func() { gshader.Mix() })
}
