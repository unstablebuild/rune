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

// Package llm holds the top-level Config struct that the rune-side
// llmrouter consumes. It is intentionally a leaf package: it does not
// import any of the provider sub-packages directly, only their typed
// Config values. That keeps the dependency direction one-way (router
// and ide import llm; providers do not).
package llm

import (
	"unstable.build/rune/internal/llm/anthropic"
	"unstable.build/rune/internal/llm/bedrock"
	"unstable.build/rune/internal/llm/codex"
	"unstable.build/rune/internal/llm/gemini"
	"unstable.build/rune/internal/llm/llamaserver"
	"unstable.build/rune/internal/llm/openai"
)

// Config is the full rune-side LLM configuration. It is built by
// ide.modelsConfig from DefaultConfig() plus the merged `models.*`
// config block. Every nested sub-config is the same struct the
// provider client takes at construction time, so there are no
// untyped maps anywhere in the router.
type Config struct {
	// ReasoningSummary is forwarded to every OpenAI-compatible client
	// (OpenAI, Codex, Gemini, custom) as the Responses API
	// reasoning summary level ("auto", "concise", "detailed",
	// "disabled").
	ReasoningSummary string

	// DebugHTTP enables HTTP debug logging for every provider client.
	DebugHTTP bool

	// Provider sub-configs. Each carries the fields the provider
	// client actually understands; rune.star defaults are mapped onto
	// these directly.
	OpenAI    OpenAIConfig
	Anthropic AnthropicConfig
	Gemini    GeminiConfig
	Bedrock   BedrockConfig
	Codex     CodexConfig
	Claude    ClaudeConfig
	Custom    CustomConfig
	Local     LocalConfig
}

// OpenAIConfig wraps openai.Config with the credentials and base URL
// fields that come from `models.openai.*`.
type OpenAIConfig struct {
	APIKey          string
	BaseURL         string
	ReasoningEffort string
}

// AnthropicConfig captures `models.anthropic.*`.
type AnthropicConfig struct {
	APIKey          string
	BaseURL         string
	ReasoningEffort string
	CacheControl    string
}

// GeminiConfig captures `models.gemini.*`.
type GeminiConfig struct {
	APIKey          string
	BaseURL         string
	ReasoningEffort string
}

// BedrockConfig captures `models.bedrock.*`. The API key and region are
// deliberately absent: Bedrock keys are region-scoped, so both are stored
// together through the `models providers bedrock add <name> <region>`
// keystore flow. Without a stored key the client authenticates through
// the standard AWS credential chain, whose region comes from the AWS
// environment or the selected profile.
type BedrockConfig struct {
	Profile         string
	BaseURL         string
	ReasoningEffort string
	CacheControl    string
}

// CodexConfig captures `models.codex.*`.
type CodexConfig struct {
	BaseURL string
}

// ClaudeConfig captures `models.claude.*`. The claude provider is
// OAuth-only (no API key); only the base URL is configurable.
type ClaudeConfig struct {
	BaseURL string
}

// CustomConfig captures `models.custom.*`. AvailableModels maps a
// user-declared model name to its context window.
type CustomConfig struct {
	URL             string
	APIKey          string
	AvailableModels map[string]int
}

// LocalConfig captures `models.local.*`. It carries the llama-server
// tunables plus the managed-subprocess lifecycle knobs. Model-specific
// fields (model path, projector, context window) are supplied per request
// by the backend from the resolved llmapi.ModelEntry.
type LocalConfig struct {
	// ModelsCacheDir is the on-disk root for the llamacpp registry.
	// When empty, the router falls back to filepath.Join(dataDir, "models").
	ModelsCacheDir string

	// Service carries every llama-server runtime knob and lifecycle knob.
	Service llamaserver.Config
}

// DefaultConfig returns the baseline used by ide.llmConfig() before
// the `models.*` block is overlaid. It encodes the static defaults
// from rune.star.
func DefaultConfig() Config {
	return Config{
		ReasoningSummary: "auto",
		Local: LocalConfig{
			Service: llamaserver.DefaultConfig(),
		},
	}
}

// OpenAIClientConfig projects an openai.Config view of the OpenAI
// sub-config. Used by the router when constructing the OpenAI client.
func (c Config) OpenAIClientConfig() openai.Config {
	return openai.Config{
		BaseURL:           c.OpenAI.BaseURL,
		ReasoningEffort:   c.OpenAI.ReasoningEffort,
		ReasoningSummary:  c.ReasoningSummary,
		ForceResponsesAPI: true,
		DebugHTTP:         c.DebugHTTP,
	}
}

// AnthropicClientConfig projects an anthropic.Config view.
func (c Config) AnthropicClientConfig() anthropic.Config {
	return anthropic.Config{
		BaseURL:         c.Anthropic.BaseURL,
		ReasoningEffort: c.Anthropic.ReasoningEffort,
		CacheControl:    c.Anthropic.CacheControl,
		DebugHTTP:       c.DebugHTTP,
	}
}

// CodexClientConfig projects the openai.Config view used by the Codex
// provider, which always forces the Responses API. When the user has
// not overridden the base URL the ChatGPT Codex backend is used
// (codex.OpenAICompatibleURL); the router fills the per-request
// credentials, ClientMetadata, and Headers separately.
func (c Config) CodexClientConfig() openai.Config {
	baseURL := c.Codex.BaseURL
	if baseURL == "" {
		baseURL = codex.OpenAICompatibleURL
	}
	return openai.Config{
		BaseURL:           baseURL,
		ReasoningEffort:   c.OpenAI.ReasoningEffort,
		ReasoningSummary:  c.ReasoningSummary,
		ForceResponsesAPI: true,
		DebugHTTP:         c.DebugHTTP,
		// ChatGPT Codex backend specifics: it is stateless and
		// rejects parallel tool calls and client-set output limits.
		Store:                    boolPtr(false),
		DisableParallelToolCalls: true,
		DisableMaxOutputTokens:   true,
	}
}

// GeminiClientConfig projects the native gemini.Config used by the
// Gemini provider, which talks to the Gemini API through Google's
// google.golang.org/genai SDK.
func (c Config) GeminiClientConfig() gemini.Config {
	return gemini.Config{
		BaseURL:         c.Gemini.BaseURL,
		ReasoningEffort: c.Gemini.ReasoningEffort,
		DebugHTTP:       c.DebugHTTP,
	}
}

// BedrockClientConfig projects the bedrock.Config used by the Bedrock
// provider, which talks to the Amazon Bedrock ConverseStream API.
func (c Config) BedrockClientConfig() bedrock.Config {
	return bedrock.Config{
		Profile:         c.Bedrock.Profile,
		BaseURL:         c.Bedrock.BaseURL,
		ReasoningEffort: c.Bedrock.ReasoningEffort,
		CacheControl:    c.Bedrock.CacheControl,
		DebugHTTP:       c.DebugHTTP,
	}
}

// ClaudeClientConfig projects the anthropic.Config view used by the
// claude provider, which authenticates an Anthropic client with a Claude
// Code subscription OAuth token. The router fills OAuthToken and Headers
// per-request from the resolved credential.
func (c Config) ClaudeClientConfig() anthropic.Config {
	return anthropic.Config{
		BaseURL:         c.Claude.BaseURL,
		ReasoningEffort: c.Anthropic.ReasoningEffort,
		CacheControl:    c.Anthropic.CacheControl,
		DebugHTTP:       c.DebugHTTP,
		ClaudeCodeSpoof: true,
	}
}

// CustomClientConfig projects the openai.Config used to talk to a
// user-configured OpenAI-compatible endpoint.
func (c Config) CustomClientConfig() openai.Config {
	return openai.Config{
		BaseURL:   c.Custom.URL,
		DebugHTTP: c.DebugHTTP,
	}
}

// OllamaClientConfig projects the openai.Config used to talk to a
// per-model Ollama URL.
func (c Config) OllamaClientConfig(baseURL string) openai.Config {
	return openai.Config{
		BaseURL:   baseURL,
		DebugHTTP: c.DebugHTTP,
	}
}

// boolPtr returns a pointer to v. Used for openai.Config fields that
// distinguish "unset" from "false" via *bool semantics.
func boolPtr(v bool) *bool { return &v }
