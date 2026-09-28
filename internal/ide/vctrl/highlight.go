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
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// highlightBudget bounds the syntax pass. Rendering a diff runs on the
// event loop, so a pathological input opens unhighlighted rather than
// freezing the UI.
const highlightBudget = 250 * time.Millisecond

// Snippet is a run of source lines parsed as one fragment. Rows maps
// each snippet line back to the buffer row it was rendered into, since
// a hunk's lines are contiguous in the fragment but not necessarily in
// the buffer.
type Snippet struct {
	URI  workspaceapi.URI
	Text string
	Rows []int
}

// HighlightSnippets overlays tree-sitter colours on the rendered source
// lines. Every failure mode — no parser, no grammar for the language, a
// slow parse — degrades to an unhighlighted but otherwise correct diff.
// gutter is the width of the "+", "-" or " " column written ahead of
// every content line; snippet text is expected to have it stripped.
func HighlightSnippets(
	ctx context.Context, cells [][]term.Cell,
	snippets []Snippet, parser syntaxapi.Parser, gutter int,
) {
	if parser == nil || len(snippets) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, highlightBudget)
	defer cancel()
	for _, s := range snippets {
		it, err := parser.Highlight(s.URI, s.Text)
		if err != nil {
			continue
		}
		for loc, ok := it.Next(ctx); ok; loc, ok = it.Next(ctx) {
			applyHighlight(cells, s, loc, gutter)
		}
		_ = it.Close()
	}
}

// BoldRow emphasises the first width cells of row.
func BoldRow(cells [][]term.Cell, row, width int) {
	if row < 0 || row >= len(cells) {
		return
	}
	for x := 0; x < width && x < len(cells[row]); x++ {
		cells[row][x].Attrs |= term.AttrBold
	}
}

// applyHighlight paints one snippet-relative location onto the buffer.
// The cell background carries the add/remove tint, so only the
// foreground and text attributes are taken from the syntax location.
func applyHighlight(
	cells [][]term.Cell, s Snippet, loc textapi.Location, gutter int,
) {
	for y := loc.From.Y; y <= loc.To.Y && y < len(s.Rows); y++ {
		row := s.Rows[y]
		if row >= len(cells) {
			continue
		}
		startX, endX := gutter, len(cells[row])
		if y == loc.From.Y {
			startX = loc.From.X + gutter
		}
		if y == loc.To.Y {
			endX = loc.To.X + gutter
		}
		for x := startX; x < endX && x < len(cells[row]); x++ {
			attr := loc.Attr
			attr.Bg = cells[row][x].Bg
			cells[row][x].SetAttributes(attr)
		}
	}
}
