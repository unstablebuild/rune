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

package builtinfont

import (
	"bytes"
	"testing"

	gofont "github.com/go-text/typesetting/font"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/font/sfnt"
)

func TestEmojiTTFEmbeddedAndColor(t *testing.T) {
	require.NotEmpty(t, EmojiTTF, "NotoColorEmoji.ttf must be embedded")

	face, err := gofont.ParseTTF(bytes.NewReader(EmojiTTF))
	require.NoError(t, err, "bundled emoji font must parse with go-text")

	gid, ok := face.NominalGlyph('😀')
	require.True(t, ok, "grinning face must resolve to a glyph")

	bm, ok := face.GlyphData(gid).(gofont.GlyphBitmap)
	require.True(t, ok, "grinning face glyph must be a color bitmap")
	assert.Equal(t, gofont.PNG, bm.Format, "Noto color emoji stores PNG bitmaps")
	assert.Positive(t, bm.Width)
	assert.Positive(t, bm.Height)
	assert.NotEmpty(t, bm.Data)
}

func TestCJKTTCEmbedded(t *testing.T) {
	require.NotEmpty(t, CJKTTC, "NotoSansCJK-Regular.ttc must be embedded")

	collection, err := sfnt.ParseCollection(CJKTTC)
	require.NoError(t, err, "bundled CJK collection must parse with sfnt")
	require.Equal(t, 10, collection.NumFonts(),
		"collection ships Sans and Mono for JP, KR, SC, TC and HK")

	f, err := collection.Font(7)
	require.NoError(t, err, "Noto Sans Mono CJK SC must be selectable")

	var buf sfnt.Buffer
	gid, err := f.GlyphIndex(&buf, '中')
	require.NoError(t, err)
	assert.NotZero(t, gid, "U+4E2D must resolve to a glyph")
}
