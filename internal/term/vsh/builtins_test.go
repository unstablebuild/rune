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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

func TestLookupBuiltins(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: map[string]string{"plain": "echo\n", "d/.keep": "", "sub/.keep": ""}})
	for _, name := range []string{"s.sh", "other/ls", "other/tool", "other/echo"} {
		seedExec(t, f, name, "echo\n")
	}
	const other = "PATH=/bin:/other"
	tests := []struct {
		name   string
		script string
		env    []string
		dir    string
		stdout string
		stderr string
		code   int
	}{
		{name: "type a file", script: "type ls", stdout: "ls is /bin/ls\n"},
		{name: "type a builtin", script: "type cd", stdout: "cd is a shell builtin\n"},
		{name: "type a builtin with a program", script: "type echo", stdout: "echo is a shell builtin\n"},
		{name: "type a keyword", script: "type if", stdout: "if is a shell keyword\n"},
		{name: "type missing", script: "type nope", stderr: "type: nope: not found\n", code: 1},
		{name: "type mixed found and missing", script: "type ls nope cd",
			stdout: "ls is /bin/ls\ncd is a shell builtin\n", stderr: "type: nope: not found\n", code: 1},
		{name: "type no names", script: "type"},
		{name: "type -t", script: "type -t if cd ls", stdout: "keyword\nbuiltin\nfile\n"},
		{name: "type -t missing is silent", script: "type -t if nope", stdout: "keyword\n", code: 1},
		{name: "type -p a file", script: "type -p ls", stdout: "/bin/ls\n"},
		{name: "type -p a builtin prints nothing", script: "type -p echo cd if"},
		{name: "type -p missing is silent", script: "type -p nope", code: 1},
		{name: "type -P searches the PATH for builtins", script: "type -P echo", stdout: "/bin/echo\n"},
		{name: "type -P without a file", script: "type -P cd if", code: 1},
		{name: "type -P missing is silent", script: "type -P nope", code: 1},
		{name: "type -a lists every match", script: "type -a echo ls", env: []string{other},
			stdout: "echo is a shell builtin\necho is /bin/echo\necho is /other/echo\nls is /bin/ls\nls is /other/ls\n"},
		{name: "type -a missing", script: "type -a nope", stderr: "type: nope: not found\n", code: 1},
		{name: "type -at", script: "type -at echo", stdout: "builtin\nfile\n"},
		{name: "type -ap", script: "type -ap echo", env: []string{other}, stdout: "/bin/echo\n/other/echo\n"},
		{name: "type -aP", script: "type -aP ls", env: []string{other}, stdout: "/bin/ls\n/other/ls\n"},
		{name: "type -f", script: "type -f ls", stdout: "ls is /bin/ls\n"},
		{name: "type -tP: the last of t, p and P wins", script: "type -tP echo", stdout: "/bin/echo\n"},
		{name: "type -pt: the last of t, p and P wins", script: "type -pt ls", stdout: "file\n"},
		{name: "type -Pt still skips builtins", script: "type -Pt echo", stdout: "file\n"},
		{name: "type separate options", script: "type -a -t echo", stdout: "builtin\nfile\n"},
		{name: "type --", script: "type -- ls", stdout: "ls is /bin/ls\n"},
		{name: "type -- then an option-like name", script: "type -t -- -t", code: 1},
		{name: "type options end at the first name", script: "type ls -t",
			stdout: "ls is /bin/ls\n", stderr: "type: -t: not found\n", code: 1},
		{name: "type - is a name", script: "type -", stderr: "type: -: not found\n", code: 1},
		{name: "type an invalid option", script: "type -x ls",
			stderr: "type: -x: invalid option\ntype: usage: type [-afptP] name [name ...]\n", code: 2},
		{name: "type an invalid option among valid ones", script: "type -tz ls",
			stderr: "type: -z: invalid option\ntype: usage: type [-afptP] name [name ...]\n", code: 2},
		{name: "type a slash name keeps the name", script: "type ./s.sh /bin/ls", stdout: "./s.sh is ./s.sh\n/bin/ls is /bin/ls\n"},
		{name: "type a slash name relative to the directory", script: "type ../s.sh", dir: "/sub", stdout: "../s.sh is ../s.sh\n"},
		{name: "type -p a slash name", script: "type -p ./s.sh", stdout: "./s.sh\n"},
		{name: "type -t a slash name", script: "type -t ./s.sh", stdout: "file\n"},
		{name: "type a non-executable slash name", script: "type ./plain", stderr: "type: ./plain: not found\n", code: 1},
		{name: "type a directory", script: "type ./d", stderr: "type: ./d: not found\n", code: 1},
		{name: "type a missing slash name", script: "type ./nope", stderr: "type: ./nope: not found\n", code: 1},
		{name: "type follows PATH", script: "type tool", env: []string{other}, stdout: "tool is /other/tool\n"},
		{name: "type ignores files outside PATH", script: "type tool", stderr: "type: tool: not found\n", code: 1},
		{name: "type skips non-executable files in PATH", script: "type plain", env: []string{"PATH=/:/bin"},
			stderr: "type: plain: not found\n", code: 1},

		{name: "command -v a file", script: "command -v ls", stdout: "/bin/ls\n"},
		{name: "command -v a builtin", script: "command -v cd echo", stdout: "cd\necho\n"},
		{name: "command -v a keyword", script: "command -v if", stdout: "if\n"},
		{name: "command -v missing", script: "command -v nope", code: 1},
		{name: "command -v succeeds when any is found", script: "command -v nope ls", stdout: "/bin/ls\n"},
		{name: "command -v a slash name", script: "command -v ./s.sh", stdout: "./s.sh\n"},
		{name: "command -v a non-executable slash name", script: "command -v ./plain", code: 1},
		{name: "command -v --", script: "command -v -- ls", stdout: "/bin/ls\n"},
		{name: "command -V a file", script: "command -V ls", stdout: "ls is /bin/ls\n"},
		{name: "command -V a builtin and a keyword", script: "command -V cd if",
			stdout: "cd is a shell builtin\nif is a shell keyword\n"},
		{name: "command -V missing", script: "command -V nope", stderr: "command: nope: not found\n", code: 1},
		{name: "command -V succeeds when any is found", script: "command -V nope ls",
			stdout: "ls is /bin/ls\n", stderr: "command: nope: not found\n"},
		{name: "command -vV: the last wins", script: "command -vV ls", stdout: "ls is /bin/ls\n"},
		{name: "command -Vv: the last wins", script: "command -Vv ls", stdout: "/bin/ls\n"},
		{name: "command -pv uses the default PATH", script: "command -pv ls", env: []string{"PATH=/nowhere"}, stdout: "/bin/ls\n"},
		{name: "command -v follows PATH", script: "command -v tool", env: []string{other}, stdout: "/other/tool\n"},
		{name: "command an invalid option", script: "command -vx ls",
			stderr: "command: -x: invalid option\ncommand: usage: command [-pVv] command [arg ...]\n", code: 2},

		{name: "which a file", script: "which ls bash", stdout: "/bin/ls\n/bin/bash\n"},
		{name: "which a builtin program", script: "which echo test [", stdout: "/bin/echo\n/bin/test\n/bin/[\n"},
		{name: "which a builtin without a program", script: "which cd", stderr: "which: no cd in (/bin)\n", code: 1},
		{name: "which a keyword", script: "which if", stderr: "which: no if in (/bin)\n", code: 1},
		{name: "which missing names the PATH", script: "which nope", env: []string{other},
			stderr: "which: no nope in (/bin:/other)\n", code: 1},
		{name: "which mixed found and missing", script: "which nope ls",
			stdout: "/bin/ls\n", stderr: "which: no nope in (/bin)\n", code: 1},
		{name: "which the first match", script: "which ls", env: []string{"PATH=/other:/bin"}, stdout: "/other/ls\n"},
		{name: "which -a every match", script: "which -a ls", env: []string{other}, stdout: "/bin/ls\n/other/ls\n"},
		{name: "which skips other options", script: "which --skip-alias -s ls", stdout: "/bin/ls\n"},
		{name: "which a slash name", script: "which ./s.sh", stdout: "./s.sh\n"},
		{name: "which a non-executable slash name", script: "which ./plain", stderr: "which: no ./plain in (/bin)\n", code: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, exit := run(t, f, workspaceapi.Cmd{Path: "sh", Args: []string{"-c", tc.script}, Env: tc.env, Dir: tc.dir})
			assert.Equal(t, tc.stdout, stdout, "stdout")
			assert.Equal(t, tc.stderr, stderr, "stderr")
			assert.Equal(t, tc.code, exitCode(t, exit), "exit code")
		})
	}
}

func TestLookupBuiltinsAsPrograms(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{})
	tests := []struct {
		argv           []string
		stdout, stderr string
		code           int
	}{
		{argv: []string{"which", "ls", "echo"}, stdout: "/bin/ls\n/bin/echo\n"},
		{argv: []string{"/bin/which", "nope"}, stderr: "which: no nope in (/bin)\n", code: 1},
	}
	for _, tc := range tests {
		t.Run(strings.Join(tc.argv, " "), func(t *testing.T) {
			t.Parallel()
			stdout, stderr, exit := run(t, f, workspaceapi.Cmd{Path: tc.argv[0], Args: tc.argv[1:]})
			assert.Equal(t, tc.stdout, stdout, "stdout")
			assert.Equal(t, tc.stderr, stderr, "stderr")
			assert.Equal(t, tc.code, exitCode(t, exit), "exit code")
		})
	}
}

func TestHelp(t *testing.T) {
	t.Parallel()
	nop := func(name, synopsis string) vsh.Command {
		return vsh.Command{Name: name, Synopsis: synopsis, Run: func(_ context.Context, _ *vsh.Invocation) error { return nil }}
	}
	tests := []struct {
		name     string
		commands []vsh.Command
		// listing is the Commands section.
		listing string
	}{
		{name: "no commands", commands: []vsh.Command{},
			listing: "  which  which NAME...\n  help   help\n"},
		{name: "short names align to which", commands: []vsh.Command{nop("ls", "ls [FILE]..."), nop("cat", "cat [FILE]...")},
			listing: "  cat    cat [FILE]...\n  ls     ls [FILE]...\n  which  which NAME...\n  help   help\n"},
		{name: "a long name widens the column", commands: []vsh.Command{nop("b", "b X"), nop("longname", "longname Y"), nop("a", "a Z")},
			listing: "  a         a Z\n  b         b X\n  longname  longname Y\n  which     which NAME...\n  help      help\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Commands: tc.commands})
			for _, cmd := range []workspaceapi.Cmd{{Path: "help"}, {Path: "sh", Args: []string{"-c", "help"}}} {
				stdout, stderr, exit := run(t, f, cmd)
				assert.NoError(t, exit)
				assert.Empty(t, stderr)
				assert.True(t, strings.HasPrefix(stdout, "Rune built-in shell\n\n"), stdout)
				_, listing, ok := strings.Cut(stdout, "\nCommands:\n")
				assert.True(t, ok, stdout)
				listing, _, ok = strings.Cut(listing, "\nNot available:")
				assert.True(t, ok, stdout)
				assert.Equal(t, tc.listing, listing)
			}
		})
	}
}
