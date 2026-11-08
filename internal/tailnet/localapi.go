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

package tailnet

import (
	"context"
	"errors"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"sync"

	"tailscale.com/client/local"
	"tailscale.com/paths"
	"tailscale.com/safesocket"
)

// newLocalClient is a client of the Tailscale client running on this
// machine.
//
// On macOS the Tailscale apps serve their LocalAPI on a loopback port
// they pick at random, behind a token, and the client library looks
// both up again for every request: reading the app's files and testing
// the port, or running lsof for the App Store app, which costs more than
// the request itself. Requests go through a [localAPITransport] there.
func newLocalClient() *local.Client {
	if runtime.GOOS != "darwin" {
		return &local.Client{}
	}
	return &local.Client{
		OmitAuth: true,
		Transport: newLocalAPITransport(safesocket.LocalTCPPortAndToken,
			func(ctx context.Context) (net.Conn, error) {
				return safesocket.ConnectContext(ctx, paths.DefaultTailscaledSocket())
			}),
	}
}

// localAPITransport reaches a LocalAPI served on a loopback port behind
// a token, which it looks up once and again only after a request could
// not connect with them or the token was rejected: the app moved, as it
// does when it restarts. A LocalAPI without a port, such as that of a
// tailscaled run by hand, is reached over its socket.
type localAPITransport struct {
	lookup func() (port int, token string, err error)
	socket *http.Transport

	mu sync.Mutex
	// known is whether api holds what the last lookup found; a nil api
	// is a LocalAPI reached over the socket.
	known bool
	api   *loopbackAPI
}

type loopbackAPI struct {
	token string
	rt    *http.Transport
}

func newLocalAPITransport(
	lookup func() (port int, token string, err error),
	dialSocket func(ctx context.Context) (net.Conn, error),
) *localAPITransport {
	return &localAPITransport{
		lookup: lookup,
		socket: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialSocket(ctx)
			},
		},
	}
}

// RoundTrip satisfies http.RoundTripper.
func (t *localAPITransport) RoundTrip(req *http.Request) (*http.Response, error) {
	api, fresh := t.current()
	res, err := t.send(api, req)
	if !t.moved(api, res, err) {
		return res, err
	}
	if fresh {
		t.forget(api)
		return res, err
	}
	retry, ok := rewind(req)
	if !ok {
		t.forget(api)
		return res, err
	}
	if res != nil {
		_ = res.Body.Close()
	}
	t.forget(api)
	api, _ = t.current()
	return t.send(api, retry)
}

// current is where the LocalAPI is, and whether it was just looked up.
func (t *localAPITransport) current() (*loopbackAPI, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.known {
		return t.api, false
	}
	t.known = true
	t.api = nil
	port, token, err := t.lookup()
	if err != nil {
		return nil, true
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	var d net.Dialer
	t.api = &loopbackAPI{
		token: token,
		rt: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return d.DialContext(ctx, "tcp", addr)
			},
		},
	}
	return t.api, true
}

func (t *localAPITransport) send(api *loopbackAPI, req *http.Request) (*http.Response, error) {
	if api == nil {
		return t.socket.RoundTrip(req)
	}
	req = req.Clone(req.Context())
	req.SetBasicAuth("", api.token)
	return api.rt.RoundTrip(req)
}

// moved reports whether a request to api failed because the LocalAPI is
// no longer there: it could not connect, or the token was rejected.
func (t *localAPITransport) moved(api *loopbackAPI, res *http.Response, err error) bool {
	if err != nil {
		var op *net.OpError
		return errors.As(err, &op) && op.Op == "dial"
	}
	return api != nil && res.StatusCode == http.StatusUnauthorized
}

// forget drops api unless another request already replaced it.
func (t *localAPITransport) forget(api *loopbackAPI) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.known || t.api != api {
		return
	}
	t.known = false
	if api != nil {
		api.rt.CloseIdleConnections()
	}
}

// rewind is req ready to be sent again, if its body can be.
func rewind(req *http.Request) (*http.Request, bool) {
	if req.Body == nil || req.Body == http.NoBody {
		return req, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	retry := req.Clone(req.Context())
	retry.Body = body
	return retry, true
}
