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
	"syscall"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc"
	"unstable.build/rune/internal/ide/console/ideconsole/workspaceshell"
	"unstable.build/rune/internal/workspace"
)

// trackedWorkspace wraps a workspace.Workspace, overriding StartCommand
// and Signal to delegate to a workspaceshell.Executor so that all
// processes started through this workspace — including those accessed
// via gRPC by extensions — are tracked.
type trackedWorkspace struct {
	workspace.Workspace
	exec *workspaceshell.Executor
}

// OnDisconnect forwards [workspace.RemoteScheme.OnDisconnect] when the
// embedded Workspace's underlying scheme is remote. Returns nil
// for local workspaces, which callers interpret as "no transport
// invalidation events will ever fire".
func (w *trackedWorkspace) OnDisconnect() <-chan struct{} {
	if rs, ok := w.Workspace.(workspace.RemoteScheme); ok {
		return rs.OnDisconnect()
	}
	return nil
}

// WaitConnected forwards [workspace.RemoteScheme.WaitConnected] when
// the embedded Workspace is remote. Local workspaces are always
// "connected".
func (w *trackedWorkspace) WaitConnected(ctx context.Context) error {
	if rs, ok := w.Workspace.(workspace.RemoteScheme); ok {
		return rs.WaitConnected(ctx)
	}
	return nil
}

// HostConn forwards [workspace.PackageHost.HostConn] when the embedded
// Workspace supports it.
func (w *trackedWorkspace) HostConn() (grpc.ClientConnInterface, bool) {
	if p, ok := w.Workspace.(workspace.PackageHost); ok {
		return p.HostConn()
	}
	return nil, false
}

func (w *trackedWorkspace) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	return w.exec.StartCommand(ctx, cmd)
}

func (w *trackedWorkspace) Signal(
	pid workspaceapi.Pid, sig syscall.Signal,
) error {
	return w.exec.Signal(pid, sig)
}
