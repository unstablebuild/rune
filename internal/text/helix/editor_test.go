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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
)

// TestEditorUnsupportedSurface pins the parts of text.Editor Helix
// deliberately does not implement, so a caller gets an error instead of
// silent no-ops.
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

// TestEditorEventSubscription pins subscribe / unsubscribe on both the
// editor and the publisher adapter the file commands use.
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

// TestEditorIsNotExternal pins that helix manages its buffer in process,
// which is what the IDE checks before installing an external editor.
func TestEditorIsNotExternal(t *testing.T) {
	ed := Editor()
	external, ok := ed.(interface{ IsExternal() bool })
	require.True(t, ok)
	assert.False(t, external.IsExternal())
}

// TestEditorInstallsStatusBar pins that Edit composes the same chrome vi
// does, so the shared status/aux/icons bars work for helix too.
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
