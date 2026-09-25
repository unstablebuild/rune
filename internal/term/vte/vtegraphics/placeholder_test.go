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
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestDiacriticNumber(t *testing.T) {
	assert.Equal(t, uint32(1), DiacriticNumber(0x0305))
	assert.Equal(t, uint32(2), DiacriticNumber(0x030D))
	assert.Equal(t, uint32(3), DiacriticNumber(0x030E))
	assert.Equal(t, uint32(4), DiacriticNumber(0x0310))
	assert.Equal(t, uint32(5), DiacriticNumber(0x0312))
	assert.Equal(t, uint32(6), DiacriticNumber(0x033D))
	assert.Equal(t, uint32(297), DiacriticNumber(0x1D244))
	assert.Equal(t, uint32(0), DiacriticNumber(0x0301), "not in the table")
	assert.Equal(t, uint32(0), DiacriticNumber('a'))
	r, ok := RowColumnDiacritic(296)
	assert.True(t, ok)
	assert.Equal(t, rune(0x1D244), r)
	_, ok = RowColumnDiacritic(297)
	assert.False(t, ok)
}

// placeholder builds a placeholder cell for image id with the given
// 0-based row/column diacritics; a negative index omits the mark.
func placeholder(id uint32, row, col, high int) term.Cell {
	c := term.Cell{Ch: PlaceholderChar, Width: 1}
	c.Fg = term.Color(id&0xffffff) | term.ColorValid | term.ColorIsRGB
	var marks []rune
	for _, n := range []int{row, col, high} {
		if n < 0 {
			break
		}
		r, _ := RowColumnDiacritic(n)
		marks = append(marks, r)
	}
	c.SetCombining(marks)
	return c
}

func TestScanPlaceholders(t *testing.T) {
	blank := term.Cell{Ch: ' ', Width: 1}
	tests := []struct {
		name  string
		cells []term.Cell
		want  []PlaceholderRun
	}{
		{
			name:  "explicit rows and columns",
			cells: []term.Cell{placeholder(7, 0, 0, -1), placeholder(7, 0, 1, -1), placeholder(7, 0, 2, -1)},
			want:  []PlaceholderRun{{Row: 3, Col: 0, Len: 3, ImageID: 7, ImgRow: 0, ImgCol: 0}},
		},
		{
			name:  "columns inferred from the first cell",
			cells: []term.Cell{blank, placeholder(7, 1, 4, -1), placeholder(7, -1, -1, -1), placeholder(7, 1, -1, -1)},
			want:  []PlaceholderRun{{Row: 3, Col: 1, Len: 3, ImageID: 7, ImgRow: 1, ImgCol: 4}},
		},
		{
			name:  "no diacritics at all defaults to the top-left",
			cells: []term.Cell{placeholder(7, -1, -1, -1), placeholder(7, -1, -1, -1)},
			want:  []PlaceholderRun{{Row: 3, Col: 0, Len: 2, ImageID: 7}},
		},
		{
			name:  "a column gap starts a new run",
			cells: []term.Cell{placeholder(7, 0, 0, -1), placeholder(7, 0, 5, -1)},
			want: []PlaceholderRun{
				{Row: 3, Col: 0, Len: 1, ImageID: 7},
				{Row: 3, Col: 1, Len: 1, ImageID: 7, ImgCol: 5},
			},
		},
		{
			name:  "a different image starts a new run",
			cells: []term.Cell{placeholder(7, 0, 0, -1), placeholder(8, 0, 1, -1), blank},
			want: []PlaceholderRun{
				{Row: 3, Col: 0, Len: 1, ImageID: 7},
				{Row: 3, Col: 1, Len: 1, ImageID: 8, ImgCol: 1},
			},
		},
		{
			name:  "high byte of the id from the third diacritic",
			cells: []term.Cell{placeholder(0x123456, 0, 0, 2), placeholder(0x123456, 0, 1, -1)},
			want:  []PlaceholderRun{{Row: 3, Col: 0, Len: 2, ImageID: 0x02123456}},
		},
		{
			name:  "palette colour carries an 8-bit id",
			cells: []term.Cell{{Ch: PlaceholderChar, Fg: term.Color(42) | term.ColorValid, Width: 1}},
			want:  []PlaceholderRun{{Row: 3, Col: 0, Len: 1, ImageID: 42}},
		},
		{
			name:  "placement id from the underline colour",
			cells: []term.Cell{withPlacement(placeholder(7, 0, 0, -1), 9), withPlacement(placeholder(7, 0, 1, -1), 9)},
			want:  []PlaceholderRun{{Row: 3, Col: 0, Len: 2, ImageID: 7, PlacementID: 9}},
		},
		{
			name:  "a different placement id starts a new run",
			cells: []term.Cell{withPlacement(placeholder(7, 0, 0, -1), 9), withPlacement(placeholder(7, 0, 1, -1), 10)},
			want: []PlaceholderRun{
				{Row: 3, Col: 0, Len: 1, ImageID: 7, PlacementID: 9},
				{Row: 3, Col: 1, Len: 1, ImageID: 7, PlacementID: 10, ImgCol: 1},
			},
		},
		{
			name:  "text only",
			cells: []term.Cell{blank, {Ch: 'x', Width: 1}},
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ScanPlaceholders(nil, 3, tt.cells))
		})
	}
}

func withPlacement(c term.Cell, id uint32) term.Cell {
	c.SetUnderlineColor(term.Color(id&0xffffff) | term.ColorValid | term.ColorIsRGB)
	return c
}

func TestPlaceholderImages(t *testing.T) {
	cell := CellSize{Width: 10, Height: 20}
	s := NewStorage()
	exec(t, s, nil, "a=t,i=7,s=40,v=20", make([]byte, 40*20*4))
	view := View{Width: 80, Height: 24, Cell: cell}

	runs := []PlaceholderRun{{Row: 5, Col: 10, Len: 3, ImageID: 7, ImgRow: 1, ImgCol: 1}}
	assert.Empty(t, s.Visible(view, runs), "no virtual placement, nothing drawn")

	handleAt(t, s, &Cursor{}, cell, "a=p,i=7,U=1,c=8,r=2", nil)
	images := s.Visible(view, runs)
	require.Len(t, images, 1)
	img := images[0]
	assert.Equal(t, term.Coordinates{X: 9, Y: 4}, img.Pos, "box anchored so the run shows its own cells")
	assert.Equal(t, 8, img.Width)
	assert.Equal(t, 2, img.Height)
	assert.Equal(t, term.ImageFitContain, img.Fit)
	assert.Equal(t, image.Rect(10, 5, 13, 6), img.Clip)
	assert.Equal(t, s.byClientID(7).termID, img.ID)

	t.Run("natural size box", func(t *testing.T) {
		handleAt(t, s, &Cursor{}, cell, "a=d,d=i,i=7", nil)
		handleAt(t, s, &Cursor{}, cell, "a=p,i=7,U=1", nil)
		images := s.Visible(view, []PlaceholderRun{{Row: 5, Col: 10, Len: 3, ImageID: 7, ImgCol: 1}})
		require.Len(t, images, 1)
		assert.Equal(t, 4, images[0].Width, "ceil(40/10)")
		assert.Equal(t, 1, images[0].Height, "ceil(20/20)")
	})
	t.Run("cells outside the box draw nothing", func(t *testing.T) {
		assert.Empty(t, s.Visible(view, []PlaceholderRun{{Row: 5, Col: 10, Len: 3, ImageID: 7, ImgRow: 3}}))
	})
	t.Run("virtual placements are deleted only by id", func(t *testing.T) {
		handleAt(t, s, &Cursor{}, cell, "a=d,d=a", nil)
		handleAt(t, s, &Cursor{}, cell, "a=d,d=z,z=0", nil)
		assert.Len(t, s.byClientID(7).refs, 1)
		handleAt(t, s, &Cursor{}, cell, "a=d,d=I,i=7", nil)
		assert.Nil(t, s.byClientID(7))
	})
}

func TestPlaceholderSelectsPlacementByID(t *testing.T) {
	cell := CellSize{Width: 10, Height: 20}
	s := NewStorage()
	exec(t, s, nil, "a=t,i=7,s=40,v=20", make([]byte, 40*20*4))
	handleAt(t, s, &Cursor{}, cell, "a=p,i=7,U=1,p=1,c=8,r=2", nil)
	handleAt(t, s, &Cursor{}, cell, "a=p,i=7,U=1,p=2,c=3,r=4", nil)
	view := View{Width: 80, Height: 24, Cell: cell}

	images := s.Visible(view, []PlaceholderRun{{Row: 5, Col: 10, Len: 3, ImageID: 7, PlacementID: 2}})
	require.Len(t, images, 1)
	assert.Equal(t, 3, images[0].Width)
	assert.Equal(t, 4, images[0].Height)

	images = s.Visible(view, []PlaceholderRun{{Row: 5, Col: 10, Len: 3, ImageID: 7}})
	require.Len(t, images, 1)
	assert.Equal(t, 8, images[0].Width, "a zero placement id takes the first virtual placement")

	assert.Empty(t, s.Visible(view, []PlaceholderRun{{Row: 5, Col: 10, Len: 3, ImageID: 7, PlacementID: 3}}),
		"an unknown placement id draws nothing")
}

func TestRelativeToVirtualPlacement(t *testing.T) {
	cell := CellSize{Width: 10, Height: 20}
	s := NewStorage()
	exec(t, s, nil, "a=t,i=7,s=10,v=20", make([]byte, 800))
	exec(t, s, nil, "a=t,i=8,s=10,v=20", make([]byte, 800))
	handleAt(t, s, &Cursor{}, cell, "a=p,i=7,U=1,p=1", nil)
	assert.Equal(t, "\x1b_Gi=8;OK\x1b\\", handleAt(t, s, &Cursor{}, cell, "a=p,i=8,P=7,Q=1,H=1,V=1", nil))

	view := View{Width: 80, Height: 24, Cell: cell}
	assert.Len(t, s.Visible(view, nil), 0, "no placeholder cells: the child is skipped, not deleted")
	assert.Len(t, s.byClientID(8).refs, 1)

	runs := []PlaceholderRun{
		{Row: 6, Col: 4, Len: 1, ImageID: 7},
		{Row: 5, Col: 9, Len: 1, ImageID: 7, ImgRow: 1},
	}
	images := s.Visible(view, runs)
	var child []term.Image
	for _, img := range images {
		if img.ID == s.byClientID(8).termID {
			child = append(child, img)
		}
	}
	require.Len(t, child, 1)
	assert.Equal(t, term.Coordinates{X: 5, Y: 6}, child[0].Pos, "anchored at the minimum row and column plus offsets")
}
