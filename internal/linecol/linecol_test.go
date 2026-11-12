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

package linecol

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestParse(t *testing.T) {
	suite := []struct {
		raw     string
		line    int
		col     int
		n       int
		ok      bool
		comment string
	}{
		{raw: "42", line: 42, col: 1, n: 2, ok: true},
		{raw: "42:5", line: 42, col: 5, n: 4, ok: true},
		{raw: "42:", line: 42, col: 1, n: 2, ok: true,
			comment: "a colon with no column is not part of the position"},
		{raw: "42: ", line: 42, col: 1, n: 2, ok: true},
		{raw: "0", ok: false},
		{raw: "", ok: false},
		{raw: "abc", ok: false},
		{raw: ":5", ok: false},
	}
	for _, tc := range suite {
		t.Run(tc.raw, func(t *testing.T) {
			line, col, n, ok := Parse(tc.raw)
			assert.Equal(t, tc.ok, ok)
			if !tc.ok {
				return
			}
			assert.Equal(t, tc.line, line)
			assert.Equal(t, tc.col, col)
			assert.Equal(t, tc.n, n)
		})
	}
}

func TestCoord(t *testing.T) {
	assert.Equal(t, term.Coordinates{Y: 41, X: 4}, Coord(42, 5))
	assert.Equal(t, term.Coordinates{Y: 41, X: 0}, Coord(42, 1))
	assert.Equal(t, term.Coordinates{Y: 41, X: 0}, Coord(42, 0))
	assert.Equal(t, term.Coordinates{}, Coord(0, 0))
}
