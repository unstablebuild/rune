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

package ideacp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi/acpstdio"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/workspace"
)

// fakeHarness is an in-process ACP agent whose prompts are echoed back.
type fakeHarness struct {
	mu   sync.Mutex
	n    int
	cwds []string
	done chan struct{}
}

func newFakeHarness() *fakeHarness { return &fakeHarness{done: make(chan struct{})} }

func (f *fakeHarness) Info() acp.InitializeResponse {
	return acp.InitializeResponse{ProtocolVersion: acp.ProtocolVersionNumber}
}

func (f *fakeHarness) NewSession(_ context.Context, req acp.NewSessionRequest) (acpapi.Session, error) {
	f.mu.Lock()
	f.n++
	f.cwds = append(f.cwds, req.Cwd)
	id := acp.SessionId(fmt.Sprintf("s%d", f.n))
	f.mu.Unlock()
	s := &fakeHarnessSession{id: id, log: acpapi.NewLog()}
	s.log.Started(acpapi.Started{})
	return s, nil
}

func (f *fakeHarness) LoadSession(context.Context, acp.LoadSessionRequest) (acpapi.Session, error) {
	return nil, errors.New("unsupported")
}

func (f *fakeHarness) ListSessions(context.Context, acp.ListSessionsRequest) iterator.Iterator[acp.SessionInfo] {
	return iterator.Empty[acp.SessionInfo]()
}

func (f *fakeHarness) Done() <-chan struct{} { return f.done }
func (f *fakeHarness) Err() error            { return nil }
func (f *fakeHarness) Close() error          { return nil }

type fakeHarnessSession struct {
	id  acp.SessionId
	log *acpapi.Log
}

func (s *fakeHarnessSession) Info() acpapi.SessionInfo                { return acpapi.SessionInfo{ID: s.id} }
func (s *fakeHarnessSession) Events() iterator.Iterator[acpapi.Event] { return s.log.Events() }
func (s *fakeHarnessSession) Cancel(context.Context) error            { return nil }
func (s *fakeHarnessSession) Close() error                            { s.log.Close(nil); return nil }

func (s *fakeHarnessSession) Prompt(_ context.Context, content []acp.ContentBlock) error {
	if err := s.log.Prompt(content); err != nil {
		return err
	}
	s.log.Update(acp.UpdateAgentMessageText("echo: " + content[0].Text.Text))
	s.log.Done(acpapi.TurnEnd{StopReason: acp.StopReasonEndTurn})
	return nil
}

func (s *fakeHarnessSession) Respond(_ context.Context, id acpapi.PermissionID, o acp.RequestPermissionOutcome) error {
	return s.log.Respond(id, o)
}

func (s *fakeHarnessSession) SetConfigOption(context.Context, acp.SetSessionConfigOptionRequest) error {
	return nil
}

// fakeExecutor runs every command as a fakeHarness served over the
// command's stdio.
type fakeExecutor struct {
	mu       sync.Mutex
	starts   int
	cmds     []workspaceapi.Cmd
	harness  []*fakeHarness
	crashes  []chan error
	startErr error
	// hang makes agents ignore their stdin closing, so only a kill stops
	// them.
	hang   bool
	exited chan error
}

func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{exited: make(chan error, 16)}
}

func (e *fakeExecutor) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	e.mu.Lock()
	if e.startErr != nil {
		e.mu.Unlock()
		return 0, e.startErr
	}
	e.starts++
	pid := workspaceapi.Pid(e.starts)
	h := newFakeHarness()
	crash := make(chan error, 1)
	e.cmds = append(e.cmds, cmd)
	e.harness = append(e.harness, h)
	e.crashes = append(e.crashes, crash)
	hang := e.hang
	e.mu.Unlock()

	sctx, cancel := context.WithCancel(ctx)
	go func() {
		defer cancel()
		var stdin io.Reader = cmd.Stdin
		if hang {
			stdin = &hangReader{r: cmd.Stdin, ctx: sctx}
		}
		served := make(chan struct{})
		go func() {
			_ = acpstdio.Serve(sctx, h, stdin, cmd.Stdout)
			close(served)
		}()
		var exitErr error
		select {
		case <-served:
			if ctx.Err() != nil {
				exitErr = errors.New("signal: killed")
			}
		case err := <-crash:
			_, _ = fmt.Fprintln(cmd.Stderr, "panic: boom")
			exitErr = err
		}
		e.exited <- exitErr
		cmd.Watcher.WatchProcess() <- exitErr
	}()
	return pid, nil
}

func (e *fakeExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (e *fakeExecutor) LookPath(_ context.Context, name, _ string) (string, error) {
	return name, nil
}
func (e *fakeExecutor) Close() error { return nil }

func (e *fakeExecutor) startCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.starts
}

func (e *fakeExecutor) lastCmd() workspaceapi.Cmd {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cmds[len(e.cmds)-1]
}

func (e *fakeExecutor) crash(i int, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.crashes[i] <- err
}

// hangReader passes reads through but holds EOF until ctx ends.
type hangReader struct {
	r   io.Reader
	ctx context.Context
}

func (h *hangReader) Read(p []byte) (int, error) {
	n, err := h.r.Read(p)
	if errors.Is(err, io.EOF) {
		<-h.ctx.Done()
	}
	return n, err
}

func testRoot(t *testing.T) workspaceapi.URI {
	t.Helper()
	root, err := workspaceapi.ParseURI("file:///work")
	require.NoError(t, err)
	return root
}

var fakeConfig = Config{Agents: map[string]AgentConfig{
	"fake": {Command: "fake-agent", Args: []string{"acp"}, Env: map[string]string{"B": "2", "A": "1"}},
}}

func newManager(t *testing.T, e *fakeExecutor, cfg Config) *Manager {
	t.Helper()
	m := New(testRoot(t), e, cfg)
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func newSession(t *testing.T, m *Manager, name string) acpapi.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := m.NewSession(ctx, acpapi.NewSessionRequest{Agent: "fake", Name: name})
	require.NoError(t, err)
	return s
}

func sessionNames(t *testing.T, m *Manager) []string {
	t.Helper()
	infos, err := iterator.ToSlice(context.Background(), m.Sessions(context.Background()))
	require.NoError(t, err)
	var names []string
	for _, info := range infos {
		names = append(names, info.Name)
	}
	return names
}

func agents(t *testing.T, m *Manager) []acpapi.AgentInfo {
	t.Helper()
	infos, err := iterator.ToSlice(context.Background(), m.Agents(context.Background()))
	require.NoError(t, err)
	return infos
}

// endErr reads it until it ends and returns its error.
func endErr(t *testing.T, it iterator.Iterator[acpapi.Event]) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, ok := it.Next(ctx); !ok {
			require.NoError(t, ctx.Err(), "events did not end")
			return it.Err()
		}
	}
}

func TestTwoSessionsShareOneProcess(t *testing.T) {
	e := newFakeExecutor()
	m := newManager(t, e, fakeConfig)
	s1 := newSession(t, m, "one")
	s2 := newSession(t, m, "two")
	assert.Equal(t, 1, e.startCount())
	assert.Equal(t, acpapi.SessionInfo{Name: "one", Agent: "fake", ID: "s1"}, s1.Info())
	assert.Equal(t, acpapi.SessionInfo{Name: "two", Agent: "fake", ID: "s2"}, s2.Info())
	assert.Equal(t, []string{"one", "two"}, sessionNames(t, m))
	assert.Equal(t, []acpapi.AgentInfo{{Name: "fake", Running: true}}, agents(t, m))

	cmd := e.lastCmd()
	assert.Equal(t, "fake-agent", cmd.Path)
	assert.Equal(t, []string{"acp"}, cmd.Args)
	assert.Equal(t, []string{"A=1", "B=2"}, cmd.Env)
	assert.Equal(t, "/work", cmd.Dir)
	assert.NotNil(t, cmd.SysProcAttr)
	assert.Equal(t, []string{"/work", "/work"}, e.harness[0].cwds)

	opened, err := m.OpenSession(context.Background(), "two")
	require.NoError(t, err)
	it := opened.Events()
	require.NoError(t, opened.Prompt(context.Background(), []acp.ContentBlock{acp.TextBlock("hi")}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		ev, ok := it.Next(ctx)
		require.True(t, ok, "events ended: %v", it.Err())
		if ev.Update != nil && ev.Update.AgentMessageChunk != nil {
			assert.Equal(t, "echo: hi", ev.Update.AgentMessageChunk.Content.Text.Text)
			break
		}
	}
}

func TestConcurrentFirstUseSpawnsOnce(t *testing.T) {
	e := newFakeExecutor()
	m := newManager(t, e, fakeConfig)
	const n = 8
	names := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			names[i] = newSession(t, m, "").Info().Name
		})
	}
	wg.Wait()
	assert.Equal(t, 1, e.startCount())
	seen := map[string]bool{}
	for _, name := range names {
		assert.NotEmpty(t, name)
		assert.False(t, seen[name], "duplicate name %q", name)
		seen[name] = true
	}
	assert.Len(t, sessionNames(t, m), n)
}

func TestSessionCloseKeepsProcess(t *testing.T) {
	e := newFakeExecutor()
	m := newManager(t, e, fakeConfig)
	s := newSession(t, m, "one")
	it := s.Events()
	require.NoError(t, s.Close())
	assert.NoError(t, endErr(t, it))
	_, err := m.OpenSession(context.Background(), "one")
	assert.ErrorIs(t, err, acpapi.ErrNotFound)
	assert.Equal(t, []acpapi.AgentInfo{{Name: "fake", Running: true}}, agents(t, m))

	newSession(t, m, "one")
	assert.Equal(t, 1, e.startCount())
}

func TestAgentExitThenRespawn(t *testing.T) {
	e := newFakeExecutor()
	m := newManager(t, e, fakeConfig)
	s1, s2 := newSession(t, m, "one"), newSession(t, m, "two")
	it1, it2 := s1.Events(), s2.Events()
	e.crash(0, errors.New("exit status 2"))

	for _, it := range []iterator.Iterator[acpapi.Event]{it1, it2} {
		err := endErr(t, it)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "agent fake exited: exit status 2")
		assert.Contains(t, err.Error(), "panic: boom")
	}
	require.Eventually(t, func() bool { return len(sessionNames(t, m)) == 0 }, 5*time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return !agents(t, m)[0].Running }, 5*time.Second, time.Millisecond)

	newSession(t, m, "one")
	assert.Equal(t, 2, e.startCount())
}

func TestSessionNames(t *testing.T) {
	m := newManager(t, newFakeExecutor(), fakeConfig)
	assert.Equal(t, "mine", newSession(t, m, "mine").Info().Name)
	_, err := m.NewSession(context.Background(), acpapi.NewSessionRequest{Agent: "fake", Name: "mine"})
	assert.ErrorIs(t, err, acpapi.ErrExists)
	generated := newSession(t, m, "").Info().Name
	assert.Regexp(t, `^[a-z]+-[a-z]+(-\d+)?$`, generated)
	assert.NotEqual(t, "mine", generated)
}

func TestUnknownAgent(t *testing.T) {
	e := newFakeExecutor()
	m := newManager(t, e, fakeConfig)
	_, err := m.NewSession(context.Background(), acpapi.NewSessionRequest{Agent: "nope"})
	assert.ErrorIs(t, err, acpapi.ErrNotFound)
	_, err = m.OpenSession(context.Background(), "nope")
	assert.ErrorIs(t, err, acpapi.ErrNotFound)
	assert.Equal(t, 0, e.startCount())
}

func TestSetConfigAppliesOnNextStart(t *testing.T) {
	e := newFakeExecutor()
	m := newManager(t, e, fakeConfig)
	newSession(t, m, "one")
	m.SetConfig(Config{Agents: map[string]AgentConfig{
		"fake":  {Command: "fake-agent", Args: []string{"acp", "--v2"}},
		"other": {Command: "other-agent"},
	}})
	newSession(t, m, "two")
	assert.Equal(t, 1, e.startCount())
	assert.Equal(t, []string{"acp"}, e.lastCmd().Args)
	assert.Equal(t, []acpapi.AgentInfo{{Name: "fake", Running: true}, {Name: "other"}}, agents(t, m))

	e.crash(0, errors.New("exit status 1"))
	require.Eventually(t, func() bool { return !agents(t, m)[0].Running }, 5*time.Second, time.Millisecond)
	newSession(t, m, "three")
	assert.Equal(t, 2, e.startCount())
	assert.Equal(t, []string{"acp", "--v2"}, e.lastCmd().Args)
}

func TestStartFailureRetriesOnNextSession(t *testing.T) {
	e := newFakeExecutor()
	e.startErr = errors.New("fake-agent: not found")
	m := newManager(t, e, fakeConfig)
	_, err := m.NewSession(context.Background(), acpapi.NewSessionRequest{Agent: "fake"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fake-agent: not found")
	assert.Empty(t, sessionNames(t, m))

	e.mu.Lock()
	e.startErr = nil
	e.mu.Unlock()
	newSession(t, m, "")
	assert.Equal(t, 1, e.startCount())
}

func TestCloseEndsSessionsAndStopsAgents(t *testing.T) {
	e := newFakeExecutor()
	m := New(testRoot(t), e, fakeConfig)
	it := newSession(t, m, "one").Events()
	require.NoError(t, m.Close())
	assert.NoError(t, endErr(t, it))
	select {
	case err := <-e.exited:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("agent did not exit")
	}
	assert.Empty(t, sessionNames(t, m))
	_, err := m.NewSession(context.Background(), acpapi.NewSessionRequest{Agent: "fake"})
	assert.ErrorIs(t, err, acpapi.ErrClosed)
}

func TestCloseKillsAgentsThatDoNotExit(t *testing.T) {
	e := newFakeExecutor()
	e.hang = true
	cfg := fakeConfig
	cfg.CloseTimeout = 50 * time.Millisecond
	m := New(testRoot(t), e, cfg)
	newSession(t, m, "one")
	start := time.Now()
	require.NoError(t, m.Close())
	assert.GreaterOrEqual(t, time.Since(start), cfg.CloseTimeout)
	select {
	case err := <-e.exited:
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "killed"))
	case <-time.After(5 * time.Second):
		t.Fatal("agent was not killed")
	}
}

// helperModeEnv makes the test binary run TestHelperAgentProcess as an
// agent: "exit" exits before answering initialize, "crash" serves a
// fakeHarness that exits when prompted "crash".
const helperModeEnv = "IDEACP_TEST_AGENT"

// newProcessManager runs mode as an agent process through the local
// file scheme, as agents of a local workspace run.
func newProcessManager(t *testing.T, mode string) *Manager {
	t.Helper()
	root, err := workspaceapi.ParseURI("file://" + t.TempDir())
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	self, err := os.Executable()
	require.NoError(t, err)
	m := New(root, scheme, Config{Agents: map[string]AgentConfig{"proc": {
		Command: self,
		Args:    []string{"-test.run=^TestHelperAgentProcess$"},
		Env:     map[string]string{helperModeEnv: mode},
	}}})
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestAgentProcessExitsBeforeInitialize(t *testing.T) {
	m := newProcessManager(t, "exit")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := m.NewSession(ctx, acpapi.NewSessionRequest{Agent: "proc"})

	require.Error(t, err)
	require.NoError(t, ctx.Err(), "the exit went unnoticed")
	assert.Contains(t, err.Error(), "exit status 3")
	assert.Contains(t, err.Error(), "no credentials")
}

func TestAgentProcessCrashEndsSessions(t *testing.T) {
	m := newProcessManager(t, "crash")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := m.NewSession(ctx, acpapi.NewSessionRequest{Agent: "proc"})
	require.NoError(t, err)
	it := s.Events()

	_ = s.Prompt(ctx, []acp.ContentBlock{acp.TextBlock("crash")})

	err = endErr(t, it)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent proc exited: exit status 2")
	assert.Contains(t, err.Error(), "panic: boom")
}

func TestHelperAgentProcess(t *testing.T) {
	switch os.Getenv(helperModeEnv) {
	case "exit":
		fmt.Fprintln(os.Stderr, "no credentials")
		os.Exit(3)
	case "crash":
		_ = acpstdio.Serve(context.Background(), crashingHarness{newFakeHarness()}, os.Stdin, os.Stdout)
		os.Exit(0)
	}
}

// crashingHarness is a fakeHarness whose process dies when prompted
// "crash".
type crashingHarness struct{ *fakeHarness }

func (h crashingHarness) NewSession(ctx context.Context, req acp.NewSessionRequest) (acpapi.Session, error) {
	s, err := h.fakeHarness.NewSession(ctx, req)
	if err != nil {
		return nil, err
	}
	return crashingSession{s.(*fakeHarnessSession)}, nil
}

type crashingSession struct{ *fakeHarnessSession }

func (s crashingSession) Prompt(ctx context.Context, content []acp.ContentBlock) error {
	if content[0].Text.Text == "crash" {
		fmt.Fprintln(os.Stderr, "panic: boom")
		os.Exit(2)
	}
	return s.fakeHarnessSession.Prompt(ctx, content)
}
