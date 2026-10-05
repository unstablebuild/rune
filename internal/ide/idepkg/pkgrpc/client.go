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
	"io"
	"sync"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"google.golang.org/grpc"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc/pkgrpcpb"
)

// Client is the PackageManager of the host whose Server it is connected
// to. Every path it returns is a path on that host. It returns
// ErrUnsupported when the host does not serve package management.
type Client struct {
	c  pkgrpcpb.PackageManagerClient
	ui idepkg.UI
}

var _ idepkg.PackageManager = (*Client)(nil)

// NewClient returns a Client over cc that shows in ui the notifications
// the host emits while it installs or switches a package, and asks
// there the questions the host has about its user config.
func NewClient(cc grpc.ClientConnInterface, ui idepkg.UI) *Client {
	return &Client{c: pkgrpcpb.NewPackageManagerClient(cc), ui: ui}
}

// LibDir implements idepkg.PackageManager.
func (c *Client) LibDir(ctx context.Context, pkgID string) (iterator.Iterator[string], error) {
	return openStream(ctx, func(ctx context.Context) (grpc.ServerStreamingClient[pkgrpcpb.Paths], error) {
		return c.c.LibDir(ctx, &pkgrpcpb.PackageRef{Package: pkgID})
	}, (*pkgrpcpb.Paths).GetPaths)
}

// LatestVersion implements idepkg.PackageManager.
func (c *Client) LatestVersion(ctx context.Context, pkgID string) (release.Version, error) {
	v, err := c.c.LatestVersion(ctx, &pkgrpcpb.PackageRef{Package: pkgID})
	if err != nil {
		return "", fromStatus(err)
	}
	return release.Version(v.GetVersion()), nil
}

// DescribePackage implements idepkg.PackageManager.
func (c *Client) DescribePackage(ctx context.Context, pkgID string) (release.Package, error) {
	p, err := c.c.DescribePackage(ctx, &pkgrpcpb.PackageRef{Package: pkgID})
	if err != nil {
		return release.Package{}, fromStatus(err)
	}
	return packageFromProto(p), nil
}

// DescribeRelease implements idepkg.PackageManager.
func (c *Client) DescribeRelease(
	ctx context.Context, pkgID string, version string,
) (release.Bundle, error) {
	b, err := c.c.DescribeRelease(ctx, &pkgrpcpb.PackageVersion{Package: pkgID, Version: version})
	if err != nil {
		return release.Bundle{}, fromStatus(err)
	}
	return bundleFromProto(b), nil
}

// ListPackages implements idepkg.PackageManager.
func (c *Client) ListPackages(
	ctx context.Context, filters map[string]string,
) (iterator.Iterator[release.Package], error) {
	return openStream(ctx, func(ctx context.Context) (grpc.ServerStreamingClient[pkgrpcpb.Package], error) {
		return c.c.ListPackages(ctx, &pkgrpcpb.Filters{Filters: filters})
	}, func(p *pkgrpcpb.Package) []release.Package {
		return []release.Package{packageFromProto(p)}
	})
}

// ListPackageVersions implements idepkg.PackageManager.
func (c *Client) ListPackageVersions(
	ctx context.Context, pkgID string, filters map[string]string,
) (iterator.Iterator[release.Bundle], error) {
	return openStream(ctx, func(ctx context.Context) (grpc.ServerStreamingClient[pkgrpcpb.Bundle], error) {
		return c.c.ListPackageVersions(ctx, &pkgrpcpb.PackageFilters{Package: pkgID, Filters: filters})
	}, func(b *pkgrpcpb.Bundle) []release.Bundle {
		return []release.Bundle{bundleFromProto(b)}
	})
}

// ListInstalledPackages implements idepkg.PackageManager.
func (c *Client) ListInstalledPackages(ctx context.Context) (iterator.Iterator[string], error) {
	return openStream(ctx, func(ctx context.Context) (grpc.ServerStreamingClient[pkgrpcpb.PackageRef], error) {
		return c.c.ListInstalledPackages(ctx, &pkgrpcpb.Empty{})
	}, func(p *pkgrpcpb.PackageRef) []string {
		return []string{p.GetPackage()}
	})
}

// ListInstalledPackageVersions implements idepkg.PackageManager.
func (c *Client) ListInstalledPackageVersions(
	ctx context.Context, pkgID string,
) (iterator.Iterator[release.Version], error) {
	return openStream(ctx, func(ctx context.Context) (grpc.ServerStreamingClient[pkgrpcpb.Version], error) {
		return c.c.ListInstalledPackageVersions(ctx, &pkgrpcpb.PackageRef{Package: pkgID})
	}, func(v *pkgrpcpb.Version) []release.Version {
		return []release.Version{release.Version(v.GetVersion())}
	})
}

// InstallPackageVersion implements idepkg.PackageManager. It returns
// once the package is installed; the host's questions about its user
// config can be answered afterwards.
func (c *Client) InstallPackageVersion(
	ctx context.Context, pkgID string, version release.Version, pw repl.ProgressWriter,
) error {
	if pw == nil {
		pw = repl.NopProgressWriter()
	}
	return c.change(ctx, c.c.Install, pkgID, version, pw)
}

// UsePackageVersion implements idepkg.PackageManager. Like
// InstallPackageVersion, it returns before the host's questions are
// answered.
func (c *Client) UsePackageVersion(
	ctx context.Context, pkgID string, version release.Version,
) error {
	return c.change(ctx, c.c.Use, pkgID, version, repl.NopProgressWriter())
}

// DeletePackage implements idepkg.PackageManager.
func (c *Client) DeletePackage(ctx context.Context, pkgID string) error {
	_, err := c.c.DeletePackage(ctx, &pkgrpcpb.PackageRef{Package: pkgID})
	return fromStatus(err)
}

// DeletePackageVersion implements idepkg.PackageManager.
func (c *Client) DeletePackageVersion(
	ctx context.Context, pkgID string, version release.Version, force bool,
) error {
	_, err := c.c.DeletePackageVersion(ctx, &pkgrpcpb.DeleteVersionRequest{
		Package: pkgID, Version: string(version), Force: force,
	})
	return fromStatus(err)
}

// PackageVersionInUse implements idepkg.PackageManager.
func (c *Client) PackageVersionInUse(
	ctx context.Context, pkgID string,
) (release.Version, bool, error) {
	resp, err := c.c.VersionInUse(ctx, &pkgrpcpb.PackageRef{Package: pkgID})
	if err != nil {
		return "", false, fromStatus(err)
	}
	return release.Version(resp.GetVersion()), resp.GetInUse(), nil
}

type changeClientStream = grpc.BidiStreamingClient[pkgrpcpb.ChangeRequest, pkgrpcpb.ChangeEvent]

// change requests an install or a use on a stream open opens, and
// returns once the host is done with it. The stream outlives the call
// until the user answered every prompt the host sent.
func (c *Client) change(
	ctx context.Context,
	open func(context.Context, ...grpc.CallOption) (changeClientStream, error),
	pkgID string, version release.Version, pw repl.ProgressWriter,
) error {
	streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopCancel := context.AfterFunc(ctx, cancel)
	stream, err := open(streamCtx)
	if err != nil {
		cancel()
		return fromStatus(err)
	}
	s := &answerStream{stream: stream}
	// A failed send ends the stream, and Recv reports why.
	_ = s.send(&pkgrpcpb.ChangeRequest{Request: &pkgrpcpb.ChangeRequest_Package{
		Package: &pkgrpcpb.PackageVersion{Package: pkgID, Version: string(version)},
	}})
	for {
		ev, err := stream.Recv()
		if err != nil {
			cancel()
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case errors.Is(err, io.EOF):
				return errors.New("the host ended the request before it completed")
			}
			return fromStatus(err)
		}
		if !c.handle(s, ev, pw) {
			continue
		}
		stopCancel()
		go debug.CapturePanicReport(func() {
			defer cancel()
			for {
				ev, err := stream.Recv()
				if err != nil {
					return
				}
				c.handle(s, ev, repl.NopProgressWriter())
			}
		})
		return nil
	}
}

// handle shows ev and reports whether it is the host's done.
func (c *Client) handle(s *answerStream, ev *pkgrpcpb.ChangeEvent, pw repl.ProgressWriter) bool {
	switch e := ev.GetEvent().(type) {
	case *pkgrpcpb.ChangeEvent_Progress:
		pw.Progress(e.Progress.GetProgress(), e.Progress.GetTotal(), e.Progress.GetUnits())
	case *pkgrpcpb.ChangeEvent_Notice:
		c.notify(e.Notice)
	case *pkgrpcpb.ChangeEvent_Prompt:
		c.prompt(s, e.Prompt)
	case *pkgrpcpb.ChangeEvent_Done:
		return true
	}
	return false
}

func (c *Client) prompt(s *answerStream, p *pkgrpcpb.Prompt) {
	id := p.GetId()
	c.ui.PromptConfig(promptFromProto(p), func(approved bool) {
		// The answer may be given on the UI's event loop.
		go debug.CapturePanicReport(func() {
			err := s.send(&pkgrpcpb.ChangeRequest{Request: &pkgrpcpb.ChangeRequest_Answer{
				Answer: &pkgrpcpb.Answer{Prompt: id, Approved: approved},
			}})
			if err != nil && approved {
				_, _ = c.ui.Notify(browserapi.LevelError,
					"apply configuration: the connection to the host was lost")
			}
		})
	})
}

func (c *Client) notify(n *pkgrpcpb.Notice) {
	level := browserapi.NotificationLevel(n.GetLevel())
	if n.GetOnce() {
		_, _ = c.ui.NotifyOnce(level, "%s", n.GetMessage())
		return
	}
	_, _ = c.ui.Notify(level, "%s", n.GetMessage())
}

// answerStream serializes the requests sent on a change stream: prompts
// are answered from goroutines of their own.
type answerStream struct {
	mu     sync.Mutex
	stream changeClientStream
}

func (s *answerStream) send(req *pkgrpcpb.ChangeRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stream.Send(req)
}

// openStream opens a server stream and waits for the server to accept
// the request, so a rejected request fails here rather than on the
// first Next.
func openStream[M any, T any](
	ctx context.Context,
	open func(context.Context) (grpc.ServerStreamingClient[M], error),
	values func(*M) []T,
) (iterator.Iterator[T], error) {
	ctx, cancel := context.WithCancel(ctx)
	stream, err := open(ctx)
	if err != nil {
		cancel()
		return nil, fromStatus(err)
	}
	md, err := stream.Header()
	if err == nil && md == nil {
		// The stream ended before the server accepted the request.
		_, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			err = nil
		}
		cancel()
		if err == nil {
			return iterator.Empty[T](), nil
		}
	}
	if err != nil {
		cancel()
		return nil, fromStatus(err)
	}
	return &streamIterator[M, T]{stream: stream, cancel: cancel, values: values}, nil
}

type streamIterator[M any, T any] struct {
	stream grpc.ServerStreamingClient[M]
	cancel context.CancelFunc
	values func(*M) []T
	buf    []T
	done   bool
	err    error
}

func (it *streamIterator[M, T]) Next(ctx context.Context) (T, bool) {
	for len(it.buf) == 0 {
		if it.done {
			var zero T
			return zero, false
		}
		stop := context.AfterFunc(ctx, it.cancel)
		m, err := it.stream.Recv()
		stop()
		if err != nil {
			it.done = true
			it.cancel()
			switch {
			case ctx.Err() != nil:
				it.err = ctx.Err()
			case !errors.Is(err, io.EOF):
				it.err = fromStatus(err)
			}
			continue
		}
		it.buf = it.values(m)
	}
	v := it.buf[0]
	it.buf = it.buf[1:]
	return v, true
}

func (it *streamIterator[M, T]) Err() error { return it.err }

func (it *streamIterator[M, T]) Close() error {
	it.cancel()
	return nil
}
