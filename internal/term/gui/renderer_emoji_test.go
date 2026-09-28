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

package gui

import (
	"image"
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/benchdraw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/term/gui/font"
)

// spyEmojiFace is a colorEmojiFace that records the grapheme clusters
// routed to the color path (as strings), so a renderer test can assert
// an emoji cell takes the color path (Glyph is queried for the whole
// cluster) while ordinary text does not.
type spyEmojiFace struct {
	present    map[string]bool
	glyphCalls []string
}

func (s *spyEmojiFace) Has(cluster []rune) bool {
	return s.present[string(cluster)]
}

func (s *spyEmojiFace) Glyph(cluster []rune, cellW, cellH int) (*image.RGBA, bool) {
	s.glyphCalls = append(s.glyphCalls, string(cluster))
	if !s.present[string(cluster)] {
		return nil, false
	}
	img := image.NewRGBA(image.Rect(0, 0, cellW, cellH))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	return img, true
}

func newEmojiTestRenderer(t *testing.T) *renderer {
	t.Helper()
	m, err := font.NewManager(1, 1)
	require.NoError(t, err)
	const w, h = 320, 120
	return newRenderer(w, h, 1, m, 1, 1, false, term.Attributes{}, term.Attributes{})
}

// TestRendererRoutesEmojiToColorPath asserts a cell holding a color
// emoji is rasterized through the color face (Glyph queried for that
// rune) while an adjacent ASCII cell is not, proving the renderer routes
// emoji to the color path and leaves ordinary text on the mask path.
func TestRendererRoutesEmojiToColorPath(t *testing.T) {
	r := newEmojiTestRenderer(t)
	spy := &spyEmojiFace{present: map[string]bool{"😀": true}}
	r.emojiFace = spy

	cells := [][]term.Cell{{
		{Ch: 'A', Width: 1},
		{Ch: '😀', Width: 2},
	}}
	img := ebiten.NewImage(int(r.font.CellSize.X*8)+8, int(r.font.CellSize.Y)+8)

	benchdraw.BeginFrame(t)
	r.Draw(img, cells, nil, false, term.Coordinates{}, term.CursorStyleDefault, 0, 0)
	benchdraw.EndFrame(t)

	assert.Contains(t, spy.glyphCalls, "😀", "emoji cell must reach the color path")
	assert.NotContains(t, spy.glyphCalls, "A", "ASCII must not reach the color path")
}

// TestRendererRoutesClusterToColorPath asserts a cell holding a
// multi-rune emoji (base rune + combining runes) routes the whole
// cluster to the color path, so composed emoji reach the shaper rather
// than only their base rune.
func TestRendererRoutesClusterToColorPath(t *testing.T) {
	r := newEmojiTestRenderer(t)
	family := "👨\u200d👩\u200d👧"
	spy := &spyEmojiFace{present: map[string]bool{family: true}}
	r.emojiFace = spy

	cell := term.Cell{Ch: '👨', Width: 2}
	cell.SetCombining([]rune{'\u200d', '👩', '\u200d', '👧'})
	cells := [][]term.Cell{{cell}}
	img := ebiten.NewImage(int(r.font.CellSize.X*8)+8, int(r.font.CellSize.Y)+8)

	benchdraw.BeginFrame(t)
	r.Draw(img, cells, nil, false, term.Coordinates{}, term.CursorStyleDefault, 0, 0)
	benchdraw.EndFrame(t)

	assert.Contains(t, spy.glyphCalls, family,
		"the full cluster must reach the color path")
	assert.NotContains(t, spy.glyphCalls, "👨",
		"the base rune alone must not be routed")
}

// TestRendererNilEmojiFaceFallsBack asserts that with no color face the
// renderer draws every cell — emoji included — without panicking, i.e.
// the color path degrades cleanly to the monochrome mask path.
func TestRendererNilEmojiFaceFallsBack(t *testing.T) {
	r := newEmojiTestRenderer(t)
	r.emojiFace = nil

	cells := [][]term.Cell{{
		{Ch: '😀', Width: 2},
		{Ch: 'x', Width: 1},
	}}
	img := ebiten.NewImage(int(r.font.CellSize.X*8)+8, int(r.font.CellSize.Y)+8)

	benchdraw.BeginFrame(t)
	assert.NotPanics(t, func() {
		r.Draw(img, cells, nil, false, term.Coordinates{}, term.CursorStyleDefault, 0, 0)
	})
	benchdraw.EndFrame(t)
}
