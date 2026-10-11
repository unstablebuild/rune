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
	"context"
	"encoding/json"

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
)

// noopLSP is a no-op semanticapi.LSP. The e2e harness embeds it in
// captureLSP so only Initialize needs a custom implementation while the
// rest of the large LSP surface is satisfied with zero-value returns.
type noopLSP struct{}

func (noopLSP) Initialized(_ context.Context) error                                      { return nil }
func (noopLSP) Shutdown(_ context.Context) error                                         { return nil }
func (noopLSP) Exit(_ context.Context) error                                             { return nil }
func (noopLSP) DidOpen(_ context.Context, _ semanticapi.DidOpenTextDocumentParams) error { return nil }
func (noopLSP) DidChange(_ context.Context, _ semanticapi.DidChangeTextDocumentParams) error {
	return nil
}
func (noopLSP) DidClose(_ context.Context, _ semanticapi.DidCloseTextDocumentParams) error {
	return nil
}
func (noopLSP) DidSave(_ context.Context, _ semanticapi.DidSaveTextDocumentParams) error { return nil }
func (noopLSP) Completion(_ context.Context, _ semanticapi.CompletionParams) (semanticapi.CompletionResult, error) {
	return semanticapi.CompletionResult{}, nil
}
func (noopLSP) Hover(_ context.Context, _ semanticapi.HoverParams) (*semanticapi.Hover, error) {
	return nil, nil
}
func (noopLSP) SignatureHelp(_ context.Context, _ semanticapi.SignatureHelpParams) (*semanticapi.SignatureHelp, error) {
	return nil, nil
}
func (noopLSP) Definition(_ context.Context, _ semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (noopLSP) Declaration(_ context.Context, _ semanticapi.DeclarationParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (noopLSP) TypeDefinition(_ context.Context, _ semanticapi.TypeDefinitionParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (noopLSP) Implementation(_ context.Context, _ semanticapi.ImplementationParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (noopLSP) References(_ context.Context, _ semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
	return nil, nil
}
func (noopLSP) DocumentHighlight(_ context.Context, _ semanticapi.DocumentHighlightParams) ([]semanticapi.DocumentHighlight, error) {
	return nil, nil
}
func (noopLSP) DocumentSymbol(_ context.Context, _ semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error) {
	return semanticapi.DocumentSymbolResult{}, nil
}
func (noopLSP) CodeAction(_ context.Context, _ semanticapi.CodeActionParams) ([]semanticapi.CodeActionResult, error) {
	return nil, nil
}
func (noopLSP) CodeLens(_ context.Context, _ semanticapi.CodeLensParams) ([]semanticapi.CodeLens, error) {
	return nil, nil
}
func (noopLSP) Formatting(_ context.Context, _ semanticapi.DocumentFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (noopLSP) RangeFormatting(_ context.Context, _ semanticapi.DocumentRangeFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (noopLSP) Rename(_ context.Context, _ semanticapi.RenameParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (noopLSP) PrepareRename(_ context.Context, _ semanticapi.PrepareRenameParams) (*semanticapi.PrepareRenameResult, error) {
	return nil, nil
}
func (noopLSP) FoldingRange(_ context.Context, _ semanticapi.FoldingRangeParams) ([]semanticapi.FoldingRange, error) {
	return nil, nil
}
func (noopLSP) SelectionRange(_ context.Context, _ semanticapi.SelectionRangeParams) ([]semanticapi.SelectionRange, error) {
	return nil, nil
}
func (noopLSP) SemanticTokensFull(_ context.Context, _ semanticapi.SemanticTokensParams) (*semanticapi.SemanticTokens, error) {
	return nil, nil
}
func (noopLSP) SemanticTokensRange(_ context.Context, _ semanticapi.SemanticTokensRangeParams) (*semanticapi.SemanticTokens, error) {
	return nil, nil
}
func (noopLSP) Diagnostic(_ context.Context, _ semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error) {
	return semanticapi.DocumentDiagnosticReport{}, nil
}
func (noopLSP) WorkspaceDiagnostic(_ context.Context, _ semanticapi.WorkspaceDiagnosticParams) (semanticapi.WorkspaceDiagnosticReport, error) {
	return semanticapi.WorkspaceDiagnosticReport{}, nil
}
func (noopLSP) WorkspaceSymbol(_ context.Context, _ semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
	return nil, nil
}
func (noopLSP) ExecuteCommand(_ context.Context, _ semanticapi.ExecuteCommandParams) (string, error) {
	return "", nil
}
func (noopLSP) ExecuteRequest(_ context.Context, _ semanticapi.ExecuteRequestParams) (json.RawMessage, error) {
	return json.RawMessage("null"), nil
}
func (noopLSP) SendNotification(_ context.Context, _ semanticapi.NotificationParams) error {
	return nil
}
func (noopLSP) PrepareCallHierarchy(_ context.Context, _ semanticapi.CallHierarchyPrepareParams) ([]semanticapi.CallHierarchyItem, error) {
	return nil, nil
}
func (noopLSP) CallHierarchyIncomingCalls(_ context.Context, _ semanticapi.CallHierarchyIncomingCallsParams) ([]semanticapi.CallHierarchyIncomingCall, error) {
	return nil, nil
}
func (noopLSP) CallHierarchyOutgoingCalls(_ context.Context, _ semanticapi.CallHierarchyOutgoingCallsParams) ([]semanticapi.CallHierarchyOutgoingCall, error) {
	return nil, nil
}
func (noopLSP) CompletionResolve(_ context.Context, _ semanticapi.CompletionItem) (semanticapi.CompletionItem, error) {
	return semanticapi.CompletionItem{}, nil
}
func (noopLSP) CodeLensResolve(_ context.Context, _ semanticapi.CodeLens) (semanticapi.CodeLens, error) {
	return semanticapi.CodeLens{}, nil
}
func (noopLSP) DocumentColor(_ context.Context, _ semanticapi.DocumentColorParams) ([]semanticapi.ColorInformation, error) {
	return nil, nil
}
func (noopLSP) ColorPresentation(_ context.Context, _ semanticapi.ColorPresentationParams) ([]semanticapi.ColorPresentation, error) {
	return nil, nil
}
func (noopLSP) DocumentLink(_ context.Context, _ semanticapi.DocumentLinkParams) ([]semanticapi.DocumentLink, error) {
	return nil, nil
}
func (noopLSP) DocumentLinkResolve(_ context.Context, _ semanticapi.DocumentLink) (semanticapi.DocumentLink, error) {
	return semanticapi.DocumentLink{}, nil
}
func (noopLSP) OnTypeFormatting(_ context.Context, _ semanticapi.DocumentOnTypeFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (noopLSP) LinkedEditingRange(_ context.Context, _ semanticapi.LinkedEditingRangeParams) (*semanticapi.LinkedEditingRanges, error) {
	return nil, nil
}
func (noopLSP) Moniker(_ context.Context, _ semanticapi.MonikerParams) ([]semanticapi.Moniker, error) {
	return nil, nil
}
func (noopLSP) WillSaveWaitUntil(_ context.Context, _ semanticapi.WillSaveTextDocumentParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (noopLSP) SemanticTokensFullDelta(_ context.Context, _ semanticapi.SemanticTokensDeltaParams) (*semanticapi.SemanticTokensDelta, error) {
	return nil, nil
}
func (noopLSP) PrepareTypeHierarchy(_ context.Context, _ semanticapi.TypeHierarchyPrepareParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (noopLSP) TypeHierarchySupertypes(_ context.Context, _ semanticapi.TypeHierarchySupertypesParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (noopLSP) TypeHierarchySubtypes(_ context.Context, _ semanticapi.TypeHierarchySubtypesParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (noopLSP) InlayHint(_ context.Context, _ semanticapi.InlayHintParams) ([]semanticapi.InlayHint, error) {
	return nil, nil
}
func (noopLSP) InlayHintResolve(_ context.Context, _ semanticapi.InlayHint) (semanticapi.InlayHint, error) {
	return semanticapi.InlayHint{}, nil
}
func (noopLSP) InlineValue(_ context.Context, _ semanticapi.InlineValueParams) ([]semanticapi.InlineValue, error) {
	return nil, nil
}
func (noopLSP) WillCreateFiles(_ context.Context, _ semanticapi.CreateFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (noopLSP) WillRenameFiles(_ context.Context, _ semanticapi.RenameFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (noopLSP) WillDeleteFiles(_ context.Context, _ semanticapi.DeleteFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (noopLSP) WillSave(_ context.Context, _ semanticapi.WillSaveTextDocumentParams) error {
	return nil
}
func (noopLSP) DidChangeConfiguration(_ context.Context, _ semanticapi.DidChangeConfigurationParams) error {
	return nil
}
func (noopLSP) DidChangeWatchedFiles(_ context.Context, _ semanticapi.DidChangeWatchedFilesParams) error {
	return nil
}
func (noopLSP) DidChangeWorkspaceFolders(_ context.Context, _ semanticapi.DidChangeWorkspaceFoldersParams) error {
	return nil
}
func (noopLSP) WorkDoneProgressCancel(_ context.Context, _ semanticapi.WorkDoneProgressCancelParams) error {
	return nil
}
func (noopLSP) SetTrace(_ context.Context, _ semanticapi.SetTraceParams) error          { return nil }
func (noopLSP) DidCreateFiles(_ context.Context, _ semanticapi.CreateFilesParams) error { return nil }
func (noopLSP) DidRenameFiles(_ context.Context, _ semanticapi.RenameFilesParams) error { return nil }
func (noopLSP) DidDeleteFiles(_ context.Context, _ semanticapi.DeleteFilesParams) error { return nil }
