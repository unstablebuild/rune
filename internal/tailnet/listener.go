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
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"tailscale.com/ipn"
	"unstable.build/rune/internal/debug"
)

// rebindRetryInterval is how often the listener tries again to bind an
// address it could not, such as one whose port another process holds.
const rebindRetryInterval = 10 * time.Second

// listener is a net.Listener over the workspace port of every tailnet
// address this machine has at the moment. See [Client.Listen].
type listener struct {
	c      *Client
	ctx    context.Context
	cancel func()
	conns  chan net.Conn
	// changed asks run to rebind: the addresses to serve on may have
	// changed, or a listener failed.
	changed     chan struct{}
	unsubscribe func()

	mu     sync.Mutex
	bound  map[netip.Addr]net.Listener
	failed map[netip.Addr]string
}

func newListener(c *Client) *listener {
	ctx, cancel := context.WithCancel(context.Background())
	l := &listener{
		c:       c,
		ctx:     ctx,
		cancel:  cancel,
		conns:   make(chan net.Conn),
		changed: make(chan struct{}, 1),
		bound:   make(map[netip.Addr]net.Listener),
		failed:  make(map[netip.Addr]string),
	}
	l.unsubscribe = c.onChange(l.signal)
	go debug.CapturePanicReport(l.run)
	return l
}

func (l *listener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.conns:
		return conn, nil
	case <-l.ctx.Done():
		return nil, net.ErrClosed
	}
}

func (l *listener) Close() error {
	l.cancel()
	l.unsubscribe()
	l.mu.Lock()
	defer l.mu.Unlock()
	for addr, lis := range l.bound {
		_ = lis.Close()
		delete(l.bound, addr)
	}
	return nil
}

// Addr is the workspace port on no address in particular: the listener
// is bound to as many addresses as the machine has on the tailnet.
func (l *listener) Addr() net.Addr {
	return &net.TCPAddr{Port: int(l.c.port)}
}

// run binds the addresses the machine should serve on whenever they may
// have changed, and retries those it could not bind.
func (l *listener) run() {
	for {
		var retry <-chan time.Time
		if !l.rebind() {
			retry = time.After(rebindRetryInterval)
		}
		select {
		case <-l.ctx.Done():
			return
		case <-l.changed:
		case <-retry:
		}
	}
}

func (l *listener) signal() {
	select {
	case l.changed <- struct{}{}:
	default:
	}
}

// rebind binds the workspace port on each of the machine's tailnet
// addresses while the Tailscale client is connected, and closes the
// listeners on addresses it no longer has. It reports whether every
// address it should serve on is bound.
func (l *listener) rebind() bool {
	want := make(map[netip.Addr]bool)
	st, err := l.c.lc.StatusWithoutPeers(l.ctx)
	if err == nil && st.BackendState == ipn.Running.String() {
		for _, addr := range st.TailscaleIPs {
			want[addr] = true
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ctx.Err() != nil {
		return true
	}
	for addr, lis := range l.bound {
		if !want[addr] {
			_ = lis.Close()
			delete(l.bound, addr)
		}
	}
	for addr := range want {
		if _, ok := l.bound[addr]; ok {
			continue
		}
		lis, err := l.c.listen(l.ctx, "tcp", l.c.addrPort(addr))
		if err != nil {
			// Retried on every rebind, but reported once: another Rune
			// on this machine may hold the port for good.
			if l.failed[addr] != err.Error() {
				l.failed[addr] = err.Error()
				log.WithField(logging.KeyClass, "tailnet").
					Infof("not serving workspaces on %s: %v", addr, err)
			}
			continue
		}
		delete(l.failed, addr)
		l.bound[addr] = lis
		log.WithField(logging.KeyClass, "tailnet").
			Infof("serving workspaces to the tailnet on %s", lis.Addr())
		go debug.CapturePanicReport(func() { l.accept(addr, lis) })
	}
	return len(l.bound) == len(want)
}

// accept hands lis's connections to Accept until lis fails. A listener
// that failed on its own is forgotten and its address bound again if
// the machine still has it.
func (l *listener) accept(addr netip.Addr, lis net.Listener) {
	defer func() {
		l.mu.Lock()
		failed := l.bound[addr] == lis
		if failed {
			delete(l.bound, addr)
		}
		l.mu.Unlock()
		_ = lis.Close()
		if failed {
			l.signal()
		}
	}()
	for {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		select {
		case l.conns <- conn:
		case <-l.ctx.Done():
			_ = conn.Close()
			return
		}
	}
}
