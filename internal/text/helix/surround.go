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

package helix

import (
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

// surroundPair returns the opening and closing delimiters Helix uses for
// ch. Unmatched characters surround with themselves, which is how
// helix-core/src/surround.rs treats quotes and other symmetric pairs.
func surroundPair(ch rune) (open, close rune) {
	switch ch {
	case '(', ')':
		return '(', ')'
	case '{', '}':
		return '{', '}'
	case '[', ']':
		return '[', ']'
	case '<', '>':
		return '<', '>'
	}
	return ch, ch
}

// bracketPairs is match_brackets::BRACKETS restricted to the delimiters
// the cursor primitives can match.
var bracketPairs = [...][2]rune{{'(', ')'}, {'{', '}'}, {'[', ']'}, {'<', '>'}}

// bracketAt classifies ch as a pair delimiter, reporting the index into
// bracketPairs and whether it is the opening side. idx is -1 when ch is
// not a bracket at all.
func bracketAt(ch rune) (idx int, open bool) {
	for i, pair := range bracketPairs {
		switch ch {
		case pair[0]:
			return i, true
		case pair[1]:
			return i, false
		}
	}
	return -1, false
}

// closestPair implements the m textobject. find_nth_closest_pairs_plain
// walks forward from pos looking for a close bracket whose opener lies
// behind it, so nested pairs that open and shut on the way are stepped
// over.
func closestPair(buf *cell.Buffer, pos term.Coordinates) (open, closing rune, ok bool) {
	var stack []int
	for {
		ch, inBounds := charAt(buf, pos)
		if !inBounds {
			return 0, 0, false
		}
		idx, isOpen := bracketAt(ch)
		switch {
		case idx < 0:
		case isOpen:
			stack = append(stack, idx)
		case len(stack) > 0 && stack[len(stack)-1] == idx:
			stack = stack[:len(stack)-1]
		default:
			return bracketPairs[idx][0], bracketPairs[idx][1], true
		}
		next, more := nextPos(buf, pos)
		if !more {
			return 0, 0, false
		}
		pos = next
	}
}
