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

	ebiten "github.com/hajimehoshi/ebiten/v2"
)

// Image is the output of a Device, an image.Image whose pixels stay on the
// GPU. Its pixels may be read, with At, only on the drawing goroutine and
// before the next Render of its key, when the term.Image.Src contract lets
// a writer read them. The first read copies them all back from the GPU; a
// writer that draws GPU images draws them with [Texture] instead.
type Image struct {
	tex    *ebiten.Image
	bounds image.Rectangle
	// pixels is the copy At reads, valid while fresh.
	pixels *image.RGBA
	fresh  bool
	// reads counts the copies back from the GPU.
	reads int
}

var _ image.Image = (*Image)(nil)

func newImage(tex *ebiten.Image) *Image {
	return &Image{tex: tex, bounds: tex.Bounds()}
}

// ColorModel satisfies image.Image. The pixels are premultiplied.
func (i *Image) ColorModel() color.Model { return color.RGBAModel }

// Bounds satisfies image.Image.
func (i *Image) Bounds() image.Rectangle { return i.bounds }

// At satisfies image.Image.
func (i *Image) At(x, y int) color.Color { return i.RGBAAt(x, y) }

// RGBAAt returns the colour of the pixel at (x, y), transparent outside
// the bounds or once the device freed the image.
func (i *Image) RGBAAt(x, y int) color.RGBA {
	if !image.Pt(x, y).In(i.bounds) {
		return color.RGBA{}
	}
	if !i.fresh {
		if i.pixels == nil {
			i.pixels = image.NewRGBA(i.bounds)
		}
		if i.tex != nil {
			i.tex.ReadPixels(i.pixels.Pix)
			i.reads++
		} else {
			clear(i.pixels.Pix)
		}
		i.fresh = true
	}
	return i.pixels.RGBAAt(x, y)
}

// invalidate drops the copy of the pixels after the texture changed.
func (i *Image) invalidate() { i.fresh = false }

// dispose forgets the texture, which the device freed.
func (i *Image) dispose() {
	i.tex = nil
	i.pixels = nil
	i.fresh = false
}

// Texture returns the GPU texture of img when it is an Image a Device has
// not freed, so that it can be drawn without copying its pixels.
func Texture(img image.Image) (*ebiten.Image, bool) {
	i, ok := img.(*Image)
	if !ok || i.tex == nil {
		return nil, false
	}
	return i.tex, true
}
