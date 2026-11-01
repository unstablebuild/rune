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
			want: []string{"~zsh", "ls"}},
		{name: "fish", cmd: []string{"fish", "-c", "ls"},
			want: []string{"opaque: fish scripts cannot be parsed"}},
		{name: "login shell", cmd: []string{""},
			want: []string{"opaque: it runs the login shell"}},
		{name: "only builtins that run nothing", cmd: bash("cd /tmp; : > x"),
			want: []string{"opaque: it runs no program a scope can describe"}},
		{name: "functions are planned where declared", cmd: bash("f() { rm -rf x; }; f; f"),
			want: []string{"~/bin/bash", "rm -rf x"}},
		{name: "a call before the function is declared runs the program",
			cmd:  bash("git status; git() { :; }; git log"),
			want: []string{"~/bin/bash", "git status"}},
		{name: "a function declared in a branch may not be declared",
			cmd:  bash("if false; then make() { :; }; fi; make x"),
			want: []string{"~/bin/bash", "make x"}},
		{name: "unset -f makes later calls run the program",
			cmd:  bash("make() { :; }; unset -f make; make y"),
			want: []string{"opaque: unsetting a function makes later calls of it run a program"}},
		{name: "unset of a function makes later calls run the program",
			cmd:  bash("make() { :; }; unset make; make y"),
			want: []string{"opaque: unsetting a function makes later calls of it run a program"}},
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
		{name: "arithmetic of a for loop", cmd: bash(`x='a[$(id)]'; for ((i = x; i < 0; i++)); do :; done; ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic of an array subscript", cmd: bash(`x='a[$(id)]'; b[x]=1; ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic of an array element subscript", cmd: bash(`x='a[$(id)]'; b=([x]=1); ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic of an assignment to an integer variable", cmd: bash(`x='a[$(id)]'; OPTIND=x; ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on an argument of the script",
			cmd:  []string{"/bin/bash", "-c", "echo $(($1)); ls", "_", "a[$(id)]"},
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on a variable of the command environment",
			cmd: bash("echo $((x)); ls"), env: []string{"x=a[$(id)]"},
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on the output of a command", cmd: bash(`x=$(cat a); echo $((x)); ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on what read reads", cmd: bash(`read x < a; [[ $x -eq 1 ]]; ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on what mapfile reads", cmd: bash(`mapfile -t l < a; (( l[0] )); ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "conditions on the output of a command",
			cmd: bash(`[ -n "$(git status --porcelain)" ] && [[ -f $(git rev-parse --show-toplevel)/go.mod ]] && go test`),
			want: []string{"~/bin/bash", "builtin [ -n …", "git status --porcelain",
				"git rev-parse --show-toplevel", "go test"}},
		{name: "numbers in the arithmetic of a script that reads data",
			cmd:  bash(`while read -r l; do ls "$l"; done < a; [ $? -ne 0 ] && exit 1; echo $((1 + 2)) ${l:0:3}`),
			want: []string{"~/bin/bash", "builtin read -r l", "ls …", "builtin [ …", "builtin echo …"}},
		{name: "dangerous default assignment", cmd: bash(`: ${LD_PRELOAD:=/tmp/x.so}; ls`),
			want: []string{"opaque: it sets LD_PRELOAD"}},
		{name: "dangerous assignment of an unset variable", cmd: bash(`: ${PATH=/tmp}; ls`),
			want: []string{"opaque: it sets PATH"}},
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
			plan := planCommand(tc.cmd[0], tc.cmd[1:], tc.env, nil)
			assert.Equal(t, tc.want, describePlan(plan))
		})
	}
}

// zshArithmetic are the places zsh evaluates x as arithmetic, which runs
// the command substitutions of the array subscripts in its value.
var zshArithmetic = []string{
	"echo $((x))", "echo $[x]", "(( x ))", "let x", "[[ x -eq 1 ]]", "echo ${a[x]}",
	`echo "$[x]"`, "echo $a[x]", "b[x]=1", "for ((i = x; i < 0; i++)); do :; done",
	"integer n; n=x",
	"typeset -i n=x", "shift x", `printf "%d" x`, "exit x", "f() { return x }; f",
	"while :; do break x; done", "SECONDS=x", "RANDOM=x", "SHLVL=x", "HISTSIZE=x",
	"COLUMNS=x", "ERRNO=x", "SECONDS+=x", "for LINES in x; do :; done",
	"unset LINES; : ${LINES:=x}", "OPTIND=x true", "cat <<E\n$[x]\nE", "echo a$[x]b",
	"[[ -t x ]]", "test -t x", "[[ -v 'a[x]' ]]", "print -f '%d' x", "printf '%*s' x a",
	"printf '%f' x", "typeset -E y=x", "float y=x", "typeset 'b[x]'=1", "echo ${a[(e)x]}",
	"b=(1); unset 'b[x]'",
}

func TestPlanCommandZshArithmetic(t *testing.T) {
	t.Parallel()

	for _, ctx := range zshArithmetic {
		t.Run(ctx, func(t *testing.T) {
			t.Parallel()
			plan := planCommand("/bin/zsh", []string{"-c", "x='a[$(id)]'\n" + ctx + "\nls"}, nil, nil)
			assert.Equal(t, []string{
				"opaque: its arithmetic may run command substitutions hidden in strings",
			}, describePlan(plan))
			plan = planCommand("/bin/zsh", []string{"-c", "x=1\n" + ctx + "\nls"}, nil, nil)
			assert.False(t, plan.Opaque, plan.Reason)
		})
	}
}

func TestPlanCommandRecursiveDeletes(t *testing.T) {
	t.Parallel()

	bash := func(script string) []string { return []string{"/bin/bash", "-c", script} }
	local := func(name string) (string, bool, bool) {
		v, ok := map[string]string{"RUNE_DATADIR": "/home/me/.rune", "HOME": "/home/me"}[name]
		return v, ok, true
	}
	remote := func(name string) (string, bool, bool) {
		if name == "RUNE_DATADIR" {
			return "/remote/.rune", true, true
		}
		return "", false, false
	}
	const unresolved = " (unresolved)"
	cases := []struct {
		name string
		cmd  []string
		env  envLookup
		want []string
	}{
		{name: "the script sets the variable first",
			cmd:  bash(`export RUNE_DATADIR=/tmp/rs-test; rm -rf $RUNE_DATADIR`),
			want: []string{"rm -rf /tmp/rs-test"}},
		{name: "the variable of the host",
			cmd:  bash(`rm -rf "$RUNE_DATADIR"`),
			want: []string{"rm -rf /home/me/.rune"}},
		{name: "an unset variable expands to nothing",
			cmd:  bash(`rm -rf "$BUILD_DIR/"`),
			want: []string{"rm -rf /"}},
		{name: "a tilde", cmd: bash(`rm -r ~/x`), want: []string{"rm -r /home/me/x"}},
		{name: "options after operands", cmd: bash(`rm x -R`), want: []string{"rm x -R"}},
		{name: "long option", cmd: bash(`rm --recursive x`), want: []string{"rm --recursive x"}},
		{name: "not recursive", cmd: bash(`rm -f x; rm -- -r`)},
		{name: "quoted when the shell would need it", cmd: bash(`rm -rf "/tmp/a b" ''`),
			want: []string{`rm -rf '/tmp/a b' ''`}},
		{name: "an assignment only for the command does not apply to its words",
			cmd: bash(`D=/a rm -rf /x$D`), want: []string{"rm -rf /x"}},
		{name: "an assignment that may not run",
			cmd:  bash(`D=/a; test -n "$X" && D=/b; rm -rf $D`),
			want: []string{"rm -rf $D" + unresolved}},
		{name: "a command substitution",
			cmd:  bash(`rm -rf "$(pwd)/x"`),
			want: []string{`rm -rf "$(pwd)/x"` + unresolved}},
		{name: "a variable a command substitution sets",
			cmd:  bash(`D=$(mktemp -d); rm -rf $D`),
			want: []string{"rm -rf $D" + unresolved}},
		{name: "a loop variable",
			cmd:  bash(`for d in a b; do rm -rf "$d"; done`),
			want: []string{`rm -rf "$d"` + unresolved}},
		{name: "a function runs after the assignments that follow it",
			cmd:  bash(`f() { rm -rf $D; }; D=/a; f`),
			want: []string{"rm -rf $D" + unresolved}},
		{name: "a nested shell sees only what is exported",
			cmd:  bash(`D=/a; bash -c 'rm -rf "$D"'`),
			want: []string{`rm -rf "$D"` + unresolved}},
		{name: "behind a wrapper", cmd: []string{"sudo", "rm", "-rf", "/x"},
			want: []string{"rm -rf /x"}},
		{name: "what find finds",
			cmd:  bash(`find . -name build -exec rm -rf {} +`),
			want: []string{"rm -rf {}" + unresolved}},
		{name: "the directory of the shell", cmd: bash(`rm -rf "$PWD/x"`),
			want: []string{`rm -rf "$PWD/x"` + unresolved}},
		{name: "what xargs reads",
			cmd:  bash(`xargs rm -r < list`),
			want: []string{"rm -r …" + unresolved}},
		{name: "remote host data dir", env: remote,
			cmd:  bash(`rm -rf $RUNE_DATADIR/cache`),
			want: []string{"rm -rf /remote/.rune/cache"}},
		{name: "remote host variable", env: remote,
			cmd:  bash(`rm -rf $HOME/x`),
			want: []string{"rm -rf $HOME/x" + unresolved}},
		{name: "zsh literal words", cmd: []string{"zsh", "-c", "rm -rf /tmp/x"},
			want: []string{"rm -rf /tmp/x"}},
		{name: "zsh expands variables unlike bash",
			cmd:  []string{"zsh", "-c", `D=/a; rm -rf $D`},
			want: []string{"rm -rf $D" + unresolved}},
		{name: "zsh tildes depend on HOME, which the script may set",
			cmd:  []string{"zsh", "-c", `rm -rf ~/x`},
			want: []string{"rm -rf ~/x" + unresolved}},
		{name: "rmdir", cmd: bash(`rmdir "$RUNE_DATADIR/x" ~/y`),
			want: []string{"rmdir /home/me/.rune/x /home/me/y"}},
		{name: "rmdir of the parents", cmd: bash(`D=a/b; rmdir -p "$D"`),
			want: []string{"rmdir -p a/b"}},
		{name: "rmdir by path", cmd: bash(`/bin/rmdir x`), want: []string{"/bin/rmdir x"}},
		{name: "rmdir behind a wrapper", cmd: []string{"sudo", "rmdir", "/x"},
			want: []string{"rmdir /x"}},
		{name: "what find runs rmdir on",
			cmd:  bash(`find . -type d -empty -exec rmdir {} +`),
			want: []string{"rmdir {}" + unresolved}},
		{name: "rm and rmdir", cmd: bash(`rm -r a/b; rmdir a`),
			want: []string{"rm -r a/b", "rmdir a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := tc.env
			if env == nil {
				env = local
			}
			plan := planCommand(tc.cmd[0], tc.cmd[1:], nil, env)
			var got []string
			for _, d := range plan.Deletes {
				if d.Unresolved {
					d.Command += unresolved
				}
				got = append(got, d.Command)
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPlanCommandZsh(t *testing.T) {
	t.Parallel()

	zsh := func(script string) []string { return []string{"/bin/zsh", "-c", script} }
	cases := []struct {
		name string
		cmd  []string
		env  []string
		want []string
	}{
		{name: "fuzzy search", cmd: zsh(`rg --color never -n --no-heading --max-columns 500 ""`),
			want: []string{"~/bin/zsh", "rg --color never -n --no-heading --max-columns 500 "}},
		{name: "lists and pipelines", cmd: zsh("git status && rg foo | head -5; make &|"),
			want: []string{"~/bin/zsh", "git status", "rg foo", "head -5", "make"}},
		{name: "option clusters", cmd: []string{"zsh", "-fec", "ls"},
			want: []string{"~zsh", "ls"}},
		{name: "options that change the grammar", cmd: []string{"zsh", "-o", "globsubst", "-c", "ls"},
			want: []string{"opaque: the options of zsh cannot be followed"}},
		{name: "sh word splitting option", cmd: []string{"zsh", "-yc", "ls"},
			want: []string{"opaque: the options of zsh cannot be followed"}},
		{name: "script file", cmd: []string{"zsh", "x.zsh"},
			want: []string{"opaque: zsh runs a script file or its standard input"}},
		{name: "syntax the parser does not support", cmd: zsh("{ ls } always { rm x }"),
			want: []string{"opaque: its script does not parse as zsh"}},

		{name: "glob qualifier", cmd: zsh(`ls *(e:'rm -rf ~':)`),
			want: []string{"opaque: zsh glob qualifiers can run code"}},
		{name: "glob qualifier on a word without wildcards", cmd: zsh(`ls f(e:'id':)`),
			want: []string{"opaque: zsh glob qualifiers can run code"}},
		{name: "glob qualifier in a redirection", cmd: zsh(`ls > *(e:'id':)`),
			want: []string{"opaque: zsh glob qualifiers can run code"}},
		{name: "conditions match patterns, which have no qualifiers",
			cmd:  zsh(`[[ $x == (a|b) && -e *(e:'id':) ]] && ls`),
			want: []string{"~/bin/zsh", "ls"}},
		{name: "case patterns have no qualifiers", cmd: zsh(`case $x in *(e:'id':)) ls ;; esac`),
			want: []string{"~/bin/zsh", "ls"}},
		{name: "glob qualifier in a substitution of a condition",
			cmd:  zsh(`[[ -n $(ls *(e:'id':)) ]]`),
			want: []string{"opaque: zsh glob qualifiers can run code"}},
		{name: "glob qualifier in a case body", cmd: zsh(`case $x in a) ls *(e:'id':) ;; esac`),
			want: []string{"opaque: zsh glob qualifiers can run code"}},
		{name: "quoted and escaped parentheses", cmd: zsh(`git log 'HEAD^' "a(b)" c\(d\)`),
			want: []string{"~/bin/zsh", "git log HEAD^ a(b) c(d)"}},
		{name: "parentheses in a here-document", cmd: zsh("cat <<EOF\nf(x) $(git log)\nEOF"),
			want: []string{"~/bin/zsh", "cat", "git log"}},
		{name: "parameter flags", cmd: zsh(`echo ${(e)x}`),
			want: []string{"opaque: zsh parameter flags can run code"}},
		{name: "glob substitution", cmd: zsh(`ls ${~x}`),
			want: []string{"opaque: ${~…} globs values, whose qualifiers can run code"}},
		{name: "arithmetic on a string holding a substitution",
			cmd:  zsh(`x='a[$(id)]'; echo $((x))`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "redirection without a command", cmd: zsh(`< f`),
			want: []string{"opaque: zsh runs a program for a redirection without a command"}},
		{name: "output redirection without a command", cmd: zsh(`ls; > f`),
			want: []string{"opaque: zsh runs a program for a redirection without a command"}},
		{name: "substitution of a file's contents", cmd: zsh("x=$(<a); y=`< b`; cat $(0<c;) $x $y"),
			want: []string{"~/bin/zsh", "cat …"}},
		{name: "file substitution that also redirects output", cmd: zsh(`cat $(<a 2>/dev/null)`),
			want: []string{"opaque: zsh runs a program for a redirection without a command"}},
		{name: "file substitution with two inputs", cmd: zsh(`cat $(<a <b)`),
			want: []string{"opaque: zsh runs a program for a redirection without a command"}},
		{name: "file substitution with an assignment", cmd: zsh(`cat $(x=1 <a)`),
			want: []string{"opaque: zsh runs a program for a redirection without a command"}},
		{name: "file substitution of a second statement", cmd: zsh(`cat $(:; <a)`),
			want: []string{"opaque: zsh runs a program for a redirection without a command"}},
		{name: "substitution of the file a substitution names", cmd: zsh(`cat $(<$(git ls-files))`),
			want: []string{"~/bin/zsh", "cat …", "git ls-files"}},

		{name: "zsh expansions are not literal", cmd: zsh(`ls =rm a~b ^c d# <1-9> {a-z}`),
			want: []string{"~/bin/zsh", "ls …"}},
		{name: "old arithmetic the parser takes for literals", cmd: zsh(`ls "$[1+2]"; ls $[1+2]`),
			want: []string{"~/bin/zsh", "ls …", "ls …"}},
		{name: "equals expansion in the command name", cmd: zsh(`=rm -rf x`),
			want: []string{"opaque: it runs no program a scope can describe"}},
		{name: "empty braces", cmd: zsh(`find . -exec rm {} \;`),
			want: []string{"~/bin/zsh", "find . -exec rm {} ;", "rm …"}},
		{name: "tildes depend on HOME, which the script may set", cmd: zsh(`HOME=/etc; ls ~/x ~+`),
			want: []string{"~/bin/zsh", "ls …"}},
		{name: "split and set expansions", cmd: zsh(`ls $=x ${+y}`),
			want: []string{"~/bin/zsh", "ls …"}},
		{name: "process substitutions", cmd: zsh(`diff =(ls a) <(ls b)`),
			want: []string{"~/bin/zsh", "diff …", "ls a", "ls b"}},
		{name: "functions are planned where declared", cmd: zsh(`f() { rm -rf x }; f; () { git status }`),
			want: []string{"~/bin/zsh", "rm -rf x", "git status"}},
		{name: "a call before the function is declared runs the program",
			cmd:  zsh("git status; git() { :; }; git log"),
			want: []string{"~/bin/zsh", "git status"}},
		{name: "a function declared in a list may not be declared",
			cmd:  zsh("true && make() { :; }; make v"),
			want: []string{"~/bin/zsh", "make v"}},
		{name: "the parser takes a list after a declaration for its body",
			cmd:  zsh("f() { :; } && git status"),
			want: []string{"~/bin/zsh", "git status"}},
		{name: "a body calls the functions declared before it only",
			cmd:  zsh("a() { b }; b() { make }; a"),
			want: []string{"~/bin/zsh", "b", "make"}},
		{name: "unset -f makes later calls run the program",
			cmd:  zsh("make() { :; }; unset -f make; make y"),
			want: []string{"opaque: unsetting a function makes later calls of it run a program"}},
		{name: "unset of a variable named like a function",
			cmd:  zsh("make() { :; }; unset make; make y; ls"),
			want: []string{"~/bin/zsh", "ls"}},

		{name: "repeat", cmd: zsh(`repeat 3 rm x`),
			want: []string{"opaque: repeat runs code that cannot be followed"}},
		{name: "foreach", cmd: zsh(`foreach i (a b) rm $i; end`),
			want: []string{"opaque: foreach runs code that cannot be followed"}},
		{name: "coproc", cmd: zsh(`coproc rm x`),
			want: []string{"opaque: coproc runs code that cannot be followed"}},
		{name: "precommand modifier", cmd: zsh(`noglob rm *`),
			want: []string{"opaque: noglob runs code that cannot be followed"}},
		{name: "emulate", cmd: zsh(`emulate sh -c 'rm x'`),
			want: []string{"opaque: emulate runs code that cannot be followed"}},
		{name: "setopt", cmd: zsh(`setopt globsubst; ls $x`),
			want: []string{"opaque: setopt runs code that cannot be followed"}},
		{name: "autoload", cmd: zsh(`autoload -U f; f`),
			want: []string{"opaque: autoload runs code that cannot be followed"}},
		{name: "module builtin", cmd: zsh(`zpty p 'rm x'`),
			want: []string{"opaque: zpty runs code that cannot be followed"}},
		{name: "exec runs builtins", cmd: zsh(`exec eval 'rm x'`),
			want: []string{"opaque: eval runs code that cannot be followed"}},
		{name: "exec runs functions", cmd: zsh(`f() { rm x }; exec f`),
			want: []string{"~/bin/zsh", "rm x"}},
		{name: "command runs programs", cmd: zsh(`command echo hi`),
			want: []string{"~/bin/zsh", "echo hi"}},
		{name: "command and a builtin that runs code", cmd: zsh(`command eval 'rm x'`),
			want: []string{"opaque: command eval may run the builtin, whose code cannot be followed"}},
		{name: "bash builtins zsh lacks are programs", cmd: zsh(`shopt -s x`),
			want: []string{"~/bin/zsh", "shopt -s x"}},

		{name: "print", cmd: zsh(`print -r -- hi; whence -p git`),
			want: []string{"~/bin/zsh", "builtin print -r -- hi", "builtin whence -p git"}},
		{name: "print prompt expansion", cmd: zsh(`print -P '%~'`),
			want: []string{"opaque: print -P runs prompt expansions, which can run code"}},
		{name: "print sets a variable", cmd: zsh(`print -v PATH /tmp; ls`),
			want: []string{"opaque: it sets PATH"}},
		{name: "print options not known until it runs", cmd: zsh(`print $x`),
			want: []string{"opaque: the options of print cannot be followed"}},
		{name: "set", cmd: zsh(`set -eu; set -- a b; ls`),
			want: []string{"~/bin/zsh", "ls"}},
		{name: "set options", cmd: zsh(`set -o globsubst; ls`),
			want: []string{"opaque: set -o changes what the script runs"}},
		{name: "set an array", cmd: zsh(`set -A path /tmp; ls`),
			want: []string{"opaque: set -A changes what the script runs"}},
		{name: "typeset functions", cmd: zsh(`typeset -fu f; f`),
			want: []string{"opaque: typeset -fu changes functions or sets variables by pattern"}},
		{name: "integer functions", cmd: zsh(`integer -f f; f`),
			want: []string{"opaque: integer -f changes functions or sets variables by pattern"}},
		{name: "dangerous read", cmd: zsh(`read -p PATH; ls`),
			want: []string{"opaque: it sets PATH"}},
		{name: "read with a prompt", cmd: zsh(`read 'line?Name: ' <<< hi; ls $line`),
			want: []string{"~/bin/zsh", "builtin read line?Name: ", "ls …"}},
		{name: "read with a prompt into a dangerous variable", cmd: zsh(`read 'path?Name: '; ls`),
			want: []string{"opaque: it sets path"}},
		{name: "arithmetic on an argument of the script",
			cmd:  []string{"/bin/zsh", "-c", "echo $(($1)); ls", "_", "a[$(id)]"},
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on the contents of a file", cmd: zsh(`x=$(<a); echo $((x)); ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on what read reads", cmd: zsh(`read x < a; [[ $x -eq 1 ]]; ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on a substitution", cmd: zsh(`echo $(( $(git log) )); ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "read into a subscript", cmd: zsh(`read 'b[i]'; ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on file names of a loop", cmd: zsh(`for f in *; do (( f )); done; ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on file names of an array", cmd: zsh(`x=(*); (( x[1] )); ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on file names a function gets", cmd: zsh(`f() { (( $1 )) }; f *; ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on file names set as arguments", cmd: zsh(`set -- *; (( $1 )); ls`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on the file names find runs a script with",
			cmd:  []string{"find", ".", "-exec", "zsh", "-c", "(( $1 ))", "_", "{}", ";"},
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic on the input xargs appends", cmd: zsh(`ls | xargs zsh -c '(( $1 ))' _`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "arithmetic of a loop counter in a script that reads data",
			cmd:  zsh(`x=$(git log); for ((i = 0; i < 3; i++)); do ls; done`),
			want: []string{"opaque: its arithmetic may run command substitutions hidden in strings"}},
		{name: "conditions on the output of a command",
			cmd: zsh(`[[ -n $(git status --porcelain) ]] && [ -f "$(git rev-parse --show-toplevel)/go.mod" ] && go test`),
			want: []string{"~/bin/zsh", "git status --porcelain", "builtin [ -f …",
				"git rev-parse --show-toplevel", "go test"}},
		{name: "numbers in the arithmetic of a script that reads data",
			cmd:  zsh(`read -r x < a; [ $? -ne 0 ] && exit 1; (( $# > 0 )) && ls ${#x} $((1 + 2)) ${x:0:3} $x[1]`),
			want: []string{"~/bin/zsh", "builtin read -r x", "builtin [ …", "ls …"}},
		{name: "formats that convert no number", cmd: zsh(`x=$(git log); printf '%s: %-5s %%\n' "$x" a; ls`),
			want: []string{"~/bin/zsh", "git log", "builtin printf %s: %-5s %%\\n …", "ls"}},
		{name: "declarations that set no number", cmd: zsh(`x=$(git log); typeset y=$x; local z=$x; export w=$x; ls`),
			want: []string{"~/bin/zsh", "git log", "ls"}},

		{name: "path array", cmd: zsh(`path=(/tmp $path); ls`),
			want: []string{"opaque: it sets path"}},
		{name: "null command", cmd: zsh(`NULLCMD=rm; ls`),
			want: []string{"opaque: it sets NULLCMD"}},
		{name: "hook functions", cmd: zsh(`chpwd_functions+=(f); cd /tmp`),
			want: []string{"opaque: it sets chpwd_functions"}},
		{name: "functions table", cmd: zsh(`functions[ls]='rm -rf ~'; ls`),
			want: []string{"opaque: it sets functions"}},
		{name: "stty for a command", cmd: zsh(`STTY=raw ls`),
			want: []string{"opaque: it sets STTY"}},
		{name: "zsh started as another shell", cmd: []string{"bash", "-c", "ARGV0=sh zsh -c ls"},
			want: []string{"opaque: it sets ARGV0"}},
		{name: "dangerous default assignment", cmd: zsh(`: ${NULLCMD:=rm}; ls`),
			want: []string{"opaque: it sets NULLCMD"}},
		{name: "dangerous command environment", cmd: zsh("ls"), env: []string{"NULLCMD=rm"},
			want: []string{"opaque: it sets NULLCMD"}},
		{name: "dangerous variable of the script that starts zsh",
			cmd:  []string{"bash", "-c", "path=/tmp; zsh -c ls"},
			want: []string{"opaque: it sets path"}},
		{name: "path is not dangerous to bash", cmd: []string{"bash", "-c", "path=/tmp; ls $path"},
			want: []string{"~bash", "ls …"}},
		{name: "bash in zsh", cmd: zsh(`bash -c 'echo a^b'; echo a^b`),
			want: []string{"~/bin/zsh", "~bash", "builtin echo a^b", "builtin echo …"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			plan := planCommand(tc.cmd[0], tc.cmd[1:], tc.env, nil)
			assert.Equal(t, tc.want, describePlan(plan))
		})
	}
}

func TestPlanCommandBenchmarkComparisonScript(t *testing.T) {
	t.Parallel()

	plan := planCommand("/bin/bash", []string{"-c", benchmarkComparisonScript}, nil, nil)
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
		plan := planCommand(tc.cmd[0], tc.cmd[1:], nil, nil)
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
		plan := planCommand(tc.cmd[0], tc.cmd[1:], nil, nil)
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
