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

package agentshell

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/agent/audit"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/mcp"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestShellFloatCloseLeavesItsWindow(t *testing.T) {
	t.Parallel()
	const id = "rolling-fox"
	storage := storagestub.NewInMemoryService()
	auditStore := audit.NewStore(storage)
	auditStore.Append(context.Background(), id, audit.Entry{
		StartedAt: time.Now(),
		Messages:  []llmapi.Message{{Role: llmapi.RoleUser, Content: "question"}},
	})
	for _, tc := range []struct {
		name string
		show func(s *shell) (iterator.Iterator[component.Responsive], error)
	}{
		{"history", func(s *shell) (iterator.Iterator[component.Responsive], error) {
			return s.showHistory(context.Background(), id)
		}},
		{"audit", func(s *shell) (iterator.Iterator[component.Responsive], error) {
			return s.showAudit(context.Background(), id)
		}},
		{"skill", func(s *shell) (iterator.Iterator[component.Responsive], error) {
			return s.showSkill("explore")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			wm := &floatingWindowManager{}
			s := &shell{
				wm:         wm,
				auditStore: auditStore,
				store: &compactDialogueStore{dialogue: dialoguemanager.Dialogue{
					ID: id,
					Messages: []llmapi.Message{
						{Role: llmapi.RoleUser, Content: "question"},
					},
				}},
				skillRegistry: skills.NewRegistry(osFS{}, dirURI(t.TempDir()), nil, nil),
			}
			_, err := tc.show(s)
			require.NoError(t, err)
			require.NotNil(t, wm.floated)

			require.NoError(t, wm.floated.Close())

			assert.Zero(t, wm.closeWindowCalls,
				"the window may already show other content when the view is released")
		})
	}
}

// floatingWindowManager records the last floating window opened and
// every CloseWindow call.
type floatingWindowManager struct {
	stubWindowManager
	floated          browserapi.Floating
	closeWindowCalls int
}

func (m *floatingWindowManager) Floating(
	f browserapi.Floating, _ browserapi.FloatingConfig,
) (browserapi.Window, error) {
	m.floated = f
	return nil, nil
}

func (m *floatingWindowManager) CloseWindow(browserapi.Window) error {
	m.closeWindowCalls++
	return nil
}

type forkPickerCalls struct{ selected, closed int }

func newPointerLeaveForkPicker(calls *forkPickerCalls) *forkPickerHandler {
	candidates := []forkCandidate{
		{messageIndex: 0, label: "user: Fix the off-by-one in alpha.go", preview: "Fix the off-by-one in **alpha.go**"},
		{messageIndex: 1, label: "assistant: The index is off by one", preview: "The index is off by one:\n\n" +
			"```go\nfunc Last(s []int) int {\n\treturn s[len(s)-1]\n}\n```"},
		{messageIndex: 3, label: "user: Also add a test for the empty slice case", preview: "Also add a test"},
		{messageIndex: 4, label: "assistant: Added TestLastEmpty", preview: "**Tool Call:** apply_patch\n```\n{}\n```"},
	}
	h := newForkPickerHandler(candidates,
		func(forkCandidate) error { calls.selected++; return nil },
		func() error { calls.closed++; return nil })
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	return h
}

func TestForkPickerHandlerPointerLeaves(t *testing.T) {
	t.Parallel()
	for _, size := range []struct{ width, height int }{{60, 15}, {80, 30}, {1, 1}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			t.Parallel()
			calls := &forkPickerCalls{}
			handlertest.RunPointerLeaves(t, newPointerLeaveForkPicker(calls), size.width, size.height)
			assert.Equal(t, forkPickerCalls{}, *calls)
		})
	}
}

func TestForkPickerHandlerPointerLeaveKeepsFocus(t *testing.T) {
	t.Parallel()
	const width, height = 80, 30
	mouse := func(key term.Key, x, y int) term.Event {
		return term.Event{
			Type: term.EventMouse, Key: key, MouseX: x, MouseY: y,
			Context: term.ContextWithPointer(context.Background(), new(term.Pointer)),
		}
	}
	for _, tc := range []struct {
		name    string
		gesture []term.Event
	}{
		{name: "nothing"},
		{name: "hover", gesture: []term.Event{mouse(0, 5, 17)}},
		{name: "click", gesture: []term.Event{mouse(term.MouseLeft, 5, 18), mouse(term.MouseRelease, 5, 18)}},
		{name: "press", gesture: []term.Event{mouse(term.MouseLeft, 5, 19)}},
		{name: "wheel", gesture: []term.Event{mouse(term.MouseWheelDown, 5, 17)}},
	} {
		for _, leave := range []term.Coordinates{
			{X: -1, Y: 17}, {X: width, Y: 17}, {X: 5, Y: -1}, {X: 5, Y: height}, {X: 500, Y: 500},
		} {
			t.Run(fmt.Sprintf("%s then %v", tc.name, leave), func(t *testing.T) {
				t.Parallel()
				calls := &forkPickerCalls{}
				h := newPointerLeaveForkPicker(calls)
				h.Resize(width, height)
				for _, ev := range tc.gesture {
					h.Handle(ev)
				}
				before, _ := h.Selection()

				exit, _ := h.Handle(term.Event{Type: term.EventMouse, MouseX: leave.X, MouseY: leave.Y})
				h.Draw(term.NewStringWriter(width, height))

				after, ok := h.Selection()
				assert.False(t, exit)
				assert.True(t, ok)
				assert.Equal(t, before, after)
				assert.Equal(t, "assistant: The index is off by one", after)
				assert.Equal(t, forkPickerCalls{}, *calls)
			})
		}
	}
}

// fakeMCP is an MCPInfo that records the commands it receives.
type fakeMCP struct {
	mu      sync.Mutex
	servers []*mcp.ServerInfo
	calls   []string
	err     error
	// login, if set, runs in place of a sign-in.
	login func(ctx context.Context, out func(string)) error
}

func (f *fakeMCP) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	return f.err
}

func (f *fakeMCP) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeMCP) Servers() []*mcp.ServerInfo { return f.servers }

func (f *fakeMCP) Login(ctx context.Context, name string, out func(string)) error {
	if err := f.record("login " + name); err != nil {
		return err
	}
	if f.login != nil {
		return f.login(ctx, out)
	}
	return nil
}

func (f *fakeMCP) CompleteLogin(name, redirectURL string) error {
	return f.record("complete " + name + " " + redirectURL)
}

func (f *fakeMCP) Logout(_ context.Context, name string) error {
	return f.record("logout " + name)
}

func (f *fakeMCP) Reconnect(_ context.Context, name string) error {
	return f.record("reconnect " + name)
}

func (f *fakeMCP) Trust(_ context.Context, name string) error {
	return f.record("trust " + name)
}

// renderText draws r on a wide screen and returns its words.
func renderText(t *testing.T, r component.Responsive) string {
	t.Helper()
	const width = 400
	height := r.Height(width)
	r.Resize(width, height)
	w := term.NewStringWriter(width, height)
	r.Draw(w)
	require.NoError(t, w.Flush())
	return strings.Join(strings.Fields(w.String()), " ")
}

func drainText(t *testing.T, it iterator.Iterator[component.Responsive]) string {
	t.Helper()
	defer func() { _ = it.Close() }()
	var parts []string
	for {
		r, ok := it.Next(context.Background())
		if !ok {
			require.NoError(t, it.Err())
			return strings.Join(parts, "\n")
		}
		parts = append(parts, renderText(t, r))
	}
}

func TestHandleMCP(t *testing.T) {
	t.Parallel()

	docs := &mcp.ServerInfo{Name: "docs", Status: mcp.StatusConnected, ToolCount: 3}
	tests := []struct {
		name      string
		args      []string
		err       error
		wantCalls []string
		wantText  string
		wantErr   string
	}{
		{name: "no args lists servers", wantText: "MCP Servers"},
		{name: "list", args: []string{"list"}, wantText: "MCP Servers"},
		{name: "logout", args: []string{"logout", "docs"}, wantCalls: []string{"logout docs"}, wantText: "Signed out of docs."},
		{name: "reconnect", args: []string{"reconnect", "docs"}, wantCalls: []string{"reconnect docs"}, wantText: "docs is connected: 3 tools available."},
		{name: "reconnect all", args: []string{"reconnect", "all"}, wantCalls: []string{"reconnect all"}, wantText: "MCP Servers"},
		{name: "trust", args: []string{"trust", "docs"}, wantCalls: []string{"trust docs"}, wantText: "docs is connected: 3 tools available."},
		{
			name:      "login callback",
			args:      []string{"login", "docs", "--callback", "http://127.0.0.1:1234/callback?code=c&state=s"},
			wantCalls: []string{"complete docs http://127.0.0.1:1234/callback?code=c&state=s"},
			wantText:  "Finishing the sign-in to docs.",
		},
		{name: "login usage", args: []string{"login"}, wantErr: "usage: mcp login <name> [--callback <redirect-url>]"},
		{name: "login bad flag", args: []string{"login", "docs", "--cb", "x"}, wantErr: "usage: mcp login"},
		{name: "logout usage", args: []string{"logout"}, wantErr: "usage: mcp logout <name>"},
		{name: "reconnect usage", args: []string{"reconnect"}, wantErr: "usage: mcp reconnect <name|all>"},
		{name: "trust usage", args: []string{"trust", "a", "b"}, wantErr: "usage: mcp trust <name>"},
		{name: "unknown subcommand", args: []string{"bogus"}, wantErr: "usage: mcp [list|login"},
		{
			name:      "errors propagate",
			args:      []string{"trust", "docs"},
			err:       errors.New("no MCP server named \"docs\""),
			wantCalls: []string{"trust docs"},
			wantErr:   "no MCP server named",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info := &fakeMCP{servers: []*mcp.ServerInfo{docs}, err: tt.err}
			s := &shell{mcpInfo: info}

			it, err := s.handleMCP(context.Background(), tt.args)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				require.NoError(t, err)
				assert.Contains(t, drainText(t, it), tt.wantText)
			}
			assert.Equal(t, tt.wantCalls, info.recorded())
		})
	}
}

func TestHandleMCPWithoutServers(t *testing.T) {
	t.Parallel()
	s := &shell{}
	it, err := s.handleMCP(context.Background(), nil)
	require.NoError(t, err)
	assert.Contains(t, drainText(t, it), "(no MCP servers configured)")
	_, err = s.handleMCP(context.Background(), []string{"login", "docs"})
	assert.EqualError(t, err, "no MCP servers configured")
}

func TestMCPLoginStreamsSignIn(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	info := &fakeMCP{
		servers: []*mcp.ServerInfo{{Name: "docs", Status: mcp.StatusConnected, ToolCount: 2}},
		login: func(_ context.Context, out func(string)) error {
			out("https://auth.example.com/authorize?state=s")
			<-release
			return nil
		},
	}
	s := &shell{mcpInfo: info}
	it, err := s.handleMCP(context.Background(), []string{"login", "docs"})
	require.NoError(t, err)
	defer func() { _ = it.Close() }()

	r, ok := it.Next(context.Background())
	require.True(t, ok)
	page := renderText(t, r)
	assert.Contains(t, page, "Opening your browser to sign in to docs")
	assert.Contains(t, page, "https://auth.example.com/authorize?state=s")
	assert.Contains(t, page, "agent mcp login docs --callback <address>")

	close(release)
	r, ok = it.Next(context.Background())
	require.True(t, ok)
	assert.Equal(t, "Signed in to docs: 2 tools available.", renderText(t, r))
	_, ok = it.Next(context.Background())
	assert.False(t, ok)
	require.NoError(t, it.Err())
}

func TestMCPLoginOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		login   func(ctx context.Context, out func(string)) error
		want    string
		wantErr string
	}{
		{
			name:  "connects without signing in",
			login: func(context.Context, func(string)) error { return nil },
			want:  "docs is connected: 2 tools available.",
		},
		{
			name: "sign-in fails",
			login: func(_ context.Context, out func(string)) error {
				out("https://auth.example.com/authorize")
				return errors.New("sign-in timed out after 5m0s")
			},
			want:    "Opening your browser to sign in to docs",
			wantErr: "sign-in timed out after 5m0s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info := &fakeMCP{
				servers: []*mcp.ServerInfo{{Name: "docs", Status: mcp.StatusConnected, ToolCount: 2}},
				login:   tt.login,
			}
			s := &shell{mcpInfo: info}
			it, err := s.handleMCP(context.Background(), []string{"login", "docs"})
			require.NoError(t, err)
			defer func() { _ = it.Close() }()

			var texts []string
			for {
				r, ok := it.Next(context.Background())
				if !ok {
					break
				}
				texts = append(texts, renderText(t, r))
			}
			assert.Contains(t, strings.Join(texts, "\n"), tt.want)
			if tt.wantErr != "" {
				assert.ErrorContains(t, it.Err(), tt.wantErr)
			} else {
				assert.NoError(t, it.Err())
			}
		})
	}
}

func TestMCPLoginCloseCancelsSignIn(t *testing.T) {
	t.Parallel()

	exited := make(chan error, 1)
	info := &fakeMCP{login: func(ctx context.Context, out func(string)) error {
		out("https://auth.example.com/authorize")
		<-ctx.Done()
		exited <- ctx.Err()
		return ctx.Err()
	}}
	s := &shell{mcpInfo: info}
	it, err := s.handleMCP(context.Background(), []string{"login", "docs"})
	require.NoError(t, err)
	_, ok := it.Next(context.Background())
	require.True(t, ok)

	require.NoError(t, it.Close())
	select {
	case err := <-exited:
		assert.ErrorIs(t, err, context.Canceled)
	default:
		t.Fatal("Close must wait for the abandoned sign-in to stop")
	}
}

func TestCompleteMCP(t *testing.T) {
	t.Parallel()

	info := &fakeMCP{servers: []*mcp.ServerInfo{{Name: "docs"}, {Name: "search"}}}
	tests := []struct {
		args []string
		want []string
	}{
		{args: []string{""}, want: []string{"list", "login", "logout", "reconnect", "trust"}},
		{args: []string{"lo"}, want: []string{"login", "logout"}},
		{args: []string{"login", ""}, want: []string{"docs", "search"}},
		{args: []string{"reconnect", ""}, want: []string{"all", "docs", "search"}},
		{args: []string{"trust", "s"}, want: []string{"search"}},
		{args: []string{"list", ""}},
		{args: []string{"bogus", ""}},
		{args: []string{"login", "docs", "--"}, want: []string{"--callback"}},
		{args: []string{"logout", "docs", ""}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()
			s := &shell{mcpInfo: info}
			it, err := s.Complete(context.Background(), "mcp", tt.args)
			require.NoError(t, err)
			var got []string
			for {
				v, ok := it.Next(context.Background())
				if !ok {
					break
				}
				got = append(got, v)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFormatMCPServers(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name    string
		servers []*mcp.ServerInfo
		want    []string
	}{
		{
			name: "no servers",
			want: []string{"*(no MCP servers configured)*"},
		},
		{
			name: "stdio server",
			servers: []*mcp.ServerInfo{{
				Name: "fs", Transport: mcp.TransportStdio, Command: "npx",
				Status: mcp.StatusConnected, ToolCount: 4, ConnectedAt: now.Add(-90 * time.Second),
			}},
			want: []string{
				"### fs", "- Status: connected", "- Transport: stdio", "- Command: `npx`",
				"- Tools: 4", "- Uptime: 1m",
			},
		},
		{
			name: "remote server",
			servers: []*mcp.ServerInfo{{
				Name: "docs", Transport: mcp.TransportHTTP, URL: "https://mcp.example.com/mcp",
				Status: mcp.StatusConnected, ProtocolVersion: "2025-11-25",
				ServerName: "Example Docs", ServerVersion: "1.2.0",
				Warnings: []string{"environment variable DOCS_TOKEN is not set"},
			}},
			want: []string{
				"- Transport: http", "- URL: `https://mcp.example.com/mcp`",
				"- Server: Example Docs 1.2.0", "- Protocol: 2025-11-25",
				"- Warning: environment variable DOCS_TOKEN is not set",
			},
		},
		{
			name: "needs sign-in",
			servers: []*mcp.ServerInfo{{
				Name: "docs", Transport: mcp.TransportHTTP, URL: "https://mcp.example.com/mcp",
				Status: mcp.StatusNeedsAuth,
			}},
			want: []string{"- Status: needs sign-in: run `agent mcp login docs`"},
		},
		{
			name: "not trusted",
			servers: []*mcp.ServerInfo{{
				Name: "docs", Transport: mcp.TransportSSE, URL: "https://mcp.example.com/sse",
				Status: mcp.StatusError, Error: "not trusted: run `agent mcp trust docs`",
			}},
			want: []string{"- Status: error: not trusted: run `agent mcp trust docs`", "- Transport: sse"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatMCPServers(tt.servers, now)
			for _, w := range tt.want {
				assert.Contains(t, got, w)
			}
		})
	}
}
