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

	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/component/gshader/gshadertest"
)

func TestPeriod(t *testing.T) {
	inner := timed("inner")
	src := gshader.Source()
	sh := gshader.Period(inner, 4)
	tests := []struct {
		name         string
		frame, total int
		wantFrame    int
	}{
		{name: "first run", frame: 3, total: 100, wantFrame: 3},
		{name: "starts over", frame: 4, total: 100, wantFrame: 0},
		{name: "whatever the total", frame: 9, total: 3, wantFrame: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gshadertest.AssertEqual(t, inner.Shade(tt.wantFrame, 4, src), sh.Shade(tt.frame, tt.total, src))
		})
	}
}
