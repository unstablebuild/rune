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

package vtegraphics

import (
	"github.com/unstablebuild/rune-go-sdk/term"
)

// PlaceholderChar is the codepoint whose cells show images (spec §9).
const PlaceholderChar = '\U0010EEEE'

// rowColumnDiacritics is kitty's gen/rowcolumn-diacritics.txt in order:
// the combining mark at index n encodes the number n.
var rowColumnDiacritics = [...]rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F,
	0x0346, 0x034A, 0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357,
	0x035B, 0x0363, 0x0364, 0x0365, 0x0366, 0x0367, 0x0368, 0x0369,
	0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F, 0x0483, 0x0484,
	0x0485, 0x0486, 0x0487, 0x0592, 0x0593, 0x0594, 0x0595, 0x0597,
	0x0598, 0x0599, 0x059C, 0x059D, 0x059E, 0x059F, 0x05A0, 0x05A1,
	0x05A8, 0x05A9, 0x05AB, 0x05AC, 0x05AF, 0x05C4, 0x0610, 0x0611,
	0x0612, 0x0613, 0x0614, 0x0615, 0x0616, 0x0617, 0x0657, 0x0658,
	0x0659, 0x065A, 0x065B, 0x065D, 0x065E, 0x06D6, 0x06D7, 0x06D8,
	0x06D9, 0x06DA, 0x06DB, 0x06DC, 0x06DF, 0x06E0, 0x06E1, 0x06E2,
	0x06E4, 0x06E7, 0x06E8, 0x06EB, 0x06EC, 0x0730, 0x0732, 0x0733,
	0x0735, 0x0736, 0x073A, 0x073D, 0x073F, 0x0740, 0x0741, 0x0743,
	0x0745, 0x0747, 0x0749, 0x074A, 0x07EB, 0x07EC, 0x07ED, 0x07EE,
	0x07EF, 0x07F0, 0x07F1, 0x07F3, 0x0816, 0x0817, 0x0818, 0x0819,
	0x081B, 0x081C, 0x081D, 0x081E, 0x081F, 0x0820, 0x0821, 0x0822,
	0x0823, 0x0825, 0x0826, 0x0827, 0x0829, 0x082A, 0x082B, 0x082C,
	0x082D, 0x0951, 0x0953, 0x0954, 0x0F82, 0x0F83, 0x0F86, 0x0F87,
	0x135D, 0x135E, 0x135F, 0x17DD, 0x193A, 0x1A17, 0x1A75, 0x1A76,
	0x1A77, 0x1A78, 0x1A79, 0x1A7A, 0x1A7B, 0x1A7C, 0x1B6B, 0x1B6D,
	0x1B6E, 0x1B6F, 0x1B70, 0x1B71, 0x1B72, 0x1B73, 0x1CD0, 0x1CD1,
	0x1CD2, 0x1CDA, 0x1CDB, 0x1CE0, 0x1DC0, 0x1DC1, 0x1DC3, 0x1DC4,
	0x1DC5, 0x1DC6, 0x1DC7, 0x1DC8, 0x1DC9, 0x1DCB, 0x1DCC, 0x1DD1,
	0x1DD2, 0x1DD3, 0x1DD4, 0x1DD5, 0x1DD6, 0x1DD7, 0x1DD8, 0x1DD9,
	0x1DDA, 0x1DDB, 0x1DDC, 0x1DDD, 0x1DDE, 0x1DDF, 0x1DE0, 0x1DE1,
	0x1DE2, 0x1DE3, 0x1DE4, 0x1DE5, 0x1DE6, 0x1DFE, 0x20D0, 0x20D1,
	0x20D4, 0x20D5, 0x20D6, 0x20D7, 0x20DB, 0x20DC, 0x20E1, 0x20E7,
	0x20E9, 0x20F0, 0x2CEF, 0x2CF0, 0x2CF1, 0x2DE0, 0x2DE1, 0x2DE2,
	0x2DE3, 0x2DE4, 0x2DE5, 0x2DE6, 0x2DE7, 0x2DE8, 0x2DE9, 0x2DEA,
	0x2DEB, 0x2DEC, 0x2DED, 0x2DEE, 0x2DEF, 0x2DF0, 0x2DF1, 0x2DF2,
	0x2DF3, 0x2DF4, 0x2DF5, 0x2DF6, 0x2DF7, 0x2DF8, 0x2DF9, 0x2DFA,
	0x2DFB, 0x2DFC, 0x2DFD, 0x2DFE, 0x2DFF, 0xA66F, 0xA67C, 0xA67D,
	0xA6F0, 0xA6F1, 0xA8E0, 0xA8E1, 0xA8E2, 0xA8E3, 0xA8E4, 0xA8E5,
	0xA8E6, 0xA8E7, 0xA8E8, 0xA8E9, 0xA8EA, 0xA8EB, 0xA8EC, 0xA8ED,
	0xA8EE, 0xA8EF, 0xA8F0, 0xA8F1, 0xAAB0, 0xAAB2, 0xAAB3, 0xAAB7,
	0xAAB8, 0xAABE, 0xAABF, 0xAAC1, 0xFE20, 0xFE21, 0xFE22, 0xFE23,
	0xFE24, 0xFE25, 0xFE26, 0x10A0F, 0x10A38, 0x1D185, 0x1D186, 0x1D187,
	0x1D188, 0x1D189, 0x1D1AA, 0x1D1AB, 0x1D1AC, 0x1D1AD, 0x1D242, 0x1D243,
	0x1D244,
}

var diacriticIndex = func() map[rune]uint32 {
	m := make(map[rune]uint32, len(rowColumnDiacritics))
	for i, r := range rowColumnDiacritics {
		m[r] = uint32(i)
	}
	return m
}()

// DiacriticNumber returns the number a row/column diacritic encodes,
// 1-based so that 0 means "not a row/column diacritic".
func DiacriticNumber(r rune) uint32 {
	if n, ok := diacriticIndex[r]; ok {
		return n + 1
	}
	return 0
}

// RowColumnDiacritic returns the combining mark that encodes n, for
// clients and tests; ok is false when n is out of range.
func RowColumnDiacritic(n int) (rune, bool) {
	if n < 0 || n >= len(rowColumnDiacritics) {
		return 0, false
	}
	return rowColumnDiacritics[n], true
}

// PlaceholderRun is a horizontal run of placeholder cells that show
// consecutive columns of one row of a virtual placement (spec §9).
type PlaceholderRun struct {
	// Row and Col are the screen cell of the run's first placeholder.
	Row, Col int
	// Len is the number of cells in the run.
	Len int
	// ImageID and PlacementID identify the virtual placement; a zero
	// PlacementID selects the image's first virtual placement.
	ImageID, PlacementID uint32
	// ImgRow and ImgCol are the 0-based image cell shown by the first
	// cell of the run.
	ImgRow, ImgCol int
}

// placeholderCell decodes one cell. Values are 1-based, 0 meaning
// unknown, as in kitty's screen_render_line_graphics.
type placeholderCell struct {
	isPlaceholder bool
	idLow24       uint32
	placementID   uint32
	idHigh8       uint32
	row, col      uint32
}

func decodePlaceholder(c term.Cell) placeholderCell {
	if c.Ch != PlaceholderChar {
		return placeholderCell{}
	}
	p := placeholderCell{isPlaceholder: true}
	if c.Fg.Valid() {
		p.idLow24 = uint32(c.Fg) & 0xffffff
	}
	if u := c.UnderlineColor(); u.Valid() {
		p.placementID = uint32(u) & 0xffffff
	}
	marks := c.CombiningRunes()
	if len(marks) > 0 {
		p.row = DiacriticNumber(marks[0])
	}
	if len(marks) > 1 {
		p.col = DiacriticNumber(marks[1])
	}
	if len(marks) > 2 {
		p.idHigh8 = DiacriticNumber(marks[2])
	}
	return p
}

// ScanPlaceholders appends the placeholder runs of one screen row to
// runs, applying the run inference rules of spec §9.
func ScanPlaceholders(runs []PlaceholderRun, row int, cells []term.Cell) []PlaceholderRun {
	var prev placeholderCell
	runLen := 0
	flush := func(end int) {
		if runLen == 0 {
			return
		}
		runs = append(runs, PlaceholderRun{
			Row:         row,
			Col:         end - runLen,
			Len:         runLen,
			ImageID:     prev.idLow24 | (prev.idHigh8-1)<<24,
			PlacementID: prev.placementID,
			ImgRow:      int(prev.row) - 1,
			ImgCol:      int(prev.col) - int(runLen),
		})
		runLen = 0
	}
	for i, c := range cells {
		cur := decodePlaceholder(c)
		continues := runLen > 0 && cur.isPlaceholder &&
			cur.idLow24 == prev.idLow24 && cur.placementID == prev.placementID &&
			(cur.row == 0 || cur.row == prev.row) &&
			(cur.col == 0 || cur.col == prev.col+1) &&
			(cur.idHigh8 == 0 || cur.idHigh8 == prev.idHigh8)
		if continues {
			runLen++
			cur.row = max(prev.row, 1)
			cur.col = prev.col + 1
			cur.idHigh8 = max(prev.idHigh8, 1)
		} else {
			flush(i)
			if cur.isPlaceholder {
				runLen = 1
				cur.row = max(cur.row, 1)
				cur.col = max(cur.col, 1)
				cur.idHigh8 = max(cur.idHigh8, 1)
			}
		}
		prev = cur
	}
	flush(len(cells))
	return runs
}
