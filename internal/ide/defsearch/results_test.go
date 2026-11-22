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

package defsearch

import (
	"cmp"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"unstable.build/rune/internal/handler/locationsearch"
	"unstable.build/rune/internal/ide/syntax/symboldb"
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
	r := newResults(symboldb.DefinitionQuery{}, clock(3*time.Millisecond))
	assert.Equal(t, locationsearch.Annotation{Status: "Searching…"}, r.annotate(""))

	var entries []string
	for _, d := range []symboldb.Definition{
		{Path: "a.go", Line: 3, Col: 6, Kind: symboldb.KindFunction, Lang: "go", Name: "Run"},
		{Path: "a.go", Line: 9, Col: 19, Kind: symboldb.KindMethod, Lang: "go", Name: "Total"},
		{Path: "b/c.py", Line: 1, Col: 5, Kind: symboldb.KindFunction, Lang: "python", Name: "main"},
	} {
		e, ok := r.add(d)
		assert.True(t, ok)
		entries = append(entries, e)
	}
	_, ok := r.add(symboldb.Definition{Path: "a.go", Line: 9, Col: 19, Kind: symboldb.KindFunction, Lang: "go", Name: "Total"})
	assert.False(t, ok, "an entry is listed once")
	assert.Equal(t, []string{"a.go:3:6: Run", "a.go:9:19: Total", "b/c.py:1:5: main"}, entries)
	assert.Equal(t, locationsearch.Annotation{
		Status: "Searching… 3 definitions in 2 files", Detail: "method · go · 2 of 2 in file",
		First: 9, Last: 9, Terms: []string{"Total"},
	}, r.annotate(entries[1]))

	r.finish("still indexing")
	assert.Equal(t, locationsearch.Annotation{
		Status: "3 definitions in 2 files · 3ms · still indexing", Detail: "function · python · 1 of 1 in file",
		First: 1, Last: 1, Terms: []string{"main"},
	}, r.annotate(entries[2]))
}

func TestResultsStatus(t *testing.T) {
	tests := []struct {
		name  string
		req   symboldb.DefinitionQuery
		count int
		note  string
		err   error
		step  time.Duration
		want  string
	}{
		{name: "nothing", want: "No definitions · 1ms"},
		{name: "one", count: 1, want: "1 definition in 1 file · 1ms"},
		{name: "in seconds", count: 2, step: 1500 * time.Millisecond, want: "2 definitions in 1 file · 1.5s"},
		{
			name: "how it was asked for", req: symboldb.DefinitionQuery{Langs: []string{"go", "zig"}, Roots: []string{"cmd"}},
			count: 2, want: "2 definitions in 1 file · 1ms · go, zig · in cmd",
		},
		{name: "at the limit", req: symboldb.DefinitionQuery{Limit: 2}, count: 2, want: "2 definitions in 1 file · 1ms · stopped at the first 2 definitions"},
		{name: "below the limit", req: symboldb.DefinitionQuery{Limit: 3}, count: 2, want: "2 definitions in 1 file · 1ms"},
		{name: "noted", count: 1, note: "still indexing", want: "1 definition in 1 file · 1ms · still indexing"},
		{name: "failed", count: 1, err: errors.New("boom"), want: "Search failed: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newResults(tt.req, clock(cmp.Or(tt.step, time.Millisecond)))
			for n := range tt.count {
				r.add(symboldb.Definition{Path: "a", Line: n + 1, Col: 1, Kind: symboldb.KindVar, Name: "v"})
			}
			if tt.err != nil {
				r.fail(tt.err)
			} else {
				r.finish(tt.note)
			}
			got := r.annotate("")
			assert.Equal(t, tt.want, got.Status)
			assert.Equal(t, tt.err != nil, got.Error)
		})
	}
}
