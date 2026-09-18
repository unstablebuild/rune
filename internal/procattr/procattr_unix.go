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

//go:build !windows

package procattr

import (
	"errors"
	"os"
	"syscall"
)

// NewGroup returns attributes that start the process as the leader of a new
// process group.
func NewGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// NewSession returns attributes that start the process in a new session and,
// with setctty, make its stdin pty the controlling terminal.
func NewSession(setsid, setctty bool) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: setsid, Setctty: setctty}
}

// LeadsGroup reports whether attr starts the process as the leader of a new
// process group rather than joining an existing one.
func LeadsGroup(attr *syscall.SysProcAttr) bool {
	return attr != nil && attr.Setpgid && attr.Pgid == 0
}

// KillGroup terminates every process in the group led by proc.
func KillGroup(proc *os.Process) error {
	err := syscall.Kill(-proc.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// Signal sends sig to the process with the given pid.
func Signal(pid int, sig syscall.Signal) error {
	return syscall.Kill(pid, sig)
}
