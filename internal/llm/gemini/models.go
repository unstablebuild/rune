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
	// Gemini_4_Carbon is the Gemini 4 Carbon model, Google's strongest
	// reasoning model.
	Gemini_4_Carbon = "gemini-4-carbon"
	// Gemini_4_Neon is the Gemini 4 Neon model.
	Gemini_4_Neon = "gemini-4-neon"
	// Gemini_4_Hydrogen is the Gemini 4 Hydrogen model.
	Gemini_4_Hydrogen = "gemini-4-hydrogen"
	// Gemini_4_Argon is the Gemini 4 Argon model.
	Gemini_4_Argon = "gemini-4-argon"
	// Gemini_3_8_Flash is the Gemini 3.8 Flash model.
	Gemini_3_8_Flash = "gemini-3.8-flash"
	// Gemini_3_6_Flash is the current Gemini 3.6 Flash model.
	Gemini_3_6_Flash = "gemini-3.6-flash"
	// Gemini_3_5_FlashLite is the current Gemini 3.5 Flash-Lite model.
	Gemini_3_5_FlashLite = "gemini-3.5-flash-lite"
	// Gemini_3_1_FlashLite is the Gemini 3.1 Flash-Lite model.
	Gemini_3_1_FlashLite = "gemini-3.1-flash-lite"
	// Gemini_3_1_Pro_Preview is the Gemini 3.1 Pro preview model.
	Gemini_3_1_Pro_Preview = "gemini-3.1-pro-preview"
	// Gemini_3_Flash_Preview is the Gemini 3 Flash preview model.
	Gemini_3_Flash_Preview = "gemini-3-flash-preview"
	// Gemini_2_5_FlashLite is the Gemini 2.5 Flash Lite model.
	Gemini_2_5_FlashLite = "gemini-2.5-flash-lite"
)

// VerificationModel is the model used to probe a key during
// VerifyProviderKey. Flash-Lite is chosen because it is cheap and open to
// new accounts, unlike the 2.5 models Google restricts to existing users.
const VerificationModel = Gemini_3_5_FlashLite

// AvailableModels returns a map from model identifier -> nominal maximum
// context window (tokens). This static catalog is the offline fallback
// used when the live Gemini listing cannot be queried — for example at
// bootstrap, before a valid API key has been entered, since the
// generativelanguage models endpoint rejects requests without a working
// key. Values are from provider documentation as of early 2026; always
// verify with the provider at runtime for account-specific limits.
func AvailableModels() map[string]int {
	return map[string]int{
		Gemini_4_Carbon:        1048576,
		Gemini_4_Neon:          1048576,
		Gemini_4_Hydrogen:      1048576,
		Gemini_4_Argon:         1048576,
		Gemini_3_8_Flash:       1048576,
		Gemini_3_6_Flash:       1048576,
		Gemini_3_5_FlashLite:   1048576,
		Gemini_3_1_FlashLite:   1048576,
		Gemini_3_1_Pro_Preview: 1048576,
		Gemini_3_Flash_Preview: 1048576,
		Gemini_2_5_FlashLite:   1048576,
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
func FlagshipModel() string { return Gemini_4_Carbon }

// maxOutputTokens maps each model to its documented maximum output-token
// (API max_output_tokens) ceiling. Models absent from the map have an
// unknown ceiling; MaxOutputTokens returns 0 for them. Gemini 4 can decode
// up to 1M tokens only in background requests, which this client does not
// make, so its synchronous ceiling is listed.
var maxOutputTokens = map[string]int{
	Gemini_4_Carbon:        65536,
	Gemini_4_Neon:          65536,
	Gemini_4_Hydrogen:      65536,
	Gemini_4_Argon:         65536,
	Gemini_3_8_Flash:       65536,
	Gemini_3_6_Flash:       65536,
	Gemini_3_5_FlashLite:   65536,
	Gemini_3_1_FlashLite:   65536,
	Gemini_3_1_Pro_Preview: 65536,
	Gemini_3_Flash_Preview: 65536,
	Gemini_2_5_FlashLite:   65536,
}

// MaxOutputTokens returns the model's documented maximum output-token
// ceiling, or 0 when the limit is unknown.
func MaxOutputTokens(model string) int { return maxOutputTokens[model] }

// Reasoning-effort levels that map onto a Gemini thinking level, per model.
// "none" and "max" have no Gemini equivalent.
var (
	baseEfforts = map[string]bool{
		"low":    true,
		"medium": true,
		"high":   true,
	}
	minimalEfforts = map[string]bool{
		"minimal": true,
		"low":     true,
		"medium":  true,
		"high":    true,
	}
	xhighEfforts = map[string]bool{
		"low":    true,
		"medium": true,
		"high":   true,
		"xhigh":  true,
	}
)

// supportedEffortsFor returns the thinking levels the model accepts.
// Models outside the static catalog, such as ones only the live listing
// reports, accept the levels most Flash models document.
func supportedEffortsFor(model string) map[string]bool {
	switch model {
	case Gemini_4_Neon, Gemini_4_Hydrogen:
		return xhighEfforts
	case Gemini_4_Carbon, Gemini_4_Argon, Gemini_3_8_Flash, Gemini_3_1_Pro_Preview,
		Gemini_2_5_FlashLite:
		return baseEfforts
	default:
		return minimalEfforts
	}
}

// defaultThinkingEffort is the thinking level applied, when no usable effort
// is requested, to models that require a thinking config. Medium is used
// because the minimal level is not accepted by every such model (Pro and
// Gemini 4 reject it), whereas medium is broadly supported.
const defaultThinkingEffort = "medium"

// requiresThinkingConfig reports whether the model is a Gemini 3 or later
// model. These return (and on the next turn require) a thought_signature on
// function-call parts, so a thinking config must always be sent — never
// omitted — to keep signatures flowing so long tool-calling conversations do
// not degrade into empty completions.
func requiresThinkingConfig(model string) bool {
	return strings.HasPrefix(model, "gemini-3") || strings.HasPrefix(model, "gemini-4")
}

// DefaultEffortFor returns the thinking level applied when a request
// omits one, or an empty string when the thinking config is omitted
// entirely and the provider decides. Gemini 4 Neon and Hydrogen get high,
// the level Google documents as their default.
func DefaultEffortFor(model string) string {
	switch {
	case model == Gemini_4_Neon, model == Gemini_4_Hydrogen:
		return "high"
	case requiresThinkingConfig(model):
		return defaultThinkingEffort
	default:
		return ""
	}
}

// NormalizeEffort validates the requested effort against Gemini's thinking
// levels. It returns the effort to use (empty means omit the thinking config)
// and a human-readable warning when the requested effort is unsupported.
func NormalizeEffort(model, effort string) (normalized, warning string) {
	if effort == "" || effort == "none" {
		return DefaultEffortFor(model), ""
	}
	if supportedEffortsFor(model)[effort] {
		return effort, ""
	}
	// Unsupported effort: warn, then fall back. Gemini 3 and later still need
	// a thinking config, so they fall back to the default rather than
	// omitting it.
	warning = fmt.Sprintf(
		"Effort %q is not supported by %s; using model default instead.", effort, model)
	return DefaultEffortFor(model), warning
}
