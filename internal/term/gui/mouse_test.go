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

package gui

import (
	"image"
	"math"
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	tterm "unstable.build/rune/internal/term"
	"unstable.build/rune/internal/term/gui/font"
)

func TestMouseEvents(t *testing.T) {
	suite := []struct {
		description    string
		pressedButtons [][]ebiten.MouseButton
		cursorPosition []image.Point // in pixels
		wheel          []float64
		wheelX         []float64
		expectedEvents []term.Event
	}{
		{
			description: "does not dispatch if no mouse buttons are pressed or " +
				"wheel engaged, and position is the same",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{}, {}},
			wheel:          []float64{0, 0},
			expectedEvents: []term.Event{{}, {}},
		},
		{
			description:    "dispatches corrected if moved but outside of window on the x axis",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{}, {X: defaultWidth}},
			wheel:          []float64{0, 0},
			expectedEvents: []term.Event{
				{},
				{Type: term.EventMouse, MouseX: 87, MouseY: 0},
			},
		},
		{
			description:    "dispatches corrected if moved but outside of window on the x axis (negative)",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{}, {Y: 40, X: -defaultWidth}},
			wheel:          []float64{0, 0},
			expectedEvents: []term.Event{
				{},
				{Type: term.EventMouse, MouseX: 0, MouseY: 1},
			},
		},
		{
			description:    "dispatches corrected if moved but outside of window on the y axis",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{}, {Y: defaultHeight}},
			wheel:          []float64{0, 0},
			expectedEvents: []term.Event{
				{},
				{Type: term.EventMouse, MouseY: 27, MouseX: 0},
			},
		},
		{
			description:    "dispatches corrected if moved but outside of window on the y axis (negative)",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{}, {X: 20, Y: -defaultHeight}},
			wheel:          []float64{0, 0},
			expectedEvents: []term.Event{
				{},
				{Type: term.EventMouse, MouseX: 2, MouseY: 0},
			},
		},
		{
			description:    "dispatches mouse left click and subsequent release",
			pressedButtons: [][]ebiten.MouseButton{{ebiten.MouseButtonLeft}, {ebiten.MouseButtonLeft}, {}},
			cursorPosition: []image.Point{{}, {}, {}},
			wheel:          []float64{0, 0, 0},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseLeft},
				{},
				{Type: term.EventMouse, Key: term.MouseRelease},
			},
		},
		{
			description:    "dispatches mouse right click and subsequent release",
			pressedButtons: [][]ebiten.MouseButton{{ebiten.MouseButtonRight}, {ebiten.MouseButtonRight}, {}},
			cursorPosition: []image.Point{{}, {}, {}},
			wheel:          []float64{0, 0, 0},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseRight},
				{},
				{Type: term.EventMouse, Key: term.MouseRelease},
			},
		},
		{
			description:    "dispatches mouse middle click and subsequent release",
			pressedButtons: [][]ebiten.MouseButton{{ebiten.MouseButtonMiddle}, {ebiten.MouseButtonMiddle}, {}},
			cursorPosition: []image.Point{{}, {}, {}},
			wheel:          []float64{0, 0, 0},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseMiddle},
				{},
				{Type: term.EventMouse, Key: term.MouseRelease},
			},
		},
		{
			description:    "dispatches mouse wheel up and down",
			pressedButtons: [][]ebiten.MouseButton{{}, {}, {}},
			cursorPosition: []image.Point{{}, {}, {}},
			wheel:          []float64{1, 0, -1},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseWheelUp},
				{},
				{Type: term.EventMouse, Key: term.MouseWheelDown},
			},
		},
		{
			description:    "dispatches mouse cursor position changes",
			pressedButtons: [][]ebiten.MouseButton{{}, {}, {}},
			cursorPosition: []image.Point{{X: 100, Y: 100}, {}, {X: 100, Y: 100}},
			wheel:          []float64{0, 0, 0},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, MouseX: 11, MouseY: 4},
				{Type: term.EventMouse},
				{Type: term.EventMouse, MouseX: 11, MouseY: 4},
			},
		},
		{
			description: "dispatches mouse left click, drag and release",
			pressedButtons: [][]ebiten.MouseButton{
				{ebiten.MouseButtonLeft},
				{ebiten.MouseButtonLeft},
				{ebiten.MouseButtonLeft},
				{},
			},
			cursorPosition: []image.Point{{}, {X: 100, Y: 100}, {X: 120, Y: 120}, {X: 120, Y: 120}},
			wheel:          []float64{0, 0, 0, 0},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseLeft},
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 11, MouseY: 4},
				{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 13, MouseY: 5},
				{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 13, MouseY: 5},
			},
		},
		{
			description:    "clamps to the last column at the exact right boundary",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{}, {X: defaultWidth, Y: 100}},
			wheel:          []float64{0, 0},
			expectedEvents: []term.Event{
				{},
				{Type: term.EventMouse, MouseX: 87, MouseY: 4},
			},
		},
		{
			description:    "clamps to the last row at the exact bottom boundary",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{}, {X: 100, Y: defaultHeight}},
			wheel:          []float64{0, 0},
			expectedEvents: []term.Event{
				{},
				{Type: term.EventMouse, MouseX: 11, MouseY: 27},
			},
		},
		{
			description:    "clamps both axes simultaneously past the bottom-right corner",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{}, {X: defaultWidth * 2, Y: defaultHeight * 2}},
			wheel:          []float64{0, 0},
			expectedEvents: []term.Event{
				{},
				{Type: term.EventMouse, MouseX: 87, MouseY: 27},
			},
		},
		{
			description:    "clamps both axes simultaneously past the top-left corner",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{}, {X: -defaultWidth, Y: -defaultHeight}},
			wheel:          []float64{0, 0},
			expectedEvents: []term.Event{
				{},
				{Type: term.EventMouse, MouseX: 0, MouseY: 0},
			},
		},
		{
			description:    "wheel up takes precedence over a held left button in the same frame",
			pressedButtons: [][]ebiten.MouseButton{{ebiten.MouseButtonLeft}},
			cursorPosition: []image.Point{{}},
			wheel:          []float64{1},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseWheelUp},
			},
		},
		{
			description:    "wheel down takes precedence over a held right button in the same frame",
			pressedButtons: [][]ebiten.MouseButton{{ebiten.MouseButtonRight}},
			cursorPosition: []image.Point{{}},
			wheel:          []float64{-1},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseWheelDown},
			},
		},
		{
			description:    "wheel is reported even when the cursor also moves in the same frame",
			pressedButtons: [][]ebiten.MouseButton{{}},
			cursorPosition: []image.Point{{X: 100, Y: 100}},
			wheel:          []float64{1},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseWheelUp, MouseX: 11, MouseY: 4},
			},
		},
		{
			description:    "horizontal wheel movement alone is ignored",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{}, {}},
			wheel:          []float64{0, 0},
			wheelX:         []float64{0, 5},
			expectedEvents: []term.Event{{}, {}},
		},
		{
			description:    "horizontal wheel does not change the vertical wheel direction",
			pressedButtons: [][]ebiten.MouseButton{{}},
			cursorPosition: []image.Point{{}},
			wheel:          []float64{-1},
			wheelX:         []float64{9},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseWheelDown},
			},
		},
		{
			description: "left button wins precedence when multiple buttons are pressed",
			pressedButtons: [][]ebiten.MouseButton{
				{ebiten.MouseButtonLeft, ebiten.MouseButtonRight, ebiten.MouseButtonMiddle},
			},
			cursorPosition: []image.Point{{}},
			wheel:          []float64{0},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseLeft},
			},
		},
		{
			description: "right button wins over middle when both are pressed",
			pressedButtons: [][]ebiten.MouseButton{
				{ebiten.MouseButtonRight, ebiten.MouseButtonMiddle},
			},
			cursorPosition: []image.Point{{}},
			wheel:          []float64{0},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseRight},
			},
		},
		{
			description: "releasing one of two held buttons keeps reporting the still-held button",
			pressedButtons: [][]ebiten.MouseButton{
				{ebiten.MouseButtonLeft, ebiten.MouseButtonRight},
				{ebiten.MouseButtonLeft},
				{},
			},
			cursorPosition: []image.Point{{}, {}, {}},
			wheel:          []float64{0, 0, 0},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseLeft},
				{Type: term.EventMouse, Key: term.MouseLeft},
				{Type: term.EventMouse, Key: term.MouseRelease},
			},
		},
		{
			description: "switching the held button from left to right reports the new button",
			pressedButtons: [][]ebiten.MouseButton{
				{ebiten.MouseButtonLeft},
				{ebiten.MouseButtonRight},
				{},
			},
			cursorPosition: []image.Point{{}, {}, {}},
			wheel:          []float64{0, 0, 0},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseLeft},
				{Type: term.EventMouse, Key: term.MouseRight},
				{Type: term.EventMouse, Key: term.MouseRelease},
			},
		},
		{
			description:    "consecutive identical wheel frames each dispatch an event",
			pressedButtons: [][]ebiten.MouseButton{{}, {}, {}},
			cursorPosition: []image.Point{{}, {}, {}},
			wheel:          []float64{1, 1, 1},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, Key: term.MouseWheelUp},
				{Type: term.EventMouse, Key: term.MouseWheelUp},
				{Type: term.EventMouse, Key: term.MouseWheelUp},
			},
		},
		{
			// At multiplier 1 a single sub-line delta does not cross a whole
			// line, so no event is emitted; the remainder accumulates. Full
			// fractional accumulation is covered by
			// TestMouseWheelAccumulatesFractionalDeltas.
			description:    "fractional wheel delta below one line does not dispatch",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{}, {}},
			wheel:          []float64{0.1, 0.2},
			expectedEvents: []term.Event{{}, {}},
		},
		{
			description: "sub-cell pixel movement still dispatches because dedup is " +
				"keyed on raw pixels, not cells",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{X: 100, Y: 100}, {X: 101, Y: 101}},
			wheel:          []float64{0, 0},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, MouseX: 11, MouseY: 4},
				{Type: term.EventMouse, MouseX: 11, MouseY: 4},
			},
		},
		{
			description:    "identical pixel position across frames does not re-dispatch",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{X: 100, Y: 100}, {X: 100, Y: 100}},
			wheel:          []float64{0, 0},
			expectedEvents: []term.Event{
				{Type: term.EventMouse, MouseX: 11, MouseY: 4},
				{},
			},
		},
		{
			description:    "release without any prior press does not dispatch",
			pressedButtons: [][]ebiten.MouseButton{{}, {}},
			cursorPosition: []image.Point{{}, {}},
			wheel:          []float64{0, 0},
			expectedEvents: []term.Event{{}, {}},
		},
	}

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			mock, mouse := newTestMouse(t)
			// Use a 1:1 multiplier so each wheel notch maps to exactly one
			// line event; the accumulator and multiplier are covered by
			// dedicated tests below.
			mouse.multiplier = 1
			if len(test.pressedButtons) != len(test.cursorPosition) || len(test.cursorPosition) != len(test.wheel) ||
				len(test.wheel) != len(test.expectedEvents) {
				t.Fatalf("incorrectly setup test case: pressedButtons, cursorPosition, wheel " +
					"and expectedEvents must be of the same length")
			}
			for i, buttons := range test.pressedButtons {
				mock.pressedButtons = make(map[ebiten.MouseButton]struct{})
				mock.wheel = test.wheel[i]
				if test.wheelX != nil {
					mock.wheelX = test.wheelX[i]
				} else {
					mock.wheelX = 0
				}
				mock.cursorPosition = test.cursorPosition[i]
				expectedEvent := test.expectedEvents[i]
				for _, button := range buttons {
					mock.pressedButtons[button] = struct{}{}
				}
				events := mouse.processMouse()
				if expectedEvent.Type == 0 {
					assert.Empty(t, events, i)
				} else {
					require.Len(t, events, 1, i)
					// Every event carries the pointer's position inside
					// the reported cell so consumers can snap to cell
					// edges; the payload is checked separately from the
					// cell coordinates.
					frac, ok := tterm.SubCellFractionFromContext(events[0].Context)
					require.True(t, ok, i)
					assert.InDelta(t, expectedFracX(t, test.cursorPosition[i], mouse), frac.X, 1e-9, i)
					events[0].Context = nil
					assert.Equal(t, expectedEvent, events[0], i)
				}
			}
		})
	}
}

// expectedFracX recomputes the in-cell fraction processMouse should attach
// for a cursor pixel: the pointer's distance from the reported cell's left
// edge over the cell pitch, with the same window clamp the event position
// gets.
func expectedFracX(t *testing.T, cursor image.Point, m *mouse) float64 {
	t.Helper()
	pitch := m.fontManager.PixelX(1)
	px := min(max(float64(cursor.X), 0), pitch*float64(m.width)-1)
	cell := m.clampedCoordinates().X
	return (px - m.fontManager.PixelX(cell)) / pitch
}

func TestProcessMouseClampsToResizedBounds(t *testing.T) {
	mock, mouse := newTestMouse(t)
	mouse.resize(10, 5)

	mock.cursorPosition = image.Point{X: defaultWidth, Y: defaultHeight}
	events := mouse.processMouse()
	require.Len(t, events, 1)
	assert.Equal(t, 9, events[0].MouseX, "x clamps to width-1 after resize")
	assert.Equal(t, 4, events[0].MouseY, "y clamps to height-1 after resize")
}

func TestProcessMouseNoOpFrameDoesNotDispatch(t *testing.T) {
	mock, mouse := newTestMouse(t)
	mock.cursorPosition = image.Point{X: 100, Y: 100}

	require.Len(t, mouse.processMouse(), 1, "first move dispatches")
	assert.Empty(t, mouse.processMouse(), "identical follow-up frame does not dispatch")
}

func TestCalculateCoordinatesMapsPixelsToCells(t *testing.T) {
	_, mouse := newTestMouse(t)
	mouse.state.x = 100
	mouse.state.y = 100
	got := mouse.calculateCoordinates()
	assert.Equal(t, 11, got.X)
	assert.Equal(t, 4, got.Y)
}

func TestCalculateCoordinatesNegativePixelsStayNegative(t *testing.T) {
	_, mouse := newTestMouse(t)
	mouse.state.x = -100
	mouse.state.y = -100
	got := mouse.calculateCoordinates()
	assert.Negative(t, got.X, "clamping is the caller's responsibility, not calculateCoordinates")
	assert.Negative(t, got.Y)
}

func TestSubCellFractionReportsPositionInsideCell(t *testing.T) {
	_, mouse := newTestMouse(t)
	pitch := mouse.fontManager.PixelX(1)

	suite := []struct {
		description string
		x           float64
		wantX       float64
	}{
		{"cell's left edge", pitch * 3, 0},
		{"cell's midpoint", pitch*3 + pitch/2, .5},
		{"just before the next cell", pitch*4 - 1, (pitch - 1) / pitch},
		{"past the window's right edge", pitch * float64(mouse.width) * 2, (pitch - 1) / pitch},
		{"past the window's left edge", -pitch * 2, 0},
	}
	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			mouse.state.x = int(test.x)
			pos := mouse.clampedCoordinates()
			frac := mouse.subCellFraction(pos)
			// state.x is a whole pixel, so the fraction can sit a
			// pixel off the ideal position.
			assert.InDelta(t, test.wantX, frac.X, 1.0/pitch)
		})
	}
}

func TestResizeUpdatesClampBounds(t *testing.T) {
	_, mouse := newTestMouse(t)
	mouse.resize(3, 7)
	assert.Equal(t, 3, mouse.width)
	assert.Equal(t, 7, mouse.height)
}

func newTestMouse(t *testing.T) (*mockMouseManager, *mouse) {
	mock := &mockMouseManager{pressedButtons: map[ebiten.MouseButton]struct{}{}}
	f, err := font.NewManager(1, 1)
	require.NoError(t, err)
	f.SetFontByFamilyName("")
	f.SetDeviceScale(1)
	f.SetDPI(72)
	require.NoError(t, f.SetSize(16))
	ret := newMouse(f)
	ret.mouse = mock
	ret.resize(f.CellsWidth(defaultWidth), f.CellsHeight(defaultHeight))
	return mock, ret
}

type mockMouseManager struct {
	pressedButtons map[ebiten.MouseButton]struct{}
	wheel          float64
	wheelX         float64
	cursorPosition image.Point
}

func (m *mockMouseManager) IsMouseButtonPressed(key ebiten.MouseButton) bool {
	_, ok := m.pressedButtons[key]
	return ok
}

func (m *mockMouseManager) Wheel() (float64, float64) {
	return m.wheelX, m.wheel
}

func (m *mockMouseManager) CursorPosition() (int, int) {
	return m.cursorPosition.X, m.cursorPosition.Y
}

func TestMouseWheelAccumulatesFractionalDeltas(t *testing.T) {
	mock, mouse := newTestMouse(t)
	mouse.multiplier = 1

	// Four 0.3 deltas accumulate to 1.2; only one whole line should be
	// emitted, and the 0.2 remainder retained for the next gesture.
	for i := range 3 {
		mock.wheel = 0.3
		assert.Empty(t, mouse.processMouse(), "delta %d should not yet cross a line", i)
	}
	mock.wheel = 0.3
	events := mouse.processMouse()
	require.Len(t, events, 1)
	assert.Equal(t, term.MouseWheelUp, events[0].Key)

	// 0.2 remainder is carried over.
	assert.InDelta(t, 0.2, mouse.accumY, 1e-9)
}

func TestMouseWheelMultiplierEmitsMultipleLines(t *testing.T) {
	mock, mouse := newTestMouse(t)
	mouse.multiplier = 3

	mock.wheel = 1
	events := mouse.processMouse()
	require.Len(t, events, 3)
	for _, ev := range events {
		assert.Equal(t, term.EventMouse, ev.Type)
		assert.Equal(t, term.MouseWheelUp, ev.Key)
	}

	mock.wheel = -1
	events = mouse.processMouse()
	require.Len(t, events, 3)
	for _, ev := range events {
		assert.Equal(t, term.MouseWheelDown, ev.Key)
	}
}

func TestMouseWheelLineCounts(t *testing.T) {
	suite := []struct {
		description   string
		multiplier    float64
		delta         float64
		wantLines     int
		wantKey       term.Key
		wantRemainder float64
	}{
		{"exact one line up", 1, 1, 1, term.MouseWheelUp, 0},
		{"exact one line down", 1, -1, 1, term.MouseWheelDown, 0},
		{"truncates 1.9 to one line up", 1, 1.9, 1, term.MouseWheelUp, 0.9},
		{"truncates -1.9 to one line down", 1, -1.9, 1, term.MouseWheelDown, -0.9},
		{"multiplier 3 emits three lines", 3, 1, 3, term.MouseWheelUp, 0},
		{"fractional multiplier 1.5 -> one line, 0.5 remainder", 1.5, 1, 1, term.MouseWheelUp, 0.5},
		{"fractional multiplier 2.5 -> two lines, 0.5 remainder", 2.5, 1, 2, term.MouseWheelUp, 0.5},
		{"large multiplier many lines", 10, 2, 20, term.MouseWheelUp, 0},
	}
	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			mock, mouse := newTestMouse(t)
			mouse.multiplier = test.multiplier
			mock.wheel = test.delta

			events := mouse.processMouse()
			require.Len(t, events, test.wantLines)
			for _, ev := range events {
				assert.Equal(t, term.EventMouse, ev.Type)
				assert.Equal(t, test.wantKey, ev.Key)
			}
			assert.InDelta(t, test.wantRemainder, mouse.accumY, 1e-9)
		})
	}
}

func TestMouseWheelRemainderCarriesAcrossGestures(t *testing.T) {
	mock, mouse := newTestMouse(t)
	mouse.multiplier = 1

	total := 0
	for range 10 {
		mock.wheel = 0.25
		total += len(mouse.processMouse())
	}
	// 10 * 0.25 = 2.5 -> two whole lines emitted, 0.5 carried over.
	assert.Equal(t, 2, total)
	assert.InDelta(t, 0.5, mouse.accumY, 1e-9)
}

func TestMouseWheelSignReversalCancelsRemainder(t *testing.T) {
	mock, mouse := newTestMouse(t)
	mouse.multiplier = 1

	mock.wheel = 0.6
	require.Empty(t, mouse.processMouse(), "0.6 does not cross a line")
	assert.InDelta(t, 0.6, mouse.accumY, 1e-9)

	mock.wheel = -0.6
	require.Empty(t, mouse.processMouse(), "0.6 - 0.6 = 0, no line crossed")
	assert.InDelta(t, 0.0, mouse.accumY, 1e-9)
}

func TestMouseWheelRemainderSurvivesNonWheelFrames(t *testing.T) {
	mock, mouse := newTestMouse(t)
	mouse.multiplier = 1

	mock.wheel = 0.7
	require.Empty(t, mouse.processMouse())
	require.InDelta(t, 0.7, mouse.accumY, 1e-9)

	// A non-wheel frame: a click. The remainder must be preserved.
	mock.wheel = 0
	mock.pressedButtons[ebiten.MouseButtonLeft] = struct{}{}
	events := mouse.processMouse()
	require.Len(t, events, 1)
	assert.Equal(t, term.MouseLeft, events[0].Key)
	assert.InDelta(t, 0.7, mouse.accumY, 1e-9, "click frame must not reset remainder")

	// Release and resume scrolling; 0.7 + 0.4 = 1.1 crosses one line.
	delete(mock.pressedButtons, ebiten.MouseButtonLeft)
	require.Len(t, mouse.processMouse(), 1, "release event")
	mock.wheel = 0.4
	require.Len(t, mouse.processMouse(), 1)
	assert.InDelta(t, 0.1, mouse.accumY, 1e-9)
}

func TestMouseWheelCoordinatesAreClamped(t *testing.T) {
	mock, mouse := newTestMouse(t)
	mouse.multiplier = 1
	mock.cursorPosition = image.Point{X: defaultWidth * 2, Y: defaultHeight * 2}
	mock.wheel = 1

	events := mouse.processMouse()
	require.Len(t, events, 1)
	assert.Equal(t, 87, events[0].MouseX)
	assert.Equal(t, 27, events[0].MouseY)
}

func TestMouseWheelDoesNotPanicOnPathologicalDeltas(t *testing.T) {
	suite := []struct {
		description string
		multiplier  float64
		delta       float64
	}{
		{"positive infinity delta", 1, math.Inf(1)},
		{"negative infinity delta", 1, math.Inf(-1)},
		{"nan delta", 1, math.NaN()},
		{"huge positive delta", 1, 1e18},
		{"huge negative delta", 1, -1e18},
		{"infinite multiplier", math.Inf(1), 1},
		{"nan multiplier", math.NaN(), 1},
		{"max float delta", 1, math.MaxFloat64},
	}
	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			mock, mouse := newTestMouse(t)
			mouse.multiplier = test.multiplier
			mock.wheel = test.delta
			assert.NotPanics(t, func() {
				events := mouse.processMouse()
				assert.LessOrEqual(t, len(events), maxWheelLinesPerFrame,
					"emitted line count must stay bounded")
			})
		})
	}
}

func TestMouseWheelCapsLinesPerFrame(t *testing.T) {
	mock, mouse := newTestMouse(t)
	mouse.multiplier = 1
	mock.wheel = maxWheelLinesPerFrame * 10

	events := mouse.processMouse()
	assert.Len(t, events, maxWheelLinesPerFrame)
	for _, ev := range events {
		assert.Equal(t, term.MouseWheelUp, ev.Key)
	}
}

func TestMouseWheelRecoversAfterNonFiniteDelta(t *testing.T) {
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		mock, mouse := newTestMouse(t)
		mouse.multiplier = 1

		mock.wheel = bad
		require.Empty(t, mouse.processMouse())
		require.True(t, isFinite(mouse.accumY), "accumulator must remain finite after bad delta")

		mock.wheel = 1
		events := mouse.processMouse()
		require.Len(t, events, 1, "scrolling must recover after a bad delta")
		assert.Equal(t, term.MouseWheelUp, events[0].Key)
	}
}

func TestNewMouseDefaultMultiplier(t *testing.T) {
	_, mouse := newTestMouse(t)
	assert.Equal(t, float64(defaultScrollMultiplier), mouse.multiplier)
}

func TestWithScrollMultiplierIgnoresNonPositive(t *testing.T) {
	suite := []struct {
		description string
		value       float64
		want        float64
	}{
		{"positive overrides default", 5, 5},
		{"fractional positive overrides default", 1.5, 1.5},
		{"zero is ignored", 0, defaultScrollMultiplier},
		{"negative is ignored", -2, defaultScrollMultiplier},
	}
	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			_, mouse := newTestMouse(t)
			g := &GUI{mouse: mouse}
			require.NoError(t, WithScrollMultiplier(test.value)(g))
			assert.Equal(t, test.want, mouse.multiplier)
		})
	}
}
