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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// browserFrame is the shape of a frame a terminal browser streams: a
// pane's worth of opaque RGBA pixels.
const browserFrameW, browserFrameH = 1600, 1500

func opaqueFrame(w, h int) []byte {
	data := make([]byte, w*h*4)
	for i := 0; i < len(data); i += 4 {
		data[i], data[i+1], data[i+2], data[i+3] = byte(i), byte(i>>8), byte(i>>16), 0xff
	}
	return data
}

func translucentFrame(w, h int) []byte {
	data := opaqueFrame(w, h)
	for i := 3; i < len(data); i += 8 {
		data[i] = 0x80
	}
	return data
}

// premultiplyReference is the pixel-at-a-time definition the fast path
// must agree with.
func premultiplyReference(pix []byte) []byte {
	out := bytes.Clone(pix)
	for i := 0; i+3 < len(out); i += 4 {
		a := uint32(out[i+3])
		for c := range 3 {
			out[i+c] = byte((uint32(out[i+c])*a + 127) / 255)
		}
	}
	return out
}

func TestPremultiply(t *testing.T) {
	opaque := opaqueFrame(37, 1)
	tests := []struct {
		name  string
		pix   []byte
		first int
	}{
		{"empty", nil, 0},
		{"all opaque", opaque, len(opaque)},
		{"translucent in the first word", edit(opaque, 3, 0), 0},
		{"translucent in a later word", edit(opaque, 4*9+3, 0x7f), 4 * 9},
		{"translucent past the words", edit(opaque, 4*33+3, 0xfe), 4 * 33},
		{"translucent in the last pixel", edit(opaque, 4*36+3, 1), 4 * 36},
		{"stray trailing bytes", append(bytes.Clone(opaque), 1, 2, 0), len(opaque)},
		{"all translucent", translucentFrame(37, 1), 0},
		{"transparent", bytes.Repeat([]byte{200, 100, 50, 0}, 40), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.first, firstTranslucent(tt.pix))
			want := premultiplyReference(tt.pix)
			got := bytes.Clone(tt.pix)
			premultiply(got)
			assert.Equal(t, want, got)
		})
	}
}

func edit(pix []byte, i int, b byte) []byte {
	out := bytes.Clone(pix)
	out[i] = b
	return out
}

func TestDecodeRGBAAliasesTransmission(t *testing.T) {
	spec := loadSpec{format: FormatRGBA, width: 2, height: 1}
	data := []byte{255, 0, 0, 255, 200, 100, 0, 128, 9, 9}
	img, straight, err := spec.decode(data, nil)
	assert.Nil(t, err)
	assert.True(t, straight)
	assert.Equal(t, data[:8], img.Pix)
	assert.Equal(t, &data[0], &img.Pix[0], "no copy is made of the transmission")
	assert.Equal(t, 8, cap(img.Pix), "the slack past the pixels is not reachable")
	assert.Equal(t, 8, img.Stride)
}

func TestDecodePoolsBuffers(t *testing.T) {
	tests := []struct {
		name string
		spec loadSpec
		data []byte
		// seed are the sizes of the buffers in the pool beforehand, in
		// order, and pooled those left afterwards. drawn are the seeded
		// buffers decode's result and intermediates are expected to be
		// made of, by index into seed.
		seed   []int
		pooled []int
		drawn  []int
		want   []byte
		err    string
	}{
		{
			name:   "rgb converts into a pool buffer and lets the transmission go",
			spec:   loadSpec{format: FormatRGB, width: 2, height: 1},
			data:   []byte{1, 2, 3, 4, 5, 6},
			seed:   []int{8},
			drawn:  []int{0},
			pooled: []int{6},
			want:   []byte{1, 2, 3, 255, 4, 5, 6, 255},
		},
		{
			name:   "rgba keeps the transmission",
			spec:   loadSpec{format: FormatRGBA, width: 2, height: 1},
			data:   []byte{1, 2, 3, 4, 5, 6, 7, 8},
			seed:   []int{8},
			pooled: []int{8},
			want:   []byte{1, 2, 3, 4, 5, 6, 7, 8},
		},
		{
			name:   "short data goes back",
			spec:   loadSpec{format: FormatRGBA, width: 2, height: 1},
			data:   []byte{1, 2, 3, 4},
			err:    "ENODATA:Insufficient image data: 4 < 8",
			pooled: []int{4},
		},
		{
			name:  "compressed rgba inflates into a pool buffer",
			spec:  loadSpec{format: FormatRGBA, compression: 'z', width: 2, height: 1},
			data:  zlibBytes([]byte{1, 2, 3, 4, 5, 6, 7, 8}),
			seed:  []int{8},
			drawn: []int{0},
			want:  []byte{1, 2, 3, 4, 5, 6, 7, 8},
		},
		{
			name:   "compressed rgb inflates into one pool buffer and converts into another",
			spec:   loadSpec{format: FormatRGB, compression: 'z', width: 2, height: 1},
			data:   zlibBytes([]byte{1, 2, 3, 4, 5, 6}),
			seed:   []int{6, 8},
			drawn:  []int{0, 1},
			pooled: []int{6},
			want:   []byte{1, 2, 3, 255, 4, 5, 6, 255},
		},
		{
			name:   "an inflated size mismatch goes back",
			spec:   loadSpec{format: FormatRGB, compression: 'z', width: 1, height: 1},
			data:   zlibBytes([]byte{1, 2, 3, 4}),
			err:    "EINVAL:Image data size post inflation does not match expected size",
			pooled: []int{3},
		},
		{
			name:   "a short inflated stream goes back",
			spec:   loadSpec{format: FormatRGB, compression: 'z', width: 2, height: 1},
			data:   zlibBytes([]byte{1, 2, 3}),
			err:    "EINVAL:Image data size post inflation does not match expected size",
			pooled: []int{6},
		},
		{
			name:   "a corrupt stream goes back",
			spec:   loadSpec{format: FormatRGB, compression: 'z', width: 2, height: 1},
			data:   corrupt(zlibBytes([]byte{1, 2, 3, 4, 5, 6})),
			err:    "EINVAL:Failed to inflate image data with error: zlib: invalid checksum",
			pooled: []int{6},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := new(BufferPool)
			var seeded [][]byte
			for _, size := range tt.seed {
				buf := make([]byte, size)
				seeded = append(seeded, buf)
				pool.Put(buf)
			}
			img, _, err := tt.spec.decode(tt.data, pool)
			if tt.err != "" {
				require.NotNil(t, err)
				assert.Equal(t, tt.err, err.Error())
			} else {
				require.Nil(t, err)
				assert.Equal(t, tt.want, img.Pix)
			}
			for _, i := range tt.drawn {
				buf := seeded[i]
				if len(buf) == len(tt.want) {
					assert.Same(t, &buf[0], &img.Pix[0], "the result is the seeded buffer")
					continue
				}
				// An intermediate was handed back after use.
				found := false
				for _, held := range pool.free {
					found = found || &held[0] == &buf[0]
				}
				assert.True(t, found, "seeded buffer %d was used and handed back", i)
			}
			var pooled []int
			for _, buf := range pool.free {
				pooled = append(pooled, len(buf))
			}
			assert.Equal(t, tt.pooled, pooled, "buffers left for the next transmission")
		})
	}
}

func corrupt(z []byte) []byte {
	z[len(z)-1] ^= 0xff
	return z
}

func zlibBytes(data []byte) []byte {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	_, _ = w.Write(data)
	_ = w.Close()
	return buf.Bytes()
}

func BenchmarkDecodeRGBA(b *testing.B) {
	spec := loadSpec{format: FormatRGBA, width: browserFrameW, height: browserFrameH}
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"opaque", opaqueFrame(browserFrameW, browserFrameH)},
		{"translucent", translucentFrame(browserFrameW, browserFrameH)},
	} {
		b.Run(tt.name, func(b *testing.B) {
			b.SetBytes(int64(len(tt.data)))
			b.ReportAllocs()
			for b.Loop() {
				img, straight, err := spec.decode(tt.data, nil)
				if err != nil {
					b.Fatal(err)
				}
				if straight {
					premultiply(img.Pix)
				}
			}
		})
	}
}
