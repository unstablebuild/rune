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
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// programKind says how the authorizer follows a program to the programs
// it starts. Programs missing from programSpecs are plain: they are
// scoped by their own arguments and assumed to start nothing else.
type programKind int

const (
	kindPlain programKind = iota
	// kindTransparent programs run the command that follows their
	// options (env, nice, timeout). They are no scope of their own: the
	// command they run is, and they are bound by fingerprint only.
	kindTransparent
	// kindEscalating programs run the command that follows their options
	// as another user (sudo). They keep their scope, every row of which
	// runs arbitrary code, and bind the command they run.
	kindEscalating
	// kindSpawning utilities run commands embedded in their arguments
	// (find -exec), which are programs of their own.
	kindSpawning
	// kindShell programs run a script given with -c.
	kindShell
	// kindInterpreter programs run code some of their arguments carry.
	kindInterpreter
	// kindOpaque programs run commands the authorizer cannot follow, such
	// as scripts in a language it cannot parse or commands on another
	// host.
	kindOpaque
)

type programSpec struct {
	kind programKind
	opts optionSpec
	// builtin wrappers run inside the shell, so there is no file to bind.
	builtin bool
	// operands counts the operands that precede the command a wrapper
	// runs, such as timeout's DURATION.
	operands int
	// envAssign reports that NAME=VALUE operands before the command set
	// its environment.
	envAssign bool
	// envOpts are options whose value names a variable the command gets
	// or loses (env -u, strace -E).
	envOpts []string
	// opaque options make the command impossible to follow (env -S).
	opaque []string
	// none options mean no command runs (command -v).
	none []string
	// stdPATH options look the command up in the standard PATH.
	stdPATH []string
	// appends reports that the wrapper adds arguments to the command it
	// runs, which therefore never runs exactly as written.
	appends bool
	// fallback is the command a wrapper runs when none follows.
	fallback string
	// requireCommand makes a wrapper without a command opaque, because
	// it starts a shell then.
	requireCommand bool
	// commandStart, when set, finds the command a wrapper runs instead
	// of opts.
	commandStart func(args []shellWord) (start int, ok bool)
	// lang is the grammar of a kindShell script.
	lang syntax.LangVariant
	// code lists the options that carry code. Single-letter ones match
	// inside option clusters too (perl -ne).
	code []string
	// codeAlways reports that every invocation carries code (sed, awk).
	codeAlways bool
	// stdinCode reports that, without an operand, the program reads code
	// from its standard input.
	stdinCode bool
	// opaqueSub lists the subcommands that run commands the authorizer
	// cannot follow (docker run).
	opaqueSub []string
	// remoteCommand reports that operands after the first one are a
	// command for another host (ssh).
	remoteCommand bool
	// embedded returns the commands a kindSpawning utility runs, and the
	// index of the argument that starts the first of them.
	embedded func(args []shellWord) (cmds [][]shellWord, code int, ok bool)
	// scope returns the arguments that scope the program and the index of
	// the first that carries code, for programs whose global options
	// are left out of the scope (git -C).
	scope  func(args []string) (scope []string, code int)
	reason string
}

var bashOptions = optionSpec{
	short: "abefhkmnptuvxBCEHPTlircsDo:O:",
	long: []string{"login", "noprofile", "norc", "posix", "restricted", "verbose",
		"noediting", "debugger", "dump-strings", "dump-po-strings", "pretty-print",
		"rcfile=", "init-file=", "help", "version"},
	plus: true,
}

var posixShellOptions = optionSpec{short: "abCefhimnuvxEVlcso:", plus: true}

var programSpecs = map[string]programSpec{
	"env": {kind: kindTransparent, envAssign: true,
		opts: optionSpec{short: "0iu:C:S:v", long: []string{"ignore-environment", "null",
			"unset=", "chdir=", "split-string=", "block-signal=?", "default-signal=?",
			"ignore-signal=?", "list-signal-handling", "debug"}},
		envOpts: []string{"-u", "--unset"},
		opaque:  []string{"-i", "--ignore-environment", "-S", "--split-string", "-C", "--chdir"}},
	"command": {kind: kindTransparent, builtin: true, opts: optionSpec{short: "pvV"},
		none: []string{"-v", "-V"}, stdPATH: []string{"-p"}},
	"builtin": {kind: kindTransparent, builtin: true},
	"exec":    {kind: kindTransparent, builtin: true, opts: optionSpec{short: "a:cl"}},
	"nice": {kind: kindTransparent,
		opts: optionSpec{short: "n:0123456789", long: []string{"adjustment="}}},
	"nohup": {kind: kindTransparent},
	"time": {kind: kindTransparent, opts: optionSpec{short: "f:o:apqv",
		long: []string{"format=", "output=", "append", "portability", "quiet", "verbose"}}},
	"timeout": {kind: kindTransparent, operands: 1, opts: optionSpec{short: "k:s:v",
		long: []string{"kill-after=", "signal=", "preserve-status", "foreground", "verbose"}}},
	"stdbuf": {kind: kindTransparent, opts: optionSpec{short: "i:o:e:",
		long: []string{"input=", "output=", "error="}}},
	"setsid": {kind: kindTransparent, opts: optionSpec{short: "cfw",
		long: []string{"ctty", "fork", "wait"}}},
	"ionice": {kind: kindTransparent, opts: optionSpec{short: "c:n:tp:P:u:",
		long: []string{"class=", "classdata=", "ignore", "pid=", "pgid=", "uid="}},
		none: []string{"-p", "-P", "-u", "--pid", "--pgid", "--uid"}},
	"chrt": {kind: kindTransparent, operands: 1, opts: optionSpec{short: "abdefimoprRvT:P:D:",
		long: []string{"all-tasks", "batch", "deadline", "fifo", "idle", "max", "other",
			"pid", "rr", "reset-on-fork", "verbose", "sched-runtime=", "sched-period=",
			"sched-deadline="}},
		none: []string{"-p", "--pid", "-m", "--max"}},
	"taskset": {kind: kindTransparent, operands: 1, opts: optionSpec{short: "acp",
		long: []string{"all-tasks", "cpu-list", "pid"}},
		none: []string{"-p", "--pid"}},
	"caffeinate": {kind: kindTransparent, opts: optionSpec{short: "dimsut:w:"}},
	"unbuffer":   {kind: kindTransparent, opts: optionSpec{short: "p"}},
	"chronic":    {kind: kindTransparent, opts: optionSpec{short: "ev"}},
	"nocache":    {kind: kindTransparent},
	"ssh-agent": {kind: kindTransparent, opts: optionSpec{short: "cDdksa:E:O:P:t:"},
		none: []string{"-k"}},
	"strace": {kind: kindTransparent, opts: optionSpec{
		short: "ACdDfFhiqrtTvVwxyYzZa:b:e:E:I:o:O:p:P:s:S:u:U:X:",
		long: []string{"trace=", "signal=", "status=", "output=", "summary-only",
			"summary", "follow-forks", "output-separately", "string-limit=",
			"attach=", "user=", "env=", "decode-fds=?", "absolute-timestamps=?",
			"relative-timestamps=?", "syscall-times=?", "summary-columns=",
			"summary-sort-by=", "no-abbrev", "verbose=", "raw=", "read=", "write=",
			"quiet=?", "seccomp-bpf", "trace-path=", "decode-pids=", "timestamps=?",
			"instruction-pointer", "stack-trace", "kill-on-exit", "daemonize=?"}},
		envOpts: []string{"-E", "--env"},
		none:    []string{"-p", "--attach"}},
	"ltrace": {kind: kindTransparent, opts: optionSpec{
		short: "bcCfhiLrStTVa:A:D:e:F:l:n:o:p:s:u:w:x:",
		long:  []string{"output=", "library=", "indent=", "demangle", "align="}},
		none: []string{"-p"}},
	"valgrind": {kind: kindTransparent, opts: optionSpec{short: "hdqv", anyLong: true}},
	"perf":     {kind: kindTransparent, commandStart: perfCommandStart},
	"xargs": {kind: kindTransparent, appends: true, fallback: "echo", opts: optionSpec{
		short: "0a:d:E:e?I:i?L:l?n:oP:prs:tx",
		long: []string{"null", "arg-file=", "delimiter=", "eof=?", "replace=?",
			"max-lines=?", "max-args=", "open-tty", "max-procs=", "interactive",
			"no-run-if-empty", "max-chars=", "show-limits", "verbose", "exit",
			"process-slot-var="}}},

	"sudo": {kind: kindEscalating, envAssign: true, requireCommand: true, opts: optionSpec{
		short: "Aa:BbC:D:Eeg:Hh?iKklNnPp:R:r:SsT:t:U:u:Vv",
		long: []string{"askpass", "background", "bell", "close-from=", "chdir=",
			"preserve-env=?", "edit", "group=", "set-home", "help", "host=", "login",
			"remove-timestamp", "reset-timestamp", "list", "non-interactive",
			"preserve-groups", "prompt=", "chroot=", "role=", "stdin", "shell", "type=",
			"command-timeout=", "other-user=", "user=", "version", "validate"}},
		opaque: []string{"-s", "--shell", "-i", "--login", "-e", "--edit"},
		none: []string{"-l", "--list", "-v", "--validate", "-K", "--remove-timestamp",
			"-V", "--version", "--help"}},
	"doas": {kind: kindEscalating, requireCommand: true,
		opts:   optionSpec{short: "nsLu:C:"},
		opaque: []string{"-s"}, none: []string{"-L", "-C"}},
	"run0": {kind: kindEscalating, requireCommand: true, opts: optionSpec{
		short: "u:g:D:",
		long: []string{"user=", "group=", "nice=", "chdir=", "setenv=", "unit=",
			"property=", "description=", "slice=", "slice-inherit", "machine=",
			"background=", "pty", "pipe", "no-ask-password", "area=",
			"shell-prompt-prefix="}},
		envOpts: []string{"--setenv"}},
	"pkexec": {kind: kindEscalating, requireCommand: true, opts: optionSpec{
		long: []string{"user=", "disable-internal-agent", "keep-cwd", "help", "version"}}},

	"find":   {kind: kindSpawning, appends: true, embedded: findCommands},
	"gfind":  {kind: kindSpawning, appends: true, embedded: findCommands},
	"fd":     {kind: kindSpawning, appends: true, embedded: fdCommands},
	"fdfind": {kind: kindSpawning, appends: true, embedded: fdCommands},
	"entr": {kind: kindSpawning, appends: true, embedded: optionCommands(
		optionSpec{short: "acdnprsz"}, []string{"-s"}, 0)},
	"flock": {kind: kindSpawning, embedded: optionCommands(optionSpec{
		short: "sexnuow:E:c:",
		long: []string{"shared", "exclusive", "unlock", "nonblock", "nb", "timeout=",
			"conflict-exit-code=", "close", "command=", "no-fork", "verbose"}},
		[]string{"-c", "--command"}, 1)},

	"sh":    {kind: kindShell, lang: syntax.LangPOSIX, opts: posixShellOptions},
	"dash":  {kind: kindShell, lang: syntax.LangPOSIX, opts: posixShellOptions},
	"ash":   {kind: kindShell, lang: syntax.LangPOSIX, opts: posixShellOptions},
	"bash":  {kind: kindShell, lang: syntax.LangBash, opts: bashOptions},
	"ksh":   {kind: kindShell, lang: syntax.LangMirBSDKorn, opts: posixShellOptions},
	"mksh":  {kind: kindShell, lang: syntax.LangMirBSDKorn, opts: posixShellOptions},
	"ksh93": {kind: kindShell, lang: syntax.LangMirBSDKorn, opts: posixShellOptions},

	"python": {kind: kindInterpreter, code: []string{"-c", "-m"}, stdinCode: true},
	"pypy":   {kind: kindInterpreter, code: []string{"-c", "-m"}, stdinCode: true},
	"node": {kind: kindInterpreter, stdinCode: true, code: []string{"-e", "--eval", "-p",
		"--print", "-r", "--require", "--import", "--loader", "--experimental-loader"}},
	"nodejs": {kind: kindInterpreter, stdinCode: true, code: []string{"-e", "--eval", "-p",
		"--print", "-r", "--require", "--import", "--loader", "--experimental-loader"}},
	"bun": {kind: kindInterpreter, code: []string{"-e", "--eval", "-p", "--print",
		"-r", "--preload"}},
	"deno":      {kind: kindInterpreter, code: []string{"eval", "repl"}},
	"perl":      {kind: kindInterpreter, code: []string{"-e", "-E", "-M", "-m"}, stdinCode: true},
	"ruby":      {kind: kindInterpreter, code: []string{"-e", "-r"}, stdinCode: true},
	"irb":       {kind: kindInterpreter, codeAlways: true},
	"php":       {kind: kindInterpreter, code: []string{"-r", "-B", "-R", "-E"}, stdinCode: true},
	"lua":       {kind: kindInterpreter, code: []string{"-e", "-l"}, stdinCode: true},
	"luajit":    {kind: kindInterpreter, code: []string{"-e", "-l"}, stdinCode: true},
	"osascript": {kind: kindInterpreter, code: []string{"-e"}, stdinCode: true},
	"Rscript":   {kind: kindInterpreter, code: []string{"-e"}},
	"julia":     {kind: kindInterpreter, code: []string{"-e", "--eval", "-E", "--print"}},
	"awk":       {kind: kindInterpreter, codeAlways: true},
	"gawk":      {kind: kindInterpreter, codeAlways: true},
	"mawk":      {kind: kindInterpreter, codeAlways: true},
	"nawk":      {kind: kindInterpreter, codeAlways: true},
	"sed":       {kind: kindInterpreter, codeAlways: true},
	"gsed":      {kind: kindInterpreter, codeAlways: true},
	"busybox":   {kind: kindInterpreter, codeAlways: true},
	"toolbox":   {kind: kindInterpreter, codeAlways: true},
	"distrobox": {kind: kindInterpreter, codeAlways: true},
	"make":      {kind: kindInterpreter, code: []string{"-f", "--file", "--makefile", "--eval", "-E"}},
	"gmake":     {kind: kindInterpreter, code: []string{"-f", "--file", "--makefile", "--eval", "-E"}},
	"git":       {kind: kindInterpreter, scope: gitScope},
	"tar": {kind: kindInterpreter, code: []string{"-I", "-F", "--to-command",
		"--use-compress-program", "--checkpoint-action", "--info-script",
		"--new-volume-script", "--rsh-command"}},
	"rsync": {kind: kindInterpreter, code: []string{"-e", "--rsh", "--rsync-path"}},
	"scp":   {kind: kindInterpreter, code: []string{"-S", "-o", "-F"}},
	"ssh": {kind: kindInterpreter, remoteCommand: true, code: []string{"-o", "-F"},
		opts: optionSpec{short: "46AaCfGgKkMNnqsTtVvXxYyB:b:c:D:E:e:F:I:i:J:L:l:m:O:o:P:p:Q:R:S:W:w:"}},
	"docker":  {kind: kindInterpreter, opaqueSub: []string{"run", "exec", "compose", "buildx"}},
	"podman":  {kind: kindInterpreter, opaqueSub: []string{"run", "exec", "compose"}},
	"kubectl": {kind: kindInterpreter, opaqueSub: []string{"exec", "run", "debug", "attach"}},
	"nix":     {kind: kindInterpreter, opaqueSub: []string{"develop", "shell", "run"}},

	"zsh":        {kind: kindOpaque, reason: "zsh scripts cannot be parsed"},
	"fish":       {kind: kindOpaque, reason: "fish scripts cannot be parsed"},
	"nu":         {kind: kindOpaque, reason: "nu scripts cannot be parsed"},
	"pwsh":       {kind: kindOpaque, reason: "PowerShell scripts cannot be parsed"},
	"powershell": {kind: kindOpaque, reason: "PowerShell scripts cannot be parsed"},
	"cmd":        {kind: kindOpaque, reason: "cmd scripts cannot be parsed"},
	"csh":        {kind: kindOpaque, reason: "csh scripts cannot be parsed"},
	"tcsh":       {kind: kindOpaque, reason: "tcsh scripts cannot be parsed"},
	"xonsh":      {kind: kindOpaque, reason: "xonsh scripts cannot be parsed"},
	"elvish":     {kind: kindOpaque, reason: "elvish scripts cannot be parsed"},
	"su":         {kind: kindOpaque, reason: "su runs a shell"},
	"watch":      {kind: kindOpaque, reason: "watch runs its command through a shell"},
	"hyperfine":  {kind: kindOpaque, reason: "hyperfine runs its commands through a shell"},
	"script":     {kind: kindOpaque, reason: "script runs a shell"},
	"nix-shell":  {kind: kindOpaque, reason: "nix-shell runs a shell"},
	"parallel":   {kind: kindOpaque, reason: "parallel runs its command through a shell"},
	"watchexec":  {kind: kindOpaque, reason: "watchexec runs its command through a shell"},
}

// versionedPrograms are looked up without a trailing version, so that
// python3.12 is python.
var versionedPrograms = []string{"python", "pypy", "ruby", "perl", "php", "lua", "node"}

func lookupProgramSpec(name string) (programSpec, bool) {
	base := filepath.Base(name)
	if spec, ok := programSpecs[base]; ok {
		return spec, true
	}
	for _, p := range versionedPrograms {
		version, ok := strings.CutPrefix(base, p)
		if ok && version != "" && strings.Trim(version, "0123456789.") == "" {
			return programSpecs[p], true
		}
	}
	return programSpec{}, false
}

// Shell builtins by what the authorizer makes of them. Builtins in none
// of these sets are looked up on PATH like any other command.
var (
	// shellNoProgram builtins run nothing that needs approval.
	shellNoProgram = setOf("cd", "pushd", "popd", "dirs", "shift", "return",
		"break", "continue", ":", "true", "false", "set", "unset", "shopt", "umask",
		"ulimit", "wait", "times", "jobs", "getopts", "let", "caller", "local",
		"declare", "typeset", "export", "readonly", "exit", "logout", "unalias",
		"disown", "suspend", "fg", "bg", "help", "history")
	// shellBuiltinPrograms are scoped like programs and bound to the
	// shell that runs them.
	shellBuiltinPrograms = setOf("echo", "printf", "test", "[", "pwd", "read",
		"mapfile", "readarray", "kill", "type")
	// shellOpaqueBuiltins run code given as a string, or change what a
	// later command name runs.
	shellOpaqueBuiltins = setOf("eval", "source", ".", "alias", "enable", "hash",
		"trap", "fc", "bind", "complete", "compgen")
	// shellDirBuiltins change the directory relative names resolve in.
	shellDirBuiltins = setOf("cd", "pushd", "popd")
	// shellArithBuiltins may evaluate arithmetic, and so array subscripts
	// holding command substitutions, in their arguments.
	shellArithBuiltins = setOf("test", "[", "unset", "declare", "local", "typeset",
		"readonly", "export", "read", "mapfile", "readarray", "let", "getopts",
		"printf")
	// shellAssigningBuiltins set the variables their operands name.
	shellAssigningBuiltins = setOf("read", "mapfile", "readarray", "getopts",
		"unset", "export", "declare", "local", "typeset", "readonly")
)

// dangerousEnvNames change what a program runs or loads beyond its
// arguments. Setting one makes a command approvable only exactly.
var dangerousEnvNames = setOf("PATH", "BASH_ENV", "ENV", "ZDOTDIR",
	"SHELLOPTS", "BASHOPTS", "PS0", "PS1", "PS2", "PS4", "PROMPT_COMMAND", "FPATH",
	"PERL5OPT", "PERL5LIB", "PERLLIB", "PYTHONSTARTUP", "PYTHONPATH", "PYTHONHOME",
	"NODE_OPTIONS", "NODE_PATH", "RUBYOPT", "RUBYLIB", "GIT_SSH", "GIT_SSH_COMMAND",
	"GIT_EXEC_PATH", "GIT_PAGER", "GIT_EDITOR", "GIT_SEQUENCE_EDITOR", "GIT_ASKPASS",
	"SSH_ASKPASS", "GIT_EXTERNAL_DIFF", "GIT_PROXY_COMMAND", "PAGER", "EDITOR",
	"VISUAL", "GOFLAGS", "GOTOOLCHAIN", "CC", "CXX", "MAKEFLAGS", "LESSOPEN",
	"JAVA_TOOL_OPTIONS", "_JAVA_OPTIONS", "CLASSPATH", "GLIBC_TUNABLES")

var dangerousEnvPrefixes = []string{"LD_", "DYLD_", "BASH_FUNC_", "GIT_CONFIG"}

func dangerousEnv(name string) bool {
	if dangerousEnvNames[name] {
		return true
	}
	for _, prefix := range dangerousEnvPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func setOf(names ...string) map[string]bool {
	ret := make(map[string]bool, len(names))
	for _, n := range names {
		ret[n] = true
	}
	return ret
}

// optionSpec describes the options of a program the way getopt does.
type optionSpec struct {
	// short lists the single-letter options. A letter followed by ':'
	// takes a value, attached or as the next word; one followed by '?'
	// takes an optional value that must be attached.
	short string
	// long lists the long options without their dashes. A trailing '='
	// marks one that takes a value, and "=?" one whose optional value
	// must be attached.
	long []string
	// plus reports that options may start with '+' too (set +e).
	plus bool
	// anyLong accepts every long option, none of which takes a separate
	// value.
	anyLong bool
}

type parsedOption struct {
	// name is "-x" for a single-letter option and "--name" for a long one.
	name  string
	value string
	// at is the index of the word that holds the option.
	at int
}

// scan parses the options that lead args. It returns them with the index
// of the first operand. ok is false when an option is unknown, or a word
// it must read is not literal, so that what follows cannot be told apart.
func (s optionSpec) scan(args []shellWord) (opts []parsedOption, operand int, ok bool) {
	for i := 0; i < len(args); i++ {
		if !args[i].lit {
			return opts, i, false
		}
		arg := args[i].text
		if arg == "--" {
			return opts, i + 1, true
		}
		if len(arg) < 2 || (arg[0] != '-' && (arg[0] != '+' || !s.plus)) {
			return opts, i, true
		}
		if strings.HasPrefix(arg, "--") {
			name, value, attached := strings.Cut(arg[2:], "=")
			takes, found := s.longOption(name)
			switch {
			case !found:
				return opts, i, false
			case takes == "=" && !attached:
				if i+1 >= len(args) || !args[i+1].lit {
					return opts, i, false
				}
				i++
				value = args[i].text
			case takes == "" && attached:
				return opts, i, false
			}
			opts = append(opts, parsedOption{name: "--" + name, value: value, at: i})
			continue
		}
		at := i
		for j := 1; j < len(arg); j++ {
			c := arg[j]
			idx := strings.IndexByte(s.short, c)
			if idx < 0 || c == ':' || c == '?' {
				return opts, i, false
			}
			opt := parsedOption{name: "-" + string(c), at: at}
			var takes byte
			if idx+1 < len(s.short) {
				takes = s.short[idx+1]
			}
			if takes == ':' || takes == '?' {
				opt.value = arg[j+1:]
				if opt.value == "" && takes == ':' {
					if i+1 >= len(args) || !args[i+1].lit {
						return opts, i, false
					}
					i++
					opt.value = args[i].text
				}
				opts = append(opts, opt)
				break
			}
			opts = append(opts, opt)
		}
	}
	return opts, len(args), true
}

// longOption reports whether name is a long option and what it takes:
// "" nothing, "=" a value and "=?" an optional attached value.
func (s optionSpec) longOption(name string) (string, bool) {
	for _, l := range s.long {
		n, takes, _ := strings.Cut(l, "=")
		if n != name {
			continue
		}
		if strings.HasSuffix(l, "=?") {
			return "=?", true
		}
		if strings.HasSuffix(l, "=") {
			return "=", true
		}
		return takes, true
	}
	if s.anyLong {
		return "=?", true
	}
	return "", false
}

func hasOption(opts []parsedOption, names []string) (parsedOption, bool) {
	for _, o := range opts {
		if slices.Contains(names, o.name) {
			return o, true
		}
	}
	return parsedOption{}, false
}

// perfCommandStart finds the command of perf record, stat and trace,
// which is only told apart from their options by a "--".
func perfCommandStart(args []shellWord) (int, bool) {
	if len(args) == 0 || !args[0].lit {
		return 0, false
	}
	switch args[0].text {
	case "record", "stat", "trace":
	default:
		return 0, false
	}
	for i, w := range args {
		if !w.lit {
			return 0, false
		}
		if w.text == "--" {
			return i + 1, true
		}
	}
	return 0, false
}

// findCommands returns the commands find runs for -exec, -execdir, -ok
// and -okdir.
func findCommands(args []shellWord) ([][]shellWord, int, bool) {
	var cmds [][]shellWord
	code := -1
	for i := 0; i < len(args); i++ {
		if !args[i].lit {
			return nil, -1, false
		}
		switch args[i].text {
		case "-exec", "-execdir", "-ok", "-okdir":
		default:
			continue
		}
		end := i + 1
		for ; end < len(args); end++ {
			if !args[end].lit {
				return nil, -1, false
			}
			t := args[end].text
			if t == ";" || (t == "+" && end > i+1 && args[end-1].text == "{}") {
				break
			}
		}
		if end == len(args) || end == i+1 {
			return nil, -1, false
		}
		if code < 0 {
			code = i
		}
		cmds = append(cmds, args[i+1:end])
		i = end
	}
	return cmds, code, true
}

// fdCommands returns the command fd runs for -x or -X, which takes every
// argument up to a ";" or the end.
func fdCommands(args []shellWord) ([][]shellWord, int, bool) {
	for i, w := range args {
		if !w.lit {
			return nil, -1, false
		}
		switch t := w.text; {
		case t == "-x" || t == "--exec" || t == "-X" || t == "--exec-batch":
			end := i + 1
			for end < len(args) && (!args[end].lit || args[end].text != ";") {
				end++
			}
			if end == i+1 {
				return nil, -1, false
			}
			return [][]shellWord{args[i+1 : end]}, i, true
		case strings.HasPrefix(t, "--exec"),
			!strings.HasPrefix(t, "--") && strings.HasPrefix(t, "-") &&
				strings.ContainsAny(t[1:], "xX"):
			return nil, -1, false
		}
	}
	return nil, -1, true
}

// optionCommands returns the embedded function of a utility whose command
// follows its options and skip operands, such as entr and flock.
func optionCommands(
	spec optionSpec, opaque []string, skip int,
) func([]shellWord) ([][]shellWord, int, bool) {
	return func(args []shellWord) ([][]shellWord, int, bool) {
		opts, operand, ok := spec.scan(args)
		if !ok {
			return nil, -1, false
		}
		if _, found := hasOption(opts, opaque); found {
			return nil, -1, false
		}
		start := operand + skip
		if start >= len(args) {
			return nil, -1, true
		}
		// flock takes -c after its file too.
		if args[start].lit && slices.Contains(opaque, args[start].text) {
			return nil, -1, false
		}
		return [][]shellWord{args[start:]}, start, true
	}
}

// gitGlobalSkipped are git's global options that pick the repository or
// change output only, and so are left out of the scope.
var gitGlobalSkipped = map[string]bool{
	"-C": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"-P": true, "--no-pager": true, "-p": true, "--paginate": true, "--bare": true,
	"--no-replace-objects": true, "--literal-pathspecs": true,
	"--glob-pathspecs": true, "--noglob-pathspecs": true, "--icase-pathspecs": true,
	"--no-optional-locks": true, "--no-lazy-fetch": true, "--no-advice": true,
}

// gitGlobalValued are git's global options that take the next word as
// their value unless it is attached with '='.
var gitGlobalValued = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--config-env": true,
}

// gitScope scopes git by its subcommand: the global options that pick
// the repository are left out, and those that set configuration carry
// code.
func gitScope(args []string) ([]string, int) {
	scope := make([]string, 0, len(args))
	code := -1
	i := 0
	for ; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") || arg == "--" {
			break
		}
		name, _, attached := strings.Cut(arg, "=")
		words := args[i : i+1]
		if gitGlobalValued[name] && !attached && i+1 < len(args) {
			words = args[i : i+2]
			i++
		}
		if gitGlobalSkipped[name] {
			continue
		}
		switch name {
		case "-c", "--config-env", "--exec-path":
			if code < 0 {
				code = len(scope)
			}
		}
		scope = append(scope, words...)
	}
	return append(scope, args[i:]...), code
}
