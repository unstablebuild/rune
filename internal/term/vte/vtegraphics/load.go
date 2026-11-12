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
	"encoding/binary"
	"errors"
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

func (s loadSpec) decode(data []byte, pool *BufferPool) (img *image.RGBA, straight bool, err *Error) {
	if s.compression == 'z' {
		inflated, err := inflate(data, s, pool)
		if err != nil {
			return nil, false, err
		}
		data = inflated
	}
	if s.format == FormatPNG {
		img, err := decodePNG(data)
		return img, false, err
	}
	expected := s.expectedSize()
	if len(data) < expected {
		pool.Put(data)
		return nil, false, errorf("ENODATA", "Insufficient image data: %d < %d", len(data), expected)
	}
	w, h := int(s.width), int(s.height)
	if s.format == FormatRGBA {
		return &image.RGBA{
			Pix: data[:expected:expected], Stride: 4 * w, Rect: image.Rect(0, 0, w, h),
		}, true, nil
	}
	img = &image.RGBA{Pix: pool.Get(4 * w * h), Stride: 4 * w, Rect: image.Rect(0, 0, w, h)}
	for i, j := 0, 0; i < expected; i, j = i+3, j+4 {
		img.Pix[j] = data[i]
		img.Pix[j+1] = data[i+1]
		img.Pix[j+2] = data[i+2]
		img.Pix[j+3] = 0xff
	}
	pool.Put(data)
	return img, false, nil
}

// premultiply converts straight-alpha RGBA bytes in place into the
// premultiplied form image.RGBA and the GPU upload path expect.
func premultiply(pix []byte) {
	for i := firstTranslucent(pix); i+3 < len(pix); i += 4 {
		a := uint32(pix[i+3])
		if a == 0xff {
			continue
		}
		pix[i] = byte((uint32(pix[i])*a + 127) / 255)
		pix[i+1] = byte((uint32(pix[i+1])*a + 127) / 255)
		pix[i+2] = byte((uint32(pix[i+2])*a + 127) / 255)
	}
}

// firstTranslucent returns the offset of the first pixel of pix whose
// alpha is below 0xff, or that of the first incomplete pixel when there
// is none.
func firstTranslucent(pix []byte) int {
	const alpha = 0xff000000ff000000
	i := 0
	for ; i+32 <= len(pix); i += 32 {
		w := binary.LittleEndian.Uint64(pix[i:]) &
			binary.LittleEndian.Uint64(pix[i+8:]) &
			binary.LittleEndian.Uint64(pix[i+16:]) &
			binary.LittleEndian.Uint64(pix[i+24:])
		if w&alpha != alpha {
			break
		}
	}
	for ; i+3 < len(pix); i += 4 {
		if pix[i+3] != 0xff {
			break
		}
	}
	return i
}

// inflate decompresses data. A raw format's size is known up front, so
// its bytes are read straight into a pool buffer; PNG bytes are read
// into whatever they need.
func inflate(data []byte, s loadSpec, pool *BufferPool) ([]byte, *Error) {
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, errorf("EINVAL", "Failed to inflate image data with error: %v", err)
	}
	defer r.Close()
	expected := s.expectedSize()
	if expected == 0 {
		// Read one byte past the limit so an over-long stream is detected
		// without inflating it entirely.
		out, err := io.ReadAll(io.LimitReader(r, MaxDataSize+1))
		if err != nil {
			return nil, errorf("EINVAL", "Failed to inflate image data with error: %v", err)
		}
		if len(out) > MaxDataSize {
			return nil, errorf("EFBIG", "Too much data")
		}
		return out, nil
	}
	out := pool.Get(expected)
	n, err := io.ReadFull(r, out)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		pool.Put(out)
		return nil, errorf("EINVAL", "Failed to inflate image data with error: %v", err)
	}
	// The stream must end exactly there, and reading on is also what
	// has zlib verify its checksum.
	var extra [1]byte
	more, err := r.Read(extra[:])
	if err != nil && !errors.Is(err, io.EOF) {
		pool.Put(out)
		return nil, errorf("EINVAL", "Failed to inflate image data with error: %v", err)
	}
	if n != expected || more > 0 {
		pool.Put(out)
		return nil, errorf("EINVAL", "Image data size post inflation does not match expected size")
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
