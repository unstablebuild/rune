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

package imagecomp_test

import (
	"context"
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"golang.org/x/image/draw"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/imagecomp"
)

// recorder draws images, on cells of 10x20 pixels.
type recorder struct {
	term.NoopWriter
	images []term.Image
}

func (r *recorder) Context() context.Context {
	return cell.ContextWithPixelSize(context.Background(), cell.PixelSize{Width: 10, Height: 20})
}

func (r *recorder) DrawImage(img term.Image) bool {
	r.images = append(r.images, img)
	return true
}

func percent(n int) imagecomp.Length { return imagecomp.Length{N: n, Percent: true} }

func cells(n int) imagecomp.Length { return imagecomp.Length{N: n} }

func TestComponentDrawsThePicture(t *testing.T) {
	wide := image.NewRGBA(image.Rect(0, 0, 400, 100))
	id := term.NewImageID()
	tests := []struct {
		name          string
		width, height int
		cfg           imagecomp.Config
		want          []term.Image
	}{
		{
			name:  "contain is as large as the box lets it",
			width: 40, height: 20,
			cfg: imagecomp.Config{Width: percent(50), Height: percent(50), Fit: imagecomp.Contain},
			want: []term.Image{{
				Src: wide, ID: id, Crop: wide.Rect,
				Pos: term.Coordinates{X: 10, Y: 8}, Width: 20, Height: 3, Fit: term.ImageFitContain,
			}},
		},
		{
			name:  "cover fills the component, cutting off the sides",
			width: 40, height: 20,
			cfg: imagecomp.Config{Width: percent(100), Height: percent(100), Fit: imagecomp.Cover},
			want: []term.Image{{
				Src: wide, ID: id, Crop: image.Rect(150, 0, 250, 100),
				Width: 40, Height: 20, Fit: term.ImageFitFill,
			}},
		},
		{
			name:  "fill stretches over the box",
			width: 40, height: 20,
			cfg: imagecomp.Config{Width: cells(30), Height: cells(5), Fit: imagecomp.Fill},
			want: []term.Image{{
				Src: wide, ID: id, Crop: wide.Rect,
				Pos: term.Coordinates{X: 5, Y: 7}, Width: 30, Height: 5, Fit: term.ImageFitFill,
			}},
		},
		{
			name:  "a box larger than the component is cut to it",
			width: 40, height: 20,
			cfg: imagecomp.Config{Width: cells(100), Height: cells(100), Fit: imagecomp.Contain},
			want: []term.Image{{
				Src: wide, ID: id, Crop: wide.Rect,
				Pos: term.Coordinates{X: 0, Y: 7}, Width: 40, Height: 5, Fit: term.ImageFitContain,
			}},
		},
		{
			name: "an empty component draws nothing",
			cfg:  imagecomp.Config{Width: percent(100), Height: percent(100), Fit: imagecomp.Cover},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := imagecomp.New(wide, id, tt.cfg)
			c.Resize(tt.width, tt.height)
			w := new(recorder)
			c.Draw(w)
			assert.Equal(t, tt.want, w.images)
		})
	}
}

func TestComponentDrawsASCIIArtWithoutImages(t *testing.T) {
	// halves is white on its left half and black on its right one.
	halves := image.NewRGBA(image.Rect(0, 0, 2, 1))
	halves.Set(0, 0, color.White)
	halves.Set(1, 0, color.Black)
	// band is white in the middle of black.
	band := image.NewRGBA(image.Rect(0, 0, 3, 1))
	band.Set(0, 0, color.Black)
	band.Set(1, 0, color.White)
	band.Set(2, 0, color.Black)

	ascii := asciiart.Config{DensityCharacters: " #", Scaler: draw.NearestNeighbor}
	failing := term.NewStringWriter(10, 4)
	failing.SetContext = cell.ContextWithPixelSize(context.Background(), cell.PixelSize{Width: 10, Height: 20})
	tests := []struct {
		name     string
		img      image.Image
		cfg      imagecomp.Config
		w        comptest.StringerWriter
		expected string
	}{
		{
			name: "contain lays the image out for tall cells",
			img:  halves,
			cfg:  imagecomp.Config{Width: percent(100), Height: percent(100), Fit: imagecomp.Contain, ASCII: ascii},
			w:    term.NewStringWriter(10, 4),
			expected: "" +
				"          \n" +
				"#####     \n" +
				"#####     \n" +
				"          ",
		},
		{
			name: "cover cuts off the sides",
			img:  band,
			cfg:  imagecomp.Config{Width: percent(100), Height: percent(100), Fit: imagecomp.Cover, ASCII: ascii},
			w:    term.NewStringWriter(10, 4),
			expected: "" +
				"##########\n" +
				"##########\n" +
				"##########\n" +
				"##########",
		},
		{
			name: "a writer that fails to draw the picture",
			img:  halves,
			cfg:  imagecomp.Config{Width: cells(4), Height: cells(2), Fit: imagecomp.Fill, ASCII: ascii},
			w:    failing,
			expected: "" +
				"          \n" +
				"   ##     \n" +
				"   ##     \n" +
				"          ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := imagecomp.New(tt.img, term.NewImageID(), tt.cfg)
			comptest.TestComponent(t, c, tt.w, []comptest.TestCase{
				{Action: func() { c.Resize(10, 4) }, Expected: tt.expected},
			})
		})
	}
}
