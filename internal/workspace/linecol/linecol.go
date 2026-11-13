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
//
// Positions are 1-based, the way compilers report them. Zero means the
// text did not give one: a line of zero is no line, and a column of zero
// is no column. Coord turns either into the zero-based coordinates an
// editor cursor uses.
//
// This package is the only place that parses a position. Callers that
// hold a position in a byte slice, such as a terminal row, use Cut; the
// rest use Parse.
package linecol

import (
	"strconv"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// Position is a 1-based line and column. Zero means the text did not
// give one.
type Position struct {
	Line, Col int
}

// Parse reads the position at the start of s and returns the 1-based
// line and column it names, together with the number of bytes it
// spans. A position names a line, or a line and a column. A column of
// zero is no column rather than a bad one, so that a tool printing
// "42:0" still names line 42, and a missing column stays visible as
// zero. ok is false when s does not start with a position.
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
	// A colon only continues the position when a column follows it, so
	// that the colon of a compiler message stays out of the position.
	if i+1 < len(s) && s[i] == ':' && isDigit(s[i+1]) {
		j := i + 1
		for j < len(s) && isDigit(s[j]) {
			j++
		}
		c, err := strconv.Atoi(s[i+1 : j])
		if err != nil {
			return 0, 0, 0, false
		}
		// A column of zero is no column rather than a bad one, so it
		// stays zero and Coord turns it into the first column.
		col = c
		i = j
	}
	return line, col, i, true
}

// Cut splits the path off a reference of the form PATH:LINE[:COL]. It
// cuts at the first colon that starts a position followed by the end of
// s or by another colon, so a drive letter and a colon inside a name
// stay in the path. rest is what follows the position, without the
// colon that introduced it, and is empty when the position ended s.
// ok is false when s carries no such position, and path is then all of
// s.
//
// A file path is only a reference when rest is empty, which keeps an
// ambiguous suffix such as "y.go:7:2:4" inside the path.
func Cut(s string) (path string, pos Position, rest string, ok bool) {
	for i := 0; i < len(s); i++ {
		if s[i] != ':' {
			continue
		}
		// A position whose colon directly follows another names an empty
		// line, which is not a position: "/x/y.go::3" stays whole.
		if i > 0 && s[i-1] == ':' {
			continue
		}
		line, col, n, ok := Parse(s[i+1:])
		if !ok {
			continue
		}
		end := i + 1 + n
		// A position is only a position when the text ends it or
		// another colon follows: "42abc" and "1.5" are not, and
		// neither is the drive of "C:\x".
		if end < len(s) && s[end] != ':' {
			continue
		}
		if end < len(s) {
			rest = s[end+1:]
		}
		if i == 0 {
			// A position with nothing in front of it names no file.
			return s, Position{}, "", false
		}
		return s[:i], Position{Line: line, Col: col}, rest, true
	}
	return s, Position{}, "", false
}

// Coord converts a 1-based position to the 0-based coordinates an
// editor cursor uses. A line of zero becomes row zero and a column of
// zero becomes column zero; whether to move the cursor at all is the
// caller's decision, not this function's.
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
