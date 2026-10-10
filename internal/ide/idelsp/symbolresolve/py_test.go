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
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"

	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

func TestResolvePythonE2E(t *testing.T) {
	t.Parallel()

	env := setupPythonEnv(t)

	mainURI := env.fileURI("main.py")
	geometryURI := env.fileURI("geometry.py")
	requestsURI := env.fileURI("requests.py")
	appInitURI := env.fileURI("app/__init__.py")
	serviceURI := env.fileURI("app/service.py")
	modelsURI := env.fileURI("app/models.py")
	textURI := env.fileURI("utils/text.py")
	utilsConfigURI := env.fileURI("utils/config.py")
	appConfigURI := env.fileURI("app/config.py")
	arithmeticURI := env.fileURI("calc/arithmetic.py")

	tests := []struct {
		name string
		// symbol is the package-qualified name passed to Resolve.
		symbol string
		// wantURIs, when set, must match the resolved URIs exactly.
		wantURIs []string
		// wantURICount, when set, asserts the number of matches.
		wantURICount    int
		wantErrIs       error
		wantErrContains string
	}{
		{
			// geometry.area is referenced from main.py and app/models.py.
			// The reference (attribute) phase surfaces both call sites
			// and the definition file geometry.py is not added because
			// phase 1 already found matches.
			name:     "qualified reference surfaces all call sites",
			symbol:   "geometry.area",
			wantURIs: []string{mainURI, modelsURI},
		},
		{
			// A single qualified reference resolves to the one call
			// site; the definition geometry.py is not surfaced.
			name:     "single reference resolves to its call site",
			symbol:   "geometry.Shape",
			wantURIs: []string{mainURI},
		},
		{
			// perimeter is defined in geometry.py but never referenced
			// as geometry.perimeter, so the definitions phase surfaces
			// the declaration using the module file stem.
			name:     "definition-only function resolves via module stem",
			symbol:   "geometry.perimeter",
			wantURIs: []string{geometryURI},
		},
		{
			// Python has no enforced visibility: a leading-underscore
			// class must still resolve through the definitions phase.
			name:     "underscore class is still resolvable",
			symbol:   "geometry._Internal",
			wantURIs: []string{geometryURI},
		},
		{
			name:     "underscore function is still resolvable",
			symbol:   "geometry._make_internal",
			wantURIs: []string{geometryURI},
		},
		{
			// A dependency-style module ("requests") referenced from
			// main.py resolves to the call site.
			name:     "dependency module reference resolves to call site",
			symbol:   "requests.get",
			wantURIs: []string{mainURI},
		},
		{
			// The dependency's own class is never referenced
			// qualified, so it resolves to its definition file.
			name:     "dependency definition resolves to its module file",
			symbol:   "requests.Response",
			wantURIs: []string{requestsURI},
		},
		{
			// __init__.py qualifies as the parent directory name, so a
			// class declared there resolves as app.App.
			name:     "package __init__ qualifies as directory name",
			symbol:   "app.App",
			wantURIs: []string{appInitURI},
		},
		{
			name:     "package __init__ function qualifies as directory name",
			symbol:   "app.create",
			wantURIs: []string{appInitURI},
		},
		{
			// service.Service is referenced in main.py via
			// service.Service(); phase 1 finds main.py.
			name:     "class reference resolves to call site",
			symbol:   "service.Service",
			wantURIs: []string{mainURI},
		},
		{
			// service.boot is defined but unreferenced; definitions
			// phase surfaces app/service.py via the file stem.
			name:     "definition-only resolves to nested module file",
			symbol:   "service.boot",
			wantURIs: []string{serviceURI},
		},
		{
			// text.slugify is referenced from both main.py and
			// app/service.py.
			name:     "helper referenced from multiple modules",
			symbol:   "text.slugify",
			wantURIs: []string{mainURI, serviceURI},
		},
		{
			name:     "underscore helper resolves via definitions phase",
			symbol:   "text._private_helper",
			wantURIs: []string{textURI},
		},
		{
			// config.load is defined in BOTH utils/config.py and
			// app/config.py. The two files share the module name
			// "config", so the ambiguous lookup must surface both
			// definition files (Python has no import-path dedup).
			name:         "ambiguous module name surfaces both definition files",
			symbol:       "config.load",
			wantURIs:     []string{utilsConfigURI, appConfigURI},
			wantURICount: 2,
		},
		{
			// config.reset exists only in utils/config.py; the
			// homonymous app/config.py must not be surfaced for a name
			// it does not define.
			name:     "ambiguous module disambiguated by symbol name (utils)",
			symbol:   "config.reset",
			wantURIs: []string{utilsConfigURI},
		},
		{
			name:     "ambiguous module disambiguated by symbol name (app)",
			symbol:   "config.save",
			wantURIs: []string{appConfigURI},
		},
		{
			// arithmetic.helper is homonymous in name only with other
			// "helper" definitions; the distinct module name keeps it
			// isolated to calc/arithmetic.py.
			name:     "homonymous symbol in another module is isolated",
			symbol:   "arithmetic.helper",
			wantURIs: []string{arithmeticURI},
		},
		{
			name:            "missing symbol returns not-found",
			symbol:          "geometry.DoesNotExist",
			wantErrContains: "no symbols found",
		},
		{
			// slugify lives in text, not the utils package __init__;
			// asking for utils.slugify must not synthesise a match.
			name:            "valid symbol in wrong module returns not-found",
			symbol:          "utils.slugify",
			wantErrContains: "no symbols found",
		},
		{
			// helper is defined in arithmetic and service modules but
			// never in text; the resolver must not cross modules.
			name:            "symbol from a different module is not surfaced",
			symbol:          "text.helper",
			wantErrContains: "no symbols found",
		},
		{
			// pkg.Class.method resolves to the class method definition.
			name:     "class method resolves to definition",
			symbol:   "geometry.Shape.area",
			wantURIs: []string{geometryURI},
		},
		{
			// A method on an unknown class returns not-found.
			name:            "method on unknown class returns not-found",
			symbol:          "geometry.Other.area",
			wantErrContains: "no symbols found",
		},
		{
			// A missing method on an existing class returns not-found.
			name:            "missing class method returns not-found",
			symbol:          "geometry.Shape.missing",
			wantErrContains: "no symbols found",
		},
		{
			name:      "empty string returns ErrNoDot",
			symbol:    "",
			wantErrIs: syntaxapi.ErrNoDot,
		},
		{
			name:      "name without dot returns ErrNoDot",
			symbol:    "JustAName",
			wantErrIs: syntaxapi.ErrNoDot,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := &recordingProgress{}
			matches, err := symbolresolve.Resolve(
				context.Background(), env.parser, env.qc,
				specIter(symbolresolve.Python), tt.symbol, rec,
			)

			if tt.wantErrIs != nil {
				require.Error(t, err)
				assert.Truef(t, errors.Is(err, tt.wantErrIs),
					"want errors.Is(%v, %v)", err, tt.wantErrIs)
				return
			}
			if tt.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
				return
			}

			require.NoError(t, err)
			require.NotEmpty(t, matches)
			assert.NotEmpty(t, rec.events,
				"Resolve must report progress on success")

			gotURIs := uriStrings(matches)
			if len(tt.wantURIs) > 0 {
				assert.ElementsMatchf(t, tt.wantURIs, gotURIs,
					"resolved URIs mismatch; matches=%+v", matches)
			}
			if tt.wantURICount > 0 {
				assert.Equalf(t, tt.wantURICount, len(gotURIs),
					"match count mismatch; matches=%+v", matches)
			}
		})
	}
}

func TestResolvePythonSelfAttributeQuirk(t *testing.T) {
	t.Parallel()

	// service.run() calls self.helper(); the attribute phase treats
	// "self" as a module alias, so a lookup of self.helper surfaces the
	// file that defines helper. This documents a known limitation: the
	// tree-sitter resolver cannot tell instance attributes from
	// module-qualified references, and pyright is expected to provide
	// precise resolution on follow-up.
	env := setupPythonEnv(t)
	matches, err := symbolresolve.Resolve(
		context.Background(), env.parser, env.qc,
		specIter(symbolresolve.Python), "self.helper", nil,
	)
	require.NoError(t, err)
	assert.Contains(t, uriStrings(matches), env.fileURI("app/service.py"))
}

func TestResolvePythonPackageE2E(t *testing.T) {
	t.Parallel()

	env := setupPythonEnvAt(t, "testdata_py_pkg")

	pkgInitURI := env.fileURI("src/mypkg/__init__.py")
	implURI := env.fileURI("src/mypkg/_impl.py")
	subInitURI := env.fileURI("src/mypkg/sub/__init__.py")
	helpersURI := env.fileURI("src/mypkg/sub/_helpers.py")
	mainURI := env.fileURI("main.py")

	tests := []struct {
		name            string
		symbol          string
		wantURIs        []string
		wantErrContains string
	}{
		{
			// Widget is defined in _impl.py but re-exported by the
			// package __init__: the public binding site answers the
			// package-qualified name.
			name:     "re-exported class resolves to package __init__",
			symbol:   "mypkg.Widget",
			wantURIs: []string{pkgInitURI},
		},
		{
			name:     "re-exported function resolves to package __init__",
			symbol:   "mypkg.make_widget",
			wantURIs: []string{pkgInitURI},
		},
		{
			// The full dotted module path addresses the private module
			// directly.
			name:     "full dotted module path resolves definition",
			symbol:   "mypkg._impl.Widget",
			wantURIs: []string{implURI},
		},
		{
			// Any dotted suffix of the module path also resolves.
			name:     "module path suffix resolves definition",
			symbol:   "_impl.Widget",
			wantURIs: []string{implURI},
		},
		{
			name:     "nested package re-export resolves to sub __init__",
			symbol:   "mypkg.sub.slug",
			wantURIs: []string{subInitURI},
		},
		{
			name:     "nested package re-export resolves by suffix",
			symbol:   "sub.slug",
			wantURIs: []string{subInitURI},
		},
		{
			name:     "deep module definition resolves by full path",
			symbol:   "mypkg.sub._helpers.slug",
			wantURIs: []string{helpersURI},
		},
		{
			// A nested-module name falls back to the method
			// interpretation when no symbol matches.
			name:     "method resolves under full module path",
			symbol:   "mypkg.sub._helpers.Slugger.run",
			wantURIs: []string{helpersURI},
		},
		{
			name:     "method resolves under module path suffix",
			symbol:   "_helpers.Slugger.run",
			wantURIs: []string{helpersURI},
		},
		{
			// A bare Type.method name — the form an agent naturally
			// types when navigating a class method — resolves without
			// any module qualifier.
			name:     "bare class method resolves without module qualifier",
			symbol:   "Slugger.run",
			wantURIs: []string{helpersURI},
		},
		{
			name:     "bare class method on _impl resolves",
			symbol:   "Widget.render",
			wantURIs: []string{implURI},
		},
		{
			// A bare method name on an unknown class stays not-found.
			name:            "bare method on unknown class returns not-found",
			symbol:          "Nonexistent.render",
			wantErrContains: "no symbols found",
		},
		{
			// A script outside any package keeps the single-segment
			// module name.
			name:     "script outside packages keeps file stem qualifier",
			symbol:   "main.main",
			wantURIs: []string{mainURI},
		},
		{
			name:            "missing name in package returns not-found",
			symbol:          "mypkg.DoesNotExist",
			wantErrContains: "no symbols found",
		},
		{
			// Widget is bound by mypkg's __init__, not by mypkg.sub's.
			name:            "re-export in wrong package returns not-found",
			symbol:          "sub.Widget",
			wantErrContains: "no symbols found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			matches, err := symbolresolve.Resolve(
				context.Background(), env.parser, env.qc,
				specIter(symbolresolve.Python), tt.symbol, nil,
			)

			if tt.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatchf(t, tt.wantURIs, uriStrings(matches),
				"resolved URIs mismatch; matches=%+v", matches)
		})
	}
}

func TestListReferencesPythonPackageE2E(t *testing.T) {
	t.Parallel()

	env := setupPythonEnvAt(t, "testdata_py_pkg")

	got := listReferences(t, env.parser, env.qc, specIter(symbolresolve.Python))

	// Definitions stream under every dotted suffix of the module path,
	// and re-export bindings stream under the package qualifier.
	for _, name := range []string{
		"mypkg.Widget", "mypkg.make_widget",
		"mypkg._impl.Widget", "_impl.Widget",
		"mypkg.sub.slug", "sub.slug",
		"mypkg.sub._helpers.slug", "_helpers.slug",
		"main.main",
	} {
		assert.Truef(t, got[name], "expected %q to be listed", name)
	}
	assert.False(t, got["sub.Widget"],
		"re-export must be scoped to its own package")
}

func TestResolvePythonConcurrent(t *testing.T) {
	t.Parallel()

	env := setupPythonEnv(t)

	symbols := []string{
		"geometry.area", "geometry.Shape", "geometry.perimeter",
		"requests.get", "app.App", "service.boot", "text.slugify",
		"config.load", "arithmetic.helper", "models.User",
	}

	var wg sync.WaitGroup
	wg.Add(len(symbols))
	for _, s := range symbols {
		go func(symbol string) {
			defer wg.Done()
			matches, err := symbolresolve.Resolve(
				context.Background(), env.parser, env.qc,
				specIter(symbolresolve.Python), symbol, nil,
			)
			assert.NoErrorf(t, err, "Resolve(%q)", symbol)
			assert.NotEmptyf(t, matches, "Resolve(%q)", symbol)
		}(s)
	}
	wg.Wait()
}

func TestSearchDefinitionsPythonE2E(t *testing.T) {
	t.Parallel()

	env := setupPythonEnv(t)

	packages, err := symbolresolve.FilePackages(
		context.Background(), env.parser, symbolresolve.Python,
	)
	require.NoError(t, err)
	assert.Nil(t, packages, "Python has no package clause")

	ch := make(chan string, 64)
	done := make(chan error, 1)
	go func() {
		defer close(ch)
		done <- symbolresolve.SearchDefinitions(
			context.Background(), env.parser, symbolresolve.Python, env.qc,
			packages, ch, nil,
		)
	}()

	got := make(map[string]bool)
	for s := range ch {
		got[s] = true
	}
	require.NoError(t, <-done)

	// Every function and class across the workspace is streamed,
	// qualified by its module file stem. __init__.py contributes the
	// directory name, and leading-underscore names are included
	// because Python has no enforced visibility.
	for _, name := range []string{
		"geometry.area", "geometry.perimeter", "geometry.Shape",
		"geometry._Internal", "geometry._make_internal",
		"requests.get", "requests.post", "requests.Response",
		"app.App", "app.create",
		"service.Service", "service.boot", "service.run",
		"service.helper",
		"models.User", "models.Account",
		"text.slugify", "text._private_helper",
		"config.load", "config.reset", "config.save",
		"arithmetic.add", "arithmetic.helper",
	} {
		assert.Truef(t, got[name],
			"expected Python definition %q to be streamed", name)
	}

	// A keep predicate filters streamed names just like the Go path.
	kept := make(chan string, 64)
	keepDone := make(chan error, 1)
	go func() {
		defer close(kept)
		keepDone <- symbolresolve.SearchDefinitions(
			context.Background(), env.parser, symbolresolve.Python, env.qc,
			packages, kept, func(name string) bool {
				return !strings.HasPrefix(name, "_")
			},
		)
	}()
	keptSet := make(map[string]bool)
	for s := range kept {
		keptSet[s] = true
	}
	require.NoError(t, <-keepDone)
	assert.True(t, keptSet["geometry.area"])
	assert.False(t, keptSet["geometry._Internal"],
		"keep predicate must drop underscore names")
	assert.False(t, keptSet["text._private_helper"])
}

func setupPythonEnv(t *testing.T) *resolveEnv {
	return setupPythonEnvAt(t, "testdata_py")
}

// setupPythonEnvAt builds a resolve environment over one of the
// committed Python fixture trees.
func setupPythonEnvAt(t *testing.T, testdata string) *resolveEnv {
	t.Helper()

	root := t.TempDir()
	copyDirT(t, testdata, root)

	rootURI := "file://" + root
	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	parser := treesitter.NewParser(scheme, pythonPkgManager(t), uri)
	return &resolveEnv{
		root: root, parser: parser,
		qc: symbolresolve.NewQualifierContext(scheme, uri),
	}
}

func pythonPkgManager(t *testing.T) syntax.PkgManager {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	pyDir := filepath.Join(wd, "..", "..", "syntax", "syntaxtest", "python")
	files := []string{
		grammarfixture.ParserPath(t, pyDir),
		filepath.Join(pyDir, "locals.scm"),
		filepath.Join(pyDir, "highlights.scm"),
		filepath.Join(pyDir, "indents.scm"),
		filepath.Join(pyDir, "folds.scm"),
	}
	for _, p := range files {
		_, err := os.Stat(p)
		require.NoErrorf(t, err, "missing tree-sitter fixture %s", p)
	}
	return &stubPkgManager{files: files}
}
