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

//go:build e2e

// ide-package tests that specifically test Go and its integration
// with the Go tooling (gopls and the editor/workspace LSP wiring).
package ide

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

func TestIDECheckFileErrorsWaitsForOpenFileResync(t *testing.T) {
	goplsBin := findGoplsForIDE(t)

	cleanContent := "package main\n\n" +
		"import \"fmt\"\n\n" +
		"func main() {\n\tfmt.Println(\"hello\")\n}\n"
	brokenContent := "package main\n\n" +
		"import \"fmt\"\n\n" +
		"func main() {\n\tfmt.Println(undefinedSymbol)\n}\n"

	x, ws, tmpDir := newExForLSPIntegration(t)

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "go.mod"),
		[]byte("module example.com/testmod\n\ngo 1.22\n"), 0o644))

	mainURI, err := ws.URI("main.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(mainURI.Path(), []byte(cleanContent), 0o644))

	rootURI, err := ws.URI(".")
	require.NoError(t, err)

	loaded := newLoadSignal()
	diagObserver := newDiagObserver()

	callbacks := idelsp.NewCallbackHandler(
		nil, nil, nil, nil, ws,
		rootURI.String(),
		idelsp.CallbackHandlerConfig{
			// Drop UI-bound work: the handler's notification and
			// editor dependencies are nil in this test; only its
			// version-tracking state matters here.
			ScheduleNextTick: func(func()) bool { return true },
		},
	)
	mgr := idelsp.New(
		rootURI,
		schemeForLSP(t, tmpDir),
		schemeForLSP(t, tmpDir),
		&goplsPkgManager{bin: goplsBin},
		nil, nil,
		idelsp.Config{
			Callback:   &observingLSPCallback{Callback: callbacks, load: loaded, diags: diagObserver},
			MaxRetries: 1,
		},
	)
	t.Cleanup(func() { _ = mgr.Close() })

	initOpts, err := json.Marshal(map[string]any{
		"langID":  "go",
		"command": "gopls serve",
	})
	require.NoError(t, err)
	_, err = mgr.Initialize(context.Background(), semanticapi.InitializeParams{
		RootURI:           rootURI.String(),
		InitializeOptions: initOpts,
	})
	require.NoError(t, err)

	require.NoError(t, x.comp.SubscribeEvents(idelsp.EditorEvents(), mgr))

	// Open the file through the real editor: this dispatches
	// EventTypeOpen to the subscribed Manager, which sends didOpen
	// and registers the file as open.
	require.NoError(t, x.editFiles(bgctx, mainURI.String()))
	x.waitInflight()

	// Wait for gopls to finish loading the package.
	require.True(t, loaded.wait(30*time.Second), "gopls did not finish loading")
	diagObserver.drain(2 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	baseline, err := mgr.Diagnostic(ctx, semanticapi.DocumentDiagnosticParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI.String()},
	})
	require.NoError(t, err)
	require.Empty(t, baseline.Items, "baseline should be clean")

	// An external tool rewrites the open, clean file on disk.
	require.NoError(t, os.WriteFile(mainURI.Path(), []byte(brokenContent), 0o644))

	// apply_patch's host effect: a synchronous
	// workspace/didChangeWatchedFiles for the still-open file. This
	// is exactly what the agent's apply_patch tool emits before it
	// runs check_file_errors, and it marks the open file as awaiting
	// a resync.
	require.NoError(t, mgr.DidChangeWatchedFiles(ctx,
		semanticapi.DidChangeWatchedFilesParams{
			Changes: []semanticapi.FileEvent{
				{URI: mainURI.String(), Type: semanticapi.FileChangeTypeChanged},
			},
		}))

	// Issue the diagnostics pull (the agent's check_file_errors) in
	// the gap before the editor reload sends its versioned didChange.
	// With the bug it returns the stale clean snapshot; with the fix
	// it blocks for the resync.
	type diagResult struct {
		report semanticapi.DocumentDiagnosticReport
		err    error
	}
	resCh := make(chan diagResult, 1)
	go func() {
		report, derr := mgr.Diagnostic(context.Background(),
			semanticapi.DocumentDiagnosticParams{
				TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI.String()},
			})
		resCh <- diagResult{report: report, err: derr}
	}()

	select {
	case r := <-resCh:
		t.Fatalf("Diagnostic returned before resync: items=%v err=%v",
			r.report.Items, r.err)
	case <-time.After(300 * time.Millisecond):
	}

	// Drive the real filesystem-watcher path: handleFSChange detects
	// the clean open buffer changed on disk and starts an async
	// reload, which rewrites the buffer from disk and dispatches a
	// versioned didChange (EventTypeEdit) to the Manager — the
	// genuine IDE reconcile that makes gopls re-typecheck the file.
	var mu sync.Mutex
	dispatchFilesystemEvent(x, &mu, vctrl.NopMatcher(false),
		testEventInfo{e: schemeapi.Write, u: mainURI})
	x.waitInflight()
	assertBufferContent(t, x, mainURI, brokenContent[:len(brokenContent)-1])

	select {
	case r := <-resCh:
		require.NoError(t, r.err)
		require.NotEmpty(t, r.report.Items,
			"Diagnostic must report the compile error after resync")
	case <-ctx.Done():
		t.Fatal("context cancelled waiting for diagnostics after resync")
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for diagnostics after resync")
	}
}

// newExForLSPIntegration builds a real ex over a fresh temp-dir
// workspace and returns it together with the workspace and temp dir
// so the caller can seed a Go module and drive filesystem events.
func newExForLSPIntegration(t *testing.T) (*ex, workspace.Workspace, string) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tempDir) })
	x, ws := newExForLSPIntegrationAt(t, tempDir, texttest.NopEditor())
	return x, ws, tempDir
}

// newExForLSPIntegrationAt builds a real ex over the workspace at dir,
// editing with ed.
func newExForLSPIntegrationAt(
	t *testing.T, dir string, ed text.Editor,
) (*ex, workspace.Workspace) {
	t.Helper()
	ctx := context.Background()

	uri, err := workspaceapi.ParseURI(filepath.Join("file://", dir))
	require.NoError(t, err)

	fileScheme, err := workspace.NewFileScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)

	ws := workspace.NewSchemeWorkspace(uri, fileScheme, inlineSchedule)
	e := newExForTestingTerminal(t, ws, ed,
		vte.DefaultConfig(), nopPublishEvent, plugin.DefaultBarConfig(),
		text.WithCommandKey(testCommandKey))
	t.Cleanup(func() {
		require.NoError(t, e.Close())
		require.NoError(t, fileScheme.Close())
	})
	return e.ex, ws
}

func schemeForLSP(t *testing.T, dir string) schemeapi.Scheme {
	t.Helper()
	uri, err := workspaceapi.ParseURI(filepath.Join("file://", dir))
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	return scheme
}

func findGoplsForIDE(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("gopls")
	if err == nil {
		return bin
	}
	for _, p := range []string{
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "gopls"),
		filepath.Join(os.Getenv("HOME"), "go", "bin", "gopls"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("gopls not found, skipping e2e test")
	return ""
}

type goplsPkgManager struct{ bin string }

func (p *goplsPkgManager) LibDir(
	_ context.Context, _ string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice([]string{p.bin}), nil
}

// loadSignal fires once when gopls reports it finished loading.
type loadSignal struct {
	once sync.Once
	ch   chan struct{}
}

func newLoadSignal() *loadSignal {
	return &loadSignal{ch: make(chan struct{})}
}

func (l *loadSignal) fire() { l.once.Do(func() { close(l.ch) }) }

func (l *loadSignal) wait(d time.Duration) bool {
	select {
	case <-l.ch:
		return true
	case <-time.After(d):
		return false
	}
}

// diagObserver buffers diagnostics pushes so the test can drain the
// initial load noise before asserting.
type diagObserver struct {
	ch chan struct{}
}

func newDiagObserver() *diagObserver {
	return &diagObserver{ch: make(chan struct{}, 64)}
}

func (d *diagObserver) notify() {
	select {
	case d.ch <- struct{}{}:
	default:
	}
}

func (d *diagObserver) drain(timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-d.ch:
		case <-timer.C:
			return
		}
	}
}

// observingLSPCallback wraps the real idelsp.Callback so the test can
// observe load progress and diagnostics while the embedded handler
// performs the real version tracking and WaitFileProcessed gating.
type observingLSPCallback struct {
	idelsp.Callback
	load  *loadSignal
	diags *diagObserver
}

func (c *observingLSPCallback) Progress(
	ctx context.Context, params semanticapi.ProgressParams,
) error {
	var v struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal(params.Value, &v) == nil && v.Kind == "end" {
		c.load.fire()
	}
	return c.Callback.Progress(ctx, params)
}

func (c *observingLSPCallback) ShowMessage(
	ctx context.Context, params semanticapi.ShowMessageParams,
) error {
	if strings.Contains(params.Message, "Finished loading packages") {
		c.load.fire()
	}
	return c.Callback.ShowMessage(ctx, params)
}

func (c *observingLSPCallback) PublishDiagnostics(
	ctx context.Context, params semanticapi.PublishDiagnosticsParams,
) error {
	err := c.Callback.PublishDiagnostics(ctx, params)
	c.diags.notify()
	return err
}
