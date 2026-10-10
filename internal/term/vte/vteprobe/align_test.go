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

package vteprobe

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAlignByGutter(t *testing.T) {
	t.Parallel()

	lines := cellLines([]string{
		"package main",
		"",
		"import \"fmt\"",
		"",
		"func main() {",
		"\tfmt.Println(\"hello\")",
		"}",
	})
	rows := []extractedRow{
		makeRow(" 1 package main"),
		makeRow(" 2 "),
		makeRow(" 3 import \"fmt\""),
		makeRow(" 4 "),
		makeRow(" 5 func main() {"),
		makeRow(" 6     fmt.Println(\"hello\")"),
		makeRow(" 7 }"),
	}
	g := detectGutter(rows, 0, len(rows)-1)
	assert.True(t, g.present)

	a := alignByGutter(rows, 0, len(rows)-1, g, lines, []int{4, 2, 8}, NewSlab())
	assert.True(t, a.ok, "expected aligned, coverage=%v", a.coverage)
	assert.Equal(t, 1, a.topFileLine)
	assert.Equal(t, 4, a.tabstop, "should infer tabstop 4 because line 6 renders 4 spaces before fmt")
}

func TestAlignByContent(t *testing.T) {
	t.Parallel()

	lines := cellLines([]string{
		"package main",
		"",
		"import \"fmt\"",
		"",
		"func main() {",
		"\tfmt.Println(\"hello\")",
		"}",
	})
	rows := []extractedRow{
		makeRow("import \"fmt\""),
		makeRow(""),
		makeRow("func main() {"),
		makeRow("    fmt.Println(\"hello\")"),
		makeRow("}"),
	}

	a := alignByContentWithGutter(rows, 0, len(rows)-1, 0, lines, []int{4, 2, 8}, NewSlab())
	assert.True(t, a.ok, "expected aligned, coverage=%v", a.coverage)
	assert.Equal(t, 3, a.topFileLine)
	assert.Equal(t, 4, a.tabstop)
}

func TestAlignWithWrapAmbiguousAnchor(t *testing.T) {
	t.Parallel()

	// A file with several lone "}" lines so the anchor row is ambiguous,
	// and one long line that wraps at the 20-column body width.
	long := "\tx := aaaaaaaaaa + bbbbbbbbbb + cccccccccc" // > 20 cols expanded
	lines := cellLines([]string{
		"func a() {", // 1
		"\treturn",   // 2
		"}",          // 3
		"func b() {", // 4
		long,         // 5 (wraps)
		"}",          // 6
		"func c() {", // 7
		"\treturn",   // 8
		"}",          // 9
	})
	// Render the band starting at file line 6 (a lone "}"), which also
	// appears at lines 3 and 9. Width 20 forces the long line to wrap,
	// but here the band starts after it.
	const width = 20
	rows := []extractedRow{
		makeRowW("}", width),              // line 6
		makeRowW("func c() {", width),     // line 7
		makeRowW("        return", width), // line 8 (tab -> 8 spaces)
		makeRowW("}", width),              // line 9
	}

	a := alignByContentWithGutter(rows, 0, len(rows)-1, 0, lines, []int{8, 4, 2}, NewSlab())
	assert.True(t, a.ok, "expected aligned, coverage=%v", a.coverage)
	assert.Equal(t, 6, a.topFileLine,
		"should anchor on line 6, not the ambiguous lines 3 or 9")
}

func TestAlignTailViewNotAnchoredPastEOF(t *testing.T) {
	t.Parallel()

	// One long line (line 2) wraps across two rows at the 20-col body
	// width. The band starts on a lone "}" (line 3) that recurs at the
	// end of the file (line 6, the last line).
	const width = 20
	long := "\tprint(aaaaaaaaaa, bbbbbbbbbb)" // expands well past 20 cols
	lines := cellLines([]string{
		"func a() {", // 1
		long,         // 2 (wraps -> 2 rows)
		"}",          // 3
		"func b() {", // 4
		"\tnoop()",   // 5
		"}",          // 6 (last line, recurs)
	})
	// Visible band: line 3 "}" at the top, then 4,5,6 and "~" filler.
	// A correct 1:1 anchor at line 3 also matches; but to force the
	// wrap path to be the discriminator we include the wrapped line's
	// continuation as if the band scrolled to show line 3 first — the
	// regression is that anchoring on the last line (6) scored a
	// vacuous 1.0 by mapping rows 1..n past EOF and skipping them.
	rows := []extractedRow{
		makeRowW("}", width),              // line 3
		makeRowW("func b() {", width),     // line 4
		makeRowW("        noop()", width), // line 5 (tab -> 8 spaces)
		makeRowW("}", width),              // line 6
		makeRowW("~", width),              // filler past EOF
		makeRowW("~", width),              // filler past EOF
		makeRowW("~", width),              // filler past EOF
	}

	// Score the vacuous tail anchor (top line = 6) directly: only its
	// single "}" row is in bounds, the rest are real "~" filler. With
	// the fix this must NOT reach a perfect score that out-ranks the
	// true anchor at line 3.
	band := len(rows)
	tailMap := make([]int, band)
	tailMap[0] = 6 // "}" on the last line; rows 1..n fall past EOF
	tailCov := scoreAlignment(rows, 0, 0, tailMap, lines, 8, NewSlab())

	trueMap := []int{3, 4, 5, 6, 0, 0, 0}
	trueCov := scoreAlignment(rows, 0, 0, trueMap, lines, 8, NewSlab())

	assert.Greater(t, trueCov, tailCov,
		"true anchor (line 3) must score above the vacuous tail anchor (line 6): true=%v tail=%v",
		trueCov, tailCov)

	a := alignByContentWithGutter(rows, 0, len(rows)-1, 0, lines, []int{8, 4, 2}, NewSlab())
	assert.True(t, a.ok, "expected aligned, coverage=%v", a.coverage)
	assert.Equal(t, 3, a.topFileLine,
		"should anchor on line 3, not the last line 6")
}
