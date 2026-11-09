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

package regexsearch

import (
	"cmp"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"unstable.build/rune/internal/handler/locationsearch"
)

// clock is a search's clock that advances by step at every reading.
func clock(step time.Duration) func() time.Time {
	now := time.Unix(0, 0)
	return func() time.Time {
		now = now.Add(step)
		return now
	}
}

func TestResults(t *testing.T) {
	r := newResults(searchapi.RegexpRequest{Pattern: "retry.*"}, clock(3*time.Millisecond))
	searching := "Searching for “retry.*”…"
	assert.Equal(t, locationsearch.Annotation{Status: searching}, r.annotate(""))

	entries := []string{
		r.add(searchapi.RegexpResult{Path: "a.go", Line: 3, Text: "\t// retry, retry later", Spans: [][2]int{{4, 22}}}),
		r.add(searchapi.RegexpResult{Path: "a.go", Line: 9, Text: "é retry ab retry", Spans: [][2]int{{3, 8}, {12, 17}}}),
		r.add(searchapi.RegexpResult{Path: "b/c.go", Line: 1, Text: "retry", Spans: [][2]int{{0, 5}}}),
	}
	assert.Equal(t, []string{
		"a.go:3:5: // retry, retry later",
		"a.go:9:3: é retry ab retry",
		"b/c.go:1:1: retry",
	}, entries)
	assert.Equal(t, locationsearch.Annotation{
		Status: searching + " 3 lines in 2 files", Detail: "2 of 2 in file · 9:3",
		First: 9, Last: 9, Terms: []string{"retry"},
	}, r.annotate(entries[1]))

	r.finish("still indexing")
	assert.Equal(t, locationsearch.Annotation{
		Status: "3 lines in 2 files · 3ms · still indexing", Detail: "1 of 2 in file · 3:5",
		First: 3, Last: 3, Terms: []string{"retry, retry later"},
	}, r.annotate(entries[0]))
	assert.Equal(t, locationsearch.Annotation{Status: "3 lines in 2 files · 3ms · still indexing"}, r.annotate(""))
}

func TestResultsStatus(t *testing.T) {
	tests := []struct {
		name    string
		req     searchapi.RegexpRequest
		matches int
		note    string
		err     error
		step    time.Duration
		want    string
	}{
		{name: "nothing", want: "Nothing matches “q” · 1ms"},
		{name: "nothing, slowly", note: "still indexing", want: "Nothing matches “q” · 1ms · still indexing"},
		{name: "one", req: searchapi.RegexpRequest{Limit: 2}, matches: 1, want: "1 line in 1 file · 1ms"},
		{name: "in seconds", matches: 2, step: 1500 * time.Millisecond, want: "2 lines in 1 file · 1.5s"},
		{
			name: "how it was asked for", req: searchapi.RegexpRequest{IgnoreCase: true, Roots: []string{"cmd", "internal"}},
			matches: 2, want: "2 lines in 1 file · 1ms · ignore case · in cmd, internal",
		},
		{
			name: "at the limit", req: searchapi.RegexpRequest{Limit: 2}, matches: 2,
			want: "2 lines in 1 file · 1ms · stopped at the first 2 lines",
		},
		{
			name: "at a limit of one", req: searchapi.RegexpRequest{Limit: 1}, matches: 1,
			want: "1 line in 1 file · 1ms · stopped at the first line",
		},
		{
			name: "at the limit, slowly", req: searchapi.RegexpRequest{Limit: 2}, matches: 2, note: "still indexing",
			want: "2 lines in 1 file · 1ms · stopped at the first 2 lines · still indexing",
		},
		{name: "failed", matches: 1, err: errors.New("boom"), want: "Search failed: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.req.Pattern = "q"
			r := newResults(tt.req, clock(cmp.Or(tt.step, time.Millisecond)))
			var last string
			for n := range tt.matches {
				last = r.add(searchapi.RegexpResult{Path: "a", Line: n + 1, Text: "q", Spans: [][2]int{{0, 1}}})
			}
			if tt.err != nil {
				r.fail(tt.err)
			} else {
				r.finish(tt.note)
			}
			got := r.annotate("")
			assert.Equal(t, tt.want, got.Status)
			assert.Equal(t, tt.err != nil, got.Error, "a failure is an error")
			if last != "" {
				focused := r.annotate(last)
				assert.Equal(t, got.Status, focused.Status, "a focused entry says the same")
				assert.Equal(t, got.Error, focused.Error, "a focused entry says the same")
			}
		})
	}
}

func TestShown(t *testing.T) {
	assert.Equal(t, "x := 1", shown("\t  x := 1"))
	long := strings.Repeat("a", maxShown-1) + "éb"
	assert.Equal(t, strings.Repeat("a", maxShown-1)+"…", shown(long), "a long line is cut between runes")
}
