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

package searchconsole

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/llm"
	"unstable.build/rune/internal/llm/llamacpp"
	"unstable.build/rune/internal/llm/llmrouter"
	"unstable.build/rune/internal/llm/llmsearch"
	"unstable.build/rune/internal/workspace"
)

func newRouterForTest(t *testing.T) *llmrouter.Router {
	t.Helper()
	reg, err := llamacpp.NewRegistry(t.TempDir())
	require.NoError(t, err)
	cfg := llmsearch.DefaultConfig()
	cfg.Enabled = true
	search := llmsearch.New(cfg, llmsearch.NewFakeEmbedderFactory(), reg, nopCloser{})
	r := llmrouter.New(llm.DefaultConfig(), reg, storagestub.NewInMemoryService(), noLocalModels{}, search)
	t.Cleanup(func() { _ = r.Close() })
	return r
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// noLocalModels is the router's local llama-server backend, which these
// tests never reach.
type noLocalModels struct{}

func (noLocalModels) CreateCompletion(
	context.Context, llmapi.ModelEntry, llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	return iterator.Empty[llmapi.Event](), nil
}

func (noLocalModels) CountTokens(llmapi.ModelEntry, []llmapi.Message) (int, error) { return 0, nil }

func (noLocalModels) Models() iterator.Iterator[llmapi.ModelEntry] {
	return iterator.Empty[llmapi.ModelEntry]()
}

func (noLocalModels) GetModel(context.Context, llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
}

func (noLocalModels) CanGenerateImage(context.Context, llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	return llmapi.ModelEntry{}, llmapi.ErrModelNotFound
}

func (noLocalModels) CanGenerateImages(context.Context, llmapi.ModelEntry) (bool, error) {
	return false, errors.New("unsupported")
}

func (noLocalModels) GenerateImage(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error) {
	return llmapi.Image{}, errors.New("unsupported")
}

// routerAt answers semantic searches over root through router, as the
// workspace's searcher does.
type routerAt struct {
	router *llmrouter.Router
	root   workspaceapi.URI
}

func (r routerAt) SemanticSearch(ctx context.Context, req searchapi.SemanticRequest) (
	iterator.Iterator[searchapi.SemanticResult], error) {
	return r.router.SemanticSearch(ctx, r.root, req)
}

func newHandlerForWorkspace(router *llmrouter.Router, root workspaceapi.URI) *Handler {
	return New(Config{Searcher: routerAt{router, root}, Search: router.SearchService(), Root: root})
}

// openSearchWorkspace opens router's index of a workspace holding two Go
// files and waits for it to be built.
func openSearchWorkspace(t *testing.T, router *llmrouter.Router) workspaceapi.URI {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"billing/invoice.go": "package billing\n\n// InvoiceTotal sums the invoice lines and the sales tax.\nfunc InvoiceTotal() {}\n",
		"geo/route.go":       "package geo\n\n// ShortestRoute walks the map between two cities.\nfunc ShortestRoute() {}\n",
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
	return root
}

func run(t *testing.T, h *Handler, args ...string) string {
	t.Helper()
	it, err := h.HandleCommand(context.Background(), repl.Command{Name: CommandName, Args: args}, nil)
	require.NoError(t, err)
	out, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Len(t, out, 1)
	return render(t, out[0])
}

func render(t *testing.T, c component.Responsive) string {
	t.Helper()
	const width = 160
	height := c.Height(width)
	w := term.NewStringWriter(width, height)
	require.NoError(t, w.Clear(term.Attributes{}))
	c.Resize(width, height)
	c.Draw(w)
	require.NoError(t, w.Flush())
	return w.String()
}

// cannedSearch answers SemanticSearch with hits and records the request.
type cannedSearch struct {
	hits []searchapi.SemanticResult
	req  searchapi.SemanticRequest
}

func (s *cannedSearch) SemanticSearch(_ context.Context, req searchapi.SemanticRequest) (
	iterator.Iterator[searchapi.SemanticResult], error) {
	s.req = req
	return iterator.FromSlice(s.hits), nil
}

func TestQueryThroughWorkspaceIndex(t *testing.T) {
	router := newRouterForTest(t)
	h := newHandlerForWorkspace(router, openSearchWorkspace(t, router))

	out := run(t, h, "invoice", "sales", "tax")
	assert.Contains(t, out, "Search invoice sales tax")
	assert.Contains(t, out, "billing/invoice.go:")
	assert.NotContains(t, out, "geo/route.go")
}

func TestQueryRendersHits(t *testing.T) {
	router := newRouterForTest(t)
	svc := &cannedSearch{hits: []searchapi.SemanticResult{
		{Path: "retry/policy.go", Segment: "lines", Loc: 10, Focus: 14, Score: 2, Z: 3.25, Cosine: 0.61,
			Excerpt: "type «Policy» struct {"},
		{Path: "retry/backoff.go", Segment: "lines", Loc: 3, Score: 1.5, Excerpt: "func Backoff(n int)"},
		{Path: "notes/explore.ipynb", Segment: "cells", Loc: 4, Score: 0.5, Excerpt: "plot the *retries* of `run`"},
		{Path: "docs/retry.md", Segment: "name", Score: 0.25},
	}}
	h := New(Config{Searcher: svc, Search: router.SearchService(), Root: workspaceapi.RandomURI("file")})

	out := run(t, h, "retry", "policy")
	assert.Equal(t, searchapi.SemanticRequest{Query: "retry policy", Limit: 20, Meaning: true}, svc.req)
	for _, want := range []string{
		"retry/policy.go:14 (score 2.00, z 3.25) type Policy struct {",
		"retry/backoff.go:3 (score 1.50) func Backoff(n int)",
		"Weaker matches",
		"notes/explore.ipynb cell 4 (score 0.50) plot the *retries* of `run`",
		"docs/retry.md (score 0.25)",
	} {
		assert.Contains(t, out, want)
	}
	assert.Less(t, strings.Index(out, "retry/backoff.go"), strings.Index(out, "Weaker matches"))
	assert.Less(t, strings.Index(out, "Weaker matches"), strings.Index(out, "notes/explore.ipynb"))
}

func TestQueryNoMatches(t *testing.T) {
	router := newRouterForTest(t)
	h := newHandlerForWorkspace(router, openSearchWorkspace(t, router))

	assert.Contains(t, run(t, h, "zebra"), "Nothing matches zebra")
}

func TestStatusAndReindex(t *testing.T) {
	router := newRouterForTest(t)
	root := openSearchWorkspace(t, router)
	h := newHandlerForWorkspace(router, root)

	out := run(t, h, "status")
	assert.Contains(t, out, "Files: 2 indexed, 0 pending, 0 skipped, 0 failed")
	assert.Contains(t, out, "Vectors: none, keyword search only")
	assert.Contains(t, out, "Query encoder: not used yet")

	assert.Contains(t, run(t, h, "reindex"), "Rebuilding the search index")
	idx, ok := router.SearchService().Index(root)
	require.True(t, ok)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, idx.Wait(ctx))
	assert.Equal(t, 2, idx.Status().Files["done"])
}

func TestWithoutIndexErrors(t *testing.T) {
	h := newHandlerForWorkspace(newRouterForTest(t), workspaceapi.RandomURI("file"))
	for _, args := range [][]string{{"invoice"}, {"status"}, {"reindex"}} {
		_, err := h.HandleCommand(context.Background(), repl.Command{Name: CommandName, Args: args}, nil)
		assert.ErrorIs(t, err, errNoSearchIndex, "%v", args)
	}
}

func TestHelp(t *testing.T) {
	h := newHandlerForWorkspace(newRouterForTest(t), workspaceapi.RandomURI("file"))
	for _, args := range [][]string{nil, {"help"}} {
		out := run(t, h, args...)
		assert.Contains(t, out, "Usage: search (<query...>|status|reindex)", "%v", args)
		assert.Contains(t, out, "search reindex                     # rebuild the index from scratch", "%v", args)
	}

	it, err := h.Help(context.Background(), []string{"status"})
	require.NoError(t, err)
	out, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Contains(t, render(t, out[0]), "search status")
}

func TestComplete(t *testing.T) {
	h := newHandlerForWorkspace(newRouterForTest(t), workspaceapi.RandomURI("file"))
	tests := []struct {
		args []string
		want []string
	}{
		{nil, []string{"status", "reindex"}},
		{[]string{""}, []string{"status", "reindex"}},
		{[]string{"st"}, []string{"status"}},
		{[]string{"where", ""}, nil},
	}
	for _, tt := range tests {
		it, err := h.Complete(context.Background(), CommandName, tt.args)
		require.NoError(t, err)
		got, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.ElementsMatch(t, tt.want, got, "%v", tt.args)
	}
}
