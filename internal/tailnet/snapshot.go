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
	"fmt"
	"net/netip"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/tailcfg"
	"tailscale.com/types/netmap"
)

// ErrNotRunning is returned when the machine's Tailscale client is not
// connected to a tailnet, or cannot be reached at all.
var ErrNotRunning = errors.New("this machine is not connected to Tailscale")

// snapshotAttempts bounds how often reading the Tailscale client's state
// is tried. On macOS the first connection to the client can fail while
// it is being discovered, and succeed right after.
const snapshotAttempts = 3

const snapshotRetryDelay = 200 * time.Millisecond

// snapshotTTL bounds how stale a cached network map may get. The
// Tailscale client announces most changes on its notification bus,
// which drops the cached map right away; this bounds the rest, such as
// a new SSH policy, and clients too old to announce changes of peers.
const snapshotTTL = 5 * time.Second

// snapshot is the Tailscale client's view of the tailnet at one moment.
type snapshot struct {
	nm *netmap.NetworkMap
}

// snapshots caches the Tailscale client's network map, which listing
// machines, dialing one and authorizing a caller all read, and which
// grows with the tailnet.
type snapshots struct {
	lc  *local.Client
	ttl time.Duration

	mu     sync.Mutex
	cached bool
	snap   snapshot
	at     time.Time
	// gen counts invalidations, so a read that was under way when the
	// map changed is not cached.
	gen uint64
}

func newSnapshots(lc *local.Client) *snapshots {
	return &snapshots{lc: lc, ttl: snapshotTTL}
}

// get is the network map as read less than ttl ago, and not invalidated
// since, or as read now; see readSnapshot.
func (s *snapshots) get(ctx context.Context) (snapshot, error) {
	s.mu.Lock()
	if s.cached && time.Since(s.at) < s.ttl {
		snap := s.snap
		s.mu.Unlock()
		return snap, nil
	}
	s.mu.Unlock()
	return s.fresh(ctx)
}

// fresh is the network map as read now; see readSnapshot.
func (s *snapshots) fresh(ctx context.Context) (snapshot, error) {
	s.mu.Lock()
	gen := s.gen
	s.mu.Unlock()
	snap, err := readSnapshot(ctx, s.lc)
	if err != nil {
		return snapshot{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gen == gen {
		s.cached, s.snap, s.at = true, snap, time.Now()
	}
	return snap, nil
}

// invalidate makes the next get read the network map again.
func (s *snapshots) invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	s.cached = false
}

// readSnapshot reads the Tailscale client's network map. It fails with
// ErrNotRunning unless the client is connected to a tailnet.
//
// The map comes from the client's notification bus, which every
// version of the client hands to unprivileged local processes, rather
// than from a dedicated endpoint that older clients lack.
func readSnapshot(ctx context.Context, lc *local.Client) (snapshot, error) {
	var err error
	for attempt := range snapshotAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return snapshot{}, ctx.Err()
			case <-time.After(snapshotRetryDelay):
			}
		}
		var snap snapshot
		var running bool
		snap, running, err = readSnapshotOnce(ctx, lc)
		if err == nil && !running {
			return snapshot{}, ErrNotRunning
		}
		if err == nil {
			return snap, nil
		}
	}
	if ctx.Err() != nil {
		return snapshot{}, ctx.Err()
	}
	return snapshot{}, fmt.Errorf("%w: %v", ErrNotRunning, err)
}

func readSnapshotOnce(
	ctx context.Context, lc *local.Client,
) (snap snapshot, running bool, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState|ipn.NotifyInitialNetMap)
	if err != nil {
		return snapshot{}, false, err
	}
	defer w.Close()
	n, err := w.Next()
	if err != nil {
		return snapshot{}, false, err
	}
	if n.ErrMessage != nil {
		return snapshot{}, false, errors.New(*n.ErrMessage)
	}
	running = n.State != nil && *n.State == ipn.Running
	if !running || n.NetMap == nil || !n.NetMap.SelfNode.Valid() {
		return snapshot{}, false, nil
	}
	return snapshot{nm: n.NetMap}, true, nil
}

func (s snapshot) self() tailcfg.NodeView {
	return s.nm.SelfNode
}

// isSelfAddr reports whether addr is one of this machine's own tailnet
// addresses.
func (s snapshot) isSelfAddr(addr netip.Addr) bool {
	for _, p := range s.self().Addresses().All() {
		if p.Addr() == addr {
			return true
		}
	}
	return false
}

func (s snapshot) login(id tailcfg.UserID) string {
	if p, ok := s.nm.UserProfiles[id]; ok {
		return p.LoginName()
	}
	return ""
}
