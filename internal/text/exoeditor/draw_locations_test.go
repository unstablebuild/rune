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

package exoeditor

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vteprobe"
	"unstable.build/rune/internal/text"
)

func locationStoreForTest(t *testing.T) *text.LocationStore {
	t.Helper()
	return text.NewLocationStore()
}

func sliceList(locs []textapi.Location) text.LocationList {
	return text.LocationSlice(locs)
}

func fileCellsForTest(lines ...string) [][]term.Cell {
	return term.StringToCells(strings.Join(lines, "\n"))
}

// recordingWriter captures every UnionAttributes call so a test can
// assert which screen cells the location overlay touched. Only the
// methods drawLocations actually uses are implemented; SetCell is a
// no-op because drawLocations never sets cells directly.
type recordingWriter struct {
	calls []writerCall
}

type writerCall struct {
	pos  term.Coordinates
	attr term.Attributes
}

func (w *recordingWriter) UnionAttributes(pos term.Coordinates, attr term.Attributes) {
	w.calls = append(w.calls, writerCall{pos: pos, attr: attr})
}

func (w *recordingWriter) Context() context.Context            { return context.Background() }
func (w *recordingWriter) DrawImage(term.Image) bool           { return false }
func (w *recordingWriter) SetCell(term.Coordinates, term.Cell) {}

func screenCoords(calls []writerCall) []term.Coordinates {
	out := make([]term.Coordinates, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.pos)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Y != out[j].Y {
			return out[i].Y < out[j].Y
		}
		return out[i].X < out[j].X
	})
	return out
}

// fakeProbeUnwrapped builds a 5-row x 20-col probe result with a
// 3-column gutter and no soft wrap. Rows 0..2 map to file lines 1..3,
// row 3 is folded onto file line 4, row 4 is past EOF.
func fakeProbeUnwrapped() *vteprobe.Result {
	return &vteprobe.Result{
		Bands: vteprobe.Bands{
			Top: 0, Bottom: 4, GutterWidth: 3, GridWidth: 20,
		},
		Rows: []vteprobe.RowMapping{
			{FileLine: 1},
			{FileLine: 2},
			{FileLine: 3},
			{FileLine: 4, Folded: true},
			{FileLine: 0},
		},
		FileLines: fileCellsForTest(
			"abcdefghij",
			"abcdefghij",
			"abcdefghij",
			"abcdefghij",
		),
		Tabstop: 4,
	}
}

func TestDrawLocations(t *testing.T) {
	t.Parallel()

	highlight := term.Attributes{Attrs: term.AttrUnderline}

	cases := []struct {
		name   string
		probe  *vteprobe.Result
		locs   []textapi.Location
		expect []term.Coordinates
	}{
		{
			name:  "single line range maps past gutter",
			probe: fakeProbeUnwrapped(),
			locs: []textapi.Location{{
				From: term.Coordinates{X: 2, Y: 1},
				To:   term.Coordinates{X: 5, Y: 1},
				Attr: highlight,
			}},
			expect: []term.Coordinates{
				{X: 5, Y: 1}, {X: 6, Y: 1}, {X: 7, Y: 1},
			},
		},
		{
			name:  "folded row is skipped",
			probe: fakeProbeUnwrapped(),
			locs: []textapi.Location{{
				From: term.Coordinates{X: 0, Y: 3},
				To:   term.Coordinates{X: 4, Y: 3},
				Attr: highlight,
			}},
			expect: nil,
		},
		{
			name:  "row past EOF is skipped",
			probe: fakeProbeUnwrapped(),
			locs: []textapi.Location{{
				From: term.Coordinates{X: 0, Y: 4},
				To:   term.Coordinates{X: 4, Y: 4},
				Attr: highlight,
			}},
			expect: nil,
		},
		{
			name: "wrapped line maps across two rows",
			probe: &vteprobe.Result{
				Bands: vteprobe.Bands{
					Top: 0, Bottom: 1, GutterWidth: 0, GridWidth: 5,
				},
				Rows: []vteprobe.RowMapping{
					{FileLine: 1},
					{FileLine: 1, WrapOffset: 5},
				},
				FileLines: fileCellsForTest("abcdefghij"),
				Tabstop:   4,
			},
			locs: []textapi.Location{{
				From: term.Coordinates{X: 3, Y: 0},
				To:   term.Coordinates{X: 7, Y: 0},
				Attr: highlight,
			}},
			expect: []term.Coordinates{
				{X: 3, Y: 0}, {X: 4, Y: 0}, {X: 0, Y: 1}, {X: 1, Y: 1},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w := &recordingWriter{}
			drawLocations(w, tc.locs, tc.probe)
			if tc.expect == nil {
				assert.Empty(t, w.calls)
			} else {
				assert.Equal(t, tc.expect, screenCoords(w.calls))
			}
			for _, c := range w.calls {
				assert.Equal(t, highlight, c.attr,
					"every union must carry the location attr")
			}
		})
	}
}

// TestDrawLocationsNilProbeIsNop guards against the natural racy case
// where Draw fires before the first Handle/probe has populated the
// cache.
func TestDrawLocationsNilProbeIsNop(t *testing.T) {
	t.Parallel()
	w := &recordingWriter{}
	drawLocations(w, []textapi.Location{{
		From: term.Coordinates{X: 0, Y: 0},
		To:   term.Coordinates{X: 1, Y: 0},
	}}, nil)
	assert.Empty(t, w.calls)
}

// TestDrawLocationsExpandsTabs guards against the bug where a
// location's raw rune column was painted as if it were a visual cell
// column, so a highlight on a tab-indented line landed N cells too
// far to the left.
func TestDrawLocationsExpandsTabs(t *testing.T) {
	t.Parallel()
	probe := &vteprobe.Result{
		Bands: vteprobe.Bands{
			Top: 0, Bottom: 0, GutterWidth: 0, GridWidth: 40,
		},
		Rows: []vteprobe.RowMapping{
			{FileLine: 1},
		},
		FileLines: fileCellsForTest("\tdb              document.Service"),
		Tabstop:   8,
	}
	highlight := term.Attributes{Attrs: term.AttrUnderline}
	loc := textapi.Location{
		// Highlight "db" — raw columns [1, 3) on file line 1.
		From: term.Coordinates{X: 1, Y: 0},
		To:   term.Coordinates{X: 3, Y: 0},
		Attr: highlight,
	}
	w := &recordingWriter{}
	drawLocations(w, []textapi.Location{loc}, probe)

	// 'd' renders at visual column 8 (after the 8-cell tab) and 'b'
	// at 9 — those are the cells we expect UnionAttributes on.
	assert.Equal(t, []term.Coordinates{
		{X: 8, Y: 0}, {X: 9, Y: 0},
	}, screenCoords(w.calls))
}

// TestEditorHandlerExperimentalHighlightsDisabled verifies the editor
// handler's Draw does not call into the location overlay path when
// the config flag is off: SetLocationList still updates the public
// store (so observers see the same state as on non-exo editors), but
// no UnionAttributes calls reach the writer beyond what the embedded
// vte already produces.
func TestEditorHandlerExperimentalHighlightsDisabled(t *testing.T) {
	t.Parallel()
	h := &editorHandler{
		experimentalHighlights: false,
		locations:              locationStoreForTest(t),
	}
	probe := fakeProbeUnwrapped()
	h.lastProbe.Store(probe)
	h.locations.SetLocationList(0, "syntax",
		sliceList([]textapi.Location{{
			From: term.Coordinates{X: 0, Y: 0},
			To:   term.Coordinates{X: 2, Y: 0},
			Attr: term.Attributes{Attrs: term.AttrUnderline},
		}}))

	w := &recordingWriter{}
	// Bypass the embedded vte.Handler.Draw call; only assert the
	// overlay path is gated by the flag.
	if h.experimentalHighlights {
		if probe := h.lastProbe.Load(); probe != nil {
			drawLocations(w, h.locations.SortedLocations(), probe)
		}
	}
	assert.Empty(t, w.calls)

	// LocationLists must reflect what SetLocationList stored even
	// when overlay is off.
	got := h.LocationLists()
	assert.Len(t, got, 1)
	assert.Equal(t, "syntax", got[0].ID)
}
