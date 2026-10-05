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

package drawrect

import (
	"image/color"
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/benchdraw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBatchAccumulatesRects(t *testing.T) {
	Init()
	var b Batch
	assert.True(t, b.Empty())

	red := color.RGBA{R: 255, A: 255}
	blue := color.RGBA{B: 255, A: 255}
	b.AddRect(0, 0, 10, 10, red)
	b.AddRect(20, 0, 10, 10, blue)
	require.False(t, b.Empty())

	// Two filled quads: 4 vertices and 6 indices (two triangles) each.
	assert.Len(t, b.vertices, 8)
	assert.Len(t, b.indices, 12)
	// Per-quad colors are baked into the vertices.
	assert.Equal(t, float32(1), b.vertices[0].ColorR)
	assert.Equal(t, float32(0), b.vertices[0].ColorB)
	assert.Equal(t, float32(1), b.vertices[4].ColorB)
	assert.Equal(t, float32(0), b.vertices[4].ColorR)
	// The second quad's six indices are offset past the first quad's
	// four vertices, so both triangles reference the correct vertices.
	for _, idx := range b.indices[6:] {
		assert.GreaterOrEqual(t, idx, uint16(4))
	}

	// One Flush of a non-empty batch issues exactly one DrawTriangles by
	// construction and then clears the batch.
	dst := ebiten.NewImage(64, 16)
	benchdraw.BeginFrame(t)
	b.Flush(dst)
	benchdraw.EndFrame(t)
	assert.True(t, b.Empty(), "flush resets the batch")
}

func TestBatchFlushEmptyNoOp(t *testing.T) {
	Init()
	var b Batch
	dst := ebiten.NewImage(8, 8)
	require.True(t, b.Empty())
	benchdraw.BeginFrame(t)
	b.Flush(dst)
	benchdraw.EndFrame(t)
	assert.True(t, b.Empty(), "flushing an empty batch is a no-op")
}
