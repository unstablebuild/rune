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

func TestResolveCE2E(t *testing.T) {
	t.Parallel()

	env := setupCEnv(t)

	greeterC := env.fileURI("greeter.c")
	textC := env.fileURI("utils/text.c")
	utilsConfigC := env.fileURI("utils/config.c")
	appConfigC := env.fileURI("app/config.c")
	arithmeticC := env.fileURI("calc/arithmetic.c")
	brokenC := env.fileURI("calc/broken.c")

	tests := []struct {
		name string
		// symbol is passed to Resolve using a dot separator. C has no
		// qualified references, so the qualifier is the file stem.
		symbol          string
		wantURIs        []string
		wantURICount    int
		wantErrIs       error
		wantErrContains string
	}{
		{
			// greeter.c defines greet; greeter.h only declares it and
			// parses as C++, so the C spec surfaces the definition alone.
			name:     "function resolves to its defining source file",
			symbol:   "greeter.greet",
			wantURIs: []string{greeterC},
		},
		{
			name:     "static function is still resolvable",
			symbol:   "greeter.decorate",
			wantURIs: []string{greeterC},
		},
		{
			name:     "function-like macro resolves as a function",
			symbol:   "greeter.SQUARE",
			wantURIs: []string{greeterC},
		},
		{
			name:     "enum resolves as a type",
			symbol:   "greeter.greeter_kind",
			wantURIs: []string{greeterC},
		},
		{
			name:     "typedef resolves as a type",
			symbol:   "greeter.greeter_t",
			wantURIs: []string{greeterC},
		},
		{
			name:     "union resolves as a type",
			symbol:   "greeter.greeter_payload",
			wantURIs: []string{greeterC},
		},
		{
			// `struct greeter *g` in greeter.c is a use of the type; its
			// body lives in the header, which belongs to the Cpp spec.
			name:            "struct without a body in this unit is not a definition",
			symbol:          "greeter.greeter",
			wantErrContains: "no symbols found",
		},
		{
			name:     "helper in a nested directory resolves via its stem",
			symbol:   "text.slugify",
			wantURIs: []string{textC},
		},
		{
			name:     "private helper resolves via definitions phase",
			symbol:   "text.private_helper",
			wantURIs: []string{textC},
		},
		{
			// config_load is defined in both utils/config.c and
			// app/config.c; both share the stem config, so the ambiguous
			// lookup surfaces both.
			name:         "ambiguous stem surfaces both definition files",
			symbol:       "config.config_load",
			wantURIs:     []string{utilsConfigC, appConfigC},
			wantURICount: 2,
		},
		{
			name:     "ambiguous stem disambiguated by symbol name (utils)",
			symbol:   "config.config_reset",
			wantURIs: []string{utilsConfigC},
		},
		{
			name:     "ambiguous stem disambiguated by symbol name (app)",
			symbol:   "config.config_save",
			wantURIs: []string{appConfigC},
		},
		{
			name:     "homonymous static helper in another unit is isolated",
			symbol:   "arithmetic.helper",
			wantURIs: []string{arithmeticC},
		},
		{
			name:     "definition before a syntax error still resolves",
			symbol:   "broken.before_error",
			wantURIs: []string{brokenC},
		},
		{
			name:            "missing symbol returns not-found",
			symbol:          "greeter.DoesNotExist",
			wantErrContains: "no symbols found",
		},
		{
			name:            "valid symbol in wrong unit returns not-found",
			symbol:          "text.helper",
			wantErrContains: "no symbols found",
		},
		{
			// C has no methods: a three-part name has no reading.
			name:            "three-part name returns not-found",
			symbol:          "greeter.greeter.greet",
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
				specIter(symbolresolve.C), tt.symbol, rec,
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

func TestResolveCHeaderDeclarationThroughCpp(t *testing.T) {
	t.Parallel()

	env := setupCEnv(t)

	// A workspace detects both specs: greeter.h parses as C++ and the
	// resolver tries Cpp before C, so the header's declaration wins when
	// the lookup is not scoped to a spec.
	matches, err := symbolresolve.Resolve(
		context.Background(), env.parser, env.qc,
		iterator.FromSlice([]symbolresolve.Spec{*symbolresolve.Cpp, *symbolresolve.C}),
		"greeter.greet", nil,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{env.fileURI("greeter.h")}, uriStrings(matches))

	// The struct body is in the header, so the type resolves there.
	matches, err = symbolresolve.Resolve(
		context.Background(), env.parser, env.qc,
		specIter(symbolresolve.Cpp), "greeter.greeter", nil,
	)
	require.NoError(t, err)
	assert.Equal(t, []string{env.fileURI("greeter.h")}, uriStrings(matches))
}

func TestResolveCConcurrent(t *testing.T) {
	t.Parallel()

	env := setupCEnv(t)

	symbols := []string{
		"greeter.greet", "greeter.greeter_count", "greeter.greeter_t",
		"text.slugify", "config.config_load", "arithmetic.add",
	}

	var wg sync.WaitGroup
	wg.Add(len(symbols))
	for _, s := range symbols {
		go func(symbol string) {
			defer wg.Done()
			matches, err := symbolresolve.Resolve(
				context.Background(), env.parser, env.qc,
				specIter(symbolresolve.C), symbol, nil,
			)
			assert.NoErrorf(t, err, "Resolve(%q)", symbol)
			assert.NotEmptyf(t, matches, "Resolve(%q)", symbol)
		}(s)
	}
	wg.Wait()
}

func TestExtractFileC(t *testing.T) {
	t.Parallel()

	env := setupCEnv(t)

	t.Run("defs from greeter.c", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.C, "greeter.c")

		// C has no imports to resolve and no qualified references.
		assert.Empty(t, ext.Imports)
		for _, want := range []occurrence{
			{"greeter.greet", symbolresolve.SymbolDef},
			{"greeter.greeter_count", symbolresolve.SymbolDef},
			{"greeter.decorate", symbolresolve.SymbolDef},
			{"greeter.SQUARE", symbolresolve.SymbolDef},
			{"greeter.greeter_kind", symbolresolve.SymbolDef},
			{"greeter.greeter_t", symbolresolve.SymbolDef},
			{"greeter.greeter_payload", symbolresolve.SymbolDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
		for o := range occs {
			assert.Equalf(t, symbolresolve.SymbolDef, o.kind,
				"unexpected non-definition %+v", o)
		}
	})

	t.Run("field access in main.c is not a reference", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.C, "main.c")

		for o := range occs {
			assert.NotEqualf(t, symbolresolve.SymbolRef, o.kind,
				"unexpected reference %+v", o)
		}
		assert.NotZero(t, occs[occurrence{"main.main", symbolresolve.SymbolDef}])
	})

	t.Run("header extracts through the cpp spec", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Cpp, "greeter.h")

		for _, want := range []occurrence{
			{"greeter.greet", symbolresolve.SymbolDef},
			{"greeter.greeter_count", symbolresolve.SymbolDef},
			{"greeter.greeter", symbolresolve.SymbolDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
	})
}

func setupCEnv(t *testing.T) *resolveEnv {
	t.Helper()

	root := t.TempDir()
	copyDirT(t, "testdata_c", root)

	uri, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	parser := treesitter.NewParser(scheme, ccppPkgManager(t), uri)
	return &resolveEnv{
		root: root, parser: parser,
		qc: symbolresolve.NewQualifierContext(scheme, uri),
	}
}

// ccppLibDirs serves the C and C++ grammar fixtures under their own
// language ids: headers parse as C++ even in a C workspace.
type ccppLibDirs map[string][]string

func (m ccppLibDirs) LibDir(
	_ context.Context, langID string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice(m[langID]), nil
}

func ccppPkgManager(t *testing.T) ccppLibDirs {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	dirs := make(ccppLibDirs)
	for _, langID := range []string{"c", "cpp"} {
		langDir := filepath.Join(wd, "..", "..", "syntax", "syntaxtest", langID)
		files := grammarfixture.LibDir(t, langDir)
		require.Lenf(t, files, 5, "incomplete tree-sitter fixture %s", langDir)
		dirs[langID] = files
	}
	return dirs
}
