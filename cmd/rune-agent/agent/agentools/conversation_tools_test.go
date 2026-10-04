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
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
)

// stubConversationStore is a minimal in-memory Store for testing the
// conversation introspection tools.
type stubConversationStore struct {
	dialoguemanager.Store
	headers []dialoguemanager.DialogueHeader
	listErr error
}

func (s *stubConversationStore) List(_ context.Context) (iterator.Iterator[dialoguemanager.DialogueHeader], error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return iterator.FromSlice(s.headers), nil
}

func newStubStore(headers ...dialoguemanager.DialogueHeader) *stubConversationStore {
	return &stubConversationStore{headers: headers}
}

// ---------------------------------------------------------------------------
// list_conversations tests
// ---------------------------------------------------------------------------

func TestListConversations_Empty(t *testing.T) {
	t.Parallel()
	store := newStubStore()
	tool := &listConversationsTool{store: store, sessionsDir: "/tmp/sessions"}
	result := tool.Execute(context.Background(), "{}")
	assert.False(t, result.IsError)
	assert.Equal(t, "No conversations found.", result.Content)
}

func TestListConversations_FiltersAudit(t *testing.T) {
	t.Parallel()
	now := time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
	sessionsDir := "/data/sessions"
	store := newStubStore(
		dialoguemanager.DialogueHeader{
			ID:           "conv-1",
			WorkspaceURI: "file:///home/user/project",
			UpdatedAt:    now,
		},
		dialoguemanager.DialogueHeader{
			ID:        "audit:abc123",
			UpdatedAt: now,
		},
		dialoguemanager.DialogueHeader{
			ID:           "conv-2",
			WorkspaceURI: "file:///home/user/other",
			UpdatedAt:    now.Add(-time.Hour),
		},
	)

	tool := &listConversationsTool{store: store, sessionsDir: sessionsDir}
	result := tool.Execute(context.Background(), "{}")
	require.False(t, result.IsError)

	// Should contain both conversations with workspace and path, but not audit.
	assert.Contains(t, result.Content, "conv-1")
	assert.Contains(t, result.Content, "conv-2")
	assert.NotContains(t, result.Content, "audit:")
	assert.Contains(t, result.Content, "workspace=file:///home/user/project")
	assert.Contains(t, result.Content, "path=")
	assert.Contains(t, result.Content, ".json")
	// Two lines: one per non-audit conversation.
	lines := strings.Split(result.Content, "\n")
	assert.Equal(t, 2, len(lines))
}

func TestListConversations_ListError(t *testing.T) {
	t.Parallel()
	store := &stubConversationStore{listErr: fmt.Errorf("db unavailable")}
	tool := &listConversationsTool{store: store, sessionsDir: "/tmp/sessions"}
	result := tool.Execute(context.Background(), "{}")
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "db unavailable")
}

// ---------------------------------------------------------------------------
// search_conversations tests
// ---------------------------------------------------------------------------

// writeSessionFile writes a JSON session file in the sessions dir with the
// given dialogue ID. The file name is base64url(id) + ".json".
func writeSessionFile(t *testing.T, sessionsDir, id string, msgs []llmapi.Message) {
	t.Helper()
	encoded := base64.RawURLEncoding.EncodeToString([]byte(id))
	path := filepath.Join(sessionsDir, encoded+".json")
	data, err := json.Marshal(msgs)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

func TestSearchConversations_MatchesAcrossConversations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSessionFile(t, dir, "conv-1", []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "Fix the bug in handler.go"},
	})
	writeSessionFile(t, dir, "conv-2", []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "No bugs here"},
	})

	tool := &searchConversationsTool{fs: localFS{root: dir}, sessionsDir: dir}
	result := tool.Execute(context.Background(), `{"pattern":"bug"}`)
	require.False(t, result.IsError)

	// Should match lines in session files.
	assert.Contains(t, result.Content, "bug")
	// Output lines should follow the "path:line:content" walkdir format.
	lines := strings.Split(strings.TrimRight(result.Content, "\n"), "\n")
	assert.GreaterOrEqual(t, len(lines), 2)
}

func TestSearchConversations_MatchesInLargeSingleLineSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Build a single user message whose Content exceeds 64 KiB so that the
	// resulting JSON is one line well past bufio.MaxScanTokenSize.
	padding := strings.Repeat("padding ", (bufio.MaxScanTokenSize/8)+1)
	writeSessionFile(t, dir, "big-conv", []llmapi.Message{
		{Role: llmapi.RoleUser, Content: padding + " needle-token " + padding},
	})

	tool := &searchConversationsTool{fs: localFS{root: dir}, sessionsDir: dir}
	result := tool.Execute(context.Background(), `{"pattern":"needle-token"}`)
	require.False(t, result.IsError)
	assert.Contains(t, result.Content, "needle-token",
		"search_conversations should match patterns inside session JSON that "+
			"is serialized as a single line larger than bufio.MaxScanTokenSize")
}

func TestSearchConversations_NoMatches(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSessionFile(t, dir, "conv-1", []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hello world"},
	})

	tool := &searchConversationsTool{fs: localFS{root: dir}, sessionsDir: dir}
	result := tool.Execute(context.Background(), `{"pattern":"zzz_nonexistent"}`)
	assert.False(t, result.IsError)
	assert.Equal(t, "No matches found.", result.Content)
}

func TestSearchConversations_InvalidRegex(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tool := &searchConversationsTool{fs: localFS{root: dir}, sessionsDir: dir}
	result := tool.Execute(context.Background(), `{"pattern":"[invalid"}`)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "invalid regex")
}

func TestSearchConversations_MissingPattern(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tool := &searchConversationsTool{fs: localFS{root: dir}, sessionsDir: dir}
	result := tool.Execute(context.Background(), `{}`)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "pattern is required")
}

func TestSearchConversations_FiltersAudit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeSessionFile(t, dir, "conv-1", []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hello"},
	})
	writeSessionFile(t, dir, "audit:xyz", []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hello"},
	})

	tool := &searchConversationsTool{fs: localFS{root: dir}, sessionsDir: dir}
	result := tool.Execute(context.Background(), `{"pattern":"hello"}`)
	require.False(t, result.IsError)

	// Only the conv-1 session file should be searched.
	assert.Contains(t, result.Content, "hello")
	// The audit session file's name is base64-encoded; verify it's not in results.
	auditEncoded := base64.RawURLEncoding.EncodeToString([]byte("audit:xyz"))
	assert.NotContains(t, result.Content, auditEncoded)
}

func TestSearchConversations_CapsResults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create a session file with enough matching lines to exceed maxSearchResults.
	var sb strings.Builder
	for i := 0; i < maxSearchResults+50; i++ {
		if i > 0 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "match line %d", i)
	}
	// Write raw text as the file content (not JSON) so each line is searchable.
	encoded := base64.RawURLEncoding.EncodeToString([]byte("conv-big"))
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, encoded+".json"),
		[]byte(sb.String()),
		0o644,
	))

	tool := &searchConversationsTool{fs: localFS{root: dir}, sessionsDir: dir}
	result := tool.Execute(context.Background(), `{"pattern":"match line"}`)
	require.False(t, result.IsError)

	assert.Contains(t, result.Content, "(results capped at 200 matches)")
	// Count result lines (excluding the cap message).
	content := strings.TrimSuffix(result.Content, fmt.Sprintf("\n\n(results capped at %d matches)", maxSearchResults))
	lines := strings.Split(content, "\n")
	assert.Equal(t, maxSearchResults, len(lines))
}

func TestSearchConversations_Summary(t *testing.T) {
	t.Parallel()
	tool := &searchConversationsTool{}
	assert.Equal(t, "bug", tool.Summary(`{"pattern":"bug"}`))
	assert.Equal(t, "", tool.Summary(`invalid`))
}

// ---------------------------------------------------------------------------
// ConversationTools factory test
// ---------------------------------------------------------------------------

func TestConversationTools_ReturnsTwoTools(t *testing.T) {
	t.Parallel()
	store := newStubStore()
	dir := t.TempDir()
	tools := ConversationTools(store, localFS{root: dir}, dir)
	require.Len(t, tools, 2)

	names := make([]string, len(tools))
	for i, tool := range tools {
		names[i] = tool.Definition().Function.Name
	}
	assert.Contains(t, names, "list_conversations")
	assert.Contains(t, names, "search_conversations")
}
