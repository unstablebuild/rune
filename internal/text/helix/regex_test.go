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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// prompt types a pattern into an open regex prompt and confirms it.
func prompt(pattern string) []term.Event {
	return append(keys(pattern), namedKey(term.KeyEnter))
}

func message(t *testing.T, hx *Helix) string {
	t.Helper()
	w := term.NewStringWriter(60, 6)
	hx.Draw(w)
	require.NoError(t, w.Flush())
	return w.String()
}

func registerText(t *testing.T, hx *Helix, name rune) string {
	t.Helper()
	data, err := impl(hx).readRegister(name)
	require.NoError(t, err)
	return data.Text
}

// TestDocIndex pins the mapping between regex offsets and cells: one
// cell is one rune, whatever its width, and a line ending is one byte.
func TestDocIndex(t *testing.T) {
	hx, _, _ := newHelix(t, "世界 x\n\ty", term.Coordinates{})
	d := indexDoc(impl(hx).buf())
	assert.Equal(t, "世界 x\n\ty", d.text)
	for _, tc := range []struct {
		pos term.Coordinates
		off int
	}{
		{xy(0, 0), 0}, {xy(1, 0), 3}, {xy(2, 0), 6}, {xy(3, 0), 7}, {xy(4, 0), 8},
		{xy(0, 1), 9}, {xy(1, 1), 10}, {xy(2, 1), 11},
	} {
		assert.Equal(t, tc.off, d.offset(tc.pos), "offset of %v", tc.pos)
		assert.Equal(t, tc.pos, d.pos(tc.off), "pos of %d", tc.off)
	}
	assert.Equal(t, len(d.text), d.offset(docEnd(impl(hx).buf())), "the document end clamps onto the text")
}

// TestSelectRegex pins s, the select_regex prompt.
func TestSelectRegex(t *testing.T) {
	runSelCases(t, []selCase{
		{name: "every match in every range becomes a range", content: "foo bar\nbaz",
			evs: cat(keys("%s"), prompt(`\w+`)), wantSels: []string{"foo", "bar", "baz"}, wantAt: xy(2, 0)},
		{name: "^ puts a cursor on every line", content: "a\nb\nc",
			evs: cat(keys("%s"), prompt("^")), wantSels: []string{"a", "b", "c"}, wantAt: xy(0, 0)},
		// $ matches before the line ending and again at the end of the
		// range's text; the second is the anchor outside the range and
		// is dropped, the first is a point on the line ending, which
		// the cursor parks on the last cell.
		{name: "a match right at the end of the range is an anchor and is dropped", content: "ab\ncd",
			evs: cat(keys("xs"), prompt("$")), wantSels: []string{"\n"}, wantAt: xy(1, 0)},
		{name: "lower case is case insensitive", content: "Foo foo",
			evs: cat(keys("%s"), prompt("foo")), wantSels: []string{"Foo", "foo"}, wantAt: xy(2, 0)},
		{name: "an upper case letter makes it case sensitive", content: "Foo foo",
			evs: cat(keys("%s"), prompt("Foo")), wantSels: []string{"Foo"}, wantAt: xy(2, 0)},
		{name: "matches are found per range", content: "foo bar\nfoo bar", at: xy(4, 0),
			evs: cat(keys("eCs"), prompt("a")), wantSels: []string{"a", "a"}, wantAt: xy(5, 0)},
		{name: "the prompt previews while typing", content: "foo bar", evs: keys("%sba"),
			wantSels: []string{"ba"}, wantAt: xy(5, 0)},
		{name: "esc restores the selection", content: "foo bar",
			evs:      cat(keys("%sba"), []term.Event{namedKey(term.KeyEsc)}),
			wantSels: []string{"foo bar"}, wantAt: xy(6, 0)},
		{name: "a pattern that matches nothing leaves the selection", content: "foo bar",
			evs: cat(keys("%s"), prompt("zzz")), wantSels: []string{"foo bar"}, wantAt: xy(6, 0)},
		{name: "a pattern that does not compile leaves the selection", content: "foo bar",
			evs: cat(keys("%s"), prompt("(")), wantSels: []string{"foo bar"}, wantAt: xy(6, 0)},
		{name: "runes wider than one column map one to one", content: "世界 x",
			evs: cat(keys("%s"), prompt(`\pL+`)), wantSels: []string{"世界", "x"}, wantAt: xy(1, 0)},
		{name: "a tab is one cell", content: "\tfoo bar",
			evs: cat(keys("%s"), prompt("bar")), wantSels: []string{"bar"}, wantAt: xy(7, 0)},
	})

	t.Run("nothing selected is reported on enter", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		hx.Resize(60, 6)
		send(t, hx, cat(keys("%s"), prompt("zzz"))...)
		assert.Contains(t, message(t, hx), "nothing selected")
		assert.True(t, hx.IsNormalMode())
	})

	t.Run("an invalid pattern is reported on enter", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		hx.Resize(60, 6)
		send(t, hx, cat(keys("%s"), prompt("("))...)
		assert.Contains(t, message(t, hx), "error parsing regexp")
	})

	t.Run("the pattern goes to the / register and n follows it", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a1 b2 c3", term.Coordinates{})
		send(t, hx, cat(keys("%s"), prompt(`\d`))...)
		assert.Equal(t, `\d`, registerText(t, hx, '/'))
		send(t, hx, keys(",n")...)
		assert.Equal(t, []string{"2"}, sels(hx))
	})

	t.Run("the selection before the prompt is a jump", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, cat(keys("%s"), prompt("bar"))...)
		require.Equal(t, []string{"bar"}, sels(hx))
		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, []string{"foo bar"}, sels(hx))
	})
}

// TestSplitAndKeepRegex pins S, K and A-K.
func TestSplitAndKeepRegex(t *testing.T) {
	runSelCases(t, []selCase{
		{name: "S splits every range on its matches", content: "a,b,c",
			evs: cat(keys("%S"), prompt(",")), wantSels: []string{"a", "b", "c"}, wantAt: xy(0, 0)},
		{name: "S with no match keeps the range", content: "abc",
			evs: cat(keys("%S"), prompt(",")), wantSels: []string{"abc"}, wantAt: xy(2, 0)},
		{name: "K keeps the ranges that match", content: "foo\nbar\nfoo",
			evs:      cat(keys("%"), altKeys("s"), keys("K"), prompt("foo")),
			wantSels: []string{"foo", "foo"}, wantAt: xy(2, 0)},
		{name: "A-K removes the ranges that match", content: "foo\nbar\nfoo",
			evs:      cat(keys("%"), altKeys("sK"), prompt("foo")),
			wantSels: []string{"bar"}, wantAt: xy(2, 1)},
		{name: "K with nothing left keeps the set", content: "foo\nbar",
			evs:      cat(keys("%"), altKeys("s"), keys("K"), prompt("zzz")),
			wantSels: []string{"foo", "bar"}, wantAt: xy(2, 0)},
	})

	t.Run("split keeps a point as it is", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a,b", term.Coordinates{})
		re, err := compilePattern(",")
		require.NoError(t, err)
		s := splitOnMatches(impl(hx).buf(), selection{ranges: []rng{point(xy(1, 0)), fwd(0, 0, 3, 0)}}, re)
		assert.Equal(t, []rng{fwd(0, 0, 1, 0), point(xy(1, 0)), fwd(2, 0, 3, 0)}, s.ranges)
	})

	t.Run("no selections remaining is reported on enter", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		hx.Resize(60, 6)
		send(t, hx, cat(keys("%K"), prompt("zzz"))...)
		assert.Contains(t, message(t, hx), "no selections remaining")
	})
}

// TestRegexSearch pins the / prompt, n and N over the / register.
func TestRegexSearch(t *testing.T) {
	t.Run("the prompt takes a regex", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar42", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt(`\d+`))...)
		assert.Equal(t, []string{"42"}, sels(hx))
		assert.Equal(t, `\d+`, registerText(t, hx, '/'))
	})

	t.Run("the prompt previews the first match while typing and esc backs out", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("/ba")...)
		assert.Equal(t, []string{"ba"}, sels(hx))
		send(t, hx, namedKey(term.KeyEsc))
		assert.Equal(t, []string{"f"}, sels(hx))
	})

	t.Run("a search in select mode adds a range", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, cat(keys("v/"), prompt("bar"))...)
		assert.Equal(t, []string{"f", "bar"}, sels(hx))
		assert.Equal(t, 1, primaryIdx(hx))
	})

	t.Run("n in select mode adds a range per match", func(t *testing.T) {
		hx, _, _ := newHelix(t, "x a a a", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt("a"), keys("vnn"))...)
		assert.Equal(t, []string{"a", "a", "a"}, sels(hx))
		assert.Equal(t, 2, primaryIdx(hx))
	})

	t.Run("a count walks several matches", func(t *testing.T) {
		hx, _, _ := newHelix(t, "x a a a", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt("a"), keys("2n"))...)
		assert.Equal(t, xy(6, 0), hx.CursorAtScroll())
	})

	t.Run("wrapping and missing are reported", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nbar", term.Coordinates{})
		hx.Resize(60, 6)
		send(t, hx, cat(keys("/"), prompt("bar"), keys("n"))...)
		assert.Contains(t, message(t, hx), "Wrapped around document")
		assert.Equal(t, []string{"bar"}, sels(hx))
		impl(hx).setSearchPattern("zzz")
		_, handled := hx.Handle(key('n'))
		assert.False(t, handled)
		assert.Contains(t, message(t, hx), "No more matches")
	})

	t.Run("an empty match at the start of the document is skipped", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo", term.Coordinates{X: 1})
		send(t, hx, cat(keys("/"), prompt("^"))...)
		assert.Equal(t, xy(1, 0), hx.CursorAtScroll())
	})

	t.Run("the hit takes the direction of the primary", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{X: 1})
		send(t, hx, cat(keys("vh"), []term.Event{namedKey(term.KeyEsc)}, keys("/"), prompt("bar"))...)
		assert.Equal(t, []string{"bar"}, sels(hx))
		assert.Equal(t, xy(4, 0), hx.CursorAtScroll(), "a backward range has its cursor at its start")
	})

	t.Run("Search arms a literal", func(t *testing.T) {
		hx, _, _ := newHelix(t, "axb a.b", term.Coordinates{})
		hx.Search("a.b")
		send(t, hx, key('n'))
		assert.Equal(t, []string{"a.b"}, sels(hx))
	})

	t.Run("an invalid register pattern is reported", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo", term.Coordinates{})
		hx.Resize(60, 6)
		impl(hx).setSearchPattern("(")
		_, handled := hx.Handle(key('n'))
		assert.False(t, handled)
		assert.Contains(t, message(t, hx), "Invalid regex")
	})

	t.Run("every match of the pattern is highlighted", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a1\nb22", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt(`\d+`))...)
		var got []textapi.Location
		for _, set := range hx.LocationLists() {
			if set.ID == searchLocID {
				got = set.Locations
			}
		}
		attr := impl(hx).less.Scroll().ResultsAttr
		assert.Equal(t, []textapi.Location{
			{From: xy(1, 0), To: xy(2, 0), Attr: attr},
			{From: xy(1, 1), To: xy(3, 1), Attr: attr},
		}, got)
		impl(hx).setSearchPattern("(")
		for _, set := range hx.LocationLists() {
			if set.ID == searchLocID {
				assert.Empty(t, set.Locations, "a pattern that does not compile clears the highlight")
			}
		}
	})
}

// TestSearchSelection pins * and A-*, which build the / register from
// every range.
func TestSearchSelection(t *testing.T) {
	t.Run("* joins the escaped fragments with word boundaries", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo a.b\nbar foo a.b", term.Coordinates{})
		hx.Resize(60, 6)
		impl(hx).setSelection(selOf(1, fwd(0, 0, 3, 0), fwd(4, 0, 7, 0)).clone())
		send(t, hx, key('*'))
		assert.Equal(t, `\bfoo\b|\ba\.b\b`, registerText(t, hx, '/'))
		assert.Contains(t, message(t, hx), `register '/' set to '\bfoo\b|\ba\.b\b'`)
		send(t, hx, key('n'))
		assert.Equal(t, []string{"foo", "foo"}, sels(hx))
		assert.Equal(t, xy(6, 1), hx.CursorAtScroll())
		send(t, hx, key('n'))
		assert.Equal(t, []string{"foo", "a.b"}, sels(hx))
	})

	t.Run("A-* leaves the boundaries out", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foobar", term.Coordinates{})
		send(t, hx, cat(keys("vll"), altKeys("*"))...)
		assert.Equal(t, "foo", registerText(t, hx, '/'))
	})

	t.Run("* on a fragment inside a word gets no boundary on that side", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foobar", term.Coordinates{X: 1})
		send(t, hx, keys("vl*")...)
		assert.Equal(t, "oo", registerText(t, hx, '/'))
	})

	t.Run("identical fragments are listed once", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo\nfoo", term.Coordinates{})
		send(t, hx, keys("eC*")...)
		assert.Equal(t, `\bfoo\b`, registerText(t, hx, '/'))
	})
}

// TestAlignSelections pins &, which pads the ranges of every row onto
// the same columns.
func TestAlignSelections(t *testing.T) {
	t.Run("ranges line up column by column", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a b c\naaa b c", term.Coordinates{})
		impl(hx).setSelection(selection{
			ranges:  []rng{fwd(2, 0, 3, 0), fwd(4, 0, 5, 0), fwd(4, 1, 5, 1), fwd(6, 1, 7, 1)},
			primary: 3,
		})
		send(t, hx, key('&'))
		assert.Equal(t, "a   b c\naaa b c", buf.String())
		assert.Equal(t, []string{"b", "c", "b", "c"}, sels(hx))
		assert.Equal(t, 3, primaryIdx(hx))
	})

	t.Run("a range spanning rows is refused", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a b\nc d", term.Coordinates{})
		hx.Resize(60, 6)
		send(t, hx, keys("vj&")...)
		assert.Equal(t, "a b\nc d", buf.String())
		assert.Contains(t, message(t, hx), "align cannot work with multi line selections")
	})
}

// TestRotateSelectionContents pins A-( and A-), which move the text
// under the ranges around.
func TestRotateSelectionContents(t *testing.T) {
	three := selOf(0, fwd(0, 0, 1, 0), fwd(2, 0, 3, 0), fwd(4, 0, 5, 0))
	runOpSelCases(t, []opSelCase{
		{name: "A-) moves every fragment one range forward", content: "a b c", sel: three,
			evs: altKeys(")"), want: "c a b", wantSels: []string{"c", "a", "b"}, wantPrimary: 1, wantAt: xy(2, 0)},
		{name: "A-( moves every fragment one range back", content: "a b c", sel: three,
			evs: altKeys("("), want: "b c a", wantSels: []string{"b", "c", "a"}, wantPrimary: 2, wantAt: xy(4, 0)},
		{name: "a count rotates further", content: "a b c", sel: three,
			evs: cat(keys("2"), altKeys(")")), want: "b c a", wantSels: []string{"b", "c", "a"}, wantPrimary: 2, wantAt: xy(4, 0)},
		{name: "fragments of different lengths reshape the ranges", content: "aa b",
			sel: selOf(0, fwd(0, 0, 2, 0), fwd(3, 0, 4, 0)),
			evs: altKeys(")"), want: "b aa", wantSels: []string{"b", "aa"}, wantPrimary: 1, wantAt: xy(3, 0)},
		{name: "a single range is left alone", content: "abc", evs: altKeys(")"),
			want: "abc", wantSels: []string{"a"}, wantAt: xy(0, 0)},
	})
}
