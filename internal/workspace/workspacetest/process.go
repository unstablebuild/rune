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

package workspacetest

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
)

var (
	_ schemeapi.Terminal = (*PtyExecutor)(nil)
	_ schemeapi.Executor = (*PtyExecutor)(nil)
)

// ErrNotPtyCommand is returned by PtyExecutor.StartCommand for a
// command whose Stdin is not a pty created by that PtyExecutor.
var ErrNotPtyCommand = errors.New("workspacetest: command is not attached to a PtyExecutor pty")

// PtyExecutor is a schemeapi.Terminal and schemeapi.Executor whose
// commands are fake processes attached to fake ptys. Tests drive each
// process they receive from Started: what it prints reaches its pty
// master, and the test decides when its pty hangs up and which exit
// status its watcher gets, in either order, as with a real process.
type PtyExecutor struct {
	ptys    atomic.Int64
	pids    atomic.Int64
	started chan *Process
}

// NewPtyExecutor returns a PtyExecutor with no processes.
func NewPtyExecutor() *PtyExecutor {
	return &PtyExecutor{started: make(chan *Process, 64)}
}

// Started delivers every process StartCommand started, in start order.
func (e *PtyExecutor) Started() <-chan *Process {
	return e.started
}

// NewPty satisfies schemeapi.Terminal. Slaves are named /dev/pts/N,
// numbered from 1 in creation order.
func (e *PtyExecutor) NewPty(context.Context) (workspaceapi.Pty, error) {
	master := &ptyMaster{}
	master.cond = sync.NewCond(&master.mu)
	return workspaceapi.Pty{
		Master: master,
		Slave: &ptySlave{
			name:   fmt.Sprintf("/dev/pts/%d", e.ptys.Add(1)),
			master: master,
		},
	}, nil
}

// SetPtySize satisfies schemeapi.Terminal.
func (e *PtyExecutor) SetPtySize(workspaceapi.Pty, workspaceapi.PtySize) error {
	return nil
}

// StartCommand satisfies schemeapi.Executor. It fails with
// ErrNotPtyCommand unless cmd's Stdin is a pty slave from NewPty.
func (e *PtyExecutor) StartCommand(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	slave, ok := cmd.Stdin.(*ptySlave)
	if !ok {
		return 0, ErrNotPtyCommand
	}
	p := &Process{
		Cmd:    cmd,
		Pid:    workspaceapi.Pid(1000 + e.pids.Add(1)),
		master: slave.master,
	}
	e.started <- p
	return p.Pid, nil
}

// Signal satisfies schemeapi.Executor. Fake processes ignore signals.
func (e *PtyExecutor) Signal(workspaceapi.Pid, syscall.Signal) error {
	return nil
}

// Close satisfies schemeapi.Executor.
func (e *PtyExecutor) Close() error {
	return nil
}

// Process is a fake process started by a PtyExecutor.
type Process struct {
	Cmd    workspaceapi.Cmd
	Pid    workspaceapi.Pid
	master *ptyMaster
}

// Print writes s to the process' pty as its output.
func (p *Process) Print(s string) {
	p.master.feed([]byte(s))
}

// HangUp closes the process' side of its pty, as exiting does: the
// master reads EOF once the output printed before it is read.
func (p *Process) HangUp() {
	p.master.hangUp()
}

// ReportExit hands err to the command's watcher as the process' exit
// status, as an executor does once it waited for the process. It
// blocks until the watcher takes it, and does nothing for a command
// without a watcher.
func (p *Process) ReportExit(err error) {
	if p.Cmd.Watcher == nil {
		return
	}
	if ch := p.Cmd.Watcher.WatchProcess(); ch != nil {
		ch <- err
	}
}

type ptyMaster struct {
	File
	mu     sync.Mutex
	cond   *sync.Cond
	out    []byte
	hungUp bool
	closed bool
}

func (m *ptyMaster) Read(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for len(m.out) == 0 && !m.hungUp && !m.closed {
		m.cond.Wait()
	}
	switch {
	case m.closed:
		return 0, os.ErrClosed
	case len(m.out) == 0:
		return 0, io.EOF
	}
	n := copy(b, m.out)
	m.out = m.out[n:]
	return n, nil
}

// Write takes what the terminal types into the process, which ignores it.
func (m *ptyMaster) Write(b []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, os.ErrClosed
	}
	return len(b), nil
}

func (m *ptyMaster) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.cond.Broadcast()
	return nil
}

func (m *ptyMaster) feed(b []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.out = append(m.out, b...)
	m.cond.Broadcast()
}

func (m *ptyMaster) hangUp() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hungUp = true
	m.cond.Broadcast()
}

type ptySlave struct {
	File
	name   string
	master *ptyMaster
}

func (s *ptySlave) Name() string {
	return s.name
}

// Write reaches the master, as on a real pty.
func (s *ptySlave) Write(b []byte) (int, error) {
	s.master.feed(b)
	return len(b), nil
}

// Close keeps the pty up: the process holds its own copy of the slave
// until HangUp.
func (s *ptySlave) Close() error {
	return nil
}
