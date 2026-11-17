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

package treesitter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestNormalizeInjectionLanguage(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{name: "typescript", want: "typescript"},
		{name: "ts", want: "typescript"},
		{name: "js", want: "javascript"},
		{name: "javascript", want: "javascript"},
		{name: "sh", want: "bash"},
		{name: "css", want: "css"},
		{name: "comment", want: "comment"},
		{name: " scss ", want: "scss"},
		{name: "", want: ""},
		{name: "text/css", want: ""},
		{name: "c sharp", want: ""},
		{name: "a\tb", want: ""},
		{name: "a.b", want: ""},
		{name: `a\b`, want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, normalizeInjectionLanguage(c.name))
		})
	}
}

func TestSupportedInjectionPredicates(t *testing.T) {
	cases := []struct {
		name  string
		preds []tree_sitter.QueryPredicate
		want  bool
	}{
		{name: "none", want: true},
		{name: "offset", preds: []tree_sitter.QueryPredicate{{Operator: "offset!"}}, want: true},
		{name: "gsub", preds: []tree_sitter.QueryPredicate{{Operator: "gsub!"}}, want: false},
		{name: "lua-match", preds: []tree_sitter.QueryPredicate{{Operator: "lua-match?"}}, want: false},
		{
			name:  "offset then unknown",
			preds: []tree_sitter.QueryPredicate{{Operator: "offset!"}, {Operator: "trim!"}},
			want:  false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, supportedInjectionPredicates(c.preds))
		})
	}
}

func TestParseInjectionOffset(t *testing.T) {
	str := func(s string) *string { return &s }
	capture := func(id uint) *uint { return &id }
	offset := func(args ...tree_sitter.QueryPredicateArg) tree_sitter.QueryPredicate {
		return tree_sitter.QueryPredicate{Operator: "offset!", Args: args}
	}
	cases := []struct {
		name  string
		preds []tree_sitter.QueryPredicate
		want  injectionOffset
	}{
		{name: "none"},
		{
			name: "well formed",
			preds: []tree_sitter.QueryPredicate{offset(
				tree_sitter.QueryPredicateArg{CaptureId: capture(1)},
				tree_sitter.QueryPredicateArg{String: str("0")},
				tree_sitter.QueryPredicateArg{String: str("2")},
				tree_sitter.QueryPredicateArg{String: str("0")},
				tree_sitter.QueryPredicateArg{String: str("-1")},
			)},
			want: injectionOffset{startCol: 2, endCol: -1},
		},
		{
			name: "other capture",
			preds: []tree_sitter.QueryPredicate{offset(
				tree_sitter.QueryPredicateArg{CaptureId: capture(0)},
				tree_sitter.QueryPredicateArg{String: str("1")},
				tree_sitter.QueryPredicateArg{String: str("1")},
				tree_sitter.QueryPredicateArg{String: str("1")},
				tree_sitter.QueryPredicateArg{String: str("1")},
			)},
		},
		{
			name: "wrong arity",
			preds: []tree_sitter.QueryPredicate{offset(
				tree_sitter.QueryPredicateArg{CaptureId: capture(1)},
				tree_sitter.QueryPredicateArg{String: str("1")},
			)},
		},
		{
			name: "non numeric",
			preds: []tree_sitter.QueryPredicate{offset(
				tree_sitter.QueryPredicateArg{CaptureId: capture(1)},
				tree_sitter.QueryPredicateArg{String: str("x")},
				tree_sitter.QueryPredicateArg{String: str("1")},
				tree_sitter.QueryPredicateArg{String: str("1")},
				tree_sitter.QueryPredicateArg{String: str("1")},
			)},
		},
		{
			name: "other operator",
			preds: []tree_sitter.QueryPredicate{{Operator: "set!", Args: []tree_sitter.QueryPredicateArg{
				{CaptureId: capture(1)}, {String: str("1")}, {String: str("1")},
				{String: str("1")}, {String: str("1")},
			}}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, parseInjectionOffset(c.preds, 1))
		})
	}
}

func TestInjectionOffsetApply(t *testing.T) {
	cases := []struct {
		name       string
		offset     injectionOffset
		start, end tree_sitter.Point
		wantStart  tree_sitter.Point
		wantEnd    tree_sitter.Point
		ok         bool
	}{
		{
			name:      "zero",
			start:     tree_sitter.Point{Row: 1, Column: 2},
			end:       tree_sitter.Point{Row: 3, Column: 4},
			wantStart: tree_sitter.Point{Row: 1, Column: 2},
			wantEnd:   tree_sitter.Point{Row: 3, Column: 4},
			ok:        true,
		},
		{
			name:      "trims delimiters",
			offset:    injectionOffset{startCol: 2, endCol: -1},
			start:     tree_sitter.Point{Row: 0, Column: 5},
			end:       tree_sitter.Point{Row: 0, Column: 20},
			wantStart: tree_sitter.Point{Row: 0, Column: 7},
			wantEnd:   tree_sitter.Point{Row: 0, Column: 19},
			ok:        true,
		},
		{
			name:   "negative column",
			offset: injectionOffset{startCol: -1},
			start:  tree_sitter.Point{Row: 0, Column: 0},
			end:    tree_sitter.Point{Row: 0, Column: 3},
		},
		{
			name:   "start after end",
			offset: injectionOffset{startCol: 5},
			start:  tree_sitter.Point{Row: 0, Column: 0},
			end:    tree_sitter.Point{Row: 0, Column: 3},
		},
		{
			name:   "start row after end row",
			offset: injectionOffset{startRow: 2},
			start:  tree_sitter.Point{Row: 0, Column: 0},
			end:    tree_sitter.Point{Row: 1, Column: 3},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end, ok := c.offset.apply(c.start, c.end)
			assert.Equal(t, c.ok, ok)
			assert.Equal(t, c.wantStart, start)
			assert.Equal(t, c.wantEnd, end)
		})
	}
}

func TestResolveInjections(t *testing.T) {
	rng := func(sy, sx, ey, ex int) term.Range {
		return term.Range{Start: term.Coordinates{Y: sy, X: sx}, End: term.Coordinates{Y: ey, X: ex}}
	}
	cases := []struct {
		name       string
		candidates []injectionCandidate
		want       []Injection
	}{
		{name: "none", want: []Injection{}},
		{
			name: "last pattern wins for the same node",
			candidates: []injectionCandidate{
				{node: 1, pattern: 0, Injection: Injection{Language: "javascript", Range: rng(1, 0, 3, 0)}},
				{node: 1, pattern: 7, Injection: Injection{Language: "typescript", Range: rng(1, 0, 3, 0)}},
			},
			want: []Injection{{Language: "typescript", Range: rng(1, 0, 3, 0)}},
		},
		{
			name: "earlier pattern reported later does not override",
			candidates: []injectionCandidate{
				{node: 1, pattern: 7, Injection: Injection{Language: "typescript", Range: rng(1, 0, 3, 0)}},
				{node: 1, pattern: 0, Injection: Injection{Language: "javascript", Range: rng(1, 0, 3, 0)}},
			},
			want: []Injection{{Language: "typescript", Range: rng(1, 0, 3, 0)}},
		},
		{
			name: "document order",
			candidates: []injectionCandidate{
				{node: 2, pattern: 0, Injection: Injection{Language: "css", Range: rng(5, 7, 8, 0)}},
				{node: 1, pattern: 3, Injection: Injection{Language: "javascript", Range: rng(1, 8, 3, 0)}},
				{node: 3, pattern: 1, Injection: Injection{Language: "comment", Range: rng(5, 0, 5, 6)}},
			},
			want: []Injection{
				{Language: "javascript", Range: rng(1, 8, 3, 0)},
				{Language: "comment", Range: rng(5, 0, 5, 6)},
				{Language: "css", Range: rng(5, 7, 8, 0)},
			},
		},
		{
			name: "empty and inverted regions are dropped",
			candidates: []injectionCandidate{
				{node: 1, pattern: 0, Injection: Injection{Language: "javascript", Range: rng(1, 8, 1, 8)}},
				{node: 2, pattern: 0, Injection: Injection{Language: "css", Range: rng(4, 0, 2, 0)}},
				{node: 3, pattern: 0, Injection: Injection{Language: "css", Range: rng(5, 7, 8, 0)}},
			},
			want: []Injection{{Language: "css", Range: rng(5, 7, 8, 0)}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, resolveInjections(c.candidates))
		})
	}
}
