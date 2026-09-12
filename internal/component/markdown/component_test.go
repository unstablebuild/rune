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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestComponentNew(t *testing.T) {
	md, err := New("# Hello")
	require.NoError(t, err)
	require.NotNil(t, md)
	assert.NotEmpty(t, md.blocks)
}

func TestComponentNewWithConfig(t *testing.T) {
	cfg := DefaultConfig()
	md, err := NewWithConfig("# Hello", cfg)
	require.NoError(t, err)
	require.NotNil(t, md)
	assert.NotEmpty(t, md.blocks)
}

func TestComponentResize(t *testing.T) {
	md, err := New("# Hello\n\nWorld")
	require.NoError(t, err)
	md.Resize(40, 10)
	assert.Equal(t, 40, md.width)
	assert.Equal(t, 10, md.height)
	assert.Greater(t, md.totalHeight, 0)
}

func TestComponentResizeThenDraw(t *testing.T) {
	md, err := New("Hi")
	require.NoError(t, err)
	w := term.NewStringWriter(10, 3)

	require.NoError(t, w.Clear(term.Attributes{}))
	md.Resize(10, 3)
	md.Draw(w)
	require.NoError(t, w.Flush())

	expected := "Hi        \n          \n          "
	assert.Equal(t, expected, w.String())
}

func TestComponentDimensions(t *testing.T) {
	md, err := New("# Hello\n\nParagraph 1\n\nParagraph 2")
	require.NoError(t, err)
	// Don't call Resize - test ideal dimensions
	width, height := md.Dimensions()
	// "Paragraph 1" has 11 chars, which is the longest line
	assert.Equal(t, 11, width)
	// Header: 3 (1 above + 1 content + 1 spacing)
	// Paragraphs: 2 each * 2 = 4
	// Total: 3 + 4 = 7
	assert.Equal(t, 7, height)
}

func TestComponentDimensionsWithWrappedCodeBlocks(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		resizeWidth    int
		expectedWidth  int
		expectedHeight int
	}{
		{
			name:           "code block ideal size stays unwrapped",
			content:        "```\n123456\nab\n```",
			resizeWidth:    3,
			expectedWidth:  6,
			expectedHeight: 3,
		},
		{
			name:           "mixed content uses widest ideal block",
			content:        "# Hi\n\n```\n123456\nab\n```\n\nend",
			resizeWidth:    3,
			expectedWidth:  6,
			expectedHeight: 8,
		},
		{
			name:           "wrapped paragraph can exceed narrow resize but dimensions stay ideal",
			content:        "```\nabc\n```\n\nlong paragraph",
			resizeWidth:    4,
			expectedWidth:  14,
			expectedHeight: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := New(tt.content)
			require.NoError(t, err)
			if tt.resizeWidth != 0 {
				md.Resize(tt.resizeWidth, 10)
			}

			width, height := md.Dimensions()
			assert.Equal(t, tt.expectedWidth, width)
			assert.Equal(t, tt.expectedHeight, height)
		})
	}
}

func TestComponentScrolling(t *testing.T) {
	md, err := New("# H1\n\n# H2\n\n# H3\n\n# H4\n\n# H5")
	require.NoError(t, err)
	md.Resize(20, 3)

	assert.Equal(t, 0, md.SeekOffset())
	assert.False(t, md.SeekUp())

	if md.MaxSeekOffset() > 0 {
		assert.True(t, md.SeekDown())
		assert.Equal(t, 1, md.SeekOffset())

		assert.True(t, md.SeekUp())
		assert.Equal(t, 0, md.SeekOffset())
	}
}

func TestComponentSeekTo(t *testing.T) {
	md, err := New("# H1\n\n# H2\n\n# H3\n\n# H4\n\n# H5")
	require.NoError(t, err)
	md.Resize(20, 3)
	require.Positive(t, md.MaxSeekOffset())

	tests := []struct {
		name   string
		offset int
		want   func() int
	}{
		{
			name:   "negative offset",
			offset: -1,
			want:   func() int { return 0 },
		},
		{
			name:   "valid offset",
			offset: 2,
			want:   func() int { return 2 },
		},
		{
			name:   "offset beyond content",
			offset: md.MaxSeekOffset() + 1,
			want:   md.MaxSeekOffset,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			md.SeekTo(test.offset)
			assert.Equal(t, test.want(), md.SeekOffset())
		})
	}
}

func TestComponentMaxSeekOffset(t *testing.T) {
	tests := []struct {
		name           string
		content        string
		width          int
		height         int
		expectedMaxOff int
	}{
		{
			name:           "content fits",
			content:        "Hi",
			width:          20,
			height:         10,
			expectedMaxOff: 0,
		},
		{
			name:           "content exceeds viewport",
			content:        "# H1\n\n# H2\n\n# H3\n\n# H4\n\n# H5\n\n# H6\n\n# H7",
			width:          20,
			height:         5,
			expectedMaxOff: 16, // 7 headers × 3 lines each = 21, minus viewport 5 = 16
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := New(tt.content)
			require.NoError(t, err)
			md.Resize(tt.width, tt.height)
			assert.Equal(t, tt.expectedMaxOff, md.MaxSeekOffset())
		})
	}
}

func TestComponentDraw(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		width    int
		height   int
		expected string
	}{
		{
			name:     "simple paragraph",
			content:  "Hi",
			width:    10,
			height:   3,
			expected: "Hi        \n          \n          ",
		},
		{
			name:    "header",
			content: "# Title",
			width:   10,
			height:  3,
			// 1 line padding above, then " # Title" (H1 has bg, so 1 cell left padding), then 1 line spacing
			expected: "          \n # Title  \n          ",
		},
		{
			name:    "header and paragraph",
			content: "# H1\n\nText",
			width:   10,
			height:  5,
			// Header: blank, " # H1" (H1 has bg), blank; Paragraph: "Text", blank
			expected: "          \n # H1     \n          \nText      \n          ",
		},
		{
			name:     "horizontal rule",
			content:  "---",
			width:    5,
			height:   2,
			expected: "─────\n     ",
		},
		{
			name:     "unordered list",
			content:  "- A\n- B",
			width:    10,
			height:   4,
			expected: "• A       \n• B       \n          \n          ",
		},
		{
			name:     "code block",
			content:  "```\ncode\n```",
			width:    10,
			height:   3,
			expected: "code      \n          \n          ",
		},
		{
			name:     "blockquote",
			content:  "> quote",
			width:    10,
			height:   3,
			expected: "│ quote   \n          \n          ",
		},
		{
			name:    "table with full box",
			content: "| A | B |\n|---|---|\n| 1 | 2 |",
			width:   10,
			height:  7,
			expected: "┌────┬───┐\n" +
				"│A   │B  │\n" +
				"├────┼───┤\n" +
				"│1   │2  │\n" +
				"└────┴───┘\n" +
				"          \n" +
				"          ",
		},
		{
			name:    "table wraps long cell text",
			content: "| LongHeader | B |\n|---|---|\n| LongContent | 2 |",
			width:   10,
			height:  10,
			// col widths: avail = 10-3 = 7, cols = 4,3.
			// "LongHeader" (10 chars) wraps to "Long", "Head", "er".
			// "LongContent" (11 chars) wraps to "Long", "Cont", "ent".
			expected: "┌────┬───┐\n" +
				"│Long│B  │\n" +
				"│Head│   │\n" +
				"│er  │   │\n" +
				"├────┼───┤\n" +
				"│Long│2  │\n" +
				"│Cont│   │\n" +
				"│ent │   │\n" +
				"└────┴───┘\n" +
				"          ",
		},
		{
			name:    "table wraps with mixed row heights",
			content: "| A | BB CC DD |\n|---|---|\n| E | F |",
			width:   15,
			height:  7,
			// col widths: avail = 15-3 = 12, cols = 6,6.
			// Header: "A" (1 line), "BB CC DD" wraps to "BB CC","DD" (2 lines).
			// Data: "E" (1 line), "F" (1 line).
			expected: "┌──────┬──────┐\n" +
				"│A     │BB CC │\n" +
				"│      │DD    │\n" +
				"├──────┼──────┤\n" +
				"│E     │F     │\n" +
				"└──────┴──────┘\n" +
				"               ",
		},
		{
			name:    "table single column wraps",
			content: "| Head |\n|---|\n| AB CD |",
			width:   6,
			height:  7,
			// col width: 6-2 = 4.
			// "AB CD" wraps to "AB", "CD" (2 lines).
			expected: "┌────┐\n" +
				"│Head│\n" +
				"├────┤\n" +
				"│AB  │\n" +
				"│CD  │\n" +
				"└────┘\n" +
				"      ",
		},
		{
			name:    "table three columns middle wraps",
			content: "| A | BBBB CCCC | D |\n|---|---|---|\n| 1 | 2 | 3 |",
			width:   16,
			height:  7,
			// avail = 16-4 = 12, 3 cols each width 4.
			// "BBBB CCCC" wraps to "BBBB", "CCCC" (2 lines).
			expected: "┌────┬────┬────┐\n" +
				"│A   │BBBB│D   │\n" +
				"│    │CCCC│    │\n" +
				"├────┼────┼────┤\n" +
				"│1   │2   │3   │\n" +
				"└────┴────┴────┘\n" +
				"                ",
		},
		{
			name:    "table multiple rows different wrap heights",
			content: "| H |\n|---|\n| AB CD EF |\n| GHIJ |\n| KL MN OP QR |",
			width:   6,
			height:  13,
			// col width: 4.
			// Row 0 "AB CD EF" wraps to "AB","CD","EF" (3 lines).
			// Row 1 "GHIJ" fits (1 line).
			// Row 2 "KL MN OP QR" wraps to "KL","MN","OP","QR" (4 lines).
			expected: "┌────┐\n" +
				"│H   │\n" +
				"├────┤\n" +
				"│AB  │\n" +
				"│CD  │\n" +
				"│EF  │\n" +
				"│GHIJ│\n" +
				"│KL  │\n" +
				"│MN  │\n" +
				"│OP  │\n" +
				"│QR  │\n" +
				"└────┘\n" +
				"      ",
		},
		{
			name:    "table right-aligned wraps",
			content: "| H |\n|---:|\n| ABCDE |",
			width:   6,
			height:  7,
			// col width: 4, right-aligned.
			// "ABCDE" (5 chars) wraps to "ABCD","E".
			expected: "┌────┐\n" +
				"│   H│\n" +
				"├────┤\n" +
				"│ABCD│\n" +
				"│   E│\n" +
				"└────┘\n" +
				"      ",
		},
		{
			name:    "table center-aligned wraps",
			content: "| H |\n|:---:|\n| ABCDE |",
			width:   6,
			height:  7,
			// col width: 4, center-aligned.
			// "ABCDE" wraps to "ABCD" (fills column), "E" (padding=1).
			expected: "┌────┐\n" +
				"│ H  │\n" +
				"├────┤\n" +
				"│ABCD│\n" +
				"│ E  │\n" +
				"└────┘\n" +
				"      ",
		},
		{
			name:    "table ragged row",
			content: "| A | B | C |\n|---|---|---|\n| 1 |",
			width:   13,
			height:  7,
			// avail = 13-4 = 9, 3 cols each width 3.
			// Data row has 1 cell; cols 1,2 are empty.
			expected: "┌───┬───┬───┐\n" +
				"│A  │B  │C  │\n" +
				"├───┼───┼───┤\n" +
				"│1  │   │   │\n" +
				"└───┴───┴───┘\n" +
				"             \n" +
				"             ",
		},
		{
			name:    "table empty cell beside wrapping cell",
			content: "| H1 | H2 |\n|---|---|\n| AABBCC | |",
			width:   10,
			height:  7,
			// avail = 7, col 0 = 4, col 1 = 3.
			// "AABBCC" (6 chars) wraps at 4: "AABB","CC" (2 lines).
			// Second cell empty. Row height = 2.
			expected: "┌────┬───┐\n" +
				"│H1  │H2 │\n" +
				"├────┼───┤\n" +
				"│AABB│   │\n" +
				"│CC  │   │\n" +
				"└────┴───┘\n" +
				"          ",
		},
		{
			name:    "table header and data wrap different heights",
			content: "| AABBCC | D |\n|---|---|\n| E | FFGGHH |",
			width:   10,
			height:  8,
			// avail = 7, col 0 = 4, col 1 = 3.
			// Header: "AABBCC"→"AABB","CC" (2 lines). "D"→1 line. headerHeight = 2.
			// Data: "E"→1 line. "FFGGHH"→"FFG","GHH" (2 lines). rowHeight = 2.
			expected: "┌────┬───┐\n" +
				"│AABB│D  │\n" +
				"│CC  │   │\n" +
				"├────┼───┤\n" +
				"│E   │FFG│\n" +
				"│    │GHH│\n" +
				"└────┴───┘\n" +
				"          ",
		},
		{
			name:    "table minimum column width wraps",
			content: "| HI |\n|---|\n| ABCD |",
			width:   4,
			height:  7,
			// col width: 4-2 = 2.
			// "ABCD" wraps at 2: "AB","CD" (2 lines).
			expected: "┌──┐\n" +
				"│HI│\n" +
				"├──┤\n" +
				"│AB│\n" +
				"│CD│\n" +
				"└──┘\n" +
				"    ",
		},
		{
			name:    "table deep header wrapping",
			content: "| VeryLongHeader | B |\n|---|---|\n| X | Y |",
			width:   10,
			height:  9,
			// avail = 7, col 0 = 4, col 1 = 3.
			// "VeryLongHeader" (14 chars) wraps at 4: "Very","Long","Head","er" (4 lines).
			// headerHeight = 4.
			expected: "┌────┬───┐\n" +
				"│Very│B  │\n" +
				"│Long│   │\n" +
				"│Head│   │\n" +
				"│er  │   │\n" +
				"├────┼───┤\n" +
				"│X   │Y  │\n" +
				"└────┴───┘\n" +
				"          ",
		},
		{
			name:    "table wraps on word boundaries",
			content: "| Title |\n|---|\n| one two three |",
			width:   8,
			height:  8,
			// col width: 8-2 = 6.
			// "one two three" wraps to "one","two","three" (3 lines).
			expected: "┌──────┐\n" +
				"│Title │\n" +
				"├──────┤\n" +
				"│one   │\n" +
				"│two   │\n" +
				"│three │\n" +
				"└──────┘\n" +
				"        ",
		},
		{
			name:    "table wraps clipped by viewport",
			content: "| AABBCCDD |\n|---|\n| E |",
			width:   6,
			height:  5,
			// col width: 4. "AABBCCDD" (8 chars) wraps to "AABB","CCDD" (2 lines).
			// Total height = 1+2+1+1+1+1 = 7. Viewport clips to 5.
			expected: "┌────┐\n" +
				"│AABB│\n" +
				"│CCDD│\n" +
				"├────┤\n" +
				"│E   │",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := New(tt.content)
			require.NoError(t, err)
			w := term.NewStringWriter(tt.width, tt.height)

			require.NoError(t, w.Clear(term.Attributes{}))
			md.Resize(tt.width, tt.height)
			md.Draw(w)
			require.NoError(t, w.Flush())

			assert.Equal(t, tt.expected, w.String())
		})
	}
}

func TestComponentDrawWithScroll(t *testing.T) {
	md, err := New("# H1\n\n# H2\n\n# H3")
	require.NoError(t, err)
	w := term.NewStringWriter(10, 3)

	require.NoError(t, w.Clear(term.Attributes{}))
	md.Resize(10, 3)
	md.Draw(w)
	require.NoError(t, w.Flush())
	// First header: blank line above, then " # H1" (H1 has bg), then 1 blank line
	assert.Equal(t, "          \n # H1     \n          ", w.String())

	// Scroll down 3 lines to get to second header
	md.SeekDown()
	md.SeekDown()
	md.SeekDown()

	require.NoError(t, w.Clear(term.Attributes{}))
	md.Draw(w)
	require.NoError(t, w.Flush())
	// Second header: blank line above, then " # H2" (H1 has bg), then 1 blank line
	assert.Equal(t, "          \n # H2     \n          ", w.String())
}

func TestComponentZeroSize(t *testing.T) {
	md, err := New("# Hello")
	require.NoError(t, err)
	w := term.NewStringWriter(1, 1)

	md.Resize(0, 0)
	md.Draw(w)
}

func TestComponentScrollableFloatingInterface(t *testing.T) {
	md, err := New("# Hello\n\nWorld")
	require.NoError(t, err)
	md.Resize(40, 5)

	assert.Implements(t, (*interface {
		SeekUp() bool
		SeekDown() bool
		SeekOffset() int
		MaxSeekOffset() int
		Dimensions() (int, int)
	})(nil), md)
}

func TestSeekToAnchor(t *testing.T) {
	// Create content with multiple headers that we can anchor to
	content := "# First\n\nParagraph 1\n\n# Second\n\nParagraph 2\n\n# Third\n\nParagraph 3"
	md, err := New(content)
	require.NoError(t, err)
	md.Resize(20, 5)

	// Initially at offset 0
	assert.Equal(t, 0, md.SeekOffset())

	// Seek to "second" anchor
	ok := md.SeekToAnchor("second")
	assert.True(t, ok)
	assert.Greater(t, md.SeekOffset(), 0)

	// Seek to "third" anchor
	prevOffset := md.SeekOffset()
	ok = md.SeekToAnchor("third")
	assert.True(t, ok)
	assert.Greater(t, md.SeekOffset(), prevOffset)

	// Seek back to "first"
	ok = md.SeekToAnchor("first")
	assert.True(t, ok)
	assert.Equal(t, 0, md.SeekOffset())

	// Non-existent anchor returns false
	ok = md.SeekToAnchor("nonexistent")
	assert.False(t, ok)
}

func TestSeekToAnchorAndDraw(t *testing.T) {
	// Test that seeking to an anchor and then drawing doesn't leave stale content
	content := "# First Header\n\n# Second Header\n\n# Third Header"
	md, err := New(content)
	require.NoError(t, err)

	width, height := 20, 3
	w := term.NewStringWriter(width, height)

	// Initial draw
	require.NoError(t, w.Clear(term.Attributes{}))
	md.Resize(width, height)
	md.Draw(w)
	require.NoError(t, w.Flush())

	// Capture initial content
	initial := w.String()
	assert.Contains(t, initial, "First")

	// Seek to second header
	ok := md.SeekToAnchor("second-header")
	assert.True(t, ok)

	// Clear and redraw
	w.Reset()
	require.NoError(t, w.Clear(term.Attributes{}))
	md.Draw(w)
	require.NoError(t, w.Flush())

	// Content should now show second header, not first
	afterSeek := w.String()
	assert.Contains(t, afterSeek, "Second")
	assert.NotContains(t, afterSeek, "First", "Old content should be cleared")

	// Seek to third header
	ok = md.SeekToAnchor("third-header")
	assert.True(t, ok)

	// Clear and redraw
	w.Reset()
	require.NoError(t, w.Clear(term.Attributes{}))
	md.Draw(w)
	require.NoError(t, w.Flush())

	afterSeek2 := w.String()
	assert.Contains(t, afterSeek2, "Third")
	assert.NotContains(t, afterSeek2, "First", "Old content should be cleared")
	assert.NotContains(t, afterSeek2, "Second", "Old content should be cleared")
}

func TestDrawWithPartialBlocksVisible(t *testing.T) {
	// Test drawing when blocks are partially visible (offset cuts through a block)
	content := "# Header 1\n\n# Header 2\n\n# Header 3"
	md, err := New(content)
	require.NoError(t, err)

	width, height := 20, 3
	w := term.NewStringWriter(width, height)

	md.Resize(width, height)

	// Scroll to a position where a block is partially visible
	// Headers are 3 lines each (1 blank + 1 content + 1 blank)
	// Scroll 1 line to cut into the first header
	md.SeekDown()

	require.NoError(t, w.Clear(term.Attributes{}))
	md.Draw(w)
	require.NoError(t, w.Flush())

	// The draw should complete without panic and show proper content
	output := w.String()
	assert.NotEmpty(t, output)

	// Scroll more to have multiple partial blocks
	md.SeekDown()
	md.SeekDown()

	w.Reset()
	require.NoError(t, w.Clear(term.Attributes{}))
	md.Draw(w)
	require.NoError(t, w.Flush())

	output2 := w.String()
	assert.NotEmpty(t, output2)
}

func TestLargeSeekJumpClearing(t *testing.T) {
	// Test that large seek jumps properly clear old content
	// This simulates clicking on a link that jumps far in the document
	content := `# Section 1

Paragraph with lots of text here that should be cleared.

# Section 2

More text in section 2 that differs from section 1.

# Section 3

Content for section 3 is different again.

# Section 4

Final section with unique content.`

	md, err := New(content)
	require.NoError(t, err)

	width, height := 40, 5
	w := term.NewStringWriter(width, height)

	// Initial draw
	md.Resize(width, height)
	require.NoError(t, w.Clear(term.Attributes{}))
	md.Draw(w)
	require.NoError(t, w.Flush())

	initial := w.String()
	assert.Contains(t, initial, "Section 1")

	// Jump to Section 4 (simulates clicking an anchor link)
	ok := md.SeekToAnchor("section-4")
	assert.True(t, ok)

	// Clear and redraw
	w.Reset()
	require.NoError(t, w.Clear(term.Attributes{}))
	md.Draw(w)
	require.NoError(t, w.Flush())

	afterJump := w.String()

	// Section 4 should be visible
	assert.Contains(t, afterJump, "Section 4")

	// Old content should NOT be visible - key test for corruption
	assert.NotContains(t, afterJump, "Section 1", "Old content leaked through")
	assert.NotContains(t, afterJump, "Paragraph with lots", "Old content leaked through")

	// Jump back to Section 1
	ok = md.SeekToAnchor("section-1")
	assert.True(t, ok)

	w.Reset()
	require.NoError(t, w.Clear(term.Attributes{}))
	md.Draw(w)
	require.NoError(t, w.Flush())

	backToStart := w.String()
	assert.Contains(t, backToStart, "Section 1")
	assert.NotContains(t, backToStart, "Section 4", "Content from later section leaked through")
}

func TestNestedListRendersWithoutGap(t *testing.T) {
	// Regression test: a nested list followed by a heading should not have
	// a large blank gap between them. Before the fix, Height counted nested
	// items but Draw skipped them, leaving blank rows equal to the nested
	// item count.
	content := "- Parent\n    - Child 1\n    - Child 2\n\n## Heading"
	md, err := New(content)
	require.NoError(t, err)

	width, height := 30, 15
	w := term.NewStringWriter(width, height)
	require.NoError(t, w.Clear(term.Attributes{}))
	md.Resize(width, height)
	md.Draw(w)
	require.NoError(t, w.Flush())

	output := w.String()
	lines := strings.Split(output, "\n")

	childLine := -1
	headingLine := -1
	for i, line := range lines {
		if strings.Contains(line, "Child 1") {
			childLine = i
		}
		if strings.Contains(line, "Heading") {
			headingLine = i
		}
	}

	assert.GreaterOrEqual(t, childLine, 0, "nested item 'Child 1' must be rendered")
	assert.GreaterOrEqual(t, headingLine, 0, "'Heading' must be rendered")

	// The gap between the nested list and the heading should be small:
	// Child 2 is 1 line after Child 1, then list trailing space (1),
	// header above padding (1), heading content. So heading should be
	// at most ~4 lines after Child 1 — not 10+ blank lines.
	if childLine >= 0 && headingLine >= 0 {
		gap := headingLine - childLine
		assert.LessOrEqual(t, gap, 5, "excessive gap between nested list and heading")
	}
}

func TestTableLinkAt(t *testing.T) {
	// Table with a link in a cell.
	content := "| Name | Link |\n|------|------|\n| foo | [bar](http://example.com) |"
	md, err := New(content)
	require.NoError(t, err)
	md.Resize(40, 10)

	// Table layout: top border (y=0), header (y=1), separator (y=2),
	// data row (y=3), bottom border (y=4), spacing (y=5).
	// The link cell is in the second column of the data row (y=3).
	// Column widths: 40 total, 2 columns + 3 separators = 37 usable.
	// Each column ≈ 18-19 chars. Column 0 starts at x=1, column 1
	// starts at x=1+colWidth+1.

	// Find the link in the data row.
	link := md.LinkAt(22, 3)
	require.NotNil(t, link, "should find link in table cell")
	assert.Equal(t, "http://example.com", link.URL)

	// Click on the header row should NOT find a link.
	link = md.LinkAt(22, 1)
	assert.Nil(t, link, "no link in header row")
}

func TestTableWrappedCellLinkAt(t *testing.T) {
	// Table with a link that wraps to a second line within its cell.
	content := "| H |\n|---|\n| aa [link](http://x.co) |"
	md, err := New(content)
	require.NoError(t, err)
	md.Resize(8, 10)

	// Single column width = 8 - 2 borders = 6.
	// Cell "aa link" wraps to:
	//   line 0: "aa "
	//   line 1: "link"
	// Table layout:
	//   y=0: top border
	//   y=1: header
	//   y=2: separator
	//   y=3: data line 0 ("aa ")
	//   y=4: data line 1 ("link")
	//   y=5: bottom border

	// The link "link" is at y=4, starting at x=1 (after left border).
	link := md.LinkAt(1, 4)
	require.NotNil(t, link, "should find link in wrapped table cell")
	assert.Equal(t, "http://x.co", link.URL)

	// Non-link line should not find a link.
	link = md.LinkAt(1, 3)
	assert.Nil(t, link, "no link in non-link wrapped line")
}

func TestComponentHeight(t *testing.T) {
	md, err := New("# Hello\n\nWorld")
	require.NoError(t, err)
	h := md.Height(40)
	assert.Greater(t, h, 0)
	assert.Equal(t, h, md.Height(40))
}

func TestComponentHeightCacheInvalidation(t *testing.T) {
	md, err := New("# Hello\n\nWorld")
	require.NoError(t, err)

	short := md.Height(40)
	require.NoError(t, md.Init("# Hello\n\nWorld\n\nAnd some more paragraphs\n\nAgain"))
	assert.Greater(t, md.Height(40), short, "new content must not reuse the cached height")

	tall := md.Height(40)
	assert.NotEqual(t, tall, md.Height(8), "a different width must not reuse the cache")
	assert.Equal(t, tall, md.Height(40))
}

func TestComponentResponsiveInterface(t *testing.T) {
	md, err := New("# Hello\n\nWorld")
	require.NoError(t, err)
	var _ component.Responsive = md
}

func TestComponentInit(t *testing.T) {
	md, err := New("# Hello\n\nWorld")
	require.NoError(t, err)
	md.Resize(40, 2)

	// Scroll down so offset is non-zero.
	md.SeekDown()
	require.Greater(t, md.SeekOffset(), 0)

	// Init with new content.
	err = md.Init("# New\n\nContent\n\nMore")
	require.NoError(t, err)

	// Offset should be reset.
	assert.Equal(t, 0, md.SeekOffset())

	// Block heights should be recalculated for the existing width.
	assert.Equal(t, len(md.blocks), len(md.blockHeights))
	assert.Greater(t, md.totalHeight, 0)

	// New anchors should be built (old ones gone).
	_, hasOld := md.anchors["hello"]
	assert.False(t, hasOld)
	_, hasNew := md.anchors["new"]
	assert.True(t, hasNew)

	// Drawing should reflect the new content.
	w := term.NewStringWriter(40, 10)
	require.NoError(t, w.Clear(term.Attributes{}))
	md.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), "New")
	assert.NotContains(t, w.String(), "Hello")
}

func TestComponentInitBeforeResize(t *testing.T) {
	md, err := New("# Hello")
	require.NoError(t, err)
	// Don't call Resize — width is 0.
	err = md.Init("# World")
	require.NoError(t, err)
	assert.Nil(t, md.blockHeights)
	assert.Equal(t, 0, md.totalHeight)
}

func TestLinkAtOnSpacingRow(t *testing.T) {
	// Two paragraphs: "Hello" then a link. Each paragraph block has
	// height = content lines + 1 (trailing spacing). A click on the
	// spacing row of the first block should still find the link in
	// the adjacent block below.
	content := "Hello\n\n[click me](http://example.com)"
	md, err := New(content)
	require.NoError(t, err)
	md.Resize(40, 10)

	// Block 0: "Hello"    → height 2 (1 line + 1 spacing), Y=0..1
	// Block 1: "click me" → height 2 (1 line + 1 spacing), Y=2..3
	require.Equal(t, []int{2, 2}, md.BlockHeights())

	// Direct hit on the link text (Y=2, first content row of block 1).
	link := md.LinkAt(0, 2)
	require.NotNil(t, link, "direct click on link row")
	assert.Equal(t, "http://example.com", link.URL)

	// Click on the spacing row of block 0 (Y=1). The next block's
	// first row has the link, so LinkAt should still find it.
	link = md.LinkAt(0, 1)
	require.NotNil(t, link, "click on spacing row above link")
	assert.Equal(t, "http://example.com", link.URL)

	// Click on block 0 content (Y=0) should NOT find a link.
	link = md.LinkAt(0, 0)
	assert.Nil(t, link, "no link on content row of first block")
}

func TestSearchFindsMatches(t *testing.T) {
	md, err := New("hello world hello")
	require.NoError(t, err)
	md.Resize(20, 5)

	md.Search("hello")

	assert.Equal(t, "hello", md.SearchQuery())
	assert.Len(t, md.searchResults, 2)
	assert.Equal(t, 0, md.searchResults[0].From.X)
	assert.Equal(t, 5, md.searchResults[0].To.X)
	assert.Equal(t, 12, md.searchResults[1].From.X)
	assert.Equal(t, 17, md.searchResults[1].To.X)
}

func TestSearchCaseSensitive(t *testing.T) {
	md, err := New("Hello HELLO hello")
	require.NoError(t, err)
	md.Resize(20, 5)

	md.Search("hello")

	assert.Len(t, md.searchResults, 1)
	assert.Equal(t, 12, md.searchResults[0].From.X)
}

func TestSearchClearsOnEmpty(t *testing.T) {
	md, err := New("hello world")
	require.NoError(t, err)
	md.Resize(20, 5)

	md.Search("hello")
	require.Len(t, md.searchResults, 1)

	md.Search("")
	assert.Empty(t, md.searchResults)
	assert.Nil(t, md.searchList)
}

func TestSearchNoMatch(t *testing.T) {
	md, err := New("hello world")
	require.NoError(t, err)
	md.Resize(20, 5)

	md.Search("xyz")

	assert.Empty(t, md.searchResults)
	assert.Nil(t, md.searchList)
}

func TestSearchAcrossBlocks(t *testing.T) {
	md, err := New("apple\n\nbanana\n\napple pie")
	require.NoError(t, err)
	md.Resize(20, 10)

	md.Search("apple")

	assert.Len(t, md.searchResults, 2)
	// First "apple" is in block 0 at Y=0
	assert.Equal(t, 0, md.searchResults[0].From.Y)
	// Second "apple" is in block 2; block heights are 2 each
	assert.Equal(t, 4, md.searchResults[1].From.Y)
}

func TestSeekToNextSearchResult(t *testing.T) {
	// Create a long document with search targets spread out.
	md, err := New("target\n\nfiller\n\nfiller\n\nfiller\n\nfiller\n\ntarget end")
	require.NoError(t, err)
	md.Resize(20, 3)

	md.Search("target")
	require.Len(t, md.searchResults, 2)

	// LocationSlice starts at index 0 (first result). Current is the first.
	loc, hasCurr := md.searchList.Current()
	assert.True(t, hasCurr)
	assert.Equal(t, md.searchResults[0].From, loc.From)

	// First Next() moves to second result and scrolls.
	ok := md.SeekToNextSearchResult()
	assert.True(t, ok)
	loc, _ = md.searchList.Current()
	assert.Equal(t, md.searchResults[1].From, loc.From)

	// Second Next() should return false (end of list).
	ok = md.SeekToNextSearchResult()
	assert.False(t, ok)
}

func TestSeekToPrevSearchResult(t *testing.T) {
	md, err := New("target\n\nfiller\n\nfiller\n\nfiller\n\ntarget end")
	require.NoError(t, err)
	md.Resize(20, 3)

	md.Search("target")
	require.Len(t, md.searchResults, 2)

	// Advance to second result.
	md.SeekToNextSearchResult()

	// Prev should go back to first.
	ok := md.SeekToPrevSearchResult()
	assert.True(t, ok)
	loc, _ := md.searchList.Current()
	assert.Equal(t, md.searchResults[0].From, loc.From)

	// Prev at beginning should return false.
	ok = md.SeekToPrevSearchResult()
	assert.False(t, ok)
}

func TestSeekToSearchResultNoResults(t *testing.T) {
	md, err := New("hello world")
	require.NoError(t, err)
	md.Resize(20, 5)

	assert.False(t, md.SeekToNextSearchResult())
	assert.False(t, md.SeekToPrevSearchResult())
}

func TestSeekToSearchResultScrollsViewport(t *testing.T) {
	// First "target" is visible, second is far off-screen.
	md, err := New("target\n\nfiller\n\nfiller\n\nfiller\n\nfiller\n\ntarget")
	require.NoError(t, err)
	md.Resize(20, 3)

	md.Search("target")
	require.Len(t, md.searchResults, 2)

	// LocationSlice starts at first result (visible at Y=0).
	assert.Equal(t, 0, md.SeekOffset())

	// Next() moves to the second result which is off-screen.
	ok := md.SeekToNextSearchResult()
	assert.True(t, ok)

	// Viewport should have scrolled to show the second result.
	targetY := md.searchResults[1].From.Y
	screenY := targetY - md.SeekOffset()
	assert.GreaterOrEqual(t, screenY, 0)
	assert.Less(t, screenY, md.height)
}

func TestSearchRerunsOnResize(t *testing.T) {
	md, err := New("hello world hello")
	require.NoError(t, err)
	md.Resize(20, 5)

	md.Search("hello")
	require.Len(t, md.searchResults, 2)

	// Resize to narrower width — text wraps, positions change.
	md.Resize(10, 5)
	// Search should have been re-run.
	assert.NotEmpty(t, md.searchResults)
	assert.NotNil(t, md.searchList)
}

func TestSearchRerunsOnInit(t *testing.T) {
	md, err := New("hello world")
	require.NoError(t, err)
	md.Resize(20, 5)

	md.Search("hello")
	require.Len(t, md.searchResults, 1)

	// Init with new content containing the same term.
	err = md.Init("hello there hello again")
	require.NoError(t, err)

	assert.Len(t, md.searchResults, 2)
}

func TestSearchDrawHighlights(t *testing.T) {
	md, err := New("hi")
	require.NoError(t, err)
	md.Resize(10, 3)

	md.Search("hi")
	require.Len(t, md.searchResults, 1)

	w := term.NewStringWriter(10, 3)
	require.NoError(t, w.Clear(term.Attributes{}))
	md.Draw(w)
	require.NoError(t, w.Flush())

	// Verify the text still renders correctly.
	assert.Equal(t, "hi        \n          \n          ", w.String())
}

func TestSearchDoesNotScrollOnVisibleResult(t *testing.T) {
	md, err := New("hello world")
	require.NoError(t, err)
	md.Resize(20, 5)

	md.Search("hello")

	assert.Equal(t, 0, md.SeekOffset())
	md.SeekToNextSearchResult()
	// Result is already visible at Y=0, so offset should stay at 0.
	assert.Equal(t, 0, md.SeekOffset())
}
