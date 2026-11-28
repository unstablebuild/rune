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

package treesitter

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/syntax"
)

// fixtureFiles returns the absolute paths of the package files of the
// fixture grammar of lang, skipping the test on a platform it was not
// built for.
func fixtureFiles(t *testing.T, lang string) []string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "syntaxtest", lang))
	require.NoError(t, err)
	parser := filepath.Join(dir, ParserFilename)
	if runtime.GOOS != "darwin" {
		parser = filepath.Join(dir, runtime.GOOS+"_"+runtime.GOARCH, ParserFilename)
	}
	if _, err := os.Stat(parser); err != nil {
		t.Skipf("no tree-sitter fixture for %s/%s at %s", runtime.GOOS, runtime.GOARCH, parser)
	}
	files := []string{parser}
	for _, q := range []string{
		HighlightsFilename, IndentsFilename, FoldsFilename, LocalsFilename, InjectionsFilename,
	} {
		if p := filepath.Join(dir, q); fileExists(p) {
			files = append(files, p)
		}
	}
	return files
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// installFixture copies the fixture package of lang into dir/version.
func installFixture(t *testing.T, lang, dir, version string) {
	t.Helper()
	dst := filepath.Join(dir, version)
	require.NoError(t, os.MkdirAll(dst, 0o755))
	for _, src := range fixtureFiles(t, lang) {
		data, err := os.ReadFile(src)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dst, filepath.Base(src)), data, 0o644))
	}
}

// countingLoader counts the grammars next loads and closes.
type countingLoader struct {
	next          grammarLoader
	loads, closes atomic.Int32
}

func (l *countingLoader) load(f grammarFiles) (*grammar, error) {
	g, err := l.next.load(f)
	if err != nil {
		return nil, err
	}
	l.loads.Add(1)
	inner := g.close
	g.close = func() error {
		l.closes.Add(1)
		return inner()
	}
	return g, nil
}

// useGrammarLoader makes l the loader of the trees the test creates.
func useGrammarLoader(t *testing.T, l grammarLoader) {
	prev := grammars
	grammars = l
	t.Cleanup(func() { grammars = prev })
}

// parseWith parses content with the fixture grammar of lang as Parse does
// for a file of that language, without installing the tree.
func parseWith(t *testing.T, lang, content string) *parsedTree {
	t.Helper()
	buf := cell.NewBuffer()
	_, err := buf.ReadFrom(strings.NewReader(content))
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("memory:///f." + map[string]string{
		"go": "go", "markdown": "md",
	}[lang])
	require.NoError(t, err)
	cfg := syntax.DefaultConfig()
	cfg.ScheduleNextTick = func(fn func()) bool { fn(); return true }
	p := Parse(context.Background(), &countingPkgManager{files: fixtureFiles(t, lang)},
		uri, buf, cfg).(*parsedTree)
	require.NoError(t, p.init.err)
	return p
}

const grammarTestGo = "package main\n\nfunc main() {\n\tx := 1\n}\n"

func TestSharedGrammarLoader(t *testing.T) {
	newLoader := func() (*sharedGrammarLoader, *countingLoader) {
		next := &countingLoader{next: nativeGrammarLoader{}}
		return &sharedGrammarLoader{next: next}, next
	}
	load := func(t *testing.T, l grammarLoader, files []string) *grammar {
		t.Helper()
		g, err := l.load(newGrammarFiles("go", files))
		require.NoError(t, err)
		return g
	}

	t.Run("loads of the same files share a grammar", func(t *testing.T) {
		l, next := newLoader()
		files := fixtureFiles(t, "go")
		a, b := load(t, l, files), load(t, l, files)
		assert.Equal(t, int32(1), next.loads.Load())
		assert.Same(t, a.language, b.language)
		assert.Same(t, a.highlights, b.highlights)
		require.NoError(t, a.close())
		require.NoError(t, b.close())
	})

	t.Run("the grammar is closed with its last reference", func(t *testing.T) {
		l, next := newLoader()
		files := fixtureFiles(t, "go")
		a, b := load(t, l, files), load(t, l, files)
		require.NoError(t, a.close())
		assert.Zero(t, next.closes.Load())
		require.NoError(t, b.close())
		assert.Equal(t, int32(1), next.closes.Load())

		c := load(t, l, files)
		assert.Equal(t, int32(2), next.loads.Load(), "a closed grammar is loaded again")
		require.NoError(t, c.close())
	})

	t.Run("closing a reference twice releases it once", func(t *testing.T) {
		l, next := newLoader()
		files := fixtureFiles(t, "go")
		a, b := load(t, l, files), load(t, l, files)
		for range 3 {
			require.NoError(t, a.close())
		}
		assert.Zero(t, next.closes.Load(), "b still holds the grammar")
		require.NoError(t, b.close())
		assert.Equal(t, int32(1), next.closes.Load())
	})

	t.Run("another version gets a new grammar and the old one lives on", func(t *testing.T) {
		dir := t.TempDir()
		installFixture(t, "go", dir, "1")
		installFixture(t, "go", dir, "2")
		lib := filepath.Join(dir, "go")
		use := func(version string) {
			t.Helper()
			_ = os.Remove(lib)
			require.NoError(t, os.Symlink(filepath.Join(dir, version), lib))
		}
		var files []string
		for _, f := range fixtureFiles(t, "go") {
			files = append(files, filepath.Join(lib, filepath.Base(f)))
		}

		l, next := newLoader()
		use("1")
		old := load(t, l, files)
		use("2")
		upgraded := load(t, l, files)
		assert.Equal(t, int32(2), next.loads.Load())
		assert.NotSame(t, old.language, upgraded.language)
		assert.Zero(t, next.closes.Load(), "the trees on the old version keep it")

		later := time.Now().Add(time.Hour)
		require.NoError(t, os.Chtimes(filepath.Join(dir, "2", ParserFilename), later, later))
		reinstalled := load(t, l, files)
		assert.Equal(t, int32(3), next.loads.Load(), "a reinstall gets a new grammar")

		for _, g := range []*grammar{old, upgraded, reinstalled} {
			require.NoError(t, g.close())
		}
		assert.Equal(t, int32(3), next.closes.Load())
	})

	t.Run("trees load, parse, highlight and close concurrently", func(t *testing.T) {
		l, next := newLoader()
		files := fixtureFiles(t, "go")
		content := []byte(grammarTestGo)
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() {
				for range 20 {
					g, err := l.load(newGrammarFiles("go", files))
					if !assert.NoError(t, err) {
						return
					}
					parser := tree_sitter.NewParser()
					assert.NoError(t, parser.SetLanguage(g.language))
					tree := parser.Parse(content, nil)
					cur := tree_sitter.NewQueryCursor()
					matches := cur.Matches(g.highlights, tree.RootNode(), content)
					n := 0
					for _, ok := matches.Next(); ok; _, ok = matches.Next() {
						n++
					}
					assert.NotZero(t, n)
					cur.Close()
					tree.Close()
					parser.Close()
					assert.NoError(t, g.close())
				}
			})
		}
		wg.Wait()
		assert.Equal(t, next.loads.Load(), next.closes.Load())
	})

	t.Run("closing a tree leaves the others of its grammar working", func(t *testing.T) {
		l, next := newLoader()
		useGrammarLoader(t, l)
		const content = "# Title\n\nSome *text*.\n\n```go\nfunc f() {}\n```\n"
		closed, open := parseWith(t, "markdown", content), parseWith(t, "markdown", content)
		require.Equal(t, int32(1), next.loads.Load())

		var wg sync.WaitGroup
		wg.Go(func() {
			for range 200 {
				open.t.mu.Lock()
				assert.NotEmpty(t, open.t.getHighlights(open.t.cellBytes, open.t.content))
				assert.NotEmpty(t, open.t.getFolds(false))
				assert.NotEmpty(t, open.t.getInjections())
				open.t.mu.Unlock()
			}
		})
		require.NoError(t, closed.Close())
		wg.Wait()
		assert.Zero(t, next.closes.Load())
		require.NoError(t, open.Close())
		assert.Equal(t, int32(1), next.closes.Load())
	})
}
