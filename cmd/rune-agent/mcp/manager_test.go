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

package mcp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/cmd/rune-agent/agent"
)

// newToolServer returns an MCP server whose tools answer "ok from <name>".
func newToolServer(toolNames ...string) *gomcp.Server {
	server := gomcp.NewServer(
		&gomcp.Implementation{Name: "test-server", Version: "v1.0.0"}, nil,
	)
	for _, name := range toolNames {
		addOKTool(server, name)
	}
	return server
}

func addOKTool(server *gomcp.Server, name string) {
	gomcp.AddTool(server, &gomcp.Tool{
		Name:        name,
		Description: "tool " + name,
	}, func(_ context.Context, _ *gomcp.CallToolRequest, _ any) (*gomcp.CallToolResult, any, error) {
		return &gomcp.CallToolResult{
			Content: []gomcp.Content{
				&gomcp.TextContent{Text: "ok from " + name},
			},
		}, nil, nil
	})
}

// testServer creates an MCP server with n tools and returns a transport
// factory that connects to it via in-memory transports.
func testServer(t *testing.T, toolNames ...string) (*gomcp.Server, TransportFactory) {
	t.Helper()

	server := newToolServer(toolNames...)

	var mu sync.Mutex
	var sessions []*gomcp.ServerSession

	factory := func(_ string, _ ServerConfig) (gomcp.Transport, error) {
		st, ct := gomcp.NewInMemoryTransports()
		ss, err := server.Connect(context.Background(), st, nil)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		sessions = append(sessions, ss)
		mu.Unlock()
		return ct, nil
	}

	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, ss := range sessions {
			_ = ss.Wait()
		}
	})

	return server, factory
}

// httpHandler serves server over streamable HTTP. A stateless handler
// speaks protocol 2026-07-28 as well as the legacy versions; a stateful
// one only the legacy versions.
func httpHandler(server *gomcp.Server, stateless bool) http.Handler {
	return gomcp.NewStreamableHTTPHandler(
		func(*http.Request) *gomcp.Server { return server },
		&gomcp.StreamableHTTPOptions{Stateless: stateless},
	)
}

func serveHTTP(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

// httpTestServer serves an MCP server with the given tools over
// streamable HTTP at /mcp.
func httpTestServer(t *testing.T, stateless bool, toolNames ...string) (*gomcp.Server, string) {
	t.Helper()
	server := newToolServer(toolNames...)
	mux := http.NewServeMux()
	mux.Handle("/mcp", httpHandler(server, stateless))
	return server, serveHTTP(t, mux).URL + "/mcp"
}

// sseTestServer serves an MCP server with the given tools over the
// legacy HTTP+SSE transport at /sse.
func sseTestServer(t *testing.T, toolNames ...string) (*gomcp.Server, string) {
	t.Helper()
	server := newToolServer(toolNames...)
	mux := http.NewServeMux()
	mux.Handle("/sse", gomcp.NewSSEHandler(func(*http.Request) *gomcp.Server { return server }, nil))
	return server, serveHTTP(t, mux).URL + "/sse"
}

// fastRetries shrinks the connect and reconnect backoff for the test.
func fastRetries(t *testing.T) {
	t.Helper()
	delays, attempts, delay := connectRetryDelays, reconnectAttempts, reconnectDelay
	connectRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	reconnectAttempts = 5
	reconnectDelay = time.Millisecond
	t.Cleanup(func() {
		connectRetryDelays, reconnectAttempts, reconnectDelay = delays, attempts, delay
	})
}

func toolByName(t *testing.T, tools []agent.Tool, name string) agent.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Definition().Function.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %s not found", name)
	return nil
}

func serverInfo(t *testing.T, m *Manager, name string) *ServerInfo {
	t.Helper()
	for _, s := range m.Servers() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("server %s not found", name)
	return nil
}

func TestManagerTransports(t *testing.T) {
	tests := []struct {
		name          string
		connect       func(t *testing.T) (*Manager, ServerConfig)
		wantTransport string
		wantProtocol  string
	}{
		{
			name: "stdio in memory",
			connect: func(t *testing.T) (*Manager, ServerConfig) {
				_, factory := testServer(t, "echo")
				return NewManagerWithTransport(factory), ServerConfig{Command: "srv"}
			},
			wantTransport: TransportStdio,
			wantProtocol:  "2026-07-28",
		},
		{
			name: "modern streamable HTTP",
			connect: func(t *testing.T) (*Manager, ServerConfig) {
				_, url := httpTestServer(t, true, "echo")
				return NewManager(nil, "", nil), ServerConfig{Type: "http", URL: url}
			},
			wantTransport: TransportHTTP,
			wantProtocol:  "2026-07-28",
		},
		{
			name: "legacy streamable HTTP",
			connect: func(t *testing.T) (*Manager, ServerConfig) {
				_, url := httpTestServer(t, false, "echo")
				return NewManager(nil, "", nil), ServerConfig{URL: url}
			},
			wantTransport: TransportHTTP,
			wantProtocol:  "2025-11-25",
		},
		{
			name: "legacy SSE",
			connect: func(t *testing.T) (*Manager, ServerConfig) {
				_, url := sseTestServer(t, "echo")
				return NewManager(nil, "", nil), ServerConfig{Type: "sse", URL: url}
			},
			wantTransport: TransportSSE,
			wantProtocol:  "2025-11-25",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, cfg := tt.connect(t)
			t.Cleanup(func() { _ = m.Close() })

			tools, err := m.ConnectServer(t.Context(), "srv", cfg)
			require.NoError(t, err)
			require.Len(t, tools, 1)

			info := serverInfo(t, m, "srv")
			assert.Equal(t, StatusConnected, info.Status)
			assert.Equal(t, tt.wantTransport, info.Transport)
			assert.Equal(t, tt.wantProtocol, info.ProtocolVersion)
			assert.Equal(t, "test-server", info.ServerName)
			assert.Equal(t, "v1.0.0", info.ServerVersion)
			if tt.wantTransport == TransportStdio {
				assert.Equal(t, "srv", info.Command)
				assert.Empty(t, info.URL)
			} else {
				assert.Equal(t, cfg.URL, info.URL)
				assert.Empty(t, info.Command)
			}

			// The SSE stream must outlive the connect deadline.
			if tt.wantTransport == TransportSSE {
				time.Sleep(50 * time.Millisecond)
			}
			res := toolByName(t, tools, "mcp__srv__echo").Execute(t.Context(), `{}`)
			require.False(t, res.IsError, res.Content)
			assert.Equal(t, "ok from echo", res.Content)
		})
	}
}

// requireBearer guards h with a bearer token check that accepts only
// token, answering with a challenge naming the resource metadata.
func requireBearer(h http.Handler, token, metadataURL string, scopes ...string) http.Handler {
	verify := func(_ context.Context, got string, _ *http.Request) (*auth.TokenInfo, error) {
		if got != token {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{Scopes: scopes, Expiration: time.Now().Add(time.Hour)}, nil
	}
	return auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: metadataURL,
		Scopes:              scopes,
	})(h)
}

func TestManagerNeedsAuth(t *testing.T) {
	server := newToolServer("echo")
	ts := serveHTTP(t, requireBearer(httpHandler(server, true), "secret", ""))

	m := NewManager(nil, "", nil)
	t.Cleanup(func() { _ = m.Close() })
	_, err := m.ConnectServer(t.Context(), "linear", ServerConfig{Type: "http", URL: ts.URL})
	require.ErrorIs(t, err, ErrNeedsAuth)

	info := serverInfo(t, m, "linear")
	assert.Equal(t, StatusNeedsAuth, info.Status)
	assert.Equal(t, "needs sign-in: run `agent mcp login linear`", info.Error)
}

func TestManagerStaticAuthorizationHeader(t *testing.T) {
	server := newToolServer("echo")
	ts := serveHTTP(t, requireBearer(httpHandler(server, true), "secret", ""))

	tests := []struct {
		name    string
		header  string
		wantErr string
	}{
		{name: "accepted", header: "Bearer secret"},
		{
			name:    "rejected without falling back to sign-in",
			header:  "Bearer wrong",
			wantErr: "server rejected the configured Authorization header (HTTP 401)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewManager(nil, "", nil)
			t.Cleanup(func() { _ = m.Close() })
			_, err := m.ConnectServer(t.Context(), "srv", ServerConfig{
				URL:     ts.URL,
				Headers: map[string]string{"authorization": tt.header},
			})
			info := serverInfo(t, m, "srv")
			if tt.wantErr == "" {
				require.NoError(t, err)
				assert.Equal(t, StatusConnected, info.Status)
				return
			}
			require.EqualError(t, err, tt.wantErr)
			assert.NotErrorIs(t, err, ErrNeedsAuth)
			assert.Equal(t, StatusError, info.Status)
		})
	}
}

func TestManagerSkipsCollidingToolNames(t *testing.T) {
	_, urlA := httpTestServer(t, true, "x", "y")
	_, urlB := httpTestServer(t, true, "x")

	m := NewManager(nil, "", nil)
	t.Cleanup(func() { _ = m.Close() })

	toolsA, err := m.ConnectServer(t.Context(), "a.b", ServerConfig{URL: urlA})
	require.NoError(t, err)
	toolsB, err := m.ConnectServer(t.Context(), "a_b", ServerConfig{URL: urlB})
	require.NoError(t, err)

	assert.Len(t, toolsA, 2)
	assert.Empty(t, toolsB, "mcp__a_b__x already belongs to server a.b")
	info := serverInfo(t, m, "a_b")
	assert.Equal(t, StatusConnected, info.Status)
	require.Len(t, info.Warnings, 1)
	assert.Contains(t, info.Warnings[0], `tool "x" skipped: its name mcp__a_b__x is taken by server "a.b"`)
}

func TestManagerToolNamesNeverShadowBuiltins(t *testing.T) {
	_, factory := testServer(t, "patch")
	m := NewManagerWithTransport(factory)
	t.Cleanup(func() { _ = m.Close() })

	tools, err := m.ConnectServer(t.Context(), "apply", ServerConfig{Command: "x"})
	require.NoError(t, err)
	require.Len(t, tools, 1)
	assert.Equal(t, "mcp__apply__patch", tools[0].Definition().Function.Name)
}

func TestManagerRegisterBlocksReconnect(t *testing.T) {
	_, url := httpTestServer(t, true, "echo")
	m := NewManager(nil, "", nil)
	t.Cleanup(func() { _ = m.Close() })

	untrusted := errors.New("not trusted: run `agent mcp trust srv`")
	m.Register("srv", ServerConfig{URL: url}, []string{"MISSING is not set"}, untrusted)
	info := serverInfo(t, m, "srv")
	assert.Equal(t, StatusError, info.Status)
	assert.Equal(t, untrusted.Error(), info.Error)
	assert.True(t, info.Blocked)
	assert.Equal(t, []string{"MISSING is not set"}, info.Warnings)
	assert.Equal(t, url, info.URL)

	require.ErrorIs(t, m.Reconnect(t.Context(), "srv"), untrusted)
	require.NoError(t, m.Reconnect(t.Context(), "all"), "all skips blocked servers")
	assert.Equal(t, StatusError, serverInfo(t, m, "srv").Status)

	var added []string
	m.OnToolsChanged = func(_ string, a []agent.Tool, _ []string) {
		for _, tool := range a {
			added = append(added, tool.Definition().Function.Name)
		}
	}
	m.Register("srv", ServerConfig{URL: url}, nil, nil)
	assert.False(t, serverInfo(t, m, "srv").Blocked)
	require.NoError(t, m.Reconnect(t.Context(), "srv"))
	assert.Equal(t, StatusConnected, serverInfo(t, m, "srv").Status)
	assert.Equal(t, []string{"mcp__srv__echo"}, added)

	m.Register("srv", ServerConfig{URL: url}, nil, untrusted)
	_, err := m.ConnectServer(t.Context(), "srv", ServerConfig{URL: url})
	require.NoError(t, err)
	assert.False(t, serverInfo(t, m, "srv").Blocked, "connecting the server unblocks it")
}

// flakyHandler answers requests with fail while it returns a status,
// and passes them to h otherwise.
type flakyHandler struct {
	h    http.Handler
	fail func(r *http.Request) int
}

func (f *flakyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if status := f.fail(r); status != 0 {
		http.Error(w, http.StatusText(status), status)
		return
	}
	f.h.ServeHTTP(w, r)
}

func TestManagerRetriesFirstConnect(t *testing.T) {
	fastRetries(t)
	server := newToolServer("echo")
	var failures atomic.Int32
	failures.Store(2)
	ts := serveHTTP(t, &flakyHandler{h: httpHandler(server, true), fail: func(*http.Request) int {
		if failures.Add(-1) >= 0 {
			return http.StatusServiceUnavailable
		}
		return 0
	}})

	m := NewManager(nil, "", nil)
	t.Cleanup(func() { _ = m.Close() })
	tools, err := m.ConnectServer(t.Context(), "srv", ServerConfig{URL: ts.URL})
	require.NoError(t, err)
	assert.Len(t, tools, 1)
}

func TestManagerDoesNotRetryClientErrors(t *testing.T) {
	fastRetries(t)
	var requests atomic.Int32
	ts := serveHTTP(t, &flakyHandler{fail: func(*http.Request) int {
		requests.Add(1)
		return http.StatusBadRequest
	}})

	m := NewManager(nil, "", nil)
	t.Cleanup(func() { _ = m.Close() })
	_, err := m.ConnectServer(t.Context(), "srv", ServerConfig{URL: ts.URL})
	require.Error(t, err)
	assert.LessOrEqual(t, requests.Load(), int32(2), "one discover and one initialize, no retries")
}

func TestManagerReconnectsAfterSessionMissing(t *testing.T) {
	fastRetries(t)
	server := newToolServer("echo")
	var dropSession atomic.Bool
	var dropped atomic.Value // the session ID answered with 404
	ts := serveHTTP(t, &flakyHandler{h: httpHandler(server, false), fail: func(r *http.Request) int {
		id := r.Header.Get("Mcp-Session-Id")
		if r.Method == http.MethodPost && id != "" && dropSession.CompareAndSwap(true, false) {
			dropped.Store(id)
		}
		if d, _ := dropped.Load().(string); d != "" && d == id {
			return http.StatusNotFound
		}
		return 0
	}})

	m := NewManager(nil, "", nil)
	t.Cleanup(func() { _ = m.Close() })
	changed := make(chan struct{}, 1)
	m.OnToolsChanged = func(string, []agent.Tool, []string) { changed <- struct{}{} }
	tools, err := m.ConnectServer(t.Context(), "srv", ServerConfig{URL: ts.URL})
	require.NoError(t, err)
	tool := toolByName(t, tools, "mcp__srv__echo")

	dropSession.Store(true)
	res := tool.Execute(t.Context(), `{}`)
	require.True(t, res.IsError, "the server forgot the session")

	require.Eventually(t, func() bool {
		res = tool.Execute(t.Context(), `{}`)
		return !res.IsError
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "ok from echo", res.Content, "existing adapters use the new session")
	assert.Equal(t, StatusConnected, serverInfo(t, m, "srv").Status)
	select {
	case <-changed:
		t.Fatal("an unchanged tool list must not be reported as a change")
	default:
	}
}

func TestManagerGivesUpAfterReconnectAttempts(t *testing.T) {
	fastRetries(t)
	server := newToolServer("echo")
	var down atomic.Bool
	var requests atomic.Int32
	ts := serveHTTP(t, &flakyHandler{h: httpHandler(server, true), fail: func(*http.Request) int {
		if down.Load() {
			requests.Add(1)
			return http.StatusNotFound
		}
		return 0
	}})

	m := NewManager(nil, "", nil)
	t.Cleanup(func() { _ = m.Close() })
	exits := make(chan string, 1)
	m.OnServerExit = func(name, detail string) { exits <- name + ": " + detail }
	tools, err := m.ConnectServer(t.Context(), "srv", ServerConfig{URL: ts.URL})
	require.NoError(t, err)

	down.Store(true)
	res := toolByName(t, tools, "mcp__srv__echo").Execute(t.Context(), `{}`)
	require.True(t, res.IsError)

	select {
	case detail := <-exits:
		assert.True(t, strings.HasPrefix(detail, "srv: disconnected after 5 reconnect attempts: "), detail)
	case <-time.After(10 * time.Second):
		t.Fatal("expected the server to be reported lost")
	}
	info := serverInfo(t, m, "srv")
	assert.Equal(t, StatusError, info.Status)
	assert.Contains(t, info.Error, "disconnected after 5 reconnect attempts")

	res = toolByName(t, tools, "mcp__srv__echo").Execute(t.Context(), `{}`)
	assert.Contains(t, res.Content, `MCP server "srv" is unavailable`)
}

func TestManagerFollowsToolListChanges(t *testing.T) {
	tests := []struct {
		name      string
		stateless bool
	}{
		{name: "modern HTTP", stateless: true},
		{name: "legacy HTTP", stateless: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, url := httpTestServer(t, tt.stateless, "keep", "drop")
			m := NewManager(nil, "", nil)
			t.Cleanup(func() { _ = m.Close() })
			type change struct {
				added   []string
				removed []string
			}
			changes := make(chan change, 4)
			m.OnToolsChanged = func(_ string, added []agent.Tool, removed []string) {
				var c change
				for _, tool := range added {
					c.added = append(c.added, tool.Definition().Function.Name)
				}
				c.removed = removed
				changes <- c
			}
			_, err := m.ConnectServer(t.Context(), "srv", ServerConfig{URL: url})
			require.NoError(t, err)

			addOKTool(server, "new")
			server.RemoveTools("drop")

			got := change{}
			deadline := time.After(5 * time.Second)
			for len(got.added) == 0 || len(got.removed) == 0 {
				select {
				case c := <-changes:
					got.added = append(got.added, c.added...)
					got.removed = append(got.removed, c.removed...)
				case <-deadline:
					t.Fatalf("tool list change not seen; got %+v", got)
				}
			}
			assert.Equal(t, []string{"mcp__srv__new"}, got.added)
			assert.Equal(t, []string{"mcp__srv__drop"}, got.removed)
			require.Eventually(t, func() bool {
				return fmt.Sprint(serverInfo(t, m, "srv").ToolNames) == "[keep new]"
			}, time.Second, 10*time.Millisecond)
		})
	}
}

func TestManagerConnect(t *testing.T) {
	_, factory := testServer(t, "tool_a", "tool_b")

	m := NewManagerWithTransport(factory)

	cfg := Config{
		MCPServers: map[string]ServerConfig{
			"srv": {Type: "stdio", Command: "dummy"},
		},
	}

	tools := m.Connect(context.Background(), cfg)
	require.Len(t, tools, 2)

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Definition().Function.Name] = true
	}
	assert.True(t, names["mcp__srv__tool_a"])
	assert.True(t, names["mcp__srv__tool_b"])

	require.NoError(t, m.Close())
}

func TestManagerConnectMultipleServers(t *testing.T) {
	_, factoryA := testServer(t, "alpha")
	_, factoryB := testServer(t, "beta")

	// Track which server name maps to which factory.
	factories := map[string]TransportFactory{
		"a": factoryA,
		"b": factoryB,
	}

	m := NewManagerWithTransport(func(name string, cfg ServerConfig) (gomcp.Transport, error) {
		return factories[name](name, cfg)
	})

	cfg := Config{
		MCPServers: map[string]ServerConfig{
			"a": {Type: "stdio", Command: "a-cmd"},
			"b": {Type: "stdio", Command: "b-cmd"},
		},
	}

	tools := m.Connect(context.Background(), cfg)
	require.Len(t, tools, 2)

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Definition().Function.Name] = true
	}
	assert.True(t, names["mcp__a__alpha"])
	assert.True(t, names["mcp__b__beta"])

	require.NoError(t, m.Close())
}

func TestManagerConnectSkipsFailedTransport(t *testing.T) {
	_, goodFactory := testServer(t, "ok_tool")

	m := NewManagerWithTransport(func(name string, cfg ServerConfig) (gomcp.Transport, error) {
		if name == "bad" {
			return nil, errors.New("transport creation failed")
		}
		return goodFactory(name, cfg)
	})

	cfg := Config{
		MCPServers: map[string]ServerConfig{
			"bad":  {Type: "stdio", Command: "bad-cmd"},
			"good": {Type: "stdio", Command: "good-cmd"},
		},
	}

	tools := m.Connect(context.Background(), cfg)
	// Only the good server's tool should be present.
	require.Len(t, tools, 1)
	assert.Equal(t, "mcp__good__ok_tool", tools[0].Definition().Function.Name)

	require.NoError(t, m.Close())
}

func TestManagerConnectEmptyConfig(t *testing.T) {
	m := NewManagerWithTransport(nil)
	tools := m.Connect(context.Background(), Config{})
	assert.Empty(t, tools)
	require.NoError(t, m.Close())
}

func TestManagerCloseWithoutConnect(t *testing.T) {
	m := NewManagerWithTransport(nil)
	require.NoError(t, m.Close())
}

func TestManagerServersConnected(t *testing.T) {
	_, factory := testServer(t, "tool_a", "tool_b")

	m := NewManagerWithTransport(factory)

	cfg := Config{
		MCPServers: map[string]ServerConfig{
			"srv": {Type: "stdio", Command: "my-cmd"},
		},
	}
	m.Connect(context.Background(), cfg)

	servers := m.Servers()
	require.Len(t, servers, 1)
	assert.Equal(t, "srv", servers[0].Name)
	assert.Equal(t, "my-cmd", servers[0].Command)
	assert.Equal(t, StatusConnected, servers[0].Status)
	assert.Equal(t, 2, servers[0].ToolCount)
	assert.Equal(t, []string{"tool_a", "tool_b"}, servers[0].ToolNames)
	assert.False(t, servers[0].ConnectedAt.IsZero())

	require.NoError(t, m.Close())
}

func TestManagerServersError(t *testing.T) {
	m := NewManagerWithTransport(func(_ string, _ ServerConfig) (gomcp.Transport, error) {
		return nil, errors.New("bad transport")
	})

	cfg := Config{
		MCPServers: map[string]ServerConfig{
			"broken": {Type: "stdio", Command: "nope"},
		},
	}
	m.Connect(context.Background(), cfg)

	servers := m.Servers()
	require.Len(t, servers, 1)
	assert.Equal(t, "broken", servers[0].Name)
	assert.Equal(t, StatusError, servers[0].Status)
	assert.Contains(t, servers[0].Error, "bad transport")
	assert.Equal(t, 0, servers[0].ToolCount)

	require.NoError(t, m.Close())
}

func TestManagerServersSortedByName(t *testing.T) {
	_, factoryA := testServer(t, "a1")
	_, factoryB := testServer(t, "b1")

	factories := map[string]TransportFactory{"z": factoryA, "a": factoryB}
	m := NewManagerWithTransport(func(name string, cfg ServerConfig) (gomcp.Transport, error) {
		return factories[name](name, cfg)
	})

	cfg := Config{
		MCPServers: map[string]ServerConfig{
			"z": {Type: "stdio", Command: "z-cmd"},
			"a": {Type: "stdio", Command: "a-cmd"},
		},
	}
	m.Connect(context.Background(), cfg)

	servers := m.Servers()
	require.Len(t, servers, 2)
	assert.Equal(t, "a", servers[0].Name)
	assert.Equal(t, "z", servers[1].Name)

	require.NoError(t, m.Close())
}

func TestManagerCloseDisconnectsServers(t *testing.T) {
	_, factory := testServer(t, "tool_a")

	m := NewManagerWithTransport(factory)

	cfg := Config{
		MCPServers: map[string]ServerConfig{
			"srv": {Type: "stdio", Command: "cmd"},
		},
	}
	m.Connect(context.Background(), cfg)

	servers := m.Servers()
	require.Len(t, servers, 1)
	assert.Equal(t, StatusConnected, servers[0].Status)

	require.NoError(t, m.Close())

	servers = m.Servers()
	require.Len(t, servers, 1)
	assert.Equal(t, StatusDisconnected, servers[0].Status)
}

func TestManagerServersEmpty(t *testing.T) {
	m := NewManagerWithTransport(nil)
	assert.Empty(t, m.Servers())
}

func TestManagerHeadersHelper(t *testing.T) {
	server := newToolServer("echo")
	var seen atomic.Value
	guarded := requireBearer(httpHandler(server, true), "secret", "")
	ts := serveHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("X-Leak"))
		guarded.ServeHTTP(w, r)
	}))
	t.Setenv("RUNE_TOKEN", "leaked")

	tests := []struct {
		name    string
		helper  string
		timeout time.Duration
		wantErr string
	}{
		{
			name:   "valid headers",
			helper: `printf '{"Authorization":"Bearer secret","X-Leak":"%s"}' "$RUNE_TOKEN"`,
		},
		{
			name:    "invalid JSON",
			helper:  `echo not-json`,
			wantErr: "headersHelper failed: output is not a JSON object of strings",
		},
		{
			name:    "failing command",
			helper:  `echo broken >&2; exit 3`,
			wantErr: "headersHelper failed: exit status 3: broken",
		},
		{
			name:    "timeout",
			helper:  `sleep 5`,
			timeout: 200 * time.Millisecond,
			wantErr: "headersHelper failed: timed out after 200ms",
		},
		{
			name: "rejected once, then rerun",
			helper: `n=$(cat count 2>/dev/null || echo 0); n=$((n+1)); echo $n > count
if [ "$n" -ge 2 ]; then echo '{"Authorization":"Bearer secret"}'; else echo '{"Authorization":"Bearer stale"}'; fi`,
		},
		{
			name:    "always rejected",
			helper:  `echo '{"Authorization":"Bearer stale"}'`,
			wantErr: "server rejected the headers from headersHelper (HTTP 401)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.timeout != 0 {
				restore := headersHelperTimeout
				headersHelperTimeout = tt.timeout
				t.Cleanup(func() { headersHelperTimeout = restore })
			}
			e := newLocalExec()
			dir := t.TempDir()
			m := NewManager(e, dir, nil)
			t.Cleanup(func() { _ = m.Close() })

			_, err := m.ConnectServer(t.Context(), "srv", ServerConfig{URL: ts.URL, HeadersHelper: tt.helper})
			started := e.started()
			require.NotEmpty(t, started, "the helper runs through the workspace executor")
			assert.Equal(t, "sh", started[0].Path)
			assert.Equal(t, dir, started[0].Dir)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.NotErrorIs(t, err, ErrNeedsAuth, "a helper never falls back to sign-in")
				assert.Equal(t, StatusError, serverInfo(t, m, "srv").Status)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, StatusConnected, serverInfo(t, m, "srv").Status)
			if tt.name == "valid headers" {
				assert.Equal(t, "", seen.Load(), "Rune's credentials never reach the helper")
			}
		})
	}
}

func TestManagerRetryable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	_, refused := http.Get("http://" + addr)
	require.Error(t, refused)

	tests := []struct {
		name   string
		status int
		ch     *challenge
		err    error
		want   bool
	}{
		{name: "connection refused", err: refused, want: true},
		{name: "handshake timeout", err: fmt.Errorf("connect: %w", context.DeadlineExceeded), want: true},
		{name: "server error", status: http.StatusBadGateway, err: errors.New("x"), want: true},
		{name: "rate limited", status: http.StatusTooManyRequests, err: errors.New("x"), want: true},
		{name: "client error", status: http.StatusNotFound, err: errors.New("x")},
		{name: "challenge", ch: &challenge{status: http.StatusUnauthorized}, err: errors.New("x")},
		{name: "needs sign-in", err: &needsAuthError{name: "x"}},
		{name: "other", err: errors.New("x")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &serverEntry{auth: &authState{lastStatus: tt.status, challenge: tt.ch}}
			assert.Equal(t, tt.want, NewManagerWithTransport(nil).retryable(t.Context(), e, tt.err))
		})
	}
}
