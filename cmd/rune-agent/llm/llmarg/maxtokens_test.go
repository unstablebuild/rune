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

package llmarg_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"

	"unstable.build/rune/cmd/rune-agent/llm/llmarg"
	"unstable.build/rune/internal/llm/anthropic"
	"unstable.build/rune/internal/llm/claude"
	"unstable.build/rune/internal/llm/codex"
	"unstable.build/rune/internal/llm/gemini"
	"unstable.build/rune/internal/llm/openai"
)

func TestMaxOutputTokensDispatch(t *testing.T) {
	tests := []struct {
		name  string
		entry llmapi.ModelEntry
		want  int
	}{
		{"anthropic", llmapi.ModelEntry{Provider: anthropic.LLMProvider, Name: anthropic.ClaudeFable5}, 128000},
		{"claude", llmapi.ModelEntry{Provider: claude.LLMProvider, Name: anthropic.ClaudeFable5}, 128000},
		{"openai", llmapi.ModelEntry{Provider: openai.LLMProvider, Name: openai.GPT5Dot5}, 128000},
		{"codex", llmapi.ModelEntry{Provider: codex.LLMProvider, Name: codex.GPT5Dot5}, 0},
		{"gemini", llmapi.ModelEntry{Provider: gemini.LLMProvider, Name: gemini.Gemini_4_Carbon}, 65536},
		{"gemini 3.6 flash", llmapi.ModelEntry{Provider: gemini.LLMProvider, Name: gemini.Gemini_3_6_Flash}, 65536},
		{"gemini 3.5 flash-lite", llmapi.ModelEntry{Provider: gemini.LLMProvider, Name: gemini.Gemini_3_5_FlashLite}, 65536},
		{"local", llmapi.ModelEntry{Provider: "llamacpp", Name: "anything"}, 0},
		{"unknown provider", llmapi.ModelEntry{Provider: "mystery", Name: "x"}, 0},
		{"unknown model", llmapi.ModelEntry{Provider: anthropic.LLMProvider, Name: "unknown"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, llmarg.MaxOutputTokens(tt.entry))
		})
	}
}

func TestValidateMaxOutputTokens(t *testing.T) {
	known := llmapi.ModelEntry{Provider: anthropic.LLMProvider, Name: anthropic.ClaudeFable5}

	require.NoError(t, llmarg.ValidateMaxOutputTokens(known, 128000),
		"a value at the limit must pass")
	require.NoError(t, llmarg.ValidateMaxOutputTokens(known, 1000),
		"a value under the limit must pass")

	err := llmarg.ValidateMaxOutputTokens(known, 200000)
	require.Error(t, err)
	assert.Equal(t,
		"claude-fable-5 supports at most 128000 max output tokens; 200000 is too large",
		err.Error())

	noCap := llmapi.ModelEntry{Provider: "llamacpp", Name: "local"}
	require.NoError(t, llmarg.ValidateMaxOutputTokens(noCap, 1<<30),
		"unknown ceiling must never reject")

	unknownModel := llmapi.ModelEntry{Provider: anthropic.LLMProvider, Name: "unknown"}
	require.NoError(t, llmarg.ValidateMaxOutputTokens(unknownModel, 1<<30),
		"unknown model ceiling (0) must never reject")

	codexModel := llmapi.ModelEntry{Provider: codex.LLMProvider, Name: codex.GPT5Dot6Sol}
	err = llmarg.ValidateMaxOutputTokens(codexModel, 4096)
	require.EqualError(t, err,
		"codex/gpt-5.6-sol does not support custom max output token limits")
}
