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

package vctrl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

const (
	highlightFg   = term.ColorRed
	highlightTint = term.ColorNavy
)

// stubParser serves canned locations for each snippet. Only Highlight is
// reachable, so the rest of syntaxapi.Parser stays unimplemented.
type stubParser struct {
	syntaxapi.Parser
	locs     []textapi.Location
	byText   map[string][]textapi.Location
	err      error
	failText string
	block    bool
	closeErr error

	texts    []string
	uris     []workspaceapi.URI
	closed   int
	budget   time.Duration
	deadline bool
}

func (p *stubParser) Highlight(uri workspaceapi.URI, content string) (
	iterator.Iterator[textapi.Location], error,
) {
	p.texts = append(p.texts, content)
	p.uris = append(p.uris, uri)
	if p.err != nil && (p.failText == "" || p.failText == content) {
		return nil, p.err
	}
	locs := p.locs
	if p.byText != nil {
		locs = p.byText[content]
	}
	return &stubIterator{parser: p, locs: locs}, nil
}

type stubIterator struct {
	parser *stubParser
	locs   []textapi.Location
}

func (it *stubIterator) Next(ctx context.Context) (textapi.Location, bool) {
	if d, ok := ctx.Deadline(); ok {
		it.parser.deadline = true
		it.parser.budget = time.Until(d)
	}
	if it.parser.block {
		<-ctx.Done()
	}
	if ctx.Err() != nil || len(it.locs) == 0 {
		return textapi.Location{}, false
	}
	loc := it.locs[0]
	it.locs = it.locs[1:]
	return loc, true
}

func (it *stubIterator) Err() error { return nil }

func (it *stubIterator) Close() error {
	it.parser.closed++
	return it.parser.closeErr
}

// highlightCells lays out lines as tinted cells, so a test can tell the
// syntax foreground the overlay writes from the background it must
// leave alone.
func highlightCells(lines ...string) [][]term.Cell {
	cells := make([][]term.Cell, len(lines))
	for y, line := range lines {
		row := make([]term.Cell, 0, len(line))
		for _, r := range line {
			row = append(row, term.Cell{Ch: r, Bg: highlightTint})
		}
		cells[y] = row
	}
	return cells
}

// highlightMask renders which cells the overlay repainted: '#' where the
// syntax foreground landed, '.' everywhere else.
func highlightMask(cells [][]term.Cell) []string {
	mask := make([]string, len(cells))
	for y, row := range cells {
		var b strings.Builder
		for _, c := range row {
			if c.Fg == highlightFg {
				b.WriteByte('#')
				continue
			}
			b.WriteByte('.')
		}
		mask[y] = b.String()
	}
	return mask
}

func highlightAt(fromX, fromY, toX, toY int) textapi.Location {
	return textapi.Location{
		From: term.Coordinates{X: fromX, Y: fromY},
		To:   term.Coordinates{X: toX, Y: toY},
		Attr: term.Attributes{Fg: highlightFg, Attrs: term.AttrItalic},
	}
}

func TestHighlightSnippets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lines  []string
		rows   []int
		locs   []textapi.Location
		gutter int
		want   []string
	}{
		{
			name:   "span shifts past the gutter",
			lines:  []string{" alpha", " beta"},
			rows:   []int{0, 1},
			locs:   []textapi.Location{highlightAt(0, 0, 5, 0)},
			gutter: 1,
			want:   []string{".#####", "....."},
		},
		{
			name:   "no gutter starts at column zero",
			lines:  []string{"alpha"},
			rows:   []int{0},
			locs:   []textapi.Location{highlightAt(0, 0, 5, 0)},
			gutter: 0,
			want:   []string{"#####"},
		},
		{
			name:  "multi row span covers the rows between whole",
			lines: []string{" alpha", " beta", " gamma"},
			rows:  []int{0, 1, 2},
			locs:  []textapi.Location{highlightAt(2, 0, 2, 2)},
			// The middle row has no bound on either side, so it runs
			// from the gutter to the end of the line.
			gutter: 1,
			want:   []string{"...###", ".####", ".##..."},
		},
		{
			name:   "rows the snippet skips stay untouched",
			lines:  []string{" one", " two", " three", " four", " five"},
			rows:   []int{1, 4},
			locs:   []textapi.Location{highlightAt(0, 0, 3, 1)},
			gutter: 1,
			want:   []string{"....", ".###", "......", ".....", ".###."},
		},
		{
			name:   "location rows past the snippet are dropped",
			lines:  []string{" ab"},
			rows:   []int{0},
			locs:   []textapi.Location{highlightAt(0, 0, 2, 3)},
			gutter: 1,
			want:   []string{".##"},
		},
		{
			name:   "rows past the buffer are skipped",
			lines:  []string{" ab"},
			rows:   []int{0, 7},
			locs:   []textapi.Location{highlightAt(0, 0, 2, 1)},
			gutter: 1,
			want:   []string{".##"},
		},
		{
			name:   "an empty row absorbs a span",
			lines:  []string{" ab", "", " cd"},
			rows:   []int{0, 1, 2},
			locs:   []textapi.Location{highlightAt(0, 0, 2, 2)},
			gutter: 1,
			want:   []string{".##", "", ".##"},
		},
		{
			name:   "columns past the end of a row clamp",
			lines:  []string{" ab"},
			rows:   []int{0},
			locs:   []textapi.Location{highlightAt(0, 0, 99, 0)},
			gutter: 1,
			want:   []string{".##"},
		},
		{
			name:   "a start past the end of a row paints nothing",
			lines:  []string{" ab"},
			rows:   []int{0},
			locs:   []textapi.Location{highlightAt(50, 0, 60, 0)},
			gutter: 1,
			want:   []string{"..."},
		},
		{
			name:   "an empty span paints nothing",
			lines:  []string{" abc"},
			rows:   []int{0},
			locs:   []textapi.Location{highlightAt(2, 0, 2, 0)},
			gutter: 1,
			want:   []string{"...."},
		},
		{
			name:   "a reversed span paints nothing",
			lines:  []string{" abc"},
			rows:   []int{0},
			locs:   []textapi.Location{highlightAt(3, 0, 1, 0)},
			gutter: 1,
			want:   []string{"...."},
		},
		{
			name:   "a snippet with no rows paints nothing",
			lines:  []string{" abc"},
			rows:   nil,
			locs:   []textapi.Location{highlightAt(0, 0, 3, 0)},
			gutter: 1,
			want:   []string{"...."},
		},
		{
			name:  "several locations share a row",
			lines: []string{" one two"},
			rows:  []int{0},
			locs: []textapi.Location{
				highlightAt(0, 0, 3, 0), highlightAt(4, 0, 7, 0),
			},
			gutter: 1,
			want:   []string{".###.###"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cells := highlightCells(tc.lines...)
			parser := &stubParser{locs: tc.locs}
			HighlightSnippets(t.Context(), cells,
				[]Snippet{{Text: strings.Join(tc.lines, "\n"), Rows: tc.rows}},
				parser, tc.gutter)
			assert.Equal(t, tc.want, highlightMask(cells))
			assert.Equal(t, 1, parser.closed, "the iterator must be closed")
		})
	}
}

func TestHighlightSnippetsKeepsTint(t *testing.T) {
	cells := highlightCells(" alpha")
	cells[0][3].Attrs |= term.AttrBold
	parser := &stubParser{locs: []textapi.Location{highlightAt(0, 0, 5, 0)}}
	HighlightSnippets(t.Context(), cells,
		[]Snippet{{Text: "alpha", Rows: []int{0}}}, parser, 1)

	for x, c := range cells[0] {
		assert.Equalf(t, highlightTint, c.Bg,
			"cell %d must keep the diff tint the syntax pass runs over", x)
	}
	assert.Equal(t, term.ColorDefault, cells[0][0].Fg,
		"the gutter column is not part of the parsed source")
	assert.NotZero(t, cells[0][1].Attrs&term.AttrItalic,
		"the location's text attributes must be applied")
	assert.Zero(t, cells[0][3].Attrs&term.AttrBold,
		"the location's attributes replace whatever was there")
}

func TestHighlightSnippetsNoop(t *testing.T) {
	t.Run("no parser", func(t *testing.T) {
		cells := highlightCells(" alpha")
		HighlightSnippets(t.Context(), cells,
			[]Snippet{{Text: "alpha", Rows: []int{0}}}, nil, 1)
		assert.Equal(t, []string{"......"}, highlightMask(cells))
	})

	t.Run("no snippets", func(t *testing.T) {
		parser := &stubParser{locs: []textapi.Location{highlightAt(0, 0, 5, 0)}}
		cells := highlightCells(" alpha")
		HighlightSnippets(t.Context(), cells, nil, parser, 1)
		assert.Empty(t, parser.texts, "the parser must not be consulted")
		assert.Equal(t, []string{"......"}, highlightMask(cells))
	})

	t.Run("no cells", func(t *testing.T) {
		parser := &stubParser{locs: []textapi.Location{highlightAt(0, 0, 5, 0)}}
		HighlightSnippets(t.Context(), nil,
			[]Snippet{{Text: "alpha", Rows: []int{0}}}, parser, 1)
		assert.Equal(t, 1, parser.closed)
	})
}

// TestHighlightSnippetsParserError pins the degradation contract: a
// snippet whose language has no grammar leaves the diff readable and
// does not stop the snippets after it.
func TestHighlightSnippetsParserError(t *testing.T) {
	cells := highlightCells(" alpha", " beta")
	parser := &stubParser{
		err:      errors.New("no grammar"),
		failText: "alpha",
		byText: map[string][]textapi.Location{
			"beta": {highlightAt(0, 0, 4, 0)},
		},
	}
	HighlightSnippets(t.Context(), cells, []Snippet{
		{Text: "alpha", Rows: []int{0}},
		{Text: "beta", Rows: []int{1}},
	}, parser, 1)

	assert.Equal(t, []string{"......", ".####"}, highlightMask(cells))
	assert.Equal(t, []string{"alpha", "beta"}, parser.texts)
	assert.Equal(t, 1, parser.closed, "a failed Highlight yields no iterator")
}

func TestHighlightSnippetsIgnoresCloseError(t *testing.T) {
	cells := highlightCells(" alpha")
	parser := &stubParser{
		locs:     []textapi.Location{highlightAt(0, 0, 5, 0)},
		closeErr: errors.New("close failed"),
	}
	HighlightSnippets(t.Context(), cells,
		[]Snippet{{Text: "alpha", Rows: []int{0}}}, parser, 1)
	assert.Equal(t, []string{".#####"}, highlightMask(cells))
}

func TestHighlightSnippetsPassesURI(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///repo/main.go")
	require.NoError(t, err)
	parser := &stubParser{}
	HighlightSnippets(t.Context(), highlightCells(" alpha"),
		[]Snippet{{URI: uri, Text: "alpha", Rows: []int{0}}}, parser, 1)
	assert.Equal(t, []workspaceapi.URI{uri}, parser.uris,
		"snippets must be parsed as the file's language, not as a diff")
}

// TestHighlightSnippetsBudget pins the bound on the syntax pass: it runs
// on the event loop, so a parse that never finishes has to be abandoned.
func TestHighlightSnippetsBudget(t *testing.T) {
	parser := &stubParser{locs: []textapi.Location{highlightAt(0, 0, 5, 0)}}
	HighlightSnippets(t.Context(), highlightCells(" alpha"),
		[]Snippet{{Text: "alpha", Rows: []int{0}}}, parser, 1)
	require.True(t, parser.deadline, "the pass must be time bounded")
	assert.Positive(t, parser.budget)
	assert.LessOrEqual(t, parser.budget, highlightBudget)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cells := highlightCells(" alpha")
	blocked := &stubParser{
		block: true,
		locs:  []textapi.Location{highlightAt(0, 0, 5, 0)},
	}
	HighlightSnippets(ctx, cells,
		[]Snippet{{Text: "alpha", Rows: []int{0}}}, blocked, 1)
	assert.Equal(t, []string{"......"}, highlightMask(cells),
		"an abandoned parse must leave the diff unhighlighted")
	assert.Equal(t, 1, blocked.closed, "the iterator must still be closed")
}

func TestBoldRow(t *testing.T) {
	for _, tc := range []struct {
		name  string
		row   int
		width int
		want  []string
	}{
		{"partial width", 0, 2, []string{"##.", "..."}},
		{"whole row", 1, 3, []string{"...", "###"}},
		{"width past the row", 0, 99, []string{"###", "..."}},
		{"zero width", 0, 0, []string{"...", "..."}},
		{"negative width", 0, -1, []string{"...", "..."}},
		{"negative row", -1, 3, []string{"...", "..."}},
		{"row past the buffer", 5, 3, []string{"...", "..."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cells := highlightCells("abc", "def")
			BoldRow(cells, tc.row, tc.width)
			got := make([]string, len(cells))
			for y, row := range cells {
				var b strings.Builder
				for _, c := range row {
					if c.Attrs&term.AttrBold != 0 {
						b.WriteByte('#')
						continue
					}
					b.WriteByte('.')
				}
				got[y] = b.String()
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBoldRowKeepsAttributes(t *testing.T) {
	cells := highlightCells("abc")
	cells[0][0].Fg = highlightFg
	cells[0][0].Attrs |= term.AttrItalic
	BoldRow(cells, 0, 3)
	assert.Equal(t, highlightFg, cells[0][0].Fg)
	assert.Equal(t, highlightTint, cells[0][0].Bg)
	assert.NotZero(t, cells[0][0].Attrs&term.AttrItalic,
		"bold must be added to the existing attributes, not replace them")
}
