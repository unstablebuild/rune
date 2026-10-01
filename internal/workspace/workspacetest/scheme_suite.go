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

//revive:disable:exported
package workspacetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	fs "io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/walkdir"
)

// awaitFlush starts an async FlusherCloser operation and blocks until
// it completes. Test helper used throughout the suite to keep the
// existing sync-style test bodies readable now that FlusherCloser is
// asynchronous.
func awaitFlush(
	t *testing.T,
	start func(context.Context) (<-chan error, error),
) error {
	t.Helper()
	ch, err := start(context.Background())
	require.NoError(t, err)
	return <-ch
}

func TestWorkspaceSchemeExecutor(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
) {
	t.Run("command should run command and collect stdout", func(t *testing.T) {
		t.Parallel()

		scheme := schemeFn(t)
		defer scheme.Close()

		var out bytes.Buffer
		ch := make(chan error)
		cmd := workspaceapi.Cmd{
			Path:    "echo",
			Args:    []string{"blablabla\nblebleble"},
			Stdout:  &out,
			Watcher: workspaceapi.ChanProcessWatcher(ch),
		}

		pid, err := scheme.StartCommand(context.Background(), cmd)
		require.NoError(t, err)
		require.NotZero(t, pid)

		require.NoError(t, <-ch)
		assert.Equal(t, "blablabla\nblebleble\n", out.String())
	})

	t.Run("command should run command and collect stderr", func(t *testing.T) {
		t.Parallel()

		scheme := schemeFn(t)
		defer scheme.Close()

		var out bytes.Buffer
		ch := make(chan error)
		cmd := workspaceapi.Cmd{
			Path:    "sh",
			Args:    []string{"-c", "echo blabla 1>&2"},
			Stderr:  &out,
			Watcher: workspaceapi.ChanProcessWatcher(ch),
		}

		pid, err := scheme.StartCommand(context.Background(), cmd)
		require.NoError(t, err)
		require.NotZero(t, pid)

		require.NoError(t, <-ch)
		assert.Equal(t, "blabla\n", out.String())
	})

	t.Run("command should run command and feed stdin data", func(t *testing.T) {
		t.Parallel()

		scheme := schemeFn(t)
		defer scheme.Close()

		var out bytes.Buffer
		ch := make(chan error)
		cmd := workspaceapi.Cmd{
			Path:    "cat",
			Stdin:   strings.NewReader("blabla"),
			Stdout:  &out,
			Watcher: workspaceapi.ChanProcessWatcher(ch),
		}

		pid, err := scheme.StartCommand(context.Background(), cmd)
		require.NoError(t, err)
		require.NotZero(t, pid)

		require.NoError(t, <-ch)
		assert.Equal(t, "blabla", out.String())
	})

	t.Run("should return error if command doesn't exist", func(t *testing.T) {
		t.Parallel()

		scheme := schemeFn(t)
		defer scheme.Close()

		ch := make(chan error)
		cmd := workspaceapi.Cmd{
			Path:    "aCommandThatShoulndtReallyExist55",
			Watcher: workspaceapi.ChanProcessWatcher(ch),
		}

		pid, err := scheme.StartCommand(context.Background(), cmd)
		require.Zero(t, pid)
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "not found"), err.Error())
	})

	t.Run("should return error if command errors ", func(t *testing.T) {
		t.Parallel()

		scheme := schemeFn(t)
		defer scheme.Close()

		var out bytes.Buffer
		ch := make(chan error)
		cmd := workspaceapi.Cmd{
			Path:    "cat",
			Args:    []string{"-X"},
			Stderr:  &out,
			Watcher: workspaceapi.ChanProcessWatcher(ch),
		}

		pid, err := scheme.StartCommand(context.Background(), cmd)
		require.NoError(t, err)
		require.NotZero(t, pid)

		require.Error(t, <-ch)
		assert.True(t, strings.Contains(out.String(), "option"), out.String())
	})

	t.Run("pass env variables", func(t *testing.T) {
		t.Parallel()

		scheme := schemeFn(t)
		defer scheme.Close()

		var out bytes.Buffer
		ch := make(chan error)
		cmd := workspaceapi.Cmd{
			Path:    "sh",
			Args:    []string{"-c", "echo $XENV"},
			Stdout:  &out,
			Env:     []string{"XENV=myEnvVar"},
			Watcher: workspaceapi.ChanProcessWatcher(ch),
		}

		pid, err := scheme.StartCommand(context.Background(), cmd)
		require.NoError(t, err)
		require.NotZero(t, pid)

		require.NoError(t, <-ch)
		assert.Equal(t, "myEnvVar\n", out.String())
	})

	t.Run("should cancel command if context cancels", func(t *testing.T) {
		t.Parallel()

		scheme := schemeFn(t)
		defer scheme.Close()

		ch := make(chan error)
		cmd := workspaceapi.Cmd{
			Path:    "sleep",
			Args:    []string{"60"},
			Watcher: workspaceapi.ChanProcessWatcher(ch),
		}

		ctx, cancel := context.WithCancel(context.Background())

		pid, err := scheme.StartCommand(ctx, cmd)
		require.NoError(t, err)
		require.NotZero(t, pid)

		cancel()

		waitCtx, cancelWait := context.WithTimeout(
			context.Background(), 2*time.Second)
		defer cancelWait()
		select {
		case <-waitCtx.Done():
			t.Logf("failed to kill process in time")
			t.Fail()
		case err = <-ch:
			require.Error(t, err)
		}
	})
}

func TestWorkspaceSchemeFiles(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
) {
	t.Run("Open", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeOpen(t, schemeFn, defaultCreateTestFile,
			io.ReadAll, (workspaceapi.File).Write, true)
	})
	t.Run("NewFile", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeNewFile(t, schemeFn, defaultCreateTestFile,
			io.ReadAll, (workspaceapi.File).Write)
	})
	t.Run("Remove", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeRemove(t, schemeFn, defaultCreateTestFile)
	})
	t.Run("Rename", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeRename(t, schemeFn, defaultCreateTestFile, io.ReadAll)
	})
	t.Run("Stat", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeStat(t, schemeFn, defaultCreateTestFile)
	})
	t.Run("Lstat", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeLstat(t, schemeFn, defaultCreateTestFile)
	})
	t.Run("Readlink", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeReadLink(t, schemeFn, defaultCreateTestFile)
	})
	t.Run("Symlink", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeSymlink(t, schemeFn, defaultCreateTestFile)
	})
	t.Run("ReadDir", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeReadDir(t, schemeFn, defaultCreateTestFile)
	})
	t.Run("MkdirAll", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeMkdirAll(t, schemeFn, defaultCreateTestFile)
	})
	t.Run("Watch", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeWatch(t, schemeFn, defaultCreateTestFile)
	})
	t.Run("workspace.ListFiles integration", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeListFilesIntegration(t, schemeFn, defaultCreateTestFile)
	})
	t.Run("workspace.Load integration", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceLoadIntegration(t, schemeFn, defaultCreateTestFile,
			readAllExceptLastEOL, (*cell.Buffer).Write)
	})
	t.Run("Chroot/Open", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeOpen(t, func(t *testing.T) schemeapi.Scheme {
			scheme := schemeFn(t)
			err := scheme.MkdirAll("./abc", 0777)
			require.NoError(t, err)
			scheme, err = scheme.Chroot("./abc")
			require.NoError(t, err)
			return scheme
		}, defaultCreateTestFile,
			io.ReadAll, (workspaceapi.File).Write, true)
	})
	t.Run("Chroot/Stat", func(t *testing.T) {
		t.Parallel()

		TestWorkspaceSchemeStat(t, func(t *testing.T) schemeapi.Scheme {
			scheme := schemeFn(t)
			err := scheme.MkdirAll("./abc", 0777)
			require.NoError(t, err)
			scheme, err = scheme.Chroot("./abc")
			require.NoError(t, err)
			return scheme
		}, defaultCreateTestFile)
	})
	t.Run("Root", func(t *testing.T) {
		t.Parallel()

		t.Run("chroot", func(t *testing.T) {
			scheme := schemeFn(t)
			err := scheme.MkdirAll("./abc", 0777)
			require.NoError(t, err)
			scheme, err = scheme.Chroot("./abc")
			require.NoError(t, err)
			assert.True(t, strings.Contains(scheme.Root(), "abc"))

			uri, err := scheme.URI(".")
			require.NoError(t, err)
			assert.Equal(t, scheme.Root(), uri.Path())
		})
		t.Run("non chroot", func(t *testing.T) {
			scheme := schemeFn(t)
			uri, err := scheme.URI(".")
			require.NoError(t, err)
			assert.Equal(t, scheme.Root(), uri.Path())
		})

	})
	t.Run("TempFile", func(t *testing.T) {
		t.Parallel()

		scheme := schemeFn(t)
		// Many files (200) so collisions are likely if TempFile's
		// suffix expansion is broken. The previous value of 1000
		// stressed the gRPC tunnel of remote schemes (SSH) more
		// than it stressed the implementation under test.
		const numFiles = 200
		var names []string
		for i := range numFiles {
			f, err := scheme.TempFile("", "prefix_*_suffix")
			require.NoError(t, err, strconv.Itoa(i))
			require.NoError(t, f.Close())
			names = append(names, f.Name())
		}
		// Clean up the files we created, but do so as part of the
		// test body rather than via 1000 t.Cleanup callbacks. The
		// LIFO cleanup ordering would otherwise force every
		// scheme.Remove to race the scheme's own teardown, which
		// can make a single hung Remove deadlock the cleanup of
		// the entire test suite over a slow remote (e.g. SSH).
		for _, n := range names {
			_ = scheme.Remove(n)
		}
	})
}

func readAllExceptLastEOL(r io.Reader) (data []byte, err error) {
	data, err = io.ReadAll(r)
	if err != nil {
		return
	}
	if bytes.HasSuffix(data, []byte{'\n'}) {
		data = data[:len(data)-1]
	}
	return
}

func TestWorkspaceLoadIntegration(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
	readAll func(io.Reader) ([]byte, error),
	write func(*cell.Buffer, []byte) (int, error),
) {
	t.Run("loads a NEW file into a buffer and flushes new data to it", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		uri, err := scheme.URI(".")
		require.NoError(t, err)
		wp := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
		fileuri := workspaceapi.Join(uri, "myFile")
		swapDir := workspaceapi.Join(uri, ".")
		buf := cell.NewBuffer()

		fc, err := wp.Load(fileuri, buf, swapDir, false)
		require.NoError(t, err)
		_, err = write(buf, []byte("newData"))
		require.NoError(t, err)

		require.NoError(t, awaitFlush(t, fc.Flush))

		f, werr := scheme.Open("myFile")
		require.Nil(t, werr)
		data, err := readAll(f)
		require.NoError(t, err)
		require.Equal(t, "newData", string(data))
	})

	t.Run("loads an existing file into a buffer and flushes new data to it", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, cleanup := createTestFile(t, scheme, "myExistingFile", "VERY ")
		defer cleanup()

		uri, err := scheme.URI(".")
		require.NoError(t, err)
		wp := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
		fileuri := workspaceapi.Join(uri, "myExistingFile")
		swapDir := workspaceapi.Join(uri, ".")
		buf := cell.NewBuffer()

		fc, err := wp.Load(fileuri, buf, swapDir, false)
		require.NoError(t, err)
		_, err = write(buf, []byte("short"))
		require.NoError(t, err)

		require.NoError(t, awaitFlush(t, fc.Flush))

		f, werr := scheme.Open("myExistingFile")
		require.Nil(t, werr)
		data, err := readAll(f)
		require.NoError(t, err)
		require.Equal(t, "VERY short", string(data))
	})

	t.Run("recovers an existing file into a buffer", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, cleanup1 := createTestFile(t, scheme, "file", "")
		defer cleanup1()

		_, cleanup2 := createTestFile(t, scheme, ".file.rswp", "mosca")
		defer cleanup2()

		uri, err := scheme.URI(".")
		require.NoError(t, err)
		wp := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
		fileuri := workspaceapi.Join(uri, "file")
		swapuri := workspaceapi.Join(uri, ".file.rswp")
		buf := cell.NewBuffer()

		fc, err := wp.Recover(fileuri, swapuri, buf, false)
		require.NoError(t, err)

		require.NoError(t, fc.Close())

		f, werr := scheme.OpenFile("file", os.O_RDONLY, 0)
		require.Nil(t, werr)
		data, err := readAll(f)
		require.NoError(t, err)
		require.Equal(t, "mosca", string(data))
	})

	t.Run("recovers a file that doesn't exist yet into a buffer", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, cleanup2 := createTestFile(t, scheme, ".file.rswp", "mosca")
		defer cleanup2()

		uri, err := scheme.URI(".")
		require.NoError(t, err)
		wp := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
		fileuri := workspaceapi.Join(uri, "file")
		swapuri := workspaceapi.Join(uri, ".file.rswp")
		buf := cell.NewBuffer()

		fc, err := wp.Recover(fileuri, swapuri, buf, false)
		require.NoError(t, err)

		require.NoError(t, fc.Close())

		f, werr := scheme.OpenFile("file", os.O_RDONLY, 0)
		require.Nil(t, werr)
		data, err := readAll(f)
		require.NoError(t, err)
		require.Equal(t, "mosca", string(data))
	})

	t.Run("flush+close should cleanup temp state such that next Load is able to flush", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		uri, err := scheme.URI(".")
		require.NoError(t, err)
		wp := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
		fileuri := workspaceapi.Join(uri, "myCloseTest")
		swapDir := workspaceapi.Join(uri, ".")

		buf := cell.NewBuffer()
		fc, err := wp.Load(fileuri, buf, swapDir, false)
		require.NoError(t, err)
		_, err = write(buf, []byte("short"))
		require.NoError(t, err)

		require.NoError(t, awaitFlush(t, fc.Flush))
		require.NoError(t, fc.Close())

		buf = cell.NewBuffer()
		fc, err = wp.Load(fileuri, buf, swapDir, false)
		require.NoError(t, err)
		_, err = write(buf, []byte("short"))
		require.NoError(t, err)

		require.NoError(t, awaitFlush(t, fc.Flush))

		f, werr := scheme.OpenFile("myCloseTest", os.O_RDONLY, 0)
		require.Nil(t, werr)
		data, err := readAll(f)
		require.NoError(t, err)
		require.Equal(t, "shortshort", string(data))
	})

	t.Run("force recover an open, flushed file should work", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		uri, err := scheme.URI(".")
		require.NoError(t, err)
		wp := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
		fileuri := workspaceapi.Join(uri, "dataAtRestTest")
		swapfileuri := workspaceapi.Join(uri, ".dataAtRestTest.rswp")
		swapDir := workspaceapi.Join(uri, ".")

		buf := cell.NewBuffer()
		_, err = wp.Load(fileuri, buf, swapDir, false)
		require.NoError(t, err)
		_, err = write(buf, []byte("short"))
		require.NoError(t, err)

		time.Sleep(2 * time.Second)

		buf = cell.NewBuffer()
		fc2, err := wp.Recover(fileuri, swapfileuri, buf, true)
		require.NoError(t, err)
		require.NoError(t, awaitFlush(t, fc2.Flush))

		f, werr := scheme.OpenFile("dataAtRestTest", os.O_RDONLY, 0)
		require.Nil(t, werr)
		data, err := readAll(f)
		require.NoError(t, err)
		require.Equal(t, "short", string(data))

		require.NoError(t, fc2.Close())
		require.NoError(t, f.Close())
	})

	t.Run("close should cleanup temp state such that next Load works", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		uri, err := scheme.URI(".")
		require.NoError(t, err)
		wp := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
		fileuri := workspaceapi.Join(uri, "myCloseTest")
		swapDir := workspaceapi.Join(uri, ".")

		buf := cell.NewBuffer()
		fc, err := wp.Load(fileuri, buf, swapDir, false)
		require.NoError(t, err)

		require.NoError(t, fc.Close())

		buf = cell.NewBuffer()
		fc, err = wp.Load(fileuri, buf, swapDir, false)
		require.NoError(t, err)
		_, err = write(buf, []byte("short"))
		require.NoError(t, err)

		require.NoError(t, awaitFlush(t, fc.Flush))

		f, werr := scheme.OpenFile("myCloseTest", os.O_RDONLY, 0)
		require.Nil(t, werr)
		data, err := readAll(f)
		require.NoError(t, err)
		require.Equal(t, "short", string(data))
	})
}

func defaultCreateTestFile(t *testing.T, s schemeapi.Scheme, filename, content string) (workspaceapi.File, func()) {
	file, werr := s.Create(filename)
	require.NoError(t, werr)
	_, err := file.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, file.Sync())
	_, err = file.Seek(0, 0)
	require.NoError(t, err)
	return file, func() {
		_ = file.Close()
		_ = s.Remove(file.Name()) // best effort
	}
}

func testFile(
	t *testing.T, f workspaceapi.File,
	readAll func(io.Reader) ([]byte, error),
	writeFile func(workspaceapi.File, []byte) (int, error),
) {
	t.Run("Name returns the file name", func(t *testing.T) {
		// implementations may or may not return the full path name
		// in the case of a file scheme, absolute is returned because
		// a schemeapi.Scheme is localized to the current working directory
		// so if the path passed to Open is relative, then we need to
		// prepend the cwd.
		assert.Contains(t, f.Name(), "file")
	})

	t.Run("Read before write", func(t *testing.T) {
		data, err := readAll(f)
		require.NoError(t, err)
		assert.Equal(t, "", string(data))
	})

	t.Run("Write", func(t *testing.T) {
		_, err := writeFile(f, []byte("1234567890"))
		require.NoError(t, err)
	})

	t.Run("Read after write before sync", func(t *testing.T) {
		/* this is undefined for now */
	})

	t.Run("Sync", func(t *testing.T) {
		err := f.Sync()
		require.NoError(t, err)
	})

	t.Run("Stat", func(t *testing.T) {
		finfo, err := f.Stat()
		require.NoError(t, err)
		assert.Equal(t, "file", finfo.Name())
		assert.WithinDuration(t, finfo.ModTime(), time.Now(), 1*time.Minute)
		assert.Equal(t, false, finfo.IsDir())
		// the following are unused atm, so we don't test for them.
		// assert.Equal(t, int64(10), finfo.Size())
		// assert.Equal(t, fs.FileMode(0644), finfo.Mode())
	})

	t.Run("Seek", func(t *testing.T) {
		_, err := f.Seek(0, 0)
		require.NoError(t, err)
	})

	t.Run("Read", func(t *testing.T) {
		data, err := readAll(f)
		require.NoError(t, err)
		assert.Equal(t, "1234567890", string(data))
	})

	t.Run("ReadAt", func(t *testing.T) {
		var buf [10]byte
		_, err := f.ReadAt(buf[:], 0)
		require.NoError(t, err)
		assert.Equal(t, "1234567890", string(buf[:]))
	})

	t.Run("Truncate", func(t *testing.T) {
		err := f.Truncate(0)
		require.NoError(t, err)
	})

	t.Run("Read after truncate", func(t *testing.T) {
		var buf [10]byte
		n, err := f.Read(buf[:])
		require.True(t, errors.Is(err, io.EOF))
		assert.Equal(t, 0, n)
	})

	t.Run("Close", func(t *testing.T) {
		require.NoError(t, f.Close())
	})
}

func TestWorkspaceSchemeNewFile(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
	readAll func(io.Reader) ([]byte, error),
	writeFile func(workspaceapi.File, []byte) (int, error),
) {
	t.Run("returns a working File if Open succeeds", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		f, err := scheme.OpenFile("file", os.O_CREATE|os.O_RDWR, 0644)
		require.NoError(t, err)
		f = scheme.NewFile(f.Fd(), f.Name())
		testFile(t, f, readAll, writeFile)
	})

	t.Run("file offsets are kept across instances of NewFile", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		f, err := scheme.OpenFile("file", os.O_CREATE|os.O_RDWR, 0644)
		require.NoError(t, err)
		f = scheme.NewFile(f.Fd(), f.Name())

		_, err = writeFile(f, []byte("1234567890"))
		require.NoError(t, err)

		require.NoError(t, f.Sync())
		_, err = f.Seek(0, 0)
		require.NoError(t, err)

		var buf [8]byte
		n, err := f.Read(buf[:])
		require.NoError(t, err)
		assert.Equal(t, 8, n)
		assert.Equal(t, "12345678", string(buf[:]))

		f = scheme.NewFile(f.Fd(), f.Name())
		n, err = f.Read(buf[:])
		require.NoError(t, err)
		assert.Equal(t, 2, n)
		assert.Equal(t, "90", string(buf[:n]))

		f = scheme.NewFile(f.Fd(), f.Name())
		_, err = f.Read(buf[:])
		require.Equal(t, io.EOF, err)
	})

	t.Run("a mismatched filename does not resolve the descriptor", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		f, err := scheme.OpenFile("file", os.O_CREATE|os.O_RDWR, 0644)
		require.NoError(t, err)

		stale := scheme.NewFile(f.Fd(), f.Name()+".stale")
		if stale == nil {
			return
		}
		// Schemes that cannot return nil hand back an invalid file
		// instead; either way the descriptor must not be usable.
		_, err = stale.Read(make([]byte, 1))
		require.Error(t, err)
	})
}

func TestWorkspaceSchemeOpen(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
	readAll func(io.Reader) ([]byte, error),
	writeFile func(workspaceapi.File, []byte) (int, error),
	testRelativeAbsolutePaths bool,
) {
	t.Run("returns error if O_CREATE flag is not passed and file doesn't exist", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, err := scheme.OpenFile("file", 0, 0644)
		require.NotNil(t, err)
		assert.True(t, errors.Is(err, os.ErrNotExist))
	})

	t.Run("truncates file if O_TRUNC is passed if file stored has data", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, cleanup := createTestFile(t, scheme, "file", "1234")
		defer cleanup()

		d, werr := scheme.OpenFile("file", os.O_RDWR|os.O_TRUNC, 0)
		require.NoError(t, werr)

		_, err := writeFile(d, []byte("zz"))
		require.NoError(t, err)

		_, err = d.Seek(0, 0)
		require.NoError(t, err)

		data, err := readAll(d)
		require.NoError(t, err)
		assert.Equal(t, "zz", string(data))

	})

	t.Run("truncates file if O_TRUNC is passed if it's a new file", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		d, werr := scheme.OpenFile("file", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
		require.NoError(t, werr)

		_, err := writeFile(d, []byte("zz"))
		require.NoError(t, err)

		_, err = d.Seek(0, 0)
		require.NoError(t, err)

		data, err := readAll(d)
		require.NoError(t, err)
		assert.Equal(t, "zz", string(data))
	})

	if testRelativeAbsolutePaths {
		t.Run("relative to cwd or absolute to cwd should be the same file", func(t *testing.T) {
			scheme := schemeFn(t)
			defer scheme.Close()
			_, cleanup := createTestFile(t, scheme, "file", "1234")
			defer cleanup()

			cwd, err := scheme.URI(".")
			require.NoError(t, err)

			d, werr := scheme.OpenFile("file", os.O_RDONLY, 0)
			require.NoError(t, werr)

			data, err := readAll(d)
			require.NoError(t, err)
			assert.Equal(t, "1234", string(data))

			absfile := filepath.Join(cwd.Path(), "file")
			d, werr = scheme.OpenFile(absfile, os.O_RDONLY, 0)
			require.NoError(t, werr, absfile)

			data, err = readAll(d)
			require.NoError(t, err)
			assert.Equal(t, "1234", string(data))
		})

		t.Run("if a relative path is passed then that should be relative to the workspace cwd", func(t *testing.T) {
			scheme := schemeFn(t)
			defer scheme.Close()
			f, werr := scheme.OpenFile("file", os.O_CREATE, 0644)
			require.NoError(t, werr)

			cwdURI, err := scheme.URI(".")
			require.NoError(t, err)

			assert.Equal(t, filepath.Join(cwdURI.Path(), "file"), f.Name())
		})

		t.Run("if an absolute path is passed then it should access even outside of cwd", func(t *testing.T) {
			scheme := schemeFn(t)
			defer scheme.Close()

			absolutePath := filepath.Join(filepath.Dir(scheme.Root()),
				filepath.Base(scheme.Root())+"_absolute_file")
			f, err := scheme.OpenFile(absolutePath, os.O_CREATE, 0644)
			require.NoError(t, err)
			defer func() {
				require.NoError(t, f.Close())
				require.NoError(t, scheme.Remove(absolutePath))
			}()
			assert.Equal(t, absolutePath, f.Name())
		})
	}

	t.Run("returns error if O_EXCL|O_CREATE flag is passed and file exist", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, cleanup := createTestFile(t, scheme, "file", "")
		defer cleanup()
		_, err := scheme.OpenFile("file", os.O_EXCL|os.O_CREATE, 0644)
		require.Error(t, err)
		assert.True(t, errors.Is(err, os.ErrExist))
	})

	t.Run("returns a working File if Open succeeds", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		f, err := scheme.OpenFile("file", os.O_CREATE|os.O_RDWR, 0644)
		require.NoError(t, err)

		testFile(t, f, readAll, writeFile)
	})
}

func TestWorkspaceSchemeRemove(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
) {
	t.Run("removes file", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, cleanup := createTestFile(t, scheme, "file", "")
		defer cleanup()
		require.NoError(t, scheme.Remove("file"))
	})
	t.Run("returns ErrNotExist if file has already been removed", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, cleanup := createTestFile(t, scheme, "file", "")
		defer cleanup()
		require.NoError(t, scheme.Remove("file"))
		err := scheme.Remove("file")
		require.Error(t, err)
		assert.True(t, errors.Is(err, os.ErrNotExist))
	})
	t.Run("returns ErrNotExist if file never existed", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		err := scheme.Remove("file")
		require.Error(t, err)
		assert.True(t, errors.Is(err, os.ErrNotExist))
	})
}

func TestWorkspaceSchemeRename(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
	readAll func(io.Reader) ([]byte, error),
) {
	t.Run("renames a file if target name doesn't exist", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, cleanup := createTestFile(t, scheme, "file", "bla")
		defer cleanup()

		require.NoError(t, scheme.Rename("file", "foile"))
		f, werr := scheme.OpenFile("foile", 0, 0)
		require.NoError(t, werr)

		data, err := readAll(f)
		require.NoError(t, err)
		assert.Equal(t, "bla", string(data))

		_, werr = scheme.OpenFile("file", 0, 0)
		require.Error(t, werr)
		assert.True(t, errors.Is(werr, os.ErrNotExist))
	})

	t.Run("renames a file, overriding the target when it exists", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, cleanup := createTestFile(t, scheme, "file", "bla")
		defer cleanup()
		_, cleanup2 := createTestFile(t, scheme, "foile", "blo")
		defer cleanup2()

		require.NoError(t, scheme.Rename("file", "foile"))
		f, werr := scheme.OpenFile("foile", 0, 0)
		require.NoError(t, werr)

		data, err := readAll(f)
		require.NoError(t, err)
		assert.Equal(t, "bla", string(data))

		_, werr = scheme.OpenFile("file", 0, 0)
		require.Error(t, werr)
		assert.True(t, errors.Is(werr, os.ErrNotExist))
	})

	t.Run("returns error if original file does not exist", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		err := scheme.Rename("file", "foile")
		require.Error(t, err)
		assert.True(t, errors.Is(err, os.ErrNotExist))
	})
}

func testWorkspaceSchemeStats(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	method func(schemeapi.Scheme, string) (os.FileInfo, error),
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
) {
	t.Run("returns a valid os.FileInfo of a regular file", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, cleanup := createTestFile(t, scheme, "file", "bla")
		defer cleanup()

		finfo, err := method(scheme, "file")
		require.NoError(t, err)

		// always needs to be base name of the file
		require.Equal(t, "file", finfo.Name())
		assert.WithinDuration(t, finfo.ModTime(), time.Now(), 1*time.Minute)
		assert.False(t, finfo.IsDir())
		// do not test unused methods of os.FileInfo
		// assert.Equal(t, int64(3), finfo.Size())
		// assert.Equal(t, fs.FileMode(0644), finfo.Mode())
	})

	t.Run("scheme Stat returns os.ErrNotExist error if file is not found", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, err := method(scheme, "file")
		require.Error(t, err)
		assert.True(t, errors.Is(err, os.ErrNotExist))
	})
}

func TestWorkspaceSchemeStat(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
) {
	testWorkspaceSchemeStats(t, schemeFn, (schemeapi.Scheme).Stat, createTestFile)

	t.Run("returns a valid os.FileInfo of a symlink file, follows link", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, cleanup := createTestFile(t, scheme, "file", "bla")
		defer cleanup()

		require.NoError(t, scheme.Symlink("file", "foile"))

		finfo, err := scheme.Stat("foile")
		require.NoError(t, err)

		require.Equal(t, "foile", finfo.Name())
		require.Equal(t, fs.FileMode(0o644), finfo.Mode())
	})
}

func TestWorkspaceSchemeLstat(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
) {
	testWorkspaceSchemeStats(t, schemeFn, (schemeapi.Scheme).Lstat, createTestFile)

	t.Run("returns a valid os.FileInfo of a symlink file, without following link", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		_, cleanup := createTestFile(t, scheme, "file", "bla")
		defer cleanup()

		require.NoError(t, scheme.Symlink("file", "foile"))

		finfo, err := scheme.Lstat("foile")
		require.NoError(t, err)

		require.Equal(t, "foile", finfo.Name())
		// Symlink permission bits are not portable: Darwin/BSD use
		// 0o755 while Linux exposes 0o777. The link bit is what we
		// actually care about here; assert that and accept any
		// permission mask.
		require.NotZero(t, finfo.Mode()&os.ModeSymlink,
			"expected ModeSymlink to be set; got mode %v", finfo.Mode())
	})
}

func TestWorkspaceSchemeReadLink(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
) {
	t.Run("should return error if underlying file is not a symlink", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		_, cleanup := createTestFile(t, scheme, "file", "")
		defer cleanup()

		_, err := scheme.Readlink("file")
		require.Error(t, err)
	})

	t.Run("should return link if file is a symlink", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		f, cleanup := createTestFile(t, scheme, "file", "")
		defer cleanup()

		require.NoError(t, scheme.Symlink(f.Name(), "otherFile"))

		actual, err := scheme.Readlink("otherFile")
		require.NoError(t, err)
		assert.Equal(t, "file", filepath.Base(actual), actual)
	})
}

func TestWorkspaceSchemeSymlink(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
) {
	t.Run("should return error if newname exists", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		_, cleanup := createTestFile(t, scheme, "file", "")
		defer cleanup()

		_, cleanup = createTestFile(t, scheme, "file2", "")
		defer cleanup()

		require.Error(t, scheme.Symlink("file", "file2"))
	})

	t.Run("a write to a symlink's newname should be propagated to original file", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		f, cleanup := createTestFile(t, scheme, "file", "")
		defer cleanup()

		require.NoError(t, scheme.Symlink(f.Name(), "otherFile"))

		link, err := scheme.OpenFile("otherFile", os.O_RDWR, 0)
		require.NoError(t, err)

		_, err = link.Write([]byte("abc"))
		require.NoError(t, err)

		data, err := io.ReadAll(f)
		require.NoError(t, err)
		assert.Equal(t, "abc", string(data))
		require.NoError(t, link.Close())
	})

	t.Run("a write to a symlink's oldname should be propagated to the link file", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		f, cleanup := createTestFile(t, scheme, "file", "")
		defer cleanup()

		require.NoError(t, scheme.Symlink(f.Name(), "otherFile"))

		_, err := f.Write([]byte("abc"))
		require.NoError(t, err)

		link, err := scheme.Open("otherFile")
		require.NoError(t, err)
		data, err := io.ReadAll(link)
		require.NoError(t, err)
		assert.Equal(t, "abc", string(data))
		require.NoError(t, link.Close())
	})
}

func TestWorkspaceSchemeListFilesIntegration(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
) {
	t.Run("os.FileInfo.IsDir on filesystem root should always return true", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		finfo, err := scheme.Stat("/")
		require.NoError(t, err)
		assert.True(t, finfo.IsDir())
	})

	t.Run("os.FileInfo.IsDir on workspace root should always return true", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		finfo, err := scheme.Stat(".")
		require.NoError(t, err)
		assert.True(t, finfo.IsDir())
	})

	t.Run("returns an empty iterator if there are no files anywhere", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		it, err := walkdir.ListFiles(context.Background(), scheme, "")
		require.NoError(t, err)
		_, ok := it.Next(context.Background())
		require.False(t, ok)
	})

	for _, path := range []string{".", ""} {
		t.Run(fmt.Sprintf("returns valid iterator with path %s", path), func(t *testing.T) {
			scheme := schemeFn(t)
			defer scheme.Close()
			totalFiles := 25

			cwd, err := scheme.URI(".")
			require.NoError(t, err)

			for i := range totalFiles {
				_, cleanup := createTestFile(t, scheme, "file"+strconv.Itoa(i), strconv.Itoa(i))
				defer cleanup()
			}

			it, err := walkdir.ListFiles(context.Background(), scheme, path)
			require.NoError(t, err)

			for i := range totalFiles {
				path, ok := it.Next(context.Background())
				require.True(t, ok, i)
				require.NoError(t, it.Err())
				assert.NotZero(t, path)
				assert.False(t, filepath.IsAbs(path))
				assert.Equal(t, cwd.Path(), filepath.Dir(filepath.Join(cwd.Path(), path)))
			}

			path, ok := it.Next(context.Background())
			require.False(t, ok)
			assert.NoError(t, it.Err())
			assert.Zero(t, path)
		})
	}

	t.Run("root does not exist returns no error and scans parent folder", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		totalFiles := 10

		cwd, err := scheme.URI(".")
		require.NoError(t, err)

		for i := range totalFiles {
			_, cleanup := createTestFile(t, scheme, "file"+strconv.Itoa(i), strconv.Itoa(i))
			defer cleanup()
		}

		it, err := walkdir.ListFiles(context.Background(), scheme, "./subfolder")
		require.NoError(t, err)

		for i := range totalFiles {
			path, ok := it.Next(context.Background())
			require.True(t, ok, i)
			require.NoError(t, it.Err())
			assert.NotZero(t, path)
			assert.False(t, filepath.IsAbs(path))
			assert.Equal(t, cwd.Path(), filepath.Dir(filepath.Join(cwd.Path(), path)))
		}

		path, ok := it.Next(context.Background())
		require.False(t, ok)
		assert.NoError(t, it.Err())
		assert.Zero(t, path)
	})

	t.Run("root is used up until base directory; full file name is ignored", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		totalFiles := 10

		cwd, err := scheme.URI(".")
		require.NoError(t, err)

		for i := range totalFiles {
			_, cleanup := createTestFile(t, scheme, "file"+strconv.Itoa(i), strconv.Itoa(i))
			defer cleanup()
		}

		it, err := walkdir.ListFiles(context.Background(), scheme, "./file1")
		require.NoError(t, err)

		for i := range totalFiles {
			path, ok := it.Next(context.Background())
			require.True(t, ok, i)
			require.NoError(t, it.Err())
			assert.NotZero(t, path)
			assert.False(t, filepath.IsAbs(path))
			assert.Equal(t, cwd.Path(), filepath.Dir(filepath.Join(cwd.Path(), path)))
		}

		path, ok := it.Next(context.Background())
		require.False(t, ok)
		assert.NoError(t, it.Err())
		assert.Zero(t, path)
	})

	t.Run("passing canceled context to the returned iterator's Next returns an error", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()
		totalFiles := 10

		for i := range totalFiles {
			_, cleanup := createTestFile(t, scheme, "file"+strconv.Itoa(i), strconv.Itoa(i))
			defer cleanup()
		}

		ctx, cancel := context.WithCancel(context.Background())

		it, err := walkdir.ListFiles(ctx, scheme, "./file1")
		require.NoError(t, err)

		cancel()
		_, ok := it.Next(ctx)
		require.False(t, ok)
		assert.Error(t, it.Err())
	})

	t.Run("names that look like env vars are literal", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		// Expanding the unset variable would resolve the directory to
		// its parent, so the walk would recurse into itself.
		dir := filepath.Join("a", "$RUNE_TEST_UNSET_VAR")
		require.NoError(t, scheme.MkdirAll(dir, 0777))
		_, cleanupF := createTestFile(t, scheme, filepath.Join(dir, "f"), "f")
		defer cleanupF()
		_, cleanupG := createTestFile(t, scheme, filepath.Join("a", "g.txt"), "g")
		defer cleanupG()

		entries, err := scheme.ReadDir(dir)
		require.NoError(t, err)
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		require.Equal(t, []string{"f"}, names)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		it, err := walkdir.ListFiles(ctx, scheme, ".")
		require.NoError(t, err)
		defer it.Close()
		var paths []string
		for {
			path, ok := it.Next(ctx)
			if !ok {
				break
			}
			paths = append(paths, path)
		}
		require.NoError(t, it.Err())
		assert.ElementsMatch(t,
			[]string{filepath.Join(dir, "f"), filepath.Join("a", "g.txt")}, paths)
	})

	t.Run("does not return error if root's base dir does not exist", func(t *testing.T) {
		// DEPRECATED: this behaviour should not be relied upon as some implementations do
		// directories and might error with a path like fi/fi/fi
		t.SkipNow()

		scheme := schemeFn(t)
		defer scheme.Close()
		totalFiles := 10

		cwd, err := scheme.URI(".")
		require.NoError(t, err)

		for i := range totalFiles {
			_, cleanup := createTestFile(t, scheme, "file"+strconv.Itoa(i), strconv.Itoa(i))
			defer cleanup()
		}

		it, err := walkdir.ListFiles(context.Background(), scheme, "fi/fi/fi/fi")
		require.NoError(t, err)

		for i := range totalFiles {
			path, ok := it.Next(context.Background())
			require.True(t, ok, i)
			require.NoError(t, it.Err())
			assert.NotZero(t, path)
			assert.False(t, filepath.IsAbs(path))
			assert.Equal(t, cwd.Path(), filepath.Dir(filepath.Join(cwd.Path(), path)))
		}

		path, ok := it.Next(context.Background())
		require.False(t, ok)
		assert.NoError(t, it.Err())
		assert.Zero(t, path)
	})
}

func TestWorkspaceSchemeReadDir(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
) {
	t.Run("a file should return an error", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		_, cleanup := createTestFile(t, scheme, "file", "")
		defer cleanup()

		_, err := scheme.ReadDir("file")
		require.Error(t, err)
	})

	t.Run("workspace dir should return all files at the workspace root directory", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		_, cleanup := createTestFile(t, scheme, "file1", "")
		defer cleanup()

		_, cleanup = createTestFile(t, scheme, "file2", "")
		defer cleanup()

		entries, err := scheme.ReadDir(".")
		require.NoError(t, err)
		require.Len(t, entries, 2)
		if entries[0].Name() == "file1" {
			assert.Equal(t, "file2", entries[1].Name())
		} else {
			assert.Equal(t, "file2", entries[0].Name())
			assert.Equal(t, "file1", entries[1].Name())
		}
	})

	// Nested-layout suite. These tests describe the directory
	// semantics that every conforming schemeapi.Scheme must satisfy
	// for ReadDir/Stat. They were added when memoryScheme was
	// flattening nested URIs to the workspace root via filepath.Base,
	// which made nested files unreachable through the editor's
	// "completion → open" path (entries listed by basename did not
	// resolve back to a real URI).
	t.Run("nested", func(t *testing.T) {
		t.Run("workspace root only lists immediate children, dirs marked as such", func(t *testing.T) {
			scheme := schemeFn(t)
			defer scheme.Close()

			require.NoError(t, scheme.MkdirAll("./a/b", 0o777))
			_, cleanup := createTestFile(t, scheme, "./a/b/leaf.txt", "leaf")
			defer cleanup()

			_, cleanup = createTestFile(t, scheme, "./root.txt", "root")
			defer cleanup()

			entries, err := scheme.ReadDir(".")
			require.NoError(t, err)

			names := make(map[string]bool, len(entries))
			for _, e := range entries {
				names[e.Name()] = e.IsDir()
			}
			assert.Contains(t, names, "a", "intermediate dir must appear at root")
			assert.True(t, names["a"], "intermediate dir entry must report IsDir=true")
			assert.Contains(t, names, "root.txt", "root-level file must appear at root")
			assert.False(t, names["root.txt"], "root-level file entry must report IsDir=false")
			assert.NotContains(t, names, "leaf.txt",
				"deeply nested file must not surface at the workspace root")
			assert.NotContains(t, names, "b",
				"deeply nested dir must not surface at the workspace root")
		})

		t.Run("descending into a subdirectory lists only its immediate children", func(t *testing.T) {
			scheme := schemeFn(t)
			defer scheme.Close()

			require.NoError(t, scheme.MkdirAll("./a/b", 0o777))
			_, cleanup := createTestFile(t, scheme, "./a/b/leaf.txt", "leaf")
			defer cleanup()
			_, cleanup = createTestFile(t, scheme, "./a/sibling.txt", "sibling")
			defer cleanup()

			entries, err := scheme.ReadDir("./a")
			require.NoError(t, err)

			names := make(map[string]bool, len(entries))
			for _, e := range entries {
				names[e.Name()] = e.IsDir()
			}
			assert.Contains(t, names, "b", "subdir must list its child dir")
			assert.True(t, names["b"], "child dir entry must report IsDir=true")
			assert.Contains(t, names, "sibling.txt", "subdir must list its child file")
			assert.False(t, names["sibling.txt"], "child file entry must report IsDir=false")
			assert.NotContains(t, names, "leaf.txt",
				"grandchild file must not surface at the parent level")
		})

		t.Run("Stat on an intermediate directory reports IsDir=true", func(t *testing.T) {
			scheme := schemeFn(t)
			defer scheme.Close()

			require.NoError(t, scheme.MkdirAll("./a/b", 0o777))
			_, cleanup := createTestFile(t, scheme, "./a/b/leaf.txt", "leaf")
			defer cleanup()

			for _, p := range []string{"./a", "./a/b"} {
				info, err := scheme.Stat(p)
				require.NoErrorf(t, err, "Stat(%q)", p)
				assert.Truef(t, info.IsDir(), "Stat(%q).IsDir", p)
			}
		})

		// fs.DirEntry consumers like walkdir.ListFiles dispatch on
		// DirEntry.Type() — not DirEntry.IsDir() — to decide whether
		// to recurse. Each directory entry must therefore carry the
		// os.ModeDir bit in Type() so recursive walkers can find the
		// subdirectories created above.
		t.Run("directory entries carry os.ModeDir in Type()", func(t *testing.T) {
			scheme := schemeFn(t)
			defer scheme.Close()

			require.NoError(t, scheme.MkdirAll("./a/b", 0o777))
			_, cleanup := createTestFile(t, scheme, "./a/b/leaf.txt", "leaf")
			defer cleanup()

			entries, err := scheme.ReadDir(".")
			require.NoError(t, err)

			var found bool
			for _, e := range entries {
				if e.Name() != "a" {
					continue
				}
				found = true
				assert.Truef(t, e.IsDir(), "entry %q must report IsDir=true", e.Name())
				assert.NotZerof(t, e.Type()&os.ModeDir,
					"entry %q.Type() must have ModeDir set; got %v", e.Name(), e.Type())
				assert.Truef(t, e.Type().IsDir(),
					"entry %q.Type().IsDir must be true; got %v", e.Name(), e.Type())
			}
			require.True(t, found, "expected directory entry 'a' to be listed")
		})
	})
}

func TestWorkspaceSchemeMkdirAll(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
) {
	t.Run("a file should return an error", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		_, cleanup := createTestFile(t, scheme, "./file", "")
		defer cleanup()

		f, werr := scheme.OpenFile("./file", os.O_RDONLY, 0)
		require.Nil(t, werr)
		require.NoError(t, f.Close())

		err := scheme.MkdirAll("./file", 0700)
		require.Error(t, err)
	})

	t.Run("workspace dir should be a no-op", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		err := scheme.MkdirAll(".", 0700)
		require.NoError(t, err)
	})

	t.Run("nested dir and then create a nested file should always work", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		err := scheme.MkdirAll("./nested/directory/very/nested", 0700)
		require.NoError(t, err)

		file, werr := scheme.OpenFile("./nested/directory/very/nested/file", os.O_CREATE, 0666)
		require.NoError(t, werr)
		require.NoError(t, file.Close())
	})
}

func expectNoMoreEvents(t *testing.T, ch chan schemeapi.EventInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	select {
	case <-ch:
		t.Log("was not expecting any more events")
		t.FailNow()
	case <-ctx.Done():
	}
}

func TestWorkspaceSchemeWatch(
	t *testing.T,
	schemeFn func(t *testing.T) schemeapi.Scheme,
	createTestFile func(*testing.T, schemeapi.Scheme, string, string) (workspaceapi.File, func()),
) {
	t.Run("schemeapi.Create watches for file created events", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		// believe it or not, some event subsystems report the directory created above
		// unless padding is added here.
		time.Sleep(100 * time.Millisecond)

		ch := make(chan schemeapi.EventInfo, 2)
		id, err := scheme.Watch(".", ch, schemeapi.Create)
		require.NoError(t, err)

		f, cleanup := createTestFile(t, scheme, "sza", "")
		defer cleanup()

		ei := <-ch
		require.NoError(t, err)
		// cannot rely on just comparing full path, as temp
		// folder used in most harnesses contains symlinks
		// and event subsystem will resolve them, whereas schemes don't.
		assert.Equal(t, filepath.Base(f.Name()), filepath.Base(ei.URI().Path()), ei.URI().Path())
		assert.Equal(t, schemeapi.Create, ei.Event())

		expectNoMoreEvents(t, ch)

		require.NoError(t, scheme.StopWatch(id))
	})

	t.Run("schemeapi.Write watches for file sync events", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		f, cleanup := createTestFile(t, scheme, "sza", "")
		defer cleanup()

		ch := make(chan schemeapi.EventInfo, 2)
		id, err := scheme.Watch(".", ch, schemeapi.Write)
		require.NoError(t, err)

		_, err = f.Write([]byte("1234"))
		require.NoError(t, err)
		err = f.Sync()
		require.NoError(t, err)
		// Close is the only guarantee that file is actually synced to the
		// underlying file system
		err = f.Close()
		require.NoError(t, err)

		ei := <-ch
		require.NoError(t, err)
		assert.Equal(t, filepath.Base(f.Name()), filepath.Base(ei.URI().Path()), ei.URI().Path())
		assert.Equal(t, schemeapi.Write, ei.Event())

		expectNoMoreEvents(t, ch)

		require.NoError(t, scheme.StopWatch(id))
	})

	t.Run("schemeapi.Rename watches for file rename events", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		f, cleanup := createTestFile(t, scheme, "sza", "")
		defer cleanup()

		ch := make(chan schemeapi.EventInfo, 3)
		id, err := scheme.Watch(".", ch, schemeapi.Rename)
		require.NoError(t, err)

		require.NoError(t, scheme.Rename(f.Name(), "SZA"))

		// macOS's fsevents reports the rename on both source and
		// target, while Linux's inotify only emits IN_MOVED_FROM
		// for the source within a watched directory (IN_MOVED_TO
		// is only delivered when the target is watched, which it
		// also is here, but notify maps IN_MOVED_TO to Create —
		// not Rename — under encode). Drain whatever events come,
		// then assert that the source filename was reported.
		var files []string
		drain := time.NewTimer(1 * time.Second)
		drained := false
		for !drained {
			select {
			case ei := <-ch:
				assert.Equal(t, schemeapi.Rename, ei.Event())
				files = append(files, filepath.Base(ei.URI().Path()))
			case <-drain.C:
				drained = true
			}
		}
		require.NotEmpty(t, files,
			"expected at least one Rename event for the source file")
		assert.Contains(t, files, "sza",
			"expected the source filename to appear among Rename events; got %v", files)

		expectNoMoreEvents(t, ch)

		require.NoError(t, scheme.StopWatch(id))
	})

	t.Run("schemeapi.Remove watches for file remove events", func(t *testing.T) {
		scheme := schemeFn(t)
		defer scheme.Close()

		f, cleanup := createTestFile(t, scheme, "sza", "")
		defer cleanup()

		ch := make(chan schemeapi.EventInfo, 2)
		id, err := scheme.Watch(".", ch, schemeapi.Remove)
		require.NoError(t, err)

		require.NoError(t, scheme.Remove(f.Name()))

		ei := <-ch
		require.NoError(t, err)
		assert.Equal(t, filepath.Base(f.Name()), filepath.Base(ei.URI().Path()), ei.URI().Path())
		assert.Equal(t, schemeapi.Remove, ei.Event())

		expectNoMoreEvents(t, ch)

		require.NoError(t, scheme.StopWatch(id))
	})
}

// inlineSchedule is a synchronous workspace.ScheduleNextTick stub
// that runs fn on the calling goroutine. Test-only: production code
// must use the host event-loop scheduler so reload's buffer
// mutations do not run on a worker goroutine.
func inlineSchedule(fn func()) bool {
	fn()
	return true
}
