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

	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

func TestResolveE2E(t *testing.T) {
	t.Parallel()

	env := setupResolveEnv(t)

	mainURI := env.fileURI("main.go")
	useBlueURI := env.fileURI("use_blue.go")
	useSdkURI := env.fileURI("use_sdk.go")
	iterFileURI := env.fileURI("iter/iter.go")
	iterExtraURI := env.fileURI("iter/extra.go")
	mylibFileURI := env.fileURI("mylib/mylib.go")
	manyB := env.fileURI("manyfiles/b.go")
	manyUserURI := env.fileURI("uses_manyfiles.go")
	useGenericURI := env.fileURI("use_generic.go")

	tests := []struct {
		name                  string
		symbol                string
		wantURIs              []string
		wantDisplays          []string
		wantImportPaths       map[string]string
		wantErrIs             error
		wantErrContains       string
		wantURICount          int
		mustContainURI        string
		mustContainImportPath string
	}{
		{
			// MyType is referenced from main.go (parameter type),
			// from embeds.go (struct embedding + type-alias RHS),
			// and defined in mylib.go. The two consumer files share
			// an import path so dedupeByImport collapses them to
			// one Match keyed by the import path; the surviving URI
			// depends on tree-sitter traversal order. The
			// definitions phase is skipped because phase 1 already
			// found a match, so mylib/mylib.go is NOT surfaced
			// here — it shows up only via gopls follow-up.
			name:                  "qualified type collapses consumers with same import path",
			symbol:                "mylib.MyType",
			wantURICount:          1,
			mustContainImportPath: "example.com/resolvetest/mylib",
		},
		{
			// MyFunc is called qualified in main.go. The unqualified
			// reference inside dotimport.go (via dot import) must NOT
			// appear as a match. The definitions phase is skipped
			// once main.go is found, so mylib/mylib.go is not in
			// the result set.
			name:     "selector expression ignores dot-imported references",
			symbol:   "mylib.MyFunc",
			wantURIs: []string{mainURI},
		},
		{
			// iter.Iterator is referenced from main.go as a
			// qualified type. Phase 1 finds main.go, so the
			// definitions phase is skipped and iter/iter.go is not
			// returned. The user navigates via gopls follow-up
			// from main.go.
			name:     "iter package qualified type and selector phases",
			symbol:   "iter.Iterator",
			wantURIs: []string{mainURI},
		},
		{
			name: "workspace-defined symbol with no reference resolves via " +
				"definitions phase",
			symbol:   "iter.OnlyDefined",
			wantURIs: []string{iterFileURI},
		},
		{
			// Same package split across two files: the definitions
			// phase must find Aggregate in extra.go because the
			// symbol has no external qualified usages and phases
			// 1+2 return empty.
			name:     "definition in sibling file of same package resolves",
			symbol:   "iter.Aggregate",
			wantURIs: []string{iterExtraURI},
		},
		{
			// Constants are captured under DefinitionVar, which the
			// definitions phase intentionally does not search. The
			// resolver must report not-found rather than pretending
			// it can resolve constants.
			name:            "package-level constant is not found (DefinitionVar not in mask)",
			symbol:          "iter.MaxItems",
			wantErrContains: "no symbols found",
		},
		{
			// Same as MaxItems: package-level vars are intentionally
			// outside the DefinitionFunc | DefinitionType mask used
			// by the resolver.
			name:            "package-level variable is not found (DefinitionVar not in mask)",
			symbol:          "iter.DefaultIterator",
			wantErrContains: "no symbols found",
		},
		{
			// Imports whose directory layout repeats the package name
			// (nested/nested) must use the declared package, not any
			// path segment. Phase 1 finds the reference in main.go;
			// the definitions phase is skipped so nested/nested.go
			// is not in the result set.
			name:     "nested directory whose path repeats the package name",
			symbol:   "nested.Repeated",
			wantURIs: []string{mainURI},
		},
		{
			// Homonymous exported names in two unrelated packages
			// must NOT cross-contaminate. mylib.New has no external
			// callers, so phases 1+2 return empty and the
			// definitions phase surfaces the declaration in
			// mylib/mylib.go without pulling iter/extra.go.
			name:     "homonymous symbol in another package is excluded (mylib.New)",
			symbol:   "mylib.New",
			wantURIs: []string{mylibFileURI},
		},
		{
			// The mirror image: iter.New must point into iter/extra.go
			// and not leak the mylib.New definition. Same reasoning
			// as mylib.New above — the definitions phase only runs
			// because phases 1+2 returned no matches.
			name:     "homonymous symbol in another package is excluded (iter.New)",
			symbol:   "iter.New",
			wantURIs: []string{iterExtraURI},
		},
		{
			// Generic functions must not break the resolver — Map is
			// declared as func Map[T any](...) and the definitions
			// phase still needs to surface it for iter.Map.
			name:     "generic function resolves via definitions phase",
			symbol:   "iter.Map",
			wantURIs: []string{iterExtraURI},
		},
		{
			// manyfiles.Token has:
			//   - external qualified-type reference in
			//     uses_manyfiles.go (collapses by import path)
			//   - the declaration in manyfiles/a.go
			// Phase 1 finds uses_manyfiles.go, so the definitions
			// phase is skipped and manyfiles/a.go is not returned.
			// Same-package unqualified references in b.go and c.go
			// are intentionally NOT surfaced — they lack the
			// "manyfiles." prefix the resolver depends on.
			name:     "external reference plus declaration of multi-file package symbol",
			symbol:   "manyfiles.Token",
			wantURIs: []string{manyUserURI},
		},
		{
			// Acquire is defined in c.go but never written as
			// "manyfiles.Acquire" outside its own package. The
			// only external call site is uses_manyfiles.go which
			// uses "manyfiles.Acquire()", so phase 2 finds that
			// file and the definitions phase is skipped. c.go is
			// not surfaced.
			name:     "definition-only symbol from one file of a multi-file package",
			symbol:   "manyfiles.Acquire",
			wantURIs: []string{manyUserURI},
		},
		{
			// manyfiles.Use is defined in b.go but never referenced
			// externally; the resolver must still find it via the
			// definitions phase alone.
			name:     "definition-only manyfiles.Use surfaces b.go",
			symbol:   "manyfiles.Use",
			wantURIs: []string{manyB},
		},
		{
			// Two packages named "iterator" produce four match
			// importing files plus the two definition files, but
			// the definitions phase is skipped once phases 1+2 find
			// the importing files. The matches whose file declares
			// an explicit alias get a Display prefixed with the
			// resolved import path, which is the disambiguation
			// users see in the picker.
			name:     "two packages named iterator emit disambiguated Matches",
			symbol:   "iterator.Iterator",
			wantURIs: []string{useBlueURI, useSdkURI},
			wantImportPaths: map[string]string{
				useBlueURI: "example.com/resolvetest/blueiter",
				useSdkURI:  "example.com/resolvetest/sdkiter",
			},
		},
		{
			// pkg.Type.method resolves to the value-receiver method
			// declaration in mylib.go.
			name:     "value-receiver method resolves to declaration",
			symbol:   "mylib.MyType.String",
			wantURIs: []string{mylibFileURI},
		},
		{
			// Pointer-receiver methods resolve identically.
			name:     "pointer-receiver method resolves to declaration",
			symbol:   "mylib.MyType.Set",
			wantURIs: []string{mylibFileURI},
		},
		{
			// A method on a type that does not exist returns
			// not-found rather than matching another type's method.
			name:            "method on unknown type returns not-found",
			symbol:          "mylib.Other.String",
			wantErrContains: "no symbols found",
		},
		{
			// A missing method on an existing type returns not-found.
			name:            "missing method returns not-found",
			symbol:          "mylib.MyType.Missing",
			wantErrContains: "no symbols found",
		},
		{
			// Names with more than three dotted segments are not
			// resolvable.
			name:            "four-segment dotted name returns not-found",
			symbol:          "mylib.MyType.String.Extra",
			wantErrContains: "no symbols found",
		},
		{
			// Generic qualified types like genericiter.Stream[*data]
			// are parsed by tree-sitter as
			//   (generic_type type: (qualified_type ...) ...)
			// rather than a bare qualified_type. Phase 1 finds the
			// instantiating consumer (use_generic.go) and the
			// definitions phase is skipped, so the declaration
			// genericiter/genericiter.go is not surfaced. This
			// mirrors real-world usage of iterator.Iterator[*T].
			name:     "generic qualified type resolves consumers",
			symbol:   "genericiter.Stream",
			wantURIs: []string{useGenericURI},
		},
		{
			name:      "empty string returns ErrNoDot",
			symbol:    "",
			wantErrIs: syntaxapi.ErrNoDot,
		},
		{
			// Wrong package, valid symbol: iter.Reduce exists but
			// mylib.Reduce does not. The resolver must NOT cross
			// packages when the symbol is missing in the target.
			name:            "valid symbol in wrong package returns not-found",
			symbol:          "mylib.Reduce",
			wantErrContains: "no symbols found",
		},
		{
			// iterator package has Iterator in both blueiter and
			// sdkiter but Reduce only in iter. Asking for
			// iterator.Reduce must not synthesise a match by mixing
			// "iterator" package with "Reduce" symbol from a
			// different package.
			name:            "symbol from a different package with same alias is not surfaced",
			symbol:          "iterator.Reduce",
			wantErrContains: "no symbols found",
		},
		{
			name:      "name without dot returns ErrNoDot",
			symbol:    "JustAName",
			wantErrIs: syntaxapi.ErrNoDot,
		},
		{
			name:            "missing symbol returns not-found error",
			symbol:          "mylib.DoesNotExist",
			wantErrContains: "no symbols found",
		},
		{
			name:            "unexported workspace symbol is not surfaced",
			symbol:          "iter.privateOnlyDefined",
			wantErrContains: "no symbols found",
		},
		{
			name:            "unexported helper in another package is not surfaced",
			symbol:          "mylib.unexportedHelper",
			wantErrContains: "no symbols found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &recordingProgress{}
			matches, err := symbolresolve.Resolve(
				context.Background(), env.parser, env.qc,
				specIter(symbolresolve.Go), tt.symbol, rec,
			)

			if tt.wantErrIs != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErrIs),
					"want errors.Is(%v, %v) to be true", err, tt.wantErrIs)
				return
			}
			if tt.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
				return
			}

			require.NoError(t, err)
			require.NotEmpty(t, matches)

			// At least one progress phase must have been reported per
			// successful resolution.
			assert.NotEmpty(t, rec.events,
				"Resolve must invoke Progress at least once for successful runs")

			gotURIs := uriStrings(matches)
			if len(tt.wantURIs) > 0 {
				assert.ElementsMatch(t, tt.wantURIs, gotURIs,
					"resolved URIs mismatch; matches=%+v", matches)
			}
			if tt.wantURICount > 0 {
				assert.Equalf(t, tt.wantURICount, len(gotURIs),
					"match count mismatch; matches=%+v", matches)
			}
			if tt.mustContainURI != "" {
				assert.Containsf(t, gotURIs, tt.mustContainURI,
					"matches must contain URI %s", tt.mustContainURI)
			}
			if tt.mustContainImportPath != "" {
				found := false
				for _, m := range matches {
					if m.ImportPath == tt.mustContainImportPath {
						found = true
						break
					}
				}
				assert.Truef(t, found,
					"matches must contain ImportPath %s; matches=%+v",
					tt.mustContainImportPath, matches)
			}

			if len(tt.wantDisplays) > 0 {
				// Pair each Match by URI to its expected Display
				// substring without relying on a specific order.
				byURI := make(map[string]syntaxapi.Match, len(matches))
				for _, m := range matches {
					byURI[m.URI] = m
				}
				for i, uri := range tt.wantURIs {
					m, ok := byURI[uri]
					require.Truef(t, ok, "no match for URI %s", uri)
					assert.Containsf(t, m.Display, tt.wantDisplays[i],
						"Display for %s should contain %q",
						uri, tt.wantDisplays[i])
				}
			}

			if len(tt.wantImportPaths) > 0 {
				byURI := make(map[string]syntaxapi.Match, len(matches))
				for _, m := range matches {
					byURI[m.URI] = m
				}
				for uri, wantImport := range tt.wantImportPaths {
					m, ok := byURI[uri]
					require.Truef(t, ok, "no match for URI %s", uri)
					assert.Equalf(t, wantImport, m.ImportPath,
						"ImportPath for %s", uri)
					assert.Containsf(t, m.Display, wantImport,
						"Display for %s should be prefixed with import path",
						uri)
				}
			}
		})
	}
}

func TestFilePackagesE2E(t *testing.T) {
	t.Parallel()

	env := setupResolveEnv(t)

	packages, err := symbolresolve.FilePackages(
		context.Background(), env.parser, symbolresolve.Go,
	)
	require.NoError(t, err)

	uriFor := func(rel string) workspaceapi.URI {
		uri, perr := workspaceapi.ParseURI(env.fileURI(rel))
		require.NoError(t, perr)
		return uri
	}

	want := map[workspaceapi.URI]string{
		uriFor("main.go"):                 "main",
		uriFor("use_blue.go"):             "main",
		uriFor("use_sdk.go"):              "main",
		uriFor("dotimport.go"):            "main",
		uriFor("blankimport.go"):          "main",
		uriFor("embeds.go"):               "main",
		uriFor("uses_manyfiles.go"):       "main",
		uriFor("mylib/mylib.go"):          "mylib",
		uriFor("iter/iter.go"):            "iter",
		uriFor("iter/extra.go"):           "iter",
		uriFor("blueiter/iterator.go"):    "iterator",
		uriFor("sdkiter/iterator.go"):     "iterator",
		uriFor("nested/nested/nested.go"): "nested",
		uriFor("manyfiles/a.go"):          "manyfiles",
		uriFor("manyfiles/b.go"):          "manyfiles",
		uriFor("manyfiles/c.go"):          "manyfiles",
	}
	for uri, pkg := range want {
		assert.Equalf(t, pkg, packages[uri],
			"package name for %s", uri)
	}
}

func TestSearchDefinitionsE2E(t *testing.T) {
	t.Parallel()

	env := setupResolveEnv(t)

	packages, err := symbolresolve.FilePackages(
		context.Background(), env.parser, symbolresolve.Go,
	)
	require.NoError(t, err)

	ch := make(chan string, 64)
	done := make(chan error, 1)
	go func() {
		defer close(ch)
		done <- symbolresolve.SearchDefinitions(
			context.Background(), env.parser, symbolresolve.Go, env.qc,
			packages, ch, symbolresolve.IsExported,
		)
	}()

	got := make(map[string]bool)
	for s := range ch {
		got[s] = true
	}
	require.NoError(t, <-done)

	for _, name := range []string{
		"mylib.MyType", "mylib.MyFunc",
		"mylib.New",
		"iter.Iterator", "iter.Reduce", "iter.OnlyDefined",
		"iter.Aggregate", "iter.IsEmpty", "iter.New", "iter.Map",
		"iterator.Iterator", "iterator.Map", "iterator.Filter",
		"manyfiles.Token", "manyfiles.Use", "manyfiles.Acquire",
		"nested.Repeated",
	} {
		assert.Truef(t, got[name],
			"expected workspace definition %q to be streamed", name)
	}

	for _, name := range []string{
		"mylib.unexportedHelper",
		"iter.privateOnlyDefined",
	} {
		assert.Falsef(t, got[name],
			"unexported %q must not be streamed", name)
	}

	// DefinitionVar is intentionally outside the SearchDefinitions
	// mask.
	for _, name := range []string{
		"iter.MaxItems",
		"iter.DefaultIterator",
	} {
		assert.Falsef(t, got[name],
			"DefinitionVar %q must not be streamed by SearchDefinitions", name)
	}
}

func TestResolveCancelledContext(t *testing.T) {
	t.Parallel()

	env := setupResolveEnv(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// The exact error is implementation-defined; what matters is
	// that the call returns rather than hanging on a closed channel.
	_, _ = symbolresolve.Resolve(
		ctx, env.parser, env.qc, specIter(symbolresolve.Go), "mylib.MyType", nil)
}

func TestResolveConcurrent(t *testing.T) {
	t.Parallel()

	env := setupResolveEnv(t)

	symbols := []string{
		"mylib.MyType", "mylib.MyFunc", "mylib.New",
		"iter.Iterator", "iter.Reduce", "iter.Aggregate",
		"iter.IsEmpty", "iter.OnlyDefined", "iter.Map", "iter.New",
		"iterator.Iterator", "iterator.Map", "iterator.Filter",
		"manyfiles.Token", "manyfiles.Use", "manyfiles.Acquire",
		"nested.Repeated",
	}

	var wg sync.WaitGroup
	wg.Add(len(symbols))
	for _, s := range symbols {
		go func(symbol string) {
			defer wg.Done()
			matches, err := symbolresolve.Resolve(
				context.Background(), env.parser, env.qc,
				specIter(symbolresolve.Go), symbol, nil,
			)
			assert.NoErrorf(t, err, "Resolve(%q)", symbol)
			assert.NotEmptyf(t, matches, "Resolve(%q)", symbol)
		}(s)
	}
	wg.Wait()
}

type resolveEnv struct {
	root   string
	parser treesitter.Parser
	qc     symbolresolve.QualifierContext
}

func (e *resolveEnv) fileURI(rel string) string {
	return "file://" + filepath.Join(e.root, rel)
}

func setupResolveEnv(t *testing.T) *resolveEnv {
	t.Helper()

	root := t.TempDir()
	copyDirT(t, "testdata", root)

	rootURI := "file://" + root
	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	parser := treesitter.NewParser(scheme, treeSitterPkgManager(t), uri)
	return &resolveEnv{
		root: root, parser: parser,
		qc: symbolresolve.NewQualifierContext(scheme, uri),
	}
}

func treeSitterPkgManager(t testing.TB) syntax.PkgManager {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	goDir := filepath.Join(wd, "go")
	for _, name := range []string{
		"locals.scm", "highlights.scm", "indents.scm", "folds.scm",
	} {
		p := filepath.Join(goDir, name)
		_, err := os.Stat(p)
		require.NoErrorf(t, err, "missing tree-sitter fixture %s", p)
	}

	return &stubPkgManager{
		files: []string{
			grammarfixture.ParserPath(t, goDir),
			filepath.Join(goDir, "locals.scm"),
			filepath.Join(goDir, "highlights.scm"),
			filepath.Join(goDir, "indents.scm"),
			filepath.Join(goDir, "folds.scm"),
		},
	}
}

type stubPkgManager struct {
	files []string
}

func (s *stubPkgManager) LibDir(
	_ context.Context, _ string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice(s.files), nil
}

func copyDirT(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			require.NoError(t, os.MkdirAll(dstPath, 0o755))
			copyDirT(t, srcPath, dstPath)
			continue
		}
		data, err := os.ReadFile(srcPath)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(dstPath, data, 0o644))
	}
}

func uriStrings(matches []syntaxapi.Match) []string {
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = m.URI
	}
	return out
}

type recordingProgress struct {
	mu     sync.Mutex
	events []progressEvent
}

type progressEvent struct {
	msg         string
	found       int
	step, total int64
}

func (r *recordingProgress) Report(msg string, found int, step, total int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events,
		progressEvent{msg: msg, found: found, step: step, total: total})
}
