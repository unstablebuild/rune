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

//go:build windows

package syntax

import "golang.org/x/sys/windows"

const (
	dlNow    = 0
	dlGlobal = 0
)

func sysDlopen(path string, flags int) (uintptr, error) {
	dll, err := windows.LoadDLL(path)
	if err != nil {
		return 0, err
	}
	return uintptr(dll.Handle), nil
}

func sysDlsym(lib uintptr, name string) (uintptr, error) {
	dll := windows.DLL{Handle: windows.Handle(lib)}
	p, err := dll.FindProc(name)
	if err != nil {
		return 0, err
	}
	return p.Addr(), nil
}

func sysDlclose(lib uintptr) error {
	return windows.FreeLibrary(windows.Handle(lib))
}
