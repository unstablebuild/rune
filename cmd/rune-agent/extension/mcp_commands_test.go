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

package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	runemcp "unstable.build/rune/cmd/rune-agent/mcp"
)

// serveMCP serves an MCP server with one tool, search, at /mcp of a
// loopback server. guard, if set, wraps the MCP endpoint.
func serveMCP(t *testing.T, mux *http.ServeMux, guard func(http.Handler) http.Handler) string {
	t.Helper()
	server := gomcp.NewServer(&gomcp.Implementation{Name: "docs", Version: "v1.2.0"}, nil)
	gomcp.AddTool(server, &gomcp.Tool{Name: "search", Description: "search the docs"},
		func(context.Context, *gomcp.CallToolRequest, any) (*gomcp.CallToolResult, any, error) {
			return &gomcp.CallToolResult{Content: []gomcp.Content{&gomcp.TextContent{Text: "found"}}}, nil, nil
		})
	var h http.Handler = gomcp.NewStreamableHTTPHandler(
		func(*http.Request) *gomcp.Server { return server },
		&gomcp.StreamableHTTPOptions{Stateless: true})
	if guard != nil {
		h = guard(h)
	}
	mux.Handle("/mcp", h)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL
}

// newMCPTestHandler returns a handler that connects MCP servers the way
// a workspace at cwd does, with the user answering trust prompts with
// answers.
func newMCPTestHandler(
	t *testing.T, cwd string, servers map[string]runemcp.ServerConfig, answers ...int,
) (*aiEditorHandler, *capturingNotifications, *answeringPrompter) {
	t.Helper()
	noti := &capturingNotifications{}
	prompter := &answeringPrompter{answers: answers}
	h := &aiEditorHandler{
		mcpManager:   runemcp.NewManager(nil, t.TempDir(), nil),
		mcpTrust:     newMCPTrust(storagestub.NewInMemoryService(), prompter),
		mcpServers:   servers,
		cwd:          mustURI(t, cwd),
		n:            noti,
		toolRegistry: agent.NewRegistry(),
	}
	h.ctx, h.cancelCtx = context.WithCancel(context.Background())
	h.wireMCPManager()
	t.Cleanup(func() {
		h.cancelCtx()
		_ = h.mcpManager.Close()
	})
	return h, noti, prompter
}

func noteMessages(noti *capturingNotifications) []string {
	var msgs []string
	for _, n := range noti.captured() {
		msgs = append(msgs, n.msg)
	}
	return msgs
}

func TestMCPCommandsTrustConnectsDeclinedServer(t *testing.T) {
	t.Parallel()

	base := serveMCP(t, http.NewServeMux(), nil)
	servers := workspaceServers(t, map[string]any{
		"docs": map[string]any{"type": "http", "url": base + "/mcp"},
	})
	h, _, prompter := newMCPTestHandler(t, "file:///work/repo", servers, 2, 0)
	cmds := mcpCommands{Manager: h.mcpManager, h: h}
	ctx := context.Background()

	h.connectMCPServers(servers)
	err := cmds.Reconnect(ctx, "docs")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not trusted: run `agent mcp trust docs`",
		"reconnecting must not bypass the declined trust")

	require.NoError(t, cmds.Trust(ctx, "docs"))

	assert.Equal(t, 2, prompter.prompts())
	assert.Equal(t, runemcp.StatusConnected, h.mcpManager.Servers()[0].Status)
	_, ok := h.toolRegistry.Get("mcp__docs__search", "")
	assert.True(t, ok, "the trusted server's tools must reach the agent")
	assert.Error(t, cmds.Trust(ctx, "nope"))
}

// signInOpener stands in for the browser: shown pages are fetched,
// following redirects back to Rune's sign-in callback.
type signInOpener struct {
	mu    sync.Mutex
	shown []string
	errs  chan error
}

func (o *signInOpener) Open(workspaceapi.URI) (browserapi.Handler, error) {
	return nil, errors.New("unexpected Open")
}

func (o *signInOpener) Show(uri workspaceapi.URI) error {
	o.mu.Lock()
	o.shown = append(o.shown, uri.String())
	o.mu.Unlock()
	go func() {
		resp, err := http.Get(uri.String())
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("sign-in ended with %s", resp.Status)
			}
		}
		o.errs <- err
	}()
	return nil
}

// signInMux serves a minimal authorization server that grants every
// sign-in, and guards the MCP endpoint with the token it issues.
func signInMux(t *testing.T) (*http.ServeMux, func(http.Handler) http.Handler) {
	t.Helper()
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		origin := "http://" + r.Host
		writeJSON(w, map[string]any{
			"resource":              origin + "/mcp",
			"authorization_servers": []string{origin},
		})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		origin := "http://" + r.Host
		writeJSON(w, map[string]any{
			"issuer":                                origin,
			"authorization_endpoint":                origin + "/authorize",
			"token_endpoint":                        origin + "/token",
			"response_types_supported":              []string{"code"},
			"code_challenge_methods_supported":      []string{"S256"},
			"client_id_metadata_document_supported": true,
		})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=granted&state="+q.Get("state"), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("code") != "granted" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"access_token": "signed-in", "token_type": "Bearer", "expires_in": 3600})
	})
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer signed-in" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(
					"Bearer resource_metadata=%q", "http://"+r.Host+"/.well-known/oauth-protected-resource/mcp"))
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	return mux, guard
}

func TestMCPCommandsLoginSignsInThroughTheBrowser(t *testing.T) {
	t.Parallel()

	mux, guard := signInMux(t)
	base := serveMCP(t, mux, guard)
	declared, err := runemcp.ParseServers(map[string]any{
		"docs": map[string]any{"type": "http", "url": base + "/mcp"},
	})
	require.NoError(t, err)
	servers := runemcp.Merge(declared, nil)
	h, noti, _ := newMCPTestHandler(t, "file:///work/repo", servers)
	opener := &signInOpener{errs: make(chan error, 1)}
	h.o = opener
	cmds := mcpCommands{Manager: h.mcpManager, h: h}

	h.connectMCPServers(servers)
	require.Equal(t, runemcp.StatusNeedsAuth, h.mcpManager.Servers()[0].Status)
	assert.Contains(t, noteMessages(noti),
		"MCP server \"docs\" needs sign-in: run `agent mcp login docs`")

	var printed []string
	require.NoError(t, cmds.Login(context.Background(), "docs", func(u string) {
		printed = append(printed, u)
	}))
	require.NoError(t, <-opener.errs)

	require.Len(t, printed, 1)
	assert.Equal(t, printed, opener.shown, "the browser opens the page the user is shown")
	assert.True(t, strings.HasPrefix(printed[0], base+"/authorize?"), printed[0])
	assert.Equal(t, runemcp.StatusConnected, h.mcpManager.Servers()[0].Status)
	_, ok := h.toolRegistry.Get("mcp__docs__search", "")
	assert.True(t, ok, "signing in must bring the server's tools to the agent")
}
