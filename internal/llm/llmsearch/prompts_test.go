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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripCodePrefix(t *testing.T) {
	tests := []struct{ in, want string }{
		{"code: retry with backoff", "retry with backoff"},
		{"  Code:useDebounce", "useDebounce"},
		{"code:", ""},
		{"code review notes", "code review notes"},
		{"the code: 1234", "the code: 1234"},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, stripCodePrefix(tt.in))
		})
	}
}

func TestPrompts(t *testing.T) {
	assert.Equal(t, "task: code retrieval | query: retry", queryPrompt("retry"))
	assert.Equal(t, "title: rune/a.go | text: x", documentPrompt("rune/a.go", "x"))
	assert.Equal(t, "title: none | text: x", documentPrompt("", "x"))
}
