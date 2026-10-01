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
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const (
	// taskkill exits with this code when the process no longer exists.
	taskkillNotFound = 128
	// KillGroup runs from exec.Cmd.Cancel and teardown paths that the Unix
	// implementation never blocks, so a wedged taskkill must not hang them.
	taskkillTimeout = 10 * time.Second
)

// NewGroup returns attributes that start the process as the root of a new
// process group. Such a process ignores CTRL_C_EVENT unless it re-enables it.
func NewGroup() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// NewSession returns nil: Windows has no sessions or controlling terminals.
func NewSession(setsid, setctty bool) *syscall.SysProcAttr {
	return nil
}

// LeadsGroup reports whether attr starts the process as the root of a new
// process group. Callers opt into having the process's descendants terminated
// with it by asking for a new group, even though KillGroup does not depend on
// the group, so that the contract matches Unix.
func LeadsGroup(attr *syscall.SysProcAttr) bool {
	return attr != nil && attr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP != 0
}

// KillGroup terminates proc and every process descended from it.
func KillGroup(proc *os.Process) error {
	sysDir, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskkillTimeout)
	defer cancel()
	// Windows has no group-wide signal; taskkill /T walks the parent-pid tree.
	// Resolving it from the system directory keeps PATH from choosing it.
	cmd := exec.CommandContext(ctx, filepath.Join(sysDir, "taskkill.exe"), taskkillArgs(proc.Pid)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	err = cmd.Run()
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
	defer func() { _ = proc.Release() }()
	return proc.Signal(sig)
}
