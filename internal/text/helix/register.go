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
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"unstable.build/rune/internal/text/registerset"
)

const (
	unnamedRegister   = '"'
	lastYankRegister  = '0'
	clipboardRegister = '+'
	blackHoleRegister = '_'
)

// validRegisterName reports whether name is accepted as a register
// identifier for `"{register}` selections. Helix names registers the
// same way Vim does, so the accepted set matches internal/text/vi.
func validRegisterName(name rune) bool {
	return name == unnamedRegister || name == lastYankRegister ||
		name == clipboardRegister || name == blackHoleRegister ||
		name == '/' || name == '.' || name == '-' ||
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
