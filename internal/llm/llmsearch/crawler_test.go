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

package llmsearch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fixtureCode = []string{
	"README.md",
	"auth/session.ts",
	"billing/invoice.py",
	"billing/tax.py",
	"docs/design.md",
	"dup/a.go",
	"dup/b.go",
	"gen.pb.go",
	"geo/distance.py",
	"notebooks/analysis.ipynb",
	"retry/backoff.go",
	"retry/policy.go",
	"storage/cache.rs",
}

func candidatePaths(cands []candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.path
	}
	return out
}

func TestCrawlWorkspace(t *testing.T) {
	_, _, fsys := newWorkspace(t)
	cands, report, err := crawl(context.Background(), fsys, []string{"."}, DefaultOptions())
	require.NoError(t, err)
	assert.Equal(t, fixtureCode, candidatePaths(cands))
	assert.Equal(t, map[string]int{
		skipSecret:    1, // .env
		skipMinified:  1, // web/app.min.js
		skipData:      1, // data.csv
		skipOtherType: 1, // .gitignore
	}, report.skipped)
	assert.Equal(t, map[string]int{skipVendoredFolder: 2}, report.skippedFolders, "vendor/ and build/")
	for _, c := range cands {
		assert.Positive(t, c.size, c.path)
		assert.Positive(t, c.modTime, c.path)
	}
}

func TestCrawlRoots(t *testing.T) {
	tests := []struct {
		name  string
		roots []string
		opts  func(*Options)
		want  []string
	}{
		{
			name:  "files and folders",
			roots: []string{"retry/backoff.go", "geo", "billing/"},
			want:  []string{"billing/invoice.py", "billing/tax.py", "geo/distance.py", "retry/backoff.go"},
		},
		{
			name:  "missing and left-out paths",
			roots: []string{"missing.go", "vendor/lib/lib.go", "tmp/scratch.go", "build", "data.csv"},
		},
		{
			name:  "excluded folders and extensions",
			roots: []string{"."},
			opts: func(o *Options) {
				o.ExcludedFolders = []string{"billing/", "./retry"}
				o.ExcludedExtensions = []string{".md", "ts", "ipynb"}
			},
			want: []string{"dup/a.go", "dup/b.go", "gen.pb.go", "geo/distance.py", "storage/cache.rs"},
		},
		{
			name:  "an excluded folder's file",
			roots: []string{"billing/tax.py"},
			opts:  func(o *Options) { o.ExcludedFolders = []string{"billing"} },
		},
		{
			name:  "too big",
			roots: []string{"retry", "notebooks"},
			opts:  func(o *Options) { o.MaxCodeBytes, o.MaxNotebookBytes = 1500, 1<<20 },
			want:  []string{"notebooks/analysis.ipynb", "retry/policy.go"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, fsys := newWorkspace(t)
			opts := DefaultOptions()
			if tt.opts != nil {
				tt.opts(&opts)
			}
			cands, _, err := crawl(context.Background(), fsys, tt.roots, opts)
			require.NoError(t, err)
			assert.Equal(t, tt.want, nilIfEmpty(candidatePaths(cands)))
		})
	}
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func TestCrawlSkipsSubmodules(t *testing.T) {
	dir, _, fsys := newWorkspace(t)
	writeFile(t, dir, "third/.git", "gitdir: ../.git/modules/third\n")
	writeFile(t, dir, "third/main.go", "package main\n")
	cands, report, err := crawl(context.Background(), fsys, []string{"."}, DefaultOptions())
	require.NoError(t, err)
	assert.NotContains(t, candidatePaths(cands), "third/main.go")
	assert.Equal(t, 1, report.skippedFolders[skipSubmodule])
}

func TestCrawlCanceled(t *testing.T) {
	_, _, fsys := newWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := crawl(ctx, fsys, []string{"."}, DefaultOptions())
	assert.ErrorIs(t, err, context.Canceled)
}
