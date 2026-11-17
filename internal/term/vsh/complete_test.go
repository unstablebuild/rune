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
	"errors"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/coreutils"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

// completionFiles is the tree every completion test runs in, besides
// the fixture's bin directory.
var completionFiles = map[string]string{
	"agent/x.md":       "",
	"AGENTS.md":        "",
	"appearance/y.md":  "",
	"config.md":        "",
	"docs/guide.md":    "",
	"docs/guides/a.md": "",
	"docs/README":      "",
	".hidden":          "",
	".config/z":        "",
	"my file.txt":      "",
	"tools/data":       "",
	"acc/é1":           "",
	"acc/è2":           "",
	"uni/日本":           "",
	"uni/日本語":          "",
	"sp/dollar$x":      "",
	"sp/quote'x":       "",
	"sp/dquote\"x":     "",
	"sp/star*x":        "",
	"sp/brack[x]":      "",
	"sp/semi;x":        "",
	"sp/~tilde":        "",
	"sp/paren(x)":      "",
	"sp/amp&x":         "",
	"sp/pipe|x":        "",
	"sp/bang!x":        "",
	"sp/brace{x}":      "",
	"sp/lt<x>":         "",
	"sp/bs\\x":         "",
	"sp/tick`x":        "",
	"sp/eq=x,y:z@w":    "",
}

// completionFixture is a workspace holding completionFiles, the empty
// directory empty and the executable tools/run.
func completionFixture(t *testing.T, backend vshtest.Backend, commands ...vsh.Command) *vshtest.Fixture {
	t.Helper()
	f := vshtest.New(t, vshtest.Options{Backend: backend, Files: completionFiles,
		Commands: append(coreutils.Commands(), commands...)})
	require.NoError(t, f.Scheme.MkdirAll(f.Path("empty"), 0o755))
	seedExec(t, f, "tools/run", "")
	return f
}

// homeReplacer replaces {home} in expected paths with the fixture's.
func homeReplacer(f *vshtest.Fixture) *strings.Replacer {
	return strings.NewReplacer("{home}/", strings.TrimSuffix(f.Home, "/")+"/")
}

func completions(t *testing.T, c vsh.Completer, cmd string, args []string) []string {
	t.Helper()
	it, err := c.Complete(context.Background(), cmd, args)
	require.NoError(t, err)
	got, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	if len(got) == 0 {
		return nil
	}
	return got
}

var homeEntries = []string{"AGENTS.md", "acc/", "agent/", "appearance/", "bin/", "config.md", "docs/", "empty/",
	"my file.txt", "sp/", "tools/", "uni/"}

func TestCompleterComplete(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		dir   string
		path  string
		funcs []string
		cmd   string
		args  []string
		want  []string
	}{
		{name: "a command name completes to the program it starts", cmd: "ca", want: []string{"cat"}},
		{name: "a command name completes to builtins, programs and directories",
			cmd: "e", want: []string{"echo", "empty/", "env", "eval", "exec", "exit", "export"}},
		{name: "a command name completes to the functions defined", funcs: []string{"greet", "grab"},
			cmd: "gr", want: []string{"grab", "greet", "grep"}},
		{name: "a builtin and a program of one name complete once", cmd: "pw", want: []string{"pwd"}},
		{name: "a hidden command name completes for a dot", cmd: ".",
			want: []string{".", "../", "./", ".config/"}},
		{name: "a command name matching nothing completes to nothing", cmd: "zz"},
		{name: "the PATH is searched, relative entries from dir", path: "tools",
			cmd: "r", want: []string{"read", "readarray", "readonly", "return", "run"}},
		{name: "files the PATH holds that are not executable are no commands", path: "tools",
			cmd: "d", want: []string{"declare", "dirs", "docs/"}},
		{name: "an empty PATH is the bin directory", path: "", cmd: "wh", want: []string{"which"}},
		{name: "a missing PATH directory is skipped", path: "/nope:tools", cmd: "ru", want: []string{"run"}},
		{name: "every PATH directory is searched", path: "{home}/bin:tools", cmd: "r",
			want: []string{"read", "readarray", "readonly", "return", "rm", "rmdir", "run"}},
		{name: "a command with a slash completes to executables", cmd: "tools/", want: []string{"tools/run"}},
		{name: "a command with a slash completes to directories", cmd: "./t", want: []string{"./tools/"}},
		{name: "a command from HOME", cmd: "~/tools/r", want: []string{"~/tools/run"}},
		{name: "a command from the bin directory", cmd: "bin/ca", want: []string{"bin/cat"}},
		{name: "an absolute command", cmd: "{home}/tools/", want: []string{"{home}/tools/run"}},

		{name: "an argument completes to a directory", cmd: "cat", args: []string{"age"}, want: []string{"agent/"}},
		{name: "an argument completes to every match, case-sensitively", cmd: "cat", args: []string{"a"},
			want: []string{"acc/", "agent/", "appearance/"}},
		{name: "an uppercase argument", cmd: "cat", args: []string{"A"}, want: []string{"AGENTS.md"}},
		{name: "an empty argument completes to every visible entry", cmd: "cat", args: []string{""}, want: homeEntries},
		{name: "args without a partial word complete as an empty one", cmd: "cat", args: []string{}, want: homeEntries},
		{name: "an argument in a directory", cmd: "cat", args: []string{"docs/gu"},
			want: []string{"docs/guide.md", "docs/guides/"}},
		{name: "a directory's slash completes to its entries", cmd: "cat", args: []string{"docs/"},
			want: []string{"docs/README", "docs/guide.md", "docs/guides/"}},
		{name: "an empty directory completes to nothing", cmd: "cat", args: []string{"empty/"}},
		{name: "an absolute argument", cmd: "cat", args: []string{"{home}/con"}, want: []string{"{home}/config.md"}},
		{name: "the root", dir: "docs", cmd: "cat", args: []string{"{home}/docs/R"}, want: []string{"{home}/docs/README"}},
		{name: "HOME", dir: "docs", cmd: "cat", args: []string{"~/con"}, want: []string{"~/config.md"}},
		{name: "a bare tilde completes to HOME's slash", cmd: "cat", args: []string{"~"}, want: []string{"~/"}},
		{name: "HOME's slash completes to its entries", cmd: "cat", args: []string{"~/do"}, want: []string{"~/docs/"}},
		{name: "another user's HOME completes to nothing", cmd: "cat", args: []string{"~nobody"}},
		{name: "another user's files complete to nothing", cmd: "cat", args: []string{"~nobody/x"}},
		{name: "a dot completes to the hidden entries and the dot directories", cmd: "cat", args: []string{"."},
			want: []string{"../", "./", ".config/", ".hidden"}},
		{name: "two dots complete to the parent", cmd: "cat", args: []string{".."}, want: []string{"../"}},
		{name: "a hidden prefix", cmd: "cat", args: []string{".h"}, want: []string{".hidden"}},
		{name: "a hidden directory's entries", cmd: "cat", args: []string{".config/"}, want: []string{".config/z"}},
		{name: "the parent's entries", dir: "docs", cmd: "cat", args: []string{"../con"}, want: []string{"../config.md"}},
		{name: "a doubled slash is kept as typed", cmd: "cat", args: []string{"docs//gu"},
			want: []string{"docs//guide.md", "docs//guides/"}},
		{name: "a file is no directory", cmd: "cat", args: []string{"config.md/x"}},
		{name: "a missing directory", cmd: "cat", args: []string{"nope/x"}},
		{name: "nothing matches", cmd: "cat", args: []string{"zzz"}},
		{name: "candidates are unquoted", cmd: "cat", args: []string{"my"}, want: []string{"my file.txt"}},
		{name: "special characters are unquoted", cmd: "cat", args: []string{"sp/d"},
			want: []string{"sp/dollar$x", "sp/dquote\"x"}},
		{name: "earlier words do not matter", cmd: "grep", args: []string{"-r", "pattern", "do"}, want: []string{"docs/"}},
		{name: "a relative argument is taken from dir", dir: "docs", cmd: "cat", args: []string{"gu"},
			want: []string{"guide.md", "guides/"}},
		{name: "an unknown command's arguments are paths", cmd: "nope", args: []string{"con"}, want: []string{"config.md"}},
		{name: "a command run by path has paths for arguments", cmd: "./tools/run", args: []string{"con"},
			want: []string{"config.md"}},
		{name: "the arguments of cd are directories", cmd: "cd", args: []string{"a"},
			want: []string{"acc/", "agent/", "appearance/"}},
		{name: "cd completes no file", cmd: "cd", args: []string{"con"}},
		{name: "an empty argument of cd", cmd: "cd", args: []string{""},
			want: []string{"acc/", "agent/", "appearance/", "bin/", "docs/", "empty/", "sp/", "tools/", "uni/"}},
		{name: "the arguments of pushd are directories", cmd: "pushd", args: []string{"docs/"},
			want: []string{"docs/guides/"}},
	}
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			f := completionFixture(t, backend)
			x := homeReplacer(f)
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					args := tc.args
					if args != nil {
						args = make([]string, len(tc.args))
						for i, a := range tc.args {
							args[i] = x.Replace(a)
						}
					}
					var want []string
					for _, w := range tc.want {
						want = append(want, x.Replace(w))
					}
					c := f.Exec.NewCompleter(f.Path(tc.dir), x.Replace(tc.path), tc.funcs...)
					assert.Equal(t, want, completions(t, c, x.Replace(tc.cmd), args))
				})
			}
		})
	}
}

func TestCompleterCompletesEveryCommandForNothingTyped(t *testing.T) {
	t.Parallel()
	f := completionFixture(t, vshtest.Memory)
	got := completions(t, f.Exec.NewCompleter(f.Home, "", "myfunc"), "", nil)
	assert.True(t, slices.IsSorted(got), got)
	assert.Equal(t, got, slices.Compact(slices.Clone(got)), "no name twice")
	for _, name := range append(f.Exec.BinNames(), "cd", "export", "myfunc", "agent/", "tools/") {
		assert.Contains(t, got, name)
	}
	for _, name := range []string{"config.md", ".config/", "bg", "jobs"} {
		assert.NotContains(t, got, name, "files, hidden directories and job control are no commands")
	}
}

// recordingCompleter answers with cands or err, remembering what it was
// asked.
type recordingCompleter struct {
	cands []string
	err   error
	itErr error
	calls [][]string
}

func (r *recordingCompleter) Complete(_ context.Context, cmd string, args []string) (iterator.Iterator[string], error) {
	r.calls = append(r.calls, append([]string{cmd}, args...))
	switch {
	case r.err != nil:
		return nil, r.err
	case r.itErr != nil:
		return iterator.Error[string](r.itErr), nil
	}
	return iterator.FromSlice(r.cands), nil
}

func TestCompleterAsksTheCommandsCompleter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		completer recordingCompleter
		cmd       string
		args      []string
		want      []string
		calls     [][]string
	}{
		{name: "its candidates, in its order",
			completer: recordingCompleter{cands: []string{"--beta", "--alpha"}},
			cmd:       "probe", args: []string{"-v", "--"},
			want: []string{"--beta", "--alpha"}, calls: [][]string{{"probe", "-v", "--"}}},
		{name: "an empty partial word",
			completer: recordingCompleter{cands: []string{"one"}},
			cmd:       "probe", args: []string{""}, want: []string{"one"}, calls: [][]string{{"probe", ""}}},
		{name: "paths when it yields nothing",
			completer: recordingCompleter{},
			cmd:       "probe", args: []string{"con"}, want: []string{"config.md"}, calls: [][]string{{"probe", "con"}}},
		{name: "paths when it fails",
			completer: recordingCompleter{err: errors.New("boom")},
			cmd:       "probe", args: []string{"con"}, want: []string{"config.md"}, calls: [][]string{{"probe", "con"}}},
		{name: "paths when its candidates fail",
			completer: recordingCompleter{itErr: errors.New("boom")},
			cmd:       "probe", args: []string{"con"}, want: []string{"config.md"}, calls: [][]string{{"probe", "con"}}},
		{name: "not for the command name",
			completer: recordingCompleter{cands: []string{"x"}},
			cmd:       "pro", want: []string{"probe"}},
		{name: "not for another command",
			completer: recordingCompleter{cands: []string{"x"}},
			cmd:       "cat", args: []string{"con"}, want: []string{"config.md"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := tc.completer
			f := completionFixture(t, vshtest.Memory, vsh.Command{Name: "probe", Run: nopRun, Completer: &rec})
			assert.Equal(t, tc.want, completions(t, f.Exec.NewCompleter(f.Home, ""), tc.cmd, tc.args))
			assert.Equal(t, tc.calls, rec.calls)
		})
	}
}

func TestCompleterFollowsSymlinks(t *testing.T) {
	t.Parallel()
	f := completionFixture(t, vshtest.File)
	for link, target := range map[string]string{"ldir": "docs", "lfile": "config.md", "ldangling": "nope"} {
		require.NoError(t, f.Scheme.Symlink(filepath.FromSlash(target), f.Path(link)))
	}
	c := f.Exec.NewCompleter(f.Home, "")
	assert.Equal(t, []string{"ldangling", "ldir/", "lfile"}, completions(t, c, "cat", []string{"l"}),
		"a link to a directory is one, any other link a file")
	assert.Equal(t, []string{"ldir/"}, completions(t, c, "cd", []string{"l"}))
	assert.Equal(t, []string{"ldir/README", "ldir/guide.md", "ldir/guides/"}, completions(t, c, "cat", []string{"ldir/"}))
}

// splitCursor removes the cursor mark "‸" from s, the end without one,
// and returns its byte offset.
func splitCursor(s string) (string, int) {
	if i := strings.Index(s, "‸"); i >= 0 {
		return s[:i] + s[i+len("‸"):], i
	}
	return s, len(s)
}

func TestCompleteLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		dir   string
		funcs []string
		// line marks the cursor with "‸", as does want, the line Tab
		// leaves; an empty want leaves line as is.
		line string
		want string
		list []string
	}{
		{name: "a word becomes the directory it completes to", line: "cat age", want: "cat agent/‸"},
		{name: "a unique file ends the word", line: "cat con", want: "cat config.md ‸"},
		{name: "several advance to what they share", line: "cat docs/g", want: "cat docs/guide‸"},
		{name: "several sharing nothing more are listed", line: "cat a", list: []string{"acc/", "agent/", "appearance/"}},
		{name: "a listing holds whole candidates", line: "cat docs/guide",
			list: []string{"docs/guide.md", "docs/guides/"}},
		{name: "nothing to complete leaves the line", line: "cat zzz"},
		{name: "an empty word lists the directory", line: "cat ", list: homeEntries},
		{name: "a directory's entries complete after its slash", line: "cat docs/guides/", want: "cat docs/guides/a.md ‸"},
		{name: "the text before the cursor completes", line: "cat con‸ | wc -l", want: "cat config.md ‸| wc -l"},
		{name: "the rest of the word under the cursor stays", line: "cat con‸fig", want: "cat config.md ‸fig"},
		{name: "a space after the cursor is not doubled", line: "cat con‸ x", want: "cat config.md ‸x"},
		{name: "a directory before a space", line: "cat do‸ x", want: "cat docs/‸ x"},
		{name: "words are completed relative to the directory", dir: "docs", line: "cat gu", want: "cat guide‸"},
		{name: "characters are never split", line: "cat acc/", list: []string{"acc/è2", "acc/é1"}},
		{name: "several characters are shared", line: "cat uni/", want: "cat uni/日本‸"},
		{name: "a shared prefix that is a candidate lists", line: "cat uni/日本", list: []string{"uni/日本", "uni/日本語"}},

		{name: "a space is escaped", line: "cat my", want: `cat my\ file.txt ‸`},
		{name: "an escaped space in the partial word", line: `cat my\ f`, want: `cat my\ file.txt ‸`},
		{name: "a trailing backslash", line: `cat my\`, want: `cat my\ file.txt ‸`},
		{name: "a single-quoted word stays quoted and is closed", line: "cat 'my", want: "cat 'my file.txt' ‸"},
		{name: "a double-quoted word stays quoted and is closed", line: `cat "my`, want: `cat "my file.txt" ‸`},
		{name: "a quote opened mid-word quotes the word", line: `cat my" f`, want: `cat "my file.txt" ‸`},
		{name: "a quote closed mid-word is escaped", line: `cat "my "f`, want: `cat my\ file.txt ‸`},
		{name: "a quoted directory stays open", line: `cat "do`, want: `cat "docs/‸`},
		{name: "a quoted empty word", line: `cat "con`, want: `cat "config.md" ‸`},
		{name: "a dollar is escaped", line: "cat sp/do", want: `cat sp/dollar\$x ‸`},
		{name: "a dollar in double quotes", line: `cat "sp/do`, want: `cat "sp/dollar\$x" ‸`},
		{name: "a dollar in single quotes", line: `cat 'sp/do`, want: `cat 'sp/dollar$x' ‸`},
		{name: "a single quote is escaped", line: "cat sp/q", want: `cat sp/quote\'x ‸`},
		{name: "a single quote in single quotes", line: "cat 'sp/q", want: `cat 'sp/quote'\''x' ‸`},
		{name: "a double quote in double quotes", line: `cat "sp/dq`, want: `cat "sp/dquote\"x" ‸`},
		{name: "a glob character is escaped", line: "cat sp/st", want: `cat sp/star\*x ‸`},
		{name: "brackets are escaped", line: "cat sp/brack", want: `cat sp/brack\[x\] ‸`},
		{name: "an operator is escaped", line: "cat sp/se", want: `cat sp/semi\;x ‸`},
		{name: "a tilde past the start is not escaped", line: "cat sp/~", want: "cat sp/~tilde ‸"},
		{name: "a leading tilde of a file is escaped", dir: "sp", line: "cat ./~", want: "cat ./~tilde ‸"},
		{name: "parentheses are escaped", line: "cat sp/pa", want: `cat sp/paren\(x\) ‸`},
		{name: "an ampersand is escaped", line: "cat sp/am", want: `cat sp/amp\&x ‸`},
		{name: "a pipe is escaped", line: "cat sp/pi", want: `cat sp/pipe\|x ‸`},
		{name: "a bang is escaped", line: "cat sp/ba", want: `cat sp/bang\!x ‸`},
		{name: "several sharing a prefix of special names", line: "cat sp/bra", want: "cat sp/brac‸"},
		{name: "braces are escaped", line: "cat sp/brace", want: `cat sp/brace\{x\} ‸`},
		{name: "redirection characters are escaped", line: "cat sp/l", want: `cat sp/lt\<x\> ‸`},
		{name: "a backslash is escaped", line: "cat sp/bs", want: `cat sp/bs\\x ‸`},
		{name: "a backtick is escaped", line: "cat sp/ti", want: "cat sp/tick\\`x ‸"},
		{name: "a backtick in double quotes", line: `cat "sp/ti`, want: "cat \"sp/tick\\`x\" ‸"},
		{name: "harmless punctuation is not escaped", line: "cat sp/e", want: "cat sp/eq=x,y:z@w ‸"},
		{name: "HOME is kept", line: "cat ~/con", want: "cat ~/config.md ‸"},
		{name: "a tilde completes to HOME", line: "cat ~", want: "cat ~/‸"},
		{name: "HOME stays outside the quotes", line: `cat ~/"my`, want: `cat ~/"my file.txt" ‸`},
		{name: "HOME's directory stays outside the quotes", line: `cat ~/"do`, want: `cat ~/"docs/‸`},

		{name: "a command name", line: "ca", want: "cat ‸"},
		{name: "a command after a pipe", line: "ls | wc -l | gr", want: "ls | wc -l | grep ‸"},
		{name: "a command after a semicolon", line: "true;ca", want: "true;cat ‸"},
		{name: "a command after and", line: "true && ca", want: "true && cat ‸"},
		{name: "a command after or", line: "true || ca", want: "true || cat ‸"},
		{name: "a command after an ampersand", line: "true & ca", want: "true & cat ‸"},
		{name: "a command in a subshell", line: "(ca", want: "(cat ‸"},
		{name: "a command after a subshell", line: "(true); ca", want: "(true); cat ‸"},
		{name: "a command in a substitution", line: "echo $(ca", want: "echo $(cat ‸"},
		{name: "a command in a quoted substitution", line: `echo "$(ca`, want: `echo "$(cat ‸`},
		{name: "a command in a backtick substitution", line: "echo `ca", want: "echo `cat ‸"},
		{name: "a command in a quoted backtick substitution", line: "echo \"`ca", want: "echo \"`cat ‸"},
		{name: "a command in a nested substitution", line: "echo $(echo $(ca", want: "echo $(echo $(cat ‸"},
		{name: "a command in a subshell in a substitution", line: "echo $( (ca", want: "echo $( (cat ‸"},
		{name: "an argument in a substitution", line: "echo $(cat con", want: "echo $(cat config.md ‸"},
		{name: "an argument after a substitution", line: "echo $(true) con", want: "echo $(true) config.md ‸"},
		{name: "an argument after a backtick substitution", line: "echo `true` con", want: "echo `true` config.md ‸"},
		{name: "an argument after arithmetic", line: "echo $((1+(2))) con", want: "echo $((1+(2))) config.md ‸"},
		{name: "an argument after a quoted parenthesis", line: `echo $(echo ")") con`, want: `echo $(echo ")") config.md ‸`},
		{name: "an argument after a substitution in quotes", line: `echo "$(true)" con`, want: `echo "$(true)" config.md ‸`},
		{name: "a command after if", line: "if ca", want: "if cat ‸"},
		{name: "a command after then", line: "if true; then ca", want: "if true; then cat ‸"},
		{name: "a command after do", line: "while true; do ca", want: "while true; do cat ‸"},
		{name: "a command after else", line: "if true; then :; else ca", want: "if true; then :; else cat ‸"},
		{name: "a command after a bang", line: "! ca", want: "! cat ‸"},
		{name: "a command after time", line: "time ca", want: "time cat ‸"},
		{name: "a command in a group", line: "{ ca", want: "{ cat ‸"},
		{name: "a command after keywords", line: "if ! ca", want: "if ! cat ‸"},
		{name: "a command after an assignment", line: "X=1 ca", want: "X=1 cat ‸"},
		{name: "a command after appending assignments", line: "X+=1 Y=2 ca", want: "X+=1 Y=2 cat ‸"},
		{name: "a quoted assignment is the command", line: `"X=1" con`, want: `"X=1" config.md ‸`},
		{name: "a quoted keyword is the command", line: `"if" con`, want: `"if" config.md ‸`},
		{name: "an assignment after the command is an argument", line: "env X=1 con", want: "env X=1 config.md ‸"},
		{name: "a function", funcs: []string{"greet"}, line: "gre", list: []string{"greet", "grep"}},
		{name: "an executable by path", line: "./tools/r", want: "./tools/run ‸"},
		{name: "a directory in command position", line: "do", want: "docs/‸"},
		{name: "commands after a pipe are listed", line: "ls | w", list: []string{"wc", "which"}},

		{name: "a redirection target", line: "echo hi > con", want: "echo hi > config.md ‸"},
		{name: "a redirection target without a blank", line: "echo hi >con", want: "echo hi >config.md ‸"},
		{name: "an appending redirection target", line: "echo hi >>con", want: "echo hi >>config.md ‸"},
		{name: "an input redirection target", line: "cat <con", want: "cat <config.md ‸"},
		{name: "a descriptor's redirection target", line: "echo hi 2>con", want: "echo hi 2>config.md ‸"},
		{name: "a redirection of both streams", line: "echo hi &>con", want: "echo hi &>config.md ‸"},
		{name: "an appending redirection of both streams", line: "echo hi &>>con", want: "echo hi &>>config.md ‸"},
		{name: "a redirection after a duplication", line: "echo hi 2>&1 >con", want: "echo hi 2>&1 >config.md ‸"},
		{name: "a redirection before the command", line: "<con", want: "<config.md ‸"},
		{name: "a command after a redirection", line: "<config.md ca", want: "<config.md cat ‸"},
		{name: "an argument after a redirection", line: "cat >out con", want: "cat >out config.md ‸"},
		{name: "a number that is an argument", line: "head -n 2 con", want: "head -n 2 config.md ‸"},
		{name: "a quoted number before a redirection is an argument", line: `echo "2">con`, want: `echo "2">config.md ‸`},
		{name: "a redirection target is a file for cd", line: "cd >con", want: "cd >config.md ‸"},

		{name: "cd completes a directory", line: "cd do", want: "cd docs/‸"},
		{name: "cd lists directories", line: "cd a", list: []string{"acc/", "agent/", "appearance/"}},
		{name: "cd completes no file", line: "cd con"},
		{name: "a comment completes nothing", line: "echo # con"},
		{name: "a quoted hash is no comment", line: "echo '#' con", want: "echo '#' config.md ‸"},
	}
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			f := completionFixture(t, backend)
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					line, pos := splitCursor(tc.line)
					wantLine, wantPos := line, pos
					if tc.want != "" {
						wantLine, wantPos = splitCursor(tc.want)
					}
					c := f.Exec.NewCompleter(f.Path(tc.dir), "", tc.funcs...)
					gotLine, gotPos, list := vsh.CompleteLine(c, line, pos)
					assert.Equal(t, wantLine, gotLine)
					assert.Equal(t, wantPos, gotPos)
					assert.Equal(t, tc.list, list)
				})
			}
		})
	}
}

// trickyNames are file names every quoting must get back. The memory
// scheme drops a newline from a name, which insertion's own test
// covers.
var trickyNames = []string{
	"plain", "with space", "multiple  spaces", " leading", "trailing ", "tab\there",
	"ctrl\x01char", "del\x7f", "esc\x1b[0m", "quote'", `dq"`, `it's "both"`, `back\slash`, `\`,
	"$dollar", "$(cmd)", "${var}", "`tick`", "star*", "q?", "[br]", "{a,b}", "a;b", "a&b", "a|b",
	"<lt>", "#hash", "~tilde", "!bang", "(p)", "日本語", "emoji😀", "=eq", "-dash", "a=b", "x:y@z%",
}

// readBack is the one word the shell reads text as, with HOME home.
func readBack(t *testing.T, text, home string) string {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader("x "+text), "")
	require.NoError(t, err, text)
	require.Len(t, file.Stmts, 1, text)
	fields, err := expand.Fields(&expand.Config{Env: expand.ListEnviron("HOME=" + home)},
		file.Stmts[0].Cmd.(*syntax.CallExpr).Args...)
	require.NoError(t, err, text)
	require.Len(t, fields, 2, text)
	return fields[1]
}

func TestInsertionIsReadBack(t *testing.T) {
	t.Parallel()
	for _, word := range append(trickyNames, "new\nline", "cr\rx", "~", "~/") {
		for _, quote := range []byte{0, '\'', '"'} {
			t.Run(strconv.Quote(string(quote)+word), func(t *testing.T) {
				t.Parallel()
				assert.Equal(t, word, readBack(t, vsh.Insertion(word, quote, true, false), "/home"))
				assert.Equal(t, "/home/"+word, readBack(t, vsh.Insertion("~/"+word, quote, true, true), "/home"),
					"HOME is expanded")
			})
		}
	}
}

func TestCompleteLineQuotesNamesTheShellReadsBack(t *testing.T) {
	t.Parallel()
	prefixes := []string{"", "'", `"`, "~/", "~/'", `~/"`}
	// The memory scheme cannot hold the names a URI escapes.
	for _, backend := range []vshtest.Backend{vshtest.File} {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Backend: backend})
			for i, name := range trickyNames {
				dir := "d" + strconv.Itoa(i)
				f.Seed(t, dir+"/"+name, "")
				for _, prefix := range prefixes {
					t.Run(strconv.Quote(prefix+name), func(t *testing.T) {
						t.Parallel()
						line := "cat " + prefix + dir + "/"
						got, _, list := vsh.CompleteLine(f.Exec.NewCompleter(f.Home, ""), line, len(line))
						require.Nil(t, list)
						word, ok := strings.CutPrefix(got, "cat ")
						require.True(t, ok, got)
						word, ok = strings.CutSuffix(word, " ")
						require.True(t, ok, "the word ends: %q", got)
						want := dir + "/" + name
						read := readBack(t, word, f.Home)
						if strings.HasPrefix(prefix, "~") {
							want = path.Join(f.Home, want)
							read = path.Clean(read)
						}
						assert.Equal(t, want, read, "%q", got)
					})
				}
			}
		})
	}
}

func TestLexLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		head string
		want vsh.LineContext
	}{
		{head: "", want: vsh.LineContext{}},
		{head: "ca", want: vsh.LineContext{Partial: "ca"}},
		{head: "cat ", want: vsh.LineContext{Words: []string{"cat"}, Start: 4}},
		{head: "cat a", want: vsh.LineContext{Words: []string{"cat"}, Partial: "a", Start: 4}},
		{head: "  cat  -n  a", want: vsh.LineContext{Words: []string{"cat", "-n"}, Partial: "a", Start: 11}},
		{head: "cat\ta", want: vsh.LineContext{Words: []string{"cat"}, Partial: "a", Start: 4}},
		{head: `cat a\ b`, want: vsh.LineContext{Words: []string{"cat"}, Partial: "a b", Start: 4}},
		{head: `cat \`, want: vsh.LineContext{Words: []string{"cat"}, Start: 4}},
		{head: "cat \\\na", want: vsh.LineContext{Words: []string{"cat"}, Partial: "a", Start: 4}},
		{head: `cat 'a b`, want: vsh.LineContext{Words: []string{"cat"}, Partial: "a b", Start: 4, Quote: '\''}},
		{head: `cat 'a\'`, want: vsh.LineContext{Words: []string{"cat"}, Partial: `a\`, Start: 4}},
		{head: `cat "a b`, want: vsh.LineContext{Words: []string{"cat"}, Partial: "a b", Start: 4, Quote: '"'}},
		{head: `cat "a\"b\\c\$d\e`, want: vsh.LineContext{Words: []string{"cat"}, Partial: `a"b\c$d\e`, Start: 4, Quote: '"'}},
		{head: `cat "a\`, want: vsh.LineContext{Words: []string{"cat"}, Partial: "a", Start: 4, Quote: '"'}},
		{head: `cat a"b c"d`, want: vsh.LineContext{Words: []string{"cat"}, Partial: "ab cd", Start: 4}},
		{head: `cat "a" 'b' c`, want: vsh.LineContext{Words: []string{"cat", "a", "b"}, Partial: "c", Start: 12}},
		{head: `cat ''`, want: vsh.LineContext{Words: []string{"cat"}, Start: 4}},
		{head: "cat 日本 語", want: vsh.LineContext{Words: []string{"cat", "日本"}, Partial: "語", Start: 11}},

		{head: "a | b", want: vsh.LineContext{Words: []string{}, Partial: "b", Start: 4}},
		{head: "a |b c", want: vsh.LineContext{Words: []string{"b"}, Partial: "c", Start: 5}},
		{head: "a; b", want: vsh.LineContext{Partial: "b", Start: 3}},
		{head: "a && b", want: vsh.LineContext{Partial: "b", Start: 5}},
		{head: "a || b", want: vsh.LineContext{Partial: "b", Start: 5}},
		{head: "a & b", want: vsh.LineContext{Partial: "b", Start: 4}},
		{head: "a |& b", want: vsh.LineContext{Partial: "b", Start: 5}},
		{head: "a\nb", want: vsh.LineContext{Partial: "b", Start: 2}},
		{head: "a |", want: vsh.LineContext{Start: 3}},
		{head: "(a", want: vsh.LineContext{Partial: "a", Start: 1}},
		{head: "(a) b", want: vsh.LineContext{Partial: "b", Start: 4}},
		{head: "f() { a", want: vsh.LineContext{Partial: "a", Start: 6}},
		{head: "a 'b|c' d", want: vsh.LineContext{Words: []string{"a", "b|c"}, Partial: "d", Start: 8}},
		{head: `a b\;c`, want: vsh.LineContext{Words: []string{"a"}, Partial: "b;c", Start: 2}},

		{head: "echo $(a", want: vsh.LineContext{Partial: "a", Start: 7}},
		{head: "echo $(a b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 9}},
		{head: "echo $(a) b", want: vsh.LineContext{Words: []string{"echo", ""}, Partial: "b", Start: 10}},
		{head: "echo x$(a)y b", want: vsh.LineContext{Words: []string{"echo", "xy"}, Partial: "b", Start: 12}},
		{head: "echo $(a $(b", want: vsh.LineContext{Partial: "b", Start: 11}},
		{head: "echo $(a $(b) c", want: vsh.LineContext{Words: []string{"a", ""}, Partial: "c", Start: 14}},
		{head: "echo $((1+2)) b", want: vsh.LineContext{Words: []string{"echo", ""}, Partial: "b", Start: 14}},
		{head: "echo $( (a", want: vsh.LineContext{Partial: "a", Start: 9}},
		{head: "echo $( (a) b", want: vsh.LineContext{Partial: "b", Start: 12}},
		{head: "echo `a", want: vsh.LineContext{Partial: "a", Start: 6}},
		{head: "echo `a` b", want: vsh.LineContext{Words: []string{"echo", ""}, Partial: "b", Start: 9}},
		{head: `echo "$(a`, want: vsh.LineContext{Partial: "a", Start: 8}},
		{head: `echo "$(a)" b`, want: vsh.LineContext{Words: []string{"echo", ""}, Partial: "b", Start: 12}},
		{head: `echo "$(a) b`, want: vsh.LineContext{Words: []string{"echo"}, Partial: " b", Start: 5, Quote: '"'}},
		{head: "echo \"`a", want: vsh.LineContext{Partial: "a", Start: 7}},
		{head: "echo \"`a \"b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 9, Quote: '"'}},
		{head: "echo \"`a\"`\" b", want: vsh.LineContext{Words: []string{"echo", ""}, Partial: "b", Start: 12}},
		{head: `echo $(echo ")" b`, want: vsh.LineContext{Words: []string{"echo", ")"}, Partial: "b", Start: 16}},
		{head: "echo '$(' b", want: vsh.LineContext{Words: []string{"echo", "$("}, Partial: "b", Start: 10}},
		{head: `echo \$(a b`, want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 10}},

		{head: "if a", want: vsh.LineContext{Partial: "a", Start: 3}},
		{head: "if a b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 5}},
		{head: "! time { while until do then elif else a", want: vsh.LineContext{Partial: "a", Start: 39}},
		{head: "echo if a", want: vsh.LineContext{Words: []string{"echo", "if"}, Partial: "a", Start: 8}},
		{head: "'if' a", want: vsh.LineContext{Words: []string{"if"}, Partial: "a", Start: 5}},
		{head: "for x in a", want: vsh.LineContext{Words: []string{"for", "x", "in"}, Partial: "a", Start: 9}},
		{head: "A=1 B+=2 a", want: vsh.LineContext{Partial: "a", Start: 9}},
		{head: "A=1 a B=2 c", want: vsh.LineContext{Words: []string{"a", "B=2"}, Partial: "c", Start: 10}},
		{head: `"A=1" a`, want: vsh.LineContext{Words: []string{"A=1"}, Partial: "a", Start: 6}},
		{head: "1A=x a", want: vsh.LineContext{Words: []string{"1A=x"}, Partial: "a", Start: 5}},

		{head: "a > b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 4, Redirect: true}},
		{head: "a >b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 3, Redirect: true}},
		{head: "a > ", want: vsh.LineContext{Words: []string{"a"}, Start: 4, Redirect: true}},
		{head: "a >", want: vsh.LineContext{Words: []string{"a"}, Start: 3, Redirect: true}},
		{head: "a >> b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 5, Redirect: true}},
		{head: "a < b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 4, Redirect: true}},
		{head: "a 2>b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 4, Redirect: true}},
		{head: "a 2>&1 b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 7}},
		{head: "a &>b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 4, Redirect: true}},
		{head: "a &>>b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 5, Redirect: true}},
		{head: "a >|b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 4, Redirect: true}},
		{head: "a <<<b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 5, Redirect: true}},
		{head: "a > f b", want: vsh.LineContext{Words: []string{"a"}, Partial: "b", Start: 6}},
		{head: "> f a", want: vsh.LineContext{Partial: "a", Start: 4}},
		{head: "a b>c", want: vsh.LineContext{Words: []string{"a", "b"}, Partial: "c", Start: 4, Redirect: true}},
		{head: `a "2">c`, want: vsh.LineContext{Words: []string{"a", "2"}, Partial: "c", Start: 6, Redirect: true}},
		{head: "a 2 >c", want: vsh.LineContext{Words: []string{"a", "2"}, Partial: "c", Start: 5, Redirect: true}},
		{head: "a > 'b c", want: vsh.LineContext{Words: []string{"a"}, Partial: "b c", Start: 4, Quote: '\'', Redirect: true}},
		{head: "a > b | c", want: vsh.LineContext{Partial: "c", Start: 8}},

		{head: "# a", want: vsh.LineContext{Comment: true}},
		{head: "echo # a", want: vsh.LineContext{Comment: true}},
		{head: "echo a#b", want: vsh.LineContext{Words: []string{"echo"}, Partial: "a#b", Start: 5}},
		{head: "echo '#' a", want: vsh.LineContext{Words: []string{"echo", "#"}, Partial: "a", Start: 9}},
		{head: `echo \# a`, want: vsh.LineContext{Words: []string{"echo", "#"}, Partial: "a", Start: 8}},
		{head: "echo $(# a", want: vsh.LineContext{Comment: true}},
	}
	for _, tc := range tests {
		t.Run(tc.head, func(t *testing.T) {
			t.Parallel()
			got := vsh.LexLine(tc.head)
			if len(got.Words) == 0 && len(tc.want.Words) == 0 {
				got.Words, tc.want.Words = nil, nil
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestInsertion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		word         string
		quote        byte
		final, tilde bool
		want         string
	}{
		{name: "plain", word: "a.md", final: true, want: "a.md"},
		{name: "every special character is escaped", word: " \\'\"`$&|;<>()*?[]#!{}",
			want: `\ \\\'\"\` + "`" + `\$\&\|\;\<\>\(\)\*\?\[\]\#\!\{\}`},
		{name: "punctuation that is not special", word: "a-b_c.d,e:f@g%h+i=j^k/", want: "a-b_c.d,e:f@g%h+i=j^k/"},
		{name: "a leading tilde is escaped", word: "~a", want: `\~a`},
		{name: "a tilde past the start is not", word: "a~b", want: "a~b"},
		{name: "HOME is kept", word: "~/a b", tilde: true, want: `~/a\ b`},
		{name: "HOME alone", word: "~/", tilde: true, want: "~/"},
		{name: "HOME is kept outside quotes", word: "~/a b", quote: '"', final: true, tilde: true, want: `~/"a b"`},
		{name: "a tilde that is not HOME is escaped even so", word: "~a", tilde: true, want: `\~a`},
		{name: "HOME's tilde is escaped without the partial word's", word: "~/a", want: `\~/a`},
		{name: "single quotes", word: "a b", quote: '\'', want: "'a b"},
		{name: "single quotes closed", word: "a b", quote: '\'', final: true, want: "'a b'"},
		{name: "a single quote in single quotes", word: "it's", quote: '\'', final: true, want: `'it'\''s'`},
		{name: "nothing is special in single quotes", word: `$"\` + "`", quote: '\'', final: true, want: `'$"\` + "`'"},
		{name: "double quotes", word: "a b", quote: '"', want: `"a b`},
		{name: "double quotes closed", word: "a b", quote: '"', final: true, want: `"a b"`},
		{name: "what double quotes escape", word: "$`\"\\'!*", quote: '"', final: true, want: `"\$\` + "`" + `\"\\'!*"`},
		{name: "control characters are ANSI-C quoted", word: "a\nb\tc\rd\x01e\x7f", final: true,
			want: `$'a\nb\tc\rd\x01e\x7f'`},
		{name: "ANSI-C quoting escapes quotes and backslashes", word: "'\\\n", want: `$'\'\\\n'`},
		{name: "ANSI-C quoting closes whatever the quote", word: "a\nb", quote: '"', want: `$'a\nb'`},
		{name: "ANSI-C quoting after HOME", word: "~/a\nb", tilde: true, want: `~/$'a\nb'`},
		{name: "utf-8 passes through", word: "日本 語", want: `日本\ 語`},
		{name: "an empty word", word: "", want: ""},
		{name: "an empty quoted word", word: "", quote: '"', final: true, want: `""`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, vsh.Insertion(tc.word, tc.quote, tc.final, tc.tilde))
		})
	}
}

func TestCommonPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cands []string
		want  string
	}{
		{cands: []string{"abc"}, want: "abc"},
		{cands: []string{"abc", "abd"}, want: "ab"},
		{cands: []string{"abc", "abd", "xyz"}, want: ""},
		{cands: []string{"abc", "ab"}, want: "ab"},
		{cands: []string{"", "a"}, want: ""},
		{cands: []string{"日本", "日本語"}, want: "日本"},
		{cands: []string{"é1", "è2"}, want: ""},
		{cands: []string{"aé", "aè"}, want: "a"},
		{cands: []string{"😀", "😁"}, want: ""},
		{cands: []string{"x😀", "x😀y"}, want: "x😀"},
	}
	for _, tc := range tests {
		t.Run(strings.Join(tc.cands, ","), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, vsh.CommonPrefix(tc.cands))
		})
	}
}

func TestFormatList(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cands []string
		width int
		want  string
	}{
		{name: "one", cands: []string{"a"}, width: 80, want: "a\r\n"},
		{name: "columns two cells apart", cands: []string{"agent/", "appearance/"}, width: 80,
			want: "agent/       appearance/\r\n"},
		{name: "down the columns", cands: []string{"a", "b", "c", "d", "e"}, width: 5,
			want: "a  d\r\nb  e\r\nc\r\n"},
		{name: "a full row", cands: []string{"aa", "bb", "cc"}, width: 10, want: "aa  bb  cc\r\n"},
		{name: "one cell short of a column", cands: []string{"aa", "bb", "cc"}, width: 9, want: "aa  cc\r\nbb\r\n"},
		{name: "one column when the names are wider", cands: []string{"long-name", "x"}, width: 4,
			want: "long-name\r\nx\r\n"},
		{name: "no width is one column", cands: []string{"a", "b"}, width: 0, want: "a\r\nb\r\n"},
		{name: "paths show their last element", cands: []string{"docs/guide.md", "docs/guides/", "../", "~/"}, width: 80,
			want: "guide.md  guides/   ../       ~/\r\n"},
		{name: "wide characters take two cells", cands: []string{"日本", "ab", "cd"}, width: 16,
			want: "日本  ab    cd\r\n"},
		{name: "wide characters narrow the columns", cands: []string{"日本", "ab", "cd"}, width: 15,
			want: "日本  cd\r\nab\r\n"},
		{name: "wide characters pad by cells", cands: []string{"日", "abcd"}, width: 80, want: "日    abcd\r\n"},
		{name: "control characters show as question marks", cands: []string{"a\nb", "c\x1bd"}, width: 80,
			want: "a?b  c?d\r\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, string(vsh.FormatList(tc.cands, tc.width)))
		})
	}
}

func TestDisplayName(t *testing.T) {
	t.Parallel()
	tests := []struct{ cand, want string }{
		{cand: "cat", want: "cat"},
		{cand: "a.md", want: "a.md"},
		{cand: "docs/a.md", want: "a.md"},
		{cand: "docs/guides/", want: "guides/"},
		{cand: "/abs/x", want: "x"},
		{cand: "/", want: "/"},
		{cand: "~/", want: "~/"},
		{cand: "../", want: "../"},
		{cand: "a/../", want: "../"},
		{cand: "a//b", want: "b"},
		{cand: "new\nline", want: "new?line"},
	}
	for _, tc := range tests {
		t.Run(tc.cand, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, vsh.DisplayName(tc.cand))
		})
	}
}

func TestIsAssignment(t *testing.T) {
	t.Parallel()
	tests := []struct {
		w    string
		want bool
	}{
		{w: "A=1", want: true},
		{w: "a_b9=", want: true},
		{w: "_=x", want: true},
		{w: "A+=1", want: true},
		{w: "A=b=c", want: true},
		{w: "A", want: false},
		{w: "=1", want: false},
		{w: "+=1", want: false},
		{w: "9A=1", want: false},
		{w: "a-b=1", want: false},
		{w: "a b=1", want: false},
		{w: "A++=1", want: false},
		{w: "--opt=1", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.w, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, vsh.IsAssignment(tc.w))
		})
	}
}
