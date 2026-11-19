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
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/multitree"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/vi"
)

// markdownDocument covers every injection the markdown package ships but
// html, whose grammar has no fixture.
var markdownDocument = strings.Join([]string{
	"---",                                    // 0
	"title: Notes",                           // 1
	"---",                                    // 2
	"",                                       // 3
	"# Heading with *emphasis*",              // 4
	"",                                       // 5
	"Some `code` and a [link](https://x.y).", // 6
	"",                                       // 7
	"```go",                                  // 8
	"func main() {",                          // 9
	"\tx := 1",                               // 10
	"}",                                      // 11
	"```",                                    // 12
	"",                                       // 13
	"```js",                                  // 14
	"const a = 1;",                           // 15
	"```",                                    // 16
	"",                                       // 17
	"- item **two**",                         // 18
	"",                                       // 19
	"| a | b |",                              // 20
	"|---|---|",                              // 21
	"| *c* | d |",                            // 22
	"",
}, "\n")

func TestMarkdownCompositeStateIntegration(t *testing.T) {
	s := newMarkdownTree(t, newAllMarkdownPackages(t), markdownDocument)
	it := s.tree.State()
	defer it.Close()
	state, ok := it.Next(context.Background())
	require.True(t, ok)
	assert.Equal(t, syntax.State{
		LangID: "markdown", Folds: true, Indents: true, Highlights: true, Progress: 1,
	}, state)
}

func TestMarkdownCompositeHighlightsIntegration(t *testing.T) {
	xy := func(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }
	literal := syntax.DefaultConfig().CaptureNamesAttributes["text.literal"]
	str := syntax.DefaultConfig().CaptureNamesAttributes["string"]
	number := syntax.DefaultConfig().CaptureNamesAttributes["number"]

	t.Run("every region is highlighted by the language it names", func(t *testing.T) {
		s := newMarkdownTree(t, newAllMarkdownPackages(t), markdownDocument)
		s.waitLocation(highlight(xy(0, 9), xy(4, 9), markdownKeywordAttr))
		s.waitLocation(highlight(xy(0, 15), xy(5, 15), markdownKeywordAttr))
		s.waitLocation(highlight(xy(2, 22), xy(5, 22), markdownEmphasisAttr))
		s.waitLocation(highlight(xy(7, 1), xy(12, 1), str))
		s.locked(func() {
			for _, c := range []struct {
				name string
				want textapi.Location
			}{
				{"the host highlights the heading",
					highlight(xy(2, 4), xy(25, 4), markdownTitleAttr)},
				{"inline emphasis in the heading",
					highlight(xy(15, 4), xy(25, 4), markdownEmphasisAttr)},
				{"an inline code span", highlight(xy(5, 6), xy(11, 6), literal)},
				{"an inline link destination", highlight(xy(25, 6), xy(36, 6), markdownURIAttr)},
				{"a number in the go block", highlight(xy(6, 10), xy(7, 10), number)},
				{"strong emphasis in a list item",
					highlight(xy(7, 18), xy(14, 18), markdownStrongAttr)},
			} {
				assert.True(t, s.hasLocation(c.want), "%s: %+v", c.name, c.want)
			}
			assert.Empty(t, s.notis.messages)
		})
		inline := func(sx, sy, ex, ey int) multitree.Section {
			return multitree.Section{
				Language: "markdown_inline", Range: term.Range{Start: xy(sx, sy), End: xy(ex, ey)},
			}
		}
		assert.Equal(t, []multitree.Section{
			{Language: "yaml", Range: term.Range{Start: xy(0, 1), End: xy(0, 2)}},
			inline(2, 4, 25, 4),
			inline(0, 6, 38, 6),
			{Language: "go", Range: term.Range{Start: xy(0, 9), End: xy(0, 12)}},
			{Language: "javascript", Range: term.Range{Start: xy(0, 15), End: xy(0, 16)}},
			inline(2, 18, 14, 18),
			inline(2, 20, 4, 20),
			inline(6, 20, 8, 20),
			inline(2, 22, 6, 22),
			inline(8, 22, 10, 22),
		}, s.tree.Sections())
	})

	t.Run("a declined language leaves its region to the host", func(t *testing.T) {
		pkgs := newPromptingPackages(t, []string{"markdown", "markdown_inline", "html"},
			"markdown")
		s := newMarkdownTree(t, pkgs, "# Heading with *emphasis*\n\n<div>\nhi\n</div>\n")
		s.waitLocation(highlight(xy(2, 0), xy(25, 0), markdownTitleAttr))
		pkgs.waitPrompts(t, "markdown_inline", "html")
		pkgs.decline("markdown_inline")
		pkgs.decline("html")
		s.waitSections(2)
		s.locked(func() {
			assert.True(t, s.hasLocation(highlight(xy(2, 0), xy(25, 0), markdownTitleAttr)))
			assert.False(t, s.hasAttrOnRow(markdownEmphasisAttr, 0))
			assert.Empty(t, s.notis.messages, "a declined package does not warn")
		})
		assert.ElementsMatch(t, []string{"markdown_inline", "html"}, pkgs.asked())
	})

	t.Run("a fence language nobody publishes is left to the host", func(t *testing.T) {
		pkgs := newPromptingPackages(t, []string{"markdown", "go"}, "markdown", "go")
		s := newMarkdownTree(t, pkgs,
			"```text\nfunc f() {}\n```\n\n```go\nfunc f() {}\n```\n")
		s.waitLocation(highlight(xy(0, 5), xy(4, 5), markdownKeywordAttr))
		s.locked(func() {
			assert.True(t, s.hasLocation(highlight(xy(0, 0), xy(0, 3), literal)),
				"the host's code block")
			assert.False(t, s.hasAttrOnRow(markdownKeywordAttr, 1))
			assert.Empty(t, s.notis.messages)
		})
		assert.Empty(t, pkgs.asked(),
			"the user is not asked to install a package that does not exist")
	})
}

func TestMarkdownCompositeEditsIntegration(t *testing.T) {
	xy := func(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }
	section := func(lang string, sx, sy, ex, ey int) multitree.Section {
		return multitree.Section{
			Language: lang, Range: term.Range{Start: xy(sx, sy), End: xy(ex, ey)},
		}
	}

	t.Run("a fence typed between paragraphs becomes a section", func(t *testing.T) {
		s := newMarkdownTree(t, newAllMarkdownPackages(t), "Intro *a*\n\nend\n")
		s.waitLocation(highlight(xy(6, 0), xy(9, 0), markdownEmphasisAttr))
		s.edit(xy(0, 2), xy(0, 2), "```go\nfunc f() {}\n```\n\n")
		s.waitLocation(highlight(xy(0, 3), xy(4, 3), markdownKeywordAttr))
		assert.Equal(t, []multitree.Section{
			section("markdown_inline", 0, 0, 9, 0),
			section("go", 0, 3, 0, 4),
			section("markdown_inline", 0, 6, 3, 6),
		}, s.tree.Sections())
	})

	t.Run("renaming the fence language switches its grammar", func(t *testing.T) {
		s := newMarkdownTree(t, newAllMarkdownPackages(t), "```go\nvar a = 1\n```\n")
		varKeyword := highlight(xy(0, 1), xy(3, 1), markdownKeywordAttr)
		s.waitLocation(varKeyword)
		s.edit(xy(3, 0), xy(5, 0), "js")
		assert.Equal(t, []multitree.Section{section("javascript", 0, 1, 0, 2)},
			s.tree.Sections())
		// The go section's highlights left with it, so this one is
		// javascript's.
		s.waitLocation(varKeyword)
	})

	t.Run("an unclosed fence runs to the end and closing it shrinks it", func(t *testing.T) {
		s := newMarkdownTree(t, newAllMarkdownPackages(t),
			"Intro\n\n```go\nfunc main() {\n\tx := 1\n")
		s.waitLocation(highlight(xy(0, 3), xy(4, 3), markdownKeywordAttr))
		assert.Equal(t, []multitree.Section{
			section("markdown_inline", 0, 0, 5, 0),
			section("go", 0, 3, 0, 5),
		}, s.tree.Sections())

		s.edit(xy(0, 5), xy(0, 5), "}\n```\n\nafter *x*\n")
		s.waitLocation(highlight(xy(6, 8), xy(9, 8), markdownEmphasisAttr))
		assert.Equal(t, []multitree.Section{
			section("markdown_inline", 0, 0, 5, 0),
			section("go", 0, 3, 0, 6),
			section("markdown_inline", 0, 8, 9, 8),
		}, s.tree.Sections())
	})

	t.Run("an edit inside a paragraph re-highlights it", func(t *testing.T) {
		s := newMarkdownTree(t, newAllMarkdownPackages(t), "plain *words*\n")
		s.waitLocation(highlight(xy(6, 0), xy(13, 0), markdownEmphasisAttr))
		s.edit(xy(0, 0), xy(5, 0), "**plain**")
		s.waitLocation(highlight(xy(0, 0), xy(9, 0), markdownStrongAttr))
		s.locked(func() {
			assert.True(t, s.hasLocation(highlight(xy(10, 0), xy(17, 0), markdownEmphasisAttr)))
		})
		assert.Equal(t, []multitree.Section{section("markdown_inline", 0, 0, 17, 0)},
			s.tree.Sections())
	})
}

func TestMarkdownCompositeFoldsIntegration(t *testing.T) {
	xy := func(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }
	s := newMarkdownTree(t, newAllMarkdownPackages(t),
		"# A\n\n```go\nfunc f() {\n\treturn\n}\n```\n\n# B\n\ntext\n")
	s.waitLocation(highlight(xy(0, 3), xy(4, 3), markdownKeywordAttr))

	// Section fold iterators wait for their trees, which become ready on
	// the loop, so they are drained off it.
	it, ok := s.tree.Folds()
	require.True(t, ok)
	folds, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	// The host's folds are its nodes' ranges: Rune does not apply the
	// #trim! directive of the markdown folds query.
	assert.Contains(t, folds, term.Range{Start: xy(0, 0), End: xy(0, 8)}, "the first section")
	assert.Contains(t, folds, term.Range{Start: xy(0, 8), End: xy(0, 11)}, "the last section")
	assert.Contains(t, folds, term.Range{Start: xy(0, 2), End: xy(0, 7)}, "the code block")
	assert.Contains(t, folds, term.Range{Start: xy(9, 3), End: xy(1, 5)},
		"the function body, in file coordinates")
}

func TestMarkdownCompositeFoldsKeepTheEventLoopFreeIntegration(t *testing.T) {
	xy := func(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }
	pkgs := newPromptingPackages(t, []string{"markdown", "markdown_inline"}, "markdown")
	mu, comp, _, cleanup := newCompositeComponent(t, pkgs, multitree.New, markdownSyntaxConfig,
		func(tick func(func()) bool) vi.Option {
			return vi.WithAuxiliaryBar(true, text.AuxBarConfig{
				LinesEnabled: true, AbsoluteLines: true, FoldsEnabled: true,
				ScheduleNextTick: tick,
			})
		})
	defer cleanup()
	mu.Lock()
	_, h := newEditFileName(t, mu, comp, "# A\n\nalpha *one*\n\n# B\n\nbeta\n",
		strconv.Itoa(int(i.Add(1)))+".md")
	mu.Unlock()
	waitSyntaxLocation(t, mu, h, highlight(xy(2, 0), xy(3, 0), markdownTitleAttr))
	// The host is parsed and its paragraphs' sections exist, but their
	// trees wait for the grammar being installed.
	pkgs.waitPrompts(t, "markdown_inline")

	mu.Lock()
	h.SetCursorAtScroll(xy(0, 2))
	mu.Unlock()
	pkgs.install(t, "markdown_inline")

	waitSyntaxLocation(t, mu, h, highlight(xy(6, 2), xy(11, 2), markdownEmphasisAttr))
}

func TestMarkdownCompositeIndentationAtIntegration(t *testing.T) {
	content := strings.Join([]string{
		"```go",         // 0
		"func f() {",    // 1
		"\tx := 1",      // 2
		"}",             // 3
		"```",           // 4
		"",              // 5
		"first line",    // 6
		"\tsecond line", // 7
		"third line",    // 8
		"",              // 9
		"<div>",         // 10
		"hi",            // 11
		"</div>",        // 12
		"",
	}, "\n")
	pkgs := newPromptingPackages(t, []string{"markdown", "markdown_inline", "go", "html"},
		"markdown", "markdown_inline", "go")
	s := newMarkdownTree(t, pkgs, content)
	s.waitLocation(highlight(term.Coordinates{X: 0, Y: 1}, term.Coordinates{X: 4, Y: 1},
		markdownKeywordAttr))
	pkgs.waitPrompts(t, "html")
	pkgs.decline("html")
	s.waitSections(3)

	t.Run("the go block indents as go", func(t *testing.T) {
		for line, want := range map[int]int{2: 1, 3: 0} {
			s.locked(func() {
				got, ok := s.tree.IndentationAt(line)
				require.True(t, ok, "line %d", line)
				assert.Equal(t, want, got, "line %d", line)
			})
		}
	})

	host := newMarkdownHostTree(t, content)
	for _, c := range []struct {
		name string
		line int
	}{
		{name: "a paragraph's middle line, which markdown_inline cannot indent", line: 7},
		{name: "a line of a declined html block", line: 11},
	} {
		t.Run(c.name+" follows the host", func(t *testing.T) {
			want, wantOK := host.IndentationAt(c.line)
			require.True(t, wantOK, "the host must indent line %d", c.line)
			s.locked(func() {
				got, ok := s.tree.IndentationAt(c.line)
				assert.True(t, ok)
				assert.Equal(t, want, got)
			})
		})
	}
}

func TestMarkdownCompositeEnterIndentsIntegration(t *testing.T) {
	xy := func(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }
	mu, comp, notis, cleanup := newCompositeComponent(t, newAllMarkdownPackages(t),
		multitree.New, markdownSyntaxConfig)
	defer cleanup()
	mu.Lock()
	_, h := newEditFileName(t, mu, comp, "```go\nfunc f() {\n}\n```\n",
		strconv.Itoa(int(i.Add(1)))+".md")
	mu.Unlock()
	waitSyntaxLocation(t, mu, h, highlight(xy(0, 1), xy(4, 1), markdownKeywordAttr))

	mu.Lock()
	defer mu.Unlock()
	pressKeys(t, h, "ggjA<enter>x<esc>")
	assert.Equal(t, "```go\nfunc f() {\n\tx\n}\n```\n", h.CellView().String())
	assert.Empty(t, notis.messages)
}

// The attributes the markdown suites tell the host's and markdown_inline's
// captures apart by; their defaults share a color.
var (
	markdownKeywordAttr  = syntax.CaptureNameAttributes(nil, "keyword")
	markdownTitleAttr    = term.Attributes{Fg: term.ColorAqua}
	markdownEmphasisAttr = term.Attributes{Fg: term.ColorLime}
	markdownStrongAttr   = term.Attributes{Fg: term.ColorOlive}
	markdownURIAttr      = term.Attributes{Fg: term.ColorTeal}
)

func markdownSyntaxConfig(mu *sync.Mutex) syntax.Config {
	cfg := serializedSyntaxConfig(mu)
	cfg.CaptureNamesAttributes["text.title"] = markdownTitleAttr
	cfg.CaptureNamesAttributes["text.emphasis"] = markdownEmphasisAttr
	cfg.CaptureNamesAttributes["text.strong"] = markdownStrongAttr
	cfg.CaptureNamesAttributes["text.uri"] = markdownURIAttr
	return cfg
}

func newAllMarkdownPackages(t *testing.T) *promptingPackages {
	langs := []string{"markdown", "markdown_inline", "go", "javascript", "yaml"}
	return newPromptingPackages(t, langs, langs...)
}

func newMarkdownTree(t *testing.T, pkgs syntax.PkgManager, content string) *compositeTree {
	return newCompositeTree(t, pkgs, ".md", content, markdownSyntaxConfig)
}

func newMarkdownHostTree(t *testing.T, content string) *treesitter.Tree {
	t.Helper()
	pkgs := newFixturePackages(t, "markdown")
	buf := cell.NewBuffer()
	_, err := buf.ReadFrom(strings.NewReader(content))
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("memory:///host.md")
	require.NoError(t, err)
	var mu sync.Mutex
	tree := treesitter.New(context.Background(), &recordingNotifications{}, term.NopInterrupter(),
		pkgs, syntax.FuncLocationSetter(func(textapi.LocationList) {}), uri, buf,
		nopFlusherCloser{}, nil, markdownSyntaxConfig(&mu)).(*treesitter.Tree)
	t.Cleanup(func() { require.NoError(t, tree.Close()) })
	it := tree.State()
	defer it.Close()
	_, ok := it.Next(context.Background())
	require.True(t, ok)
	return tree
}
