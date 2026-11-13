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
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
)

func TestEditorUnsupportedSurface(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///x")
	require.NoError(t, err)
	ed := Editor().(*helixEditor)

	assert.Error(t, ed.SubscribeCommand(textapi.CommandManual{}, nil))
	assert.Error(t, ed.RegisterREPLCommand(textapi.CommandManual{}, nil))
	assert.Nil(t, ed.REPLCommands())
	assert.Error(t, ed.UnsubscribeCommand("x"))
	assert.Error(t, ed.UnregisterREPLCommand("x"))

	h, err := ed.Editor(uri)
	assert.Nil(t, h)
	assert.Error(t, err)
}

func TestEditorEventSubscription(t *testing.T) {
	ed := Editor()
	sub := text.FuncEventHandler(
		func(ctx context.Context, ev textapi.Event) bool { return false })

	require.NoError(t, ed.SubscribeEvents(
		[]textapi.EventType{textapi.EventTypeEdit}, sub))
	ok, err := ed.UnsubscribeEvents(sub)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = ed.UnsubscribeEvents(sub)
	require.NoError(t, err)
	assert.False(t, ok, "a second unsubscribe is a no-op")

	adapter := publisherEventsAdapter{pub: &ed.(*helixEditor).Publisher}
	require.NoError(t, adapter.SubscribeEvents(
		[]textapi.EventType{textapi.EventTypeEdit}, sub))
	ok, err = adapter.UnsubscribeEvents(sub)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestEditorDispatchFocus(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("file:///")
	require.NoError(t, err)
	ed := Editor(
		WithWorkspaceCommandRegistry(cwd, texttest.NopWorkspaceRegistry()),
	)
	content := "Clement"
	uri, err := workspaceapi.ParseURI("file:///Jolie")
	require.NoError(t, err)

	var h textapi.Handler
	ed.SubscribeEvents([]textapi.EventType{textapi.EventTypeOpen},
		text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
			assert.Equal(t, textapi.EventTypeOpen, ev.Type)
			assert.Equal(t, content, ev.Content)
			assert.Equal(t, uri, ev.URI)
			h = ev.Resource
			return false
		}))

	var focusCalled int
	ed.SubscribeEvents([]textapi.EventType{textapi.EventTypeFocus},
		text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
			focusCalled++
			assert.Equal(t, uri, ev.URI)
			assert.Equal(t, h, ev.Resource)
			return false
		}))

	buf := cell.NewBuffer()
	buf.WriteString(content)
	_, err = ed.Edit(context.Background(), uri, buf, false, false)
	require.NoError(t, err)

	assert.Equal(t, 1, focusCalled)
}

func TestEditorDispatchScroll(t *testing.T) {
	ed := Editor()
	buf := cell.NewBuffer()
	buf.WriteString("Atzari\nSurinach")
	h, err := ed.Edit(context.Background(), workspaceapi.URI{}, buf, false, false)
	require.NoError(t, err)

	h.Resize(2, 1)
	h.Handle(term.Event{Type: term.EventKey, Ch: 'j'})

	at := term.Coordinates{X: -1}
	ed.SubscribeEvents([]textapi.EventType{textapi.EventTypeScroll},
		text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
			at = ev.Start
			return false
		}))

	h.Handle(term.Event{Type: term.EventKey, Ch: 'k'})
	assert.Equal(t, term.Coordinates{}, at)

	at = term.Coordinates{X: -1}
	h.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
	assert.Equal(t, term.Coordinates{Y: 1}, at)
}

func TestEditorHatchesRowsPastTheEnd(t *testing.T) {
	bars := text.WithBars(context.Background(), text.BarOptions{})
	tests := []struct {
		name string
		ctx  context.Context
		opts []Option
		want image.Rectangle
		// wantPeriod is the side of the tile in pixels, on 20 pixel rows.
		wantPeriod int
		wantBlue   bool
	}{
		{name: "in a window with bars", ctx: bars, want: image.Rect(0, 3, 20, 6), wantPeriod: 10},
		{
			name: "in the configured style", ctx: bars,
			opts: []Option{WithHatch(component.HatchStyle{Color: term.ColorBlue, Period: 0.75})},
			want: image.Rect(0, 3, 20, 6), wantPeriod: 15, wantBlue: true,
		},
		{name: "nowhere else", ctx: context.Background()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := cell.NewBuffer()
			buf.WriteString("Atzari\nSurinach\n")
			h, err := Editor(tt.opts...).Edit(tt.ctx, workspaceapi.URI{}, buf, true, false)
			require.NoError(t, err)
			h.Resize(20, 6)
			w := cell.NewBufferWriter(cell.ContextWithPixelSize(context.Background(),
				cell.PixelSize{Width: 10, Height: 20}), 20, 6)
			h.Draw(w)

			var hatched []term.Image
			for _, img := range w.Images() {
				if img.Fit == term.ImageFitTile {
					hatched = append(hatched, img)
				}
			}
			if tt.want.Empty() {
				assert.Empty(t, hatched)
				return
			}
			require.Len(t, hatched, 1)
			assert.Equal(t, tt.want, hatched[0].Bounds())
			tile, ok := hatched[0].Src.(*image.RGBA)
			require.True(t, ok)
			assert.Equal(t, image.Rect(0, 0, tt.wantPeriod, tt.wantPeriod), tile.Rect)
			stripe := tile.RGBAAt(0, 0)
			assert.Positive(t, stripe.A, "the tile starts on a stripe")
			assert.Equal(t, tt.wantBlue, stripe.R == 0 && stripe.G == 0 && stripe.B > 0,
				"the stripe's color is %v", stripe)
		})
	}
}

func TestEditorIsNotExternal(t *testing.T) {
	ed := Editor()
	external, ok := ed.(interface{ IsExternal() bool })
	require.True(t, ok)
	assert.False(t, external.IsExternal())
}

func TestEditorInstallsStatusBar(t *testing.T) {
	tick := func(fn func()) bool { fn(); return true }
	ed := Editor(
		WithAuxiliaryBar(true, text.AuxBarConfig{
			LinesEnabled: true, ScheduleNextTick: tick,
		}),
		WithStatusBarConfig(true, text.StatusBarConfig{
			Publisher:        &texttest.TestEditor{},
			ScheduleNextTick: tick,
		}),
	)
	buf := cell.NewBuffer()
	buf.WriteString("Atzari\nSurinach\n")
	h, err := ed.Edit(
		text.WithBars(context.Background(), text.BarOptions{}),
		workspaceapi.URI{}, buf, true, false)
	require.NoError(t, err)
	require.IsType(t, &text.StatusBar{}, h)

	h.Resize(80, 20)
	_, handled := h.Handle(term.Event{Type: term.EventKey, Ch: 'w'})
	assert.True(t, handled, "the wrapped handler must still dispatch motions")
}
