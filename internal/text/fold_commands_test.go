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
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestFoldCommandHandlerPointerLeaves(t *testing.T) {
	t.Parallel()
	cwd, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	uri := workspaceapi.Join(cwd, "fold.go")
	wrap := func() Handler {
		fileRegistry := NewFileCommandRegistry(cwd, newIndentTestWorkspaceRegistry())
		ed := newPointerEditor(t, uri)
		// A hidden range shifts every row below it, so a leave must not
		// map its position through the folds either.
		require.True(t, ed.cursor.scroll.MarkHidden(3, 6))
		h, err := SubscribeFoldCommands(uri, fileRegistry, ed.cursor, ed)
		require.NoError(t, err)
		return h
	}
	for _, size := range []struct{ width, height int }{{40, 10}, {1, 1}} {
		handlertest.RunPointerLeaves(t, wrap(), size.width, size.height)
	}
	requirePointerLeavesKeepState(t, wrap(), 40, 10,
		term.Coordinates{X: 3, Y: 2}, term.Coordinates{X: 12, Y: 4})
}
