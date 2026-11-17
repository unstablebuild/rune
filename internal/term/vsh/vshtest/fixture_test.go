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

package vshtest_test

import (
	"bytes"
	"context"
	"io/fs"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

// The suites below run unchanged on every backend, so that the shell
// can depend on no property of one filesystem. Expected text names the
// fixture's directories with {home} and {bin}.

// scriptCase runs one command line to completion.
type scriptCase struct {
	name string
	// files are seeded relative to HOME before the command runs.
	files map[string]string
	env   []string
	// dir is the working directory, relative to HOME.
	dir   string
	stdin string
	argv  []string

	stdout, stderr string
	code           int
	// after are files expected once the command exits, and absent
	// paths that must not exist then, both relative to HOME.
	after  map[string]string
	absent []string
}

func sh(script string) []string { return []string{"sh", "-c", script} }

var scriptCases = []scriptCase{
	// The language.
	{name: "variables and arithmetic",
		argv: sh(`x=2; y=$((x * 21)); echo "$x $y ${#y} ${z:-unset}"`), stdout: "2 42 2 unset\n"},
	{name: "functions, locals and return statuses",
		argv: sh(`v=global; f() { local v=$1; echo "f:$v"; return 3; }; f a; echo "$? $v"`), stdout: "f:a\n3 global\n"},
	{name: "case and loops",
		argv:   sh(`for w in apple kiwi; do case $w in a*) echo "A $w";; *) echo "other $w";; esac; done; i=0; while [ $i -lt 3 ]; do i=$((i+1)); done; until false; do break; done; echo $i`),
		stdout: "A apple\nother kiwi\n3\n"},
	{name: "command substitution and subshells",
		files: map[string]string{"sub/.keep": ""},
		argv:  sh(`v=$(echo inner); (cd sub && pwd); echo "$v $(pwd) $(basename "$(pwd)")"`), stdout: "{home}/sub\ninner {home} " + "{homebase}\n"},
	{name: "pipelines and their statuses",
		argv: sh(`echo x | false; echo $?; false | true; echo $?; set -o pipefail; false | true; echo $?`), stdout: "1\n0\n1\n"},
	{name: "and-or lists",
		argv: sh(`true && echo and; false || echo or; false && echo never; echo $?`), stdout: "and\nor\n1\n"},
	{name: "here-documents and here-strings",
		argv: sh("cat <<EOF\nhome=$HOME\nEOF\ncat <<'EOF'\nraw=$HOME\nEOF\ncat <<< 'here string'"), stdout: "home={home}\nraw=$HOME\nhere string\n"},
	{name: "arrays",
		argv: sh(`a=(one two three); a+=(four); echo "${#a[@]} ${a[1]} ${a[@]: -1}"`), stdout: "4 two four\n"},
	{name: "positional parameters",
		argv: []string{"sh", "-c", `echo "$0 $# $2"; shift; echo "$@"`, "name", "a", "b"}, stdout: "name 2 b\nb\n"},
	{name: "set -e stops at the first failure",
		argv: []string{"sh", "-e", "-c", "echo before; false; echo after"}, stdout: "before\n", code: 1},
	{name: "exported variables reach nested shells, others do not",
		argv: sh(`export A=exported; B=local; sh -c 'echo "$A:$B"'`), stdout: "exported:\n"},
	{name: "read splits fields",
		stdin: "one two three\n",
		argv:  sh(`read a b; echo "$b|$a"`), stdout: "two three|one\n"},

	// Redirections reach the workspace.
	{name: "redirections create, append and read files",
		argv: sh(`echo one > out; echo two >> out; cat < out; wc -l < out`), stdout: "one\ntwo\n2\n",
		after: map[string]string{"out": "one\ntwo\n"}},
	{name: "a truncating redirection drops the old content",
		files: map[string]string{"f": "a much longer old content\n"},
		argv:  sh(`echo new > f`), after: map[string]string{"f": "new\n"}},
	{name: "stderr redirections",
		argv:   sh(`nope 2> err; echo hidden 2>/dev/null 1>&2; cat missing > all 2>&1; echo $?`),
		stdout: "1\n",
		after:  map[string]string{"err": "sh: nope: command not found\n", "all": "cat: missing: No such file or directory\n"}},
	{name: "a here-document into a file",
		argv: sh("cat > notes.md <<'EOF'\n# $title\nEOF"), after: map[string]string{"notes.md": "# $title\n"}},
	{name: "redirecting into a missing directory fails",
		argv: sh(`echo x > nodir/f`), stderr: "open nodir/f: no such file or directory\n", code: 1,
		absent: []string{"nodir"}},
	{name: "redirecting below a file fails",
		files: map[string]string{"f": ""},
		argv:  sh(`echo x > f/g`), stderr: "open f/g: not a directory\n", code: 1},
	{name: "redirecting onto a directory fails",
		files: map[string]string{"d/.keep": ""},
		argv:  sh(`echo x > d`), stderr: "open d: is a directory\n", code: 1},
	{name: "reading a missing file fails",
		argv: sh(`cat < missing`), stderr: "open missing: no such file or directory\n", code: 1},
	{name: "while read consumes a file line by line",
		files: map[string]string{"list": "a\nb c\nlast"},
		argv:  sh(`while read -r l; do echo "<$l>"; done < list; echo "[$l]"`), stdout: "<a>\n<b c>\n[last]\n"},
	{name: "$(< file) reads a file",
		files: map[string]string{"v": "value\n\n"},
		argv:  sh(`echo "[$(< v)]"`), stdout: "[value]\n"},
	{name: "the standard streams and /dev/null",
		stdin:  "input\n",
		argv:   sh(`echo gone > /dev/null; cat /dev/stdin; echo o > /dev/stdout; echo e > /dev/stderr; cat < /dev/null`),
		stdout: "input\no\n", stderr: "e\n"},
	{name: "/dev/tty is absent without a terminal",
		argv: sh(`echo x > /dev/tty`), stderr: "open /dev/tty: " + syscall.ENXIO.Error() + "\n", code: 1},
	{name: "exec redirects the rest of the script",
		argv: sh(`exec > log; echo one; echo two`), after: map[string]string{"log": "one\ntwo\n"}},

	// Globs read the workspace.
	{name: "globs sort, skip hidden files and keep unmatched patterns",
		files:  map[string]string{"b.txt": "", "a.txt": "", ".h.txt": "", "c.md": ""},
		argv:   sh(`echo *.txt; echo ?.md; echo [ab].*; echo *.none; echo .*.txt`),
		stdout: "a.txt b.txt\nc.md\na.txt b.txt\n*.none\n.h.txt\n"},
	{name: "globs in directories and absolute paths",
		files:  map[string]string{"d/x.go": "", "d/y.go": "", "d/z.md": ""},
		argv:   sh(`echo d/*.go; echo $HOME/d/x*; cd d; echo ../d/*.md`),
		stdout: "d/x.go d/y.go\n{home}/d/x.go\n../d/z.md\n"},
	{name: "globstar and nullglob",
		files:  map[string]string{"a/b/c.txt": "", "a/d.txt": ""},
		argv:   sh(`shopt -s globstar nullglob; echo a/**/*.txt; echo none* end`),
		stdout: "a/b/c.txt a/d.txt\nend\n"},

	// File tests stat the workspace.
	{name: "file tests",
		files: map[string]string{"f": "x", "empty": "", "d/.keep": ""},
		// The size of a directory is the filesystem's own business,
		// so -s does not test one.
		argv:   sh(`for p in f empty d missing bin/ls; do printf '%s:' $p; [ -e $p ] && printf e; [ -f $p ] && printf f; [ -d $p ] && printf d; [ -f $p ] && [ -s $p ] && printf s; [ -r $p ] && printf r; [ -w $p ] && printf w; [ -x $p ] && printf x; [ -L $p ] && printf L; echo; done`),
		stdout: "f:efsrw\nempty:efrw\nd:edrwx\nmissing:\nbin/ls:efsrwx\n"},
	{name: "file tests on absolute paths and after cd",
		files:  map[string]string{"d/f": "x"},
		argv:   sh(`[ -f $HOME/d/f ] && echo abs; cd d; [ -f f ] && [ -f ../d/f ] && [ ! -e d ] && echo rel`),
		stdout: "abs\nrel\n"},

	// The working directory.
	{name: "cd moves the working directory of later commands",
		files:  map[string]string{"a/b/f": "in b\n"},
		argv:   sh(`cd a/b && cat f && cd .. && pwd && cd - && cd && pwd && echo "$OLDPWD" && cd ~/a && pwd`),
		stdout: "in b\n{home}/a\n{home}/a/b\n{home}\n{home}/a/b\n{home}/a\n"},
	{name: "cd into a file or a missing directory fails",
		files:  map[string]string{"f": ""},
		argv:   sh(`cd f; echo $?; cd missing; echo $?; pwd`),
		stdout: "1\n1\n{home}\n", stderr: "cd: no such file or directory: \"f\"\ncd: no such file or directory: \"missing\"\n"},
	{name: "the command's directory is where it starts",
		files: map[string]string{"sub/x": "x\n"},
		dir:   "sub", argv: sh(`pwd; cat x; echo $PWD`), stdout: "{home}/sub\nx\n{home}/sub\n"},
	{name: "pwd -P answers without the host",
		files: map[string]string{"sub/.keep": ""},
		dir:   "sub", argv: sh(`pwd -P`), stdout: "{home}/sub\n"},

	// Programs and their lookup.
	{name: "the environment names the shell's directories",
		env:    []string{"HOME=/elsewhere"},
		argv:   sh(`echo "$HOME|$PWD|$SHELL|$PATH|$TERM"`),
		stdout: "{home}|{home}|{bin}/sh|{bin}|dumb\n"},
	{name: "type, command -v and which answer from the bin directory",
		argv:   sh(`type ls; type cd; type if; type -t grep; command -v grep; command -v cd; which sh; which nope`),
		stdout: "ls is {bin}/ls\ncd is a shell builtin\nif is a shell keyword\nfile\n{bin}/grep\ncd\n{bin}/sh\n",
		stderr: "which: no nope in ({bin})\n", code: 1},
	{name: "programs run by path",
		argv: sh(`$HOME/bin/echo by path; cd bin && ./echo relative`), stdout: "by path\nrelative\n"},
	{name: "a PATH without the bin directory finds nothing",
		argv: sh(`PATH=/nowhere; ls`), stderr: "sh: ls: command not found\n", code: 127},
	{name: "a missing program",
		argv: sh(`./missing; echo $?; nope`), stdout: "127\n", stderr: "sh: ./missing: no such file or directory\nsh: nope: command not found\n", code: 127},
	{name: "running a directory is refused",
		files: map[string]string{"d/.keep": ""},
		argv:  sh(`./d`), stderr: "sh: ./d: permission denied\n", code: 126},
	{name: "help lists the commands",
		argv: sh(`help | head -n 1; help | grep -c '^  grep '`), stdout: "Rune built-in shell\n1\n"},

	// Scripts.
	{name: "scripts run with their arguments",
		files: map[string]string{"s.sh": "#!/bin/sh\necho \"$0:$#:$1\"\n"},
		argv:  sh(`./s.sh a b; sh s.sh c; bash ./s.sh`), stdout: "./s.sh:2:a\ns.sh:1:c\n./s.sh:0:\n"},
	{name: "a script runs in its caller's directory",
		files: map[string]string{"sub/where.sh": "pwd\n"},
		argv:  sh(`sub/where.sh; cd sub && ./where.sh`), stdout: "{home}\n{home}/sub\n"},
	{name: "a script names its interpreter through env",
		files: map[string]string{"e.sh": "#!/usr/bin/env bash\necho env $#\n"},
		argv:  sh(`./e.sh x y`), stdout: "env 2\n"},
	{name: "a script with a foreign interpreter is refused",
		files: map[string]string{"p.py": "#!/usr/bin/env python3\nprint(1)\n"},
		argv:  sh(`./p.py`), stderr: "sh: ./p.py: python3: bad interpreter: not a shell\n", code: 126},
	{name: "a script's exit status",
		files: map[string]string{"s.sh": "exit 7\n"},
		argv:  sh(`./s.sh; echo $?`), stdout: "7\n"},
	{name: "source runs a file in the current shell",
		files: map[string]string{"lib/env.sh": "v=sourced\n"},
		argv:  sh(`cd lib; . env.sh; source ./env.sh; echo $v`), stdout: "sourced\n"},
	{name: "a script read from standard input",
		argv: []string{"sh"}, stdin: "echo from stdin\nexit 4\n", stdout: "from stdin\n", code: 4},
	{name: "a script operand",
		files: map[string]string{"s.sh": "echo \"$0 $1\"\n"},
		argv:  []string{"bash", "s.sh", "arg"}, stdout: "s.sh arg\n"},
	{name: "a missing script operand",
		argv: []string{"sh", "missing.sh"}, stderr: "sh: missing.sh: no such file or directory\n", code: 127},
	{name: "nested shells propagate statuses",
		argv: sh(`sh -c 'exit 5'; echo $?; bash -c 'zsh -c "echo deep \$0"'`), stdout: "5\ndeep zsh\n"},
	{name: "unbounded recursion stops",
		files: map[string]string{"r.sh": "./r.sh\n"},
		argv:  sh(`./r.sh`), stderr: "r.sh: too many nested shells\n", code: 126},
	{name: "syntax errors are status 2",
		argv: sh(`echo )`), stderr: "sh: 1:6: a command can only contain words and redirects; encountered `)`\n", code: 2},
	{name: "process substitution is refused",
		argv: sh(`cat <(echo x)`), stderr: "sh: 1:5: process substitution is not supported\n", code: 2},

	// The utilities, through the shell.
	{name: "mkdir, touch, cp, mv and rm change the workspace",
		files:  map[string]string{"a.txt": "alpha\n"},
		argv:   sh(`mkdir -p x/y && touch x/t && cp a.txt x/y/ && mv x/y/a.txt x/y/b.txt && cp -r x z && rm x/t && mv z/y w`),
		after:  map[string]string{"a.txt": "alpha\n", "x/y/b.txt": "alpha\n", "z/t": "", "w/b.txt": "alpha\n"},
		absent: []string{"x/t", "x/y/a.txt", "z/y"}},
	{name: "mv moves a tree",
		files: map[string]string{"d/f": "f\n", "d/s/g": "g\n", "into/.keep": ""},
		argv:  sh(`mv d e && mv e into`), after: map[string]string{"into/e/f": "f\n", "into/e/s/g": "g\n"},
		absent: []string{"d", "e"}},
	{name: "rm -r removes a tree and rmdir only empty directories",
		files:  map[string]string{"t/a/b": "", "t/c": "", "u/v/.keep": ""},
		argv:   sh(`rm -r t; rmdir u; echo $?; rm u/v/.keep; rmdir -p u/v`),
		stdout: "1\n", stderr: "rmdir: failed to remove 'u': Directory not empty\n",
		absent: []string{"t", "u"}},
	{name: "rm -rf refuses the working directory",
		files: map[string]string{"keep": "k\n"},
		argv:  sh(`rm -rf .`), stderr: "rm: refusing to remove '.' or '..' directory: skipping '.'\n", code: 1,
		after: map[string]string{"keep": "k\n"}},
	{name: "cp onto itself is refused",
		files: map[string]string{"f": "f\n"},
		argv:  sh(`cp f .`), stderr: "cp: 'f' and './f' are the same file\n", code: 1,
		after: map[string]string{"f": "f\n"}},
	{name: "grep walks the tree",
		files:  map[string]string{"src/a.go": "package a\n", "src/b/b.go": "package b\n// TODO\n", "README": "TODO list\n"},
		argv:   sh(`grep -rn TODO .; grep -rl package src; cd src && grep -r TODO ..`),
		stdout: "README:1:TODO list\nsrc/b/b.go:2:// TODO\nsrc/a.go\nsrc/b/b.go\n../README:TODO list\n../src/b/b.go:// TODO\n"},
	{name: "ls lists directories",
		files:  map[string]string{"d/b": "", "d/a": "", "d/.h": "", "d/s/x": ""},
		argv:   sh(`ls d; ls -a d; ls -F d; ls -d d/*; ls -R d`),
		stdout: "a\nb\ns\n" + ".h\na\nb\ns\n" + "a\nb\ns/\n" + "d/a\nd/b\nd/s\n" + "d:\na\nb\ns\n\nd/s:\nx\n"},
	{name: "ls -l shows modes and sizes",
		files:  map[string]string{"f": "12345"},
		argv:   sh(`ls -l f bin/sh | while read m n u g s rest; do echo "$m $s"; done`),
		stdout: "-rwxr-xr-x {shstubsize}\n-rw-r--r-- 5\n"},
	{name: "pipelines of utilities",
		files:  map[string]string{"words": "b\na\nc\na\n"},
		argv:   sh(`cat words | grep -v c | head -n 2 | tail -n 1; grep -c a words | tee count; wc -l < words`),
		stdout: "a\n2\n4\n", after: map[string]string{"count": "2\n"}},
	{name: "head, tail and wc over files",
		files: map[string]string{"a": "1\n2\n3\n", "b": "x y\n"},
		argv:  sh(`head -n 1 a b; tail -n 1 a; wc a b`),
		stdout: "==> a <==\n1\n\n==> b <==\nx y\n3\n" +
			"      3       3       6 a\n      1       2       4 b\n      4       5      10 total\n"},
	{name: "tee writes several files",
		files: map[string]string{"log": "old\n"},
		stdin: "data\n", argv: sh(`tee new | tee -a log`), stdout: "data\n",
		after: map[string]string{"new": "data\n", "log": "old\ndata\n"}},
	{name: "basename and dirname",
		argv: sh(`basename a/b.txt .txt; dirname a/b/; dirname /; basename "$PWD"`), stdout: "b\na\n/\n{homebase}\n"},
	{name: "env lists the exported environment",
		env:  []string{"FOO=bar"},
		argv: sh(`env | grep -E '^(FOO|HOME|SHELL)='`), stdout: "FOO=bar\nHOME={home}\nSHELL={bin}/sh\n"},
	{name: "utility errors keep the shell going",
		argv:   sh(`cat missing; echo $?; mkdir; echo $?; ls -z; echo $?`),
		stdout: "1\n2\n2\n",
		stderr: "cat: missing: No such file or directory\nmkdir: missing operand\nls: invalid option -- 'z'\n"},
	{name: "large files stream through pipes",
		files: map[string]string{"big": strings.Repeat("0123456789\n", 20000)},
		argv:  sh(`cat big | wc -lc; tail -n 1 big; grep -c 0 big`), stdout: " 20000 220000\n0123456789\n20000\n"},
	{name: "many appends to one file",
		argv:   sh(`i=0; while [ $i -lt 100 ]; do echo "line $i" >> log; i=$((i+1)); done; wc -l < log; tail -n 1 log`),
		stdout: "100\nline 99\n"},
	{name: "a loop reads one file and writes another",
		files: map[string]string{"in": "a\nb\n"},
		argv:  sh(`while read -r l; do echo "$l!"; done < in > out; cat out`), stdout: "a!\nb!\n",
		after: map[string]string{"in": "a\nb\n", "out": "a!\nb!\n"}},
	{name: "the devices are the process's own",
		argv:   sh(`echo in | cat /dev/stdin; echo out | tee /dev/stderr; grep x /dev/null; echo $?; cp /dev/null emptied; [ -c /dev/null ] && [ -e /dev/stdin ] && [ -w /dev/stdout ] && echo devices`),
		stdout: "in\nout\n1\ndevices\n", stderr: "out\n",
		after: map[string]string{"emptied": ""}},
	{name: "utilities create no file in a missing directory",
		files: map[string]string{"a.txt": "a\n"},
		argv:  sh(`cp a.txt no/x; tee no/y < a.txt > /dev/null; touch no/z; mkdir no/w`), code: 1,
		stderr: "cp: cannot create regular file 'no/x': No such file or directory\ntee: no/y: No such file or directory\n" +
			"touch: cannot touch 'no/z': No such file or directory\nmkdir: cannot create directory 'no/w': No such file or directory\n",
		absent: []string{"no"}},
	{name: "mv replaces an existing file",
		files: map[string]string{"a": "new\n", "b": "old\n"},
		argv:  sh(`mv a b`), after: map[string]string{"b": "new\n"}, absent: []string{"a"}},

	// Programs started directly.
	{name: "a program started directly",
		files: map[string]string{"a.txt": "alpha\n"},
		argv:  []string{"cat", "a.txt"}, stdout: "alpha\n"},
	{name: "a program started by absolute path",
		argv: []string{"{bin}/echo", "-n", "it's"}, stdout: "it's"},
	{name: "an interpreter builtin started as a program",
		argv: []string{"test", "-d", "bin"}},
	{name: "a failing program started directly",
		argv: []string{"false"}, code: 1},
	{name: "a script started directly",
		files: map[string]string{"s.sh": "echo \"$0 $*\"\n"},
		argv:  []string{"./s.sh", "a", "b"}, stdout: "./s.sh a b\n"},
}

func TestScripts(t *testing.T) {
	t.Parallel()
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range scriptCases {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					f := vshtest.New(t, vshtest.Options{Backend: backend, Files: tc.files, Env: tc.env})
					x := expander(t, f)
					argv := make([]string, len(tc.argv))
					for i, a := range tc.argv {
						argv[i] = x.Replace(a)
					}

					got := run(t, f, workspaceapi.Cmd{
						Path: argv[0], Args: argv[1:], Dir: f.Path(tc.dir), Stdin: strings.NewReader(tc.stdin),
					})

					assert.Equal(t, x.Replace(tc.stdout), got.stdout, "stdout")
					assert.Equal(t, x.Replace(tc.stderr), got.stderr, "stderr")
					assert.Equal(t, tc.code, got.code, "exit code")
					for name, content := range tc.after {
						assert.Equal(t, content, f.ReadFile(t, name), name)
					}
					for _, name := range tc.absent {
						_, err := f.Scheme.Lstat(f.Path(name))
						assert.ErrorIs(t, err, fs.ErrNotExist, name)
					}
				})
			}
		})
	}
}

func TestStartErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		path string
		dir  string
		is   error
		msg  string
	}{
		{name: "a program not in the PATH", path: "aCommandThatShouldntExist55", is: fs.ErrNotExist,
			msg: `exec: "aCommandThatShouldntExist55": executable file not found in $PATH`},
		{name: "a missing file", path: "./missing", is: syscall.ENOENT,
			msg: "fork/exec ./missing: no such file or directory"},
		{name: "a directory", path: "./d", is: syscall.EACCES,
			msg: "fork/exec ./d: permission denied"},
		{name: "a foreign interpreter", path: "./p.py", is: syscall.ENOEXEC,
			msg: "./p.py: /usr/bin/python3: bad interpreter: not a shell"},
		{name: "a missing directory", path: "ls", dir: "nowhere", is: syscall.ENOENT,
			msg: "chdir {home}/nowhere: no such file or directory"},
		{name: "a file as the directory", path: "ls", dir: "f", is: syscall.ENOTDIR,
			msg: "chdir {home}/f: not a directory"},
	}
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					f := vshtest.New(t, vshtest.Options{Backend: backend, Files: map[string]string{
						"f": "", "d/.keep": "", "p.py": "#!/usr/bin/python3\n",
					}})
					_, err := f.Exec.StartCommand(context.Background(), workspaceapi.Cmd{Path: tc.path, Dir: f.Path(tc.dir)})
					assert.ErrorIs(t, err, tc.is)
					assert.EqualError(t, err, expander(t, f).Replace(tc.msg))
				})
			}
		})
	}
}

func TestLookPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, dir string
		// want is empty when name must not resolve.
		want string
	}{
		{name: "", want: "{bin}/sh"},
		{name: "ls", want: "{bin}/ls"},
		{name: "bash", want: "{bin}/bash"},
		{name: "echo", want: "{bin}/echo"},
		{name: "which", want: "{bin}/which"},
		{name: "nope"},
		{name: "script.sh"},
		{name: "./script.sh", want: "{home}/script.sh"},
		{name: "./tool", dir: "sub", want: "{home}/sub/tool"},
		{name: "sub/tool", want: "{home}/sub/tool"},
		{name: "{home}/sub/tool", dir: "sub/dir", want: "{home}/sub/tool"},
		{name: "../tool", dir: "sub/dir", want: "{home}/sub/tool"},
		{name: "~/script.sh", want: "{home}/script.sh"},
		{name: "./dir", dir: "sub"},
		{name: "$HOME/script.sh"},
		{name: "./missing"},
	}
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Backend: backend, Files: map[string]string{
				"script.sh":     "echo hi\n",
				"sub/tool":      "echo tool\n",
				"sub/dir/.keep": "",
			}})
			x := expander(t, f)
			for _, tc := range tests {
				t.Run(tc.name+" in "+tc.dir, func(t *testing.T) {
					dir := ""
					if tc.dir != "" {
						dir = f.Path(tc.dir)
					}
					got, err := f.Exec.LookPath(context.Background(), x.Replace(tc.name), dir)
					if tc.want == "" {
						assert.ErrorIs(t, err, fs.ErrNotExist)
						return
					}
					require.NoError(t, err)
					assert.Equal(t, x.Replace(tc.want), got)
				})
			}
		})
	}
}

// step types keys into an interactive shell and expects want to be
// the next output.
type step struct {
	keys, want string
}

const (
	markA  = "\x1b]133;A\x1b\\"
	markB  = "\x1b]133;B\x1b\\"
	markC  = "\x1b]133;C\x1b\\"
	prompt = markA + "$ " + markB
)

func markD(status string) string { return "\x1b]133;D;" + status + "\x1b\\" }

// ran is the output of a line that runs: its echo, the command marks
// around out, and the next prompt.
func ran(line, out, status string) step {
	return step{keys: line + "\r", want: line + "\r\n" + markC + out + markD(status) + prompt}
}

var sessionCases = []struct {
	name  string
	files map[string]string
	steps []step
	code  int
}{
	{name: "state persists across lines",
		files: map[string]string{"docs/a.md": "A\n"},
		steps: []step{
			ran("x=5", "", "0"),
			ran(`f() { echo "f:$x:$1"; }`, "", "0"),
			ran("f one", "f:5:one\r\n", "0"),
			ran("cd docs", "", "0"),
			ran("pwd", "{home}/docs\r\n", "0"),
			ran("cat a.md", "A\r\n", "0"),
			ran("alias ll='ls -l'", "", "0"),
			ran("ll | wc -l", "1\r\n", "0"),
			ran("cd", "", "0"),
			ran("pwd", "{home}\r\n", "0"),
		}},
	{name: "files written on one line are read on the next",
		steps: []step{
			ran("echo hi > f", "", "0"),
			ran("cat f", "hi\r\n", "0"),
			ran("mkdir d && mv f d/ && ls d", "f\r\n", "0"),
			ran("rm -r d; ls d", "ls: d: No such file or directory\r\n", "1"),
		},
		code: 1},
	{name: "errors keep the session",
		steps: []step{
			ran("nope", "sh: nope: command not found\r\n", "127"),
			ran("echo $?", "127\r\n", "0"),
			{keys: "echo )\r", want: "echo )\r\nsh: 1:6: a command can only contain words and redirects; encountered `)`\r\n" + prompt},
			ran("echo $?", "2\r\n", "0"),
			{keys: "sleep 1 &\r", want: "sleep 1 &\r\nsh: 1:1: a background job is not supported\r\n" + prompt},
			ran("false", "", "1"),
		},
		code: 1},
	{name: "a multi-line command",
		steps: []step{
			{keys: "for i in 1 2; do\r", want: "for i in 1 2; do\r\n" + markA + "> " + markB},
			{keys: "echo $i\r", want: "echo $i\r\n" + markA + "> " + markB},
			{keys: "done\r", want: "done\r\n" + markC + "1\r\n2\r\n" + markD("0") + prompt},
		}},
	{name: "a command reads the terminal until EOF",
		steps: []step{
			{keys: "cat > typed\r", want: "cat > typed\r\n" + markC},
			{keys: "one\r", want: "one\r\n"},
			{keys: "two\r\x04", want: "two\r\n" + markD("0") + prompt},
			ran("cat typed", "one\r\ntwo\r\n", "0"),
		}},
	{name: "a script runs on the terminal",
		files: map[string]string{"s.sh": "echo \"script $1\"\nread x\necho \"got $x\"\n"},
		steps: []step{
			{keys: "./s.sh a\r", want: "./s.sh a\r\n" + markC + "script a\r\n"},
			{keys: "typed\r", want: "typed\r\ngot typed\r\n" + markD("0") + prompt},
		}},
	{name: "exit ends the session with its status",
		steps: []step{
			{keys: "exit 3\r", want: "exit 3\r\n" + markC + markD("3")},
		},
		code: 3},
	{name: "Tab completes a path",
		files: map[string]string{"agent/x.md": "X\n", "AGENTS.md": "A\n", "appearance/y.md": "", "config.md": "C\n"},
		steps: []step{
			{keys: "cat c\t", want: "cat c\x1b[5Dcat config.md "},
			{keys: "\r", want: "\r\n" + markC + "C\r\n" + markD("0") + prompt},
			{keys: "cat age\t", want: "cat age\x1b[7Dcat agent/"},
			{keys: "\t", want: "\x1b[10Dcat agent/x.md "},
			{keys: "\r", want: "\r\n" + markC + "X\r\n" + markD("0") + prompt},
		}},
	{name: "Tab lists what it cannot choose between and prompts again",
		files: map[string]string{"agent/x.md": "X\n", "appearance/y.md": "Y\n"},
		steps: []step{
			{keys: "cat a\t", want: "cat a\r\nagent/       appearance/\r\n" + prompt + "cat a"},
			{keys: "p\t", want: "p\x1b[6Dcat appearance/"},
			{keys: "y\t\r", want: "y\x1b[16Dcat appearance/y.md \r\n" + markC + "Y\r\n" + markD("0") + prompt},
		}},
	{name: "Tab lists in the middle of a line and keeps the cursor",
		files: map[string]string{"a1": "1\n", "a2": "2\n"},
		steps: []step{
			{keys: "cat a | wc -l" + strings.Repeat("\x1b[D", 8) + "\t",
				want: "cat a | wc -l" + strings.Repeat("\x1b[D", 8) + "\x1b[8C\r\na1  a2\r\n" +
					prompt + "cat a | wc -l" + strings.Repeat("\x1b[D", 8)},
			{keys: "2\r", want: "2 | wc -l\x1b[8D\x1b[8C\r\n" + markC + "1\r\n" + markD("0") + prompt},
		}},
	{name: "Tab completes a command name",
		steps: []step{
			{keys: "ec\t", want: "ec\x1b[2Decho "},
			{keys: "hi\r", want: "hi\r\n" + markC + "hi\r\n" + markD("0") + prompt},
		}},
	{name: "Tab completes in the directory cd entered",
		files: map[string]string{"docs/guide.md": "G\n"},
		steps: []step{
			ran("cd docs", "", "0"),
			{keys: "cat g\t", want: "cat g\x1b[5Dcat guide.md "},
			{keys: "\r", want: "\r\n" + markC + "G\r\n" + markD("0") + prompt},
		}},
	{name: "Tab escapes what the shell would read otherwise",
		files: map[string]string{"my file.txt": "M\n"},
		steps: []step{
			{keys: "cat my\t", want: "cat my\x1b[6Dcat my\\ file.txt "},
			{keys: "\r", want: "\r\n" + markC + "M\r\n" + markD("0") + prompt},
			{keys: "cat 'my\t", want: "cat 'my\x1b[7Dcat 'my file.txt' "},
			{keys: "\r", want: "\r\n" + markC + "M\r\n" + markD("0") + prompt},
		}},
}

func TestSessions(t *testing.T) {
	t.Parallel()
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range sessionCases {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					s := startSession(t, vshtest.Options{Backend: backend, Files: tc.files})
					x := expander(t, s.f)
					at := len(s.waitFor(0, prompt))
					for _, st := range tc.steps {
						s.typeKeys(st.keys)
						want := x.Replace(st.want)
						got := s.waitFor(at, want)
						at += len(got)
						assert.Equal(t, want, got, "after typing %q", st.keys)
					}
					// ^D ends the session wherever the steps left it.
					s.typeKeys("\x04")
					assert.Equal(t, tc.code, exitCode(t, s.waitExit()))
				})
			}
		})
	}
}

// expander replaces the placeholders of expected text with the
// fixture's values.
func expander(t *testing.T, f *vshtest.Fixture) *strings.Replacer {
	t.Helper()
	info, err := f.Scheme.Stat(f.Path("bin/sh"))
	require.NoError(t, err)
	return strings.NewReplacer(
		"{home}/", strings.TrimSuffix(f.Home, "/")+"/",
		"{home}", f.Home,
		"{homebase}", path.Base(f.Home),
		"{bin}", f.Bin,
		"{shstubsize}", strconv.FormatInt(info.Size(), 10),
	)
}

type result struct {
	stdout, stderr string
	code           int
}

func run(t *testing.T, f *vshtest.Fixture, cmd workspaceapi.Cmd) result {
	t.Helper()
	var out, errb bytes.Buffer
	ch := make(chan error, 1)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	cmd.Watcher = workspaceapi.ChanProcessWatcher(ch)
	_, err := f.Exec.StartCommand(context.Background(), cmd)
	require.NoError(t, err)
	select {
	case err = <-ch:
	case <-time.After(20 * time.Second):
		t.Fatal("command did not exit")
	}
	return result{stdout: out.String(), stderr: errb.String(), code: exitCode(t, err)}
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exit *vsh.ExitError
	require.ErrorAs(t, err, &exit)
	return exit.ExitCode()
}

// session is an interactive sh on a pty, driven from the master side.
type session struct {
	t    *testing.T
	f    *vshtest.Fixture
	pty  workspaceapi.Pty
	exit chan error
	eof  chan struct{}

	mu  sync.Mutex
	out strings.Builder
}

func startSession(t *testing.T, opts vshtest.Options) *session {
	t.Helper()
	f := vshtest.New(t, opts)
	pt, err := f.Exec.NewPty(context.Background())
	require.NoError(t, err)
	s := &session{t: t, f: f, pty: pt, exit: make(chan error, 1), eof: make(chan struct{})}
	_, err = f.Exec.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "sh", Stdin: pt.Slave, Stdout: pt.Slave, Stderr: pt.Slave,
		Watcher: workspaceapi.ChanProcessWatcher(s.exit),
	})
	require.NoError(t, err)
	require.NoError(t, pt.Slave.Close())
	t.Cleanup(func() { _ = pt.Master.Close() })
	go debug.CapturePanicReport(func() {
		buf := make([]byte, 4096)
		for {
			n, err := pt.Master.Read(buf)
			s.mu.Lock()
			s.out.Write(buf[:n])
			s.mu.Unlock()
			if err != nil {
				close(s.eof)
				return
			}
		}
	})
	return s
}

func (s *session) typeKeys(keys string) {
	s.t.Helper()
	_, err := s.pty.Master.Write([]byte(keys))
	require.NoError(s.t, err)
}

func (s *session) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// waitFor blocks until the output after offset from contains substr
// and returns that output up to and including substr.
func (s *session) waitFor(from int, substr string) string {
	s.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		out := s.output()
		if i := strings.Index(out[from:], substr); i >= 0 {
			return out[from : from+i+len(substr)]
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("output never contained %q after offset %d:\n%q", substr, from, out)
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *session) waitExit() error {
	s.t.Helper()
	var err error
	select {
	case err = <-s.exit:
	case <-time.After(20 * time.Second):
		s.t.Fatalf("the shell did not exit:\n%q", s.output())
	}
	select {
	case <-s.eof:
	case <-time.After(20 * time.Second):
		s.t.Fatalf("the master did not read EOF:\n%q", s.output())
	}
	return err
}
