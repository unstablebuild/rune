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

package syntaxtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"go.uber.org/goleak"
	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/workspace"
)

// countingFS wraps a workspace file system and counts OpenFile calls per
// path so a test can assert how many times each file is read. It also wraps
// returned files to count Close calls so a test can assert that every opened
// file is closed (no descriptor leak in the read path).
type countingFS struct {
	workspaceapi.FileSystem
	mu    sync.Mutex
	opens map[string]int
	open  int
	close int
}

func (c *countingFS) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	f, err := c.FileSystem.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.opens == nil {
		c.opens = make(map[string]int)
	}
	c.opens[path]++
	c.open++
	c.mu.Unlock()
	return &countingFile{File: f, fs: c}, nil
}

type countingFile struct {
	workspaceapi.File
	fs   *countingFS
	once sync.Once
}

func (f *countingFile) Close() error {
	f.once.Do(func() {
		f.fs.mu.Lock()
		f.fs.close++
		f.fs.mu.Unlock()
	})
	return f.File.Close()
}

func (c *countingFS) openCloseBalance() (open, close int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.open, c.close
}

func (c *countingFS) maxOpens() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	max := 0
	for _, n := range c.opens {
		if n > max {
			max = n
		}
	}
	return max
}

func (c *countingFS) distinctFiles() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.opens)
}

// openedUnder reports whether any opened path contains the given path
// segment, used to assert that filtered directories are never read.
func (c *countingFS) openedUnder(segment string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for p := range c.opens {
		if strings.Contains(p, segment) {
			return true
		}
	}
	return false
}

const multiQueryFile = `package pkg

func function() string {
	return ""
}

type myType struct {
	a string
}
`

const (
	funcQuery = `(function_declaration name: (identifier) @fn)`
	typeQuery = `(type_declaration (type_spec name: (type_identifier) @ty))`
	varQuery  = `(var_declaration (var_spec name: (identifier) @v))`
)

func setupMultiSearcher(t *testing.T, files int) (
	symbolresolve.Searcher, *countingFS,
) {
	t.Helper()

	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	for i := range files {
		createFile(t, scheme, fileName(i), multiQueryFile)
	}

	fs := &countingFS{FileSystem: scheme}
	parser := syntax.NewParser(fs, goPkgManager(t), uri)
	return parser, fs
}

func fileName(i int) string {
	return "f" + string(rune('a'+i)) + ".go"
}

func goPkgManager(t testing.TB) syntax.PkgManager {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	return goFixturePkgManager{files: []string{
		grammarfixture.ParserPath(t, filepath.Join(wd, "go")),
		filepath.Join(wd, "go", "locals.scm"),
		filepath.Join(wd, "go", "highlights.scm"),
		filepath.Join(wd, "go", "indents.scm"),
		filepath.Join(wd, "go", "folds.scm"),
	}}
}

type goFixturePkgManager struct {
	files []string
}

func (m goFixturePkgManager) LibDir(
	context.Context, string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice(m.files), nil
}

func collectMulti(
	t *testing.T, searcher symbolresolve.Searcher, queries []symbolresolve.MultiQuery,
) []symbolresolve.MultiResult {
	t.Helper()
	it, err := searcher.SearchMulti(queries, "go")
	require.NoError(t, err)
	results, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	return results
}

func countByQuery(results []symbolresolve.MultiResult) map[int]int {
	counts := make(map[int]int)
	for _, r := range results {
		counts[r.QueryID]++
	}
	return counts
}

func TestSearchMultiSharesParsePerFile(t *testing.T) {
	const files = 3
	searcher, fs := setupMultiSearcher(t, files)

	queries := []symbolresolve.MultiQuery{
		{ID: 7, Query: funcQuery, Captures: []string{"fn"}},
		{ID: 9, Query: typeQuery, Captures: []string{"ty"}},
	}
	results := collectMulti(t, searcher, queries)

	assert.Equal(t, files, fs.distinctFiles(), "every file should be opened")
	assert.Equal(t, 1, fs.maxOpens(),
		"each file must be opened (and parsed) exactly once for the whole batch")

	counts := countByQuery(results)
	assert.Equal(t, files, counts[7], "one function capture per file")
	assert.Equal(t, files, counts[9], "one type capture per file")

	for _, r := range results {
		switch r.QueryID {
		case 7:
			assert.Equal(t, "function", r.Match[0].Text)
			assert.Equal(t, "fn", r.Match[0].CaptureName)
		case 9:
			assert.Equal(t, "myType", r.Match[0].Text)
			assert.Equal(t, "ty", r.Match[0].CaptureName)
		default:
			t.Fatalf("unexpected QueryID %d", r.QueryID)
		}
	}
}

func TestSearchMultiResultsMatchSeparateSearches(t *testing.T) {
	searcher, _ := setupMultiSearcher(t, 2)

	multi := collectMulti(t, searcher, []symbolresolve.MultiQuery{
		{ID: 0, Query: funcQuery, Captures: []string{"fn"}},
		{ID: 1, Query: typeQuery, Captures: []string{"ty"}},
	})

	funcIt, err := searcher.Search(funcQuery, []string{"fn"}, "go")
	require.NoError(t, err)
	funcResults, err := iterator.ToSlice(context.Background(), funcIt)
	require.NoError(t, err)
	typeIt, err := searcher.Search(typeQuery, []string{"ty"}, "go")
	require.NoError(t, err)
	typeResults, err := iterator.ToSlice(context.Background(), typeIt)
	require.NoError(t, err)

	var gotFunc, gotType []syntaxapi.Result
	for _, r := range multi {
		switch r.QueryID {
		case 0:
			gotFunc = append(gotFunc, r.Match[0])
		case 1:
			gotType = append(gotType, r.Match[0])
		}
	}
	assert.ElementsMatch(t, funcResults, gotFunc,
		"SearchMulti function results must match a standalone Search")
	assert.ElementsMatch(t, typeResults, gotType,
		"SearchMulti type results must match a standalone Search")
}

func TestSearchMultiAddsQueryWithoutExtraWalk(t *testing.T) {
	const files = 4

	searcher2, fs2 := setupMultiSearcher(t, files)
	collectMulti(t, searcher2, []symbolresolve.MultiQuery{
		{ID: 0, Query: funcQuery, Captures: []string{"fn"}},
		{ID: 1, Query: typeQuery, Captures: []string{"ty"}},
	})
	assert.Equal(t, 1, fs2.maxOpens())
	assert.Equal(t, files, fs2.distinctFiles())

	searcher3, fs3 := setupMultiSearcher(t, files)
	collectMulti(t, searcher3, []symbolresolve.MultiQuery{
		{ID: 0, Query: funcQuery, Captures: []string{"fn"}},
		{ID: 1, Query: typeQuery, Captures: []string{"ty"}},
		{ID: 2, Query: varQuery, Captures: []string{"v"}},
	})
	assert.Equal(t, 1, fs3.maxOpens(),
		"adding a third query must not open/parse any file more than once")
	assert.Equal(t, files, fs3.distinctFiles(),
		"adding a third query must not walk extra files")
}

func TestSearchMultiRuneColumns(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	src := "package pkg\ntype T struct{ å int; bar int }\n"
	createFile(t, scheme, "fields.go", src)

	parser := syntax.NewParser(scheme, goPkgManager(t), uri)
	results := collectMulti(t, parser, []symbolresolve.MultiQuery{
		{ID: 0, Query: `(field_declaration name: (field_identifier) @f)`, Captures: []string{"f"}},
	})

	got := make(map[string]syntaxapi.Result, len(results))
	for _, r := range results {
		got[r.Match[0].Text] = r.Match[0]
	}

	bar, ok := got["bar"]
	require.Truef(t, ok, "expected to capture field 'bar'; got %+v", results)
	assert.Equal(t, 1, bar.From.Y, "row")
	assert.Equalf(t, 22, bar.From.X,
		"field column must be the rune column (22), not the byte column (23); got %d", bar.From.X)
	assert.Equalf(t, 25, bar.To.X,
		"end column must also be rune-based (25); got %d", bar.To.X)

	// "å" itself has no multi-byte rune before it, so rune and byte columns
	// coincide (15).
	aField, ok := got["å"]
	require.True(t, ok, "expected to capture field 'å'")
	assert.Equal(t, 15, aField.From.X, "leading field column")
}

func TestSearchMultiClosesEveryFile(t *testing.T) {
	const files = 6
	searcher, fs := setupMultiSearcher(t, files)

	for range 3 {
		collectMulti(t, searcher, []symbolresolve.MultiQuery{
			{ID: 0, Query: funcQuery, Captures: []string{"fn"}},
			{ID: 1, Query: typeQuery, Captures: []string{"ty"}},
		})
	}

	open, closed := fs.openCloseBalance()
	assert.Positive(t, open, "expected files to be opened")
	assert.Equalf(t, open, closed,
		"every opened file must be closed: opened=%d closed=%d", open, closed)
}

func TestSearchMultiNoGoroutineLeak(t *testing.T) {
	defer goleak.VerifyNone(t)

	searcher, _ := setupMultiSearcher(t, 5)
	for range 4 {
		it, err := searcher.SearchMulti([]symbolresolve.MultiQuery{
			{ID: 0, Query: funcQuery, Captures: []string{"fn"}},
			{ID: 1, Query: typeQuery, Captures: []string{"ty"}},
		}, "go")
		require.NoError(t, err)
		_, err = iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
	}
}

func TestSearchMultiSkipsFilteredDirs(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	createFile(t, scheme, "a.go", multiQueryFile)
	createFile(t, scheme, "b.go", multiQueryFile)
	createFile(t, scheme, "node_modules/dep.go", multiQueryFile)

	fs := &countingFS{FileSystem: scheme}
	searcher := syntax.NewParser(fs, goPkgManager(t), uri)

	results := collectMulti(t, searcher, []symbolresolve.MultiQuery{
		{ID: 0, Query: funcQuery, Captures: []string{"fn"}},
	})

	assert.False(t, fs.openedUnder("node_modules"),
		"SearchMulti must not open files under filtered dirs")
	assert.Equal(t, 2, fs.distinctFiles(),
		"only the two root files should be walked")
	assert.Len(t, results, 2, "node_modules file must not contribute results")
}

func TestQuerySessionScratchDoesNotAliasResults(t *testing.T) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	fileA := createFile(t, scheme, "a.go", `package pkg

func alphaFunc() string {
	return ""
}

type alphaType struct{}
`)
	fileB := createFile(t, scheme, "b.go", `package pkg

// Longer than a.go so a reused buffer is fully overwritten.
func betaFuncWithMuchLongerName() string {
	return "`+strings.Repeat("padding ", 64)+`"
}

type betaTypeWithMuchLongerName struct{}
`)

	parser := syntax.NewParser(scheme, goPkgManager(t), uri)
	session := parser.NewQuerySession()
	t.Cleanup(func() { _ = session.Close() })

	queries := []symbolresolve.MultiQuery{
		{ID: 0, Query: funcQuery, Captures: []string{"fn"}},
		{ID: 1, Query: typeQuery, Captures: []string{"ty"}},
	}
	resA, err := session.QueryMulti(context.Background(), fileA, queries)
	require.NoError(t, err)

	resB, err := session.QueryMulti(context.Background(), fileB, queries)
	require.NoError(t, err)
	require.NotEmpty(t, resB)

	gotA := make(map[int]string, len(resA))
	for _, r := range resA {
		gotA[r.QueryID] = r.Match[0].Text
	}
	assert.Equal(t, "alphaFunc", gotA[0],
		"file A results must survive file B reusing the session buffer")
	assert.Equal(t, "alphaType", gotA[1],
		"file A results must survive file B reusing the session buffer")
}

// BenchmarkQuerySessionQueryMulti measures the per-file extraction cost the
// symboldb index scan pays: one session, one large file, batched queries.
func BenchmarkQuerySessionQueryMulti(b *testing.B) {
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(b, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(b, err)
	b.Cleanup(func() { _ = scheme.Close() })

	var src strings.Builder
	src.WriteString("package pkg\n\n")
	for i := range 2000 {
		fmt.Fprintf(&src, "func fn%04d() string {\n\treturn \"body %04d\"\n}\n\n", i, i)
	}
	file := createFile(b, scheme, "big.go", src.String())
	b.Logf("file size: %d bytes", src.Len())

	parser := syntax.NewParser(scheme, goPkgManager(b), uri)
	session := parser.NewQuerySession()
	b.Cleanup(func() { _ = session.Close() })

	queries := []symbolresolve.MultiQuery{
		{ID: 0, Query: funcQuery, Captures: []string{"fn"}},
		{ID: 1, Query: typeQuery, Captures: []string{"ty"}},
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := session.QueryMulti(context.Background(), file, queries); err != nil {
			b.Fatal(err)
		}
	}
}
