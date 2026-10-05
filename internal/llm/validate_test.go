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

package llm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateConfig_DefaultAccepted(t *testing.T) {
	require.NoError(t, ValidateConfig(DefaultConfig()))
}

func TestValidateConfig_RejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Config)
		msg  string
	}{
		{
			name: "reasoning summary",
			mut: func(c *Config) {
				c.ReasoningSummary = "loud"
			},
			msg: "reasoning_summary",
		},
		{
			name: "openai effort",
			mut: func(c *Config) {
				c.OpenAI.ReasoningEffort = "warp"
			},
			msg: "openai.reasoning_effort",
		},
		{
			name: "anthropic effort",
			mut: func(c *Config) {
				c.Anthropic.ReasoningEffort = "turbo"
			},
			msg: "anthropic.reasoning_effort",
		},
		{
			name: "bedrock effort",
			mut: func(c *Config) {
				c.Bedrock.ReasoningEffort = "turbo"
			},
			msg: "bedrock.reasoning_effort",
		},
		{
			name: "relative cache dir",
			mut: func(c *Config) {
				c.Local.ModelsCacheDir = "relative/path"
			},
			msg: "absolute path",
		},
		{
			name: "custom models without url",
			mut: func(c *Config) {
				c.Custom.AvailableModels = map[string]int{"foo": 8192}
			},
			msg: "models.custom.url",
		},
		{
			name: "negative top_k",
			mut: func(c *Config) {
				c.Local.Service.Sampling.TopK = -1
			},
			msg: "top_k",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.mut(&cfg)
			err := ValidateConfig(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.msg)
		})
	}
}
