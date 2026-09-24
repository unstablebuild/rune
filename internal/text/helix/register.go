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
	"strings"

	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/registerset"
)

const (
	unnamedRegister   = '"'
	lastYankRegister  = '0'
	clipboardRegister = '+'
	blackHoleRegister = '_'
	// sequenceRegister is Helix's # register: it holds nothing, but a
	// counted increment selected through it grows by one per range.
	sequenceRegister = '#'
)

// validRegisterName reports whether name is accepted as a register
// identifier for `"{register}` selections. Helix names registers the
// same way Vim does, so the accepted set matches internal/text/vi.
func validRegisterName(name rune) bool {
	return name == unnamedRegister || name == lastYankRegister ||
		name == clipboardRegister || name == blackHoleRegister ||
		name == sequenceRegister || name == '/' || name == '.' || name == '-' ||
		('a' <= name && name <= 'z') ||
		('A' <= name && name <= 'Z')
}

func registerNameToID(name rune) string {
	if name == 0 || name == unnamedRegister {
		return clipboard.DefaultRegisterID
	}
	return registerset.Normalize(string(name))
}

func normalizedRegisterName(name rune) rune {
	registerID := registerNameToID(name)
	if registerID == clipboard.DefaultRegisterID {
		return unnamedRegister
	}
	names := []rune(registerID)
	if len(names) > 0 {
		return names[0]
	}
	return unnamedRegister
}

// fragmentsMetadata is what a yank or delete over several ranges
// stores alongside the joined text: one fragment per range, as Helix's
// registers hold a Vec<String>. A single range keeps the plain
// text.SelectMode metadata every other consumer of the register reads.
type fragmentsMetadata struct {
	Mode      text.SelectMode
	Fragments []string
}

// registerData builds the register value for fragments. The text is
// the fragments joined by newlines, which is what Helix hands the
// system clipboard and what vi and the IDE paste.
func registerData(mode text.SelectMode, fragments []string) clipboard.Data {
	if len(fragments) == 1 {
		return clipboard.Data{Text: fragments[0], Metadata: mode}
	}
	return clipboard.Data{
		Text:     strings.Join(fragments, "\n"),
		Metadata: fragmentsMetadata{Mode: mode, Fragments: fragments},
	}
}

// registerFragments reads a register value back as fragments: what a
// multi-range yank stored, or the whole text as one fragment for a
// value written by anything else.
func registerFragments(data clipboard.Data) ([]string, text.SelectMode) {
	switch m := data.Metadata.(type) {
	case fragmentsMetadata:
		return m.Fragments, m.Mode
	case text.SelectMode:
		return []string{data.Text}, m
	default:
		return []string{data.Text}, text.NoSelection
	}
}
