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

package procattr

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// taskkill exits with this code when the process no longer exists.
const taskkillNotFound = 128

// NewGroup returns attributes that start the process as the root of a new
// process group.
func NewGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// NewSession returns nil: Windows has no sessions or controlling terminals.
func NewSession(setsid, setctty bool) *syscall.SysProcAttr {
	return nil
}

// LeadsGroup reports whether attr starts the process as the root of a new
// process group.
func LeadsGroup(attr *syscall.SysProcAttr) bool {
	return attr != nil && attr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP != 0
}

// KillGroup terminates proc and every process descended from it.
func KillGroup(proc *os.Process) error {
	// Windows has no group-wide signal; taskkill /T walks the parent-pid tree.
	err := exec.Command("taskkill", taskkillArgs(proc.Pid)...).Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && exitErr.ExitCode() == taskkillNotFound {
		return os.ErrProcessDone
	}
	return err
}

func taskkillArgs(pid int) []string {
	return []string{"/T", "/F", "/PID", strconv.Itoa(pid)}
}

// Signal sends sig to the process with the given pid. Windows supports only
// SIGKILL.
func Signal(pid int, sig syscall.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Signal(sig)
}
