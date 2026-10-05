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
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// pickLiveModel chooses the model the live suite exercises from the models
// the account can actually invoke, preferring the flagship profile serving
// the region. Inference profiles are partitioned by geography, and model
// access is granted per account, so no single identifier is valid
// everywhere. The suite asserts on tool calls and signed reasoning replay,
// so only models supporting extended thinking qualify.
func pickLiveModel(entries []llmapi.ModelEntry, region string) (llmapi.ModelEntry, bool) {
	preferred := RegionalProfile(ClaudeSonnet45, region)
	for _, e := range entries {
		if e.Name == preferred {
			return e, true
		}
	}
	for _, e := range entries {
		if SupportsEffort(e.Name) {
			return e, true
		}
	}
	return llmapi.ModelEntry{}, false
}

func TestPickLiveModel(t *testing.T) {
	catalog := []llmapi.ModelEntry{
		{Name: NovaPro},
		{Name: "eu.anthropic.claude-haiku-4-5-20251001-v1:0"},
		{Name: "eu.anthropic.claude-sonnet-4-5-20250929-v1:0"},
	}

	got, ok := pickLiveModel(catalog, "eu-west-1")
	require.True(t, ok)
	assert.Equal(t, "eu.anthropic.claude-sonnet-4-5-20250929-v1:0", got.Name,
		"the region's own flagship profile wins")

	// The us. profile is absent from an EU catalog, so the suite must fall
	// back to another thinking-capable model instead of failing.
	got, ok = pickLiveModel(catalog, "us-east-1")
	require.True(t, ok)
	assert.Equal(t, "eu.anthropic.claude-haiku-4-5-20251001-v1:0", got.Name)

	_, ok = pickLiveModel([]llmapi.ModelEntry{{Name: NovaPro}}, "us-east-1")
	assert.False(t, ok, "no thinking-capable model means the suite cannot run")
}

func TestLiveCreateCompletion(t *testing.T) {
	region := os.Getenv("BEDROCK_TESTING_REGION")
	if region == "" {
		t.Skip("set BEDROCK_TESTING_REGION to run the live Bedrock suite")
	}
	key := os.Getenv("BEDROCK_TESTING_KEY")

	c := NewClient(key, Config{Region: region})
	entries, err := iterator.ToSlice(t.Context(), c.Models())
	require.NoError(t, err)
	model, ok := pickLiveModel(entries, region)
	if !ok {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name)
		}
		t.Skipf("no thinking-capable model available in %s for this account; catalog: %v",
			region, names)
	}
	t.Logf("exercising %s in %s", model.Name, region)

	t.Run("plain chat completion", func(t *testing.T) {
		text, done := liveCollect(t, c, model, llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "Reply with exactly: pong"},
		}})
		require.NotNil(t, done)
		assert.Contains(t, strings.ToLower(text), "pong")
		assert.Equal(t, llmapi.FinishReasonStop, done.FinishReason)
		assert.Positive(t, done.Usage.TokensSent)
		assert.Positive(t, done.Usage.TokensReceived)
	})

	t.Run("tool call round trip", func(t *testing.T) {
		_, done := liveCollect(t, c, model, llmapi.Request{
			ToolChoice: llmapi.ToolChoiceRequired,
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "What is the weather in Lisbon?"},
			},
			Tools: []llmapi.Tool{{
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionDefinition{
					Name:        "get_weather",
					Description: "Look up the current weather for a city.",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"city": map[string]any{"type": "string"},
						},
						"required": []any{"city"},
					},
				},
			}},
		})
		require.NotNil(t, done)
		require.Len(t, done.Message.ToolCalls, 1)
		assert.Equal(t, "get_weather", done.Message.ToolCalls[0].Function.Name)
		assert.Contains(t, strings.ToLower(done.Message.ToolCalls[0].Function.Arguments), "lisbon")
		assert.Equal(t, llmapi.FinishReasonToolCall, done.FinishReason)
	})

	t.Run("reasoning replays with its signature", func(t *testing.T) {
		_, done := liveCollect(t, c, model, llmapi.Request{
			ReasoningEffort: llmapi.ReasoningEffort("medium"),
			Messages: []llmapi.Message{{
				Role: llmapi.RoleUser,
				Content: "A bat and ball cost $1.10. The bat costs $1 more than " +
					"the ball. How much is the ball?",
			}},
		})
		require.NotNil(t, done)
		require.NotEmpty(t, done.Message.ReasoningBlocks)
		assert.NotEmpty(t, done.Message.ReasoningBlocks[0].Signature)

		// Replaying the signed reasoning must be accepted by the API.
		_, second := liveCollect(t, c, model, llmapi.Request{
			ReasoningEffort: llmapi.ReasoningEffort("medium"),
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "A bat and ball cost $1.10..."},
				done.Message,
				{Role: llmapi.RoleUser, Content: "Are you sure?"},
			},
		})
		require.NotNil(t, second)
	})
}

func liveCollect(
	t *testing.T, c llmapi.Service, model llmapi.ModelEntry, req llmapi.Request,
) (string, *llmapi.DoneData) {
	t.Helper()
	it, err := c.CreateCompletion(t.Context(), model, req)
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	var text strings.Builder
	var done *llmapi.DoneData
	for {
		ev, ok := it.Next(t.Context())
		if !ok {
			break
		}
		switch ev.Type {
		case llmapi.EventTextDelta:
			text.WriteString(ev.Text)
		case llmapi.EventStreamDone:
			done = ev.DoneData
		case llmapi.EventStreamError:
			require.NoError(t, ev.Error)
		}
	}
	return text.String(), done
}
