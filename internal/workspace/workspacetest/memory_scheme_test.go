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

package workspacetest

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace"
)

func TestMemoryScheme(t *testing.T) {
	ctx := context.Background()

	t.Run("at root path", func(t *testing.T) {
		TestWorkspaceSchemeFiles(t, func(t *testing.T) schemeapi.Scheme {
			uri, err := workspaceapi.ParseURI("memory:///")
			require.NoError(t, err)
			mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
			require.NoError(t, err)
			return mem
		})
	})
	t.Run("at nested path", func(t *testing.T) {
		TestWorkspaceSchemeFiles(t, func(t *testing.T) schemeapi.Scheme {
			uri, err := workspaceapi.ParseURI("memory:///var/log")
			require.NoError(t, err)
			mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
			require.NoError(t, err)
			return mem
		})
	})
	t.Run("at nested path with end-slash", func(t *testing.T) {
		TestWorkspaceSchemeFiles(t, func(t *testing.T) schemeapi.Scheme {
			uri, err := workspaceapi.ParseURI("memory:///var/log/")
			require.NoError(t, err)
			mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
			require.NoError(t, err)
			return mem
		})
	})
	t.Run("executor", func(t *testing.T) {
		TestWorkspaceSchemeExecutor(t, func(t *testing.T) schemeapi.Scheme {
			uri, err := workspaceapi.ParseURI("memory:///")
			require.NoError(t, err)
			mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
			require.NoError(t, err)
			return mem
		})
	})
}

func newMemoryScheme(t *testing.T, uri string) schemeapi.Scheme {
	t.Helper()
	u, err := workspaceapi.ParseURI(uri)
	require.NoError(t, err)
	mem, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), u)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mem.Close() })
	return mem
}

// runCommand runs cmd on scheme and returns its output and exit error.
func runCommand(t *testing.T, scheme schemeapi.Scheme, cmd workspaceapi.Cmd) (string, error) {
	t.Helper()
	var out strings.Builder
	ch := make(chan error, 1)
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.Watcher = workspaceapi.ChanProcessWatcher(ch)
	_, err := scheme.StartCommand(context.Background(), cmd)
	require.NoError(t, err)
	select {
	case err := <-ch:
		return out.String(), err
	case <-time.After(10 * time.Second):
		t.Fatal("the command did not exit")
		return "", nil
	}
}

func TestMemorySchemeRunsTheRuneShell(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("commands see the workspace files and install /bin", func(t *testing.T) {
		t.Parallel()
		mem := newMemoryScheme(t, "memory:///")
		f, err := mem.Create("/notes.md")
		require.NoError(t, err)
		_, err = f.Write([]byte("# notes\n"))
		require.NoError(t, err)
		require.NoError(t, f.Close())

		_, err = mem.Stat("/bin/ls")
		require.ErrorIs(t, err, os.ErrNotExist, "the shell is installed on first use")

		out, exit := runCommand(t, mem, workspaceapi.Cmd{Path: "sh", Args: []string{"-c", "cat notes.md; ls"}})
		require.NoError(t, exit)
		assert.Equal(t, "# notes\nbin\nnotes.md\n", out)
		_, err = mem.Stat("/bin/ls")
		assert.NoError(t, err)
	})

	t.Run("the root is the shell's home and start directory", func(t *testing.T) {
		t.Parallel()
		mem := newMemoryScheme(t, "memory:///var/log")
		out, exit := runCommand(t, mem, workspaceapi.Cmd{Path: "sh", Args: []string{"-c", "pwd; echo $HOME; cd / && ls && cd ~ && pwd"}})
		require.NoError(t, exit)
		assert.Equal(t, "/var/log\n/var/log\nbin\n/var/log\n", out)
	})

	t.Run("LookPath answers for the shell's programs", func(t *testing.T) {
		t.Parallel()
		mem := newMemoryScheme(t, "memory:///")
		got, err := mem.LookPath(ctx, "ls", "")
		require.NoError(t, err)
		assert.Equal(t, "/bin/ls", got)
		got, err = mem.LookPath(ctx, "", "")
		require.NoError(t, err)
		assert.Equal(t, "/bin/sh", got, "the login shell")
		_, err = mem.LookPath(ctx, "git", "")
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("Signal and SetPtySize before any process", func(t *testing.T) {
		t.Parallel()
		mem := newMemoryScheme(t, "memory:///")
		assert.ErrorIs(t, mem.Signal(1, syscall.SIGTERM), os.ErrProcessDone)
		assert.ErrorIs(t, mem.SetPtySize(workspaceapi.Pty{}, workspaceapi.PtySize{Columns: 1, Rows: 1}),
			workspace.ErrInvalidMasterPtyFd)
	})

	t.Run("a pty runs an interactive shell and is reachable by descriptor", func(t *testing.T) {
		t.Parallel()
		mem := newMemoryScheme(t, "memory:///")
		pty, err := mem.NewPty(ctx)
		require.NoError(t, err)
		assert.Same(t, pty.Master, mem.NewFile(pty.Master.Fd(), pty.Master.Name()),
			"the master resolves by descriptor, as over RPC")
		assert.Same(t, pty.Slave, mem.NewFile(pty.Slave.Fd(), pty.Slave.Name()))
		_, err = mem.ForegroundProcessGroup(pty)
		assert.ErrorIs(t, err, errors.ErrUnsupported, "the shell has no job control")

		ch := make(chan error, 1)
		_, err = mem.StartCommand(ctx, workspaceapi.Cmd{
			Path: "sh", Stdin: pty.Slave, Stdout: pty.Slave, Stderr: pty.Slave,
			Watcher: workspaceapi.ChanProcessWatcher(ch),
		})
		require.NoError(t, err)
		require.NoError(t, pty.Slave.Close())
		require.NoError(t, mem.SetPtySize(pty, workspaceapi.PtySize{Columns: 40, Rows: 10}))

		_, err = pty.Master.Write([]byte("echo hi\rexit\r"))
		require.NoError(t, err)
		var out strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := pty.Master.Read(buf)
			out.Write(buf[:n])
			if err != nil {
				assert.ErrorIs(t, err, io.EOF)
				break
			}
		}
		assert.Contains(t, out.String(), "Rune's built-in in-memory shell", "the banner greets the session")
		assert.Contains(t, out.String(), "\r\nhi\r\n")
		select {
		case err := <-ch:
			assert.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("the shell did not exit")
		}
		require.NoError(t, pty.Master.Close())
		assert.ErrorIs(t, mem.SetPtySize(pty, workspaceapi.PtySize{Columns: 1, Rows: 1}),
			workspace.ErrInvalidMasterPtyFd)
	})

	t.Run("Close ends the processes before the files go", func(t *testing.T) {
		t.Parallel()
		mem := newMemoryScheme(t, "memory:///")
		ch := make(chan error, 1)
		pid, err := mem.StartCommand(ctx, workspaceapi.Cmd{
			Path: "sleep", Args: []string{"60"}, Watcher: workspaceapi.ChanProcessWatcher(ch),
		})
		require.NoError(t, err)
		require.NoError(t, mem.Signal(pid, 0))
		require.NoError(t, mem.Close())
		select {
		case err := <-ch:
			assert.Error(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("the process did not exit on Close")
		}
		_, err = mem.StartCommand(ctx, workspaceapi.Cmd{Path: "echo"})
		assert.Error(t, err)
		_, err = mem.NewPty(ctx)
		assert.Error(t, err)
	})

	t.Run("a chroot runs its own shell over the shared files", func(t *testing.T) {
		t.Parallel()
		mem := newMemoryScheme(t, "memory:///")
		require.NoError(t, mem.MkdirAll("/sub", 0o755))
		sub, err := mem.Chroot("/sub")
		require.NoError(t, err)
		defer sub.Close()

		out, exit := runCommand(t, sub, workspaceapi.Cmd{Path: "sh", Args: []string{"-c", "pwd; echo $HOME; echo made > here"}})
		require.NoError(t, exit)
		assert.Equal(t, "/sub\n/sub\n", out)
		_, err = mem.Stat("/sub/here")
		assert.NoError(t, err, "the chroot writes the shared files")
		_, err = mem.Stat("/bin/ls")
		assert.NoError(t, err, "the chroot's shell shares /bin")

		out, exit = runCommand(t, mem, workspaceapi.Cmd{Path: "cat", Args: []string{"sub/here"}})
		require.NoError(t, exit)
		assert.Equal(t, "made\n", out)
	})
}

func TestMemorySchemeCallsAfterClose(t *testing.T) {
	ctx := context.Background()
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)

	closer, ok := mem.(io.Closer)
	require.True(t, ok)
	require.NoError(t, closer.Close())

	_, err = mem.Create("post-close.txt")
	require.Error(t, err,
		"Create after Close must error instead of panicking on the nil files map")

	ch := make(chan schemeapi.EventInfo, 1)
	_, err = mem.Watch("/", ch, schemeapi.Create)
	require.Error(t, err,
		"Watch after Close must error instead of panicking on the nil watchpoint maps")
}

func TestMemoryFile(t *testing.T) {
	t.Run("Write overwrites data", func(t *testing.T) {
		f := workspace.NewMemoryFile("bla", 1, 0, []byte("12345"), new(sync.Mutex))
		n, err := f.Write([]byte("ZZ"))
		require.NoError(t, err)
		assert.Equal(t, 2, n)

		nn, err := f.Seek(0, 0)
		require.NoError(t, err)
		assert.Equal(t, int64(0), nn)

		data, err := io.ReadAll(f)
		require.NoError(t, err)
		assert.Equal(t, "ZZ345", string(data))
	})

	t.Run("Read uses write offset", func(t *testing.T) {
		f := workspace.NewMemoryFile("bla", 2, 0, []byte("12345"), new(sync.Mutex))
		n, err := f.Write([]byte("ZZ"))
		require.NoError(t, err)
		assert.Equal(t, 2, n)

		data, err := io.ReadAll(f)
		require.NoError(t, err)
		assert.Equal(t, "345", string(data))
	})

	t.Run("Write grows the file without shifting existing data", func(t *testing.T) {
		f := workspace.NewMemoryFile("bla", 3, 0, nil, new(sync.Mutex))
		_, err := f.Write([]byte("abc"))
		require.NoError(t, err)
		_, err = f.Write([]byte("de"))
		require.NoError(t, err)

		_, err = f.Seek(0, 0)
		require.NoError(t, err)
		data, err := io.ReadAll(f)
		require.NoError(t, err)
		assert.Equal(t, "abcde", string(data))
	})

	t.Run("Write past the end after a partial overwrite keeps the tail", func(t *testing.T) {
		f := workspace.NewMemoryFile("bla", 4, 0, []byte("12345"), new(sync.Mutex))
		_, err := f.Seek(3, 0)
		require.NoError(t, err)
		_, err = f.Write([]byte("XYZW"))
		require.NoError(t, err)

		_, err = f.Seek(0, 0)
		require.NoError(t, err)
		data, err := io.ReadAll(f)
		require.NoError(t, err)
		assert.Equal(t, "123XYZW", string(data))
	})
}

func TestMemorySchemeOpenAppend(t *testing.T) {
	ctx := context.Background()
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)
	defer mem.Close()

	f, err := mem.Create("log.txt")
	require.NoError(t, err)
	_, err = f.Write([]byte("1234"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	a, err := mem.OpenFile("log.txt", os.O_WRONLY|os.O_APPEND, 0)
	require.NoError(t, err)
	_, err = a.Write([]byte("56"))
	require.NoError(t, err)
	require.NoError(t, a.Close())

	r, err := mem.Open("log.txt")
	require.NoError(t, err)
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "123456", string(data))
}

func TestMemoryScheme_CloseDoesNotDoubleCloseMultiEventWatchers(t *testing.T) {
	ctx := context.Background()
	uri, err := workspaceapi.ParseURI("memory:///workspace")
	require.NoError(t, err)

	mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)

	ch := make(chan schemeapi.EventInfo, 1)
	_, err = mem.Watch("/workspace/...", ch,
		schemeapi.Create, schemeapi.Write, schemeapi.Remove, schemeapi.Rename)
	require.NoError(t, err)

	// Must not panic with "close of closed channel".
	require.NotPanics(t, func() {
		require.NoError(t, mem.Close())
	})
}

func TestMemorySchemeCloseWithBlockedWatchSend(t *testing.T) {
	ctx := context.Background()
	uri, err := workspaceapi.ParseURI("memory:///workspace")
	require.NoError(t, err)
	mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)

	// Unbuffered and never consumed: the Create event sender parks.
	ch := make(chan schemeapi.EventInfo)
	_, err = mem.Watch("/workspace/...", ch, schemeapi.Create)
	require.NoError(t, err)

	createDone := make(chan struct{})
	go func() {
		defer close(createDone)
		_, _ = mem.Create("blocked.txt")
	}()

	// Let the creator park on the watch channel send.
	time.Sleep(50 * time.Millisecond)

	closer, ok := mem.(io.Closer)
	require.True(t, ok)

	closeDone := make(chan error, 1)
	go func() { closeDone <- closer.Close() }()

	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close never returned with a blocked watch send in flight")
	}
	select {
	case <-createDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Create never returned after Close")
	}
}
