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

package openai

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFlagshipModelInCatalog(t *testing.T) {
	_, ok := AvailableModels()[FlagshipModel()]
	assert.True(t, ok, "flagship %q must be in the catalog", FlagshipModel())
	assert.Equal(t, GPT6Astra, FlagshipModel())
}

func TestGPT6AstraCatalog(t *testing.T) {
	assert.Equal(t, 1050000, AvailableModels()[GPT6Astra])
	assert.Equal(t, 128000, MaxOutputTokens(GPT6Astra))
	assert.True(t, SupportsReasoning(GPT6Astra))
}

func TestMaxOutputTokens(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{GPT5Dot6, 128000},
		{GPT6Astra, 128000},
		{GPT5Dot6Sol, 128000},
		{GPT5Dot6Terra, 128000},
		{GPT5Dot6Luna, 128000},
		{GPT5Dot5, 128000},
		{O1, 100000},
		{O3Mini, 65536},
		{GPT4o, 16384},
		{GPT4, 8192},
		{"unknown-model", 0},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, MaxOutputTokens(tt.model))
		})
	}
}

func TestIsReasoningModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"o1", true},
		{"o1-mini", true},
		{"o1-preview", true},
		{"o3", true},
		{"o3-mini", true},
		{"o4-mini", true},
		{"gpt-4", false},
		{"gpt-4-turbo", false},
		{"gpt-5", false},
		{"gpt-5-mini", false},
		{"gpt-3.5-turbo", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, IsReasoningModel(tt.model))
		})
	}
}

func TestSupportsReasoning(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		// O-series models support reasoning.
		{"o1", true},
		{"o1-mini", true},
		{"o3", true},
		{"o3-mini", true},
		{"o3-pro", true},
		{"o4-mini", true},
		// GPT-5.x models support reasoning.
		{"gpt-5", true},
		{"gpt-5-mini", true},
		{"gpt-5-nano", true},
		{"gpt-5.1", true},
		{"gpt-5.2", true},
		{"gpt-5.2-pro", true},
		{"gpt-5.3-codex", true},
		{"gpt-5.3-instant", true},
		{"gpt-5.4", true},
		{"gpt-5.4-pro", true},
		{"gpt-5.4-mini", true},
		{"gpt-5.4-nano", true},
		{"gpt-5.6", true},
		{"gpt-5.6-sol", true},
		{"gpt-5.6-terra", true},
		{"gpt-5.6-luna", true},
		{"gpt-6-astra", true},
		{"gpt-6-sol", true},
		{"gpt-6-luna", true},
		// Older models do not support reasoning.
		{"gpt-4", false},
		{"gpt-4-turbo", false},
		{"gpt-4o", false},
		{"gpt-4.1", false},
		{"gpt-4.1-mini", false},
		{"gpt-3.5-turbo", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, SupportsReasoning(tt.model))
		})
	}
}

func TestNormalizeEffort(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		effort     string
		wantEffort string
		wantWarn   bool
	}{
		// Empty effort always passes through.
		{"empty effort", "o4-mini", "", "", false},

		// Non-reasoning models: effort is dropped silently.
		{"gpt-4 with high", "gpt-4", "high", "", false},
		{"gpt-4o with low", "gpt-4o", "low", "", false},

		// O-series: low/medium/high are supported.
		{"o4-mini low", "o4-mini", "low", "low", false},
		{"o4-mini medium", "o4-mini", "medium", "medium", false},
		{"o4-mini high", "o4-mini", "high", "high", false},
		{"o3 low", "o3", "low", "low", false},
		{"o1-mini medium", "o1-mini", "medium", "medium", false},

		// O-series: unsupported levels are dropped with warning.
		{"o4-mini none", "o4-mini", "none", "", true},
		{"o4-mini minimal", "o4-mini", "minimal", "", true},
		{"o4-mini xhigh", "o4-mini", "xhigh", "", true},
		{"o4-mini max", "o4-mini", "max", "", true},
		{"o3 max", "o3", "max", "", true},
		{"o1 xhigh", "o1", "xhigh", "", true},

		// GPT-5.4: none/low/medium/high/xhigh are supported.
		{"gpt-5.4 none", "gpt-5.4", "none", "none", false},
		{"gpt-5.4 low", "gpt-5.4", "low", "low", false},
		{"gpt-5.4 medium", "gpt-5.4", "medium", "medium", false},
		{"gpt-5.4 high", "gpt-5.4", "high", "high", false},
		{"gpt-5.4 xhigh", "gpt-5.4", "xhigh", "xhigh", false},

		// GPT-5.4: unsupported levels are dropped with warning.
		{"gpt-5.4 max", "gpt-5.4", "max", "", true},
		{"gpt-5.4 minimal", "gpt-5.4", "minimal", "", true},
		{"gpt-5.4 ultra", "gpt-5.4", "ultra", "", true},

		// GPT-5.6: none/low/medium/high/xhigh/max/ultra are supported.
		{"gpt-5.6 none", "gpt-5.6", "none", "none", false},
		{"gpt-5.6 max", "gpt-5.6", "max", "max", false},
		{"gpt-5.6 ultra", "gpt-5.6", "ultra", "ultra", false},
		{"gpt-5.6-sol xhigh", "gpt-5.6-sol", "xhigh", "xhigh", false},
		{"gpt-5.6-sol max", "gpt-5.6-sol", "max", "max", false},
		{"gpt-5.6-sol ultra", "gpt-5.6-sol", "ultra", "ultra", false},
		{"gpt-5.6-terra high", "gpt-5.6-terra", "high", "high", false},
		{"gpt-5.6-terra ultra", "gpt-5.6-terra", "ultra", "ultra", false},
		{"gpt-5.6-luna ultra", "gpt-5.6-luna", "ultra", "ultra", false},
		{"gpt-5.6-luna minimal", "gpt-5.6-luna", "minimal", "", true},

		// GPT-6 Astra: low/medium/high/xhigh/max/ultra are supported.
		{"gpt-6-astra low", "gpt-6-astra", "low", "low", false},
		{"gpt-6-astra medium", "gpt-6-astra", "medium", "medium", false},
		{"gpt-6-astra high", "gpt-6-astra", "high", "high", false},
		{"gpt-6-astra xhigh", "gpt-6-astra", "xhigh", "xhigh", false},
		{"gpt-6-astra max", "gpt-6-astra", "max", "max", false},
		{"gpt-6-astra ultra", "gpt-6-astra", "ultra", "ultra", false},
		{"gpt-6-astra none", "gpt-6-astra", "none", "", true},
		{"gpt-6-astra minimal", "gpt-6-astra", "minimal", "", true},

		// GPT-6 Sol matches Astra; GPT-6 Luna stops at max.
		{"gpt-6-sol max", "gpt-6-sol", "max", "max", false},
		{"gpt-6-sol ultra", "gpt-6-sol", "ultra", "ultra", false},
		{"gpt-6-sol none", "gpt-6-sol", "none", "", true},
		{"gpt-6-luna medium", "gpt-6-luna", "medium", "medium", false},
		{"gpt-6-luna xhigh", "gpt-6-luna", "xhigh", "xhigh", false},
		{"gpt-6-luna max", "gpt-6-luna", "max", "max", false},
		{"gpt-6-luna ultra", "gpt-6-luna", "ultra", "", true},

		// GPT-5.4-mini: none/low/medium/high/xhigh (same as gpt-5.4).
		{"gpt-5.4-mini none", "gpt-5.4-mini", "none", "none", false},
		{"gpt-5.4-mini low", "gpt-5.4-mini", "low", "low", false},
		{"gpt-5.4-mini xhigh", "gpt-5.4-mini", "xhigh", "xhigh", false},
		{"gpt-5.4-mini max", "gpt-5.4-mini", "max", "", true},
		{"gpt-5.4-mini minimal", "gpt-5.4-mini", "minimal", "", true},

		// GPT-5.4-nano: none/low/medium/high/xhigh (same as gpt-5.4).
		{"gpt-5.4-nano none", "gpt-5.4-nano", "none", "none", false},
		{"gpt-5.4-nano xhigh", "gpt-5.4-nano", "xhigh", "xhigh", false},
		{"gpt-5.4-nano max", "gpt-5.4-nano", "max", "", true},

		// GPT-5.4-pro: medium/high/xhigh only.
		{"gpt-5.4-pro medium", "gpt-5.4-pro", "medium", "medium", false},
		{"gpt-5.4-pro xhigh", "gpt-5.4-pro", "xhigh", "xhigh", false},
		{"gpt-5.4-pro low", "gpt-5.4-pro", "low", "", true},
		{"gpt-5.4-pro none", "gpt-5.4-pro", "none", "", true},

		// GPT-5.2: none/low/medium/high/xhigh are supported.
		{"gpt-5.2 none", "gpt-5.2", "none", "none", false},
		{"gpt-5.2 xhigh", "gpt-5.2", "xhigh", "xhigh", false},
		{"gpt-5.2 max", "gpt-5.2", "max", "", true},

		// GPT-5.3-codex: low/medium/high/xhigh.
		{"gpt-5.3-codex low", "gpt-5.3-codex", "low", "low", false},
		{"gpt-5.3-codex xhigh", "gpt-5.3-codex", "xhigh", "xhigh", false},
		{"gpt-5.3-codex none", "gpt-5.3-codex", "none", "", true},

		// GPT-5.1: none/low/medium/high.
		{"gpt-5.1 none", "gpt-5.1", "none", "none", false},
		{"gpt-5.1 high", "gpt-5.1", "high", "high", false},
		{"gpt-5.1 xhigh", "gpt-5.1", "xhigh", "", true},

		// GPT-5 base: minimal/low/medium/high.
		{"gpt-5 minimal", "gpt-5", "minimal", "minimal", false},
		{"gpt-5 high", "gpt-5", "high", "high", false},
		{"gpt-5 xhigh", "gpt-5", "xhigh", "", true},
		{"gpt-5 none", "gpt-5", "none", "", true},
		{"gpt-5 max", "gpt-5", "max", "", true},

		// GPT-5-mini: minimal/low/medium/high.
		{"gpt-5-mini minimal", "gpt-5-mini", "minimal", "minimal", false},
		{"gpt-5-mini high", "gpt-5-mini", "high", "high", false},
		{"gpt-5-mini xhigh", "gpt-5-mini", "xhigh", "", true},

		// GPT-5-nano: minimal/low/medium/high.
		{"gpt-5-nano minimal", "gpt-5-nano", "minimal", "minimal", false},
		{"gpt-5-nano none", "gpt-5-nano", "none", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			normalized, warning := NormalizeEffort(tt.model, tt.effort)
			assert.Equal(t, tt.wantEffort, normalized)
			if tt.wantWarn {
				assert.NotEmpty(t, warning, "expected a warning")
				assert.Contains(t, warning, tt.model)
				assert.Contains(t, warning, tt.effort)
			} else {
				assert.Empty(t, warning, "expected no warning")
			}
		})
	}
}
