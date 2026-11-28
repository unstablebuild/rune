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
	"unstable.build/rune/internal/component/shader/timeshader"
)

func TestRemap(t *testing.T) {
	inner := timed("inner")
	src := gshader.Source()
	tests := []struct {
		name         string
		fn           func(frame, total int) int
		frame, total int
		wantFrame    int
	}{
		{name: "identity", fn: func(f, _ int) int { return f }, frame: 3, total: 10, wantFrame: 3},
		{name: "reverse", fn: timeshader.ReverseTime(), frame: 3, total: 10, wantFrame: 6},
		{name: "ping pong forth", fn: timeshader.PingPongTime(), frame: 2, total: 10, wantFrame: 4},
		{name: "ping pong back", fn: timeshader.PingPongTime(), frame: 7, total: 10, wantFrame: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gshadertest.AssertEqual(t, inner.Shade(tt.wantFrame, tt.total, src),
				gshader.Remap(inner, tt.fn).Shade(tt.frame, tt.total, src))
		})
	}
}
