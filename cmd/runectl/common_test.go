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
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi/searchrpc"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi/semanticrpc"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagerpc/docpb"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi/syntaxrpc"
	"github.com/unstablebuild/rune-go-sdk/api/textapi/textrpc"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"github.com/unstablebuild/rune-go-sdk/handler/handlerrpc"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/termrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"unstable.build/rune/internal/debug"
)

// testEnv contains all mock services and test configuration.
type testEnv struct {
	t        *testing.T
	srv      *grpc.Server
	rpcs     *rpcLog
	socket   string
	datadir  string
	notif    *mockNotifications
	wm       *mockWindowManager
	opener   *mockResourceOpener
	scheme   *mockScheme
	editor   *mockEditor
	docStore *mockDocStore
	syntax   *mockSyntax
	executor *mockExecutor
	terminal *mockTerminal
	lsp      *mockLSP
	search   *mockSearch
	cleanup  func()
	// stderr is what the last run wrote to the command's stderr.
	stderr bytes.Buffer
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	datadir := t.TempDir()
	sockPath := filepath.Join(datadir, "rune.sock")

	lis, err := net.Listen("unix", sockPath)
	require.NoError(t, err)

	rpcs := &rpcLog{}
	srv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(rpcs.unary),
		grpc.ChainStreamInterceptor(rpcs.stream),
	)

	notif := &mockNotifications{}
	wm := &mockWindowManager{focusWindowID: 42}
	opener := &mockResourceOpener{}
	scheme := &mockScheme{}
	ed := &mockEditor{cursorX: 10, cursorY: 20}
	ds := newMockDocStore()
	syn := &mockSyntax{}
	exec := &mockExecutor{startPid: 12345}
	term := newMockTerminal()
	lspMock := &mockLSP{}
	searchMock := &mockSearch{regexp: cannedRegexpResults, semantic: cannedSemanticResults}

	browserrpc.RegisterNotificationsServer(srv, notif)
	browserrpc.RegisterWindowManagerServer(srv, wm)
	browserrpc.RegisterResourceOpenerServer(srv, opener)
	workspacerpc.RegisterSchemeServer(srv, scheme)
	textrpc.RegisterEditorServer(srv, ed)
	docpb.RegisterDocumentStoreServer(srv, ds)
	syntaxrpc.RegisterSyntaxServer(srv, syn)
	workspacerpc.RegisterExecutorServer(srv, exec)
	workspacerpc.RegisterTerminalServer(srv, term)
	semanticrpc.RegisterLSPServer(srv, lspMock)
	searchrpc.RegisterSearcherServer(srv, searchMock)

	go debug.CapturePanicReport(func() { _ = srv.Serve(lis) })

	t.Setenv("RUNE_SOCKET", sockPath)
	t.Setenv("RUNE_DATADIR", datadir)
	t.Setenv("RUNE_CERT", "")
	t.Setenv("RUNE_TOKEN", "")

	return &testEnv{
		t: t, srv: srv, rpcs: rpcs, socket: sockPath,
		datadir: datadir, notif: notif, wm: wm,
		opener: opener, scheme: scheme,
		editor: ed, docStore: ds, syntax: syn,
		executor: exec, terminal: term,
		cleanup: func() {
			srv.Stop()
			term.cleanup()
		},
		lsp:    lspMock,
		search: searchMock,
	}
}

func (e *testEnv) run(args ...string) (string, error) {
	e.t.Helper()

	root := newRootCmd()
	root.SetArgs(args)
	e.stderr.Reset()
	root.SetErr(&e.stderr)

	old := os.Stdout
	r, ww, err := os.Pipe()
	require.NoError(e.t, err)
	os.Stdout = ww

	runErr := root.ExecuteContext(context.Background())

	_ = ww.Close()
	os.Stdout = old

	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	_ = r.Close()

	return buf.String(), runErr
}

// rpcLog records the RPCs the test host serves.
type rpcLog struct {
	mu   sync.Mutex
	rpcs []rpc
}

// rpc is one RPC the test host served.
type rpc struct {
	// method is the service and method, as in
	// "LSP/Definition".
	method string
	req    proto.Message
}

func (l *rpcLog) add(fullMethod string, req any) {
	service, method, _ := strings.Cut(strings.TrimPrefix(fullMethod, "/"), "/")
	service = service[strings.LastIndex(service, ".")+1:]
	m, _ := req.(proto.Message)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rpcs = append(l.rpcs, rpc{method: service + "/" + method, req: m})
}

// take returns the RPCs served since the last take.
func (l *rpcLog) take() []rpc {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.rpcs
	l.rpcs = nil
	return out
}

func (l *rpcLog) unary(
	ctx context.Context, req any,
	info *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
) (any, error) {
	l.add(info.FullMethod, req)
	return handler(ctx, req)
}

func (l *rpcLog) stream(
	srv any, ss grpc.ServerStream,
	info *grpc.StreamServerInfo, handler grpc.StreamHandler,
) error {
	return handler(srv, &recordingStream{
		ServerStream: ss, log: l, method: info.FullMethod,
	})
}

// recordingStream records a streaming RPC with its first
// request, which is the only one of a server stream.
type recordingStream struct {
	grpc.ServerStream
	log    *rpcLog
	method string
	once   sync.Once
}

func (s *recordingStream) RecvMsg(m any) error {
	err := s.ServerStream.RecvMsg(m)
	if err == nil {
		s.once.Do(func() { s.log.add(s.method, m) })
	}
	return err
}

// mockNotifications implements browserrpc.NotificationsServer.
type mockNotifications struct {
	browserrpc.UnimplementedNotificationsServer
	lastLevel uint32
	lastMsg   string
}

func (m *mockNotifications) Notify(_ context.Context, req *browserrpc.NotifyRequest) (*browserrpc.NotifyResponse, error) {
	m.lastLevel = req.GetLevel()
	m.lastMsg = req.GetMsg()
	return &browserrpc.NotifyResponse{
		Id: "notif-1",
	}, nil
}

// cannedRegexpResults covers a line with several matches and one with
// none recorded.
var cannedRegexpResults = []searchapi.RegexpResult{
	{Path: "billing/invoice.go", Line: 42, Text: "\treturn invoice(invoice)", Spans: [][2]int{{8, 15}, {16, 23}}},
	{Path: ".github/ci.yml", Line: 3, Text: "runs-on: invoice"},
}

// cannedSemanticResults covers every segment kind SemanticSearch answers
// with.
var cannedSemanticResults = []searchapi.SemanticResult{
	{
		Path: "billing/invoice.go", Kind: "code", Score: 3.14159, Z: 2.5,
		Cosine: 0.61, KeywordRank: 1, Segment: "lines", Loc: 40, LocEnd: 58,
		Focus: 42, Excerpt: "rounds the «invoice» total", Modified: 1700000000,
		Size: 2048, Text: "\treturn roundInvoiceTotal(lines)",
	},
	{
		Path: "geo/route.go", Kind: "code", Score: 0.75, Segment: "lines",
		Loc: 7, LocEnd: 19, KeywordRank: 2, Excerpt: "shortest route",
		Text: "func shortestRoute(a, b Place) []Place {",
	},
	{
		Path: "notebooks/fit.ipynb", Kind: "notebook", Score: 1.5, Z: 1.25,
		Cosine: 0.4, Segment: "cells", Loc: 3, LocEnd: 3, Excerpt: "fit the model",
	},
	{Path: "docs/invoice.md", Kind: "doc", Score: 0.5, Segment: "name"},
}

// mockSearch implements searchrpc.SearcherServer. Each search streams
// its canned results, the semantic ones after status, then ends the
// stream with err.
type mockSearch struct {
	searchrpc.UnimplementedSearcherServer
	regexp   []searchapi.RegexpResult
	semantic []searchapi.SemanticResult
	status   searchapi.SemanticStatus
	err      error

	mu           sync.Mutex
	lastRegexp   *searchapi.RegexpRequest
	lastSemantic *searchapi.SemanticRequest
}

func (m *mockSearch) RegexpSearch(
	req *searchrpc.RegexpRequest, stream grpc.ServerStreamingServer[searchrpc.RegexpResponse],
) error {
	got := searchrpc.FromProtoRegexpRequest(req)
	m.mu.Lock()
	m.lastRegexp = &got
	m.mu.Unlock()
	resp := &searchrpc.RegexpResponse{}
	for _, r := range m.regexp {
		resp.Results = append(resp.Results, searchrpc.ToProtoRegexpResult(r))
	}
	if err := stream.Send(resp); err != nil {
		return err
	}
	return m.err
}

func (m *mockSearch) SemanticSearch(
	req *searchrpc.SemanticRequest, stream grpc.ServerStreamingServer[searchrpc.SemanticResponse],
) error {
	got := searchrpc.FromProtoSemanticRequest(req)
	m.mu.Lock()
	m.lastSemantic = &got
	m.mu.Unlock()
	if err := stream.Send(&searchrpc.SemanticResponse{Status: searchrpc.ToProtoSemanticStatus(m.status)}); err != nil {
		return err
	}
	for _, r := range m.semantic {
		if err := stream.Send(&searchrpc.SemanticResponse{Result: searchrpc.ToProtoSemanticResult(r)}); err != nil {
			return err
		}
	}
	return m.err
}

// regexpSearched returns the request of the last RegexpSearch.
func (m *mockSearch) regexpSearched(t *testing.T) searchapi.RegexpRequest {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotNil(t, m.lastRegexp, "RegexpSearch was not called")
	return *m.lastRegexp
}

// semanticSearched returns the request of the last SemanticSearch.
func (m *mockSearch) semanticSearched(t *testing.T) searchapi.SemanticRequest {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotNil(t, m.lastSemantic, "SemanticSearch was not called")
	return *m.lastSemantic
}

// mockWindowManager implements browserrpc.WindowManagerServer.
type mockWindowManager struct {
	browserrpc.UnimplementedWindowManagerServer
	focusWindowID uint64
	closedID      uint64
	// floatingErr fails Floating after it receives the request.
	floatingErr error
	// promptEvents are streamed by Prompt, which then ends the stream
	// with promptErr.
	promptEvents []*browserrpc.PromptEvent
	promptErr    error
}

func (m *mockWindowManager) Focus(_ context.Context, _ *browserrpc.FocusRequest) (*browserrpc.FocusResponse, error) {
	return &browserrpc.FocusResponse{
		WindowId: m.focusWindowID,
	}, nil
}

func (m *mockWindowManager) CloseWindow(
	_ context.Context,
	req *browserrpc.WindowCloseRequest,
) (*browserrpc.WindowCloseResponse, error) {
	m.closedID = req.GetWindowId()
	return &browserrpc.WindowCloseResponse{}, nil
}

func (m *mockWindowManager) Split(
	stream grpc.BidiStreamingServer[browserrpc.SplitWindowMessage, handlerrpc.ServerMessage],
) error {
	_, err := stream.Recv()
	if err != nil {
		return err
	}
	resp := &handlerrpc.ServerMessage{
		Type: handlerrpc.MessageType_Response,
		Response: &handlerrpc.InstallResourceResponse{
			WindowId: 200,
		},
	}
	return stream.Send(resp)
}

func (m *mockWindowManager) SetContent(
	stream grpc.BidiStreamingServer[browserrpc.WindowSetContentMessage, handlerrpc.ServerMessage],
) error {
	_, err := stream.Recv()
	if err != nil {
		return err
	}
	resp := &handlerrpc.ServerMessage{
		Type: handlerrpc.MessageType_Response,
		Response: &handlerrpc.InstallResourceResponse{
			WindowId: 300,
		},
	}
	return stream.Send(resp)
}

func (m *mockWindowManager) Floating(
	stream grpc.BidiStreamingServer[browserrpc.FloatingWindowMessage, handlerrpc.ServerMessage],
) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if m.floatingErr != nil {
		return m.floatingErr
	}
	return stream.Send(&handlerrpc.ServerMessage{
		Type:     handlerrpc.MessageType_Response,
		Response: &handlerrpc.InstallResourceResponse{WindowId: 400},
	})
}

func (m *mockWindowManager) Prompt(
	_ *browserrpc.PromptRequest, stream grpc.ServerStreamingServer[browserrpc.PromptEvent],
) error {
	for _, ev := range m.promptEvents {
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
	return m.promptErr
}

// mockResourceOpener implements browserrpc.ResourceOpenerServer.
type mockResourceOpener struct {
	browserrpc.UnimplementedResourceOpenerServer
}

func (m *mockResourceOpener) Open(
	_ context.Context,
	req *browserrpc.OpenResourceRequest,
) (*browserrpc.OpenResourceResponse, error) {
	return &browserrpc.OpenResourceResponse{
		Uri: req.GetResource(),
	}, nil
}

// mockScheme implements workspacerpc.SchemeServer.
type mockScheme struct {
	workspacerpc.UnimplementedSchemeServer
}

func (m *mockScheme) URI(_ context.Context, req *workspacerpc.URIRequest) (*workspacerpc.URIResponse, error) {
	uri := fmt.Sprintf("file://%s", req.GetPath())
	return &workspacerpc.URIResponse{Uri: uri}, nil
}

// mockEditor implements textrpc.EditorServer.
type mockEditor struct {
	textrpc.UnimplementedEditorServer
	cursorX         int32
	cursorY         int32
	setCursorCalled bool
	setAttrsCalled  bool
	setLocCalled    bool
	moveNextCalled  bool
	movePrevCalled  bool
}

func (m *mockEditor) Editor(_ context.Context, _ *textrpc.EditorRequest) (*textrpc.EditorResponse, error) {
	return &textrpc.EditorResponse{}, nil
}

func (m *mockEditor) Cursor(_ context.Context, _ *textrpc.CursorRequest) (*textrpc.CursorResponse, error) {
	return &textrpc.CursorResponse{
		Pos: &termrpc.Coordinates{
			X: m.cursorX, Y: m.cursorY,
		},
	}, nil
}

func (m *mockEditor) SetCursor(_ context.Context, req *textrpc.SetCursorRequest) (*textrpc.SetCursorResponse, error) {
	m.setCursorCalled = true
	m.cursorX = req.GetPos().GetX()
	m.cursorY = req.GetPos().GetY()
	return &textrpc.SetCursorResponse{}, nil
}

func (m *mockEditor) EditCell(_ context.Context, req *textrpc.EditCellRequest) (*textrpc.EditCellResponse, error) {
	return &textrpc.EditCellResponse{
		From: &termrpc.Coordinates{
			X: req.GetStart().GetX(),
			Y: req.GetStart().GetY(),
		},
		To: &termrpc.Coordinates{
			X: req.GetEnd().GetX(),
			Y: req.GetEnd().GetY(),
		},
		Old: "old-text",
	}, nil
}

func (m *mockEditor) RawCells(_ context.Context, _ *textrpc.RawCellsRequest) (*textrpc.RawCellsResponse, error) {
	cells := term.StringToCells("hello world")
	return textrpc.NewRawCellsResponse(cells), nil
}

func (m *mockEditor) SetDefaultAttributes(
	_ context.Context,
	_ *textrpc.SetDefaultAttributesRequest,
) (*textrpc.SetDefaultAttributesResponse, error) {
	m.setAttrsCalled = true
	return &textrpc.SetDefaultAttributesResponse{}, nil
}

func (m *mockEditor) SetLocationList(
	_ context.Context,
	_ *textrpc.SetLocationListRequest,
) (*textrpc.SetLocationListResponse, error) {
	m.setLocCalled = true
	return &textrpc.SetLocationListResponse{}, nil
}

func (m *mockEditor) MoveToNextLocation(
	_ context.Context,
	_ *textrpc.MoveToLocationRequest,
) (*textrpc.MoveToLocationResponse, error) {
	m.moveNextCalled = true
	return &textrpc.MoveToLocationResponse{}, nil
}

func (m *mockEditor) MoveToPrevLocation(
	_ context.Context,
	_ *textrpc.MoveToLocationRequest,
) (*textrpc.MoveToLocationResponse, error) {
	m.movePrevCalled = true
	return &textrpc.MoveToLocationResponse{}, nil
}

// mockDocStore implements docpb.DocumentStoreServer.
type mockDocStore struct {
	docpb.UnimplementedDocumentStoreServer
	docs       map[string][]byte
	lastUpdate *docpb.UpdateDocumentRequest
}

func newMockDocStore() *mockDocStore {
	return &mockDocStore{
		docs: make(map[string][]byte),
	}
}

func (m *mockDocStore) Create(stream grpc.ClientStreamingServer[docpb.CreateDocumentRequest, docpb.CreateDocumentResponse]) error {
	var id string
	var data []byte
	first := true
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if first {
			id = req.GetId()
			first = false
		}
		data = append(data, req.GetData()...)
	}
	m.docs[id] = data
	return stream.SendAndClose(&docpb.CreateDocumentResponse{})
}

func (m *mockDocStore) Set(stream grpc.ClientStreamingServer[docpb.SetDocumentRequest, docpb.DocumentResponse]) error {
	var id string
	var data []byte
	first := true
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if first {
			id = req.GetId()
			first = false
		}
		data = append(data, req.GetData()...)
	}
	m.docs[id] = data
	return stream.SendAndClose(&docpb.DocumentResponse{})
}

func (m *mockDocStore) Get(req *docpb.GetDocumentRequest, stream grpc.ServerStreamingServer[docpb.GetDocumentResponse]) error {
	data, ok := m.docs[req.GetId()]
	if !ok {
		return stream.Send(&docpb.GetDocumentResponse{})
	}
	return stream.Send(&docpb.GetDocumentResponse{Data: data})
}

func (m *mockDocStore) Delete(_ context.Context, req *docpb.DeleteDocumentRequest) (*docpb.DocumentResponse, error) {
	delete(m.docs, req.GetId())
	return &docpb.DocumentResponse{}, nil
}

func (m *mockDocStore) Update(stream grpc.ClientStreamingServer[docpb.UpdateDocumentRequest, docpb.UpdateDocumentResponse]) error {
	reassembled := &docpb.UpdateDocumentRequest{}
	first := true
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if first {
			reassembled.Id = req.GetId()
			first = false
		}
		reassembled.Updates = append(reassembled.Updates, req.GetUpdates()...)
		reassembled.Preconditions = append(reassembled.Preconditions, req.GetPreconditions()...)
	}
	m.lastUpdate = reassembled
	return stream.SendAndClose(&docpb.UpdateDocumentResponse{})
}

func (m *mockDocStore) List(
	_ *docpb.ListDocumentRequest,
	stream grpc.ServerStreamingServer[docpb.ListDocumentResponse],
) error {
	for _, data := range m.docs {
		err := stream.Send(
			&docpb.ListDocumentResponse{Data: data},
		)
		if err != nil {
			return err
		}
	}
	return nil
}

// mockTerminal implements workspacerpc.TerminalServer.
type mockTerminal struct {
	workspacerpc.UnimplementedTerminalServer
	masterR *os.File // Read end for master (code reads from here)
	masterW *os.File // Write end for master (test writes here)
	slaveR  *os.File // Read end for slave
	slaveW  *os.File // Write end for slave
}

func newMockTerminal() *mockTerminal {
	// Create pipes for PTY simulation
	masterR, masterW, _ := os.Pipe()
	slaveR, slaveW, _ := os.Pipe()
	return &mockTerminal{
		masterR: masterR,
		masterW: masterW,
		slaveR:  slaveR,
		slaveW:  slaveW,
	}
}

func (m *mockTerminal) NewPty(_ context.Context, _ *workspacerpc.NewPtyRequest) (*workspacerpc.NewPtyResponse, error) {
	return &workspacerpc.NewPtyResponse{
		Master:   m.masterR.Name(),
		MasterFd: uint32(m.masterR.Fd()),
		Slave:    m.slaveW.Name(),
		SlaveFd:  uint32(m.slaveW.Fd()),
	}, nil
}

func (m *mockTerminal) cleanup() {
	_ = m.masterR.Close()
	_ = m.masterW.Close()
	_ = m.slaveR.Close()
	_ = m.slaveW.Close()
}

// mockExecutor implements workspacerpc.ExecutorServer.
type mockExecutor struct {
	workspacerpc.UnimplementedExecutorServer
	lastPid      int64
	lastSignal   int32
	signalCalled bool
	startPid     int64
	lastDir      string
	lastArgs     []string
	stdoutData   string
	stderrData   string
	exitError    string
	paths        map[string]string
	lastLookPath *workspacerpc.LookPathRequest
}

func (m *mockExecutor) StartCommand(
	stream grpc.BidiStreamingServer[workspacerpc.CommandPayload, workspacerpc.CommandPayload],
) error {
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	if msg.GetType() != workspacerpc.CommandPayload_TypeStart {
		return fmt.Errorf("expected TypeStart, got %v", msg.GetType())
	}
	m.lastDir = msg.GetStart().GetDir()
	m.lastArgs = msg.GetStart().GetArgs()

	resp := &workspacerpc.CommandPayload{
		Type:    workspacerpc.CommandPayload_TypeStarted,
		Started: &workspacerpc.CommandPayload_Started{Pid: m.startPid},
	}
	if err := stream.Send(resp); err != nil {
		return err
	}

	// Stream stdout if configured
	if m.stdoutData != "" {
		stdoutResp := &workspacerpc.CommandPayload{
			Type: workspacerpc.CommandPayload_TypeIO,
			Io: &workspacerpc.CommandPayload_IO{
				Type: workspacerpc.CommandPayload_IO_TypeStdout,
				Data: []byte(m.stdoutData),
			},
		}
		if err := stream.Send(stdoutResp); err != nil {
			return err
		}
	}

	// Stream stderr if configured
	if m.stderrData != "" {
		stderrResp := &workspacerpc.CommandPayload{
			Type: workspacerpc.CommandPayload_TypeIO,
			Io: &workspacerpc.CommandPayload_IO{
				Type: workspacerpc.CommandPayload_IO_TypeStderr,
				Data: []byte(m.stderrData),
			},
		}
		if err := stream.Send(stderrResp); err != nil {
			return err
		}
	}

	// Send done message
	doneResp := &workspacerpc.CommandPayload{
		Type: workspacerpc.CommandPayload_TypeDone,
		Done: &workspacerpc.CommandPayload_Done{ExitError: m.exitError},
	}
	return stream.Send(doneResp)
}

func (m *mockExecutor) Signal(_ context.Context, req *workspacerpc.SignalRequest) (*workspacerpc.SignalResponse, error) {
	m.signalCalled = true
	m.lastPid = req.GetPid()
	m.lastSignal = req.GetSig()
	return &workspacerpc.SignalResponse{}, nil
}

func (m *mockExecutor) LookPath(_ context.Context, req *workspacerpc.LookPathRequest) (*workspacerpc.LookPathResponse, error) {
	m.lastLookPath = req
	path, ok := m.paths[req.GetName()]
	if !ok {
		return &workspacerpc.LookPathResponse{IsNotExistErr: true}, nil
	}
	return &workspacerpc.LookPathResponse{Path: path}, nil
}

// mockSyntax implements syntaxrpc.SyntaxServer.
type mockSyntax struct {
	syntaxrpc.UnimplementedSyntaxServer
	// mu guards the last* fields, which concurrent MCP tool calls write at once.
	mu            sync.Mutex
	lastCaptures  []string
	lastNodeTypes uint32
	lastQueryURI  string
	lastQuery     string
	lastResolve   string
}

func (m *mockSyntax) Search(
	req *syntaxrpc.SearchRequest,
	stream grpc.ServerStreamingServer[syntaxrpc.SearchResponse],
) error {
	m.mu.Lock()
	m.lastCaptures = req.GetCaptureNames()
	m.mu.Unlock()
	return stream.Send(&syntaxrpc.SearchResponse{
		Uri:         "file:///src/main.go",
		Text:        "func main()",
		From:        &termrpc.Coordinates{X: 0, Y: 5},
		To:          &termrpc.Coordinates{X: 12, Y: 5},
		CaptureName: "fn",
	})
}

func (m *mockSyntax) SearchNode(
	req *syntaxrpc.SearchNodeRequest,
	stream grpc.ServerStreamingServer[syntaxrpc.SearchResponse],
) error {
	m.mu.Lock()
	m.lastNodeTypes = req.GetNodeTypes()
	m.mu.Unlock()
	return stream.Send(&syntaxrpc.SearchResponse{
		Uri:         "file:///src/main.go",
		Text:        "MyFunc",
		From:        &termrpc.Coordinates{X: 5, Y: 10},
		To:          &termrpc.Coordinates{X: 11, Y: 10},
		CaptureName: "definition.func",
	})
}

func (m *mockSyntax) Query(
	req *syntaxrpc.QueryRequest,
	stream grpc.ServerStreamingServer[syntaxrpc.SearchResponse],
) error {
	m.mu.Lock()
	m.lastQueryURI = req.GetUri()
	m.lastQuery = req.GetQuery()
	m.lastCaptures = req.GetCaptureNames()
	m.mu.Unlock()
	return stream.Send(&syntaxrpc.SearchResponse{
		Uri:         req.GetUri(),
		Text:        "package main",
		From:        &termrpc.Coordinates{X: 0, Y: 0},
		To:          &termrpc.Coordinates{X: 12, Y: 0},
		CaptureName: "pkg",
	})
}

func (m *mockSyntax) QueryNode(
	req *syntaxrpc.QueryNodeRequest,
	stream grpc.ServerStreamingServer[syntaxrpc.SearchResponse],
) error {
	m.mu.Lock()
	m.lastQueryURI = req.GetUri()
	m.lastNodeTypes = req.GetNodeTypes()
	m.mu.Unlock()
	return stream.Send(&syntaxrpc.SearchResponse{
		Uri:         req.GetUri(),
		Text:        "main",
		From:        &termrpc.Coordinates{X: 8, Y: 0},
		To:          &termrpc.Coordinates{X: 12, Y: 0},
		CaptureName: "definition.namespace",
	})
}

// ResolveSymbol resolves pkg.Symbol to main.go 10:5. Like Rune, it refuses a name without
// a dot, and has no match for any other.
func (m *mockSyntax) ResolveSymbol(
	req *syntaxrpc.ResolveSymbolRequest,
	stream grpc.ServerStreamingServer[syntaxrpc.ResolveSymbolResponse],
) error {
	m.mu.Lock()
	m.lastResolve = req.GetName()
	m.mu.Unlock()
	if !strings.Contains(req.GetName(), ".") {
		return status.Error(codes.InvalidArgument, "syntaxapi.ErrNoDot")
	}
	if err := stream.Send(&syntaxrpc.ResolveSymbolResponse{
		Payload: &syntaxrpc.ResolveSymbolResponse_Progress{
			Progress: &syntaxrpc.ResolveSymbolProgress{
				Message: "searching",
				Found:   0,
				Step:    1,
				Total:   2,
			},
		},
	}); err != nil {
		return err
	}
	if req.GetName() != "pkg.Symbol" {
		return nil
	}
	return stream.Send(&syntaxrpc.ResolveSymbolResponse{
		Payload: &syntaxrpc.ResolveSymbolResponse_Match{
			Match: &syntaxrpc.ResolveSymbolMatch{
				Uri:        "file:///src/main.go",
				Line:       10,
				Character:  5,
				Display:    "pkg.Symbol",
				ImportPath: "example.com/pkg",
			},
		},
	})
}

func (m *mockSyntax) ListReferencedSymbols(
	_ *syntaxrpc.ListReferencedSymbolsRequest,
	stream grpc.ServerStreamingServer[syntaxrpc.ListReferencedSymbolsResponse],
) error {
	for _, name := range []string{"pkg.Symbol", "other.Thing"} {
		if err := stream.Send(&syntaxrpc.ListReferencedSymbolsResponse{
			Name: name,
		}); err != nil {
			return err
		}
	}
	return nil
}

// mockLSP implements semanticrpc.LSPServer.
type mockLSP struct {
	semanticrpc.UnimplementedLSPServer
}

func (m *mockLSP) Hover(_ context.Context, req *semanticrpc.HoverRequest) (*semanticrpc.HoverResponse, error) {
	return &semanticrpc.HoverResponse{
		HasResult: true,
		Result: &semanticrpc.Hover{
			Contents: &semanticrpc.MarkupContent{
				Kind:  1,
				Value: "func main()",
			},
		},
	}, nil
}

func (m *mockLSP) Definition(_ context.Context, req *semanticrpc.DefinitionRequest) (*semanticrpc.DefinitionResponse, error) {
	return &semanticrpc.DefinitionResponse{
		Locations: []*semanticrpc.Location{{
			Uri: "file:///src/main.go",
			Range: &semanticrpc.Range{
				Start: &semanticrpc.Position{
					Line: 10, Character: 5,
				},
				End: &semanticrpc.Position{
					Line: 10, Character: 15,
				},
			},
		}},
	}, nil
}

func (m *mockLSP) References(_ context.Context, req *semanticrpc.ReferencesRequest) (*semanticrpc.ReferencesResponse, error) {
	return &semanticrpc.ReferencesResponse{
		Locations: []*semanticrpc.Location{
			{
				Uri: "file:///src/main.go",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 10, Character: 5,
					},
					End: &semanticrpc.Position{
						Line: 10, Character: 15,
					},
				},
			},
			{
				Uri: "file:///src/util.go",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 20, Character: 3,
					},
					End: &semanticrpc.Position{
						Line: 20, Character: 13,
					},
				},
			},
		},
	}, nil
}

func (m *mockLSP) DocumentSymbol(
	_ context.Context,
	req *semanticrpc.DocumentSymbolRequest,
) (*semanticrpc.DocumentSymbolResponse, error) {
	return &semanticrpc.DocumentSymbolResponse{
		Symbols: []*semanticrpc.DocumentSymbol{{
			Name: "main",
			Kind: 12,
			Range: &semanticrpc.Range{
				Start: &semanticrpc.Position{
					Line: 5, Character: 0,
				},
				End: &semanticrpc.Position{
					Line: 10, Character: 1,
				},
			},
			SelectionRange: &semanticrpc.Range{
				Start: &semanticrpc.Position{
					Line: 5, Character: 5,
				},
				End: &semanticrpc.Position{
					Line: 5, Character: 9,
				},
			},
			Children: []*semanticrpc.DocumentSymbol{{
				Name: "x",
				Kind: 13,
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 6, Character: 1,
					},
					End: &semanticrpc.Position{
						Line: 6, Character: 10,
					},
				},
				SelectionRange: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 6, Character: 1,
					},
					End: &semanticrpc.Position{
						Line: 6, Character: 2,
					},
				},
			}},
		}},
	}, nil
}

func (m *mockLSP) WorkspaceSymbol(
	_ context.Context,
	req *semanticrpc.WorkspaceSymbolRequest,
) (*semanticrpc.WorkspaceSymbolResponse, error) {
	allSymbols := []*semanticrpc.SymbolInformation{
		{
			Name: "MyFunc",
			Kind: 12, // Function
			Location: &semanticrpc.Location{
				Uri: "file:///src/main.go",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 5, Character: 0,
					},
					End: &semanticrpc.Position{
						Line: 5, Character: 10,
					},
				},
			},
		},
		{
			Name: "MyInterface",
			Kind: 11, // Interface
			Location: &semanticrpc.Location{
				Uri: "file:///src/types.go",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 15, Character: 5,
					},
					End: &semanticrpc.Position{
						Line: 18, Character: 1,
					},
				},
			},
		},
		{
			Name: "MyStruct",
			Kind: 23, // Struct
			Location: &semanticrpc.Location{
				Uri: "file:///src/types.go",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 20, Character: 5,
					},
					End: &semanticrpc.Position{
						Line: 25, Character: 1,
					},
				},
			},
		},
		{
			Name: "myVar",
			Kind: 13, // Variable
			Location: &semanticrpc.Location{
				Uri: "file:///src/main.go",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 3, Character: 4,
					},
					End: &semanticrpc.Position{
						Line: 3, Character: 9,
					},
				},
			},
		},
		{
			Name: "MyStruct.MyMethod",
			Kind: 6, // Method
			Location: &semanticrpc.Location{
				Uri: "file:///src/types.go",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 30, Character: 0,
					},
					End: &semanticrpc.Position{
						Line: 35, Character: 1,
					},
				},
			},
		},
	}
	query := req.GetQuery()
	var filtered []*semanticrpc.SymbolInformation
	for _, s := range allSymbols {
		if strings.Contains(s.Name, query) {
			filtered = append(filtered, s)
		}
	}
	return &semanticrpc.WorkspaceSymbolResponse{
		Symbols: filtered,
	}, nil
}

func (m *mockLSP) Diagnostic(_ context.Context, req *semanticrpc.DiagnosticRequest) (*semanticrpc.DiagnosticResponse, error) {
	return &semanticrpc.DiagnosticResponse{
		Report: &semanticrpc.DocumentDiagnosticReport{
			Kind: "full",
			Items: []*semanticrpc.Diagnostic{{
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 3, Character: 10,
					},
					End: &semanticrpc.Position{
						Line: 3, Character: 15,
					},
				},
				Severity: 1,
				Code:     "E001",
				Source:   "test",
				Message:  "undefined variable",
			}},
		},
	}, nil
}

func (m *mockLSP) WorkspaceDiagnostic(
	_ context.Context,
	_ *semanticrpc.WorkspaceDiagnosticRequest,
) (*semanticrpc.WorkspaceDiagnosticResponse, error) {
	return &semanticrpc.WorkspaceDiagnosticResponse{
		Items: []*semanticrpc.WorkspaceDocumentDiagnosticReport{
			{
				Kind:     "full",
				ResultId: "r1",
				Uri:      "file:///src/main.go",
				Version:  1,
				Items: []*semanticrpc.Diagnostic{{
					Range: &semanticrpc.Range{
						Start: &semanticrpc.Position{
							Line: 3, Character: 10,
						},
						End: &semanticrpc.Position{
							Line: 3, Character: 15,
						},
					},
					Severity: 1,
					Code:     "E001",
					Source:   "test",
					Message:  "undefined variable",
				}},
			},
			{
				Kind:    "full",
				Uri:     "file:///src/util.go",
				Version: 2,
				Items: []*semanticrpc.Diagnostic{{
					Range: &semanticrpc.Range{
						Start: &semanticrpc.Position{
							Line: 7, Character: 0,
						},
						End: &semanticrpc.Position{
							Line: 7, Character: 5,
						},
					},
					Severity: 2,
					Code:     "W002",
					Source:   "test",
					Message:  "unused import",
				}},
			},
		},
	}, nil
}

func (m *mockLSP) Rename(_ context.Context, req *semanticrpc.RenameRequest) (*semanticrpc.RenameResponse, error) {
	return &semanticrpc.RenameResponse{
		HasResult: true,
		Result: &semanticrpc.WorkspaceEdit{
			Changes: map[string]*semanticrpc.TextEditList{
				"file:///src/main.go": {
					Edits: []*semanticrpc.TextEdit{
						{
							Range: &semanticrpc.Range{
								Start: &semanticrpc.Position{
									Line: 5, Character: 5,
								},
								End: &semanticrpc.Position{
									Line: 5, Character: 9,
								},
							},
							NewText: req.GetNewName(),
						},
						{
							Range: &semanticrpc.Range{
								Start: &semanticrpc.Position{
									Line: 20, Character: 3,
								},
								End: &semanticrpc.Position{
									Line: 20, Character: 7,
								},
							},
							NewText: req.GetNewName(),
						},
					},
				},
			},
		},
	}, nil
}

func (m *mockLSP) CodeAction(_ context.Context, req *semanticrpc.CodeActionRequest) (*semanticrpc.CodeActionResponse, error) {
	return &semanticrpc.CodeActionResponse{
		Items: []*semanticrpc.CodeActionResultItem{
			{
				CodeAction: &semanticrpc.CodeAction{
					Title: "Extract variable",
					Kind:  "refactor.extract",
				},
			},
			{
				CodeAction: &semanticrpc.CodeAction{
					Title: "Organize imports",
					Kind:  "source.organizeImports",
					Edit: &semanticrpc.WorkspaceEdit{
						Changes: map[string]*semanticrpc.TextEditList{
							"file:///src/main.go": {
								Edits: []*semanticrpc.TextEdit{
									{
										Range: &semanticrpc.Range{
											Start: &semanticrpc.Position{
												Line: 2, Character: 0,
											},
											End: &semanticrpc.Position{
												Line: 4, Character: 0,
											},
										},
										NewText: "import (\n\t\"fmt\"\n)\n",
									},
								},
							},
						},
					},
				},
			},
			{
				Command: &semanticrpc.Command{
					Title:     "Run test",
					Command:   "test.run",
					Arguments: []string{`{"uri":"file:///test.go"}`},
				},
			},
		},
	}, nil
}

func (m *mockLSP) Completion(_ context.Context, req *semanticrpc.CompletionRequest) (*semanticrpc.CompletionResponse, error) {
	return &semanticrpc.CompletionResponse{
		Result: &semanticrpc.CompletionResult{
			Items: []*semanticrpc.CompletionItem{
				{
					Label:  "fmt",
					Kind:   9, // Module
					Detail: "package fmt",
				},
				{
					Label:  "Println",
					Kind:   3, // Function
					Detail: "func Println(a ...any)",
				},
			},
		},
	}, nil
}

func (m *mockLSP) SignatureHelp(
	_ context.Context,
	req *semanticrpc.SignatureHelpRequest,
) (*semanticrpc.SignatureHelpResponse, error) {
	return &semanticrpc.SignatureHelpResponse{
		HasResult: true,
		Result: &semanticrpc.SignatureHelp{
			Signatures: []*semanticrpc.SignatureInformation{
				{
					Label: "func Println(a ...any) (n int, err error)",
					Parameters: []*semanticrpc.ParameterInformation{
						{Label: "a ...any"},
					},
				},
			},
			ActiveSignature: 0,
			ActiveParameter: 0,
		},
	}, nil
}

func (m *mockLSP) Declaration(_ context.Context, req *semanticrpc.DeclarationRequest) (*semanticrpc.DeclarationResponse, error) {
	return &semanticrpc.DeclarationResponse{
		Locations: []*semanticrpc.Location{{
			Uri: "file:///src/types.go",
			Range: &semanticrpc.Range{
				Start: &semanticrpc.Position{
					Line: 15, Character: 5,
				},
				End: &semanticrpc.Position{
					Line: 15, Character: 15,
				},
			},
		}},
	}, nil
}

func (m *mockLSP) TypeDefinition(
	_ context.Context,
	req *semanticrpc.TypeDefinitionRequest,
) (*semanticrpc.TypeDefinitionResponse, error) {
	return &semanticrpc.TypeDefinitionResponse{
		Locations: []*semanticrpc.Location{{
			Uri: "file:///src/types.go",
			Range: &semanticrpc.Range{
				Start: &semanticrpc.Position{
					Line: 20, Character: 5,
				},
				End: &semanticrpc.Position{
					Line: 20, Character: 12,
				},
			},
		}},
	}, nil
}

func (m *mockLSP) Implementation(
	_ context.Context,
	req *semanticrpc.ImplementationRequest,
) (*semanticrpc.ImplementationResponse, error) {
	return &semanticrpc.ImplementationResponse{
		Locations: []*semanticrpc.Location{
			{
				Uri: "file:///src/impl1.go",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 10, Character: 0,
					},
					End: &semanticrpc.Position{
						Line: 10, Character: 10,
					},
				},
			},
			{
				Uri: "file:///src/impl2.go",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 25, Character: 0,
					},
					End: &semanticrpc.Position{
						Line: 25, Character: 10,
					},
				},
			},
		},
	}, nil
}

func (m *mockLSP) Formatting(_ context.Context, req *semanticrpc.FormattingRequest) (*semanticrpc.FormattingResponse, error) {
	return &semanticrpc.FormattingResponse{
		Edits: []*semanticrpc.TextEdit{
			{
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 0, Character: 0,
					},
					End: &semanticrpc.Position{
						Line: 0, Character: 5,
					},
				},
				NewText: "package",
			},
			{
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{
						Line: 5, Character: 0,
					},
					End: &semanticrpc.Position{
						Line: 5, Character: 2,
					},
				},
				NewText: "\t",
			},
		},
	}, nil
}

func (m *mockLSP) PrepareRename(
	_ context.Context,
	req *semanticrpc.PrepareRenameRequest,
) (*semanticrpc.PrepareRenameResponse, error) {
	return &semanticrpc.PrepareRenameResponse{
		HasResult: true,
		Result: &semanticrpc.PrepareRenameResult{
			Range: &semanticrpc.Range{
				Start: &semanticrpc.Position{
					Line: 5, Character: 5,
				},
				End: &semanticrpc.Position{
					Line: 5, Character: 9,
				},
			},
			Placeholder: "main",
		},
	}, nil
}

func (m *mockLSP) DocumentHighlight(
	_ context.Context,
	req *semanticrpc.DocumentHighlightRequest,
) (*semanticrpc.DocumentHighlightResponse, error) {
	return &semanticrpc.DocumentHighlightResponse{
		Highlights: []*semanticrpc.DocumentHighlight{
			{
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 5, Character: 5},
					End:   &semanticrpc.Position{Line: 5, Character: 9},
				},
				Kind: 2, // Read
			},
			{
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 10, Character: 3},
					End:   &semanticrpc.Position{Line: 10, Character: 7},
				},
				Kind: 3, // Write
			},
		},
	}, nil
}

func (m *mockLSP) CodeLens(_ context.Context, req *semanticrpc.CodeLensRequest) (*semanticrpc.CodeLensResponse, error) {
	return &semanticrpc.CodeLensResponse{
		Lenses: []*semanticrpc.CodeLens{
			{
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 5, Character: 0},
					End:   &semanticrpc.Position{Line: 5, Character: 10},
				},
				Command: &semanticrpc.Command{
					Title:   "Run test",
					Command: "test.run",
				},
			},
		},
	}, nil
}

func (m *mockLSP) RangeFormatting(
	_ context.Context,
	req *semanticrpc.RangeFormattingRequest,
) (*semanticrpc.RangeFormattingResponse, error) {
	return &semanticrpc.RangeFormattingResponse{
		Edits: []*semanticrpc.TextEdit{
			{
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 0, Character: 0},
					End:   &semanticrpc.Position{Line: 0, Character: 5},
				},
				NewText: "fixed",
			},
		},
	}, nil
}

func (m *mockLSP) FoldingRange(
	_ context.Context,
	req *semanticrpc.FoldingRangeRequest,
) (*semanticrpc.FoldingRangeResponse, error) {
	return &semanticrpc.FoldingRangeResponse{
		Ranges: []*semanticrpc.FoldingRange{
			{
				StartLine:      5,
				StartCharacter: 0,
				EndLine:        20,
				EndCharacter:   1,
				Kind:           "region",
			},
			{
				StartLine:      1,
				StartCharacter: 0,
				EndLine:        3,
				EndCharacter:   0,
				Kind:           "imports",
			},
		},
	}, nil
}

func (m *mockLSP) SelectionRange(
	_ context.Context,
	req *semanticrpc.SelectionRangeRequest,
) (*semanticrpc.SelectionRangeResponse, error) {
	return &semanticrpc.SelectionRangeResponse{
		Ranges: []*semanticrpc.SelectionRange{
			{
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 5, Character: 5},
					End:   &semanticrpc.Position{Line: 5, Character: 9},
				},
				Parent: &semanticrpc.SelectionRange{
					Range: &semanticrpc.Range{
						Start: &semanticrpc.Position{Line: 5, Character: 0},
						End:   &semanticrpc.Position{Line: 10, Character: 1},
					},
				},
			},
		},
	}, nil
}

func (m *mockLSP) ExecuteCommand(
	_ context.Context,
	req *semanticrpc.ExecuteCommandRequest,
) (*semanticrpc.ExecuteCommandResponse, error) {
	result := "command executed: " + req.GetCommand()
	if args := req.GetArguments(); len(args) > 0 {
		result += " args="
		for i, a := range args {
			if i > 0 {
				result += ","
			}
			result += a
		}
	}
	return &semanticrpc.ExecuteCommandResponse{Result: result}, nil
}

func (m *mockLSP) InlayHint(_ context.Context, req *semanticrpc.InlayHintRequest) (*semanticrpc.InlayHintResponse, error) {
	return &semanticrpc.InlayHintResponse{
		Hints: []*semanticrpc.InlayHint{
			{
				Position: &semanticrpc.Position{Line: 10, Character: 15},
				Label:    ": string",
				Kind:     1, // Type
			},
			{
				Position: &semanticrpc.Position{Line: 12, Character: 8},
				Label:    "name:",
				Kind:     2, // Parameter
			},
		},
	}, nil
}

func (m *mockLSP) PrepareCallHierarchy(
	_ context.Context,
	req *semanticrpc.PrepareCallHierarchyRequest,
) (*semanticrpc.PrepareCallHierarchyResponse, error) {
	return &semanticrpc.PrepareCallHierarchyResponse{
		Items: []*semanticrpc.CallHierarchyItem{
			{
				Name: "main",
				Kind: 12, // Function
				Uri:  "file:///src/main.go",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 5, Character: 0},
					End:   &semanticrpc.Position{Line: 10, Character: 1},
				},
				SelectionRange: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 5, Character: 5},
					End:   &semanticrpc.Position{Line: 5, Character: 9},
				},
			},
		},
	}, nil
}

func (m *mockLSP) CallHierarchyIncomingCalls(
	_ context.Context,
	req *semanticrpc.CallHierarchyIncomingCallsRequest,
) (*semanticrpc.CallHierarchyIncomingCallsResponse, error) {
	return &semanticrpc.CallHierarchyIncomingCallsResponse{
		Calls: []*semanticrpc.CallHierarchyIncomingCall{
			{
				From: &semanticrpc.CallHierarchyItem{
					Name: "runApp",
					Kind: 12,
					Uri:  "file:///src/app.go",
					Range: &semanticrpc.Range{
						Start: &semanticrpc.Position{Line: 20, Character: 0},
						End:   &semanticrpc.Position{Line: 30, Character: 1},
					},
					SelectionRange: &semanticrpc.Range{
						Start: &semanticrpc.Position{Line: 20, Character: 5},
						End:   &semanticrpc.Position{Line: 20, Character: 11},
					},
				},
				FromRanges: []*semanticrpc.Range{
					{
						Start: &semanticrpc.Position{Line: 25, Character: 2},
						End:   &semanticrpc.Position{Line: 25, Character: 8},
					},
				},
			},
		},
	}, nil
}

func (m *mockLSP) CallHierarchyOutgoingCalls(
	_ context.Context,
	req *semanticrpc.CallHierarchyOutgoingCallsRequest,
) (*semanticrpc.CallHierarchyOutgoingCallsResponse, error) {
	return &semanticrpc.CallHierarchyOutgoingCallsResponse{
		Calls: []*semanticrpc.CallHierarchyOutgoingCall{
			{
				To: &semanticrpc.CallHierarchyItem{
					Name: "fmt.Println",
					Kind: 12,
					Uri:  "file:///go/src/fmt/print.go",
					Range: &semanticrpc.Range{
						Start: &semanticrpc.Position{Line: 100, Character: 0},
						End:   &semanticrpc.Position{Line: 110, Character: 1},
					},
					SelectionRange: &semanticrpc.Range{
						Start: &semanticrpc.Position{Line: 100, Character: 5},
						End:   &semanticrpc.Position{Line: 100, Character: 12},
					},
				},
				FromRanges: []*semanticrpc.Range{
					{
						Start: &semanticrpc.Position{Line: 7, Character: 2},
						End:   &semanticrpc.Position{Line: 7, Character: 13},
					},
				},
			},
		},
	}, nil
}

func (m *mockLSP) PrepareTypeHierarchy(
	_ context.Context,
	req *semanticrpc.PrepareTypeHierarchyRequest,
) (*semanticrpc.PrepareTypeHierarchyResponse, error) {
	return &semanticrpc.PrepareTypeHierarchyResponse{
		Items: []*semanticrpc.TypeHierarchyItem{
			{
				Name:   "Reader",
				Kind:   11, // Interface
				Uri:    "file:///src/io.go",
				Detail: "interface",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 15, Character: 0},
					End:   &semanticrpc.Position{Line: 18, Character: 1},
				},
				SelectionRange: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 15, Character: 5},
					End:   &semanticrpc.Position{Line: 15, Character: 11},
				},
			},
		},
	}, nil
}

func (m *mockLSP) TypeHierarchySupertypes(
	_ context.Context,
	req *semanticrpc.TypeHierarchySupertypesRequest,
) (*semanticrpc.TypeHierarchySupertypesResponse, error) {
	return &semanticrpc.TypeHierarchySupertypesResponse{
		Items: []*semanticrpc.TypeHierarchyItem{
			{
				Name: "io.Reader",
				Kind: 11, // Interface
				Uri:  "file:///go/src/io/io.go",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 50, Character: 0},
					End:   &semanticrpc.Position{Line: 53, Character: 1},
				},
				SelectionRange: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 50, Character: 5},
					End:   &semanticrpc.Position{Line: 50, Character: 11},
				},
			},
		},
	}, nil
}

func (m *mockLSP) TypeHierarchySubtypes(
	_ context.Context,
	req *semanticrpc.TypeHierarchySubtypesRequest,
) (*semanticrpc.TypeHierarchySubtypesResponse, error) {
	return &semanticrpc.TypeHierarchySubtypesResponse{
		Items: []*semanticrpc.TypeHierarchyItem{
			{
				Name:   "MyReader",
				Kind:   23, // Struct
				Uri:    "file:///src/reader.go",
				Detail: "struct",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 10, Character: 0},
					End:   &semanticrpc.Position{Line: 15, Character: 1},
				},
				SelectionRange: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 10, Character: 5},
					End:   &semanticrpc.Position{Line: 10, Character: 13},
				},
			},
			{
				Name:   "BufferedReader",
				Kind:   23, // Struct
				Uri:    "file:///src/buffered.go",
				Detail: "struct",
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 8, Character: 0},
					End:   &semanticrpc.Position{Line: 12, Character: 1},
				},
				SelectionRange: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 8, Character: 5},
					End:   &semanticrpc.Position{Line: 8, Character: 19},
				},
			},
		},
	}, nil
}

func (m *mockLSP) SemanticTokensFull(
	_ context.Context,
	req *semanticrpc.SemanticTokensFullRequest,
) (*semanticrpc.SemanticTokensFullResponse, error) {
	return &semanticrpc.SemanticTokensFullResponse{
		HasResult: true,
		Result: &semanticrpc.SemanticTokens{
			ResultId: "test-result-id",
			Data:     []uint32{0, 0, 5, 0, 0, 1, 0, 10, 1, 0},
		},
	}, nil
}

func (m *mockLSP) SemanticTokensRange(
	_ context.Context,
	req *semanticrpc.SemanticTokensRangeRequest,
) (*semanticrpc.SemanticTokensRangeResponse, error) {
	return &semanticrpc.SemanticTokensRangeResponse{
		HasResult: true,
		Result: &semanticrpc.SemanticTokens{
			ResultId: "range-result-id",
			Data:     []uint32{0, 5, 8, 2, 0},
		},
	}, nil
}

func (m *mockLSP) DocumentColor(
	_ context.Context,
	req *semanticrpc.DocumentColorRequest,
) (*semanticrpc.DocumentColorResponse, error) {
	return &semanticrpc.DocumentColorResponse{
		Colors: []*semanticrpc.ColorInformation{
			{
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 5, Character: 10},
					End:   &semanticrpc.Position{Line: 5, Character: 17},
				},
				Color: &semanticrpc.Color{
					Red: 1.0, Green: 0.0, Blue: 0.0, Alpha: 1.0,
				},
			},
		},
	}, nil
}

func (m *mockLSP) ColorPresentation(
	_ context.Context,
	req *semanticrpc.ColorPresentationRequest,
) (*semanticrpc.ColorPresentationResponse, error) {
	return &semanticrpc.ColorPresentationResponse{
		Presentations: []*semanticrpc.ColorPresentation{
			{Label: "#FF0000"},
			{Label: "rgb(255, 0, 0)"},
		},
	}, nil
}

func (m *mockLSP) DocumentLink(
	_ context.Context,
	req *semanticrpc.DocumentLinkRequest,
) (*semanticrpc.DocumentLinkResponse, error) {
	return &semanticrpc.DocumentLinkResponse{
		Links: []*semanticrpc.DocumentLink{
			{
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 3, Character: 5},
					End:   &semanticrpc.Position{Line: 3, Character: 30},
				},
				Target:  "https://example.com/docs",
				Tooltip: "Documentation link",
			},
		},
	}, nil
}

func (m *mockLSP) OnTypeFormatting(
	_ context.Context,
	req *semanticrpc.OnTypeFormattingRequest,
) (*semanticrpc.OnTypeFormattingResponse, error) {
	return &semanticrpc.OnTypeFormattingResponse{
		Edits: []*semanticrpc.TextEdit{
			{
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 5, Character: 0},
					End:   &semanticrpc.Position{Line: 5, Character: 2},
				},
				NewText: "\t",
			},
		},
	}, nil
}

func (m *mockLSP) LinkedEditingRange(
	_ context.Context,
	req *semanticrpc.LinkedEditingRangeRequest,
) (*semanticrpc.LinkedEditingRangeResponse, error) {
	return &semanticrpc.LinkedEditingRangeResponse{
		HasResult: true,
		Result: &semanticrpc.LinkedEditingRanges{
			Ranges: []*semanticrpc.Range{
				{
					Start: &semanticrpc.Position{Line: 1, Character: 5},
					End:   &semanticrpc.Position{Line: 1, Character: 10},
				},
				{
					Start: &semanticrpc.Position{Line: 5, Character: 8},
					End:   &semanticrpc.Position{Line: 5, Character: 13},
				},
			},
			WordPattern: "\\w+",
		},
	}, nil
}

func (m *mockLSP) Moniker(_ context.Context, req *semanticrpc.MonikerRequest) (*semanticrpc.MonikerResponse, error) {
	return &semanticrpc.MonikerResponse{
		Monikers: []*semanticrpc.Moniker{
			{
				Scheme:     "go",
				Identifier: "github.com/example/pkg.Func",
				Unique:     "global",
				Kind:       "export",
			},
		},
	}, nil
}

func (m *mockLSP) InlineValue(_ context.Context, req *semanticrpc.InlineValueRequest) (*semanticrpc.InlineValueResponse, error) {
	return &semanticrpc.InlineValueResponse{
		Values: []*semanticrpc.InlineValue{
			{
				Range: &semanticrpc.Range{
					Start: &semanticrpc.Position{Line: 10, Character: 5},
					End:   &semanticrpc.Position{Line: 10, Character: 15},
				},
				Text:         "value: 42",
				VariableName: "x",
			},
		},
	}, nil
}
