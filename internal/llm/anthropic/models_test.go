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

package anthropic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFlagshipModelInCatalog(t *testing.T) {
	_, ok := AvailableModels()[FlagshipModel()]
	assert.True(t, ok, "flagship %q must be in the catalog", FlagshipModel())
	assert.Equal(t, ClaudeOpus5Dot5, FlagshipModel())
}

func TestClaudeOpus5Dot5Catalog(t *testing.T) {
	assert.Equal(t, 1000000, AvailableModels()[ClaudeOpus5Dot5])
	assert.Equal(t, 128000, MaxOutputTokens(ClaudeOpus5Dot5))
	assert.True(t, SupportsAdaptiveThinking(ClaudeOpus5Dot5))
}

func TestClaudeOpus5Catalog(t *testing.T) {
	assert.Equal(t, 1000000, AvailableModels()[ClaudeOpus5])
	assert.Equal(t, 128000, MaxOutputTokens(ClaudeOpus5))
	assert.True(t, SupportsAdaptiveThinking(ClaudeOpus5))
}

func TestClaudeSonnet5Catalog(t *testing.T) {
	assert.Equal(t, 1000000, AvailableModels()[ClaudeSonnet5])
	assert.Equal(t, 128000, MaxOutputTokens(ClaudeSonnet5))
	assert.True(t, SupportsAdaptiveThinking(ClaudeSonnet5))
}

func TestClaudeFable5Dot1Catalog(t *testing.T) {
	assert.Equal(t, 1000000, AvailableModels()[ClaudeFable5Dot1])
	assert.Equal(t, 128000, MaxOutputTokens(ClaudeFable5Dot1))
	assert.True(t, SupportsAdaptiveThinking(ClaudeFable5Dot1))
}

func TestMaxOutputTokens(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{ClaudeOpus5, 128000},
		{ClaudeOpus5Dot5, 128000},
		{ClaudeSonnet5, 128000},
		{ClaudeFable5Dot1, 128000},
		{ClaudeFable5, 128000},
		{ClaudeSonnet4Dot5, 64000},
		{ClaudeOpus4, 32000},
		{ClaudeHaiku3, 4096},
		{"unknown-model", 0},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, MaxOutputTokens(tt.model))
		})
	}
}

func TestSupportsEffort(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{ClaudeOpus5, true},
		{ClaudeOpus5Dot5, true},
		{ClaudeSonnet5, true},
		{ClaudeOpus4Dot7, true},
		{ClaudeOpus4Dot6, true},
		{ClaudeSonnet4Dot6, true},
		{ClaudeHaiku4Dot5, true},
		{ClaudeOpus4Dot5, true},
		{ClaudeSonnet4Dot5, true},
		{ClaudeOpus4Dot1, true},
		{ClaudeSonnet4, true},
		{ClaudeOpus4, true},
		{ClaudeOpus3, false},
		{ClaudeSonnet3, false},
		{ClaudeHaiku3, false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, SupportsEffort(tt.model))
		})
	}
}

func TestSupportsAdaptiveThinking(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{ClaudeOpus5, true},
		{ClaudeOpus5Dot5, true},
		{ClaudeSonnet5, true},
		{ClaudeFable5Dot1, true},
		{ClaudeOpus4Dot6, true},
		{ClaudeSonnet4Dot6, true},
		{ClaudeHaiku4Dot5, false},
		{ClaudeOpus4Dot5, false},
		{ClaudeSonnet4Dot5, false},
		{ClaudeOpus4Dot1, false},
		{ClaudeOpus3, false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, SupportsAdaptiveThinking(tt.model))
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
		{"empty effort", ClaudeOpus4Dot6, "", "", false},

		// Claude 3 models: effort is dropped with warning.
		{"claude-3 opus with high", ClaudeOpus3, "high", "", true},
		{"claude-3 sonnet with low", ClaudeSonnet3, "low", "", true},
		{"claude-3 haiku with medium", ClaudeHaiku3, "medium", "", true},

		// Claude 4.0–4.5: low/medium/high are supported.
		{"opus-4 low", ClaudeOpus4, "low", "low", false},
		{"opus-4 medium", ClaudeOpus4, "medium", "medium", false},
		{"opus-4 high", ClaudeOpus4, "high", "high", false},
		{"sonnet-4.5 low", ClaudeSonnet4Dot5, "low", "low", false},
		{"opus-4.5 high", ClaudeOpus4Dot5, "high", "high", false},
		{"haiku-4.5 medium", ClaudeHaiku4Dot5, "medium", "medium", false},

		// Claude 4.0–4.5: unsupported levels are dropped with warning.
		{"opus-4 none", ClaudeOpus4, "none", "", true},
		{"opus-4 minimal", ClaudeOpus4, "minimal", "", true},
		{"opus-4 xhigh", ClaudeOpus4, "xhigh", "", true},
		{"opus-4 max", ClaudeOpus4, "max", "", true},
		{"sonnet-4.5 xhigh", ClaudeSonnet4Dot5, "xhigh", "", true},
		{"haiku-4.5 max", ClaudeHaiku4Dot5, "max", "", true},

		// Claude 4.6+: low/medium/high/xhigh/max are all supported.
		{"opus-4.6 low", ClaudeOpus4Dot6, "low", "low", false},
		{"opus-4.6 medium", ClaudeOpus4Dot6, "medium", "medium", false},
		{"opus-4.6 high", ClaudeOpus4Dot6, "high", "high", false},
		{"opus-4.6 xhigh", ClaudeOpus4Dot6, "xhigh", "xhigh", false},
		{"opus-4.6 max", ClaudeOpus4Dot6, "max", "max", false},
		{"opus-4.7 low", ClaudeOpus4Dot7, "low", "low", false},
		{"opus-4.7 medium", ClaudeOpus4Dot7, "medium", "medium", false},
		{"opus-4.7 high", ClaudeOpus4Dot7, "high", "high", false},
		{"opus-4.7 xhigh", ClaudeOpus4Dot7, "xhigh", "xhigh", false},
		{"opus-4.7 max", ClaudeOpus4Dot7, "max", "max", false},
		{"opus-4.8 low", ClaudeOpus4Dot8, "low", "low", false},
		{"opus-4.8 high", ClaudeOpus4Dot8, "high", "high", false},
		{"opus-4.8 xhigh", ClaudeOpus4Dot8, "xhigh", "xhigh", false},
		{"opus-4.8 max", ClaudeOpus4Dot8, "max", "max", false},
		{"sonnet-4.6 max", ClaudeSonnet4Dot6, "max", "max", false},
		{"sonnet-4.6 xhigh", ClaudeSonnet4Dot6, "xhigh", "xhigh", false},
		{"fable-5 low", ClaudeFable5, "low", "low", false},
		{"fable-5 high", ClaudeFable5, "high", "high", false},
		{"fable-5 xhigh", ClaudeFable5, "xhigh", "xhigh", false},
		{"fable-5 max", ClaudeFable5, "max", "max", false},
		{"fable-5.1 xhigh", ClaudeFable5Dot1, "xhigh", "xhigh", false},
		{"fable-5.1 max", ClaudeFable5Dot1, "max", "max", false},
		{"opus-5 low", ClaudeOpus5, "low", "low", false},
		{"opus-5 medium", ClaudeOpus5, "medium", "medium", false},
		{"opus-5 high", ClaudeOpus5, "high", "high", false},
		{"opus-5 xhigh", ClaudeOpus5, "xhigh", "xhigh", false},
		{"opus-5 max", ClaudeOpus5, "max", "max", false},
		{"opus-5.5 low", ClaudeOpus5Dot5, "low", "low", false},
		{"opus-5.5 medium", ClaudeOpus5Dot5, "medium", "medium", false},
		{"opus-5.5 high", ClaudeOpus5Dot5, "high", "high", false},
		{"opus-5.5 xhigh", ClaudeOpus5Dot5, "xhigh", "xhigh", false},
		{"opus-5.5 max", ClaudeOpus5Dot5, "max", "max", false},
		{"opus-5.5 none", ClaudeOpus5Dot5, "none", "", true},
		{"sonnet-5 low", ClaudeSonnet5, "low", "low", false},
		{"sonnet-5 medium", ClaudeSonnet5, "medium", "medium", false},
		{"sonnet-5 high", ClaudeSonnet5, "high", "high", false},
		{"sonnet-5 xhigh", ClaudeSonnet5, "xhigh", "xhigh", false},
		{"sonnet-5 max", ClaudeSonnet5, "max", "max", false},

		// Claude 4.6: unsupported levels are dropped with warning.
		{"opus-4.6 none", ClaudeOpus4Dot6, "none", "", true},
		{"opus-4.6 minimal", ClaudeOpus4Dot6, "minimal", "", true},
		{"sonnet-4.6 none", ClaudeSonnet4Dot6, "none", "", true},
		{"sonnet-4.7 none", ClaudeOpus4Dot7, "none", "", true},
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
