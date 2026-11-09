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

package llamaserver_test

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"syscall"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

// realExecutor is a minimal os/exec-based schemeapi.Executor for the e2e
// suite. It launches real child processes (llama-server), kills them when
// the lifecycle context is cancelled, and drives each Cmd's Watcher on
// exit — matching the contract the pool relies on in production.
type realExecutor struct {
	mu    sync.Mutex
	next  workspaceapi.Pid
	procs map[workspaceapi.Pid]*exec.Cmd
	wg    sync.WaitGroup
}

func (e *realExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	c := exec.Command(cmd.Path, cmd.Args...) // #nosec G204 -- test-only, args from our config
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	// New process group so we can signal the whole tree on cancel.
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := c.Start(); err != nil {
		return 0, err
	}

	e.mu.Lock()
	if e.procs == nil {
		e.procs = make(map[workspaceapi.Pid]*exec.Cmd)
	}
	e.next++
	pid := e.next
	e.procs[pid] = c
	e.mu.Unlock()

	var watchCh chan error
	if cmd.Watcher != nil {
		watchCh = cmd.Watcher.WatchProcess()
	}

	e.wg.Add(1)
	go debug.CapturePanicReport(func() {
		defer e.wg.Done()
		waitErr := make(chan error, 1)
		go debug.CapturePanicReport(func() { waitErr <- c.Wait() })

		select {
		case <-ctx.Done():
			// Kill the whole process group; the child may spawn helpers.
			if c.Process != nil {
				_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
			}
			<-waitErr
		case err := <-waitErr:
			if watchCh != nil {
				select {
				case watchCh <- err:
				default:
				}
			}
		}
		e.mu.Lock()
		delete(e.procs, pid)
		e.mu.Unlock()
	})

	return pid, nil
}

func (e *realExecutor) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	e.mu.Lock()
	c := e.procs[pid]
	e.mu.Unlock()
	if c == nil || c.Process == nil {
		return nil
	}
	return c.Process.Signal(sig)
}

func (*realExecutor) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (e *realExecutor) Close() error { return nil }

// wait blocks until every launched process goroutine has exited. Used as a
// test cleanup to avoid leaking child processes across tests.
func (e *realExecutor) wait() {
	e.mu.Lock()
	for _, c := range e.procs {
		if c.Process != nil {
			_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		}
	}
	e.mu.Unlock()
	e.wg.Wait()
}
