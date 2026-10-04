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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/iterator"

	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
)

// listReferences drains ListReferences for the given specs into a set.
func listReferences(
	t *testing.T, parser symbolresolve.Searcher,
	qc symbolresolve.QualifierContext,
	specs iterator.Iterator[symbolresolve.Spec],
) map[string]bool {
	t.Helper()
	ch := make(chan string, 64)
	done := make(chan error, 1)
	go func() {
		defer close(ch)
		done <- symbolresolve.ListReferences(context.Background(), parser, qc, specs, ch)
	}()
	got := make(map[string]bool)
	for s := range ch {
		got[s] = true
	}
	require.NoError(t, <-done)
	return got
}

func TestListReferencesGoE2E(t *testing.T) {
	t.Parallel()

	env := setupResolveEnv(t)

	got := listReferences(t, env.parser, env.qc, specIter(symbolresolve.Go))

	// Qualified references (selectors and qualified types) plus
	// workspace definitions surfaced by SearchDefinitions.
	for _, name := range []string{
		"mylib.MyType", "mylib.MyFunc", "mylib.New",
		"iter.Iterator", "iter.OnlyDefined", "iter.Aggregate",
		"iterator.Iterator", "iterator.Map", "iterator.Filter",
		"manyfiles.Token",
	} {
		assert.Truef(t, got[name], "expected Go reference %q to be listed", name)
	}

	// Unexported workspace definitions must be filtered by the Go
	// export predicate.
	for _, name := range []string{
		"mylib.unexportedHelper", "iter.privateOnlyDefined",
	} {
		assert.Falsef(t, got[name], "unexported %q must not be listed", name)
	}
}

func TestListReferencesGoImportFiltering(t *testing.T) {
	t.Parallel()

	env := setupResolveEnv(t)
	got := listReferences(t, env.parser, env.qc, specIter(symbolresolve.Go))

	// dotimport.go calls MyFunc("dot") through a dot import, so it has no
	// package qualifier and must not surface a mylib.* reference. mylib.MyFunc
	// still appears because main.go calls it qualified, so assert the dot-import
	// file's own bare call did not invent a spurious selector reference.
	assert.False(t, got["mylib.DotUser"], "dot-imported call must not be listed as a reference")
	assert.False(t, got[".MyFunc"], "dot import must not produce an empty qualifier reference")

	// blankimport.go blank-imports blueiter purely for its side effects; no
	// blueiter.* reference may be attributed to it.
	assert.False(t, got["blueiter.Iterator"], "blank-imported package must contribute no references")

	// Qualified types from genuinely imported packages pass RequireImport.
	assert.True(t, got["iterator.Iterator"], "qualified type from an imported package must be listed")
}

func TestListReferencesGoStreamsDuplicates(t *testing.T) {
	t.Parallel()

	env := setupResolveEnv(t)

	ch := make(chan string, 256)
	done := make(chan error, 1)
	go func() {
		defer close(ch)
		done <- symbolresolve.ListReferences(
			context.Background(), env.parser, env.qc, specIter(symbolresolve.Go), ch,
		)
	}()
	counts := make(map[string]int)
	for s := range ch {
		counts[s]++
	}
	require.NoError(t, <-done)

	// mylib.MyType is referenced from several files, so the unbuffered stream
	// carries it more than once.
	assert.Greater(t, counts["mylib.MyType"], 1,
		"engine streams one entry per occurrence; callers deduplicate")
}

func TestListReferencesPythonSpecOnGoWorkspaceEmpty(t *testing.T) {
	t.Parallel()

	env := setupResolveEnv(t)

	// A workspace with no Python files must contribute nothing for the
	// Python spec.
	got := listReferences(t, env.parser, env.qc, specIter(symbolresolve.Python))
	assert.Empty(t, got)
}

func TestListReferencesPythonE2E(t *testing.T) {
	t.Parallel()

	env := setupPythonEnv(t)

	got := listReferences(t, env.parser, env.qc, specIter(symbolresolve.Python))

	for _, name := range []string{
		"geometry.area", "geometry.Shape", "requests.get", "service.Service",
	} {
		assert.Truef(t, got[name], "expected Python reference %q to be listed", name)
	}

	// Go-only qualified names must never appear in a Python-only run.
	for _, name := range []string{"mylib.MyType", "iterator.Iterator"} {
		assert.Falsef(t, got[name], "Go name %q must not be listed for Python spec", name)
	}
}

func TestListReferencesGoSpecOnPythonWorkspaceEmpty(t *testing.T) {
	t.Parallel()

	env := setupPythonEnv(t)

	got := listReferences(t, env.parser, env.qc, specIter(symbolresolve.Go))
	assert.Empty(t, got)
}
