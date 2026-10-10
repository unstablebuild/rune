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

package drawtext

import (
	"image"
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/benchdraw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unstable.build/rune/internal/term/gui/font"
)

// fakeColorSource is a deterministic ColorGlyphSource for tests. It
// returns a solid, fully opaque RGBA sized to the requested box for any
// cluster whose string is present, and reports absence otherwise, so
// tests exercise the color path without depending on a real emoji font.
type fakeColorSource struct {
	present map[string]bool
	calls   int
}

func (s *fakeColorSource) Glyph(cluster []rune, cellW, cellH int) (*image.RGBA, bool) {
	s.calls++
	if !s.present[string(cluster)] {
		return nil, false
	}
	img := image.NewRGBA(image.Rect(0, 0, cellW, cellH))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	return img, true
}

func TestColorAtlasCachesGlyph(t *testing.T) {
	src := &fakeColorSource{present: map[string]bool{"😀": true}}
	a := newColorAtlas()

	g1 := a.get(src, []rune{'😀'}, 12, 24)
	require.False(t, g1.empty)
	assert.Equal(t, 1, a.pageCount())
	assert.Equal(t, 1, src.calls, "first lookup rasterizes once")

	g2 := a.get(src, []rune{'😀'}, 12, 24)
	assert.Equal(t, g1.page, g2.page)
	assert.Equal(t, g1.rect, g2.rect, "cached lookup reuses the same rect")
	assert.Equal(t, 1, src.calls, "cache hit does not rasterize again")

	// A different cell size is a distinct key and packs a new glyph.
	a.get(src, []rune{'😀'}, 16, 32)
	assert.Equal(t, 2, src.calls, "size change is a new key")
}

func TestColorAtlasAbsentGlyphCachedEmpty(t *testing.T) {
	src := &fakeColorSource{present: map[string]bool{}}
	a := newColorAtlas()

	g := a.get(src, []rune{'A'}, 12, 24)
	assert.True(t, g.empty)
	assert.Equal(t, 0, a.pageCount(), "absent glyph opens no page")

	a.get(src, []rune{'A'}, 12, 24)
	assert.Equal(t, 1, src.calls, "absent result is cached")
}

func TestDrawColorGlyphIdentityColor(t *testing.T) {
	src := &fakeColorSource{present: map[string]bool{"😀": true}}
	d := New()
	dst := ebiten.NewImage(64, 32)

	benchdraw.BeginFrame(t)
	d.DrawColorGlyph(dst, []rune{'😀'}, src, 10, 4, 12, 24)
	require.Len(t, d.vertices, 4)
	require.Len(t, d.indices, 6)
	assert.True(t, d.runColor, "a color run is open")

	for _, v := range d.vertices {
		assert.Equal(t, float32(1), v.ColorR)
		assert.Equal(t, float32(1), v.ColorG)
		assert.Equal(t, float32(1), v.ColorB)
		assert.Equal(t, float32(1), v.ColorA)
	}
	// Bitmap fills the whole cell here (offset 0), so the quad spans the
	// cell box exactly from its top-left origin.
	assert.Equal(t, float32(10), d.vertices[0].DstX)
	assert.Equal(t, float32(4), d.vertices[0].DstY)
	assert.Equal(t, float32(22), d.vertices[3].DstX)
	assert.Equal(t, float32(28), d.vertices[3].DstY)
	d.Flush(dst)
	benchdraw.EndFrame(t)
	assert.Empty(t, d.indices, "flush closes the run")
}

func TestDrawColorGlyphBatchesRun(t *testing.T) {
	src := &fakeColorSource{present: map[string]bool{"😀": true, "🚀": true}}
	d := New()
	dst := ebiten.NewImage(128, 32)

	benchdraw.BeginFrame(t)
	d.DrawColorGlyph(dst, []rune{'😀'}, src, 0, 0, 12, 24)
	d.DrawColorGlyph(dst, []rune{'🚀'}, src, 12, 0, 12, 24)
	assert.Len(t, d.indices, 12, "both glyphs share one open run")
	assert.Len(t, d.vertices, 8)
	d.Flush(dst)
	benchdraw.EndFrame(t)
}

func TestDrawColorGlyphSeparatesFromMask(t *testing.T) {
	m, err := font.NewManager(0, 0)
	require.NoError(t, err)
	face := m.RegularFontFace()
	src := &fakeColorSource{present: map[string]bool{"😀": true}}

	d := New()
	dst := ebiten.NewImage(64, 32)
	white := [4]float32{1, 1, 1, 1}

	benchdraw.BeginFrame(t)
	// A mask glyph opens a mask run.
	d.DrawGlyph(dst, 'a', face, 0, 0, white, false)
	require.Len(t, d.indices, 6)
	require.False(t, d.runColor)

	// The color glyph flushes the mask run before opening a color run,
	// so only the color quad remains buffered.
	d.DrawColorGlyph(dst, []rune{'😀'}, src, 8, 0, 12, 24)
	assert.Len(t, d.indices, 6, "switch to color flushed the mask run")
	assert.True(t, d.runColor, "a color run is now open")

	// Switching back to the mask path flushes the color run.
	d.DrawGlyph(dst, 'b', face, 20, 0, white, false)
	assert.Len(t, d.indices, 6, "switch back to mask flushed the color run")
	assert.False(t, d.runColor)
	d.Flush(dst)
	benchdraw.EndFrame(t)
}

func TestDrawColorGlyphEmptyAddsNoQuad(t *testing.T) {
	src := &fakeColorSource{present: map[string]bool{}}
	d := New()
	dst := ebiten.NewImage(16, 16)

	benchdraw.BeginFrame(t)
	d.DrawColorGlyph(dst, []rune{'A'}, src, 0, 0, 12, 24)
	assert.Empty(t, d.indices, "an absent color glyph buffers no quad")
	d.Flush(dst)
	benchdraw.EndFrame(t)
}
