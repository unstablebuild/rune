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

package ide

import (
	"context"
	"errors"
	"sync/atomic"
	"syscall"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

type currentExecutor struct {
	inner atomic.Pointer[schemeapi.Executor]
}

func (d *currentExecutor) set(exe schemeapi.Executor) {
	d.inner.Store(&exe)
}

func (d *currentExecutor) get() schemeapi.Executor {
	p := d.inner.Load()
	if p == nil {
		return nil
	}
	return *p
}

// StartCommand satisfies schemeapi.Executor.
func (d *currentExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	exe := d.get()
	if exe == nil {
		return 0, errors.New("currentExecutor: no underlying executor installed")
	}
	return exe.StartCommand(ctx, cmd)
}

// Signal satisfies schemeapi.Executor.
func (d *currentExecutor) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	exe := d.get()
	if exe == nil {
		return errors.New("currentExecutor: no underlying executor installed")
	}
	return exe.Signal(pid, sig)
}

// LookPath satisfies schemeapi.Executor.
func (d *currentExecutor) LookPath(ctx context.Context, name, dir string) (string, error) {
	exe := d.get()
	if exe == nil {
		return "", errors.New("currentExecutor: no underlying executor installed")
	}
	return exe.LookPath(ctx, name, dir)
}

// Close satisfies io.Closer. Close is a no-op on the proxy itself;
// ownership of the underlying executor lifecycle stays with the
// component that produced it (workspace_handler, doInit).
func (d *currentExecutor) Close() error { return nil }
