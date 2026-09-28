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

import (
	"time"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/text"
)

// ComponentConfig holds configuration options for dialogue.Component.
type ComponentConfig struct {
	// MessagesRowConfig determines the positioning of the messages span
	// within dialogue component.
	MessagesRowConfig component.SpanConfig
	// InputRowColumns determines the width of the input row in columns,
	// from 1 to component.MaxCols. A zero value uses the default of 10.
	InputRowColumns int
	// SendMessageStringConfig determines the StringConfig of the sent messages in
	// the messages component.
	SendMessageStringConfig component.StringConfig
	// SendMessageSpanConfig allows for clients to add padding to send
	// messages and control its alignent in regards of this padding.
	SendMessageSpanConfig component.SpanConfig
	// SendMessageBottomPad adds N rows of empty space after each send
	// message in the messages list. Unlike SpanConfig.PadVertical this
	// padding lives outside the message's background, preventing the
	// background color from leaking into the gap.
	SendMessageBottomPad int

	// QueuedMessageStringConfig determines the StringConfig for queued
	// (pending) messages displayed while the agent is busy.
	QueuedMessageStringConfig component.StringConfig
	// QueuedMessageSpanConfig allows for clients to add padding to queued
	// messages and control their alignment.
	QueuedMessageSpanConfig component.SpanConfig
	// QueuedMessagePrefix is the prefix shown before each queued message.
	// Defaults to "󰄝 " when empty.
	QueuedMessagePrefix string

	// ReceiveMessageStringConfig determines the StringConfig of the received
	// messages in the messages component.
	ReceiveMessageStringConfig component.StringConfig
	// ReceiveMessageSpanConfig allows for clients to add padding to receive
	// messages and control its alignent in regards of this padding.
	ReceiveMessageSpanConfig component.SpanConfig
	// ReceiveMessageBottomPad adds N rows of empty space after each
	// receive message in the messages list. Unlike SpanConfig.PadVertical
	// this padding lives outside the message's background, preventing
	// the background color from leaking into the gap.
	ReceiveMessageBottomPad int

	// ReasoningStringConfig determines the StringConfig for reasoning text
	// from reasoning models.
	ReasoningStringConfig component.StringConfig
	// ReasoningSpanConfig allows for clients to add padding to reasoning
	// text and control its alignment.
	ReasoningSpanConfig component.SpanConfig

	// ToolCallStringConfig determines the StringConfig for tool call headers
	// (e.g. "⚙ read_file").
	ToolCallStringConfig component.StringConfig
	// ToolCallSpanConfig allows for clients to add padding to tool call
	// headers and control their alignment.
	ToolCallSpanConfig component.SpanConfig
	// ToolCallArgsStringConfig determines the StringConfig for tool call
	// arguments displayed below the tool header.
	ToolCallArgsStringConfig component.StringConfig
	// ToolResultStringConfig determines the StringConfig for tool result text.
	ToolResultStringConfig component.StringConfig
	// ToolResultSpanConfig allows for clients to add padding to tool result
	// text and control its alignment.
	ToolResultSpanConfig component.SpanConfig
	// ToolResultMaxLines is the maximum number of lines of tool output shown.
	// A zero value defaults to 5.
	ToolResultMaxLines int

	// ErrorStringConfig determines the StringConfig for inline error messages.
	ErrorStringConfig component.StringConfig
	// ErrorSpanConfig allows for clients to add padding to error messages
	// and control their alignment.
	ErrorSpanConfig component.SpanConfig

	// WarningStringConfig determines the StringConfig for inline warning messages.
	WarningStringConfig component.StringConfig
	// WarningSpanConfig allows for clients to add padding to warning messages
	// and control their alignment.
	WarningSpanConfig component.SpanConfig

	// CommandOutputSpanConfig allows for clients to add padding to command output
	// and control its alignment.
	CommandOutputSpanConfig component.SpanConfig

	// ReasoningAnnotationStringConfig determines the StringConfig for the
	// reasoning toggle annotation (e.g. "ctrl-o to collapse").
	ReasoningAnnotationStringConfig component.StringConfig

	// CollapsedTreeAttr determines the attributes for tree connectors
	// (├─, └─, │) in the collapsed tool view. Defaults to gray.
	CollapsedTreeAttr term.Attributes
	// CollapsedSuccessAttr determines the attributes for the success
	// icon (✓) in the collapsed tool view. Defaults to green.
	CollapsedSuccessAttr term.Attributes
	// CollapsedErrorAttr determines the attributes for the error
	// icon (✗) in the collapsed tool view. Defaults to red.
	CollapsedErrorAttr term.Attributes
	// CollapsedToolNameAttr determines the attributes for the tool
	// name in the collapsed tool view. Defaults to bold.
	CollapsedToolNameAttr term.Attributes
	// CollapsedToolArgsAttr determines the attributes for tool
	// arguments and summary in the collapsed tool view. Defaults to olive.
	CollapsedToolArgsAttr term.Attributes

	// PromptToolCallStringConfig determines the StringConfig for the
	// ask_user_question tool call header ("? ask_user_question").
	PromptToolCallStringConfig component.StringConfig
	// CollapsedDroppedAttr determines the attributes for the icon
	// of a tool call whose result was dropped from history.
	// Defaults to gray.
	CollapsedDroppedAttr term.Attributes
	// CollapsedPromptAttr determines the attributes for the prompt
	// icon (?) in the collapsed tool view. Defaults to aqua.
	CollapsedPromptAttr term.Attributes
	// CollapsedMemoryAttr determines the attributes for the memory
	// icon (󰍛) in the collapsed tool view. Defaults to purple.
	CollapsedMemoryAttr term.Attributes
	// CollapsedResultAttr determines the attributes for the sub-agent
	// result icon (󰮹) in the collapsed tool view. Defaults to green.
	CollapsedResultAttr term.Attributes
	// CollapsedResultErrorAttr determines the attributes for the sub-agent
	// error result icon (󱑑) in the collapsed tool view. Defaults to red.
	CollapsedResultErrorAttr term.Attributes
	// MemoryIDStringConfig determines the StringConfig for the memory
	// ID text displayed in both expanded and collapsed views.
	MemoryIDStringConfig component.StringConfig
	// MemoryContentStringConfig determines the StringConfig for the
	// memory content text displayed in expanded view.
	MemoryContentStringConfig component.StringConfig

	// SelectionConfig holds styling for inline Selection prompts.
	SelectionConfig SelectionConfig

	// MarkdownConfig, when non-nil, configures the markdown renderer
	// used for received messages. A nil value uses markdown.DefaultConfig().
	MarkdownConfig *markdown.Config

	// ReasoningMarkdownConfig, when non-nil, configures the markdown
	// renderer used for reasoning text. A nil value derives a config from
	// MarkdownConfig styled with ReasoningStringConfig's attributes.
	ReasoningMarkdownConfig *markdown.Config

	// DurationPrecision, when positive, truncates tool call durations
	// to this precision (e.g. time.Second shows "1s" instead of "1.234s").
	// Zero uses the default adaptive rounding.
	DurationPrecision time.Duration

	// StartCollapsed, when true, causes tool call turns to render
	// in collapsed tree view by default. The user can toggle with ctrl-o.
	StartCollapsed bool

	// BackgroundColor, when set to a valid color, fills every cell with
	// this background color before drawing content.
	BackgroundColor term.Color

	InputBox InputBoxConfig

	// Editor, when non-nil, is used to compose the message to the
	// assistant via the user's configured editor (modal/modeless). When
	// nil, the component falls back to a default modeless editor.
	Editor text.Editor

	// EditorModal reports whether Editor is a modal (vi-style) editor.
	// It distinguishes modal from modeless compose editors so a bare
	// <Enter> can submit in normal mode while inserting a newline in
	// insert mode. Ignored when Editor is nil.
	EditorModal bool

	// ModalStartInsert, when true, makes a modal compose editor enter
	// insert mode at startup so the user can type immediately. Ignored
	// for modeless editors.
	ModalStartInsert bool

	// InputBackgroundColor, when valid, sets the background color of the
	// compose input: the editor's scroll area (via SetDefaultAttributes)
	// and the surrounding Frame. ColorDefault leaves both at the
	// terminal default.
	InputBackgroundColor term.Color

	// CompletionMatchedTextAttr styles the fuzzy-matched substring within
	// each '#' completion candidate. Mirrors the command overlay default.
	CompletionMatchedTextAttr term.Attributes
	// CompletionFocusElementAttr styles the currently selected candidate
	// row in the '#' completion list. Mirrors the command overlay default.
	CompletionFocusElementAttr term.Attributes
	// CompletionElementAttr styles non-selected candidate rows in the '#'
	// completion list.
	CompletionElementAttr term.Attributes
	// CompletionRadarColor is the sweep color of the radar-frame shader
	// drawn over the compose box while '#' candidates stream in. A
	// ColorDefault value uses the radar frame's built-in default.
	CompletionRadarColor term.Color
	// InlineAttachmentAttr styles the inline label left in the compose
	// text by an accepted '#' completion, for as long as that label is
	// still linked to a pending attachment.
	InlineAttachmentAttr term.Attributes

	// StatusBar configures the dialogue's bottom status row. A
	// zero value leaves the bar disabled and the layout unchanged.
	StatusBar StatusBarConfig
}

// InputBoxConfig holds styling configuration for the compose input
// frame and the prompt feedback inputbox.
type InputBoxConfig struct {
	Placeholder       string
	PlaceholderConfig component.StringResponsiveConfig
	FrameAttr         term.Attributes
	FrameCharSet      component.FrameCharSet
	ContentAttr       term.Attributes
}
