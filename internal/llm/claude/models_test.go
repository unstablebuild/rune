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

package claude

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/llm/anthropic"
)

func TestFlagshipModelTracksAnthropic(t *testing.T) {
	assert.Equal(t, anthropic.FlagshipModel(), FlagshipModel())
	assert.Equal(t, anthropic.ClaudeOpus5Dot5, FlagshipModel())
}

func TestMaxOutputTokensMirrorsAnthropic(t *testing.T) {
	model := anthropic.ClaudeOpus5
	assert.Equal(t, anthropic.MaxOutputTokens(model), MaxOutputTokens(model))
	assert.Equal(t, 128000, MaxOutputTokens(model))
	assert.Equal(t, 0, MaxOutputTokens("unknown-model"))
}

func TestModelEntriesStampClaudeProvider(t *testing.T) {
	entries := ModelEntries()
	require.NotEmpty(t, entries)

	gotSlugs := make(map[string]bool, len(entries))
	var opus5ContextWindow int
	var sonnet5ContextWindow int
	var fable51ContextWindow int
	for _, e := range entries {
		assert.Equal(t, LLMProvider, e.Provider, "entry %q not stamped claude", e.Name)
		gotSlugs[e.Name] = true
		if e.Name == anthropic.ClaudeOpus5 {
			opus5ContextWindow = e.ContextWindow
		}
		if e.Name == anthropic.ClaudeSonnet5 {
			sonnet5ContextWindow = e.ContextWindow
		}
		if e.Name == anthropic.ClaudeFable5Dot1 {
			fable51ContextWindow = e.ContextWindow
		}
	}
	assert.Equal(t, 1000000, opus5ContextWindow)
	assert.Equal(t, 1000000, sonnet5ContextWindow)
	assert.Equal(t, 1000000, fable51ContextWindow)

	wantSlugs := make(map[string]bool)
	for _, e := range anthropic.ModelEntries() {
		wantSlugs[e.Name] = true
	}
	assert.Equal(t, wantSlugs, gotSlugs, "claude slugs must equal anthropic slugs")
}
