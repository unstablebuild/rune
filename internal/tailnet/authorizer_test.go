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
	"net"
	"net/netip"
	"os/user"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn"
	"tailscale.com/tailcfg"
	"unstable.build/rune/internal/tailnet/tailnettest"
)

func TestEvalSSHPolicy(t *testing.T) {
	src := netip.MustParseAddr("100.64.0.2")
	who := &apitype.WhoIsResponse{
		Node:        &tailcfg.Node{StableID: "caller"},
		UserProfile: &tailcfg.UserProfile{LoginName: "alice@example.com"},
	}
	ernie := localUser{name: "ernie"}
	root := localUser{name: "root", root: true}
	accept := &tailcfg.SSHAction{Accept: true}
	anyone := []*tailcfg.SSHPrincipal{{Any: true}}
	asIs := map[string]string{"*": "="}
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	rule := func(principals []*tailcfg.SSHPrincipal, users map[string]string,
		action *tailcfg.SSHAction) *tailcfg.SSHRule {
		return &tailcfg.SSHRule{Principals: principals, SSHUsers: users, Action: action}
	}

	cases := []struct {
		name  string
		rules []*tailcfg.SSHRule
		// local is ernie unless set.
		local       localUser
		noLocalUser bool
		want        sshVerdict
	}{
		{name: "no rules", want: sshDenied},
		{name: "any caller", rules: []*tailcfg.SSHRule{rule(anyone, asIs, accept)},
			want: sshAccept},
		{name: "by node", rules: []*tailcfg.SSHRule{
			rule([]*tailcfg.SSHPrincipal{{Node: "caller"}}, asIs, accept)},
			want: sshAccept},
		{name: "by another node", rules: []*tailcfg.SSHRule{
			rule([]*tailcfg.SSHPrincipal{{Node: "other"}}, asIs, accept)},
			want: sshDenied},
		{name: "by address", rules: []*tailcfg.SSHRule{
			rule([]*tailcfg.SSHPrincipal{{NodeIP: "100.64.0.2"}}, asIs, accept)},
			want: sshAccept},
		{name: "by another address", rules: []*tailcfg.SSHRule{
			rule([]*tailcfg.SSHPrincipal{{NodeIP: "100.64.0.3"}, {NodeIP: "bogus"}}, asIs, accept)},
			want: sshDenied},
		{name: "by login", rules: []*tailcfg.SSHRule{
			rule([]*tailcfg.SSHPrincipal{nil, {UserLogin: "alice@example.com"}}, asIs, accept)},
			want: sshAccept},
		{name: "by another login", rules: []*tailcfg.SSHRule{
			rule([]*tailcfg.SSHPrincipal{{UserLogin: "bob@example.com"}}, asIs, accept)},
			want: sshDenied},
		{name: "the local user by name", rules: []*tailcfg.SSHRule{
			rule(anyone, map[string]string{"ernie": "ernie"}, accept)},
			want: sshAccept},
		{name: "another local user", rules: []*tailcfg.SSHRule{
			rule(anyone, map[string]string{"*": "ubuntu"}, accept),
			rule(anyone, asIs, accept)},
			want: sshDenied},
		{name: "no local user", rules: []*tailcfg.SSHRule{
			rule(anyone, map[string]string{"root": "root"}, accept),
			rule(anyone, map[string]string{"*": ""}, accept)},
			want: sshDenied},
		{name: "a rule letting no one in is skipped", rules: []*tailcfg.SSHRule{
			rule(anyone, map[string]string{"root": "root"}, &tailcfg.SSHAction{Reject: false}),
			rule(anyone, asIs, accept)},
			want: sshAccept},
		{name: "= is never root", local: root, rules: []*tailcfg.SSHRule{
			rule(anyone, asIs, accept)},
			want: sshDenied},
		{name: "root by name", local: root, rules: []*tailcfg.SSHRule{
			rule(anyone, asIs, accept),
			rule(anyone, map[string]string{"root": "root"}, accept)},
			want: sshAccept},
		{name: "expired rules are skipped", rules: []*tailcfg.SSHRule{
			{RuleExpires: &past, Principals: anyone, SSHUsers: asIs, Action: accept}},
			want: sshDenied},
		{name: "rules that have not expired apply", rules: []*tailcfg.SSHRule{
			{RuleExpires: &future, Principals: anyone, SSHUsers: asIs, Action: accept}},
			want: sshAccept},
		{name: "rules without an action are skipped", rules: []*tailcfg.SSHRule{
			nil, rule(anyone, asIs, nil), rule(anyone, asIs, accept)},
			want: sshAccept},
		{name: "reject needs no user", rules: []*tailcfg.SSHRule{
			rule(anyone, nil, &tailcfg.SSHAction{Reject: true}),
			rule(anyone, asIs, accept)},
			want: sshDenied},
		{name: "reject wins over accept", rules: []*tailcfg.SSHRule{
			rule(anyone, asIs, &tailcfg.SSHAction{Reject: true, Accept: true})},
			want: sshDenied},
		{name: "check", rules: []*tailcfg.SSHRule{
			rule(anyone, asIs, &tailcfg.SSHAction{HoldAndDelegate: "https://control/check"}),
			rule(anyone, asIs, accept)},
			want: sshCheck},
		{name: "recorded", rules: []*tailcfg.SSHRule{
			rule(anyone, asIs, &tailcfg.SSHAction{Accept: true,
				Recorders: []netip.AddrPort{netip.MustParseAddrPort("100.64.0.9:80")}})},
			want: sshRecorded},
		{name: "an action that neither accepts nor rejects", rules: []*tailcfg.SSHRule{
			rule(anyone, asIs, &tailcfg.SSHAction{Message: "hi"}),
			rule(anyone, asIs, accept)},
			want: sshDenied},
		{name: "unknown local user", noLocalUser: true, rules: []*tailcfg.SSHRule{
			rule(anyone, asIs, accept)},
			want: sshDenied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			local := tc.local
			if local == (localUser{}) && !tc.noLocalUser {
				local = ernie
			}
			pol := &tailcfg.SSHPolicy{Rules: tc.rules}
			assert.Equal(t, tc.want, evalSSHPolicy(pol, who, src, local, time.Now()))
		})
	}
	assert.Equal(t, sshDenied, evalSSHPolicy(nil, who, src, ernie, time.Now()))
}

func callerCtx(addr netip.Addr) context.Context {
	return callerCtxPort(addr, 41641)
}

func callerCtxPort(addr netip.Addr, port uint16) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		Addr: net.TCPAddrFromAddrPort(netip.AddrPortFrom(addr, port)),
	})
}

func currentUser(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	require.NoError(t, err)
	return u.Username
}

func TestAuthorizer(t *testing.T) {
	me := currentUser(t)
	asMe := map[string]string{me: me}
	grant := tailcfg.PeerCapMap{WorkspaceCapability: nil}
	sshRule := func(action tailcfg.SSHAction, users map[string]string) *tailcfg.SSHPolicy {
		return &tailcfg.SSHPolicy{Rules: []*tailcfg.SSHRule{{
			Principals: []*tailcfg.SSHPrincipal{{Any: true}},
			SSHUsers:   users,
			Action:     &action,
		}}}
	}
	laptop := testNode(1, "laptop", alice)
	server := testNode(1, "server", 0, "tag:server")
	desktop := testNode(2, "desktop", alice)
	bobs := testNode(2, "bobs-laptop", bob)
	ci := testNode(2, "ci", 0, "tag:ci")

	cases := []struct {
		name     string
		self     *tailcfg.Node
		caller   *tailcfg.Node
		caps     tailcfg.PeerCapMap
		ssh      *tailcfg.SSHPolicy
		wantCode codes.Code
		wantMsg  string
	}{
		{name: "the same user's machine", self: laptop, caller: desktop},
		{name: "another user's machine", self: laptop, caller: bobs, caps: grant,
			ssh:      sshRule(tailcfg.SSHAction{Accept: true}, asMe),
			wantCode: codes.PermissionDenied, wantMsg: "belongs to bob@example.com, not to alice@example.com"},
		{name: "a tagged machine into a user's", self: laptop, caller: ci, caps: grant,
			ssh:      sshRule(tailcfg.SSHAction{Accept: true}, asMe),
			wantCode: codes.PermissionDenied, wantMsg: "ci (100.64.0.2) is a tagged machine"},
		{name: "a tagged machine without a policy", self: server, caller: desktop,
			wantCode: codes.PermissionDenied,
			wantMsg:  "grant it rune.build/cap/workspace on this machine, or let it SSH in as \"" + me + "\""},
		{name: "a tagged machine granting workspaces", self: server, caller: desktop, caps: grant},
		{name: "a tagged machine with an SSH rule", self: server, caller: desktop,
			ssh: sshRule(tailcfg.SSHAction{Accept: true}, asMe)},
		{name: "tagged machines with an SSH rule", self: server, caller: ci,
			ssh: sshRule(tailcfg.SSHAction{Accept: true}, asMe)},
		{name: "an SSH rule for another local user", self: server, caller: desktop,
			ssh:      sshRule(tailcfg.SSHAction{Accept: true}, map[string]string{"*": me + "-other"}),
			wantCode: codes.PermissionDenied, wantMsg: "may not open workspaces here"},
		{name: "an SSH check rule", self: server, caller: desktop,
			ssh:      sshRule(tailcfg.SSHAction{HoldAndDelegate: "https://control/check"}, asMe),
			wantCode: codes.PermissionDenied, wantMsg: "is a check rule, which only Tailscale SSH can enforce"},
		{name: "an SSH rule recording sessions", self: server, caller: desktop,
			ssh: sshRule(tailcfg.SSHAction{Accept: true,
				Recorders: []netip.AddrPort{netip.MustParseAddrPort("100.64.0.9:80")}}, asMe),
			wantCode: codes.PermissionDenied, wantMsg: "records sessions, which only Tailscale SSH can do"},
		{name: "this machine's own address", self: laptop, caller: laptop,
			wantCode: codes.PermissionDenied, wantMsg: "own tailnet address are refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, lc := newFakeLocalAPI(t, tc.self)
			if tc.caller != tc.self {
				f.set(func(f *fakeLocalAPI) {
					f.peers = []*tailcfg.Node{tc.caller}
					f.caps = map[tailcfg.NodeID]tailcfg.PeerCapMap{tc.caller.ID: tc.caps}
					f.ssh = tc.ssh
				})
			}
			a := NewAuthorizer(lc)
			err := a.Authorize(callerCtx(tc.caller.Addresses[0].Addr()))
			assert.Equal(t, tc.wantCode, status.Code(err), "%v", err)
			if tc.wantMsg != "" {
				assert.Contains(t, status.Convert(err).Message(), tc.wantMsg)
			}
		})
	}

	t.Run("a decision holds for all of a machine's connections", func(t *testing.T) {
		f, lc := newFakeLocalAPI(t, laptop, desktop, bobs)
		a := NewAuthorizer(lc)
		require.NoError(t, a.Authorize(callerCtxPort(testAddr(2), 1000)))
		asked := f.requests.Load()
		require.NoError(t, a.Authorize(callerCtxPort(testAddr(2), 1000)))
		require.NoError(t, a.Authorize(callerCtxPort(testAddr(2), 2000)))
		assert.Equal(t, asked, f.requests.Load())
	})

	// desktop moves to bob once the first decision is made.
	givenToBob := func(f *fakeLocalAPI) {
		f.set(func(f *fakeLocalAPI) {
			moved := *desktop
			moved.User = bob
			f.peers = []*tailcfg.Node{&moved}
		})
	}

	t.Run("an aging decision is made again without holding up requests", func(t *testing.T) {
		f, lc := newFakeLocalAPI(t, laptop, desktop)
		a := NewAuthorizer(lc)
		a.refreshAfter = 0
		require.NoError(t, a.Authorize(callerCtx(testAddr(2))))
		givenToBob(f)
		assert.NoError(t, a.Authorize(callerCtx(testAddr(2))), "the old decision while asking")
		require.Eventually(t, func() bool {
			return status.Code(a.Authorize(callerCtx(testAddr(2)))) == codes.PermissionDenied
		}, 5*time.Second, 10*time.Millisecond)
	})

	t.Run("a decision too old is made again before answering", func(t *testing.T) {
		f, lc := newFakeLocalAPI(t, laptop, desktop)
		a := NewAuthorizer(lc)
		require.NoError(t, a.Authorize(callerCtx(testAddr(2))))
		givenToBob(f)
		a.ttl = 0
		err := a.Authorize(callerCtx(testAddr(2)))
		assert.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	})

	t.Run("a failure to ask again keeps the decision", func(t *testing.T) {
		f, lc := newFakeLocalAPI(t, laptop, desktop)
		a := NewAuthorizer(lc)
		a.refreshAfter = 0
		require.NoError(t, a.Authorize(callerCtx(testAddr(2))))
		f.set(func(f *fakeLocalAPI) { f.state = ipn.Stopped })
		asked := f.mapReads.Load()
		require.NoError(t, a.Authorize(callerCtx(testAddr(2))))
		require.Eventually(t, func() bool { return f.mapReads.Load() > asked },
			5*time.Second, 10*time.Millisecond)
		assert.NoError(t, a.Authorize(callerCtx(testAddr(2))))
	})

	t.Run("an unknown caller", func(t *testing.T) {
		_, lc := newFakeLocalAPI(t, laptop)
		err := NewAuthorizer(lc).Authorize(callerCtx(testAddr(7)))
		assert.Equal(t, codes.Unauthenticated, status.Code(err), "%v", err)
	})

	t.Run("no caller", func(t *testing.T) {
		_, lc := newFakeLocalAPI(t, laptop)
		err := NewAuthorizer(lc).Authorize(context.Background())
		assert.Equal(t, codes.Unauthenticated, status.Code(err), "%v", err)
	})

	t.Run("a failure to ask is not remembered", func(t *testing.T) {
		f, lc := newFakeLocalAPI(t, laptop, desktop)
		f.set(func(f *fakeLocalAPI) { f.state = ipn.Stopped })
		a := NewAuthorizer(lc)
		err := a.Authorize(callerCtx(testAddr(2)))
		assert.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
		f.set(func(f *fakeLocalAPI) { f.state = ipn.Running })
		assert.NoError(t, a.Authorize(callerCtx(testAddr(2))))
	})
}

// TestAuthorizerOnATailnet authorizes connections between real tsnet
// machines, so the network map, WhoIs and grants come from a real
// Tailscale client and coordination server.
func TestAuthorizerOnATailnet(t *testing.T) {
	tn := tailnettest.Start(t)
	server := tn.Join(t, "server")
	desktop := tn.Join(t, "desktop")
	other := tn.Join(t, "other")
	tn.SetOwner(t, desktop, server)
	serverUser := tn.Node(t, server).User
	tailnettest.WaitWhoIs(t, server, desktop.Addr, func(w *apitype.WhoIsResponse) bool {
		return w.Node.User == serverUser
	})

	auth := NewAuthorizer(server.LocalClient)
	authorize := func(caller *tailnettest.Machine) error {
		return auth.Authorize(callerCtx(caller.Addr))
	}
	assert.NoError(t, authorize(desktop), "the same user's machine")
	err := authorize(other)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "%v", err)
	assert.Contains(t, status.Convert(err).Message(), "belongs to")
	err = authorize(server)
	assert.Contains(t, status.Convert(err).Message(), "own tailnet address")

	// Tagged, the server only lets in whom its policy names.
	tn.SetTags(t, server, "tag:server")
	tailnettest.WaitSelf(t, server, func(n *tailcfg.Node) bool { return len(n.Tags) == 1 })
	auth = NewAuthorizer(server.LocalClient)
	err = authorize(other)
	assert.Contains(t, status.Convert(err).Message(), "may not open workspaces here")

	me := currentUser(t)
	tn.Control.SetSSHPolicy(&tailcfg.SSHPolicy{Rules: []*tailcfg.SSHRule{{
		Principals: []*tailcfg.SSHPrincipal{{NodeIP: other.Addr.String()}},
		SSHUsers:   map[string]string{me: me},
		Action:     &tailcfg.SSHAction{Accept: true},
	}}})
	require.Eventually(t, func() bool {
		return NewAuthorizer(server.LocalClient).Authorize(callerCtx(other.Addr)) == nil
	}, tailnettest.Timeout, 100*time.Millisecond, "the SSH rule never let other in")
	auth = NewAuthorizer(server.LocalClient)
	err = authorize(desktop)
	assert.Contains(t, status.Convert(err).Message(), "may not open workspaces here",
		"the rule names other only")

	tn.Control.SetGlobalAppCaps(tailcfg.PeerCapMap{WorkspaceCapability: []tailcfg.RawMessage{"{}"}})
	tailnettest.WaitWhoIs(t, server, desktop.Addr, func(w *apitype.WhoIsResponse) bool {
		return w.CapMap.HasCapability(WorkspaceCapability)
	})
	auth = NewAuthorizer(server.LocalClient)
	assert.NoError(t, authorize(desktop), "granted workspaces")
}
