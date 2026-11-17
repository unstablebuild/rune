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

// Package vshtest is a hermetic fixture for tests that drive the Rune
// shell: a workspace with a vsh.Executor on it, so that a terminal
// emulator or a command caller runs against a deterministic,
// in-process shell with no host process, HOME or environment involved.
// The workspace is in memory by default, or a temporary host directory
// for suites proving the shell holds on any filesystem.
package vshtest

import (
	"context"
	"path"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/coreutils"
	"unstable.build/rune/internal/workspace"
)

// Options configure a Fixture.
type Options struct {
	// Backend is the filesystem the fixture runs on; the zero value
	// is Memory.
	Backend Backend
	// Files are seeded into the filesystem, keyed by path relative to
	// Home. Parent directories are created.
	Files map[string]string
	// Env is appended to the base environment, which sets PS1 to "$ ".
	Env []string
	// Commands replaces the coreutils when non-nil.
	Commands []vsh.Command
	// Banner is the interactive session's banner; none by default.
	Banner string
}

// Backend is a kind of filesystem a Fixture runs on.
type Backend struct {
	// Name identifies the backend, as in subtest names.
	Name string
	// open returns a new scheme and the absolute directory in it that
	// the fixture owns.
	open func(t testing.TB) (schemeapi.Scheme, string)
}

var (
	// Memory is a memory:/// workspace, whose root the fixture owns.
	Memory = Backend{Name: "memory", open: openMemory}
	// File is a file:// workspace on a temporary host directory.
	File = Backend{Name: "file", open: openFile}
	// Backends lists every Backend, for suites that must hold on all.
	Backends = []Backend{Memory, File}
)

func openMemory(t testing.TB) (schemeapi.Scheme, string) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	return scheme, "/"
}

func openFile(t testing.TB) (schemeapi.Scheme, string) {
	t.Helper()
	// The shell prints the paths it is given, so resolve the links
	// some hosts put in the temporary directory's path.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	uri, err := workspaceapi.ParseURI("file://" + filepath.ToSlash(dir))
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	return scheme, filepath.ToSlash(dir)
}

// Fixture is a workspace and the Executor serving it.
type Fixture struct {
	Scheme schemeapi.Scheme
	Exec   *vsh.Executor
	// Home is the absolute directory the shell starts in, HOME.
	Home string
	// Bin is the absolute directory holding the shell's programs:
	// the bin directory in Home.
	Bin string
}

// New builds a fixture and closes it with the test.
func New(t testing.TB, opts Options) *Fixture {
	t.Helper()
	backend := opts.Backend
	if backend.open == nil {
		backend = Memory
	}
	scheme, home := backend.open(t)
	t.Cleanup(func() { _ = scheme.Close() })
	f := &Fixture{Scheme: scheme, Home: home, Bin: path.Join(home, "bin")}

	for name, content := range opts.Files {
		f.Seed(t, name, content)
	}
	commands := opts.Commands
	if commands == nil {
		commands = coreutils.Commands()
	}
	exec, err := vsh.NewExecutor(vsh.Config{
		Scheme:   backend.Name,
		Root:     f.Home,
		Bin:      f.Bin,
		FS:       scheme,
		Env:      append([]string{"PS1=$ "}, opts.Env...),
		Banner:   opts.Banner,
		Commands: commands,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = exec.Close() })
	f.Exec = exec
	return f
}

// Path resolves name against Home.
func (f *Fixture) Path(name string) string {
	if path.IsAbs(name) {
		return path.Clean(name)
	}
	return path.Join(f.Home, name)
}

// Seed writes content to the file at name, relative to Home, creating
// its directories.
func (f *Fixture) Seed(t testing.TB, name, content string) {
	t.Helper()
	name = f.Path(name)
	require.NoError(t, f.Scheme.MkdirAll(path.Dir(name), 0o755))
	file, err := f.Scheme.Create(name)
	require.NoError(t, err)
	_, err = file.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, file.Sync())
	require.NoError(t, file.Close())
}

// ReadFile returns the contents of the file at name, relative to Home.
func (f *Fixture) ReadFile(t testing.TB, name string) string {
	t.Helper()
	file, err := f.Scheme.Open(f.Path(name))
	require.NoError(t, err)
	defer file.Close()
	var out []byte
	buf := make([]byte, 4096)
	for off := int64(0); ; {
		n, err := file.ReadAt(buf, off)
		out = append(out, buf[:n]...)
		off += int64(n)
		if err != nil {
			break
		}
	}
	return string(out)
}
