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

package syntaxrpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi/syntaxrpc"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"unstable.build/rune/internal/debug"
)

func TestServerClientIntegration(t *testing.T) {
	testURI, err := workspaceapi.ParseURI("file:///tmp/test.go")
	require.NoError(t, err)
	testURI2, err := workspaceapi.ParseURI("file:///tmp/other.go")
	require.NoError(t, err)

	tsuite := []struct {
		name   string
		setup  func(*mockParser)
		action func(t *testing.T, client *syntaxrpc.Client)
	}{
		{
			name: "Search returns results",
			setup: func(m *mockParser) {
				m.searchResults = []syntaxapi.Result{
					{
						File:        testURI,
						Text:        "func main()",
						From:        term.Coordinates{X: 0, Y: 10},
						To:          term.Coordinates{X: 11, Y: 10},
						CaptureName: "function.name",
					},
					{
						File:        testURI2,
						Text:        "func helper()",
						From:        term.Coordinates{X: 0, Y: 5},
						To:          term.Coordinates{X: 13, Y: 5},
						CaptureName: "function.name",
					},
				}
			},
			action: func(t *testing.T, client *syntaxrpc.Client) {
				it, err := client.Search("(function_declaration)", []string{"function.name"})
				require.NoError(t, err)
				results := collectResults(t, it)
				assert.Len(t, results, 2)
				assert.Equal(t, "func main()", results[0].Text)
				assert.Equal(t, "func helper()", results[1].Text)
			},
		},
		{
			name: "Search returns empty results",
			setup: func(m *mockParser) {
				m.searchResults = nil
			},
			action: func(t *testing.T, client *syntaxrpc.Client) {
				it, err := client.Search("(nonexistent)", nil)
				require.NoError(t, err)
				results := collectResults(t, it)
				assert.Empty(t, results)
			},
		},
		{
			name: "Search returns error",
			setup: func(m *mockParser) {
				m.searchErr = errors.New("search failed")
			},
			action: func(t *testing.T, client *syntaxrpc.Client) {
				it, err := client.Search("(function_declaration)", nil)
				require.NoError(t, err)
				defer func() { _ = it.Close() }()
				ctx := context.Background()
				_, ok := it.Next(ctx)
				assert.False(t, ok)
				assert.Error(t, it.Err())
			},
		},
		{
			name: "SearchNode with single node type",
			setup: func(m *mockParser) {
				m.searchNodeResults = []syntaxapi.Result{
					{
						File:        testURI,
						Text:        "func TestFunc()",
						From:        term.Coordinates{X: 0, Y: 20},
						To:          term.Coordinates{X: 15, Y: 20},
						CaptureName: "definition.function",
					},
				}
			},
			action: func(t *testing.T, client *syntaxrpc.Client) {
				it, err := client.SearchNode(syntaxapi.NodeCaptureDefinitionFunc)
				require.NoError(t, err)
				results := collectResults(t, it)
				assert.Len(t, results, 1)
				assert.Equal(t, "func TestFunc()", results[0].Text)
				assert.Equal(t, "definition.function", results[0].CaptureName)
			},
		},
		{
			name: "SearchNode with multiple node types (bitflag)",
			setup: func(m *mockParser) {
				m.searchNodeResults = []syntaxapi.Result{
					{
						File:        testURI,
						Text:        "func TestFunc()",
						From:        term.Coordinates{X: 0, Y: 20},
						To:          term.Coordinates{X: 15, Y: 20},
						CaptureName: "definition.function",
					},
					{
						File:        testURI,
						Text:        "var x int",
						From:        term.Coordinates{X: 0, Y: 1},
						To:          term.Coordinates{X: 9, Y: 1},
						CaptureName: "definition.var",
					},
				}
			},
			action: func(t *testing.T, client *syntaxrpc.Client) {
				nodeTypes := syntaxapi.NodeCaptureDefinitionFunc | syntaxapi.NodeCaptureDefinitionVar
				it, err := client.SearchNode(nodeTypes)
				require.NoError(t, err)
				results := collectResults(t, it)
				assert.Len(t, results, 2)
			},
		},
		{
			name: "Query specific file",
			setup: func(m *mockParser) {
				m.queryResults = []syntaxapi.Result{
					{
						File:        testURI,
						Text:        "type MyStruct struct",
						From:        term.Coordinates{X: 0, Y: 15},
						To:          term.Coordinates{X: 20, Y: 15},
						CaptureName: "type.definition",
					},
				}
			},
			action: func(t *testing.T, client *syntaxrpc.Client) {
				it, err := client.Query(testURI, "(type_declaration)", []string{"type.definition"})
				require.NoError(t, err)
				results := collectResults(t, it)
				assert.Len(t, results, 1)
				assert.Equal(t, "type MyStruct struct", results[0].Text)
			},
		},
		{
			name: "QueryNode specific file",
			setup: func(m *mockParser) {
				m.queryNodeResults = []syntaxapi.Result{
					{
						File:        testURI,
						Text:        "myPackage",
						From:        term.Coordinates{X: 8, Y: 0},
						To:          term.Coordinates{X: 17, Y: 0},
						CaptureName: "definition.namespace",
					},
				}
			},
			action: func(t *testing.T, client *syntaxrpc.Client) {
				it, err := client.QueryNode(testURI, syntaxapi.NodeCaptureDefinitionNamespace)
				require.NoError(t, err)
				results := collectResults(t, it)
				assert.Len(t, results, 1)
				assert.Equal(t, "myPackage", results[0].Text)
			},
		},
		{
			name: "coordinates are preserved",
			setup: func(m *mockParser) {
				m.searchResults = []syntaxapi.Result{
					{
						File:        testURI,
						Text:        "test",
						From:        term.Coordinates{X: 5, Y: 100},
						To:          term.Coordinates{X: 50, Y: 100},
						CaptureName: "test",
					},
				}
			},
			action: func(t *testing.T, client *syntaxrpc.Client) {
				it, err := client.Search("test", nil)
				require.NoError(t, err)
				results := collectResults(t, it)
				require.Len(t, results, 1)
				assert.Equal(t, term.Coordinates{X: 5, Y: 100}, results[0].From)
				assert.Equal(t, term.Coordinates{X: 50, Y: 100}, results[0].To)
			},
		},
		{
			name: "ResolveSymbol streams progress and matches",
			setup: func(m *mockParser) {
				m.resolveMatches = []syntaxapi.Match{
					{
						URI:        testURI.String(),
						Pos:        term.Coordinates{X: 5, Y: 10},
						Display:    "pkg.Sym",
						ImportPath: "example/pkg",
					},
					{URI: testURI2.String(), Pos: term.Coordinates{X: 1, Y: 20}},
				}
			},
			action: func(t *testing.T, client *syntaxrpc.Client) {
				var reports []string
				prog := syntaxapi.ProgressFunc(func(msg string, _ int, _, _ int64) {
					reports = append(reports, msg)
				})
				it, err := client.ResolveSymbol(context.Background(), "pkg.Sym", prog)
				require.NoError(t, err)
				matches, err := iterator.ToSlice(context.Background(), it)
				require.NoError(t, err)
				require.Len(t, matches, 2)
				assert.Equal(t, testURI.String(), matches[0].URI)
				assert.Equal(t, term.Coordinates{X: 5, Y: 10}, matches[0].Pos)
				assert.Equal(t, "example/pkg", matches[0].ImportPath)
				assert.Equal(t, []string{"Searching references…", "Searching definitions…"}, reports)
			},
		},
		{
			name:  "ResolveSymbol propagates ErrNoDot",
			setup: func(m *mockParser) { m.resolveErr = syntaxapi.ErrNoDot },
			action: func(t *testing.T, client *syntaxrpc.Client) {
				it, err := client.ResolveSymbol(context.Background(), "Sym", nil)
				require.NoError(t, err)
				_, err = iterator.ToSlice(context.Background(), it)
				assert.ErrorIs(t, err, syntaxapi.ErrNoDot)
			},
		},
		{
			name: "ListReferencedSymbols streams names",
			setup: func(m *mockParser) {
				m.referencedNames = []string{"pkg.Foo", "pkg.Bar", "other.Baz"}
			},
			action: func(t *testing.T, client *syntaxrpc.Client) {
				it, err := client.ListReferencedSymbols(context.Background())
				require.NoError(t, err)
				names, err := iterator.ToSlice(context.Background(), it)
				require.NoError(t, err)
				require.NoError(t, it.Err())
				assert.Equal(t, []string{"pkg.Foo", "pkg.Bar", "other.Baz"}, names)
			},
		},
		{
			name:  "ListReferencedSymbols streams empty",
			setup: func(m *mockParser) { m.referencedNames = nil },
			action: func(t *testing.T, client *syntaxrpc.Client) {
				it, err := client.ListReferencedSymbols(context.Background())
				require.NoError(t, err)
				names, err := iterator.ToSlice(context.Background(), it)
				require.NoError(t, err)
				assert.Empty(t, names)
			},
		},
		{
			name:  "ListReferencedSymbols propagates server error",
			setup: func(m *mockParser) { m.referencedErr = errors.New("boom") },
			action: func(t *testing.T, client *syntaxrpc.Client) {
				it, err := client.ListReferencedSymbols(context.Background())
				require.NoError(t, err)
				_, err = iterator.ToSlice(context.Background(), it)
				require.Error(t, err)
			},
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			mock := &mockParser{}
			tcase.setup(mock)

			server, client, cleanup := setupServerClient(t, mock)
			defer cleanup()
			_ = server

			tcase.action(t, client)
		})
	}
}

func TestNodeCaptureBitflags(t *testing.T) {
	tsuite := []struct {
		name      string
		nodeTypes syntaxapi.NodeCaptureName
		wantValue uint32
	}{
		{"single scope", syntaxapi.NodeCaptureScope, 1},
		{"single definition ns", syntaxapi.NodeCaptureDefinitionNamespace, 2},
		{"single reference", syntaxapi.NodeCaptureReference, 4},
		{"single definition func", syntaxapi.NodeCaptureDefinitionFunc, 8},
		{"single definition var", syntaxapi.NodeCaptureDefinitionVar, 16},
		{
			"combined func and var",
			syntaxapi.NodeCaptureDefinitionFunc | syntaxapi.NodeCaptureDefinitionVar,
			24,
		},
		{
			"combined all",
			syntaxapi.NodeCaptureScope | syntaxapi.NodeCaptureDefinitionNamespace |
				syntaxapi.NodeCaptureReference | syntaxapi.NodeCaptureDefinitionFunc |
				syntaxapi.NodeCaptureDefinitionVar,
			31,
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			assert.Equal(t, tcase.wantValue, uint32(tcase.nodeTypes))
		})
	}
}

func TestHighlight(t *testing.T) {
	tests := []struct {
		name      string
		uri       string
		content   string
		locations []textapi.Location
	}{
		{
			name:    "single location",
			uri:     "file:///tmp/test.go",
			content: "package main",
			locations: []textapi.Location{
				{
					From:    term.Coordinates{X: 0, Y: 0},
					To:      term.Coordinates{X: 7, Y: 0},
					Attr:    term.Attributes{Fg: term.ColorBlue},
					Message: "keyword",
				},
			},
		},
		{
			name:    "multiple locations",
			uri:     "file:///workspace/main.rs",
			content: "fn main() {}",
			locations: []textapi.Location{
				{
					From:    term.Coordinates{X: 0, Y: 0},
					To:      term.Coordinates{X: 2, Y: 0},
					Attr:    term.Attributes{Fg: term.ColorRed},
					Message: "keyword",
				},
				{
					From:    term.Coordinates{X: 3, Y: 0},
					To:      term.Coordinates{X: 7, Y: 0},
					Attr:    term.Attributes{Fg: term.ColorGreen, Attrs: term.AttrBold},
					Message: "function",
				},
			},
		},
		{
			name:      "empty locations",
			uri:       "file:///tmp/empty.txt",
			content:   "",
			locations: []textapi.Location{},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &mockParser{locations: tt.locations}
			srv := grpc.NewServer()
			syntaxrpc.RegisterSyntaxServer(srv, NewServer(stub))

			// Use a short path to avoid unix socket path length limits.
			tmpDir, err := os.MkdirTemp("", "syn")
			require.NoError(t, err)
			t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
			sockPath := filepath.Join(tmpDir, fmt.Sprintf("%d.sock", i))
			lis, err := net.Listen("unix", sockPath)
			require.NoError(t, err)
			go func() { _ = srv.Serve(lis) }()
			t.Cleanup(srv.Stop)

			conn, err := grpc.NewClient(
				"unix:"+sockPath,
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			)
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })

			ctx := context.Background()
			client := syntaxrpc.NewClient(ctx, conn)

			uri, err := workspaceapi.ParseURI(tt.uri)
			require.NoError(t, err)

			it, err := client.Highlight(uri, tt.content)
			require.NoError(t, err)

			got, err := iterator.ToSlice(ctx, it)
			require.NoError(t, err)

			require.Equal(t, tt.uri, stub.lastURI.String())
			require.Equal(t, tt.content, stub.lastContent)
			require.Equal(t, tt.locations, got)
		})
	}
}

type mockParser struct {
	locations         []textapi.Location
	highlightIter     iterator.Iterator[textapi.Location]
	searchResults     []syntaxapi.Result
	searchErr         error
	searchNodeResults []syntaxapi.Result
	searchNodeErr     error
	queryResults      []syntaxapi.Result
	queryErr          error
	queryNodeResults  []syntaxapi.Result
	queryNodeErr      error
	lastContent       string
	lastURI           workspaceapi.URI
	resolveMatches    []syntaxapi.Match
	resolveErr        error
	lastResolveName   string
	referencedNames   []string
	referencedErr     error
}

func (m *mockParser) Highlight(uri workspaceapi.URI, content string) (
	iterator.Iterator[textapi.Location], error,
) {
	m.lastURI = uri
	m.lastContent = content
	if m.highlightIter != nil {
		return m.highlightIter, nil
	}
	return iterator.FromSlice(m.locations), nil
}

func (m *mockParser) Search(_ string, _ []string, _ ...string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	return iterator.FromSlice(m.searchResults), nil
}

func (m *mockParser) SearchNode(
	_ syntaxapi.NodeCaptureName, _ ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	if m.searchNodeErr != nil {
		return nil, m.searchNodeErr
	}
	return iterator.FromSlice(m.searchNodeResults), nil
}

func (m *mockParser) Query(_ workspaceapi.URI, _ string, _ []string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	return iterator.FromSlice(m.queryResults), nil
}

func (m *mockParser) QueryNode(_ workspaceapi.URI, _ syntaxapi.NodeCaptureName) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	if m.queryNodeErr != nil {
		return nil, m.queryNodeErr
	}
	return iterator.FromSlice(m.queryNodeResults), nil
}

func (m *mockParser) ResolveSymbol(
	_ context.Context, name string, progress syntaxapi.Progress,
) (iterator.Iterator[syntaxapi.Match], error) {
	m.lastResolveName = name
	if m.resolveErr != nil {
		return nil, m.resolveErr
	}
	if progress != nil {
		progress.Report("Searching references…", 0, 0, 4)
		progress.Report("Searching definitions…", len(m.resolveMatches), 2, 4)
	}
	return iterator.FromSlice(m.resolveMatches), nil
}

func (m *mockParser) ListReferencedSymbols(
	_ context.Context,
) (iterator.Iterator[string], error) {
	if m.referencedErr != nil {
		return nil, m.referencedErr
	}
	return iterator.FromSlice(m.referencedNames), nil
}

func setupServerClient(t *testing.T, mock *mockParser) (*Server, *syntaxrpc.Client, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "syntaxrpc-test-*")
	require.NoError(t, err)
	socketPath := filepath.Join(tmpDir, "test.sock")

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	server := NewServer(mock)
	grpcServer := grpc.NewServer()
	syntaxrpc.RegisterSyntaxServer(grpcServer, server)

	go func() {
		_ = grpcServer.Serve(listener)
	}()

	ctx := context.Background()
	conn, err := grpc.NewClient(
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	client := syntaxrpc.NewClient(ctx, conn)

	cleanup := func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = os.RemoveAll(tmpDir)
	}

	return server, client, cleanup
}

func collectResults(t *testing.T, it iterator.Iterator[syntaxapi.Result]) []syntaxapi.Result {
	t.Helper()
	defer func() { _ = it.Close() }()

	var results []syntaxapi.Result
	ctx := context.Background()
	for {
		result, ok := it.Next(ctx)
		if !ok {
			break
		}
		results = append(results, result)
	}
	require.NoError(t, it.Err())
	return results
}

func TestHighlightHonoursClientCancel(t *testing.T) {
	released := make(chan struct{})
	started := make(chan struct{})
	var startOnce sync.Once
	stub := &mockParser{
		highlightIter: iterator.FromFunc(
			func(ctx context.Context) (textapi.Location, bool, error) {
				startOnce.Do(func() { close(started) })
				<-ctx.Done()
				return textapi.Location{}, false, ctx.Err()
			},
			func() error {
				close(released)
				return nil
			},
		),
	}

	srv := grpc.NewServer()
	syntaxrpc.RegisterSyntaxServer(srv, NewServer(stub))

	tmpDir, err := os.MkdirTemp("", "syn")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })
	sockPath := filepath.Join(tmpDir, "cancel.sock")
	lis, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	go debug.CapturePanicReport(func() { _ = srv.Serve(lis) })
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient(
		"unix:"+sockPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	ctx, cancel := context.WithCancel(context.Background())
	client := syntaxrpc.NewClient(ctx, conn)

	uri, err := workspaceapi.ParseURI("file:///tmp/cancel.go")
	require.NoError(t, err)
	it, err := client.Highlight(uri, "package main")
	require.NoError(t, err)
	t.Cleanup(func() { _ = it.Close() })

	// Wait until the server goroutine is actually blocked inside the
	// parser iterator before cancelling; otherwise the cancel can race
	// the stream establishment and never reach the server handler.
	select {
	case <-started:
	case <-time.After(30 * time.Second):
		t.Fatal("server-side Highlight iterator never started")
	}

	cancel()

	select {
	case <-released:
	case <-time.After(30 * time.Second):
		t.Fatal("server-side Highlight iterator never released after client cancel")
	}
}
