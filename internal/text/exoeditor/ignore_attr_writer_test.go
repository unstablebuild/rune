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

package exoeditor

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// setCellRecorder captures every SetCell call so the test can assert
// which Cell payload reached the underlying writer through
// ignoreAttrWriter.
type setCellRecorder struct {
	calls []setCellCall
}

type setCellCall struct {
	pos  term.Coordinates
	cell term.Cell
}

func (w *setCellRecorder) SetCell(pos term.Coordinates, c term.Cell) {
	w.calls = append(w.calls, setCellCall{pos: pos, cell: c})
}

func (w *setCellRecorder) UnionAttributes(term.Coordinates, term.Attributes) {}
func (w *setCellRecorder) Context() context.Context                          { return context.Background() }
func (w *setCellRecorder) DrawImage(term.Image) bool                         { return false }

func TestIgnoreAttrWriterSetCellPreservesBgAndReverse(t *testing.T) {
	red := term.NewColor(255, 0, 0)
	blue := term.NewColor(0, 0, 255)

	tests := []struct {
		name     string
		in       term.Attributes
		wantAttr term.AttrMask
		wantBg   term.Color
	}{
		{
			name:     "reverse preserved on its own",
			in:       term.Attributes{Attrs: term.AttrReverse},
			wantAttr: term.AttrReverse,
		},
		{
			name:     "reverse preserved alongside other attrs",
			in:       term.Attributes{Fg: red, Bg: blue, Attrs: term.AttrReverse | term.AttrBold | term.AttrItalic | term.AttrUnderline | term.AttrBlink},
			wantAttr: term.AttrReverse,
			wantBg:   blue,
		},
		{
			name:     "bg preserved without reverse, other attrs dropped",
			in:       term.Attributes{Fg: red, Bg: blue, Attrs: term.AttrBold | term.AttrItalic | term.AttrUnderline | term.AttrBlink | term.AttrDim | term.AttrStrikeThrough},
			wantAttr: 0,
			wantBg:   blue,
		},
		{
			name:     "zero attributes stay zero",
			in:       term.Attributes{},
			wantAttr: 0,
		},
		{
			name:     "fg dropped, bg preserved",
			in:       term.Attributes{Fg: red, Bg: blue},
			wantAttr: 0,
			wantBg:   blue,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := &setCellRecorder{}
			w := ignoreAttrWriter{Writer: rec}

			pos := term.Coordinates{X: 3, Y: 7}
			in := term.NewCell('x', 0, tc.in)
			w.SetCell(pos, in)

			if assert.Len(t, rec.calls, 1) {
				got := rec.calls[0]
				assert.Equal(t, pos, got.pos)
				assert.Equal(t, in.Ch, got.cell.Ch)
				assert.Equal(t, tc.wantAttr, got.cell.Attrs)
				assert.Equal(t, term.Color(0), got.cell.Fg)
				assert.Equal(t, tc.wantBg, got.cell.Bg)
			}
		})
	}
}
