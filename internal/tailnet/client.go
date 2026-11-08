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
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
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

// silentTTL is how long a machine that did not answer a probe in time
// is left out before it is asked again: it may drop connections to the
// workspace port for good, or have been waiting on its tunnel.
const silentTTL = 30 * time.Second

// listWait bounds how long listing machines waits on probes of those
// nothing is known about yet. A probe outlives the listing that started
// it, so the machine is listed the next time.
const listWait = time.Second

// prewarmInterval spaces the probes of machines that newly appear on
// the tailnet, so a burst of changes is answered with one round.
const prewarmInterval = 5 * time.Second

// probeConcurrency bounds the probes in flight at once.
const probeConcurrency = 8

// Following the Tailscale client is retried after losing it, at first
// after watchRetryDelay and then less and less often, down to once per
// watchRetryMaxDelay, on a machine where Tailscale does not run.
const (
	watchRetryDelay    = 2 * time.Second
	watchRetryMaxDelay = time.Minute
)

// watchOpts subscribes to the changes of the network map without the
// map itself, which some clients would otherwise send whole on each.
const watchOpts = ipn.NotifyInitialState | ipn.NotifyPeerChanges |
	ipn.NotifyPeerPatches | ipn.NotifyNoNetMap

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
//
// Once it lists machines or listens, it follows the Tailscale client
// until closed: it learns of changes to the tailnet as they happen, and
// probes the machines that appear on it ahead of the next listing.
type Client struct {
	lc     *local.Client
	listen func(ctx context.Context, network, address string) (net.Listener, error)
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
	port   uint16
	snaps  *snapshots

	probeTimeout    time.Duration
	probeTTL        time.Duration
	silentTTL       time.Duration
	listWait        time.Duration
	prewarmInterval time.Duration

	ctx       context.Context
	cancel    func()
	startOnce sync.Once
	sem       chan struct{}
	prewarmCh chan struct{}

	mu       sync.Mutex
	probes   map[tailcfg.StableNodeID]probe
	inflight map[tailcfg.StableNodeID]chan struct{}
	subs     map[*func()]struct{}
}

// probeAnswer is what probing a machine's workspace port came to.
type probeAnswer int

const (
	// probeServes: the machine accepted the connection.
	probeServes probeAnswer = iota + 1
	// probeRefused: it turned the connection down.
	probeRefused
	// probeSilent: it did not answer in time.
	probeSilent
)

type probe struct {
	answer probeAnswer
	at     time.Time
}

// New builds a Client. It does no I/O: a machine without Tailscale
// fails each call with ErrNotRunning instead.
func New(cfg Config) *Client {
	c := &Client{
		lc:              cfg.LocalClient,
		listen:          cfg.Listen,
		dial:            cfg.Dial,
		port:            uint16(cfg.Port),
		probeTimeout:    probeTimeout,
		probeTTL:        probeTTL,
		silentTTL:       silentTTL,
		listWait:        listWait,
		prewarmInterval: prewarmInterval,
		sem:             make(chan struct{}, probeConcurrency),
		prewarmCh:       make(chan struct{}, 1),
		probes:          make(map[tailcfg.StableNodeID]probe),
		inflight:        make(map[tailcfg.StableNodeID]chan struct{}),
		subs:            make(map[*func()]struct{}),
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	if c.lc == nil {
		c.lc = newLocalClient()
	}
	c.snaps = newSnapshots(c.lc)
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

// Close stops following the Tailscale client and probing the tailnet's
// machines. The listeners the Client made stop following it too, so
// close them first.
func (c *Client) Close() error {
	c.cancel()
	return nil
}

// start follows the Tailscale client, once.
func (c *Client) start() {
	c.startOnce.Do(func() {
		go debug.CapturePanicReport(c.watch)
		go debug.CapturePanicReport(c.prewarm)
	})
}

// watch follows the Tailscale client's notification bus, and reports a
// change whenever the network map changes and whenever the bus is lost,
// since anything may change before it is back.
func (c *Client) watch() {
	delay := watchRetryDelay
	for c.ctx.Err() == nil {
		if w, err := c.lc.WatchIPNBus(c.ctx, watchOpts); err == nil {
			delay = watchRetryDelay
			for {
				n, err := w.Next()
				if err != nil {
					break
				}
				if changesNetMap(&n) {
					c.changed()
				}
			}
			_ = w.Close()
			c.changed()
		}
		select {
		case <-c.ctx.Done():
		case <-time.After(delay):
		}
		delay = min(2*delay, watchRetryMaxDelay)
	}
}

// changesNetMap reports whether n changes what Rune reads of the network
// map: this machine's state and node, and which machines there are,
// whether they are online, and how they are named and owned.
func changesNetMap(n *ipn.Notify) bool {
	if n.State != nil || n.SelfChange != nil || n.NetMap != nil ||
		n.LoginFinished != nil || len(n.PeersChanged) > 0 || len(n.PeersRemoved) > 0 {
		return true
	}
	for _, p := range n.PeerChangedPatch {
		if p != nil && p.Online != nil {
			return true
		}
	}
	return false
}

func (c *Client) changed() {
	c.snaps.invalidate()
	c.mu.Lock()
	subs := make([]func(), 0, len(c.subs))
	for f := range c.subs {
		subs = append(subs, *f)
	}
	c.mu.Unlock()
	for _, f := range subs {
		f()
	}
	select {
	case c.prewarmCh <- struct{}{}:
	default:
	}
}

// onChange calls f whenever the network map may have changed, until the
// returned func is called. f must not block.
func (c *Client) onChange(f func()) (cancel func()) {
	key := &f
	c.mu.Lock()
	c.subs[key] = struct{}{}
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.subs, key)
	}
}

// prewarm probes the machines nothing is known about yet whenever the
// network map changes, so they are known by the time the user lists
// them.
func (c *Client) prewarm() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.prewarmCh:
		}
		if snap, err := c.snaps.get(c.ctx); err == nil {
			for _, p := range candidates(snap) {
				if !isRune(p) && !c.known(p) {
					c.probeAsync(p)
				}
			}
		}
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(c.prewarmInterval):
		}
	}
}

// Peers lists the online machines of the tailnet that serve workspaces
// and could let this one in, sorted by name: each is named by its
// MagicDNS label. A machine owned by another user never lets this one
// in, so it is not even asked; see [Authorizer]. A machine is known to
// serve workspaces when it runs Rune's own Tailscale node, or when it
// accepts a connection on the workspace port.
//
// Peers answers from what is known, and asks again in the background
// what is stale. It waits a moment for machines nothing is known about
// yet; one that does not answer by then is listed the next time it
// does. A machine that did not answer in time is not waited for again.
func (c *Client) Peers(ctx context.Context) ([]runenet.Peer, error) {
	c.start()
	snap, err := c.snaps.get(ctx)
	if err != nil {
		return nil, err
	}
	found := candidates(snap)
	var pending []<-chan struct{}
	for _, p := range found {
		if done := c.refresh(p); done != nil {
			pending = append(pending, done)
		}
	}
	if err := c.await(ctx, pending); err != nil {
		return nil, err
	}
	var ret []runenet.Peer
	for _, p := range found {
		if isRune(p) || c.answer(p) == probeServes {
			ret = append(ret, peerOf(p))
		}
	}
	sort.Slice(ret, func(i, j int) bool {
		return ret[i].Hostname < ret[j].Hostname
	})
	return ret, nil
}

// candidates are the online machines of snap that could let this one
// in.
func candidates(snap snapshot) []tailcfg.NodeView {
	self := snap.self()
	var ret []tailcfg.NodeView
	for _, p := range snap.nm.Peers {
		online, _ := p.Online().GetOk()
		if !online || p.Addresses().Len() == 0 || !mayAdmit(self, p) {
			continue
		}
		ret = append(ret, p)
	}
	return ret
}

// mayAdmit reports whether peer could let self in at all: a machine
// owned by a user only admits that user's other untagged machines.
func mayAdmit(self, peer tailcfg.NodeView) bool {
	if peer.Tags().Len() > 0 {
		return true
	}
	return self.Tags().Len() == 0 && self.User() == peer.User()
}

// isRune reports whether p runs Rune's own Tailscale node, which always
// serves workspaces.
func isRune(p tailcfg.NodeView) bool {
	return p.Hostinfo().App() == runenet.App
}

// refresh probes p when what is known of it is missing or stale, and
// returns the probe to wait on when p cannot be listed without its
// answer. A machine known to serve stays listed while it is asked
// again, and one that did not answer stays out.
func (c *Client) refresh(p tailcfg.NodeView) <-chan struct{} {
	if isRune(p) {
		return nil
	}
	c.mu.Lock()
	known, ok := c.probes[p.StableID()]
	c.mu.Unlock()
	age := time.Since(known.at)
	switch {
	case !ok, known.answer == probeRefused && age >= c.probeTTL:
		return c.probeAsync(p)
	case known.answer == probeServes && age >= c.probeTTL,
		known.answer == probeSilent && age >= c.silentTTL:
		c.probeAsync(p)
	}
	return nil
}

// await waits until every probe in pending is done, listWait passes or
// ctx ends.
func (c *Client) await(ctx context.Context, pending []<-chan struct{}) error {
	if len(pending) == 0 {
		return nil
	}
	timer := time.NewTimer(c.listWait)
	defer timer.Stop()
	for _, done := range pending {
		select {
		case <-done:
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (c *Client) known(p tailcfg.NodeView) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.probes[p.StableID()]
	return ok
}

func (c *Client) answer(p tailcfg.NodeView) probeAnswer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.probes[p.StableID()].answer
}

// probeAsync probes p unless a probe of it is already under way, and
// returns a channel closed once that probe is done. A probe outlives
// whatever asked for it: its answer is what the next listing needs.
func (c *Client) probeAsync(p tailcfg.NodeView) <-chan struct{} {
	id := p.StableID()
	c.mu.Lock()
	if done, ok := c.inflight[id]; ok {
		c.mu.Unlock()
		return done
	}
	done := make(chan struct{})
	c.inflight[id] = done
	c.mu.Unlock()
	addr := c.addrPort(p.Addresses().At(0).Addr())
	go debug.CapturePanicReport(func() {
		defer close(done)
		answer, ok := c.probe(addr)
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.inflight, id)
		if ok {
			c.probes[id] = probe{answer: answer, at: time.Now()}
		}
	})
	return done
}

// probe connects to the workspace port at addr. ok is false when the
// Client closed first.
func (c *Client) probe(addr string) (answer probeAnswer, ok bool) {
	select {
	case c.sem <- struct{}{}:
	case <-c.ctx.Done():
		return 0, false
	}
	defer func() { <-c.sem }()
	ctx, cancel := context.WithTimeout(c.ctx, c.probeTimeout)
	defer cancel()
	conn, err := c.dial(ctx, "tcp", addr)
	switch {
	case err == nil:
		_ = conn.Close()
		return probeServes, true
	case c.ctx.Err() != nil:
		return 0, false
	case ctx.Err() != nil:
		return probeSilent, true
	default:
		return probeRefused, true
	}
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
	snap, err := c.snaps.get(ctx)
	if err != nil {
		return netip.Addr{}, err
	}
	addr, err := lookup(snap, peer)
	if !errors.Is(err, runenet.ErrPeerNotFound) {
		return addr, err
	}
	// The map may be from before the machine joined.
	if snap, err = c.snaps.fresh(ctx); err != nil {
		return netip.Addr{}, err
	}
	return lookup(snap, peer)
}

func lookup(snap snapshot, peer string) (netip.Addr, error) {
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
	c.start()
	return newListener(c)
}
