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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"

	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

func TestResolveRustModuleE2E(t *testing.T) {
	root := rustModuleWorkspace(t)
	parser := newFileSchemeParser(t, root)

	src := func(rel string) string {
		return "file://" + filepath.Join(root, "src", rel)
	}
	mainRS := src("main.rs")
	geometryRS := src("geometry.rs")
	shapesRS := src("shapes.rs")

	tests := []struct {
		name string
		// symbol uses a dot separator; ResolveSymbol splits on the first
		// dot while Rust source uses `module::Symbol`.
		symbol          string
		wantURIs        []string
		wantErr         bool
		wantErrNotNoDot bool
	}{
		{
			// geometry::area is referenced from main.rs (both as a path
			// call and via the `use` alias); the reference phase surfaces
			// the call site rather than the definition.
			name:     "qualified function reference resolves to call site",
			symbol:   "geometry.area",
			wantURIs: []string{mainRS},
		},
		{
			// circumference is defined in geometry.rs and referenced from
			// shapes.rs as geometry::circumference.
			name:     "qualified reference across modules resolves to caller",
			symbol:   "geometry.circumference",
			wantURIs: []string{shapesRS},
		},
		{
			// Point is never referenced qualified, so the definitions
			// phase surfaces its declaration via the module file stem.
			name:     "definition-only type resolves via module stem",
			symbol:   "geometry.Point",
			wantURIs: []string{geometryRS},
		},
		{
			// Circle::new is referenced from main.rs via shapes::Circle.
			name:     "scoped type reference resolves to call site",
			symbol:   "shapes.Circle",
			wantURIs: []string{mainRS},
		},
		{
			// Rectangle is declared but never referenced; definitions
			// phase surfaces shapes.rs.
			name:     "unreferenced type resolves to its module file",
			symbol:   "shapes.Rectangle",
			wantURIs: []string{shapesRS},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			matches, err := resolveAll(t, parser, tt.symbol)
			if tt.wantErr {
				require.Error(t, err)
				if tt.wantErrNotNoDot {
					assert.False(t, errors.Is(err, syntaxapi.ErrNoDot))
				}
				return
			}
			require.NoError(t, err)
			require.NotEmpty(t, matches)
			assert.ElementsMatchf(t, tt.wantURIs, matchURIs(matches),
				"resolved URIs mismatch; matches=%+v", matches)
		})
	}
}

func TestResolveRustModuleNotFoundE2E(t *testing.T) {
	root := rustModuleWorkspace(t)
	parser := newFileSchemeParser(t, root)

	t.Run("missing symbol yields not found", func(t *testing.T) {
		_, err := resolveAll(t, parser, "geometry.DoesNotExist")
		require.Error(t, err)
		assert.False(t, errors.Is(err, syntaxapi.ErrNoDot))
	})

	t.Run("name without dot returns ErrNoDot", func(t *testing.T) {
		_, err := parser.ResolveSymbol(context.Background(), "NoDot", nil)
		assert.ErrorIs(t, err, syntaxapi.ErrNoDot)
	})
}

// rustModuleWorkspace copies the on-disk Rust crate fixture into a fresh
// temp dir so the FileScheme indexes a real, writable workspace.
func rustModuleWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyTree(t, "testdata_rust_module", root)
	return root
}

// newFileSchemeParser builds a parser backed by a real FileScheme rooted at
// root and the on-disk Rust tree-sitter assets under ./rust.
func newFileSchemeParser(t *testing.T, root string) syntaxapi.Parser {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	return treesitter.NewParser(scheme, langPkgManager{wd: wd}, uri)
}

func matchURIs(matches []syntaxapi.Match) []string {
	uris := make([]string, len(matches))
	for i, m := range matches {
		uris[i] = m.URI
	}
	return uris
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			require.NoError(t, os.MkdirAll(dstPath, 0o755))
			copyTree(t, srcPath, dstPath)
			continue
		}
		data, err := os.ReadFile(srcPath)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(dstPath, data, 0o644))
	}
}
