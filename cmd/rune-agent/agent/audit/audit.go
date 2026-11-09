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

// Package audit wraps an llmapi.Service with persistent completion
// auditing. It was relocated from cmd/rune-agent/llm when rune-agent
// stopped owning its own provider clients; the on-disk key prefix
// ("audit:") and JSON layout are preserved so previously persisted
// entries continue to deserialize.
package audit

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// auditDialogueIDKey is the context key for passing the dialogue ID
// through to the audit service.
type auditDialogueIDKey struct{}

// WithAuditDialogueID returns a context carrying the given dialogue ID
// for audit logging.
func WithAuditDialogueID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, auditDialogueIDKey{}, id)
}

func auditDialogueIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(auditDialogueIDKey{}).(string)
	return v
}

// Entry records a single LLM completion request and its outcome.
// The JSON tags match the previous llm.AuditEntry layout so old entries
// continue to deserialize.
type Entry struct {
	StartedAt       time.Time           `json:"StartedAt"`
	Duration        time.Duration       `json:"Duration"`
	Model           string              `json:"Model,omitempty"`
	Provider        string              `json:"Provider,omitempty"`
	Messages        []llmapi.Message    `json:"Messages"`
	Tools           int                 `json:"Tools"`
	ToolNames       []string            `json:"ToolNames,omitempty"`
	ReasoningEffort string              `json:"ReasoningEffort,omitempty"`
	MaxOutputTokens int                 `json:"MaxOutputTokens,omitempty"`
	EstimatedTokens int                 `json:"EstimatedTokens"`
	ContextWindow   int                 `json:"ContextWindow"`
	Response        *llmapi.Message     `json:"Response,omitempty"`
	Usage           llmapi.Usage        `json:"Usage"`
	FinishReason    llmapi.FinishReason `json:"FinishReason,omitempty"`
	Err             string              `json:"Err,omitempty"`
}

// auditDocument is the persisted document for a dialogue's audit log.
type auditDocument struct {
	Entries []Entry `json:"Entries"`
}

const auditKeyPrefix = "audit:"

// Store persists audit entries per dialogue using storageapi.
type Store struct {
	backend storageapi.Service
}

// NewStore returns a store backed by the given storage service.
func NewStore(backend storageapi.Service) *Store {
	return &Store{backend: backend}
}

// Append adds an entry to the audit log for the given dialogue.
// It is safe to call on a nil receiver or with an empty dialogue ID.
func (s *Store) Append(ctx context.Context, dialogueID string, entry Entry) {
	if s == nil || dialogueID == "" {
		return
	}
	key := auditKeyPrefix + dialogueID
	var doc auditDocument
	if err := s.backend.Get(ctx, key, &doc); err != nil && !errors.Is(err, storageapi.ErrNotFound) {
		slog.Warn("audit: load audit log", "dialogue", dialogueID, "error", err)
		return
	}
	doc.Entries = append(doc.Entries, entry)
	if err := s.backend.Set(ctx, key, &doc); err != nil {
		slog.Warn("audit: save audit log", "dialogue", dialogueID, "error", err)
	}
}

// Get returns all audit entries for the given dialogue.
// Returns nil, nil when there are no entries or the receiver is nil.
func (s *Store) Get(ctx context.Context, dialogueID string) ([]Entry, error) {
	if s == nil {
		return nil, nil
	}
	key := auditKeyPrefix + dialogueID
	var doc auditDocument
	if err := s.backend.Get(ctx, key, &doc); err != nil {
		if errors.Is(err, storageapi.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return doc.Entries, nil
}

// auditService wraps an llmapi.Service, logging and recording token
// usage for every completion request. The Model/Provider fields written
// into each Entry come from the ModelEntry threaded through
// CreateCompletion at call time; the constructor captures only the
// initial entry as a fallback for tests that do not exercise per-call
// routing.
type auditService struct {
	inner llmapi.Service
	store *Store
	model llmapi.ModelEntry
}

// NewService wraps inner in an audit decorator that logs every
// completion request via slog and records entries in store. The model
// is the ModelEntry the decorator was bound to; the per-call
// CreateCompletion ModelEntry takes precedence when set.
func NewService(inner llmapi.Service, store *Store, model llmapi.ModelEntry) llmapi.Service {
	return &auditService{inner: inner, store: store, model: model}
}

// CountTokens delegates to the inner service.
func (s *auditService) CountTokens(model llmapi.ModelEntry, msgs []llmapi.Message) (int, error) {
	return s.inner.CountTokens(s.resolveModel(model), msgs)
}

// Models delegates to the inner service.
func (s *auditService) Models() iterator.Iterator[llmapi.ModelEntry] {
	return s.inner.Models()
}

func (s *auditService) GetModel(ctx context.Context, model llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	return s.inner.GetModel(ctx, model)
}

var _ llmapi.Service = (*auditService)(nil)

func (s *auditService) CanGenerateImages(ctx context.Context, model llmapi.ModelEntry) (bool, error) {
	return s.inner.CanGenerateImages(ctx, model)
}

func (s *auditService) GenerateImage(ctx context.Context, model llmapi.ModelEntry, request llmapi.ImageRequest) (llmapi.Image, error) {
	return s.inner.GenerateImage(ctx, model, request)
}

// CreateCompletion delegates to the inner service and wraps the returned
// iterator to intercept completion events for audit logging.
func (s *auditService) CreateCompletion(
	ctx context.Context, model llmapi.ModelEntry, req llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	resolved := s.resolveModel(model)
	dialogueID := auditDialogueIDFrom(ctx)
	estimated, _ := s.inner.CountTokens(resolved, req.Messages)
	ctxWindow := resolved.ContextWindow

	slog.Info("audit: completion started",
		"dialogue", dialogueID,
		"estimated_tokens_sent", estimated,
		"messages", len(req.Messages),
		"tools", len(req.Tools),
		"context_window", ctxWindow,
	)

	start := time.Now()
	toolNames := extractToolNames(req.Tools)

	it, err := s.inner.CreateCompletion(ctx, resolved, req)
	if err != nil {
		elapsed := time.Since(start)
		slog.Warn("audit: completion error", "error", err, "duration", elapsed)
		s.store.Append(context.WithoutCancel(ctx), dialogueID, Entry{
			StartedAt:       start,
			Duration:        elapsed,
			Model:           resolved.Name,
			Provider:        resolved.Provider,
			Messages:        copyMessages(req.Messages),
			Tools:           len(req.Tools),
			ToolNames:       toolNames,
			ReasoningEffort: string(req.ReasoningEffort),
			MaxOutputTokens: req.MaxOutputTokens,
			EstimatedTokens: estimated,
			ContextWindow:   ctxWindow,
			Err:             err.Error(),
		})
		return nil, err
	}

	entry := &Entry{
		StartedAt:       start,
		Model:           resolved.Name,
		Provider:        resolved.Provider,
		Messages:        copyMessages(req.Messages),
		Tools:           len(req.Tools),
		ToolNames:       toolNames,
		ReasoningEffort: string(req.ReasoningEffort),
		MaxOutputTokens: req.MaxOutputTokens,
		EstimatedTokens: estimated,
		ContextWindow:   ctxWindow,
	}

	// Use a context without cancellation for storage writes so
	// the audit entry is persisted even if the request is cancelled.
	storeCtx := context.WithoutCancel(ctx)

	var recorded bool
	return iterator.FromFunc(
		func(ctx context.Context) (llmapi.Event, bool, error) {
			ev, ok := it.Next(ctx)
			if !ok {
				if !recorded {
					recorded = true
					entry.Duration = time.Since(start)
					if itErr := it.Err(); itErr != nil {
						entry.Err = itErr.Error()
						slog.Warn("audit: completion error",
							"error", itErr, "duration", entry.Duration)
					}
					s.store.Append(storeCtx, dialogueID, *entry)
				}
				return llmapi.Event{}, false, it.Err()
			}
			switch ev.Type {
			case llmapi.EventStreamDone:
				if ev.DoneData != nil && !recorded {
					recorded = true
					entry.Response = &ev.DoneData.Message
					entry.Usage = ev.DoneData.Usage
					entry.FinishReason = ev.DoneData.FinishReason
					entry.Duration = time.Since(start)
					slog.Info("audit: completion done",
						"dialogue", dialogueID,
						"tokens_sent", ev.DoneData.Usage.TokensSent,
						"tokens_received", ev.DoneData.Usage.TokensReceived,
						"tokens_reasoned", ev.DoneData.Usage.TokensReasoned,
						"tokens_cached", ev.DoneData.Usage.TokensCached,
						"tokens_cache_created", ev.DoneData.Usage.TokensCacheCreated,
						"finish_reason", ev.DoneData.FinishReason,
						"duration", entry.Duration,
					)
					s.store.Append(storeCtx, dialogueID, *entry)
				}
			case llmapi.EventStreamError:
				if ev.Error != nil && !recorded {
					recorded = true
					entry.Duration = time.Since(start)
					entry.Err = ev.Error.Error()
					slog.Warn("audit: completion error",
						"error", ev.Error, "duration", entry.Duration)
					s.store.Append(storeCtx, dialogueID, *entry)
				}
			}
			return ev, true, nil
		},
		it.Close,
	), nil
}

// resolveModel returns the per-call ModelEntry when it carries metadata
// (Provider or ContextWindow), and falls back to the constructor-bound
// entry otherwise. This lets callers pass either a fully-resolved entry
// or a name-only lookup.
func (s *auditService) resolveModel(call llmapi.ModelEntry) llmapi.ModelEntry {
	if call.Provider != "" || call.ContextWindow != 0 || call.BaseURL != "" {
		return call
	}
	if call.Name != "" && call.Name != s.model.Name {
		return call
	}
	return s.model
}

func copyMessages(msgs []llmapi.Message) []llmapi.Message {
	out := make([]llmapi.Message, len(msgs))
	copy(out, msgs)
	return out
}

func extractToolNames(tools []llmapi.Tool) []string {
	if len(tools) == 0 {
		return nil
	}
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Function.Name
	}
	return names
}
