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

package symbolresolve_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"

	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
)

func TestSpecForFile(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		path string
		want *symbolresolve.Spec
	}{
		{"a.go", symbolresolve.Go},
		{"pkg/deep/a.go", symbolresolve.Go},
		{"a.py", symbolresolve.Python},
		{"a.pyi", symbolresolve.Python},
		{"a.rs", symbolresolve.Rust},
		{"a.zig", symbolresolve.Zig},
		{"a.ts", symbolresolve.TypeScript},
		{"a.mts", symbolresolve.TypeScript},
		{"a.cts", symbolresolve.TypeScript},
		{"a.d.ts", symbolresolve.TypeScript},
		{"a.tsx", symbolresolve.TSX},
		{"a.js", symbolresolve.JavaScript},
		{"a.mjs", symbolresolve.JavaScript},
		{"a.cjs", symbolresolve.JavaScript},
		{"a.jsx", symbolresolve.JavaScript},
		{"a.txt", nil},
		{"go", nil},
	} {
		assert.Equal(t, tc.want, symbolresolve.SpecForFile(tc.path), tc.path)
	}
}

// occurrence keys one extracted symbol by qualified name and kind.
type occurrence struct {
	name string
	kind symbolresolve.SymbolKind
}

func extractFileT(
	t *testing.T, env *resolveEnv, spec *symbolresolve.Spec, rel string,
) (symbolresolve.FileExtraction, map[occurrence]int) {
	t.Helper()
	uri, err := workspaceapi.ParseURI(env.fileURI(rel))
	require.NoError(t, err)
	q := env.parser.NewQuerySession()
	defer func() { require.NoError(t, q.Close()) }()
	ext, err := symbolresolve.ExtractFile(
		context.Background(), q, spec, env.qc, uri)
	require.NoError(t, err)
	occs := make(map[occurrence]int, len(ext.Symbols))
	for _, s := range ext.Symbols {
		occs[occurrence{s.Name, s.Kind}]++
	}
	return ext, occs
}

func TestExtractFileGo(t *testing.T) {
	t.Parallel()

	env := setupResolveEnv(t)

	t.Run("refs and imports from main.go", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.Go, "main.go")

		for _, want := range []occurrence{
			{"mylib.MyType", symbolresolve.SymbolRef},
			{"iter.Iterator", symbolresolve.SymbolRef},
			{"nested.Repeated", symbolresolve.SymbolRef},
			{"iter.Reduce", symbolresolve.SymbolRef},
			{"mylib.MyFunc", symbolresolve.SymbolRef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
		// m.String(): the selector query requires the operand to be an
		// imported alias, so local variables never produce references.
		assert.Zero(t, occs[occurrence{"m.String", symbolresolve.SymbolRef}])
		// main and useTypes are unexported, so main.go has no defs.
		for o := range occs {
			assert.Equalf(t, symbolresolve.SymbolRef, o.kind,
				"unexpected non-ref %+v", o)
		}

		assert.Equal(t, map[string]string{
			"context": "context",
			"fmt":     "fmt",
			"iter":    "example.com/resolvetest/iter",
			"mylib":   "example.com/resolvetest/mylib",
			"nested":  "example.com/resolvetest/nested/nested",
		}, ext.Imports)
	})

	t.Run("defs and methods from mylib.go", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.Go, "mylib/mylib.go")

		assert.Equal(t, map[occurrence]int{
			{"mylib.MyType", symbolresolve.SymbolDef}:              1,
			{"mylib.MyFunc", symbolresolve.SymbolDef}:              1,
			{"mylib.New", symbolresolve.SymbolDef}:                 1,
			{"mylib.MyType.String", symbolresolve.SymbolMethodDef}: 1,
			{"mylib.MyType.Set", symbolresolve.SymbolMethodDef}:    1,
		}, occs, "unexportedHelper must be excluded")
		assert.Empty(t, ext.Imports)
	})

	t.Run("blank import contributes no symbols", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.Go, "blankimport.go")
		assert.Empty(t, occs)
		assert.NotContains(t, ext.Imports, "_")
	})

	t.Run("dot-imported references are invisible", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Go, "dotimport.go")
		assert.Zero(t, occs[occurrence{"mylib.MyFunc", symbolresolve.SymbolRef}])
		assert.NotZero(t, occs[occurrence{"main.DotUser", symbolresolve.SymbolDef}])
	})
}

func TestExtractFilePython(t *testing.T) {
	t.Parallel()

	env := setupPythonEnv(t)

	t.Run("attribute refs from main.py", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.Python, "main.py")

		for _, want := range []occurrence{
			{"geometry.area", symbolresolve.SymbolRef},
			{"geometry.Shape", symbolresolve.SymbolRef},
			{"requests.get", symbolresolve.SymbolRef},
			{"service.Service", symbolresolve.SymbolRef},
			{"text.slugify", symbolresolve.SymbolRef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
		// The module qualifier is the file stem.
		assert.NotZero(t, occs[occurrence{"main.main", symbolresolve.SymbolDef}])
		// Python has no import queries.
		assert.Empty(t, ext.Imports)
	})

	t.Run("defs and methods from geometry.py", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Python, "geometry.py")

		// Python has no visibility filter: underscore names are kept.
		for _, want := range []occurrence{
			{"geometry.area", symbolresolve.SymbolDef},
			{"geometry.perimeter", symbolresolve.SymbolDef},
			{"geometry.Shape", symbolresolve.SymbolDef},
			{"geometry._Internal", symbolresolve.SymbolDef},
			{"geometry._make_internal", symbolresolve.SymbolDef},
			{"geometry.Shape.__init__", symbolresolve.SymbolMethodDef},
			{"geometry.Shape.area", symbolresolve.SymbolMethodDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
	})
}

func TestExtractFilePythonPackage(t *testing.T) {
	t.Parallel()

	env := setupPythonEnvAt(t, "testdata_py_pkg")

	t.Run("defs are emitted under every module path suffix", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Python, "src/mypkg/_impl.py")

		for _, want := range []occurrence{
			{"mypkg._impl.Widget", symbolresolve.SymbolDef},
			{"_impl.Widget", symbolresolve.SymbolDef},
			{"mypkg._impl.make_widget", symbolresolve.SymbolDef},
			{"_impl.make_widget", symbolresolve.SymbolDef},
			{"mypkg._impl.Widget.render", symbolresolve.SymbolMethodDef},
			{"_impl.Widget.render", symbolresolve.SymbolMethodDef},
			// The module-less Type.method suffix is also indexed so a
			// bare "Widget.render" lookup resolves.
			{"Widget.render", symbolresolve.SymbolMethodDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
		// The bare package name is not a suffix of mypkg._impl.
		assert.Zero(t, occs[occurrence{"mypkg.Widget", symbolresolve.SymbolDef}])
	})

	t.Run("package __init__ re-exports are defs", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Python, "src/mypkg/__init__.py")

		for _, want := range []occurrence{
			{"mypkg.Widget", symbolresolve.SymbolDef},
			{"mypkg.make_widget", symbolresolve.SymbolDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
	})

	t.Run("nested package __init__ re-exports carry suffixes", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Python, "src/mypkg/sub/__init__.py")

		for _, want := range []occurrence{
			{"mypkg.sub.slug", symbolresolve.SymbolDef},
			{"sub.slug", symbolresolve.SymbolDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
	})

	t.Run("imports outside re-export files are not defs", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Python, "main.py")

		assert.Zero(t, occs[occurrence{"main.make_widget", symbolresolve.SymbolDef}],
			"a plain module's imports must not become definitions")
		assert.NotZero(t, occs[occurrence{"main.main", symbolresolve.SymbolDef}])
	})
}

func TestExtractFileRust(t *testing.T) {
	t.Parallel()

	env := setupRustEnv(t)

	t.Run("scoped refs from main.rs", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Rust, "main.rs")

		for _, want := range []occurrence{
			{"geometry.area", symbolresolve.SymbolRef},
			{"geometry.Shape", symbolresolve.SymbolRef},
			{"requests.get", symbolresolve.SymbolRef},
			{"service.Service", symbolresolve.SymbolRef},
			{"text.slugify", symbolresolve.SymbolRef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
	})

	t.Run("defs and methods from geometry.rs", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Rust, "geometry.rs")

		// Rust cannot observe pub visibility from tree-sitter captures,
		// so private items are kept. The module qualifier is the file
		// stem.
		for _, want := range []occurrence{
			{"geometry.area", symbolresolve.SymbolDef},
			{"geometry.perimeter", symbolresolve.SymbolDef},
			{"geometry.Shape", symbolresolve.SymbolDef},
			{"geometry.Internal", symbolresolve.SymbolDef},
			{"geometry.Shape.new", symbolresolve.SymbolMethodDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
	})
}

func TestExtractFileZig(t *testing.T) {
	t.Parallel()

	env := setupZigEnv(t)

	t.Run("field refs and imports from main.zig", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.Zig, "main.zig")

		// The @import bindings resolve aliases to their import paths.
		assert.Equal(t, map[string]string{
			"geometry": "geometry.zig",
			"requests": "requests.zig",
			"service":  "app/service.zig",
			"text":     "utils/text.zig",
		}, ext.Imports)

		for _, want := range []occurrence{
			{"geometry.area", symbolresolve.SymbolRef},
			{"geometry.Shape", symbolresolve.SymbolRef},
			{"requests.get", symbolresolve.SymbolRef},
			{"service.Service", symbolresolve.SymbolRef},
			{"text.slugify", symbolresolve.SymbolRef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
	})

	t.Run("defs and methods from geometry.zig", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Zig, "geometry.zig")

		// Zig cannot observe pub visibility from tree-sitter captures,
		// so private items are kept. The module qualifier is the file
		// stem.
		for _, want := range []occurrence{
			{"geometry.area", symbolresolve.SymbolDef},
			{"geometry.perimeter", symbolresolve.SymbolDef},
			{"geometry.Shape", symbolresolve.SymbolDef},
			{"geometry.Internal", symbolresolve.SymbolDef},
			{"geometry.Shape.init", symbolresolve.SymbolMethodDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
	})

	t.Run("member access on locals is dropped without import", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Zig, "geometry.zig")

		// geometry.zig imports nothing, so its field expressions (e.g.
		// struct-literal member reads) never pass RequireImport.
		for o := range occs {
			assert.NotEqualf(t, symbolresolve.SymbolRef, o.kind,
				"unexpected reference %+v", o)
		}
	})
}

func TestExtractFileTypeScript(t *testing.T) {
	t.Parallel()

	env := setupTSEnv(t)

	t.Run("member refs and imports from main.ts", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.TypeScript, "src/main.ts")

		// Named imports bind bare names, so ./utils is not an alias.
		assert.Equal(t, map[string]string{
			"geometry": "./geometry",
			"service":  "./app/service",
			"text":     "./utils/text",
			"requests": "./vendor/requests",
		}, ext.Imports)

		assert.Equal(t, map[occurrence]int{
			{"geometry.Shape", symbolresolve.SymbolRef}:  2,
			{"geometry.area", symbolresolve.SymbolRef}:   1,
			{"requests.get", symbolresolve.SymbolRef}:    1,
			{"service.Service", symbolresolve.SymbolRef}: 1,
			{"text.slugify", symbolresolve.SymbolRef}:    1,
			{"main.main", symbolresolve.SymbolDef}:       1,
		}, occs, "console.log must not pass RequireImport")
	})

	t.Run("defs and methods from geometry.ts", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.TypeScript, "src/geometry.ts")

		// Member reads on parameters and this are not module references.
		assert.Equal(t, map[occurrence]int{
			{"geometry.Sized", symbolresolve.SymbolDef}:                   1,
			{"geometry.Point", symbolresolve.SymbolDef}:                   1,
			{"geometry.Unit", symbolresolve.SymbolDef}:                    1,
			{"geometry.Shape", symbolresolve.SymbolDef}:                   1,
			{"geometry.Polygon", symbolresolve.SymbolDef}:                 1,
			{"geometry.area", symbolresolve.SymbolDef}:                    1,
			{"geometry.perimeter", symbolresolve.SymbolDef}:               1,
			{"geometry.Internal", symbolresolve.SymbolDef}:                1,
			{"geometry.makeInternal", symbolresolve.SymbolDef}:            1,
			{"geometry.Shape.constructor", symbolresolve.SymbolMethodDef}: 1,
			{"geometry.Shape.size", symbolresolve.SymbolMethodDef}:        1,
			{"geometry.Shape.scale", symbolresolve.SymbolMethodDef}:       1,
			{"geometry.Polygon.describe", symbolresolve.SymbolMethodDef}:  1,
		}, occs)
		assert.Empty(t, ext.Imports)
	})

	t.Run("index re-exports are defs of the directory", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.TypeScript, "src/utils/index.ts")

		assert.Equal(t, map[occurrence]int{
			{"utils.slugify", symbolresolve.SymbolDef}:    1,
			{"utils.loadConfig", symbolresolve.SymbolDef}: 1,
			{"utils.reset", symbolresolve.SymbolDef}:      1,
			{"utils.describe", symbolresolve.SymbolDef}:   1,
		}, occs)
	})

	t.Run("declaration file is named without its suffix", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.TypeScript, "src/types.d.ts")

		assert.Equal(t, map[occurrence]int{
			{"types.Settings", symbolresolve.SymbolDef}:  1,
			{"types.configure", symbolresolve.SymbolDef}: 1,
		}, occs)
	})

	t.Run("tsx refs, defs and methods from Badge.tsx", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.TSX, "src/components/Badge.tsx")

		assert.Equal(t, map[string]string{"geometry": "../geometry"}, ext.Imports)
		assert.Equal(t, map[occurrence]int{
			{"geometry.Shape", symbolresolve.SymbolRef}:           2,
			{"geometry.area", symbolresolve.SymbolRef}:            1,
			{"Badge.Badge", symbolresolve.SymbolDef}:              1,
			{"Badge.Panel", symbolresolve.SymbolDef}:              1,
			{"Badge.Panel.render", symbolresolve.SymbolMethodDef}: 1,
		}, occs)
	})

	t.Run("commonjs require binds an alias in loader.cjs", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.JavaScript, "src/legacy/loader.cjs")

		assert.Equal(t, map[string]string{"requests": "./requests.js"}, ext.Imports)
		assert.Equal(t, map[occurrence]int{
			{"requests.Response", symbolresolve.SymbolRef}: 1,
			{"loader.load", symbolresolve.SymbolDef}:       1,
		}, occs, "module.exports must not pass RequireImport")
	})
}
