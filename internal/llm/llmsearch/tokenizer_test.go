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
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fts5Golden struct {
	Rows []struct {
		RowID   uint32 `json:"rowid"`
		Name    string `json:"name"`
		Body    string `json:"body"`
		Raw     fts5GoldenColumns
		Stemmed fts5GoldenColumns
	} `json:"rows"`
	Porter []struct {
		Word  string   `json:"word"`
		Stems []string `json:"stems"`
	} `json:"porter"`
	Queries []struct {
		Match string `json:"match"`
		Hits  []struct {
			RowID   uint32  `json:"rowid"`
			BM25    float64 `json:"bm25"`
			Snippet string  `json:"snippet"`
		} `json:"hits"`
	} `json:"queries"`
}

type fts5GoldenColumns struct {
	Name []string `json:"name"`
	Body []string `json:"body"`
}

func loadFTS5Golden(t *testing.T) fts5Golden {
	t.Helper()
	data, err := os.ReadFile("testdata/fts5/golden.json")
	require.NoError(t, err)
	var g fts5Golden
	require.NoError(t, json.Unmarshal(data, &g))
	require.NotEmpty(t, g.Rows)
	return g
}

func unicode61Texts(text string) []string {
	out := []string{}
	unicode61(text, func(tok []byte, _, _ int) { out = append(out, string(tok)) })
	return out
}

func TestUnicode61_Golden(t *testing.T) {
	for _, row := range loadFTS5Golden(t).Rows {
		assert.Equal(t, row.Raw.Name, unicode61Texts(row.Name), "row %d name %q", row.RowID, row.Name)
		assert.Equal(t, row.Raw.Body, unicode61Texts(row.Body), "row %d body %q", row.RowID, row.Body)
	}
}

func TestFTSTokens_Golden(t *testing.T) {
	for _, row := range loadFTS5Golden(t).Rows {
		assert.Equal(t, row.Stemmed.Name, tokenTexts(ftsTokens(row.Name)), "row %d name %q", row.RowID, row.Name)
		assert.Equal(t, row.Stemmed.Body, tokenTexts(ftsTokens(row.Body)), "row %d body %q", row.RowID, row.Body)
	}
}

func TestFTSTokens(t *testing.T) {
	tests := []struct {
		name string
		text string
		want []token
	}{
		{
			name: "ascii case folds and offsets are bytes",
			text: "Hello, WORLD",
			want: []token{{"hello", 0, 5}, {"world", 7, 12}},
		},
		{
			name: "diacritics are removed",
			text: "Café naïve",
			want: []token{{"cafe", 0, 5}, {"naiv", 6, 12}},
		},
		{
			name: "a bengali word keeps its vowel signs",
			text: "কলকাতায় যাই",
			want: []token{{"কলকাতায়", 0, 24}, {"যাই", 25, 34}},
		},
		{
			name: "arabic harakat stay in the word",
			text: "قَالَ",
			want: []token{{"قَالَ", 0, 10}},
		},
		{
			name: "underscores and punctuation separate",
			text: "quantize_row_q8_0_ref()",
			want: []token{{"quantiz", 0, 8}, {"row", 9, 12}, {"q8", 13, 15}, {"0", 16, 17}, {"ref", 18, 21}},
		},
		{
			name: "empty text has no tokens",
			text: "  ...  ",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ftsTokens(tt.text))
		})
	}
}
