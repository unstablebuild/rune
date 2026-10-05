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

package runetest

import (
	"context"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"
	"unstable.build/rune/internal/runenet"
)

// StartTestControl runs an in-process coordination server plus a local
// DERP and STUN server. It is the docker-free stand-in for Headscale:
// the nodes still speak the real control protocol and still carry real
// WireGuard traffic, so everything above the coordination server is
// exercised exactly as in production.
//
// sameUser controls whether the registered nodes belong to one account,
// which is what [runenet.PeerAuthorizer] admits or rejects on.
func StartTestControl(t *testing.T, sameUser bool) ControlPlane {
	t.Helper()

	// Tests must not bind their sockets to a physical interface: the
	// nodes talk to each other over loopback.
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })

	control := &testcontrol.Server{
		DERPMap:          integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1"),
		MagicDNSDomain:   "rune.test",
		AllNodesSameUser: sameUser,
		Logf:             logger.Discard,
	}
	server := httptest.NewUnstartedServer(control)
	server.Start()
	t.Cleanup(server.Close)

	return ControlPlane{URL: server.URL, testControl: control}
}

// SetNodeTags retags node on the in-process control plane and waits
// until observer's netmap carries the new tags. The node keeps its
// owning account, which is the shape of a policy that was misapplied:
// a tagged machine whose packets still reach this one.
func (c ControlPlane) SetNodeTags(
	t *testing.T, node, observer *runenet.Node, tags ...string,
) {
	t.Helper()
	if c.testControl == nil {
		t.Fatalf("control plane at %s is not in-process", c.URL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), nodeJoinTimeout)
	defer cancel()
	st, err := node.Status(ctx)
	if err != nil || st.MachineID == "" || len(st.Addrs) == 0 {
		t.Fatalf("node %q has not registered: %+v: %v", st.Hostname, st, err)
	}
	var target *tailcfg.Node
	for _, n := range c.testControl.AllNodes() {
		if string(n.StableID) == st.MachineID {
			target = n
		}
	}
	if target == nil {
		t.Fatalf("control plane has no node %s", st.MachineID)
	}
	target.Tags = tags
	c.testControl.UpdateNode(target)

	addr := st.Addrs[0].String()
	for {
		caller, err := observer.WhoIs(ctx, addr)
		if err == nil && slices.Equal(caller.Tags, tags) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("observer never saw %s tagged %v (last: %+v, %v)",
				addr, tags, caller, err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
