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

package gui

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

// cellRow renders text as a cell row, expanding wide runes into a cell
// carrying the rune plus a Ch == 0 continuation half, as the writer does.
func cellRow(text string) []term.Cell {
	var row []term.Cell
	for _, r := range text {
		width := 1
		if utf8.RuneLen(r) > 2 {
			width = 2
		}
		row = append(row, term.Cell{Ch: r, Width: uint8(width)})
		for range width - 1 {
			row = append(row, term.Cell{})
		}
	}
	return row
}

// writeRow draws text at row y, one rune per cell.
func writeRow(w term.Writer, y int, text string) {
	x := 0
	for _, r := range text {
		w.SetCell(term.Coordinates{X: x, Y: y}, term.Cell{Ch: r, Width: 1})
		x++
	}
}

// runeRow builds a row one cell per rune, bypassing cellRow's UTF-8
// handling so a test can plant code points no string can hold.
func runeRow(runes ...rune) []term.Cell {
	row := make([]term.Cell, len(runes))
	for i, r := range runes {
		row[i] = term.Cell{Ch: r, Width: 1}
	}
	return row
}

// clusterCell renders one grapheme cluster the way the writer does: the
// leading code point in Ch, the rest of the cluster in Combining, and a
// Ch == 0 continuation half for every extra column it occupies.
func clusterCell(width int, runes ...rune) []term.Cell {
	cell := term.Cell{Ch: runes[0], Width: uint8(width)}
	if len(runes) > 1 {
		cell.SetCombining(runes[1:])
	}
	row := []term.Cell{cell}
	for range width - 1 {
		row = append(row, term.Cell{})
	}
	return row
}

const (
	zeroWidthJoiner  = '\u200d'
	combiningAcute   = '\u0301'
	variationSelect  = '\ufe0f'
	regionalJ        = '\U0001F1EF'
	regionalP        = '\U0001F1F5'
	skinToneModifier = '\U0001F3FD'
)

// markWrapped records on each listed row that its line overflowed and
// continues on the next one, which is how the terminal and the editor
// report a wrap.
func markWrapped(grid [][]term.Cell, rows ...int) [][]term.Cell {
	for _, y := range rows {
		row := grid[y]
		end := len(row)
		for end > 0 && row[end-1].Ch == 0 {
			end--
		}
		row[end-1].Bytes = cell.WrapMarker
	}
	return grid
}

func TestRowScannerScanRow(t *testing.T) {
	suite := []struct {
		description string
		row         []term.Cell
		expected    []linkSpan
	}{
		{
			description: "no urls",
			row:         cellRow("just some prose with a colon: here"),
		},
		{
			description: "bare https url",
			row:         cellRow("https://example.com"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 19, url: "https://example.com"},
			},
		},
		{
			description: "trailing sentence punctuation is not part of the url",
			row:         cellRow("see https://example.com."),
			expected: []linkSpan{
				{y: 3, x0: 4, x1: 23, url: "https://example.com"},
			},
		},
		{
			description: "repeated trailing punctuation is trimmed",
			row:         cellRow("really? https://example.com?!"),
			expected: []linkSpan{
				{y: 3, x0: 8, x1: 27, url: "https://example.com"},
			},
		},
		{
			description: "unbalanced closing paren is trimmed",
			row:         cellRow("(see https://example.com/a)"),
			expected: []linkSpan{
				{y: 3, x0: 5, x1: 26, url: "https://example.com/a"},
			},
		},
		{
			description: "balanced parens are kept",
			row:         cellRow("https://en.wikipedia.org/wiki/Go_(language)"),
			expected: []linkSpan{
				{
					y: 3, x0: 0, x1: 43,
					url: "https://en.wikipedia.org/wiki/Go_(language)",
				},
			},
		},
		{
			description: "unbalanced closing bracket is trimmed",
			row:         cellRow("[https://example.com/a]"),
			expected: []linkSpan{
				{y: 3, x0: 1, x1: 22, url: "https://example.com/a"},
			},
		},
		{
			description: "http scheme",
			row:         cellRow("http://localhost:8080/x"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 23, url: "http://localhost:8080/x"},
			},
		},
		{
			description: "file scheme",
			row:         cellRow("file:///tmp/notes.md"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 20, url: "file:///tmp/notes.md"},
			},
		},
		{
			description: "scheme with no host is not a link",
			row:         cellRow("https:// and http://"),
		},
		{
			description: "blank cells bound the scan",
			row: append(append(cellRow("https://a.com"),
				term.Cell{}),
				cellRow("https://b.com")...),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 13, url: "https://a.com"},
				{y: 3, x0: 14, x1: 27, url: "https://b.com"},
			},
		},
		{
			description: "adjacent urls separated by a space",
			row:         cellRow("https://a.com https://b.com"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 13, url: "https://a.com"},
				{y: 3, x0: 14, x1: 27, url: "https://b.com"},
			},
		},
		{
			description: "wide characters shift cell coordinates",
			row:         cellRow("日本 https://example.com"),
			expected: []linkSpan{
				{y: 3, x0: 5, x1: 24, url: "https://example.com"},
			},
		},
		{
			description: "a trailing wide character stays in the url",
			row:         cellRow("https://example.com日"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 20, url: "https://example.com日"},
			},
		},
		{
			description: "angle brackets delimit the url",
			row:         cellRow("<https://example.com>"),
			expected: []linkSpan{
				{y: 3, x0: 1, x1: 20, url: "https://example.com"},
			},
		},
		{
			description: "https is not truncated to http",
			row:         cellRow("https://example.com/http://x"),
			expected: []linkSpan{
				{
					y: 3, x0: 0, x1: 28,
					url: "https://example.com/http://x",
				},
			},
		},
		{
			description: "a false start does not hide the scheme behind it",
			row:         cellRow("hhttps://a.com"),
			expected: []linkSpan{
				{y: 3, x0: 1, x1: 14, url: "https://a.com"},
			},
		},
		{
			description: "a word starting with h is not a scheme",
			row:         cellRow("here we have http and file but no url"),
		},
		{
			description: "a truncated scheme at the end of the row",
			row:         cellRow("trailing http"),
		},

		// Degenerate rows.
		{
			description: "a nil row",
			row:         nil,
		},
		{
			description: "an empty row",
			row:         []term.Cell{},
		},
		{
			description: "a row of blank cells",
			row:         make([]term.Cell, 16),
		},
		{
			description: "a lone scheme-opening rune",
			row:         cellRow("h"),
		},
		{
			description: "a lone file-opening rune",
			row:         cellRow("f"),
		},
		{
			description: "nothing but scheme-opening runes",
			row:         cellRow("hhhhhffffff"),
		},

		// Scheme boundaries.
		{
			description: "a scheme one cell short of complete",
			row:         cellRow("https:/"),
		},
		{
			description: "https with no host at all",
			row:         cellRow("https://"),
		},
		{
			description: "http with no host at all",
			row:         cellRow("http://"),
		},
		{
			description: "file with no host at all",
			row:         cellRow("file://"),
		},
		{
			description: "a host of exactly one cell",
			row:         cellRow("https://a"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 9, url: "https://a"},
			},
		},
		{
			description: "a blank cell inside the scheme breaks it",
			row:         runeRow('h', 't', 't', 'p', 's', 0, '/', '/', 'a'),
		},
		{
			description: "an uppercase scheme is not matched",
			row:         cellRow("HTTPS://A.COM"),
		},
		{
			description: "a capitalized scheme is not matched",
			row:         cellRow("Https://a.com"),
		},
		{
			description: "a scheme glued to the preceding word",
			row:         cellRow("xhttps://a.com"),
			expected: []linkSpan{
				{y: 3, x0: 1, x1: 14, url: "https://a.com"},
			},
		},
		{
			description: "a second scheme inside the first url is not split off",
			row:         cellRow("https://a.comhttp://b"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 21, url: "https://a.comhttp://b"},
			},
		},

		// Trimming down to nothing.
		{
			description: "a scheme followed only by punctuation has no host",
			row:         cellRow("https://....."),
		},
		{
			description: "a scheme followed only by closing brackets has no host",
			row:         cellRow("https://)))"),
		},
		{
			description: "every trailing punctuation mark is trimmed",
			row:         cellRow("https://a.com...!?,;:"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 13, url: "https://a.com"},
			},
		},
		{
			description: "repeated unbalanced brackets are all trimmed",
			row:         cellRow("https://a.com]]]"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 13, url: "https://a.com"},
			},
		},
		{
			description: "an unbalanced brace is trimmed",
			row:         cellRow("https://a.com}"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 13, url: "https://a.com"},
			},
		},
		{
			description: "balanced braces are kept",
			row:         cellRow("https://a.com/{x}"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 17, url: "https://a.com/{x}"},
			},
		},
		{
			description: "nested balanced brackets are kept",
			row:         cellRow("https://a.com/(x[y])"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 20, url: "https://a.com/(x[y])"},
			},
		},

		// Terminators.
		{
			description: "a tab ends the url",
			row:         cellRow("https://a.com\tmore"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 13, url: "https://a.com"},
			},
		},
		{
			description: "a control character ends the url",
			row:         append(cellRow("https://a.com"), runeRow(0x01, 'x')...),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 13, url: "https://a.com"},
			},
		},
		{
			description: "delete ends the url",
			row:         append(cellRow("https://a.com"), runeRow(0x7f, 'x')...),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 13, url: "https://a.com"},
			},
		},
		{
			description: "every prose delimiter ends the url",
			row:         cellRow(`https://a.com"'` + "`" + `<>\|^`),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 13, url: "https://a.com"},
			},
		},

		// Markdown links.
		{
			description: "a markdown link ends at its closing paren",
			row:         cellRow("[docs](https://rune.build/docs)"),
			expected: []linkSpan{
				{y: 3, x0: 7, x1: 30, url: "https://rune.build/docs"},
			},
		},
		{
			description: "a markdown link whose text is its url",
			row:         cellRow("[https://a.com](https://a.com)"),
			expected: []linkSpan{
				{y: 3, x0: 1, x1: 14, url: "https://a.com"},
				{y: 3, x0: 16, x1: 29, url: "https://a.com"},
			},
		},
		{
			description: "a markdown image nested in a markdown link",
			row: cellRow("[![Discord](https://img.shields.io/discord/1068976834382925865)]" +
				"(https://discord.gg/ZjZadyC7PK)"),
			expected: []linkSpan{
				{
					y: 3, x0: 12, x1: 62,
					url: "https://img.shields.io/discord/1068976834382925865",
				},
				{y: 3, x0: 65, x1: 94, url: "https://discord.gg/ZjZadyC7PK"},
			},
		},
		{
			description: "adjacent markdown links",
			row:         cellRow("[a](https://a.com)[b](https://b.com)"),
			expected: []linkSpan{
				{y: 3, x0: 4, x1: 17, url: "https://a.com"},
				{y: 3, x0: 22, x1: 35, url: "https://b.com"},
			},
		},
		{
			description: "a reference-style markdown link",
			row:         cellRow("[https://a.com][1]"),
			expected: []linkSpan{
				{y: 3, x0: 1, x1: 14, url: "https://a.com"},
			},
		},
		{
			description: "a markdown link to a url with balanced parens",
			row:         cellRow("[Go](https://en.wikipedia.org/wiki/Go_(language))"),
			expected: []linkSpan{
				{
					y: 3, x0: 5, x1: 48,
					url: "https://en.wikipedia.org/wiki/Go_(language)",
				},
			},
		},
		{
			description: "a markdown link to a url with nested brackets",
			row:         cellRow("[x](https://a.com/(x[y]))"),
			expected: []linkSpan{
				{y: 3, x0: 4, x1: 24, url: "https://a.com/(x[y])"},
			},
		},
		{
			description: "a markdown link ending a sentence",
			row:         cellRow("see [a](https://a.com)."),
			expected: []linkSpan{
				{y: 3, x0: 8, x1: 21, url: "https://a.com"},
			},
		},

		// Unicode.
		{
			description: "a narrow non-ascii host is kept whole",
			row:         cellRow("https://café.fr"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 15, url: "https://café.fr"},
			},
		},
		{
			// A wide rune is followed by a Ch == 0 continuation half,
			// which is indistinguishable from a blank cell and so ends
			// the url. A wide host therefore links only its first rune.
			description: "a wide rune truncates the url at its continuation half",
			row:         cellRow("https://例え.jp"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 9, url: "https://例"},
			},
		},
		{
			description: "an emoji ends the url but stays in it",
			row:         cellRow("https://a.com/😀"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 15, url: "https://a.com/😀"},
			},
		},
		{
			// The rendered host is café; opening cafe.fr instead would
			// silently send the user somewhere else.
			description: "a combining accent stays in the host",
			row: slices.Concat(
				cellRow("https://caf"),
				clusterCell(1, 'e', combiningAcute),
				cellRow(".fr"),
			),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 15, url: "https://cafe\u0301.fr"},
			},
		},
		{
			description: "a zwj emoji cluster is kept whole",
			row: slices.Concat(
				cellRow("https://a.com/"),
				clusterCell(2, '👨', zeroWidthJoiner, '👧', zeroWidthJoiner, '👦'),
			),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 15, url: "https://a.com/👨\u200d👧\u200d👦"},
			},
		},
		{
			description: "a regional indicator pair is kept whole",
			row: slices.Concat(
				cellRow("https://a.com/"),
				clusterCell(2, regionalJ, regionalP),
			),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 15, url: "https://a.com/🇯🇵"},
			},
		},
		{
			description: "a skin tone modifier is kept whole",
			row: slices.Concat(
				cellRow("https://a.com/"),
				clusterCell(2, '👍', skinToneModifier),
			),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 15, url: "https://a.com/👍\U0001F3FD"},
			},
		},
		{
			description: "a variation selector is kept whole",
			row: slices.Concat(
				cellRow("https://a.com/"),
				clusterCell(2, '❤', variationSelect),
			),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 15, url: "https://a.com/❤\ufe0f"},
			},
		},
		{
			description: "an empty combining slice is harmless",
			row: append(cellRow("https://a"), term.Cell{
				Ch: 'b', Width: 1, Extra: &term.CellExtra{Combining: []rune{}},
			}),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 10, url: "https://ab"},
			},
		},
		{
			description: "a cluster of blank base and marks is skipped",
			row: slices.Concat(
				cellRow("https://a"),
				[]term.Cell{{Ch: 0, Extra: &term.CellExtra{Combining: []rune{combiningAcute}}}},
				cellRow("b"),
			),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 9, url: "https://a"},
			},
		},
		{
			// The cluster's continuation half is indistinguishable from
			// a blank cell, so it ends the url just as a wide rune does.
			description: "a cluster truncates the url at its continuation half",
			row: slices.Concat(
				cellRow("https://a.com/"),
				clusterCell(2, '👨', zeroWidthJoiner, '👧'),
				cellRow("/x"),
			),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 15, url: "https://a.com/👨\u200d👧"},
			},
		},

		// Code points no well-formed frame should carry.
		{
			description: "a surrogate half is replaced, not fatal",
			row:         append(cellRow("https://a"), runeRow(0xD800)...),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 10, url: "https://a\uFFFD"},
			},
		},
		{
			description: "a rune past the unicode range is replaced, not fatal",
			row:         append(cellRow("https://a"), runeRow(utf8.MaxRune+1)...),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 10, url: "https://a\uFFFD"},
			},
		},
		{
			description: "a negative rune is replaced, not fatal",
			row:         append(cellRow("https://a"), runeRow(-1)...),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 10, url: "https://a\uFFFD"},
			},
		},
		{
			description: "a nonsense cell width is ignored",
			row: []term.Cell{
				{Ch: 'h', Width: 0}, {Ch: 't', Width: 200}, {Ch: 't'},
				{Ch: 'p', Width: 7}, {Ch: 's'}, {Ch: ':'}, {Ch: '/'},
				{Ch: '/'}, {Ch: 'a', Width: 255},
			},
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 9, url: "https://a"},
			},
		},
		{
			description: "bare path with line and column",
			row:         cellRow("a/b.go:12:3"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 11, path: "a/b.go", line: 12, col: 3},
			},
		},
		{
			description: "bare path with a line only",
			row:         cellRow("a/b.go:12"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 9, path: "a/b.go", line: 12},
			},
		},
		{
			description: "the colon a compiler appends is not part of the match",
			row:         cellRow("./b.go:1:1:"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 10, path: "./b.go", line: 1, col: 1},
			},
		},
		{
			description: "a path inside a compiler message",
			row: cellRow(
				"internal/term/gui/link.go:442:36: not enough arguments in call"),
			expected: []linkSpan{
				{
					y: 3, x0: 0, x1: 32,
					path: "internal/term/gui/link.go", line: 442, col: 36,
				},
			},
		},
		{
			description: "grep output keeps the line and drops the match text",
			row:         cellRow("cmd/main.go:7:func main() {"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 13, path: "cmd/main.go", line: 7},
			},
		},
		{
			description: "a path in parentheses",
			row:         cellRow("(a/b.go:12)"),
			expected: []linkSpan{
				{y: 3, x0: 1, x1: 10, path: "a/b.go", line: 12},
			},
		},
		{
			description: "a path in quotes",
			row:         cellRow(`"a/b.go:12:3"`),
			expected: []linkSpan{
				{y: 3, x0: 1, x1: 12, path: "a/b.go", line: 12, col: 3},
			},
		},
		{
			description: "a path after a key and an equals sign",
			row:         cellRow("path=a/b.go:3"),
			expected: []linkSpan{
				{y: 3, x0: 5, x1: 13, path: "a/b.go", line: 3},
			},
		},
		{
			description: "brackets inside a path are kept",
			row:         cellRow("app/[id]/page.tsx:9"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 19, path: "app/[id]/page.tsx", line: 9},
			},
		},
		{
			description: "an absolute path",
			row:         cellRow("at /usr/lib/x.c:5:1"),
			expected: []linkSpan{
				{y: 3, x0: 3, x1: 19, path: "/usr/lib/x.c", line: 5, col: 1},
			},
		},
		{
			description: "a home relative path",
			row:         cellRow("~/src/x.go:5"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 12, path: "~/src/x.go", line: 5},
			},
		},
		{
			description: "a bare file name with an extension",
			row:         cellRow("main.go:5:2: oops"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 11, path: "main.go", line: 5, col: 2},
			},
		},
		{
			description: "a windows path keeps its drive letter",
			row:         cellRow(`C:\x\y.go:12:3`),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 14, path: `C:\x\y.go`, line: 12, col: 3},
			},
		},
		{
			description: "a windows drive letter is not a line number",
			row:         cellRow(`C:\x\dir and D:/tmp`),
		},
		{
			description: "a windows path with forward slashes",
			row:         cellRow("D:/src/y.go:4"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 13, path: "D:/src/y.go", line: 4},
			},
		},
		{
			description: "two paths in one row",
			row:         cellRow("a/b.go:1 then c/d.go:2:3"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 8, path: "a/b.go", line: 1},
				{y: 3, x0: 14, x1: 24, path: "c/d.go", line: 2, col: 3},
			},
		},
		{
			description: "a url and a path in one row",
			row:         cellRow("see https://e.com/a and a/b.go:4"),
			expected: []linkSpan{
				{y: 3, x0: 4, x1: 19, url: "https://e.com/a"},
				{y: 3, x0: 24, x1: 32, path: "a/b.go", line: 4},
			},
		},
		{
			description: "a position is not read from an http url",
			row:         cellRow("https://e.com/a.go:12:3"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 23, url: "https://e.com/a.go:12:3"},
			},
		},
		{
			description: "a port is not a position",
			row:         cellRow("http://h:8080"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 13, url: "http://h:8080"},
			},
		},
		{
			description: "file url with line and column",
			row:         cellRow("file:///x/y.go:7:2"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 18, url: "file:///x/y.go", line: 7, col: 2},
			},
		},
		{
			description: "file url with a line only",
			row:         cellRow("file:///x/y.go:7"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 16, url: "file:///x/y.go", line: 7},
			},
		},
		{
			description: "file url with a trailing colon after the position",
			row:         cellRow("file:///x/y.go:7:2: oops"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 18, url: "file:///x/y.go", line: 7, col: 2},
			},
		},
		{
			description: "file url without a position",
			row:         cellRow("file:///x/y.go"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 14, url: "file:///x/y.go"},
			},
		},
		{
			description: "a word followed by a number is prose",
			row:         cellRow("see foo:1 and note:2"),
		},
		{
			description: "a time is prose",
			row:         cellRow("meet at 12:30 or 9:15:00"),
		},
		{
			description: "an address with a port is prose",
			row:         cellRow("listening on 127.0.0.1:8080"),
		},
		{
			description: "a version is prose",
			row:         cellRow("upgrade to v1.2:3"),
		},
		{
			description: "a path without a position is not matched",
			row:         cellRow("see a/b.go: not found"),
		},
		{
			description: "a position running into a word is not matched",
			row:         cellRow("a/b.go:12x"),
		},
		{
			description: "a line too long to be a position is not matched",
			row:         cellRow("a/b.go:1234567890"),
		},
		{
			description: "a url with an unknown scheme is not a path",
			row:         cellRow("ftp://h/x.go:12"),
		},
		{
			description: "a prefix before a colon is not part of the path",
			row:         cellRow("see foo:bar/a.go:3 here"),
			expected: []linkSpan{
				{y: 3, x0: 8, x1: 18, path: "bar/a.go", line: 3},
			},
		},
		{
			description: "a label with no space after its colon",
			row:         cellRow("FAIL:pkg/a.go:3"),
			expected: []linkSpan{
				{y: 3, x0: 5, x1: 15, path: "pkg/a.go", line: 3},
			},
		},
		{
			description: "a compiler prefix with no space after its colon",
			row:         cellRow("error:src/x.c:1:9: oops"),
			expected: []linkSpan{
				{y: 3, x0: 6, x1: 17, path: "src/x.c", line: 1, col: 9},
			},
		},
		{
			description: "a bare name after a label",
			row:         cellRow("FAIL:main.go:12"),
			expected: []linkSpan{
				{y: 3, x0: 5, x1: 15, path: "main.go", line: 12},
			},
		},
		{
			description: "a windows path after a space keeps its drive letter",
			row:         cellRow(`see C:\x\y.go:12`),
			expected: []linkSpan{
				{y: 3, x0: 4, x1: 16, path: `C:\x\y.go`, line: 12},
			},
		},
		{
			description: "a windows path after a label keeps its drive letter",
			row:         cellRow(`error:C:\x\y.go:12`),
			expected: []linkSpan{
				{y: 3, x0: 6, x1: 18, path: `C:\x\y.go`, line: 12},
			},
		},
		{
			description: "a letter that is part of a word is not a drive",
			row:         cellRow(`abc:/x/y.go:3`),
			expected: []linkSpan{
				{y: 3, x0: 4, x1: 13, path: "/x/y.go", line: 3},
			},
		},
		{
			description: "a host and port is prose",
			row:         cellRow("listening on example.com:8080"),
		},
		{
			description: "a host with several labels and a port is prose",
			row:         cellRow("dial api.github.com:443"),
		},
		{
			description: "a host and port followed by a colon is prose",
			row:         cellRow("dial tcp example.org:80: refused"),
		},
		{
			description: "a host with a path and a port is prose",
			row:         cellRow("localhost:8080/x"),
		},
		{
			description: "an ipv6 host and port is prose",
			row:         cellRow("listening on [::1]:8080"),
		},
		{
			description: "a bare name with a source extension is a file",
			row:         cellRow("foo_test.go:42"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 14, path: "foo_test.go", line: 42},
			},
		},
		{
			description: "a bare name with several dots and a source extension",
			row:         cellRow("api.pb.go:42:7"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 14, path: "api.pb.go", line: 42, col: 7},
			},
		},
		{
			description: "a bare name with an uppercase extension",
			row:         cellRow("README.MD:3"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 11, path: "README.MD", line: 3},
			},
		},
		{
			description: "a host path with a separator is a path",
			row:         cellRow("example.com/a.go:8080"),
			expected: []linkSpan{
				{y: 3, x0: 0, x1: 21, path: "example.com/a.go", line: 8080},
			},
		},
	}

	for _, tc := range suite {
		t.Run(tc.description, func(t *testing.T) {
			var s rowScanner
			got := s.scanRow(3, tc.row, nil)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func TestRowScannerReusesScratchWithoutLeaking(t *testing.T) {
	var s rowScanner
	rows := []string{
		"https://first.example/aaaaaaaaaaaaaaaaaaaa",
		"https://second",
		"no link at all",
		"https://third",
	}
	for i, text := range rows {
		got := s.scanRow(i, cellRow(text), nil)
		if i == 2 {
			assert.Empty(t, got)
			continue
		}
		require.Len(t, got, 1)
		assert.Equal(t, text, got[0].url)
	}
}

func TestRowScannerScanRowHandlesAPathologicallyLongURL(t *testing.T) {
	text := "https://a.example/" + strings.Repeat("x", 8192)
	got := new(rowScanner).scanRow(0, cellRow(text), nil)
	require.Len(t, got, 1)
	assert.Equal(t, text, got[0].url)
	assert.Equal(t, len(text), got[0].x1)
}

func TestRowScannerScanRowHandlesManyURLsInOneRow(t *testing.T) {
	const count = 500
	var text strings.Builder
	for range count {
		text.WriteString("https://a.co ")
	}
	got := new(rowScanner).scanRow(0, cellRow(text.String()), nil)
	require.Len(t, got, count)
	for i, span := range got {
		assert.Equal(t, "https://a.co", span.url)
		assert.Equal(t, i*13, span.x0)
		assert.Equal(t, i*13+12, span.x1)
	}
}

func TestTrimURL(t *testing.T) {
	suite := []struct {
		raw, expected string
	}{
		{"", ""},
		{".", ""},
		{"....,,;;::!!??", ""},
		{")", ")"},
		{"()", "()"},
		{"a.", "a"},
		{"a", "a"},
		{"(a).", "(a)"},
		{"a.b.", "a.b"},
	}
	for _, tc := range suite {
		t.Run(tc.raw, func(t *testing.T) {
			assert.Equal(t, tc.expected, string(trimURL([]byte(tc.raw))))
		})
	}
}

func TestHasHost(t *testing.T) {
	suite := []struct {
		url      string
		expected bool
	}{
		{"", false},
		{"https", false},
		{"https:/", false},
		{"https://", false},
		{"https://a", true},
		{"://", false},
		{"://a", true},
		{"a://b", true},
	}
	for _, tc := range suite {
		t.Run(tc.url, func(t *testing.T) {
			assert.Equal(t, tc.expected, hasHost([]byte(tc.url)))
		})
	}
}

func TestIsURLRune(t *testing.T) {
	for _, r := range []rune{
		' ', '\t', '\n', '\r', 0, 0x01, 0x1f, 0x7f,
		'"', '\'', '`', '<', '>', '\\', '|', '^',
	} {
		assert.False(t, isURLRune(r), "%q must end a url", r)
	}
	for _, r := range []rune{
		'a', 'Z', '0', '/', ':', '?', '=', '&', '%', '#', '~', '+',
		'(', ')', '[', ']', '{', '}', '.', ',', ';', '!', '-', '_',
		'é', '例', '😀', 0x80, utf8.MaxRune,
	} {
		assert.True(t, isURLRune(r), "%q must stay in a url", r)
	}
}

func TestSchemeAt(t *testing.T) {
	suite := []struct {
		description string
		row         []term.Cell
		x           int
		width       int
		ok          bool
	}{
		{description: "empty row"},
		{description: "https", row: cellRow("https://"), width: 8, ok: true},
		{description: "http", row: cellRow("http://"), width: 7, ok: true},
		{description: "file", row: cellRow("file://"), width: 7, ok: true},
		{
			description: "offset into the row",
			row:         cellRow("xxhttp://"), x: 2, width: 7, ok: true,
		},
		{description: "one cell short", row: cellRow("http:/")},
		{
			description: "past the end of the row",
			row:         cellRow("http://"), x: 6,
		},
		{description: "start index at the row length", row: cellRow("a"), x: 1},
	}
	for _, tc := range suite {
		t.Run(tc.description, func(t *testing.T) {
			width, ok := schemeAt(tc.row, tc.x)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.width, width)
		})
	}
}

func TestRowScannerJoinsWrappedURLWithGraphemeCluster(t *testing.T) {
	first := cellRow("https://rune.build?aa")
	second := slices.Concat(
		clusterCell(1, 'e', combiningAcute),
		cellRow("b"),
		make([]term.Cell, len(first)-2),
	)
	const whole = "https://rune.build?aae\u0301b"

	got := new(rowScanner).scan(markWrapped([][]term.Cell{first, second}, 0), nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: 0, x1: 21, url: whole},
		{y: 1, x0: 0, x1: 2, url: whole},
	}, got)
}

// FuzzRowScannerScan guards the hand-rolled cell walking against panics
// and against spans that would index outside the frame they came from.
func FuzzRowScannerScan(f *testing.F) {
	f.Add("https://example.com", 80)
	f.Add("see https://a.com. and file:///tmp/x", 12)
	f.Add("https://a.com/(x[y]{z})...", 7)
	f.Add("https://", 8)
	f.Add("hhhhffff", 1)
	f.Add("https://例え.jp/パス", 5)
	f.Add("https://a\x00b\x7fc\td", 4)
	f.Add("", 0)
	f.Add("https://cafe\u0301.fr", 9)
	f.Add("https://a.com/👨\u200d👧\u200d👦", 6)
	f.Add("\u0301\u0301\u0301https://a.com\ufe0f", 3)
	f.Add("[![a](https://a.com/(x)y)](https://b.com/[z])", 9)
	f.Add("a/b.go:12:3: oops", 7)
	f.Add("(a/b.go:1) C:\\x\\y.go:2:3 file:///x:4:5", 5)
	f.Add(":1:2:3 ::: a:1 /:1 .:1", 3)
	f.Add("FAIL:pkg/a.go:3 example.com:8080 foo:bar/a.go:3", 20)
	f.Add("x.go:1 a/bcdefgh/ijklmnopq.go:5", 6)
	f.Add("a/b.go:12 3.go:5 C:\\x:/y.go:7", 9)

	f.Fuzz(func(t *testing.T, text string, width int) {
		width = min(max(width, 1), 256)
		var cells [][]term.Cell
		row := make([]term.Cell, 0, width)
		for _, r := range text {
			// Fold marks onto the cell they decorate, as the writer's
			// grapheme clustering does.
			if len(row) > 0 && isClusterContinuation(r) {
				cell := &row[len(row)-1]
				cell.SetCombining(append(slices.Clone(cell.CombiningRunes()), r))
				continue
			}
			if len(row) == width {
				cells = append(cells, row)
				row = make([]term.Cell, 0, width)
			}
			row = append(row, term.Cell{Ch: r, Width: 1})
		}
		if len(row) > 0 {
			cells = append(cells, row)
		}

		// Mark every other row as overflowing, so the join runs and a
		// span cannot escape without the address it stands for.
		for y := 0; y < len(cells); y += 2 {
			if len(cells[y]) > 0 {
				cells[y][len(cells[y])-1].Bytes = cell.WrapMarker
			}
		}

		var s rowScanner
		spans := s.scan(cells, nil)
		for i, span := range spans {
			require.GreaterOrEqual(t, span.y, 0)
			require.Less(t, span.y, len(cells))
			require.GreaterOrEqual(t, span.x0, 0)
			require.Greater(t, span.x1, span.x0)
			require.LessOrEqual(t, span.x1, len(cells[span.y]))
			require.True(t, span.url != "" || span.path != "")
			// overlay copies a row afresh whenever the row of the span
			// changes, so a row the spans leave and return to loses its
			// underlines, and a span over another hides it.
			if i > 0 {
				prev := spans[i-1]
				require.GreaterOrEqual(t, span.y, prev.y, "spans out of row order")
				if span.y == prev.y {
					require.True(t, span.x0 >= prev.x1 || prev.x0 >= span.x1,
						"spans overlap: %+v %+v", prev, span)
				}
			}
		}
	})
}

// isClusterContinuation reports whether r extends the preceding cell's
// grapheme cluster rather than opening a cell of its own.
func isClusterContinuation(r rune) bool {
	switch r {
	case zeroWidthJoiner, variationSelect, '\ufe0e':
		return true
	}
	return unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r)
}

func TestRowScannerScanAllRows(t *testing.T) {
	var s rowScanner
	cells := [][]term.Cell{
		cellRow("nothing here"),
		cellRow("go to https://example.com now"),
		cellRow("file:///tmp/a"),
	}
	got := s.scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 1, x0: 6, x1: 25, url: "https://example.com"},
		{y: 2, x0: 0, x1: 13, url: "file:///tmp/a"},
	}, got)
}

func TestRowScannerJoinsURLWrappedAcrossRows(t *testing.T) {
	suite := []struct {
		description string
		rows        []string
		// wrapped lists the rows whose line overflowed its pane.
		wrapped  []int
		expected []linkSpan
	}{
		{
			description: "url continued on the next row",
			rows: []string{
				"see https://rune.build?aaaa",
				"bbbb=cc",
			},
			wrapped: []int{0},
			expected: []linkSpan{
				{y: 0, x0: 4, x1: 27, url: "https://rune.build?aaaabbbb=cc"},
				{y: 1, x0: 0, x1: 7, url: "https://rune.build?aaaabbbb=cc"},
			},
		},
		{
			description: "url spanning three rows",
			rows: []string{
				"https://rune.build?aaaaaa",
				"bbbbbbbbbbbbbbbbbbbbbbbbb",
				"cc done",
			},
			wrapped: []int{0, 1},
			expected: []linkSpan{
				{
					y: 0, x0: 0, x1: 25,
					url: "https://rune.build?aaaaaabbbbbbbbbbbbbbbbbbbbbbbbbcc",
				},
				{
					y: 1, x0: 0, x1: 25,
					url: "https://rune.build?aaaaaabbbbbbbbbbbbbbbbbbbbbbbbbcc",
				},
				{
					y: 2, x0: 0, x1: 2,
					url: "https://rune.build?aaaaaabbbbbbbbbbbbbbbbbbbbbbbbbcc",
				},
			},
		},
		{
			description: "trailing punctuation on the continuation row is trimmed",
			rows: []string{
				"see https://rune.build?aaaa",
				"bbbb.",
			},
			wrapped: []int{0},
			expected: []linkSpan{
				{y: 0, x0: 4, x1: 27, url: "https://rune.build?aaaabbbb"},
				{y: 1, x0: 0, x1: 4, url: "https://rune.build?aaaabbbb"},
			},
		},
		{
			description: "a url that stops short of the edge is not joined",
			rows: []string{
				"see https://rune.build/a and more",
				"bbbb",
			},
			expected: []linkSpan{
				{y: 0, x0: 4, x1: 24, url: "https://rune.build/a"},
			},
		},
		{
			description: "a blank continuation row ends the url",
			rows: []string{
				"see https://rune.build?aaaa",
				"",
			},
			wrapped: []int{0},
			expected: []linkSpan{
				{y: 0, x0: 4, x1: 27, url: "https://rune.build?aaaa"},
			},
		},
		{
			description: "a continuation row starting with a space ends the url",
			rows: []string{
				"see https://rune.build?aaaa",
				" bbbb",
			},
			wrapped: []int{0},
			expected: []linkSpan{
				{y: 0, x0: 4, x1: 27, url: "https://rune.build?aaaa"},
			},
		},
		{
			description: "the last row of the frame ends the url",
			rows: []string{
				"see https://rune.build?aaaa",
			},
			wrapped: []int{0},
			expected: []linkSpan{
				{y: 0, x0: 4, x1: 27, url: "https://rune.build?aaaa"},
			},
		},
		{
			// Trimming eats the whole continuation, leaving it with no
			// cells to underline.
			description: "a continuation row of pure punctuation is dropped",
			rows: []string{
				"see https://rune.build?aaaa",
				"...",
			},
			wrapped: []int{0},
			expected: []linkSpan{
				{y: 0, x0: 4, x1: 27, url: "https://rune.build?aaaa"},
			},
		},
		{
			description: "a ragged frame does not misjudge the right edge",
			rows: []string{
				"https://rune.build?aa",
				"bb",
			},
			wrapped: []int{0},
			expected: []linkSpan{
				{y: 0, x0: 0, x1: 21, url: "https://rune.build?aabb"},
				{y: 1, x0: 0, x1: 2, url: "https://rune.build?aabb"},
			},
		},
		{
			description: "a paren opened before the wrap is closed after it",
			rows: []string{
				"[Go](https://en.wikipedia.org/wiki/Go_(",
				"language)) rest",
			},
			wrapped: []int{0},
			expected: []linkSpan{
				{
					y: 0, x0: 5, x1: 39,
					url: "https://en.wikipedia.org/wiki/Go_(language)",
				},
				{
					y: 1, x0: 0, x1: 9,
					url: "https://en.wikipedia.org/wiki/Go_(language)",
				},
			},
		},
		{
			description: "a markdown link closed on the continuation row",
			rows: []string{
				"[![ci](https://rune.build?aaaa",
				"bbbb)](#ci)",
			},
			wrapped: []int{0},
			expected: []linkSpan{
				{y: 0, x0: 7, x1: 30, url: "https://rune.build?aaaabbbb"},
				{y: 1, x0: 0, x1: 4, url: "https://rune.build?aaaabbbb"},
			},
		},
	}

	for _, tc := range suite {
		t.Run(tc.description, func(t *testing.T) {
			var s rowScanner
			cells := markWrapped(linkGrid(tc.rows...), tc.wrapped...)
			assert.Equal(t, tc.expected, s.scan(cells, nil))
		})
	}
}

// linkTestGUI installs a handler drawing rows and returns the GUI plus the
// URLs its link observer received.
func linkTestGUI(t *testing.T, rows ...string) (*GUI, *mockInputManager, *[]Link) {
	t.Helper()
	var opened []Link
	mock := mockHandler{
		assertDraw: func(w term.Writer) {
			for y, row := range rows {
				writeRow(w, y, row)
			}
		},
		assertEvent: func(term.Event) (bool, bool) { return false, true },
	}
	g, input := newTestGUI(t, &mock)
	require.NoError(t, WithLinkObserver(func(l Link) {
		opened = append(opened, l)
	})(g))
	g.drawHandler(g.ctx)
	return g, input, &opened
}

func metaDown(m *mockInputManager) {
	m.events = []ebiten.InputEvent{press(ebiten.KeyMetaLeft, ebiten.KeyModSuper)}
}

func metaUp(m *mockInputManager) {
	m.events = []ebiten.InputEvent{release(ebiten.KeyMetaLeft)}
}

func TestGUIMetaTransitionSchedulesRenderWithoutMouseInput(t *testing.T) {
	g, input, _ := linkTestGUI(t, "see https://example.com")
	g.needsRender = false

	metaDown(input)
	require.NoError(t, g.Update())
	assert.True(t, g.links.meta)
	assert.True(t, g.needsRender)

	g.needsRender = false
	input.events = nil
	require.NoError(t, g.Update())
	assert.True(t, g.links.meta, "meta stays held with no further events")
	assert.False(t, g.needsRender, "a steady modifier does not force a render")

	g.needsRender = false
	metaUp(input)
	require.NoError(t, g.Update())
	assert.False(t, g.links.meta)
	assert.True(t, g.needsRender)
}

// linkGrid renders rows into a cell grid, padding every row to the
// width of the widest one as the writer does.
func linkGrid(rows ...string) [][]term.Cell {
	grid := make([][]term.Cell, len(rows))
	width := 0
	for i, row := range rows {
		grid[i] = cellRow(row)
		width = max(width, len(grid[i]))
	}
	for i := range grid {
		for len(grid[i]) < width {
			grid[i] = append(grid[i], term.Cell{})
		}
	}
	return grid
}

func heldScanner(observer func(Link)) *linkScanner {
	l := newLinkScanner()
	if observer != nil {
		l.observer = observer
	}
	l.setShape = func(ebiten.CursorShapeType) {}
	l.setMeta(true)
	return &l
}

func TestLinkScannerOverlayCopiesOnlyLinkedRows(t *testing.T) {
	cells := linkGrid(
		"no link on this row",
		"see https://example.com",
		"still nothing to see here",
	)
	before := make([][]term.Cell, len(cells))
	for y := range cells {
		before[y] = append([]term.Cell(nil), cells[y]...)
	}

	overlay := heldScanner(nil).overlay(cells)

	for y := range cells {
		assert.Equal(t, before[y], cells[y], "source row %d must be untouched", y)
	}
	for _, y := range []int{0, 2} {
		assert.Same(t, &cells[y][0], &overlay[y][0],
			"link-free row %d must not be copied", y)
	}
	assert.NotSame(t, &cells[1][0], &overlay[1][0], "the linked row must be copied")

	for x := 4; x < 23; x++ {
		assert.NotZero(t, overlay[1][x].Attrs&term.AttrUnderline,
			"cell %d should be underlined", x)
	}
	assert.Zero(t, overlay[1][3].Attrs&term.AttrUnderline)
	assert.Zero(t, overlay[1][23].Attrs&term.AttrUnderline)
}

func TestLinkScannerOverlayReturnsSourceWhenFrameHasNoLink(t *testing.T) {
	cells := linkGrid("no link here")
	assert.Equal(t, cells, heldScanner(nil).overlay(cells))
}

func TestLinkScannerOverlayReusesScratchAcrossFrames(t *testing.T) {
	l := heldScanner(nil)

	first := l.overlay(linkGrid("https://a.com", "plain"))
	assert.NotZero(t, first[0][0].Attrs&term.AttrUnderline)

	l.invalidate()
	second := l.overlay(linkGrid("plain", "https://b.com"))
	assert.Zero(t, second[0][0].Attrs&term.AttrUnderline,
		"a row that lost its link must not keep a stale underline")
	assert.NotZero(t, second[1][0].Attrs&term.AttrUnderline)
}

func TestLinkScannerHandleMouse(t *testing.T) {
	cells := linkGrid("see https://example.com and file:///tmp/a")

	suite := []struct {
		description string
		meta        bool
		events      []term.Event
		consumed    []bool
		opened      []string
	}{
		{
			description: "meta click inside a span opens the link",
			meta:        true,
			events: []term.Event{
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 6},
				{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 6},
			},
			consumed: []bool{true, true},
			opened:   []string{"https://example.com"},
		},
		{
			description: "meta click on a file url opens the file",
			meta:        true,
			events: []term.Event{
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 30},
			},
			consumed: []bool{true},
			opened:   []string{"file:///tmp/a"},
		},
		{
			description: "meta click outside every span is forwarded",
			meta:        true,
			events: []term.Event{
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 1},
			},
			consumed: []bool{false},
		},
		{
			description: "click without meta is forwarded",
			events: []term.Event{
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 6},
				{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 6},
			},
			consumed: []bool{false, false},
		},
		{
			description: "meta drag over a span opens the link once",
			meta:        true,
			events: []term.Event{
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 6},
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 8},
				{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 8},
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 8},
			},
			consumed: []bool{true, true, true, true},
			opened:   []string{"https://example.com", "https://example.com"},
		},
		{
			description: "right click on a span is forwarded",
			meta:        true,
			events: []term.Event{
				{Type: term.EventMouse, Key: term.MouseRight, MouseX: 6},
			},
			consumed: []bool{false},
		},
	}

	for _, tc := range suite {
		t.Run(tc.description, func(t *testing.T) {
			var opened []string
			l := heldScanner(func(l Link) {
				opened = append(opened, l.URL.String())
			})
			l.setMeta(tc.meta)

			var consumed []bool
			for _, ev := range tc.events {
				consumed = append(consumed, l.handleMouse(ev, cells))
			}
			assert.Equal(t, tc.consumed, consumed)
			assert.Equal(t, tc.opened, opened)
		})
	}
}

func TestLinkScannerHandleMouseWithDefaultObserver(t *testing.T) {
	l := heldScanner(nil)
	cells := linkGrid("see https://example.com")
	assert.True(t, l.handleMouse(
		term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 6}, cells))
}

func TestLinkScannerHandleMouseOnUnparseableURL(t *testing.T) {
	var opened []Link
	l := heldScanner(func(l Link) { opened = append(opened, l) })
	cells := linkGrid("https://a.com/%zz")

	assert.True(t, l.handleMouse(
		term.Event{Type: term.EventMouse, Key: term.MouseLeft}, cells))
	assert.Empty(t, opened)
}

func TestLinkScannerOverlayIgnoresSpansPastTheFrame(t *testing.T) {
	l := heldScanner(nil)
	l.scan(linkGrid("plain", "plain", "https://example.com"))
	require.Len(t, l.spans, 1)

	shrunk := linkGrid("plain")
	assert.NotPanics(t, func() { l.overlay(shrunk) })
}

func TestLinkScannerClickOnWrappedURLOpensWholeAddress(t *testing.T) {
	cells := markWrapped(linkGrid("see https://rune.build?aaaa", "bbbb=cc"), 0)
	const whole = "https://rune.build?aaaabbbb=cc"

	for _, ev := range []term.Event{
		{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 6, MouseY: 0},
		{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 2, MouseY: 1},
	} {
		var opened []string
		l := heldScanner(func(l Link) { opened = append(opened, l.URL.String()) })
		require.True(t, l.handleMouse(ev, cells))
		assert.Equal(t, []string{whole}, opened)
	}
}

func TestLinkScannerUnderlinesEveryRowOfAWrappedURL(t *testing.T) {
	cells := markWrapped(linkGrid("see https://rune.build?aaaa", "bbbb=cc"), 0)
	overlay := heldScanner(nil).overlay(cells)

	for x := 4; x < 27; x++ {
		assert.NotZero(t, overlay[0][x].Attrs&term.AttrUnderline, "row 0 cell %d", x)
	}
	for x := range 7 {
		assert.NotZero(t, overlay[1][x].Attrs&term.AttrUnderline, "row 1 cell %d", x)
	}
	assert.Zero(t, overlay[1][7].Attrs&term.AttrUnderline, "past the wrapped tail")
}

func TestLinkScannerSetMetaReportsTransitions(t *testing.T) {
	l := newLinkScanner()
	assert.False(t, l.setMeta(false), "no transition")
	assert.False(t, l.armed())
	assert.True(t, l.setMeta(true))
	assert.True(t, l.armed())
	assert.False(t, l.setMeta(true))
	assert.True(t, l.setMeta(false))
	assert.False(t, l.armed())
}

func TestLinkScannerScanCachesUntilInvalidated(t *testing.T) {
	l := heldScanner(nil)
	l.scan(linkGrid("https://a.com"))
	require.Len(t, l.spans, 1)

	l.scan(linkGrid("https://b.com https://c.com"))
	assert.Equal(t, "https://a.com", l.spans[0].url, "cached spans are reused")

	l.invalidate()
	l.scan(linkGrid("https://b.com https://c.com"))
	assert.Len(t, l.spans, 2)
}

// recordingScanner is a meta-held scanner capturing every cursor shape
// it pushes to the window.
func recordingScanner() (*linkScanner, *[]ebiten.CursorShapeType) {
	var shapes []ebiten.CursorShapeType
	l := newLinkScanner()
	l.setShape = func(s ebiten.CursorShapeType) { shapes = append(shapes, s) }
	l.setMeta(true)
	return &l, &shapes
}

func TestLinkScannerPointAtOffersTheLinkCursor(t *testing.T) {
	l, shapes := recordingScanner()
	cells := linkGrid("see https://example.com")

	l.pointAt(term.Coordinates{X: 1}, cells)
	assert.Empty(t, *shapes, "prose offers no link cursor")

	l.pointAt(term.Coordinates{X: 6}, cells)
	l.pointAt(term.Coordinates{X: 8}, cells)
	assert.Equal(t, []ebiten.CursorShapeType{ebiten.CursorShapePointer}, *shapes,
		"moving within a span must not push the shape again")

	l.pointAt(term.Coordinates{X: 1}, cells)
	assert.Equal(t, []ebiten.CursorShapeType{
		ebiten.CursorShapePointer, ebiten.CursorShapeDefault,
	}, *shapes)
}

func TestLinkScannerReleasingMetaRestoresTheCursor(t *testing.T) {
	l, shapes := recordingScanner()
	l.pointAt(term.Coordinates{X: 0}, linkGrid("https://example.com"))
	require.Equal(t, []ebiten.CursorShapeType{ebiten.CursorShapePointer}, *shapes)

	l.setMeta(false)
	assert.Equal(t, []ebiten.CursorShapeType{
		ebiten.CursorShapePointer, ebiten.CursorShapeDefault,
	}, *shapes)

	l.setMeta(true)
	assert.Len(t, *shapes, 2, "arming alone does not move the cursor")
}

func TestGUIMetaHoverSetsLinkCursor(t *testing.T) {
	mock := mockHandler{
		assertDraw: func(w term.Writer) {
			writeRow(w, 0, "https://example.com")
		},
		assertEvent: func(term.Event) (bool, bool) { return false, true },
	}
	g, input := newTestGUI(t, &mock)
	var shapes []ebiten.CursorShapeType
	g.links.setShape = func(s ebiten.CursorShapeType) { shapes = append(shapes, s) }
	g.drawHandler(g.ctx)

	// The headless cursor rests at cell 0,0, inside the link.
	metaDown(input)
	require.NoError(t, g.Update())
	assert.Equal(t, []ebiten.CursorShapeType{ebiten.CursorShapePointer}, shapes)

	metaUp(input)
	require.NoError(t, g.Update())
	assert.Equal(t, []ebiten.CursorShapeType{
		ebiten.CursorShapePointer, ebiten.CursorShapeDefault,
	}, shapes)
}

func TestGUIMetaClickOpensLinkAndIsNotForwarded(t *testing.T) {
	var handled []term.Event
	mock := mockHandler{
		assertDraw: func(w term.Writer) {
			writeRow(w, 0, "see https://example.com")
		},
		assertEvent: func(ev term.Event) (bool, bool) {
			handled = append(handled, ev)
			return false, true
		},
	}
	g, input := newTestGUI(t, &mock)
	var opened []Link
	require.NoError(t, WithLinkObserver(func(l Link) {
		opened = append(opened, l)
	})(g))
	g.drawHandler(g.ctx)

	metaDown(input)
	require.NoError(t, g.Update())
	input.events = nil

	g.pendingEvents = append(g.pendingEvents,
		term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 6},
		term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 6})
	require.NoError(t, g.Update())

	require.Len(t, opened, 1)
	assert.Equal(t, "https://example.com", opened[0].URL.String())
	assert.Empty(t, handled, "the whole gesture is consumed")
}

func TestGUIClickWithoutMetaIsForwarded(t *testing.T) {
	var handled []term.Event
	mock := mockHandler{
		assertDraw: func(w term.Writer) {
			writeRow(w, 0, "see https://example.com")
		},
		assertEvent: func(ev term.Event) (bool, bool) {
			handled = append(handled, ev)
			return false, true
		},
	}
	g, _ := newTestGUI(t, &mock)
	var opened []Link
	require.NoError(t, WithLinkObserver(func(l Link) {
		opened = append(opened, l)
	})(g))
	g.drawHandler(g.ctx)

	g.pendingEvents = append(g.pendingEvents,
		term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 6})
	require.NoError(t, g.Update())

	assert.Empty(t, opened)
	assert.Len(t, handled, 1)
}

func TestInputTracksMetaFromKeyTransitions(t *testing.T) {
	mock, in := newTestInput(t)
	assert.False(t, in.metaHeld())

	mock.events = []ebiten.InputEvent{press(ebiten.KeyMetaLeft, ebiten.KeyModSuper)}
	in.processEvents(nil)
	assert.True(t, in.metaHeld())

	// A chord typed while meta is held keeps the modifier down.
	mock.events = shortcut(press(ebiten.KeyA, ebiten.KeyModSuper), 'a')
	in.processEvents(nil)
	assert.True(t, in.metaHeld())

	// A key action reported without Super resynchronizes a missed release.
	mock.events = action(press(ebiten.KeyB), 'b')
	in.processEvents(nil)
	assert.False(t, in.metaHeld())

	mock.events = []ebiten.InputEvent{press(ebiten.KeyMetaRight, ebiten.KeyModSuper)}
	in.processEvents(nil)
	assert.True(t, in.metaHeld())

	mock.events = []ebiten.InputEvent{release(ebiten.KeyMetaRight)}
	in.processEvents(nil)
	assert.False(t, in.metaHeld())
}

// benchGrid builds a rows x cols frame where every linkEvery-th row ends
// in a URL, approximating a terminal or editor pane full of prose.
func benchGrid(rows, cols, linkEvery int) [][]term.Cell {
	grid := make([][]term.Cell, rows)
	for y := range grid {
		text := strings.Repeat("lorem ipsum dolor ", 1+cols/18)
		if linkEvery > 0 && y%linkEvery == 0 {
			text = fmt.Sprintf("see https://example.com/%d for details %s", y, text)
		}
		row := cellRow(text)
		if len(row) > cols {
			row = row[:cols]
		}
		for len(row) < cols {
			row = append(row, term.Cell{})
		}
		grid[y] = row
	}
	return grid
}

// benchFrames are the frame shapes a full-screen Rune window produces.
var benchFrames = []struct {
	name string
	rows int
	cols int
}{
	{"80x24", 24, 80},
	{"200x60", 60, 200},
	{"400x120", 120, 400},
}

func BenchmarkRowScannerScan(b *testing.B) {
	for _, frame := range benchFrames {
		for _, density := range []struct {
			name  string
			every int
		}{
			{"nolinks", 0},
			{"sparse", 10},
			{"dense", 1},
		} {
			cells := benchGrid(frame.rows, frame.cols, density.every)
			b.Run(frame.name+"/"+density.name, func(b *testing.B) {
				var s rowScanner
				var spans []linkSpan
				b.ReportAllocs()
				for b.Loop() {
					spans = s.scan(cells, spans[:0])
				}
			})
		}
	}
}

func BenchmarkLinkScannerOverlay(b *testing.B) {
	for _, frame := range benchFrames {
		cells := benchGrid(frame.rows, frame.cols, 10)

		// A frame where the handler redrew, so the spans are rescanned.
		b.Run(frame.name+"/rescan", func(b *testing.B) {
			l := heldScanner(nil)
			b.ReportAllocs()
			for b.Loop() {
				l.invalidate()
				l.overlay(cells)
			}
		})

		// An idle frame while meta stays held, which is the common case.
		b.Run(frame.name+"/cached", func(b *testing.B) {
			l := heldScanner(nil)
			l.overlay(cells)
			b.ReportAllocs()
			for b.Loop() {
				l.overlay(cells)
			}
		})
	}
}

func BenchmarkLinkScannerHandleMouse(b *testing.B) {
	cells := benchGrid(60, 200, 10)
	hit := term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 6, MouseY: 50}
	release := term.Event{Type: term.EventMouse, Key: term.MouseRelease}
	miss := term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 2, MouseY: 51}

	b.Run("hit", func(b *testing.B) {
		l := heldScanner(func(Link) {})
		l.overlay(cells)
		b.ReportAllocs()
		for b.Loop() {
			l.handleMouse(hit, cells)
			l.handleMouse(release, cells)
		}
	})

	b.Run("miss", func(b *testing.B) {
		l := heldScanner(func(Link) {})
		l.overlay(cells)
		b.ReportAllocs()
		for b.Loop() {
			l.handleMouse(miss, cells)
		}
	})
}

// pane renders rows into a frame wider than they are, the way the
// composited screen holds a terminal beside a gutter and an icon bar.
// Nothing marks where the pane ends: the cells around it are simply
// blank.
func pane(width, left int, rows ...string) [][]term.Cell {
	grid := make([][]term.Cell, len(rows))
	for y, text := range rows {
		grid[y] = make([]term.Cell, width)
		copy(grid[y][left:], cellRow(text))
	}
	return grid
}

func TestRowScannerJoinsWrappedURLInsideAPane(t *testing.T) {
	const width, left, paneWidth = 60, 8, 34
	head := "https://rune.build?"
	first := head + strings.Repeat("a", paneWidth-len(head))
	cells := markWrapped(pane(width, left, first, "bbbb=cc"), 0)
	whole := first + "bbbb=cc"

	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: left, x1: left + paneWidth, url: whole},
		{y: 1, x0: left, x1: left + 7, url: whole},
	}, got)
}

func TestRowScannerJoinsWrappedURLWithShortContinuationRow(t *testing.T) {
	const width, left = 40, 8
	head := markWrapped(pane(width, left, "https://rune.build?aa"), 0)[0]
	cells := [][]term.Cell{head, cellRow("xy")}

	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: left, x1: left + 21, url: "https://rune.build?aa"},
	}, got)
}

func TestRowScannerJoinsWrappedURLWithLaterCandidateOnTheSameRow(t *testing.T) {
	cells := markWrapped(pane(60, 0, "https://rune.build/aa", "bb"), 0)
	copy(cells[0][30:], cellRow("http://"))

	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: 0, x1: 21, url: "https://rune.build/aabb"},
		{y: 1, x0: 0, x1: 2, url: "https://rune.build/aabb"},
	}, got)
}

func TestRowScannerJoinsWrappedURLBreakingOnPunctuation(t *testing.T) {
	const width = 40
	head := "xx api_url=https://rune-prod.us."
	cells := pane(width, 0, head, "auth0.com/api/v2/ done")
	cells[0][len(head)-1].Bytes = cell.WrapMarker

	whole := "https://rune-prod.us.auth0.com/api/v2/"
	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: 11, x1: len(head), url: whole},
		{y: 1, x0: 0, x1: 17, url: whole},
	}, got)
}

func TestRowScannerJoinsWrappedURLBreakingOnPunctuationRepeatedly(t *testing.T) {
	cells := markWrapped(pane(30, 0,
		"https://rune.build/a,",
		"b.c(d)e,f.g/h?i=j,k.",
		"l.m/n",
	), 0, 1)

	whole := "https://rune.build/a,b.c(d)e,f.g/h?i=j,k.l.m/n"
	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: 0, x1: 21, url: whole},
		{y: 1, x0: 0, x1: 20, url: whole},
		{y: 2, x0: 0, x1: 5, url: whole},
	}, got)
}

func TestRowScannerTrimsWrappedURLOnlyAtItsEnd(t *testing.T) {
	cells := markWrapped(pane(30, 0, "https://rune.build/a.", "b/c."), 0)

	whole := "https://rune.build/a.b/c"
	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: 0, x1: 21, url: whole},
		{y: 1, x0: 0, x1: 3, url: whole},
	}, got)
}

func TestRowScannerJoinsWrappedURLWithHostOnTheContinuation(t *testing.T) {
	cells := markWrapped(pane(30, 0, "https://", "rune.build/a"), 0)

	whole := "https://rune.build/a"
	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: 0, x1: 8, url: whole},
		{y: 1, x0: 0, x1: 12, url: whole},
	}, got)
}

// framed renders rows into a pane the window drew chrome around: a
// border to its left and a scroll bar hard against its right margin,
// which is where a wrapped line ends.
func framed(width, left, paneWidth int, rows ...string) [][]term.Cell {
	grid := pane(width, left, rows...)
	for y := range grid {
		grid[y][left-1].Ch = '│'
		grid[y][left+paneWidth].Ch = '█'
	}
	return grid
}

func TestRowScannerJoinsWrappedURLInsideAFramedPane(t *testing.T) {
	const width, left, paneWidth = 30, 2, 21
	cells := framed(width, left, paneWidth, "https://rune.build/aa", "bb")
	cells[0][left+paneWidth-1].Bytes = cell.WrapMarker

	whole := "https://rune.build/aabb"
	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: left, x1: left + paneWidth, url: whole},
		{y: 1, x0: left, x1: left + 2, url: whole},
	}, got)
}

func TestRowScannerStopsAtTheChromeBesideAPane(t *testing.T) {
	const width, left, paneWidth = 30, 2, 21
	cells := framed(width, left, paneWidth, "https://rune.build/aa")

	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: left, x1: left + paneWidth, url: "https://rune.build/aa"},
	}, got)
}

func TestRowScannerStopsAtTheMarkerBesideAnotherPane(t *testing.T) {
	const width, paneWidth = 40, 21
	cells := pane(width, 0, "https://rune.build/aa", "bb")
	copy(cells[0][paneWidth:], cellRow("neighbour/text"))
	cells[0][paneWidth-1].Bytes = cell.WrapMarker

	whole := "https://rune.build/aabb"
	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: 0, x1: paneWidth, url: whole},
		{y: 1, x0: 0, x1: 2, url: whole},
	}, got)
}

func TestRowScannerJoinsWrappedURLAcrossFourRows(t *testing.T) {
	const width, margin = 110, 102
	rows := []string{
		"https://auth.rune.build/authorize?access_type=offline&client_id=XHBpJIm3q6PYazpxZMAhcwxAuR5Ks9B7&code_c",
		"hallenge=82mRxm5ftn_YTsr2kc0-imEjqLl9Q07b-gpFpb3z2QI&code_challenge_method=S256&redirect_uri=http%3A%2F",
		"%2F127.0.0.1%3A11524%2Fo%2Foauth2%2Fredirect&response_type=code&scope=offline_access+openid&state=435f9",
		"295-a571-4f50-bf70-7b00cbe5e44c",
	}
	cells := pane(width, 0, rows...)
	for _, y := range []int{0, 1, 2} {
		cells[y][margin].Bytes = cell.WrapMarker
	}

	whole := strings.Join(rows, "")
	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: 0, x1: margin + 1, url: whole},
		{y: 1, x0: 0, x1: margin + 1, url: whole},
		{y: 2, x0: 0, x1: margin + 1, url: whole},
		{y: 3, x0: 0, x1: len(rows[3]), url: whole},
	}, got)
}

// benchWrappedGrid builds a frame whose links overflow a pane and carry
// on across rows, so the join path is measured instead of skipped. Rows
// come in groups of three: a head and a middle that both overflow, then
// a tail that ends the address.
func benchWrappedGrid(rows, cols, left, paneWidth int) [][]term.Cell {
	const head = "https://example.com/"
	grid := make([][]term.Cell, rows)
	for y := range grid {
		grid[y] = make([]term.Cell, cols)
		text := strings.Repeat("b", paneWidth)
		if y%3 == 0 {
			text = head + strings.Repeat("a", paneWidth-len(head))
		}
		if y%3 == 2 {
			text = strings.Repeat("c", paneWidth/2)
		}
		copy(grid[y][left:], cellRow(text))
		if y%3 != 2 {
			markWrapped(grid, y)
		}
	}
	return grid
}

func BenchmarkRowScannerScanWrapped(b *testing.B) {
	for _, frame := range benchFrames {
		left := frame.cols / 8
		cells := benchWrappedGrid(frame.rows, frame.cols, left, frame.cols-2*left)
		b.Run(frame.name, func(b *testing.B) {
			var s rowScanner
			var spans []linkSpan
			b.ReportAllocs()
			for b.Loop() {
				spans = s.scan(cells, spans[:0])
			}
		})
	}
}

func TestRowScannerJoinsPathWrappedAcrossRows(t *testing.T) {
	suite := []struct {
		description string
		rows        []string
		wrapped     []int
		expected    []linkSpan
	}{
		{
			description: "position on the continuation row",
			rows: []string{
				"error: internal/term/g",
				"ui/link.go:12:3: oops",
			},
			wrapped: []int{0},
			expected: []linkSpan{
				{
					y: 0, x0: 7, x1: 22,
					path: "internal/term/gui/link.go", line: 12, col: 3,
				},
				{
					y: 1, x0: 0, x1: 15,
					path: "internal/term/gui/link.go", line: 12, col: 3,
				},
			},
		},
		{
			description: "path across three rows",
			rows: []string{
				"internal/term/",
				"gui/commands/c",
				"ursor.go:7",
			},
			wrapped: []int{0, 1},
			expected: []linkSpan{
				{y: 0, x0: 0, x1: 14, path: "internal/term/gui/commands/cursor.go", line: 7},
				{y: 1, x0: 0, x1: 14, path: "internal/term/gui/commands/cursor.go", line: 7},
				{y: 2, x0: 0, x1: 10, path: "internal/term/gui/commands/cursor.go", line: 7},
			},
		},
		{
			description: "a row that did not wrap is not joined",
			rows: []string{
				"internal/term/g",
				"ui/link.go:12:3",
			},
			expected: []linkSpan{
				{y: 1, x0: 0, x1: 15, path: "ui/link.go", line: 12, col: 3},
			},
		},
		{
			description: "a wrapped row ending in a space is not joined",
			rows: []string{
				"see this ",
				"a/b.go:2",
			},
			wrapped: []int{0},
			expected: []linkSpan{
				{y: 1, x0: 0, x1: 8, path: "a/b.go", line: 2},
			},
		},
	}

	for _, tc := range suite {
		t.Run(tc.description, func(t *testing.T) {
			var s rowScanner
			cells := markWrapped(linkGrid(tc.rows...), tc.wrapped...)
			assert.Equal(t, tc.expected, s.scan(cells, nil))
		})
	}
}

func TestRowScannerJoinsWrappedPathInsideAPane(t *testing.T) {
	const width, left = 40, 8
	cells := markWrapped(pane(width, left, "see internal/te", "rm/a.go:9:1"), 0)

	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: left + 4, x1: left + 15, path: "internal/term/a.go", line: 9, col: 1},
		{y: 1, x0: left, x1: left + 11, path: "internal/term/a.go", line: 9, col: 1},
	}, got)
}

func TestRowScannerKeepsRowOrderWhenAPathJoinsAHead(t *testing.T) {
	cells := markWrapped(linkGrid(
		"https://a.com internal/te",
		"rm/a.go:9 and https://b.com",
	), 0)

	got := new(rowScanner).scan(cells, nil)
	ys := make([]int, len(got))
	for i, span := range got {
		ys[i] = span.y
	}
	assert.True(t, slices.IsSorted(ys), "spans out of row order: %v", ys)
	assert.Len(t, got, 4)
}

// leftPaneRow builds a row of a window split in two panes: left is drawn
// in the first leftWidth cells and right from the cell rightAt, with the
// cells between them blank. The left pane's text is marked as wrapped when
// wrapped is set.
func leftPaneRow(left string, leftWidth int, right string, rightAt int, wrapped bool) []term.Cell {
	row := make([]term.Cell, rightAt+len(right))
	copy(row, cellRow(left))
	copy(row[rightAt:], cellRow(right))
	if wrapped {
		row[leftWidth-1].Bytes = cell.WrapMarker
	}
	return row
}

func TestRowScannerKeepsRowOrderWhenAPathJoinsSeveralHeads(t *testing.T) {
	const leftWidth, rightAt = 12, 20
	cells := [][]term.Cell{
		leftPaneRow("x.go:1 a/bcd", leftWidth, "https://r0.io", rightAt, true),
		leftPaneRow("efgh/ijklmno", leftWidth, "https://r1.io", rightAt, true),
		leftPaneRow("pq.go:5", leftWidth, "", rightAt, false),
	}

	got := new(rowScanner).scan(cells, nil)

	ys := make([]int, len(got))
	for i, span := range got {
		ys[i] = span.y
	}
	assert.True(t, slices.IsSorted(ys), "spans out of row order: %v", ys)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: 0, x1: 6, path: "x.go", line: 1},
		{y: 0, x0: rightAt, x1: rightAt + 13, url: "https://r0.io"},
		{y: 0, x0: 7, x1: 12, path: "a/bcdefgh/ijklmnopq.go", line: 5},
		{y: 1, x0: rightAt, x1: rightAt + 13, url: "https://r1.io"},
		{y: 1, x0: 0, x1: 12, path: "a/bcdefgh/ijklmnopq.go", line: 5},
		{y: 2, x0: 0, x1: 7, path: "a/bcdefgh/ijklmnopq.go", line: 5},
	}, got)
}

func TestLinkScannerOverlayKeepsEveryUnderlineWhenAPathJoinsSeveralHeads(t *testing.T) {
	const leftWidth, rightAt = 12, 20
	cells := [][]term.Cell{
		leftPaneRow("x.go:1 a/bcd", leftWidth, "https://r0.io", rightAt, true),
		leftPaneRow("efgh/ijklmno", leftWidth, "https://r1.io", rightAt, true),
		leftPaneRow("pq.go:5", leftWidth, "", rightAt, false),
	}
	l := heldScanner(nil)

	got := l.overlay(cells)

	underlined := func(y, x int) bool { return got[y][x].Attrs&term.AttrUnderline != 0 }
	for _, at := range [][2]int{
		{0, 0}, {0, 5}, // the path that ends the row's first pane
		{0, 7}, {0, 11}, // the head of the wrapped path
		{0, rightAt}, {0, rightAt + 12}, // the url in the other pane
		{1, 0}, {1, 11},
		{1, rightAt}, {1, rightAt + 12},
		{2, 0}, {2, 6},
	} {
		assert.True(t, underlined(at[0], at[1]), "row %d, column %d", at[0], at[1])
	}
	assert.False(t, underlined(0, 6), "the space between the two paths")
}

func TestRowScannerDoesNotJoinAHeadOntoASpanOnItsRow(t *testing.T) {
	// "a/b.go:12" ends the wrapped row with a position of its own, and
	// "3.go:5" is the next line. Joining the "12" as the head of "3.go"
	// would cover cells the first span already covers.
	cells := markWrapped(linkGrid("a/b.go:12", "3.go:5"), 0)

	got := new(rowScanner).scan(cells, nil)

	assert.Equal(t, []linkSpan{
		{y: 0, x0: 0, x1: 9, path: "a/b.go", line: 12},
		{y: 1, x0: 0, x1: 6, path: "3.go", line: 5},
	}, got)
}

func TestRowScannerLetsAWrappedURLTakeAPathLikeContinuation(t *testing.T) {
	cells := markWrapped(linkGrid("https://rune.build/a", "b.go:9"), 0)

	got := new(rowScanner).scan(cells, nil)
	assert.Equal(t, []linkSpan{
		{y: 0, x0: 0, x1: 20, url: "https://rune.build/ab.go:9"},
		{y: 1, x0: 0, x1: 6, url: "https://rune.build/ab.go:9"},
	}, got, "the url takes the continuation, as before")
}

func TestLinkScannerHandleMouseReportsPosition(t *testing.T) {
	cells := linkGrid(
		"a/b.go:12:3: boom",
		"file:///x/y.go:7:2",
		"https://e.com and c/d.go:4",
	)

	suite := []struct {
		description string
		x, y        int
		expected    Link
	}{
		{
			description: "bare path",
			x:           2, y: 0,
			expected: Link{Path: "a/b.go", Line: 12, Col: 3},
		},
		{
			description: "file url",
			x:           12, y: 1,
			expected: Link{URL: mustParse(t, "file:///x/y.go"), Line: 7, Col: 2},
		},
		{
			description: "url on a row that also holds a path",
			x:           3, y: 2,
			expected: Link{URL: mustParse(t, "https://e.com")},
		},
		{
			description: "path with a line only",
			x:           24, y: 2,
			expected: Link{Path: "c/d.go", Line: 4},
		},
	}

	for _, tc := range suite {
		t.Run(tc.description, func(t *testing.T) {
			var opened []Link
			l := heldScanner(func(link Link) { opened = append(opened, link) })

			ev := term.Event{
				Type: term.EventMouse, Key: term.MouseLeft,
				MouseX: tc.x, MouseY: tc.y,
			}
			assert.True(t, l.handleMouse(ev, cells))
			assert.Equal(t, []Link{tc.expected}, opened)
		})
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func TestLinkScannerOverlayUnderlinesAPathWithItsPosition(t *testing.T) {
	l := heldScanner(nil)
	cells := linkGrid("see a/b.go:12:3: boom")

	got := l.overlay(cells)
	for x, c := range got[0] {
		underlined := c.Attrs&term.AttrUnderline != 0
		assert.Equal(t, x >= 4 && x < 15, underlined, "cell %d", x)
	}
}

func TestGUIMetaClickOnAPathReportsItsPosition(t *testing.T) {
	g, input, opened := linkTestGUI(t, "src/a.go:12:3: undefined: x")

	metaDown(input)
	require.NoError(t, g.Update())
	input.events = nil

	g.pendingEvents = append(g.pendingEvents,
		term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 3},
		term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 3})
	require.NoError(t, g.Update())

	assert.Equal(t, []Link{{Path: "src/a.go", Line: 12, Col: 3}}, *opened)
}

func TestParsePosition(t *testing.T) {
	suite := []struct {
		text      string
		line, col int
		end       int
		ok        bool
	}{
		{text: "12", line: 12, end: 2, ok: true},
		{text: "12:3", line: 12, col: 3, end: 4, ok: true},
		{text: "12:", line: 12, end: 2, ok: true},
		{text: "12: msg", line: 12, end: 2, ok: true},
		{text: "12:3:", line: 12, col: 3, end: 4, ok: true},
		{text: "12)", line: 12, end: 2, ok: true},
		{text: "12-14", line: 12, end: 2, ok: true},
		{text: "0:0", line: 0, col: 0, end: 3, ok: true},
		{text: "12x"},
		{text: "12:3x"},
		{text: "12_"},
		{text: "x12"},
		{text: ""},
		{text: "1234567890"},
		{text: "12:1234567890", line: 12, end: 2, ok: true},
	}

	for _, tc := range suite {
		t.Run(tc.text, func(t *testing.T) {
			line, col, end, ok := parsePosition(cellRow(tc.text), 0)
			assert.Equal(t, tc.ok, ok)
			if !ok {
				return
			}
			assert.Equal(t, [3]int{tc.line, tc.col, tc.end}, [3]int{line, col, end})
		})
	}
}

func TestIsFilePath(t *testing.T) {
	suite := []struct {
		path string
		want bool
	}{
		{"a/b.go", true},
		{"./b", true},
		{"/etc", true},
		{`C:\x`, true},
		{"main.go", true},
		{"foo.pb.go", true},
		{"README.MD", true},
		{"example.com/x", true},
		{"example.com", false},
		{"api.github.com", false},
		{"localhost", false},
		{"a.verylongextension", false},
		{"note", false},
		{"12", false},
		{"", false},
		{"1.5", false},
		{"e.g.", false},
		{".bashrc", false},
		{"v1.2.3", false},
		{"a.b-c", false},
		{"http://h/x.go", false},
		{"ftp://h", false},
	}

	for _, tc := range suite {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, isFilePath([]byte(tc.path)))
		})
	}
}

func TestSplitPosition(t *testing.T) {
	suite := []struct {
		text      string
		address   string
		line, col int
	}{
		{text: "file:///x/y.go:7:2", address: "file:///x/y.go", line: 7, col: 2},
		{text: "file:///x/y.go:7", address: "file:///x/y.go", line: 7},
		{text: "file:///x/y.go", address: "file:///x/y.go"},
		{text: "file:///x/y.go:a", address: "file:///x/y.go:a"},
		{text: "file:///x:1:2:3", address: "file:///x:1", line: 2, col: 3},
		{text: "file://", address: "file://"},
		{text: "file://:7", address: "file://", line: 7},
		{text: "http://h:8080", address: "http://h:8080"},
		{text: "https://e.com/a.go:1:2", address: "https://e.com/a.go:1:2"},
		{text: "file:///x:1234567890", address: "file:///x:1234567890"},
	}

	for _, tc := range suite {
		t.Run(tc.text, func(t *testing.T) {
			address, line, col := splitPosition([]byte(tc.text))
			assert.Equal(t, tc.address, string(address))
			assert.Equal(t, [2]int{tc.line, tc.col}, [2]int{line, col})
		})
	}
}
