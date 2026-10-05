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
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// helperEnv makes the test binary re-exec itself as a stdio MCP server.
const helperEnv = "RUNE_MCP_TEST_HELPER"

// helperFailEnv makes the helper die on stderr instead of serving MCP,
// like a server missing its credentials.
const helperFailEnv = "RUNE_MCP_TEST_FAIL"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(runHelperServer())
	}
	os.Exit(m.Run())
}

// runHelperServer serves a single tool over stdin/stdout. The tool
// reports whether a Rune credential variable reached this process, so
// the round-trip test can assert the scrubbing from the child's view.
func runHelperServer() int {
	if os.Getenv(helperFailEnv) == "1" {
		fmt.Fprintln(os.Stderr, "boom: credentials missing")
		return 1
	}
	fmt.Fprintln(os.Stderr, "helper ready")
	server := gomcp.NewServer(
		&gomcp.Implementation{Name: "helper", Version: "v1.0.0"}, nil,
	)
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        "echo_env",
		Description: "reports the child's view of selected env vars",
	}, func(_ context.Context, _ *gomcp.CallToolRequest, _ any) (*gomcp.CallToolResult, any, error) {
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{&gomcp.TextContent{
				Text: "RUNE_TOKEN=" + os.Getenv("RUNE_TOKEN") +
					" DECLARED=" + os.Getenv("DECLARED"),
			}},
		}, nil, nil
	})
	if err := server.Run(context.Background(), &gomcp.StdioTransport{}); err != nil {
		return 1
	}
	return 0
}

// localExec runs commands in-process, mirroring how the workspace file
// scheme hands *os.File stdio straight to the child.
type localExec struct {
	mu    chan struct{}
	calls []workspaceapi.Cmd
	procs map[workspaceapi.Pid]*os.Process
	// delay simulates the IDE holding StartCommand while it waits for
	// the user to answer the authorization prompt.
	delay time.Duration
}

func newLocalExec() *localExec {
	return &localExec{
		mu:    make(chan struct{}, 1),
		procs: make(map[workspaceapi.Pid]*os.Process),
	}
}

func (e *localExec) Start(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	time.Sleep(e.delay)

	e.mu <- struct{}{}
	e.calls = append(e.calls, cmd)
	<-e.mu

	c := exec.Command(cmd.Path, cmd.Args...) // #nosec G204 -- test helper
	c.Dir = cmd.Dir
	c.Env = append(os.Environ(), cmd.Env...)
	if f, ok := cmd.Stdin.(*os.File); ok {
		c.Stdin = f
	}
	if f, ok := cmd.Stdout.(*os.File); ok {
		c.Stdout = f
	}
	if cmd.Stderr != nil {
		c.Stderr = cmd.Stderr
	}
	if err := c.Start(); err != nil {
		return 0, err
	}
	pid := workspaceapi.Pid(c.Process.Pid)

	e.mu <- struct{}{}
	e.procs[pid] = c.Process
	<-e.mu

	if cmd.Watcher != nil {
		ch := cmd.Watcher.WatchProcess()
		go func() { ch <- c.Wait() }()
	}
	return pid, nil
}

func (e *localExec) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	e.mu <- struct{}{}
	p := e.procs[pid]
	<-e.mu
	if p == nil {
		return nil
	}
	return p.Signal(sig)
}

func (e *localExec) Close() error { return nil }

func (e *localExec) started() []workspaceapi.Cmd {
	e.mu <- struct{}{}
	defer func() { <-e.mu }()
	return append([]workspaceapi.Cmd(nil), e.calls...)
}

// killAll SIGKILLs every process this executor started.
func (e *localExec) killAll() {
	e.mu <- struct{}{}
	defer func() { <-e.mu }()
	for _, p := range e.procs {
		_ = p.Kill()
	}
}

func TestServerEnvScrubsRuneCredentials(t *testing.T) {
	t.Parallel()

	env := serverEnv(ServerConfig{Env: map[string]string{
		"DECLARED":   "keep",
		"RUNE_TOKEN": "attacker-supplied",
	}})

	// Declared vars are passed through; credentials are blanked last so
	// the workspace-declared value cannot win.
	assert.Equal(t, []string{
		"DECLARED=keep",
		"RUNE_TOKEN=attacker-supplied",
		"RUNE_CERT=",
		"RUNE_TOKEN=",
		"RUNE_SOCKET=",
		"IDE_CERT=",
		"IDE_TOKEN=",
	}, env)
}

func TestExecutorTransportRejectsNonStdio(t *testing.T) {
	t.Parallel()

	exec := newLocalExec()
	factory := ExecutorTransport(exec, t.TempDir(), nil)

	_, err := factory("remote", ServerConfig{Type: "http", URL: "https://example.com"})
	require.ErrorContains(t, err, "remote MCP servers are not supported")

	_, err = factory("empty", ServerConfig{Type: "stdio"})
	require.ErrorContains(t, err, "no command")

	assert.Empty(t, exec.started(), "no process may start for a rejected config")
}

func TestExecutorTransportStartsThroughExecutor(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	e := newLocalExec()
	m := NewManagerWithTransport(ExecutorTransport(e, dir, nil))

	m.Connect(context.Background(), Config{
		MCPServers: map[string]ServerConfig{
			"helper": {
				Type:    "stdio",
				Command: os.Args[0],
				Args:    []string{"-test.run=TestMain"},
				Env:     map[string]string{helperEnv: "1"},
			},
		},
	})
	require.NoError(t, m.Close())

	started := e.started()
	require.Len(t, started, 1, "launch must go through the workspace executor")
	assert.Equal(t, os.Args[0], started[0].Path)
	assert.Equal(t, []string{"-test.run=TestMain"}, started[0].Args)
	assert.Equal(t, dir, started[0].Dir)
	assert.Contains(t, started[0].Env, "RUNE_TOKEN=")
	assert.NotNil(t, started[0].Stdin)
	assert.NotNil(t, started[0].Stdout)
}

func TestExecutorTransportRoundTrip(t *testing.T) {
	t.Setenv("RUNE_TOKEN", "super-secret")

	e := newLocalExec()
	m := NewManagerWithTransport(ExecutorTransport(e, t.TempDir(), nil))

	tools := m.Connect(context.Background(), Config{
		MCPServers: map[string]ServerConfig{
			"helper": {
				Type:    "stdio",
				Command: os.Args[0],
				Env:     map[string]string{helperEnv: "1", "DECLARED": "visible"},
			},
		},
	})
	t.Cleanup(func() { _ = m.Close() })

	require.Len(t, tools, 1, "stdio pipes must carry a real MCP session")
	assert.Equal(t, "helper_echo_env", tools[0].Definition().Function.Name)

	res := tools[0].Execute(context.Background(), "{}")
	require.False(t, res.IsError, res.Content)
	assert.Contains(t, res.Content, "RUNE_TOKEN= ",
		"Rune credentials must not reach the child")
	assert.Contains(t, res.Content, "DECLARED=visible")
}

func TestExecutorTransportAuthorizationWaitDoesNotConsumeConnectDeadline(t *testing.T) {
	restore := connectTimeout
	connectTimeout = 250 * time.Millisecond
	t.Cleanup(func() { connectTimeout = restore })

	e := newLocalExec()
	e.delay = 3 * connectTimeout

	m := NewManagerWithTransport(ExecutorTransport(e, t.TempDir(), nil))
	tools := m.Connect(context.Background(), Config{
		MCPServers: map[string]ServerConfig{
			"helper": {
				Type:    "stdio",
				Command: os.Args[0],
				Env:     map[string]string{helperEnv: "1"},
			},
		},
	})
	t.Cleanup(func() { _ = m.Close() })

	require.Len(t, tools, 1,
		"slow authorization must not fail the connect")
	require.Len(t, m.Servers(), 1)
	assert.Equal(t, StatusConnected, m.Servers()[0].Status)
}

func TestUnexpectedServerDeathReportsCauseAndStderr(t *testing.T) {
	t.Parallel()

	e := newLocalExec()
	m := NewManager(e, t.TempDir())
	exitCh := make(chan string, 1)
	m.OnServerExit = func(name, detail string) { exitCh <- name + " " + detail }

	tools := m.Connect(context.Background(), Config{
		MCPServers: map[string]ServerConfig{
			"helper": {
				Type:    "stdio",
				Command: os.Args[0],
				Env:     map[string]string{helperEnv: "1"},
			},
		},
	})
	t.Cleanup(func() { _ = m.Close() })
	require.Len(t, tools, 1)

	e.killAll()

	select {
	case detail := <-exitCh:
		assert.Contains(t, detail, "helper", "must name the dead server")
		assert.Contains(t, detail, "exited", "must state the cause")
		assert.Contains(t, detail, "helper ready",
			"must carry the retained stderr output")
		assert.Contains(t, detail, "process info",
			"must point at the process console")
	case <-time.After(10 * time.Second):
		t.Fatal("expected an unexpected-exit report")
	}

	servers := m.Servers()
	require.Len(t, servers, 1)
	assert.Equal(t, StatusError, servers[0].Status,
		"a dead server must not keep reporting connected")
}

func TestDeliberateCloseDoesNotReportServerExit(t *testing.T) {
	t.Parallel()

	e := newLocalExec()
	m := NewManager(e, t.TempDir())
	var exits atomic.Int32
	m.OnServerExit = func(string, string) { exits.Add(1) }

	tools := m.Connect(context.Background(), Config{
		MCPServers: map[string]ServerConfig{
			"helper": {
				Type:    "stdio",
				Command: os.Args[0],
				Env:     map[string]string{helperEnv: "1"},
			},
		},
	})
	require.Len(t, tools, 1)

	require.NoError(t, m.Close())
	time.Sleep(50 * time.Millisecond)
	assert.Zero(t, exits.Load(),
		"asking the server to shut down is not an unexpected exit")
}

func TestServerDeathDuringConnectFailsFastWithStderr(t *testing.T) {
	restore := connectTimeout
	connectTimeout = 10 * time.Second
	t.Cleanup(func() { connectTimeout = restore })

	e := newLocalExec()
	m := NewManager(e, t.TempDir())

	start := time.Now()
	_, err := m.ConnectServer(context.Background(), "dying", ServerConfig{
		Type:    "stdio",
		Command: os.Args[0],
		Env:     map[string]string{helperEnv: "1", helperFailEnv: "1"},
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "context deadline exceeded",
		"death must be reported as such, not as a timeout")
	assert.Contains(t, err.Error(), "boom: credentials missing",
		"the server's stderr must reach the error")
	assert.Less(t, elapsed, connectTimeout/2,
		"death must fail the connect immediately")

	servers := m.Servers()
	require.Len(t, servers, 1)
	assert.Equal(t, StatusError, servers[0].Status)
	assert.Contains(t, servers[0].Error, "boom: credentials missing")
}
