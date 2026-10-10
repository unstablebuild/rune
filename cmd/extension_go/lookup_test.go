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
	"path"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/pkgapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/extension/langext/langexttest"
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

// fakeFS implements workspaceapi.FileSystem for resolver tests.
// Paths that exist must be registered in `files` (regular file) or
// `dirs` (directory). Any other Stat returns os.ErrNotExist.
// Stat expands a leading "~/" against `home` before lookup, mirroring
// the real workspaceapi.ExpandPath behavior that runs inside
// fileScheme.Stat / remoteScheme.Stat.
type fakeFS struct {
	files map[string]bool
	dirs  map[string]bool
	home  string
}

func newFakeFS() *fakeFS {
	return &fakeFS{files: map[string]bool{}, dirs: map[string]bool{}}
}

func (f *fakeFS) addFile(p string) *fakeFS { f.files[p] = true; return f }
func (f *fakeFS) addDir(p string) *fakeFS  { f.dirs[p] = true; return f }
func (f *fakeFS) withHome(home string) *fakeFS {
	f.home = home
	return f
}

// expand mirrors workspaceapi.ExpandPath: a leading "~/" is replaced
// by f.home. Other paths are returned unchanged.
func (f *fakeFS) expand(p string) string {
	if f.home == "" {
		return p
	}
	if p == "~" {
		return f.home
	}
	if strings.HasPrefix(p, "~/") {
		return path.Join(f.home, strings.TrimPrefix(p, "~/"))
	}
	return p
}

func (f *fakeFS) URI(p string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + f.expand(p))
}

func (f *fakeFS) OpenFile(_ string, _ int, _ os.FileMode) (workspaceapi.File, error) {
	return nil, errors.New("not supported")
}

func (f *fakeFS) Remove(_ string) error { return errors.New("not supported") }
func (f *fakeFS) MkdirAll(_ string, _ os.FileMode) error {
	return errors.New("not supported")
}

func (f *fakeFS) Stat(name string) (os.FileInfo, error) {
	resolved := f.expand(name)
	if f.files[resolved] {
		return fakeFileInfo{name: resolved, dir: false}, nil
	}
	if f.dirs[resolved] {
		return fakeFileInfo{name: resolved, dir: true}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: name, Err: os.ErrNotExist}
}

func (f *fakeFS) ReadDir(_ string) ([]os.DirEntry, error) {
	return nil, errors.New("not supported")
}

// scriptedCmd records expected exit code, stdout payload, and a tag
// recorded into log for assertions.
type scriptedCmd struct {
	stdout string
	stderr string
	err    error
}

// fakeExecutor implements workspaceapi.Executor. It dispatches on
// (cmd.Path, cmd.Args[0..]) by joining them with spaces and looking
// up the resulting key in `responses`. Unknown commands return an
// error from Start.
type fakeExecutor struct {
	mu        sync.Mutex
	responses map[string]scriptedCmd
	calls     []string
	nextPid   workspaceapi.Pid
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		responses: map[string]scriptedCmd{},
		nextPid:   1,
	}
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

func (e *fakeExecutor) Signal(_ workspaceapi.Pid, _ syscall.Signal) error {
	return nil
}

func (e *fakeExecutor) Close() error { return nil }

const shellProbe = "sh -lc command -v gopls"

func TestResolveGoplsBinary(t *testing.T) {
	t.Run("packaged gopls wins over well-known and shell", func(t *testing.T) {
		fs := newFakeFS().addFile("/usr/local/go/bin/gopls")
		ex := newFakeExecutor()
		got, err := resolveGoplsBinary(
			context.Background(), fs, ex, "/Users/u/.rune/lib/go/bin/gopls")
		require.NoError(t, err)
		assert.Equal(t, "/Users/u/.rune/lib/go/bin/gopls", got)
		assert.Empty(t, ex.calls,
			"no executor probes should run when the package ships gopls")
	})

	t.Run("no packaged gopls falls through to well-known with tilde", func(t *testing.T) {
		// Regression for RUNE-164: tilde well-known paths must
		// resolve via fs.Stat without any prior $HOME probe.
		fs := newFakeFS().
			withHome("/Users/u").
			addFile("/Users/u/go/bin/gopls")
		ex := newFakeExecutor()
		got, err := resolveGoplsBinary(
			context.Background(), fs, ex, "")
		require.NoError(t, err)
		assert.Equal(t, "/Users/u/go/bin/gopls", got)
		assert.Empty(t, ex.calls,
			"no executor probes should run when a well-known path resolves")
	})

	t.Run("absolute well-known path resolves without tilde", func(t *testing.T) {
		fs := newFakeFS().addFile("/opt/homebrew/bin/gopls")
		ex := newFakeExecutor()
		got, err := resolveGoplsBinary(
			context.Background(), fs, ex, "")
		require.NoError(t, err)
		assert.Equal(t, "/opt/homebrew/bin/gopls", got)
		assert.Empty(t, ex.calls)
	})

	t.Run("directory at well-known location is ignored", func(t *testing.T) {
		fs := newFakeFS().addDir("/usr/local/go/bin/gopls")
		ex := newFakeExecutor().
			respond(shellProbe, scriptedCmd{err: errors.New("not found")})
		_, err := resolveGoplsBinary(
			context.Background(), fs, ex, "")
		require.Error(t, err)
	})

	t.Run("well-known miss falls through to shell probe", func(t *testing.T) {
		fs := newFakeFS()
		ex := newFakeExecutor().
			respond(shellProbe, scriptedCmd{stdout: "/opt/gopls\n"})
		got, err := resolveGoplsBinary(
			context.Background(), fs, ex, "")
		require.NoError(t, err)
		assert.Equal(t, "/opt/gopls", got)
	})

	t.Run("non-absolute shell output is rejected", func(t *testing.T) {
		fs := newFakeFS()
		ex := newFakeExecutor().
			respond(shellProbe, scriptedCmd{stdout: "gopls\n"})
		_, err := resolveGoplsBinary(
			context.Background(), fs, ex, "")
		require.Error(t, err)
	})

	t.Run("everything fails returns error", func(t *testing.T) {
		fs := newFakeFS()
		ex := newFakeExecutor().
			respond(shellProbe, scriptedCmd{err: errors.New("not found")})
		_, err := resolveGoplsBinary(
			context.Background(), fs, ex, "")
		require.Error(t, err)
	})
}

func TestResolveGoplsForRootLooksUpPackageOnlyWithoutOverride(t *testing.T) {
	const wellKnown = "/usr/local/go/bin/gopls"
	tests := []struct {
		name     string
		cfg      config.Config
		inst     *langexttest.Installer
		want     string
		lookups  int
		warnings []string
	}{
		{
			name: "lsp_path override",
			cfg:  config.JSONFromMap(map[string]any{"lsp_path": "/opt/gopls"}),
			inst: &langexttest.Installer{Files: []string{"/lib/go/bin/gopls"}},
			want: "/opt/gopls",
		},
		{
			name:    "packaged gopls",
			inst:    &langexttest.Installer{Files: []string{"/lib/go/bin/gopls"}},
			want:    "/lib/go/bin/gopls",
			lookups: 1,
		},
		{
			name:    "package not installed falls back quietly",
			inst:    &langexttest.Installer{Err: fmt.Errorf("go: %w", pkgapi.ErrNotInstalled)},
			want:    wellKnown,
			lookups: 1,
		},
		{
			name:    "package without gopls warns and falls back",
			inst:    &langexttest.Installer{Files: []string{"/lib/go/bin/go"}},
			want:    wellKnown,
			lookups: 1,
			warnings: []string{"Could not find gopls in the go package: " +
				"go/bin/gopls: langext: tool not shipped by package. " +
				"Looking for one on the host instead."},
		},
		{
			name:    "failed lookup warns and falls back",
			inst:    &langexttest.Installer{Err: errors.New("rpc error: code = PermissionDenied")},
			want:    wellKnown,
			lookups: 1,
			warnings: []string{"Could not find gopls in the go package: " +
				"rpc error: code = PermissionDenied. " +
				"Looking for one on the host instead."},
		},
		{
			name: "failed lookup uses the install root quietly",
			inst: &langexttest.Installer{
				Err:         errors.New("unknown service pkg.Packages"),
				Provisioned: map[string]string{"gopls": "/home/me/.rune/bin/gopls"},
			},
			want:    "/home/me/.rune/bin/gopls",
			lookups: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tools := langext.NewInitializer(t.Context(), nil, nil, tc.inst, langext.ProjectConfig{
				LanguageID: "go", Tools: []string{"gopls"},
			}).Tools()
			fs := newFakeFS().addFile(wellKnown)
			ex := newFakeExecutor().respond(shellProbe, scriptedCmd{err: errors.New("not found")})
			notify := &mockNotifications{}

			got := resolveGoplsForRoot(t.Context(), fs, ex, tools, tc.cfg, notify, "file")
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.lookups, tc.inst.Lookups())
			var warnings []string
			for _, m := range notify.getMessages() {
				warnings = append(warnings, m.Message)
			}
			assert.Equal(t, tc.warnings, warnings)
		})
	}
}

func TestHasGoProjectFiles(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{"empty workspace", nil, false},
		{"go.mod present", []string{"go.mod"}, true},
		{"go.sum only", []string{"go.sum"}, true},
		{"go.work only", []string{"go.work"}, true},
		{"unrelated file", []string{"README.md"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeFS()
			for _, f := range tc.files {
				fs.addFile(f)
			}
			assert.Equal(t, tc.want, hasGoProjectFiles(context.Background(), fs))
		})
	}
}

func TestReadGoplsLspPath(t *testing.T) {
	t.Run("missing key returns empty without warning", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{})
		mn := &mockNotifications{}
		got, ok := readGoplsLspPath(cfg, mn)
		assert.Equal(t, "", got)
		assert.False(t, ok)
		assert.Empty(t, mn.getMessages())
	})

	t.Run("valid path is returned verbatim", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"lsp_path": "/opt/gopls"})
		mn := &mockNotifications{}
		got, ok := readGoplsLspPath(cfg, mn)
		assert.Equal(t, "/opt/gopls", got)
		assert.True(t, ok)
		assert.Empty(t, mn.getMessages())
	})

	t.Run("wrong type is rejected with warn", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"lsp_path": 42})
		mn := &mockNotifications{}
		got, ok := readGoplsLspPath(cfg, mn)
		assert.Equal(t, "", got)
		assert.False(t, ok)
		require.Len(t, mn.getMessages(), 1)
		assert.Equal(t, browserapi.LevelWarn, mn.getMessages()[0].Level)
	})
}
