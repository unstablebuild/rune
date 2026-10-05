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

package emoji

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"unstable.build/rune/internal/term/gui/font/builtinfont"
)

func newTestFace(t *testing.T) *Face {
	t.Helper()
	f, err := NewFace(builtinfont.EmojiTTF)
	require.NoError(t, err)
	require.NotNil(t, f)
	return f
}

func TestHasDistinguishesColorEmoji(t *testing.T) {
	f := newTestFace(t)

	for _, r := range []rune{'😀', '🚀', '🎉'} {
		assert.Truef(t, f.Has([]rune{r}), "%c must be a color emoji", r)
	}
	// '❤' (U+2764) has default *text* presentation (width 1), so it must
	// stay on the monochrome path; only its variation-selected form '❤️'
	// is emoji-presented. Colorizing the bare heart would regress
	// ordinary text, so it belongs with the non-emoji runes.
	for _, r := range []rune{'A', '中', ' ', '1', 'z', '❤'} {
		assert.Falsef(t, f.Has([]rune{r}), "%c must not be treated as a color emoji", r)
	}
}

func TestHasComposesMultiRuneClusters(t *testing.T) {
	f := newTestFace(t)

	colored := [][]rune{
		{'\U0001F91F', '\U0001F3FC'},                                   // rock-on + medium-light skin tone
		{'\U0001F468', '\u200D', '\U0001F469', '\u200D', '\U0001F467'}, // family (ZWJ)
		{'\u2764', '\uFE0F'},                                           // heart + emoji variation selector
		{'\U0001F1EB', '\U0001F1F7'},                                   // regional-indicator flag
	}
	for _, cl := range colored {
		assert.Truef(t, f.Has(cl), "%q must be a color emoji cluster", string(cl))
	}
}

func TestHasRejectsTextClustersWithCombining(t *testing.T) {
	f := newTestFace(t)

	for _, cl := range [][]rune{
		{'1', '\uFE0F', '\u20E3'}, // keycap "1" (width 1)
		{'❤'},                     // bare heart, text presentation
		{'1'},                     // bare digit
	} {
		assert.Falsef(t, f.Has(cl), "%q must not be a color emoji cluster", string(cl))
	}
}

func TestHasRejectsClusterFontCannotLigate(t *testing.T) {
	f := newTestFace(t)

	assert.False(t, f.Has([]rune{'\U0001F468', '\u200D', '\U0001F469'}))
}

// systemEmojiFace opens the host's color-emoji font, skipping when there
// is none. The bundled Noto consumes a variation selector while shaping,
// so only a system face exercises fonts that keep it as its own glyph.
func systemEmojiFace(t *testing.T) *Face {
	t.Helper()
	for _, path := range []string{
		"/System/Library/Fonts/Apple Color Emoji.ttc",
		"/usr/share/fonts/truetype/noto/NotoColorEmoji.ttf",
		"/usr/share/fonts/noto/NotoColorEmoji.ttf",
	} {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if f, err := NewFaceFromFile(path); err == nil {
			return f
		}
	}
	t.Skip("no system color-emoji font on this host")
	return nil
}

func TestHasAcceptsClusterWhenFontKeepsIgnorableGlyph(t *testing.T) {
	f := systemEmojiFace(t)

	for _, cl := range [][]rune{
		{'\U0001F577', '\uFE0F'}, // spider, default text presentation
		{'\u23F1', '\uFE0F'},     // stopwatch
		{'\u2764', '\uFE0F'},     // heart
	} {
		assert.Truef(t, f.Has(cl), "%q must be a color emoji cluster", string(cl))
	}
}

func TestGlyphProducesColorRGBA(t *testing.T) {
	f := newTestFace(t)

	const cellW, cellH = 12, 24
	img, ok := f.Glyph([]rune{'😀'}, cellW, cellH)
	require.True(t, ok)
	require.NotNil(t, img)

	// Fits within the cell box and is non-degenerate.
	assert.LessOrEqual(t, img.Bounds().Dx(), cellW)
	assert.LessOrEqual(t, img.Bounds().Dy(), cellH)
	assert.Positive(t, img.Bounds().Dx())
	assert.Positive(t, img.Bounds().Dy())
	// Tightly packed so it can be uploaded as one contiguous run.
	assert.Equal(t, 4*img.Bounds().Dx(), img.Stride)

	var chroma, opaque int
	for i := 0; i+3 < len(img.Pix); i += 4 {
		r, g, b, a := img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3]
		// Premultiplied invariant: no channel may exceed alpha.
		require.LessOrEqual(t, r, a)
		require.LessOrEqual(t, g, a)
		require.LessOrEqual(t, b, a)
		if a > 0 {
			opaque++
		}
		if r != g || g != b {
			chroma++
		}
	}
	assert.Positive(t, opaque, "glyph must have visible pixels")
	assert.Positive(t, chroma, "glyph must be multi-color, not grayscale")
}

func TestGlyphComposesClusterBitmap(t *testing.T) {
	f := newTestFace(t)
	const cellW, cellH = 24, 24

	family := []rune{'👨', '\u200d', '👩', '\u200d', '👧'}
	composed, ok := f.Glyph(family, cellW, cellH)
	require.True(t, ok)
	require.NotNil(t, composed)

	base, ok := f.Glyph([]rune{'👨'}, cellW, cellH)
	require.True(t, ok)

	assert.NotEqual(t, base.Pix, composed.Pix,
		"composed family glyph must differ from the base man glyph")
}

func TestGlyphUnknownRune(t *testing.T) {
	f := newTestFace(t)
	img, ok := f.Glyph([]rune{'A'}, 12, 24)
	assert.False(t, ok)
	assert.Nil(t, img)
}

func TestNewFaceRejectsCorruptFont(t *testing.T) {
	f, err := NewFace([]byte("not a font"))
	assert.Error(t, err)
	assert.Nil(t, f)

	f, err = NewFace(nil)
	assert.Error(t, err)
	assert.Nil(t, f)
}

func TestGlyphResultIsCopy(t *testing.T) {
	f := newTestFace(t)
	a, ok := f.Glyph([]rune{'😀'}, 12, 24)
	require.True(t, ok)
	first := append([]byte(nil), a.Pix...)

	b, ok := f.Glyph([]rune{'🚀'}, 12, 24)
	require.True(t, ok)

	assert.NotSame(t, a, b)
	assert.Equal(t, first, a.Pix, "first glyph must be untouched by the second decode")
}

// writeBundledFont writes the embedded Noto Color Emoji bytes to a temp
// file so NewFaceFromFile can be exercised against the file/collection
// path without depending on any system-installed emoji font.
func writeBundledFont(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "NotoColorEmoji.ttf")
	require.NoError(t, os.WriteFile(path, builtinfont.EmojiTTF, 0o600))
	return path
}

func TestNewFaceFromFileLoadsColorFont(t *testing.T) {
	f, err := NewFaceFromFile(writeBundledFont(t))
	require.NoError(t, err)
	require.NotNil(t, f)

	assert.True(t, f.Has([]rune{'😀'}))
	assert.False(t, f.Has([]rune{'A'}))

	img, ok := f.Glyph([]rune{'😀'}, 12, 24)
	require.True(t, ok)
	require.NotNil(t, img)
}

func TestNewFaceFromFileMissing(t *testing.T) {
	f, err := NewFaceFromFile(filepath.Join(t.TempDir(), "does-not-exist.ttf"))
	assert.Error(t, err)
	assert.Nil(t, f)
}

func TestNewFaceFromFileRejectsNonFont(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notafont.ttf")
	require.NoError(t, os.WriteFile(path, []byte("not a font"), 0o600))

	f, err := NewFaceFromFile(path)
	assert.Error(t, err)
	assert.Nil(t, f)
}
