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
	"net/netip"
	"net/url"
	"strings"

	gomcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// httpTransport returns the transport for a remote server. Every
// request to the server's origin carries the configured headers and
// the stored sign-in, and its response is recorded in auth.
func httpTransport(cfg ServerConfig, auth *authState) (gomcp.Transport, error) {
	kind, err := cfg.Transport()
	if err != nil {
		return nil, err
	}
	endpoint, err := validateServerURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Transport: &authRoundTripper{
		base:   http.DefaultTransport.(*http.Transport).Clone(),
		origin: endpoint,
		static: cfg.Headers,
		auth:   auth,
	}}
	switch kind {
	case TransportHTTP:
		return &gomcp.StreamableClientTransport{Endpoint: cfg.URL, HTTPClient: client}, nil
	case TransportSSE:
		return &sseTransport{inner: &gomcp.SSEClientTransport{Endpoint: cfg.URL, HTTPClient: client}}, nil
	default:
		return nil, fmt.Errorf("%s is not a remote transport", kind)
	}
}

// validateServerURL accepts https URLs, and http URLs to this machine.
func validateServerURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if u.User != nil {
		return nil, errors.New("url must not contain credentials; use headers instead")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("url %q has no host", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !IsLoopbackHost(u.Hostname()) {
			return nil, fmt.Errorf("url %q must use https unless it points at this machine", raw)
		}
	default:
		return nil, fmt.Errorf("url %q must use https", raw)
	}
	return u, nil
}

// IsLoopbackHost reports whether host names this machine: localhost or
// a loopback address.
func IsLoopbackHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return err == nil && ip.Unmap().IsLoopback()
}

// authRoundTripper adds the configured headers and the bearer token to
// requests for the server's origin, and to no other: redirect hops and
// discovery requests elsewhere go out unchanged.
type authRoundTripper struct {
	base   http.RoundTripper
	origin *url.URL
	static map[string]string
	auth   *authState
}

func (rt *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if !sameOrigin(req.URL, rt.origin) {
		return rt.base.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	for k, v := range rt.static {
		req.Header.Set(k, v)
	}
	helper := rt.auth.helperHeaders()
	for k, v := range helper {
		req.Header.Set(k, v)
	}
	if !hasHeader(rt.static, "Authorization") && !hasHeader(helper, "Authorization") {
		token, err := rt.auth.bearer()
		if err != nil {
			return nil, fmt.Errorf("refresh access token: %w", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := rt.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	rt.auth.record(req.URL.String(), resp)
	return resp, nil
}

func sameOrigin(a, b *url.URL) bool {
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(hostPort(a), hostPort(b))
}

func hostPort(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// sseTransport keeps the event stream of a legacy SSE server open past
// the connect deadline: the SDK reads the stream with the context
// Connect is given.
type sseTransport struct {
	inner *gomcp.SSEClientTransport
}

func (t *sseTransport) Connect(ctx context.Context) (gomcp.Connection, error) {
	streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	conn, err := t.inner.Connect(streamCtx)
	if !stop() {
		if conn != nil {
			_ = conn.Close()
		}
		return nil, ctx.Err()
	}
	if err != nil {
		cancel()
		return nil, err
	}
	return &cancelOnClose{Connection: conn, cancel: cancel}, nil
}

type cancelOnClose struct {
	gomcp.Connection
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.Connection.Close()
	c.cancel()
	return err
}
