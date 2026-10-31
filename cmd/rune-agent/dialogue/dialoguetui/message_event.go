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

package dialoguetui

import "time"

// MessageEventType describes the kind of structured event sent to the TUI.
type MessageEventType int

const (
	// MessageEventText is a text chunk from the assistant.
	MessageEventText MessageEventType = iota
	// MessageEventBreak signals the end of the current message.
	MessageEventBreak
	// MessageEventToolCall signals a tool invocation has started.
	MessageEventToolCall
	// MessageEventToolResult signals a tool invocation has completed.
	MessageEventToolResult
	// MessageEventReasoning is a reasoning chunk from reasoning models.
	MessageEventReasoning
	// MessageEventError is an error message to display inline.
	MessageEventError
	// MessageEventPrompt shows a selection UI for user input.
	MessageEventPrompt
	// MessageEventPromptDismiss removes an active prompt.
	MessageEventPromptDismiss
	// MessageEventWarning is a non-fatal warning message to display inline.
	MessageEventWarning
	// MessageEventToolsDropped signals that tool results were dropped from history.
	MessageEventToolsDropped
	// MessageEventMemoryRecall signals a memory was recalled for this conversation.
	MessageEventMemoryRecall
	// MessageEventTaskProgress signals a task progress update.
	MessageEventTaskProgress
	// MessageEventBusy signals the start (Busy=true) or end (Busy=false) of
	// an active agent completion. The dialogue handler uses this to decide
	// whether to queue follow-up messages or send them directly.
	MessageEventBusy
	// MessageEventChildResult signals that a sub-agent finished and its
	// final result should be displayed as a child leaf node under the
	// parent tool call.
	MessageEventChildResult
	// MessageEventCommand injects an in-chat slash command into the
	// dialogue, running it exactly as if the user had typed it. Used to
	// route workspace command-prompt commands to the focused chat.
	MessageEventCommand
	// MessageEventAttachment adds a pending attachment to the compose
	// strip, as if the user had picked it from the '#' completion.
	MessageEventAttachment
)

// MessageEvent is a structured event sent through the display channel.
type MessageEvent struct {
	Type               MessageEventType
	Text               string              // MessageEventText: text chunk
	ToolCallID         string              // MessageEventToolCall / MessageEventToolResult: unique call identifier
	ToolName           string              // MessageEventToolCall / MessageEventToolResult
	ToolArgs           string              // MessageEventToolCall: JSON arguments
	ToolSummary        string              // MessageEventToolCall / MessageEventToolResult: short human-readable args summary
	ToolOutput         string              // MessageEventToolResult: execution output
	ImagePaths         []string            // MessageEventToolResult: image paths, captioned with ToolSummary (or filename when empty)
	IsError            bool                // MessageEventToolResult: whether the tool errored
	ToolStartTime      time.Time           // MessageEventToolCall: when the tool call was announced
	ToolDuration       time.Duration       // MessageEventToolResult: how long the tool took
	ParentToolCallID   string              // if set, this is a child event nested under this parent tool call
	DroppedToolCallIDs []string            // MessageEventToolsDropped: tool call IDs removed from history
	Memories           []MemoryRecallEntry // MessageEventMemoryRecall: recalled memories
	MemoryDuration     time.Duration       // MessageEventMemoryRecall: how long recall took
	TaskProgress       ProgressTaskEntry   // MessageEventTaskProgress: single task update

	// Prompt fields (MessageEventPrompt)
	PromptTitle       string
	PromptHeader      string
	PromptBody        string // optional markdown content shown before options
	PromptOptions     []PromptEventOption
	PromptMultiSelect bool
	PromptResult      chan<- []string // side-channel for response

	// Busy field (MessageEventBusy): true when the agent starts processing,
	// false when it finishes.
	Busy bool

	// Command fields (MessageEventCommand)
	CommandName string
	CommandArgs []string

	// Attachment field (MessageEventAttachment)
	Attachment Attachment
}

// MemoryRecallEntry represents a single memory in a recall event.
type MemoryRecallEntry struct {
	ID      string
	Content string
}

// PromptEventOption describes an option in a prompt event.
type PromptEventOption struct {
	Label       string
	Description string
	// RequiresInput indicates that selecting this option should
	// open a text input box for the user to type a response.
	RequiresInput bool
}
