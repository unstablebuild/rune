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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// findClangdBin locates the clangd binary the e2e tests run, in the
// order resolveClangd probes the host: well-known locations, then PATH.
// A missing clangd fails the test: the e2e suite exists to exercise the
// real server, so a dev machine without one is misconfigured.
func findClangdBin(t *testing.T) string {
	t.Helper()
	for _, p := range wellKnownClangdPaths {
		if strings.HasPrefix(p, "~/") {
			p = filepath.Join(os.Getenv("HOME"), p[2:])
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if bin, err := exec.LookPath("clangd"); err == nil {
		return bin
	}
	t.Fatal("clangd not found; install LLVM or the system toolchain to run the e2e tests")
	return ""
}

// setupWorkspace copies the testdata project at src into a fresh temp
// directory, templating the compilation databases with its path since
// clangd needs absolute entries, and returns the path.
func setupWorkspace(t *testing.T, src string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ccpp-ext-e2e-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	// macOS hands out a symlinked temp root; clangd reports real paths.
	dir, err = filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	require.NoError(t, filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if filepath.Base(path) == compileCommandsName {
			data = bytes.ReplaceAll(data, []byte("@ROOT@"), []byte(dir))
		}
		return os.WriteFile(dst, data, 0o644)
	}))
	return dir
}

// ccppCallback implements idelsp.Callback for the e2e tests, recording
// published diagnostics and the workspace edits clangd asks the client to
// apply, which is how its tweaks land.
type ccppCallback struct {
	mu          sync.Mutex
	diagnostics []semanticapi.PublishDiagnosticsParams
	applied     []semanticapi.ApplyWorkspaceEditParams
}

func (c *ccppCallback) ShowMessage(_ context.Context, _ semanticapi.ShowMessageParams) error {
	return nil
}

func (c *ccppCallback) LogMessage(_ context.Context, _ semanticapi.LogMessageParams) error {
	return nil
}

func (c *ccppCallback) PublishDiagnostics(
	_ context.Context, params semanticapi.PublishDiagnosticsParams,
) error {
	c.mu.Lock()
	c.diagnostics = append(c.diagnostics, params)
	c.mu.Unlock()
	return nil
}

func (c *ccppCallback) Progress(_ context.Context, _ semanticapi.ProgressParams) error {
	return nil
}

func (c *ccppCallback) LogTrace(_ context.Context, _ semanticapi.LogTraceParams) error { return nil }

func (c *ccppCallback) ShowDocument(
	_ context.Context, _ semanticapi.ShowDocumentParams,
) (semanticapi.ShowDocumentResult, error) {
	return semanticapi.ShowDocumentResult{Success: true}, nil
}

func (c *ccppCallback) ShowMessageRequest(
	_ context.Context, _ semanticapi.ShowMessageRequestParams,
) (*semanticapi.MessageActionItem, error) {
	return nil, nil
}

func (c *ccppCallback) WorkDoneProgressCreate(
	_ context.Context, _ semanticapi.WorkDoneProgressCreateParams,
) error {
	return nil
}

func (c *ccppCallback) ApplyEdit(
	_ context.Context, params semanticapi.ApplyWorkspaceEditParams,
) (semanticapi.ApplyWorkspaceEditResult, error) {
	c.mu.Lock()
	c.applied = append(c.applied, params)
	c.mu.Unlock()
	return semanticapi.ApplyWorkspaceEditResult{Applied: true}, nil
}

func (c *ccppCallback) WorkspaceFolders(_ context.Context) ([]semanticapi.WorkspaceFolder, error) {
	return nil, nil
}

func (c *ccppCallback) Configuration(
	_ context.Context, params semanticapi.ConfigurationParams,
) ([]json.RawMessage, error) {
	result := make([]json.RawMessage, len(params.Items))
	for i := range result {
		result[i] = json.RawMessage(`null`)
	}
	return result, nil
}

func (c *ccppCallback) RegisterCapability(_ context.Context, _ semanticapi.RegistrationParams) error {
	return nil
}

func (c *ccppCallback) UnregisterCapability(
	_ context.Context, _ semanticapi.UnregistrationParams,
) error {
	return nil
}

func (c *ccppCallback) CodeLensRefresh(_ context.Context) error       { return nil }
func (c *ccppCallback) SemanticTokensRefresh(_ context.Context) error { return nil }
func (c *ccppCallback) InlayHintRefresh(_ context.Context) error      { return nil }
func (c *ccppCallback) DiagnosticRefresh(_ context.Context) error     { return nil }
func (c *ccppCallback) FileDidChange(_ string, _ int32, _, _ bool)    {}
func (c *ccppCallback) InvalidateAllPending()                         {}
func (c *ccppCallback) WaitFileProcessed(_ context.Context, _ string) error {
	return nil
}

// PublishedDiagnostics hands the manager the latest publish for uri, the
// way the IDE's callback does, so clangd's quick fixes can be requested.
func (c *ccppCallback) PublishedDiagnostics(uri string) []semanticapi.Diagnostic {
	diags, _ := c.diagnosticsFor(uri)
	return diags
}

func (c *ccppCallback) HandleNotification(
	_ context.Context, _ string, _ json.RawMessage,
) error {
	return nil
}

// diagnosticsFor returns the most recent published diagnostics for uri.
func (c *ccppCallback) diagnosticsFor(uri string) ([]semanticapi.Diagnostic, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := len(c.diagnostics) - 1; i >= 0; i-- {
		if c.diagnostics[i].URI == uri {
			return c.diagnostics[i].Diagnostics, true
		}
	}
	return nil, false
}

// appliedEdits returns every workspace edit clangd asked the client to
// apply so far.
func (c *ccppCallback) appliedEdits() []semanticapi.ApplyWorkspaceEditParams {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]semanticapi.ApplyWorkspaceEditParams(nil), c.applied...)
}

// localScheme implements schemeapi.FileSystem and schemeapi.Executor
// over the local OS, rooted at a directory so relative paths resolve
// into the test workspace.
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
	stderr *synchronizedBuffer
}

// startedProcesses returns every process started at path, oldest first.
func (s *localScheme) startedProcesses(path string) []startedProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []startedProcess
	for _, p := range s.started {
		if p.path == path {
			out = append(out, p)
		}
	}
	return out
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
	s.started = append(s.started, startedProcess{
		path: cmd.Path, args: append([]string{}, cmd.Args...), stderr: stderr,
	})
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
		return os.ErrProcessDone
	}
	return proc.Signal(sig)
}

func (*localScheme) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (s *localScheme) Close() error { return nil }

// stubPkgManager implements idelsp.PkgManager, resolving the language
// server directly to the discovered binary.
type stubPkgManager struct {
	bin string
}

func (p *stubPkgManager) LibDir(
	_ context.Context, _ string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice([]string{p.bin}), nil
}

// locateInFile returns the 0-based LSP position of ident on the first
// line containing marker, so fixture edits cannot desynchronize
// hard-coded coordinates.
func locateInFile(t *testing.T, path, marker, ident string) semanticapi.Position {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for i, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		col := strings.Index(line, ident)
		require.GreaterOrEqual(t, col, 0)
		return semanticapi.Position{Line: uint32(i), Character: uint32(col)}
	}
	t.Fatalf("could not find %q in %s", marker, path)
	return semanticapi.Position{}
}
