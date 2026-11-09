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
	"fmt"
	"net/netip"
	"os/user"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"tailscale.com/client/local"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/tailcfg"
	"unstable.build/rune/internal/debug"
)

// WorkspaceCapability is the capability a tailnet policy grants a
// machine to let it open workspaces on a tagged one. Opening a
// workspace amounts to a shell on that machine.
const WorkspaceCapability tailcfg.PeerCapability = "rune.build/cap/workspace"

// Every request is authorized, and a workspace issues many, over
// several connections. A decision about a machine is reused for
// decisionTTL, and asked again in the background from decisionRefresh
// on, so a machine that keeps working is never held up by it, and one
// whose access was revoked is turned away within decisionTTL.
const (
	decisionRefresh = 5 * time.Second
	decisionTTL     = 10 * time.Second
)

// refreshTimeout bounds a background refresh of a decision.
const refreshTimeout = 10 * time.Second

// Authorizer decides which machines of a tailnet may open workspaces on
// this one. A workspace is a shell on this machine, so it admits no one
// Tailscale SSH would turn away: a machine owned by a user admits only
// that user's other untagged machines, and a tagged machine admits a
// caller its tailnet policy grants [WorkspaceCapability], or one an
// accept rule of the policy's SSH section lets log in as the local user
// Rune runs as. Check rules and rules that record sessions only
// Tailscale SSH can enforce, so they admit no one.
//
// Connections from this machine's own tailnet addresses are refused:
// any local user can make them, and the tailnet would vouch for each
// of them as this machine.
type Authorizer struct {
	lc    *local.Client
	snaps *snapshots
	local localUser

	refreshAfter time.Duration
	ttl          time.Duration

	mu        sync.Mutex
	decisions map[netip.Addr]*decision
}

type decision struct {
	err error
	at  time.Time
	// refreshing is whether it is being asked again.
	refreshing bool
}

// localUser is the account Rune runs under on this machine.
type localUser struct {
	name string
	root bool
}

// NewAuthorizer builds an Authorizer that reads the tailnet through lc.
func NewAuthorizer(lc *local.Client) *Authorizer {
	if lc == nil {
		panic("tailnet.NewAuthorizer: client is required")
	}
	return newAuthorizer(lc, newSnapshots(lc))
}

// newAuthorizer builds an Authorizer that reads the network map from
// snaps, which a [Client] keeps up to date.
func newAuthorizer(lc *local.Client, snaps *snapshots) *Authorizer {
	var local localUser
	if u, err := user.Current(); err != nil {
		log.WithField(logging.KeyClass, "tailnet").Warnf(
			"could not tell which user Rune runs as, so no SSH rule "+
				"admits anyone: %v", err)
	} else {
		local = localUser{name: u.Username, root: u.Uid == "0" || u.Username == "root"}
	}
	return &Authorizer{
		lc:           lc,
		snaps:        snaps,
		local:        local,
		refreshAfter: decisionRefresh,
		ttl:          decisionTTL,
		decisions:    make(map[netip.Addr]*decision),
	}
}

// Authorize satisfies runenet.Authorizer. What the tailnet says about a
// machine depends on its address alone, so the decision is shared by
// all of its connections.
func (a *Authorizer) Authorize(ctx context.Context) error {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return status.Error(codes.Unauthenticated, "no peer address")
	}
	src, err := netip.ParseAddrPort(p.Addr.String())
	if err != nil {
		return status.Errorf(codes.Unauthenticated,
			"caller %s is not on the tailnet", p.Addr)
	}
	if d, ok := a.cached(src); ok {
		return d.err
	}
	err = a.decide(ctx, src, a.snaps.get)
	a.remember(src.Addr().Unmap(), err)
	return err
}

// cached is the decision about src's machine, if one is recent enough,
// and asks again in the background when it is getting old.
func (a *Authorizer) cached(src netip.AddrPort) (decision, bool) {
	ip := src.Addr().Unmap()
	a.mu.Lock()
	defer a.mu.Unlock()
	d, ok := a.decisions[ip]
	if !ok || time.Since(d.at) >= a.ttl {
		return decision{}, false
	}
	if time.Since(d.at) >= a.refreshAfter && !d.refreshing {
		d.refreshing = true
		go debug.CapturePanicReport(func() { a.refresh(src) })
	}
	return *d, true
}

// refresh decides about src's machine again, from the network map as it
// is now: the tailnet's SSH policy changes without notice.
func (a *Authorizer) refresh(src netip.AddrPort) {
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	ip := src.Addr().Unmap()
	err := a.decide(ctx, src, a.snaps.fresh)
	if decided(err) {
		a.remember(ip, err)
		return
	}
	// The old decision stands until it is too old: a failure to ask is
	// not an answer.
	a.mu.Lock()
	defer a.mu.Unlock()
	if d, ok := a.decisions[ip]; ok {
		d.refreshing = false
	}
}

// decided reports whether err is a decision, rather than a failure to
// make one.
func decided(err error) bool {
	code := status.Code(err)
	return code == codes.OK || code == codes.PermissionDenied
}

// remember keeps err as the decision about ip when it is one, and
// reports a refusal unless it was already the decision.
func (a *Authorizer) remember(ip netip.Addr, err error) {
	if !decided(err) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for addr, d := range a.decisions {
		if now.Sub(d.at) >= a.ttl {
			delete(a.decisions, addr)
		}
	}
	prev, seen := a.decisions[ip]
	if err != nil && (!seen || status.Convert(prev.err).Message() != status.Convert(err).Message()) {
		log.WithField(logging.KeyClass, "tailnet").
			Warnf("rejected workspace request from %s: %s", ip, status.Convert(err).Message())
	}
	a.decisions[ip] = &decision{err: err, at: now}
}

// decide is whether src's machine may open workspaces here, as the
// network map read says.
func (a *Authorizer) decide(
	ctx context.Context, src netip.AddrPort,
	read func(context.Context) (snapshot, error),
) error {
	snap, err := read(ctx)
	if err != nil {
		return status.Errorf(codes.Unavailable,
			"could not read this machine's tailnet: %v", err)
	}
	if snap.isSelfAddr(src.Addr().Unmap()) {
		return status.Error(codes.PermissionDenied, "connections from this machine's own tailnet "+
			"address are refused: any user of this machine could have made them")
	}
	who, err := a.lc.WhoIs(ctx, src.String())
	if err != nil {
		return status.Errorf(codes.Unauthenticated,
			"could not identify caller %s: %v", src.Addr(), err)
	}
	if who.Node == nil || who.UserProfile == nil {
		return status.Errorf(codes.Unauthenticated,
			"could not identify caller %s", src.Addr())
	}
	if reason := a.refusal(snap, who, src.Addr().Unmap()); reason != "" {
		return status.Error(codes.PermissionDenied, reason)
	}
	return nil
}

// refusal is why who, connecting from src, may not open workspaces on
// this machine, or empty when it may.
func (a *Authorizer) refusal(
	snap snapshot, who *apitype.WhoIsResponse, src netip.Addr,
) string {
	self := snap.self()
	caller := callerName(who, src)
	if self.Tags().Len() == 0 {
		switch {
		case len(who.Node.Tags) > 0:
			return fmt.Sprintf("%s is a tagged machine, and like Tailscale "+
				"SSH, Rune never lets a tagged machine into one owned by a user",
				caller)
		case who.Node.User != self.User():
			return fmt.Sprintf("%s belongs to %s, not to %s, and like "+
				"Tailscale SSH, Rune never lets one user into another's machine",
				caller, who.UserProfile.LoginName, snap.login(self.User()))
		}
		return ""
	}
	if who.CapMap.HasCapability(WorkspaceCapability) {
		return ""
	}
	switch evalSSHPolicy(snap.nm.SSHPolicy, who, src, a.local, time.Now()) {
	case sshAccept:
		return ""
	case sshCheck:
		return fmt.Sprintf("the tailnet's SSH rule for %s is a check rule, "+
			"which only Tailscale SSH can enforce: make it an accept rule, "+
			"or grant %s %s", caller, caller, WorkspaceCapability)
	case sshRecorded:
		return fmt.Sprintf("the tailnet's SSH rule for %s records sessions, "+
			"which only Tailscale SSH can do: grant %s %s instead",
			caller, caller, WorkspaceCapability)
	}
	return fmt.Sprintf("%s may not open workspaces here: grant it %s on "+
		"this machine, or let it SSH in as %q in the tailnet's SSH policy",
		caller, WorkspaceCapability, a.local.name)
}

func callerName(who *apitype.WhoIsResponse, src netip.Addr) string {
	if who.Node.ComputedName != "" {
		return fmt.Sprintf("%s (%s)", who.Node.ComputedName, src)
	}
	return src.String()
}

// sshVerdict is what the tailnet's SSH policy says about a caller
// logging in as the local user.
type sshVerdict int

const (
	sshDenied sshVerdict = iota
	sshAccept
	sshCheck
	sshRecorded
)

// evalSSHPolicy evaluates pol the way Tailscale SSH does for who,
// connecting from src, asking to log in as local: the first rule whose
// principals match the caller, and whose users map the requested user
// to a local one, decides. A rule that maps the user to another local
// account decides too, and denies: Rune cannot log in as anyone else.
func evalSSHPolicy(
	pol *tailcfg.SSHPolicy, who *apitype.WhoIsResponse, src netip.Addr,
	local localUser, now time.Time,
) sshVerdict {
	if pol == nil || local.name == "" {
		return sshDenied
	}
	for _, r := range pol.Rules {
		if r == nil || r.Action == nil {
			continue
		}
		if r.RuleExpires != nil && now.After(*r.RuleExpires) {
			continue
		}
		if !anyPrincipalMatches(r.Principals, who, src) {
			continue
		}
		if r.Action.Reject {
			return sshDenied
		}
		mapped := mapLocalUser(r.SSHUsers, local)
		switch {
		case mapped == "":
			continue
		case mapped != local.name:
			return sshDenied
		case r.Action.HoldAndDelegate != "":
			return sshCheck
		case !r.Action.Accept:
			return sshDenied
		case len(r.Action.Recorders) > 0:
			return sshRecorded
		}
		return sshAccept
	}
	return sshDenied
}

func anyPrincipalMatches(
	ps []*tailcfg.SSHPrincipal, who *apitype.WhoIsResponse, src netip.Addr,
) bool {
	for _, p := range ps {
		if p == nil {
			continue
		}
		if p.Any ||
			(!p.Node.IsZero() && p.Node == who.Node.StableID) ||
			(p.UserLogin != "" && p.UserLogin == who.UserProfile.LoginName) {
			return true
		}
		if ip, err := netip.ParseAddr(p.NodeIP); err == nil && ip == src {
			return true
		}
	}
	return false
}

// mapLocalUser is the local account a rule's users let the caller log
// in as when it asks for local, or empty when they let it log in as no
// one. "=" stands for the account asked for, unless it is root.
func mapLocalUser(users map[string]string, local localUser) string {
	v, ok := users[local.name]
	if !ok {
		v = users["*"]
	}
	if v == "=" {
		if local.root {
			return ""
		}
		return local.name
	}
	return v
}
