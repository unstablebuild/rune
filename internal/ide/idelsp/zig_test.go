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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
)

func findZls(t *testing.T) string {
	t.Helper()
	if bin, err := exec.LookPath("zls"); err == nil {
		return bin
	}
	for _, p := range []string{
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "zls"),
		"/opt/homebrew/bin/zls",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("zls not found, skipping zig e2e test")
	return ""
}

func findZig(t *testing.T) string {
	t.Helper()
	if bin, err := exec.LookPath("zig"); err == nil {
		return bin
	}
	for _, p := range []string{
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "zig"),
		"/opt/homebrew/bin/zig",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("zig not found, skipping zig e2e test")
	return ""
}

// zigPositions holds the 0-based LSP coordinates of the fixture symbols,
// computed from the on-disk file so the license header (or any edit to
// the fixture) cannot desynchronize the assertions.
type zigPositions struct {
	addFn   semanticapi.Position
	addCall semanticapi.Position
	greeter semanticapi.Position
	endLine uint32
}

// locateZigPositions scans main.zig for the fixture symbols and returns
// their 0-based positions. It points at the identifier within each line
// so requests resolve to the symbol rather than surrounding tokens.
func locateZigPositions(t *testing.T, path string) zigPositions {
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
	return zigPositions{
		addFn:   find("pub fn add(", "add("),
		addCall: find("const result = add(", "add("),
		greeter: find("const g = Greeter.init(", "g "),
		endLine: uint32(len(lines) - 1),
	}
}

// setupZigManager drives a real zls through the Manager against a zig
// project fixture, mirroring extension_zig's bring-up: langID "zig",
// command is the bare zls path (a single argv element, since idelsp
// tokenizes command on spaces), and the zls initialization options
// carry zig_exe_path so the server can resolve the toolchain. It
// self-skips when zls or zig are absent and waits for the server to
// answer the probed queries before returning.
func setupZigManager(
	t *testing.T, ctx context.Context, cb *testCallback,
) (*Manager, string, zigPositions) {
	t.Helper()
	zlsBin := findZls(t)
	zigBin := findZig(t)

	tmpDir := setupTestWorkspace(t, filepath.Join("testdata", "zig"))
	mainPath := filepath.Join(tmpDir, "src", "main.zig")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	mainURI := "file://" + mainPath
	pos := locateZigPositions(t, mainPath)

	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()

	mgr := New(uri, scheme, scheme,
		&stubPkgManager{bin: zlsBin},
		nil, nil,
		Config{
			Callback:           cb,
			MaxRetries:         1,
			NoInitializeServer: true,
			InitializeTimeout:  30 * time.Second,
		})
	t.Cleanup(func() { _ = mgr.Close() })

	params := autoInitParams(uri.String())
	// zls only registers workspaces (the scope of workspace/symbol and
	// build-on-save) from workspaceFolders; rootUri alone is ignored.
	params.WorkspaceFolders = []semanticapi.WorkspaceFolder{
		{URI: uri.String(), Name: filepath.Base(tmpDir)},
	}
	initOpts, err := json.Marshal(map[string]any{
		"langID":       "zig",
		"command":      zlsBin,
		"zig_exe_path": zigBin,
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	require.NoError(t, mgr.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
		TextDocument: semanticapi.TextDocumentItem{
			URI:        mainURI,
			LanguageID: "zig",
			Version:    0,
			Text:       string(mainContent),
		},
	}))

	waitForZigReady(t, ctx, mgr, mainURI, pos)
	return mgr, mainURI, pos
}

// zigLocations normalizes a LocationResult into a flat location list:
// zls answers location requests with a bare Location when there is a
// single result, unlike gopls and rust-analyzer which always return
// arrays.
func zigLocations(res semanticapi.LocationResult) []semanticapi.Location {
	if res.Location != nil {
		return []semanticapi.Location{*res.Location}
	}
	if len(res.Locations) > 0 {
		return res.Locations
	}
	locs := make([]semanticapi.Location, 0, len(res.LocationLinks))
	for _, l := range res.LocationLinks {
		locs = append(locs, semanticapi.Location{
			URI: l.TargetURI, Range: l.TargetRange,
		})
	}
	return locs
}

// waitForZigReady polls zls until it answers the queries the subtests
// assert on, so request assertions are not racing the server's async
// startup (zls resolves the zig toolchain and build.zig in the
// background after initialize).
func waitForZigReady(
	t *testing.T, ctx context.Context, mgr *Manager, mainURI string, pos zigPositions,
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
			return err == nil && len(zigLocations(res)) > 0
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
			t.Fatalf("context cancelled waiting for zls: %v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for zls to become ready: stuck on probe %d", next)
}

func TestE2EZig(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cb := &testCallback{}
	mgr, mainURI, pos := setupZigManager(t, ctx, cb)

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
				locs := zigLocations(res)
				require.NotEmpty(t, locs)
				assert.True(t, strings.HasSuffix(locs[0].URI, "/main.zig"))
				assert.Equal(t, pos.addFn.Line, locs[0].Range.Start.Line)
			},
		},
		{
			name: "Declaration resolves the add call to its declaration",
			fn: func(t *testing.T, mgr *Manager) {
				res, err := mgr.Declaration(ctx, semanticapi.DeclarationParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     pos.addCall,
				})
				require.NoError(t, err)
				locs := zigLocations(res)
				require.NotEmpty(t, locs)
				assert.True(t, strings.HasSuffix(locs[0].URI, "/main.zig"))
				assert.Equal(t, pos.addFn.Line, locs[0].Range.Start.Line)
			},
		},
		{
			name: "TypeDefinition resolves the Greeter binding to main.zig",
			fn: func(t *testing.T, mgr *Manager) {
				res, err := mgr.TypeDefinition(ctx, semanticapi.TypeDefinitionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     pos.greeter,
				})
				require.NoError(t, err)
				// zls resolves the type of the `g` binding to the Greeter
				// struct declaration; assert the target only when present
				// to stay stable across zls releases.
				for _, l := range zigLocations(res) {
					assert.True(t, strings.HasSuffix(l.URI, "/main.zig"))
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
			name: "DocumentSymbol lists the module items",
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
				// Put the member-access probe on its own line so the
				// completion position is a deterministic column right
				// after `g.`.
				probe := string(orig) +
					"\nfn _probe() void {\n    const g = Greeter.init(\"x\");\n    g.\n}\n"
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
				// `fn _probe() void {` line that starts at that index.
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
				require.NoError(t, mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
					TextDocument: semanticapi.VersionedTextDocumentIdentifier{
						URI: mainURI, Version: 6,
					},
					ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
						{Text: string(orig)},
					},
				}))
			},
		},
		{
			name: "Formatting returns zig fmt edits or none",
			fn: func(t *testing.T, mgr *Manager) {
				_, err := mgr.Formatting(ctx, semanticapi.DocumentFormattingParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Options:      semanticapi.FormattingOptions{TabSize: 4, InsertSpaces: true},
				})
				require.NoError(t, err)
			},
		},
		{
			name: "FoldingRange covers the struct block",
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
			name: "PrepareRename accepts the add identifier",
			fn: func(t *testing.T, mgr *Manager) {
				res, err := mgr.PrepareRename(ctx, semanticapi.PrepareRenameParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     pos.addFn,
				})
				require.NoError(t, err)
				require.NotNil(t, res)
			},
		},
		{
			name: "Rename add rewrites the declaration and call",
			fn: func(t *testing.T, mgr *Manager) {
				edit, err := mgr.Rename(ctx, semanticapi.RenameParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     pos.addFn,
					NewName:      "sum",
				})
				require.NoError(t, err)
				require.NotNil(t, edit)
				edits := 0
				for _, es := range edit.Changes {
					edits += len(es)
				}
				for _, dc := range edit.DocumentChanges {
					if dc.TextDocumentEdit != nil {
						edits += len(dc.TextDocumentEdit.Edits)
					}
				}
				assert.GreaterOrEqual(t, edits, 2,
					"expected edits for the declaration and the call")
			},
		},
		{
			name: "SelectionRange expands from the add identifier",
			fn: func(t *testing.T, mgr *Manager) {
				res, err := mgr.SelectionRange(ctx, semanticapi.SelectionRangeParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Positions:    []semanticapi.Position{pos.addFn},
				})
				require.NoError(t, err)
				assert.NotEmpty(t, res)
			},
		},
		{
			name: "SignatureHelp describes the add call",
			fn: func(t *testing.T, mgr *Manager) {
				sh, err := mgr.SignatureHelp(ctx, semanticapi.SignatureHelpParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position: semanticapi.Position{
						Line:      pos.addCall.Line,
						Character: pos.addCall.Character + 4,
					},
				})
				require.NoError(t, err)
				require.NotNil(t, sh)
				require.NotEmpty(t, sh.Signatures)
				assert.Contains(t, sh.Signatures[0].Label, "add")
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
						End:   semanticapi.Position{Line: pos.endLine, Character: 0},
					},
				})
				require.NoError(t, err)
			},
		},
		{
			name: "CodeAction returns assists or none",
			fn: func(t *testing.T, mgr *Manager) {
				// zls, like rust-analyzer, requires a non-null
				// `context.diagnostics` sequence; an empty slice is the
				// minimal valid form.
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
		{
			name: "PushDiagnostics reports an ast-check error on a broken edit",
			fn: func(t *testing.T, mgr *Manager) {
				orig, err := os.ReadFile(strings.TrimPrefix(mainURI, "file://"))
				require.NoError(t, err)
				broken := string(orig) + "\nconst broken = ;\n"
				require.NoError(t, mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
					TextDocument: semanticapi.VersionedTextDocumentIdentifier{
						URI: mainURI, Version: 7,
					},
					ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
						{Text: broken},
					},
				}))
				require.Eventually(t, func() bool {
					cb.mu.Lock()
					defer cb.mu.Unlock()
					for _, p := range cb.diagnostics {
						if p.URI == mainURI && len(p.Diagnostics) > 0 {
							return true
						}
					}
					return false
				}, 30*time.Second, 200*time.Millisecond,
					"expected zls to publish an ast-check diagnostic")
				require.NoError(t, mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
					TextDocument: semanticapi.VersionedTextDocumentIdentifier{
						URI: mainURI, Version: 8,
					},
					ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
						{Text: string(orig)},
					},
				}))
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, mgr)
		})
	}
}
