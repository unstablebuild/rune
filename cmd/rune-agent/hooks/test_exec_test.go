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

package hooks

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// localExec is a test-only Executor that shells out via os/exec. It
// only exists to give the hooks package an end-to-end exec path
// without depending on a workspace.
type localExec struct{}

func (localExec) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Dir = cmd.Dir
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	c.Env = append(os.Environ(), cmd.Env...)
	if err := c.Start(); err != nil {
		return 0, err
	}
	pid := workspaceapi.Pid(c.Process.Pid)
	go func() {
		err := c.Wait()
		if cmd.Watcher != nil {
			cmd.Watcher.WatchProcess() <- err
		}
	}()
	return pid, nil
}

func (localExec) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		return err
	}
	return proc.Signal(sig)
}

func (localExec) Close() error { return nil }

func (localExec) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}
