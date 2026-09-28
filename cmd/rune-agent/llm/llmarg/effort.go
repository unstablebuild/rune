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

package llmarg

import (
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/internal/llm/anthropic"
	"unstable.build/rune/internal/llm/gemini"
)

// DefaultEffort returns the reasoning effort entry runs at when the
// session sets none, so callers can name it instead of showing an
// opaque "default". It returns an empty string when the provider
// publishes no default, which is the common case: the request simply
// omits the parameter and the provider decides.
func DefaultEffort(entry llmapi.ModelEntry) llmapi.ReasoningEffort {
	switch entry.Provider {
	case anthropic.LLMProvider:
		return llmapi.ReasoningEffort(anthropic.DefaultEffortFor(entry.Name))
	case gemini.LLMProvider:
		return llmapi.ReasoningEffort(gemini.DefaultEffortFor(entry.Name))
	default:
		return ""
	}
}
