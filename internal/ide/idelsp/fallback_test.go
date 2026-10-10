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

package idelsp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

// fallbackTestURI must use a language that has no LSP server
// configured in langConfigs, so Manager requests take the
// ErrLanguageNotSupported path that triggers the tree-sitter fallback.
const fallbackTestURI = "file:///ws/main.lua"

// errIter is a canned iterator with an optional terminal error.
type errIter[T any] struct {
	items []T
	err   error
	i     int
}

func (it *errIter[T]) Next(context.Context) (T, bool) {
	var zero T
	if it.i >= len(it.items) {
		return zero, false
	}
	v := it.items[it.i]
	it.i++
	return v, true
}

func (it *errIter[T]) Err() error   { return it.err }
func (it *errIter[T]) Close() error { return nil }

// fallbackParser implements syntaxapi.Parser with canned data.
type fallbackParser struct {
	captures []syntaxapi.Result
	queryErr error
	iterErr  error
	resolves map[string][]syntaxapi.Match
	resolved []string
	names    []string
	listErr  error
}

func (p *fallbackParser) Search(
	string, []string, ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (p *fallbackParser) SearchNode(
	syntaxapi.NodeCaptureName, ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (p *fallbackParser) Query(
	workspaceapi.URI, string, []string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}

func (p *fallbackParser) QueryNode(
	workspaceapi.URI, syntaxapi.NodeCaptureName,
) (iterator.Iterator[syntaxapi.Result], error) {
	if p.queryErr != nil {
		return nil, p.queryErr
	}
	return &errIter[syntaxapi.Result]{
		items: p.captures, err: p.iterErr,
	}, nil
}

func (p *fallbackParser) Highlight(
	workspaceapi.URI, string,
) (iterator.Iterator[textapi.Location], error) {
	return iterator.Empty[textapi.Location](), nil
}

func (p *fallbackParser) ResolveSymbol(
	_ context.Context, name string, _ syntaxapi.Progress,
) (iterator.Iterator[syntaxapi.Match], error) {
	p.resolved = append(p.resolved, name)
	return iterator.FromSlice(p.resolves[name]), nil
}

func (p *fallbackParser) ListReferencedSymbols(
	context.Context,
) (iterator.Iterator[string], error) {
	if p.listErr != nil {
		return nil, p.listErr
	}
	return iterator.FromSlice(p.names), nil
}

func capr(name, text string, fy, fx, ty, tx int) syntaxapi.Result {
	return syntaxapi.Result{
		Text:        text,
		From:        term.Coordinates{Y: fy, X: fx},
		To:          term.Coordinates{Y: ty, X: tx},
		CaptureName: name,
	}
}

// baseCaptures models this synthetic locals.scm pass:
//
//	L0: fn outer(a: i32) void {   // + ref y at (0,15)
//	L1:     var x = a;            // + def y at (1,16)
//	L2:     {
//	L3:         var x = 1;
//	L4:         use(x);
//	L5:     }
//	L6:     use(x);               // + def y at (6,16)
//	L7:     use(a);               // + ref y at (7,16)
//	L8: }
func baseCaptures() []syntaxapi.Result {
	return []syntaxapi.Result{
		capr("local.scope", "", 0, 0, 8, 1),
		capr("local.scope", "", 2, 4, 5, 5),
		capr("local.definition.function", "outer", 0, 3, 0, 8),
		capr("local.definition.var", "a", 0, 9, 0, 10),
		capr("local.definition.var", "x", 1, 8, 1, 9),
		capr("local.definition.var", "x", 3, 12, 3, 13),
		capr("local.definition.var", "y", 1, 16, 1, 17),
		capr("local.definition.var", "y", 6, 16, 6, 17),
		capr("local.reference", "a", 1, 12, 1, 13),
		capr("local.reference", "x", 4, 12, 4, 13),
		capr("local.reference", "x", 6, 8, 6, 9),
		capr("local.reference", "a", 7, 8, 7, 9),
		capr("local.reference", "y", 0, 15, 0, 16),
		capr("local.reference", "y", 7, 16, 7, 17),
		capr("local.reference", "outer", 9, 0, 9, 5),
	}
}

func newTestFallback(
	p *fallbackParser, indexed bool, content string,
) *syntaxFallback {
	return newSyntaxFallback(p, func(string) (string, error) {
		if content == "" {
			return "", errors.New("no content")
		}
		return content, nil
	}, indexed)
}

func TestNewSyntaxFallbackPanics(t *testing.T) {
	readFile := func(string) (string, error) { return "", nil }
	require.Panics(t, func() {
		newSyntaxFallback(nil, readFile, false)
	})
	require.Panics(t, func() {
		newSyntaxFallback(&fallbackParser{}, nil, false)
	})
}

func defParams(line, char int) semanticapi.DefinitionParams {
	return semanticapi.DefinitionParams{
		TextDocument: semanticapi.TextDocumentIdentifier{
			URI: fallbackTestURI,
		},
		Position: semanticapi.Position{
			Line: uint32(line), Character: uint32(char),
		},
	}
}

func wantRange(sy, sx, ey, ex int) semanticapi.Range {
	return semanticapi.Range{
		Start: semanticapi.Position{
			Line: uint32(sy), Character: uint32(sx),
		},
		End: semanticapi.Position{
			Line: uint32(ey), Character: uint32(ex),
		},
	}
}

func TestFallbackDefinitionLocals(t *testing.T) {
	tests := []struct {
		name      string
		line, col int
		want      semanticapi.Range
	}{
		{
			name: "reference resolves to outer var",
			line: 6, col: 8,
			want: wantRange(1, 8, 1, 9),
		},
		{
			name: "shadowed reference resolves to inner var",
			line: 4, col: 12,
			want: wantRange(3, 12, 3, 13),
		},
		{
			name: "reference resolves to parameter",
			line: 7, col: 8,
			want: wantRange(0, 9, 0, 10),
		},
		{
			name: "reference in initializer resolves to parameter",
			line: 1, col: 12,
			want: wantRange(0, 9, 0, 10),
		},
		{
			name: "cursor on definition resolves to itself",
			line: 3, col: 12,
			want: wantRange(3, 12, 3, 13),
		},
		{
			name: "cursor at end of identifier still hits",
			line: 6, col: 9,
			want: wantRange(1, 8, 1, 9),
		},
		{
			name: "same scope picks latest preceding definition",
			line: 7, col: 16,
			want: wantRange(6, 16, 6, 17),
		},
		{
			name: "reference before any definition picks earliest",
			line: 0, col: 15,
			want: wantRange(1, 16, 1, 17),
		},
		{
			name: "function name hoisted outside its own scope",
			line: 9, col: 0,
			want: wantRange(0, 3, 0, 8),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fallbackParser{captures: baseCaptures()}
			s := newTestFallback(p, false, "")
			res, err := s.definition(
				context.Background(), defParams(tt.line, tt.col),
			)
			require.NoError(t, err)
			require.NotNil(t, res.Location)
			assert.Equal(t, fallbackTestURI, res.Location.URI)
			assert.Equal(t, tt.want, res.Location.Range)
			assert.Empty(t, p.resolved,
				"local resolution must not hit the symbol index")
		})
	}
}

func qualifiedContent() string {
	return strings.Repeat("\n", 9) + "const top = std.mem.copy;\n"
}

func TestFallbackDefinitionQualified(t *testing.T) {
	match := syntaxapi.Match{
		URI: "file:///dep/std.zig",
		Pos: term.Coordinates{Y: 2, X: 4},
	}
	wantLoc := semanticapi.Location{
		URI:   "file:///dep/std.zig",
		Range: wantRange(2, 4, 2, 4),
	}
	tests := []struct {
		name         string
		col          int
		resolves     map[string][]syntaxapi.Match
		wantResolved []string
	}{
		{
			name: "full dotted token resolves",
			col:  21, // on "copy"
			resolves: map[string][]syntaxapi.Match{
				"std.mem.copy": {match},
			},
			wantResolved: []string{"std.mem.copy"},
		},
		{
			name: "shorter dotted suffix retried",
			col:  21,
			resolves: map[string][]syntaxapi.Match{
				"mem.copy": {match},
			},
			wantResolved: []string{"std.mem.copy", "mem.copy"},
		},
		{
			name: "token truncated after cursor segment",
			col:  17, // on "mem"
			resolves: map[string][]syntaxapi.Match{
				"std.mem": {match},
			},
			wantResolved: []string{"std.mem"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fallbackParser{
				captures: baseCaptures(),
				resolves: tt.resolves,
			}
			s := newTestFallback(p, false, qualifiedContent())
			res, err := s.definition(
				context.Background(), defParams(9, tt.col),
			)
			require.NoError(t, err)
			require.Len(t, res.Locations, 1)
			assert.Equal(t, wantLoc, res.Locations[0])
			assert.Equal(t, tt.wantResolved, p.resolved)
		})
	}
}

func TestFallbackDefinitionUnqualified(t *testing.T) {
	content := strings.Repeat("\n", 10) + "    Foo()\n"
	newParser := func() *fallbackParser {
		return &fallbackParser{
			captures: append(baseCaptures(),
				capr("local.reference", "Foo", 10, 4, 10, 7)),
			names: []string{"p1.Foo", "p2.Foo", "px.Bar"},
			resolves: map[string][]syntaxapi.Match{
				"p1.Foo": {{
					URI: "file:///ws/p1.zig",
					Pos: term.Coordinates{Y: 1, X: 0},
				}},
				"p2.Foo": {{
					URI: "file:///ws/p2.zig",
					Pos: term.Coordinates{Y: 2, X: 0},
				}},
			},
		}
	}

	t.Run("indexed returns all namespace candidates", func(t *testing.T) {
		p := newParser()
		s := newTestFallback(p, true, content)
		res, err := s.definition(context.Background(), defParams(10, 4))
		require.NoError(t, err)
		require.Len(t, res.Locations, 2)
		assert.Equal(t, "file:///ws/p1.zig", res.Locations[0].URI)
		assert.Equal(t, "file:///ws/p2.zig", res.Locations[1].URI)
	})

	t.Run("not indexed skips workspace enumeration", func(t *testing.T) {
		p := newParser()
		s := newTestFallback(p, false, content)
		res, err := s.definition(context.Background(), defParams(10, 4))
		require.NoError(t, err)
		assert.Nil(t, res.Location)
		assert.Empty(t, res.Locations)
		assert.Empty(t, p.resolved)
	})
}

func TestFallbackWorkspaceSymbolNotIndexed(t *testing.T) {
	p := &fallbackParser{names: []string{"mypkg.Frobnicate"}}
	s := newTestFallback(p, false, "")
	syms, err := s.workspaceSymbol(
		context.Background(),
		semanticapi.WorkspaceSymbolParams{Query: "frob"},
	)
	require.NoError(t, err)
	assert.Empty(t, syms)
	assert.Empty(t, p.resolved,
		"unindexed fallback must not enumerate the workspace")
}

func TestFallbackDefinitionErrors(t *testing.T) {
	t.Run("query error propagates", func(t *testing.T) {
		p := &fallbackParser{queryErr: errors.New("not installed")}
		s := newTestFallback(p, false, "")
		_, err := s.definition(context.Background(), defParams(0, 0))
		require.ErrorContains(t, err, "not installed")
	})

	t.Run("iterator terminal error propagates", func(t *testing.T) {
		p := &fallbackParser{
			captures: baseCaptures(),
			iterErr:  errors.New("grammar broke"),
		}
		s := newTestFallback(p, false, "")
		_, err := s.definition(context.Background(), defParams(6, 8))
		require.ErrorContains(t, err, "grammar broke")
	})

	t.Run("nothing under cursor yields empty result", func(t *testing.T) {
		p := &fallbackParser{captures: baseCaptures()}
		s := newTestFallback(p, false, "")
		res, err := s.definition(context.Background(), defParams(2, 0))
		require.NoError(t, err)
		assert.Nil(t, res.Location)
		assert.Empty(t, res.Locations)
	})
}

func refParams(
	line, char int, includeDecl bool,
) semanticapi.ReferenceParams {
	return semanticapi.ReferenceParams{
		TextDocument: semanticapi.TextDocumentIdentifier{
			URI: fallbackTestURI,
		},
		Position: semanticapi.Position{
			Line: uint32(line), Character: uint32(char),
		},
		Context: semanticapi.ReferenceContext{
			IncludeDeclaration: includeDecl,
		},
	}
}

func TestFallbackReferences(t *testing.T) {
	tests := []struct {
		name        string
		line, col   int
		includeDecl bool
		want        []semanticapi.Range
	}{
		{
			name: "outer var excludes shadowed sites",
			line: 6, col: 8, includeDecl: true,
			want: []semanticapi.Range{
				wantRange(1, 8, 1, 9), // declaration
				wantRange(6, 8, 6, 9),
			},
		},
		{
			name: "without declaration",
			line: 6, col: 8, includeDecl: false,
			want: []semanticapi.Range{wantRange(6, 8, 6, 9)},
		},
		{
			name: "cursor on inner definition finds its references",
			line: 3, col: 12, includeDecl: true,
			want: []semanticapi.Range{
				wantRange(3, 12, 3, 13), // declaration
				wantRange(4, 12, 4, 13),
			},
		},
		{
			name: "parameter references",
			line: 0, col: 9, includeDecl: false,
			want: []semanticapi.Range{
				wantRange(1, 12, 1, 13),
				wantRange(7, 8, 7, 9),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fallbackParser{captures: baseCaptures()}
			s := newTestFallback(p, false, "")
			locs, err := s.references(
				context.Background(),
				refParams(tt.line, tt.col, tt.includeDecl),
			)
			require.NoError(t, err)
			got := make([]semanticapi.Range, 0, len(locs))
			for _, l := range locs {
				assert.Equal(t, fallbackTestURI, l.URI)
				got = append(got, l.Range)
			}
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

func TestFallbackReferencesQualified(t *testing.T) {
	p := &fallbackParser{
		captures: baseCaptures(),
		resolves: map[string][]syntaxapi.Match{
			"std.mem.copy": {
				{URI: "file:///a.zig", Pos: term.Coordinates{Y: 1, X: 1}},
				{URI: "file:///b.zig", Pos: term.Coordinates{Y: 2, X: 2}},
			},
		},
	}
	s := newTestFallback(p, false, qualifiedContent())
	locs, err := s.references(
		context.Background(), refParams(9, 21, true),
	)
	require.NoError(t, err)
	require.Len(t, locs, 2)
	assert.Equal(t, "file:///a.zig", locs[0].URI)
	assert.Equal(t, "file:///b.zig", locs[1].URI)
}

func TestFallbackWorkspaceSymbol(t *testing.T) {
	newParser := func() *fallbackParser {
		return &fallbackParser{
			names: []string{
				"mypkg.Frobnicate",
				"mypkg.Widget.Render",
				"other.Unrelated",
			},
			resolves: map[string][]syntaxapi.Match{
				"mypkg.Frobnicate": {{
					URI: "file:///ws/frob.zig",
					Pos: term.Coordinates{Y: 3, X: 0},
				}},
				"mypkg.Widget.Render": {{
					URI: "file:///ws/widget.zig",
					Pos: term.Coordinates{Y: 8, X: 0},
				}},
			},
		}
	}

	t.Run("fuzzy query picks function", func(t *testing.T) {
		s := newTestFallback(newParser(), true, "")
		syms, err := s.workspaceSymbol(
			context.Background(),
			semanticapi.WorkspaceSymbolParams{Query: "frob"},
		)
		require.NoError(t, err)
		require.Len(t, syms, 1)
		assert.Equal(t, "mypkg.Frobnicate", syms[0].Name)
		assert.Equal(t, semanticapi.SymbolKindFunction, syms[0].Kind)
		assert.Equal(t, "file:///ws/frob.zig", syms[0].Location.URI)
	})

	t.Run("method kind from container segment", func(t *testing.T) {
		s := newTestFallback(newParser(), true, "")
		syms, err := s.workspaceSymbol(
			context.Background(),
			semanticapi.WorkspaceSymbolParams{Query: "render"},
		)
		require.NoError(t, err)
		require.Len(t, syms, 1)
		assert.Equal(t, "mypkg.Widget.Render", syms[0].Name)
		assert.Equal(t, semanticapi.SymbolKindMethod, syms[0].Kind)
	})

	t.Run("unresolvable names are dropped", func(t *testing.T) {
		s := newTestFallback(newParser(), true, "")
		syms, err := s.workspaceSymbol(
			context.Background(),
			semanticapi.WorkspaceSymbolParams{Query: "unrelated"},
		)
		require.NoError(t, err)
		assert.Empty(t, syms)
	})
}

// outlineCaptures models this synthetic locals.scm pass:
//
//	L1: def outer():          // scope L1-L6
//	L2:     x = 1
//	L3:     def inner():      // scope L3-L5
//	L4:         y = 2
//	L6: TOP = 3
//	L7: class Widget:         // scope L7-L10
//	L8:     def render(self): // scope L8-L10
func outlineCaptures() []syntaxapi.Result {
	return []syntaxapi.Result{
		capr("local.scope", "", 0, 0, 12, 0),
		capr("local.scope", "", 1, 0, 6, 0),
		capr("local.definition.function", "outer", 1, 4, 1, 9),
		capr("local.definition.var", "x", 2, 4, 2, 5),
		capr("local.scope", "", 3, 4, 5, 0),
		capr("local.definition.function", "inner", 3, 8, 3, 13),
		capr("local.definition.var", "y", 4, 8, 4, 9),
		capr("local.definition.var", "TOP", 6, 0, 6, 3),
		capr("local.scope", "", 7, 0, 10, 0),
		capr("local.definition.type", "Widget", 7, 6, 7, 12),
		capr("local.scope", "", 8, 4, 10, 0),
		capr("local.definition.method", "render", 8, 8, 8, 14),
	}
}

func docSymParams() semanticapi.DocumentSymbolParams {
	return semanticapi.DocumentSymbolParams{
		TextDocument: semanticapi.TextDocumentIdentifier{
			URI: fallbackTestURI,
		},
	}
}

// flattenOutline renders the tree as "dotted.name:kind:line" entries
// in traversal order, mirroring how outline_file presents it.
func flattenOutline(
	syms []semanticapi.DocumentSymbol, parent string,
) []string {
	var ret []string
	for _, s := range syms {
		name := s.Name
		if parent != "" {
			name = parent + "." + name
		}
		ret = append(ret, fmt.Sprintf(
			"%s:%d:%d", name, s.Kind, s.Range.Start.Line,
		))
		ret = append(ret, flattenOutline(s.Children, s.Name)...)
	}
	return ret
}

func TestSyntaxFallbackDocumentSymbol(t *testing.T) {
	t.Run("nests definitions under owning scopes", func(t *testing.T) {
		s := newTestFallback(
			&fallbackParser{captures: outlineCaptures()}, false, "",
		)
		res, err := s.documentSymbol(context.Background(), docSymParams())
		require.NoError(t, err)
		assert.Equal(t, []string{
			"outer:12:1",
			"outer.x:13:2",
			"outer.inner:12:3",
			"inner.y:13:4",
			"TOP:13:6",
			"Widget:5:7",
			"Widget.render:6:8",
		}, flattenOutline(res.DocumentSymbols, ""))
		outer := res.DocumentSymbols[0]
		assert.Equal(t, wantRange(1, 0, 6, 0), outer.Range)
		assert.Equal(t, wantRange(1, 4, 1, 9), outer.SelectionRange)
	})

	t.Run("root scope never owns a definition", func(t *testing.T) {
		s := newTestFallback(&fallbackParser{captures: []syntaxapi.Result{
			capr("local.scope", "", 0, 0, 4, 0),
			capr("local.definition.function", "first", 0, 4, 0, 9),
			capr("local.definition.function", "second", 2, 4, 2, 10),
		}}, false, "")
		res, err := s.documentSymbol(context.Background(), docSymParams())
		require.NoError(t, err)
		assert.Equal(t, []string{
			"first:12:0", "second:12:2",
		}, flattenOutline(res.DocumentSymbols, ""))
	})

	t.Run("definitions without scopes stay flat", func(t *testing.T) {
		s := newTestFallback(&fallbackParser{captures: []syntaxapi.Result{
			capr("local.definition.var", "a", 0, 0, 0, 1),
			capr("local.definition.namespace", "ns", 1, 0, 1, 2),
		}}, false, "")
		res, err := s.documentSymbol(context.Background(), docSymParams())
		require.NoError(t, err)
		assert.Equal(t, []string{
			"a:13:0", "ns:3:1",
		}, flattenOutline(res.DocumentSymbols, ""))
	})

	t.Run("no definitions yields empty result", func(t *testing.T) {
		s := newTestFallback(&fallbackParser{captures: []syntaxapi.Result{
			capr("local.scope", "", 0, 0, 4, 0),
		}}, false, "")
		res, err := s.documentSymbol(context.Background(), docSymParams())
		require.NoError(t, err)
		assert.Empty(t, res.DocumentSymbols)
	})

	// Python's locals.scm captures a method as both
	// local.definition.method and local.definition.function; the
	// duplicate must not become a child of itself.
	t.Run("captures sharing a range collapse to one symbol", func(t *testing.T) {
		s := newTestFallback(&fallbackParser{captures: []syntaxapi.Result{
			capr("local.scope", "", 0, 0, 8, 0),
			capr("local.scope", "", 1, 0, 5, 0),
			capr("local.definition.type", "Widget", 1, 6, 1, 12),
			capr("local.scope", "", 2, 4, 5, 0),
			capr("local.definition.function", "render", 2, 8, 2, 14),
			capr("local.definition.method", "render", 2, 8, 2, 14),
			capr("local.definition.var", "pad", 3, 8, 3, 11),
		}}, false, "")
		res, err := s.documentSymbol(context.Background(), docSymParams())
		require.NoError(t, err)
		assert.Equal(t, []string{
			"Widget:5:1", "Widget.render:6:2", "render.pad:13:3",
		}, flattenOutline(res.DocumentSymbols, ""))
	})

	t.Run("query error propagates", func(t *testing.T) {
		s := newTestFallback(
			&fallbackParser{queryErr: errors.New("not installed")},
			false, "",
		)
		_, err := s.documentSymbol(context.Background(), docSymParams())
		require.Error(t, err)
	})
}

func TestLanguageNotSupportedSentinel(t *testing.T) {
	_, err := languageForFilename("/ws/notes.md")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLanguageNotSupported))
	assert.Contains(t, err.Error(), "LSP is not supported yet")
	assert.Contains(t, err.Error(), "markdown")
	assert.True(t, isNoServer(err))
	assert.True(t, isNoServer(ErrNoServer))
	assert.False(t, isNoServer(errors.New("boom")))
}

func TestManagerDefinitionFallback(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///ws")
	require.NoError(t, err)

	t.Run("no server falls back to syntax", func(t *testing.T) {
		p := &fallbackParser{captures: baseCaptures()}
		m := New(uri, newTestScheme(), nil, nil, nil, nil, Config{
			NoInitializeServer: true, Parser: p,
		})
		defer m.Close() // nolint:errcheck
		res, derr := m.Definition(
			context.Background(), defParams(6, 8),
		)
		require.NoError(t, derr)
		require.NotNil(t, res.Location)
		assert.Equal(t, wantRange(1, 8, 1, 9), res.Location.Range)
	})

	t.Run("no parser preserves error", func(t *testing.T) {
		m := New(uri, newTestScheme(), nil, nil, nil, nil, Config{
			NoInitializeServer: true,
		})
		defer m.Close() // nolint:errcheck
		_, derr := m.Definition(
			context.Background(), defParams(6, 8),
		)
		require.Error(t, derr)
		assert.True(t, errors.Is(derr, ErrLanguageNotSupported))
	})

	t.Run("fallback failure preserves original error", func(t *testing.T) {
		p := &fallbackParser{queryErr: errors.New("not installed")}
		m := New(uri, newTestScheme(), nil, nil, nil, nil, Config{
			NoInitializeServer: true, Parser: p,
		})
		defer m.Close() // nolint:errcheck
		_, derr := m.Definition(
			context.Background(), defParams(6, 8),
		)
		require.Error(t, derr)
		assert.True(t, errors.Is(derr, ErrLanguageNotSupported))
	})
}

func TestManagerReferencesFallback(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///ws")
	require.NoError(t, err)
	p := &fallbackParser{captures: baseCaptures()}
	m := New(uri, newTestScheme(), nil, nil, nil, nil, Config{
		NoInitializeServer: true, Parser: p,
	})
	defer m.Close() // nolint:errcheck
	locs, rerr := m.References(
		context.Background(), refParams(6, 8, true),
	)
	require.NoError(t, rerr)
	require.Len(t, locs, 2)
}

func TestManagerDocumentSymbolFallback(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///ws")
	require.NoError(t, err)

	t.Run("no server falls back to syntax", func(t *testing.T) {
		p := &fallbackParser{captures: outlineCaptures()}
		m := New(uri, newTestScheme(), nil, nil, nil, nil, Config{
			NoInitializeServer: true, Parser: p,
		})
		defer m.Close() // nolint:errcheck
		res, derr := m.DocumentSymbol(
			context.Background(), docSymParams(),
		)
		require.NoError(t, derr)
		assert.Equal(t, []string{
			"outer:12:1",
			"outer.x:13:2",
			"outer.inner:12:3",
			"inner.y:13:4",
			"TOP:13:6",
			"Widget:5:7",
			"Widget.render:6:8",
		}, flattenOutline(res.DocumentSymbols, ""))
	})

	t.Run("no parser preserves error", func(t *testing.T) {
		m := New(uri, newTestScheme(), nil, nil, nil, nil, Config{
			NoInitializeServer: true,
		})
		defer m.Close() // nolint:errcheck
		_, derr := m.DocumentSymbol(
			context.Background(), docSymParams(),
		)
		require.Error(t, derr)
		assert.True(t, errors.Is(derr, ErrLanguageNotSupported))
	})

	t.Run("fallback failure preserves original error", func(t *testing.T) {
		p := &fallbackParser{queryErr: errors.New("not installed")}
		m := New(uri, newTestScheme(), nil, nil, nil, nil, Config{
			NoInitializeServer: true, Parser: p,
		})
		defer m.Close() // nolint:errcheck
		_, derr := m.DocumentSymbol(
			context.Background(), docSymParams(),
		)
		require.Error(t, derr)
		assert.True(t, errors.Is(derr, ErrLanguageNotSupported))
	})
}

func TestManagerWorkspaceSymbolFallback(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///ws")
	require.NoError(t, err)
	newParser := func() *fallbackParser {
		return &fallbackParser{
			names: []string{"mypkg.Frobnicate"},
			resolves: map[string][]syntaxapi.Match{
				"mypkg.Frobnicate": {{
					URI: "file:///ws/frob.zig",
					Pos: term.Coordinates{Y: 3, X: 0},
				}},
			},
		}
	}

	t.Run("indexed serves fuzzy results", func(t *testing.T) {
		m := New(uri, newTestScheme(), nil, nil, nil, nil, Config{
			NoInitializeServer: true, Parser: newParser(),
			IndexedSymbols: true,
		})
		defer m.Close() // nolint:errcheck
		syms, serr := m.WorkspaceSymbol(
			context.Background(),
			semanticapi.WorkspaceSymbolParams{Query: "frob"},
		)
		require.NoError(t, serr)
		require.Len(t, syms, 1)
		assert.Equal(t, "mypkg.Frobnicate", syms[0].Name)
	})

	t.Run("not indexed keeps empty result", func(t *testing.T) {
		m := New(uri, newTestScheme(), nil, nil, nil, nil, Config{
			NoInitializeServer: true, Parser: newParser(),
		})
		defer m.Close() // nolint:errcheck
		syms, serr := m.WorkspaceSymbol(
			context.Background(),
			semanticapi.WorkspaceSymbolParams{Query: "frob"},
		)
		require.NoError(t, serr)
		assert.Empty(t, syms)
	})
}

// The integration suite runs the fallback over real tree-sitter
// grammars — the checked-in fixtures under ide/syntax/syntaxtest — so
// capture vocabularies, scope shapes and coordinate conventions are
// exercised as shipped rather than as hand-written captures.

// fixturePkgManager serves those fixtures the way an installed
// language package would.
type fixturePkgManager struct{ root string }

func (m fixturePkgManager) LibDir(
	_ context.Context, langID string,
) (iterator.Iterator[string], error) {
	dir := filepath.Join(m.root, langID)
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("package %s not installed", langID)
	}
	return iterator.FromSlice([]string{
		grammarfixture.Parser(dir),
		filepath.Join(dir, "locals.scm"),
		filepath.Join(dir, "highlights.scm"),
		filepath.Join(dir, "indents.scm"),
		filepath.Join(dir, "folds.scm"),
	}), nil
}

// integrationFiles is a multi-language workspace: a two-package Go
// module, a python package, a rust crate, plus the degenerate files
// (empty, comment-only, unparseable, binary, no grammar) that the
// fallback has to survive.
var integrationFiles = map[string]string{
	"go.mod": "module example.com/fixture\n\ngo 1.22\n",
	"greet/greet.go": `package greet

type Greeter struct {
	Prefix string
}

func New(prefix string) *Greeter {
	return &Greeter{Prefix: prefix}
}

func (g *Greeter) Greet(name string) string {
	msg := g.Prefix + " " + name
	return msg
}
`,
	"main.go": `package main

import "example.com/fixture/greet"

const banner = "hi"

func main() {
	g := greet.New(banner)
	shout := func(s string) string {
		out := s + "!"
		return out
	}
	println(shout(g.Greet("world")))
}
`,
	"shadow.go": `package shadow

func f(x int) int {
	y := x
	{
		y := x * 2
		_ = y
	}
	return y
}
`,
	"unicode.go": `package unicode

func größe(wert int) int {
	σ := wert * 2
	return σ
}
`,
	"empty.go":    "",
	"comments.go": "// only comments here\n// and nothing else\n",
	"broken.go":   "package broken\n\nfunc (((\n\t??? ~~~ }}}\n",
	"binary.go":   "package binary\n\x00\x01\x02\xff\xfe garbage \x00\n",
	"py/mod.py": `import os


CONST = 1


class Widget:
    def __init__(self, name):
        self.name = name

    def render(self, indent=0):
        pad = " " * indent
        return pad + self.name


def build(count):
    items = [Widget(str(i)) for i in range(count)]
    return items
`,
	"py/empty.py": "",
	"rs/lib.rs": `pub struct Point {
    pub x: i32,
}

impl Point {
    pub fn new(x: i32) -> Point {
        let p = Point { x };
        p
    }
}

pub fn origin() -> Point {
    Point::new(0)
}
`,
	"conf.yaml": "root:\n  key: value\n",
	"notes.txt": "no grammar for this one\n",
}

// integrationWorkspace materializes integrationFiles on disk and
// returns a parser bound to it plus the workspace root.
func integrationWorkspace(t *testing.T) (syntaxapi.Parser, string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("tree-sitter grammar fixtures are darwin-only")
	}
	wd, err := os.Getwd()
	require.NoError(t, err)
	fixtures := filepath.Join(wd, "..", "syntax", "syntaxtest")
	require.FileExists(t, filepath.Join(fixtures, "go", "tree-sitter.so"))

	root := t.TempDir()
	for name, content := range integrationFiles {
		p := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	}

	uri, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	return treesitter.NewParser(scheme, fixturePkgManager{root: fixtures}, uri), root
}

func integrationFallback(t *testing.T) (*syntaxFallback, string) {
	t.Helper()
	parser, root := integrationWorkspace(t)
	fb := newSyntaxFallback(parser, func(path string) (string, error) {
		data, err := os.ReadFile(path) // nolint:gosec // test fixture path
		return string(data), err
	}, false)
	return fb, root
}

func fileURI(root, name string) string {
	return "file://" + filepath.Join(root, name)
}

func TestFallbackIntegrationDocumentSymbol(t *testing.T) {
	fb, root := integrationFallback(t)

	tests := []struct {
		name string
		file string
		want []string
	}{
		{
			name: "go package with type, function and method",
			file: "greet/greet.go",
			want: []string{
				"greet:3:0",
				"Greeter:5:2",
				"New:12:6",
				"New.prefix:13:6",
				"Greet:6:10",
				"Greet.g:13:10",
				"Greet.name:13:10",
				"Greet.msg:13:11",
			},
		},
		{
			name: "go closures nest under their enclosing function",
			file: "main.go",
			want: []string{
				"main:3:0",
				"banner:13:4",
				"main:12:6",
				"main.g:13:7",
				"main.shout:13:8",
				"main.s:13:8",
				"main.out:13:9",
			},
		},
		{
			name: "shadowed declarations are listed separately",
			file: "shadow.go",
			want: []string{
				"shadow:3:0", "f:12:2", "f.x:13:2", "f.y:13:3", "f.y:13:5",
			},
		},
		{
			name: "multibyte identifiers survive",
			file: "unicode.go",
			want: []string{
				"unicode:3:0", "größe:12:2", "größe.wert:13:2", "größe.σ:13:3",
			},
		},
		{
			name: "python methods nest under their class exactly once",
			file: "py/mod.py",
			want: []string{
				"CONST:13:3",
				"Widget:5:6",
				"Widget.__init__:6:7",
				"Widget.render:6:10",
				"render.pad:13:11",
				"build:12:15",
				"build.items:13:16",
				"build.i:13:16",
			},
		},
		{
			name: "rust impl blocks nest their functions",
			file: "rs/lib.rs",
			want: []string{
				"Point:5:0", "new:12:5", "new.x:13:5", "new.p:13:6",
				"origin:12:11",
			},
		},
		{
			name: "empty file yields no symbols",
			file: "empty.go",
			want: nil,
		},
		{
			name: "comment-only file yields no symbols",
			file: "comments.go",
			want: nil,
		},
		{
			name: "unparseable source yields what still parses",
			file: "broken.go",
			want: []string{"broken:3:0"},
		},
		{
			name: "binary garbage yields what still parses",
			file: "binary.go",
			want: []string{"binary:3:0"},
		},
		{
			name: "grammar without definition captures yields no symbols",
			file: "conf.yaml",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := fb.documentSymbol(
				context.Background(), semanticapi.DocumentSymbolParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: fileURI(root, tt.file),
					},
				})
			require.NoError(t, err)
			assert.Equal(t, tt.want, flattenOutline(res.DocumentSymbols, ""))
		})
	}
}

func TestFallbackIntegrationDefinition(t *testing.T) {
	fb, root := integrationFallback(t)

	tests := []struct {
		name       string
		file       string
		line, char int
		wantFile   string
		wantLine   uint32
		wantChar   uint32
		wantEmpty  bool
		wantMulti  bool
	}{
		{
			name: "file-level const from its use",
			file: "main.go", line: 7, char: 16,
			wantFile: "main.go", wantLine: 4, wantChar: 6,
		},
		{
			name: "local declared earlier in the same block",
			file: "main.go", line: 12, char: 15,
			wantFile: "main.go", wantLine: 7, wantChar: 1,
		},
		{
			name: "local inside a closure body",
			file: "main.go", line: 10, char: 9,
			wantFile: "main.go", wantLine: 9, wantChar: 2,
		},
		{
			name: "closure variable from the call site",
			file: "main.go", line: 12, char: 10,
			wantFile: "main.go", wantLine: 8, wantChar: 1,
		},
		{
			name: "closure parameter from the body",
			file: "main.go", line: 9, char: 9,
			wantFile: "main.go", wantLine: 8, wantChar: 15,
		},
		{
			name: "cursor on a definition resolves to itself",
			file: "shadow.go", line: 2, char: 5,
			wantFile: "shadow.go", wantLine: 2, wantChar: 5,
		},
		{
			name: "inner shadow wins inside its scope",
			file: "shadow.go", line: 6, char: 7,
			wantFile: "shadow.go", wantLine: 5, wantChar: 2,
		},
		{
			name: "outer declaration wins outside the shadowing scope",
			file: "shadow.go", line: 8, char: 8,
			wantFile: "shadow.go", wantLine: 3, wantChar: 1,
		},
		{
			name: "parameter from its use",
			file: "shadow.go", line: 3, char: 6,
			wantFile: "shadow.go", wantLine: 2, wantChar: 7,
		},
		{
			name: "python local from a later expression",
			file: "py/mod.py", line: 12, char: 15,
			wantFile: "mod.py", wantLine: 11, wantChar: 8,
		},
		{
			name: "rust type from an associated call",
			file: "rs/lib.rs", line: 12, char: 4,
			wantFile: "lib.rs", wantLine: 0, wantChar: 11,
		},
		{
			name: "qualified cross-package name resolves through the index",
			file: "main.go", line: 7, char: 12,
			wantMulti: true,
		},
		{
			name: "cursor on whitespace resolves to nothing",
			file: "main.go", line: 5, char: 0,
			wantEmpty: true,
		},
		{
			name: "cursor past the end of the file resolves to nothing",
			file: "main.go", line: 99, char: 0,
			wantEmpty: true,
		},
		{
			name: "empty file resolves to nothing",
			file: "empty.go", line: 0, char: 0,
			wantEmpty: true,
		},
		{
			name: "unparseable source resolves to nothing",
			file: "broken.go", line: 3, char: 3,
			wantEmpty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := fb.definition(
				context.Background(), semanticapi.DefinitionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: fileURI(root, tt.file),
					},
					Position: semanticapi.Position{
						Line: uint32(tt.line), Character: uint32(tt.char),
					},
				})
			require.NoError(t, err)
			switch {
			case tt.wantEmpty:
				assert.Nil(t, res.Location)
				assert.Empty(t, res.Locations)
			case tt.wantMulti:
				require.NotEmpty(t, res.Locations)
			default:
				require.NotNil(t, res.Location)
				assert.Equal(t, tt.wantFile, filepath.Base(res.Location.URI))
				assert.Equal(t, semanticapi.Position{
					Line: tt.wantLine, Character: tt.wantChar,
				}, res.Location.Range.Start)
			}
		})
	}
}

func TestFallbackIntegrationReferences(t *testing.T) {
	fb, root := integrationFallback(t)

	tests := []struct {
		name       string
		file       string
		line, char int
		decl       bool
		want       []string
	}{
		{
			name: "closure local with declaration",
			file: "main.go", line: 9, char: 2, decl: true,
			want: []string{"main.go:9:2", "main.go:10:9"},
		},
		{
			name: "file-level const with declaration",
			file: "main.go", line: 4, char: 6, decl: true,
			want: []string{"main.go:4:6", "main.go:7:16"},
		},
		{
			name: "file-level const without declaration",
			file: "main.go", line: 4, char: 6, decl: false,
			want: []string{"main.go:7:16"},
		},
		{
			name: "outer declaration excludes shadowed uses",
			file: "shadow.go", line: 3, char: 1, decl: true,
			want: []string{"shadow.go:3:1", "shadow.go:8:8"},
		},
		{
			name: "inner declaration only covers its own scope",
			file: "shadow.go", line: 5, char: 2, decl: true,
			want: []string{"shadow.go:5:2", "shadow.go:6:6"},
		},
		{
			name: "empty file has no references",
			file: "empty.go", line: 0, char: 0, decl: true,
			want: nil,
		},
		{
			name: "unparseable source has no references",
			file: "broken.go", line: 3, char: 3, decl: true,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			locs, err := fb.references(
				context.Background(), semanticapi.ReferenceParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: fileURI(root, tt.file),
					},
					Position: semanticapi.Position{
						Line: uint32(tt.line), Character: uint32(tt.char),
					},
					Context: semanticapi.ReferenceContext{
						IncludeDeclaration: tt.decl,
					},
				})
			require.NoError(t, err)
			var got []string
			for _, l := range locs {
				got = append(got, fmt.Sprintf("%s:%d:%d",
					filepath.Base(l.URI),
					l.Range.Start.Line, l.Range.Start.Character))
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFallbackIntegrationNoGrammar(t *testing.T) {
	fb, root := integrationFallback(t)

	tests := []struct {
		name string
		file string
	}{
		{name: "language without a grammar package", file: "notes.txt"},
		{name: "file that does not exist", file: "missing.go"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			uri := fileURI(root, tt.file)
			_, err := fb.documentSymbol(
				context.Background(), semanticapi.DocumentSymbolParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: uri},
				})
			require.Error(t, err)

			_, err = fb.definition(
				context.Background(), semanticapi.DefinitionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: uri},
				})
			require.Error(t, err)

			_, err = fb.references(
				context.Background(), semanticapi.ReferenceParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: uri},
				})
			require.Error(t, err)
		})
	}
}

func TestFallbackIntegrationUnindexedWorkspaceSymbol(t *testing.T) {
	fb, _ := integrationFallback(t)
	syms, err := fb.workspaceSymbol(
		context.Background(),
		semanticapi.WorkspaceSymbolParams{Query: "Greet"},
	)
	require.NoError(t, err)
	assert.Empty(t, syms)
}

func TestManagerIntegrationDocumentSymbol(t *testing.T) {
	parser, root := integrationWorkspace(t)
	uri, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)
	m := New(uri, newTestScheme(), nil, nil, nil, nil, Config{
		NoInitializeServer: true, Parser: parser,
	})
	defer m.Close() // nolint:errcheck

	t.Run("unsupported language is served by the fallback", func(t *testing.T) {
		res, derr := m.DocumentSymbol(
			context.Background(), semanticapi.DocumentSymbolParams{
				TextDocument: semanticapi.TextDocumentIdentifier{
					URI: fileURI(root, "conf.yaml"),
				},
			})
		require.NoError(t, derr)
		assert.Empty(t, res.DocumentSymbols)
	})

	t.Run("missing grammar preserves the original error", func(t *testing.T) {
		_, derr := m.DocumentSymbol(
			context.Background(), semanticapi.DocumentSymbolParams{
				TextDocument: semanticapi.TextDocumentIdentifier{
					URI: fileURI(root, "notes.txt"),
				},
			})
		require.Error(t, derr)
		assert.ErrorIs(t, derr, ErrLanguageNotSupported)
	})
}
