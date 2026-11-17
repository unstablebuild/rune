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

package vteregistry

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/ernestrc/go-multierror"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vte"
)

// Set owns the terminals of the workspaces a host serves to other
// machines: one Registry per workspace root, made when the root is
// first named, over the served scheme chrooted there. It has no display
// of its own, as its terminals are shown only by the replicas attached
// over the wire, so what an emulator schedules runs on a goroutine of
// the Set's, in order.
type Set struct {
	scheme schemeapi.Scheme
	cfg    Config
	loop   *loop

	mu     sync.Mutex
	roots  map[string]*root
	closed bool
}

// root is the registry of one workspace root, and the scheme chrooted
// there that its terminals run on.
type root struct {
	r      *Registry
	scheme schemeapi.Scheme
	// chroot is whether scheme is a chroot of the Set's, which it
	// closes with the registry; the served scheme is the host's.
	chroot bool
}

// NewSet returns an empty Set over scheme, which every root a client
// names is resolved against. cfg configures every registry; its
// ScheduleNextTick is replaced, and its FileSystem is the root's scheme
// unless set.
func NewSet(scheme schemeapi.Scheme, cfg Config) *Set {
	s := &Set{scheme: scheme, cfg: cfg, loop: newLoop(), roots: make(map[string]*root)}
	s.cfg.VTE.ScheduleNextTick = s.loop.schedule
	return s
}

// Resolve is the registry of the workspace rooted at root, made if
// there is none yet. An empty root is the served scheme's own. It
// satisfies [Resolver].
func (s *Set) Resolve(_ context.Context, path string) (*Registry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if rt, ok := s.roots[path]; ok {
		return rt.r, nil
	}
	scheme, chroot := s.scheme, false
	if path != "" {
		var err error
		if scheme, err = s.scheme.Chroot(path); err != nil {
			return nil, fmt.Errorf("workspace %s: %w", path, err)
		}
		chroot = true
	}
	cfg := s.cfg
	if cfg.VTE.FileSystem == nil {
		cfg.VTE.FileSystem = scheme
	}
	rt := &root{
		r:      New(headlessHost{}, headlessHost{}, scheme, scheme, headlessHost{}, cfg),
		scheme: scheme,
		chroot: chroot,
	}
	s.roots[path] = rt
	return rt.r, nil
}

// Host is the [vte.Host] of the terminals of the workspace rooted at
// root; see [Set.Resolve]. It is what workspacerpc.VTEHosts asks for.
func (s *Set) Host(ctx context.Context, root string) (vte.Host, error) {
	r, err := s.Resolve(ctx, root)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Serve accepts attachment connections on lis until it is closed and
// serves each from the registry of the root its HELLO names. See
// [Serve].
func (s *Set) Serve(ctx context.Context, lis net.Listener) error {
	return Serve(ctx, lis, s.Resolve)
}

// Close terminates every terminal of every registry. Later calls to
// Resolve fail with [ErrClosed].
func (s *Set) Close() error {
	s.mu.Lock()
	s.closed = true
	roots := make([]*root, 0, len(s.roots))
	for key, rt := range s.roots {
		roots = append(roots, rt)
		delete(s.roots, key)
	}
	s.mu.Unlock()
	var errs error
	for _, rt := range roots {
		if err := rt.r.Close(); err != nil {
			errs = multierror.Append(errs, err)
		}
		if rt.chroot {
			if err := rt.scheme.Close(); err != nil {
				errs = multierror.Append(errs, err)
			}
		}
	}
	s.loop.stop()
	return errs
}

// loop runs what the emulators of a Set schedule on one goroutine, in
// order, as a host event loop would.
type loop struct {
	mu      sync.Mutex
	queue   []func()
	stopped bool
	wake    chan struct{}
	done    chan struct{}
}

func newLoop() *loop {
	l := &loop{wake: make(chan struct{}, 1), done: make(chan struct{})}
	go debug.CapturePanicReport(l.run)
	return l
}

func (l *loop) run() {
	for {
		select {
		case <-l.wake:
		case <-l.done:
			return
		}
		l.mu.Lock()
		fns := l.queue
		l.queue = nil
		l.mu.Unlock()
		for _, fn := range fns {
			fn()
		}
	}
}

// schedule satisfies vte.Config.ScheduleNextTick. It reports false once
// the loop stopped, as a host whose event loop ended does.
func (l *loop) schedule(fn func()) bool {
	l.mu.Lock()
	if l.stopped {
		l.mu.Unlock()
		return false
	}
	l.queue = append(l.queue, fn)
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
	return true
}

// stop ends the loop. What was scheduled and not run yet is dropped.
func (l *loop) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return
	}
	l.stopped = true
	l.queue = nil
	close(l.done)
}

// headlessHost is the collaborator of the emulators of a Set that has
// no display: nothing to publish events to, notify or show tabs on.
type headlessHost struct{}

func (headlessHost) PublishEvent(term.Event) error { return nil }

func (headlessHost) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (headlessHost) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (headlessHost) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

func (headlessHost) Tab(
	workspaceapi.URI, rune, string, browserapi.Handler,
) (browserapi.Handler, error) {
	return nil, nil
}

func (headlessHost) SetTabName(workspaceapi.URI, string, term.Attributes) error {
	return nil
}

func (headlessHost) OnTabExit(workspaceapi.URI) bool { return false }

func (headlessHost) SetTabStatus(workspaceapi.URI, programstatus.Report) error {
	return nil
}

func (headlessHost) EndTabStatus(workspaceapi.URI) error { return nil }
