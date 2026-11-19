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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"google.golang.org/genai"
)

// contentsFromMessages converts llmapi messages into Gemini contents. System
// messages are accumulated separately and returned as the system instruction
// text. Gemini has no tool role: tool results are encoded as user-role
// contents carrying a FunctionResponse part.
func contentsFromMessages(msgs []llmapi.Message) (system string, contents []*genai.Content) {
	var systemParts []string
	for _, msg := range msgs {
		switch msg.Role {
		case llmapi.RoleSystem:
			if msg.Content != "" {
				systemParts = append(systemParts, msg.Content)
			}

		case llmapi.RoleUser:
			contents = append(contents, &genai.Content{
				Role:  genai.RoleUser,
				Parts: userParts(msg),
			})

		case llmapi.RoleAssistant:
			parts := assistantParts(msg)
			if len(parts) > 0 {
				contents = append(contents, &genai.Content{
					Role:  genai.RoleModel,
					Parts: parts,
				})
			}

		case llmapi.RoleTool:
			contents = append(contents, &genai.Content{
				Role:  genai.RoleUser,
				Parts: []*genai.Part{toolResponsePart(msg)},
			})
		}
	}
	return strings.Join(systemParts, "\n\n"), contents
}

// userParts converts a user message into Gemini parts, mapping image content
// parts to inline blobs or file URIs.
func userParts(msg llmapi.Message) []*genai.Part {
	if len(msg.MultiContent) == 0 {
		return []*genai.Part{genai.NewPartFromText(msg.Content)}
	}
	parts := make([]*genai.Part, 0, len(msg.MultiContent))
	for _, p := range msg.MultiContent {
		switch p.Type {
		case llmapi.ContentPartTypeImageURL:
			parts = append(parts, imagePart(p.ImageURL))
		default:
			parts = append(parts, genai.NewPartFromText(p.Text))
		}
	}
	return parts
}

// imagePart converts a data URI or remote URL into a Gemini image part.
func imagePart(url string) *genai.Part {
	if strings.HasPrefix(url, "data:") {
		mediaType, data := parseDataURI(url)
		return genai.NewPartFromBytes(data, mediaType)
	}
	return genai.NewPartFromURI(url, "")
}

// parseDataURI extracts the media type and decoded bytes from a base64 data URI.
func parseDataURI(uri string) (mediaType string, data []byte) {
	uri = strings.TrimPrefix(uri, "data:")
	parts := strings.SplitN(uri, ";base64,", 2)
	if len(parts) != 2 {
		return "image/png", []byte(uri)
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return parts[0], []byte(parts[1])
	}
	return parts[0], decoded
}

// assistantParts converts an assistant message into Gemini parts: optional
// text followed by one FunctionCall part per tool call.
func assistantParts(msg llmapi.Message) []*genai.Part {
	var parts []*genai.Part
	if msg.Content != "" {
		parts = append(parts, genai.NewPartFromText(msg.Content))
	}
	for i, tc := range msg.ToolCalls {
		args := map[string]any{}
		if tc.Function.Arguments != "" {
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		}
		// Gemini 3+ validates the thought_signature only on the first
		// functionCall part of a step; parallel calls after it carry none.
		// Fall back to the documented validator-skip sentinel when the first
		// call has no captured signature (legacy or cross-model history).
		sig := thoughtSignature(tc)
		if len(sig) == 0 && i == 0 {
			sig = []byte(skipSignatureValidator)
		}
		parts = append(parts, &genai.Part{
			FunctionCall: &genai.FunctionCall{
				ID:   tc.ID,
				Name: tc.Function.Name,
				Args: args,
			},
			ThoughtSignature: sig,
		})
	}
	return parts
}

// toolResponsePart encodes a tool-result message as a FunctionResponse part.
// The result content is decoded as a JSON object when possible; otherwise it
// is wrapped under an "output" key, matching the FunctionResponse contract.
func toolResponsePart(msg llmapi.Message) *genai.Part {
	var response map[string]any
	if err := json.Unmarshal([]byte(msg.Content), &response); err != nil || response == nil {
		response = map[string]any{"output": msg.Content}
	}
	return &genai.Part{
		FunctionResponse: &genai.FunctionResponse{
			ID:       msg.ToolCallID,
			Name:     msg.Name,
			Response: response,
		},
	}
}

// toolsFromModel converts llmapi tools into a single Gemini Tool carrying all
// function declarations. Parameterless tools omit Parameters entirely so the
// request never carries an empty-properties object.
func toolsFromModel(tools []llmapi.Tool) []*genai.Tool {
	if len(tools) == 0 {
		return nil
	}
	decls := make([]*genai.FunctionDeclaration, 0, len(tools))
	for _, tool := range tools {
		decl := &genai.FunctionDeclaration{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
		}
		if schema := normalizeSchema(tool.Function.Parameters); schema != nil {
			decl.ParametersJsonSchema = schema
		}
		decls = append(decls, decl)
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

// normalizeSchema converts an agent JSON-schema value into a sanitized
// map[string]any suitable for Gemini's ParametersJsonSchema field. It returns
// nil for parameterless tools (no properties) so Parameters is omitted.
func normalizeSchema(params any) map[string]any {
	m := schemaAsMap(params)
	if m == nil {
		return nil
	}
	sanitizeSchema(m)
	if props, ok := m["properties"].(map[string]any); !ok || len(props) == 0 {
		return nil
	}
	return m
}

// schemaAsMap coerces a JSON-schema value (map, raw JSON, or struct) into a
// map[string]any.
func schemaAsMap(params any) map[string]any {
	switch p := params.(type) {
	case nil:
		return nil
	case map[string]any:
		return p
	case json.RawMessage:
		return unmarshalSchema(p)
	case []byte:
		return unmarshalSchema(p)
	default:
		b, err := json.Marshal(p)
		if err != nil {
			return nil
		}
		return unmarshalSchema(b)
	}
}

func unmarshalSchema(b []byte) map[string]any {
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// sanitizeSchema strips JSON-schema keywords Gemini does not accept and
// recurses into nested schemas. Gemini's JSON-schema support omits meta
// keywords like $schema and additionalProperties.
func sanitizeSchema(node map[string]any) {
	if node == nil {
		return
	}
	delete(node, "$schema")
	delete(node, "additionalProperties")

	if props, ok := node["properties"].(map[string]any); ok {
		for _, v := range props {
			if child, ok := v.(map[string]any); ok {
				sanitizeSchema(child)
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		sanitizeSchema(items)
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if arr, ok := node[key].([]any); ok {
			for _, v := range arr {
				if child, ok := v.(map[string]any); ok {
					sanitizeSchema(child)
				}
			}
		}
	}
}

// toolConfig maps the cross-provider ToolChoice onto Gemini's function-calling
// mode. An empty choice leaves the provider default (AUTO).
func toolConfig(choice llmapi.ToolChoice) *genai.ToolConfig {
	var mode genai.FunctionCallingConfigMode
	switch choice {
	case llmapi.ToolChoiceAuto:
		mode = genai.FunctionCallingConfigModeAuto
	case llmapi.ToolChoiceRequired:
		mode = genai.FunctionCallingConfigModeAny
	case llmapi.ToolChoiceNone:
		mode = genai.FunctionCallingConfigModeNone
	default:
		return nil
	}
	return &genai.ToolConfig{
		FunctionCallingConfig: &genai.FunctionCallingConfig{Mode: mode},
	}
}

// thinkingConfig maps a normalized reasoning effort to a Gemini thinking level.
// An empty effort returns nil so the model default applies.
func thinkingConfig(effort string) *genai.ThinkingConfig {
	var level genai.ThinkingLevel
	switch effort {
	case "minimal":
		level = genai.ThinkingLevelMinimal
	case "low":
		level = genai.ThinkingLevelLow
	case "medium":
		level = genai.ThinkingLevelMedium
	case "high":
		level = genai.ThinkingLevelHigh
	case "xhigh":
		// The pinned genai SDK predates the XHIGH level Gemini 4 Neon and
		// Hydrogen accept.
		level = genai.ThinkingLevel("XHIGH")
	default:
		return nil
	}
	return &genai.ThinkingConfig{
		IncludeThoughts: true,
		ThinkingLevel:   level,
	}
}

// applyResponseFormat maps a JSON-schema ResponseFormat onto the Gemini
// structured-output config. The request-level format takes precedence over
// the client config default.
func applyResponseFormat(cfg *genai.GenerateContentConfig, reqFmt, cfgFmt *llmapi.ResponseFormat) {
	format := cfgFmt
	if reqFmt != nil {
		format = reqFmt
	}
	if format == nil || format.Type != llmapi.ResponseFormatTypeJSONSchema || format.JSONSchema == nil {
		return
	}
	schema, err := format.JSONSchema.Schema.MarshalJSON()
	if err != nil {
		return
	}
	m := unmarshalSchema(schema)
	if m == nil {
		return
	}
	sanitizeSchema(m)
	cfg.ResponseMIMEType = "application/json"
	cfg.ResponseJsonSchema = m
}

// usageFromMetadata maps Gemini usage metadata onto llmapi.Usage. Gemini
// reports the cached token count as part of the prompt count, matching the
// TokensSent semantics used by the other providers.
func usageFromMetadata(m *genai.GenerateContentResponseUsageMetadata) llmapi.Usage {
	if m == nil {
		return llmapi.Usage{}
	}
	return llmapi.Usage{
		TokensSent:     int(m.PromptTokenCount),
		TokensReceived: int(m.CandidatesTokenCount),
		TokensReasoned: int(m.ThoughtsTokenCount),
		TokensCached:   int(m.CachedContentTokenCount),
	}
}

// mapFinishReason converts a Gemini finish reason to llmapi.FinishReason.
func mapFinishReason(reason genai.FinishReason, hasToolCalls bool) llmapi.FinishReason {
	switch reason {
	case genai.FinishReasonStop:
		if hasToolCalls {
			return llmapi.FinishReasonToolCall
		}
		return llmapi.FinishReasonStop
	case genai.FinishReasonMaxTokens:
		return llmapi.FinishReasonLength
	case genai.FinishReasonSafety, genai.FinishReasonProhibitedContent,
		genai.FinishReasonBlocklist, genai.FinishReasonSPII, genai.FinishReasonRecitation:
		return llmapi.FinishReasonContentFilter
	case "":
		return llmapi.FinishReasonNull
	default:
		return llmapi.FinishReasonStop
	}
}

// mapError unwraps a genai.APIError so the structured Code/Message/Status text
// is preserved instead of being dropped behind an opaque transport failure.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		return fmt.Errorf("gemini API error %d (%s): %s",
			apiErr.Code, apiErr.Status, apiErr.Message)
	}
	return err
}

// modelEntryFromGenAI converts a genai.Model into an llmapi.ModelEntry, or
// reports false when the model is not a chat-completion model. Gemini lists
// embedding, image, and legacy models the agent cannot drive through
// CreateCompletion, so callers filter to generateContent-capable entries.
func modelEntryFromGenAI(m *genai.Model) (llmapi.ModelEntry, bool) {
	if m == nil || !supportsGenerateContent(m) {
		return llmapi.ModelEntry{}, false
	}
	// The API returns resource names such as "models/gemini-2.5-flash"; the
	// rest of the system keys models by their bare identifier.
	name := strings.TrimPrefix(m.Name, "models/")
	if name == "" {
		return llmapi.ModelEntry{}, false
	}
	return llmapi.ModelEntry{
		Name:          name,
		Provider:      LLMProvider,
		ContextWindow: int(m.InputTokenLimit),
	}, true
}

// supportsGenerateContent reports whether the model advertises the streaming
// chat-completion action. Older catalog entries omit SupportedActions; treat
// those as usable so the catalog is not silently truncated.
func supportsGenerateContent(m *genai.Model) bool {
	if len(m.SupportedActions) == 0 {
		return true
	}
	return slices.Contains(m.SupportedActions, "generateContent")
}
