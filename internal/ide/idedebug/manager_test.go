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

package idedebug

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/google/go-dap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/debugapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	rdebug "unstable.build/rune/internal/debug"
)

// fakeSubscriber is a no-op EventSubscriber for tests that do
// not exercise the event stream.
type fakeSubscriber struct{}

func (fakeSubscriber) OnEvent(dap.EventMessage) {}
func (fakeSubscriber) OnClose(string)           {}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file:///tmp/test")
	require.NoError(t, err)
	return New(uri, nil, nil, Config{})
}

type libDirPkgManager struct{ paths []string }

func (p libDirPkgManager) LibDir(context.Context, string) (iterator.Iterator[string], error) {
	return iterator.FromSlice(p.paths), nil
}

func TestFindBinaryRunsTheHostBinary(t *testing.T) {
	t.Parallel()
	const hostPath = "/home/studio/.rune/pkg/go/1.24.0/bin/dlv"
	uri, err := workspaceapi.ParseURI("file:///tmp/test")
	require.NoError(t, err)
	m := New(uri, nil, libDirPkgManager{paths: []string{
		"/home/studio/.rune/pkg/go/1.24.0/bin/go", hostPath,
	}}, Config{})
	t.Cleanup(func() { _ = m.Close() })

	got, err := m.findBinary(t.Context(), &debugConfig{langID: "go", command: "dlv"})
	require.NoError(t, err)
	assert.Equal(t, hostPath, got)
}

func TestSessionIDRequired(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Close() })

	ctx := context.Background()
	_, err := m.Threads(ctx, "missing")
	assert.ErrorIs(t, err, debugapi.ErrSessionNotFound)

	_, err = m.Continue(ctx, "missing", &dap.ContinueArguments{})
	assert.ErrorIs(t, err, debugapi.ErrSessionNotFound)

	err = m.Terminate(ctx, "missing", &dap.TerminateArguments{})
	assert.ErrorIs(t, err, debugapi.ErrSessionNotFound)
}

func TestCreateSessionNoAdapter(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Close() })

	_, _, err := m.CreateSession(context.Background(), "unknown",
		debugapi.ClientCapabilities{}, fakeSubscriber{})
	assert.True(t, errors.Is(err, debugapi.ErrNoAdapterConfigured),
		"expected ErrNoAdapterConfigured, got %v", err)
}

func TestManagerCloseTerminatesAdapterAndGoroutines(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)

	// Inject a session mirroring startSession's invariants instead of
	// launching a real DAP adapter (which would need a fake binary):
	// a *debugServer parented on m.ctx with an open conn, a watcher
	// channel, and a watchSession goroutine tracked by m.wg.
	a, b := net.Pipe()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })

	watcher := make(chan error, 1)
	srv := newDebugServer(m.ctx, debugConfig{langID: "test", command: "test"},
		"/bin/test", nil, m.rootURI,
		debugapi.ClientCapabilities{}, fakeSubscriber{})
	srv.conn = a
	srv.watcher = watcher
	srv.alive = true

	const sessionID = "test-session"
	m.mu.Lock()
	m.sessions[sessionID] = srv
	m.mu.Unlock()

	m.wg.Add(1)
	done := make(chan struct{})
	go func() {
		defer m.wg.Done()
		defer close(done)
		m.watchSession(sessionID, &srv.cfg, srv)
	}()

	select {
	case <-done:
		t.Fatal("watchSession exited before Close")
	case <-time.After(50 * time.Millisecond):
	}

	require.NoError(t, m.Close())

	require.Error(t, m.ctx.Err(),
		"Manager.Close must cancel m.ctx")
	require.Error(t, srv.ctx.Err(),
		"Manager.Close must cancel each debugServer ctx so the "+
			"adapter subprocess is killed")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchSession goroutine did not exit after Manager.Close")
	}
}

func TestManagerConcurrentSessionAccess(t *testing.T) {
	t.Parallel()
	m := newTestManager(t)

	const workers = 8
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range workers {
		sessionID := newTestSessionID(i)
		wg.Go(func() {
			for range 50 {
				srv := newDebugServer(m.ctx, debugConfig{langID: "test", command: "test"},
					"/bin/test", nil, m.rootURI,
					debugapi.ClientCapabilities{}, fakeSubscriber{})
				m.mu.Lock()
				m.sessions[sessionID] = srv
				m.mu.Unlock()

				_, _ = m.Threads(ctx, sessionID)
				_ = m.Terminate(ctx, "missing", &dap.TerminateArguments{})

				m.removeSession(sessionID, srv)
			}
		})
	}

	wg.Go(func() {
		_ = m.Close()
	})

	wg.Wait()
}

func newTestSessionID(i int) string {
	return "session-" + string(rune('a'+i))
}

// closeRecorder records the reason passed to OnClose so tests can
// assert session teardown semantics.
type closeRecorder struct{ ch chan string }

func (closeRecorder) OnEvent(dap.EventMessage) {}
func (r closeRecorder) OnClose(reason string) {
	select {
	case r.ch <- reason:
	default:
	}
}

// startFakeDAPAdapter listens on an ephemeral port and speaks just
// enough DAP to satisfy the client handshake: every request is
// answered with a success response. Received requests are published
// on the returned channel so tests can assert on the wire payloads.
// The returned stop function closes the client connection, simulating
// a remote adapter going away.
func startFakeDAPAdapter(
	t *testing.T,
) (addr string, requests <-chan dap.RequestMessage, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	var (
		mu   sync.Mutex
		conn net.Conn
	)
	reqCh := make(chan dap.RequestMessage, 16)
	go rdebug.CapturePanicReport(func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		mu.Lock()
		conn = c
		mu.Unlock()
		reader := bufio.NewReader(c)
		for {
			msg, err := dap.ReadProtocolMessage(reader)
			if err != nil {
				_ = c.Close()
				return
			}
			req, ok := msg.(dap.RequestMessage)
			if !ok {
				continue
			}
			select {
			case reqCh <- req:
			default:
			}
			resp := dap.Response{
				ProtocolMessage: dap.ProtocolMessage{
					Seq:  req.GetSeq() + 1000,
					Type: "response",
				},
				Command:    req.GetRequest().Command,
				RequestSeq: req.GetSeq(),
				Success:    true,
			}
			var out dap.Message
			if req.GetRequest().Command == "initialize" {
				out = &dap.InitializeResponse{Response: resp}
			} else {
				out = &resp
			}
			if err := dap.WriteProtocolMessage(c, out); err != nil {
				_ = c.Close()
				return
			}
		}
	})
	return ln.Addr().String(), reqCh, func() {
		mu.Lock()
		defer mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
	}
}

func TestCreateSessionDirectConnect(t *testing.T) {
	t.Parallel()

	t.Run("dials the listening adapter and initializes", func(t *testing.T) {
		t.Parallel()
		addr, _, stop := startFakeDAPAdapter(t)
		defer stop()

		uri, err := workspaceapi.ParseURI("file:///tmp/test")
		require.NoError(t, err)
		m := New(uri, nil, nil, Config{
			InitializeTimeout: 5 * time.Second,
			Adapters: map[string]AdapterConfig{
				"python": {
					Command:   []string{"connect://" + addr},
					AdapterID: "debugpy",
				},
			},
		})
		t.Cleanup(func() { _ = m.Close() })

		closed := closeRecorder{ch: make(chan string, 1)}
		sid, _, err := m.CreateSession(context.Background(), "python",
			debugapi.ClientCapabilities{}, closed)
		require.NoError(t, err)
		require.NotEmpty(t, sid)

		// The remote adapter dropping the connection must end the
		// session cleanly ("terminated"), not trigger a respawn.
		stop()
		select {
		case reason := <-closed.ch:
			assert.Equal(t, "terminated", reason)
		case <-time.After(5 * time.Second):
			t.Fatal("OnClose not called after remote adapter closed the connection")
		}
		require.Eventually(t, func() bool {
			_, err := m.Threads(context.Background(), sid)
			return errors.Is(err, debugapi.ErrSessionNotFound)
		}, 5*time.Second, 20*time.Millisecond,
			"session not removed after remote adapter closed the connection")
	})

	t.Run("invalid connect address errors", func(t *testing.T) {
		t.Parallel()
		uri, err := workspaceapi.ParseURI("file:///tmp/test")
		require.NoError(t, err)
		m := New(uri, nil, nil, Config{
			InitializeTimeout: time.Second,
			Adapters: map[string]AdapterConfig{
				"python": {Command: []string{"connect://"}},
			},
		})
		t.Cleanup(func() { _ = m.Close() })

		_, _, err = m.CreateSession(context.Background(), "python",
			debugapi.ClientCapabilities{}, fakeSubscriber{})
		require.ErrorContains(t, err, "connect address")
	})
}

func TestCreateSessionConnect(t *testing.T) {
	t.Parallel()

	newManager := func(t *testing.T) *Manager {
		t.Helper()
		uri, err := workspaceapi.ParseURI("file:///tmp/test")
		require.NoError(t, err)
		m := New(uri, nil, nil, Config{
			InitializeTimeout: 5 * time.Second,
			Adapters: map[string]AdapterConfig{
				"python": {
					Command:   []string{"python", "-m", "debugpy.adapter"},
					AdapterID: "debugpy",
					AttachArgs: map[string]string{
						"request": "attach",
						"type":    "python",
					},
				},
			},
		})
		t.Cleanup(func() { _ = m.Close() })
		return m
	}

	t.Run("dials endpoint keeping adapter id and template", func(t *testing.T) {
		t.Parallel()
		addr, requests, stop := startFakeDAPAdapter(t)
		defer stop()

		m := newManager(t)
		sid, _, err := m.CreateSessionConnect(context.Background(), "python",
			addr, debugapi.ClientCapabilities{}, fakeSubscriber{})
		require.NoError(t, err)
		require.NotEmpty(t, sid)

		init, ok := (<-requests).(*dap.InitializeRequest)
		require.True(t, ok, "first request must be initialize")
		assert.Equal(t, "debugpy", init.Arguments.AdapterID)

		require.NoError(t, m.Attach(context.Background(), sid,
			debugapi.AttachRequestArguments{Program: "main.py"}))
		attach, ok := (<-requests).(*dap.AttachRequest)
		require.True(t, ok, "second request must be attach")
		var args map[string]any
		require.NoError(t, json.Unmarshal(attach.Arguments, &args))
		assert.Equal(t, map[string]any{
			"request": "attach",
			"type":    "python",
			"program": "main.py",
		}, args)
	})

	t.Run("unknown language errors", func(t *testing.T) {
		t.Parallel()
		m := newManager(t)
		_, _, err := m.CreateSessionConnect(context.Background(), "ruby",
			"127.0.0.1:5678", debugapi.ClientCapabilities{}, fakeSubscriber{})
		assert.ErrorIs(t, err, debugapi.ErrNoAdapterConfigured)
	})

	t.Run("malformed address errors", func(t *testing.T) {
		t.Parallel()
		m := newManager(t)
		_, _, err := m.CreateSessionConnect(context.Background(), "python",
			"nohostport", debugapi.ClientCapabilities{}, fakeSubscriber{})
		require.ErrorContains(t, err, "connect address")
	})
}

func TestSubstituteAddr(t *testing.T) {
	t.Parallel()
	t.Run("addr placeholder", func(t *testing.T) {
		t.Parallel()
		got := substituteAddr([]string{"dap", "--listen={addr}"}, "127.0.0.1:5555")
		assert.Equal(t, []string{"dap", "--listen=127.0.0.1:5555"}, got)
	})
	t.Run("host and port placeholders", func(t *testing.T) {
		t.Parallel()
		got := substituteAddr(
			[]string{"-m", "debugpy.adapter", "--host", "{host}", "--port", "{port}"},
			"127.0.0.1:5555")
		assert.Equal(t,
			[]string{"-m", "debugpy.adapter", "--host", "127.0.0.1", "--port", "5555"},
			got)
	})
}

func TestDialWithRetryUsesContextDeadline(t *testing.T) {
	t.Parallel()
	addr, err := findFreeAddr()
	require.NoError(t, err)

	listenErr := make(chan error, 1)
	go rdebug.CapturePanicReport(func() {
		time.Sleep(300 * time.Millisecond)
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			listenErr <- err
			return
		}
		defer func() { _ = listener.Close() }()
		if tcp, ok := listener.(*net.TCPListener); ok {
			_ = tcp.SetDeadline(time.Now().Add(time.Second))
		}
		conn, err := listener.Accept()
		if err != nil {
			listenErr <- err
			return
		}
		_ = conn.Close()
		listenErr <- nil
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	conn, err := dialWithRetry(ctx, addr, 5*time.Millisecond, make(chan error))
	require.NoError(t, err)
	_ = conn.Close()
	require.NoError(t, <-listenErr)
}

func TestDialWithRetryStopsWhenAdapterExits(t *testing.T) {
	t.Parallel()
	processExited := make(chan error, 1)
	processExited <- errors.New("adapter crashed")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)

	conn, err := dialWithRetry(ctx, "127.0.0.1:1", 5*time.Millisecond, processExited)
	require.Nil(t, conn)
	require.ErrorContains(t, err, "debug adapter exited before accepting connections")
	require.ErrorContains(t, err, "adapter crashed")
}

func TestDialWithRetryReportsLastDialErrorOnTimeout(t *testing.T) {
	t.Parallel()
	addr, err := findFreeAddr()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	t.Cleanup(cancel)

	conn, err := dialWithRetry(ctx, addr, 5*time.Millisecond, make(chan error))
	require.Nil(t, conn)
	require.ErrorContains(t, err, "dial "+addr+" before timeout")
}

// captureRequestArgs injects a session whose cfg carries the given
// launch/attach templates, invokes fn (Launch or Attach), reads the
// single DAP request it writes over the wire, and returns its parsed
// Arguments object together with the request command.
func captureRequestArgs(
	t *testing.T, cfg debugConfig,
	fn func(m *Manager, sessionID string) error,
) (command string, args map[string]any) {
	t.Helper()
	m := newTestManager(t)
	t.Cleanup(func() { _ = m.Close() })

	ideEnd, adapterEnd := net.Pipe()
	t.Cleanup(func() { _ = ideEnd.Close(); _ = adapterEnd.Close() })

	srv := newDebugServer(m.ctx, cfg, "/bin/test", nil, m.rootURI,
		debugapi.ClientCapabilities{}, fakeSubscriber{})
	srv.conn = ideEnd
	srv.alive = true

	const sessionID = "capture-session"
	m.mu.Lock()
	m.sessions[sessionID] = srv
	m.mu.Unlock()

	errCh := make(chan error, 1)
	go func() { errCh <- fn(m, sessionID) }()

	reader := bufio.NewReader(adapterEnd)
	msg, err := dap.ReadProtocolMessage(reader)
	require.NoError(t, err)
	require.NoError(t, <-errCh)

	switch req := msg.(type) {
	case *dap.LaunchRequest:
		require.NoError(t, json.Unmarshal(req.Arguments, &args))
		return req.Command, args
	case *dap.AttachRequest:
		require.NoError(t, json.Unmarshal(req.Arguments, &args))
		return req.Command, args
	default:
		t.Fatalf("unexpected request type: %T", msg)
		return "", nil
	}
}

func TestLaunchArgs(t *testing.T) {
	t.Parallel()
	launchReq := func(args debugapi.LaunchRequestArguments) func(*Manager, string) error {
		return func(m *Manager, sessionID string) error {
			return m.Launch(context.Background(), sessionID, args)
		}
	}

	t.Run("delve template from config", func(t *testing.T) {
		t.Parallel()
		cfg := debugConfig{
			langID:  "go",
			command: "dlv",
			launchArgs: map[string]string{
				"mode":       "debug",
				"outputMode": "remote",
			},
		}
		cmd, args := captureRequestArgs(t, cfg,
			launchReq(debugapi.LaunchRequestArguments{
				Program:     "/tmp/app",
				Args:        []string{"-v"},
				Cwd:         "/tmp",
				Env:         map[string]string{"K": "V"},
				StopOnEntry: true,
			}))
		assert.Equal(t, "launch", cmd)
		assert.Equal(t, map[string]any{
			"mode":        "debug",
			"outputMode":  "remote",
			"program":     "/tmp/app",
			"cwd":         "/tmp",
			"stopOnEntry": true,
			"noDebug":     false,
			"args":        []any{"-v"},
			"env":         map[string]any{"K": "V"},
		}, args)
	})

	t.Run("no template sends only typed overlays", func(t *testing.T) {
		t.Parallel()
		cmd, args := captureRequestArgs(t, debugConfig{langID: "go", command: "dlv"},
			launchReq(debugapi.LaunchRequestArguments{
				Program:     "/tmp/app",
				StopOnEntry: true,
			}))
		assert.Equal(t, "launch", cmd)
		// With no template only the SDK-typed overlays are present;
		// no host-side adapter defaults (mode/outputMode) are injected.
		assert.Equal(t, map[string]any{
			"program":     "/tmp/app",
			"stopOnEntry": true,
			"noDebug":     false,
		}, args)
	})

	t.Run("debugpy template with overlays", func(t *testing.T) {
		t.Parallel()
		cfg := debugConfig{
			langID:  "python",
			command: "python",
			launchArgs: map[string]string{
				"request": "launch",
				"console": "internalConsole",
				"type":    "python",
			},
		}
		cmd, args := captureRequestArgs(t, cfg,
			launchReq(debugapi.LaunchRequestArguments{
				Program:     "/tmp/main.py",
				Args:        []string{"--flag"},
				Cwd:         "/work",
				Env:         map[string]string{"PYTHONPATH": "/x"},
				StopOnEntry: false,
			}))
		assert.Equal(t, "launch", cmd)
		// Template keys plus typed overlays; no Delve-only defaults
		// (mode/outputMode) leak into the debugpy payload.
		assert.Equal(t, map[string]any{
			"request":     "launch",
			"console":     "internalConsole",
			"type":        "python",
			"program":     "/tmp/main.py",
			"cwd":         "/work",
			"stopOnEntry": false,
			"noDebug":     false,
			"args":        []any{"--flag"},
			"env":         map[string]any{"PYTHONPATH": "/x"},
		}, args)
	})

	t.Run("placeholder substitution", func(t *testing.T) {
		t.Parallel()
		cfg := debugConfig{
			langID:     "python",
			command:    "python",
			launchArgs: map[string]string{"program": "{program}"},
		}
		cmd, args := captureRequestArgs(t, cfg,
			launchReq(debugapi.LaunchRequestArguments{Program: "/tmp/main.py"}))
		assert.Equal(t, "launch", cmd)
		// {program} in the template is substituted, then the typed
		// overlay sets the same key to the identical value.
		assert.Equal(t, map[string]any{
			"program":     "/tmp/main.py",
			"stopOnEntry": false,
			"noDebug":     false,
		}, args)
	})
}

func TestAttachArgs(t *testing.T) {
	t.Parallel()
	attachReq := func(args debugapi.AttachRequestArguments) func(*Manager, string) error {
		return func(m *Manager, sessionID string) error {
			return m.Attach(context.Background(), sessionID, args)
		}
	}

	t.Run("delve template from config", func(t *testing.T) {
		t.Parallel()
		cfg := debugConfig{
			langID:     "go",
			command:    "dlv",
			attachArgs: map[string]string{"mode": "local"},
		}
		cmd, args := captureRequestArgs(t, cfg,
			attachReq(debugapi.AttachRequestArguments{PID: 4321, Program: "/tmp/app"}))
		assert.Equal(t, "attach", cmd)
		assert.Equal(t, map[string]any{
			"mode":      "local",
			"processId": float64(4321),
			"program":   "/tmp/app",
		}, args)
	})

	t.Run("no template sends only typed overlays", func(t *testing.T) {
		t.Parallel()
		cmd, args := captureRequestArgs(t, debugConfig{langID: "go", command: "dlv"},
			attachReq(debugapi.AttachRequestArguments{PID: 4321, Program: "/tmp/app"}))
		assert.Equal(t, "attach", cmd)
		// No template: only the SDK-typed overlays, with no host-side
		// adapter defaults (mode) injected.
		assert.Equal(t, map[string]any{
			"processId": float64(4321),
			"program":   "/tmp/app",
		}, args)
	})

	t.Run("debugpy template with overlays", func(t *testing.T) {
		t.Parallel()
		cfg := debugConfig{
			langID:  "python",
			command: "python",
			attachArgs: map[string]string{
				"request": "attach",
				"type":    "python",
			},
		}
		cmd, args := captureRequestArgs(t, cfg,
			attachReq(debugapi.AttachRequestArguments{PID: 99}))
		assert.Equal(t, "attach", cmd)
		// Program is empty so no program overlay is added.
		assert.Equal(t, map[string]any{
			"request":   "attach",
			"type":      "python",
			"processId": float64(99),
		}, args)
	})

	t.Run("dotted keys nest into objects", func(t *testing.T) {
		t.Parallel()
		// debugpy attach requires a nested connect object; the flat
		// template expresses it via dotted keys.
		cfg := debugConfig{
			langID:  "python",
			command: "python",
			attachArgs: map[string]string{
				"request":      "attach",
				"type":         "python",
				"connect.host": "127.0.0.1",
				"connect.port": "5688",
			},
		}
		cmd, args := captureRequestArgs(t, cfg,
			attachReq(debugapi.AttachRequestArguments{}))
		assert.Equal(t, "attach", cmd)
		assert.Equal(t, map[string]any{
			"request": "attach",
			"type":    "python",
			"connect": map[string]any{
				"host": "127.0.0.1",
				// debugpy rejects a string port: template values that
				// are integers must be sent as JSON numbers.
				"port": float64(5688),
			},
		}, args)
	})

	t.Run("integer and boolean template values are sent typed", func(t *testing.T) {
		t.Parallel()
		// debugpy validates e.g. listen.port with a strict int check
		// and lldb-dap expects stopOnEntry as a bool, so template
		// values that parse as integers or booleans must not be sent
		// as JSON strings.
		cfg := debugConfig{
			langID:  "python",
			command: "python",
			attachArgs: map[string]string{
				"request":     "attach",
				"type":        "python",
				"listen.host": "127.0.0.1",
				"listen.port": "5678",
				"subProcess":  "true",
				"pathToken":   "false-positive",
			},
		}
		cmd, args := captureRequestArgs(t, cfg,
			attachReq(debugapi.AttachRequestArguments{}))
		assert.Equal(t, "attach", cmd)
		assert.Equal(t, map[string]any{
			"request": "attach",
			"type":    "python",
			"listen": map[string]any{
				"host": "127.0.0.1",
				"port": float64(5678),
			},
			"subProcess": true,
			"pathToken":  "false-positive",
		}, args)
	})
}
