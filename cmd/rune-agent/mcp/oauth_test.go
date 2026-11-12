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
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"unstable.build/rune/cmd/rune-agent/agent"
)

// fakeAuthServer is an OAuth authorization server guarding an MCP
// server, which it serves from another origin.
type fakeAuthServer struct {
	t      *testing.T
	as     *httptest.Server
	mcpURL string

	mu sync.Mutex
	// What the server advertises.
	cimd, dcr, issSupported bool
	scopes                  []string
	// requiredScopes are the scopes the MCP server demands.
	requiredScopes []string
	// wrongIss makes the authorization response name another issuer.
	wrongIss bool
	// refreshFails makes every refresh fail with invalid_grant.
	refreshFails bool
	accessTTL    time.Duration

	clients       map[string]string // registered client ID -> redirect URI
	registrations int
	codes         map[string]grant
	access        map[string]grant
	refresh       map[string]grant
	refreshes     int
	revoked       []string
	authorized    []url.Values
}

type grant struct {
	clientID, redirect, challenge, resource string
	scopes                                  []string
	expiry                                  time.Time
}

func newFakeAuthServer(t *testing.T) *fakeAuthServer {
	t.Helper()
	f := &fakeAuthServer{
		t:         t,
		dcr:       true,
		scopes:    []string{"read"},
		accessTTL: time.Hour,
		clients:   make(map[string]string),
		codes:     make(map[string]grant),
		access:    make(map[string]grant),
		refresh:   make(map[string]grant),
	}
	asMux := http.NewServeMux()
	asMux.HandleFunc("/.well-known/oauth-authorization-server", f.metadata)
	asMux.HandleFunc("/authorize", f.authorize)
	asMux.HandleFunc("/token", f.token)
	asMux.HandleFunc("/register", f.register)
	asMux.HandleFunc("/revoke", f.revoke)
	f.as = serveHTTP(t, asMux)

	server := newToolServer("echo")
	mcpMux := http.NewServeMux()
	mcpTS := serveHTTP(t, mcpMux)
	f.mcpURL = mcpTS.URL + "/mcp"
	prmURL := mcpTS.URL + "/.well-known/oauth-protected-resource/mcp"
	mcpMux.Handle("/.well-known/oauth-protected-resource/mcp", auth.ProtectedResourceMetadataHandler(
		&oauthex.ProtectedResourceMetadata{
			Resource:             f.mcpURL,
			AuthorizationServers: []string{f.as.URL},
			ScopesSupported:      []string{"read"},
		}))
	mcpMux.Handle("/mcp", f.guard(httpHandler(server, true), prmURL))
	return f
}

func (f *fakeAuthServer) issuer() string { return f.as.URL }

// guard admits requests whose bearer token the authorization server
// issued with every required scope.
func (f *fakeAuthServer) guard(h http.Handler, prmURL string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		g, ok := f.access[token]
		required := f.requiredScopes
		f.mu.Unlock()
		if !ok || time.Now().After(g.expiry) {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf("Bearer resource_metadata=%q", prmURL))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		for _, s := range required {
			if !slices.Contains(g.scopes, s) {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(
					"Bearer error=%q, scope=%q, resource_metadata=%q",
					"insufficient_scope", strings.Join(required, " "), prmURL))
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}

func (f *fakeAuthServer) metadata(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	meta := map[string]any{
		"issuer":                                         f.issuer(),
		"authorization_endpoint":                         f.issuer() + "/authorize",
		"token_endpoint":                                 f.issuer() + "/token",
		"revocation_endpoint":                            f.issuer() + "/revoke",
		"response_types_supported":                       []string{"code"},
		"code_challenge_methods_supported":               []string{"S256"},
		"scopes_supported":                               append(slices.Clone(f.scopes), "offline_access"),
		"client_id_metadata_document_supported":          f.cimd,
		"authorization_response_iss_parameter_supported": f.issSupported,
	}
	if f.dcr {
		meta["registration_endpoint"] = f.issuer() + "/register"
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meta)
}

func (f *fakeAuthServer) register(w http.ResponseWriter, r *http.Request) {
	var meta oauthex.ClientRegistrationMetadata
	if err := json.NewDecoder(r.Body).Decode(&meta); err != nil || len(meta.RedirectURIs) != 1 {
		http.Error(w, "bad registration", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.registrations++
	id := fmt.Sprintf("dcr-%d", f.registrations)
	f.clients[id] = meta.RedirectURIs[0]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"client_id":                  id,
		"redirect_uris":              meta.RedirectURIs,
		"token_endpoint_auth_method": "none",
	})
}

func (f *fakeAuthServer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authorized = append(f.authorized, q)
	clientID, redirect := q.Get("client_id"), q.Get("redirect_uri")
	switch {
	case clientID == CIMDURL:
		if !f.cimd {
			http.Error(w, "unknown client", http.StatusBadRequest)
			return
		}
	case strings.HasPrefix(clientID, "dcr-"):
		if f.clients[clientID] != redirect {
			http.Error(w, "unknown client or redirect", http.StatusBadRequest)
			return
		}
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("resource") != f.mcpURL {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	code := rand.Text()
	f.codes[code] = grant{
		clientID:  clientID,
		redirect:  redirect,
		challenge: q.Get("code_challenge"),
		resource:  q.Get("resource"),
		scopes:    strings.Fields(q.Get("scope")),
	}
	back, _ := url.Parse(redirect)
	params := url.Values{"code": {code}, "state": {q.Get("state")}}
	if f.wrongIss {
		params.Set("iss", "https://evil.example.com")
	} else if f.issSupported {
		params.Set("iss", f.issuer())
	}
	back.RawQuery = params.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (f *fakeAuthServer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var g grant
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		var ok bool
		g, ok = f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge ||
			r.PostForm.Get("redirect_uri") != g.redirect || r.PostForm.Get("client_id") != g.clientID {
			tokenError(w, "invalid_grant")
			return
		}
	case "refresh_token":
		var ok bool
		g, ok = f.refresh[r.PostForm.Get("refresh_token")]
		if !ok || f.refreshFails {
			tokenError(w, "invalid_grant")
			return
		}
		f.refreshes++
	default:
		tokenError(w, "unsupported_grant_type")
		return
	}
	access, refresh := rand.Text(), rand.Text()
	g.expiry = time.Now().Add(f.accessTTL)
	f.access[access] = g
	f.refresh[refresh] = g
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":  access,
		"token_type":    "Bearer",
		"expires_in":    int(f.accessTTL.Seconds()),
		"refresh_token": refresh,
		"scope":         strings.Join(g.scopes, " "),
	})
}

func tokenError(w http.ResponseWriter, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func (f *fakeAuthServer) revoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	token := r.PostForm.Get("token")
	f.revoked = append(f.revoked, r.PostForm.Get("token_type_hint"))
	delete(f.access, token)
	delete(f.refresh, token)
}

func (f *fakeAuthServer) set(fn func(f *fakeAuthServer)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func (f *fakeAuthServer) lastAuthorize() url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authorized[len(f.authorized)-1]
}

// browse plays the browser: it visits the authorization URL and follows
// the redirect back to the loopback callback.
func browse(authURL string) error {
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := noFollow.Get(authURL)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		return fmt.Errorf("authorize answered %s", resp.Status)
	}
	resp, err = ipv6Browser.Get(resp.Header.Get("Location"))
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func loginCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// signIn connects srv, which needs sign-in, and signs in through the
// fake browser.
func signIn(t *testing.T, m *Manager, f *fakeAuthServer, cfg ServerConfig) error {
	t.Helper()
	cfg.URL = f.mcpURL
	_, err := m.ConnectServer(t.Context(), "srv", cfg)
	require.ErrorIs(t, err, ErrNeedsAuth)
	var shown []string
	err = m.Login(loginCtx(t), "srv", browse, func(u string) { shown = append(shown, u) })
	if err == nil {
		require.Len(t, shown, 1, "the authorization URL is shown to the user")
	}
	return err
}

func newTestCreds() *CredentialStore {
	return newCredentialStore(newFakeKeyring(), "/data", storagestub.NewInMemoryService())
}

func callEcho(t *testing.T, m *Manager) string {
	t.Helper()
	m.mu.Lock()
	ad := m.lookup("srv").tools["echo"]
	m.mu.Unlock()
	require.NotNil(t, ad, "echo is registered")
	res := ad.Execute(t.Context(), `{}`)
	require.False(t, res.IsError, res.Content)
	return res.Content
}

func TestLoginWithDynamicClientRegistration(t *testing.T) {
	f := newFakeAuthServer(t)
	creds := newTestCreds()
	m := NewManager(nil, "", creds)
	t.Cleanup(func() { _ = m.Close() })
	var added []string
	m.OnToolsChanged = func(_ string, tools []agent.Tool, _ []string) {
		for _, tool := range tools {
			added = append(added, tool.Definition().Function.Name)
		}
	}

	require.NoError(t, signIn(t, m, f, ServerConfig{}))
	assert.Equal(t, StatusConnected, serverInfo(t, m, "srv").Status)
	assert.Equal(t, []string{"mcp__srv__echo"}, added, "signing in reports the server's tools")
	assert.Equal(t, "ok from echo", callEcho(t, m))

	assert.Equal(t, 1, f.registrations)
	q := f.lastAuthorize()
	assert.Equal(t, "dcr-1", q.Get("client_id"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, f.mcpURL, q.Get("resource"))
	assert.ElementsMatch(t, []string{"read", "offline_access"}, strings.Fields(q.Get("scope")))

	doc, err := creds.LoadToken(t.Context(), f.mcpURL)
	require.NoError(t, err)
	require.NotNil(t, doc)
	assert.Equal(t, f.issuer(), doc.Issuer)
	assert.NotEmpty(t, doc.RefreshToken)
	client, err := creds.LoadClient(t.Context(), f.issuer(), doc.RedirectURL)
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, "dcr-1", client.ClientID)
}

func TestLoginPrefersConfiguredClient(t *testing.T) {
	f := newFakeAuthServer(t)
	f.cimd = true
	m := NewManager(nil, "", newTestCreds())
	t.Cleanup(func() { _ = m.Close() })

	require.NoError(t, signIn(t, m, f, ServerConfig{OAuth: &OAuthConfig{ClientID: "preregistered"}}))
	assert.Equal(t, "preregistered", f.lastAuthorize().Get("client_id"))
	assert.Zero(t, f.registrations)
}

func TestLoginPrefersClientIDMetadataDocument(t *testing.T) {
	f := newFakeAuthServer(t)
	f.cimd = true
	m := NewManager(nil, "", newTestCreds())
	t.Cleanup(func() { _ = m.Close() })

	require.NoError(t, signIn(t, m, f, ServerConfig{}))
	assert.Equal(t, CIMDURL, f.lastAuthorize().Get("client_id"))
	assert.Zero(t, f.registrations)
}

func TestLoginWithoutRegistrationMethod(t *testing.T) {
	f := newFakeAuthServer(t)
	f.dcr = false
	m := NewManager(nil, "", newTestCreds())
	t.Cleanup(func() { _ = m.Close() })

	err := signIn(t, m, f, ServerConfig{})
	require.ErrorContains(t, err, "supports no automatic client registration; set oauth.clientId")
}

func TestLoginPinsConfiguredScopes(t *testing.T) {
	f := newFakeAuthServer(t)
	m := NewManager(nil, "", newTestCreds())
	t.Cleanup(func() { _ = m.Close() })

	require.NoError(t, signIn(t, m, f, ServerConfig{OAuth: &OAuthConfig{Scopes: "read extra"}}))
	assert.ElementsMatch(t, []string{"read", "extra", "offline_access"},
		strings.Fields(f.lastAuthorize().Get("scope")))
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close())
	return port
}

func TestLoginReusesRegisteredClientPerIssuer(t *testing.T) {
	creds := newTestCreds()
	cfg := ServerConfig{OAuth: &OAuthConfig{CallbackPort: freePort(t)}}

	f := newFakeAuthServer(t)
	m := NewManager(nil, "", creds)
	t.Cleanup(func() { _ = m.Close() })
	require.NoError(t, signIn(t, m, f, cfg))
	require.NoError(t, m.Logout(t.Context(), "srv"))
	require.NoError(t, m.Login(loginCtx(t), "srv", browse, func(string) {}))
	assert.Equal(t, 1, f.registrations, "the client registered with this issuer is reused")
	assert.Equal(t, "dcr-1", f.lastAuthorize().Get("client_id"))

	other := newFakeAuthServer(t)
	m2 := NewManager(nil, "", creds)
	t.Cleanup(func() { _ = m2.Close() })
	require.NoError(t, signIn(t, m2, other, cfg))
	assert.Equal(t, 1, other.registrations, "a client is never presented to another issuer")
}

func TestLoginReusesPreviousRedirectPort(t *testing.T) {
	f := newFakeAuthServer(t)
	creds := newTestCreds()
	m := NewManager(nil, "", creds)
	t.Cleanup(func() { _ = m.Close() })
	require.NoError(t, signIn(t, m, f, ServerConfig{}))

	// The refresh token stops working, so the user signs in again.
	f.set(func(f *fakeAuthServer) {
		f.refreshFails = true
		clear(f.access)
	})
	require.ErrorIs(t, m.Reconnect(t.Context(), "srv"), ErrNeedsAuth)
	require.NoError(t, m.Login(loginCtx(t), "srv", browse, func(string) {}))
	assert.Equal(t, 1, f.registrations)
	assert.Equal(t, StatusConnected, serverInfo(t, m, "srv").Status)
}

func TestLoginRejectsIssuerMismatch(t *testing.T) {
	f := newFakeAuthServer(t)
	f.wrongIss = true
	m := NewManager(nil, "", newTestCreds())
	t.Cleanup(func() { _ = m.Close() })

	err := signIn(t, m, f, ServerConfig{})
	require.ErrorContains(t, err, `issuer "https://evil.example.com"`)
	assert.Equal(t, StatusNeedsAuth, serverInfo(t, m, "srv").Status)
}

func TestLoginRequiresAdvertisedIssuer(t *testing.T) {
	f := newFakeAuthServer(t)
	f.issSupported = true
	m := NewManager(nil, "", newTestCreds())
	t.Cleanup(func() { _ = m.Close() })
	require.NoError(t, signIn(t, m, f, ServerConfig{}), "a matching iss is accepted")
}

func TestSignInSurvivesRestart(t *testing.T) {
	f := newFakeAuthServer(t)
	creds := newTestCreds()
	m := NewManager(nil, "", creds)
	require.NoError(t, signIn(t, m, f, ServerConfig{}))
	require.NoError(t, m.Close())

	// The access token expires while Rune is closed.
	f.set(func(f *fakeAuthServer) { clear(f.access) })
	m2 := NewManager(nil, "", creds)
	t.Cleanup(func() { _ = m2.Close() })
	_, err := m2.ConnectServer(t.Context(), "srv", ServerConfig{URL: f.mcpURL})
	require.NoError(t, err, "the stored refresh token signs the server back in")
	assert.Equal(t, 1, f.refreshes)
	assert.Equal(t, "ok from echo", callEcho(t, m2))
}

func TestRejectedRefreshTokenNeedsAuth(t *testing.T) {
	f := newFakeAuthServer(t)
	creds := newTestCreds()
	m := NewManager(nil, "", creds)
	require.NoError(t, signIn(t, m, f, ServerConfig{}))
	require.NoError(t, m.Close())

	f.set(func(f *fakeAuthServer) {
		f.refreshFails = true
		clear(f.access)
	})
	m2 := NewManager(nil, "", creds)
	t.Cleanup(func() { _ = m2.Close() })
	_, err := m2.ConnectServer(t.Context(), "srv", ServerConfig{URL: f.mcpURL})
	require.ErrorIs(t, err, ErrNeedsAuth)
	assert.Equal(t, StatusNeedsAuth, serverInfo(t, m2, "srv").Status)
}

func TestInsufficientScopeNeedsAuthWithScope(t *testing.T) {
	f := newFakeAuthServer(t)
	f.requiredScopes = []string{"read", "admin"}
	m := NewManager(nil, "", newTestCreds())
	t.Cleanup(func() { _ = m.Close() })

	err := signIn(t, m, f, ServerConfig{})
	require.ErrorIs(t, err, ErrNeedsAuth)
	info := serverInfo(t, m, "srv")
	assert.Equal(t, StatusNeedsAuth, info.Status)
	assert.Contains(t, info.Error, `the server requires scope "read admin"`)

	require.NoError(t, m.Login(loginCtx(t), "srv", browse, func(string) {}))
	assert.ElementsMatch(t, []string{"read", "admin", "offline_access"},
		strings.Fields(f.lastAuthorize().Get("scope")))
	assert.Equal(t, StatusConnected, serverInfo(t, m, "srv").Status)
}

func TestLogoutRevokesAndForgets(t *testing.T) {
	f := newFakeAuthServer(t)
	creds := newTestCreds()
	m := NewManager(nil, "", creds)
	t.Cleanup(func() { _ = m.Close() })
	require.NoError(t, signIn(t, m, f, ServerConfig{}))

	require.NoError(t, m.Logout(t.Context(), "srv"))
	assert.Equal(t, []string{"refresh_token", "access_token"}, f.revoked)
	doc, err := creds.LoadToken(t.Context(), f.mcpURL)
	require.NoError(t, err)
	assert.Nil(t, doc)
	info := serverInfo(t, m, "srv")
	assert.Equal(t, StatusNeedsAuth, info.Status)

	m.mu.Lock()
	ad := m.lookup("srv").tools["echo"]
	m.mu.Unlock()
	res := ad.Execute(t.Context(), `{}`)
	assert.Contains(t, res.Content, "needs sign-in: run `agent mcp login srv`")
}

func TestLoginCompletesWithPastedRedirect(t *testing.T) {
	f := newFakeAuthServer(t)
	m := NewManager(nil, "", newTestCreds())
	t.Cleanup(func() { _ = m.Close() })
	_, err := m.ConnectServer(t.Context(), "srv", ServerConfig{URL: f.mcpURL})
	require.ErrorIs(t, err, ErrNeedsAuth)

	// The browser runs elsewhere: it lands on a redirect that never
	// reaches this machine, and the user pastes it.
	pasted := make(chan string, 1)
	open := func(authURL string) error {
		noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}
		resp, err := noFollow.Get(authURL)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		pasted <- resp.Header.Get("Location")
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- m.Login(loginCtx(t), "srv", open, func(string) {}) }()
	require.NoError(t, m.CompleteLogin("srv", <-pasted))
	require.NoError(t, <-done)
	assert.Equal(t, StatusConnected, serverInfo(t, m, "srv").Status)
	require.ErrorContains(t, m.CompleteLogin("srv", "x"), "no sign-in")
}

func TestLoginNotApplicable(t *testing.T) {
	_, url := httpTestServer(t, true, "echo")
	m := NewManager(nil, "", newTestCreds())
	t.Cleanup(func() { _ = m.Close() })

	_, err := m.ConnectServer(t.Context(), "open", ServerConfig{URL: url})
	require.NoError(t, err)
	called := false
	require.NoError(t, m.Login(loginCtx(t), "open", browse, func(string) { called = true }))
	assert.False(t, called, "a server that needs no sign-in is not signed in to")

	m.Register("headers", ServerConfig{URL: url, Headers: map[string]string{"Authorization": "x"}}, nil, nil)
	require.ErrorContains(t, m.Login(loginCtx(t), "headers", browse, func(string) {}),
		"authenticates with its configured headers")

	_, factory := testServer(t, "x")
	m3 := NewManagerWithTransport(factory)
	t.Cleanup(func() { _ = m3.Close() })
	_, err = m3.ConnectServer(t.Context(), "local", ServerConfig{Command: "x"})
	require.NoError(t, err)
	require.ErrorContains(t, m3.Login(loginCtx(t), "local", browse, func(string) {}), "not a remote server")
}

func TestCIMDURL(t *testing.T) {
	assert.Equal(t, "https://api.rune.build/o/oauth2/mcp-client.json", CIMDURL)
}
