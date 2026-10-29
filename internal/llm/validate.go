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

package llm

import (
	"fmt"
	"path/filepath"
	"sort"
)

// validReasoningEffort enumerates the strings accepted by every
// provider that supports a reasoning_effort knob. The empty string
// means "use the provider default".
var validReasoningEffort = map[string]struct{}{
	"":        {},
	"none":    {},
	"minimal": {},
	"low":     {},
	"medium":  {},
	"high":    {},
	"xhigh":   {},
	"max":     {},
	"ultra":   {},
}

// validReasoningSummary enumerates the strings accepted by the OpenAI
// Responses API for the reasoning summary level. Empty defers to the
// provider default.
var validReasoningSummary = map[string]struct{}{
	"":         {},
	"auto":     {},
	"concise":  {},
	"detailed": {},
	"disabled": {},
}

// ValidateConfig validates the typed Config the rune-side LLM router
// consumes. Any error returned here would otherwise surface as a
// runtime failure inside llmrouter.New or the first router call, so
// the IDE refuses to load the config rather than tolerate it.
//
// The rule is simple: any input that would cause llmrouter.New to
// return an error must be caught here, plus any out-of-range enum
// values that the provider clients would reject at request time.
func ValidateConfig(cfg Config) error {
	if _, ok := validReasoningSummary[cfg.ReasoningSummary]; !ok {
		return fmt.Errorf(
			"models.reasoning_summary: %q is not one of auto|concise|detailed|disabled",
			cfg.ReasoningSummary)
	}
	if _, ok := validReasoningEffort[cfg.OpenAI.ReasoningEffort]; !ok {
		return fmt.Errorf(
			"models.openai.reasoning_effort: %q is not one of "+
				"none|minimal|low|medium|high|xhigh|max|ultra",
			cfg.OpenAI.ReasoningEffort)
	}
	if _, ok := validReasoningEffort[cfg.Anthropic.ReasoningEffort]; !ok {
		return fmt.Errorf(
			"models.anthropic.reasoning_effort: %q is not one of "+
				"none|minimal|low|medium|high|xhigh|max|ultra",
			cfg.Anthropic.ReasoningEffort)
	}
	if _, ok := validReasoningEffort[cfg.Gemini.ReasoningEffort]; !ok {
		return fmt.Errorf(
			"models.gemini.reasoning_effort: %q is not one of "+
				"none|minimal|low|medium|high|xhigh|max|ultra",
			cfg.Gemini.ReasoningEffort)
	}
	if _, ok := validReasoningEffort[cfg.Bedrock.ReasoningEffort]; !ok {
		return fmt.Errorf(
			"models.bedrock.reasoning_effort: %q is not one of "+
				"none|minimal|low|medium|high|xhigh|max|ultra",
			cfg.Bedrock.ReasoningEffort)
	}
	// The IDE mints a llamacpp.Registry whose root must not be empty.
	// It falls back to the models folder of its data directory when
	// ModelsCacheDir is empty, so the only failure case here is a
	// non-absolute user override.
	if cfg.Local.ModelsCacheDir != "" && !filepath.IsAbs(cfg.Local.ModelsCacheDir) {
		return fmt.Errorf(
			"models.local.models_cache_dir: %q must be an absolute path",
			cfg.Local.ModelsCacheDir)
	}
	// Custom provider: when AvailableModels is populated, URL must be
	// set; otherwise the entries would point at the empty base URL
	// and any CreateCompletion call would fail at request time.
	if len(cfg.Custom.AvailableModels) > 0 && cfg.Custom.URL == "" {
		return fmt.Errorf(
			"models.custom.available_models: declared models %v require models.custom.url",
			sortedKeys(cfg.Custom.AvailableModels))
	}
	// Sampling integers: negative values for top_k / repeat_last_n
	// would be silently truncated to zero by the C bindings; reject
	// them outright so the user sees the typo.
	if cfg.Local.Service.Sampling.TopK < 0 {
		return fmt.Errorf(
			"models.local.sampling.top_k: must be >= 0, got %d",
			cfg.Local.Service.Sampling.TopK)
	}
	if cfg.Local.Service.Sampling.RepeatLastN < 0 {
		return fmt.Errorf(
			"models.local.sampling.repeat_last_n: must be >= 0, got %d",
			cfg.Local.Service.Sampling.RepeatLastN)
	}
	return nil
}

// sortedKeys returns the sorted set of keys for an error message so
// the output is deterministic.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
