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

package extension

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/workspace"
)

type search struct {
	query        string
	captureNames []string
	langs        []string
}

type fakeParser struct {
	syntaxapi.Parser
	results  map[string][]syntaxapi.Result
	errs     map[string]error
	searches []search
}

func (p *fakeParser) Search(query string, captureNames []string, langs ...string) (
	sdkiterator.Iterator[syntaxapi.Result], error,
) {
	p.searches = append(p.searches, search{query, captureNames, langs})
	return &resultIterator{results: p.results[langs[0]], err: p.errs[langs[0]]}, nil
}

type resultIterator struct {
	results []syntaxapi.Result
	err     error
}

func (it *resultIterator) Next(context.Context) (syntaxapi.Result, bool) {
	if len(it.results) == 0 {
		return syntaxapi.Result{}, false
	}
	r := it.results[0]
	it.results = it.results[1:]
	return r, true
}

func (it *resultIterator) Err() error   { return it.err }
func (it *resultIterator) Close() error { return nil }

type fakePackages struct {
	files map[string][]string
	calls []string
}

func (p *fakePackages) LibDir(_ context.Context, pkgID string) (sdkiterator.Iterator[string], error) {
	p.calls = append(p.calls, pkgID)
	files, ok := p.files[pkgID]
	if !ok {
		return nil, fmt.Errorf("%s: %w", pkgID, pkgapi.ErrNotInstalled)
	}
	return iterator.FromSlice(files), nil
}

func TestReadSymbols(t *testing.T) {
	const capture = "local.definition.function"
	type capturePos struct {
		file string
		y, x int
	}
	workspaceFiles := map[string]string{
		"a.go":  "package a\nfunc Foo(x int) {}\r\nvar ñame = 1\n",
		"b.py":  "def bar():\n    pass\n",
		"NOTES": "def baz():\n",
	}
	goCaptures := []capturePos{{"a.go", 1, 5}, {"a.go", 2, 4}, {"a.go", 9, 0}}
	goLines := []string{"a.go:2: Foo(x int) {}", "a.go:3: ñame = 1", "a.go:10: "}
	pyCaptures := []capturePos{{"b.py", 0, 4}}
	pyLines := []string{"b.py:1: bar():"}
	bothPackages := map[string]map[string]string{
		"go":     {"lib/parser.so": "", "queries/locals.scm": "(go)"},
		"python": {"lib/parser.so": "", "locals.scm": "(python)"},
	}

	tests := []struct {
		name         string
		packages     map[string]map[string]string
		libQueryFile string
		query        string
		captures     map[string][]capturePos
		searchErrs   map[string]error
		wantLines    []string
		wantSearches []search
		wantLibDirs  []string
	}{
		{
			name:         "each language searches with its package's query file",
			packages:     bothPackages,
			libQueryFile: "locals.scm",
			captures:     map[string][]capturePos{"go": goCaptures, "python": pyCaptures},
			wantLines:    append(append([]string{}, goLines...), pyLines...),
			wantSearches: []search{
				{"(go)", []string{capture}, []string{"go"}},
				{"(python)", []string{capture}, []string{"python"}},
			},
			wantLibDirs: []string{"go", "python"},
		},
		{
			name:         "languages whose package is not installed are skipped",
			packages:     map[string]map[string]string{"go": bothPackages["go"]},
			libQueryFile: "locals.scm",
			captures:     map[string][]capturePos{"go": goCaptures, "python": pyCaptures},
			wantLines:    goLines,
			wantSearches: []search{{"(go)", []string{capture}, []string{"go"}}},
			wantLibDirs:  []string{"go", "python"},
		},
		{
			name: "languages whose package lacks the query file are skipped",
			packages: map[string]map[string]string{
				"go":     bothPackages["go"],
				"python": {"lib/parser.so": ""},
			},
			libQueryFile: "locals.scm",
			captures:     map[string][]capturePos{"go": goCaptures, "python": pyCaptures},
			wantLines:    goLines,
			wantSearches: []search{{"(go)", []string{capture}, []string{"go"}}},
			wantLibDirs:  []string{"go", "python"},
		},
		{
			name:      "an inline query runs against every language",
			query:     "(custom)",
			captures:  map[string][]capturePos{"go": goCaptures, "python": pyCaptures},
			wantLines: append(append([]string{}, goLines...), pyLines...),
			wantSearches: []search{
				{"(custom)", []string{capture}, []string{"go"}},
				{"(custom)", []string{capture}, []string{"python"}},
			},
		},
		{
			name:       "a language whose search fails does not fail the others",
			query:      "(custom)",
			captures:   map[string][]capturePos{"go": goCaptures},
			searchErrs: map[string]error{"python": fmt.Errorf("invalid query")},
			wantLines:  goLines,
			wantSearches: []search{
				{"(custom)", []string{capture}, []string{"go"}},
				{"(custom)", []string{capture}, []string{"python"}},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range workspaceFiles {
				require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
			}
			root, err := workspaceapi.CurrentUserHostURI(dir)
			require.NoError(t, err)
			fs, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), root)
			require.NoError(t, err)
			t.Cleanup(func() { _ = fs.Close() })

			libDir := t.TempDir()
			pkgs := &fakePackages{files: map[string][]string{}}
			for pkgID, files := range tc.packages {
				pkgs.files[pkgID] = []string{}
				for name, content := range files {
					p := filepath.Join(libDir, pkgID, name)
					require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o700))
					require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
					pkgs.files[pkgID] = append(pkgs.files[pkgID], p)
				}
			}
			parser := &fakeParser{results: map[string][]syntaxapi.Result{}, errs: tc.searchErrs}
			for lang, captures := range tc.captures {
				for _, c := range captures {
					parser.results[lang] = append(parser.results[lang], syntaxapi.Result{
						File:        workspaceapi.Join(root, c.file),
						From:        term.Coordinates{Y: c.y, X: c.x},
						CaptureName: capture,
					})
				}
			}

			ctx := context.Background()
			it, err := readSymbols(ctx, fs, pkgs, parser,
				tc.libQueryFile, tc.query, []string{capture})
			require.NoError(t, err)
			lines, err := iterator.ToSlice(ctx, it)
			require.NoError(t, err)
			require.NoError(t, it.Close())

			assert.ElementsMatch(t, tc.wantLines, lines)
			assert.ElementsMatch(t, tc.wantSearches, parser.searches)
			assert.ElementsMatch(t, tc.wantLibDirs, pkgs.calls)
		})
	}
}
