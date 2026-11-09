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

// Package ideacp runs the workspace's configured Agent Client Protocol
// agents. Each agent gets one process per workspace, started on first use
// and shared by its sessions, which are named so any viewer can attach.
package ideacp

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	petname "github.com/dustinkirkland/golang-petname"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi/acpstdio"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/procattr"
)

const (
	// DefaultCloseTimeout is how long Close waits for agents to exit after
	// their stdin closes before killing them.
	DefaultCloseTimeout = 2 * time.Second
	// initializeTimeout bounds an agent's start up to its initialize answer.
	initializeTimeout = 2 * time.Minute
	// stderrTail is how much of an agent's stderr is kept for errors.
	stderrTail = 16 << 10
	// drainTimeout bounds the wait for the rest of an exited agent's
	// output, which a child it left running may hold open.
	drainTimeout = time.Second
)

// Config is the set of agents a Manager can run.
type Config struct {
	Agents map[string]AgentConfig
	// CloseTimeout defaults to DefaultCloseTimeout.
	CloseTimeout time.Duration
}

// AgentConfig is how an agent's process is started.
type AgentConfig struct {
	Command string
	Args    []string
	Env     map[string]string
}

var _ acpapi.Service = (*Manager)(nil)

// Manager is the workspace's acpapi.Service. It is safe for concurrent use.
type Manager struct {
	root     workspaceapi.URI
	executor schemeapi.Executor
	ctx      context.Context
	cancel   context.CancelFunc
	log      *slog.Logger

	mu       sync.Mutex
	cfg      Config
	procs    map[string]*agentProc
	sessions map[string]*session
	// reserved are names of sessions being opened.
	reserved map[string]bool
	closed   bool
}

// New returns a Manager that starts agents through executor in root.
func New(root workspaceapi.URI, executor schemeapi.Executor, cfg Config) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		root:     root,
		executor: executor,
		ctx:      ctx,
		cancel:   cancel,
		log:      slog.With("struct", "ideacp.Manager", "workspace", root.String()),
		cfg:      cfg,
		procs:    make(map[string]*agentProc),
		sessions: make(map[string]*session),
		reserved: make(map[string]bool),
	}
}

// SetConfig replaces the configured agents. Running agents keep the config
// they started with until they exit.
func (m *Manager) SetConfig(cfg Config) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
}

// Close ends every session and closes every agent's stdin, killing the
// agents still running after the close timeout.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	procs := slices.Collect(maps.Values(m.procs))
	clear(m.procs)
	clear(m.sessions)
	timeout := cmp.Or(m.cfg.CloseTimeout, DefaultCloseTimeout)
	m.mu.Unlock()

	for _, p := range procs {
		p.close()
	}
	deadline := time.After(timeout)
	for _, p := range procs {
		select {
		case <-p.exited:
		case <-deadline:
		}
	}
	m.cancel()
	return nil
}

// Agents satisfies acpapi.Service.
func (m *Manager) Agents(context.Context) iterator.Iterator[acpapi.AgentInfo] {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []acpapi.AgentInfo
	for _, name := range slices.Sorted(maps.Keys(m.cfg.Agents)) {
		p := m.procs[name]
		out = append(out, acpapi.AgentInfo{Name: name, Running: p != nil && p.running()})
	}
	return iterator.FromSlice(out)
}

// Sessions satisfies acpapi.Service.
func (m *Manager) Sessions(context.Context) iterator.Iterator[acpapi.SessionInfo] {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []acpapi.SessionInfo
	for _, name := range slices.Sorted(maps.Keys(m.sessions)) {
		out = append(out, m.sessions[name].info)
	}
	return iterator.FromSlice(out)
}

// OpenSession satisfies acpapi.Service.
func (m *Manager) OpenSession(_ context.Context, name string) (acpapi.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.sessions[name]; s != nil {
		return s, nil
	}
	return nil, fmt.Errorf("acp session %q: %w", name, acpapi.ErrNotFound)
}

// NewSession satisfies acpapi.Service.
func (m *Manager) NewSession(ctx context.Context, req acpapi.NewSessionRequest) (acpapi.Session, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, acpapi.ErrClosed
	}
	cfg, ok := m.cfg.Agents[req.Agent]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("acp agent %q: %w", req.Agent, acpapi.ErrNotFound)
	}
	name := req.Name
	if name == "" {
		name = m.freeNameLocked()
	} else if m.sessions[name] != nil || m.reserved[name] {
		m.mu.Unlock()
		return nil, fmt.Errorf("acp session %q: %w", name, acpapi.ErrExists)
	}
	m.reserved[name] = true
	p := m.procs[req.Agent]
	if p == nil {
		p = m.startLocked(req.Agent, cfg)
	}
	m.mu.Unlock()

	s, err := m.open(ctx, p, name, req.Session)
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.reserved, name)
	if err != nil {
		return nil, err
	}
	if m.closed || p.isDone() {
		_ = s.Session.Close()
		return nil, cmp.Or(p.doneErr(), acpapi.ErrClosed)
	}
	m.sessions[name] = s
	return s, nil
}

func (m *Manager) open(ctx context.Context, p *agentProc, name string, req acp.NewSessionRequest) (*session, error) {
	select {
	case <-p.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if p.err != nil {
		return nil, p.err
	}
	if req.Cwd == "" {
		req.Cwd = m.root.Path()
	}
	s, err := p.agent.NewSession(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("start %s session: %w", p.name, err)
	}
	return &session{
		Session: s,
		m:       m,
		proc:    p,
		info:    acpapi.SessionInfo{Name: name, Agent: p.name, ID: s.Info().ID},
	}, nil
}

// freeNameLocked returns a petname no session uses.
func (m *Manager) freeNameLocked() string {
	taken := func(n string) bool { return m.sessions[n] != nil || m.reserved[n] }
	for range 32 {
		if n := petname.Generate(2, "-"); !taken(n) {
			return n
		}
	}
	base := petname.Generate(2, "-")
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s-%d", base, i); !taken(n) {
			return n
		}
	}
}

// startLocked registers a process for name and starts it in the
// background, so concurrent first uses share one spawn.
func (m *Manager) startLocked(name string, cfg AgentConfig) *agentProc {
	p := &agentProc{
		name:   name,
		cfg:    cfg,
		ready:  make(chan struct{}),
		exited: make(chan struct{}),
		done:   make(chan struct{}),
		stderr: &tailBuffer{max: stderrTail},
	}
	m.procs[name] = p
	go debug.CapturePanicReport(func() { m.run(p) })
	return p
}

// run starts p's process, connects to it and supervises it until it exits.
func (m *Manager) run(p *agentProc) {
	ctx, kill := context.WithCancel(m.ctx)
	defer kill()
	// Executors hand *os.File ends to the process as they are. With any
	// other reader os/exec copies stdin through a goroutine, and Wait
	// waits for it to read EOF, which only comes once stdin is closed in
	// response to the exit Wait has yet to report.
	pipes, err := newStdio()
	if err != nil {
		m.fail(p, fmt.Errorf("start agent %s: %w", p.name, err))
		close(p.exited)
		return
	}
	watch := make(chan error, 1)
	env := make([]string, 0, len(p.cfg.Env))
	for _, k := range slices.Sorted(maps.Keys(p.cfg.Env)) {
		env = append(env, k+"="+p.cfg.Env[k])
	}
	cmd := workspaceapi.Cmd{
		Path:    p.cfg.Command,
		Dir:     m.root.Path(),
		Args:    slices.Clone(p.cfg.Args),
		Env:     env,
		Stdin:   pipes.stdinR,
		Stdout:  pipes.stdoutW,
		Stderr:  pipes.stderrW,
		Watcher: workspaceapi.ChanProcessWatcher(watch),
		// Agents run tools of their own; leading a group lets killing the
		// agent reach them.
		SysProcAttr: procattr.NewGroup(),
		Description: "ACP agent " + p.name,
	}
	m.log.Info("starting agent", "agent", p.name, "command", cmd.Path, "args", cmd.Args)
	pid, err := m.executor.StartCommand(ctx, cmd)
	if err != nil {
		pipes.close()
		m.fail(p, fmt.Errorf("start agent %s: %w", p.name, err))
		close(p.exited)
		return
	}
	p.setStdin(pipes.stdinW)
	// The connection reads stdout through connR, which ends with why the
	// agent exited, so its sessions end with that.
	connR, connW := io.Pipe()
	stdoutDone := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(stdoutDone)
		_, _ = io.Copy(connW, pipes.stdoutR)
	})
	stderrDone := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(stderrDone)
		_, _ = io.Copy(p.stderr, pipes.stderrR)
	})

	exited := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		werr := <-watch
		m.log.Info("agent exited", "agent", p.name, "pid", pid, "error", werr)
		// Remote executors write through the process's ends until it
		// exits, so they stay open until then.
		_ = pipes.stdinR.Close()
		_ = pipes.stdoutW.Close()
		_ = pipes.stderrW.Close()
		drained, cancel := context.WithTimeout(context.Background(), drainTimeout)
		for _, done := range []chan struct{}{stdoutDone, stderrDone} {
			select {
			case <-done:
			case <-drained.Done():
			}
		}
		cancel()
		_ = pipes.stdoutR.Close()
		_ = pipes.stderrR.Close()
		exitErr := p.exitErr(werr)
		_ = connW.CloseWithError(exitErr)
		_ = pipes.stdinW.Close()
		exited <- exitErr
		close(p.exited)
	})

	dctx, cancel := context.WithTimeout(ctx, initializeTimeout)
	agent, err := acpstdio.Dial(dctx, pipes.stdinW, connR, acp.InitializeRequest{
		ClientInfo: &acp.Implementation{Name: "rune", Version: debug.Tag},
	})
	cancel()
	if err != nil {
		m.fail(p, fmt.Errorf("start agent %s: %w", p.name, err))
		_ = pipes.stdinW.Close()
		kill()
		<-exited
		return
	}
	p.agent = agent
	close(p.ready)

	var cause error
	select {
	case <-agent.Done():
		cause = agent.Err()
		if cause == nil {
			// Close closed the agent and gives it time to exit before
			// killing it.
			m.drop(p, nil)
			<-p.exited
			return
		}
	case cause = <-exited:
	}
	kill()
	m.drop(p, cause)
	<-p.exited
}

// fail ends p before it became ready.
func (m *Manager) fail(p *agentProc, err error) {
	m.log.Warn("agent failed to start", "agent", p.name, "error", err)
	p.err = err
	close(p.ready)
	m.drop(p, err)
}

// drop forgets p and its sessions, so the next session restarts the agent.
func (m *Manager) drop(p *agentProc, cause error) {
	p.finish(cause)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.procs[p.name] == p {
		delete(m.procs, p.name)
	}
	for name, s := range m.sessions {
		if s.proc == p {
			delete(m.sessions, name)
		}
	}
}

func (m *Manager) forget(s *session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[s.info.Name] == s {
		delete(m.sessions, s.info.Name)
	}
}

// agentProc is one agent process and its connection.
type agentProc struct {
	name   string
	cfg    AgentConfig
	stderr *tailBuffer

	// ready is closed once agent or err is set.
	ready chan struct{}
	agent acpapi.Agent
	err   error
	// exited is closed once the process is gone.
	exited chan struct{}

	mu    sync.Mutex
	stdin io.Closer
	done  chan struct{}
	cause error
}

func (p *agentProc) setStdin(c io.Closer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stdin = c
}

// close ends the agent's sessions and closes its stdin, which asks the
// agent to exit.
func (p *agentProc) close() {
	select {
	case <-p.ready:
		if p.agent != nil {
			_ = p.agent.Close()
		}
	default:
	}
	p.mu.Lock()
	c := p.stdin
	p.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

func (p *agentProc) running() bool {
	select {
	case <-p.ready:
		return p.err == nil && !p.isDone()
	default:
		return false
	}
}

func (p *agentProc) finish(cause error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case <-p.done:
	default:
		p.cause = cause
		close(p.done)
	}
}

func (p *agentProc) isDone() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *agentProc) doneErr() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cause
}

// exitErr describes the process's exit with the tail of its stderr.
func (p *agentProc) exitErr(werr error) error {
	msg := "agent " + p.name + " exited"
	if werr != nil {
		msg += ": " + werr.Error()
	}
	if tail := strings.TrimSpace(p.stderr.String()); tail != "" {
		msg += "\n" + tail
	}
	return errors.New(msg)
}

// session is a named session that leaves the registry when closed.
type session struct {
	acpapi.Session
	m    *Manager
	proc *agentProc
	info acpapi.SessionInfo
}

func (s *session) Info() acpapi.SessionInfo { return s.info }

// Close ends the session; the agent process keeps running.
func (s *session) Close() error {
	s.m.forget(s)
	return s.Session.Close()
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// stdio are the pipes of an agent process: it gets stdinR, stdoutW and
// stderrW, and the manager the other ends.
type stdio struct {
	stdinR, stdinW   *os.File
	stdoutR, stdoutW *os.File
	stderrR, stderrW *os.File
}

func newStdio() (*stdio, error) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, err
	}
	return &stdio{
		stdinR: stdinR, stdinW: stdinW,
		stdoutR: stdoutR, stdoutW: stdoutW,
		stderrR: stderrR, stderrW: stderrW,
	}, nil
}

func (s *stdio) close() {
	for _, f := range []*os.File{s.stdinR, s.stdinW, s.stdoutR, s.stdoutW, s.stderrR, s.stderrW} {
		_ = f.Close()
	}
}
