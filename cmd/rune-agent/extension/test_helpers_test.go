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

package extension

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// testLocalFS is a minimal workspaceapi.FileSystem implementation that
// resolves relative paths against root and shells out to os.* for
// everything else. Used by e2e tests that need a real on-disk
// workspace for the agent's tools to walk.
type testLocalFS struct{ root string }

func (f testLocalFS) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(f.root, path)
}

func (f testLocalFS) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + f.resolve(path))
}
func (f testLocalFS) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(f.resolve(path), flag, mode)
}
func (f testLocalFS) Remove(path string) error              { return os.Remove(f.resolve(path)) }
func (f testLocalFS) Stat(path string) (os.FileInfo, error) { return os.Stat(f.resolve(path)) }
func (f testLocalFS) ReadDir(name string) ([]os.DirEntry, error) {
	return os.ReadDir(f.resolve(name))
}
func (f testLocalFS) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(f.resolve(path), perm)
}

// testLocalExec is a minimal workspaceapi.Executor implementation that runs
// commands on the local machine. Used by gitidentity_test.go to invoke real
// git binaries against temp repos and by handler_test.go to invoke the
// bash tool against the host shell.
type testLocalExec struct{}

func (testLocalExec) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Dir = cmd.Dir
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	c.Env = cmd.Env

	if err := c.Start(); err != nil {
		return 0, err
	}
	pid := workspaceapi.Pid(c.Process.Pid)

	go func() {
		err := c.Wait()
		if cmd.Watcher != nil {
			cmd.Watcher.WatchProcess() <- err
		}
	}()

	return pid, nil
}

func (testLocalExec) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	proc, err := os.FindProcess(int(pid))
	if err != nil {
		return err
	}
	return proc.Signal(sig)
}

func (testLocalExec) Close() error { return nil }

// stubLSP is a no-op semanticapi.LSP whose Diagnostic call is the only
// configurable hook. e2e tests use it to simulate a host where no
// language server is running for a file's language.
type stubLSP struct {
	diagnosticFn      func(semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error)
	definitionFn      func(semanticapi.DefinitionParams) (semanticapi.LocationResult, error)
	referencesFn      func(semanticapi.ReferenceParams) ([]semanticapi.Location, error)
	hoverFn           func(semanticapi.HoverParams) (*semanticapi.Hover, error)
	workspaceSymbolFn func(semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error)
}

func (s stubLSP) Diagnostic(_ context.Context, p semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error) {
	if s.diagnosticFn != nil {
		return s.diagnosticFn(p)
	}
	return semanticapi.DocumentDiagnosticReport{}, nil
}

func (stubLSP) Initialize(context.Context, semanticapi.InitializeParams) (semanticapi.InitializeResult, error) {
	return semanticapi.InitializeResult{}, nil
}
func (stubLSP) Initialized(context.Context) error                                    { return nil }
func (stubLSP) Shutdown(context.Context) error                                       { return nil }
func (stubLSP) Exit(context.Context) error                                           { return nil }
func (stubLSP) DidOpen(context.Context, semanticapi.DidOpenTextDocumentParams) error { return nil }
func (stubLSP) DidChange(context.Context, semanticapi.DidChangeTextDocumentParams) error {
	return nil
}
func (stubLSP) DidClose(context.Context, semanticapi.DidCloseTextDocumentParams) error { return nil }
func (stubLSP) DidSave(context.Context, semanticapi.DidSaveTextDocumentParams) error   { return nil }
func (stubLSP) Completion(context.Context, semanticapi.CompletionParams) (semanticapi.CompletionResult, error) {
	return semanticapi.CompletionResult{}, nil
}
func (stubLSP) SignatureHelp(context.Context, semanticapi.SignatureHelpParams) (*semanticapi.SignatureHelp, error) {
	return nil, nil
}
func (stubLSP) Declaration(context.Context, semanticapi.DeclarationParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (s stubLSP) Definition(_ context.Context, p semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
	if s.definitionFn != nil {
		return s.definitionFn(p)
	}
	return semanticapi.LocationResult{}, nil
}
func (stubLSP) TypeDefinition(context.Context, semanticapi.TypeDefinitionParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (stubLSP) Implementation(context.Context, semanticapi.ImplementationParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (s stubLSP) References(_ context.Context, p semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
	if s.referencesFn != nil {
		return s.referencesFn(p)
	}
	return nil, nil
}
func (stubLSP) DocumentHighlight(context.Context, semanticapi.DocumentHighlightParams) ([]semanticapi.DocumentHighlight, error) {
	return nil, nil
}
func (stubLSP) DocumentSymbol(context.Context, semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error) {
	return semanticapi.DocumentSymbolResult{}, nil
}
func (s stubLSP) WorkspaceSymbol(_ context.Context, p semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
	if s.workspaceSymbolFn != nil {
		return s.workspaceSymbolFn(p)
	}
	return nil, nil
}
func (stubLSP) CodeAction(context.Context, semanticapi.CodeActionParams) ([]semanticapi.CodeActionResult, error) {
	return nil, nil
}
func (stubLSP) CodeLens(context.Context, semanticapi.CodeLensParams) ([]semanticapi.CodeLens, error) {
	return nil, nil
}
func (stubLSP) CodeLensResolve(context.Context, semanticapi.CodeLens) (semanticapi.CodeLens, error) {
	return semanticapi.CodeLens{}, nil
}
func (stubLSP) Formatting(context.Context, semanticapi.DocumentFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (stubLSP) RangeFormatting(context.Context, semanticapi.DocumentRangeFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (stubLSP) OnTypeFormatting(context.Context, semanticapi.DocumentOnTypeFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (stubLSP) Rename(context.Context, semanticapi.RenameParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (stubLSP) PrepareRename(context.Context, semanticapi.PrepareRenameParams) (*semanticapi.PrepareRenameResult, error) {
	return nil, nil
}
func (s stubLSP) Hover(_ context.Context, p semanticapi.HoverParams) (*semanticapi.Hover, error) {
	if s.hoverFn != nil {
		return s.hoverFn(p)
	}
	return nil, nil
}
func (stubLSP) FoldingRange(context.Context, semanticapi.FoldingRangeParams) ([]semanticapi.FoldingRange, error) {
	return nil, nil
}
func (stubLSP) SelectionRange(context.Context, semanticapi.SelectionRangeParams) ([]semanticapi.SelectionRange, error) {
	return nil, nil
}
func (stubLSP) SemanticTokensFull(context.Context, semanticapi.SemanticTokensParams) (*semanticapi.SemanticTokens, error) {
	return nil, nil
}
func (stubLSP) SemanticTokensFullDelta(context.Context, semanticapi.SemanticTokensDeltaParams) (*semanticapi.SemanticTokensDelta, error) {
	return nil, nil
}
func (stubLSP) SemanticTokensRange(context.Context, semanticapi.SemanticTokensRangeParams) (*semanticapi.SemanticTokens, error) {
	return nil, nil
}
func (stubLSP) WorkspaceDiagnostic(context.Context, semanticapi.WorkspaceDiagnosticParams) (semanticapi.WorkspaceDiagnosticReport, error) {
	return semanticapi.WorkspaceDiagnosticReport{}, nil
}
func (stubLSP) ExecuteCommand(context.Context, semanticapi.ExecuteCommandParams) (string, error) {
	return "", nil
}
func (stubLSP) ExecuteRequest(context.Context, semanticapi.ExecuteRequestParams) (json.RawMessage, error) {
	return json.RawMessage("null"), nil
}
func (stubLSP) SendNotification(context.Context, semanticapi.NotificationParams) error {
	return nil
}
func (stubLSP) PrepareCallHierarchy(context.Context, semanticapi.CallHierarchyPrepareParams) ([]semanticapi.CallHierarchyItem, error) {
	return nil, nil
}
func (stubLSP) CallHierarchyIncomingCalls(context.Context, semanticapi.CallHierarchyIncomingCallsParams) ([]semanticapi.CallHierarchyIncomingCall, error) {
	return nil, nil
}
func (stubLSP) CallHierarchyOutgoingCalls(context.Context, semanticapi.CallHierarchyOutgoingCallsParams) ([]semanticapi.CallHierarchyOutgoingCall, error) {
	return nil, nil
}
func (stubLSP) CompletionResolve(context.Context, semanticapi.CompletionItem) (semanticapi.CompletionItem, error) {
	return semanticapi.CompletionItem{}, nil
}
func (stubLSP) DocumentColor(context.Context, semanticapi.DocumentColorParams) ([]semanticapi.ColorInformation, error) {
	return nil, nil
}
func (stubLSP) ColorPresentation(context.Context, semanticapi.ColorPresentationParams) ([]semanticapi.ColorPresentation, error) {
	return nil, nil
}
func (stubLSP) DocumentLink(context.Context, semanticapi.DocumentLinkParams) ([]semanticapi.DocumentLink, error) {
	return nil, nil
}
func (stubLSP) DocumentLinkResolve(context.Context, semanticapi.DocumentLink) (semanticapi.DocumentLink, error) {
	return semanticapi.DocumentLink{}, nil
}
func (stubLSP) LinkedEditingRange(context.Context, semanticapi.LinkedEditingRangeParams) (*semanticapi.LinkedEditingRanges, error) {
	return nil, nil
}
func (stubLSP) Moniker(context.Context, semanticapi.MonikerParams) ([]semanticapi.Moniker, error) {
	return nil, nil
}
func (stubLSP) WillSaveWaitUntil(context.Context, semanticapi.WillSaveTextDocumentParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (stubLSP) PrepareTypeHierarchy(context.Context, semanticapi.TypeHierarchyPrepareParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (stubLSP) TypeHierarchySupertypes(context.Context, semanticapi.TypeHierarchySupertypesParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (stubLSP) TypeHierarchySubtypes(context.Context, semanticapi.TypeHierarchySubtypesParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (stubLSP) InlayHint(context.Context, semanticapi.InlayHintParams) ([]semanticapi.InlayHint, error) {
	return nil, nil
}
func (stubLSP) InlayHintResolve(context.Context, semanticapi.InlayHint) (semanticapi.InlayHint, error) {
	return semanticapi.InlayHint{}, nil
}
func (stubLSP) InlineValue(context.Context, semanticapi.InlineValueParams) ([]semanticapi.InlineValue, error) {
	return nil, nil
}
func (stubLSP) WillCreateFiles(context.Context, semanticapi.CreateFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (stubLSP) WillRenameFiles(context.Context, semanticapi.RenameFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (stubLSP) WillDeleteFiles(context.Context, semanticapi.DeleteFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (stubLSP) WillSave(context.Context, semanticapi.WillSaveTextDocumentParams) error { return nil }
func (stubLSP) DidChangeConfiguration(context.Context, semanticapi.DidChangeConfigurationParams) error {
	return nil
}
func (stubLSP) DidChangeWatchedFiles(context.Context, semanticapi.DidChangeWatchedFilesParams) error {
	return nil
}
func (stubLSP) DidChangeWorkspaceFolders(context.Context, semanticapi.DidChangeWorkspaceFoldersParams) error {
	return nil
}
func (stubLSP) WorkDoneProgressCancel(context.Context, semanticapi.WorkDoneProgressCancelParams) error {
	return nil
}
func (stubLSP) SetTrace(context.Context, semanticapi.SetTraceParams) error { return nil }
func (stubLSP) DidCreateFiles(context.Context, semanticapi.CreateFilesParams) error {
	return nil
}
func (stubLSP) DidRenameFiles(context.Context, semanticapi.RenameFilesParams) error {
	return nil
}
func (stubLSP) DidDeleteFiles(context.Context, semanticapi.DeleteFilesParams) error {
	return nil
}

var _ semanticapi.LSP = stubLSP{}

func (testLocalExec) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}
