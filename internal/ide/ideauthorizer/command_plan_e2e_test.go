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

//go:build e2e

package ideauthorizer

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"mvdan.cc/sh/v3/syntax"
)

// zshPassthroughs are the programs whose stubs run the real program after
// logging, so that the programs they start are logged too.
var zshPassthroughs = []string{"bash", "sh", "zsh", "env", "nice", "nohup", "xargs", "find"}

// zshHarness runs scripts with zsh -f, with nothing on its PATH but stubs
// that log the arguments they are started with.
type zshHarness struct {
	zsh, bin string
}

// zshRun is what a script started.
type zshRun struct {
	// started are the arguments of every stub started, its name first.
	started [][]string
	// notFound are the commands the shells found no program for.
	notFound []string
	log      string
}

var (
	commandNames    = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_-]*`)
	notFoundMessage = regexp.MustCompile(`(?m)command not found: (.+)$|: (\S+): command not found$`)
)

func lookPathZsh(t *testing.T) string {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh is not installed")
	}
	return zsh
}

// newZshHarness writes a stub for every name in scripts that could be a
// command, so that zsh finds a program for each.
func newZshHarness(t *testing.T, scripts ...string) zshHarness {
	t.Helper()
	h := zshHarness{zsh: lookPathZsh(t), bin: t.TempDir()}
	stubs := map[string]string{}
	for _, s := range scripts {
		for _, name := range commandNames.FindAllString(s, -1) {
			stubs[name] = ""
		}
	}
	for _, name := range zshPassthroughs {
		if real, err := exec.LookPath(name); err == nil {
			stubs[name] = real
		}
	}
	// Arguments end in \037 and invocations in \036, which no argument of
	// the scripts holds. Each process logs to a file of its own, so that
	// stubs running at once do not interleave their records.
	const logArgs = `printf '%s\037' "${0##*/}" "$@" >> "$STUB_LOG.$$"; printf '\036' >> "$STUB_LOG.$$"` + "\n"
	for name, real := range stubs {
		stub := "#!/bin/sh\n" + logArgs
		if real != "" {
			q, err := syntax.Quote(real, syntax.LangPOSIX)
			require.NoError(t, err)
			stub += "exec " + q + ` "$@"` + "\n"
		}
		require.NoError(t, os.WriteFile(filepath.Join(h.bin, name), []byte(stub), 0o755))
	}
	return h
}

// run runs script with zsh -f in a directory holding the files a and b,
// the directory sub and the script s.zsh.
func (h zshHarness) run(t *testing.T, script string) zshRun {
	t.Helper()
	dir := t.TempDir()
	r := zshRun{log: filepath.Join(dir, ".stub-log")}
	for name, data := range map[string]string{"a": "", "b": "", "s.zsh": "rm sourced\n"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644))
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.zsh, "-f", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + h.bin, "STUB_LOG=" + r.log, "HOME=" + dir}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	// What matters is what the script starts, not whether it succeeds.
	_ = cmd.Run()
	require.NoError(t, ctx.Err(), "zsh did not finish")
	r.read(t)
	for _, m := range notFoundMessage.FindAllStringSubmatch(stderr.String(), -1) {
		r.notFound = append(r.notFound, m[1]+m[2])
	}
	return r
}

func (r *zshRun) read(t *testing.T) {
	t.Helper()
	logs, err := filepath.Glob(r.log + ".*")
	require.NoError(t, err)
	r.started = nil
	for _, log := range logs {
		data, err := os.ReadFile(log)
		require.NoError(t, err)
		for rec := range strings.SplitSeq(string(data), "\036") {
			if rec != "" {
				r.started = append(r.started, strings.Split(strings.TrimSuffix(rec, "\037"), "\037"))
			}
		}
	}
}

// startedEventually reports whether the script started name, waiting for
// the process substitutions zsh does not wait for.
func (r *zshRun) startedEventually(t *testing.T, name string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if slices.ContainsFunc(r.started, func(argv []string) bool { return argv[0] == name }) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
		r.read(t)
	}
}

// showsProgram reports whether inv is the program argv a shell started:
// a file of the same name, given the literal arguments inv tells, and
// only those when inv tells them all.
func showsProgram(inv programInvocation, argv []string) bool {
	if inv.Opaque || inv.Shell >= 0 || filepath.Base(inv.Name) != argv[0] {
		return false
	}
	args := argv[1:]
	if len(args) < len(inv.Args) || !slices.Equal(args[:len(inv.Args)], inv.Args) {
		return false
	}
	return !inv.Complete || len(args) == len(inv.Args)
}

// assertPlanCovers asserts that what the authorizer approves of plan
// covers everything run started: each program is one the plan shows,
// with the literal arguments it shows, and each rm or rmdir that deletes
// directories is one the prompt shows as it runs or as unresolved.
func assertPlanCovers(t *testing.T, plan commandPlan, run zshRun) {
	t.Helper()
	require.False(t, plan.Opaque, plan.Reason)
	if slices.ContainsFunc(plan.Programs, func(inv programInvocation) bool {
		return inv.Opaque && inv.Role == roleScoped
	}) {
		// Only an approval of the exact command covers a program whose
		// name is not known until it runs.
		return
	}
	for _, argv := range run.started {
		assert.True(t, slices.ContainsFunc(plan.Programs, func(inv programInvocation) bool {
			return showsProgram(inv, argv)
		}), "zsh started %q, which the plan does not show: %q", argv, describePlan(plan))
		if !deletesDirectories(argv[0], literalWords(argv[1:])) {
			continue
		}
		fields := []string{argv[0]}
		for _, a := range argv[1:] {
			fields = append(fields, displayField(a))
		}
		shown := strings.Join(fields, " ")
		assert.True(t, slices.ContainsFunc(plan.Deletes, func(d RecursiveDelete) bool {
			return d.Unresolved || d.Command == shown
		}), "zsh ran %q, which the prompt shows as %q", shown, plan.Deletes)
	}
	for _, name := range run.notFound {
		assert.True(t, slices.ContainsFunc(plan.Programs, func(inv programInvocation) bool {
			return !inv.Opaque && inv.Shell < 0 && inv.Name == name
		}), "zsh looked %q up, which the plan does not show: %q", name, describePlan(plan))
	}
}

// zshFollowed are scripts whose programs the authorizer follows.
var zshFollowed = []string{
	// Lists, pipelines and compound commands.
	"ls",
	"ls; ls -l",
	"ls\ngit status\n\nmake test",
	"ls && git status || make",
	"ls || git status && make",
	"! ls && git status",
	"ls | head -1 | cat",
	"ls |& cat",
	"ls & wait",
	"{ ls; cat a }",
	"{ ls }",
	"(ls; cat a)",
	"(cd sub && ls)",
	"if ls; then cat a; elif git status; then make; else rm -f x; fi",
	"if [[ -e a ]]; then ls; fi",
	"if (( 1 > 0 )); then ls; fi",
	"while false; do ls; done; until true; do make; done",
	"for f in a b; do cat $f; done",
	"for f; do ls $f; done",
	"for ((i = 0; i < 2; i++)); do ls $i; done",
	"case a in a) ls;; b) cat;; esac",
	"case a in (a|b) ls;; esac",
	"case a in a) ls ;| *) cat a ;; esac",
	"case a in a) ls ;& b) cat a ;; esac",
	"select x in a; do ls; break; done",
	"time ls",
	"ls # rm x",
	"ls \\\n  -l",
	"ls; exit 0; rm x",
	"ls &!",
	"ls &|",
	"{ ls } 2>/dev/null",
	"function { ls }",
	"true || exit; ls",
	"if ! ls; then cat a; fi",
	"time (ls; cat a)",
	"ls; return; rm x",
	"[[ -n x ]] || ls",
	"while false; do ls; done; [[ -e a ]] && rm x",
	"[[ a = (a|b) ]] && ls",
	`[[ a = *(e:'rm x':) || -e a(e:'rm y':) ]] || ls`,
	`case a in *(e:'rm x':)) ls ;; esac`,
	"ls; #comment\nmake",

	// Words, quoting and expansions.
	`ls 'a b' "c d" e\ f $'g\th' ''`,
	`ls "$HOME" ${HOME} $HOME/x`,
	"ls ~ ~/x ~+ ~-",
	"ls =ls",
	"ls * a? [ab]",
	"ls {a,b} {1..3} {a-c}",
	`x='a b'; ls $x $=x ${=x}`,
	"x=a; ls ${x:-d} ${x:+alt} ${#x} ${x//a/b} ${x:h} ${x:t} $x:r ${x:u}",
	"ls ${+x} $+x ${#x}",
	"x=(a b); ls ${x[1]} $x[1,2] ${x[(r)a]} ${x[-1]}",
	"ls ${${x}:-y}",
	"ls $((1 + 2)) $[1+2]",
	"ls $(git rev-parse HEAD) `git log`",
	`ls "$(git log)" "${x}" "a$(make)b"`,
	"diff <(ls a) >(cat) =(ls b)",
	`ls $# $? $$ $- $0 "$@" $*`,
	`ls a\(b\) 'c(d)' "e(f)"`,
	"ls a^b a~b a#",
	`ls '*' "?" \*`,
	"ls 2>(cat)",
	"cat 0<(ls a)",
	"ls $(cat $(git log))",
	"ls `echo \\`git log\\``",
	"x=a; ls ${x/a/$(make)} ${x:-$(git log)} ${x:+$(cat a)}",
	"[[ $(git log) == x ]] && ls",
	"case $(git log) in x) ls;; esac",
	"ls >$(make)",
	`ls "$(git log | head -1)"`,
	"ls <<< \"$(git log)\"",
	"echo $(<a); x=$(< a); y=`<b`; ls $x $y",
	"cat $(<$(git ls-files))",
	`x=$'a\nb'; ls $x $'\x41' $'\u00e9'`,
	"x=(a b); ls \"${x[@]}\" ${#x} ${x:-y}",
	"x=a.b; ls ${x:s/a/b/} ${x:e} ${x:r} ${x#*.} ${x%%.*} ${x:#a}",
	"ls ${#} ${##} ${#*} ${x:-~/y}",
	"x=(a b); ls ${x[(i)a]} ${x[(I)a]} $=x[1]",
	"a=(x); ls ${a:|b} ${a:*a}",
	"ls $(( 1 + 2 )) $(( 0x10 )) $(( 2#101 ))",
	"ls ~root ~+/a ~-/a",
	"ls {1..3} {a..c} {01..03} x{,y}",
	"ls x ^a",
	"a=(1); ls x$a[1] x${a[1]-}",
	"ls >(cat) <(cat a)",
	"cat <(ls) <(git log)",
	"ls > >(make)",

	// Assignments and declarations.
	"x=1 ls",
	"x=1; ls $x",
	"export x=1; ls",
	"typeset -i n=1; integer m=2; float f=1.5; readonly r=1; ls",
	"typeset -A m; m[k]=v; ls ${m[k]}",
	"a=(1 2); a+=(3); a[1]=x; ls $a",
	"x=$(git log); ls $x",
	"local x=1; ls",
	"typeset -a arr=(a b); ls $arr",
	"local -a x; x=(a); ls $x",
	"x=$(git log) && ls",

	// Redirections and here-documents.
	"ls > out 2>&1; cat < out",
	"ls >| out; ls >! out; ls >> out",
	"ls &> out; ls &>> out",
	"cat <<< hi",
	"cat <<< $(git log)",
	"cat <<EOF\n$(git rev-parse HEAD) f(x)\nEOF",
	"cat <<'EOF'\n$(rm -rf x)\nEOF",
	"cat <<-EOF\n\tls $(make)\n\tEOF",
	"exec 3> out; ls >&3; exec 3>&-",
	"ls 2>/dev/null",
	"ls 2>&1 >/dev/null",
	"ls a b >out <a",
	"ls 2>&- 1>&2",
	"exec 2>/dev/null; ls",
	"ls |& tee out",
	"make 2>&1 | tee log",
	"exec 3<a; read -u3 l; ls",

	// Functions.
	"f() { ls }; f",
	"function f { ls }; f",
	"function f g { ls }; f; g",
	"f() ls; f",
	"() { ls }",
	"f() { rm -rf x }; f; f",
	"TRAPEXIT() { git status }; ls",
	"chpwd() { make }; cd sub",
	"f() { g() { make }; g }; f",
	"a() { b }; b() { make }; a",
	"make() { command make -j4 }; make",
	"git status; git() { :; }; git log",
	"if false; then make() { :; }; fi; make x",
	"true && make() { :; }; make v",
	"f() { :; } && git status",
	"f() rm x; f",
	"zshexit() { rm x }; ls",
	"TRAPZERR() { make }; false; ls",

	// Builtins.
	`print hi; print -r -- a; print -n x; print -l a b; print -f '%s\n' a; print -u2 err`,
	`printf '%s\n' a; printf -v x %s a; ls $x`,
	`echo -n hi; echo -e 'a\tb'; echo -E x`,
	"test -e a && ls; [ -e a ] && ls",
	"[[ -e a && -n x ]] && ls",
	"read x <<< hi; ls $x",
	"read -A arr <<< 'a b'; ls $arr",
	"read 'x?prompt> ' <<< hi; ls $x",
	"pwd; cd sub; pwd; cd -; pushd sub; popd; dirs; ls",
	"kill -0 $$; ls",
	"type ls; whence -v ls; where ls; which ls",
	"getopts ab opt; ls",
	"set -e; set -u; set -x; ls",
	"set -- a b; ls $1",
	"set a b; shift; ls $1",
	"true; false; :; ls",
	"unset x; unset -v x; ls",
	"umask 022; ulimit -n; ls",
	"jobs; wait; ls",
	"let x=1; (( x++ )); ls $x",
	"rehash; ls",
	"unalias ls 2>/dev/null; ls",
	"exec ls",
	"exec -a name ls",
	"builtin print hi; ls",
	"command ls; command -v ls; command -V ls",
	"command echo hi",
	"shopt -s x",
	"print -u2 x; ls",
	"pushd -q sub; ls; popd -q",
	"builtin cd sub; ls",
	"whence -p ls; ls",
	"git log | while read -r h s; do ls $h; done",
	"read -k1 c <<< y; read -q d <<< y; ls",
	`"rm" -rf x`,
	"rm x &>/dev/null",

	// Wrappers.
	"env FOO=1 ls",
	"env -u FOO ls -l",
	"nice -n 1 ls",
	"nohup ls",
	"xargs ls <<< a",
	"ls | xargs cat",
	`find . -maxdepth 1 -name a -exec ls {} \;`,
	"find . -maxdepth 1 -name a -exec cat {} +",
	"exec env ls",
	"exec -c ls",
	"exec -l ls",
	"command -p ls",
	"nohup ls &",

	// Shells in the script.
	"bash -c 'ls; git status'",
	"sh -c 'ls && make'",
	"zsh -c 'ls'",
	`zsh -fc "bash -c 'git log'"`,
	"bash -c 'echo a^b'; echo a^b",
	`bash -c "zsh -c 'cat a'"`,
	"zsh -c 'ls' &",
	"bash -c 'x=$(git log); ls'",
	`zsh -c "zsh -c 'zsh -c ls'"`,
	"zsh -fc 'ls $1' _ a b",
	"zsh -e -c ls; zsh -x -c ls",
	"sh -c 'for f in a; do cat \"$f\"; done'",

	// Recursive deletes.
	"rm -rf x y",
	`rm -r "a b" -- c`,
	"D=x; rm -rf $D",
	"rm -rf ~/x",
	`rm -rf "$(pwd)/x"`,
	"for d in a b; do rm -rf $d; done",
	"find . -name nothing -exec rm -rf {} +",
	"rmdir x y",
	"D=x; rmdir -p $D/y",
	"rmdir ~/x",
	"find . -type d -empty -exec rmdir {} +",
}

// zshOpaque are scripts the authorizer only approves exactly. hides names
// a program zsh starts for the script that a plan following the script
// as written would miss, or is empty when it only needs options of the
// user's startup files to run code.
var zshOpaque = []struct{ script, hides string }{
	{`ls *(e:'rm x':)`, "rm"},
	{`ls a(e:'rm x':)`, "rm"},
	{`x=a; ls $x(e:rm\ x:)`, "rm"},
	{`x='*(e:rm x:)'; ls ${~x}`, "rm"},
	{`x='*(e:rm x:)'; ls $~x`, "rm"},
	{`a=(*(e:'rm x':)); ls`, "rm"},
	{`for f in *(e:'rm x':); do :; done`, "rm"},
	{"ls >>(rm x)", "rm"},
	{"ls 2>>(rm x)", "rm"},
	{"cat <<(rm x)", "rm"},
	{"ls &>(rm x)", ""},
	{`x='$(rm x)'; print ${(e)x}`, "rm"},
	{`print -P '%~'`, ""},
	{`x='a b'; ls ${(z)x}`, ""},
	{`emulate sh -c 'rm x'`, "rm"},
	{`setopt globsubst; x='*(e:rm x:)'; ls $x`, "rm"},
	{`set -o globsubst; x='*(e:rm x:)'; ls $x`, "rm"},
	{`functions[ls]='rm x'; ls`, "rm"},
	{"alias ls='rm x'\nls", ""},
	{`commands[ls]=$commands[rm]; ls`, "rm"},
	{`hash ls=$commands[rm]; ls`, "rm"},
	{"NULLCMD=rm; > x", "rm"},
	{"READNULLCMD=rm; < a", "rm"},
	{"exec eval 'rm x'", "rm"},
	{"eval 'rm x'", "rm"},
	{"trap 'rm x' EXIT", "rm"},
	{"source ./s.zsh", "rm"},
	{". ./s.zsh", "rm"},
	{"repeat 2 rm x", "rm"},
	{"foreach i (a) rm $i; end", "rm"},
	{"noglob rm x", "rm"},
	{"nocorrect rm x", "rm"},
	{"ls; - rm x", "rm"},
	{`a=(1); x='a[$(rm x)]'; echo $((x))`, "rm"},
	{`a=(1); x='a[$(rm x)]'; for ((i = x; i < 0; i++)); do :; done`, "rm"},
	{`a=(1); x='a[$(rm x)]'; b[x]=1`, "rm"},
	{"make() { :; }; unset -f make; make y", "make"},
	{"make() { :; }; unfunction make; make y", "make"},
	{"zsh -o globsubst -c ls", ""},
	{"ARGV0=sh zsh -c ls", ""},
	{"print -v path x; ls", ""},
	{"read path <<< .; ls", ""},
	{"for path in .; do ls; done", ""},
	{"exec {path}>x; ls", ""},
	{": ${LD_PRELOAD:=x}; ls", ""},
	{`zsh -c 'NULLCMD=rm; > y'`, "rm"},
	{"cat $(<a 2>/dev/null)", ""},
	{"cat $(<a <b)", ""},
	{`f() { rm x }; ls *(+f)`, "rm"},
	{"ls *(N)", ""},
	{`setopt extendedglob; ls *(#qe:'rm x':)`, "rm"},
	{`set -o globassign; x=*(e:'rm x':)`, "rm"},
	{"x=(a b); ls \"${(@)x}\"", ""},
	{"print ${(q)x}", ""},
	{`\eval 'rm x'`, "rm"},
	{`'eval' 'rm x'`, "rm"},
	{`e"va"l 'rm x'`, "rm"},
	{`builtin eval 'rm x'`, "rm"},
	{`command eval 'rm x'`, "eval"},
	{"=rm -rf x", "rm"},
	{"x='rm y'; $=x", "rm"},
	{"x=rm; $x -rf y", "rm"},
	{"x=ls; ${x}", "ls"},
	{"zmodload zsh/files; rm -rf x", ""},
	{"autoload -U x; x", ""},
	{"emulate -L zsh; ls", ""},
	{"disable -r if; ls", ""},
	{"enable -r if; ls", ""},
	{"unhash ls; ls", ""},
	{"typeset -f f; ls", ""},
	{"nocorrect ls", ""},
	{"precmd_functions+=(f); ls", ""},
	{"alias ls=rm", ""},
	{"zsh -o errexit -c ls", ""},
	{"ls () { :; }; ls", ""},
	{"repeat 1 rm x", "rm"},
}

func TestPlanCommandZshCoversWhatZshStarts(t *testing.T) {
	scripts := slices.Clone(zshFollowed)
	for _, c := range zshOpaque {
		scripts = append(scripts, c.script)
	}
	h := newZshHarness(t, scripts...)
	for _, script := range zshFollowed {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			plan := planCommand(h.zsh, []string{"-f", "-c", script}, nil, nil)
			assertPlanCovers(t, plan, h.run(t, script))
		})
	}
	for _, c := range zshOpaque {
		t.Run(c.script, func(t *testing.T) {
			t.Parallel()
			plan := planCommand(h.zsh, []string{"-f", "-c", c.script}, nil, nil)
			assert.True(t, plan.Opaque, "%q", describePlan(plan))
			if c.hides == "" {
				return
			}
			run := h.run(t, c.script)
			assert.True(t, run.startedEventually(t, c.hides), "zsh did not start %s", c.hides)
		})
	}
}

// zshUnparsed are scripts zsh runs that the parser does not support, so
// that the authorizer only approves them exactly. Each runs rm.
var zshUnparsed = []string{
	"for f (a) { rm $f }",
	"for f (a) rm $f",
	"for f in a; rm $f",
	"for ((i = 0; i < 1; i++)) { rm x }",
	"for ((i = 0; i < 1; i++)) rm x",
	"if [[ -e a ]] rm x",
	"repeat 1 { rm x }",
	"case a { a) rm x ;; }",
	"() { rm $1 } x",
	"function { rm $1 } x",
	"{ ls } always { rm x }",
	"a=(x); rm ${a:^a}",
}

func TestPlanCommandZshSyntaxTheParserLacks(t *testing.T) {
	h := newZshHarness(t, zshUnparsed...)
	for _, script := range zshUnparsed {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			_, err := syntax.NewParser(syntax.Variant(syntax.LangZsh)).Parse(strings.NewReader(script), "")
			assert.Error(t, err, "the parser supports it now, so it belongs in zshFollowed")
			plan := planCommand(h.zsh, []string{"-f", "-c", script}, nil, nil)
			assert.Equal(t, []string{"opaque: its script does not parse as zsh"}, describePlan(plan))
			run := h.run(t, script)
			assert.True(t, run.startedEventually(t, "rm"), "zsh did not run it")
		})
	}
}

// zshLoadModules loads every module of zsh but zsh/files and zsh/stat,
// which name builtins after the programs they replace, such as rm and
// stat, that the authorizer plans as those programs: a script only gets
// them by loading the module, which it cannot follow. zsh/example prints
// when it loads.
const zshLoadModules = `for f in $^module_path/zsh/**/*.(so|bundle)(N); do
  m=${f##*/zsh/}; m=zsh/${m%.*}
  [[ $m == zsh/(files|stat|example) ]] || zmodload -i $m 2>/dev/null
done
`

func TestZshWordsCoverZsh(t *testing.T) {
	zsh := lookPathZsh(t)
	const script = zshLoadModules + `zmodload -F -m zsh/files 'b:zf_*'
zmodload -F -m zsh/stat b:zstat
print -rl -- ${(k)builtins} ${(k)reswords}`
	out, err := exec.Command(zsh, "-f", "-c", script).Output()
	require.NoError(t, err)
	words := strings.Fields(string(out))
	require.Contains(t, words, "zf_rm", "zsh loaded no module")
	var missing []string
	for _, w := range words {
		if !zshWords[w] {
			missing = append(missing, w)
		}
	}
	assert.Empty(t, missing, "zsh words the authorizer does not know, so it would look them up on PATH")
}

func TestZshArithmeticRunsHiddenSubstitutions(t *testing.T) {
	scripts := make([]string, len(zshArithmetic))
	for i, ctx := range zshArithmetic {
		scripts[i] = "a=(1)\nx='a[$(hit)]'\n" + ctx
	}
	h := newZshHarness(t, scripts...)
	for i, ctx := range zshArithmetic {
		t.Run(ctx, func(t *testing.T) {
			t.Parallel()
			run := h.run(t, scripts[i])
			assert.True(t, run.startedEventually(t, "hit"), "zsh did not evaluate x")
		})
	}
}

// zshDataSetup writes the file d holding a[$(hit)], and a file of that
// name in sub, for zshDataArithmetic to read.
const zshDataSetup = "a=(1)\nprint -r -- 'a[$(hit)]' > d\n: > 'sub/a[$(hit)]'\n"

// zshDataArithmetic are scripts whose arithmetic evaluates data they get
// while they run: the contents of d, or the names of the files in sub.
var zshDataArithmetic = []string{
	"x=$(<d); echo $((x))",
	"read x < d; [[ $x -eq 1 ]]",
	"echo $(( $(<d) ))",
	"while read -r l; do (( l )); done < d",
	"for f in sub/*; do (( f )); done",
	"set -- sub/*; (( $1 ))",
	"f() { (( $1 )) }; f sub/*",
	"x=(sub/*); (( x[1] ))",
	`find sub -type f -exec zsh -fc 'a=(1); (( $1 ))' _ {} \;`,
	"print -rl -- sub/* | xargs zsh -fc 'a=(1); (( $1 ))' _",
}

func TestPlanCommandZshArithmeticOnData(t *testing.T) {
	h := newZshHarness(t, append([]string{zshDataSetup}, zshDataArithmetic...)...)
	for _, script := range zshDataArithmetic {
		t.Run(script, func(t *testing.T) {
			t.Parallel()
			plan := planCommand(h.zsh, []string{"-f", "-c", script}, nil, nil)
			assert.Equal(t, []string{
				"opaque: its arithmetic may run command substitutions hidden in strings",
			}, describePlan(plan))
			run := h.run(t, zshDataSetup+script)
			assert.True(t, run.startedEventually(t, "hit"), "zsh did not evaluate the data")
		})
	}
}

// integersOf returns the variables of shell whose assignments run the
// command substitutions of array subscripts in the value, setting each
// variable the script lists in $names in a subshell of its own.
func integersOf(t *testing.T, shell []string, script string) []string {
	t.Helper()
	hits := filepath.Join(t.TempDir(), "hits")
	script += `
a=(1)
for n in $names; do
  x="a[\$(echo $n >> $HITS)]"
  ( eval "$n=\$x" ) </dev/null >/dev/null 2>&1
done
:`
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell[0], append(shell[1:], "-c", script)...)
	cmd.Env = []string{"HITS=" + hits, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	data, err := os.ReadFile(hits)
	require.NoError(t, err)
	return strings.Fields(string(data))
}

func TestZshIntegersCoverZsh(t *testing.T) {
	zsh := lookPathZsh(t)
	names := integersOf(t, []string{zsh, "-f"}, zshLoadModules+
		"setopt extendedglob\n"+`names=(${(M)${(k)parameters}:#[A-Za-z_][A-Za-z0-9_]#})`)
	require.Contains(t, names, "SECONDS")
	for _, n := range names {
		assert.True(t, zshBuiltins.integers[n], "assigning %s evaluates arithmetic", n)
	}
}

func TestBashIntegersCoverBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	names := integersOf(t, []string{bash}, `names=$(compgen -v)`)
	require.Contains(t, names, "OPTIND")
	for _, n := range names {
		assert.True(t, bashBuiltins.integers[n], "assigning %s evaluates arithmetic", n)
	}
}

func TestZshBuiltinsTheAuthorizerFollowsAreZshBuiltins(t *testing.T) {
	zsh := lookPathZsh(t)
	followed := slices.Sorted(maps.Keys(zshBuiltins.noProgram))
	followed = append(followed, slices.Sorted(maps.Keys(zshBuiltins.programs))...)
	followed = append(followed, slices.Sorted(maps.Keys(zshWrappers))...)
	out, err := exec.Command(zsh, append([]string{"-f", "-c",
		`for w; do print -r -- "$w ${$(whence -w -- $w)##*: }"; done`, "zsh"}, followed...)...).Output()
	require.NoError(t, err)
	for line := range strings.Lines(string(out)) {
		word, kind, _ := strings.Cut(strings.TrimSpace(line), " ")
		assert.Contains(t, []string{"builtin", "reserved"}, kind, word)
	}
}
