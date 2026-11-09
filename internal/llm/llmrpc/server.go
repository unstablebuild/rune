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

// Package llmrpc implements the host-side gRPC server for llmapi.Service.
package llmrpc

import (
	"context"
	"errors"
	"io"
	"math"

	"github.com/unstablebuild/blue/bluectx"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi/llmrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	// MaxRecvMsgSize caps inbound llmrpc messages at 16 MiB (4× the gRPC
	// default). A large tool result already in the conversation can push a
	// CreateCompletion request past the 4 MiB default and wedge the agent.
	MaxRecvMsgSize = 16 * 1024 * 1024
	// MaxSendMsgSize matches the gRPC default (effectively unbounded).
	MaxSendMsgSize = math.MaxInt32
)

// Server adapts an llmapi.Service to the generated LLMServer interface.
//
// gRPC dispatches each request on grpc-go's goroutine pool and the host
// does not hold the event-loop locker around these I/O-bound calls, so the
// supplied svc must be safe for concurrent use (e.g. llmrouter.Router
// guards its caches with its own mutex).
type Server struct {
	llmrpc.UnimplementedLLMServer
	ctx       context.Context
	cancelCtx func()
	svc       llmapi.Service
}

// NewServer returns a new Server that delegates to svc. svc must be safe
// for concurrent use; see the Server doc comment.
func NewServer(svc llmapi.Service) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{svc: svc, ctx: ctx, cancelCtx: cancel}
}

// RegisterServer registers an llmapi.Service as a gRPC service.
func RegisterServer(registrar grpc.ServiceRegistrar, svc llmapi.Service) {
	llmrpc.RegisterLLMServer(registrar, NewServer(svc))
}

// Close cancels in-flight streams.
func (s *Server) Close() error {
	s.cancelCtx()
	return nil
}

// CreateCompletion satisfies LLMServer.
func (s *Server) CreateCompletion(
	stream grpc.BidiStreamingServer[llmrpc.CreateCompletionRequestChunk, llmrpc.CreateCompletionResponseChunk],
) error {
	header, msgs, err := recvCompletionRequest(stream)
	if err != nil {
		return err
	}
	model, apiReq, err := llmrpc.RequestFromHeaderAndMessages(header, msgs)
	if err != nil {
		return err
	}
	ctx, cancel := bluectx.First(stream.Context(), s.ctx)
	defer cancel()
	it, err := s.svc.CreateCompletion(ctx, model, apiReq)
	if err != nil {
		if cwErr, ok := errors.AsType[*llmapi.ErrContextWindowExceeded](err); ok {
			return llmrpc.ContextWindowExceededStatus(cwErr)
		}
		return err
	}
	defer func() { _ = it.Close() }()
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			if iErr := it.Err(); iErr != nil {
				if cwErr, ok := errors.AsType[*llmapi.ErrContextWindowExceeded](iErr); ok {
					return llmrpc.ContextWindowExceededStatus(cwErr)
				}
				return iErr
			}
			return nil
		}
		rep := llmrpc.CreateCompletionResponseChunk{
			Event: llmrpc.ToProtoEvent(ev),
		}
		err := stream.Send(&rep)
		if err != nil {
			return err
		}
	}
}

// recvCompletionRequest drains the client-streamed CreateCompletion chunks: the
// first frame must carry the header, followed by zero or more message frames in
// order. A missing or duplicate header, or a message before the header, is
// rejected with codes.InvalidArgument.
func recvCompletionRequest(
	stream grpc.BidiStreamingServer[llmrpc.CreateCompletionRequestChunk, llmrpc.CreateCompletionResponseChunk],
) (*llmrpc.CompletionHeader, []*llmrpc.Message, error) {
	var header *llmrpc.CompletionHeader
	var msgs []*llmrpc.Message
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		switch payload := chunk.GetPayload().(type) {
		case *llmrpc.CreateCompletionRequestChunk_Header:
			if header != nil {
				return nil, nil, status.Error(codes.InvalidArgument, "llm: duplicate completion header")
			}
			header = payload.Header
		case *llmrpc.CreateCompletionRequestChunk_Message:
			if header == nil {
				return nil, nil, status.Error(codes.InvalidArgument, "llm: message chunk before header")
			}
			msgs = append(msgs, payload.Message)
		default:
			return nil, nil, status.Error(codes.InvalidArgument, "llm: empty completion chunk")
		}
	}
	if header == nil {
		return nil, nil, status.Error(codes.InvalidArgument, "llm: missing completion header")
	}
	return header, msgs, nil
}

// CountTokens satisfies LLMServer.
func (s *Server) CountTokens(
	_ context.Context, req *llmrpc.CountTokensRequest,
) (*llmrpc.CountTokensResponse, error) {
	msgs := make([]llmapi.Message, 0, len(req.GetMessages()))
	for _, m := range req.GetMessages() {
		apiReq, err := llmrpc.FromProtoRequest(&llmrpc.Request{Messages: []*llmrpc.Message{m}})
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, apiReq.Messages...)
	}
	model := llmrpc.FromProtoModelEntry(req.GetModel())
	count, err := s.svc.CountTokens(model, msgs)
	if err != nil {
		return nil, err
	}
	return &llmrpc.CountTokensResponse{Count: int32(count)}, nil
}

// Models satisfies LLMServer.
func (s *Server) Models(
	_ *llmrpc.ModelsRequest,
	stream grpc.ServerStreamingServer[llmrpc.ModelsResponse],
) error {
	ctx, cancel := bluectx.First(stream.Context(), s.ctx)
	defer cancel()
	it := s.svc.Models()
	defer func() { _ = it.Close() }()
	for {
		entry, ok := it.Next(ctx)
		if !ok {
			return it.Err()
		}
		if err := stream.Send(&llmrpc.ModelsResponse{
			Model: llmrpc.ToProtoModelEntry(entry),
		}); err != nil {
			return err
		}
	}
}

// GetModel satisfies LLMServer.
func (s *Server) GetModel(
	ctx context.Context, req *llmrpc.GetModelRequest,
) (*llmrpc.GetModelResponse, error) {
	model := llmrpc.FromProtoModelEntry(req.GetModel())
	entry, err := s.svc.GetModel(ctx, model)
	if err != nil {
		if errors.Is(err, llmapi.ErrModelNotFound) {
			return &llmrpc.GetModelResponse{Found: false}, nil
		}
		return nil, err
	}
	return &llmrpc.GetModelResponse{
		Model: llmrpc.ToProtoModelEntry(entry),
		Found: true,
	}, nil
}

// CanGenerateImages reports the selected service's capability, subject to the
// same request metadata budget as GenerateImage.
func (s *Server) CanGenerateImages(
	ctx context.Context, req *llmrpc.CanGenerateImagesRequest,
) (*llmrpc.CanGenerateImagesResponse, error) {
	if size := proto.Size(req); size > llmrpc.MaxImageMessageSize {
		return nil, status.Errorf(codes.ResourceExhausted, "llm: image availability request is %d bytes; maximum is %d bytes", size, llmrpc.MaxImageMessageSize)
	}
	ctx, cancel := bluectx.First(ctx, s.ctx)
	defer cancel()
	available, err := s.svc.CanGenerateImages(ctx, llmrpc.FromProtoModelEntry(req.GetModel()))
	if err != nil {
		return nil, err
	}
	return &llmrpc.CanGenerateImagesResponse{Available: available}, nil
}

// GenerateImage caps encoded requests and responses
// at the SDK image budget, including model metadata and provider identifiers.
func (s *Server) GenerateImage(
	ctx context.Context, req *llmrpc.GenerateImageRequest,
) (*llmrpc.GenerateImageResponse, error) {
	if size := proto.Size(req); size > llmrpc.MaxImageMessageSize {
		return nil, status.Errorf(codes.ResourceExhausted, "llm: image generation request is %d bytes; maximum is %d bytes; reduce prompt or reference images", size, llmrpc.MaxImageMessageSize)
	}
	ctx, cancel := bluectx.First(ctx, s.ctx)
	defer cancel()
	image, err := s.svc.GenerateImage(ctx, llmrpc.FromProtoModelEntry(req.GetModel()), llmrpc.FromProtoImageRequest(req.GetRequest()))
	if err != nil {
		return nil, err
	}
	resp := &llmrpc.GenerateImageResponse{Image: llmrpc.ToProtoImage(image)}
	if size := proto.Size(resp); size > llmrpc.MaxImageMessageSize {
		return nil, status.Errorf(codes.ResourceExhausted, "llm: generated image response is %d bytes; maximum is %d bytes", size, llmrpc.MaxImageMessageSize)
	}
	return resp, nil
}
