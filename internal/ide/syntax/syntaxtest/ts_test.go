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
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/text"
)

func TestTypeScriptTreeStateIntegration(t *testing.T) {
	// Each fixture directory is named after the language ID Rune resolves
	// from the file extension, which is also the parser's exported symbol.
	dialects := []struct {
		langID  string
		ext     string
		content string
	}{
		{langID: "typescript", ext: ".ts", content: tsFileContent},
		{langID: "tsx", ext: ".tsx", content: tsxFileContent},
		{langID: "javascript", ext: ".js", content: jsFileContent},
	}
	for _, d := range dialects {
		t.Run(d.langID+" parses its fixture without errors", func(t *testing.T) {
			tree, cleanup := newTSTree(t, d.langID, d.ext, d.content)
			defer cleanup()
			defer func() { require.NoError(t, tree.Close()) }()

			it := tree.State()
			defer it.Close()
			actual, ok := it.Next(context.Background())
			require.True(t, ok)
			assert.Equal(t, syntax.State{
				LangID:     d.langID,
				Folds:      true,
				Indents:    true,
				Highlights: true,
				Progress:   1,
			}, actual)
		})
	}

	t.Run("incomplete source reports a parser error", func(t *testing.T) {
		tree, cleanup := newTSTree(t, "typescript", ".ts", tsIncompleteContent)
		defer cleanup()
		defer func() { require.NoError(t, tree.Close()) }()

		it := tree.State()
		defer it.Close()
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		assert.Equal(t, "typescript", actual.LangID)
		assert.True(t, actual.Highlights)
		assert.NotEmpty(t, actual.ParserError)
	})

	t.Run("missing folds.scm still streams state with Folds false", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"typescript/tree-sitter.so",
			"typescript/highlights.scm",
			"typescript/indents.scm",
		)
		tree, cleanup := newTSTreeWithPkgManager(t, pkgs, ".ts", tsFileContent)
		defer cleanup()
		defer func() { require.NoError(t, tree.Close()) }()

		it := tree.State()
		defer it.Close()
		actual, ok := it.Next(context.Background())
		require.True(t, ok)
		assert.Equal(t, syntax.State{
			LangID:     "typescript",
			Folds:      false,
			Indents:    true,
			Highlights: true,
			Progress:   1,
		}, actual)
	})
}

func TestTypeScriptTreeQueryIntegration(t *testing.T) {
	cases := []struct {
		name    string
		langID  string
		ext     string
		content string
		capture string
		want    []treesitter.Match
	}{
		{
			langID: "typescript", ext: ".ts", content: tsFileContent,
			capture: "local.definition.function",
			want: []treesitter.Match{
				{LineString: "export function add(a: number, b: number): number {", Line: 21},
				{LineString: "export const double = (n: number) => n * 2;", Line: 25},
			},
		},
		{
			langID: "typescript", ext: ".ts", content: tsFileContent,
			capture: "local.definition.method",
			want: []treesitter.Match{
				{LineString: "  area(): number;", Line: 3},
				{LineString: "  constructor(private side: number) {}", Line: 7},
				{LineString: "  area(): number {", Line: 9},
			},
		},
		{
			langID: "typescript", ext: ".ts", content: tsFileContent,
			capture: "local.definition.type",
			want: []treesitter.Match{
				{LineString: "export interface Shape {", Line: 2},
				{LineString: "export class Square implements Shape {", Line: 6},
				{LineString: "export type Point = { x: number; y: number };", Line: 14},
				{LineString: "export enum Color {", Line: 16},
			},
		},
		{
			langID: "typescript", ext: ".ts", content: tsFileContent,
			capture: "local.definition.namespace",
			want: []treesitter.Match{
				{LineString: "namespace Geometry {", Line: 27},
			},
		},
		{
			langID: "typescript", ext: ".ts", content: tsFileContent,
			capture: "local.definition.var",
			want: []treesitter.Match{
				{LineString: "export const double = (n: number) => n * 2;", Line: 25},
				{LineString: "  export const origin: Point = { x: 0, y: 0 };", Line: 28},
			},
		},
		{
			langID: "typescript", ext: ".ts", content: tsFileContent,
			capture: "local.definition.import",
			want: []treesitter.Match{
				{LineString: `import { readFile } from "node:fs/promises";`, Line: 0},
			},
		},
		{
			langID: "tsx", ext: ".tsx", content: tsxFileContent,
			capture: "local.definition.function",
			want: []treesitter.Match{
				{LineString: "export function Counter({ label }: Props) {", Line: 6},
				{LineString: "export const Badge = ({ label }: Props) => <span>{label}</span>;", Line: 15},
			},
		},
		{
			langID: "tsx", ext: ".tsx", content: tsxFileContent,
			capture: "local.definition.type",
			want: []treesitter.Match{
				{LineString: "interface Props {", Line: 2},
			},
		},
		{
			langID: "tsx", ext: ".tsx", content: tsxFileContent,
			capture: "local.definition.var",
			want: []treesitter.Match{
				{LineString: "  const [count, setCount] = useState(0);", Line: 7},
				{LineString: "  const [count, setCount] = useState(0);", Line: 7},
				{LineString: "export const Badge = ({ label }: Props) => <span>{label}</span>;", Line: 15},
			},
		},
		{
			langID: "javascript", ext: ".js", content: jsFileContent,
			capture: "local.definition.function",
			want: []treesitter.Match{
				{LineString: "function render(props) {", Line: 14},
				{LineString: "export const shout = (text) => text.toUpperCase();", Line: 18},
			},
		},
		{
			langID: "javascript", ext: ".js", content: jsFileContent,
			capture: "local.definition.method",
			want: []treesitter.Match{
				{LineString: "  constructor(name) {", Line: 5},
				{LineString: "  greet() {", Line: 9},
			},
		},
		{
			langID: "javascript", ext: ".js", content: jsFileContent,
			capture: "local.definition.type",
			want: []treesitter.Match{
				{LineString: "class Greeter {", Line: 2},
			},
		},
		{
			langID: "javascript", ext: ".js", content: jsFileContent,
			capture: "local.definition.field",
			want: []treesitter.Match{
				{LineString: "  #name;", Line: 3},
			},
		},
		{
			name:   "declarations before a syntax error still resolve",
			langID: "typescript", ext: ".ts", content: tsIncompleteContent,
			capture: "local.definition.function",
			want: []treesitter.Match{
				{LineString: "export function add(a: number, b: number): number {", Line: 0},
			},
		},
	}
	for _, c := range cases {
		name := c.name
		if name == "" {
			name = c.langID + " " + c.capture
		}
		t.Run(name, func(t *testing.T) {
			tree, cleanup := newTSTree(t, c.langID, c.ext, c.content)
			defer cleanup()
			defer func() { require.NoError(t, tree.Close()) }()

			it, err := tree.Query("locals.scm", c.capture)
			require.NoError(t, err)
			matches, err := iterator.ToSlice(context.Background(), it)
			require.NoError(t, err)

			for i := range c.want {
				c.want[i].CaptureName = c.capture
			}
			assert.Equal(t, c.want, matches)
		})
	}

	t.Run("missing locals.scm makes Query error", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t,
			"typescript/tree-sitter.so",
			"typescript/highlights.scm",
		)
		tree, cleanup := newTSTreeWithPkgManager(t, pkgs, ".ts", tsFileContent)
		defer cleanup()
		defer func() { require.NoError(t, tree.Close()) }()

		_, err := tree.Query("locals.scm", "local.reference")
		require.Error(t, err)
	})

	t.Run("missing language parser makes the Query iterator error", func(t *testing.T) {
		pkgs := newInstalledPkgManagerWithFiles(t, "typescript/locals.scm")
		tree, cleanup := newTSTreeWithPkgManager(t, pkgs, ".ts", tsFileContent)
		defer cleanup()
		defer func() { require.NoError(t, tree.Close()) }()

		it, err := tree.Query("locals.scm", "local.reference")
		require.NoError(t, err)
		_, ok := it.Next(context.Background())
		assert.False(t, ok)
		require.Error(t, it.Err())
	})
}

func TestTypeScriptTreeFoldsIntegration(t *testing.T) {
	cases := []struct {
		langID  string
		ext     string
		content string
		want    []term.Range
	}{
		{
			langID: "typescript", ext: ".ts", content: tsFileContent,
			want: []term.Range{
				// interface Shape body
				{Start: term.Coordinates{X: 23, Y: 2}, End: term.Coordinates{X: 1, Y: 4}},
				// class Square body
				{Start: term.Coordinates{X: 37, Y: 6}, End: term.Coordinates{X: 1, Y: 12}},
				// enum Color body
				{Start: term.Coordinates{X: 18, Y: 16}, End: term.Coordinates{X: 1, Y: 19}},
				// function add body
				{Start: term.Coordinates{X: 50, Y: 21}, End: term.Coordinates{X: 1, Y: 23}},
			},
		},
		{
			langID: "tsx", ext: ".tsx", content: tsxFileContent,
			want: []term.Range{
				// interface Props body
				{Start: term.Coordinates{X: 16, Y: 2}, End: term.Coordinates{X: 1, Y: 4}},
				// <button> element
				{Start: term.Coordinates{X: 4, Y: 9}, End: term.Coordinates{X: 13, Y: 11}},
			},
		},
		{
			langID: "javascript", ext: ".js", content: jsFileContent,
			want: []term.Range{
				// class Greeter body
				{Start: term.Coordinates{X: 14, Y: 2}, End: term.Coordinates{X: 1, Y: 12}},
				// render body
				{Start: term.Coordinates{X: 23, Y: 14}, End: term.Coordinates{X: 1, Y: 16}},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.langID, func(t *testing.T) {
			tree, cleanup := newTSTree(t, c.langID, c.ext, c.content)
			defer cleanup()
			defer func() { require.NoError(t, tree.Close()) }()

			it, ok := tree.Folds()
			require.True(t, ok)
			actual, err := iterator.ToSlice(context.Background(), it)
			require.NoError(t, err)
			for _, want := range c.want {
				assert.Contains(t, actual, want)
			}
		})
	}
}

func TestTypeScriptTreeCommentCoverageIntegration(t *testing.T) {
	t.Run("returns exact line comment ranges when fully covered", func(t *testing.T) {
		content := "function main() {}\n\n// alpha\n// beta\nfunction other() {}\n"
		tree, cleanup := newTSTree(t, "typescript", ".ts", content)
		defer cleanup()
		defer func() { require.NoError(t, tree.Close()) }()

		ranges, ok := tree.CommentCoverage(term.Range{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 3, X: len("// beta")},
		})
		require.True(t, ok)
		assert.Equal(t, []term.Range{
			{Start: term.Coordinates{Y: 2, X: 0}, End: term.Coordinates{Y: 2, X: len("// alpha")}},
			{Start: term.Coordinates{Y: 3, X: 0}, End: term.Coordinates{Y: 3, X: len("// beta")}},
		}, ranges)
	})

	t.Run("returns exact block comment range when fully covered", func(t *testing.T) {
		content := "const a = 1;\n\n/** alpha */\nconst b = 2;\n"
		tree, cleanup := newTSTree(t, "javascript", ".js", content)
		defer cleanup()
		defer func() { require.NoError(t, tree.Close()) }()

		ranges, ok := tree.CommentCoverage(term.Range{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 2, X: len("/** alpha */")},
		})
		require.True(t, ok)
		assert.Equal(t, []term.Range{{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 2, X: len("/** alpha */")},
		}}, ranges)
	})

	t.Run("returns false when selection is only partially covered", func(t *testing.T) {
		content := "function main() {}\n\n// alpha\nfunction other() {}\n"
		tree, cleanup := newTSTree(t, "tsx", ".tsx", content)
		defer cleanup()
		defer func() { require.NoError(t, tree.Close()) }()

		ranges, ok := tree.CommentCoverage(term.Range{
			Start: term.Coordinates{Y: 2, X: 0},
			End:   term.Coordinates{Y: 3, X: 4},
		})
		assert.False(t, ok)
		assert.Nil(t, ranges)
	})
}

func TestTypeScriptTreeHighlightsIntegration(t *testing.T) {
	cases := []struct {
		langID   string
		name     string
		content  string
		expected string
	}{
		{langID: "typescript", name: "highlight.ts", content: tsHighlightContent, expected: tsHighlightFrame},
		{langID: "tsx", name: "highlight.tsx", content: tsxHighlightContent, expected: tsxHighlightFrame},
		{langID: "javascript", name: "highlight.js", content: jsHighlightContent, expected: jsHighlightFrame},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var wg sync.WaitGroup
			ready := func(context.Context) error { wg.Done(); return nil }
			const width, height = 40, 15
			mu, comp, cleanup := newTestCase(t, newInstalledTSPkgManager(t, c.langID), width, height, ready)
			defer cleanup()
			w := newWriter(width, height)

			wg.Add(1)
			mu.Lock()
			newEditFileName(t, mu, comp, c.content, c.name)
			mu.Unlock()
			wg.Wait()

			handlertest.TestHandler(t, comp.Browser(), []handlertest.SingleTestCase{
				{Event: term.Event{Ch: 'g', Type: term.EventKey}, Expected: c.expected},
			}, w)
		})
	}
}

func TestTypeScriptTreeIndentsIntegration(t *testing.T) {
	cases := []struct {
		langID   string
		name     string
		content  string
		input    string
		expected string
	}{
		{langID: "typescript", name: "block.ts", content: tsIndentBlockContent, input: "ggjo", expected: tsIndentBlockFrame},
		{langID: "typescript", name: "interface.ts", content: tsIndentInterfaceContent, input: "ggo", expected: tsIndentInterfaceFrame},
		{langID: "typescript", name: "chain.ts", content: tsIndentChainContent, input: "ggjo", expected: tsIndentChainFrame},
		{langID: "typescript", name: "incomplete.ts", content: tsIndentIncompleteContent, input: "Go", expected: tsIndentIncompleteFrame},
		{langID: "tsx", name: "jsx.tsx", content: tsxIndentContent, input: "ggjjo", expected: tsxIndentFrame},
		{langID: "javascript", name: "object.js", content: jsIndentContent, input: "ggo", expected: jsIndentFrame},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var wg sync.WaitGroup
			ready := func(context.Context) error { wg.Done(); return nil }
			const width, height = 40, 15
			mu, comp, cleanup := newTestCase(t, newInstalledTSPkgManager(t, c.langID), width, height, ready)
			defer cleanup()
			w := newWriter(width, height)

			wg.Add(1)
			mu.Lock()
			newEditFileName(t, mu, comp, c.content, c.name)
			mu.Unlock()
			wg.Wait()

			handlertest.TestHandlerSequenceWriter(t, w, comp.Browser(), width, height,
				[]handlertest.SequenceTestCase{{InputSequence: c.input, Expected: c.expected}})
		})
	}
}

func TestTypeScriptTreeSelectionIntegration(t *testing.T) {
	tree, cleanup := newTSTree(t, "tsx", ".tsx", tsxFileContent)
	defer cleanup()
	defer func() { require.NoError(t, tree.Close()) }()

	// The caret sits on "label" inside the <button> children.
	caret := term.Coordinates{Y: 10, X: 7}

	t.Run("expand climbs the node ancestry", func(t *testing.T) {
		current := term.Range{Start: caret, End: caret}
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
		outer, ok := tree.SelectionExpand(term.Range{
			Start: caret,
			End:   term.Coordinates{Y: 10, X: 12},
		})
		require.True(t, ok)
		inner, ok := tree.SelectionShrink(outer, caret)
		require.True(t, ok)
		assert.True(t, rangeContains(outer, inner),
			"shrunk range %#v must sit inside %#v", inner, outer)
	})
}

func newInstalledTSPkgManager(t *testing.T, langID string) *mockPkgManager {
	return newInstalledPkgManagerWithFiles(t,
		langID+"/tree-sitter.so",
		langID+"/highlights.scm",
		langID+"/indents.scm",
		langID+"/folds.scm",
		langID+"/locals.scm",
	)
}

func newTSTree(t *testing.T, langID, ext, content string) (*treesitter.Tree, func()) {
	return newTSTreeWithPkgManager(t, newInstalledTSPkgManager(t, langID), ext, content)
}

func newTSTreeWithPkgManager(
	t *testing.T, pkgs syntax.PkgManager, ext, content string,
) (*treesitter.Tree, func()) {
	var wg sync.WaitGroup
	ready := func(context.Context) error { wg.Done(); return nil }
	const width, height = 30, 15
	mu, comp, cleanup := newTestCase(t, pkgs, width, height, ready)

	wg.Add(1)
	mu.Lock()
	_, h := newEditFileName(t, mu, comp, content, strconv.Itoa(int(i.Add(1)))+ext)
	mu.Unlock()
	wg.Wait()

	cref, ok := h.(*text.StatusBar)
	require.True(t, ok)
	tree, ok := cref.Buffer().View().(*treesitter.Tree)
	require.True(t, ok)
	return tree, cleanup
}

const tsFileContent = `import { readFile } from "node:fs/promises";

export interface Shape {
  area(): number;
}

export class Square implements Shape {
  constructor(private side: number) {}

  area(): number {
    return this.side * this.side;
  }
}

export type Point = { x: number; y: number };

export enum Color {
  Red,
  Green,
}

export function add(a: number, b: number): number {
  return a + b;
}

export const double = (n: number) => n * 2;

namespace Geometry {
  export const origin: Point = { x: 0, y: 0 };
}
`

const tsxFileContent = `import { useState } from "react";

interface Props {
  label: string;
}

export function Counter({ label }: Props) {
  const [count, setCount] = useState(0);
  return (
    <button onClick={() => setCount(count + 1)}>
      {label}: {count}
    </button>
  );
}

export const Badge = ({ label }: Props) => <span>{label}</span>;
`

const jsFileContent = `const path = require("node:path");

class Greeter {
  #name;

  constructor(name) {
    this.#name = name;
  }

  greet() {
    return ` + "`Hello, ${this.#name}`" + `;
  }
}

function render(props) {
  return <div className="greeting">{props.text}</div>;
}

export const shout = (text) => text.toUpperCase();
`

const tsIncompleteContent = `export function add(a: number, b: number): number {
  return a + b;
}

export function sub(a: number, b: number): number {
  return a -
`

const tsHighlightContent = `interface Shape {
  area(): number;
}
// sum two numbers
export function add(a: number) {
  const s = "hi";
  return a + 1;
}
`

const tsHighlightFrame = `┌##############────────────────────────┐
│# ############                        │
├──────────────────────────────────────┤
│######### Shape {                     │
│  area(): number;                     │
│}                                     │
│##################                    │
│###### ######## add(a: number) {      │
│  ##### s = ####;                     │
│  ###### a + #;                       │
│}                                     │
│                                      │
│                                      │
│                                NORMAL│
└──────────────────────────────────────┘`

const tsxHighlightContent = `const App = () => (
  <div className="box">{count}</div>
);
`

const tsxHighlightFrame = `┌###############───────────────────────┐
│# #############                       │
├──────────────────────────────────────┤
│##### App = () => (                   │
│  <div className=#####>{count}</div>  │
│);                                    │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                NORMAL│
└──────────────────────────────────────┘`

const jsHighlightContent = `const p = require("node:path");
async function main() {
  await p.join(` + "`${p}`" + `);
}
`

const jsHighlightFrame = `┌##############────────────────────────┐
│# ############                        │
├──────────────────────────────────────┤
│##### p = #######(###########);       │
│##### ######## main() {               │
│  ##### p.join(######);               │
│}                                     │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                NORMAL│
└──────────────────────────────────────┘`

const tsIndentBlockContent = `function main(): void {
  const x = 1;
}
`

const tsIndentBlockFrame = `┌###########───────────────────────────┐
│# #########                           │
├──────────────────────────────────────┤
│######## main(): #### {               │
│  ##### x = #;                        │
│    ▐                                 │
│}                                     │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                               #######│
└──────────────────────────────────────┘`

const tsIndentInterfaceContent = `interface Shape {
  area(): number;
}
`

const tsIndentInterfaceFrame = `┌###############───────────────────────┐
│# #############                       │
├──────────────────────────────────────┤
│######### Shape {                     │
│    ▐                                 │
│  area(): number;                     │
│}                                     │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                               #######│
└──────────────────────────────────────┘`

const tsIndentChainContent = `const out = items
  .map((x) => {
    return x;
  });
`

const tsIndentChainFrame = `┌###########───────────────────────────┐
│# #########                           │
├──────────────────────────────────────┤
│##### out = items                     │
│  .map((x) => {                       │
│        ▐                             │
│    ###### x;                         │
│  });                                 │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                               #######│
└──────────────────────────────────────┘`

const tsxIndentContent = `const App = () => (
  <div>
    <span />
  </div>
);
`

const tsxIndentFrame = `┌##########────────────────────────────┐
│# ########                            │
├──────────────────────────────────────┤
│##### App = () => (                   │
│  <div>                               │
│    <span />                          │
│        ▐                             │
│  </div>                              │
│);                                    │
│                                      │
│                                      │
│                                      │
│                                      │
│                               #######│
└──────────────────────────────────────┘`

const jsIndentContent = `const config = {
  name: "rune",
};
`

const jsIndentFrame = `┌############──────────────────────────┐
│# ##########                          │
├──────────────────────────────────────┤
│##### config = {                      │
│    ▐                                 │
│  name: ######,                       │
│};                                    │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                               #######│
└──────────────────────────────────────┘`

const tsIndentIncompleteContent = `function main() {
  if (ready) {`

const tsIndentIncompleteFrame = `┌################──────────────────────┐
│# ##############                      │
├──────────────────────────────────────┤
│######## main() {                     │
│  ## (ready) {                        │
│        ▐                             │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                                      │
│                               #######│
└──────────────────────────────────────┘`
