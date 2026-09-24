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
	"unstable.build/rune/internal/text"
)

func impl(hx *Helix) *helixHandlerImpl { return hx.handler.(*helixHandlerImpl) }

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
			assert.Equal(t, h.readRange(), h.sel.primaryRange(), "set mirrors the cursor")

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
