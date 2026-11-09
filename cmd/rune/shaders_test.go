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

package main

import (
	"context"
	"image"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/shader"
)

func TestOpenShaderBurnsThePicturesOfTheLoadingScreenIntoTheNewOnes(t *testing.T) {
	const cols, rows = 120, 40
	cellSize := func() (int, int) { return 9, 20 }
	ctx := cell.ContextWithPixelSize(context.Background(), cell.PixelSize{Width: 9, Height: 20})
	draw := func(theme string) ([][]term.Cell, []term.Image) {
		wallpaper := makeThemedWallpaper(func() string { return theme }, cellSize).NewComponent()
		wallpaper.Resize(cols, rows)
		buf := cell.NewBufferWriter(ctx, cols, rows)
		wallpaper.Draw(buf)
		return buf.RawCells(), buf.Images()
	}
	pixels := func(images []term.Image) [][]uint8 {
		var out [][]uint8
		for _, img := range images {
			out = append(out, img.Src.(*image.RGBA).Pix)
		}
		return out
	}
	defAttr := term.Attributes{Fg: term.NewRGBColor(220, 220, 220), Bg: term.NewRGBColor(12, 12, 20)}
	oldCells, oldImages := draw("default")
	newCells, newImages := draw("hopper")
	require.Len(t, oldImages, 1)
	require.Len(t, newImages, 1)

	loading := loadingShader(defAttr).(*shader.GrayFadeShader)
	loadingTotal := int(loadingShaderDuration / (time.Second / loadingShaderFPS))
	loaded := shader.ShadeImages(loading, 15, loadingTotal, term.CloneCells(oldCells), slices.Clone(oldImages))
	require.Positive(t, loading.Progress())
	require.Less(t, loading.Progress(), 1.0)

	open := openShader(defAttr, shader.Snapshot{
		Cells: oldCells, Images: oldImages, Desaturation: loading.Progress(),
	})
	total := int(openShaderDuration / (time.Second / openShaderFPS))
	shade := func(frame int) []term.Image {
		return shader.ShadeImages(open, frame, total, term.CloneCells(newCells), slices.Clone(newImages))
	}

	first := shade(0)
	require.Len(t, first, 1)
	assert.Equal(t, loaded[0].Visible(), first[0].Visible())
	assert.Equal(t, pixels(loaded), pixels(first), "the burn starts on the pictures as the loading left them")

	both := false
	for frame := 1; frame < total-1; frame++ {
		var old, shown bool
		for _, img := range shade(frame) {
			old = old || img.ID != newImages[0].ID
			shown = shown || img.ID == newImages[0].ID
		}
		both = both || old && shown
	}
	assert.True(t, both, "the new picture is never revealed while the old one burns")
	assert.Equal(t, newImages, shade(total-1), "the burn ends on the new screen's pictures")
}
