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
	"io"
	"sync"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// watcherWaitTimeout bounds how long an exited process waits for its
// watcher to take the exit status.
const watcherWaitTimeout = 2 * time.Minute

// process is one started command.
type process struct {
	e      *Executor
	pid    workspaceapi.Pid
	cmd    workspaceapi.Cmd
	ctx    context.Context
	cancel context.CancelCauseFunc
	// pty is the terminal the process is attached to, when its
	// standard input is a pty slave.
	pty *pty
	// session is set for an interactive shell.
	session *session
	// shellName is what error messages name the shell as.
	shellName string
	prog      *program
	cfg       runnerConfig
	done      chan struct{}
}

// run executes the process to completion, then reports its exit to
// the watcher with no lock held and without the Executor waiting on
// the delivery.
func (p *process) run() {
	var err error
	if p.session != nil {
		err = p.session.run()
	} else {
		err = p.e.runProgram(p.ctx, p.cfg, p.prog)
	}
	// A signal ends the process even when the program then returns
	// cleanly, as one reading the EOF of a hung-up terminal does. The
	// hangup wakes such a reader before its hook delivers the SIGHUP.
	if err == nil {
		if cause, ok := errors.AsType[signalCause](context.Cause(p.ctx)); ok {
			err = cause
		} else if p.pty != nil && p.pty.isHungUp() {
			err = signalCause{sig: syscall.SIGHUP}
		}
	}
	exit := exitError(p.ctx, err)
	if p.pty != nil {
		p.pty.releaseSlaveRef()
	}
	p.cancel(nil)

	p.e.mu.Lock()
	delete(p.e.procs, p.pid)
	p.e.mu.Unlock()
	close(p.done)
	p.e.wg.Done()

	if p.cmd.Watcher == nil {
		return
	}
	ch := p.cmd.Watcher.WatchProcess()
	if ch == nil {
		return
	}
	timer := time.NewTimer(watcherWaitTimeout)
	defer timer.Stop()
	select {
	case ch <- exit:
	case <-p.e.closedCh:
	case <-timer.C:
	}
}

// signal delivers sig. SIGINT interrupts an interactive session's
// foreground command or prompt line and terminates any other process;
// the terminating signals end the process; 0 only probes.
func (p *process) signal(sig syscall.Signal) {
	switch sig {
	case 0:
	case syscall.SIGINT:
		if p.session != nil {
			p.session.interrupt()
			return
		}
		p.cancel(signalCause{sig: sig})
	case syscall.SIGHUP, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGKILL:
		p.cancel(signalCause{sig: sig})
	}
}

// processWriter is a process's output stream onto its pty. A blocked
// write ends with the process.
type processWriter struct {
	p   *pty
	ctx context.Context
}

func (w *processWriter) Write(b []byte) (int, error) { return w.p.writeOutput(w.ctx, b) }
func (w *processWriter) ptyOf() *pty                 { return w.p }

// ptyStdin is a process's cooked standard input from its pty.
type ptyStdin struct {
	p   *pty
	ctx context.Context
}

func (r *ptyStdin) Read(b []byte) (int, error) { return r.p.ld.read(r.ctx, b) }

// lockedWriter serializes the writes of streams a caller shares one
// buffer for.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(b)
}
