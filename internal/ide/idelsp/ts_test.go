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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
)

// findTsgo locates a TypeScript 7 native compiler, which serves LSP,
// or skips. An older tsc on PATH is not a candidate.
func findTsgo(t *testing.T) string {
	t.Helper()
	var candidates []string
	for _, name := range []string{"tsgo", "tsc"} {
		if bin, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, bin)
		}
	}
	candidates = append(candidates,
		filepath.Join(os.Getenv("HOME"), ".rune", "lib", "typescript", "bin", "tsgo"),
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "tsgo"),
	)
	for _, bin := range candidates {
		out, err := exec.Command(bin, "--version").Output()
		if err != nil {
			continue
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			continue
		}
		major, _, _ := strings.Cut(fields[len(fields)-1], ".")
		if n, err := strconv.Atoi(major); err == nil && n >= 7 {
			return bin
		}
	}
	t.Skip("TypeScript 7 (tsgo) not found, skipping typescript e2e test")
	return ""
}

// tsPositions holds the 0-based LSP coordinates of the fixture symbols,
// computed from the on-disk files so fixture edits cannot desynchronize
// the assertions.
type tsPositions struct {
	addFn     semanticapi.Position
	addCall   semanticapi.Position
	appAdd    semanticapi.Position
	greeter   semanticapi.Position
	shape     semanticapi.Position
	squareCls uint32
	greeterCl uint32
	libEnd    uint32
}

func locateTSPosition(t *testing.T, path, marker, ident string) semanticapi.Position {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for i, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		col := strings.Index(line, ident)
		require.GreaterOrEqual(t, col, 0, "ident %q not in %q", ident, line)
		return semanticapi.Position{Line: uint32(i), Character: uint32(col)}
	}
	t.Fatalf("could not find %q in %s", marker, path)
	return semanticapi.Position{}
}

func locateTSPositions(t *testing.T, srcDir string) tsPositions {
	t.Helper()
	lib := filepath.Join(srcDir, "lib.ts")
	main := filepath.Join(srcDir, "main.ts")
	app := filepath.Join(srcDir, "App.tsx")
	data, err := os.ReadFile(lib)
	require.NoError(t, err)
	return tsPositions{
		addFn:     locateTSPosition(t, lib, "export function add(", "add("),
		addCall:   locateTSPosition(t, main, "const result = add(", "add("),
		appAdd:    locateTSPosition(t, app, "{add(", "add("),
		greeter:   locateTSPosition(t, main, "const g = new Greeter(", "g "),
		shape:     locateTSPosition(t, lib, "export interface Shape", "Shape"),
		squareCls: locateTSPosition(t, lib, "export class Square", "Square").Line,
		greeterCl: locateTSPosition(t, lib, "export class Greeter", "Greeter").Line,
		libEnd:    uint32(strings.Count(string(data), "\n")),
	}
}

// tsLocations normalizes a LocationResult into a flat location list.
func tsLocations(res semanticapi.LocationResult) []semanticapi.Location {
	if res.Location != nil {
		return []semanticapi.Location{*res.Location}
	}
	if len(res.Locations) > 0 {
		return res.Locations
	}
	locs := make([]semanticapi.Location, 0, len(res.LocationLinks))
	for _, l := range res.LocationLinks {
		locs = append(locs, semanticapi.Location{URI: l.TargetURI, Range: l.TargetRange})
	}
	return locs
}

// setupTSManager drives a real tsgo through the Manager against the
// TypeScript fixture, mirroring extension_typescript's bring-up: one
// "typescript" server for every dialect, started as `tsgo --lsp
// --stdio`, with the textDocument.diagnostic capability tsgo needs to
// report per-file diagnostics. main.ts and lib.ts are opened through
// the editor-event path; App.tsx and Badge.jsx stay closed so requests
// on them exercise the transient open.
func setupTSManager(
	t *testing.T, ctx context.Context, cb *testCallback,
) (*Manager, string, tsPositions) {
	t.Helper()
	tsgo := findTsgo(t)

	tmpDir := setupTestWorkspace(t, filepath.Join("testdata", "ts"))
	srcDir := filepath.Join(tmpDir, "src")
	pos := locateTSPositions(t, srcDir)

	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()
	mgr := New(uri, scheme, scheme,
		&stubPkgManager{bin: tsgo},
		nil, nil,
		Config{
			Callback:                cb,
			MaxRetries:              1,
			NoInitializeServer:      true,
			InitializeTimeout:       30 * time.Second,
			PullDiagnosticsDebounce: 100 * time.Millisecond,
		})
	t.Cleanup(func() { _ = mgr.Close() })

	params := withPullDiagnosticsCapability(t, autoInitParams(uri.String()))
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "typescript",
		"command": tsgo + " --lsp --stdio",
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	for _, name := range []string{"lib.ts", "main.ts"} {
		path := filepath.Join(srcDir, name)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, mgr.handle(textapi.Event{
			Type:    textapi.EventTypeOpen,
			URI:     makeURI(t, "file://"+path),
			Content: string(content),
		}))
	}
	waitForTSReady(t, ctx, mgr, srcDir, pos)
	return mgr, srcDir, pos
}

// waitForTSReady polls tsgo until it resolves the cross-file definition
// and the references the subtests assert on, so assertions do not race
// the server's asynchronous project load.
func waitForTSReady(
	t *testing.T, ctx context.Context, mgr *Manager, srcDir string, pos tsPositions,
) {
	t.Helper()
	mainURI := "file://" + filepath.Join(srcDir, "main.ts")
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		res, err := mgr.Definition(ctx, semanticapi.DefinitionParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
			Position:     pos.addCall,
		})
		if err == nil && len(tsLocations(res)) > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled waiting for tsgo: %v", ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
	t.Fatal("timed out waiting for tsgo to become ready")
}

func TestE2ETypeScript(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cb := &testCallback{}
	mgr, srcDir, pos := setupTSManager(t, ctx, cb)
	uriOf := func(name string) string { return "file://" + filepath.Join(srcDir, name) }
	libURI, mainURI := uriOf("lib.ts"), uriOf("main.ts")

	tests := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{
			name: "Hover reports the add signature",
			fn: func(t *testing.T) {
				h, err := mgr.Hover(ctx, semanticapi.HoverParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: libURI},
					Position:     pos.addFn,
				})
				require.NoError(t, err)
				require.NotNil(t, h)
				assert.Contains(t, h.Contents.Value, "function add(a: number, b: number): number")
			},
		},
		{
			name: "Definition resolves the add call into lib.ts",
			fn: func(t *testing.T) {
				res, err := mgr.Definition(ctx, semanticapi.DefinitionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     pos.addCall,
				})
				require.NoError(t, err)
				locs := tsLocations(res)
				require.NotEmpty(t, locs)
				assert.Equal(t, libURI, locs[0].URI)
				assert.Equal(t, pos.addFn.Line, locs[0].Range.Start.Line)
			},
		},
		{
			name: "Definition from a closed tsx file resolves into lib.ts",
			fn: func(t *testing.T) {
				res, err := mgr.Definition(ctx, semanticapi.DefinitionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: uriOf("App.tsx")},
					Position:     pos.appAdd,
				})
				require.NoError(t, err)
				locs := tsLocations(res)
				require.NotEmpty(t, locs)
				assert.Equal(t, libURI, locs[0].URI)
				assert.Equal(t, pos.addFn.Line, locs[0].Range.Start.Line)
			},
		},
		{
			name: "TypeDefinition resolves the Greeter binding to its class",
			fn: func(t *testing.T) {
				res, err := mgr.TypeDefinition(ctx, semanticapi.TypeDefinitionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     pos.greeter,
				})
				require.NoError(t, err)
				locs := tsLocations(res)
				require.NotEmpty(t, locs)
				assert.Equal(t, libURI, locs[0].URI)
				assert.Equal(t, pos.greeterCl, locs[0].Range.Start.Line)
			},
		},
		{
			name: "Implementation finds the Shape implementor",
			fn: func(t *testing.T) {
				res, err := mgr.Implementation(ctx, semanticapi.ImplementationParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: libURI},
					Position:     pos.shape,
				})
				require.NoError(t, err)
				locs := tsLocations(res)
				require.NotEmpty(t, locs)
				assert.Equal(t, pos.squareCls, locs[0].Range.Start.Line)
			},
		},
		{
			name: "References span the ts and tsx callers",
			fn: func(t *testing.T) {
				locs, err := mgr.References(ctx, semanticapi.ReferenceParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: libURI},
					Position:     pos.addFn,
					Context:      semanticapi.ReferenceContext{IncludeDeclaration: true},
				})
				require.NoError(t, err)
				files := map[string]bool{}
				for _, l := range locs {
					files[filepath.Base(l.URI)] = true
				}
				assert.True(t, files["lib.ts"], "declaration")
				assert.True(t, files["main.ts"], "ts caller")
				assert.True(t, files["App.tsx"], "tsx caller")
			},
		},
		{
			name: "DocumentSymbol lists the module items",
			fn: func(t *testing.T) {
				res, err := mgr.DocumentSymbol(ctx, semanticapi.DocumentSymbolParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: libURI},
				})
				require.NoError(t, err)
				names := symbolNames(res)
				assert.Contains(t, names, "Shape")
				assert.Contains(t, names, "Square")
				assert.Contains(t, names, "Greeter")
				assert.Contains(t, names, "add")
			},
		},
		{
			name: "WorkspaceSymbol finds the JavaScript double",
			fn: func(t *testing.T) {
				syms, err := mgr.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{
					Query: "double",
				})
				require.NoError(t, err)
				var found bool
				for _, s := range syms {
					found = found || (s.Name == "double" &&
						strings.HasSuffix(s.Location.URI, "/util.js"))
				}
				assert.True(t, found, "expected double from util.js, got %+v", syms)
			},
		},
		{
			name: "Completion offers Greeter members",
			fn: func(t *testing.T) {
				orig, err := os.ReadFile(strings.TrimPrefix(mainURI, "file://"))
				require.NoError(t, err)
				probe := string(orig) + "g.\n"
				require.NoError(t, mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
					TextDocument: semanticapi.VersionedTextDocumentIdentifier{
						URI: mainURI, Version: 5,
					},
					ContentChanges: []semanticapi.TextDocumentContentChangeEvent{{Text: probe}},
				}))
				res, err := mgr.Completion(ctx, semanticapi.CompletionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position: semanticapi.Position{
						Line: uint32(strings.Count(string(orig), "\n")), Character: 2,
					},
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
			name: "Formatting returns edits or none",
			fn: func(t *testing.T) {
				_, err := mgr.Formatting(ctx, semanticapi.DocumentFormattingParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: libURI},
					Options:      semanticapi.FormattingOptions{TabSize: 2, InsertSpaces: true},
				})
				require.NoError(t, err)
			},
		},
		{
			name: "FoldingRange covers the class bodies",
			fn: func(t *testing.T) {
				ranges, err := mgr.FoldingRange(ctx, semanticapi.FoldingRangeParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: libURI},
				})
				require.NoError(t, err)
				assert.NotEmpty(t, ranges)
			},
		},
		{
			name: "DocumentHighlight marks add occurrences",
			fn: func(t *testing.T) {
				hs, err := mgr.DocumentHighlight(ctx, semanticapi.DocumentHighlightParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Position:     pos.addCall,
				})
				require.NoError(t, err)
				assert.GreaterOrEqual(t, len(hs), 2, "import and call")
			},
		},
		{
			name: "PrepareRename accepts the add identifier",
			fn: func(t *testing.T) {
				res, err := mgr.PrepareRename(ctx, semanticapi.PrepareRenameParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: libURI},
					Position:     pos.addFn,
				})
				require.NoError(t, err)
				require.NotNil(t, res)
			},
		},
		{
			name: "Rename add rewrites every dialect's caller",
			fn: func(t *testing.T) {
				edit, err := mgr.Rename(ctx, semanticapi.RenameParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: libURI},
					Position:     pos.addFn,
					NewName:      "sum",
				})
				require.NoError(t, err)
				require.NotNil(t, edit)
				files := map[string]bool{}
				for uri := range edit.Changes {
					files[filepath.Base(uri)] = true
				}
				for _, dc := range edit.DocumentChanges {
					if dc.TextDocumentEdit != nil {
						files[filepath.Base(dc.TextDocumentEdit.TextDocument.URI)] = true
					}
				}
				assert.True(t, files["lib.ts"])
				assert.True(t, files["main.ts"])
				assert.True(t, files["App.tsx"])
			},
		},
		{
			name: "SelectionRange expands from the add identifier",
			fn: func(t *testing.T) {
				res, err := mgr.SelectionRange(ctx, semanticapi.SelectionRangeParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: libURI},
					Positions:    []semanticapi.Position{pos.addFn},
				})
				require.NoError(t, err)
				assert.NotEmpty(t, res)
			},
		},
		{
			name: "SignatureHelp describes the add call",
			fn: func(t *testing.T) {
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
				assert.Contains(t, sh.Signatures[0].Label, "add(a: number, b: number)")
			},
		},
		{
			name: "SemanticTokensFull returns a token stream",
			fn: func(t *testing.T) {
				toks, err := mgr.SemanticTokensFull(ctx, semanticapi.SemanticTokensParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: libURI},
				})
				require.NoError(t, err)
				require.NotNil(t, toks)
				assert.NotEmpty(t, toks.Data)
			},
		},
		{
			name: "InlayHint returns hints for the file range",
			fn: func(t *testing.T) {
				_, err := mgr.InlayHint(ctx, semanticapi.InlayHintParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: libURI},
					Range: semanticapi.Range{
						End: semanticapi.Position{Line: pos.libEnd},
					},
				})
				require.NoError(t, err)
			},
		},
		{
			name: "CodeAction offers organize imports",
			fn: func(t *testing.T) {
				actions, err := mgr.CodeAction(ctx, semanticapi.CodeActionParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
					Range:        semanticapi.Range{Start: pos.addCall, End: pos.addCall},
					Context: semanticapi.CodeActionContext{
						Diagnostics: []semanticapi.Diagnostic{},
						Only:        []semanticapi.CodeActionKind{"source.organizeImports"},
					},
				})
				require.NoError(t, err)
				var kinds []string
				for _, a := range actions {
					if a.CodeAction != nil {
						kinds = append(kinds, string(a.CodeAction.Kind))
					}
				}
				assert.Contains(t, kinds, "source.organizeImports")
			},
		},
		{
			name: "Diagnostic parses editor-opened tsx and jsx files as JSX",
			fn: func(t *testing.T) {
				// An edit makes tsgo parse the open buffer rather than
				// reuse the disk parse, and the buffer's dialect comes
				// only from the didOpen languageId.
				for _, name := range []string{"App.tsx", "Badge.jsx"} {
					path := filepath.Join(srcDir, name)
					content, err := os.ReadFile(path)
					require.NoError(t, err)
					require.NoError(t, mgr.handle(textapi.Event{
						Type:    textapi.EventTypeOpen,
						URI:     makeURI(t, uriOf(name)),
						Content: string(content) + "// edited\n",
					}))
					report, err := mgr.Diagnostic(ctx, semanticapi.DocumentDiagnosticParams{
						TextDocument: semanticapi.TextDocumentIdentifier{URI: uriOf(name)},
					})
					require.NoError(t, err, name)
					assert.Equal(t, "full", report.Kind, name)
					assert.Empty(t, report.Items, name)
					require.NoError(t, mgr.handle(textapi.Event{
						Type: textapi.EventTypeClose,
						URI:  makeURI(t, uriOf(name)),
					}))
				}
			},
		},
		{
			name: "PullDiagnostics publishes a type error on a broken edit",
			fn: func(t *testing.T) {
				orig, err := os.ReadFile(strings.TrimPrefix(mainURI, "file://"))
				require.NoError(t, err)
				broken := string(orig) + "const broken: number = \"oops\";\n"
				require.NoError(t, mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
					TextDocument: semanticapi.VersionedTextDocumentIdentifier{
						URI: mainURI, Version: 7,
					},
					ContentChanges: []semanticapi.TextDocumentContentChangeEvent{{Text: broken}},
				}))
				require.Eventually(t, func() bool {
					cb.mu.Lock()
					defer cb.mu.Unlock()
					for _, p := range cb.publishes {
						if p.params.URI != mainURI ||
							!strings.HasSuffix(p.metadata.ServerName, ":pull") {
							continue
						}
						for _, d := range p.params.Diagnostics {
							if strings.Contains(d.Message, "not assignable to type 'number'") {
								return true
							}
						}
					}
					return false
				}, 30*time.Second, 200*time.Millisecond,
					"expected a bridged pull-diagnostics publish carrying the type error")
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
		t.Run(tc.name, tc.fn)
	}
}
