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
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/text"
)

// The location lists the LSP diagnostics and Git change bars publish.
const (
	lspDiagnosticsLocationListID = "lsp-diagnostics"
	gitChangeLocationListID      = "gitchange"
)

// KeyBindings returns the entries of Helix's g, [ and ] menus that run
// workspace commands rather than edit the buffer. The editor opens those
// menus itself and declines these second keys, so the commands only run
// where the key dispatcher completes a sequence whose first key the
// editor consumed.
func KeyBindings() map[handler.Sequence][][]string {
	seq := func(first, last rune) handler.Sequence {
		return handler.Sequence{First: term.KeyComb{Ch: first}, Last: term.KeyComb{Ch: last}}
	}
	return map[handler.Sequence][][]string{
		seq('g', 'd'): {{"lsp", "definition"}},      // goto_definition
		seq('g', 'D'): {{"lsp", "declaration"}},     // goto_declaration
		seq('g', 'y'): {{"lsp", "type-definition"}}, // goto_type_definition
		seq('g', 'r'): {{"lsp", "references"}},      // goto_reference
		seq('g', 'i'): {{"lsp", "implementation"}},  // goto_implementation
		seq('g', 'n'): {{"tabnext"}},                // goto_next_buffer
		seq('g', 'p'): {{"tabprevious"}},            // goto_previous_buffer
		seq(']', 'd'): {{text.CommandLocationJump, "next", lspDiagnosticsLocationListID}},
		seq('[', 'd'): {{text.CommandLocationJump, "previous", lspDiagnosticsLocationListID}},
		seq(']', 'g'): {{text.CommandLocationJump, "next", gitChangeLocationListID}},
		seq('[', 'g'): {{text.CommandLocationJump, "previous", gitChangeLocationListID}},
	}
}
