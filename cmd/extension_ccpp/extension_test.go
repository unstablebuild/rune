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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/langext/langexttest"
)

func TestNewExtensionMetadata(t *testing.T) {
	_, meta := newExtension()
	assert.Equal(t, "ccpp", meta.ExtensionID, "the id is the server id and the package name")
	assert.Equal(t, debug.Tag, meta.ExtensionVersion)
	assert.NotEmpty(t, meta.ExtensionName)
	for _, p := range []extensionapi.Permission{
		extensionapi.PermissionLSP,
		extensionapi.PermissionBrowserWindowManager,
		extensionapi.PermissionBrowserResourceOpener,
		extensionapi.PermissionStorage,
		extensionapi.PermissionPackages,
	} {
		assert.Contains(t, meta.Permissions, p)
	}
}

func TestIsCcppFile(t *testing.T) {
	for _, name := range []string{
		"a.c", "a.h", "a.cpp", "a.cc", "a.cxx", "a.hpp", "a.hh", "a.hxx", "a.h++",
		"a.inl", "a.ipp", "a.tpp", "A.CPP",
	} {
		uri, err := workspaceapi.ParseURI("file:///ws/" + name)
		require.NoError(t, err)
		assert.True(t, isCcppFile(uri), name)
	}
	for _, name := range []string{"a.rs", "a.zig", "a.m", "a.mm", "Makefile", "a.cs"} {
		uri, err := workspaceapi.ParseURI("file:///ws/" + name)
		require.NoError(t, err)
		assert.False(t, isCcppFile(uri), name)
	}
}

// writeFiles creates the files under root with the given contents.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
}

// packaged is an installer whose ccpp package ships a clangd.
func packaged() *langexttest.Installer {
	return &langexttest.Installer{Files: []string{"/data/ccpp/bin/clangd"}}
}

func TestExtendWorkspaceRegistersCommandsWithoutStartingClangd(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"README.md": "# no C here\n", "main.go": "package main\n"})
	env := runExtensionOnDir(t, dir, nil, packaged())

	require.Len(t, env.manuals, 1)
	assert.Equal(t, ccppCommandName, env.manuals[0].Name)
	require.Len(t, env.cmds, 2)
	assert.Equal(t, cActionCmdName, env.cmds[0].Name)
	assert.Equal(t, cppActionCmdName, env.cmds[1].Name)
	assert.NotEmpty(t, env.cmds[1].Commands)

	_, count := env.lsp.captured()
	assert.Zero(t, count, "clangd starts on the first C or C++ open, not on workspace open")

	env.editor.open(t, filepath.Join(dir, "main.go"))
	time.Sleep(50 * time.Millisecond)
	_, count = env.lsp.captured()
	assert.Zero(t, count, "a file of another language must not start clangd")
}

func TestExtendWorkspaceStartsOneClangdAtTheRootOnFirstOpen(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"lib/CMakeLists.txt":              "project(lib)\n",
		"lib/src/a.cpp":                   "int a() { return 1; }\n",
		"app/meson.build":                 "project('app', 'c')\n",
		"app/main.c":                      "int main() { return 0; }\n",
		"include/util.h":                  "#pragma once\n",
		"lib/build/compile_commands.json": "[]",
	})
	cfg := config.JSONFromMap(map[string]any{
		"args":           []any{"--query-driver=/usr/bin/g++"},
		"fallback_flags": []any{"-std=c++20"},
	})
	env := runExtensionOnDir(t, dir, cfg, packaged())

	env.editor.open(t, filepath.Join(dir, "lib", "src", "a.cpp"))
	env.lsp.waitForInit(t, 5*time.Second)
	params, count := env.lsp.captured()
	require.Equal(t, 1, count)
	assert.Equal(t, "file://"+dir, params.RootURI, "one server at the workspace root, not at the nested project")
	require.Len(t, params.WorkspaceFolders, 1)
	assert.Equal(t, params.RootURI, params.WorkspaceFolders[0].URI)
	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	assert.Equal(t, "ccpp", opts["langID"])
	assert.Equal(t, "/data/ccpp/bin/clangd --background-index --clang-tidy --header-insertion=iwyu "+
		"--log=error --query-driver=/usr/bin/g++", opts["command"])
	assert.Equal(t, []any{"-std=c++20"}, opts["fallbackFlags"])
	_, hasDB := opts["compilationDatabasePath"]
	assert.False(t, hasDB, "clangd finds compile_commands.json per file unless one is pinned")

	// Further opens, in other projects or of headers, reuse the server.
	env.editor.open(t, filepath.Join(dir, "app", "main.c"))
	env.editor.open(t, filepath.Join(dir, "include", "util.h"))
	time.Sleep(50 * time.Millisecond)
	_, count = env.lsp.captured()
	assert.Equal(t, 1, count)

	phase, opts2, err := envState(t, env).snapshot()
	require.NoError(t, err)
	assert.Equal(t, "running", phase)
	assert.Equal(t, "/data/ccpp/bin/clangd", opts2.bin)
}

func TestExtendWorkspacePinnedDatabaseReachesClangd(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"main.c":                          "int main() { return 0; }\n",
		"out/Debug/compile_commands.json": "[]",
	})
	env := runExtensionOnDir(t, dir, nil, packaged())

	// Pin a database before clangd starts; the first start picks it up.
	out, err := runCcpp(t, env.repl.(*ccppHandler), "compdb", "use", "out/Debug")
	require.NoError(t, err)
	assert.Contains(t, out, "clangd will use it when it starts")

	env.editor.open(t, filepath.Join(dir, "main.c"))
	env.lsp.waitForInit(t, 5*time.Second)
	params, _ := env.lsp.captured()
	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	assert.Equal(t, filepath.Join(dir, "out", "Debug"), opts["compilationDatabasePath"])

	// Clearing it restarts clangd without the pin.
	out, err = runCcpp(t, env.repl.(*ccppHandler), "compdb", "clear")
	require.NoError(t, err)
	assert.Contains(t, out, "clangd restarted.")
	params, count := env.lsp.captured()
	assert.Equal(t, 2, count)
	opts = nil
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	_, hasDB := opts["compilationDatabasePath"]
	assert.False(t, hasDB)
}

func TestExtendWorkspaceOutsideFileDoesNotStartClangd(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	writeFiles(t, other, map[string]string{"stray.c": "int x;\n"})
	env := runExtensionOnDir(t, dir, nil, packaged())
	env.editor.open(t, filepath.Join(other, "stray.c"))
	time.Sleep(50 * time.Millisecond)
	_, count := env.lsp.captured()
	assert.Zero(t, count, "a file outside the workspace has no workspace server to start")
}

func TestExtendWorkspaceWithoutClangdFailsTheBringUpAndReports(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"main.c": "int main() { return 0; }\n"})
	notInstalled := &langexttest.Installer{Err: errors.New("ccpp: not installed")}
	env := runExtensionOnFS(t, dir, noClangdFS{realFS{root: dir}}, nil, notInstalled, &fakePrompter{})
	env.editor.open(t, filepath.Join(dir, "main.c"))

	state := envState(t, env)
	require.Eventually(t, func() bool {
		phase, _, _ := state.snapshot()
		return phase == "failed"
	}, 5*time.Second, 10*time.Millisecond)
	_, _, err := state.snapshot()
	assert.ErrorContains(t, err, "no clangd found")
	assert.True(t, env.notify.hasMessage("could not locate the clangd executable"),
		"got %v", env.notify.notifMessages())
	_, count := env.lsp.captured()
	assert.Zero(t, count)

	out, err := runCcpp(t, env.repl.(*ccppHandler), "status")
	require.NoError(t, err)
	assert.Contains(t, out, "failed to start: no clangd found")
}

func TestServerState(t *testing.T) {
	var s serverState
	phase, _, err := s.snapshot()
	assert.Empty(t, phase)
	assert.NoError(t, err)

	s.starting()
	phase, _, _ = s.snapshot()
	assert.Equal(t, "starting", phase)

	s.failed(errors.New("boom"))
	phase, _, err = s.snapshot()
	assert.Equal(t, "failed", phase)
	assert.EqualError(t, err, "boom")

	s.running(clangdOptions{bin: "clangd"})
	phase, opts, err := s.snapshot()
	assert.Equal(t, "running", phase)
	assert.Equal(t, "clangd", opts.bin)
	assert.NoError(t, err, "a later success clears the failure")
}

// envState reaches the server state the bring-up tracks through the
// console handler, which shares it.
func envState(t *testing.T, env *ccppEnv) *serverState {
	t.Helper()
	h, ok := env.repl.(*ccppHandler)
	require.True(t, ok)
	return h.state
}
