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

func TestResolveRustE2E(t *testing.T) {
	t.Parallel()

	env := setupRustEnv(t)

	mainURI := env.fileURI("main.rs")
	geometryURI := env.fileURI("geometry.rs")
	requestsURI := env.fileURI("requests.rs")
	serviceURI := env.fileURI("app/service.rs")
	textURI := env.fileURI("utils/text.rs")
	utilsConfigURI := env.fileURI("utils/config.rs")
	appConfigURI := env.fileURI("app/config.rs")
	arithmeticURI := env.fileURI("calc/arithmetic.rs")

	tests := []struct {
		name string
		// symbol is passed to Resolve using a dot separator; the engine
		// splits on the first dot, while Rust source uses `mod::Symbol`.
		symbol          string
		wantURIs        []string
		wantURICount    int
		wantErrIs       error
		wantErrContains string
	}{
		{
			// geometry::area is referenced from main.rs; the reference
			// phase surfaces the call site and not the definition file.
			name:     "qualified function reference resolves to call site",
			symbol:   "geometry.area",
			wantURIs: []string{mainURI},
		},
		{
			// geometry::Shape appears as a scoped type reference in
			// main.rs.
			name:     "qualified type reference resolves to call site",
			symbol:   "geometry.Shape",
			wantURIs: []string{mainURI},
		},
		{
			// perimeter is defined but never referenced as
			// geometry::perimeter; the definitions phase surfaces the
			// declaration via the module file stem.
			name:     "definition-only function resolves via module stem",
			symbol:   "geometry.perimeter",
			wantURIs: []string{geometryURI},
		},
		{
			// Rust visibility is not observed by the resolver, so a
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
			// text::slugify is referenced from both main.rs and
			// app/service.rs.
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
			// load is defined in BOTH utils/config.rs and app/config.rs;
			// both share the module name "config", so the ambiguous
			// lookup surfaces both definition files.
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
			// pkg.Type.method resolves to the inherent impl method.
			name:     "impl method resolves to definition",
			symbol:   "geometry.Shape.new",
			wantURIs: []string{geometryURI},
		},
		{
			// A method on an unknown type returns not-found.
			name:            "method on unknown type returns not-found",
			symbol:          "geometry.Other.new",
			wantErrContains: "no symbols found",
		},
		{
			// A missing method on an existing type returns not-found.
			name:            "missing impl method returns not-found",
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
				specIter(symbolresolve.Rust), tt.symbol, rec,
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

func TestResolveRustConcurrent(t *testing.T) {
	t.Parallel()

	env := setupRustEnv(t)

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
				specIter(symbolresolve.Rust), symbol, nil,
			)
			assert.NoErrorf(t, err, "Resolve(%q)", symbol)
			assert.NotEmptyf(t, matches, "Resolve(%q)", symbol)
		}(s)
	}
	wg.Wait()
}

func setupRustEnv(t *testing.T) *resolveEnv {
	t.Helper()

	root := t.TempDir()
	copyDirT(t, "testdata_rs", root)

	rootURI := "file://" + root
	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	parser := treesitter.NewParser(scheme, rustPkgManager(t), uri)
	return &resolveEnv{
		root: root, parser: parser,
		qc: symbolresolve.NewQualifierContext(scheme, uri),
	}
}

func rustPkgManager(t *testing.T) syntax.PkgManager {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	rsDir := filepath.Join(wd, "..", "..", "syntax", "syntaxtest", "rust")
	files := []string{
		grammarfixture.ParserPath(t, rsDir),
		filepath.Join(rsDir, "locals.scm"),
		filepath.Join(rsDir, "highlights.scm"),
		filepath.Join(rsDir, "indents.scm"),
		filepath.Join(rsDir, "folds.scm"),
	}
	for _, p := range files {
		_, err := os.Stat(p)
		require.NoErrorf(t, err, "missing tree-sitter fixture %s", p)
	}
	return &stubPkgManager{files: files}
}
