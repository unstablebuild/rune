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
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtesnap"
	"unstable.build/rune/internal/term/vte/vtewire"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// wireMember is a test's end of one attachment served over net.Pipe.
type wireMember struct {
	conn *vtewire.Conn
	// served is the result of serving the attachment, once it ended.
	served chan error
}

// dial attaches to the registry as a wire client would and returns
// the member with the server's reply and snapshot. A refused HELLO is
// returned as the error.
func (f *fixture) dial(
	t *testing.T, hello *vtewire.Hello,
) (*wireMember, *vtewire.HelloOK, *vtesnap.Snapshot, error) {
	t.Helper()
	clientEnd, serverEnd := net.Pipe()
	m := &wireMember{conn: vtewire.NewConn(clientEnd), served: make(chan error, 1)}
	go debug.CapturePanicReport(func() {
		m.served <- f.r.Accept(context.Background(), serverEnd)
	})
	t.Cleanup(func() { _ = m.conn.Close() })
	ok, err := vtewire.Dial(m.conn, hello)
	if err != nil {
		return nil, nil, nil, err
	}
	frame := m.read(t)
	raw, isSnapshot := frame.(*vtewire.Snapshot)
	require.True(t, isSnapshot, "expected SNAPSHOT after HELLO_OK, got %s", frame.Type())
	dec, err := vtesnap.NewDecoder(bytes.NewReader(raw.Records))
	require.NoError(t, err)
	snap, err := dec.Ready()
	require.NoError(t, err)
	return m, ok, snap, nil
}

// read returns the next frame, failing the test when none arrives.
func (m *wireMember) read(t *testing.T) vtewire.Frame {
	t.Helper()
	type result struct {
		frame vtewire.Frame
		err   error
	}
	ch := make(chan result, 1)
	go debug.CapturePanicReport(func() {
		frame, err := m.conn.ReadFrame()
		ch <- result{frame, err}
	})
	select {
	case r := <-ch:
		require.NoError(t, r.err)
		return r.frame
	case <-time.After(5 * time.Second):
		t.Fatal("no frame arrived")
		return nil
	}
}

// output reads frames until it collected want bytes of OUTPUT, returning
// every other frame it met on the way.
func (m *wireMember) output(t *testing.T, want string) (others []vtewire.Frame) {
	t.Helper()
	var got strings.Builder
	for got.Len() < len(want) {
		switch f := m.read(t).(type) {
		case *vtewire.Output:
			got.Write(f.Data)
		default:
			others = append(others, f)
		}
	}
	assert.Equal(t, want, got.String())
	return others
}

// ended waits for the server to finish serving the attachment.
func (m *wireMember) ended(t *testing.T) error {
	t.Helper()
	select {
	case err := <-m.served:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the attachment was not ended")
		return nil
	}
}

func rowsText(rows [][]term.Cell) string {
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		for _, c := range row {
			if c.Ch == 0 {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(c.Ch)
		}
	}
	return b.String()
}

func TestRegistryWireCreate(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m, ok, snap, err := f.dial(t, &vtewire.Hello{
		Width: 10, Height: 4, Create: vtewire.CreateOptions{CommandAndArgs: []string{"top"}},
	})
	require.NoError(t, err)
	var p *workspacetest.Process
	select {
	case p = <-f.term.Started():
	case <-time.After(5 * time.Second):
		t.Fatal("the terminal's process never started")
	}
	assert.Equal(t, "top", p.Cmd.Path)

	id := vte.ID(ok.VTEID)
	info := f.info(t, id)
	assert.Equal(t, info.URI.String(), ok.URI)
	assert.Equal(t, 1, info.Attachments, "a wire member is an attachment")
	assert.Equal(t, [2]int{10, 4}, [2]int{ok.Width, ok.Height})
	assert.Equal(t, [2]int{10, 4}, [2]int{snap.Terminal.Width, snap.Terminal.Height})
	assert.False(t, ok.Complete)

	p.Print("hello")
	m.output(t, "hello")

	require.NoError(t, m.conn.WriteFrame(&vtewire.Input{Data: []byte("ls\r")}))
	require.Eventually(t, func() bool { return f.term.typed() == "ls\r" },
		5*time.Second, 5*time.Millisecond, "input reaches the pty")

	require.NoError(t, m.conn.WriteFrame(&vtewire.Resize{Width: 20, Height: 6}))
	assert.Equal(t, &vtewire.Resize{Width: 20, Height: 6}, m.read(t),
		"the owner's resize is told to every member, in stream order")
	info = f.info(t, id)
	assert.Equal(t, [2]int{20, 6}, [2]int{info.Width, info.Height})

	require.NoError(t, m.conn.WriteFrame(&vtewire.Detach{}))
	require.NoError(t, m.ended(t))
	assert.Equal(t, 0, f.info(t, id).Attachments, "detaching leaves the terminal")
}

func TestRegistryWireAttach(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v, p := f.create(t, 10, 4)
	p.Print("before")
	require.Eventually(t, func() bool {
		return strings.Contains(drawn(v, 10, 4), "before")
	}, 5*time.Second, 5*time.Millisecond)

	m, ok, snap, err := f.dial(t, &vtewire.Hello{VTEID: string(v.ID()), Width: 10, Height: 4})
	require.NoError(t, err)
	assert.Equal(t, string(v.ID()), ok.VTEID)
	assert.Equal(t, 2, f.info(t, v.ID()).Attachments)
	assert.Contains(t, rowsText(snap.Primary.Rows), "before",
		"what was printed before attaching is in the snapshot")

	p.Print("after")
	m.output(t, "after")

	_, _, _, err = f.dial(t, &vtewire.Hello{VTEID: "missing"})
	var wireErr *vtewire.Error
	require.ErrorAs(t, err, &wireErr)
	assert.True(t, wireErr.NotFound())

	require.NoError(t, m.conn.Close())
	require.Error(t, m.ended(t), "a dropped connection ends the attachment")
	assert.Equal(t, 1, f.info(t, v.ID()).Attachments)
}

func TestRegistryWirePreview(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v, p := f.create(t, 10, 4)
	f.term.fg.Store(int64(p.Pid))
	require.NoError(t, v.Close())

	m, _, _, err := f.dial(t, &vtewire.Hello{
		VTEID: string(v.ID()), Preview: true, Width: 30, Height: 8,
	})
	require.NoError(t, err)
	info := f.info(t, v.ID())
	assert.Equal(t, 0, info.Attachments, "a preview leaves the terminal detached")
	assert.Equal(t, [2]int{30, 8}, [2]int{info.Width, info.Height},
		"a preview owns the size of a detached terminal")

	again := f.attach(t, v.ID())
	again.Resize(20, 6)
	assert.Equal(t, &vtewire.Resize{Width: 20, Height: 6}, m.read(t),
		"an attachment takes the size over")
	require.NoError(t, m.conn.WriteFrame(&vtewire.Resize{Width: 30, Height: 8}))
	assert.Equal(t, &vtewire.Resize{Width: 20, Height: 6}, m.read(t),
		"a request that lost is answered with the size in force")
}

func TestRegistryWireResizeOwnership(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v, _ := f.create(t, 10, 4)
	v.Resize(10, 4)

	m, ok, _, err := f.dial(t, &vtewire.Hello{
		VTEID: string(v.ID()), Width: 30, Height: 8,
		CellPixelWidth: 9, CellPixelHeight: 18,
	})
	require.NoError(t, err)
	assert.Equal(t, [2]int{10, 4}, [2]int{ok.Width, ok.Height},
		"a second attachment does not own the size")

	require.NoError(t, m.conn.WriteFrame(&vtewire.Focus{Focused: true}))
	assert.Equal(t, &vtewire.Resize{
		Width: 30, Height: 8, CellPixelWidth: 9, CellPixelHeight: 18,
	}, m.read(t), "focusing takes the size over, cell size included")
	info := f.info(t, v.ID())
	assert.Equal(t, [2]int{30, 8}, [2]int{info.Width, info.Height})

	v.OnFocusChange(true)
	assert.Equal(t, &vtewire.Resize{
		Width: 10, Height: 4, CellPixelWidth: 9, CellPixelHeight: 18,
	}, m.read(t), "an in-process view takes it back; the cell size stays")

	require.NoError(t, m.conn.WriteFrame(&vtewire.Resize{Width: 40, Height: 9}))
	assert.Equal(t, &vtewire.Resize{
		Width: 10, Height: 4, CellPixelWidth: 9, CellPixelHeight: 18,
	}, m.read(t), "a member that lost the size is told what it is")
}

func TestRegistryWireHistoryAndExit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v, p := f.create(t, 10, 2)
	p.Print("one\r\ntwo\r\nthree\r\nfour\r\nfive")
	require.Eventually(t, func() bool {
		return strings.Contains(drawn(v, 10, 2), "five")
	}, 5*time.Second, 5*time.Millisecond)

	m, _, snap, err := f.dial(t, &vtewire.Hello{VTEID: string(v.ID()), Width: 10, Height: 2})
	require.NoError(t, err)
	assert.Equal(t, "four      \nfive      ", rowsText(snap.Primary.Rows))
	page, isPage := m.read(t).(*vtewire.HistoryPage)
	require.True(t, isPage)
	assert.Equal(t, 0, page.Remaining)
	kind, rows, err := vtesnap.DecodePage(page.Page)
	require.NoError(t, err)
	assert.Equal(t, vtesnap.ScreenPrimary, kind)
	assert.Equal(t, "one       \ntwo       \nthree     ", rowsText(rows),
		"the scrollback follows the snapshot")

	p.HangUp()
	p.ReportExit(errors.New("exit status 3"))
	assert.Equal(t, &vtewire.Exit{Err: "exit status 3"}, m.read(t))

	require.NoError(t, f.r.DeleteVTE(context.Background(), v.ID()))
	dropped, isDrop := m.read(t).(*vtewire.Dropped)
	require.True(t, isDrop, "a deleted terminal's members are told")
	assert.Equal(t, vtewire.CodeNotFound, dropped.Code)
	require.NoError(t, m.ended(t))
}

func TestRegistryCloseTellsWireMembers(t *testing.T) {
	t.Parallel()
	t.Run("waits for a member to be told", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		v, _ := f.create(t, 10, 4)
		m, _, _, err := f.dial(t, &vtewire.Hello{VTEID: string(v.ID()), Width: 10, Height: 4})
		require.NoError(t, err)
		// The pipe hands a frame over only as it is read, so a member
		// that reads late is one whose telling takes that long.
		const late = 200 * time.Millisecond
		frame := make(chan vtewire.Frame, 1)
		go debug.CapturePanicReport(func() {
			time.Sleep(late)
			f, err := m.conn.ReadFrame()
			if err == nil {
				frame <- f
			}
			close(frame)
		})

		start := time.Now()
		require.NoError(t, f.r.Close())
		assert.GreaterOrEqual(t, time.Since(start), late, "Close waited for the member")
		select {
		case got := <-frame:
			dropped, ok := got.(*vtewire.Dropped)
			require.True(t, ok, "expected DROPPED, got %v", got)
			assert.Equal(t, vtewire.CodeNotFound, dropped.Code)
		case <-time.After(time.Second):
			t.Fatal("the member was not told")
		}
		require.NoError(t, m.ended(t))
	})
	t.Run("gives up on a member that reads nothing", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		v, _ := f.create(t, 10, 4)
		_, _, _, err := f.dial(t, &vtewire.Hello{VTEID: string(v.ID()), Width: 10, Height: 4})
		require.NoError(t, err)

		start := time.Now()
		require.NoError(t, f.r.Close())
		took := time.Since(start)
		assert.GreaterOrEqual(t, took, defaultDropGrace)
		assert.Less(t, took, defaultDropGrace+2*time.Second)
	})
}

func TestRegistryWireSlowMemberIsDropped(t *testing.T) {
	t.Parallel()
	// More than a member's queue holds, printed while it reads nothing.
	flood := strings.Repeat(strings.Repeat("x", 1023)+"\n", (vtewire.DefaultQueueBytes+(1<<20))/1024)
	const end = "FLOOD END"

	t.Run("a member that resumes is told to resync", func(t *testing.T) {
		t.Parallel()
		// The member resumes once the terminal drew the flood, which a
		// loaded host takes seconds to; a grace shorter than that closes
		// the connection before the member reads why.
		f := newFixture(t, func(cfg *Config) { cfg.DropGrace = time.Minute })
		v, p := f.create(t, 20, 4)
		m, _, _, err := f.dial(t, &vtewire.Hello{VTEID: string(v.ID()), Width: 20, Height: 4})
		require.NoError(t, err)
		assert.Equal(t, 2, f.info(t, v.ID()).Attachments)

		p.Print(flood + "\r\n" + end)
		require.Eventually(t, func() bool {
			return strings.Contains(drawn(v, 20, 4), end)
		}, 10*time.Second, 5*time.Millisecond,
			"a member that stopped reading must not stall the terminal")

		for {
			frame := m.read(t)
			dropped, isDrop := frame.(*vtewire.Dropped)
			if !isDrop {
				_, isOutput := frame.(*vtewire.Output)
				require.True(t, isOutput, "unexpected %s", frame.Type())
				continue
			}
			assert.Equal(t, vtewire.CodeSlow, dropped.Code)
			break
		}
		_, err = m.conn.ReadFrame()
		require.Error(t, err, "the connection is closed after the drop")
		require.NoError(t, m.ended(t), "the drop is no fault of the member's")
		assert.Equal(t, 1, f.info(t, v.ID()).Attachments, "the member is detached")

		_, _, snap, err := f.dial(t, &vtewire.Hello{VTEID: string(v.ID()), Width: 20, Height: 4})
		require.NoError(t, err, "the terminal is still there to attach to")
		assert.Contains(t, rowsText(snap.Primary.Rows), end, "and resyncs from a fresh snapshot")
	})

	t.Run("a member that never resumes is closed", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		v, p := f.create(t, 20, 4)
		m, _, _, err := f.dial(t, &vtewire.Hello{VTEID: string(v.ID()), Width: 20, Height: 4})
		require.NoError(t, err)

		p.Print(flood + "\r\n" + end)
		require.Eventually(t, func() bool {
			return strings.Contains(drawn(v, 20, 4), end)
		}, 10*time.Second, 5*time.Millisecond)
		require.NoError(t, m.ended(t), "the drop does not wait on the member")
		assert.Equal(t, 1, f.info(t, v.ID()).Attachments)
	})
}

func TestServe(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	other := newFixture(t)
	lis, err := net.Listen("unix", filepath.Join(shortTempDir(t), "vte.sock"))
	require.NoError(t, err)

	errBadRoot := errors.New("no such workspace")
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		served <- Serve(ctx, lis, func(_ context.Context, root string) (*Registry, error) {
			switch root {
			case "/a":
				return f.r, nil
			case "/b":
				return other.r, nil
			}
			return nil, errBadRoot
		})
	})

	dial := func(root string) (*vtewire.HelloOK, error) {
		conn, err := net.Dial("unix", lis.Addr().String())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		return vtewire.Dial(vtewire.NewConn(conn), &vtewire.Hello{Root: root, Width: 10, Height: 4})
	}
	okA, err := dial("/a")
	require.NoError(t, err)
	okB, err := dial("/b")
	require.NoError(t, err)
	_, err = dial("/c")
	var wireErr *vtewire.Error
	require.ErrorAs(t, err, &wireErr)
	assert.Equal(t, vtewire.CodeUnknown, wireErr.Code)
	assert.Contains(t, wireErr.Message, errBadRoot.Error())

	require.Eventually(t, func() bool {
		a, errA := f.r.ListVTEs(ctx)
		b, errB := other.r.ListVTEs(ctx)
		return errA == nil && errB == nil && len(a) == 1 && len(b) == 1 &&
			string(a[0].ID) == okA.VTEID && string(b[0].ID) == okB.VTEID
	}, 5*time.Second, 5*time.Millisecond, "each root's terminal lands in its registry")

	cancel()
	require.NoError(t, lis.Close())
	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return once its listener closed")
	}
}

// shortTempDir is a temporary directory whose path fits a unix socket
// address, which t.TempDir's may not.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "vte")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// typedMaster records what is typed into a pty, which the fake process
// behind it ignores.
type typedMaster struct {
	workspaceapi.File
	t *fgTerminal
}

func (m typedMaster) Write(b []byte) (int, error) {
	m.t.mu.Lock()
	m.t.input.Write(b)
	m.t.mu.Unlock()
	return m.File.Write(b)
}

func (t *fgTerminal) NewPty(ctx context.Context) (workspaceapi.Pty, error) {
	pty, err := t.PtyExecutor.NewPty(ctx)
	if err != nil {
		return pty, err
	}
	pty.Master = typedMaster{File: pty.Master, t: t}
	return pty, nil
}

// typed is everything typed into the terminal's ptys so far.
func (t *fgTerminal) typed() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.input.String()
}
