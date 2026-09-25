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

package workspace

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

func newTestScheme(scheme string) schemeapi.SchemeFunc {
	return func(_ context.Context, cfg config.Config, uri workspaceapi.URI) (schemeapi.Scheme, error) {
		scheme := &testScheme{scheme: scheme}
		scheme.openFunc = func(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
			return testFile{}, nil
		}
		scheme.removeFunc = func(name string) error {
			return nil
		}
		scheme.renameFunc = func(oldName, newName string) error {
			return nil
		}
		scheme.statFunc = func(name string) (os.FileInfo, error) {
			// best effort
			return testFileInfo{isDir: !strings.Contains(name, ".")}, nil
		}
		scheme.lstatFunc = func(name string) (os.FileInfo, error) {
			return testFileInfo{}, nil
		}
		return scheme, nil
	}
}

type testFile struct {
}

func (t testFile) Name() string {
	return ""
}

func (t testFile) Stat() (os.FileInfo, error) {
	return testFileInfo{}, nil
}

func (t testFile) Sync() error {
	return nil
}

func (t testFile) Fd() uintptr {
	return 0
}

func (t testFile) Truncate(size int64) error {
	return nil
}

func (t testFile) Seek(x int64, y int) (int64, error) {
	return 0, nil
}

func (t testFile) Read(b []byte) (int, error) {
	return 0, io.EOF
}

func (t testFile) ReadAt(b []byte, offset int64) (int, error) {
	return 0, io.EOF
}

func (t testFile) Write(b []byte) (int, error) {
	return 0, nil
}

func (t testFile) Close() error {
	return nil
}

// implements os.FileInfo
type testFileInfo struct {
	name    string
	isDir   bool
	modTime time.Time
	size    int64
	mode    os.FileMode
}

func (t testFileInfo) Name() string {
	return t.name
}
func (t testFileInfo) Size() int64 {
	return t.size
}

func (t testFileInfo) Mode() os.FileMode {
	return t.mode
}

func (t testFileInfo) ModTime() time.Time {
	return t.modTime
}

func (t testFileInfo) IsDir() bool {
	return t.isDir
}

func (t testFileInfo) Sys() any {
	return nil
}

type testScheme struct {
	scheme     string
	openFunc   func(name string, flag int, perm os.FileMode) (workspaceapi.File, error)
	removeFunc func(name string) error
	renameFunc func(oldName, newName string) error
	statFunc   func(name string) (os.FileInfo, error)
	lstatFunc  func(name string) (os.FileInfo, error)
}

func (t *testScheme) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (
	workspaceapi.Pid, error,
) {
	panic("unimplemented")
}
func (t *testScheme) Signal(workspaceapi.Pid, syscall.Signal) error {
	panic("unimplemented")
}

func (t *testScheme) NewFile(fd uintptr, name string) workspaceapi.File {
	panic("unimplemented")
}

func (t *testScheme) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI(fmt.Sprintf("%s://%s", t.scheme, filepath.Join("/", path)))
}

func (t *testScheme) OpenFile(path string, flag int, perm os.FileMode) (
	workspaceapi.File, error,
) {
	return t.openFunc(path, flag, perm)
}

func (t *testScheme) Remove(path string) error {
	return t.removeFunc(path)
}

func (t *testScheme) Rename(old, new string) error {
	return t.renameFunc(old, new)
}

func (t *testScheme) Stat(path string) (os.FileInfo, error) {
	return t.statFunc(path)
}

func (t *testScheme) MkdirAll(path string, perm os.FileMode) error {
	panic("unimplemented")
}

func (t *testScheme) NewPty(ctx context.Context) (ret workspaceapi.Pty, err error) {
	panic("unimplemented")
}

func (t *testScheme) SetPtySize(p workspaceapi.Pty, size workspaceapi.PtySize) (err error) {
	panic("unimplemented")
}

func (t *testScheme) Lstat(path string) (os.FileInfo, error) {
	return t.lstatFunc(path)
}

func (t *testScheme) Readlink(path string) (string, error) {
	return path, nil
}

func (t *testScheme) ReadDir(string) (
	[]os.DirEntry, error,
) {
	panic("unimplemented")
}

func (t *testScheme) Watch(
	path string, c chan<- schemeapi.EventInfo, events ...schemeapi.Event,
) (int, error) {
	panic("unimplemented")
}

func (t *testScheme) StopWatch(ID int) error {
	panic("unimplemented")
}

func (t testScheme) Chroot(path string) (schemeapi.Scheme, error) {
	panic("unimplemented")
}

func (t testScheme) Root() string {
	panic("unimplemented")
}

func (t testScheme) Symlink(target, link string) error {
	panic("unimplemented")
}

func (t testScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	panic("unimplemented")
}

func (t testScheme) Join(elem ...string) string {
	panic("unimplemented")
}

func (t testScheme) Create(filename string) (workspaceapi.File, error) {
	panic("unimplemented")
}

func (t testScheme) Open(filename string) (workspaceapi.File, error) {
	panic("unimplemented")
}

func (t *testScheme) Close() error {
	return nil
}
