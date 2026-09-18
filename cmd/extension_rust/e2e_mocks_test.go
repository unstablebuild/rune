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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
)

// raCallback implements idelsp.Callback for e2e tests, recording applied
// edits and forwarding progress/apply-edit notifications to optional hooks.
type raCallback struct {
	mu             sync.Mutex
	onServerStatus func(serverStatus)
	onApplyEdit    func(semanticapi.ApplyWorkspaceEditParams)
	appliedEdits   []semanticapi.ApplyWorkspaceEditParams
	diagnostics    []semanticapi.PublishDiagnosticsParams
}

// serverStatus is the payload of rust-analyzer's experimental/serverStatus
// notification; quiescent reports that no background work is pending.
type serverStatus struct {
	Health    string `json:"health"`
	Quiescent bool   `json:"quiescent"`
}

func (c *raCallback) ShowMessage(_ context.Context, _ semanticapi.ShowMessageParams) error {
	return nil
}

func (c *raCallback) LogMessage(_ context.Context, _ semanticapi.LogMessageParams) error {
	return nil
}

func (c *raCallback) PublishDiagnostics(
	_ context.Context, params semanticapi.PublishDiagnosticsParams,
) error {
	c.mu.Lock()
	c.diagnostics = append(c.diagnostics, params)
	c.mu.Unlock()
	return nil
}

func (c *raCallback) publishedDiagnostics() []semanticapi.PublishDiagnosticsParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]semanticapi.PublishDiagnosticsParams(nil), c.diagnostics...)
}

func (c *raCallback) Progress(_ context.Context, _ semanticapi.ProgressParams) error {
	return nil
}

func (c *raCallback) LogTrace(_ context.Context, _ semanticapi.LogTraceParams) error { return nil }

func (c *raCallback) ShowDocument(
	_ context.Context, _ semanticapi.ShowDocumentParams,
) (semanticapi.ShowDocumentResult, error) {
	return semanticapi.ShowDocumentResult{Success: true}, nil
}

func (c *raCallback) ShowMessageRequest(
	_ context.Context, _ semanticapi.ShowMessageRequestParams,
) (*semanticapi.MessageActionItem, error) {
	return nil, nil
}

func (c *raCallback) WorkDoneProgressCreate(
	_ context.Context, _ semanticapi.WorkDoneProgressCreateParams,
) error {
	return nil
}

func (c *raCallback) ApplyEdit(
	_ context.Context, params semanticapi.ApplyWorkspaceEditParams,
) (semanticapi.ApplyWorkspaceEditResult, error) {
	c.mu.Lock()
	c.appliedEdits = append(c.appliedEdits, params)
	cb := c.onApplyEdit
	c.mu.Unlock()
	if cb != nil {
		cb(params)
	}
	return semanticapi.ApplyWorkspaceEditResult{Applied: true}, nil
}

func (c *raCallback) WorkspaceFolders(_ context.Context) ([]semanticapi.WorkspaceFolder, error) {
	return nil, nil
}

func (c *raCallback) Configuration(
	_ context.Context, params semanticapi.ConfigurationParams,
) ([]json.RawMessage, error) {
	result := make([]json.RawMessage, len(params.Items))
	for i := range result {
		result[i] = json.RawMessage(`{}`)
	}
	return result, nil
}

func (c *raCallback) RegisterCapability(_ context.Context, _ semanticapi.RegistrationParams) error {
	return nil
}

func (c *raCallback) UnregisterCapability(
	_ context.Context, _ semanticapi.UnregistrationParams,
) error {
	return nil
}

func (c *raCallback) CodeLensRefresh(_ context.Context) error       { return nil }
func (c *raCallback) SemanticTokensRefresh(_ context.Context) error { return nil }
func (c *raCallback) InlayHintRefresh(_ context.Context) error      { return nil }
func (c *raCallback) DiagnosticRefresh(_ context.Context) error     { return nil }
func (c *raCallback) FileDidChange(_ string, _ int32, _, _ bool)    {}
func (c *raCallback) InvalidateAllPending()                         {}
func (c *raCallback) WaitFileProcessed(_ context.Context, _ string) error {
	return nil
}

func (c *raCallback) HandleNotification(
	_ context.Context, method string, params json.RawMessage,
) error {
	if method != "experimental/serverStatus" {
		return nil
	}
	var status serverStatus
	if err := json.Unmarshal(params, &status); err != nil {
		return err
	}
	c.mu.Lock()
	cb := c.onServerStatus
	c.mu.Unlock()
	if cb != nil {
		cb(status)
	}
	return nil
}

// localScheme implements schemeapi.FileSystem, schemeapi.Executor, and
// workspaceapi.Executor over the local OS, rooted at a directory so
// relative paths resolve into the test workspace.
type localScheme struct {
	root    string
	mu      sync.Mutex
	procs   map[workspaceapi.Pid]*os.Process
	nextPid workspaceapi.Pid
	started []startedProcess
}

type synchronizedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *synchronizedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *synchronizedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

type startedProcess struct {
	path   string
	args   []string
	env    []string
	stderr *synchronizedBuffer
}

func (s *localScheme) startedProcess(path string) (startedProcess, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.started) - 1; i >= 0; i-- {
		if s.started[i].path == path {
			return s.started[i], true
		}
	}
	return startedProcess{}, false
}

func newLocalScheme(root string) *localScheme {
	return &localScheme{root: root, procs: make(map[workspaceapi.Pid]*os.Process), nextPid: 1}
}

func (s *localScheme) resolve(path string) string {
	if s.root == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(s.root, path)
}

func (s *localScheme) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + s.resolve(path))
}
func (s *localScheme) Create(name string) (workspaceapi.File, error) {
	return os.Create(s.resolve(name))
}
func (s *localScheme) Open(name string) (workspaceapi.File, error) { return os.Open(s.resolve(name)) }
func (s *localScheme) OpenFile(name string, flag int, perm os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(s.resolve(name), flag, perm)
}
func (s *localScheme) Stat(name string) (os.FileInfo, error) { return os.Stat(s.resolve(name)) }
func (s *localScheme) Rename(o, n string) error {
	return os.Rename(s.resolve(o), s.resolve(n))
}
func (s *localScheme) Remove(name string) error   { return os.Remove(s.resolve(name)) }
func (s *localScheme) Join(elem ...string) string { return filepath.Join(elem...) }
func (s *localScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	return os.CreateTemp(s.resolve(dir), prefix)
}
func (s *localScheme) Lstat(name string) (os.FileInfo, error) { return os.Lstat(s.resolve(name)) }
func (s *localScheme) Symlink(o, n string) error {
	return os.Symlink(s.resolve(o), s.resolve(n))
}
func (s *localScheme) Readlink(link string) (string, error) { return os.Readlink(s.resolve(link)) }
func (s *localScheme) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(s.resolve(path))
}
func (s *localScheme) MkdirAll(name string, perm os.FileMode) error {
	return os.MkdirAll(s.resolve(name), perm)
}

func (s *localScheme) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return s.StartCommand(ctx, cmd)
}

func (s *localScheme) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	stderr := &synchronizedBuffer{}
	if cmd.Stderr == nil {
		cmd.Stderr = stderr
	}
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	if cmd.Dir != "" {
		c.Dir = cmd.Dir
	}
	if cmd.Env != nil {
		c.Env = append(c.Environ(), cmd.Env...)
	}
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	if cmd.SysProcAttr != nil {
		c.SysProcAttr = cmd.SysProcAttr
	}
	if err := c.Start(); err != nil {
		return 0, err
	}
	s.mu.Lock()
	pid := s.nextPid
	s.nextPid++
	s.procs[pid] = c.Process
	s.started = append(s.started, startedProcess{
		path: cmd.Path, args: append([]string{}, cmd.Args...),
		env: append([]string{}, cmd.Env...), stderr: stderr,
	})
	s.mu.Unlock()
	if cmd.Watcher != nil {
		ch := cmd.Watcher.WatchProcess()
		go debug.CapturePanicReport(func() {
			err := c.Wait()
			if ch != nil {
				ch <- err
			}
		})
	}
	return pid, nil
}

func (s *localScheme) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	s.mu.Lock()
	proc, ok := s.procs[pid]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("process %d not found", pid)
	}
	return proc.Signal(sig)
}

func (s *localScheme) Close() error { return nil }

// mockNotification records a single notification.
type mockNotification struct {
	Level   browserapi.NotificationLevel
	Message string
}

// mockNotifications implements browserapi.Notifications for tests.
type mockNotifications struct {
	mu       sync.Mutex
	messages []mockNotification
}

var _ browserapi.Notifications = (*mockNotifications)(nil)

func (m *mockNotifications) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, mockNotification{Level: level, Message: fmt.Sprintf(msg, args...)})
	return "", nil
}

func (m *mockNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return m.Notify(level, msg, args...)
}

func (m *mockNotifications) UpdateNotificationProgress(_, _ string, _, _ int64) error {
	return nil
}

func (m *mockNotifications) hasMessage(substr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, msg := range m.messages {
		if strings.Contains(msg.Message, substr) {
			return true
		}
	}
	return false
}

// mockEdit records a single CellEditor.Edit call.
type mockEdit struct {
	Start   term.Coordinates
	End     term.Coordinates
	NewText string
}

// mockCellEditor implements textapi.CellEditor, recording edits and
// forwarding them to an optional hook.
type mockCellEditor struct {
	mu     sync.Mutex
	edits  []mockEdit
	onEdit func(start, end term.Coordinates, text string)
}

var _ textapi.CellEditor = (*mockCellEditor)(nil)

func (e *mockCellEditor) Edit(
	_ context.Context, start, end term.Coordinates, str string,
) (term.Coordinates, term.Coordinates, string, error) {
	e.mu.Lock()
	e.edits = append(e.edits, mockEdit{Start: start, End: end, NewText: str})
	cb := e.onEdit
	e.mu.Unlock()
	if cb != nil {
		cb(start, end, str)
	}
	return start, end, "", nil
}

// mockEditor implements textapi.Editor for tests, tracking registered
// resources, subscribed event handlers, and per-handler cell editors.
type mockEditor struct {
	mu          sync.Mutex
	handlers    map[string]textapi.Handler
	cellEditors map[textapi.Handler]*mockCellEditor
	evHandlers  []textapi.EventHandler
	onCellEdit  func(uri workspaceapi.URI, start, end term.Coordinates, text string)
	cursors     map[textapi.Handler]term.Coordinates
}

var _ textapi.Editor = (*mockEditor)(nil)

func newMockEditor() *mockEditor {
	return &mockEditor{
		handlers:    make(map[string]textapi.Handler),
		cellEditors: make(map[textapi.Handler]*mockCellEditor),
		cursors:     make(map[textapi.Handler]term.Coordinates),
	}
}

func (e *mockEditor) Register(h textapi.Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[h.Resource().String()] = h
}

func (e *mockEditor) SubscribeEvents(_ []textapi.EventType, h textapi.EventHandler) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.evHandlers = append(e.evHandlers, h)
	return nil
}

func (e *mockEditor) fireEvent(ctx context.Context, ev textapi.Event) {
	e.mu.Lock()
	handlers := append([]textapi.EventHandler{}, e.evHandlers...)
	e.mu.Unlock()
	for _, h := range handlers {
		h.Handle(ctx, ev)
	}
}

func (e *mockEditor) Editor(uri workspaceapi.URI) (textapi.Handler, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if h, ok := e.handlers[uri.String()]; ok {
		return h, nil
	}
	return nil, errors.New("editor not found")
}

func (e *mockEditor) SetLocationList(
	_ textapi.Handler, _ textapi.LocationPriority, _ string, _ textapi.LocationList,
) error {
	return nil
}
func (e *mockEditor) MoveToNextLocation(_ textapi.Handler, _ string) error { return nil }
func (e *mockEditor) MoveToPrevLocation(_ textapi.Handler, _ string) error { return nil }
func (e *mockEditor) Cursor(h textapi.Handler) (term.Coordinates, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cursors[h], nil
}
func (e *mockEditor) SetCursor(h textapi.Handler, c term.Coordinates) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cursors[h] = c
	return nil
}
func (e *mockEditor) CellView(_ textapi.Handler) textapi.CellView { return nil }

func (e *mockEditor) CellEditor(h textapi.Handler) textapi.CellEditor {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ce, ok := e.cellEditors[h]; ok {
		return ce
	}
	ce := &mockCellEditor{}
	if e.onCellEdit != nil {
		uri := h.Resource()
		parent := e.onCellEdit
		ce.onEdit = func(start, end term.Coordinates, text string) {
			parent(uri, start, end, text)
		}
	}
	e.cellEditors[h] = ce
	return ce
}

func (e *mockEditor) SetDefaultAttributes(_ textapi.Handler, _ term.Attributes) error {
	return nil
}

func (e *mockEditor) editsFor(h textapi.Handler) []mockEdit {
	e.mu.Lock()
	ce, ok := e.cellEditors[h]
	e.mu.Unlock()
	if !ok {
		return nil
	}
	ce.mu.Lock()
	defer ce.mu.Unlock()
	return append([]mockEdit{}, ce.edits...)
}

// lastCursor returns the cursor position last set for h via SetCursor.
func (e *mockEditor) lastCursor(h textapi.Handler) term.Coordinates {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cursors[h]
}

// mockResourceOpener registers a stubResource for any opened URI into the
// backing mockEditor, so navigation commands that open a file (then call
// editor.Editor) find a handler. It records the opened URIs for assertions.
type mockResourceOpener struct {
	editor *mockEditor
	mu     sync.Mutex
	opened []string
}

var _ browserapi.ResourceOpener = (*mockResourceOpener)(nil)

func newMockResourceOpener(editor *mockEditor) *mockResourceOpener {
	return &mockResourceOpener{editor: editor}
}

func (o *mockResourceOpener) Open(uri workspaceapi.URI) (browserapi.Handler, error) {
	o.mu.Lock()
	o.opened = append(o.opened, uri.String())
	o.mu.Unlock()
	if _, err := o.editor.Editor(uri); err != nil {
		res := &stubResource{uri: uri}
		o.editor.Register(res)
		return res, nil
	}
	h, _ := o.editor.Editor(uri)
	return h, nil
}

func (o *mockResourceOpener) openedURIs() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string{}, o.opened...)
}
