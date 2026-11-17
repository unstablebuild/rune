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

package vtesnap

import "github.com/unstablebuild/rune-go-sdk/term"

// maxRun bounds one run-length prefix. Rows wider than this simply use
// more runs.
const maxRun = 0xFFFF

// maxRowCells bounds a decoded row. Run-length encoding means a row's
// declared width says nothing about how many payload bytes back it, so
// the width needs a limit of its own.
const maxRowCells = 1 << 20

// maxPageCells bounds the cells of a decoded page for the same reason:
// rows each within maxRowCells would otherwise let a payload of a few
// kilobytes expand into gigabytes. Encoders page far fewer rows.
const maxPageCells = 1 << 22

// decodeRowCapacity caps the initial allocation for a row so a large
// declared width cannot reserve memory before any run is validated.
const decodeRowCapacity = 4096

// Terminal rows are overwhelmingly repeated blanks, so cells are
// stored as runs rather than one fixed record each. A cell carrying
// combining marks or an underline colour is never merged into a run
// because those live behind a pointer.
func (p *Page) encode() []byte {
	var e enc
	e.u8(uint8(p.Kind))
	e.u32(uint32(len(p.Rows)))
	for _, row := range p.Rows {
		encodeRow(&e, row)
	}
	return e.b
}

func encodeRow(e *enc, row []term.Cell) {
	e.u32(uint32(len(row)))
	for i := 0; i < len(row); {
		run := 1
		if mergeable(row[i]) {
			for i+run < len(row) && run < maxRun &&
				mergeable(row[i+run]) && row[i+run] == row[i] {
				run++
			}
		}
		e.u16(uint16(run))
		encodeCell(e, row[i])
		i += run
	}
}

func mergeable(c term.Cell) bool { return c.Extra == nil }

// EncodePage encodes a page payload on its own, for transports that
// carry pages as individual frames rather than inside one snapshot
// blob.
func EncodePage(kind ScreenKind, rows [][]term.Cell) []byte {
	page := Page{Kind: kind, Rows: rows}
	return page.encode()
}

// DecodePage is the inverse of EncodePage.
func DecodePage(payload []byte) (ScreenKind, [][]term.Cell, error) {
	page, err := decodePage(payload)
	if err != nil {
		return 0, nil, err
	}
	return page.Kind, page.Rows, nil
}

func encodeCell(e *enc, c term.Cell) {
	e.rune(c.Ch)
	e.u32(uint32(c.Fg))
	e.u32(uint32(c.Bg))
	e.u16(uint16(c.Attrs))
	e.u8(c.Width)
	e.u8(c.Bytes)
	combining := c.CombiningRunes()
	if len(combining) > 0xFF {
		combining = combining[:0xFF]
	}
	e.u8(uint8(len(combining)))
	for _, r := range combining {
		e.rune(r)
	}
	e.u32(uint32(c.UnderlineColor()))
}

func decodePage(payload []byte) (Page, error) {
	d := dec{b: payload}
	var p Page
	p.Kind = ScreenKind(d.u8())
	// A row costs at least its own length prefix.
	if n := d.count(4); n > 0 {
		p.Rows = make([][]term.Cell, n)
		room := maxPageCells
		for i := range p.Rows {
			p.Rows[i] = decodeRow(&d, room)
			if d.err != nil {
				return Page{}, d.err
			}
			room -= len(p.Rows[i])
		}
	}
	if err := d.done(); err != nil {
		return Page{}, err
	}
	if err := p.Kind.validate(); err != nil {
		return Page{}, err
	}
	return p, nil
}

// decodeRow decodes a row of at most room cells.
func decodeRow(d *dec, room int) []term.Cell {
	width := int(d.u32())
	if d.err != nil || width == 0 {
		return nil
	}
	if width > maxRowCells {
		d.fail("row of %d cells exceeds the %d cell limit", width, maxRowCells)
		return nil
	}
	if width > room {
		d.fail("row of %d cells exceeds the %d cells left in the page", width, room)
		return nil
	}
	row := make([]term.Cell, 0, min(width, decodeRowCapacity))
	for len(row) < width {
		run := int(d.u16())
		cell := decodeCell(d)
		if d.err != nil {
			return nil
		}
		if run == 0 || len(row)+run > width {
			d.fail("run of %d overflows row of %d", run, width)
			return nil
		}
		for range run {
			row = append(row, cell)
		}
	}
	return row
}

func decodeCell(d *dec) term.Cell {
	var c term.Cell
	c.Ch = d.rune()
	c.Fg = term.Color(d.u32())
	c.Bg = term.Color(d.u32())
	c.Attrs = term.AttrMask(d.u16())
	c.Width = d.u8()
	c.Bytes = d.u8()
	if n := int(d.u8()); n > 0 {
		if d.err != nil {
			return c
		}
		combining := make([]rune, n)
		for i := range combining {
			combining[i] = d.rune()
		}
		if d.err == nil {
			c.SetCombining(combining)
		}
	}
	if underline := term.Color(d.u32()); d.err == nil {
		c.SetUnderlineColor(underline)
	}
	return c
}
