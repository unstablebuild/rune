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

package semanticsearch

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"unstable.build/rune/internal/llm/llmsearch"
)

const (
	// wordLinesPerFile is the most lines of one file that rank it.
	wordLinesPerFile = 100
	// wordLines is the most lines that rank the files in all.
	wordLines = 20000
	// defaultLimit is how many files a request without a limit gets, as
	// searchapi.SemanticRequest says.
	defaultLimit = 20
	codePrefix   = "code:"
)

// wordSearch ranks the files by how many of the query's words they
// have, then by how many lines have them, through the regular expression
// index: unlike the semantic index, it has every file's words even when
// the embedding model cannot run. A file's hit is its line with the most
// of them, and its score the share of the words it has.
func wordSearch(ctx context.Context, s searchapi.Searcher, req searchapi.SemanticRequest) (
	[]searchapi.SemanticResult, error,
) {
	query := strings.TrimSpace(req.Query)
	if len(query) >= len(codePrefix) && strings.EqualFold(query[:len(codePrefix)], codePrefix) {
		query = query[len(codePrefix):]
	}
	words := llmsearch.Terms(query)
	if len(words) == 0 {
		return nil, nil
	}
	// The longest alternative goes first, or a word would only ever
	// match as the start of a longer one. Terms are letters and digits,
	// which need no quoting.
	alternatives := slices.SortedStableFunc(slices.Values(words), func(a, b string) int {
		return cmp.Compare(len(b), len(a))
	})
	it, err := s.RegexpSearch(ctx, searchapi.RegexpRequest{
		Pattern: strings.Join(alternatives, "|"), IgnoreCase: true, Roots: req.Roots,
		Limit: wordLines, MaxPerFile: wordLinesPerFile,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = it.Close() }()

	type file struct {
		path      string
		words     map[string]bool
		lines     int
		best      searchapi.RegexpResult
		bestWords int
	}
	byPath := map[string]*file{}
	var files []*file
	for {
		m, ok := it.Next(ctx)
		if !ok {
			break
		}
		f := byPath[m.Path]
		if f == nil {
			f = &file{path: m.Path, words: map[string]bool{}}
			byPath[m.Path] = f
			files = append(files, f)
		}
		f.lines++
		inLine := map[string]bool{}
		for _, span := range m.Spans {
			if w := strings.ToLower(m.Text[span[0]:span[1]]); slices.Contains(words, w) {
				inLine[w], f.words[w] = true, true
			}
		}
		if len(inLine) > f.bestWords {
			f.best, f.bestWords = m, len(inLine)
		}
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	slices.SortStableFunc(files, func(a, b *file) int {
		return cmp.Or(cmp.Compare(len(b.words), len(a.words)), cmp.Compare(b.lines, a.lines))
	})
	files = files[:min(cmp.Or(req.Limit, defaultLimit), len(files))]
	hits := make([]searchapi.SemanticResult, len(files))
	for i, f := range files {
		hits[i] = searchapi.SemanticResult{
			Path: f.path, Score: float64(len(f.words)) / float64(len(words)), KeywordRank: i + 1,
			Segment: "lines", Loc: f.best.Line, LocEnd: f.best.Line, Text: f.best.Text,
			Excerpt: markSpans(f.best),
		}
	}
	return hits, nil
}

// markSpans returns m's line with its matches «marked», as an excerpt
// marks its keywords.
func markSpans(m searchapi.RegexpResult) string {
	var b strings.Builder
	at := 0
	for _, span := range m.Spans {
		b.WriteString(m.Text[at:span[0]])
		b.WriteString("«")
		b.WriteString(m.Text[span[0]:span[1]])
		b.WriteString("»")
		at = span[1]
	}
	b.WriteString(m.Text[at:])
	return strings.TrimSpace(b.String())
}
