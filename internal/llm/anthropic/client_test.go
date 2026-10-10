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

package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	ant "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/internal/llm/ratelimit"
)

// minimalSSEResponse returns a valid Anthropic SSE stream that completes
// with end_turn and the given text.
func minimalSSEResponse(text string) string {
	return fmt.Sprintf(`event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`, text)
}

// cacheControlJSON is the JSON shape we expect for cache_control fields.
type cacheControlJSON struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// systemBlockJSON represents a system text block with optional cache_control.
type systemBlockJSON struct {
	Text         string            `json:"text"`
	CacheControl *cacheControlJSON `json:"cache_control,omitempty"`
}

// toolBlockJSON represents a tool definition with optional cache_control.
type toolBlockJSON struct {
	Name         string            `json:"name"`
	CacheControl *cacheControlJSON `json:"cache_control,omitempty"`
}

// messageBlockJSON represents a content block within a message.
type messageBlockJSON struct {
	Type         string            `json:"type"`
	Text         string            `json:"text,omitempty"`
	CacheControl *cacheControlJSON `json:"cache_control,omitempty"`
}

// messageJSON represents a message with content blocks.
type messageJSON struct {
	Role    string             `json:"role"`
	Content []messageBlockJSON `json:"content"`
}

// requestBody is a minimal subset of the Anthropic request for assertions.
type requestBody struct {
	CacheControl *cacheControlJSON `json:"cache_control"`
	MaxTokens    int64             `json:"max_tokens"`
	System       []systemBlockJSON `json:"system"`
	Tools        []toolBlockJSON   `json:"tools"`
	Messages     []messageJSON     `json:"messages"`
}

func TestMaxOutputTokensOverridesConfig(t *testing.T) {
	var captured requestBody

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &captured))

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, minimalSSEResponse("ok"))
	}))
	defer srv.Close()

	c := NewClient("test-key", Config{
		BaseURL:   srv.URL,
		MaxTokens: 1024,
	})

	it, err := c.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "claude-test", ContextWindow: 0}, llmapi.Request{
		Messages:        []llmapi.Message{{Role: llmapi.RoleUser, Content: "Hello"}},
		MaxOutputTokens: 8192,
	})
	require.NoError(t, err)
	for {
		_, ok := it.Next(context.Background())
		if !ok {
			break
		}
	}
	require.NoError(t, it.Err())
	require.NoError(t, it.Close())

	assert.Equal(t, int64(8192), captured.MaxTokens)
}

func TestEffortWarningProvenance(t *testing.T) {
	// Claude 3 models do not support the effort parameter at all.
	model := llmapi.ModelEntry{Name: "claude-3-5-haiku-20241022"}

	newServer := func(t *testing.T) string {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, minimalSSEResponse("ok"))
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	drain := func(t *testing.T, cfg Config, req llmapi.Request) []llmapi.Event {
		t.Helper()
		cfg.BaseURL = newServer(t)
		it, err := NewClient("test-key", cfg).CreateCompletion(context.Background(), model, req)
		require.NoError(t, err)
		defer func() { _ = it.Close() }()
		var events []llmapi.Event
		for {
			ev, ok := it.Next(context.Background())
			if !ok {
				break
			}
			events = append(events, ev)
		}
		return events
	}

	t.Run("request effort warns", func(t *testing.T) {
		events := drain(t, Config{}, llmapi.Request{
			Messages:        []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
			ReasoningEffort: llmapi.ReasoningEffortHigh,
		})
		require.NotEmpty(t, events)
		assert.Equal(t, llmapi.EventRateLimitWarning, events[0].Type)
	})

	t.Run("config effort is silent", func(t *testing.T) {
		events := drain(t, Config{ReasoningEffort: "high"}, llmapi.Request{
			Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
		})
		for _, ev := range events {
			assert.NotEqual(t, llmapi.EventRateLimitWarning, ev.Type)
		}
	})
}

func TestNewClientOAuthBearerAuth(t *testing.T) {
	tests := []struct {
		name       string
		oauthToken string
		wantAuth   string
		wantAPIKey string
		wantBeta   string
	}{
		{
			name:       "oauth bearer suppresses x-api-key",
			oauthToken: "oauth-access-token",
			wantAuth:   "Bearer oauth-access-token",
			wantAPIKey: "",
			wantBeta:   "oauth-2025-04-20",
		},
		{
			name:       "legacy api key path unchanged",
			oauthToken: "",
			wantAuth:   "",
			wantAPIKey: "test-key",
			wantBeta:   "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				gotAuth   string
				gotAPIKey string
				gotBeta   string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				gotAPIKey = r.Header.Get("x-api-key")
				gotBeta = r.Header.Get("anthropic-beta")
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, minimalSSEResponse("ok"))
			}))
			defer srv.Close()

			cfg := Config{BaseURL: srv.URL, MaxTokens: 1024}
			if tt.oauthToken != "" {
				cfg.OAuthToken = tt.oauthToken
				cfg.Headers = map[string]string{"anthropic-beta": tt.wantBeta}
			}
			c := NewClient("test-key", cfg)

			it, err := c.CreateCompletion(context.Background(),
				llmapi.ModelEntry{Name: "claude-test"},
				llmapi.Request{Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "Hello"}}})
			require.NoError(t, err)
			for {
				if _, ok := it.Next(context.Background()); !ok {
					break
				}
			}
			require.NoError(t, it.Err())
			require.NoError(t, it.Close())

			assert.Equal(t, tt.wantAuth, gotAuth)
			assert.Equal(t, tt.wantAPIKey, gotAPIKey)
			assert.Equal(t, tt.wantBeta, gotBeta)
		})
	}
}

func TestCreateCompletion_NormalizedReplayDoesNotEmitUnpairedToolUse(t *testing.T) {
	var captured requestBody

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(body, &captured))

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, minimalSSEResponse("ok"))
	}))
	defer srv.Close()

	c := NewClient("test-key", Config{
		BaseURL: srv.URL,
	})

	// This is the provider-facing shape normalizeMessages should produce
	// for a stored replay where an assistant tool call was separated from a
	// matching result by another user turn: keep the assistant text, but
	// strip both the non-adjacent tool_use and late tool_result.
	it, err := c.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "claude-test", ContextWindow: 0}, llmapi.Request{
		Messages: []llmapi.Message{
			{Role: llmapi.RoleSystem, Content: "sys"},
			{Role: llmapi.RoleUser, Content: "inspect"},
			{Role: llmapi.RoleAssistant, Content: "I'll inspect."},
			{Role: llmapi.RoleUser, Content: "continue instead"},
			{Role: llmapi.RoleUser, Content: "continue"},
		},
	})
	require.NoError(t, err)
	for {
		_, ok := it.Next(context.Background())
		if !ok {
			break
		}
	}
	require.NoError(t, it.Err())
	require.NoError(t, it.Close())

	for _, msg := range captured.Messages {
		for _, block := range msg.Content {
			assert.NotEqual(t, "tool_use", block.Type,
				"normalized replay must not emit tool_use blocks without immediate tool_result blocks")
			assert.NotEqual(t, "tool_result", block.Type,
				"normalized replay must not emit late tool_result blocks")
		}
	}
}

func TestCacheBreakpoints(t *testing.T) {
	tests := []struct {
		name         string
		cacheControl string
		messages     []llmapi.Message
		tools        []llmapi.Tool
		assert       func(t *testing.T, body requestBody)
	}{
		{
			name:         "ephemeral uses default 5m TTL on system block",
			cacheControl: "ephemeral",
			messages: []llmapi.Message{
				{Role: llmapi.RoleSystem, Content: "You are helpful."},
				{Role: llmapi.RoleUser, Content: "Hello"},
			},
			assert: func(t *testing.T, body requestBody) {
				assert.Nil(t, body.CacheControl, "top-level cache_control should not be set")
				require.Len(t, body.System, 1)
				require.NotNil(t, body.System[0].CacheControl, "system block should have cache_control")
				assert.Equal(t, "ephemeral", body.System[0].CacheControl.Type)
				assert.Empty(t, body.System[0].CacheControl.TTL, "TTL should use API default (5m)")
			},
		},
		{
			name:         "1h sets explicit 1h TTL on system block",
			cacheControl: "1h",
			messages: []llmapi.Message{
				{Role: llmapi.RoleSystem, Content: "You are helpful."},
				{Role: llmapi.RoleUser, Content: "Hello"},
			},
			assert: func(t *testing.T, body requestBody) {
				assert.Nil(t, body.CacheControl, "top-level cache_control should not be set")
				require.Len(t, body.System, 1)
				require.NotNil(t, body.System[0].CacheControl)
				assert.Equal(t, "1h", body.System[0].CacheControl.TTL)
			},
		},
		{
			name:         "5m sets explicit 5m TTL on system block",
			cacheControl: "5m",
			messages: []llmapi.Message{
				{Role: llmapi.RoleSystem, Content: "You are helpful."},
				{Role: llmapi.RoleUser, Content: "Hello"},
			},
			assert: func(t *testing.T, body requestBody) {
				assert.Nil(t, body.CacheControl, "top-level cache_control should not be set")
				require.Len(t, body.System, 1)
				require.NotNil(t, body.System[0].CacheControl)
				assert.Equal(t, "5m", body.System[0].CacheControl.TTL)
			},
		},
		{
			name:         "empty string defaults to 5m",
			cacheControl: "",
			messages: []llmapi.Message{
				{Role: llmapi.RoleSystem, Content: "You are helpful."},
				{Role: llmapi.RoleUser, Content: "Hello"},
			},
			assert: func(t *testing.T, body requestBody) {
				assert.Nil(t, body.CacheControl, "top-level cache_control should not be set")
				require.Len(t, body.System, 1)
				require.NotNil(t, body.System[0].CacheControl)
				assert.Equal(t, "5m", body.System[0].CacheControl.TTL)
			},
		},
		{
			name:         "cache_control set on last tool definition",
			cacheControl: "5m",
			messages: []llmapi.Message{
				{Role: llmapi.RoleSystem, Content: "You are helpful."},
				{Role: llmapi.RoleUser, Content: "Hello"},
			},
			tools: []llmapi.Tool{
				{Function: llmapi.FunctionDefinition{Name: "tool_a", Parameters: map[string]any{"properties": map[string]any{}}}},
				{Function: llmapi.FunctionDefinition{Name: "tool_b", Parameters: map[string]any{"properties": map[string]any{}}}},
			},
			assert: func(t *testing.T, body requestBody) {
				require.Len(t, body.Tools, 2)
				assert.Nil(t, body.Tools[0].CacheControl, "first tool should not have cache_control")
				require.NotNil(t, body.Tools[1].CacheControl, "last tool should have cache_control")
				assert.Equal(t, "ephemeral", body.Tools[1].CacheControl.Type)
			},
		},
		{
			name:         "cache_control set on conversation prefix boundary",
			cacheControl: "5m",
			messages: []llmapi.Message{
				{Role: llmapi.RoleSystem, Content: "You are helpful."},
				{Role: llmapi.RoleUser, Content: "First message"},
				{Role: llmapi.RoleAssistant, Content: "First response"},
				{Role: llmapi.RoleUser, Content: "Second message"},
			},
			assert: func(t *testing.T, body requestBody) {
				// Messages: [user:"First message", assistant:"First response", user:"Second message"]
				// Second-to-last = assistant:"First response"
				require.Len(t, body.Messages, 3)
				lastBlock := body.Messages[1].Content[len(body.Messages[1].Content)-1]
				require.NotNil(t, lastBlock.CacheControl,
					"second-to-last message should have cache_control on its last block")
				assert.Equal(t, "ephemeral", lastBlock.CacheControl.Type)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured requestBody

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(body, &captured))

				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, minimalSSEResponse("ok"))
			}))
			defer srv.Close()

			c := &client{
				config: Config{
					MaxTokens:    1024,
					CacheControl: tt.cacheControl,
				},
				anthropic: ant.NewClient(
					option.WithAPIKey("test-key"),
					option.WithBaseURL(srv.URL),
					option.WithMaxRetries(0),
				),
			}

			it, err := c.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "claude-test", ContextWindow: 0}, llmapi.Request{
				Messages: tt.messages,
				Tools:    tt.tools,
			})
			require.NoError(t, err)
			defer func() { _ = it.Close() }()

			// Drain the stream.
			for {
				if _, ok := it.Next(context.Background()); !ok {
					break
				}
			}

			tt.assert(t, captured)
		})
	}
}

func TestTokensSentIncludesCacheTokens(t *testing.T) {
	tests := []struct {
		name          string
		inputTokens   int
		cacheRead     int
		cacheCreation int
		wantSent      int
		wantCached    int
		wantCreated   int
	}{
		{
			name:          "no caching",
			inputTokens:   500,
			cacheRead:     0,
			cacheCreation: 0,
			wantSent:      500,
			wantCached:    0,
			wantCreated:   0,
		},
		{
			name:          "cache creation only",
			inputTokens:   6,
			cacheRead:     0,
			cacheCreation: 12000,
			wantSent:      12006,
			wantCached:    0,
			wantCreated:   12000,
		},
		{
			name:          "cache read only",
			inputTokens:   2,
			cacheRead:     8000,
			cacheCreation: 0,
			wantSent:      8002,
			wantCached:    8000,
			wantCreated:   0,
		},
		{
			name:          "all three buckets",
			inputTokens:   10,
			cacheRead:     5000,
			cacheCreation: 3000,
			wantSent:      8010,
			wantCached:    5000,
			wantCreated:   3000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sse := fmt.Sprintf(`event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":1,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`, tt.inputTokens, tt.cacheCreation, tt.cacheRead)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, sse)
			}))
			t.Cleanup(srv.Close)

			c := &client{
				config: Config{MaxTokens: 1024},
				anthropic: ant.NewClient(
					option.WithAPIKey("test-key"),
					option.WithBaseURL(srv.URL),
					option.WithMaxRetries(0),
				),
			}

			it, err := c.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "claude-test", ContextWindow: 0}, llmapi.Request{
				Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hello"}},
			})
			require.NoError(t, err)
			defer func() { _ = it.Close() }()

			var usage llmapi.Usage
			for {
				ev, ok := it.Next(context.Background())
				if !ok {
					break
				}
				if ev.Type == llmapi.EventStreamDone {
					usage = ev.DoneData.Usage
				}
			}
			require.NoError(t, it.Err())

			assert.Equal(t, tt.wantSent, usage.TokensSent, "TokensSent should be sum of all input token buckets")
			assert.Equal(t, tt.wantCached, usage.TokensCached, "TokensCached should be cache_read_input_tokens")
			assert.Equal(t, tt.wantCreated, usage.TokensCacheCreated, "TokensCacheCreated should be cache_creation_input_tokens")
		})
	}
}

// sseResponseWithCache returns a valid Anthropic SSE stream with explicit
// cache token counts in the usage payload.
func sseResponseWithCache(text string, inputTokens, cacheCreation, cacheRead int) string {
	return fmt.Sprintf(`event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":%d,"output_tokens":1,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`, inputTokens, cacheCreation, cacheRead, text)
}

func TestPromptCaching(t *testing.T) {
	// Simulate two sequential requests: the first creates a cache entry,
	// the second reads from it. The server returns different usage payloads
	// for each request to model real Anthropic caching behaviour.
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			// First request: 6 uncached tokens, 10000 tokens written to cache.
			_, _ = fmt.Fprint(w, sseResponseWithCache("ok", 6, 10000, 0))
		} else {
			// Second request: 6 uncached tokens, cache hit for 10000.
			_, _ = fmt.Fprint(w, sseResponseWithCache("ok", 6, 0, 10000))
		}
	}))
	t.Cleanup(srv.Close)

	c := &client{
		config: Config{MaxTokens: 1024},
		anthropic: ant.NewClient(
			option.WithAPIKey("test-key"),
			option.WithBaseURL(srv.URL),
			option.WithMaxRetries(0),
		),
	}

	req := llmapi.Request{
		Messages: []llmapi.Message{
			{Role: llmapi.RoleSystem, Content: "You are helpful."},
			{Role: llmapi.RoleUser, Content: "Hello"},
		},
	}

	// First request: creates cache.
	it1, err := c.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "claude-test", ContextWindow: 0}, req)
	require.NoError(t, err)
	var usage1 llmapi.Usage
	for {
		ev, ok := it1.Next(context.Background())
		if !ok {
			break
		}
		if ev.Type == llmapi.EventStreamDone {
			usage1 = ev.DoneData.Usage
		}
	}
	require.NoError(t, it1.Err())
	_ = it1.Close()

	assert.Equal(t, 10006, usage1.TokensSent, "first request: TokensSent = uncached + cache_created")
	assert.Equal(t, 0, usage1.TokensCached, "first request: no cache reads")
	assert.Equal(t, 10000, usage1.TokensCacheCreated, "first request: tokens written to cache")

	// Second request: reads from cache.
	it2, err := c.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "claude-test", ContextWindow: 0}, req)
	require.NoError(t, err)
	var usage2 llmapi.Usage
	for {
		ev, ok := it2.Next(context.Background())
		if !ok {
			break
		}
		if ev.Type == llmapi.EventStreamDone {
			usage2 = ev.DoneData.Usage
		}
	}
	require.NoError(t, it2.Err())
	_ = it2.Close()

	assert.Equal(t, 10006, usage2.TokensSent, "second request: TokensSent = uncached + cached")
	assert.Equal(t, 10000, usage2.TokensCached, "second request: cache hit tokens")
	assert.Equal(t, 0, usage2.TokensCacheCreated, "second request: no new cache entries")
}

// partialSSEWithOverloadError returns SSE events that start normally
// then emit an overloaded_error event mid-stream.
func partialSSEWithOverloadError() string {
	return `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"test","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}

event: error
data: {"type":"error","error":{"details":null,"type":"overloaded_error","message":"Overloaded"},"request_id":"req_test123"}

`
}

func TestMidStreamOverloadRetry(t *testing.T) {
	t.Run("retries on overloaded_error mid-stream then succeeds", func(t *testing.T) {
		var attempts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := attempts.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if n == 1 {
				_, _ = w.Write([]byte(partialSSEWithOverloadError()))
				return
			}
			_, _ = w.Write([]byte(minimalSSEResponse("hello")))
		}))
		t.Cleanup(srv.Close)

		c := &client{
			config: Config{
				MaxTokens: 1024,
			},
			anthropic: ant.NewClient(
				option.WithAPIKey("test-key"),
				option.WithBaseURL(srv.URL),
				option.WithMaxRetries(0),
			),
		}

		ctx := context.Background()
		it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: "claude-test", ContextWindow: 0}, llmapi.Request{
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "hello"},
			},
		})
		require.NoError(t, err)

		var gotReset, gotWarning, gotDone bool
		var textChunks []string
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventStreamReset:
				gotReset = true
			case llmapi.EventRateLimitWarning:
				gotWarning = true
				assert.Contains(t, ev.RateLimit.Message, "overloaded_error")
			case llmapi.EventTextDelta:
				textChunks = append(textChunks, ev.Text)
			case llmapi.EventStreamDone:
				gotDone = true
			}
		}
		require.NoError(t, it.Err())
		_ = it.Close()

		assert.Equal(t, int32(2), attempts.Load(), "should have made 2 attempts")
		assert.True(t, gotReset, "should have received stream reset event")
		assert.True(t, gotWarning, "should have received retry warning")
		assert.True(t, gotDone, "should have received done event")
		assert.Contains(t, textChunks, "hello", "should have text from successful stream")
	})

	t.Run("exhausts mid-stream retries on persistent overload", func(t *testing.T) {
		var attempts atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(partialSSEWithOverloadError()))
		}))
		t.Cleanup(srv.Close)

		c := &client{
			config: Config{
				MaxTokens: 1024,
			},
			anthropic: ant.NewClient(
				option.WithAPIKey("test-key"),
				option.WithBaseURL(srv.URL),
				option.WithMaxRetries(0),
			),
		}

		ctx := context.Background()
		it, err := c.CreateCompletion(ctx, llmapi.ModelEntry{Name: "claude-test", ContextWindow: 0}, llmapi.Request{
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "hello"},
			},
		})
		require.NoError(t, err)

		var resets, warnings int
		var gotError bool
		for {
			ev, ok := it.Next(ctx)
			if !ok {
				break
			}
			switch ev.Type {
			case llmapi.EventStreamReset:
				resets++
			case llmapi.EventRateLimitWarning:
				warnings++
			case llmapi.EventStreamError:
				gotError = true
			}
		}

		assert.Equal(t, int32(ratelimit.MaxMidStreamRetries+1), attempts.Load(),
			"should exhaust all mid-stream retries")
		assert.Equal(t, ratelimit.MaxMidStreamRetries, resets,
			"should emit one reset per retry")
		assert.Equal(t, ratelimit.MaxMidStreamRetries, warnings,
			"should emit one warning per retry")
		assert.True(t, gotError, "should surface error after exhausting retries")
	})
}

func TestAssistantContentBlocks_EmptyMessage(t *testing.T) {
	// An assistant message with no content and no tool calls must not produce
	// an empty text block — the Anthropic API rejects those with:
	// "text content blocks must be non-empty".
	blocks := assistantContentBlocks(llmapi.Message{
		Role:    llmapi.RoleAssistant,
		Content: "",
	})
	for _, b := range blocks {
		if b.OfText != nil {
			assert.NotEmpty(t, b.OfText.Text, "text content block must not be empty")
		}
	}
}

func TestAssistantContentBlocks_ThinkingFirst(t *testing.T) {
	// A thinking block must be replayed before tool_use so the assistant turn
	// preceding a tool_result starts with thinking (Anthropic requires this).
	blocks := assistantContentBlocks(llmapi.Message{
		Role:    llmapi.RoleAssistant,
		Content: "calling a tool",
		ReasoningBlocks: []llmapi.ReasoningBlock{
			{Kind: "thinking", Text: "I should read the file", Signature: "sig-1"},
		},
		ToolCalls: []llmapi.ToolCall{
			{ID: "call-1", Type: llmapi.ToolTypeFunction, Function: llmapi.FunctionCall{Name: "read", Arguments: `{"path":"x"}`}},
		},
	})

	require.GreaterOrEqual(t, len(blocks), 3)
	require.NotNil(t, blocks[0].OfThinking, "first block must be thinking")
	assert.Equal(t, "sig-1", blocks[0].OfThinking.Signature)
	assert.Equal(t, "I should read the file", blocks[0].OfThinking.Thinking)
	require.NotNil(t, blocks[1].OfText, "text must follow thinking")
	assert.Equal(t, "calling a tool", blocks[1].OfText.Text)
	require.NotNil(t, blocks[2].OfToolUse, "tool_use must come after thinking and text")
}

func TestAssistantContentBlocks_RedactedThinkingReplayedAsIs(t *testing.T) {
	blocks := assistantContentBlocks(llmapi.Message{
		Role: llmapi.RoleAssistant,
		ReasoningBlocks: []llmapi.ReasoningBlock{
			{Kind: "redacted", Data: "encrypted-blob"},
			{Kind: "thinking", Text: "visible reasoning", Signature: "sig-2"},
		},
		Content: "answer",
	})

	require.Len(t, blocks, 3)
	require.NotNil(t, blocks[0].OfRedactedThinking)
	assert.Equal(t, "encrypted-blob", blocks[0].OfRedactedThinking.Data)
	require.NotNil(t, blocks[1].OfThinking)
	assert.Equal(t, "sig-2", blocks[1].OfThinking.Signature)
	require.NotNil(t, blocks[2].OfText)
}

func TestAssistantContentBlocks_ReasoningOnlyYieldsThinkingBlock(t *testing.T) {
	// A reasoning-only turn (no text, no tool calls) must still produce a
	// non-empty content block so the API does not reject it with
	// "text content blocks must be non-empty".
	blocks := assistantContentBlocks(llmapi.Message{
		Role: llmapi.RoleAssistant,
		ReasoningBlocks: []llmapi.ReasoningBlock{
			{Kind: "thinking", Text: "just thinking", Signature: "sig-3"},
		},
	})

	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].OfThinking)
	assert.Equal(t, "sig-3", blocks[0].OfThinking.Signature)
	for _, b := range blocks {
		assert.Nil(t, b.OfText, "reasoning-only turn must not emit a text block")
	}
}

func TestAssistantContentBlocks_UnsignedReasoningSkipped(t *testing.T) {
	// Legacy/unsigned reasoning cannot be replayed (the API rejects unsigned
	// thinking), so no thinking block is emitted. With visible content the
	// turn is still valid; with none it yields zero blocks and must be
	// dropped upstream rather than sent.
	withText := assistantContentBlocks(llmapi.Message{
		Role:    llmapi.RoleAssistant,
		Content: "the answer",
		ReasoningBlocks: []llmapi.ReasoningBlock{
			{Kind: "thinking", Text: "unsigned thought", Signature: ""},
		},
	})
	for _, b := range withText {
		assert.Nil(t, b.OfThinking, "unsigned thinking must not be replayed")
	}
	require.Len(t, withText, 1)
	require.NotNil(t, withText[0].OfText)

	reasoningOnly := assistantContentBlocks(llmapi.Message{
		Role: llmapi.RoleAssistant,
		ReasoningBlocks: []llmapi.ReasoningBlock{
			{Kind: "thinking", Text: "unsigned thought", Signature: ""},
		},
	})
	assert.Empty(t, reasoningOnly, "unsigned reasoning-only turn yields no replayable blocks")
}

// noEmptyTextBlock asserts that no text content block in blocks is empty.
// Newer Claude models reject requests containing an empty text block with
// "text content blocks must be non-empty".
func noEmptyTextBlock(t *testing.T, blocks []ant.ContentBlockParamUnion) {
	t.Helper()
	for _, b := range blocks {
		if b.OfText != nil {
			assert.NotEmpty(t, b.OfText.Text, "text content block must not be empty")
		}
	}
}

func TestUserContentBlocks_ImageWithEmptyTextPart(t *testing.T) {
	// Image sessions carry an image part alongside a text summary in
	// MultiContent. If the text part is empty (e.g. stripped upstream), the
	// image block must still be emitted without an accompanying empty text
	// block.
	blocks := userContentBlocks(llmapi.Message{
		Role: llmapi.RoleUser,
		MultiContent: []llmapi.ContentPart{
			{Type: llmapi.ContentPartTypeText, Text: ""},
			{Type: llmapi.ContentPartTypeImageURL, ImageURL: "data:image/png;base64,AAAA"},
		},
	})

	noEmptyTextBlock(t, blocks)
	require.Len(t, blocks, 1, "empty text part must be dropped, image kept")
	require.NotNil(t, blocks[0].OfImage, "image block must be preserved")
}

func TestUserContentBlocks_WhitespaceOnlyTextPartDropped(t *testing.T) {
	blocks := userContentBlocks(llmapi.Message{
		Role: llmapi.RoleUser,
		MultiContent: []llmapi.ContentPart{
			{Type: llmapi.ContentPartTypeText, Text: "   \n\t "},
			{Type: llmapi.ContentPartTypeImageURL, ImageURL: "data:image/png;base64,AAAA"},
		},
	})

	noEmptyTextBlock(t, blocks)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].OfImage)
}

func TestUserContentBlocks_ImageOnly(t *testing.T) {
	blocks := userContentBlocks(llmapi.Message{
		Role: llmapi.RoleUser,
		MultiContent: []llmapi.ContentPart{
			{Type: llmapi.ContentPartTypeImageURL, ImageURL: "data:image/png;base64,AAAA"},
		},
	})

	noEmptyTextBlock(t, blocks)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].OfImage)
}

func TestUserContentBlocks_AllEmptyTextPartsYieldSentinel(t *testing.T) {
	// When every MultiContent part is empty text (no image), the per-part skip
	// empties the loop, so the message-level sentinel must keep the turn
	// present rather than emitting an empty content array.
	blocks := userContentBlocks(llmapi.Message{
		Role: llmapi.RoleUser,
		MultiContent: []llmapi.ContentPart{
			{Type: llmapi.ContentPartTypeText, Text: ""},
			{Type: llmapi.ContentPartTypeText, Text: "   "},
		},
	})

	noEmptyTextBlock(t, blocks)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].OfText)
	assert.Equal(t, "(no content)", blocks[0].OfText.Text)
}

func TestUserContentBlocks_EmptyContentNoMultiContent(t *testing.T) {
	// A user message with empty Content and no MultiContent must keep the turn
	// present with a non-empty sentinel block: the API rejects both an empty
	// text block and a trailing non-user message.
	blocks := userContentBlocks(llmapi.Message{
		Role:    llmapi.RoleUser,
		Content: "",
	})

	noEmptyTextBlock(t, blocks)
	require.Len(t, blocks, 1)
	require.NotNil(t, blocks[0].OfText)
	assert.Equal(t, "(no content)", blocks[0].OfText.Text)
}

func TestUserContentBlocks_TextPreserved(t *testing.T) {
	blocks := userContentBlocks(llmapi.Message{
		Role: llmapi.RoleUser,
		MultiContent: []llmapi.ContentPart{
			{Type: llmapi.ContentPartTypeText, Text: "describe this"},
			{Type: llmapi.ContentPartTypeImageURL, ImageURL: "data:image/png;base64,AAAA"},
		},
	})

	require.Len(t, blocks, 2)
	require.NotNil(t, blocks[0].OfText)
	assert.Equal(t, "describe this", blocks[0].OfText.Text)
	require.NotNil(t, blocks[1].OfImage)
}

func TestConvertMessages_EmptyUserMessageKeptWithSentinel(t *testing.T) {
	// A blank trailing user message must be preserved as a non-empty user turn
	// so the request neither sends an empty text block nor ends on a non-user
	// message — both are rejected by the API.
	_, msgs := convertMessages([]llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hello"},
		{Role: llmapi.RoleAssistant, Content: "hi"},
		{Role: llmapi.RoleUser, Content: "   "},
	})

	require.Len(t, msgs, 3, "blank trailing user message must be kept")
	assert.Equal(t, ant.MessageParamRoleUser, msgs[2].Role,
		"request must still end with a user message")
	require.Len(t, msgs[2].Content, 1)
	require.NotNil(t, msgs[2].Content[0].OfText)
	assert.Equal(t, "(no content)", msgs[2].Content[0].OfText.Text)
}
