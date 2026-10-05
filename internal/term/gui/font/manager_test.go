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

package font

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaultFont(t *testing.T) {
	m, err := NewManager(0, 0)
	require.NoError(t, err)
	assert.NotPanics(t, func() {
		assert.NotNil(t, m.RegularFontFace())
	})
}

func TestPixelAndCellCalculation(t *testing.T) {
	t.Run("doesn't panic", func(t *testing.T) {
		m, err := NewManager(100, 2)
		require.NoError(t, err)
		assert.NotPanics(t, func() {
			m.PixelX(-10)
			m.PixelY(-10)
			m.CellX(-10)
			m.CellY(-10)
		})
	})

	t.Run("PixelX/Y returns the pixel corresponding to the given cell", func(t *testing.T) {
		m, err := NewManager(2, 1)
		require.NoError(t, err)
		setTestCharSize(m, 21, 43)
		assert.NotNil(t, m.RegularFontFace())
		assert.Equal(t, float64(190), m.PixelX(10))
		assert.Equal(t, float64(420), m.PixelY(10))
	})

	t.Run("CellX/Y returns the cell corresponding to the given pixel", func(t *testing.T) {
		m, err := NewManager(2, 1)
		require.NoError(t, err)
		setTestCharSize(m, 21, 43)
		assert.NotNil(t, m.RegularFontFace())
		assert.Equal(t, 10, m.CellX(190))
		assert.Equal(t, 10, m.CellY(420))
	})
}

func TestSetOffsetXYMixup(t *testing.T) {
	m, err := NewManager(0, 0)
	require.NoError(t, err)
	// ensure font is loaded so SetOffset's ReloadFont path is exercised.
	require.NotNil(t, m.RegularFontFace())

	const wantX = 3.0
	const wantY = 7.0
	require.NoError(t, m.SetOffset(wantX, wantY))

	assert.Equal(t, wantX, fixedToFloat64(m.offset.X),
		"SetOffset must store the x argument in offset.X, not the y argument")
	assert.Equal(t, wantY, fixedToFloat64(m.offset.Y),
		"SetOffset must store the y argument in offset.Y")
}

func TestSetOffsetAxisPreservesOtherAxis(t *testing.T) {
	m, err := NewManager(0, 0)
	require.NoError(t, err)
	// ensure font is loaded so SetOffset's ReloadFont path is exercised.
	require.NotNil(t, m.RegularFontFace())

	require.NoError(t, m.SetOffset(3, 7))
	require.NoError(t, m.SetOffsetX(5))
	assert.Equal(t, 5.0, fixedToFloat64(m.offset.X))
	assert.Equal(t, 7.0, fixedToFloat64(m.offset.Y))

	require.NoError(t, m.SetOffsetY(9))
	assert.Equal(t, 5.0, fixedToFloat64(m.offset.X))
	assert.Equal(t, 9.0, fixedToFloat64(m.offset.Y))
}

func TestCellWidthOffsetPreservesLineHeightOffset(t *testing.T) {
	m, err := NewManager(0, 0)
	require.NoError(t, err)
	// ensure font is loaded so SetOffset's ReloadFont path is exercised.
	require.NotNil(t, m.RegularFontFace())

	require.NoError(t, m.SetOffset(3, 7))
	require.NoError(t, m.IncreaseCellWidth())
	assert.Equal(t, 4.0, fixedToFloat64(m.offset.X))
	assert.Equal(t, 7.0, fixedToFloat64(m.offset.Y))

	require.NoError(t, m.DecreaseCellWidth())
	assert.Equal(t, 3.0, fixedToFloat64(m.offset.X))
	assert.Equal(t, 7.0, fixedToFloat64(m.offset.Y))
}

func TestSetOffsetInvalidMetricsRollsBack(t *testing.T) {
	m, err := NewManager(0, 0)
	require.NoError(t, err)
	require.NotNil(t, m.RegularFontFace())

	goodCharSize := m.CharSize()
	prevOffsetX := fixedToFloat64(m.offset.X)
	prevOffsetY := fixedToFloat64(m.offset.Y)

	// An offset more negative than the widest glyph advance collapses
	// charSize.X to zero, which is rejected by setFaceMetrics.
	err = m.SetOffset(-1000, 0)
	require.Error(t, err,
		"SetOffset must surface the invalid-metrics error instead of panicking")
	assert.Contains(t, err.Error(), "invalid font metrics")

	// Offset must be rolled back to the previous value.
	assert.Equal(t, prevOffsetX, fixedToFloat64(m.offset.X),
		"offset.X must be restored after rollback")
	assert.Equal(t, prevOffsetY, fixedToFloat64(m.offset.Y),
		"offset.Y must be restored after rollback")

	// Metrics must be restored to the previously-known-good values, so
	// subsequent CellsWidth/Height calls are safe.
	assert.Equal(t, goodCharSize, m.CharSize(),
		"charSize must be restored after rollback")
	assert.NotPanics(t, func() {
		_ = m.CellsWidth(1200)
		_ = m.CellsHeight(900)
	})
}

func TestSetFontByFamilyNameRestoresOnError(t *testing.T) {
	m, err := NewManager(0, 0)
	require.NoError(t, err)
	require.NotNil(t, m.RegularFontFace())
	goodCharSize := m.CharSize()

	err = m.SetFontByFamilyName("this-font-family-does-not-exist-RUNE-51")
	require.Error(t, err)

	// Manager must remain usable with the previous (builtin) font.
	assert.Equal(t, "", m.FontFamily(),
		"family must be rolled back to builtin fallback")
	assert.Equal(t, goodCharSize, m.CharSize(),
		"charSize must be the builtin fallback's metrics after rollback")
	assert.NotPanics(t, func() {
		_ = m.CellsWidth(1200)
		_ = m.CellsHeight(900)
	})
}

func setTestCharSize(m *Manager, x, y float64) {
	m.ensureFontLoaded()
	m.charSize.X = x
	m.charSize.Y = y
}

func TestSymbolFallbackResolvesGapGlyphs(t *testing.T) {
	m, err := NewManager(0, 0)
	require.NoError(t, err)
	face := m.RegularFontFace()
	require.NotNil(t, face)

	gaps := map[string]rune{
		"BLACK_CIRCLE_MAC (U+23FA)": '⏺',
		"PAUSE (U+23F8)":            '⏸',
		"REFERENCE_MARK (U+203B)":   '※',
		"FORK (U+2442)":             '⑂',
	}
	for name, r := range gaps {
		_, ok := face.GlyphAdvance(r)
		assert.True(t, ok, "glyph %s must resolve through the font fallback chain", name)
	}
}

func TestCJKFallbackResolvesGlyphs(t *testing.T) {
	m, err := NewManager(0, 0)
	require.NoError(t, err)
	face := m.RegularFontFace()
	require.NotNil(t, face)

	glyphs := map[string]rune{
		"CJK_IDEOGRAPH_ZHONG (U+4E2D)":   '中',
		"CJK_IDEOGRAPH_SHUI (U+6C34)":    '水',
		"HIRAGANA_A (U+3042)":            'あ',
		"KATAKANA_KA (U+30AB)":           'カ',
		"HANGUL_HAN (U+D55C)":            '한',
		"IDEOGRAPHIC_FULL_STOP (U+3002)": '。',
		"FULLWIDTH_COMMA (U+FF0C)":       '，',
		"HALFWIDTH_KATAKANA_A (U+FF71)":  'ｱ',
	}
	for name, r := range glyphs {
		_, ok := face.GlyphAdvance(r)
		assert.True(t, ok, "glyph %s must resolve through the font fallback chain", name)
	}
}

func TestCJKIdeographAdvanceMatchesTwoCells(t *testing.T) {
	for _, size := range []float64{10, 14, 20} {
		t.Run(fmt.Sprintf("size=%v", size), func(t *testing.T) {
			m, err := NewManager(0, 0)
			require.NoError(t, err)
			require.NoError(t, m.SetSize(size))

			advance, ok := m.RegularFontFace().GlyphAdvance('水')
			require.True(t, ok)

			want := 2 * m.CharSize().X
			assert.InDelta(t, want, float64(advance)/(1<<6), 1.0,
				"ideograph advance must span two cells")
		})
	}
}

func TestDefaultSizeForScale(t *testing.T) {
	cases := []struct {
		scale float64
		want  float64
	}{
		{0.75, 15},
		{1.0, 15},
		{1.5, 14},
		{1.75, 13.5},
		{2.0, 13},
		{3.0, 13},
	}
	for _, tc := range cases {
		assert.InDelta(t, tc.want, defaultSizeForScale(tc.scale), 1e-9,
			"scale %v", tc.scale)
	}
}

func TestSetSizeZeroResolvesDefaultForDeviceScale(t *testing.T) {
	m, err := NewManager(0, 0)
	require.NoError(t, err)

	m.SetDeviceScale(1)
	require.NoError(t, m.SetSize(0))
	assert.Equal(t, float64(15), m.size)

	m.SetDeviceScale(2)
	require.NoError(t, m.SetSize(0))
	assert.Equal(t, float64(13), m.size)
}

func TestSetSizeExplicitIgnoresDeviceScale(t *testing.T) {
	m, err := NewManager(0, 0)
	require.NoError(t, err)

	m.SetDeviceScale(1)
	require.NoError(t, m.SetSize(13))
	assert.Equal(t, float64(13), m.size)
}
