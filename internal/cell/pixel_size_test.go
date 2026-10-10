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

package cell

import (
	"context"
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestPixelSizeFromContext(t *testing.T) {
	size := PixelSize{Width: 9.5, Height: 20}
	got, ok := PixelSizeFromContext(ContextWithPixelSize(context.Background(), size))
	assert.True(t, ok)
	assert.Equal(t, size, got)

	_, ok = PixelSizeFromContext(context.Background())
	assert.False(t, ok, "a context carries no size by default")
	_, ok = PixelSizeFromContext(context.TODO())
	assert.False(t, ok)
	_, ok = PixelSizeFromContext(ContextWithPixelSize(context.Background(), PixelSize{}))
	assert.False(t, ok, "a zero size tells nothing")
}

func TestPixelSizePixels(t *testing.T) {
	tests := []struct {
		name string
		size PixelSize
		r    image.Rectangle
		want image.Rectangle
	}{
		{
			name: "whole pixels",
			size: PixelSize{Width: 10, Height: 20},
			r:    image.Rect(1, 2, 4, 5),
			want: image.Rect(10, 40, 40, 100),
		},
		{
			name: "fractional edges are rounded",
			size: PixelSize{Width: 9.6, Height: 20.4},
			r:    image.Rect(1, 1, 3, 3),
			want: image.Rect(10, 20, 29, 61),
		},
		{
			name: "cells left of and above the grid",
			size: PixelSize{Width: 10, Height: 20},
			r:    image.Rect(-2, -1, 1, 1),
			want: image.Rect(-20, -20, 10, 20),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.size.Pixels(tt.r))
		})
	}
}

func TestPixelSizeCells(t *testing.T) {
	size := PixelSize{Width: 10, Height: 20}
	tests := []struct {
		name string
		r    image.Rectangle
		want image.Rectangle
	}{
		{name: "on cell edges", r: image.Rect(10, 40, 40, 100), want: image.Rect(1, 2, 4, 5)},
		{name: "reaching into the cells around", r: image.Rect(11, 41, 39, 99), want: image.Rect(1, 2, 4, 5)},
		{name: "reaching a pixel past the edges", r: image.Rect(9, 39, 41, 101), want: image.Rect(0, 1, 5, 6)},
		{name: "left of and above the grid", r: image.Rect(-15, -1, 5, 1), want: image.Rect(-2, -1, 1, 1)},
		{name: "empty", r: image.Rect(10, 10, 10, 30)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, size.Cells(tt.r))
		})
	}
}

func TestPixelSizeCellsWithoutASide(t *testing.T) {
	r := image.Rect(10, 40, 40, 100)
	for _, size := range []PixelSize{
		{},
		{Width: 10},
		{Height: 20},
		{Width: -10, Height: 20},
	} {
		assert.Equal(t, image.Rectangle{}, size.Cells(r), "%+v", size)
		assert.Equal(t, image.Rectangle{}, size.Cells(image.Rectangle{}), "%+v", size)
	}
}

func TestPixelSizeCovered(t *testing.T) {
	size := PixelSize{Width: 10, Height: 20}
	box := term.Image{Pos: term.Coordinates{X: 4, Y: 3}, Width: 4, Height: 2}
	withOffset := func(img term.Image, x, y int) term.Image {
		img.Offset = image.Pt(x, y)
		return img
	}
	withClip := func(img term.Image, clip image.Rectangle) term.Image {
		img.Clip = clip
		return img
	}
	tests := []struct {
		name string
		size PixelSize
		img  term.Image
		want image.Rectangle
	}{
		{name: "unmoved covers its cells", size: size, img: box, want: image.Rect(4, 3, 8, 5)},
		{
			name: "unmoved and clipped covers its visible cells", size: size,
			img:  withClip(box, image.Rect(0, 0, 6, 10)),
			want: image.Rect(4, 3, 6, 5),
		},
		{
			name: "moved by whole cells covers the cells it lands on", size: size,
			img:  withOffset(box, 20, -20),
			want: image.Rect(6, 2, 10, 4),
		},
		{
			name: "moved by a few pixels reaches into the next cells", size: size,
			img:  withOffset(box, 3, -7),
			want: image.Rect(4, 2, 9, 5),
		},
		{
			name: "moved onto the grid from cells outside it", size: size,
			img:  withOffset(term.Image{Pos: term.Coordinates{X: -10, Y: 3}, Width: 4, Height: 2}, 141, 0),
			want: image.Rect(4, 3, 9, 5),
		},
		{
			name: "moved is confined to its clip", size: size,
			img:  withClip(withOffset(box, 0, 30), image.Rect(0, 0, 16, 5)),
			want: image.Rect(4, 4, 8, 5),
		},
		{
			name: "moved outside its clip covers nothing", size: size,
			img: withClip(withOffset(box, 0, 60), image.Rect(0, 0, 16, 5)),
		},
		{
			name: "moved onto its clip from cells outside it", size: size,
			img:  withClip(withOffset(term.Image{Pos: term.Coordinates{X: 4, Y: 7}, Width: 4, Height: 2}, 0, -80), image.Rect(0, 0, 16, 5)),
			want: image.Rect(4, 3, 8, 5),
		},
		{
			name: "without a size, moved covers its own cells",
			img:  withOffset(box, 20, -20),
			want: image.Rect(4, 3, 8, 5),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.size.Covered(tt.img))
		})
	}
}
