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

	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

func TestResolveZigE2E(t *testing.T) {
	t.Parallel()

	env := setupZigEnv(t)

	mainURI := env.fileURI("main.zig")
	geometryURI := env.fileURI("geometry.zig")
	requestsURI := env.fileURI("requests.zig")
	serviceURI := env.fileURI("app/service.zig")
	textURI := env.fileURI("utils/text.zig")
	utilsConfigURI := env.fileURI("utils/config.zig")
	appConfigURI := env.fileURI("app/config.zig")
	arithmeticURI := env.fileURI("calc/arithmetic.zig")

	tests := []struct {
		name string
		// symbol is passed to Resolve using a dot separator, which is
		// also Zig's member-access syntax for imported modules.
		symbol          string
		wantURIs        []string
		wantURICount    int
		wantErrIs       error
		wantErrContains string
	}{
		{
			// geometry.area is referenced from main.zig; the reference
			// phase surfaces the call site and not the definition file.
			name:     "qualified function reference resolves to call site",
			symbol:   "geometry.area",
			wantURIs: []string{mainURI},
		},
		{
			// geometry.Shape appears as a field access in main.zig.
			name:     "qualified type reference resolves to call site",
			symbol:   "geometry.Shape",
			wantURIs: []string{mainURI},
		},
		{
			// perimeter is defined but never referenced as
			// geometry.perimeter; the definitions phase surfaces the
			// declaration via the module file stem.
			name:     "definition-only function resolves via module stem",
			symbol:   "geometry.perimeter",
			wantURIs: []string{geometryURI},
		},
		{
			// Zig visibility is not observed by the resolver, so a
			// private item still resolves through the definitions phase.
			name:     "private struct is still resolvable",
			symbol:   "geometry.Internal",
			wantURIs: []string{geometryURI},
		},
		{
			name:     "private function is still resolvable",
			symbol:   "geometry.make_internal",
			wantURIs: []string{geometryURI},
		},
		{
			name:     "dependency module reference resolves to call site",
			symbol:   "requests.get",
			wantURIs: []string{mainURI},
		},
		{
			name:     "dependency definition resolves to its module file",
			symbol:   "requests.Response",
			wantURIs: []string{requestsURI},
		},
		{
			name:     "class reference resolves to call site",
			symbol:   "service.Service",
			wantURIs: []string{mainURI},
		},
		{
			name:     "definition-only resolves to nested module file",
			symbol:   "service.boot",
			wantURIs: []string{serviceURI},
		},
		{
			// text.slugify is referenced from both main.zig and
			// app/service.zig.
			name:     "helper referenced from multiple modules",
			symbol:   "text.slugify",
			wantURIs: []string{mainURI, serviceURI},
		},
		{
			name:     "private helper resolves via definitions phase",
			symbol:   "text.private_helper",
			wantURIs: []string{textURI},
		},
		{
			// load is defined in BOTH utils/config.zig and
			// app/config.zig; both share the module name "config", so
			// the ambiguous lookup surfaces both definition files.
			name:         "ambiguous module name surfaces both definition files",
			symbol:       "config.load",
			wantURIs:     []string{utilsConfigURI, appConfigURI},
			wantURICount: 2,
		},
		{
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
			// pkg.Type.method resolves to the container method.
			name:     "container method resolves to definition",
			symbol:   "geometry.Shape.init",
			wantURIs: []string{geometryURI},
		},
		{
			// A method on an unknown type returns not-found.
			name:            "method on unknown type returns not-found",
			symbol:          "geometry.Other.init",
			wantErrContains: "no symbols found",
		},
		{
			// A missing method on an existing type returns not-found.
			name:            "missing container method returns not-found",
			symbol:          "geometry.Shape.missing",
			wantErrContains: "no symbols found",
		},
		{
			name:            "valid symbol in wrong module returns not-found",
			symbol:          "text.helper",
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
				specIter(symbolresolve.Zig), tt.symbol, rec,
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

func TestResolveZigConcurrent(t *testing.T) {
	t.Parallel()

	env := setupZigEnv(t)

	symbols := []string{
		"geometry.area", "geometry.Shape", "geometry.perimeter",
		"requests.get", "service.boot", "text.slugify",
		"config.load", "arithmetic.helper",
	}

	var wg sync.WaitGroup
	wg.Add(len(symbols))
	for _, s := range symbols {
		go func(symbol string) {
			defer wg.Done()
			matches, err := symbolresolve.Resolve(
				context.Background(), env.parser, env.qc,
				specIter(symbolresolve.Zig), symbol, nil,
			)
			assert.NoErrorf(t, err, "Resolve(%q)", symbol)
			assert.NotEmptyf(t, matches, "Resolve(%q)", symbol)
		}(s)
	}
	wg.Wait()
}

func setupZigEnv(t *testing.T) *resolveEnv {
	t.Helper()

	root := t.TempDir()
	copyDirT(t, "testdata_zig", root)

	rootURI := "file://" + root
	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	parser := treesitter.NewParser(scheme, zigPkgManager(t), uri)
	return &resolveEnv{
		root: root, parser: parser,
		qc: symbolresolve.NewQualifierContext(scheme, uri),
	}
}

func zigPkgManager(t *testing.T) syntax.PkgManager {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	zigDir := filepath.Join(wd, "..", "..", "syntax", "syntaxtest", "zig")
	files := []string{
		grammarfixture.ParserPath(t, zigDir),
		filepath.Join(zigDir, "locals.scm"),
		filepath.Join(zigDir, "highlights.scm"),
		filepath.Join(zigDir, "indents.scm"),
		filepath.Join(zigDir, "folds.scm"),
	}
	for _, p := range files {
		_, err := os.Stat(p)
		require.NoErrorf(t, err, "missing tree-sitter fixture %s", p)
	}
	return &stubPkgManager{files: files}
}
