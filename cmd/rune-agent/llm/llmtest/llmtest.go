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

// Package llmtest provides a scriptable in-memory llmapi.Service for
// rune-agent tests. It supersedes the per-provider client tests deleted
// with the cmd/rune-agent/llm tree: rune-agent now talks to the host
// router via the SDK, so the only test surface left is the rune-agent
// side of the wire (agent loop, audit decorator, agentshell, extension
// handler).
package llmtest

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// Response describes a single scripted CreateCompletion response.
type Response struct {
	// Chunks are emitted as EventTextDelta events in order. The
	// concatenation forms the DoneData.Message.Content.
	Chunks []string
	// ReasoningChunks are emitted as EventReasoningDelta events
	// interleaved with Chunks (paired by index). The concatenation
	// forms DoneData.Message.ReasoningContent.
	ReasoningChunks []string
	// ToolCalls are emitted as EventToolCallDone events and appear on
	// DoneData.Message.ToolCalls. FinishReason is forced to
	// FinishReasonToolCall when non-empty unless explicitly overridden.
	ToolCalls []llmapi.ToolCall
	// FinishReason controls the DoneData.FinishReason for this response.
	// Defaults to FinishReasonStop.
	FinishReason llmapi.FinishReason
	// Usage is forwarded into DoneData.Usage.
	Usage llmapi.Usage
	// Err, when non-nil, is returned directly from CreateCompletion
	// instead of producing an event stream.
	Err error
	// StreamErr, when non-nil, is surfaced via iterator.Err() after the
	// scripted events have been drained.
	StreamErr error
	// RateLimitWarnings are emitted as EventRateLimitWarning events
	// before any text/reasoning deltas.
	RateLimitWarnings []*llmapi.RateLimitInfo
	// ProviderItems are copied into DoneData.Message.ProviderItems so
	// tests can simulate provider-stateful streams.
	ProviderItems []json.RawMessage
}

// Service is a scriptable llmapi.Service.
type Service struct {
	mu sync.Mutex
	// Models is the catalog returned by Models() / GetModel(). When
	// empty, GetModel echoes the requested entry with no metadata.
	models []llmapi.ModelEntry
	// CountTokensFn, when non-nil, overrides the default CountTokens
	// implementation. The default returns 0.
	CountTokensFn func(model llmapi.ModelEntry, msgs []llmapi.Message) (int, error)
	// BeforeCompletion is invoked just before the next scripted
	// response is consumed. Tests use this to cancel contexts or to
	// dynamically extend Responses mid-run.
	BeforeCompletion func()

	callCount int
	responses []Response
	requests  []Request
}

// Request captures a single CreateCompletion invocation for assertion.
type Request struct {
	Model   llmapi.ModelEntry
	Request llmapi.Request
}

// New returns a Service with the given catalog and scripted responses.
// Both slices may be nil/empty.
func New(models []llmapi.ModelEntry, responses ...Response) *Service {
	return &Service{models: append([]llmapi.ModelEntry(nil), models...), responses: responses}
}

// SetModels replaces the model catalog.
func (s *Service) SetModels(models []llmapi.ModelEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.models = append([]llmapi.ModelEntry(nil), models...)
}

// Enqueue appends scripted responses to the queue.
func (s *Service) Enqueue(responses ...Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses = append(s.responses, responses...)
}

// CallCount returns how many CreateCompletion calls have been served.
func (s *Service) CallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callCount
}

// Requests returns a snapshot of captured requests in invocation order.
func (s *Service) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Request, len(s.requests))
	copy(out, s.requests)
	return out
}

// CreateCompletion satisfies llmapi.Service. It pops the next scripted
// response and emits it as an event stream.
func (s *Service) CreateCompletion(
	ctx context.Context, model llmapi.ModelEntry, req llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.BeforeCompletion != nil {
		s.BeforeCompletion()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	idx := s.callCount
	s.callCount++
	s.requests = append(s.requests, Request{Model: model, Request: req})
	if idx >= len(s.responses) {
		s.mu.Unlock()
		return nil, errors.New("llmtest: no more scripted responses")
	}
	resp := s.responses[idx]
	s.mu.Unlock()

	if resp.Err != nil {
		return nil, resp.Err
	}

	var items []llmapi.Event
	for _, rl := range resp.RateLimitWarnings {
		items = append(items, llmapi.Event{
			Type:      llmapi.EventRateLimitWarning,
			RateLimit: rl,
		})
	}
	// Reasoning chunks not paired with a text chunk are emitted
	// before the text deltas.
	for i, chunk := range resp.ReasoningChunks {
		if i < len(resp.Chunks) {
			continue
		}
		items = append(items, llmapi.Event{Type: llmapi.EventReasoningDelta, Reasoning: chunk})
	}
	for i, chunk := range resp.Chunks {
		if i < len(resp.ReasoningChunks) {
			items = append(items, llmapi.Event{Type: llmapi.EventReasoningDelta, Reasoning: resp.ReasoningChunks[i]})
		}
		if chunk != "" {
			items = append(items, llmapi.Event{Type: llmapi.EventTextDelta, Text: chunk})
		}
	}
	for i := range resp.ToolCalls {
		tc := resp.ToolCalls[i]
		items = append(items, llmapi.Event{Type: llmapi.EventToolCallDone, ToolCall: &tc})
	}

	finish := resp.FinishReason
	if finish == "" {
		finish = llmapi.FinishReasonStop
	}
	msg := llmapi.Message{
		Role:             llmapi.RoleAssistant,
		Content:          strings.Join(resp.Chunks, ""),
		ReasoningContent: strings.Join(resp.ReasoningChunks, ""),
		ToolCalls:        resp.ToolCalls,
		ProviderItems:    resp.ProviderItems,
	}
	items = append(items, llmapi.Event{
		Type: llmapi.EventStreamDone,
		DoneData: &llmapi.DoneData{
			Message:      msg,
			FinishReason: finish,
			Usage:        resp.Usage,
		},
	})

	inner := iterator.FromSlice(items)
	if resp.StreamErr != nil {
		return &errAfterIterator{inner: inner, err: resp.StreamErr}, nil
	}
	return inner, nil
}

// CanGenerateImages reports false; scripted completions do not include image generation.
func (s *Service) CanGenerateImages(context.Context, llmapi.ModelEntry) (bool, error) {
	return false, nil
}

// GenerateImage returns ErrImageGenerationUnsupported without consuming a scripted response.
func (s *Service) GenerateImage(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error) {
	return llmapi.Image{}, llmapi.ErrImageGenerationUnsupported
}

// CountTokens satisfies llmapi.Service.
func (s *Service) CountTokens(model llmapi.ModelEntry, msgs []llmapi.Message) (int, error) {
	if s.CountTokensFn != nil {
		return s.CountTokensFn(model, msgs)
	}
	return 0, nil
}

// Models satisfies llmapi.Service.
func (s *Service) Models() iterator.Iterator[llmapi.ModelEntry] {
	s.mu.Lock()
	out := make([]llmapi.ModelEntry, len(s.models))
	copy(out, s.models)
	s.mu.Unlock()
	return iterator.FromSlice(out)
}

// GetModel satisfies llmapi.Service.
func (s *Service) GetModel(_ context.Context, model llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.models {
		if m.Name == model.Name {
			return m, nil
		}
	}
	// When the catalog is empty, return a synthetic entry with a
	// very large context window so tests that ignore the catalog
	// still get a working ModelEntry from a name lookup.
	if len(s.models) == 0 && model.Name != "" {
		out := model
		if out.ContextWindow == 0 {
			out.ContextWindow = math.MaxInt
		}
		return out, nil
	}
	return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
}

// errAfterIterator wraps an iterator and surfaces a final error from Err().
type errAfterIterator struct {
	inner iterator.Iterator[llmapi.Event]
	err   error
}

func (e *errAfterIterator) Next(ctx context.Context) (llmapi.Event, bool) {
	return e.inner.Next(ctx)
}

func (e *errAfterIterator) Err() error {
	if err := e.inner.Err(); err != nil {
		return err
	}
	return e.err
}

func (e *errAfterIterator) Close() error { return e.inner.Close() }
