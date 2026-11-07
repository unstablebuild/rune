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
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
)

// Spawner abstracts the ability to run sub-agents.
type Spawner interface {
	Run(ctx context.Context, req RunRequest) (RunHandle, error)
}

// RunRequest is a request to run a sub-agent.
type RunRequest struct {
	Message string
	// DisplayMessage is persisted and shown in transcripts when it differs
	// from the model-facing Message.
	DisplayMessage string
	// Attachments are ordered content parts appended after Message.
	Attachments []llmapi.ContentPart
	// AdditionalContext is prepended only to the provider request.
	AdditionalContext string
	Label             string
	AgentID           string
	// Skill, when set, names the agent skill the sub-agent runs, which
	// gives it the skill's body as system prompt and the skill's tool
	// policy. Unlike an AgentID, it never names an agent definition, so
	// that one sharing the skill's name cannot run without the skill's
	// restrictions. AgentID is then ignored.
	Skill string
	Model string
	// Tools restricts the tools of the sub-agent. It is combined with the
	// restrictions of the caller, which the context of Run carries (see
	// WithToolPolicy), so a sub-agent never gets more than its parent.
	Tools        toolpolicy.Policy
	SystemPrompt string // if non-empty, overrides the agent definition's prompt
	Cleanup      string // "delete" or "keep"

	// InitialMessages, when non-empty, are prepended to the child
	// dialogue (after the child agent's own system prompt) before the
	// current Message is appended. This is used to seed a sub-agent with
	// a snapshot of the parent dialogue so it can act on existing
	// conversation context (see skills.Skill.ContextSharing).
	InitialMessages []llmapi.Message
}

// RunHandle is the result of a Run call.
type RunHandle struct {
	SessionKey string
	DialogueID string
	Label      string
	Events     iterator.Iterator[Event]
}

// AgentSummary describes an agent for listing purposes.
//
//nolint:revive // Preserved imported API name for compatibility and clarity.
type AgentSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ChildEvent wraps an agent Event with the parent tool call ID
// that spawned the sub-agent, so the TUI can nest it.
type ChildEvent struct {
	ParentToolCallID string
	Event            Event
}
