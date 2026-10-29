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

// Package llamaserver runs the OpenAI-compatible `llama-server` binary as a
// managed subprocess pool whose lifecycle is driven by CreateCompletion
// demand. Each distinct resolved model gets its own server process with an
// idle timeout; the pool LRU-evicts past a configured cap. The package
// implements llmapi.Service by delegating to each server's embedded
// OpenAI-compatible client.
package llamaserver

import (
	"strconv"
	"time"
)

// LLMProvider is the provider identifier carried on ModelEntry.Provider for
// locally-served GGUF models. It matches the llamacpp registry provider so
// the router dispatches registry entries to this backend unchanged.
const LLMProvider = "llamacpp"

const (
	defaultIdleTimeout    = 5 * time.Minute
	defaultMaxServers     = 1
	defaultStartupTimeout = 120 * time.Second
	defaultHost           = "127.0.0.1"
	// defaultEmbeddingCtx is the context (and physical batch) of an
	// embedding server when neither the config nor the entry sets one.
	defaultEmbeddingCtx = 2048
)

// Config carries the server tunables sourced from `models.local.*` plus the
// lifecycle knobs that govern the managed subprocess pool.
type Config struct {
	// ContextWindow is the n_ctx requested via --ctx-size. 0 lets
	// llama-server use the model's training context window.
	ContextWindow uint32

	// BatchSize maps to --batch-size (n_batch). 0 picks the server default.
	BatchSize uint32

	// NGPULayers maps to --n-gpu-layers. Negative offloads every layer the
	// backend supports (the llama-server convention for "all").
	NGPULayers int

	// Threads maps to --threads. 0 lets llama-server decide.
	Threads int

	// FlashAttention enables --flash-attn when true.
	FlashAttention bool

	// MaxOutputTokens caps generated tokens per request via -n / --predict.
	// 0 means "until end-of-generation or context exhausted".
	MaxOutputTokens int

	// ChatTemplate overrides the GGUF's embedded template via --chat-template.
	ChatTemplate string

	// NCacheReuse maps to --cache-reuse. 0 disables shift-reuse.
	NCacheReuse int

	// Sampling carries every sampler knob; reused verbatim from the llamacpp
	// config so the `models.local.sampling.*` mapping is shared.
	Sampling SamplerParams

	// Host is the loopback address llama-server binds to. Defaults to
	// 127.0.0.1 when empty.
	Host string

	// ServerBinPath, when set, is used verbatim instead of resolving
	// `llama-server` through the package manager or $PATH.
	ServerBinPath string

	// ExtraArgs are appended verbatim to the llama-server command line after
	// the derived flags.
	ExtraArgs []string

	// IdleTimeout is how long a server may sit with zero in-flight requests
	// before it is stopped and evicted. 0 uses defaultIdleTimeout.
	IdleTimeout time.Duration

	// MaxServers caps the number of concurrently running servers. When a new
	// model would exceed the cap the least-recently-used idle server is
	// evicted. 0 uses defaultMaxServers.
	MaxServers int

	// StartupTimeout bounds how long Acquire waits for a freshly started
	// server to report healthy. 0 uses defaultStartupTimeout.
	StartupTimeout time.Duration
}

// DefaultConfig returns the baseline llamaserver config used before the
// `models.local.*` block is overlaid.
func DefaultConfig() Config {
	return Config{
		NGPULayers:     -1,
		Sampling:       DefaultSamplerParams(),
		IdleTimeout:    defaultIdleTimeout,
		MaxServers:     defaultMaxServers,
		StartupTimeout: defaultStartupTimeout,
		Host:           defaultHost,
	}
}

// withDefaults returns a copy of cfg with any zero lifecycle knob replaced by
// its default so the pool never divides by zero or arms a zero-length timer.
func (c Config) withDefaults() Config {
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = defaultIdleTimeout
	}
	if c.MaxServers <= 0 {
		c.MaxServers = defaultMaxServers
	}
	if c.StartupTimeout <= 0 {
		c.StartupTimeout = defaultStartupTimeout
	}
	if c.Host == "" {
		c.Host = defaultHost
	}
	return c
}

// serverArgs builds the llama-server flag slice for a single resolved model.
// modelPath and projectorPath come from the resolved ModelEntry;
// contextWindow overrides c.ContextWindow when non-zero so per-model context
// windows from the registry take effect. host and port bind the server.
// embedding starts a mean-pooling encoder instead of a chat server: the
// generation flags are meaningless there and are left out, and the batch
// sizes equal the context because llama-server rejects an embedding input
// larger than one physical batch.
func (c Config) serverArgs(
	modelPath, projectorPath string, contextWindow uint32, embedding bool, host string, port int,
) []string {
	args := []string{
		"--model", modelPath,
		"--host", host,
		"--port", strconv.Itoa(port),
	}
	if projectorPath != "" {
		args = append(args, "--mmproj", projectorPath)
	}
	ctxSize := c.ContextWindow
	if contextWindow != 0 {
		ctxSize = contextWindow
	}
	if embedding && ctxSize == 0 {
		ctxSize = defaultEmbeddingCtx
	}
	if ctxSize != 0 {
		args = append(args, "--ctx-size", strconv.FormatUint(uint64(ctxSize), 10))
	}
	if embedding {
		n := strconv.FormatUint(uint64(ctxSize), 10)
		args = append(args, "--embedding", "--pooling", "mean", "--batch-size", n, "--ubatch-size", n)
	} else if c.BatchSize != 0 {
		args = append(args, "--batch-size", strconv.FormatUint(uint64(c.BatchSize), 10))
	}
	// llama-server takes "all layers" as any sufficiently large count;
	// negative values from our config mean the same thing.
	if c.NGPULayers < 0 {
		args = append(args, "--n-gpu-layers", "999")
	} else if c.NGPULayers > 0 {
		args = append(args, "--n-gpu-layers", strconv.Itoa(c.NGPULayers))
	}
	if c.Threads > 0 {
		args = append(args, "--threads", strconv.Itoa(c.Threads))
	}
	if c.FlashAttention {
		args = append(args, "--flash-attn")
	}
	if embedding {
		return append(args, c.ExtraArgs...)
	}
	if c.MaxOutputTokens > 0 {
		args = append(args, "--predict", strconv.Itoa(c.MaxOutputTokens))
	}
	if c.ChatTemplate != "" {
		args = append(args, "--chat-template", c.ChatTemplate)
	}
	if c.NCacheReuse > 0 {
		args = append(args, "--cache-reuse", strconv.Itoa(c.NCacheReuse))
	}
	args = append(args, c.samplingArgs()...)
	args = append(args, c.ExtraArgs...)
	return args
}

// samplingArgs maps the sampler params onto llama-server flags. Only knobs
// that differ from the server's own defaults are emitted so an unset config
// leaves the server's built-in defaults untouched.
func (c Config) samplingArgs() []string {
	s := c.Sampling
	var args []string
	if s.Seed != 0 {
		args = append(args, "--seed", strconv.FormatUint(uint64(s.Seed), 10))
	}
	if s.Temperature != 0 {
		args = append(args, "--temp", formatFloat(s.Temperature))
	}
	if s.TopK != 0 {
		args = append(args, "--top-k", strconv.Itoa(s.TopK))
	}
	if s.TopP != 0 {
		args = append(args, "--top-p", formatFloat(s.TopP))
	}
	if s.MinP != 0 {
		args = append(args, "--min-p", formatFloat(s.MinP))
	}
	if s.RepeatPenalty != 0 {
		args = append(args, "--repeat-penalty", formatFloat(s.RepeatPenalty))
	}
	if s.RepeatLastN != 0 {
		args = append(args, "--repeat-last-n", strconv.Itoa(s.RepeatLastN))
	}
	if s.FreqPenalty != 0 {
		args = append(args, "--frequency-penalty", formatFloat(s.FreqPenalty))
	}
	if s.PresencePenalty != 0 {
		args = append(args, "--presence-penalty", formatFloat(s.PresencePenalty))
	}
	return args
}

func formatFloat(f float32) string {
	return strconv.FormatFloat(float64(f), 'g', -1, 32)
}
