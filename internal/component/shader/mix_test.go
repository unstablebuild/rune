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

package shader

import (
	"image"
	"image/color"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestMixPassesImagesThroughItsShadersInOrder(t *testing.T) {
	var seen [][]term.Image
	adding := func(id term.ImageID) *imageShader {
		return &imageShader{images: func(images []term.Image) []term.Image {
			seen = append(seen, slices.Clone(images))
			return append(images, term.Image{ID: id})
		}}
	}
	mix := Mix(adding(2), adding(3)).(ImageShader)

	got := mix.ShadeImages(0, 1, makeCellMatrix(2, 2), []term.Image{{ID: 1}})

	assert.Equal(t, []term.Image{{ID: 1}, {ID: 2}, {ID: 3}}, got)
	assert.Equal(t, [][]term.Image{{{ID: 1}}, {{ID: 1}, {ID: 2}}}, seen)
}

func TestMixPaintsThePicturesItsShadersOfCellsAreGiven(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	src.SetRGBA(0, 0, color.RGBA{R: 200, A: 255})
	var seen []term.Image
	last := &imageShader{images: func(images []term.Image) []term.Image {
		seen = images
		return images
	}}
	mix := Mix(halveBg{}, last).(ImageShader)
	cells := makeCellMatrix(2, 1)

	got := mix.ShadeImages(0, 1, cells, []term.Image{{Src: src, Width: 1, Height: 1}})

	assert.Empty(t, got)
	assert.Empty(t, seen)
	assert.Equal(t, term.NewRGBColor(100, 0, 0), cells[0][0].Bg)
}
