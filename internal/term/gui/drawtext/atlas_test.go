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
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/benchdraw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"image"

	xfont "golang.org/x/image/font"
	"golang.org/x/image/math/fixed"

	"unstable.build/rune/internal/term/gui/font"
)

func TestAtlasPacksManyGlyphsIntoFewPages(t *testing.T) {
	m, err := font.NewManager(0, 0)
	require.NoError(t, err)
	face := m.RegularFontFace()

	a := newGlyphAtlas()
	for r := rune(' '); r <= '~'; r++ {
		g := a.get(face, r)
		assert.GreaterOrEqual(t, g.page, 0)
	}
	// The printable ASCII range is small enough to share one page.
	assert.Equal(t, 1, a.pageCount())
	// Every distinct rune is cached exactly once.
	assert.Equal(t, int('~'-' '+1), len(a.cache))

	// A second pass over the same runes re-uses the cache: no new pages
	// and no new cache entries.
	for r := rune(' '); r <= '~'; r++ {
		a.get(face, r)
	}
	assert.Equal(t, 1, a.pageCount())
	assert.Equal(t, int('~'-' '+1), len(a.cache))
}

func TestDrawerDeallocateReleasesAtlasStorage(t *testing.T) {
	d := New()
	d.atlas.pages = []*ebiten.Image{ebiten.NewImage(32, 32)}
	d.atlas.cache[glyphCacheKey{r: 'a'}] = atlasGlyph{}
	d.atlas.scratch = make([]byte, 1024)
	d.colorAtlas.pages = []*ebiten.Image{ebiten.NewImage(32, 32)}
	d.colorAtlas.cache[colorGlyphKey{cluster: "a"}] = colorGlyph{}
	d.vertices = make([]ebiten.Vertex, 4)
	d.indices = make([]uint16, 6)

	d.Deallocate()

	assert.Nil(t, d.atlas)
	assert.Nil(t, d.colorAtlas)
	assert.Nil(t, d.vertices)
	assert.Nil(t, d.indices)
	assert.NotPanics(t, d.Deallocate)
}

func TestDrawerBatchesGlyphRun(t *testing.T) {
	m, err := font.NewManager(0, 0)
	require.NoError(t, err)
	face := m.RegularFontFace()

	d := New()
	dst := ebiten.NewImage(256, 32)
	white := [4]float32{1, 1, 1, 1}

	const run = "hello world batching"
	nonEmpty := 0
	benchdraw.BeginFrame(t)
	x := 0.0
	for _, r := range run {
		d.DrawGlyph(dst, r, face, x, 0, white, false)
		if !d.atlas.get(face, r).empty {
			nonEmpty++
		}
		x += 8
	}
	// All glyphs share one page and blend, so nothing has flushed: the
	// open run holds every non-empty glyph's quad.
	assert.Positive(t, nonEmpty)
	assert.Len(t, d.indices, 6*nonEmpty, "one open run holds every quad")
	assert.Len(t, d.vertices, 4*nonEmpty)
	assert.False(t, d.runAdditive)
	d.Flush(dst)
	benchdraw.EndFrame(t)
	assert.Empty(t, d.indices, "flush closes the run")
}

func TestDrawerSeparatesBlendModes(t *testing.T) {
	m, err := font.NewManager(0, 0)
	require.NoError(t, err)
	face := m.RegularFontFace()

	d := New()
	dst := ebiten.NewImage(64, 32)
	col := [4]float32{1, 1, 1, 1}

	benchdraw.BeginFrame(t)
	d.DrawGlyph(dst, 'a', face, 0, 0, col, false)
	require.Len(t, d.indices, 6, "first glyph opens a normal-blend run")
	require.False(t, d.runAdditive)

	// The additive glyph's differing blend flushes the normal run before
	// opening a new one, so only the additive quad remains buffered.
	d.DrawGlyph(dst, 'b', face, 8, 0, col, true)
	assert.Len(t, d.indices, 6, "blend change flushed the first run")
	assert.True(t, d.runAdditive, "a new additive run is open")
	d.Flush(dst)
	benchdraw.EndFrame(t)
}

func TestAtlasEmptyGlyphAddsNoQuad(t *testing.T) {
	m, err := font.NewManager(0, 0)
	require.NoError(t, err)
	face := m.RegularFontFace()

	d := New()
	dst := ebiten.NewImage(16, 16)
	require.True(t, d.atlas.get(face, ' ').empty, "space is a zero-size glyph")
	benchdraw.BeginFrame(t)
	d.DrawGlyph(dst, ' ', face, 0, 0, [4]float32{1, 1, 1, 1}, false)
	assert.Empty(t, d.indices, "an empty glyph buffers no quad")
	d.Flush(dst)
	benchdraw.EndFrame(t)
}

func TestAtlasRasterizesTightlyPackedMask(t *testing.T) {
	m, err := font.NewManager(0, 0)
	require.NoError(t, err)
	face := m.RegularFontFace()

	// A normal letter is far narrower than a cell-sized scratch, which
	// is exactly the case the stride bug corrupted.
	const r = 'R'
	b, _, _ := face.GlyphBounds(r)
	offset := fixed.Point26_6{X: b.Min.X & ((1 << 6) - 1), Y: b.Min.Y & ((1 << 6) - 1)}
	w, h := (b.Max.X - b.Min.X).Ceil(), (b.Max.Y - b.Min.Y).Ceil()
	if offset.X != 0 {
		w++
	}
	if offset.Y != 0 {
		h++
	}
	require.Positive(t, w)
	require.Positive(t, h)
	require.Less(t, w, 64, "letter must be narrower than a cell-sized scratch to exercise the stride path")

	got := newGlyphAtlas().rasterizeMask(face, r, b, offset, w, h)
	require.Len(t, got, 4*w*h, "mask must be a tightly packed 4*w*h run")

	// Independent reference rasterization into a tightly packed RGBA.
	want := image.NewRGBA(image.Rect(0, 0, w, h))
	require.Equal(t, 4*w, want.Stride)
	fd := xfont.Drawer{Dst: want, Src: image.White, Face: face}
	fd.Dot = fixed.Point26_6{X: -b.Min.X + offset.X, Y: -b.Min.Y + offset.Y}
	fd.DrawString(string(rune(r)))

	var cov int
	for i := 3; i < len(got); i += 4 {
		if want.Pix[i] != 0 {
			cov++
		}
	}
	require.Positive(t, cov, "reference glyph must have coverage")
	assert.Equal(t, want.Pix, got, "packed mask must match the reference rasterization row for row")
}
