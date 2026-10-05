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

// Package workspacerune implements the rune:// workspace scheme, which
// opens a workspace on another machine in the user's private mesh. It
// needs no SSH server, no credentials and no port forwarding: the mesh
// already authenticates both ends, so the scheme only has to speak the
// workspace RPC protocol over a mesh connection.
package workspacerune

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"

	multierr "github.com/ernestrc/go-multierror"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/runenet"
	"unstable.build/rune/internal/runenet/runenetpb"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/remotescheme"
)

// Scheme is the URL scheme implemented by this package.
const Scheme = "rune"

// ErrConnectionClosed is reported when a mesh connection to a peer
// drops mid-session.
var ErrConnectionClosed = errors.New("network connection closed unexpectedly")

var _ workspace.RemoteScheme = (*scheme)(nil)
var _ workspace.InstallDataDirProvider = (*scheme)(nil)
var _ workspace.PackageHost = (*scheme)(nil)

// Dialer opens a connection to a peer's workspace server. It is
// satisfied by [runenet.Node].
type Dialer interface {
	Dial(ctx context.Context, peer string) (net.Conn, error)
}

// New returns a scheme constructor for rune:// workspaces served by
// peers reachable through dialer.
//
// It panics if dialer is nil: the scheme is only registered once the
// mesh node exists, so a missing one is a programming error.
func New(dialer Dialer) schemeapi.SchemeFunc {
	if dialer == nil {
		panic("workspacerune.New: dialer is required")
	}
	return func(
		ctx context.Context, _ config.Config, uri workspaceapi.URI,
	) (schemeapi.Scheme, error) {
		return newScheme(ctx, dialer, uri)
	}
}

type scheme struct {
	dialer Dialer
	peer   string
	// path is the remote directory to open, already defaulted to the
	// peer's home when the URI carried none.
	path string

	ctx       context.Context
	cancelCtx func()

	schemeapi.Scheme
}

func newScheme(
	ctx context.Context, dialer Dialer, uri workspaceapi.URI,
) (*scheme, error) {
	peer, path, err := parseWorkspaceURI(uri)
	if err != nil {
		return nil, err
	}
	ret := &scheme{dialer: dialer, peer: peer, path: path}
	// The scheme outlives the constructor's context: the latter may be
	// a per-open context, while reconnects must keep working for as
	// long as the workspace is open.
	ret.ctx, ret.cancelCtx = context.WithCancel(context.Background())
	ret.Scheme = remotescheme.New(ctx, ret.connectScheme, uri, isRetryableConnectError)
	return ret, nil
}

// parseWorkspaceURI splits rune://<peer>[/path]. An absent path opens
// the peer's home directory, which is also what the peer completer
// offers, so `rune://<peer>/` is a complete workspace URI.
func parseWorkspaceURI(uri workspaceapi.URI) (peer, path string, err error) {
	if uri.Scheme() != Scheme {
		return "", "", fmt.Errorf("invalid non-rune scheme %q", uri.Scheme())
	}
	if uri.User() != "" {
		return "", "", errors.New(
			"rune scheme does not take a user: peers authenticate by machine identity")
	}
	peer = uri.Host()
	if peer = strings.TrimSpace(peer); peer == "" {
		return "", "", errors.New("rune scheme with empty peer is invalid")
	}
	path = uri.Path()
	if path == "" || path == "/" {
		path = "~"
	}
	return peer, path, nil
}

// isRetryableConnectError reports whether another dial may succeed. A
// peer that is not in the mesh, or one that refuses us, will not fix
// itself, so the reconnect loop must stop instead of hammering the
// mesh forever.
func isRetryableConnectError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, runenet.ErrPeerNotFound) {
		return false
	}
	switch status.Code(err) {
	case codes.PermissionDenied, codes.Unauthenticated:
		// The peer is owned by another account. Retrying cannot
		// change who we are.
		return false
	}
	return true
}

func (s *scheme) connectScheme(
	ctx context.Context, _ workspaceapi.URI, closeHook func(error),
) (schemeapi.Scheme, error) {
	// Dial before handing the connection to gRPC: gRPC turns dial
	// failures into opaque status errors, and the reconnect loop needs
	// the real one to tell "peer is gone" from "peer is busy".
	peerConn, err := s.dialer.Dial(ctx, s.peer)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", s.peer, err)
	}
	pending := &notifyConn{Conn: peerConn, onClose: closeHook}

	conn, err := grpc.NewClient("passthrough:///"+s.peer,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// The mesh transport can die silently (peer suspended, network
		// switched); without keepalive pings a save would block
		// indefinitely instead of triggering a reconnect.
		grpc.WithKeepaliveParams(remotescheme.ClientKeepalive),
		// One connection per attempt: a re-dial by gRPC would silently
		// resurrect a transport whose peer-side file descriptors the
		// reconnect loop has already invalidated.
		grpc.WithContextDialer(takeConn(pending)))
	if err != nil {
		pending.abandon()
		return nil, fmt.Errorf("connect to %s: %w", s.peer, err)
	}

	client := workspacerpc.NewClient(s.ctx, conn)
	// Chroot is the first round-trip: it resolves the workspace path on
	// the peer (expanding "~" there) and, because grpc.NewClient
	// connects lazily, it is also what proves the peer really serves a
	// workspace before we report a successful connection.
	remote, err := client.Chroot(s.path)
	if err != nil {
		// Tearing down a connection we are abandoning must not also
		// report a drop: the caller learns why this attempt failed
		// from the returned error, and a racing hook would replace it
		// with a generic "connection closed".
		pending.abandon()
		_ = conn.Close()
		return nil, fmt.Errorf("open %s on %s: %w", s.path, s.peer, err)
	}
	return &connScheme{Scheme: workspaceCwd{Scheme: remote}, conn: conn}, nil
}

// workspaceCwd runs commands in the workspace directory. A peer serves
// its whole filesystem rooted at "/" and the workspace is a chroot of
// it, but the protocol carries no root for StartCommand: without a
// directory of its own every terminal would start in the peer's root
// instead of the workspace the user opened.
type workspaceCwd struct {
	schemeapi.Scheme
}

func (w workspaceCwd) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	if cmd.Dir == "" {
		cmd.Dir = w.Root()
	}
	return w.Scheme.StartCommand(ctx, cmd)
}

func (w workspaceCwd) Chroot(path string) (schemeapi.Scheme, error) {
	sub, err := w.Scheme.Chroot(path)
	if err != nil {
		return nil, err
	}
	return workspaceCwd{Scheme: sub}, nil
}

// takeConn hands an already-established connection to gRPC exactly
// once. Later dials fail, which surfaces as an RPC error while the
// reconnect loop, already notified by [notifyConn], establishes a
// fresh connection.
func takeConn(conn *notifyConn) func(context.Context, string) (net.Conn, error) {
	var pending atomic.Pointer[notifyConn]
	pending.Store(conn)
	return func(context.Context, string) (net.Conn, error) {
		if c := pending.Swap(nil); c != nil {
			return c, nil
		}
		return nil, ErrConnectionClosed
	}
}

// URI resolves path on the peer and re-addresses it as a rune:// URI so
// the IDE keeps identifying the workspace by peer rather than by the
// peer's local filesystem layout.
func (s *scheme) URI(path string) (workspaceapi.URI, error) {
	remote, err := s.Scheme.URI(path)
	if err != nil {
		return workspaceapi.URI{}, err
	}
	return workspaceapi.ParseURI(fmt.Sprintf("%s://%s%s",
		Scheme, s.peer, remote.Path()))
}

func (s *scheme) Close() (ret error) {
	if err := s.Scheme.Close(); err != nil {
		ret = multierr.Append(ret, err)
	}
	s.cancelCtx()
	return ret
}

func (s *scheme) OnDisconnect() <-chan struct{} {
	return s.Scheme.(workspace.RemoteScheme).OnDisconnect()
}

func (s *scheme) WaitConnected(ctx context.Context) error {
	return s.Scheme.(workspace.RemoteScheme).WaitConnected(ctx)
}

// HostConn reaches the peer's other services over the workspace
// connection, following its reconnects.
func (s *scheme) HostConn() (grpc.ClientConnInterface, bool) {
	return s.Scheme.(workspace.PackageHost).HostConn()
}

// installRootTimeout bounds the PeerInfo round-trip so resolving the
// install root against a peer that is asleep or gone fails over to the
// caller's fallback instead of stalling extension startup.
const installRootTimeout = 5 * time.Second

// InstallDataDir asks the peer for its Rune data directory over a
// connection of its own: the reconnect loop owns the workspace
// connection, and a one-shot query must not race its lifecycle. A peer
// running an older Rune without the PeerInfo service reports
// errors.ErrUnsupported so the caller degrades to its own guess.
func (s *scheme) InstallDataDir(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, installRootTimeout)
	defer cancel()

	conn, err := dialPeer(s.dialer, s.peer)
	if err != nil {
		return "", fmt.Errorf("connect to %s: %w", s.peer, err)
	}
	defer func() { _ = conn.Close() }()

	info, err := runenetpb.NewPeerInfoClient(conn).
		Get(ctx, &runenetpb.PeerInfoRequest{})
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return "", fmt.Errorf(
				"peer %s does not advertise its install root: %w",
				s.peer, errors.ErrUnsupported)
		}
		return "", fmt.Errorf("peer info from %s: %w", s.peer, err)
	}
	return info.GetDataDir(), nil
}

// dialPeer opens a gRPC connection to peer's workspace server over the
// mesh. The connection is established lazily on the first RPC; the
// caller owns it and must close it.
func dialPeer(dialer Dialer, peer string) (*grpc.ClientConn, error) {
	return grpc.NewClient("passthrough:///"+peer,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return dialer.Dial(ctx, peer)
		}))
}

// connScheme ties the lifetime of the gRPC connection to the scheme
// built on top of it, so a reconnect does not leak the previous
// connection's background goroutines.
type connScheme struct {
	schemeapi.Scheme
	conn *grpc.ClientConn
}

func (c *connScheme) Conn() grpc.ClientConnInterface { return c.conn }

func (c *connScheme) Close() (ret error) {
	if err := c.Scheme.Close(); err != nil {
		ret = multierr.Append(ret, err)
	}
	if err := c.conn.Close(); err != nil {
		ret = multierr.Append(ret, err)
	}
	return ret
}

// notifyConn reports transport death to the reconnect loop. gRPC hides
// connection errors behind per-RPC failures, so the drop is observed
// here, at the only place that sees the mesh connection itself.
type notifyConn struct {
	net.Conn
	// notified is a CAS flag rather than a sync.Once because the hook
	// re-enters this conn: reporting a drop tears the scheme down,
	// which closes this very connection.
	notified atomic.Bool
	onClose  func(error)
}

func (c *notifyConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if err != nil {
		c.notify(err)
	}
	return n, err
}

func (c *notifyConn) Close() error {
	err := c.Conn.Close()
	c.notify(ErrConnectionClosed)
	return err
}

func (c *notifyConn) notify(err error) {
	if !c.notified.CompareAndSwap(false, true) {
		return
	}
	// Off this goroutine: the hook tears the connection down, and
	// gRPC's shutdown waits for the very reader that observed the
	// drop and called us.
	go debug.CapturePanicReport(func() {
		c.onClose(err)
	})
}

// abandon closes the connection without reporting a drop.
func (c *notifyConn) abandon() {
	c.notified.Store(true)
	_ = c.Conn.Close()
}
