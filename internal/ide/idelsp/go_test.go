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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

const realLSPCloseTimeout = 15 * time.Second

func TestE2E(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "testdata")

	uri := makeURI(t, "file://"+tmpDir)

	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	mainText := string(mainContent)
	formattingMainText := strings.Replace(
		mainText,
		"\treturn fmt.Sprintf(\"Beep boop, I am robot %d\", r.ID)\n",
		"    return fmt.Sprintf(\"Beep boop, I am robot %d\", r.ID)\n",
		1,
	)
	require.NotEqual(t, mainText, formattingMainText)

	mainTestPath := filepath.Join(tmpDir, "main_test.go")
	mainTestContent, err := os.ReadFile(mainTestPath)
	require.NoError(t, err)

	utilPath := filepath.Join(tmpDir, "util.go")
	utilContent, err := os.ReadFile(utilPath)
	require.NoError(t, err)

	mainURI := "file://" + mainPath
	utilURI := "file://" + utilPath
	testURI := "file://" + mainTestPath

	mainWSURI, err := workspaceapi.ParseURI(mainURI)
	require.NoError(t, err)
	utilWSURI, err := workspaceapi.ParseURI(utilURI)
	require.NoError(t, err)
	testWSURI, err := workspaceapi.ParseURI(testURI)
	require.NoError(t, err)

	mainTextForTest := func(name string) string {
		if name == "Formatting" {
			return formattingMainText
		}
		return mainText
	}

	scheme := newTestScheme()
	var textcomp browserapi.ResourceOpener

	tests := []struct {
		name string
		fn   func(t *testing.T, mgr *Manager)
	}{
		{
			name: "Hover",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.Hover(t.Context(),
					semanticapi.HoverParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 26, Character: 20,
						},
					},
				)
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, semanticapi.MarkupKindMarkdown, result.Contents.Kind)
				assert.Contains(t, result.Contents.Value,
					"func (g *Greeter) Greet() string")
				assert.Contains(t, result.Contents.Value,
					"Greet returns a greeting message.")
				require.NotNil(t, result.Range)
				assert.Equal(t, semanticapi.Range{
					Start: semanticapi.Position{Line: 26, Character: 18},
					End:   semanticapi.Position{Line: 26, Character: 23},
				}, *result.Range)
			},
		},
		{
			name: "Definition",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.Definition(t.Context(),
					semanticapi.DefinitionParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 38, Character: 13,
						},
					},
				)
				locs := result.Locations
				require.NoError(t, err)
				require.Len(t, locs, 1)
				assert.Equal(t, mainURI, locs[0].URI)
				assert.Equal(t, semanticapi.Range{
					Start: semanticapi.Position{Line: 31, Character: 5},
					End:   semanticapi.Position{Line: 31, Character: 8},
				}, locs[0].Range)
			},
		},
		{
			name: "References",
			fn: func(t *testing.T, mgr *Manager) {
				// Add references
				locs, err := mgr.References(t.Context(),
					semanticapi.ReferenceParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 31, Character: 6,
						},
						Context: semanticapi.ReferenceContext{
							IncludeDeclaration: true,
						},
					},
				)
				require.NoError(t, err)
				require.Len(t, locs, 4)
				assert.ElementsMatch(t, []semanticapi.Location{
					{
						URI: mainURI,
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 31, Character: 5},
							End:   semanticapi.Position{Line: 31, Character: 8},
						},
					},
					{
						URI: mainURI,
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 38, Character: 13},
							End:   semanticapi.Position{Line: 38, Character: 16},
						},
					},
					{
						URI: testURI,
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 28, Character: 2},
							End:   semanticapi.Position{Line: 28, Character: 5},
						},
					},
					{
						URI: testURI,
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 21, Character: 4},
							End:   semanticapi.Position{Line: 21, Character: 7},
						},
					},
				}, locs)
			},
		},
		{
			name: "DocumentSymbol",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.DocumentSymbol(t.Context(),
					semanticapi.DocumentSymbolParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
					},
				)
				syms := result.SymbolInformation
				require.NoError(t, err)
				require.Len(t, syms, 7)
				names := make([]string, len(syms))
				for i, s := range syms {
					names[i] = s.Name
				}
				assert.Contains(t, names, "Greeter")
				assert.Contains(t, names, "Add")
				assert.Contains(t, names, "main")
				assert.Equal(t, semanticapi.SymbolInformation{
					Name: "Add",
					Kind: semanticapi.SymbolKindFunction,
					Location: semanticapi.Location{
						URI: mainURI,
						Range: semanticapi.Range{
							Start: semanticapi.Position{
								Character: 0,
								Line:      31,
							},
							End: semanticapi.Position{
								Character: 1,
								Line:      33,
							},
						},
					},
				}, syms[2])
			},
		},
		{
			name: "WorkspaceSymbol",
			fn: func(t *testing.T, mgr *Manager) {
				syms, err := mgr.WorkspaceSymbol(t.Context(),
					semanticapi.WorkspaceSymbolParams{
						Query: "main.Add",
					},
				)
				require.NoError(t, err)
				require.NotEmpty(t, syms)
				assert.Equal(t, semanticapi.SymbolInformation{
					Name: "main.Add",
					Kind: semanticapi.SymbolKindFunction,
					Location: semanticapi.Location{
						URI: mainURI,
						Range: semanticapi.Range{
							Start: semanticapi.Position{
								Character: 5,
								Line:      31,
							},
							End: semanticapi.Position{
								Character: 8,
								Line:      31,
							},
						},
					},
				}, syms[0])
			},
		},
		{
			name: "Completion",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.Completion(t.Context(),
					semanticapi.CompletionParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 27, Character: 12,
						},
					},
				)
				require.NoError(t, err)
				require.NotEmpty(t, result.Items)
				var sprintf *semanticapi.CompletionItem
				for i := range result.Items {
					if result.Items[i].Label == "Sprintf" {
						sprintf = &result.Items[i]
						break
					}
				}
				require.NotNil(t, sprintf, "expected Sprintf in completion list")
				assert.Equal(t, semanticapi.CompletionItemKindFunction, sprintf.Kind)
			},
		},
		{
			name: "Formatting",
			fn: func(t *testing.T, mgr *Manager) {
				edits, err := mgr.Formatting(t.Context(),
					semanticapi.DocumentFormattingParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Options: semanticapi.FormattingOptions{
							TabSize:      4,
							InsertSpaces: false,
						},
					},
				)
				require.NoError(t, err)
				assert.Equal(t, []semanticapi.TextEdit{{
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 54, Character: 0},
						End:   semanticapi.Position{Line: 54, Character: 4},
					},
					NewText: "\t",
				}}, edits)
			},
		},
		{
			name: "FoldingRange",
			fn: func(t *testing.T, mgr *Manager) {
				ranges, err := mgr.FoldingRange(t.Context(),
					semanticapi.FoldingRangeParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
					},
				)
				require.NoError(t, err)
				require.NotEmpty(t, ranges)
				var addFold *semanticapi.FoldingRange
				for i := range ranges {
					if ranges[i].StartLine == 38 {
						addFold = &ranges[i]
						break
					}
				}
				require.NotNil(t, addFold, "expected folding range for Add")
				assert.Equal(t, uint32(38), addFold.StartLine)
			},
		},
		{
			// references to the symbol scoped to this file, like references
			// but only for the same file.
			name: "DocumentHighlight",
			fn: func(t *testing.T, mgr *Manager) {
				highlights, err := mgr.DocumentHighlight(t.Context(),
					semanticapi.DocumentHighlightParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 31, Character: 5,
						},
					},
				)
				require.NoError(t, err)
				require.NotEmpty(t, highlights)
				assert.GreaterOrEqual(t, len(highlights), 2)
			},
		},
		{
			// test that we can rename at position before calling Rename
			name: "PrepareRename",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.PrepareRename(t.Context(),
					semanticapi.PrepareRenameParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 31, Character: 5,
						},
					},
				)
				require.NoError(t, err)
				require.NotNil(t, result)
				assert.Equal(t, "Add", result.Placeholder)
				assert.Equal(t, semanticapi.Range{
					Start: semanticapi.Position{Line: 31, Character: 5},
					End:   semanticapi.Position{Line: 31, Character: 8},
				}, result.Range)
			},
		},
		{
			name: "Rename",
			fn: func(t *testing.T, mgr *Manager) {
				edit, err := mgr.Rename(t.Context(),
					semanticapi.RenameParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: utilURI,
						},
						Position: semanticapi.Position{
							Line: 19, Character: 5,
						},
						NewName: "Mul",
					},
				)
				require.NoError(t, err)
				require.NotNil(t, edit)
				require.NotEmpty(t, edit.Changes)
				edits, ok := edit.Changes[utilURI]
				require.True(t, ok, "expected edits in util.go")
				require.NotEmpty(t, edits)
				for _, e := range edits {
					assert.Equal(t, "Mul", e.NewText)
				}
			},
		},
		{
			name: "RenameConflict",
			fn: func(t *testing.T, mgr *Manager) {
				_, err := mgr.Rename(t.Context(), semanticapi.RenameParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: utilURI},
					Position:     semanticapi.Position{Line: 24, Character: 5},
					NewName:      "Multiply",
				})
				require.Error(t, err)
				assert.Contains(t, err.Error(), "conflicts with func in same block")
			},
		},
		{
			// given a set of positions, selection ranges that user
			// might be interested in selecting
			name: "SelectionRange",
			fn: func(t *testing.T, mgr *Manager) {
				ranges, err := mgr.SelectionRange(t.Context(),
					semanticapi.SelectionRangeParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Positions: []semanticapi.Position{
							{Line: 32, Character: 1},
						},
					},
				)
				require.NoError(t, err)
				require.Len(t, ranges, 1)
			},
		},
		{
			name: "SemanticTokensRange",
			fn: func(t *testing.T, mgr *Manager) {
				tokens, err := mgr.SemanticTokensRange(t.Context(),
					semanticapi.SemanticTokensRangeParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 2, Character: 0},
							End:   semanticapi.Position{Line: 12, Character: 0},
						},
					},
				)
				require.NoError(t, err)
				require.NotNil(t, tokens)
			},
		},
		{
			// required to call CallHierarchyIncoming/Outgoing calls
			name: "PrepareCallHierarchy",
			fn: func(t *testing.T, mgr *Manager) {
				items, err := mgr.PrepareCallHierarchy(t.Context(),
					semanticapi.CallHierarchyPrepareParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 31, Character: 5,
						},
					},
				)
				require.NoError(t, err)
				require.Len(t, items, 1)
				assert.Equal(t, "Add", items[0].Name)
				assert.Equal(t, semanticapi.SymbolKindFunction, items[0].Kind)
			},
		},
		{
			// what functions are alling this
			name: "CallHierarchyIncomingCalls",
			fn: func(t *testing.T, mgr *Manager) {
				items, err := mgr.PrepareCallHierarchy(t.Context(),
					semanticapi.CallHierarchyPrepareParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 31, Character: 5,
						},
					},
				)
				require.NoError(t, err)
				require.NotEmpty(t, items)

				calls, err := mgr.CallHierarchyIncomingCalls(t.Context(),
					semanticapi.CallHierarchyIncomingCallsParams{
						Item: items[0],
					},
				)
				require.NoError(t, err)
				require.Len(t, calls, 3)
				assert.Equal(t, "main", calls[0].From.Name)
			},
		},
		{
			// what functions is this calling
			name: "CallHierarchyOutgoingCalls",
			fn: func(t *testing.T, mgr *Manager) {
				items, err := mgr.PrepareCallHierarchy(t.Context(),
					semanticapi.CallHierarchyPrepareParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 35, Character: 5,
						},
					},
				)
				require.NoError(t, err)
				require.NotEmpty(t, items)

				calls, err := mgr.CallHierarchyOutgoingCalls(t.Context(),
					semanticapi.CallHierarchyOutgoingCallsParams{
						Item: items[0],
					},
				)
				require.NoError(t, err)
				require.Len(t, calls, 3)
				var found bool
				for _, c := range calls {
					if c.To.Name == "Add" {
						found = true
						break
					}
				}
				assert.True(t, found, "expected outgoing call to Add")
			},
		},
		{
			name: "CodeAction",
			fn: func(t *testing.T, mgr *Manager) {
				actions, err := mgr.CodeAction(t.Context(),
					semanticapi.CodeActionParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 2, Character: 0},
							End:   semanticapi.Position{Line: 20, Character: 0},
						},
					},
				)
				require.NoError(t, err)
				require.NotEmpty(t, actions)
				assert.Len(t, actions, 5)
			},
		},
		{
			name: "TypeDefinition",
			fn: func(t *testing.T, mgr *Manager) {
				// TypeDefinition of variable "g" at line 43
				result, err := mgr.TypeDefinition(t.Context(),
					semanticapi.TypeDefinitionParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 36, Character: 1,
						},
					},
				)
				require.NoError(t, err)
				locs := result.Locations
				require.Len(t, locs, 1)
				assert.Equal(t, mainURI, locs[0].URI)
				assert.Equal(t, semanticapi.Range{
					Start: semanticapi.Position{Line: 21, Character: 5},
					End:   semanticapi.Position{Line: 21, Character: 12},
				}, locs[0].Range)
			},
		},
		{
			name: "SignatureHelp",
			fn: func(t *testing.T, mgr *Manager) {
				result, err := mgr.SignatureHelp(t.Context(),
					semanticapi.SignatureHelpParams{
						TextDocument: semanticapi.TextDocumentIdentifier{
							URI: mainURI,
						},
						Position: semanticapi.Position{
							Line: 38, Character: 17,
						},
					},
				)
				require.NoError(t, err)
				assert.ElementsMatch(t, []semanticapi.SignatureInformation{
					{
						Label: "Add(a int, b int) int",
						Documentation: &semanticapi.MarkupContent{
							Kind:  "markdown",
							Value: "Add adds two integers.",
						},
						Parameters: []semanticapi.ParameterInformation{
							{
								Label:         "a int",
								LabelOffsets:  nil,
								Documentation: nil,
							},
							{
								Label:         "b int",
								LabelOffsets:  nil,
								Documentation: nil,
							},
						},
					},
				}, result.Signatures)
			},
		},
	}

	t.Run("lazyly initialized via Handle EventTypeOpen file", func(t *testing.T) {
		t.Parallel()
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				var wg sync.WaitGroup
				var ready sync.Once
				cfg := Config{
					MaxRetries:   1,
					CloseTimeout: realLSPCloseTimeout,
					Callback: &testCallback{
						onShowMessage: func(params semanticapi.ShowMessageParams) {
							if strings.Contains(params.Message, "Finished loading packages") {
								ready.Do(wg.Done)
							}
						},
						onProgress: readyOnProgress(&ready, &wg),
					},
				}
				mgr := New(
					uri,
					scheme,
					scheme,
					&stubPkgManager{bin: goplsBin},
					nil, // notifications
					textcomp,
					cfg,
				)
				t.Cleanup(func() { _ = mgr.Close() })

				ctx := context.Background()

				wg.Add(1)
				mgr.Handle(ctx, textapi.Event{
					Type:    textapi.EventTypeOpen,
					URI:     mainWSURI,
					Content: mainTextForTest(tt.name),
				})
				mgr.Handle(ctx, textapi.Event{
					Type:    textapi.EventTypeOpen,
					URI:     utilWSURI,
					Content: string(utilContent),
				})
				mgr.Handle(ctx, textapi.Event{
					Type:    textapi.EventTypeOpen,
					URI:     testWSURI,
					Content: string(mainTestContent),
				})

				wg.Wait()
				tt.fn(t, mgr)
				require.NoError(t, mgr.Close())
			})
		}
	})

	t.Run("initialized via lsp.Initialize", func(t *testing.T) {
		t.Parallel()
		// the following set of the API is only supported via
		// Initialize, w hich allows configuring gopls with custom configuration.
		initializedTests := make([]struct {
			name string
			fn   func(t *testing.T, mgr *Manager)
		}, len(tests))
		copy(initializedTests, tests)
		initializedTests = append(initializedTests, []struct {
			name string
			fn   func(t *testing.T, mgr *Manager)
		}{
			{
				name: "Declaration",
				fn: func(t *testing.T, mgr *Manager) {
					// Declaration of "Add" at its call site (line 36, char 13).
					// gopls doesn't support textDocument/declaration for Go.
					_, err := mgr.Declaration(t.Context(),
						semanticapi.DeclarationParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
							Position: semanticapi.Position{
								Line: 29, Character: 13,
							},
						},
					)
					// gopls returns "method not found" for declaration.
					require.Error(t, err)
					assert.Contains(t, err.Error(), "Declaration")
				},
			},
			{
				name: "Implementation",
				fn: func(t *testing.T, mgr *Manager) {
					// Implementation on Speaker.Speak interface method (line 42, char 1).
					// Should return the Robot.Speak implementation.
					result, err := mgr.Implementation(t.Context(),
						semanticapi.ImplementationParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
							Position: semanticapi.Position{
								Line: 44, Character: 1,
							},
						},
					)
					require.NoError(t, err)
					locs := result.Locations
					require.Len(t, locs, 1)
					assert.Equal(t, mainURI, locs[0].URI)
					// Robot.Speak is at line 60, chars 16-21
					assert.Equal(t, semanticapi.Range{
						Start: semanticapi.Position{Line: 53, Character: 16},
						End:   semanticapi.Position{Line: 53, Character: 21},
					}, locs[0].Range)
				},
			},
			{
				name: "CodeLens",
				fn: func(t *testing.T, mgr *Manager) {
					// Request code lenses for the test file.
					lenses, err := mgr.CodeLens(t.Context(),
						semanticapi.CodeLensParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: testURI,
							},
						},
					)
					require.NoError(t, err)
					expected := []semanticapi.CodeLens{
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 16, Character: 0},
								End:   semanticapi.Position{Line: 16, Character: 0},
							},
							Command: &semanticapi.Command{
								Title:   "run file benchmarks",
								Command: "gopls.run_tests",
								Arguments: []json.RawMessage{
									json.RawMessage(
										fmt.Sprintf(`{"URI":"%s","Tests":null,"Benchmarks":["BenchmarkAdd"]}`, testURI)),
									json.RawMessage(`{"source":"codelens"}`),
								},
							},
						},
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 20, Character: 0},
								End:   semanticapi.Position{Line: 20, Character: 0},
							},
							Command: &semanticapi.Command{
								Title:   "run test",
								Command: "gopls.run_tests",
								Arguments: []json.RawMessage{
									json.RawMessage(
										fmt.Sprintf(`{"URI":"%s","Tests":["TestAdd"],"Benchmarks":null}`, testURI)),
									json.RawMessage(`{"source":"codelens"}`),
								},
							},
						},
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 26, Character: 0},
								End:   semanticapi.Position{Line: 26, Character: 0},
							},
							Command: &semanticapi.Command{
								Title:   "run benchmark",
								Command: "gopls.run_tests",
								Arguments: []json.RawMessage{
									json.RawMessage(
										fmt.Sprintf(`{"URI":"%s","Tests":null,"Benchmarks":["BenchmarkAdd"]}`, testURI)),
									json.RawMessage(`{"source":"codelens"}`),
								},
							},
						},
					}
					require.Equal(t, expected, lenses)

				},
			},
			{
				name: "RangeFormatting",
				fn: func(t *testing.T, mgr *Manager) {
					// RangeFormatting on the Add function (lines 29-31).
					_, err := mgr.RangeFormatting(t.Context(),
						semanticapi.DocumentRangeFormattingParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 22, Character: 0},
								End:   semanticapi.Position{Line: 25, Character: 0},
							},
							Options: semanticapi.FormattingOptions{
								TabSize:      4,
								InsertSpaces: false,
							},
						},
					)
					// gopls doesn't support rangeFormatting.
					require.Error(t, err)
					assert.Contains(t, err.Error(), "RangeFormatting")
				},
			},
			{
				name: "Diagnostic",
				fn: func(t *testing.T, mgr *Manager) {
					// Introduce an error by adding an unused variable.
					// Send a didChange to add "unused := 42" at line 35.
					err := mgr.DidChange(t.Context(),
						semanticapi.DidChangeTextDocumentParams{
							TextDocument: semanticapi.VersionedTextDocumentIdentifier{
								URI:     utilURI,
								Version: 2,
							},
							ContentChanges: []semanticapi.TextDocumentContentChangeEvent{
								{
									// Replace entire content with error code.
									Text: `package main

// Broken function with unused variable.
func Broken() {
	unused := 42
}
`,
								},
							},
						},
					)
					require.NoError(t, err)

					// Wait for gopls to process the change.
					time.Sleep(500 * time.Millisecond)

					// Pull diagnostics for the file with error.
					report, err := mgr.Diagnostic(t.Context(),
						semanticapi.DocumentDiagnosticParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: utilURI,
							},
						},
					)
					require.NoError(t, err)
					// Should have at least one diagnostic for unused variable.
					require.NotEmpty(t, report.Items)
					// Verify the diagnostic for unused variable.
					diag := report.Items[0]
					assert.Equal(t, semanticapi.DiagnosticSeverityError, diag.Severity)
					assert.Contains(t, diag.Message, "declared and not used")
					assert.Equal(t, uint32(4), diag.Range.Start.Line)
				},
			},
			{
				name: "WorkspaceDiagnostic",
				fn: func(t *testing.T, mgr *Manager) {
					// Introduce a broken file inline so the shared
					// testdata workspace stays clean for other tests.
					brokenCode := "package main\n\n" +
						"func CallBroken() string {\n" +
						"\tg := &Greeter{Name: \"test\"}\n" +
						"\treturn g.NonExistentMethod()\n}\n"
					brokenPath := filepath.Join(tmpDir, "broken.go")
					require.NoError(t,
						os.WriteFile(brokenPath, []byte(brokenCode), 0644),
					)
					t.Cleanup(func() { _ = os.Remove(brokenPath) })
					fileURI := "file://" + brokenPath

					require.NoError(t, mgr.DidOpen(t.Context(),
						semanticapi.DidOpenTextDocumentParams{
							TextDocument: semanticapi.TextDocumentItem{
								URI:        fileURI,
								LanguageID: "go",
								Version:    0,
								Text:       brokenCode,
							},
						},
					))
					// Wait for gopls to analyze the file.
					time.Sleep(500 * time.Millisecond)

					report, err := mgr.WorkspaceDiagnostic(
						t.Context(),
						semanticapi.WorkspaceDiagnosticParams{},
					)
					if err != nil {
						if strings.Contains(err.Error(), "not yet implemented") ||
							strings.Contains(err.Error(), "method not found") {
							t.Skip("gopls does not support workspace/diagnostic yet")
						}
						t.Fatal(err)
					}
					// Find a report item for broken.go.
					var found bool
					for _, item := range report.Items {
						if item.URI != fileURI {
							continue
						}
						found = true
						require.NotEmpty(t, item.Items,
							"expected diagnostics for broken.go",
						)
						diag := item.Items[0]
						assert.Equal(t,
							semanticapi.DiagnosticSeverityError,
							diag.Severity,
						)
						assert.Contains(t, diag.Message,
							"NonExistentMethod",
						)
					}
					require.True(t, found,
						"workspace diagnostics should include broken.go",
					)
				},
			},
			{
				name: "ExecuteCommand",
				fn: func(t *testing.T, mgr *Manager) {
					arg := map[string]string{"URI": mainURI}
					argBytes, _ := json.Marshal(arg)
					result, err := mgr.ExecuteCommand(t.Context(),
						semanticapi.ExecuteCommandParams{
							Command:   "gopls.list_known_packages",
							Arguments: []json.RawMessage{argBytes},
						},
					)
					require.NoError(t, err)
					assert.Contains(t, result, "Packages")
					assert.Contains(t, result, "fmt")
				},
			},
			{
				name: "ExecuteCommand/unsupported",
				fn: func(t *testing.T, mgr *Manager) {
					_, err := mgr.ExecuteCommand(
						t.Context(),
						semanticapi.ExecuteCommandParams{
							Command: "gopls.doc.features",
						},
					)
					require.Error(t, err)
				},
			},
			{
				name: "CompletionResolve",
				fn: func(t *testing.T, mgr *Manager) {
					// CompletionResolve returns item unchanged in this impl.
					item := semanticapi.CompletionItem{
						Label:  "testFunction",
						Kind:   semanticapi.CompletionItemKindFunction,
						Detail: "func testFunction()",
					}
					resolved, err := mgr.CompletionResolve(t.Context(), item)
					require.NoError(t, err)
					assert.Equal(t, item, resolved)
				},
			},
			{
				name: "CodeLensResolve",
				fn: func(t *testing.T, mgr *Manager) {
					// CodeLensResolve returns lens unchanged in this impl.
					lens := semanticapi.CodeLens{
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 26, Character: 0},
							End:   semanticapi.Position{Line: 26, Character: 4},
						},
						Command: &semanticapi.Command{
							Title:   "run",
							Command: "gopls.run",
						},
					}
					resolved, err := mgr.CodeLensResolve(t.Context(), lens)
					require.NoError(t, err)
					assert.Equal(t, lens, resolved)
				},
			},
			{
				name: "DocumentColor",
				fn: func(t *testing.T, mgr *Manager) {
					colors, err := mgr.DocumentColor(t.Context(),
						semanticapi.DocumentColorParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
						},
					)
					require.NoError(t, err)
					// Go files have no color literals; expect empty.
					assert.Empty(t, colors)
				},
			},
			{
				name: "ColorPresentation",
				fn: func(t *testing.T, mgr *Manager) {
					presentations, err := mgr.ColorPresentation(t.Context(),
						semanticapi.ColorPresentationParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
							Color: semanticapi.Color{
								Red: 1.0, Green: 0.0, Blue: 0.0, Alpha: 1.0,
							},
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 9, Character: 7},
								End:   semanticapi.Position{Line: 9, Character: 12},
							},
						},
					)
					require.NoError(t, err)
					// Go doesn't have color literals; expect empty.
					assert.Empty(t, presentations)
				},
			},
			{
				name: "DocumentLink",
				fn: func(t *testing.T, mgr *Manager) {
					links, err := mgr.DocumentLink(t.Context(),
						semanticapi.DocumentLinkParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
						},
					)
					require.NoError(t, err)
					// gopls returns links for import paths and URLs in comments
					require.Len(t, links, 2)
					assert.Equal(t, semanticapi.DocumentLink{
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 18, Character: 8},
							End:   semanticapi.Position{Line: 18, Character: 11},
						},
						Target: "https://pkg.go.dev/fmt",
					}, links[0])
					// The GPL license header carries a gnu.org URL.
					assert.Equal(t, semanticapi.DocumentLink{
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 14, Character: 41},
							End:   semanticapi.Position{Line: 14, Character: 70},
						},
						Target: "https://www.gnu.org/licenses/",
					}, links[1])
				},
			},
			{
				name: "DocumentLinkResolve",
				fn: func(t *testing.T, mgr *Manager) {
					link := semanticapi.DocumentLink{
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 18, Character: 7},
							End:   semanticapi.Position{Line: 18, Character: 12},
						},
						Target: "fmt",
					}
					resolved, err := mgr.DocumentLinkResolve(t.Context(), link)
					require.NoError(t, err)
					assert.Equal(t, link, resolved)
				},
			},
			{
				name: "OnTypeFormatting",
				fn: func(t *testing.T, mgr *Manager) {
					// gopls doesn't support onTypeFormatting.
					edits, err := mgr.OnTypeFormatting(t.Context(),
						semanticapi.DocumentOnTypeFormattingParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
							Position: semanticapi.Position{
								Line: 23, Character: 0,
							},
							Character: "\n",
							Options: semanticapi.FormattingOptions{
								TabSize:      4,
								InsertSpaces: false,
							},
						},
					)
					require.NoError(t, err)
					// gopls returns nil for unsupported features.
					assert.Nil(t, edits)
				},
			},
			{
				name: "LinkedEditingRange",
				fn: func(t *testing.T, mgr *Manager) {
					// Go doesn't have linked editing ranges (like HTML tags).
					ranges, err := mgr.LinkedEditingRange(t.Context(),
						semanticapi.LinkedEditingRangeParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
							Position: semanticapi.Position{
								Line: 22, Character: 5,
							},
						},
					)
					require.NoError(t, err)
					// Go files have no linked editing ranges.
					assert.Nil(t, ranges)
				},
			},
			{
				name: "Moniker",
				fn: func(t *testing.T, mgr *Manager) {
					// gopls doesn't fully support monikers.
					monikers, err := mgr.Moniker(t.Context(),
						semanticapi.MonikerParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
							Position: semanticapi.Position{
								Line: 22, Character: 5,
							},
						},
					)
					require.NoError(t, err)
					// gopls returns nil for unsupported features.
					assert.Nil(t, monikers)
				},
			},
			{
				name: "WillSaveWaitUntil",
				fn: func(t *testing.T, mgr *Manager) {
					// gopls doesn't support willSaveWaitUntil.
					edits, err := mgr.WillSaveWaitUntil(t.Context(),
						semanticapi.WillSaveTextDocumentParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
							Reason: semanticapi.TextDocumentSaveReasonManual,
						},
					)
					require.NoError(t, err)
					// gopls returns nil for unsupported features.
					assert.Nil(t, edits)
				},
			},
			{
				name: "SemanticTokensFullDelta",
				fn: func(t *testing.T, mgr *Manager) {
					// gopls doesn't support semanticTokens/full/delta.
					_, err := mgr.SemanticTokensFullDelta(t.Context(),
						semanticapi.SemanticTokensDeltaParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
							PreviousResultID: "",
						},
					)
					require.Error(t, err)
					assert.Contains(t, err.Error(), "SemanticTokensFullDelta")
				},
			},
			{
				name: "PrepareTypeHierarchy",
				fn: func(t *testing.T, mgr *Manager) {
					// Prepare type hierarchy on Greeter struct (line 28, char 6).
					items, err := mgr.PrepareTypeHierarchy(t.Context(),
						semanticapi.TypeHierarchyPrepareParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
							Position: semanticapi.Position{
								Line: 21, Character: 6,
							},
						},
					)
					require.NoError(t, err)
					require.Len(t, items, 1)
					assert.Equal(t, "Greeter", items[0].Name)
					// gopls returns SymbolKindClass for Go structs.
					assert.Equal(t, semanticapi.SymbolKindClass, items[0].Kind)
					assert.Equal(t, mainURI, items[0].URI)
					assert.Equal(t, semanticapi.Range{
						Start: semanticapi.Position{Line: 21, Character: 5},
						End:   semanticapi.Position{Line: 21, Character: 12},
					}, items[0].SelectionRange)
				},
			},
			{
				name: "TypeHierarchySupertypes",
				fn: func(t *testing.T, mgr *Manager) {
					// Create a synthetic item since PrepareTypeHierarchy returns nil.
					// This tests that the call works even with no real data.
					item := semanticapi.TypeHierarchyItem{
						Name: "Greeter",
						Kind: semanticapi.SymbolKindStruct,
						URI:  mainURI,
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 21, Character: 0},
							End:   semanticapi.Position{Line: 24, Character: 1},
						},
						SelectionRange: semanticapi.Range{
							Start: semanticapi.Position{Line: 21, Character: 5},
							End:   semanticapi.Position{Line: 21, Character: 12},
						},
					}
					supertypes, err := mgr.TypeHierarchySupertypes(t.Context(),
						semanticapi.TypeHierarchySupertypesParams{
							Item: item,
						},
					)
					require.NoError(t, err)
					// Greeter has no supertypes (no embedded types).
					assert.Nil(t, supertypes)
				},
			},
			{
				name: "TypeHierarchySubtypes",
				fn: func(t *testing.T, mgr *Manager) {
					// Create a synthetic item since PrepareTypeHierarchy returns nil.
					item := semanticapi.TypeHierarchyItem{
						Name: "Greeter",
						Kind: semanticapi.SymbolKindStruct,
						URI:  mainURI,
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 12, Character: 0},
							End:   semanticapi.Position{Line: 15, Character: 1},
						},
						SelectionRange: semanticapi.Range{
							Start: semanticapi.Position{Line: 12, Character: 5},
							End:   semanticapi.Position{Line: 12, Character: 12},
						},
					}
					subtypes, err := mgr.TypeHierarchySubtypes(t.Context(),
						semanticapi.TypeHierarchySubtypesParams{
							Item: item,
						},
					)
					require.NoError(t, err)
					// Greeter has no subtypes.
					assert.Nil(t, subtypes)
				},
			},
			{
				name: "InlayHint",
				fn: func(t *testing.T, mgr *Manager) {
					hints, err := mgr.InlayHint(t.Context(),
						semanticapi.InlayHintParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 0, Character: 0},
								End:   semanticapi.Position{Line: 53, Character: 0},
							},
						},
					)
					require.NoError(t, err)
					expected := []semanticapi.InlayHint{
						{
							Position:     semanticapi.Position{Line: 27, Character: 20},
							LabelParts:   []semanticapi.InlayHintLabelPart{{Value: "format:"}},
							Kind:         semanticapi.InlayHintKindParameter,
							PaddingRight: true,
						},
						{
							Position:     semanticapi.Position{Line: 27, Character: 34},
							LabelParts:   []semanticapi.InlayHintLabelPart{{Value: "a...:"}},
							Kind:         semanticapi.InlayHintKindParameter,
							PaddingRight: true,
						},
						{
							Position:     semanticapi.Position{Line: 37, Character: 13},
							LabelParts:   []semanticapi.InlayHintLabelPart{{Value: "a...:"}},
							Kind:         semanticapi.InlayHintKindParameter,
							PaddingRight: true,
						},
						{
							Position:     semanticapi.Position{Line: 38, Character: 13},
							LabelParts:   []semanticapi.InlayHintLabelPart{{Value: "a...:"}},
							Kind:         semanticapi.InlayHintKindParameter,
							PaddingRight: true,
						},
						{
							Position:     semanticapi.Position{Line: 38, Character: 17},
							LabelParts:   []semanticapi.InlayHintLabelPart{{Value: "a:"}},
							Kind:         semanticapi.InlayHintKindParameter,
							PaddingRight: true,
						},
						{
							Position:     semanticapi.Position{Line: 38, Character: 20},
							LabelParts:   []semanticapi.InlayHintLabelPart{{Value: "b:"}},
							Kind:         semanticapi.InlayHintKindParameter,
							PaddingRight: true,
						},
						{
							Position:     semanticapi.Position{Line: 54, Character: 20},
							LabelParts:   []semanticapi.InlayHintLabelPart{{Value: "format:"}},
							Kind:         semanticapi.InlayHintKindParameter,
							PaddingRight: true,
						},
						{
							Position:     semanticapi.Position{Line: 54, Character: 48},
							LabelParts:   []semanticapi.InlayHintLabelPart{{Value: "a...:"}},
							Kind:         semanticapi.InlayHintKindParameter,
							PaddingRight: true,
						},
						{
							Position:    semanticapi.Position{Line: 36, Character: 2},
							LabelParts:  []semanticapi.InlayHintLabelPart{{Value: "*Greeter"}},
							Kind:        semanticapi.InlayHintKindType,
							PaddingLeft: true,
						},
					}
					assert.ElementsMatch(t, expected, hints)
				},
			},
			{
				name: "InlayHintResolve",
				fn: func(t *testing.T, mgr *Manager) {
					hint := semanticapi.InlayHint{
						Position: semanticapi.Position{Line: 29, Character: 17},
						Label:    "a:",
						Kind:     semanticapi.InlayHintKindParameter,
					}
					resolved, err := mgr.InlayHintResolve(t.Context(), hint)
					require.NoError(t, err)
					assert.Equal(t, hint, resolved)
				},
			},
			{
				name: "InlineValue",
				fn: func(t *testing.T, mgr *Manager) {
					// gopls doesn't support inlineValue (debugger feature).
					values, err := mgr.InlineValue(t.Context(),
						semanticapi.InlineValueParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 22, Character: 0},
								End:   semanticapi.Position{Line: 25, Character: 0},
							},
						},
					)
					require.NoError(t, err)
					// gopls returns nil for unsupported features.
					assert.Nil(t, values)
				},
			},
			{
				name: "WillCreateFiles",
				fn: func(t *testing.T, mgr *Manager) {
					// gopls doesn't support willCreateFiles.
					_, err := mgr.WillCreateFiles(t.Context(),
						semanticapi.CreateFilesParams{
							Files: []semanticapi.FileCreate{
								{URI: "file:///tmp/newfile.go"},
							},
						},
					)
					require.Error(t, err)
					assert.Contains(t, err.Error(), "WillCreateFiles")
				},
			},
			{
				name: "WillRenameFiles",
				fn: func(t *testing.T, mgr *Manager) {
					// gopls doesn't support willRenameFiles.
					_, err := mgr.WillRenameFiles(t.Context(),
						semanticapi.RenameFilesParams{
							Files: []semanticapi.FileRename{
								{
									OldURI: utilURI,
									NewURI: "file:///tmp/renamed.go",
								},
							},
						},
					)
					require.Error(t, err)
					assert.Contains(t, err.Error(), "WillRenameFiles")
				},
			},
			{
				name: "WillDeleteFiles",
				fn: func(t *testing.T, mgr *Manager) {
					// gopls doesn't support willDeleteFiles.
					_, err := mgr.WillDeleteFiles(t.Context(),
						semanticapi.DeleteFilesParams{
							Files: []semanticapi.FileDelete{
								{URI: "file:///tmp/todelete.go"},
							},
						},
					)
					require.Error(t, err)
					assert.Contains(t, err.Error(), "WillDeleteFiles")
				},
			},
			{
				name: "SemanticTokensFull",
				fn: func(t *testing.T, mgr *Manager) {
					tokens, err := mgr.SemanticTokensFull(t.Context(),
						semanticapi.SemanticTokensParams{
							TextDocument: semanticapi.TextDocumentIdentifier{
								URI: mainURI,
							},
						},
					)
					require.NoError(t, err)
					require.NotNil(t, tokens)
					assert.Equal(t, 490, len(tokens.Data))
				},
			},
		}...)
		for _, tt := range initializedTests {
			t.Run(tt.name, func(t *testing.T) {
				var wg sync.WaitGroup
				var ready sync.Once
				cfg := Config{
					MaxRetries:   1,
					CloseTimeout: realLSPCloseTimeout,
					Callback: &testCallback{
						onShowMessage: func(params semanticapi.ShowMessageParams) {
							if strings.Contains(params.Message, "Finished loading packages") ||
								strings.Contains(params.Message, "background refresh finished") {
								ready.Do(wg.Done)
							}
						},
						onProgress: readyOnProgress(&ready, &wg),
					},
				}
				mgr := New(
					uri,
					scheme,
					scheme,
					&stubPkgManager{bin: goplsBin},
					nil, // notifications
					textcomp,
					cfg,
				)
				t.Cleanup(func() { _ = mgr.Close() })

				ctx := context.Background()

				params := autoInitParams(uri.String())
				initializeOpts, err := json.Marshal(map[string]any{
					"semanticTokens": true,
					"langID":         "go",
					"command":        "gopls serve",
					"codelenses": map[string]bool{
						"gc_details":         true,
						"generate":           true,
						"regenerate_cgo":     true,
						"run_govulncheck":    true,
						"test":               true,
						"tidy":               true,
						"upgrade_dependency": true,
						"vendor":             true,
					},
					"hints": map[string]bool{
						"assignVariableTypes":    true,
						"compositeLiteralFields": true,
						"compositeLiteralTypes":  true,
						"constantValues":         true,
						"functionTypeParameters": true,
						"parameterNames":         true,
						"rangeVariableTypes":     true,
					},
				})
				require.NoError(t, err)
				params.InitializeOptions = initializeOpts

				wg.Add(1)
				_, err = mgr.Initialize(ctx, params)
				require.NoError(t, err)
				require.NoError(t, mgr.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
					TextDocument: semanticapi.TextDocumentItem{
						URI:        mainURI,
						LanguageID: "go",
						Version:    0,
						Text:       mainTextForTest(tt.name),
					},
				}))
				require.NoError(t, mgr.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
					TextDocument: semanticapi.TextDocumentItem{
						URI:        utilURI,
						LanguageID: "go",
						Version:    0,
						Text:       string(utilContent),
					},
				}))
				require.NoError(t, mgr.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
					TextDocument: semanticapi.TextDocumentItem{
						URI:        testURI,
						LanguageID: "go",
						Version:    0,
						Text:       string(mainTestContent),
					},
				}))

				wg.Wait()
				tt.fn(t, mgr)
				require.NoError(t, mgr.Close())
			})
		}
	})
}

func TestE2ECallback(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "")

	mainPath := filepath.Join(tmpDir, "main.go")
	testURI := "file://" + mainPath

	errorContent := `package main

func broken() {
	unused := 42
}
`

	ctx, cancel := context.WithTimeout(
		context.Background(), 10*time.Second,
	)
	defer cancel()

	var wg sync.WaitGroup
	var receivedDiagnostics []semanticapi.PublishDiagnosticsParams
	callback := &testCallback{
		onDiagnostics: func(p semanticapi.PublishDiagnosticsParams) {
			if len(p.Diagnostics) != 0 {
				defer wg.Done()
				receivedDiagnostics = append(receivedDiagnostics, p)
			}
		},
	}

	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()
	var opener browserapi.ResourceOpener
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpDir, "go.mod"),
		[]byte("module testmod\n\ngo 1.21\n"), 0644,
	))
	err := os.WriteFile(mainPath, []byte(errorContent), 0777)
	require.NoError(t, err)

	mgr := New(
		uri,
		scheme,
		scheme,
		&stubPkgManager{bin: goplsBin},
		nil, // notifications
		opener,
		Config{Callback: callback, MaxRetries: 1},
	)
	t.Cleanup(func() { _ = mgr.Close() })

	params := autoInitParams(uri.String())
	initOptions := []byte(`{"langID": "go", "command": "gopls serve"}`)
	params.InitializeOptions = initOptions
	wg.Add(1)
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)
	require.NoError(t, mgr.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
		TextDocument: semanticapi.TextDocumentItem{
			URI:        testURI,
			LanguageID: "go",
			Version:    0,
			Text:       errorContent,
		},
	}))
	wg.Wait()

	require.NotNil(t, receivedDiagnostics, "expected to receive diagnostics for main.go")
	require.Len(t, receivedDiagnostics, 1)
	require.Equal(t, testURI, receivedDiagnostics[0].URI)
	require.Equal(t, int32(0), receivedDiagnostics[0].Version)
	require.Len(t, receivedDiagnostics[0].Diagnostics, 1)
	diag := receivedDiagnostics[0].Diagnostics[0]
	assert.Equal(t, semanticapi.Range{
		Start: semanticapi.Position{Line: 3, Character: 1},
		End:   semanticapi.Position{Line: 3, Character: 7},
	}, diag.Range)
	assert.Equal(t, semanticapi.DiagnosticSeverity(1), diag.Severity)
	assert.Equal(t, "UnusedVar", diag.Code)
	assert.Equal(t, "compiler", diag.Source)
	assert.Contains(t, diag.Message, "declared and not used: unused")
	assert.Equal(t, []semanticapi.DiagnosticTag{
		semanticapi.DiagnosticTagUnnecessary,
	}, diag.Tags)
}

func TestE2ECallbackProgress(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "testdata")

	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)

	mainURI := "file://" + mainPath
	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()

	ctx, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()

	var (
		progressMu sync.Mutex
		sawBegin   bool
		sawEnd     bool
	)
	done := make(chan struct{})

	callback := &testCallback{onProgress: func(p semanticapi.ProgressParams) {
		var kind struct {
			Kind string `json:"kind"`
		}
		if err := json.Unmarshal(
			p.Value, &kind,
		); err != nil {
			return
		}
		progressMu.Lock()
		defer progressMu.Unlock()
		switch kind.Kind {
		case "begin":
			sawBegin = true
		case "end":
			if sawBegin {
				sawEnd = true
				select {
				case <-done:
				default:
					close(done)
				}
			}
		}
	},
	}

	params := autoInitParams(uri.String())
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "go",
		"command": "gopls serve",
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	mgr := New(
		uri, scheme, scheme,
		&stubPkgManager{bin: goplsBin},
		nil, nil,
		Config{Callback: callback, MaxRetries: 1},
	)
	t.Cleanup(func() { _ = mgr.Close() })

	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)
	require.NoError(t, mgr.DidOpen(ctx,
		semanticapi.DidOpenTextDocumentParams{
			TextDocument: semanticapi.TextDocumentItem{
				URI:        mainURI,
				LanguageID: "go",
				Version:    0,
				Text:       string(mainContent),
			},
		},
	))

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for progress events")
	}

	progressMu.Lock()
	defer progressMu.Unlock()
	assert.True(t, sawBegin, "expected begin progress")
	assert.True(t, sawEnd, "expected end progress")
	require.NoError(t, mgr.Close())
}

func TestE2EWorkDoneProgress(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "testdata")

	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)

	goModURI := "file://" + filepath.Join(tmpDir, "go.mod")
	mainURI := "file://" + mainPath
	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()

	ctx, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()

	// Track progress events: loading uses server-initiated string
	// tokens; ExecuteCommand uses our injected integer token.
	var (
		progressMu    sync.Mutex
		integerTokens []semanticapi.ProgressToken
	)
	loaded := make(chan struct{})
	cmdDone := make(chan struct{})

	callback := &testCallback{
		onProgress: func(p semanticapi.ProgressParams) {
			var kind struct {
				Kind string `json:"kind"`
			}
			if json.Unmarshal(p.Value, &kind) != nil {
				return
			}
			progressMu.Lock()
			defer progressMu.Unlock()

			if p.Token.IsInteger {
				integerTokens = append(integerTokens, p.Token)
				if kind.Kind == "end" {
					select {
					case <-cmdDone:
					default:
						close(cmdDone)
					}
				}
				return
			}
			// Server-initiated string tokens signal loading.
			if kind.Kind == "end" {
				select {
				case <-loaded:
				default:
					close(loaded)
				}
			}
		},
	}

	params := autoInitParams(uri.String())
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "go",
		"command": "gopls serve",
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	mgr := New(
		uri, scheme, scheme,
		&stubPkgManager{bin: goplsBin},
		nil, nil,
		Config{
			Callback:         callback,
			MaxRetries:       1,
			WorkDoneProgress: true,
		},
	)
	t.Cleanup(func() { _ = mgr.Close() })

	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)
	require.NoError(t, mgr.DidOpen(ctx,
		semanticapi.DidOpenTextDocumentParams{
			TextDocument: semanticapi.TextDocumentItem{
				URI:        mainURI,
				LanguageID: "go",
				Version:    0,
				Text:       string(mainContent),
			},
		},
	))

	// Wait for gopls to finish loading before issuing a command.
	select {
	case <-loaded:
	case <-ctx.Done():
		t.Fatal("timed out waiting for gopls to load")
	}

	// Record the token sequence before the command; the next
	// tokenFor call will produce seqBefore+1.
	seqBefore := atomic.LoadInt64(&mgr.tokenSeq)

	// gopls.tidy honors client-provided workDoneToken: its
	// run() method passes params.WorkDoneToken to progress.Start.
	arg, err := json.Marshal(map[string]any{
		"URIs": []string{goModURI},
	})
	require.NoError(t, err)

	_, err = mgr.ExecuteCommand(ctx, semanticapi.ExecuteCommandParams{
		Command:   "gopls.tidy",
		Arguments: []json.RawMessage{arg},
	})
	require.NoError(t, err)

	// Wait for progress reported with our integer token.
	select {
	case <-cmdDone:
	case <-ctx.Done():
		t.Fatal("timed out waiting for progress with integer token")
	}

	progressMu.Lock()
	defer progressMu.Unlock()
	require.NotEmpty(t, integerTokens,
		"expected $/progress events with our integer token",
	)
	wantToken := int(seqBefore + 1)
	assert.Equal(t, wantToken, integerTokens[0].IntegerValue)
	assert.True(t, integerTokens[0].IsInteger)

	require.NoError(t, mgr.Close())
}

func TestE2ECallbackApplyEdit(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "testdata")

	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)

	mainURI := "file://" + mainPath
	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()

	ctx, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()

	done := make(chan struct{}, 1)
	var receivedEdit []semanticapi.ApplyWorkspaceEditParams

	var loaded sync.WaitGroup
	var loadReady sync.Once
	callback := &testCallback{
		onShowMessage: func(
			params semanticapi.ShowMessageParams,
		) {
			if strings.Contains(
				params.Message,
				"Finished loading packages",
			) {
				loadReady.Do(loaded.Done)
			}
		},
		onProgress: readyOnProgress(
			&loadReady, &loaded,
		),
		onApplyEdit: func(
			p semanticapi.ApplyWorkspaceEditParams,
		) {
			receivedEdit = append(receivedEdit, p)
			select {
			case done <- struct{}{}:
			default:
			}
		},
	}

	params := autoInitParams(uri.String())
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "go",
		"command": "gopls serve",
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	mgr := New(
		uri, scheme, scheme,
		&stubPkgManager{bin: goplsBin},
		nil, nil,
		Config{Callback: callback, MaxRetries: 1},
	)
	t.Cleanup(func() { _ = mgr.Close() })

	loaded.Add(1)
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)
	require.NoError(t, mgr.DidOpen(ctx,
		semanticapi.DidOpenTextDocumentParams{
			TextDocument: semanticapi.TextDocumentItem{
				URI:        mainURI,
				LanguageID: "go",
				Version:    0,
				Text:       string(mainContent),
			},
		},
	))
	loaded.Wait()

	// Use gopls.add_import to trigger workspace/applyEdit.
	arg, err := json.Marshal(map[string]string{
		"URI":        mainURI,
		"ImportPath": "strings",
	})
	require.NoError(t, err)

	_, err = mgr.ExecuteCommand(ctx,
		semanticapi.ExecuteCommandParams{
			Command:   "gopls.add_import",
			Arguments: []json.RawMessage{arg},
		},
	)
	require.NoError(t, err)

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(
			"timed out waiting for applyEdit callback",
		)
	}

	require.NotEmpty(t, receivedEdit,
		"expected at least one applyEdit callback",
	)
	edit := receivedEdit[0].Edit
	// gopls converts `import "fmt"` (line 25) into a
	// grouped import adding "strings":
	//   import (
	//       "fmt"
	//       "strings"
	//   )
	expected := semanticapi.WorkspaceEdit{
		DocumentChanges: []semanticapi.DocumentChange{
			{
				TextDocumentEdit: &semanticapi.TextDocumentEdit{
					TextDocument: semanticapi.VersionedTextDocumentIdentifier{
						URI:     mainURI,
						Version: 0,
					},
					Edits: []semanticapi.TextEdit{
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{
									Line: 18, Character: 7,
								},
								End: semanticapi.Position{
									Line: 18, Character: 7,
								},
							},
							NewText: "(\n\t",
						},
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{
									Line: 18, Character: 10,
								},
								End: semanticapi.Position{
									Line: 18, Character: 10,
								},
							},
							NewText: "t\"\n\t\"s",
						},
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{
									Line: 18, Character: 11,
								},
								End: semanticapi.Position{
									Line: 18, Character: 11,
								},
							},
							NewText: "rings",
						},
						{
							Range: semanticapi.Range{
								Start: semanticapi.Position{
									Line: 18, Character: 12,
								},
								End: semanticapi.Position{
									Line: 18, Character: 12,
								},
							},
							NewText: "\n)",
						},
					},
				},
			},
		},
	}
	assert.Equal(t, expected, edit)
	require.NoError(t, mgr.Close())
}

func TestE2ECallbackShowDocument(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "testdata")

	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)

	mainURI := "file://" + mainPath
	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()

	ctx, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()

	showDocCh := make(chan semanticapi.ShowDocumentParams, 1)
	var loaded sync.WaitGroup
	var loadReady sync.Once
	callback := &testCallback{
		onShowMessage: func(
			params semanticapi.ShowMessageParams,
		) {
			if strings.Contains(
				params.Message,
				"Finished loading packages",
			) {
				loadReady.Do(loaded.Done)
			}
		},
		onProgress: readyOnProgress(
			&loadReady, &loaded,
		),
		onShowDocument: func(
			p semanticapi.ShowDocumentParams,
		) {
			select {
			case showDocCh <- p:
			default:
			}
		},
	}

	// Build init params with showDocument capability.
	// autoInitParams only declares workDoneProgress in the
	// window capabilities, which means gopls doesn't know
	// the client supports window/showDocument. Without
	// "showDocument": {"support": true}, gopls will not
	// call window/showDocument when source.doc is executed.
	params := autoInitParams(uri.String())
	var caps map[string]any
	require.NoError(t, json.Unmarshal(
		params.Capabilities, &caps,
	))
	windowCaps := caps["window"].(map[string]any)
	windowCaps["showDocument"] = map[string]any{
		"support": true,
	}
	capsJSON, err := json.Marshal(caps)
	require.NoError(t, err)
	params.Capabilities = capsJSON

	initOpts, err := json.Marshal(map[string]any{
		"langID":  "go",
		"command": "gopls serve",
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	mgr := New(
		uri, scheme, scheme,
		&stubPkgManager{bin: goplsBin},
		nil, nil,
		Config{Callback: callback, MaxRetries: 1},
	)
	t.Cleanup(func() { _ = mgr.Close() })

	loaded.Add(1)
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)
	require.NoError(t, mgr.DidOpen(ctx,
		semanticapi.DidOpenTextDocumentParams{
			TextDocument: semanticapi.TextDocumentItem{
				URI:        mainURI,
				LanguageID: "go",
				Version:    0,
				Text:       string(mainContent),
			},
		},
	))
	loaded.Wait()

	// Request code actions on the Add function (line 38-40).
	// With showDocument capability advertised, gopls should
	// include source.doc among the available actions.
	actions, err := mgr.CodeAction(ctx,
		semanticapi.CodeActionParams{
			TextDocument: semanticapi.TextDocumentIdentifier{
				URI: mainURI,
			},
			Range: semanticapi.Range{
				Start: semanticapi.Position{
					Line: 38, Character: 0,
				},
				End: semanticapi.Position{
					Line: 40, Character: 0,
				},
			},
		},
	)
	require.NoError(t, err)

	// Find the source.doc code action.
	var docAction *semanticapi.CodeActionResult
	for i := range actions {
		a := actions[i].CodeAction
		if a != nil && a.Kind == "source.doc" {
			docAction = &actions[i]
			break
		}
	}
	if docAction == nil {
		t.Skip(
			"gopls version does not offer source.doc code action",
		)
	}
	require.NotNil(t, docAction.CodeAction.Command,
		"expected source.doc to have a command",
	)

	// Execute the command; gopls starts a doc server and
	// calls window/showDocument with the documentation URL.
	_, err = mgr.ExecuteCommand(ctx,
		semanticapi.ExecuteCommandParams{
			Command:   docAction.CodeAction.Command.Command,
			Arguments: docAction.CodeAction.Command.Arguments,
		},
	)
	require.NoError(t, err)

	select {
	case p := <-showDocCh:
		// gopls opens a local HTTP URL pointing to the
		// package documentation for the symbol.
		assert.Contains(t, p.URI, "http")
	case <-ctx.Done():
		t.Fatal(
			"timed out waiting for showDocument callback",
		)
	}

	require.NoError(t, mgr.Close())
}

func TestE2EGoModChangeNotifiesGopls(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "")

	uri := makeURI(t, "file://"+tmpDir)

	// Write a valid workspace first.
	goModPath := filepath.Join(tmpDir, "go.mod")
	require.NoError(t, os.WriteFile(goModPath,
		[]byte("module example.com/testmod\n\ngo 1.22\n"), 0644))

	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent := `package main

import "fmt"

func main() {
	fmt.Println("hello")
}
`
	require.NoError(t, os.WriteFile(mainPath, []byte(mainContent), 0644))

	mainURI := "file://" + mainPath

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var (
		loadedWg    sync.WaitGroup
		loadedReady sync.Once
	)

	diagCh := make(chan semanticapi.PublishDiagnosticsParams, 32)
	callback := &testCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") {
				loadedReady.Do(loadedWg.Done)
			}
		},
		onProgress: readyOnProgress(&loadedReady, &loadedWg),
		onDiagnostics: func(p semanticapi.PublishDiagnosticsParams) {
			select {
			case diagCh <- p:
			default:
			}
		},
	}

	scheme := newTestScheme()
	mgr := New(
		uri,
		scheme,
		scheme,
		&stubPkgManager{bin: goplsBin},
		nil, // notifications
		nil, // opener
		Config{Callback: callback, MaxRetries: 1},
	)
	t.Cleanup(func() { _ = mgr.Close() })

	params := autoInitParams(uri.String())
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "go",
		"command": "gopls serve",
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	loadedWg.Add(1)
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)
	require.NoError(t, mgr.DidOpen(ctx,
		semanticapi.DidOpenTextDocumentParams{
			TextDocument: semanticapi.TextDocumentItem{
				URI:        mainURI,
				LanguageID: "go",
				Version:    0,
				Text:       mainContent,
			},
		},
	))
	loadedWg.Wait()

	// Drain any initial diagnostics from loading.
	drainDiagnostics(diagCh, 1*time.Second)

	// Break go.mod by adding a require for a nonexistent module.
	// gopls publishes diagnostics for go.mod when it can't resolve deps.
	require.NoError(t, os.WriteFile(goModPath,
		[]byte("module example.com/testmod\n\ngo 1.22\n\nrequire nonexistent.invalid/pkg v0.0.0\n"), 0644))

	goModURI, err := workspaceapi.ParseURI("file://" + goModPath)
	require.NoError(t, err)

	mgr.Handle(ctx, textapi.Event{
		Type: textapi.EventTypeChange,
		URI:  goModURI,
	})

	// Wait for diagnostics that include errors.
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	var got bool
	for !got {
		select {
		case p := <-diagCh:
			if len(p.Diagnostics) > 0 {
				got = true
			}
		case <-timer.C:
			t.Fatal("timed out waiting for diagnostics after go.mod change")
		case <-ctx.Done():
			t.Fatal("context cancelled waiting for diagnostics")
		}
	}

	require.NoError(t, mgr.Close())
}

func TestE2EFileCreateNotifiesGopls(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "")

	uri := makeURI(t, "file://"+tmpDir)

	// Write a valid workspace first.
	goModPath := filepath.Join(tmpDir, "go.mod")
	require.NoError(t, os.WriteFile(goModPath,
		[]byte("module example.com/testmod\n\ngo 1.22\n"), 0644))

	// Write main.go that references Helper() which doesn't exist yet.
	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent := `package main

func main() {
	Helper()
}
`
	require.NoError(t, os.WriteFile(mainPath,
		[]byte(mainContent), 0644))

	mainURI := "file://" + mainPath

	ctx, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()

	var (
		loadedWg    sync.WaitGroup
		loadedReady sync.Once
	)

	diagCh := make(chan semanticapi.PublishDiagnosticsParams, 32)
	callback := &testCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") {
				loadedReady.Do(loadedWg.Done)
			}
		},
		onProgress: readyOnProgress(&loadedReady, &loadedWg),
		onDiagnostics: func(p semanticapi.PublishDiagnosticsParams) {
			select {
			case diagCh <- p:
			default:
			}
		},
	}

	scheme := newTestScheme()
	mgr := New(
		uri,
		scheme,
		scheme,
		&stubPkgManager{bin: goplsBin},
		nil, // notifications
		nil, // opener
		Config{Callback: callback, MaxRetries: 1},
	)
	t.Cleanup(func() { _ = mgr.Close() })

	params := autoInitParams(uri.String())
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "go",
		"command": "gopls serve",
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	loadedWg.Add(1)
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)
	loadedWg.Wait()

	// Wait for diagnostics that include the undefined Helper error.
	// This confirms our test setup is correct before proceeding.
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	var gotError bool
	for !gotError {
		select {
		case p := <-diagCh:
			if p.URI != mainURI {
				continue
			}
			for _, d := range p.Diagnostics {
				if strings.Contains(d.Message, "Helper") {
					gotError = true
					break
				}
			}
		case <-timer.C:
			t.Fatal("timed out waiting for diagnostics with undefined Helper error")
		case <-ctx.Done():
			t.Fatal("context cancelled waiting for diagnostics")
		}
	}

	// Drain any remaining diagnostics from the initial load.
	drainDiagnostics(diagCh, 1*time.Second)

	// Create helper.go on disk (simulating an external process
	// creating a file in the workspace).
	helperPath := filepath.Join(tmpDir, "helper.go")
	helperContent := `package main

// Helper is a helper function.
func Helper() {}
`
	require.NoError(t, os.WriteFile(helperPath,
		[]byte(helperContent), 0644))
	t.Cleanup(func() { _ = os.Remove(helperPath) })

	helperURI, err := workspaceapi.ParseURI("file://" + helperPath)
	require.NoError(t, err)

	// Notify the LSP manager about the newly created file.
	mgr.Handle(ctx, textapi.Event{
		Type: textapi.EventTypeCreate,
		URI:  helperURI,
	})

	// Wait for diagnostics for main.go to clear — the undefined
	// Helper error should disappear now that helper.go exists.
	timer2 := time.NewTimer(15 * time.Second)
	defer timer2.Stop()
	var cleared bool
	for !cleared {
		select {
		case p := <-diagCh:
			if p.URI != mainURI {
				continue
			}
			// Check that no diagnostic mentions Helper anymore.
			hasHelper := false
			for _, d := range p.Diagnostics {
				if strings.Contains(d.Message, "Helper") {
					hasHelper = true
					break
				}
			}
			if !hasHelper {
				cleared = true
			}
		case <-timer2.C:
			t.Fatal("timed out waiting for diagnostics to clear after helper.go creation")
		case <-ctx.Done():
			t.Fatal("context cancelled waiting for diagnostics to clear")
		}
	}

	require.NoError(t, mgr.Close())
}

func TestE2EFileRenameNotifiesGopls(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "")

	uri := makeURI(t, "file://"+tmpDir)

	// Write a valid workspace first.
	goModPath := filepath.Join(tmpDir, "go.mod")
	require.NoError(t, os.WriteFile(goModPath,
		[]byte("module example.com/testmod\n\ngo 1.22\n"), 0644))

	// Write main.go that references Helper() which doesn't exist yet.
	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent := `package main

func main() {
	Helper()
}
`
	require.NoError(t, os.WriteFile(mainPath,
		[]byte(mainContent), 0644))
	helperPath := filepath.Join(tmpDir, "helper.go.swp")
	helperContent := `package main

// Helper is a helper function.
func Helper() {}
`
	require.NoError(t, os.WriteFile(helperPath,
		[]byte(helperContent), 0644))
	t.Cleanup(func() { _ = os.Remove(helperPath) })

	mainURI := "file://" + mainPath

	ctx, cancel := context.WithTimeout(
		context.Background(), 30*time.Second,
	)
	defer cancel()

	var (
		loadedWg    sync.WaitGroup
		loadedReady sync.Once
	)

	diagCh := make(chan semanticapi.PublishDiagnosticsParams, 32)
	callback := &testCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") {
				loadedReady.Do(loadedWg.Done)
			}
		},
		onProgress: readyOnProgress(&loadedReady, &loadedWg),
		onDiagnostics: func(p semanticapi.PublishDiagnosticsParams) {
			select {
			case diagCh <- p:
			default:
			}
		},
	}

	scheme := newTestScheme()
	mgr := New(
		uri,
		scheme,
		scheme,
		&stubPkgManager{bin: goplsBin},
		nil, // notifications
		nil, // opener
		Config{Callback: callback, MaxRetries: 1},
	)
	t.Cleanup(func() { _ = mgr.Close() })

	params := autoInitParams(uri.String())
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "go",
		"command": "gopls serve",
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	loadedWg.Add(1)
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)
	loadedWg.Wait()

	// Wait for diagnostics that include the undefined Helper error.
	// This confirms our test setup is correct before proceeding.
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	var gotError bool
	for !gotError {
		select {
		case p := <-diagCh:
			if p.URI != mainURI {
				continue
			}
			for _, d := range p.Diagnostics {
				if strings.Contains(d.Message, "Helper") {
					gotError = true
					break
				}
			}
		case <-timer.C:
			t.Fatal("timed out waiting for diagnostics with undefined Helper error")
		case <-ctx.Done():
			t.Fatal("context cancelled waiting for diagnostics")
		}
	}

	// Drain any remaining diagnostics from the initial load.
	drainDiagnostics(diagCh, 1*time.Second)

	helperPathTarget := filepath.Join(tmpDir, "helper.go")
	require.NoError(t, os.Rename(helperPath, helperPathTarget))

	helperURI, err := workspaceapi.ParseURI("file://" + helperPathTarget)
	require.NoError(t, err)

	// Notify the LSP manager about the newly created file.
	mgr.Handle(ctx, textapi.Event{
		Type: textapi.EventTypeRename,
		URI:  helperURI,
	})

	// Wait for diagnostics for main.go to clear — the undefined
	// Helper error should disappear now that helper.go exists.
	timer2 := time.NewTimer(15 * time.Second)
	defer timer2.Stop()
	var cleared bool
	for !cleared {
		select {
		case p := <-diagCh:
			if p.URI != mainURI {
				continue
			}
			// Check that no diagnostic mentions Helper anymore.
			hasHelper := false
			for _, d := range p.Diagnostics {
				if strings.Contains(d.Message, "Helper") {
					hasHelper = true
					break
				}
			}
			if !hasHelper {
				cleared = true
			}
		case <-timer2.C:
			t.Fatal("timed out waiting for diagnostics to clear after helper.go creation")
		case <-ctx.Done():
			t.Fatal("context cancelled waiting for diagnostics to clear")
		}
	}

	require.NoError(t, mgr.Close())
}

func TestE2EReinitializePreservesParams(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "testdata")

	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	mainURI := "file://" + mainPath

	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var ready sync.Once
	var wg sync.WaitGroup
	callback := &testCallback{
		onProgress: readyOnProgress(&ready, &wg),
	}

	mgr := New(
		uri, scheme, scheme,
		&stubPkgManager{bin: goplsBin},
		nil, nil,
		Config{Callback: callback, MaxRetries: 3, NoInitializeServer: true},
	)
	t.Cleanup(func() { _ = mgr.Close() })

	// Initialize with custom options that gopls will use.
	params := autoInitParams(uri.String())
	initOpts, err := json.Marshal(map[string]any{
		"langID":         "go",
		"command":        "gopls serve",
		"semanticTokens": true,
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	wg.Add(1)
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	// Open a file and wait for gopls to finish loading so the
	// server is fully operational before we kill it.
	require.NoError(t, mgr.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
		TextDocument: semanticapi.TextDocumentItem{
			URI:        mainURI,
			LanguageID: "go",
			Version:    0,
			Text:       string(mainContent),
		},
	}))
	wg.Wait()

	// Snapshot the original server and its InitializeParams.
	mgr.mu.Lock()
	origSrv := mgr.servers[serverKey{languageID: "go", rootURI: uri.String()}].(*langServer)
	mgr.mu.Unlock()
	require.NotNil(t, origSrv)
	originalParams := origSrv.params

	// Kill the gopls process so watchServer triggers a restart.
	require.NoError(t, scheme.Signal(origSrv.pid, syscall.SIGKILL))

	// Wait for the manager to detect the crash and start a new server.
	var newSrv *langServer
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		s, _ := mgr.servers[serverKey{languageID: "go", rootURI: uri.String()}].(*langServer)
		mgr.mu.Unlock()
		if s != nil && s != origSrv {
			newSrv = s
			return true
		}
		return false
	}, 15*time.Second, 200*time.Millisecond,
		"server did not restart after kill")

	// The restarted server must have the exact same InitializeParams
	// that were originally sent, including the custom InitializeOptions.
	assert.Equal(t, originalParams, newSrv.params)
}

// drainDiagnostics reads and discards diagnostics from the channel
// for the given duration.
func drainDiagnostics(
	ch <-chan semanticapi.PublishDiagnosticsParams,
	d time.Duration,
) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-ch:
		case <-timer.C:
			return
		}
	}
}

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

func setupTestWorkspace(t *testing.T, testdataDir string) string {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	// macOS temp dirs live behind /var -> /private/var symlinks. ty
	// canonicalizes workspace paths and silently reports no diagnostics
	// for documents whose URI does not match the canonical root, so
	// resolve the symlinks before building any file:// URI from it.
	tmpDir, err = filepath.EvalSymlinks(tmpDir)
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

// TestE2EOutOfRootHover asserts that a Hover at a location outside any
// initialized root (e.g. a GOROOT file returned by Definition on a
// stdlib symbol) falls back to the same-language server with the
// broadest root instead of failing with ErrNoServer.
func TestE2EOutOfRootHover(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "testdata")

	mainPath := filepath.Join(tmpDir, "main.go")
	mainContent, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	mainURI := "file://" + mainPath

	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mgr := New(
		uri, scheme, scheme,
		&stubPkgManager{bin: goplsBin},
		nil, nil,
		Config{
			Callback:           &testCallback{},
			MaxRetries:         1,
			NoInitializeServer: true,
			InitializeTimeout:  30 * time.Second,
		},
	)
	t.Cleanup(func() { _ = mgr.Close() })

	params := autoInitParams(uri.String())
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "go",
		"command": "gopls serve",
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	require.NoError(t, mgr.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
		TextDocument: semanticapi.TextDocumentItem{
			URI:        mainURI,
			LanguageID: "go",
			Version:    0,
			Text:       string(mainContent),
		},
	}))

	// Definition of fmt.Sprintf resolves into GOROOT, outside the
	// workspace and outside any initialized root.
	result, err := mgr.Definition(ctx, semanticapi.DefinitionParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
		Position:     semanticapi.Position{Line: 27, Character: 13},
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Locations)
	loc := result.Locations[0]
	require.False(t, rootContains(uri.String(), loc.URI),
		"expected a stdlib definition outside the workspace, got %s", loc.URI)

	hover, err := mgr.Hover(ctx, semanticapi.HoverParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: loc.URI},
		Position:     loc.Range.Start,
	})
	require.NoError(t, err,
		"hover at an out-of-root location must route to the broadest "+
			"same-language server")
	require.NotNil(t, hover)
	assert.Contains(t, hover.Contents.Value, "Sprintf")
}

// TestWatchServerRestartsOnConnLoss locks in the fix that watchServer
// listens on srv.conn.Done() in addition to the process watcher.
// Closing the IDE-side jsonrpc2 conn while the gopls process is still
// alive must trigger a SIGKILL of the orphan plus a fresh server.
func TestWatchServerRestartsOnConnLoss(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "testdata")

	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var ready sync.Once
	var wg sync.WaitGroup
	callback := &testCallback{
		onProgress: readyOnProgress(&ready, &wg),
	}

	mgr := New(
		uri, scheme, scheme,
		&stubPkgManager{bin: goplsBin},
		nil, nil,
		Config{Callback: callback, MaxRetries: 3, NoInitializeServer: true},
	)
	t.Cleanup(func() { _ = mgr.Close() })

	params := autoInitParams(uri.String())
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "go",
		"command": "gopls serve",
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	wg.Add(1)
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)
	wg.Wait()

	mgr.mu.Lock()
	origSrv := mgr.servers[serverKey{languageID: "go", rootURI: uri.String()}].(*langServer)
	mgr.mu.Unlock()
	require.NotNil(t, origSrv)
	origPid := origSrv.pid

	// Tear down the jsonrpc2 connection without touching the
	// child process. Closing the IDE-side stdin/stdout breaks
	// the readIncoming goroutine, which closes the conn's done
	// channel.
	require.NoError(t, origSrv.stdin.Close())
	require.NoError(t, origSrv.stdout.Close())

	require.Eventually(t, func() bool {
		select {
		case <-origSrv.conn.Done():
			return true
		default:
			return false
		}
	}, 5*time.Second, 50*time.Millisecond,
		"original conn never reported Done")

	// The manager must now kill the orphan process and start a new server.
	var newSrv *langServer
	require.Eventually(t, func() bool {
		mgr.mu.Lock()
		s, _ := mgr.servers[serverKey{languageID: "go", rootURI: uri.String()}].(*langServer)
		mgr.mu.Unlock()
		if s != nil && s != origSrv {
			newSrv = s
			return true
		}
		return false
	}, 15*time.Second, 200*time.Millisecond,
		"server did not restart after conn loss")

	assert.NotEqual(t, origPid, newSrv.pid,
		"new server must have a different pid")
	assert.Equal(t, origSrv.params, newSrv.params,
		"InitializeParams must be preserved across conn-loss restart")

	// Sanity check the new server actually responds.
	pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pingCancel()
	var raw semanticapi.DocumentSymbolResult
	_ = newSrv.call(pingCtx, "workspace/symbol",
		semanticapi.WorkspaceSymbolParams{Query: ""}, &raw)
}

// TestManagerCloseTerminatesGopls locks in the RUNE-180 contract that
// Manager.Close propagates cancellation down to spawned language-server
// processes (m.ctx → langServer.ctx → exec.CommandContext-bound child).
func TestManagerCloseTerminatesGopls(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)
	tmpDir := setupTestWorkspace(t, "testdata")

	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var ready sync.Once
	var wg sync.WaitGroup
	callback := &testCallback{
		onProgress: readyOnProgress(&ready, &wg),
	}

	mgr := New(
		uri, scheme, scheme,
		&stubPkgManager{bin: goplsBin},
		nil, nil,
		Config{Callback: callback, MaxRetries: 3, NoInitializeServer: true},
	)

	params := autoInitParams(uri.String())
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "go",
		"command": "gopls serve",
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts

	wg.Add(1)
	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)
	wg.Wait()

	mgr.mu.Lock()
	srv := mgr.servers[serverKey{languageID: "go", rootURI: uri.String()}].(*langServer)
	mgr.mu.Unlock()
	require.NotNil(t, srv)
	require.NotNil(t, srv.watcher,
		"langServer must have a process watcher so close can be observed")

	require.NoError(t, mgr.Close())

	require.Error(t, mgr.ctx.Err(),
		"Manager.Close must cancel m.ctx so handleEvs / watchServer exit")

	select {
	case <-srv.watcher:
	case <-time.After(10 * time.Second):
		t.Fatal("gopls child did not exit within 10s of Manager.Close: " +
			"manager teardown is not propagating cancellation to the child")
	}
}

// newTransientTestManager builds a Manager backed by the local
