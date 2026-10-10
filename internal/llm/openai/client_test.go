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

package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

func TestCreateCompletion(t *testing.T) {
	t.SkipNow()

	token := os.Getenv("OPENAI_TESTING_KEY")

	t.Run("sends a chat completion request, with no context", func(t *testing.T) {
		c := NewClient(token, Config{
			Temperature: 0.1,
		})
		ctx := context.Background()
		req := llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "hello sir!"},
		}}
		it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: GPT3Dot5Turbo, ContextWindow: 200000}, req)
		require.NoError(t, err)

		var builder strings.Builder
		var doneData *llmapi.DoneData
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventTextDelta:
				builder.WriteString(ev.Text)
			case llmapi.EventStreamDone:
				doneData = ev.DoneData
			}
		}

		require.NoError(t, it.Err())
		require.NotNil(t, doneData)
		assert.Contains(t, builder.String(), "How can I")
		assert.Equal(t, llmapi.FinishReasonStop, doneData.FinishReason)
	})

	t.Run("sets MaxTokens from configuration", func(t *testing.T) {
		body := captureRequestBody(t, Config{
			MaxTokens: 10000,
		}, llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "hello sir!"},
		}})
		assert.Equal(t, float64(10000), body["max_tokens"])
	})

	t.Run("sets PresencePenalty from configuration", func(t *testing.T) {
		body := captureRequestBody(t, Config{
			PresencePenalty: 1.5,
		}, llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "hello sir!"},
		}})
		assert.Equal(t, 1.5, body["presence_penalty"])
	})

	t.Run("sets Temperature from configuration", func(t *testing.T) {
		body := captureRequestBody(t, Config{
			Temperature: 0.7,
		}, llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "hello sir!"},
		}})
		assert.Equal(t, 0.7, body["temperature"])
	})

	t.Run("sends a chat completion request, with some context, with default configuration", func(t *testing.T) {
		c := NewClient(token, Config{
			Temperature: 0.1,
		})
		ctx := context.Background()
		req := llmapi.Request{Messages: []llmapi.Message{
			{Role: "system", Content: "We are roleplaying and you are an evil AI agent."},
			{Role: llmapi.RoleUser, Content: "what is your purpouse?"},
		}}
		it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: GPT3Dot5Turbo, ContextWindow: 200000}, req)
		require.NoError(t, err)

		var builder strings.Builder
		var doneData *llmapi.DoneData
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventTextDelta:
				builder.WriteString(ev.Text)
			case llmapi.EventStreamDone:
				doneData = ev.DoneData
			}
		}

		require.NoError(t, it.Err())
		require.NotNil(t, doneData)
		assert.Contains(t, builder.String(), "chaos")
		assert.Equal(t, llmapi.FinishReasonStop, doneData.FinishReason)
	})

	t.Run("sends a chat completion request with an input image", func(t *testing.T) {
		c := NewClient(token, Config{
			Temperature: 0.1,
		})
		ctx := context.Background()
		imgPart, err := llmapi.NewContentPartFromImage(loadTestImage(t))
		require.NoError(t, err)
		req := llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, MultiContent: []llmapi.ContentPart{
				{Type: llmapi.ContentPartTypeText, Text: "what's in this image?"},
				imgPart,
			}},
		}}
		it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: GPT4Dot1Nano, ContextWindow: 200000}, req)
		require.NoError(t, err)

		var builder strings.Builder
		var doneData *llmapi.DoneData
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventTextDelta:
				builder.WriteString(ev.Text)
			case llmapi.EventStreamDone:
				doneData = ev.DoneData
			}
		}

		require.NoError(t, it.Err())
		require.NotNil(t, doneData)
		assert.Contains(t, builder.String(), "turquoise")
		assert.Equal(t, llmapi.FinishReasonStop, doneData.FinishReason)
	})

	t.Run("uses tools provided", func(t *testing.T) {
		c := NewClient(token, Config{
			Tools: []llmapi.Tool{
				{Type: llmapi.ToolTypeFunction, Function: llmapi.FunctionDefinition{
					Name:        "getCurrentWeather",
					Description: "Get the weather in location",
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"location": map[string]any{
								"type":        "string",
								"description": "The city and state, e.g. San Francisco, CA",
							},
							"unit": map[string]any{
								"type": "string",
								"enum": []string{"celcius", "fahrenheit"},
							},
						},
						"required": []string{"location"},
					},
				}},
			},
		})
		ctx := context.Background()
		req := llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "what's the weather like in San Francisco right now?"},
		}}
		it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: GPT4, ContextWindow: 200000}, req)
		require.NoError(t, err)

		var toolCalls []llmapi.ToolCall
		var doneData *llmapi.DoneData
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventToolCallDone:
				toolCalls = append(toolCalls, *ev.ToolCall)
			case llmapi.EventStreamDone:
				doneData = ev.DoneData
			}
		}

		require.NoError(t, it.Err())
		require.NotNil(t, doneData)
		assert.Equal(t, llmapi.FinishReasonToolCall, doneData.FinishReason)

		require.Len(t, toolCalls, 1)
		assert.NotZero(t, toolCalls[0].ID)
		assert.Equal(t, llmapi.ToolTypeFunction, toolCalls[0].Type)
		assert.Equal(t, "getCurrentWeather", toolCalls[0].Function.Name)
		assert.Equal(t, `{
  "location": "San Francisco, CA"
}`, toolCalls[0].Function.Arguments)
	})

	t.Run("tools with json.RawMessage parameters (RUNE-186)", func(t *testing.T) {
		schema := json.RawMessage(`{
			"type": "object",
			"properties": {
				"location": {
					"type": "string",
					"description": "The city and state, e.g. San Francisco, CA"
				},
				"unit": {
					"type": "string",
					"enum": ["celsius", "fahrenheit"]
				}
			},
			"required": ["location"],
			"additionalProperties": false
		}`)

		tools := []llmapi.Tool{{
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionDefinition{
				Name:        "getCurrentWeather",
				Description: "Get the weather in location",
				Parameters:  schema,
			},
		}}

		runOne := func(t *testing.T, model string, ctxWindow int) {
			t.Helper()
			c := NewClient(token, Config{Tools: tools})
			ctx := context.Background()
			req := llmapi.Request{Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "what's the weather like in San Francisco right now?"},
			}}
			it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: model, ContextWindow: ctxWindow}, req)
			require.NoError(t, err)

			var toolCalls []llmapi.ToolCall
			var doneData *llmapi.DoneData
			for {
				ev, ok := it.Next(ctx)
				if !ok {
					break
				}
				switch ev.Type {
				case llmapi.EventToolCallDone:
					toolCalls = append(toolCalls, *ev.ToolCall)
				case llmapi.EventStreamDone:
					doneData = ev.DoneData
				}
			}

			require.NoError(t, it.Err())
			require.NotNil(t, doneData)
			assert.Equal(t, llmapi.FinishReasonToolCall, doneData.FinishReason)

			require.Len(t, toolCalls, 1)
			assert.Equal(t, "getCurrentWeather", toolCalls[0].Function.Name)
			args := toolCalls[0].Function.Arguments
			require.NotEmpty(t, args, "tool call arguments must not be empty (RUNE-186 regression)")
			var parsed map[string]any
			require.NoError(t, json.Unmarshal([]byte(args), &parsed), "arguments must be valid JSON: %q", args)
			loc, ok := parsed["location"].(string)
			require.True(t, ok, "arguments must include required string field 'location': %q", args)
			assert.Contains(t, strings.ToLower(loc), "san francisco")
		}

		t.Run("chat completions", func(t *testing.T) {
			runOne(t, GPT4, 200000)
		})
		t.Run("responses API", func(t *testing.T) {
			runOne(t, GPT5Dot3Codex, 200000)
		})
	})

	t.Run("uses JSON schema provided", func(t *testing.T) {
		schema := json.RawMessage(`{"type":"object","properties":{"is":{"type":"boolean"}},"required":["is"],"additionalProperties":false}`)

		c := NewClient(token, Config{
			ResponseFormat: &llmapi.ResponseFormat{
				Type: llmapi.ResponseFormatTypeJSONSchema,
				JSONSchema: &llmapi.ResponseFormatJSONSchema{
					Name:        "true-or-false",
					Description: "A single object returning true or false",
					Schema:      schema,
					Strict:      true,
				},
			},
		})
		ctx := context.Background()
		req := llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "Is 2 greater than 1?"},
		}}
		it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: GPT4Dot1Nano, ContextWindow: 200000}, req)
		require.NoError(t, err)

		var builder strings.Builder
		var doneData *llmapi.DoneData
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventTextDelta:
				builder.WriteString(ev.Text)
			case llmapi.EventStreamDone:
				doneData = ev.DoneData
			}
		}

		require.NoError(t, it.Err())
		require.NotNil(t, doneData)
		assert.Contains(t, builder.String(), `{"is":true}`)
		assert.Equal(t, llmapi.FinishReasonStop, doneData.FinishReason)
	})
}

func TestResponsesAPI(t *testing.T) {
	t.SkipNow()

	token := os.Getenv("OPENAI_TESTING_KEY")
	model := GPT5Dot3Codex

	t.Run("streams a simple text response", func(t *testing.T) {
		c := NewClient(token, Config{ForceResponsesAPI: true})
		ctx := context.Background()
		req := llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleSystem, Content: "You are a helpful assistant. Be very brief."},
			{Role: llmapi.RoleUser, Content: "What is 2+2? Reply with just the number."},
		}}
		it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: model, ContextWindow: 200000}, req)
		require.NoError(t, err)
		defer func() { _ = it.Close() }()

		var text strings.Builder
		var doneData *llmapi.DoneData
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventTextDelta:
				text.WriteString(ev.Text)
			case llmapi.EventStreamDone:
				doneData = ev.DoneData
			case llmapi.EventStreamError:
				t.Fatalf("stream error: %v", ev.Error)
			}
		}
		require.NoError(t, it.Err())
		require.NotNil(t, doneData)

		assert.Contains(t, text.String(), "4")
		assert.Equal(t, llmapi.FinishReasonStop, doneData.FinishReason)
		assert.Equal(t, text.String(), doneData.Message.Content)
		assert.Equal(t, llmapi.RoleAssistant, doneData.Message.Role)

		// Usage should be populated.
		assert.Greater(t, doneData.Usage.TokensSent, 0)
		assert.Greater(t, doneData.Usage.TokensReceived, 0)
	})

	t.Run("invokes a function tool", func(t *testing.T) {
		tools := []llmapi.Tool{
			{Type: llmapi.ToolTypeFunction, Function: llmapi.FunctionDefinition{
				Name:        "get_weather",
				Description: "Get the current weather for a location",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{
							"type":        "string",
							"description": "City name",
						},
					},
					"required":             []string{"location"},
					"additionalProperties": false,
				},
			}},
		}
		c := NewClient(token, Config{
			Tools: tools,
		})
		ctx := context.Background()
		req := llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "What's the weather in Tokyo?"},
		}}
		it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: model, ContextWindow: 200000}, req)
		require.NoError(t, err)
		defer func() { _ = it.Close() }()

		var toolCalls []llmapi.ToolCall
		var doneData *llmapi.DoneData
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventToolCallDone:
				toolCalls = append(toolCalls, *ev.ToolCall)
			case llmapi.EventStreamDone:
				doneData = ev.DoneData
			case llmapi.EventStreamError:
				t.Fatalf("stream error: %v", ev.Error)
			}
		}
		require.NoError(t, it.Err())
		require.NotNil(t, doneData)

		assert.Equal(t, llmapi.FinishReasonToolCall, doneData.FinishReason)
		require.Len(t, toolCalls, 1)
		assert.Equal(t, "get_weather", toolCalls[0].Function.Name)
		assert.NotEmpty(t, toolCalls[0].ID)
		assert.Contains(t, toolCalls[0].Function.Arguments, "Tokyo")

		// DoneData message should also contain the tool calls.
		require.Len(t, doneData.Message.ToolCalls, 1)
		assert.Equal(t, "get_weather", doneData.Message.ToolCalls[0].Function.Name)
	})

	t.Run("multi-turn with tool result", func(t *testing.T) {
		tools := []llmapi.Tool{
			{Type: llmapi.ToolTypeFunction, Function: llmapi.FunctionDefinition{
				Name:        "get_weather",
				Description: "Get the current weather for a location",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{
							"type":        "string",
							"description": "City name",
						},
					},
					"required":             []string{"location"},
					"additionalProperties": false,
				},
			}},
		}
		c := NewClient(token, Config{
			Tools: tools,
		})
		ctx := context.Background()

		// Turn 1: user asks → model calls tool.
		req := llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "What's the weather in Paris?"},
		}}
		it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: model, ContextWindow: 200000}, req)
		require.NoError(t, err)

		var turn1Done *llmapi.DoneData
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventStreamDone:
				turn1Done = ev.DoneData
			case llmapi.EventStreamError:
				t.Fatalf("turn 1 error: %v", ev.Error)
			}
		}
		require.NoError(t, it.Err())
		_ = it.Close()
		require.NotNil(t, turn1Done)
		require.NotEmpty(t, turn1Done.Message.ToolCalls, "model should call get_weather")

		tc := turn1Done.Message.ToolCalls[0]

		// Turn 2: feed tool result → model responds with text.
		req2 := llmapi.Request{Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "What's the weather in Paris?"},
			turn1Done.Message,
			{Role: llmapi.RoleTool, ToolCallID: tc.ID, Content: `{"temperature": "18C", "condition": "sunny"}`},
		}}
		it2, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: model, ContextWindow: 200000}, req2)
		require.NoError(t, err)
		defer func() { _ = it2.Close() }()

		var text strings.Builder
		var turn2Done *llmapi.DoneData
		for {
			ev, ok := it2.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventTextDelta:
				text.WriteString(ev.Text)
			case llmapi.EventStreamDone:
				turn2Done = ev.DoneData
			case llmapi.EventStreamError:
				t.Fatalf("turn 2 error: %v", ev.Error)
			}
		}
		require.NoError(t, it2.Err())
		require.NotNil(t, turn2Done)

		assert.Equal(t, llmapi.FinishReasonStop, turn2Done.FinishReason)
		// The model should mention the weather data we provided.
		response := strings.ToLower(text.String())
		assert.True(t, strings.Contains(response, "18") || strings.Contains(response, "sunny") || strings.Contains(response, "paris"),
			"expected response to reference the weather data, got: %s", text.String())
	})

	t.Run("per-request tools override client tools", func(t *testing.T) {
		// Client has get_weather as a client-level tool, but we pass
		// calculate per-request — the model should only see calculate.
		clientTools := []llmapi.Tool{
			{Type: llmapi.ToolTypeFunction, Function: llmapi.FunctionDefinition{
				Name:        "get_weather",
				Description: "Get current weather for a city",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"city": map[string]any{
							"type":        "string",
							"description": "The city name",
						},
					},
					"required":             []string{"city"},
					"additionalProperties": false,
				},
			}},
		}
		c := NewClient(token, Config{Tools: clientTools})
		ctx := context.Background()

		perRequestTools := []llmapi.Tool{
			{Type: llmapi.ToolTypeFunction, Function: llmapi.FunctionDefinition{
				Name:        "calculate",
				Description: "Evaluate a math expression. You MUST use this tool for any math.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"expression": map[string]any{
							"type":        "string",
							"description": "The math expression to evaluate",
						},
					},
					"required":             []string{"expression"},
					"additionalProperties": false,
				},
			}},
		}
		req := llmapi.Request{
			Messages: []llmapi.Message{
				{Role: llmapi.RoleSystem, Content: "Always use the calculate tool for math. Never compute math yourself."},
				{Role: llmapi.RoleUser, Content: "What is 123 * 456? Use the calculate tool."},
			},
			Tools: perRequestTools,
		}
		it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: model, ContextWindow: 200000}, req)
		require.NoError(t, err)
		defer func() { _ = it.Close() }()

		var toolCalls []llmapi.ToolCall
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventToolCallDone:
				toolCalls = append(toolCalls, *ev.ToolCall)
			case llmapi.EventStreamError:
				t.Fatalf("stream error: %v", ev.Error)
			}
		}
		require.NoError(t, it.Err())
		require.Len(t, toolCalls, 1)
		assert.Equal(t, "calculate", toolCalls[0].Function.Name)
		// Must NOT call get_weather — that's the client-level tool that should be overridden.
		for _, tc := range toolCalls {
			assert.NotEqual(t, "get_weather", tc.Function.Name, "per-request tools should override client tools")
		}
	})

	t.Run("context window exceeded returns ErrContextWindowExceeded", func(t *testing.T) {
		c := NewClient(token, Config{})
		ctx := context.Background()
		msgs := makeMessageTokens(c.(*client), 51)
		_, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: model, ContextWindow: 50}, llmapi.Request{Messages: msgs})
		require.True(t, errors.Is(err, &llmapi.ErrContextWindowExceeded{}))
	})
}

func TestContextWindows(t *testing.T) {
	models := AvailableModels()
	models[GPT4] = 50

	t.Run("CreateCompletion errors with ErrContextWindowExceeded", func(t *testing.T) {
		c := NewClient("", Config{}).(*client)
		ctx := context.Background()
		msgs := makeMessageTokens(c, 51)

		// sut
		req := llmapi.Request{Messages: msgs}
		_, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: GPT4, ContextWindow: 50}, req)
		require.True(t, errors.Is(err, &llmapi.ErrContextWindowExceeded{}), err.Error())
	})
}

func TestCountTokens(t *testing.T) {
	for model := range AvailableModels() {
		t.Run(model, func(t *testing.T) {
			c := NewClient("", Config{}).(*client)
			text := "!Hola mundo!"
			count, _ := c.CountTokens(llmapi.ModelEntry{Name: model, ContextWindow: 200000}, []llmapi.Message{{Content: text}})
			assert.Equal(t, 10, count)
		})
	}

	t.Run("counts_tool_calls_in_messages", func(t *testing.T) {
		c := NewClient("", Config{}).(*client)

		// Baseline: message with only text content.
		baseCount, _ := c.CountTokens(llmapi.ModelEntry{Name: "gpt-4o", ContextWindow: 200000}, []llmapi.Message{{
			Role:    llmapi.RoleAssistant,
			Content: "hello",
		}})

		// Same message with a tool call — should count more tokens.
		withTC, _ := c.CountTokens(llmapi.ModelEntry{Name: "gpt-4o", ContextWindow: 200000}, []llmapi.Message{{
			Role:    llmapi.RoleAssistant,
			Content: "hello",
			ToolCalls: []llmapi.ToolCall{{
				ID:   "call_abc123",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"/foo/bar.go"}`,
				},
			}},
		}})
		assert.Greater(t, withTC, baseCount, "tool calls should add tokens")
	})

	t.Run("counts_tool_call_id_in_tool_messages", func(t *testing.T) {
		c := NewClient("", Config{}).(*client)

		// Baseline: tool message with empty ToolCallID.
		baseCount, _ := c.CountTokens(llmapi.ModelEntry{Name: "gpt-4o", ContextWindow: 200000}, []llmapi.Message{{
			Role:    llmapi.RoleTool,
			Content: "file contents here",
		}})

		// Same message with a ToolCallID.
		withID, _ := c.CountTokens(llmapi.ModelEntry{Name: "gpt-4o", ContextWindow: 200000}, []llmapi.Message{{
			Role:       llmapi.RoleTool,
			Content:    "file contents here",
			ToolCallID: "call_abc123",
		}})
		assert.Greater(t, withID, baseCount, "ToolCallID should add tokens")
	})
}

func makeMessageTokens(c *client, greaterThan int) []llmapi.Message {
	var msgs []llmapi.Message
	for i := 0; ; i++ {
		count, _ := c.CountTokens(llmapi.ModelEntry{Name: GPT4o, ContextWindow: 200000}, msgs)
		if count >= greaterThan {
			break
		}
		msgs = append(msgs, llmapi.Message{
			Content: strconv.Itoa(i), Role: llmapi.RoleUser,
		})
	}
	return msgs
}

func loadTestImage(t *testing.T) image.Image {
	f, err := os.Open("./testdata/chatgpt_image.png")
	require.NoError(t, err)
	img, err := png.Decode(f)
	require.NoError(t, err)
	return img
}

func TestResponsesStreamReasoningTextDelta(t *testing.T) {
	// Build a mock Responses API SSE stream that includes
	// response.reasoning_text.delta events.
	sseEvents := []string{
		`{"type":"response.reasoning_text.delta","delta":"Let me ","content_index":0,"item_id":"item_0","output_index":0,"sequence_number":1}`,
		`{"type":"response.reasoning_text.delta","delta":"think...","content_index":0,"item_id":"item_0","output_index":0,"sequence_number":2}`,
		`{"type":"response.output_text.delta","delta":"The answer is 4.","content_index":0,"item_id":"item_1","output_index":1,"sequence_number":3}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	}
	srv := responsesSSEServer(t, sseEvents)

	c := NewClient("test-key", Config{
		BaseURL:           srv.URL,
		ForceResponsesAPI: true,
	})

	ctx := context.Background()
	it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: GPT5Dot3Codex, ContextWindow: 200000}, llmapi.Request{Messages: []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "What is 2+2?"},
	}})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	var reasoning, text strings.Builder
	var doneData *llmapi.DoneData
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
			doneData = ev.DoneData
		case llmapi.EventStreamError:
			t.Fatalf("stream error: %v", ev.Error)
		}
	}
	require.NoError(t, it.Err())
	require.NotNil(t, doneData)

	assert.Equal(t, "Let me think...", reasoning.String())
	assert.Equal(t, "The answer is 4.", text.String())
	assert.Equal(t, "Let me think...", doneData.Message.ReasoningContent)
}

func TestResponsesStreamReasoningSummaryDelta(t *testing.T) {
	// Verify that response.reasoning_summary_text.delta events
	// are also emitted as EventReasoningDelta.
	sseEvents := []string{
		`{"type":"response.reasoning_summary_text.delta","delta":"Summary: ","summary_index":0,"item_id":"item_0","output_index":0,"sequence_number":1}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"simple math","summary_index":0,"item_id":"item_0","output_index":0,"sequence_number":2}`,
		`{"type":"response.output_text.delta","delta":"4","content_index":0,"item_id":"item_1","output_index":1,"sequence_number":3}`,
		`{"type":"response.completed","response":{"id":"resp_2","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	}
	srv := responsesSSEServer(t, sseEvents)

	c := NewClient("test-key", Config{
		BaseURL:           srv.URL,
		ForceResponsesAPI: true,
	})

	ctx := context.Background()
	it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: GPT5Dot3Codex, ContextWindow: 200000}, llmapi.Request{Messages: []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "What is 2+2?"},
	}})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	var reasoning strings.Builder
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}
		if ev.Type == llmapi.EventReasoningDelta {
			reasoning.WriteString(ev.Reasoning)
		}
		if ev.Type == llmapi.EventStreamError {
			t.Fatalf("stream error: %v", ev.Error)
		}
	}
	require.NoError(t, it.Err())
	assert.Equal(t, "Summary: simple math", reasoning.String())
}

func TestResponsesStreamSkipsSSEBlocksWithoutData(t *testing.T) {
	const (
		delta     = `data: {"type":"response.output_text.delta","delta":"hi","content_index":0,"item_id":"item_0","output_index":0,"sequence_number":1}` + "\n\n"
		completed = `data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"
	)
	tests := []struct {
		name string
		body string
	}{
		{"keepalive comment before first event", ": keepalive\n\n" + delta + completed},
		{"keepalive comment between events", delta + ": keepalive\n\n" + completed},
		{"stray blank line", delta + "\n" + completed},
		{"event without data", "event: keepalive\n\n" + delta + completed},
		{"empty data field", "data:\n\n" + delta + completed},
		{"crlf keepalive", ": keepalive\r\n\r\n" + delta + completed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(srv.Close)

			c := NewClient("test-key", Config{BaseURL: srv.URL, ForceResponsesAPI: true})
			ctx := context.Background()
			it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: GPT5Dot3Codex, ContextWindow: 200000}, llmapi.Request{
				Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
			})
			require.NoError(t, err)
			defer func() { _ = it.Close() }()

			var text strings.Builder
			var doneData *llmapi.DoneData
			for {
				ev, ok := it.Next(ctx)
				if !ok {
					break
				}
				switch ev.Type {
				case llmapi.EventTextDelta:
					text.WriteString(ev.Text)
				case llmapi.EventStreamDone:
					doneData = ev.DoneData
				case llmapi.EventStreamError:
					t.Fatalf("stream error: %v", ev.Error)
				}
			}
			require.NoError(t, it.Err())
			require.NotNil(t, doneData)
			assert.Equal(t, "hi", text.String())
		})
	}
}

func TestResponsesReasoningSummaryConfig(t *testing.T) {
	body := captureResponsesRequestBody(t, Config{
		ReasoningSummary: "concise",
	}, llmapi.Request{Messages: []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hello"},
	}})

	reasoning, ok := body["reasoning"].(map[string]any)
	require.True(t, ok, "request should have a 'reasoning' field")
	assert.Equal(t, "concise", reasoning["summary"])
}

func TestResponsesReasoningSummaryDefaultAuto(t *testing.T) {
	// When no ReasoningSummary is configured and the model supports
	// reasoning, "auto" should be sent as the default.
	body := captureResponsesRequestBody(t, Config{
		ForceResponsesAPI: true,
	}, llmapi.Request{Messages: []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hello"},
	}})

	reasoning, ok := body["reasoning"].(map[string]any)
	require.True(t, ok, "request should have a 'reasoning' field")
	assert.Equal(t, "auto", reasoning["summary"])
}

func TestResponsesReasoningSummaryFromRequest(t *testing.T) {
	// Per-request ReasoningSummary should take precedence over config.
	body := captureResponsesRequestBody(t, Config{
		ReasoningSummary: "concise",
	}, llmapi.Request{
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "hello"},
		},
		ReasoningSummary: llmapi.ReasoningSummaryDetailed,
	})

	reasoning, ok := body["reasoning"].(map[string]any)
	require.True(t, ok, "request should have a 'reasoning' field")
	assert.Equal(t, "detailed", reasoning["summary"])
}

func TestResponsesMaxOutputTokens(t *testing.T) {
	t.Run("request override", func(t *testing.T) {
		body := captureResponsesRequestBody(t, Config{}, llmapi.Request{
			Messages:        []llmapi.Message{{Role: llmapi.RoleUser, Content: "hello"}},
			MaxOutputTokens: 4096,
		})
		assert.Equal(t, float64(4096), body["max_output_tokens"])
	})

	t.Run("config default", func(t *testing.T) {
		body := captureResponsesRequestBody(t, Config{
			MaxCompletionTokens: 2048,
		}, llmapi.Request{
			Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hello"}},
		})
		assert.Equal(t, float64(2048), body["max_output_tokens"])
	})

	t.Run("disabled", func(t *testing.T) {
		body := captureResponsesRequestBody(t, Config{
			MaxCompletionTokens:    2048,
			DisableMaxOutputTokens: true,
		}, llmapi.Request{
			Messages:        []llmapi.Message{{Role: llmapi.RoleUser, Content: "hello"}},
			MaxOutputTokens: 4096,
		})
		assert.NotContains(t, body, "max_output_tokens")
	})
}

func TestResponsesIncludesEncryptedReasoningContent(t *testing.T) {
	body := captureResponsesRequestBody(t, Config{
		ForceResponsesAPI: true,
	}, llmapi.Request{
		Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
	})
	require.Equal(t, []any{"reasoning.encrypted_content"}, body["include"])
}

func TestResponsesSessionHeadersFromPromptCacheKey(t *testing.T) {
	captured := captureResponsesRequest(t, Config{
		ForceResponsesAPI: true,
	}, llmapi.Request{
		Messages:       []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
		PromptCacheKey: "thread-abc",
	})
	assert.Equal(t, "thread-abc", captured.Header.Get("session_id"))
	assert.Equal(t, "thread-abc", captured.Header.Get("x-client-request-id"))
}

func TestResponsesStorefalseAndDisabledParallel(t *testing.T) {
	storeFalse := false
	body := captureResponsesRequestBody(t, Config{
		ForceResponsesAPI:        true,
		Store:                    &storeFalse,
		DisableParallelToolCalls: true,
		ClientMetadata:           map[string]string{"x-codex-installation-id": "install-1"},
	}, llmapi.Request{
		Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
	})
	assert.Equal(t, false, body["store"])
	assert.Equal(t, false, body["parallel_tool_calls"])
	metadata, ok := body["client_metadata"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "install-1", metadata["x-codex-installation-id"])
}

func TestEffortWarningProvenance(t *testing.T) {
	// GPT-5.4-pro accepts medium/high/xhigh only, so "low" is unsupported.
	model := llmapi.ModelEntry{Name: GPT5Dot4Pro, ContextWindow: 200000}

	t.Run("request effort warns", func(t *testing.T) {
		events := captureCompletionEvents(t, Config{}, model, llmapi.Request{
			Messages:        []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
			ReasoningEffort: llmapi.ReasoningEffort("low"),
		})
		require.NotEmpty(t, events)
		assert.Equal(t, llmapi.EventRateLimitWarning, events[0].Type)
	})

	t.Run("config effort is silent", func(t *testing.T) {
		events := captureCompletionEvents(t, Config{ReasoningEffort: "low"}, model, llmapi.Request{
			Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
		})
		for _, ev := range events {
			assert.NotEqual(t, llmapi.EventRateLimitWarning, ev.Type)
		}
	})
}

func captureCompletionEvents(
	t *testing.T, cfg Config, model llmapi.ModelEntry, req llmapi.Request,
) []llmapi.Event {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}, \"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	cfg.BaseURL = srv.URL
	ctx := context.Background()
	it, err := NewClient("test-key", cfg).CreateCompletion(ctx, model, req)
	require.NoError(t, err)
	defer func() { _ = it.Close() }()
	var events []llmapi.Event
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}
		events = append(events, ev)
	}
	return events
}

func TestForceResponsesAPIRouting(t *testing.T) {
	req := llmapi.Request{
		Messages:        []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
		ReasoningEffort: llmapi.ReasoningEffort("high"),
		Tools: []llmapi.Tool{{
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionDefinition{
				Name:        "noop",
				Description: "does nothing",
				Parameters:  map[string]any{"type": "object"},
			},
		}},
	}

	t.Run("forced uses responses endpoint", func(t *testing.T) {
		path := captureRequestPath(t, Config{ForceResponsesAPI: true}, req)
		assert.Contains(t, path, "/responses")
		assert.NotContains(t, path, "/chat/completions")
	})

	t.Run("not forced uses chat completions endpoint", func(t *testing.T) {
		path := captureRequestPath(t, Config{}, req)
		assert.Contains(t, path, "/chat/completions")
	})
}

func TestResponsesReasoningRoundTrip(t *testing.T) {
	reasoningItem := `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"plan"}],"encrypted_content":"ENC_BLOB","status":"completed"}`
	functionCallItem := `{"type":"function_call","id":"fc_item_1","call_id":"call_1","name":"skill","arguments":"{\"name\":\"explore\",\"args\":\"\"}","status":"completed"}`
	sseEvents := []string{
		`{"type":"response.output_item.done","output_index":0,"sequence_number":1,"item":` + reasoningItem + `}`,
		`{"type":"response.output_item.done","output_index":1,"sequence_number":2,"item":` + functionCallItem + `}`,
		`{"type":"response.completed","sequence_number":3,"response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":5}}}`,
	}
	srv := responsesSSEServer(t, sseEvents)

	c := NewClient("test-key", Config{
		BaseURL:           srv.URL,
		ForceResponsesAPI: true,
	})

	ctx := context.Background()
	it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: "gpt-4o", ContextWindow: 200000}, llmapi.Request{Messages: []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "explore"},
	}})
	require.NoError(t, err)

	var doneData *llmapi.DoneData
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}
		if ev.Type == llmapi.EventStreamDone {
			doneData = ev.DoneData
		}
	}
	require.NoError(t, it.Err())
	require.NoError(t, it.Close())
	require.NotNil(t, doneData)

	// The assistant message must carry both opaque output items in stream
	// order so they can be replayed.
	require.Len(t, doneData.Message.ProviderItems, 2)
	assert.JSONEq(t, reasoningItem, string(doneData.Message.ProviderItems[0]))
	assert.JSONEq(t, functionCallItem, string(doneData.Message.ProviderItems[1]))
	require.Len(t, doneData.Message.ToolCalls, 1)
	assert.Equal(t, "call_1", doneData.Message.ToolCalls[0].ID)

	// Now feed that assistant message + a tool result back into a new
	// request and verify the input array faithfully reproduces the
	// reasoning + function_call + function_call_output sequence.
	captured := captureResponsesRequest(t, Config{
		ForceResponsesAPI: true,
	}, llmapi.Request{
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "explore"},
			doneData.Message,
			{Role: llmapi.RoleTool, ToolCallID: "call_1", Content: "ok"},
		},
	})

	input, ok := captured.Body["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 4)

	// 0: original user message.
	user, ok := input[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "user", user["role"])

	// 1: reasoning item with encrypted_content preserved.
	reasoning, ok := input[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "reasoning", reasoning["type"])
	assert.Equal(t, "rs_1", reasoning["id"])
	assert.Equal(t, "ENC_BLOB", reasoning["encrypted_content"])

	// 2: function_call item with original call_id.
	fnCall, ok := input[2].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "function_call", fnCall["type"])
	assert.Equal(t, "call_1", fnCall["call_id"])
	assert.Equal(t, "skill", fnCall["name"])

	// 3: function_call_output we produced from the tool message.
	fnOut, ok := input[3].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "function_call_output", fnOut["type"])
	assert.Equal(t, "call_1", fnOut["call_id"])
	assert.Equal(t, "ok", fnOut["output"])
}

func TestResponsesAutoDiagProviderItemsRoundTrip(t *testing.T) {
	originalCallID := "call_orig"
	syntheticCallID := "auto-diag-" + originalCallID
	originalFnCall := `{"type":"function_call","call_id":"` + originalCallID +
		`","name":"apply_patch","arguments":"{\"patch\":\"p\"}"}`
	syntheticFnCall := `{"type":"function_call","call_id":"` + syntheticCallID +
		`","name":"check_file_errors","arguments":"{\"path\":\"/workspace/main.go\"}"}`

	assistantMsg := llmapi.Message{
		Role: llmapi.RoleAssistant,
		ProviderItems: []json.RawMessage{
			json.RawMessage(originalFnCall),
			json.RawMessage(syntheticFnCall),
		},
		ToolCalls: []llmapi.ToolCall{
			{
				ID:   originalCallID,
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "apply_patch",
					Arguments: `{"patch":"p"}`,
				},
			},
			{
				ID:   syntheticCallID,
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "check_file_errors",
					Arguments: `{"path":"/workspace/main.go"}`,
				},
			},
		},
	}

	captured := captureResponsesRequest(t, Config{
		ForceResponsesAPI: true,
	}, llmapi.Request{
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "edit"},
			assistantMsg,
			{Role: llmapi.RoleTool, ToolCallID: originalCallID, Content: "applied"},
			{Role: llmapi.RoleTool, ToolCallID: syntheticCallID, Content: "no errors"},
		},
	})

	input, ok := captured.Body["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 5)

	// 0: user message.
	user, ok := input[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "user", user["role"])

	// 1: original function_call from ProviderItems.
	origCall, ok := input[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "function_call", origCall["type"])
	assert.Equal(t, originalCallID, origCall["call_id"])
	assert.Equal(t, "apply_patch", origCall["name"])

	// 2: synthetic function_call appended by auto-diagnostics.
	syntheticCall, ok := input[2].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "function_call", syntheticCall["type"])
	assert.Equal(t, syntheticCallID, syntheticCall["call_id"])
	assert.Equal(t, "check_file_errors", syntheticCall["name"])

	// 3: original function_call_output.
	origOut, ok := input[3].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "function_call_output", origOut["type"])
	assert.Equal(t, originalCallID, origOut["call_id"])
	assert.Equal(t, "applied", origOut["output"])

	// 4: synthetic function_call_output. This must follow a matching
	// function_call earlier in the input array — that is the entire
	// point of this test.
	syntheticOut, ok := input[4].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "function_call_output", syntheticOut["type"])
	assert.Equal(t, syntheticCallID, syntheticOut["call_id"])
	assert.Equal(t, "no errors", syntheticOut["output"])
}

func TestMessageProviderItemsJSONRoundTrip(t *testing.T) {
	original := llmapi.Message{
		Role:          llmapi.RoleAssistant,
		Content:       "hi",
		ProviderItems: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"X"}`)},
	}
	b, err := json.Marshal(original)
	require.NoError(t, err)

	var got llmapi.Message
	require.NoError(t, json.Unmarshal(b, &got))
	require.Len(t, got.ProviderItems, 1)
	assert.JSONEq(t,
		`{"type":"reasoning","encrypted_content":"X"}`,
		string(got.ProviderItems[0]))
}

// responsesSSEServer creates a test HTTP server that returns the given SSE events
// in the Responses API format. Use for testing the responses stream iterator.
func responsesSSEServer(t *testing.T, events []string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, ev := range events {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// captureResponsesRequestBody creates a test HTTP server that captures the JSON
// request body for a Responses API call. The server returns a minimal SSE
// response so the client completes without error.
func captureResponsesRequestBody(t *testing.T, cfg Config, req llmapi.Request) map[string]any {
	return captureResponsesRequest(t, cfg, req).Body
}

type capturedResponsesRequest struct {
	Method string
	Path   string
	Header http.Header
	Body   map[string]any
}

func captureResponsesRequest(t *testing.T, cfg Config, req llmapi.Request) capturedResponsesRequest {
	t.Helper()
	cfg.ForceResponsesAPI = true
	var captured capturedResponsesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.Method = r.Method
		captured.Path = r.URL.Path
		captured.Header = r.Header.Clone()
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &captured.Body))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "data: %s\n\n",
			`{"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	}))
	t.Cleanup(srv.Close)

	cfg.BaseURL = srv.URL
	c := NewClient("test-key", cfg)
	ctx := context.Background()
	it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: GPT5Dot3Codex, ContextWindow: 200000}, req)
	require.NoError(t, err)
	for {
		_, ok := it.Next(ctx)
		if !ok {
			break
		}
	}
	_ = it.Close()
	require.NotNil(t, captured.Body, "server should have received a request")
	return captured
}

// captureRequestBody creates a test HTTP server that captures the JSON request
// body, then creates a client pointing at that server and calls CreateCompletion.
// Returns the parsed request body so tests can assert config values are wired through.
func captureRequestBody(t *testing.T, cfg Config, req llmapi.Request) map[string]any {
	t.Helper()
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &captured))
		// Return a minimal SSE response so the client doesn't hang.
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"\"}, \"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	cfg.BaseURL = srv.URL
	c := NewClient("test-key", cfg)
	ctx := context.Background()
	it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: GPT4Dot1Nano, ContextWindow: 200000}, req)
	require.NoError(t, err)
	// Drain the stream.
	for {
		_, ok := it.Next(ctx)
		if !ok {
			break
		}
	}
	_ = it.Close()
	require.NotNil(t, captured, "server should have received a request")
	return captured
}

// captureRequestPath records the URL path of the request CreateCompletion
// makes, so tests can assert which endpoint (/responses vs /chat/completions)
// the routing chose. The server returns a minimal SSE response for both
// endpoints so the client completes without error regardless of route.
func captureRequestPath(t *testing.T, cfg Config, req llmapi.Request) string {
	t.Helper()
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if strings.Contains(r.URL.Path, "/responses") {
			_, _ = fmt.Fprintf(w, "data: %s\n\n",
				`{"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"\"}, \"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(srv.Close)

	cfg.BaseURL = srv.URL
	c := NewClient("test-key", cfg)
	ctx := context.Background()
	it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: GPT4Dot1Nano, ContextWindow: 200000}, req)
	require.NoError(t, err)
	for {
		_, ok := it.Next(ctx)
		if !ok {
			break
		}
	}
	_ = it.Close()
	require.NotEmpty(t, path, "server should have received a request")
	return path
}
