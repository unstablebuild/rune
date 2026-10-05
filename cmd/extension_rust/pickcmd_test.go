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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// newTestPickCmd wires a listPickCmd over the given entries against
// recording mocks.
func newTestPickCmd(entries []pickEntry) (
	*listPickCmd, *fakeWM, *mockEditor, *mockResourceOpener, *mockNotifications,
) {
	editor := newMockEditor()
	opener := newMockResourceOpener(editor)
	wm := &fakeWM{}
	notify := &mockNotifications{}
	cmd := &listPickCmd{
		pickDeps: pickDeps{
			editor: editor, wm: wm, opener: opener,
			notify: notify, fs: realFS{root: "/ws"},
		},
		produce: func(context.Context, textapi.Command) ([]pickEntry, error) {
			return entries, nil
		},
		emptyMsg: "nothing here",
	}
	return cmd, wm, editor, opener, notify
}

func pickEntryAt(name string, line uint32) pickEntry {
	return pickEntry{
		loc: semanticapi.Location{
			URI: "file:///ws/src/" + name,
			Range: semanticapi.Range{
				Start: semanticapi.Position{Line: line},
				End:   semanticapi.Position{Line: line, Character: 3},
			},
		},
		display: name,
	}
}

func TestPickerViewTickInterrupts(t *testing.T) {
	ir := &recordingInterrupter{}
	v := &pickerView{ch: make(chan int, 1), interrupt: ir}
	ran := false
	v.tick(func() { ran = true })
	require.True(t, ran, "the deferred work runs")
	assert.Equal(t, 1, ir.interrupts(), "the tick requests a redraw")
}

func TestListPickCmdEmptyNotifies(t *testing.T) {
	cmd, wm, _, opener, notify := newTestPickCmd(nil)
	require.NoError(t, cmd.HandleCommand(t.Context(), rustCmd("diagnostics", newTestURI(t), nil)))
	assert.True(t, notify.hasMessage("nothing here"), "an empty result is reported")
	assert.Empty(t, opener.openedURIs(), "nothing is opened")
	wm.mu.Lock()
	defer wm.mu.Unlock()
	assert.Nil(t, wm.floating, "no picker is floated")
}

func TestListPickCmdSingleEntryJumps(t *testing.T) {
	entry := pickEntryAt("main.rs", 4)
	cmd, wm, editor, opener, _ := newTestPickCmd([]pickEntry{entry})
	require.NoError(t, cmd.HandleCommand(t.Context(), rustCmd("diagnostics", newTestURI(t), nil)))

	opened := opener.openedURIs()
	require.Len(t, opened, 1, "the lone entry is opened without a picker")
	assert.Contains(t, opened[0], "src/main.rs")
	h, err := editor.Editor(parseTestURI(t, opened[0]))
	require.NoError(t, err)
	assert.Equal(t, 4, editor.lastCursor(h).Y, "the cursor lands on the entry's line")
	wm.mu.Lock()
	defer wm.mu.Unlock()
	assert.Nil(t, wm.floating, "no picker is floated for a single entry")
}

func TestListPickCmdSelectionJumps(t *testing.T) {
	entries := []pickEntry{
		pickEntryAt("main.rs", 1),
		pickEntryAt("lib.rs", 7),
	}
	cmd, wm, editor, opener, _ := newTestPickCmd(entries)

	done := make(chan error, 1)
	go func() {
		done <- cmd.HandleCommand(context.Background(), rustCmd("diagnostics", newTestURI(t), nil))
	}()

	view := awaitPickerView(t, wm)
	displays := make([]string, 0, len(view.Entries()))
	for _, e := range view.Entries() {
		displays = append(displays, e.Display)
	}
	assert.Equal(t, []string{"main.rs", "lib.rs"}, displays)

	view.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	view.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the jump")
	}

	opened := opener.openedURIs()
	require.Len(t, opened, 1)
	assert.Contains(t, opened[0], "src/lib.rs", "the second entry was selected")
	h, err := editor.Editor(parseTestURI(t, opened[0]))
	require.NoError(t, err)
	assert.Equal(t, 7, editor.lastCursor(h).Y, "the cursor lands on the entry's line")
}

func TestListPickCmdCancelUnblocks(t *testing.T) {
	cmd, wm, _, opener, _ := newTestPickCmd([]pickEntry{
		pickEntryAt("main.rs", 1),
		pickEntryAt("lib.rs", 7),
	})

	done := make(chan error, 1)
	go func() {
		done <- cmd.HandleCommand(context.Background(), rustCmd("diagnostics", newTestURI(t), nil))
	}()

	require.NoError(t, awaitPickerView(t, wm).Close())
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the cancelled command")
	}
	assert.Empty(t, opener.openedURIs(), "cancelling opens nothing")
}

func TestListPickCmdJumpTargetsInvokeWindow(t *testing.T) {
	entries := []pickEntry{
		pickEntryAt("main.rs", 1),
		pickEntryAt("lib.rs", 7),
	}
	cmd, wm, _, _, _ := newTestPickCmd(entries)

	done := make(chan error, 1)
	go func() {
		done <- cmd.HandleCommand(context.Background(), rustCmd("diagnostics", newTestURI(t), nil))
	}()

	view := awaitPickerView(t, wm)
	view.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the jump")
	}

	win, content := wm.lastContent()
	require.NotNil(t, content, "the jump sets window content")
	assert.Equal(t, uint64(editorWinID), win.WindowID(),
		"the jump must target the invoking editor window, not the floating picker")
}

func awaitPickerView(t *testing.T, wm *fakeWM) *pickerView {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		wm.mu.Lock()
		view, ok := wm.floating.(*pickerView)
		wm.mu.Unlock()
		if ok {
			return view
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the location picker")
	return nil
}

// requestLSP answers a single ExecuteRequest method with a canned
// result.
type requestLSP struct {
	noopLSP
	method string
	result json.RawMessage
}

func (l *requestLSP) Initialize(
	_ context.Context, _ semanticapi.InitializeParams,
) (semanticapi.InitializeResult, error) {
	return semanticapi.InitializeResult{}, nil
}

func (l *requestLSP) ExecuteRequest(
	_ context.Context, p semanticapi.ExecuteRequestParams,
) (json.RawMessage, error) {
	if p.Method != l.method {
		return nil, fmt.Errorf("unexpected method %s", p.Method)
	}
	return l.result, nil
}

func TestDependenciesPickResolvesCratePaths(t *testing.T) {
	tests := []struct {
		name    string
		crate   map[string]string
		fs      func(*fakeFS)
		wantURI string
	}{
		{
			name:  "url path resolves to the crate manifest",
			crate: map[string]string{"name": "serde", "version": "1.0.0", "path": "file:///dep/serde"},
			fs: func(f *fakeFS) {
				f.addDir("/dep/serde").addFile("/dep/serde/Cargo.toml")
			},
			wantURI: "file:///dep/serde/Cargo.toml",
		},
		{
			name:  "plain path resolves to the crate manifest",
			crate: map[string]string{"name": "libc", "path": "/dep/libc"},
			fs: func(f *fakeFS) {
				f.addDir("/dep/libc").addFile("/dep/libc/Cargo.toml")
			},
			wantURI: "file:///dep/libc/Cargo.toml",
		},
		{
			name:    "directory without a manifest is kept as is",
			crate:   map[string]string{"name": "core", "path": "file:///dep/core"},
			fs:      func(f *fakeFS) { f.addDir("/dep/core") },
			wantURI: "file:///dep/core",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := json.Marshal(map[string]any{"crates": []any{tt.crate}})
			require.NoError(t, err)
			lsp := &requestLSP{method: "rust-analyzer/fetchDependencyList", result: result}
			fs := newFakeFS()
			tt.fs(fs)

			entries, err := dependenciesPick(lsp, fs)(t.Context(), textapi.Command{})
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, tt.wantURI, entries[0].loc.URI)
			wantDisplay := tt.crate["name"]
			if v := tt.crate["version"]; v != "" {
				wantDisplay += " " + v
			}
			assert.Equal(t, wantDisplay, entries[0].display)
		})
	}
}
