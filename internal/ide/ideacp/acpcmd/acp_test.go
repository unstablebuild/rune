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
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
)

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
		out = append(out, s.info)
	}
	slices.SortFunc(out, func(a, b acpapi.SessionInfo) int { return strings.Compare(a.Name, b.Name) })
	return iterator.FromSlice(out)
}

func (m *fakeManager) OpenSession(_ context.Context, name string) (acpapi.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[name]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("acp session %q: %w", name, acpapi.ErrNotFound)
}

func (m *fakeManager) createdSessions() []acpapi.NewSessionRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.created)
}

type window uint64

func (w window) WindowID() uint64 { return uint64(w) }

// fakeWM keeps a tab per URI like the browser does, and records which tab
// each window shows.
type fakeWM struct {
	browserapi.WindowManager
	t *testing.T

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

func newTestHandler(t *testing.T, m Manager) (*Handler, *fakeWM) {
	t.Helper()
	wm := &fakeWM{
		t:     t,
		tabs:  make(map[workspaceapi.URI]*tab),
		shown: make(map[browserapi.Window]*tab),
	}
	return New(m, Config{
		WindowManager: wm,
		Interrupter:   term.NopInterrupter(),
		Root:          mustURI(t, "file:///work"),
		Chat:          dialoguetui.DefaultComponentConfig(),
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
		h, wm := newTestHandler(t, newFakeManager([]string{"claude"}, "brave-fox"))
		open := textapi.Command{Name: Name, Args: []string{"open", "brave-fox"}, Window: window(1)}
		require.NoError(t, h.HandleCommand(context.Background(), open))
		first := wm.shownIn(window(1))
		require.NoError(t, wm.closeTab(first.uri))

		require.NoError(t, h.HandleCommand(context.Background(), open))
		second := wm.shownIn(window(1))
		assert.NotSame(t, first, second)
		assert.NotSame(t, first.Handler, second.Handler)
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
	style := func(marker string) config.Config {
		return config.MapConfig(map[string]any{
			"status_bar": map[string]any{"layout": "{{ .Conversation }} " + marker},
		})
	}
	open := newChat("open", window(1))
	closed := newChat("closed", window(2))
	require.NoError(t, wm.closeTab(closed.uri))

	h.Restyle(style("@@"), config.NopConfig())
	h.Restyle(style("##"), config.NopConfig())
	require.Eventually(t, func() bool { return strings.Contains(screen(open), "open ##") },
		10*time.Second, 10*time.Millisecond, "the open chat takes the last style:\n%s", screen(open))
	assert.Contains(t, screen(newChat("next", window(3))), "next ##", "a chat opened next")
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
		{args: []string{"open"}, want: "usage: acp open <session>"},
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
