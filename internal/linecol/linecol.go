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

// Package linecol reads the line and column positions that compilers,
// stack traces and link fragments spell out.
package linecol

import (
	"strconv"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// Parse reads the position at the start of s and returns the 1-based
// line and column it names, together with the number of bytes it
// spans. A position names a line, or a line and a column, and a
// position without a column starts at column one. ok is false when s
// does not start with a position.
func Parse(s string) (line, col, n int, ok bool) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	if i == 0 {
		return 0, 0, 0, false
	}
	line, err := strconv.Atoi(s[:i])
	if err != nil || line <= 0 {
		return 0, 0, 0, false
	}
	col = 1
	// A colon only continues the position when a column follows it, so
	// that the colon of a compiler message stays out of the position.
	if i+1 < len(s) && s[i] == ':' && isDigit(s[i+1]) {
		j := i + 1
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		c, err := strconv.Atoi(s[i+1 : j])
		if err != nil || c <= 0 {
			return 0, 0, 0, false
		}
		col = c
		i = j
	}
	return line, col, i, true
}

// Coord converts a 1-based position to the 0-based coordinates an
// editor cursor uses. A line of zero leaves the cursor alone, and a
// column of zero starts at the first column.
func Coord(line, col int) term.Coordinates {
	y := line - 1
	if y < 0 {
		y = 0
	}
	x := col - 1
	if x < 0 {
		x = 0
	}
	return term.Coordinates{Y: y, X: x}
}

// isDigit reports whether b is an ASCII digit.
func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
