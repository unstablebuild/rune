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

package vtegraphics

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
)

// Pixel formats (spec §4.1).
const (
	FormatRGB  = 24
	FormatRGBA = 32
	FormatPNG  = 100
)

const (
	// MaxImageDimension bounds each axis in pixels (spec §4.2).
	MaxImageDimension = 10000
	// MaxDataSize bounds the bytes accepted for one image (spec §4.7).
	MaxDataSize = 400_000_000
	// directSlack is how many bytes beyond the exact pixel size kitty
	// tolerates on an uncompressed direct transmission before EFBIG.
	directSlack = 10
	// MaxPathLength bounds a file or shared memory name (spec §4.4).
	MaxPathLength = 2048
)

// Error is a protocol error response: CODE:message (spec §6.3).
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string {
	return e.Code + ":" + e.Msg
}

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// loadSpec is the shape of a transmission, fixed by its first chunk.
type loadSpec struct {
	format      uint32
	compression byte
	width       uint32
	height      uint32
}

// newLoadSpec returns the shape the first chunk cmd declares.
func newLoadSpec(cmd Command) loadSpec {
	format := cmd.Format
	if format == 0 {
		format = FormatRGBA
	}
	return loadSpec{
		format: format, compression: cmd.Compression,
		width: cmd.Width, height: cmd.Height,
	}
}

// expectedSize returns the pixel byte count for raw formats and 0 for
// PNG, whose size is only known after decoding.
func (s loadSpec) expectedSize() int {
	switch s.format {
	case FormatRGB:
		return int(s.width) * int(s.height) * 3
	case FormatRGBA:
		return int(s.width) * int(s.height) * 4
	}
	return 0
}

// validate applies the checks kitty makes before accepting data
// (spec §4.1, §4.2, §4.5).
func (s loadSpec) validate(cmd Command) *Error {
	if s.width > MaxImageDimension || s.height > MaxImageDimension {
		return errorf("EINVAL", "Image too large, width or height greater than %d",
			MaxImageDimension)
	}
	switch s.format {
	case FormatPNG:
		if cmd.DataSize > MaxDataSize {
			return errorf("EINVAL", "PNG data size too large")
		}
	case FormatRGB, FormatRGBA:
		if s.expectedSize() == 0 {
			return errorf("EINVAL", "Zero width/height not allowed")
		}
	default:
		return errorf("EINVAL", "Unknown image format: %d", s.format)
	}
	if s.compression != 0 && s.compression != 'z' {
		return errorf("EINVAL", "Unknown image compression: %c", s.compression)
	}
	return nil
}

// capacity is the most bytes a direct transmission may accumulate
// before it is rejected with EFBIG.
func (s loadSpec) capacity() int {
	if s.format == FormatPNG {
		return MaxDataSize
	}
	if s.compression != 0 {
		return s.expectedSize() + 1024
	}
	return s.expectedSize() + directSlack
}

// decode turns the accumulated bytes into premultiplied RGBA pixels
// (spec §4.1-§4.3). The returned image has a zero Min and a tight
// stride so the GUI can upload it without copying.
func (s loadSpec) decode(data []byte) (*image.RGBA, *Error) {
	if s.compression == 'z' {
		inflated, err := inflate(data, s)
		if err != nil {
			return nil, err
		}
		data = inflated
	}
	if s.format == FormatPNG {
		return decodePNG(data)
	}
	expected := s.expectedSize()
	if len(data) < expected {
		return nil, errorf("ENODATA", "Insufficient image data: %d < %d", len(data), expected)
	}
	w, h := int(s.width), int(s.height)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if s.format == FormatRGBA {
		premultiply(img.Pix, data[:expected])
	} else {
		for i, j := 0, 0; i < expected; i, j = i+3, j+4 {
			img.Pix[j] = data[i]
			img.Pix[j+1] = data[i+1]
			img.Pix[j+2] = data[i+2]
			img.Pix[j+3] = 0xff
		}
	}
	return img, nil
}

// premultiply converts straight-alpha RGBA bytes into the premultiplied
// form image.RGBA and the GPU upload path expect.
func premultiply(dst, src []byte) {
	for i := 0; i+3 < len(src); i += 4 {
		a := uint32(src[i+3])
		if a == 0xff {
			copy(dst[i:i+4], src[i:i+4])
			continue
		}
		dst[i] = byte((uint32(src[i])*a + 127) / 255)
		dst[i+1] = byte((uint32(src[i+1])*a + 127) / 255)
		dst[i+2] = byte((uint32(src[i+2])*a + 127) / 255)
		dst[i+3] = byte(a)
	}
}

func inflate(data []byte, s loadSpec) ([]byte, *Error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, errorf("EINVAL", "Failed to inflate image data with error: %v", err)
	}
	defer r.Close()
	limit := MaxDataSize
	if expected := s.expectedSize(); expected > 0 {
		limit = expected
	}
	// Read one byte past the limit so an over-long stream is detected
	// without inflating it entirely.
	out, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, errorf("EINVAL", "Failed to inflate image data with error: %v", err)
	}
	if expected := s.expectedSize(); expected > 0 && len(out) != expected {
		return nil, errorf("EINVAL", "Image data size post inflation does not match expected size")
	}
	if len(out) > MaxDataSize {
		return nil, errorf("EFBIG", "Too much data")
	}
	return out, nil
}

func decodePNG(data []byte) (*image.RGBA, *Error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errorf("EBADPNG", "%v", err)
	}
	if cfg.Width > MaxImageDimension || cfg.Height > MaxImageDimension {
		return nil, errorf("ENOMEM", "PNG image is too large")
	}
	src, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errorf("EBADPNG", "%v", err)
	}
	if rgba, ok := src.(*image.RGBA); ok &&
		rgba.Rect.Min == (image.Point{}) && rgba.Stride == 4*rgba.Rect.Dx() {
		return rgba, nil
	}
	b := src.Bounds()
	img := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(img, img.Rect, src, b.Min, draw.Src)
	return img, nil
}
