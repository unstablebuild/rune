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
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/text"
)

const lastChangeLocationListID = "changes"

// KeyBindings returns a list of custom key bindings for a vi editor.
func KeyBindings() (ret map[handler.Sequence][][]string) {
	ret = make(map[handler.Sequence][][]string, 'z'-'a')

	for ch := 'a'; ch <= 'z'; ch++ {
		seq := handler.Sequence{First: term.KeyComb{Ch: 'm'}, Last: term.KeyComb{Ch: ch}}
		ret[seq] = [][]string{
			{text.CommandDeleteAllLocations, string(ch)},
			{text.CommandCreateLocation, string(ch)},
		}
		seq = handler.Sequence{First: term.KeyComb{Ch: '`'}, Last: term.KeyComb{Ch: ch}}
		ret[seq] = [][]string{
			{text.CommandLocationJump, "next", string(ch)},
		}
		seq = handler.Sequence{First: term.KeyComb{Ch: '\''}, Last: term.KeyComb{Ch: ch}}
		ret[seq] = [][]string{
			{text.CommandLocationJumpLine, "next", string(ch)},
		}
	}

	// Special marks:
	// `. — jump to last change position (exact)
	seq := handler.Sequence{First: term.KeyComb{Ch: '`'}, Last: term.KeyComb{Ch: '.'}}
	ret[seq] = [][]string{
		{text.CommandLocationJump, "next", lastChangeLocationListID},
	}
	// '. — jump to last change position (linewise)
	seq = handler.Sequence{First: term.KeyComb{Ch: '\''}, Last: term.KeyComb{Ch: '.'}}
	ret[seq] = [][]string{
		{text.CommandLocationJumpLine, "next", lastChangeLocationListID},
	}
	// `< / `> — jump to start/end of last visual selection (exact)
	seq = handler.Sequence{First: term.KeyComb{Ch: '`'}, Last: term.KeyComb{Ch: '<'}}
	ret[seq] = [][]string{
		{text.CommandLocationJump, "next", visualSelectionStartMarkID},
	}
	seq = handler.Sequence{First: term.KeyComb{Ch: '`'}, Last: term.KeyComb{Ch: '>'}}
	ret[seq] = [][]string{
		{text.CommandLocationJump, "next", visualSelectionEndMarkID},
	}
	// '< / '> — jump to start/end of last visual selection (linewise)
	seq = handler.Sequence{First: term.KeyComb{Ch: '\''}, Last: term.KeyComb{Ch: '<'}}
	ret[seq] = [][]string{
		{text.CommandLocationJumpLine, "next", visualSelectionStartMarkID},
	}
	seq = handler.Sequence{First: term.KeyComb{Ch: '\''}, Last: term.KeyComb{Ch: '>'}}
	ret[seq] = [][]string{
		{text.CommandLocationJumpLine, "next", visualSelectionEndMarkID},
	}
	// gf — edit file under cursor; vi's gMode declines f, so the key
	// dispatcher completes the sequence.
	seq = handler.Sequence{First: term.KeyComb{Ch: 'g'}, Last: term.KeyComb{Ch: 'f'}}
	ret[seq] = [][]string{
		{"editfileoncursor"},
	}
	return ret
}
