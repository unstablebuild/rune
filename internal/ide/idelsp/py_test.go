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
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// pyPkgManager resolves both the ty and ruff binaries from a single
// language package, mirroring how the Python toolchain ships several
// executables in one lib dir. findBinary disambiguates by base name.
type pyPkgManager struct {
	bins []string
}

func (p *pyPkgManager) LibDir(
	_ context.Context, _ string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice(p.bins), nil
}

// Pinned Astral toolchain versions, kept in sync with
// rune-language-python/Makefile (RUFF_VERSION/TY_VERSION). The e2e
// expectations below are exact for these versions; a host toolchain
// that diverges from what the package distributes must fail loudly
// rather than silently validate against unshipped behavior.
const (
	pinnedRuffVersion = "0.15.18"
	pinnedTyVersion   = "0.0.51"
)

// findOnPath resolves name on PATH, skipping when it is absent (so CI
// without the Python toolchain stays green) but failing when the
// resolved binary's version diverges from the distributed pin.
func findOnPath(t *testing.T, name, wantVersion string) string {
	t.Helper()
	bin, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not found, skipping python e2e test", name)
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	require.NoErrorf(t, err, "%s --version failed", name)
	require.Containsf(t, string(out), wantVersion,
		"%s version diverges from the distributed pin %s; update the pin "+
			"and e2e expectations together", name, wantVersion)
	return bin
}

// setupPythonManager drives a real ty + ruff multi-server setup through
// the Manager: ty is the default child (hover/definition/symbols/etc.)
// and ruff serves formatting via alternate_commands. It self-skips when
// ty or ruff is not installed so CI without the Python toolchain stays
// green, and fails when an installed tool diverges from the distributed
// pin. It returns the manager and the opened main.py URI.
func setupPythonManager(
	t *testing.T, ctx context.Context, callback Callback,
) (*Manager, string) {
	t.Helper()
	mgr, mainURI, mainContent := setupPythonManagerNoOpen(t, ctx, callback)
	require.NoError(t, mgr.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
		TextDocument: semanticapi.TextDocumentItem{
			URI:        mainURI,
			LanguageID: "python",
			Version:    0,
			Text:       mainContent,
		},
	}))
	return mgr, mainURI
}

// setupPythonManagerNoOpen initializes a real ty + ruff manager rooted
// at a temp copy of testdata/py but does NOT send didOpen for main.py,
// so callers can exercise requests against an unopened document.
func setupPythonManagerNoOpen(
	t *testing.T, ctx context.Context, callback Callback,
) (*Manager, string, string) {
	t.Helper()
	if callback == nil {
		callback = &testCallback{}
	}
	tyBin := findOnPath(t, "ty", pinnedTyVersion)
	ruffBin := findOnPath(t, "ruff", pinnedRuffVersion)

	tmpDir := setupTestWorkspace(t, filepath.Join("testdata", "py"))
	mainPath := filepath.Join(tmpDir, "main.py")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	mainURI := "file://" + mainPath

	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()

	mgr := New(uri, scheme, scheme,
		&pyPkgManager{bins: []string{tyBin, ruffBin}},
		nil, nil,
		Config{Callback: callback, MaxRetries: 1, NoInitializeServer: true})
	t.Cleanup(func() { _ = mgr.Close() })

	params := autoInitParams(uri.String())
	var capabilities map[string]any
	require.NoError(t, json.Unmarshal(params.Capabilities, &capabilities))
	textDocument := capabilities["textDocument"].(map[string]any)
	textDocument["hover"] = map[string]any{
		"contentFormat": []string{"markdown", "plaintext"},
	}
	textDocument["declaration"] = map[string]any{
		"linkSupport": true,
	}
	textDocument["definition"] = map[string]any{
		"linkSupport": true,
	}
	textDocument["typeDefinition"] = map[string]any{
		"linkSupport": true,
	}
	textDocument["completion"] = map[string]any{
		"completionItem": map[string]any{
			"documentationFormat": []string{"markdown", "plaintext"},
		},
	}
	textDocument["signatureHelp"] = map[string]any{
		"signatureInformation": map[string]any{
			"activeParameterSupport": true,
			"parameterInformation": map[string]any{
				"labelOffsetSupport": true,
			},
		},
	}
	textDocument["publishDiagnostics"] = map[string]any{
		"relatedInformation": true,
	}
	params.Capabilities, err = json.Marshal(capabilities)
	require.NoError(t, err)
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "python",
		"command": "ty server",
		"alternate_commands": map[string]string{
			"textDocument/formatting":      "ruff server",
			"textDocument/rangeFormatting": "ruff server",
		},
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	return mgr, mainURI, string(mainContent)
}

func TestE2EPython(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mgr, mainURI := setupPythonManager(t, ctx, nil)

	tests := []struct {
		name string
		fn   func(t *testing.T, mgr *Manager)
	}{
		{
			// ruff (alternate_commands child) serves formatting; the
			// edit reflows the body and fixes the a+b spacing.
			name: "Formatting routes to ruff",
			fn: func(t *testing.T, mgr *Manager) {
				edits, err := mgr.Formatting(ctx,
					semanticapi.DocumentFormattingParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Options: semanticapi.FormattingOptions{
							TabSize: 4, InsertSpaces: true,
						},
					},
				)
				require.NoError(t, err)
				require.Equal(t, []semanticapi.TextEdit{{
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 33, Character: 0},
						End:   semanticapi.Position{Line: 34, Character: 0},
					},
					NewText: "    return a + b\n",
				}}, edits)
			},
		},
		{
			// ty (default child) serves hover.
			name: "Hover routes to ty",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.Hover(ctx, semanticapi.HoverParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: mainURI,
					},
					Position: semanticapi.Position{Line: 31, Character: 4},
				})
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, semanticapi.MarkupKindMarkdown,
					result.Contents.Kind)
				assert.Equal(t,
					"```python\ndef add(\n    a: int,\n    b: int\n) -> int\n```\n"+
						"---\nadd adds two integers.",
					result.Contents.Value)
				require.NotNil(t, result.Range)
				assert.Equal(t, semanticapi.Range{
					Start: semanticapi.Position{Line: 31, Character: 4},
					End:   semanticapi.Position{Line: 31, Character: 7},
				}, *result.Range)
			},
		},
		{
			name: "Declaration links route to ty",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.Declaration(ctx,
					semanticapi.DeclarationParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 39, Character: 13,
						},
					},
				)
				require.NoError(t, err)
				require.Equal(t, []semanticapi.LocationLink{{
					OriginSelectionRange: &semanticapi.Range{
						Start: semanticapi.Position{Line: 39, Character: 13},
						End:   semanticapi.Position{Line: 39, Character: 16},
					},
					TargetURI: mainURI,
					TargetRange: semanticapi.Range{
						Start: semanticapi.Position{Line: 31, Character: 0},
						End:   semanticapi.Position{Line: 33, Character: 14},
					},
					TargetSelectionRange: semanticapi.Range{
						Start: semanticapi.Position{Line: 31, Character: 4},
						End:   semanticapi.Position{Line: 31, Character: 7},
					},
				}}, result.LocationLinks)
			},
		},
		{
			name: "Definition links route to ty",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.Definition(ctx,
					semanticapi.DefinitionParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 39, Character: 13,
						},
					},
				)
				require.NoError(t, err)
				require.Equal(t, []semanticapi.LocationLink{{
					OriginSelectionRange: &semanticapi.Range{
						Start: semanticapi.Position{Line: 39, Character: 13},
						End:   semanticapi.Position{Line: 39, Character: 16},
					},
					TargetURI: mainURI,
					TargetRange: semanticapi.Range{
						Start: semanticapi.Position{Line: 31, Character: 0},
						End:   semanticapi.Position{Line: 33, Character: 14},
					},
					TargetSelectionRange: semanticapi.Range{
						Start: semanticapi.Position{Line: 31, Character: 4},
						End:   semanticapi.Position{Line: 31, Character: 7},
					},
				}}, result.LocationLinks)
			},
		},
		{
			name: "References routes to ty",
			fn: func(t *testing.T, mgr *Manager) {
				locs, err := mgr.References(ctx,
					semanticapi.ReferenceParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 31, Character: 4,
						},
						Context: semanticapi.ReferenceContext{
							IncludeDeclaration: true,
						},
					},
				)
				require.NoError(t, err)
				assert.ElementsMatch(t, []semanticapi.Location{
					{
						URI: mainURI,
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 31, Character: 4},
							End:   semanticapi.Position{Line: 31, Character: 7},
						},
					},
					{
						URI: mainURI,
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 39, Character: 13},
							End:   semanticapi.Position{Line: 39, Character: 16},
						},
					},
				}, locs)
			},
		},
		{
			name: "DocumentSymbol routes to ty",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.DocumentSymbol(ctx,
					semanticapi.DocumentSymbolParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
					},
				)
				require.NoError(t, err)
				loc := func(sl, sc, el, ec uint32) semanticapi.Location {
					return semanticapi.Location{
						URI: mainURI,
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: sl, Character: sc},
							End:   semanticapi.Position{Line: el, Character: ec},
						},
					}
				}
				assert.Equal(t, []semanticapi.SymbolInformation{
					{Name: "Greeter", Kind: semanticapi.SymbolKindClass,
						Location: loc(20, 0, 28, 42)},
					{Name: "__init__", Kind: semanticapi.SymbolKindConstructor,
						Location: loc(23, 4, 24, 24)},
					{Name: "greet", Kind: semanticapi.SymbolKindMethod,
						Location: loc(26, 4, 28, 42)},
					{Name: "add", Kind: semanticapi.SymbolKindFunction,
						Location: loc(31, 0, 33, 14)},
					{Name: "main", Kind: semanticapi.SymbolKindFunction,
						Location: loc(36, 0, 40, 17)},
				}, result.SymbolInformation)
			},
		},
		{
			name: "WorkspaceSymbol routes to ty",
			fn: func(t *testing.T, mgr *Manager) {
				syms, err := mgr.WorkspaceSymbol(ctx,
					semanticapi.WorkspaceSymbolParams{Query: "add"},
				)
				require.NoError(t, err)
				// ty resolves workspace symbols against its own index
				// root, which can include other Python files in the
				// repository tree, so select the main.py result and
				// assert its stable name/kind/range.
				var got *semanticapi.SymbolInformation
				for i := range syms {
					if strings.HasSuffix(syms[i].Location.URI, "/main.py") {
						got = &syms[i]
						break
					}
				}
				require.NotNil(t, got,
					"no workspace symbol resolved to main.py: %v", syms)
				assert.Equal(t, "add", got.Name)
				assert.Equal(t, semanticapi.SymbolKindFunction, got.Kind)
				assert.Equal(t, semanticapi.Range{
					Start: semanticapi.Position{Line: 31, Character: 0},
					End:   semanticapi.Position{Line: 33, Character: 14},
				}, got.Location.Range)
			},
		},
		{
			name: "Completion routes to ty",
			fn: func(t *testing.T, mgr *Manager) {
				// Append a member-access expression so the completion
				// list is the deterministic set of Greeter members
				// followed by inherited object dunders.
				probe := "\n_probe = Greeter(\"x\").\n"
				orig, err := os.ReadFile(strings.TrimPrefix(mainURI, "file://"))
				require.NoError(t, err)
				require.NoError(t, mgr.DidChange(ctx,
					semanticapi.DidChangeTextDocumentParams{
						TextDocument: semanticapi.VersionedTextDocumentIdentifier{
							URI: mainURI, Version: 5,
						},
						ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
							{Text: string(orig) + probe},
						},
					},
				))
				memberLine := uint32(len(strings.Split(string(orig), "\n")))
				result, err := mgr.Completion(ctx,
					semanticapi.CompletionParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: memberLine, Character: 22,
						},
					},
				)
				require.NoError(t, err)
				labels := make([]string, len(result.Items))
				for i, it := range result.Items {
					labels[i] = it.Label
				}
				assert.Equal(t, []string{
					"greet", "name",
					"__annotations__", "__class__", "__delattr__",
					"__dict__", "__dir__", "__doc__", "__eq__",
					"__format__", "__getattribute__", "__getstate__",
					"__hash__", "__init__", "__init_subclass__",
					"__module__", "__ne__", "__new__", "__reduce__",
					"__reduce_ex__", "__repr__", "__setattr__",
					"__sizeof__", "__str__", "__subclasshook__",
				}, labels)
				var greet *semanticapi.CompletionItem
				for i := range result.Items {
					if result.Items[i].Label == "greet" {
						greet = &result.Items[i]
						break
					}
				}
				require.NotNil(t, greet)
				require.NotNil(t, greet.Documentation)
				assert.Equal(t, semanticapi.MarkupKindMarkdown,
					greet.Documentation.Kind)
				// restore original content for any later cases
				require.NoError(t, mgr.DidChange(ctx,
					semanticapi.DidChangeTextDocumentParams{
						TextDocument: semanticapi.VersionedTextDocumentIdentifier{
							URI: mainURI, Version: 6,
						},
						ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
							{Text: string(orig)},
						},
					},
				))
			},
		},
		{
			name: "DocumentHighlight routes to ty",
			fn: func(t *testing.T, mgr *Manager) {
				highlights, err := mgr.DocumentHighlight(ctx,
					semanticapi.DocumentHighlightParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 31, Character: 4,
						},
					},
				)
				require.NoError(t, err)
				assert.Equal(t, []semanticapi.DocumentHighlight{
					{
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 31, Character: 4},
							End:   semanticapi.Position{Line: 31, Character: 7},
						},
						Kind: semanticapi.DocumentHighlightKindText,
					},
					{
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 39, Character: 13},
							End:   semanticapi.Position{Line: 39, Character: 16},
						},
						Kind: semanticapi.DocumentHighlightKindRead,
					},
				}, highlights)
			},
		},
		{
			name: "FoldingRange routes to ty",
			fn: func(t *testing.T, mgr *Manager) {
				ranges, err := mgr.FoldingRange(ctx,
					semanticapi.FoldingRangeParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
					},
				)
				require.NoError(t, err)
				assert.Equal(t, []semanticapi.FoldingRange{
					{StartLine: 20, StartCharacter: 14, EndLine: 28, EndCharacter: 42},
					{StartLine: 23, StartCharacter: 42, EndLine: 24, EndCharacter: 24},
					{StartLine: 26, StartCharacter: 27, EndLine: 28, EndCharacter: 42},
					{StartLine: 31, StartCharacter: 31, EndLine: 33, EndCharacter: 14},
					{StartLine: 36, StartCharacter: 19, EndLine: 40, EndCharacter: 17},
					{StartLine: 43, StartCharacter: 26, EndLine: 44, EndCharacter: 10},
					{StartLine: 0, EndLine: 15, EndCharacter: 49,
						Kind: semanticapi.FoldingRangeKindComment},
				}, ranges)
			},
		},
		{
			// With a canonical (symlink-resolved) workspace root, ty
			// 0.0.51 answers prepareRename for this function symbol with
			// the identifier's range (range-only, no placeholder).
			name: "PrepareRename returns identifier range for function",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.PrepareRename(ctx,
					semanticapi.PrepareRenameParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 31, Character: 4,
						},
					},
				)
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, semanticapi.Range{
					Start: semanticapi.Position{Line: 31, Character: 4},
					End:   semanticapi.Position{Line: 31, Character: 7},
				}, result.Range)
				assert.True(t, result.IsRangeOnly)
			},
		},
		{
			name: "Rename routes to ty",
			fn: func(t *testing.T, mgr *Manager) {
				edit, err := mgr.Rename(ctx, semanticapi.RenameParams{
					TextDocument: semanticapi.TextDocumentIdentifier{
						URI: mainURI,
					},
					Position: semanticapi.Position{Line: 31, Character: 4},
					NewName:  "addNums",
				})
				require.NoError(t, err)
				require.NotNil(t, edit)
				assert.Equal(t, map[string][]semanticapi.TextEdit{
					mainURI: {
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 31, Character: 4},
								End:   semanticapi.Position{Line: 31, Character: 7},
							},
							NewText: "addNums",
						},
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 39, Character: 13},
								End:   semanticapi.Position{Line: 39, Character: 16},
							},
							NewText: "addNums",
						},
					},
				}, edit.Changes)
			},
		},
		{
			name: "SelectionRange routes to ty",
			fn: func(t *testing.T, mgr *Manager) {
				ranges, err := mgr.SelectionRange(ctx,
					semanticapi.SelectionRangeParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Positions: []semanticapi.Position{
							{Line: 34, Character: 11},
						},
					},
				)
				require.NoError(t, err)
				require.Len(t, ranges, 1)
				assert.Equal(t, semanticapi.Range{
					Start: semanticapi.Position{Line: 0, Character: 0},
					End:   semanticapi.Position{Line: 45, Character: 0},
				}, ranges[0].Range)
			},
		},
		{
			name: "SignatureHelp routes to ty",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.SignatureHelp(ctx,
					semanticapi.SignatureHelpParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 39, Character: 17,
						},
					},
				)
				require.NoError(t, err)
				assert.Equal(t, uint32(0), result.ActiveSignature)
				assert.Equal(t, uint32(0), result.ActiveParameter)
				assert.Equal(t, []semanticapi.SignatureInformation{
					{
						Label: "(a: int, b: int) -> int",
						Parameters: []semanticapi.ParameterInformation{
							{Label: "a: int"},
							{Label: "b: int"},
						},
					},
				}, result.Signatures)
			},
		},
		{
			// ty resolves int to the vendored typeshed builtins stub;
			// the exact path is environment-specific so only its shape
			// is asserted.
			name: "TypeDefinition links route to ty",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.TypeDefinition(ctx,
					semanticapi.TypeDefinitionParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 38, Character: 4,
						},
					},
				)
				require.NoError(t, err)
				require.Len(t, result.LocationLinks, 1)
				assert.True(t,
					strings.HasSuffix(result.LocationLinks[0].TargetURI, "builtins.pyi"),
					"unexpected type definition uri: %s",
					result.LocationLinks[0].TargetURI)
				assert.Equal(t, &semanticapi.Range{
					Start: semanticapi.Position{Line: 38, Character: 4},
					End:   semanticapi.Position{Line: 38, Character: 9},
				}, result.LocationLinks[0].OriginSelectionRange)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.fn(t, mgr)
		})
	}
}

func TestE2EPythonMergedDiagnostics(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var diagMu sync.Mutex
	byURI := map[string][]semanticapi.Diagnostic{}
	callback := &testCallback{
		onDiagnostics: func(p semanticapi.PublishDiagnosticsParams) {
			diagMu.Lock()
			defer diagMu.Unlock()
			if len(p.Diagnostics) > 0 {
				byURI[p.URI] = p.Diagnostics
			}
		},
	}

	_, mainURI := setupPythonManager(t, ctx, callback)

	var got semanticapi.Diagnostic
	require.Eventually(t, func() bool {
		diagMu.Lock()
		defer diagMu.Unlock()
		ds := byURI[mainURI]
		if len(ds) == 0 {
			return false
		}
		got = ds[0]
		return true
	}, 15*time.Second, 200*time.Millisecond,
		"expected ruff F401 diagnostic for unused import")

	assert.Equal(t, "F401", got.Code)
	assert.Equal(t, "Ruff", got.Source)
	assert.Equal(t, semanticapi.DiagnosticSeverityWarning, got.Severity)
	assert.Equal(t,
		"`os` imported but unused\n\nhelp: Remove unused import: `os`",
		got.Message)
	assert.Equal(t, semanticapi.Range{
		Start: semanticapi.Position{Line: 17, Character: 7},
		End:   semanticapi.Position{Line: 17, Character: 9},
	}, got.Range)
}

func TestE2EPythonDefinitionUnopenedFile(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mgr, mainURI, _ := setupPythonManagerNoOpen(t, ctx, nil)

	// main.py is intentionally not opened. Position 39:13 is the `add`
	// call in `result = add(1, 2)`.
	result, err := mgr.Definition(ctx, semanticapi.DefinitionParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
		Position:     semanticapi.Position{Line: 39, Character: 13},
	})
	require.NoError(t, err)
	require.Equal(t, []semanticapi.LocationLink{{
		OriginSelectionRange: &semanticapi.Range{
			Start: semanticapi.Position{Line: 39, Character: 13},
			End:   semanticapi.Position{Line: 39, Character: 16},
		},
		TargetURI: mainURI,
		TargetRange: semanticapi.Range{
			Start: semanticapi.Position{Line: 31, Character: 0},
			End:   semanticapi.Position{Line: 33, Character: 14},
		},
		TargetSelectionRange: semanticapi.Range{
			Start: semanticapi.Position{Line: 31, Character: 4},
			End:   semanticapi.Position{Line: 31, Character: 7},
		},
	}}, result.LocationLinks)

	// The transient open must not leave the document cached as open.
	mgr.mu.Lock()
	_, cached := mgr.files[mainURI]
	mgr.mu.Unlock()
	assert.False(t, cached,
		"transient open must not cache the unopened file in m.files")
}

func TestE2EPythonUnopenedDocumentRequests(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	mgr, mainURI, _ := setupPythonManagerNoOpen(t, ctx, nil)

	td := semanticapi.TextDocumentIdentifier{URI: mainURI}
	// Position 39:13 is the `add` call in `result = add(1, 2)`.
	pos := semanticapi.Position{Line: 39, Character: 13}

	t.Run("Completion", func(t *testing.T) {
		_, err := mgr.Completion(ctx, semanticapi.CompletionParams{
			TextDocument: td, Position: pos,
		})
		require.NoError(t, err)
	})
	t.Run("SignatureHelp", func(t *testing.T) {
		_, err := mgr.SignatureHelp(ctx, semanticapi.SignatureHelpParams{
			TextDocument: td, Position: pos,
		})
		require.NoError(t, err)
	})
	t.Run("FoldingRange", func(t *testing.T) {
		_, err := mgr.FoldingRange(ctx, semanticapi.FoldingRangeParams{
			TextDocument: td,
		})
		require.NoError(t, err)
	})
	t.Run("SelectionRange", func(t *testing.T) {
		_, err := mgr.SelectionRange(ctx, semanticapi.SelectionRangeParams{
			TextDocument: td, Positions: []semanticapi.Position{pos},
		})
		require.NoError(t, err)
	})
	t.Run("SemanticTokensFull", func(t *testing.T) {
		_, err := mgr.SemanticTokensFull(ctx, semanticapi.SemanticTokensParams{
			TextDocument: td,
		})
		require.NoError(t, err)
	})
	t.Run("Diagnostic", func(t *testing.T) {
		_, err := mgr.Diagnostic(ctx, semanticapi.DocumentDiagnosticParams{
			TextDocument: td,
		})
		require.NoError(t, err)
	})
	t.Run("PrepareCallHierarchy", func(t *testing.T) {
		_, err := mgr.PrepareCallHierarchy(ctx, semanticapi.CallHierarchyPrepareParams{
			TextDocument: td, Position: pos,
		})
		require.NoError(t, err)
	})
	t.Run("PrepareRename", func(t *testing.T) {
		_, err := mgr.PrepareRename(ctx, semanticapi.PrepareRenameParams{
			TextDocument: td, Position: pos,
		})
		require.NoError(t, err)
	})
	t.Run("Rename", func(t *testing.T) {
		_, err := mgr.Rename(ctx, semanticapi.RenameParams{
			TextDocument: td, Position: pos, NewName: "add2",
		})
		require.NoError(t, err)
	})

	// None of the transient opens may leak into the open-file cache.
	mgr.mu.Lock()
	_, cached := mgr.files[mainURI]
	mgr.mu.Unlock()
	assert.False(t, cached,
		"transient opens must not cache the unopened file in m.files")
}

func TestE2EPythonPullDiagnosticsUnopenedFile(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mgr, mainURI, _ := setupPythonManagerNoOpen(t, ctx, nil)

	// bad.py carries one ty type error (invalid-argument-type on the
	// f("not an int") call) and one ruff lint finding (F401 unused
	// import). It is written to disk and never opened.
	dir := filepath.Dir(strings.TrimPrefix(mainURI, "file://"))
	badPath := filepath.Join(dir, "bad.py")
	src := "import sys\n\n\ndef f(x: int) -> int:\n    return x\n\n\nf(\"not an int\")\n"
	require.NoError(t, os.WriteFile(badPath, []byte(src), 0o644))
	badURI := "file://" + badPath

	report, err := mgr.Diagnostic(ctx, semanticapi.DocumentDiagnosticParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: badURI},
	})
	require.NoError(t, err)

	bySource := map[string][]semanticapi.Diagnostic{}
	for _, d := range report.Items {
		bySource[d.Source] = append(bySource[d.Source], d)
	}
	require.Len(t, bySource["ty"], 1,
		"expected ty's invalid-argument-type in the merged report, got: %+v",
		report.Items)
	assert.Equal(t, "invalid-argument-type", bySource["ty"][0].Code)
	assert.Equal(t, uint32(7), bySource["ty"][0].Range.Start.Line)
	require.Len(t, bySource["Ruff"], 1,
		"expected ruff's F401 in the merged report, got: %+v", report.Items)
	assert.Equal(t, "F401", bySource["Ruff"][0].Code)
	assert.Equal(t, uint32(0), bySource["Ruff"][0].Range.Start.Line)

	mgr.mu.Lock()
	_, cached := mgr.files[badURI]
	mgr.mu.Unlock()
	assert.False(t, cached,
		"transient open must not cache the unopened file in m.files")
}

// configArrayCallback answers workspace/configuration with a JSON null
// per requested item (a well-formed array reply), which ty requires
// before it will service workspace/diagnostic. The embedded
// testCallback provides every other callback method.
type configArrayCallback struct {
	*testCallback
}

func (c *configArrayCallback) Configuration(
	_ context.Context, p semanticapi.ConfigurationParams,
) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, len(p.Items))
	for i := range out {
		out[i] = json.RawMessage("null")
	}
	return out, nil
}

func TestE2EPythonWorkspaceDiagnosticMode(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	tyBin := findOnPath(t, "ty", pinnedTyVersion)
	ruffBin := findOnPath(t, "ruff", pinnedRuffVersion)
	tmpDir := setupTestWorkspace(t, filepath.Join("testdata", "py"))

	// bad.py carries a ty type error and is never opened.
	badPath := filepath.Join(tmpDir, "bad.py")
	src := "import sys\n\n\ndef f(x: int) -> int:\n    return x\n\n\nf(\"not an int\")\n"
	require.NoError(t, os.WriteFile(badPath, []byte(src), 0o644))
	badURI := "file://" + badPath

	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()
	// ty requests workspace/configuration during initialization and
	// defers workspace/diagnostic until it gets a well-formed reply: an
	// array with one entry per requested item. The reply must also
	// carry an explicit result member on the wire (the jsonrpc2 fix);
	// without it ty rejects the response and stalls the pull below.
	mgr := New(uri, scheme, scheme,
		&pyPkgManager{bins: []string{tyBin, ruffBin}},
		nil, nil,
		Config{Callback: &configArrayCallback{testCallback: &testCallback{}},
			MaxRetries: 1, NoInitializeServer: true})
	t.Cleanup(func() { _ = mgr.Close() })

	params := autoInitParams(uri.String())
	var caps map[string]any
	require.NoError(t, json.Unmarshal(params.Capabilities, &caps))
	td := caps["textDocument"].(map[string]any)
	td["diagnostic"] = map[string]any{
		"dynamicRegistration":    false,
		"relatedDocumentSupport": false,
	}
	ws := caps["workspace"].(map[string]any)
	ws["diagnostics"] = map[string]any{"refreshSupport": true}
	ws["configuration"] = true
	capsData, err := json.Marshal(caps)
	require.NoError(t, err)
	params.Capabilities = capsData
	initOpts, err := json.Marshal(map[string]any{
		"langID":         "python",
		"command":        "ty server",
		"diagnosticMode": "workspace",
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	// A first workspace/diagnostic pull against ty (the default child)
	// runs a full-project check and returns diagnostics for every file,
	// including the never-opened bad.py. Route directly to ty: ty
	// long-polls (suspends) subsequent pulls when unchanged and ruff
	// does not participate in workspace diagnostics, so the
	// merge-and-fan-out Manager.WorkspaceDiagnostic is out of scope for
	// this B1 check.
	srv, err := mgr.serverForURI(badURI)
	require.NoError(t, err)

	pullCtx, pullCancel := context.WithTimeout(ctx, 30*time.Second)
	defer pullCancel()
	var report semanticapi.WorkspaceDiagnosticReport
	// Leave PreviousResultIDs nil to also exercise the SDK marshaling
	// fix: a nil slice must serialize as [] (not null/omitted), which
	// ty requires.
	require.NoError(t, srv.call(pullCtx, "workspace/diagnostic",
		semanticapi.WorkspaceDiagnosticParams{}, &report))

	var found bool
	for _, item := range report.Items {
		if item.URI != badURI {
			continue
		}
		for _, d := range item.Items {
			if d.Code == "invalid-argument-type" {
				found = true
			}
		}
	}
	assert.True(t, found,
		"ty with diagnosticMode=workspace must report the unopened "+
			"bad.py type error in a workspace/diagnostic pull; got: %+v",
		report.Items)
}
