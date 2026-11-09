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

package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
)

func TestConversation(t *testing.T) {
	base := WithDialogueID(context.Background(), "dialogue")
	require.Nil(t, Conversation(base))
	for _, messages := range [][]llmapi.Message{nil, {}, {{Role: llmapi.RoleUser, Content: "hello"}}} {
		ctx := WithConversation(base, messages)
		assert.Equal(t, messages, Conversation(ctx))
		assert.Equal(t, "dialogue", DialogueIDFromContext(ctx))
		assert.Nil(t, Conversation(base))
	}
}

func TestConversationToolContext(t *testing.T) {
	attachment := llmapi.ContentPart{Type: llmapi.ContentPartTypeImageURL, ImageURL: "data:image/png;base64,YXR0YWNobWVudA=="}
	generated := llmapi.ContentPart{Type: llmapi.ContentPartTypeImageURL, ImageURL: "data:image/png;base64,Z2VuZXJhdGVk"}
	var conversations [][]llmapi.Message
	var ids []string
	tool := &mockTool{
		name: "capture",
		executeFn: func(ctx context.Context, _ string) ToolResult {
			conversations = append(conversations, append([]llmapi.Message(nil), Conversation(ctx)...))
			ids = append(ids, DialogueIDFromContext(ctx))
			return ToolResult{Content: "image", MultiContent: []llmapi.ContentPart{generated}}
		},
	}
	svc := &mockService{responses: []mockResponse{
		toolCallResponse("capture", "{}", "c1"),
		toolCallResponse("capture", "{}", "c2"),
		stopResponse("done"),
	}}
	ag := NewAgent(svc, NewRegistry(tool), noSkills(), newMockStore(), NoMemory(), Config{SystemPrompt: "test"})
	events := collectEvents(t, ag.Run(context.Background(), "images", "draw", WithAttachments([]llmapi.ContentPart{attachment})))
	require.False(t, hasEventType(events, EventError))
	require.Len(t, conversations, 2)
	assert.Equal(t, []string{"images", "images"}, ids)
	for i, messages := range conversations {
		var images []llmapi.ContentPart
		for _, message := range messages {
			for _, part := range message.MultiContent {
				if part.Type == llmapi.ContentPartTypeImageURL {
					images = append(images, part)
				}
			}
		}
		want := []llmapi.ContentPart{attachment}
		if i > 0 {
			want = append(want, generated)
		}
		assert.Equal(t, want, images)
	}
}

func TestConversationOrderedToolContext(t *testing.T) {
	image := llmapi.ContentPart{Type: llmapi.ContentPartTypeImageURL, ImageURL: "data:image/png;base64,YQ=="}
	var seen []llmapi.ContentPart
	tool := &mockTool{name: "images", needsOrder: true, executeFn: func(ctx context.Context, _ string) ToolResult {
		for _, message := range Conversation(ctx) {
			if message.Role == llmapi.RoleUser {
				for _, part := range message.MultiContent {
					if part.Type == llmapi.ContentPartTypeImageURL {
						seen = append(seen, part)
					}
				}
			}
		}
		return ToolResult{MultiContent: []llmapi.ContentPart{image}}
	}}
	response := toolCallResponse("images", "{}", "c1")
	response.toolCalls = append(response.toolCalls, toolCallResponse("images", "{}", "c2").toolCalls...)
	svc := &mockService{responses: []mockResponse{response, stopResponse("done")}}
	ag := NewAgent(svc, NewRegistry(tool), noSkills(), newMockStore(), NoMemory(), Config{SystemPrompt: "test"})
	events := collectEvents(t, ag.Run(context.Background(), "images", "draw and edit"))
	require.False(t, hasEventType(events, EventError))
	assert.Equal(t, []llmapi.ContentPart{image}, seen)
}
