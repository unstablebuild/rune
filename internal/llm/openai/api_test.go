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
	"encoding/json"
	"testing"

	"github.com/openai/openai-go/v2/shared"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

func TestOpenAIMessageFromModel_JSONDeserializedToolCalls(t *testing.T) {
	// Simulate a message that was persisted and loaded back from JSON.
	original := llmapi.Message{
		Role:    llmapi.RoleAssistant,
		Content: "Let me read that file.",
		ToolCalls: []llmapi.ToolCall{
			{
				ID:   "call_123",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"/tmp/test.txt"}`,
				},
			},
		},
	}

	// Round-trip through JSON to simulate persistence
	data, err := json.Marshal(original)
	require.NoError(t, err)

	var deserialized llmapi.Message
	require.NoError(t, json.Unmarshal(data, &deserialized))

	// This must not panic
	result, err := openAIMessageFromModel(deserialized)
	require.NoError(t, err)
	assert.Len(t, result.OfAssistant.ToolCalls, 1)
	assert.Equal(t, "call_123", result.OfAssistant.ToolCalls[0].OfFunction.ID)
	assert.Equal(t, "read_file", result.OfAssistant.ToolCalls[0].OfFunction.Function.Name)
	assert.Equal(t, `{"path":"/tmp/test.txt"}`, result.OfAssistant.ToolCalls[0].OfFunction.Function.Arguments)
}

func TestOpenAIMessageFromModel_NoToolCalls(t *testing.T) {
	msg := llmapi.Message{
		Role:    llmapi.RoleAssistant,
		Content: "Hello!",
	}
	result, err := openAIMessageFromModel(msg)
	require.NoError(t, err)
	assert.Empty(t, result.OfAssistant.ToolCalls)
}

func TestOpenAIMessageFromModel_WithToolCalls(t *testing.T) {
	msg := llmapi.Message{
		Role: llmapi.RoleAssistant,
		ToolCalls: []llmapi.ToolCall{
			{
				ID:   "call_456",
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{
					Name:      "edit_file",
					Arguments: `{"path":"/tmp/x.txt"}`,
				},
			},
		},
	}
	result, err := openAIMessageFromModel(msg)
	require.NoError(t, err)
	assert.Len(t, result.OfAssistant.ToolCalls, 1)
	assert.Equal(t, "edit_file", result.OfAssistant.ToolCalls[0].OfFunction.Function.Name)
}

func TestResponsesToolsFromModel(t *testing.T) {
	tools := []llmapi.Tool{
		{
			Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionDefinition{
				Name:        "read_file",
				Description: "Read a file",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":   map[string]any{"type": "string", "description": "File path"},
						"offset": map[string]any{"type": []string{"integer", "null"}, "description": "Line offset"},
					},
					"required":             []string{"path", "offset"},
					"additionalProperties": false,
				},
			},
		},
	}

	result := responsesToolsFromModel(tools)
	require.Len(t, result, 1)

	ft := result[0].OfFunction
	require.NotNil(t, ft)
	assert.Equal(t, "read_file", ft.Name)

	// Verify the function produces valid JSON with strict-compliant schema.
	data, err := json.Marshal(ft)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"additionalProperties":false`)
}

// schemaJSON is a tiny strict-compliant tool schema used by the parameter
// helper tests below. It is intentionally simple so the tests can assert
// the conversion preserves both the structural fields ("type",
// "properties", "required") and any leaf annotations.
const schemaJSON = `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`

type typedSchema struct {
	Type                 string         `json:"type"`
	Properties           map[string]any `json:"properties"`
	Required             []string       `json:"required"`
	AdditionalProperties bool           `json:"additionalProperties"`
}

func newTypedSchema() typedSchema {
	return typedSchema{
		Type: "object",
		Properties: map[string]any{
			"path": map[string]any{"type": "string"},
		},
		Required:             []string{"path"},
		AdditionalProperties: false,
	}
}

func TestOpenAIToolsFromModel_ParameterShapes(t *testing.T) {
	mapParams := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"path": map[string]any{"type": "string"}},
		"required":             []any{"path"},
		"additionalProperties": false,
	}

	cases := []struct {
		name       string
		parameters any
		wantNil    bool
	}{
		{"map[string]any", mapParams, false},
		{"shared.FunctionParameters", shared.FunctionParameters(mapParams), false},
		{"json.RawMessage", json.RawMessage([]byte(schemaJSON)), false},
		{"[]byte", []byte(schemaJSON), false},
		{"typed struct", newTypedSchema(), false},
		{"invalid json.RawMessage", json.RawMessage([]byte("not json")), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools := []llmapi.Tool{{
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionDefinition{
					Name:        "read_file",
					Description: "Read a file",
					Parameters:  tc.parameters,
				},
			}}

			got := openAIToolsFromModel(tools)
			require.Len(t, got, 1)
			def := got[0].OfFunction
			require.NotNil(t, def)

			if tc.wantNil {
				assert.Nil(t, def.Function.Parameters)
				return
			}

			require.NotNil(t, def.Function.Parameters, "schema must survive conversion")
			assert.Equal(t, "object", def.Function.Parameters["type"])
			props, ok := def.Function.Parameters["properties"].(map[string]any)
			require.True(t, ok, "properties must be a map")
			assert.Contains(t, props, "path")
		})
	}
}

func TestResponsesToolsFromModel_ParameterShapes(t *testing.T) {
	mapParams := map[string]any{
		"type":                 "object",
		"properties":           map[string]any{"path": map[string]any{"type": "string"}},
		"required":             []any{"path"},
		"additionalProperties": false,
	}

	cases := []struct {
		name       string
		parameters any
		wantNil    bool
	}{
		{"map[string]any", mapParams, false},
		{"shared.FunctionParameters", shared.FunctionParameters(mapParams), false},
		{"json.RawMessage", json.RawMessage([]byte(schemaJSON)), false},
		{"[]byte", []byte(schemaJSON), false},
		{"typed struct", newTypedSchema(), false},
		{"invalid json.RawMessage", json.RawMessage([]byte("not json")), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tools := []llmapi.Tool{{
				Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionDefinition{
					Name:        "read_file",
					Description: "Read a file",
					Parameters:  tc.parameters,
				},
			}}

			got := responsesToolsFromModel(tools)
			require.Len(t, got, 1)
			ft := got[0].OfFunction
			require.NotNil(t, ft)

			if tc.wantNil {
				assert.Nil(t, ft.Parameters)
				return
			}

			require.NotNil(t, ft.Parameters, "schema must survive conversion")
			assert.Equal(t, "object", ft.Parameters["type"])
			props, ok := ft.Parameters["properties"].(map[string]any)
			require.True(t, ok, "properties must be a map")
			assert.Contains(t, props, "path")
		})
	}
}
