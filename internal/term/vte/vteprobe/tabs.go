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

import "github.com/unstablebuild/rune-go-sdk/term"

// expandTabs returns the visual rendering of line according to tabstop:
// tab characters are expanded to the next multiple of tabstop. The
// returned string contains only the visual cells; downstream code uses
// graphemecluster widths to map back to runes.
func expandTabs(line string, tabstop int) string {
	if tabstop <= 0 {
		tabstop = 1
	}
	if !containsTab(line) {
		return line
	}
	out := make([]rune, 0, len(line))
	col := 0
	for _, r := range line {
		if r != '\t' {
			out = append(out, r)
			col++
			continue
		}
		pad := tabstop - (col % tabstop)
		for k := 0; k < pad; k++ {
			out = append(out, ' ')
		}
		col += pad
	}
	return string(out)
}

func containsTab(line string) bool {
	for i := 0; i < len(line); i++ {
		if line[i] == '\t' {
			return true
		}
	}
	return false
}

// visualToRawCol converts a visual rune-cell offset on the expanded
// rendering of line (with tabstop) back to a 1-based rune column in the
// raw file content. A tab in the raw file always counts as a single
// rune column even though it expands to multiple visual cells.
func visualToRawColCells(line []term.Cell, runeOffset int, tabstop int) int {
	if tabstop <= 0 {
		tabstop = 1
	}
	if runeOffset <= 0 {
		return 1
	}
	visual := 0
	rawCol := 0
	for _, cell := range line {
		rawCol++
		if cell.Ch == '\t' {
			pad := tabstop - (visual % tabstop)
			if pad <= 0 {
				pad = tabstop
			}
			if visual+pad > runeOffset {
				return rawCol
			}
			visual += pad
			continue
		}
		visual += cellWidth(cell)
		if visual > runeOffset {
			return rawCol
		}
	}
	// Past end of line: 1-based column equals rawCol+1 to allow cursor
	// past the last char (common in vim insert mode).
	return rawCol + 1
}

// RawToVisualCol returns the 0-based visual rune-cell offset that the
// 0-based raw rune column rawCol resolves to under tabstop. Tabs
// expand to the next tabstop multiple; combining-only clusters
// contribute zero visual cells but still consume one raw column. Out
// of range rawCol values are clamped to the line's visual width.
func RawToVisualCol(line []term.Cell, rawCol, tabstop int) int {
	if tabstop <= 0 {
		tabstop = 1
	}
	if rawCol <= 0 {
		return 0
	}
	visual := 0
	raw := 0
	for _, cell := range line {
		if raw >= rawCol {
			break
		}
		if cell.Ch == '\t' {
			pad := tabstop - (visual % tabstop)
			if pad <= 0 {
				pad = tabstop
			}
			visual += pad
		} else {
			visual += cellWidth(cell)
		}
		raw++
	}
	return visual
}

func cellWidth(cell term.Cell) int {
	width := int(cell.Width)
	if width < 1 {
		return 1
	}
	return width
}

func lineRuneLen(line []term.Cell) int {
	n := 0
	for _, cell := range line {
		n += 1 + len(cell.CombiningRunes())
	}
	return n
}
