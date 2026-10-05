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

package extension

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/ide/syntax/grammarfixture"
)

// grammarPkgManager serves the prebuilt tree-sitter grammars checked in
// under internal/ide/syntax/syntaxtest. Each call re-reads the directory so a
// review issuing several Highlight calls gets a fresh iterator.
type grammarPkgManager struct{ root string }

func (p grammarPkgManager) LibDir(
	_ context.Context, langID string,
) (iterator.Iterator[string], error) {
	dir := filepath.Join(p.root, langID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return iterator.FromSlice[string](nil), nil
	}
	files := []string{grammarfixture.Parser(dir)}
	for _, e := range entries {
		if e.IsDir() || e.Name() == syntax.ParserFilename {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	return iterator.FromSlice(files), nil
}

// realParser builds a parser over the checked-in grammars. The tests
// skip when the .so is unavailable (e.g. a cross-compiled checkout).
func realParser(t *testing.T, languages ...string) syntaxapi.Parser {
	t.Helper()
	wd, err := os.Getwd()
	require.NoError(t, err)
	root, err := filepath.Abs(filepath.Join(
		wd, "..", "..", "..", "internal", "ide", "syntax", "syntaxtest"))
	require.NoError(t, err)
	for _, lang := range languages {
		grammarfixture.ParserPath(t, filepath.Join(root, lang))
	}
	wuri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	// Highlight never reads the workspace: it parses the content it is
	// handed, so no file system is needed.
	return syntax.NewParser(nil, grammarPkgManager{root: root}, wuri)
}

// cellAt returns the cell at (x, y) of buf rendered by reviewBuffer.
func cellAt(t *testing.T, review changesReview, p syntaxapi.Parser, x, y int) term.Cell {
	t.Helper()
	buf := reviewBuffer(t.Context(), review, p)
	c, ok := buf.Cell(term.Coordinates{X: x, Y: y})
	require.Truef(t, ok, "no cell at %d,%d", x, y)
	return c
}

// rowOf finds the buffer row whose text equals want.
func rowOf(t *testing.T, buf string, want string) int {
	t.Helper()
	for y, line := range strings.Split(buf, "\n") {
		if line == want {
			return y
		}
	}
	t.Fatalf("no row %q in:\n%s", want, buf)
	return -1
}

const goUpdatePatch = "*** Begin Patch\n" +
	"*** Update File: server.go\n" +
	"@@ func serve\n" +
	" func serve() {\n" +
	"-\tconst old = \"a\"\n" +
	"+\tconst new = \"b\"\n" +
	" }\n" +
	"*** End Patch"

func goReview(t *testing.T) changesReview {
	t.Helper()
	review, err := reviewChanges([]llmapi.Message{
		assistant(patchCall(t, "c1", goUpdatePatch)),
		toolResult("c1", okResult),
	}, workspaceView{})
	require.NoError(t, err)
	return review
}

func TestReviewBufferHighlightsBothDiffSidesE2E(t *testing.T) {
	p := realParser(t, "go")
	review := goReview(t)

	text := reviewBuffer(t.Context(), review, p).String()
	delRow := rowOf(t, text, "-\tconst old = \"a\"")
	addRow := rowOf(t, text, "+\tconst new = \"b\"")

	// Column 0 is the gutter, column 1 the tab, so `const` starts at 2.
	for _, tc := range []struct {
		name   string
		row    int
		wantBg term.Color
	}{
		{"removed line", delRow, term.GetColor("darkred")},
		{"added line", addRow, term.GetColor("darkgreen")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for x := 2; x < 2+len("const"); x++ {
				c := cellAt(t, review, p, x, tc.row)
				assert.Equalf(t, term.ColorYellow, c.Fg,
					"keyword cell %d,%d (%q) must be highlighted",
					x, tc.row, string(c.Ch))
				assert.Equalf(t, tc.wantBg, c.Bg,
					"cell %d,%d must keep the diff tint", x, tc.row)
			}
		})
	}
}

func TestReviewBufferGutterUnhighlightedE2E(t *testing.T) {
	p := realParser(t, "go")
	review := goReview(t)

	text := reviewBuffer(t.Context(), review, p).String()
	for _, tc := range []struct {
		row    int
		wantCh rune
		wantBg term.Color
	}{
		{rowOf(t, text, "-\tconst old = \"a\""), '-', term.GetColor("darkred")},
		{rowOf(t, text, "+\tconst new = \"b\""), '+', term.GetColor("darkgreen")},
	} {
		c := cellAt(t, review, p, 0, tc.row)
		assert.Equal(t, tc.wantCh, c.Ch)
		assert.Equal(t, term.ColorDefault, c.Fg)
		assert.Equal(t, tc.wantBg, c.Bg)
	}
}

func TestReviewBufferHeadersUnhighlightedE2E(t *testing.T) {
	p := realParser(t, "go")
	review := goReview(t)

	require.Len(t, review.headers, 1)
	h := review.headers[0]
	for x := range len([]rune(h.text)) {
		c := cellAt(t, review, p, x, h.row)
		assert.Equalf(t, term.ColorDefault, c.Fg,
			"header cell %d,%d (%q)", x, h.row, string(c.Ch))
		assert.NotZerof(t, c.Attrs&term.AttrBold,
			"header cell %d,%d (%q) must stay bold", x, h.row, string(c.Ch))
	}
}

func TestReviewBufferHighlightsAddedFileE2E(t *testing.T) {
	p := realParser(t, "go")
	review, err := reviewChanges([]llmapi.Message{
		assistant(patchCall(t, "c1", "*** Begin Patch\n"+
			"*** Add File: main.go\n"+
			"+package main\n"+
			"+\n"+
			"+func main() {}\n"+
			"*** End Patch")),
		toolResult("c1", okResult),
	}, workspaceView{})
	require.NoError(t, err)

	text := reviewBuffer(t.Context(), review, p).String()
	row := rowOf(t, text, "+package main")
	c := cellAt(t, review, p, 1, row)
	assert.Equal(t, 'p', c.Ch)
	assert.Equal(t, term.ColorYellow, c.Fg, "`package` is a keyword")
	assert.Equal(t, term.GetColor("darkgreen"), c.Bg)
}

func TestReviewBufferHighlightsMultipleLanguagesE2E(t *testing.T) {
	p := realParser(t, "go", "rust")
	review, err := reviewChanges([]llmapi.Message{
		assistant(
			patchCall(t, "c1", "*** Begin Patch\n"+
				"*** Add File: main.go\n"+
				"+package main\n"+
				"*** End Patch"),
			patchCall(t, "c2", "*** Begin Patch\n"+
				"*** Add File: lib.rs\n"+
				"+fn main() {}\n"+
				"*** End Patch"),
		),
		toolResult("c1", okResult),
		toolResult("c2", okResult),
	}, workspaceView{})
	require.NoError(t, err)

	text := reviewBuffer(t.Context(), review, p).String()
	goRow := rowOf(t, text, "+package main")
	rsRow := rowOf(t, text, "+fn main() {}")

	assert.Equal(t, term.ColorYellow, cellAt(t, review, p, 1, goRow).Fg,
		"go `package` keyword")
	assert.Equal(t, term.ColorYellow, cellAt(t, review, p, 1, rsRow).Fg,
		"rust `fn` keyword")
}

func TestReviewBufferUnknownLanguageE2E(t *testing.T) {
	p := realParser(t, "go")
	review, err := reviewChanges([]llmapi.Message{
		assistant(patchCall(t, "c1", "*** Begin Patch\n"+
			"*** Add File: notes.xyzlang\n"+
			"+package main\n"+
			"*** End Patch")),
		toolResult("c1", okResult),
	}, workspaceView{})
	require.NoError(t, err)

	text := reviewBuffer(t.Context(), review, p).String()
	row := rowOf(t, text, "+package main")
	c := cellAt(t, review, p, 1, row)
	assert.Equal(t, term.ColorDefault, c.Fg)
	assert.Equal(t, term.GetColor("darkgreen"), c.Bg)
}

func TestReviewBufferHighlightingPreservesTextE2E(t *testing.T) {
	p := realParser(t, "go")
	review := goReview(t)

	assert.Equal(t,
		reviewBuffer(t.Context(), review, nil).String(),
		reviewBuffer(t.Context(), review, p).String())
}

func TestCommandAdapterReviewChangesHighlightsE2E(t *testing.T) {
	p := realParser(t, "go")
	a, wm, _, _ := newReviewAdapter(t, []llmapi.Message{
		assistant(patchCall(t, "c1", goUpdatePatch)),
		toolResult("c1", okResult),
	})
	a.parser = p

	f := openReview(t, a, wm)
	w := term.NewStringWriter(80, 20)
	f.Draw(w)
	require.NoError(t, w.Flush())

	var keyword, tinted bool
	for _, c := range w.Cells() {
		if c.Fg == term.ColorYellow && c.Ch == 'c' {
			keyword = true
		}
		if c.Bg == term.GetColor("darkgreen") && c.Fg == term.ColorYellow {
			tinted = true
		}
	}
	assert.True(t, keyword, "keywords must be highlighted in the window")
	assert.True(t, tinted, "highlighted cells must keep the diff tint")
}

func TestReviewBufferHighlightsExpandedContextE2E(t *testing.T) {
	p := realParser(t, "go")
	const file = "package main\n" +
		"\n" +
		"import \"fmt\"\n" +
		"\n" +
		"func serve() {\n" +
		"\tconst b = 2\n" +
		"}\n"
	patch := "*** Begin Patch\n*** Update File: server.go\n@@\n" +
		"-\tconst a = 1\n+\tconst b = 2\n*** End Patch"

	review, err := reviewChanges([]llmapi.Message{
		assistant(patchCall(t, "c1", patch)),
		toolResult("c1", okResult),
	}, expandFrom(map[string]string{"server.go": file}))
	require.NoError(t, err)

	text := reviewBuffer(t.Context(), review, p).String()
	require.Contains(t, text, " import \"fmt\"",
		"surrounding source must be pulled into the diff")

	// `import` is a keyword on a context line: highlighted, untinted.
	row := rowOf(t, text, " import \"fmt\"")
	c := cellAt(t, review, p, 1, row)
	assert.Equal(t, 'i', c.Ch)
	assert.Equal(t, term.ColorYellow, c.Fg)
	assert.Equal(t, term.ColorDefault, c.Bg, "context lines carry no tint")
}
