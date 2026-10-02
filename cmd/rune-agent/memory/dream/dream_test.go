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

package dream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/internal/ide/vctrl/testgit"
)

func TestDream(t *testing.T) {
	t.Run("empty store produces done", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{}

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		progress := collectProgress(t, it)
		require.NotEmpty(t, progress)
		assert.True(t, hasProgressType(progress, ProgressDone))
		assert.NoError(t, it.Err())
	})

	t.Run("single dialogue is processed", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{
			dialogues: []dialoguemanager.Dialogue{
				{
					ID:      "d1",
					Version: 1,
					Messages: []llmapi.Message{
						{Role: llmapi.RoleUser, Content: "How do I write tests?"},
						{Role: llmapi.RoleAssistant, Content: "Use table-driven tests."},
					},
				},
			},
		}
		deps.LLM = &mockLLMService{responses: []mockLLMResponse{
			stopLLMResponse("I've analyzed the conversation and written memories."),
		}}

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		progress := collectProgress(t, it)
		assert.NoError(t, it.Err())
		assert.True(t, hasProgressType(progress, ProgressAnalyzing))
		assert.True(t, hasProgressType(progress, ProgressDone))

		// Verify progress tracking.
		for _, p := range progress {
			if p.Type == ProgressAnalyzing {
				assert.Equal(t, 0, p.Progress)
				assert.Equal(t, "conversations", p.Units)
			}
		}

		// Verify state was saved.
		var state DreamState
		require.NoError(t, deps.Storage.Get(context.Background(), dreamStateID, &state))
		assert.Equal(t, int64(1), state.Dreamed["d1"])
	})

	t.Run("already dreamed dialogue is skipped", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{
			dialogues: []dialoguemanager.Dialogue{
				{ID: "d1", Version: 1, Messages: []llmapi.Message{
					{Role: llmapi.RoleUser, Content: "hi"},
				}},
			},
		}
		// Pre-populate dream state.
		storage := deps.Storage.(*mockStorage)
		storage.data[dreamStateID] = &DreamState{
			SchemaVersion: templateVersion,
			Dreamed:       map[string]int64{"d1": 1},
		}

		svc := &mockLLMService{}
		deps.LLM = svc

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		progress := collectProgress(t, it)
		assert.NoError(t, it.Err())
		assert.True(t, hasProgressType(progress, ProgressDone))
		assert.False(t, hasProgressType(progress, ProgressAnalyzing))
		assert.Equal(t, 0, svc.getCallCount())
	})

	t.Run("updated dialogue is re-dreamed", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{
			dialogues: []dialoguemanager.Dialogue{
				{ID: "d1", Version: 3, Messages: []llmapi.Message{
					{Role: llmapi.RoleUser, Content: "updated content"},
				}},
			},
		}
		// Version 1 was dreamed, but dialogue is now at version 3.
		storage := deps.Storage.(*mockStorage)
		storage.data[dreamStateID] = &DreamState{
			SchemaVersion: templateVersion,
			Dreamed:       map[string]int64{"d1": 1},
		}

		deps.LLM = &mockLLMService{responses: []mockLLMResponse{
			stopLLMResponse("done"),
		}}

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		progress := collectProgress(t, it)
		assert.NoError(t, it.Err())
		assert.True(t, hasProgressType(progress, ProgressAnalyzing))

		// State should now reflect version 3.
		var state DreamState
		require.NoError(t, deps.Storage.Get(context.Background(), dreamStateID, &state))
		assert.Equal(t, int64(3), state.Dreamed["d1"])
	})

	t.Run("agent error skips dialogue continues", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{
			dialogues: []dialoguemanager.Dialogue{
				{ID: "d1", Version: 1, Messages: []llmapi.Message{
					{Role: llmapi.RoleUser, Content: "first"},
				}},
				{ID: "d2", Version: 1, Messages: []llmapi.Message{
					{Role: llmapi.RoleUser, Content: "second"},
				}},
			},
		}
		// First call fails, second succeeds.
		deps.LLM = &mockLLMService{responses: []mockLLMResponse{
			{err: errors.New("api error")},
			stopLLMResponse("done"),
		}}

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		progress := collectProgress(t, it)
		assert.NoError(t, it.Err())
		assert.True(t, hasProgressType(progress, ProgressError))
		assert.True(t, hasProgressType(progress, ProgressDone))

		// Verify the error event carries the dialogue ID and message.
		for _, p := range progress {
			if p.Type == ProgressError {
				assert.Equal(t, "d1", p.DialogueID)
				assert.Contains(t, p.Message, "api error")
			}
		}

		// Only d2 should be marked as dreamed.
		var state DreamState
		require.NoError(t, deps.Storage.Get(context.Background(), dreamStateID, &state))
		assert.NotContains(t, state.Dreamed, "d1")
		assert.Equal(t, int64(1), state.Dreamed["d2"])
	})

	t.Run("context cancellation stops processing", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{
			dialogues: []dialoguemanager.Dialogue{
				{ID: "d1", Version: 1, Messages: []llmapi.Message{
					{Role: llmapi.RoleUser, Content: "hi"},
				}},
			},
		}
		// LLM blocks until cancelled.
		deps.LLM = &mockLLMService{responses: []mockLLMResponse{
			{err: context.Canceled},
		}}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		it, err := Dream(ctx, deps)
		require.NoError(t, err)

		_ = collectProgress(t, it)
		assert.Error(t, it.Err())
	})

	t.Run("cancellation stops in-flight producer", func(t *testing.T) {
		dir := t.TempDir()
		// Pre-create .git so ensureGitRepo short-circuits and the
		// producer reaches the LLM call quickly.
		require.NoError(t, os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{
			dialogues: []dialoguemanager.Dialogue{
				{ID: "d1", Version: 1, Messages: []llmapi.Message{
					{Role: llmapi.RoleUser, Content: "hi"},
				}},
			},
		}
		// LLM blocks until either ctx is cancelled or the test releases it.
		release := make(chan struct{})
		defer close(release)
		deps.LLM = &mockLLMService{responses: []mockLLMResponse{
			{block: release},
		}}

		it, err := Dream(t.Context(), deps)
		require.NoError(t, err)

		// Drain a couple of bootstrap/phase events so the producer is
		// past startup and waiting on the LLM call.
		drainCtx, drainCancel := context.WithTimeout(t.Context(), 2*time.Second)
		got := 0
		for got < 2 {
			_, ok := it.Next(drainCtx)
			if !ok {
				break
			}
			got++
		}
		drainCancel()
		require.Greater(t, got, 0, "no progress events received before cancel")

		// Cancelling Close must stop the producer goroutine within the
		// deadline. Before the fix, Close is a no-op so the goroutine
		// keeps running until the (blocking) LLM call returns.
		closeDone := make(chan error, 1)
		go func() { closeDone <- it.Close() }()

		select {
		case <-closeDone:
		case <-time.After(2 * time.Second):
			t.Fatal("it.Close() did not return within 2s after cancel; " +
				"producer goroutine is still running")
		}

		// After Close returns, the iterator must report an error
		// terminating in context.Canceled.
		assert.ErrorIs(t, it.Err(), context.Canceled)
	})

	t.Run("schema upgrade triggers re-processing", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)

		d1 := dialoguemanager.Dialogue{
			ID:      "d1",
			Version: 1,
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "old convo"},
				{Role: llmapi.RoleAssistant, Content: "old response"},
			},
		}
		deps.Store = &mockDialogueStore{
			dialogues: []dialoguemanager.Dialogue{d1},
		}

		storage := deps.Storage.(*mockStorage)
		storage.data[dreamStateID] = &DreamState{
			SchemaVersion: 1,
			Dreamed:       map[string]int64{"d1": 1},
		}

		svc := &mockLLMService{responses: []mockLLMResponse{
			stopLLMResponse("re-processed memories"),
		}}
		deps.LLM = svc

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		_ = collectProgress(t, it)
		assert.NoError(t, it.Err())
		assert.Greater(t, svc.getCallCount(), 0)

		var state DreamState
		require.NoError(t, deps.Storage.Get(context.Background(), dreamStateID, &state))
		assert.Equal(t, templateVersion, state.SchemaVersion)
	})

	t.Run("re-processing skips missing dialogues", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{} // Get returns ErrNotFound

		storage := deps.Storage.(*mockStorage)
		storage.data[dreamStateID] = &DreamState{
			SchemaVersion: 1,
			Dreamed:       map[string]int64{"d1": 1},
		}

		svc := &mockLLMService{}
		deps.LLM = svc

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		_ = collectProgress(t, it)
		assert.NoError(t, it.Err())
		assert.Equal(t, 0, svc.getCallCount())

		var state DreamState
		require.NoError(t, deps.Storage.Get(context.Background(), dreamStateID, &state))
		assert.Equal(t, templateVersion, state.SchemaVersion)
	})

	t.Run("no re-processing when schema current", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{}

		storage := deps.Storage.(*mockStorage)
		storage.data[dreamStateID] = &DreamState{
			SchemaVersion: templateVersion,
			Dreamed:       map[string]int64{"d1": 1},
		}

		svc := &mockLLMService{}
		deps.LLM = svc

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		_ = collectProgress(t, it)
		assert.NoError(t, it.Err())
		assert.Equal(t, 0, svc.getCallCount())
	})

	t.Run("re-processing emits ProgressReprocessing", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)

		d1 := dialoguemanager.Dialogue{
			ID:      "d1",
			Version: 1,
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "old convo"},
			},
		}
		deps.Store = &mockDialogueStore{
			dialogues: []dialoguemanager.Dialogue{d1},
		}

		storage := deps.Storage.(*mockStorage)
		storage.data[dreamStateID] = &DreamState{
			SchemaVersion: 1,
			Dreamed:       map[string]int64{"d1": 1},
		}

		deps.LLM = &mockLLMService{responses: []mockLLMResponse{
			stopLLMResponse("re-processed"),
		}}

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		progress := collectProgress(t, it)
		assert.NoError(t, it.Err())
		assert.True(t, hasProgressType(progress, ProgressReprocessing))
	})

	t.Run("re-processing integration with real workspace", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping integration test in short mode")
		}

		dir := t.TempDir()
		fsys := newOSFileSystem()

		// Bootstrap real workspace and tidy deps.
		_, err := bootstrap(fsys, dir)
		require.NoError(t, err)
		assertGoCommand(t, dir, "mod", "tidy")

		// Write a real memory Go file.
		require.NoError(t, os.WriteFile(filepath.Join(dir, "mem_table_tests.go"), []byte(
			"package main\n\n"+
				"import \"context\"\n\n"+
				"type tableTestPatterns struct{}\n\n"+
				"func init() { Register(tableTestPatterns{}) }\n\n"+
				"func (tableTestPatterns) ID() string      { return \"table-test-patterns\" }\n"+
				"func (tableTestPatterns) Content() string { return \"Use table-driven tests.\" }\n"+
				"func (tableTestPatterns) FetchConversation(_ context.Context) (Dialogue, error) {\n"+
				"\treturn Dialogue{}, nil\n"+
				"}\n"+
				"func (tableTestPatterns) testingPattern() {}\n",
		), 0o644))

		// Verify workspace compiles with the memory file.
		assertGoCommand(t, dir, "test", "./...")

		// Set up deps: real FS + Exec, mock LLM + storage.
		storage := &mockStorage{data: make(map[string]any)}
		storage.data[dreamStateID] = &DreamState{
			SchemaVersion: 1,
			Dreamed:       map[string]int64{"d1": 1},
		}

		d1 := dialoguemanager.Dialogue{
			ID:      "d1",
			Version: 1,
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "How should I write tests?"},
				{Role: llmapi.RoleAssistant, Content: "Use table-driven tests."},
			},
		}
		store := &mockDialogueStore{
			dialogues: []dialoguemanager.Dialogue{d1},
		}

		svc := &mockLLMService{responses: []mockLLMResponse{
			stopLLMResponse("I've reviewed the memories and they look good."),
		}}

		deps := Deps{
			LLM:      svc,
			Store:    store,
			Storage:  storage,
			FS:       fsys,
			Exec:     osExec{},
			LSP:      &stubDreamLSP{},
			DataPath: dir,
		}

		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		it, err := Dream(ctx, deps)
		require.NoError(t, err)

		var progress []Progress
		for {
			p, ok := it.Next(ctx)
			if !ok {
				break
			}
			progress = append(progress, p)
		}
		require.NoError(t, it.Err())

		// Re-processing should have been triggered.
		assert.True(t, hasProgressType(progress, ProgressReprocessing))
		assert.Greater(t, svc.getCallCount(), 0)

		// SchemaVersion should be updated.
		var state DreamState
		require.NoError(t, storage.Get(context.Background(), dreamStateID, &state))
		assert.Equal(t, templateVersion, state.SchemaVersion)

		// Workspace should still compile after re-processing.
		assertGoCommand(t, dir, "test", "./...")
	})

	t.Run("bootstrap failure stops iterator", func(t *testing.T) {
		deps := validDeps(t, t.TempDir())
		deps.FS = &failingFS{statErr: errors.New("permission denied")}

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		_ = collectProgress(t, it)
		assert.Error(t, it.Err())
		assert.Contains(t, it.Err().Error(), "bootstrap")
	})

}

func TestValidateDeps(t *testing.T) {
	tests := []struct {
		name    string
		modify  func(*Deps)
		wantErr string
	}{
		{"missing LLM", func(d *Deps) { d.LLM = nil }, "LLM"},
		{"missing Store", func(d *Deps) { d.Store = nil }, "Store"},
		{"missing Storage", func(d *Deps) { d.Storage = nil }, "Storage"},
		{"missing FS", func(d *Deps) { d.FS = nil }, "FS"},
		{"missing Exec", func(d *Deps) { d.Exec = nil }, "Exec"},
		{"missing LSP", func(d *Deps) { d.LSP = nil }, "LSP"},
		{"missing DataPath", func(d *Deps) { d.DataPath = "" }, "DataPath"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := validDeps(t, t.TempDir())
			tt.modify(&deps)
			assert.PanicsWithValue(t, fmt.Sprintf("dream: %s is required", tt.wantErr), func() {
				_, _ = Dream(context.Background(), deps)
			})
		})
	}
}

func TestFormatTranscript(t *testing.T) {
	t.Run("formats user and assistant messages", func(t *testing.T) {
		d := dialoguemanager.Dialogue{
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "hello"},
				{Role: llmapi.RoleAssistant, Content: "world"},
			},
		}
		result := formatTranscript(d)
		assert.Contains(t, result, "### user")
		assert.Contains(t, result, "hello")
		assert.Contains(t, result, "### assistant")
		assert.Contains(t, result, "world")
	})

	t.Run("skips system messages", func(t *testing.T) {
		d := dialoguemanager.Dialogue{
			Messages: []llmapi.Message{
				{Role: llmapi.RoleSystem, Content: "secret"},
				{Role: llmapi.RoleUser, Content: "hello"},
			},
		}
		result := formatTranscript(d)
		assert.NotContains(t, result, "secret")
		assert.Contains(t, result, "hello")
	})

	t.Run("empty dialogue produces empty string", func(t *testing.T) {
		d := dialoguemanager.Dialogue{}
		assert.Empty(t, formatTranscript(d))
	})
}

func TestRunPhases(t *testing.T) {
	t.Run("skips when already run", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{}

		storage := deps.Storage.(*mockStorage)
		storage.data[dreamStateID] = &DreamState{
			SchemaVersion: templateVersion,
			Dreamed:       map[string]int64{},
			LastExtract:   5,
			PhasesRun:     map[string]int64{"custom": 5},
		}

		origPhases := append([]Phase(nil), phases...)
		defer func() { phases = origPhases }()

		called := false
		phases = append(phases, Phase{
			Name:        "custom",
			Description: "Custom test phase",
			Run: func(_ context.Context, _ chan<- Progress, _ Deps,
				_ *DreamState, _ *dreamStateStore) error {
				called = true
				return nil
			},
		})

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		_ = collectProgress(t, it)
		assert.NoError(t, it.Err())
		assert.False(t, called, "phase should be skipped when PhasesRun >= LastExtract")
	})

	t.Run("runs after new extract", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{
			dialogues: []dialoguemanager.Dialogue{
				{ID: "d1", Version: 1, Messages: []llmapi.Message{
					{Role: llmapi.RoleUser, Content: "hello"},
				}},
			},
		}
		deps.LLM = &mockLLMService{responses: []mockLLMResponse{
			stopLLMResponse("done"), // for extract phase
		}}

		origPhases := append([]Phase(nil), phases...)
		defer func() { phases = origPhases }()

		called := false
		phases = append(phases, Phase{
			Name:        "custom",
			Description: "Custom test phase",
			Run: func(_ context.Context, _ chan<- Progress, _ Deps,
				state *DreamState, stateStore *dreamStateStore) error {
				called = true
				return nil
			},
		})

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		_ = collectProgress(t, it)
		assert.NoError(t, it.Err())
		assert.True(t, called, "phase should run after extract processes a dialogue")

		// Verify PhasesRun was updated.
		var state DreamState
		require.NoError(t, deps.Storage.Get(context.Background(), dreamStateID, &state))
		assert.Equal(t, state.LastExtract, state.PhasesRun["custom"])
	})

	t.Run("skips when user prompt empty", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{}

		storage := deps.Storage.(*mockStorage)
		storage.data[dreamStateID] = &DreamState{
			SchemaVersion: templateVersion,
			Dreamed:       map[string]int64{},
			LastExtract:   5,
			PhasesRun:     map[string]int64{},
		}

		svc := &mockLLMService{}
		deps.LLM = svc

		origPhases := append([]Phase(nil), phases...)
		defer func() { phases = origPhases }()

		phases = append(phases, NewAgentPhase(
			"empty-prompt", "Phase with empty prompt", "system prompt",
			func(_ context.Context, _ Deps) (string, error) {
				return "", nil
			},
		))

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		_ = collectProgress(t, it)
		assert.NoError(t, it.Err())
		assert.Equal(t, 0, svc.getCallCount(), "no LLM call when user prompt is empty")
	})

	t.Run("phase verify failure retries", func(t *testing.T) {
		dir := t.TempDir()
		mockEx := &mockExec{failVerify: 1}
		deps := validDeps(t, dir)
		deps.Exec = mockEx
		deps.MaxFixAttempts = 1
		deps.Store = &mockDialogueStore{}

		storage := deps.Storage.(*mockStorage)
		storage.data[dreamStateID] = &DreamState{
			SchemaVersion: templateVersion,
			Dreamed:       map[string]int64{},
			LastExtract:   5,
			PhasesRun:     map[string]int64{},
		}

		svc := &mockLLMService{responses: []mockLLMResponse{
			stopLLMResponse("initial analysis"),
			stopLLMResponse("fixed the code"),
		}}
		deps.LLM = svc

		origPhases := append([]Phase(nil), phases...)
		defer func() { phases = origPhases }()

		phases = append(phases, NewAgentPhase(
			"verify-retry", "Phase that retries on verify failure",
			"fix system prompt",
			func(_ context.Context, _ Deps) (string, error) {
				return "run quality check", nil
			},
		))

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		_ = collectProgress(t, it)
		assert.NoError(t, it.Err())
		assert.Equal(t, 2, svc.getCallCount(), "initial + fix = 2 LLM calls")
	})

	t.Run("emits progress events", func(t *testing.T) {
		dir := t.TempDir()
		deps := validDeps(t, dir)
		deps.Store = &mockDialogueStore{
			dialogues: []dialoguemanager.Dialogue{
				{ID: "d1", Version: 1, Messages: []llmapi.Message{
					{Role: llmapi.RoleUser, Content: "hi"},
				}},
			},
		}
		deps.LLM = &mockLLMService{responses: []mockLLMResponse{
			stopLLMResponse("done"),
		}}

		origPhases := append([]Phase(nil), phases...)
		defer func() { phases = origPhases }()

		phases = append(phases, Phase{
			Name:        "custom",
			Description: "Custom test phase",
			Run: func(_ context.Context, _ chan<- Progress, _ Deps,
				_ *DreamState, _ *dreamStateStore) error {
				return nil
			},
		})

		it, err := Dream(context.Background(), deps)
		require.NoError(t, err)

		progress := collectProgress(t, it)
		assert.NoError(t, it.Err())

		// Both extract and custom phases should emit start/finish.
		var customStart, customFinish bool
		for _, p := range progress {
			if p.Type == ProgressPhaseStart && strings.Contains(p.Message, "Custom test phase") {
				customStart = true
			}
			if p.Type == ProgressPhaseFinish && strings.Contains(p.Message, "Custom test phase") {
				customFinish = true
			}
		}
		assert.True(t, customStart, "expected ProgressPhaseStart for custom phase")
		assert.True(t, customFinish, "expected ProgressPhaseFinish for custom phase")
	})
}

func TestDreamPipelineGitIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	dir := t.TempDir()
	fsys := newOSFileSystem()

	// Bootstrap real workspace and tidy deps.
	_, err := bootstrap(fsys, dir)
	require.NoError(t, err)
	assertGoCommand(t, dir, "mod", "tidy")

	d1 := dialoguemanager.Dialogue{
		ID:      "d1",
		Version: 1,
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "How should I write tests?"},
			{Role: llmapi.RoleAssistant, Content: "Use table-driven tests."},
		},
	}

	svc := &mockLLMService{responses: []mockLLMResponse{
		stopLLMResponse("I've analyzed the conversation and written memories."),
	}}

	deps := Deps{
		LLM:      svc,
		Store:    &mockDialogueStore{dialogues: []dialoguemanager.Dialogue{d1}},
		Storage:  &mockStorage{data: make(map[string]any)},
		FS:       fsys,
		Exec:     osExec{},
		LSP:      &stubDreamLSP{},
		DataPath: dir,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	it, err := Dream(ctx, deps)
	require.NoError(t, err)

	var progress []Progress
	for {
		p, ok := it.Next(ctx)
		if !ok {
			break
		}
		progress = append(progress, p)
	}
	require.NoError(t, it.Err())

	// Pipeline should have completed successfully.
	assert.True(t, hasProgressType(progress, ProgressPhaseStart))
	assert.True(t, hasProgressType(progress, ProgressPhaseFinish))
	assert.True(t, hasProgressType(progress, ProgressDone))

	// Memory workspace should be a git repository.
	_, statErr := os.Stat(filepath.Join(dir, ".git"))
	require.NoError(t, statErr, "memory workspace should be a git repo")

	// Verify the expected commits exist. The bootstrap commit includes all
	// workspace files. The "dream: update memories" commit only appears if
	// the agent actually writes files (here the mock LLM returns text only,
	// so there is nothing new to commit).
	out := testgit.Run(t, dir, "log", "--format=%s", "--reverse")
	commits := strings.Split(strings.TrimSpace(string(out)), "\n")

	require.GreaterOrEqual(t, len(commits), 1, "at least the bootstrap commit")
	assert.Equal(t, "initialize memory module", commits[0])

	// State should reflect the processed dialogue.
	var state DreamState
	require.NoError(t, deps.Storage.Get(ctx, dreamStateID, &state))
	assert.Equal(t, int64(1), state.Dreamed["d1"])
	assert.Equal(t, int64(1), state.LastExtract)
}

// TestEnsureGitRepoIgnoresHookGitEnv guards runGitCmd against the
// repo-location overrides git exports to hook subprocesses: with an
// inherited GIT_DIR, `git init` re-initializes the hook's repository
// (it once flipped a developer repo to bare) and the follow-up
// add/commit land there instead of in the memory workspace.
func TestEnsureGitRepoIgnoresHookGitEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	hookRepo := t.TempDir()
	testgit.Run(t, hookRepo, "init", "-q")
	testgit.Run(t, hookRepo, "commit", "--allow-empty", "-m", "victim", "-q")

	dataPath := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataPath, "memory.go"),
		[]byte("package memory\n"), 0o600))

	t.Setenv("GIT_DIR", filepath.Join(hookRepo, ".git"))
	t.Setenv("GIT_WORK_TREE", hookRepo)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(hookRepo, ".git", "index"))

	err := ensureGitRepo(context.Background(), newOSFileSystem(), osExec{}, dataPath)
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(dataPath, ".git"))
	require.NoError(t, statErr,
		"repo must be created at dataPath, not at the hook's GIT_DIR")

	bare := strings.TrimSpace(string(testgit.Run(t, hookRepo,
		"rev-parse", "--is-bare-repository")))
	assert.Equal(t, "false", bare,
		"hook repo must not be re-initialized")
	log := strings.TrimSpace(string(testgit.Run(t, hookRepo,
		"log", "--format=%s")))
	assert.Equal(t, "victim", log,
		"hook repo must not receive the memory-module commit")
}

// TestEnsureGitRepoIgnoresUserCommitSigning guards the memory module's
// bootstrap commit against the user's commit.gpgsign: signing an
// automated commit needs a key and may block on a pinentry prompt that
// no one is there to answer.
func TestEnsureGitRepoIgnoresUserCommitSigning(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}

	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(t, os.WriteFile(globalConfig, []byte(
		"[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = /nonexistent-signer\n",
	), 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)

	dataPath := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dataPath, "memory.go"),
		[]byte("package memory\n"), 0o600))

	require.NoError(t, ensureGitRepo(
		context.Background(), newOSFileSystem(), osExec{}, dataPath))

	log := strings.TrimSpace(string(testgit.Run(t, dataPath, "log", "--format=%s")))
	assert.Equal(t, "initialize memory module", log)
}

// --- Test helpers ---

func validDeps(t *testing.T, dir string) Deps {
	t.Helper()
	return Deps{
		LLM:      &mockLLMService{},
		Store:    &mockDialogueStore{},
		Storage:  &mockStorage{data: make(map[string]any)},
		FS:       newOSFileSystem(),
		Exec:     &mockExec{},
		LSP:      &stubDreamLSP{},
		DataPath: dir,
	}
}

type stubDreamLSP struct{}

func (s *stubDreamLSP) Initialize(context.Context, semanticapi.InitializeParams) (semanticapi.InitializeResult, error) {
	return semanticapi.InitializeResult{}, nil
}
func (s *stubDreamLSP) Initialized(context.Context) error { return nil }
func (s *stubDreamLSP) Shutdown(context.Context) error    { return nil }
func (s *stubDreamLSP) Exit(context.Context) error        { return nil }
func (s *stubDreamLSP) DidOpen(context.Context, semanticapi.DidOpenTextDocumentParams) error {
	return nil
}
func (s *stubDreamLSP) DidChange(context.Context, semanticapi.DidChangeTextDocumentParams) error {
	return nil
}
func (s *stubDreamLSP) DidClose(context.Context, semanticapi.DidCloseTextDocumentParams) error {
	return nil
}
func (s *stubDreamLSP) DidSave(context.Context, semanticapi.DidSaveTextDocumentParams) error {
	return nil
}
func (s *stubDreamLSP) Completion(context.Context, semanticapi.CompletionParams) (semanticapi.CompletionResult, error) {
	return semanticapi.CompletionResult{}, nil
}
func (s *stubDreamLSP) Hover(context.Context, semanticapi.HoverParams) (*semanticapi.Hover, error) {
	return nil, nil
}
func (s *stubDreamLSP) SignatureHelp(context.Context, semanticapi.SignatureHelpParams) (*semanticapi.SignatureHelp, error) {
	return nil, nil
}
func (s *stubDreamLSP) Definition(context.Context, semanticapi.DefinitionParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (s *stubDreamLSP) Declaration(context.Context, semanticapi.DeclarationParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (s *stubDreamLSP) TypeDefinition(context.Context, semanticapi.TypeDefinitionParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (s *stubDreamLSP) Implementation(context.Context, semanticapi.ImplementationParams) (semanticapi.LocationResult, error) {
	return semanticapi.LocationResult{}, nil
}
func (s *stubDreamLSP) References(context.Context, semanticapi.ReferenceParams) ([]semanticapi.Location, error) {
	return nil, nil
}
func (s *stubDreamLSP) DocumentHighlight(context.Context, semanticapi.DocumentHighlightParams) ([]semanticapi.DocumentHighlight, error) {
	return nil, nil
}
func (s *stubDreamLSP) DocumentSymbol(context.Context, semanticapi.DocumentSymbolParams) (semanticapi.DocumentSymbolResult, error) {
	return semanticapi.DocumentSymbolResult{}, nil
}
func (s *stubDreamLSP) CodeAction(context.Context, semanticapi.CodeActionParams) ([]semanticapi.CodeActionResult, error) {
	return nil, nil
}
func (s *stubDreamLSP) CodeLens(context.Context, semanticapi.CodeLensParams) ([]semanticapi.CodeLens, error) {
	return nil, nil
}
func (s *stubDreamLSP) Formatting(context.Context, semanticapi.DocumentFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (s *stubDreamLSP) RangeFormatting(context.Context, semanticapi.DocumentRangeFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (s *stubDreamLSP) Rename(context.Context, semanticapi.RenameParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (s *stubDreamLSP) PrepareRename(context.Context, semanticapi.PrepareRenameParams) (*semanticapi.PrepareRenameResult, error) {
	return nil, nil
}
func (s *stubDreamLSP) FoldingRange(context.Context, semanticapi.FoldingRangeParams) ([]semanticapi.FoldingRange, error) {
	return nil, nil
}
func (s *stubDreamLSP) SelectionRange(context.Context, semanticapi.SelectionRangeParams) ([]semanticapi.SelectionRange, error) {
	return nil, nil
}
func (s *stubDreamLSP) SemanticTokensFull(context.Context, semanticapi.SemanticTokensParams) (*semanticapi.SemanticTokens, error) {
	return nil, nil
}
func (s *stubDreamLSP) SemanticTokensRange(context.Context, semanticapi.SemanticTokensRangeParams) (*semanticapi.SemanticTokens, error) {
	return nil, nil
}
func (s *stubDreamLSP) Diagnostic(context.Context, semanticapi.DocumentDiagnosticParams) (semanticapi.DocumentDiagnosticReport, error) {
	return semanticapi.DocumentDiagnosticReport{}, nil
}
func (s *stubDreamLSP) WorkspaceDiagnostic(context.Context, semanticapi.WorkspaceDiagnosticParams) (semanticapi.WorkspaceDiagnosticReport, error) {
	return semanticapi.WorkspaceDiagnosticReport{}, nil
}
func (s *stubDreamLSP) WorkspaceSymbol(context.Context, semanticapi.WorkspaceSymbolParams) ([]semanticapi.SymbolInformation, error) {
	return nil, nil
}
func (s *stubDreamLSP) ExecuteCommand(context.Context, semanticapi.ExecuteCommandParams) (string, error) {
	return "", nil
}
func (s *stubDreamLSP) ExecuteRequest(context.Context, semanticapi.ExecuteRequestParams) (json.RawMessage, error) {
	return json.RawMessage("null"), nil
}
func (s *stubDreamLSP) SendNotification(context.Context, semanticapi.NotificationParams) error {
	return nil
}
func (s *stubDreamLSP) PrepareCallHierarchy(context.Context, semanticapi.CallHierarchyPrepareParams) ([]semanticapi.CallHierarchyItem, error) {
	return nil, nil
}
func (s *stubDreamLSP) CallHierarchyIncomingCalls(context.Context, semanticapi.CallHierarchyIncomingCallsParams) ([]semanticapi.CallHierarchyIncomingCall, error) {
	return nil, nil
}
func (s *stubDreamLSP) CallHierarchyOutgoingCalls(context.Context, semanticapi.CallHierarchyOutgoingCallsParams) ([]semanticapi.CallHierarchyOutgoingCall, error) {
	return nil, nil
}
func (s *stubDreamLSP) CompletionResolve(context.Context, semanticapi.CompletionItem) (semanticapi.CompletionItem, error) {
	return semanticapi.CompletionItem{}, nil
}
func (s *stubDreamLSP) CodeLensResolve(context.Context, semanticapi.CodeLens) (semanticapi.CodeLens, error) {
	return semanticapi.CodeLens{}, nil
}
func (s *stubDreamLSP) DocumentColor(context.Context, semanticapi.DocumentColorParams) ([]semanticapi.ColorInformation, error) {
	return nil, nil
}
func (s *stubDreamLSP) ColorPresentation(context.Context, semanticapi.ColorPresentationParams) ([]semanticapi.ColorPresentation, error) {
	return nil, nil
}
func (s *stubDreamLSP) DocumentLink(context.Context, semanticapi.DocumentLinkParams) ([]semanticapi.DocumentLink, error) {
	return nil, nil
}
func (s *stubDreamLSP) DocumentLinkResolve(context.Context, semanticapi.DocumentLink) (semanticapi.DocumentLink, error) {
	return semanticapi.DocumentLink{}, nil
}
func (s *stubDreamLSP) OnTypeFormatting(context.Context, semanticapi.DocumentOnTypeFormattingParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (s *stubDreamLSP) LinkedEditingRange(context.Context, semanticapi.LinkedEditingRangeParams) (*semanticapi.LinkedEditingRanges, error) {
	return nil, nil
}
func (s *stubDreamLSP) Moniker(context.Context, semanticapi.MonikerParams) ([]semanticapi.Moniker, error) {
	return nil, nil
}
func (s *stubDreamLSP) WillSaveWaitUntil(context.Context, semanticapi.WillSaveTextDocumentParams) ([]semanticapi.TextEdit, error) {
	return nil, nil
}
func (s *stubDreamLSP) SemanticTokensFullDelta(context.Context, semanticapi.SemanticTokensDeltaParams) (*semanticapi.SemanticTokensDelta, error) {
	return nil, nil
}
func (s *stubDreamLSP) PrepareTypeHierarchy(context.Context, semanticapi.TypeHierarchyPrepareParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (s *stubDreamLSP) TypeHierarchySupertypes(context.Context, semanticapi.TypeHierarchySupertypesParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (s *stubDreamLSP) TypeHierarchySubtypes(context.Context, semanticapi.TypeHierarchySubtypesParams) ([]semanticapi.TypeHierarchyItem, error) {
	return nil, nil
}
func (s *stubDreamLSP) InlayHint(context.Context, semanticapi.InlayHintParams) ([]semanticapi.InlayHint, error) {
	return nil, nil
}
func (s *stubDreamLSP) InlayHintResolve(context.Context, semanticapi.InlayHint) (semanticapi.InlayHint, error) {
	return semanticapi.InlayHint{}, nil
}
func (s *stubDreamLSP) InlineValue(context.Context, semanticapi.InlineValueParams) ([]semanticapi.InlineValue, error) {
	return nil, nil
}
func (s *stubDreamLSP) WillCreateFiles(context.Context, semanticapi.CreateFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (s *stubDreamLSP) WillRenameFiles(context.Context, semanticapi.RenameFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (s *stubDreamLSP) WillDeleteFiles(context.Context, semanticapi.DeleteFilesParams) (*semanticapi.WorkspaceEdit, error) {
	return nil, nil
}
func (s *stubDreamLSP) WillSave(context.Context, semanticapi.WillSaveTextDocumentParams) error {
	return nil
}
func (s *stubDreamLSP) DidChangeConfiguration(context.Context, semanticapi.DidChangeConfigurationParams) error {
	return nil
}
func (s *stubDreamLSP) DidChangeWatchedFiles(context.Context, semanticapi.DidChangeWatchedFilesParams) error {
	return nil
}
func (s *stubDreamLSP) DidChangeWorkspaceFolders(context.Context, semanticapi.DidChangeWorkspaceFoldersParams) error {
	return nil
}
func (s *stubDreamLSP) WorkDoneProgressCancel(context.Context, semanticapi.WorkDoneProgressCancelParams) error {
	return nil
}
func (s *stubDreamLSP) SetTrace(context.Context, semanticapi.SetTraceParams) error { return nil }
func (s *stubDreamLSP) DidCreateFiles(context.Context, semanticapi.CreateFilesParams) error {
	return nil
}
func (s *stubDreamLSP) DidRenameFiles(context.Context, semanticapi.RenameFilesParams) error {
	return nil
}
func (s *stubDreamLSP) DidDeleteFiles(context.Context, semanticapi.DeleteFilesParams) error {
	return nil
}

var _ semanticapi.LSP = (*stubDreamLSP)(nil)

func collectProgress(t *testing.T, it iterator.Iterator[Progress]) []Progress {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer func() { _ = it.Close() }()
	var progress []Progress
	for {
		p, ok := it.Next(ctx)
		if !ok {
			break
		}
		progress = append(progress, p)
	}
	return progress
}

func hasProgressType(progress []Progress, typ ProgressType) bool {
	for _, p := range progress {
		if p.Type == typ {
			return true
		}
	}
	return false
}

// --- Mocks ---

// mockLLMService implements llmapi.Service for testing.
type mockLLMService struct {
	mu        sync.Mutex
	callCount int
	responses []mockLLMResponse
	requests  []llmapi.Request
}

type mockLLMResponse struct {
	text         string
	finishReason llmapi.FinishReason
	err          error
	// block, if non-nil, causes CreateCompletion to block until the channel
	// is closed or ctx is done. Used by cancellation tests to keep the
	// producer goroutine in-flight.
	block <-chan struct{}
}

func (m *mockLLMService) CreateCompletion(
	ctx context.Context, _ llmapi.ModelEntry, req llmapi.Request,
) (iterator.Iterator[llmapi.Event], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	m.mu.Lock()
	idx := m.callCount
	m.callCount++
	m.requests = append(m.requests, req)
	m.mu.Unlock()

	if idx >= len(m.responses) {
		return nil, errors.New("no more mock responses")
	}

	resp := m.responses[idx]
	if resp.block != nil {
		select {
		case <-resp.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if resp.err != nil {
		return nil, resp.err
	}

	events := []llmapi.Event{
		{Type: llmapi.EventTextDelta, Text: resp.text},
		{Type: llmapi.EventStreamDone, DoneData: &llmapi.DoneData{
			Message: llmapi.Message{
				Role:    llmapi.RoleAssistant,
				Content: resp.text,
			},
			FinishReason: resp.finishReason,
		}},
	}
	return iterator.FromSlice(events), nil
}

func (m *mockLLMService) CountTokens(_ llmapi.ModelEntry, _ []llmapi.Message) (int, error) {
	return 0, nil
}

func (m *mockLLMService) Models() iterator.Iterator[llmapi.ModelEntry] {
	return iterator.FromSlice([]llmapi.ModelEntry{{Name: "test", ContextWindow: 100000}})
}

func (m *mockLLMService) GetModel(_ context.Context, model llmapi.ModelEntry) (llmapi.ModelEntry, error) {
	if model.Name == "" {
		model.Name = "test"
	}
	if model.ContextWindow == 0 {
		model.ContextWindow = 100000
	}
	return model, nil
}

func (m *mockLLMService) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

func stopLLMResponse(text string) mockLLMResponse {
	return mockLLMResponse{
		text:         text,
		finishReason: llmapi.FinishReasonStop,
	}
}

// mockDialogueStore implements dialoguemanager.Store for testing.
type mockDialogueStore struct {
	mu        sync.Mutex
	dialogues []dialoguemanager.Dialogue
	data      map[string]dialoguemanager.Dialogue
}

func (s *mockDialogueStore) Health(_ context.Context) error { return nil }

func (s *mockDialogueStore) Create(
	_ context.Context, d dialoguemanager.Dialogue,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string]dialoguemanager.Dialogue)
	}
	if _, ok := s.data[d.ID]; ok {
		return storageapi.ErrAlreadyExists
	}
	s.data[d.ID] = d
	return nil
}

func (s *mockDialogueStore) Get(
	_ context.Context, id string,
) (dialoguemanager.Dialogue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data != nil {
		if d, ok := s.data[id]; ok {
			return d, nil
		}
	}
	for _, d := range s.dialogues {
		if d.ID == id {
			return d, nil
		}
	}
	return dialoguemanager.Dialogue{}, storageapi.ErrNotFound
}

func (s *mockDialogueStore) Delete(
	_ context.Context, id string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data != nil {
		delete(s.data, id)
	}
	return nil
}

func (s *mockDialogueStore) AppendMessages(
	_ context.Context, d dialoguemanager.Dialogue,
	msgs []llmapi.Message, _ llmapi.DialogueUsage,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string]dialoguemanager.Dialogue)
	}
	existing := s.data[d.ID]
	existing.Messages = append(existing.Messages, msgs...)
	s.data[d.ID] = existing
	return nil
}

func (s *mockDialogueStore) SetTitle(_ context.Context, id, title string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		return storageapi.ErrNotFound
	}
	d, ok := s.data[id]
	if !ok {
		return storageapi.ErrNotFound
	}
	d.Title = title
	s.data[id] = d
	return nil
}

func (s *mockDialogueStore) List(
	_ context.Context,
) (iterator.Iterator[dialoguemanager.DialogueHeader], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	headers := make([]dialoguemanager.DialogueHeader, len(s.dialogues))
	for i, d := range s.dialogues {
		headers[i] = d.Header()
	}
	return iterator.FromSlice(headers), nil
}

func (s *mockDialogueStore) ArchiveAndReplace(_ context.Context, p dialoguemanager.ArchiveAndReplaceParams) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = make(map[string]dialoguemanager.Dialogue)
	}
	archived := p.Dialogue
	archived.ID = p.ArchivedDialogueID
	s.data[p.ArchivedDialogueID] = archived
	replaced := p.Dialogue
	replaced.Messages = p.Messages
	replaced.MessageCount = len(p.Messages)
	s.data[p.Dialogue.ID] = replaced
	return nil
}

// mockExec implements workspaceapi.Executor for testing.
// Set failVerify > 0 to make the first N `go test` invocations fail.
type mockExec struct {
	mu          sync.Mutex
	failVerify  int // number of verify (go test) calls to fail
	verifyCalls int
}

func (m *mockExec) Start(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	var procErr error
	if cmd.Path == "go" && len(cmd.Args) > 0 && cmd.Args[0] == "test" {
		m.mu.Lock()
		m.verifyCalls++
		if m.failVerify > 0 && m.verifyCalls <= m.failVerify {
			procErr = errors.New("test failure")
		}
		m.mu.Unlock()
	}
	if cmd.Watcher != nil {
		go func() {
			if procErr != nil {
				if w := cmd.Stderr; w != nil {
					_, _ = fmt.Fprint(w, "FAIL: test failure")
				}
			}
			cmd.Watcher.WatchProcess() <- procErr
		}()
	}
	return 0, nil
}

func (*mockExec) Signal(_ workspaceapi.Pid, _ syscall.Signal) error {
	return nil
}

func (*mockExec) Close() error { return nil }

// osExec implements workspaceapi.Executor using real processes.
type osExec struct{}

func (osExec) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Dir = cmd.Dir
	c.Env = cmd.Env
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr

	if err := c.Start(); err != nil {
		return 0, err
	}

	if cmd.Watcher != nil {
		go func() { cmd.Watcher.WatchProcess() <- c.Wait() }()
	}

	return workspaceapi.Pid(c.Process.Pid), nil
}

func (osExec) Signal(_ workspaceapi.Pid, _ syscall.Signal) error { return nil }
func (osExec) Close() error                                      { return nil }

// failingFS is a FileSystem that returns errors.
type failingFS struct {
	statErr error
}

func (f *failingFS) URI(_ string) (workspaceapi.URI, error) {
	return workspaceapi.URI{}, nil
}

func (f *failingFS) OpenFile(_ string, _ int, _ os.FileMode) (workspaceapi.File, error) {
	return nil, errors.New("failingFS: open not supported")
}

func (f *failingFS) Remove(_ string) error                   { return nil }
func (f *failingFS) Stat(_ string) (os.FileInfo, error)      { return nil, f.statErr }
func (f *failingFS) ReadDir(_ string) ([]os.DirEntry, error) { return nil, nil }
func (f *failingFS) MkdirAll(_ string, _ os.FileMode) error  { return nil }
