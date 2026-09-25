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
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

func TestAnthropicParamsMaxTokens(t *testing.T) {
	tests := []struct {
		name    string
		model   string
		request llmapi.Request
		config  Config
		want    int64
	}{
		{"request overrides all", ClaudeOpus4Dot8, llmapi.Request{MaxOutputTokens: 222}, Config{MaxTokens: 111}, 222},
		{"config overrides model ceiling", ClaudeOpus4Dot8, llmapi.Request{}, Config{MaxTokens: 111}, 111},
		{"defaults to model ceiling", ClaudeOpus4Dot8, llmapi.Request{}, Config{}, 128000},
		{"defaults to model ceiling with thinking", ClaudeSonnet4Dot6, llmapi.Request{}, Config{EnableThinking: true}, 64000},
		{"legacy model ceiling", ClaudeHaiku3, llmapi.Request{}, Config{}, 4096},
		{"unknown model with thinking", "claude-unknown", llmapi.Request{}, Config{EnableThinking: true}, 32768},
		{"unknown model", "claude-unknown", llmapi.Request{}, Config{}, 8192},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := anthropicParamsFromRequest(tt.model, tt.request, tt.config)
			assert.Equal(t, tt.want, params.MaxTokens)
		})
	}
}
