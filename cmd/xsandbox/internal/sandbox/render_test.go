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

package sandbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"unstable.build/rune/internal/handler/handlerrpc/handlerrpctest"
)

func TestRenderResponsive(t *testing.T) {
	c := component.NewResponsiveString("the quick brown fox jumps over the lazy dog",
		component.StringResponsiveConfig{})
	assert.Equal(t, "the quick brown\n fox jumps over\n the lazy dog", renderResponsive(c, 15))
}

func TestBrowserHostRenderGraphics(t *testing.T) {
	h := newBrowserHost(defaultRenderWidth, defaultRenderHeight)
	t.Cleanup(func() { _ = h.Close() })
	raster := handlerrpctest.NewRasterHandler()
	_, ok := h.comp.Split(browserapi.OrientationRight, h.comp.Focus(), raster)
	require.True(t, ok)

	out := h.renderGraphics(80, 24, 9, 18)
	require.Len(t, out.Images, 1)
	img := out.Images[0]
	assert.Positive(t, img.X, "the split is right of the window it split")
	assert.Positive(t, img.Width)
	assert.Positive(t, img.Height)
	assert.Equal(t, img.Width*9, img.PixelWidth, "the handler drew at the frame's cell size")
	assert.Equal(t, img.Height*18, img.PixelHeight)
	assert.Greater(t, img.Colors, 1)
	assert.Equal(t, []handlerrpctest.CellPixelSize{{Width: 9, Height: 18}}, raster.CellPixelSizes())

	text := h.render(80, 24)
	assert.Contains(t, text, "F", "a render without a cell size draws the handler's fallback")
}
