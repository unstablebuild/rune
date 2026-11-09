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

package llmrpc

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi/llmrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"unstable.build/rune/internal/debug"
)

func TestServerImageIntegration(t *testing.T) {
	model := llmapi.ModelEntry{Name: "image-model", Provider: "provider", BaseURL: "https://example.test", ContextWindow: 1024}
	request := llmapi.ImageRequest{Prompt: "draw a tree", TransparentBackground: true, Images: [][]byte{{1, 2}, {3, 4}}}
	image := llmapi.Image{PNG: []byte{5, 6}, GenerationID: "generation", RequestID: "request"}
	for _, tc := range []struct {
		name      string
		available bool
		code      codes.Code
	}{
		{name: "available", available: true},
		{name: "unavailable"},
		{name: "provider error", code: codes.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &imageService{
				availability: func(_ context.Context, got llmapi.ModelEntry) (bool, error) {
					assert.Equal(t, model, got)
					return tc.available, status.Error(tc.code, "provider error")
				},
				generate: func(_ context.Context, got llmapi.ModelEntry, req llmapi.ImageRequest) (llmapi.Image, error) {
					assert.Equal(t, model, got)
					assert.Equal(t, request, req)
					return image, status.Error(tc.code, "provider error")
				},
			}
			client := llmrpc.NewClient(context.Background(), imageServerConn(t, NewServer(svc)))
			available, err := client.CanGenerateImages(context.Background(), model)
			assert.Equal(t, tc.code, status.Code(err))
			assert.Equal(t, tc.available, available)
			got, err := client.GenerateImage(context.Background(), model, request)
			assert.Equal(t, tc.code, status.Code(err))
			if tc.code == codes.OK {
				assert.Equal(t, image, got)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		server llmrpc.LLMServer
		code   codes.Code
	}{
		{"unsupported service", NewServer(&mockService{}), codes.Unknown},
		{"older server", &llmrpc.UnimplementedLLMServer{}, codes.Unimplemented},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := llmrpc.NewClient(context.Background(), imageServerConn(t, tc.server))
			available, err := client.CanGenerateImages(context.Background(), model)
			require.NoError(t, err)
			assert.False(t, available)
			_, err = client.GenerateImage(context.Background(), model, request)
			assert.Equal(t, tc.code, status.Code(err))
		})
	}
}

func TestServerImageForwarding(t *testing.T) {
	type contextKey struct{}
	model := llmapi.ModelEntry{Name: "image-model", Provider: "provider"}
	request := llmapi.ImageRequest{Prompt: "draw", Images: [][]byte{{1, 2}}}
	for _, providerErr := range []error{nil, errors.New("provider failure"), llmapi.ErrImageGenerationUnsupported} {
		ctx := context.WithValue(context.Background(), contextKey{}, "context value")
		svc := &imageService{
			availability: func(gotCtx context.Context, got llmapi.ModelEntry) (bool, error) {
				assert.Equal(t, ctx.Value(contextKey{}), gotCtx.Value(contextKey{}))
				assert.Equal(t, model, got)
				return true, providerErr
			},
			generate: func(gotCtx context.Context, got llmapi.ModelEntry, req llmapi.ImageRequest) (llmapi.Image, error) {
				assert.Equal(t, ctx.Value(contextKey{}), gotCtx.Value(contextKey{}))
				assert.Equal(t, model, got)
				assert.Equal(t, request, req)
				return llmapi.Image{PNG: []byte("png")}, providerErr
			},
		}
		server := NewServer(svc)
		available, err := server.CanGenerateImages(ctx, &llmrpc.CanGenerateImagesRequest{Model: llmrpc.ToProtoModelEntry(model)})
		assert.Equal(t, providerErr, err)
		if providerErr == nil {
			require.NotNil(t, available)
			assert.True(t, available.Available)
		}
		image, err := server.GenerateImage(ctx, &llmrpc.GenerateImageRequest{Model: llmrpc.ToProtoModelEntry(model), Request: llmrpc.ToProtoImageRequest(request)})
		assert.Equal(t, providerErr, err)
		if providerErr == nil {
			require.NotNil(t, image)
			assert.Equal(t, []byte("png"), image.Image.Png)
		}
		require.NoError(t, server.Close())
	}
}

func TestServerImageSizeBudget(t *testing.T) {
	for _, delta := range []int{0, 1} {
		t.Run([]string{"request at budget", "request over budget"}[delta], func(t *testing.T) {
			req := &llmrpc.GenerateImageRequest{Request: &llmrpc.ImageRequest{Images: [][]byte{make([]byte, llmrpc.MaxImageMessageSize)}}}
			overhead := proto.Size(req) - llmrpc.MaxImageMessageSize
			req.Request.Images[0] = req.Request.Images[0][:llmrpc.MaxImageMessageSize-overhead+delta]
			var called atomic.Bool
			svc := &imageService{generate: func(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error) {
				called.Store(true)
				return llmapi.Image{}, nil
			}}
			client := llmrpc.NewLLMClient(imageServerConn(t, NewServer(svc)))
			_, err := client.GenerateImage(context.Background(), req, grpc.MaxCallSendMsgSize(MaxRecvMsgSize))
			if delta == 0 {
				require.NoError(t, err)
			} else {
				assert.Equal(t, codes.ResourceExhausted, status.Code(err))
			}
			assert.Equal(t, delta == 0, called.Load())
		})
	}
	t.Run("availability metadata", func(t *testing.T) {
		client := llmrpc.NewLLMClient(imageServerConn(t, NewServer(&imageService{})))
		_, err := client.CanGenerateImages(context.Background(), &llmrpc.CanGenerateImagesRequest{Model: &llmrpc.ModelEntry{Name: strings.Repeat("x", llmrpc.MaxImageMessageSize)}}, grpc.MaxCallSendMsgSize(MaxRecvMsgSize))
		assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	})
	for _, delta := range []int{0, 1} {
		t.Run([]string{"response at budget", "response over budget"}[delta], func(t *testing.T) {
			image := llmapi.Image{PNG: make([]byte, llmrpc.MaxImageMessageSize), GenerationID: "generation", RequestID: "request"}
			overhead := proto.Size(&llmrpc.GenerateImageResponse{Image: llmrpc.ToProtoImage(image)}) - llmrpc.MaxImageMessageSize
			image.PNG = image.PNG[:llmrpc.MaxImageMessageSize-overhead+delta]
			svc := &imageService{generate: func(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error) { return image, nil }}
			client := llmrpc.NewLLMClient(imageServerConn(t, NewServer(svc)))
			resp, err := client.GenerateImage(context.Background(), &llmrpc.GenerateImageRequest{}, grpc.MaxCallRecvMsgSize(MaxRecvMsgSize))
			if delta == 0 {
				require.NoError(t, err)
				assert.Equal(t, image, llmrpc.FromProtoImage(resp.Image))
			} else {
				assert.Equal(t, codes.ResourceExhausted, status.Code(err))
			}
		})
	}
}

type imageService struct {
	llmapi.Service
	availability func(context.Context, llmapi.ModelEntry) (bool, error)
	generate     func(context.Context, llmapi.ModelEntry, llmapi.ImageRequest) (llmapi.Image, error)
}

func (s *imageService) CanGenerateImages(ctx context.Context, model llmapi.ModelEntry) (bool, error) {
	return s.availability(ctx, model)
}

func (s *imageService) GenerateImage(ctx context.Context, model llmapi.ModelEntry, req llmapi.ImageRequest) (llmapi.Image, error) {
	return s.generate(ctx, model, req)
}

func imageServerConn(t *testing.T, server llmrpc.LLMServer) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer(grpc.MaxRecvMsgSize(MaxRecvMsgSize), grpc.MaxSendMsgSize(MaxSendMsgSize))
	llmrpc.RegisterLLMServer(gs, server)
	go debug.CapturePanicReport(func() { _ = gs.Serve(listener) })
	t.Cleanup(func() {
		gs.Stop()
		_ = listener.Close()
		if s, ok := server.(*Server); ok {
			_ = s.Close()
		}
	})
	conn, err := grpc.NewClient("passthrough:///llm", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// fakeCompletionStream replays a fixed sequence of chunks to recvCompletionRequest.
type fakeCompletionStream struct {
	grpc.BidiStreamingServer[llmrpc.CreateCompletionRequestChunk, llmrpc.CreateCompletionResponseChunk]
	chunks []*llmrpc.CreateCompletionRequestChunk
	idx    int
}

func (f *fakeCompletionStream) Recv() (*llmrpc.CreateCompletionRequestChunk, error) {
	if f.idx >= len(f.chunks) {
		return nil, io.EOF
	}
	c := f.chunks[f.idx]
	f.idx++
	return c, nil
}

func headerChunk(h *llmrpc.CompletionHeader) *llmrpc.CreateCompletionRequestChunk {
	return &llmrpc.CreateCompletionRequestChunk{
		Payload: &llmrpc.CreateCompletionRequestChunk_Header{Header: h},
	}
}

func messageChunk(m *llmrpc.Message) *llmrpc.CreateCompletionRequestChunk {
	return &llmrpc.CreateCompletionRequestChunk{
		Payload: &llmrpc.CreateCompletionRequestChunk_Message{Message: m},
	}
}

func TestRecvCompletionRequest(t *testing.T) {
	tests := []struct {
		name     string
		chunks   []*llmrpc.CreateCompletionRequestChunk
		wantMsgs int
		wantCode codes.Code
	}{
		{
			name:     "header only",
			chunks:   []*llmrpc.CreateCompletionRequestChunk{headerChunk(&llmrpc.CompletionHeader{})},
			wantMsgs: 0,
		},
		{
			name: "header then messages preserve order",
			chunks: []*llmrpc.CreateCompletionRequestChunk{
				headerChunk(&llmrpc.CompletionHeader{}),
				messageChunk(&llmrpc.Message{Content: "a"}),
				messageChunk(&llmrpc.Message{Content: "b"}),
			},
			wantMsgs: 2,
		},
		{
			name:     "missing header",
			chunks:   nil,
			wantCode: codes.InvalidArgument,
		},
		{
			name: "message before header",
			chunks: []*llmrpc.CreateCompletionRequestChunk{
				messageChunk(&llmrpc.Message{Content: "a"}),
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "duplicate header",
			chunks: []*llmrpc.CreateCompletionRequestChunk{
				headerChunk(&llmrpc.CompletionHeader{}),
				headerChunk(&llmrpc.CompletionHeader{}),
			},
			wantCode: codes.InvalidArgument,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			header, msgs, err := recvCompletionRequest(&fakeCompletionStream{chunks: tc.chunks})
			if tc.wantCode != codes.OK {
				require.Error(t, err)
				assert.Equal(t, tc.wantCode, status.Code(err))
				return
			}
			require.NoError(t, err)
			require.NotNil(t, header)
			assert.Len(t, msgs, tc.wantMsgs)
			for i := range msgs {
				assert.Equal(t, tc.chunks[i+1].GetMessage().GetContent(), msgs[i].GetContent())
			}
		})
	}
}
