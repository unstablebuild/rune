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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler"
)

func TestKeyBindings(t *testing.T) {
	want := map[string][][]string{
		"gd": {{"lsp", "definition"}},
		"gD": {{"lsp", "declaration"}},
		"gy": {{"lsp", "type-definition"}},
		"gr": {{"lsp", "references"}},
		"gi": {{"lsp", "implementation"}},
		"gn": {{"tabnext"}},
		"gp": {{"tabprevious"}},
		"]d": {{"jumptolocation", "next", "lsp-diagnostics"}},
		"[d": {{"jumptolocation", "previous", "lsp-diagnostics"}},
		"]g": {{"jumptolocation", "next", "gitchange"}},
		"[g": {{"jumptolocation", "previous", "gitchange"}},
	}

	bindings := KeyBindings()
	require.Len(t, bindings, len(want))
	for keys, cmds := range want {
		seq, err := handler.ParseSequence(keys)
		require.NoError(t, err)
		assert.Equalf(t, cmds, bindings[seq], "binding for %s", keys)
	}
}

// TestKeyBindingsLeaveEditorGotoAndBracketKeys pins that no binding
// shadows a goto or bracket command the editor implements: the editor
// sees the second key first, so such a binding could never fire.
func TestKeyBindingsLeaveEditorGotoAndBracketKeys(t *testing.T) {
	for seq := range KeyBindings() {
		keys := seq.String()
		t.Run(keys, func(t *testing.T) {
			hx, _, _ := newHelix(t, "one\ntwo\nthree\n", term.Coordinates{})
			_, handled := hx.Handle(key(seq.First.Ch))
			require.True(t, handled, "editor opens the %c menu", seq.First.Ch)
			_, handled = hx.Handle(key(seq.Last.Ch))
			assert.False(t, handled, "editor declines %s", keys)
			assert.True(t, hx.IsNormalMode(), "declining %s leaves the menu", keys)
		})
	}
}
