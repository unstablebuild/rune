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

package locationsearch

import (
	"context"
	"io"
	"os"
	"slices"
	"strings"
	"unicode"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
)

// loadPreview parses line as a `path[:line[:col]]` location, opens
// the file via the workspace filesystem, and stores the cell matrix
// for drawing. When a parser is configured the syntax-highlighting
// is computed off the rendering goroutine and applied via schedule.
func (h *Handler) loadPreview(line string) {
	h.prevLine = line
	parsed := parseLocationLineDetailed(h.fs, line)
	if !parsed.ok {
		h.previewCells = nil
		h.highlightTargetLine = false
		return
	}
	uri := parsed.uri
	coords := parsed.coords
	f, err := h.fs.OpenFile(uri.Path(), os.O_RDONLY, 0)
	if err != nil {
		h.previewCells = nil
		h.highlightTargetLine = false
		return
	}
	defer f.Close() //nolint:errcheck
	data, err := io.ReadAll(f)
	if err != nil {
		h.previewCells = nil
		h.highlightTargetLine = false
		return
	}
	content := string(data)
	h.previewCells = term.StringToCells(content)
	for _, row := range h.previewCells {
		h.previewW = max(h.previewW, len(row))
	}
	h.targetLine = coords.Y
	h.targetStartX = coords.X
	h.targetEndX = coords.X
	h.highlightTargetLine = parsed.hasColumn
	if h.parser != nil && h.schedule != nil {
		baseCells := term.CloneCells(h.previewCells)
		fileURI := uri
		go debug.CapturePanicReport(func() {
			h.loadHighlights(fileURI, content, baseCells)
		})
	}
}

func (h *Handler) loadHighlights(
	uri workspaceapi.URI, content string, baseCells [][]term.Cell,
) {
	iter, err := h.parser.Highlight(uri, content)
	if err != nil {
		return
	}
	defer func() { _ = iter.Close() }()
	highlighted := term.CloneCells(baseCells)
	for {
		loc, ok := iter.Next(context.Background())
		if !ok {
			break
		}
		y := loc.From.Y
		if y < 0 || y >= len(highlighted) {
			continue
		}
		row := highlighted[y]
		for x := loc.From.X; x < loc.To.X && x < len(row); x++ {
			row[x].SetAttributes(loc.Attr)
		}
	}
	if err := iter.Err(); err != nil {
		h.log.Warn("highlight iteration", "uri", uri, "err", err)
		return
	}
	h.schedule(func() {
		h.previewCells = highlighted
	})
}

func (h *Handler) drawPreview(w term.Writer, ann Annotation) {
	if h.previewCells == nil {
		return
	}
	targetLine := h.targetLine
	if targetLine >= len(h.previewCells) {
		targetLine = len(h.previewCells) - 1
	}
	if targetLine < 0 {
		targetLine = 0
	}
	startLine := max(0, targetLine-h.previewH/2)
	if startLine+h.previewH > len(h.previewCells) {
		startLine = max(0, len(h.previewCells)-h.previewH)
	}
	// Match innerSpan's horizontal padding so the preview, separator
	// and inner finder share the same left/right margins.
	leftPad := spanHorizontalPad / 2
	rightPad := spanHorizontalPad - leftPad
	contentW := max(0, h.innerW-leftPad-rightPad)
	for row := range h.previewH {
		srcLine := startLine + row
		if srcLine >= len(h.previewCells) {
			break
		}
		highlight := srcLine == targetLine && (h.highlightTargetLine || ann.First > 0)
		cells := h.previewCells[srcLine]
		var emphasized []bool
		if ann.First > 0 && srcLine+1 >= ann.First && srcLine+1 <= ann.Last {
			emphasized = termCells(cells, ann.Terms)
		}
		for x := range contentW {
			var cell term.Cell
			if x < len(cells) {
				cell = cells[x]
			} else {
				cell = term.Cell{Ch: ' ', Width: 1}
			}
			if highlight {
				cell.SetAttributes(term.AttributesUnion(cell.Attributes(), h.cfg.PreviewAttr))
				if h.targetStartX < h.targetEndX &&
					x >= h.targetStartX && x < h.targetEndX {
					cell.Attrs |= term.AttrReverse
				}
			}
			if x < len(emphasized) && emphasized[x] {
				cell.SetAttributes(term.AttributesUnion(cell.Attributes(), h.cfg.TermAttr))
			}
			w.SetCell(term.Coordinates{X: leftPad + x, Y: row}, cell)
		}
	}
}

// termCells reports which of cells spell one of terms, regardless of
// case.
func termCells(cells []term.Cell, terms []string) []bool {
	if len(terms) == 0 {
		return nil
	}
	line := make([]rune, len(cells))
	for i, c := range cells {
		line[i] = unicode.ToLower(c.Ch)
	}
	marked := make([]bool, len(cells))
	for _, t := range terms {
		word := []rune(strings.ToLower(t))
		if len(word) == 0 {
			continue
		}
		for i := 0; i+len(word) <= len(line); i++ {
			if slices.Equal(line[i:i+len(word)], word) {
				for j := range word {
					marked[i+j] = true
				}
			}
		}
	}
	return marked
}

func (h *Handler) drawSeparator(w term.Writer) {
	if h.cfg.SeparatorHeight <= 0 {
		return
	}
	ch := component.FrameCharSetDefault().HorizontalTop
	attr := term.Attributes{Fg: term.ColorGray}
	leftPad := spanHorizontalPad / 2
	rightPad := spanHorizontalPad - leftPad
	contentW := max(0, h.innerW-leftPad-rightPad)
	for sy := range h.cfg.SeparatorHeight {
		y := h.previewH + sy
		for x := range contentW {
			w.SetCell(term.Coordinates{X: leftPad + x, Y: y},
				term.NewCell(ch, 1, attr))
		}
	}
}
