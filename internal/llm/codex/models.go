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
	// GPT6Astra is the GPT-6 Astra Codex model.
	GPT6Astra = "gpt-6-astra"
	// GPT6Sol is the GPT-6 Sol Codex model.
	GPT6Sol = "gpt-6-sol"
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
	// GPT5Dot4 is the strong everyday Codex coding model.
	GPT5Dot4 = "gpt-5.4"
	// GPT5Dot4Mini is the small Codex coding model.
	GPT5Dot4Mini = "gpt-5.4-mini"
	// GPT5Dot3Codex is the Codex-optimized GPT-5.3 model.
	GPT5Dot3Codex = "gpt-5.3-codex"
	// GPT5Dot2 is the older professional-work Codex model.
	GPT5Dot2 = "gpt-5.2"
	// CodexAutoReview is the Codex automatic review model.
	CodexAutoReview = "codex-auto-review"

	// OpenAICompatibleURL is the ChatGPT Codex backend OpenAI-compatible API.
	OpenAICompatibleURL = "https://chatgpt.com/backend-api/codex"
)

// AvailableModels returns Codex models exposed by the Codex backend.
func AvailableModels() map[string]int {
	// Context windows mirror codex-rs/models-manager/models.json. For
	// `gpt-5.4` and `codex-auto-review` the bundled `max_context_window`
	// is 1M (with a 272k default plan budget); the GPT-6 and GPT-5.6
	// models have an 872k upstream ceiling.
	return map[string]int{
		GPT6Astra:       872000,
		GPT6Sol:         872000,
		GPT6Luna:        872000,
		GPT5Dot6Sol:     872000,
		GPT5Dot6Terra:   872000,
		GPT5Dot6Luna:    872000,
		GPT5Dot5:        272000,
		GPT5Dot4:        1000000,
		GPT5Dot4Mini:    272000,
		GPT5Dot3Codex:   272000,
		GPT5Dot2:        272000,
		CodexAutoReview: 1000000,
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
func FlagshipModel() string { return GPT6Astra }

// UpstreamModelName returns the Codex backend model slug for a given
// catalog name. The rune-side codex catalog stores the upstream slug
// directly (the legacy `codex/` registry prefix was only ever needed
// when codex and openai models shared a flat namespace keyed by Name);
// this helper is kept as the identity function so callers do not need
// to know whether the rune-agent transitional namespace is in use.
func UpstreamModelName(model string) string { return model }

// MaxOutputTokens returns 0 because Codex does not accept client-set output limits.
func MaxOutputTokens(string) int { return 0 }
