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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"

	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

func TestResolveCppE2E(t *testing.T) {
	t.Parallel()

	env := setupCppEnv(t)

	mainURI := env.fileURI("src/main.cpp")
	shapeHpp := env.fileURI("include/geo/shape.hpp")
	shapeCpp := env.fileURI("src/shape.cpp")
	serviceHpp := env.fileURI("app/service.hpp")
	serviceCpp := env.fileURI("app/service.cpp")
	textCpp := env.fileURI("utils/text.cpp")
	utilsConfigCpp := env.fileURI("utils/config.cpp")
	appConfigCpp := env.fileURI("app/config.cpp")
	nestedCpp := env.fileURI("app/nested.cpp")
	brokenCpp := env.fileURI("src/broken.cpp")

	tests := []struct {
		name string
		// symbol is passed to Resolve using a dot separator in place
		// of C++'s `::`.
		symbol          string
		wantURIs        []string
		wantURICount    int
		wantErrIs       error
		wantErrContains string
	}{
		{
			// geo::area is referenced from main.cpp; the reference phase
			// surfaces the call site and not the definition file.
			name:     "qualified function reference resolves to call site",
			symbol:   "geo.area",
			wantURIs: []string{mainURI},
		},
		{
			// geo::Shape appears as a type in main.cpp, including inside
			// the std::vector template argument.
			name:     "qualified type reference resolves to use site",
			symbol:   "geo.Shape",
			wantURIs: []string{mainURI},
		},
		{
			name:     "qualified constant reference resolves to use site",
			symbol:   "geo.kDimensions",
			wantURIs: []string{mainURI},
		},
		{
			// std::vector<geo::Shape> is a template type reference.
			name:     "template type reference resolves to use site",
			symbol:   "std.vector",
			wantURIs: []string{mainURI},
		},
		{
			// Shape::unit() is a qualified reference to a static member
			// at its call site in main.cpp and at its out-of-line
			// definition in shape.cpp.
			name:     "scoped static member resolves to call and definition sites",
			symbol:   "Shape.unit",
			wantURIs: []string{mainURI, shapeCpp},
		},
		{
			// perimeter is never referenced as geo::perimeter, so the
			// definitions phase surfaces the files declaring namespace
			// geo that define it.
			name:     "definition-only function resolves via its namespace",
			symbol:   "geo.hidden",
			wantURIs: []string{shapeCpp},
		},
		{
			// Point is only declared in the header.
			name:     "struct declared in the header resolves there",
			symbol:   "geo.Point",
			wantURIs: []string{mainURI},
		},
		{
			name:     "enum class resolves via its namespace",
			symbol:   "geo.Kind",
			wantURIs: []string{mainURI},
		},
		{
			name:     "type alias resolves via its namespace",
			symbol:   "geo.Side",
			wantURIs: []string{shapeHpp},
		},
		{
			// Widget's class body precedes the syntax error.
			name:     "definition before a syntax error still resolves",
			symbol:   "geo.Widget",
			wantURIs: []string{brokenCpp},
		},
		{
			name:     "class reference resolves to use site",
			symbol:   "app.Service",
			wantURIs: []string{mainURI},
		},
		{
			name:     "helper referenced from multiple units",
			symbol:   "utils.slugify",
			wantURIs: []string{mainURI, serviceCpp},
		},
		{
			name:     "static helper resolves via definitions phase",
			symbol:   "utils.private_helper",
			wantURIs: []string{textCpp},
		},
		{
			// load is defined in both utils/config.cpp and
			// app/config.cpp inside namespace config, so the ambiguous
			// lookup surfaces both.
			name:         "ambiguous namespace surfaces both definition files",
			symbol:       "config.load",
			wantURIs:     []string{utilsConfigCpp, appConfigCpp},
			wantURICount: 2,
		},
		{
			name:     "ambiguous namespace disambiguated by symbol name (utils)",
			symbol:   "config.reset",
			wantURIs: []string{utilsConfigCpp},
		},
		{
			name:     "ambiguous namespace disambiguated by symbol name (app)",
			symbol:   "config.save",
			wantURIs: []string{appConfigCpp},
		},
		{
			// namespace app::net qualifies the file as net.
			name:     "nested namespace definition qualifies by innermost segment",
			symbol:   "net.connect",
			wantURIs: []string{nestedCpp},
		},
		{
			// detail::helper() is defined out of line in service.cpp,
			// where detail:: is a qualified reference.
			name:     "nested namespace member resolves to its qualified definition",
			symbol:   "detail.helper",
			wantURIs: []string{serviceCpp},
		},
		{
			// main.cpp declares no namespace, so its stem qualifies it.
			name:     "file outside any namespace resolves via its stem",
			symbol:   "main.run",
			wantURIs: []string{mainURI},
		},
		{
			name:            "missing symbol returns not-found",
			symbol:          "geo.DoesNotExist",
			wantErrContains: "no symbols found",
		},
		{
			// ns.Type.method resolves to the in-class declaration and
			// the out-of-line definition.
			name:     "method resolves to declaration and definition",
			symbol:   "geo.Shape.area",
			wantURIs: []string{shapeHpp, shapeCpp},
		},
		{
			name:     "in-class method definition resolves",
			symbol:   "geo.Point.manhattan",
			wantURIs: []string{shapeHpp},
		},
		{
			name:     "member template resolves to declaration and definition",
			symbol:   "geo.Shape.scale",
			wantURIs: []string{shapeHpp, shapeCpp},
		},
		{
			name:     "method of a class in another namespace resolves",
			symbol:   "app.Service.boot",
			wantURIs: []string{serviceHpp, serviceCpp},
		},
		{
			name:            "method on unknown type returns not-found",
			symbol:          "geo.Other.area",
			wantErrContains: "no symbols found",
		},
		{
			name:            "missing method returns not-found",
			symbol:          "geo.Shape.missing",
			wantErrContains: "no symbols found",
		},
		{
			name:            "valid symbol in wrong namespace returns not-found",
			symbol:          "utils.area",
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
				specIter(symbolresolve.Cpp), tt.symbol, rec,
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

func TestResolveCppConcurrent(t *testing.T) {
	t.Parallel()

	env := setupCppEnv(t)

	symbols := []string{
		"geo.area", "geo.Shape", "geo.hidden", "app.Service",
		"utils.slugify", "config.load", "geo.Shape.area", "main.run",
	}

	var wg sync.WaitGroup
	wg.Add(len(symbols))
	for _, s := range symbols {
		go func(symbol string) {
			defer wg.Done()
			matches, err := symbolresolve.Resolve(
				context.Background(), env.parser, env.qc,
				specIter(symbolresolve.Cpp), symbol, nil,
			)
			assert.NoErrorf(t, err, "Resolve(%q)", symbol)
			assert.NotEmptyf(t, matches, "Resolve(%q)", symbol)
		}(s)
	}
	wg.Wait()
}

func TestExtractFileCpp(t *testing.T) {
	t.Parallel()

	env := setupCppEnv(t)

	t.Run("qualified refs from main.cpp", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.Cpp, "src/main.cpp")

		// #include is not an import binding: nothing to resolve.
		assert.Empty(t, ext.Imports)
		for _, want := range []occurrence{
			{"geo.Shape", symbolresolve.SymbolRef},
			{"geo.area", symbolresolve.SymbolRef},
			{"geo.Point", symbolresolve.SymbolRef},
			{"geo.Kind", symbolresolve.SymbolRef},
			{"Kind.Square", symbolresolve.SymbolRef},
			{"std.vector", symbolresolve.SymbolRef},
			{"Shape.unit", symbolresolve.SymbolRef},
			{"app.Service", symbolresolve.SymbolRef},
			{"utils.slugify", symbolresolve.SymbolRef},
			{"geo.kDimensions", symbolresolve.SymbolRef},
			// No namespace: the stem qualifies the definitions.
			{"main.run", symbolresolve.SymbolDef},
			{"main.main", symbolresolve.SymbolDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
		// Member access on objects is not a qualified reference.
		for _, unwanted := range []occurrence{
			{"p.manhattan", symbolresolve.SymbolRef},
			{"s.area", symbolresolve.SymbolRef},
			{"svc.boot", symbolresolve.SymbolRef},
		} {
			assert.Zerof(t, occs[unwanted], "unexpected %+v", unwanted)
		}
	})

	t.Run("defs and methods from shape.hpp", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Cpp, "include/geo/shape.hpp")

		for _, want := range []occurrence{
			{"geo.Shape", symbolresolve.SymbolDef},
			{"geo.Point", symbolresolve.SymbolDef},
			{"geo.Kind", symbolresolve.SymbolDef},
			{"geo.Side", symbolresolve.SymbolDef},
			{"geo.area", symbolresolve.SymbolDef},
			{"geo.Shape.Shape", symbolresolve.SymbolMethodDef},
			{"geo.Shape.area", symbolresolve.SymbolMethodDef},
			{"geo.Shape.perimeter", symbolresolve.SymbolMethodDef},
			{"geo.Shape.unit", symbolresolve.SymbolMethodDef},
			{"geo.Shape.scale", symbolresolve.SymbolMethodDef},
			{"geo.Point.manhattan", symbolresolve.SymbolMethodDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
	})

	t.Run("out-of-line definitions from shape.cpp", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Cpp, "src/shape.cpp")

		for _, want := range []occurrence{
			// `Shape::area` at the definition is also a qualified
			// reference, which is how `Shape.area` jumps here.
			{"Shape.area", symbolresolve.SymbolRef},
			{"geo.Shape.area", symbolresolve.SymbolMethodDef},
			{"geo.Shape.perimeter", symbolresolve.SymbolMethodDef},
			{"geo.Shape.unit", symbolresolve.SymbolMethodDef},
			{"geo.Shape.scale", symbolresolve.SymbolMethodDef},
			{"geo.area", symbolresolve.SymbolDef},
			{"geo.hidden", symbolresolve.SymbolDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
	})

	t.Run("nested namespace definition qualifies by innermost segment", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Cpp, "app/nested.cpp")

		assert.NotZero(t, occs[occurrence{"net.connect", symbolresolve.SymbolDef}])
		assert.Zero(t, occs[occurrence{"app.connect", symbolresolve.SymbolDef}])
	})
}

func setupCppEnv(t *testing.T) *resolveEnv {
	t.Helper()

	root := t.TempDir()
	copyDirT(t, "testdata_cpp", root)

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
