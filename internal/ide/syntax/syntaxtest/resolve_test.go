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
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/workspace"
)

// langPkgManager returns the tree-sitter fixtures for the requested
// language only, so a multi-language workspace loads the correct parser
// per file extension.
type langPkgManager struct {
	wd string
}

func (m langPkgManager) LibDir(_ context.Context, langID string) (iterator.Iterator[string], error) {
	dir := filepath.Join(m.wd, langID)
	if _, err := os.Stat(dir); err != nil {
		return iterator.FromSlice([]string(nil)), nil
	}
	return iterator.FromSlice([]string{
		grammarfixture.Parser(dir),
		filepath.Join(dir, "locals.scm"),
		filepath.Join(dir, "highlights.scm"),
		filepath.Join(dir, "indents.scm"),
		filepath.Join(dir, "folds.scm"),
	}), nil
}

const goGeometryDef = `package geometry

func Area() int {
	return 0
}

type Point struct {
	X int
	Y int
}

func (p Point) Norm() int {
	return p.X*p.X + p.Y*p.Y
}
`

const goGeometryUse = `package main

import "example/geometry"

func main() {
	_ = geometry.Area()
}
`

const pyShapesDef = `def perimeter():
    return 0
`

const pyShapesUse = `import shapes

shapes.perimeter()
`

func newResolveParser(t *testing.T, files map[string]string) syntaxapi.Parser {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	for name, content := range files {
		createFile(t, scheme, name, content)
	}
	return syntax.NewParser(scheme, langPkgManager{wd: wd}, uri)
}

// recordingPkgManager wraps a PkgManager and records every language for
// which a grammar (LibDir) was requested, so tests can assert which
// languages the workspace walk attempted to load parsers for.
type recordingPkgManager struct {
	inner syntax.PkgManager
	mu    sync.Mutex
	seen  map[string]bool
}

func (m *recordingPkgManager) LibDir(
	ctx context.Context, langID string,
) (iterator.Iterator[string], error) {
	m.mu.Lock()
	if m.seen == nil {
		m.seen = make(map[string]bool)
	}
	m.seen[langID] = true
	m.mu.Unlock()
	return m.inner.LibDir(ctx, langID)
}

func (m *recordingPkgManager) requested(langID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen[langID]
}

func newRecordingResolveParser(
	t *testing.T, files map[string]string,
) (syntaxapi.Parser, *recordingPkgManager) {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	for name, content := range files {
		createFile(t, scheme, name, content)
	}
	pkg := &recordingPkgManager{inner: langPkgManager{wd: wd}}
	return syntax.NewParser(scheme, pkg, uri), pkg
}

func resolveAll(t *testing.T, parser syntaxapi.Parser, name string) ([]syntaxapi.Match, error) {
	t.Helper()
	it, err := parser.ResolveSymbol(context.Background(), name, nil)
	require.NoError(t, err)
	return iterator.ToSlice(context.Background(), it)
}

type recordingProgress struct {
	mu     sync.Mutex
	events []progressEvent
}

type progressEvent struct {
	step, total int64
}

func (r *recordingProgress) Report(_ string, _ int, step, total int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, progressEvent{step: step, total: total})
}

func TestParserResolveSymbolProgressMonotonic(t *testing.T) {
	parser := newResolveParser(t, map[string]string{
		"geometry/area.go": goGeometryDef,
		"main.go":          goGeometryUse,
		"shapes.py":        pyShapesDef,
		"app.py":           pyShapesUse,
	})

	rec := &recordingProgress{}
	it, err := parser.ResolveSymbol(
		context.Background(), "missingpkg.DoesNotExistAnywhere", rec,
	)
	require.NoError(t, err)
	_, err = iterator.ToSlice(context.Background(), it)
	require.Error(t, err)

	rec.mu.Lock()
	events := append([]progressEvent(nil), rec.events...)
	rec.mu.Unlock()
	require.GreaterOrEqual(t, len(events), 2,
		"multiple specs should each report progress")

	var prevStep, prevTotal int64
	for i, e := range events {
		assert.GreaterOrEqualf(t, e.step, prevStep, "step non-decreasing at %d: %+v", i, events)
		assert.LessOrEqualf(t, e.step, e.total, "step <= total at %d: %+v", i, events)
		assert.GreaterOrEqualf(t, e.total, prevTotal, "total non-decreasing at %d: %+v", i, events)
		prevStep, prevTotal = e.step, e.total
	}
}

func TestParserResolveSymbolLanguageDetection(t *testing.T) {
	t.Run("go only resolves go and skips python", func(t *testing.T) {
		parser := newResolveParser(t, map[string]string{
			"geometry/area.go": goGeometryDef,
			"main.go":          goGeometryUse,
		})

		matches, err := resolveAll(t, parser, "geometry.Area")
		require.NoError(t, err)
		assert.NotEmpty(t, matches, "Go symbol should resolve in a Go-only workspace")

		_, err = resolveAll(t, parser, "shapes.perimeter")
		require.Error(t, err, "Python symbol must not resolve in a Go-only workspace")
	})

	t.Run("python only resolves python and skips go", func(t *testing.T) {
		parser := newResolveParser(t, map[string]string{
			"shapes.py": pyShapesDef,
			"main.py":   pyShapesUse,
		})

		matches, err := resolveAll(t, parser, "shapes.perimeter")
		require.NoError(t, err)
		assert.NotEmpty(t, matches, "Python symbol should resolve in a Python-only workspace")

		_, err = resolveAll(t, parser, "geometry.Area")
		require.Error(t, err, "Go symbol must not resolve in a Python-only workspace")
	})

	t.Run("mixed workspace resolves both languages", func(t *testing.T) {
		parser := newResolveParser(t, map[string]string{
			"geometry/area.go": goGeometryDef,
			"main.go":          goGeometryUse,
			"shapes.py":        pyShapesDef,
			"app.py":           pyShapesUse,
		})

		goMatches, err := resolveAll(t, parser, "geometry.Area")
		require.NoError(t, err)
		assert.NotEmpty(t, goMatches)

		pyMatches, err := resolveAll(t, parser, "shapes.perimeter")
		require.NoError(t, err)
		assert.NotEmpty(t, pyMatches)
	})

	t.Run("name without dot returns ErrNoDot", func(t *testing.T) {
		parser := newResolveParser(t, map[string]string{"main.go": goGeometryUse})
		_, err := parser.ResolveSymbol(context.Background(), "NoDot", nil)
		assert.ErrorIs(t, err, syntaxapi.ErrNoDot)
	})

	t.Run("absent language yields not found", func(t *testing.T) {
		parser := newResolveParser(t, map[string]string{"main.go": goGeometryUse})
		_, err := resolveAll(t, parser, "shapes.perimeter")
		require.Error(t, err)
		assert.False(t, errors.Is(err, syntaxapi.ErrNoDot))
	})
}

func TestParserResolveSymbolScopesDefinitionWalk(t *testing.T) {
	parser, pkg := newRecordingResolveParser(t, map[string]string{
		"shapes.py":   pyShapesDef,
		"app.py":      pyShapesUse,
		"config.yaml": "name: value\n",
	})

	// A method on a Python module type resolves to nothing via references,
	// forcing the Python spec's definitions phase to run.
	matches, err := resolveAll(t, parser, "shapes.Shape.perimeter")
	require.Error(t, err, "unresolvable method must not resolve")
	assert.Empty(t, matches)

	assert.True(t, pkg.requested("python"),
		"resolving a python symbol must load the python grammar")
	assert.False(t, pkg.requested("yaml"),
		"definitions phase must not load grammars for unrelated languages")
}

func TestParserResolveSymbolMethod(t *testing.T) {
	parser, pkg := newRecordingResolveParser(t, map[string]string{
		"geometry/area.go": goGeometryDef,
		"main.go":          goGeometryUse,
		"config.yaml":      "name: value\n",
	})

	matches, err := resolveAll(t, parser, "geometry.Point.Norm")
	require.NoError(t, err)
	require.Len(t, matches, 1)

	m := matches[0]
	assert.Contains(t, m.URI, "geometry/area.go")
	// goGeometryDef declares Norm on line 12 (1-based); the capture
	// lands on the method name, which begins after "func (p Point) ".
	assert.Equal(t, 11, m.Pos.Y, "method name should be on the Norm line")
	assert.Equal(t, len("func (p Point) "), m.Pos.X)

	assert.True(t, pkg.requested("go"),
		"resolving a go method must load the go grammar")
	assert.False(t, pkg.requested("yaml"),
		"method definitions phase must not load grammars for unrelated languages")
}

func listReferencedAll(t *testing.T, parser syntaxapi.Parser) map[string]bool {
	t.Helper()
	it, err := parser.ListReferencedSymbols(context.Background())
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.NoError(t, it.Err())
	got := make(map[string]bool, len(names))
	for _, n := range names {
		got[n] = true
	}
	return got
}

func TestParserListReferencedSymbolsLanguageDetection(t *testing.T) {
	t.Run("go only lists go refs and not python", func(t *testing.T) {
		parser := newResolveParser(t, map[string]string{
			"geometry/area.go": goGeometryDef,
			"main.go":          goGeometryUse,
		})

		got := listReferencedAll(t, parser)
		assert.True(t, got["geometry.Area"], "Go reference should be listed")
		assert.True(t, got["geometry.Point.Norm"],
			"Go method definition should be listed")
		assert.False(t, got["shapes.perimeter"], "Python ref must not appear in a Go-only workspace")
	})

	t.Run("python only lists python refs and not go", func(t *testing.T) {
		parser := newResolveParser(t, map[string]string{
			"shapes.py": pyShapesDef,
			"main.py":   pyShapesUse,
		})

		got := listReferencedAll(t, parser)
		assert.True(t, got["shapes.perimeter"], "Python reference should be listed")
		assert.False(t, got["geometry.Area"], "Go ref must not appear in a Python-only workspace")
	})

	t.Run("mixed workspace lists both languages", func(t *testing.T) {
		parser := newResolveParser(t, map[string]string{
			"geometry/area.go": goGeometryDef,
			"main.go":          goGeometryUse,
			"shapes.py":        pyShapesDef,
			"app.py":           pyShapesUse,
		})

		got := listReferencedAll(t, parser)
		assert.True(t, got["geometry.Area"], "Go reference should be listed")
		assert.True(t, got["geometry.Point.Norm"],
			"Go method definition should be listed")
		assert.True(t, got["shapes.perimeter"], "Python reference should be listed")
	})
}
