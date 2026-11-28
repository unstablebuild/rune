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

package shaderdevice

import (
	"image"
	"image/color"
	"testing"

	"github.com/hajimehoshi/ebiten/v2/benchdraw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/gshader"
)

func TestImageCopiesPixelsBackOncePerRender(t *testing.T) {
	d := newDevice(t)
	key := term.NewImageID()
	var f gshader.Frame
	frame(t, d, px, black, func() { f = render(t, d, key, area, gshader.Solid(term.ColorRed)) })
	img := f.Src.(*Image)
	assert.Equal(t, color.RGBAModel, img.ColorModel(), "premultiplied")
	assert.Equal(t, image.Rect(0, 0, 80, 80), img.Bounds())
	assert.Zero(t, img.reads, "nothing is read until asked")

	benchdraw.BeginFrame(t)
	img.At(0, 0)
	img.At(79, 79)
	benchdraw.EndFrame(t)
	assert.Equal(t, 1, img.reads, "the pixels are copied back once")
	assert.Equal(t, color.RGBA{}, img.At(80, 0), "outside the bounds is transparent")
	assert.Equal(t, 1, img.reads)

	frame(t, d, px, black, func() {
		again := render(t, d, key, area, gshader.Solid(term.ColorRed))
		require.Same(t, img, again.Src)
		img.At(0, 0)
	})
	assert.Equal(t, 1, img.reads, "a reused rendering keeps the copy")

	frame(t, d, px, black, func() {
		next := render(t, d, key, area, gshader.Solid(term.ColorBlue))
		require.Same(t, img, next.Src)
		img.At(0, 0)
		img.At(1, 1)
	})
	assert.Equal(t, 2, img.reads, "a new rendering invalidates the copy")
}

func TestTexture(t *testing.T) {
	_, ok := Texture(image.NewRGBA(image.Rect(0, 0, 1, 1)))
	assert.False(t, ok, "not a GPU image")
	_, ok = Texture(nil)
	assert.False(t, ok)

	d := newDevice(t)
	var f gshader.Frame
	frame(t, d, px, black, func() { f = render(t, d, 1, area, gshader.Source()) })
	tex, ok := Texture(f.Src)
	require.True(t, ok)
	assert.Equal(t, f.Src.Bounds(), tex.Bounds())
}
