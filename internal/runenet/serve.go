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

package runenet

import (
	"context"
	"errors"
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/runenet/runenetpb"
	"unstable.build/rune/internal/workspace/remotescheme"
	tworkspacerpc "unstable.build/rune/internal/workspace/workspacerpc"
)

// WhoIs resolves a mesh address to the machine behind it. It is the
// authentication primitive the workspace server authorizes against: the
// address cannot be spoofed because packets only reach the listener
// after the WireGuard handshake succeeded.
type WhoIs func(ctx context.Context, addr string) (Caller, error)

// Caller is the machine behind a mesh address.
type Caller struct {
	// Login is the account the coordination server reports as the
	// machine's owner. For a tagged machine it is a placeholder, not
	// an account.
	Login string
	// Tags are the machine's ACL tags. Empty for a machine owned by
	// an account.
	Tags []string
}

// WorkspaceServer serves this machine's filesystem, processes and
// terminals to authorized mesh peers.
type WorkspaceServer struct {
	grpcServer *grpc.Server
	rpcServer  *tworkspacerpc.Server
}

// ServeWorkspace starts serving scheme to mesh peers on the node's
// workspace port. Only machines owned by the same account as this node
// are allowed to issue requests; see [Node.WhoIs].
//
// dataDir is this instance's Rune data directory, advertised to peers
// through the PeerInfo service so a client can provision and resolve
// toolchains under the peer's actual install root rather than guessing
// it from its own datadir. It must be an absolute path on this host.
func ServeWorkspace(
	node *Node, scheme schemeapi.Scheme, dataDir string,
) (*WorkspaceServer, error) {
	lis, err := node.Listen()
	if err != nil {
		return nil, fmt.Errorf("listen on network: %w", err)
	}
	auth := NewPeerAuthorizer(node.WhoIs, node.LoginName)
	grpcServer := grpc.NewServer(
		grpc.KeepaliveEnforcementPolicy(remotescheme.ServerEnforcement),
		grpc.ChainUnaryInterceptor(auth.UnaryInterceptor()),
		grpc.ChainStreamInterceptor(auth.StreamInterceptor()),
	)
	rpcServer := tworkspacerpc.NewServer(scheme,
		tworkspacerpc.CommandAuthorizerFunc(
			func(context.Context, workspaceapi.Cmd) error { return nil }))
	workspacerpc.RegisterSchemeServer(grpcServer, rpcServer)
	workspacerpc.RegisterFilesServer(grpcServer, rpcServer)
	workspacerpc.RegisterExecutorServer(grpcServer, rpcServer)
	workspacerpc.RegisterTerminalServer(grpcServer, rpcServer)
	runenetpb.RegisterPeerInfoServer(grpcServer, peerInfoServer{dataDir: dataDir})

	go debug.CapturePanicReport(func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.WithField(logging.KeyClass, "runenet").
				Warnf("network workspace server stopped: %v", err)
		}
	})
	return &WorkspaceServer{grpcServer: grpcServer, rpcServer: rpcServer}, nil
}

// peerInfoServer answers PeerInfo requests with this instance's data
// directory. It runs behind the same PeerAuthorizer interceptors as
// the workspace services, so only same-account peers can query it.
type peerInfoServer struct {
	runenetpb.UnimplementedPeerInfoServer
	dataDir string
}

func (s peerInfoServer) Get(
	context.Context, *runenetpb.PeerInfoRequest,
) (*runenetpb.PeerInfoResponse, error) {
	return &runenetpb.PeerInfoResponse{DataDir: s.dataDir}, nil
}

// Close stops serving and drops every in-flight peer request.
func (s *WorkspaceServer) Close() error {
	s.grpcServer.Stop()
	return s.rpcServer.Stop()
}

// WhoIs resolves a mesh address to the machine behind it.
func (n *Node) WhoIs(ctx context.Context, addr string) (Caller, error) {
	lc, err := n.client()
	if err != nil {
		return Caller{}, err
	}
	who, err := lc.WhoIs(ctx, addr)
	if err != nil {
		return Caller{}, fmt.Errorf("whois %s: %w", addr, err)
	}
	if who.UserProfile == nil || who.Node == nil {
		return Caller{}, fmt.Errorf("whois %s: no user profile", addr)
	}
	return Caller{Login: who.UserProfile.LoginName, Tags: who.Node.Tags}, nil
}

// LoginName is the account this node belongs to. It is empty until the
// node has joined the mesh.
func (n *Node) LoginName(ctx context.Context) (string, error) {
	n.mu.Lock()
	cached := n.loginName
	n.mu.Unlock()
	if cached != "" {
		return cached, nil
	}
	st, err := n.status(ctx)
	if err != nil {
		return "", err
	}
	name := selfLoginName(st)
	if name == "" {
		return "", errors.New("network node has not logged in yet")
	}
	n.mu.Lock()
	n.loginName = name
	n.mu.Unlock()
	return name, nil
}

// PeerAuthorizer rejects mesh requests coming from machines owned by a
// different account, and from every tagged machine. Sharing a tailnet
// is not enough: a shared node belongs to someone else and must not
// read this machine's files or run commands on it. A tagged machine is
// a serve-only one, which the mesh policy already forbids from opening
// connections; refusing it here keeps that true if the policy is ever
// misapplied.
type PeerAuthorizer struct {
	whoIs WhoIs
	self  func(context.Context) (string, error)
}

// NewPeerAuthorizer builds an authorizer that only admits callers whose
// owning account matches the one self reports.
func NewPeerAuthorizer(
	whoIs WhoIs, self func(context.Context) (string, error),
) *PeerAuthorizer {
	return &PeerAuthorizer{whoIs: whoIs, self: self}
}

// Authorize reports whether the caller behind ctx may issue requests.
func (a *PeerAuthorizer) Authorize(ctx context.Context) error {
	p, ok := peer.FromContext(ctx)
	if !ok || p.Addr == nil {
		return status.Error(codes.Unauthenticated, "no peer address")
	}
	self, err := a.self(ctx)
	if err != nil {
		return status.Errorf(codes.Unavailable,
			"could not resolve local network identity: %v", err)
	}
	caller, err := a.whoIs(ctx, p.Addr.String())
	if err != nil {
		return status.Errorf(codes.Unauthenticated,
			"could not identify caller %s: %v", p.Addr, err)
	}
	if len(caller.Tags) > 0 {
		log.WithField(logging.KeyClass, "runenet").
			Warnf("rejected network request from %s tagged %v", p.Addr, caller.Tags)
		return status.Errorf(codes.PermissionDenied,
			"caller %s is a serve-only machine and cannot open workspaces", p.Addr)
	}
	if caller.Login != self {
		log.WithField(logging.KeyClass, "runenet").
			Warnf("rejected network request from %s owned by %q", p.Addr, caller.Login)
		return status.Errorf(codes.PermissionDenied,
			"caller %s is not owned by %s", p.Addr, self)
	}
	return nil
}

// UnaryInterceptor authorizes unary requests.
func (a *PeerAuthorizer) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context, req any, _ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		if err := a.Authorize(ctx); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// StreamInterceptor authorizes streaming requests.
func (a *PeerAuthorizer) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		if err := a.Authorize(ss.Context()); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}
