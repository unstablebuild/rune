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

package llmrpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi/llmrpc"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestServerClientIntegration(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(*mockService)
		action func(t *testing.T, mock *mockService, client llmapi.Service)
	}{
		{
			name: "CreateCompletion routes model and request",
			setup: func(m *mockService) {
				m.events = []llmapi.Event{{Type: llmapi.EventTextDelta, Text: "hello"}}
			},
			action: func(t *testing.T, mock *mockService, client llmapi.Service) {
				req := llmapi.Request{
					Messages:        []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
					ReasoningEffort: llmapi.ReasoningEffortHigh,
					MaxOutputTokens: 256,
					PromptCacheKey:  "dlg-1",
					TokenCount:      42,
				}
				model := llmapi.ModelEntry{
					Name:          "gpt-4o",
					Provider:      "openai",
					ContextWindow: 128_000,
					BaseURL:       "https://api.openai.example",
				}
				it, err := client.CreateCompletion(context.Background(), model, req)
				require.NoError(t, err)
				_ = drainEvents(t, it)

				mock.mu.Lock()
				defer mock.mu.Unlock()
				assert.Equal(t, model, mock.lastCompletionModel)
				assert.Equal(t, llmapi.ReasoningEffortHigh, mock.lastCompletionReq.ReasoningEffort)
				assert.Equal(t, 256, mock.lastCompletionReq.MaxOutputTokens)
				assert.Equal(t, "dlg-1", mock.lastCompletionReq.PromptCacheKey)
				assert.Equal(t, 42, mock.lastCompletionReq.TokenCount)
				require.Len(t, mock.lastCompletionReq.Messages, 1)
				assert.Equal(t, llmapi.RoleUser, mock.lastCompletionReq.Messages[0].Role)
				assert.Equal(t, "hi", mock.lastCompletionReq.Messages[0].Content)
			},
		},
		{
			// Regression for RUNE-251: an accumulated history whose unary
			// protobuf encoding exceeds gRPC's 4 MiB per-message receive limit
			// (which previously failed with ResourceExhausted) must now
			// round-trip because each Message is streamed as its own small frame.
			name: "many messages exceeding 4 MiB round-trip in order",
			setup: func(m *mockService) {
				m.events = []llmapi.Event{{Type: llmapi.EventTextDelta, Text: "ok"}}
			},
			action: func(t *testing.T, mock *mockService, client llmapi.Service) {
				const (
					perMsg  = 200 * 1024
					numMsgs = 30 // ~6 MiB total, well over the 4 MiB unary cap
				)
				msgs := make([]llmapi.Message, numMsgs)
				for i := range msgs {
					msgs[i] = llmapi.Message{
						Role:    llmapi.RoleUser,
						Content: strings.Repeat(string(rune('a'+i%26)), perMsg),
					}
				}
				req := llmapi.Request{Messages: msgs}
				it, err := client.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "m"}, req)
				require.NoError(t, err)
				_ = drainEvents(t, it)

				mock.mu.Lock()
				defer mock.mu.Unlock()
				require.Len(t, mock.lastCompletionReq.Messages, numMsgs)
				for i := range msgs {
					assert.Equal(t, msgs[i].Content, mock.lastCompletionReq.Messages[i].Content,
						"message %d must arrive byte-identical and in order", i)
				}
			},
		},
		{
			name: "text and reasoning deltas round-trip",
			setup: func(m *mockService) {
				m.events = []llmapi.Event{
					{Type: llmapi.EventReasoningDelta, Reasoning: "thinking..."},
					{Type: llmapi.EventTextDelta, Text: "hello "},
					{Type: llmapi.EventTextDelta, Text: "world"},
				}
			},
			action: func(t *testing.T, _ *mockService, client llmapi.Service) {
				it, err := client.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "m"}, llmapi.Request{})
				require.NoError(t, err)
				evs := drainEvents(t, it)
				require.Len(t, evs, 3)
				assert.Equal(t, llmapi.EventReasoningDelta, evs[0].Type)
				assert.Equal(t, "thinking...", evs[0].Reasoning)
				assert.Equal(t, llmapi.EventTextDelta, evs[1].Type)
				assert.Equal(t, "hello ", evs[1].Text)
				assert.Equal(t, "world", evs[2].Text)
			},
		},
		{
			name: "tool call done round-trip",
			setup: func(m *mockService) {
				m.events = []llmapi.Event{
					{
						Type: llmapi.EventToolCallDone,
						ToolCall: &llmapi.ToolCall{
							ID:   "call-1",
							Type: llmapi.ToolTypeFunction,
							Function: llmapi.FunctionCall{
								Name:      "do_thing",
								Arguments: `{"x":1,"y":"two"}`,
							},
						},
					},
				}
			},
			action: func(t *testing.T, _ *mockService, client llmapi.Service) {
				it, err := client.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "m"}, llmapi.Request{})
				require.NoError(t, err)
				evs := drainEvents(t, it)
				require.Len(t, evs, 1)
				require.NotNil(t, evs[0].ToolCall)
				assert.Equal(t, "call-1", evs[0].ToolCall.ID)
				assert.Equal(t, llmapi.ToolTypeFunction, evs[0].ToolCall.Type)
				assert.Equal(t, "do_thing", evs[0].ToolCall.Function.Name)
				assert.Equal(t, `{"x":1,"y":"two"}`, evs[0].ToolCall.Function.Arguments)
			},
		},
		{
			name: "stream done carries usage finish-reason and message",
			setup: func(m *mockService) {
				m.events = []llmapi.Event{{
					Type: llmapi.EventStreamDone,
					DoneData: &llmapi.DoneData{
						Message: llmapi.Message{
							Role:    llmapi.RoleAssistant,
							Content: "done",
						},
						FinishReason: llmapi.FinishReasonStop,
						Usage: llmapi.Usage{
							TokensSent:         100,
							TokensReceived:     50,
							TokensReasoned:     10,
							TokensCached:       5,
							TokensCacheCreated: 1,
						},
					},
				}}
			},
			action: func(t *testing.T, _ *mockService, client llmapi.Service) {
				it, err := client.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "m"}, llmapi.Request{})
				require.NoError(t, err)
				evs := drainEvents(t, it)
				require.Len(t, evs, 1)
				require.NotNil(t, evs[0].DoneData)
				dd := evs[0].DoneData
				assert.Equal(t, llmapi.RoleAssistant, dd.Message.Role)
				assert.Equal(t, "done", dd.Message.Content)
				assert.Equal(t, llmapi.FinishReasonStop, dd.FinishReason)
				assert.Equal(t, 100, dd.Usage.TokensSent)
				assert.Equal(t, 50, dd.Usage.TokensReceived)
				assert.Equal(t, 10, dd.Usage.TokensReasoned)
				assert.Equal(t, 5, dd.Usage.TokensCached)
				assert.Equal(t, 1, dd.Usage.TokensCacheCreated)
			},
		},
		{
			name: "stream reset event",
			setup: func(m *mockService) {
				m.events = []llmapi.Event{{Type: llmapi.EventStreamReset}}
			},
			action: func(t *testing.T, _ *mockService, client llmapi.Service) {
				it, err := client.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "m"}, llmapi.Request{})
				require.NoError(t, err)
				evs := drainEvents(t, it)
				require.Len(t, evs, 1)
				assert.Equal(t, llmapi.EventStreamReset, evs[0].Type)
			},
		},
		{
			name: "rate limit warning carries wait duration and message",
			setup: func(m *mockService) {
				m.events = []llmapi.Event{{
					Type: llmapi.EventRateLimitWarning,
					RateLimit: &llmapi.RateLimitInfo{
						WaitDuration: 2500 * time.Millisecond,
						Message:      "slow down",
					},
				}}
			},
			action: func(t *testing.T, _ *mockService, client llmapi.Service) {
				it, err := client.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "m"}, llmapi.Request{})
				require.NoError(t, err)
				evs := drainEvents(t, it)
				require.Len(t, evs, 1)
				require.NotNil(t, evs[0].RateLimit)
				assert.Equal(t, 2500*time.Millisecond, evs[0].RateLimit.WaitDuration)
				assert.Equal(t, "slow down", evs[0].RateLimit.Message)
			},
		},
		{
			name: "request fields round-trip with multi-content message tool and response format",
			setup: func(m *mockService) {
				m.events = nil
			},
			action: func(t *testing.T, mock *mockService, client llmapi.Service) {
				schema := schemaMarshaler{payload: `{"type":"object"}`}
				req := llmapi.Request{
					Messages: []llmapi.Message{
						{
							Role:    llmapi.RoleUser,
							Content: "describe this",
							MultiContent: []llmapi.ContentPart{
								{Type: llmapi.ContentPartTypeText, Text: "look:"},
								llmapi.NewContentPartFromImageURL("https://example.com/i.png"),
							},
						},
						{
							Role: llmapi.RoleAssistant,
							ToolCalls: []llmapi.ToolCall{{
								ID:   "tc-1",
								Type: llmapi.ToolTypeFunction,
								Function: llmapi.FunctionCall{
									Name: "search", Arguments: `{"q":"x"}`,
								},
							}},
						},
					},
					Tools: []llmapi.Tool{{
						Type: llmapi.ToolTypeFunction,
						Function: llmapi.FunctionDefinition{
							Name:        "search",
							Description: "search the web",
							Parameters:  map[string]any{"type": "object"},
						},
					}},
					ReasoningSummary: llmapi.ReasoningSummaryDetailed,
					ResponseFormat: &llmapi.ResponseFormat{
						Type: llmapi.ResponseFormatTypeJSONSchema,
						JSONSchema: &llmapi.ResponseFormatJSONSchema{
							Name:        "Out",
							Description: "output",
							Schema:      schema,
							Strict:      true,
						},
					},
				}
				it, err := client.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "m"}, req)
				require.NoError(t, err)
				_ = drainEvents(t, it)

				mock.mu.Lock()
				defer mock.mu.Unlock()
				got := mock.lastCompletionReq
				require.Len(t, got.Messages, 2)
				require.Len(t, got.Messages[0].MultiContent, 2)
				assert.Equal(t, llmapi.ContentPartTypeText, got.Messages[0].MultiContent[0].Type)
				assert.Equal(t, "look:", got.Messages[0].MultiContent[0].Text)
				assert.Equal(t, llmapi.ContentPartTypeImageURL, got.Messages[0].MultiContent[1].Type)
				assert.Equal(t, "https://example.com/i.png", got.Messages[0].MultiContent[1].ImageURL)
				require.Len(t, got.Messages[1].ToolCalls, 1)
				assert.Equal(t, "tc-1", got.Messages[1].ToolCalls[0].ID)
				assert.Equal(t, `{"q":"x"}`, got.Messages[1].ToolCalls[0].Function.Arguments)
				require.Len(t, got.Tools, 1)
				assert.Equal(t, "search", got.Tools[0].Function.Name)
				assert.Equal(t, "search the web", got.Tools[0].Function.Description)
				// Parameters are decoded as raw JSON.
				params, ok := got.Tools[0].Function.Parameters.(json.RawMessage)
				require.True(t, ok)
				assert.JSONEq(t, `{"type":"object"}`, string(params))
				assert.Equal(t, llmapi.ReasoningSummaryDetailed, got.ReasoningSummary)
				require.NotNil(t, got.ResponseFormat)
				assert.Equal(t, llmapi.ResponseFormatTypeJSONSchema, got.ResponseFormat.Type)
				require.NotNil(t, got.ResponseFormat.JSONSchema)
				assert.Equal(t, "Out", got.ResponseFormat.JSONSchema.Name)
				assert.True(t, got.ResponseFormat.JSONSchema.Strict)
				schemaBytes, err := got.ResponseFormat.JSONSchema.Schema.MarshalJSON()
				require.NoError(t, err)
				assert.JSONEq(t, `{"type":"object"}`, string(schemaBytes))
			},
		},
		{
			name: "ErrContextWindowExceeded is preserved on completion error",
			setup: func(m *mockService) {
				m.completionErr = &llmapi.ErrContextWindowExceeded{Count: 200_000, Max: 128_000}
			},
			action: func(t *testing.T, _ *mockService, client llmapi.Service) {
				it, err := client.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "m"}, llmapi.Request{})
				// Server-streaming RPCs deliver handler errors via the first Recv.
				if err == nil {
					require.NotNil(t, it)
					_, ok := it.Next(context.Background())
					assert.False(t, ok)
					err = it.Err()
					_ = it.Close()
				}
				require.Error(t, err)
				cw, ok := errors.AsType[*llmapi.ErrContextWindowExceeded](err)
				require.True(t, ok)
				assert.Equal(t, 200_000, cw.Count)
				assert.Equal(t, 128_000, cw.Max)
			},
		},
		{
			name: "CountTokens routes model and messages",
			setup: func(m *mockService) {
				m.countTokensResult = 7
			},
			action: func(t *testing.T, mock *mockService, client llmapi.Service) {
				msgs := []llmapi.Message{
					{Role: llmapi.RoleUser, Content: "hi"},
					{Role: llmapi.RoleAssistant, Content: "yo"},
				}
				model := llmapi.ModelEntry{Name: "count-model", Provider: "openai"}
				got, err := client.CountTokens(model, msgs)
				require.NoError(t, err)
				assert.Equal(t, 7, got)

				mock.mu.Lock()
				defer mock.mu.Unlock()
				assert.Equal(t, model, mock.lastCountModel)
				require.Len(t, mock.lastCountMsgs, 2)
				assert.Equal(t, "hi", mock.lastCountMsgs[0].Content)
				assert.Equal(t, "yo", mock.lastCountMsgs[1].Content)
			},
		},
		{
			name: "Models streams all entries",
			setup: func(m *mockService) {
				m.models = []llmapi.ModelEntry{
					{Name: "gpt-4o", Provider: "openai", ContextWindow: 128_000},
					{
						Name: "claude-opus-4-6", Provider: "anthropic",
						ContextWindow: 200_000, BaseURL: "https://api.anthropic.example",
					},
				}
			},
			action: func(t *testing.T, _ *mockService, client llmapi.Service) {
				it := client.Models()
				got, err := iterator.ToSlice(context.Background(), it)
				require.NoError(t, err)
				require.Len(t, got, 2)
				assert.Equal(t, "gpt-4o", got[0].Name)
				assert.Equal(t, "openai", got[0].Provider)
				assert.Equal(t, 128_000, got[0].ContextWindow)
				assert.Equal(t, "claude-opus-4-6", got[1].Name)
				assert.Equal(t, "anthropic", got[1].Provider)
				assert.Equal(t, 200_000, got[1].ContextWindow)
				assert.Equal(t, "https://api.anthropic.example", got[1].BaseURL)
			},
		},
		{
			name: "Models empty result",
			setup: func(m *mockService) {
				m.models = nil
			},
			action: func(t *testing.T, _ *mockService, client llmapi.Service) {
				it := client.Models()
				got, err := iterator.ToSlice(context.Background(), it)
				require.NoError(t, err)
				assert.Empty(t, got)
			},
		},
		{
			name: "GetModel returns entry when found",
			setup: func(m *mockService) {
				m.getModelFound = true
				m.getModelResult = llmapi.ModelEntry{
					Name: "claude-opus-4-6", Provider: "anthropic", ContextWindow: 200_000,
				}
			},
			action: func(t *testing.T, mock *mockService, client llmapi.Service) {
				lookup := llmapi.ModelEntry{Name: "claude-opus-4-6"}
				entry, err := client.GetModel(context.Background(), lookup)
				require.NoError(t, err)
				assert.Equal(t, "claude-opus-4-6", entry.Name)
				assert.Equal(t, "anthropic", entry.Provider)
				assert.Equal(t, 200_000, entry.ContextWindow)
				mock.mu.Lock()
				defer mock.mu.Unlock()
				assert.Equal(t, lookup, mock.lastGetModelArg)
			},
		},
		{
			name: "GetModel returns ErrModelNotFound when missing",
			setup: func(m *mockService) {
				m.getModelFound = false
			},
			action: func(t *testing.T, mock *mockService, client llmapi.Service) {
				lookup := llmapi.ModelEntry{Name: "unknown"}
				_, err := client.GetModel(context.Background(), lookup)
				assert.ErrorIs(t, err, llmapi.ErrModelNotFound)
				mock.mu.Lock()
				defer mock.mu.Unlock()
				assert.Equal(t, lookup, mock.lastGetModelArg)
			},
		},
		{
			name: "iterator close cancels server stream",
			setup: func(m *mockService) {
				m.blockUntil = make(chan struct{})
			},
			action: func(t *testing.T, mock *mockService, client llmapi.Service) {
				it, err := client.CreateCompletion(context.Background(), llmapi.ModelEntry{Name: "m"}, llmapi.Request{})
				require.NoError(t, err)
				// Pull one event to ensure the server is in its Next loop
				// before we close, so cancellation has somewhere to land.
				readyCh := make(chan struct{})
				go func() {
					_, _ = it.Next(context.Background())
					close(readyCh)
				}()
				// Give the server a moment to enter the blocking Next.
				time.Sleep(50 * time.Millisecond)
				require.NoError(t, it.Close())
				<-readyCh
				// Server's Next loop should observe context cancellation.
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					if mock.cancelled.Load() {
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				assert.True(t, mock.cancelled.Load(), "server-side context should be cancelled")
				close(mock.blockUntil)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockService{}
			tc.setup(mock)

			_, client, cleanup := setupServerClient(t, mock)
			defer cleanup()

			tc.action(t, mock, client)
		})
	}
}

func setupServerClient(
	t *testing.T, svc llmapi.Service,
) (*Server, llmapi.Service, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "llm")
	require.NoError(t, err)
	socketPath := filepath.Join(tmpDir, "test.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	server := NewServer(svc)
	grpcServer := grpc.NewServer()
	llmrpc.RegisterLLMServer(grpcServer, server)

	go func() { _ = grpcServer.Serve(listener) }()

	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	client := llmrpc.NewClient(context.Background(), conn)

	cleanup := func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = server.Close()
		_ = os.RemoveAll(tmpDir)
	}
	return server, client, cleanup
}

func drainEvents(t *testing.T, it iterator.Iterator[llmapi.Event]) []llmapi.Event {
	t.Helper()
	defer func() { _ = it.Close() }()
	var out []llmapi.Event
	ctx := context.Background()
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}
		out = append(out, ev)
	}
	require.NoError(t, it.Err())
	return out
}

// mockService is a recording implementation of llmapi.Service for integration tests.
type mockService struct {
	mu sync.Mutex

	// recorded inputs
	lastCompletionModel llmapi.ModelEntry
	lastCompletionReq   llmapi.Request
	lastCountModel      llmapi.ModelEntry
	lastCountMsgs       []llmapi.Message
	lastGetModelArg     llmapi.ModelEntry

	// configured outputs
	events            []llmapi.Event
	completionErr     error
	streamErrAfter    int // emit stream error after N events
	countTokensResult int
	models            []llmapi.ModelEntry
	modelsErr         error
	getModelResult    llmapi.ModelEntry
	getModelFound     bool

	// completion-side signaling
	blockUntil chan struct{}
	cancelled  atomic.Bool
}

func (m *mockService) CanGenerateImages(context.Context, llmapi.ModelEntry) (bool, error) {
	return false, nil
}

func (m *mockService) GenerateImage(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error) {
	return llmapi.Image{}, llmapi.ErrImageGenerationUnsupported
}

func (m *mockService) CreateCompletion(
	ctx context.Context, model llmapi.ModelEntry, req llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	m.mu.Lock()
	m.lastCompletionModel = model
	m.lastCompletionReq = req
	events := append([]llmapi.Event(nil), m.events...)
	err := m.completionErr
	block := m.blockUntil
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if block != nil {
		// Emit nothing until ctx cancelled or block closed.
		return iterator.FromFunc(
			func(ctx context.Context) (llmapi.Event, bool, error) {
				select {
				case <-ctx.Done():
					m.cancelled.Store(true)
					return llmapi.Event{}, false, ctx.Err()
				case <-block:
					return llmapi.Event{}, false, nil
				}
			},
			func() error { return nil },
		), nil
	}
	idx := 0
	return iterator.FromFunc(
		func(ctx context.Context) (llmapi.Event, bool, error) {
			if idx >= len(events) {
				return llmapi.Event{}, false, nil
			}
			ev := events[idx]
			idx++
			return ev, true, nil
		},
		func() error { return nil },
	), nil
}

func (m *mockService) CountTokens(
	model llmapi.ModelEntry, msgs []llmapi.Message,
) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastCountModel = model
	m.lastCountMsgs = msgs
	return m.countTokensResult, nil
}

func (m *mockService) Models() iterator.Iterator[llmapi.ModelEntry] {
	m.mu.Lock()
	entries := append([]llmapi.ModelEntry(nil), m.models...)
	err := m.modelsErr
	m.mu.Unlock()
	if err != nil {
		return iterator.FromFunc(
			func(context.Context) (llmapi.ModelEntry, bool, error) {
				return llmapi.ModelEntry{}, false, err
			},
			func() error { return nil },
		)
	}
	return iterator.FromSlice(entries)
}

func (m *mockService) GetModel(
	_ context.Context, model llmapi.ModelEntry,
) (llmapi.ModelEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastGetModelArg = model
	if !m.getModelFound {
		return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
	}
	return m.getModelResult, nil
}

// schemaMarshaler is a test helper json.Marshaler used to feed a
// ResponseFormatJSONSchema through the wire.
type schemaMarshaler struct{ payload string }

func (s schemaMarshaler) MarshalJSON() ([]byte, error) { return []byte(s.payload), nil }
