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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/cell"
)

func fwd(x0, y0, x1, y1 int) rng { return rng{anchor: xy(x0, y0), head: xy(x1, y1)} }

// TestRangeBasics pins the accessors the rest of the model is built on.
func TestRangeBasics(t *testing.T) {
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("abc\ndef"))

	f := fwd(1, 0, 3, 0)
	b := fwd(3, 0, 1, 0)
	p := fwd(2, 0, 2, 0)

	assert.False(t, f.backward())
	assert.True(t, b.backward())
	assert.Equal(t, xy(1, 0), f.from())
	assert.Equal(t, xy(3, 0), f.to())
	assert.Equal(t, xy(1, 0), b.from())
	assert.Equal(t, xy(3, 0), b.to())
	assert.Equal(t, f, b.forward())
	assert.Equal(t, b, f.flipped())
	assert.True(t, p.isPoint())
	assert.False(t, f.isPoint())

	assert.Equal(t, xy(2, 0), f.cursor(buf), "forward: last covered cell")
	assert.Equal(t, xy(1, 0), b.cursor(buf), "backward: the head")
	assert.Equal(t, xy(2, 0), p.cursor(buf), "point: the head")
	assert.Equal(t, xy(3, 0), fwd(0, 0, 0, 1).cursor(buf),
		"a range ending at a row start sits on the previous line ending")
}

// TestRangePutCursor pins Range::put_cursor, including the anchor shift
// when an extension crosses over the anchor.
func TestRangePutCursor(t *testing.T) {
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("abcdef"))
	for _, tc := range []struct {
		name   string
		r      rng
		target int
		extend bool
		want   rng
	}{
		{name: "no extend collapses to one cell", r: fwd(0, 0, 4, 0), target: 2,
			want: fwd(2, 0, 3, 0)},
		{name: "extend forward past the head", r: fwd(1, 0, 3, 0), target: 4, extend: true,
			want: fwd(1, 0, 5, 0)},
		{name: "extend forward inside the range", r: fwd(1, 0, 4, 0), target: 2, extend: true,
			want: fwd(1, 0, 3, 0)},
		{name: "extend forward crossing the anchor flips and shifts it",
			r: fwd(2, 0, 4, 0), target: 0, extend: true, want: fwd(3, 0, 0, 0)},
		{name: "extend backward crossing the anchor flips and shifts it",
			r: fwd(3, 0, 1, 0), target: 4, extend: true, want: fwd(2, 0, 5, 0)},
		{name: "extend onto the anchor itself", r: fwd(3, 0, 1, 0), target: 3, extend: true,
			want: fwd(2, 0, 4, 0)},
		{name: "no extend at the buffer end", r: fwd(0, 0, 1, 0), target: 6,
			want: fwd(6, 0, 0, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.r.putCursor(buf, xy(tc.target, 0), tc.extend))
		})
	}
}

// TestRangeOverlapsAndMerge pins Range::overlaps and Range::merge.
func TestRangeOverlapsAndMerge(t *testing.T) {
	for _, tc := range []struct {
		name     string
		a, b     rng
		overlaps bool
		merged   rng
	}{
		{name: "disjoint", a: fwd(0, 0, 2, 0), b: fwd(3, 0, 5, 0), overlaps: false,
			merged: fwd(0, 0, 5, 0)},
		{name: "touching does not overlap", a: fwd(0, 0, 2, 0), b: fwd(2, 0, 5, 0),
			overlaps: false, merged: fwd(0, 0, 5, 0)},
		{name: "crossing", a: fwd(0, 0, 3, 0), b: fwd(2, 0, 5, 0), overlaps: true,
			merged: fwd(0, 0, 5, 0)},
		{name: "contained", a: fwd(0, 0, 5, 0), b: fwd(2, 0, 3, 0), overlaps: true,
			merged: fwd(0, 0, 5, 0)},
		{name: "same from", a: fwd(1, 0, 2, 0), b: fwd(1, 0, 4, 0), overlaps: true,
			merged: fwd(1, 0, 4, 0)},
		{name: "two points at one position", a: fwd(2, 0, 2, 0), b: fwd(2, 0, 2, 0),
			overlaps: true, merged: fwd(2, 0, 2, 0)},
		{name: "a point at the from of a range", a: fwd(2, 0, 2, 0), b: fwd(2, 0, 4, 0),
			overlaps: true, merged: fwd(2, 0, 4, 0)},
		{name: "a point inside a range", a: fwd(3, 0, 3, 0), b: fwd(2, 0, 4, 0),
			overlaps: true, merged: fwd(2, 0, 4, 0)},
		{name: "a point at the to of a range", a: fwd(4, 0, 4, 0), b: fwd(2, 0, 4, 0),
			overlaps: false, merged: fwd(2, 0, 4, 0)},
		{name: "across rows", a: fwd(2, 0, 1, 1), b: fwd(0, 1, 3, 1), overlaps: true,
			merged: fwd(2, 0, 3, 1)},
		{name: "both backward keeps the direction", a: fwd(3, 0, 0, 0), b: fwd(5, 0, 2, 0),
			overlaps: true, merged: fwd(5, 0, 0, 0)},
		{name: "mixed directions faces forward", a: fwd(3, 0, 0, 0), b: fwd(2, 0, 5, 0),
			overlaps: true, merged: fwd(0, 0, 5, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.overlaps, tc.a.overlaps(tc.b), "a overlaps b")
			assert.Equal(t, tc.overlaps, tc.b.overlaps(tc.a), "b overlaps a")
			assert.Equal(t, tc.merged, tc.a.merged(tc.b), "a merged b")
			assert.Equal(t, tc.merged, tc.b.merged(tc.a), "b merged a")
		})
	}
}

// TestSelectionNormalize pins Selection::normalize: the result is
// sorted, overlapping neighbours are merged, and the primary index
// follows the range it was on through sorting and merging.
func TestSelectionNormalize(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   selection
		want selection
	}{
		{name: "single range is untouched",
			in:   selection{ranges: []rng{fwd(3, 0, 1, 0)}},
			want: selection{ranges: []rng{fwd(3, 0, 1, 0)}}},
		{name: "sorts by from",
			in:   selection{ranges: []rng{fwd(4, 0, 6, 0), fwd(0, 1, 2, 1), fwd(0, 0, 2, 0)}, primary: 1},
			want: selection{ranges: []rng{fwd(0, 0, 2, 0), fwd(4, 0, 6, 0), fwd(0, 1, 2, 1)}, primary: 2}},
		{name: "merges overlaps and keeps the primary on the merged range",
			in:   selection{ranges: []rng{fwd(0, 0, 3, 0), fwd(2, 0, 5, 0), fwd(7, 0, 8, 0)}, primary: 1},
			want: selection{ranges: []rng{fwd(0, 0, 5, 0), fwd(7, 0, 8, 0)}}},
		{name: "a chain of overlaps collapses to one",
			in:   selection{ranges: []rng{fwd(0, 0, 2, 0), fwd(1, 0, 3, 0), fwd(2, 0, 4, 0)}, primary: 2},
			want: selection{ranges: []rng{fwd(0, 0, 4, 0)}}},
		{name: "primary after a merge shifts down",
			in:   selection{ranges: []rng{fwd(0, 0, 3, 0), fwd(2, 0, 5, 0), fwd(7, 0, 8, 0)}, primary: 2},
			want: selection{ranges: []rng{fwd(0, 0, 5, 0), fwd(7, 0, 8, 0)}, primary: 1}},
		{name: "touching ranges stay separate",
			in:   selection{ranges: []rng{fwd(2, 0, 4, 0), fwd(0, 0, 2, 0)}},
			want: selection{ranges: []rng{fwd(0, 0, 2, 0), fwd(2, 0, 4, 0)}, primary: 1}},
		{name: "duplicate points merge",
			in:   selection{ranges: []rng{fwd(1, 0, 1, 0), fwd(1, 0, 1, 0)}, primary: 1},
			want: selection{ranges: []rng{fwd(1, 0, 1, 0)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.in.normalized())
		})
	}
}

// TestSelectionEdits pins with, without and rotated.
func TestSelectionEdits(t *testing.T) {
	base := selection{ranges: []rng{fwd(0, 0, 1, 0), fwd(3, 0, 4, 0), fwd(6, 0, 7, 0)}, primary: 1}

	t.Run("with appends and makes the new range primary", func(t *testing.T) {
		got := base.with(fwd(9, 0, 10, 0))
		assert.Equal(t, 4, got.len())
		assert.Equal(t, 3, got.primary)
		assert.Equal(t, fwd(9, 0, 10, 0), got.primaryRange())
		assert.Equal(t, 1, base.primary, "the receiver is left alone")
	})

	t.Run("with sorts the new range into place", func(t *testing.T) {
		got := base.with(fwd(2, 0, 2, 0))
		assert.Equal(t, 1, got.primary)
		assert.Equal(t, fwd(2, 0, 2, 0), got.primaryRange())
	})

	t.Run("with merges into an overlapping range", func(t *testing.T) {
		got := base.with(fwd(3, 0, 5, 0))
		assert.Equal(t, 3, got.len())
		assert.Equal(t, fwd(3, 0, 5, 0), got.primaryRange())
	})

	t.Run("without a range before the primary", func(t *testing.T) {
		got := base.without(0)
		assert.Equal(t, []rng{fwd(3, 0, 4, 0), fwd(6, 0, 7, 0)}, got.ranges)
		assert.Equal(t, 0, got.primary)
	})

	t.Run("without the primary itself moves to the next", func(t *testing.T) {
		got := base.without(1)
		assert.Equal(t, []rng{fwd(0, 0, 1, 0), fwd(6, 0, 7, 0)}, got.ranges)
		assert.Equal(t, 1, got.primary)
	})

	t.Run("without the last primary moves to the previous", func(t *testing.T) {
		got := selection{ranges: base.ranges, primary: 2}.without(2)
		assert.Equal(t, 1, got.primary)
	})

	t.Run("without keeps the last range", func(t *testing.T) {
		got := single(fwd(0, 0, 1, 0)).without(0)
		assert.Equal(t, 1, got.len())
	})

	t.Run("rotated wraps in both directions", func(t *testing.T) {
		assert.Equal(t, 2, base.rotated(1).primary)
		assert.Equal(t, 0, base.rotated(2).primary)
		assert.Equal(t, 0, base.rotated(-1).primary)
		assert.Equal(t, 2, base.rotated(-2).primary)
		assert.Equal(t, 1, base.rotated(3).primary)
		assert.Equal(t, 0, single(fwd(0, 0, 1, 0)).rotated(5).primary)
	})
}

// TestSelectionTransformAndMap pins that transform and mapThrough both
// normalize their result.
func TestSelectionTransformAndMap(t *testing.T) {
	s := selection{ranges: []rng{fwd(0, 0, 2, 0), fwd(4, 0, 6, 0)}, primary: 1}

	got := s.transform(func(r rng) rng {
		r.head.X += 3
		return r
	})
	require.Equal(t, 1, got.len(), "grown ranges merge")
	assert.Equal(t, fwd(0, 0, 9, 0), got.primaryRange())

	got = s.mapThrough(changeSet{replacing(xy(2, 0), xy(4, 0), "")})
	assert.Equal(t, []rng{fwd(0, 0, 2, 0), fwd(2, 0, 4, 0)}, got.ranges)
	assert.Equal(t, 1, got.primary)
	assert.Equal(t, s, s.mapThrough(nil))
}

// TestRangeTextAndFragments pins the text a range covers, across rows
// and with a combining mark that shares a cell with its base.
func TestRangeTextAndFragments(t *testing.T) {
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("ab\u0301c\ndef\n\nghi"))
	for _, tc := range []struct {
		name string
		r    rng
		want string
	}{
		{name: "within a row", r: fwd(0, 0, 2, 0), want: "ab\u0301"},
		{name: "backward reads the same", r: fwd(2, 0, 0, 0), want: "ab\u0301"},
		{name: "to the line ending", r: fwd(1, 0, 3, 0), want: "b\u0301c"},
		{name: "across the line ending", r: fwd(1, 0, 0, 1), want: "b\u0301c\n"},
		{name: "over a blank row", r: fwd(2, 1, 1, 3), want: "f\n\ng"},
		{name: "a point", r: fwd(1, 1, 1, 1), want: ""},
		{name: "past the buffer end", r: fwd(2, 3, 0, 9), want: "i"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, rangeText(buf, tc.r))
		})
	}

	s := selection{ranges: []rng{fwd(0, 0, 1, 0), fwd(0, 1, 3, 1), fwd(0, 3, 1, 3)}}
	assert.Equal(t, []string{"a", "def", "g"}, s.fragments(buf))
}
