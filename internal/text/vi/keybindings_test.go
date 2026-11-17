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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/text"
)

func TestKeyBindingsSpecialMarks(t *testing.T) {
	bindings := KeyBindings()

	tests := []struct {
		name    string
		first   rune
		last    rune
		wantCmd [][]string
	}{
		{
			name:    "backtick-dot jumps to last change position",
			first:   '`',
			last:    '.',
			wantCmd: [][]string{{text.CommandLocationJump, "next", lastChangeLocationListID}},
		},
		{
			name:    "apostrophe-dot jumps to last change line",
			first:   '\'',
			last:    '.',
			wantCmd: [][]string{{text.CommandLocationJumpLine, "next", lastChangeLocationListID}},
		},
		{
			name:    "backtick-less jumps to start of last visual selection",
			first:   '`',
			last:    '<',
			wantCmd: [][]string{{text.CommandLocationJump, "next", visualSelectionStartMarkID}},
		},
		{
			name:    "backtick-greater jumps to end of last visual selection",
			first:   '`',
			last:    '>',
			wantCmd: [][]string{{text.CommandLocationJump, "next", visualSelectionEndMarkID}},
		},
		{
			name:    "apostrophe-less jumps to start of last visual selection line",
			first:   '\'',
			last:    '<',
			wantCmd: [][]string{{text.CommandLocationJumpLine, "next", visualSelectionStartMarkID}},
		},
		{
			name:    "apostrophe-greater jumps to end of last visual selection line",
			first:   '\'',
			last:    '>',
			wantCmd: [][]string{{text.CommandLocationJumpLine, "next", visualSelectionEndMarkID}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seq := handler.Sequence{
				First: term.KeyComb{Ch: tc.first},
				Last:  term.KeyComb{Ch: tc.last},
			}
			cmds, ok := bindings[seq]
			require.True(t, ok, "binding for %c%c should exist", tc.first, tc.last)
			assert.Equal(t, tc.wantCmd, cmds)
		})
	}
}

func TestKeyBindingsLetterMarks(t *testing.T) {
	bindings := KeyBindings()

	// Verify all a-z mark bindings exist.
	for ch := 'a'; ch <= 'z'; ch++ {
		// m{ch} — set mark
		seq := handler.Sequence{
			First: term.KeyComb{Ch: 'm'},
			Last:  term.KeyComb{Ch: ch},
		}
		cmds, ok := bindings[seq]
		require.True(t, ok, "m%c should exist", ch)
		assert.Equal(t, [][]string{
			{text.CommandDeleteAllLocations, string(ch)},
			{text.CommandCreateLocation, string(ch)},
		}, cmds)

		// `{ch} — exact position jump
		seq = handler.Sequence{
			First: term.KeyComb{Ch: '`'},
			Last:  term.KeyComb{Ch: ch},
		}
		cmds, ok = bindings[seq]
		require.True(t, ok, "`%c should exist", ch)
		assert.Equal(t, [][]string{
			{text.CommandLocationJump, "next", string(ch)},
		}, cmds)

		// '{ch} — linewise jump
		seq = handler.Sequence{
			First: term.KeyComb{Ch: '\''},
			Last:  term.KeyComb{Ch: ch},
		}
		cmds, ok = bindings[seq]
		require.True(t, ok, "'%c should exist", ch)
		assert.Equal(t, [][]string{
			{text.CommandLocationJumpLine, "next", string(ch)},
		}, cmds)
	}
}

func TestKeyBindingsGoFile(t *testing.T) {
	seq := handler.Sequence{
		First: term.KeyComb{Ch: 'g'},
		Last:  term.KeyComb{Ch: 'f'},
	}
	cmds, ok := KeyBindings()[seq]
	require.True(t, ok, "gf should be bound")
	assert.Equal(t, [][]string{{"editfileoncursor"}}, cmds)
}
