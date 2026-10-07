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

package lspcmd

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// codeActionLSP returns a scripted set of code actions and records the
// request it answered plus every command it was asked to execute.
type codeActionLSP struct {
	stubLSP
	mu       sync.Mutex
	params   semanticapi.CodeActionParams
	results  []semanticapi.CodeActionResult
	executed []string
}

func (l *codeActionLSP) CodeAction(
	_ context.Context, params semanticapi.CodeActionParams,
) ([]semanticapi.CodeActionResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.params = params
	return l.results, nil
}

func (l *codeActionLSP) ExecuteCommand(
	_ context.Context, params semanticapi.ExecuteCommandParams,
) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.executed = append(l.executed, params.Command)
	return "", nil
}

func (l *codeActionLSP) request() semanticapi.CodeActionParams {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.params
}

func (l *codeActionLSP) commands() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.executed...)
}

// floatRecorder records the handler floated by the command under test.
type floatRecorder struct {
	mu       sync.Mutex
	floating browserapi.Floating
}

func (r *floatRecorder) manager() *mockWindowManager {
	return &mockWindowManager{
		floatingFn: func(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
			r.mu.Lock()
			r.floating = h
			r.mu.Unlock()
			return &mockWindow{id: 1}, nil
		},
	}
}

// picker polls until the command floats its picker, failing otherwise.
func (r *floatRecorder) picker(t *testing.T) *codeActionPicker {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		f := r.floating
		r.mu.Unlock()
		if p, ok := f.(*codeActionPicker); ok {
			return p
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for code-action picker")
	return nil
}

// floated returns whatever handler was floated, if any.
func (r *floatRecorder) floated() browserapi.Floating {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.floating
}

// notifyRecorder collects the messages notified by the command.
type notifyRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (n *notifyRecorder) notifications() *mockNotifications {
	return &mockNotifications{
		notifyFn: func(_ browserapi.NotificationLevel, msg string, _ ...any) (string, error) {
			n.mu.Lock()
			n.msgs = append(n.msgs, msg)
			n.mu.Unlock()
			return "", nil
		},
	}
}

func (n *notifyRecorder) messages() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.msgs...)
}

func codeActionResult(
	title string, kind semanticapi.CodeActionKind, cmd *semanticapi.Command,
) semanticapi.CodeActionResult {
	return semanticapi.CodeActionResult{
		CodeAction: &semanticapi.CodeAction{Title: title, Kind: kind, Command: cmd},
	}
}

type mockCellView struct {
	cells [][]term.Cell
}

func (m *mockCellView) RawCells() ([][]term.Cell, error) {
	return m.cells, nil
}

func codeActionTestURI(t *testing.T) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI("file:///ws/src/main.go")
	require.NoError(t, err)
	return uri
}

func codeActionCommand(uri workspaceapi.URI, name string) textapi.Command {
	return textapi.Command{
		Name:     name,
		URI:      uri,
		Resource: &mockHandler{uri: uri},
	}
}

func TestCodeActionRequiresOpenFile(t *testing.T) {
	t.Parallel()

	notify := &notifyRecorder{}
	h := CodeActionHandler(&codeActionLSP{}, &mockEditor{}, notify.notifications(),
		&mockWindowManager{}, NewSelectionTracker(), "refactor.rewrite", false, "hint")
	err := h.HandleCommand(t.Context(), textapi.Command{Name: "rewrite"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no file open")
}

func TestCodeActionNoActionNotifiesHint(t *testing.T) {
	t.Parallel()

	uri := codeActionTestURI(t)
	lsp := &codeActionLSP{}
	notify := &notifyRecorder{}
	floats := &floatRecorder{}
	h := CodeActionHandler(lsp, &mockEditor{}, notify.notifications(),
		floats.manager(), NewSelectionTracker(), "refactor.rewrite", false, "no rewrite here")

	require.NoError(t, h.HandleCommand(t.Context(), codeActionCommand(uri, "rewrite")))

	params := lsp.request()
	assert.Equal(t, []semanticapi.CodeActionKind{"refactor.rewrite"}, params.Context.Only)
	// Servers such as rust-analyzer reject a request whose
	// context.diagnostics is null.
	assert.NotNil(t, params.Context.Diagnostics)
	assert.Equal(t, []string{"no rewrite here"}, notify.messages())
}

func TestCodeActionConfirmsThroughPicker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		kind      semanticapi.CodeActionKind
		results   []semanticapi.CodeActionResult
		wantOnly  []semanticapi.CodeActionKind
		wantOffer []string
		wantExec  []string
	}{
		{
			// A broad kind is sent as the Only filter, and results whose
			// kind does not share the requested prefix are dropped.
			name: "filters results by kind prefix",
			kind: "refactor.extract",
			results: []semanticapi.CodeActionResult{
				codeActionResult("Extract into variable", "refactor.extract",
					&semanticapi.Command{Command: "extract"}),
				codeActionResult("Invert if", "refactor.rewrite",
					&semanticapi.Command{Command: "rewrite"}),
			},
			wantOnly:  []semanticapi.CodeActionKind{"refactor.extract"},
			wantOffer: []string{"Extract into variable"},
			wantExec:  []string{"extract"},
		},
		{
			name: "executes the action command",
			kind: "source.organizeImports",
			results: []semanticapi.CodeActionResult{
				codeActionResult("Organize imports", "source.organizeImports",
					&semanticapi.Command{Command: "gopls.organize_imports"}),
			},
			wantOnly:  []semanticapi.CodeActionKind{"source.organizeImports"},
			wantOffer: []string{"Organize imports"},
			wantExec:  []string{"gopls.organize_imports"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uri := codeActionTestURI(t)
			lsp := &codeActionLSP{results: tt.results}
			notify := &notifyRecorder{}
			floats := &floatRecorder{}
			h := CodeActionHandler(lsp, &mockEditor{}, notify.notifications(),
				floats.manager(), NewSelectionTracker(), tt.kind, false, "hint")

			done := make(chan error, 1)
			go func() {
				done <- h.HandleCommand(context.Background(), codeActionCommand(uri, "run"))
			}()

			picker := floats.picker(t)
			offered := make([]string, 0, len(picker.Actions()))
			for _, a := range picker.Actions() {
				offered = append(offered, a.Title)
			}
			assert.Equal(t, tt.wantOffer, offered)
			assert.Equal(t, tt.wantOnly, lsp.request().Context.Only)
			// Nothing may run before the user confirms.
			assert.Empty(t, lsp.commands())

			picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			require.NoError(t, <-done)
			assert.Equal(t, tt.wantExec, lsp.commands())
		})
	}
}

func TestCodeActionListRequestsAllKinds(t *testing.T) {
	t.Parallel()

	uri := codeActionTestURI(t)
	lsp := &codeActionLSP{results: []semanticapi.CodeActionResult{
		codeActionResult("Extract into variable", "refactor.extract", nil),
		codeActionResult("Invert if", "refactor.rewrite", nil),
	}}
	notify := &notifyRecorder{}
	floats := &floatRecorder{}
	h := CodeActionHandler(lsp, &mockEditor{}, notify.notifications(),
		floats.manager(), NewSelectionTracker(), "", false, "hint")

	// Two applicable actions means the picker is shown and HandleCommand
	// blocks on the user's choice. Run it in the background, wait for the
	// picker, then cancel to unblock without selecting anything.
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.HandleCommand(ctx, codeActionCommand(uri, "list")) }()

	picker := floats.picker(t)
	assert.Len(t, picker.Actions(), 2)
	assert.Nil(t, lsp.request().Context.Only)

	cancel()
	require.Error(t, <-done)
}

func TestCodeActionAppliesLoneAction(t *testing.T) {
	t.Parallel()

	t.Run("sole action runs without the picker", func(t *testing.T) {
		t.Parallel()

		uri := codeActionTestURI(t)
		lsp := &codeActionLSP{results: []semanticapi.CodeActionResult{
			codeActionResult("organize @import", "source.organizeImports",
				&semanticapi.Command{Command: "organize"}),
		}}
		notify := &notifyRecorder{}
		floats := &floatRecorder{}
		h := CodeActionHandler(lsp, &mockEditor{}, notify.notifications(),
			floats.manager(), NewSelectionTracker(),
			"source.organizeImports", true, "hint")

		require.NoError(t, h.HandleCommand(
			t.Context(), codeActionCommand(uri, "organize-imports")))

		assert.Nil(t, floats.floated(), "no picker may be shown")
		assert.Equal(t, []string{"organize"}, lsp.commands())
	})

	t.Run("several actions still need a choice", func(t *testing.T) {
		t.Parallel()

		uri := codeActionTestURI(t)
		lsp := &codeActionLSP{results: []semanticapi.CodeActionResult{
			codeActionResult("apply fix 1", "source.fixAll",
				&semanticapi.Command{Command: "fix1"}),
			codeActionResult("apply fix 2", "source.fixAll",
				&semanticapi.Command{Command: "fix2"}),
		}}
		notify := &notifyRecorder{}
		floats := &floatRecorder{}
		h := CodeActionHandler(lsp, &mockEditor{}, notify.notifications(),
			floats.manager(), NewSelectionTracker(), "source.fixAll", true, "hint")

		done := make(chan error, 1)
		go func() {
			done <- h.HandleCommand(context.Background(), codeActionCommand(uri, "fix-all"))
		}()

		picker := floats.picker(t)
		assert.Len(t, picker.Actions(), 2)
		assert.Empty(t, lsp.commands())

		picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
		require.NoError(t, <-done)
		assert.Equal(t, []string{"fix1"}, lsp.commands())
	})
}

func TestCodeActionPickerHasMinimumSize(t *testing.T) {
	t.Parallel()

	ch := make(chan int, 1)
	picker := newCodeActionPicker([]semanticapi.CodeAction{
		{Title: "organize @import"},
	}, ch)
	w, h := picker.Dimensions()
	assert.Equal(t, minPickerWidth, w)
	assert.Equal(t, minPickerHeight, h)

	long := strings.Repeat("x", minPickerWidth+10)
	actions := make([]semanticapi.CodeAction, maxPickerHeight+5)
	for i := range actions {
		actions[i] = semanticapi.CodeAction{Title: long}
	}
	w, h = newCodeActionPicker(actions, ch).Dimensions()
	assert.Equal(t, len(long), w, "a wider title still wins")
	assert.Equal(t, maxPickerHeight, h, "the height cap still applies")
}

func TestCodeActionSkipsNoopWorkspaceEdit(t *testing.T) {
	t.Parallel()

	const src = "const std = @import(\"std\");\n" +
		"const foo = @import(\"foo.zig\");\n" +
		"\n" +
		"pub fn main() void {}\n"
	rewrite := func(block string) *semanticapi.WorkspaceEdit {
		return &semanticapi.WorkspaceEdit{
			Changes: map[string][]semanticapi.TextEdit{
				"file:///ws/src/main.go": {
					insertAt(0, 0, block),
					replaceEdit(0, 0, 1, 0, ""),
					replaceEdit(1, 0, 2, 0, ""),
				},
			},
		}
	}

	tests := []struct {
		name      string
		edit      *semanticapi.WorkspaceEdit
		wantEdits int
	}{
		{
			name: "rewrite that reorders nothing is dropped",
			edit: rewrite("const std = @import(\"std\");\n" +
				"const foo = @import(\"foo.zig\");\n"),
			wantEdits: 0,
		},
		{
			name: "rewrite that reorders is applied",
			edit: rewrite("const foo = @import(\"foo.zig\");\n" +
				"const std = @import(\"std\");\n"),
			wantEdits: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uri := codeActionTestURI(t)
			handler := &mockHandler{uri: uri}
			view := &mockCellView{cells: cellsOf(src)}
			applied := 0
			editor := &mockEditor{
				editorFn: func(workspaceapi.URI) (textapi.Handler, error) {
					return handler, nil
				},
				cellViewFn: func(textapi.Handler) textapi.CellView { return view },
				cellEditorFn: func(textapi.Handler) textapi.CellEditor {
					return &mockCellEditor{editFn: func(
						context.Context, term.Coordinates, term.Coordinates, string,
					) (term.Coordinates, term.Coordinates, string, error) {
						applied++
						return term.Coordinates{}, term.Coordinates{}, "", nil
					}}
				},
			}
			lsp := &codeActionLSP{results: []semanticapi.CodeActionResult{{
				CodeAction: &semanticapi.CodeAction{
					Title: "Organize imports",
					Kind:  "source.organizeImports",
					Edit:  tt.edit,
				},
			}}}
			notify := &notifyRecorder{}
			floats := &floatRecorder{}
			h := CodeActionHandler(lsp, editor, notify.notifications(),
				floats.manager(), NewSelectionTracker(),
				"source.organizeImports", false, "hint")

			done := make(chan error, 1)
			go func() {
				done <- h.HandleCommand(
					context.Background(), codeActionCommand(uri, "organize"))
			}()

			picker := floats.picker(t)
			picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			require.NoError(t, <-done)
			assert.Equal(t, tt.wantEdits, applied)
		})
	}
}

func TestCodeActionLoneNoopActionNotifiesHint(t *testing.T) {
	t.Parallel()

	const src = "import { a } from \"./a\";\n"
	const file = "file:///ws/src/main.go"
	tests := []struct {
		name       string
		action     semanticapi.CodeAction
		wantEdits  int
		wantNotify []string
	}{
		{
			name: "rewrite to the same text",
			action: semanticapi.CodeAction{Edit: &semanticapi.WorkspaceEdit{
				Changes: map[string][]semanticapi.TextEdit{
					file: {replaceEdit(0, 0, 1, 0, src)},
				},
			}},
			wantNotify: []string{"already organized"},
		},
		{
			name: "edit with no changes",
			action: semanticapi.CodeAction{Edit: &semanticapi.WorkspaceEdit{
				Changes: map[string][]semanticapi.TextEdit{},
			}},
			wantNotify: []string{"already organized"},
		},
		{
			name: "edit that changes the text",
			action: semanticapi.CodeAction{Edit: &semanticapi.WorkspaceEdit{
				Changes: map[string][]semanticapi.TextEdit{
					file: {replaceEdit(0, 0, 1, 0, "")},
				},
			}},
			wantEdits: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			uri := codeActionTestURI(t)
			handler := &mockHandler{uri: uri}
			view := &mockCellView{cells: cellsOf(src)}
			applied := 0
			editor := &mockEditor{
				editorFn: func(workspaceapi.URI) (textapi.Handler, error) {
					return handler, nil
				},
				cellViewFn: func(textapi.Handler) textapi.CellView { return view },
				cellEditorFn: func(textapi.Handler) textapi.CellEditor {
					return &mockCellEditor{editFn: func(
						context.Context, term.Coordinates, term.Coordinates, string,
					) (term.Coordinates, term.Coordinates, string, error) {
						applied++
						return term.Coordinates{}, term.Coordinates{}, "", nil
					}}
				},
			}
			action := tt.action
			action.Title, action.Kind = "Organize imports", "source.organizeImports"
			lsp := &codeActionLSP{results: []semanticapi.CodeActionResult{{CodeAction: &action}}}
			notify := &notifyRecorder{}
			floats := &floatRecorder{}
			h := CodeActionHandler(lsp, editor, notify.notifications(),
				floats.manager(), NewSelectionTracker(),
				"source.organizeImports", true, "already organized")

			require.NoError(t, h.HandleCommand(t.Context(), codeActionCommand(uri, "organize")))
			assert.Nil(t, floats.floated(), "no picker may be shown")
			assert.Equal(t, tt.wantEdits, applied)
			assert.Equal(t, tt.wantNotify, notify.messages())
		})
	}
}
