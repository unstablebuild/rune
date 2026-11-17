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

package agentools

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

const (
	maxSessions  = 64
	minSessionID = 1000
	maxSessionID = 100000
)

// Session represents a persistent command execution session.
type Session struct {
	ID       int
	stdin    io.WriteCloser // pipeWriter (non-PTY) or pty.Master (PTY)
	buf      *HeadTailBuffer
	done     chan struct{} // closed on process exit
	exitCode int
	exitErr  error
	pid      workspaceapi.Pid
	pty      *workspaceapi.Pty  // nil for non-PTY
	cancel   context.CancelFunc // cancels session context
	started  time.Time
	clock    func() time.Time // if set, used by WallTime instead of time.Now
	mu       sync.Mutex
}

// WriteStdin writes data to the session's stdin.
func (s *Session) WriteStdin(data []byte) error {
	if s.stdin == nil {
		return fmt.Errorf("session %d has no stdin", s.ID)
	}
	_, err := s.stdin.Write(data)
	return err
}

// Wait blocks until the session exits or the timeout elapses.
// Returns true if the session has exited.
func (s *Session) Wait(timeout time.Duration) bool {
	if timeout <= 0 {
		return s.Exited()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.done:
		return true
	case <-timer.C:
		// Check if new data arrived; if not, just return.
		return s.Exited()
	}
}

// WallTime returns seconds elapsed since the session started.
func (s *Session) WallTime() float64 {
	now := time.Now()
	if s.clock != nil {
		now = s.clock()
	}
	return now.Sub(s.started).Seconds()
}

// Exited returns true if the process has exited.
func (s *Session) Exited() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// ExitCode returns the process exit code. Only valid after Exited() returns true.
func (s *Session) ExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitCode
}

// Output returns the current buffered output.
func (s *Session) Output() string {
	return s.buf.String()
}

// SessionManager manages persistent command sessions.
type SessionManager struct {
	mu       sync.Mutex
	sessions map[int]*Session
	ctx      context.Context // long-lived lifecycle context
	cancel   context.CancelFunc
	exec     workspaceapi.Executor
	term     workspaceapi.Terminal // may be nil if PTY not available

	// GenerateID, if set, overrides the default random ID generator.
	// For testing only.
	GenerateID func() int

	// Clock, if set, overrides time.Now for session timestamps.
	// For testing only.
	Clock func() time.Time
}

// NewSessionManager creates a session manager. The terminal parameter may
// be nil if PTY support is not needed.
func NewSessionManager(
	ctx context.Context,
	exec workspaceapi.Executor,
	term workspaceapi.Terminal,
) *SessionManager {
	ctx, cancel := context.WithCancel(ctx)
	return &SessionManager{
		sessions: make(map[int]*Session),
		ctx:      ctx,
		cancel:   cancel,
		exec:     exec,
		term:     term,
	}
}

// Create starts a new command session. The shell parameter defaults to
// "bash" if empty. The process runs with a long-lived context that
// outlives individual tool calls. env is appended to the host's environment,
// so a nil env runs the command with the host's environment alone.
func (m *SessionManager) Create(
	cmd, workDir, shell string, tty bool, env []string,
) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ctx.Err(); err != nil {
		return nil, fmt.Errorf("session manager closed: %w", err)
	}

	// Evict if at capacity.
	if len(m.sessions) >= maxSessions {
		if !m.evictLocked() {
			return nil, fmt.Errorf("max sessions (%d) reached and none can be evicted", maxSessions)
		}
	}

	if shell == "" {
		shell = "bash"
	}

	id := m.generateIDLocked()
	sessionCtx, sessionCancel := context.WithCancel(m.ctx)

	buf := NewHeadTailBuffer(0, 0)
	watcher := newProcessWatcher()
	now := time.Now()
	if m.Clock != nil {
		now = m.Clock()
	}
	sess := &Session{
		ID:      id,
		buf:     buf,
		done:    make(chan struct{}),
		cancel:  sessionCancel,
		started: now,
		clock:   m.Clock,
	}

	wcmd := workspaceapi.Cmd{
		Path:    shell,
		Args:    []string{"-c", cmd},
		Dir:     workDir,
		Env:     env,
		Stdout:  buf,
		Stderr:  buf,
		Watcher: watcher,
	}

	if tty && m.term != nil {
		pty, err := m.term.StartPty()
		if err != nil {
			sessionCancel()
			return nil, fmt.Errorf("start pty: %w", err)
		}
		sess.pty = &pty
		wcmd.Stdin = pty.Slave
		wcmd.Stdout = pty.Slave
		wcmd.Stderr = pty.Slave
		sess.stdin = pty.Master

		pid, err := m.exec.Start(sessionCtx, wcmd)
		if err != nil {
			_ = pty.Master.Close()
			_ = pty.Slave.Close()
			sessionCancel()
			return nil, fmt.Errorf("start command: %w", err)
		}
		sess.pid = pid

		// Close slave after start — only master is needed.
		_ = pty.Slave.Close()

		// Read master output into buffer.
		go debug.CapturePanicReport(func() {

			_, _ = io.Copy(buf, pty.Master)

		})
	} else {
		// Non-PTY: use OS pipes for stdin. os.Pipe returns *os.File
		// objects which exec.Cmd can pass directly to the child
		// without an internal copy goroutine, avoiding a Wait deadlock.
		pr, pw, err := os.Pipe()
		if err != nil {
			sessionCancel()
			return nil, fmt.Errorf("create stdin pipe: %w", err)
		}
		wcmd.Stdin = pr
		sess.stdin = pw

		pid, err := m.exec.Start(sessionCtx, wcmd)
		if err != nil {
			_ = pr.Close()
			_ = pw.Close()
			sessionCancel()
			return nil, fmt.Errorf("start command: %w", err)
		}
		sess.pid = pid

		// Close the read end — the child process owns it now.
		_ = pr.Close()
	}

	// Watch for process exit.
	go debug.CapturePanicReport(func() {

		procErr := <-watcher.WatchProcess()
		sess.mu.Lock()
		sess.exitErr = procErr
		if procErr != nil {
			// Try to extract exit code from the error.
			sess.exitCode = exitCodeFromError(procErr)
		}
		sess.mu.Unlock()
		close(sess.done)
		// Clean up PTY master on exit.
		if sess.pty != nil {
			_ = sess.pty.Master.Close()
		}

	})

	m.sessions[id] = sess
	return sess, nil
}

// Get returns the session with the given ID.
func (m *SessionManager) Get(id int) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Close kills all sessions and releases resources.
func (m *SessionManager) Close() error {
	m.cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		s.cancel()
		// Best-effort signal.
		_ = m.exec.Signal(s.pid, syscall.SIGKILL)
		delete(m.sessions, id)
	}
	return nil
}

func (m *SessionManager) generateIDLocked() int {
	if m.GenerateID != nil {
		return m.GenerateID()
	}
	for {
		id := minSessionID + rand.Intn(maxSessionID-minSessionID)
		if _, exists := m.sessions[id]; !exists {
			return id
		}
	}
}

// evictLocked removes one session to make room. Prefers oldest completed,
// then oldest running. Returns true if a session was evicted.
func (m *SessionManager) evictLocked() bool {
	// Try completed sessions first.
	type candidate struct {
		id      int
		started time.Time
	}
	var completed, running []candidate
	for id, s := range m.sessions {
		c := candidate{id: id, started: s.started}
		if s.Exited() {
			completed = append(completed, c)
		} else {
			running = append(running, c)
		}
	}

	sortByAge := func(cs []candidate) {
		sort.Slice(cs, func(i, j int) bool {
			return cs[i].started.Before(cs[j].started)
		})
	}

	if len(completed) > 0 {
		sortByAge(completed)
		victim := completed[0]
		m.sessions[victim.id].cancel()
		delete(m.sessions, victim.id)
		return true
	}
	if len(running) > 0 {
		sortByAge(running)
		victim := running[0]
		s := m.sessions[victim.id]
		s.cancel()
		_ = m.exec.Signal(s.pid, syscall.SIGKILL)
		delete(m.sessions, victim.id)
		return true
	}
	return false
}

// exitCodeFromError extracts an exit code from a process error.
// Returns -1 if the code cannot be determined.
func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	// os/exec wraps the exit status in an *exec.ExitError.
	type exitCoder interface {
		ExitCode() int
	}
	if ec, ok := err.(exitCoder); ok {
		return ec.ExitCode()
	}
	return -1
}
