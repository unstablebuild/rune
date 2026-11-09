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

package llamaserver

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
)

// argValue returns the value following flag in args, or "" if absent.
func argValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestConfig_ServerArgs_CoreFlags(t *testing.T) {
	cfg := Config{
		ContextWindow:   4096,
		BatchSize:       512,
		NGPULayers:      -1,
		Threads:         8,
		FlashAttention:  true,
		MaxOutputTokens: 256,
		ChatTemplate:    "chatml",
		NCacheReuse:     128,
		ExtraArgs:       []string{"--verbose"},
	}
	args := cfg.serverArgs("/m.gguf", "/proj.gguf", 0, false, "127.0.0.1", 8080)

	assert.Equal(t, "/m.gguf", argValue(args, "--model"))
	assert.Equal(t, "/proj.gguf", argValue(args, "--mmproj"))
	assert.Equal(t, "127.0.0.1", argValue(args, "--host"))
	assert.Equal(t, "8080", argValue(args, "--port"))
	assert.Equal(t, "4096", argValue(args, "--ctx-size"))
	assert.Equal(t, "512", argValue(args, "--batch-size"))
	assert.Equal(t, "999", argValue(args, "--n-gpu-layers"), "negative offloads all layers")
	assert.Equal(t, "8", argValue(args, "--threads"))
	assert.True(t, slices.Contains(args, "--flash-attn"))
	assert.Equal(t, "256", argValue(args, "--predict"))
	assert.Equal(t, "chatml", argValue(args, "--chat-template"))
	assert.Equal(t, "128", argValue(args, "--cache-reuse"))
	assert.Equal(t, "--verbose", args[len(args)-1], "extra args appended last")
}

func TestConfig_ServerArgs_EmbeddingMode(t *testing.T) {
	chatKnobs := Config{
		BatchSize:       512,
		NGPULayers:      -1,
		Threads:         4,
		FlashAttention:  true,
		MaxOutputTokens: 256,
		ChatTemplate:    "chatml",
		NCacheReuse:     128,
		Sampling:        SamplerParams{Temperature: 0.7, TopK: 40, Seed: 7},
	}
	for _, tc := range []struct {
		name      string
		cfg       Config
		ctxWindow uint32
		wantN     string
	}{
		{name: "unset context defaults to 2048", cfg: chatKnobs, wantN: "2048"},
		{name: "config context sizes both batches", cfg: func() Config { c := chatKnobs; c.ContextWindow = 1024; return c }(), wantN: "1024"},
		{name: "entry context wins", cfg: chatKnobs, ctxWindow: 512, wantN: "512"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.cfg.serverArgs("/e.gguf", "", tc.ctxWindow, true, "127.0.0.1", 1)
			assert.True(t, slices.Contains(args, "--embedding"))
			assert.Equal(t, "mean", argValue(args, "--pooling"))
			assert.Equal(t, tc.wantN, argValue(args, "--ctx-size"))
			assert.Equal(t, tc.wantN, argValue(args, "--batch-size"))
			assert.Equal(t, tc.wantN, argValue(args, "--ubatch-size"))
			assert.Equal(t, 1, countFlag(args, "--batch-size"), "config batch size is overridden, not duplicated")
			assert.Equal(t, "999", argValue(args, "--n-gpu-layers"))
			assert.Equal(t, "4", argValue(args, "--threads"))
			assert.True(t, slices.Contains(args, "--flash-attn"))
			for _, flag := range []string{"--mmproj", "--predict", "--chat-template", "--cache-reuse", "--temp", "--top-k", "--seed"} {
				assert.False(t, slices.Contains(args, flag), flag)
			}
		})
	}
}

func countFlag(args []string, flag string) int {
	n := 0
	for _, a := range args {
		if a == flag {
			n++
		}
	}
	return n
}

func TestConfig_ServerArgs_PerModelContextOverridesConfig(t *testing.T) {
	cfg := Config{ContextWindow: 4096}
	args := cfg.serverArgs("/m.gguf", "", 8192, false, "127.0.0.1", 1)
	assert.Equal(t, "8192", argValue(args, "--ctx-size"),
		"registry per-model context window wins over config default")
}

func TestConfig_ServerArgs_OmitsUnsetKnobs(t *testing.T) {
	cfg := Config{}
	args := cfg.serverArgs("/m.gguf", "", 0, false, "127.0.0.1", 1)
	assert.Empty(t, argValue(args, "--mmproj"))
	assert.Empty(t, argValue(args, "--ctx-size"))
	assert.Empty(t, argValue(args, "--batch-size"))
	assert.Empty(t, argValue(args, "--n-gpu-layers"), "zero gpu layers omits the flag")
	assert.False(t, slices.Contains(args, "--flash-attn"))
}

func TestConfig_ServerArgs_SamplingFlags(t *testing.T) {
	cfg := Config{Sampling: SamplerParams{
		Temperature:     0.7,
		TopK:            40,
		TopP:            0.9,
		MinP:            0.05,
		RepeatPenalty:   1.1,
		RepeatLastN:     64,
		FreqPenalty:     0.2,
		PresencePenalty: 0.3,
	}}
	args := cfg.serverArgs("/m.gguf", "", 0, false, "127.0.0.1", 1)
	assert.Equal(t, "0.7", argValue(args, "--temp"))
	assert.Equal(t, "40", argValue(args, "--top-k"))
	assert.Equal(t, "0.9", argValue(args, "--top-p"))
	assert.Equal(t, "0.05", argValue(args, "--min-p"))
	assert.Equal(t, "1.1", argValue(args, "--repeat-penalty"))
	assert.Equal(t, "64", argValue(args, "--repeat-last-n"))
	assert.Equal(t, "0.2", argValue(args, "--frequency-penalty"))
	assert.Equal(t, "0.3", argValue(args, "--presence-penalty"))
}

func TestConfig_WithDefaults(t *testing.T) {
	got := Config{}.withDefaults()
	assert.Equal(t, defaultIdleTimeout, got.IdleTimeout)
	assert.Equal(t, defaultMaxServers, got.MaxServers)
	assert.Equal(t, defaultStartupTimeout, got.StartupTimeout)
	assert.Equal(t, defaultHost, got.Host)
}

func TestParseLoadProgress(t *testing.T) {
	for _, tc := range []struct {
		line string
		want int
		ok   bool
	}{
		{"load_tensors: loading model tensors 42%", 42, true},
		{"llama_model_loader: 100%", 100, true},
		{"some line with 7.5% done", 7, true},
		{"no percentage here", 0, false},
		{"trailing percent sign only %", 0, false},
	} {
		got, ok := parseLoadProgress(tc.line)
		assert.Equal(t, tc.ok, ok, tc.line)
		if tc.ok {
			assert.Equal(t, tc.want, got, tc.line)
		}
	}
}
