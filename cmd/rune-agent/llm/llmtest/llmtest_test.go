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

package llmtest

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

func TestService_ImageGenerationUnsupported(t *testing.T) {
	var svc llmapi.Service = New(nil)
	ctx := context.Background()
	model := llmapi.ModelEntry{Name: "m"}

	supported, err := svc.CanGenerateImages(ctx, model)
	require.NoError(t, err)
	assert.False(t, supported)

	image, err := svc.GenerateImage(ctx, model, llmapi.ImageRequest{})
	assert.ErrorIs(t, err, llmapi.ErrImageGenerationUnsupported)
	assert.Equal(t, llmapi.Image{}, image)
}

func TestService_CreateCompletion_emits_text_chunks_and_done(t *testing.T) {
	svc := New(nil, Response{
		Chunks:       []string{"hello ", "world"},
		FinishReason: llmapi.FinishReasonStop,
	})

	it, err := svc.CreateCompletion(
		context.Background(),
		llmapi.ModelEntry{Name: "m"},
		llmapi.Request{},
	)
	require.NoError(t, err)
	events, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	require.Len(t, events, 3)
	assert.Equal(t, llmapi.EventTextDelta, events[0].Type)
	assert.Equal(t, "hello ", events[0].Text)
	assert.Equal(t, llmapi.EventTextDelta, events[1].Type)
	assert.Equal(t, "world", events[1].Text)
	assert.Equal(t, llmapi.EventStreamDone, events[2].Type)
	require.NotNil(t, events[2].DoneData)
	assert.Equal(t, "hello world", events[2].DoneData.Message.Content)
	assert.Equal(t, llmapi.FinishReasonStop, events[2].DoneData.FinishReason)
	assert.Equal(t, 1, svc.CallCount())
	reqs := svc.Requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "m", reqs[0].Model.Name)
}

func TestService_CreateCompletion_returns_scripted_error(t *testing.T) {
	want := errors.New("boom")
	svc := New(nil, Response{Err: want})

	_, err := svc.CreateCompletion(
		context.Background(),
		llmapi.ModelEntry{Name: "m"},
		llmapi.Request{},
	)
	assert.ErrorIs(t, err, want)
}

func TestService_CreateCompletion_exhausted_responses_errors(t *testing.T) {
	svc := New(nil)

	_, err := svc.CreateCompletion(
		context.Background(),
		llmapi.ModelEntry{Name: "m"},
		llmapi.Request{},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no more scripted responses")
}

func TestService_GetModel_uses_catalog_then_falls_back(t *testing.T) {
	svc := New([]llmapi.ModelEntry{
		{Name: "a", ContextWindow: 1234},
	})

	got, err := svc.GetModel(context.Background(), llmapi.ModelEntry{Name: "a"})
	require.NoError(t, err)
	assert.Equal(t, 1234, got.ContextWindow)

	_, err = svc.GetModel(context.Background(), llmapi.ModelEntry{Name: "unknown"})
	assert.ErrorIs(t, err, llmapi.ErrModelNotFound)

	// Empty catalog returns a synthetic entry.
	empty := New(nil)
	got, err = empty.GetModel(context.Background(), llmapi.ModelEntry{Name: "x"})
	require.NoError(t, err)
	assert.Equal(t, "x", got.Name)
	assert.NotZero(t, got.ContextWindow)
}

func TestService_CountTokens_default_and_override(t *testing.T) {
	svc := New(nil)
	n, err := svc.CountTokens(llmapi.ModelEntry{}, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	svc.CountTokensFn = func(_ llmapi.ModelEntry, msgs []llmapi.Message) (int, error) {
		return len(msgs), nil
	}
	n, err = svc.CountTokens(llmapi.ModelEntry{}, []llmapi.Message{{}, {}, {}})
	require.NoError(t, err)
	assert.Equal(t, 3, n)
}
