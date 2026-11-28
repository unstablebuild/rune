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
	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/component/gshader/gshadertest"
)

func TestCrossFade(t *testing.T) {
	first, second := timed("first"), timed("second")
	src := gshader.Source()
	blend := func(weight float32, a, b gshader.Node) gshader.Node {
		return gshader.Run(gshader.BlendProgram, gshader.Uniforms{"Weight": weight}, a, b)
	}
	// Over 100 frames, the default fade runs from 37.5 to 62.5, and each
	// shader plays over 63 frames.
	tests := []struct {
		name   string
		params gshader.CrossFadeParams
		frame  int
		want   gshader.Node
	}{
		{name: "first alone", params: gshader.DefaultCrossFadeParams(), frame: 0, want: first.Shade(0, 63, src)},
		{name: "before the fade", params: gshader.DefaultCrossFadeParams(), frame: 37, want: first.Shade(37, 63, src)},
		{
			name: "fade starts", params: gshader.DefaultCrossFadeParams(), frame: 38,
			want: blend(0.02, first.Shade(38, 63, src), second.Shade(1, 63, src)),
		},
		{
			name: "half way", params: gshader.DefaultCrossFadeParams(), frame: 50,
			want: blend(0.5, first.Shade(50, 63, src), second.Shade(13, 63, src)),
		},
		{
			name: "fade ends", params: gshader.DefaultCrossFadeParams(), frame: 62,
			want: blend(0.98, first.Shade(62, 63, src), second.Shade(25, 63, src)),
		},
		{name: "after the fade", params: gshader.DefaultCrossFadeParams(), frame: 63, want: second.Shade(26, 63, src)},
		{name: "second alone", params: gshader.DefaultCrossFadeParams(), frame: 99, want: second.Shade(62, 63, src)},
		{
			name: "without a fade it cuts", params: gshader.CrossFadeParams{ChangeAtPerc: 0.5}, frame: 50,
			want: second.Shade(0, 50, src),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gshadertest.AssertEqual(t, tt.want, gshader.CrossFade(tt.params, first, second).Shade(tt.frame, 100, src))
		})
	}
}

func TestCrossFadePanicsOutsideTheRun(t *testing.T) {
	tests := []gshader.CrossFadeParams{
		{ChangeAtPerc: -0.1}, {ChangeAtPerc: 1.1}, {OverlapPerc: -0.1}, {OverlapPerc: 1.1},
	}
	for _, params := range tests {
		assert.Panics(t, func() { gshader.CrossFade(params, gshader.Nop(), gshader.Nop()) }, "%+v", params)
	}
}
