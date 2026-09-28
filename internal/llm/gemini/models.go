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
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

// LLMProvider identifies the Gemini provider in the model registry.
const LLMProvider = "gemini"

const (
	// Gemini_3_6_Flash is the current Gemini 3.6 Flash model.
	Gemini_3_6_Flash = "gemini-3.6-flash"
	// Gemini_3_5_FlashLite is the current Gemini 3.5 Flash-Lite model.
	Gemini_3_5_FlashLite = "gemini-3.5-flash-lite"
	// Gemini_3_1_Pro_Preview is the Gemini 3.1 Pro preview model.
	Gemini_3_1_Pro_Preview = "gemini-3.1-pro-preview"
	// Gemini_3_Flash_Preview is the Gemini 3 Flash preview model.
	Gemini_3_Flash_Preview = "gemini-3-flash-preview"
	// Gemini_3_1_FlashLite_Preview is the Gemini 3.1 Flash Lite preview model.
	Gemini_3_1_FlashLite_Preview = "gemini-3.1-flash-lite-preview"
	// Gemini_2_5_Pro is the Gemini 2.5 Pro model.
	Gemini_2_5_Pro = "gemini-2.5-pro"
	// Gemini_2_5_Flash is the Gemini 2.5 Flash model.
	Gemini_2_5_Flash = "gemini-2.5-flash"
	// Gemini_2_5_FlashLite is the Gemini 2.5 Flash Lite model.
	Gemini_2_5_FlashLite = "gemini-2.5-flash-lite"
	// Gemini_2_0_Flash is the Gemini 2.0 Flash model.
	Gemini_2_0_Flash = "gemini-2.0-flash"
	// Gemini_2_0_FlashLite is the Gemini 2.0 Flash Lite model.
	Gemini_2_0_FlashLite = "gemini-2.0-flash-lite"
)

// VerificationModel is the model used to probe a key during
// VerifyProviderKey. Flash is chosen because it is cheap and broadly
// available across accounts.
const VerificationModel = Gemini_2_5_Flash

// AvailableModels returns a map from model identifier -> nominal maximum
// context window (tokens). This static catalog is the offline fallback
// used when the live Gemini listing cannot be queried — for example at
// bootstrap, before a valid API key has been entered, since the
// generativelanguage models endpoint rejects requests without a working
// key. Values are from provider documentation as of early 2026; always
// verify with the provider at runtime for account-specific limits.
func AvailableModels() map[string]int {
	return map[string]int{
		Gemini_3_6_Flash:             1048576,
		Gemini_3_5_FlashLite:         1048576,
		Gemini_3_1_Pro_Preview:       1048576,
		Gemini_3_Flash_Preview:       1048576,
		Gemini_3_1_FlashLite_Preview: 1048576,
		Gemini_2_5_Pro:               1048576,
		Gemini_2_5_Flash:             1048576,
		Gemini_2_5_FlashLite:         1048576,
		Gemini_2_0_Flash:             1048576,
		Gemini_2_0_FlashLite:         1048576,
	}
}

// ModelEntries returns the static catalog of Gemini models. It is the
// offline fallback for the live listing (see client.Models) and the
// bootstrap catalog shown before a key is verified.
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
func FlagshipModel() string { return Gemini_3_1_Pro_Preview }

// maxOutputTokens maps each model to its documented maximum output-token
// (API max_output_tokens) ceiling. Models absent from the map have an
// unknown ceiling; MaxOutputTokens returns 0 for them.
var maxOutputTokens = map[string]int{
	Gemini_3_6_Flash:             65536,
	Gemini_3_5_FlashLite:         65536,
	Gemini_3_1_Pro_Preview:       65536,
	Gemini_3_Flash_Preview:       65536,
	Gemini_3_1_FlashLite_Preview: 65536,
	Gemini_2_5_Pro:               65536,
	Gemini_2_5_Flash:             65536,
	Gemini_2_5_FlashLite:         65536,
	Gemini_2_0_Flash:             8192,
	Gemini_2_0_FlashLite:         8192,
}

// MaxOutputTokens returns the model's documented maximum output-token
// ceiling, or 0 when the limit is unknown.
func MaxOutputTokens(model string) int { return maxOutputTokens[model] }

// supportedEfforts are the reasoning-effort levels that map onto a Gemini
// thinking level. "none" and "xhigh"/"max" have no Gemini equivalent.
var supportedEfforts = map[string]bool{
	"minimal": true,
	"low":     true,
	"medium":  true,
	"high":    true,
}

// defaultGemini3Effort is the thinking level applied to Gemini 3 models when
// no usable effort is requested. Gemini 3 returns (and on the next turn
// requires) a thought_signature on function-call parts, so a thinking config
// must always be sent — never omitted — to keep signatures flowing so long
// tool-calling conversations do not degrade into empty completions. Medium is
// used as the default because the minimal level is not accepted by every
// Gemini 3 model (Pro rejects it), whereas medium is broadly supported.
const defaultGemini3Effort = "medium"

// isGemini3 reports whether the model is a Gemini 3.x model, which mandates
// thought signatures during function calling.
func isGemini3(model string) bool {
	return strings.HasPrefix(model, "gemini-3")
}

// DefaultEffortFor returns the thinking level applied when a request
// omits one, or an empty string when the thinking config is omitted
// entirely and the provider decides.
func DefaultEffortFor(model string) string {
	if isGemini3(model) {
		return defaultGemini3Effort
	}
	return ""
}

// NormalizeEffort validates the requested effort against Gemini's thinking
// levels. It returns the effort to use (empty means omit the thinking config)
// and a human-readable warning when the requested effort is unsupported.
func NormalizeEffort(model, effort string) (normalized, warning string) {
	if effort == "" || effort == "none" {
		if isGemini3(model) {
			return defaultGemini3Effort, ""
		}
		return "", ""
	}
	if supportedEfforts[effort] {
		return effort, ""
	}
	// Unsupported effort: warn, then fall back. Gemini 3 still needs a thinking
	// config, so it falls back to the default rather than omitting it.
	warning = fmt.Sprintf(
		"Effort %q is not supported by %s; using model default instead.", effort, model)
	if isGemini3(model) {
		return defaultGemini3Effort, warning
	}
	return "", warning
}
