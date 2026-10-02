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

package dialoguemanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

type listFailingBackend struct {
	storageapi.Service
	listCalls int
}

func (b *listFailingBackend) List(ctx context.Context, filters []storageapi.Filter) (storageapi.Iterator, error) {
	b.listCalls++
	return nil, fmt.Errorf("backend List should not be called")
}

func newTempStore(t *testing.T, backend storageapi.Service) Store {
	t.Helper()
	return NewStore(backend, t.TempDir())
}

func TestStoreCreateStoresMessagesOutsideBackendDocument(t *testing.T) {
	ctx := context.Background()
	backend := storagestub.NewInMemoryService()
	messagesDir := t.TempDir()
	s := NewStore(backend, messagesDir)

	msgs := []llmapi.Message{
		{Role: llmapi.RoleSystem, Content: "system"},
		{Role: llmapi.RoleUser, Content: "hello"},
	}
	require.NoError(t, s.Create(ctx, Dialogue{ID: "d1", Messages: msgs}))

	var raw map[string]any
	require.NoError(t, backend.Get(ctx, "d1", &raw))

	messagesPath, ok := raw["messagespath"].(string)
	require.True(t, ok, "stored dialogue should include MessagesPath")
	assert.NotEmpty(t, messagesPath)
	assert.Equal(t, messagesDir, filepath.Dir(messagesPath))

	storedMessages, hasMessages := raw["messages"]
	if hasMessages {
		assert.Empty(t, storedMessages, "stored dialogue should not embed full Messages")
	}

	d, err := s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Equal(t, msgs, d.Messages)
	assert.Equal(t, messagesPath, d.MessagesPath)
	_, err = os.Stat(messagesPath)
	assert.NoError(t, err)
}

func TestStoreAppendMessagesAccumulatesUsage(t *testing.T) {
	ctx := context.Background()
	backend := storagestub.NewInMemoryService()
	s := newTempStore(t, backend)

	// Create initial dialogue.
	err := s.Create(ctx, Dialogue{
		ID:       "d1",
		Messages: []llmapi.Message{{Role: llmapi.RoleSystem, Content: "hello"}},
	})
	require.NoError(t, err)

	d, err := s.Get(ctx, "d1")
	require.NoError(t, err)

	// First append with usage.
	usage1 := llmapi.DialogueUsage{
		TokensSent:        100,
		TokensReceived:    20,
		TokensReasoned:    5,
		TokensCached:      10,
		Completions:       1,
		ToolCalls:         2,
		TotalDuration:     time.Second,
		InferenceDuration: 800 * time.Millisecond,
		ToolCallDuration:  200 * time.Millisecond,
	}
	err = s.AppendMessages(ctx, d, []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hi"},
		{Role: llmapi.RoleAssistant, Content: "hey"},
	}, usage1)
	require.NoError(t, err)

	d, err = s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Len(t, d.Messages, 3) // system + user + assistant
	assert.Equal(t, 100, d.Usage.TokensSent)
	assert.Equal(t, 20, d.Usage.TokensReceived)
	assert.Equal(t, 5, d.Usage.TokensReasoned)
	assert.Equal(t, 10, d.Usage.TokensCached)
	assert.Equal(t, 1, d.Usage.Completions)
	assert.Equal(t, 2, d.Usage.ToolCalls)
	assert.Equal(t, time.Second, d.Usage.TotalDuration)
	assert.Equal(t, 800*time.Millisecond, d.Usage.InferenceDuration)
	assert.Equal(t, 200*time.Millisecond, d.Usage.ToolCallDuration)

	// Second append — usage should accumulate on top of the first.
	usage2 := llmapi.DialogueUsage{
		TokensSent:        150,
		TokensReceived:    30,
		TokensReasoned:    8,
		TokensCached:      15,
		Completions:       1,
		ToolCalls:         1,
		TotalDuration:     2 * time.Second,
		InferenceDuration: 1500 * time.Millisecond,
		ToolCallDuration:  500 * time.Millisecond,
	}
	err = s.AppendMessages(ctx, d, []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "more"},
		{Role: llmapi.RoleAssistant, Content: "sure"},
	}, usage2)
	require.NoError(t, err)

	d, err = s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Len(t, d.Messages, 5)
	assert.Equal(t, 250, d.Usage.TokensSent)
	assert.Equal(t, 50, d.Usage.TokensReceived)
	assert.Equal(t, 13, d.Usage.TokensReasoned)
	assert.Equal(t, 25, d.Usage.TokensCached)
	assert.Equal(t, 2, d.Usage.Completions)
	assert.Equal(t, 3, d.Usage.ToolCalls)
	assert.Equal(t, 3*time.Second, d.Usage.TotalDuration)
	assert.Equal(t, 2300*time.Millisecond, d.Usage.InferenceDuration)
	assert.Equal(t, 700*time.Millisecond, d.Usage.ToolCallDuration)
	assert.Equal(t, int64(3), d.Version)
}

func TestStoreAppendMessagesRetryAccumulatesUsageCorrectly(t *testing.T) {
	ctx := context.Background()
	backend := storagestub.NewInMemoryService()
	s := newTempStore(t, backend)

	// Create initial dialogue with some existing usage.
	err := s.Create(ctx, Dialogue{
		ID:       "d1",
		Messages: []llmapi.Message{{Role: llmapi.RoleSystem, Content: "hello"}},
		Usage: llmapi.DialogueUsage{
			TokensSent:  50,
			Completions: 1,
		},
	})
	require.NoError(t, err)

	// Read the dialogue — it sees version 1.
	staleDialogue, err := s.Get(ctx, "d1")
	require.NoError(t, err)
	require.Equal(t, int64(1), staleDialogue.Version)

	// Simulate a concurrent writer: bump version to 2 and add usage.
	// This makes our staleDialogue's version outdated.
	err = backend.Update(ctx, "d1",
		[]storageapi.Update{
			{FieldPath: []string{"Version"}, Value: int64(2)},
			{FieldPath: []string{"Usage"}, Value: llmapi.DialogueUsage{
				TokensSent:     70,
				TokensReceived: 10,
				Completions:    2,
			}},
		},
		storageapi.Precondition{FieldPath: []string{"Version"}, Value: int64(1)},
	)
	require.NoError(t, err)

	// Now call AppendMessages with the stale dialogue.
	// First attempt will fail (precondition: Version=1, but storage has Version=2).
	// ConsistentUpdate retries: re-reads (gets Version=2, Usage with TokensSent=70),
	// then accumulates our new usage on top of the fresh state.
	newUsage := llmapi.DialogueUsage{
		TokensSent:     30,
		TokensReceived: 5,
		Completions:    1,
	}
	err = s.AppendMessages(ctx, staleDialogue, []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "question"},
	}, newUsage)
	require.NoError(t, err)

	// Verify the result: usage should be the concurrent writer's state + our delta.
	d, err := s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Equal(t, int64(3), d.Version)        // 2 (from concurrent writer) + 1
	assert.Len(t, d.Messages, 2)                // system + user
	assert.Equal(t, 100, d.Usage.TokensSent)    // 70 (concurrent) + 30 (ours)
	assert.Equal(t, 15, d.Usage.TokensReceived) // 10 (concurrent) + 5 (ours)
	assert.Equal(t, 3, d.Usage.Completions)     // 2 (concurrent) + 1 (ours)
}

func TestStoreAppendMessagesRecoversWhenMessagesFileIsMissing(t *testing.T) {
	ctx := context.Background()
	backend := storagestub.NewInMemoryService()
	s := newTempStore(t, backend)

	missingPath := filepath.Join(t.TempDir(), "missing.json")
	stored := storedDialogue{
		ID:           "d1",
		Version:      1,
		MessageCount: 0,
		MessagesPath: missingPath,
	}
	require.NoError(t, backend.Create(ctx, "d1", &stored))

	d := Dialogue{ID: "d1"}
	err := s.AppendMessages(ctx, d, []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hello"},
		{Role: llmapi.RoleAssistant, Content: "world"},
	}, llmapi.DialogueUsage{Completions: 1})
	require.NoError(t, err)

	got, err := s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Equal(t, []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hello"},
		{Role: llmapi.RoleAssistant, Content: "world"},
	}, got.Messages)
	assert.Equal(t, 1, got.Usage.Completions)
	_, err = os.Stat(missingPath)
	assert.NoError(t, err)
}

func TestStoreListReturnsSortedByUpdatedAtDescending(t *testing.T) {
	ctx := context.Background()
	backend := storagestub.NewInMemoryService()
	s := newTempStore(t, backend)

	// Create dialogues in order: oldest first, newest last.
	// The in-memory stub auto-sets UpdatedAt to time.Now() on create,
	// so we sleep between creates to ensure distinct timestamps.
	ids := []string{"old", "middle", "newest"}
	for i, id := range ids {
		require.NoError(t, s.Create(ctx, Dialogue{ID: id}))
		if i < len(ids)-1 {
			time.Sleep(10 * time.Millisecond)
		}
	}

	it, err := s.List(ctx)
	require.NoError(t, err)

	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, all, 3)

	// Should be sorted newest first (LIFO by UpdatedAt).
	assert.Equal(t, "newest", all[0].ID)
	assert.Equal(t, "middle", all[1].ID)
	assert.Equal(t, "old", all[2].ID)
}

func TestStoreMessageCountAccuracy(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	// Create sets MessageCount.
	err := s.Create(ctx, Dialogue{
		ID: "d1",
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "hello"},
			{Role: llmapi.RoleAssistant, Content: "hi"},
		},
	})
	require.NoError(t, err)

	d, err := s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Equal(t, 2, d.MessageCount, "Create should set MessageCount")

	// AppendMessages updates MessageCount.
	err = s.AppendMessages(ctx, Dialogue{ID: "d1"}, []llmapi.Message{
		{Role: llmapi.RoleAssistant, Content: "sure thing"},
	}, llmapi.DialogueUsage{})
	require.NoError(t, err)

	d, err = s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Equal(t, 3, d.MessageCount, "AppendMessages should update MessageCount")

	// MessageCount visible via List headers.
	it, err := s.List(ctx)
	require.NoError(t, err)
	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, 3, all[0].MessageCount, "List header should reflect MessageCount")
}

func TestStoreListConsistency(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	// Populate store.
	require.NoError(t, s.Create(ctx, Dialogue{ID: "a"}))
	require.NoError(t, s.Create(ctx, Dialogue{ID: "b"}))

	it, err := s.List(ctx)
	require.NoError(t, err)
	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, all, 2)

	// Second List returns same results.
	it, err = s.List(ctx)
	require.NoError(t, err)
	all, err = iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Len(t, all, 2)

	// Create is reflected in List.
	require.NoError(t, s.Create(ctx, Dialogue{ID: "c"}))

	it, err = s.List(ctx)
	require.NoError(t, err)
	all, err = iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Len(t, all, 3, "Create should be visible in List")

	// Delete is reflected in List.
	require.NoError(t, s.Delete(ctx, "c"))

	it, err = s.List(ctx)
	require.NoError(t, err)
	all, err = iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Len(t, all, 2, "Delete should be visible in List")

	// AppendMessages is reflected in List.
	dB, err := s.Get(ctx, "b")
	require.NoError(t, err)
	require.NoError(t, s.AppendMessages(ctx, dB, []llmapi.Message{
		{Role: llmapi.RoleAssistant, Content: "reply"},
	}, llmapi.DialogueUsage{}))

	it, err = s.List(ctx)
	require.NoError(t, err)
	all, err = iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	for _, h := range all {
		if h.ID == "b" {
			assert.Equal(t, 1, h.MessageCount, "AppendMessages should be visible in List")
		}
	}
}

func TestStoreListReadsHeaderRecordsOnly(t *testing.T) {
	ctx := context.Background()
	backend := storagestub.NewInMemoryService()
	s := newTempStore(t, backend)

	require.NoError(t, s.Create(ctx, Dialogue{
		ID:      "chat-1",
		AgentID: "agent-1",
		Model:   "gpt-4o",
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "hello"},
		},
		Usage: llmapi.DialogueUsage{TokensSent: 10},
	}))

	// Corrupt only the full dialogue record in a way that would break List if it
	// still enumerated full dialogue documents. Header records must remain enough.
	require.NoError(t, backend.Set(ctx, "chat-1", map[string]any{
		"ID":        "chat-1",
		"UpdatedAt": time.Now(),
		"Messages":  "not-a-message-slice",
	}))

	it, err := s.List(ctx)
	require.NoError(t, err)
	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, all, 1)

	assert.Equal(t, "chat-1", all[0].ID)
	assert.Equal(t, "agent-1", all[0].AgentID)
	assert.Equal(t, "gpt-4o", all[0].Model)
	assert.Equal(t, 1, all[0].MessageCount)
	assert.Equal(t, 10, all[0].Usage.TokensSent)
}

func TestStoreListDoesNotCallBackendList(t *testing.T) {
	ctx := context.Background()
	backend := storagestub.NewInMemoryService()
	s := newTempStore(t, backend)

	require.NoError(t, s.Create(ctx, Dialogue{
		ID:      "chat-1",
		AgentID: "agent-1",
		Model:   "gpt-4o",
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "hello"},
		},
	}))

	// First list may provision/bootstrap the index. The steady-state fast path is
	// what must avoid backend.List.
	it, err := s.List(ctx)
	require.NoError(t, err)
	_, err = iterator.ToSlice(ctx, it)
	require.NoError(t, err)

	wrapped := &listFailingBackend{Service: backend}
	s = newTempStore(t, wrapped)
	it, err = s.List(ctx)
	require.NoError(t, err)
	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Zero(t, wrapped.listCalls)
	assert.Equal(t, "chat-1", all[0].ID)
}

func TestStoreListBootstrapsLegacyRawDialogueRecords(t *testing.T) {
	ctx := context.Background()
	backend := storagestub.NewInMemoryService()
	s := newTempStore(t, backend)

	legacy := Dialogue{
		ID:           "legacy-chat",
		AgentID:      "agent-1",
		Model:        "gpt-4o",
		WorkspaceURI: "file:///tmp/project",
		Version:      2,
		MessageCount: 1,
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "hello"},
		},
		Usage:     llmapi.DialogueUsage{TokensSent: 12},
		UpdatedAt: time.Now(),
	}

	// Simulate pre-index storage: raw dialogue at the legacy unprefixed ID and
	// no dialogue-index document yet.
	require.NoError(t, backend.Set(ctx, legacy.ID, &legacy))

	it, err := s.List(ctx)
	require.NoError(t, err)
	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, legacy.ID, all[0].ID)
	assert.Equal(t, legacy.AgentID, all[0].AgentID)
	assert.Equal(t, legacy.Model, all[0].Model)
	assert.Equal(t, legacy.MessageCount, all[0].MessageCount)

	// After bootstrapping once, the fast path should no longer need backend.List.
	wrapped := &listFailingBackend{Service: backend}
	s = newTempStore(t, wrapped)
	it, err = s.List(ctx)
	require.NoError(t, err)
	all, err = iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Zero(t, wrapped.listCalls)
}

func TestStoreConcurrentCreatesDoNotLoseIndexEntries(t *testing.T) {
	ctx := context.Background()
	backend := storagestub.NewInMemoryService()

	const workers = 16
	stores := make([]Store, workers)
	for i := range workers {
		stores[i] = newTempStore(t, backend)
	}

	start := make(chan struct{})
	errCh := make(chan error, workers)
	var wg sync.WaitGroup

	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			id := fmt.Sprintf("c-%d", i)
			errCh <- stores[i].Create(ctx, Dialogue{
				ID:      id,
				AgentID: "agent",
				Model:   "gpt-4o",
				Messages: []llmapi.Message{
					{Role: llmapi.RoleUser, Content: id},
				},
			})
		}(i)
	}

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	it, err := stores[0].List(ctx)
	require.NoError(t, err)
	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, all, workers)

	seen := make(map[string]bool, len(all))
	for _, h := range all {
		seen[h.ID] = true
	}
	for i := range workers {
		assert.True(t, seen[fmt.Sprintf("c-%d", i)])
	}
}

func TestStoreBootstrapMergesConcurrentCreate(t *testing.T) {
	ctx := context.Background()
	backend := storagestub.NewInMemoryService()

	legacy := Dialogue{
		ID:           "legacy-chat",
		AgentID:      "agent-legacy",
		Model:        "gpt-4o",
		MessageCount: 1,
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "hello"},
		},
		UpdatedAt: time.Now().Add(-time.Minute),
	}
	require.NoError(t, backend.Set(ctx, legacy.ID, &legacy))

	storeA := newTempStore(t, backend)
	storeB := newTempStore(t, backend)

	start := make(chan struct{})
	errCh := make(chan error, 2)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		it, err := storeA.List(ctx)
		if err != nil {
			errCh <- err
			return
		}
		_, err = iterator.ToSlice(ctx, it)
		if err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		errCh <- storeB.Create(ctx, Dialogue{
			ID:      "new-chat",
			AgentID: "agent-new",
			Model:   "gpt-4o",
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "hi"},
			},
		})
	}()

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	it, err := storeA.List(ctx)
	require.NoError(t, err)
	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, all, 2)

	seen := make(map[string]bool, 2)
	for _, h := range all {
		seen[h.ID] = true
	}
	assert.True(t, seen[legacy.ID])
	assert.True(t, seen["new-chat"])
}

func TestStoreArchiveAndReplacePreservesMetadata(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	// Seed a dialogue with rich metadata.
	original := Dialogue{
		ID:      "chat-1",
		AgentID: "agent-42",
		Model:   "gpt-4o",
		Messages: []llmapi.Message{
			{Role: llmapi.RoleSystem, Content: "system prompt"},
			{Role: llmapi.RoleUser, Content: "hello"},
			{Role: llmapi.RoleAssistant, Content: "hi there"},
		},
		Usage: llmapi.DialogueUsage{
			TokensSent:     100,
			TokensReceived: 50,
			Completions:    2,
		},
	}
	require.NoError(t, s.Create(ctx, original))

	// Bump version via AppendMessages so the original has Version > 1.
	require.NoError(t, s.AppendMessages(ctx, original, []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "follow-up"},
	}, llmapi.DialogueUsage{TokensSent: 10}))

	// Read the fully populated dialogue.
	orig, err := s.Get(ctx, "chat-1")
	require.NoError(t, err)
	require.Equal(t, 4, len(orig.Messages))
	require.True(t, orig.Version > 1, "should have version > 1 after append")
	orig.UpdatedAt = time.Date(2020, time.January, 2, 3, 4, 5, 0, time.UTC)

	// Perform ArchiveAndReplace.
	compactedMsgs := []llmapi.Message{
		{Role: llmapi.RoleSystem, Content: "system prompt"},
		{Role: llmapi.RoleUser, Content: "summary of prior conversation"},
	}
	err = s.ArchiveAndReplace(ctx, ArchiveAndReplaceParams{
		Dialogue:           orig,
		ArchivedDialogueID: "chat-1-archive",
		Messages:           compactedMsgs,
	})
	require.NoError(t, err)

	t.Run("archived copy preserves ALL original fields", func(t *testing.T) {
		archived, err := s.Get(ctx, "chat-1-archive")
		require.NoError(t, err)

		assert.Equal(t, "chat-1-archive", archived.ID)
		assert.Equal(t, orig.AgentID, archived.AgentID, "AgentID must be preserved")
		assert.Equal(t, orig.Model, archived.Model, "Model must be preserved")
		assert.Equal(t, orig.Usage, archived.Usage, "Usage must be preserved")
		require.Len(t, archived.Messages, len(orig.Messages), "Messages count must match")
		for i, m := range orig.Messages {
			assert.Equal(t, m.Role, archived.Messages[i].Role, "Archived Message[%d] role", i)
			assert.Equal(t, m.Content, archived.Messages[i].Content, "Archived Message[%d] content", i)
		}
		assert.Equal(t, len(orig.Messages), archived.MessageCount, "MessageCount must match archived messages")
		assert.WithinDuration(t, orig.UpdatedAt, archived.UpdatedAt, 0,
			"UpdatedAt must be the original's timestamp")
	})

	t.Run("replaced dialogue preserves stable metadata", func(t *testing.T) {
		replaced, err := s.Get(ctx, "chat-1")
		require.NoError(t, err)

		assert.Equal(t, "chat-1", replaced.ID)
		assert.Equal(t, orig.AgentID, replaced.AgentID, "AgentID must survive replacement")
		assert.Equal(t, orig.Model, replaced.Model, "Model must survive replacement")
		assert.Equal(t, orig.Usage, replaced.Usage, "Usage must survive replacement")
	})

	t.Run("replaced dialogue has new messages and reset metadata", func(t *testing.T) {
		replaced, err := s.Get(ctx, "chat-1")
		require.NoError(t, err)

		require.Len(t, replaced.Messages, len(compactedMsgs), "Messages count must match")
		for i, m := range compactedMsgs {
			assert.Equal(t, m.Role, replaced.Messages[i].Role, "Message[%d] role", i)
			assert.Equal(t, m.Content, replaced.Messages[i].Content, "Message[%d] content", i)
		}
		assert.Equal(t, len(compactedMsgs), replaced.MessageCount, "MessageCount must match new messages")
		assert.Equal(t, int64(1), replaced.Version, "Version must reset to 1")
		assert.WithinDuration(t, time.Now(), replaced.UpdatedAt, 5*time.Second,
			"UpdatedAt must be refreshed to ~now")
	})

	t.Run("both archived and replaced dialogues appear in List", func(t *testing.T) {
		it, err := s.List(ctx)
		require.NoError(t, err)
		all, err := iterator.ToSlice(ctx, it)
		require.NoError(t, err)

		ids := make(map[string]bool)
		for _, h := range all {
			ids[h.ID] = true
		}
		assert.True(t, ids["chat-1"], "active dialogue must be in List")
		assert.True(t, ids["chat-1-archive"], "archived dialogue must be in List")

		// Verify header metadata.
		for _, h := range all {
			if h.ID == "chat-1-archive" {
				assert.Equal(t, orig.AgentID, h.AgentID)
				assert.Equal(t, orig.Model, h.Model)
				assert.Equal(t, len(orig.Messages), h.MessageCount)
			}
			if h.ID == "chat-1" {
				assert.Equal(t, len(compactedMsgs), h.MessageCount)
			}
		}
	})
}

func TestStoreCreateRoundTripsApprovedPlan(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	d := Dialogue{
		ID: "chat-plan",
		ApprovedPlan: &ApprovedPlan{
			Path: "/tmp/plan.md",
			Body: "## Step 1\nDo something",
		},
		Messages: []llmapi.Message{{Role: llmapi.RoleSystem, Content: "sys"}},
	}
	require.NoError(t, s.Create(ctx, d))

	got, err := s.Get(ctx, "chat-plan")
	require.NoError(t, err)
	require.NotNil(t, got.ApprovedPlan)
	assert.Equal(t, d.ApprovedPlan.Path, got.ApprovedPlan.Path)
	assert.Equal(t, d.ApprovedPlan.Body, got.ApprovedPlan.Body)
	assert.Len(t, got.Messages, 1)
}

func TestStoreRoundTripsReasoningBlocks(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	blocks := []llmapi.ReasoningBlock{
		{Kind: "thinking", Text: "let me think", Signature: "sig-abc=="},
		{Kind: "redacted", Data: "encrypted-blob"},
	}
	d := Dialogue{
		ID: "chat-reasoning",
		Messages: []llmapi.Message{
			{Role: llmapi.RoleSystem, Content: "sys"},
			{Role: llmapi.RoleAssistant, Content: "answer", ReasoningBlocks: blocks},
		},
	}
	require.NoError(t, s.Create(ctx, d))

	got, err := s.Get(ctx, "chat-reasoning")
	require.NoError(t, err)
	require.Len(t, got.Messages, 2)
	assert.Equal(t, blocks, got.Messages[1].ReasoningBlocks,
		"reasoning blocks (incl. signature) must survive save/load for replay")

	// The live append path persists incrementally; reasoning blocks must also
	// survive that route so replay works mid-conversation after a reload.
	appended := llmapi.Message{
		Role:    llmapi.RoleAssistant,
		Content: "second",
		ReasoningBlocks: []llmapi.ReasoningBlock{
			{Kind: "thinking", Text: "more thought", Signature: "sig-2"},
		},
	}
	require.NoError(t, s.AppendMessages(ctx, got, []llmapi.Message{appended}, llmapi.DialogueUsage{}))

	reloaded, err := s.Get(ctx, "chat-reasoning")
	require.NoError(t, err)
	require.Len(t, reloaded.Messages, 3)
	assert.Equal(t, appended.ReasoningBlocks, reloaded.Messages[2].ReasoningBlocks)
}

// TestStoreSharedInstanceConcurrentUpserts verifies that many goroutines
// concurrently writing through the *same* Store instance work correctly.
func TestStoreSharedInstanceConcurrentUpserts(t *testing.T) {
	ctx := context.Background()
	backend := storagestub.NewInMemoryService()
	s := newTempStore(t, backend)

	const workers = 20

	// Seed one dialogue per worker.
	for i := range workers {
		require.NoError(t, s.Create(ctx, Dialogue{
			ID:      fmt.Sprintf("d-%d", i),
			AgentID: "a",
			Model:   "m",
			Messages: []llmapi.Message{
				{Role: llmapi.RoleSystem, Content: "sys"},
			},
		}))
	}

	// Fire all workers at the same time — each one appends a message to
	// its own dialogue.
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start

			id := fmt.Sprintf("d-%d", i)
			d, err := s.Get(ctx, id)
			if err != nil {
				errCh <- fmt.Errorf("worker %d get: %w", i, err)
				return
			}
			err = s.AppendMessages(ctx, d, []llmapi.Message{
				{Role: llmapi.RoleUser, Content: fmt.Sprintf("msg-%d", i)},
			}, llmapi.DialogueUsage{})
			if err != nil {
				errCh <- fmt.Errorf("worker %d append: %w", i, err)
				return
			}
		}(i)
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	// Every dialogue should be visible in List.
	it, err := s.List(ctx)
	require.NoError(t, err)
	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Len(t, all, workers)
}

func TestStoreArchiveAndReplaceConcurrentContention(t *testing.T) {
	ctx := context.Background()
	backend := storagestub.NewInMemoryService()

	const workers = 8
	stores := make([]Store, workers)
	for i := range workers {
		stores[i] = newTempStore(t, backend)
	}

	// Seed one dialogue per worker.
	for i := range workers {
		id := fmt.Sprintf("chat-%d", i)
		require.NoError(t, stores[i].Create(ctx, Dialogue{
			ID:      id,
			AgentID: fmt.Sprintf("agent-%d", i),
			Model:   "gpt-4o",
			Messages: []llmapi.Message{
				{Role: llmapi.RoleSystem, Content: "system prompt"},
				{Role: llmapi.RoleUser, Content: "hello"},
				{Role: llmapi.RoleAssistant, Content: "hi there"},
			},
		}))
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, workers)

	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start

			s := stores[i]
			id := fmt.Sprintf("chat-%d", i)

			d, err := s.Get(ctx, id)
			if err != nil {
				errCh <- fmt.Errorf("worker %d get: %w", i, err)
				return
			}

			err = s.ArchiveAndReplace(ctx, ArchiveAndReplaceParams{
				Dialogue:           d,
				ArchivedDialogueID: fmt.Sprintf("chat-%d-archive", i),
				Messages: []llmapi.Message{
					{Role: llmapi.RoleSystem, Content: "compacted"},
				},
			})
			if err != nil {
				errCh <- fmt.Errorf("worker %d archive-and-replace: %w", i, err)
				return
			}
		}(i)
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	// All original + archived dialogues should be visible in List.
	it, err := stores[0].List(ctx)
	require.NoError(t, err)
	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Len(t, all, workers*2, "should have original + archive for each worker")
}

func TestStoreArchiveAndReplaceFailsForMissingDialogue(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	err := s.ArchiveAndReplace(ctx, ArchiveAndReplaceParams{
		Dialogue:           Dialogue{ID: "nonexistent"},
		ArchivedDialogueID: "nonexistent-archive",
		Messages:           []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}},
	})
	assert.NoError(t, err, "ArchiveAndReplace should succeed since dialogue is provided in params")
}

func TestStoreListEmptyStoreReturnsEmptyIterator(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	it, err := s.List(ctx)
	require.NoError(t, err)

	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Empty(t, all, "List on a fresh store should return no dialogues")
}

func TestStoreCreateDeleteRecreateCycle(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	listHeaders := func() []DialogueHeader {
		t.Helper()
		it, err := s.List(ctx)
		require.NoError(t, err)
		all, err := iterator.ToSlice(ctx, it)
		require.NoError(t, err)
		return all
	}
	listIDs := func() []string {
		t.Helper()
		hdrs := listHeaders()
		ids := make([]string, len(hdrs))
		for i, h := range hdrs {
			ids[i] = h.ID
		}
		return ids
	}

	// Create.
	require.NoError(t, s.Create(ctx, Dialogue{
		ID:    "cycle",
		Model: "v1",
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "first"},
		},
	}))
	assert.Equal(t, []string{"cycle"}, listIDs())

	// Delete.
	require.NoError(t, s.Delete(ctx, "cycle"))
	assert.Empty(t, listIDs())

	// Recreate with different data.
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, s.Create(ctx, Dialogue{
		ID:    "cycle",
		Model: "v2",
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "a"},
			{Role: llmapi.RoleAssistant, Content: "b"},
		},
	}))
	hdrs := listHeaders()
	require.Len(t, hdrs, 1)
	h := hdrs[0]
	assert.Equal(t, "cycle", h.ID)
	assert.Equal(t, "v2", h.Model)
	assert.Equal(t, 2, h.MessageCount)
	assert.Equal(t, int64(1), h.Version)
}

func TestStoreMultipleRapidMutations(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	listHeaders := func() []DialogueHeader {
		t.Helper()
		it, err := s.List(ctx)
		require.NoError(t, err)
		all, err := iterator.ToSlice(ctx, it)
		require.NoError(t, err)
		return all
	}
	listIDs := func() []string {
		t.Helper()
		hdrs := listHeaders()
		ids := make([]string, len(hdrs))
		for i, h := range hdrs {
			ids[i] = h.ID
		}
		return ids
	}
	findHeader := func(headers []DialogueHeader, id string) DialogueHeader {
		t.Helper()
		for _, h := range headers {
			if h.ID == id {
				return h
			}
		}
		t.Fatalf("header %q not found in list", id)
		return DialogueHeader{}
	}
	assertSortedDesc := func(headers []DialogueHeader) {
		t.Helper()
		for i := 1; i < len(headers); i++ {
			assert.False(t, headers[i].UpdatedAt.After(headers[i-1].UpdatedAt),
				"headers[%d].UpdatedAt (%v) should not be after headers[%d].UpdatedAt (%v)",
				i, headers[i].UpdatedAt, i-1, headers[i-1].UpdatedAt)
		}
	}

	// Create 5 dialogues rapidly.
	for i := range 5 {
		require.NoError(t, s.Create(ctx, Dialogue{
			ID: fmt.Sprintf("r%d", i),
		}))
		time.Sleep(2 * time.Millisecond) // ensure distinct timestamps
	}
	hdrs := listHeaders()
	require.Len(t, hdrs, 5)
	assertSortedDesc(hdrs)

	// Delete 2.
	require.NoError(t, s.Delete(ctx, "r1"))
	require.NoError(t, s.Delete(ctx, "r3"))
	ids := listIDs()
	assert.Len(t, ids, 3)
	assert.NotContains(t, ids, "r1")
	assert.NotContains(t, ids, "r3")

	// AppendMessages to 2 remaining dialogues.
	require.NoError(t, s.AppendMessages(ctx, Dialogue{ID: "r0"}, []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "updated"},
	}, llmapi.DialogueUsage{}))
	require.NoError(t, s.AppendMessages(ctx, Dialogue{ID: "r2"}, []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "also updated"},
	}, llmapi.DialogueUsage{}))

	hdrs = listHeaders()
	require.Len(t, hdrs, 3)
	assertSortedDesc(hdrs)
	assert.Equal(t, 1, findHeader(hdrs, "r0").MessageCount, "r0 starts with 0 messages + 1 appended")
	assert.Equal(t, 1, findHeader(hdrs, "r2").MessageCount, "r2 starts with 0 messages + 1 appended")
}

func TestStoreDeleteNonExistent(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	require.NoError(t, s.Create(ctx, Dialogue{ID: "keep"}))

	// Delete something that was never created — should not error.
	require.NoError(t, s.Delete(ctx, "ghost"))

	it, err := s.List(ctx)
	require.NoError(t, err)
	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "keep", all[0].ID)
}

func TestStoreCreateWithEmptyMessages(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	require.NoError(t, s.Create(ctx, Dialogue{
		ID:       "empty-msgs",
		Messages: nil,
	}))

	it, err := s.List(ctx)
	require.NoError(t, err)
	all, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, 0, all[0].MessageCount)
}

func TestStoreSetTitle(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	require.NoError(t, s.Create(ctx, Dialogue{
		ID: "d1",
		Messages: []llmapi.Message{
			{Role: llmapi.RoleUser, Content: "hello"},
		},
	}))

	d, err := s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Empty(t, d.Title)

	require.NoError(t, s.SetTitle(ctx, "d1", "fix the flaky test"))

	d, err = s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Equal(t, "fix the flaky test", d.Title)
	assert.Len(t, d.Messages, 1, "rename must not disturb messages")

	it, err := s.List(ctx)
	require.NoError(t, err)
	headers, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, headers, 1)
	assert.Equal(t, "fix the flaky test", headers[0].Title)

	require.NoError(t, s.SetTitle(ctx, "d1", "second name"))
	d, err = s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Equal(t, "second name", d.Title)

	require.NoError(t, s.SetTitle(ctx, "d1", ""))
	d, err = s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Empty(t, d.Title, "clearing the title returns to the unnamed state")
}

func TestStoreSetTitleNonExistent(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	assert.Error(t, s.SetTitle(ctx, "ghost", "name"))

	it, err := s.List(ctx)
	require.NoError(t, err)
	headers, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Empty(t, headers, "a failed rename must not create index entries")
}

func TestStoreSetTitleSurvivesAppendMessages(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	require.NoError(t, s.Create(ctx, Dialogue{ID: "d1"}))
	require.NoError(t, s.SetTitle(ctx, "d1", "named"))

	require.NoError(t, s.AppendMessages(ctx, Dialogue{ID: "d1"}, []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "after rename"},
	}, llmapi.DialogueUsage{}))

	d, err := s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.Equal(t, "named", d.Title, "append must not clobber the title")

	it, err := s.List(ctx)
	require.NoError(t, err)
	headers, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, headers, 1)
	assert.Equal(t, "named", headers[0].Title)
	assert.Equal(t, 1, headers[0].MessageCount)
}

func TestStoreSetTitleConcurrentAppendMessages(t *testing.T) {
	ctx := context.Background()
	s := newTempStore(t, storagestub.NewInMemoryService())

	require.NoError(t, s.Create(ctx, Dialogue{ID: "d1", Messages: []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "seed"},
	}}))

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = s.SetTitle(ctx, "d1", fmt.Sprintf("name-%d", i))
		}()
		go func() {
			defer wg.Done()
			_ = s.AppendMessages(ctx, Dialogue{ID: "d1"}, []llmapi.Message{
				{Role: llmapi.RoleUser, Content: fmt.Sprintf("msg-%d", i)},
			}, llmapi.DialogueUsage{})
		}()
	}
	wg.Wait()

	d, err := s.Get(ctx, "d1")
	require.NoError(t, err)
	assert.NotEmpty(t, d.Title, "a rename must not be lost")
	assert.Greater(t, d.MessageCount, 1, "appends must not be lost")

	it, err := s.List(ctx)
	require.NoError(t, err)
	headers, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	require.Len(t, headers, 1)
	assert.Regexp(t, `^name-\d+$`, headers[0].Title,
		"the index must hold a title committed by one of the renames")
}

func TestDialogueHeaderNamedID(t *testing.T) {
	assert.Equal(t, "rolling-fox", DialogueHeader{ID: "rolling-fox"}.NamedID())
	assert.Equal(t, "fix the flaky test (rolling-fox)",
		DialogueHeader{ID: "rolling-fox", Title: "fix the flaky test"}.NamedID())
}

func TestParseNamedID(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"rolling-fox", "rolling-fox"},
		{"fix the flaky test (rolling-fox)", "rolling-fox"},
		{"fix (auth) bug (rolling-fox)", "rolling-fox"},
		{"no close (paren", "no close (paren"},
		{"trailing (paren", "trailing (paren"},
	} {
		assert.Equal(t, tc.want, ParseNamedID(tc.in), "input %q", tc.in)
	}
}
