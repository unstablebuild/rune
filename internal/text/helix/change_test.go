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
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

func xy(x, y int) term.Coordinates { return term.Coordinates{X: x, Y: y} }

// TestChangeSetMapPos pins ChangeSet::map_pos over every arm of the
// mapping: positions before, at, inside and after an edit, on the same
// row and on later rows, for inserts, deletes and replaces.
func TestChangeSetMapPos(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cs         changeSet
		pos        term.Coordinates
		wantBefore term.Coordinates
		wantAfter  term.Coordinates
	}{
		// insert "xy" at (2,0)
		{name: "insert: before", cs: changeSet{replacing(xy(2, 0), xy(2, 0), "xy")},
			pos: xy(1, 0), wantBefore: xy(1, 0), wantAfter: xy(1, 0)},
		{name: "insert: at point", cs: changeSet{replacing(xy(2, 0), xy(2, 0), "xy")},
			pos: xy(2, 0), wantBefore: xy(2, 0), wantAfter: xy(4, 0)},
		{name: "insert: same row after", cs: changeSet{replacing(xy(2, 0), xy(2, 0), "xy")},
			pos: xy(5, 0), wantBefore: xy(7, 0), wantAfter: xy(7, 0)},
		{name: "insert: later row", cs: changeSet{replacing(xy(2, 0), xy(2, 0), "xy")},
			pos: xy(5, 3), wantBefore: xy(5, 3), wantAfter: xy(5, 3)},

		// insert "a\nb" at (2,0): a line break shifts later rows down
		{name: "multi-line insert: at point", cs: changeSet{replacing(xy(2, 0), xy(2, 0), "a\nb")},
			pos: xy(2, 0), wantBefore: xy(2, 0), wantAfter: xy(1, 1)},
		{name: "multi-line insert: same row after", cs: changeSet{replacing(xy(2, 0), xy(2, 0), "a\nb")},
			pos: xy(5, 0), wantBefore: xy(4, 1), wantAfter: xy(4, 1)},
		{name: "multi-line insert: later row", cs: changeSet{replacing(xy(2, 0), xy(2, 0), "a\nb")},
			pos: xy(5, 2), wantBefore: xy(5, 3), wantAfter: xy(5, 3)},

		// delete [2,5) on row 0
		{name: "delete: before", cs: changeSet{replacing(xy(2, 0), xy(5, 0), "")},
			pos: xy(1, 0), wantBefore: xy(1, 0), wantAfter: xy(1, 0)},
		{name: "delete: at from", cs: changeSet{replacing(xy(2, 0), xy(5, 0), "")},
			pos: xy(2, 0), wantBefore: xy(2, 0), wantAfter: xy(2, 0)},
		{name: "delete: inside", cs: changeSet{replacing(xy(2, 0), xy(5, 0), "")},
			pos: xy(3, 0), wantBefore: xy(2, 0), wantAfter: xy(2, 0)},
		{name: "delete: at to", cs: changeSet{replacing(xy(2, 0), xy(5, 0), "")},
			pos: xy(5, 0), wantBefore: xy(2, 0), wantAfter: xy(2, 0)},
		{name: "delete: same row after", cs: changeSet{replacing(xy(2, 0), xy(5, 0), "")},
			pos: xy(8, 0), wantBefore: xy(5, 0), wantAfter: xy(5, 0)},
		{name: "delete: later row", cs: changeSet{replacing(xy(2, 0), xy(5, 0), "")},
			pos: xy(8, 4), wantBefore: xy(8, 4), wantAfter: xy(8, 4)},

		// delete a line ending: [3,0) .. (0,1)
		{name: "join: later row moves up", cs: changeSet{replacing(xy(3, 0), xy(0, 1), "")},
			pos: xy(2, 1), wantBefore: xy(5, 0), wantAfter: xy(5, 0)},
		{name: "join: rows further down shift", cs: changeSet{replacing(xy(3, 0), xy(0, 1), "")},
			pos: xy(2, 4), wantBefore: xy(2, 3), wantAfter: xy(2, 3)},

		// replace [2,5) with "Q"
		{name: "replace: at from sticks to the start", cs: changeSet{replacing(xy(2, 0), xy(5, 0), "Q")},
			pos: xy(2, 0), wantBefore: xy(2, 0), wantAfter: xy(2, 0)},
		{name: "replace: inside", cs: changeSet{replacing(xy(2, 0), xy(5, 0), "Q")},
			pos: xy(4, 0), wantBefore: xy(2, 0), wantAfter: xy(3, 0)},
		{name: "replace: at to", cs: changeSet{replacing(xy(2, 0), xy(5, 0), "Q")},
			pos: xy(5, 0), wantBefore: xy(3, 0), wantAfter: xy(3, 0)},
		{name: "replace: same row after", cs: changeSet{replacing(xy(2, 0), xy(5, 0), "Q")},
			pos: xy(9, 0), wantBefore: xy(7, 0), wantAfter: xy(7, 0)},

		// replace a whole line span with a longer multi-line text
		{name: "multi-line replace: later row",
			cs:  changeSet{replacing(xy(0, 1), xy(0, 2), "a\nb\nc\n")},
			pos: xy(3, 5), wantBefore: xy(3, 7), wantAfter: xy(3, 7)},

		// two sequential changes: delete [0,2) then insert "zz" at (1,0)
		{name: "sequential: folded in order",
			cs:  changeSet{replacing(xy(0, 0), xy(2, 0), ""), replacing(xy(1, 0), xy(1, 0), "zz")},
			pos: xy(5, 0), wantBefore: xy(5, 0), wantAfter: xy(5, 0)},
		{name: "sequential: lands on the second insert",
			cs:  changeSet{replacing(xy(0, 0), xy(2, 0), ""), replacing(xy(1, 0), xy(1, 0), "zz")},
			pos: xy(3, 0), wantBefore: xy(1, 0), wantAfter: xy(3, 0)},
		{name: "empty set is the identity", cs: nil,
			pos: xy(3, 2), wantBefore: xy(3, 2), wantAfter: xy(3, 2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wantBefore, tc.cs.mapPos(tc.pos, assocBefore), "before")
			assert.Equal(t, tc.wantAfter, tc.cs.mapPos(tc.pos, assocAfter), "after")
		})
	}
}

// TestRangeMapThroughAssoc pins Range::map's associativity: from is
// carried after an insert at its position, to is carried before one,
// and a point follows the inserted text.
func TestRangeMapThroughAssoc(t *testing.T) {
	insertAt := func(x int) changeSet { return changeSet{replacing(xy(x, 0), xy(x, 0), "++")} }
	for _, tc := range []struct {
		name string
		r    rng
		cs   changeSet
		want rng
	}{
		{name: "insert at from pushes the range along",
			r: rng{anchor: xy(2, 0), head: xy(5, 0)}, cs: insertAt(2),
			want: rng{anchor: xy(4, 0), head: xy(7, 0)}},
		{name: "insert at to is left outside",
			r: rng{anchor: xy(2, 0), head: xy(5, 0)}, cs: insertAt(5),
			want: rng{anchor: xy(2, 0), head: xy(5, 0)}},
		{name: "backward range keeps its direction",
			r: rng{anchor: xy(5, 0), head: xy(2, 0)}, cs: insertAt(2),
			want: rng{anchor: xy(7, 0), head: xy(4, 0)}},
		{name: "backward range: insert at to is left outside",
			r: rng{anchor: xy(5, 0), head: xy(2, 0)}, cs: insertAt(5),
			want: rng{anchor: xy(5, 0), head: xy(2, 0)}},
		{name: "a point follows the text",
			r: rng{anchor: xy(3, 0), head: xy(3, 0)}, cs: insertAt(3),
			want: rng{anchor: xy(5, 0), head: xy(5, 0)}},
		{name: "a range inside a delete collapses",
			r:    rng{anchor: xy(3, 0), head: xy(4, 0)},
			cs:   changeSet{replacing(xy(1, 0), xy(6, 0), "")},
			want: rng{anchor: xy(1, 0), head: xy(1, 0)}},
		{name: "an untouched range is unchanged",
			r: rng{anchor: xy(3, 1), head: xy(4, 1)}, cs: insertAt(9),
			want: rng{anchor: xy(3, 1), head: xy(4, 1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.r.mapThrough(tc.cs))
		})
	}
}

// TestChangeRecorderCapturesEdits pins what the recorder sees for the
// edit shapes the operators produce, including a grapheme cluster that
// takes one cell for two runes.
func TestChangeRecorderCapturesEdits(t *testing.T) {
	buf := cell.NewBuffer()
	buf.ReadFrom(strings.NewReader("hello\nworld"))
	var rec changeRecorder
	buf.Subscribe(&rec)
	ctx := context.Background()

	buf.Edit(ctx, xy(1, 0), xy(1, 0), "taken")
	rec.take()
	buf.Edit(ctx, xy(0, 0), xy(2, 0), "")
	buf.Edit(ctx, xy(0, 1), xy(0, 1), "e\u0301x")
	buf.Edit(ctx, xy(1, 0), xy(0, 1), "")
	got := rec.take()
	buf.Edit(ctx, xy(0, 0), xy(0, 0), "later")

	require.Equal(t, changeSet{
		{from: xy(0, 0), to: xy(2, 0), end: xy(0, 0)},
		{from: xy(0, 1), to: xy(0, 1), end: xy(2, 1)},
		{from: xy(1, 0), to: xy(0, 1), end: xy(1, 0)},
	}, got)
	assert.Len(t, rec.changes, 1, "take hands the recording over")
}

// TestChangeSetMapPosOracle checks the mapping against the buffer
// itself: a unique sentinel is planted at a random position, random
// edits are recorded, and mapPos of the original position must land on
// wherever the sentinel ended up, as long as no edit deleted it. The
// sentinel sits on the position, so an insert exactly there lands in
// front of it: that is the after association.
func TestChangeSetMapPosOracle(t *testing.T) {
	const sentinel = '§'
	rnd := rand.New(rand.NewSource(1))
	alphabet := []rune("ab \n")

	randomPos := func(buf *cell.Buffer) term.Coordinates {
		y := rnd.Intn(buf.Rows())
		return xy(rnd.Intn(buf.Columns(y)+1), y)
	}
	randomText := func() string {
		var b strings.Builder
		for range rnd.Intn(4) {
			b.WriteRune(alphabet[rnd.Intn(len(alphabet))])
		}
		return b.String()
	}
	find := func(buf *cell.Buffer) (term.Coordinates, bool) {
		for y, row := range buf.View().RawCells() {
			for x, c := range row {
				if c.Ch == sentinel {
					return xy(x, y), true
				}
			}
		}
		return term.Coordinates{}, false
	}

	for trial := range 2000 {
		buf := cell.NewBuffer()
		var content strings.Builder
		for range 1 + rnd.Intn(30) {
			content.WriteRune(alphabet[rnd.Intn(len(alphabet))])
		}
		buf.ReadFrom(strings.NewReader(content.String()))
		var rec changeRecorder
		buf.Subscribe(&rec)
		ctx := context.Background()

		orig := randomPos(buf)
		buf.Edit(ctx, orig, orig, string(sentinel))

		rec.take()
		deleted := false
		for range 1 + rnd.Intn(4) {
			pos, ok := find(buf)
			require.True(t, ok, "trial %d: sentinel lost without a covering delete", trial)
			from := randomPos(buf)
			to := from
			if rnd.Intn(2) == 0 {
				to = randomPos(buf)
			}
			from, to = term.CoordinatesSort(from, to)
			if !coordinatesBefore(pos, from) && coordinatesBefore(pos, to) {
				deleted = true
			}
			buf.Edit(ctx, from, to, randomText())
			if deleted {
				break
			}
		}
		cs := rec.take()
		if deleted {
			continue
		}
		want, ok := find(buf)
		require.True(t, ok, "trial %d", trial)
		assert.Equal(t, want, cs.mapPos(orig, assocAfter),
			"trial %d: %q planted at %v through %+v", trial, content.String(), orig, cs)
	}
}
