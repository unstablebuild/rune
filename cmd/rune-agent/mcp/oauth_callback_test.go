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
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startTestCallback(t *testing.T) *callbackServer {
	t.Helper()
	cb, err := startCallbackServer(0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cb.Close() })
	return cb
}

// ipv6Browser follows URLs like a browser on a machine where localhost
// resolves to ::1, as it does by default on many systems, or where
// another program holds the callback's port on ::1.
var ipv6Browser = &http.Client{Transport: &http.Transport{
	DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if host == "localhost" {
			addr = net.JoinHostPort("::1", port)
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	},
}}

// visit follows the redirect URL the way the browser would, and returns
// the response status and body.
func visit(t *testing.T, cb *callbackServer, query url.Values) (int, string) {
	t.Helper()
	resp, err := ipv6Browser.Get(cb.RedirectURL() + "?" + query.Encode())
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

func waitResult(t *testing.T, cb *callbackServer) (callbackResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	return cb.wait(ctx)
}

func TestCallbackServerRedirectURL(t *testing.T) {
	cb := startTestCallback(t)
	u, err := url.Parse(cb.RedirectURL())
	require.NoError(t, err)
	assert.Equal(t, "http", u.Scheme)
	assert.Equal(t, "127.0.0.1", u.Hostname(), "the redirect names the address listened on")
	assert.NotEmpty(t, u.Port())
	assert.Equal(t, "/callback", u.Path)
}

func TestCallbackServerSuccess(t *testing.T) {
	cb := startTestCallback(t)

	status, body := visit(t, cb, url.Values{"code": {"c0de"}, "state": {cb.state}, "iss": {"https://auth"}})
	assert.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, "You are signed in")

	res, err := waitResult(t, cb)
	require.NoError(t, err)
	assert.Equal(t, callbackResult{code: "c0de", state: cb.state, iss: "https://auth"}, res)
}

func TestCallbackServerRejectsWrongState(t *testing.T) {
	cb := startTestCallback(t)

	status, _ := visit(t, cb, url.Values{"code": {"c0de"}, "state": {"forged"}})
	assert.Equal(t, http.StatusBadRequest, status)

	status, _ = visit(t, cb, url.Values{"code": {"good"}, "state": {cb.state}})
	assert.Equal(t, http.StatusOK, status, "a forged redirect must not consume the sign-in")
	res, err := waitResult(t, cb)
	require.NoError(t, err)
	assert.Equal(t, "good", res.code)
}

func TestCallbackServerIgnoresSecondCallback(t *testing.T) {
	cb := startTestCallback(t)

	status, _ := visit(t, cb, url.Values{"code": {"first"}, "state": {cb.state}})
	assert.Equal(t, http.StatusOK, status)
	status, _ = visit(t, cb, url.Values{"code": {"second"}, "state": {cb.state}})
	assert.Equal(t, http.StatusConflict, status)

	res, err := waitResult(t, cb)
	require.NoError(t, err)
	assert.Equal(t, "first", res.code)
}

func TestCallbackServerErrorParameter(t *testing.T) {
	cb := startTestCallback(t)

	status, body := visit(t, cb, url.Values{
		"error": {"access_denied"}, "error_description": {"user <said> no"}, "state": {cb.state},
	})
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body, "user &lt;said&gt; no", "the description is escaped")

	_, err := waitResult(t, cb)
	require.EqualError(t, err, "authorization server: access_denied: user <said> no")
}

func TestCallbackServerDeliver(t *testing.T) {
	tests := []struct {
		name    string
		url     func(cb *callbackServer) string
		wantErr string
	}{
		{
			name: "pasted redirect",
			url: func(cb *callbackServer) string {
				return cb.RedirectURL() + "?code=pasted&state=" + url.QueryEscape(cb.state)
			},
		},
		{
			name:    "wrong path",
			url:     func(cb *callbackServer) string { return "http://localhost:1/other?code=x&state=" + cb.state },
			wantErr: `redirect URL path is "/other"`,
		},
		{
			name:    "wrong state",
			url:     func(cb *callbackServer) string { return cb.RedirectURL() + "?code=x&state=nope" },
			wantErr: "state does not match",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb := startTestCallback(t)
			err := cb.Deliver(tt.url(cb))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			res, err := waitResult(t, cb)
			require.NoError(t, err)
			assert.Equal(t, "pasted", res.code)
			require.ErrorContains(t, cb.Deliver(tt.url(cb)), "already completed")
		})
	}
}

func TestCallbackServerWaitEndsWithContext(t *testing.T) {
	cb := startTestCallback(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := cb.wait(ctx)
	require.ErrorIs(t, err, context.Canceled)
}
