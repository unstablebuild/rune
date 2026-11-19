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

func TestCppTreeFoldsIntegration(t *testing.T) {
	tree, cleanup := newCppTree(t, ".cpp", cppFileContent)

	it, ok := tree.Folds()
	require.True(t, ok)

	actual, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.NotEmpty(t, actual)

	require.NoError(t, tree.Close())
	cleanup()
}

func TestCppTreeStateIntegration(t *testing.T) {
	t.Run("ready tree reports a fully initialized cpp state", func(t *testing.T) {
		tree, cleanup := newCppTree(t, ".cpp", cppFileContent)

		it := tree.State()
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		defer it.Close()
		assert.Equal(t, syntax.State{
			LangID:     "cpp",
			Folds:      true,
			Indents:    true,
			Highlights: true,
			Progress:   1,
		}, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("a C header parses as cpp", func(t *testing.T) {
		tree, cleanup := newCppTree(t, ".h", cHeaderContent)

		it := tree.State()
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		defer it.Close()
		assert.Equal(t, "cpp", actual.LangID)
		assert.Equal(t, "", actual.ParserError)

		defs, err := tree.Query("locals.scm", "local.definition.function")
		require.NoError(t, err)
		matches, err := iterator.ToSlice(context.Background(), defs)
		require.NoError(t, err)
		assert.Equal(t, []treesitter.Match{
			{CaptureName: "local.definition.function", LineString: "const char *greet(struct greeter *g);", Line: 7},
			{CaptureName: "local.definition.function", LineString: "int counter_next(void);", Line: 8},
		}, matches)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("missing folds.scm still streams state with Folds false", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"cpp/tree-sitter.so",
			"cpp/highlights.scm",
			"cpp/indents.scm",
		)
		tree, cleanup := newCppTreeWithPkgManager(t, pkgs, ".cpp", cppFileContent)

		it := tree.State()
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		defer it.Close()
		assert.Equal(t, syntax.State{
			LangID:     "cpp",
			Folds:      false,
			Indents:    true,
			Highlights: true,
			Progress:   1,
		}, actual)

		require.NoError(t, tree.Close())
		cleanup()
	})
}

func TestCppTreeQueryIntegration(t *testing.T) {
	t.Run("missing locals.scm makes Query error", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"cpp/tree-sitter.so",
			"cpp/highlights.scm",
			"cpp/indents.scm",
		)
		tree, cleanup := newCppTreeWithPkgManager(t, pkgs, ".cpp", cppFileContent)

		_, err := tree.Query("locals.scm", "query")
		require.Error(t, err)

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("locals.scm resolves definitions by kind", func(t *testing.T) {
		tree, cleanup := newCppTree(t, ".cpp", cppFileContent)

		tests := []struct {
			capture string
			want    []treesitter.Match
		}{
			{
				// Free functions, constructors and out-of-line member
				// definitions are functions; the resolver pairs the
				// latter with their class through its own query. The
				// grammar parses an in-class member template as a plain
				// declaration, so it lands here rather than as a method.
				capture: "local.definition.function",
				want: []treesitter.Match{
					{LineString: "    Point(int x, int y) : x_(x), y_(y) {}", Line: 6},
					{LineString: "    template <typename T> T scale(T f) const;", Line: 10},
					{LineString: "int Point::x() const { return x_; }", Line: 16},
					{LineString: "template <typename T> T Point::scale(T f) const { return f * x_; }", Line: 17},
					{LineString: "int area(const Point &p);", Line: 19},
					{LineString: "int main() {", Line: 22},
				},
			},
			{
				capture: "local.definition.method",
				want: []treesitter.Match{
					{LineString: "    int x() const { return x_; }", Line: 7},
					{LineString: "    int y() const;", Line: 8},
					{LineString: "    virtual ~Point() = default;", Line: 9},
				},
			},
			{
				capture: "local.definition.type",
				want: []treesitter.Match{
					{LineString: "class Point {", Line: 4},
					{LineString: "    template <typename T> T scale(T f) const;", Line: 10},
					{LineString: "template <typename T> T Point::scale(T f) const { return f * x_; }", Line: 17},
					{LineString: "using Shape = Point;", Line: 20},
					{LineString: "enum class Color { Red, Green };", Line: 21},
				},
			},
			{
				capture: "local.definition.namespace",
				want: []treesitter.Match{
					{LineString: "namespace geo {", Line: 3},
				},
			},
			{
				capture: "local.definition.field",
				want: []treesitter.Match{
					{LineString: "    int x_, y_;", Line: 13},
					{LineString: "    int x_, y_;", Line: 13},
				},
			},
			{
				capture: "local.definition.var",
				want: []treesitter.Match{
					// A reference parameter is declared through its
					// reference_declarator, which the query reads as a
					// variable like nvim-treesitter does.
					{LineString: "int area(const Point &p);", Line: 19},
					{LineString: "enum class Color { Red, Green };", Line: 21},
					{LineString: "enum class Color { Red, Green };", Line: 21},
					{LineString: "    auto p = new geo::Point(1, 2);", Line: 23},
					{LineString: "    std::vector<int> v = {1, 2};", Line: 24},
					{LineString: "    for (auto &n : v) {", Line: 25},
					{LineString: "    auto [a, b] = std::pair{1, 2};", Line: 28},
					{LineString: "    auto [a, b] = std::pair{1, 2};", Line: 28},
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
			"cpp/tree-sitter.so",
			"abc_cpp.scm",
		)
		_, comp, tree, cleanup := newCppTreeFull(t, pkgs, ".cpp", cppFileContent)

		w := comp.Workspace()
		f, err := w.OpenFile("abc_cpp.scm", os.O_CREATE|os.O_WRONLY, 0o666)
		require.NoError(t, err)
		_, err = f.Write([]byte(`((namespace_definition name: (namespace_identifier) @local.reference)
  (#set! reference.kind "namespace"))`))
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
			"cpp/indents.scm",
		)
		tree, cleanup := newCppTreeWithPkgManager(t, pkgs, ".cpp", cppFileContent)

		it, err := tree.Query("indents.scm", "query")
		require.NoError(t, err)
		_, ok := it.Next(context.Background())
		assert.False(t, ok)
		require.Error(t, it.Err())

		require.NoError(t, tree.Close())
		cleanup()
	})

	t.Run("incomplete code keeps the definitions before the error", func(t *testing.T) {
		const content = "class Widget {\n" +
			"public:\n" +
			"    int size() const { return n_; }\n" +
			"private:\n" +
			"    int n_;\n" +
			"};\n" +
			"\n" +
			"int Widget::broken(int x) {\n" +
			"    if (x > 0 {\n" +
			"        return\n"
		tree, cleanup := newCppTree(t, ".cpp", content)

		it, err := tree.Query("locals.scm", "local.definition.method")
		require.NoError(t, err)
		matches, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Equal(t, []treesitter.Match{
			{CaptureName: "local.definition.method", LineString: "    int size() const { return n_; }", Line: 2},
		}, matches)

		it, err = tree.Query("locals.scm", "local.definition.function")
		require.NoError(t, err)
		matches, err = iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Equal(t, []treesitter.Match{
			{CaptureName: "local.definition.function", LineString: "int Widget::broken(int x) {", Line: 7},
		}, matches)

		require.NoError(t, tree.Close())
		cleanup()
	})
}

func TestCppTreeCommentCoverageIntegration(t *testing.T) {
	t.Run("returns exact line comment ranges when fully covered", func(t *testing.T) {
		content := "int main() {}\n\n// alpha\n// beta\nint other() {}\n"
		tree, cleanup := newCppTree(t, ".cpp", content)

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
		content := "int main() {}\n\n// alpha\nint other() {}\n"
		tree, cleanup := newCppTree(t, ".cpp", content)

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

func TestCppTreeHighlightsIntegration(t *testing.T) {
	var wg sync.WaitGroup
	ready := func(context.Context) error { wg.Done(); return nil }
	const width, height = 50, 15
	pkgs := newInstalledCppPkgManager(t)
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)
	defer cleanup()
	w := newWriter(width, height)

	wg.Add(1)
	mu.Lock()
	newEditFileName(t, mu, comp, cppHighlightContent, "highlight.cpp")
	mu.Unlock()
	wg.Wait()

	cases := []handlertest.SingleTestCase{
		{Event: term.Event{Ch: 'g', Type: term.EventKey}, Expected: cppHighlightFrame},
	}
	handlertest.TestHandler(t, comp.Browser(), cases, w)
}

const cppHighlightContent = `#include <string>
namespace app {
class Greeter {
public:
    virtual ~Greeter() = default;
    std::string greet() const { return "hi"; }
};
}  // namespace app
`

const cppHighlightFrame = `┌###############─────────────────────────────────┐
│# #############                                 │
├────────────────────────────────────────────────┤
│######## ########                               │
│######### app {                                 │
│##### Greeter {                                 │
│######:                                         │
│    ####### ~Greeter() = #######;               │
│    std::string greet() ##### { ###### ####; }  │
│};                                              │
│}  ################                             │
│                                                │
│                                                │
│                                          NORMAL│
└────────────────────────────────────────────────┘`

func TestCppTreeIndentsIntegration(t *testing.T) {
	var wg sync.WaitGroup
	ready := func(context.Context) error { wg.Done(); return nil }
	const width, height = 30, 15
	pkgs := newInstalledCppPkgManager(t)
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)
	defer cleanup()
	w := newWriter(width, height)

	wg.Add(1)
	mu.Lock()
	newEditFileName(t, mu, comp, cppIndentContent, "indent.cpp")
	mu.Unlock()
	wg.Wait()

	cases := []handlertest.SequenceTestCase{
		{InputSequence: "ggjjo", Expected: cppIndentFrame},
	}
	handlertest.TestHandlerSequenceWriter(t, w, comp.Browser(), width, height, cases)
}

const cppIndentContent = `class Point {
public:
    int x() const;
private:
    int x_;
};
`

const cppIndentFrame = `┌#############───────────────┐
│# ###########               │
├────────────────────────────┤
│##### Point {               │
│######:                     │
│    int x() #####;          │
│    ▐                       │
│#######:                    │
│    int x_;                 │
│};                          │
│                            │
│                            │
│                            │
│                     #######│
└────────────────────────────┘`

func TestCppTreeSelectionIntegration(t *testing.T) {
	const content = `struct Person {
    std::string name;
};

std::string greet(const Person &p) {
    std::vector<std::string> values = {
        p.name,
        p.name,
    };
    if (!values.empty()) {
        for (const auto &value : values) {
            return value;
        }
    }
    return "";
}
`

	tree, cleanup := newCppTree(t, ".cpp", content)
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
			"cpp/highlights.scm",
			"cpp/indents.scm",
			"cpp/folds.scm",
		)
		bad, badCleanup := newCppTreeWithPkgManager(t, pkgs, ".cpp", content)
		defer badCleanup()
		defer func() { require.NoError(t, bad.Close()) }()

		_, ok := bad.SelectionExpand(term.Range{
			Start: term.Coordinates{Y: 4, X: 3},
			End:   term.Coordinates{Y: 4, X: 8},
		})
		assert.False(t, ok)
	})
}

func newInstalledCppPkgManager(t *testing.T) *mockPkgManager {
	return newInstalledPkgManagerWithFiles(t,
		"cpp/tree-sitter.so",
		"cpp/highlights.scm",
		"cpp/indents.scm",
		"cpp/folds.scm",
		"cpp/locals.scm",
	)
}

var cppFileCounter atomic.Int32

func newCppEditFile(
	t *testing.T, mu sync.Locker, comp *text.Component, ext, content string,
) (cell.Editor, text.Handler) {
	n := cppFileCounter.Add(1)
	return newEditFileName(t, mu, comp, content, "cpp"+strconv.Itoa(int(n))+ext)
}

func newCppTree(t *testing.T, ext, content string) (*treesitter.Tree, func()) {
	_, _, tree, cleanup := newCppTreeFull(t, newInstalledCppPkgManager(t), ext, content)
	return tree, cleanup
}

func newCppTreeWithPkgManager(
	t *testing.T, pkgs syntax.PkgManager, ext, content string,
) (*treesitter.Tree, func()) {
	_, _, tree, cleanup := newCppTreeFull(t, pkgs, ext, content)
	return tree, cleanup
}

func newCppTreeFull(
	t *testing.T, pkgs syntax.PkgManager, ext, content string,
) (*cell.Buffer, *text.Component, *treesitter.Tree, func()) {
	var wg sync.WaitGroup
	ready := func(context.Context) error { wg.Done(); return nil }
	const width, height = 30, 15
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)

	wg.Add(1)
	mu.Lock()
	_, h := newCppEditFile(t, mu, comp, ext, content)
	mu.Unlock()
	wg.Wait()

	cref, ok := h.(*text.StatusBar)
	require.True(t, ok)
	tree, ok := cref.Buffer().View().(*treesitter.Tree)
	require.True(t, ok)

	return cref.Buffer(), comp, tree, cleanup
}

const cppFileContent = `#include <utility>
#include <vector>

namespace geo {
class Point {
public:
    Point(int x, int y) : x_(x), y_(y) {}
    int x() const { return x_; }
    int y() const;
    virtual ~Point() = default;
    template <typename T> T scale(T f) const;

private:
    int x_, y_;
};

int Point::x() const { return x_; }
template <typename T> T Point::scale(T f) const { return f * x_; }

int area(const Point &p);
using Shape = Point;
enum class Color { Red, Green };
int main() {
    auto p = new geo::Point(1, 2);
    std::vector<int> v = {1, 2};
    for (auto &n : v) {
        v.push_back(p->scale<int>(n));
    }
    auto [a, b] = std::pair{1, 2};
    delete p;
    return a + b;
}
}  // namespace geo
`

// cHeaderContent is a C header: it parses as cpp because the C++
// grammar reads C declarations while the C grammar cannot read C++.
const cHeaderContent = `#ifndef GREETER_H
#define GREETER_H

struct greeter {
    const char *name;
};

const char *greet(struct greeter *g);
int counter_next(void);

#endif
`
