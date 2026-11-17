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
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// maxShellDepth bounds nested shells, as a kernel bounds the process
// tree a fork bomb grows.
const maxShellDepth = 16

// hiddenPrefix marks a command the call handler routed to the shell's
// own implementation. No typed input can produce it: both the line
// discipline and the prompt editor drop the byte.
const hiddenPrefix = "\x00"

// runnerConfig describes one interpreter.
type runnerConfig struct {
	proc        *process
	dir         string
	env         []string
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	depth       int
	interactive bool
	params      []string
	opts        []string
}

// baseEnv builds a process environment: the defaults, then the
// Executor's configured environment, then the command's own, with the
// variables the shell owns last so that they win.
func (e *Executor) baseEnv(extra []string, dir string) []string {
	env := make([]string, 0, 8+len(e.cfg.Env)+len(extra))
	env = append(env, "PATH="+e.cfg.Bin, "TERM="+defaultTERM)
	env = append(env, e.cfg.Env...)
	env = append(env, extra...)
	env = append(env,
		"HOME="+e.cfg.Root,
		"PWD="+dir,
		"OLDPWD="+dir,
		"SHELL="+e.shellPath(),
	)
	return env
}

// exportedPairs lists the exported variables of env as NAME=VALUE.
func exportedPairs(env expand.Environ) []string {
	var pairs []string
	env.Each(func(name string, vr expand.Variable) bool {
		if vr.Exported && vr.Kind == expand.String {
			pairs = append(pairs, name+"="+vr.Str)
		}
		return true
	})
	return pairs
}

func (e *Executor) newRunner(cfg runnerConfig) (*interp.Runner, error) {
	opts := []interp.RunnerOption{
		interp.Env(expand.ListEnviron(cfg.env...)),
		// interp.Dir stats the directory on the host; the directory
		// was validated against the workspace filesystem already.
		func(r *interp.Runner) error {
			r.Dir = cfg.dir
			return nil
		},
		interp.Interactive(cfg.interactive),
		interp.StdIO(cfg.stdin, cfg.stdout, cfg.stderr),
		interp.OpenHandler(e.openHandler(cfg.proc)),
		interp.ReadDirHandler2(e.readDirHandler()),
		interp.StatHandler(e.statHandler()),
		interp.AccessHandler(e.accessHandler()),
		interp.CallHandler(callHandler),
		interp.ExecHandlers(e.execMiddleware(cfg.proc, cfg.depth)),
	}
	if len(cfg.opts) > 0 || len(cfg.params) > 0 {
		params := slices.Clone(cfg.opts)
		if len(cfg.params) > 0 {
			params = append(params, "--")
			params = append(params, cfg.params...)
		}
		opts = append(opts, interp.Params(params...))
	}
	return interp.New(opts...)
}

// pathError wraps a filesystem error for the interpreter, which prints
// *os.PathError values and sets status 1 for them. The scheme's
// sentinel errors are mapped to errnos so messages read as a shell's.
func pathError(op, p string, err error) *os.PathError {
	if pe, ok := errors.AsType[*os.PathError](err); ok {
		err = pe.Err
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		err = syscall.ENOENT
	case errors.Is(err, fs.ErrExist):
		err = syscall.EEXIST
	case errors.Is(err, fs.ErrPermission):
		err = syscall.EACCES
	case errors.Is(err, fs.ErrClosed):
		err = syscall.EBADF
	}
	return &os.PathError{Op: op, Path: p, Err: err}
}

func (e *Executor) openHandler(proc *process) interp.OpenHandlerFunc {
	return func(ctx context.Context, p string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
		hc := interp.HandlerCtx(ctx)
		d := devices{proc: proc, stdin: hc.Stdin, stdout: hc.Stdout, stderr: hc.Stderr}
		return d.open(e.cfg.FS, absPath(hc.Dir, p), p, flag, perm)
	}
}

// devices are what a process finds under /dev: its standard streams
// and its controlling terminal, whatever the filesystem holds there.
type devices struct {
	proc           *process
	stdin          io.Reader
	stdout, stderr io.Writer
}

// open opens the file at absolute path abs, which the caller named p,
// as the process would, each file reading and writing at its own
// offset. Errors are *os.PathError values naming p.
func (d devices) open(fsys schemeapi.FileSystem, abs, p string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
	if dev, ok, err := d.device(abs, p); ok {
		return dev, err
	}
	if info, err := fsys.Stat(abs); err == nil && info.IsDir() {
		return nil, pathError("open", p, syscall.EISDIR)
	}
	writing := flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0
	if !writing {
		flag = os.O_RDONLY
	}
	f, err := (procFS{FileSystem: fsys, dev: d}).OpenFile(abs, flag, perm)
	if err != nil {
		return nil, pathError("open", p, err)
	}
	if writing {
		return &fileWriter{f: f, append: flag&os.O_APPEND != 0, read: flag&os.O_RDWR != 0}, nil
	}
	return &fileReader{f: f}, nil
}

// device opens the device at absolute path abs, which the caller
// named p, and reports whether abs names one.
func (d devices) device(abs, p string) (io.ReadWriteCloser, bool, error) {
	switch abs {
	case "/dev/null":
		return devNull{}, true, nil
	case "/dev/stdin":
		return &streamFile{r: d.stdin}, true, nil
	case "/dev/stdout":
		return &streamFile{w: d.stdout}, true, nil
	case "/dev/stderr":
		return &streamFile{w: d.stderr}, true, nil
	case "/dev/tty":
		if d.proc == nil || d.proc.pty == nil {
			return nil, true, pathError("open", p, syscall.ENXIO)
		}
		return &ttyFile{proc: d.proc}, true, nil
	}
	return nil, false, nil
}

func isDevice(abs string) bool {
	switch abs {
	case "/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return true
	}
	return false
}

// procFS is the filesystem as a process sees it: the scheme's files,
// with the process's devices under /dev whatever the scheme holds
// there, and no file created without a parent directory, which some
// schemes would create on their own.
type procFS struct {
	schemeapi.FileSystem
	dev devices
}

func (p procFS) Create(name string) (workspaceapi.File, error) {
	return p.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
}

func (p procFS) Open(name string) (workspaceapi.File, error) {
	return p.OpenFile(name, os.O_RDONLY, 0)
}

func (p procFS) OpenFile(name string, flag int, perm fs.FileMode) (workspaceapi.File, error) {
	abs := path.Clean(name)
	if dev, ok, err := p.dev.device(abs, name); ok {
		if err != nil {
			return nil, err
		}
		return &devFile{ReadWriteCloser: dev, name: abs}, nil
	}
	if flag&os.O_CREATE != 0 {
		if _, err := p.FileSystem.Stat(abs); err != nil {
			if err := checkParent(p.FileSystem, abs); err != nil {
				return nil, pathError("open", name, err)
			}
		}
	}
	return p.FileSystem.OpenFile(name, flag, perm)
}

func (p procFS) Stat(name string) (fs.FileInfo, error) {
	if abs := path.Clean(name); isDevice(abs) {
		return deviceInfo(path.Base(abs)), nil
	}
	return p.FileSystem.Stat(name)
}

func (p procFS) Lstat(name string) (fs.FileInfo, error) {
	if abs := path.Clean(name); isDevice(abs) {
		return deviceInfo(path.Base(abs)), nil
	}
	return p.FileSystem.Lstat(name)
}

// devFile is a device opened as a file. A device has no offset, so
// ReadAt reads on from where the last read ended and Seek does
// nothing.
type devFile struct {
	io.ReadWriteCloser
	name string
}

func (f *devFile) Name() string                          { return f.name }
func (f *devFile) Stat() (fs.FileInfo, error)            { return deviceInfo(path.Base(f.name)), nil }
func (f *devFile) Sync() error                           { return nil }
func (f *devFile) Truncate(int64) error                  { return nil }
func (f *devFile) Fd() uintptr                           { return ^uintptr(0) }
func (f *devFile) Seek(int64, int) (int64, error)        { return 0, nil }
func (f *devFile) ReadAt(p []byte, _ int64) (int, error) { return f.Read(p) }

// deviceInfo describes a character device everyone can read and write.
type deviceInfo string

func (d deviceInfo) Name() string       { return string(d) }
func (d deviceInfo) Size() int64        { return 0 }
func (d deviceInfo) Mode() fs.FileMode  { return fs.ModeDevice | fs.ModeCharDevice | 0o666 }
func (d deviceInfo) ModTime() time.Time { return time.Time{} }
func (d deviceInfo) IsDir() bool        { return false }
func (d deviceInfo) Sys() any           { return nil }

// checkParent reports why no file can be created at absolute path p:
// its directory is missing or is not a directory.
func checkParent(fsys schemeapi.FileSystem, p string) error {
	info, err := fsys.Stat(path.Dir(p))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return syscall.ENOTDIR
	}
	return nil
}

func (e *Executor) readDirHandler() interp.ReadDirHandlerFunc2 {
	return func(ctx context.Context, p string) ([]fs.DirEntry, error) {
		hc := interp.HandlerCtx(ctx)
		entries, err := e.cfg.FS.ReadDir(absPath(hc.Dir, p))
		if err != nil {
			return nil, pathError("readdir", p, err)
		}
		slices.SortFunc(entries, func(a, b fs.DirEntry) int {
			return strings.Compare(a.Name(), b.Name())
		})
		return entries, nil
	}
}

func (e *Executor) statHandler() interp.StatHandlerFunc {
	return func(ctx context.Context, name string, followSymlinks bool) (fs.FileInfo, error) {
		hc := interp.HandlerCtx(ctx)
		info, err := e.stat(absPath(hc.Dir, name), followSymlinks)
		if err != nil {
			return nil, pathError("stat", name, err)
		}
		return statInfo{FileInfo: info}, nil
	}
}

func (e *Executor) accessHandler() interp.AccessHandlerFunc {
	return func(_ context.Context, p string, mode interp.AccessMode) error {
		info, err := e.stat(p, true)
		if err != nil {
			return pathError("access", p, err)
		}
		if !permits(info, mode) {
			return pathError("access", p, syscall.EACCES)
		}
		return nil
	}
}

// stat describes the file at absolute path abs as a process finds it.
func (e *Executor) stat(abs string, followSymlinks bool) (fs.FileInfo, error) {
	fsys := procFS{FileSystem: e.cfg.FS}
	if followSymlinks {
		return fsys.Stat(abs)
	}
	return fsys.Lstat(abs)
}

// permits checks mode against the owner's permission bits: the user
// owns every file the shell sees.
func permits(info fs.FileInfo, mode interp.AccessMode) bool {
	return uint32(mode)&^uint32(FileMode(info).Perm()>>6) == 0
}

// FileMode returns the mode of info with the permission bits a scheme
// left unset, as an in-memory one does for its directories, filled in:
// 0755 for a directory and 0644 for a regular file. Other modes,
// including any with some permission bit set, are returned unchanged.
func FileMode(info fs.FileInfo) fs.FileMode {
	mode := info.Mode()
	switch {
	case mode.Perm() != 0:
	case mode.IsDir():
		mode |= 0o755
	case mode.IsRegular():
		mode |= 0o644
	}
	return mode
}

// statInfo gives a workspace FileInfo the mode FileMode fills in and
// the Sys the interpreter's -O and -G tests type-assert without
// checking.
type statInfo struct {
	fs.FileInfo
}

func (i statInfo) Mode() fs.FileMode { return FileMode(i.FileInfo) }
func (i statInfo) Sys() any          { return sysStat(i.FileInfo) }

// callHandler routes the builtins whose interpreter implementation
// would consult the host to the shell's own, and strips the pwd option
// that evaluates symlinks on the host.
func callHandler(_ context.Context, args []string) ([]string, error) {
	switch args[0] {
	case "type", "help":
		return append([]string{hiddenPrefix + args[0]}, args[1:]...), nil
	case "command":
		for _, a := range args[1:] {
			if a == "--" || !strings.HasPrefix(a, "-") {
				break
			}
			if strings.ContainsAny(a, "vV") {
				return append([]string{hiddenPrefix + "command"}, args[1:]...), nil
			}
		}
	case "pwd":
		return slices.DeleteFunc(slices.Clone(args), func(a string) bool { return a == "-P" }), nil
	case "source", ".":
		// The interpreter searches the PATH on the host for a bare
		// name before asking the open handler; a relative path goes
		// straight to the handler.
		if len(args) > 1 && !strings.Contains(args[1], "/") {
			args = slices.Clone(args)
			args[1] = "./" + args[1]
		}
	}
	return args, nil
}

// execMiddleware is the only exec handler: it never calls next, so no
// host process can be started.
func (e *Executor) execMiddleware(proc *process, depth int) func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
	return func(interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			return e.execProgram(ctx, proc, depth, args)
		}
	}
}

func (e *Executor) execProgram(ctx context.Context, proc *process, depth int, args []string) error {
	hc := interp.HandlerCtx(ctx)
	shell := "sh"
	if proc != nil {
		shell = proc.shellName
	}
	name := args[0]
	if hidden, ok := strings.CutPrefix(name, hiddenPrefix); ok {
		return e.runVshBuiltin(ctx, e.invocation(runnerConfig{
			proc: proc, dir: hc.Dir, stdin: hc.Stdin, stdout: hc.Stdout, stderr: hc.Stderr,
		}, hidden, args[1:], hc.Env))
	}
	prog, err := e.resolve(name, args[1:], hc.Env.Get("PATH").String(), hc.Dir)
	if err != nil {
		if _, ok := errors.AsType[*badInterpreterError](err); ok {
			fmt.Fprintf(hc.Stderr, "%s: %v\n", shell, err)
			return interp.ExitStatus(126)
		}
		if _, ok := errors.AsType[*notFoundError](err); ok {
			fmt.Fprintf(hc.Stderr, "%s: %s: command not found\n", shell, name)
			return interp.ExitStatus(127)
		}
		if pe, ok := errors.AsType[*os.PathError](err); ok {
			err = pe.Err
		}
		fmt.Fprintf(hc.Stderr, "%s: %s: %v\n", shell, name, err)
		if errors.Is(err, fs.ErrNotExist) {
			return interp.ExitStatus(127)
		}
		return interp.ExitStatus(126)
	}
	env := e.baseEnv(exportedPairs(hc.Env), hc.Dir)
	return e.runProgram(ctx, runnerConfig{
		proc: proc, dir: hc.Dir, env: env,
		stdin: hc.Stdin, stdout: hc.Stdout, stderr: hc.Stderr,
		depth: depth + 1,
	}, prog)
}

// runProgram runs a resolved program to completion in the calling
// goroutine. A shell with neither -c nor a script operand reads its
// script from standard input; interactive sessions are started by
// the process, not here.
func (e *Executor) runProgram(ctx context.Context, cfg runnerConfig, prog *program) error {
	if cfg.depth > maxShellDepth {
		fmt.Fprintf(cfg.stderr, "%s: too many nested shells\n", prog.name)
		return interp.ExitStatus(126)
	}
	switch prog.kind {
	case kindCommand:
		return e.runCommand(ctx, cfg, prog)
	case kindVshBuiltin:
		return e.runVshBuiltin(ctx, e.invocation(cfg, prog.name, prog.args, expand.ListEnviron(cfg.env...)))
	case kindInterpBuiltin:
		r, err := e.newRunner(cfg)
		if err != nil {
			return err
		}
		return r.Run(ctx, callStmt(prog.name, prog.args))
	case kindScript:
		cfg.params = prog.args
		return e.runScript(ctx, cfg, prog.script, prog.arg0)
	}
	sa := prog.shell
	cfg.opts = sa.opts
	cfg.params = sa.params
	switch {
	case sa.hasCommand:
		name := prog.name
		if sa.name != "" {
			name = sa.name
		}
		return e.runScript(ctx, cfg, []byte(sa.command), name)
	case sa.file != "":
		src, err := e.readFile(absPath(cfg.dir, sa.file))
		if err != nil {
			fmt.Fprintf(cfg.stderr, "%s: %s: %v\n", prog.name, sa.file, pathError("open", sa.file, err).Err)
			return interp.ExitStatus(127)
		}
		return e.runScript(ctx, cfg, src, sa.file)
	}
	if cfg.stdin == nil {
		return nil
	}
	src, err := io.ReadAll(cfg.stdin)
	if err != nil {
		fmt.Fprintf(cfg.stderr, "%s: read script: %v\n", prog.name, err)
		return interp.ExitStatus(126)
	}
	return e.runScript(ctx, cfg, src, prog.name)
}

// parseScript parses src as bash, rejecting the constructs that would
// create host FIFOs. name is the script's $0; errors locate the
// problem by line and column only, for the caller to name the script.
func parseScript(src []byte, name string) (*syntax.File, error) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(bytes.NewReader(src), "")
	if err != nil {
		return nil, err
	}
	if err := rejectUnsupported(f); err != nil {
		return nil, err
	}
	f.Name = name
	return f, nil
}

// errUnsupported reports a construct the shell refuses to run.
type errUnsupported struct {
	what string
	pos  syntax.Pos
}

func (e *errUnsupported) Error() string {
	return fmt.Sprintf("%s: %s is not supported", e.pos, e.what)
}

func rejectUnsupported(node syntax.Node) error {
	var err error
	syntax.Walk(node, func(n syntax.Node) bool {
		if err != nil {
			return false
		}
		switch n := n.(type) {
		case *syntax.ProcSubst:
			err = &errUnsupported{what: "process substitution", pos: n.Pos()}
		case *syntax.CoprocClause:
			err = &errUnsupported{what: "coproc", pos: n.Pos()}
		}
		return err == nil
	})
	return err
}

func (e *Executor) runScript(ctx context.Context, cfg runnerConfig, src []byte, name string) error {
	f, err := parseScript(src, name)
	if err != nil {
		fmt.Fprintf(cfg.stderr, "%s: %v\n", name, err)
		return interp.ExitStatus(2)
	}
	r, err := e.newRunner(cfg)
	if err != nil {
		return err
	}
	return r.Run(ctx, f)
}

// callStmt builds the statement running name with args as literals.
func callStmt(name string, args []string) *syntax.Stmt {
	call := &syntax.CallExpr{}
	for _, a := range append([]string{name}, args...) {
		call.Args = append(call.Args, &syntax.Word{
			Parts: []syntax.WordPart{&syntax.SglQuoted{Value: a}},
		})
	}
	return &syntax.Stmt{Cmd: call}
}

// ptyWriter is an output stream attached to a pty.
type ptyWriter interface {
	ptyOf() *pty
}

func (e *Executor) runCommand(ctx context.Context, cfg runnerConfig, prog *program) error {
	cmd := e.commands[prog.name]
	inv := e.invocation(cfg, prog.name, prog.args, expand.ListEnviron(cfg.env...))
	return commandStatus(inv, cmd.Run(ctx, inv))
}

// invocation describes running name with args in cfg's process and
// directory, on its streams, with env.
func (e *Executor) invocation(cfg runnerConfig, name string, args []string, env expand.Environ) *Invocation {
	inv := &Invocation{
		Name: name, Args: args, Dir: cfg.dir, Env: env,
		Stdin: cfg.stdin, Stdout: cfg.stdout, Stderr: cfg.stderr,
		proc: cfg.proc,
	}
	if inv.Stdin == nil {
		inv.Stdin = devNull{}
	}
	if inv.Stdout == nil {
		inv.Stdout = io.Discard
	}
	if inv.Stderr == nil {
		inv.Stderr = io.Discard
	}
	if w, ok := cfg.stdout.(ptyWriter); ok {
		inv.Term = &Term{p: w.ptyOf()}
	}
	inv.FS = procFS{
		FileSystem: e.cfg.FS,
		dev:        devices{proc: inv.proc, stdin: inv.Stdin, stdout: inv.Stdout, stderr: inv.Stderr},
	}
	return inv
}

// commandStatus maps a command's error to an interpreter status,
// printing the errors that are not statuses.
func commandStatus(inv *Invocation, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if exit, ok := errors.AsType[*ExitError](err); ok {
		if exit.Signal != 0 {
			// A shell reports a signalled command as 128 plus the
			// signal's number.
			return interp.ExitStatus(128 + int(exit.Signal))
		}
		return interp.ExitStatus(exit.Code)
	}
	if _, ok := errors.AsType[interp.ExitStatus](err); ok {
		return err
	}
	fmt.Fprintf(inv.Stderr, "%s: %v\n", inv.Name, err)
	return interp.ExitStatus(1)
}

// devNull is /dev/null.
type devNull struct{}

func (devNull) Read([]byte) (int, error)    { return 0, io.EOF }
func (devNull) Write(p []byte) (int, error) { return len(p), nil }
func (devNull) Close() error                { return nil }

// streamFile exposes one of the shell's standard streams as a file.
// Closing it does not close the stream.
type streamFile struct {
	r io.Reader
	w io.Writer
}

func (s *streamFile) Read(p []byte) (int, error) {
	if s.r == nil {
		return 0, syscall.EBADF
	}
	return s.r.Read(p)
}

func (s *streamFile) Write(p []byte) (int, error) {
	if s.w == nil {
		return 0, syscall.EBADF
	}
	return s.w.Write(p)
}

func (s *streamFile) Close() error { return nil }

// ttyFile is /dev/tty: the process's controlling pty.
type ttyFile struct {
	proc *process
}

func (t *ttyFile) Read(p []byte) (int, error) {
	return t.proc.pty.ld.read(t.proc.ctx, p)
}

func (t *ttyFile) Write(p []byte) (int, error) {
	return t.proc.pty.writeOutput(t.proc.ctx, p)
}

func (t *ttyFile) Close() error { return nil }

// fileWriter writes a workspace file at its own offset, since handles
// of an in-memory file share one, and reads it too when opened for
// reading and writing. Close syncs it so that watchers learn of the
// write.
type fileWriter struct {
	f      workspaceapi.File
	off    int64
	append bool
	read   bool
}

func (w *fileWriter) Read(p []byte) (int, error) {
	if !w.read {
		return 0, syscall.EBADF
	}
	r := fileReader{f: w.f, off: w.off}
	n, err := r.Read(p)
	w.off = r.off
	return n, err
}

func (w *fileWriter) Write(p []byte) (int, error) {
	off, whence := w.off, io.SeekStart
	if w.append {
		off, whence = 0, io.SeekEnd
	}
	pos, err := w.f.Seek(off, whence)
	if err != nil {
		return 0, err
	}
	n, err := w.f.Write(p)
	w.off = pos + int64(n)
	return n, err
}

func (w *fileWriter) Close() error {
	err := w.f.Sync()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	return err
}
