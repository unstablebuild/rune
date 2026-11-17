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
	"context"
	"io/fs"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/coreutils"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

func TestBinNames(t *testing.T) {
	t.Parallel()
	base := []string{"[", "bash", "dash", "echo", "false", "fish", "help", "ksh", "printf", "pwd", "sh", "test", "true", "which", "zsh"}
	tests := []struct {
		name     string
		commands []string
		want     []string
	}{
		{name: "no commands", want: base},
		{name: "commands are sorted in", commands: []string{"ls", "cat", "zzz"},
			want: []string{"[", "bash", "cat", "dash", "echo", "false", "fish", "help", "ksh", "ls", "printf", "pwd", "sh", "test", "true", "which", "zsh", "zzz"}},
		{name: "a command named like a builtin program is listed once", commands: []string{"echo", "true"}, want: base},
		{name: "a command named like a shell builtin is listed once", commands: []string{"which", "help"}, want: base},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmds := []vsh.Command{}
			for _, name := range tc.commands {
				cmds = append(cmds, vsh.Command{Name: name, Run: func(context.Context, *vsh.Invocation) error { return nil }})
			}
			e := newExecutor(t, vsh.Config{Commands: cmds})
			got := e.BinNames()
			assert.Equal(t, tc.want, got)
			assert.True(t, slices.IsSorted(got))
			assert.Equal(t, got, slices.Compact(slices.Clone(got)))
		})
	}
}

func TestStubContent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		bin, name, want string
	}{
		{bin: "/bin", name: "sh", want: "#!/bin/sh\n# sh: Rune built-in shell, run in-process by the Rune shell.\nexec sh \"$@\"\n"},
		{bin: "/bin", name: "zsh", want: "#!/bin/sh\n# zsh: Rune built-in shell, run in-process by the Rune shell.\nexec zsh \"$@\"\n"},
		{bin: "/bin", name: "ls", want: "#!/bin/sh\n# ls: Rune built-in command, run in-process by the Rune shell.\nexec ls \"$@\"\n"},
		{bin: "/bin", name: "[", want: "#!/bin/sh\n# [: Rune built-in command, run in-process by the Rune shell.\nexec [ \"$@\"\n"},
		{bin: "/tools", name: "bash", want: "#!/tools/sh\n# bash: Rune built-in shell, run in-process by the Rune shell.\nexec bash \"$@\"\n"},
		{bin: "/opt/x/bin", name: "which", want: "#!/opt/x/bin/sh\n# which: Rune built-in command, run in-process by the Rune shell.\nexec which \"$@\"\n"},
	}
	for _, tc := range tests {
		t.Run(tc.bin+" "+tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, newExecutor(t, vsh.Config{Bin: tc.bin}).StubContent(tc.name))
		})
	}
}

func TestInstallBin(t *testing.T) {
	t.Parallel()
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			for _, bin := range []string{"bin", "tools", "deep/er/bin"} {
				t.Run(bin, func(t *testing.T) {
					t.Parallel()
					// A stale file where a stub goes is replaced, even
					// one that is not executable.
					f := vshtest.New(t, vshtest.Options{Backend: backend, Files: map[string]string{bin + "/ls": "stale\n"}})
					e := newExecutor(t, vsh.Config{FS: f.Scheme, Root: f.Home, Bin: f.Path(bin), Commands: coreutils.Commands()})
					check := func() {
						t.Helper()
						for _, name := range e.BinNames() {
							p := f.Path(bin + "/" + name)
							info, err := f.Scheme.Stat(p)
							require.NoError(t, err, name)
							assert.True(t, info.Mode().IsRegular(), name)
							assert.Equal(t, fs.FileMode(0o755), info.Mode().Perm(), name)
							assert.Equal(t, e.StubContent(name), f.ReadFile(t, p), name)
						}
					}
					check()
					assert.Contains(t, f.ReadFile(t, bin+"/bash"), "Rune built-in shell")
					assert.Contains(t, f.ReadFile(t, bin+"/ls"), "Rune built-in command")

					// Reinstalling is harmless and restores what was
					// changed.
					f.Seed(t, bin+"/ls", "a much longer replacement for the stub\n")
					require.NoError(t, e.InstallBin())
					check()
					again := newExecutor(t, vsh.Config{FS: f.Scheme, Root: f.Home, Bin: f.Path(bin), Commands: coreutils.Commands()})
					assert.Equal(t, e.BinNames(), again.BinNames())
					check()
				})
			}
		})
	}
}

func TestSearchPath(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: map[string]string{"c/plain": "", "c/dirname/.keep": ""}})
	for _, name := range []string{"a/tool", "b/tool", "b/only", "sub/rel/tool"} {
		seedExec(t, f, name, "")
	}
	tests := []struct {
		name     string
		pathList []string
		file     string
		dir      string
		want     string
	}{
		{name: "the first match", pathList: []string{"/a", "/b"}, file: "tool", want: "/a/tool"},
		{name: "PATH order", pathList: []string{"/b", "/a"}, file: "tool", want: "/b/tool"},
		{name: "a later entry", pathList: []string{"/a", "/b"}, file: "only", want: "/b/only"},
		{name: "a missing entry is skipped", pathList: []string{"/missing", "/a"}, file: "tool", want: "/a/tool"},
		{name: "a trailing slash", pathList: []string{"/a/"}, file: "tool", want: "/a/tool"},
		{name: "an empty entry is the directory", pathList: []string{""}, file: "tool", dir: "/a", want: "/a/tool"},
		{name: "dot is the directory", pathList: []string{"."}, file: "tool", dir: "/b", want: "/b/tool"},
		{name: "a relative entry", pathList: []string{"rel"}, file: "tool", dir: "/sub", want: "/sub/rel/tool"},
		{name: "a parent entry", pathList: []string{"../a"}, file: "tool", dir: "/sub", want: "/a/tool"},
		{name: "a non-executable file", pathList: []string{"/c"}, file: "plain"},
		{name: "a directory", pathList: []string{"/c"}, file: "dirname"},
		{name: "an empty name", pathList: []string{"/a"}, file: ""},
		{name: "no entries", pathList: nil, file: "tool"},
		{name: "a missing name", pathList: []string{"/a", "/b"}, file: "nope"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := tc.dir
			if dir == "" {
				dir = "/"
			}
			got, ok := f.Exec.SearchPath(tc.pathList, tc.file, dir)
			assert.Equal(t, tc.want != "", ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPathList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		bin, value string
		want       []string
	}{
		{bin: "/bin", value: "", want: []string{"/bin"}},
		{bin: "/tools", value: "", want: []string{"/tools"}},
		{bin: "/bin", value: "/x", want: []string{"/x"}},
		{bin: "/bin", value: "/x:/y", want: []string{"/x", "/y"}},
		{bin: "/bin", value: "/x::/y", want: []string{"/x", "", "/y"}},
		{bin: "/bin", value: ":", want: []string{"", ""}},
		{bin: "/bin", value: "/x:", want: []string{"/x", ""}},
		{bin: "/bin", value: "rel:.", want: []string{"rel", "."}},
	}
	for _, tc := range tests {
		t.Run(tc.bin+" "+tc.value, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, newExecutor(t, vsh.Config{Bin: tc.bin}).PathList(tc.value))
		})
	}
}

func TestEnvValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  []string
		key  string
		want string
		ok   bool
	}{
		{name: "missing", env: []string{"A=1"}, key: "PATH"},
		{name: "empty environment", key: "PATH"},
		{name: "found", env: []string{"A=1", "PATH=/x", "B=2"}, key: "PATH", want: "/x", ok: true},
		{name: "the last wins", env: []string{"PATH=/x", "PATH=/y"}, key: "PATH", want: "/y", ok: true},
		{name: "an empty value", env: []string{"PATH=/x", "PATH="}, key: "PATH", want: "", ok: true},
		{name: "a longer name sharing the prefix", env: []string{"PATH=/x", "PATHX=/y"}, key: "PATH", want: "/x", ok: true},
		{name: "only a longer name", env: []string{"PATHX=/y"}, key: "PATH"},
		{name: "a name without a value", env: []string{"PATH"}, key: "PATH"},
		{name: "a value holding =", env: []string{"A=b=c"}, key: "A", want: "b=c", ok: true},
		{name: "names are case-sensitive", env: []string{"path=/x"}, key: "PATH"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := vsh.EnvValue(tc.env, tc.key)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLookPath(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: map[string]string{
		"script.sh":     "echo hi\n",
		"sub/tool":      "echo tool\n",
		"sub/dir/.keep": "",
	}})
	seedExec(t, f, "other/ls", "")
	seedExec(t, f, "sub/exe", "")
	cmds := coreutils.Commands()
	executors := map[string]*vsh.Executor{
		"":        f.Exec,
		"other":   newExecutor(t, vsh.Config{FS: f.Scheme, Env: []string{"PATH=/other:/bin"}, Commands: cmds}),
		"dot":     newExecutor(t, vsh.Config{FS: f.Scheme, Env: []string{"PATH=/x", "PATH=."}, Commands: cmds}),
		"tools":   newExecutor(t, vsh.Config{FS: f.Scheme, Bin: "/tools", Commands: cmds}),
		"subroot": newExecutor(t, vsh.Config{FS: f.Scheme, Root: "/sub", Commands: cmds}),
	}
	tests := []struct {
		exec, name, dir, want string
		notExist              bool
	}{
		{name: "", want: "/bin/sh"},
		{name: "ls", want: "/bin/ls"},
		{name: "bash", want: "/bin/bash"},
		{name: "echo", want: "/bin/echo"},
		{name: "which", want: "/bin/which"},
		{name: "nope", notExist: true},
		{name: "script.sh", notExist: true},
		{name: "./script.sh", want: "/script.sh"},
		{name: "./tool", dir: "/sub", want: "/sub/tool"},
		{name: "sub/tool", want: "/sub/tool"},
		{name: "/sub/tool", dir: "/elsewhere", want: "/sub/tool"},
		{name: "../tool", dir: "/sub/dir", want: "/sub/tool"},
		{name: "~/script.sh", want: "/script.sh"},
		{name: "~/script.sh", dir: "/sub", want: "/script.sh"},
		{name: "~", notExist: true},
		{name: "./dir", dir: "/sub", notExist: true},
		{name: "$HOME/script.sh", notExist: true},
		{name: "$x", notExist: true},
		{name: "./missing", notExist: true},
		{name: "./tool", dir: "sub", want: "/sub/tool"},
		{name: "./tool", dir: "~/sub", want: "/sub/tool"},
		{name: "ls", dir: "/sub", want: "/bin/ls"},
		{exec: "other", name: "ls", want: "/other/ls"},
		{exec: "other", name: "cat", want: "/bin/cat"},
		{exec: "dot", name: "exe", dir: "/sub", want: "/sub/exe"},
		{exec: "dot", name: "exe", notExist: true},
		{exec: "dot", name: "ls", notExist: true},
		{exec: "tools", name: "", want: "/tools/sh"},
		{exec: "tools", name: "ls", want: "/tools/ls"},
		{exec: "subroot", name: "./tool", want: "/sub/tool"},
		{exec: "subroot", name: "~/tool", dir: "/", want: "/sub/tool"},
		{exec: "subroot", name: "./tool", dir: "dir/..", want: "/sub/tool"},
	}
	for _, tc := range tests {
		t.Run(tc.exec+" "+tc.name+" in "+tc.dir, func(t *testing.T) {
			t.Parallel()
			got, err := executors[tc.exec].LookPath(context.Background(), tc.name, tc.dir)
			if tc.notExist {
				require.ErrorIs(t, err, fs.ErrNotExist)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExpandHome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		root, p, want string
	}{
		{root: "/", p: "~", want: "/"},
		{root: "/home", p: "~", want: "/home"},
		{root: "/home", p: "~/a/b", want: "/home/a/b"},
		{root: "/home", p: "~/", want: "/home"},
		{root: "/home", p: "~/../x", want: "/x"},
		{root: "/home", p: "~user/a", want: "~user/a"},
		{root: "/home", p: "a/~", want: "a/~"},
		{root: "/home", p: "/~/a", want: "/~/a"},
		{root: "/home", p: "rel", want: "rel"},
		{root: "/home", p: "", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.root+" "+tc.p, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, newExecutor(t, vsh.Config{Root: tc.root}).ExpandHome(tc.p))
		})
	}
}

func TestIsBuiltinProgram(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want bool
	}{
		{"echo", true}, {"printf", true}, {"test", true}, {"[", true}, {"true", true}, {"false", true}, {"pwd", true},
		{"cd", false}, {"exit", false}, {"export", false}, {"ls", false}, {"sh", false}, {"which", false}, {"", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, vsh.IsBuiltinProgram(tc.name))
		})
	}
}
