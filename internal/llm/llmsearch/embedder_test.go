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
	"context"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

func TestEmbedderInfoVectorSource(t *testing.T) {
	tests := []struct {
		name string
		info EmbedderInfo
		want string
	}{
		{"long revision", EmbedderInfo{Model: "m", Revision: "0123456789abcdef", Runtime: "r", Dtype: "Q8_0"}, "m@0123456 r Q8_0"},
		{"short revision", EmbedderInfo{Model: "m", Revision: "abc", Runtime: "r", Dtype: "fp32"}, "m@abc r fp32"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.info.VectorSource())
		})
	}
}

func TestLlamaInfo(t *testing.T) {
	info := llamaInfo(llmapi.ModelEntry{
		Name:    testModel,
		BaseURL: "/models/blobs/sha256-0123456789abcdef0123456789abcdef",
	})
	assert.Equal(t, EmbedderInfo{
		Model: testModel, Revision: "0123456789abcdef0123456789abcdef", Runtime: llamaRuntime, Dtype: "Q8_0", Dim: dims,
	}, info)
	assert.Equal(t, testModel+"@0123456 llama-server-vectors1 Q8_0", info.VectorSource())
}

func TestFakeEmbedder(t *testing.T) {
	e := NewFakeEmbedderFactory()(llmapi.ModelEntry{})
	vecs, err := e.EmbedTexts(context.Background(), []string{
		"retry with exponential backoff",
		"retry with exponential backoff and jitter",
		"great circle distance in kilometres",
		"",
	})
	require.NoError(t, err)
	require.Len(t, vecs, 4)
	dot := func(a, b []float32) float64 {
		s := 0.0
		for i := range a {
			s += float64(a[i] * b[i])
		}
		return s
	}
	for _, v := range vecs[:3] {
		require.Len(t, v, dims)
		assert.InDelta(t, 1, math.Sqrt(dot(v, v)), 1e-6)
	}
	assert.Greater(t, dot(vecs[0], vecs[1]), dot(vecs[0], vecs[2]), "texts sharing words are closer")
	assert.Zero(t, dot(vecs[3], vecs[3]), "a text without words has no direction")
	again, err := e.EmbedTexts(context.Background(), []string{"retry with exponential backoff"})
	require.NoError(t, err)
	assert.Equal(t, vecs[0], again[0])
}
