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
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

const testMarkListID = "mark"

func TestMarkCommandHandlerPointerLeaves(t *testing.T) {
	t.Parallel()
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri := workspaceapi.Join(cwd, "mark.go")
	wrap := func() Handler {
		fileRegistry := NewFileCommandRegistry(cwd, newIndentTestWorkspaceRegistry())
		ed := newPointerEditor(t, uri)
		ed.cursor.SetLocationList(textapi.LocationPriorityInfo, testMarkListID, LocationSlice(
			[]textapi.Location{{From: term.Coordinates{Y: 2}, To: term.Coordinates{Y: 2, X: 1}}}))
		h, err := SubscribeMarkCommands(uri, fileRegistry, ed.cursor, testMarkListID, ed)
		require.NoError(t, err)
		return h
	}
	for _, size := range []struct{ width, height int }{{40, 10}, {1, 1}} {
		handlertest.RunPointerLeaves(t, wrap(), size.width, size.height)
	}
	requirePointerLeavesKeepState(t, wrap(), 40, 10,
		term.Coordinates{X: 3, Y: 2}, term.Coordinates{X: 12, Y: 4})
}

func TestExchangePointAndMarkSwapsCursorAndMark(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri := workspaceapi.Join(cwd, "mark.go")

	registry := newIndentTestWorkspaceRegistry()
	fileRegistry := NewFileCommandRegistry(cwd, registry)
	h := &indentTestHandler{uri: uri}
	c := setupCursorContent(t, 80, 10, "a\nb\nc\nd", false)

	wrapped, err := SubscribeMarkCommands(uri, fileRegistry, c, testMarkListID, h)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, wrapped.Close()) })

	commands := registry.sub[cwd.String()]
	require.Contains(t, commands, CommandExchangePointAndMark)

	// Mark at line 0, point at line 2.
	c.SetLocationList(textapi.LocationPriorityInfo, testMarkListID, LocationSlice(
		[]textapi.Location{{From: term.Coordinates{}, To: term.Coordinates{X: 1}}}))
	_, ok := c.MoveToScroll(term.Coordinates{Y: 2})
	require.True(t, ok)

	err = commands[CommandExchangePointAndMark].HandleCommand(context.Background(),
		textapi.Command{URI: uri, Name: CommandExchangePointAndMark})
	require.NoError(t, err)

	// Point moved to the old mark; the mark now records the old point.
	assert.Equal(t, term.Coordinates{}, c.CursorAtScroll())
	marks := markListLocations(c, testMarkListID)
	require.Len(t, marks, 1)
	assert.Equal(t, term.Coordinates{Y: 2}, marks[0].From)

	// A second exchange is the inverse and restores the original positions.
	err = commands[CommandExchangePointAndMark].HandleCommand(context.Background(),
		textapi.Command{URI: uri, Name: CommandExchangePointAndMark})
	require.NoError(t, err)
	assert.Equal(t, term.Coordinates{Y: 2}, c.CursorAtScroll())
	marks = markListLocations(c, testMarkListID)
	require.Len(t, marks, 1)
	assert.Equal(t, term.Coordinates{}, marks[0].From)
}

func TestExchangePointAndMarkWithoutMarkErrors(t *testing.T) {
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri := workspaceapi.Join(cwd, "mark.go")

	registry := newIndentTestWorkspaceRegistry()
	fileRegistry := NewFileCommandRegistry(cwd, registry)
	h := &indentTestHandler{uri: uri}
	c := setupCursorContent(t, 80, 10, "a\nb", false)

	wrapped, err := SubscribeMarkCommands(uri, fileRegistry, c, testMarkListID, h)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, wrapped.Close()) })

	err = registry.sub[cwd.String()][CommandExchangePointAndMark].HandleCommand(
		context.Background(), textapi.Command{URI: uri, Name: CommandExchangePointAndMark})
	require.EqualError(t, err, "no mark set in this buffer")
}

func markListLocations(c *Cursor, id string) []textapi.Location {
	for _, list := range c.LocationLists() {
		if list.ID == id {
			return list.Locations
		}
	}
	return nil
}
