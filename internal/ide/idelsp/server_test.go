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

package idelsp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/idelsp/jsonrpc2"
	"unstable.build/rune/internal/workspace/processctx"
)

func TestLSPSocketpairIsCloseOnExec(t *testing.T) {
	fds, err := lspSocketpair()
	require.NoError(t, err)
	for _, fd := range fds {
		t.Cleanup(func() { require.NoError(t, syscall.Close(fd)) })
		flags, _, errno := syscall.Syscall(
			syscall.SYS_FCNTL, uintptr(fd), uintptr(syscall.F_GETFD), 0,
		)
		require.Zero(t, errno)
		assert.NotZero(t, flags&syscall.FD_CLOEXEC,
			"socket fd %d would survive exec and keep the LSP transport alive", fd)
	}
}

func TestLangServerStartCarriesProcessContext(t *testing.T) {
	startErr := errors.New("start failed")
	exec := &recordingStartExecutor{err: startErr}
	srv := newLangServer(
		context.Background(),
		langConfig{
			id: "go", command: "gopls", args: []string{"serve"},
			env: []string{"GOPLS_LOG_LEVEL=debug"},
		},
		"gopls",
		exec,
		"file:///workspace",
		nil,
		semanticapi.InitializeParams{},
	)

	ctx := processctx.ContextWithExtensionID(context.Background(), "go")
	err := srv.start(ctx)
	require.ErrorIs(t, err, startErr)

	extensionID, ok := processctx.ExtensionIDFromContext(exec.ctx)
	require.True(t, ok)
	assert.Equal(t, "go", extensionID)
	assert.Equal(t, "gopls", exec.cmd.Path)
	assert.Equal(t, []string{"serve"}, exec.cmd.Args)
	assert.Equal(t, []string{"GOPLS_LOG_LEVEL=debug"}, exec.cmd.Env)
}

func TestLangServerStartPreservesNilEnvironment(t *testing.T) {
	startErr := errors.New("start failed")
	exec := &recordingStartExecutor{err: startErr}
	srv := newLangServer(
		context.Background(),
		langConfig{id: "go", command: "gopls", args: []string{"serve"}},
		"gopls", exec, "file:///workspace", nil,
		semanticapi.InitializeParams{},
	)

	err := srv.start(context.Background())
	require.ErrorIs(t, err, startErr)
	assert.Nil(t, exec.cmd.Env)
}

func TestLangServerStartCancelsProcessWhenTransportSetupFails(t *testing.T) {
	exec := &recordingStartExecutor{}
	srv := newLangServer(
		context.Background(),
		langConfig{id: "go", command: "gopls", args: []string{"serve"}},
		"gopls", exec, "file:///workspace", nil,
		semanticapi.InitializeParams{},
	)
	wantErr := errors.New("file conn failed")
	srv.fileConn = func(*os.File) (net.Conn, error) { return nil, wantErr }

	err := srv.start(context.Background())
	require.ErrorIs(t, err, wantErr)
	require.ErrorIs(t, exec.ctx.Err(), context.Canceled,
		"a spawned process must be canceled when transport ownership cannot be established")
}

// TestLangServerCallDoesNotKillReader exercises the regression where a
// short per-RPC deadline applied to the shared reader FD would tear
// down the whole jsonrpc2 connection mid-flight, leaving every
// subsequent Call returning ErrClientClosing.
func TestLangServerCallDoesNotKillReader(t *testing.T) {
	srv, fake, cleanup := newFakeLSPLangServer(t)
	defer cleanup()

	fake.setDelay("slow", 500*time.Millisecond)
	fake.setDelay("verySlow", 5*time.Second)

	var (
		wg       sync.WaitGroup
		fastErr  error
		slowErr  error
		fastResp string
		slowResp string
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		// Slow call needs to outlast the fast call's deadline so we
		// can observe whether the fast call's per-RPC deadline tore
		// down the shared reader mid-flight.
		ctx, cancel := context.WithTimeout(
			context.Background(), 10*time.Second,
		)
		defer cancel()
		slowErr = srv.call(ctx, "slow", nil, &slowResp)
	}()
	go func() {
		defer wg.Done()
		// Stagger so the slow call has issued its request and
		// readIncoming is parked inside ReadString before the fast
		// call's deadline fires.
		time.Sleep(50 * time.Millisecond)
		ctx, cancel := context.WithTimeout(
			context.Background(), 100*time.Millisecond,
		)
		defer cancel()
		fastErr = srv.call(ctx, "verySlow", nil, &fastResp)
	}()

	wg.Wait()

	// The fast call must have hit its context deadline.
	require.Error(t, fastErr)
	assert.True(t,
		errors.Is(fastErr, context.DeadlineExceeded),
		"fast call err: %v", fastErr,
	)

	// The slow call must complete normally — the connection must
	// not have been torn down by the fast call's deadline.
	require.NoError(t, slowErr, "slow call err: %v", slowErr)
	assert.Equal(t, "ok", slowResp)

	// A subsequent call must still succeed (i.e. the conn is not in
	// the closing state).
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var followUp string
	require.NoError(t, srv.call(ctx, "ping", nil, &followUp))
	assert.Equal(t, "ok", followUp)
}

// TestLangServerCallWriteDeadlineDoesNotDeadlock locks in the
// deadlineWriter behaviour: if the peer never drains the kernel write
// buffer, a Write must still fail with the context deadline rather
// than blocking forever.
func TestLangServerCallWriteDeadlineDoesNotDeadlock(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	require.NoError(t, err)
	lspFile := os.NewFile(uintptr(fds[1]), "fake-lsp")
	ideFile := os.NewFile(uintptr(fds[0]), "fake-ide")

	// Hold the LSP-side fd open but never read from it. We do not
	// even build a net.Conn for it — just keep the file handle so
	// the kernel does not tear down the socket.
	t.Cleanup(func() { _ = lspFile.Close() })

	stdout, err := net.FileConn(ideFile)
	require.NoError(t, err)
	stdin, err := net.FileConn(ideFile)
	require.NoError(t, err)
	_ = ideFile.Close()
	t.Cleanup(func() {
		_ = stdout.Close()
		_ = stdin.Close()
	})

	framer := jsonrpc2.HeaderFramer()
	srv := &langServer{
		cfg:     langConfig{id: "fake", command: "fake"},
		rootURI: "file:///tmp",
		log:     slog.Default(),
	}
	srv.stdin = stdin
	srv.stdout = stdout
	srv.conn = jsonrpc2.NewConnection(context.Background(), jsonrpc2.ConnectionConfig{
		Reader: framer.Reader(stdout),
		Writer: &deadlineWriter{inner: framer.Writer(stdin), conn: stdin},
		Closer: pipeCloser{r: stdout, w: stdin},
		Bind:   func(*jsonrpc2.Connection) jsonrpc2.Handler { return nil },
	})
	srv.alive = true
	t.Cleanup(func() { _ = srv.Close() })

	// Fill the kernel write buffer so subsequent writes block.
	// We set a short deadline first so this pre-fill itself does
	// not hang the test if the buffer is huge.
	require.NoError(t, stdin.SetWriteDeadline(time.Now().Add(50*time.Millisecond)))
	bigPayload := make([]byte, 1<<22)
	_, _ = stdin.Write(bigPayload) // best effort; expected to time out
	require.NoError(t, stdin.SetWriteDeadline(time.Time{}))

	ctx, cancel := context.WithTimeout(
		context.Background(), 100*time.Millisecond,
	)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		var resp string
		done <- srv.call(ctx, "any", map[string]any{
			"payload": string(make([]byte, 1<<20)),
		}, &resp)
	}()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("call did not return after write deadline expired")
	}
}

// lateTimerContext carries a deadline whose Done channel closes well after
// it: the socket deadline derived from it fires on time while the
// context's own timer, as under a loaded CI runner, runs late.
type lateTimerContext struct {
	context.Context
	deadline time.Time
	done     chan struct{}
}

func newLateTimerContext(deadline time.Time, lag time.Duration) *lateTimerContext {
	c := &lateTimerContext{
		Context: context.Background(), deadline: deadline, done: make(chan struct{}),
	}
	time.AfterFunc(time.Until(deadline)+lag, func() { close(c.done) })
	return c
}

func (c *lateTimerContext) Deadline() (time.Time, bool) { return c.deadline, true }
func (c *lateTimerContext) Done() <-chan struct{}       { return c.done }

func (c *lateTimerContext) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// TestLangServerWriteDeadlineDoesNotBreakConnection covers a write that
// times out on the socket deadline before the context's timer fires. The
// connection must attribute that to the call's deadline; treating it as
// a broken writer closes the transport and restarts the server under a
// caller that merely hit its own timeout.
func TestLangServerWriteDeadlineDoesNotBreakConnection(t *testing.T) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	require.NoError(t, err)
	lspFile := os.NewFile(uintptr(fds[1]), "fake-lsp")
	ideFile := os.NewFile(uintptr(fds[0]), "fake-ide")
	t.Cleanup(func() { _ = lspFile.Close() })

	stdout, err := net.FileConn(ideFile)
	require.NoError(t, err)
	stdin, err := net.FileConn(ideFile)
	require.NoError(t, err)
	_ = ideFile.Close()
	t.Cleanup(func() {
		_ = stdout.Close()
		_ = stdin.Close()
	})

	framer := jsonrpc2.HeaderFramer()
	srv := &langServer{
		cfg:     langConfig{id: "fake", command: "fake"},
		rootURI: "file:///tmp",
		log:     slog.Default(),
	}
	srv.stdin = stdin
	srv.stdout = stdout
	srv.conn = jsonrpc2.NewConnection(context.Background(), jsonrpc2.ConnectionConfig{
		Reader: framer.Reader(stdout),
		Writer: &deadlineWriter{inner: framer.Writer(stdin), conn: stdin},
		Closer: pipeCloser{r: stdout, w: stdin},
		Bind:   func(*jsonrpc2.Connection) jsonrpc2.Handler { return nil },
	})
	srv.alive = true
	t.Cleanup(func() { _ = srv.Close() })

	require.NoError(t, stdin.SetWriteDeadline(time.Now().Add(50*time.Millisecond)))
	_, _ = stdin.Write(make([]byte, 1<<22))
	require.NoError(t, stdin.SetWriteDeadline(time.Time{}))

	late := newLateTimerContext(time.Now().Add(100*time.Millisecond), 300*time.Millisecond)
	done := make(chan error, 1)
	go func() {
		var resp string
		done <- srv.call(late, "any", map[string]any{
			"payload": string(make([]byte, 1<<20)),
		}, &resp)
	}()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("call did not return after write deadline expired")
	}

	// The peer drains what was written; the transport itself is fine.
	go func() { _, _ = io.Copy(io.Discard, lspFile) }()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = srv.conn.Notify(ctx, "ping", nil)
	require.NotErrorIs(t, err, jsonrpc2.ErrClientClosing,
		"a call timing out on its own deadline must not close the connection")
	require.NoError(t, err)
}

// recordingStartExecutor is a schemeapi.Executor stub that records
// the StartCommand args and returns a configurable error.
type recordingStartExecutor struct {
	ctx context.Context
	cmd workspaceapi.Cmd
	err error
}

func (e *recordingStartExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	e.ctx = ctx
	e.cmd = cmd
	return 0, e.err
}

func (e *recordingStartExecutor) Signal(
	workspaceapi.Pid, syscall.Signal,
) error {
	return nil
}

func (e *recordingStartExecutor) Close() error {
	return nil
}

// fakeLSP is an in-process LSP peer that reads framed jsonrpc2
// requests on the LSP side of a socketpair and replies after a
// per-method delay.
type fakeLSP struct {
	conn   net.Conn
	reader jsonrpc2.Reader
	writer jsonrpc2.Writer

	mu     sync.Mutex
	delays map[string]time.Duration
}

func (f *fakeLSP) setDelay(method string, d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delays[method] = d
}

func (f *fakeLSP) delayFor(method string) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.delays[method]
}

func (f *fakeLSP) serve(ctx context.Context) {
	for {
		msg, err := f.reader.Read(ctx)
		if err != nil {
			return
		}
		req, ok := msg.(*jsonrpc2.Request)
		if !ok {
			continue
		}
		if !req.IsCall() {
			continue
		}
		go f.respond(ctx, req)
	}
}

func (f *fakeLSP) respond(ctx context.Context, req *jsonrpc2.Request) {
	delay := f.delayFor(req.Method)
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
	resp, err := jsonrpc2.NewResponse(req.ID, "ok", nil)
	if err != nil {
		return
	}
	_ = f.writer.Write(ctx, resp)
}

// newFakeLSPLangServer builds a *langServer wired to an in-process
// fakeLSP via a socketpair, bypassing process startup. The returned
// cleanup closes both ends.
func newFakeLSPLangServer(t *testing.T) (*langServer, *fakeLSP, func()) {
	t.Helper()

	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	require.NoError(t, err)
	lspFile := os.NewFile(uintptr(fds[1]), "fake-lsp")
	ideFile := os.NewFile(uintptr(fds[0]), "fake-ide")

	lspConn, err := net.FileConn(lspFile)
	require.NoError(t, err)
	_ = lspFile.Close()

	stdout, err := net.FileConn(ideFile)
	require.NoError(t, err)
	stdin, err := net.FileConn(ideFile)
	require.NoError(t, err)
	_ = ideFile.Close()

	framer := jsonrpc2.HeaderFramer()
	fake := &fakeLSP{
		conn:   lspConn,
		reader: framer.Reader(lspConn),
		writer: framer.Writer(lspConn),
		delays: map[string]time.Duration{},
	}
	serveCtx, serveCancel := context.WithCancel(context.Background())
	go fake.serve(serveCtx)

	srv := &langServer{
		ctx:     serveCtx,
		cancel:  serveCancel,
		cfg:     langConfig{id: "fake", command: "fake"},
		rootURI: "file:///tmp",
		log:     slog.Default(),
	}
	srv.stdin = stdin
	srv.stdout = stdout
	srv.conn = jsonrpc2.NewConnection(serveCtx, jsonrpc2.ConnectionConfig{
		Reader: framer.Reader(stdout),
		Writer: &deadlineWriter{inner: framer.Writer(stdin), conn: stdin},
		Closer: pipeCloser{r: stdout, w: stdin},
		Bind:   func(*jsonrpc2.Connection) jsonrpc2.Handler { return nil },
	})
	srv.alive = true

	cleanup := func() {
		serveCancel()
		_ = srv.Close()
		_ = lspConn.Close()
	}
	return srv, fake, cleanup
}

// fakeChildServer is a minimal server used to exercise multiLangServer
// lifecycle ownership without spawning real language-server processes.
type fakeChildServer struct {
	startErr   error
	started    bool
	closeCount int
}

func (f *fakeChildServer) start(context.Context) error {
	if f.startErr != nil {
		// A real langServer.start cleans up after itself on failure, so it
		// is never Closed by the caller. Model that: a failed start leaves
		// started=false and must not receive a later Close.
		return f.startErr
	}
	f.started = true
	return nil
}

func (f *fakeChildServer) Close() error { f.closeCount++; return nil }

func (f *fakeChildServer) call(context.Context, string, any, any) error { return nil }
func (f *fakeChildServer) notify(context.Context, string, any) error    { return nil }
func (f *fakeChildServer) pullDiagnostics(
	context.Context, semanticapi.DocumentDiagnosticParams,
) (semanticapi.DocumentDiagnosticReport, error) {
	return semanticapi.DocumentDiagnosticReport{}, nil
}
func (f *fakeChildServer) initialize(context.Context) (semanticapi.InitializeResult, error) {
	return semanticapi.InitializeResult{}, nil
}
func (f *fakeChildServer) stop(context.Context) error    { return nil }
func (f *fakeChildServer) supportsPullDiagnostics() bool { return false }
func (f *fakeChildServer) config() langConfig            { return langConfig{} }
func (f *fakeChildServer) key() serverKey                { return serverKey{} }
func (f *fakeChildServer) name() string                  { return "fake" }
func (f *fakeChildServer) initResult() semanticapi.InitializeResult {
	return semanticapi.InitializeResult{}
}
func (f *fakeChildServer) isAlive() bool { return f.started }

// TestMultiLangServerStartOwnership pins the lifecycle contract behind the
// remote gopls crash: when one child fails to start, multiLangServer.start
// closes the siblings it already brought up and returns the error, without
// Closing the failed child (langServer.start already self-cleans). The
// caller must therefore NOT Close a multiLangServer whose start failed —
// doing so previously re-Closed a never-started child and nil-deref'd its
// pipes.
func TestMultiLangServerStartOwnership(t *testing.T) {
	t.Parallel()

	t.Run("second child fails: first is closed, failed one is not", func(t *testing.T) {
		t.Parallel()
		first := &fakeChildServer{}
		failed := &fakeChildServer{startErr: errors.New("gopls missing")}
		third := &fakeChildServer{}
		mls := &multiLangServer{children: []server{first, failed, third}}

		err := mls.start(context.Background())
		require.Error(t, err)

		assert.Equal(t, 1, first.closeCount,
			"an already-started child must be closed when a later child fails")
		assert.Equal(t, 0, failed.closeCount,
			"a failed start already self-cleaned; it must not be closed again")
		assert.Equal(t, 0, third.closeCount,
			"a child after the failure never started; it must not be closed")
	})

	t.Run("all succeed: no child is closed", func(t *testing.T) {
		t.Parallel()
		a := &fakeChildServer{}
		b := &fakeChildServer{}
		mls := &multiLangServer{children: []server{a, b}}

		require.NoError(t, mls.start(context.Background()))
		assert.Zero(t, a.closeCount)
		assert.Zero(t, b.closeCount)
	})
}

// retainingExecutor mimics a non-dup'ing executor such as the ssh scheme:
// StartCommand keeps the *os.File it was handed (cmd.Stdin/Stdout) and
// serves a fake LSP that answers "initialize" by reading/writing that file
// from a background goroutine. Unlike os/exec (which dups the fd into the
// child), it never dups, so if the caller closes its copy of the fd the
// transport is torn down.
type retainingExecutor struct {
	t      *testing.T
	cancel context.CancelFunc
	file   *os.File
}

func (e *retainingExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	f, ok := cmd.Stdout.(*os.File)
	require.True(e.t, ok, "expected *os.File stdout from langServer.start")
	// Read/write the *os.File directly (no net.FileConn, which would dup
	// the fd and mask the early-close bug). This mirrors x/crypto/ssh,
	// which streams through the io.Reader/io.Writer by reference.
	e.file = f
	framer := jsonrpc2.HeaderFramer()
	serveCtx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	reader := framer.Reader(f)
	writer := framer.Writer(f)
	go func() {
		for {
			msg, err := reader.Read(serveCtx)
			if err != nil {
				return
			}
			req, ok := msg.(*jsonrpc2.Request)
			if !ok || !req.IsCall() {
				continue
			}
			// Reply with a valid (empty) InitializeResult.
			resp, err := jsonrpc2.NewResponse(req.ID, semanticapi.InitializeResult{}, nil)
			if err != nil {
				return
			}
			_ = writer.Write(serveCtx, resp)
		}
	}()
	return 1, nil
}

func (e *retainingExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (e *retainingExecutor) Close() error {
	if e.cancel != nil {
		e.cancel()
	}
	if e.file != nil {
		_ = e.file.Close()
	}
	return nil
}

// TestLangServerStartKeepsFDForNonDupExecutor reproduces the remote gopls
// "write unix : write: broken pipe" failure. langServer.start hands the LSP
// end of a socketpair to StartCommand and then closed its own copy of that
// fd, assuming the executor duped it (true for local os/exec, false for the
// ssh scheme, which streams through the *os.File by reference). Closing it
// early tears down the transport so the first initialize write breaks. The
// server must keep the fd open until Close.
func TestLangServerStartKeepsFDForNonDupExecutor(t *testing.T) {
	t.Parallel()

	exec := &retainingExecutor{t: t}
	t.Cleanup(func() { _ = exec.Close() })

	srv := newLangServer(context.Background(),
		langConfig{id: "fake", command: "fake"},
		"fake", exec, "file:///tmp", nil, semanticapi.InitializeParams{})
	t.Cleanup(func() { _ = srv.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, srv.start(ctx),
		"initialize must succeed; a broken pipe here means start closed the "+
			"LSP fd the executor still streams through")
}
