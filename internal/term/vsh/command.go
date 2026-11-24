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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/term"
	"mvdan.cc/sh/v3/expand"
)

// Command is a program the shell runs in-process. Commands are
// installed as executable files in the bin directory so that PATH
// lookups, which, type and the authorizer see them as any other
// program.
type Command struct {
	// Name is the program name, as typed and as installed.
	Name string
	// Synopsis is the one-line usage shown by help.
	Synopsis string
	// Run executes one invocation. A nil error is exit status 0; an
	// ExitError sets the status; any other error is printed to
	// Stderr as "<name>: <error>" with status 1. Run must stop when
	// ctx ends and on the first write error.
	Run func(ctx context.Context, inv *Invocation) error
	// Completer completes the command's arguments at the prompt; nil,
	// or one that fails or yields nothing, completes paths.
	Completer Completer
}

// replWidth is the width a REPL command's output is drawn at off a
// terminal.
const replWidth = 80

// REPLCommand is the command name of the console's h as a Command, so
// that the shell runs and completes it as the console does. Its
// output components are drawn as text at the width of the terminal it
// writes to, or 80 columns, each on lines of its own. A repl.ExitError
// sets the status, and repl.ErrNotFound is status 127.
func REPLCommand(name, synopsis string, h repl.CommandHandler) Command {
	return Command{
		Name:      name,
		Synopsis:  synopsis,
		Completer: h,
		Run: func(ctx context.Context, inv *Invocation) error {
			it, err := h.HandleCommand(ctx, repl.Command{Name: name, Args: inv.Args}, repl.NopProgressWriter())
			if errors.Is(err, repl.ErrNotFound) {
				fmt.Fprintf(inv.Stderr, "%s: command not found\n", inv.Name)
				return &ExitError{Code: 127}
			}
			if err != nil {
				return err
			}
			width := replWidth
			if inv.Term != nil {
				if cols, _ := inv.Term.Size(); cols > 0 {
					width = cols
				}
			}
			defer func() { _ = it.Close() }()
			for {
				item, ok := it.Next(ctx)
				if !ok {
					break
				}
				if text := drawText(item, width); text != "" {
					if _, err := io.WriteString(inv.Stdout, text+"\n"); err != nil {
						return err
					}
				}
			}
			if err := it.Err(); err != nil {
				if exit, ok := errors.AsType[*repl.ExitError](err); ok {
					return &ExitError{Code: exit.Code}
				}
				return err
			}
			return nil
		},
	}
}

// drawText draws c width cells wide as text, without the blanks that
// end its lines and rows.
func drawText(c component.Responsive, width int) string {
	height := c.Height(width)
	if height <= 0 {
		return ""
	}
	w := term.NewStringWriter(width, height)
	c.Resize(width, height)
	c.Draw(w)
	_ = w.Flush()
	lines := strings.Split(w.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// Invocation is one run of a Command.
type Invocation struct {
	// Name is the program name it was invoked as and Args are its
	// arguments, options included.
	Name string
	Args []string
	// Dir is the absolute working directory.
	Dir string
	// FS is the filesystem the command operates on, as its process
	// sees it when the shell runs it: the devices under /dev are the
	// invocation's streams and terminal, and no file is created
	// without a parent directory. Relative paths in Args are relative
	// to Dir; use Abs to resolve them.
	FS schemeapi.FileSystem
	// Env is the environment, exported variables and shell variables
	// alike.
	Env expand.Environ

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Term is non-nil iff Stdout is the process's own terminal, so a
	// command can lay its output out in columns.
	Term *Term

	// proc is the process running the command, nil outside one.
	proc *process
}

// Abs resolves p against the invocation's working directory.
func (inv *Invocation) Abs(p string) string {
	return absPath(inv.Dir, p)
}

// Open opens p, relative to Dir, as the process running the command
// would, with os.OpenFile's flag and perm. The devices under /dev are
// the invocation's streams and terminal on any filesystem; other
// paths are files of FS, each open one reading and writing at its own
// offset. A directory cannot be opened, and a new file needs an
// existing parent directory. Errors are *os.PathError values naming p.
func (inv *Invocation) Open(p string, flag int, perm fs.FileMode) (io.ReadWriteCloser, error) {
	d := devices{proc: inv.proc, stdin: inv.Stdin, stdout: inv.Stdout, stderr: inv.Stderr}
	return d.open(inv.FS, inv.Abs(p), p, flag, perm)
}

// Term is the terminal a command's standard output is attached to.
type Term struct {
	p *pty
}

// Size returns the terminal's dimensions in cells.
func (t *Term) Size() (cols, rows int) {
	t.p.mu.Lock()
	defer t.p.mu.Unlock()
	return t.p.size.Columns, t.p.size.Rows
}

// absPath resolves p against dir, cleaning the result.
func absPath(dir, p string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(dir, p)
}
