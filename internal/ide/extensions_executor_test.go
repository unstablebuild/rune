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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// recordingRoot is a minimal workspaceapi.Executor that captures
// the last Cmd it was asked to start. Used to assert what
// extensionsExecutor forwards to its underlying executor.
type recordingRoot struct {
	mu     sync.Mutex
	cmd    workspaceapi.Cmd
	closed bool
	err    error
}

var _ workspaceapi.Executor = (*recordingRoot)(nil)

func (r *recordingRoot) Start(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmd = cmd
	if r.err != nil {
		return 0, r.err
	}
	return 1, nil
}

func (r *recordingRoot) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }

func (r *recordingRoot) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *recordingRoot) lastCmd() workspaceapi.Cmd {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmd
}

func TestExtensionsExecutorPinsCmdDir(t *testing.T) {
	t.Parallel()

	root := &recordingRoot{}
	exe := newExtensionsExecutorFromUnderlying(root)

	// Caller-provided Dir must be discarded: extensions don't get
	// to pick their own working directory.
	_, err := exe.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "/bin/ext",
		Dir:  "/some/caller/dir",
	})
	require.NoError(t, err)

	assert.Equal(t, extensionsCmdDir, root.lastCmd().Dir,
		"extensionsExecutor must override Cmd.Dir to the canonical "+
			"extensions working directory regardless of what the "+
			"caller asked for; a workspace-flavored dir would "+
			"either not exist on the IDE host (ssh workspaces) or "+
			"leak workspace state into extension processes")
}

func TestExtensionsExecutorPinsCmdDirWhenUnset(t *testing.T) {
	t.Parallel()

	root := &recordingRoot{}
	exe := newExtensionsExecutorFromUnderlying(root)

	_, err := exe.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "/bin/ext",
	})
	require.NoError(t, err)

	assert.Equal(t, extensionsCmdDir, root.lastCmd().Dir,
		"extensionsExecutor must set Cmd.Dir even when the caller "+
			"left it empty so extension binaries always start in a "+
			"path that exists on every supported host")
}

func TestExtensionsExecutorPropagatesStartError(t *testing.T) {
	t.Parallel()

	root := &recordingRoot{err: errors.New("boom")}
	exe := newExtensionsExecutorFromUnderlying(root)

	_, err := exe.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "/bin/ext",
	})
	require.Error(t, err)
}

func TestExtensionsExecutorCloseClosesUnderlying(t *testing.T) {
	t.Parallel()

	root := &recordingRoot{}
	exe := newExtensionsExecutorFromUnderlying(root)

	require.NoError(t, exe.Close())
	assert.True(t, root.closed,
		"extensionsExecutor.Close must propagate to the underlying "+
			"executor so file descriptors held by the local "+
			"fileScheme are released")
}

// TestExtensionsExecutorTracksLaunchedPid verifies that PIDs of
// extension binaries launched through extensionsExecutor are
// recorded in the embedded tracking shell, so that the
// "extensions process status" REPL command can list them.
//
// Before this behavior was centralized the extension PID was never
// recorded anywhere: workspaceRunner.Run forwarded straight to the
// passthrough extensionsExecutor and `process status / tree / audit`
// only saw gRPC sub-children of extensions, never the extension
// itself.
func TestExtensionsExecutorTracksLaunchedPid(t *testing.T) {
	t.Parallel()

	root := &recordingRoot{}
	exe := newExtensionsExecutorFromUnderlying(root)

	pid, err := exe.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "/bin/ext",
	})
	require.NoError(t, err)

	iter, err := exe.shell.HandleCommand(context.Background(), repl.Command{
		Name: "process",
		Args: []string{"status"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	// Render output and assert the extension PID shows up.
	out := renderResponsives(t, iter)
	assert.Contains(t, out, strconv.Itoa(int(pid)),
		"extension PIDs launched via extensionsExecutor must be "+
			"tracked by the embedded shell so 'extensions process "+
			"status' can list them")
}

// renderResponsives draws each responsive returned by iter into a
// fixed-width string writer and concatenates the result.
func renderResponsives(
	t *testing.T, iter sdkiterator.Iterator[component.Responsive],
) string {
	t.Helper()
	const width = 200
	ctx := context.Background()
	var b strings.Builder
	for {
		item, ok := iter.Next(ctx)
		if !ok {
			break
		}
		h := item.Height(width)
		if h <= 0 {
			continue
		}
		w := term.NewStringWriter(width, h)
		item.Resize(width, h)
		item.Draw(w)
		_ = w.Flush()
		b.WriteString(w.String())
	}
	require.NoError(t, iter.Err())
	return b.String()
}

func TestExtensionsCmdDirExistsOnHost(t *testing.T) {
	assert.True(t, filepath.IsAbs(extensionsCmdDir),
		"extensionsCmdDir %q must be absolute so it does not resolve against the caller's cwd",
		extensionsCmdDir)
	info, err := os.Stat(extensionsCmdDir)
	require.NoError(t, err, "extensionsCmdDir must exist on the IDE host")
	assert.True(t, info.IsDir())
}
