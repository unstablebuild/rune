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
