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

package llmsearch

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHalf_RoundTripsEveryValue(t *testing.T) {
	for h := range 1 << 16 {
		f := halfToFloat32(uint16(h))
		if math.IsNaN(float64(f)) {
			continue
		}
		require.Equal(t, uint16(h), float32ToHalf(f), "half %#04x (%v)", h, f)
	}
}

func TestFloat32ToHalf(t *testing.T) {
	tests := []struct {
		name string
		in   float32
		want float32
	}{
		{"one", 1, 1},
		{"largest", 65504, 65504},
		{"tie rounds to even down", 1 + 1.0/2048, 1},
		{"tie rounds to even up", 1 + 3.0/2048, 1 + 1.0/512},
		{"overflow tie is infinity", 65520, float32(math.Inf(1))},
		{"subnormal", 1.0 / (1 << 24), 1.0 / (1 << 24)},
		{"subnormal tie to zero", 1.0 / (1 << 25), 0},
		{"subnormal tie to even", 3.0 / (1 << 25), 1.0 / (1 << 23)},
		{"negative", -0.5, -0.5},
		{"too small", 1e-10, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, halfToFloat32(float32ToHalf(tt.in)))
		})
	}
	assert.True(t, math.IsNaN(float64(halfToFloat32(float32ToHalf(float32(math.NaN()))))))
}

func unitVector(i int) []float32 {
	v := make([]float32, dims)
	v[i%dims] = 1
	return v
}

func TestVectorMatrix_ScoreAcrossBlocks(t *testing.T) {
	var m vectorMatrix
	rows := vectorBlockRows + 1
	for r := range rows {
		require.NoError(t, m.add(uint32(r), uint32(r%7), halfBytes(unitVector(r))))
	}
	scores := m.score(unitVector(rows - 1))
	require.Len(t, scores, rows)
	for r, s := range scores {
		want := float32(0)
		if r%dims == (rows-1)%dims {
			want = 1
		}
		assert.Equal(t, want, s, "row %d", r)
	}

	assert.Error(t, m.add(1, 1, []byte{1, 2}))
}

func TestVectorMatrix_RemoveFile(t *testing.T) {
	var m vectorMatrix
	for r, file := range []uint32{1, 2, 1, 3, 2} {
		require.NoError(t, m.add(uint32(10+r), file, halfBytes(unitVector(r))))
	}
	m.removeFile(2)
	assert.Equal(t, []uint32{10, 12, 13}, m.rowSeg)
	assert.Equal(t, []uint32{1, 1, 3}, m.rowFile)
	assert.Equal(t, []float32{0, 0, 1}, m.score(unitVector(3)))
	assert.Equal(t, []float32{0, 1, 0}, m.score(unitVector(2)))
}

func TestMeaning(t *testing.T) {
	tests := []struct {
		name   string
		scores []float32
		want   []float64
	}{
		{
			name:   "standard deviations from the average",
			scores: []float32{0.1, 0.2, 0.3, 0.4, 0.5},
			want:   []float64{-math.Sqrt2, -math.Sqrt2 / 2, 0, math.Sqrt2 / 2, math.Sqrt2},
		},
		{
			name:   "deviation floor",
			scores: []float32{0.5, 0.5, 0.5, 0.5, 0.51},
			want:   []float64{-0.1, -0.1, -0.1, -0.1, 0.4},
		},
		{
			name: "no scores",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := meaning(tt.scores)
			require.Len(t, got, len(tt.want))
			for i := range got {
				assert.InDelta(t, tt.want[i], got[i], 1e-5, "row %d", i)
			}
		})
	}
}
