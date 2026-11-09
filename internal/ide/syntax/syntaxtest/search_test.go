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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

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
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

//go:embed go/locals.scm
var locals []byte

func TestParserHighlights(t *testing.T) {
	wuri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)

	pkgs := newInstalledPkgManager(t)
	parser := treesitter.NewParser(nil /* fs not needed for highlights */, pkgs, wuri)
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

func TestParserHighlightsUseConfiguredCaptureNamesAttributes(t *testing.T) {
	wuri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("memory:///file.go")
	require.NoError(t, err)

	keyword := term.Attributes{Fg: term.ColorGreen, Attrs: term.AttrBold}
	function := term.Attributes{Fg: term.ColorAqua}
	parser := treesitter.NewParser(nil, newInstalledPkgManager(t), wuri).
		WithCaptureNamesAttributes(map[string]term.Attributes{
			"keyword":  keyword,
			"function": function,
		})

	it, err := parser.Highlight(uri, "package main\nfunc main() {}")
	require.NoError(t, err)
	locations, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	attrs := make(map[term.Coordinates][]term.Attributes)
	for _, l := range locations {
		attrs[l.From] = append(attrs[l.From], l.Attr)
	}
	assert.Equal(t, []term.Attributes{keyword}, attrs[term.Coordinates{X: 0, Y: 0}], "package")
	assert.Equal(t, []term.Attributes{keyword}, attrs[term.Coordinates{X: 0, Y: 1}], "func")
	assert.Contains(t, attrs[term.Coordinates{X: 5, Y: 1}], function, "main")
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

func TestParserGrammarFingerprintOnDisk(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	tsx := grammarfixture.PackageFiles(t, filepath.Join(wd, "tsx"))
	typescript := grammarfixture.PackageFiles(t, filepath.Join(wd, "typescript"))
	// with returns a copy of files with changes applied; a nil change
	// removes the file.
	with := func(files map[string][]byte, changes map[string][]byte) map[string][]byte {
		out := maps.Clone(files)
		for name, data := range changes {
			if data == nil {
				delete(out, name)
				continue
			}
			out[name] = data
		}
		return out
	}
	upgradedLocals := func(files map[string][]byte) []byte {
		return append(slices.Clone(files[treesitter.LocalsFilename]),
			"\n(class_declaration name: (type_identifier) @local.definition.type)\n"...)
	}
	catalog := map[string][]string{"tsx": {"1", "2"}, "typescript": {"1", "2"}}
	unchanged := func(_ *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
		return pkgs
	}

	tests := []struct {
		name string
		// change alters an install of tsx release 1 and returns the
		// package manager the next parser reads from.
		change     func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager
		sameParser bool
		wantEqual  bool
		wantErr    bool
	}{
		{
			name:      "nothing changed",
			change:    unchanged,
			wantEqual: true,
		},
		{
			name:       "nothing changed, same parser",
			change:     unchanged,
			sameParser: true,
			wantEqual:  true,
		},
		{
			name: "installed files touched",
			change: func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
				later := time.Now().Add(time.Hour)
				for _, path := range pkgs.Files(t, "tsx") {
					require.NoError(t, os.Chtimes(path, later, later))
				}
				return pkgs
			},
			wantEqual: true,
		},
		{
			name: "installed files made read-only",
			change: func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
				for _, path := range pkgs.Files(t, "tsx") {
					require.NoError(t, os.Chmod(path, 0o444))
				}
				return pkgs
			},
			wantEqual: true,
		},
		{
			name: "same version reinstalled",
			change: func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
				pkgs.Uninstall(t, "tsx", "1")
				pkgs.Install(t, "tsx", "1", tsx)
				return pkgs
			},
			wantEqual: true,
		},
		{
			name: "identical release under a new version",
			change: func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
				pkgs.Install(t, "tsx", "2", tsx)
				return pkgs
			},
			wantEqual: true,
		},
		{
			name: "same release in another data directory",
			change: func(t *testing.T, _ *grammarfixture.Packages) syntax.PkgManager {
				other := grammarfixture.NewPackages(t, catalog)
				other.Install(t, "tsx", "1", tsx)
				return other
			},
			wantEqual: true,
		},
		{
			name: "only files extraction does not read changed",
			change: func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
				pkgs.Install(t, "tsx", "2", with(tsx, map[string][]byte{
					treesitter.HighlightsFilename: []byte("(identifier) @variable\n"),
					treesitter.IndentsFilename:    nil,
					"README.md":                   []byte("# tsx\n"),
					"queries/injections.scm":      []byte("(comment) @injection.content\n"),
				}))
				return pkgs
			},
			wantEqual: true,
		},
		{
			name: "another language upgraded",
			change: func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
				pkgs.Install(t, "typescript", "1", typescript)
				pkgs.Install(t, "typescript", "2", with(typescript, map[string][]byte{
					treesitter.LocalsFilename: upgradedLocals(typescript),
				}))
				return pkgs
			},
			wantEqual: true,
		},
		{
			name: "rolled back to the previous version",
			change: func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
				pkgs.Install(t, "tsx", "2", with(tsx, map[string][]byte{
					treesitter.LocalsFilename: upgradedLocals(tsx),
				}))
				pkgs.Use(t, "tsx", "1")
				return pkgs
			},
			wantEqual: true,
		},
		{
			name: "locals query upgraded",
			change: func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
				pkgs.Install(t, "tsx", "2", with(tsx, map[string][]byte{
					treesitter.LocalsFilename: upgradedLocals(tsx),
				}))
				return pkgs
			},
		},
		{
			name: "grammar upgraded",
			change: func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
				pkgs.Install(t, "tsx", "2", with(tsx, map[string][]byte{
					treesitter.ParserFilename: typescript[treesitter.ParserFilename],
				}))
				return pkgs
			},
		},
		{
			name: "uninstalled",
			change: func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
				pkgs.Uninstall(t, "tsx", "1")
				return pkgs
			},
			wantErr: true,
		},
		{
			name: "release without a locals query",
			change: func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
				pkgs.Install(t, "tsx", "2", with(tsx, map[string][]byte{
					treesitter.LocalsFilename: nil,
				}))
				return pkgs
			},
			wantErr: true,
		},
		{
			name: "release without a grammar",
			change: func(t *testing.T, pkgs *grammarfixture.Packages) syntax.PkgManager {
				pkgs.Install(t, "tsx", "2", with(tsx, map[string][]byte{
					treesitter.ParserFilename: nil,
				}))
				return pkgs
			},
			wantErr: true,
		},
	}
	root, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pkgs := grammarfixture.NewPackages(t, catalog)
			pkgs.Install(t, "tsx", "1", tsx)
			parser := treesitter.NewParser(nil, pkgs, root)
			want, err := parser.GrammarFingerprint(ctx, "tsx")
			require.NoError(t, err)

			pm := tt.change(t, pkgs)
			if !tt.sameParser {
				parser = treesitter.NewParser(nil, pm, root)
			}
			got, err := parser.GrammarFingerprint(ctx, "tsx")
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantEqual {
				assert.Equal(t, want, got)
			} else {
				assert.NotEqual(t, want, got)
			}
		})
	}
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
	searcher = treesitter.NewParser(scheme, pkgs, uri)
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

	searcher := treesitter.NewParser(scheme, pkgs, uri)
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
