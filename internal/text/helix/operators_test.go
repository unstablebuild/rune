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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/registerset"
)

// The operators over a selection set, from helix-term/src/commands.rs.
// Every table drives the same harness, which checks the buffer, the
// fragments, the primary, the caret and the mode, then the invariants
// every command must leave behind. Ranges are spelled in document
// space: fwd(x0, y0, x1, y1) covers [x0, x1) on one row facing forward,
// bwd the same facing backward, lines(y0, y1) the whole rows y0..y1.

func bwd(x0, y0, x1, y1 int) rng { return rng{anchor: xy(x1, y1), head: xy(x0, y0)} }

func lines(y0, y1 int) rng { return rng{anchor: xy(0, y0), head: xy(0, y1+1)} }

func ctrl(ch rune) term.Event { return modKey(term.ModCtrl, ch) }

func space() []term.Event { return []term.Event{namedKey(term.KeySpace)} }

func esc() []term.Event { return []term.Event{namedKey(term.KeyEsc)} }

func frags(mode text.SelectMode, fragments ...string) *clipboard.Data {
	data := registerData(mode, fragments)
	return &data
}

const grid = "abc\ndef"

// indentService installs an indent service answering for the listed
// rows with the given level. The rows are those at the time the
// service is asked, which for an operator working from the last range
// to the first is before the rows above have moved.
func indentService(indents map[int]int) func(*testing.T, *Helix, *cell.Buffer) {
	return func(_ *testing.T, _ *Helix, buf *cell.Buffer) {
		buf.WithView(testIndentView{View: buf.View(), indents: indents})
	}
}

const grid3 = "abc\ndef\nghi"

// TestDeleteOverAllRanges pins d and A-d: delete_selection_impl over
// every range, with the registers written once for the whole set.
func TestDeleteOverAllRanges(t *testing.T) {
	runOpSelCases(t, []opSelCase{
		{name: "three ranges on one row", content: "abcdef",
			sel: selOf(2, fwd(0, 0, 1, 0), fwd(2, 0, 3, 0), fwd(4, 0, 5, 0)), evs: keys("d"),
			want: "bdf", wantSels: []string{"b", "d", "f"}, wantPrimary: 2, wantAt: xy(2, 0)},
		{name: "ranges on three rows", content: grid3,
			sel: selOf(0, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1), fwd(1, 2, 2, 2)), evs: keys("d"),
			want: "ac\ndf\ngi", wantSels: []string{"c", "f", "i"}, wantAt: xy(1, 0)},
		{name: "touching ranges collapse onto the same cell and merge", content: "abcdX",
			sel: selOf(1, fwd(0, 0, 2, 0), fwd(2, 0, 4, 0)), evs: keys("d"),
			want: "X", wantSels: []string{"X"}, wantAt: xy(0, 0)},
		{name: "backward ranges", content: "abcdef",
			sel: selOf(1, bwd(0, 0, 2, 0), bwd(3, 0, 5, 0)), evs: keys("d"),
			want: "cf", wantSels: []string{"c", "f"}, wantPrimary: 1, wantAt: xy(1, 0)},
		{name: "whole lines on separate rows", content: "a\nb\nc\nd",
			sel: selOf(0, lines(0, 0), lines(2, 2)), evs: keys("d"),
			want: "b\nd", wantSels: []string{"b", "d"}, wantAt: xy(0, 0)},
		{name: "every range covers the whole buffer leaves a cursor on each empty row", content: "abc\ndef",
			sel: selOf(1, fwd(0, 0, 3, 0), fwd(0, 1, 3, 1)), evs: keys("d"),
			want: "\n", wantSels: []string{"\n", ""}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "ranges over the line endings join the rows", content: "ab\ncd\nef",
			sel: selOf(1, fwd(2, 0, 0, 1), fwd(2, 1, 0, 2)), evs: keys("d"),
			want: "abcdef", wantSels: []string{"c", "e"}, wantPrimary: 1, wantAt: xy(4, 0)},
		{name: "a range spanning rows next to a one-row range", content: grid3,
			sel: selOf(0, fwd(1, 0, 1, 1), fwd(1, 2, 3, 2)), evs: keys("d"),
			want: "aef\ng", wantSels: []string{"e", ""}, wantAt: xy(1, 0)},
		{name: "wide glyphs are one cell each", content: "世界x\n世界y",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("d"),
			want: "界x\n界y", wantSels: []string{"界", "界"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "a tab is one cell", content: "\tab\n\tcd",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("d"),
			want: "ab\ncd", wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "d exits select mode", content: grid, at: xy(1, 0), evs: keys("Cvd"),
			want: "ac\ndf", wantSels: []string{"c", "f"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "d on an empty buffer is unhandled", content: "", evs: keys("d"),
			want: "", wantSels: []string{""}, wantAt: xy(0, 0), wantHandled: new(false)},
		{name: "d on blank rows removes them", content: "\n\n",
			sel: selOf(1, point(xy(0, 0)), point(xy(0, 1))), evs: keys("d"),
			want: "", wantSels: []string{""}, wantAt: xy(0, 0), wantHandled: new(true),
			wantFrags: map[rune][]string{'"': {"\n", "\n"}}},
		{name: "d on the last row's end is unhandled", content: "ab\ncd",
			sel: selOf(0, point(xy(2, 1))), evs: keys("d"),
			want: "ab\ncd", wantSels: []string{""}, wantAt: xy(1, 1), wantHandled: new(false)},
		{name: "d on a point at the document end is unhandled", content: "ab\ncd",
			sel: selOf(0, point(xy(0, 2))), evs: keys("d"),
			want: "ab\ncd", wantSels: []string{""}, wantAt: xy(1, 1), wantHandled: new(false)},
		{name: "d of the last row leaves its empty row selected", content: "ab\ncd",
			at: xy(0, 1), evs: keys("xd"),
			want: "ab\n", wantSels: []string{"\n"}, wantAt: xy(0, 1)},
		{name: "d again removes the empty row", content: "ab\ncd",
			at: xy(0, 1), evs: keys("xdd"),
			want: "ab", wantSels: []string{""}, wantAt: xy(1, 0)},
		{name: "d of the trailing empty row takes the line ending before it", content: "ab\n",
			at: xy(0, 1), evs: keys("xd"),
			want: "ab", wantSels: []string{""}, wantAt: xy(1, 0)},
		{name: "the registers hold one fragment per range", content: grid, at: xy(1, 0), evs: keys("C\"ad"),
			want: "ac\ndf", wantSels: []string{"c", "f"}, wantPrimary: 1, wantAt: xy(1, 1),
			wantFrags: map[rune][]string{'a': {"b", "e"}, '"': {"b", "e"}, '-': {"b", "e"}},
			wantRegs:  map[rune]string{'a': "b\ne"}},
		{name: "whole lines skip the small delete register", content: "a\nb\nc\nd", evs: keys("xCd"),
			want: "c\nd", wantSels: []string{"c"}, wantAt: xy(0, 0),
			wantFrags: map[rune][]string{'"': {"a\n", "b\n"}}, wantRegs: map[rune]string{'-': ""}},
		{name: "A-d leaves the registers alone", content: grid, at: xy(1, 0), seed: "KEEP",
			evs:  cat(keys("C"), altKeys("d")),
			want: "ac\ndf", wantSels: []string{"c", "f"}, wantPrimary: 1, wantAt: xy(1, 1),
			wantRegs: map[rune]string{'"': "KEEP", '-': ""}},
		{name: "the black hole register drops every fragment", content: grid, at: xy(1, 0), seed: "KEEP",
			evs:  keys("C\"_d"),
			want: "ac\ndf", wantSels: []string{"c", "f"}, wantPrimary: 1, wantAt: xy(1, 1),
			wantRegs: map[rune]string{'"': "KEEP", '-': ""}},
		{name: "a count is ignored", content: grid, at: xy(1, 0), evs: keys("C3d"),
			want: "ac\ndf", wantSels: []string{"c", "f"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "deleting the last cell of a row leaves the cursor on its line ending", content: "ab\ncd",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1)), evs: keys("d"),
			want: "a\nc", wantSels: []string{"\n", ""}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "negative coordinates are clamped to the document start", content: grid,
			sel: selOf(0, rng{anchor: xy(-3, -1), head: xy(1, 0)}), evs: keys("d"),
			want: "bc\ndef", wantSels: []string{"b"}, wantAt: xy(0, 0)},
		{name: "coordinates past the document are clamped to its end", content: grid,
			sel: selOf(0, fwd(2, 1, 9, 9)), evs: keys("d"),
			want: "abc\nde", wantSels: []string{""}, wantAt: xy(1, 1)},
		{name: "a primary index past the set falls back to the last range", content: grid,
			sel: selOf(7, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("d"),
			want: "bc\nef", wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "an empty set falls back to the cursor", content: grid, at: xy(1, 0),
			sel: selOf(0), evs: keys("d"),
			want: "ac\ndef", wantSels: []string{"c"}, wantAt: xy(1, 0)},
	})
}

// TestChangeOverAllRanges pins c and A-c.
func TestChangeOverAllRanges(t *testing.T) {
	runOpSelCases(t, []opSelCase{
		{name: "c on one row leaves a cursor in every gap", content: "abcdef",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(4, 0, 5, 0)), evs: keys("c"),
			want: "acdf", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(3, 0), wantInsert: true},
		{name: "typing after c fills every gap", content: "abcdef",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(4, 0, 5, 0)), evs: cat(keys("cX"), esc()),
			want: "aXcdXf", wantSels: []string{"c", "f"}, wantPrimary: 1, wantAt: xy(5, 0)},
		{name: "c on whole lines on separate rows opens a line for each", content: "a\nb\nc\nd",
			sel: selOf(1, lines(0, 0), lines(2, 2)), evs: keys("c"),
			want: "\nb\n\nd", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(0, 2), wantInsert: true},
		{name: "c on touching whole lines opens one line", content: "a\nb\nc",
			sel: selOf(1, lines(0, 0), lines(1, 1)), evs: keys("c"),
			want: "\nc", wantSels: []string{""}, wantAt: xy(0, 0), wantInsert: true},
		{name: "c on backward ranges", content: "abcdef",
			sel: selOf(0, bwd(1, 0, 2, 0), bwd(4, 0, 5, 0)), evs: cat(keys("cX"), esc()),
			want: "aXcdXf", wantSels: []string{"c", "f"}, wantAt: xy(2, 0)},
		{name: "c exits select mode", content: grid, at: xy(1, 0), evs: keys("Cvc"),
			want: "ac\ndf", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(1, 1), wantInsert: true},
		{name: "c yanks the fragments", content: grid, at: xy(1, 0), evs: keys("Cc"),
			want: "ac\ndf", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(1, 1), wantInsert: true,
			wantFrags: map[rune][]string{'"': {"b", "e"}}},
		{name: "A-c leaves the registers alone", content: grid, at: xy(1, 0), seed: "KEEP",
			evs:  cat(keys("C"), altKeys("c")),
			want: "ac\ndf", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(1, 1), wantInsert: true,
			wantRegs: map[rune]string{'"': "KEEP"}},
		{name: "c with the black hole register", content: grid, at: xy(1, 0), seed: "KEEP", evs: keys("C\"_c"),
			want: "ac\ndf", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(1, 1), wantInsert: true,
			wantRegs: map[rune]string{'"': "KEEP"}},
		{name: "c on wide glyphs", content: "世界\n世界",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: cat(keys("cx"), esc()),
			want: "x界\nx界", wantSels: []string{"界", "界"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "c on an empty buffer enters insert mode", content: "", evs: cat(keys("cX"), esc()),
			want: "X", wantSels: []string{""}, wantAt: xy(0, 0)},
		{name: "c over line endings joins the rows and types at the seams", content: "ab\ncd\nef",
			sel: selOf(1, fwd(2, 0, 0, 1), fwd(2, 1, 0, 2)), evs: cat(keys("c-"), esc()),
			want: "ab-cd-ef", wantSels: []string{"c", "e"}, wantPrimary: 1, wantAt: xy(6, 0)},
		{name: "c on a point at the last row's end appends", content: "ab",
			sel: selOf(0, point(xy(2, 0))), evs: cat(keys("cX"), esc()),
			want: "abX", wantSels: []string{""}, wantAt: xy(2, 0)},
		{name: "c on a range spanning rows", content: grid3,
			sel: selOf(0, fwd(1, 0, 2, 2)), evs: cat(keys("c_"), esc()),
			want: "a_i", wantSels: []string{"i"}, wantAt: xy(2, 0)},
		{name: "a count is ignored", content: grid, at: xy(1, 0), evs: cat(keys("C3cX"), esc()),
			want: "aXc\ndXf", wantSels: []string{"c", "f"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "enter after c splits every row", content: grid, at: xy(1, 0),
			evs:  cat(keys("Cc"), []term.Event{namedKey(term.KeyEnter)}, esc()),
			want: "a\nc\nd\nf", wantSels: []string{"c", "f"}, wantPrimary: 1, wantAt: xy(0, 3)},
		{name: "backspace after c eats the cell before every gap", content: grid, at: xy(1, 0),
			evs:  cat(keys("Cc"), []term.Event{namedKey(term.KeyBackspace)}, esc()),
			want: "c\nf", wantSels: []string{"c", "f"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "c then esc without typing leaves cursors on the cells after the gaps", content: grid, at: xy(1, 0),
			evs:  cat(keys("Cc"), esc()),
			want: "ac\ndf", wantSels: []string{"c", "f"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "u after c restores the text and the ranges", content: grid, at: xy(1, 0),
			evs:  cat(keys("CcX"), esc(), keys("u")),
			want: grid, wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1)},
	})
}

// TestYankOverAllRanges pins y: yank_impl stores one fragment per
// range, joined by newlines for anyone reading the plain text.
func TestYankOverAllRanges(t *testing.T) {
	runOpSelCases(t, []opSelCase{
		{name: "y fills the chosen, unnamed and last-yank registers", content: grid, at: xy(1, 0),
			evs:  keys("C\"ay"),
			want: grid, wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1),
			wantFrags: map[rune][]string{'a': {"b", "e"}, '"': {"b", "e"}, '0': {"b", "e"}},
			wantRegs:  map[rune]string{'a': "b\ne", '"': "b\ne", '0': "b\ne"}},
		{name: "y leaves the small delete register alone", content: grid, at: xy(1, 0), evs: keys("Cy"),
			want: grid, wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1),
			wantRegs: map[rune]string{'-': ""}},
		{name: "y of wide glyphs", content: "世界\n世界",
			sel: selOf(1, fwd(0, 0, 2, 0), fwd(0, 1, 2, 1)), evs: keys("y"),
			want: "世界\n世界", wantSels: []string{"世界", "世界"}, wantPrimary: 1, wantAt: xy(1, 1),
			wantRegs: map[rune]string{'"': "世界\n世界"}, wantFrags: map[rune][]string{'"': {"世界", "世界"}}},
		{name: "y of a range spanning rows keeps the line ending inside it", content: grid,
			sel: selOf(1, fwd(1, 0, 1, 1), fwd(2, 1, 3, 1)), evs: keys("y"),
			want: grid, wantSels: []string{"bc\nd", "f"}, wantPrimary: 1, wantAt: xy(2, 1),
			wantRegs: map[rune]string{'"': "bc\nd\nf"}, wantFrags: map[rune][]string{'"': {"bc\nd", "f"}}},
		{name: "y of a range over a line ending", content: grid,
			sel: selOf(1, fwd(3, 0, 0, 1), fwd(2, 1, 3, 1)), evs: keys("y"),
			want: grid, wantSels: []string{"\n", "f"}, wantPrimary: 1, wantAt: xy(2, 1),
			wantRegs: map[rune]string{'"': "\n\nf"}, wantFrags: map[rune][]string{'"': {"\n", "f"}}},
		{name: "y of whole lines", content: "a\nb\nc", evs: keys("xCy"),
			want: "a\nb\nc", wantSels: []string{"a\n", "b\n"}, wantPrimary: 1, wantAt: xy(0, 1),
			wantRegs: map[rune]string{'"': "a\n\nb\n"}, wantFrags: map[rune][]string{'"': {"a\n", "b\n"}}},
		{name: "y on a blank row stores its line ending", content: "a\n\nb",
			sel: selOf(1, point(xy(0, 1)), fwd(0, 2, 1, 2)), evs: keys("y"),
			want: "a\n\nb", wantSels: []string{"\n", "b"}, wantPrimary: 1, wantAt: xy(0, 2),
			wantRegs: map[rune]string{'"': "\n\nb"}, wantFrags: map[rune][]string{'"': {"\n", "b"}}},
		{name: "y of a point at the last row's end stores an empty fragment", content: "a\nb",
			sel: selOf(0, fwd(0, 0, 1, 0), point(xy(1, 1))), evs: keys("y"),
			want: "a\nb", wantSels: []string{"a", ""}, wantAt: xy(0, 0),
			wantRegs: map[rune]string{'"': "a\n"}, wantFrags: map[rune][]string{'"': {"a", ""}}},
		{name: "y keeps the selection and exits select mode", content: grid, at: xy(1, 0), evs: keys("Cvy"),
			want: grid, wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "y on an empty buffer stores an empty fragment", content: "", evs: keys("y"),
			want: "", wantSels: []string{""}, wantAt: xy(0, 0), wantHandled: new(true),
			wantRegs: map[rune]string{'"': ""}, wantFrags: map[rune][]string{'"': {""}}},
		{name: "y with the black hole register still fills the unnamed one", content: grid, at: xy(1, 0),
			evs:  keys("C\"_y"),
			want: grid, wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1),
			wantFrags: map[rune][]string{'"': {"b", "e"}}},
	})

	t.Run("the register mode follows the primary's shape", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			primary int
			want    text.SelectMode
		}{
			{name: "linewise primary", primary: 0, want: text.LineSelection},
			{name: "standard primary", primary: 1, want: text.StandardSelection},
		} {
			t.Run(tc.name, func(t *testing.T) {
				hx, _, clip := newHelix(t, "a\nb\nc", term.Coordinates{})
				impl(hx).setSelection(selection{ranges: []rng{lines(0, 0), fwd(0, 2, 1, 2)}, primary: tc.primary})
				send(t, hx, key('y'))
				data, err := clip.Paste(clipboard.DefaultRegisterID)
				require.NoError(t, err)
				assert.Equal(t, fragmentsMetadata{Mode: tc.want, Fragments: []string{"a\n", "c"}}, data.Metadata)
			})
		}
	})
}

// TestPasteOverAllRanges pins p and P: paste_impl puts the i-th register
// value next to the i-th range, the last value serving every range
// beyond that.
func TestPasteOverAllRanges(t *testing.T) {
	runOpSelCases(t, []opSelCase{
		{name: "a plain register goes after every range", content: grid, at: xy(1, 0), seed: "X", evs: keys("Cp"),
			want: "abXc\ndeXf", wantSels: []string{"X", "X"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "P puts it before every range", content: grid, at: xy(1, 0), seed: "X", evs: keys("CP"),
			want: "aXbc\ndXef", wantSels: []string{"X", "X"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "fragments pair up with the ranges", content: grid, at: xy(1, 0),
			seedData: frags(text.StandardSelection, "X", "Y"), evs: keys("Cp"),
			want: "abXc\ndeYf", wantSels: []string{"X", "Y"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "the last fragment serves the extra ranges", content: grid3, at: xy(1, 0),
			seedData: frags(text.StandardSelection, "X", "Y"), evs: keys("CCp"),
			want: "abXc\ndeYf\nghYi", wantSels: []string{"X", "Y", "Y"}, wantPrimary: 2, wantAt: xy(2, 2)},
		{name: "extra fragments are ignored", content: grid, at: xy(1, 0),
			seedData: frags(text.StandardSelection, "X", "Y", "Z"), evs: keys("Cp"),
			want: "abXc\ndeYf", wantSels: []string{"X", "Y"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "a count repeats every fragment as one run", content: grid, at: xy(1, 0),
			seedData: frags(text.StandardSelection, "X", "Y"), evs: keys("C3p"),
			want: "abXXXc\ndeYYYf", wantSels: []string{"XXX", "YYY"}, wantPrimary: 1, wantAt: xy(4, 1)},
		{name: "a register without metadata pastes into all", content: grid, at: xy(1, 0),
			seedData: &clipboard.Data{Text: "X"}, evs: keys("Cp"),
			want: "abXc\ndeXf", wantSels: []string{"X", "X"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "an empty register is unhandled", content: grid, at: xy(1, 0), evs: keys("Cp"),
			want: grid, wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1), wantHandled: new(false)},
		{name: "the pasted ranges take the direction of their range", content: "abcdef",
			sel: selOf(1, bwd(1, 0, 2, 0), bwd(4, 0, 5, 0)), seed: "X", evs: keys("p"),
			want: "abXcdeXf", wantSels: []string{"X", "X"}, wantPrimary: 1, wantAt: xy(6, 0)},
		{name: "wide glyphs", content: grid, at: xy(1, 0), seed: "世", evs: keys("Cp"),
			want: "ab世c\nde世f", wantSels: []string{"世", "世"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "a value with a newline inside is not linewise", content: "abcdef",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(4, 0, 5, 0)), seed: "X\nY", evs: keys("p"),
			want: "abX\nYcdeX\nYf", wantSels: []string{"X\nY", "X\nY"}, wantPrimary: 1, wantAt: xy(0, 2)},
		{name: "linewise P goes above every range's line", content: "a\nb", evs: keys("xyCP"),
			want: "a\na\na\nb", wantSels: []string{"a\n", "a\n"}, wantPrimary: 1, wantAt: xy(0, 2)},
		{name: "linewise p below the last row opens the row it needs", content: "a\nb", evs: keys("xyCp"),
			want: "a\na\nb\na", wantSels: []string{"a\n", "a\n"}, wantPrimary: 1, wantAt: xy(0, 3)},
		{name: "one linewise fragment makes every paste linewise", content: grid3, at: xy(1, 0),
			seedData: frags(text.StandardSelection, "X", "Y\n"), evs: keys("Cp"),
			want: "abc\nX\ndef\nY\nghi", wantSels: []string{"X\n", "Y\n"}, wantPrimary: 1, wantAt: xy(0, 3)},
		{name: "a register holding only a line ending adds blank lines", content: "a\nb", seed: "\n", evs: keys("Cp"),
			want: "a\n\nb\n", wantSels: []string{"\n", "\n"}, wantPrimary: 1, wantAt: xy(0, 3)},
		{name: "p into ranges on the same row as each other and the paste", content: "ab",
			sel: selOf(0, fwd(0, 0, 1, 0), fwd(1, 0, 2, 0)), seed: "XY", evs: keys("p"),
			want: "aXYbXY", wantSels: []string{"XY", "XY"}, wantAt: xy(2, 0)},
		{name: "p on an empty buffer", content: "", seed: "X", evs: keys("p"),
			want: "X", wantSels: []string{"X"}, wantAt: xy(0, 0)},
		{name: "p exits select mode", content: grid, at: xy(1, 0), seed: "X", evs: keys("Cvp"),
			want: "abXc\ndeXf", wantSels: []string{"X", "X"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "p from a named register", content: grid, at: xy(1, 0),
			regs: map[rune]clipboard.Data{'a': {Text: "N", Metadata: text.StandardSelection}}, evs: keys("C\"ap"),
			want: "abNc\ndeNf", wantSels: []string{"N", "N"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "yank then paste round trips wide glyphs and tabs", content: "世\t\n界\t",
			sel: selOf(1, fwd(0, 0, 2, 0), fwd(0, 1, 2, 1)), evs: keys("yp"),
			want: "世\t世\t\n界\t界\t", wantSels: []string{"世\t", "界\t"}, wantPrimary: 1, wantAt: xy(3, 1)},
		{name: "p after a point at the last row's end appends", content: "ab",
			sel: selOf(0, point(xy(2, 0))), seed: "X", evs: keys("p"),
			want: "abX", wantSels: []string{"X"}, wantAt: xy(2, 0)},
		{name: "P before a point at the last row's end appends too", content: "ab",
			sel: selOf(0, point(xy(2, 0))), seed: "X", evs: keys("P"),
			want: "abX", wantSels: []string{"X"}, wantAt: xy(2, 0)},
		{name: "p after a range over a line ending pastes on the next row", content: "ab\ncd",
			sel: selOf(0, fwd(2, 0, 0, 1)), seed: "X", evs: keys("p"),
			want: "ab\nXcd", wantSels: []string{"X"}, wantAt: xy(0, 1)},
		{name: "P before a range over a line ending pastes at the row end", content: "ab\ncd",
			sel: selOf(0, fwd(2, 0, 0, 1)), seed: "X", evs: keys("P"),
			want: "abX\ncd", wantSels: []string{"X"}, wantAt: xy(2, 0)},
		{name: "linewise p after a range over rows goes under its last", content: grid3,
			sel: selOf(0, fwd(1, 0, 2, 1)), seedData: &clipboard.Data{Text: "X\n", Metadata: text.LineSelection},
			evs:  keys("p"),
			want: "abc\ndef\nX\nghi", wantSels: []string{"X\n"}, wantAt: xy(0, 2)},
		{name: "linewise P before a range over rows goes above its first", content: grid3,
			sel: selOf(0, fwd(1, 1, 2, 2)), seedData: &clipboard.Data{Text: "X\n", Metadata: text.LineSelection},
			evs:  keys("P"),
			want: "abc\nX\ndef\nghi", wantSels: []string{"X\n"}, wantAt: xy(0, 1)},
		{name: "linewise fragments into three ranges", content: "a\nb\nc",
			sel:      selOf(2, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1), fwd(0, 2, 1, 2)),
			seedData: frags(text.LineSelection, "X\n", "Y\n", "Z\n"), evs: keys("p"),
			want: "a\nX\nb\nY\nc\nZ", wantSels: []string{"X\n", "Y\n", "Z\n"}, wantPrimary: 2, wantAt: xy(0, 5)},
		{name: "p into ranges on the same row", content: "abc",
			sel:      selOf(2, fwd(0, 0, 1, 0), fwd(1, 0, 2, 0), fwd(2, 0, 3, 0)),
			seedData: frags(text.StandardSelection, "1", "2", "3"), evs: keys("p"),
			want: "a1b2c3", wantSels: []string{"1", "2", "3"}, wantPrimary: 2, wantAt: xy(5, 0)},
		{name: "P into ranges on the same row", content: "abc",
			sel:      selOf(0, fwd(0, 0, 1, 0), fwd(1, 0, 2, 0), fwd(2, 0, 3, 0)),
			seedData: frags(text.StandardSelection, "1", "2", "3"), evs: keys("P"),
			want: "1a2b3c", wantSels: []string{"1", "2", "3"}, wantAt: xy(0, 0)},
		{name: "an empty fragment pastes nothing and leaves a cursor after its range", content: grid, at: xy(1, 0),
			seedData: frags(text.StandardSelection, "", "Y"), evs: keys("Cp"),
			want: "abc\ndeYf", wantSels: []string{"c", "Y"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "a count of zero pastes once", content: grid, at: xy(1, 0), seed: "X", evs: keys("C0p"),
			want: "abXc\ndeXf", wantSels: []string{"X", "X"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "u after p removes every paste", content: grid, at: xy(1, 0), seed: "X", evs: keys("Cpu"),
			want: grid, wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1)},
	})
}

// TestReplaceWithRegisterOverAllRanges pins R.
func TestReplaceWithRegisterOverAllRanges(t *testing.T) {
	runOpSelCases(t, []opSelCase{
		{name: "fragments pair up with the ranges", content: grid, at: xy(1, 0),
			seedData: frags(text.StandardSelection, "X", "Y"), evs: keys("CR"),
			want: "aXc\ndYf", wantSels: []string{"X", "Y"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "extra fragments are ignored", content: grid, at: xy(1, 0),
			seedData: frags(text.StandardSelection, "X", "Y", "Z"), evs: keys("CR"),
			want: "aXc\ndYf", wantSels: []string{"X", "Y"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "the last fragment serves the extra ranges", content: grid3, at: xy(1, 0),
			seedData: frags(text.StandardSelection, "X"), evs: keys("CCR"),
			want: "aXc\ndXf\ngXi", wantSels: []string{"X", "X", "X"}, wantPrimary: 2, wantAt: xy(1, 2)},
		{name: "a longer value reshapes the ranges", content: grid, at: xy(1, 0), seed: "XYZ", evs: keys("CR"),
			want: "aXYZc\ndXYZf", wantSels: []string{"XYZ", "XYZ"}, wantPrimary: 1, wantAt: xy(3, 1)},
		{name: "a count repeats the value", content: grid, at: xy(1, 0), evs: keys("Cy2R"),
			want: "abbc\ndeef", wantSels: []string{"bb", "ee"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "a blank row's line ending is replaced like any text", content: "a\n\nb",
			sel: selOf(1, point(xy(0, 1)), fwd(0, 2, 1, 2)), seed: "X", evs: keys("R"),
			want: "a\nXX", wantSels: []string{"X", "X"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "a point at the last row's end is skipped", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), point(xy(1, 1))), seed: "X", evs: keys("R"),
			want: "X\nb", wantSels: []string{"X", ""}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "R on an empty buffer has nothing to replace", content: "", seed: "X", evs: keys("R"),
			want: "", wantSels: []string{""}, wantAt: xy(0, 0)},
		{name: "a linewise value keeps its line ending", content: grid, at: xy(1, 0),
			seedData: &clipboard.Data{Text: "X\n", Metadata: text.LineSelection}, evs: keys("CR"),
			want: "aX\nc\ndX\nf", wantSels: []string{"X\n", "X\n"}, wantPrimary: 1, wantAt: xy(1, 2)},
		{name: "backward ranges keep their direction", content: "abcdef",
			sel: selOf(1, bwd(1, 0, 2, 0), bwd(4, 0, 5, 0)), seed: "XY", evs: keys("R"),
			want: "aXYcdXYf", wantSels: []string{"XY", "XY"}, wantPrimary: 1, wantAt: xy(5, 0)},
		{name: "an empty register is unhandled", content: grid, at: xy(1, 0), evs: keys("CR"),
			want: grid, wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1), wantHandled: new(false)},
		{name: "R exits select mode", content: grid, at: xy(1, 0), seed: "X", evs: keys("CvR"),
			want: "aXc\ndXf", wantSels: []string{"X", "X"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "wide glyphs in and out", content: "世界\n世界",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), seed: "x", evs: keys("R"),
			want: "x界\nx界", wantSels: []string{"x", "x"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "R of a whole-line range", content: "a\nb\nc", seed: "X", evs: keys("xCR"),
			want: "XXc", wantSels: []string{"X", "X"}, wantPrimary: 1, wantAt: xy(1, 0)},
	})
}

// TestReplaceCharOverAllRanges pins r{char}.
func TestReplaceCharOverAllRanges(t *testing.T) {
	runOpSelCases(t, []opSelCase{
		{name: "every cell of every range", content: "abcdef",
			sel: selOf(1, fwd(0, 0, 2, 0), fwd(3, 0, 5, 0)), evs: keys("rz"),
			want: "zzczzf", wantSels: []string{"zz", "zz"}, wantPrimary: 1, wantAt: xy(4, 0)},
		{name: "a wide replacement", content: grid, at: xy(1, 0), evs: append(keys("Cr"), key('界')),
			want: "a界c\nd界f", wantSels: []string{"界", "界"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "over wide glyphs", content: "世界\n世界",
			sel: selOf(1, fwd(0, 0, 2, 0), fwd(0, 1, 2, 1)), evs: keys("rx"),
			want: "xx\nxx", wantSels: []string{"xx", "xx"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "space", content: grid, at: xy(1, 0), evs: cat(keys("Cr"), space()),
			want: "a c\nd f", wantSels: []string{" ", " "}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "tab", content: grid, at: xy(1, 0), evs: cat(keys("Cr"), []term.Event{namedKey(term.KeyTab)}),
			want: "a\tc\nd\tf", wantSels: []string{"\t", "\t"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "enter splits every range's row", content: grid, at: xy(1, 0),
			evs:  cat(keys("Cr"), []term.Event{namedKey(term.KeyEnter)}),
			want: "a\nc\nd\nf", wantSels: []string{"\n", "\n"}, wantPrimary: 1, wantAt: xy(0, 2)},
		{name: "a range spanning rows keeps every line ending", content: "ab\ncd\nef",
			sel: selOf(1, fwd(0, 0, 1, 1), fwd(0, 2, 2, 2)), evs: keys("r-"),
			want: "--\n-d\n--", wantSels: []string{"--\n-", "--"}, wantPrimary: 1, wantAt: xy(1, 2)},
		{name: "backward ranges keep their direction", content: "abcdef",
			sel: selOf(1, bwd(0, 0, 2, 0), bwd(3, 0, 5, 0)), evs: keys("rz"),
			want: "zzczzf", wantSels: []string{"zz", "zz"}, wantPrimary: 1, wantAt: xy(3, 0)},
		{name: "line endings are kept and alone leave the buffer as it was", content: "a\n\n\nb",
			sel: selOf(1, point(xy(0, 1)), point(xy(0, 2))), evs: keys("rz"),
			want: "a\n\n\nb", wantSels: []string{"\n", "\n"}, wantPrimary: 1, wantAt: xy(0, 2)},
		{name: "a point at the last row's end is skipped", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), point(xy(1, 1))), evs: keys("rz"),
			want: "z\nb", wantSels: []string{"z", ""}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "esc cancels", content: grid, at: xy(1, 0), evs: cat(keys("Cr"), esc()),
			want: grid, wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "a modified key cancels", content: grid, at: xy(1, 0), evs: append(keys("Cr"), ctrl('x')),
			want: grid, wantSels: []string{"b", "e"}, wantPrimary: 1, wantAt: xy(1, 1), wantHandled: new(false)},
		{name: "r exits select mode", content: grid, at: xy(1, 0), evs: keys("Cvrz"),
			want: "azc\ndzf", wantSels: []string{"z", "z"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "r on an empty buffer is unhandled", content: "", evs: keys("rz"),
			want: "", wantSels: []string{""}, wantAt: xy(0, 0), wantHandled: new(false)},
	})
}

// TestCaseOverAllRanges pins ~, ` and A-`.
func TestCaseOverAllRanges(t *testing.T) {
	runOpSelCases(t, []opSelCase{
		{name: "~ flips every letter", content: "aB\ncD",
			sel: selOf(1, fwd(0, 0, 2, 0), fwd(0, 1, 2, 1)), evs: keys("~"),
			want: "Ab\nCd", wantSels: []string{"Ab", "Cd"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "backtick lowercases", content: "AB\nCD",
			sel: selOf(1, fwd(0, 0, 2, 0), fwd(0, 1, 2, 1)), evs: keys("`"),
			want: "ab\ncd", wantSels: []string{"ab", "cd"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "A-backtick uppercases", content: "ab\ncd",
			sel: selOf(1, fwd(0, 0, 2, 0), fwd(0, 1, 2, 1)), evs: altKeys("`"),
			want: "AB\nCD", wantSels: []string{"AB", "CD"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "letters beyond ASCII", content: "é\nñ",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("~"),
			want: "É\nÑ", wantSels: []string{"É", "Ñ"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "nothing to change is unhandled", content: "1\n2",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("~"),
			want: "1\n2", wantSels: []string{"1", "2"}, wantPrimary: 1, wantAt: xy(0, 1), wantHandled: new(false)},
		{name: "glyphs without case are unhandled", content: "世\n界",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: altKeys("`"),
			want: "世\n界", wantSels: []string{"世", "界"}, wantPrimary: 1, wantAt: xy(0, 1), wantHandled: new(false)},
		{name: "a range spanning rows", content: "ab\ncd",
			sel: selOf(1, fwd(0, 0, 1, 1), fwd(1, 1, 2, 1)), evs: keys("~"),
			want: "AB\nCD", wantSels: []string{"AB\nC", "D"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "a line ending has no case", content: "a\n\nb",
			sel: selOf(1, point(xy(0, 1)), fwd(0, 2, 1, 2)), evs: keys("~"),
			want: "a\n\nB", wantSels: []string{"\n", "B"}, wantPrimary: 1, wantAt: xy(0, 2)},
		{name: "a point at the last row's end is skipped", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), point(xy(1, 1))), evs: keys("~"),
			want: "A\nb", wantSels: []string{"A", ""}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "backward ranges keep their direction", content: "abcd",
			sel: selOf(1, bwd(0, 0, 2, 0), bwd(2, 0, 4, 0)), evs: keys("~"),
			want: "ABCD", wantSels: []string{"AB", "CD"}, wantPrimary: 1, wantAt: xy(2, 0)},
		{name: "~ exits select mode", content: grid, at: xy(1, 0), evs: keys("Cv~"),
			want: "aBc\ndEf", wantSels: []string{"B", "E"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "only the ranges that change are rewritten", content: "a1\nb2",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(0, 1, 1, 1)), evs: keys("~"),
			want: "a1\nB2", wantSels: []string{"1", "B"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "~ on an empty buffer is unhandled", content: "", evs: keys("~"),
			want: "", wantSels: []string{""}, wantAt: xy(0, 0), wantHandled: new(false)},
	})
}

// TestJoinOverAllRanges pins J and A-J: join_selections_impl, which
// joins every line a range touches with the next, carries the ranges
// through the joins, and for A-J selects the separators it inserted.
func TestJoinOverAllRanges(t *testing.T) {
	commentSpec := func(t *testing.T, hx *Helix, _ *cell.Buffer) {
		hx.cursor.SetCommentSpec(text.CommentSpec{Line: []string{"//", "///"}})
	}
	runOpSelCases(t, []opSelCase{
		{name: "J on one range joins the next line", content: "a\nb\nc", at: xy(0, 0), evs: keys("J"),
			want: "a b\nc", wantSels: []string{"a"}, wantAt: xy(0, 0)},
		{name: "J keeps a range past the join in place", content: "ab\ncd",
			sel: selOf(0, fwd(1, 0, 2, 0)), evs: keys("J"),
			want: "ab cd", wantSels: []string{"b"}, wantAt: xy(1, 0)},
		{name: "J on two ranges joins after each", content: "a\nb\nc\nd\ne",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 2, 1, 2)), evs: keys("J"),
			want: "a b\nc d\ne", wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "J on consecutive rows joins them all", content: "a\nb\nc\nd",
			sel: selOf(0, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("J"),
			want: "a b c\nd", wantSels: []string{"a", "b"}, wantAt: xy(0, 0)},
		{name: "two ranges on one row join it once", content: "ab\ncd",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(1, 0, 2, 0)), evs: keys("J"),
			want: "ab cd", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(1, 0)},
		{name: "a range over rows joins every line it touches", content: "a\nb\nc\nd",
			sel: selOf(0, fwd(0, 0, 1, 2)), evs: keys("J"),
			want: "a b c\nd", wantSels: []string{"a b c"}, wantAt: xy(4, 0)},
		{name: "a range ending on a row start does not touch that row", content: "a\nb\nc",
			sel: selOf(0, lines(0, 0)), evs: keys("J"),
			want: "a b\nc", wantSels: []string{"a "}, wantAt: xy(1, 0)},
		{name: "the joining line's indentation is dropped", content: "a\n   b",
			evs:  keys("J"),
			want: "a b", wantSels: []string{"a"}, wantAt: xy(0, 0)},
		{name: "a tab-indented joining line", content: "a\n\t\tb",
			evs:  keys("J"),
			want: "a b", wantSels: []string{"a"}, wantAt: xy(0, 0)},
		{name: "a blank joining line adds no separator", content: "a\n\nb",
			evs:  keys("J"),
			want: "a\nb", wantSels: []string{"a"}, wantAt: xy(0, 0)},
		{name: "a whitespace-only joining line adds no separator", content: "a\n   \nb",
			evs:  keys("J"),
			want: "a\nb", wantSels: []string{"a"}, wantAt: xy(0, 0)},
		{name: "the first line's trailing space is kept", content: "a \nb",
			evs:  keys("J"),
			want: "a  b", wantSels: []string{"a"}, wantAt: xy(0, 0)},
		{name: "a closing bracket still gets a separator", content: "f(\n)",
			evs:  keys("J"),
			want: "f( )", wantSels: []string{"f"}, wantAt: xy(0, 0)},
		{name: "wide glyphs", content: "世\n界",
			evs:  keys("J"),
			want: "世 界", wantSels: []string{"世"}, wantAt: xy(0, 0)},
		{name: "a shared comment leader is stripped", content: "// a\n// b", setup: commentSpec,
			evs:  keys("J"),
			want: "// a b", wantSels: []string{"/"}, wantAt: xy(0, 0)},
		{name: "the longest leader wins", content: "/// a\n/// b", setup: commentSpec,
			evs:  keys("J"),
			want: "/// a b", wantSels: []string{"/"}, wantAt: xy(0, 0)},
		{name: "a different leader is kept and becomes the one to strip", content: "// a\n/// b\n/// c",
			setup: commentSpec, evs: keys("xxxJ"),
			want: "// a /// b c", wantSels: []string{"// a /// b c\n"}, wantAt: xy(11, 0)},
		{name: "a leader on the joining line only is kept", content: "a\n// b", setup: commentSpec,
			evs:  keys("J"),
			want: "a // b", wantSels: []string{"a"}, wantAt: xy(0, 0)},
		{name: "without a comment spec leaders are text", content: "// a\n// b",
			evs:  keys("J"),
			want: "// a // b", wantSels: []string{"/"}, wantAt: xy(0, 0)},
		{name: "J on the last row is unhandled", content: "a\nb", at: xy(0, 1), evs: keys("J"),
			want: "a\nb", wantSels: []string{"b"}, wantAt: xy(0, 1), wantHandled: new(false)},
		{name: "J with one range on the last row still joins the others", content: "a\nb\nc",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 2, 1, 2)), evs: keys("J"),
			want: "a b\nc", wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "J on an empty buffer is unhandled", content: "", evs: keys("J"),
			want: "", wantSels: []string{""}, wantAt: xy(0, 0), wantHandled: new(false)},
		{name: "J keeps select mode", content: "a\nb", evs: keys("vJ"),
			want: "a b", wantSels: []string{"a"}, wantAt: xy(0, 0), wantSelect: true},
		{name: "A-J selects every separator with the first as primary", content: "a\nb\nc\nd",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 2, 1, 2)), evs: altKeys("J"),
			want: "a b\nc d", wantSels: []string{" ", " "}, wantAt: xy(1, 0)},
		{name: "A-J keeps the ranges when no separator was inserted", content: "a\n\nb",
			evs:  altKeys("J"),
			want: "a\nb", wantSels: []string{"a"}, wantAt: xy(0, 0)},
		{name: "A-J selects only the separators that were inserted", content: "a\n\nb\nc",
			sel: selOf(0, fwd(0, 0, 1, 0), fwd(0, 2, 1, 2)), evs: altKeys("J"),
			want: "a\nb c", wantSels: []string{" "}, wantAt: xy(1, 1)},
		{name: "A-J on the last row is unhandled", content: "a", evs: altKeys("J"),
			want: "a", wantSels: []string{"a"}, wantAt: xy(0, 0), wantHandled: new(false)},
		{name: "u restores the lines and the ranges", content: "a\nb\nc\nd",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 2, 1, 2)), evs: keys("Ju"),
			want: "a\nb\nc\nd", wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(0, 2)},
	})
}

// TestIndentOverAllRanges pins >, < and =: every line any range touches
// is shifted once however many ranges share it, blank lines are left
// alone, and the ranges are carried through the inserted or removed
// indentation.
func TestIndentOverAllRanges(t *testing.T) {
	tabs := func(_ *testing.T, hx *Helix, _ *cell.Buffer) {
		impl(hx).config.indentRune = text.IndentRuneTab
		impl(hx).config.indentTabspaces = 4
	}
	runOpSelCases(t, []opSelCase{
		{name: "> indents the line of every range", content: "a\nb\nc",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 2, 1, 2)), evs: keys(">"),
			want: "  a\nb\n  c", wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(2, 2)},
		{name: "> on ranges sharing a line indents it once", content: "abc",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(2, 0, 3, 0)), evs: keys(">"),
			want: "  abc", wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(4, 0)},
		{name: "> over a range spanning rows", content: "a\nb\nc",
			sel: selOf(0, fwd(0, 0, 1, 1)), evs: keys(">"),
			want: "  a\n  b\nc", wantSels: []string{"a\n  b"}, wantAt: xy(2, 1)},
		{name: "> on a linewise range indents every row of it", content: "a\nb\nc",
			evs:  keys("xx>"),
			want: "  a\n  b\nc", wantSels: []string{"a\n  b\n"}, wantAt: xy(2, 1)},
		{name: "> skips blank lines", content: "a\n\nb",
			evs:  keys("xxx>"),
			want: "  a\n\n  b", wantSels: []string{"a\n\n  b"}, wantAt: xy(2, 2)},
		{name: "> with tabs", content: "a\nb", setup: tabs,
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys(">"),
			want: "\ta\n\tb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "a count indents count times", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("2>"),
			want: "    a\n    b", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(4, 1)},
		{name: "> keeps a backward range backward", content: "ab\ncd",
			sel: selOf(1, bwd(0, 0, 2, 0), bwd(0, 1, 2, 1)), evs: keys(">"),
			want: "  ab\n  cd", wantSels: []string{"ab", "cd"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "> on wide glyph rows", content: "世界\n世界",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys(">"),
			want: "  世界\n  世界", wantSels: []string{"世", "世"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "> exits select mode", content: "a\nb", evs: keys("Cv>"),
			want: "  a\n  b", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "> on an empty buffer is unhandled", content: "", evs: keys(">"),
			want: "", wantSels: []string{""}, wantAt: xy(0, 0), wantHandled: new(false)},
		{name: "< unindents the line of every range", content: "  a\n  b\n  c",
			sel: selOf(1, fwd(2, 0, 3, 0), fwd(2, 2, 3, 2)), evs: keys("<"),
			want: "a\n  b\nc", wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(0, 2)},
		{name: "< on ranges sharing a line unindents it once", content: "    ab",
			sel: selOf(1, fwd(4, 0, 5, 0), fwd(5, 0, 6, 0)), evs: keys("<"),
			want: "  ab", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(3, 0)},
		{name: "< takes what is there on a shallow line", content: " a\n  b",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(2, 1, 3, 1)), evs: keys("<"),
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "< on flush lines is unhandled", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("<"),
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1), wantHandled: new(false)},
		{name: "< with one flush line still shifts the others", content: "a\n  b",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(2, 1, 3, 1)), evs: keys("<"),
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "< removes a tab", content: "\ta\n\tb", setup: tabs,
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1)), evs: keys("<"),
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "a range whose start is unindented away starts on the row start", content: "  a\n  b",
			sel: selOf(1, fwd(1, 0, 3, 0), fwd(1, 1, 3, 1)), evs: keys("<"),
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "= reindents every range's line to its target", content: "a\n      b\n  c",
			setup: indentService(map[int]int{0: 1, 1: 1, 2: 0}),
			sel:   selOf(1, fwd(0, 0, 1, 0), fwd(6, 1, 7, 1)), evs: keys("="),
			want: "  a\n  b\n  c", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "= leaves lines without a target alone", content: "a\n  b",
			setup: indentService(map[int]int{}),
			sel:   selOf(1, fwd(0, 0, 1, 0), fwd(2, 1, 3, 1)), evs: keys("="),
			want: "a\n  b", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(2, 1), wantHandled: new(false)},
		{name: "= over three rows of a range", content: "  a\n  b\n  c",
			setup: indentService(map[int]int{0: 0, 1: 2, 2: 0}),
			sel:   selOf(0, fwd(2, 0, 3, 2)), evs: keys("="),
			want: "a\n    b\nc", wantSels: []string{"a\n    b\nc"}, wantAt: xy(0, 2)},
		{name: "u restores the indentation and the ranges", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys(">u"),
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1)},
	})
}

// TestCommentOverAllRanges pins C-c: toggle_line_comments over every
// line any range touches, commenting them all when one is not
// commented and uncommenting them all otherwise.
func TestCommentOverAllRanges(t *testing.T) {
	spec := text.CommentSpec{Line: []string{"//"}}
	withComments := func(_ *testing.T, hx *Helix, buf *cell.Buffer) {
		hx.cursor.SetCommentSpec(spec)
		buf.WithView(testCommentView{View: buf.View(), line: spec.Line})
	}
	runOpSelCases(t, []opSelCase{
		{name: "comments the line of every range", content: "a\nb\nc", setup: withComments,
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 2, 1, 2)), evs: []term.Event{ctrl('c')},
			want: "// a\nb\n// c", wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(3, 2)},
		{name: "uncomments when every line is commented", content: "// a\nb\n// c", setup: withComments,
			sel: selOf(1, fwd(3, 0, 4, 0), fwd(3, 2, 4, 2)), evs: []term.Event{ctrl('c')},
			want: "a\nb\nc", wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(0, 2)},
		{name: "comments all when one line is not commented", content: "// a\nb", setup: withComments,
			sel: selOf(1, fwd(3, 0, 4, 0), fwd(0, 1, 1, 1)), evs: []term.Event{ctrl('c')},
			want: "// // a\n// b", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(3, 1)},
		{name: "ranges sharing a line comment it once", content: "abc", setup: withComments,
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(2, 0, 3, 0)), evs: []term.Event{ctrl('c')},
			want: "// abc", wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(5, 0)},
		{name: "a range spanning rows comments each", content: "a\nb\nc", setup: withComments,
			sel: selOf(0, fwd(0, 0, 1, 1)), evs: []term.Event{ctrl('c')},
			want: "// a\n// b\nc", wantSels: []string{"a\n// b"}, wantAt: xy(3, 1)},
		{name: "toggling twice restores the text", content: "a\nb", setup: withComments,
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: []term.Event{ctrl('c'), ctrl('c')},
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "indented lines keep their indentation", content: "  a\n  b", setup: withComments,
			sel: selOf(1, fwd(2, 0, 3, 0), fwd(2, 1, 3, 1)), evs: []term.Event{ctrl('c')},
			want: "  // a\n  // b", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(5, 1)},
		{name: "without a comment spec it is unhandled", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: []term.Event{ctrl('c')},
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1), wantHandled: new(false)},
		{name: "on an empty buffer it is unhandled", content: "", setup: withComments,
			evs:  []term.Event{ctrl('c')},
			want: "", wantSels: []string{""}, wantAt: xy(0, 0), wantHandled: new(false)},
		{name: "u restores the lines and the ranges", content: "a\nb", setup: withComments,
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: []term.Event{ctrl('c'), key('u')},
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1)},
	})
}

// TestOpenLineOverAllRanges pins o and O: open, which puts a line
// after (or before) the line every range's cursor sits on, with the
// indentation of that line, and leaves one cursor on each new line.
func TestOpenLineOverAllRanges(t *testing.T) {
	runOpSelCases(t, []opSelCase{
		{name: "o under every range", content: "a\nb\nc",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 2, 1, 2)), evs: keys("o"),
			want: "a\n\nb\nc\n", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(0, 4), wantInsert: true},
		{name: "o without an indent service opens flush lines", content: "  a\n    b",
			sel: selOf(1, fwd(2, 0, 3, 0), fwd(4, 1, 5, 1)), evs: keys("o"),
			want: "  a\n\n    b\n", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(0, 3), wantInsert: true},
		{name: "o indents every new line as the indent service says", content: "  a\n    b",
			setup: indentService(map[int]int{1: 1, 2: 2}),
			sel:   selOf(1, fwd(2, 0, 3, 0), fwd(4, 1, 5, 1)), evs: keys("o"),
			want: "  a\n  \n    b\n    ", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(4, 3), wantInsert: true},
		{name: "o under a range spanning rows opens below its last row", content: "a\nb\nc",
			sel: selOf(0, fwd(0, 0, 1, 1)), evs: keys("o"),
			want: "a\nb\n\nc", wantSels: []string{""}, wantAt: xy(0, 2), wantInsert: true},
		{name: "o under a backward range spanning rows opens below its last row", content: "a\nb\nc",
			sel: selOf(0, bwd(0, 0, 1, 1)), evs: keys("o"),
			want: "a\nb\n\nc", wantSels: []string{""}, wantAt: xy(0, 2), wantInsert: true},
		{name: "o under a linewise range opens below its last row", content: "a\nb\nc",
			evs:  keys("xxo"),
			want: "a\nb\n\nc", wantSels: []string{""}, wantAt: xy(0, 2), wantInsert: true},
		{name: "ranges sharing a row each open a line", content: "ab",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(1, 0, 2, 0)), evs: keys("o"),
			want: "ab\n\n", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(0, 2), wantInsert: true},
		{name: "o then typing writes on every new line", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: cat(keys("ox"), esc()),
			want: "a\nx\nb\nx", wantSels: []string{"\n", ""}, wantPrimary: 1, wantAt: xy(0, 3)},
		{name: "a count opens count lines under every range", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("2o"),
			want: "a\n\n\nb\n\n", wantSels: []string{"", "", "", ""}, wantPrimary: 1, wantAt: xy(0, 2), wantInsert: true},
		{name: "O above every range", content: "a\nb\nc",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 2, 1, 2)), evs: keys("O"),
			want: "\na\nb\n\nc", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(0, 3), wantInsert: true},
		{name: "O above a range spanning rows opens above its first row", content: "a\nb\nc",
			sel: selOf(0, fwd(0, 1, 1, 2)), evs: keys("O"),
			want: "a\n\nb\nc", wantSels: []string{""}, wantAt: xy(0, 1), wantInsert: true},
		{name: "O above a backward range spanning rows opens above its first row", content: "a\nb\nc",
			sel: selOf(0, bwd(0, 1, 1, 2)), evs: keys("O"),
			want: "a\n\nb\nc", wantSels: []string{""}, wantAt: xy(0, 1), wantInsert: true},
		{name: "O on the first row", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("O"),
			want: "\na\n\nb", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(0, 2), wantInsert: true},
		{name: "O indents every new line as the indent service says", content: "  a\n  b",
			setup: indentService(map[int]int{0: 1, 1: 1}),
			sel:   selOf(1, fwd(2, 0, 3, 0), fwd(2, 1, 3, 1)), evs: keys("O"),
			want: "  \n  a\n  \n  b", wantSels: []string{"", ""}, wantPrimary: 1, wantAt: xy(2, 2), wantInsert: true},
		{name: "esc leaves a cursor on every opened line", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: cat(keys("o"), esc()),
			want: "a\n\nb\n", wantSels: []string{"\n", ""}, wantPrimary: 1, wantAt: xy(0, 3)},
		{name: "o on an empty buffer", content: "", evs: keys("o"),
			want: "\n", wantSels: []string{""}, wantAt: xy(0, 1), wantInsert: true},
		{name: "O on an empty buffer", content: "", evs: keys("O"),
			want: "\n", wantSels: []string{""}, wantAt: xy(0, 0), wantInsert: true},
		{name: "o exits select mode", content: "a", evs: keys("vo"),
			want: "a\n", wantSels: []string{""}, wantAt: xy(0, 1), wantInsert: true},
		{name: "u after o removes every opened line", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: cat(keys("ox"), esc(), keys("u")),
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1)},
	})
}

// TestAddNewlineOverAllRanges pins ]<space> and [<space>:
// add_newline_below/above, one line ending per range after (before)
// the lines it touches, without moving the ranges off their text.
func TestAddNewlineOverAllRanges(t *testing.T) {
	below := cat(keys("]"), space())
	above := cat(keys("["), space())
	runOpSelCases(t, []opSelCase{
		{name: "]<space> under every range", content: "a\nb\nc",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 2, 1, 2)), evs: below,
			want: "a\n\nb\nc\n", wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(0, 3)},
		{name: "]<space> under a range spanning rows", content: "a\nb\nc",
			sel: selOf(0, fwd(0, 0, 1, 1)), evs: below,
			want: "a\nb\n\nc", wantSels: []string{"a\nb"}, wantAt: xy(0, 1)},
		{name: "]<space> under a linewise range", content: "a\nb\nc",
			evs:  cat(keys("xx"), below),
			want: "a\nb\n\nc", wantSels: []string{"a\nb\n"}, wantAt: xy(0, 1)},
		{name: "ranges sharing a row each add a line", content: "ab\nc",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(1, 0, 2, 0)), evs: below,
			want: "ab\n\n\nc", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(1, 0)},
		{name: "a count adds count lines", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: cat(keys("2"), below),
			want: "a\n\n\nb\n\n", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 3)},
		{name: "[<space> above every range", content: "a\nb\nc",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 2, 1, 2)), evs: above,
			want: "\na\nb\n\nc", wantSels: []string{"a", "c"}, wantPrimary: 1, wantAt: xy(0, 4)},
		{name: "[<space> above a range spanning rows", content: "a\nb\nc",
			sel: selOf(0, fwd(0, 1, 1, 2)), evs: above,
			want: "a\n\nb\nc", wantSels: []string{"b\nc"}, wantAt: xy(0, 3)},
		{name: "[<space> keeps a backward range backward", content: "a\nb",
			sel: selOf(0, bwd(0, 1, 1, 1)), evs: above,
			want: "a\n\nb", wantSels: []string{"b"}, wantAt: xy(0, 2)},
		{name: "wide glyph rows", content: "世\n界",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: below,
			want: "世\n\n界\n", wantSels: []string{"世", "界"}, wantPrimary: 1, wantAt: xy(0, 2)},
		{name: "on an empty buffer", content: "", evs: below,
			want: "\n", wantSels: []string{""}, wantAt: xy(0, 1)},
		{name: "does not enter insert mode", content: "a", evs: cat(below, keys("x")),
			want: "a\n", wantSels: []string{"a\n"}, wantAt: xy(0, 0)},
		{name: "u removes every added line", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: cat(below, keys("u")),
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1)},
	})
}

// TestSurroundOverAllRanges pins ms, mr and md: surround_add wraps
// every range, surround_replace and surround_delete act on the pair
// around every range's cursor and refuse the whole command when one
// range has no pair or two share one.
func TestSurroundOverAllRanges(t *testing.T) {
	runOpSelCases(t, []opSelCase{
		{name: "ms wraps every range and selects the pairs", content: "ab\ncd",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("ms("),
			want: "(a)b\n(c)d", wantSels: []string{"(a)", "(c)"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "ms with the closing half of a pair", content: "ab",
			sel: selOf(0, fwd(0, 0, 1, 0)), evs: keys("ms]"),
			want: "[a]b", wantSels: []string{"[a]"}, wantAt: xy(2, 0)},
		{name: "ms with a quote", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("ms\""),
			want: "\"a\"\n\"b\"", wantSels: []string{"\"a\"", "\"b\""}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "ms with any other character uses it on both sides", content: "a",
			evs:  keys("ms*"),
			want: "*a*", wantSels: []string{"*a*"}, wantAt: xy(2, 0)},
		{name: "ms on ranges sharing a row", content: "abc",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(2, 0, 3, 0)), evs: keys("ms("),
			want: "(a)b(c)", wantSels: []string{"(a)", "(c)"}, wantPrimary: 1, wantAt: xy(6, 0)},
		{name: "ms on touching ranges", content: "ab",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(1, 0, 2, 0)), evs: keys("ms("),
			want: "(a)(b)", wantSels: []string{"(a)", "(b)"}, wantPrimary: 1, wantAt: xy(5, 0)},
		{name: "ms around a range spanning rows", content: "ab\ncd",
			sel: selOf(0, fwd(1, 0, 1, 1)), evs: keys("ms{"),
			want: "a{b\nc}d", wantSels: []string{"{b\nc}"}, wantAt: xy(1, 1)},
		{name: "ms keeps a backward range backward", content: "ab",
			sel: selOf(0, bwd(0, 0, 2, 0)), evs: keys("ms<"),
			want: "<ab>", wantSels: []string{"<ab>"}, wantAt: xy(0, 0)},
		{name: "ms around wide glyphs", content: "世界",
			sel: selOf(0, fwd(0, 0, 2, 0)), evs: keys("ms("),
			want: "(世界)", wantSels: []string{"(世界)"}, wantAt: xy(3, 0)},
		{name: "ms exits select mode", content: "ab", evs: keys("vlms("),
			want: "(ab)", wantSels: []string{"(ab)"}, wantAt: xy(3, 0)},
		{name: "ms esc cancels", content: "ab", evs: cat(keys("ms"), esc()),
			want: "ab", wantSels: []string{"a"}, wantAt: xy(0, 0)},
		{name: "md removes the pair around every range", content: "(a)\n(b)",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1)), evs: keys("md("),
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "md with the cursor on the opening half", content: "(a)\n(b)",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: keys("md("),
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "md with the cursor on the closing half leaves it past the text", content: "(a)\n(b)",
			sel: selOf(1, fwd(2, 0, 3, 0), fwd(2, 1, 3, 1)), evs: keys("md)"),
			want: "a\nb", wantSels: []string{"\n", ""}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "md removes the innermost pair around each range", content: "((a))\n((b))",
			sel: selOf(1, fwd(2, 0, 3, 0), fwd(2, 1, 3, 1)), evs: keys("md("),
			want: "(a)\n(b)", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "md on nested ranges removes each range's own pair", content: "(a(b))",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(3, 0, 4, 0)), evs: keys("md("),
			want: "ab", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(1, 0)},
		{name: "md of quotes", content: "\"a\" \"b\"",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(5, 0, 6, 0)), evs: keys("md\""),
			want: "a b", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(2, 0)},
		{name: "md of a pair spanning rows", content: "(a\nb)",
			sel: selOf(0, fwd(0, 1, 1, 1)), evs: keys("md("),
			want: "a\nb", wantSels: []string{"b"}, wantAt: xy(0, 1)},
		{name: "md with a range lacking a pair refuses the whole command", content: "(a)\nb",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(0, 1, 1, 1)), evs: keys("md("),
			want: "(a)\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1),
			wantHandled: new(false), wantMsg: "pair not found"},
		{name: "md with two ranges in one pair refuses the whole command", content: "(ab)",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(2, 0, 3, 0)), evs: keys("md("),
			want: "(ab)", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(2, 0),
			wantHandled: new(false), wantMsg: "cursors overlap"},
		{name: "md with the wrong pair character does nothing", content: "(a)",
			sel: selOf(0, fwd(1, 0, 2, 0)), evs: keys("md["),
			want: "(a)", wantSels: []string{"a"}, wantAt: xy(1, 0), wantHandled: new(false)},
		{name: "md on an empty buffer is unhandled", content: "", evs: keys("md("),
			want: "", wantSels: []string{""}, wantAt: xy(0, 0), wantHandled: new(false)},
		{name: "md exits select mode", content: "(a)", at: xy(1, 0), evs: keys("vmd("),
			want: "a", wantSels: []string{"a"}, wantAt: xy(0, 0)},
		{name: "mr swaps the pair around every range", content: "(a)\n(b)",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1)), evs: keys("mr(["),
			want: "[a]\n[b]", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "mr into a quote", content: "(a)",
			sel: selOf(0, fwd(1, 0, 2, 0)), evs: keys("mr('"),
			want: "'a'", wantSels: []string{"a"}, wantAt: xy(1, 0)},
		{name: "mr from a quote", content: "'a'\n'b'",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1)), evs: keys("mr'{"),
			want: "{a}\n{b}", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "mr on nested pairs swaps each range's own", content: "(a(b))",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(3, 0, 4, 0)), evs: keys("mr(["),
			want: "[a[b]]", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(3, 0)},
		{name: "mr of a pair spanning rows", content: "(a\nb)",
			sel: selOf(0, fwd(0, 1, 1, 1)), evs: keys("mr({"),
			want: "{a\nb}", wantSels: []string{"b"}, wantAt: xy(0, 1)},
		{name: "mr with a range lacking a pair refuses the whole command", content: "(a)\nb",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(0, 1, 1, 1)), evs: keys("mr(["),
			want: "(a)\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1),
			wantHandled: new(false), wantMsg: "pair not found"},
		{name: "mr esc after the first character cancels", content: "(a)",
			sel: selOf(0, fwd(1, 0, 2, 0)), evs: cat(keys("mr("), esc()),
			want: "(a)", wantSels: []string{"a"}, wantAt: xy(1, 0)},
		{name: "u restores the pairs and the ranges", content: "(a)\n(b)",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1)), evs: keys("md(u"),
			want: "(a)\n(b)", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(1, 1)},
	})
}

// TestIncrementOverAllRanges pins C-a and C-x: increment_impl over
// every range, each handing the text it covers to the incrementors,
// with the # register turning the amount into a sequence.
func TestIncrementOverAllRanges(t *testing.T) {
	inc := []term.Event{ctrl('a')}
	dec := []term.Event{ctrl('x')}
	runOpSelCases(t, []opSelCase{
		{name: "every range increments its number", content: "1 1\n1 1",
			sel: selOf(2, fwd(0, 0, 1, 0), fwd(2, 0, 3, 0), fwd(0, 1, 1, 1)), evs: inc,
			want: "2 2\n2 1", wantSels: []string{"2", "2", "2"}, wantPrimary: 2, wantAt: xy(0, 1)},
		{name: "C-x decrements every range", content: "10\n10",
			sel: selOf(1, fwd(0, 0, 2, 0), fwd(0, 1, 2, 1)), evs: dec,
			want: "9\n9", wantSels: []string{"9", "9"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "a count is the amount for every range", content: "1\n1",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: cat(keys("5"), inc),
			want: "6\n6", wantSels: []string{"6", "6"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "a cursor on one digit changes that digit alone", content: "123\n456",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1)), evs: inc,
			want: "133\n466", wantSels: []string{"3", "6"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "a range over a number and text is left alone", content: "1x\n2",
			sel: selOf(1, fwd(0, 0, 2, 0), fwd(0, 1, 1, 1)), evs: inc,
			want: "1x\n3", wantSels: []string{"1x", "3"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "negative numbers", content: "-1\n-1",
			sel: selOf(1, fwd(0, 0, 2, 0), fwd(0, 1, 2, 1)), evs: inc,
			want: "0\n0", wantSels: []string{"0", "0"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "crossing zero downwards", content: "0\n0",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: dec,
			want: "-1\n-1", wantSels: []string{"-1", "-1"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "zero padding is kept per range", content: "007\n09",
			sel: selOf(1, fwd(0, 0, 3, 0), fwd(0, 1, 2, 1)), evs: inc,
			want: "008\n10", wantSels: []string{"008", "10"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "growing past the padding", content: "99\n099",
			sel: selOf(1, fwd(0, 0, 2, 0), fwd(0, 1, 3, 1)), evs: inc,
			want: "100\n100", wantSels: []string{"100", "100"}, wantPrimary: 1, wantAt: xy(2, 1)},
		{name: "a number beyond 64 bits", content: "99999999999999999999\n1",
			sel: selOf(1, fwd(0, 0, 20, 0), fwd(0, 1, 1, 1)), evs: inc,
			want: "100000000000000000000\n2", wantSels: []string{"100000000000000000000", "2"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "hexadecimal, octal and binary", content: "0xff\n0o17\n0b11",
			sel: selOf(2, fwd(0, 0, 4, 0), fwd(0, 1, 4, 1), fwd(0, 2, 4, 2)), evs: inc,
			want: "0x100\n0o20\n0b100", wantSels: []string{"0x100", "0o20", "0b100"}, wantPrimary: 2, wantAt: xy(4, 2)},
		{name: "a date and a time", content: "2021-12-31\n23:59",
			sel: selOf(1, fwd(0, 0, 10, 0), fwd(0, 1, 5, 1)), evs: inc,
			want: "2022-01-01\n00:00", wantSels: []string{"2022-01-01", "00:00"}, wantPrimary: 1, wantAt: xy(4, 1)},
		{name: "ranges without a number are unchanged", content: "a\n1",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: inc,
			want: "a\n2", wantSels: []string{"a", "2"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "no range with a number is unhandled", content: "a\nb",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: inc,
			want: "a\nb", wantSels: []string{"a", "b"}, wantPrimary: 1, wantAt: xy(0, 1), wantHandled: new(false)},
		{name: "a range spanning rows is left alone", content: "1\n1\n1",
			sel: selOf(1, fwd(0, 0, 1, 1), fwd(0, 2, 1, 2)), evs: inc,
			want: "1\n1\n2", wantSels: []string{"1\n1", "2"}, wantPrimary: 1, wantAt: xy(0, 2)},
		{name: "a range over a line ending is left alone", content: "1\n1",
			sel: selOf(1, fwd(1, 0, 0, 1), fwd(0, 1, 1, 1)), evs: inc,
			want: "1\n2", wantSels: []string{"\n", "2"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "two ranges on one number change their own digits", content: "12",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(1, 0, 2, 0)), evs: inc,
			want: "23", wantSels: []string{"2", "3"}, wantPrimary: 1, wantAt: xy(1, 0)},
		{name: "a number after wide glyphs", content: "世1\n界1",
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1)), evs: inc,
			want: "世2\n界2", wantSels: []string{"2", "2"}, wantPrimary: 1, wantAt: xy(1, 1)},
		{name: "a range over wide glyphs is left alone", content: "世1",
			sel: selOf(0, fwd(0, 0, 2, 0)), evs: inc,
			want: "世1", wantSels: []string{"世1"}, wantAt: xy(1, 0), wantHandled: new(false)},
		{name: "backward ranges keep their direction", content: "1\n1",
			sel: selOf(1, bwd(0, 0, 1, 0), bwd(0, 1, 1, 1)), evs: inc,
			want: "2\n2", wantSels: []string{"2", "2"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "the # register makes a sequence from the first range", content: "0\n0\n0",
			sel: selOf(0, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1), fwd(0, 2, 1, 2)), evs: cat(keys("\"#"), inc),
			want: "1\n2\n3", wantSels: []string{"1", "2", "3"}, wantAt: xy(0, 0)},
		{name: "the # register with a count starts from the count and steps by one", content: "0\n0\n0",
			sel: selOf(0, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1), fwd(0, 2, 1, 2)), evs: cat(keys("\"#2"), inc),
			want: "2\n3\n4", wantSels: []string{"2", "3", "4"}, wantAt: xy(0, 0)},
		{name: "the # register counts a range without a number", content: "0\nx\n0",
			sel: selOf(0, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1), fwd(0, 2, 1, 2)), evs: cat(keys("\"#"), inc),
			want: "1\nx\n3", wantSels: []string{"1", "x", "3"}, wantAt: xy(0, 0)},
		{name: "the # register decrements as a sequence", content: "9\n9",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: cat(keys("\"#"), dec),
			want: "8\n7", wantSels: []string{"8", "7"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "a register other than # is a plain increment", content: "1\n1",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: cat(keys("\"a"), inc),
			want: "2\n2", wantSels: []string{"2", "2"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "on an empty buffer it is unhandled", content: "", evs: inc,
			want: "", wantSels: []string{""}, wantAt: xy(0, 0), wantHandled: new(false)},
		{name: "C-a exits select mode", content: "1\n1", evs: cat(keys("Cv"), inc),
			want: "2\n2", wantSels: []string{"2", "2"}, wantPrimary: 1, wantAt: xy(0, 1)},
		{name: "u restores the numbers and the ranges", content: "1\n1",
			sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)), evs: cat(inc, keys("u")),
			want: "1\n1", wantSels: []string{"1", "1"}, wantPrimary: 1, wantAt: xy(0, 1)},
	})
}

// TestIncrementText pins the incrementors with Helix's own fixtures
// from helix-core/src/increment.
func TestIncrementText(t *testing.T) {
	for _, tc := range []struct {
		in     string
		amount int64
		want   string
	}{
		{"100", 1, "101"}, {"100", -1, "99"}, {"99", 1, "100"}, {"100", 1000, "1100"},
		{"100", -1000, "-900"}, {"-1", 1, "0"}, {"-1", 2, "1"}, {"1", -1, "0"}, {"1", -2, "-1"},
		{"0", -1, "-1"}, {"007", 1, "008"}, {"099", 1, "100"}, {"-007", 1, "-006"}, {"-001", 1, "000"},
		{"0x0100", 1, "0x0101"}, {"0x0100", -1, "0x00ff"}, {"0x0001", -1, "0x0000"},
		{"0x0000", -1, "0x0000"}, {"0xffffffffffffffff", 1, "0x10000000000000000"},
		{"0xffffffffffffffff", 2, "0x10000000000000001"},
		{"0xffffffffffffffff", -1, "0xfffffffffffffffe"},
		{"0xABCDEF1234567890", 1, "0xABCDEF1234567891"},
		{"0xabcdef1234567890", 1, "0xabcdef1234567891"},
		{"0o0107", 1, "0o0110"}, {"0o0110", -1, "0o0107"}, {"0o0001", -1, "0o0000"},
		{"0o7777", 1, "0o10000"}, {"0o1000", -1, "0o0777"}, {"0o0107", 10, "0o0121"},
		{"0o0000", -1, "0o0000"},
		{"0o1777777777777777777777", 1, "0o2000000000000000000000"},
		{"0o1777777777777777777777", 2, "0o2000000000000000000001"},
		{"0o1777777777777777777777", -1, "0o1777777777777777777776"},
		{"0b00000100", 1, "0b00000101"}, {"0b00000100", -1, "0b00000011"},
		{"0b00000100", 2, "0b00000110"}, {"0b00000100", -2, "0b00000010"},
		{"0b00000001", -1, "0b00000000"}, {"0b00111111", 10, "0b01001001"},
		{"0b11111111", 1, "0b100000000"}, {"0b10000000", -1, "0b01111111"},
		{"0b0000", -1, "0b0000"},
		{"0b1111111111111111111111111111111111111111111111111111111111111111", 1,
			"0b10000000000000000000000000000000000000000000000000000000000000000"},
		{"0b1111111111111111111111111111111111111111111111111111111111111111", -1,
			"0b1111111111111111111111111111111111111111111111111111111111111110"},
		{"999_999", 1, "1_000_000"}, {"1_000_000", -1, "999_999"}, {"-999_999", -1, "-1_000_000"},
		{"0x0000_0000_0001", 0x1_ffff_0000, "0x0001_ffff_0001"}, {"0x0000_0000", -1, "0x0000_0000"},
		{"0x0000_0000_0000", -1, "0x0000_0000_0000"},
		{"0b01111111_11111111", 1, "0b10000000_00000000"},
		{"0b11111111_11111111", 1, "0b1_00000000_00000000"},
		{"2020-02-28", 1, "2020-02-29"}, {"2020-02-29", 1, "2020-03-01"}, {"2020-01-31", 1, "2020-02-01"},
		{"2020-01-20", 1, "2020-01-21"}, {"2021-01-01", -1, "2020-12-31"}, {"2021-01-31", -2, "2021-01-29"},
		{"2021-02-28", 1, "2021-03-01"}, {"2021-03-01", -1, "2021-02-28"}, {"2020-02-29", -1, "2020-02-28"},
		{"1980/12/21", 100, "1981/03/31"}, {"1980/12/21", -100, "1980/09/12"},
		{"1980/12/21", 1000, "1983/09/17"}, {"1980/12/21", -1000, "1978/03/27"},
		{"2021-11-24 07:12:23", 1, "2021-11-24 07:13:23"}, {"2021-11-24 07:12", 1, "2021-11-24 07:13"},
		{"2021/11/24 07:12:23", 1, "2021/11/24 07:13:23"}, {"2021/11/24 07:12", 1, "2021/11/24 07:13"},
		{"Wed Nov 24 2021", 1, "Thu Nov 25 2021"}, {"24-Nov-2021", 1, "25-Nov-2021"},
		{"2021 Nov 24", 1, "2021 Nov 25"}, {"Nov 24, 2021", 1, "Nov 25, 2021"},
		{"7:21:53 am", 1, "7:22:53 am"}, {"7:21:53 AM", 1, "7:22:53 AM"}, {"7:21 am", 1, "7:22 am"},
		{"7:21 PM", 1, "7:22 PM"}, {"11:59 pm", 1, "12:00 am"},
		{"23:24:23", 1, "23:25:23"}, {"23:24", 1, "23:25"}, {"23:59", 1, "00:00"},
		{"23:59:59", 1, "00:00:59"}, {"00:00", -1, "23:59"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := incrementText(tc.in, tc.amount)
			require.True(t, ok)
			assert.Equal(t, tc.want, got)
		})
	}

	for _, in := range []string{
		"", "_", "9_", "_9", "_9_", "a", "1a", "a1", "1 2", " 1", "1 ", "1\n", "-", "+1", "--1",
		"0x", "0xg", "0x-1", "0o8", "0b2", "0b", "１", "世", "1.5", "1,000",
		"0000-00-00", "1980-2-21", "1980-12-1", "12345x", "2020-02-30", "1999-12-32", "19-12-32",
		"1-2-3", "0000/00/00", "1980/2/21", "1980/12/1", "2020/02/30", "1999/12/32", "19/12/32",
		"1/2/3", "123:456:789", "11:61", "2021-55-12 08:12:54", "Wed Nov 31 2021", "Nov 31, 2021",
		"13:00 pm", "24:00",
	} {
		t.Run("rejects "+in, func(t *testing.T) {
			got, ok := incrementText(in, 1)
			assert.False(t, ok, "got %q", got)
		})
	}

	t.Run("a huge amount does not overflow a time", func(t *testing.T) {
		_, ok := incrementText("23:59", 1<<62)
		assert.False(t, ok)
	})
}

// TestUndoRedoOverAllRanges pins u, U, A-u and A-U across every
// operator: undo brings back the text and the whole selection set the
// operator started from, redo the set it left behind, and a count
// walks several revisions at once.
func TestUndoRedoOverAllRanges(t *testing.T) {
	two := selOf(1, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1))
	for _, tc := range []struct {
		name      string
		content   string
		sel       *selection
		setup     func(*testing.T, *Helix, *cell.Buffer)
		seed      string
		op        []term.Event
		wantAfter string
		// wantBefore is the set undo restores when it is not the one
		// the operator was given.
		wantBefore []string
	}{
		{name: "d", content: grid, sel: two, op: keys("d"), wantAfter: "ac\ndf"},
		{name: "A-d", content: grid, sel: two, op: altKeys("d"), wantAfter: "ac\ndf"},
		{name: "c", content: grid, sel: two, op: cat(keys("cX"), esc()), wantAfter: "aXc\ndXf"},
		{name: "p", content: grid, sel: two, seed: "X", op: keys("p"), wantAfter: "abXc\ndeXf"},
		{name: "P", content: grid, sel: two, seed: "X", op: keys("P"), wantAfter: "aXbc\ndXef"},
		{name: "R", content: grid, sel: two, seed: "X", op: keys("R"), wantAfter: "aXc\ndXf"},
		{name: "r", content: grid, sel: two, op: keys("rX"), wantAfter: "aXc\ndXf"},
		{name: "~", content: grid, sel: two, op: keys("~"), wantAfter: "aBc\ndEf"},
		{name: "J", content: "a\nb\nc\nd", sel: selOf(1, fwd(0, 0, 1, 0), fwd(0, 2, 1, 2)),
			op: keys("J"), wantAfter: "a b\nc d"},
		{name: ">", content: grid, sel: two, op: keys(">"), wantAfter: "  abc\n  def"},
		{name: "<", content: "  abc\n  def", sel: selOf(1, fwd(3, 0, 4, 0), fwd(3, 1, 4, 1)),
			op: keys("<"), wantAfter: "abc\ndef"},
		{name: "C-c", content: grid, sel: two,
			setup: func(_ *testing.T, hx *Helix, buf *cell.Buffer) {
				hx.cursor.SetCommentSpec(text.CommentSpec{Line: []string{"//"}})
				buf.WithView(testCommentView{View: buf.View(), line: []string{"//"}})
			},
			op: []term.Event{ctrl('c')}, wantAfter: "// abc\n// def"},
		{name: "o", content: grid, sel: two, op: cat(keys("oX"), esc()), wantAfter: "abc\nX\ndef\nX"},
		{name: "O", content: grid, sel: two, op: cat(keys("OX"), esc()), wantAfter: "X\nabc\nX\ndef"},
		{name: "]<space>", content: grid, sel: two, op: cat(keys("]"), space()), wantAfter: "abc\n\ndef\n"},
		{name: "[<space>", content: grid, sel: two, op: cat(keys("["), space()), wantAfter: "\nabc\n\ndef"},
		{name: "ms", content: grid, sel: two, op: keys("ms("), wantAfter: "a(b)c\nd(e)f"},
		{name: "md", content: "a(b)c\nd(e)f", sel: selOf(1, fwd(2, 0, 3, 0), fwd(2, 1, 3, 1)),
			op: keys("md("), wantAfter: "abc\ndef"},
		{name: "mr", content: "a(b)c\nd(e)f", sel: selOf(1, fwd(2, 0, 3, 0), fwd(2, 1, 3, 1)),
			op: keys("mr(["), wantAfter: "a[b]c\nd[e]f"},
		{name: "C-a", content: "a1c\nd1f", sel: two, op: []term.Event{ctrl('a')}, wantAfter: "a2c\nd2f"},
		{name: "i", content: grid, sel: two, op: cat(keys("iX"), esc()), wantAfter: "aXbc\ndXef"},
		// The commands that move the ranges before the first edit have
		// undo restore the moved ranges, which is where the transaction
		// started from.
		{name: "a", content: grid, sel: two, op: cat(keys("aX"), esc()), wantAfter: "abXc\ndeXf",
			wantBefore: []string{"bc", "ef"}},
		{name: "A", content: grid, sel: two, op: cat(keys("AX"), esc()), wantAfter: "abcX\ndefX",
			wantBefore: []string{"\n", ""}},
		{name: "I", content: grid, sel: two, op: cat(keys("IX"), esc()), wantAfter: "Xabc\nXdef",
			wantBefore: []string{"a", "d"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, buf, clip := newHelix(t, tc.content, term.Coordinates{})
			if tc.seed != "" {
				seedClipboard(t, clip, tc.seed, text.StandardSelection)
			}
			if tc.setup != nil {
				tc.setup(t, hx, buf)
			}
			impl(hx).setSelection(*tc.sel)
			before := sels(hx)
			if tc.wantBefore != nil {
				before = tc.wantBefore
			}
			send(t, hx, tc.op...)
			require.Equal(t, tc.wantAfter, buf.String(), "after the operator")
			after := sels(hx)
			afterPrimary := primaryIdx(hx)

			send(t, hx, key('u'))
			assert.Equal(t, tc.content, buf.String(), "undo restores the text")
			assert.Equal(t, before, sels(hx), "undo restores the set")
			assert.Equal(t, tc.sel.primary, primaryIdx(hx), "undo restores the primary")
			assertSelectionInvariants(t, hx)

			send(t, hx, key('U'))
			assert.Equal(t, tc.wantAfter, buf.String(), "redo restores the text")
			assert.Equal(t, after, sels(hx), "redo restores the set")
			assert.Equal(t, afterPrimary, primaryIdx(hx), "redo restores the primary")
			assertSelectionInvariants(t, hx)

			send(t, hx, modKey(term.ModAlt, 'u'))
			assert.Equal(t, tc.content, buf.String(), "A-u undoes")
			send(t, hx, modKey(term.ModAlt, 'U'))
			assert.Equal(t, tc.wantAfter, buf.String(), "A-U redoes")
		})
	}

	t.Run("a count walks several revisions", func(t *testing.T) {
		hx, buf, _ := newHelix(t, grid, term.Coordinates{})
		impl(hx).setSelection(*two)
		send(t, hx, keys("d")...)
		send(t, hx, keys("d")...)
		require.Equal(t, "a\nd", buf.String())
		send(t, hx, key('2'), modKey(term.ModAlt, 'u'))
		assert.Equal(t, grid, buf.String())
		assert.Equal(t, []string{"b", "e"}, sels(hx))
		send(t, hx, key('2'), modKey(term.ModAlt, 'U'))
		assert.Equal(t, "a\nd", buf.String())
		assert.Equal(t, []string{"\n", ""}, sels(hx))
	})

	t.Run("undo with nothing to undo is unhandled and keeps the set", func(t *testing.T) {
		hx, _, _ := newHelix(t, grid, term.Coordinates{})
		impl(hx).setSelection(*two)
		_, handled := hx.Handle(key('u'))
		assert.False(t, handled)
		assert.Equal(t, []string{"b", "e"}, sels(hx))
		_, handled = hx.Handle(key('U'))
		assert.False(t, handled)
		assert.Equal(t, []string{"b", "e"}, sels(hx))
	})

	t.Run("a set that no longer fits the restored text is clamped", func(t *testing.T) {
		hx, buf, _ := newHelix(t, "abcdef\nghijkl", term.Coordinates{})
		send(t, hx, keys("xxd")...)
		require.Equal(t, "", buf.String())
		impl(hx).setSelection(*selOf(0, point(xy(0, 0))))
		send(t, hx, key('U'))
		assert.Equal(t, "", buf.String())
		send(t, hx, key('u'))
		assert.Equal(t, "abcdef\nghijkl", buf.String())
		assertSelectionInvariants(t, hx)
	})
}

// TestRegisterData pins registerData, registerFragments and
// registerValues: one fragment per range on the way in, the joined
// text for anyone reading the register as plain text, and one value
// per range on the way out however the register was written.
func TestRegisterData(t *testing.T) {
	t.Run("registerData", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			mode      text.SelectMode
			fragments []string
			wantText  string
			wantMeta  any
		}{
			{name: "one fragment is plain", mode: text.StandardSelection, fragments: []string{"a"},
				wantText: "a", wantMeta: text.StandardSelection},
			{name: "one linewise fragment", mode: text.LineSelection, fragments: []string{"a\n"},
				wantText: "a\n", wantMeta: text.LineSelection},
			{name: "two fragments join with a newline", mode: text.StandardSelection,
				fragments: []string{"a", "b"}, wantText: "a\nb",
				wantMeta: fragmentsMetadata{Mode: text.StandardSelection, Fragments: []string{"a", "b"}}},
			{name: "empty fragments survive", mode: text.StandardSelection,
				fragments: []string{"", "b", ""}, wantText: "\nb\n",
				wantMeta: fragmentsMetadata{Mode: text.StandardSelection, Fragments: []string{"", "b", ""}}},
			{name: "fragments with newlines", mode: text.LineSelection,
				fragments: []string{"a\n", "b\n"}, wantText: "a\n\nb\n",
				wantMeta: fragmentsMetadata{Mode: text.LineSelection, Fragments: []string{"a\n", "b\n"}}},
			{name: "wide glyphs", mode: text.StandardSelection,
				fragments: []string{"世", "界"}, wantText: "世\n界",
				wantMeta: fragmentsMetadata{Mode: text.StandardSelection, Fragments: []string{"世", "界"}}},
			{name: "no fragments", mode: text.StandardSelection, fragments: nil,
				wantText: "", wantMeta: fragmentsMetadata{Mode: text.StandardSelection}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				data := registerData(tc.mode, tc.fragments)
				assert.Equal(t, tc.wantText, data.Text)
				assert.Equal(t, tc.wantMeta, data.Metadata)
			})
		}
	})

	t.Run("registerFragments", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			data     clipboard.Data
			want     []string
			wantMode text.SelectMode
		}{
			{name: "fragments metadata",
				data: clipboard.Data{Text: "a\nb", Metadata: fragmentsMetadata{Mode: text.StandardSelection, Fragments: []string{"a", "b"}}},
				want: []string{"a", "b"}, wantMode: text.StandardSelection},
			{name: "select mode metadata", data: clipboard.Data{Text: "x\n", Metadata: text.LineSelection},
				want: []string{"x\n"}, wantMode: text.LineSelection},
			{name: "no metadata", data: clipboard.Data{Text: "sys"},
				want: []string{"sys"}, wantMode: text.NoSelection},
			{name: "foreign metadata", data: clipboard.Data{Text: "sys", Metadata: 42},
				want: []string{"sys"}, wantMode: text.NoSelection},
			{name: "empty data", data: clipboard.Data{},
				want: []string{""}, wantMode: text.NoSelection},
			{name: "fragments metadata without fragments",
				data: clipboard.Data{Text: "t", Metadata: fragmentsMetadata{Mode: text.StandardSelection}},
				want: nil, wantMode: text.StandardSelection},
			{name: "fragments metadata by pointer is foreign",
				data: clipboard.Data{Text: "t", Metadata: &fragmentsMetadata{Fragments: []string{"a"}}},
				want: []string{"t"}, wantMode: text.NoSelection},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, mode := registerFragments(tc.data)
				assert.Equal(t, tc.want, got)
				assert.Equal(t, tc.wantMode, mode)
			})
		}
	})

	t.Run("registerValues", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			ranges int
			data   clipboard.Data
			count  int
			want   []string
		}{
			{name: "one value per range", ranges: 2,
				data:  registerData(text.StandardSelection, []string{"a", "b"}),
				count: 1, want: []string{"a", "b"}},
			{name: "the last value serves the extra ranges", ranges: 4,
				data:  registerData(text.StandardSelection, []string{"a", "b"}),
				count: 1, want: []string{"a", "b", "b", "b"}},
			{name: "extra values are dropped", ranges: 1,
				data:  registerData(text.StandardSelection, []string{"a", "b"}),
				count: 1, want: []string{"a"}},
			{name: "a plain value serves every range", ranges: 3,
				data: clipboard.Data{Text: "x"}, count: 1, want: []string{"x", "x", "x"}},
			{name: "a count repeats every value", ranges: 2,
				data:  registerData(text.StandardSelection, []string{"a", "b"}),
				count: 3, want: []string{"aaa", "bbb"}},
			{name: "a zero count is one", ranges: 1,
				data: clipboard.Data{Text: "x"}, count: 0, want: []string{"x"}},
			{name: "a negative count is one", ranges: 1,
				data: clipboard.Data{Text: "x"}, count: -5, want: []string{"x"}},
			{name: "fragments metadata without fragments falls back to the text", ranges: 2,
				data:  clipboard.Data{Text: "t", Metadata: fragmentsMetadata{Mode: text.StandardSelection}},
				count: 1, want: []string{"t", "t"}},
			{name: "empty data gives empty values", ranges: 2,
				data: clipboard.Data{}, count: 1, want: []string{"", ""}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				hx, _, _ := newHelix(t, strings.Repeat("a\n", tc.ranges), term.Coordinates{})
				ranges := make([]rng, tc.ranges)
				for i := range ranges {
					ranges[i] = fwd(0, i, 1, i)
				}
				impl(hx).setSelection(selection{ranges: ranges})
				got, _ := impl(hx).registerValues(tc.data, tc.count)
				assert.Equal(t, tc.want, got)
			})
		}
	})
}

// TestOperatorsWithSharedScroll pins that the operators carry the
// selection set through their edits when the handler is built on an
// existing scroll rather than a buffer of its own, since the change
// recorder has to sit on whichever buffer the scroll shows.
func TestOperatorsWithSharedScroll(t *testing.T) {
	newShared := func(t *testing.T, content string) (*Helix, *cell.Buffer) {
		t.Helper()
		resource, err := workspaceapi.ParseURI("test:///shared")
		require.NoError(t, err)
		buf := cell.NewBuffer()
		buf.ReadFrom(strings.NewReader(content))
		hx := new(Helix)
		hx.InitWithScroll(component.NewScroll(buf), resource, text.IndentRuneSpace, 2,
			WithClipboard(registerset.New(clipboard.NewInMemory())))
		hx.Resize(80, 20)
		return hx, buf
	}
	for _, tc := range []struct {
		name     string
		content  string
		sel      *selection
		evs      []term.Event
		want     string
		wantSels []string
	}{
		{name: "d", content: grid, sel: selOf(1, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1)), evs: keys("d"),
			want: "ac\ndf", wantSels: []string{"c", "f"}},
		{name: "c and typing", content: grid, sel: selOf(1, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1)),
			evs: cat(keys("cXY"), esc()), want: "aXYc\ndXYf", wantSels: []string{"c", "f"}},
		{name: "an edit made behind the handler's back", content: grid,
			sel: selOf(1, fwd(1, 0, 2, 0), fwd(1, 1, 2, 1)), evs: keys("d"),
			want: "Zac\ndf", wantSels: []string{"c", "f"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, buf := newShared(t, tc.content)
			impl(hx).setSelection(*tc.sel)
			if strings.HasPrefix(tc.name, "an edit made behind") {
				buf.Edit(context.Background(), xy(0, 0), xy(0, 0), "Z")
			}
			send(t, hx, tc.evs...)
			assert.Equal(t, tc.want, buf.String())
			assert.Equal(t, tc.wantSels, sels(hx))
			assertSelectionInvariants(t, hx)
		})
	}
}

// TestSetSelectionClamps pins what setSelection makes of a set no
// command would build: coordinates outside the document land on its
// nearest position, an empty set falls back to the cursor, the
// primary index is kept in range, and points widen onto the character
// after them outside insert mode.
func TestSetSelectionClamps(t *testing.T) {
	for _, tc := range []struct {
		name        string
		content     string
		sel         selection
		wantRanges  []rng
		wantPrimary int
	}{
		{name: "negative coordinates", content: grid,
			sel:        selection{ranges: []rng{{anchor: xy(-1, -1), head: xy(-2, 0)}}},
			wantRanges: []rng{fwd(0, 0, 1, 0)}},
		{name: "a row past the end", content: grid,
			sel:        selection{ranges: []rng{{anchor: xy(0, 5), head: xy(2, 9)}}},
			wantRanges: []rng{point(xy(0, 2))}},
		{name: "a column past the row's end", content: grid,
			sel:        selection{ranges: []rng{{anchor: xy(1, 0), head: xy(99, 0)}}},
			wantRanges: []rng{fwd(1, 0, 3, 0)}},
		{name: "a column past the last row's end", content: grid,
			sel:        selection{ranges: []rng{{anchor: xy(99, 1), head: xy(99, 1)}}},
			wantRanges: []rng{point(xy(3, 1))}},
		{name: "an empty set falls back to the cursor", content: grid,
			sel: selection{}, wantRanges: []rng{fwd(0, 0, 1, 0)}},
		{name: "a primary past the set", content: grid,
			sel:         selection{ranges: []rng{fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)}, primary: 9},
			wantRanges:  []rng{fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)},
			wantPrimary: 1},
		{name: "a negative primary", content: grid,
			sel:        selection{ranges: []rng{fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)}, primary: -3},
			wantRanges: []rng{fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)}},
		{name: "unsorted ranges are sorted and the primary follows", content: grid,
			sel:         selection{ranges: []rng{fwd(0, 1, 1, 1), fwd(0, 0, 1, 0)}},
			wantRanges:  []rng{fwd(0, 0, 1, 0), fwd(0, 1, 1, 1)},
			wantPrimary: 1},
		{name: "overlapping ranges merge", content: grid,
			sel:        selection{ranges: []rng{fwd(0, 0, 2, 0), fwd(1, 0, 3, 0)}},
			wantRanges: []rng{fwd(0, 0, 3, 0)}},
		{name: "duplicate ranges merge", content: grid,
			sel:        selection{ranges: []rng{fwd(1, 0, 2, 0), fwd(1, 0, 2, 0), fwd(1, 0, 2, 0)}},
			wantRanges: []rng{fwd(1, 0, 2, 0)}},
		{name: "a point widens forward", content: grid,
			sel:        selection{ranges: []rng{point(xy(1, 0))}},
			wantRanges: []rng{fwd(1, 0, 2, 0)}},
		{name: "a point on a line ending widens onto it", content: grid,
			sel:        selection{ranges: []rng{point(xy(3, 0))}},
			wantRanges: []rng{{anchor: xy(3, 0), head: xy(0, 1)}}},
		{name: "a point at the last row's end stays", content: grid,
			sel:        selection{ranges: []rng{point(xy(3, 1))}},
			wantRanges: []rng{point(xy(3, 1))}},
		{name: "a point at the document end stays", content: grid,
			sel:        selection{ranges: []rng{point(xy(0, 2))}},
			wantRanges: []rng{point(xy(0, 2))}},
		{name: "points that widen onto each other merge", content: grid,
			sel:        selection{ranges: []rng{point(xy(1, 0)), fwd(2, 0, 3, 0)}},
			wantRanges: []rng{fwd(1, 0, 2, 0), fwd(2, 0, 3, 0)}},
		{name: "an empty document clamps onto its end", content: "",
			sel:        selection{ranges: []rng{fwd(3, 3, 4, 4)}},
			wantRanges: []rng{point(xy(0, 1))}},
		{name: "a blank last row keeps a point at its start", content: "a\n",
			sel:        selection{ranges: []rng{point(xy(5, 1))}},
			wantRanges: []rng{point(xy(0, 1))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hx, _, _ := newHelix(t, tc.content, term.Coordinates{})
			h := impl(hx)
			h.setSelection(tc.sel)
			got := make([]rng, len(h.sel.ranges))
			for i, r := range h.sel.ranges {
				r.col = 0
				got[i] = r
			}
			assert.Equal(t, tc.wantRanges, got)
			assert.Equal(t, tc.wantPrimary, h.sel.primary)
			assertSelectionInvariants(t, hx)
			// The set installs the same way twice.
			h.setSelection(h.sel)
			assert.Equal(t, tc.wantRanges, func() []rng {
				out := make([]rng, len(h.sel.ranges))
				for i, r := range h.sel.ranges {
					r.col = 0
					out[i] = r
				}
				return out
			}())
		})
	}
}
