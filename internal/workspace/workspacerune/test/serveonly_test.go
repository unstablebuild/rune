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

//go:build e2e

package runetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"unstable.build/rune/auth"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// servePolicy renders the ACL document the account server keeps on a
// shard where accounts own serve-only machines: every machine reaches
// the other machines of its own account, and each account's machines
// also reach its serve-only machines. No rule has a tag as its source,
// so a serve-only machine never opens a connection; it only answers.
func servePolicy(accounts ...string) string {
	var owners, rules strings.Builder
	for _, name := range accounts {
		tag := auth.ServeTagPrefix + name
		fmt.Fprintf(&owners, "    %q: [],\n", tag)
		fmt.Fprintf(&rules,
			"    {\"action\": \"accept\", \"src\": [%q], \"dst\": [%q]},\n",
			name+"@", tag+":*")
	}
	return "{\n" +
		"  \"tagOwners\": {\n" + owners.String() + "  },\n" +
		"  \"acls\": [\n" +
		"    {\"action\": \"accept\", \"src\": [\"autogroup:member\"], " +
		"\"dst\": [\"autogroup:self:*\"]},\n" +
		rules.String() +
		"  ],\n" +
		"}\n"
}

// meshNode is an in-process topology node with a probe listener on the
// workspace port and on an unrelated port.
type meshNode struct {
	*runenet.Node
	ws, probe Endpoint
}

func startMeshNode(t *testing.T, control ControlPlane, hostname, key string) meshNode {
	t.Helper()
	node := StartNode(t, control, hostname, key)
	return meshNode{
		Node:  node,
		ws:    ListenProbe(t, node, runenet.DefaultPort),
		probe: ListenProbe(t, node, ProbePort),
	}
}

func TestServeOnlyTopology(t *testing.T) {
	SkipIfNoDocker(t)

	control := StartHeadscale(t, WithPolicy(servePolicy("rune-a", "rune-b")))
	a := control.NewAccount(t, "rune-a")
	b := control.NewAccount(t, "rune-b")
	aUserKey, aServeKey := a.UserKey(t), a.ServeKey(t)
	bUserKey, bServeKey := b.UserKey(t), b.ServeKey(t)

	aUser1 := startMeshNode(t, control, "a-user1", aUserKey)
	aUser2 := startMeshNode(t, control, "a-user2", aUserKey)
	aServe1 := StartInstance(t, control, "a-serve1", aServeKey)
	aServe2 := startMeshNode(t, control, "a-serve2", aServeKey)
	bUser := startMeshNode(t, control, "b-user", bUserKey)
	bServe := startMeshNode(t, control, "b-serve", bServeKey)

	// Every node is shown to be on the mesh before anything is
	// asserted blocked; b-user has no peer allowed to reach it, so its
	// outbound connection stands in.
	t.Run("baseline", func(t *testing.T) {
		t.Run("1 account machines reach each other", func(t *testing.T) {
			AssertReachable(t, aUser1.Node, aUser2.ws)
			AssertReachable(t, aUser1.Node, aUser2.probe)
			AssertReachable(t, aUser2.Node, aUser1.ws)
			AssertReachable(t, aUser2.Node, aUser1.probe)
		})
		t.Run("2 account machines reach their serve nodes", func(t *testing.T) {
			AssertReachable(t, aUser1.Node, aServe1.WorkspaceEndpoint())
			AssertReachable(t, aUser1.Node, aServe1.ProbeEndpoint())
			AssertReachable(t, aUser2.Node, aServe1.ProbeEndpoint())
			AssertReachable(t, aUser1.Node, aServe2.ws)
			AssertReachable(t, aUser1.Node, aServe2.probe)
			AssertReachable(t, bUser.Node, bServe.ws)
			AssertReachable(t, bUser.Node, bServe.probe)
		})
	})
	if t.Failed() {
		t.FailNow()
	}

	t.Run("blocked", func(t *testing.T) {
		blocked := []struct {
			name string
			from *runenet.Node
			to   []Endpoint
		}{
			{"3 serve node cannot open a connection to a user machine",
				aServe2.Node, []Endpoint{aUser1.ws, aUser1.probe, aUser2.probe}},
			{"3 serve node cannot open a connection to a user machine (b)",
				bServe.Node, []Endpoint{bUser.ws, bUser.probe}},
			{"4 serve nodes cannot reach each other",
				aServe2.Node, []Endpoint{aServe1.WorkspaceEndpoint(), aServe1.ProbeEndpoint()}},
			{"5 serve node cannot cross accounts",
				aServe2.Node, []Endpoint{bUser.ws, bUser.probe, bServe.ws, bServe.probe}},
			{"6 other accounts cannot reach a serve node (user)",
				bUser.Node, []Endpoint{aServe1.WorkspaceEndpoint(), aServe1.ProbeEndpoint(), aServe2.probe}},
			{"6 other accounts cannot reach a serve node (serve)",
				bServe.Node, []Endpoint{aServe1.WorkspaceEndpoint(), aServe1.ProbeEndpoint()}},
			{"7 account isolation still holds",
				bUser.Node, []Endpoint{aUser1.ws, aUser1.probe}},
		}
		for _, tc := range blocked {
			for _, to := range tc.to {
				t.Run(fmt.Sprintf("%s/%s:%d", tc.name, to.Name, to.Port), func(t *testing.T) {
					t.Parallel()
					AssertUnreachable(t, tc.from, to)
				})
			}
		}
	})

	t.Run("8 the netmap matches the policy", func(t *testing.T) {
		assertPeers(t, aServe2.Node, "a-user1", "a-user2")
		assertPeers(t, bUser.Node, "b-serve")
		assertPeers(t, bServe.Node, "b-user")
		assertPeers(t, aUser1.Node, "a-serve1", "a-serve2", "a-user2")
	})

	t.Run("9 the node is registered as serve-only", func(t *testing.T) {
		serve := control.Node(t, "a-serve1")
		assert.Equal(t, []string{a.ServeTag()}, serve.Tags)
		assert.Equal(t, "tagged-devices", serve.User)

		user := control.Node(t, "a-user1")
		assert.Empty(t, user.Tags)
		assert.Equal(t, a.Name, user.User)
	})

	t.Run("11 serve node authorizes its own account", func(t *testing.T) {
		assert.Equal(t, a.Name, aServe1.LoginName,
			"a serve-only machine must know the account it serves")

		dir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "hello.txt"), []byte("served\n"), 0o600))
		scheme := OpenWorkspace(t, aUser1.Node, "a-serve1", dir)
		defer scheme.Close()
		entries, err := scheme.ReadDir(".")
		require.NoError(t, err, "a-user1 must be able to use a-serve1")
		require.Len(t, entries, 1)
		assert.Equal(t, "hello.txt", entries[0].Name())
	})

	t.Run("2 the workspace works over a serve node", func(t *testing.T) {
		SkipIfRace(t)
		newScheme := func(t *testing.T) schemeapi.Scheme {
			return OpenWorkspace(t, aUser1.Node, "a-serve1", t.TempDir())
		}
		workspacetest.TestWorkspaceSchemeFiles(t, newScheme)
		workspacetest.TestWorkspaceSchemeExecutor(t, newScheme)
	})

	// Last: it takes a-serve1 down and replaces it with a node started
	// from a copy of its disk.
	t.Run("10 stolen serve state stays serve-only", func(t *testing.T) {
		before := control.Node(t, "a-serve1")
		aServe1.Stop(t)
		stolen := startNodeIn(t, control, "a-serve1", "", CloneNodeState(t, aServe1.DataDir))

		after := control.Node(t, "a-serve1")
		assert.Equal(t, before.ID, after.ID, "the copy must be the same machine")
		assert.Equal(t, []string{a.ServeTag()}, after.Tags)

		AssertReachable(t, aUser1.Node, ListenProbe(t, stolen, ProbePort))
		AssertUnreachable(t, stolen, aUser1.ws)
		AssertUnreachable(t, stolen, aUser1.probe)
	})
}

// assertPeers checks node's netmap converges to exactly want.
func assertPeers(t *testing.T, node *runenet.Node, want ...string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var got []string
	for {
		peers, err := node.Peers(ctx)
		if err == nil {
			got = got[:0]
			for _, p := range peers {
				got = append(got, p.Hostname)
			}
			slices.Sort(got)
			if slices.Equal(got, want) {
				return
			}
		}
		select {
		case <-ctx.Done():
			st, _ := node.Status(context.Background())
			t.Errorf("%s sees peers %v, want %v", st.Hostname, got, want)
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}
