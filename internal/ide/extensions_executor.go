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
	"fmt"
	"syscall"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/ideshell/workspaceshell"
	"unstable.build/rune/internal/workspace"
)

// extensionsExecutor combines a local file scheme rooted at
// extensionsCmdDir with a workspaceshell.Executor that tracks
// extension binaries as their own root processes. It forces
// Cmd.Dir to "/tmp" for every StartCommand call.
//
// Extensions are user-owned local processes; the workspace
// directory has no special meaning to them and may not even exist
// on the host where the extension actually runs (e.g. an SSH
// workspace's path lives on the remote host, while the extension
// binary lives on the IDE host). Pinning Cmd.Dir to a path that is
// guaranteed to exist on every supported host keeps fork/exec
// deterministic regardless of the underlying scheme.
//
// Wrapping the local scheme in a workspaceshell.Executor lets the
// "extensions process" REPL command list extension PIDs even
// though they bypass the workspace's own process executor.
type extensionsExecutor struct {
	shell *workspaceshell.Executor
}

// newExtensionsExecutor builds a fresh extensionsExecutor backed by
// a local fileScheme rooted at extensionsCmdDir and a tracking
// workspaceshell.Executor.
func newExtensionsExecutor() (*extensionsExecutor, error) {
	rootURI, err := workspaceapi.CurrentUserHostURI(extensionsCmdDir)
	if err != nil {
		return nil, fmt.Errorf("extensions root uri: %w", err)
	}
	root, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), rootURI)
	if err != nil {
		return nil, fmt.Errorf("new extensions root scheme: %w", err)
	}
	return newExtensionsExecutorFromUnderlying(
		workspaceExecutorAdapter{e: root}), nil
}

// newExtensionsExecutorFromUnderlying wraps the given underlying
// executor in a tracking workspaceshell.Executor. Used by tests so
// they can inject a recording mock instead of a real fileScheme.
func newExtensionsExecutorFromUnderlying(
	underlying workspaceapi.Executor,
) *extensionsExecutor {
	return &extensionsExecutor{
		shell: workspaceshell.NewExecutor(underlying),
	}
}

// StartCommand satisfies schemeapi.Executor. It rewrites Cmd.Dir to
// extensionsCmdDir and forwards to the tracking shell executor. Any
// caller-provided Dir is dropped on purpose: extensions don't get to
// pick their working directory.
func (e *extensionsExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	cmd.Dir = extensionsCmdDir
	return e.shell.StartCommand(ctx, cmd)
}

// Signal satisfies schemeapi.Executor.
func (e *extensionsExecutor) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	return e.shell.Signal(pid, sig)
}

// Close satisfies schemeapi.Executor (and io.Closer).
func (e *extensionsExecutor) Close() error {
	return e.shell.Close()
}
