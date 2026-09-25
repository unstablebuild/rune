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

// NewNopScheme returns a scheme that does nothing and workspace.Executor API panics.
func NewNopScheme(scheme string) schemeapi.SchemeFunc {
	return func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (schemeapi.Scheme, error) {
		scheme := &NopScheme{scheme: scheme}
		scheme.OpenFunc = func(name string, flag int, perm os.FileMode) (
			workspaceapi.File, error,
		) {
			return nopFile{}, nil
		}
		scheme.RemoveFunc = func(name string) error {
			return nil
		}
		scheme.RenameFunc = func(oldName, newName string) error {
			return nil
		}
		scheme.StatFunc = func(name string) (os.FileInfo, error) {
			// best effort
			return FileInfo{FileIsDir: !strings.Contains(name, ".")}, nil
		}
		scheme.LstatFunc = func(name string) (os.FileInfo, error) {
			return FileInfo{}, nil
		}
		scheme.NewPtyFunc = func(ctx context.Context) (workspaceapi.Pty, error) {
			return workspaceapi.Pty{
				Master: nopFile{},
				Slave:  nopFile{},
			}, nil
		}
		scheme.StartCommandFunc = func(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error) {
			return 0, nil
		}
		return scheme, nil
	}
}

// NopScheme is a scheme for testing.
type NopScheme struct {
	scheme           string
	OpenFunc         func(name string, flag int, perm os.FileMode) (workspaceapi.File, error)
	RemoveFunc       func(name string) error
	RenameFunc       func(oldName, newName string) error
	StatFunc         func(name string) (os.FileInfo, error)
	LstatFunc        func(name string) (os.FileInfo, error)
	NewPtyFunc       func(context.Context) (workspaceapi.Pty, error)
	StartCommandFunc func(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error)
}

// Command satisfies schemeapi.Scheme
func (t *NopScheme) Command(ctx context.Context, name string, arg ...string) (workspaceapi.Pid, error) {
	return t.StartCommandFunc(ctx, workspaceapi.Cmd{Path: name, Args: arg})
}

// StartCommand satisfies schemeapi.Scheme
func (t *NopScheme) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (
	workspaceapi.Pid, error,
) {
	return t.StartCommandFunc(ctx, cmd)
}

// Signal satisfies schemeapi.Scheme
func (t *NopScheme) Signal(workspaceapi.Pid, syscall.Signal) error {
	return nil
}

// InstallDataDir satisfies workspace.InstallDataDirProvider with the "host
// cannot say" default, so Workspace fakes embedding NopScheme keep
// exercising callers' fallback paths.
func (t *NopScheme) InstallDataDir(context.Context) (string, error) {
	return "", errors.ErrUnsupported
}

// NewFile satisfies schemeapi.Scheme
func (t *NopScheme) NewFile(fd uintptr, name string) workspaceapi.File {
	return nopFile{}
}

// URI satisfies schemeapi.Scheme
func (t *NopScheme) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI(fmt.Sprintf("%s://%s", t.scheme, filepath.Join("/", path)))
}

// OpenFile satisfies schemeapi.Scheme
func (t *NopScheme) OpenFile(path string, flag int, perm os.FileMode) (workspaceapi.File, error) {
	return t.OpenFunc(path, flag, perm)
}

// Create satisfies schemeapi.Scheme
func (t *NopScheme) Create(filename string) (workspaceapi.File, error) {
	return t.OpenFunc(filename, os.O_CREATE|os.O_EXCL, 0)
}

// Open satisfies schemeapi.Scheme
func (t *NopScheme) Open(filename string) (workspaceapi.File, error) {
	return t.OpenFunc(filename, os.O_RDONLY, 0)
}

// Chroot satisfies schemeapi.Scheme
func (t *NopScheme) Chroot(path string) (schemeapi.Scheme, error) {
	return new(NopScheme), nil
}

// Root satisfies schemeapi.Scheme
func (t *NopScheme) Root() string {
	return ""
}

// Symlink satisfies schemeapi.Scheme
func (t *NopScheme) Symlink(target, link string) error {
	panic("unimplemented")
}

// TempFile satisfies schemeapi.Scheme
func (t *NopScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	panic("unimplemented")
}

// Join satisfies schemeapi.Scheme
func (t *NopScheme) Join(elem ...string) string {
	return filepath.Join(elem...)
}

// Remove satisfies schemeapi.Scheme
func (t *NopScheme) Remove(path string) error {
	return t.RemoveFunc(path)
}

// Rename satisfies schemeapi.Scheme
func (t *NopScheme) Rename(old, new string) error {
	return t.RenameFunc(old, new)
}

// Stat satisfies schemeapi.Scheme
func (t *NopScheme) Stat(path string) (os.FileInfo, error) {
	return t.StatFunc(path)
}

// NewPty satisfies schemeapi.Scheme
func (t *NopScheme) NewPty(ctx context.Context) (ret workspaceapi.Pty, err error) {
	return t.NewPtyFunc(ctx)
}

// SetPtySize satisfies schemeapi.Scheme
func (t *NopScheme) SetPtySize(workspaceapi.Pty, workspaceapi.PtySize) (err error) {
	return nil
}

// Lstat satisfies schemeapi.Scheme
func (t *NopScheme) Lstat(path string) (os.FileInfo, error) {
	return t.LstatFunc(path)
}

// Readlink satisfies schemeapi.Scheme
func (t *NopScheme) Readlink(path string) (string, error) {
	return path, nil
}

// ReadDir satisfies schemeapi.Scheme
func (t *NopScheme) ReadDir(string) (
	[]os.DirEntry, error,
) {
	panic("unimplemented")
}

// MkdirAll satisfies schemeapi.Scheme
func (t *NopScheme) MkdirAll(path string, perm os.FileMode) error {
	return nil
}

// Watch satisfies schemeapi.Scheme
func (t *NopScheme) Watch(
	path string, c chan<- schemeapi.EventInfo, events ...schemeapi.Event,
) (int, error) {
	panic("unimplemented")
}

// StopWatch satisfies schemeapi.Scheme
func (t *NopScheme) StopWatch(ID int) error {
	panic("unimplemented")
}

// Close satisfies schemeapi.Scheme
func (t *NopScheme) Close() error {
	return nil
}

// NewFile returns a workspaceapi.File that does nothing.
func NewFile() workspaceapi.File {
	return nopFile{}
}

type nopFile struct {
}

func (t nopFile) Name() string {
	return ""
}

func (t nopFile) Stat() (os.FileInfo, error) {
	return nopFileInfo{}, nil
}

func (t nopFile) ReadAt(b []byte, off int64) (int, error) {
	panic("unimplemented")
}

func (t nopFile) Sync() error {
	return nil
}

func (t nopFile) Fd() uintptr {
	return 0
}

func (t nopFile) Truncate(size int64) error {
	return nil
}

func (t nopFile) Seek(x int64, y int) (int64, error) {
	return 0, nil
}

func (t nopFile) Read(b []byte) (int, error) {
	return 0, io.EOF
}

func (t nopFile) Write(b []byte) (int, error) {
	return 0, nil
}

func (t nopFile) Close() error {
	return nil
}

// implements os.FileInfo
type nopFileInfo struct {
	name    string
	isDir   bool
	modTime time.Time
	size    int64
	mode    os.FileMode
}

func (t nopFileInfo) Name() string {
	return t.name
}
func (t nopFileInfo) Size() int64 {
	return t.size
}

func (t nopFileInfo) Mode() os.FileMode {
	return t.mode
}

func (t nopFileInfo) ModTime() time.Time {
	return t.modTime
}

func (t nopFileInfo) IsDir() bool {
	return t.isDir
}

func (t nopFileInfo) Sys() any {
	return nil
}
