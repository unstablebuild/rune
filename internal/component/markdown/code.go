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
	"context"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idelsp/languages"
)

type codeBlock struct {
	cells  [][]term.Cell
	code   string // source without the trailing newline
	cfg    *Config
	w      int // width from last Height call
	cancel context.CancelFunc
	// copyHovered draws the copy icon with CodeBlockCopyIconHoverAttr.
	copyHovered bool
	// copied draws CodeBlockCopiedIcon instead of the copy icon.
	copied bool
}

var _ block = (*codeBlock)(nil)

func newCodeBlock(language, code string, cfg *Config) *codeBlock {
	cells := term.StringToCells(code)
	// Strip trailing empty line (goldmark includes trailing newline).
	if len(cells) > 0 && len(cells[len(cells)-1]) == 0 {
		cells = cells[:len(cells)-1]
	}

	// Apply base CodeBlock attributes to every cell.
	for y := range cells {
		for x := range cells[y] {
			cells[y][x].SetAttributes(cfg.CodeBlock)
		}
	}

	cb := &codeBlock{cells: cells, code: strings.TrimSuffix(code, "\n"), cfg: cfg}

	if cfg.Parser == nil || language == "" {
		return cb
	}

	ctx, cancel := context.WithCancel(context.Background())
	cb.cancel = cancel

	parser := cfg.Parser
	codeBlock := cfg.CodeBlock
	schedule := cfg.ScheduleNextTick
	go debug.CapturePanicReport(func() {

		hls := collectHighlights(ctx, parser, language, code)
		if len(hls) == 0 {
			return
		}
		schedule(func() {
			applyHighlights(cb.cells, hls, codeBlock)
		})

	})

	return cb
}

// collectHighlights performs I/O by calling Parser.Highlight and
// collects all resulting locations into a slice. The context controls
// cancellation of the iterator consumption.
func collectHighlights(ctx context.Context, parser syntaxapi.Parser, language, code string) []textapi.Location {
	filename := languages.FilenameForLanguage(language)
	uri, err := workspaceapi.ParseURI("file:///" + filename)
	if err != nil {
		return nil
	}
	iter, err := parser.Highlight(uri, code)
	if err != nil {
		return nil
	}
	defer func() { _ = iter.Close() }()

	var hls []textapi.Location
	for hl, ok := iter.Next(ctx); ok; hl, ok = iter.Next(ctx) {
		hls = append(hls, hl)
	}
	return hls
}

func (c *codeBlock) close() {
	if c.cancel != nil {
		c.cancel()
	}
}

// applyHighlights writes highlight attributes into pre-built cells.
// Must only be called on the event-loop thread (or synchronously
// before the codeBlock is used).
func applyHighlights(cells [][]term.Cell, hls []textapi.Location, codeBlock term.Attributes) {
	for _, hl := range hls {
		for y := hl.From.Y; y <= hl.To.Y && y < len(cells); y++ {
			startX := 0
			if y == hl.From.Y {
				startX = hl.From.X
			}
			endX := len(cells[y])
			if y == hl.To.Y {
				endX = hl.To.X
			}
			for x := startX; x < endX && x < len(cells[y]); x++ {
				attr := hl.Attr
				// Preserve the code block background color.
				if codeBlock.Bg != term.ColorDefault {
					attr.Bg = codeBlock.Bg
				}
				cells[y][x].SetAttributes(attr)
			}
		}
	}
}

func (c *codeBlock) Height(width int) int {
	if width <= 0 {
		return 0
	}
	p := c.padding(width)
	lines := 0
	for _, row := range c.cells {
		lines += len(splitRow(row, width-p.Left-p.Right))
	}
	return p.Top + lines + p.Bottom + 1
}

// padding returns the configured padding for a block of the given width,
// without horizontal padding when it would leave no column for code.
func (c *codeBlock) padding(width int) Padding {
	p := c.cfg.CodeBlockPadding
	if width-p.Left-p.Right < 1 {
		p.Left, p.Right = 0, 0
	}
	return p
}

func (c *codeBlock) Resize(width, _ int) {
	c.w = width
}

func (c *codeBlock) Draw(w term.Writer) {
	if c.w <= 0 {
		return
	}
	p := c.padding(c.w)
	codeWidth := c.w - p.Left - p.Right

	if c.cfg.CodeBlock.Bg != term.ColorDefault {
		bgAttr := term.Attributes{Bg: c.cfg.CodeBlock.Bg}
		for y := range c.Height(c.w) - 1 {
			for x := range c.w {
				w.UnionAttributes(term.Coordinates{X: x, Y: y}, bgAttr)
			}
		}
	}

	drawY := p.Top
	for _, row := range c.cells {
		for _, line := range splitRow(row, codeWidth) {
			x := 0
			for _, cell := range line {
				width := max(1, int(cell.Width))
				if x+width > codeWidth {
					break
				}
				w.SetCell(term.Coordinates{X: p.Left + x, Y: drawY}, cell)
				x += width
			}
			drawY++
		}
	}

	if x, _, ok := c.copyIcon(); ok {
		icon, attr := c.cfg.CodeBlockCopyIcon, c.cfg.CodeBlockCopyIconAttr
		switch {
		case c.copied:
			icon, attr = c.cfg.CodeBlockCopiedIcon, c.cfg.CodeBlockCopiedIconAttr
		case c.copyHovered:
			attr = c.cfg.CodeBlockCopyIconHoverAttr
		}
		if c.cfg.CodeBlock.Bg != term.ColorDefault {
			attr.Bg = c.cfg.CodeBlock.Bg
		}
		component.WriteText(w, x, 0, c.w, string(icon), attr)
	}
}

// copyIcon returns the column and width of the copy icon, which sits on
// the block's first row where the right padding starts. The width fits
// both the copy and copied icons so the target does not move on click.
// ok is false when the icon is disabled or the padding has no room for it.
func (c *codeBlock) copyIcon() (x, width int, ok bool) {
	if !c.cfg.CodeBlockCopy {
		return 0, 0, false
	}
	p := c.padding(c.w)
	width = max(textWidth(string(c.cfg.CodeBlockCopyIcon)),
		textWidth(string(c.cfg.CodeBlockCopiedIcon)))
	if p.Top < 1 || p.Right < width {
		return 0, 0, false
	}
	return c.w - p.Right, width, true
}

func (c *codeBlock) Dimensions() (width, height int) {
	p := c.cfg.CodeBlockPadding
	return p.Left + c.maxLineWidth() + p.Right, p.Top + len(c.cells) + p.Bottom + 1
}

func (c *codeBlock) SpanAt(x, y int) (text, url string, ok bool) {
	if c.w <= 0 {
		return
	}
	row, _, ok := c.cellAt(x, y)
	if !ok {
		return
	}
	return cellsToString(row), "", true
}

func (c *codeBlock) CharAt(x, y int) (rune, bool) {
	if c.w <= 0 {
		return 0, false
	}
	_, cell, ok := c.cellAt(x, y)
	if ok {
		return cell.Ch, true
	}
	return 0, false
}

func (c *codeBlock) maxLineWidth() int {
	maxWidth := 0
	for _, row := range c.cells {
		rowWidth := 0
		for _, cell := range row {
			rowWidth += max(1, int(cell.Width))
		}
		if rowWidth > maxWidth {
			maxWidth = rowWidth
		}
	}
	return maxWidth
}

// splitRow slices a source row into the visual lines it occupies at the given
// width. The slices share the row's storage. A cell wider than the remaining
// columns starts the next line instead of straddling the boundary; one wider
// than the whole width gets a line of its own and is clipped when drawn.
func splitRow(row []term.Cell, width int) [][]term.Cell {
	if width <= 0 {
		return nil
	}
	if len(row) == 0 {
		return [][]term.Cell{nil}
	}

	var lines [][]term.Cell
	start, cols := 0, 0
	for i, cell := range row {
		cellWidth := max(1, int(cell.Width))
		if i > start && cols+cellWidth > width {
			lines = append(lines, row[start:i])
			start, cols = i, 0
		}
		cols += cellWidth
	}
	return append(lines, row[start:])
}

func (c *codeBlock) cellAt(x, y int) ([]term.Cell, term.Cell, bool) {
	p := c.padding(c.w)
	codeWidth := c.w - p.Left - p.Right
	x, y = x-p.Left, y-p.Top
	if x < 0 || x >= codeWidth || y < 0 {
		return nil, term.Cell{}, false
	}

	rowY := 0
	for _, row := range c.cells {
		lines := splitRow(row, codeWidth)
		if y < rowY+len(lines) {
			col := 0
			for _, cell := range lines[y-rowY] {
				cellWidth := max(1, int(cell.Width))
				if x >= col && x < col+cellWidth {
					return row, cell, true
				}
				col += cellWidth
			}
			return nil, term.Cell{}, false
		}
		rowY += len(lines)
	}

	return nil, term.Cell{}, false
}

func cellsToString(row []term.Cell) string {
	runes := make([]rune, len(row))
	for i, c := range row {
		runes[i] = c.Ch
	}
	return string(runes)
}
