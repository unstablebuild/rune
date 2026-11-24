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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/workspace"
)

func TestExtractFileSvelte(t *testing.T) {
	t.Parallel()

	env := setupSvelteEnv(t, sveltePkgManager(t, "svelte", "typescript", "javascript"))

	t.Run("typescript script block", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.Svelte, "src/Counter.svelte")

		for _, want := range []occurrence{
			// Member and type references through the namespace imports.
			{"geometry.Shape", symbolresolve.SymbolRef},
			{"geometry.area", symbolresolve.SymbolRef},
			{"motion.tweened", symbolresolve.SymbolRef},
			// The component is the module, so its stem qualifies the
			// script's declarations.
			{"Counter.Store", symbolresolve.SymbolDef},
			{"Counter.Step", symbolresolve.SymbolDef},
			{"Counter.increment", symbolresolve.SymbolDef},
			{"Counter.Store.reset", symbolresolve.SymbolMethodDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
		// Expressions in the markup are JavaScript regions of their own,
		// and their member reads resolve against the script's imports.
		assert.NotZero(t, occs[occurrence{"geometry.describe", symbolresolve.SymbolRef}])
		// A default component import binds a bare identifier, and
		// store.reset() reads a local, so neither is a module reference.
		assert.Zero(t, occs[occurrence{"Badge.label", symbolresolve.SymbolRef}])
		assert.Zero(t, occs[occurrence{"store.reset", symbolresolve.SymbolRef}])
		assert.Zero(t, occs[occurrence{"step.by", symbolresolve.SymbolRef}])

		assert.Equal(t, map[string]string{
			"geometry": "./lib/geometry",
			"motion":   "svelte/motion",
		}, ext.Imports)
	})

	t.Run("positions are in file coordinates", func(t *testing.T) {
		ext, _ := extractFileT(t, env, symbolresolve.Svelte, "src/Counter.svelte")

		positions := make(map[string]term.Coordinates)
		for _, s := range ext.Symbols {
			if _, seen := positions[s.Name]; !seen {
				positions[s.Name] = s.Pos
			}
		}
		// `export function increment` is the 22nd line of the component;
		// the region's own coordinates would put it near the top.
		assert.Equal(t, term.Coordinates{Y: 21, X: 18}, positions["Counter.increment"])
		assert.Equal(t, term.Coordinates{Y: 10, X: 4}, positions["Counter.Store.reset"])
		// The markup expression sits after the script block.
		assert.Equal(t, term.Coordinates{Y: 28, X: 23}, positions["geometry.describe"])
	})

	t.Run("javascript script block", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.Svelte, "src/Badge.svelte")

		for _, want := range []occurrence{
			{"format.money", symbolresolve.SymbolRef},
			{"format.percent", symbolresolve.SymbolRef},
			{"Badge.Palette", symbolresolve.SymbolDef},
			{"Badge.emphasize", symbolresolve.SymbolDef},
			{"Badge.Palette.swatch", symbolresolve.SymbolMethodDef},
		} {
			assert.NotZerof(t, occs[want], "missing %+v", want)
		}
		assert.Zero(t, occs[occurrence{"palette.swatch", symbolresolve.SymbolRef}])
		assert.Zero(t, occs[occurrence{"text.toUpperCase", symbolresolve.SymbolRef}])
		assert.Equal(t, map[string]string{"format": "./lib/format"}, ext.Imports)
	})

	t.Run("every script block contributes", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Svelte, "src/Settings.svelte")

		assert.NotZero(t, occs[occurrence{"Settings.load", symbolresolve.SymbolDef}])
		assert.NotZero(t, occs[occurrence{"Settings.toggle", symbolresolve.SymbolDef}])
	})

	t.Run("declarations before a syntax error still extract", func(t *testing.T) {
		_, occs := extractFileT(t, env, symbolresolve.Svelte, "src/Broken.svelte")

		assert.NotZero(t, occs[occurrence{"Broken.stable", symbolresolve.SymbolDef}])
	})

	t.Run("markup and styles contribute nothing", func(t *testing.T) {
		ext, occs := extractFileT(t, env, symbolresolve.Svelte, "src/Plain.svelte")

		assert.Empty(t, occs)
		assert.Empty(t, ext.Imports)
	})
}

func TestExtractFileSvelteMissingInjectedGrammar(t *testing.T) {
	t.Parallel()

	// Without the typescript grammar the TypeScript script block cannot
	// be read, while JavaScript regions still are.
	env := setupSvelteEnv(t, sveltePkgManager(t, "svelte", "javascript"))

	_, occs := extractFileT(t, env, symbolresolve.Svelte, "src/Counter.svelte")
	assert.Zero(t, occs[occurrence{"Counter.increment", symbolresolve.SymbolDef}])
	// The markup expression is JavaScript, but its reference needs the
	// unreadable script's import to count as a module reference.
	assert.Zero(t, occs[occurrence{"geometry.describe", symbolresolve.SymbolRef}])

	_, occs = extractFileT(t, env, symbolresolve.Svelte, "src/Badge.svelte")
	assert.NotZero(t, occs[occurrence{"Badge.emphasize", symbolresolve.SymbolDef}])
	assert.NotZero(t, occs[occurrence{"format.money", symbolresolve.SymbolRef}])
}

func TestExtractFileSvelteWithoutInjectionsQuery(t *testing.T) {
	t.Parallel()

	pkg := sveltePkgManager(t, "svelte", "typescript", "javascript")
	var kept []string
	for _, path := range pkg["svelte"] {
		if filepath.Base(path) != treesitter.InjectionsFilename {
			kept = append(kept, path)
		}
	}
	pkg["svelte"] = kept
	env := setupSvelteEnv(t, pkg)

	ext, occs := extractFileT(t, env, symbolresolve.Svelte, "src/Counter.svelte")
	assert.Empty(t, occs)
	assert.Empty(t, ext.Imports)
}

func TestResolveSkipsHostSpecs(t *testing.T) {
	t.Parallel()

	env := setupSvelteEnv(t, sveltePkgManager(t, "svelte", "typescript", "javascript"))

	// The walk-based resolver addresses whole files of one language and
	// cannot reach injected regions; host specs are index-only.
	matches, err := symbolresolve.Resolve(
		context.Background(), env.parser, env.qc,
		specIter(symbolresolve.Svelte), "Counter.increment", nil,
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no symbols found")
	assert.Empty(t, matches)

	assert.Empty(t, listReferences(t, env.parser, env.qc, specIter(symbolresolve.Svelte)))
}

func setupSvelteEnv(t *testing.T, pkg tsLibDirs) *resolveEnv {
	t.Helper()

	root := t.TempDir()
	copyDirT(t, "testdata_svelte", root)

	uri, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })

	parser := treesitter.NewParser(scheme, pkg, uri)
	return &resolveEnv{
		root: root, parser: parser,
		qc: symbolresolve.NewQualifierContext(scheme, uri),
	}
}

// sveltePkgManager serves the grammar fixtures of the given languages;
// every other language reads as not installed.
func sveltePkgManager(t *testing.T, langIDs ...string) tsLibDirs {
	t.Helper()

	wd, err := os.Getwd()
	require.NoError(t, err)

	dirs := make(tsLibDirs)
	for _, langID := range langIDs {
		langDir := filepath.Join(wd, "..", "..", "syntax", "syntaxtest", langID)
		files := grammarfixture.LibDir(t, langDir)
		require.GreaterOrEqualf(t, len(files), 5, "incomplete tree-sitter fixture %s", langDir)
		dirs[langID] = files
	}
	return dirs
}
