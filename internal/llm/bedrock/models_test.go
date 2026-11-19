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

package bedrock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelEntries_CoverCatalog(t *testing.T) {
	entries := ModelEntries()
	require.Len(t, entries, len(AvailableModels()))
	for _, e := range entries {
		assert.Equal(t, LLMProvider, e.Provider)
		assert.Positive(t, e.ContextWindow, "model %s", e.Name)
	}
}

func TestFlagshipModel_IsInCatalog(t *testing.T) {
	_, ok := AvailableModels()[FlagshipModel()]
	assert.True(t, ok)
	assert.Equal(t, ClaudeOpus55, FlagshipModel())
}

func TestNewModelsCatalog(t *testing.T) {
	tests := []struct {
		model     string
		ctxWindow int
		maxOutput int
	}{
		{ClaudeFable52, 1_000_000, 128000},
		{ClaudeFable51, 1_000_000, 128000},
		{ClaudeFable5, 1_000_000, 128000},
		{ClaudeOpus55, 1_000_000, 128000},
		{ClaudeSonnet55, 1_000_000, 128000},
		{ClaudeHaiku55, 1_000_000, 128000},
		{ClaudeOpus5, 1_000_000, 128000},
		{ClaudeSonnet5, 1_000_000, 128000},
		{ClaudeOpus48, 1_000_000, 128000},
		{ClaudeOpus47, 1_000_000, 128000},
		{ClaudeOpus46, 1_000_000, 128000},
		// Bedrock caps Sonnet 4.6 lower than the Claude API does.
		{ClaudeSonnet46, 1_000_000, 64000},
		{GPT62Astra, 1_050_000, 128000},
		{GPT62Sol, 1_050_000, 128000},
		{GPT6Astra, 1_050_000, 128000},
		{GPT61Sol, 1_000_000, 131072},
		{GPT61Terra, 1_050_000, 128000},
		{GPT6Sol, 1_050_000, 128000},
		{GPT6Luna, 1_050_000, 128000},
		{GPT56Sol, 1_050_000, 128000},
		{GPT56Terra, 1_050_000, 128000},
		{GPT56Luna, 1_050_000, 128000},
		{Nova2Lite, 1_000_000, 64000},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.ctxWindow, AvailableModels()[tt.model])
			assert.Equal(t, tt.maxOutput, MaxOutputTokens(tt.model))
		})
	}
}

func TestRetiredModelsNotInCatalog(t *testing.T) {
	retired := []string{
		"us.anthropic.claude-sonnet-4-5-20250929-v1:0",
		"us.anthropic.claude-opus-4-1-20250805-v1:0",
		"us.anthropic.claude-sonnet-4-20250514-v1:0",
	}
	avail := AvailableModels()
	for _, model := range retired {
		t.Run(model, func(t *testing.T) {
			_, ok := avail[model]
			assert.False(t, ok)
		})
	}
}

func TestNormalizeEffort(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		effort   string
		want     string
		wantWarn bool
	}{
		{"unset", ClaudeOpus45, "", "", false},
		{"claude high", ClaudeOpus45, "high", "high", false},
		{"claude unsupported level", ClaudeOpus45, "ultra", "", true},
		{"budget claude rejects max", ClaudeOpus45, "max", "", true},
		{"adaptive claude max", ClaudeOpus55, "max", "max", false},
		{"adaptive claude xhigh", ClaudeFable52, "xhigh", "xhigh", false},
		{"adaptive claude ultra", ClaudeOpus55, "ultra", "", true},
		{"global profile", "global.anthropic.claude-opus-5-5", "xhigh", "xhigh", false},
		{"claude 4.6 max", ClaudeOpus46, "max", "max", false},
		{"claude 4.6 rejects xhigh", ClaudeSonnet46, "xhigh", "", true},
		{"openai on bedrock rejects effort", GPT62Astra, "high", "", true},
		{"nova rejects effort", NovaPro, "high", "", true},
		{"claude 3 predates thinking", "us.anthropic.claude-3-haiku-20240307-v1:0", "low", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warn := NormalizeEffort(tt.model, tt.effort)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantWarn, warn != "", "warning: %q", warn)
		})
	}
}

func TestIsClaudeModel(t *testing.T) {
	assert.True(t, IsClaudeModel(ClaudeOpus45))
	assert.False(t, IsClaudeModel(NovaPro))
	assert.False(t, IsClaudeModel(DeepSeekR1))
}

func TestSupportsAdaptiveThinking(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{ClaudeFable52, true},
		{ClaudeOpus55, true},
		{ClaudeOpus5, true},
		{ClaudeOpus47, true},
		{ClaudeOpus46, true},
		{ClaudeSonnet46, true},
		{"global.anthropic.claude-mythos-5-2", true},
		{"anthropic.claude-opus-5", true},
		{ClaudeOpus45, false},
		{ClaudeHaiku45, false},
		{"eu.anthropic.claude-sonnet-4-20250514-v1:0", false},
		{"us.anthropic.claude-3-haiku-20240307-v1:0", false},
		{NovaPro, false},
		{GPT62Astra, false},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			assert.Equal(t, tt.want, SupportsAdaptiveThinking(tt.model))
		})
	}
}

func TestRegionalProfile(t *testing.T) {
	tests := []struct {
		model, region, want string
	}{
		{ClaudeOpus55, "eu-west-1", "eu.anthropic.claude-opus-5-5"},
		{"au.anthropic.claude-opus-5-5", "us-east-1", "us.anthropic.claude-opus-5-5"},
		// Global profiles already serve every commercial region.
		{"global.anthropic.claude-opus-5-5", "eu-west-1", "global.anthropic.claude-opus-5-5"},
	}
	for _, tt := range tests {
		t.Run(tt.model+" "+tt.region, func(t *testing.T) {
			assert.Equal(t, tt.want, RegionalProfile(tt.model, tt.region))
		})
	}
}

func TestVerificationModelForRegion(t *testing.T) {
	tests := []struct {
		region string
		want   string
	}{
		{"", FlagshipModel()},
		{"us-east-1", FlagshipModel()},
		{"eu-west-1", "eu.anthropic.claude-opus-5-5"},
		{"ap-southeast-2", "apac.anthropic.claude-opus-5-5"},
		{"us-gov-west-1", "us-gov.anthropic.claude-opus-5-5"},
	}
	for _, tt := range tests {
		t.Run("region "+tt.region, func(t *testing.T) {
			assert.Equal(t, tt.want, VerificationModelForRegion(tt.region))
		})
	}
}
