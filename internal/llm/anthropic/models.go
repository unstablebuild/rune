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
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

// LLMProvider identifies the Anthropic provider in the model registry.
const LLMProvider = "anthropic"

const (
	// ClaudeFable5Dot1 is Anthropic's Claude Fable 5.1 model.
	ClaudeFable5Dot1 = "claude-fable-5-1"
	// ClaudeFable5 is Anthropic's Claude Fable 5 model — its most capable
	// widely released model.
	ClaudeFable5 = "claude-fable-5"
	// ClaudeOpus5Dot5 is Anthropic's Claude Opus 5.5 model.
	ClaudeOpus5Dot5 = "claude-opus-5-5"
	// ClaudeOpus5 is Anthropic's Claude Opus 5 model.
	ClaudeOpus5 = "claude-opus-5"
	// ClaudeSonnet5 is Anthropic's Claude Sonnet 5 model.
	ClaudeSonnet5 = "claude-sonnet-5"
	// ClaudeOpus4Dot8 is Anthropic's Claude Opus 4.8 model.
	ClaudeOpus4Dot8 = "claude-opus-4-8"
	// ClaudeOpus4Dot7 is Anthropic's Claude Opus 4.7 model.
	ClaudeOpus4Dot7 = "claude-opus-4-7"
	// ClaudeOpus4Dot6 is Anthropic's Claude Opus 4.6 model.
	ClaudeOpus4Dot6 = "claude-opus-4-6"
	// ClaudeSonnet4Dot6 is Anthropic's Claude Sonnet 4.6 model.
	ClaudeSonnet4Dot6 = "claude-sonnet-4-6"
	// ClaudeHaiku4Dot5 is Anthropic's Claude Haiku 4.5 model.
	ClaudeHaiku4Dot5 = "claude-haiku-4-5"
	// ClaudeOpus4Dot5 is Anthropic's Claude Opus 4.5 model.
	ClaudeOpus4Dot5 = "claude-opus-4-5"
	// ClaudeSonnet4Dot5 is Anthropic's Claude Sonnet 4.5 model.
	ClaudeSonnet4Dot5 = "claude-sonnet-4-5"
	// ClaudeOpus4Dot1 is Anthropic's Claude Opus 4.1 model.
	ClaudeOpus4Dot1 = "claude-opus-4-1"
	// ClaudeSonnet4 is Anthropic's Claude Sonnet 4.0 model.
	ClaudeSonnet4 = "claude-sonnet-4-0"
	// ClaudeOpus4 is Anthropic's Claude Opus 4.0 model.
	ClaudeOpus4 = "claude-opus-4-0"
	// ClaudeOpus3 is Anthropic's Claude 3 Opus model.
	ClaudeOpus3 = "claude-3-opus"
	// ClaudeSonnet3 is Anthropic's Claude 3 Sonnet model.
	ClaudeSonnet3 = "claude-3-sonnet"
	// ClaudeHaiku3 is Anthropic's Claude 3 Haiku model.
	ClaudeHaiku3 = "claude-3-haiku"
)

// AvailableModels returns a map from model identifier -> nominal maximum
// context window (tokens). These values are collected from provider
// documentation and public release notes as of early 2026. They are a
// convenience for client-side capacity checks — always verify with the
// provider at runtime for account- or region-specific limits.
func AvailableModels() map[string]int {
	return map[string]int{
		ClaudeFable5Dot1:  1000000,
		ClaudeFable5:      1000000,
		ClaudeOpus5Dot5:   1000000,
		ClaudeOpus5:       1000000,
		ClaudeSonnet5:     1000000,
		ClaudeOpus4Dot8:   1000000,
		ClaudeOpus4Dot7:   1000000,
		ClaudeOpus4Dot6:   1000000,
		ClaudeSonnet4Dot6: 1000000,
		ClaudeHaiku4Dot5:  200000,
		ClaudeOpus4Dot5:   200000,
		ClaudeSonnet4Dot5: 200000,
		ClaudeOpus4Dot1:   200000,
		ClaudeSonnet4:     200000,
		ClaudeOpus4:       200000,
		ClaudeOpus3:       200000,
		ClaudeSonnet3:     200000,
		ClaudeHaiku3:      200000,
	}
}

// SupportsAdaptiveThinking reports whether the given model supports adaptive
// extended thinking.
func SupportsAdaptiveThinking(model string) bool {
	switch model {
	case ClaudeOpus4Dot6, ClaudeSonnet4Dot6, ClaudeOpus4Dot7, ClaudeOpus4Dot8,
		ClaudeOpus5, ClaudeOpus5Dot5, ClaudeSonnet5, ClaudeFable5,
		ClaudeFable5Dot1:
		return true
	default:
		return false
	}
}

// maxOutputTokens maps each model to its documented maximum output-token
// (API max_tokens) ceiling. Models absent from the map have an unknown
// ceiling; MaxOutputTokens returns 0 for them so callers apply no cap.
var maxOutputTokens = map[string]int{
	ClaudeFable5Dot1:  128000,
	ClaudeFable5:      128000,
	ClaudeOpus5Dot5:   128000,
	ClaudeOpus5:       128000,
	ClaudeSonnet5:     128000,
	ClaudeOpus4Dot8:   128000,
	ClaudeOpus4Dot7:   128000,
	ClaudeOpus4Dot6:   128000,
	ClaudeSonnet4Dot6: 64000,
	ClaudeHaiku4Dot5:  64000,
	ClaudeSonnet4Dot5: 64000,
	ClaudeOpus4Dot5:   64000,
	ClaudeSonnet4:     64000,
	ClaudeOpus4Dot1:   32000,
	ClaudeOpus4:       32000,
	ClaudeOpus3:       4096,
	ClaudeSonnet3:     4096,
	ClaudeHaiku3:      4096,
}

// MaxOutputTokens returns the model's documented maximum output-token
// (API max_tokens) ceiling, or 0 when the limit is unknown.
func MaxOutputTokens(model string) int { return maxOutputTokens[model] }

// SupportsEffort reports whether the given model supports the
// OutputConfig.Effort parameter. Claude 4+ models support it;
// Claude 3 models do not.
func SupportsEffort(model string) bool {
	return !strings.HasPrefix(model, "claude-3")
}

// claude4Efforts are the effort levels supported by Claude 4.0–4.5 models.
var claude4Efforts = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
}

// claude46Efforts are the effort levels supported by adaptive-thinking models.
var claude46Efforts = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
	"xhigh":  true,
	"max":    true,
}

// DefaultEffortFor returns the effort level the provider applies when a
// request omits one, or an empty string when the model publishes no
// such default. Adaptive-thinking models document high, except Opus 5.5
// which documents medium; earlier models take an explicit thinking
// budget instead of an effort level.
func DefaultEffortFor(model string) string {
	switch {
	case model == ClaudeOpus5Dot5:
		return "medium"
	case SupportsAdaptiveThinking(model):
		return "high"
	default:
		return ""
	}
}

// NormalizeEffort validates the requested effort level against the given
// model's capabilities. It returns the effort to use (empty string means
// omit the parameter entirely) and a human-readable warning when the
// requested effort is not supported by the model.
func NormalizeEffort(model, effort string) (normalized string, warning string) {
	if effort == "" {
		return "", ""
	}

	if !SupportsEffort(model) {
		return "", fmt.Sprintf(
			"Effort %q is not supported by %s; using model default instead.", effort, model)
	}

	var supported map[string]bool
	if SupportsAdaptiveThinking(model) {
		supported = claude46Efforts
	} else {
		supported = claude4Efforts
	}

	if supported[effort] {
		return effort, ""
	}

	return "", fmt.Sprintf(
		"Effort %q is not supported by %s; using model default instead.", effort, model)
}

// ModelEntries returns the static catalog of Anthropic models in this
// provider, suitable for registering with an llmrouter.Router via
// llmrouter.WithModels.
func ModelEntries() []llmapi.ModelEntry {
	avail := AvailableModels()
	out := make([]llmapi.ModelEntry, 0, len(avail))
	for name, ctxWindow := range avail {
		out = append(out, llmapi.ModelEntry{
			Name:          name,
			Provider:      LLMProvider,
			ContextWindow: ctxWindow,
		})
	}
	return out
}

// FlagshipModel returns the provider's top model identifier. It is
// deterministic, unlike iterating ModelEntries() whose order is
// map-random.
func FlagshipModel() string { return ClaudeOpus5Dot5 }
