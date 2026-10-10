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

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// fakeFileInfo satisfies os.FileInfo for paths scripted into fakeFS.
type fakeFileInfo struct {
	name string
	dir  bool
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o755 }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.dir }
func (f fakeFileInfo) Sys() any           { return nil }

// fakeDirEntry satisfies os.DirEntry for ReadDir results.
type fakeDirEntry struct {
	name string
	dir  bool
}

func (e fakeDirEntry) Name() string               { return e.name }
func (e fakeDirEntry) IsDir() bool                { return e.dir }
func (e fakeDirEntry) Type() os.FileMode          { return 0 }
func (e fakeDirEntry) Info() (os.FileInfo, error) { return fakeFileInfo{name: e.name, dir: e.dir}, nil }

// fakeFS implements workspaceapi.FileSystem for resolver tests.
type fakeFS struct {
	files   map[string]bool
	dirs    map[string]bool
	entries []os.DirEntry
}

func newFakeFS() *fakeFS {
	return &fakeFS{files: map[string]bool{}, dirs: map[string]bool{}}
}

func (f *fakeFS) addFile(p string) *fakeFS { f.files[p] = true; return f }
func (f *fakeFS) addDir(p string) *fakeFS  { f.dirs[p] = true; return f }

func (f *fakeFS) addEntry(name string, dir bool) *fakeFS {
	f.entries = append(f.entries, fakeDirEntry{name: name, dir: dir})
	return f
}

func (f *fakeFS) URI(p string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + p)
}

func (f *fakeFS) OpenFile(_ string, _ int, _ os.FileMode) (workspaceapi.File, error) {
	return nil, errors.New("not supported")
}

func (f *fakeFS) Remove(_ string) error { return errors.New("not supported") }
func (f *fakeFS) MkdirAll(_ string, _ os.FileMode) error {
	return errors.New("not supported")
}

func (f *fakeFS) Stat(name string) (os.FileInfo, error) {
	if f.files[name] {
		return fakeFileInfo{name: name, dir: false}, nil
	}
	if f.dirs[name] {
		return fakeFileInfo{name: name, dir: true}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
}

func (f *fakeFS) ReadDir(_ string) ([]os.DirEntry, error) {
	return f.entries, nil
}

// scriptedCmd records the stdout/stderr payload and exit error returned
// by the fake executor for a matching command key.
type scriptedCmd struct {
	stdout string
	stderr string
	err    error
}

// fakeExecutor implements workspaceapi.Executor. It dispatches on the
// space-joined (cmd.Path, cmd.Args...) key. Unknown commands error from
// Start.
type fakeExecutor struct {
	mu        sync.Mutex
	responses map[string]scriptedCmd
	calls     []string
	dirs      map[string]string
	nextPid   workspaceapi.Pid
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{responses: map[string]scriptedCmd{}, dirs: map[string]string{}, nextPid: 1}
}

func (e *fakeExecutor) respond(key string, r scriptedCmd) *fakeExecutor {
	e.responses[key] = r
	return e
}

func (e *fakeExecutor) callKey(cmd workspaceapi.Cmd) string {
	return strings.Join(append([]string{cmd.Path}, cmd.Args...), " ")
}

func (e *fakeExecutor) Start(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	e.mu.Lock()
	key := e.callKey(cmd)
	e.calls = append(e.calls, key)
	e.dirs[key] = cmd.Dir
	resp, ok := e.responses[key]
	pid := e.nextPid
	e.nextPid++
	e.mu.Unlock()

	if !ok {
		return 0, fmt.Errorf("fakeExecutor: no scripted response for %q", key)
	}
	if cmd.Stdout != nil && resp.stdout != "" {
		_, _ = io.Copy(cmd.Stdout, bytes.NewBufferString(resp.stdout))
	}
	if cmd.Stderr != nil && resp.stderr != "" {
		_, _ = io.Copy(cmd.Stderr, bytes.NewBufferString(resp.stderr))
	}
	if cmd.Watcher != nil {
		ch := cmd.Watcher.WatchProcess()
		go func(err error) {
			if ch != nil {
				ch <- err
			}
		}(resp.err)
	}
	return pid, nil
}

func (e *fakeExecutor) Signal(_ workspaceapi.Pid, _ syscall.Signal) error { return nil }
func (e *fakeExecutor) Close() error                                      { return nil }

func (e *fakeExecutor) callsSnapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.calls))
	copy(out, e.calls)
	return out
}

// dirFor returns the Cmd.Dir the executor saw for the command matching
// key (the space-joined path and args).
func (e *fakeExecutor) dirFor(key string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dirs[key]
}
