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
	"encoding/json"
	"strings"

	ant "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

// Reasoning block kinds for llmapi.ReasoningBlock, mapping Anthropic's
// thinking and redacted_thinking content blocks. These values are persisted
// in dialogue history, so changing them breaks replay of saved reasoning.
const (
	reasoningKindThinking = "thinking"
	reasoningKindRedacted = "redacted"
)

// emptyUserContentSentinel stands in for a user message that has no renderable
// content (no text, no image). The API rejects both empty text blocks and a
// trailing non-user message, so the turn is kept with this placeholder.
const emptyUserContentSentinel = "(no content)"

// anthropicParamsFromRequest converts an llmapi.Request into Anthropic API parameters.
// System messages are extracted into the separate System field, and the remaining
// messages are converted with strict user/assistant alternation.
func anthropicParamsFromRequest(model string, request llmapi.Request, config Config) ant.MessageNewParams {
	system, messages := convertMessages(request.Messages)

	tools := anthropicToolsFromModel(request.Tools)

	params := ant.MessageNewParams{
		Model:    ant.Model(model),
		Messages: messages,
		System:   system,
		Tools:    tools,
	}

	// Claude Code request shaping: lead the system array with the billing
	// header, agent identifier, and static system prompt so the Claude
	// subscription backend accepts the request. The billing-header cch is
	// signed later by claudeCodeMiddleware over the serialized body.
	if config.ClaudeCodeSpoof {
		params.System = append(claudeCodeSystemBlocks(system), system...)
	}

	switch {
	case request.MaxOutputTokens > 0:
		params.MaxTokens = int64(request.MaxOutputTokens)
	case config.MaxTokens > 0:
		params.MaxTokens = int64(config.MaxTokens)
	case MaxOutputTokens(model) > 0:
		// Streaming requests are billed on generated tokens, not the
		// requested budget, so the ceiling costs nothing extra and keeps
		// high-effort thinking from truncating visible output.
		params.MaxTokens = int64(MaxOutputTokens(model))
	case config.EnableThinking:
		// Adaptive thinking shares the max_tokens budget between thinking
		// and text output. With high/max effort Opus 4.6 can easily
		// exhaust a small budget on thinking alone, leaving no tokens for
		// visible content. Use a generous default so the model has room
		// for both.
		params.MaxTokens = 32768
	default:
		params.MaxTokens = 8192
	}

	if config.Temperature != 0 {
		params.Temperature = param.NewOpt(config.Temperature)
	}
	if config.TopP != 0 {
		params.TopP = param.NewOpt(config.TopP)
	}

	// Adaptive thinking: Claude decides dynamically when and how much to think.
	// budget_tokens is deprecated on 4.6 models; adaptive is the recommended mode.
	if config.EnableThinking {
		params.Thinking = ant.ThinkingConfigParamUnion{
			OfAdaptive: &ant.ThinkingConfigAdaptiveParam{},
		}
	}

	// Output effort — already normalized by client.CreateCompletion.
	if effort := string(request.ReasoningEffort); effort != "" {
		params.OutputConfig.Effort = ant.OutputConfigEffort(effort)
	}

	// Tool routing. Anthropic exposes a richer ToolChoice union than the
	// llmapi cross-provider literal; map "auto"/"required"/"none" onto
	// the matching variants. ParallelToolCalls maps onto
	// DisableParallelToolUse on whichever variant is selected.
	disableParallel := request.ParallelToolCalls != nil && !*request.ParallelToolCalls
	switch request.ToolChoice {
	case llmapi.ToolChoiceAuto:
		params.ToolChoice = ant.ToolChoiceUnionParam{
			OfAuto: &ant.ToolChoiceAutoParam{
				DisableParallelToolUse: optBool(disableParallel),
			},
		}
	case llmapi.ToolChoiceRequired:
		params.ToolChoice = ant.ToolChoiceUnionParam{
			OfAny: &ant.ToolChoiceAnyParam{
				DisableParallelToolUse: optBool(disableParallel),
			},
		}
	case llmapi.ToolChoiceNone:
		params.ToolChoice = ant.ToolChoiceUnionParam{
			OfNone: &ant.ToolChoiceNoneParam{},
		}
	default:
		// No explicit choice from the caller; only set DisableParallelToolUse
		// when the caller explicitly opted out and tools are present.
		if disableParallel && len(tools) > 0 {
			params.ToolChoice = ant.ToolChoiceUnionParam{
				OfAuto: &ant.ToolChoiceAutoParam{
					DisableParallelToolUse: param.NewOpt(true),
				},
			}
		}
	}

	// Structured output: map ResponseFormat to Anthropic's JSON schema format.
	format := config.ResponseFormat
	if request.ResponseFormat != nil {
		format = request.ResponseFormat
	}
	if format != nil && format.Type == llmapi.ResponseFormatTypeJSONSchema && format.JSONSchema != nil {
		schema, err := format.JSONSchema.Schema.MarshalJSON()
		if err == nil {
			var m map[string]any
			if json.Unmarshal(schema, &m) == nil {
				params.OutputConfig.Format = ant.JSONOutputFormatParam{
					Schema: m,
				}
			}
		}
	}

	return params
}

// optBool returns a param.Opt[bool] for non-zero values, or the zero
// value (omitted) when v is false. Used for disable_parallel_tool_use
// which should only be sent when explicitly disabled.
func optBool(v bool) param.Opt[bool] {
	if !v {
		return param.Opt[bool]{}
	}
	return param.NewOpt(true)
}

// convertMessages separates system messages and converts the rest into Anthropic
// message params. Tool result messages are grouped into user messages and
// consecutive same-role messages are merged.
func convertMessages(msgs []llmapi.Message) ([]ant.TextBlockParam, []ant.MessageParam) {
	var system []ant.TextBlockParam
	var raw []ant.MessageParam

	for _, msg := range msgs {
		switch msg.Role {
		case llmapi.RoleSystem:
			system = append(system, ant.TextBlockParam{Text: msg.Content})

		case llmapi.RoleUser:
			blocks := userContentBlocks(msg)
			raw = append(raw, ant.NewUserMessage(blocks...))

		case llmapi.RoleAssistant:
			blocks := assistantContentBlocks(msg)
			raw = append(raw, ant.NewAssistantMessage(blocks...))

		case llmapi.RoleTool:
			// Tool results become user-role messages with ToolResultBlock content.
			raw = append(raw, ant.NewUserMessage(
				ant.NewToolResultBlock(msg.ToolCallID, msg.Content, false),
			))
		}
	}

	// Merge consecutive same-role messages for strict alternation.
	merged := mergeConsecutiveRoles(raw)
	return system, merged
}

// userContentBlocks converts an llmapi.Message with role=user into Anthropic content blocks.
func userContentBlocks(msg llmapi.Message) []ant.ContentBlockParamUnion {
	if len(msg.MultiContent) > 0 {
		blocks := make([]ant.ContentBlockParamUnion, 0, len(msg.MultiContent))
		for _, p := range msg.MultiContent {
			switch p.Type {
			case llmapi.ContentPartTypeImageURL:
				blocks = append(blocks, imageBlockFromURL(p.ImageURL))
			default:
				// Skip empty text parts: the API rejects empty text blocks
				// with "text content blocks must be non-empty". An empty text
				// part alongside an image would otherwise still be sent.
				if strings.TrimSpace(p.Text) == "" {
					continue
				}
				blocks = append(blocks, ant.NewTextBlock(p.Text))
			}
		}
		if len(blocks) > 0 {
			return blocks
		}
		// Every part was empty: keep the turn present with a sentinel so the
		// request still ends with a non-empty user message. Dropping it would
		// trade an empty-text-block rejection for a "final message must be
		// from the user" rejection.
		return []ant.ContentBlockParamUnion{ant.NewTextBlock(emptyUserContentSentinel)}
	}
	if strings.TrimSpace(msg.Content) == "" {
		return []ant.ContentBlockParamUnion{ant.NewTextBlock(emptyUserContentSentinel)}
	}
	return []ant.ContentBlockParamUnion{ant.NewTextBlock(msg.Content)}
}

// imageBlockFromURL creates an image content block from a data URL or regular URL.
func imageBlockFromURL(url string) ant.ContentBlockParamUnion {
	// Handle data URIs: data:image/png;base64,<data>
	if strings.HasPrefix(url, "data:") {
		mediaType, data := parseDataURI(url)
		return ant.ContentBlockParamUnion{
			OfImage: &ant.ImageBlockParam{
				Source: ant.ImageBlockParamSourceUnion{
					OfBase64: &ant.Base64ImageSourceParam{
						MediaType: ant.Base64ImageSourceMediaType(mediaType),
						Data:      data,
					},
				},
			},
		}
	}
	return ant.ContentBlockParamUnion{
		OfImage: &ant.ImageBlockParam{
			Source: ant.ImageBlockParamSourceUnion{
				OfURL: &ant.URLImageSourceParam{URL: url},
			},
		},
	}
}

// parseDataURI extracts media type and base64 data from a data URI.
func parseDataURI(uri string) (mediaType, data string) {
	// Format: data:<mediatype>;base64,<data>
	uri = strings.TrimPrefix(uri, "data:")
	parts := strings.SplitN(uri, ";base64,", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "image/png", uri
}

// assistantContentBlocks converts an llmapi.Message with role=assistant into content blocks.
func assistantContentBlocks(msg llmapi.Message) []ant.ContentBlockParamUnion {
	var blocks []ant.ContentBlockParamUnion
	// Reasoning blocks must be replayed first, before any text or tool_use,
	// because Anthropic requires the assistant turn preceding a tool result to
	// begin with its thinking block, and rejects out-of-order thinking.
	for _, rb := range msg.ReasoningBlocks {
		switch rb.Kind {
		case reasoningKindThinking:
			// The API rejects thinking blocks without a byte-exact signature,
			// so skip unsigned reasoning (e.g. legacy persisted messages or
			// thinking captured before signatures were preserved).
			if rb.Signature == "" {
				continue
			}
			blocks = append(blocks, ant.NewThinkingBlock(rb.Signature, rb.Text))
		case reasoningKindRedacted:
			if rb.Data == "" {
				continue
			}
			blocks = append(blocks, ant.NewRedactedThinkingBlock(rb.Data))
		}
	}
	if msg.Content != "" {
		blocks = append(blocks, ant.NewTextBlock(msg.Content))
	}
	for _, tc := range msg.ToolCalls {
		// Pass arguments as json.RawMessage to avoid an Unmarshal→Marshal
		// round-trip. The SDK serialises the input field directly, so raw
		// JSON bytes are forwarded as-is.
		var input any = json.RawMessage("{}")
		if tc.Function.Arguments != "" {
			input = json.RawMessage(tc.Function.Arguments)
		}
		blocks = append(blocks, ant.NewToolUseBlock(tc.ID, input, tc.Function.Name))
	}
	return blocks
}

// mergeConsecutiveRoles merges consecutive messages with the same role by
// concatenating their content blocks. Anthropic requires strict user/assistant
// alternation.
func mergeConsecutiveRoles(msgs []ant.MessageParam) []ant.MessageParam {
	if len(msgs) <= 1 {
		return msgs
	}
	var merged []ant.MessageParam
	for _, msg := range msgs {
		if len(merged) > 0 && merged[len(merged)-1].Role == msg.Role {
			merged[len(merged)-1].Content = append(merged[len(merged)-1].Content, msg.Content...)
		} else {
			merged = append(merged, msg)
		}
	}
	return merged
}

// anthropicToolsFromModel converts llmapi.Tool slices to Anthropic tool params.
func anthropicToolsFromModel(tools []llmapi.Tool) []ant.ToolUnionParam {
	if len(tools) == 0 {
		return nil
	}
	ret := make([]ant.ToolUnionParam, len(tools))
	for i, tool := range tools {
		tp := &ant.ToolParam{
			Name: tool.Function.Name,
		}
		if tool.Function.Description != "" {
			tp.Description = param.NewOpt(tool.Function.Description)
		}
		tp.InputSchema = toolInputSchema(tool.Function.Parameters)
		ret[i] = ant.ToolUnionParam{OfTool: tp}
	}
	return ret
}

// toolInputSchema converts the generic Parameters value to an Anthropic
// ToolInputSchemaParam. The input may be raw JSON bytes, a map, or a struct
// that marshals to a JSON schema object.
func toolInputSchema(params any) ant.ToolInputSchemaParam {
	schema := ant.ToolInputSchemaParam{}
	switch p := params.(type) {
	case map[string]any:
		if props, ok := p["properties"]; ok {
			schema.Properties = props
		}
		if req, ok := p["required"].([]any); ok {
			for _, r := range req {
				if s, ok := r.(string); ok {
					schema.Required = append(schema.Required, s)
				}
			}
		}
	case json.RawMessage:
		var m map[string]any
		if json.Unmarshal(p, &m) == nil {
			return toolInputSchema(m)
		}
	case []byte:
		var m map[string]any
		if json.Unmarshal(p, &m) == nil {
			return toolInputSchema(m)
		}
	default:
		if p != nil {
			b, err := json.Marshal(p)
			if err == nil {
				var m map[string]any
				if json.Unmarshal(b, &m) == nil {
					return toolInputSchema(m)
				}
			}
		}
	}
	return schema
}

// applyCacheBreakpoints places explicit cache_control markers on the last
// system text block, the last tool definition, and the conversation prefix
// boundary (second-to-last message). Explicit breakpoints give precise
// control over what is cached and consistently outperform top-level
// auto-caching for multi-turn agent conversations.
//
// The cacheControl value selects the TTL: "5m" (default), "1h",
// "ephemeral" (API default), or "" to disable caching.
func applyCacheBreakpoints(params *ant.MessageNewParams, cacheControl string) {
	cc := ant.NewCacheControlEphemeralParam()
	switch cacheControl {
	case "1h":
		cc.TTL = ant.CacheControlEphemeralTTLTTL1h
	case "ephemeral":
		// Leave TTL unset
	default:
		cc.TTL = ant.CacheControlEphemeralTTLTTL5m
	}

	// Mark the last system text block — caches the entire system prompt
	// (including resource context) as a single prefix.
	if len(params.System) > 0 {
		params.System[len(params.System)-1].CacheControl = cc
	}

	// Mark the last tool definition — caches all tool schemas.
	if len(params.Tools) > 0 {
		last := &params.Tools[len(params.Tools)-1]
		if last.OfTool != nil {
			last.OfTool.CacheControl = cc
		}
	}

	// Mark the second-to-last message — caches the conversation history
	// prefix. Each new turn appends to the end, so everything before the
	// final message is stable and benefits from caching.
	if len(params.Messages) >= 2 {
		turn := &params.Messages[len(params.Messages)-2]
		if n := len(turn.Content); n > 0 {
			setCacheControlOnBlock(&turn.Content[n-1], cc)
		}
	}
}

// setCacheControlOnBlock sets cache_control on the underlying block of a
// ContentBlockParamUnion. Only the block types that appear in agent
// conversations are handled (text, tool_use, tool_result).
func setCacheControlOnBlock(block *ant.ContentBlockParamUnion, cc ant.CacheControlEphemeralParam) {
	switch {
	case block.OfText != nil:
		block.OfText.CacheControl = cc
	case block.OfToolUse != nil:
		block.OfToolUse.CacheControl = cc
	case block.OfToolResult != nil:
		block.OfToolResult.CacheControl = cc
	}
}
