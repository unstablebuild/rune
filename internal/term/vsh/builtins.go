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

package vsh

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// runVshBuiltin runs one of the shell's own commands: help, which,
// and the type and command -v lookups the interpreter would otherwise
// answer from the host.
func (e *Executor) runVshBuiltin(ctx context.Context, inv *Invocation) error {
	var err error
	switch inv.Name {
	case "help":
		err = e.help(inv)
	case "which":
		err = e.which(inv)
	case "type":
		err = e.typeBuiltin(inv)
	case "command":
		err = e.commandBuiltin(inv)
	default:
		fmt.Fprintf(inv.Stderr, "%s: command not found\n", inv.Name)
		return interp.ExitStatus(127)
	}
	return commandStatus(inv, err)
}

// match is one thing a command name runs as: a keyword, a builtin, or
// the file at path.
type match struct {
	kind, path string
}

// lookup lists what name runs as, in the order the shell tries them:
// a keyword or interpreter builtin, then the executable files of
// pathList, whose relative entries are resolved against dir. A name
// with a slash is the executable file it names, reported by that
// name, as bash reports it. all keeps every match rather than the
// first; files skips keywords and builtins.
func (e *Executor) lookup(name string, pathList []string, dir string, all, files bool) []match {
	if strings.Contains(name, "/") {
		if e.isExecutable(absPath(dir, e.expandHome(name))) {
			return []match{{kind: "file", path: name}}
		}
		return nil
	}
	var found []match
	switch {
	case files:
	case syntax.IsKeyword(name):
		found = append(found, match{kind: "keyword"})
	case interp.IsBuiltin(name):
		found = append(found, match{kind: "builtin"})
	}
	for p := range e.pathMatches(pathList, name, dir) {
		if len(found) > 0 && !all {
			break
		}
		found = append(found, match{kind: "file", path: p})
	}
	return found
}

// invPath is the PATH of an invocation, split.
func (e *Executor) invPath(inv *Invocation) []string {
	return e.pathList(inv.Env.Get("PATH").String())
}

// parseOptions splits args into the option letters before the first
// operand or "--", and the operands. It reports the first letter not
// in allowed as bad.
func parseOptions(args []string, allowed string) (letters string, operands []string, bad rune) {
	for i, a := range args {
		if a == "--" {
			return letters, args[i+1:], 0
		}
		if len(a) < 2 || a[0] != '-' {
			return letters, args[i:], 0
		}
		for _, c := range a[1:] {
			if !strings.ContainsRune(allowed, c) {
				return letters, nil, c
			}
		}
		letters += a[1:]
	}
	return letters, nil, 0
}

// usageError reports an invalid option as bash does, with status 2.
func usageError(inv *Invocation, bad rune, usage string) error {
	fmt.Fprintf(inv.Stderr, "%s: -%c: invalid option\n%s: usage: %s\n", inv.Name, bad, inv.Name, usage)
	return &ExitError{Code: 2}
}

func (e *Executor) which(inv *Invocation) error {
	status := 0
	all := false
	var names []string
	for _, a := range inv.Args {
		if strings.HasPrefix(a, "-") {
			// GNU which takes many long options about aliases and
			// functions; only -a changes what a lookup answers.
			all = all || (!strings.HasPrefix(a, "--") && strings.Contains(a, "a"))
			continue
		}
		names = append(names, a)
	}
	for _, name := range names {
		found := e.lookup(name, e.invPath(inv), inv.Dir, all, true)
		if len(found) == 0 {
			fmt.Fprintf(inv.Stderr, "which: no %s in (%s)\n", name, inv.Env.Get("PATH").String())
			status = 1
			continue
		}
		for _, m := range found {
			if _, err := fmt.Fprintln(inv.Stdout, m.path); err != nil {
				return err
			}
		}
	}
	if status != 0 {
		return &ExitError{Code: status}
	}
	return nil
}

func (e *Executor) typeBuiltin(inv *Invocation) error {
	letters, names, bad := parseOptions(inv.Args, "afptP")
	if bad != 0 {
		return usageError(inv, bad, "type [-afptP] name [name ...]")
	}
	// As in bash, the last of -t, -p and -P picks the output, and -P
	// skips the keywords and builtins whichever comes last.
	var mode rune
	all, files := false, false
	for _, c := range letters {
		switch c {
		case 'a':
			all = true
		case 'p', 't':
			mode = c
		case 'P':
			mode, files = 'p', true
		}
	}
	status := 0
	for _, name := range names {
		found := e.lookup(name, e.invPath(inv), inv.Dir, all, files)
		if len(found) == 0 {
			if mode == 0 {
				fmt.Fprintf(inv.Stderr, "type: %s: not found\n", name)
			}
			status = 1
			continue
		}
		for _, m := range found {
			var line string
			switch {
			case mode == 't':
				line = m.kind
			case mode == 'p' && m.kind != "file":
				continue
			case mode == 'p':
				line = m.path
			case m.kind == "file":
				line = name + " is " + m.path
			default:
				line = name + " is a shell " + m.kind
			}
			if _, err := fmt.Fprintln(inv.Stdout, line); err != nil {
				return err
			}
		}
	}
	if status != 0 {
		return &ExitError{Code: status}
	}
	return nil
}

// commandBuiltin is command -v and -V. Unlike type, it succeeds when
// any name is found, as bash's does.
func (e *Executor) commandBuiltin(inv *Invocation) error {
	letters, names, bad := parseOptions(inv.Args, "pvV")
	if bad != 0 {
		return usageError(inv, bad, "command [-pVv] command [arg ...]")
	}
	verbose := false
	pathList := e.invPath(inv)
	for _, c := range letters {
		switch c {
		case 'p':
			pathList = e.pathList("")
		case 'v', 'V':
			verbose = c == 'V'
		}
	}
	found := false
	for _, name := range names {
		ms := e.lookup(name, pathList, inv.Dir, false, false)
		if len(ms) == 0 {
			if verbose {
				fmt.Fprintf(inv.Stderr, "command: %s: not found\n", name)
			}
			continue
		}
		found = true
		m := ms[0]
		var line string
		switch {
		case verbose && m.kind == "file":
			line = name + " is " + m.path
		case verbose:
			line = name + " is a shell " + m.kind
		case m.kind == "file":
			line = m.path
		default:
			line = name
		}
		if _, err := fmt.Fprintln(inv.Stdout, line); err != nil {
			return err
		}
	}
	if !found && len(names) > 0 {
		return &ExitError{Code: 1}
	}
	return nil
}

func (e *Executor) help(inv *Invocation) error {
	var b strings.Builder
	b.WriteString("Rune built-in shell\n\n")
	b.WriteString("Commands run in-process against this workspace's files; no host\n")
	b.WriteString("program ever runs. The shell is a Bash dialect: variables,\n")
	b.WriteString("functions, pipes, redirections, globs, loops and the usual builtins\n")
	b.WriteString("(cd, echo, printf, read, test, export, ...) work as in Bash.\n\n")
	b.WriteString("Commands:\n")
	names := make([]string, 0, len(e.commands))
	for name := range e.commands {
		names = append(names, name)
	}
	slices.Sort(names)
	width := len("which")
	for _, name := range names {
		width = max(width, len(name))
	}
	for _, name := range names {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, name, e.commands[name].Synopsis)
	}
	fmt.Fprintf(&b, "  %-*s  %s\n", width, "which", "which NAME...")
	fmt.Fprintf(&b, "  %-*s  %s\n", width, "help", "help")
	b.WriteString("\nNot available: full-screen programs, job control (&, fg, bg),\n")
	b.WriteString("process substitution, persistent history.\n")
	b.WriteString("Wide characters on the prompt line may misplace the cursor.\n")
	_, err := inv.Stdout.Write([]byte(b.String()))
	return err
}
