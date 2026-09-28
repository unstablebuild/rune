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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/idelsp/jsonrpc2"
	"unstable.build/rune/internal/workspace/processctx"
)

// server is the routing-facing contract the Manager uses to talk
// to a language backend. A backend is either a single langServer
// or a multiLangServer that fans calls out to several langServers
// sharing one language id (e.g. ty + ruff for python). call and
// notify pick the appropriate child by LSP method; the remaining
// methods drive lifecycle and report state.
type server interface {
	io.Closer
	call(ctx context.Context, method string, params, result any) error
	notify(ctx context.Context, method string, params any) error
	// pullDiagnostics issues a textDocument/diagnostic pull. A
	// single-server implementation performs one call; a multi-server
	// implementation fans out to every child and merges the reports so
	// findings from all backends (e.g. ty type errors and ruff lint)
	// are returned together rather than only the default child's.
	pullDiagnostics(ctx context.Context, params semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error)
	// supportsPullDiagnostics reports whether both halves of the
	// pull-diagnostics handshake are in place: the client advertised
	// textDocument.diagnostic and the server answered with a
	// diagnosticProvider capability.
	supportsPullDiagnostics() bool
	initialize(ctx context.Context) (semanticapi.InitializeResult, error)
	stop(ctx context.Context) error
	start(ctx context.Context) error
	config() langConfig
	key() serverKey
	name() string
	initResult() semanticapi.InitializeResult
	isAlive() bool
}

var _ server = (*langServer)(nil)

type langServer struct {
	mu         sync.Mutex
	ctx        context.Context
	cancel     func()
	stopCalled bool
	params     semanticapi.InitializeParams
	cfg        langConfig
	serverName string
	binPath    string
	pid        workspaceapi.Pid
	watcher    chan error
	rootURI    string
	executor   schemeapi.Executor
	handler    jsonrpc2.Handler
	fileConn   func(*os.File) (net.Conn, error)
	lspFile    *os.File
	stdin      net.Conn
	stdout     net.Conn
	conn       *jsonrpc2.Connection
	alive      bool
	log        *slog.Logger
	init       semanticapi.InitializeResult
	pullDiags  bool
}

// pullDiagnosticsSupported reports whether a server can serve
// textDocument/diagnostic pulls for a client with the given raw
// capabilities. Both halves are required: servers only advertise
// diagnosticProvider when the client declared textDocument.diagnostic,
// and a client that never declared it must not issue pulls.
func pullDiagnosticsSupported(
	clientCaps json.RawMessage, res semanticapi.InitializeResult,
) bool {
	if res.Capabilities.DiagnosticProvider == nil || len(clientCaps) == 0 {
		return false
	}
	var parsed struct {
		TextDocument struct {
			Diagnostic json.RawMessage `json:"diagnostic"`
		} `json:"textDocument"`
	}
	if err := json.Unmarshal(clientCaps, &parsed); err != nil {
		return false
	}
	return len(parsed.TextDocument.Diagnostic) > 0
}

// pipeCloser wraps read and write pipes to close them together.
type pipeCloser struct {
	r net.Conn
	w net.Conn
}

func (pc pipeCloser) Close() error {
	slog.Debug("closing pipes")
	_ = pc.w.Close()
	return pc.r.Close()
}

// deadlineWriter wraps a jsonrpc2.Writer to apply a per-write deadline
// derived from the call context onto the underlying net.Conn. This
// prevents Write from blocking indefinitely if the kernel write buffer
// fills (e.g. peer LSP stalled). Because jsonrpc2.Connection serializes
// writes through a 1-buffered channel, the deadline pokes here cannot
// overlap with another writer's deadline. Crucially, only the writer
// FD is touched — the reader FD is never given a per-RPC deadline,
// so a slow caller cannot kill the long-lived readIncoming goroutine.
type deadlineWriter struct {
	inner jsonrpc2.Writer
	conn  net.Conn
}

func (w *deadlineWriter) Write(ctx context.Context, msg jsonrpc2.Message) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return w.inner.Write(ctx, msg)
	}
	if err := w.conn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	defer func() { _ = w.conn.SetWriteDeadline(time.Time{}) }()
	err := w.inner.Write(ctx, msg)
	if errors.Is(err, os.ErrDeadlineExceeded) {
		// The socket deadline is the context's, but the context's own
		// timer can lag it under load. jsonrpc2 attributes a failed write
		// to the context only when ctx.Err() is already set; otherwise it
		// declares the writer broken and closes the connection, so wait
		// for the timer before reporting the timeout.
		<-ctx.Done()
	}
	return err
}

func newLangServer(
	ctx context.Context,
	cfg langConfig,
	binPath string,
	executor schemeapi.Executor,
	rootURI string,
	handler jsonrpc2.Handler,
	params semanticapi.InitializeParams,
) *langServer {
	ctx, cancel := context.WithCancel(ctx)
	return &langServer{
		params:     params,
		ctx:        ctx,
		cancel:     cancel,
		cfg:        cfg,
		serverName: cfg.id,
		binPath:    binPath,
		executor:   executor,
		rootURI:    rootURI,
		handler:    handler,
		fileConn:   net.FileConn,
		log: slog.With("struct", "idelsp.langServer",
			"language", cfg.id, "workspace", rootURI),
	}
}

func (s *langServer) start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fds, err := lspSocketpair()
	if err != nil {
		return fmt.Errorf("create socket pair: %v", err)
	}
	// One os.File per fd to avoid GC finalizer races that can
	// close reused file descriptors in other goroutines.
	lspFile := os.NewFile(uintptr(fds[1]), "file+net lsp")
	ideFile := os.NewFile(uintptr(fds[0]), "file+net ide")

	watchCh := make(chan error, 1)
	watcher := workspaceapi.ChanProcessWatcher(watchCh)

	cmd := workspaceapi.Cmd{
		Path:    s.binPath,
		Dir:     uriToPath(s.rootURI),
		Args:    s.cfg.args,
		Env:     slices.Clone(s.cfg.env),
		Stdin:   lspFile,
		Stdout:  lspFile,
		Watcher: watcher,
		// Language servers spawn helpers of their own (gopls runs a
		// telemetry child), and some are reached through a launcher
		// that execs the server as a grandchild. Heading its own
		// process group is what lets stopping the server reach them.
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
	}

	// Do not use ctx for lifecycle cancellation: it is scoped to the initial
	// protocol exchange. Copy logical process metadata from ctx into the server
	// lifecycle context so LSP server processes remain associated with the
	// extension that requested them.
	lifecycleContext := processctx.DeriveCommandContext(s.ctx, ctx)

	s.log.Info("starting server", "path", cmd.Path, "args", cmd.Args)
	pid, err := s.executor.StartCommand(lifecycleContext, cmd)
	if err != nil {
		_ = lspFile.Close()
		_ = ideFile.Close()
		return fmt.Errorf(
			"start %s: %w", s.cfg.command, err,
		)
	}
	s.pid = pid
	s.watcher = watchCh
	// Keep the LSP end of the socketpair open for the server's lifetime.
	// The local file scheme dups the fd into the child, so closing our copy
	// would be harmless there; but the ssh scheme streams through this
	// *os.File by reference (x/crypto/ssh io.Copy goroutines), so an early
	// close tears down the transport and the first write breaks the pipe.
	// Close it in Close() instead, alongside the IDE-side pipes.
	s.lspFile = lspFile

	stdout, err := s.fileConn(ideFile)
	if err != nil {
		_ = ideFile.Close()
		_ = s.Close()
		return fmt.Errorf("new stdout file conn: %w", err)
	}
	stdin, err := s.fileConn(ideFile)
	if err != nil {
		_ = stdout.Close()
		_ = ideFile.Close()
		_ = s.Close()
		return fmt.Errorf("new stdin file conn: %w", err)
	}
	// FileConn duped the fd; close our copy.
	_ = ideFile.Close()
	framer := jsonrpc2.HeaderFramer()
	closer := pipeCloser{r: stdout, w: stdin}
	s.conn = jsonrpc2.NewConnection(lifecycleContext, jsonrpc2.ConnectionConfig{
		Reader: framer.Reader(stdout),
		Writer: &deadlineWriter{inner: framer.Writer(stdin), conn: stdin},
		Closer: closer,
		Bind:   func(*jsonrpc2.Connection) jsonrpc2.Handler { return s.handler },
	})
	s.alive = true
	s.stdin = stdin
	s.stdout = stdout

	resp, err := s.initialize(ctx)
	if err != nil {
		_ = s.Close()
		return fmt.Errorf("initialize %s: %w", s.cfg.command, err)
	}
	s.init = resp
	s.pullDiags = pullDiagnosticsSupported(s.params.Capabilities, resp)
	return nil
}

func (s *langServer) Close() error {
	if s.cancel != nil {
		s.cancel()
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.stdout != nil {
		_ = s.stdout.Close()
	}
	var err error
	if s.conn != nil {
		err = s.conn.Close()
	}
	if s.lspFile != nil {
		_ = s.lspFile.Close()
	}
	return err
}

func (s *langServer) initialize(ctx context.Context) (
	response semanticapi.InitializeResult, err error,
) {

	s.log.Debug("rpc call", "method", "initialize")
	err = s.conn.Call(ctx, "initialize", s.params).Await(ctx, &response)
	if err != nil {
		return
	}

	err = s.conn.Notify(ctx, "initialized", struct{}{})
	return
}

func (s *langServer) stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopCalled = true
	if !s.alive {
		s.mu.Unlock()
		return nil
	}
	s.alive = false
	s.mu.Unlock()

	var raw json.RawMessage
	err := s.conn.Call(ctx, "shutdown", nil).Await(ctx, &raw)
	if err != nil {
		return fmt.Errorf("call shutdown: %w", err)
	}
	err = s.conn.Notify(ctx, "exit", nil)
	if err != nil {
		return fmt.Errorf("notify exit: %w", err)
	}
	return nil
}

func (s *langServer) call(
	ctx context.Context, method string,
	params, result any,
) error {
	s.mu.Lock()
	if !s.alive {
		s.mu.Unlock()
		s.log.Warn("rpc call", "method", method, "error", "no server")
		return ErrNoServer
	}
	conn := s.conn
	s.mu.Unlock()
	s.log.Debug("rpc call", "method", method, "step", "attempt")

	call := conn.Call(ctx, method, params)
	err := call.Await(ctx, result)
	if err != nil {
		s.log.Warn("rpc call", "method", method, "error", err)
	} else {
		s.log.Debug("rpc call", "method", method, "step", "success")
	}
	return err
}

// pullDiagnostics issues a single textDocument/diagnostic pull against
// this server.
func (s *langServer) pullDiagnostics(
	ctx context.Context, params semanticapi.DocumentDiagnosticParams,
) (semanticapi.DocumentDiagnosticReport, error) {
	var report semanticapi.DocumentDiagnosticReport
	err := s.call(ctx, "textDocument/diagnostic", params, &report)
	return report, err
}

func (s *langServer) notify(
	ctx context.Context, method string, params any,
) error {
	s.mu.Lock()
	if !s.alive {
		s.mu.Unlock()
		s.log.Warn("rpc notify", "method", method, "error", "no server")
		return ErrNoServer
	}
	conn := s.conn
	s.mu.Unlock()

	s.log.Debug("rpc notify", "method", method, "step", "attempt")
	err := conn.Notify(ctx, method, params)
	if err != nil {
		s.log.Warn("rpc notify", "method", method, "step", "error", "error", err)
	} else {
		s.log.Debug("rpc notify", "method", method, "step", "success")
	}
	return err
}

func (s *langServer) config() langConfig {
	return s.cfg
}

func (s *langServer) key() serverKey {
	return serverKey{languageID: s.cfg.id, rootURI: s.rootURI}
}

// name returns the child server's name, derived from its command's base
// name. It identifies a specific backing server when a language is
// served by several (see the alternate-commands feature) and is the
// value matched against ExecuteRequestParams.ServerID.
func (s *langServer) name() string {
	return s.serverName
}

func (s *langServer) initResult() semanticapi.InitializeResult {
	return s.init
}

func (s *langServer) supportsPullDiagnostics() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pullDiags
}

func (s *langServer) isAlive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.alive
}
