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

package vi

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
)

func TestEditorDispatchFocus(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("file:///")
	require.NoError(t, err)
	ed := Editor(
		// test wrapping/unwrapping of ifcs
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
			assert.Equal(t, textapi.EventTypeFocus, ev.Type)
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

	at = term.Coordinates{X: -1}
	h.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
	assert.Equal(t, term.Coordinates{X: -1}, at)
}

// TestEditorBarOptions pins the bar selection :gitshow depends on: the
// caller can drop the aux and icons bars for one Edit while keeping the
// status bar, and can redirect the status bar's git lookups at a
// resource the editor's own configuration cannot resolve.
func TestEditorBarOptions(t *testing.T) {
	tick := func(fn func()) bool { fn(); return true }
	newEditor := func() text.Editor {
		return Editor(
			WithAuxiliaryBar(true, text.AuxBarConfig{
				LinesEnabled: true, ScheduleNextTick: tick,
			}),
			WithStatusBarConfig(true, text.StatusBarConfig{
				Publisher:        &texttest.TestEditor{},
				ScheduleNextTick: tick,
			}),
		)
	}
	open := func(t *testing.T, opts text.BarOptions) text.Handler {
		t.Helper()
		buf := cell.NewBuffer()
		buf.WriteString("Atzari\nSurinach\n")
		h, err := newEditor().Edit(
			text.WithBars(context.Background(), opts),
			workspaceapi.URI{}, buf, true, false)
		require.NoError(t, err)
		return h
	}

	withBars := open(t, text.BarOptions{})
	require.IsType(t, &text.StatusBar{}, withBars)
	full, _ := withBars.Dimensions()

	disabled := open(t, text.BarOptions{DisableAuxBar: true, DisableIconsBar: true})
	require.IsType(t, &text.StatusBar{}, disabled,
		"disabling the aux bar must leave the status bar installed")
	narrow, _ := disabled.Dimensions()
	assert.Less(t, narrow, full,
		"without the aux bar the handler must not claim its gutter")
}

func TestEditorStatusBarOverride(t *testing.T) {
	tick := func(fn func()) bool { fn(); return true }
	base, err := workspaceapi.ParseURI("memory:///gitshow")
	require.NoError(t, err)
	file, err := workspaceapi.ParseURI("memory:///gitshow/hello.go.diff")
	require.NoError(t, err)

	ed := Editor(WithStatusBarConfig(true, text.StatusBarConfig{
		Publisher:        &texttest.TestEditor{},
		ScheduleNextTick: tick,
		Layout: []text.StatusBarComponent{
			{Type: text.StatusBarFilePath, Template: "%s"},
		},
	}))
	buf := cell.NewBuffer()
	buf.WriteString("--- a/hello.go\n")
	h, err := ed.Edit(text.WithBars(context.Background(), text.BarOptions{
		StatusBar: &text.StatusBarOverride{
			Workspace: base, GitService: vctrl.NopService(),
		},
	}), file, buf, true, false)
	require.NoError(t, err)

	h.Resize(40, 4)
	w := term.NewStringWriter(40, 4)
	h.Draw(w)
	require.NoError(t, w.Flush())
	assert.Contains(t, w.String(), "hello.go.diff",
		"the status bar must resolve the path against the override base")
	assert.NotContains(t, w.String(), "memory:",
		"the popup path must not fall back to the whole URI")
}

func TestEditorDispatchCursor(t *testing.T) {
	t.Run("regular handler-driven changes to cursor", func(t *testing.T) {
		uri, err := workspaceapi.ParseURI("file:///tmp/zsh.sh")
		require.NoError(t, err)
		ed := Editor()
		buf := cell.NewBuffer()
		buf.WriteString("Matias\nGiordano\n")
		h, err := ed.Edit(context.Background(), uri, buf, false, false)
		require.NoError(t, err)

		// should scroll as well, but changes in cursorAtScroll is what we are expecting
		h.Resize(2, 1)

		windowCursor := term.Coordinates{X: -1}
		scrollCursor := term.Coordinates{X: -1}
		ed.SubscribeEvents([]textapi.EventType{textapi.EventTypeCursor},
			text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
				windowCursor = ev.Start
				scrollCursor = ev.From
				assert.Equal(t, ev.URI.String(), "file:///tmp/zsh.sh")
				assert.Equal(t, ev.Resource, h)
				return false
			}))

		h.Handle(term.Event{Type: term.EventKey, Ch: 'k'})
		assert.Equal(t, term.Coordinates{X: -1}, windowCursor)
		assert.Equal(t, term.Coordinates{X: -1}, scrollCursor)

		h.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		assert.Equal(t, term.Coordinates{Y: 0}, windowCursor)
		assert.Equal(t, term.Coordinates{Y: 1}, scrollCursor)

		windowCursor = term.Coordinates{X: -1}
		scrollCursor = term.Coordinates{X: -1}
		h.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
		assert.Equal(t, term.Coordinates{X: 1}, windowCursor)
		assert.Equal(t, term.Coordinates{Y: 1, X: 1}, scrollCursor)
	})

	t.Run("api-driven changes to cursor", func(t *testing.T) {
		uri, err := workspaceapi.ParseURI("file:///tmp/zsh.sh")
		require.NoError(t, err)
		ed := Editor()
		buf := cell.NewBuffer()
		buf.WriteString("Matias\nGiordano\n")
		h, err := ed.Edit(context.Background(), uri, buf, false, false)
		require.NoError(t, err)
		h.Resize(2, 1)

		windowCursor := term.Coordinates{X: -1}
		scrollCursor := term.Coordinates{X: -1}
		ed.SubscribeEvents([]textapi.EventType{textapi.EventTypeCursor},
			text.FuncEventHandler(func(ctx context.Context, ev textapi.Event) bool {
				windowCursor = ev.Start
				scrollCursor = ev.From
				assert.Equal(t, ev.URI.String(), "file:///tmp/zsh.sh")
				assert.Equal(t, ev.Resource, h)
				return false
			}))

		require.True(t, h.SetCursorAtScroll(term.Coordinates{Y: 1}))
		assert.Equal(t, term.Coordinates{Y: 0}, windowCursor)
		assert.Equal(t, term.Coordinates{Y: 1}, scrollCursor)

		windowCursor = term.Coordinates{X: -1}
		scrollCursor = term.Coordinates{X: -1}
		h.SetLocationList(textapi.LocationPriorityInfo, "id",
			textapi.LocationSlice([]textapi.Location{{}, {From: term.Coordinates{Y: 1}}}))
		h.MoveToNextLocation("id")
		assert.Equal(t, term.Coordinates{Y: 0}, windowCursor)
		assert.Equal(t, term.Coordinates{Y: 0}, scrollCursor)

		windowCursor = term.Coordinates{X: -1}
		scrollCursor = term.Coordinates{X: -1}
		h.MoveToPrevLocation("id")
		assert.Equal(t, term.Coordinates{Y: 0}, windowCursor)
		assert.Equal(t, term.Coordinates{Y: 1}, scrollCursor)
	})
}

func TestEditorSetCursor(t *testing.T) {
	uri, err := workspaceapi.ParseURI("file:///tmp/zsh.sh")
	require.NoError(t, err)

	for _, wrap := range []bool{true, false} {
		t.Run(fmt.Sprintf("wrap: %v, does not return error if cursor already at position", wrap),
			func(t *testing.T) {
				ed := Editor(WithWrap(wrap), WithAutoCenter(true))
				h, err := ed.Edit(context.Background(), uri, cell.NewBuffer(), false, false)
				require.NoError(t, err)
				if wrap {
					h.Resize(1, 1) // just not 0, 0
				}

				h.SetCursorAtScroll(term.Coordinates{})
			})

		t.Run(fmt.Sprintf("wrap: %v, sets cursor at position", wrap),
			func(t *testing.T) {
				buf := cell.NewBuffer()
				buf.WriteString("a")
				ed := Editor(WithWrap(wrap), WithAutoCenter(true))
				h, err := ed.Edit(context.Background(), uri, buf, false, false)
				require.NoError(t, err)
				h.Resize(1, 1)

				h.SetCursorAtScroll(term.Coordinates{X: 1})
				pos := h.CursorAtScroll()
				require.NoError(t, err)
				assert.Equal(t, term.Coordinates{X: 1}, pos)
			})

		t.Run(fmt.Sprintf("wrap: %v, should be robust against Resize", wrap),
			func(t *testing.T) {
				buf := cell.NewBuffer()
				buf.WriteString("aaaaaaaaaaaaaaaa\nbb\nc\nd\ne")
				ed := Editor(WithWrap(wrap), WithAutoCenter(true))
				h, err := ed.Edit(context.Background(), uri, buf, false, false)
				require.NoError(t, err)
				cursor := h.(interface{ CursorReference() *text.Cursor }).CursorReference()

				h.SetCursorAtScroll(term.Coordinates{Y: 3})

				assert.Equal(t, term.Coordinates{}, cursor.Coordinates())
				assert.Equal(t, term.Coordinates{}, cursor.CursorAtScroll())

				h.Resize(1, 1)
				assert.Equal(t, term.Coordinates{}, cursor.Coordinates())
				assert.Equal(t, term.Coordinates{Y: 3}, cursor.CursorAtScroll())

				h.SetCursorAtScroll(term.Coordinates{Y: 4})
				require.NoError(t, err)

				h.Resize(10, 10)
				assert.Equal(t, term.Coordinates{Y: 0}, cursor.Coordinates())
				assert.Equal(t, term.Coordinates{Y: 4}, cursor.CursorAtScroll())

				h.Resize(2, 2)
				assert.Equal(t, term.Coordinates{Y: 0}, cursor.Coordinates())
				assert.Equal(t, term.Coordinates{Y: 4}, cursor.CursorAtScroll())
			})
	}
}

func TestEditorRegistersIndentCommand(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("memory:///indent.go")
	require.NoError(t, err)

	wr := newWorkspaceRegistry()
	ed := Editor(WithWorkspaceCommandRegistry(cwd, wr))
	buf := cell.NewBuffer()
	buf.WriteString("a")

	h, err := ed.Edit(context.Background(), uri, buf, false, false)
	require.NoError(t, err)
	h.Resize(80, 10)

	cmds := wr.sub[cwd.String()]
	require.Contains(t, cmds, text.CommandReindent)

	mock := &mockIndentView{View: buf.View(), indents: map[int]int{0: 1}}
	buf.WithView(mock)

	err = cmds[text.CommandReindent].HandleCommand(context.Background(), textapi.Command{
		URI:  uri,
		Name: text.CommandReindent,
	})
	require.NoError(t, err)
	assert.Equal(t, "\ta", buf.String())

	require.NoError(t, h.Close())
	assert.NotContains(t, wr.sub[cwd.String()], text.CommandReindent)
}

// TestEditorAppliesDefaultConfig ensures Editor seeds viConfig with
// defaultviHandlerImplConfig values so that downstream consumers (e.g.
// vctrlcmd.SubscribeGitCommands) receive non-nil dependencies such as
// notifications. Regression test for a nil pointer dereference in the
// :gitlink command path when no WithNotifications option was passed.
func TestEditorAppliesDefaultConfig(t *testing.T) {
	ed := Editor().(*viEditor)
	require.NotNil(t, ed.config.notifications,
		"viEditor.config.notifications must default to a non-nil "+
			"implementation so that command handlers like :gitlink do not "+
			"panic when WithNotifications is not provided")
	require.NotNil(t, ed.config.clipboard,
		"viEditor.config.clipboard must default to a non-nil register")
	require.NotNil(t, ed.config.scheduleNextTick,
		"viEditor.config.scheduleNextTick must default to a non-nil func")
}

type workspaceRegistry struct {
	sub map[string]map[string]text.CommandHandler
}

func newWorkspaceRegistry() *workspaceRegistry {
	return &workspaceRegistry{sub: make(map[string]map[string]text.CommandHandler)}
}

func (r *workspaceRegistry) SubscribeCommandForWorkspace(
	workspace workspaceapi.URI, cmd textapi.CommandManual, handler text.CommandHandler,
) error {
	if r.sub[workspace.String()] == nil {
		r.sub[workspace.String()] = make(map[string]text.CommandHandler)
	}
	r.sub[workspace.String()][cmd.Name] = handler
	return nil
}

func (r *workspaceRegistry) UnsubscribeCommandForWorkspace(
	workspace workspaceapi.URI, name string,
) error {
	if r.sub[workspace.String()] != nil {
		delete(r.sub[workspace.String()], name)
	}
	return nil
}

type mockIndentView struct {
	cell.View
	indents map[int]int
}

func (v mockIndentView) IndentationAt(line int) (int, bool) {
	indent, ok := v.indents[line]
	return indent, ok
}
