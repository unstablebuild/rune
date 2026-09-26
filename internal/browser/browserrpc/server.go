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

package browserrpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/handlerrpc"
	"github.com/unstablebuild/rune-go-sdk/term"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
	"unstable.build/rune/internal/browser"
	thandlerrpc "unstable.build/rune/internal/handler/handlerrpc"
)

// Server serves a Browser over GRPC.
type Server struct {
	browserrpc.UnimplementedEventPublisherServer
	browserrpc.UnimplementedNotificationsServer
	browserrpc.UnimplementedResourceOpenerServer
	browserrpc.UnimplementedWindowManagerServer

	syncMode bool

	browser struct {
		browser.Browser
		sync.Locker
	}

	serverCtx       context.Context
	serverCancelCtx func()
}

// NewServer allocates storage for a new Server and initializes it.
func NewServer(browser browser.Browser, lock sync.Locker) *Server {
	ret := new(Server)
	ret.Init(browser, lock)
	return ret
}

// Init initializes this Server with the given browser impl and locker.
func (s *Server) Init(browser browser.Browser, lock sync.Locker) {
	s.browser.Browser = browser
	s.browser.Locker = lock
	s.serverCtx, s.serverCancelCtx = context.WithCancel(context.Background())
}

// SetSyncMode ensures that all future handler clients are fully synchronous.
// This should be used only for testing.
func (s *Server) SetSyncMode() {
	s.syncMode = true
}

func (s *Server) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "browser.Server").Logf(level, msg, args...)
}

// Split satisfies BrowserServer
func (s *Server) Split(srv browserrpc.WindowManager_SplitServer) error {
	msg, err := srv.Recv()
	if err != nil {
		return fmt.Errorf("receive initial request: %w", err)
	}
	req := msg.GetRequest()
	if msg.GetType() != handlerrpc.MessageType_Request || req == nil {
		return errors.New("receive initial request: missing request")
	}
	orientation := protoToModelOrientation(req.GetOrientation())
	uri := req.GetUri()

	var handler browserapi.Handler
	var client clientIfc
	if uri == "" {
		if s.syncMode {
			client = thandlerrpc.NewSyncClientStream(s.serverCtx, srv,
				func() *browserrpc.SplitWindowMessage {
					return new(browserrpc.SplitWindowMessage)
				})
		} else {
			client = thandlerrpc.NewClientStream(s.serverCtx, srv,
				func() *browserrpc.SplitWindowMessage {
					return new(browserrpc.SplitWindowMessage)
				}, s.browser.PublishEvent)
		}
		handler = &streamHandler{mu: s.browser, Handler: client}
	} else {
		h, err := s.getResourceHandler(uri)
		if err != nil {
			return fmt.Errorf("get resource handler: %w", err)
		}
		handler = h
	}

	s.browser.Lock()
	inWin, ok := s.browser.Window(req.GetWindowId())
	if !ok {
		s.browser.Unlock()
		return fmt.Errorf("cannot find window with windowID: %d", req.GetWindowId())
	}
	if inWin.Closed() {
		s.browser.Unlock()
		return fmt.Errorf("cannot split over a closed window: %d", req.GetWindowId())
	}

	outWin, err := s.browser.Split(orientation, inWin, handler)
	if err != nil {
		s.browser.Unlock()
		return fmt.Errorf("new split window: %w", err)
	}
	id := outWin.WindowID()
	resp := handlerrpc.InstallResourceResponse{WindowId: id}
	respMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Response, Response: &resp}

	return s.scheduleOrRespond(handler, client, srv, &respMsg)
}

// Bar satisfies BrowserServer
func (s *Server) Bar(srv browserrpc.WindowManager_BarServer) error {
	msg, err := srv.Recv()
	if err != nil {
		return fmt.Errorf("receive bar request: %w", err)
	}
	req := msg.GetRequest()
	if msg.GetType() != handlerrpc.MessageType_Request || req == nil {
		return errors.New("receive bar request: missing request")
	}
	var client clientIfc
	if s.syncMode {
		client = thandlerrpc.NewSyncClientStream(s.serverCtx, srv,
			func() *browserrpc.SplitWindowMessage {
				return new(browserrpc.SplitWindowMessage)
			})
	} else {
		client = thandlerrpc.NewClientStream(s.serverCtx, srv,
			func() *browserrpc.BarMessage {
				return new(browserrpc.BarMessage)
			}, s.browser.PublishEvent)
	}

	cfg := browserapi.BarConfig{}
	cfg.Orientation = protoToModelOrientation(req.GetOrientation())
	cfg.Size = int(req.GetSize())
	cfg.Frame = protoToModelBarFrame(req.GetFrame())

	streamHandler := &streamHandler{mu: s.browser, Handler: client}

	s.browser.Lock()
	err = s.browser.Bar(cfg, streamHandler)
	if err != nil {
		s.browser.Unlock()
		return err
	}
	resp := handlerrpc.InstallResourceResponse{}
	respMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Response, Response: &resp}
	return s.scheduleOrRespond(streamHandler, client, srv, &respMsg)
}

// Notify satisfies BrowserServer
func (s *Server) Notify(
	ctx context.Context, req *browserrpc.NotifyRequest,
) (*browserrpc.NotifyResponse, error) {
	return s.notify(ctx, req, false)
}

// NotifyOnce satisfies BrowserServer
func (s *Server) NotifyOnce(
	ctx context.Context, req *browserrpc.NotifyRequest,
) (*browserrpc.NotifyResponse, error) {
	return s.notify(ctx, req, true)
}

// UpdateNotificationProgress satisfies BrowserServer
func (s *Server) UpdateNotificationProgress(
	ctx context.Context, req *browserrpc.UpdateNotificationProgressRequest,
) (*browserrpc.UpdateNotificationProgressResponse, error) {
	id := req.GetId()
	total := req.GetTotal()
	progress := req.GetProgress()
	message := req.GetMsg()

	if id == "" || total == 0 || progress > total {
		return nil, status.Errorf(codes.InvalidArgument,
			"id must not be empty, total must not be zero and progress "+
				"must not be larger than total")
	}

	s.browser.Lock()
	defer s.browser.Unlock()

	err := s.browser.UpdateNotificationProgress(id, message, progress, total)
	if err != nil {
		return nil, err
	}

	return &browserrpc.UpdateNotificationProgressResponse{}, nil
}

// Open satisfies BrowserServer
func (s *Server) Open(
	ctx context.Context, req *browserrpc.OpenResourceRequest,
) (*browserrpc.OpenResourceResponse, error) {
	uri, err := workspaceapi.ParseURI(req.GetResource())
	if err != nil {
		return nil, err
	}

	s.browser.Lock()
	defer s.browser.Unlock()

	_, err = s.browser.Open(uri)
	if err != nil {
		return nil, err
	}

	return &browserrpc.OpenResourceResponse{Uri: uri.String()}, nil
}

// Publish satisfies BrowserServer
func (s *Server) Publish(
	ctx context.Context, req *browserrpc.PublishRequest,
) (*browserrpc.PublishResponse, error) {
	ev, err := req.GetEv().ToModel()
	if err != nil {
		s.log(log.WarnLevel, "error converting rpc event to model: %v", err)
		return nil, err
	}

	// Deliberately unlocked: EventPublisher is contracted to be
	// concurrent-safe, and the sink is an atomic store or a buffered
	// channel send. Taking the UI lock here would queue every extension
	// redraw behind a render loop that holds it for a whole tick.
	err = s.browser.PublishEvent(ev)
	if err != nil {
		return nil, err
	}
	s.log(log.TraceLevel, "publish event: %v", ev)

	return new(browserrpc.PublishResponse), nil
}

// Focus satisfies BrowserServer
func (s *Server) Focus(
	ctx context.Context, req *browserrpc.FocusRequest,
) (*browserrpc.FocusResponse, error) {
	s.browser.Lock()
	defer s.browser.Unlock()

	win, err := s.browser.Focus()
	if err != nil {
		return nil, err
	}

	res := &browserrpc.FocusResponse{
		WindowId: win.WindowID(),
	}

	return res, nil
}

// SetTabActivity satisfies BrowserServer.
func (s *Server) SetTabActivity(
	ctx context.Context, req *browserrpc.SetTabActivityRequest,
) (*browserrpc.SetTabActivityResponse, error) {
	uri, err := workspaceapi.ParseURI(req.GetResourceId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument,
			"parse resource id %q: %v", req.GetResourceId(), err)
	}

	s.browser.Lock()
	defer s.browser.Unlock()

	if err := s.browser.SetTabActivity(uri, req.GetActive()); err != nil {
		return nil, err
	}
	return new(browserrpc.SetTabActivityResponse), nil
}

// CloseWindow satisfies BrowserServer.
func (s *Server) CloseWindow(
	ctx context.Context, req *browserrpc.WindowCloseRequest,
) (*browserrpc.WindowCloseResponse, error) {
	id := req.GetWindowId()
	if id == 0 {
		return nil, fmt.Errorf("missing request window id: %d", id)
	}

	s.browser.Lock()
	defer s.browser.Unlock()

	// if window could not be found, then we should
	// mimic idempotent close behaviour
	win, ok := s.browser.Window(id)
	if !ok {
		return new(browserrpc.WindowCloseResponse), nil
	}

	err := win.Close()
	if err != nil {
		return nil, err
	}

	return new(browserrpc.WindowCloseResponse), nil
}

// SetTabName satisfies BrowserServer.
func (s *Server) SetTabName(
	ctx context.Context, req *browserrpc.SetTabNameRequest,
) (*browserrpc.SetTabNameResponse, error) {
	uri, err := workspaceapi.ParseURI(req.GetResourceId())
	if err != nil {
		return nil, fmt.Errorf("parse uri: %w", err)
	}

	s.browser.Lock()
	defer s.browser.Unlock()

	if err := s.browser.SetTabName(uri, req.GetName(), term.Attributes{}); err != nil {
		return nil, err
	}
	return new(browserrpc.SetTabNameResponse), nil
}

// Floating satisfies BrowserServer
func (s *Server) Floating(srv browserrpc.WindowManager_FloatingServer) error {
	msg, err := srv.Recv()
	if err != nil {
		return fmt.Errorf("receive initial request: %w", err)
	}
	req := msg.GetRequest()
	if msg.GetType() != handlerrpc.MessageType_Request || req == nil {
		return errors.New("receive initial request: missing request")
	}

	var client clientIfc
	if s.syncMode {
		client = thandlerrpc.NewSyncClientStream(s.serverCtx, srv,
			func() *browserrpc.SplitWindowMessage {
				return new(browserrpc.SplitWindowMessage)
			})
	} else {
		client = thandlerrpc.NewClientStream(s.serverCtx, srv,
			func() *browserrpc.FloatingWindowMessage {
				return new(browserrpc.FloatingWindowMessage)
			}, s.browser.PublishEvent)
	}

	at := req.GetOffset().ToModel()
	alignment := component.Alignment(req.GetAlignment())
	cfg := browserapi.FloatingConfig{
		Offset:      at,
		Alignment:   alignment,
		NoWindowBar: req.GetNoWindowBar(),
		Title:       req.GetTitle(),
	}

	// NOTE: intercept the first calls to Dimensions and Resize
	// so send install response before stream starts exchanging
	// messages.
	streamHandler := &floatingStreamHandler{Floating: client}

	s.browser.Lock()
	win, err := s.browser.Floating(streamHandler, cfg)
	if err != nil {
		s.browser.Unlock()
		return fmt.Errorf("new floating window: %w", err)
	}
	id := win.WindowID()
	resp := handlerrpc.InstallResourceResponse{WindowId: id}
	respMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Response, Response: &resp}
	return s.scheduleOrRespond(streamHandler, client, srv, &respMsg)
}

// Tab satisfies BrowserServer
func (s *Server) Tab(srv browserrpc.WindowManager_TabServer) error {
	msg, err := srv.Recv()
	if err != nil {
		return fmt.Errorf("receive initial request: %w", err)
	}
	req := msg.GetRequest()
	if msg.GetType() != handlerrpc.MessageType_Request || req == nil {
		return errors.New("receive initial request: missing request")
	}

	name := req.GetResourceName()
	iconStr := req.GetResourceIcon()
	resourceID := req.GetResourceId()
	resourceURI, err := workspaceapi.ParseURI(resourceID)
	if err != nil {
		return fmt.Errorf("parse uri: %w", err)
	}

	var icon rune
	if len(iconStr) != 0 {
		icon = []rune(iconStr)[0]
	}

	var client clientIfc
	if s.syncMode {
		client = thandlerrpc.NewSyncClientStream(s.serverCtx, srv,
			func() *browserrpc.SplitWindowMessage {
				return new(browserrpc.SplitWindowMessage)
			})
	} else {
		client = thandlerrpc.NewClientStream(s.serverCtx, srv,
			func() *browserrpc.TabMessage {
				return new(browserrpc.TabMessage)
			}, s.browser.PublishEvent)
	}
	handler := &streamHandler{mu: s.browser, Handler: client}

	s.browser.Lock()
	_, err = s.browser.Tab(resourceURI, icon, name, handler)
	s.browser.Unlock()
	if err != nil {
		return fmt.Errorf("new split window: %w", err)
	}

	resp := handlerrpc.InstallResourceResponse{}
	respMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Response, Response: &resp}
	if err := srv.SendMsg(&respMsg); err != nil {
		return fmt.Errorf("send install response: %w", err)
	}

	handler.doneSetup()
	return client.ReceiveMessages()
}

// SetContent satisfies BrowserServer.
func (s *Server) SetContent(srv browserrpc.WindowManager_SetContentServer) error {
	msg, err := srv.Recv()
	if err != nil {
		return fmt.Errorf("receive initial request: %w", err)
	}
	req := msg.GetRequest()
	if msg.GetType() != handlerrpc.MessageType_Request || req == nil {
		return errors.New("receive initial request: missing request")
	}

	uri := req.GetUri()

	var handler browserapi.Handler
	var client clientIfc
	if uri == "" {
		if s.syncMode {
			client = thandlerrpc.NewSyncClientStream(s.serverCtx, srv,
				func() *browserrpc.SplitWindowMessage {
					return new(browserrpc.SplitWindowMessage)
				})
		} else {
			client = thandlerrpc.NewClientStream(s.serverCtx, srv,
				func() *browserrpc.WindowSetContentMessage {
					return new(browserrpc.WindowSetContentMessage)
				}, s.browser.PublishEvent)
		}
		handler = &streamHandler{mu: s.browser, Handler: client}
	} else {
		h, err := s.getResourceHandler(uri)
		if err != nil {
			return fmt.Errorf("get resource handler: %w", err)
		}
		handler = h
	}

	s.browser.Lock()
	inWin, ok := s.browser.Window(req.GetWindowId())
	if !ok {
		s.browser.Unlock()
		return fmt.Errorf("cannot find window with windowID: %d", req.GetWindowId())
	}
	if inWin.Closed() {
		s.browser.Unlock()
		return fmt.Errorf("cannot set content to a closed window: %d", req.GetWindowId())
	}

	err = inWin.SetContent(handler)
	if err != nil {
		s.browser.Unlock()
		return fmt.Errorf("window set content: %w", err)
	}

	resp := handlerrpc.InstallResourceResponse{WindowId: inWin.WindowID()}
	respMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Response, Response: &resp}
	return s.scheduleOrRespond(handler, client, srv, &respMsg)
}

// Close satisfies BrowserServer.
func (s *Server) Close(
	ctx context.Context, req *browserrpc.WindowCloseRequest,
) (*browserrpc.WindowCloseResponse, error) {
	s.browser.Lock()
	defer s.browser.Unlock()

	win, ok := s.browser.Window(req.GetWindowId())
	if !ok || win.Closed() {
		// close is idempotent
		return new(browserrpc.WindowCloseResponse), nil
	}

	err := win.Close()
	if err != nil {
		return nil, err
	}
	return new(browserrpc.WindowCloseResponse), nil
}

// Stop closes all resources associated with this server.
func (s *Server) Stop() (err error) {
	if s.serverCancelCtx != nil {
		s.serverCancelCtx()
		s.serverCancelCtx = nil
	}
	s.log(log.TraceLevel, "stopping server along with all handlerrpc clients")
	return nil
}

func (s *Server) getResourceHandler(uriStr string) (browserapi.Handler, error) {
	// if it's not a URI, then it must be a remote handler
	uri, err := workspaceapi.ParseURI(uriStr)
	if err != nil {
		return nil, fmt.Errorf("parse uri %q: %w", uriStr, err)
	}
	s.browser.Lock()
	h, ok := s.browser.Resource(uri)
	s.browser.Unlock()
	if !ok {
		return nil, fmt.Errorf("resource with uri %s not found", uriStr)
	}

	return h, nil
}

// assumes we're holding the browser mutex
func (s *Server) scheduleOrRespond(
	handler browserapi.Handler, client clientIfc,
	srv grpc.ServerStream, respMsg *handlerrpc.ServerMessage,
) error {
	// if it not a client, it's safe to response immediately
	// since nothing else will be sending messages on the stream.
	if client != nil {
		client.ScheduleResponse(respMsg)
		s.browser.Unlock()
		if h, ok := handler.(*streamHandler); ok {
			h.doneSetup()
		}
		if h, ok := handler.(*floatingStreamHandler); ok {
			h.setup.Store(true)
		}
		return client.ReceiveMessages()
	}
	s.browser.Unlock()
	if err := srv.SendMsg(respMsg); err != nil {
		return fmt.Errorf("send install response: %w", err)
	}
	if h, ok := handler.(*floatingStreamHandler); ok {
		h.setup.Store(true)
	}
	return nil
}

func (s *Server) setBrowserMessage(
	level browserapi.NotificationLevel, msg string, once bool,
) (string, error) {
	s.browser.Lock()
	defer s.browser.Unlock()

	if !once {
		return s.browser.Notify(level, "%s", msg)
	}
	return s.browser.NotifyOnce(level, "%s", msg)
}

func (s *Server) notify(
	ctx context.Context, req *browserrpc.NotifyRequest, once bool,
) (*browserrpc.NotifyResponse, error) {
	msg := sanitizeLine(req.GetMsg())
	level := browserapi.NotificationLevel(req.GetLevel())
	switch level {
	case browserapi.LevelInfo,
		browserapi.LevelSuccess,
		browserapi.LevelWarn,
		browserapi.LevelError:
	default:
		return nil, status.Error(codes.InvalidArgument, "invalid level")
	}
	id, err := s.setBrowserMessage(level, msg, once)
	if err != nil {
		return nil, err
	}
	resp := new(browserrpc.NotifyResponse)
	resp.Id = id
	return resp, nil
}

func protoToModelOrientation(p browserrpc.Orientation) (o browserapi.Orientation) {
	switch p {
	case browserrpc.Orientation_Default:
		o = browserapi.OrientationDefault
	case browserrpc.Orientation_Top:
		o = browserapi.OrientationTop
	case browserrpc.Orientation_Bottom:
		o = browserapi.OrientationBottom
	case browserrpc.Orientation_Left:
		o = browserapi.OrientationLeft
	case browserrpc.Orientation_Right:
		o = browserapi.OrientationRight
	}
	return
}

func protoToModelBarFrame(p browserrpc.BarRequest_Frame) (o browserapi.BarFrame) {
	switch p {
	case browserrpc.BarRequest_Default:
		o = browserapi.BarFrameDefault
	case browserrpc.BarRequest_Always:
		o = browserapi.BarFrameAlways
	case browserrpc.BarRequest_Never:
		o = browserapi.BarFrameNever
	}
	return
}

func sanitizeLine(in string) string {
	var b strings.Builder
	for _, r := range in {
		switch r {
		case '\x00':
		case '\n':
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

type streamHandler struct {
	browserapi.Handler
	mu     sync.Locker
	setup  atomic.Bool
	width  int
	height int
}

func (f *streamHandler) Resize(width, height int) {
	if !f.setup.Load() {
		f.width = width
		f.height = height
		return
	}
	f.Handler.Resize(width, height)
}

func (f *streamHandler) doneSetup() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Handler.Resize(f.width, f.height)
	// Resizes reach this handler under the same lock, so publishing the
	// flag here is what stops one that lands now from being recorded as
	// pending setup and never forwarded.
	f.setup.Store(true)
}

type floatingStreamHandler struct {
	browserapi.Floating
	setup atomic.Bool
}

func (f *floatingStreamHandler) Dimensions() (width, height int) {
	if !f.setup.Load() {
		return
	}
	return f.Floating.Dimensions()
}

func (f *floatingStreamHandler) Resize(width, height int) {
	if !f.setup.Load() {
		return
	}
	f.Floating.Resize(width, height)
}

type clientIfc interface {
	browserapi.Floating
	ReceiveMessages() error
	ScheduleResponse(*handlerrpc.ServerMessage)
}
