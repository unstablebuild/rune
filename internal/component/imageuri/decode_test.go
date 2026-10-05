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

package imageuri

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func solid(w, h int, c color.Color) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	return img
}

func encodePNG(t testing.TB, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestDecode(t *testing.T) {
	red := color.NRGBA{R: 255, A: 255}
	var jpg, gf bytes.Buffer
	require.NoError(t, jpeg.Encode(&jpg, solid(4, 3, red), nil))
	require.NoError(t, gif.Encode(&gf, solid(4, 3, red), nil))

	tests := []struct {
		name    string
		data    []byte
		wantErr string
	}{
		{name: "png", data: encodePNG(t, solid(4, 3, red))},
		{name: "jpeg", data: jpg.Bytes()},
		{name: "gif", data: gf.Bytes()},
		{
			name:    "too wide",
			data:    encodePNG(t, image.NewGray(image.Rect(0, 0, maxDimension+1, 1))),
			wantErr: "larger than",
		},
		{
			name:    "too tall",
			data:    encodePNG(t, image.NewGray(image.Rect(0, 0, 1, maxDimension+1))),
			wantErr: "larger than",
		},
		{name: "garbage", data: []byte("not an image"), wantErr: "decode image header"},
		{name: "empty", data: nil, wantErr: "decode image header"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decode(tt.data)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, image.Rect(0, 0, 4, 3), got.Bounds())
			assert.Equal(t, 4*4, got.Stride)
			r, g, b, a := got.At(1, 1).RGBA()
			assert.InDelta(t, 0xffff, r, 0x0800)
			assert.InDelta(t, 0, g, 0x0800)
			assert.InDelta(t, 0, b, 0x0800)
			assert.Equal(t, uint32(0xffff), a)
		})
	}
}

func TestToRGBA(t *testing.T) {
	tight := image.NewRGBA(image.Rect(0, 0, 2, 2))
	assert.Same(t, tight, toRGBA(tight), "tight RGBA at origin is reused")

	big := image.NewRGBA(image.Rect(0, 0, 4, 4))
	big.Set(2, 3, color.RGBA{G: 255, A: 255})
	sub := big.SubImage(image.Rect(1, 1, 3, 4))
	got := toRGBA(sub)
	assert.NotSame(t, big, got)
	assert.Equal(t, image.Rect(0, 0, 2, 3), got.Bounds())
	assert.Equal(t, 2*4, got.Stride)
	assert.Equal(t, color.RGBA{G: 255, A: 255}, got.RGBAAt(1, 2))
}
