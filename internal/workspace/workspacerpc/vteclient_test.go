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

package workspacerpc

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	grpc "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/workspace/workspacerpc/vterpc"
)

// fakeHost is a vte.Host holding only what the lifecycle
// RPCs reach: a listing, liveness per terminal, and watchers.
type fakeHost struct {
	mu       sync.Mutex
	infos    []vte.Info
	liveness map[vte.ID]vte.Liveness
	deleted  []vte.ID
	watchers []func(vte.Change)
}

var _ vte.Host = (*fakeHost)(nil)

func (*fakeHost) CreateVTE(context.Context, vte.CreateOptions) (vte.View, error) {
	return nil, errors.ErrUnsupported
}

func (*fakeHost) AttachVTE(context.Context, vte.ID) (vte.View, error) {
	return nil, errors.ErrUnsupported
}

func (*fakeHost) PreviewVTE(context.Context, vte.ID) (vte.View, error) {
	return nil, errors.ErrUnsupported
}

func (o *fakeHost) ListVTEs(context.Context) ([]vte.Info, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]vte.Info(nil), o.infos...), nil
}

func (o *fakeHost) VTELiveness(_ context.Context, id vte.ID) (vte.Liveness, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	l, ok := o.liveness[id]
	if !ok {
		return vte.LivenessUnknown, vte.ErrNotFound
	}
	return l, nil
}

func (o *fakeHost) DeleteVTE(_ context.Context, id vte.ID) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.liveness[id]; !ok {
		return vte.ErrNotFound
	}
	delete(o.liveness, id)
	o.deleted = append(o.deleted, id)
	return nil
}

func (o *fakeHost) Watch(ctx context.Context, fn func(vte.Change)) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.watchers = append(o.watchers, func(change vte.Change) {
		if ctx.Err() == nil {
			fn(change)
		}
	})
}

func (o *fakeHost) Close() error { return nil }

func (o *fakeHost) tell(change vte.Change) {
	o.mu.Lock()
	watchers := make([]func(vte.Change), len(o.watchers))
	copy(watchers, o.watchers)
	o.mu.Unlock()
	for _, fn := range watchers {
		fn(change)
	}
}

func (o *fakeHost) watching() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.watchers)
}

// vteFixture serves a VTEServer over bufconn, with one host per root.
type vteFixture struct {
	hosts map[string]*fakeHost
	conn  *grpc.ClientConn
}

func newVTEFixture(t *testing.T) *vteFixture {
	t.Helper()
	f := &vteFixture{hosts: map[string]*fakeHost{
		"/a": {liveness: map[vte.ID]vte.Liveness{}},
		"/b": {liveness: map[vte.ID]vte.Liveness{}},
	}}
	lis := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	vterpc.RegisterVTEServer(server, NewVTEServer(
		func(_ context.Context, root string) (vte.Host, error) {
			host, ok := f.hosts[root]
			if !ok {
				return nil, errors.New("no such workspace: " + root)
			}
			return host, nil
		}))
	go debug.CapturePanicReport(func() { _ = server.Serve(lis) })
	conn, err := grpc.NewClient("passthrough:///vte",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}))
	require.NoError(t, err)
	f.conn = conn
	t.Cleanup(func() {
		_ = conn.Close()
		server.Stop()
	})
	return f
}

func TestVTEClientLifecycle(t *testing.T) {
	t.Parallel()
	f := newVTEFixture(t)
	ctx := context.Background()
	uri, err := workspaceapi.ParseURI("file:///dev/pts/3")
	require.NoError(t, err)
	created := time.Unix(1_700_000_000, 42)
	a := f.hosts["/a"]
	a.infos = []vte.Info{{
		ID: "one", URI: uri, Title: "vim", CommandAndArgs: []string{"vim", "x"},
		Width: 80, Height: 24, Attachments: 2, Liveness: vte.LivenessRunning,
		Complete: false, CreatedAt: created,
	}}
	a.liveness["one"] = vte.LivenessRunning
	f.hosts["/b"].liveness["two"] = vte.LivenessIdle

	ca := NewVTEClient(f.conn, "/a")
	cb := NewVTEClient(f.conn, "/b")

	infos, err := ca.ListVTEs(ctx)
	require.NoError(t, err)
	assert.Equal(t, a.infos, infos, "a listing survives the wire whole")
	infos, err = cb.ListVTEs(ctx)
	require.NoError(t, err)
	assert.Empty(t, infos, "each root sees its own terminals")

	l, err := ca.VTELiveness(ctx, "one")
	require.NoError(t, err)
	assert.Equal(t, vte.LivenessRunning, l)
	l, err = cb.VTELiveness(ctx, "two")
	require.NoError(t, err)
	assert.Equal(t, vte.LivenessIdle, l)
	_, err = ca.VTELiveness(ctx, "two")
	require.ErrorIs(t, err, vte.ErrNotFound)

	require.NoError(t, cb.DeleteVTE(ctx, "two"))
	assert.Equal(t, []vte.ID{"two"}, f.hosts["/b"].deleted)
	require.ErrorIs(t, cb.DeleteVTE(ctx, "two"), vte.ErrNotFound)

	_, err = NewVTEClient(f.conn, "/c").ListVTEs(ctx)
	require.Error(t, err)
	assert.NotErrorIs(t, err, vte.ErrNotFound, "an unknown workspace is no missing terminal")
}

func TestVTEClientWatch(t *testing.T) {
	t.Parallel()
	f := newVTEFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := f.hosts["/a"]
	uri, err := workspaceapi.ParseURI("file:///dev/pts/3")
	require.NoError(t, err)

	it, err := NewVTEClient(f.conn, "/a").WatchVTEs(ctx)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return a.watching() == 1 },
		5*time.Second, 5*time.Millisecond)

	changes := []vte.Change{
		{ID: "one", URI: uri, Title: "make", Attachments: 1},
		{ID: "one", URI: uri, Title: "make", Liveness: vte.LivenessRunning},
		{ID: "one", URI: uri, Title: "zsh", Liveness: vte.LivenessIdle, Complete: true},
		{ID: "one", Deleted: true},
	}
	for _, change := range changes {
		a.tell(change)
	}
	f.hosts["/b"].tell(vte.Change{ID: "other"})
	for _, want := range changes {
		got, ok := it.Next(ctx)
		require.True(t, ok, "stream ended: %v", it.Err())
		assert.Equal(t, want, got)
	}

	require.NoError(t, it.Close())
	_, ok := it.Next(ctx)
	assert.False(t, ok, "a closed watch ends")
	require.NoError(t, it.Err())
}
