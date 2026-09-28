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

package codex

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFlagshipModelInCatalog(t *testing.T) {
	_, ok := AvailableModels()[FlagshipModel()]
	assert.True(t, ok, "flagship %q must be in the catalog", FlagshipModel())
	assert.Equal(t, GPT6Astra, FlagshipModel())
}

func TestMaxOutputTokens(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{GPT5Dot6Sol, 0},
		{GPT5Dot6Terra, 0},
		{GPT5Dot6Luna, 0},
		{GPT5Dot5, 0},
		{GPT5Dot4, 0},
		{GPT5Dot3Codex, 0},
		{CodexAutoReview, 0},
		{"unknown-model", 0},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, MaxOutputTokens(tt.model))
		})
	}
}

func TestModelEntries(t *testing.T) {
	entries := ModelEntries()
	byName := make(map[string]int, len(entries))
	for _, e := range entries {
		byName[e.Name] = e.ContextWindow
		assert.Equal(t, LLMProvider, e.Provider)
		assert.Equal(t, OpenAICompatibleURL, e.BaseURL)
	}
	// Catalog names are bare upstream slugs; the router disambiguates
	// duplicates between providers via ModelEntry.Provider.
	assert.Equal(t, "gpt-5.4", GPT5Dot4)
	_, hasBareGPT56 := byName["gpt-5.6"]
	assert.False(t, hasBareGPT56)
	assert.Equal(t, 872000, byName["gpt-6-astra"])
	assert.Equal(t, 872000, byName["gpt-6-sol"])
	assert.Equal(t, 872000, byName["gpt-6-luna"])
	assert.Equal(t, 872000, byName["gpt-5.6-sol"])
	assert.Equal(t, 872000, byName["gpt-5.6-terra"])
	assert.Equal(t, 872000, byName["gpt-5.6-luna"])
	assert.Equal(t, 1000000, byName["gpt-5.4"])
	assert.Equal(t, 272000, byName["gpt-5.5"])
	assert.Equal(t, 1000000, byName["codex-auto-review"])
}

func TestUpstreamModelName(t *testing.T) {
	// On the rune side the catalog already holds upstream slugs so
	// UpstreamModelName is the identity function.
	assert.Equal(t, "gpt-5.4", UpstreamModelName("gpt-5.4"))
	assert.Equal(t, "unknown", UpstreamModelName("unknown"))
}
