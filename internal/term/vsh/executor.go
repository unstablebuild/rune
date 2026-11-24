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

// Package vsh is an in-process shell for workspaces whose files live
// only in Rune's memory. It serves the terminal emulator a pty with
// an interactive shell on it, runs plain commands for the console,
// agents and extensions, and resolves command names the way the
// authorizer expects, all against a schemeapi.FileSystem and without
// ever touching the host: no process, no host file, no host PATH.
package vsh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

var (
	_ schemeapi.Terminal = (*Executor)(nil)
	_ schemeapi.Executor = (*Executor)(nil)
)

// Config configures an Executor.
type Config struct {
	// Scheme names the workspace's URI scheme, for prompts.
	Scheme string
	// Root is HOME and the directory commands without one run in. A
	// relative Root is taken from the filesystem's root.
	Root string
	// Bin is the directory the programs are installed in, the default
	// PATH and the home of SHELL; empty is /bin. A relative Bin is
	// taken from the filesystem's root. A filesystem whose root the
	// shell does not own, such as the host's, needs one in its tree.
	Bin string
	// FS is the filesystem the shell runs against.
	FS schemeapi.FileSystem
	// AllocFd returns descriptor numbers for pty files, unique in the
	// namespace FS's owner resolves with NewFile. Nil uses a private
	// counter.
	AllocFd func() uintptr
	// Env is the base environment of every process, such as PS1.
	Env []string
	// Banner is written once at the start of an interactive session;
	// empty writes nothing.
	Banner string
	// Commands are the programs installed in Bin besides the shells
	// and the shell's own builtins.
	Commands []Command
}

var pidCounter atomic.Int64

// Executor runs commands and serves ptys for one filesystem.
type Executor struct {
	cfg      Config
	commands map[string]Command
	ctx      context.Context
	cancel   context.CancelFunc
	closedCh chan struct{}

	mu     sync.Mutex
	closed bool
	procs  map[workspaceapi.Pid]*process
	ptys   map[*pty]ptyHandles
	wg     sync.WaitGroup
	fds    atomic.Uint64
}

// ptyHandles are the one master and slave handle of a pty, which
// NewFile hands out again so that a Close over RPC releases the same
// reference as a local one.
type ptyHandles struct {
	master *ptyMaster
	slave  *ptySlave
}

// NewExecutor installs the programs into cfg.FS and returns the
// Executor serving them.
func NewExecutor(cfg Config) (*Executor, error) {
	if cfg.FS == nil {
		return nil, errors.New("vsh: Config.FS is required")
	}
	// Every path the shell derives from Root must be absolute: a
	// scheme reads a relative path's first element as a host.
	cfg.Root = absPath("/", cfg.Root)
	if cfg.Bin == "" {
		cfg.Bin = defaultBin
	}
	cfg.Bin = absPath("/", cfg.Bin)
	e := &Executor{
		cfg:      cfg,
		commands: make(map[string]Command, len(cfg.Commands)),
		closedCh: make(chan struct{}),
		procs:    make(map[workspaceapi.Pid]*process),
		ptys:     make(map[*pty]ptyHandles),
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	if e.cfg.AllocFd == nil {
		e.cfg.AllocFd = func() uintptr { return uintptr(e.fds.Add(1)) }
	}
	for _, c := range cfg.Commands {
		if c.Name == "" || c.Run == nil {
			return nil, fmt.Errorf("vsh: command %q has no name or Run", c.Name)
		}
		if isShellName(c.Name) {
			return nil, fmt.Errorf("vsh: command %q shadows a shell", c.Name)
		}
		e.commands[c.Name] = c
	}
	if err := e.installBin(); err != nil {
		return nil, err
	}
	return e, nil
}

// NewPty satisfies schemeapi.Terminal.
func (e *Executor) NewPty(context.Context) (workspaceapi.Pty, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return workspaceapi.Pty{}, os.ErrClosed
	}
	p := newPty(e.cfg.AllocFd(), e.cfg.AllocFd())
	h := ptyHandles{master: &ptyMaster{p: p}, slave: &ptySlave{p: p}}
	e.ptys[p] = h
	p.addHangupHook(func() {
		e.mu.Lock()
		delete(e.ptys, p)
		e.mu.Unlock()
	})
	return workspaceapi.Pty{Master: h.master, Slave: h.slave}, nil
}

// SetPtySize satisfies schemeapi.Terminal. It reports ErrPtyClosed for
// a pty whose master is closed or that this Executor did not create.
func (e *Executor) SetPtySize(pt workspaceapi.Pty, size workspaceapi.PtySize) error {
	m, ok := pt.Master.(*ptyMaster)
	if !ok || !e.owns(m.p) {
		return ErrPtyClosed
	}
	return m.p.setSize(size)
}

// ForegroundProcessGroup satisfies schemeapi.Terminal. It always fails
// with an error wrapping errors.ErrUnsupported: the shell has no job
// control, so no process group leads its ptys.
func (e *Executor) ForegroundProcessGroup(workspaceapi.Pty) (int, error) {
	return 0, fmt.Errorf("vsh: no job control: %w", errors.ErrUnsupported)
}

func (e *Executor) owns(p *pty) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.ptys[p]
	return ok
}

// NewFile returns the master or slave handle of a live pty by its
// descriptor number and name, or nil.
func (e *Executor) NewFile(fd uintptr, name string) workspaceapi.File {
	e.mu.Lock()
	defer e.mu.Unlock()
	for p, h := range e.ptys {
		switch {
		case p.masterFd == fd && h.master.Name() == name:
			return h.master
		case p.slaveFd == fd && h.slave.Name() == name:
			return h.slave
		}
	}
	return nil
}

// StartCommand satisfies schemeapi.Executor. The command's Path is
// resolved like LookPath does; a shell with no script on a pty slave
// runs interactively. ctx ends the process as SIGKILL would.
func (e *Executor) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return 0, os.ErrClosed
	}
	dir := cmd.Dir
	if dir == "" {
		dir = e.cfg.Root
	}
	dir = absPath(e.cfg.Root, e.expandHome(dir))
	if info, err := e.cfg.FS.Stat(dir); err != nil {
		return 0, &os.PathError{Op: "chdir", Path: cmd.Dir, Err: syscall.ENOENT}
	} else if !info.IsDir() {
		return 0, &os.PathError{Op: "chdir", Path: cmd.Dir, Err: syscall.ENOTDIR}
	}
	env := e.baseEnv(cmd.Env, dir)
	// The program itself is found in the Executor's PATH, as exec finds
	// a command in its parent's; cmd.Env applies to what it runs.
	pathEnv, _ := envValue(e.cfg.Env, "PATH")
	prog, err := e.resolve(cmd.Path, cmd.Args, pathEnv, dir)
	if err != nil {
		return 0, err
	}

	proc := &process{
		e:         e,
		pid:       workspaceapi.Pid(pidCounter.Add(1)),
		cmd:       cmd,
		prog:      prog,
		shellName: prog.name,
		done:      make(chan struct{}),
	}
	proc.ctx, proc.cancel = context.WithCancelCause(e.ctx)
	if ctx != nil && ctx.Done() != nil {
		context.AfterFunc(ctx, func() { proc.cancel(signalCause{sig: syscall.SIGKILL}) })
	}
	if s, ok := cmd.Stdin.(*ptySlave); ok && e.owns(s.p) {
		proc.pty = s.p
	}

	var stdin io.Reader
	switch r := cmd.Stdin.(type) {
	case nil:
	case *ptySlave:
		if proc.pty != nil {
			stdin = &ptyStdin{p: proc.pty, ctx: proc.ctx}
		} else {
			stdin = r
		}
	default:
		stdin = r
	}
	outMu := new(sync.Mutex)
	wrap := func(w io.Writer) io.Writer {
		switch w := w.(type) {
		case nil:
			return io.Discard
		case *ptySlave:
			if e.owns(w.p) {
				return &processWriter{p: w.p, ctx: proc.ctx}
			}
			return &lockedWriter{mu: outMu, w: w}
		default:
			return &lockedWriter{mu: outMu, w: w}
		}
	}
	proc.cfg = runnerConfig{
		proc:   proc,
		dir:    dir,
		env:    env,
		stdin:  stdin,
		stdout: wrap(cmd.Stdout),
		stderr: wrap(cmd.Stderr),
	}
	if proc.pty != nil && prog.kind == kindShell && !prog.shell.hasCommand && prog.shell.file == "" {
		proc.session = newSession(proc)
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		proc.cancel(nil)
		return 0, os.ErrClosed
	}
	e.procs[proc.pid] = proc
	e.wg.Add(1)
	e.mu.Unlock()
	if proc.pty != nil {
		proc.pty.addSlaveRef()
		proc.pty.addHangupHook(func() { proc.cancel(signalCause{sig: syscall.SIGHUP}) })
	}
	go debug.CapturePanicReport(proc.run)
	return proc.pid, nil
}

// Signal satisfies schemeapi.Executor. An unknown pid reports
// os.ErrProcessDone.
func (e *Executor) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	e.mu.Lock()
	proc, ok := e.procs[pid]
	e.mu.Unlock()
	if !ok {
		return os.ErrProcessDone
	}
	proc.signal(sig)
	return nil
}

// Close satisfies schemeapi.Executor: every process is hung up and
// awaited, every pty closed. It does not wait for watchers to take
// the exit statuses.
func (e *Executor) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	procs := make([]*process, 0, len(e.procs))
	for _, p := range e.procs {
		procs = append(procs, p)
	}
	ptys := make([]*pty, 0, len(e.ptys))
	for p := range e.ptys {
		ptys = append(ptys, p)
	}
	e.mu.Unlock()

	for _, p := range procs {
		p.cancel(signalCause{sig: syscall.SIGHUP})
	}
	for _, p := range ptys {
		p.closeMaster()
	}
	e.cancel()
	e.wg.Wait()
	close(e.closedCh)
	return nil
}
