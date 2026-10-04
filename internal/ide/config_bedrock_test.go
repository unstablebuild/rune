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

package ide

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLLMConfig_BedrockBlock(t *testing.T) {
	c := ideConfig{cfg: map[string]any{
		"models": map[string]any{
			"bedrock": map[string]any{
				"profile":          "work",
				"base_url":         "https://bedrock-gw.internal",
				"reasoning_effort": "medium",
				"cache_control":    "default",
			},
		},
	}, errors: map[string]error{}}

	got := c.llmConfig()
	assert.Equal(t, "work", got.Bedrock.Profile)
	assert.Equal(t, "https://bedrock-gw.internal", got.Bedrock.BaseURL)
	assert.Equal(t, "medium", got.Bedrock.ReasoningEffort)
	assert.Equal(t, "default", got.Bedrock.CacheControl)

	client := got.BedrockClientConfig()
	assert.Equal(t, "work", client.Profile)
	assert.Equal(t, "https://bedrock-gw.internal", client.BaseURL)
	assert.Equal(t, "medium", client.ReasoningEffort)
	assert.Equal(t, "default", client.CacheControl)
}
