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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"unstable.build/rune/internal/handler/locationsearch"
)

// cannedHits are four hits of every kind, best first: two strong, two
// weaker.
var cannedHits = []searchapi.SemanticResult{
	{
		Path: "billing/invoice.go", Segment: "lines", Loc: 40, LocEnd: 47, Focus: 42,
		Text: "\treturn roundInvoiceTotal(lines)", Excerpt: "func «invoice» «total»",
		Score: 3.14, Z: 2.5, Cosine: 0.61, KeywordRank: 2,
	},
	{
		Path: "geo/route.go", Segment: "lines", Loc: 7, LocEnd: 7,
		Text: "func shortestRoute(a, b Place) []Place {", Score: 2, KeywordRank: 1,
	},
	{Path: "notebooks/fit.ipynb", Segment: "cells", Loc: 3, LocEnd: 3, Excerpt: "fit the «model»", Score: 1},
	{Path: "docs/invoice.md", Segment: "name", Score: 0.5},
}

func TestResultsFound(t *testing.T) {
	const note = "3 files left to index"
	r := newResults("invoice total")
	assert.Equal(t, locationsearch.Annotation{Status: "Searching for “invoice total”…"}, r.annotate(""))

	entries := r.found(cannedHits, note, false)
	assert.Equal(t, []string{
		"billing/invoice.go:42: return roundInvoiceTotal(lines)",
		"geo/route.go:7:        func shortestRoute(a, b Place) []Place {",
		"notebooks/fit.ipynb:1: [cell 3] fit the model",
		"docs/invoice.md:1:     (path matches)",
	}, entries)
	assert.Equal(t, []locationsearch.Annotation{
		{
			Status: "score 3.14 · meaning z 2.50 cos 0.61 · words #2 · " + note, Detail: "lines 40–47",
			First: 40, Last: 47, Terms: []string{"invoice", "total"},
		},
		{Status: "score 2.00 · words #1 · " + note, Detail: "line 7", First: 7, Last: 7},
		{Status: "weaker · score 1.00 · " + note, Detail: "cell 3", Terms: []string{"model"}},
		{Status: "weaker · score 0.50 · " + note, Detail: "path"},
	}, []locationsearch.Annotation{
		r.annotate(entries[0]), r.annotate(entries[1]), r.annotate(entries[2]), r.annotate(entries[3]),
	})
	assert.Equal(t, locationsearch.Annotation{Status: "4 matches for “invoice total”, 2 strong · " + note}, r.annotate(""),
		"nothing focused describes the search")
}

func TestResultsFoundByWordsInstead(t *testing.T) {
	r := newResults("shortest route")
	entries := r.found(cannedHits[1:2], "", true)
	assert.Equal(t, locationsearch.Annotation{
		Status: fallbackStatus + " · score 2.00 · words #1", Detail: "line 7", Error: true, First: 7, Last: 7,
	}, r.annotate(entries[0]))
	assert.Equal(t, locationsearch.Annotation{
		Status: fallbackStatus + " · 1 match for “shortest route”, 1 strong", Error: true,
	}, r.annotate(""))

	r.found(nil, "", true)
	assert.Equal(t, locationsearch.Annotation{
		Status: fallbackStatus + " · Nothing matches “shortest route”", Error: true,
	}, r.annotate(""))
}

func TestResultsFoundEdgeCases(t *testing.T) {
	tests := []struct {
		name        string
		hits        []searchapi.SemanticResult
		wantEntries []string
		wantStatus  string
	}{
		{name: "nothing", wantStatus: "Nothing matches “q”"},
		{
			name:        "one",
			hits:        cannedHits[1:2],
			wantEntries: []string{"geo/route.go:7: func shortestRoute(a, b Place) []Place {"},
			wantStatus:  "1 match for “q”, 1 strong",
		},
		{
			name:        "same entry twice",
			hits:        []searchapi.SemanticResult{cannedHits[1], cannedHits[1]},
			wantEntries: []string{"geo/route.go:7: func shortestRoute(a, b Place) []Place {"},
			wantStatus:  "2 matches for “q”, 2 strong",
		},
		{
			name:        "lines without text",
			hits:        []searchapi.SemanticResult{{Path: "a.go", Segment: "lines", Loc: 3, Excerpt: "«retry» loop", Score: 1}},
			wantEntries: []string{"a.go:3: retry loop"},
			wantStatus:  "1 match for “q”, 1 strong",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newResults("q")
			assert.Equal(t, tt.wantEntries, nilIfEmpty(r.found(tt.hits, "", false)))
			assert.Equal(t, locationsearch.Annotation{Status: tt.wantStatus}, r.annotate(""))
		})
	}
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func TestResultsFail(t *testing.T) {
	r := newResults("q")
	r.fail(errors.New("boom"))
	assert.Equal(t, locationsearch.Annotation{Status: "Search failed: boom", Error: true}, r.annotate(""))
}

func TestMarked(t *testing.T) {
	tests := []struct {
		excerpt string
		want    []string
	}{
		{"", nil},
		{"no marks", nil},
		{"«one»", []string{"one"}},
		{"a «retry» then «backoff» loop", []string{"retry", "backoff"}},
		{"«» empty", nil},
		{"«unclosed", nil},
	}
	for _, tt := range tests {
		t.Run(tt.excerpt, func(t *testing.T) {
			assert.Equal(t, tt.want, marked(tt.excerpt))
		})
	}
}
