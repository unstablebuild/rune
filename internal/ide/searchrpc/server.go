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

// Package searchrpc implements the host-side gRPC server for
// searchapi.Searcher.
package searchrpc

import (
	"context"
	"errors"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi/searchrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// regexpBatch bounds how many lines one RegexpSearch message carries:
// a broad pattern can match hundreds of thousands of lines.
const regexpBatch = 128

type server struct {
	searchrpc.UnimplementedSearcherServer
	s searchapi.Searcher
}

// RegisterServer registers s as the Searcher gRPC service. Errors
// wrapping searchapi.ErrUnsupported, llmapi.ErrModelNotFound or a
// context error reach clients as those errors.
func RegisterServer(registrar grpc.ServiceRegistrar, s searchapi.Searcher) {
	searchrpc.RegisterSearcherServer(registrar, &server{s: s})
}

// RegexpSearch satisfies SearcherServer.
func (s *server) RegexpSearch(
	req *searchrpc.RegexpRequest, stream grpc.ServerStreamingServer[searchrpc.RegexpResponse],
) error {
	ctx := stream.Context()
	it, err := s.s.RegexpSearch(ctx, searchrpc.FromProtoRegexpRequest(req))
	if err != nil {
		return toStatus(err)
	}
	defer func() { _ = it.Close() }()

	batch := make([]*searchrpc.RegexpResult, 0, regexpBatch)
	for {
		r, ok := it.Next(ctx)
		if !ok {
			break
		}
		batch = append(batch, searchrpc.ToProtoRegexpResult(r))
		if len(batch) < regexpBatch {
			continue
		}
		if err := stream.Send(&searchrpc.RegexpResponse{Results: batch}); err != nil {
			return err
		}
		batch = make([]*searchrpc.RegexpResult, 0, regexpBatch)
	}
	if err := it.Err(); err != nil {
		return toStatus(err)
	}
	if len(batch) == 0 {
		return nil
	}
	return stream.Send(&searchrpc.RegexpResponse{Results: batch})
}

// SemanticSearch satisfies SearcherServer.
func (s *server) SemanticSearch(
	req *searchrpc.SemanticRequest, stream grpc.ServerStreamingServer[searchrpc.SemanticResponse],
) error {
	ctx := stream.Context()
	it, st, err := s.s.SemanticSearch(ctx, searchrpc.FromProtoSemanticRequest(req))
	if err != nil {
		return toStatus(err)
	}
	defer func() { _ = it.Close() }()
	// Clients wait for the first response, which carries the status, before
	// their call returns, so it goes out before the search waits for a result.
	if err := stream.Send(&searchrpc.SemanticResponse{
		Status: searchrpc.ToProtoSemanticStatus(st),
	}); err != nil {
		return err
	}
	for {
		r, ok := it.Next(ctx)
		if !ok {
			break
		}
		if err := stream.Send(&searchrpc.SemanticResponse{Result: searchrpc.ToProtoSemanticResult(r)}); err != nil {
			return err
		}
	}
	return toStatus(it.Err())
}

func toStatus(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, searchapi.ErrUnsupported):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, llmapi.ErrModelNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return err
	}
}
