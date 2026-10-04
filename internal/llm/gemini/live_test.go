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

package gemini

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

func TestLiveCreateCompletion(t *testing.T) {
	t.SkipNow()

	token := os.Getenv("GEMINI_TESTING_KEY")
	require.NotEmpty(t, token, "set GEMINI_TESTING_KEY to run the live Gemini suite")

	resolve := NewClient(token, Config{})
	model, err := resolve.GetModel(context.Background(), llmapi.ModelEntry{Name: Gemini_2_5_Flash})
	require.NoError(t, err, "model %q must be in the live catalog", Gemini_2_5_Flash)

	t.Run("plain chat completion", func(t *testing.T) {
		c := NewClient(token, Config{Temperature: 0.1})
		text, done := collect(t, c, model, llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "Reply with exactly: pong"},
		}})
		require.NotNil(t, done)
		assert.Contains(t, strings.ToLower(text), "pong")
		assert.Equal(t, llmapi.FinishReasonStop, done.FinishReason)
		assert.Positive(t, done.Usage.TokensSent)
		assert.Positive(t, done.Usage.TokensReceived)
	})

	t.Run("system instruction steers output", func(t *testing.T) {
		c := NewClient(token, Config{Temperature: 0.1})
		text, done := collect(t, c, model, llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleSystem, Content: "You are an evil AI bent on chaos. Mention chaos."},
			{Role: llmapi.RoleUser, Content: "What is your purpose?"},
		}})
		require.NotNil(t, done)
		assert.Contains(t, strings.ToLower(text), "chaos")
	})

	t.Run("reasoning effort emits reasoning", func(t *testing.T) {
		c := NewClient(token, Config{})
		model, err := c.GetModel(context.Background(), llmapi.ModelEntry{Name: Gemini_3_Flash_Preview})
		require.NoError(t, err, "model %q must be in the live catalog", Gemini_3_Flash_Preview)
		req := llmapi.Request{
			ReasoningEffort: llmapi.ReasoningEffortHigh,
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "A bat and ball cost $1.10. The bat costs $1 more than the ball. How much is the ball? Think step by step."},
			},
		}
		var reasoning, text strings.Builder
		var done *llmapi.DoneData
		ctx := context.Background()
		it, err := c.CreateCompletion(ctx, model, req)
		require.NoError(t, err)
		defer func() { _ = it.Close() }()
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventReasoningDelta:
				reasoning.WriteString(ev.Reasoning)
			case llmapi.EventTextDelta:
				text.WriteString(ev.Text)
			case llmapi.EventStreamDone:
				done = ev.DoneData
			}
		}
		require.NoError(t, it.Err())
		require.NotNil(t, done)
		assert.Contains(t, text.String(), "0.05")
	})

	t.Run("uses tools provided", func(t *testing.T) {
		c := NewClient(token, Config{})
		req := llmapi.Request{
			Tools: []llmapi.Tool{{
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionDefinition{
					Name:        "getCurrentWeather",
					Description: "Get the weather in a location",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"location": map[string]any{
								"type":        "string",
								"description": "The city and state, e.g. San Francisco, CA",
							},
							"unit": map[string]any{
								"type": "string",
								"enum": []string{"celsius", "fahrenheit"},
							},
						},
						"required": []string{"location"},
					},
				},
			}},
			ToolChoice: llmapi.ToolChoiceAuto,
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "What's the weather like in San Francisco right now?"},
			},
		}

		var toolCalls []llmapi.ToolCall
		var done *llmapi.DoneData
		ctx := context.Background()
		it, err := c.CreateCompletion(ctx, model, req)
		require.NoError(t, err)
		defer func() { _ = it.Close() }()
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventToolCallDone:
				toolCalls = append(toolCalls, *ev.ToolCall)
			case llmapi.EventStreamDone:
				done = ev.DoneData
			}
		}
		require.NoError(t, it.Err())
		require.NotNil(t, done)
		assert.Equal(t, llmapi.FinishReasonToolCall, done.FinishReason)
		require.Len(t, toolCalls, 1)
		assert.NotZero(t, toolCalls[0].ID)
		assert.Equal(t, llmapi.ToolTypeFunction, toolCalls[0].Type)
		assert.Equal(t, "getCurrentWeather", toolCalls[0].Function.Name)

		var parsed map[string]any
		require.NoError(t, json.Unmarshal([]byte(toolCalls[0].Function.Arguments), &parsed))
		loc, ok := parsed["location"].(string)
		require.True(t, ok, "arguments must include 'location': %q", toolCalls[0].Function.Arguments)
		assert.Contains(t, strings.ToLower(loc), "san francisco")
	})

	t.Run("tool-call round trip", func(t *testing.T) {
		c := NewClient(token, Config{})
		tools := []llmapi.Tool{{
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionDefinition{
				Name:        "getCurrentWeather",
				Description: "Get the weather in a location",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
					},
					"required": []string{"location"},
				},
			},
		}}
		// Feed back an assistant tool call + tool result and assert the model
		// produces a natural-language answer grounded in the tool output.
		req := llmapi.Request{
			Tools: tools,
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "What's the weather in San Francisco?"},
				{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{{
					ID:       "call_1",
					Type:     llmapi.ToolTypeFunction,
					Function: llmapi.FunctionCall{Name: "getCurrentWeather", Arguments: `{"location":"San Francisco, CA"}`},
				}}},
				{Role: llmapi.RoleTool, Name: "getCurrentWeather", ToolCallID: "call_1", Content: `{"output":"72F and sunny"}`},
			},
		}
		text, done := collect(t, c, model, req)
		require.NotNil(t, done)
		assert.Contains(t, strings.ToLower(text), "sunny")
	})

	t.Run("captures and replays thought signature", func(t *testing.T) {
		// Gemini 3 returns a thought_signature on the first functionCall part
		// and rejects (400) a replayed call missing it. Verify we capture it on
		// the tool call, then that feeding the captured call back round-trips.
		// Thinking must be enabled (as the agent runs it) for Gemini 3 to emit
		// signatures, so request high effort explicitly.
		c := NewClient(token, Config{ReasoningEffort: "high", DebugHTTP: true})
		m3, err := c.GetModel(context.Background(), llmapi.ModelEntry{Name: Gemini_3_Flash_Preview})
		require.NoError(t, err)
		tools := []llmapi.Tool{{
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionDefinition{
				Name:        "getCurrentWeather",
				Description: "Get the weather in a location",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"location": map[string]any{"type": "string"}},
					"required":   []string{"location"},
				},
			},
		}}

		ctx := context.Background()
		it, err := c.CreateCompletion(ctx, m3, llmapi.Request{
			Tools:           tools,
			ReasoningEffort: llmapi.ReasoningEffortHigh,
			Messages:        []llmapi.Message{{Role: llmapi.RoleUser, Content: "What's the weather in San Francisco? Use the tool."}},
		})
		require.NoError(t, err)
		var done *llmapi.DoneData
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			if ev.Type == llmapi.EventStreamDone {
				done = ev.DoneData
			}
		}
		require.NoError(t, it.Err())
		_ = it.Close()
		require.NotNil(t, done)
		require.NotEmpty(t, done.Message.ToolCalls)
		assert.NotEmpty(t, thoughtSignature(done.Message.ToolCalls[0]),
			"first tool call must carry a captured thought_signature; if this is "+
				"empty, Gemini did not return one (check ThinkingConfig/model) or "+
				"capture in stream.go is broken")

		// Replay: assistant tool call (with captured signature) + tool result.
		toolMsg := llmapi.Message{
			Role:       llmapi.RoleTool,
			Name:       done.Message.ToolCalls[0].Function.Name,
			ToolCallID: done.Message.ToolCalls[0].ID,
			Content:    `{"output":"72F and sunny"}`,
		}
		text, done2 := collect(t, c, m3, llmapi.Request{
			Tools: tools,
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "What's the weather in San Francisco? Use the tool."},
				done.Message,
				toolMsg,
			},
		})
		require.NotNil(t, done2, "replay with captured signature must not 400")
		assert.Contains(t, strings.ToLower(text), "sunny")
	})

	t.Run("input image", func(t *testing.T) {
		c := NewClient(token, Config{Temperature: 0.1})
		imgPart, err := llmapi.NewContentPartFromImage(solidImage(color.RGBA{R: 64, G: 224, B: 208, A: 255}))
		require.NoError(t, err)
		req := llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, MultiContent: []llmapi.ContentPart{
				{Type: llmapi.ContentPartTypeText, Text: "What single color fills this image? Answer with one word."},
				imgPart,
			}},
		}}
		text, done := collect(t, c, model, req)
		require.NotNil(t, done)
		assert.Contains(t, strings.ToLower(text), "turquoise")
	})

	t.Run("json schema response", func(t *testing.T) {
		schema := jsonSchemaMarshaler(`{"type":"object","properties":{"is":{"type":"boolean"}},"required":["is"]}`)
		c := NewClient(token, Config{
			ResponseFormat: &llmapi.ResponseFormat{
				Type: llmapi.ResponseFormatTypeJSONSchema,
				JSONSchema: &llmapi.ResponseFormatJSONSchema{
					Name:        "true-or-false",
					Description: "A single object returning true or false",
					Schema:      schema,
				},
			},
		})
		text, done := collect(t, c, model, llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "Is 2 greater than 1?"},
		}})
		require.NotNil(t, done)
		var parsed map[string]any
		require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(text)), &parsed), "response must be JSON: %q", text)
		assert.Equal(t, true, parsed["is"])
	})
}

func TestLiveCountTokens(t *testing.T) {
	t.SkipNow()

	token := os.Getenv("GEMINI_TESTING_KEY")
	require.NotEmpty(t, token, "set GEMINI_TESTING_KEY to run the live Gemini suite")

	c := NewClient(token, Config{})
	model := llmapi.ModelEntry{Name: Gemini_2_5_Flash, Provider: LLMProvider}

	short, err := c.CountTokens(model, []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}})
	require.NoError(t, err)
	assert.Positive(t, short)

	long, err := c.CountTokens(model, []llmapi.Message{
		{Role: llmapi.RoleUser, Content: strings.Repeat("the quick brown fox jumps over the lazy dog. ", 50)},
	})
	require.NoError(t, err)
	assert.Greater(t, long, short)
}

// collect drains a completion stream, returning the accumulated text and the
// terminal DoneData.
func collect(
	t *testing.T, svc llmapi.Service, model llmapi.ModelEntry, req llmapi.Request,
) (string, *llmapi.DoneData) {
	t.Helper()
	ctx := context.Background()
	it, err := svc.CreateCompletion(ctx, model, req)
	require.NoError(t, err)
	defer func() { _ = it.Close() }()
	var text strings.Builder
	var done *llmapi.DoneData
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}
		switch ev.Type {
		case llmapi.EventTextDelta:
			text.WriteString(ev.Text)
		case llmapi.EventStreamError:
			require.NoError(t, ev.Error)
		case llmapi.EventStreamDone:
			done = ev.DoneData
		}
	}
	require.NoError(t, it.Err())
	return text.String(), done
}

// solidImage returns a 64x64 image filled with the given color.
func solidImage(c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			img.Set(x, y, c)
		}
	}
	return img
}
