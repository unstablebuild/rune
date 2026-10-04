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
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

// stubAPI scripts ConverseStream and CountTokens responses.
type stubAPI struct {
	converseErrs  []error
	converseCalls int
	lastInput     *bedrockruntime.ConverseStreamInput
	stream        *bedrockruntime.ConverseStreamEventStream

	countTokens    int32
	countTokensErr error
	countedInput   *bedrockruntime.CountTokensInput
}

func (s *stubAPI) ConverseStream(
	_ context.Context, in *bedrockruntime.ConverseStreamInput,
) (*bedrockruntime.ConverseStreamEventStream, error) {
	s.lastInput = in
	s.converseCalls++
	if s.converseCalls <= len(s.converseErrs) {
		return nil, s.converseErrs[s.converseCalls-1]
	}
	if s.stream != nil {
		return s.stream, nil
	}
	return scriptedStream(nil, messageStop(types.StopReasonEndTurn)), nil
}

func (s *stubAPI) CountTokens(
	_ context.Context, in *bedrockruntime.CountTokensInput,
) (*bedrockruntime.CountTokensOutput, error) {
	s.countedInput = in
	if s.countTokensErr != nil {
		return nil, s.countTokensErr
	}
	return &bedrockruntime.CountTokensOutput{InputTokens: aws.Int32(s.countTokens)}, nil
}

func testClient(t *testing.T, api converseStreamAPI, cfg Config) *client {
	t.Helper()
	return &client{config: cfg, api: api}
}

func TestClient_CountTokens_UsesNativeAPI(t *testing.T) {
	api := &stubAPI{countTokens: 42}
	c := testClient(t, api, Config{})

	got, err := c.CountTokens(
		llmapi.ModelEntry{Name: ClaudeSonnet45, Provider: LLMProvider},
		[]llmapi.Message{
			{Role: llmapi.RoleSystem, Content: "be brief"},
			{Role: llmapi.RoleUser, Content: "hello"},
		})
	require.NoError(t, err)
	assert.Equal(t, 42, got)

	require.NotNil(t, api.countedInput)
	assert.Equal(t, ClaudeSonnet45, *api.countedInput.ModelId)
	converse, ok := api.countedInput.Input.(*types.CountTokensInputMemberConverse)
	require.True(t, ok)
	assert.Len(t, converse.Value.System, 1)
	assert.Len(t, converse.Value.Messages, 1)
}

func TestClient_CountTokens_FallsBackToEstimate(t *testing.T) {
	api := &stubAPI{countTokensErr: errors.New("unsupported in region")}
	c := testClient(t, api, Config{})

	msgs := []llmapi.Message{{Role: llmapi.RoleUser, Content: "hello world"}}
	got, err := c.CountTokens(llmapi.ModelEntry{Name: NovaPro}, msgs)
	require.NoError(t, err)
	assert.Equal(t, estimateTokens(msgs), got)
}

func TestClient_CreateCompletion_ContextWindowGuard(t *testing.T) {
	c := testClient(t, &stubAPI{}, Config{})

	_, err := c.CreateCompletion(t.Context(),
		llmapi.ModelEntry{Name: NovaMicro, Provider: LLMProvider, ContextWindow: 1000},
		llmapi.Request{
			TokenCount: 990,
			Messages:   []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
		})

	var exceeded *llmapi.ErrContextWindowExceeded
	require.ErrorAs(t, err, &exceeded)
	assert.Equal(t, 990, exceeded.Count)
	assert.Equal(t, 1000, exceeded.Max)
}

func TestClient_CreateCompletion_RetriesThrottling(t *testing.T) {
	api := &stubAPI{converseErrs: []error{&types.ThrottlingException{}}}
	c := testClient(t, api, Config{})

	it, err := c.CreateCompletion(t.Context(),
		llmapi.ModelEntry{Name: ClaudeSonnet45, Provider: LLMProvider},
		llmapi.Request{Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}}})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	// The dial is lazy: no I/O happens until the first Next call.
	assert.Equal(t, 0, api.converseCalls)
	ev, ok := it.Next(t.Context())
	require.True(t, ok)
	assert.Equal(t, llmapi.EventRateLimitWarning, ev.Type)
	assert.Equal(t, 2, api.converseCalls)
}

func TestClient_CreateCompletion_NonRetryableErrorSurfacesOnNext(t *testing.T) {
	api := &stubAPI{converseErrs: []error{&types.AccessDeniedException{}}}
	c := testClient(t, api, Config{})

	it, err := c.CreateCompletion(t.Context(),
		llmapi.ModelEntry{Name: ClaudeSonnet45, Provider: LLMProvider},
		llmapi.Request{Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}}})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	ev, ok := it.Next(t.Context())
	require.True(t, ok)
	require.Equal(t, llmapi.EventStreamError, ev.Type)
	var denied *types.AccessDeniedException
	require.ErrorAs(t, ev.Error, &denied)
	assert.Equal(t, 1, api.converseCalls)

	_, ok = it.Next(t.Context())
	assert.False(t, ok)
}

func TestClient_CreateCompletion_WarnsOnUnsupportedEffort(t *testing.T) {
	api := &stubAPI{}
	c := testClient(t, api, Config{})

	it, err := c.CreateCompletion(t.Context(),
		llmapi.ModelEntry{Name: NovaPro, Provider: LLMProvider},
		llmapi.Request{
			Messages:        []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
			ReasoningEffort: llmapi.ReasoningEffortHigh,
		})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	ev, ok := it.Next(t.Context())
	require.True(t, ok)
	require.Equal(t, llmapi.EventRateLimitWarning, ev.Type)
	assert.Contains(t, ev.RateLimit.Message, "is not supported by")
	// Drain to the end so the request has been sent before inspecting it.
	for {
		if _, more := it.Next(t.Context()); !more {
			break
		}
	}
	assert.Nil(t, api.lastInput.AdditionalModelRequestFields)
}

func TestClient_CreateCompletion_ConfigEffortDropsSilently(t *testing.T) {
	api := &stubAPI{}
	c := testClient(t, api, Config{ReasoningEffort: "high"})

	it, err := c.CreateCompletion(t.Context(),
		llmapi.ModelEntry{Name: NovaPro, Provider: LLMProvider},
		llmapi.Request{Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}}})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	ev, ok := it.Next(t.Context())
	require.True(t, ok)
	assert.NotEqual(t, llmapi.EventRateLimitWarning, ev.Type)
	for {
		if _, more := it.Next(t.Context()); !more {
			break
		}
	}
	assert.Nil(t, api.lastInput.AdditionalModelRequestFields)
}

func TestClient_CreateCompletion_ConfigEffortAppliesToClaude(t *testing.T) {
	api := &stubAPI{}
	c := testClient(t, api, Config{ReasoningEffort: "low"})

	it, err := c.CreateCompletion(t.Context(),
		llmapi.ModelEntry{Name: ClaudeOpus45, Provider: LLMProvider},
		llmapi.Request{Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}}})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	_, ok := it.Next(t.Context())
	require.True(t, ok)

	fields := documentJSON(t, api.lastInput.AdditionalModelRequestFields)
	thinking, ok := fields["thinking"].(map[string]any)
	require.True(t, ok)
	assert.EqualValues(t, thinkingBudget["low"], thinking["budget_tokens"])
}

func TestClient_GetModel(t *testing.T) {
	c := testClient(t, &stubAPI{}, Config{})

	entry, err := c.GetModel(t.Context(), llmapi.ModelEntry{Name: NovaPro})
	require.NoError(t, err)
	assert.Equal(t, LLMProvider, entry.Provider)
	assert.Equal(t, AvailableModels()[NovaPro], entry.ContextWindow)

	_, err = c.GetModel(t.Context(), llmapi.ModelEntry{Name: "nope"})
	assert.ErrorIs(t, err, llmapi.ErrModelNotFound)
}

func TestNewClient_BaseURLDisablesLiveCatalog(t *testing.T) {
	direct := NewClient("key", Config{Region: "us-east-1"})
	assert.NotNil(t, direct.(*client).control)

	gateway := NewClient("key", Config{Region: "us-east-1", BaseURL: "http://127.0.0.1:1"})
	assert.Nil(t, gateway.(*client).control)
}

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"throttling", &types.ThrottlingException{}, true},
		{"unavailable", &types.ServiceUnavailableException{}, true},
		{"internal", &types.InternalServerException{}, true},
		{"model timeout", &types.ModelTimeoutException{}, true},
		{"stream error", &types.ModelStreamErrorException{}, true},
		{"validation", &types.ValidationException{}, false},
		{"access denied", &types.AccessDeniedException{}, false},
		{"connection reset", errors.New("read: connection reset by peer"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isRetryableError(tt.err))
		})
	}
}
