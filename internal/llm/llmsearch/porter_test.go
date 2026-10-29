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
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPorterStem_Golden(t *testing.T) {
	g := loadFTS5Golden(t)
	for _, p := range g.Porter {
		assert.Equal(t, p.Stems, tokenTexts(ftsTokens(p.Word)), "word %q", p.Word)
	}
}

func TestPorterStem(t *testing.T) {
	tests := []struct {
		word, want string
	}{
		{"caresses", "caress"},
		{"ponies", "poni"},
		{"agreed", "agre"},
		{"feed", "feed"},
		{"proceed", "proce"},
		{"hopping", "hop"},
		{"falling", "fall"},
		{"filing", "file"},
		{"relational", "relat"},
		{"generalizations", "gener"},
		{"ab", "ab"},
		{strings.Repeat("abc", 20) + "aing", strings.Repeat("abc", 20) + "a"},
		{strings.Repeat("abc", 20) + "abing", strings.Repeat("abc", 20) + "abing"},
	}
	for _, tt := range tests {
		t.Run(tt.word, func(t *testing.T) {
			in := []byte(tt.word)
			assert.Equal(t, tt.want, string(porterStem(in)))
			assert.Equal(t, tt.word, string(in), "the input is not modified")
		})
	}
}
