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

package idelsp

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
)

type rustReadyCallback struct {
	testCallback
	ready chan struct{}
	once  sync.Once
}

func (c *rustReadyCallback) HandleNotification(_ context.Context, method string, params json.RawMessage) error {
	if method != "experimental/serverStatus" {
		return nil
	}
	var status struct {
		Quiescent bool   `json:"quiescent"`
		Health    string `json:"health"`
	}
	if err := json.Unmarshal(params, &status); err != nil {
		return err
	}
	if status.Quiescent && status.Health == "ok" {
		c.once.Do(func() { close(c.ready) })
	}
	return nil
}

func findRustAnalyzer(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("rust-analyzer")
	if err != nil {
		t.Skip("rust-analyzer not found, skipping rust e2e test")
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not found, skipping rust e2e test")
	}
	return bin
}

// rustPositions holds the 0-based LSP coordinates of the fixture symbols,
// computed from the on-disk file so the license header (or any edit to
// the fixture) cannot desynchronize the assertions.
type rustPositions struct {
	addFn   semanticapi.Position
	addCall semanticapi.Position
	greeter semanticapi.Position
}

// locateRustPositions scans main.rs for the fixture symbols and returns
// their 0-based positions. It points at the identifier within each line
// so requests resolve to the symbol rather than surrounding tokens.
func locateRustPositions(t *testing.T, path string) rustPositions {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")

	col := func(line, substr string) uint32 {
		i := strings.Index(line, substr)
		require.GreaterOrEqual(t, i, 0, "substr %q not in %q", substr, line)
		return uint32(i)
	}
	find := func(prefix, ident string) semanticapi.Position {
		for i, l := range lines {
			if strings.Contains(l, prefix) {
				return semanticapi.Position{Line: uint32(i), Character: col(l, ident)}
			}
		}
		t.Fatalf("could not find %q in %s", prefix, path)
		return semanticapi.Position{}
	}
	return rustPositions{
		addFn:   find("pub fn add(", "add("),
		addCall: find("let result = add(", "add("),
		greeter: find("let g = Greeter::new(", "g "),
	}
}

// setupRustManager drives a real rust-analyzer through the Manager
// against a cargo crate fixture, mirroring extension_rust's bring-up:
// langID "rust", command is the bare rust-analyzer path (a single argv
// element, since idelsp tokenizes command on spaces), and the
// rust-analyzer initialization options carry the cargo/procMacro/check
// settings. It self-skips when rust-analyzer or cargo are absent and
// waits for the workspace to finish loading before returning so request
// assertions run against an indexed project.
func setupRustManager(
	t *testing.T, ctx context.Context,
) (*Manager, string, rustPositions) {
	t.Helper()
	raBin := findRustAnalyzer(t)

	tmpDir := setupTestWorkspace(t, filepath.Join("testdata", "rs"))
	mainPath := filepath.Join(tmpDir, "src", "main.rs")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	mainURI := "file://" + mainPath
	pos := locateRustPositions(t, mainPath)

	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()
	callback := &rustReadyCallback{ready: make(chan struct{})}

	mgr := New(uri, scheme, scheme,
		&stubPkgManager{bin: raBin},
		nil, nil,
		Config{
			Callback:           callback,
			MaxRetries:         1,
			NoInitializeServer: true,
			InitializeTimeout:  30 * time.Second,
		})
	t.Cleanup(func() { _ = mgr.Close() })

	params := autoInitParams(uri.String())
	var capabilities map[string]any
	require.NoError(t, json.Unmarshal(params.Capabilities, &capabilities))
	capabilities["experimental"] = map[string]any{"serverStatusNotification": true}
	params.Capabilities, err = json.Marshal(capabilities)
	require.NoError(t, err)
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "rust",
		"command": raBin,
		"cargo": map[string]any{
			"buildScripts": map[string]any{"enable": true},
		},
		"procMacro": map[string]any{"enable": true},
		"check":     map[string]any{"command": "clippy"},
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	require.NoError(t, mgr.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
		TextDocument: semanticapi.TextDocumentItem{
			URI:        mainURI,
			LanguageID: "rust",
			Version:    0,
			Text:       string(mainContent),
		},
	}))

	select {
	case <-callback.ready:
	case <-ctx.Done():
		t.Fatalf("waiting for rust-analyzer quiescence: %v", ctx.Err())
	}
	waitForRustReady(t, ctx, mgr, mainURI, pos)
	return mgr, mainURI, pos
}

// waitForRustReady polls rust-analyzer until it answers the queries the
// subtests assert on, so request assertions are not racing the server's
// async startup. Definition alone is not enough: under load
// rust-analyzer resolves single-file definitions before the symbol
// index behind References/DocumentSymbol/WorkspaceSymbol is warm, and
// those requests return empty results rather than errors.
func waitForRustReady(
	t *testing.T, ctx context.Context, mgr *Manager, mainURI string, pos rustPositions,
) {
	t.Helper()
	containsLine := func(locs []semanticapi.Location, line uint32) bool {
		for _, l := range locs {
			if l.Range.Start.Line == line {
				return true
			}
		}
		return false
	}
	probes := []func() bool{
		func() bool {
			res, err := mgr.Definition(ctx, semanticapi.DefinitionParams{
				TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
				Position:     pos.addCall,
			})
			return err == nil && len(res.Locations) > 0
		},
		func() bool {
			locs, err := mgr.References(ctx, semanticapi.ReferenceParams{
				TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
				Position:     pos.addFn,
				Context:      semanticapi.ReferenceContext{IncludeDeclaration: true},
			})
			return err == nil && containsLine(locs, pos.addFn.Line) &&
				containsLine(locs, pos.addCall.Line)
		},
		func() bool {
			res, err := mgr.DocumentSymbol(ctx, semanticapi.DocumentSymbolParams{
				TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
			})
			return err == nil && len(symbolNames(res)) > 0
		},
		func() bool {
			syms, err := mgr.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{
				Query: "add",
			})
			return err == nil && len(syms) > 0
		},
		func() bool {
			hs, err := mgr.DocumentHighlight(ctx, semanticapi.DocumentHighlightParams{
				TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
				Position:     pos.addFn,
			})
			return err == nil && len(hs) > 0
		},
		func() bool {
			items, err := mgr.PrepareCallHierarchy(ctx, semanticapi.CallHierarchyPrepareParams{
				TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
				Position:     pos.addFn,
			})
			return err == nil && len(items) > 0
		},
		func() bool {
			tokens, err := mgr.SemanticTokensFull(ctx, semanticapi.SemanticTokensParams{
				TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
			})
			return err == nil && tokens != nil && len(tokens.Data) > 0
		},
	}
	deadline := time.Now().Add(90 * time.Second)
	next := 0
	for time.Now().Before(deadline) {
		for next < len(probes) && probes[next]() {
			next++
		}
		if next == len(probes) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled waiting for rust-analyzer: %v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for rust-analyzer readiness probe %d/%d", next+1, len(probes))
}

func TestE2ERust(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	mgr, mainURI, pos := setupRustManager(t, ctx)

	tests := []struct {
		name string
		fn   func(t *testing.T, mgr *Manager)
	}{
		{
			name: "Hover reports the add signature",
			fn: func(t *testing.T, mgr *Manager) {
				h, err := mgr.Hover(ctx, semanticapi.HoverParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     pos.addFn,
				})
				require.NoError(t, err)
				require.NotNil(t, h)
				assert.Contains(t, h.Contents.Value, "fn add")
			},
		},
		{
			name: "Definition resolves the add call to its declaration",
			fn: func(t *testing.T, mgr *Manager) {
				res, err := mgr.Definition(ctx, semanticapi.DefinitionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     pos.addCall,
				})
				require.NoError(t, err)
				require.NotEmpty(t, res.Locations)
				assert.True(t, strings.HasSuffix(res.Locations[0].URI, "/main.rs"))
				assert.Equal(t, pos.addFn.Line, res.Locations[0].Range.Start.Line)
			},
		},
		{
			name: "TypeDefinition resolves the Greeter binding to main.rs",
			fn: func(t *testing.T, mgr *Manager) {
				res, err := mgr.TypeDefinition(ctx, semanticapi.TypeDefinitionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     pos.greeter,
				})
				require.NoError(t, err)
				// rust-analyzer resolves the type of the `g` binding to the
				// Greeter struct declaration; older versions may return an
				// empty set for a local let binding, so only assert the
				// target when present.
				for _, l := range res.Locations {
					assert.True(t, strings.HasSuffix(l.URI, "/main.rs"))
				}
			},
		},
		{
			name: "References finds the add declaration and call",
			fn: func(t *testing.T, mgr *Manager) {
				locs, err := mgr.References(ctx, semanticapi.ReferenceParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     pos.addFn,
					Context:      semanticapi.ReferenceContext{IncludeDeclaration: true},
				})
				require.NoError(t, err)
				lines := make([]uint32, len(locs))
				for i, l := range locs {
					lines[i] = l.Range.Start.Line
				}
				assert.Contains(t, lines, pos.addFn.Line, "declaration line")
				assert.Contains(t, lines, pos.addCall.Line, "call line")
			},
		},
		{
			name: "DocumentSymbol lists the crate items",
			fn: func(t *testing.T, mgr *Manager) {
				res, err := mgr.DocumentSymbol(ctx, semanticapi.DocumentSymbolParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
				})
				require.NoError(t, err)
				names := symbolNames(res)
				assert.Contains(t, names, "Greeter")
				assert.Contains(t, names, "add")
				assert.Contains(t, names, "main")
			},
		},
		{
			name: "WorkspaceSymbol finds add",
			fn: func(t *testing.T, mgr *Manager) {
				syms, err := mgr.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{
					Query: "add",
				})
				require.NoError(t, err)
				require.NotEmpty(t, syms)
				found := false
				for _, s := range syms {
					if s.Name == "add" {
						found = true
					}
				}
				assert.True(t, found, "expected an 'add' workspace symbol")
			},
		},
		{
			name: "Completion offers Greeter members",
			fn: func(t *testing.T, mgr *Manager) {
				orig, err := os.ReadFile(strings.TrimPrefix(mainURI, "file://"))
				require.NoError(t, err)
				t.Cleanup(func() {
					require.NoError(t, mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
						TextDocument: semanticapi.VersionedTextDocumentIdentifier{
							URI: mainURI, Version: 6,
						},
						ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
							{Text: string(orig)},
						},
					}))
					// didChange delivery does not wait for the restored document's analysis.
					waitForRustReady(t, ctx, mgr, mainURI, pos)
				})
				// Put the member-access probe on its own line so the
				// completion position is a deterministic column right
				// after `g.`.
				probe := string(orig) +
					"\nfn _probe() {\n    let g = Greeter::new(\"x\");\n    g.\n}\n"
				require.NoError(t, mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
					TextDocument: semanticapi.VersionedTextDocumentIdentifier{
						URI: mainURI, Version: 5,
					},
					ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
						{Text: probe},
					},
				}))
				// orig ends with a newline, so its split yields a trailing
				// empty element; the `g.` line is two lines below the
				// `fn _probe() {` line that starts at that index.
				baseLine := uint32(len(strings.Split(string(orig), "\n")))
				memberLine := baseLine + 2
				res, err := mgr.Completion(ctx, semanticapi.CompletionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     semanticapi.Position{Line: memberLine, Character: 6},
				})
				require.NoError(t, err)
				labels := make([]string, len(res.Items))
				for i, it := range res.Items {
					labels[i] = it.Label
				}
				assert.Contains(t, labels, "greet")
			},
		},
		{
			name: "Formatting returns rustfmt edits or none",
			fn: func(t *testing.T, mgr *Manager) {
				_, err := mgr.Formatting(ctx, semanticapi.DocumentFormattingParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Options:      semanticapi.FormattingOptions{TabSize: 4, InsertSpaces: true},
				})
				require.NoError(t, err)
			},
		},
		{
			name: "FoldingRange covers the impl block",
			fn: func(t *testing.T, mgr *Manager) {
				ranges, err := mgr.FoldingRange(ctx, semanticapi.FoldingRangeParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
				})
				require.NoError(t, err)
				assert.NotEmpty(t, ranges)
			},
		},
		{
			name: "DocumentHighlight marks add occurrences",
			fn: func(t *testing.T, mgr *Manager) {
				hs, err := mgr.DocumentHighlight(ctx, semanticapi.DocumentHighlightParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     pos.addFn,
				})
				require.NoError(t, err)
				assert.NotEmpty(t, hs)
			},
		},
		{
			name: "PrepareCallHierarchy resolves add",
			fn: func(t *testing.T, mgr *Manager) {
				items, err := mgr.PrepareCallHierarchy(ctx,
					semanticapi.CallHierarchyPrepareParams{
						TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
						Position:     pos.addFn,
					})
				require.NoError(t, err)
				assert.NotEmpty(t, items)
			},
		},
		{
			name: "SemanticTokensFull returns a token stream",
			fn: func(t *testing.T, mgr *Manager) {
				toks, err := mgr.SemanticTokensFull(ctx, semanticapi.SemanticTokensParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
				})
				require.NoError(t, err)
				require.NotNil(t, toks)
				assert.NotEmpty(t, toks.Data)
			},
		},
		{
			name: "InlayHint returns hints for the file range",
			fn: func(t *testing.T, mgr *Manager) {
				_, err := mgr.InlayHint(ctx, semanticapi.InlayHintParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 0, Character: 0},
						End:   semanticapi.Position{Line: 34, Character: 0},
					},
				})
				require.NoError(t, err)
			},
		},
		{
			name: "CodeAction returns assists or none",
			fn: func(t *testing.T, mgr *Manager) {
				// rust-analyzer rejects a null `context.diagnostics`
				// (it deserializes the field as a required sequence),
				// unlike gopls which tolerates null. Callers must send a
				// non-nil slice; an empty slice is the minimal valid form.
				_, err := mgr.CodeAction(ctx, semanticapi.CodeActionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Range: semanticapi.Range{
						Start: pos.addFn,
						End:   pos.addFn,
					},
					Context: semanticapi.CodeActionContext{
						Diagnostics: []semanticapi.Diagnostic{},
					},
				})
				require.NoError(t, err)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, mgr)
		})
	}
}

func symbolNames(res semanticapi.DocumentSymbolResult) []string {
	var names []string
	for _, s := range res.SymbolInformation {
		names = append(names, s.Name)
	}
	var walk func(items []semanticapi.DocumentSymbol)
	walk = func(items []semanticapi.DocumentSymbol) {
		for _, s := range items {
			names = append(names, s.Name)
			walk(s.Children)
		}
	}
	walk(res.DocumentSymbols)
	return names
}

// withPullDiagnosticsCapability adds the textDocument.diagnostic client
// capability to params, which is what makes rust-analyzer advertise
// diagnosticProvider and answer textDocument/diagnostic pulls.
func withPullDiagnosticsCapability(
	t *testing.T, params semanticapi.InitializeParams,
) semanticapi.InitializeParams {
	t.Helper()
	var caps map[string]any
	require.NoError(t, json.Unmarshal(params.Capabilities, &caps))
	td, ok := caps["textDocument"].(map[string]any)
	require.True(t, ok, "capabilities must carry a textDocument object")
	td["diagnostic"] = map[string]any{}
	raw, err := json.Marshal(caps)
	require.NoError(t, err)
	params.Capabilities = raw
	return params
}

func TestRustE2E_PullDiagnosticsPublish(t *testing.T) {
	// Deliberately not parallel: TestE2ERust already drives a
	// rust-analyzer against the same fixture, and two concurrent
	// workspace loads make its readiness probes flaky under load.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	raBin := findRustAnalyzer(t)
	tmpDir := setupTestWorkspace(t, filepath.Join("testdata", "rs"))
	mainPath := filepath.Join(tmpDir, "src", "main.rs")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	mainURI := "file://" + mainPath

	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()
	callback := &testCallback{}
	mgr := New(uri, scheme, scheme,
		&stubPkgManager{bin: raBin},
		nil, nil,
		Config{
			Callback:                callback,
			MaxRetries:              1,
			NoInitializeServer:      true,
			InitializeTimeout:       30 * time.Second,
			PullDiagnosticsDebounce: 100 * time.Millisecond,
		})
	t.Cleanup(func() { _ = mgr.Close() })

	params := withPullDiagnosticsCapability(t, autoInitParams(uri.String()))
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "rust",
		"command": raBin,
		"cargo": map[string]any{
			"buildScripts": map[string]any{"enable": true},
		},
		"procMacro": map[string]any{"enable": true},
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	broken := string(mainContent) +
		"\nfn __broken() { let _x: u32 = \"oops\"; }\n"
	require.NoError(t, mgr.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
		TextDocument: semanticapi.TextDocumentItem{
			URI:        mainURI,
			LanguageID: "rust",
			Version:    1,
			Text:       broken,
		},
	}))

	found := func() bool {
		callback.mu.Lock()
		defer callback.mu.Unlock()
		for _, p := range callback.publishes {
			if p.params.URI != mainURI ||
				!strings.HasSuffix(p.metadata.ServerName, ":pull") {
				continue
			}
			for _, d := range p.params.Diagnostics {
				if strings.Contains(d.Message, "expected u32") {
					return true
				}
			}
		}
		return false
	}

	// rust-analyzer answers pulls with an empty report until the
	// workspace finishes loading, so keep re-pulling until the semantic
	// error shows up or the bound expires.
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if found() {
			return
		}
		require.NoError(t, mgr.RefreshDiagnostics(ctx))
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled waiting for pull diagnostics: %v", ctx.Err())
		case <-time.After(time.Second):
		}
	}
	t.Fatal("timed out waiting for a bridged pull-diagnostics publish " +
		"carrying the overlay-only semantic error")
}
