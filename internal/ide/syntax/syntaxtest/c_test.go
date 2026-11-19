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
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/text"
)

func TestCTreeFoldsIntegration(t *testing.T) {
	tree, cleanup := newCTree(t, cFileContent)

	it, ok := tree.Folds()
	require.True(t, ok)

	actual, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.NotEmpty(t, actual)

	require.NoError(t, tree.Close())
	cleanup()
}

func TestCTreeStateIntegration(t *testing.T) {
	t.Run("ready tree reports a fully initialized c state", func(t *testing.T) {
		tree, cleanup := newCTree(t, cFileContent)

		it := tree.State()
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		defer it.Close()
		assert.Equal(t, syntax.State{
			LangID:     "c",
			Folds:      true,
			Indents:    true,
			Highlights: true,
			Progress:   1,
		}, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("missing folds.scm still streams state with Folds false", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"c/tree-sitter.so",
			"c/highlights.scm",
			"c/indents.scm",
		)
		tree, cleanup := newCTreeWithPkgManager(t, pkgs, cFileContent)

		it := tree.State()
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		defer it.Close()
		assert.Equal(t, syntax.State{
			LangID:     "c",
			Folds:      false,
			Indents:    true,
			Highlights: true,
			Progress:   1,
		}, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})
}

func TestCTreeQueryIntegration(t *testing.T) {
	t.Run("missing locals.scm makes Query error", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"c/tree-sitter.so",
			"c/highlights.scm",
			"c/indents.scm",
		)
		tree, cleanup := newCTreeWithPkgManager(t, pkgs, cFileContent)

		_, err := tree.Query("locals.scm", "query")
		require.Error(t, err)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("locals.scm resolves definitions by kind", func(t *testing.T) {
		tree, cleanup := newCTree(t, cFileContent)

		tests := []struct {
			capture string
			want    []treesitter.Match
		}{
			{
				// Prototypes and definitions both declare the function,
				// and a function-like macro is indexed alongside them.
				capture: "local.definition.function",
				want: []treesitter.Match{
					{LineString: "#define MAX(a, b) ((a) > (b) ? (a) : (b))", Line: 3},
					{LineString: "const char *greet(struct greeter *g);", Line: 14},
					{LineString: "const char *greet(struct greeter *g) {", Line: 16},
					{LineString: "int main(void) {", Line: 20},
				},
			},
			{
				// Only specifiers with a body define a type: the
				// `struct greeter *` parameter is a use of it.
				capture: "local.definition.type",
				want: []treesitter.Match{
					{LineString: "typedef struct greeter {", Line: 6},
					{LineString: "} greeter_t;", Line: 8},
					{LineString: "enum color { RED, GREEN };", Line: 10},
				},
			},
			{
				capture: "local.definition.var",
				want: []treesitter.Match{
					{LineString: "#define LIMIT 3", Line: 4},
					{LineString: "enum color { RED, GREEN };", Line: 10},
					{LineString: "enum color { RED, GREEN };", Line: 10},
					{LineString: "static int counter;", Line: 12},
					// A pointer parameter is declared through its
					// pointer_declarator, which the query reads as a
					// variable like nvim-treesitter does.
					{LineString: "const char *greet(struct greeter *g);", Line: 14},
					{LineString: "const char *greet(struct greeter *g) {", Line: 16},
					{LineString: `    greeter_t g = { .name = "World" };`, Line: 21},
					{LineString: "    for (int i = 0; i < LIMIT; i++) {", Line: 22},
				},
			},
			{
				capture: "local.definition.field",
				want: []treesitter.Match{
					{LineString: "    const char *name;", Line: 7},
				},
			},
		}
		for _, tt := range tests {
			it, err := tree.Query("locals.scm", tt.capture)
			require.NoError(t, err, tt.capture)
			matches, err := iterator.ToSlice(context.Background(), it)
			require.NoError(t, err, tt.capture)
			for i := range matches {
				assert.Equal(t, tt.capture, matches[i].CaptureName)
				matches[i].CaptureName = ""
			}
			assert.Equal(t, tt.want, matches, tt.capture)
		}

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("custom query file drives the query", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"c/tree-sitter.so",
			"abc_c.scm",
		)
		_, comp, tree, cleanup := newCTreeFull(t, pkgs, cFileContent)

		w := comp.Workspace()
		f, err := w.OpenFile("abc_c.scm", os.O_CREATE|os.O_WRONLY, 0o666)
		require.NoError(t, err)
		_, err = f.Write([]byte(`((function_declarator declarator: (identifier) @local.reference)
  (#set! reference.kind "function"))`))
		require.NoError(t, err)
		require.NoError(t, f.Close())

		it, err := tree.Query(f.Name(), "local.reference")
		require.NoError(t, err)

		matches, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		require.NotEmpty(t, matches)
		for _, m := range matches {
			assert.Equal(t, "local.reference", m.CaptureName)
		}

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("missing language parser makes the Query iterator error", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"c/indents.scm",
		)
		tree, cleanup := newCTreeWithPkgManager(t, pkgs, cFileContent)

		it, err := tree.Query("indents.scm", "query")
		require.NoError(t, err)
		_, ok := it.Next(context.Background())
		assert.False(t, ok)
		require.Error(t, it.Err())

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("incomplete code keeps the definitions before the error", func(t *testing.T) {
		const content = "int add(int a, int b) {\n" +
			"    return a + b;\n" +
			"}\n" +
			"\n" +
			"int broken(int x) {\n" +
			"    if (x > 0 {\n" +
			"        return\n"
		tree, cleanup := newCTree(t, content)

		it, err := tree.Query("locals.scm", "local.definition.function")
		require.NoError(t, err)
		matches, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Equal(t, []treesitter.Match{
			{CaptureName: "local.definition.function", LineString: "int add(int a, int b) {", Line: 0},
			{CaptureName: "local.definition.function", LineString: "int broken(int x) {", Line: 4},
		}, matches)

		require.NoError(t, tree.Close())
		cleanup()
	})
}

func TestCTreeCommentCoverageIntegration(t *testing.T) {
	t.Run("returns exact line comment ranges when fully covered", func(t *testing.T) {
		content := "int main(void) {}\n\n// alpha\n// beta\nint other(void) {}\n"
		tree, cleanup := newCTree(t, content)

		ranges, ok := tree.CommentCoverage(term.Range{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 3, X: len("// beta")},
		})
		require.True(t, ok)
		assert.Equal(t, []term.Range{
			{Start: term.Coordinates{Y: 2, X: 0}, End: term.Coordinates{Y: 2, X: len("// alpha")}},
			{Start: term.Coordinates{Y: 3, X: 0}, End: term.Coordinates{Y: 3, X: len("// beta")}},
		}, ranges)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("returns exact block comment range when fully covered", func(t *testing.T) {
		content := "int main(void) {}\n\n/* alpha */\nint other(void) {}\n"
		tree, cleanup := newCTree(t, content)

		ranges, ok := tree.CommentCoverage(term.Range{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 2, X: len("/* alpha */")},
		})
		require.True(t, ok)
		assert.Equal(t, []term.Range{{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 2, X: len("/* alpha */")},
		}}, ranges)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("returns false when selection is only partially covered", func(t *testing.T) {
		content := "int main(void) {}\n\n// alpha\nint other(void) {}\n"
		tree, cleanup := newCTree(t, content)

		ranges, ok := tree.CommentCoverage(term.Range{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 3, X: 4},
		})
		assert.False(t, ok)
		assert.Nil(t, ranges)

		require.NoError(t, tree.Close())
		cleanup()
	})
}

func TestCTreeHighlightsIntegration(t *testing.T) {
	var wg sync.WaitGroup
	ready := func(context.Context) error { wg.Done(); return nil }
	const width, height = 34, 15
	pkgs := newInstalledCPkgManager(t)
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)
	defer cleanup()
	w := newWriter(width, height)

	wg.Add(1)
	mu.Lock()
	newEditFileName(t, mu, comp, cHighlightContent, "highlight.c")
	mu.Unlock()
	wg.Wait()

	cases := []handlertest.SingleTestCase{
		{Event: term.Event{Ch: 'g', Type: term.EventKey}, Expected: cHighlightFrame},
	}
	handlertest.TestHandler(t, comp.Browser(), cases, w)
}

const cHighlightContent = `#include <stdio.h>
#define LIMIT 10

static int add(int a, int b) {
    int total = a + b; // sum
    return total;
}
`

const cHighlightFrame = `┌#############───────────────────┐
│# ###########                   │
├────────────────────────────────┤
│######## #########              │
│####### LIMIT 10                │
│                                │
│###### int add(int a, int b) {  │
│    int total = a + b; ######   │
│    ###### total;               │
│}                               │
│                                │
│                                │
│                                │
│                          NORMAL│
└────────────────────────────────┘`

func TestCTreeIndentsIntegration(t *testing.T) {
	var wg sync.WaitGroup
	ready := func(context.Context) error { wg.Done(); return nil }
	const width, height = 30, 15
	pkgs := newInstalledCPkgManager(t)
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)
	defer cleanup()
	w := newWriter(width, height)

	wg.Add(1)
	mu.Lock()
	newEditFileName(t, mu, comp, cIndentContent, "indent.c")
	mu.Unlock()
	wg.Wait()

	cases := []handlertest.SequenceTestCase{
		{InputSequence: "ggjjjo", Expected: cIndentFrame},
	}
	handlertest.TestHandlerSequenceWriter(t, w, comp.Browser(), width, height, cases)
}

const cIndentContent = `int main(void) {
    int x = 1;
    if (x) {
        x = 2;
    }
}
`

const cIndentFrame = `┌###########─────────────────┐
│# #########                 │
├────────────────────────────┤
│int main(void) {            │
│    int x = #;              │
│    ## (x) {                │
│        x = #;              │
│        ▐                   │
│    }                       │
│}                           │
│                            │
│                            │
│                            │
│                     #######│
└────────────────────────────┘`

func TestCTreeSelectionIntegration(t *testing.T) {
	const content = `struct person {
    const char *name;
};

const char *greet(struct person p) {
    const char *values[] = {
        p.name,
        p.name,
    };
    if (values[0]) {
        for (int i = 0; i < 2; i++) {
            return values[i];
        }
    }
    return "";
}
`

	tree, cleanup := newCTree(t, content)
	defer cleanup()
	defer func() { require.NoError(t, tree.Close()) }()

	t.Run("expand climbs the node ancestry", func(t *testing.T) {
		from := term.Range{
			Start: term.Coordinates{Y: 6, X: 8},
			End:   term.Coordinates{Y: 6, X: 8},
		}
		current := from
		var got []term.Range
		for i := range 4 {
			next, ok := tree.SelectionExpand(current)
			require.True(t, ok, "expand %d from %#v", i, current)
			require.NotEqual(t, current, next, "expand %d did not grow", i)
			got = append(got, next)
			current = next
		}
		for i := 1; i < len(got); i++ {
			assert.True(t, rangeContains(got[i], got[i-1]),
				"expansion %d (%#v) must contain previous (%#v)", i, got[i], got[i-1])
		}
	})

	t.Run("shrink reverses an expansion around the caret", func(t *testing.T) {
		caret := term.Coordinates{Y: 6, X: 8}
		outer, ok := tree.SelectionExpand(term.Range{
			Start: term.Coordinates{Y: 6, X: 8},
			End:   term.Coordinates{Y: 6, X: 14},
		})
		require.True(t, ok)
		inner, ok := tree.SelectionShrink(outer, caret)
		require.True(t, ok)
		assert.True(t, rangeContains(outer, inner),
			"shrunk range %#v must sit inside %#v", inner, outer)
	})

	t.Run("returns false when the syntax tree is unavailable", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"c/highlights.scm",
			"c/indents.scm",
			"c/folds.scm",
		)
		bad, badCleanup := newCTreeWithPkgManager(t, pkgs, content)
		defer badCleanup()
		defer func() { require.NoError(t, bad.Close()) }()

		_, ok := bad.SelectionExpand(term.Range{
			Start: term.Coordinates{Y: 4, X: 3},
			End:   term.Coordinates{Y: 4, X: 8},
		})
		assert.False(t, ok)
	})
}

func newInstalledCPkgManager(t *testing.T) *mockPkgManager {
	return newInstalledPkgManagerWithFiles(t,
		"c/tree-sitter.so",
		"c/highlights.scm",
		"c/indents.scm",
		"c/folds.scm",
		"c/locals.scm",
	)
}

var cFileCounter atomic.Int32

func newCEditFile(
	t *testing.T, mu sync.Locker, comp *text.Component, content string,
) (cell.Editor, text.Handler) {
	n := cFileCounter.Add(1)
	return newEditFileName(t, mu, comp, content, "c"+strconv.Itoa(int(n))+".c")
}

func newCTree(t *testing.T, content string) (*treesitter.Tree, func()) {
	_, _, tree, cleanup := newCTreeFull(t, newInstalledCPkgManager(t), content)
	return tree, cleanup
}

func newCTreeWithPkgManager(
	t *testing.T, pkgs syntax.PkgManager, content string,
) (*treesitter.Tree, func()) {
	_, _, tree, cleanup := newCTreeFull(t, pkgs, content)
	return tree, cleanup
}

func newCTreeFull(
	t *testing.T, pkgs syntax.PkgManager, content string,
) (*cell.Buffer, *text.Component, *treesitter.Tree, func()) {
	var wg sync.WaitGroup
	ready := func(context.Context) error { wg.Done(); return nil }
	const width, height = 30, 15
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)

	wg.Add(1)
	mu.Lock()
	_, h := newCEditFile(t, mu, comp, content)
	mu.Unlock()
	wg.Wait()

	cref, ok := h.(*text.StatusBar)
	require.True(t, ok)
	tree, ok := cref.Buffer().View().(*treesitter.Tree)
	require.True(t, ok)

	return cref.Buffer(), comp, tree, cleanup
}

const cFileContent = `#include <stdio.h>
#include "greeter.h"

#define MAX(a, b) ((a) > (b) ? (a) : (b))
#define LIMIT 3

typedef struct greeter {
    const char *name;
} greeter_t;

enum color { RED, GREEN };

static int counter;

const char *greet(struct greeter *g);

const char *greet(struct greeter *g) {
    return g->name;
}

int main(void) {
    greeter_t g = { .name = "World" };
    for (int i = 0; i < LIMIT; i++) {
        printf("%s %d\n", greet(&g), MAX(i, counter));
    }
    return 0;
}
`
