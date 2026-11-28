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
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/codesearch"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/ide/syntax/symboldb"
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

// openSearchWorkspace opens router's semantic index of a workspace
// holding two Go files and waits for it to be built.
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
		DB:       storageapi.WithPartition(storagestub.NewInMemoryService(), root.String()),
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

func dispatch(h *Handler, pw repl.ProgressWriter, args ...string) (string, error) {
	it, err := h.HandleCommand(context.Background(), repl.Command{Name: CommandName, Args: args}, pw)
	if err != nil {
		return "", err
	}
	out, err := iterator.ToSlice(context.Background(), it)
	if err != nil {
		return "", err
	}
	if len(out) != 1 {
		return "", fmt.Errorf("%d outputs", len(out))
	}
	return render(out[0]), nil
}

func run(t *testing.T, h *Handler, args ...string) string {
	t.Helper()
	out, err := dispatch(h, nil, args...)
	require.NoError(t, err, "%v", args)
	return out
}

func render(c component.Responsive) string {
	const width = 160
	height := c.Height(width)
	w := term.NewStringWriter(width, height)
	_ = w.Clear(term.Attributes{})
	c.Resize(width, height)
	c.Draw(w)
	_ = w.Flush()
	return w.String()
}

type stubRegexIndex struct {
	status    codesearch.Status
	reindexed int
	err       error
}

func (s *stubRegexIndex) Status() codesearch.Status { return s.status }

func (s *stubRegexIndex) Reindex() error {
	s.reindexed++
	return s.err
}

func (s *stubRegexIndex) Remove() error {
	if s.err != nil {
		return s.err
	}
	s.status = codesearch.Status{Removed: true}
	return nil
}

// stubSymbolsIndex reports progress for two files when reindexed.
type stubSymbolsIndex struct {
	status symboldb.Status
	err    error
	ctx    context.Context
}

func (s *stubSymbolsIndex) Status() symboldb.Status { return s.status }

func (s *stubSymbolsIndex) Reindex(ctx context.Context, prog symboldb.Progress) error {
	s.ctx = ctx
	if s.err != nil {
		return s.err
	}
	prog.Start(2)
	prog.File(0, 2, "a.go")
	prog.File(1, 2, "b.go")
	prog.Done(2)
	return nil
}

func (s *stubSymbolsIndex) Remove(ctx context.Context) error {
	s.ctx = ctx
	if s.err != nil {
		return s.err
	}
	s.status = symboldb.Status{Loaded: true, Scanned: true, Removed: true}
	return nil
}

type recordingProgressWriter struct{ samples []string }

func (w *recordingProgressWriter) Progress(progress, total int64, units string) {
	w.samples = append(w.samples, fmt.Sprintf("%d/%d %s", progress, total, units))
}

func TestSemantic(t *testing.T) {
	router := newRouterForTest(t)
	root := openSearchWorkspace(t, router)
	h := New(Config{Search: router.SearchService(), Root: root})

	out := run(t, h, "semantic", "status")
	for _, want := range []string{
		"Semantic Index",
		"Files: 2 indexed, 0 pending, 0 skipped, 0 failed",
		"Vectors: none, keyword search only",
		"Query encoder: not used yet",
	} {
		assert.Contains(t, out, want)
	}

	assert.Contains(t, run(t, h, "semantic", "reindex"), "Rebuilding the semantic index")
	idx, ok := router.SearchService().Index(root)
	require.True(t, ok)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, idx.Wait(ctx))
	assert.Equal(t, 2, idx.Status().Files["done"])

	out = run(t, h, "semantic", "remove")
	assert.Contains(t, out, "Removed the semantic index of this workspace")
	assert.Contains(t, out, "search.semantic_enabled")
	_, ok = router.SearchService().Index(root)
	assert.False(t, ok, "the workspace has no semantic index")
	for _, action := range []string{"status", "reindex", "remove"} {
		_, err := dispatch(h, nil, "semantic", action)
		assert.ErrorIs(t, err, errNoSemanticIndex, action)
	}
}

func TestSymbols(t *testing.T) {
	router := newRouterForTest(t)
	symbols := &stubSymbolsIndex{status: symboldb.Status{
		Loaded: true, Files: 40, Records: 900, Pending: 3,
		Skipped:  map[string]int{"no language package": 12, "parse failed": 1},
		Grammars: map[string]string{"go": "abc123", "md": "", "json": ""},
	}}
	h := New(Config{Search: router.SearchService(), Root: workspaceapi.RandomURI("file"), Symbols: symbols})

	out := run(t, h, "symbols", "status")
	for _, want := range []string{
		"Symbols Index",
		"Files: 40, 3 pending",
		"Symbols: 900",
		"Scan: in progress",
		"Files without symbols",
		"no language package: 12",
		"parse failed: 1",
		"go: abc123",
		"No grammar installed: json, md",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "Semantic Index", "status shows the index asked for")

	symbols.status = symboldb.Status{Loaded: true, Scanned: true, Files: 2, Records: 7}
	assert.NotContains(t, run(t, h, "symbols", "status"), "Scan: in progress")
	pw := &recordingProgressWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	it, err := h.HandleCommand(ctx, repl.Command{Name: CommandName, Args: []string{"symbols", "reindex"}}, pw)
	require.NoError(t, err)
	outs, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Contains(t, render(outs[0]), "Rebuilt the symbols index: 7 symbols in 2 files")
	assert.Equal(t, []string{"0/2 files", "1/2 files", "2/2 files"}, pw.samples)
	assert.Equal(t, ctx, symbols.ctx, "the command's context stops the rebuild")

	symbols.err = errors.New("boom")
	_, err = dispatch(h, nil, "symbols", "reindex")
	assert.ErrorContains(t, err, "search symbols reindex: boom")
	_, err = dispatch(h, nil, "symbols", "remove")
	assert.ErrorContains(t, err, "search symbols remove: boom")
	symbols.err = nil

	out = run(t, h, "symbols", "remove")
	assert.Contains(t, out, "Removed the symbols index of this workspace")
	assert.Contains(t, out, "search.symbols_enabled")
	assert.Contains(t, run(t, h, "symbols", "status"), "Index: removed")
	for _, action := range []string{"reindex", "remove"} {
		_, err := dispatch(h, nil, "symbols", action)
		assert.ErrorContains(t, err, "the symbols index of this workspace was removed", action)
	}
}

func TestRegex(t *testing.T) {
	router := newRouterForTest(t)
	regex := &stubRegexIndex{}
	h := New(Config{Search: router.SearchService(), Root: workspaceapi.RandomURI("file"), Regex: regex})

	for _, tt := range []struct {
		status codesearch.Status
		want   []string
		absent []string
	}{
		{codesearch.Status{}, []string{"Index: loading"}, []string{"Files:", "Build:"}},
		{codesearch.Status{Building: true}, []string{"Build: in progress"}, []string{"loading"}},
		{
			codesearch.Status{Ready: true, Files: 12, Changed: 3},
			[]string{"Files: 12 indexed, 3 changed since the index was built"}, []string{"Build:"},
		},
		{codesearch.Status{Err: "disk full"}, []string{"Error: disk full"}, []string{"loading"}},
	} {
		regex.status = tt.status
		out := run(t, h, "regex", "status")
		assert.Contains(t, out, "Regex Index")
		for _, want := range tt.want {
			assert.Contains(t, out, want, "%+v", tt.status)
		}
		for _, absent := range tt.absent {
			assert.NotContains(t, out, absent, "%+v", tt.status)
		}
	}

	regex.status = codesearch.Status{Ready: true}
	assert.Contains(t, run(t, h, "regex", "reindex"), "Rebuilding the regex index")
	assert.Equal(t, 1, regex.reindexed)
	regex.err = errors.New("boom")
	_, err := dispatch(h, nil, "regex", "reindex")
	assert.ErrorContains(t, err, "search regex reindex: boom")
	regex.err = codesearch.ErrRemoved
	_, err = dispatch(h, nil, "regex", "reindex")
	assert.ErrorContains(t, err, "the regex index of this workspace was removed")
	regex.err = nil

	out := run(t, h, "regex", "remove")
	assert.Contains(t, out, "Removed the regex index of this workspace")
	assert.Contains(t, out, "searchregex reads every file")
	assert.Contains(t, run(t, h, "regex", "status"), "Index: removed")
	for _, action := range []string{"reindex", "remove"} {
		_, err := dispatch(h, nil, "regex", action)
		assert.ErrorContains(t, err, "the regex index of this workspace was removed", action)
	}
}

func TestWithoutIndexes(t *testing.T) {
	h := New(Config{Search: newRouterForTest(t).SearchService(), Root: workspaceapi.RandomURI("file")})
	for index, want := range map[string]error{
		"semantic": errNoSemanticIndex, "symbols": errNoSymbolsIndex, "regex": errNoRegexIndex,
	} {
		for _, action := range []string{"status", "reindex", "remove"} {
			_, err := dispatch(h, nil, index, action)
			assert.ErrorIs(t, err, want, "%s %s", index, action)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	h := New(Config{Search: newRouterForTest(t).SearchService(), Root: workspaceapi.RandomURI("file")})
	for _, tt := range []struct {
		args []string
		want string
	}{
		{[]string{"where", "is", "the", "retry"}, `unknown index "where": want semantic, symbols or regex`},
		{[]string{"status"}, `unknown index "status"`},
		{[]string{"regex", "rebuild"}, "usage: search regex (reindex|status|remove)"},
		{[]string{"symbols", "status", "now"}, "usage: search symbols (reindex|status|remove)"},
	} {
		_, err := dispatch(h, nil, tt.args...)
		assert.ErrorContains(t, err, tt.want, "%v", tt.args)
	}
}

func TestHelp(t *testing.T) {
	h := New(Config{Search: newRouterForTest(t).SearchService(), Root: workspaceapi.RandomURI("file")})
	for _, args := range [][]string{nil, {"help"}} {
		out := run(t, h, args...)
		assert.Contains(t, out, "Usage: search (semantic|symbols|regex) (reindex|status|remove)", "%v", args)
		assert.Contains(t, out, "search regex remove       # delete the regex index of this workspace", "%v", args)
	}

	out := run(t, h, "symbols")
	assert.Contains(t, out, "Usage: search symbols (reindex|status|remove)")
	assert.Contains(t, out, "searchfunc")
	assert.NotContains(t, out, "Examples")

	it, err := h.Help(context.Background(), []string{"regex", "remove"})
	require.NoError(t, err)
	outs, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	out = render(outs[0])
	assert.Contains(t, out, "search regex remove")
	assert.Contains(t, out, "search.regex_enabled")
}

func TestComplete(t *testing.T) {
	h := New(Config{Search: newRouterForTest(t).SearchService(), Root: workspaceapi.RandomURI("file")})
	tests := []struct {
		args []string
		want []string
	}{
		{nil, []string{"semantic", "symbols", "regex"}},
		{[]string{""}, []string{"semantic", "symbols", "regex"}},
		{[]string{"s"}, []string{"semantic", "symbols"}},
		{[]string{"symbols", ""}, []string{"reindex", "status", "remove"}},
		{[]string{"regex", "re"}, []string{"reindex", "remove"}},
		{[]string{"regex", "status", ""}, nil},
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
