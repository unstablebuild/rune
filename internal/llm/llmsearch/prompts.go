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

package llmsearch

import (
	"strings"
	"unicode"
)

// codePrefix marks a search of code ("code: retry with backoff") in DigUp;
// every search here is one, so it is only stripped.
const codePrefix = "code:"

// queryPrompt is EmbeddingGemma's prompt for finding code by what it does.
func queryPrompt(text string) string { return "task: code retrieval | query: " + text }

// documentPrompt is EmbeddingGemma's prompt for a document: a stretch of
// code, titled with its path.
func documentPrompt(title, text string) string {
	if title == "" {
		title = "none"
	}
	return "title: " + title + " | text: " + text
}

// stripCodePrefix returns what is typed after a leading "code:" (any case),
// or q when it has none.
func stripCodePrefix(q string) string {
	trimmed := strings.TrimLeftFunc(q, unicode.IsSpace)
	if len(trimmed) < len(codePrefix) || !strings.EqualFold(trimmed[:len(codePrefix)], codePrefix) {
		return q
	}
	return strings.TrimSpace(trimmed[len(codePrefix):])
}
