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

package audit

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// stubAuditService is a minimal llmapi.Service for testing the audit wrapper.
type stubAuditService struct {
	events      []llmapi.Event
	createErr   error
	tokenCount  int
	contextWin  int
	lastRequest *llmapi.Request
	lastModel   llmapi.ModelEntry
}

func (s *stubAuditService) CreateCompletion(
	_ context.Context, model llmapi.ModelEntry, req llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	s.lastRequest = &req
	s.lastModel = model
	if s.createErr != nil {
		return nil, s.createErr
	}
	return iterator.FromSlice(s.events), nil
}

func (s *stubAuditService) CountTokens(_ llmapi.ModelEntry, _ []llmapi.Message) (int, error) {
	return s.tokenCount, nil
}

func (s *stubAuditService) Models() iterator.Iterator[llmapi.ModelEntry] {
	return iterator.FromSlice[llmapi.ModelEntry](nil)
}

func (s *stubAuditService) GetModel(_ context.Context, m llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	return m, nil
}

func (s *stubAuditService) CanGenerateImages(context.Context, llmapi.ModelEntry) (bool, error) {
	return false, nil
}

func (s *stubAuditService) GenerateImage(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error) {
	return llmapi.Image{}, llmapi.ErrImageGenerationUnsupported
}

func testModel() llmapi.ModelEntry {
	return llmapi.ModelEntry{Name: "test-model", Provider: "test-provider"}
}

type stubImageService struct {
	stubAuditService
	ctx       context.Context
	model     llmapi.ModelEntry
	request   llmapi.ImageRequest
	available bool
	image     llmapi.Image
	err       error
}

func (s *stubImageService) CanGenerateImages(ctx context.Context, model llmapi.ModelEntry) (bool, error) {
	s.ctx, s.model = ctx, model
	return s.available, s.err
}

func (s *stubImageService) GenerateImage(ctx context.Context, model llmapi.ModelEntry, request llmapi.ImageRequest) (llmapi.Image, error) {
	s.ctx, s.model, s.request = ctx, model, request
	return s.image, s.err
}

func TestAuditImageForwarding(t *testing.T) {
	ctx := WithAuditDialogueID(context.Background(), "images")
	for _, fail := range []bool{false, true} {
		for _, model := range []llmapi.ModelEntry{{}, {Name: "override", Provider: "other"}} {
			inner := &stubImageService{available: true, image: llmapi.Image{PNG: []byte("png"), GenerationID: "generation", RequestID: "request"}}
			if fail {
				inner.err = errors.New("provider failure")
			}
			generator := NewService(inner, nil, testModel())
			wantModel := model
			available, err := generator.CanGenerateImages(ctx, model)
			assert.Equal(t, inner.available, available)
			assert.Equal(t, inner.err, err)
			assert.Equal(t, ctx, inner.ctx)
			assert.Equal(t, wantModel, inner.model)
			req := llmapi.ImageRequest{Prompt: "draw", TransparentBackground: true, Images: [][]byte{[]byte("reference")}}
			image, err := generator.GenerateImage(ctx, model, req)
			assert.Equal(t, inner.image, image)
			assert.Equal(t, inner.err, err)
			assert.Equal(t, ctx, inner.ctx)
			assert.Equal(t, wantModel, inner.model)
			assert.Equal(t, req, inner.request)
		}
	}
	t.Run("unsupported", func(t *testing.T) {
		generator := NewService(&stubAuditService{}, nil, testModel())
		available, err := generator.CanGenerateImages(ctx, testModel())
		require.NoError(t, err)
		assert.False(t, available)
		image, err := generator.GenerateImage(ctx, testModel(), llmapi.ImageRequest{})
		require.ErrorIs(t, err, llmapi.ErrImageGenerationUnsupported)
		assert.Empty(t, image)
	})
}

func TestAuditService(t *testing.T) {
	tests := []struct {
		name        string
		inner       *stubAuditService
		request     llmapi.Request
		dialogueID  string
		wantEntries int
		assertEntry func(t *testing.T, e Entry)
		wantErr     bool
	}{
		{
			name: "successful completion records entry with usage",
			inner: &stubAuditService{
				tokenCount: 100,
				contextWin: 4096,
				events: []llmapi.Event{
					{Type: llmapi.EventTextDelta, Text: "hello"},
					{Type: llmapi.EventStreamDone, DoneData: &llmapi.DoneData{
						Message:      llmapi.Message{Role: llmapi.RoleAssistant, Content: "hello"},
						FinishReason: llmapi.FinishReasonStop,
						Usage:        llmapi.Usage{TokensSent: 50, TokensReceived: 10, TokensCacheCreated: 5},
					}},
				},
			},
			request: llmapi.Request{
				Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
				Tools:    []llmapi.Tool{{Type: llmapi.ToolTypeFunction, Function: llmapi.FunctionDefinition{Name: "do_thing"}}},
			},
			dialogueID:  "conv-1",
			wantEntries: 1,
			assertEntry: func(t *testing.T, e Entry) {
				assert.Equal(t, 100, e.EstimatedTokens)
				assert.Equal(t, 4096, e.ContextWindow)
				assert.Equal(t, 1, e.Tools)
				assert.Equal(t, 50, e.Usage.TokensSent)
				assert.Equal(t, 10, e.Usage.TokensReceived)
				assert.Equal(t, 5, e.Usage.TokensCacheCreated)
				assert.Equal(t, llmapi.FinishReasonStop, e.FinishReason)
				assert.Empty(t, e.Err)
				require.NotNil(t, e.Response)
				assert.Equal(t, "hello", e.Response.Content)
				require.Len(t, e.Messages, 1)
				assert.Equal(t, "hi", e.Messages[0].Content)
				assert.True(t, e.Duration > 0)
				assert.Equal(t, "test-model", e.Model)
				assert.Equal(t, "test-provider", e.Provider)
				assert.Equal(t, []string{"do_thing"}, e.ToolNames)
			},
		},
		{
			name: "CreateCompletion error records entry",
			inner: &stubAuditService{
				createErr:  errors.New("api down"),
				tokenCount: 42,
				contextWin: 8192,
			},
			request: llmapi.Request{
				Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "test"}},
			},
			dialogueID:  "conv-err",
			wantEntries: 1,
			wantErr:     true,
			assertEntry: func(t *testing.T, e Entry) {
				assert.Equal(t, "api down", e.Err)
				assert.Equal(t, 42, e.EstimatedTokens)
				assert.Nil(t, e.Response)
			},
		},
		{
			name: "stream error event records entry",
			inner: &stubAuditService{
				events: []llmapi.Event{
					{Type: llmapi.EventTextDelta, Text: "partial"},
					{Type: llmapi.EventStreamError, Error: errors.New("stream broken")},
				},
			},
			request: llmapi.Request{
				Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "test"}},
			},
			dialogueID:  "conv-stream-err",
			wantEntries: 1,
			assertEntry: func(t *testing.T, e Entry) {
				assert.Equal(t, "stream broken", e.Err)
				assert.Nil(t, e.Response)
			},
		},
		{
			name: "all event types are passed through",
			inner: &stubAuditService{
				events: []llmapi.Event{
					{Type: llmapi.EventReasoningDelta, Reasoning: "thinking"},
					{Type: llmapi.EventTextDelta, Text: "answer"},
					{Type: llmapi.EventToolCallDone, ToolCall: &llmapi.ToolCall{ID: "t1"}},
					{Type: llmapi.EventStreamDone, DoneData: &llmapi.DoneData{
						Message:      llmapi.Message{Role: llmapi.RoleAssistant, Content: "answer"},
						FinishReason: llmapi.FinishReasonToolCall,
						Usage:        llmapi.Usage{TokensSent: 200, TokensReceived: 50, TokensReasoned: 30},
					}},
				},
			},
			request:     llmapi.Request{Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "q"}}},
			dialogueID:  "conv-passthrough",
			wantEntries: 1,
			assertEntry: func(t *testing.T, e Entry) {
				assert.Equal(t, llmapi.FinishReasonToolCall, e.FinishReason)
				assert.Equal(t, 30, e.Usage.TokensReasoned)
			},
		},
		{
			name: "no dialogue ID skips recording",
			inner: &stubAuditService{
				events: []llmapi.Event{
					{Type: llmapi.EventStreamDone, DoneData: &llmapi.DoneData{
						Message:      llmapi.Message{Role: llmapi.RoleAssistant, Content: "ok"},
						FinishReason: llmapi.FinishReasonStop,
					}},
				},
			},
			request:     llmapi.Request{Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}}},
			dialogueID:  "", // no dialogue ID
			wantEntries: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NewStore(storagestub.NewInMemoryService())
			model := testModel()
			model.ContextWindow = tt.inner.contextWin
			svc := NewService(tt.inner, store, model)

			ctx := context.Background()
			if tt.dialogueID != "" {
				ctx = WithAuditDialogueID(ctx, tt.dialogueID)
			}

			it, err := svc.CreateCompletion(ctx, llmapi.ModelEntry{}, tt.request)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				var got []llmapi.Event
				for {
					ev, ok := it.Next(ctx)
					if !ok {
						break
					}
					got = append(got, ev)
				}
				it.Close() //nolint:errcheck
				assert.Len(t, got, len(tt.inner.events))
			}

			entries, err := store.Get(ctx, tt.dialogueID)
			require.NoError(t, err)
			require.Len(t, entries, tt.wantEntries)
			if tt.assertEntry != nil && tt.wantEntries > 0 {
				tt.assertEntry(t, entries[0])
			}
		})
	}
}

func TestAuditServicePassthrough(t *testing.T) {
	inner := &stubAuditService{tokenCount: 77, contextWin: 32000}
	store := NewStore(storagestub.NewInMemoryService())
	model := testModel()
	model.ContextWindow = 32000
	svc := NewService(inner, store, model)

	n, err := svc.CountTokens(llmapi.ModelEntry{}, []llmapi.Message{{Content: "x"}})
	require.NoError(t, err)
	assert.Equal(t, 77, n)
}

func TestAuditStorePerDialogue(t *testing.T) {
	store := NewStore(storagestub.NewInMemoryService())
	ctx := context.Background()

	store.Append(ctx, "d1", Entry{EstimatedTokens: 10})
	store.Append(ctx, "d1", Entry{EstimatedTokens: 20})
	store.Append(ctx, "d2", Entry{EstimatedTokens: 30})

	d1, err := store.Get(ctx, "d1")
	require.NoError(t, err)
	require.Len(t, d1, 2)
	assert.Equal(t, 10, d1[0].EstimatedTokens)
	assert.Equal(t, 20, d1[1].EstimatedTokens)

	d2, err := store.Get(ctx, "d2")
	require.NoError(t, err)
	require.Len(t, d2, 1)
	assert.Equal(t, 30, d2[0].EstimatedTokens)

	// Non-existent dialogue returns nil.
	d3, err := store.Get(ctx, "d3")
	require.NoError(t, err)
	assert.Nil(t, d3)
}

func TestAuditStoreNilSafe(t *testing.T) {
	var store *Store
	ctx := context.Background()

	// Should not panic.
	store.Append(ctx, "d1", Entry{})

	entries, err := store.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Nil(t, entries)
}

func TestAuditRequestMessagesAreCopied(t *testing.T) {
	inner := &stubAuditService{
		events: []llmapi.Event{
			{Type: llmapi.EventStreamDone, DoneData: &llmapi.DoneData{
				Message:      llmapi.Message{Role: llmapi.RoleAssistant, Content: "ok"},
				FinishReason: llmapi.FinishReasonStop,
			}},
		},
	}
	store := NewStore(storagestub.NewInMemoryService())
	svc := NewService(inner, store, testModel())

	ctx := WithAuditDialogueID(context.Background(), "copy-test")
	msgs := []llmapi.Message{{Role: llmapi.RoleUser, Content: "original"}}
	it, err := svc.CreateCompletion(ctx, llmapi.ModelEntry{}, llmapi.Request{Messages: msgs})
	require.NoError(t, err)
	for {
		if _, ok := it.Next(ctx); !ok {
			break
		}
	}
	it.Close() //nolint:errcheck

	// Mutate the original slice after the call.
	msgs[0].Content = "mutated"

	entries, err := store.Get(ctx, "copy-test")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "original", entries[0].Messages[0].Content)
}
