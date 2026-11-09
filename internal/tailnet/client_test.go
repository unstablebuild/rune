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
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/netmap"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/tailnet/tailnettest"
)

// fakeLocalAPI serves the slice of a Tailscale client's LocalAPI this
// package reads, from a network map the test sets. Its notification bus
// stays open, and carries what the test pushes.
type fakeLocalAPI struct {
	requests atomic.Int32
	// mapReads counts the reads of the whole network map.
	mapReads atomic.Int32

	mu       sync.Mutex
	state    ipn.State
	self     *tailcfg.Node
	peers    []*tailcfg.Node
	users    map[tailcfg.UserID]tailcfg.UserProfile
	ssh      *tailcfg.SSHPolicy
	caps     map[tailcfg.NodeID]tailcfg.PeerCapMap
	watchers map[chan ipn.Notify]struct{}
}

func newFakeLocalAPI(t *testing.T, self *tailcfg.Node, peers ...*tailcfg.Node) (*fakeLocalAPI, *local.Client) {
	t.Helper()
	f := &fakeLocalAPI{
		state: ipn.Running,
		self:  self,
		peers: peers,
		users: map[tailcfg.UserID]tailcfg.UserProfile{
			alice:  {ID: alice, LoginName: "alice@example.com"},
			bob:    {ID: bob, LoginName: "bob@example.com"},
			tagged: {ID: tagged, LoginName: "tagged-devices"},
		},
		watchers: make(map[chan ipn.Notify]struct{}),
	}
	server := httptest.NewServer(f)
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	lc := &local.Client{
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", server.Listener.Addr().String())
		},
		OmitAuth: true,
	}
	return f, lc
}

func (f *fakeLocalAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	if r.URL.Path == "/localapi/v0/watch-ipn-bus" {
		f.watch(w, r)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/localapi/v0/status":
		st := &ipnstate.Status{BackendState: f.state.String()}
		for _, p := range f.self.Addresses {
			st.TailscaleIPs = append(st.TailscaleIPs, p.Addr())
		}
		_ = json.NewEncoder(w).Encode(st)
	case "/localapi/v0/whois":
		addr, err := netip.ParseAddrPort(r.FormValue("addr"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, n := range append([]*tailcfg.Node{f.self}, f.peers...) {
			for _, p := range n.Addresses {
				if p.Addr() != addr.Addr() {
					continue
				}
				profile := f.users[n.User]
				_ = json.NewEncoder(w).Encode(apitype.WhoIsResponse{
					Node: n, UserProfile: &profile, CapMap: f.caps[n.ID],
				})
				return
			}
		}
		http.Error(w, "not found", http.StatusNotFound)
	default:
		http.Error(w, "unexpected "+r.URL.Path, http.StatusNotImplemented)
	}
}

// watch serves the notification bus: the current state, and the map
// when asked for it, then whatever the test pushes.
func (f *fakeLocalAPI) watch(w http.ResponseWriter, r *http.Request) {
	mask, err := strconv.ParseUint(r.FormValue("mask"), 10, 64)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pushed := make(chan ipn.Notify, 16)
	f.mu.Lock()
	state := f.state
	initial := ipn.Notify{State: &state}
	if ipn.NotifyWatchOpt(mask)&ipn.NotifyInitialNetMap != 0 {
		f.mapReads.Add(1)
		initial.NetMap = f.netMapLocked()
	}
	f.watchers[pushed] = struct{}{}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		delete(f.watchers, pushed)
		f.mu.Unlock()
	}()
	enc := json.NewEncoder(w)
	for n := initial; ; {
		if err := enc.Encode(n); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			return
		case n = <-pushed:
		}
	}
}

func (f *fakeLocalAPI) netMapLocked() *netmap.NetworkMap {
	peers := make([]tailcfg.NodeView, 0, len(f.peers))
	for _, p := range f.peers {
		peers = append(peers, p.View())
	}
	users := make(map[tailcfg.UserID]tailcfg.UserProfileView, len(f.users))
	for id, u := range f.users {
		users[id] = u.View()
	}
	return &netmap.NetworkMap{
		SelfNode:     f.self.View(),
		Peers:        peers,
		UserProfiles: users,
		SSHPolicy:    f.ssh,
	}
}

// push sends n to every open notification bus.
func (f *fakeLocalAPI) push(n ipn.Notify) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for w := range f.watchers {
		w <- n
	}
}

func (f *fakeLocalAPI) set(update func(f *fakeLocalAPI)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	update(f)
}

const (
	alice  tailcfg.UserID = 1
	bob    tailcfg.UserID = 2
	tagged tailcfg.UserID = 3
)

// testNode is machine id of the tailnet, named name, owned by user, and
// online. Tagged machines belong to the tagged pseudo-user.
func testNode(id int, name string, user tailcfg.UserID, tags ...string) *tailcfg.Node {
	if len(tags) > 0 {
		user = tagged
	}
	online := true
	return &tailcfg.Node{
		ID:           tailcfg.NodeID(id),
		StableID:     tailcfg.StableNodeID(fmt.Sprintf("node%d", id)),
		Name:         name + ".tailnet.ts.net.",
		ComputedName: name,
		User:         user,
		Tags:         tags,
		Addresses:    []netip.Prefix{netip.PrefixFrom(testAddr(id), 32)},
		Online:       &online,
		Hostinfo:     (&tailcfg.Hostinfo{Hostname: name, OS: "linux"}).View(),
	}
}

func testAddr(id int) netip.Addr {
	return netip.AddrFrom4([4]byte{100, 64, 0, byte(id)})
}

// stubDialer stands in for the tailnet: machines in serving accept a
// connection on the workspace port, those in silent never answer, and
// every other address refuses it.
type stubDialer struct {
	mu      sync.Mutex
	serving map[netip.Addr]bool
	silent  map[netip.Addr]bool
	dialed  []string
}

func (d *stubDialer) dial(ctx context.Context, _, address string) (net.Conn, error) {
	d.mu.Lock()
	d.dialed = append(d.dialed, address)
	addr, err := netip.ParseAddrPort(address)
	if err != nil {
		d.mu.Unlock()
		return nil, err
	}
	silent, serving := d.silent[addr.Addr()], d.serving[addr.Addr()]
	d.mu.Unlock()
	if silent {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if !serving {
		return nil, errors.New("connection refused")
	}
	client, server := net.Pipe()
	_ = server.Close()
	return client, nil
}

func (d *stubDialer) calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dialed...)
}

// set changes which machines serve and which never answer.
func (d *stubDialer) set(serving, silent map[netip.Addr]bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.serving, d.silent = serving, silent
}

func (d *stubDialer) dialedTo(addr string) int {
	n := 0
	for _, c := range d.calls() {
		if c == addr {
			n++
		}
	}
	return n
}

func workspaceAddr(id int) string {
	return netip.AddrPortFrom(testAddr(id), runenet.DefaultPort).String()
}

// newTestClient is a Client of lc reaching the tailnet through dialer,
// closed when the test ends.
func newTestClient(t *testing.T, lc *local.Client, dialer *stubDialer) *Client {
	t.Helper()
	c := New(Config{LocalClient: lc, Dial: dialer.dial})
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func peerNames(peers []runenet.Peer) []string {
	var ret []string
	for _, p := range peers {
		ret = append(ret, p.Hostname)
	}
	return ret
}

func TestClientPeers(t *testing.T) {
	headless := testNode(6, "headless", 0, "tag:server")
	headless.Hostinfo = (&tailcfg.Hostinfo{Hostname: "headless", App: runenet.App}).View()
	asleep := testNode(7, "asleep", alice)
	asleep.Online = nil
	nameless := testNode(8, "", alice)
	nameless.Name = ""
	nameless.Hostinfo = (&tailcfg.Hostinfo{Hostname: "nameless"}).View()
	noAddress := testNode(9, "no-address", alice)
	noAddress.Addresses = nil
	peers := []*tailcfg.Node{
		testNode(2, "desktop", alice),
		testNode(3, "phone", alice),
		testNode(4, "bobs-laptop", bob),
		testNode(5, "server", 0, "tag:server"),
		headless, asleep, nameless, noAddress,
	}
	serving := map[netip.Addr]bool{
		testAddr(2): true, testAddr(4): true, testAddr(5): true,
		testAddr(7): true, testAddr(8): true, testAddr(9): true,
	}

	cases := []struct {
		name       string
		self       *tailcfg.Node
		want       []string
		wantProbed []string
	}{
		{
			name: "a user's machine",
			self: testNode(1, "laptop", alice),
			// Bob's laptop would refuse alice, so it is not asked;
			// Rune's own node is known to serve without asking.
			want:       []string{"desktop", "headless", "nameless", "server"},
			wantProbed: []string{workspaceAddr(2), workspaceAddr(3), workspaceAddr(5), workspaceAddr(8)},
		},
		{
			name: "a tagged machine",
			self: testNode(1, "ci", 0, "tag:ci"),
			// A user's machines never let a tagged one in.
			want:       []string{"headless", "server"},
			wantProbed: []string{workspaceAddr(5)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, lc := newFakeLocalAPI(t, tc.self, peers...)
			dialer := &stubDialer{serving: serving}
			c := newTestClient(t, lc, dialer)

			got, err := c.Peers(context.Background())
			require.NoError(t, err)
			for _, p := range got {
				assert.True(t, p.Online)
			}
			assert.Equal(t, tc.want, peerNames(got))
			assert.ElementsMatch(t, tc.wantProbed, dialer.calls())

			// The answers are remembered.
			again, err := c.Peers(context.Background())
			require.NoError(t, err)
			assert.Equal(t, got, again)
			assert.Len(t, dialer.calls(), len(tc.wantProbed))
		})
	}

	laptop := testNode(1, "laptop", alice)
	desktop := testNode(2, "desktop", alice)
	phone := testNode(3, "phone", alice)
	silentDesktop := func() *stubDialer {
		return &stubDialer{
			serving: map[netip.Addr]bool{testAddr(3): true},
			silent:  map[netip.Addr]bool{testAddr(2): true},
		}
	}
	answered := func(c *Client, p *tailcfg.Node, want probeAnswer) func() bool {
		return func() bool { return c.answer(p.View()) == want }
	}

	t.Run("a machine that does not answer does not hold up the list", func(t *testing.T) {
		_, lc := newFakeLocalAPI(t, laptop, desktop, phone)
		dialer := silentDesktop()
		c := newTestClient(t, lc, dialer)
		c.probeTimeout = time.Second
		c.listWait = 200 * time.Millisecond

		start := time.Now()
		got, err := c.Peers(context.Background())
		require.NoError(t, err)
		assert.Equal(t, []string{"phone"}, peerNames(got))
		assert.Less(t, time.Since(start), c.probeTimeout)

		// Once it failed to answer in time, it is not waited for at all.
		require.Eventually(t, answered(c, desktop, probeSilent), 5*time.Second, 10*time.Millisecond)
		c.listWait = time.Hour
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		got, err = c.Peers(ctx)
		require.NoError(t, err)
		assert.Equal(t, []string{"phone"}, peerNames(got))
		assert.Equal(t, 1, dialer.dialedTo(workspaceAddr(2)), "asked again too soon")
	})

	t.Run("a machine that did not answer is asked again later", func(t *testing.T) {
		_, lc := newFakeLocalAPI(t, laptop, desktop, phone)
		dialer := silentDesktop()
		c := newTestClient(t, lc, dialer)
		c.probeTimeout = 100 * time.Millisecond
		c.silentTTL = 0
		_, err := c.Peers(context.Background())
		require.NoError(t, err)
		require.Eventually(t, answered(c, desktop, probeSilent), 5*time.Second, 10*time.Millisecond)

		dialer.set(map[netip.Addr]bool{testAddr(2): true, testAddr(3): true}, nil)
		require.Eventually(t, func() bool {
			got, err := c.Peers(context.Background())
			return err == nil && slices.Equal([]string{"desktop", "phone"}, peerNames(got))
		}, 5*time.Second, 10*time.Millisecond)
	})

	t.Run("a machine known to serve is asked again without waiting", func(t *testing.T) {
		_, lc := newFakeLocalAPI(t, laptop, desktop)
		dialer := &stubDialer{serving: map[netip.Addr]bool{testAddr(2): true}}
		c := newTestClient(t, lc, dialer)
		c.probeTTL = 0
		c.probeTimeout = 200 * time.Millisecond
		got, err := c.Peers(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"desktop"}, peerNames(got))

		// It stopped serving: listed once more while it is asked, then
		// left out.
		dialer.set(nil, map[netip.Addr]bool{testAddr(2): true})
		c.listWait = time.Hour
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		got, err = c.Peers(ctx)
		require.NoError(t, err)
		assert.Equal(t, []string{"desktop"}, peerNames(got))
		dialer.set(nil, nil)
		require.Eventually(t, func() bool {
			got, err := c.Peers(ctx)
			return err == nil && len(got) == 0
		}, 5*time.Second, 10*time.Millisecond)
	})

	t.Run("machines are probed before they are listed", func(t *testing.T) {
		f, lc := newFakeLocalAPI(t, laptop, desktop, phone)
		dialer := &stubDialer{}
		c := New(Config{
			LocalClient: lc,
			Dial:        dialer.dial,
			Listen: func(ctx context.Context, _, _ string) (net.Listener, error) {
				var lc net.ListenConfig
				return lc.Listen(ctx, "tcp", "127.0.0.1:0")
			},
		})
		t.Cleanup(func() { _ = c.Close() })
		c.prewarmInterval = 10 * time.Millisecond
		lis := c.Listen()
		t.Cleanup(func() { _ = lis.Close() })
		probed := func(ids ...int) func() bool {
			return func() bool {
				for _, id := range ids {
					if dialer.dialedTo(workspaceAddr(id)) == 0 {
						return false
					}
				}
				return true
			}
		}
		require.Eventually(t, probed(2, 3), 5*time.Second, 10*time.Millisecond)

		studio := testNode(4, "studio", alice)
		f.set(func(f *fakeLocalAPI) { f.peers = append(f.peers, studio) })
		f.push(ipn.Notify{PeersChanged: []*tailcfg.Node{studio}})
		require.Eventually(t, probed(4), 5*time.Second, 10*time.Millisecond)
		assert.Equal(t, 1, dialer.dialedTo(workspaceAddr(2)), "probed again")
	})

	t.Run("follows the tailnet", func(t *testing.T) {
		f, lc := newFakeLocalAPI(t, laptop, desktop)
		dialer := &stubDialer{serving: map[netip.Addr]bool{testAddr(2): true, testAddr(4): true}}
		c := newTestClient(t, lc, dialer)
		c.snaps.ttl = time.Hour
		got, err := c.Peers(context.Background())
		require.NoError(t, err)
		require.Equal(t, []string{"desktop"}, peerNames(got))

		studio := testNode(4, "studio", alice)
		f.set(func(f *fakeLocalAPI) { f.peers = append(f.peers, studio) })
		f.push(ipn.Notify{PeersChanged: []*tailcfg.Node{studio}})
		require.Eventually(t, func() bool {
			got, err := c.Peers(context.Background())
			return err == nil && slices.Equal([]string{"desktop", "studio"}, peerNames(got))
		}, 5*time.Second, 10*time.Millisecond)
	})

	t.Run("describes each machine", func(t *testing.T) {
		_, lc := newFakeLocalAPI(t, testNode(1, "laptop", alice), testNode(2, "desktop", alice))
		c := newTestClient(t, lc, &stubDialer{serving: serving})
		got, err := c.Peers(context.Background())
		require.NoError(t, err)
		assert.Equal(t, []runenet.Peer{{
			Hostname: "desktop",
			DNSName:  "desktop.tailnet.ts.net",
			Addrs:    []netip.Addr{testAddr(2)},
			OS:       "linux",
			Online:   true,
		}}, got)
	})

	t.Run("fails while Tailscale is not connected", func(t *testing.T) {
		f, lc := newFakeLocalAPI(t, testNode(1, "laptop", alice), peers...)
		f.set(func(f *fakeLocalAPI) { f.state = ipn.NeedsLogin })
		_, err := newTestClient(t, lc, &stubDialer{}).Peers(context.Background())
		assert.ErrorIs(t, err, ErrNotRunning)
	})

	t.Run("fails without a Tailscale client", func(t *testing.T) {
		lc := &local.Client{Socket: "/nonexistent/tailscaled.sock", UseSocketOnly: true}
		_, err := newTestClient(t, lc, &stubDialer{}).Peers(context.Background())
		assert.ErrorIs(t, err, ErrNotRunning)
	})
}

func TestClientDial(t *testing.T) {
	cases := []struct {
		name    string
		peer    string
		state   ipn.State
		want    string
		wantErr error
	}{
		{name: "by label", peer: "desktop", want: workspaceAddr(2)},
		{name: "by name", peer: "desktop.tailnet.ts.net", want: workspaceAddr(2)},
		{name: "by fully qualified name", peer: "desktop.tailnet.ts.net.", want: workspaceAddr(2)},
		{name: "names are case insensitive", peer: "Desktop", want: workspaceAddr(2)},
		{name: "by address", peer: testAddr(9).String(), want: workspaceAddr(9)},
		{name: "a machine the tailnet does not know", peer: "nope",
			wantErr: runenet.ErrPeerNotFound},
		{name: "while Tailscale is not connected", peer: "desktop",
			state: ipn.Stopped, wantErr: ErrNotRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, lc := newFakeLocalAPI(t, testNode(1, "laptop", alice),
				testNode(2, "desktop", alice), testNode(3, "phone", alice))
			if tc.state != ipn.NoState {
				f.set(func(f *fakeLocalAPI) { f.state = tc.state })
			}
			dialer := &stubDialer{serving: map[netip.Addr]bool{
				testAddr(2): true, testAddr(9): true,
			}}
			c := newTestClient(t, lc, dialer)

			conn, err := c.Dial(context.Background(), tc.peer)
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Empty(t, dialer.calls())
				return
			}
			require.NoError(t, err)
			_ = conn.Close()
			assert.Equal(t, []string{tc.want}, dialer.calls())
		})
	}

	t.Run("a machine that joined after the map was read", func(t *testing.T) {
		f, lc := newFakeLocalAPI(t, testNode(1, "laptop", alice), testNode(2, "desktop", alice))
		dialer := &stubDialer{serving: map[netip.Addr]bool{testAddr(2): true, testAddr(4): true}}
		c := newTestClient(t, lc, dialer)
		c.snaps.ttl = time.Hour
		conn, err := c.Dial(context.Background(), "desktop")
		require.NoError(t, err)
		_ = conn.Close()

		f.set(func(f *fakeLocalAPI) { f.peers = append(f.peers, testNode(4, "studio", alice)) })
		conn, err = c.Dial(context.Background(), "studio")
		require.NoError(t, err)
		_ = conn.Close()
		assert.Equal(t, int32(2), f.mapReads.Load())

		conn, err = c.Dial(context.Background(), "desktop")
		require.NoError(t, err)
		_ = conn.Close()
		assert.Equal(t, int32(2), f.mapReads.Load(), "a fresh map is reused")
	})
}

// tailnetClient is a Client on m, reaching the tailnet as the
// operating system's network does on a machine running Tailscale.
func tailnetClient(m *tailnettest.Machine) *Client {
	return New(Config{LocalClient: m.LocalClient, Listen: m.Listen, Dial: m.Dial})
}

// serving reports whether something listens on the workspace port of
// addr, one of m's own addresses: tsnet refuses a second listener on
// an address.
func serving(m *tailnettest.Machine, addr netip.Addr) bool {
	lis, err := m.Server.Listen("tcp", netip.AddrPortFrom(addr, runenet.DefaultPort).String())
	if err != nil {
		return true
	}
	_ = lis.Close()
	return false
}

func TestClientListen(t *testing.T) {
	tn := tailnettest.Start(t)
	server := tn.Join(t, "server")
	caller := tn.Join(t, "caller")
	addrs := server.Addrs(t)
	require.Len(t, addrs, 2, "an IPv4 and an IPv6 address")
	callerAddrs := caller.Addrs(t)

	lis := tailnetClient(server).Listen()
	t.Cleanup(func() { _ = lis.Close() })
	accepted := make(chan net.Conn)
	go debug.CapturePanicReport(func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				close(accepted)
				return
			}
			accepted <- conn
		}
	})

	reach := func(addr netip.Addr) {
		t.Helper()
		var conn net.Conn
		var err error
		require.Eventually(t, func() bool {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err = caller.Dial(ctx, "tcp",
				netip.AddrPortFrom(addr, runenet.DefaultPort).String())
			return err == nil
		}, tailnettest.Timeout, 100*time.Millisecond, "dial %s: %v", addr, err)
		defer conn.Close()
		select {
		case got := <-accepted:
			remote, err := netip.ParseAddrPort(got.RemoteAddr().String())
			require.NoError(t, err)
			assert.Contains(t, callerAddrs, remote.Addr())
			_ = got.Close()
		case <-time.After(tailnettest.Timeout):
			t.Fatalf("the listener never accepted the connection to %s", addr)
		}
	}
	for _, addr := range addrs {
		reach(addr)
	}

	t.Run("lets go of the addresses while Tailscale is stopped", func(t *testing.T) {
		server.SetWantRunning(t, false)
		for _, addr := range addrs {
			require.Eventually(t, func() bool { return !serving(server, addr) },
				tailnettest.Timeout, 100*time.Millisecond, "still bound to %s", addr)
		}
		server.SetWantRunning(t, true)
		reach(addrs[0])
	})

	t.Run("close", func(t *testing.T) {
		require.NoError(t, lis.Close())
		_, ok := <-accepted
		assert.False(t, ok, "Accept returns once closed")
		for _, addr := range addrs {
			assert.False(t, serving(server, addr), "still bound to %s", addr)
		}
		assert.Equal(t, runenet.DefaultPort, lis.Addr().(*net.TCPAddr).Port)
	})
}
