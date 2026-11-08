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
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/client/local"
	"tailscale.com/ipn/ipnstate"
)

// loopbackLocalAPI stands in for the LocalAPI a macOS Tailscale app
// serves on a loopback port, behind a token.
type loopbackLocalAPI struct {
	server *httptest.Server

	mu    sync.Mutex
	token string
}

func serveLoopbackLocalAPI(t *testing.T, token string) *loopbackLocalAPI {
	t.Helper()
	api := &loopbackLocalAPI{token: token}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.mu.Lock()
		want := api.token
		api.mu.Unlock()
		if _, got, _ := r.BasicAuth(); want != "" && got != want {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/localapi/v0/status" {
			_ = json.NewEncoder(w).Encode(&ipnstate.Status{BackendState: "Running"})
			return
		}
		http.Error(w, "access denied", http.StatusForbidden)
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (api *loopbackLocalAPI) port() int {
	return api.server.Listener.Addr().(*net.TCPAddr).Port
}

func (api *loopbackLocalAPI) setToken(token string) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.token = token
}

// appLookup finds the app's port and token where the test says, and
// counts how often it was asked.
type appLookup struct {
	calls atomic.Int32

	mu    sync.Mutex
	port  int
	token string
	err   error
}

func (l *appLookup) lookup() (int, string, error) {
	l.calls.Add(1)
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.port, l.token, l.err
}

func (l *appLookup) set(port int, token string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.port, l.token, l.err = port, token, err
}

func noSocket(context.Context) (net.Conn, error) {
	return nil, &net.OpError{Op: "dial", Net: "unix", Err: errors.New("no such file")}
}

func TestLocalAPITransport(t *testing.T) {
	ctx := context.Background()
	status := func(t *testing.T, lc *local.Client) {
		t.Helper()
		st, err := lc.StatusWithoutPeers(ctx)
		require.NoError(t, err)
		assert.Equal(t, "Running", st.BackendState)
	}
	client := func(l *appLookup, socket func(context.Context) (net.Conn, error)) *local.Client {
		return &local.Client{OmitAuth: true, Transport: newLocalAPITransport(l.lookup, socket)}
	}

	t.Run("looks the app up once", func(t *testing.T) {
		api := serveLoopbackLocalAPI(t, "token")
		l := &appLookup{}
		l.set(api.port(), "token", nil)
		lc := client(l, noSocket)
		for range 3 {
			status(t, lc)
		}
		assert.Equal(t, int32(1), l.calls.Load())
	})

	t.Run("looks it up again once it moved", func(t *testing.T) {
		before := serveLoopbackLocalAPI(t, "before")
		l := &appLookup{}
		l.set(before.port(), "before", nil)
		lc := client(l, noSocket)
		status(t, lc)

		before.server.Close()
		after := serveLoopbackLocalAPI(t, "after")
		l.set(after.port(), "after", nil)
		status(t, lc)
		status(t, lc)
		assert.Equal(t, int32(2), l.calls.Load())
	})

	t.Run("looks it up again once its token is rejected", func(t *testing.T) {
		api := serveLoopbackLocalAPI(t, "before")
		l := &appLookup{}
		l.set(api.port(), "before", nil)
		lc := client(l, noSocket)
		status(t, lc)

		api.setToken("after")
		l.set(api.port(), "after", nil)
		status(t, lc)
		assert.Equal(t, int32(2), l.calls.Load())
	})

	t.Run("a refusal is not taken for a move", func(t *testing.T) {
		api := serveLoopbackLocalAPI(t, "token")
		l := &appLookup{}
		l.set(api.port(), "token", nil)
		lc := client(l, noSocket)
		_, err := lc.Ping(ctx, testAddr(2), "disco")
		require.Error(t, err)
		status(t, lc)
		assert.Equal(t, int32(1), l.calls.Load())
	})

	t.Run("a LocalAPI without a port is reached over its socket", func(t *testing.T) {
		api := serveLoopbackLocalAPI(t, "")
		l := &appLookup{}
		l.set(0, "", errors.New("no app"))
		var sockets atomic.Int32
		lc := client(l, func(ctx context.Context) (net.Conn, error) {
			sockets.Add(1)
			var d net.Dialer
			return d.DialContext(ctx, "tcp", api.server.Listener.Addr().String())
		})
		for range 3 {
			status(t, lc)
		}
		assert.Equal(t, int32(1), l.calls.Load())
		assert.Positive(t, sockets.Load())

		// The socket went away and the app started.
		api.server.Close()
		app := serveLoopbackLocalAPI(t, "token")
		l.set(app.port(), "token", nil)
		status(t, lc)
		assert.Equal(t, int32(2), l.calls.Load())
	})

	t.Run("a request is sent once when the app cannot be found", func(t *testing.T) {
		l := &appLookup{}
		l.set(0, "", errors.New("no app"))
		lc := client(l, noSocket)
		_, err := lc.StatusWithoutPeers(ctx)
		require.Error(t, err)
		assert.Equal(t, int32(1), l.calls.Load())
		_, err = lc.StatusWithoutPeers(ctx)
		require.Error(t, err)
		assert.Equal(t, int32(2), l.calls.Load(), "each failure looks again")
	})
}

func TestNewLocalClient(t *testing.T) {
	lc := newLocalClient()
	_, cached := lc.Transport.(*localAPITransport)
	assert.Equal(t, runtime.GOOS == "darwin", cached)
	assert.Equal(t, cached, lc.OmitAuth, "the transport sends the token itself")
}
