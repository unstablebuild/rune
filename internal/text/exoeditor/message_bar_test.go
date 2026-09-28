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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/term/vte/vteprobe"
	"unstable.build/rune/internal/text"
)

// stubTextHandler is a no-op text.Handler used as the inner of a
// messageBar test. Its Resize records the last height it received so
// tests can assert the bar reserved the right number of rows.
type stubTextHandler struct {
	lastWidth  int
	lastHeight int
}

func (s *stubTextHandler) Resize(w, h int) {
	s.lastWidth = w
	s.lastHeight = h
}

func (s *stubTextHandler) Draw(term.Writer)               {}
func (s *stubTextHandler) Handle(term.Event) (bool, bool) { return false, false }
func (s *stubTextHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}
func (s *stubTextHandler) Selection() (string, bool)  { return "", false }
func (s *stubTextHandler) Close() error               { return nil }
func (s *stubTextHandler) Resource() workspaceapi.URI { return workspaceapi.URI{} }
func (s *stubTextHandler) SetWrap(bool)               {}
func (s *stubTextHandler) ShowCommandBar(bool)        {}
func (s *stubTextHandler) SetCursorAtScroll(term.Coordinates) bool {
	return false
}
func (s *stubTextHandler) CursorAtScroll() term.Coordinates { return term.Coordinates{} }
func (s *stubTextHandler) SetLocationList(textapi.LocationPriority, string, text.LocationList) {
}
func (s *stubTextHandler) LocationLists() []text.LocationSet    { return nil }
func (s *stubTextHandler) MoveToNextLocation(string) bool       { return false }
func (s *stubTextHandler) MoveToPrevLocation(string) bool       { return false }
func (s *stubTextHandler) CellView() cell.View                  { return nil }
func (s *stubTextHandler) CellEditor() cell.Editor              { return nil }
func (s *stubTextHandler) SetDefaultAttributes(term.Attributes) {}
func (s *stubTextHandler) Dimensions() (int, int)               { return 0, 0 }
func (s *stubTextHandler) IsSearchMode() bool                   { return false }
func (s *stubTextHandler) IsNormalMode() bool                   { return false }
func (s *stubTextHandler) SeekUp() bool                         { return false }
func (s *stubTextHandler) SeekDown() bool                       { return false }
func (s *stubTextHandler) SeekOffset() int                      { return 0 }
func (s *stubTextHandler) MaxSeekOffset() int                   { return 0 }

// editorHandlerWithCursor returns a minimally-initialized editorHandler
// whose CursorAtScroll returns the given coordinates by stuffing them
// into a probe stored on lastProbe. Only the fields needed by
// LocationMessageAtCursor are wired.
func editorHandlerWithCursor(t *testing.T, cursor term.Coordinates) *editorHandler {
	t.Helper()
	h := &editorHandler{locations: text.NewLocationStore()}
	h.lastProbe.Store(&vteprobe.Result{CursorAtScroll: cursor})
	return h
}

// TestEditorHandlerLocationMessageBar exercises the exo message bar
// in three scenarios: no probe (cursor unknown), cursor off any
// location, and cursor on a location with a non-empty Message. It
// asserts both LocationMessageAtCursor and the rendered frame so a
// regression in either the lookup or the layout is caught.
func TestEditorHandlerLocationMessageBar(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cursor   term.Coordinates
		setProbe bool
		message  string
		frame    string
		barRows  int
	}{
		{
			name:     "no probe yields no message",
			setProbe: false,
			message:  "",
			frame: `
               
               
               
               
               `,
			barRows: 0,
		},
		{
			name:     "cursor off location yields no message",
			cursor:   term.Coordinates{X: 0, Y: 4},
			setProbe: true,
			message:  "",
			frame: `
               
               
               
               
               `,
			barRows: 0,
		},
		{
			name:     "cursor on location renders message",
			cursor:   term.Coordinates{X: 2, Y: 1},
			setProbe: true,
			message:  "hello",
			frame: `
               
               
               
               
hello          `,
			barRows: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &editorHandler{locations: text.NewLocationStore()}
			if tc.setProbe {
				h.lastProbe.Store(&vteprobe.Result{
					CursorAtScroll: tc.cursor,
				})
			}
			h.locations.SetLocationList(0, "diag", sliceList([]textapi.Location{
				{
					From:    term.Coordinates{X: 0, Y: 1},
					To:      term.Coordinates{X: 5, Y: 1},
					Message: "hello",
				},
				{
					From: term.Coordinates{X: 0, Y: 2},
					To:   term.Coordinates{X: 5, Y: 2},
				},
			}))

			require.Equal(t, tc.message, h.LocationMessageAtCursor())

			inner := &stubTextHandler{}
			bar := withMessageBar(inner, h)
			w := term.NewStringWriter(15, 5)
			bar.Resize(15, 5)
			comptest.TestComponent(t, bar, w, []comptest.TestCase{
				{Expected: tc.frame},
			})
			assert.Equal(t, 5-tc.barRows, inner.lastHeight,
				"inner should be sized to leftover height")
			assert.Equal(t, 15, inner.lastWidth)
		})
	}
}

// TestEditorHandlerLocationMessageBarFirstNonEmptyWins covers the
// precedence rule: when several locations cover the cursor, the
// first location with a non-empty Message wins. The order is
// determined by the LocationStore message bucket; we just assert one
// of the configured non-empty messages is rendered and that empty
// messages are skipped.
func TestEditorHandlerLocationMessageBarFirstNonEmptyWins(t *testing.T) {
	t.Parallel()
	h := editorHandlerWithCursor(t, term.Coordinates{X: 0, Y: 0})
	h.locations.SetLocationList(0, "a", sliceList([]textapi.Location{
		{From: term.Coordinates{}, To: term.Coordinates{X: 1}, Message: ""},
	}))
	h.locations.SetLocationList(1, "b", sliceList([]textapi.Location{
		{From: term.Coordinates{}, To: term.Coordinates{X: 1}, Message: "winner"},
	}))
	assert.Equal(t, "winner", h.LocationMessageAtCursor())
}

// TestEditorHandlerLocationMessageBarNoLocations guards the no-list
// path: LocationMessageAtCursor must return "" when no list has
// been set, and the bar must stay collapsed.
func TestEditorHandlerLocationMessageBarNoLocations(t *testing.T) {
	t.Parallel()
	h := editorHandlerWithCursor(t, term.Coordinates{})
	assert.Empty(t, h.LocationMessageAtCursor())

	inner := &stubTextHandler{}
	bar := withMessageBar(inner, h)
	bar.Resize(20, 4)
	assert.Equal(t, 4, inner.lastHeight)
}

// TestBarHeightFor covers the wrapping + clamp logic that decides
// how many rows the bar should claim.
func TestBarHeightFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		msg    string
		width  int
		height int
		want   int
	}{
		{"empty stays collapsed", "", 10, 5, 0},
		{"fits in one row", "hello", 10, 5, 1},
		{"wraps to two rows", "hello world!!", 10, 5, 2},
		{"capped by host height", "abcdefghij", 2, 3, 3},
		{"capped by max bar height", repeatRune('x', 21*5), 5, 100, maxMessageBarHeight},
		{"zero width is collapsed", "hi", 0, 5, 0},
		{"zero height is collapsed", "hi", 5, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want,
				barHeightFor(tc.msg, tc.width, tc.height))
		})
	}
}

func repeatRune(r rune, n int) string {
	runes := make([]rune, n)
	for i := range runes {
		runes[i] = r
	}
	return string(runes)
}

// TestEditorHandlerLocationMessageBarWraps drives the messageBar at
// width=5 with a message that needs two rows, and asserts the
// bottom-most rows render the message wrapped while the inner is
// resized to leave room for both.
func TestEditorHandlerLocationMessageBarWraps(t *testing.T) {
	t.Parallel()
	h := &editorHandler{locations: text.NewLocationStore()}
	h.lastProbe.Store(&vteprobe.Result{
		CursorAtScroll: term.Coordinates{X: 2, Y: 0},
	})
	h.locations.SetLocationList(0, "diag", sliceList([]textapi.Location{
		{
			From:    term.Coordinates{X: 0, Y: 0},
			To:      term.Coordinates{X: 5, Y: 0},
			Message: "hello world",
		},
	}))

	inner := &stubTextHandler{}
	bar := withMessageBar(inner, h)
	w := term.NewStringWriter(5, 5)
	bar.Resize(5, 5)
	comptest.TestComponent(t, bar, w, []comptest.TestCase{{Expected: `
     
     
hello
 worl
d    `}})
	assert.Equal(t, 2, inner.lastHeight,
		"inner should be shrunk to leave 3 rows for the bar")
}
