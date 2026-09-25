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

package markdown

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestHeaderBlockHeight(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		level    int
		content  textRun
		width    int
		expected int
	}{
		{
			name:     "simple header",
			level:    1,
			content:  textRun{{text: "Hello"}},
			width:    20,
			expected: 3, // 1 (above) + 1 (content) + 1 (spacing) = 3
		},
		{
			name:     "header wraps",
			level:    1,
			content:  textRun{{text: "Hello World"}},
			width:    8, // "# " takes 2 chars, leaving 6 for content
			expected: 4, // 1 (above) + 2 (wrapped lines) + 1 (spacing) = 4
		},
		{
			name:     "zero width",
			level:    1,
			content:  textRun{{text: "Hello"}},
			width:    0,
			expected: 0,
		},
		{
			name:     "h2 header",
			level:    2,
			content:  textRun{{text: "Title"}},
			width:    20,
			expected: 3, // 1 (above) + 1 (content) + 1 (spacing) = 3
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newHeaderBlock(tt.level, tt.content, &cfg)
			assert.Equal(t, tt.expected, block.Height(tt.width))
		})
	}
}

func TestParagraphBlockHeight(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		content  textRun
		width    int
		expected int
	}{
		{
			name:     "simple paragraph",
			content:  textRun{{text: "Hello"}},
			width:    20,
			expected: 2,
		},
		{
			name:     "paragraph wraps",
			content:  textRun{{text: "Hello World Test"}},
			width:    5,
			expected: 4,
		},
		{
			name:     "zero width",
			content:  textRun{{text: "Hello"}},
			width:    0,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newParagraphBlock(tt.content, &cfg)
			assert.Equal(t, tt.expected, block.Height(tt.width))
		})
	}
}

func TestCodeBlockHeight(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		code     string
		width    int
		expected int
	}{
		{
			name:     "single line",
			code:     "code",
			width:    20,
			expected: 2,
		},
		{
			name:     "multiple lines",
			code:     "line1\nline2\nline3",
			width:    20,
			expected: 4,
		},
		{
			name:     "trailing newline",
			code:     "code\n",
			width:    20,
			expected: 2,
		},
		{
			name:     "zero width",
			code:     "code",
			width:    0,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newCodeBlock("", tt.code, &cfg)
			assert.Equal(t, tt.expected, block.Height(tt.width))
		})
	}
}

func TestListBlockHeight(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		items    []listItem
		width    int
		expected int
	}{
		{
			name: "simple list",
			items: []listItem{
				{content: textRun{{text: "Item 1"}}},
				{content: textRun{{text: "Item 2"}}},
			},
			width:    20,
			expected: 3, // 2 items + 1 spacing at top level
		},
		{
			name: "nested list",
			items: []listItem{
				{
					content: textRun{{text: "Item 1"}},
					nested: newListBlock(false, 1, []listItem{
						{content: textRun{{text: "Nested"}}},
					}, &cfg),
				},
			},
			width:    20,
			expected: 3, // 1 parent item + 1 nested item + 1 spacing (only top level adds spacing)
		},
		{
			name:     "zero width",
			items:    []listItem{{content: textRun{{text: "Item"}}}},
			width:    0,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newListBlock(false, 1, tt.items, &cfg)
			assert.Equal(t, tt.expected, block.Height(tt.width))
		})
	}
}

func TestBlockquoteBlockHeight(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		content  []textRun
		nested   *blockquoteBlock
		width    int
		expected int
	}{
		{
			name:     "simple blockquote",
			content:  []textRun{{{text: "Quote"}}},
			width:    20,
			expected: 2,
		},
		{
			name:    "nested blockquote",
			content: []textRun{{{text: "Outer"}}},
			nested: newBlockquoteBlock(
				[]textRun{{{text: "Inner"}}},
				nil,
				&cfg,
			),
			width:    20,
			expected: 4,
		},
		{
			name:     "zero width",
			content:  []textRun{{{text: "Quote"}}},
			width:    0,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newBlockquoteBlock(tt.content, tt.nested, &cfg)
			assert.Equal(t, tt.expected, block.Height(tt.width))
		})
	}
}

func TestHorizontalRuleBlockHeight(t *testing.T) {
	cfg := DefaultConfig()
	block := newHorizontalRuleBlock(&cfg)

	assert.Equal(t, 2, block.Height(20))
	assert.Equal(t, 0, block.Height(0))
}

func TestTableBlockHeight(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		header   []textRun
		rows     [][]textRun
		width    int
		expected int
	}{
		{
			name:     "simple table",
			header:   []textRun{{{text: "A"}}, {{text: "B"}}},
			rows:     [][]textRun{{{{text: "1"}}, {{text: "2"}}}},
			width:    20,
			expected: 6, // top + header + sep + 1 row + bottom + spacing
		},
		{
			name:     "empty header",
			header:   nil,
			rows:     [][]textRun{{{{text: "1"}}, {{text: "2"}}}},
			width:    20,
			expected: 0,
		},
		{
			name:     "zero width",
			header:   []textRun{{{text: "A"}}},
			rows:     nil,
			width:    0,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newTableBlock(tt.header, tt.rows, nil, &cfg)
			assert.Equal(t, tt.expected, block.Height(tt.width))
		})
	}
}

func TestTableDrawWideClusters(t *testing.T) {
	cfg := DefaultConfig()
	const tableWidth = 8 // 1 column of 6 plus both separators

	tests := []struct {
		name      string
		align     tableAlignment
		cell      string
		wantX     int
		wantCh    rune
		wantWidth uint8
	}{
		{
			name:      "left aligned emoji",
			align:     alignLeft,
			cell:      "🚀",
			wantX:     1,
			wantCh:    '🚀',
			wantWidth: 2,
		},
		{
			name:      "center aligned emoji",
			align:     alignCenter,
			cell:      "🚀",
			wantX:     3,
			wantCh:    '🚀',
			wantWidth: 2,
		},
		{
			name:      "right aligned emoji",
			align:     alignRight,
			cell:      "🚀",
			wantX:     5,
			wantCh:    '🚀',
			wantWidth: 2,
		},
		{
			name:      "right aligned cjk",
			align:     alignRight,
			cell:      "日本",
			wantX:     3,
			wantCh:    '日',
			wantWidth: 2,
		},
		{
			name:      "left aligned combining cluster",
			align:     alignLeft,
			cell:      "e\u0301",
			wantX:     1,
			wantCh:    'e',
			wantWidth: 1,
		},
		{
			name:      "center aligned flag",
			align:     alignCenter,
			cell:      "🇺🇸",
			wantX:     3,
			wantCh:    '🇺',
			wantWidth: 2,
		},
		{
			name:      "right aligned nerd icon",
			align:     alignRight,
			cell:      "󰗠",
			wantX:     5,
			wantCh:    '󰗠',
			wantWidth: 2,
		},
		{
			name:      "right aligned fullwidth latin",
			align:     alignRight,
			cell:      "ｗ",
			wantX:     5,
			wantCh:    'ｗ',
			wantWidth: 2,
		},
		{
			name:      "center aligned skin tone emoji",
			align:     alignCenter,
			cell:      "👍🏽",
			wantX:     3,
			wantCh:    '👍',
			wantWidth: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newTableBlock(
				[]textRun{{{text: "H"}}},
				[][]textRun{{{{text: tt.cell}}}},
				[]tableAlignment{tt.align},
				&cfg,
			)
			block.Resize(tableWidth, block.Height(tableWidth))

			w := term.NewStringWriter(tableWidth, 6)
			require.NoError(t, w.Clear(term.Attributes{}))
			block.Draw(w)

			const dataRow = 3
			cells := w.Cells()[dataRow*tableWidth:]
			assert.Equal(t, tt.wantCh, cells[tt.wantX].Ch)
			assert.Equal(t, tt.wantWidth, cells[tt.wantX].Width)
			assert.Equal(t, rune(0), cells[tt.wantX+1].Ch,
				"continuation column must stay empty")
			assert.Equal(t, cfg.TableCharSet.ColumnSeparator,
				cells[tableWidth-1].Ch, "right separator overwritten")
		})
	}
}

func TestTableDrawWideHeaderAndMultiColumn(t *testing.T) {
	cfg := DefaultConfig()
	// 2 columns: width 11 leaves 8 columns of content, 4 per column.
	// Layout: │....│....│ with column content at x 1-4 and 6-9.
	const tableWidth = 11
	block := newTableBlock(
		[]textRun{{{text: "🚀"}}, {{text: "B"}}},
		[][]textRun{{{{text: "x"}}, {{text: "中"}}}},
		nil,
		&cfg,
	)
	block.Resize(tableWidth, block.Height(tableWidth))

	w := term.NewStringWriter(tableWidth, 6)
	require.NoError(t, w.Clear(term.Attributes{}))
	block.Draw(w)

	cells := w.Cells()
	const headerRow, dataRow = 1, 3

	header := cells[headerRow*tableWidth:]
	assert.Equal(t, '🚀', header[1].Ch)
	assert.Equal(t, uint8(2), header[1].Width)
	assert.Equal(t, rune(0), header[2].Ch, "continuation column")
	assert.NotZero(t, header[1].Attrs&term.AttrBold, "header must be bold")
	assert.Equal(t, 'B', header[6].Ch)
	assert.Equal(t, cfg.TableCharSet.ColumnSeparator, header[0].Ch)
	assert.Equal(t, cfg.TableCharSet.ColumnSeparator, header[5].Ch)
	assert.Equal(t, cfg.TableCharSet.ColumnSeparator, header[10].Ch)

	data := cells[dataRow*tableWidth:]
	assert.Equal(t, 'x', data[1].Ch)
	assert.Equal(t, '中', data[6].Ch)
	assert.Equal(t, uint8(2), data[6].Width)
	assert.Equal(t, rune(0), data[7].Ch, "continuation column")
	assert.Equal(t, cfg.TableCharSet.ColumnSeparator, data[10].Ch)
}

func TestTableDrawCellExactFill(t *testing.T) {
	cfg := DefaultConfig()
	const tableWidth = 8 // one column, 6 columns of content
	block := newTableBlock(
		[]textRun{{{text: "H"}}},
		[][]textRun{{{{text: "中文字"}}}},
		nil,
		&cfg,
	)
	block.Resize(tableWidth, block.Height(tableWidth))

	w := term.NewStringWriter(tableWidth, 6)
	require.NoError(t, w.Clear(term.Attributes{}))
	block.Draw(w)

	const dataRow = 3
	cells := w.Cells()[dataRow*tableWidth:]
	assert.Equal(t, '中', cells[1].Ch)
	assert.Equal(t, '文', cells[3].Ch)
	assert.Equal(t, '字', cells[5].Ch)
	assert.Equal(t, cfg.TableCharSet.ColumnSeparator, cells[7].Ch,
		"separator survives an exactly-filled cell")
}

func TestTableDrawTruncatesAtClusterBoundary(t *testing.T) {
	cfg := DefaultConfig()
	const tableWidth = 3 // a single 1-column-wide cell

	block := newTableBlock(
		[]textRun{{{text: "H"}}},
		[][]textRun{{{{text: "🚀"}}}},
		nil,
		&cfg,
	)
	block.Resize(tableWidth, block.Height(tableWidth))

	w := term.NewStringWriter(tableWidth, 6)
	require.NoError(t, w.Clear(term.Attributes{}))
	block.Draw(w)

	const dataRow = 3
	cells := w.Cells()[dataRow*tableWidth:]
	assert.Equal(t, rune(0), cells[1].Ch,
		"a cluster wider than the column must not be drawn")
	assert.Equal(t, cfg.TableCharSet.ColumnSeparator, cells[0].Ch)
	assert.Equal(t, cfg.TableCharSet.ColumnSeparator, cells[2].Ch)
}

func TestBlockDraw(t *testing.T) {
	cfg := DefaultConfig()
	w := term.NewStringWriter(20, 10)

	tests := []struct {
		name         string
		block        block
		expectedRows int
	}{
		{
			name:         "header",
			block:        newHeaderBlock(1, textRun{{text: "Title"}}, &cfg),
			expectedRows: 3, // 1 (above) + 1 (content) + 1 (spacing)
		},
		{
			name:         "paragraph",
			block:        newParagraphBlock(textRun{{text: "Text"}}, &cfg),
			expectedRows: 2,
		},
		{
			name:         "code",
			block:        newCodeBlock("", "code", &cfg),
			expectedRows: 2,
		},
		{
			name:         "hrule",
			block:        newHorizontalRuleBlock(&cfg),
			expectedRows: 2,
		},
		{
			name:         "list",
			block:        newListBlock(false, 1, []listItem{{content: textRun{{text: "Item"}}}}, &cfg),
			expectedRows: 2,
		},
		{
			name:         "blockquote",
			block:        newBlockquoteBlock([]textRun{{{text: "Quote"}}}, nil, &cfg),
			expectedRows: 2,
		},
		{
			name: "table",
			block: newTableBlock(
				[]textRun{{{text: "A"}}},
				[][]textRun{{{{text: "1"}}}},
				nil,
				&cfg,
			),
			expectedRows: 6, // top + header + sep + 1 row + bottom + spacing
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, w.Clear(term.Attributes{}))
			h := tt.block.Height(20)
			tt.block.Resize(20, h)
			tt.block.Draw(w)
			assert.Equal(t, tt.expectedRows, h)
		})
	}
}

func TestTextRunString(t *testing.T) {
	tests := []struct {
		name     string
		run      textRun
		expected string
	}{
		{
			name:     "empty",
			run:      textRun{},
			expected: "",
		},
		{
			name:     "single span",
			run:      textRun{{text: "hello"}},
			expected: "hello",
		},
		{
			name:     "multiple spans",
			run:      textRun{{text: "hello "}, {text: "world"}},
			expected: "hello world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.run.String())
		})
	}
}

func TestTextRunWidth(t *testing.T) {
	tests := []struct {
		name     string
		run      textRun
		expected int
	}{
		{
			name:     "empty",
			run:      textRun{},
			expected: 0,
		},
		{
			name:     "single span",
			run:      textRun{{text: "hello"}},
			expected: 5,
		},
		{
			name:     "multiple spans",
			run:      textRun{{text: "hello"}, {text: "world"}},
			expected: 10,
		},
		{
			name:     "accented rune counts as one column",
			run:      textRun{{text: "héllo"}},
			expected: 5,
		},
		{
			name:     "combining mark after ascii merges into one column",
			run:      textRun{{text: "e\u0301e\u0301"}},
			expected: 2,
		},
		{
			name:     "emoji counts as two columns",
			run:      textRun{{text: "🚀"}},
			expected: 2,
		},
		{
			name:     "cjk counts as two columns each",
			run:      textRun{{text: "日本"}},
			expected: 4,
		},
		{
			name:     "zwj family is a single wide cluster",
			run:      textRun{{text: "👨‍👩‍👧"}},
			expected: 2,
		},
		{
			name:     "nul byte counts as one column",
			run:      textRun{{text: "a\x00b"}},
			expected: 3,
		},
		{
			name:     "tab counts as one column",
			run:      textRun{{text: "a\tb"}},
			expected: 3,
		},
		{
			name:     "bell control counts as one column",
			run:      textRun{{text: "\a"}},
			expected: 1,
		},
		{
			name:     "lone combining mark counts as one column",
			run:      textRun{{text: "\u0301"}},
			expected: 1,
		},
		{
			name:     "lone zwj counts as one column",
			run:      textRun{{text: "\u200d"}},
			expected: 1,
		},
		{
			name:     "zero width space counts as one column",
			run:      textRun{{text: "a\u200bb"}},
			expected: 3,
		},
		{
			name:     "zwj attaches to preceding ascii",
			run:      textRun{{text: "a\u200db"}},
			expected: 2,
		},
		{
			name:     "regional indicator flag counts as two columns",
			run:      textRun{{text: "🇺🇸"}},
			expected: 2,
		},
		{
			name:     "skin tone emoji counts as two columns",
			run:      textRun{{text: "👍🏽"}},
			expected: 2,
		},
		{
			name:     "vs16 promotes heart to two columns",
			run:      textRun{{text: "❤️"}},
			expected: 2,
		},
		{
			name:     "heart without vs16 is one column",
			run:      textRun{{text: "❤"}},
			expected: 1,
		},
		{
			name:     "nerd font icon counts as two columns",
			run:      textRun{{text: "󰗠"}},
			expected: 2,
		},
		{
			name:     "fullwidth latin counts as two columns",
			run:      textRun{{text: "ｗ"}},
			expected: 2,
		},
		{
			name:     "halfwidth katakana counts as one column",
			run:      textRun{{text: "ﾜ"}},
			expected: 1,
		},
		{
			name:     "astral narrow rune counts as one column",
			run:      textRun{{text: "𝄞"}},
			expected: 1,
		},
		{
			name:     "circled digit narrow circled number wide",
			run:      textRun{{text: "①㉑"}},
			expected: 3,
		},
		{
			name:     "empty span contributes nothing",
			run:      textRun{{text: ""}, {text: "ab"}, {text: ""}},
			expected: 2,
		},
		{
			name: "mixed styled spans sum display columns",
			run: textRun{
				{text: "héllo ", style: styleBold},
				{text: "世界", style: styleCode},
				{text: " 🚀", style: styleLink, url: "u"},
			},
			expected: 13,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.run.Width())
		})
	}
}

func TestWrapTextRun(t *testing.T) {
	tests := []struct {
		name          string
		run           textRun
		width         int
		expectedLines int
	}{
		{
			name:          "fits in width",
			run:           textRun{{text: "hello"}},
			width:         10,
			expectedLines: 1,
		},
		{
			name:          "wraps at word boundary",
			run:           textRun{{text: "hello world"}},
			width:         6,
			expectedLines: 2,
		},
		{
			name:          "breaks long word",
			run:           textRun{{text: "superlongword"}},
			width:         5,
			expectedLines: 3,
		},
		{
			name:          "zero width",
			run:           textRun{{text: "hello"}},
			width:         0,
			expectedLines: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := wrapTextRun(tt.run, tt.width)
			assert.Len(t, lines, tt.expectedLines)
		})
	}
}

func TestWrapTextRunWideClusters(t *testing.T) {
	tests := []struct {
		name     string
		run      textRun
		width    int
		expected []string
	}{
		{
			name:     "emoji does not straddle the wrap boundary",
			run:      textRun{{text: "ab🚀"}},
			width:    3,
			expected: []string{"ab", "🚀"},
		},
		{
			name:     "cjk word breaks at cluster boundaries",
			run:      textRun{{text: "日本語"}},
			width:    4,
			expected: []string{"日本", "語"},
		},
		{
			name:     "zwj family stays whole",
			run:      textRun{{text: "🚀👨‍👩‍👧🚀"}},
			width:    4,
			expected: []string{"🚀👨‍👩‍👧", "🚀"},
		},
		{
			name:     "wide word wraps as a unit at word boundary",
			run:      textRun{{text: "日本語 hi"}},
			width:    6,
			expected: []string{"日本語", "hi"},
		},
		{
			name:     "cluster wider than the line is force placed",
			run:      textRun{{text: "🚀🚀"}},
			width:    1,
			expected: []string{"🚀", "🚀"},
		},
		{
			name:     "multibyte word measured in columns not bytes",
			run:      textRun{{text: "héllo wörld"}},
			width:    11,
			expected: []string{"héllo wörld"},
		},
		{
			name:     "empty run",
			run:      textRun{},
			width:    5,
			expected: []string{},
		},
		{
			name:     "zero width yields no lines",
			run:      textRun{{text: "hello"}},
			width:    0,
			expected: []string{},
		},
		{
			name:     "negative width yields no lines",
			run:      textRun{{text: "hello"}},
			width:    -1,
			expected: []string{},
		},
		{
			name:     "spaces only yields no lines",
			run:      textRun{{text: "   "}},
			width:    5,
			expected: []string{},
		},
		{
			name:     "tab acts as a word separator",
			run:      textRun{{text: "a\tb"}},
			width:    10,
			expected: []string{"a b"},
		},
		{
			name:     "non breaking space acts as a word separator",
			run:      textRun{{text: "a\u00a0b"}},
			width:    10,
			expected: []string{"a b"},
		},
		{
			name:     "consecutive spaces collapse",
			run:      textRun{{text: "a   b"}},
			width:    10,
			expected: []string{"a b"},
		},
		{
			name:     "nul stays inside its word",
			run:      textRun{{text: "a\x00b"}},
			width:    10,
			expected: []string{"a\x00b"},
		},
		{
			name:     "nul word chunks by columns",
			run:      textRun{{text: "a\x00b"}},
			width:    2,
			expected: []string{"a\x00", "b"},
		},
		{
			name:     "combining clusters chunk whole",
			run:      textRun{{text: "e\u0301e\u0301"}},
			width:    1,
			expected: []string{"e\u0301", "e\u0301"},
		},
		{
			name:     "flags wrap at cluster boundaries",
			run:      textRun{{text: "🇺🇸🇺🇸"}},
			width:    2,
			expected: []string{"🇺🇸", "🇺🇸"},
		},
		{
			name:     "skin tone emoji wraps whole",
			run:      textRun{{text: "👍🏽👍🏽"}},
			width:    3,
			expected: []string{"👍🏽", "👍🏽"},
		},
		{
			name:     "wide word after narrow word wraps at boundary",
			run:      textRun{{text: "ab 中文"}},
			width:    4,
			expected: []string{"ab", "中文"},
		},
		{
			name:     "zero width space is not a separator",
			run:      textRun{{text: "a\u200bb"}},
			width:    10,
			expected: []string{"a\u200bb"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := wrapTextRun(tt.run, tt.width)
			got := make([]string, len(lines))
			for i, line := range lines {
				got[i] = line.String()
				assert.True(t, utf8.ValidString(got[i]),
					"line %d split mid-rune: %q", i, got[i])
			}
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestClusterChunk(t *testing.T) {
	tests := []struct {
		name      string
		word      string
		maxWidth  int
		wantChunk string
		wantWidth int
	}{
		{name: "empty word", word: "", maxWidth: 5},
		{name: "zero max width", word: "abc", maxWidth: 0},
		{name: "ascii fits whole", word: "hello", maxWidth: 10,
			wantChunk: "hello", wantWidth: 5},
		{name: "ascii exact fit", word: "hello", maxWidth: 5,
			wantChunk: "hello", wantWidth: 5},
		{name: "ascii truncated", word: "hello", maxWidth: 3,
			wantChunk: "hel", wantWidth: 3},
		{name: "accented truncated at cluster boundary", word: "héllo",
			maxWidth: 3, wantChunk: "hél", wantWidth: 3},
		{name: "cjk exact fit", word: "中文", maxWidth: 4,
			wantChunk: "中文", wantWidth: 4},
		{name: "cjk cannot split a cluster", word: "中文", maxWidth: 3,
			wantChunk: "中", wantWidth: 2},
		{name: "leading cluster too wide yields empty", word: "中文",
			maxWidth: 1},
		{name: "emoji too wide yields empty", word: "🚀", maxWidth: 1},
		{name: "combining cluster kept whole", word: "e\u0301x", maxWidth: 1,
			wantChunk: "e\u0301", wantWidth: 1},
		{name: "zwj attached to ascii kept whole", word: "a\u200db",
			maxWidth: 1, wantChunk: "a\u200d", wantWidth: 1},
		{name: "family cluster kept whole", word: "👨‍👩‍👧x", maxWidth: 2,
			wantChunk: "👨‍👩‍👧", wantWidth: 2},
		{name: "nul counts one column", word: "\x00\x00", maxWidth: 1,
			wantChunk: "\x00", wantWidth: 1},
		{name: "lone combining mark counts one column", word: "\u0301x",
			maxWidth: 1, wantChunk: "\u0301", wantWidth: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunk, width := clusterChunk(tt.word, tt.maxWidth)
			assert.Equal(t, tt.wantChunk, chunk)
			assert.Equal(t, tt.wantWidth, width)
			assert.True(t, utf8.ValidString(chunk))
		})
	}
}

func TestWrapPreservesStyleAcrossChunks(t *testing.T) {
	run := textRun{{text: "日本語のリンク", style: styleLink, url: "u"}}
	lines := wrapTextRun(run, 4)
	require.NotEmpty(t, lines)
	for i, line := range lines {
		require.NotEmpty(t, line, "line %d", i)
		for _, sp := range line {
			assert.Equal(t, styleLink, sp.style, "line %d span %q", i, sp.text)
			assert.Equal(t, "u", sp.url, "line %d span %q", i, sp.text)
		}
	}
}

func TestCountWrappedLines(t *testing.T) {
	tests := []struct {
		name     string
		run      textRun
		width    int
		expected int
	}{
		{name: "zero width", run: textRun{{text: "ab"}}, width: 0, expected: 0},
		{name: "empty run counts one line", run: textRun{}, width: 5, expected: 1},
		{name: "spaces only counts one line", run: textRun{{text: "  "}},
			width: 5, expected: 1},
		{name: "fits in one line", run: textRun{{text: "ab"}}, width: 5,
			expected: 1},
		{name: "cjk wraps by columns", run: textRun{{text: "中文字"}},
			width: 4, expected: 2},
		{name: "family emoji stays on one line",
			run: textRun{{text: "👨‍👩‍👧"}}, width: 2, expected: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, countWrappedLines(tt.run, tt.width))
		})
	}
}

func TestSpanAtInLine(t *testing.T) {
	linkLine := textRun{
		{text: "a"},
		{text: "🚀", url: "u1", style: styleLink},
		{text: "b", url: "u2", style: styleLink},
	}
	cjkLine := textRun{{text: "中文"}, {text: "x", url: "u"}}
	zeroWidthLine := textRun{{text: "\u200d"}, {text: "a"}}
	controlLine := textRun{{text: "\x00\tb"}}

	tests := []struct {
		name     string
		line     textRun
		x        int
		wantText string
		wantURL  string
		wantOK   bool
	}{
		{name: "empty line", line: textRun{}, x: 0},
		{name: "nil line", line: nil, x: 0},
		{name: "negative x", line: linkLine, x: -1},
		{name: "ascii span", line: linkLine, x: 0, wantText: "a", wantOK: true},
		{name: "first column of wide cluster", line: linkLine, x: 1,
			wantText: "🚀", wantURL: "u1", wantOK: true},
		{name: "continuation column of wide cluster", line: linkLine, x: 2,
			wantText: "🚀", wantURL: "u1", wantOK: true},
		{name: "span after wide cluster", line: linkLine, x: 3,
			wantText: "b", wantURL: "u2", wantOK: true},
		{name: "past end", line: linkLine, x: 4},
		{name: "cjk first cluster", line: cjkLine, x: 0,
			wantText: "中文", wantOK: true},
		{name: "cjk second cluster continuation", line: cjkLine, x: 3,
			wantText: "中文", wantOK: true},
		{name: "span after cjk", line: cjkLine, x: 4,
			wantText: "x", wantURL: "u", wantOK: true},
		{name: "zero width cluster occupies its column", line: zeroWidthLine,
			x: 0, wantText: "\u200d", wantOK: true},
		{name: "span after zero width cluster", line: zeroWidthLine, x: 1,
			wantText: "a", wantOK: true},
		{name: "control characters count one column each", line: controlLine,
			x: 2, wantText: "\x00\tb", wantOK: true},
		{name: "past controls end", line: controlLine, x: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, url, ok := spanAtInLine(tt.line, tt.x)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantText, text)
			assert.Equal(t, tt.wantURL, url)
		})
	}
}

func TestCharAtInLine(t *testing.T) {
	emojiLine := textRun{{text: "a"}, {text: "🚀"}, {text: "b"}}
	combiningLine := textRun{{text: "e\u0301x"}}
	familyLine := textRun{{text: "👨‍👩‍👧"}}
	controlLine := textRun{{text: "\x00\tb"}}
	flagLine := textRun{{text: "🇺🇸x"}}

	tests := []struct {
		name   string
		line   textRun
		x      int
		want   rune
		wantOK bool
	}{
		{name: "empty line", line: textRun{}, x: 0},
		{name: "negative x", line: emojiLine, x: -1},
		{name: "ascii", line: emojiLine, x: 0, want: 'a', wantOK: true},
		{name: "first column of wide cluster", line: emojiLine, x: 1,
			want: '🚀', wantOK: true},
		{name: "continuation column of wide cluster", line: emojiLine, x: 2,
			want: '🚀', wantOK: true},
		{name: "after wide cluster", line: emojiLine, x: 3,
			want: 'b', wantOK: true},
		{name: "past end", line: emojiLine, x: 4},
		{name: "combining cluster resolves to base rune", line: combiningLine,
			x: 0, want: 'e', wantOK: true},
		{name: "after combining cluster", line: combiningLine, x: 1,
			want: 'x', wantOK: true},
		{name: "family resolves to base rune", line: familyLine, x: 0,
			want: '👨', wantOK: true},
		{name: "family continuation resolves to base rune", line: familyLine,
			x: 1, want: '👨', wantOK: true},
		{name: "nul resolves with ok", line: controlLine, x: 0,
			want: 0, wantOK: true},
		{name: "tab resolves", line: controlLine, x: 1,
			want: '\t', wantOK: true},
		{name: "flag resolves to first regional indicator", line: flagLine,
			x: 1, want: '🇺', wantOK: true},
		{name: "after flag", line: flagLine, x: 2, want: 'x', wantOK: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := charAtInLine(tt.line, tt.x)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWrapTextRunPreservesSpaces(t *testing.T) {
	tests := []struct {
		name     string
		run      textRun
		width    int
		expected string
	}{
		{
			name:     "single span with spaces",
			run:      textRun{{text: "hello world"}},
			width:    20,
			expected: "hello world",
		},
		{
			name: "multiple spans with trailing space",
			run: textRun{
				{text: "Create file in "},
				{text: "component/", style: styleCode},
			},
			width:    80,
			expected: "Create file in component/",
		},
		{
			name: "multiple spans without explicit space",
			run: textRun{
				{text: "hello"},
				{text: " "},
				{text: "world"},
			},
			width:    80,
			expected: "hello world",
		},
		{
			name: "inline code in sentence",
			run: textRun{
				{text: "Implement "},
				{text: "tui.Component", style: styleCode},
				{text: " interface"},
			},
			width:    80,
			expected: "Implement tui.Component interface",
		},
		{
			name: "bold word in sentence",
			run: textRun{
				{text: "This is "},
				{text: "bold", style: styleBold},
				{text: " text"},
			},
			width:    80,
			expected: "This is bold text",
		},
		{
			name: "Development Workflow header",
			run: textRun{
				{text: "Development Workflow"},
			},
			width:    80,
			expected: "Development Workflow",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := wrapTextRun(tt.run, tt.width)
			var buf strings.Builder
			for _, line := range lines {
				buf.WriteString(line.String())
			}
			assert.Equal(t, tt.expected, buf.String())
		})
	}
}

func TestHeaderPrefixRendering(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		level    int
		content  string
		width    int
		expected string
	}{
		{
			// H1 has background in DefaultConfig, so content is offset by 1
			name:     "H1 with # prefix",
			level:    1,
			content:  "Title",
			width:    12,
			expected: " # Title",
		},
		{
			// H2-H6 don't have background, so no offset
			name:     "H2 with ## prefix",
			level:    2,
			content:  "Section",
			width:    15,
			expected: "## Section",
		},
		{
			name:     "H3 with ### prefix",
			level:    3,
			content:  "Subsection",
			width:    20,
			expected: "### Subsection",
		},
		{
			name:     "H6 with ###### prefix",
			level:    6,
			content:  "Deep",
			width:    20,
			expected: "###### Deep",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newHeaderBlock(tt.level, textRun{{text: tt.content}}, &cfg)
			w := term.NewStringWriter(tt.width, 4)
			require.NoError(t, w.Clear(term.Attributes{}))
			h := block.Height(tt.width)
			block.Resize(tt.width, h)
			block.Draw(w)
			require.NoError(t, w.Flush())

			// The header is drawn at Y=1 (with 1 line padding above)
			// Extract the content line (line 1)
			lines := strings.Split(w.String(), "\n")
			require.GreaterOrEqual(t, len(lines), 2)
			contentLine := strings.TrimRight(lines[1], " ")
			assert.Equal(t, tt.expected, contentLine)
		})
	}
}

// drawBlockLines renders b at width and returns the flushed rows with
// trailing spaces trimmed. StringWriter renders the untouched continuation
// column of a wide cell as a space and drops combining runes, so a wide
// cluster appears as "<base rune><space>" in the returned strings.
func drawBlockLines(t *testing.T, b block, width, height int) []string {
	t.Helper()
	w := term.NewStringWriter(width, height)
	require.NoError(t, w.Clear(term.Attributes{}))
	b.Resize(width, b.Height(width))
	b.Draw(w)
	require.NoError(t, w.Flush())
	lines := strings.Split(w.String(), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return lines
}

// assertDrawnLines compares the leading rows against expected and requires
// every remaining row to be blank.
func assertDrawnLines(t *testing.T, got, expected []string) {
	t.Helper()
	require.GreaterOrEqual(t, len(got), len(expected))
	assert.Equal(t, expected, got[:len(expected)])
	for i := len(expected); i < len(got); i++ {
		assert.Empty(t, got[i], "row %d should be blank", i)
	}
}

func TestParagraphDrawEdgeCases(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		content  textRun
		width    int
		expected []string
	}{
		{
			name:     "accented ascii",
			content:  textRun{{text: "héllo"}},
			width:    10,
			expected: []string{"héllo"},
		},
		{
			name:     "cjk wraps by columns",
			content:  textRun{{text: "中文字"}},
			width:    4,
			expected: []string{"中 文", "字"},
		},
		{
			name:     "emoji inside a word",
			content:  textRun{{text: "a🚀b c"}},
			width:    10,
			expected: []string{"a🚀 b c"},
		},
		{
			name:     "emoji word wraps whole",
			content:  textRun{{text: "ab 🚀🚀"}},
			width:    4,
			expected: []string{"ab", "🚀 🚀"},
		},
		{
			name:     "nul renders as one blank cell",
			content:  textRun{{text: "a\x00b"}},
			width:    10,
			expected: []string{"a b"},
		},
		{
			name:     "tab separates words",
			content:  textRun{{text: "a\tb"}},
			width:    10,
			expected: []string{"a b"},
		},
		{
			name:     "family emoji renders as one cell",
			content:  textRun{{text: "👨‍👩‍👧"}},
			width:    10,
			expected: []string{"👨"},
		},
		{
			name:     "vs16 heart wide plain heart narrow",
			content:  textRun{{text: "❤ ❤️"}},
			width:    10,
			expected: []string{"❤ ❤"},
		},
		{
			name: "styled spans keep column positions",
			content: textRun{
				{text: "中", style: styleBold},
				{text: "x", style: styleCode},
			},
			width:    10,
			expected: []string{"中 x"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newParagraphBlock(tt.content, &cfg)
			got := drawBlockLines(t, block, tt.width, len(tt.expected)+2)
			assertDrawnLines(t, got, tt.expected)
		})
	}
}

func TestParagraphHitTestingWideClusters(t *testing.T) {
	cfg := DefaultConfig()
	block := newParagraphBlock(textRun{
		{text: "中文 "},
		{text: "link", style: styleLink, url: "u"},
	}, &cfg)
	block.Resize(10, block.Height(10))

	spanTests := []struct {
		x, y     int
		wantText string
		wantURL  string
		wantOK   bool
	}{
		{x: 0, y: 0, wantText: "中文", wantOK: true},
		{x: 3, y: 0, wantText: "中文", wantOK: true},
		{x: 4, y: 0, wantText: " ", wantOK: true},
		{x: 5, y: 0, wantText: "link", wantURL: "u", wantOK: true},
		{x: 8, y: 0, wantText: "link", wantURL: "u", wantOK: true},
		{x: 9, y: 0},
		{x: 0, y: 1},
		{x: 0, y: -1},
	}
	for _, tt := range spanTests {
		text, url, ok := block.SpanAt(tt.x, tt.y)
		assert.Equal(t, tt.wantOK, ok, "SpanAt(%d,%d)", tt.x, tt.y)
		assert.Equal(t, tt.wantText, text, "SpanAt(%d,%d)", tt.x, tt.y)
		assert.Equal(t, tt.wantURL, url, "SpanAt(%d,%d)", tt.x, tt.y)
	}

	charTests := []struct {
		x, y   int
		want   rune
		wantOK bool
	}{
		{x: 0, y: 0, want: '中', wantOK: true},
		{x: 1, y: 0, want: '中', wantOK: true},
		{x: 2, y: 0, want: '文', wantOK: true},
		{x: 5, y: 0, want: 'l', wantOK: true},
		{x: 9, y: 0},
	}
	for _, tt := range charTests {
		got, ok := block.CharAt(tt.x, tt.y)
		assert.Equal(t, tt.wantOK, ok, "CharAt(%d,%d)", tt.x, tt.y)
		assert.Equal(t, tt.want, got, "CharAt(%d,%d)", tt.x, tt.y)
	}
}

func TestHeaderDrawWideClusters(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		level    int
		content  textRun
		width    int
		expected []string
	}{
		{
			name:    "h2 with emoji",
			level:   2,
			content: textRun{{text: "🚀 Go"}},
			width:   20,
			// row 0 is header spacing; the emoji occupies two columns.
			expected: []string{"", "## 🚀  Go"},
		},
		{
			name:     "h3 with cjk",
			level:    3,
			content:  textRun{{text: "中文"}},
			width:    20,
			expected: []string{"", "### 中 文"},
		},
		{
			name:     "h1 with emoji keeps background offset",
			level:    1,
			content:  textRun{{text: "🚀"}},
			width:    12,
			expected: []string{"", " # 🚀"},
		},
		{
			name:     "wide content wraps with prefix indent",
			level:    2,
			content:  textRun{{text: "中文字词"}},
			width:    7,
			expected: []string{"", "## 中 文", "   字 词"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newHeaderBlock(tt.level, tt.content, &cfg)
			got := drawBlockLines(t, block, tt.width, len(tt.expected)+2)
			assertDrawnLines(t, got, tt.expected)
		})
	}
}

func TestListDrawWideClusters(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		ordered  bool
		start    int
		items    []listItem
		width    int
		expected []string
	}{
		{
			name:     "bullet item with emoji",
			items:    []listItem{{content: textRun{{text: "🚀 go"}}}},
			width:    10,
			expected: []string{"• 🚀  go"},
		},
		{
			name:     "ordered item with cjk",
			ordered:  true,
			start:    3,
			items:    []listItem{{content: textRun{{text: "中"}}}},
			width:    10,
			expected: []string{"3. 中"},
		},
		{
			name: "task items",
			items: []listItem{
				{content: textRun{{text: "done"}}, isTask: true, checked: true},
				{content: textRun{{text: "todo"}}, isTask: true},
			},
			width:    10,
			expected: []string{"☑ done", "☐ todo"},
		},
		{
			name:     "cjk content wraps at the content column",
			items:    []listItem{{content: textRun{{text: "中文字"}}}},
			width:    6,
			expected: []string{"• 中 文", "  字"},
		},
		{
			name: "nested list indents wide content",
			items: []listItem{{
				content: textRun{{text: "a"}},
				nested: newListBlock(false, 1, []listItem{
					{content: textRun{{text: "中"}}},
				}, &cfg),
			}},
			width:    10,
			expected: []string{"• a", "  • 中"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newListBlock(tt.ordered, max(1, tt.start), tt.items, &cfg)
			got := drawBlockLines(t, block, tt.width, len(tt.expected)+2)
			assertDrawnLines(t, got, tt.expected)
		})
	}
}

func TestBlockquoteDrawWideClusters(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		content  []textRun
		nested   *blockquoteBlock
		width    int
		expected []string
	}{
		{
			name:     "emoji content",
			content:  []textRun{{{text: "🚀 go"}}},
			width:    10,
			expected: []string{"│ 🚀  go"},
		},
		{
			name:     "cjk content wraps at the content column",
			content:  []textRun{{{text: "中文字"}}},
			width:    6,
			expected: []string{"│ 中 文", "│ 字"},
		},
		{
			name:     "multiple runs with mixed width",
			content:  []textRun{{{text: "a"}}, {{text: "中"}}},
			width:    10,
			expected: []string{"│ a", "│ 中"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newBlockquoteBlock(tt.content, tt.nested, &cfg)
			got := drawBlockLines(t, block, tt.width, len(tt.expected)+2)
			assertDrawnLines(t, got, tt.expected)
		})
	}
}

func TestParagraphDrawWideClusters(t *testing.T) {
	cfg := DefaultConfig()
	block := newParagraphBlock(textRun{{text: "a 🚀 b"}}, &cfg)
	block.Resize(10, block.Height(10))

	w := term.NewStringWriter(10, 3)
	require.NoError(t, w.Clear(term.Attributes{}))
	block.Draw(w)

	cells := w.Cells()
	assert.Equal(t, 'a', cells[0].Ch)
	assert.Equal(t, uint8(1), cells[0].Width)
	assert.Equal(t, '🚀', cells[2].Ch)
	assert.Equal(t, uint8(2), cells[2].Width)
	assert.Equal(t, rune(0), cells[3].Ch, "continuation column must stay empty")
	assert.Equal(t, 'b', cells[5].Ch)
	assert.Equal(t, uint8(1), cells[5].Width)
}

func TestParagraphDrawCombiningClusters(t *testing.T) {
	cfg := DefaultConfig()
	block := newParagraphBlock(textRun{{text: "👨‍👩‍👧❤️"}}, &cfg)
	block.Resize(10, block.Height(10))

	w := term.NewStringWriter(10, 3)
	require.NoError(t, w.Clear(term.Attributes{}))
	block.Draw(w)

	cells := w.Cells()
	assert.Equal(t, '👨', cells[0].Ch)
	assert.Equal(t, uint8(2), cells[0].Width)
	assert.Equal(t, []rune{'\u200d', '👩', '\u200d', '👧'}, cells[0].CombiningRunes())
	assert.Equal(t, '❤', cells[2].Ch)
	assert.Equal(t, uint8(2), cells[2].Width)
	assert.Equal(t, []rune{'\ufe0f'}, cells[2].CombiningRunes())
}

func TestNestedListSpacing(t *testing.T) {
	cfg := DefaultConfig()

	// Create a nested list similar to a table of contents
	nestedList := newListBlock(false, 1, []listItem{
		{content: textRun{{text: "Sub 1"}}},
		{content: textRun{{text: "Sub 2"}}},
	}, &cfg)

	parentList := newListBlock(false, 1, []listItem{
		{content: textRun{{text: "Item 1"}}},
		{
			content: textRun{{text: "Item 2"}},
			nested:  nestedList,
		},
		{content: textRun{{text: "Item 3"}}},
	}, &cfg)

	// Expected height:
	// - Item 1: 1 line
	// - Item 2: 1 line
	// - Sub 1: 1 line (nested, no extra spacing)
	// - Sub 2: 1 line (nested, no extra spacing)
	// - Item 3: 1 line
	// - Top level spacing: 1 line
	// Total: 6 lines
	assert.Equal(t, 6, parentList.Height(30))
}

func TestNestedListDraw(t *testing.T) {
	// Regression test: nested list items must be drawn, not just counted
	// in height. Previously, nested listBlock instances had l.w == 0
	// (only the top-level list's Height call set l.w), so drawAtIndent
	// checked "l.w <= indent+listIndent" → "0 <= anything" → true, and
	// returned 0 immediately, skipping all nested content. Height still
	// counted nested items, so blank rows appeared in their place.
	cfg := DefaultConfig()

	nestedList := newListBlock(false, 1, []listItem{
		{content: textRun{{text: "Child A"}}},
		{content: textRun{{text: "Child B"}}},
	}, &cfg)

	parentList := newListBlock(false, 1, []listItem{
		{
			content: textRun{{text: "Parent 1"}},
			nested:  nestedList,
		},
		{content: textRun{{text: "Parent 2"}}},
	}, &cfg)

	width := 30
	h := parentList.Height(width)
	// Parent 1 (1) + Child A (1) + Child B (1) + Parent 2 (1) + spacing (1) = 5
	require.Equal(t, 5, h)
	parentList.Resize(width, h)

	w := term.NewStringWriter(width, h)
	require.NoError(t, w.Clear(term.Attributes{}))
	parentList.Draw(w)
	require.NoError(t, w.Flush())

	output := w.String()
	assert.Contains(t, output, "Child A", "nested item must be drawn")
	assert.Contains(t, output, "Child B", "nested item must be drawn")
	assert.Contains(t, output, "Parent 1")
	assert.Contains(t, output, "Parent 2")
}

func TestNestedListDrawDeep(t *testing.T) {
	// Three levels of nesting to verify the fix propagates recursively.
	cfg := DefaultConfig()

	grandchild := newListBlock(false, 1, []listItem{
		{content: textRun{{text: "Grandchild"}}},
	}, &cfg)
	child := newListBlock(false, 1, []listItem{
		{content: textRun{{text: "Child"}}, nested: grandchild},
	}, &cfg)
	root := newListBlock(false, 1, []listItem{
		{content: textRun{{text: "Root"}}, nested: child},
	}, &cfg)

	width := 30
	h := root.Height(width)
	root.Resize(width, h)
	w := term.NewStringWriter(width, h)
	require.NoError(t, w.Clear(term.Attributes{}))
	root.Draw(w)
	require.NoError(t, w.Flush())

	output := w.String()
	assert.Contains(t, output, "Root")
	assert.Contains(t, output, "Child")
	assert.Contains(t, output, "Grandchild", "deeply nested item must be drawn")
}

func TestHeaderDimensions(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name           string
		level          int
		content        string
		expectedWidth  int
		expectedHeight int
	}{
		{
			name:           "H1 dimensions",
			level:          1,
			content:        "Title",
			expectedWidth:  7, // "# " (2) + "Title" (5)
			expectedHeight: 3, // 1 (above) + 1 (content) + 1 (spacing)
		},
		{
			name:           "H3 dimensions",
			level:          3,
			content:        "Test",
			expectedWidth:  8, // "### " (4) + "Test" (4)
			expectedHeight: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			block := newHeaderBlock(tt.level, textRun{{text: tt.content}}, &cfg)
			w, h := block.Dimensions()
			assert.Equal(t, tt.expectedWidth, w)
			assert.Equal(t, tt.expectedHeight, h)
		})
	}
}

func TestHeaderPadding(t *testing.T) {
	cfg := DefaultConfig()
	block := newHeaderBlock(1, textRun{{text: "Title"}}, &cfg)
	w := term.NewStringWriter(15, 3)
	require.NoError(t, w.Clear(term.Attributes{}))
	h := block.Height(15)
	block.Resize(15, h)
	block.Draw(w)
	require.NoError(t, w.Flush())

	lines := strings.Split(w.String(), "\n")
	require.Len(t, lines, 3)

	// Line 0: blank (1 line padding above)
	assert.Equal(t, "               ", lines[0])
	// Line 1: " # Title" (content with 1 cell left padding since H1 has background)
	assert.Equal(t, " # Title       ", lines[1])
	// Line 2: blank (standard spacing)
	assert.Equal(t, "               ", lines[2])
}

func TestHeaderWrappingWithPrefix(t *testing.T) {
	cfg := DefaultConfig()
	// Test that long header content wraps correctly with prefix
	// Width = 12, prefix "## " = 3 chars, effective content width = 9 chars
	// "Long Title Here" wraps as: "Long", "Title", "Here" (each word on its own line at width 9)
	block := newHeaderBlock(2, textRun{{text: "Long Title Here"}}, &cfg)
	w := term.NewStringWriter(12, 5)
	require.NoError(t, w.Clear(term.Attributes{}))
	h := block.Height(12)
	block.Resize(12, h)
	block.Draw(w)
	require.NoError(t, w.Flush())

	lines := strings.Split(w.String(), "\n")
	require.Len(t, lines, 5)

	// Line 0: blank (1 line padding above)
	assert.Equal(t, "            ", lines[0])
	// Line 1: "## Long" (first line with prefix)
	contentLine1 := strings.TrimRight(lines[1], " ")
	assert.Equal(t, "## Long", contentLine1)
	// Line 2: "   Title" (continuation, indented with 3 spaces for "## " prefix)
	contentLine2 := strings.TrimRight(lines[2], " ")
	assert.Equal(t, "   Title", contentLine2)
	// Line 3: "   Here" (continuation)
	contentLine3 := strings.TrimRight(lines[3], " ")
	assert.Equal(t, "   Here", contentLine3)
}

func TestInlineStyleNotAppliedToLeadingWhitespace(t *testing.T) {
	// Test that leading whitespace before inline code/links doesn't inherit the style
	// Note: The markdown parser outputs spans with trailing space when there's a
	// space between normal text and styled text (e.g. "Run `command`" becomes
	// [{text: "Run "}, {text: "command", style: code}])
	tests := []struct {
		name           string
		run            textRun
		width          int
		expectedStyles []inlineStyle // styles for each span in output
	}{
		{
			name: "inline code preceded by text with trailing space",
			run: textRun{
				{text: "Run "}, // trailing space
				{text: "command", style: styleCode},
			},
			width: 80,
			// Should output: "Run" (none), " " (none), "command" (code)
			expectedStyles: []inlineStyle{styleNone, styleNone, styleCode},
		},
		{
			name: "link preceded by text with trailing space",
			run: textRun{
				{text: "Click "}, // trailing space
				{text: "here", style: styleLink, url: "http://example.com"},
			},
			width: 80,
			// Should output: "Click" (none), " " (none), "here" (link)
			expectedStyles: []inlineStyle{styleNone, styleNone, styleLink},
		},
		{
			name: "styled span with leading space",
			run: textRun{
				{text: "Run"},
				{text: " command", style: styleCode}, // leading space in styled span
			},
			width: 80,
			// Should output: "Run" (none), " " (none), "command" (code)
			expectedStyles: []inlineStyle{styleNone, styleNone, styleCode},
		},
		{
			name: "multiple styled spans with spaces",
			run: textRun{
				{text: "Use "},
				{text: "bold", style: styleBold},
				{text: " and "},
				{text: "code", style: styleCode},
			},
			width: 80,
			// Should output: "Use" (none), " " (none), "bold" (bold), " " (none),
			// "and" (none), " " (none), "code" (code)
			expectedStyles: []inlineStyle{
				styleNone, styleNone, styleBold, styleNone,
				styleNone, styleNone, styleCode,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := wrapTextRun(tt.run, tt.width)
			require.Len(t, lines, 1, "expected single line output")

			var actualStyles []inlineStyle
			for _, sp := range lines[0] {
				actualStyles = append(actualStyles, sp.style)
			}
			assert.Equal(t, tt.expectedStyles, actualStyles)
		})
	}
}

func TestInlineCodeSpaceNotStyled(t *testing.T) {
	// Specific test: space before inline code should NOT have code style
	// When markdown parser sees "Prefix `code`", it produces:
	// [{text: "Prefix "}, {text: "code", style: code}]
	run := textRun{
		{text: "Prefix "}, // trailing space
		{text: "code", style: styleCode},
	}
	lines := wrapTextRun(run, 80)
	require.Len(t, lines, 1)

	// Find the space span
	var spaceSpan *span
	for i := range lines[0] {
		if lines[0][i].text == " " {
			spaceSpan = &lines[0][i]
			break
		}
	}

	require.NotNil(t, spaceSpan, "expected to find space span")
	assert.Equal(t, styleNone, spaceSpan.style, "space should have no style")
}

func TestLinkSpaceNotUnderlined(t *testing.T) {
	// Specific test: space before link should NOT have link style (underline)
	// When markdown parser sees "Click [here](url)", it produces:
	// [{text: "Click "}, {text: "here", style: link, url: "..."}]
	run := textRun{
		{text: "Click "}, // trailing space
		{text: "here", style: styleLink, url: "http://test.com"},
	}
	lines := wrapTextRun(run, 80)
	require.Len(t, lines, 1)

	// Find the space span
	var spaceSpan *span
	for i := range lines[0] {
		if lines[0][i].text == " " {
			spaceSpan = &lines[0][i]
			break
		}
	}

	require.NotNil(t, spaceSpan, "expected to find space span")
	assert.Equal(t, styleNone, spaceSpan.style, "space should have no style (no underline)")
}

func TestHeaderPrefixConfig(t *testing.T) {
	tests := []struct {
		name         string
		headerPrefix bool
		level        int
		content      string
		width        int
		expectedText string
	}{
		{
			name:         "prefix enabled (default)",
			headerPrefix: true,
			level:        1,
			content:      "Title",
			width:        20,
			expectedText: "# Title",
		},
		{
			name:         "prefix disabled",
			headerPrefix: false,
			level:        1,
			content:      "Title",
			width:        20,
			expectedText: "Title",
		},
		{
			name:         "H2 prefix enabled",
			headerPrefix: true,
			level:        2,
			content:      "Section",
			width:        20,
			expectedText: "## Section",
		},
		{
			name:         "H2 prefix disabled",
			headerPrefix: false,
			level:        2,
			content:      "Section",
			width:        20,
			expectedText: "Section",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.HeaderPrefix = tt.headerPrefix
			// Clear background to avoid offset
			cfg.H1 = term.Attributes{}
			cfg.H2 = term.Attributes{}

			block := newHeaderBlock(tt.level, textRun{{text: tt.content}}, &cfg)
			w := term.NewStringWriter(tt.width, 4)
			require.NoError(t, w.Clear(term.Attributes{}))
			h := block.Height(tt.width)
			block.Resize(tt.width, h)
			block.Draw(w)
			require.NoError(t, w.Flush())

			// Header content is at Y=1 (1 line padding above)
			lines := strings.Split(w.String(), "\n")
			require.GreaterOrEqual(t, len(lines), 2)
			contentLine := strings.TrimRight(lines[1], " ")
			assert.Equal(t, tt.expectedText, contentLine)
		})
	}
}

func TestHeaderPrefixDimensions(t *testing.T) {
	cfg := DefaultConfig()

	// With prefix enabled
	cfg.HeaderPrefix = true
	block := newHeaderBlock(2, textRun{{text: "Title"}}, &cfg)
	w, h := block.Dimensions()
	assert.Equal(t, 8, w) // "## " (3) + "Title" (5)
	assert.Equal(t, 3, h)

	// With prefix disabled
	cfg.HeaderPrefix = false
	block = newHeaderBlock(2, textRun{{text: "Title"}}, &cfg)
	w, h = block.Dimensions()
	assert.Equal(t, 5, w) // "Title" (5) only
	assert.Equal(t, 3, h)
}

func TestHeaderBackgroundPadding(t *testing.T) {
	// Test that header background has symmetric padding (1 cell on each side)
	// For "# Title" (7 chars), background should be:
	// - X=0: padding (bg only, no content)
	// - X=1-7: content "# Title" (with bg)
	// - X=8: padding (bg only, no content)
	cfg := DefaultConfig()
	cfg.H1 = term.Attributes{Bg: term.ColorRed}

	block := newHeaderBlock(1, textRun{{text: "Title"}}, &cfg)
	w := term.NewStringWriter(20, 4)
	require.NoError(t, w.Clear(term.Attributes{}))
	h := block.Height(20)
	block.Resize(20, h)
	block.Draw(w)
	require.NoError(t, w.Flush())

	// Content "# Title" is 7 chars, with 1 padding each side = 9 cells with bg
	// Header is drawn at Y=1 (1 line padding above)
	// Check that content starts at X=1 (leaving X=0 for left padding)
	lines := strings.Split(w.String(), "\n")
	require.GreaterOrEqual(t, len(lines), 2)

	contentLine := lines[1]
	// X=0 should be a space (left padding)
	assert.Equal(t, ' ', rune(contentLine[0]), "X=0 should be padding space")
	// X=1 should be '#' (start of content)
	assert.Equal(t, '#', rune(contentLine[1]), "X=1 should be start of content '#'")
	// Content "# Title" spans X=1 to X=7
	// X=8 should be a space (right padding)
	assert.Equal(t, ' ', rune(contentLine[8]), "X=8 should be padding space")
}
