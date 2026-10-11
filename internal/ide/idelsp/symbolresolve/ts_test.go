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
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

func TestResolveTypeScriptE2E(t *testing.T) {
	t.Parallel()

	env := setupTSEnv(t)

	mainURI := env.fileURI("src/main.ts")
	geometryURI := env.fileURI("src/geometry.ts")
	typesURI := env.fileURI("src/types.d.ts")
	brokenURI := env.fileURI("src/broken.ts")
	serviceURI := env.fileURI("src/app/service.ts")
	appConfigURI := env.fileURI("src/app/config.ts")
	utilsConfigURI := env.fileURI("src/utils/config.ts")
	textURI := env.fileURI("src/utils/text.ts")
	utilsIndexURI := env.fileURI("src/utils/index.ts")
	arithmeticURI := env.fileURI("src/calc/arithmetic.ts")
	vendorRequestsURI := env.fileURI("src/vendor/requests.d.ts")
	badgeURI := env.fileURI("src/components/Badge.tsx")
	jsRequestsURI := env.fileURI("src/legacy/requests.js")
	clientURI := env.fileURI("src/legacy/client.js")
	loaderURI := env.fileURI("src/legacy/loader.cjs")
	legacyIndexURI := env.fileURI("src/legacy/index.js")
	widgetURI := env.fileURI("src/legacy/Widget.jsx")

	tests := []struct {
		name            string
		spec            *symbolresolve.Spec
		symbol          string
		wantURIs        []string
		wantErrIs       error
		wantErrContains string
	}{
		{
			// The reference phase surfaces the namespace-import call
			// site, not the definition file.
			name:     "namespace import member resolves to call site",
			spec:     symbolresolve.TypeScript,
			symbol:   "geometry.area",
			wantURIs: []string{mainURI},
		},
		{
			// geometry.Shape is both a type annotation and a
			// constructor call in main.ts.
			name:     "qualified type reference resolves to call site",
			spec:     symbolresolve.TypeScript,
			symbol:   "geometry.Shape",
			wantURIs: []string{mainURI},
		},
		{
			name:     "arrow function constant resolves via module stem",
			spec:     symbolresolve.TypeScript,
			symbol:   "geometry.perimeter",
			wantURIs: []string{geometryURI},
		},
		{
			name:     "interface resolves via module stem",
			spec:     symbolresolve.TypeScript,
			symbol:   "geometry.Sized",
			wantURIs: []string{geometryURI},
		},
		{
			name:     "type alias resolves via module stem",
			spec:     symbolresolve.TypeScript,
			symbol:   "geometry.Point",
			wantURIs: []string{geometryURI},
		},
		{
			name:     "enum resolves via module stem",
			spec:     symbolresolve.TypeScript,
			symbol:   "geometry.Unit",
			wantURIs: []string{geometryURI},
		},
		{
			// export is not observed, so module-private declarations
			// still resolve through the definitions phase.
			name:     "unexported class is still resolvable",
			spec:     symbolresolve.TypeScript,
			symbol:   "geometry.Internal",
			wantURIs: []string{geometryURI},
		},
		{
			name:     "unexported function is still resolvable",
			spec:     symbolresolve.TypeScript,
			symbol:   "geometry.makeInternal",
			wantURIs: []string{geometryURI},
		},
		{
			name:     "class method resolves to definition",
			spec:     symbolresolve.TypeScript,
			symbol:   "geometry.Shape.scale",
			wantURIs: []string{geometryURI},
		},
		{
			name:     "concrete method of an abstract class resolves",
			spec:     symbolresolve.TypeScript,
			symbol:   "geometry.Polygon.describe",
			wantURIs: []string{geometryURI},
		},
		{
			name:            "method on unknown class returns not-found",
			spec:            symbolresolve.TypeScript,
			symbol:          "geometry.Other.scale",
			wantErrContains: "no symbols found",
		},
		{
			name:            "missing class method returns not-found",
			spec:            symbolresolve.TypeScript,
			symbol:          "geometry.Shape.missing",
			wantErrContains: "no symbols found",
		},
		{
			// main.ts binds requests with import-require.
			name:     "import-require member resolves to call site",
			spec:     symbolresolve.TypeScript,
			symbol:   "requests.get",
			wantURIs: []string{mainURI},
		},
		{
			// A .d.ts file is named after its module, without the
			// declaration suffix.
			name:     "declaration file class resolves via module stem",
			spec:     symbolresolve.TypeScript,
			symbol:   "requests.Response",
			wantURIs: []string{vendorRequestsURI},
		},
		{
			name:     "declaration file function resolves via module stem",
			spec:     symbolresolve.TypeScript,
			symbol:   "types.configure",
			wantURIs: []string{typesURI},
		},
		{
			name:     "constructed class resolves to call site",
			spec:     symbolresolve.TypeScript,
			symbol:   "service.Service",
			wantURIs: []string{mainURI},
		},
		{
			name:     "definition-only resolves to nested module file",
			spec:     symbolresolve.TypeScript,
			symbol:   "service.boot",
			wantURIs: []string{serviceURI},
		},
		{
			// main.ts and app/service.ts import text through different
			// relative paths, so both call sites survive import dedup.
			name:     "helper referenced from multiple modules",
			spec:     symbolresolve.TypeScript,
			symbol:   "text.slugify",
			wantURIs: []string{mainURI, serviceURI},
		},
		{
			name:     "private helper resolves via definitions phase",
			spec:     symbolresolve.TypeScript,
			symbol:   "text.privateHelper",
			wantURIs: []string{textURI},
		},
		{
			// load is defined in both utils/config.ts and
			// app/config.ts, which share the module name "config".
			name:     "ambiguous module name surfaces both definition files",
			spec:     symbolresolve.TypeScript,
			symbol:   "config.load",
			wantURIs: []string{utilsConfigURI, appConfigURI},
		},
		{
			name:     "ambiguous module disambiguated by symbol name (utils)",
			spec:     symbolresolve.TypeScript,
			symbol:   "config.reset",
			wantURIs: []string{utilsConfigURI},
		},
		{
			name:     "ambiguous module disambiguated by symbol name (app)",
			spec:     symbolresolve.TypeScript,
			symbol:   "config.save",
			wantURIs: []string{appConfigURI},
		},
		{
			// An index file is named after its directory, and its
			// export clauses are the directory's public definitions.
			name:     "index re-export resolves to the index file",
			spec:     symbolresolve.TypeScript,
			symbol:   "utils.slugify",
			wantURIs: []string{utilsIndexURI},
		},
		{
			name:     "aliased index re-export resolves by its alias",
			spec:     symbolresolve.TypeScript,
			symbol:   "utils.loadConfig",
			wantURIs: []string{utilsIndexURI},
		},
		{
			name:            "aliased index re-export hides the original name",
			spec:            symbolresolve.TypeScript,
			symbol:          "utils.load",
			wantErrContains: "no symbols found",
		},
		{
			name:     "index file definition resolves via directory name",
			spec:     symbolresolve.TypeScript,
			symbol:   "utils.describe",
			wantURIs: []string{utilsIndexURI},
		},
		{
			name:     "homonymous symbol in another module is isolated",
			spec:     symbolresolve.TypeScript,
			symbol:   "arithmetic.helper",
			wantURIs: []string{arithmeticURI},
		},
		{
			name:            "valid symbol in wrong module returns not-found",
			spec:            symbolresolve.TypeScript,
			symbol:          "text.helper",
			wantErrContains: "no symbols found",
		},
		{
			name:     "declaration before a syntax error still resolves",
			spec:     symbolresolve.TypeScript,
			symbol:   "broken.stable",
			wantURIs: []string{brokenURI},
		},
		{
			// TSX files belong to the tsx spec alone.
			name:            "tsx definition is not visible to the typescript spec",
			spec:            symbolresolve.TypeScript,
			symbol:          "Badge.Badge",
			wantErrContains: "no symbols found",
		},
		{
			name:     "tsx namespace import member resolves to call site",
			spec:     symbolresolve.TSX,
			symbol:   "geometry.area",
			wantURIs: []string{badgeURI},
		},
		{
			name:     "tsx component resolves via module stem",
			spec:     symbolresolve.TSX,
			symbol:   "Badge.Badge",
			wantURIs: []string{badgeURI},
		},
		{
			name:     "tsx class method resolves to definition",
			spec:     symbolresolve.TSX,
			symbol:   "Badge.Panel.render",
			wantURIs: []string{badgeURI},
		},
		{
			name:            "typescript definition is not visible to the tsx spec",
			spec:            symbolresolve.TSX,
			symbol:          "geometry.perimeter",
			wantErrContains: "no symbols found",
		},
		{
			name:     "javascript namespace import member resolves to call site",
			spec:     symbolresolve.JavaScript,
			symbol:   "requests.get",
			wantURIs: []string{clientURI},
		},
		{
			// loader.cjs binds requests with a CommonJS require().
			name:     "commonjs require member resolves to call site",
			spec:     symbolresolve.JavaScript,
			symbol:   "requests.Response",
			wantURIs: []string{loaderURI},
		},
		{
			name:     "javascript private function resolves via module stem",
			spec:     symbolresolve.JavaScript,
			symbol:   "requests.retry",
			wantURIs: []string{jsRequestsURI},
		},
		{
			name:     "javascript class method resolves to definition",
			spec:     symbolresolve.JavaScript,
			symbol:   "requests.Response.json",
			wantURIs: []string{jsRequestsURI},
		},
		{
			name:     "javascript aliased index re-export resolves",
			spec:     symbolresolve.JavaScript,
			symbol:   "legacy.fetch",
			wantURIs: []string{legacyIndexURI},
		},
		{
			name:     "javascript index re-export resolves",
			spec:     symbolresolve.JavaScript,
			symbol:   "legacy.fetchAll",
			wantURIs: []string{legacyIndexURI},
		},
		{
			name:     "jsx component resolves via module stem",
			spec:     symbolresolve.JavaScript,
			symbol:   "Widget.Widget",
			wantURIs: []string{widgetURI},
		},
		{
			name:            "missing symbol returns not-found",
			spec:            symbolresolve.TypeScript,
			symbol:          "geometry.DoesNotExist",
			wantErrContains: "no symbols found",
		},
		{
			name:      "name without dot returns ErrNoDot",
			spec:      symbolresolve.TypeScript,
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
				specIter(tt.spec), tt.symbol, rec,
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
			assert.NotEmpty(t, rec.events,
				"Resolve must report progress on success")
			assert.ElementsMatchf(t, tt.wantURIs, uriStrings(matches),
				"resolved URIs mismatch; matches=%+v", matches)
		})
	}
}

func TestListReferencesTypeScriptE2E(t *testing.T) {
	t.Parallel()

	env := setupTSEnv(t)

	got := listReferences(t, env.parser, env.qc, iterator.FromSlice([]symbolresolve.Spec{
		*symbolresolve.TypeScript, *symbolresolve.TSX, *symbolresolve.JavaScript,
	}))

	for _, name := range []string{
		"geometry.area", "geometry.Shape", "requests.get", "service.Service",
		"text.slugify", "requests.Response",
		"geometry.perimeter", "geometry.Shape.scale", "types.configure",
		"utils.slugify", "utils.loadConfig", "utils.describe",
		"Badge.Badge", "Badge.Panel.render", "legacy.fetch", "Widget.Widget",
	} {
		assert.Truef(t, got[name], "expected %q to be listed", name)
	}

	// Member reads on values that no import binds are not module
	// references.
	for _, name := range []string{"console.log", "shape.size", "urls.map"} {
		assert.Falsef(t, got[name], "%q must not be listed", name)
	}
	assert.False(t, got["utils.load"], "an aliased re-export must not list its source name")
}

func TestResolveTypeScriptConcurrent(t *testing.T) {
	t.Parallel()

	env := setupTSEnv(t)

	symbols := []string{
		"geometry.area", "geometry.Shape", "geometry.perimeter",
		"requests.get", "service.boot", "text.slugify",
		"config.load", "utils.slugify", "geometry.Shape.scale",
	}

	var wg sync.WaitGroup
	wg.Add(len(symbols))
	for _, s := range symbols {
		go func(symbol string) {
			defer wg.Done()
			matches, err := symbolresolve.Resolve(
				context.Background(), env.parser, env.qc,
				specIter(symbolresolve.TypeScript), symbol, nil,
			)
			assert.NoErrorf(t, err, "Resolve(%q)", symbol)
			assert.NotEmptyf(t, matches, "Resolve(%q)", symbol)
		}(s)
	}
	wg.Wait()
}

func setupTSEnv(t *testing.T) *resolveEnv {
	t.Helper()

	root := t.TempDir()
	copyDirT(t, "testdata_ts", root)

	uri, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	parser := treesitter.NewParser(scheme, tsPkgManager(t), uri)
	return &resolveEnv{
		root: root, parser: parser,
		qc: symbolresolve.NewQualifierContext(scheme, uri),
	}
}

// tsLibDirs serves each ECMAScript grammar fixture under its own
// language id, since one workspace mixes all three.
type tsLibDirs map[string][]string

func (m tsLibDirs) LibDir(
	_ context.Context, langID string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice(m[langID]), nil
}

func tsPkgManager(t *testing.T) tsLibDirs {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	dirs := make(tsLibDirs)
	for _, langID := range []string{"typescript", "tsx", "javascript"} {
		langDir := filepath.Join(wd, "..", "..", "syntax", "syntaxtest", langID)
		files := grammarfixture.LibDir(t, langDir)
		require.Lenf(t, files, 5, "incomplete tree-sitter fixture %s", langDir)
		dirs[langID] = files
	}
	return dirs
}
