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

package idetutorial_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/idetutorial"
)

// fakeTutorial draws a fixed body and records what reached it.
type fakeTutorial struct {
	width, height int
	events        []term.Event
	handled       bool
	offset, max   int
	finished      bool
	completed     bool
	skips         int
	backs         int
	cursor        bool
	prompt        bool
	viewingPast   bool
	forwards      int
}

func (f *fakeTutorial) Resize(width, height int) { f.width, f.height = width, height }

func (f *fakeTutorial) Draw(w term.Writer) {
	for y := range f.height {
		for x := range f.width {
			w.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell('b', 1, term.Attributes{}))
		}
	}
}

func (f *fakeTutorial) Handle(ev term.Event) (bool, bool) {
	f.events = append(f.events, ev)
	return true, f.handled
}

func (f *fakeTutorial) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{X: 1, Y: 1}, term.CursorStyleDefault, f.cursor
}

func (f *fakeTutorial) Selection() (string, bool) { return "sel", true }
func (f *fakeTutorial) Reset()                    {}
func (f *fakeTutorial) Stop()                     {}
func (f *fakeTutorial) Skip() bool                { f.skips++; return f.finished }
func (f *fakeTutorial) Back() bool                { f.backs++; return true }
func (f *fakeTutorial) Forward() bool             { f.forwards++; return true }
func (f *fakeTutorial) ViewingPast() bool         { return f.viewingPast }
func (f *fakeTutorial) Finished() bool            { return f.finished }
func (f *fakeTutorial) Completed() bool           { return f.completed }
func (f *fakeTutorial) PromptActive() bool        { return f.prompt }
func (f *fakeTutorial) SeekUp() bool {
	if f.offset == 0 {
		return false
	}
	f.offset--
	return true
}

func (f *fakeTutorial) SeekDown() bool {
	if f.offset >= f.max {
		return false
	}
	f.offset++
	return true
}
func (f *fakeTutorial) SeekOffset() int    { return f.offset }
func (f *fakeTutorial) MaxSeekOffset() int { return f.max }
func (f *fakeTutorial) ObserveCommand(_, _ string, _ []string, _ error) bool {
	return f.finished
}
func (f *fakeTutorial) ObserveEvent(_, _ string) bool { return f.finished }

// framedStyle frames the tile the way a framed IDE frames its windows,
// with a distinct focus charset so a test can tell the two apart.
func framedStyle() idetutorial.TileStyle {
	return idetutorial.TileStyle{
		Frame:             true,
		FrameCharSet:      component.FrameCharSetDefault(),
		FocusFrameCharSet: component.FrameCharSetHighlight(),
		FrameAttr:         term.Attributes{Fg: term.ColorBlue},
		FocusFrameAttr:    term.Attributes{Fg: term.ColorRed},
		ScrollBarCharSet:  component.ScrollBarCharSet{Middle: '#'},
	}
}

func TestTileWidth(t *testing.T) {
	t.Parallel()
	cases := []struct{ total, want int }{
		{total: 40, want: idetutorial.MinTileWidth},
		{total: 96, want: idetutorial.MinTileWidth},
		{total: 120, want: 30},
		{total: 200, want: 50},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, idetutorial.TileWidth(tc.total), "total=%d", tc.total)
	}
}

func TestTileStacksBodyOverFooter(t *testing.T) {
	t.Parallel()
	tut := &fakeTutorial{}
	tile := idetutorial.NewTile(tut, idetutorial.TileStyle{}, func() {}, func() {}, func() {})
	const width = idetutorial.MinTileWidth
	tile.Resize(width, 6)

	assert.Equal(t, width, tut.width)
	assert.Equal(t, 4, tut.height, "the footer takes the last two rows")

	drawn := handlertest.DrawHandler(tile, width, 6)
	rows := strings.Split(drawn, "\n")
	require.Len(t, rows, 6)
	for _, row := range rows[:4] {
		assert.Equal(t, strings.Repeat("b", width), row)
	}
	assert.Equal(t, strings.Repeat(" ", width), rows[4],
		"a row of air keeps the buttons off the lesson")
	assert.Contains(t, rows[5], "Back")
	assert.Contains(t, rows[5], "Skip")
	assert.Contains(t, rows[5], "Stop")
}

func TestTileFramesItselfLikeAWindow(t *testing.T) {
	t.Parallel()
	tut := &fakeTutorial{max: 3}
	tile := idetutorial.NewTile(tut, framedStyle(), func() {}, func() {}, func() {})
	tile.Resize(20, 6)

	assert.Equal(t, 18, tut.width, "the frame takes a column on each side")
	assert.Equal(t, 2, tut.height, "the frame takes a row on each side and the footer two more")

	rows := strings.Split(handlertest.DrawHandler(tile, 20, 6), "\n")
	require.Len(t, rows, 6)
	assert.Equal(t, "┌"+strings.Repeat("─", 18)+"┐", rows[0])
	assert.True(t, strings.HasPrefix(rows[1], "│"+strings.Repeat("b", 18)),
		"the body is inset by the frame: %q", rows[1])
	assert.Equal(t, "└"+strings.Repeat("─", 18)+"┘", rows[5])
	assert.Contains(t, strings.Join(rows[1:5], "\n"), "#",
		"the frame draws the tutorial's scroll bar")
}

func TestTileFocusSwitchesTheFrame(t *testing.T) {
	t.Parallel()
	tile := idetutorial.NewTile(&fakeTutorial{}, framedStyle(),
		func() {}, func() {}, func() {})
	tile.Resize(20, 6)
	assert.False(t, tile.Focused())
	assert.True(t, strings.HasPrefix(handlertest.DrawHandler(tile, 20, 6), "┌"))

	tile.SetFocused(true)
	assert.True(t, tile.Focused())
	assert.True(t, strings.HasPrefix(handlertest.DrawHandler(tile, 20, 6), "┏"),
		"a focused tile draws the focus frame")

	tile.SetFocused(false)
	assert.True(t, strings.HasPrefix(handlertest.DrawHandler(tile, 20, 6), "┌"))
}

func TestTileKeepsTheRightInsetClear(t *testing.T) {
	t.Parallel()
	tut := &fakeTutorial{}
	tile := idetutorial.NewTile(tut, framedStyle(), func() {}, func() {}, func() {})
	tile.Resize(20, 6)
	tile.SetRightInset(4)

	assert.Equal(t, 14, tut.width, "the inset comes out of the tile's width")
	rows := strings.Split(handlertest.DrawHandler(tile, 20, 6), "\n")
	assert.Equal(t, "┌"+strings.Repeat("─", 14)+"┐    ", rows[0],
		"the frame stops short of the reserved column")

	_, handled := tile.Handle(term.Event{
		Type: term.EventMouse, Key: term.MouseLeft, MouseX: 18, MouseY: 2,
	})
	assert.False(t, handled, "a click in the reserved column is not the tile's")
	assert.Empty(t, tut.events)

	tile.Resize(30, 6)
	assert.Equal(t, 24, tut.width, "the inset survives a resize")
}

func TestTileFooterButtons(t *testing.T) {
	t.Parallel()
	press := func(x, y int) []term.Event {
		return []term.Event{
			{Type: term.EventMouse, Key: term.MouseLeft, MouseX: x, MouseY: y},
			{Type: term.EventMouse, Key: term.MouseRelease, MouseX: x, MouseY: y},
		}
	}
	tut := &fakeTutorial{}
	var backs, skips, stops int
	tile := idetutorial.NewTile(tut, framedStyle(), func() { backs++ },
		func() { skips++ }, func() { stops++ })
	tile.Resize(30, 6)
	row := strings.Split(handlertest.DrawHandler(tile, 30, 6), "\n")[4]
	backX := strings.Index(row, "Back")
	skipX := strings.Index(row, "Skip")
	stopX := strings.Index(row, "Stop")
	require.GreaterOrEqual(t, backX, 0)
	require.GreaterOrEqual(t, skipX, 0)
	require.GreaterOrEqual(t, stopX, 0)
	assert.Equal(t, skipX-backX, stopX-skipX,
		"the three buttons are evenly spaced: %q", row)

	cases := []struct {
		name                            string
		events                          []term.Event
		wantBacks, wantSkips, wantStops int
	}{
		{name: "back", events: press(backX, 4), wantBacks: 1},
		{name: "skip", events: press(skipX, 4), wantSkips: 1},
		{name: "stop", events: press(stopX, 4), wantStops: 1},
		{
			name: "release elsewhere cancels",
			events: []term.Event{
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: stopX, MouseY: 4},
				{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 1, MouseY: 4},
			},
		},
		{name: "body click", events: press(skipX, 2)},
		{name: "frame click", events: press(skipX, 5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backs, skips, stops = 0, 0, 0
			for _, ev := range tc.events {
				exit, _ := tile.Handle(ev)
				assert.False(t, exit, "the tile never asks to be closed")
			}
			assert.Equal(t, tc.wantBacks, backs)
			assert.Equal(t, tc.wantSkips, skips)
			assert.Equal(t, tc.wantStops, stops)
		})
	}
}

func TestTileMiddleButtonNamesWhatItDoes(t *testing.T) {
	t.Parallel()
	tut := &fakeTutorial{}
	var advances int
	tile := idetutorial.NewTile(tut, framedStyle(), func() {},
		func() { advances++ }, func() {})
	tile.Resize(30, 6)

	footer := func() string {
		return strings.Split(handlertest.DrawHandler(tile, 30, 6), "\n")[4]
	}
	assert.Contains(t, footer(), "Skip")
	assert.NotContains(t, footer(), "Next")

	tut.viewingPast = true
	row := footer()
	assert.Contains(t, row, "Next")
	assert.NotContains(t, row, "Skip")

	// Relabelling keeps the button clickable where it is drawn.
	nextX := strings.Index(row, "Next")
	require.GreaterOrEqual(t, nextX, 0)
	for _, ev := range []term.Event{
		{Type: term.EventMouse, Key: term.MouseLeft, MouseX: nextX, MouseY: 4},
		{Type: term.EventMouse, Key: term.MouseRelease, MouseX: nextX, MouseY: 4},
	} {
		_, _ = tile.Handle(ev)
	}
	assert.Equal(t, 1, advances)

	tut.viewingPast = false
	assert.Contains(t, footer(), "Skip")
}

func TestTileFooterButtonsHover(t *testing.T) {
	t.Parallel()
	text := term.Attributes{Bg: term.ColorGray}
	highlight := term.Attributes{Bg: term.ColorRed}
	hover := term.Attributes{Fg: term.ColorBlack, Bg: term.ColorSilver}
	clicked := term.Attributes{Fg: term.ColorWhite, Bg: term.ColorPurple}
	const width, height, footerY = 30, 6, 4
	move := func(x, y int) term.Event {
		return term.Event{Type: term.EventMouse, MouseX: x, MouseY: y}
	}

	newTile := func() (*idetutorial.Tile, *fakeTutorial) {
		style := framedStyle()
		style.Prompt = idetutorial.PromptStyle{
			TextAttr: text, HighlightAttr: highlight, HoverAttr: hover, ClickedAttr: clicked,
		}
		tut := &fakeTutorial{}
		tile := idetutorial.NewTile(tut, style, func() {}, func() {}, func() {})
		tile.Resize(width, height)
		return tile, tut
	}
	buttons := func(tile *idetutorial.Tile) (back, skip, stop int) {
		row := strings.Split(handlertest.DrawHandler(tile, width, height), "\n")[footerY]
		back = strings.Index(row, "Back")
		skip = strings.Index(row, "Skip")
		stop = strings.Index(row, "Stop")
		require.GreaterOrEqual(t, back, 0)
		require.GreaterOrEqual(t, skip, 0)
		require.GreaterOrEqual(t, stop, 0)
		return back, skip, stop
	}
	attrAt := func(tile *idetutorial.Tile, x int) term.Attributes {
		w := term.NewStringWriter(width, height)
		tile.Draw(w)
		return w.Cells()[footerY*width+x].Attributes()
	}
	tile, _ := newTile()
	backX, skipX, stopX := buttons(tile)

	cases := []struct {
		name                     string
		events                   []term.Event
		wantBack, wantSkip, want term.Attributes
	}{
		{
			name:     "no button looks chosen before the pointer comes",
			wantBack: text, wantSkip: text, want: text,
		},
		{
			name:     "the button under the pointer",
			events:   []term.Event{move(skipX, footerY)},
			wantBack: text, wantSkip: hover, want: text,
		},
		{
			name:     "the first button under the pointer",
			events:   []term.Event{move(backX, footerY)},
			wantBack: hover, wantSkip: text, want: text,
		},
		{
			name:     "the pointer moves on to the lesson",
			events:   []term.Event{move(skipX, footerY), move(skipX, 2)},
			wantBack: text, wantSkip: text, want: text,
		},
		{
			name: "a pressed button",
			events: []term.Event{
				move(stopX, footerY),
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: stopX, MouseY: footerY},
			},
			wantBack: text, wantSkip: text, want: clicked,
		},
		{
			name:     "the pointer moves on to the frame below",
			events:   []term.Event{move(skipX, footerY), move(skipX, height-1)},
			wantBack: text, wantSkip: text, want: text,
		},
		{
			name:     "the pointer moves off the tile to the left",
			events:   []term.Event{move(backX, footerY), move(-3, footerY)},
			wantBack: text, wantSkip: text, want: text,
		},
		{
			name:     "the pointer moves off the tile above",
			events:   []term.Event{move(backX, footerY), move(backX, -1)},
			wantBack: text, wantSkip: text, want: text,
		},
		{
			name: "a press dragged off the tile stays pressed",
			events: []term.Event{
				move(stopX, footerY),
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: stopX, MouseY: footerY},
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: stopX, MouseY: height - 1},
			},
			wantBack: text, wantSkip: text, want: clicked,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tile, _ := newTile()
			for _, ev := range tc.events {
				_, _ = tile.Handle(ev)
			}
			assert.Equal(t, tc.wantBack, attrAt(tile, backX), "Back")
			assert.Equal(t, tc.wantSkip, attrAt(tile, skipX), "Skip")
			assert.Equal(t, tc.want, attrAt(tile, stopX), "Stop")
		})
	}

	t.Run("the pointer moves into the reserved inset", func(t *testing.T) {
		tile, _ := newTile()
		tile.SetRightInset(4)
		_, _, stop := buttons(tile)
		_, _ = tile.Handle(move(stop, footerY))
		require.Equal(t, hover, attrAt(tile, stop))
		_, _ = tile.Handle(move(width-2, footerY))
		assert.Equal(t, text, attrAt(tile, stop))
	})

	t.Run("the lesson is told the pointer left it for the footer", func(t *testing.T) {
		tile, tut := newTile()
		_, _ = tile.Handle(move(skipX, 2))
		_, _ = tile.Handle(move(skipX, footerY))
		require.Len(t, tut.events, 2)
		leave := tut.events[1]
		assert.Equal(t, term.EventMouse, leave.Type)
		assert.Zero(t, leave.Key, "a leave is a motion")
		assert.GreaterOrEqual(t, leave.MouseY, tut.height, "below the lesson")
	})
}

func TestTilePointerLeaves(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		style idetutorial.TileStyle
		inset int
		tut   *fakeTutorial
	}{
		{name: "unframed", tut: &fakeTutorial{max: 5, handled: true}},
		{name: "framed", style: framedStyle(), tut: &fakeTutorial{max: 5, cursor: true}},
		{name: "framed with an inset", style: framedStyle(), inset: 4, tut: &fakeTutorial{viewingPast: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tile := idetutorial.NewTile(tc.tut, tc.style, func() {}, func() {}, func() {})
			tile.SetRightInset(tc.inset)
			for _, size := range []struct{ w, h int }{{60, 15}, {idetutorial.MinTileWidth, 6}, {1, 1}} {
				handlertest.RunPointerLeaves(t, tile, size.w, size.h)
			}
		})
	}
}

func TestTilePointerLeaveClicksNoButton(t *testing.T) {
	t.Parallel()
	const width, height, footerY = 30, 6, 4
	var clicks int
	click := func() { clicks++ }
	tile := idetutorial.NewTile(&fakeTutorial{}, framedStyle(), click, click, click)
	tile.Resize(width, height)
	row := strings.Split(handlertest.DrawHandler(tile, width, height), "\n")[footerY]
	stopX := strings.Index(row, "Stop")
	require.GreaterOrEqual(t, stopX, 0)
	for _, off := range []term.Coordinates{
		{X: -1, Y: footerY}, {X: width, Y: footerY}, {X: stopX, Y: height}, {X: stopX, Y: -400},
	} {
		_, _ = tile.Handle(term.Event{Type: term.EventMouse, MouseX: stopX, MouseY: footerY})
		_, _ = tile.Handle(term.Event{
			Type: term.EventMouse, Key: term.MouseLeft, MouseX: stopX, MouseY: footerY,
		})
		_, _ = tile.Handle(term.Event{Type: term.EventMouse, MouseX: off.X, MouseY: off.Y})
		assert.Zero(t, clicks, "a leave to %+v must not click the pressed button", off)
		_, _ = tile.Handle(term.Event{
			Type: term.EventMouse, Key: term.MouseRelease, MouseX: 0, MouseY: 0,
		})
		assert.Zero(t, clicks, "a release away from the button must not click it")
	}
}

func TestTileRoutesKeysAndBodyMouseToTheTutorial(t *testing.T) {
	t.Parallel()
	tut := &fakeTutorial{handled: true}
	tile := idetutorial.NewTile(tut, framedStyle(), func() {}, func() {}, func() {})
	tile.Resize(20, 6)

	key := term.Event{Type: term.EventKey, Key: term.KeyEnter}
	exit, handled := tile.Handle(key)
	assert.False(t, exit, "a tutorial's exit must never close the tile")
	assert.True(t, handled)

	wheel := term.Event{Type: term.EventMouse, Key: term.MouseWheelDown, MouseX: 3, MouseY: 2}
	_, handled = tile.Handle(wheel)
	assert.True(t, handled)

	footer := term.Event{Type: term.EventMouse, Key: term.MouseWheelDown, MouseX: 3, MouseY: 4}
	_, _ = tile.Handle(footer)

	require.Len(t, tut.events, 3)
	assert.Equal(t, key, tut.events[0])
	assert.Equal(t, term.Event{
		Type: term.EventMouse, Key: term.MouseWheelDown, MouseX: 2, MouseY: 1,
	}, tut.events[1], "body mouse events are translated past the frame")
	assert.Zero(t, tut.events[2].Key,
		"footer events must not reach the body: it is only told the pointer left")
}

func TestTileDelegatesCursorAndSelection(t *testing.T) {
	t.Parallel()
	tut := &fakeTutorial{cursor: true}
	tile := idetutorial.NewTile(tut, framedStyle(), func() {}, func() {}, func() {})
	tile.Resize(20, 6)

	pos, _, show := tile.Cursor()
	assert.True(t, show)
	assert.Equal(t, term.Coordinates{X: 2, Y: 2}, pos, "the cursor is offset by the frame")
	sel, ok := tile.Selection()
	assert.True(t, ok)
	assert.Equal(t, "sel", sel)
}
