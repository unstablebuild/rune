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

package workspacerune

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	sdkworkspacerpc "github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/runenet/runenetpb"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtewire"
	"unstable.build/rune/internal/workspace"
	tworkspacerpc "unstable.build/rune/internal/workspace/workspacerpc"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestParseWorkspaceURI(t *testing.T) {
	tsuite := []struct {
		name string
		// scheme is Scheme unless set.
		scheme   string
		uri      string
		wantPeer string
		wantPath string
		wantErr  string
	}{
		{
			name:     "peer and absolute path",
			uri:      "rune://laptop/home/ernie/code",
			wantPeer: "laptop",
			wantPath: "/home/ernie/code",
		},
		{
			name:     "peer without path opens the peer home",
			uri:      "rune://laptop",
			wantPeer: "laptop",
			wantPath: "~",
		},
		{
			name:     "trailing slash opens the peer home",
			uri:      "rune://laptop/",
			wantPeer: "laptop",
			wantPath: "~",
		},
		{
			name:     "explicit home path",
			uri:      "rune://workstation/~/code",
			wantPeer: "workstation",
			wantPath: "/~/code",
		},
		{
			name:    "empty peer",
			uri:     "rune:///home/ernie",
			wantErr: "empty peer",
		},
		{
			name:    "user is rejected",
			uri:     "rune://ernie@laptop/code",
			wantErr: "does not take a user",
		},
		{
			name:    "wrong scheme",
			uri:     "ssh://laptop/code",
			wantErr: "invalid non-rune scheme",
		},
		{
			name:     "tailscale",
			scheme:   workspace.TailscaleScheme,
			uri:      "tailscale://laptop/home/ernie/code",
			wantPeer: "laptop",
			wantPath: "/home/ernie/code",
		},
		{
			name:    "rune is not tailscale",
			scheme:  workspace.TailscaleScheme,
			uri:     "rune://laptop/code",
			wantErr: "invalid non-tailscale scheme",
		},
		{
			name:    "tailscale with a user",
			scheme:  workspace.TailscaleScheme,
			uri:     "tailscale://ernie@laptop/code",
			wantErr: "tailscale scheme does not take a user",
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			uri, err := workspaceapi.ParseURI(tcase.uri)
			require.NoError(t, err)

			scheme := tcase.scheme
			if scheme == "" {
				scheme = Scheme
			}
			peer, path, err := parseWorkspaceURI(scheme, uri)
			if tcase.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tcase.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tcase.wantPeer, peer)
			assert.Equal(t, tcase.wantPath, path)
		})
	}
}

func TestIsRetryableConnectError(t *testing.T) {
	assert.False(t, isRetryableConnectError(nil))
	assert.False(t, isRetryableConnectError(
		fmt.Errorf("dial laptop: %w", runenet.ErrPeerNotFound)))
	assert.True(t, isRetryableConnectError(errors.New("connection refused")))

	// A peer that refuses us will refuse the next attempt too, so the
	// reconnect loop must stop. The status arrives wrapped by the
	// connect path, so the check has to see through that.
	for _, code := range []codes.Code{
		codes.PermissionDenied, codes.Unauthenticated,
	} {
		err := fmt.Errorf("open /src on laptop: %w",
			status.Error(code, "caller is not owned by ernie@example.com"))
		assert.False(t, isRetryableConnectError(err), "code %s", code)
	}
	assert.True(t, isRetryableConnectError(
		fmt.Errorf("open /src on laptop: %w",
			status.Error(codes.Unavailable, "peer is asleep"))))
}

func TestConnectFailureReportsItsOwnError(t *testing.T) {
	addr := servePeerWorkspace(t, t.TempDir())
	dialer := dialerFunc(func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	})

	// A path that does not exist on the peer fails the connect. Tearing
	// the connection down must not race a drop report over the real
	// reason, or the reconnect loop's stop/retry decision becomes a
	// coin flip.
	uri, err := workspaceapi.ParseURI("rune://laptop/no/such/directory")
	require.NoError(t, err)
	s, err := New(Scheme, dialer)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	require.NoError(t, s.(workspace.RemoteScheme).WaitConnected(context.Background()))
	_, err = s.Open("anything")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrConnectionClosed,
		"the connect failure must survive its own cleanup: %v", err)
}

// dialerFunc adapts a function to Dialer, with no attachment port.
type dialerFunc func(ctx context.Context, peer string) (net.Conn, error)

func (f dialerFunc) Dial(ctx context.Context, peer string) (net.Conn, error) {
	return f(ctx, peer)
}

func (dialerFunc) DialAttachment(context.Context, string) (net.Conn, error) {
	return nil, errors.ErrUnsupported
}

func TestSchemeConnectsOverDialer(t *testing.T) {
	for _, scheme := range []string{Scheme, workspace.TailscaleScheme} {
		t.Run(scheme, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(
				filepath.Join(dir, "hello.txt"), []byte("hi"), 0o600))

			addr := servePeerWorkspace(t, dir)
			var dialedPeer string
			dialer := dialerFunc(func(ctx context.Context, peer string) (net.Conn, error) {
				dialedPeer = peer
				var d net.Dialer
				return d.DialContext(ctx, "tcp", addr)
			})

			uri, err := workspaceapi.ParseURI(scheme + "://laptop" + dir)
			require.NoError(t, err)
			s, err := New(scheme, dialer)(context.Background(), config.NopConfig(), uri)
			require.NoError(t, err)
			t.Cleanup(func() { _ = s.Close() })

			require.NoError(t, s.(workspace.RemoteScheme).WaitConnected(context.Background()))
			assert.Equal(t, "laptop", dialedPeer)
			assert.Equal(t, dir, s.Root())

			entries, err := s.ReadDir(".")
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, "hello.txt", entries[0].Name())

			// The workspace must keep identifying itself by peer, not
			// by the peer's local filesystem layout.
			fileURI, err := s.URI("hello.txt")
			require.NoError(t, err)
			assert.Equal(t, scheme+"://laptop"+filepath.Join(dir, "hello.txt"),
				fileURI.String())
		})
	}
}

func TestSchemeReportsDisconnect(t *testing.T) {
	addr := servePeerWorkspace(t, t.TempDir())

	var mu = make(chan net.Conn, 1)
	dialer := dialerFunc(func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		c, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			mu <- c
		}
		return c, err
	})

	uri, err := workspaceapi.ParseURI("rune://laptop/")
	require.NoError(t, err)
	s, err := New(Scheme, dialer)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	remote := s.(workspace.RemoteScheme)
	require.NoError(t, remote.WaitConnected(context.Background()))
	disconnected := remote.OnDisconnect()

	require.NoError(t, (<-mu).Close())
	<-disconnected
}

// servePeerWorkspace stands in for a peer running Rune: it serves root
// over the same workspace RPC services runenet.ServeWorkspace registers.
func servePeerWorkspace(t *testing.T, root string) string {
	t.Helper()
	return servePeerScheme(t, peerFileScheme(t, root))
}

func peerFileScheme(t *testing.T, root string) schemeapi.Scheme {
	t.Helper()

	uri, err := workspaceapi.ParseURI("file://" + root)
	require.NoError(t, err)
	fileScheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = fileScheme.Close() })
	return fileScheme
}

// servePeerScheme serves scheme over the workspace RPC services; any
// register hooks add further services (e.g. PeerInfo) to the same
// server, as runenet.ServeWorkspace does.
func servePeerScheme(
	t *testing.T, scheme schemeapi.Scheme, register ...func(*grpc.Server),
) string {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcServer := grpc.NewServer()
	rpcServer := tworkspacerpc.NewServer(scheme,
		tworkspacerpc.CommandAuthorizerFunc(
			func(context.Context, workspaceapi.Cmd) error { return nil }))
	sdkworkspacerpc.RegisterSchemeServer(grpcServer, rpcServer)
	sdkworkspacerpc.RegisterFilesServer(grpcServer, rpcServer)
	sdkworkspacerpc.RegisterExecutorServer(grpcServer, rpcServer)
	sdkworkspacerpc.RegisterTerminalServer(grpcServer, rpcServer)
	for _, r := range register {
		r(grpcServer)
	}
	go debug.CapturePanicReport(func() {
		_ = grpcServer.Serve(lis)
	})
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = rpcServer.Stop()
	})
	return lis.Addr().String()
}

// staticPeerInfo advertises a fixed data directory, standing in for a
// peer that serves the PeerInfo service.
type staticPeerInfo struct {
	runenetpb.UnimplementedPeerInfoServer
	dataDir string
}

func (s staticPeerInfo) Get(
	context.Context, *runenetpb.PeerInfoRequest,
) (*runenetpb.PeerInfoResponse, error) {
	return &runenetpb.PeerInfoResponse{DataDir: s.dataDir}, nil
}

func TestInstallRootReturnsPeerAdvertisedDataDir(t *testing.T) {
	dir := t.TempDir()
	addr := servePeerScheme(t, peerFileScheme(t, dir), func(s *grpc.Server) {
		runenetpb.RegisterPeerInfoServer(s,
			staticPeerInfo{dataDir: "/home/peer/.rune"})
	})
	dialer := dialerFunc(func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	})

	uri, err := workspaceapi.ParseURI("rune://laptop" + dir)
	require.NoError(t, err)
	s, err := New(Scheme, dialer)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	root, err := s.(workspace.InstallDataDirProvider).
		InstallDataDir(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/home/peer/.rune", root)
}

func TestInstallRootRidesTheWorkspaceConnection(t *testing.T) {
	dir := t.TempDir()
	addr := servePeerScheme(t, peerFileScheme(t, dir), func(s *grpc.Server) {
		runenetpb.RegisterPeerInfoServer(s,
			staticPeerInfo{dataDir: "/home/peer/.rune"})
	})
	var dials atomic.Int32
	dialer := dialerFunc(func(ctx context.Context, _ string) (net.Conn, error) {
		dials.Add(1)
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	})

	uri, err := workspaceapi.ParseURI("rune://laptop" + dir)
	require.NoError(t, err)
	s, err := New(Scheme, dialer)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.(workspace.RemoteScheme).WaitConnected(context.Background()))

	root, err := s.(workspace.InstallDataDirProvider).InstallDataDir(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "/home/peer/.rune", root)
	assert.Equal(t, int32(1), dials.Load(), "a connection of its own")
}

func TestInstallRootOnOldPeerReportsUnsupported(t *testing.T) {
	dir := t.TempDir()
	// No PeerInfo service registered: this peer runs an older Rune,
	// and the real gRPC Unimplemented round-trip must degrade to
	// ErrUnsupported so the caller falls back to its own guess.
	addr := servePeerScheme(t, peerFileScheme(t, dir))
	dialer := dialerFunc(func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	})

	uri, err := workspaceapi.ParseURI("rune://laptop" + dir)
	require.NoError(t, err)
	s, err := New(Scheme, dialer)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	_, err = s.(workspace.InstallDataDirProvider).InstallDataDir(context.Background())
	require.ErrorIs(t, err, errors.ErrUnsupported)
}

// recordingExecutor answers StartCommand with the request it received
// instead of running anything, so a test can assert what the peer was
// asked to do without spawning a process.
type recordingExecutor struct {
	schemeapi.Scheme
	dirs chan string
}

func (r *recordingExecutor) StartCommand(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	r.dirs <- cmd.Dir
	return 0, errors.New("recorded, not started")
}

func TestSchemeStartsCommandsInTheWorkspaceDirectory(t *testing.T) {
	dir := t.TempDir()
	peer := &recordingExecutor{
		Scheme: peerFileScheme(t, "/"),
		dirs:   make(chan string, 1),
	}
	addr := servePeerScheme(t, peer)
	dialer := dialerFunc(func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	})

	uri, err := workspaceapi.ParseURI("rune://laptop" + dir)
	require.NoError(t, err)
	s, err := New(Scheme, dialer)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	require.NoError(t, s.(workspace.RemoteScheme).WaitConnected(context.Background()))

	_, err = s.StartCommand(context.Background(), workspaceapi.Cmd{Path: "true"})
	require.Error(t, err)
	// A peer serves its whole filesystem, so a command that names no
	// directory of its own — every terminal Rune opens — would start in
	// the peer's root instead of the workspace.
	assert.Equal(t, dir, <-peer.dirs)
}

// admitAll admits every peer, as a test's loopback has one.
type admitAll struct{}

func (admitAll) Authorize(context.Context) error { return nil }

// peerDialer reaches a peer served by runenet.Serve on the loopback: its
// workspace port and its attachment port. Its connections can be
// broken, as a network loss breaks them.
type peerDialer struct {
	addr, attachAddr string

	mu    sync.Mutex
	conns []net.Conn
}

func (d *peerDialer) Dial(ctx context.Context, _ string) (net.Conn, error) {
	return d.dial(ctx, d.addr)
}

func (d *peerDialer) DialAttachment(ctx context.Context, _ string) (net.Conn, error) {
	return d.dial(ctx, d.attachAddr)
}

func (d *peerDialer) dial(ctx context.Context, addr string) (net.Conn, error) {
	var nd net.Dialer
	conn, err := nd.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.conns = append(d.conns, conn)
	d.mu.Unlock()
	return conn, nil
}

func (d *peerDialer) breakAll() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, c := range d.conns {
		_ = c.Close()
	}
	d.conns = nil
}

// servePeer stands in for a peer running Rune with its terminals: it
// serves scheme as runenet.ServeWorkspace does, on the loopback.
func servePeer(t *testing.T, scheme schemeapi.Scheme) *peerDialer {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	attachLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := runenet.Serve(lis, attachLis, admitAll{}, scheme, "/data")
	t.Cleanup(func() { _ = server.Close() })
	return &peerDialer{addr: lis.Addr().String(), attachAddr: attachLis.Addr().String()}
}

func TestSchemePeer(t *testing.T) {
	peer := workspacetest.NewPtyScheme("/")
	dialer := servePeer(t, peer)
	uri, err := workspaceapi.ParseURI("rune://laptop/home/ernie/code")
	require.NoError(t, err)
	s, err := New(Scheme, dialer)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	p, ok := s.(workspace.PeerScheme).Peer()
	require.True(t, ok, "a peer serves every workspace of the scheme")
	ctx := context.Background()

	require.NoError(t, s.(workspace.RemoteScheme).WaitConnected(ctx))
	root, err := p.Root(ctx)
	require.NoError(t, err)
	assert.Equal(t, "/home/ernie/code", root, "the workspace as the peer resolved it")
	// The peer's terminals are reached over the workspace connection,
	// which follows its reconnects.
	host := tworkspacerpc.NewVTEClient(p.Conn(), root)
	infos, err := host.ListVTEs(ctx)
	require.NoError(t, err)
	assert.Empty(t, infos)

	it, err := host.WatchVTEs(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = it.Close() })

	// An attachment is a stream of its own to the peer, in the
	// workspace the peer knows.
	rw, err := p.DialAttachment(ctx)
	require.NoError(t, err)
	conn := vtewire.NewConn(rw)
	t.Cleanup(func() { _ = conn.Close() })
	hello, err := vtewire.Dial(conn, &vtewire.Hello{Root: root, Width: 20, Height: 4})
	require.NoError(t, err)
	select {
	case <-peer.Chrooted(root).Started():
	case <-time.After(5 * time.Second):
		t.Fatal("no process started in the workspace on the peer")
	}
	id := vte.ID(hello.VTEID)
	infos, err = host.ListVTEs(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, id, infos[0].ID)
	change, ok := it.Next(ctx)
	require.True(t, ok)
	assert.Equal(t, id, change.ID)
	l, err := host.VTELiveness(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, vte.LivenessUnknown, l, "the fake pty host cannot tell")

	// The terminal is the peer's: it survives the workspace connection,
	// and is reached again, by its id, over the next one.
	dialer.breakAll()
	<-s.(workspace.RemoteScheme).OnDisconnect()
	require.Eventually(t, func() bool {
		infos, err := host.ListVTEs(ctx)
		return err == nil && len(infos) == 1 && infos[0].ID == id
	}, 10*time.Second, 20*time.Millisecond, "the terminal is listed over the next connection")
	rw, err = p.DialAttachment(ctx)
	require.NoError(t, err)
	again := vtewire.NewConn(rw)
	t.Cleanup(func() { _ = again.Close() })
	_, err = vtewire.Dial(again, &vtewire.Hello{Root: root, VTEID: string(id)})
	require.NoError(t, err)

	require.NoError(t, host.DeleteVTE(ctx, id))
	assert.ErrorIs(t, host.DeleteVTE(ctx, id), vte.ErrNotFound)
	infos, err = host.ListVTEs(ctx)
	require.NoError(t, err)
	assert.Empty(t, infos)
}

func TestSchemePeerBeforeConnected(t *testing.T) {
	// A peer that never answers cannot say what the workspace's root is
	// on it, and the scheme says so instead of guessing.
	dialer := dialerFunc(func(ctx context.Context, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	uri, err := workspaceapi.ParseURI("rune://laptop/")
	require.NoError(t, err)
	s, err := New(Scheme, dialer)(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	p, _ := s.(workspace.PeerScheme).Peer()
	_, err = p.Root(context.Background())
	assert.ErrorIs(t, err, ErrNotConnected)
}
