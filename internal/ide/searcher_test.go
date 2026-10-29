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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/llm"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/llm/llmrouter"
	"unstable.build/rune/internal/llm/llmsearch"
	"unstable.build/rune/internal/workspace"
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

// noLocalModels is the router's local llama-server backend, which these
// tests never reach.
type noLocalModels struct{}

func (noLocalModels) CreateCompletion(
	context.Context, llmapi.ModelEntry, llmapi.Request,
) (sdkiterator.Iterator[llmapi.Event], error) {
	return sdkiterator.Empty[llmapi.Event](), nil
}

func (noLocalModels) CountTokens(llmapi.ModelEntry, []llmapi.Message) (int, error) { return 0, nil }

func (noLocalModels) Models() sdkiterator.Iterator[llmapi.ModelEntry] {
	return sdkiterator.Empty[llmapi.ModelEntry]()
}

func (noLocalModels) GetModel(context.Context, llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// newSearchRouter returns a router whose search service has an open
// index of a workspace holding a file in billing and one in geo, and
// the workspace's folder.
func newSearchRouter(t *testing.T) (*llmrouter.Router, workspaceapi.URI, string) {
	t.Helper()
	reg, err := llamacpp.NewRegistry(t.TempDir())
	require.NoError(t, err)
	search := llmsearch.New(llmsearch.DefaultConfig(), llmsearch.NewFakeEmbedderFactory(), reg, nopCloser{})
	router := llmrouter.New(llm.DefaultConfig(), reg, storagestub.NewInMemoryService(), noLocalModels{}, search)
	t.Cleanup(func() { _ = router.Close() })

	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for name, content := range map[string]string{
		"billing/invoice.go": "package billing\n\n// InvoiceTotal sums the invoice lines and the sales tax.\nfunc InvoiceTotal() {}\n",
		"geo/tax.go":         "package geo\n\n// SalesTax is the sales tax of a region.\nfunc SalesTax() {}\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}
	root, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	fsys, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fsys.Close() })
	idx, err := router.SearchService().OpenIndex(llmsearch.IndexConfig{
		Root:     root,
		FS:       fsys,
		DB:       storagestub.NewInMemoryService(),
		Notify:   idepkgtest.NewNotifications(t),
		Schedule: func(f func()) bool { f(); return true },
		Options:  llmsearch.DefaultOptions(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = idx.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, idx.Wait(ctx))
	return router, root, dir
}

func TestNewWorkspaceSearcherPanicsWithoutDependencies(t *testing.T) {
	root := workspaceapi.RandomURI("file")
	router, _, _ := newSearchRouter(t)
	tests := []struct {
		name   string
		dir    string
		regexp regexpSearcher
		router *llmrouter.Router
	}{
		{"no folder", "", noRegexpSearch{}, router},
		{"no regexp search", "/w", nil, router},
		{"no router", "/w", noRegexpSearch{}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Panics(t, func() { newWorkspaceSearcher(root, tt.dir, tt.regexp, tt.router) })
		})
	}
}

func TestWorkspaceSearcherRegexpSearch(t *testing.T) {
	const dir = "/w/project"
	router, _, _ := newSearchRouter(t)
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
			s := newWorkspaceSearcher(workspaceapi.RandomURI("file"), dir, regexp, router)
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
	router, root, dir := newSearchRouter(t)
	tests := []struct {
		name      string
		root      workspaceapi.URI
		roots     []string
		wantPaths []string
		wantErr   string
		wantIs    []error
	}{
		{name: "the whole workspace", root: root, wantPaths: []string{"billing/invoice.go", "geo/tax.go"}},
		{name: "an absolute root", root: root, roots: []string{filepath.Join(dir, "geo")}, wantPaths: []string{"geo/tax.go"}},
		{name: "a relative root", root: root, roots: []string{"billing/"}, wantPaths: []string{"billing/invoice.go"}},
		{
			name:    "a root outside",
			root:    root,
			roots:   []string{filepath.Dir(dir)},
			wantErr: "outside the workspace",
			wantIs:  []error{searchapi.ErrUnsupported},
		},
		{
			name:    "a workspace without an index",
			root:    workspaceapi.Join(root, "elsewhere"),
			wantErr: "no search index",
			wantIs:  []error{searchapi.ErrUnsupported, llmsearch.ErrNoIndex},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newWorkspaceSearcher(tt.root, dir, noRegexpSearch{}, router)
			it, err := s.SemanticSearch(context.Background(),
				searchapi.SemanticRequest{Query: "sales tax", Roots: tt.roots})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				for _, is := range tt.wantIs {
					assert.ErrorIs(t, err, is)
				}
				return
			}
			require.NoError(t, err)
			hits, err := sdkiterator.ToSlice(context.Background(), it)
			require.NoError(t, err)
			var paths []string
			for _, h := range hits {
				paths = append(paths, h.Path)
			}
			assert.ElementsMatch(t, tt.wantPaths, paths)
		})
	}
}

func TestNoRegexpSearch(t *testing.T) {
	_, err := noRegexpSearch{}.RegexpSearch(context.Background(), searchapi.RegexpRequest{Pattern: "x"})
	assert.ErrorIs(t, err, searchapi.ErrUnsupported)
}
