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
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

// Reasoning block kinds for llmapi.ReasoningBlock, mapping Bedrock's
// reasoningText and redactedContent blocks. These values are persisted in
// dialogue history, so changing them breaks replay of saved reasoning.
const (
	reasoningKindThinking = "thinking"
	reasoningKindRedacted = "redacted"
)

// emptyUserContentSentinel stands in for a user message that has no
// renderable content. Converse rejects both empty text blocks and a
// conversation whose final message is not from the user, so the turn is kept
// with this placeholder.
const emptyUserContentSentinel = "(no content)"

// converseInput converts an llmapi.Request into ConverseStream parameters.
// The reasoning effort on request must already be normalized by the caller.
func converseInput(
	model string, request llmapi.Request, config Config,
) *bedrockruntime.ConverseStreamInput {
	system, messages := convertMessages(request.Messages)
	toolConfig := toolConfiguration(request.Tools, request.ToolChoice)

	cacheEnabled := config.CacheControl != "" && config.CacheControl != "none"
	if cacheEnabled {
		system = appendSystemCachePoint(system)
		toolConfig = appendToolCachePoint(toolConfig)
		messages = markConversationPrefix(messages)
	}

	in := &bedrockruntime.ConverseStreamInput{
		ModelId:         aws.String(model),
		Messages:        messages,
		System:          system,
		ToolConfig:      toolConfig,
		InferenceConfig: inferenceConfig(model, request, config),
	}

	if fields := additionalRequestFields(model, string(request.ReasoningEffort)); fields != nil {
		in.AdditionalModelRequestFields = fields
	}
	format := config.ResponseFormat
	if request.ResponseFormat != nil {
		format = request.ResponseFormat
	}
	if oc := outputConfig(format); oc != nil {
		in.OutputConfig = oc
	}
	return in
}

// outputConfig maps a JSON-schema response format onto Converse structured
// output.
func outputConfig(format *llmapi.ResponseFormat) *types.OutputConfig {
	if format == nil || format.Type != llmapi.ResponseFormatTypeJSONSchema ||
		format.JSONSchema == nil || format.JSONSchema.Schema == nil {
		return nil
	}
	schema, err := format.JSONSchema.Schema.MarshalJSON()
	if err != nil {
		slog.Warn("bedrock: dropping unmarshalable response format schema", "error", err)
		return nil
	}
	def := types.JsonSchemaDefinition{Schema: aws.String(string(schema))}
	if format.JSONSchema.Name != "" {
		def.Name = aws.String(format.JSONSchema.Name)
	}
	if format.JSONSchema.Description != "" {
		def.Description = aws.String(format.JSONSchema.Description)
	}
	return &types.OutputConfig{
		TextFormat: &types.OutputFormat{
			Type:      types.OutputFormatTypeJsonSchema,
			Structure: &types.OutputFormatStructureMemberJsonSchema{Value: def},
		},
	}
}

// inferenceConfig maps the caller's output limits and sampling knobs onto
// the base inference parameters every Converse model understands.
func inferenceConfig(
	model string, request llmapi.Request, config Config,
) *types.InferenceConfiguration {
	cfg := &types.InferenceConfiguration{}
	switch {
	case request.MaxOutputTokens > 0:
		cfg.MaxTokens = aws.Int32(int32(request.MaxOutputTokens))
	case config.MaxTokens > 0:
		cfg.MaxTokens = aws.Int32(int32(config.MaxTokens))
	case MaxOutputTokens(model) > 0:
		cfg.MaxTokens = aws.Int32(int32(MaxOutputTokens(model)))
	}
	if config.Temperature != 0 {
		cfg.Temperature = aws.Float32(float32(config.Temperature))
	}
	if config.TopP != 0 {
		cfg.TopP = aws.Float32(float32(config.TopP))
	}
	return cfg
}

// additionalRequestFields renders the model-family-specific request
// extensions. Only Claude thinking is mapped today: adaptive thinking with an
// effort level on Claude 4.6 and later, and otherwise a fixed budget, which
// must stay below the output-token ceiling or Bedrock rejects the request.
func additionalRequestFields(model, effort string) document.Interface {
	if effort == "" || !SupportsEffort(model) {
		return nil
	}
	if SupportsAdaptiveThinking(model) {
		return document.NewLazyDocument(map[string]any{
			"thinking":      map[string]any{"type": "adaptive"},
			"output_config": map[string]any{"effort": effort},
		})
	}
	budget, ok := thinkingBudget[effort]
	if !ok {
		return nil
	}
	return document.NewLazyDocument(map[string]any{
		"thinking": map[string]any{
			"type":          "enabled",
			"budget_tokens": budget,
		},
	})
}

// convertMessages separates system messages and converts the rest into
// Converse messages. Tool results become user-role messages and consecutive
// same-role messages are merged, because Converse requires strict
// user/assistant alternation.
func convertMessages(msgs []llmapi.Message) ([]types.SystemContentBlock, []types.Message) {
	var system []types.SystemContentBlock
	var raw []types.Message

	for _, msg := range msgs {
		switch msg.Role {
		case llmapi.RoleSystem:
			if strings.TrimSpace(msg.Content) == "" {
				continue
			}
			system = append(system, &types.SystemContentBlockMemberText{Value: msg.Content})

		case llmapi.RoleUser:
			raw = append(raw, types.Message{
				Role:    types.ConversationRoleUser,
				Content: userContentBlocks(msg),
			})

		case llmapi.RoleAssistant:
			blocks := assistantContentBlocks(msg)
			if len(blocks) == 0 {
				continue
			}
			raw = append(raw, types.Message{
				Role:    types.ConversationRoleAssistant,
				Content: blocks,
			})

		case llmapi.RoleTool:
			raw = append(raw, types.Message{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberToolResult{
						Value: types.ToolResultBlock{
							ToolUseId: aws.String(msg.ToolCallID),
							Content: []types.ToolResultContentBlock{
								&types.ToolResultContentBlockMemberText{Value: msg.Content},
							},
						},
					},
				},
			})
		}
	}

	return system, mergeConsecutiveRoles(raw)
}

// userContentBlocks converts a user-role message into Converse content
// blocks.
func userContentBlocks(msg llmapi.Message) []types.ContentBlock {
	if len(msg.MultiContent) > 0 {
		blocks := make([]types.ContentBlock, 0, len(msg.MultiContent))
		for _, p := range msg.MultiContent {
			switch p.Type {
			case llmapi.ContentPartTypeImageURL:
				if block, ok := imageBlockFromURL(p.ImageURL); ok {
					blocks = append(blocks, block)
				}
			default:
				// Converse rejects empty text blocks, so an empty part
				// alongside an image must be dropped rather than sent.
				if strings.TrimSpace(p.Text) == "" {
					continue
				}
				blocks = append(blocks, &types.ContentBlockMemberText{Value: p.Text})
			}
		}
		if len(blocks) > 0 {
			return blocks
		}
		return []types.ContentBlock{
			&types.ContentBlockMemberText{Value: emptyUserContentSentinel},
		}
	}
	if strings.TrimSpace(msg.Content) == "" {
		return []types.ContentBlock{
			&types.ContentBlockMemberText{Value: emptyUserContentSentinel},
		}
	}
	return []types.ContentBlock{&types.ContentBlockMemberText{Value: msg.Content}}
}

// imageBlockFromURL builds an image content block from a data URI. Converse
// accepts only inline bytes or an S3 location, so remote URLs are dropped.
func imageBlockFromURL(url string) (types.ContentBlock, bool) {
	if !strings.HasPrefix(url, "data:") {
		slog.Warn("bedrock: dropping image with unsupported source", "url_scheme", schemeOf(url))
		return nil, false
	}
	mediaType, encoded := parseDataURI(url)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		slog.Warn("bedrock: dropping image with undecodable data URI", "error", err)
		return nil, false
	}
	return &types.ContentBlockMemberImage{
		Value: types.ImageBlock{
			Format: imageFormat(mediaType),
			Source: &types.ImageSourceMemberBytes{Value: raw},
		},
	}, true
}

func schemeOf(url string) string {
	if i := strings.Index(url, ":"); i > 0 {
		return url[:i]
	}
	return ""
}

// parseDataURI extracts the media type and base64 payload from a data URI.
func parseDataURI(uri string) (mediaType, data string) {
	uri = strings.TrimPrefix(uri, "data:")
	parts := strings.SplitN(uri, ";base64,", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "image/png", uri
}

// imageFormat maps an image media type onto the Converse image format enum,
// defaulting to PNG for unrecognised types.
func imageFormat(mediaType string) types.ImageFormat {
	switch strings.TrimPrefix(mediaType, "image/") {
	case "jpeg", "jpg":
		return types.ImageFormatJpeg
	case "gif":
		return types.ImageFormatGif
	case "webp":
		return types.ImageFormatWebp
	default:
		return types.ImageFormatPng
	}
}

// assistantContentBlocks converts an assistant-role message into content
// blocks. Reasoning is replayed first because Claude requires the assistant
// turn preceding a tool result to begin with its reasoning block.
func assistantContentBlocks(msg llmapi.Message) []types.ContentBlock {
	var blocks []types.ContentBlock
	for _, rb := range msg.ReasoningBlocks {
		switch rb.Kind {
		case reasoningKindThinking:
			// Converse rejects reasoning text whose signature does not match,
			// so unsigned reasoning (e.g. legacy persisted messages) is skipped.
			if rb.Signature == "" {
				continue
			}
			blocks = append(blocks, &types.ContentBlockMemberReasoningContent{
				Value: &types.ReasoningContentBlockMemberReasoningText{
					Value: types.ReasoningTextBlock{
						Text:      aws.String(rb.Text),
						Signature: aws.String(rb.Signature),
					},
				},
			})
		case reasoningKindRedacted:
			if rb.Data == "" {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(rb.Data)
			if err != nil {
				continue
			}
			blocks = append(blocks, &types.ContentBlockMemberReasoningContent{
				Value: &types.ReasoningContentBlockMemberRedactedContent{Value: raw},
			})
		}
	}
	if strings.TrimSpace(msg.Content) != "" {
		blocks = append(blocks, &types.ContentBlockMemberText{Value: msg.Content})
	}
	for _, tc := range msg.ToolCalls {
		blocks = append(blocks, &types.ContentBlockMemberToolUse{
			Value: types.ToolUseBlock{
				ToolUseId: aws.String(tc.ID),
				Name:      aws.String(tc.Function.Name),
				Input:     documentFromJSON(tc.Function.Arguments),
			},
		})
	}
	return blocks
}

// documentFromJSON turns serialized tool-call arguments into a document
// value. Unparseable arguments degrade to an empty object rather than
// failing the whole turn.
func documentFromJSON(args string) document.Interface {
	if strings.TrimSpace(args) == "" {
		return document.NewLazyDocument(map[string]any{})
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		slog.Warn("bedrock: tool call arguments are not a JSON object", "error", err)
		return document.NewLazyDocument(map[string]any{})
	}
	return document.NewLazyDocument(m)
}

// mergeConsecutiveRoles merges consecutive messages with the same role.
func mergeConsecutiveRoles(msgs []types.Message) []types.Message {
	if len(msgs) <= 1 {
		return msgs
	}
	var merged []types.Message
	for _, msg := range msgs {
		if len(merged) > 0 && merged[len(merged)-1].Role == msg.Role {
			last := &merged[len(merged)-1]
			last.Content = append(last.Content, msg.Content...)
			continue
		}
		merged = append(merged, msg)
	}
	return merged
}

// toolConfiguration converts llmapi tools and routing preference into the
// Converse toolConfig.
func toolConfiguration(tools []llmapi.Tool, choice llmapi.ToolChoice) *types.ToolConfiguration {
	if len(tools) == 0 {
		return nil
	}
	out := make([]types.Tool, 0, len(tools))
	for _, tool := range tools {
		spec := types.ToolSpecification{
			Name:        aws.String(tool.Function.Name),
			InputSchema: &types.ToolInputSchemaMemberJson{Value: toolInputSchema(tool.Function.Parameters)},
		}
		if tool.Function.Description != "" {
			spec.Description = aws.String(tool.Function.Description)
		}
		out = append(out, &types.ToolMemberToolSpec{Value: spec})
	}
	cfg := &types.ToolConfiguration{Tools: out}
	switch choice {
	case llmapi.ToolChoiceAuto:
		cfg.ToolChoice = &types.ToolChoiceMemberAuto{}
	case llmapi.ToolChoiceRequired:
		cfg.ToolChoice = &types.ToolChoiceMemberAny{}
	}
	return cfg
}

// toolInputSchema normalizes a tool's parameter schema into a document
// value. Converse requires an object schema, so a missing or unparseable
// schema degrades to an empty object schema.
func toolInputSchema(params any) document.Interface {
	empty := map[string]any{"type": "object", "properties": map[string]any{}}
	switch p := params.(type) {
	case nil:
		return document.NewLazyDocument(empty)
	case map[string]any:
		return document.NewLazyDocument(p)
	case json.RawMessage:
		return unmarshalSchema(p, empty)
	case []byte:
		return unmarshalSchema(p, empty)
	default:
		b, err := json.Marshal(p)
		if err != nil {
			return document.NewLazyDocument(empty)
		}
		return unmarshalSchema(b, empty)
	}
}

func unmarshalSchema(raw []byte, fallback map[string]any) document.Interface {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return document.NewLazyDocument(fallback)
	}
	return document.NewLazyDocument(m)
}

// cachePoint returns the cache breakpoint marker inserted into system,
// tool, and message content.
func cachePoint() types.CachePointBlock {
	return types.CachePointBlock{Type: types.CachePointTypeDefault}
}

// appendSystemCachePoint caches the whole system prompt as one prefix.
func appendSystemCachePoint(system []types.SystemContentBlock) []types.SystemContentBlock {
	if len(system) == 0 {
		return system
	}
	return append(system, &types.SystemContentBlockMemberCachePoint{Value: cachePoint()})
}

// appendToolCachePoint caches every tool schema as one prefix.
func appendToolCachePoint(cfg *types.ToolConfiguration) *types.ToolConfiguration {
	if cfg == nil || len(cfg.Tools) == 0 {
		return cfg
	}
	cfg.Tools = append(cfg.Tools, &types.ToolMemberCachePoint{Value: cachePoint()})
	return cfg
}

// markConversationPrefix caches the conversation history up to the
// second-to-last message. Each turn appends to the end, so everything before
// the final message is a stable prefix.
func markConversationPrefix(msgs []types.Message) []types.Message {
	if len(msgs) < 2 {
		return msgs
	}
	turn := &msgs[len(msgs)-2]
	turn.Content = append(turn.Content, &types.ContentBlockMemberCachePoint{Value: cachePoint()})
	return msgs
}
