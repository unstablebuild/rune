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

package browser

import (
	"context"
	"image"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/asciiart"
)

type dropRecorder struct {
	nopHandler
	events []term.Event
}

func newDropRecorder() *dropRecorder {
	r := new(dropRecorder)
	r.HandleOverride = func(ev term.Event) (bool, bool) {
		r.events = append(r.events, ev)
		return false, true
	}
	return r
}

// pasted reconstructs the text delivered by a bracketed paste, or false
// when the recorded events are not a complete paste sequence.
func (r *dropRecorder) pasted() (string, bool) {
	if len(r.events) < 2 ||
		r.events[0].Type != term.EventPasteStart ||
		r.events[len(r.events)-1].Type != term.EventPasteEnd {
		return "", false
	}
	var sb strings.Builder
	for _, ev := range r.events[1 : len(r.events)-1] {
		sb.WriteRune(ev.Ch)
	}
	return sb.String(), true
}

func dragConfig() Config {
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	cfg.Dim = false
	cfg.TabBarHeight = 1
	cfg.DropTargetAttr = term.Attributes{Fg: term.ColorSilver}
	cfg.DropTargetLabelAttr = term.Attributes{Fg: term.ColorBlue}
	cfg.DropTargetLabels = map[string]string{
		"":           "Drop files here",
		"rune-agent": "Drop files here to add to chat",
	}
	return cfg
}

// splitBrowser returns a browser with two side-by-side windows, the
// right one focused.
func splitBrowser(t *testing.T, width, height int) (
	b *Component, left, right Window, leftH, rightH *dropRecorder,
) {
	t.Helper()
	b = NewComponent(dragConfig())

	uriA, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	leftH = newDropRecorder()
	tabA := b.NewTab(uriA, 'A', "a", leftH, nil)
	left = b.Focus()
	require.NoError(t, left.SetContent(tabA))

	uriB, err := workspaceapi.ParseURI("rune-agent://model/chat")
	require.NoError(t, err)
	rightH = newDropRecorder()
	tabB := b.NewTab(uriB, 'B', "chat", rightH, nil)
	var ok bool
	right, ok = b.Split(browserapi.OrientationRight, left, tabB)
	require.True(t, ok)
	require.Equal(t, right, b.Focus())

	b.Resize(width, height)
	b.Draw(term.NewStringWriter(width, height))
	return b, left, right, leftH, rightH
}

// windowCenter returns component coordinates inside win.
func windowCenter(b *Component, win Window) term.Coordinates {
	off := b.WindowManagerPosition()
	pos := win.Position()
	return term.Coordinates{
		X: off.X + pos.X + win.Width()/2,
		Y: off.Y + pos.Y + win.Height()/2,
	}
}

func TestComponentWindowAt(t *testing.T) {
	b, left, right, _, _ := splitBrowser(t, 40, 10)

	got, ok := b.WindowAt(windowCenter(b, left))
	require.True(t, ok)
	assert.Equal(t, left, got)

	got, ok = b.WindowAt(windowCenter(b, right))
	require.True(t, ok)
	assert.Equal(t, right, got)

	_, ok = b.WindowAt(term.Coordinates{X: -1, Y: 0})
	assert.False(t, ok, "positions left of the window manager have no window")

	_, ok = b.WindowAt(term.Coordinates{X: 0, Y: 0})
	assert.False(t, ok, "the tab bar row has no window")
}

func TestComponentDragDropFocusesTargetWindow(t *testing.T) {
	b, left, right, leftH, rightH := splitBrowser(t, 40, 10)
	require.Equal(t, right, b.Focus())

	paths := []string{"/tmp/a.png", "/tmp/b.txt"}
	require.True(t, b.DragDrop(windowCenter(b, left), paths))

	assert.Equal(t, left, b.Focus(),
		"the drop must focus the window under the cursor")
	text, ok := leftH.pasted()
	require.True(t, ok, "target window must receive a bracketed paste")
	assert.Equal(t, strings.Join(paths, "\n"), text)
	assert.Empty(t, rightH.events,
		"the previously focused window must not receive the drop")
}

func TestComponentDragDropWithoutWindowIsNoOp(t *testing.T) {
	b, _, _, leftH, rightH := splitBrowser(t, 40, 10)

	assert.False(t, b.DragDrop(term.Coordinates{X: -1, Y: -1}, []string{"/tmp/a"}))
	assert.False(t, b.DragDrop(term.Coordinates{X: 5, Y: 5}, nil))
	assert.Empty(t, leftH.events)
	assert.Empty(t, rightH.events)
}

func TestComponentDragVeilTogglesOnDraw(t *testing.T) {
	b, left, right, _, _ := splitBrowser(t, 60, 10)

	require.True(t, b.DragHover(windowCenter(b, left)))
	assert.Contains(t, drawString(b, 60, 10), "Drop files here")

	require.True(t, b.DragHover(windowCenter(b, right)))
	assert.Contains(t, drawString(b, 60, 10),
		"Drop files here to add to chat",
		"the veil message follows the target tab's URI scheme")

	b.DragCancel()
	assert.NotContains(t, drawString(b, 60, 10), "Drop files here")
}

func TestComponentDragHoverOutsideClearsTarget(t *testing.T) {
	b, left, _, _, _ := splitBrowser(t, 60, 10)

	require.True(t, b.DragHover(windowCenter(b, left)))
	assert.False(t, b.DragHover(term.Coordinates{X: -1, Y: -1}))
	assert.NotContains(t, drawString(b, 60, 10), "Drop files here")
}

func TestComponentDragDropClearsVeil(t *testing.T) {
	b, left, _, _, _ := splitBrowser(t, 60, 10)

	require.True(t, b.DragHover(windowCenter(b, left)))
	require.True(t, b.DragDrop(windowCenter(b, left), []string{"/tmp/a.png"}))
	assert.NotContains(t, drawString(b, 60, 10), "Drop files here")
}

func TestComponentDragVeilAnimates(t *testing.T) {
	b, left, _, _, _ := splitBrowser(t, 60, 10)
	interrupts := make(chan struct{}, 64)
	b.SetInterrupter(term.FuncInterrupter(func(context.Context) error {
		select {
		case interrupts <- struct{}{}:
		default:
		}
		return nil
	}))

	require.True(t, b.DragHover(windowCenter(b, left)))
	for range 2 {
		select {
		case <-interrupts:
		case <-time.After(5 * time.Second):
			t.Fatal("drop-target veil did not request a redraw")
		}
	}

	b.DragCancel()
	assert.Nil(t, b.drag.cancel, "leaving the drag must stop the animation")
}

type pictureHandler struct {
	nopHandler
	pic           image.Image
	id            term.ImageID
	width, height int
}

func (h *pictureHandler) Resize(width, height int) {
	h.width, h.height = width, height
}

func (h *pictureHandler) Draw(w term.Writer) {
	w.DrawImage(term.Image{
		Src: h.pic, ID: h.id, Width: h.width, Height: h.height,
		Fit: term.ImageFitFill,
	})
}

func TestComponentDragVeilKeepsImages(t *testing.T) {
	const width, height = 40, 6
	b := NewComponent(dragConfig())
	white := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for i := range white.Pix {
		white.Pix[i] = 0xff
	}
	uri, err := workspaceapi.ParseURI("file:///a.png")
	require.NoError(t, err)
	h := &pictureHandler{pic: white, id: term.NewImageID()}
	win := b.Focus()
	require.NoError(t, win.SetContent(b.NewTab(uri, 'A', "a.png", h, nil)))
	b.Resize(width, height)

	w := asciiart.NewStringWriter(width, height, asciiart.DefaultConfig())
	draw := func() string {
		require.NoError(t, w.Clear(term.Attributes{}))
		b.Draw(w)
		require.NoError(t, w.Flush())
		return w.String()
	}
	picture := strings.Repeat("@", width)
	assert.Equal(t, strings.Join([]string{
		"A a.png                                 ",
		picture, picture, picture, picture, picture,
	}, "\n"), draw())

	require.True(t, b.DragHover(windowCenter(b, win)))
	assert.Equal(t, strings.Join([]string{
		"A a.png                                 ",
		picture, picture,
		"@@@@@@@@@@@@Drop files here@@@@@@@@@@@@@",
		picture, picture,
	}, "\n"), draw(), "the veil must keep the picture and label it")
}

func drawString(b *Component, width, height int) string {
	w := term.NewStringWriter(width, height)
	b.Draw(w)
	_ = w.Flush()
	return w.String()
}
