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
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestParseHeaders(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		input    string
		expected int
		level    int
	}{
		{
			name:     "H1",
			input:    "# Header 1",
			expected: 1,
			level:    1,
		},
		{
			name:     "H2",
			input:    "## Header 2",
			expected: 1,
			level:    2,
		},
		{
			name:     "H3",
			input:    "### Header 3",
			expected: 1,
			level:    3,
		},
		{
			name:     "H4",
			input:    "#### Header 4",
			expected: 1,
			level:    4,
		},
		{
			name:     "H5",
			input:    "##### Header 5",
			expected: 1,
			level:    5,
		},
		{
			name:     "H6",
			input:    "###### Header 6",
			expected: 1,
			level:    6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, err := parse(tt.input, &cfg, images{}, false)
			require.NoError(t, err)
			require.Len(t, blocks, tt.expected)

			header, ok := blocks[0].(*headerBlock)
			require.True(t, ok, "expected headerBlock")
			assert.Equal(t, tt.level, header.level)
		})
	}
}

func TestParseParagraphs(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		input    string
		expected int
		text     string
	}{
		{
			name:     "simple paragraph",
			input:    "Hello world",
			expected: 1,
			text:     "Hello world",
		},
		{
			name:     "paragraph with bold",
			input:    "Hello **bold** world",
			expected: 1,
			text:     "Hello bold world",
		},
		{
			name:     "paragraph with italic",
			input:    "Hello *italic* world",
			expected: 1,
			text:     "Hello italic world",
		},
		{
			name:     "paragraph with code",
			input:    "Hello `code` world",
			expected: 1,
			text:     "Hello code world",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, err := parse(tt.input, &cfg, images{}, false)
			require.NoError(t, err)
			require.Len(t, blocks, tt.expected)

			para, ok := blocks[0].(*paragraphBlock)
			require.True(t, ok, "expected paragraphBlock")
			assert.Equal(t, tt.text, para.content.String())
		})
	}
}

func TestParseCodeBlocks(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		input    string
		expected int
		language string
		code     string
	}{
		{
			name:     "fenced code block",
			input:    "```\ncode here\n```",
			expected: 1,
			language: "",
			code:     "code here\n",
		},
		{
			name:     "fenced code block with language",
			input:    "```go\nfunc main() {}\n```",
			expected: 1,
			language: "go",
			code:     "func main() {}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, err := parse(tt.input, &cfg, images{}, false)
			require.NoError(t, err)
			require.Len(t, blocks, tt.expected)

			code, ok := blocks[0].(*codeBlock)
			require.True(t, ok, "expected codeBlock")
			// Verify the cells contain the expected code text.
			var b strings.Builder
			for _, row := range code.cells {
				b.WriteString(cellsToString(row))
				b.WriteByte('\n')
			}
			assert.Equal(t, tt.code, b.String())
		})
	}
}

func TestParseLists(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name    string
		input   string
		ordered bool
		items   int
	}{
		{
			name:    "unordered list",
			input:   "- item 1\n- item 2\n- item 3",
			ordered: false,
			items:   3,
		},
		{
			name:    "ordered list",
			input:   "1. item 1\n2. item 2\n3. item 3",
			ordered: true,
			items:   3,
		},
		{
			name:    "task list",
			input:   "- [ ] unchecked\n- [x] checked",
			ordered: false,
			items:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, err := parse(tt.input, &cfg, images{}, false)
			require.NoError(t, err)
			require.Len(t, blocks, 1)

			list, ok := blocks[0].(*listBlock)
			require.True(t, ok, "expected listBlock")
			assert.Equal(t, tt.ordered, list.ordered)
			assert.Len(t, list.items, tt.items)
		})
	}
}

func TestParseBlockquotes(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name     string
		input    string
		expected int
		content  int
	}{
		{
			name:     "simple blockquote",
			input:    "> quoted text",
			expected: 1,
			content:  1,
		},
		{
			name:     "multi-line blockquote",
			input:    "> line 1\n> line 2",
			expected: 1,
			content:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, err := parse(tt.input, &cfg, images{}, false)
			require.NoError(t, err)
			require.Len(t, blocks, tt.expected)

			quote, ok := blocks[0].(*blockquoteBlock)
			require.True(t, ok, "expected blockquoteBlock")
			assert.Len(t, quote.content, tt.content)
		})
	}
}

func TestParseHorizontalRules(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name  string
		input string
	}{
		{name: "dashes", input: "---"},
		{name: "asterisks", input: "***"},
		{name: "underscores", input: "___"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, err := parse(tt.input, &cfg, images{}, false)
			require.NoError(t, err)
			require.Len(t, blocks, 1)

			_, ok := blocks[0].(*horizontalRuleBlock)
			assert.True(t, ok, "expected horizontalRuleBlock")
		})
	}
}

func TestParseTables(t *testing.T) {
	cfg := DefaultConfig()
	input := `| Header 1 | Header 2 |
| --- | --- |
| Cell 1 | Cell 2 |
| Cell 3 | Cell 4 |`

	blocks, err := parse(input, &cfg, images{}, false)
	require.NoError(t, err)
	require.Len(t, blocks, 1)

	table, ok := blocks[0].(*tableBlock)
	require.True(t, ok, "expected tableBlock")
	assert.Len(t, table.header, 2)
	assert.Len(t, table.rows, 2)
}

func TestParseInlineFormatting(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name  string
		input string
		style inlineStyle
	}{
		{
			name:  "bold",
			input: "**bold**",
			style: styleBold,
		},
		{
			name:  "italic",
			input: "*italic*",
			style: styleItalic,
		},
		{
			name:  "code",
			input: "`code`",
			style: styleCode,
		},
		{
			name:  "strikethrough",
			input: "~~strike~~",
			style: styleStrikethrough,
		},
		{
			name:  "link",
			input: "[link](http://example.com)",
			style: styleLink,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, err := parse(tt.input, &cfg, images{}, false)
			require.NoError(t, err)
			require.Len(t, blocks, 1)

			para, ok := blocks[0].(*paragraphBlock)
			require.True(t, ok, "expected paragraphBlock")
			require.NotEmpty(t, para.content)
			assert.Equal(t, tt.style, para.content[0].style)
		})
	}
}

func TestParseComplex(t *testing.T) {
	cfg := DefaultConfig()
	input := `# Title

This is a paragraph with **bold** and *italic* text.

## Section 1

- Item 1
- Item 2

> A blockquote

---

| Col A | Col B |
| ----- | ----- |
| 1     | 2     |
`
	blocks, err := parse(input, &cfg, images{}, false)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(blocks), 6)
}

func TestParseImages(t *testing.T) {
	type item struct {
		alt, dest, url string
		caption        string
		size           imageSize
		resolved       bool
		gap, newline   bool
	}
	tests := []struct {
		name     string
		input    string
		noImages bool
		// wantItems is the image block the input parses to, if any.
		wantItems []item
		// wantRun is otherwise the content of the paragraph or the first
		// list item the input parses to.
		wantRun textRun
	}{
		{
			name:      "a lone image",
			input:     "![logo](logo.png)",
			wantItems: []item{{alt: "logo", dest: "logo.png", url: "logo.png", resolved: true}},
		},
		{
			name:  "a linked image",
			input: "[![logo](logo.png)](https://example.com)",
			wantItems: []item{
				{alt: "logo", dest: "logo.png", url: "https://example.com", resolved: true},
			},
		},
		{
			name:  "alt text keeps its plain text",
			input: "![*my* `logo`](logo.png)",
			wantItems: []item{
				{alt: "my logo", dest: "logo.png", url: "logo.png", resolved: true},
			},
		},
		{
			name:  "adjacent images",
			input: "![a](a.png)![b](b.png)",
			wantItems: []item{
				{alt: "a", dest: "a.png", url: "a.png", resolved: true},
				{alt: "b", dest: "b.png", url: "b.png", resolved: true},
			},
		},
		{
			name:  "images split by a space",
			input: "![a](a.png) [![b](b.png)](https://example.com)",
			wantItems: []item{
				{alt: "a", dest: "a.png", url: "a.png", resolved: true},
				{alt: "b", dest: "b.png", url: "https://example.com", resolved: true, gap: true},
			},
		},
		{
			name:  "images split by a soft break",
			input: "![a](a.png)\n![b](b.png)",
			wantItems: []item{
				{alt: "a", dest: "a.png", url: "a.png", resolved: true},
				{alt: "b", dest: "b.png", url: "b.png", resolved: true, gap: true},
			},
		},
		{
			name:  "images split by a hard break",
			input: "![a](a.png)\\\n![b](b.png)",
			wantItems: []item{
				{alt: "a", dest: "a.png", url: "a.png", resolved: true},
				{alt: "b", dest: "b.png", url: "b.png", resolved: true, newline: true},
			},
		},
		{
			name:  "destinations not shown keep their alt text",
			input: "![a](http://example.com/a.png) ![b](b.png)",
			wantItems: []item{
				{alt: "a", dest: "http://example.com/a.png", url: "http://example.com/a.png"},
				{alt: "b", dest: "b.png", url: "b.png", resolved: true, gap: true},
			},
		},
		{
			name:      "an empty alt",
			input:     "![](a.png)",
			wantItems: []item{{dest: "a.png", url: "a.png", resolved: true}},
		},
		{
			name:  "a caption from the title",
			input: `![a](a.png "Acme &amp; \"Co\"")`,
			wantItems: []item{
				{alt: "a", dest: "a.png", url: "a.png", caption: `Acme & "Co"`, resolved: true},
			},
		},
		{
			name:  "an escaped entity reference in a caption is literal",
			input: `![a](a.png "\&amp; &lt;b&gt; &#65;\\")`,
			wantItems: []item{
				{alt: "a", dest: "a.png", url: "a.png", caption: `&amp; <b> A\`, resolved: true},
			},
		},
		{
			name:  "a caption is on one line",
			input: "![a](a.png \"Acme\n\tCo  Inc\")",
			wantItems: []item{
				{alt: "a", dest: "a.png", url: "a.png", caption: "Acme Co Inc", resolved: true},
			},
		},
		{
			name:  "a size from an attribute block",
			input: "![a](a.png){cols=16 rows=8 maintain_aspect_ratio=false}",
			wantItems: []item{{
				alt: "a", dest: "a.png", url: "a.png",
				size: imageSize{cols: 16, rows: 8, stretch: true}, resolved: true,
			}},
		},
		{
			name:  "a linked image with a caption and a size",
			input: `[![a](a.png "Acme"){rows=2 maintain_aspect_ratio=true}](https://example.com) ![b](b.png){cols=3}`,
			wantItems: []item{
				{
					alt: "a", dest: "a.png", url: "https://example.com", caption: "Acme",
					size: imageSize{rows: 2}, resolved: true,
				},
				{
					alt: "b", dest: "b.png", url: "b.png",
					size: imageSize{cols: 3}, resolved: true, gap: true,
				},
			},
		},
		{
			name:  "sizes that are not whole cells are left to the picture",
			input: `![a](a.png){cols=0 rows=2.5} ![b](b.png){cols=-1 rows=1001} ![c](c.png){cols="4" maintain_aspect_ratio="false"}`,
			wantItems: []item{
				{alt: "a", dest: "a.png", url: "a.png", resolved: true},
				{alt: "b", dest: "b.png", url: "b.png", resolved: true, gap: true},
				{alt: "c", dest: "c.png", url: "c.png", resolved: true, gap: true},
			},
		},
		{
			name:  "an inline image hides its attribute block",
			input: "See ![a](a.png){cols=4} here",
			wantRun: textRun{
				{text: "See "},
				{text: "a", style: styleLink, url: "a.png"},
				{text: " here"},
			},
		},
		{
			name:  "an attribute block apart from an image is text",
			input: "![a](a.png) {cols=4}",
			wantRun: textRun{
				{text: "a", style: styleLink, url: "a.png"},
				{text: " "},
				{text: "{cols=4}"},
			},
		},
		{
			name:  "an unclosed attribute block is text",
			input: "![a](a.png){cols=4",
			wantRun: textRun{
				{text: "a", style: styleLink, url: "a.png"},
				{text: "{cols=4"},
			},
		},
		{
			name:  "an attribute block without an image is text",
			input: "a {cols=4 maintain_aspect_ratio=false}",
			wantRun: textRun{
				{text: "a {cols=4 maintain_"},
				{text: "aspect_"},
				{text: "ratio=false}"},
			},
		},
		{
			name:  "an image with text is a link to the image",
			input: "See ![a](a.png) here",
			wantRun: textRun{
				{text: "See "},
				{text: "a", style: styleLink, url: "a.png"},
				{text: " here"},
			},
		},
		{
			name:  "an image linked with text is a link to the link",
			input: "[![a](a.png) docs](https://example.com)",
			wantRun: textRun{
				{text: "a", style: styleLink, url: "https://example.com"},
				{text: " docs", style: styleLink, url: "https://example.com"},
			},
		},
		{
			// Resolving would leak the images of a paragraph that is not
			// drawn as an image block.
			name:  "images followed by text are not resolved",
			input: "![a](a.png) [![b](b.png)](https://example.com) here",
			wantRun: textRun{
				{text: "a", style: styleLink, url: "a.png"},
				{text: " "},
				{text: "b", style: styleLink, url: "https://example.com"},
				{text: " here"},
			},
		},
		{
			name:  "an inline image with an empty alt shows its destination",
			input: "See ![](a.png)",
			wantRun: textRun{
				{text: "See "},
				{text: "a.png", style: styleLink, url: "a.png"},
			},
		},
		{
			name:    "an image in a list item is a link to the image",
			input:   "- ![a](a.png)",
			wantRun: textRun{{text: "a", style: styleLink, url: "a.png"}},
		},
		{
			name:     "without Images an image is its plain alt text",
			input:    "![a](a.png)",
			noImages: true,
			wantRun:  textRun{{text: "a"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			imgs := images{
				fs:          pictureFS{},
				storage:     storagestub.NewInMemoryService(),
				interrupter: term.NopInterrupter(),
			}
			blocks, err := parse(tt.input, &cfg, imgs, !tt.noImages)
			require.NoError(t, err)
			require.Len(t, blocks, 1)

			switch blk := blocks[0].(type) {
			case *imageBlock:
				defer blk.close()
				items := make([]item, len(blk.items))
				for i, it := range blk.items {
					items[i] = item{
						alt: it.alt, dest: it.dest, url: it.url,
						caption: it.caption, size: it.size,
						resolved: it.img != nil, gap: it.gap, newline: it.newline,
					}
				}
				assert.Equal(t, tt.wantItems, items)
			case *paragraphBlock:
				assert.Nil(t, tt.wantItems, "parsed to a paragraph")
				assert.Equal(t, tt.wantRun, blk.content)
			case *listBlock:
				assert.Nil(t, tt.wantItems, "parsed to a list")
				assert.Equal(t, tt.wantRun, blk.items[0].content)
			default:
				t.Fatalf("unexpected block %T", blk)
			}
		})
	}
}
