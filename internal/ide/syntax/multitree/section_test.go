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

package multitree

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func xy(x, y int) term.Coordinates {
	return term.Coordinates{X: x, Y: y}
}

func rng(sx, sy, ex, ey int) term.Range {
	return term.Range{Start: xy(sx, sy), End: xy(ex, ey)}
}

func TestToLocalToHost(t *testing.T) {
	cases := []struct {
		name  string
		start term.Coordinates
		host  term.Coordinates
		local term.Coordinates
	}{
		{name: "section at origin", start: xy(0, 0), host: xy(4, 2), local: xy(4, 2)},
		{name: "first row shifts columns", start: xy(8, 3), host: xy(12, 3), local: xy(4, 0)},
		{name: "later rows keep columns", start: xy(8, 3), host: xy(2, 5), local: xy(2, 2)},
		{name: "section start", start: xy(8, 3), host: xy(8, 3), local: xy(0, 0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.local, toLocal(c.start, c.host))
			assert.Equal(t, c.host, toHost(c.start, c.local))
		})
	}
}

func TestSectionSnapshotRanges(t *testing.T) {
	s := sectionSnapshot{rng: rng(8, 3, 0, 9)}
	assert.Equal(t, rng(1, 0, 3, 2), s.rangeToLocal(rng(9, 3, 3, 5)))
	assert.Equal(t, rng(9, 3, 3, 5), s.rangeToHost(rng(1, 0, 3, 2)))
	assert.True(t, s.contains(rng(8, 3, 0, 9)))
	assert.True(t, s.contains(rng(0, 4, 0, 5)))
	assert.False(t, s.contains(rng(7, 3, 0, 5)))
	assert.False(t, s.contains(rng(0, 4, 1, 9)))
}

func TestShift(t *testing.T) {
	cases := []struct {
		name           string
		p, oldEnd, new term.Coordinates
		want           term.Coordinates
	}{
		{name: "insert line above", p: xy(3, 5), oldEnd: xy(0, 1), new: xy(0, 2), want: xy(3, 6)},
		{name: "delete line above", p: xy(3, 5), oldEnd: xy(0, 2), new: xy(0, 1), want: xy(3, 4)},
		{name: "insert on same row before", p: xy(10, 5), oldEnd: xy(4, 5), new: xy(7, 5), want: xy(13, 5)},
		{name: "delete on same row before", p: xy(10, 5), oldEnd: xy(4, 5), new: xy(2, 5), want: xy(8, 5)},
		{name: "newline on same row before", p: xy(10, 5), oldEnd: xy(4, 5), new: xy(0, 6), want: xy(6, 6)},
		{name: "join rows before", p: xy(3, 6), oldEnd: xy(0, 6), new: xy(9, 5), want: xy(12, 5)},
		{name: "no-op", p: xy(3, 6), oldEnd: xy(0, 2), new: xy(0, 2), want: xy(3, 6)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, shift(c.p, c.oldEnd, c.new))
		})
	}
}

func TestRangesOverlap(t *testing.T) {
	cases := []struct {
		name string
		a, b term.Range
		want bool
	}{
		{name: "disjoint rows", a: rng(0, 1, 0, 3), b: rng(0, 5, 0, 7)},
		{name: "adjacent", a: rng(0, 1, 0, 3), b: rng(0, 3, 0, 7)},
		{name: "nested", a: rng(0, 1, 0, 9), b: rng(0, 3, 0, 7), want: true},
		{name: "partial", a: rng(0, 1, 0, 5), b: rng(0, 3, 0, 7), want: true},
		{name: "same row disjoint", a: rng(0, 1, 4, 1), b: rng(4, 1, 9, 1)},
		{name: "same row overlap", a: rng(0, 1, 5, 1), b: rng(4, 1, 9, 1), want: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, rangesOverlap(c.a, c.b))
			assert.Equal(t, c.want, rangesOverlap(c.b, c.a))
		})
	}
}
