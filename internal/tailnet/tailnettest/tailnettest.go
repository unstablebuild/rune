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

// Package tailnettest runs an in-process tailnet for tests: a
// coordination server and machines on it, each a tsnet node standing in
// for a machine's own Tailscale client. The machines still speak the
// real control protocol and carry real WireGuard traffic, over
// loopback, so everything above the Tailscale client runs as it does
// on a real tailnet.
package tailnettest

import (
	"context"
	"net"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"
	"unstable.build/rune/internal/debug"
)

// Timeout bounds how long a machine takes to join, and a change made on
// the coordination server takes to reach the machines.
const Timeout = 30 * time.Second

// Tailnet is an in-process tailnet. Each machine that joins it is owned
// by a user of its own until SetOwner says otherwise.
type Tailnet struct {
	// Control is the coordination server. Its configuration may be
	// changed until the first machine joins: set RequireAuth to have
	// machines sign in by URL, and AllNodesSameUser to have one user
	// own them all.
	Control *testcontrol.Server
	URL     string
}

// Start runs an in-process coordination server, and a DERP and STUN
// server for it, until the test ends.
func Start(t *testing.T) *Tailnet {
	t.Helper()
	// The machines talk to each other over loopback.
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })
	control := &testcontrol.Server{
		DERPMap:        integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1"),
		MagicDNSDomain: "rune.test",
		AllOnline:      true,
		Logf:           logger.Discard,
	}
	server := httptest.NewUnstartedServer(control)
	// The sign-in URLs it hands out are rooted here.
	control.HTTPTestServer = server
	server.Start()
	t.Cleanup(server.Close)
	return &Tailnet{Control: control, URL: server.URL}
}

// Machine is a machine of a Tailnet.
type Machine struct {
	Hostname string
	Server   *tsnet.Server
	// LocalClient is the machine's Tailscale client API.
	LocalClient *local.Client
	// Addr is the machine's IPv4 tailnet address.
	Addr netip.Addr
	ID   tailcfg.StableNodeID
}

// Join starts a machine named hostname and waits for it to join,
// signing it in as its user would when the coordination server asks.
func (tn *Tailnet) Join(t *testing.T, hostname string) *Machine {
	t.Helper()
	srv := &tsnet.Server{
		Dir:        t.TempDir(),
		Hostname:   hostname,
		ControlURL: tn.URL,
		Logf:       logger.Discard,
		UserLogf:   logger.Discard,
	}
	t.Cleanup(func() { _ = srv.Close() })
	require.NoError(t, srv.Start())
	lc, err := srv.LocalClient()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()
	go debug.CapturePanicReport(func() { tn.signIn(ctx, lc) })
	st, err := srv.Up(ctx)
	require.NoError(t, err, "machine %s did not join", hostname)
	return &Machine{
		Hostname:    hostname,
		Server:      srv,
		LocalClient: lc,
		Addr:        st.TailscaleIPs[0],
		ID:          st.Self.ID,
	}
}

// signIn completes every sign-in lc's machine asks for until ctx ends.
func (tn *Tailnet) signIn(ctx context.Context, lc *local.Client) {
	for ctx.Err() == nil {
		if st, err := lc.StatusWithoutPeers(ctx); err == nil && st.AuthURL != "" {
			tn.Control.CompleteAuth(st.AuthURL)
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Listen listens on the machine's tailnet, as the operating system's
// network does on a machine where the Tailscale client runs.
func (m *Machine) Listen(_ context.Context, network, address string) (net.Listener, error) {
	return m.Server.Listen(network, address)
}

// Dial dials over the machine's tailnet.
func (m *Machine) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	return m.Server.Dial(ctx, network, address)
}

// SetWantRunning connects the machine's Tailscale client to the tailnet
// or disconnects it, as its user does from the client's menu.
func (m *Machine) SetWantRunning(t *testing.T, running bool) {
	t.Helper()
	_, err := m.LocalClient.EditPrefs(context.Background(), &ipn.MaskedPrefs{
		Prefs:          ipn.Prefs{WantRunning: running},
		WantRunningSet: true,
	})
	require.NoError(t, err)
}

// Addrs are all of the machine's tailnet addresses.
func (m *Machine) Addrs(t *testing.T) []netip.Addr {
	t.Helper()
	st, err := m.LocalClient.StatusWithoutPeers(context.Background())
	require.NoError(t, err)
	return st.TailscaleIPs
}

// Node is the coordination server's record of m.
func (tn *Tailnet) Node(t *testing.T, m *Machine) *tailcfg.Node {
	t.Helper()
	for _, n := range tn.Control.AllNodes() {
		if n.StableID == m.ID {
			return n
		}
	}
	t.Fatalf("the coordination server has no machine %s", m.Hostname)
	return nil
}

// SetOwner gives m to the user who owns owner.
func (tn *Tailnet) SetOwner(t *testing.T, m, owner *Machine) {
	t.Helper()
	user := tn.Node(t, owner).User
	n := tn.Node(t, m)
	n.User = user
	tn.Control.UpdateNode(n)
}

// SetTags tags m.
func (tn *Tailnet) SetTags(t *testing.T, m *Machine, tags ...string) {
	t.Helper()
	n := tn.Node(t, m)
	n.Tags = tags
	tn.Control.UpdateNode(n)
}

// WaitWhoIs waits until observer sees the machine at addr as ok says.
func WaitWhoIs(
	t *testing.T, observer *Machine, addr netip.Addr,
	ok func(*apitype.WhoIsResponse) bool,
) {
	t.Helper()
	require.Eventually(t, func() bool {
		who, err := observer.LocalClient.WhoIs(context.Background(), addr.String())
		return err == nil && ok(who)
	}, Timeout, 100*time.Millisecond, "%s never saw %s change", observer.Hostname, addr)
}

// WaitSelf waits until m sees itself as ok says.
func WaitSelf(t *testing.T, m *Machine, ok func(*tailcfg.Node) bool) {
	t.Helper()
	require.Eventually(t, func() bool {
		st, err := m.LocalClient.StatusWithoutPeers(context.Background())
		if err != nil || st.Self == nil {
			return false
		}
		who, err := m.LocalClient.WhoIs(context.Background(), st.TailscaleIPs[0].String())
		return err == nil && ok(who.Node)
	}, Timeout, 100*time.Millisecond, "%s never saw itself change", m.Hostname)
}
