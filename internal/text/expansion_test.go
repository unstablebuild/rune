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

package text

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

func TestWordAtCursor(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString("foo bar.baz\nhello_world  \n")

	cases := []struct {
		name string
		pos  term.Coordinates
		want string
	}{
		{"middle of identifier", term.Coordinates{Y: 0, X: 1}, "foo"},
		{"start of identifier", term.Coordinates{Y: 0, X: 4}, "bar"},
		{"after dot", term.Coordinates{Y: 0, X: 8}, "baz"},
		{"on separator", term.Coordinates{Y: 0, X: 3}, ""},
		{"snake_case treated as one word",
			term.Coordinates{Y: 1, X: 3}, "hello_world"},
		{"row out of range", term.Coordinates{Y: 99}, ""},
		{"column out of range", term.Coordinates{Y: 0, X: 99}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, wordAtCursor(buf, tc.pos))
		})
	}
}

func TestWordAtCursorNilView(t *testing.T) {
	assert.Equal(t, "", wordAtCursor(nil, term.Coordinates{}))
}
