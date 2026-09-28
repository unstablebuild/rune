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

package text

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

func TestSubscribeIndentCommands(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri := workspaceapi.Join(cwd, "indent.go")

	registry := newIndentTestWorkspaceRegistry()
	fileRegistry := NewFileCommandRegistry(cwd, registry)
	h := &indentTestHandler{uri: uri}
	c := setupCursorContent(t, 80, 10, "a", false)
	mock := &mockIndentService{returnIndentationAt: 1}
	mock.View = c.buffer().WithView(mock)

	wrapped, err := SubscribeIndentCommands(uri, fileRegistry, c, IndentConfig{}, h)
	require.NoError(t, err)
	commands := registry.sub[cwd.String()]
	require.Contains(t, commands, CommandReindent)

	err = commands[CommandReindent].HandleCommand(context.Background(), textapi.Command{
		URI:  uri,
		Name: CommandReindent,
	})
	require.NoError(t, err)
	assert.Equal(t, "\ta", c.buffer().String())

	require.NoError(t, wrapped.Close())
	assert.NotContains(t, registry.sub[cwd.String()], CommandReindent)
}

func TestIndentCommandReindentsSelection(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri := workspaceapi.Join(cwd, "indent.go")

	registry := newIndentTestWorkspaceRegistry()
	fileRegistry := NewFileCommandRegistry(cwd, registry)
	h := &indentTestHandler{uri: uri}
	c := setupCursorContent(t, 80, 10, "a\n\t\tb", false)
	mock := &mockIndentService{returnIndentationAt: 1}
	mock.View = c.buffer().WithView(mock)

	wrapped, err := SubscribeIndentCommands(uri, fileRegistry, c, IndentConfig{}, h)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, wrapped.Close()) })

	require.True(t, c.SelectRange(term.Coordinates{}, term.Coordinates{Y: 1}))
	err = registry.sub[cwd.String()][CommandReindent].HandleCommand(context.Background(), textapi.Command{
		URI:  uri,
		Name: CommandReindent,
	})
	require.NoError(t, err)
	assert.Equal(t, "\ta\n\tb", c.buffer().String())
	mode, ok := c.SelectionMode()
	assert.False(t, ok)
	assert.Equal(t, NoSelection, mode)
}

func TestIndentCommandWithoutIndentServiceReturnsError(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri := workspaceapi.Join(cwd, "indent.go")

	registry := newIndentTestWorkspaceRegistry()
	fileRegistry := NewFileCommandRegistry(cwd, registry)
	h := &indentTestHandler{uri: uri}
	c := setupCursorContent(t, 80, 10, "a", false)

	wrapped, err := SubscribeIndentCommands(uri, fileRegistry, c, IndentConfig{}, h)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, wrapped.Close()) })

	err = registry.sub[cwd.String()][CommandReindent].HandleCommand(context.Background(), textapi.Command{
		URI:  uri,
		Name: CommandReindent,
	})
	require.EqualError(t, err, "auto-indentation not available at the current position")
}

type indentTestWorkspaceRegistry struct {
	sub map[string]map[string]CommandHandler
}

func newIndentTestWorkspaceRegistry() *indentTestWorkspaceRegistry {
	return &indentTestWorkspaceRegistry{sub: make(map[string]map[string]CommandHandler)}
}

func (r *indentTestWorkspaceRegistry) SubscribeCommandForWorkspace(
	workspace workspaceapi.URI, cmd textapi.CommandManual, handler CommandHandler,
) error {
	if r.sub[workspace.String()] == nil {
		r.sub[workspace.String()] = make(map[string]CommandHandler)
	}
	r.sub[workspace.String()][cmd.Name] = handler
	return nil
}

func (r *indentTestWorkspaceRegistry) UnsubscribeCommandForWorkspace(
	workspace workspaceapi.URI, name string,
) error {
	if r.sub[workspace.String()] != nil {
		delete(r.sub[workspace.String()], name)
	}
	return nil
}

type indentTestHandler struct {
	handler.TestHandler
	uri workspaceapi.URI
}

func (h *indentTestHandler) Close() error { return nil }

func (h *indentTestHandler) Resource() workspaceapi.URI { return h.uri }

func (h *indentTestHandler) SetWrap(bool) {}

func (h *indentTestHandler) ShowCommandBar(bool) {}

func (h *indentTestHandler) SetCursorAtScroll(term.Coordinates) bool { return false }

func (h *indentTestHandler) CursorAtScroll() term.Coordinates { return term.Coordinates{} }

func (h *indentTestHandler) SetLocationList(textapi.LocationPriority, string, LocationList) {}

func (h *indentTestHandler) LocationLists() []LocationSet { return nil }

func (h *indentTestHandler) MoveToNextLocation(string) bool { return false }

func (h *indentTestHandler) MoveToPrevLocation(string) bool { return false }

func (h *indentTestHandler) CellView() cell.View { return nil }

func (h *indentTestHandler) CellEditor() cell.Editor { return nil }

func (h *indentTestHandler) SetDefaultAttributes(term.Attributes) {}

func (h *indentTestHandler) SeekUp() bool { return false }

func (h *indentTestHandler) SeekDown() bool { return false }

func (h *indentTestHandler) SeekOffset() int { return 0 }

func (h *indentTestHandler) MaxSeekOffset() int { return 0 }

func (h *indentTestHandler) Dimensions() (int, int) { return 0, 0 }

func (h *indentTestHandler) IsSearchMode() bool { return false }

func (h *indentTestHandler) IsNormalMode() bool { return false }

var _ Handler = (*indentTestHandler)(nil)
var _ browserapi.Handler = (*indentTestHandler)(nil)
