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

package gui

import (
	"context"
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestFrameWriterDrawImage(t *testing.T) {
	tests := []struct {
		name    string
		img     term.Image
		want    bool
		wantLen int
		wantVis image.Rectangle
	}{
		{
			name:    "inside the grid is kept unclipped",
			img:     term.Image{Pos: term.Coordinates{X: 1, Y: 1}, Width: 3, Height: 2},
			want:    true,
			wantLen: 1,
			wantVis: image.Rect(1, 1, 4, 3),
		},
		{
			name:    "overflowing the grid is clipped to it",
			img:     term.Image{Pos: term.Coordinates{X: 8, Y: 3}, Width: 5, Height: 5},
			want:    true,
			wantLen: 1,
			wantVis: image.Rect(8, 3, 10, 5),
		},
		{
			name:    "fully outside the grid is dropped",
			img:     term.Image{Pos: term.Coordinates{X: 10, Y: 0}, Width: 2, Height: 2},
			want:    true,
			wantLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newFrameWriter(context.Background(), 10, 5)
			assert.Equal(t, tt.want, w.DrawImage(tt.img))
			require.Len(t, w.Images(), tt.wantLen)
			if tt.wantLen > 0 {
				assert.Equal(t, tt.wantVis, w.Images()[0].Visible())
			}
		})
	}
}

// TestFrameWriterClearDropsPlacements asserts placements live for a
// single frame, so a picture that is no longer drawn disappears and
// stops pinning its pixels.
func TestFrameWriterClearDropsPlacements(t *testing.T) {
	w := newFrameWriter(context.Background(), 10, 5)
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	require.True(t, w.DrawImage(term.Image{
		Src: src, ID: 1, Width: 2, Height: 2,
	}))
	require.Len(t, w.Images(), 1)

	released := w.Images()[:1]
	require.NoError(t, w.Clear(term.Attributes{}))
	assert.Empty(t, w.Images())
	assert.Nil(t, released[0].Src, "Clear must release the pixel reference")

	require.True(t, w.DrawImage(term.Image{ID: 2, Width: 1, Height: 1}))
	require.Len(t, w.Images(), 1)
	assert.Equal(t, term.ImageID(2), w.Images()[0].ID)
}

// TestFrameWriterCellsUnaffected asserts collecting placements does not
// disturb the cell buffer the renderer reads.
func TestFrameWriterCellsUnaffected(t *testing.T) {
	w := newFrameWriter(context.Background(), 4, 2)
	w.SetCell(term.Coordinates{X: 1, Y: 1}, term.Cell{Ch: 'a'})
	require.True(t, w.DrawImage(term.Image{Width: 4, Height: 2}))
	assert.Equal(t, 'a', w.RawCells()[1][1].Ch)
}
