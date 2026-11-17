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
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
)

type programKind int

const (
	// kindShell runs the shell itself: interactively, with -c, or
	// over a script operand.
	kindShell programKind = iota
	// kindCommand is a registered Command.
	kindCommand
	// kindVshBuiltin is a command the shell implements itself.
	kindVshBuiltin
	// kindInterpBuiltin is an interpreter builtin run as a program.
	kindInterpBuiltin
	// kindScript is a shell script file.
	kindScript
)

// program is what a command name resolved to.
type program struct {
	kind programKind
	// path is the file the program runs: a program stub or the script.
	path string
	// name is the base name the program was invoked as.
	name string
	// arg0 is the script's $0: the path it was invoked by, or the
	// file the PATH search found for a bare name.
	arg0 string
	args []string
	// shell holds the parsed shell arguments for kindShell.
	shell shellArgs
	// script holds the file contents for kindScript.
	script []byte
}

// shellArgs are the arguments of a shell invocation.
type shellArgs struct {
	// command is the -c operand.
	command    string
	hasCommand bool
	// name is the $0 of the -c command, the operand after it.
	name string
	// file is the script operand when there is no -c.
	file string
	// params are the positional parameters that follow the command's
	// name or the file.
	params []string
	// opts are the -e -u -x -o options, for interp.Params.
	opts []string
}

// parseShellArgs interprets a shell's command line the way sh does:
// -c takes the next argument as the script, the first operand is a
// script file, the set options pass through, and the flags about
// login and startup files are accepted and ignored.
func parseShellArgs(args []string) shellArgs {
	var sa shellArgs
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--" || a == "-":
			if i+1 < len(args) {
				sa.file = args[i+1]
				sa.params = args[i+2:]
			}
			return sa
		case a == "-o" || a == "+o":
			if i+1 < len(args) {
				sa.opts = append(sa.opts, a, args[i+1])
				i++
			}
		case strings.HasPrefix(a, "--"):
			// --login, --norc, --noprofile, --posix and friends.
		case len(a) > 1 && (a[0] == '-' || a[0] == '+'):
			for _, c := range a[1:] {
				switch c {
				case 'c':
					sa.hasCommand = true
				case 'e', 'u', 'x', 'f', 'n', 'a':
					sa.opts = append(sa.opts, string(a[0])+string(c))
				}
			}
			if sa.hasCommand {
				if i+1 < len(args) {
					sa.command = args[i+1]
					if rest := args[i+2:]; len(rest) > 0 {
						sa.name, sa.params = rest[0], rest[1:]
					}
				}
				return sa
			}
		default:
			sa.file = a
			sa.params = args[i+1:]
			return sa
		}
	}
	return sa
}

// badInterpreterError reports a script whose "#!" line names a
// program the shell cannot run.
type badInterpreterError struct {
	path, interpreter string
}

func (e *badInterpreterError) Error() string {
	return fmt.Sprintf("%s: %s: bad interpreter: not a shell", e.path, e.interpreter)
}

// Unwrap classifies the error as a kernel would a file it cannot
// execute.
func (e *badInterpreterError) Unwrap() error { return syscall.ENOEXEC }

// notFoundError reports a bare command name absent from the PATH.
type notFoundError struct {
	name string
}

func (e *notFoundError) Error() string {
	return fmt.Sprintf("exec: %q: executable file not found in $PATH", e.name)
}

func (e *notFoundError) Is(target error) bool { return target == os.ErrNotExist }

// resolve finds the program a command name runs. A bare name is
// searched in pathEnv; a name with a slash is resolved against dir.
// Programs in the bin directory run the built-in of their base name;
// any other file runs as a script.
func (e *Executor) resolve(name string, args []string, pathEnv, dir string) (*program, error) {
	if name == "" {
		return &program{kind: kindShell, path: e.shellPath(), name: "sh", args: args,
			shell: parseShellArgs(args)}, nil
	}
	var p string
	arg0 := name
	if !strings.Contains(name, "/") {
		found, ok := e.searchPath(e.pathList(pathEnv), name, dir)
		if !ok {
			return nil, &notFoundError{name: name}
		}
		p, arg0 = found, found
	} else {
		p = absPath(dir, e.expandHome(name))
		info, err := e.cfg.FS.Stat(p)
		if err != nil {
			return nil, &os.PathError{Op: "fork/exec", Path: name, Err: syscall.ENOENT}
		}
		if info.IsDir() {
			return nil, &os.PathError{Op: "fork/exec", Path: name, Err: syscall.EACCES}
		}
		if !info.Mode().IsRegular() {
			return nil, &os.PathError{Op: "fork/exec", Path: name, Err: syscall.ENOEXEC}
		}
	}
	base := path.Base(p)
	prog := &program{path: p, name: base, arg0: arg0, args: args}
	if path.Dir(p) == e.cfg.Bin {
		switch {
		case isShellName(base):
			prog.kind = kindShell
			prog.shell = parseShellArgs(args)
			return prog, nil
		case e.commands[base].Run != nil:
			prog.kind = kindCommand
			return prog, nil
		case slices.Contains(vshBuiltinNames, base):
			prog.kind = kindVshBuiltin
			return prog, nil
		case isBuiltinProgram(base):
			prog.kind = kindInterpBuiltin
			return prog, nil
		}
	}
	src, err := e.readFile(p)
	if err != nil {
		return nil, &os.PathError{Op: "fork/exec", Path: name, Err: err}
	}
	if interpreter, ok := shebang(src); ok && !isShellName(path.Base(interpreter)) {
		return nil, &badInterpreterError{path: name, interpreter: interpreter}
	}
	prog.kind = kindScript
	prog.script = src
	return prog, nil
}

// shebang returns the interpreter a "#!" line names, looking through
// "env".
func shebang(src []byte) (string, bool) {
	if !bytes.HasPrefix(src, []byte("#!")) {
		return "", false
	}
	line, _, _ := bytes.Cut(src[2:], []byte("\n"))
	fields := strings.Fields(string(line))
	if len(fields) == 0 {
		return "", false
	}
	interpreter := fields[0]
	if path.Base(interpreter) == "env" {
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "-") && !strings.Contains(f, "=") {
				return f, true
			}
		}
		return interpreter, true
	}
	return interpreter, true
}

// readFile returns the contents of the file at absolute path p.
func (e *Executor) readFile(p string) ([]byte, error) {
	f, err := e.cfg.FS.OpenFile(p, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(&fileReader{f: f})
}

// fileReader reads a workspace file through ReadAt with its own
// offset, since handles of an in-memory file share one.
type fileReader struct {
	f interface {
		io.ReaderAt
		io.Closer
	}
	off int64
}

func (r *fileReader) Read(p []byte) (int, error) {
	n, err := r.f.ReadAt(p, r.off)
	r.off += int64(n)
	if n > 0 && errors.Is(err, io.EOF) {
		// Report the short read now and the EOF on the next call.
		return n, nil
	}
	return n, err
}

func (r *fileReader) Write([]byte) (int, error) { return 0, syscall.EBADF }
func (r *fileReader) Close() error              { return r.f.Close() }
