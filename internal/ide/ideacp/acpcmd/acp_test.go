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

package acpcmd

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/programstatus"
)

// fakeManager forgets a session once it is closed, as ideacp.Manager
// does.
type fakeManager struct {
	agents []acpapi.AgentInfo

	mu       sync.Mutex
	sessions map[string]*fakeSession
	created  []acpapi.NewSessionRequest
}

func newFakeManager(agents []string, sessions ...string) *fakeManager {
	m := &fakeManager{sessions: make(map[string]*fakeSession)}
	for _, a := range agents {
		m.agents = append(m.agents, acpapi.AgentInfo{Name: a})
	}
	for _, name := range sessions {
		s := newFakeSession()
		s.info.Name = name
		s.follow = true
		m.sessions[name] = s
	}
	return m
}

func (m *fakeManager) Agents(context.Context) iterator.Iterator[acpapi.AgentInfo] {
	return iterator.FromSlice(m.agents)
}

func (m *fakeManager) NewSession(_ context.Context, req acpapi.NewSessionRequest) (acpapi.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created = append(m.created, req)
	s := newFakeSession()
	s.info = acpapi.SessionInfo{Name: req.Name, Agent: req.Agent}
	s.follow = true
	m.sessions[req.Name] = s
	return s, nil
}

func (m *fakeManager) Sessions(context.Context) iterator.Iterator[acpapi.SessionInfo] {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []acpapi.SessionInfo
	for _, s := range m.sessions {
		if !s.snapshot().closed {
			out = append(out, s.info)
		}
	}
	slices.SortFunc(out, func(a, b acpapi.SessionInfo) int { return strings.Compare(a.Name, b.Name) })
	return iterator.FromSlice(out)
}

func (m *fakeManager) OpenSession(_ context.Context, name string) (acpapi.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[name]; ok && !s.snapshot().closed {
		return s, nil
	}
	return nil, fmt.Errorf("acp session %q: %w", name, acpapi.ErrNotFound)
}

// live adds a session of claude called name, whose events the test logs.
func (m *fakeManager) live(t *testing.T, name string) *fakeSession {
	s := newFakeSession()
	s.info = acpapi.SessionInfo{Name: name, Agent: "claude"}
	s.log = acpapi.NewLog()
	t.Cleanup(func() { s.log.Close(nil) })
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[name] = s
	return s
}

func (m *fakeManager) createdSessions() []acpapi.NewSessionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.created)
}

type window uint64

func (w window) WindowID() uint64 { return uint64(w) }

// fakeWM keeps a tab per URI like the browser does, and records which tab
// each window shows, and status the reports of sessions no tab shows.
type fakeWM struct {
	browserapi.WindowManager
	t      *testing.T
	status *fakeStatus

	mu    sync.Mutex
	tabs  map[workspaceapi.URI]*tab
	shown map[browserapi.Window]*tab
}

type tab struct {
	browserapi.Handler
	uri   workspaceapi.URI
	label string
}

func (w *fakeWM) Tab(uri workspaceapi.URI, _ rune, name string, h browserapi.Handler) (browserapi.Handler, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if tb, ok := w.tabs[uri]; ok {
		return tb, nil
	}
	tb := &tab{Handler: h, uri: uri, label: name}
	w.tabs[uri] = tb
	w.t.Cleanup(func() { _ = h.Close() })
	return tb, nil
}

func (w *fakeWM) SetWindowContent(win browserapi.Window, h browserapi.Handler) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.shown[win] = h.(*tab)
	return nil
}

func (w *fakeWM) Focus() (browserapi.Window, error) { return window(9), nil }

// closeTab closes the tab of uri like the user would.
func (w *fakeWM) closeTab(uri workspaceapi.URI) error {
	w.mu.Lock()
	tb := w.tabs[uri]
	delete(w.tabs, uri)
	w.mu.Unlock()
	return tb.Close()
}

func (w *fakeWM) shownIn(win browserapi.Window) *tab {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.shown[win]
}

// fakeStatus keeps the reports made at each uri.
type fakeStatus struct {
	mu      sync.Mutex
	reports map[string][]programstatus.Report
}

func (s *fakeStatus) SetTabStatus(uri workspaceapi.URI, r programstatus.Report) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reports == nil {
		s.reports = make(map[string][]programstatus.Report)
	}
	s.reports[uri.String()] = append(s.reports[uri.String()], r)
	return nil
}

func (s *fakeStatus) at(uri string) []programstatus.Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reports[uri])
}

// waitReports waits for the reports made at uri to be want.
func (s *fakeStatus) waitReports(t *testing.T, uri string, want ...programstatus.Report) {
	t.Helper()
	require.Eventually(t, func() bool { return slices.Equal(s.at(uri), want) },
		10*time.Second, 10*time.Millisecond, "reports at %s: %+v", uri, s.at(uri))
}

func newTestHandler(t *testing.T, m Manager) (*Handler, *fakeWM) {
	t.Helper()
	wm := &fakeWM{
		t:      t,
		status: &fakeStatus{},
		tabs:   make(map[workspaceapi.URI]*tab),
		shown:  make(map[browserapi.Window]*tab),
	}
	return New(m, Config{
		WindowManager: wm,
		Interrupter:   term.NopInterrupter(),
		Root:          mustURI(t, "file:///work"),
		Chat:          dialoguetui.DefaultComponentConfig(),
		Status:        wm.status,
	}), wm
}

func mustURI(t *testing.T, s string) workspaceapi.URI {
	t.Helper()
	u, err := workspaceapi.ParseURI(s)
	require.NoError(t, err)
	return u
}

func TestHandlerNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		agents  []string
		args    []string
		wantErr string
		wantURI string
	}{
		{
			name:    "named",
			agents:  []string{"claude"},
			args:    []string{"new", "claude", "review"},
			wantURI: "acp://claude/review",
		},
		{
			name:    "unknown agent",
			agents:  []string{"claude", "codex"},
			args:    []string{"new", "gemini"},
			wantErr: `unknown agent "gemini", choose one of: claude, codex`,
		},
		{
			name:    "no agents",
			args:    []string{"new", "claude"},
			wantErr: `unknown agent "claude": configure agents under acp.agents`,
		},
		{
			name:    "taken name",
			agents:  []string{"claude"},
			args:    []string{"new", "claude", "taken"},
			wantErr: `session "taken" already exists`,
		},
		{
			name:    "usage",
			agents:  []string{"claude"},
			args:    []string{"new"},
			wantErr: "usage: acp new <agent> [<session>]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newFakeManager(tt.agents, "taken")
			h, wm := newTestHandler(t, m)
			err := h.HandleCommand(context.Background(), textapi.Command{
				Name: Name, Args: tt.args, Window: window(1),
			})
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			shown := wm.shownIn(window(1))
			require.NotNil(t, shown)
			assert.Equal(t, tt.wantURI, shown.uri.String())
			assert.Equal(t, "review", shown.label)
			require.Eventually(t, func() bool { return len(m.createdSessions()) == 1 },
				10*time.Second, 10*time.Millisecond)
			assert.Equal(t, []acpapi.NewSessionRequest{{Agent: "claude", Name: "review"}},
				m.createdSessions())
		})
	}
}

func TestHandlerNewPetname(t *testing.T) {
	t.Parallel()
	m := newFakeManager([]string{"claude"})
	h, wm := newTestHandler(t, m)
	require.NoError(t, h.HandleCommand(context.Background(), textapi.Command{
		Name: Name, Args: []string{"new", "claude"},
	}))
	shown := wm.shownIn(window(9))
	require.NotNil(t, shown)
	require.NotEmpty(t, shown.label)
	assert.Equal(t, "acp://claude/"+shown.label, shown.uri.String())
	require.Eventually(t, func() bool { return len(m.createdSessions()) == 1 },
		10*time.Second, 10*time.Millisecond)
	assert.Equal(t, []acpapi.NewSessionRequest{{Agent: "claude", Name: shown.label}},
		m.createdSessions())
}

func TestHandlerOpen(t *testing.T) {
	t.Parallel()

	t.Run("unknown session", func(t *testing.T) {
		t.Parallel()
		h, _ := newTestHandler(t, newFakeManager([]string{"claude"}))
		err := h.HandleCommand(context.Background(), textapi.Command{
			Name: Name, Args: []string{"open", "nope"},
		})
		assert.ErrorIs(t, err, acpapi.ErrNotFound)
	})

	t.Run("reuses an open tab", func(t *testing.T) {
		t.Parallel()
		h, wm := newTestHandler(t, newFakeManager([]string{"claude"}, "brave-fox"))
		open := textapi.Command{Name: Name, Args: []string{"open", "brave-fox"}, Window: window(1)}
		require.NoError(t, h.HandleCommand(context.Background(), open))
		first := wm.shownIn(window(1))
		require.NotNil(t, first)
		assert.Equal(t, "acp://claude/brave-fox", first.uri.String())

		open.Window = window(2)
		require.NoError(t, h.HandleCommand(context.Background(), open))
		assert.Same(t, first, wm.shownIn(window(2)))
	})

	t.Run("reopens a closed tab", func(t *testing.T) {
		t.Parallel()
		m := newFakeManager([]string{"claude"})
		require.NoError(t, m.live(t, "brave-fox").log.Prompt(nil))
		h, wm := newTestHandler(t, m)
		open := textapi.Command{Name: Name, Args: []string{"open", "brave-fox"}, Window: window(1)}
		require.NoError(t, h.HandleCommand(context.Background(), open))
		first := wm.shownIn(window(1))
		require.NoError(t, wm.closeTab(first.uri))

		require.NoError(t, h.HandleCommand(context.Background(), open))
		second := wm.shownIn(window(1))
		assert.NotSame(t, first, second)
		assert.NotSame(t, first.Handler, second.Handler)
	})

	t.Run("opens another tab with --new", func(t *testing.T) {
		t.Parallel()
		m := newFakeManager([]string{"claude"}, "brave-fox")
		h, wm := newTestHandler(t, m)
		open := func(win window, args ...string) *tab {
			t.Helper()
			require.NoError(t, h.HandleCommand(context.Background(), textapi.Command{
				Name: Name, Args: args, Window: win,
			}))
			shown := wm.shownIn(win)
			require.NotNil(t, shown)
			return shown
		}
		first := open(1, "open", "brave-fox")
		second := open(2, "open", "--new", "brave-fox")
		assert.Equal(t, "acp://claude/brave-fox/2", second.uri.String())
		assert.Equal(t, "brave-fox (2)", second.label)
		assert.NotSame(t, first.Handler, second.Handler, "each tab has its own chat")
		assert.Same(t, first, open(3, "open", "brave-fox"), "without --new, the first tab")
		assert.Equal(t, "acp://claude/brave-fox/3", open(4, "open", "--new", "brave-fox").uri.String())

		require.NoError(t, wm.closeTab(first.uri))
		assert.Same(t, second, open(5, "open", "brave-fox"), "the first tab left")
		again := open(6, "open", "--new", "brave-fox")
		assert.Equal(t, "acp://claude/brave-fox", again.uri.String(), "the lowest free number")
		assert.Equal(t, "brave-fox", again.label)
		assert.False(t, m.sessions["brave-fox"].snapshot().closed, "the session's other tabs stay")
	})
}

func TestHandlerViews(t *testing.T) {
	t.Parallel()
	say := func(log *acpapi.Log, text string) {
		log.Update(acp.SessionUpdate{AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
			Content: acp.TextBlock(text),
		}})
	}
	m := newFakeManager([]string{"claude"})
	s := m.live(t, "brave-fox")
	say(s.log, "the walrus says hi")
	h, wm := newTestHandler(t, m)
	const height = 12
	views := []struct {
		args  []string
		width int
		tab   *tab
	}{
		{args: []string{"open", "brave-fox"}, width: 40},
		{args: []string{"open", "--new", "brave-fox"}, width: 70},
	}
	for i := range views {
		v := &views[i]
		require.NoError(t, h.HandleCommand(context.Background(), textapi.Command{
			Name: Name, Args: v.args, Window: window(i + 1),
		}))
		v.tab = wm.shownIn(window(i + 1))
		v.tab.Resize(v.width, height)
	}
	require.NotSame(t, views[0].tab.Handler, views[1].tab.Handler)
	screen := func(i int) string {
		w := term.NewStringWriter(views[i].width, height)
		views[i].tab.Draw(w)
		_ = w.Flush()
		return w.String()
	}
	shows := func(text string) {
		t.Helper()
		for i := range views {
			require.Eventually(t, func() bool { return strings.Contains(screen(i), text) },
				10*time.Second, 10*time.Millisecond, "the tab %d wide shows %q:\n%s",
				views[i].width, text, screen(i))
		}
	}
	shows("the walrus says hi")
	say(s.log, " and bye")
	shows("the walrus says hi and bye")
}

func TestHandlerCloseTab(t *testing.T) {
	t.Parallel()
	const status = "acp://claude/brave-fox/session"
	report := func(state programstatus.State, msg string) programstatus.Report {
		return programstatus.Report{State: state, Progress: -1, App: "brave-fox", Msg: msg}
	}
	working := report(programstatus.Working, "")
	blocked := report(programstatus.Blocked, "Run ls")
	blocked.Kind, blocked.Title = programstatus.KindPermission, "Run ls"
	ask := func(t *testing.T, log *acpapi.Log) {
		title := "Run ls"
		go debug.CapturePanicReport(func() {
			_, _ = log.Ask(context.Background(), acp.RequestPermissionRequest{
				ToolCall: acp.ToolCallUpdate{ToolCallId: "t1", Title: &title},
				Options:  []acp.PermissionOption{allowOnce},
			})
		})
		require.Eventually(t, func() bool { return len(log.State().Pending) == 1 },
			10*time.Second, 10*time.Millisecond)
	}
	answer := func(log *acpapi.Log) {
		_ = log.Respond(log.State().Pending[0].ID, acp.NewRequestPermissionOutcomeSelected("allow"))
	}
	endTurn := func(log *acpapi.Log) { log.Done(acpapi.TurnEnd{StopReason: acp.StopReasonEndTurn}) }

	tests := []struct {
		name string
		// before is what the session did before its tab closed, and after
		// what it does once the session reported want[0].
		before func(*testing.T, *acpapi.Log)
		after  func(*acpapi.Log)
		want   []programstatus.Report
		ends   bool
	}{
		{
			name:   "idle session ends",
			before: func(*testing.T, *acpapi.Log) {},
			ends:   true,
		},
		{
			name: "idle after a turn ends",
			before: func(_ *testing.T, log *acpapi.Log) {
				_ = log.Prompt(nil)
				endTurn(log)
			},
			ends: true,
		},
		{
			name:   "running turn reports until it ends",
			before: func(_ *testing.T, log *acpapi.Log) { _ = log.Prompt(nil) },
			after:  endTurn,
			want:   []programstatus.Report{working, report(programstatus.Done, "Turn completed.")},
		},
		{
			name:   "failed turn",
			before: func(_ *testing.T, log *acpapi.Log) { _ = log.Prompt(nil) },
			after: func(log *acpapi.Log) {
				log.Done(acpapi.TurnEnd{StopReason: acpapi.StopReasonError, Error: "agent claude\nexited"})
			},
			want: []programstatus.Report{working, report(programstatus.Error, "agent claude exited")},
		},
		{
			name: "pending permission",
			before: func(t *testing.T, log *acpapi.Log) {
				_ = log.Prompt(nil)
				ask(t, log)
			},
			after: func(log *acpapi.Log) {
				answer(log)
				endTurn(log)
			},
			want: []programstatus.Report{blocked, working, report(programstatus.Done, "Turn completed.")},
		},
		{
			name:   "permission outside a turn",
			before: ask,
			after:  answer,
			want:   []programstatus.Report{blocked, report(programstatus.Done, "")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := newFakeManager([]string{"claude"})
			s := m.live(t, "brave-fox")
			tt.before(t, s.log)
			h, wm := newTestHandler(t, m)
			require.NoError(t, h.HandleCommand(context.Background(), textapi.Command{
				Name: Name, Args: []string{"open", "brave-fox"}, Window: window(1),
			}))
			require.NoError(t, wm.closeTab(wm.shownIn(window(1)).uri))
			if tt.ends {
				require.Eventually(t, func() bool { return s.snapshot().closed },
					10*time.Second, 10*time.Millisecond, "the session ends with its last tab")
				assert.Empty(t, wm.status.at(status))
				return
			}
			wm.status.waitReports(t, status, tt.want[0])
			tt.after(s.log)
			wm.status.waitReports(t, status, tt.want...)
			assert.False(t, s.snapshot().closed, "the session runs on")
		})
	}

	t.Run("the last of two tabs ends it", func(t *testing.T) {
		t.Parallel()
		m := newFakeManager([]string{"claude"})
		s := m.live(t, "brave-fox")
		h, wm := newTestHandler(t, m)
		for i, args := range [][]string{{"open", "brave-fox"}, {"open", "--new", "brave-fox"}} {
			require.NoError(t, h.HandleCommand(context.Background(), textapi.Command{
				Name: Name, Args: args, Window: window(i + 1),
			}))
		}
		require.NoError(t, wm.closeTab(wm.shownIn(window(1)).uri))
		_, followed := h.awayAt(mustURI(t, status))
		assert.False(t, followed, "the session still has a tab")
		assert.False(t, s.snapshot().closed)
		require.NoError(t, wm.closeTab(wm.shownIn(window(2)).uri))
		require.Eventually(t, func() bool { return s.snapshot().closed },
			10*time.Second, 10*time.Millisecond, "the session ends with its last tab")
	})
}

func TestHandlerAway(t *testing.T) {
	t.Parallel()
	const status = "acp://claude/brave-fox/session"
	working := programstatus.Report{State: programstatus.Working, Progress: -1, App: "brave-fox"}
	cleared := programstatus.Report{State: programstatus.Clear, Progress: -1}
	// away runs a turn in brave-fox and closes its tab.
	away := func(t *testing.T) (*Handler, *fakeWM, *fakeSession) {
		t.Helper()
		m := newFakeManager([]string{"claude"})
		s := m.live(t, "brave-fox")
		require.NoError(t, s.log.Prompt(nil))
		h, wm := newTestHandler(t, m)
		require.NoError(t, h.HandleCommand(context.Background(), textapi.Command{
			Name: Name, Args: []string{"open", "brave-fox"}, Window: window(1),
		}))
		require.NoError(t, wm.closeTab(wm.shownIn(window(1)).uri))
		wm.status.waitReports(t, status, working)
		return h, wm, s
	}
	run := func(t *testing.T, h *Handler, args ...string) {
		t.Helper()
		require.NoError(t, h.HandleCommand(context.Background(), textapi.Command{
			Name: Name, Args: args, Window: window(1),
		}))
	}

	t.Run("reopening withdraws the status", func(t *testing.T) {
		t.Parallel()
		h, wm, s := away(t)
		run(t, h, "open", "brave-fox")
		assert.Equal(t, "acp://claude/brave-fox", wm.shownIn(window(1)).uri.String())
		wm.status.waitReports(t, status, working, cleared)

		require.NoError(t, wm.closeTab(wm.shownIn(window(1)).uri))
		wm.status.waitReports(t, status, working, cleared, working)
		assert.False(t, s.snapshot().closed)
	})

	t.Run("acp close ends it", func(t *testing.T) {
		t.Parallel()
		h, wm, s := away(t)
		run(t, h, "close", "brave-fox")
		wm.status.waitReports(t, status, working, cleared)
		require.Eventually(t, func() bool { return s.snapshot().closed },
			10*time.Second, 10*time.Millisecond)
	})

	t.Run("ending on its own withdraws the status", func(t *testing.T) {
		t.Parallel()
		h, wm, s := away(t)
		s.log.Close(errors.New("agent claude exited"))
		wm.status.waitReports(t, status, working, cleared)
		ok, err := h.ShowStatus(mustURI(t, status))
		assert.False(t, ok, "nothing reports there any more")
		assert.NoError(t, err)
	})

	t.Run("ShowStatus opens it", func(t *testing.T) {
		t.Parallel()
		h, wm, _ := away(t)
		ok, err := h.ShowStatus(mustURI(t, "acp://claude/brave-fox"))
		assert.False(t, ok, "only its status uri")
		assert.NoError(t, err)

		ok, err = h.ShowStatus(mustURI(t, status))
		require.True(t, ok)
		require.NoError(t, err)
		shown := wm.shownIn(window(9))
		require.NotNil(t, shown, "in the focused window")
		assert.Equal(t, "acp://claude/brave-fox", shown.uri.String())
		wm.status.waitReports(t, status, working, cleared)
	})

	t.Run("CloseStatus ends it", func(t *testing.T) {
		t.Parallel()
		h, wm, s := away(t)
		ok, err := h.CloseStatus(mustURI(t, status))
		require.True(t, ok)
		require.NoError(t, err)
		wm.status.waitReports(t, status, working, cleared)
		require.Eventually(t, func() bool { return s.snapshot().closed },
			10*time.Second, 10*time.Millisecond)
	})
}

func TestHandlerRestyle(t *testing.T) {
	t.Parallel()
	const width, height = 60, 10
	h, wm := newTestHandler(t, newFakeManager([]string{"claude"}))
	newChat := func(name string, win window) *tab {
		t.Helper()
		require.NoError(t, h.HandleCommand(context.Background(), textapi.Command{
			Name: Name, Args: []string{"new", "claude", name}, Window: win,
		}))
		shown := wm.shownIn(win)
		require.NotNil(t, shown)
		shown.Resize(width, height)
		return shown
	}
	screen := func(tb *tab) string {
		w := term.NewStringWriter(width, height)
		tb.Draw(w)
		_ = w.Flush()
		return w.String()
	}
	inputBg := func(tb *tab, bg term.Color) bool {
		w := term.NewStringWriter(width, height)
		tb.Draw(w)
		for _, c := range w.Cells() {
			if c.Ch == '┌' {
				return c.Bg == bg
			}
		}
		return false
	}
	style := func(marker, bg string) config.Config {
		return config.MapConfig(map[string]any{
			"status_bar":             map[string]any{"layout": "{{ .Conversation }} " + marker},
			"input_background_color": bg,
		})
	}
	open := newChat("open", window(1))
	closed := newChat("closed", window(2))
	require.NoError(t, wm.closeTab(closed.uri))

	h.Restyle(style("@@", "red"), config.NopConfig())
	h.Restyle(style("##", "blue"), config.NopConfig())
	require.Eventually(t, func() bool { return strings.Contains(screen(open), "open ##") },
		10*time.Second, 10*time.Millisecond, "the open chat takes the last style:\n%s", screen(open))
	assert.True(t, inputBg(open, term.ColorBlue), "the open chat's compose box")
	next := newChat("next", window(3))
	assert.Contains(t, screen(next), "next ##", "a chat opened next")
	assert.True(t, inputBg(next, term.ColorBlue), "a chat opened next: compose box")
}

func TestHandlerClose(t *testing.T) {
	t.Parallel()
	m := newFakeManager([]string{"claude"}, "brave-fox")
	h, _ := newTestHandler(t, m)
	require.NoError(t, h.HandleCommand(context.Background(), textapi.Command{
		Name: Name, Args: []string{"close", "brave-fox"},
	}))
	require.Eventually(t, func() bool { return m.sessions["brave-fox"].snapshot().closed },
		10*time.Second, 10*time.Millisecond)

	err := h.HandleCommand(context.Background(), textapi.Command{
		Name: Name, Args: []string{"close", "nope"},
	})
	assert.ErrorIs(t, err, acpapi.ErrNotFound)
}

func TestHandlerUsage(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, newFakeManager(nil))
	tests := []struct {
		args []string
		want string
	}{
		{want: "usage: acp <subcommand> [<args>]"},
		{args: []string{"open"}, want: "usage: acp open [--new] <session>"},
		{args: []string{"open", "--new"}, want: "usage: acp open [--new] <session>"},
		{args: []string{"close", "a", "b"}, want: "usage: acp close <session>"},
		{args: []string{"restart"}, want: `acp: unknown subcommand "restart"`},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()
			err := h.HandleCommand(context.Background(), textapi.Command{Name: Name, Args: tt.args})
			assert.EqualError(t, err, tt.want)
		})
	}
}

func TestHandlerComplete(t *testing.T) {
	t.Parallel()
	h, _ := newTestHandler(t, newFakeManager([]string{"claude", "codex"}, "brave-fox", "calm-owl"))
	tests := []struct {
		args []string
		want []string
	}{
		{want: []string{"close", "new", "open"}},
		{args: []string{"o"}, want: []string{"open"}},
		{args: []string{"new", ""}, want: []string{"claude", "codex"}},
		{args: []string{"new", "cl"}, want: []string{"claude"}},
		{args: []string{"open", ""}, want: []string{"brave-fox", "calm-owl"}},
		{args: []string{"open", "-"}, want: []string{"--new"}},
		{args: []string{"open", "--new", "c"}, want: []string{"calm-owl"}},
		{args: []string{"close", "b"}, want: []string{"brave-fox"}},
		{args: []string{"new", "claude", ""}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()
			it, expanded, err := h.Complete(context.Background(), textapi.Command{Name: Name, Args: tt.args})
			require.NoError(t, err)
			assert.Empty(t, expanded)
			got, err := collect(context.Background(), it)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
