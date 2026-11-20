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

package vteclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtesnap"
	"unstable.build/rune/internal/term/vte/vtewire"
)

// Resync backoff: how long a replica waits before attaching again after
// an attempt failed to reach the host, growing to the maximum.
const (
	resyncMinBackoff = 50 * time.Millisecond
	resyncMaxBackoff = 5 * time.Second
)

// replica is one view of a terminal the host owns.
//
// The emulator is real and local: it parses the terminal's byte stream
// itself, so bells, notifications, titles and selections happen here
// rather than being rendered copies of decisions made elsewhere. Only
// authority over the shared pty, its size and its input, belongs to the
// host. When the attachment the stream comes over ends while the
// terminal lives on, the replica attaches again and resyncs from a
// fresh snapshot: a jump on the screen, not a dead tab.
type replica struct {
	*vte.Handler
	o        *Owner
	id       vte.ID
	root     string
	uri      workspaceapi.URI
	preview  bool
	pty      *wirePty
	schedule func(func()) bool
	// ctx ends with the replica or its Owner, which releases a resync
	// waiting to attach again.
	ctx    context.Context
	cancel context.CancelFunc
	// cell is the size of a cell in force for the terminal.
	cell atomicCellSize

	mu sync.Mutex
	// conn is the attachment in force. It changes on a resync.
	conn          *vtewire.Conn
	width, height int
	// asked is the display's cell size last sent to the host.
	asked   cellSize
	focused bool
	closed  bool
}

var _ vte.View = (*replica)(nil)

// ID satisfies vte.View.
func (v *replica) ID() vte.ID { return v.id }

// URI satisfies vte.View.
func (v *replica) URI() workspaceapi.URI { return v.uri }

// Title satisfies vte.View.
func (v *replica) Title() string { return v.Handler.Component().Title() }

// IsComplete satisfies vte.View.
func (v *replica) IsComplete() bool { return v.Handler.Component().IsComplete() }

// UsedAlternateBuffer satisfies vte.View.
func (v *replica) UsedAlternateBuffer() bool {
	return v.Handler.Component().UsedAlternateBuffer()
}

// current is the attachment in force, nil once closed.
func (v *replica) current() *vtewire.Conn {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	return v.conn
}

// Resize satisfies tui.Component. It is a request: the shared pty
// follows one view at a time, so the local emulator only resizes when
// the host says the size changed.
//
// A new font resizes a window without changing its grid, so the size
// of this display's cells is asked for too.
func (v *replica) Resize(width, height int) {
	if width <= 0 || height <= 0 {
		return
	}
	cell := v.o.display()
	v.mu.Lock()
	same := v.width == width && v.height == height && v.asked == cell
	v.width, v.height = width, height
	v.asked = cell
	conn := v.conn
	if v.closed {
		conn = nil
	}
	v.mu.Unlock()
	if same || conn == nil {
		return
	}
	_ = conn.WriteFrame(&vtewire.Resize{
		Width:           width,
		Height:          height,
		CellPixelWidth:  cell.width,
		CellPixelHeight: cell.height,
	})
}

// OnFocusChange satisfies vte.View. Focus decides whose size the
// shared pty follows, so the host is told as well as the emulator.
func (v *replica) OnFocusChange(inFocus bool) {
	v.Handler.OnFocusChange(inFocus)
	v.mu.Lock()
	v.focused = inFocus
	conn := v.conn
	if v.closed {
		conn = nil
	}
	v.mu.Unlock()
	if conn != nil {
		_ = conn.WriteFrame(&vtewire.Focus{Focused: inFocus})
	}
}

// Close detaches this replica. The terminal, its pty and its process
// keep running on the host.
func (v *replica) Close() error {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return nil
	}
	v.closed = true
	conn := v.conn
	v.mu.Unlock()
	v.cancel()
	v.o.forget(v)

	// DETACH only lets the host drop the attachment before the socket
	// does. Closing runs on the host loop, so a host that has stopped
	// reading must not be able to hold a window open: closing the
	// handler closes the connection under this write.
	go debug.CapturePanicReport(func() {
		_ = conn.WriteFrame(&vtewire.Detach{})
	})
	return v.Handler.Close()
}

// applyResize reflows the local emulator at the point in the byte
// stream the host reflowed at, then lets the host loop catch up with
// the handler-level state that belongs to it.
func (v *replica) applyResize(f *vtewire.Resize) {
	width, height := f.Width, f.Height
	if width <= 0 || height <= 0 {
		return
	}
	v.mu.Lock()
	v.width, v.height = width, height
	v.mu.Unlock()

	v.cell.store(cellSizeOf(f.CellPixelWidth, f.CellPixelHeight))
	if err := v.Handler.Component().Resize(width, height); err != nil {
		return
	}
	v.schedule(func() { v.Handler.Resize(width, height) })
}

func (v *replica) applyHistory(page []byte) error {
	kind, rows, err := vtesnap.DecodePage(page)
	if err != nil {
		return err
	}
	if kind != vtesnap.ScreenPrimary {
		return nil
	}
	v.Handler.Component().PrependHistory(rows)
	return nil
}

// resync attaches again after the attachment in force ended for cause,
// and replaces the emulator's state with the fresh snapshot. It runs on
// the parse goroutine, between two feeds, which is the one place the
// emulator's interpretation of the stream may be replaced. It returns
// the continuation the stream resumes with, or the error the replica
// ends with: the host confirming the terminal is gone, or the replica
// closing.
func (v *replica) resync(cause error) ([]byte, error) {
	backoff := resyncMinBackoff
	for attempt := 1; ; attempt++ {
		v.mu.Lock()
		closed := v.closed
		hello := &vtewire.Hello{
			Root: v.root, VTEID: string(v.id), Preview: v.preview,
			Width: v.width, Height: v.height,
			CellPixelWidth: v.asked.width, CellPixelHeight: v.asked.height,
		}
		focused := v.focused
		v.mu.Unlock()
		if closed {
			return nil, cause
		}
		conn, ok, snap, err := v.o.dial(v.ctx, hello)
		if err == nil {
			return v.resume(conn, ok, snap, focused)
		}
		var wireErr *vtewire.Error
		if errors.As(err, &wireErr) {
			// The host answered: the terminal is gone, or not ours to
			// have. Trying again would not change that.
			return nil, v.o.attachError(err)
		}
		if v.ctx.Err() != nil {
			return nil, cause
		}
		if attempt == 1 {
			log.WithField(logging.KeyClass, "vteclient").
				Infof("terminal %s: attachment lost (%v), attaching again", v.id, cause)
		}
		select {
		case <-v.ctx.Done():
			return nil, cause
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, resyncMaxBackoff)
	}
}

// resume makes conn the attachment in force and the snapshot the
// emulator's state.
func (v *replica) resume(
	conn *vtewire.Conn, ok *vtewire.HelloOK, snap *vtesnap.Snapshot, focused bool,
) ([]byte, error) {
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	old := v.conn
	v.conn = conn
	v.width, v.height = ok.Width, ok.Height
	v.mu.Unlock()
	_ = old.Close()

	v.cell.store(cellSizeOf(snap.Terminal.CellPixelWidth, snap.Terminal.CellPixelHeight))
	if err := v.Handler.Component().ApplySnapshot(snap); err != nil {
		return nil, fmt.Errorf("resync terminal %s: %w", v.id, err)
	}
	if snap.Terminal.Width > 0 && snap.Terminal.Height > 0 {
		width, height := snap.Terminal.Width, snap.Terminal.Height
		v.schedule(func() { v.Handler.Resize(width, height) })
	}
	if focused {
		_ = conn.WriteFrame(&vtewire.Focus{Focused: true})
	}
	return snap.Continuation, nil
}

// wireTerminal presents the attachment as a terminal provider so the
// emulator can be built the same way a local one is.
//
// The pty's size is the host's business: it is shared by every view,
// so a replica must never set it directly.
type wireTerminal struct{ pty *wirePty }

// NewPty satisfies schemeapi.Terminal.
func (t wireTerminal) NewPty(context.Context) (workspaceapi.Pty, error) {
	return workspaceapi.Pty{Master: t.pty, Slave: t.pty}, nil
}

// SetPtySize satisfies schemeapi.Terminal.
func (wireTerminal) SetPtySize(workspaceapi.Pty, workspaceapi.PtySize) error { return nil }

// ForegroundProcessGroup satisfies schemeapi.Terminal. The host of the
// pty knows; a replica does not.
func (wireTerminal) ForegroundProcessGroup(workspaceapi.Pty) (int, error) {
	return 0, errors.ErrUnsupported
}

// wirePty is the attachment seen as a pty: reads are the terminal's
// output stream, writes are this view's input.
//
// Reads dispatch every other frame in stream order, so a resize or a
// scrollback page lands at the same point in the byte stream for this
// replica as it did for the terminal it replicates. Each OUTPUT frame
// is one batch, so the replica parses the stream in the batches the
// terminal did.
type wirePty struct {
	v    *replica
	name string
	// ready is closed once the emulator holds the state the stream
	// continues from; closed is closed with the attachment, which
	// releases a read that will never get there.
	ready  chan struct{}
	closed chan struct{}
	// pending is fed before the next frame: a snapshot's continuation.
	pending []byte
	// exited is whether the terminal's end was already reported.
	exited    bool
	closeOnce sync.Once
}

var (
	_ workspaceapi.File = (*wirePty)(nil)
	_ vte.ReplicaReader = (*wirePty)(nil)
)

func newWirePty(v *replica, name string) *wirePty {
	return &wirePty{
		v:      v,
		name:   name,
		ready:  make(chan struct{}),
		closed: make(chan struct{}),
	}
}

// start releases reads, with continuation as the first batch.
func (p *wirePty) start(continuation []byte) {
	p.pending = continuation
	close(p.ready)
}

// ReadBatch satisfies vte.BatchReader.
func (p *wirePty) ReadBatch() ([]byte, error) {
	select {
	case <-p.ready:
	case <-p.closed:
		return nil, net.ErrClosed
	}
	for {
		if len(p.pending) > 0 {
			batch := p.pending
			p.pending = nil
			return batch, nil
		}
		conn := p.v.current()
		if conn == nil {
			return nil, net.ErrClosed
		}
		frame, err := conn.ReadFrame()
		if err != nil {
			if p.pending, err = p.v.resync(err); err != nil {
				return nil, p.end(err)
			}
			continue
		}
		switch f := frame.(type) {
		case *vtewire.Output:
			return f.Data, nil
		case *vtewire.Resize:
			p.v.applyResize(f)
		case *vtewire.GraphicsMedium:
			// The frame's bytes belong to the read buffer, and the
			// emulator keeps them until it parses the transmission.
			p.v.Handler.Component().RelayGraphicsMedium(bytes.Clone(f.Data), f.OK)
		case *vtewire.HistoryPage:
			if err := p.v.applyHistory(f.Page); err != nil {
				return nil, err
			}
		case *vtewire.Exit:
			// The process ending is news carried by the stream, not
			// the end of it: the terminal still reflows.
			var exitErr error
			if f.Err != "" {
				exitErr = errors.New(f.Err)
			}
			p.markExited(exitErr)
			if f.Message != "" {
				return nil, errors.New(f.Message)
			}
		case *vtewire.Dropped:
			cause := &vtewire.Error{Code: f.Code, Message: f.Message}
			if !cause.Slow() {
				return nil, p.end(p.v.o.attachError(cause))
			}
			if p.pending, err = p.v.resync(cause); err != nil {
				return nil, p.end(err)
			}
		default:
			return nil, fmt.Errorf("%w: unexpected %s from host",
				vtewire.ErrMalformed, frame.Type())
		}
	}
}

// markExited reports the terminal's end once.
func (p *wirePty) markExited(exitErr error) {
	if p.exited {
		return
	}
	p.exited = true
	p.v.Handler.MarkExited(exitErr)
}

// end is the error the stream ends with for err: a terminal the host
// confirmed gone ended as a process does, and is listed no more; anything
// else is the failure it is.
func (p *wirePty) end(err error) error {
	if errors.Is(err, vte.ErrNotFound) {
		p.markExited(nil)
		p.v.o.mirror.gone(p.v.id)
		return io.EOF
	}
	return err
}

// Read satisfies io.Reader. The emulator reads batches instead.
func (p *wirePty) Read([]byte) (int, error) { return 0, errNotAFile }

// Write satisfies io.Writer.
func (p *wirePty) Write(b []byte) (int, error) {
	conn := p.v.current()
	if conn == nil {
		return 0, net.ErrClosed
	}
	if err := conn.WriteFrame(&vtewire.Input{Data: b}); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Close satisfies io.Closer. The emulator closes master and slave
// separately, and both are this one attachment.
func (p *wirePty) Close() error {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.v.mu.Lock()
		conn := p.v.conn
		p.v.mu.Unlock()
		_ = conn.Close()
	})
	return nil
}

// Name satisfies workspaceapi.File.
func (p *wirePty) Name() string { return p.name }

// Stat satisfies workspaceapi.File.
func (p *wirePty) Stat() (os.FileInfo, error) { return nil, errNotAFile }

// Sync satisfies workspaceapi.File.
func (p *wirePty) Sync() error { return nil }

// Truncate satisfies workspaceapi.File.
func (p *wirePty) Truncate(int64) error { return errNotAFile }

// Fd satisfies workspaceapi.File.
func (p *wirePty) Fd() uintptr { return 0 }

// Seek satisfies io.Seeker.
func (p *wirePty) Seek(int64, int) (int64, error) { return 0, errNotAFile }

// ReadAt satisfies io.ReaderAt.
func (p *wirePty) ReadAt([]byte, int64) (int, error) { return 0, errNotAFile }

// errNotAFile rejects the file operations a byte stream cannot answer.
var errNotAFile = errors.New("vteclient: terminal attachment is not a file")
