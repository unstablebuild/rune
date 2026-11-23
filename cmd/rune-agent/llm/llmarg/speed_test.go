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

package llmarg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/internal/llm/anthropic"
	"unstable.build/rune/internal/llm/claude"
	"unstable.build/rune/internal/llm/codex"
	"unstable.build/rune/internal/llm/gemini"
	"unstable.build/rune/internal/llm/openai"
)

func TestSupportsSpeed(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		speed    llmapi.Speed
		want     bool
	}{
		{
			name:     "anthropic fast model",
			provider: anthropic.LLMProvider, model: anthropic.ClaudeOpus5Dot5,
			speed: llmapi.SpeedFast, want: true,
		},
		{
			name:     "anthropic model without fast mode",
			provider: anthropic.LLMProvider, model: anthropic.ClaudeSonnet5Dot5,
			speed: llmapi.SpeedFast,
		},
		{
			name:     "claude shares the anthropic table",
			provider: claude.LLMProvider, model: anthropic.ClaudeOpus5,
			speed: llmapi.SpeedFast, want: true,
		},
		{
			name:     "claude has no ultrafast",
			provider: claude.LLMProvider, model: anthropic.ClaudeOpus5Dot5,
			speed: llmapi.SpeedUltrafast,
		},
		{
			name:     "openai ultrafast model",
			provider: openai.LLMProvider, model: openai.GPT6Dot1Sol,
			speed: llmapi.SpeedUltrafast, want: true,
		},
		{
			name:     "openai model without fast mode",
			provider: openai.LLMProvider, model: openai.GPT5Dot4Nano,
			speed: llmapi.SpeedFast,
		},
		{
			name:     "codex fast model",
			provider: codex.LLMProvider, model: codex.GPT5Dot5,
			speed: llmapi.SpeedFast, want: true,
		},
		{
			name:     "codex model without ultrafast",
			provider: codex.LLMProvider, model: codex.GPT6Dot1Terra,
			speed: llmapi.SpeedUltrafast,
		},
		{
			name:     "provider without speeds rejects fast",
			provider: gemini.LLMProvider, model: "gemini-3-pro-preview",
			speed: llmapi.SpeedFast,
		},
		{
			name:     "unknown provider accepts standard",
			provider: "whatever", model: "m",
			speed: llmapi.SpeedStandard, want: true,
		},
		{
			name:     "unknown provider accepts empty",
			provider: "whatever", model: "m",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SupportsSpeed(llmapi.ModelEntry{
				Provider: tt.provider, Name: tt.model,
			}, tt.speed)
			assert.Equal(t, tt.want, got)
		})
	}
}
