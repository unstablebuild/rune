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

package coreutils

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/term/vsh"
)

// errMessage renders a filesystem error the way GNU utilities do.
func errMessage(err error) string {
	if pe, ok := errors.AsType[*os.PathError](err); ok {
		err = pe.Err
	}
	switch {
	case errors.Is(err, syscall.EISDIR):
		return "Is a directory"
	case errors.Is(err, syscall.ENOTDIR):
		return "Not a directory"
	case errors.Is(err, syscall.ENOTEMPTY):
		return "Directory not empty"
	case errors.Is(err, fs.ErrNotExist):
		return "No such file or directory"
	case errors.Is(err, fs.ErrExist):
		return "File exists"
	case errors.Is(err, fs.ErrPermission):
		return "Permission denied"
	}
	return err.Error()
}

// fail reports "<name>: <what>: <error>" and remembers a failure.
func fail(inv *vsh.Invocation, what string, err error, failed *bool) {
	*failed = true
	if what == "" {
		fmt.Fprintf(inv.Stderr, "%s: %s\n", inv.Name, errMessage(err))
		return
	}
	fmt.Fprintf(inv.Stderr, "%s: %s: %s\n", inv.Name, what, errMessage(err))
}

// status is the exit for a command that reported failures.
func status(failed bool) error {
	if failed {
		return &vsh.ExitError{Code: 1}
	}
	return nil
}

// sortedDir lists a directory sorted by name.
func sortedDir(fsys schemeapi.FileSystem, dir string) ([]fs.DirEntry, error) {
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b fs.DirEntry) int {
		return strings.Compare(a.Name(), b.Name())
	})
	return entries, nil
}

// walk visits p and, when it is a directory, everything beneath it in
// name order, parents before children. fn receives absolute paths.
func walk(ctx context.Context, fsys schemeapi.FileSystem, p string, fn func(p string, info fs.FileInfo) error) error {
	info, err := fsys.Stat(p)
	if err != nil {
		return err
	}
	if err := fn(p, info); err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	entries, err := sortedDir(fsys, p)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := walk(ctx, fsys, path.Join(p, e.Name()), fn); err != nil {
			return err
		}
	}
	return nil
}

// fileReader reads a workspace file through ReadAt with its own
// offset, since handles of an in-memory file share one.
type fileReader struct {
	f   workspaceapi.File
	off int64
}

func (r *fileReader) Read(p []byte) (int, error) {
	n, err := r.f.ReadAt(p, r.off)
	r.off += int64(n)
	if n > 0 && errors.Is(err, io.EOF) {
		return n, nil
	}
	return n, err
}

func (r *fileReader) Close() error { return r.f.Close() }

// openRead opens the file at p for reading; "-" is the invocation's
// standard input.
func openRead(inv *vsh.Invocation, p string) (io.ReadCloser, error) {
	if p == "-" {
		return io.NopCloser(inv.Stdin), nil
	}
	return inv.Open(p, os.O_RDONLY, 0)
}

// readFile returns the contents of the file at absolute path p.
func readFile(fsys schemeapi.FileSystem, p string) ([]byte, error) {
	f, err := fsys.OpenFile(p, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(&fileReader{f: f})
}

// writeFile writes data to the file at p, creating or truncating it,
// or appending with appendTo.
func writeFile(inv *vsh.Invocation, p string, data []byte, appendTo bool, perm os.FileMode) error {
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if appendTo {
		flag = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	f, err := inv.Open(p, flag, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// checkParent reports why a file cannot be created at absolute path
// p: its directory is missing or is not a directory. Some schemes
// create missing parents on their own, which utilities must not.
func checkParent(fsys schemeapi.FileSystem, p string) error {
	info, err := fsys.Stat(path.Dir(p))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return syscall.ENOTDIR
	}
	return nil
}

// isExecutable reports whether info is an executable regular file.
func isExecutable(info fs.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
