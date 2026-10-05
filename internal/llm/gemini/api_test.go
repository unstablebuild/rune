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
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"google.golang.org/genai"
)

func TestContentsFromMessages(t *testing.T) {
	msgs := []llmapi.Message{
		{Role: llmapi.RoleSystem, Content: "be terse"},
		{Role: llmapi.RoleSystem, Content: "and helpful"},
		{Role: llmapi.RoleUser, Content: "hello"},
		{Role: llmapi.RoleAssistant, Content: "calling tool", ToolCalls: []llmapi.ToolCall{
			{ID: "call_1", Function: llmapi.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}},
		}},
		{Role: llmapi.RoleTool, Name: "read_file", ToolCallID: "call_1", Content: `{"output":"contents"}`},
	}

	system, contents := contentsFromMessages(msgs)
	assert.Equal(t, "be terse\n\nand helpful", system)
	require.Len(t, contents, 3)

	// User.
	assert.Equal(t, genai.RoleUser, contents[0].Role)
	require.Len(t, contents[0].Parts, 1)
	assert.Equal(t, "hello", contents[0].Parts[0].Text)

	// Assistant: text part + function call part.
	assert.Equal(t, genai.RoleModel, contents[1].Role)
	require.Len(t, contents[1].Parts, 2)
	assert.Equal(t, "calling tool", contents[1].Parts[0].Text)
	fc := contents[1].Parts[1].FunctionCall
	require.NotNil(t, fc)
	assert.Equal(t, "call_1", fc.ID)
	assert.Equal(t, "read_file", fc.Name)
	assert.Equal(t, "a.go", fc.Args["path"])

	// Tool result becomes a user-role function response (no tool role in Gemini).
	assert.Equal(t, genai.RoleUser, contents[2].Role)
	require.Len(t, contents[2].Parts, 1)
	fr := contents[2].Parts[0].FunctionResponse
	require.NotNil(t, fr)
	assert.Equal(t, "call_1", fr.ID)
	assert.Equal(t, "read_file", fr.Name)
	assert.Equal(t, "contents", fr.Response["output"])
}

func TestToolResponsePartNonJSON(t *testing.T) {
	part := toolResponsePart(llmapi.Message{
		Role: llmapi.RoleTool, Name: "grep", ToolCallID: "c1", Content: "plain text",
	})
	require.NotNil(t, part.FunctionResponse)
	assert.Equal(t, "plain text", part.FunctionResponse.Response["output"])
}

func TestThoughtSignatureRoundTrip(t *testing.T) {
	sig := []byte{0x01, 0x02, 0x03, 0xff}

	var tc llmapi.ToolCall
	tc.ID = "call_1"
	tc.Function = llmapi.FunctionCall{Name: "bash", Arguments: `{"cmd":"ls"}`}
	setThoughtSignature(&tc, sig)

	msg := llmapi.Message{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{tc}}
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var restored llmapi.Message
	require.NoError(t, json.Unmarshal(data, &restored))
	require.Len(t, restored.ToolCalls, 1)

	parts := assistantParts(restored)
	require.Len(t, parts, 1)
	require.NotNil(t, parts[0].FunctionCall)
	assert.Equal(t, sig, parts[0].ThoughtSignature)
}

func TestThoughtSignatureEmpty(t *testing.T) {
	tc := llmapi.ToolCall{ID: "call_1", Function: llmapi.FunctionCall{Name: "bash"}}
	setThoughtSignature(&tc, nil)
	assert.Nil(t, tc.ProviderFields)
	assert.Nil(t, thoughtSignature(tc))
}

func TestAssistantPartsMissingSignatureFallback(t *testing.T) {
	msg := llmapi.Message{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{
		{ID: "call_1", Function: llmapi.FunctionCall{Name: "agent"}},
		{ID: "call_2", Function: llmapi.FunctionCall{Name: "agent"}},
	}}
	parts := assistantParts(msg)
	require.Len(t, parts, 2)
	assert.Equal(t, []byte(skipSignatureValidator), parts[0].ThoughtSignature)
	assert.Nil(t, parts[1].ThoughtSignature)
}

func TestAssistantPartsRealSignaturePreferred(t *testing.T) {
	tc := llmapi.ToolCall{ID: "call_1", Function: llmapi.FunctionCall{Name: "agent"}}
	setThoughtSignature(&tc, []byte("realsig"))
	parts := assistantParts(llmapi.Message{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{tc}})
	require.Len(t, parts, 1)
	assert.Equal(t, []byte("realsig"), parts[0].ThoughtSignature)
}

func TestUserPartsImageDataURI(t *testing.T) {
	// "AAEC" base64-decodes to bytes {0,1,2}.
	msg := llmapi.Message{
		Role: llmapi.RoleUser,
		MultiContent: []llmapi.ContentPart{
			{Type: llmapi.ContentPartTypeText, Text: "look"},
			{Type: llmapi.ContentPartTypeImageURL, ImageURL: "data:image/png;base64,AAEC"},
		},
	}
	parts := userParts(msg)
	require.Len(t, parts, 2)
	assert.Equal(t, "look", parts[0].Text)
	require.NotNil(t, parts[1].InlineData)
	assert.Equal(t, "image/png", parts[1].InlineData.MIMEType)
	assert.Equal(t, []byte{0, 1, 2}, parts[1].InlineData.Data)
}

func TestToolsFromModel(t *testing.T) {
	tools := []llmapi.Tool{
		{Function: llmapi.FunctionDefinition{
			Name:        "read_file",
			Description: "read a file",
			Parameters: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"$schema":              "http://json-schema.org/draft-07/schema#",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
				"required": []any{"path"},
			},
		}},
		{Function: llmapi.FunctionDefinition{Name: "noop", Description: "no params"}},
	}

	out := toolsFromModel(tools)
	require.Len(t, out, 1)
	decls := out[0].FunctionDeclarations
	require.Len(t, decls, 2)

	assert.Equal(t, "read_file", decls[0].Name)
	schema, ok := decls[0].ParametersJsonSchema.(map[string]any)
	require.True(t, ok)
	// Unsupported keywords are dropped.
	_, hasAdditional := schema["additionalProperties"]
	assert.False(t, hasAdditional)
	_, hasSchema := schema["$schema"]
	assert.False(t, hasSchema)
	_, hasProps := schema["properties"]
	assert.True(t, hasProps)

	// Parameterless tool omits the schema entirely.
	assert.Equal(t, "noop", decls[1].Name)
	assert.Nil(t, decls[1].ParametersJsonSchema)
}

func TestToolConfig(t *testing.T) {
	tests := []struct {
		choice llmapi.ToolChoice
		want   genai.FunctionCallingConfigMode
		nilCfg bool
	}{
		{llmapi.ToolChoiceAuto, genai.FunctionCallingConfigModeAuto, false},
		{llmapi.ToolChoiceRequired, genai.FunctionCallingConfigModeAny, false},
		{llmapi.ToolChoiceNone, genai.FunctionCallingConfigModeNone, false},
		{llmapi.ToolChoice(""), "", true},
	}
	for _, tt := range tests {
		cfg := toolConfig(tt.choice)
		if tt.nilCfg {
			assert.Nil(t, cfg)
			continue
		}
		require.NotNil(t, cfg)
		assert.Equal(t, tt.want, cfg.FunctionCallingConfig.Mode)
	}
}

func TestThinkingConfig(t *testing.T) {
	assert.Nil(t, thinkingConfig(""))
	assert.Nil(t, thinkingConfig("none"))

	tc := thinkingConfig("high")
	require.NotNil(t, tc)
	assert.Equal(t, genai.ThinkingLevelHigh, tc.ThinkingLevel)
	assert.True(t, tc.IncludeThoughts)
}

func TestNormalizeEffort(t *testing.T) {
	tests := []struct {
		model   string
		effort  string
		want    string
		hasWarn bool
	}{
		// Gemini 3 requires a thinking config for function-call signatures, so
		// an unset/none/unsupported effort falls back to medium rather than
		// omitting thinking entirely.
		{"gemini-3-flash-preview", "", "medium", false},
		{"gemini-3-flash-preview", "none", "medium", false},
		{"gemini-3-flash-preview", "high", "high", false},
		{"gemini-3-flash-preview", "minimal", "minimal", false},
		{"gemini-3-flash-preview", "xhigh", "medium", true},
		{"gemini-3-flash-preview", "bogus", "medium", true},
		{"gemini-3.1-pro-preview", "", "medium", false},
		// Gemini 2.x does not require signatures; empty stays empty.
		{"gemini-2.5-flash", "", "", false},
		{"gemini-2.5-flash", "none", "", false},
		{"gemini-2.5-flash", "high", "high", false},
		{"gemini-2.5-flash", "xhigh", "", true},
	}
	for _, tt := range tests {
		got, warn := NormalizeEffort(tt.model, tt.effort)
		assert.Equal(t, tt.want, got, "%s effort %q", tt.model, tt.effort)
		assert.Equal(t, tt.hasWarn, warn != "", "%s effort %q warning", tt.model, tt.effort)
	}
}

func TestUsageFromMetadata(t *testing.T) {
	usage := usageFromMetadata(&genai.GenerateContentResponseUsageMetadata{
		PromptTokenCount:        100,
		CandidatesTokenCount:    40,
		ThoughtsTokenCount:      15,
		CachedContentTokenCount: 30,
	})
	assert.Equal(t, 100, usage.TokensSent)
	assert.Equal(t, 40, usage.TokensReceived)
	assert.Equal(t, 15, usage.TokensReasoned)
	assert.Equal(t, 30, usage.TokensCached)

	assert.Equal(t, llmapi.Usage{}, usageFromMetadata(nil))
}

func TestMapFinishReason(t *testing.T) {
	assert.Equal(t, llmapi.FinishReasonStop, mapFinishReason(genai.FinishReasonStop, false))
	assert.Equal(t, llmapi.FinishReasonToolCall, mapFinishReason(genai.FinishReasonStop, true))
	assert.Equal(t, llmapi.FinishReasonLength, mapFinishReason(genai.FinishReasonMaxTokens, false))
	assert.Equal(t, llmapi.FinishReasonContentFilter, mapFinishReason(genai.FinishReasonSafety, false))
	assert.Equal(t, llmapi.FinishReasonNull, mapFinishReason("", false))
}

func TestMapError(t *testing.T) {
	assert.NoError(t, mapError(nil))

	apiErr := genai.APIError{Code: 400, Status: "INVALID_ARGUMENT", Message: "bad schema"}
	got := mapError(apiErr)
	require.Error(t, got)
	assert.Contains(t, got.Error(), "400")
	assert.Contains(t, got.Error(), "INVALID_ARGUMENT")
	assert.Contains(t, got.Error(), "bad schema")

	plain := errors.New("network down")
	assert.Equal(t, plain, mapError(plain))
}

func TestApplyResponseFormat(t *testing.T) {
	schema := jsonSchemaMarshaler(`{"type":"object","additionalProperties":false,"properties":{"x":{"type":"string"}}}`)
	cfg := &genai.GenerateContentConfig{}
	applyResponseFormat(cfg, &llmapi.ResponseFormat{
		Type: llmapi.ResponseFormatTypeJSONSchema,
		JSONSchema: &llmapi.ResponseFormatJSONSchema{
			Name:   "out",
			Schema: schema,
		},
	}, nil)
	assert.Equal(t, "application/json", cfg.ResponseMIMEType)
	m, ok := cfg.ResponseJsonSchema.(map[string]any)
	require.True(t, ok)
	_, hasAdditional := m["additionalProperties"]
	assert.False(t, hasAdditional)
}

// jsonSchemaMarshaler adapts a raw JSON schema string to json.Marshaler.
type jsonSchemaMarshaler string

func (s jsonSchemaMarshaler) MarshalJSON() ([]byte, error) {
	return json.RawMessage(s).MarshalJSON()
}
