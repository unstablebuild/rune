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

package vte

import (
	"testing"

	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
)

func TestRuneWidthMatchesGraphemeCluster(t *testing.T) {
	t.Parallel()

	for c := rune(0); c <= 0x10FFFF; c++ {
		if c >= 0xD800 && c <= 0xDFFF {
			continue
		}
		want := graphemecluster.StringWidth(string(c))
		if c < 0x7F {
			// Printable ASCII short-circuits to one cell; the C0
			// controls never reach runeWidth from the parser.
			want = 1
		}
		if got := runeWidth(c); got != want {
			t.Fatalf("runeWidth(%U) = %d, want %d", c, got, want)
		}
	}
}
