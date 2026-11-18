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
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
	"unstable.build/rune/internal/ide/syntax/multitree"
	"unstable.build/rune/internal/ide/syntax/treesitter"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/text/vi"
	"unstable.build/rune/internal/workspace"
)

// The attributes the svelte suites assert on. keyword is the default; the
// others are given a color because their defaults are empty.
var (
	svelteKeywordAttr  = term.Attributes{Fg: term.ColorYellow}
	svelteTypeAttr     = term.Attributes{Fg: term.ColorAqua}
	sveltePropertyAttr = term.Attributes{Fg: term.ColorGreen}
	svelteTagAttr      = term.Attributes{Fg: term.ColorTeal}
)

const svelteWait = 20 * time.Second

// newSveltePackages installs the fixture grammars of langs through a real
// package manager, so each section tree finds its own language's package.
func newSveltePackages(t *testing.T, langs ...string) *grammarfixture.Packages {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	versions := make(map[string][]string, len(langs))
	for _, lang := range langs {
		versions[lang] = []string{"1"}
	}
	pkgs := grammarfixture.NewPackages(t, versions)
	for _, lang := range langs {
		pkgs.Install(t, lang, "1", grammarfixture.PackageFiles(t, filepath.Join(wd, lang)))
	}
	return pkgs
}

func newAllSveltePackages(t *testing.T) *grammarfixture.Packages {
	return newSveltePackages(t, "svelte", "javascript", "typescript", "css")
}

// promptingPackages answers lookups the way the editor's package manager
// does with updates.auto_install off: a lookup of a package that is not
// installed waits for the user, who either installs it or declines, and a
// declined package reads as storageapi.ErrNotFound.
type promptingPackages struct {
	*grammarfixture.Packages

	// done is closed when the test ends. Tree.Close does not cancel a
	// download, and a lookup after the fixture's cleanup reads as not
	// installed, so a prompt then would wait forever.
	done chan struct{}

	mu      sync.Mutex
	prompts map[string]chan struct{}
}

func newPromptingPackages(t *testing.T, catalog []string, installed ...string) *promptingPackages {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	versions := make(map[string][]string, len(catalog))
	for _, lang := range catalog {
		versions[lang] = []string{"1"}
	}
	pkgs := grammarfixture.NewPackages(t, versions)
	for _, lang := range installed {
		pkgs.Install(t, lang, "1", grammarfixture.PackageFiles(t, filepath.Join(wd, lang)))
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	return &promptingPackages{Packages: pkgs, done: done, prompts: make(map[string]chan struct{})}
}

func (p *promptingPackages) LibDir(
	ctx context.Context, pkgID string,
) (sdkiterator.Iterator[string], error) {
	it, err := p.Packages.LibDir(ctx, pkgID)
	if !errors.Is(err, idepkg.ErrNotInstalled) {
		return it, err
	}
	p.mu.Lock()
	answered, ok := p.prompts[pkgID]
	if !ok {
		answered = make(chan struct{})
		p.prompts[pkgID] = answered
	}
	p.mu.Unlock()
	select {
	case <-answered:
	case <-p.done:
		return nil, storageapi.ErrNotFound
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if it, err := p.Packages.LibDir(ctx, pkgID); err == nil {
		return it, nil
	}
	return nil, storageapi.ErrNotFound
}

func (p *promptingPackages) waitPrompts(t *testing.T, pkgIDs ...string) {
	t.Helper()
	require.Eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, id := range pkgIDs {
			if _, ok := p.prompts[id]; !ok {
				return false
			}
		}
		return true
	}, svelteWait, 5*time.Millisecond, "waiting for install prompts for %v", pkgIDs)
}

func (p *promptingPackages) asked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	ids := make([]string, 0, len(p.prompts))
	for id := range p.prompts {
		ids = append(ids, id)
	}
	return ids
}

func (p *promptingPackages) install(t *testing.T, pkgID string) {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	p.Install(t, pkgID, "1", grammarfixture.PackageFiles(t, filepath.Join(wd, pkgID)))
	p.answer(pkgID)
}

func (p *promptingPackages) decline(pkgID string) {
	p.answer(pkgID)
}

func (p *promptingPackages) answer(pkgID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	close(p.prompts[pkgID])
}

// svelteSyntaxConfig serializes every tree's event-loop work through mu,
// which the test holds while it edits or reads.
func svelteSyntaxConfig(mu *sync.Mutex) syntax.Config {
	cfg := syntax.DefaultConfig()
	cfg.ScheduleNextTick = func(fn func()) bool {
		mu.Lock()
		defer mu.Unlock()
		fn()
		return true
	}
	cfg.CaptureNamesAttributes["type"] = svelteTypeAttr
	cfg.CaptureNamesAttributes["property"] = sveltePropertyAttr
	cfg.CaptureNamesAttributes["tag"] = svelteTagAttr
	return cfg
}

func svelteLocation(from, to term.Coordinates, attr term.Attributes) textapi.Location {
	return textapi.Location{From: from, To: to, Attr: attr}
}

// svelteTree is a composite tree installed into a bare buffer, with the
// highlights it publishes recorded.
type svelteTree struct {
	t     *testing.T
	mu    *sync.Mutex
	buf   *cell.Buffer
	tree  *multitree.Tree
	notis *recordingNotifications
	// locs is the last published list, written on the event loop.
	locs []textapi.Location
}

func newSvelteTree(t *testing.T, pkgs syntax.PkgManager, content string) *svelteTree {
	t.Helper()
	mu := new(sync.Mutex)
	s := &svelteTree{t: t, mu: mu, buf: cell.NewBuffer(), notis: &recordingNotifications{}}
	_, err := s.buf.ReadFrom(strings.NewReader(content))
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("memory:///" + strconv.Itoa(int(i.Add(1))) + ".svelte")
	require.NoError(t, err)
	loc := syntax.FuncLocationSetter(func(ll textapi.LocationList) {
		s.locs = collectLocations(ll)
	})
	mu.Lock()
	defer mu.Unlock()
	s.tree = multitree.New(context.Background(), s.notis, term.NopInterrupter(), pkgs, loc,
		uri, s.buf, nopFlusherCloser{}, nil, svelteSyntaxConfig(mu)).(*multitree.Tree)
	t.Cleanup(func() { require.NoError(t, s.tree.Close()) })
	return s
}

// edit edits the buffer as the editor would, on the event loop.
func (s *svelteTree) edit(start, end term.Coordinates, str string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Edit(context.Background(), start, end, str)
}

// locked runs fn with the event loop held.
func (s *svelteTree) locked(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

func (s *svelteTree) hasLocation(want textapi.Location) bool {
	return slices.Contains(s.locs, want)
}

func (s *svelteTree) hasAttrOnRow(attr term.Attributes, row int) bool {
	for _, l := range s.locs {
		if l.Attr == attr && l.From.Y == row {
			return true
		}
	}
	return false
}

// waitLocation waits for the trees to publish want, which a section does
// once its grammar is loaded and parsed.
func (s *svelteTree) waitLocation(want textapi.Location) {
	s.t.Helper()
	require.Eventually(s.t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.hasLocation(want)
	}, svelteWait, 5*time.Millisecond, "waiting for highlight %+v", want)
}

func (s *svelteTree) waitSections(n int) {
	s.t.Helper()
	require.Eventually(s.t, func() bool {
		return len(s.tree.Sections()) == n
	}, svelteWait, 5*time.Millisecond, "waiting for %d sections", n)
}

func (s *svelteTree) waitNotifications(n int) {
	s.t.Helper()
	require.Eventually(s.t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.notis.messages) >= n
	}, svelteWait, 5*time.Millisecond, "waiting for %d notifications", n)
}

func newSvelteHostTree(t *testing.T, content string) *treesitter.Tree {
	t.Helper()
	pkgs := newInstalledPkgManagerWithFiles(t,
		"svelte/tree-sitter.so",
		"svelte/highlights.scm",
		"svelte/indents.scm",
		"svelte/folds.scm",
		"svelte/locals.scm",
		"svelte/injections.scm",
	)
	buf := cell.NewBuffer()
	_, err := buf.ReadFrom(strings.NewReader(content))
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("memory:///host.svelte")
	require.NoError(t, err)
	var mu sync.Mutex
	tree := treesitter.New(context.Background(), &recordingNotifications{}, term.NopInterrupter(),
		pkgs, syntax.FuncLocationSetter(func(textapi.LocationList) {}), uri, buf,
		nopFlusherCloser{}, nil, svelteSyntaxConfig(&mu)).(*treesitter.Tree)
	t.Cleanup(func() { require.NoError(t, tree.Close()) })
	it := tree.State()
	defer it.Close()
	_, ok := it.Next(context.Background())
	require.True(t, ok)
	return tree
}

func TestSvelteInjectionsIntegration(t *testing.T) {
	rng := func(sx, sy, ex, ey int) term.Range {
		return term.Range{Start: term.Coordinates{X: sx, Y: sy}, End: term.Coordinates{X: ex, Y: ey}}
	}
	cases := []struct {
		name    string
		content string
		want    []treesitter.Injection
	}{
		{
			name:    "script is javascript",
			content: "<script>\nconst a = 1;\n</script>",
			want:    []treesitter.Injection{{Language: "javascript", Range: rng(8, 0, 0, 2)}},
		},
		{
			name:    "script lang ts is typescript",
			content: "<script lang=\"ts\">\nlet a: number;\n</script>",
			want:    []treesitter.Injection{{Language: "typescript", Range: rng(18, 0, 0, 2)}},
		},
		{
			name:    "script type module is javascript",
			content: "<script type=\"module\">\nlet a;\n</script>",
			want:    []treesitter.Injection{{Language: "javascript", Range: rng(22, 0, 0, 2)}},
		},
		{
			name:    "style is css",
			content: "<style>\np { color: red; }\n</style>",
			want:    []treesitter.Injection{{Language: "css", Range: rng(7, 0, 0, 2)}},
		},
		{
			name:    "style lang scss is scss",
			content: "<style lang=\"scss\">\np { }\n</style>",
			want:    []treesitter.Injection{{Language: "scss", Range: rng(19, 0, 0, 2)}},
		},
		{
			name:    "html comment is comment",
			content: "<!-- x -->",
			want:    []treesitter.Injection{{Language: "comment", Range: rng(0, 0, 10, 0)}},
		},
		{
			name:    "event handler attribute is javascript",
			content: "<a onclick=\"go()\">x</a>",
			want:    []treesitter.Injection{{Language: "javascript", Range: rng(12, 0, 16, 0)}},
		},
		{
			name:    "language captured from an attribute",
			content: "<p lang=\"pug\">x</p>",
			want:    []treesitter.Injection{{Language: "pug", Range: rng(14, 0, 15, 0)}},
		},
		{
			name:    "no blocks",
			content: "<p>hi</p>",
			want:    []treesitter.Injection{},
		},
		{
			name:    "unterminated script",
			content: "<script>\nconst a",
			want:    []treesitter.Injection{},
		},
		{
			name: "every block in document order",
			content: "<script lang=\"ts\">\nlet a = 1;\n</script>\n<!-- c -->\n<style>\np { }\n</style>\n" +
				"<script>\nlet b;\n</script>",
			want: []treesitter.Injection{
				{Language: "typescript", Range: rng(18, 0, 0, 2)},
				{Language: "comment", Range: rng(0, 3, 10, 3)},
				{Language: "css", Range: rng(7, 4, 0, 6)},
				{Language: "javascript", Range: rng(8, 7, 0, 9)},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tree := newSvelteHostTree(t, c.content)
			got, ok := tree.Injections()
			require.True(t, ok)
			assert.Equal(t, c.want, got)
			rows := strings.Count(c.content, "\n") + 1
			for _, inj := range got {
				assert.Less(t, inj.Range.End.Y, rows, "%+v lies inside the buffer", inj)
			}
		})
	}

	t.Run("grammar without an injections query", func(t *testing.T) {
		tree, cleanup := newTSTree(t, "javascript", ".js", jsFileContent)
		defer cleanup()
		defer func() { require.NoError(t, tree.Close()) }()
		_, ok := tree.Injections()
		assert.False(t, ok)
	})
}

const svelteComponentContent = "<script lang=\"ts\">\n\tconst a: number = 1;\n</script>\n\n" +
	"<style>\n\tp { color: red; }\n</style>\n\n<p>hi</p>\n"

func TestSvelteCompositeStateIntegration(t *testing.T) {
	s := newSvelteTree(t, newAllSveltePackages(t), svelteComponentContent)
	it := s.tree.State()
	defer it.Close()
	state, ok := it.Next(context.Background())
	require.True(t, ok)
	assert.Equal(t, syntax.State{
		LangID: "svelte", Folds: true, Indents: true, Highlights: true, Progress: 1,
	}, state)
}

func TestSvelteCompositeHighlightsIntegration(t *testing.T) {
	xy := func(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }

	t.Run("every block is highlighted by its own grammar", func(t *testing.T) {
		s := newSvelteTree(t, newAllSveltePackages(t), svelteComponentContent)
		s.waitLocation(svelteLocation(xy(1, 1), xy(6, 1), svelteKeywordAttr))
		s.waitLocation(svelteLocation(xy(5, 5), xy(10, 5), sveltePropertyAttr))
		s.locked(func() {
			assert.True(t, s.hasLocation(svelteLocation(xy(10, 1), xy(16, 1), svelteTypeAttr)),
				"typescript highlights the type annotation")
			assert.True(t, s.hasLocation(svelteLocation(xy(1, 8), xy(2, 8), svelteTagAttr)),
				"the host still highlights tags outside the blocks")
			assert.True(t, s.hasLocation(svelteLocation(xy(1, 0), xy(7, 0), svelteTagAttr)),
				"the host highlights the script tag itself")
		})
		assert.Equal(t, []multitree.Section{
			{Language: "typescript", Range: term.Range{Start: xy(18, 0), End: xy(0, 2)}},
			{Language: "css", Range: term.Range{Start: xy(7, 4), End: xy(0, 6)}},
		}, s.tree.Sections())
	})

	t.Run("plain script is javascript", func(t *testing.T) {
		content := strings.Replace(svelteComponentContent, `<script lang="ts">`, "<script>", 1)
		s := newSvelteTree(t, newAllSveltePackages(t), content)
		s.waitLocation(svelteLocation(xy(1, 1), xy(6, 1), svelteKeywordAttr))
		s.locked(func() {
			assert.False(t, s.hasLocation(svelteLocation(xy(10, 1), xy(16, 1), svelteTypeAttr)),
				"javascript has no type annotations")
		})
		assert.Equal(t, "javascript", s.tree.Sections()[0].Language)
	})

	t.Run("comments are highlighted by the comment grammar", func(t *testing.T) {
		number := syntax.DefaultConfig().CaptureNamesAttributes["number"]
		s := newSvelteTree(t, newSveltePackages(t, "svelte", "comment"),
			"<!-- TODO: fix #12 soon -->\n<p>hi</p>\n")
		s.waitLocation(svelteLocation(xy(15, 0), xy(18, 0), number))
		s.locked(func() {
			var numbers []textapi.Location
			for _, l := range s.locs {
				if l.Attr == number {
					numbers = append(numbers, l)
				}
			}
			assert.Equal(t, []textapi.Location{svelteLocation(xy(15, 0), xy(18, 0), number)},
				numbers, "only the issue number is a number, not every word")
		})
		assert.Equal(t, []multitree.Section{
			{Language: "comment", Range: term.Range{Start: xy(0, 0), End: xy(27, 0)}},
		}, s.tree.Sections())
	})
}

func TestSvelteCompositeEditsIntegration(t *testing.T) {
	xy := func(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }
	const content = "<p>top</p>\n<script>\n\tconst a = 1;\n</script>\n<p>bye</p>\n"
	constKeyword := svelteLocation(xy(1, 2), xy(6, 2), svelteKeywordAttr)
	section := func(sx, sy, ex, ey int) []multitree.Section {
		return []multitree.Section{{
			Language: "javascript",
			Range:    term.Range{Start: xy(sx, sy), End: xy(ex, ey)},
		}}
	}

	t.Run("a line inserted above moves the block's highlights", func(t *testing.T) {
		s := newSvelteTree(t, newAllSveltePackages(t), content)
		s.waitLocation(constKeyword)
		s.edit(xy(0, 0), xy(0, 0), "\n")
		s.locked(func() {
			assert.True(t, s.hasLocation(svelteLocation(xy(1, 3), xy(6, 3), svelteKeywordAttr)))
			assert.False(t, s.hasLocation(constKeyword))
		})
		assert.Equal(t, section(8, 2, 0, 4), s.tree.Sections())
	})

	t.Run("a statement inserted inside is highlighted", func(t *testing.T) {
		s := newSvelteTree(t, newAllSveltePackages(t), content)
		s.waitLocation(constKeyword)
		s.edit(xy(13, 2), xy(13, 2), "\n\tlet b = 2;")
		s.locked(func() {
			assert.True(t, s.hasLocation(constKeyword))
			assert.True(t, s.hasLocation(svelteLocation(xy(1, 3), xy(4, 3), svelteKeywordAttr)))
		})
		assert.Equal(t, section(8, 1, 0, 4), s.tree.Sections())
	})

	t.Run("an end tag typed mid-block shrinks the block", func(t *testing.T) {
		s := newSvelteTree(t, newAllSveltePackages(t),
			"<script>\n\tconst a = 1;\n\tconst b = 2;\n</script>\n")
		s.waitLocation(svelteLocation(xy(1, 2), xy(6, 2), svelteKeywordAttr))
		s.edit(xy(0, 2), xy(0, 2), "</script>\n")
		assert.Equal(t, section(8, 0, 0, 2), s.tree.Sections())
		s.locked(func() {
			assert.True(t, s.hasLocation(svelteLocation(xy(1, 1), xy(6, 1), svelteKeywordAttr)))
			assert.False(t, s.hasAttrOnRow(svelteKeywordAttr, 3),
				"text after the end tag is the host's, not javascript")
		})
	})

	t.Run("deleting and restoring the end tag reconciles", func(t *testing.T) {
		s := newSvelteTree(t, newAllSveltePackages(t), content)
		s.waitLocation(constKeyword)
		s.edit(xy(0, 3), xy(0, 4), "")
		sections := s.tree.Sections()
		assert.LessOrEqual(t, len(sections), 1)
		s.locked(func() {
			_, _ = s.tree.IndentationAt(2)
		})
		s.edit(xy(0, 3), xy(0, 3), "</script>\n")
		assert.Equal(t, section(8, 1, 0, 3), s.tree.Sections())
		// A block the host had dropped comes back with a fresh tree,
		// which highlights once parsed.
		s.waitLocation(constKeyword)
	})
}

func TestSvelteCompositeFoldsIntegration(t *testing.T) {
	xy := func(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }
	s := newSvelteTree(t, newAllSveltePackages(t),
		"<script>\n\tfunction f() {\n\t\treturn 1;\n\t}\n</script>\n")
	s.waitLocation(svelteLocation(xy(1, 1), xy(9, 1), svelteKeywordAttr))

	var folds, initial []term.Range
	// Section fold iterators wait for their trees, which become ready on
	// the loop, so they are drained off it.
	it, ok := s.tree.Folds()
	require.True(t, ok)
	folds, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Contains(t, folds, term.Range{Start: xy(0, 0), End: xy(9, 4)}, "the host's script element")
	assert.Contains(t, folds, term.Range{Start: xy(14, 1), End: xy(2, 3)}, "the function body, in file coordinates")

	it, ok = s.tree.InitialFolds()
	require.True(t, ok)
	done := make(chan error, 1)
	go func() {
		var err error
		initial, err = iterator.ToSlice(context.Background(), it)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(svelteWait):
		t.Fatal("initial folds did not complete")
	}
	assert.NotNil(t, initial)
}

func TestSvelteCompositeSelectionIntegration(t *testing.T) {
	xy := func(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }
	s := newSvelteTree(t, newAllSveltePackages(t), "<script>\n\tconst a = 1;\n</script>\n")
	s.waitLocation(svelteLocation(xy(1, 1), xy(6, 1), svelteKeywordAttr))

	caret := xy(7, 1)
	current := term.Range{Start: caret, End: caret}
	var got []term.Range
	s.locked(func() {
		for range 8 {
			next, ok := s.tree.SelectionExpand(current)
			if !ok {
				break
			}
			got = append(got, next)
			current = next
		}
	})
	require.NotEmpty(t, got)
	for i := 1; i < len(got); i++ {
		assert.True(t, rangeContains(got[i], got[i-1]),
			"expansion %d (%#v) must contain the previous one (%#v)", i, got[i], got[i-1])
	}
	assert.Equal(t, term.Range{Start: xy(7, 1), End: xy(8, 1)}, got[0], "the identifier")
	rawText := term.Range{Start: xy(8, 0), End: xy(0, 2)}
	scriptElement := term.Range{Start: xy(0, 0), End: xy(9, 2)}
	rawTextAt := slices.Index(got, rawText)
	require.GreaterOrEqual(t, rawTextAt, 1, "the host's raw_text follows the script's own nodes: %v", got)
	assert.Equal(t, scriptElement, got[rawTextAt+1], "then the host's script_element")
	assert.Equal(t, term.Range{Start: xy(0, 0), End: xy(0, 3)}, got[len(got)-1], "then the document")

	s.locked(func() {
		statement := term.Range{Start: xy(1, 1), End: xy(13, 1)}
		inner, ok := s.tree.SelectionShrink(statement, caret)
		require.True(t, ok)
		assert.True(t, rangeContains(statement, inner))
	})
}

func TestSvelteCompositeIndentationAtIntegration(t *testing.T) {
	// Rows: 0 <div>, 1 \t<script>, 2 (blank), 3 \t\tfunction f() {,
	// 4 (blank), 5 \t\t}, 6 (blank), 7 \t</script>, 8 </div>
	s := newSvelteTree(t, newAllSveltePackages(t),
		"<div>\n\t<script>\n\n\t\tfunction f() {\n\n\t\t}\n\n\t</script>\n</div>\n")
	s.waitLocation(svelteLocation(term.Coordinates{X: 2, Y: 3}, term.Coordinates{X: 10, Y: 3}, svelteKeywordAttr))

	cases := []struct {
		name string
		line int
		want int
	}{
		{name: "inside the function adds a level to the block's base", line: 4, want: 3},
		{name: "after the last statement keeps the block's base", line: 6, want: 2},
		{name: "the closing brace dedents to the base", line: 5, want: 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s.locked(func() {
				got, ok := s.tree.IndentationAt(c.line)
				require.True(t, ok)
				assert.Equal(t, c.want, got)
			})
		})
	}
	t.Run("a blank line before the first statement follows the host", func(t *testing.T) {
		host := newSvelteHostTree(t, "<div>\n\t<script>\n\n\t\tfunction f() {\n\n\t\t}\n\n\t</script>\n</div>\n")
		want, wantOK := host.IndentationAt(2)
		s.locked(func() {
			got, ok := s.tree.IndentationAt(2)
			assert.Equal(t, wantOK, ok)
			assert.Equal(t, want, got)
		})
	})
}

// newSvelteComponent is newYAMLTestCase for a svelte buffer: the tabs
// install syntaxTree and the component's notifications are recorded.
func newSvelteComponent(
	t *testing.T, pkgs syntax.PkgManager, syntaxTree syntax.NewTreeFunc,
) (*sync.Mutex, *text.Component, *recordingNotifications, func()) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)

	mu := new(sync.Mutex)
	scfg := svelteSyntaxConfig(mu)
	ed := vi.Editor(vi.WithStatusBarConfig(true, text.StatusBarConfig{
		Publisher:        texttest.NopEditor(),
		ScheduleNextTick: scfg.ScheduleNextTick,
	}))
	w := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
	notis := &recordingNotifications{}
	tcfg := text.DefaultConfig()
	tcfg.ScheduleNextTick = scfg.ScheduleNextTick
	tcfg.Syntax = scfg
	tcfg.SyntaxTree = syntaxTree
	tcfg.PkgManager = pkgs
	tcfg.Notifications = notis
	tcfg.EventPublisher = func(term.Event) bool { return true }
	comp, err := text.NewComponent(ed, w, tcfg)
	require.NoError(t, err)
	comp.Browser().Resize(60, 20)
	return mu, comp, notis, func() {
		_ = comp.Close()
		_ = scheme.Close()
	}
}

func waitSyntaxLocation(t *testing.T, mu *sync.Mutex, h text.Handler, want textapi.Location) {
	t.Helper()
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(syntaxLocations(h.LocationLists()), want)
	}, svelteWait, 5*time.Millisecond, "waiting for highlight %+v", want)
}

func pressKeys(t *testing.T, h text.Handler, input string) {
	t.Helper()
	keys, err := term.ParseKeys(input)
	require.NoError(t, err)
	for _, key := range keys {
		ev := term.Event{Type: term.EventKey, Ch: key.Ch, Key: key.Key, Mod: key.Mod}
		_, handled := h.Handle(ev)
		require.True(t, handled, "%s", ev.KeyComb().String())
	}
}

func TestSvelteCompositeEnterIndentsIntegration(t *testing.T) {
	xy := func(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }
	cases := []struct {
		name    string
		content string
		// ready is a highlight of the script, published once its tree
		// is parsed.
		ready textapi.Location
		keys  string
		want  string
	}{
		{
			name:    "a block at one tab opens a function body at two",
			content: "<script>\n\tfunction f() {\n\t}\n</script>\n",
			ready:   svelteLocation(xy(1, 1), xy(9, 1), svelteKeywordAttr),
			keys:    "ggjA<enter>x<esc>",
			want:    "<script>\n\tfunction f() {\n\t\tx\n\t}\n</script>\n",
		},
		{
			name:    "a block at one tab keeps its base after the last statement",
			content: "<script>\n\tlet a = 1;\n</script>\n",
			ready:   svelteLocation(xy(1, 1), xy(4, 1), svelteKeywordAttr),
			keys:    "ggjA<enter>x<esc>",
			want:    "<script>\n\tlet a = 1;\n\tx\n</script>\n",
		},
		{
			name:    "a block at column zero opens a function body at one tab",
			content: "<script>\nfunction f() {\n}\n</script>\n",
			ready:   svelteLocation(xy(0, 1), xy(8, 1), svelteKeywordAttr),
			keys:    "ggjA<enter>x<esc>",
			want:    "<script>\nfunction f() {\n\tx\n}\n</script>\n",
		},
		{
			name:    "a nested block keeps the base of its first statement",
			content: "<div>\n\t<script>\n\t\tlet a = 1;\n\t</script>\n</div>\n",
			ready:   svelteLocation(xy(2, 2), xy(5, 2), svelteKeywordAttr),
			keys:    "ggjjA<enter>x<esc>",
			want:    "<div>\n\t<script>\n\t\tlet a = 1;\n\t\tx\n\t</script>\n</div>\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mu, comp, _, cleanup := newSvelteComponent(t, newAllSveltePackages(t), multitree.New)
			defer cleanup()
			mu.Lock()
			_, h := newEditFileName(t, mu, comp, c.content,
				strconv.Itoa(int(i.Add(1)))+".svelte")
			mu.Unlock()
			waitSyntaxLocation(t, mu, h, c.ready)

			mu.Lock()
			pressKeys(t, h, c.keys)
			got := h.CellView().String()
			mu.Unlock()
			assert.Equal(t, c.want, got)
		})
	}

	hostRuled := []struct {
		name    string
		content string
		keys    string
		// ready is a highlight of the script, which only the composite
		// publishes; hostReady is one of the host's.
		ready, hostReady textapi.Location
	}{
		{
			name:      "the script line itself",
			content:   "<div>\n\t<script>\n\t\tlet a = 1;\n\t</script>\n</div>\n",
			keys:      "ggjA<enter>x<esc>",
			ready:     svelteLocation(xy(2, 2), xy(5, 2), svelteKeywordAttr),
			hostReady: svelteLocation(xy(1, 0), xy(4, 0), svelteTagAttr),
		},
		{
			name:      "a line inside an html comment",
			content:   "<div>\n\t<!--\n\t\tnote\n\t-->\n\t<script>\n\t\tlet a = 1;\n\t</script>\n</div>\n",
			keys:      "ggjjA<enter>x<esc>",
			ready:     svelteLocation(xy(2, 5), xy(5, 5), svelteKeywordAttr),
			hostReady: svelteLocation(xy(1, 0), xy(4, 0), svelteTagAttr),
		},
	}
	for _, c := range hostRuled {
		t.Run(c.name+" follows the host", func(t *testing.T) {
			run := func(tree syntax.NewTreeFunc, ready textapi.Location) string {
				mu, comp, _, cleanup := newSvelteComponent(t, newAllSveltePackages(t), tree)
				defer cleanup()
				mu.Lock()
				_, h := newEditFileName(t, mu, comp, c.content,
					strconv.Itoa(int(i.Add(1)))+".svelte")
				mu.Unlock()
				waitSyntaxLocation(t, mu, h, ready)
				mu.Lock()
				defer mu.Unlock()
				pressKeys(t, h, c.keys)
				return h.CellView().String()
			}
			want := run(treesitter.New, c.hostReady)
			assert.NotEqual(t, c.content, want, "the host tree must have indented")
			assert.Equal(t, want, run(multitree.New, c.ready))
		})
	}
}

func TestSvelteCompositeMissingGrammarIntegration(t *testing.T) {
	xy := func(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }
	pkgs := newPromptingPackages(t, []string{"svelte", "javascript", "css", "comment"},
		"svelte", "javascript")
	s := newSvelteTree(t, pkgs,
		"<script>\n\tlet a = 1;\n</script>\n<!-- note -->\n"+
			"<style>\n\tp { color: red; }\n</style>\n<style>\n\ta { color: red; }\n</style>\n")
	s.waitLocation(svelteLocation(xy(1, 1), xy(4, 1), svelteKeywordAttr))

	// A block's language is installed the way opening a file in it is:
	// through the editor's package manager, which asks the user.
	pkgs.waitPrompts(t, "css", "comment")
	s.waitSections(4)
	s.locked(func() {
		assert.False(t, s.hasLocation(svelteLocation(xy(5, 5), xy(10, 5), sveltePropertyAttr)),
			"the style block waits for the answer")
	})

	pkgs.install(t, "css")
	pkgs.decline("comment")
	s.waitLocation(svelteLocation(xy(5, 5), xy(10, 5), sveltePropertyAttr))
	s.waitLocation(svelteLocation(xy(5, 8), xy(10, 8), sveltePropertyAttr))
	assert.ElementsMatch(t, []string{"css", "comment"}, pkgs.asked())
	s.locked(func() {
		assert.Empty(t, s.notis.messages, "neither an installed nor a declined package warns")
	})
}

func TestSvelteCompositeParseInstallIntegration(t *testing.T) {
	xy := func(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }
	mu := new(sync.Mutex)
	cfg := svelteSyntaxConfig(mu)
	buf := cell.NewBuffer()
	_, err := buf.ReadFrom(strings.NewReader(svelteComponentContent))
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("memory:///parsed.svelte")
	require.NoError(t, err)

	parsed := multitree.Parse(context.Background(), newAllSveltePackages(t), uri, buf, cfg)
	var published [][]textapi.Location
	loc := syntax.FuncLocationSetter(func(ll textapi.LocationList) {
		published = append(published, collectLocations(ll))
	})
	mu.Lock()
	tree := parsed.Install(&recordingNotifications{}, term.NopInterrupter(), loc,
		nopFlusherCloser{}, nil)
	installed := len(published)
	sections := tree.(*multitree.Tree).Sections()
	mu.Unlock()
	t.Cleanup(func() { require.NoError(t, tree.Close()) })

	require.GreaterOrEqual(t, installed, 1, "Install publishes the host's highlights")
	assert.Contains(t, published[0], svelteLocation(xy(1, 0), xy(7, 0), svelteTagAttr))
	assert.Len(t, sections, 2, "Install creates the sections of the parsed host")
	assert.Same(t, tree, buf.View())

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		if len(published) == 0 {
			return false
		}
		last := published[len(published)-1]
		for _, l := range last {
			if l == svelteLocation(xy(1, 1), xy(6, 1), svelteKeywordAttr) {
				return true
			}
		}
		return false
	}, svelteWait, 5*time.Millisecond, "the sections highlight once their trees are parsed")
}

func TestMultitreeMatchesTreesitterForSingleLanguageIntegration(t *testing.T) {
	type result struct {
		locs  []textapi.Location
		state syntax.State
	}
	run := func(newTree syntax.NewTreeFunc) result {
		mu := new(sync.Mutex)
		cfg := svelteSyntaxConfig(mu)
		buf := cell.NewBuffer()
		_, err := buf.ReadFrom(strings.NewReader(fileContent))
		require.NoError(t, err)
		uri, err := workspaceapi.ParseURI("memory:///same.go")
		require.NoError(t, err)
		var locs []textapi.Location
		loc := syntax.FuncLocationSetter(func(ll textapi.LocationList) {
			locs = collectLocations(ll)
		})
		mu.Lock()
		tree := newTree(context.Background(), &recordingNotifications{}, term.NopInterrupter(),
			newInstalledPkgManager(t), loc, uri, buf, nopFlusherCloser{}, nil, cfg)
		mu.Unlock()
		t.Cleanup(func() { require.NoError(t, tree.Close()) })
		svc, ok := tree.(interface {
			State() iterator.Iterator[syntax.State]
		})
		require.True(t, ok)
		it := svc.State()
		defer it.Close()
		state, ok := it.Next(context.Background())
		require.True(t, ok)
		mu.Lock()
		defer mu.Unlock()
		return result{locs: locs, state: state}
	}
	single := run(treesitter.New)
	composite := run(multitree.New)
	require.NotEmpty(t, single.locs)
	assert.Equal(t, single.state, composite.state)
	assert.Equal(t, single.locs, composite.locs)
}
