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
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

// LLMProvider identifies the OpenAI provider in the model registry.
const LLMProvider = "openai"

const (
	// GPT6Astra is the GPT-6 Astra model.
	GPT6Astra = "gpt-6-astra"
	// GPT6Sol is the GPT-6 Sol model.
	GPT6Sol = "gpt-6-sol"
	// GPT6Luna is the GPT-6 Luna model.
	GPT6Luna = "gpt-6-luna"
	// GPT5Dot6 is the GPT-5.6 Sol alias.
	GPT5Dot6 = "gpt-5.6"
	// GPT5Dot6Sol is the GPT-5.6 Sol model.
	GPT5Dot6Sol = "gpt-5.6-sol"
	// GPT5Dot6Terra is the GPT-5.6 Terra model.
	GPT5Dot6Terra = "gpt-5.6-terra"
	// GPT5Dot6Luna is the GPT-5.6 Luna model.
	GPT5Dot6Luna = "gpt-5.6-luna"
	// GPT5Dot5 is the GPT-5.5 model.
	GPT5Dot5 = "gpt-5.5"
	// GPT5Dot4 is the GPT-5.4 model.
	GPT5Dot4 = "gpt-5.4"
	// GPT5Dot4Pro is the GPT-5.4 Pro model.
	GPT5Dot4Pro = "gpt-5.4-pro"
	// GPT5Dot4Mini is the GPT-5.4 Mini model.
	GPT5Dot4Mini = "gpt-5.4-mini"
	// GPT5Dot4Nano is the GPT-5.4 Nano model.
	GPT5Dot4Nano = "gpt-5.4-nano"
	// GPT5Dot3Codex is the GPT-5.3 Codex model.
	GPT5Dot3Codex = "gpt-5.3-codex"
	// GPT5Dot3Instant is the GPT-5.3 Instant model.
	GPT5Dot3Instant = "gpt-5.3-instant"
	// GPT5Dot2 is the GPT-5.2 model.
	GPT5Dot2 = "gpt-5.2"
	// GPT5Dot2Chat is the GPT-5.2 chat-latest model.
	GPT5Dot2Chat = "gpt-5.2-chat-latest"
	// GPT5Dot2Pro is the GPT-5.2 Pro model.
	GPT5Dot2Pro = "gpt-5.2-pro"
	// GPT5Dot1 is the GPT-5.1 model.
	GPT5Dot1 = "gpt-5.1"
	// GPT5 is the base GPT-5 model.
	GPT5 = "gpt-5"
	// GPT5Mini is the GPT-5 Mini model.
	GPT5Mini = "gpt-5-mini"
	// GPT5Nano is the GPT-5 Nano model.
	GPT5Nano = "gpt-5-nano"
	// GPT4Dot1 is the GPT-4.1 model.
	GPT4Dot1 = "gpt-4.1"
	// GPT4Dot1Mini is the GPT-4.1 Mini model.
	GPT4Dot1Mini = "gpt-4.1-mini"
	// GPT4Dot1Nano is the GPT-4.1 Nano model.
	GPT4Dot1Nano = "gpt-4.1-nano"
	// GPT4o is the GPT-4o model.
	GPT4o = "gpt-4o"
	// GPT4Turbo is the GPT-4 Turbo model.
	GPT4Turbo = "gpt-4-turbo"
	// GPT4 is the GPT-4 model.
	GPT4 = "gpt-4"
	// GPT3Dot5Turbo is the GPT-3.5 Turbo model.
	GPT3Dot5Turbo = "gpt-3.5-turbo"
	// GPT3Dot5Turbo16K is the GPT-3.5 Turbo 16K model.
	GPT3Dot5Turbo16K = "gpt-3.5-turbo-16k"
	// O1 is the o1 reasoning model.
	O1 = "o1"
	// O1Mini is the o1-mini reasoning model.
	O1Mini = "o1-mini"
	// O3 is the o3 reasoning model.
	O3 = "o3"
	// O3Mini is the o3-mini reasoning model.
	O3Mini = "o3-mini"
	// O3Pro is the o3-pro reasoning model.
	O3Pro = "o3-pro"
	// O4Mini is the o4-mini reasoning model.
	O4Mini = "o4-mini"

	// OpenAICompatibleURL indicates use of the default OpenAI API endpoint.
	OpenAICompatibleURL = "" // indicates to underlying client to use the default openai api
)

// IsReasoningModel returns true for OpenAI o-series reasoning models
// (o1, o3, o4 prefixes). These models do not support Temperature or
// penalty parameters.
func IsReasoningModel(model string) bool {
	for _, prefix := range []string{"o1", "o3", "o4"} {
		if model == prefix || strings.HasPrefix(model, prefix+"-") {
			return true
		}
	}
	return false
}

// SupportsReasoning returns true for models that support reasoning
// parameters (ReasoningEffort, MaxCompletionTokens). This includes
// o-series models, GPT-5.x models, and GPT-6 models.
func SupportsReasoning(model string) bool {
	if IsReasoningModel(model) {
		return true
	}
	return strings.HasPrefix(model, "gpt-5") || strings.HasPrefix(model, "gpt-6")
}

// oSeriesEfforts are the effort levels supported by o-series reasoning models
// (o1, o3, o4). Per OpenAI docs these models accept low/medium/high only.
var oSeriesEfforts = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
}

// gpt5BaseEfforts covers GPT-5, GPT-5-mini, GPT-5-nano.
// Per OpenAI docs: minimal, low, medium, high.
var gpt5BaseEfforts = map[string]bool{
	"minimal": true,
	"low":     true,
	"medium":  true,
	"high":    true,
}

// gpt5Dot1Efforts covers GPT-5.1.
// Per OpenAI docs: none, low, medium, high.
var gpt5Dot1Efforts = map[string]bool{
	"none":   true,
	"low":    true,
	"medium": true,
	"high":   true,
}

// gpt5Dot2Efforts covers GPT-5.2, GPT-5.2-pro, GPT-5.2-chat-latest.
// Per OpenAI docs: none, low, medium, high, xhigh.
var gpt5Dot2Efforts = map[string]bool{
	"none":   true,
	"low":    true,
	"medium": true,
	"high":   true,
	"xhigh":  true,
}

// gpt5Dot3CodexEfforts covers GPT-5.3-codex.
// Per OpenAI docs: low, medium, high, xhigh.
var gpt5Dot3CodexEfforts = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
	"xhigh":  true,
}

// gpt5Dot4Efforts covers GPT-5.4, GPT-5.4-mini, GPT-5.4-nano.
// Per OpenAI docs: none, low, medium, high, xhigh.
var gpt5Dot4Efforts = map[string]bool{
	"none":   true,
	"low":    true,
	"medium": true,
	"high":   true,
	"xhigh":  true,
}

// gpt5Dot6Efforts covers GPT-5.6 Sol/Terra/Luna.
// Per OpenAI docs: none, low, medium, high, xhigh, max, ultra.
var gpt5Dot6Efforts = map[string]bool{
	"none":   true,
	"low":    true,
	"medium": true,
	"high":   true,
	"xhigh":  true,
	"max":    true,
	"ultra":  true,
}

// gpt6Efforts covers GPT-6 Astra and GPT-6 Sol.
var gpt6Efforts = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
	"xhigh":  true,
	"max":    true,
	"ultra":  true,
}

// gpt6LunaEfforts covers GPT-6 Luna, which stops at max: it has no
// ultra level because it does not delegate work to sub-agents.
var gpt6LunaEfforts = map[string]bool{
	"low":    true,
	"medium": true,
	"high":   true,
	"xhigh":  true,
	"max":    true,
}

// gpt5Dot4ProEfforts covers GPT-5.4-pro.
// Per OpenAI docs: medium, high, xhigh.
var gpt5Dot4ProEfforts = map[string]bool{
	"medium": true,
	"high":   true,
	"xhigh":  true,
}

// supportedEfforts returns the set of supported effort levels for a given
// GPT-5.x or GPT-6 model. Returns nil if the model is not a known GPT variant
// (caller should fall back to gpt5Dot4Efforts for unknown gpt-5 prefixes).
func supportedEfforts(model string) map[string]bool {
	switch {
	case model == GPT6Luna:
		return gpt6LunaEfforts
	case strings.HasPrefix(model, "gpt-6"):
		return gpt6Efforts
	case model == GPT5Dot4Pro:
		return gpt5Dot4ProEfforts
	case strings.HasPrefix(model, "gpt-5.6"):
		return gpt5Dot6Efforts
	case model == GPT5Dot5 || strings.HasPrefix(model, "gpt-5.4"):
		return gpt5Dot4Efforts
	case model == GPT5Dot3Codex || strings.HasPrefix(model, "gpt-5.3-codex"):
		return gpt5Dot3CodexEfforts
	case strings.HasPrefix(model, "gpt-5.3"):
		// gpt-5.3-instant and other 5.3 variants — use 5.2 effort set as
		// the closest documented peer.
		return gpt5Dot2Efforts
	case strings.HasPrefix(model, "gpt-5.2"):
		return gpt5Dot2Efforts
	case strings.HasPrefix(model, "gpt-5.1"):
		return gpt5Dot1Efforts
	case strings.HasPrefix(model, "gpt-5"):
		// gpt-5, gpt-5-mini, gpt-5-nano
		return gpt5BaseEfforts
	default:
		return nil
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

	if !SupportsReasoning(model) {
		// Model doesn't support reasoning effort at all — drop silently.
		return "", ""
	}

	var supported map[string]bool
	if IsReasoningModel(model) {
		supported = oSeriesEfforts
	} else {
		supported = supportedEfforts(model)
		if supported == nil {
			// Unknown gpt-5 variant — default to the broadest set.
			supported = gpt5Dot4Efforts
		}
	}

	if supported[effort] {
		return effort, ""
	}

	return "", fmt.Sprintf(
		"Effort %q is not supported by %s; using model default instead.", effort, model)
}

// AvailableModels returns a map from model identifier -> nominal maximum
// context window (tokens). These values are collected from provider
// documentation and public release notes as of early 2026. They are a
// convenience for client-side capacity checks — always verify with the
// provider at runtime for account- or region-specific limits.
func AvailableModels() map[string]int {
	return map[string]int{
		GPT6Astra:        1050000,
		GPT6Sol:          1050000,
		GPT6Luna:         1050000,
		GPT5Dot6:         1050000,
		GPT5Dot6Sol:      1050000,
		GPT5Dot6Terra:    1050000,
		GPT5Dot6Luna:     1050000,
		GPT5Dot5:         1050000,
		GPT5Dot4:         1050000,
		GPT5Dot4Pro:      1050000,
		GPT5Dot4Mini:     1050000,
		GPT5Dot4Nano:     1050000,
		GPT5Dot3Codex:    400000,
		GPT5Dot3Instant:  128000,
		GPT5Dot2:         400000,
		GPT5Dot2Chat:     400000,
		GPT5Dot2Pro:      400000,
		GPT5Dot1:         400000,
		GPT5:             400000,
		GPT5Mini:         400000,
		GPT5Nano:         400000,
		GPT4Dot1:         1047576,
		GPT4Dot1Mini:     1047576,
		GPT4Dot1Nano:     1047576,
		GPT4o:            128000,
		GPT4Turbo:        128000,
		GPT4:             8192,
		GPT3Dot5Turbo:    16000,
		GPT3Dot5Turbo16K: 16000,
		O1:               200000,
		O1Mini:           128000,
		O3:               200000,
		O3Mini:           200000,
		O3Pro:            200000,
		O4Mini:           200000,
	}
}

// ModelEntries returns the static catalog of OpenAI models in this
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
			BaseURL:       OpenAICompatibleURL,
		})
	}
	return out
}

// FlagshipModel returns the provider's top model identifier. It is
// deterministic, unlike iterating ModelEntries() whose order is
// map-random.
func FlagshipModel() string { return GPT6Astra }

// maxOutputTokens maps each model to its documented maximum output-token
// (API max_completion_tokens / max_tokens) ceiling. Models absent from the
// map have an unknown ceiling; MaxOutputTokens returns 0 for them.
var maxOutputTokens = map[string]int{
	GPT6Astra:        128000,
	GPT6Sol:          128000,
	GPT6Luna:         128000,
	GPT5Dot6:         128000,
	GPT5Dot6Sol:      128000,
	GPT5Dot6Terra:    128000,
	GPT5Dot6Luna:     128000,
	GPT5Dot5:         128000,
	GPT5Dot4:         128000,
	GPT5Dot4Pro:      128000,
	GPT5Dot4Mini:     128000,
	GPT5Dot4Nano:     128000,
	GPT5Dot3Codex:    128000,
	GPT5Dot3Instant:  128000,
	GPT5Dot2:         128000,
	GPT5Dot2Chat:     128000,
	GPT5Dot2Pro:      128000,
	GPT5Dot1:         128000,
	GPT5:             128000,
	GPT5Mini:         128000,
	GPT5Nano:         128000,
	O1:               100000,
	O3:               100000,
	O3Pro:            100000,
	O4Mini:           100000,
	O1Mini:           65536,
	O3Mini:           65536,
	GPT4Dot1:         32768,
	GPT4Dot1Mini:     32768,
	GPT4Dot1Nano:     32768,
	GPT4o:            16384,
	GPT4Turbo:        16384,
	GPT4:             8192,
	GPT3Dot5Turbo:    4096,
	GPT3Dot5Turbo16K: 4096,
}

// MaxOutputTokens returns the model's documented maximum output-token
// ceiling, or 0 when the limit is unknown.
func MaxOutputTokens(model string) int { return maxOutputTokens[model] }
