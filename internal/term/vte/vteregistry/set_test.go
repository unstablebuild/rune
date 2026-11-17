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
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtewire"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func newTestSet(t *testing.T) (*Set, *workspacetest.PtyScheme) {
	t.Helper()
	scheme := workspacetest.NewPtyScheme("/")
	scheme.Missing = map[string]bool{"/missing": true}
	cfg := vte.DefaultConfig()
	cfg.ScheduleNextTick = nil
	s := NewSet(scheme, Config{VTE: cfg, PollInterval: 10 * time.Millisecond})
	t.Cleanup(func() { _ = s.Close() })
	return s, scheme
}

// started waits for a process to start on e.
func started(t *testing.T, e *workspacetest.PtyExecutor) *workspacetest.Process {
	t.Helper()
	select {
	case p := <-e.Started():
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no process started")
		return nil
	}
}

func TestSetResolvesOneRegistryPerRoot(t *testing.T) {
	t.Parallel()
	s, scheme := newTestSet(t)
	ctx := context.Background()

	a, err := s.Resolve(ctx, "/home/a")
	require.NoError(t, err)
	again, err := s.Resolve(ctx, "/home/a")
	require.NoError(t, err)
	assert.Same(t, a, again, "the same root resolves to the same registry")
	b, err := s.Resolve(ctx, "/home/b")
	require.NoError(t, err)
	assert.NotSame(t, a, b, "another root is another registry")
	own, err := s.Resolve(ctx, "")
	require.NoError(t, err)
	assert.NotSame(t, a, own)

	_, err = s.Resolve(ctx, "/missing")
	assert.ErrorIs(t, err, os.ErrNotExist, "a root that is not there is refused")

	// A terminal of a root runs on the scheme chrooted there.
	_, err = a.CreateVTE(ctx, vte.CreateOptions{})
	require.NoError(t, err)
	started(t, scheme.Chrooted("/home/a").PtyExecutor)
	infos, err := b.ListVTEs(ctx)
	require.NoError(t, err)
	assert.Empty(t, infos, "the other root's registry has no terminal")

	owner, err := s.Host(ctx, "/home/a")
	require.NoError(t, err)
	infos, err = owner.ListVTEs(ctx)
	require.NoError(t, err)
	assert.Len(t, infos, 1)
}

func TestSetWatchesDetachedTerminals(t *testing.T) {
	t.Parallel()
	s, scheme := newTestSet(t)
	ctx := context.Background()
	r, err := s.Resolve(ctx, "/home/a")
	require.NoError(t, err)

	changes := make(chan vte.Change, 64)
	r.Watch(ctx, func(change vte.Change) { changes <- change })

	v, err := r.CreateVTE(ctx, vte.CreateOptions{})
	require.NoError(t, err)
	started(t, scheme.Chrooted("/home/a").PtyExecutor)
	// A program in the foreground, as the host of the pty tells it.
	scheme.Chrooted("/home/a").Foreground.Store(4242)
	require.NoError(t, v.Close())

	// What a detached terminal runs is told on the Set's own loop: the
	// change arrives with no event loop of a test's.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case change := <-changes:
			if change.Liveness == vte.LivenessRunning {
				return
			}
		case <-deadline:
			t.Fatal("the detached terminal was never told running")
		}
	}
}

func TestSetServesAttachmentsByRoot(t *testing.T) {
	t.Parallel()
	s, scheme := newTestSet(t)
	dir := t.TempDir()
	lis, err := net.Listen("unix", filepath.Join(dir, "a.sock"))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go debug.CapturePanicReport(func() { served <- s.Serve(ctx, lis) })
	t.Cleanup(func() {
		cancel()
		_ = lis.Close()
		<-served
	})

	rw, err := net.Dial("unix", lis.Addr().String())
	require.NoError(t, err)
	conn := vtewire.NewConn(rw)
	t.Cleanup(func() { _ = conn.Close() })
	ok, err := vtewire.Dial(conn, &vtewire.Hello{Root: "/home/a", Width: 20, Height: 4})
	require.NoError(t, err)
	started(t, scheme.Chrooted("/home/a").PtyExecutor)

	r, err := s.Resolve(ctx, "/home/a")
	require.NoError(t, err)
	infos, err := r.ListVTEs(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, vte.ID(ok.VTEID), infos[0].ID)
	assert.Equal(t, 1, infos[0].Attachments, "a wire member is attached")

	rw, err = net.Dial("unix", lis.Addr().String())
	require.NoError(t, err)
	refused := vtewire.NewConn(rw)
	t.Cleanup(func() { _ = refused.Close() })
	_, err = vtewire.Dial(refused, &vtewire.Hello{Root: "/missing"})
	var wireErr *vtewire.Error
	require.ErrorAs(t, err, &wireErr)
	assert.Equal(t, vtewire.CodeUnknown, wireErr.Code)
}

func TestSetCloseEndsTerminalsAndChroots(t *testing.T) {
	t.Parallel()
	s, scheme := newTestSet(t)
	ctx := context.Background()
	r, err := s.Resolve(ctx, "/home/a")
	require.NoError(t, err)
	v, err := r.CreateVTE(ctx, vte.CreateOptions{})
	require.NoError(t, err)
	started(t, scheme.Chrooted("/home/a").PtyExecutor)

	require.NoError(t, s.Close())
	assert.True(t, v.IsComplete(), "the terminal ended with the Set")
	assert.True(t, scheme.Chrooted("/home/a").Closed(), "the chroot is closed")
	assert.False(t, scheme.Closed(), "the served scheme is the host's")
	_, err = s.Resolve(ctx, "/home/b")
	assert.True(t, errors.Is(err, ErrClosed))
}
