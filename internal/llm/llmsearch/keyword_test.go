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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseMatch reads the FTS5 queries DigUp writes: quoted phrases, each
// optionally followed by *, joined by OR.
func parseMatch(t *testing.T, q string) []phrase {
	t.Helper()
	var out []phrase
	for part := range strings.SplitSeq(q, " OR ") {
		prefix := strings.HasSuffix(part, "*")
		part = strings.TrimSuffix(part, "*")
		require.True(t, strings.HasPrefix(part, `"`) && strings.HasSuffix(part, `"`), "query %q", q)
		out = append(out, newPhrase(strings.Trim(part, `"`), prefix))
	}
	return out
}

type testRows map[uint32][2]string

func (rows testRows) text(_ context.Context, row uint32) (string, string, bool, error) {
	r, ok := rows[row]
	return r[0], r[1], ok, nil
}

func goldenIndex(t *testing.T, g fts5Golden) (*keywordIndex, testRows) {
	t.Helper()
	k := newKeywordIndex()
	rows := testRows{}
	for _, r := range g.Rows {
		rows[r.RowID] = [2]string{r.Name, r.Body}
		k.add(r.RowID, r.RowID, r.Name, r.Body)
	}
	return k, rows
}

func TestKeywordIndex_Golden(t *testing.T) {
	g := loadFTS5Golden(t)
	k, rows := goldenIndex(t, g)

	// A row added and removed again leaves no trace in the statistics.
	k.add(999, 999, "the extra row", "the data pipeline the cat")
	k.add(999, 999, "the extra row", "replaced: the data pipeline the cat")
	k.remove(999)
	k.remove(1000)

	for _, q := range g.Queries {
		t.Run(q.Match, func(t *testing.T) {
			phrases := parseMatch(t, q.Match)
			m, err := k.match(context.Background(), phrases, rows.text)
			require.NoError(t, err)
			require.Len(t, m.rows, len(q.Hits))
			for i, want := range q.Hits {
				got := m.rows[i]
				assert.Equal(t, want.RowID, got.row, "hit %d", i)
				assert.InDelta(t, want.BM25, -got.score, 1e-9, "hit %d row %d", i, got.row)
				assert.Equal(t, want.Snippet, snippet(rows[got.row][1], phrases, "«", "»", "…", 12), "row %d", got.row)
			}
		})
	}
}

func TestKeywordIndex_IsIndexed(t *testing.T) {
	k, rows := goldenIndex(t, loadFTS5Golden(t))
	tests := []struct {
		text   string
		prefix bool
		want   bool
	}{
		{"cat", false, true},
		{"groki", false, false},
		{"groki", true, true},
		{"quantize row q8", false, true},
		{"row quantize", false, false},
		{"zzzz", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got, err := k.isIndexed(context.Background(), newPhrase(tt.text, tt.prefix), rows.text)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestKeywordIndex_PhraseOfRemovedRow(t *testing.T) {
	k := newKeywordIndex()
	rows := testRows{1: {"", "quantize row ref"}, 2: {"", "quantize row ref"}}
	k.add(1, 1, "", rows[1][1])
	k.add(2, 2, "", rows[2][1])
	delete(rows, 2)

	m, err := k.match(context.Background(), []phrase{newPhrase("quantize row", false)}, rows.text)
	require.NoError(t, err)
	require.Len(t, m.rows, 1)
	assert.Equal(t, uint32(1), m.rows[0].row)
}

func TestKeywordMatch_SearchAndCoverage(t *testing.T) {
	k := newKeywordIndex()
	rows := testRows{}
	add := func(row, file uint32, body string) {
		rows[row] = [2]string{"", body}
		k.add(row, file, "", body)
	}
	add(1, 10, "retry retry retry backoff")
	add(2, 10, "retry")
	add(3, 20, "retry backoff")
	add(4, 30, "backoff")
	add(5, 40, "retry")
	add(6, 50, "unrelated words")

	m, err := k.match(context.Background(), []phrase{newPhrase("retry", false), newPhrase("backoff", false)}, rows.text)
	require.NoError(t, err)
	all := func(uint32) bool { return true }

	tests := []struct {
		name     string
		allowed  func(uint32) bool
		maxRows  int
		maxFiles int
		want     []uint32
	}{
		{"distinct files in rank order", all, 1000, 100, []uint32{20, 10, 30, 40}},
		{"max files", all, 1000, 2, []uint32{20, 10}},
		{"max rows", all, 1, 100, []uint32{20}},
		{"allowed", func(f uint32) bool { return f != 20 }, 1000, 100, []uint32{10, 30, 40}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var files []uint32
			for i, h := range m.search(tt.allowed, tt.maxRows, tt.maxFiles) {
				assert.Equal(t, i+1, h.rank)
				files = append(files, h.file)
			}
			assert.Equal(t, tt.want, files)
		})
	}

	assert.Equal(t, map[uint32]float64{10: 1, 20: 1, 40: 0.5}, m.coverage([]uint32{10, 20, 40, 50}))
}

func TestSnippet(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		phrases []phrase
		want    string
	}{
		{
			name:    "no instances shows the start",
			text:    "one two three four five six seven eight nine ten eleven twelve thirteen",
			phrases: []phrase{newPhrase("zzz", false)},
			want:    "one two three four five six seven eight nine ten eleven twelve…",
		},
		{
			name:    "short text is whole",
			text:    "  the cat sat.  ",
			phrases: []phrase{newPhrase("cat", false)},
			want:    "  the «cat» sat.  ",
		},
		{
			name:    "adjacent instances of a phrase coalesce",
			text:    "data data data pipeline",
			phrases: []phrase{newPhrase("data data", false)},
			want:    "«data data data» pipeline",
		},
		{
			name:    "empty text",
			text:    "",
			phrases: []phrase{newPhrase("cat", false)},
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, snippet(tt.text, tt.phrases, "«", "»", "…", 12))
		})
	}
}
