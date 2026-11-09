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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestPendingHandlerPointerLeaves(t *testing.T) {
	t.Parallel()
	uri, err := workspaceapi.ParseURI("ext:///日本語/🙂/a-uri-longer-than-the-placeholder-is-wide")
	require.NoError(t, err)
	tests := []struct {
		name    string
		pending func() *pendingHandler
	}{
		{name: "waiting", pending: func() *pendingHandler { return newPendingHandler(uri) }},
		{name: "failed", pending: func() *pendingHandler {
			h := newPendingHandler(uri)
			h.fail(errors.New("the extension went away"))
			return h
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			for _, size := range []struct{ width, height int }{{40, 10}, {1, 1}} {
				handlertest.RunPointerLeaves(t, tt.pending(), size.width, size.height)
			}
			requirePointerLeavesKeepState(t, tt.pending(), 40, 10,
				term.Coordinates{X: 3, Y: 2}, term.Coordinates{X: 12, Y: 4})
		})
	}
}
