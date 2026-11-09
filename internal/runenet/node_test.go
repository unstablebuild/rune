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

package runenet

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"
	"tailscale.com/types/views"
	"unstable.build/rune/internal/debug"
)

func TestStartCredentials(t *testing.T) {
	t.Run("propagates the entitlement error without joining", func(t *testing.T) {
		node := New(Config{
			Hostname: "workstation",
			Port:     DefaultPort,
			Dir:      t.TempDir(),
			Credentials: func(context.Context) (string, string, error) {
				return "", "", ErrSubscriptionRequired
			},
		})
		t.Cleanup(func() { _ = node.Close() })

		err := node.Start(context.Background())
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrSubscriptionRequired))

		_, err = node.Peers(context.Background())
		assert.True(t, errors.Is(err, ErrNotStarted))
	})

	t.Run("records the failure for status", func(t *testing.T) {
		credErr := errors.New("credentials endpoint unreachable")
		node := New(Config{
			Hostname: "workstation",
			Port:     DefaultPort,
			Dir:      t.TempDir(),
			Credentials: func(context.Context) (string, string, error) {
				return "", "", credErr
			},
		})
		t.Cleanup(func() { _ = node.Close() })

		require.Error(t, node.Start(context.Background()))

		st, err := node.Status(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "NotRunning", st.State)
		assert.Equal(t, credErr.Error(), st.LastError)
	})

	t.Run("joins with the minted credentials", func(t *testing.T) {
		var gotCtxValue any
		node := New(Config{
			Hostname: "workstation",
			Port:     DefaultPort,
			Dir:      t.TempDir(),
			Credentials: func(ctx context.Context) (string, string, error) {
				gotCtxValue = ctx.Value(credentialsCtxKey{})
				return "https://control.example.com", "tskey-auth-secret", nil
			},
		})
		t.Cleanup(func() { _ = node.Close() })

		require.NoError(t, node.Start(context.WithValue(
			context.Background(), credentialsCtxKey{}, "called")))
		assert.Equal(t, "called", gotCtxValue)
		assert.Equal(t, "https://control.example.com", node.srv.ControlURL)
		assert.Equal(t, "tskey-auth-secret", node.srv.AuthKey)
	})
}

func TestNeedsReauth(t *testing.T) {
	assert.True(t, needsReauth(&ipnstate.Status{
		BackendState: ipn.NeedsLogin.String()}))
	assert.False(t, needsReauth(&ipnstate.Status{
		BackendState: ipn.Running.String()}))
	assert.False(t, needsReauth(nil))
}

func TestReauthenticate(t *testing.T) {
	t.Run("is a no-op without a credentials source", func(t *testing.T) {
		node := New(Config{
			Hostname: "workstation", Port: DefaultPort, Dir: t.TempDir(),
			AuthKey: "tskey-static",
		})
		t.Cleanup(func() { _ = node.Close() })
		assert.NoError(t, node.reauthenticate(context.Background()))
	})

	t.Run("needs a running node", func(t *testing.T) {
		node := New(Config{
			Hostname: "workstation", Port: DefaultPort, Dir: t.TempDir(),
			Credentials: func(context.Context) (string, string, error) {
				return "https://control.example.com", "tskey-auth-secret", nil
			},
		})
		t.Cleanup(func() { _ = node.Close() })
		assert.True(t, errors.Is(
			node.reauthenticate(context.Background()), ErrNotStarted))
	})
}

func TestRejoin(t *testing.T) {
	t.Run("is a no-op without a credentials source", func(t *testing.T) {
		node := New(Config{
			Hostname: "workstation", Port: DefaultPort, Dir: t.TempDir(),
			AuthKey: "tskey-static",
		})
		t.Cleanup(func() { _ = node.Close() })
		assert.NoError(t, node.Rejoin(context.Background()))
	})

	t.Run("needs a running node", func(t *testing.T) {
		node := New(Config{
			Hostname: "workstation", Port: DefaultPort, Dir: t.TempDir(),
			Credentials: func(context.Context) (string, string, error) {
				return "https://control.example.com", "tskey-auth-secret", nil
			},
		})
		t.Cleanup(func() { _ = node.Close() })
		assert.True(t, errors.Is(
			node.Rejoin(context.Background()), ErrNotStarted))
	})
}

// joinTimeout bounds a join against the in-process coordination
// server. A join that was cut short never gets there.
const joinTimeout = 20 * time.Second

const testAuthKey = "tskey-auth-test"

// startTestControl runs an in-process coordination server that only
// registers nodes presenting testAuthKey. Every /key request is held
// for a second, like the round-trip to the production server: without
// it a login completes before anything else can see it in flight.
func startTestControl(t *testing.T) string {
	t.Helper()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })

	control := &testcontrol.Server{
		DERPMap:          integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1"),
		RequireAuthKey:   testAuthKey,
		AllNodesSameUser: true,
		Logf:             logger.Discard,
	}
	server := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/key" {
				time.Sleep(time.Second)
			}
			control.ServeHTTP(w, r)
		}))
	t.Cleanup(server.Close)
	return server.URL
}

// countingCredentials hands out the test control server's credentials
// and counts how often it was asked: every call is an auth key the
// account server mints.
type countingCredentials struct {
	controlURL string
	calls      atomic.Int32
}

func (c *countingCredentials) get(context.Context) (string, string, error) {
	c.calls.Add(1)
	return c.controlURL, testAuthKey, nil
}

func newTestNode(t *testing.T, creds *countingCredentials) *Node {
	t.Helper()
	return newHostTestNode(t, "workstation", creds)
}

func newHostTestNode(t *testing.T, hostname string, creds *countingCredentials) *Node {
	t.Helper()
	node := New(Config{
		Hostname:    hostname,
		Port:        DefaultPort,
		Dir:         t.TempDir(),
		Credentials: creds.get,
	})
	t.Cleanup(func() { _ = node.Close() })
	return node
}

func TestPing(t *testing.T) {
	controlURL := startTestControl(t)
	creds := &countingCredentials{controlURL: controlURL}
	laptop := newHostTestNode(t, "laptop", creds)
	desktop := newHostTestNode(t, "desktop", creds)
	ctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
	defer cancel()
	require.NoError(t, laptop.Up(ctx))
	require.NoError(t, desktop.Up(ctx))
	st, err := desktop.Status(ctx)
	require.NoError(t, err)

	var got PingResult
	require.Eventually(t, func() bool {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		got, err = laptop.Ping(pingCtx, "desktop")
		return err == nil
	}, joinTimeout, 100*time.Millisecond, "no pong: %v", err)
	assert.Equal(t, "desktop", got.Peer)
	assert.Equal(t, st.Addrs[0], got.Addr)
	assert.Positive(t, got.Latency)
	assert.True(t, got.Endpoint != "" || got.DERPRegion != "", "%+v", got)

	_, err = laptop.Ping(ctx, "nope")
	assert.ErrorIs(t, err, ErrPeerNotFound)
}

func TestPingResultDirect(t *testing.T) {
	assert.True(t, PingResult{Endpoint: "192.168.1.5:41641"}.Direct())
	assert.False(t, PingResult{DERPRegion: "fra"}.Direct())
	assert.False(t, PingResult{Endpoint: "10.0.0.1:41641", PeerRelay: "10.0.0.2:7777:vni:1"}.Direct())
	assert.False(t, PingResult{}.Direct())
}

// requireJoined checks the node reached the configured coordination
// server, not the Tailscale default a reset preference falls back to.
func requireJoined(t *testing.T, node *Node, controlURL string) {
	t.Helper()
	st, err := node.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, StateRunning, st.State)
	assert.Empty(t, st.LastError)

	lc, err := node.client()
	require.NoError(t, err)
	prefs, err := lc.GetPrefs(context.Background())
	require.NoError(t, err)
	assert.Equal(t, controlURL, prefs.ControlURL)
}

func TestUpFirstJoin(t *testing.T) {
	cases := []struct {
		name string
		up   func(context.Context, *Node) error
	}{
		{name: "up", up: func(ctx context.Context, n *Node) error {
			return n.Up(ctx)
		}},
		{name: "start then up", up: func(ctx context.Context, n *Node) error {
			if err := n.Start(ctx); err != nil {
				return err
			}
			return n.Up(ctx)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			controlURL := startTestControl(t)
			creds := &countingCredentials{controlURL: controlURL}
			node := newTestNode(t, creds)

			ctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
			defer cancel()
			require.NoError(t, tc.up(ctx, node))

			requireJoined(t, node, controlURL)
			assert.Equal(t, int32(1), creds.calls.Load())
		})
	}
}

func TestUpAfterLogout(t *testing.T) {
	controlURL := startTestControl(t)
	creds := &countingCredentials{controlURL: controlURL}
	node := newTestNode(t, creds)
	logOut(t, node, creds)

	ctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
	defer cancel()
	require.NoError(t, node.Up(ctx))

	requireJoined(t, node, controlURL)
	assert.Equal(t, int32(1), creds.calls.Load())
}

func TestConcurrentUpAfterLogout(t *testing.T) {
	controlURL := startTestControl(t)
	creds := &countingCredentials{controlURL: controlURL}
	node := newTestNode(t, creds)
	logOut(t, node, creds)

	ctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
	defer cancel()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			errs[i] = node.Up(ctx)
		})
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	requireJoined(t, node, controlURL)
	assert.Equal(t, int32(1), creds.calls.Load())
}

// logOut joins node through the join Start runs in the background,
// logs it out, and resets the credentials count.
func logOut(t *testing.T, node *Node, creds *countingCredentials) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
	defer cancel()
	require.NoError(t, node.Start(ctx))
	_, err := node.srv.Up(ctx)
	require.NoError(t, err)
	lc, err := node.client()
	require.NoError(t, err)
	require.NoError(t, lc.Logout(ctx))

	// The backend can pass through Stopped on its way out.
	require.Eventually(t, func() bool {
		st, err := node.Status(ctx)
		return err == nil && st.State == ipn.NeedsLogin.String()
	}, joinTimeout, 50*time.Millisecond)
	creds.calls.Store(0)
}

func TestEnteredNeedsLogin(t *testing.T) {
	cases := []struct {
		name string
		prev ipn.State
		next ipn.State
		want bool
	}{
		{"from running", ipn.Running, ipn.NeedsLogin, true},
		{"from no state", ipn.NoState, ipn.NeedsLogin, true},
		{"from starting", ipn.Starting, ipn.NeedsLogin, true},
		{"repeated on re-watch", ipn.NeedsLogin, ipn.NeedsLogin, false},
		{"joining", ipn.NeedsLogin, ipn.Starting, false},
		{"joined", ipn.Starting, ipn.Running, false},
		{"stopped", ipn.Running, ipn.Stopped, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, enteredNeedsLogin(tc.prev, tc.next))
		})
	}
}

func TestSelfMachineID(t *testing.T) {
	assert.Equal(t, "", selfMachineID(nil))
	assert.Equal(t, "", selfMachineID(&ipnstate.Status{}))
	assert.Equal(t, "42", selfMachineID(&ipnstate.Status{
		Self: &ipnstate.PeerStatus{ID: tailcfg.StableNodeID("42")},
	}))
}

func TestSelfLoginName(t *testing.T) {
	users := map[tailcfg.UserID]tailcfg.UserProfile{
		1:          {LoginName: "rune-a"},
		2147455555: {LoginName: "tagged-devices"},
	}
	tags := func(tags ...string) *views.Slice[string] {
		v := views.SliceOf(tags)
		return &v
	}
	cases := []struct {
		name string
		self *ipnstate.PeerStatus
		want string
	}{
		{"not logged in", nil, ""},
		{"owned by an account",
			&ipnstate.PeerStatus{UserID: 1}, "rune-a"},
		{"serve-only machine of an account",
			&ipnstate.PeerStatus{UserID: 2147455555, Tags: tags("tag:serve-rune-a")},
			"rune-a"},
		{"serve tag among others",
			&ipnstate.PeerStatus{UserID: 2147455555, Tags: tags("tag:ci", "tag:serve-rune-a")},
			"rune-a"},
		{"tagged without a serve tag serves no account",
			&ipnstate.PeerStatus{UserID: 2147455555, Tags: tags("tag:ci")},
			"tagged-devices"},
		{"empty serve tag serves no account",
			&ipnstate.PeerStatus{UserID: 2147455555, Tags: tags("tag:serve-")},
			"tagged-devices"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, selfLoginName(&ipnstate.Status{Self: tc.self, User: users}))
		})
	}
}

func TestStatusBeforeStart(t *testing.T) {
	node := New(Config{
		Hostname: "workstation",
		Port:     DefaultPort,
		Dir:      t.TempDir(),
	})
	t.Cleanup(func() { _ = node.Close() })

	st, err := node.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "workstation", st.Hostname)
	assert.Equal(t, "NotRunning", st.State)
	assert.Empty(t, st.LastError)
}

func TestIdentityDir(t *testing.T) {
	custom := func(url string) Config {
		return Config{ControlURL: url, CustomControl: true}
	}
	assert.Equal(t, "runenet", identityDir(Config{}))
	// The harnesses imitate Rune's network with a control URL of
	// their own; that is still Rune's network.
	assert.Equal(t, "runenet",
		identityDir(Config{ControlURL: "http://127.0.0.1:8080"}))

	dir := identityDir(custom("https://headscale.example.com"))
	assert.Regexp(t, `^runenet-[0-9a-f]{12}$`, dir)
	assert.Equal(t, dir, identityDir(custom("https://headscale.example.com")))
	assert.NotEqual(t, dir, identityDir(custom("https://other.example.com")))

	node := New(Config{Dir: "/data", ControlURL: "https://headscale.example.com",
		CustomControl: true})
	assert.Equal(t, filepath.Join("/data", dir), node.srv.Dir)
}

func TestNoteAuthURL(t *testing.T) {
	var reported []string
	node := New(Config{Dir: t.TempDir(), OnAuthURL: func(url string) {
		reported = append(reported, url)
	}})

	node.noteAuthURL("https://control.example.com/a/1")
	assert.Equal(t, "https://control.example.com/a/1", node.PendingAuthURL())
	// A re-established watch repeats the pending URL.
	node.noteAuthURL("https://control.example.com/a/1")
	node.clearAuthURL()
	assert.Empty(t, node.PendingAuthURL())
	node.noteAuthURL("https://control.example.com/a/2")

	assert.Equal(t, []string{
		"https://control.example.com/a/1", "https://control.example.com/a/2",
	}, reported)
}

func TestSelfOwner(t *testing.T) {
	users := map[tailcfg.UserID]tailcfg.UserProfile{
		1:          {LoginName: "alice@example.com"},
		2147455555: {LoginName: "tagged-devices"},
	}
	tags := func(tags ...string) *views.Slice[string] {
		v := views.SliceOf(tags)
		return &v
	}
	cases := []struct {
		name     string
		self     *ipnstate.PeerStatus
		wantUser string
	}{
		{"not logged in", nil, ""},
		{"owned by a user", &ipnstate.PeerStatus{UserID: 1}, "alice@example.com"},
		{"tagged", &ipnstate.PeerStatus{UserID: 2147455555, Tags: tags("tag:server")}, ""},
		// On the user's own tailnet a serve tag is just a tag.
		{"serve tag is not an account",
			&ipnstate.PeerStatus{UserID: 2147455555, Tags: tags("tag:serve-alice")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &ipnstate.Status{Self: tc.self, User: users}
			assert.Equal(t, tc.wantUser, selfOwner(st))
		})
	}
}

// startAuthTestControl runs an in-process coordination server that
// signs nodes in by URL, as the user's own Tailscale or Headscale
// does.
func startAuthTestControl(t *testing.T) (*testcontrol.Server, string) {
	t.Helper()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })

	control := &testcontrol.Server{
		DERPMap:          integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1"),
		RequireAuth:      true,
		AllNodesSameUser: true,
		Logf:             logger.Discard,
	}
	server := httptest.NewUnstartedServer(control)
	// The sign-in URLs it hands out are rooted here.
	control.HTTPTestServer = server
	server.Start()
	t.Cleanup(server.Close)
	return control, server.URL
}

// newCustomNode builds a node named hostname on the user's own server
// at controlURL, keeping its identity in dir. The returned channel
// receives every sign-in URL the node reports.
func newCustomNode(
	t *testing.T, hostname, controlURL, dir string,
) (*Node, <-chan string) {
	t.Helper()
	urls := make(chan string, 16)
	node := New(Config{
		Hostname:      hostname,
		Port:          DefaultPort,
		Dir:           dir,
		ControlURL:    controlURL,
		CustomControl: true,
		OnAuthURL:     func(url string) { urls <- url },
	})
	t.Cleanup(func() { _ = node.Close() })
	return node, urls
}

func nextAuthURL(t *testing.T, urls <-chan string) string {
	t.Helper()
	select {
	case url := <-urls:
		return url
	case <-time.After(joinTimeout):
		t.Fatal("the node never reported a sign-in URL")
		return ""
	}
}

// signIn completes the sign-in the node asks for next and waits for it
// to join.
func signIn(
	t *testing.T, control *testcontrol.Server, node *Node, urls <-chan string,
) string {
	t.Helper()
	url := nextAuthURL(t, urls)
	require.True(t, control.CompleteAuth(url), "unknown sign-in URL %s", url)
	ctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
	defer cancel()
	require.NoError(t, node.Up(ctx))
	return url
}

func waitStatus(t *testing.T, node *Node, ok func(Status) bool) {
	t.Helper()
	var last Status
	require.Eventually(t, func() bool {
		st, err := node.Status(context.Background())
		last = st
		return err == nil && ok(st)
	}, joinTimeout, 100*time.Millisecond, "last status: %+v", last)
}

func TestCustomControlSignIn(t *testing.T) {
	control, controlURL := startAuthTestControl(t)
	node, urls := newCustomNode(t, "workstation", controlURL, t.TempDir())

	// Up waits for the sign-in, as `rune --headless --control-url` does.
	require.NoError(t, node.Start(context.Background()))
	up := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		ctx, cancel := context.WithTimeout(context.Background(), joinTimeout)
		defer cancel()
		up <- node.Up(ctx)
	})
	url := nextAuthURL(t, urls)
	assert.Contains(t, url, controlURL)
	require.True(t, control.CompleteAuth(url))
	require.NoError(t, <-up)

	requireJoined(t, node, controlURL)
	require.Eventually(t, func() bool { return node.PendingAuthURL() == "" },
		joinTimeout, 50*time.Millisecond)
	st, err := node.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "user-123@fake-control.example.net", st.LoginName)

	// Signed in, the machine must not sign in again: a new node key
	// would leave its peers holding the old one.
	assert.Never(t, func() bool {
		return len(control.AllNodes()) > 1 || len(urls) > 0
	}, 2*time.Second, 50*time.Millisecond, "the machine signed in again")
}

func TestCustomControlIgnoresEnvCredentials(t *testing.T) {
	_, controlURL := startAuthTestControl(t)
	for _, name := range envCredentials {
		t.Setenv(name, "tskey-from-the-environment")
	}
	node, urls := newCustomNode(t, "workstation", controlURL, t.TempDir())

	require.NoError(t, node.Start(context.Background()))
	for _, name := range envCredentials {
		_, ok := os.LookupEnv(name)
		assert.False(t, ok, "%s is still set", name)
	}
	nextAuthURL(t, urls)
}

// setKeyExpiry has control expire the key of each of its machines at
// expiry.
func setKeyExpiry(control *testcontrol.Server, expiry time.Time) {
	for _, n := range control.AllNodes() {
		n.KeyExpiry = expiry
		control.UpdateNode(n)
	}
}

func TestCustomControlKeyExpiry(t *testing.T) {
	control, controlURL := startAuthTestControl(t)
	node, urls := newCustomNode(t, "workstation", controlURL, t.TempDir())
	require.NoError(t, node.Start(context.Background()))
	first := signIn(t, control, node, urls)

	setKeyExpiry(control, time.Now().Add(-time.Minute))
	waitStatus(t, node, func(st Status) bool {
		return st.State == ipn.NeedsLogin.String()
	})

	second := nextAuthURL(t, urls)
	assert.NotEqual(t, first, second)
	assert.Equal(t, second, node.PendingAuthURL())
	// The server signs the machine in again with a fresh expiry, where
	// the in-process one carries the expired key's over.
	setKeyExpiry(control, time.Time{})
	require.True(t, control.CompleteAuth(second))
	waitStatus(t, node, func(st Status) bool { return st.State == StateRunning })
}

type credentialsCtxKey struct{}
