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

package mcp

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

// terminateGrace is how long a stdio server is given to exit after its
// stdin is closed before it is signalled.
const terminateGrace = 5 * time.Second

// stderrTailCap bounds how much of the server's most recent stderr is
// retained for diagnostics.
const stderrTailCap = 4096

// ExitFunc is called when a server process exits without the client
// asking it to. detail describes the cause and carries the last stderr
// output.
type ExitFunc func(name, detail string)

// credentialEnvKeys are the variables through which the IDE hands an
// extension its RPC socket and authentication material. A `.mcp.json`
// server is workspace-declared and must never inherit them.
//
// They are neutralized with empty overrides rather than omitted because
// workspaceapi.Cmd.Env is appended to the execution host's environment
// instead of replacing it, and the last assignment wins.
var credentialEnvKeys = []string{
	"RUNE_CERT",
	"RUNE_TOKEN",
	"RUNE_SOCKET",
	"IDE_CERT",
	"IDE_TOKEN",
}

// serverEnv builds the environment overrides for a stdio MCP server:
// the config-declared env first, then the credential blanks so a
// workspace cannot re-inject them.
func serverEnv(cfg ServerConfig) []string {
	env := make([]string, 0, len(cfg.Env)+len(credentialEnvKeys))
	for _, k := range slices.Sorted(maps.Keys(cfg.Env)) {
		env = append(env, k+"="+cfg.Env[k])
	}
	for _, k := range credentialEnvKeys {
		env = append(env, k+"=")
	}
	return env
}

// launchDescription tells the user, in the command authorization prompt,
// that the command starts an MCP server and which configuration asked
// for it.
func launchDescription(name string, cfg ServerConfig) string {
	if cfg.FromWorkspace() {
		return fmt.Sprintf("MCP server %q from the workspace's .mcp.json", name)
	}
	return fmt.Sprintf("MCP server %q from your Rune config", name)
}

// ExecutorTransport returns a TransportFactory that starts stdio MCP
// servers through the workspace executor, so each launch is subject to
// the IDE's StartCommand authorization instead of being spawned
// directly by this process. onExit, if non-nil, observes unexpected
// server death.
func ExecutorTransport(
	exec workspaceapi.Executor, dir string, onExit ExitFunc,
) TransportFactory {
	return func(name string, cfg ServerConfig) (gomcp.Transport, error) {
		kind, err := cfg.Transport()
		if err != nil {
			return nil, err
		}
		if kind != TransportStdio {
			return nil, fmt.Errorf("%s server cannot run through the workspace executor", kind)
		}
		return startServer(exec, dir, name, cfg, onExit)
	}
}

// startServer launches the server and returns a transport over its stdio
// pipes.
//
// The process is started here, in the transport factory, rather than in
// Connect: starting it requires StartCommand authorization, which blocks
// until the user answers a prompt. Connect runs under the MCP handshake
// deadline, so starting there would charge the user's reaction time
// against that deadline and fail the connection.
func startServer(
	exec workspaceapi.Executor, dir, name string, cfg ServerConfig,
	onExit ExitFunc,
) (*executorTransport, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create stdin pipe: %w", err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_, _ = inR.Close(), inW.Close()
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	tail := &stderrTail{}
	// The process outlives the connect deadline, so it must not be tied
	// to the caller's context.
	ctx, cancel := context.WithCancel(context.Background())
	watcher := &processWatcher{ch: make(chan error, 1)}
	pid, err := exec.Start(ctx, workspaceapi.Cmd{
		Path:        cfg.Command,
		Args:        cfg.Args,
		Dir:         dir,
		Env:         serverEnv(cfg),
		Stdin:       inR,
		Stdout:      outW,
		Stderr:      tail,
		Watcher:     watcher,
		Description: launchDescription(name, cfg),
	})
	if err != nil {
		cancel()
		_, _ = inR.Close(), inW.Close()
		_, _ = outR.Close(), outW.Close()
		return nil, fmt.Errorf("start command: %w", err)
	}

	conn, err := (&gomcp.IOTransport{Reader: outR, Writer: inW}).Connect(ctx)
	if err != nil {
		cancel()
		_, _ = inR.Close(), inW.Close()
		_, _ = outR.Close(), outW.Close()
		return nil, err
	}

	econn := &executorConn{
		Connection: conn,
		exec:       exec,
		pid:        pid,
		cancel:     cancel,
		childFiles: []*os.File{inR, outW},
	}
	exited := make(chan struct{})
	econn.exited = exited
	go debug.CapturePanicReport(func() {
		procErr := <-watcher.WatchProcess()
		detail := exitDetail(procErr, tail.String(), pid)
		econn.death.Store(&detail)
		close(exited)
		if onExit != nil && !econn.closing.Load() {
			onExit(name, detail)
		}
		if !econn.closing.Load() {
			// Unblock a handshake still reading from the dead server.
			for _, f := range econn.childFiles {
				_ = f.Close()
			}
		}
	})

	return &executorTransport{conn: econn}, nil
}

// exitDetail composes a human-readable cause for an unexpected server
// exit, including the retained stderr and a pointer to the process
// console.
func exitDetail(err error, stderr string, pid workspaceapi.Pid) string {
	var b strings.Builder
	if err != nil {
		fmt.Fprintf(&b, "exited: %v", err)
	} else {
		b.WriteString("exited unexpectedly")
	}
	fmt.Fprintf(&b, " (pid %d, see `process info %d` in the rune shell)", pid, pid)
	if stderr != "" {
		b.WriteString("\nlast stderr output:\n")
		b.WriteString(stderr)
	}
	return b.String()
}

// stderrTail retains the most recent stderr output of a server process.
type stderrTail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *stderrTail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > stderrTailCap {
		t.buf = t.buf[len(t.buf)-stderrTailCap:]
	}
	return len(p), nil
}

func (t *stderrTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.TrimSpace(string(t.buf))
}

// executorTransport hands out the connection to the already-running
// server process.
type executorTransport struct {
	conn *executorConn
}

func (t *executorTransport) Connect(context.Context) (gomcp.Connection, error) {
	return t.conn, nil
}

// Close tears the server down when the session never got established.
func (t *executorTransport) Close() error { return t.conn.Close() }

// ExitDetail reports why the server process died, or empty while it is
// still running.
func (t *executorTransport) ExitDetail() string {
	if d := t.conn.death.Load(); d != nil {
		return *d
	}
	return ""
}

// executorConn shuts the server process down when the MCP connection
// closes: stdin first, then SIGTERM and SIGKILL as backstops.
type executorConn struct {
	gomcp.Connection

	exec       workspaceapi.Executor
	pid        workspaceapi.Pid
	cancel     context.CancelFunc
	exited     <-chan struct{}
	childFiles []*os.File

	// closing distinguishes a deliberate shutdown from the server
	// dying on its own.
	closing atomic.Bool
	// death carries the exit cause once the process is gone.
	death atomic.Pointer[string]
	once  sync.Once
	err   error
}

func (c *executorConn) Close() error {
	c.once.Do(func() {
		c.closing.Store(true)
		// Closing the connection closes stdin, which is how a
		// well-behaved stdio server is asked to exit.
		c.err = c.Connection.Close()
		for _, f := range c.childFiles {
			_ = f.Close()
		}
		if !c.waitExit(terminateGrace) {
			_ = c.exec.Signal(c.pid, syscall.SIGTERM)
			if !c.waitExit(terminateGrace) {
				_ = c.exec.Signal(c.pid, syscall.SIGKILL)
			}
		}
		c.cancel()
	})
	return c.err
}

func (c *executorConn) waitExit(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c.exited:
		return true
	case <-timer.C:
		return false
	}
}

type processWatcher struct {
	ch chan error
}

func (w *processWatcher) WatchProcess() chan error { return w.ch }
