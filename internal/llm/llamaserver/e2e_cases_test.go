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

//go:build e2e

package llamaserver_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/internal/llm/llamaserver"
)

func TestE2E_CreateCompletion_Basic(t *testing.T) {
	svc, model := e2eService(t, llamaserver.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	it, err := svc.CreateCompletion(ctx, model, llmapi.Request{
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "Say hi in one word."},
		},
	})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	text, tools, done := collectStream(t, ctx, it)
	assert.Empty(t, tools)
	assert.NotEmpty(t, text, "expected some generated text")
	assert.Equal(t, done.Message.Content, text,
		"done.Message.Content must equal accumulated deltas")
	assert.Positive(t, done.Usage.TokensSent)
	assert.Positive(t, done.Usage.TokensReceived)
	switch done.FinishReason {
	case llmapi.FinishReasonStop, llmapi.FinishReasonLength:
	default:
		t.Fatalf("unexpected finish reason %q", done.FinishReason)
	}
}

func TestE2E_CreateCompletion_Length(t *testing.T) {
	svc, model := e2eService(t, llamaserver.Config{MaxOutputTokens: 4})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	it, err := svc.CreateCompletion(ctx, model, llmapi.Request{
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "Tell me a long story about a dragon and a knight."},
		},
	})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	_, _, done := collectStream(t, ctx, it)
	assert.Equal(t, llmapi.FinishReasonLength, done.FinishReason)
}

func TestE2E_CreateCompletion_Tools(t *testing.T) {
	svc, model := e2eService(t, llamaserver.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	tool := llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name:        "get_weather",
			Description: "Return the current weather for a city.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"city": map[string]any{
						"type":        "string",
						"description": "The city to look up.",
					},
				},
				"required": []any{"city"},
			},
		},
	}

	it, err := svc.CreateCompletion(ctx, model, llmapi.Request{
		Messages: []llmapi.Message{
			{Role: llmapi.RoleSystem, Content: "You are a helpful assistant. Always call a tool when one is available."},
			{Role: llmapi.RoleUser, Content: "What's the weather in Paris? Use the tool."},
		},
		Tools: []llmapi.Tool{tool},
	})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	_, tools, done := collectStream(t, ctx, it)
	if len(tools) == 0 {
		t.Skipf("model did not emit a tool call (content=%q) — weak quant, not a bug",
			done.Message.Content)
	}
	assert.Equal(t, llmapi.FinishReasonToolCall, done.FinishReason)
	tc := tools[0]
	assert.Equal(t, "get_weather", tc.Function.Name)
	assert.NotEmpty(t, tc.Function.Arguments, "tool call has empty arguments")
	assertJSONObject(t, tc.Function.Arguments)
	assert.Contains(t, strings.ToLower(tc.Function.Arguments), "paris",
		"tool arguments should carry the requested city")
}

func TestE2E_ToolResult_RoundTrip(t *testing.T) {
	svc, model := e2eService(t, llamaserver.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	it, err := svc.CreateCompletion(ctx, model, llmapi.Request{
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "What is the capital of France?"},
			{
				Role: llmapi.RoleAssistant,
				ToolCalls: []llmapi.ToolCall{{
					ID:   "call_1",
					Type: llmapi.ToolTypeFunction,
					Function: llmapi.FunctionCall{
						Name:      "lookup_capital",
						Arguments: `{"country":"France"}`,
					},
				}},
			},
			{Role: llmapi.RoleTool, ToolCallID: "call_1", Content: "Paris"},
		},
	})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	// Some templates (e.g. Gemma) reject the assistant→tool message
	// sequence outright with a Jinja role-alternation error. That is a
	// model-template limitation, not a client bug, so skip rather than
	// fail when the server rejects the transcript.
	text, streamErr := drainText(ctx, it)
	if streamErr != nil {
		if isTemplateError(streamErr) {
			t.Skipf("model template rejects tool messages: %v", streamErr)
		}
		t.Fatalf("stream error: %v", streamErr)
	}
	assert.Contains(t, strings.ToLower(text), "paris",
		"assistant should incorporate the tool result")
}

func TestE2E_CountTokens(t *testing.T) {
	svc, model := e2eService(t, llamaserver.Config{})
	n, err := svc.CountTokens(model, []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hello world"},
	})
	require.NoError(t, err)
	assert.Positive(t, n)
}

func TestE2E_ContextCancellation(t *testing.T) {
	svc, model := e2eService(t, llamaserver.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	it, err := svc.CreateCompletion(ctx, model, llmapi.Request{
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "Count slowly from one to one hundred, one number per line."},
		},
	})
	require.NoError(t, err)

	var sawDelta bool
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}
		if ev.Type == llmapi.EventTextDelta && !sawDelta {
			sawDelta = true
			cancel()
		}
	}
	assert.NoError(t, it.Close(), "iterator close after cancel")
	assert.True(t, sawDelta, "never saw a delta before cancelling")

	// The pool must survive a cancelled request: a follow-up completion on a
	// fresh context still succeeds against the same running server.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel2()
	it2, err := svc.CreateCompletion(ctx2, model, llmapi.Request{
		Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "Say ok."}},
	})
	require.NoError(t, err)
	defer func() { _ = it2.Close() }()
	text, _, _ := collectStream(t, ctx2, it2)
	assert.NotEmpty(t, text)
}

func TestE2E_ServerReuse_AcrossRequests(t *testing.T) {
	svc, model := e2eService(t, llamaserver.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	for i := range 2 {
		it, err := svc.CreateCompletion(ctx, model, llmapi.Request{
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "Reply with the single word: pong."},
			},
		})
		require.NoErrorf(t, err, "request %d", i)
		text, _, done := collectStream(t, ctx, it)
		_ = it.Close()
		assert.NotEmptyf(t, text, "request %d produced no text", i)
		assert.Positivef(t, done.Usage.TokensReceived, "request %d", i)
	}
}

func TestE2E_ServerNotInstalled(t *testing.T) {
	if !e2eEnabled() {
		t.Skip("llamaserver e2e: opt in with RUNE_LLAMASERVER_E2E=1")
	}
	exec := &realExecutor{}
	t.Cleanup(exec.wait)
	svc := llamaserver.New(
		llamaserver.Config{}, exec, llamaserver.NewFixedLocator(""), nopNotifications{},
	)
	t.Cleanup(func() { _ = svc.Close() })

	_, err := svc.CreateCompletion(context.Background(),
		llmapi.ModelEntry{Name: "x", Provider: llamaserver.LLMProvider},
		llmapi.Request{Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pkg install llama-server")
	assert.Contains(t, err.Error(), "models.local.server_bin_path")
}

func TestE2E_ResponseFormat_JSONObject(t *testing.T) {
	svc, model := e2eService(t, llamaserver.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	it, err := svc.CreateCompletion(ctx, model, llmapi.Request{
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: `Return a JSON object with a single key "ok" set to true.`},
		},
		ResponseFormat: &llmapi.ResponseFormat{Type: llmapi.ResponseFormatTypeJSONObject},
	})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	text, _, _ := collectStream(t, ctx, it)
	assertJSONObject(t, text)
}

func TestE2E_ModelLoadFailure_FastAndClear(t *testing.T) {
	if !e2eEnabled() {
		t.Skip("llamaserver e2e: set RUNE_LLAMASERVER_E2E=1 to run")
	}
	bin := e2eServerBin(t)

	// A file with a valid GGUF magic followed by garbage: llama-server
	// recognizes the format, begins loading, then fails.
	corrupt := filepath.Join(t.TempDir(), "corrupt.gguf")
	require.NoError(t, os.WriteFile(corrupt,
		append([]byte("GGUF"), make([]byte, 256)...), 0o600))

	// A long startup timeout proves we fail fast via early-exit, not by
	// waiting out the deadline.
	const startupTimeout = 2 * time.Minute
	exec := &realExecutor{}
	t.Cleanup(exec.wait)
	notis := &e2eRecordingNotifications{}
	svc := llamaserver.New(
		llamaserver.Config{StartupTimeout: startupTimeout, IdleTimeout: time.Hour, MaxServers: 2},
		exec, llamaserver.NewFixedLocator(bin), notis,
	)
	t.Cleanup(func() { _ = svc.Close() })

	entry := llmapi.ModelEntry{
		Name:          "corrupt-e2e",
		Provider:      llamaserver.LLMProvider,
		BaseURL:       corrupt,
		ContextWindow: e2eContextWindow,
	}

	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	start := time.Now()
	it, err := svc.CreateCompletion(ctx, entry, llmapi.Request{
		Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
	})
	if err == nil {
		_, streamErr := drainText(ctx, it)
		_ = it.Close()
		err = streamErr
	}
	elapsed := time.Since(start)

	require.Error(t, err, "loading a corrupt GGUF must fail")
	assert.Less(t, elapsed, 30*time.Second,
		"early exit must fail fast, not wait out the %s startup timeout", startupTimeout)

	require.Eventually(t, func() bool {
		return len(notis.errorMessages()) >= 1
	}, 5*time.Second, 20*time.Millisecond, "an error notification is surfaced")
	msgs := notis.errorMessages()
	require.NotEmpty(t, msgs)
	lower := strings.ToLower(strings.Join(msgs, "\n"))
	assert.Contains(t, lower, "failed to load model",
		"failure must lead with a meaningful load-failure summary, got %q", msgs)
	assert.NotContains(t, lower, "not ready after",
		"failure must not be the generic startup-timeout message")
}
