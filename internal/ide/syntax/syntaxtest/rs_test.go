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
	"math/rand"
	"os"
	"strconv"
	"sync"
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

func TestRustTreeFoldsIntegration(t *testing.T) {
	tree, cleanup := newRustTree(t, rustFileContent)

	it, ok := tree.Folds()
	require.True(t, ok)

	actual, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.NotEmpty(t, actual)

	require.NoError(t, tree.Close())
	cleanup()
}

func TestRustTreeStateIntegration(t *testing.T) {
	t.Run("ready tree reports a fully initialized rust state", func(t *testing.T) {
		tree, cleanup := newRustTree(t, rustFileContent)

		it := tree.State()
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		defer it.Close()
		assert.Equal(t, syntax.State{
			LangID:     "rust",
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
			"rust/tree-sitter.so",
			"rust/highlights.scm",
			"rust/indents.scm",
		)
		tree, cleanup := newRustTreeWithPkgManager(t, pkgs, rustFileContent)

		it := tree.State()
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		defer it.Close()
		assert.Equal(t, syntax.State{
			LangID:     "rust",
			Folds:      false,
			Indents:    true,
			Highlights: true,
			Progress:   1,
		}, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})
}

func TestRustTreeQueryIntegration(t *testing.T) {
	t.Run("missing locals.scm makes Query error", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"rust/tree-sitter.so",
			"rust/highlights.scm",
			"rust/indents.scm",
		)
		tree, cleanup := newRustTreeWithPkgManager(t, pkgs, rustFileContent)

		_, err := tree.Query("locals.scm", "query")
		require.Error(t, err)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("locals.scm resolves the function definition capture", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"rust/tree-sitter.so",
			"rust/locals.scm",
		)
		tree, cleanup := newRustTreeWithPkgManager(t, pkgs, rustFileContent)

		it, err := tree.Query("locals.scm", "local.definition.function")
		require.NoError(t, err)

		matches, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		require.NotEmpty(t, matches)
		for _, m := range matches {
			assert.Equal(t, "local.definition.function", m.CaptureName)
		}

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("custom query file drives the query", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"rust/tree-sitter.so",
			"abc_rs.scm",
		)
		_, comp, tree, cleanup := newRustTreeFull(t, pkgs, rustFileContent)

		w := comp.Workspace()
		f, err := w.OpenFile("abc_rs.scm", os.O_CREATE|os.O_WRONLY, 0o666)
		require.NoError(t, err)
		_, err = f.Write([]byte(`((function_item name: (identifier) @local.reference)
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
			"rust/indents.scm",
		)
		tree, cleanup := newRustTreeWithPkgManager(t, pkgs, rustFileContent)

		it, err := tree.Query("indents.scm", "query")
		require.NoError(t, err)
		_, ok := it.Next(context.Background())
		assert.False(t, ok)
		require.Error(t, it.Err())

		require.NoError(t, tree.Close())
		cleanup()
	})
}

func TestRustTreeCommentCoverageIntegration(t *testing.T) {
	t.Run("returns exact line comment ranges when fully covered", func(t *testing.T) {
		content := "fn main() {}\n\n// alpha\n// beta\nfn other() {}\n"
		tree, cleanup := newRustTree(t, content)

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

	t.Run("returns false when selection is only partially covered", func(t *testing.T) {
		content := "fn main() {}\n\n// alpha\nfn other() {}\n"
		tree, cleanup := newRustTree(t, content)

		ranges, ok := tree.CommentCoverage(term.Range{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 3, X: 4},
		})
		assert.False(t, ok)
		assert.Nil(t, ranges)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("returns exact block comment range when fully covered", func(t *testing.T) {
		content := "fn main() {}\n\n/* alpha */\nfn other() {}\n"
		tree, cleanup := newRustTree(t, content)

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
}

func TestRustTreeHighlightsIntegration(t *testing.T) {
	var wg sync.WaitGroup
	ready := func(context.Context) error { wg.Done(); return nil }
	const width, height = 30, 15
	pkgs := newInstalledRustPkgManager(t)
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)
	defer cleanup()
	w := newWriter(width, height)

	wg.Add(1)
	mu.Lock()
	newEditFileName(t, mu, comp, rustHighlightContent, "highlight.rs")
	mu.Unlock()
	wg.Wait()

	cases := []handlertest.SingleTestCase{
		{Event: term.Event{Ch: 'g', Type: term.EventKey}, Expected: rustHighlightFrame},
	}
	handlertest.TestHandler(t, comp.Browser(), cases, w)
}

const rustHighlightContent = `fn add(a: i32, b: i32) -> i32 {
    let total = a + b;
    total
}
`

const rustHighlightFrame = `┌##############──────────────┐
│# ############              │
├────────────────────────────┤
│## add(a: i32, b: i32) -> i3│
│    ### total = a + b;      │
│    total                   │
│}                           │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                      NORMAL│
└────────────────────────────┘`

func TestRustTreeIndentsIntegration(t *testing.T) {
	var wg sync.WaitGroup
	ready := func(context.Context) error { wg.Done(); return nil }
	const width, height = 30, 15
	pkgs := newInstalledRustPkgManager(t)
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)
	defer cleanup()
	w := newWriter(width, height)

	wg.Add(1)
	mu.Lock()
	newEditFileName(t, mu, comp, rustIndentContent, "indent.rs")
	mu.Unlock()
	wg.Wait()

	cases := []handlertest.SequenceTestCase{
		{InputSequence: "ggjo", Expected: rustIndentFrame},
	}
	handlertest.TestHandlerSequenceWriter(t, w, comp.Browser(), width, height, cases)
}

const rustIndentContent = `fn main() {
    let x = 1;
}
`

const rustIndentFrame = `┌############────────────────┐
│# ##########                │
├────────────────────────────┤
│## main() {                 │
│    ### x = #;              │
│    ▐                       │
│}                           │
│                            │
│                            │
│                            │
│                            │
│                            │
│                            │
│                     #######│
└────────────────────────────┘`

func TestRustTreeSelectionIntegration(t *testing.T) {
	const content = `struct Person {
    name: String,
}

fn greet(p: Person) -> String {
    let values = vec![
        p.name.clone(),
        format!("hello {}", p.name),
    ];
    if values.len() > 0 {
        for value in values {
            return value.trim().to_string();
        }
    }
    String::new()
}
`

	tree, cleanup := newRustTree(t, content)
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
			"rust/highlights.scm",
			"rust/indents.scm",
			"rust/folds.scm",
		)
		bad, badCleanup := newRustTreeWithPkgManager(t, pkgs, content)
		defer badCleanup()
		defer func() { require.NoError(t, bad.Close()) }()

		_, ok := bad.SelectionExpand(term.Range{
			Start: term.Coordinates{Y: 4, X: 3},
			End:   term.Coordinates{Y: 4, X: 8},
		})
		assert.False(t, ok)
	})
}

func rangeContains(outer, inner term.Range) bool {
	startOK := outer.Start.Y < inner.Start.Y ||
		(outer.Start.Y == inner.Start.Y && outer.Start.X <= inner.Start.X)
	endOK := outer.End.Y > inner.End.Y ||
		(outer.End.Y == inner.End.Y && outer.End.X >= inner.End.X)
	return startOK && endOK
}

func newInstalledRustPkgManager(t *testing.T) *mockPkgManager {
	return newInstalledPkgManagerWithFiles(t,
		"rust/tree-sitter.so",
		"rust/highlights.scm",
		"rust/indents.scm",
		"rust/folds.scm",
		"rust/locals.scm",
	)
}

func newRustEditFile(
	t *testing.T, mu sync.Locker, comp *text.Component, content string,
) (cell.Editor, text.Handler) {
	n := rand.Int()
	return newEditFileName(t, mu, comp, content, strconv.Itoa(n)+".rs")
}

func newRustTree(t *testing.T, content string) (*treesitter.Tree, func()) {
	_, _, tree, cleanup := newRustTreeFull(t, newInstalledRustPkgManager(t), content)
	return tree, cleanup
}

func newRustTreeWithPkgManager(
	t *testing.T, pkgs syntax.PkgManager, content string,
) (*treesitter.Tree, func()) {
	_, _, tree, cleanup := newRustTreeFull(t, pkgs, content)
	return tree, cleanup
}

func newRustTreeFull(
	t *testing.T, pkgs syntax.PkgManager, content string,
) (*cell.Buffer, *text.Component, *treesitter.Tree, func()) {
	var wg sync.WaitGroup
	ready := func(context.Context) error { wg.Done(); return nil }
	const width, height = 30, 15
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)

	wg.Add(1)
	mu.Lock()
	_, h := newRustEditFile(t, mu, comp, content)
	mu.Unlock()
	wg.Wait()

	cref, ok := h.(*text.StatusBar)
	require.True(t, ok)
	tree, ok := cref.Buffer().View().(*treesitter.Tree)
	require.True(t, ok)

	return cref.Buffer(), comp, tree, cleanup
}

const rustFileContent = `use std::fmt;

struct Greeter {
    name: String,
}

impl Greeter {
    fn greet(&self) -> String {
        format!("Hello, {}!", self.name)
    }
}

fn main() {
    let g = Greeter { name: "World".to_string() };
    for i in 0..3 {
        println!("{} {}", g.greet(), i);
    }
}
`
