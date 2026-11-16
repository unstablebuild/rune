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

// Package vteregistry keeps the terminals of a workspace apart from the
// tabs that show them, so a terminal can be shown in several tabs at
// once, or in none and keep running.
package vteregistry

import (
	"context"
	"errors"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/ernestrc/go-multierror"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/term/vte"
)

const defaultPollInterval = time.Second

// ErrClosed is returned by a Registry that was closed.
var ErrClosed = errors.New("vteregistry: closed")

var _ vte.Host = (*Registry)(nil)

// Config configures a Registry.
type Config struct {
	// VTE configures every emulator. CreateVTEOptions override its
	// command and size hints. Its ScheduleNextTick is required.
	VTE vte.Config
	// PollInterval is how often a detached terminal running a program
	// that prints nothing is checked for its shell's prompt. Zero is a
	// second.
	PollInterval time.Duration
}

// Registry owns the terminals of a workspace: their emulators, ptys,
// processes, grids and scrollback. Callers hold attachments, never a
// terminal itself, so a terminal outlives the tab that opened it and
// only [Registry.DeleteVTE] or [Registry.Close] terminate it.
//
// Every attachment shows the one grid of its terminal, which has one
// size: that of its resize owner, the attachment focused last. The
// others draw the grid anchored to its bottom-left corner, cutting its
// top rows and right columns or padding it with the default
// background: a program renders for exactly one size, so a view of
// another size can only crop it.
//
// Attachments must be used on the host event loop. The other methods
// may be called from any goroutine.
type Registry struct {
	publisher     browser.EventPublisher
	notifications browser.Notifications
	terminal      schemeapi.Terminal
	executor      schemeapi.Executor
	tm            browser.TabManager
	cfg           Config

	mu       sync.Mutex
	entries  map[vte.ID]*canonical
	watchers map[*watcher]struct{}
	closed   bool
	// notifyMu serializes the changes told to watchers, so each sees
	// them in the order they happened.
	notifyMu sync.Mutex
}

type watcher struct{ fn func(vte.Change) }

// New returns an empty Registry. publisher, n, terminal and executor
// are the collaborators of every emulator, as for [vte.NewHandler].
//
// tm receives the tab and status calls of every terminal under the
// terminal's own URI, attached or not: the host resolves it to the
// tabs of its attachments, and keeps what a terminal with none reports
// as a terminal that is no tab. What a detached terminal runs is told
// to [Registry.Watch]: it is the host's to report.
func New(
	publisher browser.EventPublisher, n browser.Notifications,
	terminal schemeapi.Terminal, executor schemeapi.Executor,
	tm browser.TabManager, cfg Config,
) *Registry {
	if cfg.VTE.ScheduleNextTick == nil {
		panic("vteregistry: Config.VTE.ScheduleNextTick is required")
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = defaultPollInterval
	}
	return &Registry{
		publisher: publisher, notifications: n,
		terminal: terminal, executor: executor, tm: tm, cfg: cfg,
		entries:  make(map[vte.ID]*canonical),
		watchers: make(map[*watcher]struct{}),
	}
}

// CreateVTE creates a terminal and returns the caller's attachment to
// it. It blocks while the terminal's pty and process start, for no
// longer than ctx's deadline when it has one.
func (r *Registry) CreateVTE(
	ctx context.Context, opts vte.CreateOptions,
) (vte.View, error) {
	if r.isClosed() {
		return nil, ErrClosed
	}
	cfg := r.cfg.VTE
	if deadline, ok := ctx.Deadline(); ok {
		left := max(time.Until(deadline), time.Millisecond)
		if cfg.SpawnTimeout == 0 || left < cfg.SpawnTimeout {
			cfg.SpawnTimeout = left
		}
	}
	c := &canonical{
		r:  r,
		id: vte.NewID(),
		// A shell is idle at its prompt; any other program is running
		// for as long as it lives.
		shell: len(opts.CommandAndArgs) == 0 ||
			slices.Equal(opts.CommandAndArgs, cfg.CommandAndArgs),
		commandAndArgs: opts.CommandAndArgs,
		createdAt:      time.Now(),
		attachments:    make(map[*attachment]struct{}),
		previews:       make(map[*attachment]struct{}),
		poke:           make(chan struct{}, 1),
	}
	if len(opts.CommandAndArgs) != 0 {
		cfg.CommandAndArgs = opts.CommandAndArgs
	}
	if opts.Width > 0 && opts.Height > 0 {
		cfg.WidthHint, cfg.HeightHint = opts.Width, opts.Height
	}
	h, err := vte.NewHandler(publisher{c}, r.notifications,
		r.terminal, r.executor, tabManager{c}, cfg)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.h = h
	c.uri = h.Component().URI()
	if opts.Width > 0 && opts.Height > 0 {
		c.width, c.height = opts.Width, opts.Height
		h.Resize(opts.Width, opts.Height)
	}
	a := c.attachLocked()
	c.mu.Unlock()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = c.destroy()
		return nil, ErrClosed
	}
	r.entries[c.id] = c
	r.mu.Unlock()
	c.changed(vte.LivenessUnknown)
	return a, nil
}

// AttachVTE returns a new attachment to the terminal id, or
// [vte.ErrNotFound] when there is none with that id.
func (r *Registry) AttachVTE(
	_ context.Context, id vte.ID,
) (vte.View, error) {
	c, ok := r.lookup(id)
	if !ok {
		return nil, vte.ErrNotFound
	}
	c.mu.Lock()
	if c.destroyed {
		c.mu.Unlock()
		return nil, vte.ErrNotFound
	}
	if len(c.attachments) == 0 {
		c.stopWatchLocked()
	}
	a := c.attachLocked()
	c.mu.Unlock()
	c.changed(vte.LivenessUnknown)
	return a, nil
}

// PreviewVTE returns a view of the terminal id that leaves it detached:
// it is not one of its attachments, so the terminal is still reported
// and watched as it was. The view owns the terminal's size while no
// attachment does.
func (r *Registry) PreviewVTE(
	_ context.Context, id vte.ID,
) (vte.View, error) {
	c, ok := r.lookup(id)
	if !ok {
		return nil, vte.ErrNotFound
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.destroyed {
		return nil, vte.ErrNotFound
	}
	a := &attachment{c: c, preview: true}
	c.previews[a] = struct{}{}
	if c.resizeOwner == nil {
		c.resizeOwner = a
	}
	return a, nil
}

// ListVTEs reports every terminal of the registry, attached or not, in
// the order they were created. It does no I/O, so the event loop may
// call it.
func (r *Registry) ListVTEs(context.Context) ([]vte.Info, error) {
	r.mu.Lock()
	entries := make([]*canonical, 0, len(r.entries))
	for _, c := range r.entries {
		entries = append(entries, c)
	}
	r.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool {
		if !entries[i].createdAt.Equal(entries[j].createdAt) {
			return entries[i].createdAt.Before(entries[j].createdAt)
		}
		return entries[i].id < entries[j].id
	})
	ret := make([]vte.Info, 0, len(entries))
	for _, c := range entries {
		ret = append(ret, c.info())
	}
	return ret, nil
}

// VTELiveness tells whether the terminal id runs something for the
// user, or [vte.ErrNotFound] when there is none with that id.
// It asks the host of the terminal's pty, which may be remote, so the
// event loop must not call it.
func (r *Registry) VTELiveness(
	_ context.Context, id vte.ID,
) (vte.Liveness, error) {
	c, ok := r.lookup(id)
	if !ok {
		return vte.LivenessUnknown, vte.ErrNotFound
	}
	return c.liveness(), nil
}

// DeleteVTE terminates the terminal id: it closes every attachment, its
// pty and its process, and forgets it. It returns
// [vte.ErrNotFound] when there is none with that id.
func (r *Registry) DeleteVTE(_ context.Context, id vte.ID) error {
	r.mu.Lock()
	c, ok := r.entries[id]
	delete(r.entries, id)
	r.mu.Unlock()
	if !ok {
		return vte.ErrNotFound
	}
	err := c.destroy()
	c.changed(vte.LivenessUnknown)
	return err
}

// Watch calls fn with every change to a terminal's attachments, with
// its creation, completion and deletion, and with what it runs when
// the registry finds out while it is detached, until ctx ends. fn may
// be called from any goroutine, never two at once, and must return
// promptly without calling back into the registry.
func (r *Registry) Watch(ctx context.Context, fn func(vte.Change)) {
	w := &watcher{fn: fn}
	r.mu.Lock()
	r.watchers[w] = struct{}{}
	r.mu.Unlock()
	context.AfterFunc(ctx, func() {
		r.mu.Lock()
		delete(r.watchers, w)
		r.mu.Unlock()
	})
}

// notify tells every watcher of change.
func (r *Registry) notify(change vte.Change) {
	r.mu.Lock()
	watchers := make([]*watcher, 0, len(r.watchers))
	for w := range r.watchers {
		watchers = append(watchers, w)
	}
	r.mu.Unlock()
	for _, w := range watchers {
		w.fn(change)
	}
}

// Close terminates every terminal. Later calls fail with [ErrClosed].
func (r *Registry) Close() error {
	r.mu.Lock()
	r.closed = true
	entries := make([]*canonical, 0, len(r.entries))
	for id, c := range r.entries {
		entries = append(entries, c)
		delete(r.entries, id)
	}
	r.mu.Unlock()

	var errs error
	for _, c := range entries {
		if err := c.destroy(); err != nil {
			errs = multierror.Append(errs, err)
		}
		c.changed(vte.LivenessUnknown)
	}
	return errs
}

func (r *Registry) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func (r *Registry) lookup(id vte.ID) (*canonical, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.entries[id]
	return c, ok
}

func (r *Registry) schedule(fn func()) {
	_ = r.cfg.VTE.ScheduleNextTick(fn)
}
