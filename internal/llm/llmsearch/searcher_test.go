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
	"bufio"
	"cmp"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
)

// paraphraseEmbedder stands for a model that knows what a paraphrase
// means: it embeds a query as the words it paraphrases.
type paraphraseEmbedder struct {
	fakeEmbedder
	means map[string]string
}

func (e paraphraseEmbedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([]string, len(texts))
	for i, text := range texts {
		out[i] = text
		if words, ok := e.means[text]; ok {
			out[i] = words
		}
	}
	return e.fakeEmbedder.EmbedTexts(ctx, out)
}

type searchCase struct {
	query      string
	meaning    bool
	prefixLast bool
	roots      []string
}

func (ti *testIndexer) search(t *testing.T, embed Embedder, c searchCase) []searchapi.SemanticResult {
	t.Helper()
	ctx := context.Background()
	text := stripCodePrefix(c.query)
	var query []float32
	if c.meaning {
		vecs, err := embed.EmbedTexts(ctx, []string{queryPrompt(text)})
		require.NoError(t, err)
		query = vecs[0]
		require.NoError(t, ti.x.cat.loadVectors(ctx, ti.x.st))
	}
	var allowed func(string) bool
	if c.roots != nil {
		allowed = func(p string) bool {
			for _, r := range c.roots {
				if strings.HasPrefix(p, r+"/") {
					return true
				}
			}
			return false
		}
	}
	hits, err := newSearcher(ti.x.st, ti.x.cat).search(ctx, text, query, allowed, defaultLimit, c.prefixLast)
	require.NoError(t, err)
	return hits
}

func indexedFixture(t *testing.T) *testIndexer {
	t.Helper()
	ti := newTestIndexer(t)
	ti.index(t, fakeEmbedder{})
	return ti
}

// lineOf is the 1-based line of the fixture file rel that starts with
// prefix.
func lineOf(t *testing.T, dir, rel, prefix string) int {
	t.Helper()
	f, err := os.Open(filepath.Join(dir, rel))
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	s := bufio.NewScanner(f)
	for n := 1; s.Scan(); n++ {
		if strings.HasPrefix(s.Text(), prefix) {
			return n
		}
	}
	require.NoError(t, s.Err())
	t.Fatalf("%s has no line starting with %q", rel, prefix)
	return 0
}

// fixtureLine is the 1-based line n of the fixture file rel.
func fixtureLine(t *testing.T, dir, rel string, n int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	require.Less(t, n-1, len(lines), "%s has no line %d", rel, n)
	return lines[n-1]
}

func TestSearchByMeaningAlone(t *testing.T) {
	ti := indexedFixture(t)
	query := "how far apart are two places on the globe"
	embed := paraphraseEmbedder{means: map[string]string{
		queryPrompt(query): "haversine great circle distance kilometres earth radius latitude longitude",
	}}

	hits := ti.search(t, embed, searchCase{query: query, meaning: true})
	require.NotEmpty(t, hits)
	top := hits[0]
	assert.Equal(t, "geo/distance.py", top.Path)
	assert.GreaterOrEqual(t, top.Z, zGate)
	assert.Positive(t, top.Cosine)
	assert.Equal(t, segmentLines, top.Segment)
	assert.Equal(t, 1, top.Loc)
	assert.Equal(t, fixtureLine(t, ti.dir, "geo/distance.py", cmp.Or(top.Focus, top.Loc)), top.Text)
	assert.Equal(t, kindCode, top.Kind)
	assert.Positive(t, top.Size)
	assert.Positive(t, top.Modified)

	words := ti.search(t, embed, searchCase{query: query})
	assert.NotContains(t, paths(words), "geo/distance.py", "its words alone do not qualify the file")
}

func TestSearchByWordsAlone(t *testing.T) {
	ti := indexedFixture(t)
	hits := ti.search(t, fakeEmbedder{}, searchCase{query: "INV7Q3X42", meaning: true})
	require.Len(t, hits, 1)
	hit := hits[0]
	assert.Equal(t, "billing/invoice.py", hit.Path)
	assert.Less(t, hit.Z, zGate, "the code does not stand out by meaning")
	assert.Equal(t, 1, hit.KeywordRank)
	assert.InDelta(t, 0.5*max(hit.Z, 0)+2, hit.Score, 1e-9, "a lookup weighs words double and meaning half")
	assert.Equal(t, lineOf(t, ti.dir, "billing/invoice.py", "LATE_FEE_CODE"), hit.Focus)
	assert.Equal(t, fixtureLine(t, ti.dir, "billing/invoice.py", hit.Focus), hit.Text)
	assert.True(t, strings.HasPrefix(hit.Text, "LATE_FEE_CODE"), hit.Text)
	assert.Contains(t, hit.Excerpt, "«INV7Q3X42»")
}

func TestSearchLooksUpWhereANameIsDefined(t *testing.T) {
	ti := indexedFixture(t)
	for _, query := range []string{"parseRetryBackoff", "parseRetryBackoff()", "code: parseRetryBackoff"} {
		t.Run(query, func(t *testing.T) {
			hits := ti.search(t, fakeEmbedder{}, searchCase{query: query, meaning: true})
			byPath := map[string]searchapi.SemanticResult{}
			for _, h := range hits {
				byPath[h.Path] = h
			}
			def, ok := byPath["retry/backoff.go"]
			require.True(t, ok, "%v", paths(hits))
			assert.Equal(t, lineOf(t, ti.dir, "retry/backoff.go", "func parseRetryBackoff"), def.Focus,
				"the defining line, not a call")
			assert.LessOrEqual(t, def.Loc, def.Focus)
			assert.GreaterOrEqual(t, def.LocEnd, def.Focus)
			assert.Equal(t, "func parseRetryBackoff(spec string) (time.Duration, float64, error) {", def.Excerpt)
			assert.Equal(t, "func parseRetryBackoff(spec string) (time.Duration, float64, error) {", def.Text)

			use, ok := byPath["retry/policy.go"]
			require.True(t, ok)
			assert.Equal(t, lineOf(t, ti.dir, "retry/policy.go", "\tfirst, factor, _ := parseRetryBackoff"), use.Focus,
				"a file that only calls it opens at the first call")
			assert.Equal(t, fixtureLine(t, ti.dir, "retry/policy.go", use.Focus), use.Text, "as written, indented")
		})
	}
}

func TestSearchPrefixLast(t *testing.T) {
	ti := indexedFixture(t)
	typing := ti.search(t, fakeEmbedder{}, searchCase{query: "exponential backoff ji", prefixLast: true})
	done := ti.search(t, fakeEmbedder{}, searchCase{query: "exponential backoff ji"})
	require.NotEmpty(t, typing)
	require.NotEmpty(t, done)
	assert.Equal(t, "retry/backoff.go", typing[0].Path)
	assert.Equal(t, "retry/backoff.go", done[0].Path)
	assert.Greater(t, typing[0].Score, done[0].Score, "the word being typed matches as a prefix")
	assert.Contains(t, typing[0].Excerpt, "«jitter»")
	assert.NotContains(t, done[0].Excerpt, "«jitter»", "a word of two letters is not a prefix")
}

func TestSearchUnindexedWordMatchesAsAPrefix(t *testing.T) {
	ti := indexedFixture(t)
	hits := ti.search(t, fakeEmbedder{}, searchCase{query: "evic"})
	require.NotEmpty(t, hits)
	assert.Equal(t, "storage/cache.rs", hits[0].Path)
	assert.Contains(t, hits[0].Excerpt, "«evicting»")
}

func TestSearchEqualScoresInFileOrder(t *testing.T) {
	ti := indexedFixture(t)
	hits := ti.search(t, fakeEmbedder{}, searchCase{query: "palindrome"})
	require.Len(t, hits, 2, "vendored, generated and ignored files are not indexed")
	assert.Equal(t, []string{"dup/a.go", "dup/b.go"}, paths(hits))
	assert.Equal(t, hits[0].Score, hits[1].Score)
}

func TestSearchWithoutMeaning(t *testing.T) {
	ti := indexedFixture(t)
	for _, query := range []string{"great circle radius earth", "INV7Q3X42", "parseRetryBackoff", "palindrome"} {
		hits := ti.search(t, fakeEmbedder{}, searchCase{query: query})
		require.NotEmpty(t, hits, query)
		for _, h := range hits {
			assert.Zero(t, h.Z, "%s: %s", query, h.Path)
			assert.Zero(t, h.Cosine, "%s: %s", query, h.Path)
		}
	}
}

func TestSearchWithinRoots(t *testing.T) {
	ti := indexedFixture(t)
	hits := ti.search(t, fakeEmbedder{}, searchCase{query: "tax", roots: []string{"billing"}})
	require.NotEmpty(t, hits)
	for _, h := range hits {
		assert.True(t, strings.HasPrefix(h.Path, "billing/"), h.Path)
	}
	assert.Empty(t, ti.search(t, fakeEmbedder{}, searchCase{query: "tax", roots: []string{"geo"}}))
}

func TestSearchByFileName(t *testing.T) {
	ti := indexedFixture(t)
	hits := ti.search(t, fakeEmbedder{}, searchCase{query: "analysis"})
	require.NotEmpty(t, hits)
	assert.Equal(t, "notebooks/analysis.ipynb", hits[0].Path)
	assert.Equal(t, segmentName, hits[0].Segment)
	assert.Zero(t, hits[0].Loc)
	assert.Empty(t, hits[0].Text)
}

func TestSearchFindsNothing(t *testing.T) {
	ti := indexedFixture(t)
	assert.Empty(t, ti.search(t, fakeEmbedder{}, searchCase{query: "zzyzx quokka", meaning: true}))
	assert.Empty(t, ti.search(t, fakeEmbedder{}, searchCase{query: "  ", meaning: true}))
}

func TestStrongCount(t *testing.T) {
	hits := func(scores ...float64) []searchapi.SemanticResult {
		out := make([]searchapi.SemanticResult, len(scores))
		for i, s := range scores {
			out[i].Score = s
		}
		return out
	}
	tests := []struct {
		name string
		hits []searchapi.SemanticResult
		want int
	}{
		{"none", nil, 0},
		{"all strong", hits(4, 3, 2), 3},
		{"weaker trail", hits(4, 2.5, 1.9, 1), 2},
		{"zero top", hits(0, 0), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, StrongCount(tt.hits, 0.5))
		})
	}
}

func TestExcerpt(t *testing.T) {
	tests := []struct {
		name, text string
		limit      int
		want       string
	}{
		{"collapses whitespace", "  a\n\tb   c  ", 10, "a b c"},
		{"cuts long lines", "abcdefghij", 4, "abcd…"},
		{"marks a cut text", strings.Repeat("x ", 50), 5, "x x x…"},
		{"counts characters", "ééé", 3, "ééé"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, excerpt(tt.text, tt.limit))
		})
	}
}
