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

package ideauthorizer

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const benchmarkComparisonScript = `go test -c -o /tmp/after.test ./handler/search/ && cd handler/search && for i in 1 2 3; do
  for v in before after; do
    /tmp/$v.test -test.run='^$' -test.bench='BenchmarkDrawRows' -test.benchmem -test.count=4 2>/dev/null | python3 -c "
import sys
for l in sys.stdin:
    if l.startswith('BenchmarkDrawRows'):
        p=l.split(); print('$v', p[0].split('-')[0], p[2], p[6])
"
  done
done | python3 -c "
import sys,collections,statistics
d=collections.defaultdict(list)
for l in sys.stdin:
    v,b,ns,al=l.split(); d[(b,v)].append(int(ns))
for b in ['BenchmarkDrawRowsASCII','BenchmarkDrawRowsWide']:
    be,af=d[(b,'before')],d[(b,'after')]
    print(f'{b:26} before med={statistics.median(be):>8.0f} | after med={statistics.median(af):>8.0f} | {100*(statistics.median(af)/statistics.median(be)-1):+.1f}%')
"`

// describePlan renders the programs of a plan one per line: "~" marks a
// bound program, "?" one whose name is not literal, "builtin" a shell
// builtin, "…" arguments that are not all known and "⚠N" the index of
// the scope argument that carries code.
func describePlan(plan commandPlan) []string {
	if plan.Opaque {
		return []string{"opaque: " + plan.Reason}
	}
	ret := make([]string, 0, len(plan.Programs))
	for _, inv := range plan.Programs {
		var b strings.Builder
		switch {
		case inv.Opaque:
			b.WriteString("?" + inv.Name)
			ret = append(ret, b.String())
			continue
		case inv.Role == roleBound:
			ret = append(ret, "~"+inv.Name)
			continue
		case inv.Shell >= 0:
			b.WriteString("builtin ")
		}
		b.WriteString(strings.Join(append([]string{inv.Name}, inv.Scope...), " "))
		if !inv.Complete {
			b.WriteString(" …")
		}
		if inv.Code >= 0 {
			fmt.Fprintf(&b, " ⚠%d", inv.Code)
		}
		ret = append(ret, b.String())
	}
	return ret
}

func TestPlanCommand(t *testing.T) {
	t.Parallel()

	bash := func(script string) []string { return []string{"/bin/bash", "-c", script} }
	cases := []struct {
		name string
		cmd  []string
		env  []string
		want []string
	}{
		{name: "plain command", cmd: []string{"/usr/bin/grep", "foo"},
			want: []string{"/usr/bin/grep foo"}},
		{name: "shell with a single command", cmd: bash("grep foo file.txt"),
			want: []string{"~/bin/bash", "grep foo file.txt"}},
		{name: "compound and-chain with a nested shell",
			cmd:  bash(`make test && grep "bla" file && bash -c "cd /tmp/abc && rm -rf ."`),
			want: []string{"~/bin/bash", "make test", "grep bla file", "~bash", "rm -rf ."}},
		{name: "pipeline", cmd: bash("grep foo f.txt | wc -l"),
			want: []string{"~/bin/bash", "grep foo f.txt", "wc -l"}},
		{name: "if clause", cmd: bash("if true; then ls a; else grep b file; fi"),
			want: []string{"~/bin/bash", "ls a", "grep b file"}},
		{name: "for clause with a builtin", cmd: bash("for i in a b; do echo x; done"),
			want: []string{"~/bin/bash", "builtin echo x"}},
		{name: "for clause words run their substitutions",
			cmd:  bash("for i in $(curl -s x); do echo $i; done"),
			want: []string{"~/bin/bash", "curl -s x", "builtin echo …"}},
		{name: "subshell", cmd: bash("(echo a; grep b file)"),
			want: []string{"~/bin/bash", "builtin echo a", "grep b file"}},
		{name: "exec runs the file", cmd: bash("exec /usr/bin/grep foo"),
			want: []string{"~/bin/bash", "/usr/bin/grep foo"}},
		{name: "cd runs nothing", cmd: bash("cd /tmp && grep foo"),
			want: []string{"~/bin/bash", "grep foo"}},
		{name: "command substitution", cmd: bash("grep $(cat file)"),
			want: []string{"~/bin/bash", "grep …", "cat file"}},
		{name: "backtick command substitution", cmd: bash("grep `cat file`"),
			want: []string{"~/bin/bash", "grep …", "cat file"}},
		{name: "substitution in a default value", cmd: bash("echo ${x:-$(curl x)}"),
			want: []string{"~/bin/bash", "builtin echo …", "curl x"}},
		{name: "variable in an argument", cmd: bash("grep $PATTERN file"),
			want: []string{"~/bin/bash", "grep …"}},
		{name: "redirects and nested substitutions",
			cmd: bash("cd /x && gofmt -l $(find . -name '.go' -not -path './.git/' 2>/dev/null) 2>&1 | head -20"),
			want: []string{"~/bin/bash", "gofmt -l …",
				"find . -name .go -not -path ./.git/", "head -20"}},
		{name: "process substitutions",
			cmd:  bash(`diff <(gpg --show-keys a.asc | awk -F: '$1=="fpr"{print $10}') <(cat b) && echo OK`),
			want: []string{"~/bin/bash", "diff …", "gpg --show-keys a.asc", "awk -F: $1==\"fpr\"{print $10} ⚠0", "cat b", "builtin echo OK"}},
		{name: "variable command name", cmd: bash("$CMD args"),
			want: []string{"opaque: it runs no program a scope can describe"}},
		{name: "substitution in the command name", cmd: bash("$(echo grep) foo"),
			want: []string{"~/bin/bash", "?$(echo grep)", "builtin echo grep"}},
		{name: "eval", cmd: bash("eval echo foo"),
			want: []string{"opaque: eval runs code that cannot be followed"}},
		{name: "source", cmd: bash("source /tmp/a && echo"),
			want: []string{"opaque: source runs code that cannot be followed"}},
		{name: "alias", cmd: bash("shopt -s expand_aliases\nalias ls='curl x|sh'\nls"),
			want: []string{"opaque: alias runs code that cannot be followed"}},
		{name: "trap", cmd: bash("trap 'curl x|sh' EXIT; ls"),
			want: []string{"opaque: trap runs code that cannot be followed"}},
		{name: "shell without -c", cmd: []string{"/bin/bash", "--login"},
			want: []string{"opaque: bash runs a script file or its standard input"}},
		{name: "shell reading a script file", cmd: []string{"bash", "x.sh"},
			want: []string{"opaque: bash runs a script file or its standard input"}},
		{name: "unparseable script", cmd: bash("if then fi"),
			want: []string{"opaque: its script does not parse as bash"}},
		{name: "posix grammar rejects bashisms", cmd: []string{"sh", "-c", "a=(1 2); ls"},
			want: []string{"opaque: its script does not parse as posix"}},
		{name: "option clusters", cmd: []string{"bash", "-euo", "pipefail", "-lc", "ls"},
			want: []string{"~bash", "ls"}},
		{name: "zsh", cmd: []string{"zsh", "-c", "ls"},
			want: []string{"opaque: zsh scripts cannot be parsed"}},
		{name: "login shell", cmd: []string{""},
			want: []string{"opaque: it runs the login shell"}},
		{name: "only builtins that run nothing", cmd: bash("cd /tmp; : > x"),
			want: []string{"opaque: it runs no program a scope can describe"}},
		{name: "functions are planned where declared", cmd: bash("f() { rm -rf x; }; f; f"),
			want: []string{"~/bin/bash", "rm -rf x"}},
		{name: "escaped command name", cmd: bash(`g\it status`),
			want: []string{"~/bin/bash", "git status"}},
		{name: "globs are not literal", cmd: bash("rm -f *.go"),
			want: []string{"~/bin/bash", "rm -f …"}},
		{name: "brace expansions are not literal", cmd: bash("git {status,push}"),
			want: []string{"~/bin/bash", "git …"}},
		{name: "find placeholders are not known until it runs", cmd: bash("find . -exec rm {} \\;"),
			want: []string{"~/bin/bash", "find . -exec rm {} ;", "rm …"}},
		{name: "find placeholder in a script",
			cmd: []string{"find", ".", "-exec", "sh", "-c", "cat {}", ";"},
			want: []string{"opaque: find puts the names of the files it finds in the command " +
				"it runs, where they can run as code"}},
		{name: "find placeholder passed to a script",
			cmd:  []string{"find", ".", "-exec", "sh", "-c", `cat "$1"`, "sh", "{}", ";"},
			want: []string{"find . -exec sh -c cat \"$1\" sh {} ;", "~sh", "cat …"}},
		{name: "find placeholder as the command",
			cmd:  []string{"find", ".", "-name", "*.sh", "-exec", "{}", ";"},
			want: []string{"find . -name *.sh -exec {} ;", "?{}"}},
		{name: "find placeholder next to what cannot be followed anyway",
			cmd:  []string{"find", ".", "-exec", "env", "-i", "{}", ";"},
			want: []string{"opaque: env -i runs a command that cannot be followed"}},
		{name: "fd placeholder in a script", cmd: []string{"fd", "-x", "sh", "-c", "cat {/}"},
			want: []string{"opaque: fd puts the paths it finds in the command it runs, " +
				"where they can run as code"}},
		{name: "xargs replace string in a script", cmd: bash(`ls | xargs -I % sh -c "cat %"`),
			want: []string{"opaque: xargs puts the lines it reads in the command it runs, " +
				"where they can run as code"}},
		{name: "xargs default replace string", cmd: bash(`ls | xargs --replace sh -c "cat {}"`),
			want: []string{"opaque: xargs puts the lines it reads in the command it runs, " +
				"where they can run as code"}},
		{name: "xargs replace string in the command name", cmd: bash("ls | xargs -I e make"),
			want: []string{"~/bin/bash", "ls", "~xargs", "?make"}},
		{name: "ansi-c escapes in a command name", cmd: bash(`$'\x72m' -rf x`),
			want: []string{"opaque: it runs no program a scope can describe"}},

		{name: "git global options", cmd: []string{"git", "-C", "x", "-c", "k=v", "commit", "-m", "fix"},
			want: []string{"git -c k=v commit -m fix ⚠0"}},
		{name: "git repository options", cmd: []string{"git", "--git-dir=x", "-P", "status"},
			want: []string{"git status"}},
		{name: "sudo", cmd: []string{"sudo", "-u", "root", "apt", "install", "x"},
			want: []string{"sudo -u root apt install x ⚠0", "~apt"}},
		{name: "sudo shell", cmd: []string{"sudo", "-s"},
			want: []string{"opaque: sudo -s runs a shell"}},
		{name: "sudo without a command", cmd: []string{"doas"},
			want: []string{"opaque: doas without a command runs a shell"}},
		{name: "sudo dangerous env", cmd: []string{"sudo", "LD_PRELOAD=x", "ls"},
			want: []string{"opaque: it sets LD_PRELOAD"}},
		{name: "find exec", cmd: []string{"find", ".", "-exec", "rm", "{}", "+"},
			want: []string{"find . -exec rm {} +", "rm …"}},
		{name: "find without terminator", cmd: []string{"find", ".", "-exec", "rm", "{}"},
			want: []string{"opaque: find runs commands that cannot be followed"}},
		{name: "find with a variable", cmd: bash("find $D -exec rm {} +"),
			want: []string{"opaque: find runs commands that cannot be followed"}},
		{name: "fd exec", cmd: []string{"fd", "-e", "go", "-x", "cat"},
			want: []string{"fd -e go -x cat", "cat …"}},
		{name: "flock", cmd: []string{"flock", "/tmp/l", "make", "all"},
			want: []string{"flock /tmp/l make all", "make all"}},
		{name: "flock command string", cmd: []string{"flock", "/tmp/l", "-c", "make"},
			want: []string{"opaque: flock runs commands that cannot be followed"}},
		{name: "xargs", cmd: bash("ls | xargs -0 rm"),
			want: []string{"~/bin/bash", "ls", "~xargs", "rm …"}},
		{name: "xargs without a command", cmd: []string{"xargs", "-n1"},
			want: []string{"~xargs", "echo …"}},
		{name: "timeout", cmd: []string{"timeout", "-s", "KILL", "5", "go", "test", "./..."},
			want: []string{"~timeout", "go test ./..."}},
		{name: "timeout abbreviated option", cmd: []string{"timeout", "--sig=KILL", "5", "ls"},
			want: []string{"opaque: the options of timeout cannot be followed"}},
		{name: "nice", cmd: []string{"nice", "-n", "5", "make"},
			want: []string{"~nice", "make"}},
		{name: "nohup env time", cmd: []string{"nohup", "env", "FOO=1", "time", "-p", "make"},
			want: []string{"~nohup", "~env", "~time", "make"}},
		{name: "env split string", cmd: []string{"env", "-S", "make test"},
			want: []string{"opaque: env -S runs a command that cannot be followed"}},
		{name: "env clears the environment", cmd: []string{"env", "-", "make"},
			want: []string{"opaque: env - clears the environment"}},
		{name: "env sets a dangerous variable", cmd: []string{"env", "LD_PRELOAD=x", "ls"},
			want: []string{"opaque: it sets LD_PRELOAD"}},
		{name: "env unsets PATH", cmd: []string{"env", "-u", "PATH", "ls"},
			want: []string{"opaque: it sets PATH"}},
		{name: "env exports a function",
			cmd:  []string{"env", "BASH_FUNC_git%%=() { id; }", "bash", "-c", "git status"},
			want: []string{"opaque: it sets BASH_FUNC_git%%"}},
		{name: "strace env", cmd: []string{"strace", "-f", "-E", "LD_PRELOAD=x", "ls"},
			want: []string{"opaque: it sets LD_PRELOAD"}},
		{name: "perf", cmd: []string{"perf", "stat", "-e", "cycles", "--", "make"},
			want: []string{"~perf", "make"}},
		{name: "perf without --", cmd: []string{"perf", "top"},
			want: []string{"opaque: the options of perf cannot be followed"}},
		{name: "command -v runs nothing", cmd: bash("command -v git && git status"),
			want: []string{"~/bin/bash", "git status"}},
		{name: "command bypasses functions", cmd: bash("git() { echo; }; command git log"),
			want: []string{"~/bin/bash", "builtin echo", "git log"}},
		{name: "dangerous shell assignment", cmd: bash("LD_PRELOAD=x ls"),
			want: []string{"opaque: it sets LD_PRELOAD"}},
		{name: "dangerous export", cmd: bash("export LD_PRELOAD=x; ls"),
			want: []string{"opaque: it sets LD_PRELOAD"}},
		{name: "dangerous posix export", cmd: []string{"sh", "-c", "export PS4='$(id)'; set -x; ls"},
			want: []string{"opaque: it sets PS4"}},
		{name: "dangerous loop variable", cmd: bash("for PATH in /tmp; do ls; done"),
			want: []string{"opaque: it sets PATH"}},
		{name: "dangerous read", cmd: bash(`read -p "x: " PATH; ls`),
			want: []string{"opaque: it sets PATH"}},
		{name: "harmless read", cmd: bash(`read -r -p "x: " line; ls`),
			want: []string{"~/bin/bash", "builtin read -r -p x:  line", "ls"}},
		{name: "dangerous printf", cmd: bash(`printf -v PATH %s /tmp; ls`),
			want: []string{"opaque: it sets PATH"}},
		{name: "nameref", cmd: bash(`declare -n r=PATH; r=/tmp; ls`),
			want: []string{"opaque: namerefs hide the variables they set"}},
		{name: "mapfile callback", cmd: bash(`mapfile -C 'rm -rf x' -c 1 a < f`),
			want: []string{"opaque: mapfile -C runs code that cannot be followed"}},
		{name: "prompt expansion", cmd: bash(`x='$(id)'; echo ${x@P}`),
			want: []string{"opaque: prompt expansions run command substitutions"}},
		{name: "arithmetic on a string holding a substitution",
			cmd:  bash(`x='a[$(id)]'; echo $((x))`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "dangerous command environment", cmd: []string{"git", "status"},
			env:  []string{"LD_PRELOAD=/tmp/x.so"},
			want: []string{"opaque: it sets LD_PRELOAD"}},
		{name: "command PATH is followed", cmd: []string{"git", "status"},
			env:  []string{"PATH=/opt/bin"},
			want: []string{"git status"}},
		{name: "ssh remote command", cmd: []string{"ssh", "-p", "22", "host", "ls"},
			want: []string{"opaque: ssh runs a command on another host"}},
		{name: "ssh session", cmd: []string{"ssh", "host"},
			want: []string{"ssh host"}},
		{name: "python code", cmd: []string{"python3", "-c", "print(1)"},
			want: []string{"python3 -c print(1) ⚠0"}},
		{name: "versioned python cluster", cmd: []string{"python3.12", "-Bc", "print(1)"},
			want: []string{"python3.12 -Bc print(1) ⚠0"}},
		{name: "python script", cmd: []string{"python3", "script.py", "-c"},
			want: []string{"python3 script.py -c"}},
		{name: "python reading stdin", cmd: []string{"python3"},
			want: []string{"python3 … ⚠0"}},
		{name: "python heredoc", cmd: bash("python3 <<'PY'\nprint(1)\nPY"),
			want: []string{"~/bin/bash", "python3 … ⚠0"}},
		{name: "perl cluster", cmd: []string{"perl", "-ne", "print", "f"},
			want: []string{"perl -ne print f ⚠0"}},
		{name: "sed", cmd: []string{"sed", "-n", "1p", "f"},
			want: []string{"sed -n 1p f ⚠0"}},
		{name: "make file", cmd: []string{"make", "test", "-f", "x.mk"},
			want: []string{"make test -f x.mk ⚠1"}},
		{name: "docker run", cmd: []string{"docker", "run", "--rm", "alpine"},
			want: []string{"docker run --rm alpine ⚠0"}},
		{name: "docker run in a shell", cmd: bash(`docker run --rm -v "$PWD:/w" alpine sh -c 'ls /w'`),
			want: []string{"~/bin/bash", "docker run --rm -v … ⚠0"}},
		{name: "docker run after global options",
			cmd:  []string{"docker", "--context", "ci", "run", "alpine"},
			want: []string{"docker --context ci run alpine ⚠2"}},
		{name: "docker subcommand not known until it runs", cmd: bash(`docker "$SUB" alpine`),
			want: []string{"opaque: the subcommand of docker is not known until it runs"}},
		{name: "kubectl exec", cmd: []string{"kubectl", "exec", "pod", "--", "ls"},
			want: []string{"kubectl exec pod -- ls ⚠0"}},
		{name: "nix develop", cmd: []string{"nix", "develop"},
			want: []string{"nix develop ⚠0"}},
		{name: "docker ps", cmd: []string{"docker", "ps"},
			want: []string{"docker ps"}},
		{name: "busybox", cmd: []string{"busybox", "sh", "-c", "x"},
			want: []string{"busybox sh -c x ⚠0"}},
		{name: "parallel", cmd: []string{"parallel", "rm", ":::", "a"},
			want: []string{"opaque: parallel runs its command through a shell"}},
		{name: "perl script with a backtick substitution",
			cmd:  []string{"/usr/bin/perl", "-e", "print `git rev-parse --show-toplevel`;"},
			want: []string{"/usr/bin/perl -e print `git rev-parse --show-toplevel`; ⚠0"}},
		{name: "backtick substitution in an argument",
			cmd:  bash("echo `git rev-parse --show-toplevel`"),
			want: []string{"~/bin/bash", "builtin echo …", "git rev-parse --show-toplevel"}},
		{name: "nested substitutions in an argument", cmd: bash("printf %s $(dirname $(which go))"),
			want: []string{"~/bin/bash", "builtin printf %s …", "dirname …", "which go"}},
		{name: "backtick command name", cmd: bash("`echo grep` foo file.txt"),
			want: []string{"~/bin/bash", "?$(echo grep)", "builtin echo grep"}},
		{name: "sequence with nested substitutions",
			cmd: bash("cd /tmp && echo `git rev-parse --show-toplevel` && " +
				"printf %s $(dirname $(which go)) | wc -c"),
			want: []string{"~/bin/bash", "builtin echo …", "git rev-parse --show-toplevel",
				"builtin printf %s …", "dirname …", "which go", "wc -c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := planCommand(tc.cmd[0], tc.cmd[1:], tc.env)
			assert.Equal(t, tc.want, describePlan(plan))
		})
	}
}

func TestPlanCommandBenchmarkComparisonScript(t *testing.T) {
	t.Parallel()

	plan := planCommand("/bin/bash", []string{"-c", benchmarkComparisonScript}, nil)
	got := describePlan(plan)
	require.Len(t, got, 5, got)
	assert.Equal(t, "~/bin/bash", got[0])
	assert.Equal(t, "go test -c -o /tmp/after.test ./handler/search/", got[1])
	assert.Equal(t, "?/tmp/$v.test", got[2])
	assert.Equal(t, "python3 -c … ⚠0", got[3])
	assert.True(t, strings.HasPrefix(got[4], "python3 -c \nimport sys,collections"), got[4])
	assert.True(t, strings.HasSuffix(got[4], " ⚠0"), got[4])
	assert.True(t, plan.DirChanged)
}

func TestPlanCommandDirChanged(t *testing.T) {
	t.Parallel()

	const findDir = "find runs it in the directory of each file it finds"
	const fdDir = "fd runs it in its base directory"
	cases := []struct {
		cmd         []string
		wantChanged bool
		// wantDir is the Dir of the last program.
		wantDir string
	}{
		{cmd: []string{"bash", "-c", "./run"}},
		{cmd: []string{"bash", "-c", "cd /tmp && ./run"}, wantChanged: true},
		{cmd: []string{"bash", "-c", "pushd /tmp; ls"}, wantChanged: true},
		{cmd: []string{"find", ".", "-exec", "./run", ";"}},
		{cmd: []string{"find", ".", "-execdir", "./run", ";"}, wantDir: findDir},
		{cmd: []string{"find", ".", "-okdir", "make", ";"}, wantDir: findDir},
		{cmd: []string{"find", ".", "-execdir", "sh", "-c", "./run", ";"}, wantDir: findDir},
		{cmd: []string{"fd", "--base-directory", "x", "-x", "./run"}, wantDir: fdDir},
		{cmd: []string{"fd", "--base-directory=x", "-x", "./run"}, wantDir: fdDir},
	}
	for _, tc := range cases {
		plan := planCommand(tc.cmd[0], tc.cmd[1:], nil)
		require.False(t, plan.Opaque, tc.cmd)
		assert.Equal(t, tc.wantChanged, plan.DirChanged, tc.cmd)
		assert.Equal(t, tc.wantDir, plan.Programs[len(plan.Programs)-1].Dir, tc.cmd)
		assert.Empty(t, plan.Programs[0].Dir, "%v runs in the directory of the command", tc.cmd)
	}
}

func TestPlanCommandFirst(t *testing.T) {
	t.Parallel()

	cases := []struct {
		cmd []string
		// want are the programs that run before anything else the command
		// runs could create them.
		want []string
	}{
		{cmd: []string{"timeout", "5", "go", "test"}, want: []string{"timeout"}},
		{cmd: []string{"bash", "-c", "timeout 300 go test > /tmp/log 2>&1; echo $?"},
			want: []string{"bash", "timeout"}},
		{cmd: []string{"bash", "-c", "bash -c 'tool run'"}, want: []string{"bash", "bash", "tool"}},
		{cmd: []string{"bash", "-c", "tool <<'EOF'\n$(x)\nEOF"}, want: []string{"bash", "tool"}},
		{cmd: []string{"bash", "-c", "make install && tool run"}, want: []string{"bash"}},
		{cmd: []string{"bash", "-c", "make install; tool run"}, want: []string{"bash", "make"}},
		{cmd: []string{"bash", "-c", "make | tool"}, want: []string{"bash"}},
		{cmd: []string{"bash", "-c", "tool $(make)"}, want: []string{"bash"}},
		{cmd: []string{"bash", "-c", "tool > $(make)"}, want: []string{"bash"}},
		{cmd: []string{"bash", "-c", "tool <<EOF\n$(make)\nEOF"}, want: []string{"bash"}},
		{cmd: []string{"bash", "-c", "tool < <(make)"}, want: []string{"bash"}},
		{cmd: []string{"bash", "-c", "X=$(make) tool"}, want: []string{"bash"}},
		{cmd: []string{"bash", "-c", "f() { tool; }; f"}, want: []string{"bash"}},
		{cmd: []string{"nice", "tool"}, want: []string{"nice"}},
	}
	for _, tc := range cases {
		plan := planCommand(tc.cmd[0], tc.cmd[1:], nil)
		require.False(t, plan.Opaque, tc.cmd)
		var got []string
		for _, inv := range plan.Programs {
			if inv.First {
				got = append(got, inv.Name)
			}
		}
		assert.Equal(t, tc.want, got, tc.cmd)
	}
}
