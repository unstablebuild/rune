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
	"unstable.build/rune/internal/llm/codex"
	"unstable.build/rune/internal/llm/gemini"
	"unstable.build/rune/internal/llm/openai"
)

func TestDefaultEffort(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		want     llmapi.ReasoningEffort
	}{
		{
			name:     "anthropic adaptive thinking",
			provider: anthropic.LLMProvider, model: anthropic.ClaudeOpus5,
			want: llmapi.ReasoningEffortHigh,
		},
		{
			name:     "anthropic without a published default",
			provider: anthropic.LLMProvider, model: anthropic.ClaudeOpus4,
		},
		{
			name:     "gemini 3 always sends a thinking level",
			provider: gemini.LLMProvider, model: "gemini-3-pro-preview",
			want: llmapi.ReasoningEffortMedium,
		},
		{
			name:     "openai omits the parameter",
			provider: openai.LLMProvider, model: "gpt-5",
		},
		{
			name:     "codex omits the parameter",
			provider: codex.LLMProvider, model: "gpt-5-codex",
		},
		{name: "unknown provider", provider: "whatever", model: "m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DefaultEffort(llmapi.ModelEntry{
				Provider: tt.provider, Name: tt.model,
			})
			assert.Equal(t, tt.want, got)
		})
	}
}
