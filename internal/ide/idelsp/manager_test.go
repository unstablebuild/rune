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
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestEventTypeClose_evictsFilesCache(t *testing.T) {
	t.Parallel()
	workspaceURI := makeURI(t, "file:///workspace")
	m := New(workspaceURI, nil, nil, nil, nil, nil,
		Config{NoInitializeServer: true})
	t.Cleanup(func() { _ = m.Close() })

	openURI := makeURI(t, "file:///workspace/open.go")
	m.mu.Lock()
	m.files[openURI.String()] = newFile(openURI, "package open\n", "go",
		serverKey{languageID: "go", rootURI: "file:///workspace"})
	m.mu.Unlock()

	require.Len(t, m.files, 1)

	err := m.handle(textapi.Event{
		Type: textapi.EventTypeClose,
		URI:  openURI,
	})
	require.NoError(t, err)

	m.mu.Lock()
	defer m.mu.Unlock()
	assert.Empty(t, m.files,
		"EventTypeClose must evict the cached file entry")
}

func TestPendingOpenIgnoresEditsAndRefreshesOnFlush(t *testing.T) {
	t.Parallel()
	workspaceURI := makeURI(t, "file:///workspace")
	m := New(workspaceURI, nil, nil, nil, nil, nil,
		Config{NoInitializeServer: true})
	t.Cleanup(func() { _ = m.Close() })

	openURI := makeURI(t, "file:///workspace/open.go")
	const opened = "package main\n\nfunc main() {}\n"
	require.NoError(t, m.handle(textapi.Event{
		Type:    textapi.EventTypeOpen,
		URI:     openURI,
		Content: opened,
	}))

	require.NoError(t, m.handle(textapi.Event{
		Type:    textapi.EventTypeEdit,
		URI:     openURI,
		Start:   term.Coordinates{Y: 2, X: 13},
		End:     term.Coordinates{Y: 2, X: 13},
		Content: "println(\"ready\")",
	}), "edit before server init must be swallowed, not logged as error")
	m.mu.Lock()
	pending := m.pendingOpens[openURI.String()]
	m.mu.Unlock()
	assert.Equal(t, opened, pending.Content,
		"edit before server init must not mutate the snapshot")

	const flushed = "package main\n\nfunc main() { println(\"ready\") }\n"
	require.NoError(t, m.handle(textapi.Event{
		Type:    textapi.EventTypeFlush,
		URI:     openURI,
		Content: flushed,
	}))
	m.mu.Lock()
	pending = m.pendingOpens[openURI.String()]
	m.mu.Unlock()
	assert.Equal(t, flushed, pending.Content,
		"flush carries the authoritative buffer and must refresh the snapshot")

	notOpenURI := makeURI(t, "file:///workspace/never_opened.go")
	require.Error(t, m.handle(textapi.Event{
		Type:    textapi.EventTypeFlush,
		URI:     notOpenURI,
		Content: "package main\n",
	}), "flush for a file with no pending open must still surface the error")
}

func TestManagerConcurrentStateAccess(t *testing.T) {
	t.Parallel()
	workspaceURI := makeURI(t, "file:///workspace")
	m := New(workspaceURI, nil, nil, nil, nil, nil,
		Config{NoInitializeServer: true})

	const workers = 8
	files := []workspaceapi.URI{
		makeURI(t, "file:///workspace/a.go"),
		makeURI(t, "file:///workspace/b.go"),
		makeURI(t, "file:///workspace/c.go"),
	}

	var wg sync.WaitGroup
	for i := range workers {
		uri := files[i%len(files)]
		wg.Go(func() {
			for range 50 {
				_ = m.handle(textapi.Event{Type: textapi.EventTypeOpen, URI: uri,
					Content: "package a\n"})
				m.mu.Lock()
				m.files[uri.String()] = newFile(uri, "package a\n", "go",
					serverKey{languageID: "go", rootURI: "file:///workspace"})
				m.mu.Unlock()
				_, _ = m.getFile(uri.String())
				_ = m.allServers()
				_ = m.handle(textapi.Event{Type: textapi.EventTypeClose, URI: uri})
			}
		})
	}

	wg.Go(func() {
		_ = m.Close()
	})

	wg.Wait()
}

func TestManagerMaxRetriesPropagatesFromConfig(t *testing.T) {
	uri := makeURI(t, "file:///workspace")

	t.Run("explicit value is propagated", func(t *testing.T) {
		m := New(uri, nil, nil, nil, nil, nil, Config{MaxRetries: 7})
		t.Cleanup(func() { _ = m.Close() })
		assert.Equal(t, uint(7), m.maxRetries)
	})

	t.Run("zero falls back to default", func(t *testing.T) {
		m := New(uri, nil, nil, nil, nil, nil, Config{})
		t.Cleanup(func() { _ = m.Close() })
		assert.Equal(t, uint(3), m.maxRetries)
	})
}

func TestDidChangeWatchedFiles_invalidatesOnDelete(t *testing.T) {
	t.Parallel()
	uri := makeURI(t, "file:///workspace")

	t.Run("deletion event triggers InvalidateAllPending", func(t *testing.T) {
		t.Parallel()
		callback := &testCallback{}
		m := New(uri, nil, nil, nil, nil, nil,
			Config{Callback: callback, NoInitializeServer: true})
		t.Cleanup(func() { _ = m.Close() })

		err := m.DidChangeWatchedFiles(context.Background(),
			semanticapi.DidChangeWatchedFilesParams{
				Changes: []semanticapi.FileEvent{
					{URI: "file:///workspace/a.go", Type: semanticapi.FileChangeTypeChanged},
					{URI: "file:///workspace/b.go", Type: semanticapi.FileChangeTypeDeleted},
				},
			})
		require.NoError(t, err)

		callback.mu.Lock()
		got := callback.invalidateAllPendingCount
		callback.mu.Unlock()
		assert.Equal(t, 1, got)
	})

	t.Run("rename-as-delete+create triggers InvalidateAllPending", func(t *testing.T) {
		t.Parallel()
		callback := &testCallback{}
		m := New(uri, nil, nil, nil, nil, nil,
			Config{Callback: callback, NoInitializeServer: true})
		t.Cleanup(func() { _ = m.Close() })

		err := m.DidChangeWatchedFiles(context.Background(),
			semanticapi.DidChangeWatchedFilesParams{
				Changes: []semanticapi.FileEvent{
					{URI: "file:///workspace/old.go", Type: semanticapi.FileChangeTypeDeleted},
					{URI: "file:///workspace/new.go", Type: semanticapi.FileChangeTypeCreated},
				},
			})
		require.NoError(t, err)

		callback.mu.Lock()
		got := callback.invalidateAllPendingCount
		callback.mu.Unlock()
		assert.Equal(t, 1, got)
	})

	t.Run("add+change only does not trigger InvalidateAllPending", func(t *testing.T) {
		t.Parallel()
		callback := &testCallback{}
		m := New(uri, nil, nil, nil, nil, nil,
			Config{Callback: callback, NoInitializeServer: true})
		t.Cleanup(func() { _ = m.Close() })

		err := m.DidChangeWatchedFiles(context.Background(),
			semanticapi.DidChangeWatchedFilesParams{
				Changes: []semanticapi.FileEvent{
					{URI: "file:///workspace/a.go", Type: semanticapi.FileChangeTypeCreated},
					{URI: "file:///workspace/b.go", Type: semanticapi.FileChangeTypeChanged},
				},
			})
		require.NoError(t, err)

		callback.mu.Lock()
		got := callback.invalidateAllPendingCount
		callback.mu.Unlock()
		assert.Equal(t, 0, got)
	})
}

func TestDidChangeWatchedFiles_marksOpenFilePending(t *testing.T) {
	t.Parallel()
	uri := makeURI(t, "file:///workspace")
	callback := &testCallback{}
	m := New(uri, nil, nil, nil, nil, nil,
		Config{Callback: callback, NoInitializeServer: true})
	t.Cleanup(func() { _ = m.Close() })

	// Simulate the file being open in the editor.
	openURI := makeURI(t, "file:///workspace/open.go")
	m.mu.Lock()
	m.files[openURI.String()] = newFile(openURI, "package open\n", "go",
		serverKey{languageID: "go", rootURI: "file:///workspace"})
	m.mu.Unlock()

	// An apply_patch-style Changed event arrives for the open file.
	err := m.DidChangeWatchedFiles(context.Background(),
		semanticapi.DidChangeWatchedFilesParams{
			Changes: []semanticapi.FileEvent{
				{URI: openURI.String(), Type: semanticapi.FileChangeTypeChanged},
			},
		})
	require.NoError(t, err)

	// The callback must have received an out-of-band change marked
	// as open so a subsequent WaitFileProcessed blocks for a newer
	// version rather than releasing on a stale push.
	callback.mu.Lock()
	got := callback.fileDidChangeCalls
	callback.mu.Unlock()
	require.Len(t, got, 1)
	assert.Equal(t, openURI.String(), got[0].uri)
	assert.True(t, got[0].open)
	assert.True(t, got[0].oob)
}

// filesystem with a single fake python server rooted at rootPath.
func newTransientTestManager(
	t *testing.T, rootPath string,
) (*Manager, *fakeChild) {
	t.Helper()
	rootURI := "file://" + rootPath
	uri := makeURI(t, rootURI)
	m := New(uri, newTestScheme(), nil, nil, nil, nil,
		Config{NoInitializeServer: true})
	t.Cleanup(func() { _ = m.Close() })

	srv := &fakeChild{childName: "python"}
	m.mu.Lock()
	m.servers[serverKey{languageID: "python", rootURI: rootURI}] = srv
	m.mu.Unlock()
	return m, srv
}

func TestTransientOpenForUnopenedFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "mod.py")
	require.NoError(t, os.WriteFile(filePath, []byte("x = 1"), 0o644))
	fileURI := "file://" + filePath

	m, srv := newTransientTestManager(t, tmpDir)

	_, err := m.Definition(context.Background(), semanticapi.DefinitionParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"notify:textDocument/didOpen",
		"call:textDocument/definition",
		"notify:textDocument/didClose",
	}, srv.eventLog(), "didOpen must precede the call and didClose must follow")

	opens := srv.didOpens()
	require.Len(t, opens, 1)
	assert.Equal(t, fileURI, opens[0].TextDocument.URI)
	assert.Equal(t, "python", opens[0].TextDocument.LanguageID)
	assert.Equal(t, int32(firstFileVersion), opens[0].TextDocument.Version)
	assert.Equal(t, "x = 1\n", opens[0].TextDocument.Text)

	m.mu.Lock()
	_, cached := m.files[fileURI]
	m.mu.Unlock()
	assert.False(t, cached, "transient open must not cache the file in m.files")
}

func TestTypeScriptDialectsShareServerWithPerFileLanguageID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		file      string
		transient bool
		wantDocID string
	}{
		{"index.ts", false, "typescript"},
		{"App.tsx", false, "typescriptreact"},
		{"index.js", false, "javascript"},
		{"App.jsx", false, "javascriptreact"},
		{"lib.mts", true, "typescript"},
		{"Widget.tsx", true, "typescriptreact"},
		{"lib.cjs", true, "javascript"},
		{"Widget.jsx", true, "javascriptreact"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()
			tmpDir := t.TempDir()
			filePath := filepath.Join(tmpDir, tt.file)
			require.NoError(t, os.WriteFile(filePath, []byte("export {}\n"), 0o644))
			fileURI := "file://" + filePath
			rootURI := "file://" + tmpDir

			m := New(makeURI(t, rootURI), newTestScheme(), nil, nil, nil, nil,
				Config{NoInitializeServer: true})
			t.Cleanup(func() { _ = m.Close() })
			srv := &fakeChild{childName: "typescript", rootURI: rootURI}
			m.mu.Lock()
			m.servers[serverKey{languageID: "typescript", rootURI: rootURI}] = srv
			m.mu.Unlock()

			if tt.transient {
				_, err := m.Hover(context.Background(), semanticapi.HoverParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
				})
				require.NoError(t, err)
			} else {
				require.NoError(t, m.handle(textapi.Event{
					Type:    textapi.EventTypeOpen,
					URI:     makeURI(t, fileURI),
					Content: "export {}\n",
				}))
			}

			opens := srv.didOpens()
			require.Len(t, opens, 1)
			assert.Equal(t, fileURI, opens[0].TextDocument.URI)
			assert.Equal(t, tt.wantDocID, opens[0].TextDocument.LanguageID)
		})
	}
}

func TestLocationRequestDecodesSingleLocation(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "mod.py")
	require.NoError(t, os.WriteFile(filePath, []byte("x = 1"), 0o644))
	fileURI := "file://" + filePath

	m, srv := newTransientTestManager(t, tmpDir)
	srv.callResult = json.RawMessage(`{"uri":"` + fileURI + `",` +
		`"range":{"start":{"line":3,"character":7},` +
		`"end":{"line":3,"character":10}}}`)

	res, err := m.Definition(context.Background(), semanticapi.DefinitionParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
	})
	require.NoError(t, err)
	require.NotNil(t, res.Location)
	assert.Equal(t, fileURI, res.Location.URI)
	assert.Equal(t, uint32(3), res.Location.Range.Start.Line)
	assert.Equal(t, uint32(7), res.Location.Range.Start.Character)
}

func TestNoTransientOpenForOpenFile(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "mod.py")
	require.NoError(t, os.WriteFile(filePath, []byte("x = 1"), 0o644))
	fileURI := "file://" + filePath

	m, srv := newTransientTestManager(t, tmpDir)
	key := serverKey{languageID: "python", rootURI: "file://" + tmpDir}
	uri := makeURI(t, fileURI)
	m.mu.Lock()
	m.files[fileURI] = newFile(uri, "x = 1\n", "python", key)
	m.mu.Unlock()

	_, err := m.Definition(context.Background(), semanticapi.DefinitionParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"call:textDocument/definition"}, srv.eventLog(),
		"an already-open file must not trigger didOpen/didClose")
}

func TestTransientOpenReadErrorSurfaces(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	fileURI := "file://" + filepath.Join(tmpDir, "missing.py")

	m, srv := newTransientTestManager(t, tmpDir)

	_, err := m.Definition(context.Background(), semanticapi.DefinitionParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
	})
	require.Error(t, err)
	assert.Empty(t, srv.eventLog(),
		"a read error must fail before any didOpen or call")
}

func TestTransientOpenNoServerPreservesErrNoServer(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	goURI := "file://" + filepath.Join(tmpDir, "main.go")

	m, _ := newTransientTestManager(t, tmpDir)

	_, err := m.Definition(context.Background(), semanticapi.DefinitionParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: goURI},
	})
	require.ErrorIs(t, err, ErrNoServer)
}

func TestTransientOpenWrapsDirectCallMethods(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "mod.py")
	require.NoError(t, os.WriteFile(filePath, []byte("x = 1"), 0o644))
	fileURI := "file://" + filePath

	m, srv := newTransientTestManager(t, tmpDir)

	_, err := m.Hover(context.Background(), semanticapi.HoverParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"notify:textDocument/didOpen",
		"call:textDocument/hover",
		"notify:textDocument/didClose",
	}, srv.eventLog())
}

func TestTransientOpenWrapsDocumentSymbol(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "mod.py")
	require.NoError(t, os.WriteFile(filePath, []byte("x = 1"), 0o644))
	fileURI := "file://" + filePath

	m, srv := newTransientTestManager(t, tmpDir)

	_, err := m.DocumentSymbol(context.Background(), semanticapi.DocumentSymbolParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"notify:textDocument/didOpen",
		"call:textDocument/documentSymbol",
		"notify:textDocument/didClose",
	}, srv.eventLog())

	m.mu.Lock()
	_, cached := m.files[fileURI]
	m.mu.Unlock()
	assert.False(t, cached, "transient open must not cache the file in m.files")
}

func TestDiagnosticSettleTimeoutDoesNotFail(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "mod.py")
	require.NoError(t, os.WriteFile(filePath, []byte("x = 1"), 0o644))
	fileURI := "file://" + filePath
	rootURI := "file://" + tmpDir

	cb := NewCallbackHandler(nil, nil, nil, nil, caseFS{}, rootURI,
		CallbackHandlerConfig{ScheduleNextTick: func(func()) bool { return true }})
	uri := makeURI(t, rootURI)
	m := New(uri, newTestScheme(), nil, nil, nil, nil,
		Config{NoInitializeServer: true, Callback: cb})
	t.Cleanup(func() { _ = m.Close() })
	srv := &fakeChild{childName: "python"}
	m.mu.Lock()
	m.servers[serverKey{languageID: "python", rootURI: rootURI}] = srv
	m.mu.Unlock()

	// The file must be open in the editor for the settle wait to run at
	// all; an unopened file skips it (see
	// TestDiagnosticUnopenedFileSkipsSettleWait).
	fileURIParsed := makeURI(t, fileURI)
	m.mu.Lock()
	m.files[fileURI] = newFile(fileURIParsed, "x = 1\n", "python",
		serverKey{languageID: "python", rootURI: rootURI})
	m.mu.Unlock()

	// Track a pending version the server will never acknowledge with a
	// publishDiagnostics push, so WaitFileProcessed would block.
	cb.FileDidChange(fileURI, 1, true, false)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	_, err := m.Diagnostic(ctx, semanticapi.DocumentDiagnosticParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
	})
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 10*time.Second,
		"Diagnostic must not block on the full caller deadline waiting "+
			"for a publishDiagnostics that never arrives")
	assert.Contains(t, srv.eventLog(), "call:textDocument/diagnostic",
		"the pull request must still be issued after the settle wait")
}

func TestDiagnosticUnopenedFileSkipsSettleWait(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "mod.py")
	require.NoError(t, os.WriteFile(filePath, []byte("x = 1"), 0o644))
	fileURI := "file://" + filePath
	rootURI := "file://" + tmpDir

	cb := NewCallbackHandler(nil, nil, nil, nil, caseFS{}, rootURI,
		CallbackHandlerConfig{ScheduleNextTick: func(func()) bool { return true }})
	uri := makeURI(t, rootURI)
	m := New(uri, newTestScheme(), nil, nil, nil, nil,
		Config{NoInitializeServer: true, Callback: cb})
	t.Cleanup(func() { _ = m.Close() })
	srv := &fakeChild{childName: "python"}
	m.mu.Lock()
	m.servers[serverKey{languageID: "python", rootURI: rootURI}] = srv
	m.mu.Unlock()

	// Mark a pending version the server will never acknowledge. Because
	// the file is not open, the settle wait must be skipped entirely
	// rather than waiting out diagnosticSettleTimeout.
	cb.FileDidChange(fileURI, 1, true, false)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := time.Now()
	_, err := m.Diagnostic(ctx, semanticapi.DocumentDiagnosticParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
	})
	require.NoError(t, err)
	assert.Less(t, time.Since(start), diagnosticSettleTimeout,
		"Diagnostic for an unopened file must not pay the settle wait")
	// Transient open/close must bracket the pull, and the pull must run.
	assert.Equal(t, []string{
		"notify:textDocument/didOpen",
		"call:textDocument/diagnostic",
		"notify:textDocument/didClose",
	}, srv.eventLog())
}

func TestDiagnosticPropagatesCancellation(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "mod.py")
	require.NoError(t, os.WriteFile(filePath, []byte("x = 1"), 0o644))
	fileURI := "file://" + filePath
	rootURI := "file://" + tmpDir

	cb := NewCallbackHandler(nil, nil, nil, nil, caseFS{}, rootURI,
		CallbackHandlerConfig{ScheduleNextTick: func(func()) bool { return true }})
	uri := makeURI(t, rootURI)
	m := New(uri, newTestScheme(), nil, nil, nil, nil,
		Config{NoInitializeServer: true, Callback: cb})
	t.Cleanup(func() { _ = m.Close() })
	srv := &fakeChild{childName: "python"}
	m.mu.Lock()
	m.servers[serverKey{languageID: "python", rootURI: rootURI}] = srv
	m.mu.Unlock()

	cb.FileDidChange(fileURI, 1, true, false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.Diagnostic(ctx, semanticapi.DocumentDiagnosticParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestAnyServerRunning(t *testing.T) {
	t.Parallel()

	newManager := func(t *testing.T) *Manager {
		t.Helper()
		uri := makeURI(t, "file:///workspace")
		m := New(uri, newTestScheme(), nil, nil, nil, nil,
			Config{NoInitializeServer: true})
		t.Cleanup(func() { _ = m.Close() })
		return m
	}

	t.Run("no servers: false", func(t *testing.T) {
		t.Parallel()
		m := newManager(t)
		assert.False(t, m.AnyServerRunning())
	})

	t.Run("alive server: true", func(t *testing.T) {
		t.Parallel()
		m := newManager(t)
		m.mu.Lock()
		m.servers[serverKey{languageID: "python", rootURI: "file:///workspace"}] =
			&fakeChild{childName: "python", alive: true}
		m.mu.Unlock()
		assert.True(t, m.AnyServerRunning())
	})

	t.Run("only dead servers: false", func(t *testing.T) {
		t.Parallel()
		m := newManager(t)
		m.mu.Lock()
		m.servers[serverKey{languageID: "python", rootURI: "file:///workspace"}] =
			&fakeChild{childName: "python", alive: false}
		m.mu.Unlock()
		assert.False(t, m.AnyServerRunning())
	})
}

// pullClientCaps is the client half of the pull-diagnostics gate: raw
// InitializeParams.Capabilities advertising textDocument.diagnostic.
var pullClientCaps = json.RawMessage(`{"textDocument":{"diagnostic":{}}}`)

// pullServerCaps is the server half of the gate: an initialize result
// advertising diagnosticProvider.
func pullServerCaps() semanticapi.InitializeResult {
	return semanticapi.InitializeResult{
		Capabilities: semanticapi.ServerCapabilities{
			DiagnosticProvider: &semanticapi.DiagnosticOptions{},
		},
	}
}

const testPullDebounce = 30 * time.Millisecond

// newPullTestManager builds a Manager rooted at a temp dir with srv
// registered as the python backend and a short pull debounce, and
// returns the manager, the recording callback and a python file URI
// inside the root.
func newPullTestManager(
	t *testing.T, srv *fakeChild,
) (*Manager, *testCallback, string) {
	t.Helper()
	tmpDir := t.TempDir()
	rootURI := "file://" + tmpDir
	filePath := filepath.Join(tmpDir, "mod.py")
	require.NoError(t, os.WriteFile(filePath, []byte("x = 1\n"), 0o644))

	callback := &testCallback{}
	m := New(makeURI(t, rootURI), newTestScheme(), nil, nil, nil, nil,
		Config{
			NoInitializeServer:      true,
			Callback:                callback,
			PullDiagnosticsDebounce: testPullDebounce,
		})
	t.Cleanup(func() { _ = m.Close() })

	srv.rootURI = rootURI
	m.mu.Lock()
	m.servers[serverKey{languageID: "python", rootURI: rootURI}] = srv
	m.mu.Unlock()
	return m, callback, "file://" + filePath
}

// openPullFile drives the open event so the file is tracked in m.files
// and waits for the open-triggered pull to settle.
func openPullFile(t *testing.T, m *Manager, fileURI string) {
	t.Helper()
	require.NoError(t, m.handle(textapi.Event{
		Type:    textapi.EventTypeOpen,
		URI:     makeURI(t, fileURI),
		Content: "x = 1\n",
	}))
}

func pullCount(srv *fakeChild) int {
	n := 0
	for _, c := range srv.callMethods() {
		if c == "textDocument/diagnostic" {
			n++
		}
	}
	return n
}

func publishedDiagnostics(cb *testCallback) []publishedDiagnostic {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return append([]publishedDiagnostic(nil), cb.publishes...)
}

func TestPullDiagnosticsBridge_PublishesReport(t *testing.T) {
	t.Parallel()
	diag := semanticapi.Diagnostic{
		Range: semanticapi.Range{
			Start: semanticapi.Position{Line: 3, Character: 4},
			End:   semanticapi.Position{Line: 3, Character: 9},
		},
		Severity: semanticapi.DiagnosticSeverityError,
		Source:   "rust-analyzer",
		Message:  "expected u32, found &str",
	}
	srv := &fakeChild{
		childName:  "python",
		clientCaps: pullClientCaps,
		initRes:    pullServerCaps(),
		diagReport: semanticapi.DocumentDiagnosticReport{
			Kind:  "full",
			Items: []semanticapi.Diagnostic{diag},
		},
	}
	m, cb, fileURI := newPullTestManager(t, srv)
	openPullFile(t, m, fileURI)

	require.Eventually(t, func() bool {
		return len(publishedDiagnostics(cb)) > 0
	}, time.Second, 5*time.Millisecond,
		"open must schedule a pull and republish its report")

	got := publishedDiagnostics(cb)[0]
	assert.Equal(t, fileURI, got.params.URI)
	assert.Equal(t, []semanticapi.Diagnostic{diag}, got.params.Diagnostics)
	assert.Equal(t, int32(firstFileVersion), got.params.Version,
		"the publish must carry the file's current version so the "+
			"callback's version tracking settles")
	assert.Equal(t, "python:pull", got.metadata.ServerName,
		"the bridge must publish under its own server slot")
	assert.Equal(t, srv.key().rootURI, got.metadata.RootURI)
}

func TestPullDiagnosticsBridge_DebouncesEdits(t *testing.T) {
	t.Parallel()
	srv := &fakeChild{
		childName:  "python",
		clientCaps: pullClientCaps,
		initRes:    pullServerCaps(),
		diagReport: semanticapi.DocumentDiagnosticReport{Kind: "full"},
	}
	m, cb, fileURI := newPullTestManager(t, srv)
	openPullFile(t, m, fileURI)
	require.Eventually(t, func() bool {
		return pullCount(srv) == 1
	}, time.Second, 5*time.Millisecond)

	const edits = 5
	for range edits {
		require.NoError(t, m.handle(textapi.Event{
			Type:    textapi.EventTypeEdit,
			URI:     makeURI(t, fileURI),
			Content: "y",
		}))
	}
	require.Eventually(t, func() bool {
		return pullCount(srv) == 2
	}, time.Second, 5*time.Millisecond,
		"a burst of edits must collapse into one additional pull")
	time.Sleep(4 * testPullDebounce)
	assert.Equal(t, 2, pullCount(srv),
		"no further pulls may fire after the burst settles")

	// The publish must reflect the version the edits produced, not the
	// version at the time the timer was armed.
	publishes := publishedDiagnostics(cb)
	require.NotEmpty(t, publishes)
	assert.Equal(t, int32(firstFileVersion+edits),
		publishes[len(publishes)-1].params.Version)
}

func TestPullDiagnosticsBridge_SaveTriggersPull(t *testing.T) {
	t.Parallel()
	srv := &fakeChild{
		childName:  "python",
		clientCaps: pullClientCaps,
		initRes:    pullServerCaps(),
		diagReport: semanticapi.DocumentDiagnosticReport{Kind: "full"},
	}
	m, _, fileURI := newPullTestManager(t, srv)
	openPullFile(t, m, fileURI)
	require.Eventually(t, func() bool {
		return pullCount(srv) == 1
	}, time.Second, 5*time.Millisecond)

	require.NoError(t, m.handle(textapi.Event{
		Type:    textapi.EventTypeFlush,
		URI:     makeURI(t, fileURI),
		Content: "x = 2\n",
	}))
	require.Eventually(t, func() bool {
		return pullCount(srv) == 2
	}, time.Second, 5*time.Millisecond)
}

func TestPullDiagnosticsBridge_Gates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		clientCaps json.RawMessage
		initRes    semanticapi.InitializeResult
	}{
		{
			name:       "server omits diagnosticProvider",
			clientCaps: pullClientCaps,
			initRes:    semanticapi.InitializeResult{},
		},
		{
			name:       "client omits textDocument.diagnostic",
			clientCaps: json.RawMessage(`{"textDocument":{"hover":{}}}`),
			initRes:    pullServerCaps(),
		},
		{
			name:    "no capabilities at all",
			initRes: semanticapi.InitializeResult{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			srv := &fakeChild{
				childName:  "python",
				clientCaps: tt.clientCaps,
				initRes:    tt.initRes,
				diagReport: semanticapi.DocumentDiagnosticReport{Kind: "full"},
			}
			m, cb, fileURI := newPullTestManager(t, srv)
			openPullFile(t, m, fileURI)
			time.Sleep(4 * testPullDebounce)
			assert.Zero(t, pullCount(srv),
				"a server without the full handshake must never be pulled")
			assert.Empty(t, publishedDiagnostics(cb))
		})
	}
}

func TestPullDiagnosticsBridge_UnchangedReportSkipsPublish(t *testing.T) {
	t.Parallel()
	srv := &fakeChild{
		childName:  "python",
		clientCaps: pullClientCaps,
		initRes:    pullServerCaps(),
		diagReport: semanticapi.DocumentDiagnosticReport{
			Kind: "unchanged", ResultID: "abc",
		},
	}
	m, cb, fileURI := newPullTestManager(t, srv)
	openPullFile(t, m, fileURI)
	require.Eventually(t, func() bool {
		return pullCount(srv) == 1
	}, time.Second, 5*time.Millisecond)
	time.Sleep(2 * testPullDebounce)
	assert.Empty(t, publishedDiagnostics(cb),
		"an unchanged report must leave the cached diagnostics alone")
}

func TestPullDiagnosticsBridge_RefreshRePullsOpenFiles(t *testing.T) {
	t.Parallel()
	srv := &fakeChild{
		childName:  "python",
		clientCaps: pullClientCaps,
		initRes:    pullServerCaps(),
		diagReport: semanticapi.DocumentDiagnosticReport{Kind: "full"},
	}
	m, cb, fileURI := newPullTestManager(t, srv)
	openPullFile(t, m, fileURI)
	require.Eventually(t, func() bool {
		return pullCount(srv) == 1
	}, time.Second, 5*time.Millisecond)

	require.NoError(t, m.callback.DiagnosticRefresh(t.Context()))
	require.Eventually(t, func() bool {
		return pullCount(srv) == 2
	}, time.Second, 5*time.Millisecond)

	cb.mu.Lock()
	forwarded := cb.diagnosticRefreshCount
	cb.mu.Unlock()
	assert.Equal(t, 1, forwarded,
		"the decorator must forward the refresh to the configured callback")
}

func TestPullDiagnosticsBridge_CloseCancelsPending(t *testing.T) {
	t.Parallel()
	srv := &fakeChild{
		childName:  "python",
		clientCaps: pullClientCaps,
		initRes:    pullServerCaps(),
		diagReport: semanticapi.DocumentDiagnosticReport{Kind: "full"},
	}
	m, _, fileURI := newPullTestManager(t, srv)
	require.NoError(t, m.handle(textapi.Event{
		Type:    textapi.EventTypeOpen,
		URI:     makeURI(t, fileURI),
		Content: "x = 1\n",
	}))
	require.NoError(t, m.handle(textapi.Event{
		Type: textapi.EventTypeClose,
		URI:  makeURI(t, fileURI),
	}))
	time.Sleep(4 * testPullDebounce)
	assert.Zero(t, pullCount(srv),
		"closing the document must cancel the pending pull")
}
