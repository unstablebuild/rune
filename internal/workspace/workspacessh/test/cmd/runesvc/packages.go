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

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc/pkgrpcpb"
)

// latestVersion is the only version the stand-in registry publishes.
const latestVersion = "1.0.0"

// installSteps is how many progress events an install reports.
const installSteps = 4

// packages stands in for the package manager `rune -x` serves, which
// cannot be built here: it links tree-sitter, and this binary is built
// without CGO. Installing a package writes an executable named after it
// into the data directory's bin directory, which the host environment
// puts on PATH at startup.
type packages struct {
	pkgrpcpb.UnimplementedPackageManagerServer
	binDir string

	mu        sync.Mutex
	installed map[string]string
}

func servePackages(grpcServer *grpc.Server, binDir string) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	pkgrpcpb.RegisterPackageManagerServer(grpcServer,
		&packages{binDir: binDir, installed: map[string]string{}})
	return nil
}

func (p *packages) LatestVersion(
	context.Context, *pkgrpcpb.PackageRef,
) (*pkgrpcpb.Version, error) {
	return &pkgrpcpb.Version{Version: latestVersion}, nil
}

func (p *packages) LibDir(
	req *pkgrpcpb.PackageRef, stream grpc.ServerStreamingServer[pkgrpcpb.Paths],
) error {
	p.mu.Lock()
	_, ok := p.installed[req.GetPackage()]
	p.mu.Unlock()
	if !ok {
		st, err := status.New(codes.NotFound, "package is not installed").
			WithDetails(&pkgrpcpb.PackageError{Kind: pkgrpcpb.PackageError_NOT_INSTALLED})
		if err != nil {
			return err
		}
		return st.Err()
	}
	if err := stream.SendHeader(metadata.MD{}); err != nil {
		return err
	}
	return stream.Send(&pkgrpcpb.Paths{
		Paths: []string{filepath.Join(p.binDir, req.GetPackage())},
	})
}

func (p *packages) Install(
	stream grpc.BidiStreamingServer[pkgrpcpb.ChangeRequest, pkgrpcpb.ChangeEvent],
) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	req := first.GetPackage()
	id, version := req.GetPackage(), req.GetVersion()
	for i := int64(1); i <= installSteps; i++ {
		if err := stream.Send(&pkgrpcpb.ChangeEvent{
			Event: &pkgrpcpb.ChangeEvent_Progress{Progress: &pkgrpcpb.Progress{
				Progress: i, Total: installSteps, Units: "KiB",
			}},
		}); err != nil {
			return err
		}
	}
	script := fmt.Sprintf("#!/bin/sh\necho \"%s ok %s\"\n", id, version)
	if err := os.WriteFile(filepath.Join(p.binDir, id), []byte(script), 0o755); err != nil {
		return status.Error(codes.Internal, err.Error())
	}
	p.mu.Lock()
	p.installed[id] = version
	p.mu.Unlock()
	// Like a package replacing one of the user's settings, ask the
	// client and apply the change once it is approved.
	if err := stream.Send(&pkgrpcpb.ChangeEvent{
		Event: &pkgrpcpb.ChangeEvent_Prompt{Prompt: &pkgrpcpb.Prompt{
			Id:      1,
			Message: fmt.Sprintf("%s wants to update your configuration.", id),
			Options: []*pkgrpcpb.PromptOption{
				{Label: "Allow", Key: "a"}, {Label: "Deny", Key: "d"},
			},
		}},
	}); err != nil {
		return err
	}
	if err := stream.Send(&pkgrpcpb.ChangeEvent{
		Event: &pkgrpcpb.ChangeEvent_Done{Done: &pkgrpcpb.Empty{}},
	}); err != nil {
		return err
	}
	answer, err := stream.Recv()
	if err != nil || !answer.GetAnswer().GetApproved() {
		return nil
	}
	return stream.Send(&pkgrpcpb.ChangeEvent{
		Event: &pkgrpcpb.ChangeEvent_Notice{Notice: &pkgrpcpb.Notice{
			Level: uint32(browserapi.LevelSuccess),
			Message: fmt.Sprintf("applied %s configuration updates. "+
				"All changes are in effect now: gui.env.", id),
		}},
	})
}
