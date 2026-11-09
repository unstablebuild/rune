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

package lspcmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// findGopls locates the gopls binary or skips the test.
func findGopls(t *testing.T) string {
	t.Helper()
	goplsBin, err := exec.LookPath("gopls")
	if err != nil {
		for _, p := range []string{
			filepath.Join(
				os.Getenv("HOME"),
				".rune", "bin", "gopls",
			),
			filepath.Join(
				os.Getenv("HOME"),
				"go", "bin", "gopls",
			),
		} {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		t.Skip("gopls not found, skipping e2e test")
	}
	return goplsBin
}

// setupTestWorkspace copies testdata into a temp directory.
func setupTestWorkspace(
	t *testing.T, testdataDir string,
) string {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "lspcmd-test-")
	require.NoError(t, err)

	if testdataDir == "" {
		return tmpDir
	}

	copyDir(t, testdataDir, tmpDir)
	return tmpDir
}

func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	require.NoError(t, err)
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			require.NoError(t, os.MkdirAll(dstPath, 0o755))
			copyDir(t, srcPath, dstPath)
			continue
		}
		data, err := os.ReadFile(srcPath)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(
			dstPath, data, 0o644,
		))
	}
}

// stubPkgManager implements idelsp.PkgManager for tests.
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

// e2eCallback implements semanticapi.LSPCallback for e2e tests.
type e2eCallback struct {
	mu            sync.Mutex
	onShowMessage func(params semanticapi.ShowMessageParams)
	onProgress    func(semanticapi.ProgressParams)
}

func (c *e2eCallback) ShowMessage(
	_ context.Context, params semanticapi.ShowMessageParams,
) error {
	c.mu.Lock()
	cb := c.onShowMessage
	c.mu.Unlock()
	if cb != nil {
		cb(params)
	}
	return nil
}

func (c *e2eCallback) LogMessage(
	_ context.Context, _ semanticapi.LogMessageParams,
) error {
	return nil
}

func (c *e2eCallback) PublishDiagnostics(
	_ context.Context, _ semanticapi.PublishDiagnosticsParams,
) error {
	return nil
}

func (c *e2eCallback) Progress(
	_ context.Context, params semanticapi.ProgressParams,
) error {
	c.mu.Lock()
	cb := c.onProgress
	c.mu.Unlock()
	if cb != nil {
		cb(params)
	}
	return nil
}

func (c *e2eCallback) LogTrace(
	_ context.Context, _ semanticapi.LogTraceParams,
) error {
	return nil
}

func (c *e2eCallback) ShowDocument(
	_ context.Context, _ semanticapi.ShowDocumentParams,
) (semanticapi.ShowDocumentResult, error) {
	return semanticapi.ShowDocumentResult{Success: true}, nil
}

func (c *e2eCallback) ShowMessageRequest(
	_ context.Context, _ semanticapi.ShowMessageRequestParams,
) (*semanticapi.MessageActionItem, error) {
	return nil, nil
}

func (c *e2eCallback) WorkDoneProgressCreate(
	_ context.Context, _ semanticapi.WorkDoneProgressCreateParams,
) error {
	return nil
}

func (c *e2eCallback) ApplyEdit(
	_ context.Context,
	_ semanticapi.ApplyWorkspaceEditParams,
) (semanticapi.ApplyWorkspaceEditResult, error) {
	return semanticapi.ApplyWorkspaceEditResult{
		Applied: true,
	}, nil
}

func (c *e2eCallback) WorkspaceFolders(
	_ context.Context,
) ([]semanticapi.WorkspaceFolder, error) {
	return nil, nil
}

func (c *e2eCallback) Configuration(
	_ context.Context, _ semanticapi.ConfigurationParams,
) ([]json.RawMessage, error) {
	return nil, nil
}

func (c *e2eCallback) RegisterCapability(
	_ context.Context, _ semanticapi.RegistrationParams,
) error {
	return nil
}

func (c *e2eCallback) UnregisterCapability(
	_ context.Context, _ semanticapi.UnregistrationParams,
) error {
	return nil
}

func (c *e2eCallback) CodeLensRefresh(_ context.Context) error {
	return nil
}

func (c *e2eCallback) SemanticTokensRefresh(_ context.Context) error {
	return nil
}

func (c *e2eCallback) InlayHintRefresh(_ context.Context) error {
	return nil
}

func (c *e2eCallback) DiagnosticRefresh(_ context.Context) error {
	return nil
}
func (c *e2eCallback) HandleNotification(_ context.Context, _ string, _ json.RawMessage) error {
	return nil
}

func (c *e2eCallback) FileDidChange(_ string, _ int32, _, _ bool) {}

func (c *e2eCallback) InvalidateAllPending() {}

func (c *e2eCallback) WaitFileProcessed(_ context.Context, _ string) error {
	return nil
}

// localScheme implements schemeapi.FileSystem and
// schemeapi.Executor using the local OS for e2e testing.
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

func (s *localScheme) Create(
	filename string,
) (workspaceapi.File, error) {
	return os.Create(filename)
}

func (s *localScheme) Open(
	filename string,
) (workspaceapi.File, error) {
	return os.Open(filename)
}

func (s *localScheme) OpenFile(
	filename string, flag int, perm os.FileMode,
) (workspaceapi.File, error) {
	return os.OpenFile(filename, flag, perm)
}

func (s *localScheme) Stat(
	filename string,
) (os.FileInfo, error) {
	return os.Stat(filename)
}

func (s *localScheme) Rename(
	oldpath, newpath string,
) error {
	return os.Rename(oldpath, newpath)
}

func (s *localScheme) Remove(
	filename string,
) error {
	return os.Remove(filename)
}

func (s *localScheme) Join(
	elem ...string,
) string {
	return filepath.Join(elem...)
}

func (s *localScheme) TempFile(
	dir, prefix string,
) (workspaceapi.File, error) {
	return os.CreateTemp(dir, prefix)
}

func (s *localScheme) Lstat(
	filename string,
) (os.FileInfo, error) {
	return os.Lstat(filename)
}

func (s *localScheme) Symlink(
	oldname, newname string,
) error {
	return os.Symlink(oldname, newname)
}

func (s *localScheme) Readlink(
	link string,
) (string, error) {
	return os.Readlink(link)
}

func (s *localScheme) ReadDir(
	path string,
) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

func (s *localScheme) MkdirAll(
	filename string, perm os.FileMode,
) error {
	return os.MkdirAll(filename, perm)
}

func (s *localScheme) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
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

func (s *localScheme) Signal(
	pid workspaceapi.Pid, sig syscall.Signal,
) error {
	s.mu.Lock()
	proc, ok := s.procs[pid]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("process %d not found", pid)
	}
	return proc.Signal(sig)
}

func (*localScheme) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (s *localScheme) Close() error {
	return nil
}

// readyOnProgress returns an onProgress callback that
// closes ready via the given sync.Once when a progress
// sequence completes (end event received).
func readyOnProgress(
	once *sync.Once, ready chan struct{},
) func(semanticapi.ProgressParams) {
	return func(p semanticapi.ProgressParams) {
		var v struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(p.Value, &v) != nil {
			return
		}
		if v.Kind == "end" {
			once.Do(func() { close(ready) })
		}
	}
}

// waitReady waits for ready to be closed or fails the test
// after 30 seconds.
func waitReady(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("gopls did not become ready within 30s")
	}
}
