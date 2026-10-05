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

package pkgrpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc/pkgrpcpb"
)

// pathsPerMessage bounds a LibDir message: a toolchain's lib dir holds
// thousands of files.
const pathsPerMessage = 512

// Server serves a PackageManager to remote clients.
type Server struct {
	pkgrpcpb.UnimplementedPackageManagerServer
	pm idepkg.PackageManager
	// mutating admits one install, use, delete or answer to a prompt at
	// a time: concurrent package config merges into the same user
	// config lose keys.
	mutating chan struct{}
}

// NewServer serves pm. Errors pm returns that already are gRPC statuses
// reach the client unchanged. pm installs and uses packages with a
// context that carries the client's idepkg.UI, which it must notify
// and ask instead of any user of its own.
func NewServer(pm idepkg.PackageManager) *Server {
	return &Server{pm: pm, mutating: make(chan struct{}, 1)}
}

// Register serves s on r.
func (s *Server) Register(r grpc.ServiceRegistrar) {
	pkgrpcpb.RegisterPackageManagerServer(r, s)
}

func (s *Server) lockMutation(ctx context.Context) (unlock func(), err error) {
	select {
	case s.mutating <- struct{}{}:
		return func() { <-s.mutating }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) mutate(ctx context.Context, fn func() error) error {
	unlock, err := s.lockMutation(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// accept tells the client the request was accepted; see the service
// doc in pkgrpc.proto.
func accept(stream grpc.ServerStream) error {
	return stream.SendHeader(metadata.MD{})
}

// streamAll sends every element of it, built by open, through send.
func streamAll[T any](
	stream grpc.ServerStream, open func() (iterator.Iterator[T], error),
	send func(T) error,
) error {
	it, err := open()
	if err != nil {
		return toStatus(err)
	}
	defer it.Close()
	if err := accept(stream); err != nil {
		return err
	}
	ctx := stream.Context()
	for {
		v, ok := it.Next(ctx)
		if !ok {
			break
		}
		if err := send(v); err != nil {
			return err
		}
	}
	return toStatus(it.Err())
}

// LibDir implements pkgrpcpb.PackageManagerServer.
func (s *Server) LibDir(req *pkgrpcpb.PackageRef, stream grpc.ServerStreamingServer[pkgrpcpb.Paths]) error {
	ctx := stream.Context()
	it, err := s.pm.LibDir(ctx, req.GetPackage())
	if err != nil {
		return toStatus(err)
	}
	defer it.Close()
	if err := accept(stream); err != nil {
		return err
	}
	batch := make([]string, 0, pathsPerMessage)
	for {
		path, ok := it.Next(ctx)
		if !ok {
			break
		}
		batch = append(batch, path)
		if len(batch) < pathsPerMessage {
			continue
		}
		if err := stream.Send(&pkgrpcpb.Paths{Paths: batch}); err != nil {
			return err
		}
		batch = make([]string, 0, pathsPerMessage)
	}
	if err := it.Err(); err != nil {
		return toStatus(err)
	}
	if len(batch) > 0 {
		return stream.Send(&pkgrpcpb.Paths{Paths: batch})
	}
	return nil
}

// LatestVersion implements pkgrpcpb.PackageManagerServer.
func (s *Server) LatestVersion(ctx context.Context, req *pkgrpcpb.PackageRef) (*pkgrpcpb.Version, error) {
	v, err := s.pm.LatestVersion(ctx, req.GetPackage())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pkgrpcpb.Version{Version: string(v)}, nil
}

// DescribePackage implements pkgrpcpb.PackageManagerServer.
func (s *Server) DescribePackage(ctx context.Context, req *pkgrpcpb.PackageRef) (*pkgrpcpb.Package, error) {
	p, err := s.pm.DescribePackage(ctx, req.GetPackage())
	if err != nil {
		return nil, toStatus(err)
	}
	return packageToProto(p), nil
}

// DescribeRelease implements pkgrpcpb.PackageManagerServer.
func (s *Server) DescribeRelease(ctx context.Context, req *pkgrpcpb.PackageVersion) (*pkgrpcpb.Bundle, error) {
	b, err := s.pm.DescribeRelease(ctx, req.GetPackage(), req.GetVersion())
	if err != nil {
		return nil, toStatus(err)
	}
	return bundleToProto(b), nil
}

// ListPackages implements pkgrpcpb.PackageManagerServer.
func (s *Server) ListPackages(req *pkgrpcpb.Filters, stream grpc.ServerStreamingServer[pkgrpcpb.Package]) error {
	return streamAll(stream, func() (iterator.Iterator[release.Package], error) {
		return s.pm.ListPackages(stream.Context(), req.GetFilters())
	}, func(p release.Package) error {
		return stream.Send(packageToProto(p))
	})
}

// ListPackageVersions implements pkgrpcpb.PackageManagerServer.
func (s *Server) ListPackageVersions(
	req *pkgrpcpb.PackageFilters, stream grpc.ServerStreamingServer[pkgrpcpb.Bundle],
) error {
	return streamAll(stream, func() (iterator.Iterator[release.Bundle], error) {
		return s.pm.ListPackageVersions(stream.Context(), req.GetPackage(), req.GetFilters())
	}, func(b release.Bundle) error {
		return stream.Send(bundleToProto(b))
	})
}

// ListInstalledPackages implements pkgrpcpb.PackageManagerServer.
func (s *Server) ListInstalledPackages(
	_ *pkgrpcpb.Empty, stream grpc.ServerStreamingServer[pkgrpcpb.PackageRef],
) error {
	return streamAll(stream, func() (iterator.Iterator[string], error) {
		return s.pm.ListInstalledPackages(stream.Context())
	}, func(pkgID string) error {
		return stream.Send(&pkgrpcpb.PackageRef{Package: pkgID})
	})
}

// ListInstalledPackageVersions implements pkgrpcpb.PackageManagerServer.
func (s *Server) ListInstalledPackageVersions(
	req *pkgrpcpb.PackageRef, stream grpc.ServerStreamingServer[pkgrpcpb.Version],
) error {
	return streamAll(stream, func() (iterator.Iterator[release.Version], error) {
		return s.pm.ListInstalledPackageVersions(stream.Context(), req.GetPackage())
	}, func(v release.Version) error {
		return stream.Send(&pkgrpcpb.Version{Version: string(v)})
	})
}

// Install implements pkgrpcpb.PackageManagerServer.
func (s *Server) Install(stream changeServerStream) error {
	return s.serveChange(stream, func(
		ctx context.Context, req *pkgrpcpb.PackageVersion, c *changeStream,
	) error {
		return s.pm.InstallPackageVersion(ctx, req.GetPackage(),
			release.Version(req.GetVersion()), c)
	})
}

// Use implements pkgrpcpb.PackageManagerServer.
func (s *Server) Use(stream changeServerStream) error {
	return s.serveChange(stream, func(
		ctx context.Context, req *pkgrpcpb.PackageVersion, _ *changeStream,
	) error {
		return s.pm.UsePackageVersion(ctx, req.GetPackage(), release.Version(req.GetVersion()))
	})
}

type changeServerStream = grpc.BidiStreamingServer[pkgrpcpb.ChangeRequest, pkgrpcpb.ChangeEvent]

// serveChange runs the install or use the client requests on stream,
// relaying to the client what pm notifies and asks meanwhile, then
// applies the client's answers to those prompts until none is left or
// the client goes away.
func (s *Server) serveChange(
	stream changeServerStream,
	run func(context.Context, *pkgrpcpb.PackageVersion, *changeStream) error,
) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	req := first.GetPackage()
	if req == nil {
		return status.Error(codes.InvalidArgument, "the first request must name the package")
	}
	c := &changeStream{stream: stream, prompts: make(map[uint64]func(bool))}
	defer c.close()
	err = s.mutate(ctx, func() error {
		return run(idepkg.WithUI(ctx, c), req, c)
	})
	if err != nil {
		return toStatus(err)
	}
	c.send(&pkgrpcpb.ChangeEvent{Event: &pkgrpcpb.ChangeEvent_Done{Done: &pkgrpcpb.Empty{}}})
	for c.pending() {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		answer, ok := c.take(msg.GetAnswer().GetPrompt())
		if !ok {
			return status.Error(codes.InvalidArgument, "the request answers no pending prompt")
		}
		err = s.mutate(ctx, func() error {
			answer(msg.GetAnswer().GetApproved())
			return nil
		})
		if err != nil {
			return toStatus(err)
		}
	}
	return c.err()
}

// DeletePackage implements pkgrpcpb.PackageManagerServer.
func (s *Server) DeletePackage(ctx context.Context, req *pkgrpcpb.PackageRef) (*pkgrpcpb.Empty, error) {
	unlock, err := s.lockMutation(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	defer unlock()
	if err := s.pm.DeletePackage(ctx, req.GetPackage()); err != nil {
		return nil, toStatus(err)
	}
	return &pkgrpcpb.Empty{}, nil
}

// DeletePackageVersion implements pkgrpcpb.PackageManagerServer.
func (s *Server) DeletePackageVersion(
	ctx context.Context, req *pkgrpcpb.DeleteVersionRequest,
) (*pkgrpcpb.Empty, error) {
	unlock, err := s.lockMutation(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	defer unlock()
	err = s.pm.DeletePackageVersion(ctx, req.GetPackage(),
		release.Version(req.GetVersion()), req.GetForce())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pkgrpcpb.Empty{}, nil
}

// VersionInUse implements pkgrpcpb.PackageManagerServer.
func (s *Server) VersionInUse(ctx context.Context, req *pkgrpcpb.PackageRef) (*pkgrpcpb.InUse, error) {
	v, ok, err := s.pm.PackageVersionInUse(ctx, req.GetPackage())
	if err != nil {
		return nil, toStatus(err)
	}
	return &pkgrpcpb.InUse{Version: string(v), InUse: ok}, nil
}

// changeStream is the progress writer and the idepkg.UI of the install
// or use a client requested. Progress and notices may come from the
// installer's goroutines, including after the install returned, so
// sends are serialized and stop at close.
type changeStream struct {
	mu      sync.Mutex
	stream  changeServerStream
	closed  bool
	sendErr error
	// prompts holds the answer callbacks of the prompts sent and not
	// answered yet, by prompt id.
	prompts    map[uint64]func(bool)
	lastPrompt uint64
}

var _ idepkg.UI = (*changeStream)(nil)

func (c *changeStream) Progress(progress, total int64, units string) {
	c.send(&pkgrpcpb.ChangeEvent{Event: &pkgrpcpb.ChangeEvent_Progress{Progress: &pkgrpcpb.Progress{
		Progress: progress, Total: total, Units: units,
	}}})
}

func (c *changeStream) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	c.notice(&pkgrpcpb.Notice{Level: uint32(level), Message: fmt.Sprintf(msg, args...)})
	return "", nil
}

func (c *changeStream) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	c.notice(&pkgrpcpb.Notice{Level: uint32(level), Message: fmt.Sprintf(msg, args...), Once: true})
	return "", nil
}

// UpdateNotificationProgress is a no-op: install progress reaches the
// client through the install's progress writer.
func (c *changeStream) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

func (c *changeStream) PromptConfig(p idepkg.ConfigPrompt, answer func(bool)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.sendErr != nil {
		return
	}
	c.lastPrompt++
	c.prompts[c.lastPrompt] = answer
	c.sendErr = c.stream.Send(&pkgrpcpb.ChangeEvent{Event: &pkgrpcpb.ChangeEvent_Prompt{
		Prompt: promptToProto(c.lastPrompt, p),
	}})
}

func (c *changeStream) notice(n *pkgrpcpb.Notice) {
	c.send(&pkgrpcpb.ChangeEvent{Event: &pkgrpcpb.ChangeEvent_Notice{Notice: n}})
}

func (c *changeStream) send(ev *pkgrpcpb.ChangeEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.sendErr != nil {
		return
	}
	c.sendErr = c.stream.Send(ev)
}

func (c *changeStream) pending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.prompts) > 0
}

func (c *changeStream) take(prompt uint64) (answer func(bool), ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	answer, ok = c.prompts[prompt]
	delete(c.prompts, prompt)
	return answer, ok
}

func (c *changeStream) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sendErr
}

func (c *changeStream) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
}
