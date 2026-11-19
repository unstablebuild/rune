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

package codex

import (
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

// LLMProvider identifies the Codex provider in the model registry.
const LLMProvider = "codex"

const (
	// GPT6Dot2Astra is the GPT-6.2 Astra Codex model.
	GPT6Dot2Astra = "gpt-6.2-astra"
	// GPT6Dot2Sol is the GPT-6.2 Sol Codex model.
	GPT6Dot2Sol = "gpt-6.2-sol"
	// GPT6Astra is the GPT-6 Astra Codex model.
	GPT6Astra = "gpt-6-astra"
	// GPT6Dot1Sol is the GPT-6.1 Sol Codex model.
	GPT6Dot1Sol = "gpt-6.1-sol"
	// GPT6Sol is the GPT-6 Sol Codex model.
	GPT6Sol = "gpt-6-sol"
	// GPT6Dot1Terra is the GPT-6.1 Terra Codex model.
	GPT6Dot1Terra = "gpt-6.1-terra"
	// GPT6Luna is the GPT-6 Luna Codex model.
	GPT6Luna = "gpt-6-luna"
	// GPT5Dot6Sol is the GPT-5.6 Sol Codex model.
	GPT5Dot6Sol = "gpt-5.6-sol"
	// GPT5Dot6Terra is the GPT-5.6 Terra Codex model.
	GPT5Dot6Terra = "gpt-5.6-terra"
	// GPT5Dot6Luna is the GPT-5.6 Luna Codex model.
	GPT5Dot6Luna = "gpt-5.6-luna"
	// GPT5Dot5 is the frontier Codex model.
	GPT5Dot5 = "gpt-5.5"
	// CodexAutoReview is the Codex automatic review model.
	CodexAutoReview = "codex-auto-review"

	// OpenAICompatibleURL is the ChatGPT Codex backend OpenAI-compatible API.
	OpenAICompatibleURL = "https://chatgpt.com/backend-api/codex"
)

// AvailableModels returns Codex models exposed by the Codex backend.
func AvailableModels() map[string]int {
	// Context windows mirror max_context_window in
	// codex-rs/models-manager/models.json, which can exceed the smaller
	// default plan budget it also lists.
	return map[string]int{
		GPT6Dot2Astra:   872000,
		GPT6Dot2Sol:     872000,
		GPT6Astra:       872000,
		GPT6Dot1Sol:     872000,
		GPT6Sol:         872000,
		GPT6Dot1Terra:   872000,
		GPT6Luna:        872000,
		GPT5Dot6Sol:     872000,
		GPT5Dot6Terra:   872000,
		GPT5Dot6Luna:    872000,
		GPT5Dot5:        272000,
		CodexAutoReview: 872000,
	}
}

// ModelEntries returns the static catalog of Codex models in this
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
func FlagshipModel() string { return GPT6Dot2Astra }

// UpstreamModelName returns the Codex backend model slug for a given
// catalog name. The rune-side codex catalog stores the upstream slug
// directly (the legacy `codex/` registry prefix was only ever needed
// when codex and openai models shared a flat namespace keyed by Name);
// this helper is kept as the identity function so callers do not need
// to know whether the rune-agent transitional namespace is in use.
func UpstreamModelName(model string) string { return model }

// MaxOutputTokens returns 0 because Codex does not accept client-set output limits.
func MaxOutputTokens(string) int { return 0 }
