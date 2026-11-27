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

func TestCut(t *testing.T) {
	first, second := timed("first"), timed("second")
	src := gshader.Source()
	tests := []struct {
		name         string
		params       gshader.CutParams
		frame, total int
		want         gshader.Node
	}{
		{name: "first", params: gshader.DefaultCutParams(), frame: 0, total: 10, want: first.Shade(0, 5, src)},
		{name: "last of the first", params: gshader.DefaultCutParams(), frame: 4, total: 10, want: first.Shade(4, 5, src)},
		{name: "second", params: gshader.DefaultCutParams(), frame: 5, total: 10, want: second.Shade(0, 5, src)},
		{name: "rounds the change", params: gshader.CutParams{ChangeAtPerc: 0.25}, frame: 3, total: 10, want: second.Shade(0, 7, src)},
		{name: "all second", params: gshader.CutParams{}, frame: 0, total: 10, want: second.Shade(0, 10, src)},
		{name: "all first", params: gshader.CutParams{ChangeAtPerc: 1}, frame: 9, total: 10, want: first.Shade(9, 10, src)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gshadertest.AssertEqual(t, tt.want, gshader.Cut(tt.params, first, second).Shade(tt.frame, tt.total, src))
		})
	}
}

func TestCutPanicsOutsideTheRun(t *testing.T) {
	for _, at := range []float64{-0.1, 1.1} {
		assert.Panics(t, func() { gshader.Cut(gshader.CutParams{ChangeAtPerc: at}, gshader.Nop(), gshader.Nop()) })
	}
}
