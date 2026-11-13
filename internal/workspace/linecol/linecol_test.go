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
		{raw: "42:0", line: 42, col: 0, n: 4, ok: true,
			comment: "a column of zero is a line with no column"},
		{raw: "76:7: undefined: foo", line: 76, col: 7, n: 4, ok: true,
			comment: "the shape a compiler prints"},
		{raw: "42:5:3", line: 42, col: 5, n: 4, ok: true,
			comment: "a third number is not read"},
		{raw: "99999999999999999999", ok: false,
			comment: "a number that does not fit is not a position"},
		{raw: "42abc", line: 42, col: 1, n: 2, ok: true,
			comment: "a number running into letters still names a line"},
		{raw: "007", line: 7, col: 1, n: 3, ok: true,
			comment: "leading zeros are read as a number"},
		{raw: "\u0661\u0662", ok: false,
			comment: "only ASCII digits count"},
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
	suite := []struct {
		description string
		line, col   int
		want        term.Coordinates
	}{
		{description: "a line and a column", line: 42, col: 5,
			want: term.Coordinates{Y: 41, X: 4}},
		{description: "a column of one is the first column", line: 42, col: 1,
			want: term.Coordinates{Y: 41, X: 0}},
		{description: "a missing column becomes the first column", line: 42, col: 0,
			want: term.Coordinates{Y: 41, X: 0}},
		{description: "a missing line becomes the first row", line: 0, col: 0,
			want: term.Coordinates{}},
		{description: "a missing line with a column", line: 0, col: 5,
			want: term.Coordinates{Y: 0, X: 4}},
	}

	for _, tc := range suite {
		t.Run(tc.description, func(t *testing.T) {
			assert.Equal(t, tc.want, Coord(tc.line, tc.col))
		})
	}
}
