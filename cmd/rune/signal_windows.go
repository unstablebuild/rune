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

package main

import (
	"os"

	"golang.org/x/sys/windows"
)

func isUrgentDataSignal(os.Signal) bool {
	return false
}

func redirectStderr(f *os.File) {
	// The runtime looks up the standard error handle on every fatal write,
	// so this also captures crash dumps.
	if windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())) == nil {
		os.Stderr = f
	}
}
