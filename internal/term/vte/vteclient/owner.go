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

// Package vteclient shows the terminals a workspace's host owns on
// another machine. Each view is a replica: a local emulator fed the
// terminal's byte stream over vtewire, which attaches again by itself
// when the connection it came over drops or falls behind. Bells, titles
// and selections happen in the replica, so they belong to the Rune the
// user is looking at; the pty, its size and its input belong to the
// host.
package vteclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtesnap"
	"unstable.build/rune/internal/term/vte/vtewire"
	"unstable.build/rune/internal/workspace"
)

// ErrClosed is returned by an Owner that was closed.
var ErrClosed = errors.New("vteclient: closed")

var _ vte.Host = (*Owner)(nil)

// Config configures an Owner.
type Config struct {
	// VTE configures every replica. Its ScheduleNextTick is required;
	// its command, process watcher and expander are ignored, as a
	// replica starts nothing.
	VTE vte.Config
	// Machine names the host's machine in errors, for the user.
	Machine string
}

// Owner reaches the terminals a workspace's host owns and shows them as
// replicas. The host decides what terminals exist; closing the Owner
// detaches its replicas and ends nothing on the host.
type Owner struct {
	host          host
	publisher     browser.EventPublisher
	notifications browser.Notifications
	tm            browser.TabManager
	cfg           Config
	ctx           context.Context
	cancel        context.CancelFunc

	mu       sync.Mutex
	replicas map[*replica]struct{}
	closed   bool
}

// New returns an Owner of the terminals of peer's workspace. publisher,
// n and tm are the collaborators of every replica, as for
// [vte.NewHandler].
func New(
	peer workspace.Peer, publisher browser.EventPublisher,
	n browser.Notifications, tm browser.TabManager, cfg Config,
) *Owner {
	return newOwner(peerHost{peer}, publisher, n, tm, cfg)
}

func newOwner(
	host host, publisher browser.EventPublisher,
	n browser.Notifications, tm browser.TabManager, cfg Config,
) *Owner {
	if cfg.VTE.ScheduleNextTick == nil {
		panic("vteclient: Config.VTE.ScheduleNextTick is required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Owner{
		host: host, publisher: publisher, notifications: n, tm: tm, cfg: cfg,
		ctx: ctx, cancel: cancel,
		replicas: make(map[*replica]struct{}),
	}
}

// CreateVTE creates a terminal on the host and returns a replica
// attached to it.
func (o *Owner) CreateVTE(
	ctx context.Context, opts vte.CreateOptions,
) (vte.View, error) {
	return o.open(ctx, &vtewire.Hello{
		Create: vtewire.CreateOptions{CommandAndArgs: opts.CommandAndArgs},
		Width:  opts.Width,
		Height: opts.Height,
	})
}

// AttachVTE returns a replica attached to the host's terminal id, or
// [vte.ErrNotFound] when the host has none with that id.
func (o *Owner) AttachVTE(ctx context.Context, id vte.ID) (vte.View, error) {
	return o.open(ctx, &vtewire.Hello{VTEID: string(id)})
}

// PreviewVTE returns a replica of the host's terminal id that leaves
// it detached, or [vte.ErrNotFound] when the host has none
// with that id.
func (o *Owner) PreviewVTE(ctx context.Context, id vte.ID) (vte.View, error) {
	return o.open(ctx, &vtewire.Hello{VTEID: string(id), Preview: true})
}

// ListVTEs reports the host's terminals. It asks the host, so the
// event loop must not call it.
func (o *Owner) ListVTEs(ctx context.Context) ([]vte.Info, error) {
	return o.host.ListVTEs(ctx)
}

// VTELiveness tells whether the host's terminal id runs something for
// the user.
func (o *Owner) VTELiveness(
	ctx context.Context, id vte.ID,
) (vte.Liveness, error) {
	return o.host.VTELiveness(ctx, id)
}

// DeleteVTE terminates the host's terminal id.
func (o *Owner) DeleteVTE(ctx context.Context, id vte.ID) error {
	return o.host.DeleteVTE(ctx, id)
}

// Watch calls fn with what the host tells of its terminals until ctx
// ends or the Owner closes. Whenever the host's stream of changes is
// opened, what the host lists is told as changes too, so what was
// missed while it was down is caught up with.
func (o *Owner) Watch(ctx context.Context, fn func(vte.Change)) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(o.ctx, cancel)
	w := &watch{o: o, fn: fn, known: make(map[vte.ID]struct{})}
	go debug.CapturePanicReport(func() {
		defer stop()
		defer cancel()
		w.run(ctx)
	})
}

// Close detaches every replica. The host's terminals keep running.
func (o *Owner) Close() error {
	o.mu.Lock()
	o.closed = true
	replicas := make([]*replica, 0, len(o.replicas))
	for v := range o.replicas {
		replicas = append(replicas, v)
	}
	o.mu.Unlock()
	o.cancel()
	var err error
	for _, v := range replicas {
		err = errors.Join(err, v.Close())
	}
	return err
}

func (o *Owner) isClosed() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.closed
}

// track keeps v among the replicas Close detaches. It reports false,
// without keeping it, once the Owner closed.
func (o *Owner) track(v *replica) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return false
	}
	o.replicas[v] = struct{}{}
	return true
}

func (o *Owner) forget(v *replica) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.replicas, v)
}

// display is the size of a cell on this Owner's display, which is none
// when it cannot draw images.
func (o *Owner) display() cellSize {
	if o.cfg.VTE.CellPixelSize == nil {
		return cellSize{}
	}
	return cellSizeOf(o.cfg.VTE.CellPixelSize())
}

// open attaches as hello asks and returns the replica.
func (o *Owner) open(ctx context.Context, hello *vtewire.Hello) (vte.View, error) {
	if o.isClosed() {
		return nil, ErrClosed
	}
	root, err := o.host.Root(ctx)
	if err != nil {
		return nil, err
	}
	hello.Root = root
	display := o.display()
	hello.CellPixelWidth, hello.CellPixelHeight = display.width, display.height
	conn, ok, snap, err := o.dial(ctx, hello)
	if err != nil {
		return nil, o.attachError(err)
	}
	uri, err := workspaceapi.ParseURI(ok.URI)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("parse terminal uri %q: %w", ok.URI, err)
	}

	v := &replica{
		o:       o,
		id:      vte.ID(ok.VTEID),
		root:    root,
		uri:     uri,
		preview: hello.Preview,
		conn:    conn,
		width:   ok.Width,
		height:  ok.Height,
		asked:   display,
	}
	v.ctx, v.cancel = context.WithCancel(o.ctx)
	v.cell.store(cellSizeOf(snap.Terminal.CellPixelWidth, snap.Terminal.CellPixelHeight))
	v.pty = newWirePty(v, uri.Path())

	cfg := o.cfg.VTE
	cfg.Observer = true
	cfg.URI = uri
	cfg.WidthHint = ok.Width
	cfg.HeightHint = ok.Height
	// The replica interprets the stream with the cell size the terminal
	// it replicates has, not this display's: placing an image moves the
	// cursor by the cells it covers. Images are sized in cells, so they
	// still draw at this display's size.
	cfg.CellPixelSize = v.cell.pixels
	if snap.Terminal.MaxScrollback > 0 {
		cfg.MaxLines = snap.Terminal.MaxScrollback
	}
	// A replica interprets a stream someone else produced, so nothing
	// here may resolve or start a program.
	cfg.CommandAndArgs = nil
	cfg.CommandExpander = nil
	cfg.Watcher = nil

	h, err := vte.NewHandler(o.publisher, o.notifications,
		wireTerminal{pty: v.pty}, nil, o.tm, cfg)
	if err != nil {
		v.cancel()
		_ = conn.Close()
		return nil, fmt.Errorf("attach terminal %s: %w", ok.VTEID, err)
	}
	v.Handler = h
	v.schedule = cfg.ScheduleNextTick
	if err := h.ApplySnapshot(snap); err != nil {
		_ = v.Close()
		return nil, fmt.Errorf("restore terminal %s: %w", ok.VTEID, err)
	}
	if !o.track(v) {
		_ = v.Close()
		return nil, ErrClosed
	}
	// The emulator only starts consuming the stream once it holds the
	// state that stream continues from.
	v.pty.start(snap.Continuation)
	return v, nil
}

// dial opens a fresh attachment and performs the handshake over it,
// returning the connection, the host's reply and the snapshot that
// followed it. ctx ending abandons the handshake.
func (o *Owner) dial(
	ctx context.Context, hello *vtewire.Hello,
) (*vtewire.Conn, *vtewire.HelloOK, *vtesnap.Snapshot, error) {
	rw, err := o.host.DialVTE(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	conn := vtewire.NewConn(rw)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	ok, err := vtewire.Dial(conn, hello)
	if err != nil {
		_ = conn.Close()
		return nil, nil, nil, err
	}
	snap, err := readSnapshot(conn)
	if err != nil {
		_ = conn.Close()
		return nil, nil, nil, err
	}
	if !stop() {
		_ = conn.Close()
		return nil, nil, nil, ctx.Err()
	}
	return conn, ok, snap, nil
}

func readSnapshot(conn *vtewire.Conn) (*vtesnap.Snapshot, error) {
	frame, err := conn.ReadFrame()
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	snapshot, ok := frame.(*vtewire.Snapshot)
	if !ok {
		return nil, fmt.Errorf("%w: expected SNAPSHOT, got %s",
			vtewire.ErrMalformed, frame.Type())
	}
	decoder, err := vtesnap.NewDecoder(bytes.NewReader(snapshot.Records))
	if err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}
	snap, err := decoder.Ready()
	if err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}
	return snap, nil
}

// attachError maps a refused handshake onto the errors callers branch
// on, so a confirmed missing terminal stays distinguishable from a
// transport failure.
func (o *Owner) attachError(err error) error {
	var wireErr *vtewire.Error
	if !errors.As(err, &wireErr) {
		return err
	}
	switch wireErr.Code {
	case vtewire.CodeNotFound:
		return fmt.Errorf("%w: %s", vte.ErrNotFound, wireErr.Message)
	case vtewire.CodeVersion:
		host, _ := wireErr.ServerVersion()
		return &VersionError{Host: host, Client: vtewire.Version, Machine: o.cfg.Machine}
	default:
		return err
	}
}

// VersionError is an attachment refused because the Rune hosting the
// workspace speaks another version of the attachment protocol than
// this one. The host refuses before it looks at the terminal asked
// for, so nothing was created or attached.
type VersionError struct {
	// Host is the version the host speaks, zero when it did not say.
	Host uint16
	// Client is the version this Rune speaks.
	Client uint16
	// Machine names the host's machine, empty when unknown.
	Machine string
}

func (e *VersionError) Error() string {
	where := "the machine hosting this workspace"
	if e.Machine != "" {
		where = e.Machine
	}
	switch {
	case e.Host > e.Client:
		return fmt.Sprintf("Rune on %s is newer than this Rune (terminal protocol "+
			"%d, this Rune speaks %d): update this Rune to use its terminals",
			where, e.Host, e.Client)
	case e.Host == 0:
		return fmt.Sprintf("Rune on %s speaks another terminal protocol than this "+
			"Rune (this Rune speaks %d): update both to use its terminals",
			where, e.Client)
	default:
		return fmt.Sprintf("Rune on %s is older than this Rune (terminal protocol "+
			"%d, this Rune speaks %d): update it to use its terminals",
			where, e.Host, e.Client)
	}
}

// watchRetry is how long a watch waits before opening the host's
// stream of changes again after it failed.
const watchRetry = time.Second

// watch relays the host's changes to one Watch caller, catching up from
// a listing whenever the stream is (re)opened.
type watch struct {
	o     *Owner
	fn    func(vte.Change)
	known map[vte.ID]struct{}
}

func (w *watch) run(ctx context.Context) {
	for ctx.Err() == nil {
		err := w.stream(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.WithField(logging.KeyClass, "vteclient").
				Debugf("watching the host's terminals: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(watchRetry):
		}
	}
}

// stream relays one run of the host's stream, until it ends.
func (w *watch) stream(ctx context.Context) error {
	it, err := w.o.host.WatchVTEs(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = it.Close() }()
	if err := w.reconcile(ctx); err != nil {
		return err
	}
	for {
		change, ok := it.Next(ctx)
		if !ok {
			return it.Err()
		}
		w.note(change)
		w.fn(change)
	}
}

// reconcile tells what the host lists, and the deletion of whatever it
// told of before that the host no longer lists.
func (w *watch) reconcile(ctx context.Context) error {
	infos, err := w.o.host.ListVTEs(ctx)
	if err != nil {
		return err
	}
	listed := make(map[vte.ID]struct{}, len(infos))
	for _, info := range infos {
		listed[info.ID] = struct{}{}
	}
	for id := range w.known {
		if _, ok := listed[id]; !ok {
			delete(w.known, id)
			w.fn(vte.Change{ID: id, Deleted: true})
		}
	}
	for _, info := range infos {
		change := vte.Change{
			ID: info.ID, URI: info.URI, Title: info.Title,
			Attachments: info.Attachments, Complete: info.Complete,
		}
		w.note(change)
		w.fn(change)
	}
	return nil
}

func (w *watch) note(change vte.Change) {
	if change.Deleted {
		delete(w.known, change.ID)
		return
	}
	w.known[change.ID] = struct{}{}
}

// maxCellPixels bounds the size of a cell in pixels a display may
// report. No display has cells anywhere near this large.
const maxCellPixels = 1024

// cellSize is the size of a cell in pixels. The zero value is none: a
// display that cannot draw images, for which graphics are off.
type cellSize struct{ width, height int }

// cellSizeOf is the cell size a peer named, which is none unless both
// of its dimensions are.
func cellSizeOf(width, height int) cellSize {
	if width <= 0 || height <= 0 {
		return cellSize{}
	}
	return cellSize{
		width:  min(width, maxCellPixels),
		height: min(height, maxCellPixels),
	}
}

// atomicCellSize is a cell size the emulator reads from its parse
// goroutine while the stream it reads changes it.
type atomicCellSize struct{ v atomic.Uint64 }

func (a *atomicCellSize) load() cellSize {
	v := a.v.Load()
	return cellSize{width: int(v >> 32), height: int(uint32(v))}
}

func (a *atomicCellSize) store(c cellSize) {
	a.v.Store(uint64(uint32(c.width))<<32 | uint64(uint32(c.height)))
}

// pixels satisfies [vte.Config.CellPixelSize].
func (a *atomicCellSize) pixels() (width, height int) {
	c := a.load()
	return c.width, c.height
}
