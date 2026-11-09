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

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// Shader abstracts the ability to apply shader-like effects
// to other components.
type Shader interface {
	// Shade applies the shader's transformation
	// to the given component turning it into an animated component,
	// or returns false if this Shader's animation is done.
	Shade(frame, total int, in [][]term.Cell)
}

// ImageShader is a Shader that also shades the image placements drawn
// over the cells it shades.
type ImageShader interface {
	Shader
	// ShadeImages shades in like Shade, and returns the placements to
	// draw over it in place of images, in drawing order. It may modify
	// images and return a slice sharing its backing array.
	ShadeImages(frame, total int, in [][]term.Cell, images []term.Image) []term.Image
}

// Snapshot is a screen as it showed: its cells and the pictures drawn
// over them. Reveal shaders, such as [Burn] and [Rain], take one in
// their params to start from instead of a blank screen, and uncover the
// cells they shade in its place. They copy it when they are made, so it
// may change afterwards.
type Snapshot struct {
	Cells [][]term.Cell
	// Images are the placements drawn over Cells.
	Images []term.Image
	// Desaturation fades the colors of the snapshot and its pictures
	// toward gray by this amount, so that a reveal picks up where a
	// [GrayFade] left off. (range [0, 1], clamped)
	Desaturation float64
}

// copied returns a copy of s, with its desaturation applied, for a
// shader to keep. defaultFg stands for the default foreground.
func (s Snapshot) copied(defaultFg term.Color) Snapshot {
	out := Snapshot{Cells: cloneCellMatrix(s.Cells), Images: copyImages(s.Images)}
	amount := min(s.Desaturation, 1)
	if !(amount > 0) {
		return out
	}
	for _, row := range out.Cells {
		for x, c := range row {
			row[x].Fg = shaderutils.DesaturateColor(c.Fg, amount, defaultFg)
		}
	}
	// copyImages shares a copy among the placements of a picture.
	desaturated := make(map[*image.RGBA]bool, len(out.Images))
	for _, img := range out.Images {
		pixels := img.Src.(*image.RGBA)
		if !desaturated[pixels] {
			desaturatePixels(pixels, amount)
			desaturated[pixels] = true
		}
	}
	return out
}

// ShadeImages shades in with s, and returns the placements to draw over
// it: the ones s returns if it is an [ImageShader]. Otherwise the
// pictures are painted onto the cells they cover and shaded with them,
// and only their parts over the cells s leaves unchanged are returned.
func ShadeImages(s Shader, frame, total int, in [][]term.Cell, images []term.Image) []term.Image {
	if is, ok := s.(ImageShader); ok {
		return is.ShadeImages(frame, total, in, images)
	}
	if len(images) == 0 {
		s.Shade(frame, total, in)
		return images
	}
	p := paintings.Get().(*painting)
	defer paintings.Put(p)
	return p.shade(s, frame, total, in, images)
}
