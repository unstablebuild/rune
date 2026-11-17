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
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/coreutils"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

func TestParseShellArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want vsh.ShellArgs
	}{
		{name: "none", args: nil, want: vsh.ShellArgs{}},
		{name: "-c with params", args: []string{"-c", "echo $0 $1", "name", "a"},
			want: vsh.ShellArgs{Command: "echo $0 $1", HasCommand: true, Name: "name", Params: []string{"a"}}},
		{name: "-c with a name only", args: []string{"-c", "x", "name"},
			want: vsh.ShellArgs{Command: "x", HasCommand: true, Name: "name", Params: []string{}}},
		{name: "-c with -- as the name", args: []string{"-c", "x", "--", "a"},
			want: vsh.ShellArgs{Command: "x", HasCommand: true, Name: "--", Params: []string{"a"}}},
		{name: "-lc combined", args: []string{"-lc", "ls"},
			want: vsh.ShellArgs{Command: "ls", HasCommand: true}},
		{name: "-xc combined", args: []string{"-xc", "ls"},
			want: vsh.ShellArgs{Command: "ls", HasCommand: true, Opts: []string{"-x"}}},
		{name: "-ce: letters after c still apply", args: []string{"-ce", "ls"},
			want: vsh.ShellArgs{Command: "ls", HasCommand: true, Opts: []string{"-e"}}},
		{name: "-c without a script", args: []string{"-c"}, want: vsh.ShellArgs{HasCommand: true}},
		{name: "-c after options", args: []string{"-e", "-c"}, want: vsh.ShellArgs{HasCommand: true, Opts: []string{"-e"}}},
		{name: "script file and params", args: []string{"s.sh", "x", "y"},
			want: vsh.ShellArgs{File: "s.sh", Params: []string{"x", "y"}}},
		{name: "script file alone", args: []string{"s.sh"}, want: vsh.ShellArgs{File: "s.sh", Params: []string{}}},
		{name: "options after the script are its params", args: []string{"s.sh", "-c", "x"},
			want: vsh.ShellArgs{File: "s.sh", Params: []string{"-c", "x"}}},
		{name: "set options", args: []string{"-e", "-u", "-x", "+x", "-o", "pipefail", "s.sh"},
			want: vsh.ShellArgs{File: "s.sh", Params: []string{}, Opts: []string{"-e", "-u", "-x", "+x", "-o", "pipefail"}}},
		{name: "-f -n -a pass through", args: []string{"-fna", "+f"},
			want: vsh.ShellArgs{Opts: []string{"-f", "-n", "-a", "+f"}}},
		{name: "+o unsets an option", args: []string{"+o", "errexit", "-c", "x"},
			want: vsh.ShellArgs{Command: "x", HasCommand: true, Opts: []string{"+o", "errexit"}}},
		{name: "-o without a name", args: []string{"-o"}, want: vsh.ShellArgs{}},
		{name: "-eu combined", args: []string{"-eu", "-c", "x"},
			want: vsh.ShellArgs{Command: "x", HasCommand: true, Opts: []string{"-e", "-u"}}},
		{name: "+eu combined", args: []string{"+eu", "-c", "x"},
			want: vsh.ShellArgs{Command: "x", HasCommand: true, Opts: []string{"+e", "+u"}}},
		{name: "login and startup flags are ignored", args: []string{"--login", "-i", "-s", "--norc", "--noprofile", "-l"},
			want: vsh.ShellArgs{}},
		{name: "long flags before -c", args: []string{"--login", "--posix", "-c", "x"},
			want: vsh.ShellArgs{Command: "x", HasCommand: true}},
		{name: "double dash ends options", args: []string{"--", "-x", "y"},
			want: vsh.ShellArgs{File: "-x", Params: []string{"y"}}},
		{name: "double dash alone", args: []string{"--"}, want: vsh.ShellArgs{}},
		{name: "options before double dash", args: []string{"-x", "--", "s.sh", "a"},
			want: vsh.ShellArgs{File: "s.sh", Params: []string{"a"}, Opts: []string{"-x"}}},
		{name: "a lone dash ends options", args: []string{"-", "s.sh", "a"},
			want: vsh.ShellArgs{File: "s.sh", Params: []string{"a"}}},
		{name: "a lone dash alone reads stdin", args: []string{"-e", "-"}, want: vsh.ShellArgs{Opts: []string{"-e"}}},
		{name: "unknown letters are ignored", args: []string{"-Z", "-c", "x"},
			want: vsh.ShellArgs{Command: "x", HasCommand: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, vsh.ParseShellArgs(tc.args))
		})
	}
}

func TestShebang(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src  string
		want string
		ok   bool
	}{
		{"echo hi\n", "", false},
		{"", "", false},
		{" #!/bin/sh\n", "", false},
		{"#!/bin/sh\necho", "/bin/sh", true},
		{"#!/bin/sh", "/bin/sh", true},
		{"#!/bin/bash\r\necho", "/bin/bash", true},
		{"#! /bin/bash -e\n", "/bin/bash", true},
		{"#!sh\n", "sh", true},
		{"#!/usr/bin/env bash\n", "bash", true},
		{"#!/usr/bin/env -S bash -e\n", "bash", true},
		{"#!/usr/bin/env FOO=1 zsh\n", "zsh", true},
		{"#!/usr/bin/env -i PATH=/x A=b sh -x\n", "sh", true},
		{"#!/bin/env dash\n", "dash", true},
		{"#!/usr/bin/python3\n", "/usr/bin/python3", true},
		{"#!\n", "", false},
		{"#!   \n", "", false},
		{"#!/usr/bin/env\n", "/usr/bin/env", true},
		{"#!/usr/bin/env -S\n", "/usr/bin/env", true},
		{"#!/bin/sh\n#!/usr/bin/python3\n", "/bin/sh", true},
	}
	for _, tc := range tests {
		t.Run(tc.src, func(t *testing.T) {
			t.Parallel()
			got, ok := vsh.Shebang([]byte(tc.src))
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: map[string]string{"plain": "", "d/.keep": "", "sub/.keep": ""}})
	scripts := map[string]string{
		"s.sh":           "echo s\n",
		"sub/tool":       "echo tool\n",
		"p.py":           "#!/usr/bin/python3\nprint(1)\n",
		"env.py":         "#!/usr/bin/env python3\n",
		"local.sh":       "#!/usr/local/bin/bash\necho local\n",
		"bin/custom.sh":  "echo custom\n",
		"bin/zzz":        "echo zzz\n",
		"other/ls":       "echo other\n",
		"tools/.keep.sh": "",
	}
	for name, content := range scripts {
		seedExec(t, f, name, content)
	}
	tools := newExecutor(t, vsh.Config{FS: f.Scheme, Bin: "/tools", Commands: coreutils.Commands()})
	executors := map[string]*vsh.Executor{"": f.Exec, "/tools": tools}
	stub := f.ReadFile(t, "bin/ls")

	tests := []struct {
		name    string
		bin     string
		cmd     string
		args    []string
		pathEnv string
		dir     string
		want    vsh.Program
		err     error
		msg     string
	}{
		{name: "the empty name is the login shell", args: []string{"-c", "x"},
			want: vsh.Program{Kind: "shell", Path: "/bin/sh", Name: "sh", Args: []string{"-c", "x"},
				Shell: vsh.ShellArgs{Command: "x", HasCommand: true}}},
		{name: "a shell", cmd: "bash", args: []string{"s.sh", "a"},
			want: vsh.Program{Kind: "shell", Path: "/bin/bash", Name: "bash", Arg0: "/bin/bash", Args: []string{"s.sh", "a"},
				Shell: vsh.ShellArgs{File: "s.sh", Params: []string{"a"}}}},
		{name: "a command", cmd: "ls", args: []string{"-l"},
			want: vsh.Program{Kind: "command", Path: "/bin/ls", Name: "ls", Arg0: "/bin/ls", Args: []string{"-l"}}},
		{name: "a shell builtin", cmd: "which",
			want: vsh.Program{Kind: "vsh builtin", Path: "/bin/which", Name: "which", Arg0: "/bin/which"}},
		{name: "help", cmd: "help",
			want: vsh.Program{Kind: "vsh builtin", Path: "/bin/help", Name: "help", Arg0: "/bin/help"}},
		{name: "an interpreter builtin", cmd: "echo", args: []string{"hi"},
			want: vsh.Program{Kind: "interp builtin", Path: "/bin/echo", Name: "echo", Arg0: "/bin/echo", Args: []string{"hi"}}},
		{name: "[", cmd: "[",
			want: vsh.Program{Kind: "interp builtin", Path: "/bin/[", Name: "[", Arg0: "/bin/["}},
		{name: "a program by path keeps the path as $0", cmd: "/bin/true",
			want: vsh.Program{Kind: "interp builtin", Path: "/bin/true", Name: "true", Arg0: "/bin/true"}},
		{name: "an unclean path", cmd: "/bin/../bin/ls",
			want: vsh.Program{Kind: "command", Path: "/bin/ls", Name: "ls", Arg0: "/bin/../bin/ls"}},
		{name: "a relative path into bin", cmd: "bin/sh", dir: "/",
			want: vsh.Program{Kind: "shell", Path: "/bin/sh", Name: "sh", Arg0: "bin/sh"}},
		{name: "a script by path", cmd: "./s.sh", args: []string{"a"}, dir: "/",
			want: vsh.Program{Kind: "script", Path: "/s.sh", Name: "s.sh", Arg0: "./s.sh", Args: []string{"a"}, Script: "echo s\n"}},
		{name: "a script relative to the directory", cmd: "../s.sh", dir: "/sub",
			want: vsh.Program{Kind: "script", Path: "/s.sh", Name: "s.sh", Arg0: "../s.sh", Script: "echo s\n"}},
		{name: "a script under ~", cmd: "~/s.sh", dir: "/sub",
			want: vsh.Program{Kind: "script", Path: "/s.sh", Name: "s.sh", Arg0: "~/s.sh", Script: "echo s\n"}},
		{name: "a script found in PATH is $0 by its file", cmd: "s.sh", pathEnv: "/bin:/",
			want: vsh.Program{Kind: "script", Path: "/s.sh", Name: "s.sh", Arg0: "/s.sh", Script: "echo s\n"}},
		{name: "a relative PATH entry", cmd: "tool", pathEnv: ".", dir: "/sub",
			want: vsh.Program{Kind: "script", Path: "/sub/tool", Name: "tool", Arg0: "/sub/tool", Script: "echo tool\n"}},
		{name: "an empty PATH entry is the directory", cmd: "tool", pathEnv: "/bin:", dir: "/sub",
			want: vsh.Program{Kind: "script", Path: "/sub/tool", Name: "tool", Arg0: "/sub/tool", Script: "echo tool\n"}},
		{name: "PATH order", cmd: "ls", pathEnv: "/other:/bin",
			want: vsh.Program{Kind: "script", Path: "/other/ls", Name: "ls", Arg0: "/other/ls", Script: "echo other\n"}},
		{name: "a script whose shebang names a shell anywhere", cmd: "./local.sh", dir: "/",
			want: vsh.Program{Kind: "script", Path: "/local.sh", Name: "local.sh", Arg0: "./local.sh", Script: scripts["local.sh"]}},
		{name: "a user script in bin is a script", cmd: "custom.sh",
			want: vsh.Program{Kind: "script", Path: "/bin/custom.sh", Name: "custom.sh", Arg0: "/bin/custom.sh", Script: "echo custom\n"}},
		{name: "a file in bin named like no program is a script", cmd: "/bin/zzz",
			want: vsh.Program{Kind: "script", Path: "/bin/zzz", Name: "zzz", Arg0: "/bin/zzz", Script: "echo zzz\n"}},
		{name: "a custom bin", bin: "/tools", cmd: "ls",
			want: vsh.Program{Kind: "command", Path: "/tools/ls", Name: "ls", Arg0: "/tools/ls"}},
		{name: "the login shell of a custom bin", bin: "/tools",
			want: vsh.Program{Kind: "shell", Path: "/tools/sh", Name: "sh"}},
		{name: "a stub outside the custom bin is a script", bin: "/tools", cmd: "/bin/ls",
			want: vsh.Program{Kind: "script", Path: "/bin/ls", Name: "ls", Arg0: "/bin/ls", Script: stub}},

		{name: "a name missing from PATH", cmd: "nope", err: fs.ErrNotExist,
			msg: `exec: "nope": executable file not found in $PATH`},
		{name: "a file outside PATH", cmd: "s.sh", err: fs.ErrNotExist,
			msg: `exec: "s.sh": executable file not found in $PATH`},
		{name: "a non-executable file in PATH", cmd: "plain", pathEnv: "/", err: fs.ErrNotExist,
			msg: `exec: "plain": executable file not found in $PATH`},
		{name: "a directory in PATH", cmd: "d", pathEnv: "/", err: fs.ErrNotExist,
			msg: `exec: "d": executable file not found in $PATH`},
		{name: "a missing path", cmd: "./missing", dir: "/", err: syscall.ENOENT,
			msg: "fork/exec ./missing: no such file or directory"},
		{name: "a path below a file", cmd: "./plain/x", dir: "/", err: syscall.ENOENT,
			msg: "fork/exec ./plain/x: no such file or directory"},
		{name: "a directory", cmd: "./d", dir: "/", err: syscall.EACCES,
			msg: "fork/exec ./d: permission denied"},
		{name: "bin itself", cmd: "/bin", err: syscall.EACCES, msg: "fork/exec /bin: permission denied"},
		{name: "a foreign interpreter", cmd: "./p.py", dir: "/", err: syscall.ENOEXEC,
			msg: "./p.py: /usr/bin/python3: bad interpreter: not a shell"},
		{name: "a foreign interpreter through env", cmd: "/env.py", err: syscall.ENOEXEC,
			msg: "/env.py: python3: bad interpreter: not a shell"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := tc.dir
			if dir == "" {
				dir = "/"
			}
			got, err := executors[tc.bin].Resolve(tc.cmd, tc.args, tc.pathEnv, dir)
			if tc.err != nil {
				assert.ErrorIs(t, err, tc.err)
				assert.EqualError(t, err, tc.msg)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// scriptedReaderAt answers each ReadAt with the next result, filling
// the bytes it reports with 'x', and records the offsets it was read
// at.
type scriptedReaderAt struct {
	results []readResult
	offs    []int64
	closed  bool
}

type readResult struct {
	n   int
	err error
}

func (r *scriptedReaderAt) ReadAt(p []byte, off int64) (int, error) {
	r.offs = append(r.offs, off)
	if len(r.results) == 0 {
		return 0, io.EOF
	}
	res := r.results[0]
	r.results = r.results[1:]
	for i := range res.n {
		p[i] = 'x'
	}
	return res.n, res.err
}

func (r *scriptedReaderAt) Close() error {
	r.closed = true
	return nil
}

func TestFileReader(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name    string
		results []readResult
		// reads are what successive Reads of 4 bytes return, and offs
		// the offsets the file is read at.
		reads []readResult
		offs  []int64
	}{
		{name: "empty", results: []readResult{{0, io.EOF}},
			reads: []readResult{{0, io.EOF}}, offs: []int64{0}},
		{name: "a short read with EOF is reported before the EOF", results: []readResult{{3, io.EOF}, {0, io.EOF}},
			reads: []readResult{{3, nil}, {0, io.EOF}}, offs: []int64{0, 3}},
		{name: "full reads advance the offset", results: []readResult{{4, nil}, {4, nil}, {1, io.EOF}, {0, io.EOF}},
			reads: []readResult{{4, nil}, {4, nil}, {1, nil}, {0, io.EOF}}, offs: []int64{0, 4, 8, 9}},
		{name: "a full read with EOF", results: []readResult{{4, io.EOF}, {0, io.EOF}},
			reads: []readResult{{4, nil}, {0, io.EOF}}, offs: []int64{0, 4}},
		{name: "an error with data keeps both", results: []readResult{{2, boom}, {0, io.EOF}},
			reads: []readResult{{2, boom}, {0, io.EOF}}, offs: []int64{0, 2}},
		{name: "an error without data", results: []readResult{{0, boom}},
			reads: []readResult{{0, boom}}, offs: []int64{0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := &scriptedReaderAt{results: tc.results}
			r := vsh.NewFileReader(src)
			var reads []readResult
			for range tc.reads {
				n, err := r.Read(make([]byte, 4))
				reads = append(reads, readResult{n, err})
			}
			assert.Equal(t, tc.reads, reads)
			assert.Equal(t, tc.offs, src.offs)

			_, err := r.Write([]byte("x"))
			assert.ErrorIs(t, err, syscall.EBADF)
			require.NoError(t, r.Close())
			assert.True(t, src.closed)
		})
	}
}

func TestFileReaderReadsWorkspaceFiles(t *testing.T) {
	t.Parallel()
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Backend: backend, Files: map[string]string{"f": "0123456789"}})
			// Two readers of one file each read from its start.
			var readers []io.Reader
			for range 2 {
				file, err := f.Scheme.OpenFile(f.Path("f"), os.O_RDONLY, 0)
				require.NoError(t, err)
				r := vsh.NewFileReader(file)
				t.Cleanup(func() { _ = r.Close() })
				readers = append(readers, r)
			}
			buf := make([]byte, 3)
			for _, want := range []string{"012", "012", "345", "345"} {
				r := readers[0]
				readers = append(readers[1:], r)
				n, err := r.Read(buf)
				require.NoError(t, err)
				assert.Equal(t, want, string(buf[:n]))
			}
		})
	}
}
