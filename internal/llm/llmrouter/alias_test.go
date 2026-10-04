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

package llmrouter

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/internal/llm/anthropic"
	"unstable.build/rune/internal/llm/gemini"
	"unstable.build/rune/internal/llm/openai"
)

func TestRouter_GetModel_ResolvesAlias(t *testing.T) {
	r := newTestRouter(t)
	ctx := context.Background()
	require.NoError(t, r.SetAlias(ctx, "default", "openai/"+openai.GPT5Dot5))

	got, err := r.GetModel(ctx, llmapi.ModelEntry{Name: "default"})
	require.NoError(t, err)
	assert.Equal(t, ProviderOpenAI, got.Provider)
	assert.Equal(t, openai.GPT5Dot5, got.Name)
}

func TestRouter_SetAlias_RejectsUnknownTarget(t *testing.T) {
	r := newTestRouter(t)
	ctx := context.Background()
	err := r.SetAlias(ctx, "default", "openai/does-not-exist")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not an available model")

	err = r.SetAlias(ctx, "default", "bare-name")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provider/name form")
}

func TestRouter_RemoveAlias(t *testing.T) {
	r := newTestRouter(t)
	ctx := context.Background()
	require.NoError(t, r.SetAlias(ctx, "default", "openai/"+openai.GPT5Dot5))
	require.NoError(t, r.RemoveAlias(ctx, "default"))
	require.ErrorIs(t, r.RemoveAlias(ctx, "default"), ErrAliasNotFound)
}

func TestRouter_Alias_UnsetReservedFallsBackToFlagship(t *testing.T) {
	ctx := context.Background()
	r := newTestRouter(t)
	// The key document's server-managed UpdatedAt advances on every add,
	// so the most-recently authenticated provider wins. A short sleep
	// guarantees the millisecond-resolution timestamps differ.
	require.NoError(t, r.store.add(ctx, ProviderAnthropic, "k", "key-a", ""))
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, r.store.add(ctx, ProviderGemini, "k", "key-g", ""))

	for _, name := range []string{llmapi.DefaultModel, "query"} {
		got, err := r.GetModel(ctx, llmapi.ModelEntry{Name: name})
		require.NoError(t, err, name)
		assert.Equal(t, ProviderGemini, got.Provider, name)
		assert.Equal(t, gemini.FlagshipModel(), got.Name, name)
	}

	// Re-authenticate anthropic so it becomes the most recent; the
	// fallback flips.
	time.Sleep(2 * time.Millisecond)
	require.NoError(t, r.store.add(ctx, ProviderAnthropic, "k2", "key-a2", ""))
	got, err := r.GetModel(ctx, llmapi.ModelEntry{Name: llmapi.DefaultModel})
	require.NoError(t, err)
	assert.Equal(t, ProviderAnthropic, got.Provider)
	assert.Equal(t, anthropic.FlagshipModel(), got.Name)
}

func TestRouter_Alias_NoAuthFallsBackToFirstModel(t *testing.T) {
	ctx := context.Background()
	r := newTestRouter(t)

	// With no authenticated provider the reserved alias falls back to a
	// catalog model. The concrete model is not asserted because Models()
	// draws from map-ordered provider catalogs; only that resolution
	// succeeds and yields a provider-qualified entry.
	got, err := r.GetModel(ctx, llmapi.ModelEntry{Name: llmapi.DefaultModel})
	require.NoError(t, err)
	assert.NotEmpty(t, got.Provider)
	assert.NotEmpty(t, got.Name)
}

func TestRouter_Alias_UnknownBareNameStillUnresolved(t *testing.T) {
	ctx := context.Background()
	r := newTestRouter(t)
	_, err := r.GetModel(ctx, llmapi.ModelEntry{Name: "gpt-5.5"})
	assert.ErrorIs(t, err, llmapi.ErrModelNotFound)
}

func TestRouter_Alias_UnsetReservedDefersToDefault(t *testing.T) {
	ctx := context.Background()
	r := newTestRouter(t)
	require.NoError(t, r.SetAlias(ctx, llmapi.DefaultModel, "openai/"+openai.GPT5Dot5))

	for _, name := range []string{"query", "compact", "dream"} {
		got, err := r.GetModel(ctx, llmapi.ModelEntry{Name: name})
		require.NoError(t, err, name)
		assert.Equal(t, ProviderOpenAI, got.Provider, name)
		assert.Equal(t, openai.GPT5Dot5, got.Name, name)
	}
}

func TestRouter_Alias_SetReservedOverridesDefault(t *testing.T) {
	ctx := context.Background()
	r := newTestRouter(t)
	require.NoError(t, r.SetAlias(ctx, llmapi.DefaultModel, "openai/"+openai.GPT5Dot5))
	require.NoError(t, r.SetAlias(ctx, "compact", "anthropic/"+anthropic.ClaudeOpus4Dot8))

	got, err := r.GetModel(ctx, llmapi.ModelEntry{Name: "compact"})
	require.NoError(t, err)
	assert.Equal(t, ProviderAnthropic, got.Provider)
	assert.Equal(t, anthropic.ClaudeOpus4Dot8, got.Name)
}

func TestRouter_Alias_ExplicitProviderBypassesAlias(t *testing.T) {
	ctx := context.Background()
	r := newTestRouter(t)
	require.NoError(t, r.SetAlias(ctx, "default", "openai/"+openai.GPT5Dot5))

	// "default" is not a real provider, so a qualified lookup fails
	// rather than resolving the alias.
	_, err := r.GetModel(ctx, llmapi.ModelEntry{Provider: ProviderAnthropic, Name: "default"})
	assert.ErrorIs(t, err, llmapi.ErrModelNotFound)
}
