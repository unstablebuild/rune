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

package ide

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
)

// recordingRegexpSearch answers with results, or fails with err, and
// records the request it got.
type recordingRegexpSearch struct {
	results []searchapi.RegexpResult
	err     error
	got     *searchapi.RegexpRequest
}

func (r *recordingRegexpSearch) RegexpSearch(_ context.Context, req searchapi.RegexpRequest) (
	sdkiterator.Iterator[searchapi.RegexpResult], error,
) {
	r.got = &req
	if r.err != nil {
		return nil, r.err
	}
	return sdkiterator.FromSlice(r.results), nil
}

func TestNewWorkspaceSearcherPanicsWithoutDependencies(t *testing.T) {
	tests := []struct {
		name   string
		dir    string
		regexp regexpSearcher
	}{
		{"no folder", "", noRegexpSearch{}},
		{"no regexp search", "/w", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Panics(t, func() { newWorkspaceSearcher(tt.dir, tt.regexp) })
		})
	}
}

func TestWorkspaceSearcherRegexpSearch(t *testing.T) {
	const dir = "/w/project"
	results := []searchapi.RegexpResult{{Path: "a.go", Line: 1, Text: "retry", Spans: [][2]int{{0, 5}}}}
	req := searchapi.RegexpRequest{
		Pattern: "retry", IgnoreCase: true, Include: []string{"*.go"}, Limit: 3, MaxPerFile: 1,
	}
	tests := []struct {
		name      string
		roots     []string
		err       error
		wantRoots []string
		wantErr   string
		wantIs    error
	}{
		{name: "the whole workspace", roots: nil, wantRoots: nil},
		{name: "relative roots", roots: []string{"a/../b/", "./.github"}, wantRoots: []string{"b", ".github"}},
		{
			name:      "absolute roots in the workspace",
			roots:     []string{dir, dir + "/cmd/main.go", dir + "/.github/"},
			wantRoots: []string{".", "cmd/main.go", ".github"},
		},
		{
			name:    "an absolute root outside",
			roots:   []string{"a", "/w/other"},
			wantErr: "outside the workspace",
			wantIs:  searchapi.ErrUnsupported,
		},
		{
			name:    "a relative root outside",
			roots:   []string{"../project-old"},
			wantErr: "outside the workspace",
			wantIs:  searchapi.ErrUnsupported,
		},
		{
			name:    "no index",
			err:     fmt.Errorf("no index: %w", searchapi.ErrUnsupported),
			wantIs:  searchapi.ErrUnsupported,
			wantErr: "no index",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			regexp := &recordingRegexpSearch{results: results, err: tt.err}
			s := newWorkspaceSearcher(dir, regexp)
			req := req
			req.Roots = tt.roots

			it, err := s.RegexpSearch(context.Background(), req)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				if tt.wantIs != nil {
					assert.ErrorIs(t, err, tt.wantIs)
				}
				return
			}
			require.NoError(t, err)
			got, err := sdkiterator.ToSlice(context.Background(), it)
			require.NoError(t, err)
			assert.Equal(t, results, got)
			want := req
			want.Roots = tt.wantRoots
			assert.Equal(t, want, *regexp.got)
		})
	}
}

func TestWorkspaceSearcherSemanticSearch(t *testing.T) {
	s := newWorkspaceSearcher("/w/project", noRegexpSearch{})
	_, err := s.SemanticSearch(context.Background(), searchapi.SemanticRequest{Query: "sales tax"})
	assert.ErrorIs(t, err, searchapi.ErrUnsupported)
}

func TestNoRegexpSearch(t *testing.T) {
	_, err := noRegexpSearch{}.RegexpSearch(context.Background(), searchapi.RegexpRequest{Pattern: "x"})
	assert.ErrorIs(t, err, searchapi.ErrUnsupported)
}
