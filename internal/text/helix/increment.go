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
	"strings"
	"unicode"
)

// numberAround finds the digit run overlapping [from, to), falling back
// to the first run starting at or after from.
func numberAround(line []rune, from, to int) (start, end int, ok bool) {
	if len(line) == 0 {
		return 0, 0, false
	}
	from = max(0, min(from, len(line)-1))
	to = max(from+1, min(to, len(line)))

	idx := -1
	for i := from; i < to; i++ {
		if unicode.IsDigit(line[i]) {
			idx = i
			break
		}
	}
	if idx < 0 {
		for i := from; i < len(line); i++ {
			if unicode.IsDigit(line[i]) {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return 0, 0, false
	}
	start = idx
	for start > 0 && unicode.IsDigit(line[start-1]) {
		start--
	}
	if start > 0 && line[start-1] == '-' {
		start--
	}
	end = idx
	for end < len(line) && unicode.IsDigit(line[end]) {
		end++
	}
	return start, end, true
}

func zeroPadded(s string) int {
	digits := strings.TrimPrefix(s, "-")
	if len(digits) > 1 && digits[0] == '0' {
		return len(digits)
	}
	return 0
}

func padLeft(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return strings.Repeat("0", width-len(s)) + s
}
