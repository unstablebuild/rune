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

package coreutils_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/coreutils"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

func TestErrMessage(t *testing.T) {
	t.Parallel()
	pathErr := func(err error) error { return &os.PathError{Op: "open", Path: "/x", Err: err} }
	tests := []struct {
		err  error
		want string
	}{
		{syscall.EISDIR, "Is a directory"},
		{pathErr(syscall.EISDIR), "Is a directory"},
		{syscall.ENOTDIR, "Not a directory"},
		{pathErr(syscall.ENOTDIR), "Not a directory"},
		{syscall.ENOTEMPTY, "Directory not empty"},
		{fmt.Errorf("remove: %w", syscall.ENOTEMPTY), "Directory not empty"},
		{fs.ErrNotExist, "No such file or directory"},
		{syscall.ENOENT, "No such file or directory"},
		{pathErr(fs.ErrNotExist), "No such file or directory"},
		{fmt.Errorf("stat: %w", fs.ErrNotExist), "No such file or directory"},
		{fs.ErrExist, "File exists"},
		{syscall.EEXIST, "File exists"},
		{pathErr(fs.ErrExist), "File exists"},
		{fs.ErrPermission, "Permission denied"},
		{syscall.EACCES, "Permission denied"},
		{syscall.EPERM, "Permission denied"},
		{pathErr(fs.ErrPermission), "Permission denied"},
		{errors.New("boom"), "boom"},
		{pathErr(errors.New("boom")), "boom"},
		{context.Canceled, "context canceled"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, coreutils.ErrMessage(tc.err), "%#v", tc.err)
	}
}

func TestSortedDir(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: map[string]string{
		"s/b": "", "s/a": "", "s/B": "", "s/_x": "", "s/.h": "", "s/10": "", "s/9": "", "s/é": "", "s/d/x": "",
		"one/only": "",
	}})
	tests := []struct {
		dir  string
		want []string
		err  error
	}{
		{dir: "/s", want: []string{".h", "10", "9", "B", "_x", "a", "b", "d", "é"}},
		{dir: "/one", want: []string{"only"}},
		{dir: "/s/d", want: []string{"x"}},
		{dir: "/nope", err: fs.ErrNotExist},
	}
	for _, tc := range tests {
		entries, err := coreutils.SortedDir(f.Scheme, tc.dir)
		assert.ErrorIs(t, err, tc.err, tc.dir)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		assert.Equal(t, tc.want, names, tc.dir)
	}
}

// errStop stops a walk at the path it names.
type errStop string

func (e errStop) Error() string { return "stop at " + string(e) }

func TestWalk(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: files})
	tests := []struct {
		name string
		root string
		// stop is the path at which fn fails, cancel whether the
		// context is done before the walk.
		stop    string
		cancel  bool
		visited []string
		err     error
	}{
		{name: "tree", root: "/dir", visited: []string{"/dir", "/dir/.hidden", "/dir/c.txt", "/dir/sub", "/dir/sub/d.txt"}},
		{name: "deep", root: "/deep", visited: []string{"/deep", "/deep/x", "/deep/x/y", "/deep/x/y/z.txt"}},
		{name: "file", root: "/a.txt", visited: []string{"/a.txt"}},
		{name: "missing", root: "/nope", err: fs.ErrNotExist},
		{name: "stop at the root", root: "/dir", stop: "/dir", visited: []string{"/dir"}, err: errStop("/dir")},
		{name: "stop in a subtree", root: "/dir", stop: "/dir/sub",
			visited: []string{"/dir", "/dir/.hidden", "/dir/c.txt", "/dir/sub"}, err: errStop("/dir/sub")},
		{name: "stop at a leaf", root: "/dir", stop: "/dir/.hidden", visited: []string{"/dir", "/dir/.hidden"}, err: errStop("/dir/.hidden")},
		{name: "cancelled", root: "/dir", cancel: true, visited: []string{"/dir"}, err: context.Canceled},
		{name: "cancelled on a file", root: "/a.txt", cancel: true, visited: []string{"/a.txt"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			var visited []string
			err := coreutils.Walk(ctx, f.Scheme, tc.root, func(p string, info fs.FileInfo) error {
				visited = append(visited, p)
				assert.Equal(t, p[strings.LastIndexByte(p, '/')+1:], info.Name(), "info of %s", p)
				if p == tc.stop {
					return errStop(p)
				}
				return nil
			})
			assert.ErrorIs(t, err, tc.err)
			assert.Equal(t, tc.visited, visited)
		})
	}
}

func TestFileReader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		content string
		bufSize int
	}{
		{name: "empty", content: "", bufSize: 4},
		{name: "one read", content: "hello", bufSize: 64},
		{name: "exact buffer", content: "hello", bufSize: 5},
		{name: "short reads", content: "hello world", bufSize: 3},
		{name: "byte at a time", content: "abc\n", bufSize: 1},
		{name: "large", content: strings.Repeat("0123456789", 10000), bufSize: 4096},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Files: map[string]string{"f": tc.content}})
			file, err := f.Scheme.Open("/f")
			require.NoError(t, err)
			r := coreutils.NewFileReader(file)
			defer r.Close()
			var got []byte
			buf := make([]byte, tc.bufSize)
			for range len(tc.content) + 2 {
				n, err := r.Read(buf)
				got = append(got, buf[:n]...)
				if err != nil {
					assert.ErrorIs(t, err, io.EOF)
					assert.Zero(t, n, "data comes without EOF")
					break
				}
				assert.Positive(t, n, "a read without error makes progress")
			}
			assert.Equal(t, tc.content, string(got))
			n, err := r.Read(buf)
			assert.Zero(t, n)
			assert.ErrorIs(t, err, io.EOF, "EOF again after the end")
		})
	}
}

func TestFileReadersHaveTheirOwnOffsets(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: map[string]string{"f": "abcdef"}})
	var readers []io.ReadCloser
	for range 2 {
		file, err := f.Scheme.Open("/f")
		require.NoError(t, err)
		r := coreutils.NewFileReader(file)
		defer r.Close()
		readers = append(readers, r)
	}
	buf := make([]byte, 2)
	for _, want := range []string{"ab", "ab", "cd", "cd", "ef", "ef"} {
		r := readers[0]
		readers[0], readers[1] = readers[1], readers[0]
		n, err := r.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, want, string(buf[:n]))
	}
}

func TestReadWriteFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		seed     map[string]string
		path     string
		data     string
		appendTo bool
		want     string
		err      error
	}{
		{name: "create", path: "/new", data: "x\n", want: "x\n"},
		{name: "create empty", path: "/new", data: "", want: ""},
		{name: "truncate", seed: map[string]string{"f": "old content"}, path: "/f", data: "new", want: "new"},
		{name: "truncate to empty", seed: map[string]string{"f": "old"}, path: "/f", data: "", want: ""},
		{name: "append", seed: map[string]string{"f": "old\n"}, path: "/f", data: "new\n", appendTo: true, want: "old\nnew\n"},
		{name: "append creates", path: "/f", data: "x", appendTo: true, want: "x"},
		{name: "nested", seed: map[string]string{"d/keep": ""}, path: "/d/f", data: "x", want: "x"},
		{name: "binary", path: "/b", data: "\x00\x01\xff", want: "\x00\x01\xff"},
		{name: "large", path: "/l", data: strings.Repeat("y", 1<<20), want: strings.Repeat("y", 1<<20)},
		{name: "missing dir", path: "/no/f", data: "x", err: fs.ErrNotExist},
		{name: "through a file", seed: map[string]string{"f": ""}, path: "/f/g", data: "x", err: syscall.ENOTDIR},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Files: tc.seed})
			inv := &vsh.Invocation{Dir: "/", FS: f.Scheme}
			err := coreutils.WriteFile(inv, tc.path, []byte(tc.data), tc.appendTo, 0o644)
			assert.ErrorIs(t, err, tc.err)
			got, rerr := coreutils.ReadFile(f.Scheme, tc.path)
			assert.Equal(t, tc.err == nil, rerr == nil, "read back: %v", rerr)
			assert.Equal(t, tc.want, string(got))
		})
	}
}

func TestReadFileErrors(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: files})
	tests := []struct {
		path string
		want string
		err  error
	}{
		{path: "/a.txt", want: files["a.txt"]},
		{path: "/empty", want: ""},
		{path: "/nope", err: fs.ErrNotExist},
	}
	for _, tc := range tests {
		got, err := coreutils.ReadFile(f.Scheme, tc.path)
		assert.ErrorIs(t, err, tc.err, tc.path)
		assert.Equal(t, tc.want, string(got), tc.path)
	}
}

func TestOpenRead(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: files})
	tests := []struct {
		dir, name string
		want      string
		err       error
	}{
		{dir: "/", name: "-", want: "stdin"},
		{dir: "/dir", name: "-", want: "stdin"},
		{dir: "/", name: "a.txt", want: files["a.txt"]},
		{dir: "/dir", name: "c.txt", want: files["dir/c.txt"]},
		{dir: "/dir", name: "../nonl", want: files["nonl"]},
		{dir: "/dir", name: "/a.txt", want: files["a.txt"]},
		{dir: "/", name: "./dir/sub/d.txt", want: files["dir/sub/d.txt"]},
		{dir: "/", name: "empty", want: ""},
		{dir: "/", name: "dir", err: syscall.EISDIR},
		{dir: "/", name: ".", err: syscall.EISDIR},
		{dir: "/", name: "nope", err: fs.ErrNotExist},
		{dir: "/", name: "./-", err: fs.ErrNotExist},
		{dir: "/dir", name: "/dev/stdin", want: "stdin"},
		{dir: "/dev", name: "stdin", want: "stdin"},
		{dir: "/", name: "/dev/null", want: ""},
		{dir: "/", name: "/dev/tty", err: syscall.ENXIO},
	}
	for _, tc := range tests {
		inv := &vsh.Invocation{Dir: tc.dir, FS: f.Scheme, Stdin: strings.NewReader("stdin")}
		r, err := coreutils.OpenRead(inv, tc.name)
		assert.ErrorIs(t, err, tc.err, "%s in %s", tc.name, tc.dir)
		if err != nil {
			continue
		}
		got, err := io.ReadAll(r)
		assert.NoError(t, err)
		assert.NoError(t, r.Close())
		assert.Equal(t, tc.want, string(got), "%s in %s", tc.name, tc.dir)
	}
}

func TestCheckParent(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: files})
	tests := []struct {
		path string
		err  error
	}{
		{path: "/new"},
		{path: "/a.txt"},
		{path: "/dir/new"},
		{path: "/dir/sub/new"},
		{path: "/no/new", err: fs.ErrNotExist},
		{path: "/a.txt/new", err: syscall.ENOTDIR},
		{path: "/no/deeper/new", err: fs.ErrNotExist},
	}
	for _, tc := range tests {
		assert.ErrorIs(t, coreutils.CheckParent(f.Scheme, tc.path), tc.err, tc.path)
	}
}

type fileInfo struct {
	name string
	mode fs.FileMode
}

func (i fileInfo) Name() string       { return i.name }
func (i fileInfo) Size() int64        { return 0 }
func (i fileInfo) Mode() fs.FileMode  { return i.mode }
func (i fileInfo) ModTime() time.Time { return time.Time{} }
func (i fileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i fileInfo) Sys() any           { return nil }

func TestIsExecutable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		mode fs.FileMode
		want bool
	}{
		{0o755, true},
		{0o700, true},
		{0o100, true},
		{0o010, true},
		{0o001, true},
		{0o644, false},
		{0o666, false},
		{0, false},
		{fs.ModeDir | 0o755, false},
		{fs.ModeSymlink | 0o777, false},
		{fs.ModeNamedPipe | 0o755, false},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, coreutils.IsExecutable(fileInfo{name: "f", mode: tc.mode}), "%v", tc.mode)
	}
}

func TestIsExecutableOnTheBin(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: files})
	tests := []struct {
		path string
		want bool
	}{
		{path: "/bin/ls", want: true},
		{path: "/bin/sh", want: true},
		{path: "/a.txt", want: false},
		{path: "/bin", want: false},
	}
	for _, tc := range tests {
		info, err := f.Scheme.Stat(tc.path)
		require.NoError(t, err)
		assert.Equal(t, tc.want, coreutils.IsExecutable(info), tc.path)
	}
}
