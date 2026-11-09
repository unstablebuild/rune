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

package codesearch

import (
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
)

func TestMatchLines(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		data    string
		limit   int
		want    []searchapi.RegexpResult
	}{
		{
			name:    "one match per line",
			pattern: "foo",
			data:    "foo\nbar\nxfoo\n",
			want: []searchapi.RegexpResult{
				{Path: "f", Line: 1, Text: "foo", Spans: [][2]int{{0, 3}}},
				{Path: "f", Line: 3, Text: "xfoo", Spans: [][2]int{{1, 4}}},
			},
		},
		{
			name:    "every match of a line",
			pattern: "o+",
			data:    "a\nfoo boo\n",
			want:    []searchapi.RegexpResult{{Path: "f", Line: 2, Text: "foo boo", Spans: [][2]int{{1, 3}, {5, 7}}}},
		},
		{
			name:    "last line without a newline",
			pattern: "end",
			data:    "a\nb\nthe end",
			want:    []searchapi.RegexpResult{{Path: "f", Line: 3, Text: "the end", Spans: [][2]int{{4, 7}}}},
		},
		{
			name:    "carriage returns are not text",
			pattern: "b",
			data:    "ab\r\ncb\r\n",
			want: []searchapi.RegexpResult{
				{Path: "f", Line: 1, Text: "ab", Spans: [][2]int{{1, 2}}},
				{Path: "f", Line: 2, Text: "cb", Spans: [][2]int{{1, 2}}},
			},
		},
		{
			name:    "anchors match by line",
			pattern: "^b",
			data:    "ab\nba\n",
			want:    []searchapi.RegexpResult{{Path: "f", Line: 2, Text: "ba", Spans: [][2]int{{0, 1}}}},
		},
		{
			name:    "a match across lines does not count",
			pattern: `a\sb`,
			data:    "xa\nb\na b\n",
			want:    []searchapi.RegexpResult{{Path: "f", Line: 3, Text: "a b", Spans: [][2]int{{0, 3}}}},
		},
		{
			name:    "a match after one across lines on the same line counts",
			pattern: `a\s*b`,
			data:    "a ab\nb\n",
			want:    []searchapi.RegexpResult{{Path: "f", Line: 1, Text: "a ab", Spans: [][2]int{{2, 4}}}},
		},
		{
			name:    "an empty match matches the line",
			pattern: "^",
			data:    "a\n\nb\n",
			want: []searchapi.RegexpResult{
				{Path: "f", Line: 1, Text: "a", Spans: [][2]int{{0, 0}}},
				{Path: "f", Line: 2, Text: "", Spans: [][2]int{{0, 0}}},
				{Path: "f", Line: 3, Text: "b", Spans: [][2]int{{0, 0}}},
			},
		},
		{
			name:    "an empty file has no lines",
			pattern: "^",
			data:    "",
		},
		{
			name:    "nothing matches",
			pattern: "zzz",
			data:    "a\nb\n",
		},
		{
			name:    "up to a limit",
			pattern: "o",
			data:    "o\nx\no o\no\n",
			limit:   2,
			want: []searchapi.RegexpResult{
				{Path: "f", Line: 1, Text: "o", Spans: [][2]int{{0, 1}}},
				{Path: "f", Line: 3, Text: "o o", Spans: [][2]int{{0, 1}, {2, 3}}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			re := regexp.MustCompile("(?m)" + tt.pattern)
			assert.Equal(t, tt.want, matchLines(re, "f", []byte(tt.data), tt.limit))
		})
	}
}

func TestIncluded(t *testing.T) {
	paths := []string{"a.go", "dir/b_test.go", "dir/c.md", "Makefile"}
	tests := []struct {
		name    string
		globs   []string
		want    []string
		wantErr string
	}{
		{name: "every file", want: paths},
		{name: "by base name", globs: []string{"*.go"}, want: []string{"a.go", "dir/b_test.go"}},
		{name: "any glob", globs: []string{"*_test.go", "Makefile"}, want: []string{"dir/b_test.go", "Makefile"}},
		{name: "not by folder", globs: []string{"dir/*"}, want: []string{}},
		{name: "malformed", globs: []string{"*.go", "[a-"}, wantErr: `include "[a-"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := included(slices.Clone(paths), tt.globs)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
