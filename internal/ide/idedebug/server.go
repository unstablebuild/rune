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
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/go-dap"
	"github.com/unstablebuild/rune-go-sdk/api/debugapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/procattr"
	"unstable.build/rune/internal/workspace/processctx"
)

// errNoServer is returned by a *debugServer when its connection
// has been torn down or it has not yet been started.
var errNoServer = errors.New("no debug server")

const defaultDialRetryDelay = 100 * time.Millisecond

// connectScheme marks an adapter command that names an already
// listening adapter endpoint instead of an executable to spawn. This
// is the debugpy attach-by-connect topology: the debuggee runs under
// `python -m debugpy --listen host:port`, which spawns the adapter on
// the debuggee side, and the client is expected to dial it directly.
const connectScheme = "connect://"

type debugServer struct {
	mu         sync.Mutex
	writeMu    sync.Mutex
	wg         sync.WaitGroup
	closeOnce  sync.Once
	closeSub   sync.Once
	ctx        context.Context
	cancel     func()
	stopCalled bool
	cfg        debugConfig
	binPath    string
	rootURI    string
	pid        workspaceapi.Pid
	watcher    chan error
	executor   schemeapi.Executor
	conn       net.Conn
	reader     *bufio.Reader
	seq        atomic.Int64
	pending    map[int]chan dap.Message
	pendingMu  sync.Mutex
	client     debugapi.ClientCapabilities
	eventSub   debugapi.EventSubscriber
	alive      bool
	log        *slog.Logger
	caps       *dap.Capabilities
	// dialRetryDelay is the cadence between connection attempts
	// while waiting for the adapter to bind its port. Tests shrink
	// it to keep startup-timing assertions fast.
	dialRetryDelay time.Duration
	// launchErr stores the error from a failed DAP launch or
	// attach response. These requests use writeRequest
	// (fire-and-forget), so the response has no pending
	// channel. readLoop captures the error here so that
	// ConfigurationDone can surface it.
	launchErr error
	// stderr accumulates the text of "output" DAP events with
	// category "stderr" so we can surface them together with a
	// failed launch/attach response (delve reports build errors
	// via output events rather than in the response body).
	stderr []byte
}

func newDebugServer(
	ctx context.Context,
	cfg debugConfig,
	binPath string,
	executor schemeapi.Executor,
	rootURI string,
	client debugapi.ClientCapabilities,
	eventSub debugapi.EventSubscriber,
) *debugServer {
	ctx, cancel := context.WithCancel(ctx)
	return &debugServer{
		ctx:            ctx,
		cancel:         cancel,
		cfg:            cfg,
		binPath:        binPath,
		executor:       executor,
		rootURI:        rootURI,
		client:         client,
		eventSub:       eventSub,
		pending:        make(map[int]chan dap.Message),
		dialRetryDelay: defaultDialRetryDelay,
		log:            slog.With("struct", "idedebug.debugServer", "lang", cfg.langID, "uri", rootURI),
	}
}

// notifyClose invokes the subscriber's OnClose exactly once.
func (s *debugServer) notifyClose(reason string) {
	if s.eventSub == nil {
		return
	}
	s.closeSub.Do(func() {
		s.eventSub.OnClose(reason)
	})
}

func (s *debugServer) start(ctx context.Context) error {
	if addr, ok := strings.CutPrefix(s.cfg.command, connectScheme); ok {
		return s.startConnect(ctx, addr)
	}
	// Pick a free TCP port for the adapter to listen on.
	addr, err := findFreeAddr()
	if err != nil {
		return fmt.Errorf("find free addr: %w", err)
	}

	args := substituteAddr(s.cfg.args, addr)

	watchCh := make(chan error, 1)
	watcher := workspaceapi.ChanProcessWatcher(watchCh)

	cmd := workspaceapi.Cmd{
		Path:    s.binPath,
		Args:    args,
		Dir:     strings.TrimPrefix(s.rootURI, "file://"),
		Watcher: watcher,
		// Adapters are reached through launchers like `uvx` that exec
		// the adapter as a grandchild, and they spawn the debuggee in
		// turn. Heading its own process group is what lets ending the
		// session tear the whole tree down.
		SysProcAttr: procattr.NewGroup(),
	}

	// Do not use ctx for lifecycle cancellation: it is scoped to the initial
	// protocol exchange and may be a testing.T context in tests. Copy only the
	// logical process metadata from ctx into the server lifecycle context so the
	// debug adapter lives until Manager.Close/debugServer.stop cancels s.ctx.
	lifecycleCtx := processctx.DeriveCommandContext(s.ctx, ctx)

	s.log.Info("starting server", "path", cmd.Path, "args", cmd.Args)
	pid, err := s.executor.StartCommand(lifecycleCtx, cmd)
	if err != nil {
		return fmt.Errorf("start %s: %w", s.cfg.command, err)
	}

	// Connect to the debug adapter as a client.
	conn, err := dialWithRetry(ctx, addr, s.dialRetryDelay, watchCh)
	if err != nil {
		s.cancel() // kill the spawned process
		return fmt.Errorf("connect to %s: %w", addr, err)
	}

	s.mu.Lock()
	s.pid = pid
	s.watcher = watchCh
	s.conn = conn
	s.reader = bufio.NewReader(conn)
	s.alive = true
	s.mu.Unlock()

	s.wg.Add(1)
	go debug.CapturePanicReport(func() {
		s.readLoop()
	})

	// initialize is called without s.mu held because it
	// calls sendRequest which also acquires s.mu.
	caps, err := s.initialize(ctx)
	if err != nil {
		s.closeConn()
		s.cancel() // kill the spawned process
		return fmt.Errorf("initialize %s: %w", s.cfg.command, err)
	}

	s.mu.Lock()
	s.caps = caps
	s.mu.Unlock()
	return nil
}

// startConnect attaches to an adapter that is already listening at
// addr instead of spawning one. The watcher channel is fed a nil
// (clean-exit) error when the connection drops so watchSession ends
// the session instead of retrying a respawn: the remote adapter's
// lifecycle is owned by whoever started it, not by this client.
func (s *debugServer) startConnect(ctx context.Context, addr string) error {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return fmt.Errorf("adapter connect address %q: %w", addr, err)
	}

	s.log.Info("connecting to listening adapter", "addr", addr)
	conn, err := dialWithRetry(ctx, addr, s.dialRetryDelay, nil)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", addr, err)
	}

	watchCh := make(chan error, 1)
	s.mu.Lock()
	s.watcher = watchCh
	s.conn = conn
	s.reader = bufio.NewReader(conn)
	s.alive = true
	s.mu.Unlock()

	s.wg.Add(1)
	go debug.CapturePanicReport(func() {
		s.readLoop()
		watchCh <- nil
	})

	caps, err := s.initialize(ctx)
	if err != nil {
		s.closeConn()
		return fmt.Errorf("initialize %s: %w", s.cfg.command, err)
	}

	s.mu.Lock()
	s.caps = caps
	s.mu.Unlock()
	return nil
}

// findFreeAddr binds to an ephemeral port to discover a
// free address, then releases it. There is a small TOCTOU
// window before the adapter binds to the same port; in
// practice this is negligible and dialWithRetry will surface
// a clear error if it occurs.
func findFreeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr, nil
}

// dialWithRetry connects to the debug adapter, retrying on the retry
// cadence until ctx is cancelled or the adapter process exits. The
// caller owns the deadline via ctx (idedebug applies
// Config.InitializeTimeout), so dialWithRetry imposes no deadline of
// its own.
func dialWithRetry(
	ctx context.Context,
	addr string,
	retryDelay time.Duration,
	processExited <-chan error,
) (net.Conn, error) {
	dialer := net.Dialer{Timeout: retryDelay}
	ticker := time.NewTicker(retryDelay)
	defer ticker.Stop()

	var lastErr error
	for {
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			return conn, nil
		}
		lastErr = err

		select {
		case <-ctx.Done():
			return nil, dialTimeoutError(addr, lastErr, ctx.Err())
		case err := <-processExited:
			return nil, adapterExitError(err)
		case <-ticker.C:
		}
	}
}

func dialTimeoutError(addr string, lastErr, ctxErr error) error {
	if lastErr == nil {
		return ctxErr
	}
	return fmt.Errorf("dial %s before timeout: %w", addr, lastErr)
}

func adapterExitError(err error) error {
	if err == nil {
		return errors.New("debug adapter exited before accepting connections")
	}
	return fmt.Errorf("debug adapter exited before accepting connections: %w", err)
}

func (s *debugServer) initialize(ctx context.Context) (*dap.Capabilities, error) {
	s.log.Debug("dap initialize")
	adapterID := s.cfg.adapterID
	if adapterID == "" {
		adapterID = s.cfg.langID
	}
	clientID := s.client.ClientID
	if clientID == "" {
		clientID = "rune"
	}
	clientName := s.client.ClientName
	if clientName == "" {
		clientName = "Rune IDE"
	}
	pathFormat := s.client.PathFormat
	if pathFormat == "" {
		pathFormat = "path"
	}
	req := &dap.InitializeRequest{
		Request: s.newRequest("initialize"),
		Arguments: dap.InitializeRequestArguments{
			ClientID:                     clientID,
			ClientName:                   clientName,
			AdapterID:                    adapterID,
			Locale:                       s.client.Locale,
			LinesStartAt1:                s.client.LinesStartAt1 || true,
			ColumnsStartAt1:              s.client.ColumnsStartAt1 || true,
			PathFormat:                   pathFormat,
			SupportsVariableType:         s.client.SupportsVariableType,
			SupportsVariablePaging:       s.client.SupportsVariablePaging,
			SupportsRunInTerminalRequest: s.client.SupportsRunInTerminalRequest,
			SupportsMemoryReferences:     s.client.SupportsMemoryReferences,
			SupportsProgressReporting:    s.client.SupportsProgressReporting,
			SupportsInvalidatedEvent:     s.client.SupportsInvalidatedEvent,
			SupportsMemoryEvent:          s.client.SupportsMemoryEvent,
		},
	}
	resp, err := s.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	initResp, ok := resp.(*dap.InitializeResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type: %T", resp)
	}
	return &initResp.Body, nil
}

func (s *debugServer) stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopCalled = true
	if !s.alive {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	req := &dap.DisconnectRequest{
		Request: s.newRequest("disconnect"),
		Arguments: &dap.DisconnectArguments{
			TerminateDebuggee: true,
		},
	}
	// best-effort disconnect
	_, _ = s.sendRequest(ctx, req)

	s.mu.Lock()
	s.alive = false
	s.mu.Unlock()

	s.closeConn()
	s.wg.Wait()
	return nil
}

func (s *debugServer) sendRequest(ctx context.Context, req dap.Message) (dap.Message, error) {
	s.mu.Lock()
	if !s.alive {
		s.mu.Unlock()
		return nil, errNoServer
	}
	s.mu.Unlock()

	seq := req.GetSeq()
	ch := make(chan dap.Message, 1)
	s.pendingMu.Lock()
	s.pending[seq] = ch
	s.pendingMu.Unlock()

	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, seq)
		s.pendingMu.Unlock()
	}()

	s.writeMu.Lock()
	writeErr := dap.WriteProtocolMessage(s.conn, req)
	s.writeMu.Unlock()
	if writeErr != nil {
		return nil, fmt.Errorf("write request: %w", writeErr)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg, ok := <-ch:
		if !ok || msg == nil {
			return nil, errors.New("server closed connection")
		}
		if resp, ok := msg.(dap.ResponseMessage); ok {
			if !resp.GetResponse().Success {
				return nil, formatResponseError(msg)
			}
		}
		return msg, nil
	}
}

func (s *debugServer) readLoop() {
	defer s.wg.Done()
	for {
		msg, err := dap.ReadProtocolMessage(s.reader)
		if err != nil {
			// Adapters may emit custom messages the SDK does not
			// model (e.g. debugpy's "debugpySockets" event). The
			// full frame has already been consumed, so skip the
			// undecodable message and keep reading rather than
			// tearing down the session.
			if _, ok := errors.AsType[*dap.DecodeProtocolMessageFieldError](err); ok {
				s.log.Debug("skipping undecodable dap message", "error", err)
				continue
			}
			s.mu.Lock()
			alive := s.alive
			stopCalled := s.stopCalled
			s.mu.Unlock()
			if alive && !stopCalled {
				s.log.Warn("read error", "error", err)
			}
			s.closePending()
			return
		}

		switch m := msg.(type) {
		case dap.ResponseMessage:
			reqSeq := m.GetResponse().RequestSeq
			s.pendingMu.Lock()
			ch, ok := s.pending[reqSeq]
			s.pendingMu.Unlock()
			if ok {
				ch <- msg
			} else {
				resp := m.GetResponse()
				if !resp.Success {
					cmd := resp.Command
					if cmd == "launch" || cmd == "attach" {
						err := s.formatLaunchError(m)
						s.mu.Lock()
						s.launchErr = err
						s.mu.Unlock()
					}
				}
				s.log.Debug("unmatched response",
					"requestSeq", reqSeq, "command", resp.Command, "success", resp.Success)
			}
		case dap.EventMessage:
			if s.eventSub != nil {
				s.eventSub.OnEvent(m)
			}
		default:
			s.log.Debug("unknown message type", "type", fmt.Sprintf("%T", msg))
		}
	}
}

func (s *debugServer) closePending() {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	for seq, ch := range s.pending {
		close(ch)
		delete(s.pending, seq)
	}
}

// writeRequest sends a DAP request without waiting for a response.
// This is needed for Launch and Attach because their responses only arrive
// after ConfigurationDone.
func (s *debugServer) writeRequest(req dap.Message) error {
	s.mu.Lock()
	if !s.alive {
		s.mu.Unlock()
		return errNoServer
	}
	s.mu.Unlock()
	s.writeMu.Lock()
	err := dap.WriteProtocolMessage(s.conn, req)
	s.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("write request: %w", err)
	}
	return nil
}

func (s *debugServer) closeConn() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.alive = false
		s.mu.Unlock()
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
}

func (s *debugServer) newRequest(command string) dap.Request {
	seq := int(s.seq.Add(1))
	return dap.Request{
		ProtocolMessage: dap.ProtocolMessage{
			Seq:  seq,
			Type: "request",
		},
		Command: command,
	}
}

// substituteAddr expands the address placeholders in the adapter
// argv. {addr} expands to host:port (used by adapters like delve's
// `--listen={addr}`); {host} and {port} expand to the components for
// adapters that take them separately (e.g. debugpy's
// `--host {host} --port {port}`). When addr is not a valid host:port,
// only {addr} is substituted.
func substituteAddr(args []string, addr string) []string {
	host, port, splitErr := net.SplitHostPort(addr)
	out := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, "{addr}", addr)
		if splitErr == nil {
			a = strings.ReplaceAll(a, "{host}", host)
			a = strings.ReplaceAll(a, "{port}", port)
		}
		out[i] = a
	}
	return out
}

// formatResponseError returns an error describing a failed DAP
// response. It prefers the structured body.error.format field
// (used by delve to report build errors and similar) and falls
// back to the short Response.Message otherwise.
//
// msg is typically a dap.ResponseMessage or *dap.Response.
func formatResponseError(msg any) error {
	var short, detail string
	if rm, ok := msg.(dap.ResponseMessage); ok {
		short = rm.GetResponse().Message
	}
	if r, ok := msg.(*dap.Response); ok {
		short = r.Message
	}
	if er, ok := msg.(*dap.ErrorResponse); ok && er.Body.Error != nil {
		detail = er.Body.Error.Format
	}
	// Some adapters (e.g. delve's dap server on launch errors)
	// already include the short Message at the start of the
	// detailed format. Avoid duplicating it in that case.
	combined := short
	if detail != "" {
		switch {
		case short == "":
			combined = detail
		case detail == short:
			// identical — keep short
		case strings.HasPrefix(detail, short+": "),
			strings.HasPrefix(detail, short+". "),
			strings.HasPrefix(detail, short):
			combined = detail
		default:
			combined = short + ": " + detail
		}
	}
	if combined == "" {
		return errors.New("dap error")
	}
	return fmt.Errorf("dap error: %s", combined)
}

// formatLaunchError builds an error for a failed launch/attach
// response by combining formatResponseError with any stderr
// output buffered from DAP output events. Clears the buffer so
// subsequent launch attempts start fresh.
func (s *debugServer) formatLaunchError(msg any) error {
	base := formatResponseError(msg)
	s.mu.Lock()
	stderr := strings.TrimRight(string(s.stderr), "\n")
	s.stderr = nil
	s.mu.Unlock()
	if stderr == "" {
		return base
	}
	return fmt.Errorf("%w\n%s", base, stderr)
}
