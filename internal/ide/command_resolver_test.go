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
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/ideauthorizer"
)

// pathProbeExecutor answers PATH probes with the next of paths.
type pathProbeExecutor struct {
	paths []string
	cmds  []workspaceapi.Cmd
}

func (e *pathProbeExecutor) StartCommand(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	e.cmds = append(e.cmds, cmd)
	if len(e.paths) == 0 {
		return 0, errors.New("no shell")
	}
	_, _ = cmd.Stdout.Write([]byte(e.paths[0]))
	e.paths = e.paths[1:]
	cmd.Watcher.WatchProcess() <- nil
	return 1, nil
}

func (e *pathProbeExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }

func (e *pathProbeExecutor) Close() error { return nil }

func TestHostPATHProbe(t *testing.T) {
	t.Parallel()

	exec := &pathProbeExecutor{paths: []string{"/usr/bin:/bin", "/opt/bin:/usr/bin"}}
	probe := &hostPATHProbe{exec: exec}

	got, err := probe.path(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, []string{"/usr/bin", "/bin"}, got)
	require.Len(t, exec.cmds, 1)
	assert.Equal(t, "sh", exec.cmds[0].Path)
	assert.Equal(t, []string{"-c", `printf %s "$PATH"`}, exec.cmds[0].Args)

	got, err = probe.path(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, []string{"/usr/bin", "/bin"}, got, "the PATH is remembered")
	assert.Len(t, exec.cmds, 1)

	got, err = probe.path(context.Background(), true)
	require.NoError(t, err)
	assert.Equal(t, []string{"/opt/bin", "/usr/bin"}, got)

	_, err = probe.path(context.Background(), true)
	assert.Error(t, err)
}

func TestLookLocalCmdPathFallsBackToTheCurrentPATH(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "rune-test-only-tool")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))
	t.Setenv("PATH", dir)

	got, err := lookLocalCmdPath("rune-test-only-tool")
	require.NoError(t, err)
	assert.Equal(t, bin, got)

	_, err = lookLocalCmdPath("rune-test-missing-tool")
	assert.ErrorIs(t, err, ideauthorizer.ErrExecutableNotFound)
}
