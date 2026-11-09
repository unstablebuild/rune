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

package appicon

import (
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToNRGBA(t *testing.T) {
	assert.Nil(t, toNRGBA(nil))

	src := image.NewNRGBA(image.Rect(10, 20, 13, 22))
	translucent := color.NRGBA{R: 200, G: 100, B: 50, A: 3}
	src.SetNRGBA(10, 20, translucent)
	src.SetNRGBA(12, 21, color.NRGBA{R: 1, G: 2, B: 3, A: 255})

	got := toNRGBA(src)
	require.NotNil(t, got)
	assert.Equal(t, image.Rect(0, 0, 3, 2), got.Rect)
	assert.Len(t, got.Pix, 3*2*4)
	assert.Equal(t, 3*4, got.Stride)
	assert.Equal(t, translucent, got.NRGBAAt(0, 0), "straight alpha is kept")
	assert.Equal(t, color.NRGBA{R: 1, G: 2, B: 3, A: 255}, got.NRGBAAt(2, 1))
	assert.Equal(t, color.NRGBA{}, got.NRGBAAt(1, 0))
}
