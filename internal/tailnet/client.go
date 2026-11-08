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

// Package tailnet opens workspaces on the machines of the user's own
// Tailscale network, and serves this machine's to them, through the
// Tailscale client already running on the machine: tailscale:// URIs
// name machines by their MagicDNS names, and nothing has to be
// configured. Who may open a workspace follows the tailnet's own
// ownership and policy; see [Authorizer].
package tailnet

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tailcfg"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/runenet"
)

// probeTimeout bounds the connection attempt that tells whether a
// machine serves workspaces. The first connection to a machine also
// sets up the tunnel to it, which can take seconds.
const probeTimeout = 5 * time.Second

// probeTTL is how long a probe's answer is trusted. Completing a
// tailscale:// URI lists machines on every keystroke, and probing the
// tailnet that often would look like a port scan.
const probeTTL = time.Minute

// probeConcurrency bounds the probes in flight at once.
const probeConcurrency = 8

// Config configures a [Client].
type Config struct {
	// LocalClient talks to the Tailscale client. Nil means the one
	// running on this machine.
	LocalClient *local.Client
	// Listen and Dial reach the tailnet. Nil means the operating
	// system's network, which reaches the tailnet on a machine where
	// the Tailscale client runs.
	Listen func(ctx context.Context, network, address string) (net.Listener, error)
	Dial   func(ctx context.Context, network, address string) (net.Conn, error)
	// Port is where machines serve workspaces. Zero means
	// [runenet.DefaultPort].
	Port int
}

// Client opens connections to the workspaces other machines of the
// tailnet serve, and lists the machines that serve one. It satisfies
// workspacerune.Mesh.
type Client struct {
	lc     *local.Client
	listen func(ctx context.Context, network, address string) (net.Listener, error)
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
	port   uint16

	mu     sync.Mutex
	probes map[tailcfg.StableNodeID]probe
}

type probe struct {
	serves bool
	at     time.Time
}

// New builds a Client. It does no I/O: a machine without Tailscale
// fails each call with ErrNotRunning instead.
func New(cfg Config) *Client {
	c := &Client{
		lc:     cfg.LocalClient,
		listen: cfg.Listen,
		dial:   cfg.Dial,
		port:   uint16(cfg.Port),
		probes: make(map[tailcfg.StableNodeID]probe),
	}
	if c.lc == nil {
		c.lc = &local.Client{}
	}
	if c.listen == nil {
		var lc net.ListenConfig
		c.listen = lc.Listen
	}
	if c.dial == nil {
		var d net.Dialer
		c.dial = d.DialContext
	}
	if c.port == 0 {
		c.port = runenet.DefaultPort
	}
	return c
}

// LocalClient is the Tailscale client the Client reads the tailnet
// through.
func (c *Client) LocalClient() *local.Client {
	return c.lc
}

// Peers lists the online machines of the tailnet that serve workspaces
// and could let this one in, sorted by name: each is named by its
// MagicDNS label. A machine owned by another user never lets this one
// in, so it is not even asked; see [Authorizer]. A machine is known to
// serve workspaces when it runs Rune's own Tailscale node, or when it
// accepts a connection on the workspace port.
func (c *Client) Peers(ctx context.Context) ([]runenet.Peer, error) {
	snap, err := readSnapshot(ctx, c.lc)
	if err != nil {
		return nil, err
	}
	self := snap.self()
	var candidates []tailcfg.NodeView
	for _, p := range snap.nm.Peers {
		online, _ := p.Online().GetOk()
		if !online || p.Addresses().Len() == 0 || !mayAdmit(self, p) {
			continue
		}
		candidates = append(candidates, p)
	}
	serves := make([]bool, len(candidates))
	sem := make(chan struct{}, probeConcurrency)
	var wg sync.WaitGroup
	for i, p := range candidates {
		if p.Hostinfo().App() == runenet.App {
			serves[i] = true
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			defer func() { <-sem }()
			serves[i] = c.serves(ctx, p)
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var ret []runenet.Peer
	for i, p := range candidates {
		if !serves[i] {
			continue
		}
		ret = append(ret, peerOf(p))
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Hostname < ret[j].Hostname
	})
	return ret, nil
}

// mayAdmit reports whether peer could let self in at all: a machine
// owned by a user only admits that user's other untagged machines.
func mayAdmit(self, peer tailcfg.NodeView) bool {
	if peer.Tags().Len() > 0 {
		return true
	}
	return self.Tags().Len() == 0 && self.User() == peer.User()
}

// serves reports whether p accepts connections on the workspace port,
// remembering the answer for probeTTL. Only an answer is remembered: a
// probe that timed out may have been waiting on the tunnel, or on a
// machine that is about to wake up.
func (c *Client) serves(ctx context.Context, p tailcfg.NodeView) bool {
	c.mu.Lock()
	cached, ok := c.probes[p.StableID()]
	c.mu.Unlock()
	if ok && time.Since(cached.at) < probeTTL {
		return cached.serves
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	conn, err := c.dial(probeCtx, "tcp", c.addrPort(p.Addresses().At(0).Addr()))
	if err == nil {
		_ = conn.Close()
	} else if probeCtx.Err() != nil {
		return false
	}
	c.mu.Lock()
	c.probes[p.StableID()] = probe{serves: err == nil, at: time.Now()}
	c.mu.Unlock()
	return err == nil
}

func peerOf(p tailcfg.NodeView) runenet.Peer {
	addrs := make([]netip.Addr, 0, p.Addresses().Len())
	for _, prefix := range p.Addresses().All() {
		addrs = append(addrs, prefix.Addr())
	}
	return runenet.Peer{
		Hostname: label(p),
		DNSName:  strings.TrimSuffix(p.Name(), "."),
		Addrs:    addrs,
		OS:       p.Hostinfo().OS(),
		Online:   true,
	}
}

// label is the name a tailscale:// URI names p by: the first label of
// its MagicDNS name, which the coordination server made unique, or the
// hostname it chose when it has none.
func label(p tailcfg.NodeView) string {
	if name, _, _ := strings.Cut(p.Name(), "."); name != "" {
		return name
	}
	return p.Hostinfo().Hostname()
}

// Dial opens a connection to the workspace port of peer, a machine of
// the tailnet named by its MagicDNS label or name, or a tailnet
// address. A name the tailnet does not know fails with
// [runenet.ErrPeerNotFound].
func (c *Client) Dial(ctx context.Context, peer string) (net.Conn, error) {
	addr, err := c.resolve(ctx, peer)
	if err != nil {
		return nil, err
	}
	return c.dial(ctx, "tcp", c.addrPort(addr))
}

func (c *Client) resolve(ctx context.Context, peer string) (netip.Addr, error) {
	if addr, err := netip.ParseAddr(peer); err == nil {
		return addr, nil
	}
	snap, err := readSnapshot(ctx, c.lc)
	if err != nil {
		return netip.Addr{}, err
	}
	peer = strings.TrimSuffix(peer, ".")
	for _, p := range snap.nm.Peers {
		if !strings.EqualFold(label(p), peer) &&
			!strings.EqualFold(strings.TrimSuffix(p.Name(), "."), peer) {
			continue
		}
		if p.Addresses().Len() == 0 {
			return netip.Addr{}, fmt.Errorf("%q has no tailnet address", peer)
		}
		return p.Addresses().At(0).Addr(), nil
	}
	return netip.Addr{}, fmt.Errorf("%q: %w", peer, runenet.ErrPeerNotFound)
}

func (c *Client) addrPort(addr netip.Addr) string {
	return netip.AddrPortFrom(addr, c.port).String()
}

// Listen accepts connections on the workspace port of this machine's
// tailnet addresses, and only there: the port is never opened on the
// machine's other networks. The listener follows the Tailscale client:
// it binds the addresses while the client is connected and lets go of
// them when it disconnects, so it may be created before Tailscale runs.
func (c *Client) Listen() net.Listener {
	return newListener(c)
}
