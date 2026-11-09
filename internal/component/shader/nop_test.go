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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestNopLeavesTheScreenAsDrawn(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	src.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	images := []term.Image{{Src: src, ID: term.NewImageID(), Width: 2, Height: 2}}
	cells := makeCellMatrix(3, 3)

	got := ShadeImages(Nop(), 1, 2, cells, images)

	assert.Equal(t, makeCellMatrix(3, 3), cells)
	assert.Equal(t, []term.Image{{Src: src, ID: images[0].ID, Width: 2, Height: 2}}, got)
}
