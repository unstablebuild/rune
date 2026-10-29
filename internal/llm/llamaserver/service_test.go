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
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

func TestService_ImageGenerationUnsupported(t *testing.T) {
	var svc llmapi.Service = &Service{}
	supported, err := svc.CanGenerateImages(context.Background(), llmapi.ModelEntry{})
	require.NoError(t, err)
	assert.False(t, supported)
	image, err := svc.GenerateImage(context.Background(), llmapi.ModelEntry{}, llmapi.ImageRequest{})
	require.ErrorIs(t, err, llmapi.ErrImageGenerationUnsupported)
	assert.Equal(t, llmapi.Image{}, image)
}

func TestServiceNew_PanicsOnNilDeps(t *testing.T) {
	exec := &fakeExecutor{}
	loc := NewFixedLocator("/usr/bin/llama-server")
	notis := &recordingNotifications{}

	assert.Panics(t, func() { New(Config{}, nil, loc, notis) }, "nil exec")
	assert.Panics(t, func() { New(Config{}, exec, nil, notis) }, "nil locator")
	assert.Panics(t, func() { New(Config{}, exec, loc, nil) }, "nil notifications")

	require.NotNil(t, New(Config{}, exec, loc, notis))
}

func TestService_CreateCompletion_ServerNotInstalled(t *testing.T) {
	svc := New(Config{}, &fakeExecutor{}, NewFixedLocator(""), &recordingNotifications{})
	t.Cleanup(func() { _ = svc.Close() })

	_, err := svc.CreateCompletion(context.Background(),
		llmapi.ModelEntry{Name: "local", Provider: LLMProvider}, llmapi.Request{})
	require.Error(t, err)
	assert.Equal(t, installMessage, err.Error())
}

func TestService_CreateCompletion_DelegatesAndReleases(t *testing.T) {
	exec := &fakeExecutor{}
	svc := New(
		Config{StartupTimeout: 5 * time.Second, IdleTimeout: time.Hour},
		exec, NewFixedLocator("/usr/bin/llama-server"), &recordingNotifications{},
	)
	t.Cleanup(func() { _ = svc.Close() })

	entry := llmapi.ModelEntry{
		Name: "local", Provider: LLMProvider, BaseURL: "/models/local.gguf",
		ContextWindow: 8192,
	}
	it, err := svc.CreateCompletion(context.Background(), entry, llmapi.Request{
		Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
	})
	require.NoError(t, err)

	// Drain the SSE stream from the stub OpenAI-compatible server.
	ctx := context.Background()
	var sawText bool
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}
		if ev.Type == llmapi.EventTextDelta && ev.Text != "" {
			sawText = true
		}
	}
	require.NoError(t, it.Err())
	assert.True(t, sawText, "stub server response should surface text")

	// Before Close, the server is still in-flight (idle timer not armed).
	svc.mu.Lock()
	proc := svc.servers[modelFromEntry(entry).key()]
	svc.mu.Unlock()
	require.NotNil(t, proc)
	assert.Equal(t, 1, proc.inFlight(), "server ref held until iterator Close")

	require.NoError(t, it.Close())
	assert.Equal(t, 0, proc.inFlight(), "iterator Close releases the server ref")
}

func TestService_CountTokens_UsesOfflineEstimate(t *testing.T) {
	svc := New(Config{}, &fakeExecutor{}, NewFixedLocator("/usr/bin/llama-server"),
		&recordingNotifications{})
	t.Cleanup(func() { _ = svc.Close() })

	n, err := svc.CountTokens(llmapi.ModelEntry{Name: "local"},
		[]llmapi.Message{{Role: llmapi.RoleUser, Content: "hello world"}})
	require.NoError(t, err)
	assert.Positive(t, n)
}

func TestService_ImplementsLLMAPIService(t *testing.T) {
	var _ llmapi.Service = New(Config{}, &fakeExecutor{},
		NewFixedLocator("/usr/bin/llama-server"), &recordingNotifications{})
}

func newEmbedTestService(t *testing.T, exec *fakeExecutor, maxServers int) *Service {
	t.Helper()
	svc := New(
		Config{StartupTimeout: 5 * time.Second, IdleTimeout: time.Hour, MaxServers: maxServers},
		exec, NewFixedLocator("/usr/bin/llama-server"), &recordingNotifications{},
	)
	t.Cleanup(func() { _ = svc.Close() })
	return svc
}

var embedEntry = llmapi.ModelEntry{
	Name: "huggingface.co/ggml-org/embeddinggemma-2-GGUF:Q8_0", Provider: LLMProvider,
	BaseURL: "/models/sha256-abc", ProjectorPath: "/models/sha256-mmproj", ContextWindow: 2048,
}

func TestService_EmbedTexts_BatchesAndOrders(t *testing.T) {
	exec := &fakeExecutor{}
	svc := newEmbedTestService(t, exec, 1)
	texts := make([]string, 40)
	for i := range texts {
		texts[i] = fmt.Sprintf("text %d", i)
	}
	vecs, err := svc.EmbedTexts(context.Background(), embedEntry, texts)
	require.NoError(t, err)
	require.Len(t, vecs, len(texts))
	for i, v := range vecs {
		assertVector(t, fakeVector(texts[i]), v, "vector %d out of order", i)
	}
	requests, inputs := exec.embedStats()
	assert.Equal(t, 3, requests)
	assert.Equal(t, []int{16, 16, 8}, []int{len(inputs[0]), len(inputs[1]), len(inputs[2])})

	svc.mu.Lock()
	proc := svc.servers[embeddingModelFromEntry(embedEntry).key()]
	svc.mu.Unlock()
	require.NotNil(t, proc)
	assert.Equal(t, 0, proc.inFlight(), "EmbedTexts releases the server")
	require.Len(t, exec.startArgs, 1)
	assert.False(t, slices.Contains(exec.startArgs[0], "--mmproj"), "an embedding server never loads the projector")
}

func TestService_EmbedTexts_SeparateProcessFromChat(t *testing.T) {
	exec := &fakeExecutor{}
	svc := newEmbedTestService(t, exec, 2)
	ctx := context.Background()
	entry := embedEntry
	entry.ProjectorPath = ""

	it, err := svc.CreateCompletion(ctx, entry, llmapi.Request{
		Messages: []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
	})
	require.NoError(t, err)
	for _, ok := it.Next(ctx); ok; _, ok = it.Next(ctx) {
	}
	require.NoError(t, it.Close())

	_, err = svc.EmbedTexts(ctx, entry, []string{"hello"})
	require.NoError(t, err)

	require.Equal(t, 2, exec.startCount(), "chat and embedding servers are distinct processes")
	assert.False(t, slices.Contains(exec.startArgs[0], "--embedding"))
	assert.True(t, slices.Contains(exec.startArgs[1], "--embedding"))
	assert.Equal(t, argValue(exec.startArgs[0], "--model"), argValue(exec.startArgs[1], "--model"))
}

func TestService_EmbedTexts_RetriesHalvedInput(t *testing.T) {
	exec := &fakeExecutor{embedMaxBytes: 100}
	svc := newEmbedTestService(t, exec, 1)
	short := "short"
	medium := strings.Repeat("m", 150)
	long := strings.Repeat("l", 300)

	vecs, err := svc.EmbedTexts(context.Background(), embedEntry, []string{short, medium, long})
	require.NoError(t, err)
	require.Len(t, vecs, 3)
	assertVector(t, fakeVector(short), vecs[0])
	assertVector(t, fakeVector(medium[:75]), vecs[1], "retried with the first half")
	assert.Nil(t, vecs[2], "still too large after halving")

	_, inputs := exec.embedStats()
	assert.Equal(t, [][]string{
		{short, medium, long},
		{short},
		{medium},
		{medium[:75]},
		{long},
		{long[:150]},
	}, inputs)
}

func TestService_EmbedTexts_ServerNotInstalled(t *testing.T) {
	svc := New(Config{}, &fakeExecutor{}, NewFixedLocator(""), &recordingNotifications{})
	t.Cleanup(func() { _ = svc.Close() })
	_, err := svc.EmbedTexts(context.Background(), embedEntry, []string{"x"})
	require.Error(t, err)
	assert.Equal(t, installMessage, err.Error())
}

func assertVector(t *testing.T, want, got []float32, msgAndArgs ...any) {
	t.Helper()
	require.Len(t, got, len(want), msgAndArgs...)
	for i := range want {
		assert.InDelta(t, want[i], got[i], 1e-6, msgAndArgs...)
	}
}
