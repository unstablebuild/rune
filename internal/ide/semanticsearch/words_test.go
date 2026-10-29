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
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
)

func TestWordSearch(t *testing.T) {
	line := func(path string, n int, text string, spans ...[2]int) searchapi.RegexpResult {
		return searchapi.RegexpResult{Path: path, Line: n, Text: text, Spans: spans}
	}
	retryOnly := []searchapi.RegexpResult{
		line("a.go", 1, "retry()", [2]int{0, 5}),
		line("a.go", 9, "\tretry(); retry()", [2]int{1, 6}, [2]int{10, 15}),
	}
	both := []searchapi.RegexpResult{
		line("b.go", 4, "// Retry with backoff", [2]int{3, 8}, [2]int{14, 21}),
	}
	tests := []struct {
		name        string
		req         searchapi.SemanticRequest
		lines       []searchapi.RegexpResult
		regexpErr   error
		wantPattern string
		want        []searchapi.SemanticResult
		wantErr     string
	}{
		{
			name: "more words first, then more lines",
			req:  searchapi.SemanticRequest{Query: "where is the retry backoff", Roots: []string{"net"}},
			lines: append(append(append([]searchapi.RegexpResult{}, retryOnly...), both...),
				line("c.go", 2, "retry", [2]int{0, 5})),
			wantPattern: "backoff|retry",
			want: []searchapi.SemanticResult{
				{
					Path: "b.go", Score: 1, KeywordRank: 1, Segment: "lines", Loc: 4, LocEnd: 4,
					Text: "// Retry with backoff", Excerpt: "// «Retry» with «backoff»",
				},
				{
					Path: "a.go", Score: 0.5, KeywordRank: 2, Segment: "lines", Loc: 1, LocEnd: 1,
					Text: "retry()", Excerpt: "«retry»()",
				},
				{
					Path: "c.go", Score: 0.5, KeywordRank: 3, Segment: "lines", Loc: 2, LocEnd: 2,
					Text: "retry", Excerpt: "«retry»",
				},
			},
		},
		{
			name: "limit and code prefix", req: searchapi.SemanticRequest{Query: "Code: retry", Limit: 1},
			lines: retryOnly, wantPattern: "retry",
			want: []searchapi.SemanticResult{{
				Path: "a.go", Score: 1, KeywordRank: 1, Segment: "lines", Loc: 1, LocEnd: 1,
				Text: "retry()", Excerpt: "«retry»()",
			}},
		},
		{
			name: "longest word first", req: searchapi.SemanticRequest{Query: "retry retrying"},
			wantPattern: "retrying|retry", want: []searchapi.SemanticResult{},
		},
		{name: "no words", req: searchapi.SemanticRequest{Query: "where is the"}},
		{
			name: "search error", req: searchapi.SemanticRequest{Query: "retry"}, regexpErr: errors.New("bad"),
			wantPattern: "retry", wantErr: "bad",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &stubSearcher{lines: tt.lines, regexpErr: tt.regexpErr}
			got, err := wordSearch(context.Background(), s, tt.req)
			if tt.wantPattern == "" {
				assert.Nil(t, s.regexpRequest(), "nothing to search for")
			} else {
				assert.Equal(t, &searchapi.RegexpRequest{
					Pattern: tt.wantPattern, IgnoreCase: true, Roots: tt.req.Roots,
					Limit: wordLines, MaxPerFile: wordLinesPerFile,
				}, s.regexpRequest())
			}
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
