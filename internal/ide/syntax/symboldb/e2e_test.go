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

package symboldb

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/localstorage/boltdoc"
	"unstable.build/rune/internal/workspace"
)

// e2eEnv wires an index-backed Parser over a real tree-sitter backing
// parser against one of the committed testdata modules.
type e2eEnv struct {
	root    string
	rootURI workspaceapi.URI
	backing syntaxapi.Parser
	p       *Parser
}

// stubPkgManager serves fixed tree-sitter artifact paths, matching the
// symbolresolve test harness.
type stubPkgManager struct{ files []string }

func (s *stubPkgManager) LibDir(
	context.Context, string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice(s.files), nil
}

// fixtureDir returns the directory holding the tree-sitter artifacts
// (tree-sitter.so and the .scm queries) for a language.
func fixtureDir(t testing.TB, rel string) string {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	dir := filepath.Join(wd, rel)
	for _, name := range []string{
		"locals.scm", "highlights.scm", "indents.scm", "folds.scm",
	} {
		_, err := os.Stat(filepath.Join(dir, name))
		require.NoErrorf(t, err, "missing tree-sitter fixture %s/%s", dir, name)
	}
	return dir
}

func pkgManagerFor(t testing.TB, fixtures string) syntax.PkgManager {
	t.Helper()
	dir := fixtureDir(t, fixtures)
	return &stubPkgManager{files: []string{
		grammarfixture.ParserPath(t, dir),
		filepath.Join(dir, "locals.scm"),
		filepath.Join(dir, "highlights.scm"),
		filepath.Join(dir, "indents.scm"),
		filepath.Join(dir, "folds.scm"),
	}}
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			require.NoError(t, os.MkdirAll(d, 0o755))
			copyDir(t, s, d)
			continue
		}
		data, err := os.ReadFile(s)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(d, data, 0o644))
	}
}

// setupE2E copies a testdata module into a temp workspace, builds a
// real backing parser plus an index-backed Parser over it, and waits
// for the initial scan.
func setupE2E(t *testing.T, testdata, fixtures string) *e2eEnv {
	t.Helper()
	root := t.TempDir()
	copyDir(t, testdata, root)

	rootURI, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), rootURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	backing := treesitter.NewParser(scheme, pkgManagerFor(t, fixtures), rootURI)
	p, err := New(backing, scheme, rootURI, storagestub.NewInMemoryService(),
		&recordingNotifications{}, syncTick)
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	require.NoError(t, p.Wait(context.Background()))

	return &e2eEnv{root: root, rootURI: rootURI, backing: backing, p: p}
}

func (e *e2eEnv) uri(t *testing.T, rel string) string {
	t.Helper()
	u, err := workspaceapi.ParseURI("file://" + filepath.Join(e.root, rel))
	require.NoError(t, err)
	return u.String()
}

func resolveURIs(t *testing.T, p syntaxapi.Parser, name string) []string {
	t.Helper()
	return matchField(t, p, name, func(m syntaxapi.Match) string { return m.URI })
}

func matchField(
	t *testing.T, p syntaxapi.Parser, name string,
	field func(syntaxapi.Match) string,
) []string {
	t.Helper()
	it, err := p.ResolveSymbol(context.Background(), name, nil)
	require.NoError(t, err)
	matches, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, field(m))
	}
	return out
}

func importPaths(t *testing.T, p syntaxapi.Parser, name string) []string {
	t.Helper()
	return matchField(t, p, name, func(m syntaxapi.Match) string { return m.ImportPath })
}

// assertResolveMatchesBacking checks that the index resolves a
// referenced symbol to the same number of matches and the same set of
// import paths as the backing parser. Exact URIs are not compared: when
// several files share an import path the resolver collapses them to one
// match whose surviving URI depends on tree-sitter traversal order.
func assertResolveMatchesBacking(t *testing.T, env *e2eEnv, name string) {
	t.Helper()
	wantURIs := resolveURIs(t, env.backing, name)
	gotURIs := resolveURIs(t, env.p, name)
	assert.Lenf(t, gotURIs, len(wantURIs),
		"match count for %q must equal backing", name)
	assert.ElementsMatchf(t,
		importPaths(t, env.backing, name), importPaths(t, env.p, name),
		"import paths for %q must equal backing", name)
}

func listSet(t *testing.T, p syntaxapi.Parser) map[string]bool {
	t.Helper()
	it, err := p.ListReferencedSymbols(context.Background())
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

func TestGoE2E(t *testing.T) {
	t.Parallel()
	env := setupE2E(t, goTestdata, goFixtures)

	t.Run("resolve matches the backing parser for referenced symbols", func(t *testing.T) {
		for _, name := range []string{
			"mylib.MyType", "mylib.MyFunc",
			"iter.Iterator", "nested.Repeated", "iterator.Iterator",
		} {
			assertResolveMatchesBacking(t, env, name)
		}
	})

	t.Run("definition-only symbols resolve from the index", func(t *testing.T) {
		for name, rel := range map[string]string{
			"iter.OnlyDefined": "iter/iter.go",
			"iter.Aggregate":   "iter/extra.go",
			"mylib.New":        "mylib/mylib.go",
			"iter.New":         "iter/extra.go",
		} {
			assert.Equalf(t, []string{env.uri(t, rel)}, resolveURIs(t, env.p, name),
				"definition resolution for %q", name)
		}
	})

	t.Run("methods resolve from method-definition locations", func(t *testing.T) {
		assert.Equal(t, []string{env.uri(t, "mylib/mylib.go")},
			resolveURIs(t, env.p, "mylib.MyType.String"))
		assert.Equal(t, []string{env.uri(t, "mylib/mylib.go")},
			resolveURIs(t, env.p, "mylib.MyType.Set"))
	})

	t.Run("unexported and constants are not indexed", func(t *testing.T) {
		for _, name := range []string{
			"iter.privateOnlyDefined", "mylib.unexportedHelper",
			"iter.MaxItems", "iter.DefaultIterator",
		} {
			// No index entry → passthrough to the backing parser,
			// which also finds nothing and reports not-found.
			it, err := env.p.ResolveSymbol(context.Background(), name, nil)
			if err != nil {
				assert.ErrorContainsf(t, err, "no symbols found",
					"symbol %q must not resolve", name)
				continue
			}
			matches, terr := iterator.ToSlice(context.Background(), it)
			if terr != nil {
				assert.ErrorContainsf(t, terr, "no symbols found",
					"symbol %q must not resolve", name)
				continue
			}
			assert.Emptyf(t, matches, "symbol %q must not resolve", name)
		}
	})

	t.Run("no-dot name reports ErrNoDot", func(t *testing.T) {
		_, err := env.p.ResolveSymbol(context.Background(), "JustAName", nil)
		assert.ErrorIs(t, err, syntaxapi.ErrNoDot)
	})

	t.Run("names with more than three parts still miss", func(t *testing.T) {
		// The index accepts any dotted length; after a full scan the
		// miss is authoritative (empty), while the backing fallback
		// reports not-found.
		for _, name := range []string{
			"mylib.MyType.String.Extra", "a.b.c.d",
		} {
			it, err := env.p.ResolveSymbol(context.Background(), name, nil)
			if err != nil {
				assert.ErrorContainsf(t, err, "no symbols found",
					"symbol %q must not resolve", name)
				continue
			}
			matches, terr := iterator.ToSlice(context.Background(), it)
			if terr != nil {
				assert.ErrorContainsf(t, terr, "no symbols found",
					"symbol %q must not resolve", name)
				continue
			}
			assert.Emptyf(t, matches, "symbol %q must not resolve", name)
		}
	})

	t.Run("listed symbols include refs, defs and methods, exclude unexported", func(t *testing.T) {
		got := listSet(t, env.p)
		for _, name := range []string{
			"mylib.MyType", "mylib.MyFunc", "mylib.New",
			"iter.Iterator", "iter.OnlyDefined", "iter.Aggregate",
			"iterator.Iterator",
			"mylib.MyType.String", "mylib.MyType.Set",
		} {
			assert.Truef(t, got[name], "expected %q to be listed", name)
		}
		for _, name := range []string{
			"mylib.unexportedHelper", "iter.privateOnlyDefined",
		} {
			assert.Falsef(t, got[name], "%q must not be listed", name)
		}
	})

	t.Run("dot- and blank-imported references are not indexed", func(t *testing.T) {
		got := listSet(t, env.p)
		assert.False(t, got["blueiter.Iterator"],
			"blank-imported package must contribute no references")
	})
}

func TestPythonE2E(t *testing.T) {
	t.Parallel()
	env := setupE2E(t, pyTestdata, pyFixtures)

	t.Run("resolve matches the backing parser for referenced symbols", func(t *testing.T) {
		for _, name := range []string{
			"geometry.area", "geometry.Shape", "requests.get", "service.Service",
		} {
			assertResolveMatchesBacking(t, env, name)
		}
	})

	t.Run("definition-only symbols resolve from the index", func(t *testing.T) {
		assert.Equal(t, []string{env.uri(t, "geometry.py")},
			resolveURIs(t, env.p, "geometry.perimeter"))
	})

	t.Run("python has no enforced visibility", func(t *testing.T) {
		// leading-underscore names are still indexed.
		assert.Equal(t, []string{env.uri(t, "geometry.py")},
			resolveURIs(t, env.p, "geometry._Internal"))
	})

	t.Run("listed symbols include qualified references", func(t *testing.T) {
		got := listSet(t, env.p)
		for _, name := range []string{
			"geometry.area", "geometry.Shape", "requests.get", "service.Service",
		} {
			assert.Truef(t, got[name], "expected %q to be listed", name)
		}
		for _, name := range []string{"mylib.MyType", "iterator.Iterator"} {
			assert.Falsef(t, got[name], "Go name %q must not appear", name)
		}
	})
}

func TestPythonPackageE2E(t *testing.T) {
	t.Parallel()
	env := setupE2E(t, pyPkgTestdata, pyFixtures)

	t.Run("dotted module paths resolve from the index", func(t *testing.T) {
		for name, rel := range map[string]string{
			// Re-export bindings answer the package-qualified names.
			"mypkg.Widget":      "src/mypkg/__init__.py",
			"mypkg.make_widget": "src/mypkg/__init__.py",
			"mypkg.sub.slug":    "src/mypkg/sub/__init__.py",
			"sub.slug":          "src/mypkg/sub/__init__.py",
			// Full dotted paths and their suffixes address the module.
			"mypkg._impl.Widget":      "src/mypkg/_impl.py",
			"_impl.Widget":            "src/mypkg/_impl.py",
			"mypkg.sub._helpers.slug": "src/mypkg/sub/_helpers.py",
			// Method definitions under dotted module paths.
			"mypkg.sub._helpers.Slugger.run": "src/mypkg/sub/_helpers.py",
			"_helpers.Slugger.run":           "src/mypkg/sub/_helpers.py",
			// Bare Type.method with no module qualifier.
			"Slugger.run":   "src/mypkg/sub/_helpers.py",
			"Widget.render": "src/mypkg/_impl.py",
			// A script outside any package keeps its file stem.
			"main.main": "main.py",
		} {
			assert.Equalf(t, []string{env.uri(t, rel)}, resolveURIs(t, env.p, name),
				"resolution for %q", name)
		}
	})

	t.Run("resolve matches the backing parser", func(t *testing.T) {
		for _, name := range []string{
			"mypkg.Widget", "mypkg._impl.Widget", "sub.slug",
			"mypkg.sub._helpers.Slugger.run",
		} {
			assertResolveMatchesBacking(t, env, name)
		}
	})

	t.Run("misses are authoritative after a full scan", func(t *testing.T) {
		for _, name := range []string{"mypkg.DoesNotExist", "sub.Widget"} {
			it, err := env.p.ResolveSymbol(context.Background(), name, nil)
			require.NoError(t, err)
			matches, err := iterator.ToSlice(context.Background(), it)
			require.NoError(t, err)
			assert.Emptyf(t, matches, "symbol %q must not resolve", name)
		}
	})

	t.Run("listed symbols include dotted and re-exported names", func(t *testing.T) {
		got := listSet(t, env.p)
		for _, name := range []string{
			"mypkg.Widget", "mypkg._impl.Widget", "_impl.Widget",
			"mypkg.sub.slug", "sub.slug",
		} {
			assert.Truef(t, got[name], "expected %q to be listed", name)
		}
		assert.False(t, got["sub.Widget"],
			"re-export must be scoped to its own package")
	})
}

func TestRustE2E(t *testing.T) {
	t.Parallel()
	env := setupE2E(t, rsTestdata, rsFixtures)

	t.Run("resolve matches the backing parser for referenced symbols", func(t *testing.T) {
		for _, name := range []string{
			"geometry.area", "geometry.Shape", "requests.get",
			"service.Service", "text.slugify",
		} {
			assertResolveMatchesBacking(t, env, name)
		}
	})

	t.Run("definition-only symbols resolve from the index", func(t *testing.T) {
		assert.Equal(t, []string{env.uri(t, "geometry.rs")},
			resolveURIs(t, env.p, "geometry.perimeter"))
		assert.Equal(t, []string{env.uri(t, "app/service.rs")},
			resolveURIs(t, env.p, "service.boot"))
	})

	t.Run("listed symbols include qualified references", func(t *testing.T) {
		got := listSet(t, env.p)
		for _, name := range []string{
			"geometry.area", "geometry.Shape", "requests.get",
			"service.Service", "text.slugify",
		} {
			assert.Truef(t, got[name], "expected %q to be listed", name)
		}
	})
}

func TestZigE2E(t *testing.T) {
	t.Parallel()
	env := setupE2E(t, zigTestdata, zigFixtures)

	t.Run("resolve matches the backing parser for referenced symbols", func(t *testing.T) {
		for _, name := range []string{
			"geometry.area", "geometry.Shape", "requests.get",
			"service.Service", "text.slugify",
		} {
			assertResolveMatchesBacking(t, env, name)
		}
	})

	t.Run("definition-only symbols resolve from the index", func(t *testing.T) {
		assert.Equal(t, []string{env.uri(t, "geometry.zig")},
			resolveURIs(t, env.p, "geometry.perimeter"))
		assert.Equal(t, []string{env.uri(t, "app/service.zig")},
			resolveURIs(t, env.p, "service.boot"))
	})

	t.Run("listed symbols include qualified references", func(t *testing.T) {
		got := listSet(t, env.p)
		for _, name := range []string{
			"geometry.area", "geometry.Shape", "requests.get",
			"service.Service", "text.slugify",
		} {
			assert.Truef(t, got[name], "expected %q to be listed", name)
		}
	})
}

// Paths to the committed testdata modules and tree-sitter fixtures,
// relative to this package's directory.
const (
	goTestdata    = "../../idelsp/symbolresolve/testdata"
	pyTestdata    = "../../idelsp/symbolresolve/testdata_py"
	pyPkgTestdata = "../../idelsp/symbolresolve/testdata_py_pkg"
	rsTestdata    = "../../idelsp/symbolresolve/testdata_rs"
	zigTestdata   = "../../idelsp/symbolresolve/testdata_zig"

	goFixtures  = "../../idelsp/symbolresolve/go"
	pyFixtures  = "../syntaxtest/python"
	rsFixtures  = "../syntaxtest/rust"
	zigFixtures = "../syntaxtest/zig"
)

// benchWorkspace generates a workspace of Go files whose shared-name
// reference ratios mirror a real repository: hot symbols such as
// require.NoError appear 6-16 times per referencing file, so every
// generated file mentions mylib.MyType well beyond once.
func benchWorkspace(b *testing.B, files int) (workspaceapi.URI, workspaceapi.FileSystem) {
	b.Helper()
	root := b.TempDir()
	for i := range files {
		dir := filepath.Join(root, fmt.Sprintf("pkg%d", i%8))
		require.NoError(b, os.MkdirAll(dir, 0o755))
		src := fmt.Sprintf(`package pkg%d

import (
	"fmt"

	"example.com/bench/mylib"
)

type Widget%d struct{ v mylib.MyType }

func (w Widget%d) String() string { return fmt.Sprint(w.v) }

func (w *Widget%d) Set(v mylib.MyType) { w.v = v }

func New%d(v mylib.MyType) Widget%d { return Widget%d{v: v} }

func Combine%d(a mylib.MyType, b mylib.MyType, c mylib.MyType) []mylib.MyType {
	items := []mylib.MyType{a, b, c}
	var out []mylib.MyType
	for _, item := range items {
		var tmp mylib.MyType = item
		out = append(out, tmp)
	}
	var last mylib.MyType = out[0]
	other := map[string]mylib.MyType{"last": last}
	_ = other
	return out
}
`, i%8, i, i, i, i, i, i, i)
		require.NoError(b, os.WriteFile(
			filepath.Join(dir, fmt.Sprintf("f%d.go", i)),
			[]byte(src), 0o644))
	}

	rootURI, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(b, err)
	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), rootURI)
	require.NoError(b, err)
	b.Cleanup(func() { _ = scheme.Close() })
	return rootURI, scheme
}

// BenchmarkInitialScan measures a cold index build over generated Go
// files with a real tree-sitter backing parser, against both the
// in-memory stub and the production bolt backend. Bolt reuses one
// database file with a fresh partition namespace per iteration so
// every iteration is a cold scan without accumulating open handles.
func BenchmarkInitialScan(b *testing.B) {
	for _, files := range []int{64, 256} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			rootURI, scheme := benchWorkspace(b, files)
			backing := treesitter.NewParser(scheme, pkgManagerFor(b, goFixtures), rootURI)

			b.Run("db=stub", func(b *testing.B) {
				b.ResetTimer()
				for range b.N {
					p, err := New(backing, scheme, rootURI,
						storagestub.NewInMemoryService(),
						&recordingNotifications{}, syncTick)
					require.NoError(b, err)
					require.NoError(b, p.Wait(context.Background()))
					require.NoError(b, p.Close())
				}
			})

			b.Run("db=bolt", func(b *testing.B) {
				db, err := boltdoc.New(
					filepath.Join(b.TempDir(), "db.data"), docbson.Marshaler())
				require.NoError(b, err)
				b.Cleanup(func() { _ = db.Close() })
				b.ResetTimer()
				for i := range b.N {
					iter, err := db.Partition(fmt.Sprintf("iter%d", i))
					require.NoError(b, err)
					p, err := New(backing, scheme, rootURI, iter,
						&recordingNotifications{}, syncTick)
					require.NoError(b, err)
					require.NoError(b, p.Wait(context.Background()))
					require.NoError(b, p.Close())
					require.NoError(b, iter.Close())
				}
			})
		})
	}
}

// queryParser builds a Parser over db without starting the indexer so
// benchmarks can populate the database directly through the real write
// path (upsertSymbol) and measure the query side in isolation.
func queryParser(b *testing.B, db storageapi.Service) *Parser {
	b.Helper()
	files, err := db.Partition(filesPartition)
	require.NoError(b, err)
	symbols, err := db.Partition(symbolsPartition)
	require.NoError(b, err)
	names, err := db.Partition(namesPartition)
	require.NoError(b, err)
	return &Parser{
		files:       files,
		symbols:     symbols,
		names:       names,
		meta:        db,
		ctx:         context.Background(),
		scannedEver: true,
	}
}

// BenchmarkListReferencedSymbols measures a full drain of the indexed
// symbol list over a database shaped like a large workspace: mostly
// small symbols, a hot tail with thousands of locations each, and a
// fraction of method-definition-only names, all of which are listed. The
// in-memory storage round-trips the same BSON marshaler as the
// production bolt backend, so decode costs are representative.
func BenchmarkListReferencedSymbols(b *testing.B) {
	ctx := context.Background()
	for _, shape := range []struct {
		names, hotEvery, locs, hotLocs int
	}{
		{names: 2000, hotEvery: 50, locs: 4, hotLocs: 500},
		{names: 10000, hotEvery: 100, locs: 4, hotLocs: 2000},
	} {
		b.Run(fmt.Sprintf("names=%d", shape.names), func(b *testing.B) {
			p := queryParser(b, storagestub.NewInMemoryService())
			want := 0
			for i := range shape.names {
				n := shape.locs
				if i%shape.hotEvery == 0 {
					n = shape.hotLocs
				}
				name := fmt.Sprintf("pkg%d.Symbol%d", i%64, i)
				methodOnly := i%7 == 0
				if methodOnly {
					name += ".Method"
				}
				want++
				locs := make([]symbolLoc, n)
				for j := range locs {
					kind := kindRef
					switch {
					case methodOnly:
						kind = kindMethodDef
					case j == 0:
						kind = kindDef
					}
					locs[j] = symbolLoc{
						URI:  fmt.Sprintf("file:///ws/pkg%d/f%d.go", i%64, j%32),
						X:    j % 80,
						Y:    j,
						Kind: kind,
					}
				}
				p.upsertSymbol(ctx, name, "", locs, false, false)
			}
			b.ResetTimer()
			for range b.N {
				it, err := p.ListReferencedSymbols(ctx)
				require.NoError(b, err)
				names, err := iterator.ToSlice(ctx, it)
				require.NoError(b, err)
				require.Len(b, names, want)
			}
		})
	}
}
