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
	"fmt"
	"strconv"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

// LLMProvider identifies the AWS Bedrock provider in the model registry.
const LLMProvider = "bedrock"

// Bedrock addresses models either by raw foundation-model ID or by
// inference-profile ID. The static catalog uses US cross-region inference
// profiles ("us." prefix) because on-demand throughput for the current
// generation of models is only offered through them.
const (
	// ClaudeFable52 is Anthropic's Claude Fable 5.2 on Bedrock.
	ClaudeFable52 = "us.anthropic.claude-fable-5-2"
	// ClaudeFable51 is Anthropic's Claude Fable 5.1 on Bedrock.
	ClaudeFable51 = "us.anthropic.claude-fable-5-1"
	// ClaudeFable5 is Anthropic's Claude Fable 5 on Bedrock.
	ClaudeFable5 = "us.anthropic.claude-fable-5"
	// ClaudeOpus55 is Anthropic's Claude Opus 5.5 on Bedrock.
	ClaudeOpus55 = "us.anthropic.claude-opus-5-5"
	// ClaudeSonnet55 is Anthropic's Claude Sonnet 5.5 on Bedrock.
	ClaudeSonnet55 = "us.anthropic.claude-sonnet-5-5"
	// ClaudeHaiku55 is Anthropic's Claude Haiku 5.5 on Bedrock.
	ClaudeHaiku55 = "us.anthropic.claude-haiku-5-5"
	// ClaudeOpus5 is Anthropic's Claude Opus 5 on Bedrock.
	ClaudeOpus5 = "us.anthropic.claude-opus-5"
	// ClaudeSonnet5 is Anthropic's Claude Sonnet 5 on Bedrock.
	ClaudeSonnet5 = "us.anthropic.claude-sonnet-5"
	// ClaudeOpus48 is Anthropic's Claude Opus 4.8 on Bedrock.
	ClaudeOpus48 = "us.anthropic.claude-opus-4-8"
	// ClaudeOpus47 is Anthropic's Claude Opus 4.7 on Bedrock.
	ClaudeOpus47 = "us.anthropic.claude-opus-4-7"
	// ClaudeOpus46 is Anthropic's Claude Opus 4.6 on Bedrock.
	ClaudeOpus46 = "us.anthropic.claude-opus-4-6-v1"
	// ClaudeSonnet46 is Anthropic's Claude Sonnet 4.6 on Bedrock.
	ClaudeSonnet46 = "us.anthropic.claude-sonnet-4-6"
	// ClaudeOpus45 is Anthropic's Claude Opus 4.5 on Bedrock.
	ClaudeOpus45 = "us.anthropic.claude-opus-4-5-20251101-v1:0"
	// ClaudeHaiku45 is Anthropic's Claude Haiku 4.5 on Bedrock.
	ClaudeHaiku45 = "us.anthropic.claude-haiku-4-5-20251001-v1:0"
	// GPT62Astra is OpenAI's GPT-6.2 Astra on Bedrock.
	GPT62Astra = "us.openai.gpt-6.2-astra"
	// GPT62Sol is OpenAI's GPT-6.2 Sol on Bedrock.
	GPT62Sol = "us.openai.gpt-6.2-sol"
	// GPT6Astra is OpenAI's GPT-6 Astra on Bedrock.
	GPT6Astra = "us.openai.gpt-6-astra"
	// GPT61Sol is OpenAI's GPT-6.1 Sol on Bedrock.
	GPT61Sol = "us.openai.gpt-6.1-sol"
	// GPT61Terra is OpenAI's GPT-6.1 Terra on Bedrock.
	GPT61Terra = "us.openai.gpt-6.1-terra"
	// GPT6Sol is OpenAI's GPT-6 Sol on Bedrock.
	GPT6Sol = "us.openai.gpt-6-sol"
	// GPT6Luna is OpenAI's GPT-6 Luna on Bedrock.
	GPT6Luna = "us.openai.gpt-6-luna"
	// GPT56Sol is OpenAI's GPT-5.6 Sol on Bedrock.
	GPT56Sol = "us.openai.gpt-5.6-sol"
	// GPT56Terra is OpenAI's GPT-5.6 Terra on Bedrock.
	GPT56Terra = "us.openai.gpt-5.6-terra"
	// GPT56Luna is OpenAI's GPT-5.6 Luna on Bedrock.
	GPT56Luna = "us.openai.gpt-5.6-luna"
	// Nova2Lite is Amazon's Nova 2 Lite model.
	Nova2Lite = "us.amazon.nova-2-lite-v1:0"
	// NovaPremier is Amazon's Nova Premier model.
	NovaPremier = "us.amazon.nova-premier-v1:0"
	// NovaPro is Amazon's Nova Pro model.
	NovaPro = "us.amazon.nova-pro-v1:0"
	// NovaLite is Amazon's Nova Lite model.
	NovaLite = "us.amazon.nova-lite-v1:0"
	// NovaMicro is Amazon's Nova Micro model.
	NovaMicro = "us.amazon.nova-micro-v1:0"
	// Llama4Maverick is Meta's Llama 4 Maverick model.
	Llama4Maverick = "us.meta.llama4-maverick-17b-instruct-v1:0"
	// Llama4Scout is Meta's Llama 4 Scout model.
	Llama4Scout = "us.meta.llama4-scout-17b-instruct-v1:0"
	// DeepSeekR1 is DeepSeek's R1 reasoning model.
	DeepSeekR1 = "us.deepseek.r1-v1:0"
	// MistralLarge is Mistral's Large 2 model.
	MistralLarge = "mistral.mistral-large-2407-v1:0"
)

// AvailableModels returns the static fallback catalog: a map from Bedrock
// model or inference-profile identifier -> nominal maximum context window
// (tokens). Model access varies per AWS account and region, so this list is
// only a starting point — the live catalog (see Client.Models) reports what
// the caller's account can actually invoke.
func AvailableModels() map[string]int {
	return map[string]int{
		ClaudeFable52:  1000000,
		ClaudeFable51:  1000000,
		ClaudeFable5:   1000000,
		ClaudeOpus55:   1000000,
		ClaudeSonnet55: 1000000,
		ClaudeHaiku55:  1000000,
		ClaudeOpus5:    1000000,
		ClaudeSonnet5:  1000000,
		ClaudeOpus48:   1000000,
		ClaudeOpus47:   1000000,
		ClaudeOpus46:   1000000,
		ClaudeSonnet46: 1000000,
		ClaudeOpus45:   200000,
		ClaudeHaiku45:  200000,
		GPT62Astra:     1050000,
		GPT62Sol:       1050000,
		GPT6Astra:      1050000,
		GPT61Sol:       1000000,
		GPT61Terra:     1050000,
		GPT6Sol:        1050000,
		GPT6Luna:       1050000,
		GPT56Sol:       1050000,
		GPT56Terra:     1050000,
		GPT56Luna:      1050000,
		Nova2Lite:      1000000,
		NovaPremier:    1000000,
		NovaPro:        300000,
		NovaLite:       300000,
		NovaMicro:      128000,
		Llama4Maverick: 1000000,
		Llama4Scout:    1000000,
		DeepSeekR1:     128000,
		MistralLarge:   128000,
	}
}

// maxOutput maps each catalog entry to its documented maximum output-token
// ceiling. Entries absent from the map have an unknown ceiling;
// MaxOutputTokens returns 0 for them so callers apply no cap.
var maxOutput = map[string]int{
	ClaudeFable52:  128000,
	ClaudeFable51:  128000,
	ClaudeFable5:   128000,
	ClaudeOpus55:   128000,
	ClaudeSonnet55: 128000,
	ClaudeHaiku55:  128000,
	ClaudeOpus5:    128000,
	ClaudeSonnet5:  128000,
	ClaudeOpus48:   128000,
	ClaudeOpus47:   128000,
	ClaudeOpus46:   128000,
	ClaudeSonnet46: 64000,
	ClaudeOpus45:   64000,
	ClaudeHaiku45:  64000,
	GPT62Astra:     128000,
	GPT62Sol:       128000,
	GPT6Astra:      128000,
	GPT61Sol:       131072,
	GPT61Terra:     128000,
	GPT6Sol:        128000,
	GPT6Luna:       128000,
	GPT56Sol:       128000,
	GPT56Terra:     128000,
	GPT56Luna:      128000,
	Nova2Lite:      64000,
	NovaPremier:    32000,
	NovaPro:        10000,
	NovaLite:       10000,
	NovaMicro:      10000,
	Llama4Maverick: 8192,
	Llama4Scout:    8192,
	DeepSeekR1:     32768,
	MistralLarge:   8192,
}

// MaxOutputTokens returns the model's documented maximum output-token
// ceiling, or 0 when the limit is unknown.
func MaxOutputTokens(model string) int { return maxOutput[model] }

// IsClaudeModel reports whether the Bedrock identifier addresses an
// Anthropic Claude model. Claude is the only family whose thinking
// is configured through additionalModelRequestFields.
func IsClaudeModel(model string) bool {
	return strings.Contains(model, "anthropic.claude")
}

// SupportsEffort reports whether reasoning effort can be mapped onto the
// model's request parameters. Only Claude thinking is mapped; other
// families either always reason (DeepSeek R1), take reasoning controls this
// client does not send (OpenAI, Nova 2), or do not reason at all.
func SupportsEffort(model string) bool {
	// Claude 3 predates extended thinking.
	return IsClaudeModel(model) && !strings.Contains(model, "claude-3")
}

// claudeVersion parses the major and minor version out of a Claude model or
// inference-profile ID such as "us.anthropic.claude-opus-4-6-v1". It reports
// false for non-Claude IDs and for Claude 3, whose IDs put the version
// before the family name.
func claudeVersion(model string) (major, minor int, ok bool) {
	_, rest, found := strings.Cut(stripRegionPrefix(model), "anthropic.claude-")
	if !found {
		return 0, 0, false
	}
	parts := strings.Split(rest, "-")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	// Older IDs follow the version with a snapshot date, not a minor
	// version: claude-sonnet-4-20250514-v1:0 is Claude Sonnet 4.0.
	if len(parts) > 2 && len(parts[2]) <= 2 {
		if n, err := strconv.Atoi(parts[2]); err == nil {
			minor = n
		}
	}
	return major, minor, true
}

// SupportsAdaptiveThinking reports whether the model is Claude 4.6 or later,
// whose thinking Bedrock configures with thinking.type "adaptive" and an
// output_config.effort level. Claude 5 and Opus 4.7+ reject the fixed
// budget_tokens form earlier models require.
func SupportsAdaptiveThinking(model string) bool {
	major, minor, ok := claudeVersion(model)
	return ok && (major > 4 || (major == 4 && minor >= 6))
}

// thinkingBudget maps a normalized effort level onto the extended thinking
// token budget sent in additionalModelRequestFields to Claude models
// without adaptive thinking.
var thinkingBudget = map[string]int{
	"low":    4096,
	"medium": 16384,
	"high":   32768,
}

// adaptiveEfforts are the output_config.effort levels adaptive-thinking
// Claude models accept. Claude 4.6 models stop short of xhigh.
var (
	adaptiveEfforts = map[string]bool{
		"low":    true,
		"medium": true,
		"high":   true,
		"xhigh":  true,
		"max":    true,
	}
	claude46Efforts = map[string]bool{
		"low":    true,
		"medium": true,
		"high":   true,
		"max":    true,
	}
)

// supportsEffortLevel reports whether the model accepts the given effort.
func supportsEffortLevel(model, effort string) bool {
	if !SupportsAdaptiveThinking(model) {
		_, ok := thinkingBudget[effort]
		return ok
	}
	if major, minor, _ := claudeVersion(model); major == 4 && minor == 6 {
		return claude46Efforts[effort]
	}
	return adaptiveEfforts[effort]
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
	if supportsEffortLevel(model, effort) {
		return effort, ""
	}
	return "", fmt.Sprintf(
		"Effort %q is not supported by %s; using model default instead.", effort, model)
}

// ModelEntries returns the static fallback catalog as router-ready model
// entries.
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
// deterministic, unlike iterating ModelEntries() whose order is map-random.
func FlagshipModel() string { return ClaudeOpus55 }

// VerificationModelForRegion returns the flagship inference profile that
// serves the given AWS region. Cross-region profiles are partitioned by
// geography ("us.", "eu.", "apac."), so a key scoped to a European region
// must be verified against the "eu." profile; the US profile would fail
// with a model-access error even when the key is valid.
func VerificationModelForRegion(region string) string {
	return RegionalProfile(FlagshipModel(), region)
}

// RegionalProfile rewrites a cross-region inference profile ID so it
// addresses the geography serving the given AWS region. Global profiles
// already serve every commercial region and are returned unchanged.
func RegionalProfile(model, region string) string {
	if strings.HasPrefix(model, "global.") {
		return model
	}
	return regionPrefixFor(region) + stripRegionPrefix(model)
}

// regionPrefixFor returns the cross-region inference profile prefix that
// serves the given AWS region.
func regionPrefixFor(region string) string {
	switch {
	case strings.HasPrefix(region, "us-gov-"):
		return "us-gov."
	case strings.HasPrefix(region, "eu-"):
		return "eu."
	case strings.HasPrefix(region, "ap-"):
		return "apac."
	default:
		return "us."
	}
}
