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

//go:build linux

package main

import "syscall"

// Yama's PR_SET_PTRACER value ("Yama" in ASCII) and its wildcard.
const (
	prSetPtracer    = 0x59616d61
	prSetPtracerAny = ^uintptr(0)
)

// allowAnyTracer opts this process into being attached by a debugger
// that is not its parent. Ubuntu ships kernel.yama.ptrace_scope=1,
// which only lets a process ptrace its descendants; the attach test
// starts dlv and this program as siblings.
func allowAnyTracer() {
	_, _, _ = syscall.Syscall(syscall.SYS_PRCTL, prSetPtracer, prSetPtracerAny, 0)
}
