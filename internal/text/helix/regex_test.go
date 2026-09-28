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
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

// prompt types a pattern into an open regex prompt and confirms it.
func prompt(pattern string) []term.Event {
	return append(keys(pattern), namedKey(term.KeyEnter))
}

func enter() []term.Event { return []term.Event{namedKey(term.KeyEnter)} }

func backspace(n int) []term.Event {
	evs := make([]term.Event, n)
	for i := range evs {
		evs[i] = namedKey(term.KeyBackspace)
	}
	return evs
}

// screen draws the handler into a small terminal and returns the
// text, which is where a message or the prompt label shows up.
func screen(t *testing.T, hx *Helix) string {
	t.Helper()
	hx.Resize(60, 6)
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

func mustCompile(t *testing.T, pattern string) *regexp.Regexp {
	t.Helper()
	re, err := compilePattern(pattern)
	require.NoError(t, err)
	return re
}

func searchLocations(hx *Helix) []textapi.Location {
	for _, set := range hx.LocationLists() {
		if set.ID == searchLocID {
			return set.Locations
		}
	}
	return nil
}

// unchanged fills in the buffer expectation of cases that must not
// edit anything.
func unchanged(cases ...opSelCase) []opSelCase {
	for i := range cases {
		if cases[i].want == "" {
			cases[i].want = cases[i].content
		}
	}
	return cases
}

// TestDocIndex pins the mapping between regex byte offsets and cells:
// one cell is one grapheme, whatever its width or byte length, a line
// ending is one byte, and anything outside the document clamps onto
// it.
func TestDocIndex(t *testing.T) {
	type pair struct {
		pos term.Coordinates
		off int
	}
	type offsetOf struct {
		pos term.Coordinates
		off int
	}
	type posOf struct {
		off int
		pos term.Coordinates
	}
	cases := []struct {
		name      string
		content   string
		wantText  string
		wantRows  []int
		roundTrip []pair
		offsets   []offsetOf
		positions []posOf
		ends      []posOf
	}{
		{name: "empty document", content: "", wantText: "", wantRows: []int{0, 1},
			roundTrip: []pair{{xy(0, 0), 0}},
			offsets:   []offsetOf{{xy(5, 0), 0}, {xy(0, 1), 0}, {xy(0, -1), 0}, {xy(-1, 0), 0}},
			positions: []posOf{{-1, xy(0, 0)}, {7, xy(0, 0)}},
			ends:      []posOf{{0, xy(0, 0)}, {3, xy(0, 0)}}},
		{name: "one line", content: "abc", wantText: "abc", wantRows: []int{0, 4},
			roundTrip: []pair{{xy(0, 0), 0}, {xy(1, 0), 1}, {xy(3, 0), 3}},
			offsets:   []offsetOf{{xy(9, 0), 3}, {xy(0, 1), 3}, {xy(0, 9), 3}, {xy(-2, 0), 0}, {xy(0, -1), 0}},
			positions: []posOf{{4, xy(3, 0)}, {99, xy(3, 0)}, {-5, xy(0, 0)}}},
		{name: "two lines", content: "ab\ncd", wantText: "ab\ncd", wantRows: []int{0, 3, 6},
			roundTrip: []pair{{xy(0, 0), 0}, {xy(2, 0), 2}, {xy(0, 1), 3}, {xy(1, 1), 4}, {xy(2, 1), 5}},
			offsets:   []offsetOf{{xy(0, 2), 5}, {xy(5, 0), 2}, {xy(-3, 1), 3}},
			positions: []posOf{{6, xy(2, 1)}}},
		{name: "empty lines", content: "\n\n", wantText: "\n\n", wantRows: []int{0, 1, 2, 3},
			roundTrip: []pair{{xy(0, 0), 0}, {xy(0, 1), 1}, {xy(0, 2), 2}},
			offsets:   []offsetOf{{xy(0, 3), 2}, {xy(4, 1), 1}}},
		{name: "trailing newline", content: "a\n", wantText: "a\n", wantRows: []int{0, 2, 3},
			roundTrip: []pair{{xy(1, 0), 1}, {xy(0, 1), 2}},
			offsets:   []offsetOf{{xy(0, 2), 2}}},
		{name: "wide glyphs and a tab", content: "世界 x\n\ty", wantText: "世界 x\n\ty", wantRows: []int{0, 9, 12},
			roundTrip: []pair{
				{xy(0, 0), 0}, {xy(1, 0), 3}, {xy(2, 0), 6}, {xy(3, 0), 7}, {xy(4, 0), 8},
				{xy(0, 1), 9}, {xy(1, 1), 10}, {xy(2, 1), 11},
			}},
		{name: "a combining mark shares its cell", content: "e\u0301x", wantText: "e\u0301x", wantRows: []int{0, 5},
			roundTrip: []pair{{xy(0, 0), 0}, {xy(1, 0), 3}, {xy(2, 0), 4}},
			positions: []posOf{{1, xy(0, 0)}, {2, xy(0, 0)}},
			ends:      []posOf{{0, xy(0, 0)}, {1, xy(1, 0)}, {2, xy(1, 0)}, {3, xy(1, 0)}, {4, xy(2, 0)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hx, buf, _ := newHelix(t, tc.content, term.Coordinates{})
			d := indexDoc(impl(hx).buf())
			assert.Equal(t, tc.wantText, d.text)
			assert.Equal(t, tc.wantRows, d.rowStart)
			for _, p := range tc.roundTrip {
				assert.Equal(t, p.off, d.offset(p.pos), "offset of %v", p.pos)
				assert.Equal(t, p.pos, d.pos(p.off), "pos of %d", p.off)
				assert.Equal(t, p.pos, d.endPos(p.off), "endPos of %d", p.off)
			}
			for _, o := range tc.offsets {
				assert.Equal(t, o.off, d.offset(o.pos), "offset of %v", o.pos)
			}
			for _, p := range tc.positions {
				assert.Equal(t, p.pos, d.pos(p.off), "pos of %d", p.off)
			}
			for _, p := range tc.ends {
				assert.Equal(t, p.pos, d.endPos(p.off), "endPos of %d", p.off)
			}
			assert.Equal(t, len(d.text), d.offset(docEnd(buf)), "the document end clamps onto the text")
		})
	}

	t.Run("a rune the encoder rejects counts as the replacement character", func(t *testing.T) {
		assert.Equal(t, 3, runeBytes(0xD800))
		assert.Equal(t, 3, runeBytes(-1))
		assert.Equal(t, 1, runeBytes(0))
		assert.Equal(t, 1, runeBytes('a'))
		assert.Equal(t, 3, runeBytes('世'))
	})
}

// TestCompilePattern pins the flags the prompt compiles with: smart
// case and multi-line anchors.
func TestCompilePattern(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		text    string
		want    bool
		wantErr bool
	}{
		{name: "lower case matches any case", pattern: "foo", text: "FOO", want: true},
		{name: "an upper case letter is case sensitive", pattern: "Foo", text: "FOO", want: false},
		{name: "an upper case letter matches itself", pattern: "Foo", text: "Foo", want: true},
		{name: "a non-ascii upper case letter is case sensitive", pattern: "Ñ", text: "ñ", want: false},
		{name: "a non-ascii lower case letter matches any case", pattern: "ñ", text: "Ñ", want: true},
		{name: "an upper case letter in a class counts", pattern: "[A-Z]", text: "a", want: false},
		{name: "^ matches after a line ending", pattern: "^b", text: "a\nb", want: true},
		{name: "$ matches before a line ending", pattern: "a$", text: "a\nb", want: true},
		{name: "an escaped meta character is literal", pattern: `a\.b`, text: "axb", want: false},
		{name: "the empty pattern matches", pattern: "", text: "x", want: true},
		{name: "an unclosed group does not compile", pattern: "(", wantErr: true},
		{name: "an unclosed class does not compile", pattern: "[", wantErr: true},
		{name: "a bare repetition does not compile", pattern: "*", wantErr: true},
		{name: "a look-around does not compile", pattern: "(?=a)", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			re, err := compilePattern(tc.pattern)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, re.MatchString(tc.text))
		})
	}
}

// TestHasContextAssertion pins which patterns need to see the text
// around a range.
func TestHasContextAssertion(t *testing.T) {
	cases := []struct {
		pattern string
		want    bool
	}{
		{`\w+`, false}, {`.`, false}, {``, false}, {`(?:x)*`, false},
		{`[$^]`, false}, {`\$`, false}, {`\^`, false}, {`a|b`, false},
		{`^`, true}, {`$`, true}, {`\b`, true}, {`\B`, true}, {`\A`, true}, {`\z`, true},
		{`a|^b`, true}, {`(?:(x$))`, true}, {`x*$`, true},
	}
	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			assert.Equal(t, tc.want, hasContextAssertion(mustCompile(t, tc.pattern)))
		})
	}
}

// TestMatcherSpans pins the search over one range: a pattern without
// an anchor runs on the range's text alone, one with an anchor sees
// the character on either side so ^, $ and \b mean what they do in
// Helix's bounded search.
func TestMatcherSpans(t *testing.T) {
	cases := []struct {
		name    string
		content string
		r       rng
		pattern string
		want    [][]int
	}{
		{name: "matches inside the range", content: "foo bar baz", r: fwd(4, 0, 7, 0), pattern: `\w+`, want: [][]int{{4, 7}}},
		{name: "a match is cut at the range end when nothing looks past it",
			content: "foo", r: fwd(0, 0, 2, 0), pattern: `\w+`, want: [][]int{{0, 2}}},
		{name: "a match starts at the range start whatever is before it",
			content: "aab", r: fwd(1, 0, 3, 0), pattern: `a+`, want: [][]int{{1, 2}}},
		// A bounded search would find "fo"; the anchor makes the search
		// look past the range, where the match keeps going.
		{name: "an anchored match running past the range is dropped",
			content: "foo", r: fwd(0, 0, 2, 0), pattern: `^\w+`, want: nil},
		{name: "^ in the middle of a line does not match", content: "ab cd", r: fwd(3, 0, 5, 0), pattern: `^`, want: nil},
		{name: "^ after a line ending matches", content: "ab\ncd", r: fwd(0, 1, 2, 1), pattern: `^`, want: [][]int{{3, 3}}},
		{name: "^ at the document start matches", content: "ab", r: fwd(0, 0, 2, 0), pattern: `^`, want: [][]int{{0, 0}}},
		{name: "$ before a line ending matches", content: "ab\ncd", r: fwd(0, 0, 2, 0), pattern: `$`, want: [][]int{{2, 2}}},
		{name: "$ in the middle of a line does not match", content: "ab cd", r: fwd(0, 0, 2, 0), pattern: `$`, want: nil},
		{name: "$ at the document end matches", content: "ab", r: fwd(0, 0, 2, 0), pattern: `$`, want: [][]int{{2, 2}}},
		{name: "$ at the end of a line-wise range", content: "ab\ncd", r: fwd(0, 0, 0, 1), pattern: `$`, want: [][]int{{2, 2}}},
		{name: `\b sees the character before the range`, content: "foobar", r: fwd(3, 0, 6, 0), pattern: `\bbar`, want: nil},
		{name: `\b at the range end sees the character after it`, content: "foobar", r: fwd(0, 0, 3, 0), pattern: `foo\b`, want: nil},
		{name: `\b at a real boundary`, content: "foo bar", r: fwd(4, 0, 7, 0), pattern: `\bbar\b`, want: [][]int{{4, 7}}},
		{name: `\A only at the document start`, content: "ab\ncd", r: fwd(0, 1, 2, 1), pattern: `\Ac`, want: nil},
		{name: `\A at the document start`, content: "ab", r: fwd(0, 0, 2, 0), pattern: `\Aa`, want: [][]int{{0, 1}}},
		{name: "a match starting before the range is dropped", content: "aab", r: fwd(1, 0, 3, 0), pattern: `\ba+`, want: nil},
		{name: "a point has only empty matches", content: "abc", r: point(xy(1, 0)), pattern: `x*`, want: [][]int{{1, 1}}},
		{name: "a point matches nothing else", content: "abc", r: point(xy(1, 0)), pattern: `b`, want: nil},
		{name: "a backward range is searched forward", content: "abc", r: bwd(0, 0, 3, 0), pattern: `b`, want: [][]int{{1, 2}}},
		{name: "a range past the document is clamped", content: "ab", r: fwd(0, 0, 9, 9), pattern: `b`, want: [][]int{{1, 2}}},
		{name: "a range before the document is clamped", content: "ab", r: rng{anchor: xy(-3, -1), head: xy(1, 0)}, pattern: `a`, want: [][]int{{0, 1}}},
		{name: "a match across a line ending", content: "ab\ncd", r: fwd(0, 0, 2, 1), pattern: `b\nc`, want: [][]int{{1, 4}}},
		{name: "empty matches at every position", content: "ab", r: fwd(0, 0, 2, 0), pattern: `x*`, want: [][]int{{0, 0}, {1, 1}, {2, 2}}},
		{name: "an empty match after a match is not repeated", content: "aab", r: fwd(0, 0, 3, 0), pattern: `a*`, want: [][]int{{0, 2}, {3, 3}}},
		{name: "wide glyphs are matched by their bytes", content: "世界", r: fwd(0, 0, 2, 0), pattern: `界`, want: [][]int{{3, 6}}},
		{name: "the context is one wide glyph", content: "世a界", r: fwd(1, 0, 2, 0), pattern: `^a$`, want: nil},
		{name: "the context is a combining cell", content: "e\u0301a", r: fwd(1, 0, 2, 0), pattern: `^a`, want: nil},
		{name: "an empty document", content: "", r: point(xy(0, 0)), pattern: `.*`, want: [][]int{{0, 0}}},
		{name: "an empty document with an anchor", content: "", r: point(xy(0, 0)), pattern: `^$`, want: [][]int{{0, 0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, buf, _ := newHelix(t, tc.content, term.Coordinates{})
			assert.Equal(t, tc.want, newMatcher(indexDoc(buf), mustCompile(t, tc.pattern)).spans(tc.r))
		})
	}
}

// TestSelectOnMatches pins selection::select_on_matches over a set.
func TestSelectOnMatches(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		sel         selection
		pattern     string
		want        []rng
		wantPrimary int
		wantOK      bool
	}{
		{name: "every match in every range", content: "foo bar\nbaz", sel: single(fwd(0, 0, 3, 1)), pattern: `\w+`,
			want: []rng{fwd(0, 0, 3, 0), fwd(4, 0, 7, 0), fwd(0, 1, 3, 1)}, wantOK: true},
		{name: "matches are gathered per range", content: "ab ab", sel: selection{ranges: []rng{fwd(0, 0, 2, 0), fwd(3, 0, 5, 0)}},
			pattern: `b`, want: []rng{fwd(1, 0, 2, 0), fwd(4, 0, 5, 0)}, wantOK: true},
		{name: "the primary becomes the first match", content: "a b c", sel: single(fwd(0, 0, 5, 0)), pattern: `\w`,
			want: []rng{fwd(0, 0, 1, 0), fwd(2, 0, 3, 0), fwd(4, 0, 5, 0)}, wantOK: true},
		{name: "the primary is the first match found, wherever it sorts", content: "ab ab",
			sel:     selection{ranges: []rng{fwd(3, 0, 5, 0), fwd(0, 0, 2, 0)}, primary: 1},
			pattern: `b`, want: []rng{fwd(1, 0, 2, 0), fwd(4, 0, 5, 0)}, wantPrimary: 1, wantOK: true},
		{name: "matches from overlapping ranges merge", content: "abcd", sel: selection{ranges: []rng{fwd(0, 0, 3, 0), fwd(1, 0, 4, 0)}},
			pattern: `\w+`, want: []rng{fwd(0, 0, 4, 0)}, wantOK: true},
		{name: "the direction of the range is not kept", content: "abc", sel: single(bwd(0, 0, 3, 0)), pattern: `b`,
			want: []rng{fwd(1, 0, 2, 0)}, wantOK: true},
		{name: "a point at the range end is an anchor outside it", content: "ab", sel: single(fwd(0, 0, 2, 0)), pattern: `$`,
			wantOK: false},
		{name: "a point before the range end stays", content: "ab\ncd", sel: single(fwd(0, 0, 0, 1)), pattern: `$`,
			want: []rng{point(xy(2, 0))}, wantOK: true},
		{name: "empty matches inside become points", content: "ab", sel: single(fwd(0, 0, 2, 0)), pattern: `x*`,
			want: []rng{point(xy(0, 0)), point(xy(1, 0))}, wantOK: true},
		{name: "a point range only matches the anchor outside it", content: "abc", sel: single(point(xy(1, 0))), pattern: `x*`,
			wantOK: false},
		{name: "nothing found", content: "abc", sel: single(fwd(0, 0, 3, 0)), pattern: `z`, wantOK: false},
		{name: "an empty document", content: "", sel: single(point(xy(0, 0))), pattern: `.*`, wantOK: false},
		{name: "^ on a partial line finds nothing", content: "ab cd", sel: single(fwd(3, 0, 5, 0)), pattern: `^`, wantOK: false},
		{name: "^ on every line", content: "a\nb\nc", sel: single(fwd(0, 0, 1, 2)), pattern: `^`,
			want: []rng{point(xy(0, 0)), point(xy(0, 1)), point(xy(0, 2))}, wantOK: true},
		{name: "a match across the line ending", content: "ab\ncd", sel: single(fwd(0, 0, 2, 1)), pattern: `b\nc`,
			want: []rng{fwd(1, 0, 1, 1)}, wantOK: true},
		{name: "a combining mark is inside its cell", content: "e\u0301x", sel: single(fwd(0, 0, 2, 0)), pattern: `\S+`,
			want: []rng{fwd(0, 0, 2, 0)}, wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, buf, _ := newHelix(t, tc.content, term.Coordinates{})
			before := tc.sel.clone()
			got, ok := selectOnMatches(newMatcher(indexDoc(buf), mustCompile(t, tc.pattern)), tc.sel)
			assert.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				assert.True(t, got.equal(before), "the selection is left as it was")
				return
			}
			assert.Equal(t, tc.want, got.ranges)
			assert.Equal(t, tc.wantPrimary, got.primary)
		})
	}
}

// TestSplitOnMatches pins selection::split_on_matches over a set.
func TestSplitOnMatches(t *testing.T) {
	cases := []struct {
		name        string
		content     string
		sel         selection
		pattern     string
		want        []rng
		wantPrimary int
	}{
		{name: "the pieces between matches", content: "a,b,c", sel: single(fwd(0, 0, 5, 0)), pattern: `,`,
			want: []rng{fwd(0, 0, 1, 0), fwd(2, 0, 3, 0), fwd(4, 0, 5, 0)}},
		{name: "a leading match leaves an empty first piece", content: ",a", sel: single(fwd(0, 0, 2, 0)), pattern: `,`,
			want: []rng{point(xy(0, 0)), fwd(1, 0, 2, 0)}},
		{name: "a trailing match leaves no tail", content: "a,", sel: single(fwd(0, 0, 2, 0)), pattern: `,`,
			want: []rng{fwd(0, 0, 1, 0)}},
		{name: "adjacent matches leave an empty piece between them", content: "a,,b", sel: single(fwd(0, 0, 4, 0)), pattern: `,`,
			want: []rng{fwd(0, 0, 1, 0), point(xy(2, 0)), fwd(3, 0, 4, 0)}},
		{name: "no match keeps the range", content: "abc", sel: single(fwd(0, 0, 3, 0)), pattern: `,`,
			want: []rng{fwd(0, 0, 3, 0)}},
		{name: "no match on a backward range makes it forward", content: "abc", sel: single(bwd(0, 0, 3, 0)), pattern: `,`,
			want: []rng{fwd(0, 0, 3, 0)}},
		{name: "a point stays as it is and keeps the primary", content: "a,b",
			sel: selection{ranges: []rng{point(xy(1, 0)), fwd(0, 0, 3, 0)}}, pattern: `,`,
			want: []rng{fwd(0, 0, 1, 0), point(xy(1, 0)), fwd(2, 0, 3, 0)}, wantPrimary: 1},
		{name: "a match covering the range leaves a point at its start", content: "abc", sel: single(fwd(0, 0, 3, 0)), pattern: `.*`,
			want: []rng{point(xy(0, 0))}},
		{name: "empty matches split every cell", content: "abc", sel: single(fwd(0, 0, 3, 0)), pattern: `x*`,
			want: []rng{fwd(0, 0, 1, 0), fwd(1, 0, 2, 0), fwd(2, 0, 3, 0)}},
		{name: "across lines", content: "ab\ncd", sel: single(fwd(0, 0, 2, 1)), pattern: `\n`,
			want: []rng{fwd(0, 0, 2, 0), fwd(0, 1, 2, 1)}},
		{name: "^ splits at line starts", content: "ab\ncd", sel: single(fwd(0, 0, 2, 1)), pattern: `^`,
			want: []rng{fwd(0, 0, 0, 1), fwd(0, 1, 2, 1)}},
		{name: "every range is split", content: "a,b\nc,d", sel: selection{ranges: []rng{fwd(0, 0, 3, 0), fwd(0, 1, 3, 1)}, primary: 1}, pattern: `,`,
			want: []rng{fwd(0, 0, 1, 0), fwd(2, 0, 3, 0), fwd(0, 1, 1, 1), fwd(2, 1, 3, 1)}},
		{name: "an empty document", content: "", sel: single(point(xy(0, 0))), pattern: `,`,
			want: []rng{point(xy(0, 0))}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, buf, _ := newHelix(t, tc.content, term.Coordinates{})
			got := splitOnMatches(newMatcher(indexDoc(buf), mustCompile(t, tc.pattern)), tc.sel)
			assert.Equal(t, tc.want, got.ranges)
			assert.Equal(t, tc.wantPrimary, got.primary)
		})
	}
}

// TestKeepOrRemoveMatches pins selection::keep_or_remove_matches.
func TestKeepOrRemoveMatches(t *testing.T) {
	perLine := selection{ranges: []rng{fwd(0, 0, 3, 0), fwd(0, 1, 3, 1), fwd(0, 2, 3, 2)}, primary: 2}
	cases := []struct {
		name    string
		content string
		sel     selection
		pattern string
		remove  bool
		want    []rng
		wantOK  bool
	}{
		{name: "keep the ranges that match", content: "foo\nbar\nfoo", sel: perLine, pattern: `foo`,
			want: []rng{fwd(0, 0, 3, 0), fwd(0, 2, 3, 2)}, wantOK: true},
		{name: "remove the ranges that match", content: "foo\nbar\nfoo", sel: perLine, pattern: `foo`, remove: true,
			want: []rng{fwd(0, 1, 3, 1)}, wantOK: true},
		{name: "keep with nothing matching", content: "foo\nbar\nfoo", sel: perLine, pattern: `z`, wantOK: false},
		{name: "remove with everything matching", content: "foo\nbar\nfoo", sel: perLine, pattern: `\w`, remove: true, wantOK: false},
		{name: "a partial match is enough", content: "foo\nbar\nfoo", sel: perLine, pattern: `a`,
			want: []rng{fwd(0, 1, 3, 1)}, wantOK: true},
		{name: "anchors see the text around the range", content: "foo\nbar\nfoo", sel: perLine, pattern: `^b`,
			want: []rng{fwd(0, 1, 3, 1)}, wantOK: true},
		{name: "a match must lie inside the range", content: "foobar", sel: single(fwd(0, 0, 3, 0)), pattern: `foob`, wantOK: false},
		{name: "the direction of a range is kept", content: "ab\ncd", sel: selection{ranges: []rng{bwd(0, 0, 2, 0), fwd(0, 1, 2, 1)}},
			pattern: `a`, want: []rng{bwd(0, 0, 2, 0)}, wantOK: true},
		{name: "a point matches an empty pattern", content: "abc", sel: single(point(xy(1, 0))), pattern: `x*`,
			want: []rng{point(xy(1, 0))}, wantOK: true},
		{name: "a point does not match a character", content: "abc", sel: single(point(xy(1, 0))), pattern: `b`, wantOK: false},
		{name: "smart case", content: "Foo\nfoo\nbar", sel: perLine, pattern: `Foo`,
			want: []rng{fwd(0, 0, 3, 0)}, wantOK: true},
		{name: "an empty document keeps its point on an empty pattern", content: "", sel: single(point(xy(0, 0))), pattern: `^$`,
			want: []rng{point(xy(0, 0))}, wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, buf, _ := newHelix(t, tc.content, term.Coordinates{})
			before := tc.sel.clone()
			got, ok := keepOrRemoveMatches(newMatcher(indexDoc(buf), mustCompile(t, tc.pattern)), tc.sel, tc.remove)
			assert.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				assert.True(t, got.equal(before), "the selection is left as it was")
				return
			}
			assert.Equal(t, tc.want, got.ranges)
			assert.Equal(t, 0, got.primary)
		})
	}
}

// TestSelectRegex pins s, the select_regex prompt, from the keyboard.
func TestSelectRegex(t *testing.T) {
	esc := esc()
	runOpSelCases(t, unchanged(
		opSelCase{name: "every match in every range becomes a range", content: "foo bar\nbaz",
			evs: cat(keys("%s"), prompt(`\w+`)), wantSels: []string{"foo", "bar", "baz"}, wantAt: xy(2, 0)},
		opSelCase{name: "^ puts a cursor on every line", content: "a\nb\nc",
			evs: cat(keys("%s"), prompt("^")), wantSels: []string{"a", "b", "c"}, wantAt: xy(0, 0)},
		opSelCase{name: "^ on a range starting inside a line finds nothing", content: "ab cd", at: xy(3, 0),
			evs: cat(keys("vl"), esc, keys("s"), prompt("^")), wantSels: []string{"cd"}, wantAt: xy(4, 0)},
		// $ matches before the line ending and again at the end of the
		// range's text; the second is the anchor outside the range and
		// is dropped, the first is a point on the line ending, which
		// the cursor parks on the last cell.
		opSelCase{name: "a match right at the end of the range is an anchor and is dropped", content: "ab\ncd",
			evs: cat(keys("xs"), prompt("$")), wantSels: []string{"\n"}, wantAt: xy(1, 0)},
		opSelCase{name: "$ on a range ending inside a line finds nothing", content: "ab cd",
			evs: cat(keys("vl"), esc, keys("s"), prompt("$")), wantSels: []string{"ab"}, wantAt: xy(1, 0)},
		opSelCase{name: `\b sees the text outside the range`, content: "foobar", at: xy(3, 0),
			evs: cat(keys("vll"), esc, keys("s"), prompt(`\bbar`)), wantSels: []string{"bar"}, wantAt: xy(5, 0)},
		opSelCase{name: `\n selects the line endings`, content: "ab\ncd\nef",
			evs: cat(keys("%s"), prompt(`\n`)), wantSels: []string{"\n", "\n"}, wantAt: xy(1, 0)},
		opSelCase{name: "a match spanning lines", content: "ab\ncd",
			evs: cat(keys("%s"), prompt(`b\nc`)), wantSels: []string{"b\nc"}, wantAt: xy(0, 1)},
		opSelCase{name: "lower case is case insensitive", content: "Foo foo",
			evs: cat(keys("%s"), prompt("foo")), wantSels: []string{"Foo", "foo"}, wantAt: xy(2, 0)},
		opSelCase{name: "an upper case letter makes it case sensitive", content: "Foo foo",
			evs: cat(keys("%s"), prompt("Foo")), wantSels: []string{"Foo"}, wantAt: xy(2, 0)},
		opSelCase{name: "matches are found per range", content: "foo bar\nfoo bar", at: xy(4, 0),
			evs: cat(keys("eCs"), prompt("a")), wantSels: []string{"a", "a"}, wantAt: xy(5, 0)},
		opSelCase{name: "the primary becomes the first match", content: "a b c", at: xy(4, 0),
			evs: cat(keys("%s"), prompt(`\w`)), wantSels: []string{"a", "b", "c"}, wantAt: xy(0, 0)},
		opSelCase{name: "a backward range yields forward matches", content: "abc", at: xy(2, 0),
			evs: cat(keys("vhh"), esc, keys("s"), prompt(".")), wantSels: []string{"a", "b", "c"}, wantAt: xy(0, 0)},
		opSelCase{name: "empty matches become one-cell ranges", content: "ab",
			evs: cat(keys("%s"), prompt("x*")), wantSels: []string{"a", "b"}, wantAt: xy(0, 0)},
		opSelCase{name: "select mode is kept", content: "abc",
			evs: cat(keys("v%s"), prompt("b")), wantSels: []string{"b"}, wantAt: xy(1, 0), wantSelect: true},
		opSelCase{name: "the prompt previews while typing", content: "foo bar", evs: keys("%sba"),
			wantSels: []string{"ba"}, wantAt: xy(5, 0)},
		opSelCase{name: "esc restores the selection", content: "foo bar",
			evs: cat(keys("%sba"), esc), wantSels: []string{"foo bar"}, wantAt: xy(6, 0)},
		opSelCase{name: "a pattern that matches nothing leaves the selection and reports", content: "foo bar",
			evs: cat(keys("%s"), prompt("zzz")), wantSels: []string{"foo bar"}, wantAt: xy(6, 0), wantMsg: "nothing selected"},
		opSelCase{name: "a pattern that does not compile leaves the selection and reports", content: "foo bar",
			evs: cat(keys("%s"), prompt("(")), wantSels: []string{"foo bar"}, wantAt: xy(6, 0), wantMsg: "error parsing regexp"},
		opSelCase{name: "runes wider than one column map one to one", content: "世界 x",
			evs: cat(keys("%s"), prompt(`\pL+`)), wantSels: []string{"世界", "x"}, wantAt: xy(1, 0)},
		opSelCase{name: "a combining mark stays in its cell", content: "e\u0301x y",
			evs: cat(keys("%s"), prompt(`\S+`)), wantSels: []string{"e\u0301x", "y"}, wantAt: xy(1, 0)},
		opSelCase{name: "a tab is one cell", content: "\tfoo bar",
			evs: cat(keys("%s"), prompt("bar")), wantSels: []string{"bar"}, wantAt: xy(7, 0)},
		opSelCase{name: "an empty document", content: "",
			evs: cat(keys("%s"), prompt(".*")), wantSels: []string{""}, wantAt: xy(0, 0), wantMsg: "nothing selected"},
		opSelCase{name: "a blank line", content: "a\n\nb", at: xy(0, 1),
			evs: cat(keys("s"), prompt("^")), wantSels: []string{"\n"}, wantAt: xy(0, 1)},
		opSelCase{name: "search disabled refuses the prompt", content: "foo bar", opts: []Option{WithSearch(false)},
			evs: keys("%s"), wantSels: []string{"foo bar"}, wantAt: xy(6, 0), wantHandled: new(false)},
	))

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

	t.Run("a preview is not a jump", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, cat(keys("%sba"), esc)...)
		_, handled := hx.Handle(modKey(term.ModCtrl, 'o'))
		assert.False(t, handled)
		assert.Equal(t, []string{"foo bar"}, sels(hx))
	})

	t.Run("u after s does not undo the selection", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, cat(keys("%s"), prompt("bar"), keys("u"))...)
		assert.Equal(t, "foo bar", buf.String())
		assert.Equal(t, []string{"bar"}, sels(hx))
	})
}

// TestSplitRegex pins S, the split_selection prompt, from the keyboard.
func TestSplitRegex(t *testing.T) {
	esc := esc()
	runOpSelCases(t, unchanged(
		opSelCase{name: "S splits every range on its matches", content: "a,b,c",
			evs: cat(keys("%S"), prompt(",")), wantSels: []string{"a", "b", "c"}, wantAt: xy(0, 0)},
		opSelCase{name: "S with no match keeps the range", content: "abc",
			evs: cat(keys("%S"), prompt(",")), wantSels: []string{"abc"}, wantAt: xy(2, 0)},
		opSelCase{name: "S on a backward range makes it forward", content: "abc", at: xy(2, 0),
			evs: cat(keys("vhh"), esc, keys("S"), prompt(",")), wantSels: []string{"abc"}, wantAt: xy(2, 0)},
		opSelCase{name: "a leading match leaves a cursor on it", content: ",a",
			evs: cat(keys("%S"), prompt(",")), wantSels: []string{",", "a"}, wantAt: xy(0, 0)},
		opSelCase{name: "a trailing match leaves no tail", content: "a,",
			evs: cat(keys("%S"), prompt(",")), wantSels: []string{"a"}, wantAt: xy(0, 0)},
		opSelCase{name: "adjacent matches leave a cursor between them", content: "a,,b",
			evs: cat(keys("%S"), prompt(",")), wantSels: []string{"a", ",", "b"}, wantAt: xy(0, 0)},
		opSelCase{name: "a match covering the range leaves a cursor at its start", content: "abc",
			evs: cat(keys("%S"), prompt(".*")), wantSels: []string{"a"}, wantAt: xy(0, 0)},
		opSelCase{name: "empty matches split every cell", content: "abc",
			evs: cat(keys("%S"), prompt("x*")), wantSels: []string{"a", "b", "c"}, wantAt: xy(0, 0)},
		opSelCase{name: `\n splits into lines`, content: "ab\ncd",
			evs: cat(keys("%S"), prompt(`\n`)), wantSels: []string{"ab", "cd"}, wantAt: xy(1, 0)},
		opSelCase{name: "^ splits at line starts", content: "ab\ncd",
			evs: cat(keys("%S"), prompt("^")), wantSels: []string{"ab\n", "cd"}, wantAt: xy(1, 0)},
		opSelCase{name: "every range is split and the primary resets", content: "a,b\nc,d", at: xy(0, 1),
			evs: cat(keys("%"), altKeys("s"), keys(")S"), prompt(",")), wantSels: []string{"a", "b", "c", "d"}, wantAt: xy(0, 0)},
		opSelCase{name: "smart case", content: "aXbxc",
			evs: cat(keys("%S"), prompt("x")), wantSels: []string{"a", "b", "c"}, wantAt: xy(0, 0)},
		opSelCase{name: "the prompt previews while typing", content: "a,b", evs: keys("%S,"),
			wantSels: []string{"a", "b"}, wantAt: xy(0, 0)},
		opSelCase{name: "esc restores the selection", content: "a,b",
			evs: cat(keys("%S,"), esc), wantSels: []string{"a,b"}, wantAt: xy(2, 0)},
		opSelCase{name: "a pattern that does not compile leaves the selection", content: "a,b",
			evs: cat(keys("%S"), prompt("[")), wantSels: []string{"a,b"}, wantAt: xy(2, 0), wantMsg: "error parsing regexp"},
		opSelCase{name: "select mode is kept", content: "a,b",
			evs: cat(keys("v%S"), prompt(",")), wantSels: []string{"a", "b"}, wantAt: xy(0, 0), wantSelect: true},
		opSelCase{name: "an empty document", content: "",
			evs: cat(keys("%S"), prompt(",")), wantSels: []string{""}, wantAt: xy(0, 0)},
		opSelCase{name: "search disabled refuses the prompt", content: "a,b", opts: []Option{WithSearch(false)},
			evs: keys("%S"), wantSels: []string{"a,b"}, wantAt: xy(2, 0), wantHandled: new(false)},
	))

	t.Run("the pattern goes to the / register", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a,b", term.Coordinates{})
		send(t, hx, cat(keys("%S"), prompt(","))...)
		assert.Equal(t, ",", registerText(t, hx, '/'))
	})
}

// TestKeepRemoveRegex pins K and A-K, the keep and remove prompts,
// from the keyboard.
func TestKeepRemoveRegex(t *testing.T) {
	perLine := cat(keys("%"), altKeys("s"))
	runOpSelCases(t, unchanged(
		opSelCase{name: "K keeps the ranges that match", content: "foo\nbar\nfoo",
			evs: cat(perLine, keys("K"), prompt("foo")), wantSels: []string{"foo", "foo"}, wantAt: xy(2, 0)},
		opSelCase{name: "A-K removes the ranges that match", content: "foo\nbar\nfoo",
			evs: cat(perLine, altKeys("K"), prompt("foo")), wantSels: []string{"bar"}, wantAt: xy(2, 1)},
		opSelCase{name: "K with nothing left keeps the set and reports", content: "foo\nbar",
			evs: cat(perLine, keys("K"), prompt("zzz")), wantSels: []string{"foo", "bar"}, wantAt: xy(2, 0),
			wantMsg: "no selections remaining"},
		opSelCase{name: "A-K with nothing left keeps the set and reports", content: "foo\nbar",
			evs: cat(perLine, altKeys("K"), prompt(`\w`)), wantSels: []string{"foo", "bar"}, wantAt: xy(2, 0),
			wantMsg: "no selections remaining"},
		opSelCase{name: "K on a single matching range", content: "abc",
			evs: cat(keys("K"), prompt("a")), wantSels: []string{"a"}, wantAt: xy(0, 0)},
		opSelCase{name: "K on a single range that does not match", content: "abc",
			evs: cat(keys("K"), prompt("z")), wantSels: []string{"a"}, wantAt: xy(0, 0), wantMsg: "no selections remaining"},
		opSelCase{name: "the primary moves to the first survivor", content: "a\nb\nc",
			evs: cat(perLine, keys(")K"), prompt("[ac]")), wantSels: []string{"a", "c"}, wantAt: xy(0, 0)},
		opSelCase{name: "anchors see the text around a range", content: "foo\nbar",
			evs: cat(perLine, keys("K"), prompt("^b")), wantSels: []string{"bar"}, wantAt: xy(2, 1)},
		opSelCase{name: "smart case", content: "Foo\nfoo",
			evs: cat(perLine, keys("K"), prompt("Foo")), wantSels: []string{"Foo"}, wantAt: xy(2, 0)},
		opSelCase{name: "a partial match keeps the range", content: "foo\nbar",
			evs: cat(perLine, keys("K"), prompt("a")), wantSels: []string{"bar"}, wantAt: xy(2, 1)},
		opSelCase{name: "K previews while typing", content: "foo\nbar", evs: cat(perLine, keys("Kb")),
			wantSels: []string{"bar"}, wantAt: xy(2, 1)},
		opSelCase{name: "esc restores the set", content: "foo\nbar", evs: cat(perLine, keys("Kb"), esc()),
			wantSels: []string{"foo", "bar"}, wantPrimary: 0, wantAt: xy(2, 0)},
		opSelCase{name: "a pattern that does not compile leaves the set", content: "foo\nbar",
			evs: cat(perLine, keys("K"), prompt("(")), wantSels: []string{"foo", "bar"}, wantAt: xy(2, 0),
			wantMsg: "error parsing regexp"},
		opSelCase{name: "select mode is kept", content: "foo\nbar",
			evs: cat(perLine, keys("vK"), prompt("bar")), wantSels: []string{"bar"}, wantAt: xy(2, 1), wantSelect: true},
		opSelCase{name: "an empty document keeps its cursor", content: "",
			evs: cat(keys("K"), prompt("^$")), wantSels: []string{""}, wantAt: xy(0, 0)},
		opSelCase{name: "search disabled refuses K", content: "foo", opts: []Option{WithSearch(false)},
			evs: keys("K"), wantSels: []string{"f"}, wantAt: xy(0, 0), wantHandled: new(false)},
		opSelCase{name: "search disabled refuses A-K", content: "foo", opts: []Option{WithSearch(false)},
			evs: altKeys("K"), wantSels: []string{"f"}, wantAt: xy(0, 0), wantHandled: new(false)},
	))
}

// TestRegexPrompt pins the prompt itself: its labels, live preview,
// what esc, backspace and enter do, and the history in the / register.
func TestRegexPrompt(t *testing.T) {
	type promptCase struct {
		name         string
		content      string
		at           term.Coordinates
		opts         []Option
		evs          []term.Event
		wantSels     []string
		wantAt       term.Coordinates
		wantPrompt   bool
		wantKind     promptKind
		wantScreen   string
		wantRegister *string
		wantHandled  *bool
	}
	str := func(s string) *string { return &s }
	cases := []promptCase{
		{name: "s opens the select prompt", content: "foo", evs: keys("s"),
			wantSels: []string{"f"}, wantPrompt: true, wantKind: promptSelect, wantScreen: "select:"},
		{name: "S opens the split prompt", content: "foo", evs: keys("S"),
			wantSels: []string{"f"}, wantPrompt: true, wantKind: promptSplit, wantScreen: "split:"},
		{name: "K opens the keep prompt", content: "foo", evs: keys("K"),
			wantSels: []string{"f"}, wantPrompt: true, wantKind: promptKeep, wantScreen: "keep:"},
		{name: "A-K opens the remove prompt", content: "foo", evs: altKeys("K"),
			wantSels: []string{"f"}, wantPrompt: true, wantKind: promptRemove, wantScreen: "remove:"},
		{name: "/ opens the search prompt", content: "foo", evs: keys("/"),
			wantSels: []string{"f"}, wantPrompt: true, wantKind: promptSearch, wantScreen: "search:"},
		{name: "? opens the search prompt", content: "foo", evs: keys("?"),
			wantSels: []string{"f"}, wantPrompt: true, wantKind: promptSearch, wantScreen: "search:"},
		{name: "z/ opens the search prompt from view mode", content: "foo", evs: keys("z/"),
			wantSels: []string{"f"}, wantPrompt: true, wantKind: promptSearch, wantScreen: "search:"},
		{name: "the input follows the label", content: "foo bar", evs: keys("%sba"),
			wantSels: []string{"ba"}, wantAt: xy(5, 0), wantPrompt: true, wantKind: promptSelect, wantScreen: "select:ba"},
		{name: "a keystroke that breaks the pattern restores the snapshot", content: "foo bar", evs: keys("%sba("),
			wantSels: []string{"foo bar"}, wantAt: xy(6, 0), wantPrompt: true, wantKind: promptSelect},
		{name: "fixing the pattern previews again", content: "foo bar", evs: cat(keys("%sba("), backspace(1)),
			wantSels: []string{"ba"}, wantAt: xy(5, 0), wantPrompt: true, wantKind: promptSelect},
		{name: "deleting the input keeps the last preview", content: "foo bar", evs: cat(keys("%sba"), backspace(2)),
			wantSels: []string{"b"}, wantAt: xy(4, 0), wantPrompt: true, wantKind: promptSelect, wantScreen: "select:"},
		{name: "backspace stops at the label", content: "foo bar", evs: cat(keys("%s"), backspace(3), keys("ba")),
			wantSels: []string{"ba"}, wantAt: xy(5, 0), wantPrompt: true, wantKind: promptSelect, wantScreen: "select:ba"},
		{name: "esc leaves the prompt without touching the register", content: "foo bar", evs: cat(keys("%sba"), esc()),
			wantSels: []string{"foo bar"}, wantAt: xy(6, 0), wantRegister: str("")},
		{name: "enter leaves the prompt", content: "foo bar", evs: cat(keys("%s"), prompt("ba")),
			wantSels: []string{"ba"}, wantAt: xy(5, 0), wantRegister: str("ba")},
		{name: "enter writes the register even for a pattern that does not compile", content: "foo bar",
			evs:      cat(keys("%s"), prompt("(")),
			wantSels: []string{"foo bar"}, wantAt: xy(6, 0), wantRegister: str("("), wantScreen: "error parsing regexp"},
		{name: "enter on an empty line with no history does nothing", content: "ab", evs: cat(keys("/"), enter()),
			wantSels: []string{"a"}, wantRegister: str("")},
		{name: "enter on an empty line runs the last pattern", content: "ab ab",
			evs:      cat(keys("/"), prompt("ab"), keys("/"), enter()),
			wantSels: []string{"ab"}, wantAt: xy(1, 0), wantRegister: str("ab")},
		{name: "every prompt writes the register", content: "a,b", evs: cat(keys("%S"), prompt(",")),
			wantSels: []string{"a", "b"}, wantRegister: str(",")},
		{name: "the keep prompt writes the register", content: "ab", evs: cat(keys("K"), prompt("a")),
			wantSels: []string{"a"}, wantRegister: str("a")},
		{name: "search disabled refuses /", content: "foo", opts: []Option{WithSearch(false)}, evs: keys("/"),
			wantSels: []string{"f"}, wantHandled: new(false)},
		{name: "search disabled refuses ?", content: "foo", opts: []Option{WithSearch(false)}, evs: keys("?"),
			wantSels: []string{"f"}, wantHandled: new(false)},
		{name: "the count in the message bar gives way to the prompt's report", content: "a\nb",
			evs:      cat(keys("Cs"), prompt("zzz")),
			wantSels: []string{"a", "b"}, wantAt: xy(0, 1), wantScreen: "nothing selected"},
		{name: "a message from the last command survives the next event", content: "a b",
			evs:      cat(keys("*l")),
			wantSels: []string{" "}, wantAt: xy(1, 0), wantScreen: `register '/' set to '\ba\b'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at, tc.opts...)
			var handled bool
			for _, ev := range tc.evs {
				_, handled = hx.Handle(ev)
			}
			assert.Equal(t, tc.wantSels, sels(hx), "fragments")
			assert.Equal(t, tc.wantAt, hx.CursorAtScroll(), "caret")
			assert.Equal(t, tc.wantPrompt, hx.IsSearchMode(), "prompt open")
			assert.Equal(t, tc.wantKind, impl(hx).prompt.kind, "prompt kind")
			if tc.wantScreen != "" {
				assert.Contains(t, screen(t, hx), tc.wantScreen)
			}
			if tc.wantRegister != nil {
				got, _ := impl(hx).searchPattern()
				assert.Equal(t, *tc.wantRegister, got, "register /")
			}
			if tc.wantHandled != nil {
				assert.Equal(t, *tc.wantHandled, handled, "handled")
			}
			assertSelectionInvariants(t, hx)
		})
	}

	t.Run("SetNormalMode closes the prompt and restores nothing", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("%sba")...)
		require.True(t, hx.IsSearchMode())
		hx.SetNormalMode()
		assert.True(t, hx.IsNormalMode())
		assert.Equal(t, promptNone, impl(hx).prompt.kind)
		assert.Equal(t, []string{"ba"}, sels(hx), "the preview stays where it was")
	})

	t.Run("the prompt's cursor is the less prompt's", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		hx.Resize(60, 6)
		send(t, hx, keys("%sba")...)
		pos, _, visible := hx.Cursor()
		assert.True(t, visible)
		assert.Equal(t, xy(len("select:ba"), 5), pos)
	})

	t.Run("a prompt opened over several ranges restores all of them on esc", func(t *testing.T) {
		hx, _, _ := newHelix(t, "ab\ncd", term.Coordinates{})
		send(t, hx, cat(keys("Cs"), keys("b"), esc())...)
		assert.Equal(t, []string{"a", "c"}, sels(hx))
		assert.Equal(t, 1, primaryIdx(hx))
	})
}

// TestRegexSearch pins /, ?, n and N over the / register: the scan
// starts past the primary, wraps around the document, and the hit
// replaces the primary or joins the set in select mode.
func TestRegexSearch(t *testing.T) {
	esc := esc()
	runOpSelCases(t, unchanged(
		opSelCase{name: "/ selects the next match", content: "foo bar42",
			evs: cat(keys("/"), prompt(`\d+`)), wantSels: []string{"42"}, wantAt: xy(8, 0)},
		opSelCase{name: "/ starts after the primary so the text under the caret is skipped", content: "ab ab",
			evs: cat(keys("/"), prompt("ab")), wantSels: []string{"ab"}, wantAt: xy(4, 0)},
		opSelCase{name: "/ wraps around the document", content: "ab cd", at: xy(3, 0),
			evs: cat(keys("/"), prompt("ab")), wantSels: []string{"ab"}, wantAt: xy(1, 0)},
		opSelCase{name: "/ on the last match wraps to itself", content: "ab", at: xy(1, 0),
			evs: cat(keys("/"), prompt("b")), wantSels: []string{"b"}, wantAt: xy(1, 0)},
		opSelCase{name: "? selects the last match before the primary", content: "ab ab ab", at: xy(6, 0),
			evs: cat(keys("?"), prompt("ab")), wantSels: []string{"ab"}, wantAt: xy(4, 0)},
		opSelCase{name: "? wraps to the end", content: "ab cd",
			evs: cat(keys("?"), prompt("cd")), wantSels: []string{"cd"}, wantAt: xy(4, 0)},
		opSelCase{name: "n walks forward", content: "x a a a",
			evs: cat(keys("/"), prompt("a"), keys("n")), wantSels: []string{"a"}, wantAt: xy(4, 0)},
		opSelCase{name: "n wraps around", content: "x a a",
			evs: cat(keys("/"), prompt("a"), keys("nn")), wantSels: []string{"a"}, wantAt: xy(2, 0), wantMsg: "Wrapped around document"},
		opSelCase{name: "N walks backward and wraps", content: "x a a",
			evs: cat(keys("/"), prompt("a"), keys("N")), wantSels: []string{"a"}, wantAt: xy(4, 0), wantMsg: "Wrapped around document"},
		opSelCase{name: "N after n comes back", content: "x a a",
			evs: cat(keys("/"), prompt("a"), keys("nN")), wantSels: []string{"a"}, wantAt: xy(2, 0)},
		opSelCase{name: "n after ? walks forward", content: "ab ab ab", at: xy(6, 0),
			evs: cat(keys("?"), prompt("ab"), keys("n")), wantSels: []string{"ab"}, wantAt: xy(7, 0)},
		opSelCase{name: "a count walks several matches", content: "x a a a",
			evs: cat(keys("/"), prompt("a"), keys("2n")), wantSels: []string{"a"}, wantAt: xy(6, 0)},
		opSelCase{name: "a count keeps wrapping", content: "a a",
			evs: cat(keys("/"), prompt("a"), keys("3n")), wantSels: []string{"a"}, wantAt: xy(0, 0)},
		opSelCase{name: "a count on N", content: "x a a a", at: xy(6, 0),
			evs: cat(keys("?"), prompt("a"), keys("2N")), wantSels: []string{"a"}, wantAt: xy(6, 0)},
		opSelCase{name: "n with an empty register is not handled", content: "a a",
			evs: keys("n"), wantSels: []string{"a"}, wantAt: xy(0, 0), wantHandled: new(false)},
		opSelCase{name: "N with an empty register is not handled", content: "a a",
			evs: keys("N"), wantSels: []string{"a"}, wantAt: xy(0, 0), wantHandled: new(false)},
		opSelCase{name: "n with no match reports and is not handled", content: "foo",
			setup: func(_ *testing.T, hx *Helix, _ *cell.Buffer) { impl(hx).setSearchPattern("zzz") },
			evs:   keys("n"), wantSels: []string{"f"}, wantAt: xy(0, 0), wantHandled: new(false),
			wantMsg: "No more matches"},
		opSelCase{name: "n with a register that does not compile reports", content: "foo",
			setup: func(_ *testing.T, hx *Helix, _ *cell.Buffer) { impl(hx).setSearchPattern("(") },
			evs:   keys("n"), wantSels: []string{"f"}, wantAt: xy(0, 0), wantHandled: new(false),
			wantMsg: "Invalid regex"},
		opSelCase{name: "a search in select mode adds a range", content: "foo bar",
			evs: cat(keys("v/"), prompt("bar")), wantSels: []string{"f", "bar"}, wantPrimary: 1, wantAt: xy(6, 0), wantSelect: true},
		opSelCase{name: "? in select mode adds a range", content: "ab ab", at: xy(4, 0),
			evs: cat(keys("v?"), prompt("ab")), wantSels: []string{"ab", "b"}, wantPrimary: 0, wantAt: xy(1, 0), wantSelect: true},
		opSelCase{name: "n in select mode adds a range per match", content: "x a a a",
			evs: cat(keys("/"), prompt("a"), keys("vnn")), wantSels: []string{"a", "a", "a"}, wantPrimary: 2, wantAt: xy(6, 0), wantSelect: true},
		opSelCase{name: "N in select mode adds a range", content: "x a a",
			evs: cat(keys("/"), prompt("a"), keys("vN")), wantSels: []string{"a", "a"}, wantPrimary: 1, wantAt: xy(4, 0), wantSelect: true},
		opSelCase{name: "a count in select mode adds every match walked", content: "x a a a a",
			evs: cat(keys("/"), prompt("a"), keys("v2n")), wantSels: []string{"a", "a", "a"}, wantPrimary: 2, wantAt: xy(6, 0), wantSelect: true},
		opSelCase{name: "the hit takes the direction of the primary", content: "foo bar", at: xy(1, 0),
			evs: cat(keys("vh"), esc, keys("/"), prompt("bar")), wantSels: []string{"bar"}, wantAt: xy(4, 0)},
		opSelCase{name: "N with a backward primary", content: "ab ab", at: xy(4, 0),
			evs: cat(keys("vh"), esc, keys("/"), prompt("ab"), keys("N")), wantSels: []string{"ab"}, wantAt: xy(3, 0)},
		opSelCase{name: "an empty match at the start of the document is skipped", content: "foo", at: xy(1, 0),
			evs: cat(keys("/"), prompt("^")), wantSels: []string{"o"}, wantAt: xy(1, 0)},
		opSelCase{name: "an empty match on a line ending selects it", content: "ab\ncd",
			evs: cat(keys("/"), prompt("$")), wantSels: []string{"\n"}, wantAt: xy(1, 0)},
		opSelCase{name: "the pattern is smart case", content: "Foo foo",
			evs: cat(keys("/"), prompt("foo")), wantSels: []string{"foo"}, wantAt: xy(6, 0)},
		opSelCase{name: "an upper case letter in the pattern is exact", content: "Foo foo",
			evs: cat(keys("/"), prompt("Foo")), wantSels: []string{"Foo"}, wantAt: xy(2, 0)},
		opSelCase{name: "a pattern spanning lines", content: "ab\ncd",
			evs: cat(keys("/"), prompt(`b\nc`)), wantSels: []string{"b\nc"}, wantAt: xy(0, 1)},
		opSelCase{name: "wide glyphs", content: "世 a 界a",
			evs: cat(keys("/"), prompt("a"), keys("n")), wantSels: []string{"a"}, wantAt: xy(5, 0)},
		opSelCase{name: "a wide glyph as the pattern", content: "世 世",
			evs: cat(keys("/"), prompt("世")), wantSels: []string{"世"}, wantAt: xy(2, 0)},
		opSelCase{name: "a combining mark stays in its cell", content: "e\u0301a xa",
			evs: cat(keys("/"), prompt("a"), keys("n")), wantSels: []string{"a"}, wantAt: xy(4, 0)},
		opSelCase{name: "a match inside a grapheme covers its cell", content: "xe\u0301",
			evs: cat(keys("/"), prompt("e")), wantSels: []string{"e\u0301"}, wantAt: xy(1, 0)},
		opSelCase{name: "n in view mode", content: "a b a",
			evs: cat(keys("/"), prompt("a"), keys("zn")), wantSels: []string{"a"}, wantAt: xy(0, 0)},
		opSelCase{name: "N in view mode", content: "a b a",
			evs: cat(keys("/"), prompt("a"), keys("zN")), wantSels: []string{"a"}, wantAt: xy(0, 0)},
		opSelCase{name: "/ from view mode", content: "a b a",
			evs: cat(keys("z/"), prompt("b")), wantSels: []string{"b"}, wantAt: xy(2, 0)},
		opSelCase{name: "? from view mode", content: "a b a", at: xy(4, 0),
			evs: cat(keys("z?"), prompt("b")), wantSels: []string{"b"}, wantAt: xy(2, 0)},
		opSelCase{name: "an empty document", content: "",
			evs: cat(keys("/"), prompt("a")), wantSels: []string{""}, wantAt: xy(0, 0)},
		opSelCase{name: "Search arms a literal", content: "axb a.b",
			setup: func(_ *testing.T, hx *Helix, _ *cell.Buffer) { hx.Search("a.b") },
			evs:   keys("n"), wantSels: []string{"a.b"}, wantAt: xy(6, 0)},
		opSelCase{name: "Search with search disabled arms nothing", content: "a a", opts: []Option{WithSearch(false)},
			setup: func(_ *testing.T, hx *Helix, _ *cell.Buffer) { hx.Search("a") },
			evs:   keys("n"), wantSels: []string{"a"}, wantAt: xy(0, 0), wantHandled: new(false)},
	))

	t.Run("the pattern goes to the / register as typed", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar42", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt(`\d+`))...)
		assert.Equal(t, `\d+`, registerText(t, hx, '/'))
	})

	t.Run("Search escapes the literal", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a.b", term.Coordinates{})
		hx.Search("a.b")
		assert.Equal(t, `a\.b`, registerText(t, hx, '/'))
	})

	t.Run("the prompt previews the first match while typing and esc backs out", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("/ba")...)
		assert.Equal(t, []string{"ba"}, sels(hx))
		send(t, hx, namedKey(term.KeyEsc))
		assert.Equal(t, []string{"f"}, sels(hx))
	})

	t.Run("the preview searches from the snapshot every time", func(t *testing.T) {
		hx, _, _ := newHelix(t, "ab ab ab", term.Coordinates{})
		send(t, hx, keys("/ab")...)
		assert.Equal(t, xy(4, 0), hx.CursorAtScroll(), "the first match past the caret")
		send(t, hx, namedKey(term.KeyBackspace), key('b'))
		assert.Equal(t, xy(4, 0), hx.CursorAtScroll(), "retyping does not walk on")
	})

	t.Run("a confirmed search is a jump and n is another", func(t *testing.T) {
		hx, _, _ := newHelix(t, "x a a", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt("a"), keys("n"))...)
		require.Equal(t, xy(4, 0), hx.CursorAtScroll())
		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, xy(2, 0), hx.CursorAtScroll())
		send(t, hx, modKey(term.ModCtrl, 'o'))
		assert.Equal(t, xy(0, 0), hx.CursorAtScroll())
	})

	t.Run("a failed n is not a jump", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo", term.Coordinates{})
		impl(hx).setSearchPattern("zzz")
		send(t, hx, key('n'))
		_, handled := hx.Handle(modKey(term.ModCtrl, 'o'))
		assert.False(t, handled)
	})

	t.Run("the confirmed prompt does not report a wrap", func(t *testing.T) {
		hx, _, _ := newHelix(t, "ab", term.Coordinates{X: 1})
		send(t, hx, cat(keys("/"), prompt("a"))...)
		assert.NotContains(t, screen(t, hx), "Wrapped")
	})

	t.Run("the hit is centred in the view", func(t *testing.T) {
		var lines []string
		for i := range 40 {
			lines = append(lines, strings.Repeat("x", i%3))
		}
		lines = append(lines, "target")
		hx, _, _ := newHelix(t, strings.Join(lines, "\n"), term.Coordinates{})
		hx.Resize(20, 10)
		send(t, hx, cat(keys("/"), prompt("target"))...)
		assert.Equal(t, xy(5, 40), hx.CursorAtScroll())
		assert.Equal(t, xy(5, 40), impl(hx).cursor.CursorAtScroll())
		assert.Less(t, impl(hx).cursor.Coordinates().Y, 10, "the caret is inside the window")
	})
}

// TestSearchHighlight pins the highlight of every match of the / register.
func TestSearchHighlight(t *testing.T) {
	cases := []struct {
		name    string
		content string
		evs     []term.Event
		setup   func(hx *Helix)
		want    []textapi.Location
	}{
		{name: "every match", content: "a1\nb22", evs: cat(keys("/"), prompt(`\d+`)),
			want: []textapi.Location{{From: xy(1, 0), To: xy(2, 0)}, {From: xy(1, 1), To: xy(3, 1)}}},
		{name: "a match across rows is split per row", content: "ab\ncd", evs: cat(keys("/"), prompt(`b\nc`)),
			want: []textapi.Location{{From: xy(1, 0), To: xy(2, 0)}, {From: xy(0, 1), To: xy(1, 1)}}},
		{name: "empty matches are not lit", content: "ab\ncd", evs: cat(keys("/"), prompt("^")), want: nil},
		{name: "a match on a line ending is not lit", content: "ab\ncd", evs: cat(keys("/"), prompt(`\n`)), want: nil},
		{name: "the highlight follows the last pattern", content: "ab", evs: cat(keys("/"), prompt("a"), keys("/"), prompt("b")),
			want: []textapi.Location{{From: xy(1, 0), To: xy(2, 0)}}},
		{name: "* lights every occurrence", content: "foo x foo", evs: keys("e*"),
			want: []textapi.Location{{From: xy(0, 0), To: xy(3, 0)}, {From: xy(6, 0), To: xy(9, 0)}}},
		{name: "s lights its pattern", content: "ab", evs: cat(keys("%s"), prompt("b")),
			want: []textapi.Location{{From: xy(1, 0), To: xy(2, 0)}}},
		{name: "Search lights the literal", content: "a.b axb", setup: func(hx *Helix) { hx.Search("a.b") },
			want: []textapi.Location{{From: xy(0, 0), To: xy(3, 0)}}},
		{name: "a pattern that does not compile clears the highlight", content: "ab",
			setup: func(hx *Helix) { impl(hx).setSearchPattern("a"); impl(hx).setSearchPattern("(") }, want: nil},
		{name: "an empty pattern clears the highlight", content: "ab",
			setup: func(hx *Helix) { impl(hx).setSearchPattern("a"); impl(hx).setSearchPattern("") }, want: nil},
		{name: "wide glyphs", content: "世界 x", evs: cat(keys("/"), prompt("界")),
			want: []textapi.Location{{From: xy(1, 0), To: xy(2, 0)}}},
		{name: "esc from the prompt lights nothing", content: "ab", evs: cat(keys("/a"), esc()), want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, term.Coordinates{})
			if tc.setup != nil {
				tc.setup(hx)
			}
			send(t, hx, tc.evs...)
			attr := impl(hx).less.Scroll().ResultsAttr
			for i := range tc.want {
				tc.want[i].Attr = attr
			}
			if len(tc.want) == 0 {
				assert.Empty(t, searchLocations(hx))
				return
			}
			assert.Equal(t, tc.want, searchLocations(hx))
		})
	}
}

// TestSearchSelection pins * and A-*, which build the / register from
// every range.
func TestSearchSelection(t *testing.T) {
	esc := esc()
	cases := []struct {
		name         string
		content      string
		at           term.Coordinates
		sel          *selection
		opts         []Option
		evs          []term.Event
		wantRegister string
		wantSels     []string
		wantAt       term.Coordinates
		wantHandled  bool
	}{
		{name: "* wraps a word in boundaries", content: "foo bar", evs: keys("e*"),
			wantRegister: `\bfoo\b`, wantSels: []string{"foo"}, wantAt: xy(2, 0), wantHandled: true},
		{name: "* at the end of the document", content: "bar foo", at: xy(4, 0), evs: keys("e*"),
			wantRegister: `\bfoo\b`, wantSels: []string{"foo"}, wantAt: xy(6, 0), wantHandled: true},
		{name: "* joins the escaped fragments", content: "foo a.b\nbar foo a.b",
			sel: selOf(1, fwd(0, 0, 3, 0), fwd(4, 0, 7, 0)), evs: keys("*"),
			wantRegister: `\bfoo\b|\ba\.b\b`, wantSels: []string{"foo", "a.b"}, wantAt: xy(6, 0), wantHandled: true},
		{name: "A-* leaves the boundaries out", content: "foobar", evs: cat(keys("vll"), altKeys("*")),
			wantRegister: "foo", wantSels: []string{"foo"}, wantAt: xy(2, 0), wantHandled: true},
		{name: "* on a fragment inside a word gets no boundary on that side", content: "foobar", at: xy(1, 0), evs: keys("vl*"),
			wantRegister: "oo", wantSels: []string{"oo"}, wantAt: xy(2, 0), wantHandled: true},
		{name: "* on a fragment ending a word gets a boundary after it", content: "foobar", at: xy(3, 0), evs: keys("vll*"),
			wantRegister: `bar\b`, wantSels: []string{"bar"}, wantAt: xy(5, 0), wantHandled: true},
		{name: "* on punctuation gets no boundary on that side", content: "a.b a.b", at: xy(1, 0), evs: keys("vl*"),
			wantRegister: `\.b\b`, wantSels: []string{".b"}, wantAt: xy(2, 0), wantHandled: true},
		{name: "identical fragments are listed once", content: "foo\nfoo", evs: keys("eC*"),
			wantRegister: `\bfoo\b`, wantSels: []string{"foo", "foo"}, wantAt: xy(2, 1), wantHandled: true},
		{name: "a linewise range keeps its line ending", content: "foo\nbar", evs: keys("x*"),
			wantRegister: "\\bfoo\n", wantSels: []string{"foo\n"}, wantAt: xy(2, 0), wantHandled: true},
		{name: "a range over a line ending", content: "ab\ncd ab\ncd", evs: cat(keys("vj"), esc, keys("*")),
			wantRegister: "\\bab\nc", wantSels: []string{"ab\nc"}, wantAt: xy(0, 1), wantHandled: true},
		{name: "a backslash is escaped", content: `a\b`, evs: keys("vll*"),
			wantRegister: `\ba\\b\b`, wantSels: []string{`a\b`}, wantAt: xy(2, 0), wantHandled: true},
		{name: "a tab is literal", content: "\ta", evs: keys("*"),
			wantRegister: "\t", wantSels: []string{"\t"}, wantAt: xy(0, 0), wantHandled: true},
		{name: "a digit and an underscore are word characters", content: "x_1 y", evs: keys("e*"),
			wantRegister: `\bx_1\b`, wantSels: []string{"x_1"}, wantAt: xy(2, 0), wantHandled: true},
		// Go's \b only knows ASCII word characters, so a boundary next
		// to a wide glyph would never match; none is added.
		{name: "a wide glyph gets no boundary", content: "世 世", evs: keys("*"),
			wantRegister: "世", wantSels: []string{"世"}, wantAt: xy(0, 0), wantHandled: true},
		{name: "an accented letter gets no boundary", content: "é x", evs: keys("*"),
			wantRegister: "é", wantSels: []string{"é"}, wantAt: xy(0, 0), wantHandled: true},
		{name: "a blank line has nothing to search for", content: "a\n\nb", at: xy(0, 1), evs: keys("*"),
			wantRegister: "\n", wantSels: []string{"\n"}, wantAt: xy(0, 1), wantHandled: true},
		{name: "an empty document is not handled", content: "", evs: keys("*"),
			wantSels: []string{""}, wantAt: xy(0, 0)},
		{name: "search disabled refuses *", content: "foo", opts: []Option{WithSearch(false)}, evs: keys("*"),
			wantSels: []string{"f"}, wantAt: xy(0, 0)},
		{name: "search disabled refuses A-*", content: "foo", opts: []Option{WithSearch(false)}, evs: altKeys("*"),
			wantSels: []string{"f"}, wantAt: xy(0, 0)},
		{name: "a count is ignored", content: "foo bar", evs: keys("e3*"),
			wantRegister: `\bfoo\b`, wantSels: []string{"foo"}, wantAt: xy(2, 0), wantHandled: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, tc.at, tc.opts...)
			if tc.sel != nil {
				impl(hx).setSelection(tc.sel.clone())
			}
			var handled bool
			for _, ev := range tc.evs {
				_, handled = hx.Handle(ev)
			}
			assert.Equal(t, tc.wantHandled, handled, "handled")
			got, _ := impl(hx).searchPattern()
			assert.Equal(t, tc.wantRegister, got, "register /")
			assert.Equal(t, tc.wantSels, sels(hx), "the selection is untouched")
			assert.Equal(t, tc.wantAt, hx.CursorAtScroll(), "caret")
			assertSelectionInvariants(t, hx)
		})
	}

	t.Run("* reports the register and n follows it", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo a.b\nbar foo a.b", term.Coordinates{})
		impl(hx).setSelection(selOf(1, fwd(0, 0, 3, 0), fwd(4, 0, 7, 0)).clone())
		send(t, hx, key('*'))
		assert.Contains(t, screen(t, hx), `register '/' set to '\bfoo\b|\ba\.b\b'`)
		send(t, hx, key('n'))
		assert.Equal(t, []string{"foo", "foo"}, sels(hx))
		assert.Equal(t, xy(6, 1), hx.CursorAtScroll())
		send(t, hx, key('n'))
		assert.Equal(t, []string{"foo", "a.b"}, sels(hx))
	})

	t.Run("* on a wide glyph finds the next one", func(t *testing.T) {
		hx, _, _ := newHelix(t, "世 世", term.Coordinates{})
		send(t, hx, keys("*n")...)
		assert.Equal(t, xy(2, 0), hx.CursorAtScroll())
	})

	t.Run("* on a word inside a longer word does not match the longer word", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo foobar foo", term.Coordinates{})
		send(t, hx, keys("e*n")...)
		assert.Equal(t, xy(13, 0), hx.CursorAtScroll())
	})

	t.Run("A-* matches inside longer words", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo foobar foo", term.Coordinates{})
		send(t, hx, cat(keys("e"), altKeys("*"), keys("n"))...)
		assert.Equal(t, xy(6, 0), hx.CursorAtScroll())
	})
}

// TestVisualColumn pins the column the scroll draws a position on.
func TestVisualColumn(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		pos       term.Coordinates
		tabspaces int
		want      int
	}{
		{name: "plain text", content: "abc", pos: xy(2, 0), tabspaces: 4, want: 2},
		{name: "a tab takes its width", content: "\tb", pos: xy(1, 0), tabspaces: 4, want: 4},
		{name: "a tab of width one", content: "\tb", pos: xy(1, 0), tabspaces: 1, want: 1},
		{name: "a tab of width zero counts as one", content: "\tb", pos: xy(1, 0), tabspaces: 0, want: 1},
		{name: "a wide glyph takes two columns", content: "世b", pos: xy(1, 0), tabspaces: 4, want: 2},
		{name: "the line ending after a wide glyph", content: "世b", pos: xy(2, 0), tabspaces: 4, want: 3},
		{name: "a combining mark takes one column", content: "e\u0301b", pos: xy(1, 0), tabspaces: 4, want: 1},
		{name: "past the row", content: "ab", pos: xy(5, 0), tabspaces: 4, want: 5},
		{name: "a negative column", content: "ab", pos: xy(-2, 0), tabspaces: 4, want: 0},
		{name: "a row before the document", content: "ab", pos: xy(1, -1), tabspaces: 4, want: 0},
		{name: "a row past the document", content: "ab", pos: xy(1, 3), tabspaces: 4, want: 0},
		{name: "the second row", content: "\tab\n\t\tc", pos: xy(2, 1), tabspaces: 2, want: 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, buf, _ := newHelix(t, tc.content, term.Coordinates{})
			assert.Equal(t, tc.want, visualColumn(buf, tc.pos, tc.tabspaces))
		})
	}
}

// TestAlignSelections pins &, which pads the ranges of every row onto
// the same visual columns.
func TestAlignSelections(t *testing.T) {
	runOpSelCases(t, []opSelCase{
		{name: "one column on two rows", content: "a b\naaa b", sel: selOf(1, fwd(2, 0, 3, 0), fwd(4, 1, 5, 1)),
			evs: keys("&"), want: "a   b\naaa b", wantSels: []string{"b", "b"}, wantPrimary: 1, wantAt: xy(4, 1)},
		{name: "a later row pushes the earlier ones", content: "aaa b\na b", sel: selOf(0, fwd(4, 0, 5, 0), fwd(2, 1, 3, 1)),
			evs: keys("&"), want: "aaa b\na   b", wantSels: []string{"b", "b"}, wantAt: xy(4, 0)},
		{name: "three rows", content: "a b\naaa b\naa b", sel: selOf(0, fwd(2, 0, 3, 0), fwd(4, 1, 5, 1), fwd(3, 2, 4, 2)),
			evs: keys("&"), want: "a   b\naaa b\naa  b", wantSels: []string{"b", "b", "b"}, wantAt: xy(4, 0)},
		{name: "two columns", content: "a b c\naaa b c",
			sel: selOf(3, fwd(2, 0, 3, 0), fwd(4, 0, 5, 0), fwd(4, 1, 5, 1), fwd(6, 1, 7, 1)),
			evs: keys("&"), want: "a   b c\naaa b c", wantSels: []string{"b", "c", "b", "c"}, wantPrimary: 3, wantAt: xy(6, 1)},
		{name: "columns are padded independently", content: "a b c\naa b cccc c",
			sel: selOf(0, fwd(2, 0, 3, 0), fwd(4, 0, 5, 0), fwd(3, 1, 4, 1), fwd(10, 1, 11, 1)),
			evs: keys("&"), want: "a  b      c\naa b cccc c", wantSels: []string{"b", "c", "b", "c"}, wantAt: xy(3, 0)},
		{name: "ranges line up on their heads", content: "ab c\na bb c",
			sel: selOf(0, fwd(0, 0, 2, 0), fwd(3, 0, 4, 0), fwd(2, 1, 4, 1), fwd(5, 1, 6, 1)),
			evs: keys("&"), want: "  ab c\na bb c", wantSels: []string{"ab", "c", "bb", "c"}, wantAt: xy(3, 0)},
		{name: "backward ranges keep their direction", content: "a b\naaa b", sel: selOf(0, bwd(2, 0, 3, 0), bwd(4, 1, 5, 1)),
			evs: keys("&"), want: "a   b\naaa b", wantSels: []string{"b", "b"}, wantAt: xy(4, 0)},
		{name: "a tab counts for its visual width", content: "\tb\naaa b", sel: selOf(0, fwd(1, 0, 2, 0), fwd(4, 1, 5, 1)),
			evs: keys("&"), want: "\t  b\naaa b", wantSels: []string{"b", "b"}, wantAt: xy(3, 0)},
		{name: "a wide glyph counts for two columns", content: "世b\naaa b", sel: selOf(0, fwd(1, 0, 2, 0), fwd(4, 1, 5, 1)),
			evs: keys("&"), want: "世  b\naaa b", wantSels: []string{"b", "b"}, wantAt: xy(3, 0)},
		{name: "a row with fewer ranges only pads what it has", content: "a b c\naaa b",
			sel: selOf(0, fwd(2, 0, 3, 0), fwd(4, 0, 5, 0), fwd(4, 1, 5, 1)),
			evs: keys("&"), want: "a   b c\naaa b", wantSels: []string{"b", "c", "b"}, wantAt: xy(4, 0)},
		{name: "a single range needs no padding", content: "a b", at: xy(2, 0),
			evs: keys("&"), want: "a b", wantSels: []string{"b"}, wantAt: xy(2, 0)},
		{name: "select mode is left", content: "a b\naaa b", sel: selOf(0, fwd(2, 0, 3, 0), fwd(4, 1, 5, 1)),
			evs: keys("v&"), want: "a   b\naaa b", wantSels: []string{"b", "b"}, wantAt: xy(4, 0)},
		{name: "a range spanning rows is refused", content: "a b\nc d",
			evs: keys("vj&"), want: "a b\nc d", wantSels: []string{"a b\nc"}, wantAt: xy(0, 1), wantSelect: true,
			wantHandled: new(false), wantMsg: "align cannot work with multi line selections"},
		{name: "a line-wise range is refused", content: "ab\ncd",
			evs: keys("x&"), want: "ab\ncd", wantSels: []string{"ab\n"}, wantAt: xy(1, 0),
			wantHandled: new(false), wantMsg: "align cannot work with multi line selections"},
		{name: "one multi-line range among many refuses the lot", content: "a b\nc d\ne f",
			sel: selOf(0, fwd(2, 0, 3, 0), fwd(2, 1, 3, 2)),
			evs: keys("&"), want: "a b\nc d\ne f", wantSels: []string{"b", "d\ne f"}, wantAt: xy(2, 0), wantHandled: new(false)},
		{name: "an empty document", content: "", evs: keys("&"), want: "", wantSels: []string{""}, wantAt: xy(0, 0)},
		{name: "undo restores the text in one step", content: "a b\naaa b", sel: selOf(0, fwd(2, 0, 3, 0), fwd(4, 1, 5, 1)),
			evs: keys("&u"), want: "a b\naaa b", wantSels: []string{"b", "b"}, wantAt: xy(2, 0)},
		{name: "ranges after a padded one on the same row are shifted", content: "a b c\naaa b c",
			sel: selOf(1, fwd(2, 0, 3, 0), fwd(4, 0, 5, 0), fwd(4, 1, 5, 1), fwd(6, 1, 7, 1)),
			evs: keys("&"), want: "a   b c\naaa b c", wantSels: []string{"b", "c", "b", "c"}, wantPrimary: 1, wantAt: xy(6, 0)},
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
		{name: "the primary follows its text", content: "a b c", sel: selOf(2, fwd(0, 0, 1, 0), fwd(2, 0, 3, 0), fwd(4, 0, 5, 0)),
			evs: altKeys(")"), want: "c a b", wantSels: []string{"c", "a", "b"}, wantPrimary: 0, wantAt: xy(0, 0)},
		{name: "a count rotates further", content: "a b c", sel: three,
			evs: cat(keys("2"), altKeys(")")), want: "b c a", wantSels: []string{"b", "c", "a"}, wantPrimary: 2, wantAt: xy(4, 0)},
		{name: "a count backward", content: "a b c", sel: three,
			evs: cat(keys("2"), altKeys("(")), want: "c a b", wantSels: []string{"c", "a", "b"}, wantPrimary: 1, wantAt: xy(2, 0)},
		{name: "a count equal to the number of ranges is a full turn", content: "a b c", sel: three,
			evs: cat(keys("3"), altKeys(")")), want: "a b c", wantSels: []string{"a", "b", "c"}, wantAt: xy(0, 0)},
		{name: "a count past the number of ranges is clamped to a full turn", content: "a b c", sel: three,
			evs: cat(keys("5"), altKeys(")")), want: "a b c", wantSels: []string{"a", "b", "c"}, wantAt: xy(0, 0)},
		{name: "rotating twice", content: "a b c", sel: three,
			evs: altKeys("(("), want: "c a b", wantSels: []string{"c", "a", "b"}, wantPrimary: 1, wantAt: xy(2, 0)},
		{name: "forward then back is the identity", content: "a b c", sel: three,
			evs: altKeys(")("), want: "a b c", wantSels: []string{"a", "b", "c"}, wantAt: xy(0, 0)},
		{name: "two ranges swap either way", content: "a b", sel: selOf(0, fwd(0, 0, 1, 0), fwd(2, 0, 3, 0)),
			evs: altKeys("("), want: "b a", wantSels: []string{"b", "a"}, wantPrimary: 1, wantAt: xy(2, 0)},
		{name: "fragments of different lengths reshape the ranges", content: "aa b",
			sel: selOf(0, fwd(0, 0, 2, 0), fwd(3, 0, 4, 0)),
			evs: altKeys(")"), want: "b aa", wantSels: []string{"b", "aa"}, wantPrimary: 1, wantAt: xy(3, 0)},
		{name: "backward ranges keep their direction", content: "a b c",
			sel: selOf(0, bwd(0, 0, 1, 0), bwd(2, 0, 3, 0), bwd(4, 0, 5, 0)),
			evs: altKeys(")"), want: "c a b", wantSels: []string{"c", "a", "b"}, wantPrimary: 1, wantAt: xy(2, 0)},
		{name: "fragments across rows", content: "a\nb\nc", sel: selOf(0, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1), fwd(0, 2, 1, 2)),
			evs: altKeys(")"), want: "c\na\nb", wantSels: []string{"c", "a", "b"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "fragments holding line endings", content: "a\nb\nc", sel: selOf(0, fwd(0, 0, 0, 1), fwd(0, 1, 0, 2), fwd(0, 2, 1, 2)),
			evs: altKeys(")"), want: "ca\nb\n", wantSels: []string{"c", "a\n", "b\n"}, wantPrimary: 1, wantAt: xy(1, 0)},
		{name: "multi-row fragments", content: "ab\ncd\nef\ngh", sel: selOf(0, fwd(0, 0, 1, 1), fwd(0, 2, 1, 3)),
			evs: altKeys(")"), want: "ef\ngd\nab\nch", wantSels: []string{"ef\ng", "ab\nc"}, wantPrimary: 1, wantAt: xy(0, 3)},
		{name: "wide glyphs", content: "世 a", sel: selOf(0, fwd(0, 0, 1, 0), fwd(2, 0, 3, 0)),
			evs: altKeys(")"), want: "a 世", wantSels: []string{"a", "世"}, wantPrimary: 1, wantAt: xy(2, 0)},
		{name: "a combining mark travels with its base", content: "e\u0301 a", sel: selOf(0, fwd(0, 0, 1, 0), fwd(2, 0, 3, 0)),
			evs: altKeys(")"), want: "a e\u0301", wantSels: []string{"a", "e\u0301"}, wantPrimary: 1, wantAt: xy(2, 0)},
		{name: "an empty fragment", content: "a b", sel: selOf(0, fwd(0, 0, 1, 0), point(xy(3, 0))),
			evs: altKeys(")"), want: " ba", wantSels: []string{" ", "a"}, wantPrimary: 1, wantAt: xy(2, 0)},
		{name: "select mode stays", content: "a b", sel: selOf(0, fwd(0, 0, 1, 0), fwd(2, 0, 3, 0)),
			evs: cat(keys("v"), altKeys(")")), want: "b a", wantSels: []string{"b", "a"}, wantPrimary: 1, wantAt: xy(2, 0), wantSelect: true},
		{name: "undo restores the text in one step", content: "a b c", sel: three,
			evs: cat(altKeys(")"), keys("u")), want: "a b c", wantSels: []string{"a", "b", "c"}, wantAt: xy(0, 0)},
		{name: "a single range is left alone", content: "abc", evs: altKeys(")"),
			want: "abc", wantSels: []string{"a"}, wantAt: xy(0, 0), wantHandled: new(false)},
		{name: "an empty document", content: "", evs: altKeys("("),
			want: "", wantSels: []string{""}, wantAt: xy(0, 0), wantHandled: new(false)},
	})
}

// TestDocCache pins that the document index and the match list are
// reused between edits and dropped by anything that changes the text:
// an edit the handler makes, one made behind its back, a new pattern,
// and a reload that never told the buffer's subscribers.
func TestDocCache(t *testing.T) {
	matchesOf := func(hx *Helix) [][]int { return impl(hx).docs.matches }

	t.Run("n reuses the index and the matches", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a x a x a", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt("a"))...)
		first := matchesOf(hx)
		require.Len(t, first, 3)
		text := impl(hx).doc().text
		send(t, hx, keys("nN")...)
		assert.Same(t, &first[0][0], &matchesOf(hx)[0][0], "the same match list served every n")
		assert.Equal(t, text, impl(hx).doc().text)
		assert.Equal(t, xy(4, 0), hx.CursorAtScroll())
	})

	t.Run("enter after a preview scans nothing again", func(t *testing.T) {
		hx, _, _ := newHelix(t, "foo bar", term.Coordinates{})
		send(t, hx, keys("/bar")...)
		preview := matchesOf(hx)
		require.Len(t, preview, 1)
		send(t, hx, namedKey(term.KeyEnter))
		assert.Same(t, &preview[0][0], &matchesOf(hx)[0][0])
		assert.Equal(t, []string{"bar"}, sels(hx))
	})

	t.Run("a new pattern replaces the matches but keeps the index", func(t *testing.T) {
		hx, _, _ := newHelix(t, "ab ab", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt("a"))...)
		text := impl(hx).doc().text
		send(t, hx, cat(keys("/"), prompt("b"))...)
		assert.Equal(t, "(?mi)b", impl(hx).docs.matched)
		assert.Equal(t, [][]int{{1, 2}, {4, 5}}, matchesOf(hx))
		assert.Equal(t, text, impl(hx).doc().text)
	})

	t.Run("smart case tells two spellings apart", func(t *testing.T) {
		hx, _, _ := newHelix(t, "Foo foo", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt("foo"))...)
		require.Len(t, matchesOf(hx), 2)
		send(t, hx, cat(keys("/"), prompt("Foo"))...)
		assert.Len(t, matchesOf(hx), 1)
	})

	t.Run("an edit by the handler drops the cache", func(t *testing.T) {
		hx, _, _ := newHelix(t, "a x a", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt("a"), keys("ggdn"))...)
		assert.Equal(t, [][]int{{3, 4}}, matchesOf(hx), "the deleted a is gone from the matches")
		assert.Equal(t, " x a", impl(hx).doc().text)
		assert.Equal(t, xy(3, 0), hx.CursorAtScroll())
	})

	t.Run("an edit made behind the handler's back drops the cache", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a x a", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt("a"))...)
		require.Len(t, matchesOf(hx), 2)
		buf.InsertString(xy(5, 0), " a")
		send(t, hx, key('n'))
		assert.Len(t, matchesOf(hx), 3)
		assert.Equal(t, xy(6, 0), hx.CursorAtScroll())
	})

	t.Run("a reload that bypasses the subscribers drops the cache when the rows change", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "a x a", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt("a"))...)
		require.Len(t, matchesOf(hx), 2)
		buf.ResetCells(term.StringToCells("x\na"))
		send(t, hx, key('n'))
		assert.Equal(t, "x\na", impl(hx).doc().text)
		assert.Len(t, matchesOf(hx), 1)
		assert.Equal(t, xy(0, 1), hx.CursorAtScroll())
	})

	t.Run("the index never holds the rows", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "ab", term.Coordinates{})
		d := impl(hx).doc()
		buf.InsertString(xy(0, 0), "x")
		assert.Equal(t, 'x', d.row(0)[0].Ch, "the row is read from the buffer as it is now")
	})

	t.Run("a pattern armed after an edit sees the new text", func(t *testing.T) {
		hx, _, _ := newHelix(t, "ab x ab", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt("ab"), keys("ggd*"))...)
		attr := impl(hx).less.Scroll().ResultsAttr
		assert.Equal(t, `\bb\b`, registerText(t, hx, '/'))
		assert.Equal(t, []textapi.Location{{From: xy(0, 0), To: xy(1, 0), Attr: attr}}, searchLocations(hx))
	})

	t.Run("the highlight of a match ending inside a grapheme covers its cell", func(t *testing.T) {
		hx, _, _ := newHelix(t, "xe\u0301", term.Coordinates{})
		send(t, hx, cat(keys("/"), prompt("e"))...)
		attr := impl(hx).less.Scroll().ResultsAttr
		assert.Equal(t, []textapi.Location{{From: xy(1, 0), To: xy(2, 0), Attr: attr}}, searchLocations(hx))
	})
}

// TestMatchLookup pins the binary searches over the match list.
func TestMatchLookup(t *testing.T) {
	all := [][]int{{0, 2}, {3, 3}, {5, 8}, {10, 11}}
	cases := []struct {
		start      int
		wantFirst  []int
		wantBefore []int
	}{
		{start: -1, wantFirst: []int{0, 2}, wantBefore: nil},
		{start: 0, wantFirst: []int{0, 2}, wantBefore: nil},
		{start: 1, wantFirst: []int{3, 3}, wantBefore: nil},
		{start: 2, wantFirst: []int{3, 3}, wantBefore: []int{0, 2}},
		{start: 3, wantFirst: []int{3, 3}, wantBefore: []int{3, 3}},
		{start: 4, wantFirst: []int{5, 8}, wantBefore: []int{3, 3}},
		{start: 8, wantFirst: []int{10, 11}, wantBefore: []int{5, 8}},
		{start: 11, wantFirst: nil, wantBefore: []int{10, 11}},
		{start: 99, wantFirst: nil, wantBefore: []int{10, 11}},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.start), func(t *testing.T) {
			assert.Equal(t, tc.wantFirst, firstMatchFrom(all, tc.start), "first from")
			assert.Equal(t, tc.wantBefore, lastMatchBefore(all, tc.start), "last before")
		})
	}
	assert.Nil(t, firstMatchFrom(nil, 0))
	assert.Nil(t, lastMatchBefore(nil, 0))
}

func largeDocument(lines int) string {
	var b strings.Builder
	for i := range lines {
		fmt.Fprintf(&b, "func f%d(a, b int) int {\n\treturn a + b + %d // foo bar baz\n}\n", i, i)
	}
	return b.String()
}

// BenchmarkSearchNext is n over a large document with the pattern
// already armed: the document must not be scanned again per press.
func BenchmarkSearchNext(b *testing.B) {
	hx, _, _ := newHelix(&testing.T{}, largeDocument(20000), term.Coordinates{})
	impl(hx).setSearchPattern("foo")
	b.ResetTimer()
	for b.Loop() {
		hx.Handle(key('n'))
	}
}

// BenchmarkSearchPrompt is the / prompt typing a pattern and
// confirming it, which scans once per keystroke and not on enter.
func BenchmarkSearchPrompt(b *testing.B) {
	hx, _, _ := newHelix(&testing.T{}, largeDocument(20000), term.Coordinates{})
	evs := cat(keys("/"), prompt("foo"))
	b.ResetTimer()
	for b.Loop() {
		send(&testing.T{}, hx, evs...)
	}
}
