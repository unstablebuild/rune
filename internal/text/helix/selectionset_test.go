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

package helix

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text"
)

func impl(hx *Helix) *helixHandlerImpl { return hx.handler.(*helixHandlerImpl) }

// sels returns the text under every range in document order.
func sels(hx *Helix) []string {
	h := impl(hx)
	return h.sel.fragments(h.buf())
}

func primaryIdx(hx *Helix) int { return impl(hx).sel.primary }

// altKeys is keys for a run of Alt-modified characters.
func altKeys(s string) []term.Event {
	evs := make([]term.Event, 0, len(s))
	for _, ch := range s {
		evs = append(evs, modKey(term.ModAlt, ch))
	}
	return evs
}

func cat(seqs ...[]term.Event) []term.Event {
	var out []term.Event
	for _, s := range seqs {
		out = append(out, s...)
	}
	return out
}

// selCase drives a key sequence and checks the resulting selection
// set: every fragment in document order, which range is primary, and
// where the caret (the primary's cursor) ended up.
type selCase struct {
	name        string
	content     string
	at          term.Coordinates
	evs         []term.Event
	wantSels    []string
	wantPrimary int
	wantAt      term.Coordinates
	// wantHandled pins the return of the last event when set.
	wantHandled *bool
}

func runSelCases(t *testing.T, cases []selCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at)
			var handled bool
			for _, ev := range tc.evs {
				_, handled = hx.Handle(ev)
			}
			assert.Equal(t, tc.wantSels, sels(hx), "fragments")
			assert.Equal(t, tc.wantPrimary, primaryIdx(hx), "primary index")
			assert.Equal(t, tc.wantAt, hx.CursorAtScroll(), "caret")
			if tc.wantHandled != nil {
				assert.Equal(t, *tc.wantHandled, handled, "handled")
			}
		})
	}
}

// TestCopySelectionOnLine pins copy_selection_on_line: C and A-C copy
// each range onto the next or previous line on the same columns, skip
// lines too short to hold it, and hand the primary to the last copy.
func TestCopySelectionOnLine(t *testing.T) {
	const grid = "abc\ndef\nghi"
	runSelCases(t, []selCase{
		{name: "C copies below", content: grid, at: xy(1, 0), evs: keys("C"),
			wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "C again copies every range", content: grid, at: xy(1, 0), evs: keys("CC"),
			wantSels: []string{"b", "e", "h"}, wantPrimary: 2, wantAt: xy(1, 2)},
		{name: "a count copies count times", content: grid, at: xy(1, 0), evs: keys("2C"),
			wantSels: []string{"b", "e", "h"}, wantPrimary: 2, wantAt: xy(1, 2)},
		{name: "C on the last line does nothing", content: grid, at: xy(1, 2), evs: keys("C"),
			wantSels: []string{"h"}, wantAt: xy(1, 2), wantHandled: new(false)},
		{name: "C skips a line too short for the column",
			content: "abcd\nab\nabcd", at: xy(3, 0), evs: keys("C"),
			wantSels: []string{"d", "d"}, wantPrimary: 1, wantAt: xy(3, 2)},
		{name: "C lands on the line ending of a line exactly as long",
			content: "ab\nab", at: xy(1, 0), evs: keys("lC"),
			wantSels: []string{"b", "b"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "A-C copies above", content: grid, at: xy(1, 2), evs: altKeys("C"),
			wantSels: []string{"e", "h"}, wantPrimary: 0, wantAt: xy(1, 1)},
		{name: "A-C twice fills the column upwards", content: grid, at: xy(1, 2), evs: altKeys("CC"),
			wantSels: []string{"b", "e", "h"}, wantPrimary: 0, wantAt: xy(1, 0)},
		{name: "A-C on the first line does nothing", content: grid, at: xy(1, 0), evs: altKeys("C"),
			wantSels: []string{"b"}, wantAt: xy(1, 0), wantHandled: new(false)},
		{name: "a multi-line range copies by its height",
			content: "ab\ncd\nef\ngh", evs: cat(keys("vj"), []term.Event{namedKey(term.KeyEsc)}, keys("C")),
			wantSels: []string{"ab\nc", "ef\ng"}, wantPrimary: 1, wantAt: xy(0, 3)},
		{name: "a wider range keeps its width", content: "abc\ndef", evs: keys("vlC"),
			wantSels: []string{"ab", "de"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "a backward range copies backward", content: "abc\ndef", at: xy(1, 0),
			evs:      cat(keys("vh"), []term.Event{namedKey(term.KeyEsc)}, keys("C")),
			wantSels: []string{"ab", "de"}, wantPrimary: 1, wantAt: xy(0, 1)},
	})
}

// TestSelectionSetCommands pins the commands that only rearrange the
// set: keep and remove primary, rotate, merge, split on newline.
func TestSelectionSetCommands(t *testing.T) {
	const grid = "abc\ndef\nghi"
	runSelCases(t, []selCase{
		{name: ", keeps the primary", content: grid, at: xy(1, 0), evs: keys("CC,"),
			wantSels: []string{"h"}, wantAt: xy(1, 2)},
		{name: ", with one range is a no-op", content: grid, evs: keys(","),
			wantSels: []string{"a"}, wantHandled: new(false)},
		{name: "A-, removes the primary", content: grid, at: xy(1, 0),
			evs:      cat(keys("CC"), altKeys(",")),
			wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "A-, on the first range moves the primary to the next", content: grid,
			at: xy(1, 0), evs: cat(keys("CC)"), altKeys(",")),
			wantSels: []string{"e", "h"}, wantPrimary: 0, wantAt: xy(1, 1)},
		{name: "A-, with one range is refused", content: grid, evs: altKeys(","),
			wantSels: []string{"a"}, wantHandled: new(false)},
		{name: ") rotates forward and wraps", content: grid, at: xy(1, 0), evs: keys("CC)"),
			wantSels: []string{"b", "e", "h"}, wantPrimary: 0, wantAt: xy(1, 0)},
		{name: "( rotates backward", content: grid, at: xy(1, 0), evs: keys("CC("),
			wantSels: []string{"b", "e", "h"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "rotation takes a count", content: grid, at: xy(1, 0), evs: keys("CC2("),
			wantSels: []string{"b", "e", "h"}, wantPrimary: 0, wantAt: xy(1, 0)},
		{name: "rotation with one range is a no-op", content: grid, evs: keys(")"),
			wantSels: []string{"a"}, wantHandled: new(false)},
		{name: "A-minus merges everything into one range", content: grid, at: xy(1, 0),
			evs:      cat(keys("CC"), altKeys("-")),
			wantSels: []string{"bc\ndef\ngh"}, wantAt: xy(1, 2)},
		{name: "A-s splits a range per line", content: grid, evs: cat(keys("%"), altKeys("s")),
			wantSels: []string{"abc", "def", "ghi"}, wantPrimary: 0, wantAt: xy(2, 0)},
		{name: "A-s on a line selection drops the line ending", content: grid,
			evs:      cat(keys("x"), altKeys("s")),
			wantSels: []string{"abc"}, wantAt: xy(2, 0)},
		{name: "A-s leaves single-line ranges alone", content: grid, evs: cat(keys("l"), altKeys("s")),
			wantSels: []string{"b"}, wantAt: xy(1, 0), wantHandled: new(false)},
	})

	t.Run("A-_ merges ranges that touch", func(t *testing.T) {
		hx, _, _ := newHelix(t, "abcdef", term.Coordinates{})
		h := impl(hx)
		h.setSelection(selection{ranges: []rng{fwd(0, 0, 2, 0), fwd(2, 0, 4, 0), fwd(5, 0, 6, 0)}, primary: 1})
		send(t, hx, modKey(term.ModAlt, '_'))
		assert.Equal(t, []string{"abcd", "f"}, sels(hx))
		assert.Equal(t, 0, primaryIdx(hx))
	})
}

// TestSelectionShapeCommandsOverAllRanges pins ;, A-;, A-:, x, X, A-x
// and _ acting on every range at once.
func TestSelectionShapeCommandsOverAllRanges(t *testing.T) {
	const grid = "abc\ndef\nghi\njkl"
	esc := namedKey(term.KeyEsc)
	runSelCases(t, []selCase{
		{name: "; collapses every range onto its cursor", content: grid,
			evs:      cat(keys("vl"), []term.Event{esc}, keys("C;")),
			wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "A-; flips every range", content: grid,
			evs:      cat(keys("vl"), []term.Event{esc}, keys("C"), altKeys(";")),
			wantSels: []string{"ab", "de"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "A-: makes every range forward again", content: grid,
			evs:      cat(keys("vl"), []term.Event{esc}, keys("C"), altKeys(";:")),
			wantSels: []string{"ab", "de"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "A-: on forward ranges is a no-op", content: grid,
			evs:      cat(keys("vl"), []term.Event{esc}, keys("C"), altKeys(":")),
			wantSels: []string{"ab", "de"}, wantPrimary: 1, wantAt: xy(1, 1), wantHandled: new(false)},
		{name: "x extends every range to its line", content: grid, at: xy(1, 0), evs: keys("Cx"),
			wantSels: []string{"abc\n", "def\n"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "x again merges lines that meet", content: grid, at: xy(1, 0), evs: keys("Cxx"),
			wantSels: []string{"abc\ndef\nghi\n"}, wantAt: xy(2, 2)},
		{name: "X snaps every range to line bounds", content: grid, at: xy(1, 0), evs: keys("CvlX"),
			wantSels: []string{"abc\n", "def\n"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "A-x shrinks every range to whole lines", content: grid,
			evs:      cat(keys("vjl"), []term.Event{esc}, keys("C"), altKeys("x")),
			wantSels: []string{"abc\n", "ghi\n"}, wantPrimary: 1, wantAt: xy(2, 2)},
		{name: "_ trims every range", content: " ab \n cd ", evs: cat(keys("%"), altKeys("s"), keys("_")),
			wantSels: []string{"ab", "cd"}, wantPrimary: 0, wantAt: xy(2, 0)},
		{name: "_ drops all-blank ranges and keeps the primary among the rest",
			content: " ab \n    \n cd ", evs: cat(keys("%"), altKeys("s"), keys(")_")),
			wantSels: []string{"ab", "cd"}, wantPrimary: 1, wantAt: xy(2, 2)},
		{name: "_ with nothing left collapses onto the primary", content: "    \n    ",
			evs:      cat(keys("%"), altKeys("s"), keys("_")),
			wantSels: []string{" "}, wantAt: xy(3, 0)},
	})
}

// TestMotionsOverAllRanges pins that every motion moves every range,
// with its own remembered column for vertical moves, and that ranges
// pushed onto each other merge.
func TestMotionsOverAllRanges(t *testing.T) {
	const grid = "abc\ndef\nghi"
	runSelCases(t, []selCase{
		{name: "l", content: grid, at: xy(1, 0), evs: keys("CCl"),
			wantSels: []string{"c", "f", "i"}, wantPrimary: 2, wantAt: xy(2, 2)},
		{name: "h", content: grid, at: xy(1, 0), evs: keys("CCh"),
			wantSels: []string{"a", "d", "g"}, wantPrimary: 2, wantAt: xy(0, 2)},
		{name: "h stops each range at the line start", content: grid, at: xy(1, 0), evs: keys("CChh"),
			wantSels: []string{"a", "d", "g"}, wantPrimary: 2, wantAt: xy(0, 2)},
		{name: "w", content: "foo bar\nbaz qux", evs: keys("Cw"),
			wantSels: []string{"foo ", "baz "}, wantPrimary: 1, wantAt: xy(3, 1)},
		{name: "e", content: "foo bar\nbaz qux", evs: keys("Ce"),
			wantSels: []string{"foo", "baz"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "b", content: "foo bar\nbaz qux", at: xy(5, 0), evs: keys("Cb"),
			wantSels: []string{"ba", "qu"}, wantPrimary: 1, wantAt: xy(4, 1)},
		{name: "v w extends every range", content: "foo bar\nbaz qux", evs: keys("Cvw"),
			wantSels: []string{"foo ", "baz "}, wantPrimary: 1, wantAt: xy(3, 1)},
		{name: "j", content: "abc\ndef\nghi\njkl", at: xy(1, 0), evs: keys("Cj"),
			wantSels: []string{"e", "h"}, wantPrimary: 1, wantAt: xy(1, 2)},
		{name: "j remembers a column per range",
			content: "abcd\nab\nabcd\nab\nabcd", at: xy(3, 0), evs: keys("Cjj"),
			wantSels: []string{"d", "d"}, wantPrimary: 1, wantAt: xy(3, 4)},
		{name: "j clamps each range onto a shorter line",
			content: "abcd\nab\nabcd\nab\nabcd", at: xy(3, 0), evs: keys("Cj"),
			wantSels: []string{"b", "b"}, wantPrimary: 1, wantAt: xy(1, 3)},
		{name: "k", content: grid, at: xy(1, 1), evs: keys("Ck"),
			wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "f", content: "a-b\nc-d", evs: keys("Cf-"),
			wantSels: []string{"a-", "c-"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "t", content: "a-b\nc-d", evs: keys("Ct-"),
			wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "gl", content: grid, at: xy(0, 0), evs: keys("CCgl"),
			wantSels: []string{"c", "f", "i"}, wantPrimary: 2, wantAt: xy(2, 2)},
		{name: "gh", content: grid, at: xy(2, 0), evs: keys("CCgh"),
			wantSels: []string{"a", "d", "g"}, wantPrimary: 2, wantAt: xy(0, 2)},
		{name: "gg merges every range onto the first line", content: grid, at: xy(1, 0), evs: keys("CCgg"),
			wantSels: []string{"a"}, wantAt: xy(0, 0)},
		{name: "ge merges every range onto the last line", content: grid, at: xy(1, 0), evs: keys("CCge"),
			wantSels: []string{"g"}, wantAt: xy(0, 2)},
		{name: "G merges every range onto the counted line", content: grid, at: xy(1, 0), evs: keys("CC2G"),
			wantSels: []string{"d"}, wantAt: xy(0, 1)},
		{name: "% replaces the set", content: grid, at: xy(1, 0), evs: keys("CC%"),
			wantSels: []string{"abc\ndef\nghi"}, wantAt: xy(2, 2)},
		{name: "mm", content: "(a)\n(b)", evs: keys("Cmm"),
			wantSels: []string{")", ")"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "miw", content: "foo bar\nbaz qux", at: xy(5, 0), evs: keys("Cmiw"),
			wantSels: []string{"bar", "qux"}, wantPrimary: 1, wantAt: xy(6, 1)},
		{name: "ranges that land on the same cell merge", content: "ab\nab", evs: keys("Cll"),
			wantSels: []string{"b", "b"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "ranges pushed into each other merge", content: "abc\ndef", evs: keys("Cxx"),
			wantSels: []string{"abc\ndef"}, wantAt: xy(2, 1)},
	})
}

// TestSearchOverSelectionSet pins search_impl: n replaces the primary
// in normal mode and pushes a new range in select mode.
func TestSearchOverSelectionSet(t *testing.T) {
	t.Run("n replaces the primary", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar foo\nfoo bar foo", term.Coordinates{})
		hx.Search("foo")
		send(t, hx, keys("Cn")...)
		assert.Equal(t, []string{"f", "foo"}, sels(hx))
		assert.Equal(t, 1, primaryIdx(hx))
		assert.Equal(t, xy(10, 1), hx.CursorAtScroll())
	})

	t.Run("v n adds a range per match", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar foo\nfoo bar", term.Coordinates{})
		hx.Search("foo")
		send(t, hx, keys("vnn")...)
		assert.Equal(t, []string{"f", "foo", "foo"}, sels(hx))
		assert.Equal(t, 2, primaryIdx(hx))
		assert.Equal(t, xy(2, 1), hx.CursorAtScroll())
		send(t, hx, keys("N")...)
		assert.Equal(t, []string{"f", "foo", "foo"}, sels(hx), "N re-finds a range already in the set")
		assert.Equal(t, 1, primaryIdx(hx))
	})

	t.Run("a miss leaves the set alone", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		hx.Search("zzz")
		send(t, hx, keys("Cn")...)
		assert.Equal(t, []string{"f"}, sels(hx))
	})
}

// TestSelectionCountMessage pins the count the message bar shows while
// more than one range exists, in the format of Helix's status line.
func TestSelectionCountMessage(t *testing.T) {
	hx, _, _ := newHelix(t, "abc\ndef\nghi", term.Coordinates{})
	hx.Resize(20, 5)
	draw := func() string {
		w := term.NewStringWriter(20, 5)
		hx.Draw(w)
		require.NoError(t, w.Flush())
		return w.String()
	}
	assert.NotContains(t, draw(), "sels")
	send(t, hx, keys("CC")...)
	assert.Contains(t, draw(), "3/3 sels")
	send(t, hx, key(')'))
	assert.Contains(t, draw(), "1/3 sels")
	send(t, hx, key(','))
	assert.NotContains(t, draw(), "sels")
}

// TestSelectionSetFollowsOutOfBandEdits pins that an edit the handler
// did not make, such as one the IDE applies between two keys, carries
// every range along with the text, the way Helix maps a view's
// selection through a transaction.
func TestSelectionSetFollowsOutOfBandEdits(t *testing.T) {
	hx, buf, _ := newHelix(t, "abcdef\nabcdef", term.Coordinates{X: 3})
	send(t, hx, keys("C")...)
	require.Equal(t, []string{"d", "d"}, sels(hx))

	buf.Edit(context.Background(), xy(0, 0), xy(0, 0), "XX")
	buf.Edit(context.Background(), xy(0, 1), xy(2, 1), "")
	send(t, hx, key('l'))
	assert.Equal(t, []string{"e", "e"}, sels(hx))
	assert.Equal(t, xy(2, 1), hx.CursorAtScroll())
	assert.Equal(t, "XXabcdef\ncdef", buf.String())
}

// TestSecondaryCaretIsDim pins the drawn attribute of a ghost caret.
func TestSecondaryCaretIsDim(t *testing.T) {
	hx, _, _ := newHelix(t, "abc\ndef", term.Coordinates{})
	hx.Resize(10, 3)
	send(t, hx, keys("C")...)
	w := term.NewStringWriter(10, 3)
	hx.Draw(w)
	require.NoError(t, w.Flush())
	cells := w.Cells()
	require.GreaterOrEqual(t, len(cells), 10)
	ghost := cells[0]
	assert.Equal(t, 'a', ghost.Ch)
	assert.NotZero(t, ghost.Attrs&term.AttrDim, "the secondary caret on row 0 is dimmed")
	assert.NotZero(t, ghost.Attrs&term.AttrReverse)
}

// cursorState is everything installRange is allowed to change.
type cursorState struct {
	mode     text.SelectMode
	selected bool
	from, to term.Coordinates
	caret    term.Coordinates
}

func snapshotCursor(hx *Helix) cursorState {
	c := &impl(hx).cursor
	s := cursorState{caret: c.CursorAtScroll()}
	s.mode, s.selected = c.SelectionMode()
	s.from, s.to, _ = c.SelectionRange()
	return s
}

// TestSelectionSetMirrorsCursor pins the Phase 1 contract: after any
// event the selection set is the one range the cursor shows, and
// installing that range again changes nothing, so the set can be
// rebuilt from the cursor and pushed back without drift.
func TestSelectionSetMirrorsCursor(t *testing.T) {
	click := func(x, y int) term.Event {
		return term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: x, MouseY: y}
	}
	release := func(x, y int) term.Event {
		return term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: x, MouseY: y}
	}
	const text3 = "one two\n\nthree four"
	for _, tc := range []struct {
		name    string
		content string
		at      term.Coordinates
		evs     []term.Event
	}{
		{name: "fresh", content: text3},
		{name: "empty buffer", content: ""},
		{name: "forward word", content: text3, evs: keys("w")},
		{name: "backward word", content: text3, at: term.Coordinates{X: 5}, evs: keys("b")},
		{name: "caret on an empty row", content: text3, evs: keys("j")},
		{name: "select mode across rows", content: text3, evs: keys("vjj")},
		{name: "select mode ending on an empty row", content: text3, evs: keys("vj")},
		{name: "line", content: text3, at: term.Coordinates{X: 2}, evs: keys("x")},
		{name: "two lines", content: text3, evs: keys("xx")},
		{name: "last line", content: text3, at: term.Coordinates{Y: 2}, evs: keys("x")},
		{name: "backward line", content: text3, at: term.Coordinates{Y: 2}, evs: keys("vkX")},
		{name: "snapped to lines", content: text3, evs: keys("wX")},
		{name: "select all", content: text3, evs: keys("%")},
		{name: "select all with a trailing newline", content: "a\nb\n", evs: keys("%")},
		{name: "text object", content: text3, at: term.Coordinates{X: 5}, evs: keys("miw")},
		{name: "paragraph", content: text3, evs: keys("mip")},
		{name: "flipped", content: text3, evs: append(keys("w"), modKey(term.ModAlt, ';'))},
		{name: "insert point", content: text3, evs: keys("i")},
		{name: "append at the line end", content: text3, at: term.Coordinates{X: 6}, evs: keys("a")},
		{name: "insert on an empty row", content: text3, evs: keys("ji")},
		{name: "after leaving insert", content: text3,
			evs: []term.Event{key('A'), key('!'), namedKey(term.KeyEsc)}},
		{name: "wide glyphs", content: "世界 x", evs: keys("e")},
		{name: "mouse click", content: text3, evs: []term.Event{click(2, 2), release(2, 2)}},
		{name: "mouse drag", content: text3, evs: []term.Event{click(0, 0), click(3, 2), release(3, 2)}},
		{name: "search hit", content: text3, evs: append(keys("/thr"), namedKey(term.KeyEnter))},
		{name: "jump back", content: text3, evs: append(keys("2G"), modKey(term.ModCtrl, 'o'))},
		{name: "after undo", content: text3, evs: keys("wdu")},
		{name: "after a paste", content: text3, evs: keys("wyp")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at)
			send(t, hx, tc.evs...)
			h := impl(hx)

			require.Equal(t, 1, h.sel.len())
			primary := h.sel.primaryRange()
			primary.col = 0
			assert.Equal(t, h.readRange(), primary, "set mirrors the cursor")

			before := snapshotCursor(hx)
			h.installRange(h.readRange())
			assert.Equal(t, before, snapshotCursor(hx), "re-installing is a no-op")
			assert.Equal(t, before, func() cursorState {
				h.setSelection(h.sel)
				return snapshotCursor(hx)
			}(), "re-setting the selection is a no-op")
		})
	}
}

// TestInstallRangeShapes pins the shapes installRange builds for the
// cursor and that readRange reads each of them back unchanged.
func TestInstallRangeShapes(t *testing.T) {
	const content = "abc\ndef\n\nghi"
	for _, tc := range []struct {
		name     string
		r        rng
		mode     text.SelectMode
		from, to term.Coordinates
		caret    term.Coordinates
		readBack rng
	}{
		{name: "forward cells", r: fwd(1, 0, 3, 0), mode: text.StandardSelection,
			from: xy(1, 0), to: xy(3, 0), caret: xy(2, 0)},
		{name: "backward cells", r: fwd(3, 0, 1, 0), mode: text.StandardSelection,
			from: xy(1, 0), to: xy(3, 0), caret: xy(1, 0)},
		{name: "across rows", r: fwd(2, 0, 2, 1), mode: text.StandardSelection,
			from: xy(2, 0), to: xy(2, 1), caret: xy(1, 1)},
		{name: "one line", r: fwd(0, 1, 0, 2), mode: text.LineSelection,
			from: xy(0, 1), to: xy(3, 1), caret: xy(2, 1)},
		{name: "two lines backward", r: fwd(0, 2, 0, 0), mode: text.LineSelection,
			from: xy(0, 0), to: xy(3, 1), caret: xy(0, 0)},
		{name: "the last line reaches the document end", r: fwd(0, 3, 0, 4),
			mode: text.LineSelection, from: xy(0, 3), to: xy(3, 3), caret: xy(2, 3)},
		{name: "a point is one cell", r: fwd(1, 0, 1, 0), mode: text.StandardSelection,
			from: xy(1, 0), to: xy(2, 0), caret: xy(1, 0), readBack: fwd(1, 0, 2, 0)},
		{name: "a point on an empty row stays a point", r: fwd(0, 2, 0, 2),
			mode: text.StandardSelection, from: xy(0, 2), to: xy(1, 2), caret: xy(0, 2)},
		{name: "a range over the empty row", r: fwd(0, 2, 1, 3),
			mode: text.StandardSelection, from: xy(0, 2), to: xy(1, 3), caret: xy(0, 3)},
		{name: "past the document end clamps", r: fwd(9, 9, 9, 9),
			mode: text.StandardSelection, from: xy(3, 3), to: xy(4, 3), caret: xy(3, 3),
			readBack: fwd(3, 3, 3, 3)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, content, term.Coordinates{})
			h := impl(hx)
			h.installRange(tc.r)
			got := snapshotCursor(hx)
			assert.Equal(t, cursorState{
				mode: tc.mode, selected: true, from: tc.from, to: tc.to, caret: tc.caret,
			}, got)
			want := tc.readBack
			if want == (rng{}) {
				want = tc.r
			}
			assert.Equal(t, want, h.readRange())
		})
	}

	t.Run("a point in insert mode has no selection", func(t *testing.T) {
		hx, _, _ := newHelix(t, content, term.Coordinates{})
		send(t, hx, key('i'))
		h := impl(hx)
		h.installRange(fwd(3, 1, 3, 1))
		assert.Equal(t, cursorState{caret: xy(3, 1)}, snapshotCursor(hx))
		assert.Equal(t, fwd(3, 1, 3, 1), h.readRange())
	})
}

// TestPinnedSelectionIsReboundAfterTheEvent pins that a text object,
// which the cursor holds as an explicit range, is an ordinary
// anchor/head pair by the time the next key arrives, so it can be
// flipped and extended like any other selection.
func TestPinnedSelectionIsReboundAfterTheEvent(t *testing.T) {
	hx, _, _ := newHelix(t, "foo bar baz", term.Coordinates{X: 5})
	send(t, hx, keys("miw")...)
	require.Equal(t, "bar", sel(t, hx))
	assert.Equal(t, term.Coordinates{X: 6}, hx.CursorAtScroll(),
		"the caret sits on the last cell of the object, as Range::cursor does")

	send(t, hx, modKey(term.ModAlt, ';'))
	assert.Equal(t, term.Coordinates{X: 4}, hx.CursorAtScroll(), "flipped onto the anchor")
	assert.Equal(t, "bar", sel(t, hx))

	send(t, hx, modKey(term.ModAlt, ';'))
	send(t, hx, key('v'), key('e'))
	assert.Equal(t, "bar baz", sel(t, hx), "extended from the object's anchor")
}

// TestSecondaryRangesAreDrawn pins the location lists that show the
// non-primary ranges: the covered cells per row, and the cell each
// range's cursor sits on dimmed, with nothing left behind once the set
// collapses to one range.
func TestSecondaryRangesAreDrawn(t *testing.T) {
	hx, _, _ := newHelix(t, "abc\ndef\nghi", term.Coordinates{})
	h := impl(hx)
	lists := func() map[string][]textapi.Location {
		out := map[string][]textapi.Location{}
		for _, set := range hx.LocationLists() {
			out[set.ID] = set.Locations
		}
		return out
	}

	h.setSelection(selection{ranges: []rng{fwd(0, 0, 2, 0), fwd(1, 1, 3, 1), fwd(1, 2, 3, 2)}, primary: 1})
	require.Equal(t, 3, h.sel.len())
	assert.Equal(t, "ef", sel(t, hx), "the cursor shows the primary")

	got := lists()
	reverse := term.Attributes{Attrs: term.AttrReverse}
	ghost := term.Attributes{Attrs: term.AttrReverse | term.AttrDim}
	assert.Equal(t, []textapi.Location{
		{From: xy(0, 0), To: xy(2, 0), Attr: reverse},
		{From: xy(1, 2), To: xy(3, 2), Attr: reverse},
	}, got[selectionsLocID])
	assert.Equal(t, []textapi.Location{
		{From: xy(1, 0), To: xy(2, 0), Attr: ghost},
		{From: xy(2, 2), To: xy(3, 2), Attr: ghost},
	}, got[cursorsLocID])

	h.setSelection(single(fwd(0, 0, 1, 0)))
	got = lists()
	assert.Empty(t, got[selectionsLocID])
	assert.Empty(t, got[cursorsLocID])
}
