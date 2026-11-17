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

package vsh_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

// fakeInfo is a FileInfo with a chosen mode and size.
type fakeInfo struct {
	mode fs.FileMode
	size int64
}

func (i fakeInfo) Name() string       { return "fake" }
func (i fakeInfo) Size() int64        { return i.size }
func (i fakeInfo) Mode() fs.FileMode  { return i.mode }
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fakeInfo) Sys() any           { return nil }

// fakeEnv is an Environ whose variables are given as is.
type fakeEnv map[string]expand.Variable

func (e fakeEnv) Get(name string) expand.Variable { return e[name] }

func (e fakeEnv) Each(fn func(string, expand.Variable) bool) {
	for name, vr := range e {
		if !fn(name, vr) {
			return
		}
	}
}

// interpStatus is the status an interpreter error stands for.
func interpStatus(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	status, ok := errors.AsType[interp.ExitStatus](err)
	require.True(t, ok, "not a status: %v", err)
	return int(status)
}

func TestBaseEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		root, bin string
		env       []string
		extra     []string
		dir       string
		want      []string
		// values are the variables a shell ends up with.
		values map[string]string
	}{
		{name: "defaults", dir: "/",
			want:   []string{"PATH=/bin", "TERM=dumb", "HOME=/", "PWD=/", "OLDPWD=/", "SHELL=/bin/sh"},
			values: map[string]string{"PATH": "/bin", "TERM": "dumb", "HOME": "/", "SHELL": "/bin/sh"}},
		{name: "a custom root and bin", root: "/home", bin: "/tools", dir: "/home/sub",
			want:   []string{"PATH=/tools", "TERM=dumb", "HOME=/home", "PWD=/home/sub", "OLDPWD=/home/sub", "SHELL=/tools/sh"},
			values: map[string]string{"PATH": "/tools", "PWD": "/home/sub", "SHELL": "/tools/sh"}},
		{name: "the configured environment overrides the defaults", env: []string{"PATH=/x", "TERM=xterm", "PS1=$ "}, dir: "/",
			want: []string{"PATH=/bin", "TERM=dumb", "PATH=/x", "TERM=xterm", "PS1=$ ",
				"HOME=/", "PWD=/", "OLDPWD=/", "SHELL=/bin/sh"},
			values: map[string]string{"PATH": "/x", "TERM": "xterm", "PS1": "$ "}},
		{name: "the command's environment overrides the configured one", env: []string{"A=config", "B=config"},
			extra: []string{"A=command"}, dir: "/",
			want: []string{"PATH=/bin", "TERM=dumb", "A=config", "B=config", "A=command",
				"HOME=/", "PWD=/", "OLDPWD=/", "SHELL=/bin/sh"},
			values: map[string]string{"A": "command", "B": "config"}},
		{name: "the shell's variables win", env: []string{"HOME=/config", "SHELL=/bin/zsh"},
			extra: []string{"HOME=/command", "PWD=/nowhere", "OLDPWD=/x"}, dir: "/d",
			want: []string{"PATH=/bin", "TERM=dumb", "HOME=/config", "SHELL=/bin/zsh", "HOME=/command", "PWD=/nowhere", "OLDPWD=/x",
				"HOME=/", "PWD=/d", "OLDPWD=/d", "SHELL=/bin/sh"},
			values: map[string]string{"HOME": "/", "PWD": "/d", "OLDPWD": "/d", "SHELL": "/bin/sh"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newExecutor(t, vsh.Config{Root: tc.root, Bin: tc.bin, Env: tc.env})
			got := e.BaseEnv(tc.extra, tc.dir)
			assert.Equal(t, tc.want, got)
			for name, want := range tc.values {
				v, ok := vsh.EnvValue(got, name)
				assert.True(t, ok, name)
				assert.Equal(t, want, v, name)
				assert.Equal(t, want, expand.ListEnviron(got...).Get(name).String(), name)
			}
		})
	}
}

func TestExportedPairs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  fakeEnv
		want []string
	}{
		{name: "empty", env: fakeEnv{}},
		{name: "exported strings", env: fakeEnv{
			"A": {Set: true, Exported: true, Kind: expand.String, Str: "1"},
			"B": {Set: true, Exported: true, Kind: expand.String, Str: "two words"},
		}, want: []string{"A=1", "B=two words"}},
		{name: "an empty value", env: fakeEnv{"E": {Set: true, Exported: true, Kind: expand.String}}, want: []string{"E="}},
		{name: "shell variables are not exported", env: fakeEnv{
			"A": {Set: true, Exported: true, Kind: expand.String, Str: "1"},
			"L": {Set: true, Kind: expand.String, Str: "local"},
		}, want: []string{"A=1"}},
		{name: "arrays are not exported", env: fakeEnv{
			"I": {Set: true, Exported: true, Kind: expand.Indexed, List: []string{"a"}},
			"M": {Set: true, Exported: true, Kind: expand.Associative, Map: map[string]string{"k": "v"}},
			"S": {Set: true, Exported: true, Kind: expand.String, Str: "s"},
		}, want: []string{"S=s"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.ElementsMatch(t, tc.want, vsh.ExportedPairs(tc.env))
		})
	}
}

func TestPathError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "not exist", err: fs.ErrNotExist, want: syscall.ENOENT},
		{name: "exist", err: os.ErrExist, want: syscall.EEXIST},
		{name: "permission", err: fs.ErrPermission, want: syscall.EACCES},
		{name: "closed", err: fs.ErrClosed, want: syscall.EBADF},
		{name: "a wrapped sentinel", err: fmt.Errorf("lookup: %w", fs.ErrNotExist), want: syscall.ENOENT},
		{name: "a path error's sentinel", err: &os.PathError{Op: "stat", Path: "/x", Err: fs.ErrNotExist}, want: syscall.ENOENT},
		{name: "a path error's errno", err: &os.PathError{Op: "open", Path: "/x", Err: syscall.ENOTDIR}, want: syscall.ENOTDIR},
		{name: "nested path errors", err: &os.PathError{Op: "a", Path: "/a", Err: &os.PathError{Op: "b", Path: "/b", Err: fs.ErrExist}},
			want: syscall.EEXIST},
		{name: "an errno", err: syscall.EISDIR, want: syscall.EISDIR},
		{name: "an errno standing for a sentinel", err: syscall.ENOENT, want: syscall.ENOENT},
		{name: "another error", err: io.ErrUnexpectedEOF, want: io.ErrUnexpectedEOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := vsh.PathError("open", "rel/p", tc.err)
			assert.Equal(t, &os.PathError{Op: "open", Path: "rel/p", Err: tc.want}, got)
		})
	}
}

func TestProcFS(t *testing.T) {
	t.Parallel()
	type call struct {
		// op is Open, Create, OpenFile, Stat or Lstat.
		op   string
		path string
		flag int
	}
	tests := []struct {
		name  string
		call  call
		stdin string
		write string
		// read is what the opened file reads; mode is what a stat
		// reports.
		read   string
		mode   fs.FileMode
		stdout string
		err    error
		after  map[string]string
		absent []string
	}{
		{name: "Open /dev/stdin", call: call{op: "Open", path: "/dev/stdin"}, stdin: "in", read: "in"},
		{name: "Open /dev/null", call: call{op: "Open", path: "/dev/null"}},
		{name: "Open an unclean device path", call: call{op: "Open", path: "/dev/../dev/stdin"}, stdin: "in", read: "in"},
		{name: "Create /dev/stdout", call: call{op: "Create", path: "/dev/stdout"}, write: "out", stdout: "out"},
		{name: "OpenFile /dev/null for writing", call: call{op: "OpenFile", path: "/dev/null", flag: os.O_WRONLY | os.O_CREATE | os.O_TRUNC}, write: "gone"},
		{name: "OpenFile /dev/tty without a terminal", call: call{op: "OpenFile", path: "/dev/tty", flag: os.O_RDWR}, err: syscall.ENXIO},
		{name: "Open a file", call: call{op: "Open", path: "a.txt"}, read: "alpha\n"},
		{name: "Open a missing file", call: call{op: "Open", path: "missing"}, err: fs.ErrNotExist},
		{name: "Create a file", call: call{op: "Create", path: "new"}, write: "x", after: map[string]string{"new": "x"}},
		{name: "Create truncates", call: call{op: "Create", path: "a.txt"}, write: "b", after: map[string]string{"a.txt": "b"}},
		{name: "Create in a directory", call: call{op: "Create", path: "d/new"}, write: "x", after: map[string]string{"d/new": "x"}},
		{name: "Create in a missing directory", call: call{op: "Create", path: "no/new"}, err: syscall.ENOENT, absent: []string{"no"}},
		{name: "Create below a file", call: call{op: "Create", path: "a.txt/new"}, err: syscall.ENOTDIR,
			after: map[string]string{"a.txt": "alpha\n"}},
		{name: "OpenFile O_CREATE in a missing directory", call: call{op: "OpenFile", path: "no/deeper/new", flag: os.O_WRONLY | os.O_CREATE},
			err: syscall.ENOENT, absent: []string{"no"}},
		{name: "OpenFile without O_CREATE in a missing directory", call: call{op: "OpenFile", path: "no/new", flag: os.O_WRONLY},
			err: fs.ErrNotExist, absent: []string{"no"}},
		{name: "Stat /dev/tty without a terminal", call: call{op: "Stat", path: "/dev/tty"}, mode: fs.ModeDevice | fs.ModeCharDevice | 0o666},
		{name: "Lstat /dev/null", call: call{op: "Lstat", path: "/dev/null"}, mode: fs.ModeDevice | fs.ModeCharDevice | 0o666},
		{name: "Stat a file", call: call{op: "Stat", path: "bin/ls"}, mode: 0o755},
		{name: "Lstat a missing file", call: call{op: "Lstat", path: "missing"}, err: fs.ErrNotExist},
	}
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					f := vshtest.New(t, vshtest.Options{Backend: backend, Files: map[string]string{"a.txt": "alpha\n", "d/.keep": ""}})
					var stdout bytes.Buffer
					fsys := vsh.NewProcFS(f.Scheme, strings.NewReader(tc.stdin), &stdout, io.Discard)
					p := tc.call.path
					if !strings.HasPrefix(p, "/dev") {
						p = f.Path(p)
					}
					var file workspaceapi.File
					var info fs.FileInfo
					var err error
					switch tc.call.op {
					case "Open":
						file, err = fsys.Open(p)
					case "Create":
						file, err = fsys.Create(p)
					case "OpenFile":
						file, err = fsys.OpenFile(p, tc.call.flag, 0o644)
					case "Stat":
						info, err = fsys.Stat(p)
					case "Lstat":
						info, err = fsys.Lstat(p)
					}
					if tc.err != nil {
						require.ErrorIs(t, err, tc.err)
					} else {
						require.NoError(t, err)
					}
					if info != nil {
						assert.Equal(t, tc.mode, vsh.FileMode(info))
					}
					if file != nil {
						if tc.write != "" {
							_, err := file.Write([]byte(tc.write))
							require.NoError(t, err)
						} else {
							got, err := io.ReadAll(vsh.NewFileReader(file))
							require.NoError(t, err)
							assert.Equal(t, tc.read, string(got))
						}
						require.NoError(t, file.Close())
					}
					assert.Equal(t, tc.stdout, stdout.String())
					for name, content := range tc.after {
						assert.Equal(t, content, f.ReadFile(t, name), name)
					}
					for _, name := range tc.absent {
						_, err := f.Scheme.Lstat(f.Path(name))
						assert.ErrorIs(t, err, fs.ErrNotExist, name)
					}
				})
			}
		})
	}
}

func TestProcFSPassesThrough(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: map[string]string{"a": "a\n"}})
	fsys := vsh.NewProcFS(f.Scheme, nil, nil, nil)
	require.NoError(t, fsys.MkdirAll(f.Path("x/y"), 0o755))
	require.NoError(t, fsys.Rename(f.Path("a"), f.Path("x/b")))
	entries, err := fsys.ReadDir(f.Path("x"))
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"b", "y"}, names)
	require.NoError(t, fsys.Remove(f.Path("x/b")))
	_, err = f.Scheme.Stat(f.Path("x/b"))
	assert.ErrorIs(t, err, fs.ErrNotExist)
	assert.Equal(t, f.Scheme.Join("a", "b"), fsys.Join("a", "b"))
}

func TestDevFile(t *testing.T) {
	t.Parallel()
	fsys := vsh.NewProcFS(vshtest.New(t, vshtest.Options{}).Scheme, strings.NewReader("abcdef"), io.Discard, io.Discard)
	file, err := fsys.Open("/dev/stdin")
	require.NoError(t, err)
	assert.Equal(t, "/dev/stdin", file.Name())
	assert.Equal(t, ^uintptr(0), file.Fd())
	assert.NoError(t, file.Sync())
	assert.NoError(t, file.Truncate(0))
	off, err := file.Seek(10, io.SeekStart)
	assert.NoError(t, err)
	assert.Zero(t, off, "a device has no offset")
	info, err := file.Stat()
	require.NoError(t, err)
	assert.Equal(t, "stdin", info.Name())

	buf := make([]byte, 2)
	for _, want := range []string{"ab", "cd", "ef"} {
		n, err := file.ReadAt(buf, 100)
		require.NoError(t, err)
		assert.Equal(t, want, string(buf[:n]), "ReadAt reads on from the last read")
	}
	_, err = file.ReadAt(buf, 0)
	assert.ErrorIs(t, err, io.EOF)
	assert.NoError(t, file.Close())
}

func TestDeviceInfo(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"null", "stdin", "stdout", "stderr", "tty"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			info := vsh.DeviceInfo(name)
			assert.Equal(t, name, info.Name())
			assert.Zero(t, info.Size())
			assert.Equal(t, fs.ModeDevice|fs.ModeCharDevice|0o666, info.Mode())
			assert.False(t, info.IsDir())
			assert.False(t, info.Mode().IsRegular())
			assert.True(t, info.ModTime().IsZero())
			assert.Nil(t, info.Sys())
			assert.Equal(t, info.Mode(), vsh.FileMode(info))
		})
	}
}

func TestCheckParent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		err  error
	}{
		{path: "new"},
		{path: "d/new"},
		{path: "d/existing"},
		{path: "no/new", err: fs.ErrNotExist},
		{path: "no/deeper/new", err: fs.ErrNotExist},
		{path: "f/new", err: syscall.ENOTDIR},
	}
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Backend: backend, Files: map[string]string{"f": "", "d/existing": ""}})
			for _, tc := range tests {
				t.Run(tc.path, func(t *testing.T) {
					err := vsh.CheckParent(f.Scheme, f.Path(tc.path))
					if tc.err == nil {
						assert.NoError(t, err)
						return
					}
					assert.ErrorIs(t, err, tc.err)
				})
			}
		})
	}
}

func TestPermits(t *testing.T) {
	t.Parallel()
	const (
		r = interp.AccessRead
		w = interp.AccessWrite
		x = interp.AccessExec
	)
	tests := []struct {
		name string
		mode fs.FileMode
		// allowed lists the access modes granted; every other
		// combination is denied.
		allowed []interp.AccessMode
	}{
		{name: "0644 file", mode: 0o644, allowed: []interp.AccessMode{r, w, r | w}},
		{name: "0755 file", mode: 0o755, allowed: []interp.AccessMode{r, w, x, r | w, r | x, w | x, r | w | x}},
		{name: "0755 directory", mode: fs.ModeDir | 0o755, allowed: []interp.AccessMode{r, w, x, r | w, r | x, w | x, r | w | x}},
		{name: "0400 file", mode: 0o400, allowed: []interp.AccessMode{r}},
		{name: "0200 file", mode: 0o200, allowed: []interp.AccessMode{w}},
		{name: "0100 file", mode: 0o100, allowed: []interp.AccessMode{x}},
		{name: "only group and other bits", mode: 0o077},
		{name: "a file with no permission bits is 0644", mode: 0, allowed: []interp.AccessMode{r, w, r | w}},
		{name: "a directory with no permission bits is 0755", mode: fs.ModeDir,
			allowed: []interp.AccessMode{r, w, x, r | w, r | x, w | x, r | w | x}},
		{name: "a symlink with no permission bits", mode: fs.ModeSymlink},
		{name: "a device", mode: fs.ModeDevice | fs.ModeCharDevice | 0o666, allowed: []interp.AccessMode{r, w, r | w}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			info := fakeInfo{mode: tc.mode}
			assert.True(t, vsh.Permits(info, 0), "no access is always granted")
			for m := interp.AccessMode(1); m <= r|w|x; m++ {
				assert.Equal(t, slices.Contains(tc.allowed, m), vsh.Permits(info, m), "access %03b", m)
			}
		})
	}
}

func TestFileMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		mode, want fs.FileMode
	}{
		{name: "a file without bits", mode: 0, want: 0o644},
		{name: "a directory without bits", mode: fs.ModeDir, want: fs.ModeDir | 0o755},
		{name: "a file with bits", mode: 0o600, want: 0o600},
		{name: "a file with only an exec bit", mode: 0o001, want: 0o001},
		{name: "a directory with bits", mode: fs.ModeDir | 0o700, want: fs.ModeDir | 0o700},
		{name: "a symlink without bits", mode: fs.ModeSymlink, want: fs.ModeSymlink},
		{name: "a device without bits", mode: fs.ModeDevice | fs.ModeCharDevice, want: fs.ModeDevice | fs.ModeCharDevice},
		{name: "a pipe without bits", mode: fs.ModeNamedPipe, want: fs.ModeNamedPipe},
		{name: "a setuid file without permission bits", mode: fs.ModeSetuid, want: fs.ModeSetuid | 0o644},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, vsh.FileMode(fakeInfo{mode: tc.mode}))
			assert.Equal(t, tc.want, vsh.StatInfo(fakeInfo{mode: tc.mode, size: 3}).Mode())
		})
	}
}

func TestStatInfo(t *testing.T) {
	t.Parallel()
	info := vsh.StatInfo(fakeInfo{mode: 0, size: 5})
	assert.Equal(t, "fake", info.Name())
	assert.Equal(t, int64(5), info.Size())
	assert.Equal(t, fs.FileMode(0o644), info.Mode())
	assert.False(t, info.IsDir())
	assert.NotNil(t, info.Sys(), "the interpreter's -O and -G type-assert Sys")
}

func TestExecutorStatAndAccess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		path   string
		follow bool
		mode   fs.FileMode
		err    error
		// denied lists the access modes the access handler refuses
		// with EACCES.
		denied []interp.AccessMode
	}{
		{name: "a file", path: "f", follow: true, mode: 0o644, denied: []interp.AccessMode{interp.AccessExec}},
		{name: "a program", path: "bin/ls", follow: true, mode: 0o755},
		{name: "a directory", path: "d", follow: true, mode: fs.ModeDir | 0o755},
		{name: "a missing file", path: "missing", follow: true, err: fs.ErrNotExist},
		{name: "a device", path: "/dev/null", follow: true, mode: fs.ModeDevice | fs.ModeCharDevice | 0o666,
			denied: []interp.AccessMode{interp.AccessExec}},
		{name: "a device without following", path: "/dev/tty", mode: fs.ModeDevice | fs.ModeCharDevice | 0o666,
			denied: []interp.AccessMode{interp.AccessExec}},
		{name: "a file without following", path: "f", mode: 0o644, denied: []interp.AccessMode{interp.AccessExec}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Files: map[string]string{"f": "x", "d/.keep": ""}})
			p := f.Path(tc.path)
			info, err := f.Exec.Stat(p, tc.follow)
			accessErr := f.Exec.Access(p, interp.AccessRead)
			if tc.err != nil {
				assert.ErrorIs(t, err, tc.err)
				assert.Equal(t, &os.PathError{Op: "access", Path: p, Err: syscall.ENOENT}, accessErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.mode, info.Mode())
			assert.NoError(t, accessErr)
			for _, m := range tc.denied {
				assert.Equal(t, &os.PathError{Op: "access", Path: p, Err: syscall.EACCES}, f.Exec.Access(p, m))
			}
		})
	}
}

func TestCallHandler(t *testing.T) {
	t.Parallel()
	hidden := vsh.HiddenPrefix
	tests := []struct {
		args []string
		want []string
	}{
		{[]string{"ls", "-l"}, []string{"ls", "-l"}},
		{[]string{"type", "ls"}, []string{hidden + "type", "ls"}},
		{[]string{"type"}, []string{hidden + "type"}},
		{[]string{"help"}, []string{hidden + "help"}},
		{[]string{"help", "cd"}, []string{hidden + "help", "cd"}},
		{[]string{"command", "-v", "ls"}, []string{hidden + "command", "-v", "ls"}},
		{[]string{"command", "-V", "ls"}, []string{hidden + "command", "-V", "ls"}},
		{[]string{"command", "-pV", "ls"}, []string{hidden + "command", "-pV", "ls"}},
		{[]string{"command", "-p", "-v", "ls"}, []string{hidden + "command", "-p", "-v", "ls"}},
		{[]string{"command", "ls", "-v"}, []string{"command", "ls", "-v"}},
		{[]string{"command", "-p", "ls"}, []string{"command", "-p", "ls"}},
		{[]string{"command", "--", "-v"}, []string{"command", "--", "-v"}},
		{[]string{"command"}, []string{"command"}},
		{[]string{"pwd", "-P"}, []string{"pwd"}},
		{[]string{"pwd", "-L"}, []string{"pwd", "-L"}},
		{[]string{"pwd", "-P", "-L", "-P"}, []string{"pwd", "-L"}},
		{[]string{"pwd"}, []string{"pwd"}},
		{[]string{"source", "lib.sh"}, []string{"source", "./lib.sh"}},
		{[]string{"source", "lib.sh", "arg"}, []string{"source", "./lib.sh", "arg"}},
		{[]string{".", "lib.sh"}, []string{".", "./lib.sh"}},
		{[]string{".", "./lib.sh"}, []string{".", "./lib.sh"}},
		{[]string{".", "dir/lib.sh"}, []string{".", "dir/lib.sh"}},
		{[]string{".", "/abs/lib.sh"}, []string{".", "/abs/lib.sh"}},
		{[]string{"source"}, []string{"source"}},
		{[]string{"echo", "type"}, []string{"echo", "type"}},
	}
	for _, tc := range tests {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			args := slices.Clone(tc.args)
			got, err := vsh.CallHandler(context.Background(), args)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.args, args, "the caller's arguments are not modified")
		})
	}
}

func TestParseScript(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		src  string
		err  string
		// stmts counts the top-level statements of a valid script.
		stmts int
	}{
		{name: "a pipeline and a command substitution", src: "ls | wc -l; echo $(ls)", stmts: 2},
		{name: "lines", src: "a\nb\n\nc\n", stmts: 3},
		{name: "empty", src: ""},
		{name: "redirections and here-documents", src: "cat <<EOF > f\nx\nEOF\n", stmts: 1},
		{name: "process substitution", src: "cat <(ls)", err: "1:5: process substitution is not supported"},
		{name: "an output process substitution", src: "echo ok\ntee >(cat)", err: "2:5: process substitution is not supported"},
		{name: "process substitution in a function", src: "f() { cat <(ls); }", err: "1:11: process substitution is not supported"},
		{name: "process substitution in a subshell", src: "(diff <(a) b)", err: "1:7: process substitution is not supported"},
		{name: "process substitution in a command substitution", src: "echo $(cat <(ls))", err: "1:12: process substitution is not supported"},
		{name: "process substitution in a loop", src: "for i in 1; do wc < <(ls); done", err: "1:21: process substitution is not supported"},
		{name: "coproc", src: "coproc ls", err: "1:1: coproc is not supported"},
		{name: "a coproc in a command substitution", src: "x=$( (coproc c) )", err: "1:7: coproc is not supported"},
		{name: "the first unsupported construct", src: "coproc a\ncat <(b)", err: "1:1: coproc is not supported"},
		{name: "a syntax error", src: "if", err: "1:1: `if` must be followed by a statement list"},
		{name: "an unclosed quote", src: "echo 'x", err: "1:6: reached EOF without closing quote `'`"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := vsh.ParseScript([]byte(tc.src), "script name")
			if tc.err != "" {
				assert.EqualError(t, err, tc.err, "errors name no script, for the caller to")
				return
			}
			require.NoError(t, err)
			assert.Len(t, f.Stmts, tc.stmts)
			assert.Equal(t, "script name", f.Name, "the name is the script's $0")
		})
	}
}

func TestCallStmt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
	}{
		{name: "no arguments"},
		{name: "spaces and empty strings", args: []string{"a b", "", "  "}},
		{name: "quotes", args: []string{"it's", `"double"`, `\`}},
		{name: "expansions stay literal", args: []string{"$HOME", "${x:-y}", "$(ls)", "`ls`", "*", "~", "a{b,c}"}},
		{name: "control characters", args: []string{"a\nb", "\t", ";", "|", "&&"}},
	}
	f := vshtest.New(t, vshtest.Options{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stmt := vsh.CallStmt("printf", tc.args)
			call, ok := stmt.Cmd.(*syntax.CallExpr)
			require.True(t, ok)
			var words []string
			for _, w := range call.Args {
				require.Len(t, w.Parts, 1)
				q, ok := w.Parts[0].(*syntax.SglQuoted)
				require.True(t, ok)
				words = append(words, q.Value)
			}
			assert.Equal(t, append([]string{"printf"}, tc.args...), words)

			// printf runs through callStmt as a program; each
			// argument must come back unchanged.
			args := append([]string{"%s|"}, tc.args...)
			stdout, stderr, exit := run(t, f, workspaceapi.Cmd{Path: "printf", Args: args})
			require.NoError(t, exit, stderr)
			want := ""
			for _, a := range tc.args {
				want += a + "|"
			}
			if len(tc.args) == 0 {
				want = "|"
			}
			assert.Equal(t, want, stdout)
		})
	}
}

func TestCommandStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		err    error
		want   error
		stderr string
	}{
		{name: "success", err: nil, want: nil},
		{name: "an exit status", err: &vsh.ExitError{Code: 3}, want: interp.ExitStatus(3)},
		{name: "a wrapped exit status", err: fmt.Errorf("x: %w", &vsh.ExitError{Code: 4}), want: interp.ExitStatus(4)},
		{name: "exit status 0", err: &vsh.ExitError{}, want: interp.ExitStatus(0)},
		{name: "a signal is 128 plus its number", err: &vsh.ExitError{Code: -1, Signal: syscall.SIGTERM}, want: interp.ExitStatus(128 + 15)},
		{name: "an interpreter status", err: interp.ExitStatus(5), want: interp.ExitStatus(5)},
		{name: "cancellation passes through", err: context.Canceled, want: context.Canceled},
		{name: "a deadline passes through", err: context.DeadlineExceeded, want: context.DeadlineExceeded},
		{name: "wrapped cancellation passes through", err: fmt.Errorf("read: %w", context.Canceled),
			want: fmt.Errorf("read: %w", context.Canceled)},
		{name: "another error is printed", err: errors.New("boom"), want: interp.ExitStatus(1), stderr: "cmd: boom\n"},
		{name: "a path error is printed", err: &os.PathError{Op: "open", Path: "x", Err: syscall.ENOENT},
			want: interp.ExitStatus(1), stderr: "cmd: open x: no such file or directory\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			got := vsh.CommandStatus("cmd", tc.err, &stderr)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.stderr, stderr.String())
		})
	}
}

// errReader fails every read.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestRunProgram(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		argv  []string
		depth int
		// stdin is the program's standard input; nil is none.
		stdin          io.Reader
		stdout, stderr string
		code           int
	}{
		{name: "a command", argv: []string{"cat", "f"}, stdout: "content\n"},
		{name: "a command reading stdin", argv: []string{"cat"}, stdin: strings.NewReader("piped\n"), stdout: "piped\n"},
		{name: "a command without stdin", argv: []string{"cat"}},
		{name: "a shell builtin", argv: []string{"which", "ls"}, stdout: "/bin/ls\n"},
		{name: "an interpreter builtin", argv: []string{"printf", "%s-%s", "a", "b c"}, stdout: "a-b c"},
		{name: "a failing interpreter builtin", argv: []string{"false"}, code: 1},
		{name: "a script", argv: []string{"./s.sh", "x"}, stdout: "./s.sh x\n"},
		{name: "sh -c names itself", argv: []string{"sh", "-c", "echo $0 $#"}, stdout: "sh 0\n"},
		{name: "bash -c names itself", argv: []string{"bash", "-c", "echo $0"}, stdout: "bash\n"},
		{name: "sh -c with a name and parameters", argv: []string{"sh", "-c", `echo "$0|$1|$2"`, "nm", "p1", "p 2"}, stdout: "nm|p1|p 2\n"},
		{name: "sh -c with options", argv: []string{"sh", "-eu", "-c", "false; echo no"}, code: 1},
		{name: "sh -c with -o", argv: []string{"sh", "-o", "pipefail", "-c", "false | true"}, code: 1},
		{name: "a syntax error names the shell", argv: []string{"sh", "-c", "echo )"},
			stderr: "sh: 1:6: a command can only contain words and redirects; encountered `)`\n", code: 2},
		{name: "a syntax error names $0", argv: []string{"sh", "-c", "if", "nm"},
			stderr: "nm: 1:1: `if` must be followed by a statement list\n", code: 2},
		{name: "a script operand", argv: []string{"sh", "s.sh", "a"}, stdout: "s.sh a\n"},
		{name: "a missing script operand", argv: []string{"bash", "nope.sh"},
			stderr: "bash: nope.sh: no such file or directory\n", code: 127},
		{name: "a directory as the script operand", argv: []string{"sh", "d"},
			stderr: "sh: d: is a directory\n", code: 127},
		{name: "a script from stdin", argv: []string{"sh"}, stdin: strings.NewReader("echo $0\nexit 3\n"), stdout: "sh\n", code: 3},
		{name: "a syntax error from stdin", argv: []string{"sh"}, stdin: strings.NewReader("fi\n"),
			stderr: "sh: 1:1: `fi` can only be used to end an `if`\n", code: 2},
		{name: "no stdin and no script", argv: []string{"sh"}},
		{name: "an unreadable stdin", argv: []string{"sh"}, stdin: errReader{errors.New("boom")},
			stderr: "sh: read script: boom\n", code: 126},
		{name: "the deepest shell runs", argv: []string{"sh", "-c", "echo deep"}, depth: 16, stdout: "deep\n"},
		{name: "a shell too deep", argv: []string{"sh", "-c", "echo deep"}, depth: 17,
			stderr: "sh: too many nested shells\n", code: 126},
		{name: "a command too deep", argv: []string{"cat", "f"}, depth: 17,
			stderr: "cat: too many nested shells\n", code: 126},
		{name: "nested shells count towards the limit", argv: []string{"sh", "-c", "sh -c 'echo nested'"}, depth: 15, stdout: "nested\n"},
		{name: "nested shells beyond the limit", argv: []string{"sh", "-c", "sh -c 'echo nested'"}, depth: 16,
			stderr: "sh: too many nested shells\n", code: 126},
	}
	f := vshtest.New(t, vshtest.Options{Files: map[string]string{
		"f": "content\n", "s.sh": "echo \"$0 $1\"\n", "d/.keep": "",
	}})
	seedExec(t, f, "s.sh", "echo \"$0 $1\"\n")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			err := f.Exec.RunProgram(context.Background(), tc.depth, tc.argv[0], tc.argv[1:], f.Home, tc.stdin, &stdout, &stderr)
			assert.Equal(t, tc.stdout, stdout.String(), "stdout")
			assert.Equal(t, tc.stderr, stderr.String(), "stderr")
			assert.Equal(t, tc.code, interpStatus(t, err), "status")
		})
	}
}

func TestRunCommand(t *testing.T) {
	t.Parallel()
	commands := []vsh.Command{
		{Name: "fails", Run: func(context.Context, *vsh.Invocation) error { return errors.New("boom") }},
		{Name: "status", Run: func(context.Context, *vsh.Invocation) error { return &vsh.ExitError{Code: 7} }},
		{Name: "signalled", Run: func(context.Context, *vsh.Invocation) error {
			return &vsh.ExitError{Code: -1, Signal: syscall.SIGTERM}
		}},
		{Name: "term", Run: func(_ context.Context, inv *vsh.Invocation) error {
			_, err := fmt.Fprintf(inv.Stdout, "terminal: %t\n", inv.Term != nil)
			return err
		}},
		{Name: "args", Run: func(_ context.Context, inv *vsh.Invocation) error {
			_, err := fmt.Fprintf(inv.Stdout, "%s %q %s\n", inv.Name, inv.Args, inv.Dir)
			return err
		}},
		{Name: "env", Run: func(_ context.Context, inv *vsh.Invocation) error {
			for _, name := range []string{"LOCAL", "EXPORTED", "HOME", "PATH"} {
				fmt.Fprintf(inv.Stdout, "%s=%s;", name, inv.Env.Get(name).String())
			}
			_, err := fmt.Fprintln(inv.Stdout)
			return err
		}},
		{Name: "copy", Run: func(_ context.Context, inv *vsh.Invocation) error {
			_, err := io.Copy(inv.Stdout, inv.Stdin)
			return err
		}},
	}
	tests := []struct {
		name           string
		argv           []string
		stdin          string
		stdout, stderr string
		code           int
		msg            string
	}{
		{name: "an error is printed with status 1", argv: []string{"fails"}, stderr: "fails: boom\n", code: 1, msg: "exit status 1"},
		{name: "an exit error sets the status", argv: []string{"status"}, code: 7, msg: "exit status 7"},
		{name: "a script sees the status", argv: []string{"sh", "-c", "status; echo $?; fails; echo $?"},
			stdout: "7\n1\n", stderr: "fails: boom\n"},
		{name: "a signalled command is 128 plus the signal", argv: []string{"sh", "-c", "signalled; echo $?"}, stdout: "143\n"},
		{name: "no terminal", argv: []string{"term"}, stdout: "terminal: false\n"},
		{name: "arguments and directory", argv: []string{"args", "a", "b c", ""}, stdout: "args [\"a\" \"b c\" \"\"] /\n"},
		{name: "the shell's directory", argv: []string{"sh", "-c", "cd d && args x"}, stdout: "args [\"x\"] /d\n"},
		{name: "a path invocation", argv: []string{"/bin/args"}, stdout: "args [] /\n"},
		{name: "only exported variables", argv: []string{"sh", "-c", "LOCAL=1; export EXPORTED=2; env"},
			stdout: "LOCAL=;EXPORTED=2;HOME=/;PATH=/bin;\n"},
		{name: "standard input", argv: []string{"copy"}, stdin: "in\n", stdout: "in\n"},
		{name: "a pipe", argv: []string{"sh", "-c", "echo piped | copy | copy"}, stdout: "piped\n"},
	}
	f := vshtest.New(t, vshtest.Options{Commands: commands, Files: map[string]string{"d/.keep": ""}})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, exit := run(t, f, workspaceapi.Cmd{Path: tc.argv[0], Args: tc.argv[1:], Stdin: strings.NewReader(tc.stdin)})
			assert.Equal(t, tc.stdout, stdout, "stdout")
			assert.Equal(t, tc.stderr, stderr, "stderr")
			assert.Equal(t, tc.code, exitCode(t, exit), "exit code")
			if tc.msg != "" {
				assert.EqualError(t, exit, tc.msg)
			}
		})
	}
}

func TestFileTestOperators(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, script, stdout string
	}{
		{name: "ownership", script: `[ -O f ] && [ -G f ] && [ -O d ] && echo own`, stdout: "own\n"},
		{name: "a missing file is nobody's", script: `[ -O missing ] || [ -G missing ] || echo none`, stdout: "none\n"},
		{name: "devices", script: `for p in /dev/null /dev/stdin /dev/stdout /dev/stderr /dev/tty; do [ -c $p ] && [ -r $p ] && [ -w $p ] && [ ! -x $p ] && [ ! -f $p ] && printf c; done; echo`,
			stdout: "ccccc\n"},
		{name: "permissions", script: `[ -x f ] || echo f; [ -x bin/sh ] && echo sh; [ -x d ] && echo d; [ -r d ] && [ -w d ] && echo rw`,
			stdout: "f\nsh\nd\nrw\n"},
		{name: "sizes", script: `[ -s f ] && echo f; [ -s empty ] || echo empty`, stdout: "f\nempty\n"},
		{name: "relative paths after cd", script: `cd d; [ -f ../f ] && [ -d . ] && [ -e ../bin/ls ] && echo rel`, stdout: "rel\n"},
	}
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Backend: backend, Files: map[string]string{"f": "x", "empty": "", "d/.keep": ""}})
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					stdout, stderr, exit := run(t, f, workspaceapi.Cmd{Path: "sh", Args: []string{"-c", tc.script}, Dir: f.Home})
					assert.NoError(t, exit, stderr)
					assert.Equal(t, tc.stdout, stdout)
				})
			}
		})
	}
}
