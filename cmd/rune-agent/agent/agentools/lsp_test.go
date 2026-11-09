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

package agentools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/agent"
)

// --- mock LSP ---

// stubLSP is a minimal LSP mock. Only the methods under test are implemented;
// all others panic if called.
type stubLSP struct {
	workspaceSymbolFn func(semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error)
	definitionFn      func(semanticapi.DefinitionParams) (semanticapi.LocationResult, error)
	implementationFn  func(semanticapi.ImplementationParams) (semanticapi.LocationResult, error)
	referencesFn      func(semanticapi.ReferenceParams) ([]semanticapi.Location, error)
	documentSymbolFn  func(semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error)
	hoverFn           func(semanticapi.HoverParams) (*semanticapi.Hover, error)
	diagnosticFn      func(semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error)
	formattingFn      func(semanticapi.DocumentFormattingParams) ([]semanticapi.TextEdit, error)

	didChangeWatchedFilesFn func(semanticapi.DidChangeWatchedFilesParams) error
}

func (s *stubLSP) WorkspaceSymbol(_ context.Context, p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
	if s.workspaceSymbolFn != nil {
		return s.workspaceSymbolFn(p)
	}
	return nil, fmt.Errorf("WorkspaceSymbol not configured")
}

func (s *stubLSP) Definition(_ context.Context, p semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
	if s.definitionFn != nil {
		return s.definitionFn(p)
	}
	return semanticapi.LocationResult{}, fmt.Errorf("Definition not configured")
}

func (s *stubLSP) Implementation(_ context.Context, p semanticapi.ImplementationParams) (semanticapi.LocationResult, error) {
	if s.implementationFn != nil {
		return s.implementationFn(p)
	}
	return semanticapi.LocationResult{}, fmt.Errorf("Implementation not configured")
}

func (s *stubLSP) DocumentSymbol(_ context.Context, p semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error) {
	if s.documentSymbolFn != nil {
		return s.documentSymbolFn(p)
	}
	return semanticapi.DocumentSymbolResult{}, fmt.Errorf("DocumentSymbol not configured")
}

func (s *stubLSP) Hover(_ context.Context, p semanticapi.HoverParams) (*semanticapi.Hover, error) {
	if s.hoverFn != nil {
		return s.hoverFn(p)
	}
	return nil, fmt.Errorf("Hover not configured")
}

func (s *stubLSP) Diagnostic(_ context.Context, p semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error) {
	if s.diagnosticFn != nil {
		return s.diagnosticFn(p)
	}
	return semanticapi.DocumentDiagnosticReport{}, fmt.Errorf("Diagnostic not configured")
}

func (s *stubLSP) Formatting(_ context.Context, p semanticapi.DocumentFormattingParams) ([]semanticapi.TextEdit, error) {
	if s.formattingFn != nil {
		return s.formattingFn(p)
	}
	return nil, fmt.Errorf("Formatting not configured")
}

// Stubs for all other LSP methods — return zero values so the mock
// satisfies the interface without pulling in the full implementation.
func (s *stubLSP) Initialize(context.Context, semanticapi.InitializeParams) (semanticapi.InitializeResult, error) {
	return semanticapi.InitializeResult{}, nil
}
func (s *stubLSP) Initialized(context.Context) error { return nil }
func (s *stubLSP) Shutdown(context.Context) error    { return nil }
func (s *stubLSP) Exit(context.Context) error        { return nil }
func (s *stubLSP) DidOpen(context.Context, semanticapi.DidOpenTextDocumentParams) error {
	return nil
}
func (s *stubLSP) DidChange(context.Context, semanticapi.DidChangeTextDocumentParams) error {
	return nil
}
func (s *stubLSP) DidClose(context.Context, semanticapi.DidCloseTextDocumentParams) error {
	return nil
}
func (s *stubLSP) DidSave(context.Context, semanticapi.DidSaveTextDocumentParams) error {
	return nil
}
func (s *stubLSP) Completion(context.Context, semanticapi.CompletionParams) (semanticapi.CompletionResult, error) {
	return semanticapi.CompletionResult{}, nil
}
func (s *stubLSP) SignatureHelp(context.Context, semanticapi.SignatureHelpParams) (*semanticapi.SignatureHelp, error) {
	return nil, nil
}
func (s *stubLSP) Declaration(context.Context, semanticapi.DeclarationParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (s *stubLSP) TypeDefinition(context.Context, semanticapi.TypeDefinitionParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (s *stubLSP) References(_ context.Context, p semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
	if s.referencesFn != nil {
		return s.referencesFn(p)
	}
	return nil, nil
}
func (s *stubLSP) DocumentHighlight(context.Context, semanticapi.DocumentHighlightParams) ([]semanticapi.DocumentHighlight, error) {
	return nil, nil
}
func (s *stubLSP) CodeAction(context.Context, semanticapi.CodeActionParams) ([]semanticapi.CodeActionResult, error) {
	return nil, nil
}
func (s *stubLSP) CodeLens(context.Context, semanticapi.CodeLensParams) ([]semanticapi.CodeLens, error) {
	return nil, nil
}
func (s *stubLSP) RangeFormatting(context.Context, semanticapi.DocumentRangeFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (s *stubLSP) Rename(context.Context, semanticapi.RenameParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (s *stubLSP) PrepareRename(context.Context, semanticapi.PrepareRenameParams) (*semanticapi.PrepareRenameResult, error) {
	return nil, nil
}
func (s *stubLSP) FoldingRange(context.Context, semanticapi.FoldingRangeParams) ([]semanticapi.FoldingRange, error) {
	return nil, nil
}
func (s *stubLSP) SelectionRange(context.Context, semanticapi.SelectionRangeParams) ([]semanticapi.SelectionRange, error) {
	return nil, nil
}
func (s *stubLSP) SemanticTokensFull(context.Context, semanticapi.SemanticTokensParams) (*semanticapi.SemanticTokens, error) {
	return nil, nil
}
func (s *stubLSP) SemanticTokensRange(context.Context, semanticapi.SemanticTokensRangeParams) (*semanticapi.SemanticTokens, error) {
	return nil, nil
}
func (s *stubLSP) WorkspaceDiagnostic(context.Context, semanticapi.WorkspaceDiagnosticParams) (semanticapi.WorkspaceDiagnosticReport, error) {
	return semanticapi.WorkspaceDiagnosticReport{}, nil
}
func (s *stubLSP) ExecuteCommand(context.Context, semanticapi.ExecuteCommandParams) (string, error) {
	return "", nil
}
func (s *stubLSP) ExecuteRequest(context.Context, semanticapi.ExecuteRequestParams) (json.RawMessage, error) {
	return json.RawMessage("null"), nil
}
func (s *stubLSP) SendNotification(context.Context, semanticapi.NotificationParams) error {
	return nil
}
func (s *stubLSP) PrepareCallHierarchy(context.Context, semanticapi.CallHierarchyPrepareParams) ([]semanticapi.CallHierarchyItem, error) {
	return nil, nil
}
func (s *stubLSP) CallHierarchyIncomingCalls(context.Context, semanticapi.CallHierarchyIncomingCallsParams) ([]semanticapi.CallHierarchyIncomingCall, error) {
	return nil, nil
}
func (s *stubLSP) CallHierarchyOutgoingCalls(context.Context, semanticapi.CallHierarchyOutgoingCallsParams) ([]semanticapi.CallHierarchyOutgoingCall, error) {
	return nil, nil
}
func (s *stubLSP) CompletionResolve(context.Context, semanticapi.CompletionItem) (semanticapi.CompletionItem, error) {
	return semanticapi.CompletionItem{}, nil
}
func (s *stubLSP) CodeLensResolve(context.Context, semanticapi.CodeLens) (semanticapi.CodeLens, error) {
	return semanticapi.CodeLens{}, nil
}
func (s *stubLSP) DocumentColor(context.Context, semanticapi.DocumentColorParams) ([]semanticapi.ColorInformation, error) {
	return nil, nil
}
func (s *stubLSP) ColorPresentation(context.Context, semanticapi.ColorPresentationParams) ([]semanticapi.ColorPresentation, error) {
	return nil, nil
}
func (s *stubLSP) DocumentLink(context.Context, semanticapi.DocumentLinkParams) ([]semanticapi.DocumentLink, error) {
	return nil, nil
}
func (s *stubLSP) DocumentLinkResolve(context.Context, semanticapi.DocumentLink) (semanticapi.DocumentLink, error) {
	return semanticapi.DocumentLink{}, nil
}
func (s *stubLSP) OnTypeFormatting(context.Context, semanticapi.DocumentOnTypeFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (s *stubLSP) LinkedEditingRange(context.Context, semanticapi.LinkedEditingRangeParams) (*semanticapi.LinkedEditingRanges, error) {
	return nil, nil
}
func (s *stubLSP) Moniker(context.Context, semanticapi.MonikerParams) ([]semanticapi.Moniker, error) {
	return nil, nil
}
func (s *stubLSP) WillSaveWaitUntil(context.Context, semanticapi.WillSaveTextDocumentParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (s *stubLSP) SemanticTokensFullDelta(context.Context, semanticapi.SemanticTokensDeltaParams) (*semanticapi.SemanticTokensDelta, error) {
	return nil, nil
}
func (s *stubLSP) PrepareTypeHierarchy(context.Context, semanticapi.TypeHierarchyPrepareParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (s *stubLSP) TypeHierarchySupertypes(context.Context, semanticapi.TypeHierarchySupertypesParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (s *stubLSP) TypeHierarchySubtypes(context.Context, semanticapi.TypeHierarchySubtypesParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (s *stubLSP) InlayHint(context.Context, semanticapi.InlayHintParams) ([]semanticapi.InlayHint, error) {
	return nil, nil
}
func (s *stubLSP) InlayHintResolve(context.Context, semanticapi.InlayHint) (semanticapi.InlayHint, error) {
	return semanticapi.InlayHint{}, nil
}
func (s *stubLSP) InlineValue(context.Context, semanticapi.InlineValueParams) ([]semanticapi.InlineValue, error) {
	return nil, nil
}
func (s *stubLSP) WillCreateFiles(context.Context, semanticapi.CreateFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (s *stubLSP) WillRenameFiles(context.Context, semanticapi.RenameFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (s *stubLSP) WillDeleteFiles(context.Context, semanticapi.DeleteFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (s *stubLSP) WillSave(context.Context, semanticapi.WillSaveTextDocumentParams) error {
	return nil
}
func (s *stubLSP) DidChangeConfiguration(context.Context, semanticapi.DidChangeConfigurationParams) error {
	return nil
}
func (s *stubLSP) DidChangeWatchedFiles(_ context.Context, p semanticapi.DidChangeWatchedFilesParams) error {
	if s.didChangeWatchedFilesFn != nil {
		return s.didChangeWatchedFilesFn(p)
	}
	return nil
}
func (s *stubLSP) DidChangeWorkspaceFolders(context.Context, semanticapi.DidChangeWorkspaceFoldersParams) error {
	return nil
}
func (s *stubLSP) WorkDoneProgressCancel(context.Context, semanticapi.WorkDoneProgressCancelParams) error {
	return nil
}
func (s *stubLSP) SetTrace(context.Context, semanticapi.SetTraceParams) error { return nil }
func (s *stubLSP) DidCreateFiles(context.Context, semanticapi.CreateFilesParams) error {
	return nil
}
func (s *stubLSP) DidRenameFiles(context.Context, semanticapi.RenameFilesParams) error {
	return nil
}
func (s *stubLSP) DidDeleteFiles(context.Context, semanticapi.DeleteFilesParams) error {
	return nil
}

var _ semanticapi.LSP = (*stubLSP)(nil)

// --- mock parser ---

// fakeParser implements syntaxapi.Parser for tests. nil fields fall
// back to empty iterators so tests can opt into stubbing only the
// surface they need.
type fakeParser struct {
	searchFn     func(string, []string) (iterator.Iterator[syntaxapi.Result], error)
	searchNodeFn func(syntaxapi.NodeCaptureName) (iterator.Iterator[syntaxapi.Result], error)
	queryNodeFn  func(workspaceapi.URI, syntaxapi.NodeCaptureName) (iterator.Iterator[syntaxapi.Result], error)
	resolveFn    func(string) ([]syntaxapi.Match, error)
}

func (p *fakeParser) Search(q string, c []string, _ ...string) (iterator.Iterator[syntaxapi.Result], error) {
	if p.searchFn != nil {
		return p.searchFn(q, c)
	}
	return iterator.Empty[syntaxapi.Result](), nil
}
func (p *fakeParser) SearchNode(n syntaxapi.NodeCaptureName, _ ...string) (iterator.Iterator[syntaxapi.Result], error) {
	if p.searchNodeFn != nil {
		return p.searchNodeFn(n)
	}
	return iterator.Empty[syntaxapi.Result](), nil
}
func (p *fakeParser) Query(_ workspaceapi.URI, _ string, _ []string) (iterator.Iterator[syntaxapi.Result], error) {
	return iterator.Empty[syntaxapi.Result](), nil
}
func (p *fakeParser) QueryNode(u workspaceapi.URI, n syntaxapi.NodeCaptureName) (iterator.Iterator[syntaxapi.Result], error) {
	if p.queryNodeFn != nil {
		return p.queryNodeFn(u, n)
	}
	return iterator.Empty[syntaxapi.Result](), nil
}
func (p *fakeParser) Highlight(_ workspaceapi.URI, _ string) (iterator.Iterator[textapi.Location], error) {
	return iterator.Empty[textapi.Location](), nil
}
func (p *fakeParser) ResolveSymbol(
	_ context.Context, name string, _ syntaxapi.Progress,
) (iterator.Iterator[syntaxapi.Match], error) {
	if p.resolveFn != nil {
		matches, err := p.resolveFn(name)
		if err != nil {
			return nil, err
		}
		return iterator.FromSlice(matches), nil
	}
	return iterator.Empty[syntaxapi.Match](), nil
}

func (p *fakeParser) ListReferencedSymbols(context.Context) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

var _ syntaxapi.Parser = (*fakeParser)(nil)

// --- helpers ---

func loc(uri string, line, char uint32) semanticapi.Location {
	return semanticapi.Location{
		URI:   uri,
		Range: semanticapi.Range{Start: semanticapi.Position{Line: line, Character: char}},
	}
}

func symInfo(name string, kind semanticapi.SymbolKind, uri string, line uint32) semanticapi.SymbolInformation {
	return semanticapi.SymbolInformation{
		Name:     name,
		Kind:     kind,
		Location: loc(uri, line, 0),
	}
}

// --- tests ---

func TestLSPTools(t *testing.T) {
	lsp := &stubLSP{}
	tools := LSPTools(lsp, localFS{}, &fakeParser{}, dirURI("/workspace"), NewFileTracker())
	require.Len(t, tools, 8)

	expectedNames := map[string]bool{
		"find_definition":      false,
		"find_implementations": false,
		"find_references":      false,
		"outline_file":         false,
		"search_symbols":       false,
		"describe_symbol":      false,
		"check_file_errors":    false,
		"format_file":          false,
	}
	for _, tool := range tools {
		def := tool.Definition()
		assert.Equal(t, llmapi.ToolTypeFunction, def.Type)
		name := def.Function.Name
		_, ok := expectedNames[name]
		assert.True(t, ok, "unexpected tool name: %s", name)
		assert.NotEmpty(t, def.Function.Description)
		assert.NotNil(t, def.Function.Parameters)
		expectedNames[name] = true
	}
	for name, found := range expectedNames {
		assert.True(t, found, "tool %q not returned by LSPTools", name)
	}
}

func TestFindDefinition(t *testing.T) {
	root := "/workspace"

	t.Run("happy path", func(t *testing.T) {
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return []semanticapi.SymbolInformation{
					symInfo("Foo", semanticapi.SymbolKindFunction, "file:///workspace/pkg/foo.go", 9),
				}, nil
			},
			definitionFn: func(p semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
				assert.Equal(t, "file:///workspace/pkg/foo.go", p.TextDocument.URI)
				assert.Equal(t, uint32(9), p.Position.Line)
				return semanticapi.LocationResult{
					Location: &semanticapi.Location{
						URI:   "file:///workspace/pkg/foo.go",
						Range: semanticapi.Range{Start: semanticapi.Position{Line: 9}},
					},
				}, nil
			},
		}
		tool := &findDefinitionTool{lsp: lsp, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"symbol":"Foo"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "pkg/foo.go:10")
	})

	t.Run("symbol not found", func(t *testing.T) {
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return nil, nil
			},
		}
		tool := &findDefinitionTool{lsp: lsp, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"symbol":"Nonexistent"}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "not found")
	})

	t.Run("workspace symbol error", func(t *testing.T) {
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return nil, fmt.Errorf("connection reset")
			},
		}
		tool := &findDefinitionTool{lsp: lsp, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"symbol":"Foo"}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "connection reset")
	})

	t.Run("definition error", func(t *testing.T) {
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return []semanticapi.SymbolInformation{
					symInfo("Foo", semanticapi.SymbolKindFunction, "file:///workspace/foo.go", 0),
				}, nil
			},
			definitionFn: func(p semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
				return semanticapi.LocationResult{}, fmt.Errorf("timeout")
			},
		}
		tool := &findDefinitionTool{lsp: lsp, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"symbol":"Foo"}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "timeout")
	})

	t.Run("invalid JSON", func(t *testing.T) {
		tool := &findDefinitionTool{lsp: &stubLSP{}, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `bad`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "invalid arguments")
	})

	t.Run("summary", func(t *testing.T) {
		tool := &findDefinitionTool{lsp: &stubLSP{}, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root)}
		assert.Equal(t, "Foo", tool.Summary(`{"symbol":"Foo"}`))
		assert.Equal(t, "", tool.Summary(`bad`))
	})
}

func TestFindImplementations(t *testing.T) {
	root := "/workspace"

	t.Run("happy path returns multiple locations", func(t *testing.T) {
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return []semanticapi.SymbolInformation{
					symInfo("Reader", semanticapi.SymbolKindInterface, "file:///workspace/io.go", 5),
				}, nil
			},
			implementationFn: func(p semanticapi.ImplementationParams) (semanticapi.LocationResult, error) {
				return semanticapi.LocationResult{
					Locations: []semanticapi.Location{
						loc("file:///workspace/bufio.go", 19, 0),
						loc("file:///workspace/bytes.go", 42, 0),
					},
				}, nil
			},
		}
		tool := &findImplementationsTool{lsp: lsp, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"symbol":"Reader"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "bufio.go:20")
		assert.Contains(t, result.Content, "bytes.go:43")
	})

	t.Run("summary", func(t *testing.T) {
		tool := &findImplementationsTool{lsp: &stubLSP{}, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root)}
		assert.Equal(t, "Reader", tool.Summary(`{"symbol":"Reader"}`))
	})
}

func TestFindReferences(t *testing.T) {
	t.Run("qualified name adds no hint", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.go"),
			[]byte("package main\n\nfunc Foo() {}\n"), 0o644))
		lsp := &stubLSP{
			referencesFn: func(p semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
				return []semanticapi.Location{loc("file://"+dir+"/foo.go", 2, 5)}, nil
			},
		}
		parser := &fakeParser{resolveFn: func(name string) ([]syntaxapi.Match, error) {
			return []syntaxapi.Match{{
				URI: "file://" + dir + "/foo.go",
				Pos: term.Coordinates{X: 5, Y: 2},
			}}, nil
		}}
		tool := &findReferencesTool{lsp: lsp, fs: localFS{}, parser: parser, cwd: dirURI(dir), tracker: NewFileTracker()}
		result := tool.Execute(context.Background(), `{"symbol":"pkg.Foo"}`)

		assert.False(t, result.IsError)
		assert.NotContains(t, result.Content, "Hint:")
	})

	t.Run("happy path returns multiple references", func(t *testing.T) {
		dir := t.TempDir()
		// Create source files so the tool can read line content.
		require.NoError(t, os.WriteFile(filepath.Join(dir, "foo.go"),
			[]byte("package main\n\nimport \"fmt\"\n\nfunc Foo() {}\n\nfunc init() { Foo() }\n"), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "bar.go"),
			[]byte("package main\n\nfunc Bar() { Foo() }\n"), 0o644))

		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return []semanticapi.SymbolInformation{
					symInfo("Foo", semanticapi.SymbolKindFunction, "file://"+dir+"/foo.go", 4),
				}, nil
			},
			referencesFn: func(p semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
				assert.Equal(t, "file://"+dir+"/foo.go", p.TextDocument.URI)
				assert.Equal(t, uint32(4), p.Position.Line)
				assert.True(t, p.Context.IncludeDeclaration)
				return []semanticapi.Location{
					loc("file://"+dir+"/foo.go", 4, 5),  // declaration
					loc("file://"+dir+"/foo.go", 6, 14), // usage in init
					loc("file://"+dir+"/bar.go", 2, 14), // usage in bar
				}, nil
			},
		}
		tool := &findReferencesTool{lsp: lsp, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(dir), tracker: NewFileTracker()}
		result := tool.Execute(context.Background(), `{"symbol":"Foo"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "foo.go:5:func Foo() {}")
		assert.Contains(t, result.Content, "foo.go:7:func init() { Foo() }")
		assert.Contains(t, result.Content, "bar.go:3:func Bar() { Foo() }")
		// A bare name fell back to fuzzy search: the result carries a
		// hint nudging the model toward a dot-qualified lookup.
		assert.Contains(t, result.Content, "Hint:")
		assert.True(t, strings.HasPrefix(result.Content, "Hint:"),
			"hint must be prepended before the references")
	})

	t.Run("no references found", func(t *testing.T) {
		root := "/workspace"
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return []semanticapi.SymbolInformation{
					symInfo("Foo", semanticapi.SymbolKindFunction, "file:///workspace/foo.go", 5),
				}, nil
			},
			referencesFn: func(p semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
				return nil, nil
			},
		}
		tool := &findReferencesTool{lsp: lsp, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root), tracker: NewFileTracker()}
		result := tool.Execute(context.Background(), `{"symbol":"Foo"}`)

		assert.False(t, result.IsError)
		// The bare name still earns a hint even when no references
		// come back; the empty-result body follows it.
		assert.True(t, strings.HasPrefix(result.Content, "Hint:"))
		assert.True(t, strings.HasSuffix(result.Content, "no references found"))
	})

	t.Run("symbol not found", func(t *testing.T) {
		root := "/workspace"
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return nil, nil
			},
		}
		tool := &findReferencesTool{lsp: lsp, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root), tracker: NewFileTracker()}
		result := tool.Execute(context.Background(), `{"symbol":"Nonexistent"}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "not found")
	})

	t.Run("workspace symbol error", func(t *testing.T) {
		root := "/workspace"
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return nil, fmt.Errorf("connection reset")
			},
		}
		tool := &findReferencesTool{lsp: lsp, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root), tracker: NewFileTracker()}
		result := tool.Execute(context.Background(), `{"symbol":"Foo"}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "connection reset")
	})

	t.Run("references error", func(t *testing.T) {
		root := "/workspace"
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return []semanticapi.SymbolInformation{
					symInfo("Foo", semanticapi.SymbolKindFunction, "file:///workspace/foo.go", 0),
				}, nil
			},
			referencesFn: func(p semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
				return nil, fmt.Errorf("timeout")
			},
		}
		tool := &findReferencesTool{lsp: lsp, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root), tracker: NewFileTracker()}
		result := tool.Execute(context.Background(), `{"symbol":"Foo"}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "timeout")
	})

	t.Run("invalid JSON", func(t *testing.T) {
		root := "/workspace"
		tool := &findReferencesTool{lsp: &stubLSP{}, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root), tracker: NewFileTracker()}
		result := tool.Execute(context.Background(), `bad`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "invalid arguments")
	})

	t.Run("summary", func(t *testing.T) {
		root := "/workspace"
		tool := &findReferencesTool{lsp: &stubLSP{}, fs: localFS{}, parser: &fakeParser{}, cwd: dirURI(root), tracker: NewFileTracker()}
		assert.Equal(t, "Foo", tool.Summary(`{"symbol":"Foo"}`))
		assert.Equal(t, "", tool.Summary(`bad`))
	})
}

func TestOutlineFile(t *testing.T) {
	root := "/workspace"

	t.Run("document symbols with children", func(t *testing.T) {
		lsp := &stubLSP{
			documentSymbolFn: func(p semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error) {
				return semanticapi.DocumentSymbolResult{
					DocumentSymbols: []semanticapi.DocumentSymbol{
						{
							Name: "MyStruct",
							Kind: semanticapi.SymbolKindStruct,
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 4},
							},
							Children: []semanticapi.DocumentSymbol{
								{
									Name: "DoWork",
									Kind: semanticapi.SymbolKindMethod,
									Range: semanticapi.Range{
										Start: semanticapi.Position{Line: 10},
									},
								},
							},
						},
						{
							Name: "Init",
							Kind: semanticapi.SymbolKindFunction,
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 0},
							},
						},
					},
				}, nil
			},
		}
		tool := &outlineFileTool{lsp: lsp, fs: localFS{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"file_path":"pkg/main.go"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "pkg/main.go:5:struct:MyStruct")
		assert.Contains(t, result.Content, "pkg/main.go:11:method:MyStruct.DoWork")
		assert.Contains(t, result.Content, "pkg/main.go:1:function:Init")
	})

	t.Run("symbol information fallback", func(t *testing.T) {
		lsp := &stubLSP{
			documentSymbolFn: func(p semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error) {
				return semanticapi.DocumentSymbolResult{
					SymbolInformation: []semanticapi.SymbolInformation{
						symInfo("Foo", semanticapi.SymbolKindFunction, "file:///workspace/foo.go", 3),
					},
				}, nil
			},
		}
		tool := &outlineFileTool{lsp: lsp, fs: localFS{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"file_path":"foo.go"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "foo.go:4:function:Foo")
	})

	t.Run("no symbols", func(t *testing.T) {
		lsp := &stubLSP{
			documentSymbolFn: func(p semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error) {
				return semanticapi.DocumentSymbolResult{}, nil
			},
		}
		tool := &outlineFileTool{lsp: lsp, fs: localFS{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"file_path":"empty.go"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "no symbols found")
	})

	t.Run("error", func(t *testing.T) {
		lsp := &stubLSP{
			documentSymbolFn: func(p semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error) {
				return semanticapi.DocumentSymbolResult{}, fmt.Errorf("server unavailable")
			},
		}
		tool := &outlineFileTool{lsp: lsp, fs: localFS{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"file_path":"foo.go"}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "server unavailable")
	})

	t.Run("summary", func(t *testing.T) {
		tool := &outlineFileTool{lsp: &stubLSP{}, fs: localFS{}, cwd: dirURI(root)}
		assert.Equal(t, "pkg/main.go", tool.Summary(`{"file_path":"pkg/main.go"}`))
	})

	t.Run("file_path fallback", func(t *testing.T) {
		lsp := &stubLSP{
			documentSymbolFn: func(p semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error) {
				return semanticapi.DocumentSymbolResult{
					DocumentSymbols: []semanticapi.DocumentSymbol{
						{
							Name: "Foo",
							Kind: semanticapi.SymbolKindFunction,
							Range: semanticapi.Range{
								Start: semanticapi.Position{Line: 0},
							},
						},
					},
				}, nil
			},
		}
		tool := &outlineFileTool{lsp: lsp, fs: localFS{}, cwd: dirURI(root)}
		// LLM sends "file_path" instead of "path" — should still work via fallback.
		result := tool.Execute(context.Background(), `{"file_path":"pkg/main.go"}`)
		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "pkg/main.go:1:function:Foo")
	})

	t.Run("missing path", func(t *testing.T) {
		tool := &outlineFileTool{lsp: &stubLSP{}, fs: localFS{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{}`)
		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "missing required parameter: path")
	})
}

func TestSearchSymbols(t *testing.T) {
	root := "/workspace"

	t.Run("happy path", func(t *testing.T) {
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				assert.Equal(t, "Handle", p.Query)
				return []semanticapi.SymbolInformation{
					symInfo("HandleRequest", semanticapi.SymbolKindFunction, "file:///workspace/handler.go", 15),
					symInfo("HandleError", semanticapi.SymbolKindFunction, "file:///workspace/errors.go", 30),
				}, nil
			},
		}
		tool := &searchSymbolsTool{lsp: lsp, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"query":"Handle"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "handler.go:16:function:HandleRequest")
		assert.Contains(t, result.Content, "errors.go:31:function:HandleError")
	})

	t.Run("no results", func(t *testing.T) {
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return nil, nil
			},
		}
		tool := &searchSymbolsTool{lsp: lsp, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"query":"zzz"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "no symbols found")
	})

	t.Run("error", func(t *testing.T) {
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return nil, fmt.Errorf("rpc failed")
			},
		}
		tool := &searchSymbolsTool{lsp: lsp, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"query":"Foo"}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "rpc failed")
	})

	t.Run("summary", func(t *testing.T) {
		tool := &searchSymbolsTool{lsp: &stubLSP{}, cwd: dirURI(root)}
		assert.Equal(t, "Handle", tool.Summary(`{"query":"Handle"}`))
	})
}

func TestDescribeSymbol(t *testing.T) {
	root := "/workspace"

	t.Run("markup content", func(t *testing.T) {
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return []semanticapi.SymbolInformation{
					symInfo("Foo", semanticapi.SymbolKindFunction, "file:///workspace/foo.go", 5),
				}, nil
			},
			hoverFn: func(p semanticapi.HoverParams) (*semanticapi.Hover, error) {
				return &semanticapi.Hover{
					Contents: semanticapi.MarkupContent{
						Kind:  semanticapi.MarkupKindMarkdown,
						Value: "```go\nfunc Foo() error\n```\nFoo does things.",
					},
				}, nil
			},
		}
		tool := &describeSymbolTool{lsp: lsp, parser: &fakeParser{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"symbol":"Foo"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "func Foo() error")
		assert.Contains(t, result.Content, "Foo does things.")
	})

	t.Run("marked string fallback", func(t *testing.T) {
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return []semanticapi.SymbolInformation{
					symInfo("Bar", semanticapi.SymbolKindVariable, "file:///workspace/bar.go", 0),
				}, nil
			},
			hoverFn: func(p semanticapi.HoverParams) (*semanticapi.Hover, error) {
				return &semanticapi.Hover{
					ContentsMarked: &semanticapi.MarkedString{Value: "var Bar int"},
				}, nil
			},
		}
		tool := &describeSymbolTool{lsp: lsp, parser: &fakeParser{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"symbol":"Bar"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "var Bar int")
	})

	t.Run("nil hover", func(t *testing.T) {
		lsp := &stubLSP{
			workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
				return []semanticapi.SymbolInformation{
					symInfo("X", semanticapi.SymbolKindVariable, "file:///workspace/x.go", 0),
				}, nil
			},
			hoverFn: func(p semanticapi.HoverParams) (*semanticapi.Hover, error) {
				return nil, nil
			},
		}
		tool := &describeSymbolTool{lsp: lsp, parser: &fakeParser{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"symbol":"X"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "no information available")
	})

	t.Run("summary", func(t *testing.T) {
		tool := &describeSymbolTool{lsp: &stubLSP{}, parser: &fakeParser{}, cwd: dirURI(root)}
		assert.Equal(t, "Foo", tool.Summary(`{"symbol":"Foo"}`))
	})
}

func TestCheckFileErrors(t *testing.T) {
	root := "/workspace"

	t.Run("reports sorted by severity and line", func(t *testing.T) {
		lsp := &stubLSP{
			diagnosticFn: func(p semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error) {
				return semanticapi.DocumentDiagnosticReport{
					Items: []semanticapi.Diagnostic{
						{
							Range:    semanticapi.Range{Start: semanticapi.Position{Line: 19, Character: 4}},
							Severity: semanticapi.DiagnosticSeverityWarning,
							Message:  "unused variable",
							Source:   "compiler",
						},
						{
							Range:    semanticapi.Range{Start: semanticapi.Position{Line: 9, Character: 0}},
							Severity: semanticapi.DiagnosticSeverityError,
							Message:  "undeclared name: foo",
							Source:   "compiler",
						},
					},
				}, nil
			},
		}
		tool := &checkFileErrorsTool{lsp: lsp, fs: localFS{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"file_path":"main.go"}`)

		assert.False(t, result.IsError)
		// Error should come before warning (sorted by severity).
		errIdx := strings.Index(result.Content, "error:")
		warnIdx := strings.Index(result.Content, "warning:")
		assert.Less(t, errIdx, warnIdx, "errors should appear before warnings")
		assert.Contains(t, result.Content, "main.go:10:error:undeclared name: foo [compiler]")
		assert.Contains(t, result.Content, "main.go:20:warning:unused variable [compiler]")
	})

	t.Run("no diagnostics", func(t *testing.T) {
		lsp := &stubLSP{
			diagnosticFn: func(p semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error) {
				return semanticapi.DocumentDiagnosticReport{}, nil
			},
		}
		tool := &checkFileErrorsTool{lsp: lsp, fs: localFS{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"file_path":"clean.go"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "no errors or warnings")
	})

	t.Run("error from LSP", func(t *testing.T) {
		lsp := &stubLSP{
			diagnosticFn: func(p semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error) {
				return semanticapi.DocumentDiagnosticReport{}, fmt.Errorf("server crashed")
			},
		}
		tool := &checkFileErrorsTool{lsp: lsp, fs: localFS{}, cwd: dirURI(root)}
		result := tool.Execute(context.Background(), `{"file_path":"main.go"}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "server crashed")
	})

	t.Run("summary", func(t *testing.T) {
		tool := &checkFileErrorsTool{lsp: &stubLSP{}, fs: localFS{}, cwd: dirURI(root)}
		assert.Equal(t, "main.go", tool.Summary(`{"file_path":"main.go"}`))
	})
}

func TestFormatFile(t *testing.T) {
	t.Run("applies edits to file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"),
			[]byte("package main\n\nfunc  main()  {\n}\n"), 0o644))

		lsp := &stubLSP{
			formattingFn: func(p semanticapi.DocumentFormattingParams) ([]semanticapi.TextEdit, error) {
				return []semanticapi.TextEdit{
					{
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 2, Character: 4},
							End:   semanticapi.Position{Line: 2, Character: 6},
						},
						NewText: " ",
					},
					{
						Range: semanticapi.Range{
							Start: semanticapi.Position{Line: 2, Character: 12},
							End:   semanticapi.Position{Line: 2, Character: 14},
						},
						NewText: " ",
					},
				}, nil
			},
		}
		tool := &formatFileTool{lsp: lsp, fs: localFS{}, cwd: dirURI(dir)}
		result := tool.Execute(context.Background(), `{"file_path":"main.go"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "formatted")
		assert.Contains(t, result.Content, "2 edits")

		data, err := os.ReadFile(filepath.Join(dir, "main.go"))
		require.NoError(t, err)
		assert.Equal(t, "package main\n\nfunc main() {\n}\n", string(data))
	})

	t.Run("no changes needed", func(t *testing.T) {
		dir := t.TempDir()
		lsp := &stubLSP{
			formattingFn: func(p semanticapi.DocumentFormattingParams) ([]semanticapi.TextEdit, error) {
				return nil, nil
			},
		}
		tool := &formatFileTool{lsp: lsp, fs: localFS{}, cwd: dirURI(dir)}
		result := tool.Execute(context.Background(), `{"file_path":"main.go"}`)

		assert.False(t, result.IsError)
		assert.Contains(t, result.Content, "no changes needed")
	})

	t.Run("formatting error", func(t *testing.T) {
		dir := t.TempDir()
		lsp := &stubLSP{
			formattingFn: func(p semanticapi.DocumentFormattingParams) ([]semanticapi.TextEdit, error) {
				return nil, fmt.Errorf("parse error")
			},
		}
		tool := &formatFileTool{lsp: lsp, fs: localFS{}, cwd: dirURI(dir)}
		result := tool.Execute(context.Background(), `{"file_path":"main.go"}`)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "parse error")
	})

	t.Run("summary", func(t *testing.T) {
		tool := &formatFileTool{lsp: &stubLSP{}, fs: localFS{}, cwd: dirURI("/w")}
		assert.Equal(t, "main.go", tool.Summary(`{"file_path":"main.go"}`))
	})
}

func TestApplyTextEdits(t *testing.T) {
	t.Run("single line replacement", func(t *testing.T) {
		content := "line0\nline1\nline2\n"
		edits := []semanticapi.TextEdit{
			{
				Range: semanticapi.Range{
					Start: semanticapi.Position{Line: 1, Character: 0},
					End:   semanticapi.Position{Line: 1, Character: 5},
				},
				NewText: "replaced",
			},
		}
		result := applyTextEdits(content, edits)
		assert.Equal(t, "line0\nreplaced\nline2\n", result)
	})

	t.Run("multi-line deletion", func(t *testing.T) {
		content := "a\nb\nc\nd\n"
		edits := []semanticapi.TextEdit{
			{
				Range: semanticapi.Range{
					Start: semanticapi.Position{Line: 1, Character: 0},
					End:   semanticapi.Position{Line: 2, Character: 1},
				},
				NewText: "",
			},
		}
		result := applyTextEdits(content, edits)
		assert.Equal(t, "a\n\nd\n", result)
	})

	t.Run("multiple non-overlapping edits", func(t *testing.T) {
		content := "aaa\nbbb\nccc\n"
		edits := []semanticapi.TextEdit{
			{
				Range: semanticapi.Range{
					Start: semanticapi.Position{Line: 0, Character: 0},
					End:   semanticapi.Position{Line: 0, Character: 3},
				},
				NewText: "AAA",
			},
			{
				Range: semanticapi.Range{
					Start: semanticapi.Position{Line: 2, Character: 0},
					End:   semanticapi.Position{Line: 2, Character: 3},
				},
				NewText: "CCC",
			},
		}
		result := applyTextEdits(content, edits)
		assert.Equal(t, "AAA\nbbb\nCCC\n", result)
	})

	t.Run("empty edits returns unchanged", func(t *testing.T) {
		content := "hello\nworld\n"
		result := applyTextEdits(content, nil)
		assert.Equal(t, content, result)
	})
}

func TestSymbolContextAtPositionUsesExactCursor(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "scope.go")
	require.NoError(t, os.WriteFile(source, []byte(strings.Join([]string{
		"package scope",
		"func f() {",
		"\tvalue := 1",
		"\t_ = value",
		"}",
	}, "\n")), 0o600))
	uri := "file://" + source
	pos := semanticapi.Position{Line: 3, Character: 5}
	assertParams := func(doc semanticapi.TextDocumentIdentifier, got semanticapi.Position) {
		assert.Equal(t, uri, doc.URI)
		assert.Equal(t, pos, got)
	}
	lsp := &stubLSP{
		workspaceSymbolFn: func(semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
			t.Fatal("cursor resolution must not call WorkspaceSymbol")
			return nil, nil
		},
		definitionFn: func(p semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
			assertParams(p.TextDocument, p.Position)
			definition := loc(uri, 2, 1)
			return semanticapi.LocationResult{Location: &definition}, nil
		},
		referencesFn: func(p semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
			assertParams(p.TextDocument, p.Position)
			assert.True(t, p.Context.IncludeDeclaration)
			return []semanticapi.Location{loc(uri, 2, 1), loc(uri, 3, 5)}, nil
		},
		hoverFn: func(p semanticapi.HoverParams) (*semanticapi.Hover, error) {
			assertParams(p.TextDocument, p.Position)
			return &semanticapi.Hover{
				Contents: semanticapi.MarkupContent{Value: "```go\nvar value int\n```"},
			}, nil
		},
	}

	got, err := SymbolContextAtPosition(
		context.Background(), lsp, localFS{root: root}, dirURI(root),
		dirURI(source), pos,
	)
	require.NoError(t, err)
	assert.Equal(t, SymbolContext{
		Definition:    "scope.go:3",
		References:    "scope.go:3:\tvalue := 1\nscope.go:4:\t_ = value",
		Documentation: "```go\nvar value int\n```",
	}, got)
}

func TestFormatLocations(t *testing.T) {
	root := "/workspace"

	t.Run("single location", func(t *testing.T) {
		result := formatLocations(semanticapi.LocationResult{
			Location: &semanticapi.Location{
				URI:   "file:///workspace/foo.go",
				Range: semanticapi.Range{Start: semanticapi.Position{Line: 9}},
			},
		}, dirURI(root))
		assert.Equal(t, "foo.go:10", result)
	})

	t.Run("multiple locations", func(t *testing.T) {
		result := formatLocations(semanticapi.LocationResult{
			Locations: []semanticapi.Location{
				loc("file:///workspace/a.go", 0, 0),
				loc("file:///workspace/b.go", 4, 0),
			},
		}, dirURI(root))
		assert.Contains(t, result, "a.go:1")
		assert.Contains(t, result, "b.go:5")
	})

	t.Run("location links", func(t *testing.T) {
		result := formatLocations(semanticapi.LocationResult{
			LocationLinks: []semanticapi.LocationLink{
				{
					TargetURI:   "file:///workspace/target.go",
					TargetRange: semanticapi.Range{Start: semanticapi.Position{Line: 14}},
				},
			},
		}, dirURI(root))
		assert.Contains(t, result, "target.go:15")
	})

	t.Run("empty result", func(t *testing.T) {
		result := formatLocations(semanticapi.LocationResult{}, dirURI(root))
		assert.Equal(t, "no results found", result)
	})
}

func TestSymbolKindName(t *testing.T) {
	assert.Equal(t, "function", symbolKindName(semanticapi.SymbolKindFunction))
	assert.Equal(t, "struct", symbolKindName(semanticapi.SymbolKindStruct))
	assert.Equal(t, "interface", symbolKindName(semanticapi.SymbolKindInterface))
	assert.Equal(t, "method", symbolKindName(semanticapi.SymbolKindMethod))
	assert.Equal(t, "variable", symbolKindName(semanticapi.SymbolKindVariable))
	assert.Equal(t, "constant", symbolKindName(semanticapi.SymbolKindConstant))
	assert.Equal(t, "kind(99)", symbolKindName(semanticapi.SymbolKind(99)))
}

func TestSeverityName(t *testing.T) {
	assert.Equal(t, "error", severityName(semanticapi.DiagnosticSeverityError))
	assert.Equal(t, "warning", severityName(semanticapi.DiagnosticSeverityWarning))
	assert.Equal(t, "info", severityName(semanticapi.DiagnosticSeverityInformation))
	assert.Equal(t, "hint", severityName(semanticapi.DiagnosticSeverityHint))
	assert.Equal(t, "unknown", severityName(semanticapi.DiagnosticSeverity(99)))
}

func TestFilePathArgsFallback(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		want    string
		wantErr bool
	}{
		{"file_path only", `{"file_path":"main.go"}`, "main.go", false},
		{"path only", `{"path":"main.go"}`, "main.go", false},
		{"both prefers path", `{"file_path":"a.go","path":"b.go"}`, "b.go", false},
		{"empty", `{}`, "", true},
		{"both empty strings", `{"file_path":"","path":""}`, "", true},
		{"file_path empty path set", `{"file_path":"","path":"main.go"}`, "main.go", false},
		{"path empty file_path set", `{"file_path":"main.go","path":""}`, "main.go", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var args filePathArgs
			err := json.Unmarshal([]byte(tt.json), &args)
			require.NoError(t, err)
			got, err := args.filePath()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestLSPToolSummaries(t *testing.T) {
	lsp := &stubLSP{}
	fs := localFS{}
	root := "/workspace"

	tests := []struct {
		name     string
		tool     agent.Tool
		args     string
		expected string
	}{
		{"find_definition", &findDefinitionTool{lsp: lsp, fs: fs, parser: &fakeParser{}, cwd: dirURI(root)}, `{"symbol":"Foo"}`, "Foo"},
		{"find_definition invalid", &findDefinitionTool{lsp: lsp, fs: fs, parser: &fakeParser{}, cwd: dirURI(root)}, `bad`, ""},
		{"find_implementations", &findImplementationsTool{lsp: lsp, fs: fs, parser: &fakeParser{}, cwd: dirURI(root)}, `{"symbol":"Reader"}`, "Reader"},
		{"find_references", &findReferencesTool{lsp: lsp, fs: fs, parser: &fakeParser{}, cwd: dirURI(root), tracker: NewFileTracker()}, `{"symbol":"Foo"}`, "Foo"},
		{"outline_file", &outlineFileTool{lsp: lsp, fs: fs, cwd: dirURI(root)}, `{"path":"main.go"}`, "main.go"},
		{"outline_file file_path fallback", &outlineFileTool{lsp: lsp, fs: fs, cwd: dirURI(root)}, `{"file_path":"main.go"}`, "main.go"},
		{"search_symbols", &searchSymbolsTool{lsp: lsp, cwd: dirURI(root)}, `{"query":"Handle"}`, "Handle"},
		{"describe_symbol", &describeSymbolTool{lsp: lsp, parser: &fakeParser{}, cwd: dirURI(root)}, `{"symbol":"Foo"}`, "Foo"},
		{"check_file_errors", &checkFileErrorsTool{lsp: lsp, fs: fs, cwd: dirURI(root)}, `{"path":"main.go"}`, "main.go"},
		{"check_file_errors file_path fallback", &checkFileErrorsTool{lsp: lsp, fs: fs, cwd: dirURI(root)}, `{"file_path":"main.go"}`, "main.go"},
		{"format_file", &formatFileTool{lsp: lsp, fs: fs, cwd: dirURI(root)}, `{"path":"main.go"}`, "main.go"},
		{"format_file file_path fallback", &formatFileTool{lsp: lsp, fs: fs, cwd: dirURI(root)}, `{"file_path":"main.go"}`, "main.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.tool.Summary(tt.args))
		})
	}
}

// --- Symbol resolver integration (RUNE-190) ---

func TestResolveSymbolDottedSkipsWorkspaceSymbol(t *testing.T) {
	// (a) describe_symbol iterator.Iterator → ambiguous → returns
	//     Hover for both packages under "# path:line" headers; assert
	//     WorkspaceSymbol was NOT called.
	parsed := func(s string) workspaceapi.URI {
		u, _ := workspaceapi.ParseURI(s)
		return u
	}
	wsCalled := false
	hovers := map[string]string{
		"file:///workspace/blue/iterator/iterator.go": "type Iterator (blue)",
		"file:///workspace/sdk/iterator/iterator.go":  "type Iterator (sdk)",
	}
	lsp := &stubLSP{
		workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
			wsCalled = true
			return nil, nil
		},
		hoverFn: func(p semanticapi.HoverParams) (*semanticapi.Hover, error) {
			v, ok := hovers[p.TextDocument.URI]
			if !ok {
				return nil, fmt.Errorf("unexpected URI: %s", p.TextDocument.URI)
			}
			return &semanticapi.Hover{
				Contents: semanticapi.MarkupContent{Kind: semanticapi.MarkupKindMarkdown, Value: v},
			}, nil
		},
	}
	blueURI := parsed("file:///workspace/blue/iterator/iterator.go")
	sdkURI := parsed("file:///workspace/sdk/iterator/iterator.go")
	parser := &fakeParser{
		resolveFn: func(string) ([]syntaxapi.Match, error) {
			return []syntaxapi.Match{
				{URI: blueURI.String(), Pos: term.Coordinates{X: 5, Y: 9}},
				{URI: sdkURI.String(), Pos: term.Coordinates{X: 5, Y: 14}},
			}, nil
		},
	}
	tool := &describeSymbolTool{lsp: lsp, parser: parser, cwd: dirURI("/workspace")}
	got := tool.Execute(context.Background(), `{"symbol":"iterator.Iterator"}`)
	assert.False(t, got.IsError, got.Content)
	assert.False(t, wsCalled, "WorkspaceSymbol must not be called for dotted names that resolve")
	assert.Contains(t, got.Content, "# blue/iterator/iterator.go:10")
	assert.Contains(t, got.Content, "# sdk/iterator/iterator.go:15")
	assert.Contains(t, got.Content, "type Iterator (blue)")
	assert.Contains(t, got.Content, "type Iterator (sdk)")
	assert.NotContains(t, got.Content, "TestStreamIterator")
}

func TestResolveSymbolDefinitionOnly(t *testing.T) {
	// (b) describe_symbol iterator.Reduce — definition-only resolves
	//     via SearchNode phase.
	parsed := func(s string) workspaceapi.URI {
		u, _ := workspaceapi.ParseURI(s)
		return u
	}
	uri := parsed("file:///workspace/iterator/reduce.go")
	lsp := &stubLSP{
		hoverFn: func(p semanticapi.HoverParams) (*semanticapi.Hover, error) {
			return &semanticapi.Hover{
				Contents: semanticapi.MarkupContent{Value: "func Reduce[T any](...)"},
			}, nil
		},
	}
	parser := &fakeParser{
		resolveFn: func(string) ([]syntaxapi.Match, error) {
			return []syntaxapi.Match{
				{URI: uri.String(), Pos: term.Coordinates{X: 5, Y: 41}},
			}, nil
		},
	}
	tool := &describeSymbolTool{lsp: lsp, parser: parser, cwd: dirURI("/workspace")}
	got := tool.Execute(context.Background(), `{"symbol":"iterator.Reduce"}`)
	assert.False(t, got.IsError, got.Content)
	assert.Equal(t, "func Reduce[T any](...)", got.Content)
}

func TestResolveSymbolNoDotFallsBackToWorkspaceSymbol(t *testing.T) {
	// (c) describe_symbol Foo (no dot) falls back to WorkspaceSymbol
	//     and hovers all returned syms.
	wsCalled := false
	lsp := &stubLSP{
		workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
			wsCalled = true
			return []semanticapi.SymbolInformation{
				symInfo("Foo", semanticapi.SymbolKindFunction, "file:///workspace/a.go", 1),
				symInfo("Foo", semanticapi.SymbolKindFunction, "file:///workspace/b.go", 2),
			}, nil
		},
		hoverFn: func(p semanticapi.HoverParams) (*semanticapi.Hover, error) {
			return &semanticapi.Hover{
				Contents: semanticapi.MarkupContent{Value: "hover " + p.TextDocument.URI},
			}, nil
		},
	}
	tool := &describeSymbolTool{lsp: lsp, parser: &fakeParser{}, cwd: dirURI("/workspace")}
	got := tool.Execute(context.Background(), `{"symbol":"Foo"}`)
	assert.False(t, got.IsError, got.Content)
	assert.True(t, wsCalled)
	assert.Contains(t, got.Content, "# a.go:2")
	assert.Contains(t, got.Content, "# b.go:3")
	assert.Contains(t, got.Content, "hover file:///workspace/a.go")
	assert.Contains(t, got.Content, "hover file:///workspace/b.go")
}

func TestFindDefinitionTwoMatches(t *testing.T) {
	// (d) two-match find_definition.
	parsed := func(s string) workspaceapi.URI {
		u, _ := workspaceapi.ParseURI(s)
		return u
	}
	blueURI := parsed("file:///workspace/blue/iterator/iterator.go")
	sdkURI := parsed("file:///workspace/sdk/iterator/iterator.go")
	defResult := func(uri string, line uint32) semanticapi.LocationResult {
		return semanticapi.LocationResult{
			Location: &semanticapi.Location{
				URI: uri, Range: semanticapi.Range{Start: semanticapi.Position{Line: line}},
			},
		}
	}
	lsp := &stubLSP{
		definitionFn: func(p semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
			return defResult(p.TextDocument.URI, p.Position.Line), nil
		},
	}
	parser := &fakeParser{
		resolveFn: func(string) ([]syntaxapi.Match, error) {
			return []syntaxapi.Match{
				{URI: blueURI.String(), Pos: term.Coordinates{Y: 9}},
				{URI: sdkURI.String(), Pos: term.Coordinates{Y: 14}},
			}, nil
		},
	}
	tool := &findDefinitionTool{
		lsp: lsp, fs: localFS{}, parser: parser,
		cwd: dirURI("/workspace"), tracker: NewFileTracker(),
	}
	got := tool.Execute(context.Background(), `{"symbol":"iterator.Iterator"}`)
	assert.False(t, got.IsError, got.Content)
	assert.Contains(t, got.Content, "# blue/iterator/iterator.go:10")
	assert.Contains(t, got.Content, "# sdk/iterator/iterator.go:15")
}

func TestFindReferencesTwoMatches(t *testing.T) {
	parsed := func(s string) workspaceapi.URI {
		u, _ := workspaceapi.ParseURI(s)
		return u
	}
	aURI := parsed("file:///workspace/a/x.go")
	bURI := parsed("file:///workspace/b/x.go")
	lsp := &stubLSP{
		referencesFn: func(p semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
			return []semanticapi.Location{
				{URI: p.TextDocument.URI, Range: semanticapi.Range{Start: semanticapi.Position{Line: 0}}},
			}, nil
		},
	}
	parser := &fakeParser{
		resolveFn: func(string) ([]syntaxapi.Match, error) {
			return []syntaxapi.Match{
				{URI: aURI.String(), Pos: term.Coordinates{Y: 1}},
				{URI: bURI.String(), Pos: term.Coordinates{Y: 1}},
			}, nil
		},
	}
	tool := &findReferencesTool{
		lsp: lsp, fs: localFS{}, parser: parser,
		cwd: dirURI("/workspace"), tracker: NewFileTracker(),
	}
	got := tool.Execute(context.Background(), `{"symbol":"x.Foo"}`)
	assert.False(t, got.IsError, got.Content)
	assert.Contains(t, got.Content, "# a/x.go:2")
	assert.Contains(t, got.Content, "# b/x.go:2")
}

func TestFindImplementationsTwoMatches(t *testing.T) {
	parsed := func(s string) workspaceapi.URI {
		u, _ := workspaceapi.ParseURI(s)
		return u
	}
	aURI := parsed("file:///workspace/a/x.go")
	bURI := parsed("file:///workspace/b/x.go")
	lsp := &stubLSP{
		implementationFn: func(p semanticapi.ImplementationParams) (semanticapi.LocationResult, error) {
			return semanticapi.LocationResult{
				Location: &semanticapi.Location{
					URI:   p.TextDocument.URI,
					Range: semanticapi.Range{Start: semanticapi.Position{Line: 0}},
				},
			}, nil
		},
	}
	parser := &fakeParser{
		resolveFn: func(string) ([]syntaxapi.Match, error) {
			return []syntaxapi.Match{
				{URI: aURI.String(), Pos: term.Coordinates{Y: 1}},
				{URI: bURI.String(), Pos: term.Coordinates{Y: 1}},
			}, nil
		},
	}
	tool := &findImplementationsTool{
		lsp: lsp, fs: localFS{}, parser: parser,
		cwd: dirURI("/workspace"), tracker: NewFileTracker(),
	}
	got := tool.Execute(context.Background(), `{"symbol":"x.Reader"}`)
	assert.False(t, got.IsError, got.Content)
	assert.Contains(t, got.Content, "# a/x.go:2")
	assert.Contains(t, got.Content, "# b/x.go:2")
}

func TestResolveSymbolCapsWorkspaceSymbol(t *testing.T) {
	// (e) maxSymbolMatches cap: WorkspaceSymbol returns 50 → at most
	//     8 follow-ups + truncation marker.
	wsCount := 0
	hoverCount := 0
	lsp := &stubLSP{
		workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
			wsCount++
			out := make([]semanticapi.SymbolInformation, 50)
			for i := range out {
				out[i] = symInfo("Foo", semanticapi.SymbolKindFunction,
					fmt.Sprintf("file:///workspace/f%d.go", i), uint32(i))
			}
			return out, nil
		},
		hoverFn: func(p semanticapi.HoverParams) (*semanticapi.Hover, error) {
			hoverCount++
			return &semanticapi.Hover{
				Contents: semanticapi.MarkupContent{Value: "x"},
			}, nil
		},
	}
	tool := &describeSymbolTool{lsp: lsp, parser: &fakeParser{}, cwd: dirURI("/workspace")}
	got := tool.Execute(context.Background(), `{"symbol":"Foo"}`)
	assert.False(t, got.IsError, got.Content)
	assert.Equal(t, 1, wsCount)
	assert.Equal(t, maxSymbolMatches, hoverCount)
	assert.Contains(t, got.Content, "(matches truncated to 8 of")
}

func TestResolveSymbolSingleMatchByteIdentical(t *testing.T) {
	// (f) single-match output is byte-identical to today (no header).
	lsp := &stubLSP{
		workspaceSymbolFn: func(p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
			return []semanticapi.SymbolInformation{
				symInfo("Foo", semanticapi.SymbolKindFunction, "file:///workspace/pkg/foo.go", 9),
			}, nil
		},
		definitionFn: func(p semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
			return semanticapi.LocationResult{
				Location: &semanticapi.Location{
					URI:   "file:///workspace/pkg/foo.go",
					Range: semanticapi.Range{Start: semanticapi.Position{Line: 9}},
				},
			}, nil
		},
	}
	tool := &findDefinitionTool{
		lsp: lsp, fs: localFS{}, parser: &fakeParser{},
		cwd: dirURI("/workspace"), tracker: NewFileTracker(),
	}
	got := tool.Execute(context.Background(), `{"symbol":"pkg.Foo"}`)
	assert.False(t, got.IsError, got.Content)
	// No header for single match.
	assert.Equal(t, "pkg/foo.go:10", got.Content)
}
