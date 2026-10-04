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
	_ "embed"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/workspace"
)

//go:embed go/locals.scm
var locals []byte

func TestParserHighlights(t *testing.T) {
	wuri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)

	pkgs := newInstalledPkgManager(t)
	parser := syntax.NewParser(nil /* fs not needed for highlights */, pkgs, wuri)
	uri, err := workspaceapi.ParseURI("memory:///file.go")
	require.NoError(t, err)

	it, err := parser.Highlight(uri, "package main\nfunc main() {}")
	require.NoError(t, err)

	locations, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	expected := []textapi.Location{
		{
			From: term.Coordinates{X: 0, Y: 0},
			To:   term.Coordinates{X: 7, Y: 0},
			Attr: term.Attributes{
				Fg: term.ColorYellow,
			},
		},
		{
			From: term.Coordinates{X: 0, Y: 1},
			To:   term.Coordinates{X: 4, Y: 1},
			Attr: term.Attributes{
				Fg: term.ColorYellow,
			},
		},
		{
			From: term.Coordinates{X: 5, Y: 1},
			To:   term.Coordinates{X: 9, Y: 1},
			Attr: term.Attributes{},
		},
		{
			From: term.Coordinates{X: 5, Y: 1},
			To:   term.Coordinates{X: 9, Y: 1},
			Attr: term.Attributes{},
		},
	}
	assert.Equal(t, expected, locations)
}

func TestSearch(t *testing.T) {
	t.Run("returns functions", func(t *testing.T) {
		searcher, uri1, uri2, uri3 := setupSearcherForTests(t,
			"go/tree-sitter.so",
			"go/locals.scm",
		)
		it, err := searcher.Search(string(locals), []string{"local.definition.function"})
		require.NoError(t, err)
		funcs, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.ElementsMatch(t, []syntaxapi.Result{
			{File: uri1, CaptureName: "local.definition.function",
				From: term.Coordinates{X: 5, Y: 3},
				To:   term.Coordinates{X: 13, Y: 3},
				Text: "function",
			},
			{File: uri2, CaptureName: "local.definition.function",
				From: term.Coordinates{X: 5, Y: 3},
				To:   term.Coordinates{X: 13, Y: 3},
				Text: "function",
			},
			{File: uri3, CaptureName: "local.definition.function",
				From: term.Coordinates{X: 5, Y: 3},
				To:   term.Coordinates{X: 13, Y: 3},
				Text: "function",
			},
		}, funcs)
	})

	t.Run("returns all captures if not capture names are passed", func(t *testing.T) {
		searcher, _, _, _ := setupSearcherForTests(t,
			"go/tree-sitter.so",
			"go/locals.scm",
		)
		it, err := searcher.Search(string(locals), []string{})
		require.NoError(t, err)
		funcs, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Len(t, funcs, 45)
	})

	t.Run("returns nothing if query capture name matches nothing", func(t *testing.T) {
		searcher, _, _, _ := setupSearcherForTests(t,
			"go/tree-sitter.so",
			"go/locals.scm",
		)
		it, err := searcher.Search(string(locals), []string{"local.definition.NOTHING"})
		require.NoError(t, err)
		funcs, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Len(t, funcs, 0)
	})

	t.Run("returns error if query is not valid SCM", func(t *testing.T) {
		searcher, _, _, _ := setupSearcherForTests(t,
			"go/tree-sitter.so",
			"go/locals.scm",
		)
		invalidQuery := `((identifier @id)
 (#eq? @id "x"`
		it, err := searcher.Search(invalidQuery, []string{"local.definition.function"})
		assert.NoError(t, err)
		_, err = iterator.ToSlice(context.Background(), it)
		require.Error(t, err)
	})
}

func TestSearchNode(t *testing.T) {
	t.Run("returns multiple node types", func(t *testing.T) {
		searcher, uri1, uri2, uri3 := setupSearcherForTests(t,
			"go/tree-sitter.so",
			"go/locals.scm",
		)
		it, err := searcher.SearchNode(syntaxapi.NodeCaptureDefinitionFunc | syntaxapi.NodeCaptureDefinitionType)
		require.NoError(t, err)
		funcs, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.ElementsMatch(t, []syntaxapi.Result{
			{File: uri1, CaptureName: "local.definition.function",
				From: term.Coordinates{X: 5, Y: 3},
				To:   term.Coordinates{X: 13, Y: 3},
				Text: "function",
			},
			{File: uri2, CaptureName: "local.definition.function",
				From: term.Coordinates{X: 5, Y: 3},
				To:   term.Coordinates{X: 13, Y: 3},
				Text: "function",
			},
			{File: uri3, CaptureName: "local.definition.function",
				From: term.Coordinates{X: 5, Y: 3},
				To:   term.Coordinates{X: 13, Y: 3},
				Text: "function",
			},
			{File: uri1, CaptureName: "local.definition.type",
				From: term.Coordinates{X: 5, Y: 8},
				To:   term.Coordinates{X: 11, Y: 8},
				Text: "myType",
			},
			{File: uri2, CaptureName: "local.definition.type",
				From: term.Coordinates{X: 5, Y: 8},
				To:   term.Coordinates{X: 11, Y: 8},
				Text: "myType",
			},
			{File: uri3, CaptureName: "local.definition.type",
				From: term.Coordinates{X: 5, Y: 8},
				To:   term.Coordinates{X: 11, Y: 8},
				Text: "myType",
			},
		}, funcs)
	})

	t.Run("returns all captures if not capture names are passed", func(t *testing.T) {
		searcher, _, _, _ := setupSearcherForTests(t,
			"go/tree-sitter.so",
			"go/locals.scm",
		)
		it, err := searcher.SearchNode(syntaxapi.NodeCaptureNameAll)
		require.NoError(t, err)
		allNodes, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		it2, err := searcher.Search(string(locals), []string{})
		require.NoError(t, err)
		all, err := iterator.ToSlice(context.Background(), it2)
		require.NoError(t, err)
		assert.ElementsMatch(t, allNodes, all)
	})

	t.Run("returns error if passed invalid capture name", func(t *testing.T) {
		searcher, _, _, _ := setupSearcherForTests(t,
			"go/tree-sitter.so",
			"go/locals.scm",
		)
		_, err := searcher.SearchNode(syntaxapi.NodeCaptureName(0))
		require.Error(t, err)
	})
}

func TestQueryNode(t *testing.T) {
	t.Run("returns functions", func(t *testing.T) {
		searcher, uri1, _, _ := setupSearcherForTests(t,
			"go/tree-sitter.so",
			"go/locals.scm",
		)
		it, err := searcher.QueryNode(uri1, syntaxapi.NodeCaptureDefinitionFunc)
		require.NoError(t, err)
		funcs, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.ElementsMatch(t, []syntaxapi.Result{
			{File: uri1, CaptureName: "local.definition.function",
				From: term.Coordinates{X: 5, Y: 3},
				To:   term.Coordinates{X: 13, Y: 3},
				Text: "function",
			},
		}, funcs)
	})

	t.Run("returns all captures if not capture names are passed", func(t *testing.T) {
		searcher, uri1, _, _ := setupSearcherForTests(t,
			"go/tree-sitter.so",
			"go/locals.scm",
		)
		it, err := searcher.QueryNode(uri1, syntaxapi.NodeCaptureNameAll)
		require.NoError(t, err)
		allNodes, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		it2, err := searcher.Query(uri1, string(locals), []string{})
		require.NoError(t, err)
		all, err := iterator.ToSlice(context.Background(), it2)
		require.NoError(t, err)
		assert.ElementsMatch(t, allNodes, all)
	})

	t.Run("returns error if passed invalid capture name", func(t *testing.T) {
		searcher, uri1, _, _ := setupSearcherForTests(t,
			"go/tree-sitter.so",
			"go/locals.scm",
		)
		_, err := searcher.QueryNode(uri1, syntaxapi.NodeCaptureName(0))
		require.Error(t, err)
	})
}

func createFile(t testing.TB, scheme schemeapi.Scheme, name string, content string) workspaceapi.URI {
	f, err := scheme.Create(name)
	require.NoError(t, err)

	_, err = f.Write([]byte(content))
	require.NoError(t, err)

	uri, err := scheme.URI(f.Name())
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return uri
}

func setupSearcherForTests(t *testing.T, filesAvail ...string) (
	searcher syntaxapi.Parser, uri1, uri2, uri3 workspaceapi.URI,
) {
	logrus.SetLevel(logrus.TraceLevel)
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	wd, err := os.Getwd()
	require.NoError(t, err)
	var fullPathFiles []string
	for _, file := range filesAvail {
		fullPathFiles = append(fullPathFiles, resolveFixture(t, wd, file))
	}
	pkgs := &mockPkgManager{
		fullPathFiles: fullPathFiles,
	}

	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)

	uri1 = createFile(t, scheme, "a.go", searchFileContent)
	uri2 = createFile(t, scheme, "b.go", searchFileContent)
	uri3 = createFile(t, scheme, "c.go", searchFileContent)
	searcher = syntax.NewParser(scheme, pkgs, uri)
	return
}

const searchFileContent = `
package pkg

func function() string {
}

var a int
	
type myType struct {
	a string
}
`

func TestSearchExcludesNoiseDirs(t *testing.T) {
	logrus.SetLevel(logrus.TraceLevel)
	dir := t.TempDir()

	writeFile := func(rel string) workspaceapi.URI {
		full := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(searchFileContent), 0o644))
		uri, err := workspaceapi.CurrentUserHostURI(full)
		require.NoError(t, err)
		return uri
	}

	srcURI := writeFile("src/a.go")
	writeFile(".venv/lib/v.go")
	writeFile("node_modules/n.go")
	writeFile("__pycache__/p.go")
	writeFile("target/debug/t.go")

	wd, err := os.Getwd()
	require.NoError(t, err)
	pkgs := &mockPkgManager{fullPathFiles: []string{
		grammarfixture.ParserPath(t, filepath.Join(wd, "go")),
		filepath.Join(wd, "go/locals.scm"),
	}}

	uri, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)

	searcher := syntax.NewParser(scheme, pkgs, uri)
	it, err := searcher.Search(string(locals), []string{"local.definition.function"})
	require.NoError(t, err)
	funcs, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	require.NotEmpty(t, funcs)
	for _, f := range funcs {
		assert.Equal(t, srcURI, f.File,
			"expected only src/a.go results, got file under a noise dir")
	}
}
