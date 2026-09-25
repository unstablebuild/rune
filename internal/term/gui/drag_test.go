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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
)

type fakeDragHost struct {
	x, y     int
	dragging bool
	paths    []string
}

func newTestDragPoller(t *testing.T) (*fakeDragHost, *dragPoller, *[]DragEvent) {
	t.Helper()
	_, m := newTestMouse(t)
	host := new(fakeDragHost)
	events := new([]DragEvent)
	d := newDragPoller(m)
	d.position = func() (int, int, bool) { return host.x, host.y, host.dragging }
	d.paths = func() []string { return host.paths }
	d.observer = func(ev DragEvent) { *events = append(*events, ev) }
	return host, d, events
}

func TestDragPollerReportsHoverLeaveAndDrop(t *testing.T) {
	host, d, events := newTestDragPoller(t)

	host.dragging = true
	host.x, host.y = 100, 100
	require.True(t, d.poll(), "the first hover changes state")
	require.Len(t, *events, 1)
	assert.Equal(t, DragHover, (*events)[0].Kind)
	assert.Equal(t, d.mouse.cellAt(100, 100), (*events)[0].Pos)

	assert.False(t, d.poll(), "an unchanged hover position is not reported")
	assert.Len(t, *events, 1)

	host.x = 200
	require.True(t, d.poll(), "a moved hover is reported")
	require.Len(t, *events, 2)
	assert.Equal(t, DragHover, (*events)[1].Kind)

	host.dragging = false
	require.True(t, d.poll())
	require.Len(t, *events, 3)
	assert.Equal(t, DragLeave, (*events)[2].Kind)

	assert.False(t, d.poll(), "leaving twice is not reported")

	host.paths = []string{"/tmp/a.png"}
	require.True(t, d.poll())
	require.Len(t, *events, 4)
	assert.Equal(t, DragDrop, (*events)[3].Kind)
	assert.Equal(t, []string{"/tmp/a.png"}, (*events)[3].Paths)
}

func TestDragPollerDropWhileHovering(t *testing.T) {
	host, d, events := newTestDragPoller(t)

	host.dragging = true
	host.x, host.y = 50, 50
	require.True(t, d.poll())

	// The host ends the drag and reports the paths in the same frame.
	host.dragging = false
	host.paths = []string{"/tmp/a.png", "/tmp/b.txt"}
	require.True(t, d.poll())

	require.Len(t, *events, 3)
	assert.Equal(t, DragHover, (*events)[0].Kind)
	assert.Equal(t, DragLeave, (*events)[1].Kind)
	assert.Equal(t, DragDrop, (*events)[2].Kind)
	assert.Equal(t, host.paths, (*events)[2].Paths)
}

// TestDragPollerDropsWhereFilesWereReleased pins the position source of a
// drop. The mouse cursor does not follow a file drag, so it still points
// at whatever was clicked last; only the host's drag position says where
// the files were released.
func TestDragPollerDropsWhereFilesWereReleased(t *testing.T) {
	host, d, events := newTestDragPoller(t)
	d.mouse.state.x, d.mouse.state.y = 10, 10

	host.dragging = true
	host.x, host.y = 300, 200
	require.True(t, d.poll())

	// The drag ends over the same spot and the host reports the paths.
	host.dragging = false
	host.paths = []string{"/tmp/a.png"}
	require.True(t, d.poll())

	drop := (*events)[len(*events)-1]
	require.Equal(t, DragDrop, drop.Kind)
	assert.Equal(t, d.mouse.cellAt(300, 200), drop.Pos)
	assert.NotEqual(t, d.mouse.cellAt(10, 10), drop.Pos)
}

// TestDragPollerDropFallsBackToCursor covers hosts that report dropped
// paths without ever reporting a drag position: the cursor is then the
// only hint of where the files landed.
func TestDragPollerDropFallsBackToCursor(t *testing.T) {
	host, d, events := newTestDragPoller(t)
	d.mouse.state.x, d.mouse.state.y = 300, 200

	host.paths = []string{"/tmp/a.png"}
	require.True(t, d.poll())

	require.Len(t, *events, 1)
	assert.Equal(t, d.mouse.cellAt(300, 200), (*events)[0].Pos)
}

func TestDragPollerClampsToWindow(t *testing.T) {
	host, d, events := newTestDragPoller(t)

	host.dragging = true
	host.x, host.y = 100000, 100000
	require.True(t, d.poll())

	require.Len(t, *events, 1)
	pos := (*events)[0].Pos
	assert.Equal(t, term.Coordinates{X: d.mouse.width - 1, Y: d.mouse.height - 1}, pos)
}

// TestDragPollerDefaultObserverIsNop pins the no-nil-deps invariant: a
// poller without a host-installed observer still polls safely.
func TestDragPollerDefaultObserverIsNop(t *testing.T) {
	_, m := newTestMouse(t)
	d := newDragPoller(m)
	d.position = func() (int, int, bool) { return 10, 10, true }
	d.paths = func() []string { return []string{"/tmp/a.png"} }

	assert.True(t, d.poll())
}

// pasted reconstructs the text a window received as a bracketed paste.
func pasted(events []term.Event) (string, bool) {
	if len(events) < 2 ||
		events[0].Type != term.EventPasteStart ||
		events[len(events)-1].Type != term.EventPasteEnd {
		return "", false
	}
	var sb strings.Builder
	for _, ev := range events[1 : len(events)-1] {
		sb.WriteRune(ev.Ch)
	}
	return sb.String(), true
}

func recordingTab(
	t *testing.T, b *browser.Component, uri string, icon rune,
) (*browser.Tab, *[]term.Event) {
	t.Helper()
	parsed, err := workspaceapi.ParseURI(uri)
	require.NoError(t, err)
	events := new([]term.Event)
	h := browsertest.NewTestHandler()
	h.HandleOverride = func(ev term.Event) (bool, bool) {
		*events = append(*events, ev)
		return false, true
	}
	return b.NewTab(parsed, icon, string(icon), h, nil), events
}

// windowCenterPixel returns the pixel position of the centre of win, as
// the host reports it while dragging files over that part of the window.
func windowCenterPixel(
	d *dragPoller, b *browser.Component, win browser.Window,
) (x, y int) {
	off := b.WindowManagerPosition()
	pos := win.Position()
	f := d.mouse.fontManager
	return int(f.PixelX(off.X + pos.X + win.Width()/2)),
		int(f.PixelY(off.Y + pos.Y + win.Height()/2))
}

// TestDragDropTargetsWindowUnderCursor is the end-to-end guard for
// dropping files on a split browser: the drop belongs to the window the
// files were released over, not to the window that happens to hold the
// focus. Dragging is not a click, so requiring the user to focus a
// window before dropping on it would defeat the feature.
func TestDragDropTargetsWindowUnderCursor(t *testing.T) {
	host, d, _ := newTestDragPoller(t)

	cfg := browser.DefaultConfig()
	cfg.DropTargetLabels = map[string]string{"": "Drop files here"}
	b := browser.NewComponent(cfg)
	leftTab, leftEvents := recordingTab(t, b, "file:///a", 'A')
	rightTab, rightEvents := recordingTab(t, b, "file:///b", 'B')

	left := b.Focus()
	require.NoError(t, left.SetContent(leftTab))
	right, ok := b.Split(browserapi.OrientationRight, left, rightTab)
	require.True(t, ok)
	b.Resize(d.mouse.width, d.mouse.height)
	b.Draw(term.NewStringWriter(d.mouse.width, d.mouse.height))
	require.Equal(t, right, b.Focus(), "the split focuses the new window")

	// The cursor stays where the user last clicked, which is what gave
	// the right window the focus.
	d.mouse.state.x, d.mouse.state.y = windowCenterPixel(d, b, right)

	// dragObserver, as cmd/rune wires it.
	d.observer = func(ev DragEvent) {
		switch ev.Kind {
		case DragHover:
			b.DragHover(ev.Pos)
		case DragLeave:
			b.DragCancel()
		case DragDrop:
			b.DragDrop(ev.Pos, ev.Paths)
		}
	}

	host.dragging = true
	host.x, host.y = windowCenterPixel(d, b, left)
	require.True(t, d.poll(), "the drag over the left window is reported")
	assert.Contains(t, drawBrowser(b, d.mouse.width, d.mouse.height),
		"Drop files here", "the hovered window must show the drop veil")

	host.dragging = false
	host.paths = []string{"/tmp/a.png"}
	require.True(t, d.poll(), "the drop is reported")

	text, ok := pasted(*leftEvents)
	require.True(t, ok, "the window under the cursor must receive the drop")
	assert.Equal(t, "/tmp/a.png", text)
	assert.Empty(t, *rightEvents, "the focused window must not receive the drop")
	assert.Equal(t, left, b.Focus(), "the drop focuses the window it landed on")
}

func drawBrowser(b *browser.Component, width, height int) string {
	w := term.NewStringWriter(width, height)
	b.Draw(w)
	_ = w.Flush()
	return w.String()
}

// idle is what lets the render loop skip a whole tick without taking the
// UI lock, so it must never report true for a frame on which poll would
// notify an observer that mutates UI state. A new branch in poll that
// idle does not account for has to fail here. idle is allowed to be
// conservative in the other direction.
func TestDragPollerIdleImpliesPollIsNoOp(t *testing.T) {
	cases := []struct {
		name     string
		hovering bool
		dragging bool
		paths    []string
	}{
		{name: "quiescent"},
		{name: "dragging", dragging: true},
		{name: "hovering", hovering: true},
		{name: "hovering and dragging", hovering: true, dragging: true},
		{name: "drop pending", paths: []string{"/tmp/a.png"}},
		{name: "drop pending while hovering", hovering: true, paths: []string{"/tmp/a.png"}},
		{name: "drop pending while dragging", dragging: true, paths: []string{"/tmp/a.png"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, d, events := newTestDragPoller(t)
			host.dragging = tc.dragging
			host.paths = tc.paths
			d.hovering = tc.hovering

			if !d.idle() {
				return
			}
			assert.False(t, d.poll(), "an idle frame must not change drag state")
			assert.Empty(t, *events, "an idle frame must not notify the observer")
		})
	}
}

// The render loop probes idle before deciding whether to take the UI
// lock, then polls under it. Probing reads per-tick host state, so it
// must leave the transition for poll to report.
func TestDragPollerIdleDoesNotConsumeTransition(t *testing.T) {
	host, d, events := newTestDragPoller(t)
	host.dragging = true
	host.x, host.y = 100, 100

	require.False(t, d.idle())
	require.False(t, d.idle(), "probing must be repeatable within a tick")

	require.True(t, d.poll(), "probing must not consume the hover transition")
	require.Len(t, *events, 1)
	assert.Equal(t, DragHover, (*events)[0].Kind)
}
