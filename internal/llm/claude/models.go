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
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/internal/llm/anthropic"
)

// LLMProvider identifies the Claude provider in the model registry. The
// claude provider disambiguates Anthropic models authenticated with a
// Claude Code subscription (OAuth) from the api-key `anthropic` provider,
// exactly as `codex` disambiguates from `openai`.
const LLMProvider = "claude"

// ModelEntries returns the static catalog of Claude models. It re-stamps
// the Anthropic catalog with Provider "claude" so callers can target the
// subscription-authenticated path while keeping identical model slugs and
// context windows.
func ModelEntries() []llmapi.ModelEntry {
	src := anthropic.ModelEntries()
	out := make([]llmapi.ModelEntry, 0, len(src))
	for _, entry := range src {
		entry.Provider = LLMProvider
		out = append(out, entry)
	}
	return out
}

// FlagshipModel returns the provider's top model identifier. The slugs are
// identical to the api-key provider's, so it tracks the Anthropic flagship.
func FlagshipModel() string { return anthropic.FlagshipModel() }

// MaxOutputTokens returns the model's documented maximum output-token
// ceiling. It mirrors the Anthropic catalog since the slugs are identical.
func MaxOutputTokens(model string) int { return anthropic.MaxOutputTokens(model) }
