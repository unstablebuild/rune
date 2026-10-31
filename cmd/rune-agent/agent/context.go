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

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/hooks"
)

type contextKey int

const (
	parentToolCallIDKey contextKey = iota
	currentModelKey
	hooksRunnerKey
	workspaceURIKey
	dialogueIDKey
	prompterKey
	conversationKey
)

// WithParentToolCallID returns a context carrying the parent tool call ID.
func WithParentToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, parentToolCallIDKey, id)
}

// ParentToolCallID extracts the parent tool call ID from the context.
func ParentToolCallID(ctx context.Context) string {
	v, _ := ctx.Value(parentToolCallIDKey).(string)
	return v
}

// WithCurrentModel returns a context carrying the current model entry.
func WithCurrentModel(ctx context.Context, model llmapi.ModelEntry) context.Context {
	return context.WithValue(ctx, currentModelKey, model)
}

// CurrentModel extracts the current model entry from the context.
func CurrentModel(ctx context.Context) llmapi.ModelEntry {
	v, _ := ctx.Value(currentModelKey).(llmapi.ModelEntry)
	return v
}

// WithHooks returns a context carrying the agent's hook runner.
func WithHooks(ctx context.Context, r *hooks.Runner) context.Context {
	return context.WithValue(ctx, hooksRunnerKey, r)
}

// HooksFromContext extracts the hook runner from the context.
func HooksFromContext(ctx context.Context) *hooks.Runner {
	v, _ := ctx.Value(hooksRunnerKey).(*hooks.Runner)
	return v
}

// WithWorkspaceURI returns a context carrying the agent's workspace
// URI. The URI is intentionally not collapsed to a string so callers
// see that it may be non-local (e.g. ssh://) and is unsafe to feed
// straight into local filesystem APIs.
func WithWorkspaceURI(ctx context.Context, uri workspaceapi.URI) context.Context {
	return context.WithValue(ctx, workspaceURIKey, uri)
}

// WorkspaceURIFromContext extracts the workspace URI from the
// context. Returns the zero URI when none was attached.
func WorkspaceURIFromContext(ctx context.Context) workspaceapi.URI {
	v, _ := ctx.Value(workspaceURIKey).(workspaceapi.URI)
	return v
}

// WithDialogueID returns a context carrying the parent dialogue ID.
func WithDialogueID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, dialogueIDKey, id)
}

// DialogueIDFromContext extracts the dialogue ID from the context.
func DialogueIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(dialogueIDKey).(string)
	return v
}

// WithConversation carries the live conversation, including unpersisted messages.
// The messages and their nested content must be treated as read-only by callers.
func WithConversation(ctx context.Context, messages []llmapi.Message) context.Context {
	return context.WithValue(ctx, conversationKey, messages)
}

// Conversation returns the read-only conversation, or nil when none was attached.
func Conversation(ctx context.Context) []llmapi.Message {
	v, _ := ctx.Value(conversationKey).([]llmapi.Message)
	return v
}

// WithPrompter returns a context carrying the agent's prompter so that
// tools (e.g. bash) can block and ask the user a question while the
// agent loop is running. A nil prompter is allowed; tools should treat
// it as "no prompter available".
func WithPrompter(ctx context.Context, p Prompter) context.Context {
	return context.WithValue(ctx, prompterKey, p)
}

// PrompterFromContext extracts the prompter from the context. Returns
// nil when none was attached.
func PrompterFromContext(ctx context.Context) Prompter {
	v, _ := ctx.Value(prompterKey).(Prompter)
	return v
}
