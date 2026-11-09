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
	"maps"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/oauth2"
)

func TestValidateServerURL(t *testing.T) {
	tests := []struct {
		url     string
		wantErr string
	}{
		{url: "https://mcp.example.com/mcp"},
		{url: "http://localhost:3000/mcp"},
		{url: "http://127.0.0.1:3000/mcp"},
		{url: "http://127.8.0.1/mcp"},
		{url: "http://[::1]:3000/mcp"},
		{url: "http://app.localhost/mcp"},
		{url: "http://mcp.example.com/mcp", wantErr: "must use https"},
		{url: "http://10.0.0.1/mcp", wantErr: "must use https"},
		{url: "ws://localhost/mcp", wantErr: "must use https"},
		{url: "https://user:pass@mcp.example.com/mcp", wantErr: "must not contain credentials"},
		{url: "https:///mcp", wantErr: "has no host"},
		{url: "::bad", wantErr: "invalid url"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			_, err := validateServerURL(tt.url)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestHTTPTransportKinds(t *testing.T) {
	a := &authState{}
	tr, err := httpTransport(ServerConfig{Type: "http", URL: "https://x/mcp"}, a)
	require.NoError(t, err)
	assert.IsType(t, &gomcp.StreamableClientTransport{}, tr)

	tr, err = httpTransport(ServerConfig{Type: "sse", URL: "https://x/sse"}, a)
	require.NoError(t, err)
	assert.IsType(t, &sseTransport{}, tr)

	_, err = httpTransport(ServerConfig{URL: "http://example.com/mcp"}, a)
	require.ErrorContains(t, err, "must use https")
}

// recorder records the headers of the requests it receives.
type recorder struct {
	mu      sync.Mutex
	headers []http.Header
	status  int
	extra   http.Header
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	r.headers = append(r.headers, req.Header.Clone())
	status, extra := r.status, r.extra
	r.mu.Unlock()
	maps.Copy(w.Header(), extra)
	if status != 0 {
		w.WriteHeader(status)
	}
}

func (r *recorder) last() http.Header {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.headers[len(r.headers)-1]
}

func roundTripClient(t *testing.T, endpoint string, static map[string]string, a *authState) *http.Client {
	t.Helper()
	origin, err := validateServerURL(endpoint)
	require.NoError(t, err)
	return &http.Client{Transport: &authRoundTripper{
		base:   http.DefaultTransport.(*http.Transport).Clone(),
		origin: origin,
		static: static,
		auth:   a,
	}}
}

func get(t *testing.T, c *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	_ = resp.Body.Close()
	return resp
}

func TestAuthRoundTripperAddsHeadersForOrigin(t *testing.T) {
	rec := &recorder{}
	ts := httptest.NewServer(rec)
	defer ts.Close()

	a := &authState{ts: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok"})}
	a.setHelperHeaders(map[string]string{"X-Helper": "h", "X-Static": "from-helper"})
	c := roundTripClient(t, ts.URL+"/mcp", map[string]string{"X-Static": "s", "X-Other": "o"}, a)

	get(t, c, ts.URL+"/mcp")
	h := rec.last()
	assert.Equal(t, "Bearer tok", h.Get("Authorization"))
	assert.Equal(t, "h", h.Get("X-Helper"))
	assert.Equal(t, "from-helper", h.Get("X-Static"), "helper headers win over static ones")
	assert.Equal(t, "o", h.Get("X-Other"))
}

func TestAuthRoundTripperKeepsConfiguredAuthorization(t *testing.T) {
	rec := &recorder{}
	ts := httptest.NewServer(rec)
	defer ts.Close()

	a := &authState{ts: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok"})}
	c := roundTripClient(t, ts.URL, map[string]string{"authorization": "Basic abc"}, a)

	get(t, c, ts.URL)
	assert.Equal(t, "Basic abc", rec.last().Get("Authorization"))
}

func TestAuthRoundTripperSendsNothingAcrossOrigins(t *testing.T) {
	other := &recorder{}
	otherTS := httptest.NewServer(other)
	defer otherTS.Close()
	origin := &recorder{status: http.StatusFound, extra: http.Header{"Location": {otherTS.URL + "/landing"}}}
	originTS := httptest.NewServer(origin)
	defer originTS.Close()

	a := &authState{ts: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok"})}
	c := roundTripClient(t, originTS.URL, map[string]string{"X-Key": "secret"}, a)

	get(t, c, originTS.URL+"/mcp")
	assert.Equal(t, "secret", origin.last().Get("X-Key"))
	h := other.last()
	assert.Empty(t, h.Get("X-Key"), "configured headers must not follow a redirect elsewhere")
	assert.Empty(t, h.Get("Authorization"), "the token must not follow a redirect elsewhere")
}

func TestAuthRoundTripperRecordsChallenge(t *testing.T) {
	rec := &recorder{
		status: http.StatusUnauthorized,
		extra:  http.Header{"Www-Authenticate": {`Bearer resource_metadata="https://x/.well-known/oauth-protected-resource"`}},
	}
	ts := httptest.NewServer(rec)
	defer ts.Close()

	a := &authState{}
	c := roundTripClient(t, ts.URL, nil, a)
	get(t, c, ts.URL+"/mcp")

	status, ch, needsAuth := a.status()
	assert.Equal(t, http.StatusUnauthorized, status)
	assert.False(t, needsAuth)
	require.NotNil(t, ch)
	assert.Equal(t, http.StatusUnauthorized, ch.status)
	assert.Equal(t, ts.URL+"/mcp", ch.url)
	assert.Contains(t, ch.header.Get("WWW-Authenticate"), "resource_metadata")

	a.beginConnect()
	_, ch, _ = a.status()
	assert.Nil(t, ch, "a new connection starts without a challenge")
}

type failingTokenSource struct{ err error }

func (f failingTokenSource) Token() (*oauth2.Token, error) { return nil, f.err }

func TestAuthRoundTripperInvalidGrantSignsOut(t *testing.T) {
	rec := &recorder{}
	ts := httptest.NewServer(rec)
	defer ts.Close()

	a := &authState{ts: failingTokenSource{&oauth2.RetrieveError{ErrorCode: "invalid_grant"}}}
	c := roundTripClient(t, ts.URL, nil, a)
	get(t, c, ts.URL)

	assert.Empty(t, rec.last().Get("Authorization"), "the request goes out without a token")
	_, _, needsAuth := a.status()
	assert.True(t, needsAuth)
	assert.Nil(t, a.ts, "the rejected sign-in is dropped")
}
