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
	"os"
	"syscall"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// LoggingScheme wraps a SchemeFunc with a constructor
// that wraps the underlying scheme with a scheme that
// logs every method call.
func LoggingScheme(scheme string, fn schemeapi.SchemeFunc) schemeapi.SchemeFunc {
	return func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (
		schemeapi.Scheme, error,
	) {
		other, err := fn(ctx, cfg, uri)
		if err != nil {
			log.Errorf("SchemeFunc error: %s", err)
			return nil, err
		}
		return loggingScheme{
			scheme: scheme,
			uri:    uri,
			other:  other,
		}, nil
	}
}

type loggingScheme struct {
	scheme string
	uri    workspaceapi.URI
	other  schemeapi.Scheme
}

func (t loggingScheme) trace(msg string, args ...any) {
	log.
		WithField(logging.KeyClass, "LoggingScheme").
		WithField("URI", t.uri.String()).
		WithField("scheme", t.scheme).
		Tracef(msg, args...)
}

func (t loggingScheme) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (
	p workspaceapi.Pid, err error,
) {
	t.trace("StartCommand(%v)", cmd)
	p, err = t.other.StartCommand(ctx, cmd)
	t.trace("StartCommand(%v): %d, %v", cmd, p, err)
	return
}

func (t loggingScheme) Signal(p workspaceapi.Pid, s syscall.Signal) (err error) {
	t.trace("Signal(%d, %d)", p, s)
	err = t.other.Signal(p, s)
	t.trace("Signal(%d, %d): %v", p, s, err)
	return
}

func (t loggingScheme) Chroot(path string) (schemeapi.Scheme, error) {
	t.trace("Chroot(%s)", path)
	fs, err := t.other.Chroot(path)
	t.trace("Chroot(%s): err: %v", path, err)
	return fs, err
}

func (t loggingScheme) Root() string {
	t.trace("Root()")
	root := t.other.Root()
	t.trace("Root(): %s", root)
	return root
}

func (t loggingScheme) Symlink(target, link string) error {
	t.trace("Symlink(%s, %s)", target, link)
	err := t.other.Symlink(target, link)
	t.trace("Symlink(%s, %s): %v", target, link, err)
	return err
}

func (t loggingScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	t.trace("TempFile(%s, %s)", dir, prefix)
	fs, err := t.other.TempFile(dir, prefix)
	t.trace("TempFile(%s, %s): %v", dir, prefix, err)
	return fs, err
}

func (t loggingScheme) Join(elem ...string) string {
	t.trace("Join(%v)", elem)
	ret := t.other.Join(elem...)
	t.trace("Join(%v)", elem)
	return ret
}

func (t loggingScheme) Create(filename string) (workspaceapi.File, error) {
	t.trace("Create(%q)", filename)
	ret, err := t.other.Create(filename)
	t.trace("Create(%q): %v", filename, err)
	return ret, err
}

func (t loggingScheme) Open(filename string) (workspaceapi.File, error) {
	t.trace("Open(%q)", filename)
	ret, err := t.other.Open(filename)
	t.trace("Open(%q): %v", filename, err)
	return ret, err
}

func (t loggingScheme) URI(path string) (ret workspaceapi.URI, err error) {
	t.trace("URI(%q)", path)
	ret, err = t.other.URI(path)
	t.trace("URI(%q): %q %v", path, ret.String(), err)
	return
}

func (t loggingScheme) OpenFile(path string, flag int, perm os.FileMode) (
	ret workspaceapi.File, err error,
) {
	t.trace("OpenFile(%q, %d, %d)", path, flag, perm)
	ret, err = t.other.OpenFile(path, flag, perm)
	t.trace("OpenFile(%q, %d, %d): %#v, %#v", path, flag, perm, ret, err)
	return
}

func (t loggingScheme) NewFile(fd uintptr, filename string) (
	ret workspaceapi.File,
) {
	t.trace("NewFile(%d, %s)", fd, filename)
	ret = t.other.NewFile(fd, filename)
	t.trace("NewFile(%d, %s, %v)", fd, filename, ret)
	return
}

func (t loggingScheme) Remove(path string) (err error) {
	t.trace("Remove(%q)", path)
	err = t.other.Remove(path)
	t.trace("Remove(%q): %v", path, err)
	return
}

func (t loggingScheme) Rename(old, new string) (err error) {
	t.trace("Rename(%q, %q)", old, new)
	err = t.other.Rename(old, new)
	t.trace("Rename(%q, %q): %v", old, new, err)
	return
}

func (t loggingScheme) Stat(path string) (ret os.FileInfo, err error) {
	t.trace("Stat(%q)", path)
	ret, err = t.other.Stat(path)
	t.trace("Stat(%q): %#v, %v", path, ret, err)
	return
}

func (t loggingScheme) Lstat(path string) (ret os.FileInfo, err error) {
	t.trace("Lstat(%q)", path)
	ret, err = t.other.Lstat(path)
	t.trace("Lstat(%q): %#v, %v", path, ret, err)
	return
}

func (t loggingScheme) Readlink(path string) (ret string, err error) {
	t.trace("ReadLink(%q)", path)
	ret, err = t.other.Readlink(path)
	t.trace("ReadLink(%q): %q, %v", path, ret, err)
	return
}
func (t loggingScheme) NewPty(ctx context.Context) (ret workspaceapi.Pty, err error) {
	t.trace("NewPty()")
	ret, err = t.other.NewPty(ctx)
	t.trace("NewPty(): %q, %v", ret, err)
	return
}

func (t loggingScheme) SetPtySize(p workspaceapi.Pty, size workspaceapi.PtySize) (err error) {
	t.trace("SetPtySize(%v, %+v)", p, size)
	err = t.other.SetPtySize(p, size)
	t.trace("SetPtySize(%v, %+v): %v", p, size, err)
	return
}

func (t loggingScheme) ReadDir(name string) (
	ret []os.DirEntry, err error,
) {
	t.trace("ReadDir(%s)", name)
	ret, err = t.other.ReadDir(name)
	t.trace("ReadDir(%s): %v, %v", name, ret, err)
	return
}

func (t loggingScheme) MkdirAll(path string, perm os.FileMode) error {
	t.trace("MkdirAll(%s, %v)", path, perm)
	err := t.other.MkdirAll(path, perm)
	t.trace("MkdirAll(%s, %v): %v", path, perm, err)
	return err
}

func (t loggingScheme) Watch(
	path string, c chan<- schemeapi.EventInfo, events ...schemeapi.Event,
) (int, error) {
	t.trace("Watch(%s, %v)", path, events)
	id, err := t.other.Watch(path, c, events...)
	t.trace("Watch(%s, %v): %v", path, events, err)
	return id, err
}

func (t loggingScheme) StopWatch(id int) error {
	t.trace("StopWatch(%d)", id)
	err := t.other.StopWatch(id)
	t.trace("StopWatch(%d): %v", id, err)
	return err
}

func (t loggingScheme) Close() (err error) {
	t.trace("Close")
	err = t.other.Close()
	t.trace("Close: %q", err)
	return
}
