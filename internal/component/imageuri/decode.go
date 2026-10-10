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
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"  // register the gif decoder
	_ "image/jpeg" // register the jpeg decoder
	_ "image/png"  // register the png decoder

	_ "golang.org/x/image/webp" // register the webp decoder
)

// maxDimension bounds each side of a decoded image. The header is
// checked before decoding so that a small, highly compressed payload
// cannot make us allocate an enormous raster.
const maxDimension = 8192

// decode returns data as a tightly packed *image.RGBA whose bounds
// start at the origin, which term writers upload without copying.
func decode(data []byte) (*image.RGBA, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image header: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("%s image is empty", format)
	}
	if cfg.Width > maxDimension || cfg.Height > maxDimension {
		return nil, fmt.Errorf("%s image is %dx%d, larger than %dx%d",
			format, cfg.Width, cfg.Height, maxDimension, maxDimension)
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode %s image: %w", format, err)
	}
	return toRGBA(img), nil
}

func toRGBA(src image.Image) *image.RGBA {
	b := src.Bounds()
	if rgba, ok := src.(*image.RGBA); ok &&
		b.Min == (image.Point{}) && rgba.Stride == 4*b.Dx() {
		return rgba
	}
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst
}
