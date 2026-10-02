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

package agentshell

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/tui"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
)

// Regression for RUNE-AGENT-95: agentshell.handleDream used to build
// dream.Deps without DataPath, panicking on every `/agent dream`.
func TestAgentShell_DreamCommand_NoPanic(t *testing.T) {
	t.Parallel()

	dataPath := newMemoryWorkspaceDir(t)
	cwd := dirURI(t.TempDir())

	fs := osFS{}
	sh := New(
		stubWindowManager{},
		&stopOnlyLLMService{},
		"test-model",
		&emptyDialogueStore{},
		agent.NewRegistry(),
		&agent.Cfg{},
		nil,
		skills.NewRegistry(fs, cwd, nil, nil),
		cwd,
		fs,
		storagestub.NewInMemoryService(),
		mockExec{},
		noopLSP{},
		nil,
		noopNotifications{},
		dataPath,
	)

	ctx := t.Context()

	it, err := sh.HandleCommand(ctx,
		repl.Command{Name: CommandName, Args: []string{"dream"}},
		noopProgressWriter{})
	require.NoError(t, err)
	require.NotNil(t, it)

	count := 0
	for {
		_, ok := it.Next(ctx)
		if !ok {
			break
		}
		count++
	}
	require.NoError(t, it.Err(), "dream iterator returned an error")
	require.Positive(t, count, "dream emitted no progress events")
}

func TestAgentShell_New_PanicsWithoutMemoryDataPath(t *testing.T) {
	t.Parallel()

	fs := osFS{}
	cwd := dirURI(t.TempDir())

	require.PanicsWithValue(t,
		"agentshell: memoryDataPath must not be empty",
		func() {
			_ = New(
				stubWindowManager{},
				&stopOnlyLLMService{},
				"test-model",
				&emptyDialogueStore{},
				agent.NewRegistry(),
				&agent.Cfg{},
				nil,
				skills.NewRegistry(fs, cwd, nil, nil),
				cwd,
				fs,
				storagestub.NewInMemoryService(),
				mockExec{},
				noopLSP{},
				nil,
				noopNotifications{},
				"",
			)
		})
}

// newMemoryWorkspaceDir seeds the memory workspace with a version
// marker matching dream's current template so bootstrap is a no-op
// and the pipeline does not shell out to `go mod tidy`.
func newMemoryWorkspaceDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module memories\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "version"),
		[]byte("99\n"), 0o644))
	return dir
}

type noopProgressWriter struct{}

func (noopProgressWriter) Progress(int64, int64, string) {}

type stubWindowManager struct{}

func (stubWindowManager) Focus() (browserapi.Window, error) { return nil, nil }
func (stubWindowManager) Split(browserapi.Orientation, browserapi.Window, browserapi.Handler) (browserapi.Window, error) {
	return nil, nil
}
func (stubWindowManager) Floating(browserapi.Floating, browserapi.FloatingConfig) (browserapi.Window, error) {
	return nil, nil
}
func (stubWindowManager) Bar(browserapi.BarConfig, tui.Handler) error { return nil }
func (stubWindowManager) Tab(workspaceapi.URI, rune, string, browserapi.Handler) (browserapi.Handler, error) {
	return nil, nil
}
func (stubWindowManager) SetWindowContent(browserapi.Window, browserapi.Handler) error { return nil }
func (stubWindowManager) CloseWindow(browserapi.Window) error                          { return nil }
func (stubWindowManager) SetTabName(workspaceapi.URI, string) error                    { return nil }

func (stubWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

var _ browserapi.WindowManager = stubWindowManager{}

type emptyDialogueStore struct{}

func (emptyDialogueStore) Health(context.Context) error { return nil }
func (emptyDialogueStore) Create(context.Context, dialoguemanager.Dialogue) error {
	return nil
}
func (emptyDialogueStore) Get(context.Context, string) (dialoguemanager.Dialogue, error) {
	return dialoguemanager.Dialogue{}, storageapi.ErrNotFound
}
func (emptyDialogueStore) Delete(context.Context, string) error { return nil }
func (emptyDialogueStore) AppendMessages(context.Context, dialoguemanager.Dialogue,
	[]llmapi.Message, llmapi.DialogueUsage) error {
	return nil
}
func (emptyDialogueStore) ArchiveAndReplace(context.Context,
	dialoguemanager.ArchiveAndReplaceParams) error {
	return nil
}
func (emptyDialogueStore) List(context.Context) (
	iterator.Iterator[dialoguemanager.DialogueHeader], error,
) {
	return iterator.FromSlice([]dialoguemanager.DialogueHeader{}), nil
}
func (emptyDialogueStore) SetTitle(context.Context, string, string) error { return nil }

var _ dialoguemanager.Store = emptyDialogueStore{}

type stopOnlyLLMService struct{}

func (s *stopOnlyLLMService) CreateCompletion(
	ctx context.Context, _ llmapi.ModelEntry, _ llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return iterator.FromSlice([]llmapi.Event{
		{Type: llmapi.EventStreamDone, DoneData: &llmapi.DoneData{
			Message:      llmapi.Message{Role: llmapi.RoleAssistant},
			FinishReason: llmapi.FinishReasonStop,
		}},
	}), nil
}

func (s *stopOnlyLLMService) CountTokens(llmapi.ModelEntry, []llmapi.Message) (int, error) {
	return 0, nil
}

func (s *stopOnlyLLMService) Models() iterator.Iterator[llmapi.ModelEntry] {
	return iterator.FromSlice([]llmapi.ModelEntry{
		{Name: "test-model", ContextWindow: 100000},
	})
}

func (s *stopOnlyLLMService) GetModel(
	_ context.Context, model llmapi.ModelEntry,
) (llmapi.ModelEntry, error) {
	if model.Name == "" {
		model.Name = "test-model"
	}
	if model.ContextWindow == 0 {
		model.ContextWindow = 100000
	}
	return model, nil
}

var _ llmapi.Service = (*stopOnlyLLMService)(nil)

type osFS struct{}

func (osFS) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.CurrentUserHostURI(path)
}
func (osFS) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(path, flag, mode)
}
func (osFS) Remove(path string) error                     { return os.Remove(path) }
func (osFS) Stat(path string) (os.FileInfo, error)        { return os.Stat(path) }
func (osFS) ReadDir(name string) ([]os.DirEntry, error)   { return os.ReadDir(name) }
func (osFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

var _ workspaceapi.FileSystem = osFS{}

type mockExec struct{}

func (mockExec) Start(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	if cmd.Watcher != nil {
		go func() { cmd.Watcher.WatchProcess() <- nil }()
	}
	return 0, nil
}

func (mockExec) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (mockExec) Close() error                                  { return nil }

var _ workspaceapi.Executor = mockExec{}

type noopLSP struct{}

func (noopLSP) Initialize(context.Context, semanticapi.InitializeParams) (semanticapi.InitializeResult, error) {
	return semanticapi.InitializeResult{}, nil
}
func (noopLSP) Initialized(context.Context) error { return nil }
func (noopLSP) Shutdown(context.Context) error    { return nil }
func (noopLSP) Exit(context.Context) error        { return nil }
func (noopLSP) DidOpen(context.Context, semanticapi.DidOpenTextDocumentParams) error {
	return nil
}
func (noopLSP) DidChange(context.Context, semanticapi.DidChangeTextDocumentParams) error {
	return nil
}
func (noopLSP) DidClose(context.Context, semanticapi.DidCloseTextDocumentParams) error {
	return nil
}
func (noopLSP) DidSave(context.Context, semanticapi.DidSaveTextDocumentParams) error { return nil }
func (noopLSP) Completion(context.Context, semanticapi.CompletionParams) (semanticapi.CompletionResult, error) {
	return semanticapi.CompletionResult{}, nil
}
func (noopLSP) Hover(context.Context, semanticapi.HoverParams) (*semanticapi.Hover, error) {
	return nil, nil
}
func (noopLSP) SignatureHelp(context.Context, semanticapi.SignatureHelpParams) (*semanticapi.SignatureHelp, error) {
	return nil, nil
}
func (noopLSP) Definition(context.Context, semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (noopLSP) Declaration(context.Context, semanticapi.DeclarationParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (noopLSP) TypeDefinition(context.Context, semanticapi.TypeDefinitionParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (noopLSP) Implementation(context.Context, semanticapi.ImplementationParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (noopLSP) References(context.Context, semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
	return nil, nil
}
func (noopLSP) DocumentHighlight(context.Context, semanticapi.DocumentHighlightParams) ([]semanticapi.DocumentHighlight, error) {
	return nil, nil
}
func (noopLSP) DocumentSymbol(context.Context, semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error) {
	return semanticapi.DocumentSymbolResult{}, nil
}
func (noopLSP) CodeAction(context.Context, semanticapi.CodeActionParams) ([]semanticapi.CodeActionResult, error) {
	return nil, nil
}
func (noopLSP) CodeLens(context.Context, semanticapi.CodeLensParams) ([]semanticapi.CodeLens, error) {
	return nil, nil
}
func (noopLSP) Formatting(context.Context, semanticapi.DocumentFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (noopLSP) RangeFormatting(context.Context, semanticapi.DocumentRangeFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (noopLSP) Rename(context.Context, semanticapi.RenameParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (noopLSP) PrepareRename(context.Context, semanticapi.PrepareRenameParams) (*semanticapi.PrepareRenameResult, error) {
	return nil, nil
}
func (noopLSP) FoldingRange(context.Context, semanticapi.FoldingRangeParams) ([]semanticapi.FoldingRange, error) {
	return nil, nil
}
func (noopLSP) SelectionRange(context.Context, semanticapi.SelectionRangeParams) ([]semanticapi.SelectionRange, error) {
	return nil, nil
}
func (noopLSP) SemanticTokensFull(context.Context, semanticapi.SemanticTokensParams) (*semanticapi.SemanticTokens, error) {
	return nil, nil
}
func (noopLSP) SemanticTokensRange(context.Context, semanticapi.SemanticTokensRangeParams) (*semanticapi.SemanticTokens, error) {
	return nil, nil
}
func (noopLSP) Diagnostic(context.Context, semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error) {
	return semanticapi.DocumentDiagnosticReport{}, nil
}
func (noopLSP) WorkspaceDiagnostic(context.Context, semanticapi.WorkspaceDiagnosticParams) (semanticapi.WorkspaceDiagnosticReport, error) {
	return semanticapi.WorkspaceDiagnosticReport{}, nil
}
func (noopLSP) WorkspaceSymbol(context.Context, semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
	return nil, nil
}
func (noopLSP) ExecuteCommand(context.Context, semanticapi.ExecuteCommandParams) (string, error) {
	return "", nil
}
func (noopLSP) ExecuteRequest(context.Context, semanticapi.ExecuteRequestParams) (json.RawMessage, error) {
	return json.RawMessage("null"), nil
}
func (noopLSP) SendNotification(context.Context, semanticapi.NotificationParams) error {
	return nil
}
func (noopLSP) PrepareCallHierarchy(context.Context, semanticapi.CallHierarchyPrepareParams) ([]semanticapi.CallHierarchyItem, error) {
	return nil, nil
}
func (noopLSP) CallHierarchyIncomingCalls(context.Context, semanticapi.CallHierarchyIncomingCallsParams) ([]semanticapi.CallHierarchyIncomingCall, error) {
	return nil, nil
}
func (noopLSP) CallHierarchyOutgoingCalls(context.Context, semanticapi.CallHierarchyOutgoingCallsParams) ([]semanticapi.CallHierarchyOutgoingCall, error) {
	return nil, nil
}
func (noopLSP) CompletionResolve(context.Context, semanticapi.CompletionItem) (semanticapi.CompletionItem, error) {
	return semanticapi.CompletionItem{}, nil
}
func (noopLSP) CodeLensResolve(context.Context, semanticapi.CodeLens) (semanticapi.CodeLens, error) {
	return semanticapi.CodeLens{}, nil
}
func (noopLSP) DocumentColor(context.Context, semanticapi.DocumentColorParams) ([]semanticapi.ColorInformation, error) {
	return nil, nil
}
func (noopLSP) ColorPresentation(context.Context, semanticapi.ColorPresentationParams) ([]semanticapi.ColorPresentation, error) {
	return nil, nil
}
func (noopLSP) DocumentLink(context.Context, semanticapi.DocumentLinkParams) ([]semanticapi.DocumentLink, error) {
	return nil, nil
}
func (noopLSP) DocumentLinkResolve(context.Context, semanticapi.DocumentLink) (semanticapi.DocumentLink, error) {
	return semanticapi.DocumentLink{}, nil
}
func (noopLSP) OnTypeFormatting(context.Context, semanticapi.DocumentOnTypeFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (noopLSP) LinkedEditingRange(context.Context, semanticapi.LinkedEditingRangeParams) (*semanticapi.LinkedEditingRanges, error) {
	return nil, nil
}
func (noopLSP) Moniker(context.Context, semanticapi.MonikerParams) ([]semanticapi.Moniker, error) {
	return nil, nil
}
func (noopLSP) WillSaveWaitUntil(context.Context, semanticapi.WillSaveTextDocumentParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (noopLSP) SemanticTokensFullDelta(context.Context, semanticapi.SemanticTokensDeltaParams) (*semanticapi.SemanticTokensDelta, error) {
	return nil, nil
}
func (noopLSP) PrepareTypeHierarchy(context.Context, semanticapi.TypeHierarchyPrepareParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (noopLSP) TypeHierarchySupertypes(context.Context, semanticapi.TypeHierarchySupertypesParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (noopLSP) TypeHierarchySubtypes(context.Context, semanticapi.TypeHierarchySubtypesParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (noopLSP) InlayHint(context.Context, semanticapi.InlayHintParams) ([]semanticapi.InlayHint, error) {
	return nil, nil
}
func (noopLSP) InlayHintResolve(context.Context, semanticapi.InlayHint) (semanticapi.InlayHint, error) {
	return semanticapi.InlayHint{}, nil
}
func (noopLSP) InlineValue(context.Context, semanticapi.InlineValueParams) ([]semanticapi.InlineValue, error) {
	return nil, nil
}
func (noopLSP) WillCreateFiles(context.Context, semanticapi.CreateFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (noopLSP) WillRenameFiles(context.Context, semanticapi.RenameFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (noopLSP) WillDeleteFiles(context.Context, semanticapi.DeleteFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (noopLSP) WillSave(context.Context, semanticapi.WillSaveTextDocumentParams) error {
	return nil
}
func (noopLSP) DidChangeConfiguration(context.Context, semanticapi.DidChangeConfigurationParams) error {
	return nil
}
func (noopLSP) DidChangeWatchedFiles(context.Context, semanticapi.DidChangeWatchedFilesParams) error {
	return nil
}
func (noopLSP) DidChangeWorkspaceFolders(context.Context, semanticapi.DidChangeWorkspaceFoldersParams) error {
	return nil
}
func (noopLSP) WorkDoneProgressCancel(context.Context, semanticapi.WorkDoneProgressCancelParams) error {
	return nil
}
func (noopLSP) SetTrace(context.Context, semanticapi.SetTraceParams) error { return nil }
func (noopLSP) DidCreateFiles(context.Context, semanticapi.CreateFilesParams) error {
	return nil
}
func (noopLSP) DidRenameFiles(context.Context, semanticapi.RenameFilesParams) error {
	return nil
}
func (noopLSP) DidDeleteFiles(context.Context, semanticapi.DeleteFilesParams) error {
	return nil
}

var _ semanticapi.LSP = noopLSP{}

type noopNotifications struct{}

func (noopNotifications) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}
func (noopNotifications) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}
func (noopNotifications) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

var (
	_ browserapi.Notifications = noopNotifications{}
	_ textapi.REPLHandler      = (*shell)(nil)
)
