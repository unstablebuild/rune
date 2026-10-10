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
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/runenet"
)

// Endpoint is a port on a mesh node that reachability is asserted
// against.
type Endpoint struct {
	Name string
	Addr netip.Addr
	Port int
	// accepts records the callers the listener behind the endpoint
	// accepted. Nil when the test cannot observe the listener, in
	// which case only the caller's side is checked.
	accepts *acceptLog
}

type acceptLog struct {
	mu   sync.Mutex
	from []netip.Addr
}

func (l *acceptLog) record(addr netip.Addr) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.from = append(l.from, addr.Unmap())
}

func (l *acceptLog) has(addr netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Contains(l.from, addr.Unmap())
}

// ListenProbe accepts bare connections on port through node and
// records each caller, so a test can tell whether a peer's packets
// reached the node at all rather than whether an application let them
// through.
func ListenProbe(t *testing.T, node *runenet.Node, port int) Endpoint {
	t.Helper()

	lis, err := node.ListenPort(port)
	if err != nil {
		t.Fatalf("listen on mesh port %d: %v", port, err)
	}
	t.Cleanup(func() { _ = lis.Close() })

	accepts := &acceptLog{}
	go debug.CapturePanicReport(func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			if from, err := netip.ParseAddrPort(conn.RemoteAddr().String()); err == nil {
				accepts.record(from.Addr())
			}
			_ = conn.Close()
		}
	})
	st := nodeStatus(t, node)
	return Endpoint{
		Name: st.Hostname, Addr: meshAddr(t, st), Port: port, accepts: accepts,
	}
}

// AssertReachable fails the test unless from can open a connection to
// to, retrying while the mesh converges.
func AssertReachable(t *testing.T, from *runenet.Node, to Endpoint) {
	t.Helper()

	st := nodeStatus(t, from)
	ctx, cancel := context.WithTimeout(context.Background(), nodeJoinTimeout)
	defer cancel()
	for {
		dialCtx, dialCancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := from.DialPort(dialCtx, to.Addr.String(), to.Port)
		dialCancel()
		if err == nil {
			_ = conn.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("%s cannot reach %s (%s:%d): %v",
				st.Hostname, to.Name, to.Addr, to.Port, err)
		case <-time.After(200 * time.Millisecond):
		}
	}
	if to.accepts == nil {
		return
	}
	from4 := meshAddr(t, st)
	for !to.accepts.has(from4) {
		select {
		case <-ctx.Done():
			t.Fatalf("%s connected to %s:%d but the listener never saw it",
				st.Hostname, to.Name, to.Port)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// unreachableWindow is how long a connection attempt that must fail is
// given to succeed. Reachable pairs on the test host connect in well
// under a second once the mesh is up, which every caller establishes
// first with [AssertReachable].
const unreachableWindow = 10 * time.Second

// AssertUnreachable fails the test if from can open a connection to to,
// or if to's listener accepts one from it. Callers must first show both
// nodes are on the mesh, so a blocked connection is never a mesh that
// failed to come up.
func AssertUnreachable(t *testing.T, from *runenet.Node, to Endpoint) {
	t.Helper()

	st := nodeStatus(t, from)
	ctx, cancel := context.WithTimeout(context.Background(), unreachableWindow)
	defer cancel()
	conn, err := from.DialPort(ctx, to.Addr.String(), to.Port)
	if err == nil {
		_ = conn.Close()
		t.Errorf("%s reached %s (%s:%d); the policy must block it",
			st.Hostname, to.Name, to.Addr, to.Port)
		return
	}
	if to.accepts == nil {
		return
	}
	// A connection the dial gave up on can still land just after.
	time.Sleep(time.Second)
	if to.accepts.has(meshAddr(t, st)) {
		t.Errorf("%s's packets reached %s:%d although its dial failed (%v)",
			st.Hostname, to.Name, to.Port, err)
	}
}

// CloneNodeState copies the network identity kept under the Rune data
// directory src into a new data directory and returns it: what someone
// who copied a machine's disk would hold.
func CloneNodeState(t *testing.T, src string) string {
	t.Helper()

	dst := t.TempDir()
	err := os.CopyFS(filepath.Join(dst, "runenet"),
		os.DirFS(filepath.Join(src, "runenet")))
	if err != nil {
		t.Fatalf("clone network state from %s: %v", src, err)
	}
	return dst
}

func nodeStatus(t *testing.T, node *runenet.Node) runenet.Status {
	t.Helper()
	st, err := node.Status(context.Background())
	if err != nil {
		t.Fatalf("network status: %v", err)
	}
	return st
}

// meshAddr is the node's IPv4 mesh address, the one a dial to another
// node's IPv4 address originates from.
func meshAddr(t *testing.T, st runenet.Status) netip.Addr {
	t.Helper()
	for _, addr := range st.Addrs {
		if addr.Is4() {
			return addr
		}
	}
	t.Fatalf("node %q has no IPv4 mesh address: %v", st.Hostname, st.Addrs)
	return netip.Addr{}
}
