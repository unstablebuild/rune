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
	"io"
	"io/fs"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
)

// The helpers below give the external tests, which need the workspace
// backends of vshtest, access to the internals they unit-test.
var (
	EnvValue         = envValue
	AbsPath          = absPath
	IsBuiltinProgram = isBuiltinProgram
	Permits          = permits
	PathError        = pathError
	CallHandler      = callHandler
	CheckParent      = checkParent
	Shebang          = shebang
	ExportedPairs    = exportedPairs
	ParseScript      = parseScript
	CallStmt         = callStmt
	CommonPrefix     = commonPrefix
	Insertion        = insertion
	FormatList       = formatList
	DisplayName      = displayName
	IsAssignment     = isAssignment
)

const HiddenPrefix = hiddenPrefix

func (e *Executor) BinNames() []string             { return e.binNames() }
func (e *Executor) StubContent(name string) string { return e.stubContent(name) }
func (e *Executor) PathList(value string) []string { return e.pathList(value) }
func (e *Executor) ExpandHome(p string) string     { return e.expandHome(p) }
func (e *Executor) BaseEnv(extra []string, dir string) []string {
	return e.baseEnv(extra, dir)
}

func (e *Executor) SearchPath(pathList []string, name, dir string) (string, bool) {
	return e.searchPath(pathList, name, dir)
}

func (e *Executor) InstallBin() error { return e.installBin() }

// NewCompleter is the shell's Completer in dir, with the PATH value
// path and the functions funcs defined.
func (e *Executor) NewCompleter(dir, path string, funcs ...string) Completer {
	return &completer{e: e, dir: dir, path: path, funcs: funcs}
}

// CompleteLine is what Tab does at byte pos of line with a Completer
// from NewCompleter.
func CompleteLine(c Completer, line string, pos int) (string, int, []string) {
	return c.(*completer).completeLine(context.Background(), line, pos)
}

// LineContext is a lineContext with its fields exported.
type LineContext struct {
	Words    []string
	Partial  string
	Start    int
	Quote    byte
	Redirect bool
	Comment  bool
}

func LexLine(head string) LineContext {
	lc := lexLine(head)
	return LineContext{Words: lc.words, Partial: lc.partial, Start: lc.start, Quote: lc.quote,
		Redirect: lc.redirect, Comment: lc.comment}
}

// Stat is what the stat handler answers for the absolute path abs.
func (e *Executor) Stat(abs string, followSymlinks bool) (fs.FileInfo, error) {
	info, err := e.stat(abs, followSymlinks)
	if err != nil {
		return nil, err
	}
	return statInfo{FileInfo: info}, nil
}

// Access is what the access handler answers for the absolute path abs.
func (e *Executor) Access(abs string, mode interp.AccessMode) error {
	return e.accessHandler()(context.Background(), abs, mode)
}

// StatInfo wraps info as the stat handler does.
func StatInfo(info fs.FileInfo) fs.FileInfo { return statInfo{FileInfo: info} }

// DeviceInfo describes the device named name.
func DeviceInfo(name string) fs.FileInfo { return deviceInfo(name) }

// Program is a resolved program with its fields exported.
type Program struct {
	Kind             string
	Path, Name, Arg0 string
	Args             []string
	Shell            ShellArgs
	Script           string
}

// ShellArgs are parsed shell arguments with their fields exported.
type ShellArgs struct {
	Command    string
	HasCommand bool
	Name       string
	File       string
	Params     []string
	Opts       []string
}

var kindNames = map[programKind]string{
	kindShell:         "shell",
	kindCommand:       "command",
	kindVshBuiltin:    "vsh builtin",
	kindInterpBuiltin: "interp builtin",
	kindScript:        "script",
}

func exportShellArgs(sa shellArgs) ShellArgs {
	return ShellArgs{Command: sa.command, HasCommand: sa.hasCommand, Name: sa.name, File: sa.file,
		Params: sa.params, Opts: sa.opts}
}

func ParseShellArgs(args []string) ShellArgs { return exportShellArgs(parseShellArgs(args)) }

func (e *Executor) Resolve(name string, args []string, pathEnv, dir string) (Program, error) {
	prog, err := e.resolve(name, args, pathEnv, dir)
	if err != nil {
		return Program{}, err
	}
	return Program{
		Kind: kindNames[prog.kind], Path: prog.path, Name: prog.name, Arg0: prog.arg0, Args: prog.args,
		Shell: exportShellArgs(prog.shell), Script: string(prog.script),
	}, nil
}

// RunProgram resolves name in the bin directory and runs it in dir as
// a program nested depth shells deep.
func (e *Executor) RunProgram(ctx context.Context, depth int, name string, args []string, dir string,
	stdin io.Reader, stdout, stderr io.Writer,
) error {
	prog, err := e.resolve(name, args, "", dir)
	if err != nil {
		return err
	}
	return e.runProgram(ctx, runnerConfig{
		dir: dir, env: e.baseEnv(nil, dir), stdin: stdin, stdout: stdout, stderr: stderr, depth: depth,
	}, prog)
}

// NewInvocation is how the shell describes running name in dir on the
// given streams, outside any process.
func (e *Executor) NewInvocation(name string, args []string, dir string, env []string,
	stdin io.Reader, stdout, stderr io.Writer,
) *Invocation {
	return e.invocation(runnerConfig{dir: dir, stdin: stdin, stdout: stdout, stderr: stderr},
		name, args, expand.ListEnviron(env...))
}

// NewProcFS is fsys as a process outside a terminal, with the given
// streams, sees it.
func NewProcFS(fsys schemeapi.FileSystem, stdin io.Reader, stdout, stderr io.Writer) schemeapi.FileSystem {
	return procFS{FileSystem: fsys, dev: devices{stdin: stdin, stdout: stdout, stderr: stderr}}
}

// OpenFile opens abs, named p, as a process outside a terminal, with
// the given streams, opens it.
func OpenFile(fsys schemeapi.FileSystem, abs, p string, flag int, perm fs.FileMode,
	stdin io.Reader, stdout, stderr io.Writer,
) (io.ReadWriteCloser, error) {
	return devices{stdin: stdin, stdout: stdout, stderr: stderr}.open(fsys, abs, p, flag, perm)
}

// CommandStatus maps the error of a command named name, which writes
// its errors to stderr, to an interpreter status.
func CommandStatus(name string, err error, stderr io.Writer) error {
	return commandStatus(&Invocation{Name: name, Stderr: stderr}, err)
}

// NewFileReader reads f from its start at its own offset.
func NewFileReader(f interface {
	io.ReaderAt
	io.Closer
}) io.ReadWriteCloser {
	return &fileReader{f: f}
}
