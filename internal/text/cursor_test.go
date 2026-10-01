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

package text

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/workspace"
)

func TestCursorHiddenLines(t *testing.T) {
	t.Run("move up and down across hidden lines", func(t *testing.T) {
		c := setupCursor(t, 10, 10, false)
		require.True(t, c.scroll.MarkHidden(1, 3))

		assert.True(t, c.MoveDown())
		assert.True(t, c.MoveDown())
		assert.True(t, c.MoveDown())
		assert.True(t, c.MoveDown())
		assert.True(t, c.MoveUp())
		assert.True(t, c.MoveUp())
		assert.True(t, c.MoveUp())
		assert.True(t, c.MoveUp())
	})
	t.Run("insert below hidden lines block, inserts below last hidden line", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "a\nb\nc\nd\ne", false)
		require.True(t, c.scroll.MarkHidden(1, 3))

		assert.True(t, c.MoveDown())
		c.InsertLineBelow(IndentRuneTab, 0)
		assert.Equal(t, "a\nb\nc\nd\n\ne", c.buffer().String())
	})
}

func TestCursorStartEndWord(t *testing.T) {
	suite := []struct {
		setCursorAtScroll       term.Coordinates
		expectStartWord         bool
		expectEndWord           bool
		rightInclusiveSemantics bool
	}{
		{term.Coordinates{}, false, false, true},
		{term.Coordinates{Y: 1}, false, false, true},
		{term.Coordinates{Y: 1, X: 1}, false, false, true},
		{term.Coordinates{Y: 2, X: 3}, true, false, true},
		{term.Coordinates{Y: 2, X: 4}, false, true, true},
		{term.Coordinates{Y: 2, X: 75}, true, false, true},
		{term.Coordinates{Y: 2, X: 76}, false, true, true},
		{term.Coordinates{Y: 5, X: 1}, true, false, true},
		{term.Coordinates{Y: 5, X: 0}, false, false, true},
		{term.Coordinates{Y: 18, X: 11}, true, false, true},
		{term.Coordinates{Y: 18, X: 14}, false, true, true},
		{term.Coordinates{Y: 18, X: 18}, false, false, true},
		{term.Coordinates{Y: 9, X: 6}, true, true, true},
		{term.Coordinates{}, false, false, false},
		{term.Coordinates{Y: 1, X: 1}, false, false, false},
		{term.Coordinates{Y: 1, X: 2}, false, false, false},
		{term.Coordinates{Y: 2, X: 3}, true, false, false},
		{term.Coordinates{Y: 2, X: 4}, false, false, false},
		{term.Coordinates{Y: 2, X: 5}, false, true, false},
		{term.Coordinates{Y: 2, X: 75}, true, false, false},
		{term.Coordinates{Y: 2, X: 76}, false, false, false},
		{term.Coordinates{Y: 2, X: 77}, false, true, false},
		{term.Coordinates{Y: 5, X: 1}, true, false, false},
		{term.Coordinates{Y: 5, X: 0}, false, false, false},
		{term.Coordinates{Y: 18, X: 11}, true, false, false},
		{term.Coordinates{Y: 18, X: 17}, false, false, false},
		{term.Coordinates{Y: 18, X: 15}, false, true, false},
		{term.Coordinates{Y: 9, X: 6}, true, false, false},
		{term.Coordinates{Y: 9, X: 7}, false, true, false},
	}

	for i, test := range suite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			c := setupCursor(t, 10, 10, false)
			c.RightInclusiveSemantics = test.rightInclusiveSemantics

			c.MoveToScroll(test.setCursorAtScroll)
			actualStartWord := c.IsStartWord()
			actualEndWord := c.IsEndWord()

			word := c.Word()
			assert.Equal(t, test.expectStartWord, actualStartWord, "IsStartWord is incorrect: %q", word)
			assert.Equal(t, test.expectEndWord, actualEndWord, "IsEndWord is incorrect: %q", word)
		})
	}
}

type cursorTextObjectCase struct {
	name     string
	content  string
	at       term.Coordinates
	selectFn func(*Cursor) bool
	wantOK   bool
	want     string
}

func runCursorTextObjectCases(t *testing.T, cases []cursorTextObjectCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 80, 10, tc.content, false)
			_, _ = c.MoveToScroll(tc.at)

			ok := tc.selectFn(c)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.want, c.Selection())
			}
		})
	}
}

func TestCursorTextObjects(t *testing.T) {
	runCursorTextObjectCases(t, []cursorTextObjectCase{
		{
			name:     "iw inside word",
			content:  "one two three",
			at:       term.Coordinates{X: 5},
			selectFn: func(c *Cursor) bool { return c.SelectInnerWord() },
			wantOK:   true,
			want:     "two",
		},
		{
			name:     "iw at word start",
			content:  "one two",
			at:       term.Coordinates{X: 4},
			selectFn: func(c *Cursor) bool { return c.SelectInnerWord() },
			wantOK:   true,
			want:     "two",
		},
		{
			name:     "iw at word end",
			content:  "one two",
			at:       term.Coordinates{X: 6},
			selectFn: func(c *Cursor) bool { return c.SelectInnerWord() },
			wantOK:   true,
			want:     "two",
		},
		{
			name:     "iw on leading whitespace prefers next word",
			content:  "one  two",
			at:       term.Coordinates{X: 3},
			selectFn: func(c *Cursor) bool { return c.SelectInnerWord() },
			wantOK:   true,
			want:     "two",
		},
		{
			name:     "iw on trailing whitespace prefers previous word same line",
			content:  "one two  ",
			at:       term.Coordinates{X: 8},
			selectFn: func(c *Cursor) bool { return c.SelectInnerWord() },
			wantOK:   true,
			want:     "two",
		},
		{
			name:     "aw prefers trailing whitespace",
			content:  "one two three",
			at:       term.Coordinates{X: 5},
			selectFn: func(c *Cursor) bool { return c.SelectAWord() },
			wantOK:   true,
			want:     "two ",
		},
		{
			name:     "aw at last word uses leading whitespace",
			content:  "one two",
			at:       term.Coordinates{X: 6},
			selectFn: func(c *Cursor) bool { return c.SelectAWord() },
			wantOK:   true,
			want:     " two",
		},
		{
			name:     "iw on punctuation selects punctuation object",
			content:  "foo...bar",
			at:       term.Coordinates{X: 4},
			selectFn: func(c *Cursor) bool { return c.SelectInnerWord() },
			wantOK:   true,
			want:     "...",
		},
		{
			name:     "iW groups punctuation with non blanks",
			content:  "one two-three",
			at:       term.Coordinates{X: 8},
			selectFn: func(c *Cursor) bool { return c.SelectInnerWordGroup() },
			wantOK:   true,
			want:     "two-three",
		},
		{
			name:     "aW at end of line uses leading whitespace",
			content:  "one two-three",
			at:       term.Coordinates{X: len("one two-three")},
			selectFn: func(c *Cursor) bool { return c.SelectAWordGroup() },
			wantOK:   true,
			want:     " two-three",
		},
		{
			name:     "2iw spans two words from first word",
			content:  "one two three",
			at:       term.Coordinates{X: 0},
			selectFn: func(c *Cursor) bool { return c.SelectInnerWords(2) },
			wantOK:   true,
			want:     "one two",
		},
		{
			name:     "2aw spans two a-word objects",
			content:  "one two three",
			at:       term.Coordinates{X: 0},
			selectFn: func(c *Cursor) bool { return c.SelectAWords(2) },
			wantOK:   true,
			want:     "one two ",
		},
		{
			name:     "2iW spans two WORD objects",
			content:  "one two-three four",
			at:       term.Coordinates{X: 0},
			selectFn: func(c *Cursor) bool { return c.SelectInnerWordGroups(2) },
			wantOK:   true,
			want:     "one two-three",
		},
		{
			name:     "2iw from whitespace chooses next two words",
			content:  "  one two three",
			at:       term.Coordinates{X: 0},
			selectFn: func(c *Cursor) bool { return c.SelectInnerWords(2) },
			wantOK:   true,
			want:     "one two",
		},
		{
			name:     "2aw from end of line uses previous then next objects not available",
			content:  "one two",
			at:       term.Coordinates{X: len("one two")},
			selectFn: func(c *Cursor) bool { return c.SelectAWords(2) },
			wantOK:   true,
			want:     " two",
		},
		{
			name:     "iw on spaces only line fails",
			content:  "   ",
			at:       term.Coordinates{X: 1},
			selectFn: func(c *Cursor) bool { return c.SelectInnerWord() },
			wantOK:   false,
		},
		{
			name:     "i quote inside text",
			content:  `say "hello world" now`,
			at:       term.Coordinates{X: 7},
			selectFn: func(c *Cursor) bool { return c.SelectInnerQuote('"') },
			wantOK:   true,
			want:     "hello world",
		},
		{
			name:     "a quote on opening delimiter",
			content:  `say "hello" now`,
			at:       term.Coordinates{X: 4},
			selectFn: func(c *Cursor) bool { return c.SelectAQuote('"') },
			wantOK:   true,
			want:     `"hello"`,
		},
		{
			name:     "a quote on closing delimiter",
			content:  `say "hello" now`,
			at:       term.Coordinates{X: 10},
			selectFn: func(c *Cursor) bool { return c.SelectAQuote('"') },
			wantOK:   true,
			want:     `"hello"`,
		},
		{
			name:     "quote ignores escaped delimiters",
			content:  `say "he\"llo" now`,
			at:       term.Coordinates{X: 8},
			selectFn: func(c *Cursor) bool { return c.SelectInnerQuote('"') },
			wantOK:   true,
			want:     `he\"llo`,
		},
		{
			name:     "single quote works",
			content:  "say 'hello' now",
			at:       term.Coordinates{X: 6},
			selectFn: func(c *Cursor) bool { return c.SelectInnerQuote('\'') },
			wantOK:   true,
			want:     "hello",
		},
		{
			name:     "backtick quote works",
			content:  "say `hello` now",
			at:       term.Coordinates{X: 6},
			selectFn: func(c *Cursor) bool { return c.SelectInnerQuote('`') },
			wantOK:   true,
			want:     "hello",
		},
		{
			name:     "empty inner quote is valid empty selection",
			content:  `say "" now`,
			at:       term.Coordinates{X: 5},
			selectFn: func(c *Cursor) bool { return c.SelectInnerQuote('"') },
			wantOK:   true,
			want:     "",
		},
		{
			name:     "unmatched quote fails",
			content:  `say "hello now`,
			at:       term.Coordinates{X: 6},
			selectFn: func(c *Cursor) bool { return c.SelectInnerQuote('"') },
			wantOK:   false,
		},
		{
			name:     "multiline quote is not matched",
			content:  "say \"hello\nworld\" now",
			at:       term.Coordinates{X: 6, Y: 0},
			selectFn: func(c *Cursor) bool { return c.SelectInnerQuote('"') },
			wantOK:   false,
		},
		{
			name:     "inner parens basic",
			content:  "call(one, two)",
			at:       term.Coordinates{X: 6},
			selectFn: func(c *Cursor) bool { return c.SelectInnerBlock('(', ')') },
			wantOK:   true,
			want:     "one, two",
		},
		{
			name:     "around parens basic",
			content:  "call(one, two)",
			at:       term.Coordinates{X: 6},
			selectFn: func(c *Cursor) bool { return c.SelectABlock('(', ')') },
			wantOK:   true,
			want:     "(one, two)",
		},
		{
			name:     "block chooses nearest nested pair",
			content:  "((a)(b))",
			at:       term.Coordinates{X: 5},
			selectFn: func(c *Cursor) bool { return c.SelectABlock('(', ')') },
			wantOK:   true,
			want:     "(b)",
		},
		{
			name:     "block works from closing delimiter",
			content:  "call(one)",
			at:       term.Coordinates{X: 8},
			selectFn: func(c *Cursor) bool { return c.SelectABlock('(', ')') },
			wantOK:   true,
			want:     "(one)",
		},
		{
			name:     "empty inner block is valid empty selection",
			content:  "call()",
			at:       term.Coordinates{X: 4},
			selectFn: func(c *Cursor) bool { return c.SelectInnerBlock('(', ')') },
			wantOK:   true,
			want:     "",
		},
		{
			name:     "unmatched block fails",
			content:  "call(one",
			at:       term.Coordinates{X: 6},
			selectFn: func(c *Cursor) bool { return c.SelectABlock('(', ')') },
			wantOK:   false,
		},
		{
			name:     "multiline block selects across lines",
			content:  "call(\none,\ntwo\n)",
			at:       term.Coordinates{X: 2, Y: 1},
			selectFn: func(c *Cursor) bool { return c.SelectABlock('(', ')') },
			wantOK:   true,
			want:     "(\none,\ntwo\n)",
		},
		{
			name:     "multiline nested block selects nearest pair",
			content:  "(a\n(b)\nc)",
			at:       term.Coordinates{X: 1, Y: 1},
			selectFn: func(c *Cursor) bool { return c.SelectABlock('(', ')') },
			wantOK:   true,
			want:     "(b)",
		},
		{
			name:     "nested mixed delimiters select bracket pair",
			content:  "fn({[abc]})",
			at:       term.Coordinates{X: 5},
			selectFn: func(c *Cursor) bool { return c.SelectABlock('[', ']') },
			wantOK:   true,
			want:     "[abc]",
		},
		{
			name:     "3aw from middle word consumes three objects",
			content:  "zero one two three",
			at:       term.Coordinates{X: 6},
			selectFn: func(c *Cursor) bool { return c.SelectAWords(3) },
			wantOK:   true,
			want:     "one two three",
		},
		{
			name:     "3iw from middle word consumes current and next two words",
			content:  "zero one two three",
			at:       term.Coordinates{X: 6},
			selectFn: func(c *Cursor) bool { return c.SelectInnerWords(3) },
			wantOK:   true,
			want:     "one two three",
		},
		{
			name:     "inner paragraph basic",
			content:  "one\ntwo\n\nthree\nfour\n",
			at:       term.Coordinates{Y: 3},
			selectFn: func(c *Cursor) bool { return c.SelectInnerParagraph() },
			wantOK:   true,
			want:     "three\nfour\n",
		},
		{
			name:     "around paragraph includes surrounding blanks",
			content:  "one\ntwo\n\nthree\nfour\n\n\n",
			at:       term.Coordinates{Y: 3},
			selectFn: func(c *Cursor) bool { return c.SelectAParagraph() },
			wantOK:   true,
			want:     "\nthree\nfour\n\n\n",
		},
		{
			name:     "paragraph from blank separator chooses next paragraph",
			content:  "one\n\n two\nthree",
			at:       term.Coordinates{Y: 1},
			selectFn: func(c *Cursor) bool { return c.SelectInnerParagraph() },
			wantOK:   true,
			want:     " two\nthree",
		},
		{
			name:     "paragraph from trailing blank lines chooses previous paragraph",
			content:  "one\ntwo\n\n\n",
			at:       term.Coordinates{Y: 3},
			selectFn: func(c *Cursor) bool { return c.SelectInnerParagraph() },
			wantOK:   true,
			want:     "one\ntwo\n",
		},
		{
			name:     "paragraph spaces only content fails",
			content:  " \n\t\n",
			at:       term.Coordinates{Y: 0},
			selectFn: func(c *Cursor) bool { return c.SelectInnerParagraph() },
			wantOK:   false,
		},
		{
			name:     "inner sentence basic",
			content:  "One. Two! Three?",
			at:       term.Coordinates{X: 6},
			selectFn: func(c *Cursor) bool { return c.SelectInnerSentence() },
			wantOK:   true,
			want:     "Two!",
		},
		{
			name:     "around sentence includes trailing separator spaces",
			content:  "One. Two!  Three?",
			at:       term.Coordinates{X: 6},
			selectFn: func(c *Cursor) bool { return c.SelectASentence() },
			wantOK:   true,
			want:     "Two!  ",
		},
		{
			name:     "around last sentence uses leading spaces",
			content:  "One. Two!",
			at:       term.Coordinates{X: 8},
			selectFn: func(c *Cursor) bool { return c.SelectASentence() },
			wantOK:   true,
			want:     " Two!",
		},
		{
			name:     "sentence from inter sentence whitespace chooses next sentence",
			content:  "One.  Two!",
			at:       term.Coordinates{X: 4},
			selectFn: func(c *Cursor) bool { return c.SelectInnerSentence() },
			wantOK:   true,
			want:     "Two!",
		},
		{
			name:     "sentence punctuation without following whitespace does not split",
			content:  "e.g.test",
			at:       term.Coordinates{X: 3},
			selectFn: func(c *Cursor) bool { return c.SelectInnerSentence() },
			wantOK:   true,
			want:     "e.g.test",
		},
		{
			name:     "sentence without punctuation selects whole line",
			content:  "just words here",
			at:       term.Coordinates{X: 5},
			selectFn: func(c *Cursor) bool { return c.SelectInnerSentence() },
			wantOK:   true,
			want:     "just words here",
		},
		{
			name:     "sentence blank line fails",
			content:  "",
			at:       term.Coordinates{},
			selectFn: func(c *Cursor) bool { return c.SelectInnerSentence() },
			wantOK:   false,
		},
	})
}

func TestCursorSelectABlockEdges(t *testing.T) {
	type blockCase struct {
		name       string
		content    string
		at         term.Coordinates
		open       rune
		close      rune
		wantOK     bool
		want       string
		wantCursor term.Coordinates
	}

	for _, tc := range []blockCase{
		{
			name:       "single line at contents",
			content:    "{abc}",
			at:         term.Coordinates{X: 2},
			open:       '{',
			close:      '}',
			wantOK:     true,
			want:       "{abc}",
			wantCursor: term.Coordinates{X: 4},
		},
		{
			name:       "single line at opening delimiter",
			content:    "{abc}",
			at:         term.Coordinates{X: 0},
			open:       '{',
			close:      '}',
			wantOK:     true,
			want:       "{abc}",
			wantCursor: term.Coordinates{X: 4},
		},
		{
			name:       "single line at closing delimiter",
			content:    "{abc}",
			at:         term.Coordinates{X: 4},
			open:       '{',
			close:      '}',
			wantOK:     true,
			want:       "{abc}",
			wantCursor: term.Coordinates{X: 4},
		},
		{
			name:       "spaced contents",
			content:    "fn({ a b })",
			at:         term.Coordinates{X: 6},
			open:       '{',
			close:      '}',
			wantOK:     true,
			want:       "{ a b }",
			wantCursor: term.Coordinates{X: 9},
		},
		{
			name:       "tabbed contents",
			content:    "fn(\t[\ta\tb\t]\t)",
			at:         term.Coordinates{X: 6},
			open:       '[',
			close:      ']',
			wantOK:     true,
			want:       "[\ta\tb\t]",
			wantCursor: term.Coordinates{X: 10},
		},
		{
			name:       "multiline block",
			content:    "call({\n\talpha\n\tbeta\n})",
			at:         term.Coordinates{Y: 2, X: 2},
			open:       '{',
			close:      '}',
			wantOK:     true,
			want:       "{\n\talpha\n\tbeta\n}",
			wantCursor: term.Coordinates{Y: 3, X: 0},
		},
		{
			name:       "nested block chooses nearest",
			content:    "outer({inner})",
			at:         term.Coordinates{X: 8},
			open:       '{',
			close:      '}',
			wantOK:     true,
			want:       "{inner}",
			wantCursor: term.Coordinates{X: 12},
		},
		{
			name:       "mixed delimiters are ignored",
			content:    "fn({[abc]})",
			at:         term.Coordinates{X: 5},
			open:       '{',
			close:      '}',
			wantOK:     true,
			want:       "{[abc]}",
			wantCursor: term.Coordinates{X: 9},
		},
		{
			name:       "empty block",
			content:    "call()",
			at:         term.Coordinates{X: 4},
			open:       '(',
			close:      ')',
			wantOK:     true,
			want:       "()",
			wantCursor: term.Coordinates{X: 5},
		},
		{
			name:    "unclosed block fails",
			content: "call({abc)",
			at:      term.Coordinates{X: 7},
			open:    '{',
			close:   '}',
			wantOK:  false,
		},
		{
			name:    "unopened block fails",
			content: "call(abc})",
			at:      term.Coordinates{X: 6},
			open:    '{',
			close:   '}',
			wantOK:  false,
		},
		{
			name:    "empty buffer fails",
			content: "",
			at:      term.Coordinates{},
			open:    '{',
			close:   '}',
			wantOK:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 80, 10, tc.content, false)
			_, _ = c.MoveToScroll(tc.at)

			ok := c.SelectABlockClose(tc.open, tc.close)
			assert.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				return
			}
			assert.Equal(t, tc.want, c.Selection())
			assert.Equal(t, tc.wantCursor, c.CursorAtScroll())
		})
	}
}

func TestCursorTextObjectDeleteSelection(t *testing.T) {
	type deleteCase struct {
		name       string
		content    string
		at         term.Coordinates
		selectFn   func(*Cursor) bool
		wantBuffer string
		wantOK     bool
	}

	for _, tc := range []deleteCase{
		{
			name:       "delete inner word from trailing spaces",
			content:    "one two  ",
			at:         term.Coordinates{X: 8},
			selectFn:   func(c *Cursor) bool { return c.SelectInnerWord() },
			wantBuffer: "one   ",
			wantOK:     true,
		},
		{
			name:       "delete around last word removes leading separator",
			content:    "one two",
			at:         term.Coordinates{X: 6},
			selectFn:   func(c *Cursor) bool { return c.SelectAWord() },
			wantBuffer: "one",
			wantOK:     true,
		},
		{
			name:       "delete two inner words",
			content:    "one two three",
			at:         term.Coordinates{X: 0},
			selectFn:   func(c *Cursor) bool { return c.SelectInnerWords(2) },
			wantBuffer: " three",
			wantOK:     true,
		},
		{
			name:       "delete two a-words",
			content:    "one two three",
			at:         term.Coordinates{X: 0},
			selectFn:   func(c *Cursor) bool { return c.SelectAWords(2) },
			wantBuffer: "three",
			wantOK:     true,
		},
		{
			name:       "delete inner empty quote keeps delimiters",
			content:    `say "" now`,
			at:         term.Coordinates{X: 5},
			selectFn:   func(c *Cursor) bool { return c.SelectInnerQuote('"') },
			wantBuffer: `say "" now`,
			wantOK:     false,
		},
		{
			name:       "delete around empty quote removes delimiters",
			content:    `say "" now`,
			at:         term.Coordinates{X: 5},
			selectFn:   func(c *Cursor) bool { return c.SelectAQuote('"') },
			wantBuffer: "say  now",
			wantOK:     true,
		},
		{
			name:       "delete inner empty block keeps delimiters",
			content:    "call()",
			at:         term.Coordinates{X: 4},
			selectFn:   func(c *Cursor) bool { return c.SelectInnerBlock('(', ')') },
			wantBuffer: "call()",
			wantOK:     false,
		},
		{
			name:       "delete around empty block removes delimiters",
			content:    "call()",
			at:         term.Coordinates{X: 4},
			selectFn:   func(c *Cursor) bool { return c.SelectABlock('(', ')') },
			wantBuffer: "call",
			wantOK:     true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 80, 10, tc.content, false)
			_, _ = c.MoveToScroll(tc.at)
			require.True(t, tc.selectFn(c))
			assert.Equal(t, tc.wantOK, c.DeleteSelection())
			assert.Equal(t, tc.wantBuffer, c.buffer().String())
		})
	}
}

func TestCursorUpperLowercase(t *testing.T) {
	suite := []struct {
		from, to     term.Coordinates
		inputBuffer  string
		outputBuffer string
		op           func(t *testing.T, c *Cursor)
	}{
		{
			from:         term.Coordinates{},
			to:           term.Coordinates{},
			inputBuffer:  "a",
			outputBuffer: "A",
			op: func(t *testing.T, c *Cursor) {
				assert.True(t, c.UppercaseSelection())
			},
		},
		{
			from:         term.Coordinates{},
			to:           term.Coordinates{},
			inputBuffer:  "\ta",
			outputBuffer: "\ta",
			op: func(t *testing.T, c *Cursor) {
				assert.True(t, c.UppercaseSelection())
			},
		},
		{
			from:         term.Coordinates{},
			to:           term.Coordinates{X: 4},
			inputBuffer:  "\ta",
			outputBuffer: "\tA",
			op: func(t *testing.T, c *Cursor) {
				assert.True(t, c.UppercaseSelection())
			},
		},
		{
			from:         term.Coordinates{},
			to:           term.Coordinates{X: 4},
			inputBuffer:  "\tA",
			outputBuffer: "\ta",
			op: func(t *testing.T, c *Cursor) {
				assert.True(t, c.LowercaseSelection())
			},
		},
		{
			from:         term.Coordinates{},
			to:           term.Coordinates{X: 8},
			inputBuffer:  "\t\ta",
			outputBuffer: "\t\tA",
			op: func(t *testing.T, c *Cursor) {
				assert.True(t, c.UppercaseSelection())
			},
		},
		{
			from:         term.Coordinates{},
			to:           term.Coordinates{Y: 2, X: 1},
			inputBuffer:  "\t\n\ta\nb",
			outputBuffer: "\t\n\tA\nB",
			op: func(t *testing.T, c *Cursor) {
				assert.True(t, c.UppercaseSelection())
			},
		},
		{
			from:         term.Coordinates{},
			to:           term.Coordinates{X: 1},
			inputBuffer:  "\x00a",
			outputBuffer: "\x00A",
			op: func(t *testing.T, c *Cursor) {
				assert.True(t, c.UppercaseSelection())
			},
		},
		{
			from:         term.Coordinates{Y: 1, X: 2},
			to:           term.Coordinates{Y: 3, X: 1},
			inputBuffer:  "func\n\ta word something else\nb\nhello",
			outputBuffer: "func\n\ta WORD SOMETHING ELSE\nB\nHEllo",
			op: func(t *testing.T, c *Cursor) {
				assert.True(t, c.UppercaseSelection())
				assert.True(t, c.Undo())
				assert.True(t, c.Redo())
			},
		},
		{
			from:         term.Coordinates{},
			to:           term.Coordinates{X: 2},
			inputBuffer:  "aBc",
			outputBuffer: "AbC",
			op: func(t *testing.T, c *Cursor) {
				assert.True(t, c.ToggleCaseSelection())
			},
		},
		{
			from:         term.Coordinates{},
			to:           term.Coordinates{Y: 1, X: 4},
			inputBuffer:  "Hello\nWorld",
			outputBuffer: "hELLO\nwORLD",
			op: func(t *testing.T, c *Cursor) {
				assert.True(t, c.ToggleCaseSelection())
			},
		},
		{
			from:         term.Coordinates{X: 1},
			to:           term.Coordinates{X: 3},
			inputBuffer:  "a123b",
			outputBuffer: "a123b",
			op: func(t *testing.T, c *Cursor) {
				assert.True(t, c.ToggleCaseSelection())
			},
		},
	}

	for i, test := range suite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			c := setupCursorContent(t, 10, 10, test.inputBuffer, false)
			c.RightInclusiveSemantics = true

			c.MoveToScroll(test.from)
			c.Select()
			c.MoveToScroll(test.to)

			// sut
			test.op(t, c)

			assert.Equal(t, test.outputBuffer, term.CellsToString(c.view().RawCells()))
		})
	}
}

func TestIndent(t *testing.T) {
	suite := []struct {
		inputBuffer          string
		expectIndentationAt  int
		indentRune           rune
		expectIndent         bool
		cursorAtScroll       term.Coordinates
		outputBuffer         string
		expectCursorAtScroll term.Coordinates
	}{
		{
			inputBuffer:          "",
			expectIndentationAt:  0,
			indentRune:           IndentRuneTab,
			expectIndent:         true,
			cursorAtScroll:       term.Coordinates{},
			outputBuffer:         "\t",
			expectCursorAtScroll: term.Coordinates{X: 1},
		},
		{
			inputBuffer:          "a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneTab,
			expectIndent:         true,
			cursorAtScroll:       term.Coordinates{},
			outputBuffer:         "\ta",
			expectCursorAtScroll: term.Coordinates{X: 1},
		},
		{
			inputBuffer:          "\ta",
			expectIndentationAt:  1,
			indentRune:           IndentRuneTab,
			expectIndent:         true,
			cursorAtScroll:       term.Coordinates{},
			outputBuffer:         "\t\ta",
			expectCursorAtScroll: term.Coordinates{X: 1},
		},
		{
			inputBuffer:          "\ta",
			expectIndentationAt:  2,
			indentRune:           IndentRuneTab,
			expectIndent:         true,
			cursorAtScroll:       term.Coordinates{X: 1},
			outputBuffer:         "\t\ta",
			expectCursorAtScroll: term.Coordinates{X: 2},
		},
		{
			inputBuffer:          "\ta",
			expectIndentationAt:  0,
			indentRune:           IndentRuneTab,
			expectIndent:         true,
			cursorAtScroll:       term.Coordinates{X: 1},
			outputBuffer:         "\t\ta",
			expectCursorAtScroll: term.Coordinates{X: 2},
		},
		{
			inputBuffer:          "\t\ta",
			expectIndentationAt:  0,
			indentRune:           IndentRuneTab,
			expectIndent:         true,
			cursorAtScroll:       term.Coordinates{X: 2},
			outputBuffer:         "\t\t\ta",
			expectCursorAtScroll: term.Coordinates{X: 3},
		},
		{
			inputBuffer:          "a\ta",
			expectIndentationAt:  0,
			indentRune:           IndentRuneTab,
			expectIndent:         true,
			cursorAtScroll:       term.Coordinates{X: 2},
			outputBuffer:         "a\t\ta",
			expectCursorAtScroll: term.Coordinates{X: 3},
		},
		{
			inputBuffer:          "a\ta",
			expectIndentationAt:  1,
			indentRune:           IndentRuneTab,
			expectIndent:         true,
			cursorAtScroll:       term.Coordinates{X: 2},
			outputBuffer:         "\ta\ta",
			expectCursorAtScroll: term.Coordinates{X: 3},
		},
		{
			inputBuffer:          "\t\ta\ta",
			expectIndentationAt:  1,
			indentRune:           IndentRuneTab,
			expectIndent:         true,
			cursorAtScroll:       term.Coordinates{X: 2},
			outputBuffer:         "\t\t\ta\ta",
			expectCursorAtScroll: term.Coordinates{X: 3},
		},
		{
			inputBuffer:          "\taX",
			expectIndentationAt:  2,
			indentRune:           IndentRuneTab,
			expectIndent:         true,
			cursorAtScroll:       term.Coordinates{X: 2},
			outputBuffer:         "\t\taX",
			expectCursorAtScroll: term.Coordinates{X: 3},
		},
		{
			inputBuffer:          "\t\taX",
			expectIndentationAt:  1,
			indentRune:           IndentRuneTab,
			expectIndent:         true,
			cursorAtScroll:       term.Coordinates{X: 3},
			outputBuffer:         "\t\ta\tX",
			expectCursorAtScroll: term.Coordinates{X: 4},
		},
	}

	for i, test := range suite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			c := setupCursorContent(t, 10, 10, test.inputBuffer, false)
			mock := &mockIndentService{returnIndentationAt: test.expectIndentationAt}
			mock.View = c.buffer().WithView(mock)
			c.MoveToScroll(test.cursorAtScroll)
			assert.Equal(t, test.expectIndent, c.TryIndent(test.indentRune, 0))
			assert.Equal(t, test.outputBuffer, term.CellsToString(c.view().RawCells()))
			if test.expectIndent {
				assert.Equal(t, test.expectCursorAtScroll, c.CursorAtScroll())
			}
		})
	}
}

func TestCursorReindent(t *testing.T) {
	suite := []struct {
		name                 string
		inputBuffer          string
		expectIndentationAt  int
		indentRune           rune
		tabspaces            int
		cursorAtScroll       term.Coordinates
		expectReindent       bool
		outputBuffer         string
		expectCursorAtScroll term.Coordinates
	}{
		{
			name:                 "adds indentation",
			inputBuffer:          "a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneTab,
			tabspaces:            component.DefaultTabspaces,
			cursorAtScroll:       term.Coordinates{},
			expectReindent:       true,
			outputBuffer:         "\ta",
			expectCursorAtScroll: term.Coordinates{X: 1},
		},
		{
			name:                 "removes indentation",
			inputBuffer:          "\t\ta",
			expectIndentationAt:  1,
			indentRune:           IndentRuneTab,
			tabspaces:            component.DefaultTabspaces,
			cursorAtScroll:       term.Coordinates{X: 2},
			expectReindent:       true,
			outputBuffer:         "\ta",
			expectCursorAtScroll: term.Coordinates{X: 1},
		},
		{
			name:                 "already indented still reports available",
			inputBuffer:          "\ta",
			expectIndentationAt:  1,
			indentRune:           IndentRuneTab,
			tabspaces:            component.DefaultTabspaces,
			cursorAtScroll:       term.Coordinates{X: 1},
			expectReindent:       true,
			outputBuffer:         "\ta",
			expectCursorAtScroll: term.Coordinates{X: 1},
		},
		{
			name:                 "spaces add indentation using tabspaces width",
			inputBuffer:          "a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneSpace,
			tabspaces:            4,
			cursorAtScroll:       term.Coordinates{},
			expectReindent:       true,
			outputBuffer:         "    a",
			expectCursorAtScroll: term.Coordinates{X: 4},
		},
		{
			name:                 "spaces remove indentation to target width",
			inputBuffer:          "        a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneSpace,
			tabspaces:            4,
			cursorAtScroll:       term.Coordinates{X: 8},
			expectReindent:       true,
			outputBuffer:         "    a",
			expectCursorAtScroll: term.Coordinates{X: 4},
		},
		{
			name:                 "spaces already at target indentation",
			inputBuffer:          "    a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneSpace,
			tabspaces:            4,
			cursorAtScroll:       term.Coordinates{X: 4},
			expectReindent:       true,
			outputBuffer:         "    a",
			expectCursorAtScroll: term.Coordinates{X: 4},
		},
		{
			name:                 "spaces honor two-column tabspaces",
			inputBuffer:          "    a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneSpace,
			tabspaces:            2,
			cursorAtScroll:       term.Coordinates{X: 4},
			expectReindent:       true,
			outputBuffer:         "  a",
			expectCursorAtScroll: term.Coordinates{X: 2},
		},
		{
			name:                 "mixed tabs and spaces preserve existing mixed indent when target reached",
			inputBuffer:          "\t  a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneSpace,
			tabspaces:            4,
			cursorAtScroll:       term.Coordinates{X: 3},
			expectReindent:       true,
			outputBuffer:         "\t  a",
			expectCursorAtScroll: term.Coordinates{X: 1},
		},
		{
			name:                 "mixed spaces and tabs preserve extra indentation when already tab-indented",
			inputBuffer:          "  \ta",
			expectIndentationAt:  1,
			indentRune:           IndentRuneTab,
			tabspaces:            component.DefaultTabspaces,
			cursorAtScroll:       term.Coordinates{X: 3},
			expectReindent:       true,
			outputBuffer:         "\t  \ta",
			expectCursorAtScroll: term.Coordinates{X: 4},
		},
		{
			name:                 "only spaces with tab indent prepends tab",
			inputBuffer:          "  a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneTab,
			tabspaces:            component.DefaultTabspaces,
			cursorAtScroll:       term.Coordinates{X: 2},
			expectReindent:       true,
			outputBuffer:         "\t  a",
			expectCursorAtScroll: term.Coordinates{X: 3},
		},
		{
			name:                 "only tabs with space indent remains valid at same visual width",
			inputBuffer:          "\ta",
			expectIndentationAt:  1,
			indentRune:           IndentRuneSpace,
			tabspaces:            4,
			cursorAtScroll:       term.Coordinates{X: 1},
			expectReindent:       true,
			outputBuffer:         "\ta",
			expectCursorAtScroll: term.Coordinates{X: 1},
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			c := setupCursorContent(t, 10, 10, test.inputBuffer, false)
			c.scroll.SetTabspaces(test.tabspaces)
			mock := &mockIndentService{returnIndentationAt: test.expectIndentationAt}
			mock.View = c.buffer().WithView(mock)
			c.MoveToScroll(test.cursorAtScroll)

			assert.Equal(t, test.expectReindent, c.Reindent(test.indentRune, 0))
			assert.Equal(t, test.outputBuffer, term.CellsToString(c.view().RawCells()))
			assert.Equal(t, test.expectCursorAtScroll, c.CursorAtScroll())
		})
	}

	t.Run("no indent service returns false", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "a", false)
		assert.False(t, c.Reindent(IndentRuneTab, 0))
		assert.Equal(t, "a", term.CellsToString(c.view().RawCells()))
		assert.Equal(t, term.Coordinates{}, c.CursorAtScroll())
	})
}

// TestCursorReindentHonorsTabspacesArgument verifies that when Reindent
// is invoked with an explicit tabspaces argument, the cursor uses that
// width rather than the scroll's configured tabspaces. This guards
// against misindenting files whose indent width differs from the
// editor's configured tabspaces.
func TestCursorReindentHonorsTabspacesArgument(t *testing.T) {
	// Scroll tabspaces configured to 4 but caller passes 2-space indent.
	c := setupCursorContent(t, 20, 10, "a", false)
	c.scroll.SetTabspaces(4)
	mock := &mockIndentService{returnIndentationAt: 1}
	mock.View = c.buffer().WithView(mock)

	assert.True(t, c.Reindent(IndentRuneSpace, 2))
	assert.Equal(t, "  a", term.CellsToString(c.view().RawCells()))
	assert.Equal(t, term.Coordinates{X: 2}, c.CursorAtScroll())
}

// TestCursorShiftLineRightHonorsTabspacesArgument verifies the explicit
// indent width is honored by the shift/dedent path as well.
func TestCursorShiftLineRightHonorsTabspacesArgument(t *testing.T) {
	c := setupCursorContent(t, 20, 10, "a", false)
	c.scroll.SetTabspaces(4)

	c.ShiftLineRight(IndentRuneSpace, 2)
	assert.Equal(t, "  a", c.scroll.Buffer().String())
	assert.Equal(t, term.Coordinates{X: 2}, c.cursor)

	assert.True(t, c.ShiftLineLeft(IndentRuneSpace, 2))
	assert.Equal(t, "a", c.scroll.Buffer().String())
}

func TestCursorTryIndentWithConfiguredIndentRune(t *testing.T) {
	suite := []struct {
		name                 string
		inputBuffer          string
		expectIndentationAt  int
		indentRune           rune
		tabspaces            int
		cursorAtScroll       term.Coordinates
		expectIndent         bool
		outputBuffer         string
		expectCursorAtScroll term.Coordinates
	}{
		{
			name:                 "spaces indent using tabspaces width",
			inputBuffer:          "a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneSpace,
			tabspaces:            4,
			cursorAtScroll:       term.Coordinates{},
			expectIndent:         true,
			outputBuffer:         "    a",
			expectCursorAtScroll: term.Coordinates{X: 4},
		},
		{
			name:                 "spaces with two-column tabspaces",
			inputBuffer:          "a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneSpace,
			tabspaces:            2,
			cursorAtScroll:       term.Coordinates{},
			expectIndent:         true,
			outputBuffer:         "  a",
			expectCursorAtScroll: term.Coordinates{X: 2},
		},
		{
			name:                 "mixed leading whitespace adds only missing spaces at start",
			inputBuffer:          "\t a",
			expectIndentationAt:  2,
			indentRune:           IndentRuneSpace,
			tabspaces:            4,
			cursorAtScroll:       term.Coordinates{X: 2},
			expectIndent:         true,
			outputBuffer:         "   \t a",
			expectCursorAtScroll: term.Coordinates{X: 5},
		},
		{
			name:                 "only spaces already indented enough",
			inputBuffer:          "    a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneSpace,
			tabspaces:            4,
			cursorAtScroll:       term.Coordinates{X: 4},
			expectIndent:         true,
			outputBuffer:         "        a",
			expectCursorAtScroll: term.Coordinates{X: 8},
		},
		{
			// RUNE-121: o<tab> on a 2-space indented YAML file. The line is
			// already at the syntax target so a tab keypress must add a full
			// indent level rather than no-op or dedent.
			name:                 "spaces at target inserts full indent level",
			inputBuffer:          "  a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneSpace,
			tabspaces:            2,
			cursorAtScroll:       term.Coordinates{X: 2},
			expectIndent:         true,
			outputBuffer:         "    a",
			expectCursorAtScroll: term.Coordinates{X: 4},
		},
		{
			// RUNE-121: a second tab from the position above must add another
			// full indent level rather than dedenting.
			name:                 "spaces above target inserts another full indent level",
			inputBuffer:          "    a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneSpace,
			tabspaces:            2,
			cursorAtScroll:       term.Coordinates{X: 4},
			expectIndent:         true,
			outputBuffer:         "      a",
			expectCursorAtScroll: term.Coordinates{X: 6},
		},
		{
			// RUNE-121 control case: line under-indented still snaps up to
			// the syntax target.
			name:                 "spaces under-indented snaps up to target",
			inputBuffer:          "a",
			expectIndentationAt:  1,
			indentRune:           IndentRuneSpace,
			tabspaces:            2,
			cursorAtScroll:       term.Coordinates{},
			expectIndent:         true,
			outputBuffer:         "  a",
			expectCursorAtScroll: term.Coordinates{X: 2},
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			c := setupCursorContent(t, 10, 10, test.inputBuffer, false)
			c.scroll.SetTabspaces(test.tabspaces)
			mock := &mockIndentService{returnIndentationAt: test.expectIndentationAt}
			mock.View = c.buffer().WithView(mock)
			c.MoveToScroll(test.cursorAtScroll)

			assert.Equal(t, test.expectIndent, c.TryIndent(test.indentRune, 0))
			assert.Equal(t, test.outputBuffer, term.CellsToString(c.view().RawCells()))
			assert.Equal(t, test.expectCursorAtScroll, c.CursorAtScroll())
		})
	}
}

// centerLines builds a buffer of n two digit lines so that a view of a known
// size can be asserted cell by cell.
func centerLines(n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%02d", i)
	}
	return b.String()
}

func drawScroll(t *testing.T, c *Cursor, width, height int) string {
	t.Helper()
	w := term.NewStringWriter(width, height)
	c.scroll.Draw(w)
	require.NoError(t, w.Flush())
	return w.String()
}

// TestCursorCenter covers Cursor.Center, which is what vim's zz maps to: the
// cursor's line moves to the middle row of the view. Around the end of the
// buffer the view keeps scrolling and leaves the rows below the last line
// empty, so that every line can reach the middle row.
func TestCursorCenter(t *testing.T) {
	// sampleSnippet is 32 lines, so with a 10 row view the middle row is 5
	// and the last line the view can show at its bottom is 22.
	suite := []struct {
		name              string
		width, height     int
		content           string
		setCursorAtScroll term.Coordinates
		wantHandled       bool
		wantWindowY       int
		wantOffsetY       int
	}{
		{
			name:  "first line cannot reach the middle row",
			width: 10, height: 10,
			setCursorAtScroll: term.Coordinates{Y: 0},
		},
		{
			name:  "line above the middle row cannot reach it either",
			width: 10, height: 10,
			setCursorAtScroll: term.Coordinates{Y: 2},
			wantWindowY:       2,
		},
		{
			name:  "line already on the middle row is a no-op",
			width: 10, height: 10,
			setCursorAtScroll: term.Coordinates{Y: 5},
			wantWindowY:       5,
		},
		{
			name:  "line below the middle row scrolls down to it",
			width: 10, height: 10,
			setCursorAtScroll: term.Coordinates{Y: 6},
			wantHandled:       true,
			wantWindowY:       5,
			wantOffsetY:       1,
		},
		{
			name:  "line in the middle of the buffer",
			width: 10, height: 10,
			setCursorAtScroll: term.Coordinates{Y: 20},
			wantHandled:       true,
			wantWindowY:       5,
			wantOffsetY:       15,
		},
		{
			name:  "last line that centers without passing the end",
			width: 10, height: 10,
			setCursorAtScroll: term.Coordinates{Y: 27},
			wantHandled:       true,
			wantWindowY:       5,
			wantOffsetY:       22,
		},
		{
			name:  "first line that only centers past the end",
			width: 10, height: 10,
			setCursorAtScroll: term.Coordinates{Y: 28},
			wantHandled:       true,
			wantWindowY:       5,
			wantOffsetY:       23,
		},
		{
			name:  "last line centers with empty rows below it",
			width: 10, height: 10,
			setCursorAtScroll: term.Coordinates{Y: 31},
			wantHandled:       true,
			wantWindowY:       5,
			wantOffsetY:       26,
		},
		{
			name:  "view taller than the buffer leaves the first line alone",
			width: 100, height: 100,
			setCursorAtScroll: term.Coordinates{Y: 0},
		},
		{
			name:  "view taller than the buffer cannot center the last line",
			width: 100, height: 100,
			setCursorAtScroll: term.Coordinates{Y: 31},
			wantWindowY:       31,
		},
		{
			name:  "single row view keeps the line on the only row",
			width: 10, height: 1,
			setCursorAtScroll: term.Coordinates{Y: 31},
			wantOffsetY:       31,
		},
		{
			name:  "three row view centers the last line past the end",
			width: 10, height: 3,
			setCursorAtScroll: term.Coordinates{Y: 31},
			wantHandled:       true,
			wantWindowY:       1,
			wantOffsetY:       30,
		},
		{
			name:  "buffer as tall as the view still centers its last line",
			width: 4, height: 10,
			content:           centerLines(10),
			setCursorAtScroll: term.Coordinates{Y: 9},
			wantHandled:       true,
			wantWindowY:       5,
			wantOffsetY:       4,
		},
		{
			name:  "single line buffer never scrolls",
			width: 4, height: 10,
			content:           "only",
			setCursorAtScroll: term.Coordinates{Y: 0},
		},
		{
			name:  "buffer shorter than half the view never scrolls",
			width: 4, height: 10,
			content:           centerLines(4),
			setCursorAtScroll: term.Coordinates{Y: 3},
			wantWindowY:       3,
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			content := test.content
			if content == "" {
				content = sampleSnippet
			}
			c := setupCursorContent(t, test.width, test.height, content, false)

			c.MoveToScroll(test.setCursorAtScroll)
			// subscribe once the cursor is in place so that only the
			// seeks performed by Center are counted.
			sub := &testScrollSubscriber{}
			c.scroll.Subscribe(sub)

			handled := c.Center()
			require.Equal(t, test.wantHandled, handled)

			assert.Equal(t, test.setCursorAtScroll, c.CursorAtScroll(),
				"the cursor must stay on its line")
			assert.Equal(t, test.wantWindowY, c.Coordinates().Y)
			assert.Equal(t, test.wantOffsetY, c.scroll.Offset().Y)
			assert.Zero(t, c.scroll.Offset().X,
				"centering must not scroll horizontally")

			win, _ := c.WindowCoordinates(c.CursorAtScroll())
			assert.Equal(t, c.Coordinates().Y, win.Y,
				"the cursor window row must agree with the scroll offset")

			if test.wantHandled {
				assert.NotZero(t, sub.seek)
			} else {
				assert.Zero(t, sub.seek,
					"a center that changes nothing must not notify subscribers")
			}
		})
	}
}

// TestCursorCenterContract pins the outcome of Cursor.Center for every
// combination of view height and cursor line: the view always ends up half a
// view above the cursor's line, clamped at the first line, and the cursor
// never leaves the view.
func TestCursorCenterContract(t *testing.T) {
	const rows = 32
	for _, height := range []int{1, 2, 3, 4, 5, 9, 10, 17, 31, 32, 33, 64} {
		for line := range rows {
			c := setupCursor(t, 10, height, false)
			require.Equal(t, rows, c.scroll.Buffer().Rows())

			c.MoveToScroll(term.Coordinates{Y: line})
			c.Center()

			offset := c.scroll.Offset()
			window := c.Coordinates()
			msg := fmt.Sprintf("height %d, line %d", height, line)
			assert.Equal(t, max(0, line-height/2), offset.Y, msg)
			assert.Equal(t, line-offset.Y, window.Y, msg)
			assert.GreaterOrEqual(t, window.Y, 0, msg)
			assert.Less(t, window.Y, height, msg)
			assert.Equal(t, term.Coordinates{Y: line}, c.CursorAtScroll(), msg)
		}
	}
}

// TestCursorCenterPastEndOfContent covers the cases where centering has to
// scroll the view beyond the last line of the buffer.
func TestCursorCenterPastEndOfContent(t *testing.T) {
	const (
		width  = 4
		height = 7
		lines  = 20
	)

	t.Run("leaves the rows below the last line empty", func(t *testing.T) {
		c := setupCursorContent(t, width, height, centerLines(lines), false)
		c.MoveToScroll(term.Coordinates{Y: lines - 1})

		require.True(t, c.Center())
		assert.Equal(t, 3, c.Coordinates().Y)
		assert.Equal(t, "16  \n17  \n18  \n19  \n    \n    \n    ",
			drawScroll(t, c, width, height))
	})

	t.Run("is idempotent", func(t *testing.T) {
		c := setupCursorContent(t, width, height, centerLines(lines), false)
		c.MoveToScroll(term.Coordinates{Y: lines - 1})

		require.True(t, c.Center())
		before := c.scroll.Offset()
		view := drawScroll(t, c, width, height)

		assert.False(t, c.Center())
		assert.Equal(t, before, c.scroll.Offset())
		assert.Equal(t, view, drawScroll(t, c, width, height))
	})

	t.Run("centers back from a view repositioned at the top", func(t *testing.T) {
		c := setupCursorContent(t, width, height, centerLines(lines), false)
		c.MoveToScroll(term.Coordinates{Y: lines - 1})

		require.True(t, c.Center())
		centered := c.scroll.Offset()

		require.True(t, c.RepositionTop())
		require.Equal(t, lines-1, c.scroll.Offset().Y,
			"the last line must be able to reach the top row")
		assert.Equal(t, 0, c.Coordinates().Y)

		require.True(t, c.Center())
		assert.Equal(t, centered, c.scroll.Offset())
		assert.Equal(t, 3, c.Coordinates().Y)
	})

	t.Run("keeps the horizontal offset", func(t *testing.T) {
		c := setupCursorContent(t, width, height, centerLines(lines), false)
		c.MoveToScroll(term.Coordinates{Y: lines - 1})
		require.True(t, c.scroll.SetOffset(
			term.Coordinates{X: 1, Y: c.scroll.Offset().Y}))

		require.True(t, c.Center())
		assert.Equal(t, 1, c.scroll.Offset().X)
	})

	t.Run("counts folded lines", func(t *testing.T) {
		c := setupCursorContent(t, width, height, centerLines(lines), false)
		require.True(t, c.scroll.MarkHidden(2, 8))
		c.MoveToScroll(term.Coordinates{Y: lines - 1})

		require.True(t, c.Center())
		assert.Equal(t, 3, c.Coordinates().Y)
		assert.Equal(t, term.Coordinates{Y: lines - 1}, c.CursorAtScroll())
		// six lines are hidden, so the same view sits six rows lower
		// in the buffer than it does without the fold.
		assert.Equal(t, 10, c.scroll.Offset().Y)
		assert.Equal(t, "16  \n17  \n18  \n19  \n    \n    \n    ",
			drawScroll(t, c, width, height))
	})

	t.Run("counts the rows a wrapped last line takes", func(t *testing.T) {
		c := setupCursorContent(t, 4, 6, "a\nb\nc\nd\ne\nf\ng\nh\niiiiiiii", true)
		c.MoveToScroll(term.Coordinates{Y: 8})

		require.True(t, c.Center())
		assert.Equal(t, 3, c.Coordinates().Y)
		assert.Equal(t, term.Coordinates{Y: 8}, c.CursorAtScroll())
		assert.Equal(t, "f   \ng   \nh   \niiii\niiii\n    ",
			drawScroll(t, c, 4, 6))
	})

	t.Run("inverted scrolls stay anchored at the end", func(t *testing.T) {
		c := setupCursorContent(t, width, height, centerLines(lines), false)
		c.scroll.InvertOffset = true
		// the cursor starts at the top, and with an inverted offset the
		// view already sits at the end of the content.
		c.MoveFirstLine()
		c.MoveToScroll(term.Coordinates{Y: lines - 1})
		before := c.scroll.Offset()

		assert.False(t, c.Center())
		assert.Equal(t, before, c.scroll.Offset())
		assert.Equal(t, height-1, c.Coordinates().Y)
		assert.LessOrEqual(t, c.scroll.SeekOffset(), c.scroll.MaxSeekOffset())
	})

	t.Run("does nothing without a view to center in", func(t *testing.T) {
		for _, size := range []term.Coordinates{{X: 0, Y: height}, {X: width, Y: 0}} {
			c := setupCursorContent(t, size.X, size.Y, centerLines(lines), false)
			c.MoveToScroll(term.Coordinates{Y: lines - 1})
			before := c.scroll.Offset()

			assert.False(t, c.Center())
			assert.Equal(t, before, c.scroll.Offset())
			assert.Equal(t, term.Coordinates{Y: lines - 1}, c.CursorAtScroll())
		}
	})

	t.Run("does nothing on an empty buffer", func(t *testing.T) {
		c := setupCursorContent(t, width, height, "", false)

		assert.False(t, c.Center())
		assert.Equal(t, term.Coordinates{}, c.scroll.Offset())
		assert.Equal(t, term.Coordinates{}, c.CursorAtScroll())
	})
}

func TestCursorRepositionTop(t *testing.T) {
	suite := []struct {
		width, height             int
		setCursorAtScroll         term.Coordinates
		expectedHandled           bool
		expectedWindowCoordinates term.Coordinates
	}{
		{10, 10, term.Coordinates{Y: 0}, false, term.Coordinates{Y: 0}},
		{10, 10, term.Coordinates{Y: 2}, true, term.Coordinates{Y: 0}},
		{10, 10, term.Coordinates{Y: 5}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 6}, true, term.Coordinates{Y: 0}},
		{10, 10, term.Coordinates{Y: 7}, true, term.Coordinates{Y: 0}},
		{10, 10, term.Coordinates{Y: 8}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 9}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 10}, true, term.Coordinates{Y: 0}},
		{10, 10, term.Coordinates{Y: 20}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 22}, true, term.Coordinates{X: 3, Y: 0}},
		// the last lines of the buffer reach the top of the view too,
		// scrolling past the end of the content, as vim does.
		{10, 10, term.Coordinates{Y: 24}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 27}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 28}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 30}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 31}, true, term.Coordinates{Y: 0}},
		{100, 100, term.Coordinates{Y: 0}, false, term.Coordinates{Y: 0}},
		{100, 100, term.Coordinates{Y: 31}, true, term.Coordinates{Y: 0}},
	}

	for i, test := range suite {
		t.Run(fmt.Sprintf("offset test case %d", i), func(t *testing.T) {
			c := setupCursor(t, test.width, test.height, false)
			sub := &testScrollSubscriber{}
			c.scroll.Subscribe(sub)

			c.MoveToScroll(test.setCursorAtScroll)
			handled := c.RepositionTop()
			require.Equal(t, test.expectedHandled, handled)

			cursor := c.CursorAtScroll()
			assert.Equal(t, test.setCursorAtScroll, cursor)
			assert.Equal(t, test.expectedWindowCoordinates, c.Coordinates())
			if test.expectedHandled {
				assert.NotZero(t, sub.seek)
			}
		})
	}
}

func TestCursorRepositionTopInverted(t *testing.T) {
	suite := []struct {
		width, height             int
		setCursorAtScroll         term.Coordinates
		expectedHandled           bool
		expectedWindowCoordinates term.Coordinates
	}{
		{10, 10, term.Coordinates{Y: 0}, false, term.Coordinates{Y: 0}},
		{10, 10, term.Coordinates{Y: 2}, true, term.Coordinates{Y: 0}},
		{10, 10, term.Coordinates{Y: 5}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 6}, true, term.Coordinates{Y: 0}},
		{10, 10, term.Coordinates{Y: 7}, true, term.Coordinates{Y: 0}},
		{10, 10, term.Coordinates{Y: 8}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 9}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 10}, true, term.Coordinates{Y: 0}},
		{10, 10, term.Coordinates{Y: 20}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 22}, true, term.Coordinates{X: 3, Y: 0}},
		{10, 10, term.Coordinates{Y: 24}, true, term.Coordinates{X: 3, Y: 2}},
		{10, 10, term.Coordinates{Y: 27}, true, term.Coordinates{X: 3, Y: 5}},
		{10, 10, term.Coordinates{Y: 28}, true, term.Coordinates{X: 3, Y: 6}},
		{10, 10, term.Coordinates{Y: 30}, true, term.Coordinates{X: 3, Y: 8}},
		{10, 10, term.Coordinates{Y: 31}, false, term.Coordinates{Y: 9}},
		{100, 100, term.Coordinates{Y: 0}, false, term.Coordinates{Y: 68}},
		{100, 100, term.Coordinates{Y: 31}, false, term.Coordinates{Y: 99}},
	}

	for i, test := range suite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			c := setupCursor(t, test.width, test.height, false)
			c.scroll.InvertOffset = true
			sub := &testScrollSubscriber{}
			c.scroll.Subscribe(sub)

			// otherwise it's already at the bottom after MoveToScroll
			// since the cursor starts at the top after initializing it
			c.MoveFirstLine()
			c.MoveToScroll(test.setCursorAtScroll)
			handled := c.RepositionTop()
			require.Equal(t, test.expectedHandled, handled)

			cursor := c.CursorAtScroll()
			assert.Equal(t, test.setCursorAtScroll, cursor)
			assert.Equal(t, test.expectedWindowCoordinates, c.Coordinates())
			if test.expectedHandled {
				assert.NotZero(t, sub.seek)
			}
		})
	}
}

func TestCursorRepositionBottom(t *testing.T) {
	suite := []struct {
		width, height             int
		setCursorAtScroll         term.Coordinates
		expectedHandled           bool
		expectedWindowCoordinates term.Coordinates
	}{
		{10, 10, term.Coordinates{Y: 0}, false, term.Coordinates{Y: 0}},
		{10, 10, term.Coordinates{Y: 2}, true, term.Coordinates{Y: 2}},
		{10, 10, term.Coordinates{Y: 5}, true, term.Coordinates{X: 3, Y: 5}},
		{10, 10, term.Coordinates{Y: 6}, true, term.Coordinates{Y: 6}},
		{10, 10, term.Coordinates{Y: 9}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 10}, true, term.Coordinates{Y: 9}},
		{10, 10, term.Coordinates{Y: 11}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 20}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 22}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 27}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 28}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 30}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 31}, false, term.Coordinates{Y: 9}},
		{100, 100, term.Coordinates{Y: 0}, false, term.Coordinates{Y: 0}},
		{100, 100, term.Coordinates{Y: 31}, false, term.Coordinates{Y: 31}},
	}

	for i, test := range suite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			c := setupCursor(t, test.width, test.height, false)
			sub := &testScrollSubscriber{}
			c.scroll.Subscribe(sub)

			// otherwise it's already at the bottom after MoveToScroll
			// since the cursor starts at the top after initializing it
			c.MoveLastLine()
			c.MoveToScroll(test.setCursorAtScroll)
			handled := c.RepositionBottom()
			require.Equal(t, test.expectedHandled, handled)

			cursor := c.CursorAtScroll()
			assert.Equal(t, test.setCursorAtScroll, cursor)
			assert.Equal(t, test.expectedWindowCoordinates, c.Coordinates())
			if test.expectedHandled {
				assert.NotZero(t, sub.seek)
			}
		})
	}
}

func TestCursorRepositionBottomInverted(t *testing.T) {
	suite := []struct {
		width, height             int
		setCursorAtScroll         term.Coordinates
		expectedHandled           bool
		expectedWindowCoordinates term.Coordinates
	}{
		{10, 10, term.Coordinates{Y: 0}, false, term.Coordinates{Y: 0}},
		{10, 10, term.Coordinates{Y: 2}, true, term.Coordinates{Y: 2}},
		{10, 10, term.Coordinates{Y: 5}, true, term.Coordinates{X: 3, Y: 5}},
		{10, 10, term.Coordinates{Y: 6}, true, term.Coordinates{Y: 6}},
		{10, 10, term.Coordinates{Y: 9}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 10}, true, term.Coordinates{Y: 9}},
		{10, 10, term.Coordinates{Y: 11}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 20}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 22}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 27}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 28}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 30}, true, term.Coordinates{X: 3, Y: 9}},
		{10, 10, term.Coordinates{Y: 31}, false, term.Coordinates{Y: 9}},
		{100, 100, term.Coordinates{Y: 0}, false, term.Coordinates{Y: 68}},
		{100, 100, term.Coordinates{Y: 31}, false, term.Coordinates{Y: 99}},
	}

	for i, test := range suite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			c := setupCursor(t, test.width, test.height, false)
			c.scroll.InvertOffset = true
			sub := &testScrollSubscriber{}
			c.scroll.Subscribe(sub)

			c.MoveToScroll(test.setCursorAtScroll)
			handled := c.RepositionBottom()
			require.Equal(t, test.expectedHandled, handled)

			cursor := c.CursorAtScroll()
			assert.Equal(t, test.setCursorAtScroll, cursor)
			assert.Equal(t, test.expectedWindowCoordinates, c.Coordinates())
			if test.expectedHandled {
				assert.NotZero(t, sub.seek)
			}
		})
	}
}

func TestCursorFolds(t *testing.T) {
	ctx := context.Background()
	tsuite := []struct {
		desc            string
		op              func(t *testing.T, c *Cursor, wg *sync.WaitGroup)
		expectOnHide    int
		expectOnVisible int
		cursor          term.Coordinates
		width, height   int
	}{
		{
			"SelectFold selects nothing if cursor is not at fold",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				assert.True(t, c.SelectFold(ctx))
				assert.Zero(t, c.Selection())
			}, 0, 0, term.Coordinates{}, 100, 100,
		},
		{
			"SelectFold selects fold at cursor",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				assert.True(t, c.MoveDown())
				assert.True(t, c.SelectFold(ctx))
				wg.Wait()
				expectedSelection := `/*
 * Ch@ek if the current buffer should be added to or removed from the list of
 * diff buffers.
 `
				assert.Equal(t, expectedSelection, c.Selection())
				assert.True(t, c.DeleteSelection())
			}, 0, 0, term.Coordinates{Y: 1}, 100, 100,
		},
		{
			"CollapseFold collapses nothing if cursor is not at fold",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				assert.True(t, c.CollapseFold(ctx))
			}, 0, 0, term.Coordinates{}, 100, 100,
		},
		{
			"CollapseFold collapses fold at cursor",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				assert.True(t, c.MoveDown())
				assert.True(t, c.CollapseFold(ctx))
			}, 1, 0, term.Coordinates{Y: 1}, 100, 100,
		},
		{
			"CollapseFold collapses inner fold at cursor",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				assert.True(t, c.MoveDown())
				assert.True(t, c.MoveDown())
				assert.True(t, c.CollapseFold(ctx))
			}, 1, 0, term.Coordinates{Y: 2}, 100, 100,
		},
		{
			"ToggleFold toggles fold at cursor",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				assert.True(t, c.MoveDown())
				assert.True(t, c.ToggleFold(ctx))
			}, 1, 0, term.Coordinates{Y: 1}, 100, 100,
		},
		{
			"ToggleFold toggles inner fold at cursor",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				assert.True(t, c.MoveDown())
				assert.True(t, c.MoveDown())
				assert.True(t, c.ToggleFold(ctx))
			}, 1, 0, term.Coordinates{Y: 2}, 100, 100,
		},
		{
			"ExpandFold does nothing if there's no folded fold at cursor",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				assert.True(t, c.MoveDown())
				assert.True(t, c.ExpandFold(ctx))
			}, 0, 0, term.Coordinates{Y: 1}, 100, 100,
		},
		{
			"ExpandFold expands fold at cursor",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				assert.True(t, c.MoveDown())
				assert.True(t, c.CollapseFold(ctx))
				wg.Wait()
				assert.True(t, c.ExpandFold(ctx))
			}, 1, 1, term.Coordinates{Y: 1}, 100, 100,
		},
		{
			"ExpandFold expands inner fold at cursor",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				assert.True(t, c.MoveDown())
				assert.True(t, c.MoveDown())
				assert.True(t, c.CollapseFold(ctx))
				wg.Wait()
				assert.True(t, c.ExpandFold(ctx))
			}, 1, 1, term.Coordinates{Y: 2}, 100, 100,
		},
		{
			"ToggleFold expands folded fold at cursor",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				assert.True(t, c.MoveDown())
				assert.True(t, c.ToggleFold(ctx))
				wg.Wait()
				assert.True(t, c.ToggleFold(ctx))
			}, 1, 1, term.Coordinates{Y: 1}, 100, 100,
		},
		{
			"ToggleAllFolds collapses all outer folds",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				_, ok := c.MoveToScroll(term.Coordinates{Y: 7})
				require.True(t, ok)
				assert.True(t, c.ToggleAllFolds(ctx))
			}, 2, 0, term.Coordinates{Y: 4}, 100, 100,
		},
		{
			"ToggleAllFolds expands all folded folds",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				_, ok := c.MoveToScroll(term.Coordinates{Y: 7})
				require.True(t, ok)
				assert.True(t, c.ToggleAllFolds(ctx))
				wg.Wait()
				wg.Add(1)
				assert.True(t, c.ToggleAllFolds(ctx))
			}, 2, 2, term.Coordinates{Y: 7}, 100, 100,
		},
		{
			"CollapseAllFolds collapses all outer folds",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				_, ok := c.MoveToScroll(term.Coordinates{Y: 7})
				require.True(t, ok)
				assert.True(t, c.CollapseAllFolds(ctx))
			}, 2, 0, term.Coordinates{Y: 4}, 100, 100,
		},
		{
			"ExpandAllFolds expands nothing if nothing is folded",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				_, ok := c.MoveToScroll(term.Coordinates{Y: 7})
				require.True(t, ok)
				assert.True(t, c.ExpandAllFolds(ctx))
			}, 0, 0, term.Coordinates{Y: 7}, 100, 100,
		},
		{
			"ToggleAllFolds collapses all outer folds, maintains window position",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				_, ok := c.MoveToScroll(term.Coordinates{Y: 7})
				require.True(t, ok)
				assert.True(t, c.ToggleAllFolds(ctx))
			}, 2, 0, term.Coordinates{Y: 4}, 100, 5,
		},
		{
			"ToggleAllFolds expands all folded folds, maintains window position",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				_, ok := c.MoveToScroll(term.Coordinates{Y: 7})
				require.True(t, ok)
				assert.True(t, c.ToggleAllFolds(ctx))
				wg.Wait()
				wg.Add(1)
				assert.True(t, c.ToggleAllFolds(ctx))
			}, 2, 2, term.Coordinates{Y: 4}, 100, 5,
		},
		{
			"CollapseAllFolds collapses all outer folds, maintains window position",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				_, ok := c.MoveToScroll(term.Coordinates{Y: 7})
				require.True(t, ok)
				assert.True(t, c.CollapseAllFolds(ctx))
			}, 2, 0, term.Coordinates{Y: 4}, 100, 5,
		},
		{
			"ExpandAllFolds expands nothing if nothing is folde, maintains window positiond",
			func(t *testing.T, c *Cursor, wg *sync.WaitGroup) {
				wg.Add(1)
				_, ok := c.MoveToScroll(term.Coordinates{Y: 7})
				require.True(t, ok)
				assert.True(t, c.ExpandAllFolds(ctx))
			}, 0, 0, term.Coordinates{Y: 4}, 100, 5,
		},
	}

	for _, _tcase := range tsuite {
		tcase := _tcase

		t.Run(tcase.desc, func(t *testing.T) {
			var wg sync.WaitGroup
			e := setupCursorForFolds(t, tcase.width, tcase.height, &wg)
			sub := &testScrollSubscriber{}
			e.scroll.Subscribe(sub)

			tcase.op(t, e, &wg)

			wg.Wait()
			cursor := e.Coordinates()
			assert.Equal(t, tcase.cursor, cursor)
			assert.Equal(t, tcase.expectOnHide, sub.hide)
			assert.Equal(t, tcase.expectOnVisible, sub.visible)
		})
	}

	t.Run("clears results if search text is empty", func(t *testing.T) {
		e := setupCursor(t, 100, 100, false)

		require.Equal(t, 2, e.Search("NULL"))
		e.MoveToNextMatch()

		cursor := e.Coordinates()
		assert.Equal(t, term.Coordinates{X: 14, Y: 18}, cursor)

		require.Equal(t, 0, e.Search(""))
		e.MoveToNextMatch()

		cursor = e.Coordinates()
		assert.Equal(t, term.Coordinates{X: 14, Y: 18}, cursor)
	})
}

func TestCursorEmptyFoldsScheduleOperation(t *testing.T) {
	buf := cell.NewBuffer()
	fs := &testFoldsService{folds: iterator.FromSlice([]term.Range{})}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	scheduled := make(chan func(), 1)
	cursor := NewCursor(scroll, func(fn func()) bool {
		scheduled <- fn
		return true
	})
	_, err := scroll.Buffer().ReadFrom(strings.NewReader(sampleSnippet))
	require.NoError(t, err)
	scroll.Resize(100, 100)

	require.True(t, cursor.ExpandAllFolds(context.Background()))
	select {
	case fn := <-scheduled:
		fn()
	case <-time.After(time.Second):
		t.Fatal("empty fold operation did not run through the scheduler")
	}
}

func TestCursorSearch(t *testing.T) {
	tsuite := []struct {
		desc          string
		width, height int
		results       int
		searchstring  string
		assertions    func(*testing.T, *Cursor)
		cursor        term.Coordinates
	}{
		{
			"does nothing if search text is not found",
			1000, 1000,
			0,
			"nothing",
			func(t *testing.T, e *Cursor) {
				assert.False(t, e.MoveToNextMatch())
			}, term.Coordinates{},
		},
		{
			"moves to the first result if search text is found",
			1000, 1000,
			2,
			"NULL",
			nil, term.Coordinates{X: 14, Y: 18},
		},
		{
			"tolerates inserts to buffer by updating locations",
			1000, 1000,
			2,
			"NULL",
			func(t *testing.T, e *Cursor) {
				var at term.Coordinates
				e.buffer().Edit(context.Background(), at, at, "\n")
				assert.True(t, e.MoveToNextMatch())
			},
			term.Coordinates{X: 14, Y: 19},
		},
		{
			"MoveToNextMatch does nothing if only one result is found",
			1000, 1000,
			1,
			"else",
			func(t *testing.T, e *Cursor) {
				assert.False(t, e.MoveToNextMatch())
			}, term.Coordinates{X: 4, Y: 29},
		},
		{
			"Seeks to first result if not in window",
			100, 10,
			1,
			"When",
			nil,
			term.Coordinates{X: 7, Y: 9},
		},
		{
			"Seeks to last result upon MoveToPrevMatch",
			1000, 1000,
			2,
			"NULL",
			func(t *testing.T, e *Cursor) {
				assert.True(t, e.MoveToPrevMatch())
			}, term.Coordinates{X: 32, Y: 23},
		},
		{
			"Seeks if MoveToNextMatch result is not in window",
			100, 10,
			2,
			"NULL",
			func(t *testing.T, e *Cursor) {
				assert.True(t, e.MoveToNextMatch())
			}, term.Coordinates{X: 32, Y: 9},
		},
		{
			"returns partial word results",
			100, 10,
			2,
			"NU",
			func(t *testing.T, e *Cursor) {
				assert.True(t, e.MoveToNextMatch())
			}, term.Coordinates{X: 32, Y: 9},
		},
		{
			"returns partial and complete word results",
			100, 10,
			38,
			"i",
			func(t *testing.T, e *Cursor) {
				assert.True(t, e.MoveToNextMatch())
			}, term.Coordinates{X: 71, Y: 2},
		},
	}

	for _, _tcase := range tsuite {
		tcase := _tcase

		t.Run(tcase.desc, func(t *testing.T) {
			e := setupCursor(t, tcase.width, tcase.height, false)

			require.Equal(t, tcase.results, e.Search(tcase.searchstring))
			e.MoveToNextMatch() // backwards compat
			if tcase.assertions != nil {
				tcase.assertions(t, e)
			}

			cursor := e.Coordinates()
			assert.Equal(t, tcase.cursor, cursor)

			if tcase.results == 0 {
				return
			}

			require.True(t, e.Select())
			for i := 1; i < len(tcase.searchstring); i++ {
				e.MoveRight()
			}
			assert.Equal(t, tcase.searchstring, e.Selection())
		})
	}

	t.Run("clears results if search text is empty", func(t *testing.T) {
		e := setupCursor(t, 100, 100, false)

		require.Equal(t, 2, e.Search("NULL"))
		e.MoveToNextMatch()

		cursor := e.Coordinates()
		assert.Equal(t, term.Coordinates{X: 14, Y: 18}, cursor)

		require.Equal(t, 0, e.Search(""))
		e.MoveToNextMatch()

		cursor = e.Coordinates()
		assert.Equal(t, term.Coordinates{X: 14, Y: 18}, cursor)
	})
}

func TestCursorSearchWord(t *testing.T) {
	tsuite := []struct {
		desc          string
		width, height int
		results       int
		searchstring  string
		assertions    func(*testing.T, *Cursor)
		cursor        term.Coordinates
	}{
		{
			"does nothing if search text is not found",
			1000, 1000,
			0,
			"nothing",
			func(t *testing.T, e *Cursor) {
				assert.False(t, e.MoveToNextMatch())
			}, term.Coordinates{},
		},
		{
			"moves to the first result if search text is found",
			1000, 1000,
			2,
			"NULL",
			nil, term.Coordinates{X: 14, Y: 18},
		},
		{
			"tolerates inserts to buffer by updating locations",
			1000, 1000,
			2,
			"NULL",
			func(t *testing.T, e *Cursor) {
				var at term.Coordinates
				e.buffer().Edit(context.Background(), at, at, "\n")
				assert.True(t, e.MoveToNextMatch())
			},
			term.Coordinates{X: 14, Y: 19},
		},
		{
			"MoveToNextMatch does nothing if only one result is found",
			1000, 1000,
			1,
			"else",
			func(t *testing.T, e *Cursor) {
				assert.False(t, e.MoveToNextMatch())
			}, term.Coordinates{X: 4, Y: 29},
		},
		{
			"Seeks to first result if not in window",
			100, 10,
			1,
			"When",
			nil,
			term.Coordinates{X: 7, Y: 9},
		},
		{
			"Seeks to last result upon MoveToPrevMatch",
			1000, 1000,
			2,
			"NULL",
			func(t *testing.T, e *Cursor) {
				assert.True(t, e.MoveToPrevMatch())
			}, term.Coordinates{X: 32, Y: 23},
		},
		{
			"Seeks if MoveToNextMatch result is not in window",
			100, 10,
			2,
			"NULL",
			func(t *testing.T, e *Cursor) {
				assert.True(t, e.MoveToNextMatch())
			}, term.Coordinates{X: 32, Y: 9},
		},
		{
			"ignores non complete words",
			100, 10,
			0,
			"NU",
			func(t *testing.T, e *Cursor) {
				assert.False(t, e.MoveToNextMatch())
			}, term.Coordinates{},
		},
		{
			"returns only complete word results",
			100, 10,
			4,
			"i",
			func(t *testing.T, e *Cursor) {
				assert.True(t, e.MoveToNextMatch())
			}, term.Coordinates{X: 8, Y: 9},
		},
	}

	for _, _tcase := range tsuite {
		tcase := _tcase

		t.Run(tcase.desc, func(t *testing.T) {
			e := setupCursor(t, tcase.width, tcase.height, false)

			require.Equal(t, tcase.results, e.SearchWord(tcase.searchstring))
			e.MoveToNextMatch() // backwards compat
			if tcase.assertions != nil {
				tcase.assertions(t, e)
			}

			cursor := e.Coordinates()
			assert.Equal(t, tcase.cursor, cursor)

			if tcase.results == 0 {
				return
			}

			require.True(t, e.Select())
			for i := 1; i < len(tcase.searchstring); i++ {
				e.MoveRight()
			}
			assert.Equal(t, tcase.searchstring, e.Selection())
		})
	}

	t.Run("clears results if search text is empty", func(t *testing.T) {
		e := setupCursor(t, 100, 100, false)

		require.Equal(t, 2, e.SearchWord("NULL"))
		e.MoveToNextMatch()

		cursor := e.Coordinates()
		assert.Equal(t, term.Coordinates{X: 14, Y: 18}, cursor)

		require.Equal(t, 0, e.SearchWord(""))
		e.MoveToNextMatch()

		cursor = e.Coordinates()
		assert.Equal(t, term.Coordinates{X: 14, Y: 18}, cursor)
	})
}

func TestCursorMove(t *testing.T) {
	tsuite := []struct {
		desc           string
		width, height  int
		sut            func(*testing.T, *Cursor)
		cursorAtScroll term.Coordinates
	}{
		{
			"MoveStartLine should do nothing if already at start of line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				assert.False(t, e.MoveStartLine())
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveStartLine should move to start of line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = 20
				assert.True(t, e.MoveStartLine())
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveStartLineNonBlank moves to first none blank character of line, starts blank",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 20
				assert.True(t, e.MoveStartLineNonBlank())
			},
			term.Coordinates{X: 2, Y: 20},
		},
		{
			"MoveStartLineNonBlank moves to first none blank character of line, doesn't start blank",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 6
				assert.True(t, e.MoveStartLineNonBlank())
			},
			term.Coordinates{X: 0, Y: 6},
		},
		{
			"MoveStartLineNonBlank moves to first none blank character of line, cursor beyond end",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = 27
				e.cursor.Y = 20
				assert.True(t, e.MoveStartLineNonBlank())
			},
			term.Coordinates{X: 2, Y: 20},
		},
		{
			"MoveStartLineNonBlank empty line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 10
				assert.False(t, e.MoveStartLineNonBlank())
			},
			term.Coordinates{X: 0, Y: 10},
		},
		{
			"MoveStartLine should seek to start of line if start is out of window",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.scroll.SeekEndLine()
				e.cursor.X = 20

				assert.True(t, e.MoveStartLine())

				assert.Equal(t, 0, e.scroll.Offset().X)
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveEndLine should do nothing if already at end of line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor = term.Coordinates{X: 2, Y: 1}
				assert.False(t, e.MoveEndLine())
			},
			term.Coordinates{X: 2, Y: 1},
		},
		{
			"MoveEndLine should fix position if past the end of line",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.cursor = term.Coordinates{X: 3, Y: 1}
				assert.True(t, e.MoveEndLine())
			},
			term.Coordinates{X: 2, Y: 1},
		},
		{
			"MoveEndLine should move cursor to end of line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 1
				assert.True(t, e.MoveEndLine())
			},
			term.Coordinates{X: 2, Y: 1},
		},
		{
			"MoveEndLine should seek to end of line if end is out of window",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 2

				assert.True(t, e.MoveEndLine())
				// backwards compat
				e.MoveToBounds(0)
				if !e.scroll.Wrap {
					assert.Equal(t, 76, e.scroll.Offset().X+e.cursor.X)
				} else {
					assert.Equal(t, 6, e.scroll.Offset().X+e.cursor.X)
				}
				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, 'f', c.Ch, string(c.Ch))
			},
			term.Coordinates{X: 76, Y: 2},
		},
		{
			"MoveFirstLine should do nothing if already on first line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				assert.False(t, e.MoveFirstLine())
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveFirstLine should move to first line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 10
				assert.True(t, e.MoveFirstLine())
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveFirstLine should seek to first line if not in window",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.scroll.SeekEndFile()
				e.cursor.Y = 2
				assert.True(t, e.MoveFirstLine())
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveLastLine should do nothing if already on last line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 31
				assert.False(t, e.MoveLastLine())
			},
			term.Coordinates{X: 0, Y: 31},
		},
		{
			"MoveLastLine should move to first line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				assert.True(t, e.MoveLastLine())
			},
			term.Coordinates{X: 0, Y: 31},
		},
		{
			"MoveLastLine should seek to last line if not in window",
			10, 10,
			func(t *testing.T, e *Cursor) {
				assert.True(t, e.MoveLastLine())

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				if !e.scroll.Wrap {
					assert.Equal(t, 31, e.scroll.Offset().Y+e.cursor.Y)
				}
				assert.Equal(t, '}', c.Ch)
			},
			term.Coordinates{X: 0, Y: 31},
		},
		{
			"MoveDown should not move the cursor position past the last line until end of window",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				if e.scroll.Wrap {
					// semantics past last content are different between wrap and non-wrap
					t.Skip()
				}
				e.cursor.Y = 31
				for i := 0; i < e.scroll.SizeHeight(); i++ {
					e.MoveDown()
				}
			},
			term.Coordinates{X: 0, Y: 31},
		},
		{
			"MoveDown should seek down if reached last line in window but not at last line",
			100, 10, // avoid wraps in wrap mode
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 9
				assert.True(t, e.MoveDown())
				assert.Equal(t, 1, e.scroll.Offset().Y)
			},
			term.Coordinates{X: 0, Y: 10},
		},
		{
			"MoveDown should NOT seek down if reached last line",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 9
				e.scroll.SeekEndFile()
				offsetY := e.scroll.Offset().Y
				assert.False(t, e.MoveDown())
				assert.Equal(t, offsetY, e.scroll.Offset().Y)
			},
			term.Coordinates{X: 0, Y: 31},
		},
		{
			"MoveDownLines with count 0 does nothing",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 3
				assert.False(t, e.MoveDownLines(0))
			},
			term.Coordinates{X: 0, Y: 3},
		},
		{
			"MoveDownLines with negative count does nothing",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 3
				assert.False(t, e.MoveDownLines(-2))
			},
			term.Coordinates{X: 0, Y: 3},
		},
		{
			"MoveDownLines with count 3 should move cursor left 3 times",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 3
				assert.True(t, e.MoveDownLines(3))
			},
			term.Coordinates{X: 0, Y: 6},
		},
		{
			"MoveDownLines should not move the cursor position past the last line until end of window",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 0
				assert.True(t, e.MoveDownLines(200)) // this places it at content last row + 1
				assert.False(t, e.MoveDownLines(200))
			},
			term.Coordinates{X: 0, Y: 31},
		},
		{
			"MoveDownLines should seek down if reached last line in window but not at last line",
			1000, 10,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 9
				assert.True(t, e.MoveDownLines(3))
				assert.Equal(t, 3, e.scroll.Offset().Y)
			},
			term.Coordinates{X: 0, Y: 12},
		},
		{
			"MoveDownLines should scroll to window end and stick cursor at last content line if jumping beyond last content line",
			1000, 20,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 5
				assert.Equal(t, 0, e.scroll.Offset().Y)
				assert.True(t, e.MoveDownLines(777))
				assert.Equal(t, 12, e.scroll.Offset().Y)
			},
			term.Coordinates{X: 0, Y: 31},
		},
		{
			"MoveUpLines with count 0 does nothing",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 3
				assert.False(t, e.MoveUpLines(0))
			},
			term.Coordinates{X: 0, Y: 3},
		},
		{
			"MoveUpLines with negative count does nothing",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 3
				assert.False(t, e.MoveUpLines(-2))
			},
			term.Coordinates{X: 0, Y: 3},
		},
		{
			"MoveUp should do nothing if already on first line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				assert.False(t, e.MoveUp())
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveUp should NOT fix cursor position if negative",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				if e.scroll.Wrap {
					// diff treatment of cursorAtScroll
					t.SkipNow()
				}
				e.cursor.Y = -1
				assert.False(t, e.MoveUp())
			},
			term.Coordinates{X: 0, Y: -1},
		},
		{
			"MoveUp should move the cursor up one line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 10
				assert.True(t, e.MoveUp())
			},
			term.Coordinates{X: 0, Y: 9},
		},
		{
			"MoveUp should seek up if not at first line and cursor is at first line of window",
			100, 10, // avoid creating wraps in wrap mode
			func(t *testing.T, e *Cursor) {
				e.scroll.SeekEndFile()
				offsetY := e.scroll.Offset().Y
				assert.True(t, e.MoveUp())
				assert.Equal(t, offsetY-1, e.scroll.Offset().Y)
			},
			term.Coordinates{X: 0, Y: 21},
		},
		{
			"MoveUp should NOT seek up if already at first line",
			10, 10,
			func(t *testing.T, e *Cursor) {
				offsetY := e.scroll.Offset().Y
				assert.False(t, e.MoveUp())
				assert.Equal(t, offsetY, e.scroll.Offset().Y)
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveUpLines should NOT fix cursor position if negative",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				if e.scroll.Wrap {
					t.SkipNow()
				}
				e.cursor.Y = -1
				assert.False(t, e.MoveUpLines(4))
			},
			term.Coordinates{X: 0, Y: -1},
		},
		{
			"MoveUpLines should move the cursor N lines",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 10
				assert.True(t, e.MoveUpLines(5))
			},
			term.Coordinates{X: 0, Y: 5},
		},
		{
			"MoveUpLines should not go beyond the top line 0 of content",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 10
				assert.True(t, e.MoveUpLines(66))
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveUpLines should seek up if not at first content line and cursor is at first line of window",
			100, 10, // avoid creating wraps in wrap mode
			func(t *testing.T, e *Cursor) {
				e.scroll.SeekEndFile()
				offsetY := e.scroll.Offset().Y
				assert.True(t, e.MoveUpLines(4))
				assert.Equal(t, offsetY-4, e.scroll.Offset().Y)
			},
			term.Coordinates{X: 0, Y: 18},
		},
		{
			"MoveUpLines should NOT seek up if already at first line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				assert.False(t, e.MoveUpLines(8))
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveLeft should do nothing if already at start of line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				assert.False(t, e.MoveLeft())
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveLeft should move cursor left",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = 1
				assert.True(t, e.MoveLeft())
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveLeft does nothing if pos is negative",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = -1
				assert.False(t, e.MoveLeft())
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveLeft should seek left if at start of window but not at start of line",
			10, 10,
			func(t *testing.T, e *Cursor) {
				if e.scroll.Wrap {
					t.Skip()
					return
				}
				e.scroll.SeekEndLine()
				assert.NotZero(t, e.scroll.Offset().X)

				for range 100 {
					e.MoveLeft()
				}
				assert.Zero(t, e.scroll.Offset().X)
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveLeftColumns with count 0 does nothing",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = 7
				assert.False(t, e.MoveLeftColumns(0))
			},
			term.Coordinates{X: 7, Y: 0},
		},
		{
			"MoveLeftColumns with negative count does nothing",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = 7
				assert.False(t, e.MoveLeftColumns(-2))
			},
			term.Coordinates{X: 7, Y: 0},
		},
		{
			"MoveLeftColumns should do nothing if already at start of line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				assert.False(t, e.MoveLeftColumns(4))
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveLeftColumns should move cursor left",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 2
				e.cursor.X = 7
				assert.True(t, e.MoveLeftColumns(4))
			},
			term.Coordinates{X: 3, Y: 2},
		},
		{
			"MoveLeftColumns fixes cursor pos if negative",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = -1
				assert.False(t, e.MoveLeftColumns(4))
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveLeftColumns should seek left if at start of window but not at start of line",
			10, 10,
			func(t *testing.T, e *Cursor) {
				if e.scroll.Wrap {
					t.Skip()
					return
				}
				assert.Zero(t, e.scroll.Offset().X)
				e.scroll.SeekEndLine()
				assert.NotZero(t, e.scroll.Offset().X)
				e.MoveLeftColumns(100)
				assert.Zero(t, e.scroll.Offset().X)
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveRightColumns with count 0 does nothing",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = 4
				assert.False(t, e.MoveRightColumns(0))
			},
			term.Coordinates{X: 4, Y: 0},
		},
		{
			"MoveRightColumns with negative count does nothing",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = 4
				assert.False(t, e.MoveRightColumns(-2))
			},
			term.Coordinates{X: 4, Y: 0},
		},
		{
			"MoveRight should move cursor right",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 2
				e.cursor.X = 5
				assert.True(t, e.MoveRight())
			},
			term.Coordinates{X: 6, Y: 2},
		},
		{
			"MoveRight should not move cursor right if past current line's end of line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = 4
				e.cursor.Y = 1
				assert.False(t, e.MoveRight())
			},
			term.Coordinates{X: 4, Y: 1},
		},
		{
			"MoveRight should not move cursor right if at the end of the line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.RightInclusiveSemantics = false
				e.cursor.X = 3
				e.cursor.Y = 1
				assert.False(t, e.MoveRight())
			},
			term.Coordinates{X: 3, Y: 1},
		},
		{
			"MoveRight should seek right if at end of window but not at end of line",
			10, 10,
			func(t *testing.T, e *Cursor) {
				if e.scroll.Wrap {
					t.Skip()
					return
				}
				e.cursor.Y = 2
				e.cursor.X = 5
				e.scroll.SeekStartLine()

				for range 100 {
					e.MoveRight()
				}
			},
			term.Coordinates{X: 77, Y: 2},
		},
		{
			"MoveRightColumns should move cursor right",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 2
				e.cursor.X = 5
				assert.True(t, e.MoveRightColumns(3))
			},
			term.Coordinates{X: 8, Y: 2},
		},
		{
			"MoveRightColumns should not move cursor right if past current line's end of line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = 1
				assert.False(t, e.MoveRightColumns(8))
			},
			term.Coordinates{X: 1, Y: 0},
		},
		{
			"MoveRightColumns should not move cursor right if at the end of the line",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = 4
				e.cursor.Y = 1
				assert.False(t, e.MoveRightColumns(10))
			},
			term.Coordinates{X: 4, Y: 1},
		},
		{
			"MoveRight should seek right if at end of window but not at end of line",
			10, 10,
			func(t *testing.T, e *Cursor) {
				if e.scroll.Wrap {
					t.Skip()
					return
				}
				e.cursor.Y = 2
				e.cursor.X = 5
				e.scroll.SeekStartLine()
				e.MoveRightColumns(100)
			},
			term.Coordinates{X: 77, Y: 2},
		},
		{
			"MoveRightStartWord should move to the start of the next word",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.MoveToScroll(term.Coordinates{X: 9, Y: 2})

				require.True(t, e.MoveRightStartWord())

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, 't', c.Ch, string(c.Ch))
			},
			term.Coordinates{X: 12, Y: 2},
		},
		{
			"MoveRightStartWord should stop stop on special symbols such as the at-symbol @",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.MoveToScroll(term.Coordinates{X: 3, Y: 2})

				require.True(t, e.MoveRightStartWord())

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, '@', c.Ch, string(c.Ch))
			},
			term.Coordinates{X: 5, Y: 2},
		},
		{
			"MoveLeftStartWord should move to the start of the next word",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 2
				e.cursor.X = 9
				e.MoveRightStartWord()

				assert.True(t, e.MoveLeftStartWord())

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, 'i', c.Ch)
			},
			term.Coordinates{X: 9, Y: 2},
		},
		{
			"MoveLeftStartWord should stop on special symbols such as the at-symbol @",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 2
				e.cursor.X = 7
				e.MoveRightStartWord()

				assert.True(t, e.MoveLeftStartWord())

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, 'e', c.Ch)
			},
			term.Coordinates{X: 6, Y: 2},
		},
		{
			"MoveRightEndWord should move to the end of the current word",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 2
				e.cursor.X = 9

				e.MoveRightStartWord()
				e.MoveLeftStartWord()

				assert.True(t, e.MoveRightEndWord())

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, 'f', c.Ch)
			},
			term.Coordinates{X: 10, Y: 2},
		},
		{
			"MoveRightEndWord should wrap around until end of file (no wrap)",
			10, 10,
			func(t *testing.T, e *Cursor) {
				for e.MoveRightEndWord() {
				}
			},
			term.Coordinates{X: 10, Y: 31},
		},
		{
			"MoveLeftEndWord should wrap around until start of file",
			10, 10,
			func(t *testing.T, e *Cursor) {
				require.True(t, e.MoveLastLine())
				for e.MoveLeftEndWord() {
				}
			},
			term.Coordinates{Y: 1},
		},
		{
			"MoveLeftEndWord should move to end of previous word (right inclusive)",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.cursor.X = 9
				e.cursor.Y = 2
				assert.True(t, e.MoveLeftEndWord())
			},
			term.Coordinates{X: 7, Y: 2},
		},
		{
			"MoveLeftEndWord should move to end of previous word (right exclusive)",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.RightInclusiveSemantics = false
				e.cursor.X = 9
				e.cursor.Y = 2
				assert.True(t, e.MoveLeftEndWord())
			},
			term.Coordinates{X: 8, Y: 2},
		},
		{
			"MoveLeftStartWord should wrap around until start of file",
			10, 10,
			func(t *testing.T, e *Cursor) {
				require.True(t, e.MoveLastLine())
				for e.MoveLeftStartWord() {
				}
			},
			term.Coordinates{},
		},
		{
			"MoveRightEndWord should move to the end of the current word (right exclusive)",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.RightInclusiveSemantics = false
				e.cursor.Y = 2
				e.cursor.X = 9

				e.MoveRightStartWord()
				e.MoveLeftStartWord()

				assert.True(t, e.MoveRightEndWord())
			},
			term.Coordinates{X: 11, Y: 2},
		},
		{
			"MoveRightEndWord should stop on special symbols such as the at-symbol @",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 2
				e.cursor.X = 4

				assert.True(t, e.MoveRightEndWord())

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, '@', c.Ch)
			},
			term.Coordinates{X: 5, Y: 2},
		},
		{
			"MoveRightEndWord should stop on special symbols such as the at-symbol @",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 2
				e.cursor.X = 4
				e.RightInclusiveSemantics = false

				assert.True(t, e.MoveRightEndWord())
			},
			term.Coordinates{X: 6, Y: 2},
		},
		{
			"MoveToMatchingRune should do nothing if rune is not {,[,(,},],)",
			10, 10,
			func(t *testing.T, e *Cursor) {
				assert.False(t, e.MoveToMatchingRune())
			},
			term.Coordinates{X: 0, Y: 0},
		},
		{
			"MoveToMatchingRune should not seek unless necessary",
			77, 77,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 27
				e.cursor.X = 4
				assert.True(t, e.MoveToMatchingRune())
			},
			term.Coordinates{X: 1, Y: 19},
		},
		{
			"MoveToMatchingRune should move to the 'matching rune' forward",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.MoveToScroll(term.Coordinates{Y: 7})

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				require.Equal(t, '{', c.Ch)

				require.True(t, e.MoveToMatchingRune())

				c, _ = e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, '}', c.Ch)
			},
			term.Coordinates{X: 0, Y: 31},
		},
		{
			"MoveToMatchingRune should move to the 'matching rune' backwards",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.MoveToScroll(term.Coordinates{Y: 31})

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				require.Equal(t, '}', c.Ch)

				require.True(t, e.MoveToMatchingRune())

				c, _ = e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, '{', c.Ch)
			},
			term.Coordinates{X: 0, Y: 7},
		},
		{
			"MoveToMatchingRune should return false if current matching rune is not found",
			1000, 1000,
			func(t *testing.T, e *Cursor) {
				e.cursor.Y = 31
				e.cursor.X = 5
				e.scroll.SeekEndFile()
				assert.False(t, e.MoveToMatchingRune())

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, '{', c.Ch)
			},
			term.Coordinates{X: 5, Y: 31},
		},
		{
			"MoveToNextChar should do nothing if there is no matches in the line",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.MoveDown()
				e.MoveRight()
				assert.False(t, e.MoveToNextChar('a'))
			},
			term.Coordinates{X: 1, Y: 1},
		},
		{
			"MoveToNextChar should do nothing if are only matches before cursor",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.MoveDown()
				e.MoveRight()
				assert.False(t, e.MoveToNextChar('/'))
			},
			term.Coordinates{X: 1, Y: 1},
		},
		{
			"MoveToNextChar should move the cursor to a matching character",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.MoveDown()
				e.MoveDown()
				assert.True(t, e.MoveToNextChar('o'))

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, 'o', c.Ch)
			},
			term.Coordinates{X: 33, Y: 2},
		},
		{
			"MoveToNextChar should move the cursor to a matching character at the end of line",
			10, 10,
			func(t *testing.T, e *Cursor) {
				for range 3 {
					assert.True(t, e.MoveDown())
				}
				assert.True(t, e.MoveToNextChar('.'))

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, '.', c.Ch)
			},
			term.Coordinates{X: 15, Y: 3},
		},
		{
			"MoveToPrevChar should do nothing if there is no matches in the line",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.MoveDown()
				e.MoveRight()
				assert.False(t, e.MoveToPrevChar('a'))
			},
			term.Coordinates{X: 1, Y: 1},
		},
		{
			"MoveToPrevChar should do nothing if are only matches after cursor",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.MoveDown()
				assert.False(t, e.MoveToPrevChar('/'))
			},
			term.Coordinates{X: 0, Y: 1},
		},
		{
			"MoveToPrevChar should move the cursor to a matching character",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.MoveDown()
				e.MoveDown()
				e.MoveEndLine()
				assert.True(t, e.MoveToPrevChar('C'))

				c, _ := e.scroll.Buffer().Cell(e.cursorAtScroll())
				assert.Equal(t, 'C', c.Ch)
			},
			term.Coordinates{X: 3, Y: 2},
		},
		{
			"MoveToScroll cursor beyond vertical content does not change coordinates",
			10, 10,
			func(t *testing.T, e *Cursor) {
				e.MoveLastLine()
				cur := e.Coordinates()
				e.MoveToScroll(term.Coordinates{X: cur.X + 1, Y: cur.Y + 1})
			},
			term.Coordinates{X: 1, Y: 10},
		},
	}

	for _, _tcase := range tsuite {
		tcase := _tcase

		t.Run(tcase.desc, func(t *testing.T) {
			e := setupCursor(t, tcase.width, tcase.height, false)

			tcase.sut(t, e)

			cursor := e.CursorAtScroll()
			assert.Equal(t, tcase.cursorAtScroll, cursor)
		})

		t.Run(tcase.desc+" (wrap mode on)", func(t *testing.T) {
			e := setupCursor(t, tcase.width, tcase.height, true)

			tcase.sut(t, e)

			cursor := e.CursorAtScroll()
			assert.Equal(t, tcase.cursorAtScroll, cursor)
		})
	}
}

func TestCursorMoveEndLineNonBlank(t *testing.T) {
	suite := []struct {
		desc    string
		content string
		width   int
		height  int
		wrap    bool
		startX  int
		startY  int
		wantOK  bool
		wantX   int
		wantY   int
	}{
		// ===== Basic cases (no wrap) =====
		{
			desc:    "trailing spaces: moves to last non-blank",
			content: "hello   \nworld",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: true, wantX: 4, wantY: 0,
		},
		{
			desc:    "no trailing spaces: moves to last char",
			content: "hello   \nworld",
			width:   1000, height: 1000,
			startX: 0, startY: 1,
			wantOK: true, wantX: 4, wantY: 1,
		},
		{
			desc:    "all-blank line: returns false, cursor at x=0",
			content: "hello\n   \nworld",
			width:   1000, height: 1000,
			startX: 0, startY: 1,
			wantOK: false, wantX: 0, wantY: 1,
		},
		{
			desc:    "empty line: returns false, cursor at x=0",
			content: "hello\n\nworld",
			width:   1000, height: 1000,
			startX: 0, startY: 1,
			wantOK: false, wantX: 0, wantY: 1,
		},
		{
			desc:    "leading and trailing spaces",
			content: "  foo  ",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: true, wantX: 4, wantY: 0,
		},
		{
			desc:    "already at last non-blank: stays put",
			content: "hello   ",
			width:   1000, height: 1000,
			startX: 4, startY: 0,
			wantOK: true, wantX: 4, wantY: 0,
		},
		{
			desc:    "from middle of line",
			content: "hello   ",
			width:   1000, height: 1000,
			startX: 2, startY: 0,
			wantOK: true, wantX: 4, wantY: 0,
		},

		// ===== Cursor past end of line =====
		{
			desc:    "cursor X beyond line length: still finds last non-blank",
			content: "hello   ",
			width:   1000, height: 1000,
			startX: 100, startY: 0,
			wantOK: true, wantX: 4, wantY: 0,
		},
		{
			desc:    "cursor X beyond line length on short line",
			content: "ab",
			width:   1000, height: 1000,
			startX: 50, startY: 0,
			wantOK: true, wantX: 1, wantY: 0,
		},
		{
			desc:    "cursor in trailing whitespace region",
			content: "hello   ",
			width:   1000, height: 1000,
			startX: 6, startY: 0,
			wantOK: true, wantX: 4, wantY: 0,
		},

		// ===== Y out of bounds =====
		{
			desc:    "Y beyond content rows: returns false, cursor unchanged",
			content: "hello",
			width:   1000, height: 1000,
			startX: 0, startY: 99,
			wantOK: false, wantX: 0, wantY: 99,
		},

		// ===== Single character lines =====
		{
			desc:    "single char line: cursor on the char",
			content: "a",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: true, wantX: 0, wantY: 0,
		},
		{
			desc:    "single char with trailing space",
			content: "a ",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: true, wantX: 0, wantY: 0,
		},
		{
			desc:    "single space: all blank returns false",
			content: " ",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: false, wantX: 0, wantY: 0,
		},

		// ===== Null characters (treated as blank) =====
		{
			desc:    "trailing nulls: finds last non-blank before nulls",
			content: "abc\x00\x00\x00",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: true, wantX: 2, wantY: 0,
		},
		{
			desc:    "all nulls: returns false",
			content: "\x00\x00\x00",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: false, wantX: 0, wantY: 0,
		},
		{
			desc:    "mixed nulls and spaces: all blank",
			content: "\x00 \x00 ",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: false, wantX: 0, wantY: 0,
		},
		{
			desc:    "non-blank between nulls",
			content: "\x00a\x00",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: true, wantX: 1, wantY: 0,
		},
		{
			desc:    "trailing null after text and spaces",
			content: "hi \x00",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: true, wantX: 1, wantY: 0,
		},

		// ===== Tab characters (treated as blank) =====
		{
			desc:    "tab only line: all blank",
			content: "\t\t",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: false, wantX: 0, wantY: 0,
		},
		{
			desc:    "text with trailing tab",
			content: "abc\t",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: true, wantX: 2, wantY: 0,
		},
		{
			desc:    "tab then text then tab",
			content: "\tx\t",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: true, wantX: 1, wantY: 0,
		},

		// ===== Mixed blank types =====
		{
			desc:    "mixed spaces tabs and nulls trailing",
			content: "xy \t\x00",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: true, wantX: 1, wantY: 0,
		},
		{
			desc:    "mixed leading blanks then text then trailing blanks",
			content: " \t\x00abc \t\x00",
			width:   1000, height: 1000,
			startX: 0, startY: 0,
			wantOK: true, wantX: 5, wantY: 0,
		},

		// ===== Multiline navigation =====
		{
			desc:    "second line with trailing spaces",
			content: "aaa\nbbb   ",
			width:   1000, height: 1000,
			startX: 0, startY: 1,
			wantOK: true, wantX: 2, wantY: 1,
		},
		{
			desc:    "last line no trailing",
			content: "aaa\nbbb\nccc",
			width:   1000, height: 1000,
			startX: 0, startY: 2,
			wantOK: true, wantX: 2, wantY: 2,
		},
		{
			desc:    "multiline: cursor on middle empty line",
			content: "aaa\n\nccc",
			width:   1000, height: 1000,
			startX: 0, startY: 1,
			wantOK: false, wantX: 0, wantY: 1,
		},

		// ===== Wrap mode =====
		{
			desc:    "wrap: short line within width, trailing spaces",
			content: "ab   ",
			width:   10, height: 10,
			wrap:   true,
			startX: 0, startY: 0,
			wantOK: true, wantX: 1, wantY: 0,
		},
		{
			desc:    "wrap: line wraps, last non-blank on first visual row",
			content: "abcd   ",
			width:   4, height: 10,
			wrap:   true,
			startX: 0, startY: 0,
			wantOK: true, wantX: 3, wantY: 0,
		},
		{
			desc:    "wrap: line wraps, last non-blank on second visual row",
			content: "abcdefg   ",
			width:   4, height: 10,
			wrap:   true,
			startX: 0, startY: 0,
			wantOK: true, wantX: 6, wantY: 0,
		},
		{
			desc:    "wrap: cursor on second visual row, finds last non-blank",
			content: "abcdefg   ",
			width:   4, height: 10,
			wrap:   true,
			startX: 4, startY: 0,
			wantOK: true, wantX: 6, wantY: 0,
		},
		{
			desc:    "wrap: multiline, second logical line wraps",
			content: "ab\nefghijkl  ",
			width:   4, height: 10,
			wrap:   true,
			startX: 0, startY: 1,
			wantOK: true, wantX: 7, wantY: 1,
		},
		{
			desc:    "wrap: all-blank line in wrapped content",
			content: "abcd\n    \nij",
			width:   4, height: 20,
			wrap:   true,
			startX: 0, startY: 1,
			wantOK: false, wantX: 0, wantY: 1,
		},
		{
			desc:    "wrap: single char line doesn't wrap",
			content: "a",
			width:   4, height: 10,
			wrap:   true,
			startX: 0, startY: 0,
			wantOK: true, wantX: 0, wantY: 0,
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.desc, func(t *testing.T) {
			e := setupCursorContent(t, tcase.width, tcase.height, tcase.content, tcase.wrap)
			e.cursor = term.Coordinates{X: tcase.startX, Y: tcase.startY}

			ok := e.MoveEndLineNonBlank()
			assert.Equal(t, tcase.wantOK, ok, "MoveEndLineNonBlank return value")

			coord := e.CursorAtScroll()
			assert.Equal(t, term.Coordinates{X: tcase.wantX, Y: tcase.wantY}, coord, "cursor scroll position")
		})
	}
}

func TestCursorMoveEndLineWithTabsNoWrap(t *testing.T) {
	const width, height = 10, 4

	// Establish the baseline window x for an end-of-line cursor on a long
	// no-tab line. Any extra drift past this position with tabs would mean
	// the cursor disappeared off-screen (the bug being fixed here).
	baseline := setupCursorContent(t, width, height,
		"abcdefghijklmnopqrstuvwxyz\nshort\n", false /*wrap*/)
	baseline.cursor = term.Coordinates{Y: 0}
	require.True(t, baseline.MoveEndLine())
	baseWin, _ := baseline.WindowCoordinates(baseline.CursorAtScroll())

	for _, tc := range []struct {
		name    string
		content string
	}{
		{
			name:    "tabs only",
			content: "\t\t\t\t\t\t\t\t\nshort\n",
		},
		{
			name:    "mixed tabs and text",
			content: "ab\tcd\tef\tgh\tij\tkl\nshort\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setupCursorContent(t, width, height, tc.content, false /*wrap*/)
			e.cursor = term.Coordinates{Y: 0}

			require.True(t, e.MoveEndLine())

			win, _ := e.WindowCoordinates(e.CursorAtScroll())
			assert.GreaterOrEqual(t, win.X, 0,
				"cursor x must not go negative")
			assert.Equal(t, baseWin.X, win.X,
				"end-of-line cursor with tabs must land where a no-tab line ends")
		})
	}
}

func TestCursorMultiMovePublish(t *testing.T) {
	suite := []struct {
		initialScrollPos term.Coordinates
		name             string
		moveFn           func(*Cursor) bool
	}{
		{
			initialScrollPos: term.Coordinates{X: 0, Y: 0},
			name:             "MoveDownLines multiplied move publishes a single scroll event",
			moveFn:           func(c *Cursor) bool { return c.MoveDownLines(44) },
		},
		{
			initialScrollPos: term.Coordinates{X: 0, Y: 100},
			name:             "MoveUpLines multiplied move publishes a single scroll event",
			moveFn:           func(c *Cursor) bool { return c.MoveUpLines(20) },
		},
		{
			initialScrollPos: term.Coordinates{X: 100, Y: 2},
			name:             "MoveLeftColumns multiplied move publishes a single scroll event",
			moveFn:           func(c *Cursor) bool { return c.MoveLeftColumns(33) },
		},
		{
			initialScrollPos: term.Coordinates{X: 0, Y: 3},
			name:             "MoveRightColumns multiplied move publishes a single scroll event",
			moveFn:           func(c *Cursor) bool { return c.MoveRightColumns(333) },
		},
	}

	for _, tcase := range suite {
		t.Run(tcase.name, func(t *testing.T) {
			e := setupCursor(t, 10, 6, false)
			e.cursor = tcase.initialScrollPos
			subscriberCalls := 0
			sub := component.FuncScrollSubscriber(func(from, to term.Coordinates) {
				subscriberCalls++
			})
			e.SubscribeScroll(sub)
			tcase.moveFn(e)
			assert.Equal(t, 1, subscriberCalls)
		})
	}
}

func TestCursorInsertLine(t *testing.T) {
	suite := []struct {
		wrap bool
	}{
		{false}, {true},
	}
	for _, test := range suite {
		t.Run(fmt.Sprintf("wrap: %v", test.wrap), func(t *testing.T) {
			e := setupCursor(t, 10, 10, test.wrap)

			assert.Equal(t, 32, e.scroll.Buffer().Rows())
			assert.Equal(t, term.Coordinates{}, e.Coordinates())
			assert.Equal(t, term.Coordinates{}, e.cursorAtScroll())

			e.InsertLineAbove(IndentRuneTab, 0)
			assert.Equal(t, 0, len(e.scroll.Buffer().RawCells()[0]))
			assert.Equal(t, 33, e.scroll.Buffer().Rows())
			assert.Equal(t, term.Coordinates{}, e.Coordinates())
			assert.Equal(t, term.Coordinates{}, e.cursorAtScroll())

			e.scroll.SeekEndLine()
			e.cursor.Y = 2
			e.cursor.X = 9

			assert.Equal(t, term.Coordinates{Y: 2, X: 9}, e.Coordinates())
			e.InsertLineAbove(IndentRuneTab, 0)
			assert.Equal(t, 0, len(e.scroll.Buffer().RawCells()[2]))
			assert.Equal(t, 34, e.scroll.Buffer().Rows())
			assert.Equal(t, term.Coordinates{Y: 2, X: 0}, e.Coordinates())

			e.MoveEndLine()
			e.InsertLineBelow(IndentRuneTab, 0)
			assert.Equal(t, 35, e.scroll.Buffer().Rows())
			assert.Equal(t, term.Coordinates{Y: 3, X: 0}, e.cursorAtScroll())

			e.MoveLastLine()
			e.MoveStartLine()
			assert.Equal(t, term.Coordinates{Y: 9, X: 0}, e.Coordinates())
			assert.Equal(t, term.Coordinates{Y: 34}, e.cursorAtScroll())

			e.InsertLineBelow(IndentRuneTab, 0)
			assert.Equal(t, 36, e.scroll.Buffer().Rows())
			assert.Equal(t, term.Coordinates{Y: 9, X: 0}, e.Coordinates())
			assert.Equal(t, term.Coordinates{Y: 35}, e.cursorAtScroll())
		})
	}
}

func TestCursorInsertLineBelow(t *testing.T) {
	suite := []struct {
		wrap bool
	}{
		{false}, {true},
	}
	for _, test := range suite {
		t.Run(fmt.Sprintf("wrap: %v", test.wrap), func(t *testing.T) {
			content := `package main
func main() {
}`
			cursor := setupCursorContent(t, 10, 10, content, test.wrap)
			buf := cursor.scroll.Buffer()
			assert.Equal(t, buf.String(), content)

			require.True(t, cursor.MoveLineDown())
			cursor.InsertLineBelow(IndentRuneTab, 0)
			cursor.InsertLineBelow(IndentRuneTab, 0)
			cursor.Insert('\t')
			cursor.Insert('f')
			cursor.Insert('m')
			cursor.Insert('t')
			cursor.Insert('.')

			assert.Equal(t, `package main
func main() {

	fmt.
}`, buf.String())

			require.True(t, cursor.MoveLastLine())

			cursor.InsertLineBelow(IndentRuneTab, 0)
			cursor.InsertLineBelow(IndentRuneTab, 0)
			cursor.Insert('i')
			cursor.InsertLineBelow(IndentRuneTab, 0)
			cursor.Insert('\t')
			cursor.InsertString("XXXXXXXXXXXXXXXXXXXXXXXX")
			cursor.InsertLineBelow(IndentRuneTab, 0)
			cursor.Insert('}')

			assert.Equal(t, `package main
func main() {

	fmt.
}

i
	XXXXXXXXXXXXXXXXXXXXXXXX
}`, buf.String())

			require.False(t, cursor.MoveLastLine())
			require.True(t, cursor.MoveLineUp())
			cursor.MoveStartLine()
			require.True(t, cursor.MoveEndLine())
			cursor.InsertLineBelow(IndentRuneTab, 0)
			cursor.InsertString("hello")

			assert.Equal(t, `package main
func main() {

	fmt.
}

i
	XXXXXXXXXXXXXXXXXXXXXXXX
hello
}`, buf.String())
		})
	}

}

func TestCursorInsertDeleteFirstEmptyLineEdgeCase(t *testing.T) {
	suite := []struct {
		wrap bool
	}{
		{true}, {false},
	}
	for _, test := range suite {
		t.Run(fmt.Sprintf("Delete from end, wrap:%v", test.wrap), func(t *testing.T) {
			e := setupCursorContent(t, 4, 4, "\n22222", test.wrap)
			str := e.scroll.Buffer().String()

			e.Insert('p')
			e.Insert('a')
			e.Insert('c')
			e.Insert('k')
			e.Insert('X')
			e.Insert('X')
			e.Insert('X')
			require.Equal(t, "packXXX\n22222", e.scroll.Buffer().String())

			for i := range 7 {
				require.True(t, e.MoveLeft(), i)
				e.Delete()
			}

			assert.Equal(t, str, e.scroll.Buffer().String())
		})
		t.Run(fmt.Sprintf("Delete from start, wrap:%v", test.wrap), func(t *testing.T) {
			e := setupCursorContent(t, 4, 4, "\n22222", test.wrap)
			str := e.scroll.Buffer().String()

			e.Insert('p')
			e.Insert('a')
			e.Insert('c')
			e.Insert('k')
			e.Insert('X')
			e.Insert('X')
			e.Insert('X')
			require.Equal(t, "packXXX\n22222", e.scroll.Buffer().String())

			e.MoveFirstLine()
			e.MoveStartLine()
			for range 7 {
				e.Delete()
			}

			assert.Equal(t, str, e.scroll.Buffer().String())
		})
	}
}

func TestCursorInsertLongStream(t *testing.T) {
	insertStr := func(c *Cursor, r rune) {
		c.InsertString(string(r))
	}
	suite := []struct {
		description string
		wrap        bool
		method      func(c *Cursor, r rune)
	}{
		{"Insert in wrap mode", true, (*Cursor).Insert},
		{"Insert in non-wrap mode", false, (*Cursor).Insert},
		{"InsertString in wrap mode", true, insertStr},
		{"InsertString in non-wrap mode", false, insertStr},
	}
	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			width, height := 4, 4
			e := setupCursorContent(t, width, height, "", test.wrap)
			for i := 0; i < width*height; i++ {
				test.method(e, rune(int('a')+i))
			}
			assert.Equal(t, "abcdefghijklmnop", e.scroll.Buffer().String())
		})
	}
}

// TestCursorInsertMultiRuneEmoji types multi-rune emoji one rune per
// keystroke, exactly as the vi/modeless insert path does (Cursor.Insert
// per key event), and asserts each grapheme cluster lands in a single
// cell with the continuation runes stored as combining. A skin-tone
// modifier or emoji variation selector that is left in its own cell
// renders as a broken box beside the base emoji.
func TestCursorInsertMultiRuneEmoji(t *testing.T) {
	cases := []struct {
		name  string
		runes []rune
		want  string
	}{
		{"skin tone", []rune{'\U0001F91F', '\U0001F3FC'}, "\U0001F91F\U0001F3FC"},
		{"variation selector", []rune{'\u2764', '\uFE0F'}, "\u2764\uFE0F"},
		{"zwj family", []rune{'\U0001F468', '\u200D', '\U0001F469', '\u200D', '\U0001F467'},
			"\U0001F468\u200D\U0001F469\u200D\U0001F467"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := setupCursorContent(t, 8, 4, "", false)
			for _, r := range tc.runes {
				e.Insert(r)
			}
			buf := e.scroll.Buffer()
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, 1, buf.Columns(0),
				"the cluster must occupy a single cell, not one per rune")
		})
	}
}

// TestCursorInsertMultiRuneEmojiInLine types multi-rune emoji one rune at
// a time between surrounding text, reproducing the reported editor bug
// where a ZWJ family (or skin-tone) sequence typed into a comment line
// fragmented into one cell per rune instead of coalescing into a single
// grapheme-cluster cell.
func TestCursorInsertMultiRuneEmojiInLine(t *testing.T) {
	cases := []struct {
		name  string
		runes []rune
		// wantCols is the expected column count of the whole line after
		// insertion: 4 (two ASCII prefix, the emoji cell, one ASCII
		// suffix) once the cluster occupies a single cell.
		wantCols int
		want     string
	}{
		{"skin tone", []rune{'\U0001F91F', '\U0001F3FC'}, 4, "//\U0001F91F\U0001F3FCx"},
		{"variation selector", []rune{'\u2764', '\uFE0F'}, 4, "//\u2764\uFE0Fx"},
		{
			name:     "zwj family",
			runes:    []rune{'\U0001F468', '\u200D', '\U0001F469', '\u200D', '\U0001F467'},
			wantCols: 4,
			want:     "//\U0001F468\u200D\U0001F469\u200D\U0001F467x",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := setupCursorContent(t, 16, 4, "//x", false)
			e.MoveFirstLine()
			e.MoveStartLine()
			require.True(t, e.MoveRight())
			require.True(t, e.MoveRight())
			for _, r := range tc.runes {
				e.Insert(r)
			}
			buf := e.scroll.Buffer()
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.wantCols, buf.Columns(0),
				"the cluster must occupy a single cell between the surrounding text")
		})
	}
}

func TestCursorBackspace(t *testing.T) {
	suite := []struct {
		wrap           bool
		rightInclusive bool
	}{
		{true, false}, {false, false},
		{true, true}, {false, true},
	}
	for _, test := range suite {
		t.Run(fmt.Sprintf("wrap:%v, right-inclusive:%t", test.wrap, test.rightInclusive), func(t *testing.T) {
			e := setupCursor(t, 10, 10, test.wrap)
			e.RightInclusiveSemantics = test.rightInclusive
			n := len(e.scroll.Buffer().String())

			require.True(t, e.MoveLastLine())
			require.True(t, e.MoveEndLine())

			for i := 0; i <= n; i++ {
				e.Backspace()
			}

			assert.Equal(t, "", e.scroll.Buffer().String())
		})
	}
}

func TestCursorBackspaceWord(t *testing.T) {
	suite := []struct {
		name       string
		content    string
		at         term.Coordinates
		wantOK     bool
		want       string
		wantCursor term.Coordinates
	}{
		{
			name:       "empty buffer",
			content:    "",
			at:         term.Coordinates{},
			wantOK:     false,
			want:       "",
			wantCursor: term.Coordinates{},
		},
		{
			name:       "buffer start",
			content:    "word",
			at:         term.Coordinates{},
			wantOK:     false,
			want:       "word",
			wantCursor: term.Coordinates{},
		},
		{
			name:       "at first word end deletes first word",
			content:    "word",
			at:         term.Coordinates{X: 4, Y: 0},
			wantOK:     true,
			want:       "",
			wantCursor: term.Coordinates{},
		},
		{
			name:       "deletes previous word and separating spaces",
			content:    "one  two",
			at:         term.Coordinates{X: 8, Y: 0},
			wantOK:     true,
			want:       "one  ",
			wantCursor: term.Coordinates{X: 5, Y: 0},
		},
		{
			name:       "at start of word deletes previous word and spaces",
			content:    "one  two",
			at:         term.Coordinates{X: 5, Y: 0},
			wantOK:     true,
			want:       "two",
			wantCursor: term.Coordinates{},
		},
		{
			name:       "inside word deletes word prefix",
			content:    "word",
			at:         term.Coordinates{X: 2, Y: 0},
			wantOK:     true,
			want:       "rd",
			wantCursor: term.Coordinates{},
		},
		{
			name:       "whitespace only deletes to line start",
			content:    "   ",
			at:         term.Coordinates{X: 3, Y: 0},
			wantOK:     true,
			want:       "",
			wantCursor: term.Coordinates{},
		},
		{
			name:       "punctuation treated as word group",
			content:    "foo.bar",
			at:         term.Coordinates{X: 7, Y: 0},
			wantOK:     true,
			want:       "foo.",
			wantCursor: term.Coordinates{X: 4, Y: 0},
		},
		{
			name:       "punctuation suffix deletes punctuation group only",
			content:    "foo...",
			at:         term.Coordinates{X: 6, Y: 0},
			wantOK:     true,
			want:       "foo",
			wantCursor: term.Coordinates{X: 3, Y: 0},
		},
		{
			name:       "crosses line boundary when at line start",
			content:    "one\ntwo",
			at:         term.Coordinates{X: 0, Y: 1},
			wantOK:     true,
			want:       "two",
			wantCursor: term.Coordinates{},
		},
		{
			name:       "crosses line boundary with trailing spaces",
			content:    "one  \ntwo",
			at:         term.Coordinates{X: 0, Y: 1},
			wantOK:     true,
			want:       "two",
			wantCursor: term.Coordinates{},
		},
		{
			name:       "stops at the line start when the word begins there",
			content:    "foo bar\nfoo baz",
			at:         term.Coordinates{X: 4, Y: 1},
			wantOK:     true,
			want:       "foo bar\nbaz",
			wantCursor: term.Coordinates{Y: 1},
		},
		{
			name:       "stops at the line start when spaces begin there",
			content:    "foo\n  bar",
			at:         term.Coordinates{X: 2, Y: 1},
			wantOK:     true,
			want:       "foo\nbar",
			wantCursor: term.Coordinates{Y: 1},
		},
	}

	for _, tc := range suite {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 20, 10, tc.content, false)
			c.RightInclusiveSemantics = true
			c.SetCursorAtScroll(tc.at)

			ok := c.BackspaceWord()
			mode, hasSelection := c.SelectionMode()
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.want, c.buffer().String())
			assert.Equal(t, tc.wantCursor, c.CursorAtScroll())
			assert.Equal(t, NoSelection, mode)
			assert.False(t, hasSelection)
		})
	}
}

func TestCursorConflate(t *testing.T) {
	conflateAllRows := func(t *testing.T, c *Cursor) {
		n := strings.Count(c.scroll.Buffer().String(), "\n")
		rows := c.scroll.Buffer().Rows()
		require.Equal(t, n+1, rows)

		for i := range n {
			require.True(t, c.Conflate(), i) //, "cursor: %+v, %s", c.Coordinates(), c.scroll.Buffer().String())
		}
		require.Equal(t, 1, c.scroll.Buffer().Rows())
		assert.Equal(t, 0, strings.Count(c.scroll.Buffer().String(), "\n"))
	}

	suite := []struct {
		description   string
		width, height int
		wrap          bool
		sut           func(*testing.T, *Cursor)
	}{
		{"conflate all rows into one (wrap)", 10, 10, true,
			conflateAllRows,
		},
		{"conflate all rows into one (wrap, no wraps)", 100, 100, true,
			conflateAllRows,
		},
		{"conflate all rows into one (no wrap)", 10, 10, false,
			conflateAllRows,
		},
	}
	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			e := setupCursor(t, test.width, test.height, test.wrap)
			test.sut(t, e)
		})
	}
}

func TestBackspaceViaConflate(t *testing.T) {
	const (
		str = `
/*
 * Check if the current buffer should be added to or removed from the list of
 * diff buffers.
 */
	void
diff_buf_adjust(win_T *win)
{
	win_T	*wp;
	int		i;

	if (!win->w_p_diff)
	{
	/* When there is no window showing a diff for this buffer, remove
	 * it from the diffs. */
	FOR_ALL_WINDOWS(wp)
		if (wp->w_buffer == win->w_buffer && wp->w_p_diff)
		break;
	if (wp == NULL)
	{
		i = diff_buf_idx(win->w_buffer);
		if (i != DB_COUNT)
		{X
`
		expected = `
/*
 * Check if the current buffer should be added to or removed from the list of
 * diff buffers.
 */
	void
diff_buf_adjust(win_T *win)
{
	win_T	*wp;
	int		i;

	if (!win->w_p_diff)
	{
	/* When there is no window showing a diff for this buffer, remove
	 * it from the diffs. */
	FOR_ALL_WINDOWS(wp)
		if (wp->w_buffer == win->w_buffer && wp->w_p_diff)
		break;
	if (wp == NULL)
	{
		i = diff_buf_idx(win->w_buffer);
		if (i != DB_COUNT)
		{`
	)
	e := setupCursorContent(t, 10, 10, str, true)
	e.MoveLastLine()
	e.MoveEndLine()

	// sut
	e.Backspace()
	e.Backspace()

	assert.Equal(t, expected, e.scroll.Buffer().String())
}

func testCursorSelect(t *testing.T, width, height int) {
	makeSelect := func(t *testing.T) *Cursor {
		e := setupCursor(t, width, height, false)
		str := e.scroll.Buffer().String()
		assertBufferAttributes(t, e.buffer(), term.Attributes{})
		assert.False(t, e.Unselect())
		require.True(t, e.SelectLine())

		for e.MoveDown() {
		}
		for e.MoveRight() {
		}
		// test that we can switch between after move
		require.True(t, e.Select())
		assert.Equal(t, str, e.Selection())
		return e
	}

	makeSelectLine := func(t *testing.T) *Cursor {
		e := setupCursor(t, width, height, false)
		str := e.scroll.Buffer().String()
		assertBufferAttributes(t, e.buffer(), term.Attributes{})
		require.True(t, e.Select())
		require.True(t, e.MoveLastLine())
		// test that we can switch between after move
		require.True(t, e.SelectLine())
		assert.Equal(t, fmt.Sprintf("%s\n", str), e.Selection())
		return e
	}

	makeSelectBlock := func(t *testing.T) *Cursor {
		e := setupCursor(t, width, height, false)
		assertBufferAttributes(t, e.buffer(), term.Attributes{})
		require.True(t, e.SelectLine())
		require.True(t, e.MoveLastLine())
		require.True(t, e.MoveEndLine())
		require.True(t, e.SelectBlock())
		return e
	}

	t.Run("Select then Unselect should reverse all attributes", func(t *testing.T) {
		e := makeSelect(t)
		assert.True(t, e.Unselect())
		assertBufferAttributes(t, e.buffer(), term.Attributes{})
	})

	t.Run("Select then insert should add attributes to inserted runes", func(t *testing.T) {
		inserts := []func(*Cursor){
			func(e *Cursor) {
				e.Insert('a')
				e.Insert('b')
				e.Insert('c')
			},
			func(e *Cursor) {
				e.InsertString("abc")
			},
		}
		for _, insert := range inserts {
			e := makeSelect(t)
			e.Unselect()
			e.MoveFirstLine()
			e.MoveStartLine()
			e.Select()
			e.MoveLastLine()
			e.MoveEndLine()
			insert(e)
			assertBufferAttributes(t, e.buffer(), term.Attributes{Attrs: term.AttrReverse})
			assert.True(t, e.Unselect())
			assertBufferAttributes(t, e.buffer(), term.Attributes{})
		}
	})

	t.Run("SelectLine then Unselect should reverse all attributes", func(t *testing.T) {
		e := makeSelectLine(t)
		assert.True(t, e.Unselect())
		assertBufferAttributes(t, e.buffer(), term.Attributes{})
	})

	t.Run("SelectBlock then Unselect should reverse all attributes", func(t *testing.T) {
		e := makeSelectBlock(t)
		assert.True(t, e.Unselect())
		assertBufferAttributes(t, e.buffer(), term.Attributes{})
	})

	t.Run("Select/DeleteSelection selects from start to end", func(t *testing.T) {
		e := makeSelect(t)
		require.True(t, e.DeleteSelection())
		assert.Equal(t, "", e.Selection())
		assertBufferAttributes(t, e.buffer(), term.Attributes{})
	})

	t.Run("reverse coords Select/DeleteSelection selects from start to end", func(t *testing.T) {
		e := setupCursor(t, width, height, false)
		str := e.scroll.Buffer().String()
		assertBufferAttributes(t, e.buffer(), term.Attributes{})

		require.True(t, e.MoveLastLine())
		require.True(t, e.MoveEndLine())

		require.True(t, e.Select())

		require.True(t, e.MoveFirstLine())

		assert.Equal(t, str, e.Selection())
		require.True(t, e.DeleteSelection())
		assert.Equal(t, "", e.scroll.Buffer().String())
	})

	t.Run("SelectLine/DeleteSelection selects from start line to end line", func(t *testing.T) {
		e := makeSelectLine(t)
		require.True(t, e.DeleteSelection())
		assertBufferAttributes(t, e.buffer(), term.Attributes{})
	})

	t.Run("SelectBlock/DeleteSelection selects from start to end in block", func(t *testing.T) {
		e := makeSelectBlock(t)
		require.True(t, e.DeleteSelection())
		assertBufferAttributes(t, e.buffer(), term.Attributes{})
	})

	t.Run("Select/CopySelection copies from start to end", func(t *testing.T) {
		e := makeSelect(t)
		c := clipboard.NewInMemory()
		ok, err := e.CopySelection(clipboard.DefaultRegisterID, c)
		require.True(t, ok)
		require.NoError(t, err)
		assert.Equal(t, "", e.Selection())
		assertBufferAttributes(t, e.buffer(), term.Attributes{})

		data, err := c.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, e.scroll.Buffer().String(), data.Text)
	})

	t.Run("Select/CopySelection doesn't panic if width is 0", func(t *testing.T) {
		e := makeSelect(t)
		e.scroll.Wrap = true
		e.scroll.Resize(0, 0)
		c := clipboard.NewInMemory()
		assert.NotPanics(t, func() {
			e.CopySelection(clipboard.DefaultRegisterID, c)
		})
	})

	t.Run("SelectLine/CopySelection copies from start line to end line", func(t *testing.T) {
		e := makeSelectLine(t)
		c := clipboard.NewInMemory()
		ok, err := e.CopySelection(clipboard.DefaultRegisterID, c)
		require.True(t, ok)
		require.NoError(t, err)
		assertBufferAttributes(t, e.buffer(), term.Attributes{})

		data, err := c.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.Equal(t, fmt.Sprintf("%s\n", e.scroll.Buffer().String()), data.Text)
	})

	t.Run("SelectBlock/CopySelection copies from start to end in block", func(t *testing.T) {
		e := makeSelectBlock(t)
		c := clipboard.NewInMemory()
		ok, err := e.CopySelection(clipboard.DefaultRegisterID, c)
		require.True(t, ok)
		require.NoError(t, err)
		assertBufferAttributes(t, e.buffer(), term.Attributes{})

		data, err := c.Paste(clipboard.DefaultRegisterID)
		require.NoError(t, err)
		assert.NotZero(t, data.Text)
	})
}

func assertBufferAttributes(t *testing.T, b *cell.Buffer, attr term.Attributes) {
	for y, row := range b.RawCells() {
		for x, c := range row {
			assert.Equal(t, attr.Bg, c.Bg, "at y=%d;x=%d", y, x)
			assert.Equal(t, attr.Fg, c.Fg, "at y=%d;x=%d", y, x)
		}
	}
}

func TestCursorSelect10(t *testing.T) {
	testCursorSelect(t, 10, 100)
}

func TestCursorSelect20(t *testing.T) {
	testCursorSelect(t, 20, 100)
}

func TestCursorSelect50(t *testing.T) {
	testCursorSelect(t, 50, 100)
}

func TestCursorSelect100(t *testing.T) {
	testCursorSelect(t, 100, 100)
}

func testCursorUndoRedo(t *testing.T, moveBefore, moveAfter func(c *Cursor) bool, width, height int) {
	const input = "Aleda"
	e := setupCursor(t, width, height, false)
	str := e.scroll.Buffer().String()

	moveBefore(e)
	cBefore := e.Coordinates()

	e.InsertString(input)
	str2 := e.scroll.Buffer().String()

	require.True(t, e.Undo())
	require.False(t, e.Undo())

	c := e.Coordinates()
	assert.Equal(t, cBefore, c)
	assert.Equal(t, str, e.scroll.Buffer().String())

	moveAfter(e)

	require.True(t, e.Redo())
	assert.Equal(t, str2, e.scroll.Buffer().String())
	require.False(t, e.Redo())

	require.True(t, e.Undo())
	require.False(t, e.Undo())

	c = e.Coordinates()
	assert.Equal(t, cBefore, c)
	assert.Equal(t, str, e.scroll.Buffer().String())
}

func TestCursorUndoRedo10(t *testing.T) {
	testCursorUndoRedo(t, (*Cursor).MoveFirstLine, (*Cursor).MoveLastLine, 10, 10)
}
func TestCursorUndoRedo20(t *testing.T) {
	testCursorUndoRedo(t, (*Cursor).MoveFirstLine, (*Cursor).MoveLastLine, 20, 20)
}
func TestCursorUndoRedo100(t *testing.T) {
	testCursorUndoRedo(t, (*Cursor).MoveFirstLine, (*Cursor).MoveLastLine, 100, 100)
}

func TestCursorUndoRedo10Backwards(t *testing.T) {
	testCursorUndoRedo(t, (*Cursor).MoveLastLine, (*Cursor).MoveFirstLine, 10, 10)
}
func TestCursorUndoRedo20Backwards(t *testing.T) {
	testCursorUndoRedo(t, (*Cursor).MoveLastLine, (*Cursor).MoveFirstLine, 20, 20)
}
func TestCursorUndoRedo100Backwards(t *testing.T) {
	testCursorUndoRedo(t, (*Cursor).MoveLastLine, (*Cursor).MoveFirstLine, 100, 100)
}

func testCursorDeleteSelection(t *testing.T, width, height int, typeSelect SelectMode) {
	tsuite := []struct {
		initialBuf              string
		initialPos              func(*Cursor)
		finalPos                func(*Cursor)
		deleted                 bool
		finalBuf                string
		skipForMode             []SelectMode
		rightInclusiveSemantics bool
	}{
		{
			initialBuf:  "",
			finalPos:    func(*Cursor) {},
			deleted:     false,
			finalBuf:    "",
			skipForMode: []SelectMode{LineSelection},
		},
		{
			initialBuf:  "a",
			finalPos:    func(*Cursor) {},
			deleted:     false,
			finalBuf:    "a",
			skipForMode: []SelectMode{LineSelection},
		},
		{
			initialBuf: "a",
			finalPos:   func(c *Cursor) { c.MoveRight() },
			deleted:    true,
			finalBuf:   "",
		},
		{
			initialBuf:  "\n",
			finalPos:    func(*Cursor) {},
			deleted:     false,
			finalBuf:    "\n",
			skipForMode: []SelectMode{BlockSelection, LineSelection},
		},
		{
			initialBuf:  "\n",
			finalPos:    func(c *Cursor) { c.MoveDown() },
			deleted:     true,
			finalBuf:    "",
			skipForMode: []SelectMode{BlockSelection},
		},
		{
			initialBuf:  "a\nb",
			finalPos:    func(c *Cursor) { c.MoveRight() },
			deleted:     true,
			finalBuf:    "\nb",
			skipForMode: []SelectMode{LineSelection, BlockSelection},
		},
		{
			initialBuf:  "a\nb",
			finalPos:    func(c *Cursor) { c.MoveDown() },
			finalBuf:    "b",
			deleted:     true,
			skipForMode: []SelectMode{LineSelection, BlockSelection},
		},
		{
			initialBuf: "a\nb",
			initialPos: func(c *Cursor) {
				for c.MoveRight() {
				}
			},
			finalPos: func(c *Cursor) {
				c.MoveDown()
				c.MoveRight()
			},
			deleted:     true,
			finalBuf:    "a",
			skipForMode: []SelectMode{LineSelection, BlockSelection},
		},
		{
			initialBuf: "a\nb\nc\nd",
			finalPos: func(c *Cursor) {
				for c.MoveDown() {
				}
				c.MoveRight()
			},
			deleted:     true,
			finalBuf:    "",
			skipForMode: []SelectMode{BlockSelection},
		},
		{
			initialBuf: "a\nb",
			initialPos: func(c *Cursor) {
				for range 100 {
					c.MoveDown()
				}
			},
			finalPos:    func(*Cursor) {},
			deleted:     false,
			finalBuf:    "a\nb",
			skipForMode: []SelectMode{LineSelection},
		},
		{
			initialBuf: "a\nb\nc\nd",
			finalPos: func(c *Cursor) {
				c.MoveLastLine()
				c.MoveEndLine()
			},
			deleted:     true,
			finalBuf:    "",
			skipForMode: []SelectMode{BlockSelection},
		},
		{
			initialBuf: "type Writer {\n\ta int\n\tb int\n}\n",
			initialPos: func(c *Cursor) {
				c.MoveLastLine()
			},
			finalPos: func(c *Cursor) {
				c.MoveFirstLine()
			},
			deleted:     true,
			finalBuf:    "",
			skipForMode: []SelectMode{StandardSelection, BlockSelection},
		},
		{
			initialBuf:              "",
			finalPos:                func(*Cursor) {},
			deleted:                 false,
			finalBuf:                "",
			rightInclusiveSemantics: true,
		},
		{
			initialBuf:              "a",
			finalPos:                func(*Cursor) {},
			deleted:                 true,
			finalBuf:                "",
			rightInclusiveSemantics: true,
		},
		{
			initialBuf:              "\n",
			finalPos:                func(*Cursor) {},
			deleted:                 true,
			finalBuf:                "",
			skipForMode:             []SelectMode{StandardSelection, BlockSelection},
			rightInclusiveSemantics: true,
		},
		{
			initialBuf:              "a\nb",
			finalPos:                func(c *Cursor) { c.MoveRight() },
			deleted:                 true,
			finalBuf:                "\nb",
			skipForMode:             []SelectMode{LineSelection, BlockSelection},
			rightInclusiveSemantics: true,
		},
		{
			initialBuf:              "a\nb",
			finalPos:                func(c *Cursor) { c.MoveDown() },
			finalBuf:                "",
			deleted:                 true,
			skipForMode:             []SelectMode{LineSelection, BlockSelection},
			rightInclusiveSemantics: true,
		},
		{
			initialBuf: "a\nb",
			initialPos: func(c *Cursor) {
				for range 3 {
					c.MoveRight()
				}
			},
			finalPos:                func(c *Cursor) { c.MoveDown() },
			deleted:                 true,
			finalBuf:                "a",
			skipForMode:             []SelectMode{LineSelection, BlockSelection},
			rightInclusiveSemantics: true,
		},
		{
			initialBuf: "a\nb\nc\nd",
			finalPos: func(c *Cursor) {
				for c.MoveDown() {
				}
			},
			deleted:                 true,
			finalBuf:                "",
			skipForMode:             []SelectMode{BlockSelection},
			rightInclusiveSemantics: true,
		},
		{
			initialBuf: "a\nb",
			initialPos: func(c *Cursor) {
				for c.MoveDown() {
				}
				c.MoveRight()
			},
			finalPos:                func(*Cursor) {},
			deleted:                 false,
			finalBuf:                "a\nb",
			skipForMode:             []SelectMode{LineSelection},
			rightInclusiveSemantics: true,
		},
		{
			initialBuf: "a\nb\nc\nd",
			finalPos: func(c *Cursor) {
				c.MoveLastLine()
				c.MoveEndLine()
			},
			deleted:                 true,
			finalBuf:                "",
			skipForMode:             []SelectMode{BlockSelection},
			rightInclusiveSemantics: true,
		},
		{
			initialBuf: "type Writer {\n\ta int\n\tb int\n}\n",
			initialPos: func(c *Cursor) {
				c.MoveLastLine()
			},
			finalPos: func(c *Cursor) {
				c.MoveFirstLine()
			},
			deleted:                 true,
			finalBuf:                "",
			skipForMode:             []SelectMode{StandardSelection, BlockSelection},
			rightInclusiveSemantics: true,
		},
	}

	for i, tcase := range tsuite {
		desc := fmt.Sprintf("select %v test case %d, right inclusive semantics: %t",
			typeSelect, i, tcase.rightInclusiveSemantics)
		t.Run(desc, func(t *testing.T) {
			if slices.Contains(tcase.skipForMode, typeSelect) {
				return
			}

			c := setupCursorContent(t, width, height, tcase.initialBuf, false)
			c.RightInclusiveSemantics = tcase.rightInclusiveSemantics
			if tcase.initialPos != nil {
				tcase.initialPos(c)
			}
			switch typeSelect {
			case NoSelection:
				panic("hmm...")
			case BlockSelection:
				c.SelectBlock()
			case LineSelection:
				c.SelectLine()
			case StandardSelection:
				c.Select()
			}
			tcase.finalPos(c)
			require.Equal(t, tcase.deleted, c.DeleteSelection(), "deleted ok")
			if !tcase.deleted {
				return
			}
			assert.Equal(t, tcase.finalBuf, c.scroll.Buffer().String())
		})
	}
}

func TestCursorDeleteSelection10(t *testing.T) {
	testCursorDeleteSelection(t, 10, 10, StandardSelection)
}
func TestCursorDeleteSelection20(t *testing.T) {
	testCursorDeleteSelection(t, 20, 20, StandardSelection)
}
func TestCursorDeleteSelection1000(t *testing.T) {
	testCursorDeleteSelection(t, 1000, 1000, StandardSelection)
}
func TestCursorDeleteSelectionLine10(t *testing.T) {
	testCursorDeleteSelection(t, 10, 10, LineSelection)
}
func TestCursorDeleteSelectionLine20(t *testing.T) {
	testCursorDeleteSelection(t, 20, 20, LineSelection)
}
func TestCursorDeleteSelectionLine1000(t *testing.T) {
	testCursorDeleteSelection(t, 1000, 1000, LineSelection)
}
func TestCursorDeleteSelectionBlock10(t *testing.T) {
	testCursorDeleteSelection(t, 10, 10, BlockSelection)
}
func TestCursorDeleteSelectionBlock20(t *testing.T) {
	testCursorDeleteSelection(t, 20, 20, BlockSelection)
}
func TestCursorDeleteSelectionBlock1000(t *testing.T) {
	testCursorDeleteSelection(t, 1000, 1000, BlockSelection)
}

func TestCursorMoveToBounds(t *testing.T) {
	tsuite := []struct {
		desc          string
		content       string
		cursorWin     term.Coordinates
		offset        term.Coordinates
		width, height int
		padding       int

		wantScroll term.Coordinates
	}{
		{"empty buf does nothing without padding",
			"", term.Coordinates{}, term.Coordinates{}, 10, 10, 0,
			term.Coordinates{}},
		{"empty buf does nothing with padding",
			"", term.Coordinates{}, term.Coordinates{}, 10, 10, 2,
			term.Coordinates{}},
		{"negative cursor window X",
			"a\nb", term.Coordinates{Y: 3, X: -3}, term.Coordinates{}, 5, 5, 1,
			term.Coordinates{Y: 1}},
		{"negative cursor window Y",
			"a\nb", term.Coordinates{X: 1, Y: -3}, term.Coordinates{}, 5, 5, 0,
			term.Coordinates{}},
		{"no offset, no padding out of X bounds first line",
			"a\nb", term.Coordinates{X: 2}, term.Coordinates{}, 5, 5, 0,
			term.Coordinates{X: 0}},
		{"no offset, with padding out of X bounds first line",
			"a\nb", term.Coordinates{X: 2}, term.Coordinates{}, 5, 5, 1,
			term.Coordinates{X: 1}},
		{"no offset, no padding out of X bounds last line",
			"a\nb", term.Coordinates{X: 2, Y: 1}, term.Coordinates{}, 5, 5, 0,
			term.Coordinates{X: 0, Y: 1}},
		{"no offset, with padding out of X bounds last line",
			"a\nb", term.Coordinates{X: 2, Y: 1}, term.Coordinates{}, 5, 5, 1,
			term.Coordinates{X: 1, Y: 1}},
		{"no offset, with padding NOT out of X bounds",
			"a\nb", term.Coordinates{X: 2, Y: 1}, term.Coordinates{}, 5, 5, 2,
			term.Coordinates{X: 2, Y: 1}},
		{"offset, no padding out of X bounds first line",
			"a\nbbbbb", term.Coordinates{X: 2}, term.Coordinates{X: 1}, 2, 2, 0,
			term.Coordinates{X: 0}},
		{"offset, with padding out of X bounds first line",
			"a\nbbbbbb", term.Coordinates{X: 2}, term.Coordinates{X: 1}, 2, 2, 1,
			term.Coordinates{X: 1}},
		{"offset, no padding out of X bounds last line",
			"aaaaaaa\nb", term.Coordinates{X: 1, Y: 1}, term.Coordinates{X: 1}, 2, 2, 0,
			term.Coordinates{X: 0, Y: 1}},
		{"offset, with padding out of X bounds last line",
			"aaaaaaa\nb", term.Coordinates{X: 1, Y: 1}, term.Coordinates{X: 1}, 2, 2, 1,
			term.Coordinates{X: 1, Y: 1}},
		{"offset, with padding NOT out of X bounds",
			"aaaaaaaaa\nb", term.Coordinates{X: 0, Y: 1}, term.Coordinates{X: 2}, 2, 2, 2,
			term.Coordinates{X: 2, Y: 1}},
		{"no offset, last EOL",
			"a\n", term.Coordinates{X: 3, Y: 2}, term.Coordinates{}, 5, 5, 0,
			term.Coordinates{X: 0, Y: 1}},
		{"offset, last EOL",
			"aaaaaaaaaaaa\n", term.Coordinates{}, term.Coordinates{X: 3, Y: 1}, 2, 1, 0,
			term.Coordinates{X: 0, Y: 1}},
	}
	for _, tcase := range tsuite {
		t.Run(tcase.desc, func(t *testing.T) {
			e := setupCursorContent(t, tcase.width, tcase.height, tcase.content, false)
			e.cursor = tcase.cursorWin
			if tcase.offset != (term.Coordinates{}) {
				require.True(t, e.scroll.SetOffset(tcase.offset))
			}

			e.MoveToBounds(tcase.padding)
			assert.Equal(t, tcase.wantScroll, e.cursorAtScroll())
		})
	}
}

func TestCursorMoveToBoundsOld(t *testing.T) {
	e := setupCursor(t, 100, 100, false)

	pos := e.Coordinates()
	assert.Equal(t, term.Coordinates{}, pos)

	e.cursor.X += 3

	e.MoveToBounds(2)

	pos = e.Coordinates()
	assert.Equal(t, term.Coordinates{X: 1}, pos)

	pos = e.Coordinates()
	assert.True(t, e.MoveDown())

	e.MoveEndLine()
	e.MoveRight()
	e.MoveRight()

	e.MoveToBounds(1)

	pos = e.Coordinates()
	assert.Equal(t, term.Coordinates{X: 2, Y: 1}, pos)

	e.MoveToBounds(0)

	pos = e.Coordinates()
	assert.Equal(t, term.Coordinates{X: 1, Y: 1}, pos)

	e.MoveLastLine()
	e.MoveDown()

	e.MoveToBounds(0)

	pos = e.Coordinates()
	assert.Equal(t, term.Coordinates{X: 1, Y: 31}, pos)
}

func TestCursorCell(t *testing.T) {
	t.Run("does not panic if cursor has negative coords", func(t *testing.T) {
		e := setupCursor(t, 100, 100, false)
		e.cursor = term.Coordinates{X: -1}
		_, ok := e.Cell()
		assert.False(t, ok)
	})
}

func TestCursorShiftLine(t *testing.T) {
	c := setupCursorContent(t, 10, 1, " blabla\nbleble", false)
	assert.True(t, c.ShiftLineLeft(IndentRuneTab, 0))
	assert.False(t, c.ShiftLineLeft(IndentRuneTab, 0))
	assert.Equal(t, term.Coordinates{}, c.cursor)

	c.ShiftLineRight(IndentRuneTab, 0)
	assert.Equal(t, term.Coordinates{X: 4}, c.cursor)
	c.ShiftLineRight(IndentRuneTab, 0)
	assert.Equal(t, term.Coordinates{X: 8}, c.cursor)
	assert.True(t, c.ShiftLineLeft(IndentRuneTab, 0))
	assert.Equal(t, term.Coordinates{X: 4}, c.cursor)
	assert.True(t, c.ShiftLineLeft(IndentRuneTab, 0))
	assert.Equal(t, term.Coordinates{}, c.cursor)
	assert.True(t, c.MoveDown())
	c.ShiftLineRight(IndentRuneTab, 0)
	assert.Equal(t, term.Coordinates{Y: 0, X: 4}, c.cursor)
	assert.True(t, c.ShiftLineLeft(IndentRuneTab, 0))
	assert.Equal(t, term.Coordinates{Y: 0, X: 0}, c.cursor)
}

func TestCursorShiftSelection(t *testing.T) {
	c := setupCursorContent(t, 10, 10, " blabla\nbleble", false)
	require.True(t, c.Select())
	require.True(t, c.MoveDown())

	c.ShiftSelectionRight(IndentRuneTab, 0)
	assert.Equal(t, "\t blabla\n\tbleble", c.scroll.Buffer().String())

	require.True(t, c.SelectBlock())
	require.True(t, c.MoveUp())
	assert.True(t, c.ShiftSelectionLeft(IndentRuneTab, 0))
	assert.Equal(t, " blabla\nbleble", c.scroll.Buffer().String())
}

// TestCursorShiftLineRightTable covers Cursor.ShiftLineRight across both
// indent materials, tabspaces widths, starting cursor positions and
// pre-existing indentation.
//
// wantCursor is the cursor in scroll.Buffer window coordinates after the
// shift. Note that when indent material is a tab and the column the cursor
// lands on is itself a tab, the window coordinate snaps to the right edge of
// that tab cell (tabspaces-1 columns past its left edge) — that is the
// existing Scroll coordinate semantics for tabs.
func TestCursorShiftLineRightTable(t *testing.T) {
	tsuite := []struct {
		name       string
		content    string
		tabspaces  int
		indentRune rune
		cursorAt   term.Coordinates
		want       string
		wantCursor term.Coordinates
	}{
		{
			name:       "tab on empty line inserts a tab",
			content:    "",
			tabspaces:  4,
			indentRune: IndentRuneTab,
			want:       "\t",
			// cursor past the inserted tab; scroll {1,0} renders at window X:4
			wantCursor: term.Coordinates{X: 4},
		},
		{
			name:       "tab on simple line inserts a single tab at start",
			content:    "hello",
			tabspaces:  4,
			indentRune: IndentRuneTab,
			want:       "\thello",
			// scroll {1,0} = 'h'; window X:4 (just past the tab)
			wantCursor: term.Coordinates{X: 4},
		},
		{
			name:       "tab on already-indented line appends another tab",
			content:    "\thello",
			tabspaces:  4,
			indentRune: IndentRuneTab,
			want:       "\t\thello",
			// scroll {1,0} = the second tab cell; window snaps to the right
			// edge of that tab (col 7 = 2*tabspaces-1)
			wantCursor: term.Coordinates{X: 7},
		},
		{
			name:       "tab preserves cursor on non-first row",
			content:    "a\nb",
			tabspaces:  4,
			indentRune: IndentRuneTab,
			cursorAt:   term.Coordinates{Y: 1},
			want:       "a\n\tb",
			// scroll {1,1} = 'b'; window X:4
			wantCursor: term.Coordinates{X: 4, Y: 1},
		},
		{
			name:       "space indent at tabspaces=2 inserts two spaces",
			content:    "hello",
			tabspaces:  2,
			indentRune: IndentRuneSpace,
			want:       "  hello",
			wantCursor: term.Coordinates{X: 2},
		},
		{
			name:       "space indent at tabspaces=4 inserts four spaces",
			content:    "hello",
			tabspaces:  4,
			indentRune: IndentRuneSpace,
			want:       "    hello",
			wantCursor: term.Coordinates{X: 4},
		},
		{
			name:       "space indent appends onto existing spaces",
			content:    "  hello",
			tabspaces:  2,
			indentRune: IndentRuneSpace,
			want:       "    hello",
			// cursor stays at scroll {2,0} = third space; window X:2
			wantCursor: term.Coordinates{X: 2},
		},
		{
			name:       "space indent on empty line inserts tabspaces spaces",
			content:    "",
			tabspaces:  3,
			indentRune: IndentRuneSpace,
			want:       "   ",
			wantCursor: term.Coordinates{X: 3},
		},
		{
			name:       "space indent zero tabspaces defaults to one space",
			content:    "hello",
			tabspaces:  0,
			indentRune: IndentRuneSpace,
			want:       " hello",
			wantCursor: term.Coordinates{X: 1},
		},
	}

	for _, tc := range tsuite {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 30, 10, tc.content, false)
			c.scroll.SetTabspaces(tc.tabspaces)
			if tc.cursorAt != (term.Coordinates{}) {
				_, ok := c.MoveToScroll(tc.cursorAt)
				require.True(t, ok)
			}

			c.ShiftLineRight(tc.indentRune, 0)

			assert.Equal(t, tc.want, c.scroll.Buffer().String())
			assert.Equal(t, tc.wantCursor, c.cursor)
			if tc.indentRune == IndentRuneSpace {
				assert.NotContains(t, c.scroll.Buffer().String(), "\t",
					"space indent must never introduce tabs")
			}
		})
	}
}

// TestCursorShiftLineLeftTable covers Cursor.ShiftLineLeft across indent
// materials, tabspaces widths and mixed leading whitespace shapes.
func TestCursorShiftLineLeftTable(t *testing.T) {
	tsuite := []struct {
		name       string
		content    string
		tabspaces  int
		indentRune rune
		cursorAt   term.Coordinates
		wantOk     bool
		want       string
		wantCursor term.Coordinates
	}{
		{
			name:       "tab removes a leading tab",
			content:    "\thello",
			tabspaces:  4,
			indentRune: IndentRuneTab,
			cursorAt:   term.Coordinates{X: 1},
			wantOk:     true,
			want:       "hello",
			wantCursor: term.Coordinates{X: 0},
		},
		{
			name:       "tab removes a single leading space",
			content:    " hello",
			tabspaces:  4,
			indentRune: IndentRuneTab,
			cursorAt:   term.Coordinates{X: 1},
			wantOk:     true,
			want:       "hello",
			wantCursor: term.Coordinates{X: 0},
		},
		{
			name:       "tab on non-indented line is a no-op",
			content:    "hello",
			tabspaces:  4,
			indentRune: IndentRuneTab,
			wantOk:     false,
			want:       "hello",
			wantCursor: term.Coordinates{},
		},
		{
			name:       "space removes tabspaces leading spaces",
			content:    "    hello",
			tabspaces:  2,
			indentRune: IndentRuneSpace,
			cursorAt:   term.Coordinates{X: 4},
			wantOk:     true,
			want:       "  hello",
			wantCursor: term.Coordinates{X: 2},
		},
		{
			name:       "space removes up to tabspaces when fewer available",
			content:    " hello",
			tabspaces:  4,
			indentRune: IndentRuneSpace,
			cursorAt:   term.Coordinates{X: 1},
			wantOk:     true,
			want:       "hello",
			wantCursor: term.Coordinates{X: 0},
		},
		{
			name:       "space on non-indented line is a no-op",
			content:    "hello",
			tabspaces:  4,
			indentRune: IndentRuneSpace,
			wantOk:     false,
			want:       "hello",
			wantCursor: term.Coordinates{},
		},
		{
			name:       "space treats a leading tab as one full indent level",
			content:    "\thello",
			tabspaces:  4,
			indentRune: IndentRuneSpace,
			cursorAt:   term.Coordinates{X: 1},
			wantOk:     true,
			want:       "hello",
			wantCursor: term.Coordinates{X: 0},
		},
		{
			name:       "space stops removing at a mid-indent tab boundary",
			content:    "  \thello",
			tabspaces:  4,
			indentRune: IndentRuneSpace,
			wantOk:     true,
			want:       "\thello",
			// After dedent, the cursor is at scroll {0,0} and the remaining
			// leading tab spans columns 0..tabspaces-1 in window coords, so
			// the window X snaps to its right edge (tabspaces-1).
			wantCursor: term.Coordinates{X: 3},
		},
		{
			name:       "space clamps negative cursor to zero",
			content:    "  hello",
			tabspaces:  4,
			indentRune: IndentRuneSpace,
			cursorAt:   term.Coordinates{X: 1},
			wantOk:     true,
			want:       "hello",
			wantCursor: term.Coordinates{X: 0},
		},
	}

	for _, tc := range tsuite {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 30, 10, tc.content, false)
			c.scroll.SetTabspaces(tc.tabspaces)
			if tc.cursorAt != (term.Coordinates{}) {
				_, ok := c.MoveToScroll(tc.cursorAt)
				require.True(t, ok)
			}

			ok := c.ShiftLineLeft(tc.indentRune, 0)

			assert.Equal(t, tc.wantOk, ok)
			assert.Equal(t, tc.want, c.scroll.Buffer().String())
			assert.Equal(t, tc.wantCursor, c.cursor)
			if tc.indentRune == IndentRuneSpace && !strings.Contains(tc.content, "\t") {
				assert.NotContains(t, c.scroll.Buffer().String(), "\t",
					"space dedent of space-only indent must not introduce tabs")
			}
		})
	}
}

// TestCursorShiftSelectionRightTable covers Cursor.ShiftSelectionRight across
// indent materials, tabspaces widths and selection modes (line and block),
// spanning multiple rows including empty rows.
func TestCursorShiftSelectionRightTable(t *testing.T) {
	tsuite := []struct {
		name       string
		content    string
		tabspaces  int
		indentRune rune
		// selectSetup is called after setup to create the selection. Returns
		// to/from — the test only uses it to drive the handler; assertions are
		// on the resulting buffer.
		selectSetup func(*Cursor)
		want        string
	}{
		{
			name:       "tab indents line-selected rows",
			content:    "a\nb",
			tabspaces:  4,
			indentRune: IndentRuneTab,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
			},
			want: "\ta\n\tb",
		},
		{
			name:       "space indents line-selected rows at tabspaces=2",
			content:    "a\nb",
			tabspaces:  2,
			indentRune: IndentRuneSpace,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
			},
			want: "  a\n  b",
		},
		{
			name:       "space indents line-selected rows at tabspaces=4",
			content:    "a\nb\nc",
			tabspaces:  4,
			indentRune: IndentRuneSpace,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
				require.True(t, c.MoveDown())
			},
			want: "    a\n    b\n    c",
		},
		{
			name:       "space indents an empty row in the selection",
			content:    "a\n\nb",
			tabspaces:  2,
			indentRune: IndentRuneSpace,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
				require.True(t, c.MoveDown())
			},
			want: "  a\n  \n  b",
		},
		{
			name:       "space indents pre-indented rows without introducing tabs",
			content:    "  a\n    b",
			tabspaces:  2,
			indentRune: IndentRuneSpace,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
			},
			want: "    a\n      b",
		},
		{
			name:       "tab on single-row selection indents that row",
			content:    "hello\nworld",
			tabspaces:  4,
			indentRune: IndentRuneTab,
			selectSetup: func(c *Cursor) {
				require.True(t, c.Select())
				require.True(t, c.MoveRight())
			},
			want: "\thello\nworld",
		},
		{
			name:       "space on single-row selection indents that row only",
			content:    "hello\nworld",
			tabspaces:  2,
			indentRune: IndentRuneSpace,
			selectSetup: func(c *Cursor) {
				require.True(t, c.Select())
				require.True(t, c.MoveRight())
			},
			want: "  hello\nworld",
		},
	}

	for _, tc := range tsuite {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 30, 10, tc.content, false)
			c.scroll.SetTabspaces(tc.tabspaces)
			tc.selectSetup(c)

			c.ShiftSelectionRight(tc.indentRune, 0)

			assert.Equal(t, tc.want, c.scroll.Buffer().String())
			if tc.indentRune == IndentRuneSpace {
				// only meaningful when the input contained no tabs
				if !strings.Contains(tc.content, "\t") {
					assert.NotContains(t, c.scroll.Buffer().String(), "\t",
						"space indent must never introduce tabs")
				}
			}
		})
	}
}

// TestCursorShiftSelectionLeftTable covers Cursor.ShiftSelectionLeft across
// indent materials, with mixed leading whitespace, asserting both the bool
// return and the resulting buffer.
func TestCursorShiftSelectionLeftTable(t *testing.T) {
	tsuite := []struct {
		name        string
		content     string
		tabspaces   int
		indentRune  rune
		selectSetup func(*Cursor)
		wantOk      bool
		want        string
	}{
		{
			name:       "tab dedents tab-indented rows",
			content:    "\ta\n\tb",
			tabspaces:  4,
			indentRune: IndentRuneTab,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
			},
			wantOk: true,
			want:   "a\nb",
		},
		{
			name:       "tab on all non-indented rows is a no-op",
			content:    "a\nb",
			tabspaces:  4,
			indentRune: IndentRuneTab,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
			},
			wantOk: false,
			want:   "a\nb",
		},
		{
			name:       "tab returns ok true if any row was dedented",
			content:    "\ta\nb",
			tabspaces:  4,
			indentRune: IndentRuneTab,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
			},
			wantOk: true,
			want:   "a\nb",
		},
		{
			name:       "space dedents space-indented rows at tabspaces=2",
			content:    "  a\n  b",
			tabspaces:  2,
			indentRune: IndentRuneSpace,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
			},
			wantOk: true,
			want:   "a\nb",
		},
		{
			name:       "space dedents only up to tabspaces per row",
			content:    "    a\n      b",
			tabspaces:  2,
			indentRune: IndentRuneSpace,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
			},
			wantOk: true,
			want:   "  a\n    b",
		},
		{
			name:       "space dedents partial indent when fewer spaces than tabspaces",
			content:    " a\n   b",
			tabspaces:  4,
			indentRune: IndentRuneSpace,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
			},
			wantOk: true,
			want:   "a\nb",
		},
		{
			name:       "space on all non-indented rows is a no-op",
			content:    "a\nb",
			tabspaces:  2,
			indentRune: IndentRuneSpace,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
			},
			wantOk: false,
			want:   "a\nb",
		},
		{
			name:       "space collapses a leading tab to empty",
			content:    "\ta\n\tb",
			tabspaces:  4,
			indentRune: IndentRuneSpace,
			selectSetup: func(c *Cursor) {
				require.True(t, c.SelectLine())
				require.True(t, c.MoveDown())
			},
			wantOk: true,
			want:   "a\nb",
		},
	}

	for _, tc := range tsuite {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 30, 10, tc.content, false)
			c.scroll.SetTabspaces(tc.tabspaces)
			tc.selectSetup(c)

			ok := c.ShiftSelectionLeft(tc.indentRune, 0)

			assert.Equal(t, tc.wantOk, ok)
			assert.Equal(t, tc.want, c.scroll.Buffer().String())
		})
	}
}

// TestCursorShiftLineRoundTripTable ensures shift-right followed by shift-left
// restores the original buffer for both indent materials.
func TestCursorShiftLineRoundTripTable(t *testing.T) {
	tsuite := []struct {
		name       string
		content    string
		tabspaces  int
		indentRune rune
	}{
		{"tab roundtrip on plain line", "hello", 4, IndentRuneTab},
		{"tab roundtrip on pre-indented line", "\thello", 4, IndentRuneTab},
		{"space roundtrip at tabspaces=2", "hello", 2, IndentRuneSpace},
		{"space roundtrip at tabspaces=4", "hello", 4, IndentRuneSpace},
		{"space roundtrip on pre-indented line", "  hello", 2, IndentRuneSpace},
	}

	for _, tc := range tsuite {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 30, 5, tc.content, false)
			c.scroll.SetTabspaces(tc.tabspaces)

			c.ShiftLineRight(tc.indentRune, 0)
			assert.NotEqual(t, tc.content, c.scroll.Buffer().String(),
				"shift-right must change the buffer")

			assert.True(t, c.ShiftLineLeft(tc.indentRune, 0),
				"shift-left after shift-right must succeed")
			assert.Equal(t, tc.content, c.scroll.Buffer().String(),
				"roundtrip must restore original content")
		})
	}
}

func TestCursorAutoPair(t *testing.T) {
	t.Run("InsertWithAutoPair", func(t *testing.T) {
		tsuite := []struct {
			name       string
			content    string
			at         term.Coordinates
			ch         rune
			wantBuffer string
			wantCursor term.Coordinates
		}{
			// Opening delimiters insert the matching closing delimiter
			// and leave the cursor between the pair.
			{"open ( before text", "abc", term.Coordinates{X: 0}, '(', "()abc", term.Coordinates{X: 1}},
			{"open [ before text", "abc", term.Coordinates{X: 0}, '[', "[]abc", term.Coordinates{X: 1}},
			{"open { before text", "abc", term.Coordinates{X: 0}, '{', "{}abc", term.Coordinates{X: 1}},
			{`open " before text`, "abc", term.Coordinates{X: 0}, '"', `""abc`, term.Coordinates{X: 1}},
			{"open ' before text", "abc", term.Coordinates{X: 0}, '\'', "''abc", term.Coordinates{X: 1}},

			// Opening delimiter at common cursor positions.
			{"open ( in middle of word", "abc", term.Coordinates{X: 1}, '(', "a()bc", term.Coordinates{X: 2}},
			{"open { at end of line", "abc", term.Coordinates{X: 3}, '{', "abc{}", term.Coordinates{X: 4}},
			{"open [ on second line", "x\ny", term.Coordinates{X: 0, Y: 1}, '[', "x\n[]y", term.Coordinates{X: 1, Y: 1}},
			{"open ( before existing close", ")", term.Coordinates{X: 0}, '(', "())", term.Coordinates{X: 1}},
			{"open { before existing close", "}", term.Coordinates{X: 0}, '{', "{}}", term.Coordinates{X: 1}},

			// Overtype: closing delimiter on top of matching close advances cursor.
			{"overtype )", "()", term.Coordinates{X: 1}, ')', "()", term.Coordinates{X: 2}},
			{"overtype ]", "[]", term.Coordinates{X: 1}, ']', "[]", term.Coordinates{X: 2}},
			{"overtype }", "{}", term.Coordinates{X: 1}, '}', "{}", term.Coordinates{X: 2}},
			{`overtype "`, `""`, term.Coordinates{X: 1}, '"', `""`, term.Coordinates{X: 2}},
			{"overtype '", "''", term.Coordinates{X: 1}, '\'', "''", term.Coordinates{X: 2}},

			// Unmatched closing delimiter inserts normally.
			{"unmatched ) inserts", "abc", term.Coordinates{X: 1}, ')', "a)bc", term.Coordinates{X: 2}},
			{"unmatched ] inserts", "abc", term.Coordinates{X: 1}, ']', "a]bc", term.Coordinates{X: 2}},
			{"unmatched } inserts", "abc", term.Coordinates{X: 0}, '}', "}abc", term.Coordinates{X: 1}},

			// Quote runes are both open and close. With no matching quote
			// under the cursor we fall through to the open-pair insert.
			{`quote " in word inserts pair`, "abc", term.Coordinates{X: 1}, '"', `a""bc`, term.Coordinates{X: 2}},
			{"quote ' in word inserts pair", "abc", term.Coordinates{X: 1}, '\'', "a''bc", term.Coordinates{X: 2}},

			// Non-delimiter characters insert normally.
			{"non-delimiter inserts normally", "ac", term.Coordinates{X: 1}, 'b', "abc", term.Coordinates{X: 2}},

			// Closing delimiter at end of line where no cell exists at
			// the cursor falls through to a normal insert.
			{"close ) at end of line", "()", term.Coordinates{X: 2}, ')', "())", term.Coordinates{X: 3}},
		}

		for _, tc := range tsuite {
			t.Run(tc.name, func(t *testing.T) {
				c := setupCursorContent(t, 10, 5, tc.content, false)
				_, _ = c.MoveToScroll(tc.at)

				ok := c.InsertWithAutoPair(tc.ch, IndentRuneTab, 0)

				assert.True(t, ok)
				assert.Equal(t, tc.wantBuffer, c.buffer().String())
				assert.Equal(t, tc.wantCursor, c.CursorAtScroll())
			})
		}
	})

	t.Run("BackspaceAutoPair", func(t *testing.T) {
		tsuite := []struct {
			name       string
			content    string
			at         term.Coordinates
			wantBuffer string
			wantCursor term.Coordinates
			wantOK     bool
		}{
			// Backspace between a matching pair removes both delimiters.
			{"between ( )", "()", term.Coordinates{X: 1}, "", term.Coordinates{}, true},
			{"between [ ]", "[]", term.Coordinates{X: 1}, "", term.Coordinates{}, true},
			{"between { }", "{}", term.Coordinates{X: 1}, "", term.Coordinates{}, true},
			{`between " "`, `""`, term.Coordinates{X: 1}, "", term.Coordinates{}, true},
			{"between ' '", "''", term.Coordinates{X: 1}, "", term.Coordinates{}, true},

			// Pair removal preserves surrounding content.
			{"pair with trailing text", "()abc", term.Coordinates{X: 1}, "abc", term.Coordinates{}, true},
			{"pair with leading text", "abc()", term.Coordinates{X: 4}, "abc", term.Coordinates{X: 3}, true},
			{"pair sandwiched", "x()y", term.Coordinates{X: 2}, "xy", term.Coordinates{X: 1}, true},

			// Mismatched pair falls back to plain backspace.
			{"mismatched ( ]", "(]", term.Coordinates{X: 1}, "]", term.Coordinates{}, true},
			{"mismatched { )", "{)", term.Coordinates{X: 1}, ")", term.Coordinates{}, true},

			// Plain backspace fallback for non-delimiters.
			{"middle of word", "abc", term.Coordinates{X: 2}, "ac", term.Coordinates{X: 1}, true},
			{"end of word", "abc", term.Coordinates{X: 3}, "ab", term.Coordinates{X: 2}, true},

			// Backspace from start of a line joins with the line above.
			{"start of second line", "ab\ncd", term.Coordinates{X: 0, Y: 1}, "abcd", term.Coordinates{X: 2, Y: 0}, true},

			// Backspace at the very start of the buffer is a no-op.
			{"start of buffer non-empty", "abc", term.Coordinates{}, "abc", term.Coordinates{}, false},
			{"empty buffer", "", term.Coordinates{}, "", term.Coordinates{}, false},

			// A close-then-open sequence is not a pair: fall back to plain
			// backspace which removes the previous close.
			{"close-open is not a pair", ")(", term.Coordinates{X: 1}, "(", term.Coordinates{}, true},
		}

		for _, tc := range tsuite {
			t.Run(tc.name, func(t *testing.T) {
				c := setupCursorContent(t, 10, 5, tc.content, false)
				_, _ = c.MoveToScroll(tc.at)

				ok := c.BackspaceAutoPair()

				assert.Equal(t, tc.wantOK, ok)
				assert.Equal(t, tc.wantBuffer, c.buffer().String())
				assert.Equal(t, tc.wantCursor, c.CursorAtScroll())
			})
		}
	})

	t.Run("InsertAutoPairNewline", func(t *testing.T) {
		tsuite := []struct {
			name       string
			content    string
			at         term.Coordinates
			wantBuffer string
			wantCursor term.Coordinates
		}{
			// Brace pair: open the body on its own line so the closing
			// brace stays alone.
			{"empty {} alone", "{}", term.Coordinates{X: 1}, "{\n\n}", term.Coordinates{X: 0, Y: 1}},
			{"{} with trailing text", "{}a", term.Coordinates{X: 1}, "{\n\n}\na", term.Coordinates{X: 0, Y: 1}},
			{"{} with leading and trailing text", "x{}y", term.Coordinates{X: 2}, "x{\n\n}\ny", term.Coordinates{X: 0, Y: 1}},
			{"{} at end of line, more lines below", "x{}\nz", term.Coordinates{X: 2}, "x{\n\n}\nz", term.Coordinates{X: 0, Y: 1}},

			// Non-brace pairs split a normal newline; the closing
			// delimiter is not relocated to its own line.
			{"() splits as plain newline", "()", term.Coordinates{X: 1}, "(\n)", term.Coordinates{X: 0, Y: 1}},
			{"[] splits as plain newline", "[]", term.Coordinates{X: 1}, "[\n]", term.Coordinates{X: 0, Y: 1}},
			{`"" splits as plain newline`, `""`, term.Coordinates{X: 1}, "\"\n\"", term.Coordinates{X: 0, Y: 1}},

			// No pair under the cursor: behaves like a normal newline.
			{"plain text mid-word", "abc", term.Coordinates{X: 1}, "a\nbc", term.Coordinates{X: 0, Y: 1}},
			{"plain text at end of line", "abc", term.Coordinates{X: 3}, "abc\n", term.Coordinates{X: 0, Y: 1}},

			// Cursor at start of a line is not between a pair, even if
			// the previous line ends with `{`. The plain newline is
			// inserted, leaving the closing `}` on the next line.
			{"start of line after {", "{\n}", term.Coordinates{X: 0, Y: 1}, "{\n\n}", term.Coordinates{X: 0, Y: 2}},
		}

		for _, tc := range tsuite {
			t.Run(tc.name, func(t *testing.T) {
				c := setupCursorContent(t, 10, 5, tc.content, false)
				_, _ = c.MoveToScroll(tc.at)

				ok := c.InsertAutoPairNewline(IndentRuneTab, 0)

				assert.True(t, ok)
				assert.Equal(t, tc.wantBuffer, c.buffer().String())
				assert.Equal(t, tc.wantCursor, c.CursorAtScroll())
			})
		}
	})
}

var (
	abcAttr      = term.Attributes{Attrs: term.AttrUnderline, Bg: term.ColorBlack}
	abcLocations = []textapi.Location{
		{
			From:    term.Coordinates{Y: 1},
			To:      term.Coordinates{Y: 1, X: 1},
			Attr:    abcAttr,
			Message: "blabla",
		},
		{
			From: term.Coordinates{Y: 2},
			To:   term.Coordinates{Y: 2, X: 1},
			Attr: abcAttr,
		},
		{
			From: term.Coordinates{Y: 3},
			To:   term.Coordinates{Y: 3, X: 1},
			Attr: abcAttr,
		},
	}
)

func TestCursorMoveLocationList(t *testing.T) {
	t.Run("MoveToPrevLocation should return false and do nothing if location list is nil", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "", false)
		assert.False(t, c.MoveToPrevLocation(locID))
	})

	t.Run("MoveToNextLocation should return false and do nothing if location list is nil", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "", false)
		assert.False(t, c.MoveToNextLocation(locID))
	})

	t.Run("MoveToPrevLocation should return false and do nothing if already at start of location list", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "", false)
		locations := []textapi.Location{{}}
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, LocationSlice(locations)))
		assert.False(t, c.MoveToPrevLocation(locID))
	})

	t.Run("MoveToNextLocation should return false and do nothing if already at end of location list", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "", false)
		locations := []textapi.Location{{}}
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, LocationSlice(locations)))
		assert.False(t, c.MoveToNextLocation(locID))
	})

	t.Run("MoveToPrevLocation should return true and move cursor to earlier location", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, " X", false)
		require.True(t, c.MoveRight())

		locations := []textapi.Location{{}}
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, LocationSlice(locations)))
		assert.True(t, c.MoveToNextLocation(locID))

		pos := c.Coordinates()
		assert.Equal(t, term.Coordinates{}, pos)
	})

	t.Run("MoveToNextLocation should wrap around to first location", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "\na\nb\nc \n", false)
		require.True(t, c.MoveLastLine())

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, LocationSlice(abcLocations)))
		assert.True(t, c.MoveToNextLocation(locID))

		pos := c.Coordinates()
		assert.Equal(t, term.Coordinates{Y: 1}, pos)
	})

	t.Run("MoveToPrevLocation should wrap around to last location", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "\na\nb\nc\n", false)

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, LocationSlice(abcLocations)))
		assert.True(t, c.MoveToPrevLocation(locID))

		pos := c.Coordinates()
		assert.Equal(t, term.Coordinates{Y: 3}, pos)
	})

	t.Run("MoveToNextLocation should wrap around to first location (special case)", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, " X", false)
		require.True(t, c.MoveRight())

		locations := []textapi.Location{{}, {From: term.Coordinates{X: 1}}}
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, LocationSlice(locations)))
		assert.True(t, c.MoveToNextLocation(locID))

		pos := c.Coordinates()
		assert.Equal(t, term.Coordinates{}, pos)
	})

	t.Run("MoveToPrevLocation should wrap around to last location (special case)", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, " X", false)

		locations := []textapi.Location{{}, {From: term.Coordinates{X: 1}}}
		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, LocationSlice(locations)))
		assert.True(t, c.MoveToPrevLocation(locID))

		pos := c.Coordinates()
		assert.Equal(t, term.Coordinates{X: 1}, pos)
	})

	t.Run("MoveToNextLocation should go to next location after cursor", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "\na\nb\nc \n", false)
		require.True(t, c.MoveDown())
		require.True(t, c.MoveDown())

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, LocationSlice(abcLocations)))
		assert.True(t, c.MoveToNextLocation(locID))

		pos := c.Coordinates()
		assert.Equal(t, term.Coordinates{Y: 3}, pos)
	})

	t.Run("MoveToPrevLocation should go to prev location before location", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "\na\nb\nc\n", false)
		require.True(t, c.MoveDown())
		require.True(t, c.MoveDown())

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, LocationSlice(abcLocations)))
		assert.True(t, c.MoveToPrevLocation(locID))

		pos := c.Coordinates()
		assert.Equal(t, term.Coordinates{Y: 1}, pos)
	})

	t.Run("MoveToNextLocation should go to next location if current location is hidden", func(t *testing.T) {
		c := setupCursorContent(t, 10, 10, "\na\nb\nc \n", false)
		require.True(t, c.Select())
		require.True(t, c.MoveDown())
		require.True(t, c.MoveDown())
		require.True(t, c.HideSelection())
		require.False(t, c.MoveFirstLine())

		assert.Equal(t, term.Coordinates{Y: 0}, c.Coordinates())

		assert.Nil(t, c.SetLocationList(textapi.LocationPriorityInfo, locID, LocationSlice(abcLocations)))
		assert.True(t, c.MoveToNextLocation(locID))

		assert.Equal(t, term.Coordinates{Y: 1}, c.Coordinates())
	})
}

func TestCursorMoveToScroll(t *testing.T) {
	for _, wrap := range []bool{true, false} {
		t.Run(fmt.Sprintf("wrap=%v", wrap), func(t *testing.T) {
			t.Run("moves cursor to position within curr width,height", func(t *testing.T) {
				e := setupCursor(t, 10, 10, wrap)
				e.MoveToScroll(term.Coordinates{X: 1, Y: 3})
				pos := e.CursorAtScroll()
				assert.Equal(t, term.Coordinates{Y: 3, X: 1}, pos)
			})
			t.Run("moves cursor to position past curr height", func(t *testing.T) {
				e := setupCursor(t, 5, 5, wrap)
				e.MoveToScroll(term.Coordinates{X: 0, Y: 6})
				pos := e.CursorAtScroll()
				assert.Equal(t, term.Coordinates{Y: 6, X: 0}, pos)
			})
			t.Run("moves cursor to position past curr width", func(t *testing.T) {
				e := setupCursor(t, 5, 5, wrap)
				e.MoveToScroll(term.Coordinates{X: 7, Y: 2})
				pos := e.CursorAtScroll()
				assert.Equal(t, term.Coordinates{Y: 2, X: 7}, pos)
			})

		})
	}
}

func TestCursorSelectIndentationLevel(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		cursorAt  term.Coordinates
		tabspaces int
		want      bool
		wantSel   string
		wantStart term.Coordinates
		wantEnd   term.Coordinates
	}{
		{
			name:    "empty buffer returns false",
			content: "",
			want:    false,
		},
		{
			name:     "blank-only buffer returns false",
			content:  "\n\t\n  ",
			cursorAt: term.Coordinates{Y: 1},
			want:     false,
		},
		{
			name:      "all indent zero selects entire buffer",
			content:   "a\nb\nc",
			cursorAt:  term.Coordinates{Y: 1},
			want:      true,
			wantSel:   "a\nb\nc\n",
			wantStart: term.Coordinates{Y: 0},
			wantEnd:   term.Coordinates{Y: 2, X: 1},
		},
		{
			name:      "indented block bounded by less-indented lines",
			content:   "a\n\tb\n\tc\nd",
			cursorAt:  term.Coordinates{Y: 1},
			want:      true,
			wantSel:   "\tb\n\tc\n",
			wantStart: term.Coordinates{Y: 1},
			wantEnd:   term.Coordinates{Y: 2, X: 2},
		},
		{
			name:      "deeper nested lines stay inside selected indent level",
			content:   "root\n  if\n    child\n  sibling\nroot2",
			cursorAt:  term.Coordinates{Y: 1},
			want:      true,
			wantSel:   "  if\n    child\n  sibling\n",
			wantStart: term.Coordinates{Y: 1},
			wantEnd:   term.Coordinates{Y: 3, X: 9},
		},
		{
			name:      "mixed tabs and spaces compare by visual indentation",
			content:   "root\n\ttabbed\n    spaced\n  shallow",
			cursorAt:  term.Coordinates{Y: 1},
			tabspaces: 4,
			want:      true,
			wantSel:   "\ttabbed\n    spaced\n",
			wantStart: term.Coordinates{Y: 1},
			wantEnd:   term.Coordinates{Y: 2, X: 10},
		},
		{
			name:      "explicit tabspaces controls visual indentation comparison",
			content:   "root\n\ttabbed\n  two spaces\n shallow",
			cursorAt:  term.Coordinates{Y: 1},
			tabspaces: 2,
			want:      true,
			wantSel:   "\ttabbed\n  two spaces\n",
			wantStart: term.Coordinates{Y: 1},
			wantEnd:   term.Coordinates{Y: 2, X: 12},
		},
		{
			name:      "blank line between same-indent lines is included",
			content:   "\ta\n\n\tb\nc",
			cursorAt:  term.Coordinates{Y: 0},
			want:      true,
			wantSel:   "\ta\n\n\tb\n",
			wantStart: term.Coordinates{Y: 0},
			wantEnd:   term.Coordinates{Y: 2, X: 2},
		},
		{
			name:      "cursor on blank line uses nearest non-blank line",
			content:   "a\n\n\tb\n\tc\nd",
			cursorAt:  term.Coordinates{Y: 1},
			want:      true,
			wantSel:   "\tb\n\tc\n",
			wantStart: term.Coordinates{Y: 2},
			wantEnd:   term.Coordinates{Y: 3, X: 2},
		},
		{
			name:      "cursor on trailing blank line falls back to previous non-blank line",
			content:   "a\n\tb\n\tc\n\n",
			cursorAt:  term.Coordinates{Y: 3},
			want:      true,
			wantSel:   "\tb\n\tc\n",
			wantStart: term.Coordinates{Y: 1},
			wantEnd:   term.Coordinates{Y: 2, X: 2},
		},
		{
			name:      "leading blank lines beyond block are trimmed",
			content:   "\n\n\tb\n\tc\nd",
			cursorAt:  term.Coordinates{Y: 2},
			want:      true,
			wantSel:   "\tb\n\tc\n",
			wantStart: term.Coordinates{Y: 2},
			wantEnd:   term.Coordinates{Y: 3, X: 2},
		},
		{
			name:      "trailing blank lines beyond block are trimmed",
			content:   "a\n\tb\n\n",
			cursorAt:  term.Coordinates{Y: 1},
			want:      true,
			wantSel:   "\tb\n",
			wantStart: term.Coordinates{Y: 1},
			wantEnd:   term.Coordinates{Y: 1, X: 2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 80, 10, tc.content, false)
			if tc.content != "" {
				c.MoveToScroll(tc.cursorAt)
				require.Equal(t, tc.cursorAt, c.cursorAtScroll())
			}
			tabspaces := tc.tabspaces
			if tabspaces <= 0 {
				tabspaces = 4
			}
			got := c.SelectIndentationLevel(tabspaces)
			assert.Equal(t, tc.want, got)
			if !tc.want {
				return
			}
			mode, ok := c.SelectionMode()
			require.True(t, ok)
			assert.Equal(t, LineSelection, mode)
			assert.Equal(t, tc.wantSel, c.Selection())
			assert.Equal(t, tc.wantStart, c.selection.scrollFrom)
			assert.Equal(t, tc.wantEnd, c.selection.scrollTo)
		})
	}
}

func TestCursorWrap(t *testing.T) {
	t.Run("takes wraps into consideration", func(t *testing.T) {
		e := setupCursor(t, 10, 10, true)
		_, ok := e.MoveToScroll(term.Coordinates{X: 76, Y: 2})
		require.True(t, ok)
		pos := e.Coordinates()
		assert.Equal(t, term.Coordinates{Y: 9, X: 6}, pos)
	})
	t.Run("handles cursor.X past last row's column", func(t *testing.T) {
		e := setupCursor(t, 10, 10, true)
		_, ok := e.MoveToScroll(term.Coordinates{X: 77, Y: 2})
		require.True(t, ok)
		pos := e.Coordinates()
		assert.Equal(t, term.Coordinates{Y: 9, X: 7}, pos)
	})
	t.Run("MoveRight moves cursor past the end of line of a wrapped line", func(t *testing.T) {
		e := setupCursor(t, 10, 10, true)
		e.MoveToScroll(term.Coordinates{X: 15, Y: 3})

		require.True(t, e.MoveRight())
		cursor := e.CursorAtScroll()
		assert.Equal(t, term.Coordinates{Y: 3, X: 16}, cursor)
	})
}

func TestCursorWrapParagraph(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name           string
		content        string
		cursor         term.Coordinates
		ruler          int
		commentSpec    CommentSpec
		wantChanged    bool
		wantContent    string
		wantCursor     *term.Coordinates
		wrapAgainNoOp  bool
		wantSecondText string
	}

	coord := func(pos term.Coordinates) *term.Coordinates { return &pos }

	tests := []testCase{
		{
			name:        "wraps plain paragraph to ruler",
			content:     "alpha beta gamma delta\nepsilon zeta eta theta\n\nnext paragraph",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       20,
			wantChanged: true,
			wantContent: "alpha beta gamma\ndelta epsilon zeta\neta theta\n\nnext paragraph",
			wantCursor:  coord(term.Coordinates{Y: 0, X: 0}),
		},
		{
			name:        "preserves indentation while wrapping",
			content:     "    alpha beta gamma delta epsilon zeta\n    eta theta iota\n",
			cursor:      term.Coordinates{Y: 0, X: 4},
			ruler:       20,
			wantChanged: true,
			wantContent: "    alpha beta gamma\n    delta epsilon\n    zeta eta theta\n    iota\n",
		},
		{
			name:        "reflows line comments preserving comment leader",
			content:     "// alpha beta gamma delta\n// epsilon zeta eta theta\n\nfn main() {}",
			cursor:      term.Coordinates{Y: 0, X: 3},
			ruler:       20,
			commentSpec: CommentSpec{Line: []string{"//"}},
			wantChanged: true,
			wantContent: "// alpha beta gamma\n// delta epsilon\n// zeta eta theta\n\nfn main() {}",
		},
		{
			name:        "returns false when paragraph already fits",
			content:     "short line\n\nnext",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       90,
			wantChanged: false,
			wantContent: "short line\n\nnext",
			wantCursor:  coord(term.Coordinates{Y: 0, X: 0}),
		},
		{
			name:        "blank line chooses next paragraph",
			content:     "\n\nalpha beta gamma delta epsilon\nnext line words\n\ntrailing",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       14,
			wantChanged: true,
			wantContent: "\n\nalpha beta\ngamma delta\nepsilon next\nline words\n\ntrailing",
		},
		{
			name:        "blank line chooses previous paragraph when no next paragraph exists",
			content:     "alpha beta gamma delta\n\n\n",
			cursor:      term.Coordinates{Y: 2, X: 0},
			ruler:       12,
			wantChanged: true,
			wantContent: "alpha beta\ngamma delta\n\n\n",
		},
		{
			name:        "empty buffer returns false",
			content:     "",
			cursor:      term.Coordinates{},
			ruler:       10,
			wantChanged: false,
			wantContent: "",
		},
		{
			name:        "single very long word stays on one line but still joins paragraph",
			content:     "supercalifragilisticexpialidocious\nword",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       8,
			wantChanged: false,
			wantContent: "supercalifragilisticexpialidocious\nword",
		},
		{
			name:           "already wrapped second invocation is no op",
			content:        "alpha beta gamma delta epsilon zeta eta theta",
			cursor:         term.Coordinates{Y: 0, X: 0},
			ruler:          12,
			wantChanged:    true,
			wantContent:    "alpha beta\ngamma delta\nepsilon zeta\neta theta",
			wrapAgainNoOp:  true,
			wantSecondText: "alpha beta\ngamma delta\nepsilon zeta\neta theta",
		},
		{
			name:        "preserves multiple blank line separators around paragraph",
			content:     "before\n\n\nalpha beta gamma delta\nepsilon zeta\n\n\nafter",
			cursor:      term.Coordinates{Y: 3, X: 0},
			ruler:       12,
			wantChanged: true,
			wantContent: "before\n\n\nalpha beta\ngamma delta\nepsilon zeta\n\n\nafter",
		},
		{
			name:        "handles very long line followed by very short line",
			content:     "alpha beta gamma delta epsilon zeta eta theta iota kappa\nx\n\nend",
			cursor:      term.Coordinates{Y: 0, X: 10},
			ruler:       16,
			wantChanged: true,
			wantContent: "alpha beta gamma\ndelta epsilon\nzeta eta theta\niota kappa x\n\nend",
		},
		{
			name:        "preserves tab indentation",
			content:     "\talpha beta gamma delta epsilon\n\teta theta\n",
			cursor:      term.Coordinates{Y: 0, X: 1},
			ruler:       14,
			wantChanged: true,
			// Tab in indent expands to tabspaces (default 4) so the
			// available width is 14-4=10. "gamma delta" = 11 cells
			// exceeds it; only "alpha beta" and "eta theta" fit pairs.
			wantContent: "\talpha beta\n\tgamma\n\tdelta\n\tepsilon\n\teta theta\n",
		},
		{
			name:        "line comments without trailing space leader are preserved",
			content:     "//alpha beta gamma delta\n//epsilon zeta eta theta",
			cursor:      term.Coordinates{Y: 0, X: 2},
			ruler:       16,
			commentSpec: CommentSpec{Line: []string{"//"}},
			wantChanged: true,
			wantContent: "//alpha beta\n//gamma delta\n//epsilon zeta\n//eta theta",
		},
		{
			name:        "mixed commented and uncommented lines do not preserve comment leader",
			content:     "// alpha beta gamma\ndelta epsilon zeta",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       16,
			commentSpec: CommentSpec{Line: []string{"//"}},
			wantChanged: true,
			wantContent: "// alpha beta\ngamma delta\nepsilon zeta",
		},
		{
			name:        "cursor inside second line of paragraph still wraps paragraph",
			content:     "alpha beta gamma\ndelta epsilon zeta eta\n\nend",
			cursor:      term.Coordinates{Y: 1, X: 4},
			ruler:       15,
			wantChanged: true,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\neta\n\nend",
		},
		{
			name:        "null character inside paragraph is preserved as content",
			content:     "alpha \x00 beta gamma delta\nepsilon zeta",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       12,
			wantChanged: true,
			wantContent: "alpha \x00 beta\ngamma delta\nepsilon zeta",
		},
		{
			name:        "ruler zero is no op",
			content:     "alpha beta gamma delta",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       0,
			wantChanged: false,
			wantContent: "alpha beta gamma delta",
		},
		{
			name:        "ruler smaller than indent still wraps one word per line",
			content:     "        alpha beta gamma",
			cursor:      term.Coordinates{Y: 0, X: 8},
			ruler:       2,
			wantChanged: true,
			wantContent: "        alpha\n        beta\n        gamma",
		},
		{
			name:        "comment ruler smaller than indent and leader still wraps one word per line",
			content:     "    // alpha beta gamma",
			cursor:      term.Coordinates{Y: 0, X: 4},
			ruler:       3,
			commentSpec: CommentSpec{Line: []string{"//"}},
			wantChanged: true,
			wantContent: "    // alpha\n    // beta\n    // gamma",
		},
		{
			name:        "single line paragraph that needs wrap",
			content:     "alpha beta gamma delta epsilon",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       11,
			wantChanged: true,
			wantContent: "alpha beta\ngamma delta\nepsilon",
		},
		{
			name:        "whitespace normalization collapses repeated spaces within paragraph",
			content:     "alpha   beta\tgamma\ndelta",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       20,
			wantChanged: true,
			wantContent: "alpha beta gamma\ndelta",
		},
		{
			name:        "cursor on blank line between very short and very long paragraphs chooses next paragraph",
			content:     "x\n\nalpha beta gamma delta epsilon zeta eta theta\n",
			cursor:      term.Coordinates{Y: 1, X: 0},
			ruler:       14,
			wantChanged: true,
			wantContent: "x\n\nalpha beta\ngamma delta\nepsilon zeta\neta theta\n",
		},
		{
			name:        "comment paragraph with indentation preserved on all wrapped lines",
			content:     "    // alpha beta gamma delta epsilon\n    // zeta eta theta\n",
			cursor:      term.Coordinates{Y: 0, X: 4},
			ruler:       18,
			commentSpec: CommentSpec{Line: []string{"//"}},
			wantChanged: true,
			wantContent: "    // alpha beta\n    // gamma delta\n    // epsilon\n    // zeta eta\n    // theta\n",
		},
		{
			name:        "block comment syntax configured but no shared line leader means literal wrap",
			content:     "/* alpha beta gamma delta */\nepsilon zeta eta",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       18,
			commentSpec: CommentSpec{Block: []CommentBlock{{Start: "/*", End: "*/"}}},
			wantChanged: true,
			wantContent: "/* alpha beta\ngamma delta */\nepsilon zeta eta",
		},
		{
			name:        "crlf style content is normalized by buffer and still wraps safely",
			content:     "alpha beta gamma delta\r\nepsilon zeta eta\r\n\r\nend",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       16,
			wantChanged: true,
			wantContent: "alpha beta gamma\ndelta epsilon\nzeta eta end",
		},
		{
			name:           "unicode wide characters remain stable under double wrap",
			content:        "你好 世界 再见 朋友 测试 内容",
			cursor:         term.Coordinates{Y: 0, X: 0},
			ruler:          8,
			wantChanged:    true,
			wantContent:    "你好\n世界\n再见\n朋友\n测试\n内容",
			wrapAgainNoOp:  true,
			wantSecondText: "你好\n世界\n再见\n朋友\n测试\n内容",
		},
		{
			name:        "hidden lines outside paragraph do not affect wrap result",
			content:     "head1\nhead2\n\nalpha beta gamma delta epsilon zeta\neta theta\n\ntail1\ntail2",
			cursor:      term.Coordinates{Y: 3, X: 0},
			ruler:       14,
			wantChanged: true,
			wantContent: "head1\nhead2\n\nalpha beta\ngamma delta\nepsilon zeta\neta theta\n\ntail1\ntail2",
		},
		{
			name:        "paragraph with punctuation preserves tokens and wraps only on spaces",
			content:     "alpha,beta gamma.delta epsilon-zeta eta/theta",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       18,
			wantChanged: true,
			wantContent: "alpha,beta\ngamma.delta\nepsilon-zeta\neta/theta",
		},
		{
			name:        "single nonblank line containing only whitespace around null remains no op when already minimal",
			content:     "\x00",
			cursor:      term.Coordinates{Y: 0, X: 0},
			ruler:       5,
			wantChanged: false,
			wantContent: "\x00",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 80, 20, tc.content, false)
			if tc.commentSpec.HasLine() || tc.commentSpec.HasBlock() {
				attachCommentTestView(c, tc.commentSpec)
			}
			if tc.content != "" {
				setCursorAtOrFail(t, c, tc.cursor)
			}
			if tc.name == "hidden lines outside paragraph do not affect wrap result" {
				require.True(t, c.scroll.MarkHidden(0, 1))
				require.True(t, c.scroll.MarkHidden(6, 7))
			}

			changed := c.WrapParagraph(tc.ruler)
			assert.Equal(t, tc.wantChanged, changed)
			assert.Equal(t, tc.wantContent, c.buffer().String())
			if tc.wantCursor != nil {
				assert.Equal(t, *tc.wantCursor, c.CursorAtScroll())
			}
			if tc.wrapAgainNoOp {
				assert.False(t, c.WrapParagraph(tc.ruler))
				assert.Equal(t, tc.wantSecondText, c.buffer().String())
			}
		})
	}
}

func FuzzCursorWrapParagraph(f *testing.F) {
	seeds := []struct {
		content string
		ruler   int
		line    string
	}{
		{"alpha beta gamma delta\nepsilon zeta\n\nend", 12, ""},
		{"// alpha beta gamma delta\n// epsilon zeta eta\n", 16, "//"},
		{"\talpha beta gamma\n\tdelta epsilon\n", 10, ""},
		{"alpha \x00 beta gamma\ndelta", 12, ""},
		{"你好 世界 再见 朋友", 8, ""},
	}
	for _, seed := range seeds {
		f.Add(seed.content, seed.ruler, seed.line)
	}

	f.Fuzz(func(t *testing.T, content string, ruler int, line string) {
		if ruler < -10 || ruler > 200 {
			return
		}
		c := setupCursorContent(t, 80, 20, content, false)
		if line != "" {
			attachCommentTestView(c, CommentSpec{Line: []string{line}})
		}
		if c.rows() > 0 {
			_, _ = c.MoveToScroll(term.Coordinates{})
		}

		_ = c.WrapParagraph(ruler)
		afterFirst := c.buffer().String()
		_ = c.WrapParagraph(ruler)
		afterSecond := c.buffer().String()

		assert.Equal(t, afterFirst, afterSecond)
	})
}

func TestCursorSelectWordInsertWord(t *testing.T) {
	for _, wrap := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrap:%v", wrap), func(t *testing.T) {
			e := setupCursorContent(t, 5, 5, sampleSnippet+"\n", wrap)
			require.True(t, e.MoveDown())
			require.True(t, e.MoveDown())
			require.True(t, e.MoveRightStartWord())
			require.True(t, e.Select())
			require.True(t, e.MoveRightStartWord())
			require.True(t, e.DeleteSelection())
			l := len(e.buffer().String())
			e.Insert('h')
			e.Insert('e')
			e.Insert('l')
			e.Insert('l')
			e.Insert('o')
			assert.Equal(t, l+5, len(e.buffer().String()))
		})
	}
}

func TestCursorInsertLimitedWidth(t *testing.T) {
	for _, wrap := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrap:%v", wrap), func(t *testing.T) {
			e := setupCursorContent(t, 3, 1, "", wrap)
			e.Insert('h')
			e.Insert('e')
			e.Insert('l')
			e.Insert('l')
			e.Insert('o')
			e.Insert(' ')
			e.Insert('w')
			e.Insert('o')
			e.Insert('r')
			e.Insert('l')
			e.Insert('d')
			assert.Equal(t, "hello world", e.buffer().String())
		})
	}
}

func TestCursorReplaceAllWithNewline(t *testing.T) {
	e := setupCursorContent(t, 5, 5, sampleSnippet+"\n", false)
	require.True(t, e.SelectLine())
	require.True(t, e.MoveLastLine())
	require.True(t, e.DeleteSelection())
	e.Insert('h')
	e.Insert('e')
	e.Insert('l')
	e.Insert('l')
	e.Insert('o')
	e.Insert('\n')
	e.Insert('w')
	e.Insert('o')
	e.Insert('r')
	e.Insert('l')
	e.Insert('d')
	assert.Equal(t, "hello\nworld", e.buffer().String())
}

func cwdURI(t *testing.T) workspaceapi.URI {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %s", err)
	}
	uri, err := workspaceapi.CurrentUserHostURI(wd)
	if err != nil {
		t.Fatalf("parse working directory as URI %s: %s", wd, err)
	}
	return uri
}

func TestFileCursorIntegration(t *testing.T) {
	tsuite := []struct {
		description string
		lastEOL     bool
		test        func(t *testing.T, c *Cursor)
	}{
		{"does not move beyond line before last EOL", true, func(t *testing.T, cursor *Cursor) {
			assert.True(t, cursor.MoveLastLine())
			assert.Equal(t, term.Coordinates{Y: 31}, cursor.CursorAtScroll())
		}},
		{"does not move beyond last line", false, func(t *testing.T, cursor *Cursor) {
			assert.True(t, cursor.MoveLastLine())
			assert.Equal(t, term.Coordinates{Y: 31}, cursor.CursorAtScroll())
		}},
		{"is able to insert at last line + 1", false, func(t *testing.T, cursor *Cursor) {
			assert.True(t, cursor.MoveLastLine())
			cursor.InsertLineBelow(IndentRuneTab, 0)
			assert.Equal(t, term.Coordinates{Y: 32}, cursor.CursorAtScroll())
			assert.Equal(t, sampleSnippet+"\n", cursor.buffer().String())
		}},
		{"is able to insert at last EOL", true, func(t *testing.T, cursor *Cursor) {
			assert.True(t, cursor.MoveLastLine())
			cursor.InsertLineBelow(IndentRuneTab, 0)
			assert.Equal(t, term.Coordinates{Y: 32}, cursor.CursorAtScroll())
			assert.Equal(t, sampleSnippet+"\n", cursor.buffer().String())
		}},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.description, func(t *testing.T) {
			b := cell.NewBuffer()
			file, err := os.CreateTemp("", "frctl_file_test")
			require.NoError(t, err)

			_, err = file.Write([]byte(sampleSnippet))
			require.NoError(t, err)

			if tcase.lastEOL {
				_, err = file.Write([]byte{'\n'})
				require.NoError(t, err)
			}

			defer file.Close()
			defer os.Remove(file.Name())

			scroll := component.NewScroll(b)
			scroll.Resize(10, 10)
			cursor := NewCursor(scroll, nil)

			uri, err := workspaceapi.CurrentUserHostURI(file.Name())
			require.NoError(t, err)

			swapDir, err := workspaceapi.CurrentUserHostURI("/tmp")
			require.NoError(t, err)

			// installs unix reader
			m := workspace.NewManager(config.NopConfig(), inlineSchedule)
			require.NoError(t, err)
			err = m.RegisterScheme(workspace.FileScheme, workspace.NewFileScheme)
			require.NoError(t, err)
			workspace, err := m.AddWorkspace(context.Background(), cwdURI(t))
			require.NoError(t, err)
			_, err = workspace.Load(uri, b, swapDir, false)
			require.NoError(t, err)

			tcase.test(t, cursor)
		})
	}
}

func TestCursorPaste(t *testing.T) {
	/*
	   z
	   x
	*/
	/*
		a
		b
		c
		d

	*/
	const initialContent = "a\nb\nc\nd"
	tsuite := []struct {
		initialPosition     term.Coordinates
		expectedEndPosition term.Coordinates
		txt                 string
		mode                SelectMode
		after               bool
		expectedBuffer      string
	}{
		{term.Coordinates{}, term.Coordinates{X: 1},
			"z", NoSelection, false, "za\nb\nc\nd"},
		{term.Coordinates{}, term.Coordinates{},
			"z\n", NoSelection, false, "z\na\nb\nc\nd"},
		{term.Coordinates{}, term.Coordinates{Y: 1},
			"z\n", NoSelection, true, "a\nz\nb\nc\nd"},
		{term.Coordinates{X: 1, Y: 3}, term.Coordinates{Y: 3},
			"z\n", NoSelection, false, "a\nb\nc\nz\nd"},
		{term.Coordinates{X: 1, Y: 3}, term.Coordinates{X: 1, Y: 3},
			"z\n", NoSelection, true, "a\nb\nc\nd\nz\n"},
		{term.Coordinates{}, term.Coordinates{X: 1},
			"z", StandardSelection, false, "za\nb\nc\nd"},
		{term.Coordinates{}, term.Coordinates{X: 2},
			"z", StandardSelection, true, "az\nb\nc\nd"},
		{term.Coordinates{}, term.Coordinates{X: 1, Y: 1},
			"z\nx", StandardSelection, false, "z\nxa\nb\nc\nd"},
		{term.Coordinates{}, term.Coordinates{X: 1, Y: 1},
			"z\nx", StandardSelection, true, "az\nx\nb\nc\nd"},
		{term.Coordinates{Y: 3}, term.Coordinates{Y: 3, X: 1},
			"z", StandardSelection, false, "a\nb\nc\nzd"},
		{term.Coordinates{Y: 3}, term.Coordinates{Y: 3, X: 2},
			"z", StandardSelection, true, "a\nb\nc\ndz"},
		{term.Coordinates{Y: 3}, term.Coordinates{Y: 4, X: 1},
			"z\nx", StandardSelection, false, "a\nb\nc\nz\nxd"},
		{term.Coordinates{Y: 3}, term.Coordinates{Y: 4, X: 1},
			"z\nx", StandardSelection, true, "a\nb\nc\ndz\nx"},
		{term.Coordinates{X: 1}, term.Coordinates{},
			"z", LineSelection, false, "za\nb\nc\nd"},
		{term.Coordinates{X: 1}, term.Coordinates{Y: 1},
			"z", LineSelection, true, "a\nzb\nc\nd"},
		{term.Coordinates{X: 1}, term.Coordinates{},
			"z\nx", LineSelection, false, "z\nxa\nb\nc\nd"},
		{term.Coordinates{X: 1}, term.Coordinates{Y: 1},
			"z\nx", LineSelection, true, "a\nz\nxb\nc\nd"},
		{term.Coordinates{X: 1, Y: 3}, term.Coordinates{Y: 3},
			"z", LineSelection, false, "a\nb\nc\nzd"},
		{term.Coordinates{X: 1, Y: 3}, term.Coordinates{Y: 3, X: 1},
			"z", LineSelection, true, "a\nb\nc\nd\nz"},
		{term.Coordinates{X: 1, Y: 3}, term.Coordinates{Y: 3},
			"z\nx", LineSelection, false, "a\nb\nc\nz\nxd"},
		{term.Coordinates{X: 1, Y: 3}, term.Coordinates{Y: 3, X: 1},
			"z\nx", LineSelection, true, "a\nb\nc\nd\nz\nx"},
		// block selection is like standard but with InsertBlock so we test that instead
	}

	for _, wrap := range []bool{false, true} {
		for i, tcase := range tsuite {
			t.Run(fmt.Sprintf("wrap: %v, %d", wrap, i), func(t *testing.T) {
				c := setupCursorContent(t, 5, 5, initialContent, wrap)
				c.MoveToScroll(tcase.initialPosition)
				c.Paste(tcase.txt, tcase.mode, tcase.after)
				assert.Equal(t, tcase.expectedBuffer, c.buffer().String())
				assert.Equal(t, tcase.expectedEndPosition, c.Coordinates())
			})
		}
	}
}

func TestPasteWithSelection(t *testing.T) {
	name := "no selection"
	for _, wrap := range []bool{false, true} {
		if wrap {
			name += " (wrap)"
		}
		t.Run(name, func(t *testing.T) {
			c := setupCursorContent(t, 4, 10, "abcdefgh\nijklmnopqrstuvxyz", true)
			c.MoveRightColumns(4)
			c.Paste("123", NoSelection, false)
			assert.Equal(t, "abcd123efgh\nijklmnopqrstuvxyz", c.buffer().String())
		})
	}

	name = "standard selection"
	for _, wrap := range []bool{false, true} {
		if wrap {
			name += " (wrap)"
		}
		t.Run(name, func(t *testing.T) {
			c := setupCursorContent(t, 4, 10, "abcdefgh\nijklmnopqrstuvxyz", wrap)
			c.MoveRightColumns(3)
			c.Select()
			c.MoveRightColumns(3)
			require.Equal(t, "defg", c.Selection())
			require.Equal(t, c.selection.mode, StandardSelection)
			c.Paste("123", StandardSelection, false)
			assert.Equal(t, "abc123h\nijklmnopqrstuvxyz", c.buffer().String())
		})
	}

	name = "line selection"
	for _, wrap := range []bool{false, true} {
		if wrap {
			name += " (wrap)"
		}
		t.Run(name, func(t *testing.T) {
			c := setupCursorContent(t, 4, 10, "abcdefgh\nijklmnopqrstuvxyz", false)
			c.MoveRightColumns(5)
			c.SelectLine()

			// NOTE: If PR #127 goes through this text will break because it will
			// select physical line instead of logical. So we would get "efgh".
			require.Equal(t, "abcdefgh\n", c.Selection())
			require.Equal(t, c.selection.mode, LineSelection)
			c.Paste("123", LineSelection, false)
			assert.Equal(t, "123\nijklmnopqrstuvxyz", c.buffer().String())
		})
	}

	name = "block selection"
	for _, wrap := range []bool{false, true} {
		if wrap {
			name += " (wrap)"
		}
		t.Run(name, func(t *testing.T) {
			c := setupCursorContent(t, 4, 10, "012\nabcd\n34567\n", false)
			c.MoveRight()
			c.SelectBlock()
			c.MoveDown()
			c.MoveDown()
			c.MoveRight()
			require.Equal(t, "12\nbc\n45", c.Selection())
			require.Equal(t, c.selection.mode, BlockSelection)
			c.Paste("XXX\nYYY\nZZZ", BlockSelection, false)
			assert.Equal(t, "0XXX\naYYYd\n3ZZZ67\n", c.buffer().String())
		})
	}
}

func TestCursorInsertBlock(t *testing.T) {
	const initialContent = "a\nb\nc"
	tsuite := []struct {
		initialPosition term.Coordinates
		txt             string
		expected        string
	}{
		{term.Coordinates{}, "a\nb\nc", "aa\nbb\ncc"},
		{term.Coordinates{Y: 1}, "a\nb\nc", "a\nab\nbc\nc"},
		{term.Coordinates{Y: 1, X: 1}, "a\nb\nc", "a\nba\ncb\n c"},
		{term.Coordinates{Y: 2}, "a\nb\nc", "a\nb\nac\nb\nc"},
		{term.Coordinates{Y: 2, X: 1}, "a\nb\nc", "a\nb\nca\n b\n c"},
	}

	for i, tcase := range tsuite {
		t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
			c := setupCursorContent(t, 5, 5, initialContent, false)
			c.MoveToScroll(tcase.initialPosition)
			c.InsertBlock(tcase.txt)
			assert.Equal(t, tcase.expected, c.buffer().String())
		})
	}
}

func TestCursorReplace(t *testing.T) {
	t.Run("non wrap", func(t *testing.T) {
		const initialContent = "a\nb\nc"
		c := setupCursorContent(t, 5, 5, initialContent, false)
		assert.Equal(t, term.Coordinates{X: 0, Y: 0}, c.cursor)

		c.Replace('X')
		assert.Equal(t, "X\nb\nc", c.buffer().String())
		assert.Equal(t, term.Coordinates{X: 0, Y: 0}, c.cursor)

		c.MoveDown()

		assert.Equal(t, term.Coordinates{X: 0, Y: 1}, c.cursor)
		c.Replace('Y')
		assert.Equal(t, "X\nY\nc", c.buffer().String())
		assert.Equal(t, term.Coordinates{X: 0, Y: 1}, c.cursor)

		c.MoveDown()
		assert.Equal(t, term.Coordinates{X: 0, Y: 2}, c.cursor)

		c.Replace('Z')
		assert.Equal(t, "X\nY\nZ", c.buffer().String())
		assert.Equal(t, term.Coordinates{X: 0, Y: 2}, c.cursor)

		c.Replace('A')
		assert.Equal(t, "X\nY\nA", c.buffer().String())
		assert.Equal(t, term.Coordinates{X: 0, Y: 2}, c.cursor)

		c.Replace('B')
		assert.Equal(t, "X\nY\nB", c.buffer().String())
		assert.Equal(t, term.Coordinates{X: 0, Y: 2}, c.cursor)
	})

	t.Run("wrap", func(t *testing.T) {
		const initialContent = "aaaaaaaaaaa\nb\nc"
		c := setupCursorContent(t, 5, 5, initialContent, true)

		for range 9 {
			c.MoveRight()
		}
		assert.Equal(t, term.Coordinates{X: 4, Y: 1}, c.cursor)

		c.Replace('X')
		assert.Equal(t, term.Coordinates{X: 4, Y: 1}, c.cursor)
		assert.Equal(t, "aaaaaaaaaXa\nb\nc", c.buffer().String())

		c.Replace('Y')
		assert.Equal(t, term.Coordinates{X: 4, Y: 1}, c.cursor)
		assert.Equal(t, "aaaaaaaaaYa\nb\nc", c.buffer().String())
	})
}

func TestCursorMoveRightWrap(t *testing.T) {
	t.Run("no seek horizontal", func(t *testing.T) {
		const initialContent = "1\n2"
		c := setupCursorContent(t, 3, 3, initialContent, false)

		cell, ok := c.Cell()
		require.True(t, ok)
		assert.Equal(t, '1', cell.Ch)

		assert.True(t, c.MoveRightWrap())
		assert.Equal(t, term.Coordinates{X: 1}, c.CursorAtScroll())
		assert.True(t, c.MoveRightWrap())
		assert.Equal(t, term.Coordinates{Y: 1, X: 0}, c.CursorAtScroll())
		assert.True(t, c.MoveRightWrap())
		assert.False(t, c.MoveRightWrap())
		assert.Equal(t, term.Coordinates{Y: 1, X: 1}, c.CursorAtScroll())
	})

	t.Run("with seek horizontal", func(t *testing.T) {
		const initialContent = "11\n22"
		c := setupCursorContent(t, 1, 1, initialContent, false)

		cell, ok := c.Cell()
		require.True(t, ok)
		assert.Equal(t, '1', cell.Ch)

		assert.True(t, c.MoveRightWrap())
		assert.Equal(t, term.Coordinates{X: 1}, c.CursorAtScroll())
		assert.True(t, c.MoveRightWrap())
		assert.True(t, c.MoveRightWrap())
		assert.Equal(t, term.Coordinates{Y: 1, X: 0}, c.CursorAtScroll())
		assert.True(t, c.MoveRightWrap())
		assert.True(t, c.MoveRightWrap())
		assert.False(t, c.MoveRightWrap())
		assert.Equal(t, term.Coordinates{Y: 1, X: 2}, c.CursorAtScroll())
	})
}

func TestCursorMoveLeftWrap(t *testing.T) {
	t.Run("no seek horizontal", func(t *testing.T) {
		const initialContent = "1\n2"
		c := setupCursorContent(t, 3, 3, initialContent, false)

		c.MoveToScroll(term.Coordinates{Y: 2, X: 2})

		assert.Equal(t, term.Coordinates{Y: 2, X: 2}, c.CursorAtScroll())
		assert.True(t, c.MoveLeftWrap())
		assert.True(t, c.MoveLeftWrap())
		assert.True(t, c.MoveLeftWrap())
		assert.Equal(t, term.Coordinates{Y: 1, X: 1}, c.CursorAtScroll())
		assert.True(t, c.MoveLeftWrap())
		assert.True(t, c.MoveLeftWrap())
		assert.True(t, c.MoveLeftWrap())
		assert.False(t, c.MoveLeftWrap())
		assert.Equal(t, term.Coordinates{}, c.CursorAtScroll())
	})

	t.Run("with seek horizontal", func(t *testing.T) {
		const initialContent = "11\n22"
		c := setupCursorContent(t, 1, 1, initialContent, false)

		c.MoveToScroll(term.Coordinates{Y: 2, X: 0})

		assert.True(t, c.MoveLeftWrap())
		assert.Equal(t, term.Coordinates{Y: 1, X: 2}, c.CursorAtScroll())
		assert.True(t, c.MoveLeftWrap())
		assert.True(t, c.MoveLeftWrap())
		assert.Equal(t, term.Coordinates{Y: 1, X: 0}, c.CursorAtScroll())
		assert.True(t, c.MoveLeftWrap())
		assert.True(t, c.MoveLeftWrap())
		assert.Equal(t, term.Coordinates{X: 1}, c.CursorAtScroll())
		assert.True(t, c.MoveLeftWrap())
		assert.False(t, c.MoveLeftWrap())
		assert.Equal(t, term.Coordinates{}, c.CursorAtScroll())
	})
}

type testScrollSubscriber struct {
	hide    int
	visible int
	seek    int
}

func (s *testScrollSubscriber) OnWillSeek(from term.Coordinates) {
	/* no op */
}

func (s *testScrollSubscriber) OnWillHide(start, end int) {
}

func (s *testScrollSubscriber) OnWillVisible(start int) {
}

func (s *testScrollSubscriber) OnDidHide(start, end int) {
	s.hide++
}

func (s *testScrollSubscriber) OnDidVisible(start int) {
	s.visible++
}

func (s *testScrollSubscriber) OnDidSeek(from, to term.Coordinates) {
	s.seek++
}

var _ = (foldsService)(testFoldsService{})

type testFoldsService struct {
	folds iterator.Iterator[term.Range]
	view  cell.View
}

func (f testFoldsService) Rows() int {
	return f.view.Rows()
}

func (f testFoldsService) Columns(row int) int {
	return f.view.Columns(row)
}

func (f testFoldsService) Cell(at term.Coordinates) (term.Cell, bool) {
	return f.view.Cell(at)
}

func (f testFoldsService) RawCells() [][]term.Cell {
	return f.view.RawCells()
}

func (f testFoldsService) String() string {
	return f.view.String()
}

func (f testFoldsService) FoldsFrom(pos term.Coordinates) (
	iterator.Iterator[term.Range], bool,
) {
	folds, ok := f.Folds()
	if !ok {
		return nil, false
	}
	return iterator.Filter(folds, func(rng term.Range) bool {
		return (rng.End.Y > pos.Y || (rng.End.Y == pos.Y && rng.End.X > pos.X))
	}), true
}

func (f testFoldsService) Folds() (iterator.Iterator[term.Range], bool) {
	if f.folds != nil {
		return f.folds, true
	}
	return iterator.FromSlice([]term.Range{
		{Start: term.Coordinates{Y: 1, X: 0}, End: term.Coordinates{Y: 4}},
		{Start: term.Coordinates{Y: 2, X: 3}, End: term.Coordinates{Y: 3, X: 15}},
		{Start: term.Coordinates{Y: 7, X: 0}, End: term.Coordinates{Y: 31, X: 0}},
		{Start: term.Coordinates{Y: 12, X: 3}, End: term.Coordinates{Y: 28, X: 3}},
		{Start: term.Coordinates{Y: 19, X: 6}, End: term.Coordinates{Y: 27, X: 6}},
		{Start: term.Coordinates{Y: 22, X: 6}, End: term.Coordinates{Y: 26, X: 9}},
		{Start: term.Coordinates{Y: 29, X: 3}, End: term.Coordinates{Y: 30, X: 3}},
	}), true
}

func newBenchmarkScroll(width, height int, fortunes int) (scroll *component.Scroll) {
	scroll = component.NewScroll(cell.NewBuffer())
	for range fortunes {
		_, _ = scroll.Buffer().ReadFrom(strings.NewReader(sampleSnippet))
	}
	scroll.Resize(width, height)
	return
}

func benchmarkCursorMoveLeft(b *testing.B, width, height int, wrap bool) {
	s := newBenchmarkScroll(width, height, 10000)
	s.Wrap = wrap
	cursor := NewCursor(s, nil)
	cursor.MoveLastLine()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok := cursor.MoveLeftWrap()
		if !ok {
			cursor.MoveLastLine()
		}
	}
}

func benchmarkCursorMoveMatchingRune(b *testing.B, width, height int, wrap bool) {
	s := newBenchmarkScroll(width, height, 1)
	s.Wrap = wrap
	cursor := NewCursor(s, nil)
	cursor.MoveToMark(CursorMark{scroll: term.Coordinates{Y: 7}})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cursor.MoveToMatchingRune()
	}
}

func BenchmarkCursorMoveMatchingRuneLargeWindowNoWrap(b *testing.B) {
	benchmarkCursorMoveMatchingRune(b, 1000, 1000, false)
}

func BenchmarkCursorMoveMatchingRuneLargeWindowWrap(b *testing.B) {
	benchmarkCursorMoveMatchingRune(b, 1000, 1000, true)
}

func BenchmarkCursorMoveMatchingRuneSmallWindowWrap(b *testing.B) {
	benchmarkCursorMoveMatchingRune(b, 10, 10, true)
}

func BenchmarkCursorMoveMatchingRuneSmallWindowNoWrap(b *testing.B) {
	benchmarkCursorMoveMatchingRune(b, 10, 10, false)
}

func BenchmarkCursorMoveLeftNoWrap(b *testing.B) {
	benchmarkCursorMoveLeft(b, 1000, 1000, false)
}

func BenchmarkCursorMoveLeftWrap(b *testing.B) {
	benchmarkCursorMoveLeft(b, 10, 10, true)
}

func benchmarkCursorMoveToRune(b *testing.B, width, height int, wrap bool) {
	s := newBenchmarkScroll(width, height, 100)
	s.Wrap = wrap
	cursor := NewCursor(s, nil)
	cursor.MoveToMark(CursorMark{scroll: term.Coordinates{Y: 7}})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cursor.MoveRightStartWordGroup()
		cursor.MoveLeftStartWordGroup()
		cursor.MoveRightEndWordGroup()
		cursor.MoveLeftEndWordGroup()
		cursor.MoveRightStartWord()
		cursor.MoveLeftStartWord()
		cursor.MoveRightEndWord()
		cursor.MoveLeftEndWord()
	}
}

func BenchmarkCursorMoveToRuneLargeWrap(b *testing.B) {
	benchmarkCursorMoveToRune(b, 10000, 10000, true)
}
func BenchmarkCursorMoveToRuneLargeNoWrap(b *testing.B) {
	benchmarkCursorMoveToRune(b, 10000, 10000, false)
}
func BenchmarkCursorMoveToRuneSmallWrap(b *testing.B) {
	benchmarkCursorMoveToRune(b, 10, 10, true)
}
func BenchmarkCursorMoveToRuneSmallNoWrap(b *testing.B) {
	benchmarkCursorMoveToRune(b, 10, 10, false)
}

const locID = "errors"
const sampleSnippet = `
/*
 * Ch@ek if the current buffer should be added to or removed from the list of
 * diff buffers.
 */
	void
diff_buf_adjust(win_T *win)
{
	win_T	*wp;
	int		i;

	if (!win->w_p_diff)
	{
	/* When there is no window showing a diff for this buffer, remove
	 * it from the diffs. */
	FOR_ALL_WINDOWS(wp)
		if (wp->w_buffer == win->w_buffer && wp->w_p_diff)
		break;
	if (wp == NULL)
	{
		i = diff_buf_idx(win->w_buffer);
		if (i != DB_COUNT)
		{X
		curtab->tp_diffbuf[i] = NULL;
		curtab->tp_diff_invalid = TRUE;
		diff_redraw(TRUE);
		}
	}
	}
	else
	diff_buf_add(win->w_buffer);
} /* { */ `

func setupCursorContent(t *testing.T, width, height int, cont string, wrap bool) (e *Cursor) {
	scroll := component.NewScroll(cell.NewBuffer())
	e = NewCursor(scroll, nil)
	e.RightInclusiveSemantics = true
	scroll.Wrap = wrap
	scroll.Buffer().ReadFrom(strings.NewReader(cont))
	scroll.Resize(width, height)
	require.Equal(t, e.scroll.Buffer(), scroll.Buffer())
	if wrap {
		// needed for wraps to be accounted for
		e.scroll.RecalculateWraps()
	}
	return
}

func setupCursor(t *testing.T, width, height int, wrap bool) *Cursor {
	return setupCursorContent(t, width, height, sampleSnippet, wrap)
}

// TestCursorSelectionUnwrapsSoftWrappedRows covers the terminal's vi
// mode, where the cursor reads the emulator's grid: rows the emulator
// broke apart to fit the width carry cell.WrapMarker and must be
// yanked back as the single logical line they were printed as.
func TestCursorSelectionUnwrapsSoftWrappedRows(t *testing.T) {
	cases := []struct {
		desc string
		mode SelectMode
		want string
	}{
		{"standard selection", StandardSelection, "abcdefghij\nxy"},
		{"line selection", LineSelection, "abcdefghij\nxy\n"},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			c := setupCursorContent(t, 5, 5, "abcde\nfghij\nxy", false)
			rows := c.buffer().RawCells()
			rows[0][len(rows[0])-1].Bytes = cell.WrapMarker

			switch tc.mode {
			case StandardSelection:
				require.True(t, c.Select())
			case LineSelection:
				require.True(t, c.SelectLine())
			}
			require.True(t, c.MoveLastLine())
			require.True(t, c.MoveEndLine())

			assert.Equal(t, tc.want, c.Selection())
		})
	}
}

type testSelectionService struct {
	view    cell.View
	expand  map[term.Range]term.Range
	shrink  map[term.Range]term.Range
	shrinks []term.Range
}

type testCommentService struct {
	view  cell.View
	line  []string
	block []CommentBlock
}

func (s testSelectionService) Rows() int {
	return s.view.Rows()
}

func (s testSelectionService) Columns(row int) int {
	return s.view.Columns(row)
}

func (s testSelectionService) Cell(at term.Coordinates) (term.Cell, bool) {
	return s.view.Cell(at)
}

func (s testSelectionService) RawCells() [][]term.Cell {
	return s.view.RawCells()
}

func (s testSelectionService) String() string {
	return s.view.String()
}

func (s testSelectionService) SelectionExpand(rng term.Range) (term.Range, bool) {
	next, ok := s.expand[rng]
	return next, ok
}

func (s *testSelectionService) SelectionShrink(rng term.Range, caret term.Coordinates) (term.Range, bool) {
	s.shrinks = append(s.shrinks, rng)
	next, ok := s.shrink[rng]
	return next, ok
}

func (s testCommentService) Rows() int { return s.view.Rows() }

func (s testCommentService) Columns(row int) int { return s.view.Columns(row) }

func (s testCommentService) Cell(at term.Coordinates) (term.Cell, bool) { return s.view.Cell(at) }

func (s testCommentService) RawCells() [][]term.Cell { return s.view.RawCells() }

func (s testCommentService) String() string { return s.view.String() }

func (s testCommentService) CommentCoverage(rng term.Range) ([]term.Range, bool) {
	start, end := term.CoordinatesSort(rng.Start, rng.End)
	var ranges []term.Range
	for y := start.Y; y <= end.Y; y++ {
		line := term.CellsToString([][]term.Cell{s.view.RawCells()[y]})
		trimmed := strings.TrimLeft(line, " \t")
		indent := len(line) - len(trimmed)
		if trimmed == "" {
			continue
		}
		for _, prefix := range s.line {
			for _, candidate := range []string{prefix + " ", prefix} {
				if strings.HasPrefix(trimmed, candidate) {
					ranges = append(ranges, term.Range{
						Start: term.Coordinates{Y: y, X: indent},
						End:   term.Coordinates{Y: y, X: len(line)},
					})
					goto nextLine
				}
			}
		}
		for _, block := range s.block {
			openIdx := strings.Index(line, block.Start)
			closeIdx := strings.LastIndex(line, block.End)
			if openIdx >= 0 && closeIdx >= openIdx+len(block.Start) {
				ranges = append(ranges, term.Range{
					Start: term.Coordinates{Y: y, X: openIdx},
					End:   term.Coordinates{Y: y, X: closeIdx + len(block.End)},
				})
				goto nextLine
			}
		}
		return nil, false
	nextLine:
	}
	if len(ranges) == 0 {
		return nil, false
	}
	return ranges, true
}

func attachCommentTestView(c *Cursor, spec CommentSpec) {
	c.buffer().WithView(testCommentService{
		view:  c.buffer().View(),
		line:  spec.Line,
		block: spec.Block,
	})
	c.SetCommentSpec(spec)
}

func setCursorAtOrFail(t *testing.T, c *Cursor, at term.Coordinates) {
	t.Helper()
	_, _ = c.MoveToScroll(at)
	assert.Equal(t, at, c.CursorAtScroll())
}

func TestCursorToggleLineComment(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name        string
		content     string
		at          term.Coordinates
		selectFn    func(*Cursor)
		spec        CommentSpec
		wantHandled bool
		wantContent string
		wantCursor  term.Coordinates
	}

	goSpec := CommentSpec{Line: []string{"//"}}
	hashSpec := CommentSpec{Line: []string{"#"}}

	tests := []testCase{
		{
			name:        "current line comments with spacing after indentation",
			content:     "\ta\nb",
			at:          term.Coordinates{Y: 0, X: 1},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "\t// a\nb",
			wantCursor:  term.Coordinates{Y: 0, X: 4},
		},
		{
			name:        "current line uncomments exact prefix range",
			content:     "// a\nb",
			at:          term.Coordinates{Y: 0, X: 3},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "a\nb",
			wantCursor:  term.Coordinates{Y: 0, X: 1},
		},
		{
			name:    "selected two lines comments both lines",
			content: "a\nb",
			at:      term.Coordinates{Y: 0, X: 0},
			selectFn: func(c *Cursor) {
				require.True(t, c.Select())
				_, ok := c.MoveToScroll(term.Coordinates{Y: 1, X: 0})
				require.True(t, ok)
			},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "// a\n// b",
			wantCursor:  term.Coordinates{Y: 1, X: 3},
		},
		{
			name:    "selected two commented lines uncomments both lines",
			content: "// a\n// b",
			at:      term.Coordinates{Y: 0, X: 0},
			selectFn: func(c *Cursor) {
				require.True(t, c.Select())
				_, ok := c.MoveToScroll(term.Coordinates{Y: 1, X: 3})
				require.True(t, ok)
			},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "a\nb",
			wantCursor:  term.Coordinates{Y: 1, X: 1},
		},
		{
			name:        "alternate line prefix works",
			content:     "x",
			at:          term.Coordinates{},
			spec:        hashSpec,
			wantHandled: true,
			wantContent: "# x",
			wantCursor:  term.Coordinates{X: 2},
		},
		{
			name:        "missing comment spec returns false",
			content:     "x",
			at:          term.Coordinates{X: 1},
			spec:        CommentSpec{},
			wantHandled: false,
			wantContent: "x",
			wantCursor:  term.Coordinates{X: 1},
		},
		{
			name:        "blank line alone returns false (no content to comment)",
			content:     "   ",
			at:          term.Coordinates{Y: 0, X: 1},
			spec:        goSpec,
			wantHandled: false,
			wantContent: "   ",
			wantCursor:  term.Coordinates{Y: 0, X: 1},
		},
		{
			name:    "selection spanning blank and content comments only content",
			content: "a\n\nb",
			at:      term.Coordinates{Y: 0, X: 0},
			selectFn: func(c *Cursor) {
				require.True(t, c.Select())
				_, ok := c.MoveToScroll(term.Coordinates{Y: 2, X: 0})
				require.True(t, ok)
			},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "// a\n\n// b",
			wantCursor:  term.Coordinates{Y: 2, X: 3},
		},
		{
			name:        "partial prefix without space still uncomments",
			content:     "//a",
			at:          term.Coordinates{Y: 0, X: 2},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "a",
			wantCursor:  term.Coordinates{Y: 0, X: 0},
		},
		{
			name:        "indented comment uncomments and keeps indent",
			content:     "\t// a",
			at:          term.Coordinates{Y: 0, X: 4},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "\ta",
			wantCursor:  term.Coordinates{Y: 0, X: 2},
		},
		{
			name:        "indented content comments at indentation boundary",
			content:     "  x",
			at:          term.Coordinates{Y: 0, X: 2},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "  // x",
			wantCursor:  term.Coordinates{Y: 0, X: 5},
		},
		{
			name:        "cursor at start of line before indent (insert) stays before",
			content:     "  x",
			at:          term.Coordinates{Y: 0, X: 0},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "  // x",
			wantCursor:  term.Coordinates{Y: 0, X: 0},
		},
		{
			name:    "line-selection across two commented lines uncomments",
			content: "// a\n// b",
			at:      term.Coordinates{Y: 0, X: 0},
			selectFn: func(c *Cursor) {
				require.True(t, c.SelectLine())
				_, ok := c.MoveToScroll(term.Coordinates{Y: 1, X: 0})
				require.True(t, ok)
			},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "a\nb",
			wantCursor:  term.Coordinates{Y: 1, X: 0},
		},
		{
			name:    "mixed comment/non-comment selection inserts prefixes on all nonblank",
			content: "// a\nb",
			at:      term.Coordinates{Y: 0, X: 0},
			selectFn: func(c *Cursor) {
				require.True(t, c.Select())
				_, ok := c.MoveToScroll(term.Coordinates{Y: 1, X: 0})
				require.True(t, ok)
			},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "// // a\n// b",
			wantCursor:  term.Coordinates{Y: 1, X: 3},
		},
		{
			name:    "selection with blank middle line uncomments commented neighbors",
			content: "// a\n\n// b",
			at:      term.Coordinates{Y: 0, X: 0},
			selectFn: func(c *Cursor) {
				require.True(t, c.Select())
				_, ok := c.MoveToScroll(term.Coordinates{Y: 2, X: 3})
				require.True(t, ok)
			},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "a\n\nb",
			wantCursor:  term.Coordinates{Y: 2, X: 1},
		},
		{
			name:        "only-prefix line uncomments to empty",
			content:     "//",
			at:          term.Coordinates{Y: 0, X: 1},
			spec:        goSpec,
			wantHandled: true,
			wantContent: "",
			wantCursor:  term.Coordinates{Y: 0, X: 1},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 80, 10, tc.content, false)
			attachCommentTestView(c, tc.spec)
			setCursorAtOrFail(t, c, tc.at)
			if tc.selectFn != nil {
				tc.selectFn(c)
			}
			handled := c.ToggleLineComment()
			assert.Equal(t, tc.wantHandled, handled)
			assert.Equal(t, tc.wantContent, c.buffer().String())
			assert.Equal(t, tc.wantCursor, c.CursorAtScroll())
		})
	}
}

func TestCursorToggleBlockComment(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name        string
		content     string
		at          term.Coordinates
		selectFn    func(*Cursor)
		spec        CommentSpec
		wantHandled bool
		wantContent string
		wantCursor  term.Coordinates
	}

	spec := CommentSpec{Block: []CommentBlock{{Start: "/*", End: "*/"}}}

	tests := []testCase{
		{
			name:    "selection wraps with block delimiters",
			content: "alpha",
			at:      term.Coordinates{X: 0},
			selectFn: func(c *Cursor) {
				require.True(t, c.Select())
				_, ok := c.MoveToScroll(term.Coordinates{X: 4})
				require.True(t, ok)
			},
			spec:        spec,
			wantHandled: true,
			wantContent: "/*alpha*/",
			wantCursor:  term.Coordinates{X: 2},
		},
		{
			name:        "cursor inside existing block comment unwraps",
			content:     "/*alpha*/",
			at:          term.Coordinates{X: 2},
			spec:        spec,
			wantHandled: true,
			wantContent: "alpha",
			wantCursor:  term.Coordinates{},
		},
		{
			name:        "missing block spec returns false",
			content:     "alpha",
			at:          term.Coordinates{X: 1},
			spec:        CommentSpec{},
			wantHandled: false,
			wantContent: "alpha",
			wantCursor:  term.Coordinates{X: 1},
		},
		{
			name:        "cursor without selection and not in block returns false",
			content:     "alpha",
			at:          term.Coordinates{X: 1},
			spec:        spec,
			wantHandled: false,
			wantContent: "alpha",
			wantCursor:  term.Coordinates{X: 1},
		},
		{
			name:        "empty block spec returns false",
			content:     "alpha",
			at:          term.Coordinates{X: 1},
			spec:        CommentSpec{Block: []CommentBlock{}},
			wantHandled: false,
			wantContent: "alpha",
			wantCursor:  term.Coordinates{X: 1},
		},
		{
			name:        "cursor at start of existing block comment unwraps",
			content:     "/*x*/",
			at:          term.Coordinates{X: 0},
			spec:        spec,
			wantHandled: true,
			wantContent: "x",
			wantCursor:  term.Coordinates{},
		},
		{
			name:    "selection already wrapped unwraps",
			content: "/*ab*/",
			at:      term.Coordinates{X: 0},
			selectFn: func(c *Cursor) {
				require.True(t, c.Select())
				_, ok := c.MoveToScroll(term.Coordinates{X: 5})
				require.True(t, ok)
			},
			spec:        spec,
			wantHandled: true,
			wantContent: "ab",
			wantCursor:  term.Coordinates{},
		},
		{
			name:    "selection wraps multi-char content",
			content: "abc",
			at:      term.Coordinates{X: 0},
			selectFn: func(c *Cursor) {
				require.True(t, c.Select())
				_, ok := c.MoveToScroll(term.Coordinates{X: 2})
				require.True(t, ok)
			},
			spec:        spec,
			wantHandled: true,
			wantContent: "/*abc*/",
			wantCursor:  term.Coordinates{X: 2},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := setupCursorContent(t, 80, 10, tc.content, false)
			attachCommentTestView(c, tc.spec)
			setCursorAtOrFail(t, c, tc.at)
			if tc.selectFn != nil {
				tc.selectFn(c)
			}
			handled := c.ToggleBlockComment()
			assert.Equal(t, tc.wantHandled, handled)
			assert.Equal(t, tc.wantContent, c.buffer().String())
			assert.Equal(t, tc.wantCursor, c.CursorAtScroll())
		})
	}
}

func TestCursorToggleCommentCoverageGaps(t *testing.T) {
	t.Parallel()

	lineSpec := CommentSpec{Line: []string{"//"}}
	blockSpec := CommentSpec{Block: []CommentBlock{{Start: "/*", End: "*/"}}}

	t.Run("line toggle ignores blank lines when commenting", func(t *testing.T) {
		c := setupCursorContent(t, 80, 10, "a\n\n", false)
		attachCommentTestView(c, lineSpec)
		setCursorAtOrFail(t, c, term.Coordinates{Y: 0, X: 0})
		require.True(t, c.ToggleLineComment())
		assert.Equal(t, "// a\n\n", c.buffer().String())
	})

	t.Run("line toggle on empty buffer line returns false", func(t *testing.T) {
		c := setupCursorContent(t, 80, 10, "", false)
		attachCommentTestView(c, lineSpec)
		assert.False(t, c.ToggleLineComment())
	})

	t.Run("block toggle unwraps exact block range when cursor inside", func(t *testing.T) {
		c := setupCursorContent(t, 80, 10, "/*x*/", false)
		attachCommentTestView(c, blockSpec)
		setCursorAtOrFail(t, c, term.Coordinates{X: 1})
		require.True(t, c.ToggleBlockComment())
		assert.Equal(t, "x", c.buffer().String())
	})

	t.Run("block toggle without comment service cannot unwrap", func(t *testing.T) {
		c := setupCursorContent(t, 80, 10, "/*x*/", false)
		c.SetCommentSpec(blockSpec)
		setCursorAtOrFail(t, c, term.Coordinates{X: 1})
		assert.False(t, c.ToggleBlockComment())
		assert.Equal(t, "/*x*/", c.buffer().String())
	})

	t.Run("line toggle without comment service still comments", func(t *testing.T) {
		c := setupCursorContent(t, 80, 10, "x", false)
		c.SetCommentSpec(lineSpec)
		setCursorAtOrFail(t, c, term.Coordinates{X: 0})
		require.True(t, c.ToggleLineComment())
		assert.Equal(t, "// x", c.buffer().String())
	})
}

func TestCursorSelectionExpandShrink(t *testing.T) {
	content := "alpha beta gamma"
	c := setupCursorContent(t, 80, 10, content, false)

	svc := &testSelectionService{
		view: c.buffer().View(),
		expand: map[term.Range]term.Range{
			{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 6}}:  {Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}},
			{Start: term.Coordinates{X: 7}, End: term.Coordinates{X: 10}}: {Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}},
			{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}}: {Start: term.Coordinates{}, End: term.Coordinates{X: 10}},
			{Start: term.Coordinates{}, End: term.Coordinates{X: 10}}:     {Start: term.Coordinates{}, End: term.Coordinates{X: len(content)}},
		},
		shrink: map[term.Range]term.Range{
			{Start: term.Coordinates{}, End: term.Coordinates{X: len(content)}}: {Start: term.Coordinates{}, End: term.Coordinates{X: 10}},
			{Start: term.Coordinates{}, End: term.Coordinates{X: 10}}:           {Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}},
			{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}}:       {Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 6}},
		},
	}
	c.buffer().WithView(svc)

	_, ok := c.MoveToScroll(term.Coordinates{X: 6})
	require.True(t, ok)

	require.True(t, c.ExpandSelection(context.Background()))
	assert.Equal(t, "beta", c.Selection())

	require.True(t, c.ExpandSelection(context.Background()))
	assert.Equal(t, "alpha beta", c.Selection())

	require.True(t, c.ExpandSelection(context.Background()))
	assert.Equal(t, content, c.Selection())

	require.True(t, c.ShrinkSelection())
	assert.Equal(t, "alpha beta", c.Selection())

	require.True(t, c.ShrinkSelection())
	assert.Equal(t, "beta", c.Selection())

	require.True(t, c.ShrinkSelection())
	mode, ok := c.SelectionMode()
	assert.False(t, ok)
	assert.Equal(t, NoSelection, mode)
	assert.Equal(t, term.Coordinates{X: 6}, c.CursorAtScroll())

	assert.False(t, c.ShrinkSelection())
}

func TestCursorSelectionShrinkUsesCurrentSelection(t *testing.T) {
	content := "alpha beta gamma"
	c := setupCursorContent(t, 80, 10, content, false)

	svc := &testSelectionService{
		view: c.buffer().View(),
		expand: map[term.Range]term.Range{
			{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 6}}:  {Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}},
			{Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}}: {Start: term.Coordinates{}, End: term.Coordinates{X: 10}},
		},
		shrink: map[term.Range]term.Range{
			{Start: term.Coordinates{}, End: term.Coordinates{X: 10}}: {Start: term.Coordinates{X: 6}, End: term.Coordinates{X: 10}},
		},
	}
	c.buffer().WithView(svc)

	_, ok := c.MoveToScroll(term.Coordinates{X: 6})
	require.True(t, ok)
	require.True(t, c.ExpandSelection(context.Background()))
	require.True(t, c.ExpandSelection(context.Background()))

	require.True(t, c.ShrinkSelection())
	assert.Equal(t, "beta", c.Selection())
	assert.Equal(t, []term.Range{{Start: term.Coordinates{}, End: term.Coordinates{X: 10}}}, svc.shrinks)
}

func setupCursorForFolds(t *testing.T, width, height int, wg *sync.WaitGroup) (e *Cursor) {
	buf := cell.NewBuffer()
	fs := &testFoldsService{}
	fs.view = buf.WithView(fs)
	scroll := component.NewScroll(buf)
	e = NewCursor(scroll, func(fn func()) bool {
		defer wg.Done()
		fn()
		return true
	})
	e.RightInclusiveSemantics = true
	scroll.Buffer().ReadFrom(strings.NewReader(sampleSnippet))
	scroll.Resize(width, height)
	require.Equal(t, e.scroll.Buffer(), scroll.Buffer())
	return
}

type mockIndentService struct {
	cell.View
	returnIndentationAt int
}

func (m *mockIndentService) IndentationAt(line int) (int, bool) {
	return m.returnIndentationAt, true
}

// TestWrapSelectedParagraphBlockSelection exercises Cursor.WrapSelectedParagraph
// with a visual-block selection across many edge cases. Vim treats gq on a
// <C-v> selection as a linewise reflow over every line covered by the block,
// regardless of per-column block bounds, so all of these cases drive the
// machinery via SelectBlock and assert the linewise reflow result.
func TestWrapSelectedParagraphBlockSelection(t *testing.T) {
	t.Parallel()

	type tc struct {
		name        string
		content     string
		anchor      term.Coordinates // block anchor (SelectBlock invocation site)
		to          term.Coordinates // block extent (cursor position after SelectBlock)
		ruler       int
		width       int
		height      int
		wrap        bool
		commentSpec CommentSpec
		wantChanged bool
		wantContent string
	}

	tests := []tc{
		{
			name:        "single column block reflows full lines",
			content:     "alpha beta gamma delta epsilon zeta\nsecond line\nthird line\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 1, X: 3},
			ruler:       12,
			width:       80,
			height:      10,
			wantChanged: true,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nsecond line\nthird line\n",
		},
		{
			name:        "single line block degenerates to current line wrap",
			content:     "alpha beta gamma delta epsilon zeta\n",
			anchor:      term.Coordinates{Y: 0, X: 3},
			to:          term.Coordinates{Y: 0, X: 8},
			ruler:       12,
			width:       80,
			height:      10,
			wantChanged: true,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\n",
		},
		{
			name:        "backwards anchor (anchor below, cursor above) still reflows linewise",
			content:     "alpha beta gamma delta epsilon zeta\nsecond line\nthird line\n",
			anchor:      term.Coordinates{Y: 2, X: 5},
			to:          term.Coordinates{Y: 0, X: 1},
			ruler:       12,
			width:       80,
			height:      10,
			wantChanged: true,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nsecond line\nthird line\n",
		},
		{
			name:        "block past EOL on short rows still reflows full lines",
			content:     "alpha beta gamma delta epsilon zeta\nx\ny\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 2, X: 30},
			ruler:       12,
			width:       80,
			height:      10,
			wantChanged: true,
			// rows 1..2 are "x" and "y" — together they form a single
			// paragraph chunk after the long row that is reflowed to
			// "alpha beta\ngamma delta\nepsilon zeta", followed by
			// "x y" merged into a single short line.
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\nx y\n",
		},
		{
			name:        "block with both endpoints past EOL clamps and reflows",
			content:     "alpha beta gamma delta epsilon\nsh\n",
			anchor:      term.Coordinates{Y: 1, X: 100},
			to:          term.Coordinates{Y: 0, X: 100},
			ruler:       12,
			width:       80,
			height:      10,
			wantChanged: true,
			wantContent: "alpha beta\ngamma delta\nepsilon sh\n",
		},
		{
			name:        "tabs in indentation are preserved on each wrapped line",
			content:     "\talpha beta gamma delta epsilon zeta\n\tsecond line\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 1, X: 2},
			ruler:       14,
			width:       80,
			height:      10,
			wantChanged: true,
			// Tab in indent expands to tabspaces (default 4) so the
			// available width is 14-4=10; pairs that exceed 10 cells
			// are wrapped to their own lines.
			wantContent: "\talpha beta\n\tgamma\n\tdelta\n\tepsilon\n\tzeta\n\tsecond\n\tline\n",
		},
		{
			name:        "double-tab indent reduces available width by two tabstops",
			content:     "\t\talpha beta gamma delta epsilon\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 0, X: 4},
			ruler:       20,
			width:       80,
			height:      10,
			wantChanged: true,
			// Two tabs at default tabspaces=4 cost 8 cells; available
			// is 20-8=12, so pairs longer than 12 are wrapped.
			wantContent: "\t\talpha beta\n\t\tgamma delta\n\t\tepsilon\n",
		},
		{
			name:        "wide CJK characters are wrapped using display width",
			content:     "你好 世界 再见 朋友 测试 内容\nsecond\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 0, X: 2},
			ruler:       8,
			width:       80,
			height:      10,
			wantChanged: true,
			wantContent: "你好\n世界\n再见\n朋友\n测试\n内容\nsecond\n",
		},
		{
			name:        "embedded NUL bytes are treated as paragraph blanks but preserve trailing tokens",
			content:     "alpha\x00 beta gamma delta epsilon\nsecond\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 1, X: 3},
			ruler:       12,
			width:       80,
			height:      10,
			wantChanged: true,
			// strings.Fields treats NUL/space identically, so "alpha\x00"
			// remains a single token preceding the rest of the words.
			wantContent: "alpha\x00 beta\ngamma delta\nepsilon\nsecond\n",
		},
		{
			name:        "block over only-blank lines is a no-op",
			content:     "\n\n\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 2, X: 0},
			ruler:       12,
			width:       80,
			height:      10,
			wantChanged: false,
			wantContent: "\n\n\n",
		},
		{
			name:        "comment block reflows preserving line leader",
			content:     "// alpha beta gamma\n// delta epsilon zeta eta theta\nplain code line\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 2, X: 4},
			ruler:       14,
			width:       80,
			height:      10,
			commentSpec: CommentSpec{Line: []string{"//"}},
			wantChanged: true,
			// the block crosses a comment->code boundary; both
			// chunks are reflowed independently (Vim behavior).
			wantContent: "// alpha beta\n// gamma delta\n// epsilon\n// zeta eta\n// theta\nplain code\nline\n",
		},
		{
			name:        "block spanning blank line splits into two reflowed chunks",
			content:     "alpha beta gamma delta epsilon\n\nsecond paragraph keeps text\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 2, X: 5},
			ruler:       12,
			width:       80,
			height:      10,
			wantChanged: true,
			wantContent: "alpha beta\ngamma delta\nepsilon\n\nsecond\nparagraph\nkeeps text\n",
		},
		{
			name:        "block over comment, blank, then non-comment only reflows leading comment chunk",
			content:     "// alpha beta gamma delta\n\nplain text\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 2, X: 2},
			ruler:       14,
			width:       80,
			height:      10,
			commentSpec: CommentSpec{Line: []string{"//"}},
			wantChanged: true,
			wantContent: "// alpha beta\n// gamma delta\n\nplain text\n",
		},
		{
			name:        "block on file without trailing newline reflows last line",
			content:     "alpha beta gamma delta epsilon zeta",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 0, X: 5},
			ruler:       12,
			width:       80,
			height:      10,
			wantChanged: true,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta",
		},
		{
			name:        "block over content already inside ruler is a no-op",
			content:     "tiny\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 0, X: 2},
			ruler:       80,
			width:       80,
			height:      10,
			wantChanged: false,
			wantContent: "tiny\n",
		},
		{
			name:        "non-positive ruler is a no-op",
			content:     "alpha beta gamma\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 0, X: 2},
			ruler:       0,
			width:       80,
			height:      10,
			wantChanged: false,
			wantContent: "alpha beta gamma\n",
		},
		{
			name:        "negative ruler is a no-op",
			content:     "alpha beta gamma\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 0, X: 2},
			ruler:       -3,
			width:       80,
			height:      10,
			wantChanged: false,
			wantContent: "alpha beta gamma\n",
		},
		{
			name:        "wrap-mode-on viewport still reflows full source lines",
			content:     "alpha beta gamma delta epsilon zeta eta theta\nsecond\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 1, X: 3},
			ruler:       14,
			width:       10,
			height:      10,
			wrap:        true,
			wantChanged: true,
			wantContent: "alpha beta\ngamma delta\nepsilon zeta\neta theta\nsecond\n",
		},
		{
			name:        "ruler smaller than indent+leader still produces at least one word per line",
			content:     "    alpha beta gamma delta\n    next words here\n",
			anchor:      term.Coordinates{Y: 0, X: 4},
			to:          term.Coordinates{Y: 1, X: 4},
			ruler:       4,
			width:       80,
			height:      10,
			wantChanged: true,
			wantContent: "    alpha\n    beta\n    gamma\n    delta\n    next\n    words\n    here\n",
		},
		{
			name:        "block over leading blank then comment chunk skips blank, reflows comment",
			content:     "\n// alpha beta gamma delta epsilon\n// zeta eta theta\n",
			anchor:      term.Coordinates{Y: 0, X: 0},
			to:          term.Coordinates{Y: 2, X: 3},
			ruler:       16,
			width:       80,
			height:      10,
			commentSpec: CommentSpec{Line: []string{"//"}},
			wantChanged: true,
			wantContent: "\n// alpha beta\n// gamma delta\n// epsilon zeta\n// eta theta\n",
		},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			c := setupCursorContent(t, tcase.width, tcase.height, tcase.content, tcase.wrap)
			if tcase.commentSpec.HasLine() || len(tcase.commentSpec.Block) > 0 {
				attachCommentTestView(c, tcase.commentSpec)
			}

			// position the anchor; we don't assert ok because (0,0) at
			// startup is already the cursor and MoveToScroll's ok
			// reports movement, not success.
			_, _ = c.MoveToScroll(tcase.anchor)
			require.True(t, c.SelectBlock(),
				"SelectBlock at anchor %+v failed", tcase.anchor)
			_, _ = c.MoveToScroll(tcase.to)

			mode, ok := c.SelectionMode()
			require.True(t, ok)
			require.Equal(t, BlockSelection, mode)

			changed := c.WrapSelectedParagraph(tcase.ruler)
			assert.Equal(t, tcase.wantChanged, changed, "changed mismatch")
			assert.Equal(t, tcase.wantContent, c.scroll.Buffer().String(),
				"content mismatch")
		})
	}
}

// inlineSchedule is a synchronous workspace.ScheduleNextTick stub
// that runs fn on the calling goroutine. Test-only: production code
// must use the host event-loop scheduler so reload's buffer
// mutations do not run on a worker goroutine.
func inlineSchedule(fn func()) bool {
	fn()
	return true
}

// TestDeleteHorizontalSpace exercises DeleteHorizontalSpace (Emacs M-\), which
// removes the run of spaces and tabs surrounding point on the current line and
// leaves point where the run began.
func TestDeleteHorizontalSpace(t *testing.T) {
	cases := []struct {
		name    string
		content string
		at      term.Coordinates
		want    string
		wantAt  term.Coordinates
		wantOk  bool
	}{
		{
			name:    "spaces on both sides",
			content: "a   b",
			at:      term.Coordinates{X: 2},
			want:    "ab",
			wantAt:  term.Coordinates{X: 1},
			wantOk:  true,
		},
		{
			name:    "spaces only before point",
			content: "a   b",
			at:      term.Coordinates{X: 4},
			want:    "ab",
			wantAt:  term.Coordinates{X: 1},
			wantOk:  true,
		},
		{
			name:    "spaces only after point",
			content: "a   b",
			at:      term.Coordinates{X: 1},
			want:    "ab",
			wantAt:  term.Coordinates{X: 1},
			wantOk:  true,
		},
		{
			name:    "tabs are treated as blank",
			content: "a\t\tb",
			at:      term.Coordinates{X: 2},
			want:    "ab",
			wantAt:  term.Coordinates{X: 1},
			wantOk:  true,
		},
		{
			name:    "no surrounding blanks is a no-op",
			content: "ab",
			at:      term.Coordinates{X: 1},
			want:    "ab",
			wantAt:  term.Coordinates{X: 1},
			wantOk:  false,
		},
		{
			name:    "leading indentation from column zero",
			content: "   ab",
			at:      term.Coordinates{X: 0},
			want:    "ab",
			wantAt:  term.Coordinates{X: 0},
			wantOk:  true,
		},
		{
			name:    "trailing blanks at end of line",
			content: "ab   ",
			at:      term.Coordinates{X: 5},
			want:    "ab",
			wantAt:  term.Coordinates{X: 2},
			wantOk:  true,
		},
		{
			name:    "blanks on one line do not cross line boundaries",
			content: "a  \nb",
			at:      term.Coordinates{X: 1},
			want:    "a\nb",
			wantAt:  term.Coordinates{X: 1},
			wantOk:  true,
		},
	}

	for _, tcase := range cases {
		t.Run(tcase.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			scroll := component.NewScroll(buf)
			scroll.Resize(20, 10)
			c := NewCursor(scroll, nil)
			c.InsertString(tcase.content)
			_, _ = c.MoveToScroll(tcase.at)

			ok := c.DeleteHorizontalSpace()

			assert.Equal(t, tcase.wantOk, ok)
			assert.Equal(t, tcase.want, buf.String())
			assert.Equal(t, tcase.wantAt, c.CursorAtScroll())
		})
	}
}

func TestCursorTransposeChars(t *testing.T) {
	t.Run("swaps chars around caret and advances", func(t *testing.T) {
		c := setupCursorContent(t, 10, 5, "abcd", false)
		c.MoveToScroll(term.Coordinates{X: 2})

		assert.True(t, c.TransposeChars())
		assert.Equal(t, "acbd", c.buffer().String())
		assert.Equal(t, term.Coordinates{X: 3}, c.CursorAtScroll())
	})

	t.Run("transposes trailing chars at end of line", func(t *testing.T) {
		c := setupCursorContent(t, 10, 5, "abcd", false)
		c.MoveToScroll(term.Coordinates{X: 4})

		assert.True(t, c.TransposeChars())
		assert.Equal(t, "abdc", c.buffer().String())
		assert.Equal(t, term.Coordinates{X: 4}, c.CursorAtScroll())
	})

	t.Run("no-op at start of line", func(t *testing.T) {
		c := setupCursorContent(t, 10, 5, "abcd", false)
		c.MoveToScroll(term.Coordinates{})

		assert.False(t, c.TransposeChars())
		assert.Equal(t, "abcd", c.buffer().String())
		assert.Equal(t, term.Coordinates{}, c.CursorAtScroll())
	})

	t.Run("no-op on single-char line", func(t *testing.T) {
		c := setupCursorContent(t, 10, 5, "a", false)
		c.MoveToScroll(term.Coordinates{X: 1})

		assert.False(t, c.TransposeChars())
		assert.Equal(t, "a", c.buffer().String())
	})
}

func TestCursorSelectionUndoRedo(t *testing.T) {
	t.Run("undo restores prior selection then clears it", func(t *testing.T) {
		c := setupCursorContent(t, 20, 5, "hello world\nsecond line", false)

		require.True(t, c.SelectRange(term.Coordinates{}, term.Coordinates{X: 5}))
		from, to, ok := c.SelectionBounds()
		require.True(t, ok)
		assert.Equal(t, term.Coordinates{}, from)
		assert.Equal(t, term.Coordinates{X: 5}, to)

		require.True(t, c.SelectRange(term.Coordinates{Y: 1}, term.Coordinates{Y: 1, X: 6}))

		assert.True(t, c.UndoSelection())
		from, to, ok = c.SelectionBounds()
		require.True(t, ok)
		assert.Equal(t, term.Coordinates{}, from)
		assert.Equal(t, term.Coordinates{X: 5}, to)

		assert.True(t, c.UndoSelection())
		_, _, ok = c.SelectionBounds()
		assert.False(t, ok)
	})

	t.Run("redo reapplies an undone selection", func(t *testing.T) {
		c := setupCursorContent(t, 20, 5, "hello world", false)

		require.True(t, c.SelectRange(term.Coordinates{}, term.Coordinates{X: 5}))
		require.True(t, c.UndoSelection())
		_, _, ok := c.SelectionBounds()
		require.False(t, ok)

		assert.True(t, c.RedoSelection())
		from, to, ok := c.SelectionBounds()
		require.True(t, ok)
		assert.Equal(t, term.Coordinates{}, from)
		assert.Equal(t, term.Coordinates{X: 5}, to)
	})

	t.Run("undo returns false with no history", func(t *testing.T) {
		c := setupCursorContent(t, 20, 5, "hello", false)
		assert.False(t, c.UndoSelection())
		assert.False(t, c.RedoSelection())
	})

	t.Run("a new selection clears the redo stack", func(t *testing.T) {
		c := setupCursorContent(t, 20, 5, "hello world", false)

		require.True(t, c.SelectRange(term.Coordinates{}, term.Coordinates{X: 5}))
		require.True(t, c.UndoSelection())
		require.True(t, c.SelectRange(term.Coordinates{X: 6}, term.Coordinates{X: 11}))

		assert.False(t, c.RedoSelection())
	})
}

// TestCursorSelectionRange verifies the half-open range hosts paint
// matches the native highlight for both selection semantics.
func TestCursorSelectionRange(t *testing.T) {
	tests := []struct {
		name           string
		rightInclusive bool
		setup          func(t *testing.T, c *Cursor)
		wantFrom       term.Coordinates
		wantTo         term.Coordinates
	}{
		{
			name: "exclusive rightward drops the cursor cell",
			setup: func(t *testing.T, c *Cursor) {
				c.MoveToScroll(term.Coordinates{X: 2})
				require.True(t, c.Select())
				c.MoveToScroll(term.Coordinates{X: 5})
			},
			wantFrom: term.Coordinates{X: 2},
			wantTo:   term.Coordinates{X: 5},
		},
		{
			name: "exclusive leftward drops the anchor cell",
			setup: func(t *testing.T, c *Cursor) {
				c.MoveToScroll(term.Coordinates{X: 5})
				require.True(t, c.Select())
				c.MoveToScroll(term.Coordinates{X: 2})
			},
			wantFrom: term.Coordinates{X: 2},
			wantTo:   term.Coordinates{X: 5},
		},
		{
			name:           "right-inclusive rightward covers the cursor cell",
			rightInclusive: true,
			setup: func(t *testing.T, c *Cursor) {
				c.MoveToScroll(term.Coordinates{X: 2})
				require.True(t, c.Select())
				c.MoveToScroll(term.Coordinates{X: 5})
			},
			wantFrom: term.Coordinates{X: 2},
			wantTo:   term.Coordinates{X: 6},
		},
		{
			name:           "right-inclusive leftward covers the anchor cell",
			rightInclusive: true,
			setup: func(t *testing.T, c *Cursor) {
				c.MoveToScroll(term.Coordinates{X: 5})
				require.True(t, c.Select())
				c.MoveToScroll(term.Coordinates{X: 2})
			},
			wantFrom: term.Coordinates{X: 2},
			wantTo:   term.Coordinates{X: 6},
		},
		{
			name:           "explicit selection is used verbatim",
			rightInclusive: true,
			setup: func(t *testing.T, c *Cursor) {
				require.True(t, c.SelectRange(
					term.Coordinates{X: 2}, term.Coordinates{X: 5}))
			},
			wantFrom: term.Coordinates{X: 2},
			wantTo:   term.Coordinates{X: 5},
		},
		{
			name: "line selection expands to whole rows",
			setup: func(t *testing.T, c *Cursor) {
				c.MoveToScroll(term.Coordinates{X: 3})
				require.True(t, c.SelectLine())
				c.MoveToScroll(term.Coordinates{X: 3, Y: 1})
			},
			wantFrom: term.Coordinates{},
			wantTo:   term.Coordinates{X: 11, Y: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := setupCursorContent(t, 20, 5, "hello world\nsecond line", false)
			c.RightInclusiveSemantics = tt.rightInclusive
			tt.setup(t, c)

			from, to, ok := c.SelectionRange()
			require.True(t, ok)
			assert.Equal(t, tt.wantFrom, from)
			assert.Equal(t, tt.wantTo, to)
		})
	}

	t.Run("no selection", func(t *testing.T) {
		c := setupCursorContent(t, 20, 5, "hello", false)
		_, _, ok := c.SelectionRange()
		assert.False(t, ok)
	})
}
