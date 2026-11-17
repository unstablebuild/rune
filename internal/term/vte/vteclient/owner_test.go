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
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vteregistry"
	"unstable.build/rune/internal/term/vte/vtewire"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// loop stands in for the host event loop: what terminals schedule runs
// on one goroutine, in order.
type loop struct {
	mu     sync.Mutex
	queue  []func()
	notify chan struct{}
}

func newLoop(t *testing.T) *loop {
	l := &loop{notify: make(chan struct{}, 1)}
	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		for {
			select {
			case <-l.notify:
			case <-done:
				return
			}
			l.mu.Lock()
			fns := l.queue
			l.queue = nil
			l.mu.Unlock()
			for _, f := range fns {
				f()
			}
		}
	})
	t.Cleanup(func() { close(done) })
	return l
}

func (l *loop) schedule(f func()) bool {
	l.mu.Lock()
	l.queue = append(l.queue, f)
	l.mu.Unlock()
	select {
	case l.notify <- struct{}{}:
	default:
	}
	return true
}

// do runs f on the loop and waits for it.
func (l *loop) do(f func()) {
	done := make(chan struct{})
	l.schedule(func() {
		defer close(done)
		f()
	})
	<-done
}

// hostTerminal is the registry's PtyExecutor: it records what is typed
// into its ptys and runs the shell in their foreground.
type hostTerminal struct {
	*workspacetest.PtyExecutor
	fg    atomic.Int64
	mu    sync.Mutex
	input bytes.Buffer
}

func (t *hostTerminal) ForegroundProcessGroup(workspaceapi.Pty) (int, error) {
	if fg := t.fg.Load(); fg != 0 {
		return int(fg), nil
	}
	return 0, errors.New("no foreground")
}

type typedMaster struct {
	workspaceapi.File
	t *hostTerminal
}

func (m typedMaster) Write(b []byte) (int, error) {
	m.t.mu.Lock()
	m.t.input.Write(b)
	m.t.mu.Unlock()
	return m.File.Write(b)
}

func (t *hostTerminal) NewPty(ctx context.Context) (workspaceapi.Pty, error) {
	pty, err := t.PtyExecutor.NewPty(ctx)
	if err != nil {
		return pty, err
	}
	pty.Master = typedMaster{File: pty.Master, t: t}
	return pty, nil
}

func (t *hostTerminal) typed() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.input.String()
}

type nopHost struct{}

func (nopHost) PublishEvent(term.Event) error { return nil }

func (nopHost) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopHost) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopHost) UpdateNotificationProgress(string, string, int64, int64) error { return nil }

func (nopHost) Tab(
	workspaceapi.URI, rune, string, browserapi.Handler,
) (browserapi.Handler, error) {
	return nil, errors.ErrUnsupported
}

func (nopHost) SetTabName(workspaceapi.URI, string, term.Attributes) error { return nil }

func (nopHost) OnTabExit(workspaceapi.URI) bool { return true }

func (nopHost) SetTabStatus(workspaceapi.URI, programstatus.Report) error { return nil }

func (nopHost) EndTabStatus(workspaceapi.URI) error { return nil }

// replicaHost records what the replicas tell the Rune showing them.
type replicaHost struct {
	nopHost
	mu      sync.Mutex
	exits   []workspaceapi.URI
	notices []string
}

func (h *replicaHost) OnTabExit(uri workspaceapi.URI) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.exits = append(h.exits, uri)
	return true
}

func (h *replicaHost) Notify(_ browserapi.NotificationLevel, format string, args ...any) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notices = append(h.notices, strings.TrimSpace(strings.ReplaceAll(format, "%v", "")))
	_ = args
	return "", nil
}

func (h *replicaHost) exited() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.exits)
}

// fakeHost is a host over a registry in this process,
// whose connections a test can cut and whose reachability it can
// toggle.
type fakeHost struct {
	r *vteregistry.Registry

	mu    sync.Mutex
	down  bool
	conns []net.Conn
	dials int
}

var _ host = (*fakeHost)(nil)

func (h *fakeHost) Root(context.Context) (string, error) {
	return "/work", nil
}

func (h *fakeHost) ListVTEs(ctx context.Context) ([]vte.Info, error) {
	if h.isDown() {
		return nil, errUnreachable
	}
	return h.r.ListVTEs(ctx)
}

func (h *fakeHost) VTELiveness(ctx context.Context, id vte.ID) (vte.Liveness, error) {
	return h.r.VTELiveness(ctx, id)
}

func (h *fakeHost) DeleteVTE(ctx context.Context, id vte.ID) error {
	return h.r.DeleteVTE(ctx, id)
}

var errUnreachable = errors.New("host unreachable")

func (h *fakeHost) WatchVTEs(ctx context.Context) (iterator.Iterator[vte.Change], error) {
	if h.isDown() {
		return nil, errUnreachable
	}
	ch := make(chan vte.Change, 256)
	wctx, cancel := context.WithCancel(ctx)
	h.r.Watch(wctx, func(change vte.Change) {
		select {
		case ch <- change:
		default:
		}
	})
	return iterator.FromFunc(func(ctx context.Context) (vte.Change, bool, error) {
		select {
		case change := <-ch:
			return change, true, nil
		case <-ctx.Done():
			return vte.Change{}, false, nil
		case <-wctx.Done():
			return vte.Change{}, false, nil
		}
	}, func() error {
		cancel()
		return nil
	}), nil
}

func (h *fakeHost) DialVTE(context.Context) (io.ReadWriteCloser, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dials++
	if h.down {
		return nil, errUnreachable
	}
	client, server := net.Pipe()
	h.conns = append(h.conns, server)
	go debug.CapturePanicReport(func() {
		_ = h.r.Accept(context.Background(), server)
	})
	return client, nil
}

func (h *fakeHost) isDown() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.down
}

func (h *fakeHost) setDown(down bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.down = down
}

// cut closes every connection the host accepted so far, as a network
// loss does.
func (h *fakeHost) cut() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.conns {
		_ = c.Close()
	}
	h.conns = nil
}

func (h *fakeHost) dialed() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.dials
}

type fixture struct {
	loop  *loop
	term  *hostTerminal
	host  *fakeHost
	shown *replicaHost
	o     *Owner
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		loop:  newLoop(t),
		term:  &hostTerminal{PtyExecutor: workspacetest.NewPtyExecutor()},
		shown: &replicaHost{},
	}
	hostCfg := vte.DefaultConfig()
	hostCfg.ScheduleNextTick = f.loop.schedule
	r := vteregistry.New(nopHost{}, nopHost{}, f.term, f.term, nopHost{}, vteregistry.Config{
		VTE: hostCfg, PollInterval: 10 * time.Millisecond,
	})
	t.Cleanup(func() { _ = r.Close() })
	f.host = &fakeHost{r: r}

	cfg := vte.DefaultConfig()
	cfg.ScheduleNextTick = f.loop.schedule
	f.o = newOwner(f.host, f.shown, f.shown, f.shown, Config{VTE: cfg, Machine: "peer"})
	t.Cleanup(func() { _ = f.o.Close() })
	return f
}

// create opens a terminal on the host and returns its replica and
// process.
func (f *fixture) create(t *testing.T, width, height int) (*replica, *workspacetest.Process) {
	t.Helper()
	v, err := f.o.CreateVTE(context.Background(), vte.CreateOptions{
		Width: width, Height: height,
	})
	require.NoError(t, err)
	select {
	case p := <-f.term.Started():
		return v.(*replica), p
	case <-time.After(5 * time.Second):
		t.Fatal("the terminal's process never started")
	}
	return nil, nil
}

// info is what the host lists of the terminal id, asked directly so
// a test can look while the host is unreachable.
func (f *fixture) info(t *testing.T, id vte.ID) vte.Info {
	t.Helper()
	infos, err := f.host.r.ListVTEs(context.Background())
	require.NoError(t, err)
	for _, info := range infos {
		if info.ID == id {
			return info
		}
	}
	t.Fatalf("terminal %s is not listed", id)
	return vte.Info{}
}

// screen renders the replica's active screen.
func screen(t *testing.T, v *replica) string {
	t.Helper()
	snap, err := v.Handler.Component().Snapshot()
	require.NoError(t, err)
	var b strings.Builder
	for _, row := range snap.ActiveCells() {
		for _, c := range row {
			if c.Ch == 0 {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(c.Ch)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func shows(t *testing.T, v *replica, want string) {
	t.Helper()
	require.Eventually(t, func() bool {
		return strings.Contains(screen(t, v), want)
	}, 10*time.Second, 5*time.Millisecond, "the replica never showed %q", want)
}

func TestOwnerViews(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	v, p := f.create(t, 20, 4)
	p.Print("hello")
	shows(t, v, "hello")
	assert.Equal(t, 1, f.info(t, v.ID()).Attachments)
	assert.Equal(t, f.info(t, v.ID()).URI, v.URI(), "a replica has the terminal's uri")
	assert.False(t, v.IsComplete())

	other, err := f.o.AttachVTE(ctx, v.ID())
	require.NoError(t, err)
	assert.Equal(t, v.ID(), other.ID())
	assert.Equal(t, 2, f.info(t, v.ID()).Attachments)
	p.Print(" again")
	shows(t, other.(*replica), "hello again")
	shows(t, v, "hello again")

	preview, err := f.o.PreviewVTE(ctx, v.ID())
	require.NoError(t, err)
	assert.Equal(t, 2, f.info(t, v.ID()).Attachments, "a preview is not an attachment")
	shows(t, preview.(*replica), "hello again")

	require.NoError(t, other.Close())
	require.NoError(t, other.Close(), "closing twice detaches once")
	require.NoError(t, preview.Close())
	require.Eventually(t, func() bool { return f.info(t, v.ID()).Attachments == 1 },
		5*time.Second, 5*time.Millisecond, "closing a replica detaches it")

	_, err = f.o.AttachVTE(ctx, "missing")
	require.ErrorIs(t, err, vte.ErrNotFound)
	_, err = f.o.PreviewVTE(ctx, "missing")
	require.ErrorIs(t, err, vte.ErrNotFound)

	f.term.fg.Store(int64(p.Pid))
	l, err := f.o.VTELiveness(ctx, v.ID())
	require.NoError(t, err)
	assert.Equal(t, vte.LivenessIdle, l)

	require.NoError(t, f.o.Close())
	require.Eventually(t, func() bool { return f.info(t, v.ID()).Attachments == 0 },
		5*time.Second, 5*time.Millisecond, "closing the owner detaches its replicas")
	_, err = f.o.CreateVTE(ctx, vte.CreateOptions{})
	require.ErrorIs(t, err, ErrClosed)
}

func TestReplicaInputAndSize(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v, p := f.create(t, 20, 4)
	p.Print("$ ")
	shows(t, v, "$ ")

	require.NoError(t, v.Handler.Component().WriteToPty([]byte("ls\r")))
	require.Eventually(t, func() bool { return f.term.typed() == "ls\r" },
		5*time.Second, 5*time.Millisecond, "input reaches the host's pty")

	f.loop.do(func() { v.Resize(30, 8) })
	require.Eventually(t, func() bool {
		info := f.info(t, v.ID())
		return info.Width == 30 && info.Height == 8
	}, 5*time.Second, 5*time.Millisecond, "the owner's size follows the replica's")
	require.Eventually(t, func() bool {
		comp := v.Handler.Component()
		return comp.MaxWidth() == 30 && comp.Height() == 8
	}, 5*time.Second, 5*time.Millisecond, "the replica reflows when the host says so")

	other, err := f.o.AttachVTE(context.Background(), v.ID())
	require.NoError(t, err)
	w := other.(*replica)
	f.loop.do(func() { w.Resize(40, 10) })
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 30, f.info(t, v.ID()).Width, "a second replica does not own the size")
	f.loop.do(func() { w.OnFocusChange(true) })
	require.Eventually(t, func() bool { return f.info(t, v.ID()).Width == 40 },
		5*time.Second, 5*time.Millisecond, "until it is focused")
	require.Eventually(t, func() bool { return v.Handler.Component().MaxWidth() == 40 },
		5*time.Second, 5*time.Millisecond, "and every replica follows")
}

func TestReplicaResyncsAfterConnectionLoss(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v, p := f.create(t, 40, 4)
	p.Print("before")
	shows(t, v, "before")

	f.host.setDown(true)
	f.host.cut()
	require.Eventually(t, func() bool { return f.info(t, v.ID()).Attachments == 0 },
		5*time.Second, 5*time.Millisecond, "the host sees the attachment end")
	p.Print(" while away")
	require.Eventually(t, func() bool { return f.host.dialed() >= 2 },
		5*time.Second, 5*time.Millisecond, "the replica tries to attach again")
	assert.Contains(t, screen(t, v), "before", "it keeps showing what it had")

	f.host.setDown(false)
	shows(t, v, "before while away")
	assert.Equal(t, 1, f.info(t, v.ID()).Attachments, "it is attached again")
	p.Print(" and after")
	shows(t, v, "before while away and after")
	assert.Zero(t, f.shown.exited(), "the tab stays")
}

func TestReplicaResyncsWhenSlow(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v, p := f.create(t, 20, 4)
	p.Print("start")
	shows(t, v, "start")

	// Holding the replica's feed is what a client that cannot keep up
	// looks like from the host: frames queue until the host gives up.
	release := make(chan struct{})
	held := make(chan struct{})
	go debug.CapturePanicReport(func() {
		v.Handler.Component().WithFeedHeld(func() {
			close(held)
			<-release
		})
	})
	<-held
	flood := strings.Repeat(strings.Repeat("x", 1023)+"\n", (vtewire.DefaultQueueBytes+(1<<20))/1024)
	const end = "FLOOD END"
	p.Print(flood + "\r\n" + end)
	require.Eventually(t, func() bool { return f.info(t, v.ID()).Attachments == 0 },
		10*time.Second, 5*time.Millisecond, "the host drops the slow attachment")
	close(release)

	shows(t, v, end)
	require.Eventually(t, func() bool { return f.info(t, v.ID()).Attachments == 1 },
		5*time.Second, 5*time.Millisecond, "the replica attached again")
	p.Print(" more")
	shows(t, v, end+" more")
}

func TestReplicaDeletedTerminal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v, p := f.create(t, 20, 4)
	p.Print("x")
	shows(t, v, "x")

	require.NoError(t, f.o.DeleteVTE(context.Background(), v.ID()))
	require.Eventually(t, func() bool { return f.shown.exited() == 1 },
		5*time.Second, 5*time.Millisecond, "the tab is told the terminal ended")
	require.Eventually(t, v.IsComplete, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, 1, f.host.dialed(), "a gone terminal is not attached to again")
	f.shown.mu.Lock()
	defer f.shown.mu.Unlock()
	assert.Empty(t, f.shown.notices, "a terminal ending is no error")
}

func TestReplicaProcessExit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v, p := f.create(t, 20, 4)
	p.Print("bye")
	shows(t, v, "bye")
	p.HangUp()
	p.ReportExit(errors.New("exit status 2"))
	require.Eventually(t, func() bool { return f.shown.exited() == 1 },
		5*time.Second, 5*time.Millisecond)
	assert.True(t, v.IsComplete())
	assert.EqualError(t, v.ExitErr(), "exit status 2")
}

func TestOwnerWatch(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	v, _ := f.create(t, 20, 4)

	var mu sync.Mutex
	var changes []vte.Change
	f.o.Watch(ctx, func(change vte.Change) {
		mu.Lock()
		defer mu.Unlock()
		changes = append(changes, change)
	})
	got := func() []vte.Change {
		mu.Lock()
		defer mu.Unlock()
		return append([]vte.Change(nil), changes...)
	}
	require.Eventually(t, func() bool { return len(got()) >= 1 },
		5*time.Second, 5*time.Millisecond)
	first := got()[0]
	assert.Equal(t, v.ID(), first.ID, "what the host lists is told first")
	assert.Equal(t, 1, first.Attachments)

	other, err := f.o.AttachVTE(ctx, v.ID())
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		c := got()
		return c[len(c)-1].Attachments == 2
	}, 5*time.Second, 5*time.Millisecond, "the host's changes are relayed")
	require.NoError(t, other.Close())

	// The stream drops: whatever happened meanwhile is caught up with
	// from a fresh listing, deletions included.
	f.host.setDown(true)
	f.host.cut()
	before := len(got())
	require.Eventually(t, func() bool { return f.host.r.Close() == nil },
		time.Second, 5*time.Millisecond)
	f.host.setDown(false)
	require.Eventually(t, func() bool {
		c := got()
		return len(c) > before && c[len(c)-1].Deleted && c[len(c)-1].ID == v.ID()
	}, 10*time.Second, 5*time.Millisecond, "the deletion is told after the stream resumes")
}

func TestOwnerVersionMismatch(t *testing.T) {
	t.Parallel()
	loop := newLoop(t)
	cfg := vte.DefaultConfig()
	cfg.ScheduleNextTick = loop.schedule
	host := &refusingHost{}
	o := newOwner(host, nopHost{}, nopHost{}, nopHost{}, Config{VTE: cfg, Machine: "peer"})
	t.Cleanup(func() { _ = o.Close() })

	_, err := o.CreateVTE(context.Background(), vte.CreateOptions{})
	var verr *VersionError
	require.ErrorAs(t, err, &verr)
	assert.Equal(t, uint16(3), verr.Host)
	assert.Equal(t, vtewire.Version, verr.Client)
	assert.Contains(t, err.Error(), "peer")
	assert.Contains(t, err.Error(), "older")
}

// refusingHost answers every attachment as a host speaking an older
// protocol would.
type refusingHost struct{ fakeHost }

func (h *refusingHost) DialVTE(context.Context) (io.ReadWriteCloser, error) {
	client, server := net.Pipe()
	go debug.CapturePanicReport(func() {
		conn := vtewire.NewConn(server)
		if _, err := conn.ReadFrame(); err != nil {
			return
		}
		_ = conn.WriteFrame(&vtewire.HelloErr{
			Code:    vtewire.CodeVersion,
			Message: "server speaks version 3, client speaks 5",
		})
		_ = conn.Close()
	})
	return client, nil
}
