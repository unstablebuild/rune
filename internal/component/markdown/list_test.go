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
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// framedScreen is a term.StringWriter whose String wraps every row in
// '|', so an expected screen spells out the viewport's width and keeps
// its blank rows, leading ones included.
type framedScreen struct{ *term.StringWriter }

func (s framedScreen) String() string {
	rows := strings.Split(s.StringWriter.String(), "\n")
	for i, row := range rows {
		rows[i] = "|" + row + "|"
	}
	return strings.Join(rows, "\n")
}

type listStep struct {
	do   func(*Component, framedScreen) // nil draws the list as it is
	want string
}

func resizeList(width, height int) func(*Component, framedScreen) {
	return func(md *Component, w framedScreen) {
		md.Resize(width, height)
		w.Resize(width, height)
	}
}

func scrollList(offset int) func(*Component, framedScreen) {
	return func(md *Component, _ framedScreen) { md.SeekTo(offset) }
}

// TestListDraw draws lists through the Component, the way a viewer
// does. The Component places each block on the rows the previous ones
// reserved, so the paragraph after a list lands in the wrong place if
// the list measures itself differently from how it draws.
func TestListDraw(t *testing.T) {
	tests := []struct {
		name          string
		src           string
		width, height int
		steps         []listStep
	}{
		{
			name:  "a tight list draws its items on consecutive rows",
			src:   "- one\n- two\n\nafter",
			width: 12, height: 5,
			steps: []listStep{{want: `
|• one       |
|• two       |
|            |
|after       |
|            |`}},
		},
		{
			name:  "a loose list leaves a blank row between items",
			src:   "- one\n\n- two\n\n- three\n\nafter",
			width: 12, height: 8,
			steps: []listStep{{want: `
|• one       |
|            |
|• two       |
|            |
|• three     |
|            |
|after       |
|            |`}},
		},
		{
			name:  "one blank line between two items loosens the whole list",
			src:   "- one\n- two\n\n- three\n\nafter",
			width: 12, height: 8,
			steps: []listStep{{want: `
|• one       |
|            |
|• two       |
|            |
|• three     |
|            |
|after       |
|            |`}},
		},
		{
			name:  "a loose item's sub-list starts below a blank row",
			src:   "- one\n\n  - sub\n\n- two\n\nafter",
			width: 12, height: 8,
			steps: []listStep{{want: `
|• one       |
|            |
|  • sub     |
|            |
|• two       |
|            |
|after       |
|            |`}},
		},
		{
			name:  "a loose sub-list spaces its own items but not its parent's",
			src:   "- one\n  - a\n\n  - b\n- two\n\nafter",
			width: 12, height: 8,
			steps: []listStep{{want: `
|• one       |
|  • a       |
|            |
|  • b       |
|• two       |
|            |
|after       |
|            |`}},
		},
		{
			name:  "a wrapped loose item leaves the gap after its last row",
			src:   "- a long first item\n\n- two\n\nafter",
			width: 12, height: 7,
			steps: []listStep{{want: `
|• a long    |
|  first item|
|            |
|• two       |
|            |
|after       |
|            |`}},
		},
		{
			name:  "a loose task list",
			src:   "- [ ] todo\n\n- [x] done\n\nafter",
			width: 12, height: 6,
			steps: []listStep{{want: `
|☐ todo      |
|            |
|☑ done      |
|            |
|after       |
|            |`}},
		},
		{
			name:  "wide text wraps at the item column without splitting a character",
			src:   "- 中文字符\n\n- 中\n\nafter",
			width: 7, height: 7,
			steps: []listStep{{want: `
|• 中 文  |
|  字 符  |
|       |
|• 中    |
|       |
|after  |
|       |`}},
		},
		{
			name:  "the viewport clips a loose list and scrolls through its gaps",
			src:   "- one\n\n- two\n\n- three\n\nafter",
			width: 12, height: 3,
			steps: []listStep{
				{want: `
|• one       |
|            |
|• two       |`},
				{do: scrollList(1), want: `
|            |
|• two       |
|            |`},
				{do: scrollList(5), want: `
|            |
|after       |
|            |`},
			},
		},
		{
			name:  "resizing rewraps the items and moves what follows",
			src:   "- one two\n\n- three\n\nafter",
			width: 12, height: 7,
			steps: []listStep{
				{want: `
|• one two   |
|            |
|• three     |
|            |
|after       |
|            |
|            |`},
				{do: resizeList(7, 7), want: `
|• one  |
|  two  |
|       |
|• three|
|       |
|after  |
|       |`},
				{do: resizeList(5, 7), want: `
|• one|
|  two|
|     |
|• thr|
|  ee |
|     |
|after|`},
				{do: resizeList(12, 7), want: `
|• one two   |
|            |
|• three     |
|            |
|after       |
|            |
|            |`},
			},
		},
		{
			name:  "a list with no room for its text draws nothing until there is",
			src:   "- one\n\n- two\n\nafter",
			width: 2, height: 4,
			steps: []listStep{
				{want: `
|af|
|te|
|r |
|  |`},
				{do: resizeList(3, 9), want: `
|• o|
|  n|
|  e|
|   |
|• t|
|  w|
|  o|
|   |
|aft|`},
			},
		},
		{
			name:  "an ordered marker is followed by a blank",
			src:   "1. one\n2. two\n\nafter",
			width: 12, height: 5,
			steps: []listStep{{want: `
|1. one      |
|2. two      |
|            |
|after       |
|            |`}},
		},
		{
			name:  "markers reaching ten pad the shorter ones",
			src:   "9. nine\n10. ten\n\nafter",
			width: 12, height: 5,
			steps: []listStep{{want: `
|9.  nine    |
|10. ten     |
|            |
|after       |
|            |`}},
		},
		{
			name:  "a list starting past ten sizes the column from its last marker",
			src:   "99. a\n100. b",
			width: 12, height: 3,
			steps: []listStep{{want: `
|99.  a      |
|100. b      |
|            |`}},
		},
		{
			name:  "a list may start at zero",
			src:   "0. zero\n1. one",
			width: 12, height: 3,
			steps: []listStep{{want: `
|0. zero     |
|1. one      |
|            |`}},
		},
		{
			name:  "wrapped text hangs under the item column",
			src:   "8. a long item that wraps\n9. b\n10. c\n\nafter",
			width: 14, height: 8,
			steps: []listStep{{want: `
|8.  a long    |
|    item that |
|    wraps     |
|9.  b         |
|10. c         |
|              |
|after         |
|              |`}},
		},
		{
			name:  "a nested list starts at its parent's item column",
			src:   "9. a\n10. b\n    - sub\n\nafter",
			width: 12, height: 6,
			steps: []listStep{{want: `
|9.  a       |
|10. b       |
|    • sub   |
|            |
|after       |
|            |`}},
		},
		{
			name:  "a loose ordered item's sub-list starts below a blank row",
			src:   "1. one\n\n   - sub\n\n2. two\n\nafter",
			width: 12, height: 8,
			steps: []listStep{{want: `
|1. one      |
|            |
|   • sub    |
|            |
|2. two      |
|            |
|after       |
|            |`}},
		},
		{
			name:  "wide text after a padded marker",
			src:   "9. 中\n10. 中文字\n\nafter",
			width: 9, height: 6,
			steps: []listStep{{want: `
|9.  中    |
|10. 中 文  |
|    字    |
|         |
|after    |
|         |`}},
		},
		{
			name:  "checkboxes replace the numbers of an ordered task list",
			src:   "1. [ ] todo\n2. [x] done",
			width: 12, height: 3,
			steps: []listStep{{want: `
|☐ todo      |
|☑ done      |
|            |`}},
		},
		{
			name:  "a checkbox lines up with the numbers around it",
			src:   "9. [ ] todo\n10. done",
			width: 12, height: 3,
			steps: []listStep{{want: `
|☐   todo    |
|10. done    |
|            |`}},
		},
		{
			name:  "a marker wider than the viewport hides the list until it fits",
			src:   "1000. a",
			width: 6, height: 2,
			steps: []listStep{
				{want: `
|      |
|      |`},
				{do: resizeList(7, 2), want: `
|1000. a|
|       |`},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := New(tt.src)
			require.NoError(t, err)
			md.Resize(tt.width, tt.height)
			w := framedScreen{term.NewStringWriter(tt.width, tt.height)}
			cases := make([]comptest.TestCase, len(tt.steps))
			for i, s := range tt.steps {
				cases[i].Expected = s.want
				if s.do != nil {
					cases[i].Action = func() { s.do(md, w) }
				}
			}
			comptest.TestComponent(t, md, w, cases)
		})
	}
}

// TestListDimensions covers the size a list asks for when nothing
// wraps it, which a floating viewer sizes itself to: every row it
// draws, including the gaps of a loose list and its trailing blank row.
func TestListDimensions(t *testing.T) {
	tests := []struct {
		name          string
		src           string
		width, height int
	}{
		{name: "tight", src: "- one\n- two", width: 5, height: 3},
		{name: "loose", src: "- one\n\n- three", width: 7, height: 4},
		{name: "loose with a sub-list", src: "- one\n\n  - sub\n\n- two",
			width: 7, height: 6},
		{name: "loose sub-list in a tight list",
			src: "- one\n  - a\n\n  - b\n- two", width: 5, height: 6},
		{name: "wide text", src: "- 中文", width: 6, height: 2},
		{name: "markers reaching ten", src: "9. nine\n10. ten", width: 8, height: 3},
		{name: "unwrapped long item",
			src: "8. a long item that wraps\n9. b\n10. c", width: 26, height: 4},
		{name: "ordered task list", src: "1. [ ] todo\n2. [x] done",
			width: 6, height: 3},
		{name: "checkbox among numbers", src: "9. [ ] todo\n10. done",
			width: 8, height: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := New(tt.src)
			require.NoError(t, err)
			width, height := md.Dimensions()
			assert.Equal(t, tt.width, width, "width")
			assert.Equal(t, tt.height, height, "height")
		})
	}
}

// TestListCopyAndLinks covers hit-testing a drawn list: copying it
// gives back the text as drawn, and a click lands on the link drawn
// under the pointer, not on the marker, the blank past it or a gap.
func TestListCopyAndLinks(t *testing.T) {
	type probe struct {
		x, y int
		url  string // "" expects no link
	}
	tests := []struct {
		name   string
		src    string
		width  int
		copied string
		links  []probe
	}{
		{name: "bullets", src: "- one\n- two", width: 12,
			copied: "• one\n• two\n"},
		{name: "task boxes", src: "- [ ] todo\n- [x] done", width: 12,
			copied: "☐ todo\n☑ done\n"},
		{name: "wrapped item keeps the space it broke at", src: "- one two", width: 7,
			copied: "• one \ntwo\n"},
		{name: "tight sub-list", src: "- one\n  - sub", width: 12,
			copied: "• one\n• sub\n"},
		{name: "link in an item", src: "- [one](u1) two", width: 12,
			copied: "• one two\n",
			links:  []probe{{x: 0, y: 0}, {x: 2, y: 0, url: "u1"}, {x: 5, y: 0}}},
		{name: "loose items", src: "- one\n\n- [two](u2)\n\nafter", width: 12,
			copied: "• one\n\n• two\n\nafter\n",
			links:  []probe{{x: 2, y: 1}, {x: 2, y: 2, url: "u2"}}},
		{name: "loose sub-list", src: "- one\n\n  - [sub](u)\n\n- two", width: 12,
			copied: "• one\n\n• sub\n\n• two\n",
			links:  []probe{{x: 4, y: 1}, {x: 4, y: 2, url: "u"}, {x: 4, y: 3}}},
		{name: "padded markers", src: "9. nine\n10. [ten](u)", width: 12,
			copied: "9.  nine\n10. ten\n",
			links:  []probe{{x: 3, y: 1}, {x: 4, y: 1, url: "u"}}},
		{name: "ordered task list", src: "1. [ ] todo", width: 12,
			copied: "☐ todo\n"},
		{name: "sub-list under a padded marker", src: "9. a\n10. b\n    - [sub](u)",
			width:  12,
			copied: "9.  a\n10. b\n• sub\n",
			links:  []probe{{x: 5, y: 2}, {x: 6, y: 2, url: "u"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := New(tt.src)
			require.NoError(t, err)
			md.Resize(tt.width, 40)
			assert.Equal(t, tt.copied, md.TextRange(
				term.Coordinates{}, term.Coordinates{Y: md.totalHeight}))
			for _, p := range tt.links {
				link := md.LinkAt(p.x, p.y)
				if p.url == "" {
					assert.Nil(t, link, "link at (%d, %d)", p.x, p.y)
					continue
				}
				if assert.NotNil(t, link, "link at (%d, %d)", p.x, p.y) {
					assert.Equal(t, p.url, link.URL, "link at (%d, %d)", p.x, p.y)
				}
			}
		})
	}
}
