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

package idelsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/workspace"
)

// caseFS is a FileSystem for tests that only need its case rule; the
// zero value is case-sensitive.
type caseFS struct {
	schemeapi.FileSystem
	insensitive bool
}

func (f caseFS) PathCaseSensitive() bool { return !f.insensitive }

type stubPkgManager struct {
	bin string
}

func (p *stubPkgManager) LibDir(
	_ context.Context, _ string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice(
		[]string{p.bin},
	), nil
}

type testCallback struct {
	mu             sync.Mutex
	onShowMessage  func(params semanticapi.ShowMessageParams)
	onProgress     func(semanticapi.ProgressParams)
	onApplyEdit    func(semanticapi.ApplyWorkspaceEditParams)
	onShowDocument func(semanticapi.ShowDocumentParams)
	diagnostics    []semanticapi.PublishDiagnosticsParams
	messages       []semanticapi.ShowMessageParams
	logMessages    []semanticapi.LogMessageParams
	progress       []semanticapi.ProgressParams
	applyEdits     []semanticapi.ApplyWorkspaceEditParams
	showDocuments  []semanticapi.ShowDocumentParams
	onDiagnostics  func(semanticapi.PublishDiagnosticsParams)

	invalidateAllPendingCount int
	diagnosticRefreshCount    int
	fileDidChangeCalls        []fileDidChangeCall
	publishes                 []publishedDiagnostic
}

// publishedDiagnostic pairs a publish with the LSP metadata carried on
// its context, which identifies the publishing server slot.
type publishedDiagnostic struct {
	params   semanticapi.PublishDiagnosticsParams
	metadata Metadata
}

type fileDidChangeCall struct {
	uri     string
	version int32
	open    bool
	oob     bool
}

func (c *testCallback) ShowMessage(
	_ context.Context, params semanticapi.ShowMessageParams,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, params)
	if c.onShowMessage != nil {
		c.onShowMessage(params)
	}
	return nil
}

func (c *testCallback) LogMessage(
	_ context.Context, params semanticapi.LogMessageParams,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logMessages = append(c.logMessages, params)
	return nil
}

func (c *testCallback) PublishDiagnostics(
	ctx context.Context, params semanticapi.PublishDiagnosticsParams,
) error {
	md, _ := metadataFromContext(ctx)
	c.mu.Lock()
	c.diagnostics = append(c.diagnostics, params)
	c.publishes = append(c.publishes,
		publishedDiagnostic{params: params, metadata: md})
	cb := c.onDiagnostics
	c.mu.Unlock()
	if cb != nil {
		cb(params)
	}
	return nil
}

func (c *testCallback) Progress(
	_ context.Context, params semanticapi.ProgressParams,
) error {
	c.mu.Lock()
	c.progress = append(c.progress, params)
	cb := c.onProgress
	c.mu.Unlock()
	if cb != nil {
		cb(params)
	}
	return nil
}

func (c *testCallback) LogTrace(
	_ context.Context, _ semanticapi.LogTraceParams,
) error {
	return nil
}

func (c *testCallback) ShowDocument(
	_ context.Context, params semanticapi.ShowDocumentParams,
) (semanticapi.ShowDocumentResult, error) {
	c.mu.Lock()
	c.showDocuments = append(c.showDocuments, params)
	cb := c.onShowDocument
	c.mu.Unlock()
	if cb != nil {
		cb(params)
	}
	return semanticapi.ShowDocumentResult{Success: true}, nil
}

func (c *testCallback) ShowMessageRequest(
	_ context.Context, _ semanticapi.ShowMessageRequestParams,
) (*semanticapi.MessageActionItem, error) {
	return nil, nil
}

func (c *testCallback) WorkDoneProgressCreate(
	_ context.Context, _ semanticapi.WorkDoneProgressCreateParams,
) error {
	return nil
}

func (c *testCallback) ApplyEdit(
	_ context.Context,
	params semanticapi.ApplyWorkspaceEditParams,
) (semanticapi.ApplyWorkspaceEditResult, error) {
	c.mu.Lock()
	c.applyEdits = append(c.applyEdits, params)
	cb := c.onApplyEdit
	c.mu.Unlock()
	if cb != nil {
		cb(params)
	}
	return semanticapi.ApplyWorkspaceEditResult{
		Applied: true,
	}, nil
}

func (c *testCallback) WorkspaceFolders(
	_ context.Context,
) ([]semanticapi.WorkspaceFolder, error) {
	return nil, nil
}

func (c *testCallback) Configuration(
	_ context.Context, _ semanticapi.ConfigurationParams,
) ([]json.RawMessage, error) {
	return nil, nil
}

func (c *testCallback) RegisterCapability(
	_ context.Context, _ semanticapi.RegistrationParams,
) error {
	return nil
}

func (c *testCallback) UnregisterCapability(
	_ context.Context, _ semanticapi.UnregistrationParams,
) error {
	return nil
}

func (c *testCallback) CodeLensRefresh(_ context.Context) error {
	return nil
}

func (c *testCallback) SemanticTokensRefresh(_ context.Context) error {
	return nil
}

func (c *testCallback) InlayHintRefresh(_ context.Context) error {
	return nil
}

func (c *testCallback) DiagnosticRefresh(_ context.Context) error {
	c.mu.Lock()
	c.diagnosticRefreshCount++
	c.mu.Unlock()
	return nil
}
func (c *testCallback) HandleNotification(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}

func (c *testCallback) FileDidChange(uri string, version int32, open, oob bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fileDidChangeCalls = append(c.fileDidChangeCalls,
		fileDidChangeCall{uri: uri, version: version, open: open, oob: oob})
}

func (c *testCallback) InvalidateAllPending() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.invalidateAllPendingCount++
}

func (c *testCallback) WaitFileProcessed(_ context.Context, _ string) error {
	return nil
}

// localScheme implements schemeapi.FileSystem and schemeapi.Executor
// using the local OS for e2e testing.
type localScheme struct {
	mu      sync.Mutex
	procs   map[workspaceapi.Pid]*os.Process
	nextPid workspaceapi.Pid
}

func newTestScheme() *localScheme {
	return &localScheme{
		procs:   make(map[workspaceapi.Pid]*os.Process),
		nextPid: 1,
	}
}

// schemeapi.FileSystem methods

func (s *localScheme) PathCaseSensitive() bool {
	return workspace.ProbePathCaseSensitive(s, os.TempDir())
}

func (s *localScheme) Create(filename string) (workspaceapi.File, error) {
	return os.Create(filename)
}

func (s *localScheme) Open(filename string) (workspaceapi.File, error) {
	return os.Open(filename)
}

func (s *localScheme) OpenFile(filename string, flag int, perm os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(filename, flag, perm)
}

func (s *localScheme) Stat(filename string) (os.FileInfo, error) {
	return os.Stat(filename)
}

func (s *localScheme) Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

func (s *localScheme) Remove(filename string) error {
	return os.Remove(filename)
}

func (s *localScheme) Join(elem ...string) string {
	return filepath.Join(elem...)
}

func (s *localScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	return os.CreateTemp(dir, prefix)
}

func (s *localScheme) Lstat(filename string) (os.FileInfo, error) {
	return os.Lstat(filename)
}

func (s *localScheme) Symlink(oldname, newname string) error {
	return os.Symlink(oldname, newname)
}

func (s *localScheme) Readlink(link string) (string, error) {
	return os.Readlink(link)
}

func (s *localScheme) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

func (s *localScheme) MkdirAll(filename string, perm os.FileMode) error {
	return os.MkdirAll(filename, perm)
}

// schemeapi.Executor methods

func (s *localScheme) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	if cmd.Dir != "" {
		c.Dir = cmd.Dir
	}
	if cmd.Env != nil {
		c.Env = cmd.Env
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
	s.mu.Unlock()

	if cmd.Watcher != nil {
		ch := cmd.Watcher.WatchProcess()
		go func() {
			err := c.Wait()
			if ch != nil {
				ch <- err
			}
		}()
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

func (s *localScheme) Close() error {
	return nil
}

// readyOnProgress returns an onProgress callback that
// signals wg.Done via the given sync.Once when a progress
// sequence completes (end event received).
func readyOnProgress(
	once *sync.Once, wg *sync.WaitGroup,
) func(semanticapi.ProgressParams) {
	return func(p semanticapi.ProgressParams) {
		var v struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(p.Value, &v) != nil {
			return
		}
		if v.Kind == "end" {
			once.Do(wg.Done)
		}
	}
}

func makeURI(t *testing.T, uri string) workspaceapi.URI {
	ret, err := workspaceapi.ParseURI(uri)
	require.NoError(t, err)
	return ret
}
