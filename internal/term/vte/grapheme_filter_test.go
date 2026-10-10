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
	"fmt"
	"testing"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
)

func TestMayExtendClusterNeverRejectsAContinuation(t *testing.T) {
	t.Parallel()

	bases := []struct {
		name string
		text string
	}{
		{"latin", "a"},
		{"cjk", "中"},
		{"pictographic", "🙂"},
		{"zwj_sequence", "🙂\u200d"},
		{"regional_indicator", "🇦"},
		{"hangul_lead", "ᄀ"},
		{"hangul_syllable", "가"},
		{"devanagari", "क"},
		{"carriage_return", "\r"},
	}

	for _, base := range bases {
		t.Run(base.name, func(t *testing.T) {
			t.Parallel()
			last, _ := utf8.DecodeLastRuneInString(base.text)
			for c := rune(0x300); c <= 0x10FFFF; c++ {
				if c >= 0xD800 && c <= 0xDFFF {
					continue
				}
				_, rest, _, _ := uniseg.FirstGraphemeClusterInString(
					base.text+string(c), -1)
				if rest != "" {
					continue
				}
				width := graphemecluster.StringWidth(string(c))
				if !mayContinueAnyCluster(c, width) {
					t.Fatalf("mayContinueAnyCluster(%U, width=%d) = false, but "+
						"uniseg merges it into the preceding cluster %q; the "+
						"cluster would be split across cells", c, width, base.text)
				}
				if !mayExtendCluster(last, c, width) {
					t.Fatalf("mayExtendCluster(%U, %U, width=%d) = false, but "+
						"uniseg merges it into the preceding cluster %q; the "+
						"cluster would be split across cells",
						last, c, width, base.text)
				}
			}
		})
	}
}

func TestMergeContinuationClusters(t *testing.T) {
	t.Parallel()

	suite := []struct {
		text  string
		cells int
	}{
		{"e\u0301", 1},            // combining acute
		{"❤\ufe0f", 1},            // variation selector 16
		{"👍\U0001F3FD", 1},        // skin-tone modifier
		{"👨\u200d👩\u200d👧", 1},    // ZWJ family
		{"🇺🇸", 1},                 // regional indicator pair
		{"각", 1},                  // precomposed hangul syllable
		{"\u1100\u1161\u11A8", 1}, // decomposed hangul jamo
		{"中文", 2},                 // unrelated CJK: never merges
		{"🙂🙂", 2},                 // unrelated pictographs
	}

	for i, test := range suite {
		t.Run(fmt.Sprintf("%d_%q", i, test.text), func(t *testing.T) {
			t.Parallel()
			ph := newBenchParserHandler(40, 4)
			for _, c := range test.text {
				ph.Input(c)
			}
			row := ph.sync.primBuf.Cells.RawCells()[0]
			var got int
			for _, cell := range row {
				if cell.Ch != ' ' && cell.Ch != 0 {
					got++
				}
			}
			if got != test.cells {
				t.Fatalf("%q occupied %d cells, want %d", test.text, got, test.cells)
			}
		})
	}
}
