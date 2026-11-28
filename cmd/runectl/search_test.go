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

package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestSearchRegexp(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		serverErr error
		wantReq   searchapi.RegexpRequest
		wantOut   string
		check     func(t *testing.T, out string)
		wantErr   string
		wantIs    error
	}{
		{
			name:    "pattern",
			args:    []string{`invoice\(`},
			wantReq: searchapi.RegexpRequest{Pattern: `invoice\(`},
			wantOut: "billing/invoice.go:42:\treturn invoice(invoice)\n" +
				".github/ci.yml:3:runs-on: invoice\n",
		},
		{
			name: "flags",
			args: []string{
				"--ignore-case", "--root", "billing", "--root", "/w/.github",
				"--include", "*.go", "--include", "*.yml",
				"--limit", "5", "--max-count", "1", "invoice",
			},
			wantReq: searchapi.RegexpRequest{
				Pattern: "invoice", IgnoreCase: true,
				Roots:   []string{"billing", "/w/.github"},
				Include: []string{"*.go", "*.yml"}, Limit: 5, MaxPerFile: 1,
			},
		},
		{
			name: "short",
			args: []string{"-i", "-r", "billing", "-g", "*.go", "-n", "3", "-m", "2", "invoice"},
			wantReq: searchapi.RegexpRequest{
				Pattern: "invoice", IgnoreCase: true, Roots: []string{"billing"},
				Include: []string{"*.go"}, Limit: 3, MaxPerFile: 2,
			},
		},
		{
			name:    "literal",
			args:    []string{"-Q", "a.b(c)*"},
			wantReq: searchapi.RegexpRequest{Pattern: `a\.b\(c\)\*`},
		},
		{
			name:    "json",
			args:    []string{"-F", "json", "invoice"},
			wantReq: searchapi.RegexpRequest{Pattern: "invoice"},
			check: func(t *testing.T, out string) {
				lines := strings.Split(strings.TrimSpace(out), "\n")
				require.Len(t, lines, len(cannedRegexpResults))
				var first map[string]any
				require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
				assert.Equal(t, map[string]any{
					"path": "billing/invoice.go", "line": 42.0,
					"text":  "\treturn invoice(invoice)",
					"spans": []any{[]any{8.0, 15.0}, []any{16.0, 23.0}},
				}, first)
			},
		},
		{
			name:    "template",
			args:    []string{"-F", "{{.Path}} {{.Line}} {{.Spans}}", "invoice"},
			wantReq: searchapi.RegexpRequest{Pattern: "invoice"},
			wantOut: "billing/invoice.go 42 [[8 15] [16 23]]\n" +
				".github/ci.yml 3 []\n",
		},
		{
			name:    "table",
			args:    []string{"-F", "table", "invoice"},
			wantReq: searchapi.RegexpRequest{Pattern: "invoice"},
			check: func(t *testing.T, out string) {
				for _, r := range cannedRegexpResults {
					assert.Contains(t, out, r.Path)
				}
				assert.Contains(t, out, "SPANS")
			},
		},
		{name: "noargs", args: nil, wantErr: "accepts 1 arg(s)"},
		{name: "twoargs", args: []string{"a", "b"}, wantErr: "accepts 1 arg(s)"},
		{name: "neglimit", args: []string{"-n", "-1", "x"}, wantErr: "--limit: must not be negative"},
		{name: "negmaxcount", args: []string{"-m", "-1", "x"}, wantErr: "--max-count: must not be negative"},
		{
			name:      "unsupported",
			args:      []string{"invoice"},
			serverErr: status.Error(codes.Unimplemented, "no index"),
			wantReq:   searchapi.RegexpRequest{Pattern: "invoice"},
			wantIs:    searchapi.ErrUnsupported,
		},
		{
			name:      "jsonerr",
			args:      []string{"-F", "json", "("},
			serverErr: errors.New("error parsing regexp: missing closing )"),
			wantReq:   searchapi.RegexpRequest{Pattern: "("},
			check: func(t *testing.T, out string) {
				lines := strings.Split(strings.TrimSpace(out), "\n")
				var last map[string]any
				require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &last))
				assert.Equal(t, false, last["success"])
				assert.Contains(t, last["error"], "missing closing )")
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer env.cleanup()
			env.search.err = tc.serverErr

			out, err := env.run(append([]string{"search", "regexp"}, tc.args...)...)
			switch {
			case tc.wantErr != "":
				require.ErrorContains(t, err, tc.wantErr)
				return
			case tc.wantIs != nil:
				require.ErrorIs(t, err, tc.wantIs)
			default:
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantReq, env.search.regexpSearched(t))
			if tc.wantOut != "" {
				assert.Equal(t, tc.wantOut, out)
			}
			if tc.check != nil {
				tc.check(t, out)
			}
		})
	}
}

func TestSearchSemantic(t *testing.T) {
	const embedding = "huggingface.co/ggml-org/embeddinggemma-2-GGUF:Q8_0"
	alias := llmapi.ModelEntry{Name: "search"}
	defaults := func(query string) searchapi.SemanticRequest {
		return searchapi.SemanticRequest{Query: query, Model: alias, Meaning: true}
	}
	tests := []struct {
		name       string
		args       []string
		status     searchapi.SemanticStatus
		serverErr  error
		wantReq    searchapi.SemanticRequest
		wantOut    string
		wantStderr string
		check      func(t *testing.T, out string)
		wantErr    string
		wantIs     error
	}{
		{
			name:    "query",
			args:    []string{"where", "is", "the", "invoice", "rounded"},
			wantReq: defaults("where is the invoice rounded"),
			wantOut: "billing/invoice.go:42:   3.14  2.50  return roundInvoiceTotal(lines)\n" +
				"geo/route.go:7:          0.75  0.00  func shortestRoute(a, b Place) []Place {\n" +
				"notebooks/fit.ipynb:1:   1.50  1.25  [cell 3] fit the model\n" +
				"docs/invoice.md:1:       0.50  0.00\n",
		},
		{
			name:    "quoted",
			args:    []string{"where is the invoice rounded"},
			wantReq: defaults("where is the invoice rounded"),
		},
		{
			name:    "status",
			args:    []string{"invoice"},
			status:  searchapi.SemanticStatus{Indexing: true, Message: "12 files left to index"},
			wantReq: defaults("invoice"),
			wantOut: "billing/invoice.go:42:   3.14  2.50  return roundInvoiceTotal(lines)\n" +
				"geo/route.go:7:          0.75  0.00  func shortestRoute(a, b Place) []Place {\n" +
				"notebooks/fit.ipynb:1:   1.50  1.25  [cell 3] fit the model\n" +
				"docs/invoice.md:1:       0.50  0.00\n",
			wantStderr: "runectl: 12 files left to index\n",
		},
		{
			name:       "status json",
			args:       []string{"-F", "json", "invoice"},
			status:     searchapi.SemanticStatus{WordsOnly: true, Message: "matched by words only"},
			wantReq:    defaults("invoice"),
			wantStderr: "runectl: matched by words only\n",
			check: func(t *testing.T, out string) {
				lines := strings.Split(strings.TrimSpace(out), "\n")
				assert.Len(t, lines, len(cannedSemanticResults))
			},
		},
		{
			name: "flags",
			args: []string{
				"--model", "llamacpp/" + embedding,
				"--root", "billing", "--root", "geo/",
				"--limit", "5", "--meaning=false", "--prefix-last",
				"invoice", "tot",
			},
			wantReq: searchapi.SemanticRequest{
				Query: "invoice tot", Model: llmapi.ModelEntry{Provider: "llamacpp", Name: embedding},
				Roots: []string{"billing", "geo/"}, Limit: 5, PrefixLast: true,
			},
		},
		{
			name: "short",
			args: []string{"-m", "llamacpp/" + embedding, "-r", "billing", "-n", "3", "invoice"},
			wantReq: searchapi.SemanticRequest{
				Query: "invoice", Model: llmapi.ModelEntry{Provider: "llamacpp", Name: embedding},
				Roots: []string{"billing"}, Limit: 3, Meaning: true,
			},
		},
		{
			name:    "interspersed",
			args:    []string{"invoice", "-n", "2", "rounding"},
			wantReq: searchapi.SemanticRequest{Query: "invoice rounding", Model: alias, Limit: 2, Meaning: true},
		},
		{
			name:    "dashdash",
			args:    []string{"-n", "1", "--", "-v", "flag"},
			wantReq: searchapi.SemanticRequest{Query: "-v flag", Model: alias, Limit: 1, Meaning: true},
		},
		{
			name: "alias",
			args: []string{"-m", "embed", "invoice"},
			wantReq: searchapi.SemanticRequest{
				Query: "invoice", Model: llmapi.ModelEntry{Name: "embed"}, Meaning: true,
			},
		},
		{
			name:    "json",
			args:    []string{"-F", "json", "invoice"},
			wantReq: defaults("invoice"),
			check: func(t *testing.T, out string) {
				lines := strings.Split(strings.TrimSpace(out), "\n")
				require.Len(t, lines, len(cannedSemanticResults))
				var first map[string]any
				require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
				assert.Equal(t, map[string]any{
					"path": "billing/invoice.go", "line": 42.0,
					"text": "\treturn roundInvoiceTotal(lines)", "kind": "code",
					"score": 3.14159, "z": 2.5, "cosine": 0.61,
					"keyword_rank": 1.0, "segment": "lines",
					"loc": 40.0, "loc_end": 58.0, "focus": 42.0,
					"excerpt":  "rounds the «invoice» total",
					"modified": 1700000000.0, "size": 2048.0,
				}, first)
			},
		},
		{
			name:    "grep",
			args:    []string{"-F", "{{.Path}}:{{.Line}}:{{.Text}}", "invoice"},
			wantReq: defaults("invoice"),
			wantOut: "billing/invoice.go:42:\treturn roundInvoiceTotal(lines)\n" +
				"geo/route.go:7:func shortestRoute(a, b Place) []Place {\n" +
				"notebooks/fit.ipynb:1:\n" +
				"docs/invoice.md:1:\n",
		},
		{
			name:    "tpl",
			args:    []string{"-F", "{{.Path}} {{.Segment}} {{.Loc}}-{{.LocEnd}}", "invoice"},
			wantReq: defaults("invoice"),
			wantOut: "billing/invoice.go lines 40-58\n" +
				"geo/route.go lines 7-19\n" +
				"notebooks/fit.ipynb cells 3-3\n" +
				"docs/invoice.md name 0-0\n",
		},
		{
			name:    "table",
			args:    []string{"-F", "table", "invoice"},
			wantReq: defaults("invoice"),
			check: func(t *testing.T, out string) {
				for _, r := range cannedSemanticResults {
					assert.Contains(t, out, r.Path)
				}
				assert.Contains(t, out, "rounds the «invoice» total")
			},
		},
		{name: "noargs", args: nil, wantErr: "requires at least 1 arg(s)"},
		{name: "blank", args: []string{" ", ""}, wantErr: "empty query"},
		{name: "neglimit", args: []string{"-n", "-1", "invoice"}, wantErr: "--limit: must not be negative"},
		{name: "nomodel", args: []string{"-m", "", "invoice"}, wantErr: "--model: must not be empty"},
		{name: "badmodel", args: []string{"-m", "llamacpp/", "invoice"}, wantErr: "--model: expected provider/model"},
		{
			name:      "unsupported",
			args:      []string{"-m", "openai/gpt-5", "invoice"},
			serverErr: status.Error(codes.Unimplemented, "openai"),
			wantReq: searchapi.SemanticRequest{
				Query: "invoice", Model: llmapi.ModelEntry{Provider: "openai", Name: "gpt-5"}, Meaning: true,
			},
			wantIs: searchapi.ErrUnsupported,
		},
		{
			name:      "notfound",
			args:      []string{"-m", "nope", "invoice"},
			serverErr: status.Error(codes.NotFound, "nope"),
			wantReq: searchapi.SemanticRequest{
				Query: "invoice", Model: llmapi.ModelEntry{Name: "nope"}, Meaning: true,
			},
			wantIs: llmapi.ErrModelNotFound,
		},
		{
			name:      "jsonerr",
			args:      []string{"-F", "json", "invoice"},
			serverErr: errors.New("index broke"),
			wantReq:   defaults("invoice"),
			check: func(t *testing.T, out string) {
				lines := strings.Split(strings.TrimSpace(out), "\n")
				var last map[string]any
				require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &last))
				assert.Equal(t, false, last["success"])
				assert.Contains(t, last["error"], "index broke")
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			defer env.cleanup()
			env.search.err = tc.serverErr
			env.search.status = tc.status

			out, err := env.run(append([]string{"search", "semantic"}, tc.args...)...)
			assert.Equal(t, tc.wantStderr, env.stderr.String())
			switch {
			case tc.wantErr != "":
				require.ErrorContains(t, err, tc.wantErr)
				return
			case tc.wantIs != nil:
				require.ErrorIs(t, err, tc.wantIs)
			default:
				require.NoError(t, err)
			}
			assert.Equal(t, tc.wantReq, env.search.semanticSearched(t))
			if tc.wantOut != "" {
				assert.Equal(t, tc.wantOut, out)
			}
			if tc.check != nil {
				tc.check(t, out)
			}
		})
	}
}

func TestSemanticResultShown(t *testing.T) {
	tests := []struct {
		name string
		r    searchapi.SemanticResult
		want string
	}{
		{"line without indentation",
			searchapi.SemanticResult{Segment: "lines", Loc: 3, Text: "\t  x := y", Excerpt: "e"}, "x := y"},
		{"blank line shows the excerpt",
			searchapi.SemanticResult{Segment: "lines", Loc: 3, Text: " \t", Excerpt: "the «retry» loop"}, "the retry loop"},
		{"cell", searchapi.SemanticResult{Segment: "cells", Loc: 2, Excerpt: "plot «it»"}, "[cell 2] plot it"},
		{"cell without excerpt", searchapi.SemanticResult{Segment: "cells", Loc: 2}, "[cell 2]"},
		{"name", searchapi.SemanticResult{Segment: "name"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, flattenSemanticResult(tc.r).shown())
		})
	}
}
